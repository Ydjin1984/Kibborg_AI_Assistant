package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
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

// downloadTelegramFileTo стримит файл Telegram сразу на диск. Для видео это принципиально:
// ffmpeg работает с файлами, и держать ролик в памяти ради последующей записи незачем.
func downloadTelegramFileTo(token, filePath, dst string) (int64, error) {
	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", token, filePath)
	resp, err := fileHTTP.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("file download HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		os.Remove(dst)
		return 0, err
	}
	return n, nil
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

// describeImage runs vision with NO tools and returns the plain description (§4, §7:
// «картинка → vision БЕЗ tools → текстовое описание»). Mixing an image with tool schemas
// breaks llama-server in assorted ways, so the two never travel together: the picture is
// turned into text first, and the text is what the dispatcher routes.
func describeImage(cfg Config, chatID int64, prompt, fileID, sysPrompt string) (string, error) {
	if err := requireVisionOK(cfg); err != nil {
		return "", err
	}
	botAPI := "https://api.telegram.org/bot" + cfg.TelegramToken
	filePath, err := getTelegramFilePath(botAPI, fileID)
	if err != nil {
		return "", fmt.Errorf("не смог получить файл из Telegram: %s", redactErr(err))
	}
	data, mime, err := downloadTelegramFile(cfg.TelegramToken, filePath)
	if err != nil {
		return "", fmt.Errorf("не смог скачать изображение: %s", redactErr(err))
	}
	return describeImageBytes(cfg, chatID, prompt, sysPrompt, mime, data)
}

// describeImageBytes is the channel-independent half (Telegram file or Web upload).
func describeImageBytes(cfg Config, chatID int64, prompt, sysPrompt, mime string, data []byte) (string, error) {
	dataURI := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	userMsg := map[string]any{
		"role": "user",
		"content": []map[string]any{
			{"type": "text", "text": prompt},
			{"type": "image_url", "image_url": map[string]any{"url": dataURI}},
		},
	}
	base := baseMessagesWith(chatID, sysPrompt)
	// Long-term memory for ordinary vision chat, but NOT for the chart role: the trading
	// filter must judge what is on the chart, uncontaminated by recalled context.
	if sysPrompt == "" {
		base = withMemory(cfg, chatID, prompt, base)
	}
	temp := defaultTemp
	if sysPrompt != "" {
		temp = chartTemp
	}
	reply, err := llmChat(cfg.BrainPort, append(base, userMsg), temp)
	if err != nil {
		log.Printf("[IMG] vision chat error: %v", err)
		if strings.Contains(err.Error(), "image input is not supported") || strings.Contains(err.Error(), "mmproj") {
			return "", fmt.Errorf("%s", visionUnavailableMsg(cfg))
		}
		return "", fmt.Errorf("ошибка обращения к модели по изображению: %v", err)
	}
	return strings.TrimSpace(stripThink(reply)), nil
}

// chartVisionPrompt asks vision for STRUCTURE, not numbers. Numbers off a screenshot are not
// data (§7): vision reads «BTC 118 024» and is one digit out, and the whole analysis then
// runs on an invented price. The ticker and timeframe are what we actually need from it.
const chartVisionPrompt = `Опиши этот торговый график СТРУКТУРНО, без выдумок:
1. ТИКЕР: первой строкой напиши «ТИКЕР: XXX» (например «ТИКЕР: BTCUSDT»). Если тикер не читается — напиши «ТИКЕР: не определён».
2. ТАЙМФРЕЙМ: второй строкой «ТАЙМФРЕЙМ: 15m/1h/4h/1d» или «не определён».
3. Дальше словами: структура движения (тренд/флэт), где ключевые уровни относительно цены, что с объёмами и индикаторами, если они видны.
Точные цены НЕ выписывай как факт — их я возьму из биржи. Ничего не додумывай.`

// chartVisionSystemPrompt keeps vision in "describe" mode. The verdict is produced later by
// the trade pack over Binance data, not by whatever the screenshot seemed to say.
const chartVisionSystemPrompt = `Ты аккуратно ОПИСЫВАЕШЬ торговый график по скриншоту. Ты не даёшь торговых сигналов, не считаешь уровни и НЕ выписываешь точные цены как факт — их возьмут с биржи. Если чего-то не видно, так и скажи.`

// extractChartTicker resolves the instrument from the user's words first, then from the
// vision description's «ТИКЕР: …» line. It returns "" when nothing is certain — and then the
// agent ASKS instead of guessing (§7: «Если тикер со скрина не определился — спросить»).
func extractChartTicker(userExtra, description string) string {
	for _, tok := range strings.Fields(userExtra) {
		if isTickerToken(tok) {
			return normalizeSymbol(tok)
		}
	}
	for _, line := range strings.Split(description, "\n") {
		up := strings.ToUpper(strings.TrimSpace(line))
		if !strings.HasPrefix(up, "ТИКЕР") {
			continue
		}
		val := up
		if i := strings.IndexAny(up, ":："); i >= 0 {
			val = up[i+1:]
		}
		val = strings.TrimSpace(val)
		if val == "" || strings.Contains(val, "НЕ ОПРЕДЕЛ") {
			return ""
		}
		// Take the first word-ish token: "BTCUSDT (Binance)" → BTCUSDT.
		if f := strings.Fields(val); len(f) > 0 {
			sym := normalizeSymbol(f[0])
			if len(sym) >= 5 {
				return sym
			}
		}
		return ""
	}
	return ""
}

// chatWithImage downloads the image, sends it to the vision model with the prompt,
// and records the exchange in the chat history. A non-empty sysPrompt overrides the
// default system role (used by /chart to switch into the trading-analyst role).
// Only called for mediaImage turns — text/voice never reach here (no image_url).
func chatWithImage(cfg Config, chatID int64, prompt, fileID, sysPrompt string) string {
	if err := requireVisionOK(cfg); err != nil {
		return "❌ " + strings.TrimPrefix(err.Error(), "❌ ")
	}
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
		// Map the common llama.cpp "mmproj missing" 500 to the same guidance as requireVisionOK.
		if strings.Contains(err.Error(), "image input is not supported") ||
			strings.Contains(err.Error(), "mmproj") {
			return visionUnavailableMsg(cfg)
		}
		return "❌ Ошибка обращения к модели по изображению: " + err.Error()
	}
	recordHistory(chatID, "[изображение] "+prompt, reply)
	return reply
}
