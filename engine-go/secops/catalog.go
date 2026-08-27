package secops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// CatalogEntry is one row from the local Awesome-Hacking index.
type CatalogEntry struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Section     string `json:"section"`
}

// Catalog is the on-disk index under hacker-tools/catalog.json.
type Catalog struct {
	Source  string         `json:"source"`
	Count   int            `json:"count"`
	Entries []CatalogEntry `json:"entries"`
}

type catalogFile struct {
	Source  string         `json:"source"`
	Count   int            `json:"count"`
	Entries []CatalogEntry `json:"entries"`
}

var (
	catalogMu    sync.Mutex
	catalogCache *Catalog
	catalogErr   error
)

// ResetCatalogCacheForTest clears the in-memory catalog (tests only).
func ResetCatalogCacheForTest() {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	catalogCache = nil
	catalogErr = nil
}

// LoadCatalog reads hacker-tools/catalog.json (cwd-relative). Cached after first call.
func LoadCatalog() (Catalog, error) {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	if catalogCache != nil || catalogErr != nil {
		if catalogCache == nil {
			return Catalog{}, catalogErr
		}
		return *catalogCache, catalogErr
	}
	path, err := findHackerToolsFile("catalog.json")
	if err != nil {
		catalogErr = err
		return Catalog{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		catalogErr = err
		return Catalog{}, err
	}
	var raw catalogFile
	if err := json.Unmarshal(data, &raw); err != nil {
		catalogErr = err
		return Catalog{}, err
	}
	cat := Catalog(raw)
	if cat.Count == 0 {
		cat.Count = len(cat.Entries)
	}
	catalogCache = &cat
	return cat, nil
}

// SearchCatalog returns entries whose name/description/section match query (case-insensitive).
// Empty query returns up to limit entries (or all if limit <= 0).
func SearchCatalog(query string, limit int) (Catalog, error) {
	cat, err := LoadCatalog()
	if err != nil {
		return Catalog{}, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	out := Catalog{Source: cat.Source}
	for _, e := range cat.Entries {
		if q != "" {
			hay := strings.ToLower(e.Name + " " + e.Description + " " + e.Section)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		out.Entries = append(out.Entries, e)
		if limit > 0 && len(out.Entries) >= limit {
			break
		}
	}
	out.Count = len(out.Entries)
	return out, nil
}

// PlaybookPath returns the path to PLAYBOOK.md when present.
func PlaybookPath() (string, error) {
	return findHackerToolsFile("PLAYBOOK.md")
}

// RenderCatalogMarkdown prints a compact list for the model / Telegram.
func RenderCatalogMarkdown(cat Catalog, title string) string {
	var b strings.Builder
	if title == "" {
		title = "Каталог Hacker Tools"
	}
	fmt.Fprintf(&b, "🛡 **%s** (%d)\n", title, cat.Count)
	if cat.Source != "" {
		fmt.Fprintf(&b, "Источник: %s\n", cat.Source)
	}
	if path, err := PlaybookPath(); err == nil {
		fmt.Fprintf(&b, "Плейбук: `%s`\n", path)
	}
	b.WriteByte('\n')
	if len(cat.Entries) == 0 {
		b.WriteString("Ничего не найдено.\n")
		return b.String()
	}
	for i, e := range cat.Entries {
		fmt.Fprintf(&b, "%d. **%s** — %s\n   %s\n", i+1, e.Name, e.Description, e.URL)
	}
	return b.String()
}

func findHackerToolsFile(name string) (string, error) {
	candidates := hackerToolsCandidates(name)
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			abs, _ := filepath.Abs(p)
			if abs != "" {
				return abs, nil
			}
			return p, nil
		}
	}
	return "", fmt.Errorf("не найден hacker-tools/%s — каталог должен лежать рядом с движком", name)
}

// hackerToolsCandidates lists likely paths for a file or directory under hacker-tools/.
// Includes the executable directory so ProbeLocalTools works even if cwd is not engine-go
// (tests, IDE run, accidental Start from another folder).
func hackerToolsCandidates(name string) []string {
	candidates := []string{
		filepath.Join("hacker-tools", name),
		filepath.Join("engine-go", "hacker-tools", name),
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "hacker-tools", name),
			filepath.Join(wd, "engine-go", "hacker-tools", name),
		)
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "hacker-tools", name),
			filepath.Join(dir, "engine-go", "hacker-tools", name),
			// go test / go run put binaries under %TEMP% — walk up a few parents
			filepath.Join(dir, "..", "hacker-tools", name),
			filepath.Join(dir, "..", "..", "hacker-tools", name),
			filepath.Join(dir, "..", "..", "..", "hacker-tools", name),
		)
	}
	return candidates
}
