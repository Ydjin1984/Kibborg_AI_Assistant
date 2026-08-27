package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePIDList(t *testing.T) {
	got := parsePIDList("1234\n1234\n  56 \nxx\n0\n")
	if len(got) != 2 || got[0] != 1234 || got[1] != 56 {
		t.Fatalf("got %#v", got)
	}
}

func TestFindLocalWeightExactAndAmbiguous(t *testing.T) {
	dir := t.TempDir()
	old := modelsBrainDir
	modelsBrainDir = dir
	t.Cleanup(func() { modelsBrainDir = old })

	a := filepath.Join(dir, "FamA")
	b := filepath.Join(dir, "FamB")
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a, "Qwen3.8-27B-Q4_K_M.gguf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a, "mmproj-F16.gguf"), []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "Qwen3.6-35B-A3B-UD-IQ4_XS.gguf"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	p, mm, err := findLocalWeight("Qwen3.8-27B-Q4_K_M.gguf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, "Qwen3.8-27B-Q4_K_M.gguf") {
		t.Fatalf("path = %s", p)
	}
	if !strings.Contains(mm, "mmproj") {
		t.Fatalf("mmproj не рядом: %s", mm)
	}

	if _, _, err := findLocalWeight("нет-такой"); err == nil {
		t.Fatal("ждали ошибку на отсутствующей модели")
	}
	if _, _, err := findLocalWeight("Qwen"); err == nil {
		t.Fatal("ждали неоднозначность на общей подстроке")
	}
}

func TestInferCapsQwen38(t *testing.T) {
	m := HubModel{ID: "Abiray/Qwen3.8-27B-GGUF", Name: "Qwen3.8-27B", HasMMProj: true}
	inferCaps(&m)
	if !m.Vision || !m.Tools || !m.Reasoning {
		t.Fatalf("Qwen3.8 + mmproj: vision=%v tools=%v reasoning=%v", m.Vision, m.Tools, m.Reasoning)
	}
}
