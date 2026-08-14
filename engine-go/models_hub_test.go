package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseQuantAndParams(t *testing.T) {
	if g := parseQuant("Qwen3.6-35B-A3B-UD-IQ4_XS.gguf"); g != "IQ4_XS" {
		t.Errorf("quant = %q", g)
	}
	if g := parseQuant("model-Q4_K_M.gguf"); g != "Q4_K_M" {
		t.Errorf("quant = %q", g)
	}
	if p := parseParamsLabel("unsloth/Qwen3.6-35B-A3B-GGUF"); !strings.Contains(p, "35B") {
		t.Errorf("params = %q", p)
	}
}

func TestInferCapsQwen(t *testing.T) {
	m := HubModel{ID: "unsloth/Qwen3.6-35B-A3B-GGUF", Name: "Qwen3.6-35B-A3B-GGUF", HasMMProj: true,
		Tags: []string{"gguf", "image-text-to-text"}}
	inferCaps(&m)
	if !m.Vision || !m.Tools || !m.Reasoning {
		t.Fatalf("Qwen3.6 + mmproj: vision=%v tools=%v reasoning=%v", m.Vision, m.Tools, m.Reasoning)
	}
}

func TestSanitizeRepoDir(t *testing.T) {
	if g := sanitizeRepoDir("unsloth/Qwen3.6-35B-A3B-GGUF"); g != "unsloth__Qwen3.6-35B-A3B-GGUF" {
		t.Errorf("got %q", g)
	}
	if strings.ContainsAny(sanitizeRepoDir("../etc/passwd"), `/\`) {
		t.Fatal("sanitize оставил разделители пути")
	}
}

func TestPatchSettingsINIPreservesComments(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.ini")
	src := "# comment\r\nMODEL_PATH=old.gguf\r\nPORT_BRAIN=8083\r\n"
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := patchSettingsINI(p, map[string]string{"MODEL_PATH": "models\\brain\\x.gguf", "MMPROJ_PATH": "models\\brain\\mm.gguf"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	s := string(got)
	if !strings.Contains(s, "# comment") {
		t.Fatal("комментарий стёрт")
	}
	if !strings.Contains(s, "MODEL_PATH=models\\brain\\x.gguf") {
		t.Fatalf("MODEL_PATH не обновился:\n%s", s)
	}
	if !strings.Contains(s, "MMPROJ_PATH=models\\brain\\mm.gguf") {
		t.Fatal("MMPROJ_PATH не дописался")
	}
	if !strings.Contains(s, "PORT_BRAIN=8083") {
		t.Fatal("чужой ключ пострадал")
	}
}

func TestFilterByFitKeepsMatchingQuants(t *testing.T) {
	models := []HubModel{{
		ID: "x/y", Name: "y",
		Files: []HubFile{
			{Name: "a.gguf", Kind: "weights", Fit: FitResult{Kind: fitGPU}},
			{Name: "b.gguf", Kind: "weights", Fit: FitResult{Kind: fitHybrid}},
			{Name: "mmproj.gguf", Kind: "mmproj"},
		},
	}}
	got := filterByFit(models, fitGPU)
	if len(got) != 1 || len(got[0].Files) != 2 {
		t.Fatalf("осталось %+v", got)
	}
	if n := len(filterByFit(models, fitCPU)); n != 0 {
		t.Fatalf("cpu-фильтра быть не должно: %d", n)
	}
	if n := len(filterByFit([]HubModel{{Files: []HubFile{{Kind: "weights", Fit: FitResult{Kind: fitCPU}}}}}, fitGPU)); n != 0 {
		t.Fatal("gpu-фильтр пропустил cpu-файл")
	}
}

func TestHFSearchUsesFakeServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/models/") && strings.Contains(r.URL.Path, "/tree/"):
			_ = json.NewEncoder(w).Encode([]hfTreeItem{
				{Type: "file", Path: "m-Q4_K_M.gguf", Size: 8 << 30},
				{Type: "file", Path: "mmproj-BF16.gguf", LFS: &struct {
					Size int64 `json:"size"`
				}{Size: 1 << 30}},
			})
		case r.URL.Path == "/api/models":
			_ = json.NewEncoder(w).Encode([]hfListItem{{
				ID: "unsloth/Demo-7B-GGUF", Downloads: 10, Tags: []string{"gguf", "qwen3"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := hfBase
	hfBase = srv.URL
	t.Cleanup(func() { hfBase = old })

	hw := HardwareReport{Summary: HardwareSum{VRAMGB: 24, RAMGB: 64}}
	models, err := searchHubModels("demo", "", "", false, false, false, hw)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Publisher != "unsloth" {
		t.Fatalf("модели: %+v", models)
	}
	if !models[0].HasMMProj || !models[0].Vision {
		t.Fatal("mmproj не поднял флаг зрения")
	}
	if len(models[0].Files) != 2 {
		t.Fatalf("файлов %d", len(models[0].Files))
	}
	if models[0].Files[0].Fit.Kind == "" {
		t.Fatal("на веса не повесили fit")
	}
}

func TestModelsCommandGetValidates(t *testing.T) {
	s := handleModelsCommand("get")
	if !strings.Contains(s, "Укажи файл") {
		t.Fatalf("без файла: %s", s)
	}
}
