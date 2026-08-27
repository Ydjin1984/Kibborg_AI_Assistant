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
	LlamaExe    string
	ModelPath   string
	MmprojPath  string
	BrainPort   int
	CtxSize     int
	GpuLayers   int
	TensorSplit string // "0.45,0.55" — доля модели на каждый GPU (по индексу). Пусто = авто.
	// SplitMode: llama-server -sm none|layer|row|tensor. none = один GPU без расщепления
	// (KIBORG-FAST: нет PCIe-трафика между картами), layer = по слоям (дефолт, KV едет
	// вместе со слоями — так 3060 получает хвост модели + свой кусок кэша).
	// Пусто = флаг не передавать (дефолт llama.cpp = layer).
	SplitMode string
	MainGpu   int // GPU для KV-кэша/compute-буферов. -1 = не задавать (дефолт llama.cpp).
	// Device: --device (напр. CUDA1) — жёстко на одну карту; пусто = все GPU / tensor-split.
	// Qwen 35B: DEVICE пустой + TENSOR_SPLIT (обе 3060).
	Device string
	// CacheTypeK/V: тип KV-кэша (f16 по умолчанию llama.cpp). q8_0 экономит VRAM/bandwidth
	// на 12 GB картах и часто даёт +decode tok/s. Пусто = не передавать флаг.
	CacheTypeK string
	CacheTypeV string
	// NoKVOffload: --no-kv-offload. Веса остаются на GPU, KV-кэш в RAM. На 2×12 ГБ это
	// единственный стабильный путь к родному окну 256K у Qwen3.6-35B-A3B (~3 ГБ RAM).
	NoKVOffload bool
	// Пустая строка = не передавать флаг (дефолт llama.cpp). "0" отключает.
	// CtxCheckpoints=0 и CacheRam=0 нужны Qwen3.6-35B-A3B: гибридный attention
	// инвалидирует checkpoint каждый ход и llama-server зависает/падает
	// (ggml-org/llama.cpp#22450). Batch/Ubatch режут CUDA-граф на 256K.
	CtxCheckpoints string
	CacheRam       string
	BatchSize      string
	UbatchSize     string
	// GPUPowerLimits (GPU_POWER_LIMIT_W): "подстрока имени:ватты" через запятую.
	// Перед каждым запуском мозга движок режет пиковые транзиенты карт через
	// nvidia-smi -pl: загрузка GGUF в VRAM роняла 3090 с шины PCIe
	// (BSOD 0x124 WHEA Surprise Down). Пусто = питание не трогать.
	GPUPowerLimits string
	// Threads: llama-server --threads. 0 = все физические ядра обоих сокетов
	// (на 2×Xeon иначе Windows сажает процесс в одну CPU-группу — второй сокет спит).
	Threads   int
	Parallel  int    // llama-server --parallel (слоты). 0/пусто = 1 (скорость). 4+ жрёт VRAM.
	Reasoning string // llama-server --reasoning: off|on|auto. Пусто = off (без thinking).
	// Сэмплинг чата. Пустые/0 = официальные Qwen3.8 instruct: top_p 0.80, top_k 20, presence 1.5.
	// Агент (tool-loop) эти значения не берёт — ему нужна более детерминированная выборка.
	LLMTopP            float64
	LLMTopK            int
	LLMPresencePenalty float64
	// LLMMaxTokens: жёсткий потолок генерации одного хода. llama-server без max_tokens
	// генерирует до EOS или конца контекста; новые модели (Gemma 4) EOS в tool-режиме
	// выдают нестабильно — ход уходил в 5000+ токенов «потока сознания», пока движок
	// не отменял задачу. Потолок обрезает такой ход: агент получает текст и нудж.
	LLMMaxTokens  int
	TelegramToken string
	TelegramID    string

	// Голос: TypeWhisper (primary) → whisper.cpp (fallback). Без обоих — голос выключен.
	// TypeWhisper: системная диктовка + HTTP API (по умолчанию :8978). Пустой URL =
	// auto-discover из %LOCALAPPDATA%\TypeWhisper\api-discovery.json, затем 127.0.0.1:8978.
	TypeWhisperURL   string // например http://127.0.0.1:8978; "off" = не использовать
	TypeWhisperToken string // если в TypeWhisper включён Require API Token
	// whisper.cpp server (fallback / standalone). Пустые пути = не использовать.
	WhisperExe   string // путь к whisper-server.exe из сборки whisper.cpp
	WhisperModel string // путь к ggml-модели whisper (например ggml-base.bin)
	WhisperPort  int
	FfmpegPath   string // путь к ffmpeg; пусто = искать в PATH
	// Документы (§23): PDF читается poppler'ом, сканы распознаёт tesseract.
	PopplerDir    string // каталог с pdftotext/pdftoppm/pdfinfo; пусто = PATH
	TesseractPath string // путь к tesseract.exe; пусто = PATH или стандартная установка
	TessdataDir   string // каталог языковых пакетов OCR; пусто = runtime/tessdata

	WebPort int // локальный веб-интерфейс (127.0.0.1); 0 = выключен

	// HandsRoots: дополнительные каталоги, где агент пишет/удаляет без подтверждения
	// (через ; или ,). Проект, Desktop и engine-go/runtime разрешены всегда (ТЗ §6.1).
	HandsRoots string

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

	// Озвучка (Qwen3-TTS 0.6B на GPU). Режим always/ask — runtime/tts_mode.json.
	TTSURL     string // http://127.0.0.1:7788; пусто = auto; off = выключить
	TTSExe     string // python/server: путь к python.exe или server.py; пусто = tts_server/.venv
	TTSPort    int    // порт serve, по умолчанию 7788
	TTSVoice   string // Serena/Vivian (женские) или Ryan/Aiden… Пусто = Serena
	TTSModel   string // HF id или локальный путь. Пусто = Qwen3-TTS-12Hz-0.6B-CustomVoice
	TTSGPU     int    // индекс nvidia-smi для озвучки. -1 = не трогать CUDA_VISIBLE_DEVICES
	TTSSteps   int    // устарело (SuperTonic); оставлено для совместимости ini
	TTSThreads int    // устарело (SuperTonic ONNX); оставлено для совместимости ini
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
		LlamaExe:           kv["LLAMA_SERVER"],
		ModelPath:          kv["MODEL_PATH"],
		MmprojPath:         kv["MMPROJ_PATH"],
		BrainPort:          atoiDefault(kv["PORT_BRAIN"], 8083),
		CtxSize:            atoiDefault(kv["LLAMA_CTX_SIZE"], 32768),
		GpuLayers:          atoiDefault(kv["LLAMA_GPU_LAYERS"], 99),
		TensorSplit:        strings.TrimSpace(kv["LLAMA_TENSOR_SPLIT"]),
		SplitMode:          strings.TrimSpace(kv["LLAMA_SPLIT_MODE"]),
		MainGpu:            atoiDefault(kv["LLAMA_MAIN_GPU"], -1),
		Device:             strings.TrimSpace(kv["LLAMA_DEVICE"]),
		CacheTypeK:         strings.TrimSpace(kv["LLAMA_CACHE_TYPE_K"]),
		CacheTypeV:         strings.TrimSpace(kv["LLAMA_CACHE_TYPE_V"]),
		NoKVOffload:        boolDefault(kv["LLAMA_NO_KV_OFFLOAD"], false),
		CtxCheckpoints:     strings.TrimSpace(kv["LLAMA_CTX_CHECKPOINTS"]),
		CacheRam:           strings.TrimSpace(kv["LLAMA_CACHE_RAM"]),
		BatchSize:          strings.TrimSpace(kv["LLAMA_BATCH"]),
		UbatchSize:         strings.TrimSpace(kv["LLAMA_UBATCH"]),
		GPUPowerLimits:     strings.TrimSpace(kv["GPU_POWER_LIMIT_W"]),
		Threads:            atoiDefault(kv["LLAMA_THREADS"], 0),
		Parallel:           atoiDefault(kv["LLAMA_PARALLEL"], 1),
		Reasoning:          strings.ToLower(strings.TrimSpace(kv["LLAMA_REASONING"])),
		LLMTopP:            atofDefault(kv["LLM_TOP_P"], 0.80),
		LLMTopK:            atoiDefault(kv["LLM_TOP_K"], 20),
		LLMPresencePenalty: atofDefault(kv["LLM_PRESENCE"], 1.5),
		LLMMaxTokens:       atoiDefault(kv["LLM_MAX_TOKENS"], 2048),
		TelegramToken:      kv["TELEGRAM_TOKEN"],
		TelegramID:         kv["TELEGRAM_ID"],

		TypeWhisperURL:   strings.TrimSpace(kv["TYPEWHISPER_URL"]),
		TypeWhisperToken: strings.TrimSpace(kv["TYPEWHISPER_TOKEN"]),
		WhisperExe:       kv["WHISPER_SERVER"],
		WhisperModel:     kv["WHISPER_MODEL"],
		WhisperPort:      atoiDefault(kv["PORT_WHISPER"], 8081),
		FfmpegPath:       kv["FFMPEG"],
		PopplerDir:       strings.TrimSpace(kv["POPPLER_DIR"]),
		TesseractPath:    strings.TrimSpace(kv["TESSERACT"]),
		TessdataDir:      strings.TrimSpace(kv["TESSDATA_DIR"]),

		WebPort:    atoiDefault(kv["PORT_WEB"], 8090),
		HandsRoots: strings.TrimSpace(kv["AGENT_HANDS_ROOTS"]),

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

		TTSURL:     strings.TrimSpace(kv["TTS_URL"]),
		TTSExe:     strings.TrimSpace(kv["TTS_SERVER"]),
		TTSPort:    atoiDefault(kv["PORT_TTS"], defaultTTSPort),
		TTSVoice:   strings.TrimSpace(kv["TTS_VOICE"]),
		TTSModel:   strings.TrimSpace(kv["TTS_MODEL"]),
		TTSGPU:     atoiDefault(kv["TTS_GPU"], 1),
		TTSSteps:   atoiDefault(kv["TTS_STEPS"], 8),
		TTSThreads: atoiDefault(kv["TTS_THREADS"], 0),
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
	if v := strings.TrimSpace(os.Getenv("AGENT_HANDS_ROOTS")); v != "" {
		cfg.HandsRoots = v
	}

	// Resolve relative model / mmproj paths against the engine-go directory.
	if wd, err := os.Getwd(); err == nil {
		if cfg.ModelPath != "" && !filepath.IsAbs(cfg.ModelPath) {
			cfg.ModelPath = filepath.Join(wd, cfg.ModelPath)
		}
		mp := strings.TrimSpace(cfg.MmprojPath)
		if mp != "" && !strings.EqualFold(mp, "auto") && !strings.EqualFold(mp, "off") &&
			!strings.EqualFold(mp, "none") && !filepath.IsAbs(mp) {
			cfg.MmprojPath = filepath.Join(wd, mp)
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
