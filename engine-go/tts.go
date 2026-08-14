package main

// Озвучка ответов через локальный SuperTonic 3 (ONNX, CPU, без GPU).
// Движок поднимает `supertonic serve` на loopback, как llama-server для мозга:
// модель грузится один раз, дальше POST /v1/tts отдаёт WAV.

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
	"runtime"
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
	ttsSpreadOnce.Do(func() {
		n := pulseTTSAffinityOnce(cfg)
		log.Printf("[TTS] после первого синтеза размазал %d нитей SuperTonic на оба CPU", n)
	})
}

func pulseTTSAffinityOnce(cfg Config) int {
	port := cfg.TTSPort
	if port <= 0 {
		port = defaultTTSPort
	}
	pid := pidListeningOnPort(port)
	if pid <= 0 {
		return 0
	}
	setProcessAllCpuSets(pid)
	return spreadPIDTree(pid)
}

// pulseTTSAffinity крутит размазку, пока идёт POST /v1/tts: пул ONNX рождается
// уже во время запроса и иначе остаётся на одном Xeon.
func pulseTTSAffinity(cfg Config) func() {
	pulseTTSAffinityOnce(cfg)
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(120 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				pulseTTSAffinityOnce(cfg)
			}
		}
	}()
	return func() { close(done) }
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
	// Только порт: панель дёргает статус раз в 1.5 с, а SuperTonic пишет
	// каждый GET /v1/health в лог — отсюда простыня «200 OK».
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

// ensureTTS поднимает `supertonic serve`, если порт свободен.
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
			n := spreadPIDTree(pid)
			log.Printf("[TTS] порт :%d уже слушает (pid %d) — потоки размазаны по CPU (%d нитей)", port, pid, n)
		} else {
			log.Printf("[TTS] порт :%d уже слушает — переиспользую SuperTonic", port)
		}
		return
	}
	exe := ttsExe(cfg)
	if exe == "" {
		log.Printf("[TTS] supertonic не найден в PATH — `pip install \"supertonic[serve]\"`")
		return
	}
	intra, inter := ttsThreadPlan(cfg)
	args := []string{"serve", "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--model", "supertonic-3"}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = ttsProcEnv(intra, inter)
	if err := cmd.Start(); err != nil {
		log.Printf("[TTS] не запустил SuperTonic: %v", err)
		return
	}
	registerEngineProc(cmd.Process)
	go func(pid int) {
		time.Sleep(2 * time.Second)
		n := spreadPIDAcrossCPUGroups(pid)
		log.Printf("[TTS] SuperTonic pid %d :%d — F1, %d intra / %d inter потоков, размазано %d нитей на оба CPU",
			pid, port, intra, inter, n)
	}(cmd.Process.Pid)
	log.Printf("[TTS] SuperTonic pid %d :%d — первая загрузка весов ~400 МБ, CPU (intra=%d)", cmd.Process.Pid, port, intra)
}

// ttsThreadPlan — сколько потоков отдать ONNX. По умолчанию все физические ядра
// обоих сокетов: иначе Windows/ONNX сажают процесс на одну группу и второй Xeon спит.
func ttsThreadPlan(cfg Config) (intra, inter int) {
	if cfg.TTSThreads > 0 {
		intra = cfg.TTSThreads
	} else {
		hw := probeHardware(false)
		// Логические процессоры обоих сокетов: иначе 44 нити садятся в одну
		// группу Windows и второй Xeon простаивает.
		intra = hw.Summary.Threads
		if intra <= 0 {
			intra = hw.Summary.Cores
		}
		if intra <= 0 {
			intra = runtime.NumCPU()
		}
	}
	if intra < 2 {
		intra = 2
	}
	inter = hwSockets()
	if inter < 2 {
		inter = 2
	}
	return intra, inter
}

func ttsProcEnv(intra, inter int) []string {
	return append(os.Environ(),
		"SUPERTONIC_INTRA_OP_THREADS="+strconv.Itoa(intra),
		"SUPERTONIC_INTER_OP_THREADS="+strconv.Itoa(inter),
		"OMP_NUM_THREADS="+strconv.Itoa(intra),
		// Не ставить OMP_PROC_BIND/OMP_PLACES: на Windows с двумя группами
		// OpenMP видит только «свой» Xeon и второй сокет остаётся пустым.
		"OMP_PROC_BIND=false",
		"KMP_AFFINITY=disabled",
		"OMP_WAIT_POLICY=ACTIVE",
	)
}

func hwSockets() int {
	hw := probeHardware(false)
	if hw.Summary.Sockets > 0 {
		return hw.Summary.Sockets
	}
	return 2
}

func ttsExe(cfg Config) string {
	if p := strings.TrimSpace(cfg.TTSExe); p != "" {
		return p
	}
	if p, err := exec.LookPath("supertonic"); err == nil {
		return p
	}
	if p, err := exec.LookPath("supertonic.exe"); err == nil {
		return p
	}
	return ""
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
				log.Printf("[TTS] SuperTonic ещё считает… %s / лимит %s (%d символов, оба CPU)",
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
		log.Printf("[TTS] SuperTonic не слушает порт — синтез не начался")
		return SpeechFile{}, fmt.Errorf("SuperTonic ещё не готов — подожди несколько секунд или поставь: pip install \"supertonic[serve]\"")
	}
	lang := speechLang(text)
	voice := strings.TrimSpace(cfg.TTSVoice)
	if voice == "" {
		voice = "F1"
	}
	steps := cfg.TTSSteps
	if steps <= 0 {
		steps = 8
	}
	chars := utf8.RuneCountInString(text)
	limit := ttsTimeoutFor(chars)
	log.Printf("[TTS] синтез %d символов (исходник %d) lang=%s voice=%s steps=%d лимит %s — «%s»",
		chars, rawN, lang, voice, steps, limit.Truncate(time.Second), clipStatus(text, 90))

	body, _ := json.Marshal(map[string]any{
		"text":  text,
		"voice": voice,
		"lang":  lang,
		"steps": steps,
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
			log.Printf("[TTS] ТАЙМАУТ за %s (лимит %s, %d символов). SuperTonic мог не успеть и всё ещё грузит оба CPU. %v",
				elapsed, limit.Truncate(time.Second), chars, err)
			return SpeechFile{}, fmt.Errorf("SuperTonic не уложился в %s (%d символов) — смотри консоль [TTS]; процесс ещё может грузить оба CPU",
				limit.Truncate(time.Second), chars)
		}
		log.Printf("[TTS] SuperTonic не ответил за %s: %v", elapsed, err)
		return SpeechFile{}, fmt.Errorf("SuperTonic не ответил: %w", err)
	}
	defer resp.Body.Close()
	wav, err := io.ReadAll(io.LimitReader(resp.Body, 80<<20))
	if err != nil {
		log.Printf("[TTS] не прочитал ответ SuperTonic за %s: %v", time.Since(started).Truncate(time.Millisecond), err)
		return SpeechFile{}, err
	}
	if resp.StatusCode != 200 {
		snippet := capAgentText(string(wav), 240)
		log.Printf("[TTS] SuperTonic HTTP %d за %s (%d байт тела): %s",
			resp.StatusCode, time.Since(started).Truncate(time.Millisecond), len(wav), snippet)
		return SpeechFile{}, fmt.Errorf("SuperTonic HTTP %d: %s", resp.StatusCode, capAgentText(string(wav), 200))
	}
	// Пул ONNX появляется после первого синтеза — тогда и размазываем нити по обоим Xeon.
	respreadTTSAfterFirstSynth(cfg)
	if len(wav) < 44 || string(wav[0:4]) != "RIFF" {
		log.Printf("[TTS] SuperTonic вернул не WAV (%d байт, HTTP %d, %s)",
			len(wav), resp.StatusCode, time.Since(started).Truncate(time.Millisecond))
		return SpeechFile{}, fmt.Errorf("SuperTonic вернул не WAV (%d байт)", len(wav))
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
	out = speakDigits(out, speechLang(out))
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

// stripUnspeakable выкидывает эмодзи и служебные знаки. SuperTonic падает на
// variation selector U+FE0F (невидимый хвост у ⚠️/✅): «unsupported character ️».
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
	var letters, cyr int
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
			if r >= 0x0400 && r <= 0x04FF {
				cyr++
			}
		}
	}
	if letters == 0 {
		return "na"
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
