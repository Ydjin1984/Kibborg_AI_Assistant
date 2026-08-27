package main

// Слой видео (§21). Две половины: чистые функции проверяются всегда, работа с настоящим
// ffmpeg — если он есть в системе. Вторая половина важнее: командную строку ffmpeg собирает
// наш код, и ошибки живут именно там, а не в разборе строк.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseTimeSpecForms(t *testing.T) {
	cases := []struct {
		in   any
		want float64
		ok   bool
	}{
		{"83", 83, true},
		{"1:23", 83, true},
		{"1:02:03", 3723, true},
		{"0:05", 5, true},
		{90.5, 90.5, true},
		{12, 12, true},
		{"", 0, false},
		{"чепуха", 0, false},
	}
	for _, c := range cases {
		got, ok := parseTimeSpec(c.in)
		if ok != c.ok {
			t.Errorf("parseTimeSpec(%v): ok=%v, ждали %v", c.in, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("parseTimeSpec(%v) = %v, ждали %v", c.in, got, c.want)
		}
	}
}

func TestFmtMediaDuration(t *testing.T) {
	cases := map[float64]string{0: "0:00", 83: "1:23", 599: "9:59", 3723: "1:02:03"}
	for in, want := range cases {
		if got := fmtMediaDuration(in); got != want {
			t.Errorf("fmtMediaDuration(%v) = %q, ждали %q", in, got, want)
		}
	}
}

// Субтитры приходят столбцом «номер / тайминг / текст». В расшифровку должен попасть ТЕКСТ,
// а метка времени — редко: реплика в секунду превратила бы её в столбец цифр.
func TestSrtToTextKeepsWordsDropsTimings(t *testing.T) {
	raw := "1\n00:00:01,000 --> 00:00:03,000\nПривет, это тест\n\n" +
		"2\n00:00:03,500 --> 00:00:05,000\nвторая реплика\n\n" +
		"3\n00:05:00,000 --> 00:05:02,000\nдалеко потом\n"
	got := srtToText(raw)
	for _, want := range []string{"Привет, это тест", "вторая реплика", "далеко потом"} {
		if !strings.Contains(got, want) {
			t.Errorf("текст %q потерялся: %q", want, got)
		}
	}
	if strings.Contains(got, "-->") || strings.Contains(got, "00:00:01") {
		t.Errorf("тайминги остались в тексте: %q", got)
	}
	// Третья реплика отстоит на 5 минут — метка времени там обязана появиться.
	if !strings.Contains(got, "[5:00]") {
		t.Errorf("нет метки времени для далёкой реплики: %q", got)
	}
	// А вторая идёт через 2.5 секунды после первой — метки быть не должно.
	if strings.Contains(got, "[0:03]") {
		t.Errorf("метка на соседней реплике — расшифровка превратится в столбец цифр: %q", got)
	}
}

// Резка длинного текста: куски в пределах лимита, ничего не потеряно, слова не разорваны.
func TestChunkTextKeepsEverything(t *testing.T) {
	words := strings.Repeat("слово ", 4000) // ~24 000 символов
	parts := chunkText(words, 1000)
	if len(parts) < 2 {
		t.Fatalf("длинный текст не разрезан: %d кусков", len(parts))
	}
	joined := 0
	for i, p := range parts {
		if n := len([]rune(p)); n > 1000 {
			t.Errorf("кусок %d длиннее лимита: %d символов", i, n)
		}
		joined += strings.Count(p, "слово")
	}
	if want := strings.Count(words, "слово"); joined != want {
		t.Errorf("при резке потерялись слова: %d из %d", joined, want)
	}
	for i, p := range parts {
		if strings.HasSuffix(p, "сло") || strings.HasSuffix(p, "слов") {
			t.Errorf("кусок %d обрывается посреди слова: …%q", i, p[len(p)-12:])
		}
	}
	// Короткий текст остаётся одним куском — лишнего дробления быть не должно.
	if got := chunkText("короткая строка", 1000); len(got) != 1 {
		t.Errorf("короткий текст разрезан на %d кусков", len(got))
	}
}

func TestFrameTimesInsideClip(t *testing.T) {
	times := frameTimes(100, 4)
	if len(times) != 4 {
		t.Fatalf("ждали 4 точки, получили %d", len(times))
	}
	for i, tm := range times {
		if tm <= 0 || tm >= 100 {
			t.Errorf("точка %d вне ролика: %v", i, tm)
		}
		if i > 0 && tm <= times[i-1] {
			t.Errorf("точки не возрастают: %v", times)
		}
	}
	if got := frameTimes(0, 3); len(got) != 1 {
		t.Errorf("для ролика без длительности ждали одну точку, получили %v", got)
	}
	if got := frameTimes(100, 0); got != nil {
		t.Errorf("ноль кадров = ничего не вынимаем, получили %v", got)
	}
}

// autoFrameCount — это правило «не плати зрением за пустоту и не оставляй разбор пустым».
func TestAutoFrameCountRules(t *testing.T) {
	video := mediaInfo{Duration: 60, Video: &mediaStream{Width: 1920, Height: 1080, FPS: 30}}
	longVideo := mediaInfo{Duration: 3600, Video: video.Video}
	audioOnly := mediaInfo{Duration: 60}

	if got := autoFrameCount(video, false); got != 8 {
		t.Errorf("немой ролик: кадры единственный источник, ждали 8, получили %d", got)
	}
	if got := autoFrameCount(video, true); got != 5 {
		t.Errorf("короткий ролик с речью: ждали 5 кадров, получили %d", got)
	}
	if got := autoFrameCount(longVideo, true); got != 4 {
		t.Errorf("длинный ролик с речью: ждали 4 кадра (слайды), получили %d", got)
	}
	if got := autoFrameCount(audioOnly, true); got != 0 {
		t.Errorf("только звук: кадров нет физически, ждали 0, получили %d", got)
	}
}

func TestScaleFilterForms(t *testing.T) {
	cases := map[string]string{
		"720":      "scale=-2:720",
		"1080p":    "scale=-2:1080",
		"1280x720": "scale=1280:720",
		"":         "",
		"большой":  "",
	}
	for in, want := range cases {
		if got := scaleFilter(in); got != want {
			t.Errorf("scaleFilter(%q) = %q, ждали %q", in, got, want)
		}
	}
	// -2, а не -1: h264 не кодирует нечётную сторону и падает с невнятной ошибкой.
	if !strings.Contains(scaleFilter("720"), "-2") {
		t.Error("масштаб обязан держать сторону чётной (-2)")
	}
}

// Windows-путь — не ссылка, хотя двоеточие в нём есть.
func TestIsRemoteMediaSource(t *testing.T) {
	remote := []string{"https://youtu.be/x", "http://example.com/a.mp4"}
	local := []string{`C:\video\лекция.mp4`, "runtime/browser/media/x.mp4", "./clip.mkv", ""}
	for _, s := range remote {
		if !isRemoteMediaSource(s) {
			t.Errorf("%q — ссылка", s)
		}
	}
	for _, s := range local {
		if isRemoteMediaSource(s) {
			t.Errorf("%q — локальный путь, а не ссылка", s)
		}
	}
}

func TestSafeFileStemSurvivesWindows(t *testing.T) {
	got := safeFileStem(`Как: сделать "бота" / v2 <тест>`)
	for _, bad := range []string{":", "/", `\`, `"`, "<", ">", "|", "*", "?"} {
		if strings.Contains(got, bad) {
			t.Errorf("в имени файла остался запрещённый символ %q: %q", bad, got)
		}
	}
	if got == "" {
		t.Error("имя файла не может быть пустым")
	}
	if n := len([]rune(safeFileStem(strings.Repeat("длинное", 50)))); n > 60 {
		t.Errorf("имя не обрезано: %d символов", n)
	}
}

// Чистка описания кадра трогает только оформление: содержание обязано остаться целиком.
func TestTidyFrameNoteKeepsContent(t *testing.T) {
	raw := "**Сцена:** белый слайд с кодом.  \n\n━━━━━━━━━━━━━━\n\n\n" +
		"ТЕКСТ НА ЭКРАНЕ:\ngithub.com/freqtrade/freqtrade\nstoploss = -0.02\n---\n"
	got := tidyFrameNote(raw)
	for _, want := range []string{"белый слайд с кодом", "github.com/freqtrade/freqtrade", "stoploss = -0.02"} {
		if !strings.Contains(got, want) {
			t.Errorf("содержание %q потерялось: %q", want, got)
		}
	}
	if strings.Contains(got, "━") || strings.Contains(got, "\n---") {
		t.Errorf("разделители остались: %q", got)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("пустые строки не схлопнулись: %q", got)
	}
	// Строка с буквами или цифрами не может быть удалена как «оформление».
	if isDecorationLine("--- итог 2 ---") {
		t.Error("строка с содержанием принята за разделитель")
	}
}

// Обрезка описания кадра не имеет права съесть дословный текст с экрана — он невосстановим,
// а проза восстановима. Проверяется ОБА порядка: модель ставит блок то до рассуждения, то после.
func TestCapFrameNoteKeepsScreenText(t *testing.T) {
	screen := "ТЕКСТ НА ЭКРАНЕ:\ngithub.com/freqtrade/freqtrade\nstoploss = -0.02"
	longProse := strings.Repeat("На кадре виден тёмный слайд с зелёным текстом. ", 40)

	for _, name := range []string{"текст-после-прозы", "текст-до-прозы"} {
		note := longProse + "\n" + screen
		if name == "текст-до-прозы" {
			note = screen + "\n" + longProse
		}
		got := capFrameNote(note, 300)
		if n := len([]rune(got)); n > 300 {
			t.Errorf("%s: не влезли в лимит: %d символов", name, n)
		}
		for _, must := range []string{"github.com/freqtrade/freqtrade", "stoploss = -0.02"} {
			if !strings.Contains(got, must) {
				t.Errorf("%s: обрезка съела дословный текст %q:\n%s", name, must, got)
			}
		}
	}

	// Жирный markdown-заголовок не должен ломать поиск блока.
	bold := longProse + "\n**ТЕКСТ НА ЭКРАНЕ:**\nFirecrawl"
	if got := capFrameNote(bold, 200); !strings.Contains(got, "Firecrawl") {
		t.Errorf("жирный заголовок сбил обрезку: %s", got)
	}
	// Короткое описание не трогаем вовсе.
	if got := capFrameNote("коротко", 300); got != "коротко" {
		t.Errorf("короткое описание изменено: %q", got)
	}
	// Без блока текста работает обычная обрезка по длине.
	if n := len([]rune(capFrameNote(longProse, 100))); n > 100 {
		t.Errorf("описание без текста экрана не обрезано: %d символов", n)
	}
}

func TestStripStampPrefix(t *testing.T) {
	if got := stripStampPrefix("20260813-150937-clip"); got != "clip" {
		t.Errorf("метка времени не снята: %q", got)
	}
	// Чужое имя, похожее на метку лишь отчасти, трогать нельзя.
	for _, keep := range []string{"clip", "2026-08-13-clip", "лекция-1"} {
		if got := stripStampPrefix(keep); got != keep {
			t.Errorf("stripStampPrefix(%q) = %q — обычное имя испорчено", keep, got)
		}
	}
}

func TestParseFrameRate(t *testing.T) {
	if got := parseFrameRate("30000/1001"); got < 29.9 || got > 30.0 {
		t.Errorf("29.97 fps разобрано как %v", got)
	}
	if got := parseFrameRate("25/1"); got != 25 {
		t.Errorf("25 fps разобрано как %v", got)
	}
	// 0/0 — это обложка трека, а не видеодорожка: кадры из неё брать бессмысленно.
	if got := parseFrameRate("0/0"); got != 0 {
		t.Errorf("обложка должна давать 0 fps, получили %v", got)
	}
}

// Ворота: конвертация без output пишет в свой каталог и не спрашивает; явный путь наружу —
// это обычная запись на диск со всеми её правилами.
func TestGuardConvertMedia(t *testing.T) {
	own := classifyToolCall("convert_media", map[string]any{"input": "a.mp4"})
	if own.Action != ActionAllow {
		t.Errorf("без output ждали allow, получили %s (%s)", own.Action, own.Reason)
	}
	outside := classifyToolCall("convert_media", map[string]any{
		"input": "a.mp4", "output": `C:\Windows\System32\x.mp4`,
	})
	if outside.Action == ActionAllow {
		t.Error("запись в System32 не может быть молчаливой")
	}
}

// Схемы пака: имена, обязательные поля и то, что диспетчер их узнаёт как локальные.
func TestVideoToolSpecsAndDispatch(t *testing.T) {
	specs := videoToolSpecs()
	want := map[string]bool{
		"analyze_video": false, "transcribe_media": false, "video_frames": false,
		"media_info": false, "convert_media": false, "speak_text": false,
	}
	for _, s := range specs {
		if _, ok := want[s.Function.Name]; !ok {
			t.Errorf("лишний инструмент в паке: %s", s.Function.Name)
			continue
		}
		want[s.Function.Name] = true
		if !localToolNames[s.Function.Name] {
			t.Errorf("%s не зарегистрирован как локальный — исполнитель пошлёт его в browser.Session",
				s.Function.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("инструмент %s пропал из пака", name)
		}
	}
	// analyze_video обязан принимать и путь, и ссылку одним полем.
	for _, s := range specs {
		if s.Function.Name != "analyze_video" {
			continue
		}
		props, _ := s.Function.Parameters["properties"].(map[string]any)
		if _, ok := props["source"]; !ok {
			t.Error("analyze_video без параметра source")
		}
		req, _ := s.Function.Parameters["required"].([]string)
		if len(req) != 1 || req[0] != "source" {
			t.Errorf("обязательным должен быть только source, получили %v", req)
		}
	}
}

func TestDispatchVideoToolRejectsEmptySource(t *testing.T) {
	task := newTask(safeActor(), "тест")
	defer task.Close()
	res, ok := dispatchVideoTool(task, Config{}, "analyze_video", map[string]any{})
	if !ok {
		t.Fatal("analyze_video должен диспетчеризоваться этим файлом")
	}
	if res.Status != StatusFailed || !strings.Contains(res.Text, "source") {
		t.Errorf("без source ждали внятный отказ, получили %s / %q", res.Status, res.Text)
	}
	if _, ok := dispatchVideoTool(task, Config{}, "web_search", nil); ok {
		t.Error("чужой инструмент не должен перехватываться")
	}
}

func TestArgBoolValueForms(t *testing.T) {
	cases := []struct {
		in   any
		want bool
	}{
		{true, true}, {false, false},
		{"true", true}, {"да", true}, {"нет", false}, {"0", false},
		{1.0, true}, {0.0, false},
	}
	for _, c := range cases {
		if got := argBoolValue(c.in, !c.want); got != c.want {
			t.Errorf("argBoolValue(%v) = %v, ждали %v", c.in, got, c.want)
		}
	}
	// Неизвестное значение не должно молча переключать смысл — берётся значение по умолчанию.
	if !argBoolValue("может быть", true) {
		t.Error("непонятное значение обязано оставить умолчание")
	}
}

// Разбор апдейта Telegram: три разных поля несут одно и то же — ролик. Пока `video` не был
// объявлен в структуре, обычное видео вообще не доходило до обработчика: сообщение выглядело
// пустым, и бот отвечал «понимаю текст, голосовые, картинки и файлы».
func TestTelegramUpdateCarriesVideo(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"video", `{"message":{"chat":{"id":1},"caption":"разбери",
			"video":{"file_id":"VID","duration":42,"mime_type":"video/mp4","file_name":"a.mp4","file_size":1048576}}}`, "VID"},
		{"video_note", `{"message":{"chat":{"id":1},
			"video_note":{"file_id":"NOTE","duration":7}}}`, "NOTE"},
		{"animation", `{"message":{"chat":{"id":1},
			"animation":{"file_id":"GIF","duration":3,"mime_type":"video/mp4"}}}`, "GIF"},
	}
	for _, c := range cases {
		var upd tgUpdate
		if err := json.Unmarshal([]byte(c.body), &upd); err != nil {
			t.Fatalf("%s: не разобрался JSON: %v", c.name, err)
		}
		v := firstVideo(upd.Message)
		if v == nil {
			t.Errorf("%s: ролик не найден в сообщении", c.name)
			continue
		}
		if v.FileID != c.want {
			t.Errorf("%s: file_id = %q, ждали %q", c.name, v.FileID, c.want)
		}
	}

	// Размер нужен, чтобы честно объяснить лимит Telegram ДО попытки скачивания.
	var upd tgUpdate
	_ = json.Unmarshal([]byte(cases[0].body), &upd)
	v := firstVideo(upd.Message)
	if v.FileSize != 1048576 || v.Duration != 42 {
		t.Errorf("размер/длительность разобраны неверно: %+v", v)
	}
	note := strings.ToLower(videoTooBigNote(50 << 20))
	if !strings.Contains(note, "ссылку") || !strings.Contains(note, "путь") {
		t.Error("объяснение лимита обязано предлагать рабочие выходы (ссылка, путь к файлу), а не только отказ")
	}

	// Видео, присланное файлом, тоже должно попадать в разбор.
	var doc tgUpdate
	_ = json.Unmarshal([]byte(`{"message":{"chat":{"id":1},
		"document":{"file_id":"DOC","file_name":"lecture.mkv","mime_type":"application/octet-stream","file_size":123}}}`), &doc)
	if got := classifyMedia(doc.Message.Document.FileName, doc.Message.Document.MimeType); got != mediaVideo {
		t.Errorf("документ .mkv распознан как %v, а не видео", got)
	}
	if doc.Message.Document.FileSize != 123 {
		t.Errorf("размер документа не разобран: %+v", doc.Message.Document)
	}
}

// ===== с настоящим ffmpeg =====

// testClip собирает трёхсекундный ролик со звуком генератором ffmpeg — не нужно тащить
// бинарный файл в репозиторий, а проверяются НАСТОЯЩИЕ команды, которые строит наш код.
func testClip(t *testing.T, cfg Config, dir string) string {
	t.Helper()
	clip := filepath.Join(dir, "clip.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	err := runFFmpeg(ctx, cfg, 90*time.Second,
		"-f", "lavfi", "-i", "testsrc=duration=3:size=320x240:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3",
		"-shortest", "-pix_fmt", "yuv420p", clip)
	if err != nil {
		t.Skipf("ffmpeg не собрал тестовый ролик: %v", err)
	}
	return clip
}

func TestProbeAndConvertWithRealFFmpeg(t *testing.T) {
	cfg := Config{}
	if err := ffmpegReady(cfg); err != nil {
		t.Skipf("ffmpeg/ffprobe нет в системе: %v", err)
	}
	dir := t.TempDir()
	clip := testClip(t, cfg, dir)
	ctx := context.Background()

	info, err := probeMedia(ctx, cfg, clip)
	if err != nil {
		t.Fatalf("probeMedia: %v", err)
	}
	if info.Duration < 2.5 || info.Duration > 3.5 {
		t.Errorf("длительность %v, ждали ~3 с", info.Duration)
	}
	if !info.hasVideo() || info.Video.Width != 320 || info.Video.Height != 240 {
		t.Errorf("видеодорожка разобрана неверно: %+v", info.Video)
	}
	if !info.hasAudio() {
		t.Error("звуковая дорожка не найдена, хотя она есть")
	}
	if !strings.Contains(info.Render(), "320x240") {
		t.Errorf("Render() не показывает разрешение: %s", info.Render())
	}

	// Кадр: реальная команда с -ss до -i и масштабированием.
	frames, err := extractFrames(ctx, cfg, clip, []float64{1.0})
	if err != nil {
		t.Fatalf("extractFrames: %v", err)
	}
	defer func() {
		for _, f := range frames {
			os.Remove(f.Path)
		}
	}()
	if len(frames) != 1 || len(frames[0].Bytes) < 500 {
		t.Fatalf("кадр не вынулся как следует: %+v", frames)
	}
	if !strings.HasPrefix(string(frames[0].Bytes[:2]), "\xff\xd8") {
		t.Error("кадр не похож на JPEG")
	}

	// Вытащить звук: в результате не должно остаться видеодорожки.
	mp3 := filepath.Join(dir, "out.mp3")
	out, oinfo, err := convertMedia(ctx, cfg, convertOpts{Input: clip, Output: mp3, AudioOnly: true})
	if err != nil {
		t.Fatalf("convertMedia (звук): %v", err)
	}
	if out != mp3 {
		t.Errorf("файл ушёл не туда: %s", out)
	}
	if oinfo.hasVideo() {
		t.Error("в audio_only-результате осталась видеодорожка")
	}
	if !oinfo.hasAudio() {
		t.Error("в audio_only-результате нет звука")
	}

	// Обрезка по времени: from/to должны реально резать.
	cut := filepath.Join(dir, "cut.mp4")
	_, cinfo, err := convertMedia(ctx, cfg, convertOpts{Input: clip, Output: cut, From: 0.5, To: 1.5})
	if err != nil {
		t.Fatalf("convertMedia (обрезка): %v", err)
	}
	if cinfo.Duration > 1.6 {
		t.Errorf("обрезка не сработала: длительность %v, ждали ~1 с", cinfo.Duration)
	}

	// Вход и выход в один файл — это молчаливая порча исходника.
	if _, _, err := convertMedia(ctx, cfg, convertOpts{Input: clip, Output: clip}); err == nil {
		t.Error("конвертация файла в самого себя должна отказывать")
	}
}
