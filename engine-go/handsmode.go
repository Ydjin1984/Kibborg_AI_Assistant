package main

// The hands switch (ТЗ §6.4). It lives ONLY in a runtime store — settings.ini is read once by
// loadConfig at startup, so a toggle there would take effect on the next restart, which is
// exactly not what a switch is for.
//
// One file backs both channels: the Web toggle and Telegram /hands flip the same state.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	handsModeSafe = "safe"
	handsModeFull = "full"

	handsModePath = "runtime/hands_mode.json"
)

type handsModeFile struct {
	Mode      string `json:"mode"`
	UpdatedAt string `json:"updated_at"`
	By        string `json:"by,omitempty"`
}

var (
	handsMu     sync.RWMutex
	handsMode   = handsModeSafe
	handsLoaded bool
)

// loadHandsMode reads the persisted switch at startup. Anything unreadable or unknown falls
// back to safe — the switch fails closed.
func loadHandsMode() {
	handsMu.Lock()
	defer handsMu.Unlock()
	handsLoaded = true
	data, err := os.ReadFile(handsModePath)
	if err != nil {
		handsMode = handsModeSafe
		return
	}
	var f handsModeFile
	if err := json.Unmarshal(data, &f); err != nil {
		log.Printf("[HANDS] %s повреждён (%v) — режим safe", handsModePath, err)
		handsMode = handsModeSafe
		return
	}
	if normalizeHandsMode(f.Mode) == handsModeFull {
		handsMode = handsModeFull
	} else {
		handsMode = handsModeSafe
	}
	log.Printf("[HANDS] режим рук: %s", handsMode)
}

// currentHandsMode returns "safe" or "full".
func currentHandsMode() string {
	handsMu.RLock()
	defer handsMu.RUnlock()
	if !handsLoaded {
		return handsModeSafe
	}
	return handsMode
}

// setHandsMode persists the switch and returns the applied value.
func setHandsMode(mode, by string) string {
	m := normalizeHandsMode(mode)
	handsMu.Lock()
	handsMode = m
	handsLoaded = true
	handsMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(handsModePath), 0o755); err == nil {
		body, _ := json.MarshalIndent(handsModeFile{
			Mode:      m,
			UpdatedAt: time.Now().Format(time.RFC3339),
			By:        by,
		}, "", "  ")
		if err := os.WriteFile(handsModePath, body, 0o644); err != nil {
			log.Printf("[HANDS] не сохранил режим: %v", err)
		}
	}
	log.Printf("[HANDS] режим переключён на %s (%s)", m, by)
	return m
}

// normalizeHandsMode maps user input (RU/EN, both channels) onto the two real values.
func normalizeHandsMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "full", "полные", "полный", "on", "1", "true", "да":
		return handsModeFull
	default:
		return handsModeSafe
	}
}

// handsModeLabel is the human line shown after /hands or on the Web toggle.
func handsModeLabel(mode string) string {
	if mode == handsModeFull {
		return "🖐 **Длинные руки (full)**: доступ ко всему ПК без подтверждений — " +
			"весь диск, консоль, рабочий стол, окна, мышь и клавиатура, запуск программ.\n" +
			"Один вопрос всё же остаётся: ядерные действия (format/diskpart/bcdedit, снос корня диска, " +
			"выключение ПК, убийство системных процессов) и операции с деньгами переспрашиваются один раз — " +
			"это предохранитель от ошибки модели, отвечается словом «да».\n" +
			"Укоротить руки: `/hands safe`."
	}
	return "🛡 **Короткие руки (safe)**: читать можно всё, а запись/удаление вне рабочих каталогов, " +
		"мышь, клавиатура и запуск программ спрашивают подтверждение. Ядерные действия запрещены.\n" +
		"Развязать руки: `/hands full`."
}

// handsModeShort is the one-word state for buttons and status ribbons.
func handsModeShort(mode string) string {
	if mode == handsModeFull {
		return "🖐 длинные"
	}
	return "🛡 короткие"
}

// ===== who is the owner =====

// isOwnerChat reports whether this Telegram chat is the machine's owner: only allowlisted
// chats may flip the hands switch or stop tasks (§4.2 «/stop», §6.4).
func isOwnerChat(allow map[int64]bool, chatID int64) bool {
	if chatID == webChatID {
		return true // Web UI is loopback-only and already behind sameOriginGuard
	}
	if allow == nil {
		return false // no TELEGRAM_ID configured → nobody owns the hands
	}
	return allow[chatID]
}

// actorFor builds the Actor a guard call needs.
func actorFor(channel string, chatID int64, isOwner bool) Actor {
	return Actor{
		Mode:    currentHandsMode(),
		Channel: channel,
		ChatID:  chatID,
		IsOwner: isOwner,
	}
}
