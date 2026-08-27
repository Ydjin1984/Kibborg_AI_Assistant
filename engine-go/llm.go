package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// chatMsg is one OpenAI-style chat message.
type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// brainServerArgs is the llama-server command line except --mmproj (mmproj is appended
// after a filesystem check so a missing projector does not fail the launch).
func brainServerArgs(cfg Config) []string {
	parallel := cfg.Parallel
	if parallel <= 0 {
		parallel = 1
	}
	reason := cfg.Reasoning
	if reason == "" {
		reason = "off"
	}
	threads := llamaThreadCount(cfg)
	args := []string{
		"-m", cfg.ModelPath,
		"--ctx-size", strconv.Itoa(cfg.CtxSize),
		"--n-gpu-layers", strconv.Itoa(cfg.GpuLayers),
		"--threads", strconv.Itoa(threads),
		"--threads-batch", strconv.Itoa(threads),
		"--port", strconv.Itoa(cfg.BrainPort),
		"--flash-attn", "on",
		"--parallel", strconv.Itoa(parallel),
		"--reasoning", reason,
	}
	if reason == "off" {
		args = append(args, "--reasoning-budget", "0")
	}
	if cfg.Device != "" {
		args = append(args, "--device", cfg.Device)
	}
	if cfg.SplitMode != "" {
		args = append(args, "--split-mode", cfg.SplitMode)
	}
	if cfg.TensorSplit != "" {
		args = append(args, "--tensor-split", cfg.TensorSplit)
	}
	if cfg.MainGpu >= 0 {
		args = append(args, "--main-gpu", strconv.Itoa(cfg.MainGpu))
	}
	if cfg.CacheTypeK != "" {
		args = append(args, "--cache-type-k", cfg.CacheTypeK)
	}
	if cfg.CacheTypeV != "" {
		args = append(args, "--cache-type-v", cfg.CacheTypeV)
	}
	if cfg.NoKVOffload {
		args = append(args, "--no-kv-offload")
	}
	if cfg.CtxCheckpoints != "" {
		args = append(args, "--ctx-checkpoints", cfg.CtxCheckpoints)
	}
	if cfg.CacheRam != "" {
		args = append(args, "--cache-ram", cfg.CacheRam)
	}
	if cfg.BatchSize != "" {
		args = append(args, "--batch-size", cfg.BatchSize)
	}
	if cfg.UbatchSize != "" {
		args = append(args, "--ubatch-size", cfg.UbatchSize)
	}
	return args
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
			setLiveBrainModel(filepath.Base(cfg.ModelPath))
			log.Printf("[LLM] brain port :%d already serving llama-server — reusing it", cfg.BrainPort)
			if pid := pidListeningOnPort(cfg.BrainPort); pid > 0 {
				go spreadBrainCPU(pid, "reuse")
			}
		} else {
			log.Printf("[LLM] ⚠ порт :%d занят ПОСТОРОННИМ процессом (его /health не похож на llama-server) — "+
				"модель НЕ будет загружена. Освободи этот порт (закрой чужой процесс) ИЛИ смени PORT_BRAIN "+
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

	args := brainServerArgs(cfg)
	if cfg.Device != "" {
		log.Printf("[LLM] device: %s", cfg.Device)
	}
	if cfg.TensorSplit != "" {
		log.Printf("[LLM] tensor-split: %s", cfg.TensorSplit)
	}
	if cfg.MainGpu >= 0 {
		log.Printf("[LLM] main-gpu: %d", cfg.MainGpu)
	}
	if cfg.CacheTypeK != "" || cfg.CacheTypeV != "" {
		log.Printf("[LLM] cache-type k=%s v=%s", cfg.CacheTypeK, cfg.CacheTypeV)
	}
	if cfg.NoKVOffload {
		log.Printf("[LLM] KV cache in RAM (--no-kv-offload), weights stay on GPU")
	}
	// Vision: ONLY when MMPROJ_PATH is set. Empty path = text-only (no auto-detect).
	// Auto-loading mmproj from the model folder silently steals ~1 GB VRAM and slows
	// every text turn — that was a major speed regression when mmproj sat next to GGUF.
	mmproj := strings.TrimSpace(cfg.MmprojPath)
	if strings.EqualFold(mmproj, "auto") {
		mmproj = autoDetectMmproj(cfg.ModelPath)
	}
	if mmproj != "" && !strings.EqualFold(mmproj, "off") && !strings.EqualFold(mmproj, "none") {
		if _, err := os.Stat(mmproj); err != nil {
			log.Printf("[LLM] mmproj not found (%s) — starting without vision", mmproj)
		} else {
			args = append(args, "--mmproj", mmproj)
			log.Printf("[LLM] vision enabled via mmproj: %s", filepath.Base(mmproj))
		}
	} else {
		log.Printf("[LLM] vision off (set MMPROJ_PATH=auto or path to mmproj.gguf to enable)")
	}
	reason := cfg.Reasoning
	if reason == "" {
		reason = "off"
	}
	parallel := cfg.Parallel
	if parallel <= 0 {
		parallel = 1
	}
	log.Printf("[LLM] parallel=%d reasoning=%s ctx=%d kv-offload=%v device=%s cache-k=%s cache-v=%s checkpoints=%s cache-ram=%s batch=%s ubatch=%s",
		parallel, reason, cfg.CtxSize, !cfg.NoKVOffload, orDash(cfg.Device), orDash(cfg.CacheTypeK), orDash(cfg.CacheTypeV),
		orDash(cfg.CtxCheckpoints), orDash(cfg.CacheRam), orDash(cfg.BatchSize), orDash(cfg.UbatchSize))

	// До запуска: пик транзиентов карты — на заливке весов в VRAM (см. gpu_power.go).
	applyGPUPowerLimits(cfg)

	logPath := brainLogPath()
	args = append(args, "--log-file", logPath)
	cmd := exec.Command(cfg.LlamaExe, args...)
	// Run from the llama build dir so its CUDA DLLs load (matches start-brain.cmd).
	cmd.Dir = filepath.Dir(cfg.LlamaExe)
	cmd.Env = llamaProcEnv(llamaThreadCount(cfg))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err != nil {
		log.Printf("[LLM] cannot write %s: %v — llama stdout only on console", logPath, err)
	} else {
		cmd.Stdout = io.MultiWriter(os.Stdout, f)
		cmd.Stderr = io.MultiWriter(os.Stderr, f)
		log.Printf("[LLM] llama log: %s", logPath)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("[LLM] failed to launch brain: %v", err)
		return
	}
	registerEngineProc(cmd.Process)
	trackBrainProc(cmd.Process)
	setLiveBrainModel(filepath.Base(cfg.ModelPath))
	log.Printf("[LLM] launching brain (pid %d): %s :%d threads=%d — model load may take 1-5 min",
		cmd.Process.Pid, liveBrainModelNow(), cfg.BrainPort, llamaThreadCount(cfg))
	go func(pid int) {
		time.Sleep(2 * time.Second)
		spreadBrainCPU(pid, "start")
		time.Sleep(8 * time.Second)
		spreadBrainCPU(pid, "load")
	}(cmd.Process.Pid)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func brainLogPath() string {
	wd, err := os.Getwd()
	if err != nil {
		return "runtime" + string(filepath.Separator) + "llama-server.log"
	}
	dir := filepath.Join(wd, "runtime")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "llama-server.log")
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

// brainHasVision reports whether the running llama-server loaded an mmproj
// (modalities.vision == true on /props). Text chat works either way; image_url
// requires true — otherwise the server returns HTTP 500.
func brainHasVision(port int) bool {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/props", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	var props struct {
		Modalities struct {
			Vision bool `json:"vision"`
		} `json:"modalities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&props); err != nil {
		return false
	}
	return props.Modalities.Vision
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
		if pid := liveBrainPID(); pid > 0 {
			spreadBrainCPU(pid, "warmup-fail")
		}
		return
	}
	resp.Body.Close()
	log.Printf("[LLM] warmup done in %s — model resident in VRAM, first user request will be fast",
		time.Since(start).Round(time.Second))
	if pid := liveBrainPID(); pid > 0 {
		spreadBrainCPU(pid, "warmup")
	}
}

func llamaThreadCount(cfg Config) int {
	if cfg.Threads > 0 {
		return cfg.Threads
	}
	hw := probeHardware(false)
	n := hw.Summary.Cores
	if n <= 0 {
		n = hw.Summary.Threads
	}
	if n <= 0 {
		n = runtime.NumCPU()
	}
	if n < 2 {
		n = 2
	}
	return n
}

func llamaProcEnv(threads int) []string {
	return append(os.Environ(),
		"OMP_NUM_THREADS="+strconv.Itoa(threads),
		"OMP_PROC_BIND=false",
		"KMP_AFFINITY=disabled",
	)
}

func spreadBrainCPU(pid int, why string) {
	if pid <= 0 {
		return
	}
	n := spreadPIDTree(pid)
	log.Printf("[LLM] CPU groups (%s): pid %d, размазано %d нитей на оба Xeon", why, pid, n)
}

func liveBrainPID() int {
	engineProcMu.Lock()
	defer engineProcMu.Unlock()
	if brainProc == nil {
		return 0
	}
	return brainProc.Pid
}

// Sampling temperatures: lower = more deterministic. /chart uses a low temperature so
// the trading analysis sticks to numbers on the chart instead of inventing them.
// defaultTemp = официальный Qwen3.8 instruct (non-thinking).
const (
	defaultTemp = 0.7
	chartTemp   = 0.3
)

// applyChatSampling вешает официальные параметры Qwen3.8 instruct на обычный чат и
// зрение. Агент (низкая температура) сюда не ходит — presence_penalty 1.5 ломает tool-call.
func applyChatSampling(payload map[string]any, temperature float64) {
	cfg := curWebCfg()
	topP := cfg.LLMTopP
	if topP <= 0 {
		topP = 0.80
	}
	topK := cfg.LLMTopK
	if topK <= 0 {
		topK = 20
	}
	payload["top_p"] = topP
	payload["top_k"] = topK
	if temperature >= 0.5 {
		pen := cfg.LLMPresencePenalty
		if pen == 0 {
			pen = 1.5
		}
		payload["presence_penalty"] = pen
	}
}

// applyGenLimit ставит жёсткий потолок на длину одного хода. llama-server без max_tokens
// генерирует до EOS или конца контекста; Gemma 4 в tool-режиме EOS выдаёт нестабильно и
// ход уходил в 5000+ токенов (галлюцинации, зацикливание, раздувание контекста до 47K),
// пока движок не отменял задачу. Потолок обрезает такой ход до осмысленного размера:
// агент получает текст + нудж, чат — ответ, а не бесконечный монолог.
func applyGenLimit(payload map[string]any) {
	n := curWebCfg().LLMMaxTokens
	if n <= 0 {
		n = 2048
	}
	payload["max_tokens"] = n
}

func applyToolSampling(payload map[string]any) {
	cfg := curWebCfg()
	topK := cfg.LLMTopK
	if topK <= 0 {
		topK = 20
	}
	payload["top_k"] = topK
	payload["top_p"] = 0.95
}

// coerceChatMessages makes the message list legal for Qwen3 Jinja templates that allow
// at most ONE role=system message and only as messages[0]. Extra system blocks (memory,
// dispatcher context, …) are merged into the leading system; later non-system roles keep order.
func coerceChatMessages(msgs []map[string]any) []map[string]any {
	if len(msgs) == 0 {
		return msgs
	}
	var sysParts []string
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		role, _ := m["role"].(string)
		if role == "system" {
			if c := strings.TrimSpace(msgContentString(m["content"])); c != "" {
				sysParts = append(sysParts, c)
			}
			continue
		}
		out = append(out, m)
	}
	if len(sysParts) == 0 {
		return out
	}
	sys := map[string]any{"role": "system", "content": strings.Join(sysParts, "\n\n")}
	return append([]map[string]any{sys}, out...)
}

// llmChat sends the conversation to llama-server's OpenAI-compatible endpoint and
// returns the assistant reply text. Messages are generic maps so content can be a
// plain string (text) or an array of parts (text + image_url) for vision.
func llmChat(port int, messages []map[string]any, temperature float64) (string, error) {
	messages = coerceChatMessages(messages)
	payload := map[string]any{
		"messages":     messages,
		"temperature":  temperature,
		"stream":       false,
		"cache_prompt": true,
	}
	applyChatSampling(payload, temperature)
	applyGenLimit(payload)
	body, _ := json.Marshal(payload)
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
	messages = coerceChatMessages(messages)
	payload := map[string]any{
		"messages":          messages,
		"temperature":       temperature,
		"stream":            true,
		"cache_prompt":      true,
		"timings_per_token": true, // ask llama.cpp to include its timings in the stream
	}
	applyChatSampling(payload, temperature)
	applyGenLimit(payload)
	body, _ := json.Marshal(payload)
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
//
// cache_prompt is always false here: tool loops rewrite the message list every step
// (assistant tool_calls + role:tool results). llama-server prompt-cache checkpoints then
// invalidate ("erased invalidated context checkpoint") and can surface as flaky 400s /
// broken generations. Normal streaming chat keeps cache_prompt=true.
// ctx is the task context: /stop must abort an in-flight GENERATION too, not just the gap
// between steps — a Qwen turn can run for a minute, and "остановлено" a minute later is not
// stopping (same reasoning as the child-process contexts in §4.2).
func llmChatTools(ctx context.Context, port int, messages []map[string]any, tools any, temperature float64) (assistantMsg, turnStats, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	messages = coerceChatMessages(messages)
	payload := map[string]any{
		"messages":     messages,
		"temperature":  temperature,
		"stream":       false,
		"cache_prompt": false,
	}
	if tools != nil {
		payload["tools"] = tools
		payload["tool_choice"] = "auto"
	}
	applyToolSampling(payload)
	applyGenLimit(payload)
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port), bytes.NewReader(body))
	if err != nil {
		return assistantMsg{}, turnStats{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	c := &http.Client{Timeout: 180 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return assistantMsg{}, turnStats{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return assistantMsg{}, turnStats{}, fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var parsed struct {
		Choices []struct {
			Message assistantMsg `json:"message"`
		} `json:"choices"`
		// llama.cpp includes per-request timings in non-streaming replies too. This is the
		// number ТЗ §3.2 tells us to watch: prompt_ms of the FIRST executor turn after the
		// dispatcher, i.e. the real cost of re-processing the system prompt once per task.
		Timings struct {
			PromptN            int     `json:"prompt_n"`
			PromptMs           float64 `json:"prompt_ms"`
			PromptPerSecond    float64 `json:"prompt_per_second"`
			PredictN           int     `json:"predicted_n"`
			PredictedMs        float64 `json:"predicted_ms"`
			PredictedPerSecond float64 `json:"predicted_per_second"`
		} `json:"timings"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return assistantMsg{}, turnStats{}, fmt.Errorf("LLM bad JSON: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return assistantMsg{}, turnStats{}, fmt.Errorf("LLM empty response")
	}
	msg := parsed.Choices[0].Message
	normalizeToolCalls(&msg)
	st := turnStats{
		PromptTokens: parsed.Timings.PromptN,
		PromptMs:     parsed.Timings.PromptMs,
		GenTokens:    parsed.Timings.PredictN,
		GenMs:        parsed.Timings.PredictedMs,
	}
	// Ход агента — это тоже генерация, и раньше о ней панель не знала НИЧЕГО: llmChatTools не
	// трогал live, а агентские пути звали live.finish(GenStats{}) с нулями. Отсюда и «цифры
	// стоят статично»: во время работы агента показывать было просто нечего.
	live.turnDone(GenStats{
		PromptTokens: parsed.Timings.PromptN,
		PromptPerSec: parsed.Timings.PromptPerSecond,
		GenTokens:    parsed.Timings.PredictN,
		GenPerSec:    parsed.Timings.PredictedPerSecond,
		TotalMs:      parsed.Timings.PromptMs + parsed.Timings.PredictedMs,
	})
	return msg, st, nil
}

// turnStats is what one tool-loop turn cost. Deliberately a handful of fields, not twenty
// (§14: telemetry for a single user on a single PC is a test bench for its own sake) — these
// answer «где тормозит» together with dispatcher_ms and total_ms in tasks.jsonl.
type turnStats struct {
	PromptTokens int
	PromptMs     float64
	GenTokens    int
	GenMs        float64
}

// normalizeToolCalls fills missing tool_call ids/args so chat templates that require
// tool_call_id never see undefined values (llama.cpp jinja: "not in tool_ids_seen").
func normalizeToolCalls(m *assistantMsg) {
	if m == nil {
		return
	}
	for i := range m.ToolCalls {
		if strings.TrimSpace(m.ToolCalls[i].ID) == "" {
			m.ToolCalls[i].ID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), i)
		}
		if strings.TrimSpace(m.ToolCalls[i].Type) == "" {
			m.ToolCalls[i].Type = "function"
		}
		if strings.TrimSpace(m.ToolCalls[i].Function.Arguments) == "" {
			m.ToolCalls[i].Function.Arguments = "{}"
		}
	}
}
