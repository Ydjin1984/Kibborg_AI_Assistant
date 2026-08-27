package secops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndSearchCatalog(t *testing.T) {
	if !chdirEngineGo(t) {
		t.Skip("hacker-tools/catalog.json не найден")
	}
	ResetCatalogCacheForTest()

	cat, err := LoadCatalog()
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if cat.Count < 10 || len(cat.Entries) < 10 {
		t.Fatalf("ожидали десятки записей, получили %d", cat.Count)
	}

	web, err := SearchCatalog("web", 5)
	if err != nil {
		t.Fatal(err)
	}
	if web.Count == 0 {
		t.Fatal("поиск web должен что-то найти")
	}
	md := RenderCatalogMarkdown(web, "тест")
	if !strings.Contains(strings.ToLower(md), "web") {
		t.Fatalf("markdown без web: %q", md)
	}
}

func chdirEngineGo(t *testing.T) bool {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	candidates := []string{cwd, filepath.Join(cwd, ".."), filepath.Dir(cwd)}
	for _, dir := range candidates {
		cat := filepath.Join(dir, "hacker-tools", "catalog.json")
		if st, err := os.Stat(cat); err == nil && !st.IsDir() {
			t.Cleanup(func() { _ = os.Chdir(cwd) })
			if err := os.Chdir(dir); err != nil {
				t.Fatal(err)
			}
			return true
		}
	}
	return false
}
