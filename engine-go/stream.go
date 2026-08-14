package main

// Live streaming of LLM answers into Telegram: the reply appears as one message that is
// edited in place (~every 1.5s) while llama-server generates, then gets a final pass with
// full HTML formatting. Telegram tolerates roughly one edit per second per chat, so the
// cadence stays under the flood limits.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	streamEditInterval = 1500 * time.Millisecond
	// One Telegram message worth of preview; the tail beyond this arrives at finish().
	streamPreviewCap = 3000
)

// unclosedThinkRe hides a reasoning block that is still being generated (no </think> yet).
var unclosedThinkRe = regexp.MustCompile(`(?s)<think>.*$`)

// streamPreview turns a partial LLM answer into displayable text: closed and still-open
// <think> blocks are hidden, so the user never sees raw chain-of-thought.
func streamPreview(s string) string {
	s = thinkRe.ReplaceAllString(s, "")
	s = unclosedThinkRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// liveMessage is the Telegram message being updated during one streamed reply.
type liveMessage struct {
	botAPI     string
	chatID     int64
	msgID      int
	lastLen    int
	lastAt     time.Time
	overflowed bool
	stopTyping func() // invoked once, when the first visible text is posted
}

// update is the llmChatStream onDelta callback: throttled plain-text edits with a cursor.
func (lm *liveMessage) update(full string) {
	if lm.overflowed {
		return
	}
	preview := streamPreview(full)
	if preview == "" {
		return // model is still thinking; the typing indicator covers this phase
	}
	if time.Since(lm.lastAt) < streamEditInterval {
		return
	}
	lm.lastAt = time.Now()
	if len(preview) > streamPreviewCap {
		cut := streamPreviewCap
		for cut > 0 && !utf8.RuneStart(preview[cut]) {
			cut--
		}
		preview = preview[:cut]
		lm.overflowed = true // message is visually full; the rest arrives in finish()
	}
	if lm.msgID == 0 {
		lm.msgID = sendTelegramRawID(lm.botAPI, lm.chatID, preview+" ▌", "")
		if lm.msgID != 0 && lm.stopTyping != nil {
			lm.stopTyping()
		}
	} else if len(preview) != lm.lastLen {
		editTelegramRaw(lm.botAPI, lm.chatID, lm.msgID, preview+" ▌", "")
	}
	lm.lastLen = len(preview)
}

// finish replaces the live preview with the final, fully formatted answer. The first
// splitMessage chunk is edited into the streamed message; the rest are sent as usual.
func (lm *liveMessage) finish(final string) {
	final = stripThink(final)
	if lm.msgID == 0 {
		sendTelegramMessage(lm.botAPI, lm.chatID, final)
		return
	}
	chunks := splitMessage(final, 3000)
	html := toTelegramHTML(chunks[0])
	if !editTelegramRaw(lm.botAPI, lm.chatID, lm.msgID, html, "HTML") {
		if !editTelegramRaw(lm.botAPI, lm.chatID, lm.msgID, stripTags(html), "") {
			log.Printf("[TELEGRAM] final edit failed for %d (html and plain)", lm.chatID)
		}
	}
	for _, chunk := range chunks[1:] {
		sendTelegramChunk(lm.botAPI, lm.chatID, chunk)
	}
}

// chatWithHistoryStream handles a plain text turn with a live-updating Telegram message.
// The reply is delivered by this function (streamed); the caller sends nothing after it.
func chatWithHistoryStream(cfg Config, botAPI string, chatID int64, userText string, stopTyping func()) {
	base := withMemory(cfg, chatID, userText, baseMessages(chatID))
	msgs := append(base, map[string]any{"role": "user", "content": userText})
	lm := &liveMessage{botAPI: botAPI, chatID: chatID, stopTyping: stopTyping}
	live.begin("чат")
	reply, stats, err := llmChatStream(cfg.BrainPort, msgs, defaultTemp, lm.update)
	live.finish(stats)
	if err != nil {
		log.Printf("[LLM] stream chat error: %v", err)
		lm.finish("❌ Ошибка обращения к модели: " + err.Error())
		return
	}
	recordHistory(chatID, userText, reply)
	lm.finish(reply + statsFooter(stats))
	speakAfterReply(cfg, botAPI, chatID, userText, reply)
	log.Printf("[TELEGRAM] replied to %d (stream, %d chars, %.1f tok/s)", chatID, len(reply), stats.GenPerSec)
}

// statsFooter renders a compact speed line appended under a finished reply.
func statsFooter(s GenStats) string {
	if s.GenTokens == 0 {
		return ""
	}
	return fmt.Sprintf("\n\n`⚡ %.0f ток/с генерация · %.0f ток/с промпт · %d ток за %.1fс`",
		s.GenPerSec, s.PromptPerSec, s.GenTokens, s.TotalMs/1000)
}

// sendTelegramRawID sends a message and returns its message_id (0 on failure) so the
// streaming path can edit it later.
func sendTelegramRawID(botAPI string, chatID int64, text, parseMode string) int {
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("text", text)
	if parseMode != "" {
		form.Set("parse_mode", parseMode)
	}
	resp, err := tgHTTP.PostForm(botAPI+"/sendMessage", form)
	if err != nil {
		log.Printf("[TELEGRAM] send error to %d: %s", chatID, redactErr(err))
		return 0
	}
	defer resp.Body.Close()
	var data struct {
		Ok     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil || !data.Ok {
		return 0
	}
	return data.Result.MessageID
}

// editTelegramRaw edits a previously sent message in place.
func editTelegramRaw(botAPI string, chatID int64, msgID int, text, parseMode string) bool {
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("message_id", strconv.Itoa(msgID))
	form.Set("text", text)
	if parseMode != "" {
		form.Set("parse_mode", parseMode)
	}
	resp, err := tgHTTP.PostForm(botAPI+"/editMessageText", form)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}
