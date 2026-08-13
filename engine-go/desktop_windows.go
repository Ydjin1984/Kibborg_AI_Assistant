//go:build windows

package main

// Рабочий стол Windows напрямую через WinAPI (ТЗ §5, пак `system`).
//
// Зачем не PowerShell: скриншот через `Add-Type System.Drawing` стоит ~700 мс на запуск
// PowerShell, ломается при политике выполнения и молча отдаёт пустой PNG на заблокированном
// экране. Здесь — те же GDI-вызовы, но из процесса агента: быстрее, без внешних зависимостей
// и с честной ошибкой вместо пустого файла.
//
// Ввод (клавиатура/мышь) идёт через SendInput, а не через SendKeys: SendKeys не умеет
// кириллицу, а KEYEVENTF_UNICODE умеет любой символ из UTF-16, включая эмодзи.

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procGetDC                    = user32.NewProc("GetDC")
	procReleaseDC                = user32.NewProc("ReleaseDC")
	procGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procEnumDisplayMonitors      = user32.NewProc("EnumDisplayMonitors")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW     = user32.NewProc("GetWindowTextLengthW")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procIsIconic                 = user32.NewProc("IsIconic")
	procGetWindowRect            = user32.NewProc("GetWindowRect")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procShowWindow               = user32.NewProc("ShowWindow")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procSetCursorPos             = user32.NewProc("SetCursorPos")
	procGetCursorPos             = user32.NewProc("GetCursorPos")
	procSendInput                = user32.NewProc("SendInput")
	procPrintWindow              = user32.NewProc("PrintWindow")
	procSetProcessDPIAware       = user32.NewProc("SetProcessDPIAware")

	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procBitBlt             = gdi32.NewProc("BitBlt")
	procGdiFlush           = gdi32.NewProc("GdiFlush")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procDeleteDC           = gdi32.NewProc("DeleteDC")

	procOpenProcess               = kernel32.NewProc("OpenProcess")
	procCloseHandle               = kernel32.NewProc("CloseHandle")
	procQueryFullProcessImageName = kernel32.NewProc("QueryFullProcessImageNameW")
)

const (
	smCXScreen        = 0
	smCYScreen        = 1
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	srcCopy      = 0x00CC0020
	captureBlt   = 0x40000000
	biRGB        = 0
	dibRGBColors = 0

	swRestore = 9
	swShow    = 5

	pwRenderFullContent = 0x00000002

	inputMouse    = 0
	inputKeyboard = 1

	keyEventKeyUp   = 0x0002
	keyEventUnicode = 0x0004

	mouseEventMove       = 0x0001
	mouseEventLeftDown   = 0x0002
	mouseEventLeftUp     = 0x0004
	mouseEventRightDown  = 0x0008
	mouseEventRightUp    = 0x0010
	mouseEventMiddleDown = 0x0020
	mouseEventMiddleUp   = 0x0040
	mouseEventWheel      = 0x0800
	mouseEventAbsolute   = 0x8000

	processQueryLimitedInformation = 0x1000
)

// dpiOnce makes the process DPI-aware exactly once. Without it GetSystemMetrics reports the
// SCALED resolution on a 125%/150% display, and every screenshot comes back cropped — the
// classic "скриншот обрезан справа и снизу" bug.
var dpiOnce sync.Once

func makeDPIAware() {
	dpiOnce.Do(func() {
		if procSetProcessDPIAware.Find() == nil {
			_, _, _ = procSetProcessDPIAware.Call()
		}
	})
}

// ===== скриншот =====

type screenRect struct {
	X, Y, W, H int
}

func systemMetric(index int) int {
	v, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int(int32(v))
}

// virtualScreenRect is the bounding box of ALL monitors (may start at negative X/Y when a
// second display sits to the left of the primary one).
func virtualScreenRect() screenRect {
	makeDPIAware()
	r := screenRect{
		X: systemMetric(smXVirtualScreen),
		Y: systemMetric(smYVirtualScreen),
		W: systemMetric(smCXVirtualScreen),
		H: systemMetric(smCYVirtualScreen),
	}
	if r.W <= 0 || r.H <= 0 { // single-monitor fallback
		r = screenRect{0, 0, systemMetric(smCXScreen), systemMetric(smCYScreen)}
	}
	return r
}

type winRect struct{ Left, Top, Right, Bottom int32 }

// monitorInfo mirrors Win32 MONITORINFO.
type monitorInfo struct {
	Size    uint32
	Monitor winRect
	Work    winRect
	Flags   uint32
}

var (
	monitorMu  sync.Mutex
	monitorAcc []screenRect
	monitorCB  = syscall.NewCallback(monitorEnumProc)

	procGetMonitorInfo = user32.NewProc("GetMonitorInfoW")
)

// monitorEnumProc reads the display rect through GetMonitorInfoW rather than dereferencing the
// callback's LPRECT: превращение uintptr обратно в указатель — ровно то, на что справедливо
// ругается `go vet` (unsafeptr), и обходить его молча в коде, который ходит в ядро, не стоит.
func monitorEnumProc(hMonitor, hdc, lprc, data uintptr) uintptr {
	var mi monitorInfo
	mi.Size = uint32(unsafe.Sizeof(mi))
	if ok, _, _ := procGetMonitorInfo.Call(hMonitor, uintptr(unsafe.Pointer(&mi))); ok == 0 {
		return 1
	}
	r := mi.Monitor
	monitorAcc = append(monitorAcc, screenRect{
		X: int(r.Left), Y: int(r.Top),
		W: int(r.Right - r.Left), H: int(r.Bottom - r.Top),
	})
	return 1
}

// listMonitors returns every display, ordered left-to-right so "монитор 1" is stable.
func listMonitors() []screenRect {
	makeDPIAware()
	monitorMu.Lock()
	defer monitorMu.Unlock()
	monitorAcc = nil
	_, _, _ = procEnumDisplayMonitors.Call(0, 0, monitorCB, 0)
	out := append([]screenRect{}, monitorAcc...)
	monitorAcc = nil
	if len(out) == 0 {
		out = append(out, virtualScreenRect())
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].X != out[j].X {
			return out[i].X < out[j].X
		}
		return out[i].Y < out[j].Y
	})
	return out
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [3]uint32
}

// capturePNG grabs a rectangle of the screen (or, when hwnd != 0, a whole window) and encodes
// it to PNG bytes.
//
// Пиксели берутся из DIB-секции, а не через GetDIBits. Причина измерена, а не выдумана:
// на этой машине GetDIBits отдаёт строки только для областей примерно до мегабайта, а на
// 3440×1440 возвращает 0 — БЕЗ кода ошибки (GetLastError говорит «операция выполнена
// успешно»). Молчаливый ноль на полном экране и работающий снимок на 500×500 — ровно тот
// сорт бага, который проходит ручную проверку «ну работает же». CreateDIBSection отдаёт
// память под пиксели сам, BitBlt пишет прямо в неё, и лишнего копирования тоже не остаётся.
func capturePNG(rect screenRect, hwnd uintptr) ([]byte, error) {
	makeDPIAware()
	if rect.W <= 0 || rect.H <= 0 {
		return nil, fmt.Errorf("нулевой размер области (%dx%d)", rect.W, rect.H)
	}
	if rect.W*rect.H > 80_000_000 {
		return nil, fmt.Errorf("слишком большая область: %dx%d", rect.W, rect.H)
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("GetDC(0) не дал контекст экрана")
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC не удался")
	}
	defer procDeleteDC.Call(memDC)

	// Отрицательная высота = top-down DIB: первая строка памяти — верхняя строка картинки.
	bi := bitmapInfo{Header: bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       int32(rect.W),
		Height:      int32(-rect.H),
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
	}}
	// bits объявлен как unsafe.Pointer, а не uintptr: GDI пишет адрес прямо в переменную
	// указательного типа, и обратного превращения числа в указатель (на которое справедливо
	// ругается `go vet`) в нашем коде не возникает вовсе.
	var bits unsafe.Pointer
	bmp, _, err := procCreateDIBSection.Call(memDC, uintptr(unsafe.Pointer(&bi)), dibRGBColors,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		return nil, fmt.Errorf("CreateDIBSection не удался (%dx%d): %v", rect.W, rect.H, err)
	}
	defer procDeleteObject.Call(bmp)

	old, _, _ := procSelectObject.Call(memDC, bmp)
	defer procSelectObject.Call(memDC, old)

	if hwnd != 0 {
		// PW_RENDERFULLCONTENT рисует и перекрытое окно (иначе поверх ляжет то, что сверху).
		ok, _, _ := procPrintWindow.Call(hwnd, memDC, pwRenderFullContent)
		if ok == 0 {
			// Не все окна умеют PrintWindow (например, аппаратно ускоренные) — падаем на BitBlt.
			ok, _, _ = procBitBlt.Call(memDC, 0, 0, uintptr(rect.W), uintptr(rect.H),
				screenDC, uintptr(int32(rect.X)), uintptr(int32(rect.Y)), srcCopy|captureBlt)
			if ok == 0 {
				return nil, fmt.Errorf("не смог снять окно (PrintWindow и BitBlt отказали)")
			}
		}
	} else {
		ok, _, _ := procBitBlt.Call(memDC, 0, 0, uintptr(rect.W), uintptr(rect.H),
			screenDC, uintptr(int32(rect.X)), uintptr(int32(rect.Y)), srcCopy|captureBlt)
		if ok == 0 {
			return nil, fmt.Errorf("BitBlt не скопировал область экрана")
		}
	}

	// GDI копит вызовы в пакете на поток: без явного сброса BitBlt может ещё не выполниться
	// к моменту, когда мы читаем пиксели прямо из памяти секции.
	procGdiFlush.Call()

	buf := unsafe.Slice((*byte)(bits), rect.W*rect.H*4)
	img := image.NewRGBA(image.Rect(0, 0, rect.W, rect.H))
	for i := 0; i < rect.W*rect.H; i++ {
		// GDI отдаёт BGRA (и A=0 у обычных окон) — переставляем каналы и делаем непрозрачным.
		b, g, r := buf[i*4], buf[i*4+1], buf[i*4+2]
		img.Pix[i*4] = r
		img.Pix[i*4+1] = g
		img.Pix[i*4+2] = b
		img.Pix[i*4+3] = 255
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, fmt.Errorf("не закодировал PNG: %w", err)
	}
	return out.Bytes(), nil
}

// captureScreenPNG snaps the whole virtual desktop (monitor <= 0) or one display (1-based).
// Прямоугольник возвращается наружу вместе с картинкой: без него нельзя перевести
// координаты, найденные зрением НА СНИМКЕ, в координаты экрана для мыши.
func captureScreenPNG(monitor int) ([]byte, screenRect, string, error) {
	if monitor <= 0 {
		r := virtualScreenRect()
		png, err := capturePNG(r, 0)
		return png, r, fmt.Sprintf("весь рабочий стол %dx%d", r.W, r.H), err
	}
	mons := listMonitors()
	if monitor > len(mons) {
		return nil, screenRect{}, "", fmt.Errorf("монитор %d не найден, их всего %d", monitor, len(mons))
	}
	r := mons[monitor-1]
	png, err := capturePNG(r, 0)
	return png, r, fmt.Sprintf("монитор %d (%dx%d)", monitor, r.W, r.H), err
}

// captureWindowPNG snaps one window by handle and reports where on screen it sits.
func captureWindowPNG(hwnd uintptr) ([]byte, screenRect, error) {
	var r winRect
	ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if ok == 0 {
		return nil, screenRect{}, fmt.Errorf("не получил размеры окна")
	}
	rect := screenRect{
		X: int(r.Left), Y: int(r.Top),
		W: int(r.Right - r.Left), H: int(r.Bottom - r.Top),
	}
	png, err := capturePNG(rect, hwnd)
	return png, rect, err
}

// ===== окна =====

type desktopWindow struct {
	Handle    uintptr `json:"-"`
	HandleS   string  `json:"handle"`
	Title     string  `json:"title"`
	PID       int     `json:"pid"`
	Exe       string  `json:"exe,omitempty"`
	X         int     `json:"x"`
	Y         int     `json:"y"`
	W         int     `json:"w"`
	H         int     `json:"h"`
	Minimized bool    `json:"minimized,omitempty"`
	Active    bool    `json:"active,omitempty"`
}

var (
	windowMu  sync.Mutex
	windowAcc []desktopWindow
	windowCB  = syscall.NewCallback(windowEnumProc)
)

func windowEnumProc(hwnd, lparam uintptr) uintptr {
	visible, _, _ := procIsWindowVisible.Call(hwnd)
	if visible == 0 {
		return 1
	}
	n, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if n == 0 {
		return 1 // окно без заголовка — служебное, человеку оно ничего не говорит
	}
	buf := make([]uint16, int(n)+2)
	_, _, _ = procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	title := syscall.UTF16ToString(buf)
	if strings.TrimSpace(title) == "" {
		return 1
	}
	var pid uint32
	_, _, _ = procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	var r winRect
	_, _, _ = procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	iconic, _, _ := procIsIconic.Call(hwnd)

	windowAcc = append(windowAcc, desktopWindow{
		Handle:    hwnd,
		HandleS:   fmt.Sprintf("0x%X", hwnd),
		Title:     title,
		PID:       int(pid),
		Exe:       processExeName(pid),
		X:         int(r.Left),
		Y:         int(r.Top),
		W:         int(r.Right - r.Left),
		H:         int(r.Bottom - r.Top),
		Minimized: iconic != 0,
	})
	return 1
}

// listDesktopWindows enumerates visible titled top-level windows.
func listDesktopWindows() ([]desktopWindow, error) {
	windowMu.Lock()
	defer windowMu.Unlock()
	windowAcc = nil
	_, _, _ = procEnumWindows.Call(windowCB, 0)
	out := append([]desktopWindow{}, windowAcc...)
	windowAcc = nil
	fg, _, _ := procGetForegroundWindow.Call()
	for i := range out {
		out[i].Active = out[i].Handle == fg
	}
	return out, nil
}

// findWindow resolves a window by title substring (case-insensitive) or by "0x…" handle.
// Returns the best match AND every other candidate, so the caller can say out loud that the
// request was ambiguous instead of silently picking one.
//
// Неоднозначность здесь не теоретическая. На живом прогоне «сделай скриншот окна Блокнота»
// совпало сразу с двумя блокнотами — открытым агентом пустым и давним свёрнутым документом
// пользователя, — и снимок уехал не туда, а отчёт всё равно был бодрый: «скриншот сделан».
// Поэтому порядок предпочтений явный, а остальные кандидаты возвращаются наверх.
func findWindowAll(match string) (desktopWindow, []desktopWindow, error) {
	match = strings.TrimSpace(match)
	if match == "" {
		return desktopWindow{}, nil, fmt.Errorf("нужен заголовок окна или его handle")
	}
	wins, err := listDesktopWindows()
	if err != nil {
		return desktopWindow{}, nil, err
	}
	var hits []desktopWindow
	lower := strings.ToLower(match)
	for _, w := range wins {
		// Совпадение ищется и по ИМЕНИ ПРОГРАММЫ, а не только по заголовку. Иначе «окно
		// Telegram» не находится вообще: Telegram Desktop пишет в заголовок имя открытого чата
		// («(1) Иван Петров»), а слово «Telegram» носят только всплывающие уведомления 320×80 —
		// и снимок «окна Telegram» приезжал именно с них.
		exe := strings.TrimSuffix(strings.ToLower(w.Exe), ".exe")
		if strings.EqualFold(w.HandleS, match) ||
			strings.Contains(strings.ToLower(w.Title), lower) ||
			(exe != "" && strings.Contains(exe, lower)) {
			hits = append(hits, w)
		}
	}
	if len(hits) == 0 {
		// Список открытых окон кладётся прямо в ошибку. Без него модель на живом прогоне
		// потратила лишний круг: получила «не найдено», позвала list_windows, а потом на всякий
		// случай запустила Блокнот ЕЩЁ раз — и оставила на рабочем столе лишнее окно.
		return desktopWindow{}, nil, fmt.Errorf("окно %q не найдено. Сейчас открыты: %s. "+
			"Возьми заголовок из этого списка (годится любая его часть)", match, windowTitles(wins))
	}
	best, others := pickWindow(hits, match)
	return best, others, nil
}

// findWindow is findWindowAll without the alternatives, for callers that do not report them.
func findWindow(match string) (desktopWindow, error) {
	w, _, err := findWindowAll(match)
	return w, err
}

// foregroundWindow returns the window that currently owns the keyboard focus.
func foregroundWindow() desktopWindow {
	fg, _, _ := procGetForegroundWindow.Call()
	if fg == 0 {
		return desktopWindow{}
	}
	wins, err := listDesktopWindows()
	if err != nil {
		return desktopWindow{}
	}
	for _, w := range wins {
		if w.Handle == fg {
			return w
		}
	}
	return desktopWindow{}
}

// waitWindowOfProcess blocks until a freshly started process shows a titled top-level window.
//
// Без этого «запусти блокнот и напиши туда текст» печатает вслепую: launch_app возвращается
// через миллисекунды, а окно Блокнота появляется через сотни, и SendInput отправляет буквы в
// то окно, которое было в фокусе ДО запуска — то есть в чужой документ, чат или терминал.
// Именно так и вышло на живом прогоне.
func waitWindowOfProcess(pid int, timeout time.Duration) (desktopWindow, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		wins, err := listDesktopWindows()
		if err == nil {
			for _, w := range wins {
				if w.PID == pid {
					return w, true
				}
			}
		}
		time.Sleep(80 * time.Millisecond)
	}
	return desktopWindow{}, false
}

// focusDesktopWindow restores and raises a window.
//
// SetForegroundWindow отказывает, когда вызывающий процесс сам не на переднем плане (защита
// Windows от кражи фокуса). Обходной путь тот же, что у всех автоматизаторов: «пошевелить»
// клавиатурой (ALT вверх-вниз) и повторить.
func focusDesktopWindow(w desktopWindow) error {
	if w.Minimized {
		_, _, _ = procShowWindow.Call(w.Handle, swRestore)
	} else {
		_, _, _ = procShowWindow.Call(w.Handle, swShow)
	}
	ok, _, _ := procSetForegroundWindow.Call(w.Handle)
	if ok != 0 {
		return nil
	}
	const vkMenu = 0x12 // ALT
	sendKeyEvents([]keyEvent{{vk: vkMenu}, {vk: vkMenu, up: true}})
	time.Sleep(30 * time.Millisecond)
	if ok, _, _ = procSetForegroundWindow.Call(w.Handle); ok == 0 {
		return fmt.Errorf("система не отдала фокус окну %q. %s", w.Title, blockedInputHint())
	}
	return nil
}

func processExeName(pid uint32) string {
	if pid == 0 {
		return ""
	}
	h, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer procCloseHandle.Call(h)
	buf := make([]uint16, 260)
	size := uint32(len(buf))
	ok, _, _ := procQueryFullProcessImageName.Call(h, 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		return ""
	}
	full := syscall.UTF16ToString(buf[:size])
	if i := strings.LastIndexAny(full, `\/`); i >= 0 {
		return full[i+1:]
	}
	return full
}

// ===== ввод: клавиатура =====

// inputRecord mirrors Win32 INPUT. The union is padded to the size of MOUSEINPUT, so one
// fixed-size struct serves both branches (sizeof(INPUT) == 40 on amd64).
type inputRecord struct {
	Type uint32
	_    uint32
	Data [32]byte
}

type keybdInput struct {
	Vk        uint16
	Scan      uint16
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

type mouseInput struct {
	DX        int32
	DY        int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

type keyEvent struct {
	vk      uint16
	unicode rune
	up      bool
}

func sendInputs(recs []inputRecord) error {
	if len(recs) == 0 {
		return nil
	}
	n, _, err := procSendInput.Call(uintptr(len(recs)),
		uintptr(unsafe.Pointer(&recs[0])), unsafe.Sizeof(recs[0]))
	if int(n) != len(recs) {
		return fmt.Errorf("SendInput отправил %d из %d событий: %v", n, len(recs), err)
	}
	return nil
}

func sendKeyEvents(events []keyEvent) error {
	recs := make([]inputRecord, 0, len(events))
	for _, e := range events {
		ki := keybdInput{Vk: e.vk}
		if e.unicode != 0 {
			ki.Vk = 0
			ki.Scan = uint16(e.unicode)
			ki.Flags |= keyEventUnicode
		}
		if e.up {
			ki.Flags |= keyEventKeyUp
		}
		rec := inputRecord{Type: inputKeyboard}
		*(*keybdInput)(unsafe.Pointer(&rec.Data[0])) = ki
		recs = append(recs, rec)
	}
	return sendInputs(recs)
}

// typeUnicodeText types arbitrary text (Cyrillic included) into the focused window.
// Surrogate pairs are sent as two UTF-16 units, which is exactly what KEYEVENTF_UNICODE wants.
func typeUnicodeText(text string) error {
	units, err := syscall.UTF16FromString(text)
	if err != nil {
		return fmt.Errorf("текст содержит нулевой байт — так печатать нельзя")
	}
	units = units[:len(units)-1] // без NUL-терминатора
	if len(units) == 0 {
		return fmt.Errorf("пустой текст")
	}
	if len(units) > 20000 {
		return fmt.Errorf("слишком длинный текст (%d символов); пиши в файл, а не через клавиатуру", len(units))
	}
	var events []keyEvent
	for _, u := range units {
		if u == '\n' {
			events = append(events, keyEvent{vk: vkByName["enter"]}, keyEvent{vk: vkByName["enter"], up: true})
			continue
		}
		events = append(events, keyEvent{unicode: rune(u)}, keyEvent{unicode: rune(u), up: true})
	}
	// Windows теряет события, если вывалить 40 000 штук одним SendInput — шлём порциями.
	const chunk = 200
	for i := 0; i < len(events); i += chunk {
		end := i + chunk
		if end > len(events) {
			end = len(events)
		}
		if err := sendKeyEvents(events[i:end]); err != nil {
			return err
		}
		time.Sleep(2 * time.Millisecond)
	}
	return nil
}

// vkByName maps the key names a model actually writes onto virtual-key codes.
var vkByName = map[string]uint16{
	"ctrl": 0x11, "control": 0x11, "alt": 0x12, "shift": 0x10,
	"win": 0x5B, "windows": 0x5B, "cmd": 0x5B, "meta": 0x5B,
	"enter": 0x0D, "return": 0x0D, "tab": 0x09, "esc": 0x1B, "escape": 0x1B,
	"space": 0x20, "backspace": 0x08, "back": 0x08, "delete": 0x2E, "del": 0x2E,
	"insert": 0x2D, "home": 0x24, "end": 0x23, "pageup": 0x21, "pgup": 0x21,
	"pagedown": 0x22, "pgdn": 0x22, "up": 0x26, "down": 0x28, "left": 0x25, "right": 0x27,
	"printscreen": 0x2C, "prtsc": 0x2C, "capslock": 0x14, "menu": 0x5D, "apps": 0x5D,
	"f1": 0x70, "f2": 0x71, "f3": 0x72, "f4": 0x73, "f5": 0x74, "f6": 0x75,
	"f7": 0x76, "f8": 0x77, "f9": 0x78, "f10": 0x79, "f11": 0x7A, "f12": 0x7B,
}

// parseKeyCombo turns "ctrl+shift+esc" into the virtual-key codes to hold and the final key.
func parseKeyCombo(combo string) (mods []uint16, key uint16, err error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(combo)), "+")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		vk, ok := vkByName[p]
		if !ok {
			if len([]rune(p)) == 1 {
				r := []rune(strings.ToUpper(p))[0]
				if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
					vk = uint16(r)
					ok = true
				}
			}
		}
		if !ok {
			return nil, 0, fmt.Errorf("не знаю клавишу %q в сочетании %q", p, combo)
		}
		if i == len(parts)-1 {
			key = vk
		} else {
			mods = append(mods, vk)
		}
	}
	if key == 0 {
		return nil, 0, fmt.Errorf("пустое сочетание клавиш")
	}
	return mods, key, nil
}

// pressKeyCombo presses one combination like "ctrl+c" or "win+r".
func pressKeyCombo(combo string) error {
	mods, key, err := parseKeyCombo(combo)
	if err != nil {
		return err
	}
	var events []keyEvent
	for _, m := range mods {
		events = append(events, keyEvent{vk: m})
	}
	events = append(events, keyEvent{vk: key}, keyEvent{vk: key, up: true})
	for i := len(mods) - 1; i >= 0; i-- {
		events = append(events, keyEvent{vk: mods[i], up: true})
	}
	return sendKeyEvents(events)
}

// ===== ввод: мышь =====

func moveMouse(x, y int) error {
	makeDPIAware()
	ok, _, _ := procSetCursorPos.Call(uintptr(int32(x)), uintptr(int32(y)))
	if ok == 0 {
		return fmt.Errorf("не смог переместить курсор в (%d,%d). %s", x, y, blockedInputHint())
	}
	return nil
}

// blockedInputHint объясняет молчаливый отказ ввода.
//
// Windows блокирует мышь и клавиатуру от обычного процесса, пока на переднем плане окно,
// запущенное ОТ АДМИНИСТРАТОРА (UIPI). Функции при этом возвращают «неуспех» без кода
// ошибки — GetLastError говорит «операция выполнена успешно». На живом прогоне это выглядело
// как необъяснимый сбой: мышь в тестах работала, а в задаче нет, потому что фокус держал
// Диспетчер задач. Без этого объяснения модель начинает «чинить» рабочий стол наугад.
func blockedInputHint() string {
	fg := foregroundWindow()
	who := "окно, запущенное от имени администратора"
	if strings.TrimSpace(fg.Title) != "" {
		who = fmt.Sprintf("активное окно «%s» (%s)", fg.Title, fg.Exe)
	}
	return "Скорее всего ввод заблокировала Windows: " + who + " работает с правами выше, " +
		"чем у меня, и пока оно на переднем плане мышь и клавиатура мне недоступны. " +
		"Попроси пользователя переключиться на обычное окно — или запустить агента от администратора. " +
		"Закрывать это окно самому НЕЛЬЗЯ."
}

func cursorPos() (int, int) {
	var p struct{ X, Y int32 }
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return int(p.X), int(p.Y)
}

func mouseButtonFlags(button string) (down, up uint32, err error) {
	switch strings.ToLower(strings.TrimSpace(button)) {
	case "", "left", "лев", "левая":
		return mouseEventLeftDown, mouseEventLeftUp, nil
	case "right", "прав", "правая":
		return mouseEventRightDown, mouseEventRightUp, nil
	case "middle", "сред", "средняя":
		return mouseEventMiddleDown, mouseEventMiddleUp, nil
	}
	return 0, 0, fmt.Errorf("кнопка %q неизвестна (left|right|middle)", button)
}

func sendMouse(flags uint32, data int32) error {
	rec := inputRecord{Type: inputMouse}
	*(*mouseInput)(unsafe.Pointer(&rec.Data[0])) = mouseInput{Flags: flags, MouseData: uint32(data)}
	return sendInputs([]inputRecord{rec})
}

func clickMouse(button string, double bool) error {
	down, up, err := mouseButtonFlags(button)
	if err != nil {
		return err
	}
	rounds := 1
	if double {
		rounds = 2
	}
	for i := 0; i < rounds; i++ {
		if err := sendMouse(down, 0); err != nil {
			return err
		}
		time.Sleep(15 * time.Millisecond)
		if err := sendMouse(up, 0); err != nil {
			return err
		}
		if double && i == 0 {
			time.Sleep(40 * time.Millisecond)
		}
	}
	return nil
}

func scrollMouse(amount int) error {
	// Одно «деление» колеса = 120 единиц; плюс — вверх/от себя.
	return sendMouse(mouseEventWheel, int32(amount*120))
}

// desktopSupported reports whether this build can drive the desktop at all.
func desktopSupported() bool { return true }

const desktopUnsupportedNote = ""
