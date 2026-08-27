package secops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	securityUploadDir  = "runtime/browser/security/uploads"
	maxUploadBytes     = 64 << 20 // 64 MiB — office/video dumps for the agent
	uploadPreviewChars = 4000
)

// SaveUpload stores a user-attached file under runtime/browser/security/uploads
// so /api/files can serve it and the agent gets a real path (docx/xlsx/binaries).
func SaveUpload(filename string, data []byte) (DownloadResult, error) {
	if len(data) == 0 {
		return DownloadResult{}, fmt.Errorf("пустой файл")
	}
	if len(data) > maxUploadBytes {
		return DownloadResult{}, fmt.Errorf("файл больше %d МБ — не сохраняю", maxUploadBytes>>20)
	}
	name := strings.TrimSpace(filename)
	if name == "" {
		name = "upload.bin"
	}
	name = sanitizeEvidenceName(name)
	if err := os.MkdirAll(securityUploadDir, 0o755); err != nil {
		return DownloadResult{}, err
	}
	stamp := time.Now().Format("20060102-150405")
	fullName := stamp + "-" + name
	pathOnDisk := filepath.Join(securityUploadDir, fullName)
	if err := os.WriteFile(pathOnDisk, data, 0o644); err != nil {
		return DownloadResult{}, err
	}
	rel := filepath.ToSlash(filepath.Join("security", "uploads", fullName))
	out := DownloadResult{
		Path:  pathOnDisk,
		URL:   rel,
		Bytes: len(data),
	}
	if utf8.Valid(data) && !uploadHasNUL(data) {
		out.Preview = textPreview(data, "text/plain")
		if len(out.Preview) > uploadPreviewChars {
			out.Preview = out.Preview[:uploadPreviewChars] + "…"
		}
	}
	return out, nil
}

// AttachmentBrief is the block injected into the agent prompt for an uploaded file.
func AttachmentBrief(name, mime string, saved DownloadResult) string {
	var b strings.Builder
	b.WriteString("Приложение пользователя (файл на диске агента):\n")
	fmt.Fprintf(&b, "- имя: `%s`\n", name)
	if mime != "" {
		fmt.Fprintf(&b, "- mime: `%s`\n", mime)
	}
	fmt.Fprintf(&b, "- путь: `%s`\n", saved.Path)
	fmt.Fprintf(&b, "- скачать в панели: `/api/files/%s`\n", saved.URL)
	fmt.Fprintf(&b, "- размер: %d байт\n", saved.Bytes)
	b.WriteString("Используй read_file / read_document / audit_file / analyze_video по типу файла. ")
	b.WriteString("Не проси пользователя прислать файл ещё раз — он уже на диске.\n")
	if saved.Preview != "" {
		b.WriteString("\nНачало содержимого (если текст):\n```\n")
		b.WriteString(saved.Preview)
		b.WriteString("\n```\n")
	}
	return b.String()
}

func uploadHasNUL(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}
