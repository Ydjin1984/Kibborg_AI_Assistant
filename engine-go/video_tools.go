package main

// Пак `media` со стороны package main: инструменты, которым нужны сразу ffmpeg (video.go),
// распознавание речи (voice.go), зрение (images.go) и модель для пересказа. Скачивание и
// субтитры живут в browser.ToolsMedia — пак собирается из двух половин (packs.go).

import (
	"fmt"
	"strings"

	"kibborg/engine/browser"
)

// videoToolNames — инструменты, которые исполняет этот файл.
var videoToolNames = map[string]bool{
	"analyze_video":    true,
	"media_info":       true,
	"transcribe_media": true,
	"video_frames":     true,
	"convert_media":    true,
	"speak_text":       true,
}

// videoToolSpecs — схемы. Описания короткие сознательно: TestPackSchemaBudget считает ЛЮБУЮ
// тройку паков, и `media` соседствует с двумя браузерными.
func videoToolSpecs() []browser.ToolSpec {
	return []browser.ToolSpec{
		spec("analyze_video",
			"Разбор видео/аудио: речь в текст + кадры глазами. source = путь или ссылка.",
			objSchema(map[string]any{
				"source":   strSchema("путь или URL"),
				"question": strSchema("что узнать"),
				"frames":   numSchema("кадров; 0 = только речь"),
				"speech":   boolSchema("false = без речи"),
			}, "source")),
		spec("transcribe_media",
			"Только речь в текст, любой длины.",
			objSchema(map[string]any{
				"source": strSchema("путь или URL"),
				"from":   strSchema("с какого времени"),
				"to":     strSchema("по какое"),
			}, "source")),
		spec("video_frames",
			"Кадры из видео + описание глазами.",
			objSchema(map[string]any{
				"source": strSchema("путь"),
				"at":     strSchema("моменты: 1:20,4:05"),
				"count":  numSchema("иначе N равномерно"),
			}, "source")),
		spec("media_info",
			"Метаданные: длительность, кодеки, дорожки.",
			objSchema(map[string]any{
				"path": strSchema(""),
			}, "path")),
		spec("speak_text",
			"Озвучить фразу SuperTonic. Не SAPI и не ffmpeg.",
			objSchema(map[string]any{
				"text": strSchema("что сказать"),
			}, "text")),
		spec("convert_media",
			"ffmpeg: конвертация, обрезка, размер, fps, гиф, вытащить звук.",
			objSchema(map[string]any{
				"input":      strSchema(""),
				"output":     strSchema("иначе рядом с артефактами"),
				"format":     strSchema("mp4|mp3|wav|gif|webm"),
				"from":       strSchema("начало"),
				"to":         strSchema("конец"),
				"scale":      strSchema("720 | 1280x720"),
				"fps":        numSchema(""),
				"audio_only": boolSchema(""),
				"mute":       boolSchema(""),
			}, "input")),
	}
}

// dispatchVideoTool исполняет один инструмент пака. ok=false — «не мой».
func dispatchVideoTool(t *Task, cfg Config, name string, args map[string]any) (ToolResult, bool) {
	if !videoToolNames[name] {
		return ToolResult{}, false
	}
	switch name {
	case "analyze_video":
		return toolAnalyzeVideo(t, cfg, args), true
	case "transcribe_media":
		return toolTranscribeMedia(t, cfg, args), true
	case "video_frames":
		return toolVideoFrames(t, cfg, args), true
	case "media_info":
		return toolMediaInfo(t, cfg, args), true
	case "convert_media":
		return toolConvertMedia(t, cfg, args), true
	case "speak_text":
		return toolSpeakText(t, cfg, args), true
	}
	return ToolResult{}, false
}

func toolAnalyzeVideo(t *Task, cfg Config, args map[string]any) ToolResult {
	source := argString(args, "source")
	if source == "" {
		return failResult("нужен source: путь к файлу или ссылка на видео", nil)
	}
	// frames отсутствует = «решай сам» (-1); frames=0 = «речь и без кадров».
	frames := -1
	if _, ok := args["frames"]; ok {
		frames = int(argFloat(args, "frames"))
	}
	speech := true
	if v, ok := args["speech"]; ok {
		speech = argBoolValue(v, true)
	}
	digest, err := analyzeMedia(t.Context(), cfg, mediaAnalyzeOpts{
		Source:   source,
		Question: argString(args, "question"),
		Frames:   frames,
		Speech:   speech,
		From:     argTimeSec(args, "from"),
		To:       argTimeSec(args, "to"),
		ChatID:   t.ChatID,
	})
	if err != nil {
		return failResult("разбор видео не вышел: "+err.Error(), err)
	}
	var artifacts []string
	if digest.TranscriptPath != "" {
		artifacts = append(artifacts, digest.TranscriptPath)
	}
	return okResult(digest.Render(), artifacts)
}

func toolTranscribeMedia(t *Task, cfg Config, args map[string]any) ToolResult {
	source := argString(args, "source")
	if source == "" {
		return failResult("нужен source: путь к файлу или ссылка", nil)
	}
	digest, err := analyzeMedia(t.Context(), cfg, mediaAnalyzeOpts{
		Source: source,
		Frames: 0, // текст просили, кадры не просили
		Speech: true,
		From:   argTimeSec(args, "from"),
		To:     argTimeSec(args, "to"),
		ChatID: t.ChatID,
	})
	if err != nil {
		return failResult("расшифровка не вышла: "+err.Error(), err)
	}
	if digest.Transcript == "" {
		return failResult("речи в этом файле не нашлось", nil)
	}
	text := digest.Transcript
	if digest.Summary != "" {
		text = "Выжимка:\n" + digest.Summary + "\n\nНачало дословной расшифровки:\n" +
			capAgentText(digest.Transcript, transcriptInlineChars)
	}
	if digest.TranscriptPath != "" {
		text += fmt.Sprintf("\n\n📄 Полный текст: %s", digest.TranscriptPath)
		return okResult(text, []string{digest.TranscriptPath})
	}
	return okResult(text, nil)
}

func toolVideoFrames(t *Task, cfg Config, args map[string]any) ToolResult {
	source := argString(args, "source")
	if source == "" {
		source = argString(args, "path")
	}
	if source == "" {
		return failResult("нужен source: путь к видеофайлу", nil)
	}
	if err := ffmpegReady(cfg); err != nil {
		return failResult(err.Error(), err)
	}
	ctx := t.Context()
	src, err := resolveMediaSource(ctx, cfg, source, true, nil)
	if err != nil {
		return failResult(err.Error(), err)
	}
	info, perr := probeMedia(ctx, cfg, src.Path)
	if perr != nil {
		return failResult(perr.Error(), perr)
	}
	if !info.hasVideo() {
		return failResult("в файле нет видеодорожки — кадры брать не из чего", nil)
	}

	var at []float64
	for _, part := range strings.Split(argString(args, "at"), ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		if sec, ok := parseTimeSpec(part); ok {
			at = append(at, sec)
		}
	}
	if len(at) == 0 {
		n := int(argFloat(args, "count"))
		if n <= 0 {
			n = 4
		}
		if n > maxFrames {
			n = maxFrames
		}
		at = frameTimes(info.Duration, n)
	}
	if len(at) > maxFrames {
		at = at[:maxFrames]
	}

	frames, ferr := extractFrames(ctx, cfg, src.Path, at)
	if ferr != nil {
		return failResult(ferr.Error(), ferr)
	}
	if verr := requireVisionOK(cfg); verr != nil {
		paths := make([]string, 0, len(frames))
		for _, f := range frames {
			paths = append(paths, f.Path)
		}
		return okResult("кадры вынуты, но посмотреть на них не могу: "+verr.Error()+
			"\nФайлы: "+strings.Join(paths, ", "), paths)
	}
	notes := describeFrames(ctx, cfg, t.ChatID, frames)
	paths := make([]string, 0, len(frames))
	for _, f := range frames {
		paths = append(paths, f.Path)
	}
	return okResult("👁 Кадры из "+src.Path+":\n- "+strings.Join(notes, "\n- "), paths)
}

func toolMediaInfo(t *Task, cfg Config, args map[string]any) ToolResult {
	path := argString(args, "path")
	if path == "" {
		path = argString(args, "source")
	}
	if path == "" {
		return failResult("нужен path", nil)
	}
	if err := ffmpegReady(cfg); err != nil {
		return failResult(err.Error(), err)
	}
	info, err := probeMedia(t.Context(), cfg, path)
	if err != nil {
		return failResult(err.Error(), err)
	}
	return okResult(info.Render(), nil)
}

func toolConvertMedia(t *Task, cfg Config, args map[string]any) ToolResult {
	in := argString(args, "input")
	if in == "" {
		return failResult("нужен input — путь к исходному файлу", nil)
	}
	out, info, err := convertMedia(t.Context(), cfg, convertOpts{
		Input:     in,
		Output:    argString(args, "output"),
		Format:    argString(args, "format"),
		From:      argTimeSec(args, "from"),
		To:        argTimeSec(args, "to"),
		Scale:     argString(args, "scale"),
		FPS:       argFloat(args, "fps"),
		AudioOnly: argBool(args, "audio_only"),
		Mute:      argBool(args, "mute"),
	})
	if err != nil {
		return failResult("конвертация не вышла: "+err.Error(), err)
	}
	text := "готово: " + out
	if info.Duration > 0 || info.Bytes > 0 {
		text += "\n" + info.Render()
	}
	return okResult(text, []string{out})
}

// argBool читает булев аргумент, который модель шлёт то булевым, то строкой «true»/«да».
func argBool(args map[string]any, key string) bool {
	v, ok := args[key]
	if !ok {
		return false
	}
	return argBoolValue(v, false)
}

func argBoolValue(v any, def bool) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "1", "yes", "да", "on":
			return true
		case "false", "0", "no", "нет", "off":
			return false
		}
	case float64:
		return t != 0
	}
	return def
}
