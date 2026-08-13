package browser

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunCommandEcho(t *testing.T) {
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "Write-Output 'kibborg-ok'"
	} else {
		cmd = "echo kibborg-ok"
	}
	out, err := RunCommand(context.Background(), cmd, "", 30)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "kibborg-ok") {
		t.Fatalf("expected echo in output, got: %s", out)
	}
	if !strings.Contains(out, "exit=0") {
		t.Fatalf("expected exit=0, got: %s", out)
	}
}

func TestRunCommandEmpty(t *testing.T) {
	if _, err := RunCommand(context.Background(), "  ", "", 5); err == nil {
		t.Fatal("expected error for empty command")
	}
}

// §12 п. 0 / приёмка №16: cancelling the task context must kill the CHILD process, not just
// return early — /stop during `Start-Sleep 120` has to answer within a second.
func TestRunCommandCancelKillsChild(t *testing.T) {
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "Start-Sleep -Seconds 30"
	} else {
		cmd = "sleep 30"
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := RunCommand(ctx, cmd, "", 120)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("cancel did not reach the child: took %s", elapsed)
	}
}

// The tool timeout must be clamped by the REMAINING task budget, never extend it (§4.2).
func TestRunCommandTaskDeadlineWins(t *testing.T) {
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "Start-Sleep -Seconds 30"
	} else {
		cmd = "sleep 30"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	start := time.Now()
	// Ask for 600 s — the 0.7 s task deadline must win.
	if _, err := RunCommand(ctx, cmd, "", 600); err == nil {
		t.Fatal("expected an error when the task deadline expires")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("task deadline ignored: took %s", elapsed)
	}
}

func TestFileOpsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	msg, err := WriteLocalFile(path, "привет kibborg\n", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "записано") {
		t.Fatalf("write msg: %s", msg)
	}
	body, err := ReadLocalFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "привет kibborg") {
		t.Fatalf("read body: %s", body)
	}
	listing, err := ListLocalDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listing, "hello.txt") {
		t.Fatalf("list: %s", listing)
	}
	info, err := LocalFileInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(info, "kind=file") {
		t.Fatalf("info: %s", info)
	}
	// append
	if _, err := WriteLocalFile(path, "line2\n", true); err != nil {
		t.Fatal(err)
	}
	body, _ = ReadLocalFile(path, 0)
	if !strings.Contains(body, "line2") {
		t.Fatalf("append failed: %s", body)
	}
	sub := filepath.Join(dir, "nested", "x")
	if _, err := MkdirLocal(sub); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(sub); err != nil || !st.IsDir() {
		t.Fatalf("mkdir: %v %v", st, err)
	}
	if _, err := DeleteLocal(path, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file should be gone")
	}
}

func TestDeletePathRefusesRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		if _, err := DeleteLocal(`C:\`, true); err == nil {
			t.Fatal("should refuse deleting C:\\")
		}
	} else {
		if _, err := DeleteLocal("/", true); err == nil {
			t.Fatal("should refuse deleting /")
		}
	}
}

func TestCleanSubtitle(t *testing.T) {
	raw := "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHello\n\n00:00:02.000 --> 00:00:03.000\nHello\n\n00:00:03.000 --> 00:00:04.000\nWorld\n"
	got := cleanSubtitle(raw)
	if !strings.Contains(got, "Hello") || !strings.Contains(got, "World") {
		t.Fatalf("got %q", got)
	}
	// consecutive dup "Hello" collapsed once
	if strings.Count(got, "Hello") != 1 {
		t.Fatalf("expected dedup, got %q", got)
	}
}

func TestWantsToolsInCatalog(t *testing.T) {
	// sanity: Dispatch unknown still errors
	s := New("")
	if _, err := s.Dispatch(context.Background(), "no_such_tool_xyz", nil); err == nil {
		t.Fatal("expected unknown tool error")
	}
	// list_dir of temp works without Chrome
	dir := t.TempDir()
	out, err := s.Dispatch(context.Background(), "list_dir", map[string]any{"path": dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "dir=") {
		t.Fatalf("list_dir: %s", out)
	}
}
