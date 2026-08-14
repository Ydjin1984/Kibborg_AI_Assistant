package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func webTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(newWebMux(Config{BrainPort: 1, WhisperPort: 1, WebPort: 8090}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWebIndexServed(t *testing.T) {
	srv := webTestServer(t)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET / = %d", resp.StatusCode)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "Kibborg") {
		t.Error("index page does not look like the dashboard")
	}
	// unknown paths must 404, not serve the index
	r2, err := http.Get(srv.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != 404 {
		t.Errorf("GET /nope = %d, want 404", r2.StatusCode)
	}
}

// Фильтр accept у кнопки 📎 — единственное место, где панель может молча отказать в формате:
// файл, которого нет в фильтре, диалог Windows просто не показывает, и со стороны это выглядит
// как «формат не поддерживается». Так выпал PDF: бэкенд (handleWebPDFTurn) его разбирал, а
// выбрать было нечем. Паритет Telegram↔Web ломается тут беззвучно — значит, проверяем кодом.
func TestUploadAcceptCoversSupportedKinds(t *testing.T) {
	accept := strings.ToLower(inputAcceptFilter(string(webIndexHTML)))
	// Значение должно быть именно атрибутом, а не куском разметки: иначе проверки ниже пройдут
	// на любом тексте страницы и тест станет декорацией.
	if accept == "" || strings.ContainsAny(accept, "<>") {
		t.Fatalf("фильтр accept не выделен: %q", accept)
	}
	cases := []struct {
		name, group string // group — групповой шаблон, если расширение перечислять не обязательно
		want        MediaKind
	}{
		{name: "скан.pdf", want: mediaPDF},
		{name: "ролик.mp4", group: "video/*", want: mediaVideo},
		{name: "фото.jpg", group: "image/*", want: mediaImage},
		{name: "речь.ogg", group: "audio/*", want: mediaAudio},
	}
	for _, c := range cases {
		if got := classifyMedia(c.name, ""); got != c.want {
			t.Errorf("classifyMedia(%q) = %v, ждали %v", c.name, got, c.want)
		}
		ext := strings.ToLower(filepath.Ext(c.name))
		if strings.Contains(accept, ext) || (c.group != "" && strings.Contains(accept, c.group)) {
			continue
		}
		t.Errorf("accept прячет %s, хотя движок этот вид разбирает: %s", c.name, accept)
	}
}

// Индикатор работы в чате показывает живые токены, а берёт их из activity.snapshot().
// Связка держится на имени поля в JSON: переименуют его в Go — счётчик в панели молча
// замрёт на нуле, и никакой компилятор об этом не скажет.
func TestLiveTokensReachTheUI(t *testing.T) {
	a := &activity{phase: "idle"}
	a.begin("тест")
	for range 5 {
		a.tick()
	}
	snap := a.snapshot()
	tok, ok := snap["live_tokens"]
	if !ok {
		t.Fatalf("snapshot не отдаёт live_tokens: %v", snap)
	}
	if tok != 5 {
		t.Errorf("live_tokens = %v, ждали 5", tok)
	}
	if !strings.Contains(string(webIndexHTML), "live_tokens") {
		t.Error("панель не читает live_tokens — индикатор останется без счётчика токенов")
	}
	// Индикатор обязан существовать: без него ожидание снова превращается в три точки.
	for _, must := range []string{"startWork", "stopWork", "WORK_SYMBOLS"} {
		if !strings.Contains(string(webIndexHTML), must) {
			t.Errorf("в панели нет %q — индикатор работы потерян", must)
		}
	}
	// Пока идёт работа, фаза обязана быть generating: по ней панель понимает, что цифры живые.
	if snap["phase"] != "generating" {
		t.Errorf("phase = %v во время генерации", snap["phase"])
	}
}

// inputAcceptFilter достаёт accept у поля загрузки файла из вшитой страницы.
func inputAcceptFilter(html string) string {
	i := strings.Index(html, `id="fileInput"`)
	if i < 0 {
		return ""
	}
	rest := html[i:]
	j := strings.Index(rest, `accept="`)
	if j < 0 {
		return ""
	}
	rest = rest[j+len(`accept="`):]
	k := strings.IndexByte(rest, '"')
	if k < 0 {
		return ""
	}
	return rest[:k]
}

func TestWebStatusShape(t *testing.T) {
	srv := webTestServer(t)
	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"brain_ready", "whisper", "chrome_tabs", "chats", "uptime_sec"} {
		if _, ok := s[key]; !ok {
			t.Errorf("status is missing %q: %v", key, s)
		}
	}
	// with nothing running, brain must honestly report not-ready
	if s["brain_ready"] != false {
		t.Errorf("brain_ready = %v with no LLM on port 1", s["brain_ready"])
	}
}

// Панель восстанавливает переписку после F5 из /api/history: история живёт на сервере, и до
// этого эндпоинта она стиралась только с экрана — со стороны это читалось как «бот забыл всё».
func TestWebHistoryAndArtifacts(t *testing.T) {
	srv := webTestServer(t)
	histMu.Lock()
	if history == nil {
		history = map[int64][]chatMsg{}
	}
	history[webChatID] = []chatMsg{{Role: "user", Content: "привет"}, {Role: "assistant", Content: "на связи"}}
	histMu.Unlock()
	t.Cleanup(func() { histMu.Lock(); delete(history, webChatID); histMu.Unlock() })

	resp, err := http.Get(srv.URL + "/api/history")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var h struct {
		Messages []struct {
			Role string `json:"role"`
			Text string `json:"text"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		t.Fatal(err)
	}
	if len(h.Messages) != 2 || h.Messages[0].Role != "user" || h.Messages[0].Text != "привет" {
		t.Fatalf("история пришла не той: %+v", h.Messages)
	}
	// Галерея файлов: список обязан отдаваться даже когда runtime/browser ещё не создан —
	// пустая вкладка честнее ошибки на пустом месте.
	r2, err := http.Get(srv.URL + "/api/artifacts")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != 200 {
		t.Errorf("GET /api/artifacts = %d", r2.StatusCode)
	}
	var a map[string]any
	if err := json.NewDecoder(r2.Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	if _, ok := a["files"]; !ok {
		t.Errorf("в ответе нет files: %v", a)
	}
}

// Свечи для графика тянутся с биржи, поэтому проверяем ровно то, что не ходит в сеть:
// таймфрейм берётся из белого списка, а пустой тикер отбивается до запроса.
func TestWebCandlesValidation(t *testing.T) {
	srv := webTestServer(t)
	for _, q := range []string{"", "?symbol=", "?symbol=BTC&tf=7m", "?symbol=BTC&tf=../../etc"} {
		resp, err := http.Get(srv.URL + "/api/candles" + q)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET /api/candles%s = %d, ждали 400", q, resp.StatusCode)
		}
	}
}

// Иконки кнопок вшиты в бинарь: панель обязана рисоваться из одного exe, без папки рядом.
func TestWebIconsEmbedded(t *testing.T) {
	srv := webTestServer(t)
	resp, err := http.Get(srv.URL + "/icons/send.svg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /icons/send.svg = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
		t.Errorf("Content-Type = %q", ct)
	}
	// Выход за пределы папки иконок не должен отдавать ничего.
	r2, err := http.Get(srv.URL + "/icons/../index.html")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode == 200 {
		t.Error("/icons/ отдал файл за пределами набора иконок")
	}
}

func TestModelsTabAndHardwareAPI(t *testing.T) {
	html := string(webIndexHTML)
	for _, want := range []string{`data-tab="models"`, `id="tab-models"`, `id="hwRun"`, `id="modList"`, "/api/hardware", "/api/models"} {
		if !strings.Contains(html, want) {
			t.Errorf("в панели нет %q", want)
		}
	}
	srv := webTestServer(t)
	resp, err := http.Get(srv.URL + "/api/hardware")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /api/hardware = %d", resp.StatusCode)
	}
	var hw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&hw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cpu", "ram", "gpus", "summary"} {
		if _, ok := hw[key]; !ok {
			t.Errorf("hardware нет %q: %v", key, hw)
		}
	}
}

func TestRouteWebMessageHardwareAndAnalyze(t *testing.T) {
	if is, _ := parseCommand("/hw", hardwareCommands); !is {
		t.Fatal("/hw должен разбираться")
	}
	if is, arg := parseCommand("/models qwen gpu", modelsCommands); !is || arg == "" {
		t.Fatal("/models должен оставлять аргумент")
	}
	if is, arg := parseCommand("/analyze BTC", analyzeCommands); !is || !strings.EqualFold(arg, "BTC") {
		t.Fatal("/analyze в веб-чате обязан ловиться тем же разбором, что в Telegram")
	}
}

func TestWebResetAndChatValidation(t *testing.T) {
	srv := webTestServer(t)
	// reset requires POST
	r, err := http.Get(srv.URL + "/api/reset")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/reset = %d, want 405", r.StatusCode)
	}
	resp, err := http.Post(srv.URL+"/api/reset", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("POST /api/reset = %d", resp.StatusCode)
	}
	// chat without message → 400
	resp2, err := http.Post(srv.URL+"/api/chat", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("empty chat = %d, want 400", resp2.StatusCode)
	}
	// analyze without symbol → 400
	resp3, err := http.Get(srv.URL + "/api/analyze")
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusBadRequest {
		t.Errorf("analyze without symbol = %d, want 400", resp3.StatusCode)
	}
}
