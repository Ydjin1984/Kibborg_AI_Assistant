package main

// Пак `system` (ТЗ §5) — весь ПК за пределами браузера: экран, окна, клавиатура, мышь,
// процессы, буфер обмена, запуск программ.
//
// Он появился потому, что на живом прогоне агент на просьбу «сделай скриншот рабочего стола»
// открыл вкладку Chrome и честно ответил, что рабочий стол ему недоступен: capture_screenshot
// снимает только страницу в браузере. Модель не капризничала — у неё действительно не было
// такого инструмента, и никакой рубильник рук этого не чинил.
//
// Артефакты (PNG) складываются под runtime/browser/desktop, чтобы их подхватили и
// sendArtifacts в Telegram, и webFileURL в панели, — тем же путём, что скриншоты вкладок.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"kibborg/engine/browser"
)

// desktopArtifactDir mirrors browser.artifactDir so the Web UI's /api/files/ mapping works.
const desktopArtifactDir = "runtime/browser/desktop"

// systemToolNames are the tools dispatched by dispatchSystemTool.
var systemToolNames = map[string]bool{
	"capture_screen": true, "list_windows": true, "focus_window": true,
	"type_keyboard": true, "press_keys": true, "mouse_action": true,
	"list_processes": true, "kill_process": true, "launch_app": true, "clipboard": true,
}

// Пак `system` живёт в package main, поэтому его имена должны попасть в тот же реестр, по
// которому исполнитель решает «это локальный инструмент, а не вызов browser.Session».
func init() {
	for name := range systemToolNames {
		localToolNames[name] = true
	}
}

// systemToolSpecs is the `system` pack. Описания намеренно короткие — их суммарный размер
// упирается в бюджет схем (TestPackSchemaBudget, §5).
func systemToolSpecs() []browser.ToolSpec {
	return []browser.ToolSpec{
		spec("capture_screen", "Скриншот рабочего стола, монитора или окна. Без аргументов — весь экран. "+
			"С look= ещё и СМОТРИТ на снимок: описывает экран и даёт координаты элемента для mouse_action.",
			objSchema(map[string]any{
				"monitor": numSchema("номер монитора, 1..N"),
				"window":  strSchema("часть заголовка окна"),
				"look":    strSchema("что найти/понять на снимке, например «кнопка Стоп»"),
			})),
		spec("list_windows", "Открытые окна: заголовок, процесс, размер, активное.", objSchema(nil)),
		spec("focus_window", "Вывести окно на передний план.", objSchema(map[string]any{
			"window": strSchema("часть заголовка"),
		}, "window")),
		spec("type_keyboard", "Напечатать текст в активное окно (любой язык).", objSchema(map[string]any{
			"text":   strSchema(""),
			"window": strSchema("сначала сфокусировать это окно"),
		}, "text")),
		spec("press_keys", "Нажать сочетание клавиш: ctrl+c, win+r, alt+f4, enter.", objSchema(map[string]any{
			"keys":   strSchema(""),
			"window": strSchema("сначала сфокусировать это окно"),
		}, "keys")),
		spec("mouse_action", "Мышь: move|click|double|right|scroll по координатам экрана.", objSchema(map[string]any{
			"action": strSchema("move|click|double|right|middle|scroll"),
			"x":      numSchema(""),
			"y":      numSchema(""),
			"amount": numSchema("делений колеса, + вверх"),
		}, "action")),
		spec("list_processes", "Процессы ПК: pid, имя, память.", objSchema(map[string]any{
			"name":  strSchema("фильтр по имени"),
			"limit": numSchema("по умолч. 30"),
		})),
		spec("kill_process", "Завершить процесс по pid или имени.", objSchema(map[string]any{
			"pid":  numSchema(""),
			"name": strSchema("chrome, notepad"),
		})),
		spec("launch_app", "Запустить программу, файл, папку или URL.", objSchema(map[string]any{
			"target": strSchema("путь к exe/файлу/папке или URL"),
			"args":   strSchema("аргументы через пробел"),
		}, "target")),
		spec("clipboard", "Буфер обмена: read или write.", objSchema(map[string]any{
			"action": strSchema("read|write"),
			"text":   strSchema("для write"),
		})),
	}
}

// dispatchSystemTool executes one `system` tool. ok=false means "not mine".
func dispatchSystemTool(t *Task, cfg Config, name string, args map[string]any) (res ToolResult, ok bool) {
	if !systemToolNames[name] {
		return ToolResult{}, false
	}
	if !desktopSupported() {
		return failResult(desktopUnsupportedNote, nil), true
	}
	switch name {
	case "capture_screen":
		return toolCaptureScreen(t, cfg, args), true
	case "list_windows":
		return toolListWindows(), true
	case "focus_window":
		return toolFocusWindow(args), true
	case "type_keyboard":
		return toolTypeKeyboard(args), true
	case "press_keys":
		return toolPressKeys(args), true
	case "mouse_action":
		return toolMouseAction(args), true
	case "list_processes":
		return toolListProcesses(t, args), true
	case "kill_process":
		return toolKillProcess(t, args), true
	case "launch_app":
		return toolLaunchApp(args), true
	case "clipboard":
		return toolClipboard(args), true
	}
	return ToolResult{}, false
}

// ===== экран =====

func toolCaptureScreen(t *Task, cfg Config, args map[string]any) ToolResult {
	var (
		data  []byte
		rect  screenRect
		what  string
		err   error
		title = argString(args, "window")
	)
	if title != "" {
		w, others, ferr := findWindowAll(title)
		if ferr != nil {
			return failResult(ferr.Error(), ferr)
		}
		// Свёрнутое окно рисуется пустым прямоугольником — сначала разворачиваем.
		restored := false
		if w.Minimized || !w.realWindow() {
			_ = focusDesktopWindow(w)
			time.Sleep(400 * time.Millisecond)
			restored = true
			if fresh, ferr := findWindow(title); ferr == nil {
				w = fresh
			}
		}
		// Программа, свёрнутая В ТРЕЙ, окно не показывает вообще: развернуть его нечем, и
		// снимок вышел бы пустым прямоугольником, выданным за экран. Отказ здесь честнее —
		// и, главное, он объясняет, что делать. Без такого объяснения модель на живом прогоне
		// принялась «чинить» ситуацию: нажала alt+f4 и запустила программу заново.
		if !w.realWindow() {
			return failResult(fmt.Sprintf(
				"окно «%s» не показывается (%dx%d) — программа свёрнута в системный трей. "+
					"Снять его нельзя. Попроси пользователя открыть окно; закрывать и перезапускать "+
					"программу самому НЕ надо.", w.Title, w.W, w.H), nil)
		}
		data, rect, err = captureWindowPNG(w.Handle)
		// Заголовок называется ЦЕЛИКОМ, а не «окно Блокнота»: под запрос могут подойти
		// несколько окон, и человек должен видеть, какое именно попало в кадр.
		what = fmt.Sprintf("окно «%s» (%dx%d)", w.Title, w.W, w.H)
		if restored {
			what += ", было свёрнуто — развернул для снимка"
		}
		if len(others) > 0 {
			what += "; под «" + title + "» подошли ещё: " + windowTitles(others) +
				" — если нужно другое, назови заголовок точнее"
		}
	} else {
		data, rect, what, err = captureScreenPNG(int(argFloat(args, "monitor")))
	}
	if err != nil {
		return failResult("скриншот не получился: "+err.Error(), err)
	}
	path, err := saveDesktopArtifact("screen", data)
	if err != nil {
		return failResult("снял экран, но не сохранил файл: "+err.Error(), err)
	}
	// Путь идёт и в текст (модели — чтобы могла сослаться), и в артефакты (человеку — файлом).
	text := fmt.Sprintf("скриншот сделан: %s → %s (файл уже отправлен пользователю)", what, path)

	// look= — это глаза. Без него инструмент отдаёт модели только путь к файлу, и она знает
	// о содержимом экрана ровно ничего: кликать ей после такого некуда.
	if q := argString(args, "look"); q != "" {
		desc, found, verr := lookAtScreen(cfg, t.ChatID, data, rect, q)
		if verr != nil {
			text += "\n👁 Посмотреть на снимок не вышло: " + verr.Error()
		} else {
			text += "\n\n👁 " + capAgentText(desc, 3000) + renderFindings(found)
		}
	}
	return ToolResult{Status: StatusOK, Text: text, Artifacts: []string{path}}
}

// saveDesktopArtifact writes PNG bytes next to the browser artifacts so both channels can
// deliver them by the existing paths.
func saveDesktopArtifact(prefix string, data []byte) (string, error) {
	if err := os.MkdirAll(desktopArtifactDir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s.png", prefix, time.Now().Format("20060102-150405.000"))
	full := filepath.Join(desktopArtifactDir, name)
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(full)
	if err != nil {
		return full, nil
	}
	return abs, nil
}

// ===== окна =====

func toolListWindows() ToolResult {
	wins, err := listDesktopWindows()
	if err != nil {
		return failResult("не получил список окон: "+err.Error(), err)
	}
	if len(wins) == 0 {
		return okResult("видимых окон с заголовком нет", nil)
	}
	if len(wins) > 40 {
		wins = wins[:40]
	}
	var b strings.Builder
	if mons := listMonitors(); len(mons) > 0 {
		b.WriteString("Мониторы: ")
		for i, m := range mons {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%d) %dx%d в (%d,%d)", i+1, m.W, m.H, m.X, m.Y)
		}
		b.WriteString("\n")
	}
	raw, _ := json.Marshal(wins)
	b.WriteString("Окна: ")
	b.Write(raw)
	return okResult(b.String(), nil)
}

func toolFocusWindow(args map[string]any) ToolResult {
	title := argString(args, "window")
	w, err := findWindow(title)
	if err != nil {
		return failResult(err.Error(), err)
	}
	if err := focusDesktopWindow(w); err != nil {
		return failResult(err.Error(), err)
	}
	time.Sleep(300 * time.Millisecond)
	// Проверяем РЕЗУЛЬТАТ, а не факт вызова: ShowWindow и SetForegroundWindow отчитываются
	// об успехе и для программы, спрятанной в трей, — окна при этом на экране так и нет.
	// Молчаливое «окно на переднем плане» в такой ситуации отправляет модель кликать в пустоту.
	if fresh, ferr := findWindow(title); ferr == nil {
		if !fresh.realWindow() {
			return failResult(fmt.Sprintf(
				"окно «%s» на экране так и не появилось (%dx%d) — программа свёрнута в системный трей. "+
					"Попроси пользователя открыть её; закрывать и перезапускать программу самому НЕ надо.",
				fresh.Title, fresh.W, fresh.H), nil)
		}
		w = fresh
	}
	return okResult(fmt.Sprintf("окно «%s» на переднем плане (%dx%d в точке %d,%d)",
		w.Title, w.W, w.H, w.X, w.Y), nil)
}

// realWindowArea отделяет настоящее окно от всплывашки. Уведомление Telegram — 320×80 = 25 600
// пикселей; свёрнутое окно в списке отдаётся как 160×28. Порог 100 000 (примерно 400×250)
// проходит любое рабочее окно и не проходит ни одно служебное.
const realWindowArea = 100_000

// realWindow reports whether this is a working window rather than a popup.
// Свёрнутые окна Windows отдаёт размером 160×28, поэтому они тоже «не настоящие» — и это
// правильно: свёрнутое окно проигрывает развёрнутому и по этому признаку тоже.
func (w desktopWindow) realWindow() bool { return w.W*w.H >= realWindowArea }

// pickWindow orders candidate windows for a title query and returns the best plus the rest.
//
// Живёт отдельно от WinAPI намеренно: это чистая функция, и именно её порядок предпочтений
// оказался неправильным на живом прогоне — «скриншот окна Блокнота» выбрал давний свёрнутый
// документ пользователя вместо только что открытого агентом окна.
func pickWindow(hits []desktopWindow, match string) (desktopWindow, []desktopWindow) {
	if len(hits) == 0 {
		return desktopWindow{}, nil
	}
	sorted := append([]desktopWindow{}, hits...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		// 1) настоящее окно бьёт всплывашку — и это выше даже точного совпадения заголовка.
		//    Живой случай: всплывающее уведомление Telegram названо ровно «Telegram» и приходит
		//    активным, а главное окно называется именем открытого чата. По заголовку и по
		//    активности выигрывало окошко 320×80 — снимок «окна Telegram» уезжал на него;
		if sa, sb := a.realWindow(), b.realWindow(); sa != sb {
			return sa
		}
		// 2) точное совпадение заголовка бьёт частичное;
		if ea, eb := strings.EqualFold(a.Title, match), strings.EqualFold(b.Title, match); ea != eb {
			return ea
		}
		// 3) активное окно — то, о котором человек скорее всего и говорит;
		if a.Active != b.Active {
			return a.Active
		}
		// 4) развёрнутое бьёт свёрнутое: снимок свёрнутого требует его развернуть, то есть
		//    полезть в чужой рабочий стол ради снимка;
		if a.Minimized != b.Minimized {
			return !a.Minimized
		}
		// 5) при прочих равных — большее по площади (совпадение с окном 1×1 бесполезно).
		return a.W*a.H > b.W*b.H
	})
	return sorted[0], sorted[1:]
}

// windowTitles renders a short list of window titles for a disambiguation hint.
func windowTitles(wins []desktopWindow) string {
	if len(wins) == 0 {
		return "(окон с заголовком нет)"
	}
	const maxShown = 12
	parts := make([]string, 0, len(wins))
	for i, w := range wins {
		if i == maxShown {
			parts = append(parts, fmt.Sprintf("и ещё %d", len(wins)-maxShown))
			break
		}
		parts = append(parts, "«"+w.Title+"»")
	}
	return strings.Join(parts, ", ")
}

// focusIfAsked brings a window forward before typing/clicking into it, so the model can do
// «напиши в блокнот» in one call instead of two.
func focusIfAsked(args map[string]any) error {
	title := argString(args, "window")
	if title == "" {
		return nil
	}
	w, err := findWindow(title)
	if err != nil {
		return err
	}
	if err := focusDesktopWindow(w); err != nil {
		return err
	}
	time.Sleep(200 * time.Millisecond)
	return nil
}

// targetWindowNote names the window that is about to receive keyboard or mouse input.
//
// Ввод всегда уходит в АКТИВНОЕ окно, и до этой строчки ни модель, ни человек не знали, в
// какое именно. «Напечатал 30 символов» — бесполезный отчёт, если буквы уехали в чужой чат;
// «напечатал 30 символов в окно "Безымянный – Блокнот"» проверяем сразу.
func targetWindowNote() string {
	w := foregroundWindow()
	if strings.TrimSpace(w.Title) == "" {
		return " (активное окно определить не удалось)"
	}
	return " в окно «" + w.Title + "»"
}

// ===== клавиатура и мышь =====

func toolTypeKeyboard(args map[string]any) ToolResult {
	text := argString(args, "text")
	if text == "" {
		return failResult("нужен text", nil)
	}
	if err := focusIfAsked(args); err != nil {
		return failResult(err.Error(), err)
	}
	where := targetWindowNote()
	if err := typeUnicodeText(text); err != nil {
		return failResult("не напечатал: "+err.Error(), err)
	}
	return okResult(fmt.Sprintf("напечатал %d символов%s", len([]rune(text)), where), nil)
}

func toolPressKeys(args map[string]any) ToolResult {
	keys := argString(args, "keys")
	if keys == "" {
		return failResult("нужно сочетание, например ctrl+c или enter", nil)
	}
	if err := focusIfAsked(args); err != nil {
		return failResult(err.Error(), err)
	}
	where := targetWindowNote()
	// Несколько сочетаний подряд пишутся через запятую: «win+r, enter».
	for _, combo := range strings.Split(keys, ",") {
		combo = strings.TrimSpace(combo)
		if combo == "" {
			continue
		}
		if err := pressKeyCombo(combo); err != nil {
			return failResult(err.Error(), err)
		}
		time.Sleep(60 * time.Millisecond)
	}
	return okResult("нажал: "+keys+where, nil)
}

func toolMouseAction(args map[string]any) ToolResult {
	action := strings.ToLower(argString(args, "action"))
	if action == "" {
		action = "click"
	}
	hasX, hasY := args["x"] != nil, args["y"] != nil
	if hasX && hasY {
		if err := moveMouse(int(argFloat(args, "x")), int(argFloat(args, "y"))); err != nil {
			return failResult(err.Error(), err)
		}
		time.Sleep(40 * time.Millisecond)
	}
	x, y := cursorPos()
	switch action {
	case "move", "переместить":
		if !hasX || !hasY {
			return failResult("для move нужны x и y", nil)
		}
		return okResult(fmt.Sprintf("курсор в (%d,%d)", x, y), nil)
	case "click", "клик", "left":
		return mouseResult(clickMouse("left", false), "клик", x, y)
	case "double", "двойной", "dblclick":
		return mouseResult(clickMouse("left", true), "двойной клик", x, y)
	case "right", "правой", "rightclick":
		return mouseResult(clickMouse("right", false), "правый клик", x, y)
	case "middle":
		return mouseResult(clickMouse("middle", false), "средний клик", x, y)
	case "scroll", "прокрутка":
		amount := int(argFloat(args, "amount"))
		if amount == 0 {
			amount = -3
		}
		if err := scrollMouse(amount); err != nil {
			return failResult(err.Error(), err)
		}
		return okResult(fmt.Sprintf("прокрутил на %d делений в (%d,%d)", amount, x, y), nil)
	}
	return failResult("не знаю действие «"+action+"» (move|click|double|right|middle|scroll)", nil)
}

func mouseResult(err error, what string, x, y int) ToolResult {
	if err != nil {
		return failResult(err.Error(), err)
	}
	return okResult(fmt.Sprintf("%s в (%d,%d)", what, x, y), nil)
}

// ===== процессы =====

func toolListProcesses(t *Task, args map[string]any) ToolResult {
	limit := int(argFloat(args, "limit"))
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	name := argString(args, "name")
	var cmd string
	if runtime.GOOS == "windows" {
		filter := ""
		if name != "" {
			filter = fmt.Sprintf("-Name '*%s*' -ErrorAction SilentlyContinue ", strings.ReplaceAll(name, "'", "''"))
		}
		cmd = fmt.Sprintf("Get-Process %s| Sort-Object -Property WS -Descending | Select-Object -First %d "+
			"Id, ProcessName, @{n='MB';e={[math]::Round($_.WS/1MB,1)}} | ConvertTo-Json -Compress", filter, limit)
	} else {
		cmd = "ps -eo pid,rss,comm --sort=-rss | head -n " + strconv.Itoa(limit+1)
		if name != "" {
			cmd = "ps -eo pid,rss,comm | grep -i " + strconv.Quote(name) + " | head -n " + strconv.Itoa(limit)
		}
	}
	out, err := browser.RunCommand(t.Context(), cmd, "", 60)
	if err != nil {
		return classifyToolErr(t.Context(), out, err)
	}
	return okResult(out, nil)
}

// criticalProcesses kill Windows outright — losing lsass or csrss is an instant BSOD, and
// «заверши процесс» said about them is far more likely a mistake than an intention.
var criticalProcesses = map[string]bool{
	"csrss": true, "wininit": true, "winlogon": true, "services": true,
	"smss": true, "lsass": true, "system": true, "systemd": true, "init": true,
}

func toolKillProcess(t *Task, args map[string]any) ToolResult {
	pid := int(argFloat(args, "pid"))
	name := strings.TrimSpace(argString(args, "name"))
	if pid <= 0 && name == "" {
		return failResult("нужен pid или name", nil)
	}
	// Defense in depth: never kill by PID under a mismatched/spoofed name.
	if pid > 0 {
		if resolved := lookupProcessName(pid); resolved != "" {
			want := strings.ToLower(strings.TrimSuffix(name, ".exe"))
			got := strings.ToLower(strings.TrimSuffix(resolved, ".exe"))
			if want != "" && want != got {
				return failResult(fmt.Sprintf("имя «%s» не совпадает с pid %d (%s)", name, pid, resolved), nil)
			}
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			return failResult(fmt.Sprintf("процесс %d не найден: %v", pid, err), err)
		}
		if err := proc.Kill(); err != nil {
			return failResult(fmt.Sprintf("не смог завершить %d: %v (нужны права администратора?)", pid, err), err)
		}
		return okResult(fmt.Sprintf("процесс %d завершён", pid), nil)
	}
	var cmd string
	if runtime.GOOS == "windows" {
		exe := name
		if !strings.HasSuffix(strings.ToLower(exe), ".exe") {
			exe += ".exe"
		}
		cmd = "taskkill /IM " + strconv.Quote(exe) + " /F /T"
	} else {
		cmd = "pkill -f " + strconv.Quote(name)
	}
	out, err := browser.RunCommand(t.Context(), cmd, "", 60)
	if err != nil {
		return classifyToolErr(t.Context(), out, err)
	}
	return okResult(out, nil)
}

// ===== запуск программ =====

// toolLaunchApp starts a program, document, folder or URL and returns immediately.
//
// Процесс НЕ привязан к контексту задачи: пользователь просил «открой блокнот», а не «открой
// блокнот на десять минут» — приложение должно пережить конец задачи.
func toolLaunchApp(args map[string]any) ToolResult {
	target := argString(args, "target")
	if target == "" {
		return failResult("нужен target: путь к программе, файлу, папке или URL", nil)
	}
	extra := splitLaunchArgs(argString(args, "args"))

	direct := exec.Command(target, extra...)
	if err := direct.Start(); err == nil {
		pid := direct.Process.Pid
		_ = direct.Process.Release()
		return okResult(fmt.Sprintf("запустил %s (pid %d)%s", target, pid, launchedWindowNote(pid)), nil)
	}

	// Не исполняемый файл (документ, папка, https://…) — открываем ассоциацией системы.
	var assoc *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		assoc = exec.Command("cmd", append([]string{"/c", "start", "", target}, extra...)...)
	case "darwin":
		assoc = exec.Command("open", append([]string{target}, extra...)...)
	default:
		assoc = exec.Command("xdg-open", target)
	}
	if err := assoc.Start(); err != nil {
		return failResult(fmt.Sprintf("не запустил %q: %v", target, err), err)
	}
	_ = assoc.Process.Release()
	return okResult("открыл через систему: "+target, nil)
}

// launchWindowWait — сколько ждём появления окна запущенной программы.
const launchWindowWait = 6 * time.Second

// launchedWindowNote waits for the new program's window and names it.
//
// Это не косметика. launch_app возвращается через миллисекунды, а окно появляется через
// сотни, и следующий же шаг «напиши туда текст» отправлял буквы в то окно, которое было в
// фокусе ДО запуска. На живом прогоне «открой блокнот и напиши…» текст ушёл мимо блокнота,
// а отчёт всё равно был «текст введён». Теперь launch_app ждёт окно и называет его — и
// модель, и человек видят, во что дальше поедет ввод.
func launchedWindowNote(pid int) string {
	w, ok := waitWindowOfProcess(pid, launchWindowWait)
	if !ok {
		return "; окно за " + launchWindowWait.String() + " не появилось — прежде чем печатать, " +
			"проверь list_windows: ввод уйдёт в то окно, которое сейчас активно"
	}
	// Окно есть, но фокус Windows отдаёт не мгновенно — дожидаемся, иначе первые символы
	// улетят в предыдущее активное окно.
	_ = focusDesktopWindow(w)
	time.Sleep(250 * time.Millisecond)
	return "; окно «" + w.Title + "» открыто и в фокусе"
}

// splitLaunchArgs splits an argument line, honouring "quoted paths with spaces".
func splitLaunchArgs(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	quote := rune(0)
	for _, r := range line {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '"' || r == '\''):
			quote = r
		case quote == 0 && (r == ' ' || r == '\t'):
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// ===== буфер обмена =====
//
// Через PowerShell, а не через WinAPI-функции буфера: OpenClipboard/GlobalLock возвращают
// uintptr, и обратное превращение его в указатель — та самая «possible misuse of
// unsafe.Pointer», на которую справедливо ругается `go vet`. Обходить проверку молча в коде,
// который лезет в ядро, не стоит, а 300 мс на буфер обмена никого не спасают и не губят.
//
// Кодировка задаётся явно в обе стороны: без `[Console]::OutputEncoding = UTF8` PowerShell
// отдаёт stdout в кодовой странице консоли (на русской Windows — 866), и кириллица приезжает
// кракозябрами. На запись — временный файл с UTF-8: текст может содержать кавычки и переносы
// строк, а безопасно пронести такое через `powershell -Command` нельзя.

func readClipboardText() (string, error) {
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("буфер обмена читаю только на Windows")
	}
	out, err := runPowerShell(`[Console]::OutputEncoding=[Text.Encoding]::UTF8; Get-Clipboard -Raw`)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\r\n"), nil
}

func writeClipboardText(text string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("буфер обмена пишу только на Windows")
	}
	tmp, err := os.CreateTemp("", "kib-clip-*.txt")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	// BOM: Get-Content -Encoding UTF8 в Windows PowerShell 5.1 распознаёт UTF-8 именно по нему.
	if _, err := tmp.Write(append([]byte{0xEF, 0xBB, 0xBF}, []byte(text)...)); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	_, err = runPowerShell(`Set-Clipboard -Value (Get-Content -Raw -Encoding UTF8 -LiteralPath ` +
		powerShellQuote(name) + `)`)
	return err
}

// runPowerShell executes a script and returns raw stdout (no exit=/--- stdout --- обвязки,
// в отличие от browser.RunCommand: содержимое буфера обмена должно приехать как есть).
func runPowerShell(script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return stdout.String(), nil
}

// powerShellQuote wraps a path in single quotes, doubling any inside — PowerShell's own escape.
func powerShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func toolClipboard(args map[string]any) ToolResult {
	action := strings.ToLower(argString(args, "action"))
	text := argString(args, "text")
	if action == "" {
		if text != "" {
			action = "write"
		} else {
			action = "read"
		}
	}
	switch action {
	case "read", "get", "чтение", "прочитать":
		got, err := readClipboardText()
		if err != nil {
			return failResult("не прочитал буфер обмена: "+err.Error(), err)
		}
		if got == "" {
			return okResult("буфер обмена пуст (или в нём не текст)", nil)
		}
		return okResult("в буфере обмена:\n"+capAgentText(got, agentMaxToolChars), nil)
	case "write", "set", "запись", "записать":
		if text == "" {
			return failResult("нужен text для записи в буфер", nil)
		}
		if err := writeClipboardText(text); err != nil {
			return failResult("не записал в буфер обмена: "+err.Error(), err)
		}
		return okResult(fmt.Sprintf("положил в буфер обмена %d символов", len([]rune(text))), nil)
	}
	return failResult("action должен быть read или write", nil)
}
