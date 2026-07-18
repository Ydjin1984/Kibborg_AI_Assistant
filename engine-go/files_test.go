package main

import "testing"

func TestIsTextFile(t *testing.T) {
	yes := []struct{ name, mime string }{
		{"main.go", "application/octet-stream"},
		{"script.py", "text/x-python"},
		{"notes.md", "text/markdown"},
		{"data.txt", "text/plain"},
		{"config.yaml", "application/octet-stream"},
		{"q.sql", ""},
		{"Dockerfile", "application/octet-stream"},
		{"go.mod", ""},
		{"page.html", "text/html"},
	}
	for _, c := range yes {
		if !isTextFile(c.name, c.mime) {
			t.Errorf("isTextFile(%q,%q) = false, want true", c.name, c.mime)
		}
	}
	no := []struct{ name, mime string }{
		{"photo.jpg", "image/jpeg"},
		{"archive.zip", "application/zip"},
		{"app.exe", "application/octet-stream"},
		{"doc.pdf", "application/pdf"},
	}
	for _, c := range no {
		if isTextFile(c.name, c.mime) {
			t.Errorf("isTextFile(%q,%q) = true, want false", c.name, c.mime)
		}
	}
}

func TestBytesHaveNUL(t *testing.T) {
	if !bytesHaveNUL([]byte{65, 0, 66}) {
		t.Error("expected NUL detected")
	}
	if bytesHaveNUL([]byte("plain text")) {
		t.Error("did not expect NUL")
	}
}
