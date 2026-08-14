package main

// HTTP озвучки: тумблер режима и синтез. Кнопка «Озвучить» и автопроигрывание
// в панели ходят сюда; Telegram использует то же ядро напрямую.

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"
)

func registerTTSRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/tts", sameOriginGuard(handleAPITTS))
	mux.HandleFunc("/api/speak", sameOriginGuard(handleAPISpeak))
}

func handleAPITTS(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		var req struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "нужно поле mode", http.StatusBadRequest)
			return
		}
		setTTSMode(req.Mode, "web")
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
		return
	}
	mode := currentTTSMode()
	writeJSON(w, map[string]any{
		"mode":    mode,
		"auto":    mode == ttsModeAuto,
		"label":   ttsModeShort(mode),
		"status":  ttsStatus(webCfg),
		"voice":   orDash(strings.TrimSpace(webCfg.TTSVoice)),
		"enabled": ttsWanted(webCfg),
	})
}

func handleAPISpeak(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		http.Error(w, "нужно поле text", http.StatusBadRequest)
		return
	}
	log.Printf("[TTS] панель «Озвучить»: исходник %d символов", utf8.RuneCountInString(req.Text))
	sf, err := synthesizeSpeech(webCfg, req.Text)
	if err != nil {
		log.Printf("[TTS] панель: %v", err)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	rememberSpeakable(webChatID, req.Text)
	writeJSON(w, map[string]any{
		"url":    sf.URL,
		"lang":   sf.Lang,
		"chars":  sf.Chars,
		"wav":    sf.WAV,
		"status": "ok",
	})
}
