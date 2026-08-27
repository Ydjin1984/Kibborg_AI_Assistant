package main

import (
	"strings"
	"testing"
)

func TestCoerceChatMessages_MergesSystems(t *testing.T) {
	in := []map[string]any{
		{"role": "system", "content": "A"},
		{"role": "system", "content": "B"},
		{"role": "user", "content": "hi"},
		{"role": "assistant", "content": "yo"},
		{"role": "system", "content": "late"}, // must still fold into leading system
		{"role": "user", "content": "again"},
	}
	out := coerceChatMessages(in)
	if len(out) != 4 {
		t.Fatalf("len=%d want 4: %+v", len(out), out)
	}
	if out[0]["role"] != "system" {
		t.Fatalf("first must be system: %+v", out[0])
	}
	sys, _ := out[0]["content"].(string)
	for _, need := range []string{"A", "B", "late"} {
		if !strings.Contains(sys, need) {
			t.Fatalf("merged system missing %q: %q", need, sys)
		}
	}
	for i, m := range out {
		if i == 0 {
			continue
		}
		if role, _ := m["role"].(string); role == "system" {
			t.Fatalf("extra system at %d", i)
		}
	}
}

func TestCoerceChatMessages_NoSystem(t *testing.T) {
	in := []map[string]any{
		{"role": "user", "content": "hi"},
	}
	out := coerceChatMessages(in)
	if len(out) != 1 || out[0]["role"] != "user" {
		t.Fatalf("unexpected %+v", out)
	}
}
