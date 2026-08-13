//go:build !windows

package main

// Заглушки пака `system` для не-Windows сборок. Kibborg живёт на Windows-ПК пользователя,
// но код должен собираться и на Linux (CI, `go vet` в контейнере), поэтому каждая функция
// рабочего стола здесь существует и честно отвечает «не поддерживается», а не отсутствует.

import (
	"fmt"
	"time"
)

type screenRect struct {
	X, Y, W, H int
}

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

var errNoDesktop = fmt.Errorf("управление рабочим столом реализовано только для Windows")

func listMonitors() []screenRect { return nil }

func captureScreenPNG(monitor int) ([]byte, screenRect, string, error) {
	return nil, screenRect{}, "", errNoDesktop
}

func captureWindowPNG(hwnd uintptr) ([]byte, screenRect, error) {
	return nil, screenRect{}, errNoDesktop
}

func listDesktopWindows() ([]desktopWindow, error) { return nil, errNoDesktop }

func findWindow(match string) (desktopWindow, error) { return desktopWindow{}, errNoDesktop }

func findWindowAll(match string) (desktopWindow, []desktopWindow, error) {
	return desktopWindow{}, nil, errNoDesktop
}

func foregroundWindow() desktopWindow { return desktopWindow{} }

func waitWindowOfProcess(pid int, timeout time.Duration) (desktopWindow, bool) {
	return desktopWindow{}, false
}

func focusDesktopWindow(w desktopWindow) error { return errNoDesktop }

func typeUnicodeText(text string) error { return errNoDesktop }

func pressKeyCombo(combo string) error { return errNoDesktop }

func parseKeyCombo(combo string) ([]uint16, uint16, error) { return nil, 0, errNoDesktop }

func moveMouse(x, y int) error { return errNoDesktop }

func cursorPos() (int, int) { return 0, 0 }

func clickMouse(button string, double bool) error { return errNoDesktop }

func scrollMouse(amount int) error { return errNoDesktop }

func desktopSupported() bool { return false }

const desktopUnsupportedNote = "управление рабочим столом доступно только на Windows"
