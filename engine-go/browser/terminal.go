package browser

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

// Terminal defaults. The agent is a local single-user assistant: full shell access is
// intentional (parity with Cursor/Claude Code). Still clamp output and time so a runaway
// command cannot freeze the tool loop forever.
const (
	defaultCmdTimeoutSec = 120
	maxCmdTimeoutSec     = 600
	maxCmdOutputBytes    = 100_000
)

// RunCommand executes a shell command and returns a compact stdout+stderr report.
// Windows → PowerShell; other OS → bash -lc. cwd may be empty (inherits process CWD).
//
// parent carries the task context: cancelling it (/stop, TaskTimeout) kills the child
// process immediately instead of letting it play out its own timeout. The tool timeout is
// layered UNDER the task deadline — a 600 s command started 30 s before the task expires
// gets 30 s, not 600.
func RunCommand(parent context.Context, command, cwd string, timeoutSec int) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("пустая команда")
	}
	if timeoutSec <= 0 {
		timeoutSec = defaultCmdTimeoutSec
	}
	if timeoutSec > maxCmdTimeoutSec {
		timeoutSec = maxCmdTimeoutSec
	}
	if parent == nil {
		parent = context.Background()
	}

	ctx, cancel := context.WithTimeout(parent, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// -NoProfile/-NonInteractive keep runs fast and non-interactive; command is the full line.
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctx, "bash", "-lc", command)
	}
	if cwd = strings.TrimSpace(cwd); cwd != "" {
		if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
			return "", fmt.Errorf("cwd не существует или не каталог: %s", cwd)
		}
		cmd.Dir = cwd
	}
	// Inherit env so PATH / agent-reach / gh / python work as for the user.
	cmd.Env = os.Environ()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	out := capCmdBytes(stdout.Bytes())
	errOut := capCmdBytes(stderr.Bytes())

	var b strings.Builder
	b.WriteString(fmt.Sprintf("exit=%d\n", exitCode(err, ctx)))
	if out != "" {
		b.WriteString("--- stdout ---\n")
		b.WriteString(out)
		if !strings.HasSuffix(out, "\n") {
			b.WriteByte('\n')
		}
	}
	if errOut != "" {
		b.WriteString("--- stderr ---\n")
		b.WriteString(errOut)
		if !strings.HasSuffix(errOut, "\n") {
			b.WriteByte('\n')
		}
	}
	if out == "" && errOut == "" {
		b.WriteString("(нет вывода)\n")
	}
	// Surface timeout / cancellation / non-zero exit in the body so the model can recover
	// without a hard fail. Cancellation is reported as an error so the agent layer maps it to
	// ResultStatus cancelled instead of feeding a half-finished stdout back to the model.
	switch {
	case parent.Err() != nil:
		return strings.TrimSpace(b.String()), context.Canceled
	case ctx.Err() == context.DeadlineExceeded:
		b.WriteString(fmt.Sprintf("⏱ таймаут %ds — процесс убит\n", timeoutSec))
		return strings.TrimSpace(b.String()), context.DeadlineExceeded
	case err != nil && exitCode(err, ctx) != 0:
		b.WriteString("⚠ команда завершилась с ошибкой (см. exit/stderr)\n")
	}
	return strings.TrimSpace(b.String()), nil
}

func exitCode(err error, ctx context.Context) int {
	if ctx.Err() != nil {
		return -1
	}
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 1
}

func capCmdBytes(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if len(b) > maxCmdOutputBytes {
		// Keep head+tail so build errors (often at the end) stay visible.
		head := maxCmdOutputBytes * 3 / 4
		tail := maxCmdOutputBytes - head
		for head > 0 && !utf8.RuneStart(b[head]) {
			head--
		}
		startTail := len(b) - tail
		for startTail < len(b) && !utf8.RuneStart(b[startTail]) {
			startTail++
		}
		return string(b[:head]) +
			fmt.Sprintf("\n…(обрезано, всего %d байт)…\n", len(b)) +
			string(b[startTail:])
	}
	// Best-effort UTF-8; invalid sequences become replacement chars via conversion.
	if !utf8.Valid(b) {
		return strings.ToValidUTF8(string(b), "�")
	}
	return string(b)
}
