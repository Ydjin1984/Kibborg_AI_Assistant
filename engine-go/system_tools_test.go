package main

// Тесты пака `system` (рабочий стол). Часть из них — настоящие: они реально снимают экран
// этого ПК и реально перечисляют его окна. На не-Windows они пропускаются, а не притворяются
// зелёными: заглушка, отвечающая «не поддерживается», проверяется отдельно.

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Главная регрессия второго живого прогона: на «сделай скриншот рабочего стола» агент открыл
// вкладку Chrome и объяснил, что рабочий стол ему недоступен. Инструмент экрана обязан
// существовать, быть в паке и НЕ путаться с браузерным capture_screenshot.
func TestSystemPackHasDesktopTools(t *testing.T) {
	names := map[string]bool{}
	for _, s := range systemToolSpecs() {
		names[s.Function.Name] = true
	}
	for _, want := range []string{
		"capture_screen", "list_windows", "focus_window", "type_keyboard",
		"press_keys", "mouse_action", "list_processes", "kill_process", "launch_app", "clipboard",
	} {
		if !names[want] {
			t.Errorf("в паке system нет инструмента %s", want)
		}
	}
	if names["capture_screenshot"] {
		t.Error("capture_screenshot — браузерный инструмент, в паке рабочего стола ему не место")
	}
	// Диспетчер исполнителя ищет локальные инструменты в одном реестре — имена обязаны там быть,
	// иначе вызов уедет в browser.Session и вернётся «неизвестный инструмент».
	for name := range systemToolNames {
		if !localToolNames[name] {
			t.Errorf("%s не зарегистрирован как локальный инструмент", name)
		}
	}
}

// Скриншот целого экрана — на настоящем экране этого ПК.
func TestCaptureScreenProducesRealPNG(t *testing.T) {
	if !desktopSupported() {
		t.Skip("рабочий стол доступен только на Windows")
	}
	task := newTask(fullActor(), "скриншот")
	defer task.Close()

	res, ok := dispatchSystemTool(task, Config{}, "capture_screen", map[string]any{})
	if !ok {
		t.Fatal("capture_screen не отдиспетчеризовался")
	}
	if res.Status != StatusOK {
		t.Fatalf("скриншот не удался: %s", res.Text)
	}
	if len(res.Artifacts) != 1 {
		t.Fatalf("скриншот обязан приехать артефактом (файлом человеку), получили %v", res.Artifacts)
	}
	path := res.Artifacts[0]
	t.Cleanup(func() { os.Remove(path) })

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("файл скриншота не читается: %v", err)
	}
	// Пустой/чёрный PNG — самый частый способ провалить снимок экрана незаметно.
	if len(data) < 5000 {
		t.Fatalf("скриншот подозрительно мал (%d байт) — вероятно, снят пустой экран", len(data))
	}
	if string(data[1:4]) != "PNG" {
		t.Fatalf("это не PNG: % x", data[:8])
	}
	t.Logf("скриншот: %s, %d КБ", path, len(data)/1024)
}

func TestListWindowsSeesRealWindows(t *testing.T) {
	if !desktopSupported() {
		t.Skip("рабочий стол доступен только на Windows")
	}
	wins, err := listDesktopWindows()
	if err != nil {
		t.Fatalf("список окон не получен: %v", err)
	}
	if len(wins) == 0 {
		t.Skip("ни одного видимого окна с заголовком — проверять нечего")
	}
	for _, w := range wins {
		if strings.TrimSpace(w.Title) == "" {
			t.Error("окно без заголовка не должно попадать в список")
		}
	}
	t.Logf("видимых окон: %d, первое: %q (%s)", len(wins), wins[0].Title, wins[0].Exe)
}

func TestClipboardRoundTrip(t *testing.T) {
	if !desktopSupported() {
		t.Skip("буфер обмена читаю только на Windows")
	}
	// Кириллица и кавычки — ровно то, что ломается при передаче через powershell -Command.
	want := `Кибборг «тест» буфера: 42 "кавычки" и путь D:\tmp`
	if err := writeClipboardText(want); err != nil {
		t.Skipf("буфер обмена недоступен в этой сессии: %v", err)
	}
	got, err := readClipboardText()
	if err != nil {
		t.Fatalf("не прочитал буфер: %v", err)
	}
	if got != want {
		t.Fatalf("буфер обмена исказил текст:\nзаписали: %q\nпрочитали: %q", want, got)
	}
}

// Сочетания клавиш модель пишет словами — разбор обязан понимать те формы, которые она пишет.
func TestParseKeyCombo(t *testing.T) {
	if !desktopSupported() {
		t.Skip("клавиатура только на Windows")
	}
	ok := []string{"ctrl+c", "CTRL+SHIFT+ESC", "win+r", "enter", "alt+f4", "ctrl+alt+delete", "a"}
	for _, c := range ok {
		if _, key, err := parseKeyCombo(c); err != nil || key == 0 {
			t.Errorf("не разобрал %q: %v", c, err)
		}
	}
	for _, bad := range []string{"", "ctrl+", "жми+сюда"} {
		if _, _, err := parseKeyCombo(bad); err == nil {
			t.Errorf("%q должно было не разобраться", bad)
		}
	}
}

// Регрессия живого прогона: «открой блокнот и напиши туда текст, потом сними окно Блокнота».
// Под «Блокнот» подошли ДВА окна — только что открытое агентом и давний свёрнутый документ
// пользователя, — снимок уехал в чужой документ, а отчёт был «скриншот окна Блокнота сделан».
// Порядок предпочтений теперь явный и проверяется здесь.
func TestPickWindowPrefersActiveOverMinimised(t *testing.T) {
	agentWin := desktopWindow{Title: "Безымянный – Блокнот", W: 800, H: 600, Active: true}
	oldWin := desktopWindow{Title: "Новый текстовый документ (2).txt – Блокнот", W: 160, H: 28, Minimized: true}

	best, others := pickWindow([]desktopWindow{oldWin, agentWin}, "Блокнот")
	if best.Title != agentWin.Title {
		t.Fatalf("выбрано %q; активное окно должно бить свёрнутое, даже если то больше", best.Title)
	}
	if len(others) != 1 || others[0].Title != oldWin.Title {
		t.Fatalf("остальные кандидаты должны возвращаться наверх, получили %v", others)
	}
	// Точное совпадение заголовка сильнее активности — но только среди настоящих окон.
	exact := desktopWindow{Title: "Блокнот", W: 900, H: 700}
	best, _ = pickWindow([]desktopWindow{agentWin, exact}, "Блокнот")
	if best.Title != "Блокнот" {
		t.Errorf("точное совпадение заголовка должно побеждать, выбрано %q", best.Title)
	}
	// Свёрнутое проигрывает развёрнутому. Размеры взяты как их отдаёт Windows: у свёрнутого
	// окна GetWindowRect возвращает служебные 160×28, а не его настоящий размер.
	best, _ = pickWindow([]desktopWindow{
		{Title: "A – Блокнот", W: 160, H: 28, Minimized: true},
		{Title: "B – Блокнот", W: 900, H: 700},
	}, "Блокнот")
	if best.Title != "B – Блокнот" {
		t.Errorf("развёрнутое окно должно побеждать свёрнутое, выбрано %q", best.Title)
	}
	if got := windowTitles([]desktopWindow{oldWin}); !strings.Contains(got, "Новый текстовый") {
		t.Errorf("подсказка про другие окна собралась неправильно: %q", got)
	}
	if got := windowTitles(nil); got == "" {
		t.Error("пустой список окон должен описываться словами, а не пустой строкой")
	}
	// Всплывающее уведомление Telegram (320×80) НЕ должно побеждать настоящее окно, даже если
	// оно активно: снимок «окна Telegram» уезжал ровно на такую всплывашку.
	toast := desktopWindow{Title: "Telegram", W: 320, H: 80, Active: true}
	main := desktopWindow{Title: "(1) Иван Петров", Exe: "Telegram.exe", W: 1630, H: 1440}
	best, _ = pickWindow([]desktopWindow{toast, main}, "telegram")
	if best.Title != main.Title {
		t.Errorf("выбрана всплывашка %q вместо настоящего окна", best.Title)
	}
}

// Ошибка «окно не найдено» обязана нести список доступных окон.
//
// Пока она этого не делала, модель тратила лишний круг: не нашла окно → позвала list_windows →
// на всякий случай запустила Блокнот ЕЩЁ раз и оставила пользователю лишнее окно на столе.
func TestWindowNotFoundListsCandidates(t *testing.T) {
	if !desktopSupported() {
		t.Skip("окна только на Windows")
	}
	_, err := findWindow("заведомо-несуществующее-окно-кибборга-42")
	if err == nil {
		t.Fatal("такого окна быть не должно")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Сейчас открыты") {
		t.Errorf("ошибка должна перечислять открытые окна, получили: %s", msg)
	}
	if strings.Contains(msg, "вызови list_windows") {
		t.Error("отсылать к отдельному вызову больше не нужно — список уже в ошибке")
	}
}

// Вторая половина той же регрессии: launch_app возвращался мгновенно, а окно программы
// появлялось позже — и следующий type_keyboard печатал в то окно, что было активно ДО
// запуска. Теперь запуск ждёт окно, а если не дождался — честно об этом говорит.
func TestLaunchWaitsForWindowOrSaysSo(t *testing.T) {
	if !desktopSupported() {
		t.Skip("окна только на Windows")
	}
	// У процесса тестов окна нет — ожидание обязано закончиться предупреждением, а не молчанием.
	if _, ok := waitWindowOfProcess(os.Getpid(), 200*time.Millisecond); ok {
		t.Fatal("у консольного процесса окна быть не должно")
	}
	note := launchedWindowNote(-1)
	if !strings.Contains(note, "не появилось") || !strings.Contains(note, "list_windows") {
		t.Errorf("без окна запуск должен предупреждать, куда уедет ввод: %q", note)
	}
}

func TestSplitLaunchArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"-a -b", []string{"-a", "-b"}},
		{`"D:\путь с пробелами\file.txt" --flag`, []string{`D:\путь с пробелами\file.txt`, "--flag"}},
		{"  один   два  ", []string{"один", "два"}},
	}
	for _, c := range cases {
		got := splitLaunchArgs(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("splitLaunchArgs(%q) = %v, ждали %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("splitLaunchArgs(%q) = %v, ждали %v", c.in, got, c.want)
			}
		}
	}
}

// Ворота для рабочего стола: короткие руки спрашивают, длинные делают молча, а чужой чат
// не получает ничего — рубильник принадлежит владельцу (§6.0).
func TestGuardDesktopInput(t *testing.T) {
	calls := []struct {
		tool string
		args map[string]any
	}{
		{"type_keyboard", map[string]any{"text": "привет"}},
		{"press_keys", map[string]any{"keys": "alt+f4"}},
		{"mouse_action", map[string]any{"action": "click", "x": 10.0, "y": 10.0}},
		{"launch_app", map[string]any{"target": "notepad"}},
	}
	for _, c := range calls {
		if d := guardToolCall(safeActor(), c.tool, c.args); d.Action != ActionAsk || d.Rule != ruleDesktopInput {
			t.Errorf("[safe] %s → %s/%s; хотели ask/desktop_input", c.tool, d.Action, d.Rule)
		}
		if d := guardToolCall(fullActor(), c.tool, c.args); d.Action != ActionAllow {
			t.Errorf("[full] %s → %s (%s); хотели allow — руки развязаны", c.tool, d.Action, d.Reason)
		}
		stranger := Actor{Mode: handsModeFull, Channel: channelTelegram, ChatID: 999, IsOwner: false}
		if d := guardToolCall(stranger, c.tool, c.args); d.Action != ActionDeny {
			t.Errorf("[чужой] %s → %s; развязанные руки владельца не дают прав постороннему",
				c.tool, d.Action)
		}
	}
	// Смотреть — не значит трогать: чтение экрана и окон не спрашивает даже в коротких руках.
	for _, name := range []string{"capture_screen", "list_windows", "focus_window", "list_processes", "clipboard"} {
		if d := guardToolCall(safeActor(), name, map[string]any{}); d.Action != ActionAllow {
			t.Errorf("[safe] %s → %s (%s); чтение должно идти без вопросов", name, d.Action, d.Reason)
		}
	}
}

// На не-Windows пак обязан честно отказать, а не упасть с паникой.
func TestSystemToolsRefuseWhereUnsupported(t *testing.T) {
	if desktopSupported() {
		t.Skip("здесь рабочий стол поддерживается — проверяем на других ОС")
	}
	task := newTask(fullActor(), "скриншот")
	defer task.Close()
	res, ok := dispatchSystemTool(task, Config{}, "capture_screen", map[string]any{})
	if !ok || res.Status != StatusFailed {
		t.Fatalf("ждали честный отказ, получили ok=%v %s", ok, res.Status)
	}
}
