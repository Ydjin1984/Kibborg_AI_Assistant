package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// lookupProcessName resolves a PID to an image name for the kill_process gate.
// Empty string means "could not resolve" — the gate must then ask, never auto-allow.
func lookupProcessName(pid int) string {
	if pid <= 0 {
		return ""
	}
	if runtime.GOOS == "windows" {
		return lookupProcessNameWindows(pid)
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func lookupProcessNameWindows(pid int) string {
	// tasklist CSV: "lsass.exe","1234","Services","0",...
	cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH")
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return ""
	}
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(out))
	low := strings.ToLower(line)
	// EN: "INFO: No tasks…"; RU: "ИНФОРМАЦИЯ: задачи… отсутствуют."
	if line == "" || !strings.Contains(line, ",") ||
		strings.Contains(low, "info:") || strings.Contains(low, "информац") {
		return ""
	}
	// CSV: "name.exe","pid","session",...
	parts := strings.Split(line, ",")
	if len(parts) < 2 {
		return ""
	}
	name := strings.Trim(parts[0], `"`)
	gotPID := strings.Trim(parts[1], `"`)
	if name == "" || gotPID != strconv.Itoa(pid) {
		return ""
	}
	return name
}
