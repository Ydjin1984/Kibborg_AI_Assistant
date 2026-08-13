package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseSTTJSON(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		ok   bool
	}{
		{`{"text":"  привет  "}`, "привет", true},
		{`{"result":"hello"}`, "hello", true},
		{`{"transcript":"ok"}`, "ok", true},
		{`plain text only`, "plain text only", true},
		{`{"text":""}`, "", false},
		{`{}`, "", false},
	}
	for _, c := range cases {
		got, err := parseSTTJSON([]byte(c.raw))
		if c.ok {
			if err != nil {
				t.Errorf("parseSTTJSON(%q) err=%v", c.raw, err)
				continue
			}
			if got != c.want {
				t.Errorf("parseSTTJSON(%q)=%q want %q", c.raw, got, c.want)
			}
		} else if err == nil {
			t.Errorf("parseSTTJSON(%q) expected error, got %q", c.raw, got)
		}
	}
}

func TestIsAudioUpload(t *testing.T) {
	if !isAudioUpload("voice.webm", "audio/webm") {
		t.Error("webm should be audio")
	}
	if !isAudioUpload("note.oga", "application/octet-stream") {
		t.Error(".oga by ext should be audio")
	}
	if isAudioUpload("photo.png", "image/png") {
		t.Error("png must not be audio")
	}
}

func TestTypeWhisperWanted(t *testing.T) {
	if !typeWhisperWanted(Config{}) {
		t.Error("empty config should want TypeWhisper by default")
	}
	if typeWhisperWanted(Config{TypeWhisperURL: "off"}) {
		t.Error("URL=off should disable TypeWhisper")
	}
	if !typeWhisperWanted(Config{TypeWhisperURL: "http://127.0.0.1:8978"}) {
		t.Error("explicit URL should want TypeWhisper")
	}
}

func TestVoiceEnabled(t *testing.T) {
	if !voiceEnabled(Config{}) {
		t.Error("default should enable voice (TypeWhisper path)")
	}
	if voiceEnabled(Config{TypeWhisperURL: "off"}) {
		t.Error("off + no whisper.cpp should disable voice")
	}
	if !voiceEnabled(Config{TypeWhisperURL: "off", WhisperExe: "x", WhisperModel: "y"}) {
		t.Error("whisper.cpp alone should enable voice")
	}
}

func TestTypeWhisperEndpointDiscovery(t *testing.T) {
	// Isolate from a real TypeWhisper install on the machine.
	empty := t.TempDir()
	t.Setenv("LOCALAPPDATA", empty)

	// Without discovery file, default port.
	base, token := typeWhisperEndpoint(Config{})
	if base != "http://127.0.0.1:8978" {
		t.Errorf("default base = %s", base)
	}
	if token != "" {
		t.Errorf("default token = %q", token)
	}

	// Explicit URL + token
	base, token = typeWhisperEndpoint(Config{
		TypeWhisperURL:   "http://127.0.0.1:9001/",
		TypeWhisperToken: "secret",
	})
	if base != "http://127.0.0.1:9001" {
		t.Errorf("explicit base = %s", base)
	}
	if token != "secret" {
		t.Errorf("token = %q", token)
	}

	// Canonical discovery path: TypeWhisper-UserData/api-discovery.json
	dir := t.TempDir()
	tw := filepath.Join(dir, "TypeWhisper-UserData")
	if err := os.MkdirAll(tw, 0o755); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"version": 1, "port": 9123, "token": "from-file"})
	if err := os.WriteFile(filepath.Join(tw, "api-discovery.json"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", dir)
	base, token = typeWhisperEndpoint(Config{})
	if base != "http://127.0.0.1:9123" {
		t.Errorf("discovered base = %s", base)
	}
	if token != "from-file" {
		t.Errorf("discovered token = %q", token)
	}
}

func TestAudioExt(t *testing.T) {
	if got := audioExt("a.webm", ""); got != ".webm" {
		t.Errorf("ext from path = %s", got)
	}
	if got := audioExt("", "audio/webm"); got != ".webm" {
		t.Errorf("ext from mime = %s", got)
	}
}
