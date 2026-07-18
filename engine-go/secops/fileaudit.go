package secops

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
)

// FileAudit is the deterministic security profile of a file's bytes.
type FileAudit struct {
	Name        string  `json:"name"`
	Size        int     `json:"size"`
	MD5         string  `json:"md5"`
	SHA1        string  `json:"sha1"`
	SHA256      string  `json:"sha256"`
	Entropy     float64 `json:"entropy"`      // Shannon entropy, 0..8 bits/byte
	EntropyNote string  `json:"entropy_note"` // human read of the entropy
	FileType    string  `json:"file_type"`    // sniffed from magic bytes
	Executable  bool    `json:"executable"`   // PE/ELF/Mach-O
}

// AuditFile hashes the bytes, measures entropy and sniffs the type. Read-only.
func AuditFile(name string, data []byte) FileAudit {
	fa := FileAudit{Name: name, Size: len(data)}

	m := md5.Sum(data)
	s1 := sha1.Sum(data)
	s2 := sha256.Sum256(data)
	fa.MD5 = hex.EncodeToString(m[:])
	fa.SHA1 = hex.EncodeToString(s1[:])
	fa.SHA256 = hex.EncodeToString(s2[:])

	fa.Entropy = shannonEntropy(data)
	fa.EntropyNote = entropyNote(fa.Entropy)
	fa.FileType, fa.Executable = sniffType(data)
	return fa
}

// shannonEntropy returns bits/byte (0 = uniform, ~8 = random/encrypted/compressed).
func shannonEntropy(data []byte) float64 {
	if len(data) == 0 {
		return 0
	}
	var freq [256]int
	for _, b := range data {
		freq[b]++
	}
	n := float64(len(data))
	var h float64
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return math.Round(h*1000) / 1000
}

func entropyNote(h float64) string {
	switch {
	case h >= 7.5:
		return "очень высокая — вероятно зашифровано/сжато/упаковано (packer)"
	case h >= 6.5:
		return "высокая — сжатые данные или обфускация"
	case h >= 4.5:
		return "средняя — типично для текста/кода"
	default:
		return "низкая — простой/повторяющийся контент"
	}
}

// sniffType identifies a file by magic bytes; returns (label, isExecutable).
func sniffType(data []byte) (string, bool) {
	has := func(prefix ...byte) bool {
		if len(data) < len(prefix) {
			return false
		}
		for i, b := range prefix {
			if data[i] != b {
				return false
			}
		}
		return true
	}
	switch {
	case has('M', 'Z'):
		return "Windows PE/DOS executable (.exe/.dll)", true
	case has(0x7F, 'E', 'L', 'F'):
		return "ELF executable (Linux)", true
	case has(0xFE, 0xED, 0xFA, 0xCE), has(0xCF, 0xFA, 0xED, 0xFE):
		return "Mach-O executable (macOS)", true
	case has('P', 'K', 0x03, 0x04):
		return "ZIP/JAR/DOCX/APK архив", false
	case has('%', 'P', 'D', 'F'):
		return "PDF", false
	case has(0x89, 'P', 'N', 'G'):
		return "PNG", false
	case has(0xFF, 0xD8, 0xFF):
		return "JPEG", false
	case has('G', 'I', 'F', '8'):
		return "GIF", false
	case has(0x1F, 0x8B):
		return "gzip архив", false
	case has('R', 'a', 'r', '!'):
		return "RAR архив", false
	case has('#', '!'):
		return "скрипт с shebang (#!)", false
	}
	if isMostlyText(data) {
		return "текст/код", false
	}
	return "бинарные данные (неизвестный тип)", false
}

func isMostlyText(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	n := len(data)
	if n > 4096 {
		n = 4096
	}
	printable := 0
	for _, b := range data[:n] {
		if b == '\t' || b == '\n' || b == '\r' || (b >= 0x20 && b < 0x7F) || b >= 0x80 {
			printable++
		}
	}
	return float64(printable)/float64(n) > 0.90
}

// RenderMarkdown renders the file audit as a Telegram/Markdown message.
func (fa FileAudit) RenderMarkdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "🧬 **Аудит файла**: `%s`\n", fa.Name)
	fmt.Fprintf(&b, "Размер: %d байт · Тип: %s\n", fa.Size, fa.FileType)
	if fa.Executable {
		b.WriteString("🔴 Исполняемый файл — запускай только из доверенного источника.\n")
	}
	fmt.Fprintf(&b, "Энтропия: %.3f/8 — %s\n", fa.Entropy, fa.EntropyNote)
	b.WriteString("\n**Хеши** (для проверки на VirusTotal и т.п.):\n")
	fmt.Fprintf(&b, "- SHA256: `%s`\n", fa.SHA256)
	fmt.Fprintf(&b, "- SHA1: `%s`\n", fa.SHA1)
	fmt.Fprintf(&b, "- MD5: `%s`", fa.MD5)
	return b.String()
}
