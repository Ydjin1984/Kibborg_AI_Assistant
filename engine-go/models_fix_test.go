package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAssignBrainModelClearsMMProj — баг: при переключении на модель без mmproj
// в settings.ini оставался mmproj от старой модели, и мозг не поднимался.
func TestAssignBrainModelClearsMMProj(t *testing.T) {
	dir := t.TempDir()
	ini := filepath.Join(dir, "settings.ini")
	if err := os.WriteFile(ini, []byte("MODEL_PATH=old.gguf\nMMPROJ_PATH=old-mmproj.gguf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldINI := settingsINIPath
	settingsINIPath = ini
	t.Cleanup(func() { settingsINIPath = oldINI })

	// Модель в отдельной папке, БЕЗ mmproj рядом.
	mod := filepath.Join(dir, "fam", "Qwen.gguf")
	if err := os.MkdirAll(filepath.Dir(mod), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mod, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldCfg := curWebCfg()
	t.Cleanup(func() { setWebCfg(oldCfg) })

	if err := assignBrainModel(mod, ""); err != nil {
		t.Fatalf("assign: %v", err)
	}
	raw, _ := os.ReadFile(ini)
	s := string(raw)
	if !strings.Contains(s, "MODEL_PATH=") {
		t.Fatalf("MODEL_PATH не записан:\n%s", s)
	}
	if !strings.Contains(s, "MMPROJ_PATH=\n") && !strings.Contains(s, "MMPROJ_PATH=\r\n") {
		t.Fatalf("MMPROJ_PATH должен быть очищен (пустое значение), а не старый:\n%s", s)
	}
	if strings.Contains(s, "old-mmproj") {
		t.Fatalf("старый mmproj остался в ini:\n%s", s)
	}
	if got := curWebCfg().MmprojPath; got != "" {
		t.Fatalf("webCfg.MmprojPath должен быть пуст, получили %q", got)
	}
	if got := curWebCfg().ModelPath; got == "" {
		t.Fatal("webCfg.ModelPath не обновился")
	}

	// Модель С mmproj рядом — должен прописаться.
	mm := filepath.Join(dir, "fam", "mmproj-F16.gguf")
	if err := os.WriteFile(mm, []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := assignBrainModel(mod, ""); err != nil {
		t.Fatalf("assign с mmproj: %v", err)
	}
	if got := curWebCfg().MmprojPath; got == "" || !strings.Contains(strings.ToLower(got), "mmproj") {
		t.Fatalf("mmproj не подхватился автоопределением: %q", got)
	}
	raw, _ = os.ReadFile(ini)
	if !strings.Contains(string(raw), "MMPROJ_PATH=") || strings.Contains(string(raw), "MMPROJ_PATH=\n") ||
		strings.Contains(string(raw), "MMPROJ_PATH=\r\n") {
		t.Fatalf("MMPROJ_PATH должен быть непустым:\n%s", raw)
	}
}

// TestAssignBrainModelRejectsNonGGUF — в панель/API можно было «назначить» любой
// файл как модель; теперь только .gguf.
func TestAssignBrainModelRejectsNonGGUF(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "readme.txt")
	if err := os.WriteFile(txt, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := assignBrainModel(txt, ""); err == nil || !strings.Contains(err.Error(), ".gguf") {
		t.Fatalf("ждали ошибку про .gguf, получили %v", err)
	}
	// mmproj с не-GGUF расширением тоже отклоняется.
	mod := filepath.Join(dir, "model.gguf")
	if err := os.WriteFile(mod, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	badMM := filepath.Join(dir, "mmproj.bin")
	if err := os.WriteFile(badMM, []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := assignBrainModel(mod, badMM); err == nil || !strings.Contains(err.Error(), ".gguf") {
		t.Fatalf("ждали ошибку про mmproj .gguf, получили %v", err)
	}
}

// TestBrainKillSetScopedToBrainAndPort — переключение модели убивает ТОЛЬКО мозг
// и (если на порту llama) слушателей порта. Pid'ы TTS/whisper/embed в выборку
// не попадают, посторонний процесс на порту (не llama) не трогается.
func TestBrainKillSetScopedToBrainAndPort(t *testing.T) {
	// Наш мозг pid=100; на порту сидят 100 (он же) и 200 (другой llama).
	bp := &os.Process{Pid: 100}
	portPids := []int{100, 200}

	got := brainKillSet(bp, portPids, true, os.Getpid())
	if len(got) != 2 || !containsInt(got, 100) || !containsInt(got, 200) {
		t.Fatalf("llama на порту: ждали {100,200}, получили %v", got)
	}

	// Порт слушает НЕ llama (посторонний dev-сервер) — его не убиваем.
	got = brainKillSet(bp, portPids, false, os.Getpid())
	if len(got) != 1 || got[0] != 100 {
		t.Fatalf("чужой процесс на порту не должен умереть: %v", got)
	}

	// Нашего мозга нет, на порту чужой не-llama — никого не убиваем.
	if got := brainKillSet(nil, []int{500}, false, os.Getpid()); len(got) != 0 {
		t.Fatalf("посторонний процесс без нашего мозга: %v", got)
	}

	// Свой pid не трогаем.
	got = brainKillSet(&os.Process{Pid: os.Getpid()}, []int{os.Getpid()}, true, os.Getpid())
	if len(got) != 0 {
		t.Fatalf("свой pid попал в выборку: %v", got)
	}

	// Дубликаты схлопываются.
	got = brainKillSet(&os.Process{Pid: 100}, []int{100, 100}, true, os.Getpid())
	if len(got) != 1 {
		t.Fatalf("дубликаты не схлопнулись: %v", got)
	}
}

func containsInt(xs []int, n int) bool {
	for _, x := range xs {
		if x == n {
			return true
		}
	}
	return false
}

func TestLiveBrainModelAtomic(t *testing.T) {
	setLiveBrainModel("Qwen.gguf")
	if got := liveBrainModelNow(); got != "Qwen.gguf" {
		t.Fatalf("после set ждали Qwen.gguf, получили %q", got)
	}
	setLiveBrainModel("")
	if got := liveBrainModelNow(); got != "" {
		t.Fatalf("после сброса ждали пусто, получили %q", got)
	}
}
