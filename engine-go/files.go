package main

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// maxFileChars caps how much of a file we feed the model so the prompt fits the context
// window (32K tokens) with room left for the answer.
const maxFileChars = 60000

// codeLang maps a file extension to a Markdown code-fence language for nicer rendering.
var codeLang = map[string]string{
	".go": "go", ".py": "python", ".js": "javascript", ".ts": "typescript",
	".tsx": "tsx", ".jsx": "jsx", ".rs": "rust", ".c": "c", ".h": "c",
	".cpp": "cpp", ".cc": "cpp", ".hpp": "cpp", ".java": "java", ".kt": "kotlin",
	".cs": "csharp", ".rb": "ruby", ".php": "php", ".swift": "swift",
	".sh": "bash", ".bash": "bash", ".ps1": "powershell", ".bat": "bat", ".cmd": "bat",
	".sql": "sql", ".html": "html", ".htm": "html", ".css": "css", ".scss": "scss",
	".json": "json", ".yaml": "yaml", ".yml": "yaml", ".toml": "toml", ".ini": "ini",
	".xml": "xml", ".md": "markdown", ".csv": "csv", ".env": "ini",
	".txt": "", ".log": "", ".conf": "ini", ".gradle": "groovy", ".dockerfile": "dockerfile",
	".vue": "vue", ".svelte": "svelte", ".r": "r", ".lua": "lua", ".pl": "perl",
	".zig": "zig", ".nim": "nim", ".ex": "elixir", ".exs": "elixir", ".erl": "erlang",
	".tf": "hcl", ".hcl": "hcl", ".proto": "protobuf", ".graphql": "graphql",
}

// isTextFile decides whether a Telegram document is a readable text/code file we can
// feed to the model. Extension is the primary signal (Telegram often reports
// application/octet-stream for code), with text/* mime as a fallback.
func isTextFile(filename, mime string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	if _, ok := codeLang[ext]; ok {
		return true
	}
	base := strings.ToLower(filename)
	if base == "dockerfile" || base == "makefile" || base == "go.mod" || base == "go.sum" {
		return true
	}
	return strings.HasPrefix(mime, "text/") ||
		strings.Contains(mime, "json") || strings.Contains(mime, "xml") ||
		strings.Contains(mime, "javascript") || strings.Contains(mime, "x-yaml")
}

// chatWithFile downloads a text/code document, embeds it in the prompt, and asks the model.
func chatWithFile(cfg Config, chatID int64, prompt string, doc *tgDocument) string {
	botAPI := "https://api.telegram.org/bot" + cfg.TelegramToken
	filePath, err := getTelegramFilePath(botAPI, doc.FileID)
	if err != nil {
		log.Printf("[FILE] getFile error: %s", redactErr(err))
		return "❌ Не смог получить файл из Telegram: " + redactErr(err)
	}
	data, _, err := downloadTelegramFile(cfg.TelegramToken, filePath)
	if err != nil {
		log.Printf("[FILE] download error: %s", redactErr(err))
		return "❌ Не смог скачать файл: " + redactErr(err)
	}
	if !utf8.Valid(data) || bytesHaveNUL(data) {
		return "❌ Это похоже на бинарный файл — могу читать только текст и код (.go, .py, .md, .txt, .json и т.п.)."
	}

	content := string(data)
	truncated := false
	if len(content) > maxFileChars {
		cut := maxFileChars
		// back off to a rune boundary so the truncated tail is still valid UTF-8
		for cut > 0 && !utf8.RuneStart(content[cut]) {
			cut--
		}
		content = content[:cut]
		truncated = true
	}

	lang := codeLang[strings.ToLower(filepath.Ext(doc.FileName))]
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\nФайл `")
	b.WriteString(doc.FileName)
	b.WriteString("`")
	if truncated {
		b.WriteString(" (показан только первый фрагмент — файл большой)")
	}
	b.WriteString(":\n```")
	b.WriteString(lang)
	b.WriteString("\n")
	b.WriteString(content)
	b.WriteString("\n```")

	base := withMemory(cfg, chatID, prompt, baseMessages(chatID))
	msgs := append(base, map[string]any{"role": "user", "content": b.String()})
	reply, err := llmChat(cfg.BrainPort, msgs, defaultTemp)
	if err != nil {
		log.Printf("[FILE] chat error: %v", err)
		return "❌ Ошибка обращения к модели по файлу: " + err.Error()
	}
	recordHistory(chatID, fmt.Sprintf("[файл %s] %s", doc.FileName, prompt), reply)
	return reply
}

func bytesHaveNUL(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}
