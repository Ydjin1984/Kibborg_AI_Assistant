package browser

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// File tools give the agent direct filesystem access on the user's machine (local assistant).
// Limits exist only to protect the model context window, not to sandbox the user.

const (
	maxReadFileBytes = 200_000
	maxListEntries   = 500
	maxWriteBytes    = 2_000_000
)

// ReadLocalFile returns text (or a note for binary) of path, capped for the LLM context.
func ReadLocalFile(path string, maxBytes int) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("нужен path")
	}
	if maxBytes <= 0 || maxBytes > maxReadFileBytes {
		maxBytes = maxReadFileBytes
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("это каталог, не файл — используй list_dir: %s", path)
	}
	// PDF технически читается, но внутри сжатые потоки, а не текст. Без этой ветки модель
	// получала двоичный мусор и пересказывала его как содержание документа.
	if strings.EqualFold(filepath.Ext(path), ".pdf") {
		return "", fmt.Errorf("это PDF — текста напрямую в нём нет, внутри сжатые потоки. "+
			"Используй read_document(path=%q): он возьмёт текстовый слой, а если внутри скан — распознает страницы", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil {
		return "", err
	}
	truncated := len(data) > maxBytes
	if truncated {
		data = data[:maxBytes]
	}
	// Binary heuristic: lots of NULs in the first chunk.
	if isMostlyBinary(data) {
		return fmt.Sprintf("binary file: %s (%d bytes, mod %s) — не показываю содержимое; используй run_command (напр. file/hexdump) или работай через терминал.",
			path, st.Size(), st.ModTime().Format(time.RFC3339)), nil
	}
	text := string(data)
	if !utf8.Valid(data) {
		text = strings.ToValidUTF8(text, "�")
	}
	// Drop a UTF-8 BOM if present.
	text = strings.TrimPrefix(text, "\ufeff")
	header := fmt.Sprintf("path=%s size=%d mod=%s\n", path, st.Size(), st.ModTime().Format(time.RFC3339))
	if truncated {
		return header + text + fmt.Sprintf("\n…(обрезано до %d байт из %d)", maxBytes, st.Size()), nil
	}
	return header + text, nil
}

// WriteLocalFile creates/overwrites (or appends) a file. Parent dirs are created as needed.
func WriteLocalFile(path, content string, appendMode bool) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("нужен path")
	}
	if len(content) > maxWriteBytes {
		return "", fmt.Errorf("content слишком большой (%d > %d байт)", len(content), maxWriteBytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	flag := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	n, err := f.WriteString(content)
	if err != nil {
		return "", err
	}
	abs, _ := filepath.Abs(path)
	mode := "записано"
	if appendMode {
		mode = "дописано"
	}
	return fmt.Sprintf("%s %d байт → %s", mode, n, abs), nil
}

// ListLocalDir lists entries in path (names + type + size).
func ListLocalDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "."
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	abs, _ := filepath.Abs(path)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("dir=%s count=%d\n", abs, len(entries)))
	limit := len(entries)
	if limit > maxListEntries {
		limit = maxListEntries
	}
	for i := 0; i < limit; i++ {
		e := entries[i]
		info, ierr := e.Info()
		size := int64(0)
		mod := ""
		if ierr == nil {
			size = info.Size()
			mod = info.ModTime().Format("2006-01-02 15:04")
		}
		kind := "file"
		if e.IsDir() {
			kind = "dir"
		}
		b.WriteString(fmt.Sprintf("%s\t%s\t%d\t%s\n", kind, e.Name(), size, mod))
	}
	if len(entries) > maxListEntries {
		b.WriteString(fmt.Sprintf("…(+%d записей)\n", len(entries)-maxListEntries))
	}
	return strings.TrimSpace(b.String()), nil
}

// LocalFileInfo returns metadata for a path.
func LocalFileInfo(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("нужен path")
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	abs, _ := filepath.Abs(path)
	kind := "file"
	if st.IsDir() {
		kind = "dir"
	}
	return fmt.Sprintf("path=%s\nabs=%s\nkind=%s\nsize=%d\nmod=%s\nmode=%s",
		path, abs, kind, st.Size(), st.ModTime().Format(time.RFC3339), st.Mode().String()), nil
}

// MkdirLocal creates a directory (and parents).
func MkdirLocal(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("нужен path")
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", err
	}
	abs, _ := filepath.Abs(path)
	return "создан каталог: " + abs, nil
}

// DeleteLocal removes a file or empty directory. recursive=true → RemoveAll.
func DeleteLocal(path string, recursive bool) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("нужен path")
	}
	// Refuse deleting drive roots / obvious disasters.
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(abs)
	if clean == "" || clean == "." || clean == string(filepath.Separator) ||
		(len(clean) == 3 && clean[1] == ':' && (clean[2] == '\\' || clean[2] == '/')) {
		return "", fmt.Errorf("отказ удалять корень/диск: %s", clean)
	}
	if recursive {
		if err := os.RemoveAll(path); err != nil {
			return "", err
		}
		return "удалено (recursive): " + abs, nil
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return "удалено: " + abs, nil
}

func isMostlyBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	n := len(data)
	if n > 8192 {
		n = 8192
	}
	nul := 0
	for i := 0; i < n; i++ {
		if data[i] == 0 {
			nul++
		}
	}
	return nul > n/50 // >2% NULs → treat as binary
}
