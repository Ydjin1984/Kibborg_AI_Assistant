package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the minimal set of values the LLM-chat bot needs, read from settings.ini
// (so the user edits settings.ini, not code). Env vars override where noted.
type Config struct {
	LlamaExe      string
	ModelPath     string
	MmprojPath    string
	BrainPort     int
	CtxSize       int
	GpuLayers     int
	TensorSplit   string // "0.55,0.45" — доля модели на каждый GPU (по индексу). Пусто = авто.
	MainGpu       int    // GPU для KV-кэша/compute-буферов. -1 = не задавать (дефолт llama.cpp).
	Threads       int
	TelegramToken string
	TelegramID    string

	// Голос (распознавание речи через whisper.cpp server). Пустые пути = голос выключен.
	WhisperExe   string // путь к whisper-server.exe из сборки whisper.cpp
	WhisperModel string // путь к ggml-модели whisper (например ggml-base.bin)
	WhisperPort  int
	FfmpegPath   string // путь к ffmpeg; пусто = искать в PATH

	WebPort int // локальный веб-интерфейс (127.0.0.1); 0 = выключен

	StopBrainOnExit bool // гасить запущенный нами llama-server при выходе бота

	// Долговременная память (SQLite + опциональные эмбеддинги). EmbedModel пуст = векторный
	// recall выключен, память работает на keyword-поиске. EmbedExe пуст = берём LlamaExe.
	MemoryEnabled  bool
	EmbedExe       string // llama-server.exe для embed-модели (пусто = как LLAMA_SERVER)
	EmbedModel     string // путь к GGUF embedding-модели (bge/nomic/e5…); пусто = без векторов
	EmbedPort      int    // порт embed-сервера
	EmbedGpuLayers int    // сколько слоёв embed-модели на GPU

	// Трейдер: параметры по умолчанию для расчёта размера позиции (/size, /analyze).
	TradeBalance  float64 // размер счёта (в валюте котировки, напр. USDT). 0 = не задан.
	TradeRiskPct  float64 // риск на сделку в % от счёта (напр. 1.5)
	TradeLeverage float64 // плечо по умолчанию (1 = спот/без плеча)
	TradeMMR      float64 // maintenance margin rate для оценки ликвидации (напр. 0.005)
}

// loadConfig parses simple KEY=VALUE lines from settings.ini (ignoring # comments),
// applies sensible defaults, and resolves a relative MODEL_PATH against engine-go/.
func loadConfig(iniPath string) Config {
	kv := map[string]string{}
	if f, err := os.Open(iniPath); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			i := strings.Index(line, "=")
			if i < 0 {
				continue
			}
			key := strings.TrimSpace(line[:i])
			val := strings.TrimSpace(line[i+1:])
			kv[key] = val
		}
	}

	cfg := Config{
		LlamaExe:      kv["LLAMA_SERVER"],
		ModelPath:     kv["MODEL_PATH"],
		MmprojPath:    kv["MMPROJ_PATH"],
		BrainPort:     atoiDefault(kv["PORT_BRAIN"], 8080),
		CtxSize:       atoiDefault(kv["LLAMA_CTX_SIZE"], 32768),
		GpuLayers:     atoiDefault(kv["LLAMA_GPU_LAYERS"], 99),
		TensorSplit:   strings.TrimSpace(kv["LLAMA_TENSOR_SPLIT"]),
		MainGpu:       atoiDefault(kv["LLAMA_MAIN_GPU"], -1),
		Threads:       atoiDefault(kv["LLAMA_THREADS"], 28),
		TelegramToken: kv["TELEGRAM_TOKEN"],
		TelegramID:    kv["TELEGRAM_ID"],

		WhisperExe:   kv["WHISPER_SERVER"],
		WhisperModel: kv["WHISPER_MODEL"],
		WhisperPort:  atoiDefault(kv["PORT_WHISPER"], 8081),
		FfmpegPath:   kv["FFMPEG"],

		WebPort: atoiDefault(kv["PORT_WEB"], 8090),

		StopBrainOnExit: boolDefault(kv["STOP_BRAIN_ON_EXIT"], false),

		MemoryEnabled:  boolDefault(kv["MEMORY_ENABLED"], true),
		EmbedExe:       kv["EMBED_SERVER"],
		EmbedModel:     kv["EMBED_MODEL"],
		EmbedPort:      atoiDefault(kv["PORT_EMBED"], 8082),
		EmbedGpuLayers: atoiDefault(kv["EMBED_GPU_LAYERS"], 99),

		TradeBalance:  atofDefault(kv["TRADE_BALANCE"], 0),
		TradeRiskPct:  atofDefault(kv["TRADE_RISK_PCT"], 1.0),
		TradeLeverage: atofDefault(kv["TRADE_LEVERAGE"], 1),
		TradeMMR:      atofDefault(kv["TRADE_MMR"], 0.005),
	}

	// Embed exe defaults to the main llama-server build; embed model may be relative to engine-go.
	if cfg.EmbedExe == "" {
		cfg.EmbedExe = cfg.LlamaExe
	}
	if cfg.EmbedModel != "" && !filepath.IsAbs(cfg.EmbedModel) {
		if wd, err := os.Getwd(); err == nil {
			cfg.EmbedModel = filepath.Join(wd, cfg.EmbedModel)
		}
	}

	// Env overrides (handy for ad-hoc runs).
	if v := strings.TrimSpace(os.Getenv("TELEGRAM_TOKEN")); v != "" {
		cfg.TelegramToken = v
	}
	if v := strings.TrimSpace(os.Getenv("TELEGRAM_ID")); v != "" {
		cfg.TelegramID = v
	}

	// Resolve a relative model path against the engine-go directory.
	if cfg.ModelPath != "" && !filepath.IsAbs(cfg.ModelPath) {
		if wd, err := os.Getwd(); err == nil {
			cfg.ModelPath = filepath.Join(wd, cfg.ModelPath)
		}
	}
	return cfg
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func atofDefault(s string, def float64) float64 {
	if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return f
	}
	return def
}

func boolDefault(s string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on", "да":
		return true
	case "0", "false", "no", "off", "нет":
		return false
	}
	return def
}
