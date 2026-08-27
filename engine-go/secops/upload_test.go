package secops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveUploadWritesUnderUploads(t *testing.T) {
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	got, err := SaveUpload("notes.docx", []byte("PK\x03\x04fake-office"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Path, filepath.Join("security", "uploads")) &&
		!strings.Contains(filepath.ToSlash(got.Path), "security/uploads") {
		t.Fatalf("path = %q, ждали uploads", got.Path)
	}
	if !strings.HasPrefix(got.URL, "security/uploads/") {
		t.Fatalf("url = %q", got.URL)
	}
	if _, err := os.Stat(got.Path); err != nil {
		t.Fatal(err)
	}
	brief := AttachmentBrief("notes.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", got)
	if !strings.Contains(brief, got.Path) || !strings.Contains(brief, "/api/files/") {
		t.Fatalf("brief без пути/ссылки: %s", brief)
	}
}
