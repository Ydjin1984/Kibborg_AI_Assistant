package main

// Voice messages: Telegram/Web audio → ffmpeg (→ 16kHz mono wav when needed) → STT.
// Primary: TypeWhisper HTTP API (/v1/transcribe on :8978 by default, auto-discover).
// Fallback: whisper.cpp server /inference (WHISPER_SERVER + WHISPER_MODEL).
// Without either backend, voice replies with setup instructions.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const voiceTmpDir = "runtime/voice"

// maxVoiceSeconds keeps CPU transcription snappy; longer clips are refused politely.
const maxVoiceSeconds = 600

// defaultTypeWhisperPort is TypeWhisper's documented local API port.
const defaultTypeWhisperPort = 8978

// voiceEnabled reports whether any speech-recognition backend is configured.
func voiceEnabled(cfg Config) bool {
	return typeWhisperWanted(cfg) || whisperCppEnabled(cfg)
}

// whisperCppEnabled is true when the classic whisper.cpp server is configured.
func whisperCppEnabled(cfg Config) bool {
	return cfg.WhisperExe != "" && cfg.WhisperModel != ""
}

// typeWhisperWanted is true unless the user explicitly disabled TypeWhisper (URL=off).
// Default is "try TypeWhisper" — the running app + API server supply the actual readiness.
func typeWhisperWanted(cfg Config) bool {
	u := strings.TrimSpace(strings.ToLower(cfg.TypeWhisperURL))
	return u != "off" && u != "false" && u != "0" && u != "disabled" && u != "нет"
}

// ensureWhisper launches whisper.cpp's server unless the port is already taken.
// Mirrors ensureBrain: check the listener, not an HTTP health endpoint.
// TypeWhisper is a separate desktop app — we never launch it from the bot.
func ensureWhisper(cfg Config) {
	if !whisperCppEnabled(cfg) {
		if typeWhisperWanted(cfg) {
			log.Printf("[VOICE] whisper.cpp fallback не настроен; основной STT = TypeWhisper")
		} else {
			log.Printf("[VOICE] STT выключен (TYPEWHISPER_URL=off и WHISPER_* пусты)")
		}
		return
	}
	if portInUse(cfg.WhisperPort) {
		log.Printf("[VOICE] whisper port :%d already in use — not launching a second instance", cfg.WhisperPort)
		return
	}
	if _, err := os.Stat(cfg.WhisperExe); err != nil {
		log.Printf("[VOICE] whisper-server not found: %s", cfg.WhisperExe)
		return
	}
	if _, err := os.Stat(cfg.WhisperModel); err != nil {
		log.Printf("[VOICE] whisper model not found: %s", cfg.WhisperModel)
		return
	}
	cmd := exec.Command(cfg.WhisperExe,
		"-m", cfg.WhisperModel,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(cfg.WhisperPort),
		"-l", "auto",
	)
	cmd.Dir = filepath.Dir(cfg.WhisperExe) // load its DLLs from the build dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Printf("[VOICE] failed to launch whisper-server: %v", err)
		return
	}
	registerEngineProc(cmd.Process)
	log.Printf("[VOICE] whisper-server launched (pid %d) :%d — %s",
		cmd.Process.Pid, cfg.WhisperPort, filepath.Base(cfg.WhisperModel))
}

// ffmpegExe resolves the ffmpeg binary: explicit FFMPEG setting wins, else PATH.
func ffmpegExe(cfg Config) string {
	if cfg.FfmpegPath != "" {
		return cfg.FfmpegPath
	}
	return "ffmpeg"
}

// typeWhisperEndpoint resolves base URL + token for the local TypeWhisper API.
// Order: explicit TYPEWHISPER_URL → api-discovery.json → default :8978.
func typeWhisperEndpoint(cfg Config) (baseURL, token string) {
	token = strings.TrimSpace(cfg.TypeWhisperToken)
	if u := strings.TrimSpace(cfg.TypeWhisperURL); u != "" &&
		!strings.EqualFold(u, "auto") && !strings.EqualFold(u, "default") {
		return strings.TrimRight(u, "/"), token
	}
	// Auto-discover: TypeWhisper-UserData (canonical) then legacy TypeWhisper install root.
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		for _, dir := range []string{
			filepath.Join(la, "TypeWhisper-UserData"),
			filepath.Join(la, "TypeWhisper"),
		} {
			if raw, err := os.ReadFile(filepath.Join(dir, "api-discovery.json")); err == nil {
				var d struct {
					Port  int    `json:"port"`
					Token string `json:"token"`
				}
				if json.Unmarshal(raw, &d) == nil && d.Port > 0 {
					if token == "" {
						token = strings.TrimSpace(d.Token)
					}
					return fmt.Sprintf("http://127.0.0.1:%d", d.Port), token
				}
			}
			// Legacy single-line port file
			if raw, err := os.ReadFile(filepath.Join(dir, "api-port")); err == nil {
				if p, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && p > 0 {
					return fmt.Sprintf("http://127.0.0.1:%d", p), token
				}
			}
		}
	}
	return fmt.Sprintf("http://127.0.0.1:%d", defaultTypeWhisperPort), token
}

// typeWhisperReady probes GET /v1/status (public even when token is required).
func typeWhisperReady(cfg Config) bool {
	if !typeWhisperWanted(cfg) {
		return false
	}
	base, _ := typeWhisperEndpoint(cfg)
	c := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := c.Get(base + "/v1/status")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// whisperCppReady reports whether the whisper.cpp listener is up.
func whisperCppReady(cfg Config) bool {
	return whisperCppEnabled(cfg) && portInUse(cfg.WhisperPort)
}

// voiceBackendStatus is used by /api/status: "ready" | "down" | "off".
// ready if any backend answers; down if configured but none answer; off if fully disabled.
func voiceBackendStatus(cfg Config) string {
	if !voiceEnabled(cfg) {
		return "off"
	}
	if typeWhisperReady(cfg) || whisperCppReady(cfg) {
		return "ready"
	}
	// TypeWhisper wanted but not up yet, and no whisper fallback ready → down
	return "down"
}

// voiceBackendLabel names the active (or preferred) STT for status UI.
func voiceBackendLabel(cfg Config) string {
	if typeWhisperReady(cfg) {
		return "TypeWhisper"
	}
	if whisperCppReady(cfg) {
		return "whisper.cpp"
	}
	if typeWhisperWanted(cfg) && whisperCppEnabled(cfg) {
		return "TypeWhisper→whisper"
	}
	if typeWhisperWanted(cfg) {
		return "TypeWhisper"
	}
	if whisperCppEnabled(cfg) {
		return "whisper.cpp"
	}
	return "выкл"
}

// transcribeAudio converts arbitrary audio bytes to text via TypeWhisper (primary)
// then whisper.cpp (fallback). srcExt is used for temp filenames (e.g. ".oga", ".webm").
func transcribeAudio(cfg Config, data []byte, srcExt string) (string, error) {
	if err := os.MkdirAll(voiceTmpDir, 0o755); err != nil {
		return "", err
	}
	stamp := fmt.Sprintf("%d", time.Now().UnixNano())
	if srcExt == "" {
		srcExt = ".bin"
	}
	if !strings.HasPrefix(srcExt, ".") {
		srcExt = "." + srcExt
	}
	src := filepath.Join(voiceTmpDir, "in-"+stamp+srcExt)
	if err := os.WriteFile(src, data, 0o644); err != nil {
		return "", err
	}
	defer os.Remove(src)

	var errs []string

	// 1) TypeWhisper — prefer local-file (no re-upload of large blobs through our process size limits
	//    beyond what we already hold), then multipart if local-file fails.
	if typeWhisperWanted(cfg) {
		if text, err := transcribeTypeWhisperFile(cfg, src); err == nil {
			return text, nil
		} else {
			errs = append(errs, "TypeWhisper local-file: "+err.Error())
		}
		// Convert to WAV and try multipart (some engines prefer clean PCM).
		wav, werr := audioToWav(cfg, src, stamp)
		if werr == nil {
			defer os.Remove(wav)
			if text, err := transcribeTypeWhisperMultipart(cfg, wav); err == nil {
				return text, nil
			} else {
				errs = append(errs, "TypeWhisper multipart: "+err.Error())
			}
		} else {
			errs = append(errs, "ffmpeg for TypeWhisper: "+werr.Error())
		}
	}

	// 2) whisper.cpp fallback
	if whisperCppEnabled(cfg) {
		wav := filepath.Join(voiceTmpDir, "out-"+stamp+".wav")
		if _, err := os.Stat(wav); err != nil {
			w, werr := audioToWav(cfg, src, stamp)
			if werr != nil {
				errs = append(errs, "ffmpeg for whisper.cpp: "+werr.Error())
			} else {
				wav = w
				defer os.Remove(wav)
			}
		} else {
			defer os.Remove(wav)
		}
		if _, err := os.Stat(wav); err == nil {
			if text, err := transcribeWhisperCpp(cfg, wav); err == nil {
				return text, nil
			} else {
				errs = append(errs, "whisper.cpp: "+err.Error())
			}
		}
	}

	if len(errs) == 0 {
		return "", fmt.Errorf("голос не настроен (TypeWhisper и whisper.cpp)")
	}
	return "", fmt.Errorf("%s", strings.Join(errs, " | "))
}

// audioToWav converts any ffmpeg-readable input path to 16 kHz mono PCM WAV.
func audioToWav(cfg Config, src, stamp string) (string, error) {
	wav := filepath.Join(voiceTmpDir, "out-"+stamp+".wav")
	conv := exec.Command(ffmpegExe(cfg), "-y", "-i", src, "-ar", "16000", "-ac", "1", "-f", "wav", wav)
	if out, err := conv.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg не сконвертировал аудио: %v (%s)", err, capLogTail(string(out)))
	}
	return wav, nil
}

// transcribeTypeWhisperFile uses POST /v1/transcribe/local-file with language hints ru+en.
func transcribeTypeWhisperFile(cfg Config, absPath string) (string, error) {
	base, token := typeWhisperEndpoint(cfg)
	abs, err := filepath.Abs(absPath)
	if err != nil {
		return "", err
	}
	payload, _ := json.Marshal(map[string]any{
		"path":            abs,
		"language_hints":  []string{"ru", "en"},
		"task":            "transcribe",
		"response_format": "json",
	})
	req, err := http.NewRequest(http.MethodPost, base+"/v1/transcribe/local-file", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-TypeWhisper-API-Token", token)
	}
	c := &http.Client{Timeout: 5 * time.Minute}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("TypeWhisper недоступен (%s): %w", base, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, capLogTail(string(raw)))
	}
	return parseSTTJSON(raw)
}

// transcribeTypeWhisperMultipart POSTs a WAV/audio file to /v1/transcribe.
func transcribeTypeWhisperMultipart(cfg Config, wavPath string) (string, error) {
	base, token := typeWhisperEndpoint(cfg)
	wavData, err := os.ReadFile(wavPath)
	if err != nil {
		return "", err
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filepath.Base(wavPath))
	if err != nil {
		return "", err
	}
	if _, err := part.Write(wavData); err != nil {
		return "", err
	}
	_ = mw.WriteField("language_hint", "ru")
	_ = mw.WriteField("language_hint", "en")
	_ = mw.WriteField("task", "transcribe")
	_ = mw.WriteField("response_format", "json")
	mw.Close()

	req, err := http.NewRequest(http.MethodPost, base+"/v1/transcribe", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-TypeWhisper-API-Token", token)
	}
	c := &http.Client{Timeout: 5 * time.Minute}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("TypeWhisper недоступен (%s): %w", base, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, capLogTail(string(raw)))
	}
	return parseSTTJSON(raw)
}

// transcribeWhisperCpp POSTs WAV to whisper.cpp /inference.
func transcribeWhisperCpp(cfg Config, wavPath string) (string, error) {
	wavData, err := os.ReadFile(wavPath)
	if err != nil {
		return "", err
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(wavData); err != nil {
		return "", err
	}
	_ = mw.WriteField("response_format", "json")
	_ = mw.WriteField("temperature", "0.0")
	mw.Close()

	c := &http.Client{Timeout: 5 * time.Minute}
	resp, err := c.Post(
		fmt.Sprintf("http://127.0.0.1:%d/inference", cfg.WhisperPort),
		mw.FormDataContentType(), &body)
	if err != nil {
		return "", fmt.Errorf("whisper-server недоступен: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("whisper HTTP %d: %s", resp.StatusCode, capLogTail(string(raw)))
	}
	return parseSTTJSON(raw)
}

// parseSTTJSON extracts text from OpenAI-compatible or TypeWhisper JSON responses.
func parseSTTJSON(raw []byte) (string, error) {
	var parsed struct {
		Text       string `json:"text"`
		Result     string `json:"result"`
		Transcript string `json:"transcript"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		// plain text body
		s := strings.TrimSpace(string(raw))
		if s != "" && !strings.HasPrefix(s, "{") {
			return s, nil
		}
		return "", fmt.Errorf("STT вернул не-JSON: %s", capLogTail(string(raw)))
	}
	text := strings.TrimSpace(parsed.Text)
	if text == "" {
		text = strings.TrimSpace(parsed.Result)
	}
	if text == "" {
		text = strings.TrimSpace(parsed.Transcript)
	}
	if text == "" {
		return "", fmt.Errorf("речь не распознана (пустой результат)")
	}
	return text, nil
}

// handleVoiceMessage downloads the voice/audio, transcribes it, echoes the transcript and
// answers it through the normal streaming chat pipeline.
func handleVoiceMessage(cfg Config, botAPI string, chatID int64, fileID string, duration int, mimeType string) {
	if !voiceEnabled(cfg) {
		sendTelegramMessage(botAPI, chatID, voiceSetupHelp())
		return
	}
	if duration > maxVoiceSeconds {
		sendTelegramMessage(botAPI, chatID, fmt.Sprintf("🎙 Слишком длинное аудио (%d мин) — пришли до %d минут.", duration/60, maxVoiceSeconds/60))
		return
	}
	stop := startTyping(botAPI, chatID)
	defer stop()

	filePath, err := getTelegramFilePath(botAPI, fileID)
	if err != nil {
		sendTelegramMessage(botAPI, chatID, "❌ Не смог получить аудио из Telegram: "+err.Error())
		return
	}
	data, _, err := downloadTelegramFile(cfg.TelegramToken, filePath)
	if err != nil {
		sendTelegramMessage(botAPI, chatID, "❌ Не смог скачать аудио: "+err.Error())
		return
	}
	ext := audioExt(filePath, mimeType)
	text, err := transcribeAudio(cfg, data, ext)
	if err != nil {
		log.Printf("[VOICE] transcribe error: %v", err)
		hint := ""
		if typeWhisperWanted(cfg) && !typeWhisperReady(cfg) {
			hint = "\n\n💡 Запусти TypeWhisper → Settings → Advanced → API Server ON (порт 8978), скачай локальную модель (Large V3 Turbo)."
		}
		sendTelegramMessage(botAPI, chatID, "❌ Не смог распознать речь: "+err.Error()+hint)
		return
	}
	log.Printf("[VOICE] transcribed %d chars from %d via %s", len(text), chatID, voiceBackendLabel(cfg))
	sendTelegramMessage(botAPI, chatID, "🎙 Распознал: «"+text+"»")

	if !brainReady(cfg.BrainPort) {
		sendTelegramMessage(botAPI, chatID, "⏳ Модель ещё грузится — отвечу на текст выше, когда будет готова. Повтори чуть позже.")
		return
	}
	// Voice and text are equal citizens (§7): the transcript goes into the SAME dispatcher,
	// so «скачай это видео» said out loud routes exactly like the typed version.
	allow := parseAllow(cfg.TelegramID)
	if p := peekPending(chatID); p != nil {
		if yes, ok := confirmWord(text); ok && isOwnerChat(allow, chatID) {
			stop()
			answerTelegramConfirmation(cfg, botAPI, chatID, yes)
			return
		}
	}
	if allow != nil && wantsToolAgent(text) {
		stop()
		runTelegramAgent(cfg, botAPI, allow, chatID, text)
		return
	}
	chatWithHistoryStream(cfg, botAPI, chatID, text, stop)
}

// voiceSetupHelp is shown when no STT backend is configured.
func voiceSetupHelp() string {
	return "🎙 Голосовые пока выключены. Варианты:\n" +
		"1) **TypeWhisper** (рекомендуется): установи TypeWhisper for Windows, включи HTTP API " +
		"(Settings → Advanced → API Server, порт 8978), скачай локальную модель " +
		"(Whisper Large V3 Turbo), оставь `TYPEWHISPER_URL=` пустым или `auto`.\n" +
		"2) **whisper.cpp fallback**: в `settings.ini` заполни `WHISPER_SERVER=` и `WHISPER_MODEL=`.\n" +
		"3. Перезапусти бота."
}

// audioExt picks a sensible file extension for ffmpeg input detection.
func audioExt(filePath, mimeType string) string {
	if ext := strings.ToLower(filepath.Ext(filePath)); ext != "" {
		return ext
	}
	switch {
	case strings.Contains(mimeType, "ogg"), strings.Contains(mimeType, "opus"):
		return ".oga"
	case strings.Contains(mimeType, "mpeg"), strings.Contains(mimeType, "mp3"):
		return ".mp3"
	case strings.Contains(mimeType, "mp4"), strings.Contains(mimeType, "m4a"):
		return ".m4a"
	case strings.Contains(mimeType, "webm"):
		return ".webm"
	case strings.Contains(mimeType, "wav"):
		return ".wav"
	default:
		return ".oga"
	}
}

// isAudioUpload reports whether an uploaded web file is audio (by mime or extension).
func isAudioUpload(name, mime string) bool {
	return classifyMedia(name, mime) == mediaAudio
}

// capLogTail keeps error payloads (ffmpeg/whisper output) short enough for Telegram/logs.
// The tail matters more than the head: ffmpeg prints the actual error last.
func capLogTail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 300 {
		return s
	}
	tail := s[len(s)-300:]
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return "…" + tail
}
