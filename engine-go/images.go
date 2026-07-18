package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var fileHTTP = &http.Client{Timeout: 60 * time.Second}

// getTelegramFilePath resolves a file_id to a downloadable file path via getFile.
func getTelegramFilePath(botAPI, fileID string) (string, error) {
	resp, err := fileHTTP.Get(botAPI + "/getFile?file_id=" + url.QueryEscape(fileID))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var data struct {
		Ok     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	if !data.Ok || data.Result.FilePath == "" {
		return "", fmt.Errorf("getFile failed")
	}
	return data.Result.FilePath, nil
}

// downloadTelegramFile fetches the file bytes and guesses its image MIME type.
func downloadTelegramFile(token, filePath string) ([]byte, string, error) {
	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", token, filePath)
	resp, err := fileHTTP.Get(url)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("file download HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20_000_000))
	if err != nil {
		return nil, "", err
	}
	return data, mimeFromPath(filePath), nil
}

func mimeFromPath(p string) string {
	lp := strings.ToLower(p)
	switch {
	case strings.HasSuffix(lp, ".png"):
		return "image/png"
	case strings.HasSuffix(lp, ".webp"):
		return "image/webp"
	case strings.HasSuffix(lp, ".gif"):
		return "image/gif"
	default:
		return "image/jpeg"
	}
}

// chatWithImage downloads the image, sends it to the vision model with the prompt,
// and records the exchange in the chat history. A non-empty sysPrompt overrides the
// default system role (used by /chart to switch into the trading-analyst role).
func chatWithImage(cfg Config, chatID int64, prompt, fileID, sysPrompt string) string {
	botAPI := "https://api.telegram.org/bot" + cfg.TelegramToken
	filePath, err := getTelegramFilePath(botAPI, fileID)
	if err != nil {
		log.Printf("[IMG] getFile error: %s", redactErr(err))
		return "❌ Не смог получить файл из Telegram: " + redactErr(err)
	}
	data, mime, err := downloadTelegramFile(cfg.TelegramToken, filePath)
	if err != nil {
		log.Printf("[IMG] download error: %s", redactErr(err))
		return "❌ Не смог скачать изображение: " + redactErr(err)
	}
	dataURI := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)

	userMsg := map[string]any{
		"role": "user",
		"content": []map[string]any{
			{"type": "text", "text": prompt},
			{"type": "image_url", "image_url": map[string]any{"url": dataURI}},
		},
	}
	base := baseMessagesWith(chatID, sysPrompt)
	// Inject long-term memory for ordinary vision chat, but NOT for /chart: the trading filter
	// must judge only what's on the chart, uncontaminated by recalled context.
	if sysPrompt == "" {
		base = withMemory(cfg, chatID, prompt, base)
	}
	msgs := append(base, userMsg)

	// Chart analysis (sysPrompt overridden) runs cooler to keep numbers honest.
	temp := defaultTemp
	if sysPrompt != "" {
		temp = chartTemp
	}
	reply, err := llmChat(cfg.BrainPort, msgs, temp)
	if err != nil {
		log.Printf("[IMG] vision chat error: %v", err)
		return "❌ Ошибка обращения к модели по изображению: " + err.Error()
	}
	recordHistory(chatID, "[изображение] "+prompt, reply)
	return reply
}
