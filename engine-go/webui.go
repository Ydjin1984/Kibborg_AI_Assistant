package main

// Local web UI: a single-page dashboard served from the Go binary on 127.0.0.1 (loopback
// only — no auth needed because nothing is exposed to the network). Practical scope:
// chat with the local LLM (streamed), the deterministic /analyze report, and live status
// of the whole stack (brain, whisper, Chrome, history).

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"kibborg/engine/browser"
)

//go:embed web/index.html
var webIndexHTML []byte

// webChatID keys the web UI's dialog in the same history store the Telegram bot uses.
// Negative → can never collide with a real Telegram chat id.
const webChatID int64 = -777

// webChartPending mirrors Telegram's /chart → next image flow for the web UI.
var (
	webChartMu      sync.Mutex
	webChartPending string // empty = not pending; non-empty may hold extra context
	webChartOn      bool
)

var startedAt = time.Now()

// webCfg is the config the stateless handlers (registered without a cfg closure) read.
// Set once in newWebMux before the server starts serving.
var webCfg Config

// newWebMux wires the dashboard routes (extracted so tests can drive it via httptest).
func newWebMux(cfg Config) *http.ServeMux {
	webCfg = cfg
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(webIndexHTML)
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) { handleAPIStatus(w, cfg) })
	// Mutating / expensive endpoints are CSRF-guarded: 127.0.0.1 is reachable by ANY page in
	// the user's browser, so without an Origin/Host check a malicious site could drive the
	// browser agent against the user's logged-in Chrome, trigger downloads, or wipe history.
	mux.HandleFunc("/api/chat", sameOriginGuard(func(w http.ResponseWriter, r *http.Request) { handleAPIChat(w, r, cfg) }))
	mux.HandleFunc("/api/analyze", sameOriginGuard(handleAPIAnalyze))
	mux.HandleFunc("/api/reset", sameOriginGuard(handleAPIReset))
	// Parity with Telegram: browser agent + video download + local file delivery.
	mux.HandleFunc("/api/browser", sameOriginGuard(func(w http.ResponseWriter, r *http.Request) { handleAPIBrowser(w, r, cfg) }))
	mux.HandleFunc("/api/download_video", sameOriginGuard(func(w http.ResponseWriter, r *http.Request) { handleAPIDownloadVideo(w, r, cfg) }))
	mux.HandleFunc("/api/files/", handleAPIFiles)
	// Agent controls (§9). These MUST stay behind sameOriginGuard: without it any page open
	// in the same Chrome the agent itself drives could flip the hands to `full` — the
	// cheapest possible bypass of the whole of chapter 6.
	mux.HandleFunc("/api/hands", sameOriginGuard(handleAPIHands))
	mux.HandleFunc("/api/stop", sameOriginGuard(handleAPIStop))
	mux.HandleFunc("/api/confirm", sameOriginGuard(func(w http.ResponseWriter, r *http.Request) { handleAPIConfirm(w, r, cfg) }))
	mux.HandleFunc("/api/compact", sameOriginGuard(func(w http.ResponseWriter, r *http.Request) { handleAPICompact(w, r, cfg) }))
	return mux
}

// handleAPICompact сжимает контекст веб-чата — веб-близнец /compact (§7: паритет каналов).
func handleAPICompact(w http.ResponseWriter, r *http.Request, cfg Config) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	live.begin("сжатие контекста")
	text := handleCompactCommand(cfg, webChatID)
	live.finish(GenStats{})
	writeJSON(w, map[string]any{"text": text, "context": contextSnapshot(cfg, webChatID)})
}

// handleAPIHands reads (GET) or flips (POST {"mode":"full"|"safe"}) the hands switch.
func handleAPIHands(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "нужно поле mode", http.StatusBadRequest)
			return
		}
		setHandsMode(req.Mode, "web")
	}
	mode := currentHandsMode()
	writeJSON(w, map[string]any{
		"mode":  mode,
		"full":  mode == handsModeFull,
		"label": handsModeLabel(mode),
	})
}

// handleAPIStop cancels the web chat's running task — the Web twin of /stop (§9).
func handleAPIStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	taskID, ok := stopActiveTask(webChatID)
	if !ok {
		writeJSON(w, map[string]any{"stopped": false, "text": "Нечего останавливать — активной задачи нет."})
		return
	}
	writeJSON(w, map[string]any{"stopped": true, "task_id": taskID, "text": "⏹ Остановлено."})
}

// handleAPIConfirm answers a pending confirmation from the Web UI buttons.
func handleAPIConfirm(w http.ResponseWriter, r *http.Request, cfg Config) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Approve bool `json:"approve"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "нужно поле approve", http.StatusBadRequest)
		return
	}
	rs := takePending(webChatID)
	if rs == nil {
		writeJSON(w, map[string]any{"error": "Нечего подтверждать — вопрос уже снят."})
		return
	}
	emit := ndjsonEmitter(w)
	live.begin("agent-confirm")
	res := resumeConfirmed(rs, req.Approve, func(s string) { emit(map[string]any{"status": s}) })
	live.finish(GenStats{})
	emitAgentResult(emit, rs.task.Input, res, "")
}

// sameOriginGuard blocks cross-site requests to local API endpoints (CSRF) and DNS-rebinding.
// It requires the request Host to be loopback AND, when the browser sends an Origin/Referer,
// that its host matches. Cross-site POSTs always carry an Origin (so they're rejected), and a
// rebinding attack uses a non-loopback Host (also rejected). Same-origin dashboard calls pass.
func sameOriginGuard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			http.Error(w, "cross-origin request blocked", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

func sameOrigin(r *http.Request) bool {
	if !isLoopbackHost(r.Host) {
		return false // blocks DNS-rebinding (attacker.com → 127.0.0.1 gives Host=attacker.com)
	}
	// When present, Origin/Referer host must equal the dashboard's own host.
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		return err == nil && u.Host == r.Host
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		return err == nil && u.Host == r.Host
	}
	return true // no Origin/Referer (non-browser client / same-origin GET) + loopback Host → allow
}

// isLoopbackHost reports whether a Host header (host[:port]) points at the local machine.
func isLoopbackHost(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	h = strings.Trim(h, "[]")
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}

// startWebUI serves the dashboard. Runs in its own goroutine; failures only disable the UI.
func startWebUI(cfg Config) {
	if cfg.WebPort <= 0 {
		return
	}
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.WebPort)
	log.Printf("[WEB] интерфейс запущен: http://%s", addr)
	if err := http.ListenAndServe(addr, newWebMux(cfg)); err != nil {
		log.Printf("[WEB] сервер не поднялся: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// webChatReply emits a single NDJSON done-line for a deterministic chat command (trader
// commands), matching the shape the chat UI expects from /api/chat.
func webChatReply(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "text": text})
}

// handleAPIStatus reports the health of every part of the stack in one call.
func handleAPIStatus(w http.ResponseWriter, cfg Config) {
	histMu.Lock()
	chats := len(history)
	webTurns := len(history[webChatID]) / 2
	histMu.Unlock()

	whisper := voiceBackendStatus(cfg)
	chromeTabs := -1
	if tabs, err := getBrowserSession().ListTabs(); err == nil {
		chromeTabs = len(tabs)
	}
	memInfo := map[string]any{"enabled": mem != nil, "vectors": embedEnabled(cfg)}
	if mem != nil {
		if n, err := mem.CountEpisodes(webChatID); err == nil {
			memInfo["web_episodes"] = n
		}
	}
	ready := brainReady(cfg.BrainPort)
	writeJSON(w, map[string]any{
		"brain_ready":   ready,
		"brain_vision":  ready && brainHasVision(cfg.BrainPort), // true only when mmproj loaded
		"brain_port":    cfg.BrainPort,
		"model":         filepathBase(cfg.ModelPath),
		"whisper":       whisper,
		"voice_backend": voiceBackendLabel(cfg),
		"typewhisper":   typeWhisperReady(cfg),
		"whisper_cpp":   whisperCppReady(cfg),
		"chrome_tabs":   chromeTabs,
		"chats":         chats,
		"web_turns":     webTurns,
		"memory":        memInfo,
		"uptime_sec":    int(time.Since(startedAt).Seconds()),
		"activity":      live.snapshot(),
		// Окно контекста: сколько занято сейчас и сколько всего. Раньше этих чисел не было
		// нигде, и «сколько ещё влезет» пользователь мог только угадывать.
		"context": contextSnapshot(cfg, webChatID),
		"hands":   map[string]any{"mode": currentHandsMode(), "short": handsModeShort(currentHandsMode())},
	})
}

// statsJSON is the per-reply speed block the chat tab shows under a finished answer.
func statsJSON(s GenStats) map[string]any {
	if s.GenTokens == 0 {
		return nil
	}
	return map[string]any{
		"gen_tok_s":     round1(s.GenPerSec),
		"prompt_tok_s":  round1(s.PromptPerSec),
		"gen_tokens":    s.GenTokens,
		"prompt_tokens": s.PromptTokens,
		"total_sec":     round1(s.TotalMs / 1000),
	}
}

// handleAPIChat streams the LLM reply as NDJSON lines: {"text": <preview so far>} …
// then a final {"done": true, "text": <full answer>}. Accepts either a JSON body
// {"message": …} or multipart/form-data with an optional image/text file attachment
// (images → vision model, text/code → embedded in the prompt), mirroring the Telegram bot.
//
// Special routes (parity with Telegram slash-commands):
//   - bare video URL or "/download <url>" → yt-dlp download
//   - "/browser <task>" → browser agent
//   - "/chart" on image → trading chart prompt
func handleAPIChat(w http.ResponseWriter, r *http.Request, cfg Config) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	// Peek at plain JSON early so /download and /browser work without the brain being ready
	// for download (brain only needed for browser agent / normal chat).
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		var peek struct {
			Message string `json:"message"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		r.Body = io.NopCloser(bytes.NewReader(body))
		_ = json.Unmarshal(body, &peek)
		if msg := strings.TrimSpace(peek.Message); msg != "" {
			if routeWebMessage(w, cfg, msg, "") {
				return
			}
		}
	}

	// Uploaded file with a security caption (/scan, /audit, /logs) → analyze bytes directly.
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if perr := r.ParseMultipartForm(25 << 20); perr == nil {
			if mode, ok := parseSecFileCaption(strings.TrimSpace(r.FormValue("message"))); ok {
				if file, hdr, ferr := r.FormFile("file"); ferr == nil {
					defer file.Close()
					data, _ := io.ReadAll(io.LimitReader(file, 20_000_000))
					webChatReply(w, handleSecFile(cfg, hdr.Filename, data, mode))
					return
				}
			}
			if file, hdr, ferr := r.FormFile("file"); ferr == nil {
				defer file.Close()
				caption := strings.TrimSpace(r.FormValue("message"))
				mime := hdr.Header.Get("Content-Type")
				switch classifyMedia(hdr.Filename, mime) {
				case mediaImage:
					// vision без tools → описание → диспетчер (§7). Parity with Telegram.
					data, _ := io.ReadAll(io.LimitReader(file, 20_000_000))
					handleWebImageTurn(w, cfg, caption, hdr.Filename, mime, data)
					return
				case mediaPDF:
					// PDF: текстовый слой или распознавание скана → текст → диспетчер (§23).
					handleWebPDFTurn(w, cfg, caption, hdr.Filename, file)
					return
				case mediaVideo:
					// Видео разбирается тем же слоем, что и в Telegram: речь+кадры → текст →
					// диспетчер. Файл стримится на диск, а не читается в память: ролик может
					// весить гигабайт, и io.ReadAll на нём — это OOM, а не загрузка.
					handleWebVideoTurn(w, cfg, caption, hdr.Filename, mime, file)
					return
				case mediaAudio:
					// §7: голос и текст равноправны. This used to fall through to the plain
					// streaming chat, where the agent has NO tools — the reason a spoken
					// «у тебя же есть консоль, сделай сам» got «я работаю в изолированной среде».
					if !voiceEnabled(cfg) {
						webChatReply(w, voiceSetupHelp())
						return
					}
					data, _ := io.ReadAll(io.LimitReader(file, 20_000_000))
					transcript, terr := transcribeAudio(cfg, data, audioExt(hdr.Filename, mime))
					if terr != nil {
						webChatReply(w, "❌ Не смог распознать речь: "+terr.Error())
						return
					}
					task := strings.TrimSpace(caption + "\n" + transcript)
					log.Printf("[WEB] голос: %q → агент", capAgentText(transcript, 120))
					if !routeWebMessage(w, cfg, task, transcript) {
						webChatReply(w, "🎙 Распознал: «"+transcript+"», но не понял, что сделать. Повтори конкретнее.")
					}
					return
				}
			}
		}
	}

	_, msgs, histNote, err := buildWebChatRequest(r, cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !brainReady(cfg.BrainPort) {
		writeJSON(w, map[string]any{"error": "Модель ещё грузится (1-5 минут после старта). Попробуй чуть позже."})
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)

	lastSent := time.Now()
	live.begin("веб-чат")
	reply, stats, err := llmChatStream(cfg.BrainPort, msgs, defaultTemp, func(full string) {
		if time.Since(lastSent) < 300*time.Millisecond {
			return
		}
		lastSent = time.Now()
		if preview := streamPreview(full); preview != "" {
			_ = enc.Encode(map[string]any{"text": preview})
			if flusher != nil {
				flusher.Flush()
			}
		}
	})
	live.finish(stats)
	if err != nil {
		_ = enc.Encode(map[string]any{"error": err.Error()})
		return
	}
	// History records a compact note (e.g. "[изображение] …") instead of raw file bytes.
	recordHistory(webChatID, histNote, reply)
	done := map[string]any{
		"done":  true,
		"text":  stripThink(reply),
		"stats": statsJSON(stats),
	}
	// Surface STT transcript to the web UI (parity with Telegram "🎙 Распознал: …").
	if strings.HasPrefix(histNote, "🎙 ") {
		done["transcript"] = strings.TrimSpace(strings.TrimPrefix(histNote, "🎙 "))
	}
	_ = enc.Encode(done)
	log.Printf("[WEB] chat reply (%d chars, %.1f tok/s)", len(reply), stats.GenPerSec)
}

// routeWebMessage is THE single entry for any Web message that has become text — typed, or
// spoken and transcribed. It returns true when the message is fully handled.
//
// It exists because voice used to bypass it. Typed text went to the layered agent while a
// voice.webm upload fell through to the plain streaming chat, which has no tools at all — so
// the model correctly, and uselessly, answered «у меня нет доступа к терминалу… я работаю в
// изолированной среде». No hands switch could help: in that code path there were no hands.
// ТЗ §7 says text and voice are equal citizens; AGENTS.md says channel parity. This is it.
func routeWebMessage(w http.ResponseWriter, cfg Config, msg, transcript string) bool {
	// Answer to a pending confirmation — works for «да» said out loud too.
	if p := peekPending(webChatID); p != nil {
		if yes, ok := confirmWord(msg); ok {
			rs := takePending(webChatID)
			if rs == nil {
				webChatReply(w, "Нечего подтверждать — вопрос уже снят.")
				return true
			}
			emit := ndjsonEmitter(w)
			live.begin("agent-confirm")
			res := resumeConfirmed(rs, yes, func(s string) { emit(map[string]any{"status": s}) })
			live.finish(GenStats{})
			emitAgentResult(emit, rs.task.Input, res, transcript)
			return true
		}
	}
	if isDL, url := parseCommand(msg, downloadCommands); isDL || browser.IsVideoURL(msg) ||
		(browser.ExtractVideoURL(msg) != "" && looksLikeDownloadRequest(msg)) {
		if url == "" {
			url = browser.ExtractVideoURL(msg)
		}
		if url == "" {
			url = msg
		}
		if extracted := browser.ExtractVideoURL(url); extracted != "" {
			url = extracted
		}
		handleWebVideoDownload(w, cfg, url)
		return true
	}
	if isBr, task := parseBrowserCommand(msg); isBr {
		handleWebBrowser(w, cfg, task, transcript)
		return true
	}
	if is, _ := parseCommand(msg, compactCommands); is {
		live.begin("сжатие контекста")
		text := handleCompactCommand(cfg, webChatID)
		live.finish(GenStats{})
		webChatReply(w, text)
		return true
	}
	// Trader commands (deterministic, no brain needed) — parity with Telegram.
	if is, arg := parseCommand(msg, sizeCommands); is {
		webChatReply(w, handleSizeCommand(cfg, arg))
		return true
	}
	if is, arg := parseCommand(msg, logCommands); is {
		webChatReply(w, handleLogCommand(cfg, webChatID, arg))
		return true
	}
	if is, arg := parseCommand(msg, journalCommands); is {
		webChatReply(w, handleJournalCommand(webChatID, arg))
		return true
	}
	if is, arg := parseCommand(msg, closeCommands); is {
		webChatReply(w, handleCloseCommand(arg))
		return true
	}
	// Security & log analysis (deterministic + grounded LLM read) — parity with Telegram.
	if is, arg := parseCommand(msg, logsCommands); is {
		webChatReply(w, handleLogsCommand(cfg, arg))
		return true
	}
	if is, arg := parseCommand(msg, scanCommands); is {
		webChatReply(w, handleScanCommand(cfg, arg))
		return true
	}
	// Parity with Telegram: standalone /chart arms the next image upload.
	if isChart, extra := parseCommand(msg, chartCommands); isChart {
		webChartMu.Lock()
		webChartOn = true
		webChartPending = extra
		webChartMu.Unlock()
		webChatReply(w, "📈 Пришлите скриншот или файл графика (скрепка / drag-and-drop) — проанализирую и дам торговый разбор.")
		return true
	}
	if strings.HasPrefix(strings.ToLower(msg), "/reset") {
		webChartMu.Lock()
		webChartOn = false
		webChartPending = ""
		webChartMu.Unlock()
		return false // let the normal reset path answer
	}
	if wantsToolAgent(msg) {
		// Free text — typed or spoken — ALWAYS goes to the layered agent.
		handleWebBrowser(w, cfg, msg, transcript)
		return true
	}
	return false
}

// ndjsonEmitter returns a writer for NDJSON progress lines, flushing after each.
func ndjsonEmitter(w http.ResponseWriter) func(map[string]any) {
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	var mu sync.Mutex
	return func(payload map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		_ = enc.Encode(payload)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// handleWebImageTurn is the Web twin of the Telegram image path (§7): vision describes the
// picture WITHOUT tools, and the description — not the pixels — is what the dispatcher routes.
// /chart additionally resolves the ticker and takes its numbers from Binance.
func handleWebImageTurn(w http.ResponseWriter, cfg Config, message, name, mime string, data []byte) {
	if err := requireVisionOK(cfg); err != nil {
		webChatReply(w, "❌ "+err.Error())
		return
	}
	// Consume a pending /chart armed by a previous web message (parity with Telegram).
	webChartMu.Lock()
	pendExtra, pending := webChartPending, webChartOn
	webChartOn = false
	webChartPending = ""
	webChartMu.Unlock()

	chartReq, extra := parseChartCommand(message)
	if chartReq {
		if pending && pendExtra != "" {
			extra = strings.TrimSpace(pendExtra + " " + extra)
		}
	} else if pending {
		chartReq = true
		extra = strings.TrimSpace(pendExtra + " " + message)
	}

	visionPrompt := "Опиши это изображение по делу, без выдумок: что на нём и какие детали важны."
	if chartReq {
		visionPrompt = chartVisionPrompt
	}
	desc, err := describeImageBytes(cfg, webChatID, visionPrompt, chartVisionSystemPrompt,
		imageUploadMime(name, mime), data)
	if err != nil {
		webChatReply(w, "❌ "+err.Error())
		return
	}

	if chartReq {
		symbol := extractChartTicker(extra, desc)
		if symbol == "" {
			webChartMu.Lock()
			webChartOn, webChartPending = true, strings.TrimSpace(extra+"\n"+desc)
			webChartMu.Unlock()
			webChatReply(w, "📈 Разобрал картинку, но тикер по ней не определился:\n\n"+
				capAgentText(desc, 1200)+
				"\n\n❓ Назови инструмент (например `BTC` или `ETHUSDT`) — возьму данные с биржи. "+
				"Цифры со скриншота я как данные не использую.")
			return
		}
		streamWebAgent(w, cfg, chartTaskText(symbol, extra, desc), "[график] "+symbol, []string{packTrade}, "")
		return
	}

	if strings.TrimSpace(message) == "" {
		recordHistory(webChatID, "[изображение]", desc)
		webChatReply(w, desc)
		return
	}
	streamWebAgent(w, cfg, message+"\n\n[Описание приложенной картинки, полученное зрением]\n"+desc,
		"[изображение] "+message, nil, "")
}

// streamWebAgent runs the layered agent for the Web channel and emits NDJSON status lines.
func streamWebAgent(w http.ResponseWriter, cfg Config, task, historyNote string, hintPacks []string, transcript string) {
	emit := ndjsonEmitter(w)
	live.begin("agent")
	res := runLayeredAgent(agentRequest{
		cfg:        cfg,
		actor:      actorFor(channelWeb, webChatID, true),
		baseMsgs:   withMemory(cfg, webChatID, task, baseMessages(webChatID)),
		input:      task,
		status:     func(s string) { emit(map[string]any{"status": s}) },
		memSummary: memorySummaryFor(webChatID),
		hintPacks:  hintPacks,
	})
	live.finish(GenStats{})
	emitAgentResult(emit, historyNote, res, transcript)
	maybeAutoCompact(cfg, webChatID, nil)
}

// buildWebChatRequest parses a chat request (JSON or multipart) into the LLM message list.
// Returns the user text, the full message slice, a compact history note, and any error.
func buildWebChatRequest(r *http.Request, cfg Config) (text string, msgs []map[string]any, histNote string, err error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err = r.ParseMultipartForm(25 << 20); err != nil {
			return "", nil, "", fmt.Errorf("не разобрал форму: %w", err)
		}
		text = strings.TrimSpace(r.FormValue("message"))
		file, hdr, ferr := r.FormFile("file")
		if ferr != nil {
			// multipart without a file → treat as plain text
			if text == "" {
				return "", nil, "", fmt.Errorf("нужно сообщение или файл")
			}
			base := withMemory(cfg, webChatID, text, baseMessages(webChatID))
			return text, append(base, map[string]any{"role": "user", "content": text}), text, nil
		}
		defer file.Close()
		data, rerr := io.ReadAll(io.LimitReader(file, 20_000_000))
		if rerr != nil {
			return "", nil, "", fmt.Errorf("не прочитал файл: %w", rerr)
		}
		name := hdr.Filename
		mime := hdr.Header.Get("Content-Type")
		return buildAttachmentMessages(cfg, text, name, mime, data)
	}
	// JSON: {"message": "..."}
	var req struct {
		Message string `json:"message"`
	}
	if derr := json.NewDecoder(r.Body).Decode(&req); derr != nil || strings.TrimSpace(req.Message) == "" {
		return "", nil, "", fmt.Errorf("нужно поле message")
	}
	text = strings.TrimSpace(req.Message)
	base := withMemory(cfg, webChatID, text, baseMessages(webChatID))
	return text, append(base, map[string]any{"role": "user", "content": text}), text, nil
}

// buildAttachmentMessages turns an uploaded file into a vision, audio transcript, or text-embedded user message.
// Routing is driven by classifyMedia: text chat never gets image_url (vision idle);
// only mediaImage attaches vision; audio → STT; video is rejected with a clear hint.
func buildAttachmentMessages(cfg Config, text, name, mime string, data []byte) (string, []map[string]any, string, error) {
	switch classifyMedia(name, mime) {
	case mediaAudio:
		// Voice / audio: STT → normal text chat (no vision).
		if !voiceEnabled(cfg) {
			return "", nil, "", fmt.Errorf("%s", voiceSetupHelp())
		}
		ext := audioExt(name, mime)
		transcript, err := transcribeAudio(cfg, data, ext)
		if err != nil {
			return "", nil, "", fmt.Errorf("не смог распознать речь: %w", err)
		}
		prompt := text
		if prompt == "" {
			prompt = transcript
		} else {
			prompt = strings.TrimSpace(prompt + "\n\n" + transcript)
		}
		histNote := "🎙 " + transcript
		base := withMemory(cfg, webChatID, prompt, baseMessages(webChatID))
		return prompt, append(base, map[string]any{"role": "user", "content": prompt}), histNote, nil

	case mediaImage:
		// Images are intercepted earlier by handleWebImageTurn (vision runs WITHOUT tools and
		// its DESCRIPTION is what gets routed, §7). Reaching here means the interception was
		// skipped, so say it plainly instead of quietly re-attaching image_url to a tool turn.
		return "", nil, "", fmt.Errorf("картинка обрабатывается отдельным путём (зрение без инструментов) — обнови страницу и повтори")

	case mediaVideo:
		// Видео перехватывается раньше — handleWebVideoTurn (разбор без инструментов, §21).
		// Сюда можно попасть только если перехват не сработал, и честнее сказать это прямо,
		// чем тихо подсунуть модели пустое сообщение.
		return "", nil, "", fmt.Errorf("видео обрабатывается отдельным путём (речь и кадры → текст) — обнови страницу и повтори")

	case mediaTextFile:
		if !utf8.Valid(data) || bytesHaveNUL(data) {
			return "", nil, "", fmt.Errorf("файл выглядит бинарным, а не текстом")
		}
		prompt := text
		if prompt == "" {
			prompt = "Разбери этот файл: что он делает, есть ли проблемы и что можно улучшить."
		}
		content := string(data)
		if len(content) > maxFileChars {
			cut := maxFileChars
			for cut > 0 && !utf8.RuneStart(content[cut]) {
				cut--
			}
			content = content[:cut]
		}
		lang := codeLang[strings.ToLower(filepathExt(name))]
		full := prompt + "\n\nФайл `" + name + "`:\n```" + lang + "\n" + content + "\n```"
		fileBase := withMemory(cfg, webChatID, prompt, baseMessages(webChatID))
		return prompt, append(fileBase, map[string]any{"role": "user", "content": full}),
			"[файл " + name + "] " + prompt, nil

	default:
		return "", nil, "", fmt.Errorf("не читаю этот формат: картинки (png/jpg/webp) → зрение; голос (webm/ogg/mp3/wav) → STT; текст/код (.go .py .md .txt .json …)")
	}
}

func filepathExt(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i:]
	}
	return ""
}

// handleAPIAnalyze streams NDJSON: first the deterministic report (immediately), then the
// LLM's narration of it token by token, then a final done+stats line. The engine's numbers
// are the source of truth; the LLM only interprets them.
func handleAPIAnalyze(w http.ResponseWriter, r *http.Request) {
	symbol := strings.TrimSpace(r.URL.Query().Get("symbol"))
	if symbol == "" {
		http.Error(w, "нужен параметр symbol", http.StatusBadRequest)
		return
	}
	cfg := webCfg
	report, err := analyzeTicker(symbol)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	// 1) deterministic report + position sizing — instant.
	_ = enc.Encode(map[string]any{"text": renderReport(report) + sizingBlock(cfg, report), "report": report})
	if flusher != nil {
		flusher.Flush()
	}
	// 2) LLM narration — only if the brain is up; otherwise the report alone is still useful.
	if !brainReady(cfg.BrainPort) {
		_ = enc.Encode(map[string]any{"done": true, "narration": ""})
		return
	}
	lastSent := time.Now()
	narration, stats, nerr := narrateReport(cfg, report, "разбор "+report.Symbol, func(full string) {
		if time.Since(lastSent) < 300*time.Millisecond {
			return
		}
		lastSent = time.Now()
		if preview := streamPreview(full); preview != "" {
			_ = enc.Encode(map[string]any{"narration": preview})
			if flusher != nil {
				flusher.Flush()
			}
		}
	})
	if nerr != nil {
		_ = enc.Encode(map[string]any{"done": true, "narration": "", "error": nerr.Error()})
		return
	}
	_ = enc.Encode(map[string]any{"done": true, "narration": stripThink(narration), "stats": statsJSON(stats)})
	log.Printf("[WEB] analyze %s narrated (%.1f tok/s)", report.Symbol, stats.GenPerSec)
}

// handleAPIReset clears the web chat's dialog context.
func handleAPIReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	// Общее тело со сбросом в Telegram (§7 — паритет): история, память, ссылки задачи и
	// ЗАМЕР занятого контекста. Раньше здесь была своя копия этой логики без обнуления
	// счётчика, и после «Сброса» панель продолжала показывать занятость стёртого диалога.
	resetChatContext(webChatID)
	webChartMu.Lock()
	webChartOn = false
	webChartPending = ""
	webChartMu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "context": contextSnapshot(webCfg, webChatID)})
}

// handleAPIBrowser runs the same browser agent as Telegram /browser.
func handleAPIBrowser(w http.ResponseWriter, r *http.Request, cfg Config) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Task string `json:"task"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Task) == "" {
		http.Error(w, "нужно поле task", http.StatusBadRequest)
		return
	}
	handleWebBrowser(w, cfg, strings.TrimSpace(req.Task), "")
}

func handleWebBrowser(w http.ResponseWriter, cfg Config, task, transcript string) {
	if task == "" {
		writeJSON(w, map[string]any{"error": "Напиши задачу: /agent <что сделать> (или /browser)."})
		return
	}
	if !brainReady(cfg.BrainPort) {
		writeJSON(w, map[string]any{"error": "Модель ещё грузится. Повтори запрос чуть позже."})
		return
	}
	// NDJSON so the UI can show live step statuses, then the final answer (the agent loop has
	// no token stream — statuses are what the user sees, never CoT §6).
	emit := ndjsonEmitter(w)
	live.begin("agent")
	emit(map[string]any{"status": "🧭 Разбираю запрос…"})
	// Include web chat history + memory so follow-ups work (same as Telegram).
	actor := actorFor(channelWeb, webChatID, true) // loopback + sameOriginGuard = owner
	base := withMemory(cfg, webChatID, task, baseMessages(webChatID))
	res := runAgent(cfg, actor, base, readFollowUpHint(webChatID, task), func(s string) {
		emit(map[string]any{"status": s})
	})
	live.finish(GenStats{})
	emitAgentResult(emit, task, res, transcript)
	maybeAutoCompact(cfg, webChatID, nil)
	log.Printf("[WEB] %s → %s (%d chars, %d artifacts)", res.TaskID, res.Status, len(res.Text), len(res.Artifacts))
}

// emitAgentResult writes the terminal NDJSON line and records history for completed tasks
// only — interrupted attempts must not reach long-term memory (§4.1).
func emitAgentResult(emit func(map[string]any), userText string, res agentResult, transcript string) {
	text := res.Text
	if len(res.Notices) > 0 {
		text = strings.Join(res.Notices, "\n") + "\n\n" + text
	}
	if !res.interrupted() {
		recordHistory(webChatID, userText, res.Text)
	}
	payload := map[string]any{
		"done":      true,
		"text":      text,
		"artifacts": res.Artifacts,
		"files":     webArtifactLinks(res.Artifacts),
		"task_id":   res.TaskID,
		"status":    string(res.Status),
		"plan":      res.Plan,
		"waiting":   res.Waiting,
	}
	if transcript != "" {
		payload["transcript"] = transcript // UI shows «🎙 Распознал: …», parity with Telegram
	}
	emit(payload)
}

// handleAPIDownloadVideo is the dedicated endpoint (also reachable via chat message).
func handleAPIDownloadVideo(w http.ResponseWriter, r *http.Request, cfg Config) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.URL) == "" {
		http.Error(w, "нужно поле url", http.StatusBadRequest)
		return
	}
	handleWebVideoDownload(w, cfg, strings.TrimSpace(req.URL))
}

func handleWebVideoDownload(w http.ResponseWriter, cfg Config, rawURL string) {
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	_ = enc.Encode(map[string]any{"text": "🎬 Скачиваю видео в максимальном доступном качестве…"})
	if flusher != nil {
		flusher.Flush()
	}
	// Direct download path (no agent loop) still gets the task-level ceiling (§4.2).
	ctx, cancel := context.WithTimeout(context.Background(), TaskTimeout)
	defer cancel()
	res, err := browser.FetchVideo(ctx, rawURL, cfg.FfmpegPath)
	if err != nil {
		_ = enc.Encode(map[string]any{"error": err.Error(), "done": true})
		return
	}
	link := webFileURL(res.Path)
	mb := float64(res.Bytes) / (1 << 20)
	title := res.Title
	if title == "" {
		title = filepathBase(res.Path)
	}
	text := fmt.Sprintf("✅ **%s**\n📦 %.1f МБ · `%s`\n📁 `%s`", title, mb, res.Ext, res.Path)
	if link != "" {
		text += "\n🔗 " + link
	}
	_ = enc.Encode(map[string]any{
		"done":  true,
		"text":  text,
		"path":  res.Path,
		"title": title,
		"bytes": res.Bytes,
		"files": []map[string]string{{"path": res.Path, "url": link, "name": filepathBase(res.Path)}},
	})
	log.Printf("[WEB] video %s → %s (%.1f MB)", rawURL, res.Path, mb)
}

// handleAPIFiles serves files only from runtime/browser (artifacts & videos) — loopback UI only.
func handleAPIFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/api/files/")
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || strings.Contains(rel, "..") {
		http.NotFound(w, r)
		return
	}
	// Only serve under runtime/browser (require separator after root — avoid browser vs browser_evil).
	full := filepath.Join("runtime", "browser", filepath.FromSlash(rel))
	abs, err := filepath.Abs(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	root, err := filepath.Abs(filepath.Join("runtime", "browser"))
	if err != nil || !pathUnderRoot(abs, root) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filepath.Base(abs)+"\"")
	http.ServeFile(w, r, abs)
}

// webFileURL maps a local artifact path to /api/files/... if it lives under runtime/browser.
func webFileURL(path string) string {
	path = filepath.Clean(path)
	// Accept both relative and absolute paths.
	markers := []string{
		filepath.Join("runtime", "browser") + string(filepath.Separator),
		"runtime/browser/",
		"runtime\\browser\\",
	}
	lower := path
	for _, m := range markers {
		if i := strings.Index(strings.ToLower(lower), strings.ToLower(m)); i >= 0 {
			rel := path[i+len(m):]
			rel = filepath.ToSlash(rel)
			return "/api/files/" + rel
		}
	}
	// Already relative like runtime/browser/videos/x.mp4
	if strings.HasPrefix(filepath.ToSlash(path), "runtime/browser/") {
		return "/api/files/" + strings.TrimPrefix(filepath.ToSlash(path), "runtime/browser/")
	}
	return ""
}

func webArtifactLinks(paths []string) []map[string]string {
	var out []map[string]string
	for _, p := range paths {
		out = append(out, map[string]string{
			"path": p,
			"url":  webFileURL(p),
			"name": filepathBase(p),
		})
	}
	return out
}

// pathUnderRoot reports whether abs is root or a path inside it (Windows-safe separator).
func pathUnderRoot(abs, root string) bool {
	abs = filepath.Clean(abs)
	root = filepath.Clean(root)
	if strings.EqualFold(abs, root) {
		return true
	}
	sep := string(os.PathSeparator)
	prefix := root + sep
	// Case-insensitive on Windows; EqualFold on the prefix region only.
	if len(abs) < len(prefix) {
		return false
	}
	return strings.EqualFold(abs[:len(prefix)], prefix)
}

func filepathBase(p string) string {
	if p == "" {
		return ""
	}
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
