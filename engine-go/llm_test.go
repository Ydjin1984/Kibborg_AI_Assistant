package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBrainServerArgs256KNoKVOffload(t *testing.T) {
	args := brainServerArgs(Config{
		ModelPath:      "m.gguf",
		CtxSize:        262144,
		GpuLayers:      99,
		Threads:        28,
		BrainPort:      8083,
		Parallel:       1,
		Reasoning:      "off",
		TensorSplit:    "0.35,0.65",
		MainGpu:        0,
		CacheTypeK:     "q8_0",
		CacheTypeV:     "q8_0",
		NoKVOffload:    true,
		CtxCheckpoints: "0",
		CacheRam:       "0",
		BatchSize:      "512",
		UbatchSize:     "256",
	})
	want := []string{
		"-m", "m.gguf",
		"--ctx-size", "262144",
		"--n-gpu-layers", "99",
		"--threads", "28",
		"--threads-batch", "28",
		"--port", "8083",
		"--flash-attn", "on",
		"--parallel", "1",
		"--reasoning", "off",
		"--reasoning-budget", "0",
		"--tensor-split", "0.35,0.65",
		"--main-gpu", "0",
		"--cache-type-k", "q8_0",
		"--cache-type-v", "q8_0",
		"--no-kv-offload",
		"--ctx-checkpoints", "0",
		"--cache-ram", "0",
		"--batch-size", "512",
		"--ubatch-size", "256",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("args =\n%q\nwant\n%q", args, want)
	}
}

func TestBrainServerArgsKeepsKVOnGPUByDefault(t *testing.T) {
	args := brainServerArgs(Config{
		ModelPath: "m.gguf",
		CtxSize:   32768,
		MainGpu:   -1,
	})
	if slices.Contains(args, "--no-kv-offload") {
		t.Fatal("без LLAMA_NO_KV_OFFLOAD флаг --no-kv-offload не должен попадать в командную строку")
	}
	if slices.Contains(args, "--ctx-checkpoints") || slices.Contains(args, "--cache-ram") {
		t.Fatal("пустые mitigations не должны попадать в командную строку")
	}
	if !slices.Contains(args, "--ctx-size") {
		t.Fatal("нет --ctx-size")
	}
}

func TestLoadConfig256KNoKVOffload(t *testing.T) {
	dir := t.TempDir()
	ini := filepath.Join(dir, "settings.ini")
	body := "LLAMA_CTX_SIZE=262144\nLLAMA_NO_KV_OFFLOAD=true\nLLAMA_CACHE_TYPE_K=q8_0\n" +
		"LLAMA_CTX_CHECKPOINTS=0\nLLAMA_CACHE_RAM=0\nLLAMA_BATCH=512\nLLAMA_UBATCH=256\n"
	if err := os.WriteFile(ini, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := loadConfig(ini)
	if cfg.CtxSize != 262144 {
		t.Fatalf("CtxSize=%d, ждали 262144", cfg.CtxSize)
	}
	if !cfg.NoKVOffload {
		t.Fatal("NoKVOffload должен быть true")
	}
	if cfg.CacheTypeK != "q8_0" {
		t.Fatalf("CacheTypeK=%q", cfg.CacheTypeK)
	}
	if cfg.CtxCheckpoints != "0" || cfg.CacheRam != "0" || cfg.BatchSize != "512" || cfg.UbatchSize != "256" {
		t.Fatalf("mitigations: checkpoints=%q ram=%q batch=%q ubatch=%q",
			cfg.CtxCheckpoints, cfg.CacheRam, cfg.BatchSize, cfg.UbatchSize)
	}
}

func TestLlamaThreadCountAutoUsesBothSockets(t *testing.T) {
	if n := llamaThreadCount(Config{Threads: 28}); n != 28 {
		t.Fatalf("явные 28 потоков стали %d", n)
	}
	n := llamaThreadCount(Config{Threads: 0})
	if n < 2 {
		t.Fatalf("auto threads=%d", n)
	}
	hw := probeHardware(false)
	if hw.Summary.Cores >= 40 && n < hw.Summary.Cores {
		t.Fatalf("два сокета, %d физических ядер, а llama threads=%d — второй Xeon снова спит", hw.Summary.Cores, n)
	}
}

func TestLlamaProcEnvDoesNotBindOneGroup(t *testing.T) {
	env := strings.Join(llamaProcEnv(44), "\n")
	if strings.Contains(env, "OMP_PROC_BIND=spread") || strings.Contains(env, "OMP_PLACES=cores") {
		t.Fatal("OpenMP снова прибит к одной процессорной группе")
	}
	if !strings.Contains(env, "OMP_PROC_BIND=false") {
		t.Fatal("нет запрета OpenMP-affinity")
	}
}

func TestLoadConfigNoKVOffloadDefaultOff(t *testing.T) {
	dir := t.TempDir()
	ini := filepath.Join(dir, "settings.ini")
	if err := os.WriteFile(ini, []byte("LLAMA_CTX_SIZE=32768\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := loadConfig(ini)
	if cfg.NoKVOffload {
		t.Fatal("без ключа NoKVOffload должен быть false")
	}
	if cfg.CtxSize != 32768 {
		t.Fatalf("CtxSize=%d", cfg.CtxSize)
	}
}
