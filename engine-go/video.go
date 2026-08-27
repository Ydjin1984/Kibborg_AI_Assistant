package main

// Слой видео (ТЗ §21). Своей «модели для видео» здесь нет и не нужно: видео — это контейнер,
// а не модальность. ffmpeg разрезает ролик на то, что движок УЖЕ умеет глотать:
//
//	дорожка речи  → voice.go  → TypeWhisper/whisper.cpp → текст
//	кадры         → images.go → зрение (mmproj)          → текст
//	метаданные    → ffprobe                              → текст
//
// Поэтому длина ролика упирается не в окно контекста, а в диск: речь режется на куски по
// sttChunkSeconds, каждый распознаётся отдельно, и в контекст модели уходит СВОДКА, а полная
// расшифровка ложится файлом рядом — за деталями модель сходит read_file'ом.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"kibborg/engine/browser"
)

const (
	// mediaWorkDir — рабочий каталог слоя: скачанное, расшифровки, кадры, конвертации.
	// Лежит ВНУТРИ runtime/browser намеренно: /api/files отдаёт веб-панели только этот корень,
	// и расшифровка, сохранённая мимо него, стала бы файлом без ссылки (как у пака system).
	mediaWorkDir = "runtime/browser/media"

	// sttChunkSeconds — длина куска для распознавания. Пять минут: TypeWhisper переваривает
	// такой кусок за секунды, а таймаут HTTP-клиента (5 мин) остаётся с большим запасом.
	sttChunkSeconds = 300
	// maxSTTChunks — предохранитель от бесконечности: 200 кусков ≈ 16 часов записи.
	maxSTTChunks = 200

	// transcriptInlineChars — до этого размера расшифровка уходит модели ДОСЛОВНО. Дальше её
	// пересказывает summariseTranscript: 6000 символов ≈ 2400 токенов, это честная доля
	// 32K-окна под один инструмент.
	transcriptInlineChars = 6000
	// transcriptChunkChars — размер куска для карты-свёртки при пересказе.
	transcriptChunkChars = 6000
	// maxSummaryCalls — потолок вызовов модели на один пересказ (≈4 часа речи).
	maxSummaryCalls = 40

	// frameMaxSide — кадр ужимается перед зрением: 1200 px хватает, чтобы прочитать код на
	// слайде, и не хватает, чтобы раздуть промпт.
	frameMaxSide = 1200
	// maxFrames — потолок кадров за один разбор (каждый кадр — отдельный запрос к зрению).
	maxFrames = 12

	// videoIngestTimeout — потолок для разбора ролика, присланного в чат напрямую. Это НЕ
	// задача агента: тут нет цикла инструментов, и десятиминутный TaskTimeout не применяется.
	videoIngestTimeout = 30 * time.Minute
)

// ===== доступность инструментов =====

// ffprobeExe резолвит ffprobe: рядом с заданным ffmpeg, иначе из PATH.
func ffprobeExe(cfg Config) string {
	if p := strings.TrimSpace(cfg.FfmpegPath); p != "" {
		name := "ffprobe"
		if strings.HasSuffix(strings.ToLower(p), ".exe") {
			name = "ffprobe.exe"
		}
		cand := filepath.Join(filepath.Dir(p), name)
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand
		}
	}
	return "ffprobe"
}

// ffmpegReady проверяет ОБА бинаря и объясняет, как поставить, если их нет. Молчаливое
// «не получилось» здесь хуже всего: без ffmpeg не работает ни один инструмент слоя.
func ffmpegReady(cfg Config) error {
	miss := make([]string, 0, 2)
	if _, err := exec.LookPath(ffmpegExe(cfg)); err != nil {
		if st, serr := os.Stat(ffmpegExe(cfg)); serr != nil || st.IsDir() {
			miss = append(miss, "ffmpeg")
		}
	}
	if _, err := exec.LookPath(ffprobeExe(cfg)); err != nil {
		if st, serr := os.Stat(ffprobeExe(cfg)); serr != nil || st.IsDir() {
			miss = append(miss, "ffprobe")
		}
	}
	if len(miss) == 0 {
		return nil
	}
	return fmt.Errorf("не найден %s. Поставь: winget install Gyan.FFmpeg (или укажи путь в settings.ini → FFMPEG=), затем перезапусти Kibborg",
		strings.Join(miss, " и "))
}

// ===== метаданные =====

// mediaStream — одна дорожка контейнера в том виде, в каком она нужна слою.
type mediaStream struct {
	Index    int
	Kind     string // video | audio | subtitle
	Codec    string
	Lang     string
	Width    int
	Height   int
	FPS      float64
	Channels int
}

// mediaInfo — что ffprobe знает о файле.
type mediaInfo struct {
	Path     string
	Format   string
	Duration float64
	Bytes    int64
	Video    *mediaStream
	Audio    *mediaStream
	Subs     []mediaStream
}

func (m mediaInfo) hasVideo() bool { return m.Video != nil }
func (m mediaInfo) hasAudio() bool { return m.Audio != nil }

// Render — человеко- и модель-читаемая строка о файле.
func (m mediaInfo) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Файл: %s\n", m.Path)
	if m.Format != "" {
		fmt.Fprintf(&b, "Формат: %s", m.Format)
		if m.Bytes > 0 {
			fmt.Fprintf(&b, " · %s", humanBytes(m.Bytes))
		}
		b.WriteString("\n")
	}
	if m.Duration > 0 {
		fmt.Fprintf(&b, "Длительность: %s\n", fmtMediaDuration(m.Duration))
	}
	if m.Video != nil {
		fmt.Fprintf(&b, "Видео: %s %dx%d", m.Video.Codec, m.Video.Width, m.Video.Height)
		if m.Video.FPS > 0 {
			fmt.Fprintf(&b, " @ %.4g fps", m.Video.FPS)
		}
		b.WriteString("\n")
	} else {
		b.WriteString("Видео: нет (только звук)\n")
	}
	if m.Audio != nil {
		fmt.Fprintf(&b, "Звук: %s, каналов %d", m.Audio.Codec, m.Audio.Channels)
		if m.Audio.Lang != "" {
			fmt.Fprintf(&b, ", язык %s", m.Audio.Lang)
		}
		b.WriteString("\n")
	} else {
		b.WriteString("Звук: нет (речь распознать не из чего)\n")
	}
	if len(m.Subs) > 0 {
		names := make([]string, 0, len(m.Subs))
		for _, s := range m.Subs {
			n := s.Codec
			if s.Lang != "" {
				n = s.Lang + "/" + s.Codec
			}
			names = append(names, n)
		}
		fmt.Fprintf(&b, "Субтитры внутри файла: %s\n", strings.Join(names, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// probeMedia читает метаданные через ffprobe.
func probeMedia(ctx context.Context, cfg Config, path string) (mediaInfo, error) {
	info := mediaInfo{Path: path}
	if st, err := os.Stat(path); err != nil {
		return info, fmt.Errorf("файл не найден: %s", path)
	} else if st.IsDir() {
		return info, fmt.Errorf("это каталог, а не файл: %s", path)
	} else {
		info.Bytes = st.Size()
	}

	// Только stdout: предупреждение ffprobe в stderr сломало бы разбор JSON.
	out, err := runToolOut(ctx, ffprobeExe(cfg), 2*time.Minute,
		"-v", "error", "-print_format", "json", "-show_format", "-show_streams", path)
	if err != nil {
		return info, fmt.Errorf("ffprobe не прочитал файл: %v", err)
	}

	var probe struct {
		Streams []struct {
			Index      int    `json:"index"`
			CodecType  string `json:"codec_type"`
			CodecName  string `json:"codec_name"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			RFrameRate string `json:"r_frame_rate"`
			Channels   int    `json:"channels"`
			Tags       struct {
				Language string `json:"language"`
			} `json:"tags"`
		} `json:"streams"`
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal([]byte(out), &probe); err != nil {
		return info, fmt.Errorf("ffprobe вернул не-JSON: %s", capLogTail(out))
	}
	info.Format = probe.Format.FormatName
	if d, err := strconv.ParseFloat(strings.TrimSpace(probe.Format.Duration), 64); err == nil {
		info.Duration = d
	}
	for _, s := range probe.Streams {
		st := mediaStream{
			Index: s.Index, Kind: s.CodecType, Codec: s.CodecName,
			Lang: s.Tags.Language, Width: s.Width, Height: s.Height, Channels: s.Channels,
		}
		st.FPS = parseFrameRate(s.RFrameRate)
		switch s.CodecType {
		case "video":
			// Обложка трека (mjpeg 1 кадр) — не видеодорожка; кадры из неё брать бессмысленно.
			if info.Video == nil && st.FPS != 0 {
				v := st
				info.Video = &v
			}
		case "audio":
			if info.Audio == nil {
				a := st
				info.Audio = &a
			}
		case "subtitle":
			info.Subs = append(info.Subs, st)
		}
	}
	return info, nil
}

// parseFrameRate разбирает «30000/1001» и «25/1». Ноль означает «кадров нет» (обложка).
func parseFrameRate(s string) float64 {
	parts := strings.SplitN(strings.TrimSpace(s), "/", 2)
	if len(parts) != 2 {
		f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return f
	}
	num, err1 := strconv.ParseFloat(parts[0], 64)
	den, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil || den == 0 {
		return 0
	}
	return num / den
}

// ===== запуск ffmpeg/ffprobe =====

// runTool выполняет внешний бинарь под контекстом задачи: /stop и TaskTimeout убивают
// процесс, а не только цикл (§4.2).
func runTool(parent context.Context, bin string, timeout time.Duration, args ...string) (string, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if parent.Err() != nil {
			return "", parent.Err()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("%s: таймаут %s", filepath.Base(bin), timeout)
		}
		return "", fmt.Errorf("%s: %v (%s)", filepath.Base(bin), err, capLogTail(string(out)))
	}
	return string(out), nil
}

// runToolOut выполняет бинарь и возвращает ТОЛЬКО stdout, а stderr оставляет для сообщения об
// ошибке. Это принципиально там, где вывод — ДАННЫЕ: pdftotext на повреждённом файле пишет в
// stderr «Syntax Error: …», и склейка потоков молча вставляла эти строки в текст документа,
// откуда они уходили в выжимку как его содержание. Поймано на первом же битом PDF.
func runToolOut(parent context.Context, bin string, timeout time.Duration, args ...string) (string, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if parent.Err() != nil {
			return "", parent.Err()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("%s: таймаут %s", filepath.Base(bin), timeout)
		}
		return "", fmt.Errorf("%s: %v (%s)", filepath.Base(bin), err, capLogTail(stderr.String()))
	}
	return stdout.String(), nil
}

// runFFmpeg вызывает ffmpeg с общими флагами. -nostdin обязателен: без него ffmpeg,
// запущенный из службы, ждёт ввода с консоли и висит до таймаута.
func runFFmpeg(ctx context.Context, cfg Config, timeout time.Duration, args ...string) error {
	full := append([]string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}, args...)
	_, err := runTool(ctx, ffmpegExe(cfg), timeout, full...)
	return err
}

// ===== речь =====

// transcriptChunk — распознанный кусок с меткой времени начала.
type transcriptChunk struct {
	Start float64
	Text  string
}

// transcribeWavFile распознаёт готовый WAV, НЕ перечитывая его в память: у часового ролика
// это сотня мегабайт, и таскать её через []byte незачем — у TypeWhisper есть local-file.
// Порядок бэкендов тот же, что у голосовых (voice.go), чтобы поведение не разъезжалось.
func transcribeWavFile(cfg Config, wav string) (string, error) {
	var errs []string
	if typeWhisperWanted(cfg) {
		if text, err := transcribeTypeWhisperFile(cfg, wav); err == nil {
			return text, nil
		} else {
			errs = append(errs, "TypeWhisper local-file: "+err.Error())
		}
		if text, err := transcribeTypeWhisperMultipart(cfg, wav); err == nil {
			return text, nil
		} else {
			errs = append(errs, "TypeWhisper multipart: "+err.Error())
		}
	}
	if whisperCppEnabled(cfg) {
		if text, err := transcribeWhisperCpp(cfg, wav); err == nil {
			return text, nil
		} else {
			errs = append(errs, "whisper.cpp: "+err.Error())
		}
	}
	if len(errs) == 0 {
		return "", fmt.Errorf("распознавание речи не настроено (ни TypeWhisper, ни whisper.cpp)")
	}
	return "", fmt.Errorf("%s", strings.Join(errs, " | "))
}

// transcribeMediaFile вытаскивает речь из любого файла, который читает ffmpeg, любой длины.
//
// Два прохода вместо N: сначала ОДНО декодирование в 16 кГц моно WAV, потом нарезка
// сегментным мультиплексором. Так метки времени точны по построению (PCM с постоянным
// битрейтом), а исходник декодируется ровно один раз.
//
// partial=true означает «время задачи кончилось, но вот что успели» — половина расшифровки
// полезнее, чем пустой ответ с извинением.
func transcribeMediaFile(ctx context.Context, cfg Config, src string, from, to float64,
	progress func(done, total int)) (chunks []transcriptChunk, partial bool, err error) {

	if err := os.MkdirAll(mediaWorkDir, 0o755); err != nil {
		return nil, false, err
	}
	stamp := time.Now().Format("20060102-150405.000")
	work := filepath.Join(mediaWorkDir, "stt-"+stamp)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, false, err
	}
	defer os.RemoveAll(work) // куски — расходный материал, ценен только текст

	wav := filepath.Join(work, "full.wav")
	args := []string{}
	if from > 0 {
		args = append(args, "-ss", fmtSeconds(from))
	}
	args = append(args, "-i", src)
	if to > from && to > 0 {
		args = append(args, "-t", fmtSeconds(to-from))
	}
	// -vn: видеодорожка распознаванию не нужна и стоит времени на декодирование.
	args = append(args, "-vn", "-ar", "16000", "-ac", "1", "-f", "wav", wav)
	if err := runFFmpeg(ctx, cfg, 60*time.Minute, args...); err != nil {
		return nil, false, fmt.Errorf("не смог извлечь звук: %w", err)
	}
	if st, serr := os.Stat(wav); serr != nil || st.Size() < 1024 {
		return nil, false, fmt.Errorf("в файле нет звуковой дорожки — распознавать нечего")
	}

	pattern := filepath.Join(work, "part-%04d.wav")
	if err := runFFmpeg(ctx, cfg, 30*time.Minute,
		"-i", wav, "-f", "segment",
		"-segment_time", strconv.Itoa(sttChunkSeconds),
		"-segment_format", "wav", "-c", "copy", pattern); err != nil {
		return nil, false, fmt.Errorf("не смог нарезать звук: %w", err)
	}
	_ = os.Remove(wav) // дальше нужны только куски

	parts, err := filepath.Glob(filepath.Join(work, "part-*.wav"))
	if err != nil || len(parts) == 0 {
		return nil, false, fmt.Errorf("после нарезки не осталось кусков звука")
	}
	sort.Strings(parts)
	if len(parts) > maxSTTChunks {
		parts = parts[:maxSTTChunks]
		partial = true
	}

	for i, p := range parts {
		if ctx.Err() != nil {
			return chunks, true, nil // время вышло — отдаём, что успели
		}
		text, terr := transcribeWavFile(cfg, p)
		if terr != nil {
			// Один глухой кусок не должен ронять часовую расшифровку.
			log.Printf("[VIDEO] кусок %d/%d не распознан: %v", i+1, len(parts), terr)
			if i == 0 && len(parts) == 1 {
				return nil, false, terr
			}
			continue
		}
		if text = strings.TrimSpace(text); text != "" {
			chunks = append(chunks, transcriptChunk{
				Start: from + float64(i*sttChunkSeconds),
				Text:  text,
			})
		}
		if progress != nil {
			progress(i+1, len(parts))
		}
	}
	if len(chunks) == 0 {
		return nil, partial, fmt.Errorf("речь не распознана: в звуке её, похоже, нет")
	}
	return chunks, partial, nil
}

// renderTranscript склеивает куски с метками времени — по ним человек находит место в ролике,
// а модель может сослаться на «на 14:20 он говорит…».
func renderTranscript(chunks []transcriptChunk) string {
	var b strings.Builder
	for _, c := range chunks {
		fmt.Fprintf(&b, "[%s] %s\n\n", fmtMediaDuration(c.Start), c.Text)
	}
	return strings.TrimSpace(b.String())
}

// ===== субтитры =====

// extractEmbeddedSubs достаёт дорожку субтитров из контейнера. Готовые субтитры всегда
// точнее и в сотни раз быстрее распознавания — если они есть, речь распознавать незачем.
func extractEmbeddedSubs(ctx context.Context, cfg Config, src string, stream mediaStream) (string, error) {
	if err := os.MkdirAll(mediaWorkDir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(mediaWorkDir, fmt.Sprintf("subs-%s.srt", time.Now().Format("20060102-150405.000")))
	defer os.Remove(dst)
	if err := runFFmpeg(ctx, cfg, 10*time.Minute,
		"-i", src, "-map", fmt.Sprintf("0:%d", stream.Index), "-f", "srt", dst); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(dst)
	if err != nil {
		return "", err
	}
	text := srtToText(string(raw))
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("дорожка субтитров пуста")
	}
	return text, nil
}

// srtToText выкидывает номера и тайминги, оставляя связный текст. Метка времени сохраняется
// раз в ~2 минуты — реплика в секунду превратила бы расшифровку в столбец цифр.
func srtToText(raw string) string {
	var b strings.Builder
	var lastStamp float64 = -1e9
	for _, block := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) < 2 {
			continue
		}
		start := -1.0
		var body []string
		for _, ln := range lines {
			ln = strings.TrimSpace(ln)
			if ln == "" {
				continue
			}
			if strings.Contains(ln, "-->") {
				start = parseSRTTime(strings.TrimSpace(strings.SplitN(ln, "-->", 2)[0]))
				continue
			}
			if start < 0 && isAllDigits(ln) {
				continue // порядковый номер блока
			}
			body = append(body, ln)
		}
		if len(body) == 0 {
			continue
		}
		if start >= 0 && start-lastStamp >= 120 {
			fmt.Fprintf(&b, "\n[%s] ", fmtMediaDuration(start))
			lastStamp = start
		}
		b.WriteString(strings.Join(body, " ") + " ")
	}
	return strings.TrimSpace(strings.ReplaceAll(b.String(), "  ", " "))
}

func parseSRTTime(s string) float64 {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", ".")
	parts := strings.Split(s, ":")
	var mult, total float64 = 1, 0
	for i := len(parts) - 1; i >= 0; i-- {
		v, err := strconv.ParseFloat(parts[i], 64)
		if err != nil {
			return -1
		}
		total += v * mult
		mult *= 60
	}
	return total
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ===== кадры =====

// frameShot — один кадр, вынутый из ролика.
type frameShot struct {
	At    float64
	Path  string
	Bytes []byte
}

// frameTimes расставляет N точек по ролику, отступая от самых краёв: на нулевой секунде
// обычно чёрный кадр или заставка, и описывать там нечего.
func frameTimes(duration float64, n int) []float64 {
	if n <= 0 {
		return nil
	}
	if duration <= 0 {
		return []float64{0}
	}
	if n == 1 {
		return []float64{duration / 2}
	}
	out := make([]float64, 0, n)
	span := duration * 0.9
	start := duration * 0.05
	for i := 0; i < n; i++ {
		out = append(out, start+span*float64(i)/float64(n-1))
	}
	return out
}

// extractFrames вынимает кадры в указанные моменты. Масштабирование делает ffmpeg: гонять
// 4K-кадр в зрение бессмысленно, а ужимать его своим кодом — лишний слой.
func extractFrames(ctx context.Context, cfg Config, src string, at []float64) ([]frameShot, error) {
	if len(at) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(mediaWorkDir, 0o755); err != nil {
		return nil, err
	}
	stamp := time.Now().Format("20060102-150405.000")
	out := make([]frameShot, 0, len(at))
	for i, t := range at {
		if ctx.Err() != nil {
			break
		}
		dst := filepath.Join(mediaWorkDir, fmt.Sprintf("frame-%s-%02d.jpg", stamp, i+1))
		// -ss ДО -i — быстрый поиск по ключевым кадрам: иначе часовой ролик декодируется
		// с начала для каждого кадра.
		err := runFFmpeg(ctx, cfg, 5*time.Minute,
			"-ss", fmtSeconds(t), "-i", src, "-frames:v", "1",
			"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", frameMaxSide),
			"-q:v", "3", dst)
		if err != nil {
			log.Printf("[VIDEO] кадр на %s не вынулся: %v", fmtMediaDuration(t), err)
			continue
		}
		data, rerr := os.ReadFile(dst)
		if rerr != nil || len(data) == 0 {
			continue
		}
		abs, aerr := filepath.Abs(dst)
		if aerr != nil {
			abs = dst
		}
		out = append(out, frameShot{At: t, Path: abs, Bytes: data})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ни одного кадра вынуть не удалось")
	}
	return out, nil
}

// framePrompt просит зрение не только описать сцену, но и ВЫПИСАТЬ видимый текст: на разборе
// туториала ценность кадра — это код на экране и заголовок слайда, а не «мужчина говорит».
const framePrompt = `Опиши этот кадр из видео по делу, без выдумок.
1) Что происходит: сцена, интерфейс, жест, график, слайд.
2) ЕСЛИ на кадре есть ТЕКСТ (код, слайд, заголовок, тикер, цена, URL, имя репозитория, команда) — выпиши его ДОСЛОВНО отдельной строкой «ТЕКСТ НА ЭКРАНЕ: …». Цифры и адреса не округляй и не исправляй.
3) Если кадр пустой, чёрный или размытый — одной строкой так и скажи.`

// frameVisionSystemPrompt держит зрение в режиме «описал и замолчал».
//
// Без него модель отвечает своим парадным стилем: заголовки «🎯 Кратко», линейки из символов,
// списки — и в лимит описания попадает оформление вместо содержания. Плюс роль отключает
// подмешивание долговременной памяти: кадр надо описывать по тому, что на нём есть, а не по
// тому, что мы обсуждали вчера.
const frameVisionSystemPrompt = `Ты коротко и точно описываешь ОДИН кадр из видео. Пиши сплошным текстом, без заголовков, списков, эмодзи и разделительных линий. Не выдумывай того, чего на кадре не видно.`

// describeFrames прогоняет кадры через зрение. Каждый кадр — ОТДЕЛЬНЫЙ запрос без
// инструментов: §7 запрещает мешать image_url со схемами в одном обращении к llama-server.
func describeFrames(ctx context.Context, cfg Config, chatID int64, frames []frameShot) []string {
	out := make([]string, 0, len(frames))
	for _, f := range frames {
		if ctx.Err() != nil {
			break
		}
		desc, err := describeImageBytes(cfg, chatID, framePrompt, frameVisionSystemPrompt, "image/jpeg", f.Bytes)
		if err != nil {
			out = append(out, fmt.Sprintf("[%s] кадр не разобрал: %s", fmtMediaDuration(f.At), err.Error()))
			continue
		}
		out = append(out, fmt.Sprintf("[%s] %s", fmtMediaDuration(f.At), capFrameNote(tidyFrameNote(desc), frameNoteChars)))
	}
	return out
}

// frameNoteChars — сколько места отводится описанию ОДНОГО кадра в ответе инструмента.
const frameNoteChars = 900

// screenTextMarker — заголовок блока с дословным текстом экрана (его просит framePrompt).
const screenTextMarker = "ТЕКСТ НА ЭКРАНЕ"

// capFrameNote обрезает описание кадра так, чтобы дословный текст с экрана уцелел ЦЕЛИКОМ.
//
// Обычная обрезка режет по длине с конца, а модель ставит блок «ТЕКСТ НА ЭКРАНЕ» то до
// рассуждения, то после. На проверенном ролике повезло — текст оказался первым; поставь его
// модель второй, и обрезка съела бы ровно то, ради чего кадры и смотрят: адрес репозитория,
// команду, цифру со слайда. Проза восстановима из соседних кадров, дословный текст — нет,
// поэтому режется именно она.
func capFrameNote(s string, limit int) string {
	if len([]rune(s)) <= limit {
		return s
	}
	idx := indexScreenText(s)
	if idx < 0 {
		return capAgentText(s, limit)
	}
	screen := strings.TrimSpace(s[idx:])
	prose := strings.TrimSpace(s[:idx])
	if len([]rune(screen)) >= limit {
		return capAgentText(screen, limit) // сам текст экрана длиннее лимита — режем его
	}
	room := limit - len([]rune(screen)) - 1
	if room <= 0 {
		return screen
	}
	if prose == "" {
		return screen
	}
	return capAgentText(prose, room) + "\n" + screen
}

// indexScreenText находит начало блока с текстом экрана, не спотыкаясь о markdown-жирный
// заголовок («**ТЕКСТ НА ЭКРАНЕ:**») и регистр.
func indexScreenText(s string) int {
	up := strings.ToUpper(s)
	i := strings.Index(up, screenTextMarker)
	if i < 0 {
		return -1
	}
	// Прихватываем открывающие звёздочки жирного начертания, если они есть.
	for i > 0 && (s[i-1] == '*' || s[i-1] == ' ') {
		i--
	}
	return i
}

// tidyFrameNote выкидывает из описания кадра оформление: разделительные линейки и висячие
// пробелы markdown-переносов. Просьбы в промпте оказалось мало — модель всё равно рисует
// «━━━━━», а лимит в 900 символов один на всех: каждая такая линейка вытесняет содержание.
// Чистится только ОФОРМЛЕНИЕ: ни одна строка с буквами или цифрами не удаляется.
func tidyFrameNote(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, ln := range lines {
		ln = strings.TrimRight(ln, " \t")
		if isDecorationLine(ln) {
			continue
		}
		if strings.TrimSpace(ln) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// isDecorationLine — строка из одних символов-разделителей и без единой буквы или цифры.
func isDecorationLine(ln string) bool {
	t := strings.TrimSpace(ln)
	if len([]rune(t)) < 3 {
		return false
	}
	for _, r := range t {
		switch r {
		case '━', '─', '—', '=', '_', '*', '-', '·', '•', '#', '~', ' ':
		default:
			return false
		}
	}
	return true
}

// autoFrameCount решает, сколько кадров смотреть, когда пользователь не сказал.
//
// Qwen3.8 видит кадр хорошо — дешевле взять лишний слайд, чем пропустить адрес
// репозитория на экране. Речь по-прежнему главный источник для длинных докладов,
// но нулевые кадры на часовом ролике оставляли слепыми презентации.
//
//   - речи нет (немой скринкаст) — кадры единственный источник, восемь;
//   - короткий ролик с речью — пять кадров: сцена + слайды задёшево;
//   - длинный ролик с речью — четыре кадра равномерно, не ноль.
func autoFrameCount(info mediaInfo, haveSpeech bool) int {
	if !info.hasVideo() {
		return 0
	}
	if !haveSpeech {
		return 8
	}
	if info.Duration > 15*60 {
		return 4
	}
	return 5
}

// ===== пересказ длинной расшифровки =====

// summariseTranscript сворачивает расшифровку любой длины. Механизм общий (summarize.go),
// здесь только роль: что в расшифровке ролика считать ценным.
func summariseTranscript(ctx context.Context, cfg Config, question, text string) (string, error) {
	sys := "Ты сжимаешь расшифровку видео для самого себя — это рабочая выжимка, а не отчёт человеку.\n" +
		"Сохрани: о чём идёт речь, названия проектов, инструментов, репозиториев и ссылки, имена, числа, " +
		"пошаговые инструкции и последовательность действий, выводы и оговорки автора.\n" +
		"Выброси: приветствия, повторы, оговорки-паразиты, рекламу.\n" +
		"Ничего не выдумывай: если в тексте чего-то нет, значит его нет. Пиши по-русски, плотными пунктами."
	if q := strings.TrimSpace(question); q != "" {
		sys += "\nОсобое внимание тому, что относится к вопросу: «" + capAgentText(q, 300) + "»."
	}
	return mapReduceSummary(ctx, cfg, sys, text)
}

// ===== источник: файл или ссылка =====

// mediaSource — откуда взялся разбираемый файл.
type mediaSource struct {
	Path      string
	FromURL   string
	Title     string
	Subtitles string // готовые субтитры, если их отдал хостинг
	AudioOnly bool
}

// resolveMediaSource приводит «путь или ссылка» к локальному файлу.
//
// Для ссылки сначала пробуются субтитры хостинга: это секунды вместо минут и ноль трафика.
// Скачивается только звук, если кадры не нужны, — у часового доклада это разница между
// 30 мегабайтами и гигабайтом.
func resolveMediaSource(ctx context.Context, cfg Config, source string, needFrames bool,
	progress func(string)) (mediaSource, error) {

	source = strings.TrimSpace(source)
	if source == "" {
		return mediaSource{}, fmt.Errorf("нужен путь к файлу или ссылка")
	}
	if !isRemoteMediaSource(source) {
		abs, err := filepath.Abs(source)
		if err != nil {
			return mediaSource{}, fmt.Errorf("путь не разбирается: %s", source)
		}
		if st, serr := os.Stat(abs); serr != nil {
			return mediaSource{}, fmt.Errorf("файл не найден: %s", abs)
		} else if st.IsDir() {
			return mediaSource{}, fmt.Errorf("это каталог, а не файл: %s", abs)
		}
		return mediaSource{Path: abs}, nil
	}

	src := mediaSource{FromURL: source}
	if !needFrames {
		if progress != nil {
			progress("проверяю, есть ли у ролика готовые субтитры")
		}
		if subs, err := browser.YouTubeTranscript(ctx, source, "ru,en"); err == nil {
			if text := strings.TrimSpace(subs); len(text) > 200 {
				src.Subtitles = text
				return src, nil
			}
		}
	}
	if progress != nil {
		what := "звук"
		if needFrames {
			what = "видео"
		}
		progress("готовых субтитров нет — скачиваю " + what)
	}
	dl, err := browser.FetchMedia(ctx, source, cfg.FfmpegPath, !needFrames)
	if err != nil {
		return mediaSource{}, fmt.Errorf("не смог скачать: %w", err)
	}
	abs, aerr := filepath.Abs(dl.Path)
	if aerr != nil {
		abs = dl.Path
	}
	src.Path = abs
	src.Title = dl.Title
	src.AudioOnly = !needFrames
	return src, nil
}

// isRemoteMediaSource отличает ссылку от пути. Windows-путь «C:\video.mp4» содержит двоеточие,
// поэтому проверяется именно схема, а не наличие «:».
func isRemoteMediaSource(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

// ===== разбор целиком =====

// mediaAnalyzeOpts — что и как разбирать.
type mediaAnalyzeOpts struct {
	Source   string
	Question string
	Frames   int  // -1 = решить самому
	Speech   bool // распознавать речь
	From, To float64
	ChatID   int64
	Progress func(string)
}

// mediaDigest — результат разбора.
type mediaDigest struct {
	Info           mediaInfo
	Source         mediaSource
	Transcript     string
	TranscriptPath string
	TranscriptFrom string // «субтитры» | «распознано с речи»
	Summary        string
	Frames         []string
	Notes          []string
	Partial        bool
}

// Render — то, что видит и модель (как результат инструмента), и человек (как сообщение).
func (d mediaDigest) Render() string {
	var b strings.Builder
	b.WriteString("🎬 **Разбор видео**\n")
	if d.Source.Title != "" {
		b.WriteString("Название: " + d.Source.Title + "\n")
	}
	if d.Source.FromURL != "" {
		b.WriteString("Ссылка: " + d.Source.FromURL + "\n")
	}
	if d.Info.Duration > 0 {
		b.WriteString("Длительность: " + fmtMediaDuration(d.Info.Duration))
		if d.Info.Video != nil {
			fmt.Fprintf(&b, " · %dx%d", d.Info.Video.Width, d.Info.Video.Height)
		}
		b.WriteString("\n")
	}
	if d.Summary != "" {
		b.WriteString("\n**О чём говорят** (" + d.TranscriptFrom + ", пересказ):\n" + d.Summary + "\n")
	} else if d.Transcript != "" {
		b.WriteString("\n**Речь** (" + d.TranscriptFrom + "):\n" + d.Transcript + "\n")
	}
	if len(d.Frames) > 0 {
		b.WriteString("\n**Что видно на кадрах:**\n")
		for _, f := range d.Frames {
			b.WriteString("- " + f + "\n")
		}
	}
	if d.TranscriptPath != "" {
		fmt.Fprintf(&b, "\n📄 Полная расшифровка: %s (%d символов) — если нужны точные формулировки, читай файл.\n",
			d.TranscriptPath, len([]rune(d.Transcript)))
	}
	if d.Source.Path != "" && d.Source.FromURL != "" {
		b.WriteString("💾 Скачанный файл: " + d.Source.Path + "\n")
	}
	for _, n := range d.Notes {
		b.WriteString("⚠️ " + n + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// analyzeMedia — весь слой одним вызовом: источник → метаданные → речь → кадры → сводка.
func analyzeMedia(ctx context.Context, cfg Config, opts mediaAnalyzeOpts) (mediaDigest, error) {
	var d mediaDigest
	if err := ffmpegReady(cfg); err != nil {
		return d, err
	}
	note := func(s string) {
		if opts.Progress != nil {
			opts.Progress(s)
		}
	}

	// Кадры нужны? От этого зависит, качать ролик целиком или только звук.
	wantFrames := opts.Frames != 0
	src, err := resolveMediaSource(ctx, cfg, opts.Source, wantFrames, opts.Progress)
	if err != nil {
		return d, err
	}
	d.Source = src

	// Субтитры хостинга — путь без единого декодирования.
	if src.Subtitles != "" {
		d.Transcript = src.Subtitles
		d.TranscriptFrom = "субтитры с хостинга"
	}

	if src.Path != "" {
		info, perr := probeMedia(ctx, cfg, src.Path)
		if perr != nil {
			return d, perr
		}
		d.Info = info

		if d.Transcript == "" && opts.Speech {
			// Готовые субтитры внутри контейнера точнее распознавания и почти бесплатны.
			for _, s := range info.Subs {
				note("беру субтитры из файла")
				if text, serr := extractEmbeddedSubs(ctx, cfg, src.Path, s); serr == nil {
					d.Transcript = text
					d.TranscriptFrom = "субтитры из файла"
					break
				}
			}
		}
		if d.Transcript == "" && opts.Speech {
			if !info.hasAudio() {
				d.Notes = append(d.Notes, "в файле нет звуковой дорожки — распознавать нечего")
			} else {
				note(fmt.Sprintf("распознаю речь (%s)", fmtMediaDuration(info.Duration)))
				chunks, partial, terr := transcribeMediaFile(ctx, cfg, src.Path, opts.From, opts.To,
					func(done, total int) {
						if total > 1 && done < total && done%4 == 0 {
							note(fmt.Sprintf("распознано %d из %d кусков", done, total))
						}
					})
				if terr != nil {
					d.Notes = append(d.Notes, "речь распознать не вышло: "+terr.Error())
				} else {
					d.Transcript = renderTranscript(chunks)
					d.TranscriptFrom = "распознано с речи"
					d.Partial = partial
					if partial {
						d.Notes = append(d.Notes, "расшифровка НЕПОЛНАЯ: время задачи кончилось раньше ролика")
					}
				}
			}
		}

		// Кадры: явное число уважаем, «сам реши» — считаем по autoFrameCount.
		n := opts.Frames
		if n < 0 {
			n = autoFrameCount(info, d.Transcript != "")
		}
		if n > maxFrames {
			n = maxFrames
		}
		if n > 0 && info.hasVideo() {
			note(fmt.Sprintf("смотрю %d кадр(а/ов) глазами", n))
			if verr := requireVisionOK(cfg); verr != nil {
				d.Notes = append(d.Notes, "кадры не посмотрел: "+verr.Error())
			} else if frames, ferr := extractFrames(ctx, cfg, src.Path, frameTimes(info.Duration, n)); ferr != nil {
				d.Notes = append(d.Notes, "кадры не вынулись: "+ferr.Error())
			} else {
				d.Frames = describeFrames(ctx, cfg, opts.ChatID, frames)
				// Кадры превратились в текст и больше не нужны: держать их на диске значит
				// копить по 150 КБ на каждый разобранный ролик впустую. Инструмент
				// video_frames — другое дело, там сами картинки и есть результат.
				for _, f := range frames {
					_ = os.Remove(f.Path)
				}
			}
		} else if n > 0 && !info.hasVideo() {
			d.Notes = append(d.Notes, "кадры не смотрел: в файле только звук")
		}
	}

	if d.Transcript == "" && len(d.Frames) == 0 {
		if len(d.Notes) > 0 {
			return d, fmt.Errorf("%s", strings.Join(d.Notes, "; "))
		}
		return d, fmt.Errorf("из этого файла не удалось достать ни речи, ни кадров")
	}

	// Длинная расшифровка целиком в контекст не лезет: файл рядом + пересказ в ответе.
	if d.Transcript != "" {
		if path, werr := saveTranscript(d.Source, d.Transcript); werr == nil {
			d.TranscriptPath = path
		}
		if len([]rune(d.Transcript)) > transcriptInlineChars {
			note("расшифровка длинная — сворачиваю в выжимку")
			sum, serr := summariseTranscript(ctx, cfg, opts.Question, d.Transcript)
			if serr != nil {
				// Пересказ не вышел — отдаём начало дословно, честно назвав это обрезкой.
				d.Summary = ""
				d.Notes = append(d.Notes, "пересказ не получился ("+serr.Error()+") — ниже начало расшифровки, полный текст в файле")
				d.Transcript = capAgentText(d.Transcript, transcriptInlineChars)
			} else {
				d.Summary = sum
				d.Transcript = strings.TrimSpace(d.Transcript)
			}
		}
	}
	return d, nil
}

// saveTranscript кладёт полный текст рядом с остальными артефактами: в контекст модели он не
// влезает, но он нужен целиком — и человеку, и самой модели через read_file.
func saveTranscript(src mediaSource, text string) (string, error) {
	if err := os.MkdirAll(mediaWorkDir, 0o755); err != nil {
		return "", err
	}
	base := "transcript"
	if src.Title != "" {
		base = safeFileStem(src.Title)
	} else if src.Path != "" {
		// Исходник, принятый из чата, уже назван с меткой времени. Без снятия этой метки
		// расшифровка получала вторую: «20260813-150951-20260813-150937-clip.txt».
		base = safeFileStem(stripStampPrefix(strings.TrimSuffix(filepath.Base(src.Path), filepath.Ext(src.Path))))
	}
	name := fmt.Sprintf("%s-%s.txt", time.Now().Format("20060102-150405"), base)
	full := filepath.Join(mediaWorkDir, name)
	header := ""
	if src.FromURL != "" {
		header = "Источник: " + src.FromURL + "\n\n"
	} else if src.Path != "" {
		header = "Источник: " + src.Path + "\n\n"
	}
	if err := os.WriteFile(full, []byte(header+text), 0o644); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(full)
	if err != nil {
		return full, nil
	}
	return abs, nil
}

// stripStampPrefix снимает наш собственный префикс «ГГГГММДД-ЧЧММСС-» с имени файла.
func stripStampPrefix(stem string) string {
	const n = len("20060102-150405-")
	if len(stem) <= n || stem[8] != '-' || stem[15] != '-' {
		return stem
	}
	for i, r := range stem[:15] {
		if i == 8 {
			continue
		}
		if r < '0' || r > '9' {
			return stem
		}
	}
	return stem[n:]
}

// safeFileStem делает из названия ролика имя файла, которое переживёт Windows.
func safeFileStem(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' ||
			r == '<' || r == '>' || r == '|' || r < 32:
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(strings.TrimSpace(b.String()), ".-")
	if out == "" {
		out = "media"
	}
	r := []rune(out)
	if len(r) > 60 {
		out = string(r[:60])
	}
	return out
}

// ===== конвертация =====

// convertOpts — структурированная конвертация. Экзотику никто не отнимает: raw-ffmpeg
// остаётся доступен через пак `console`, а этот инструмент закрывает частые случаи так,
// чтобы модели не приходилось сочинять командную строку.
type convertOpts struct {
	Input     string
	Output    string
	Format    string // mp4 | mp3 | wav | gif | webm | mkv | m4a | png …
	From, To  float64
	Scale     string // 720 | 1080 | 1280x720
	FPS       float64
	AudioOnly bool
	Mute      bool
}

// convertMedia строит команду ffmpeg под задачу и возвращает путь результата.
func convertMedia(ctx context.Context, cfg Config, o convertOpts) (string, mediaInfo, error) {
	var info mediaInfo
	if err := ffmpegReady(cfg); err != nil {
		return "", info, err
	}
	in := strings.TrimSpace(o.Input)
	if in == "" {
		return "", info, fmt.Errorf("нужен input — путь к исходному файлу")
	}
	abs, err := filepath.Abs(in)
	if err != nil {
		return "", info, fmt.Errorf("путь не разбирается: %s", in)
	}
	if st, serr := os.Stat(abs); serr != nil || st.IsDir() {
		return "", info, fmt.Errorf("файл не найден: %s", abs)
	}

	format := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(o.Format), "."))
	out := strings.TrimSpace(o.Output)
	if out == "" {
		if format == "" {
			format = "mp4"
			if o.AudioOnly {
				format = "mp3"
			}
		}
		if err := os.MkdirAll(mediaWorkDir, 0o755); err != nil {
			return "", info, err
		}
		stem := safeFileStem(strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs)))
		out = filepath.Join(mediaWorkDir, fmt.Sprintf("%s-%s.%s",
			time.Now().Format("20060102-150405"), stem, format))
	}
	outAbs, err := filepath.Abs(out)
	if err != nil {
		return "", info, fmt.Errorf("выходной путь не разбирается: %s", out)
	}
	if format == "" {
		format = strings.ToLower(strings.TrimPrefix(filepath.Ext(outAbs), "."))
	}
	if outAbs == abs {
		return "", info, fmt.Errorf("вход и выход — один файл; задай другой output")
	}
	if err := os.MkdirAll(filepath.Dir(outAbs), 0o755); err != nil {
		return "", info, err
	}

	args := []string{}
	if o.From > 0 {
		args = append(args, "-ss", fmtSeconds(o.From))
	}
	args = append(args, "-i", abs)
	if o.To > o.From && o.To > 0 {
		args = append(args, "-t", fmtSeconds(o.To-o.From))
	}

	switch {
	case o.AudioOnly || format == "mp3" || format == "m4a" || format == "wav" || format == "ogg" || format == "flac":
		args = append(args, "-vn")
	case format == "gif":
		// Палитра из самого ролика — без неё GIF выходит грязным месивом из 256 случайных цветов.
		fps := o.FPS
		if fps <= 0 {
			fps = 12
		}
		w := scaleWidth(o.Scale)
		if w <= 0 {
			w = 640
		}
		args = append(args, "-vf", fmt.Sprintf(
			"fps=%g,scale=%d:-2:flags=lanczos,split[a][b];[a]palettegen[p];[b][p]paletteuse", fps, w))
	default:
		if vf := scaleFilter(o.Scale); vf != "" {
			args = append(args, "-vf", vf)
		}
		if o.FPS > 0 {
			args = append(args, "-r", fmt.Sprintf("%g", o.FPS))
		}
		if o.Mute {
			args = append(args, "-an")
		}
	}
	args = append(args, outAbs)

	if err := runFFmpeg(ctx, cfg, 60*time.Minute, args...); err != nil {
		return "", info, err
	}
	if st, serr := os.Stat(outAbs); serr != nil || st.Size() == 0 {
		return "", info, fmt.Errorf("ffmpeg отработал, но файл пустой: %s", outAbs)
	}
	info, _ = probeMedia(ctx, cfg, outAbs)
	return outAbs, info, nil
}

// scaleFilter превращает «720», «1080p», «1280x720» в фильтр ffmpeg. -2 вместо -1 держит
// размер чётным: h264 не кодирует нечётную высоту и падает с невнятной ошибкой.
func scaleFilter(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	s = strings.TrimSuffix(s, "p")
	if i := strings.IndexAny(s, "x:"); i > 0 {
		w, err1 := strconv.Atoi(strings.TrimSpace(s[:i]))
		h, err2 := strconv.Atoi(strings.TrimSpace(s[i+1:]))
		if err1 == nil && err2 == nil && w > 0 && h > 0 {
			return fmt.Sprintf("scale=%d:%d", w, h)
		}
		return ""
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return ""
	}
	// Одно число — это высота («720p»), ширина считается по пропорции.
	return fmt.Sprintf("scale=-2:%d", n)
}

// scaleWidth достаёт ширину для GIF (там нужен именно горизонтальный размер).
func scaleWidth(s string) int {
	s = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "p")))
	if s == "" {
		return 0
	}
	if i := strings.IndexAny(s, "x:"); i > 0 {
		if w, err := strconv.Atoi(strings.TrimSpace(s[:i])); err == nil {
			return w
		}
		return 0
	}
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n * 16 / 9 // «480» для гифки читается как высота — ширину берём по 16:9
	}
	return 0
}

// ===== мелочи =====

// parseTimeSpec понимает «83», «1:23», «1:02:03» и «1м20с» — модель пишет время как придётся.
func parseTimeSpec(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, t >= 0
	case int:
		return float64(t), t >= 0
	case string:
		s := strings.TrimSpace(strings.ToLower(t))
		if s == "" {
			return 0, false
		}
		s = strings.NewReplacer("ч", ":", "м", ":", "мин", ":", "с", "", "h", ":", "m", ":", "s", "").Replace(s)
		s = strings.TrimSuffix(s, ":")
		if strings.Contains(s, ":") {
			total := parseSRTTime(s)
			return total, total >= 0
		}
		f, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
		return f, err == nil && f >= 0
	}
	return 0, false
}

// argTimeSec читает временной аргумент инструмента.
func argTimeSec(args map[string]any, key string) float64 {
	if v, ok := args[key]; ok {
		if sec, ok := parseTimeSpec(v); ok {
			return sec
		}
	}
	return 0
}

// fmtSeconds — время для командной строки ffmpeg.
func fmtSeconds(sec float64) string {
	return strconv.FormatFloat(sec, 'f', 3, 64)
}

// fmtMediaDuration — время для человека: 4:12 или 1:04:12.
func fmtMediaDuration(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	total := int(sec + 0.5)
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// humanBytes — размер файла словами, а не в байтах.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f ГБ", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f МБ", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f КБ", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d Б", n)
	}
}
