package main

// Media routing: one classifier for Telegram + Web so each upload hits the right pipeline.
// Text/voice/files never send image_url (vision stays idle). Only real images go to vision.

import (
	"fmt"
	"path/filepath"
	"strings"
)

// MediaKind is what the user attached (or plain text with no file).
type MediaKind int

const (
	mediaNone     MediaKind = iota // plain text / no attachment
	mediaImage                     // photo / png / jpg / chart screenshot → vision
	mediaAudio                     // voice note / music → STT → text chat
	mediaVideo                     // mp4 / mov / … — not vision, not STT here
	mediaTextFile                  // code / markdown / json → embed as text
	mediaOther                     // unknown binary
)

func (k MediaKind) String() string {
	switch k {
	case mediaNone:
		return "text"
	case mediaImage:
		return "image"
	case mediaAudio:
		return "audio"
	case mediaVideo:
		return "video"
	case mediaTextFile:
		return "textfile"
	default:
		return "other"
	}
}

// classifyMedia decides the pipeline from filename + Content-Type.
// Mime wins when specific (image/*, audio/*, video/*); extension fills gaps
// (Telegram often sends application/octet-stream for code and images-as-files).
func classifyMedia(name, mime string) MediaKind {
	m := strings.ToLower(strings.TrimSpace(mime))
	ext := strings.ToLower(filepath.Ext(name))

	// Explicit mime first — most reliable for browser uploads.
	switch {
	case strings.HasPrefix(m, "image/"):
		return mediaImage
	case strings.HasPrefix(m, "video/"):
		return mediaVideo
	case strings.HasPrefix(m, "audio/"):
		return mediaAudio
	}

	// webm/ogg: browser mic is audio; real video files usually carry video/* mime.
	// Without a video/* mime, treat as audio (Web 🎙 / Telegram voice fallbacks).
	if strings.Contains(m, "webm") || strings.Contains(m, "ogg") {
		return mediaAudio
	}

	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp", ".jfif", ".heic", ".heif":
		return mediaImage
	case ".mp4", ".mov", ".mkv", ".avi", ".webm", ".m4v", ".mpeg", ".mpg", ".wmv", ".3gp":
		// .webm by extension alone: could be mic or video. Prefer video for bare files
		// named *.webm from disk; browser FormData mic usually sets audio/webm mime above.
		if ext == ".webm" && (m == "" || m == "application/octet-stream") {
			return mediaAudio // safer for voice notes saved without mime
		}
		return mediaVideo
	case ".ogg", ".oga", ".opus", ".mp3", ".m4a", ".wav", ".flac", ".aac", ".wma":
		return mediaAudio
	}

	if isTextFile(name, mime) {
		return mediaTextFile
	}
	if name == "" && mime == "" {
		return mediaNone
	}
	return mediaOther
}

// isImageUpload is kept for callers; delegates to classifyMedia.
func isImageUpload(name, mime string) bool {
	return classifyMedia(name, mime) == mediaImage
}

// imageUploadMime resolves the data-URI mime for an uploaded image.
func imageUploadMime(name, mime string) string {
	if strings.HasPrefix(strings.ToLower(mime), "image/") {
		return mime
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".bmp":
		return "image/bmp"
	default:
		return "image/jpeg"
	}
}

// visionUnavailableMsg explains why an image cannot be analysed right now.
func visionUnavailableMsg(cfg Config) string {
	mp := strings.TrimSpace(cfg.MmprojPath)
	if mp == "" || strings.EqualFold(mp, "off") || strings.EqualFold(mp, "none") {
		return "❌ Зрение выключено: в settings.ini задай MMPROJ_PATH " +
			"(например models\\vision\\mmproj-BF16.gguf или auto), затем полностью перезапусти brain (Stop → Start)."
	}
	return "❌ Мозг запущен без зрения (mmproj не подключён). " +
		"В settings.ini MMPROJ_PATH уже есть — останови llama-server и перезапусти Kibborg, " +
		"чтобы подхватить vision. Текст и голос работают без этого."
}

// requireVisionOK returns a user-facing error if the brain cannot accept image_url.
// Pure text chat never calls this — vision cost is only paid on image turns at the
// request layer; the server still needs mmproj loaded once at process start.
func requireVisionOK(cfg Config) error {
	if !brainReady(cfg.BrainPort) {
		return fmt.Errorf("модель ещё грузится — пришли картинку чуть позже")
	}
	if brainHasVision(cfg.BrainPort) {
		return nil
	}
	return fmt.Errorf("%s", visionUnavailableMsg(cfg))
}
