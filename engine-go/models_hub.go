package main

// Каталог GGUF с Hugging Face и посадка модели в settings.ini.
// Список как в LM Studio: поиск, производитель, квант, зрение/инструменты/размышления,
// и вердикт «целиком GPU / гибрид / CPU» по свежему тесту железа.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

const (
	hfAPIDefault    = "https://huggingface.co"
	catalogTTL      = 8 * time.Minute
	hfSearchLimit   = 36
	hfExpandWorkers = 6
)

// settingsINIPath — куда assignBrainModel пишет выбранную модель. var, чтобы
// тесты подменяли на temp (та же схема, что у modelsBrainDir).
var settingsINIPath = "settings.ini"

// modelsBrainDir — каталог локальных GGUF. var, чтобы тесты подменяли на temp.
var modelsBrainDir = "models/brain"

var hfHTTP = &http.Client{Timeout: 28 * time.Second}
var hfDownloadHTTP = &http.Client{Timeout: 0} // большой файл; отмена через context
var hfBase = hfAPIDefault

// liveBrainModel — файл, который llama-server реально держит в VRAM. После assign
// settings.ini уже другой, а процесс мозга — старый, пока не перезапустят стек.
// Пишется из старта и горутины переключения, читается из HTTP/Telegram-хендлеров —
// доступ через atomic, чтобы не было data race.
var liveBrainModel atomic.Value // string

func setLiveBrainModel(name string) { liveBrainModel.Store(name) }

func liveBrainModelNow() string {
	if v := liveBrainModel.Load(); v != nil {
		s, _ := v.(string)
		return s
	}
	return ""
}

// HubModel — одна карточка репозитория (семейство + кванты).
type HubModel struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Publisher    string    `json:"publisher"`
	Downloads    int       `json:"downloads"`
	Likes        int       `json:"likes"`
	Tags         []string  `json:"tags,omitempty"`
	Pipeline     string    `json:"pipeline,omitempty"`
	Vision       bool      `json:"vision"`
	Tools        bool      `json:"tools"`
	Reasoning    bool      `json:"reasoning"`
	Params       string    `json:"params,omitempty"`
	Files        []HubFile `json:"files"`
	URL          string    `json:"url"`
	Local        bool      `json:"local,omitempty"`
	BestFit      string    `json:"best_fit,omitempty"`
	HasMMProj    bool      `json:"has_mmproj"`
	LastModified string    `json:"last_modified,omitempty"`
}

// HubFile — один GGUF (квант или mmproj).
type HubFile struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	SizeGB    float64   `json:"size_gb"`
	Quant     string    `json:"quant,omitempty"`
	Kind      string    `json:"kind"` // weights | mmproj
	Fit       FitResult `json:"fit"`
	Local     bool      `json:"local"`
	LocalPath string    `json:"local_path,omitempty"`
	Active    bool      `json:"active"`
}

type hfListItem struct {
	ID           string   `json:"id"`
	PipelineTag  string   `json:"pipeline_tag"`
	Downloads    int      `json:"downloads"`
	Likes        int      `json:"likes"`
	Tags         []string `json:"tags"`
	LastModified string   `json:"lastModified"`
}

type hfTreeItem struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	LFS  *struct {
		Size int64 `json:"size"`
	} `json:"lfs"`
}

type catalogEntry struct {
	at     time.Time
	models []HubModel
}

var (
	catalogMu    sync.Mutex
	catalogCache = map[string]catalogEntry{}
)

func cacheKey(q, publisher, fit string, vision, tools, reasoning bool) string {
	return strings.ToLower(q) + "|" + strings.ToLower(publisher) + "|" + fit + "|" +
		fmt.Sprintf("%t:%t:%t", vision, tools, reasoning)
}

// searchHubModels тянет GGUF с Hugging Face и вешает на каждый файл вердикт по железу.
func searchHubModels(q, publisher, fit string, vision, tools, reasoning bool, hw HardwareReport) ([]HubModel, error) {
	key := cacheKey(q, publisher, fit, vision, tools, reasoning)
	catalogMu.Lock()
	if e, ok := catalogCache[key]; ok && time.Since(e.at) < catalogTTL {
		out := e.models
		catalogMu.Unlock()
		return attachLocalAndFit(out, hw), nil
	}
	catalogMu.Unlock()

	items, err := hfSearch(q, vision)
	if err != nil {
		return nil, err
	}
	models := make([]HubModel, 0, len(items))
	for _, it := range items {
		pub, name := splitRepo(it.ID)
		if publisher != "" && !strings.EqualFold(pub, publisher) {
			continue
		}
		m := HubModel{
			ID:           it.ID,
			Name:         name,
			Publisher:    pub,
			Downloads:    it.Downloads,
			Likes:        it.Likes,
			Tags:         it.Tags,
			Pipeline:     it.PipelineTag,
			URL:          hfBase + "/" + it.ID,
			LastModified: it.LastModified,
			Params:       parseParamsLabel(it.ID + " " + name),
		}
		models = append(models, m)
	}

	expandHubFiles(models)
	for i := range models {
		inferCaps(&models[i])
	}

	if vision || tools || reasoning {
		filtered := models[:0]
		for _, m := range models {
			if vision && !m.Vision {
				continue
			}
			if tools && !m.Tools {
				continue
			}
			if reasoning && !m.Reasoning {
				continue
			}
			filtered = append(filtered, m)
		}
		models = filtered
	}

	catalogMu.Lock()
	catalogCache[key] = catalogEntry{at: time.Now(), models: models}
	catalogMu.Unlock()
	return attachLocalAndFit(models, hw), nil
}

func attachLocalAndFit(in []HubModel, hw HardwareReport) []HubModel {
	local := indexLocalGGUF()
	active := currentAssignedPaths()
	out := make([]HubModel, len(in))
	copy(out, in)
	for i := range out {
		files := make([]HubFile, 0, len(out[i].Files))
		best := fitNo
		for _, f := range out[i].Files {
			f.SizeGB = round1(bytesToGiB(f.Size))
			if f.Kind == "weights" {
				f.Fit = classifyFit(f.Size, hw)
				if rankFit(f.Fit.Kind) < rankFit(best) {
					best = f.Fit.Kind
				}
			}
			if lp, ok := local[strings.ToLower(filepath.Base(f.Path))]; ok {
				f.Local, f.LocalPath = true, lp
			}
			if f.LocalPath != "" {
				abs, _ := filepath.Abs(f.LocalPath)
				if sameFile(abs, active.model) || sameFile(abs, active.mmproj) {
					f.Active = true
				}
			}
			files = append(files, f)
		}
		out[i].Files = files
		out[i].BestFit = best
		if out[i].BestFit == fitNo && len(files) == 0 {
			out[i].BestFit = ""
		}
	}
	return out
}

func rankFit(k string) int {
	switch k {
	case fitGPU:
		return 0
	case fitHybrid:
		return 1
	case fitCPU:
		return 2
	default:
		return 3
	}
}

func filterByFit(models []HubModel, want string) []HubModel {
	if want == "" || want == "all" {
		return models
	}
	var out []HubModel
	for _, m := range models {
		var files []HubFile
		keep := false
		for _, f := range m.Files {
			if f.Kind == "mmproj" || f.Fit.Kind == want {
				files = append(files, f)
				if f.Kind == "weights" && f.Fit.Kind == want {
					keep = true
				}
			}
		}
		if keep {
			m.Files = files
			m.BestFit = want
			out = append(out, m)
		}
	}
	return out
}

func hfSearch(q string, preferVision bool) ([]hfListItem, error) {
	u, _ := url.Parse(strings.TrimRight(hfBase, "/") + "/api/models")
	qs := u.Query()
	qs.Set("filter", "gguf")
	qs.Set("sort", "downloads")
	qs.Set("direction", "-1")
	qs.Set("limit", strconvI(hfSearchLimit))
	if strings.TrimSpace(q) != "" {
		qs.Set("search", strings.TrimSpace(q))
	}
	if preferVision {
		qs.Set("pipeline_tag", "image-text-to-text")
	}
	u.RawQuery = qs.Encode()
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Kibborg/1.0 (local agent; +https://huggingface.co)")
	req.Header.Set("Accept", "application/json")
	resp, err := hfHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("huggingface недоступен: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("huggingface HTTP %d: %s", resp.StatusCode, capAgentText(string(body), 200))
	}
	var items []hfListItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("huggingface отдал не JSON: %w", err)
	}
	return items, nil
}

func expandHubFiles(models []HubModel) {
	sem := make(chan struct{}, hfExpandWorkers)
	var wg sync.WaitGroup
	for i := range models {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			files, err := hfTree(models[i].ID)
			if err != nil {
				return
			}
			models[i].Files = files
			for _, f := range files {
				if f.Kind == "mmproj" {
					models[i].HasMMProj = true
					break
				}
			}
		}(i)
	}
	wg.Wait()
}

func hfTree(repo string) ([]HubFile, error) {
	u := strings.TrimRight(hfBase, "/") + "/api/models/" + repo + "/tree/main"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Kibborg/1.0")
	req.Header.Set("Accept", "application/json")
	resp, err := hfHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tree HTTP %d", resp.StatusCode)
	}
	var items []hfTreeItem
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&items); err != nil {
		return nil, err
	}
	var files []HubFile
	for _, it := range items {
		if !strings.EqualFold(it.Type, "file") {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(it.Path), ".gguf") {
			continue
		}
		sz := it.Size
		if it.LFS != nil && it.LFS.Size > sz {
			sz = it.LFS.Size
		}
		kind := "weights"
		base := filepath.Base(it.Path)
		if strings.Contains(strings.ToLower(base), "mmproj") {
			kind = "mmproj"
		}
		files = append(files, HubFile{
			Name:  base,
			Path:  it.Path,
			Size:  sz,
			Quant: parseQuant(base),
			Kind:  kind,
		})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Kind != files[j].Kind {
			return files[i].Kind > files[j].Kind // weights first
		}
		return files[i].Size < files[j].Size
	})
	return files, nil
}

func splitRepo(id string) (publisher, name string) {
	id = strings.TrimSpace(id)
	if i := strings.IndexByte(id, '/'); i > 0 {
		return id[:i], id[i+1:]
	}
	return "", id
}

var quantTokens = []string{
	"IQ4_XS", "IQ4_NL", "IQ3_M", "IQ3_S", "IQ3_XXS", "IQ2_XXS", "IQ2_XS", "IQ2_S", "IQ2_M",
	"IQ1_S", "IQ1_M", "Q8_0", "Q6_K", "Q5_K_M", "Q5_K_S", "Q5_1", "Q5_0",
	"Q4_K_M", "Q4_K_S", "Q4_1", "Q4_0", "Q3_K_L", "Q3_K_M", "Q3_K_S", "Q2_K",
	"BF16", "F16", "F32", "Q8_K",
}

func parseQuant(name string) string {
	u := strings.ToUpper(name)
	for _, q := range quantTokens {
		if strings.Contains(u, q) {
			return q
		}
	}
	return ""
}

var paramsRe = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*B(?:-A\d+(?:\.\d+)?B)?`)

func parseParamsLabel(s string) string {
	m := paramsRe.FindString(s)
	if m == "" {
		return ""
	}
	return strings.ReplaceAll(strings.ToUpper(m), " ", "")
}

func inferCaps(m *HubModel) {
	blob := strings.ToLower(m.ID + " " + m.Pipeline + " " + strings.Join(m.Tags, " ") + " " + m.Name)
	if m.HasMMProj || strings.Contains(blob, "image-text-to-text") ||
		strings.Contains(blob, "llava") || strings.Contains(blob, "vision") ||
		strings.Contains(blob, "qwen2-vl") || strings.Contains(blob, "qwen2.5-vl") ||
		strings.Contains(blob, "qwen3-vl") || strings.Contains(blob, "qwen3.6") ||
		strings.Contains(blob, "qwen3.8") ||
		strings.Contains(blob, "gemma-3") || strings.Contains(blob, "internvl") ||
		strings.Contains(blob, "pixtral") || strings.Contains(blob, "minicpm-v") ||
		strings.Contains(blob, "multimodal") {
		m.Vision = true
	}
	if strings.Contains(blob, "function-calling") || strings.Contains(blob, "tool-use") ||
		strings.Contains(blob, "tools") || strings.Contains(blob, "qwen2.5") ||
		strings.Contains(blob, "qwen3") || strings.Contains(blob, "llama-3.1") ||
		strings.Contains(blob, "llama-3.2") || strings.Contains(blob, "llama-3.3") ||
		strings.Contains(blob, "llama-4") || strings.Contains(blob, "gemma-2") ||
		strings.Contains(blob, "gemma-3") || strings.Contains(blob, "mistral") ||
		strings.Contains(blob, "command-r") || strings.Contains(blob, "deepseek") ||
		strings.Contains(blob, "devstral") {
		m.Tools = true
	}
	if strings.Contains(blob, "reasoning") || strings.Contains(blob, "thinking") ||
		strings.Contains(blob, "deepseek-r1") || strings.Contains(blob, "qwq") ||
		regexp.MustCompile(`(^|[^a-z])r1([^a-z]|$)`).MatchString(blob) ||
		strings.Contains(blob, "qwen3") || strings.Contains(blob, "qwen3.6") ||
		strings.Contains(blob, "qwen3.8") {
		m.Reasoning = true
	}
}

func strconvI(n int) string { return fmt.Sprintf("%d", n) }

// ===== local disk =====

func indexLocalGGUF() map[string]string {
	out := map[string]string{}
	root := modelsBrainDir
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".gguf") {
			return nil
		}
		out[strings.ToLower(d.Name())] = path
		return nil
	})
	return out
}

func listLocalModels(hw HardwareReport) []HubModel {
	type acc struct {
		dir   string
		files []HubFile
	}
	byDir := map[string]*acc{}
	_ = filepath.WalkDir(modelsBrainDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".gguf") {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		dir := filepath.Dir(path)
		a := byDir[dir]
		if a == nil {
			a = &acc{dir: dir}
			byDir[dir] = a
		}
		kind := "weights"
		if strings.Contains(strings.ToLower(d.Name()), "mmproj") {
			kind = "mmproj"
		}
		a.files = append(a.files, HubFile{
			Name: d.Name(), Path: d.Name(), Size: info.Size(),
			SizeGB: round1(bytesToGiB(info.Size())), Quant: parseQuant(d.Name()),
			Kind: kind, Local: true, LocalPath: path,
		})
		return nil
	})
	active := currentAssignedPaths()
	var models []HubModel
	for dir, a := range byDir {
		base := filepath.Base(dir)
		m := HubModel{
			ID: "local/" + base, Name: base, Publisher: "диск", Local: true,
			URL: "", Files: a.files, Params: parseParamsLabel(base),
		}
		for i := range m.Files {
			if m.Files[i].Kind == "weights" {
				m.Files[i].Fit = classifyFit(m.Files[i].Size, hw)
			}
			abs, _ := filepath.Abs(m.Files[i].LocalPath)
			if sameFile(abs, active.model) || sameFile(abs, active.mmproj) {
				m.Files[i].Active = true
			}
			if m.Files[i].Kind == "mmproj" {
				m.HasMMProj = true
				m.Vision = true
			}
		}
		inferCaps(&m)
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models
}

type assignedPaths struct{ model, mmproj string }

func currentAssignedPaths() assignedPaths {
	cfg := curWebCfg()
	return assignedPaths{model: cfg.ModelPath, mmproj: cfg.MmprojPath}
}

func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return strings.EqualFold(aa, bb)
}

func publishersOf(models []HubModel) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range models {
		if m.Publisher == "" || m.Publisher == "диск" || seen[m.Publisher] {
			continue
		}
		seen[m.Publisher] = true
		out = append(out, m.Publisher)
	}
	sort.Strings(out)
	return out
}

// ===== download =====

type modelDownload struct {
	mu       sync.Mutex
	Repo     string `json:"repo"`
	File     string `json:"file"`
	Dest     string `json:"dest"`
	Total    int64  `json:"total"`
	Done     int64  `json:"done"`
	Status   string `json:"status"` // idle|running|done|error|canceled
	Err      string `json:"error,omitempty"`
	Assigned bool   `json:"assigned"`
	Mmproj   string `json:"mmproj,omitempty"`
	// SwitchErr — файл скачан и прописан, но автопереключение мозга не стартовало
	// (например, уже идёт другое). Показываем честно, а не «переключаю мозг» впустую.
	SwitchErr string `json:"switch_error,omitempty"`
	cancel    context.CancelFunc
}

var currentDL = &modelDownload{Status: "idle"}

func downloadSnapshot() map[string]any {
	currentDL.mu.Lock()
	defer currentDL.mu.Unlock()
	pct := 0.0
	if currentDL.Total > 0 {
		pct = float64(currentDL.Done) / float64(currentDL.Total) * 100
	}
	return map[string]any{
		"repo": currentDL.Repo, "file": currentDL.File, "dest": currentDL.Dest,
		"total": currentDL.Total, "done": currentDL.Done, "pct": round1(pct),
		"status": currentDL.Status, "error": currentDL.Err,
		"assigned": currentDL.Assigned, "mmproj": currentDL.Mmproj,
		"switch_error": currentDL.SwitchErr,
	}
}

func startModelDownload(repo, file string, assign, withMMProj bool) error {
	repo = strings.TrimSpace(repo)
	file = strings.TrimSpace(strings.ReplaceAll(file, "\\", "/"))
	if repo == "" || file == "" || strings.Contains(file, "..") {
		return fmt.Errorf("нужны repo и file")
	}
	if !strings.HasSuffix(strings.ToLower(file), ".gguf") {
		return fmt.Errorf("скачиваю только .gguf")
	}
	currentDL.mu.Lock()
	if currentDL.Status == "running" {
		currentDL.mu.Unlock()
		return fmt.Errorf("уже качается %s — дождись или отмени", currentDL.File)
	}
	ctx, cancel := context.WithCancel(context.Background())
	currentDL.Repo, currentDL.File = repo, file
	currentDL.Dest, currentDL.Total, currentDL.Done = "", 0, 0
	currentDL.Status, currentDL.Err, currentDL.Assigned = "running", "", false
	currentDL.Mmproj = ""
	currentDL.SwitchErr = ""
	currentDL.cancel = cancel
	currentDL.mu.Unlock()

	go func() {
		err := runModelDownload(ctx, repo, file, assign, withMMProj)
		currentDL.mu.Lock()
		if ctx.Err() != nil {
			currentDL.Status = "canceled"
			currentDL.Err = "остановлено"
		} else if err != nil {
			currentDL.Status = "error"
			currentDL.Err = err.Error()
		} else if currentDL.Status == "running" {
			currentDL.Status = "done"
		}
		currentDL.mu.Unlock()
	}()
	return nil
}

func cancelModelDownload() {
	currentDL.mu.Lock()
	c := currentDL.cancel
	currentDL.mu.Unlock()
	if c != nil {
		c()
	}
}

func runModelDownload(ctx context.Context, repo, file string, assign, withMMProj bool) error {
	dest, err := downloadHFFile(ctx, repo, file, func(done, total int64) {
		currentDL.mu.Lock()
		currentDL.Done, currentDL.Total, currentDL.Dest = done, total, destOf(repo, file)
		currentDL.mu.Unlock()
	})
	if err != nil {
		return err
	}
	mmprojPath := ""
	if withMMProj && !strings.Contains(strings.ToLower(file), "mmproj") {
		if mp := findRepoMMProj(repo); mp != "" {
			if p, merr := downloadHFFile(ctx, repo, mp, nil); merr == nil {
				mmprojPath = p
			}
		}
	}
	currentDL.mu.Lock()
	currentDL.Dest = dest
	currentDL.Mmproj = mmprojPath
	currentDL.mu.Unlock()
	if assign {
		if err := assignBrainModel(dest, mmprojPath); err != nil {
			return err
		}
		currentDL.mu.Lock()
		currentDL.Assigned = true
		currentDL.mu.Unlock()
		// Скачали и прописали — поднимаем в VRAM, не требуя ручного Stop→Start.
		// Неудачу (например, уже идёт другое переключение) НЕ проглатываем: пишем
		// в SwitchErr, и панель/`/models` честно покажут, что мозг не переключён.
		if err := startBrainSwitch(); err != nil {
			log.Printf("[MODELS] скачано, автопереключение: %v", err)
			currentDL.mu.Lock()
			currentDL.SwitchErr = err.Error()
			currentDL.mu.Unlock()
		}
	}
	return nil
}

func destOf(repo, file string) string {
	return filepath.Join(modelsBrainDir, sanitizeRepoDir(repo), filepath.Base(file))
}

func sanitizeRepoDir(repo string) string {
	s := strings.ReplaceAll(repo, "/", "__")
	s = strings.ReplaceAll(s, "\\", "__")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "model"
	}
	return out
}

func downloadHFFile(ctx context.Context, repo, file string, progress func(done, total int64)) (string, error) {
	dest := destOf(repo, file)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	src := strings.TrimRight(hfBase, "/") + "/" + repo + "/resolve/main/" + strings.TrimPrefix(file, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Kibborg/1.0")
	resp, err := hfDownloadHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		return "", fmt.Errorf("скачивание HTTP %d: %s", resp.StatusCode, capAgentText(string(raw), 160))
	}
	part := dest + ".part"
	f, err := os.Create(part)
	if err != nil {
		return "", err
	}
	var done int64
	total := resp.ContentLength
	buf := make([]byte, 256<<10)
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			_ = os.Remove(part)
			return "", err
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				_ = os.Remove(part)
				return "", werr
			}
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			_ = os.Remove(part)
			return "", rerr
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	_ = os.Remove(dest)
	if err := os.Rename(part, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func findRepoMMProj(repo string) string {
	files, err := hfTree(repo)
	if err != nil {
		return ""
	}
	for _, f := range files {
		if f.Kind == "mmproj" {
			return f.Path
		}
	}
	return ""
}

// ===== assign / settings.ini =====

func assignBrainModel(modelPath, mmprojPath string) error {
	if strings.TrimSpace(modelPath) == "" {
		return fmt.Errorf("путь к модели пуст")
	}
	abs, err := filepath.Abs(modelPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Ext(abs), ".gguf") {
		return fmt.Errorf("модель должна быть .gguf: %s", modelPath)
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("файла нет: %s", modelPath)
	}
	// mmproj строго привязан к архитектуре модели: если его нет (не передали и рядом
	// не лежит) — ОБЯЗАТЕЛЬНО чистим MMPROJ_PATH в ini и webCfg. Иначе после переключения
	// в settings.ini останется mmproj от старой модели и llama-server не поднимется.
	if mmprojPath == "" {
		if auto := autoDetectMmproj(abs); auto != "" {
			mmprojPath = auto
		}
	}
	relModel := relativizeForINI(abs)
	relMM := ""
	if mmprojPath != "" {
		am, e := filepath.Abs(mmprojPath)
		if e != nil {
			return fmt.Errorf("mmproj путь не разбирается: %s", mmprojPath)
		}
		if !strings.EqualFold(filepath.Ext(am), ".gguf") {
			return fmt.Errorf("mmproj должна быть .gguf: %s", mmprojPath)
		}
		if _, e := os.Stat(am); e != nil {
			return fmt.Errorf("mmproj файла нет: %s", mmprojPath)
		}
		relMM = relativizeForINI(am)
	}
	updates := map[string]string{"MODEL_PATH": relModel, "MMPROJ_PATH": relMM}
	if err := patchSettingsINI(settingsINIPath, updates); err != nil {
		return err
	}
	webCfgMu.Lock()
	webCfg.ModelPath = abs
	if relMM != "" {
		webCfg.MmprojPath, _ = filepath.Abs(mmprojPath)
	} else {
		webCfg.MmprojPath = ""
	}
	webCfgMu.Unlock()
	return nil
}

func relativizeForINI(abs string) string {
	wd, err := os.Getwd()
	if err != nil {
		return abs
	}
	rel, err := filepath.Rel(wd, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return abs
	}
	return rel
}

// patchSettingsINI меняет KEY=value, не трогая комментарии. Нет ключа — дописывает в конец.
func patchSettingsINI(path string, updates map[string]string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	nl := "\n"
	if bytes.Contains(raw, []byte("\r\n")) {
		nl = "\r\n"
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	seen := map[string]bool{}
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, ";") {
			continue
		}
		eq := strings.Index(trim, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(trim[:eq])
		if val, ok := updates[key]; ok {
			lines[i] = key + "=" + val
			seen[key] = true
		}
	}
	for k, v := range updates {
		if !seen[k] {
			lines = append(lines, k+"="+v)
		}
	}
	return os.WriteFile(path, []byte(strings.Join(lines, nl)), 0o644)
}

// ===== Telegram / chat =====

var (
	hardwareCommands = []string{"/hw", "/hardware", "/железо", "/gpu"}
	modelsCommands   = []string{"/models", "/модели", "/model", "/модель"}
)

func handleModelsCommand(arg string) string {
	arg = strings.TrimSpace(arg)
	hw := probeHardware(false)
	fields := strings.Fields(arg)
	if len(fields) >= 1 && (strings.EqualFold(fields[0], "get") || strings.EqualFold(fields[0], "скачать")) {
		if len(fields) < 3 {
			return "📦 Укажи файл: `/models get unsloth/Qwen3.6-35B-A3B-GGUF Qwen3.6-35B-A3B-UD-IQ4_XS.gguf`"
		}
		repo := fields[1]
		file := fields[2]
		if err := startModelDownload(repo, file, true, true); err != nil {
			return "❌ " + err.Error()
		}
		return "⬇ Качаю `" + file + "` в `models/brain/`.\n" +
			"После скачивания пропишу в settings.ini и перезапущу мозг.\n" +
			"Прогресс: правая колонка панели и `/models`."
	}
	if len(fields) >= 1 && (strings.EqualFold(fields[0], "use") || strings.EqualFold(fields[0], "switch") ||
		strings.EqualFold(fields[0], "назначить") || strings.EqualFold(fields[0], "включить")) {
		if len(fields) < 2 {
			return "📦 Укажи файл: `/models use Qwen3.8-27B-Q4_K_M.gguf`"
		}
		path, mm, err := findLocalWeight(strings.Join(fields[1:], " "))
		if err != nil {
			return "❌ " + err.Error()
		}
		if err := assignBrainModel(path, mm); err != nil {
			return "❌ " + err.Error()
		}
		if err := startBrainSwitch(); err != nil {
			return "📌 Прописал `" + filepath.Base(path) + "` в settings.ini.\n" + err.Error()
		}
		return "🔄 Переключаю мозг на `" + filepath.Base(path) + "`. Это 1–5 минут, чат подождёт."
	}
	if len(fields) == 0 || (len(fields) == 1 && (strings.EqualFold(fields[0], "local") || fields[0] == "диск" ||
		strings.EqualFold(fields[0], "status") || fields[0] == "статус")) {
		return formatModelsStatus(hw)
	}
	fit := ""
	vision, tools, reasoning := false, false, false
	var query []string
	for _, f := range fields {
		switch strings.ToLower(f) {
		case "gpu", "авто":
			fit = fitGPU
		case "hybrid", "гибрид":
			fit = fitHybrid
		case "vision", "зрение":
			vision = true
		case "tools", "инструменты":
			tools = true
		case "think", "reasoning", "размышления":
			reasoning = true
		default:
			query = append(query, f)
		}
	}
	q := strings.Join(query, " ")
	models, err := searchHubModels(q, "", fit, vision, tools, reasoning, hw)
	if err != nil {
		return "❌ Каталог: " + err.Error()
	}
	if fit != "" {
		models = filterByFit(models, fit)
	}
	return formatHubModels(hw, models, q, fit)
}

func formatHubModels(hw HardwareReport, models []HubModel, q, fit string) string {
	var b strings.Builder
	b.WriteString("📦 **Каталог GGUF** (Hugging Face)\n")
	fmt.Fprintf(&b, "Железо: %d GPU · %.0f ГБ VRAM · %.0f ГБ RAM\n",
		hw.Summary.GPUCount, hw.Summary.VRAMGB, hw.Summary.RAMGB)
	if q != "" || fit != "" {
		fmt.Fprintf(&b, "Фильтр: %s %s\n", q, fit)
	}
	if len(models) == 0 {
		b.WriteString("Ничего не нашлось. Попробуй другое имя (qwen, gemma, llama).\n")
		return b.String()
	}
	n := len(models)
	if n > 8 {
		n = 8
	}
	for _, m := range models[:n] {
		caps := []string{}
		if m.Vision {
			caps = append(caps, "зрение")
		}
		if m.Tools {
			caps = append(caps, "инструменты")
		}
		if m.Reasoning {
			caps = append(caps, "размышления")
		}
		fmt.Fprintf(&b, "\n**%s** · %s", m.Name, m.Publisher)
		if m.Params != "" {
			fmt.Fprintf(&b, " · %s", m.Params)
		}
		if len(caps) > 0 {
			fmt.Fprintf(&b, " · %s", strings.Join(caps, ", "))
		}
		b.WriteByte('\n')
		shown := 0
		for _, f := range m.Files {
			if f.Kind != "weights" || f.Size == 0 {
				continue
			}
			mark := ""
			if f.Local {
				mark = " · на диске"
			}
			fmt.Fprintf(&b, "- `%s` · %.1f ГБ · %s%s\n", f.QuantOrName(), f.SizeGB, f.Fit.Label, mark)
			shown++
			if shown >= 3 {
				break
			}
		}
		fmt.Fprintf(&b, "  скачать: `/models get %s <файл.gguf>`\n", m.ID)
	}
	b.WriteString("\nПолный список и прогресс — вкладка **Модели** в панели.")
	return b.String()
}

func (f HubFile) QuantOrName() string {
	if f.Quant != "" {
		return f.Quant
	}
	return f.Name
}

func formatLocalModels(models []HubModel) string {
	if len(models) == 0 {
		return "На диске пусто: `models/brain/` ещё без GGUF."
	}
	var b strings.Builder
	b.WriteString("📁 **Модели на диске**\n")
	for _, m := range models {
		fmt.Fprintf(&b, "\n**%s**\n", m.Name)
		for _, f := range m.Files {
			act := ""
			if f.Active {
				act = " · назначена"
			}
			fmt.Fprintf(&b, "- `%s` · %.1f ГБ · %s%s\n", f.Name, f.SizeGB, f.Fit.Label, act)
		}
	}
	return b.String()
}

func formatModelsStatus(hw HardwareReport) string {
	var b strings.Builder
	run := liveBrainModelNow()
	if run == "" {
		run = "—"
	}
	asg := filepathBase(curWebCfg().ModelPath)
	fmt.Fprintf(&b, "🧠 **Сейчас в VRAM:** `%s`\n", run)
	if asg != "" && asg != run {
		fmt.Fprintf(&b, "📌 В settings.ini: `%s` (ещё не поднята)\n", asg)
	}
	sw := switchSnapshot()
	if st, _ := sw["status"].(string); st == "stopping" || st == "starting" {
		fmt.Fprintf(&b, "🔄 Переключаю → `%s` (%s)\n", sw["to"], st)
	} else if st == "error" {
		fmt.Fprintf(&b, "❌ Переключение: %s\n", sw["error"])
	}
	dl := downloadSnapshot()
	if st, _ := dl["status"].(string); st == "running" {
		fmt.Fprintf(&b, "⬇ Скачиваю `%s` · %.0f%% (%s / %s)\n",
			dl["file"], asFloat(dl["pct"]),
			fmtGBMaybe(dl["done"]), fmtGBMaybe(dl["total"]))
	} else if st == "error" {
		fmt.Fprintf(&b, "❌ Скачивание: %s\n", dl["error"])
	} else if st == "done" {
		if se, _ := dl["switch_error"].(string); se != "" {
			fmt.Fprintf(&b, "⚠️ Скачано, но мозг не переключён: %s\n", se)
		}
	}
	b.WriteString("\n")
	b.WriteString(formatLocalModels(listLocalModels(hw)))
	b.WriteString("\nПереключить: `/models use <файл.gguf>`\nСкачать: `/models get owner/repo файл.gguf`")
	return b.String()
}

func asFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

func fmtGBMaybe(v any) string {
	n := asFloat(v)
	if n <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f ГБ", n/(1024*1024*1024))
}
