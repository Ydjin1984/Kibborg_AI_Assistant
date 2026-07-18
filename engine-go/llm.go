package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// chatMsg is one OpenAI-style chat message.
type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ensureBrain starts llama-server (the LLM) unless the brain port is already in use.
// It checks for a LISTENER (not /health), because a model that is still loading or busy
// answers /health with 503 — launching a second 17GB model would thrash the GPU.
func ensureBrain(cfg Config) {
	if portInUse(cfg.BrainPort) {
		// The port is busy — but by WHAT? A real llama-server (answers /health 200 when ready
		// or 503 while loading) means "brain already up, reuse it". A foreign process squatting
		// on the port (another dev server, a stray `go run`, …) answers 404 — then the model
		// would NEVER load and every reply would be "модель ещё грузится" with no clue why.
		// Say so loudly instead of silently giving up.
		if llamaOnPort(cfg.BrainPort) {
			log.Printf("[LLM] brain port :%d already serving llama-server — reusing it", cfg.BrainPort)
		} else {
			log.Printf("[LLM] ⚠ порт :%d занят ПОСТОРОННИМ процессом (его /health не похож на llama-server) — "+
				"модель НЕ будет загружена. Освободи порт 8080 (закрой чужой процесс) ИЛИ смени PORT_BRAIN "+
				"в settings.ini на свободный, затем перезапусти бота.", cfg.BrainPort)
		}
		return
	}
	if cfg.LlamaExe == "" || cfg.ModelPath == "" {
		log.Printf("[LLM] LLAMA_SERVER or MODEL_PATH not set in settings.ini — cannot launch brain")
		return
	}
	if _, err := os.Stat(cfg.LlamaExe); err != nil {
		log.Printf("[LLM] llama-server not found: %s", cfg.LlamaExe)
		return
	}
	if _, err := os.Stat(cfg.ModelPath); err != nil {
		log.Printf("[LLM] model not found: %s", cfg.ModelPath)
		return
	}

	args := []string{
		"-m", cfg.ModelPath,
		"--ctx-size", strconv.Itoa(cfg.CtxSize),
		"--n-gpu-layers", strconv.Itoa(cfg.GpuLayers),
		"--threads", strconv.Itoa(cfg.Threads),
		"--port", strconv.Itoa(cfg.BrainPort),
		"--flash-attn", "on",
	}
	// Multi-GPU placement. The monitor is wired to GPU 1, so by default we push more of
	// the model onto the free GPU 0 (LLAMA_TENSOR_SPLIT) and keep the KV-cache/compute
	// buffers off the display card (LLAMA_MAIN_GPU). Leaving both unset = llama.cpp auto.
	if cfg.TensorSplit != "" {
		args = append(args, "--tensor-split", cfg.TensorSplit)
		log.Printf("[LLM] tensor-split: %s", cfg.TensorSplit)
	}
	if cfg.MainGpu >= 0 {
		args = append(args, "--main-gpu", strconv.Itoa(cfg.MainGpu))
		log.Printf("[LLM] main-gpu: %d", cfg.MainGpu)
	}
	// Vision: use explicit MMPROJ_PATH, else auto-detect an mmproj next to the model
	// so screenshots/images work without editing settings.ini.
	mmproj := cfg.MmprojPath
	if mmproj == "" {
		mmproj = autoDetectMmproj(cfg.ModelPath)
	}
	if mmproj != "" {
		args = append(args, "--mmproj", mmproj)
		log.Printf("[LLM] vision enabled via mmproj: %s", filepath.Base(mmproj))
	}

	cmd := exec.Command(cfg.LlamaExe, args...)
	// Run from the llama build dir so its CUDA DLLs load (matches start-brain.cmd).
	cmd.Dir = filepath.Dir(cfg.LlamaExe)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Printf("[LLM] failed to launch brain: %v", err)
		return
	}
	registerEngineProc(cmd.Process)
	log.Printf("[LLM] launching brain (pid %d): %s :%d — model load may take 1-5 min",
		cmd.Process.Pid, filepath.Base(cfg.ModelPath), cfg.BrainPort)
}

// autoDetectMmproj returns the first mmproj*.gguf found in the model's directory, if any.
func autoDetectMmproj(modelPath string) string {
	dir := filepath.Dir(modelPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if !e.IsDir() && strings.Contains(name, "mmproj") && strings.HasSuffix(name, ".gguf") {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// portInUse reports whether anything is listening on the local port.
func portInUse(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// llamaOnPort reports whether the server on port looks like llama-server: its /health answers
// 200 (ready) or 503 (still loading the model). A foreign process squatting on the port
// answers 404 (or nothing sensible) → false. Used to distinguish "our brain is already up"
// from "someone else stole the port".
func llamaOnPort(port int) bool {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200 || resp.StatusCode == 503
}

// brainReady reports whether the LLM HTTP server answers /health with 200.
func brainReady(port int) bool {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// waitForBrain blocks until the LLM is ready or the timeout elapses.
func waitForBrain(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if brainReady(port) {
			return true
		}
		time.Sleep(3 * time.Second)
	}
	return brainReady(port)
}

// warmUpBrain forces the model fully into VRAM as soon as llama-server reports ready, by
// firing one 1-token inference. /health flips to 200 once the WEIGHTS are loaded, but
// llama.cpp still allocates the KV-cache + compute buffers and loads its CUDA kernels
// lazily on the FIRST decode — that's why the GPUs only "fill up" (and the first reply
// lags) when the first user message arrives. We pay that one-time cost here at startup
// instead, so the GPUs are pre-loaded and the user's first request is immediately fast.
func warmUpBrain(cfg Config) {
	if !waitForBrain(cfg.BrainPort, 10*time.Minute) {
		log.Printf("[LLM] warmup skipped — brain not ready within timeout")
		return
	}
	body, _ := json.Marshal(map[string]any{
		"messages":   []map[string]any{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
		"stream":     false,
	})
	start := time.Now()
	c := &http.Client{Timeout: 120 * time.Second}
	resp, err := c.Post(
		fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", cfg.BrainPort),
		"application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("[LLM] warmup request failed: %v", err)
		return
	}
	resp.Body.Close()
	log.Printf("[LLM] warmup done in %s — model resident in VRAM, first user request will be fast",
		time.Since(start).Round(time.Second))
}

// Sampling temperatures: lower = more deterministic. /chart uses a low temperature so
// the trading analysis sticks to numbers on the chart instead of inventing them.
const (
	defaultTemp = 0.7
	chartTemp   = 0.3
)

// llmChat sends the conversation to llama-server's OpenAI-compatible endpoint and
// returns the assistant reply text. Messages are generic maps so content can be a
// plain string (text) or an array of parts (text + image_url) for vision.
func llmChat(port int, messages []map[string]any, temperature float64) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"messages":     messages,
		"temperature":  temperature,
		"stream":       false,
		"cache_prompt": true,
	})
	c := &http.Client{Timeout: 180 * time.Second}
	resp, err := c.Post(
		fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port),
		"application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var parsed struct {
		Choices []struct {
			Message chatMsg `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("LLM bad JSON: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("LLM empty response")
	}
	return parsed.Choices[0].Message.Content, nil
}

// llmChatStream is llmChat with stream:true. It parses the SSE stream from llama-server and
// calls onDelta with the ACCUMULATED text after every chunk, returning the final full text
// plus llama.cpp's timing stats. onDelta must be fast (it's on the read path); the Telegram
// edit throttling lives in the caller. A mid-stream network error still returns text so far.
func llmChatStream(port int, messages []map[string]any, temperature float64, onDelta func(string)) (string, GenStats, error) {
	body, _ := json.Marshal(map[string]any{
		"messages":          messages,
		"temperature":       temperature,
		"stream":            true,
		"cache_prompt":      true,
		"timings_per_token": true, // ask llama.cpp to include its timings in the stream
	})
	c := &http.Client{Timeout: 300 * time.Second}
	resp, err := c.Post(
		fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port),
		"application/json", bytes.NewReader(body))
	if err != nil {
		return "", GenStats{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		return "", GenStats{}, fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var full strings.Builder
	var stats GenStats
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			// llama.cpp extension: per-request generation timings.
			Timings *struct {
				PromptN            int     `json:"prompt_n"`
				PromptMs           float64 `json:"prompt_ms"`
				PromptPerSecond    float64 `json:"prompt_per_second"`
				PredictedN         int     `json:"predicted_n"`
				PredictedMs        float64 `json:"predicted_ms"`
				PredictedPerSecond float64 `json:"predicted_per_second"`
			} `json:"timings"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // tolerate malformed keep-alives
		}
		if chunk.Timings != nil {
			stats = GenStats{
				PromptTokens: chunk.Timings.PromptN,
				PromptPerSec: chunk.Timings.PromptPerSecond,
				GenTokens:    chunk.Timings.PredictedN,
				GenPerSec:    chunk.Timings.PredictedPerSecond,
				TotalMs:      chunk.Timings.PromptMs + chunk.Timings.PredictedMs,
			}
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			full.WriteString(chunk.Choices[0].Delta.Content)
			live.tick()
			if onDelta != nil {
				onDelta(full.String())
			}
		}
	}
	if err := sc.Err(); err != nil && full.Len() == 0 {
		return "", stats, fmt.Errorf("LLM stream error: %w", err)
	}
	if full.Len() == 0 {
		return "", stats, fmt.Errorf("LLM empty response")
	}
	return full.String(), stats, nil
}

// ===== Tool calling (OpenAI function-calling over llama-server) =====

// toolCall is one function call the model asked for. Arguments is a JSON string (the
// OpenAI wire format), parsed by the caller.
type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// assistantMsg is the model's reply when tools are in play: it carries either prose
// (Content) or one/more tool_calls — or both.
type assistantMsg struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []toolCall `json:"tool_calls"`
}

// llmChatTools is like llmChat but advertises tools and returns the raw assistant message so
// the caller can act on tool_calls. Pass tools=nil to force a plain text turn (e.g. the
// final "summarize what you found" step). Messages are generic maps so they can include
// assistant tool_calls and role:"tool" results.
func llmChatTools(port int, messages []map[string]any, tools any, temperature float64) (assistantMsg, error) {
	payload := map[string]any{
		"messages":     messages,
		"temperature":  temperature,
		"stream":       false,
		"cache_prompt": true,
	}
	if tools != nil {
		payload["tools"] = tools
		payload["tool_choice"] = "auto"
	}
	body, _ := json.Marshal(payload)
	c := &http.Client{Timeout: 180 * time.Second}
	resp, err := c.Post(
		fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port),
		"application/json", bytes.NewReader(body))
	if err != nil {
		return assistantMsg{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return assistantMsg{}, fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var parsed struct {
		Choices []struct {
			Message assistantMsg `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return assistantMsg{}, fmt.Errorf("LLM bad JSON: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return assistantMsg{}, fmt.Errorf("LLM empty response")
	}
	return parsed.Choices[0].Message, nil
}
