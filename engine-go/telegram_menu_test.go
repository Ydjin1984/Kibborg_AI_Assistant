package main

// Тесты меню Telegram: список команд у кнопки «☰» и инлайн-панель /menu.

import (
	"encoding/json"
	"strings"
	"testing"
)

// Панель — это ещё и индикатор: активный режим рук помечен, иначе кнопка отвечает на вопрос
// «как переключить», но не на вопрос «а сейчас как».
func TestMenuKeyboardShowsCurrentHands(t *testing.T) {
	t.Cleanup(func() { setHandsMode(handsModeSafe, "test-cleanup") })

	setHandsMode(handsModeSafe, "test")
	safe := menuKeyboard()
	if !strings.Contains(safe, "✅ 🛡 Короткие руки") {
		t.Errorf("в safe должны быть отмечены короткие руки: %s", safe)
	}
	if strings.Contains(safe, "✅ 🖐 Длинные") {
		t.Error("в safe длинные руки отмечаться не должны")
	}

	setHandsMode(handsModeFull, "test")
	full := menuKeyboard()
	if !strings.Contains(full, "✅ 🖐 Длинные руки") {
		t.Errorf("в full должны быть отмечены длинные руки: %s", full)
	}
}

// callback_data у Telegram ограничен 64 байтами; превышение — молчаливо неработающая кнопка.
func TestMenuKeyboardIsValidTelegramMarkup(t *testing.T) {
	var kb struct {
		Keyboard [][]struct {
			Text string `json:"text"`
			Data string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}
	if err := json.Unmarshal([]byte(menuKeyboard()), &kb); err != nil {
		t.Fatalf("клавиатура — не валидный JSON: %v", err)
	}
	seen := map[string]bool{}
	n := 0
	for _, row := range kb.Keyboard {
		for _, b := range row {
			n++
			if b.Text == "" || b.Data == "" {
				t.Errorf("кнопка без текста или без данных: %+v", b)
			}
			if len(b.Data) > 64 {
				t.Errorf("callback_data %q длиннее 64 байт — Telegram такую кнопку не примет", b.Data)
			}
			if seen[b.Data] {
				t.Errorf("две кнопки с одинаковым callback_data %q", b.Data)
			}
			seen[b.Data] = true
		}
	}
	if n < 6 {
		t.Errorf("в панели всего %d кнопок — маловато для «панели»", n)
	}
	// Обработчик обязан знать каждую кнопку: неизвестный data = кнопка, которая ничего не делает.
	known := map[string]bool{
		cbHandsFull: true, cbHandsSafe: true, cbCompact: true, cbReset: true,
		cbStop: true, cbStatus: true, cbSkills: true, cbRefresh: true,
	}
	for data := range seen {
		if !known[data] {
			t.Errorf("кнопка %q не обрабатывается в handleCallbackQuery", data)
		}
	}
}

// Список у «☰» должен содержать то, ради чего меню и заводилось.
func TestBotCommandListCoversControls(t *testing.T) {
	got := map[string]bool{}
	for _, c := range botCommandList() {
		if strings.HasPrefix(c.Command, "/") {
			t.Errorf("команда %q не должна начинаться со слэша — Telegram добавит его сам", c.Command)
		}
		if c.Description == "" || len(c.Description) > 256 {
			t.Errorf("описание команды %q не годится (%d символов)", c.Command, len(c.Description))
		}
		got[c.Command] = true
	}
	for _, want := range []string{"menu", "hands", "stop", "compact", "reset", "agent"} {
		if !got[want] {
			t.Errorf("в списке команд нет /%s", want)
		}
	}
}

// «Что я умею» берётся из движка, а не у модели. Причина в живом прогоне: на этот же вопрос
// модель выдала список того, чего якобы не может — скриншот рабочего стола, мышь, запуск
// программ, — и всё это было неправдой.
func TestSkillsTextClaimsRealCapabilities(t *testing.T) {
	s := strings.ToLower(skillsText())
	for _, want := range []string{"скриншот экрана", "клавиатура и мышь", "запуск программ", "буфер обмена"} {
		if !strings.Contains(s, want) {
			t.Errorf("в списке возможностей нет %q", want)
		}
	}
	for _, forbidden := range []string{"не могу", "нет доступа", "недоступ"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("список возможностей не должен содержать %q", forbidden)
		}
	}
}

// Панель показывает состояние: режим рук, занятость и контекст.
func TestMenuTextShowsState(t *testing.T) {
	cfg := Config{BrainPort: 0, CtxSize: 32768}
	live.turnDone(GenStats{PromptTokens: 2000, GenTokens: 10})
	text := menuText(cfg, 12345)
	for _, want := range []string{"Панель Kibborg", "руки", "Контекст"} {
		if !strings.Contains(text, want) {
			t.Errorf("в панели нет %q:\n%s", want, text)
		}
	}
}
