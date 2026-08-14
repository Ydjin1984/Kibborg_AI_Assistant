package main

// HTTP и Telegram-обвязка каталога моделей. Само железо и Hugging Face живут
// в hardware.go / models_hub.go — здесь только вход с панели и из чата.

import (
	"encoding/json"
	"net/http"
	"strings"
)

func registerModelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/hardware", sameOriginGuard(handleAPIHardware))
	mux.HandleFunc("/api/models", sameOriginGuard(handleAPIModels))
	mux.HandleFunc("/api/models/download", sameOriginGuard(handleAPIModelDownload))
	mux.HandleFunc("/api/models/cancel", sameOriginGuard(handleAPIModelCancel))
	mux.HandleFunc("/api/models/assign", sameOriginGuard(handleAPIModelAssign))
}

func handleAPIHardware(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	force := r.URL.Query().Get("fresh") == "1" || r.URL.Query().Get("force") == "1"
	writeJSON(w, probeHardware(force))
}

func handleAPIModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	publisher := strings.TrimSpace(r.URL.Query().Get("publisher"))
	fit := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("fit")))
	vision := truthyQuery(r, "vision")
	tools := truthyQuery(r, "tools")
	reasoning := truthyQuery(r, "reasoning")
	hw := probeHardware(false)
	models, err := searchHubModels(q, publisher, "", vision, tools, reasoning, hw)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error(), "hardware": hw, "local": listLocalModels(hw)})
		return
	}
	if fit != "" && fit != "all" {
		models = filterByFit(models, fit)
	}
	writeJSON(w, map[string]any{
		"hardware":   hw,
		"models":     models,
		"local":      listLocalModels(hw),
		"publishers": publishersOf(models),
		"query":      q,
		"fit":        fit,
		"download":   downloadSnapshot(),
		"assigned":   filepathBase(webCfg.ModelPath),
		"running":    liveBrainModel,
	})
}

func handleAPIModelDownload(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, downloadSnapshot())
		return
	case http.MethodPost:
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Repo   string `json:"repo"`
		File   string `json:"file"`
		Assign *bool  `json:"assign"`
		MMProj *bool  `json:"mmproj"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "нужны repo и file", http.StatusBadRequest)
		return
	}
	assign := true
	if req.Assign != nil {
		assign = *req.Assign
	}
	withMM := true
	if req.MMProj != nil {
		withMM = *req.MMProj
	}
	if err := startModelDownload(req.Repo, req.File, assign, withMM); err != nil {
		writeJSON(w, map[string]any{"error": err.Error(), "download": downloadSnapshot()})
		return
	}
	writeJSON(w, downloadSnapshot())
}

func handleAPIModelCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	cancelModelDownload()
	writeJSON(w, downloadSnapshot())
}

func handleAPIModelAssign(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Path   string `json:"path"`
		MMProj string `json:"mmproj"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Path) == "" {
		http.Error(w, "нужно поле path", http.StatusBadRequest)
		return
	}
	if err := assignBrainModel(req.Path, req.MMProj); err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"ok":      true,
		"model":   filepathBase(webCfg.ModelPath),
		"mmproj":  filepathBase(webCfg.MmprojPath),
		"running": liveBrainModel,
		"restart": liveBrainModel != "" && liveBrainModel != filepathBase(webCfg.ModelPath),
		"text":    "Прописал в settings.ini. Мозг в VRAM не трогал — Stop → Start, когда будешь готов.",
	})
}

func truthyQuery(r *http.Request, key string) bool {
	v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
