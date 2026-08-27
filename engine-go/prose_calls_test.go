package main

// Recovering prose tool calls (prose_calls.go). Both directions are tested: a missed call
// wastes a turn, a FALSE positive executes something the model only mentioned.

import (
	"encoding/json"
	"strings"
	"testing"

	"kibborg/engine/browser"
)

func proseTools() []browser.ToolSpec {
	return assemblePackTools(browser.New(""), []string{packConsole, packFiles, packWeb, packTrade})
}

func argsOf(t *testing.T, tc toolCall) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &m); err != nil {
		t.Fatalf("аргументы не JSON: %v (%s)", err, tc.Function.Arguments)
	}
	return m
}

// Live security-tab failure: model ended with a fenced {"tool":"read_file","path":"…"}
// instead of a machine tool_call — nothing ran and the user got JSON as an "answer".
func TestParseJSONProseToolCall(t *testing.T) {
	text := "продолжу глубокий пентест\n\n```json\n" +
		`{"tool": "read_file", "path": "D:\\Kibborg_DaVinchi_Bot\\pentest_profi_sysx_uz\\endpoints_data.json"}` +
		"\n```"
	calls := parseProseToolCalls(text, proseTools())
	if len(calls) != 1 {
		t.Fatalf("ждали 1 вызов, получили %d", len(calls))
	}
	if calls[0].Function.Name != "read_file" {
		t.Fatalf("имя = %s", calls[0].Function.Name)
	}
	args := argsOf(t, calls[0])
	path, _ := args["path"].(string)
	if !strings.Contains(path, "endpoints_data.json") {
		t.Fatalf("path = %q", path)
	}
}

// Verbatim from the live run — including the Windows path with escaped backslashes, which is
// where a naive unquote turns \t into a TAB.
func TestParseProseCallFromLiveRun(t *testing.T) {
	text := "удалю каталог D:\\tmp\\kib-probe\n\n" +
		`run_command(command="Remove-Item -Path 'D:\\tmp\\kib-probe' -Recurse -Force -ErrorAction SilentlyContinue", timeout_sec=30)`

	calls := parseProseToolCalls(text, proseTools())
	if len(calls) != 1 {
		t.Fatalf("ждали 1 вызов, получили %d", len(calls))
	}
	if calls[0].Function.Name != "run_command" {
		t.Fatalf("имя = %s", calls[0].Function.Name)
	}
	args := argsOf(t, calls[0])
	cmd, _ := args["command"].(string)
	if cmd != `Remove-Item -Path 'D:\tmp\kib-probe' -Recurse -Force -ErrorAction SilentlyContinue` {
		t.Fatalf("команда разобрана неверно: %q", cmd)
	}
	if n, _ := args["timeout_sec"].(float64); n != 30 {
		t.Fatalf("timeout_sec = %v", args["timeout_sec"])
	}
}

// A path the model forgot to escape must stay a path, never become control characters.
func TestParseProseKeepsUnescapedPath(t *testing.T) {
	calls := parseProseToolCalls(`write_file(path="D:\tmp\notes.txt", content="привет")`, proseTools())
	if len(calls) != 1 {
		t.Fatalf("ждали 1 вызов, получили %d", len(calls))
	}
	args := argsOf(t, calls[0])
	if p, _ := args["path"].(string); p != `D:\tmp\notes.txt` {
		t.Fatalf("путь испорчен: %q", p)
	}
}

func TestParseProseFormsAndTypes(t *testing.T) {
	cases := []struct {
		name string
		text string
		tool string
		want map[string]any
	}{
		{"json-объект", `run_command({"command": "git status", "timeout_sec": 15})`, "run_command",
			map[string]any{"command": "git status", "timeout_sec": float64(15)}},
		{"одинарные кавычки", `web_search(query='новости BTC', limit=5)`, "web_search",
			map[string]any{"query": "новости BTC", "limit": float64(5)}},
		{"булево", `write_file(path="a.txt", content="x", append=true)`, "write_file",
			map[string]any{"path": "a.txt", "content": "x", "append": true}},
		{"запятая внутри строки", `web_search(query="btc, eth и другие")`, "web_search",
			map[string]any{"query": "btc, eth и другие"}},
		{"вложенные скобки в строке", `run_command(command="echo (test)")`, "run_command",
			map[string]any{"command": "echo (test)"}},
		{"в обратных кавычках", "Выполню: `analyze_ticker(symbol=\"ETH\")`", "analyze_ticker",
			map[string]any{"symbol": "ETH"}},
	}
	for _, c := range cases {
		calls := parseProseToolCalls(c.text, proseTools())
		if len(calls) != 1 || calls[0].Function.Name != c.tool {
			t.Errorf("%s: получили %d вызовов %+v", c.name, len(calls), calls)
			continue
		}
		args := argsOf(t, calls[0])
		for k, want := range c.want {
			if got := args[k]; got != want {
				t.Errorf("%s: %s = %#v, ждали %#v", c.name, k, got, want)
			}
		}
	}
}

// False positives execute things nobody asked for. These must all parse to nothing.
func TestParseProseRefusesNonCalls(t *testing.T) {
	for _, text := range []string{
		"",
		"Готово, файл записан.",
		"Могу выполнить run_command, если нужно.",               // упоминание без скобок
		"Инструмент run_command() умеет запускать команды.",     // пустые скобки без аргументов
		"Функция my_run_command(command=\"rm -rf /\") не наша.", // чужое имя
		"Сравни f(x) и g(y) — это математика.",                  // не инструменты
		"run_command(git status)",    // позиционный аргумент
		"delete_path(path=\"D:\\x\"", // незакрытая скобка
		"unknown_tool(path=\"x\")",   // нет в активных паках
		"Раньше я вызывал run_command(\n\nа теперь не буду",             // разрыв абзацем
		"В документации написано: run_command(command=…) — плейсхолдер", // многоточие вместо значения
	} {
		if calls := parseProseToolCalls(text, proseTools()); len(calls) != 0 {
			t.Errorf("ложное срабатывание на %q → %+v", text, calls)
		}
	}
}

// Empty-arg tools are legitimate, but only when the tool really takes none.
func TestParseProseNoArgTool(t *testing.T) {
	tools := assemblePackTools(browser.New(""), []string{packBrowserRead})
	calls := parseProseToolCalls("сейчас посмотрю: list_tabs()", tools)
	if len(calls) != 1 || calls[0].Function.Name != "list_tabs" {
		t.Fatalf("вызов без аргументов должен восстанавливаться: %+v", calls)
	}
}

// A recovered call must be gated exactly like a real one — that is what makes recovery safe.
func TestRecoveredCallGoesThroughGate(t *testing.T) {
	ls := newTestLoop(t, []string{packConsole})
	calls := parseProseToolCalls(`run_command(command="diskpart /s wipe.txt")`, ls.tools)
	if len(calls) != 1 {
		t.Fatalf("вызов не восстановлен: %+v", calls)
	}
	out := ls.runTurn(calls)
	if !out.stopped || !out.refused {
		t.Fatal("ядерная команда, восстановленная из текста, обязана упереться в ворота")
	}
	if ans := answeredIDs(ls.msgs)[calls[0].ID]; ans == "" {
		t.Fatal("восстановленный вызов тоже должен получить tool-ответ")
	}
}

// The lie that started this: the model telling the user its tools are switched off.
func TestClaimsNoToolsDetected(t *testing.T) {
	lies := []string{
		"Согласно моей текущей конфигурации, у меня отключены инструменты для работы с интернетом (web, browser.read и др.).",
		"Я работаю в изолированном режиме и не имею доступа к живым данным.",
		"У меня нет доступа к интернету прямо сейчас.",
		"Остальные наборы инструментов в данный момент не подключены.",
		// Второй живой прогон, дословно. На «сделай скриншот рабочего стола» и «перечисли,
		// чего ты не умеешь» модель выдала вот это — и каждая строка была ложью: пак system
		// умеет и экран, и мышь, и запуск программ.
		"У меня нет прямого доступа к вашему рабочему столу.",
		"Я не вижу, что происходит на вашем экране, кроме окон браузера.",
		"Не могу делать скриншоты рабочего стола: инструмент работает только внутри браузера.",
		"Не могу управлять мышью и клавиатурой вне браузера.",
		"Не могу запускать произвольные приложения.",
		"Я работаю как изолированный ИИ-ассистент в Telegram.",
		"Если нужен скриншот рабочего стола, сделайте его через Win+Shift+S.",
	}
	for _, s := range lies {
		if !claimsNoTools(s) {
			t.Errorf("не распознано как заявление об отсутствии инструментов: %q", s)
		}
	}
	for _, ok := range []string{
		"Готово, нашёл три статьи.",
		"Не нашёл такого тикера на Binance — уточни название.",
		"Команда завершилась с ошибкой доступа к файлу.",
	} {
		if claimsNoTools(ok) {
			t.Errorf("ложное срабатывание на %q", ok)
		}
	}
	if !strings.Contains(noToolsNudge, "request_pack") {
		t.Error("поправка обязана называть request_pack")
	}
}

// The armoury note must not read like a permission list — that framing is what produced
// «остальные наборы не подключены» as an answer to the user.
func TestArmouryNoteDoesNotSoundLikePermissions(t *testing.T) {
	note := armouryNote([]string{packChat}, handsModeSafe)
	for _, want := range []string{"request_pack", "НЕ нужно", "ЗАПРЕЩЕНО", "web", "console", "files", "trade", "system"} {
		if !strings.Contains(note, want) {
			t.Errorf("в описании арсенала нет %q", want)
		}
	}
	if strings.Contains(note, "Активные наборы инструментов") {
		t.Error("старая формулировка вернулась — именно она читалась моделью как «остальное отключено»")
	}
	// Рубильник рук обязан быть виден МОДЕЛИ, а не только воротам: пока промпт в обоих режимах
	// говорил одно и то же, развязанные руки не меняли ничего в том, что модель о себе думает.
	full := armouryNote([]string{packChat}, handsModeFull)
	if !strings.Contains(full, "ДЛИННЫЕ") || !strings.Contains(note, "КОРОТКИЕ") {
		t.Error("промпт должен называть текущий режим рук")
	}
	if full == note {
		t.Error("описание арсенала обязано отличаться в safe и full")
	}
}
