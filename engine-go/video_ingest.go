package main

// Приём ролика, присланного прямо в чат (§7, паритет каналов). Telegram и Web ведут видео по
// ОДНОЙ трубе, и труба та же, что у картинок (§4):
//
//	видео → ffmpeg+STT+зрение → ТЕКСТ → дальше как обычный текстовый запрос
//
// Модель никогда не видит ни пикселей ролика, ни его звука — она видит расшифровку и описания
// кадров. Поэтому «разбери это видео и найди проект на гитхабе» работает само собой: к моменту
// выбора инструментов задача уже текстовая.

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// telegramFileLimit — потолок Bot API на СКАЧИВАНИЕ файла ботом. Это ограничение Telegram,
	// а не наше: getFile на файле больше 20 МБ отвечает отказом, и обойти его нечем.
	telegramFileLimit = 20 << 20
	// webVideoUploadLimit — потолок загрузки в веб-панель. Здесь ограничивает только диск.
	webVideoUploadLimit = 2 << 30
	// ingestDigestChars — сколько разбора уходит в историю диалога.
	ingestDigestChars = 6000
)

// videoTooBigNote честно объясняет ограничение Telegram и называет ДВА рабочих пути вместо
// него. «Не могу» без выхода — это то самое объяснение ограничений вместо решения.
func videoTooBigNote(size int64) string {
	return fmt.Sprintf("🎬 Ролик весит %s, а Telegram отдаёт ботам максимум %s — это ограничение их API, не моё.\n\n"+
		"Два пути, оба рабочие:\n"+
		"• пришли ССЫЛКУ на ролик (YouTube, Instagram, TikTok…) — скачаю сам и разберу;\n"+
		"• положи файл на диск и напиши путь: «разбери D:\\видео\\лекция.mp4» — размер тогда не важен.",
		humanBytes(size), humanBytes(telegramFileLimit))
}

// throttledNotice ограничивает частоту сообщений о ходе работы: разбор часового ролика
// идёт минутами, и молчание пугает, а поток статусов засоряет чат.
func throttledNotice(send func(string), every time.Duration) func(string) {
	var mu sync.Mutex
	last := time.Now()
	return func(s string) {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(last) < every {
			return
		}
		last = time.Now()
		send("⏳ " + s)
	}
}

// ingestDigestPrompt складывает подпись пользователя и разбор в одну текстовую задачу.
func ingestDigestPrompt(caption string, d mediaDigest) string {
	body := "[Разбор приложенного видео: речь распознана, кадры описаны]\n" + d.Render()
	if strings.TrimSpace(caption) == "" {
		return body
	}
	return strings.TrimSpace(caption) + "\n\n" + body
}

// ===== Telegram =====

// handleTelegramVideo разбирает присланный ролик и передаёт РЕЗУЛЬТАТ дальше по обычному пути.
func handleTelegramVideo(cfg Config, botAPI string, allow map[int64]bool, chatID int64, v *tgVideo, caption string) {
	if err := ffmpegReady(cfg); err != nil {
		sendTelegramMessage(botAPI, chatID, "❌ "+err.Error())
		return
	}
	if v.FileSize > telegramFileLimit {
		sendTelegramMessage(botAPI, chatID, videoTooBigNote(v.FileSize))
		return
	}
	if !voiceEnabled(cfg) && !brainHasVision(cfg.BrainPort) {
		sendTelegramMessage(botAPI, chatID,
			"🎬 Ролик пришёл, но разбирать его нечем: распознавание речи выключено и зрение не подключено.\n"+
				voiceSetupHelp())
		return
	}

	stop := startTyping(botAPI, chatID)
	defer stop()

	head := "🎬 Взял видео"
	if v.Duration > 0 {
		head += " (" + fmtMediaDuration(float64(v.Duration)) + ")"
	}
	sendTelegramMessage(botAPI, chatID, head+" — вытаскиваю речь и смотрю кадры. Это может занять несколько минут.")

	path, err := downloadTelegramVideo(cfg, botAPI, v)
	if err != nil {
		sendTelegramMessage(botAPI, chatID, "❌ Не смог получить видео: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), videoIngestTimeout)
	defer cancel()
	notice := throttledNotice(func(s string) { sendTelegramMessage(botAPI, chatID, s) }, 45*time.Second)
	digest, err := analyzeMedia(ctx, cfg, mediaAnalyzeOpts{
		Source:   path,
		Question: caption,
		Frames:   -1, // «решай сам»: см. autoFrameCount
		Speech:   true,
		ChatID:   chatID,
		Progress: notice,
	})
	stop()
	if err != nil {
		sendTelegramMessage(botAPI, chatID, "❌ Разбор не вышел: "+err.Error())
		return
	}
	log.Printf("[VIDEO] telegram %d: разобрано %s, расшифровка %d символов, кадров %d",
		chatID, fmtMediaDuration(digest.Info.Duration), len([]rune(digest.Transcript)), len(digest.Frames))

	sendTelegramMessage(botAPI, chatID, digest.Render())
	if digest.TranscriptPath != "" {
		if err := sendTelegramDocumentFile(botAPI, chatID, digest.TranscriptPath, "📄 Полная расшифровка"); err != nil {
			log.Printf("[VIDEO] расшифровку файлом не отправил: %v", err)
		}
	}

	// Подписи нет — разбор и есть ответ; он ложится в историю, чтобы следующий вопрос
	// («а что за проект он упоминал?») отвечался уже по нему.
	if strings.TrimSpace(caption) == "" || allow == nil {
		recordHistory(chatID, "[видео] "+caption, capAgentText(digest.Render(), ingestDigestChars))
		return
	}
	runTelegramAgent(cfg, botAPI, allow, chatID, ingestDigestPrompt(caption, digest))
}

// downloadTelegramVideo кладёт ролик на диск: ffmpeg работает с файлами, а не с байтами
// в памяти, и держать сотни мегабайт в куче ради этого незачем.
func downloadTelegramVideo(cfg Config, botAPI string, v *tgVideo) (string, error) {
	filePath, err := getTelegramFilePath(botAPI, v.FileID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "too big") {
			return "", fmt.Errorf("%s", videoTooBigNote(v.FileSize))
		}
		return "", err
	}
	if err := os.MkdirAll(mediaWorkDir, 0o755); err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext == "" {
		ext = ".mp4"
	}
	name := safeFileStem(stripStampPrefix(strings.TrimSuffix(v.FileName, filepath.Ext(v.FileName))))
	if name == "media" {
		name = "telegram"
	}
	dst := filepath.Join(mediaWorkDir, fmt.Sprintf("%s-%s%s", time.Now().Format("20060102-150405"), name, ext))
	if _, err := downloadTelegramFileTo(cfg.TelegramToken, filePath, dst); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(dst)
	if err != nil {
		return dst, nil
	}
	return abs, nil
}

// ===== Web =====

// handleWebVideoTurn — веб-близнец: тот же разбор, тот же маршрут результата в агента.
func handleWebVideoTurn(w http.ResponseWriter, cfg Config, caption, name, mime string, src io.Reader) {
	if err := ffmpegReady(cfg); err != nil {
		webChatReply(w, "❌ "+err.Error())
		return
	}
	path, size, err := saveUploadedVideo(name, src)
	if err != nil {
		webChatReply(w, "❌ Не смог сохранить ролик: "+err.Error())
		return
	}
	log.Printf("[WEB] видео %s (%s) → разбор", name, humanBytes(size))

	// Разбор идёт минутами. Без ленты статусов панель просто молчит, и это выглядит как
	// зависание — те же строки, что показывает агент (§8: статусы да, размышления нет).
	emit := ndjsonEmitter(w)
	emit(map[string]any{"status": "🎬 Разбираю видео " + name + " (" + humanBytes(size) + ")"})

	ctx, cancel := context.WithTimeout(context.Background(), videoIngestTimeout)
	defer cancel()
	digest, aerr := analyzeMedia(ctx, cfg, mediaAnalyzeOpts{
		Source:   path,
		Question: caption,
		Frames:   -1,
		Speech:   true,
		ChatID:   webChatID,
		Progress: func(s string) { emit(map[string]any{"status": "🎬 " + s}) },
	})
	if aerr != nil {
		emit(map[string]any{"done": true, "text": "❌ Разбор видео не вышел: " + aerr.Error()})
		return
	}
	if strings.TrimSpace(caption) == "" {
		recordHistory(webChatID, "[видео "+name+"]", capAgentText(digest.Render(), ingestDigestChars))
		var files []string
		if digest.TranscriptPath != "" {
			files = append(files, digest.TranscriptPath)
		}
		emit(map[string]any{"done": true, "text": digest.Render(), "files": webArtifactLinks(files)})
		return
	}
	streamWebAgent(w, cfg, ingestDigestPrompt(caption, digest), "[видео "+name+"] "+caption, nil, "")
}

// saveUploadedVideo стримит загрузку на диск, не собирая её в памяти.
func saveUploadedVideo(name string, src io.Reader) (string, int64, error) {
	if err := os.MkdirAll(mediaWorkDir, 0o755); err != nil {
		return "", 0, err
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = ".mp4"
	}
	// Снимаем НАШУ же метку времени, если пришёл ранее сохранённый нами файл: иначе имя
	// обрастает метками на каждый круг («20260813-154424-20260813-152526-telegram.mp4»).
	stem := safeFileStem(stripStampPrefix(strings.TrimSuffix(name, filepath.Ext(name))))
	dst := filepath.Join(mediaWorkDir, fmt.Sprintf("%s-%s%s", time.Now().Format("20060102-150405"), stem, ext))
	f, err := os.Create(dst)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(src, webVideoUploadLimit))
	if err != nil {
		os.Remove(dst)
		return "", 0, err
	}
	if n == 0 {
		os.Remove(dst)
		return "", 0, fmt.Errorf("файл пустой")
	}
	abs, aerr := filepath.Abs(dst)
	if aerr != nil {
		abs = dst
	}
	return abs, n, nil
}
