package main

// Озвучка ответов через локальный Qwen3-TTS 0.6B CustomVoice (GPU).
// Движок поднимает tts_server/server.py на loopback: модель грузится один раз,
// дальше POST /v1/tts отдаёт WAV. По умолчанию женский Serena, lang=Auto для RU/EN.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	defaultTTSPort = 7788
	ttsArtifactDir = "runtime/browser/tts"
	maxSpeakChars  = 2500
	maxAutoSpeakCh = 1600
	ttsHTTPTimeout = 90 * time.Second
	ttsMaxTimeout  = 5 * time.Minute
)

var (
	ttsCommands   = []string{"/tts", "/озвучка", "/voiceout"}
	speakCommands = []string{"/speak", "/скажи", "/озвучь"}

	lastSpeakMu sync.Mutex
	lastSpeak   = map[int64]string{}

	ttsSpreadOnce sync.Once
	ttsSynthMu    sync.Mutex
)

func respreadTTSAfterFirstSynth(cfg Config) {
	// Раньше размазывали нити SuperTonic по CPU. Qwen3-TTS сидит на GPU — no-op.
	_ = cfg
	ttsSpreadOnce.Do(func() {})
}

func pulseTTSAffinity(cfg Config) func() {
	_ = cfg
	return func() {}
}

func rememberSpeakable(chatID int64, text string) {
	s := speechText(text)
	if s == "" {
		return
	}
	lastSpeakMu.Lock()
	lastSpeak[chatID] = s
	lastSpeakMu.Unlock()
}

func takeLastSpeakable(chatID int64) string {
	lastSpeakMu.Lock()
	defer lastSpeakMu.Unlock()
	return lastSpeak[chatID]
}

func ttsWanted(cfg Config) bool {
	u := strings.TrimSpace(strings.ToLower(cfg.TTSURL))
	return u != "off" && u != "false" && u != "0" && u != "disabled" && u != "нет"
}

func ttsBaseURL(cfg Config) string {
	if u := strings.TrimSpace(cfg.TTSURL); u != "" &&
		!strings.EqualFold(u, "auto") && !strings.EqualFold(u, "default") &&
		!strings.EqualFold(u, "on") {
		return strings.TrimRight(u, "/")
	}
	port := cfg.TTSPort
	if port <= 0 {
		port = defaultTTSPort
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

func ttsReady(cfg Config) bool {
	if !ttsWanted(cfg) {
		return false
	}
	port := cfg.TTSPort
	if port <= 0 {
		port = defaultTTSPort
	}
	// Только порт: панель дёргает статус раз в 1.5 с — HTTP health не дёргаем.
	return portInUse(port)
}

func ttsStatus(cfg Config) string {
	if !ttsWanted(cfg) {
		return "off"
	}
	if ttsReady(cfg) {
		return "ready"
	}
	return "down"
}

const defaultTTSModel = "Qwen/Qwen3-TTS-12Hz-0.6B-CustomVoice"
const defaultTTSVoice = "Serena"

// ensureTTS поднимает tts_server/server.py (Qwen3-TTS), если порт свободен.
func ensureTTS(cfg Config) {
	if !ttsWanted(cfg) {
		log.Printf("[TTS] выключена (TTS_URL=off)")
		return
	}
	port := cfg.TTSPort
	if port <= 0 {
		port = defaultTTSPort
	}
	if portInUse(port) {
		if pid := pidListeningOnPort(port); pid > 0 {
			log.Printf("[TTS] порт :%d уже слушает (pid %d) — переиспользую Qwen3-TTS", port, pid)
		} else {
			log.Printf("[TTS] порт :%d уже слушает — переиспользую TTS", port)
		}
		return
	}
	py, script, err := ttsPythonAndScript(cfg)
	if err != nil {
		log.Printf("[TTS] %v", err)
		return
	}
	model := strings.TrimSpace(cfg.TTSModel)
	if model == "" {
		model = defaultTTSModel
	}
	voice := strings.TrimSpace(cfg.TTSVoice)
	if voice == "" {
		voice = defaultTTSVoice
	}
	args := []string{script, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--model", model, "--voice", voice}
	cmd := exec.Command(py, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = filepath.Dir(script)
	cmd.Env = ttsProcEnv(cfg)
	if err := cmd.Start(); err != nil {
		log.Printf("[TTS] не запустил Qwen3-TTS: %v", err)
		return
	}
	registerEngineProc(cmd.Process)
	gpuNote := "CUDA default"
	if cfg.TTSGPU >= 0 {
		gpuNote = fmt.Sprintf("CUDA_VISIBLE_DEVICES=%d", cfg.TTSGPU)
	}
	log.Printf("[TTS] Qwen3-TTS pid %d :%d — %s voice=%s (%s), первая загрузка ~2 ГБ",
		cmd.Process.Pid, port, model, voice, gpuNote)
}

func ttsProcEnv(cfg Config) []string {
	env := os.Environ()
	// По умолчанию сажаем озвучку на 3060 (index 1): 3090 занята мозгом.
	if cfg.TTSGPU >= 0 {
		env = append(env, "CUDA_VISIBLE_DEVICES="+strconv.Itoa(cfg.TTSGPU))
	}
	env = append(env,
		"PYTHONUTF8=1",
		"PYTHONIOENCODING=utf-8",
		"HF_HUB_DISABLE_SYMLINKS_WARNING=1",
	)
	return env
}

// ttsPythonAndScript ищет venv рядом с server.py либо явный TTS_SERVER.
func ttsPythonAndScript(cfg Config) (python, script string, err error) {
	if p := strings.TrimSpace(cfg.TTSExe); p != "" {
		lower := strings.ToLower(p)
		if strings.HasSuffix(lower, ".py") {
			script = p
			python = filepath.Join(filepath.Dir(p), ".venv", "Scripts", "python.exe")
			if _, e := os.Stat(python); e != nil {
				if w, lookErr := exec.LookPath("python"); lookErr == nil {
					python = w
				} else {
					return "", "", fmt.Errorf("TTS_SERVER=%s, но нет .venv рядом и python в PATH", p)
				}
			}
			return python, script, nil
		}
		// Путь к python.exe — рядом ищем server.py
		python = p
		cand := filepath.Join(filepath.Dir(p), "server.py")
		if _, e := os.Stat(cand); e == nil {
			return python, cand, nil
		}
		cand = filepath.Join("tts_server", "server.py")
		if abs, e := filepath.Abs(cand); e == nil {
			if _, e2 := os.Stat(abs); e2 == nil {
				return python, abs, nil
			}
		}
		return "", "", fmt.Errorf("TTS_SERVER=%s: не нашёл server.py", p)
	}
	script = filepath.Join("tts_server", "server.py")
	if abs, e := filepath.Abs(script); e == nil {
		script = abs
	}
	if _, e := os.Stat(script); e != nil {
		return "", "", fmt.Errorf("нет %s — запусти tts_server\\install.cmd", script)
	}
	python = filepath.Join(filepath.Dir(script), ".venv", "Scripts", "python.exe")
	if _, e := os.Stat(python); e != nil {
		return "", "", fmt.Errorf("нет %s — запусти tts_server\\install.cmd", python)
	}
	return python, script, nil
}

// SpeechFile — готовый файл озвучки для панели или Telegram.
type SpeechFile struct {
	WAV      string
	OGG      string
	URL      string
	Lang     string
	Duration float64
	Chars    int
}

// ttsTimeoutFor: короткий текст — 90 с; длинный ответ (до 2500) легко
// не влезает, особенно на первом синтезе, пока ONNX поднимает пул.
func ttsTimeoutFor(chars int) time.Duration {
	d := ttsHTTPTimeout
	if extra := chars - 400; extra > 0 {
		d += time.Duration(extra) * 80 * time.Millisecond
	}
	if d > ttsMaxTimeout {
		return ttsMaxTimeout
	}
	return d
}

func isTTSTimeout(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "timeout") || strings.Contains(s, "deadline exceeded")
}

func ttsWaitLog(start time.Time, limit time.Duration, chars int) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				log.Printf("[TTS] Qwen3-TTS ещё считает… %s / лимит %s (%d символов, GPU)",
					time.Since(start).Truncate(time.Second), limit.Truncate(time.Second), chars)
			}
		}
	}()
	return func() { close(done) }
}

func synthesizeSpeech(cfg Config, raw string) (SpeechFile, error) {
	queued := time.Now()
	ttsSynthMu.Lock()
	defer ttsSynthMu.Unlock()
	if waited := time.Since(queued); waited > 200*time.Millisecond {
		log.Printf("[TTS] ждал очередь %.0f с — предыдущий синтез ещё шёл", waited.Seconds())
	}

	started := time.Now()
	rawN := utf8.RuneCountInString(raw)
	text := speechText(raw)
	if text == "" {
		log.Printf("[TTS] пусто после очистки (исходник %d символов, %.0f мс)", rawN, elapsedMS(started))
		return SpeechFile{}, fmt.Errorf("нечего озвучивать — после очистки текста не осталось")
	}
	if !ttsWanted(cfg) {
		log.Printf("[TTS] отказ: TTS_URL=off")
		return SpeechFile{}, fmt.Errorf("озвучка выключена (TTS_URL=off)")
	}
	if !ttsReady(cfg) {
		log.Printf("[TTS] Qwen3-TTS не слушает порт — синтез не начался")
		return SpeechFile{}, fmt.Errorf("озвучка ещё не готова — подожди загрузку модели или запусти tts_server\\install.cmd")
	}
	lang := speechLang(text)
	voice := strings.TrimSpace(cfg.TTSVoice)
	if voice == "" {
		voice = defaultTTSVoice
	}
	chars := utf8.RuneCountInString(text)
	limit := ttsTimeoutFor(chars)
	log.Printf("[TTS] синтез %d символов (исходник %d) lang=%s voice=%s лимит %s — «%s»",
		chars, rawN, lang, voice, limit.Truncate(time.Second), clipStatus(text, 90))

	body, _ := json.Marshal(map[string]any{
		"text":  text,
		"voice": voice,
		"lang":  lang,
	})
	c := &http.Client{Timeout: limit}
	stopAff := pulseTTSAffinity(cfg)
	defer stopAff()
	stopBeat := ttsWaitLog(started, limit, chars)
	defer stopBeat()
	resp, err := c.Post(ttsBaseURL(cfg)+"/v1/tts", "application/json", bytes.NewReader(body))
	if err != nil {
		elapsed := time.Since(started).Truncate(time.Millisecond)
		if isTTSTimeout(err) {
			log.Printf("[TTS] ТАЙМАУТ за %s (лимит %s, %d символов). %v",
				elapsed, limit.Truncate(time.Second), chars, err)
			return SpeechFile{}, fmt.Errorf("Qwen3-TTS не уложился в %s (%d символов) — смотри консоль [TTS]",
				limit.Truncate(time.Second), chars)
		}
		log.Printf("[TTS] Qwen3-TTS не ответил за %s: %v", elapsed, err)
		return SpeechFile{}, fmt.Errorf("Qwen3-TTS не ответил: %w", err)
	}
	defer resp.Body.Close()
	wav, err := io.ReadAll(io.LimitReader(resp.Body, 80<<20))
	if err != nil {
		log.Printf("[TTS] не прочитал ответ TTS за %s: %v", time.Since(started).Truncate(time.Millisecond), err)
		return SpeechFile{}, err
	}
	if resp.StatusCode != 200 {
		snippet := capAgentText(string(wav), 240)
		log.Printf("[TTS] Qwen3-TTS HTTP %d за %s (%d байт тела): %s",
			resp.StatusCode, time.Since(started).Truncate(time.Millisecond), len(wav), snippet)
		return SpeechFile{}, fmt.Errorf("Qwen3-TTS HTTP %d: %s", resp.StatusCode, capAgentText(string(wav), 200))
	}
	respreadTTSAfterFirstSynth(cfg)
	if len(wav) < 44 || string(wav[0:4]) != "RIFF" {
		log.Printf("[TTS] TTS вернул не WAV (%d байт, HTTP %d, %s)",
			len(wav), resp.StatusCode, time.Since(started).Truncate(time.Millisecond))
		return SpeechFile{}, fmt.Errorf("TTS вернул не WAV (%d байт)", len(wav))
	}
	if err := os.MkdirAll(ttsArtifactDir, 0o755); err != nil {
		log.Printf("[TTS] не создать %s: %v", ttsArtifactDir, err)
		return SpeechFile{}, err
	}
	stamp := time.Now().Format("20060102-150405.000")
	wavPath := filepath.Join(ttsArtifactDir, "say-"+stamp+".wav")
	if err := os.WriteFile(wavPath, wav, 0o644); err != nil {
		log.Printf("[TTS] не записал %s: %v", wavPath, err)
		return SpeechFile{}, err
	}
	out := SpeechFile{
		WAV: wavPath, Lang: lang, Chars: chars,
		URL: "/api/files/tts/" + filepath.Base(wavPath),
	}
	if ogg, oerr := wavToOpus(cfg, wavPath); oerr == nil {
		out.OGG = ogg
	} else {
		log.Printf("[TTS] ogg не собрался (%v) — отдам wav", oerr)
	}
	log.Printf("[TTS] готово за %s: %s (%d КБ WAV, %d символов, %s)",
		time.Since(started).Truncate(time.Millisecond), filepath.Base(wavPath), len(wav)/1024, chars, lang)
	return out, nil
}

func elapsedMS(t time.Time) float64 {
	return float64(time.Since(t).Milliseconds())
}

func wavToOpus(cfg Config, wavPath string) (string, error) {
	ogg := strings.TrimSuffix(wavPath, filepath.Ext(wavPath)) + ".ogg"
	cmd := exec.Command(ffmpegExe(cfg), "-y", "-i", wavPath, "-c:a", "libopus", "-b:a", "32k", ogg)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%v: %s", err, capAgentText(stderr.String(), 160))
	}
	return ogg, nil
}

func telegramVoicePath(s SpeechFile) string {
	if s.OGG != "" {
		return s.OGG
	}
	return s.WAV
}

// speakAfterReply озвучивает ответ, если включён режим «всегда» ИЛИ пользователь просил голос.
func wantsSpokenReply(userText string) bool {
	t := strings.ToLower(userText)
	for _, k := range []string{"озвучь", "озвучи", "голосом", "вслух", "проговори", "скажи голос"} {
		if strings.Contains(t, k) {
			return true
		}
	}
	return false
}

func shouldSpeakReply(cfg Config, userText string) bool {
	return ttsWanted(cfg) && (ttsAutoOn() || wantsSpokenReply(userText))
}

func speakAfterReply(cfg Config, botAPI string, chatID int64, userText, reply string) {
	rememberSpeakable(chatID, reply)
	if !shouldSpeakReply(cfg, userText) {
		return
	}
	clipped := speechText(reply)
	if utf8.RuneCountInString(clipped) > maxAutoSpeakCh {
		clipped = trimRunes(clipped, maxAutoSpeakCh)
	}
	go func() {
		log.Printf("[TTS] автоозвучка ответа")
		sf, err := synthesizeSpeech(cfg, clipped)
		if err != nil {
			log.Printf("[TTS] автоозвучка не вышла: %v", err)
			return
		}
		path := telegramVoicePath(sf)
		if err := sendTelegramVoiceFile(botAPI, chatID, path); err != nil {
			log.Printf("[TTS] sendVoice: %v", err)
		}
	}()
}

func sendTelegramVoiceFile(botAPI string, chatID int64, path string) error {
	return sendTelegramMultipartFile(botAPI, "sendVoice", "voice", chatID, path, "")
}

// speechText вычищает разметку: модели читают слова, не звёздочки и не блоки кода.
func speechText(raw string) string {
	s := stripThink(raw)
	s = stripCodeFences(s)
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" {
			b.WriteByte('\n')
			continue
		}
		if strings.HasPrefix(trim, "|") || strings.HasPrefix(trim, "---") {
			continue
		}
		if strings.HasPrefix(trim, "```") {
			continue
		}
		if strings.HasPrefix(trim, "⚡") && strings.Contains(trim, "ток/с") {
			continue
		}
		b.WriteString(stripMarkupLine(trim))
		b.WriteByte('\n')
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	out = speakSources(out)
	out = stripUnspeakable(out)
	out = strings.TrimSpace(out)
	lang := speechLang(out)
	digitLang := lang
	if digitLang == "auto" || digitLang == "na" {
		digitLang = "ru" // пропись чисел — по-русски, если смесь/неясно
	}
	out = speakDigits(out, digitLang)
	if utf8.RuneCountInString(out) > maxSpeakChars {
		out = trimRunes(out, maxSpeakChars)
	}
	return out
}

var (
	reSpeakMDLink = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)]+)\)`)
	reSpeakURL    = regexp.MustCompile(`https?://[^\s)>\]]+`)
)

// speakSources: «https://www.interfax.ru/…» → «источник Интерфакс», не по буквам.
func speakSources(s string) string {
	s = reSpeakMDLink.ReplaceAllStringFunc(s, func(m string) string {
		parts := reSpeakMDLink.FindStringSubmatch(m)
		if len(parts) < 3 {
			return m
		}
		return sourcePhrase(parts[1], parts[2])
	})
	s = reSpeakURL.ReplaceAllStringFunc(s, func(raw string) string {
		return sourcePhrase("", raw)
	})
	return s
}

func sourcePhrase(title, rawURL string) string {
	name := strings.TrimSpace(title)
	if name != "" && !strings.HasPrefix(strings.ToLower(name), "http") {
		return name
	}
	host := prettyHost(rawURL)
	if host == "" {
		return "источник"
	}
	return "источник " + host
}

func prettyHost(raw string) string {
	u, err := url.Parse(raw)
	host := ""
	if err == nil {
		host = u.Hostname()
	}
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	known := map[string]string{
		"interfax.ru":       "Интерфакс",
		"rbc.ru":            "РБК",
		"tass.ru":           "ТАСС",
		"ria.ru":            "РИА Новости",
		"kommersant.ru":     "Коммерсант",
		"vedomosti.ru":      "Ведомости",
		"forbes.ru":         "Форбс",
		"lenta.ru":          "Лента",
		"gazeta.ru":         "Газета.ру",
		"bbc.com":           "би-би-си",
		"bbc.co.uk":         "би-би-си",
		"reuters.com":       "Рейтер",
		"bloomberg.com":     "Блумберг",
		"coinmarketcap.com": "Коинмаркеткап",
		"coingecko.com":     "Коингеко",
		"binance.com":       "Бинанс",
		"coinbase.com":      "Коинбейс",
	}
	if n, ok := known[host]; ok {
		return n
	}
	if host == "" {
		return "сайт"
	}
	if i := strings.IndexByte(host, '.'); i > 0 {
		return host[:i]
	}
	return host
}

// stripUnspeakable выкидывает эмодзи и служебные знаки (в т.ч. U+FE0F у ⚠️/✅).
func stripUnspeakable(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if speechRuneOK(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func speechRuneOK(r rune) bool {
	if r == ' ' || r == '\n' || r == '\t' {
		return true
	}
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return true
	}
	switch r {
	case '.', ',', '!', '?', ':', ';', '-', '–', '—', '…',
		'\'', '"', '(', ')', '/', '%', '$', '+', '=',
		'«', '»', '№', '°':
		return true
	}
	return false
}

func stripCodeFences(s string) string {
	var b strings.Builder
	in := false
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			in = !in
			continue
		}
		if !in {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func stripMarkupLine(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "__", "")
	s = strings.ReplaceAll(s, "~~", "")
	s = strings.ReplaceAll(s, "`", "")
	s = strings.TrimLeft(s, "#>-*•")
	s = strings.TrimSpace(s)
	return s
}

func speechLang(s string) string {
	var letters, cyr, lat int
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		switch {
		case r >= 0x0400 && r <= 0x04FF:
			cyr++
		case (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'):
			lat++
		}
	}
	if letters == 0 {
		return "auto"
	}
	// Смесь кириллицы и латиницы — Auto: Qwen сам ведёт оба языка одним голосом.
	if cyr > 0 && lat > 0 && cyr*100/letters >= 10 && lat*100/letters >= 10 {
		return "auto"
	}
	if cyr*100/letters >= 25 {
		return "ru"
	}
	return "en"
}

func toolSpeakText(t *Task, cfg Config, args map[string]any) ToolResult {
	text := strings.TrimSpace(fmt.Sprint(args["text"]))
	if text == "" || text == "<nil>" {
		return ToolResult{Status: StatusFailed, Text: "нужно поле text"}
	}
	sf, err := synthesizeSpeech(cfg, text)
	if err != nil {
		return ToolResult{Status: StatusFailed, Text: err.Error(), Err: err}
	}
	arts := []string{sf.WAV}
	if sf.OGG != "" {
		arts = append(arts, sf.OGG)
	}
	if t != nil {
		t.AddArtifacts(arts)
	}
	return ToolResult{
		Status:    StatusOK,
		Text:      fmt.Sprintf("озвучено (%s): %s", sf.Lang, sf.URL),
		Artifacts: arts,
	}
}

func trimRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for idx := range s {
		if i == n {
			return strings.TrimSpace(s[:idx]) + "…"
		}
		i++
	}
	return s
}
