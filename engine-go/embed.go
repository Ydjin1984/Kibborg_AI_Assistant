package main

// Embeddings for long-term memory: an optional second llama-server instance running an
// embedding model (bge / nomic / e5 …) on its own port. When configured, memory recall ranks
// past episodes by vector similarity; when not, memory falls back to keyword search and this
// whole file is inert. The embedding server is separate from the chat "brain" because
// llama.cpp puts a server into embedding mode (--embeddings) exclusively — one instance can't
// both chat and embed.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// embedEnabled reports whether a vector-embedding model is configured.
func embedEnabled(cfg Config) bool {
	return cfg.MemoryEnabled && cfg.EmbedModel != "" && cfg.EmbedExe != ""
}

// ensureEmbed launches the embedding server unless it's already up or not configured.
func ensureEmbed(cfg Config) {
	if !embedEnabled(cfg) {
		log.Printf("[EMBED] EMBED_MODEL не задан — векторная память выключена (работает keyword-поиск)")
		return
	}
	if portInUse(cfg.EmbedPort) {
		log.Printf("[EMBED] port :%d already in use — not launching a second embed instance", cfg.EmbedPort)
		return
	}
	if _, err := os.Stat(cfg.EmbedExe); err != nil {
		log.Printf("[EMBED] llama-server not found: %s", cfg.EmbedExe)
		return
	}
	if _, err := os.Stat(cfg.EmbedModel); err != nil {
		log.Printf("[EMBED] embed model not found: %s", cfg.EmbedModel)
		return
	}
	args := []string{
		"-m", cfg.EmbedModel,
		"--embeddings",
		"--port", strconv.Itoa(cfg.EmbedPort),
		"--n-gpu-layers", strconv.Itoa(cfg.EmbedGpuLayers),
		"--ctx-size", "8192",
	}
	cmd := exec.Command(cfg.EmbedExe, args...)
	cmd.Dir = filepath.Dir(cfg.EmbedExe) // load CUDA DLLs from the build dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Printf("[EMBED] failed to launch embed server: %v", err)
		return
	}
	registerEngineProc(cmd.Process)
	log.Printf("[EMBED] embed server launched (pid %d) :%d — %s",
		cmd.Process.Pid, cfg.EmbedPort, filepath.Base(cfg.EmbedModel))
}

// embedReady reports whether the embedding server answers /health with 200.
func embedReady(port int) bool {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

var embedHTTP = &http.Client{Timeout: 30 * time.Second}

// embedText returns the embedding vector for text via the OpenAI-compatible /v1/embeddings
// endpoint. Returns nil (not an error) when embeddings are disabled, so callers can pass the
// result straight through to keyword-fallback recall.
func embedText(cfg Config, text string) ([]float32, error) {
	if !embedEnabled(cfg) || text == "" {
		return nil, nil
	}
	if !embedReady(cfg.EmbedPort) {
		return nil, nil // server not up yet — degrade to keyword recall silently
	}
	body, _ := json.Marshal(map[string]any{"input": text})
	resp, err := embedHTTP.Post(
		fmt.Sprintf("http://127.0.0.1:%d/v1/embeddings", cfg.EmbedPort),
		"application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("embed HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	if len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embed: пустой ответ")
	}
	return parsed.Data[0].Embedding, nil
}
