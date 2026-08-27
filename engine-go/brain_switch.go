package main

// Переключение мозга без полного Stop→Start стека: гасим llama-server на PORT_BRAIN
// и поднимаем модель из текущего webCfg (уже прописанного в settings.ini).
// Скачивание и клик в панели / /models use сходятся сюда.

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type brainSwitchState struct {
	mu     sync.Mutex
	Status string `json:"status"` // idle|stopping|starting|ready|error
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Err    string `json:"error,omitempty"`
}

var brainSW = &brainSwitchState{Status: "idle"}

func switchSnapshot() map[string]any {
	brainSW.mu.Lock()
	defer brainSW.mu.Unlock()
	return map[string]any{
		"status": brainSW.Status,
		"from":   brainSW.From,
		"to":     brainSW.To,
		"error":  brainSW.Err,
	}
}

func brainSwitchBusy() bool {
	brainSW.mu.Lock()
	defer brainSW.mu.Unlock()
	return brainSW.Status == "stopping" || brainSW.Status == "starting"
}

// startBrainSwitch гасит текущий llama-server и поднимает модель из webCfg.
// Если в VRAM уже лежит тот же файл и /health=200 — ничего не делает.
// Цель (want + cfg) фиксируется ЗДЕСЬ и передаётся в горутину: если за время
// переключения пользователь назначит другую модель, settings.ini/webCfg уже
// новые, а мозг поднимется ровно тот, что выбрали в момент старта переключения.
func startBrainSwitch() error {
	cfg := curWebCfg()
	want := filepathBase(cfg.ModelPath)
	if want == "" {
		return fmt.Errorf("MODEL_PATH пуст")
	}
	if _, err := os.Stat(cfg.ModelPath); err != nil {
		return fmt.Errorf("файла модели нет: %s", cfg.ModelPath)
	}
	if liveBrainModelNow() == want && brainReady(cfg.BrainPort) && !brainSwitchBusy() {
		return fmt.Errorf("уже в VRAM: %s", want)
	}
	brainSW.mu.Lock()
	if brainSW.Status == "stopping" || brainSW.Status == "starting" {
		cur := brainSW.To
		brainSW.mu.Unlock()
		return fmt.Errorf("уже переключаю на %s", cur)
	}
	brainSW.Status = "stopping"
	brainSW.From = liveBrainModelNow()
	brainSW.To = want
	brainSW.Err = ""
	brainSW.mu.Unlock()

	go runBrainSwitch(want, cfg)
	return nil
}

func runBrainSwitch(want string, cfg Config) {
	log.Printf("[BRAIN] switch %s → %s", liveBrainModelNow(), want)
	if err := stopBrainOnPort(cfg.BrainPort); err != nil {
		setBrainSwitch("error", err.Error())
		log.Printf("[BRAIN] stop failed: %v", err)
		return
	}
	setBrainSwitch("starting", "")
	// Порт должен освободиться, иначе ensureBrain решит «уже поднят» и не запустит новую.
	if !waitPortFree(cfg.BrainPort, 45*time.Second) {
		setBrainSwitch("error", "порт мозга не освободился после остановки")
		return
	}
	setLiveBrainModel("")
	ensureBrain(cfg)
	if !waitBrainReady(cfg.BrainPort, 6*time.Minute) {
		setBrainSwitch("error", "модель не поднялась за 6 минут — смотри лог llama-server")
		return
	}
	setLiveBrainModel(want)
	setBrainSwitch("ready", "")
	log.Printf("[BRAIN] switch ready: %s", want)
	go warmUpBrain(cfg)
}

func setBrainSwitch(status, errText string) {
	brainSW.mu.Lock()
	brainSW.Status = status
	brainSW.Err = errText
	brainSW.mu.Unlock()
}

func waitPortFree(port int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !portInUse(port) {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return !portInUse(port)
}

func waitBrainReady(port int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if brainReady(port) {
			return true
		}
		time.Sleep(800 * time.Millisecond)
	}
	return brainReady(port)
}

// stopBrainOnPort гасит ТОЛЬКО мозг (llama-server) на порту. Свой pid не трогаем.
// Кандидаты на убийство:
//   - pid мозга, который подняли МЫ (trackBrainProc) — даже если netstat его не увидел;
//   - pid'ы, слушающие порт, НО только когда сервер там похож на llama-server (llamaOnPort).
//
// Whisper/TTS/embed в engineProcs НЕ трогаем: они живут на других портах, и чистить
// общий реестр движков при смене модели нельзя (после первого переключения умирала
// озвучка и распознавание речи). Посторонний процесс на порту (не llama) тоже не
// убиваем — вернём ошибку, как ensureBrain при старте.
// brainKillSet — чистый выбор pid'ов для убийства при переключении: наш трекинг
// мозга (brainProc) + слушатели порта, НО только если сервер на порту похож на
// llama-server. Whisper/TTS/embed в engineProcs сюда не попадают в принципе.
func brainKillSet(bp *os.Process, portPids []int, llamaThere bool, self int) []int {
	seen := map[int]bool{}
	var out []int
	add := func(pid int) {
		if pid <= 0 || pid == self || seen[pid] {
			return
		}
		seen[pid] = true
		out = append(out, pid)
	}
	if bp != nil {
		add(bp.Pid)
	}
	if llamaThere {
		for _, pid := range portPids {
			add(pid)
		}
	}
	return out
}

func stopBrainOnPort(port int) error {
	self := os.Getpid()

	engineProcMu.Lock()
	bp := brainProc
	brainProc = nil
	engineProcMu.Unlock()

	portPids := pidsListening(port)
	llamaThere := llamaOnPort(port)
	pids := brainKillSet(bp, portPids, llamaThere, self)

	if bp != nil && bp.Pid > 0 && bp.Pid != self {
		forgetBrainProc(bp.Pid) // убрать из реестра движков: он сейчас умрёт
	}

	var last error
	for _, pid := range pids {
		log.Printf("[BRAIN] kill pid %d on :%d", pid, port)
		if err := killPID(pid); err != nil {
			last = err
			log.Printf("[BRAIN] kill pid %d: %v", pid, err)
		}
	}
	if bp == nil && !llamaThere && len(portPids) > 0 {
		return fmt.Errorf("порт :%d занят посторонним процессом (не llama-server) — переключение отменено", port)
	}
	if last != nil && portInUse(port) {
		return last
	}
	return nil
}

func pidsListening(port int) []int {
	if runtime.GOOS == "windows" {
		return pidsListeningWindows(port)
	}
	return pidsListeningUnix(port)
}

func pidsListeningWindows(port int) []int {
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		fmt.Sprintf("(Get-NetTCPConnection -LocalPort %d -State Listen -ErrorAction SilentlyContinue).OwningProcess", port))
	out, err := cmd.Output()
	if err == nil {
		if pids := parsePIDList(string(out)); len(pids) > 0 {
			return pids
		}
	}
	// Fallback: netstat -ano. Ищем LISTENING на нужном порту.
	ns, nerr := exec.Command("netstat", "-ano", "-p", "tcp").Output()
	if nerr != nil {
		return nil
	}
	want := ":" + strconv.Itoa(port)
	var pids []int
	for _, line := range strings.Split(string(ns), "\n") {
		up := strings.ToUpper(line)
		if !strings.Contains(up, "LISTENING") || !strings.Contains(line, want) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		local := fields[1]
		if !strings.HasSuffix(local, want) && !strings.Contains(local, want) {
			continue
		}
		if n, e := strconv.Atoi(fields[len(fields)-1]); e == nil && n > 0 {
			pids = append(pids, n)
		}
	}
	return pids
}

func pidsListeningUnix(port int) []int {
	out, err := exec.Command("lsof", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN", "-t").Output()
	if err != nil {
		return nil
	}
	return parsePIDList(string(out))
}

func parsePIDList(s string) []int {
	seen := map[int]bool{}
	var out []int
	for _, f := range strings.Fields(s) {
		n, err := strconv.Atoi(f)
		if err != nil || n <= 0 || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

func killPID(pid int) error {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F", "/T")
		b, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("taskkill %d: %s", pid, strings.TrimSpace(string(b)))
		}
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// findLocalWeight ищет GGUF весов на диске по имени файла / подстроке.
func findLocalWeight(query string) (modelPath, mmprojPath string, err error) {
	q := strings.ToLower(strings.TrimSpace(query))
	q = strings.Trim(q, "`\"")
	q = strings.ReplaceAll(q, "\\", "/")
	if q == "" {
		return "", "", fmt.Errorf("укажи имя файла модели")
	}
	if base := filepathBase(q); base != "" && base != q {
		// Пользователь дал путь — если файл есть, берём его.
		if st, e := os.Stat(q); e == nil && !st.IsDir() {
			return q, autoDetectMmproj(q), nil
		}
	}
	locals := listLocalModels(HardwareReport{})
	var exact, partial []HubFile
	for _, m := range locals {
		for _, f := range m.Files {
			if f.Kind != "weights" {
				continue
			}
			name := strings.ToLower(f.Name)
			path := strings.ToLower(strings.ReplaceAll(f.LocalPath, "\\", "/"))
			if name == q || path == q || strings.ToLower(filepathBase(f.LocalPath)) == q {
				exact = append(exact, f)
				continue
			}
			if strings.Contains(name, q) || strings.Contains(path, q) {
				partial = append(partial, f)
			}
		}
	}
	pick := exact
	if len(pick) == 0 {
		pick = partial
	}
	if len(pick) == 0 {
		return "", "", fmt.Errorf("не нашёл `%s` в models/brain/", query)
	}
	if len(pick) > 1 {
		names := make([]string, 0, len(pick))
		for _, f := range pick {
			names = append(names, f.Name)
		}
		return "", "", fmt.Errorf("несколько совпадений: %s — уточни имя файла", strings.Join(names, ", "))
	}
	return pick[0].LocalPath, autoDetectMmproj(pick[0].LocalPath), nil
}

// localModelCards — лёгкий список для правой колонки и /api/status.
// Панель опрашивает /api/status каждые ~1.5 с, а обход диска (WalkDir + stat по
// models/brain) и тест железа на каждый поллинг — зря: результат кэшируем на 5 с.
// Метки «active/running» могут отставать от реальности до 5 с — для боковой
// колонки это незаметно, зато статус-поллинг не трогает диск.
var (
	localCardsMu   sync.Mutex
	localCardsAt   time.Time
	localCardsData []map[string]any
)

const localCardsTTL = 5 * time.Second

func localModelCards() []map[string]any {
	localCardsMu.Lock()
	if !localCardsAt.IsZero() && time.Since(localCardsAt) < localCardsTTL {
		out := localCardsData
		localCardsMu.Unlock()
		return out
	}
	localCardsMu.Unlock()

	hw := probeHardware(false)
	locals := listLocalModels(hw)
	running := strings.ToLower(liveBrainModelNow())
	assigned := strings.ToLower(filepathBase(curWebCfg().ModelPath))
	var out []map[string]any
	for _, m := range locals {
		for _, f := range m.Files {
			if f.Kind != "weights" {
				continue
			}
			base := filepathBase(f.LocalPath)
			low := strings.ToLower(base)
			out = append(out, map[string]any{
				"name":     base,
				"path":     f.LocalPath,
				"size_gb":  f.SizeGB,
				"quant":    f.Quant,
				"family":   m.Name,
				"vision":   m.HasMMProj || m.Vision,
				"active":   f.Active || low == assigned,
				"running":  running != "" && low == running,
				"fit":      f.Fit.Label,
				"fit_kind": f.Fit.Kind,
			})
		}
	}
	localCardsMu.Lock()
	localCardsData, localCardsAt = out, time.Now()
	localCardsMu.Unlock()
	return out
}
