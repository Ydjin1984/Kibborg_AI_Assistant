package main

// Режим озвучки (всегда / по запросу). Как руки: runtime-store, не settings.ini —
// тумблер должен срабатывать сразу в Telegram и в панели.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	ttsModeAsk  = "ask"
	ttsModeAuto = "auto"
	ttsModePath = "runtime/tts_mode.json"
)

type ttsModeFile struct {
	Mode      string `json:"mode"`
	UpdatedAt string `json:"updated_at"`
	By        string `json:"by,omitempty"`
}

var (
	ttsModeMu   sync.RWMutex
	ttsMode     = ttsModeAsk
	ttsModeLoad bool
)

func loadTTSMode() {
	ttsModeMu.Lock()
	defer ttsModeMu.Unlock()
	ttsModeLoad = true
	data, err := os.ReadFile(ttsModePath)
	if err != nil {
		ttsMode = ttsModeAsk
		return
	}
	var f ttsModeFile
	if err := json.Unmarshal(data, &f); err != nil {
		log.Printf("[TTS] %s повреждён (%v) — режим ask", ttsModePath, err)
		ttsMode = ttsModeAsk
		return
	}
	ttsMode = normalizeTTSMode(f.Mode)
	log.Printf("[TTS] режим озвучки: %s", ttsMode)
}

func currentTTSMode() string {
	ttsModeMu.RLock()
	defer ttsModeMu.RUnlock()
	if !ttsModeLoad {
		return ttsModeAsk
	}
	return ttsMode
}

func ttsAutoOn() bool { return currentTTSMode() == ttsModeAuto }

func setTTSMode(mode, by string) string {
	m := normalizeTTSMode(mode)
	ttsModeMu.Lock()
	ttsMode = m
	ttsModeLoad = true
	ttsModeMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(ttsModePath), 0o755); err == nil {
		body, _ := json.MarshalIndent(ttsModeFile{
			Mode: m, UpdatedAt: time.Now().Format(time.RFC3339), By: by,
		}, "", "  ")
		if err := os.WriteFile(ttsModePath, body, 0o644); err != nil {
			log.Printf("[TTS] не сохранил режим: %v", err)
		}
	}
	log.Printf("[TTS] режим озвучки: %s (%s)", m, by)
	return m
}

func normalizeTTSMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ttsModeAuto, "always", "on", "всегда", "авто", "1", "true", "да":
		return ttsModeAuto
	default:
		return ttsModeAsk
	}
}

func ttsModeLabel(mode string) string {
	if mode == ttsModeAuto {
		return "🔊 **Озвучка всегда**: каждый ответ уходит голосом (SuperTonic, CPU). " +
			"Код, таблицы и статусы агента не читаю. По запросу: `/tts ask`."
	}
	return "🔇 **Озвучка по запросу**: кнопка «Озвучить» в панели или `/speak` в Telegram. " +
		"Включить на каждый ответ: `/tts auto`."
}

func ttsModeShort(mode string) string {
	if mode == ttsModeAuto {
		return "🔊 всегда"
	}
	return "🔇 по запросу"
}
