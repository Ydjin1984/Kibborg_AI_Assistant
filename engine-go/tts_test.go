package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSpeechTextStripsCodeAndTables(t *testing.T) {
	raw := "Привет.\n```go\nfmt.Println(1)\n```\n| a | b |\n|------|\n**жирный** и `код`\n\n⚡ 12 ток/с генерация"
	got := speechText(raw)
	if strings.Contains(got, "fmt.Println") || strings.Contains(got, "|") || strings.Contains(got, "**") {
		t.Fatalf("мусор остался: %q", got)
	}
	if !strings.Contains(got, "Привет") || !strings.Contains(got, "жирный") {
		t.Fatalf("смысл вырезан: %q", got)
	}
}

func TestSpeechTextStripsEmojiAndVS16(t *testing.T) {
	// U+FE0F — невидимый хвост эмодзи; в озвучку не пускаем.
	raw := "Вход ✅\uFE0F разрешён. ⚠️ Риск низкий."
	got := speechText(raw)
	if strings.Contains(got, "\uFE0F") || strings.Contains(got, "✅") || strings.Contains(got, "⚠") {
		t.Fatalf("эмодзи/VS16 ушли в озвучку: %q", got)
	}
	if !strings.Contains(got, "Вход") || !strings.Contains(got, "разрешён") {
		t.Fatalf("смысл вырезан: %q", got)
	}
}

func TestTTSReadyDoesNotHitHealthHTTP(t *testing.T) {
	// Регресс: панель опрашивает /api/status каждые 1.5 с. Если ttsReady
	// ходит на /v1/health — TTS-сервер забивает лог сотнями 200 OK.
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "health") {
			hits++
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	cfg := Config{TTSURL: srv.URL, TTSPort: 1}
	_ = ttsReady(cfg)
	_ = ttsStatus(cfg)
	if hits != 0 {
		t.Fatalf("ttsReady сходил на /health %d раз — лог TTS снова засорится", hits)
	}
}

func TestSpeechLangCyrillicVsLatin(t *testing.T) {
	if g := speechLang("Привет, как дела сегодня?"); g != "ru" {
		t.Fatalf("ru → %s", g)
	}
	if g := speechLang("Hello, how are you today?"); g != "en" {
		t.Fatalf("en → %s", g)
	}
	if g := speechLang("Сегодня BTC вырос на support level около resistance"); g != "auto" {
		t.Fatalf("mixed → %s", g)
	}
}

func TestTTSModePersistsAskDefault(t *testing.T) {
	t.Cleanup(func() { setTTSMode(ttsModeAsk, "test-cleanup") })
	if normalizeTTSMode("always") != ttsModeAuto || normalizeTTSMode("по запросу") != ttsModeAsk {
		t.Fatal("нормализация режима сломана")
	}
	if setTTSMode("auto", "test") != ttsModeAuto || !ttsAutoOn() {
		t.Fatal("auto не включился")
	}
	if setTTSMode("ask", "test") != ttsModeAsk || ttsAutoOn() {
		t.Fatal("ask не вернулся")
	}
}

func TestTTSEndpointReadsAndWrites(t *testing.T) {
	t.Cleanup(func() { setTTSMode(ttsModeAsk, "test-cleanup") })
	mux := newWebMux(Config{})
	post := func(body string) map[string]any {
		r := httptest.NewRequest(http.MethodPost, "/api/tts", strings.NewReader(body))
		r.Host = "127.0.0.1:8090"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("POST /api/tts → %d", w.Code)
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := post(`{"mode":"auto"}`); got["auto"] != true || got["mode"] != ttsModeAuto {
		t.Fatalf("auto: %v", got)
	}
	if got := post(`{"mode":"ask"}`); got["auto"] != false {
		t.Fatalf("ask: %v", got)
	}
}

func TestSpeakRejectsEmpty(t *testing.T) {
	mux := newWebMux(Config{})
	r := httptest.NewRequest(http.MethodPost, "/api/speak", strings.NewReader(`{}`))
	r.Host = "127.0.0.1:8090"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("пустой speak = %d", w.Code)
	}
}

func TestWebHasSpeakControls(t *testing.T) {
	html := string(webIndexHTML)
	for _, want := range []string{`id="ttsBtn"`, "Озвучить", "/api/speak", "/api/tts", "speakText"} {
		if !strings.Contains(html, want) {
			t.Errorf("в панели нет %q", want)
		}
	}
}

func TestWantsSpokenReply(t *testing.T) {
	if !wantsSpokenReply("озвучь свой ответ голосом") {
		t.Fatal("просьба озвучить не поймана")
	}
	if wantsSpokenReply("просто привет") {
		t.Fatal("обычный чат не должен сам включаться")
	}
}

func TestArmouryNoteForbidsDenyingTTS(t *testing.T) {
	note := armouryNote([]string{packChat}, handsModeSafe)
	for _, want := range []string{"Qwen3-TTS", "speak_text", "SAPI"} {
		if !strings.Contains(note, want) {
			t.Errorf("промпт не запрещает враньё про TTS: нет %q", want)
		}
	}
}

func TestTTSProcEnvPinsGPU(t *testing.T) {
	env := strings.Join(ttsProcEnv(Config{TTSGPU: 1}), "\n")
	if !strings.Contains(env, "CUDA_VISIBLE_DEVICES=1") {
		t.Fatal("озвучка должна садиться на TTS_GPU")
	}
}

func TestParseTTSAndSpeakCommands(t *testing.T) {
	if is, arg := parseCommand("/tts auto", ttsCommands); !is || arg != "auto" {
		t.Fatal("/tts")
	}
	if is, _ := parseCommand("/speak", speakCommands); !is {
		t.Fatal("/speak")
	}
}

func TestTTSTimeoutScalesWithLength(t *testing.T) {
	if got := ttsTimeoutFor(80); got != ttsHTTPTimeout {
		t.Fatalf("короткий текст: %s", got)
	}
	long := ttsTimeoutFor(2500)
	if long <= ttsHTTPTimeout {
		t.Fatalf("длинный текст должен получить больше 90 с, а не %s", long)
	}
	if long > ttsMaxTimeout {
		t.Fatalf("потолок %s, получили %s", ttsMaxTimeout, long)
	}
}

func TestTTSTimeoutDetectsDeadline(t *testing.T) {
	if !isTTSTimeout(fmt.Errorf("Post http://127.0.0.1:7788/v1/tts: context deadline exceeded (Client.Timeout exceeded while awaiting headers)")) {
		t.Fatal("не узнали таймаут")
	}
	if isTTSTimeout(fmt.Errorf("connection refused")) {
		t.Fatal("обычная ошибка не таймаут")
	}
	if isTTSTimeout(nil) {
		t.Fatal("nil не таймаут")
	}
}
