package main

// Test isolation for the runtime directory.
//
// The journals (§6.5) are written to relative paths — `runtime/tasks.jsonl`,
// `runtime/hands.jsonl` — resolved against the working directory. Under `go test` that
// directory is engine-go itself, so every test that builds a Task and finishes it appended
// rows like «тестовая задача» and «x» to the REAL audit trail. The first live run found them
// sitting between genuine tasks, which is exactly the confusion the journals exist to prevent.
//
// TestMain moves the whole package's tests into a scratch directory. Tests that chdir on their
// own (the rotation test) keep working; they just start from a different, equally disposable
// place.

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	orig, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	tmp, err := os.MkdirTemp("", "kibborg-tests-*")
	if err != nil {
		panic(err)
	}
	if err := os.Chdir(tmp); err != nil {
		panic(err)
	}
	code := m.Run()
	// Return before cleanup so the removal is not blocked by the current directory on Windows.
	_ = os.Chdir(orig)
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}
