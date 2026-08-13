package main

// Живая проверка слоя видео на НАСТОЯЩИХ сервисах: ffmpeg → распознавание речи → зрение →
// модель. Обычным `go test` не запускается — нужны поднятый мозг и TypeWhisper, а в CI их нет:
//
//	KIBBORG_LIVE_MEDIA=путь\к\ролику.mp4  go test -run TestLive -timeout 20m .
//
// Смысл её существования: юнит-тесты проверяют, что мы правильно СОБИРАЕМ команды и разбираем
// ответы, а здесь проверяется единственное, что нельзя проверить иначе, — что из ролика
// действительно выходит текст.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func liveMediaClip(t *testing.T) (Config, string) {
	t.Helper()
	clip := strings.TrimSpace(os.Getenv("KIBBORG_LIVE_MEDIA"))
	if clip == "" {
		t.Skip("KIBBORG_LIVE_MEDIA не задан — живая проверка пропущена")
	}
	if _, err := os.Stat(clip); err != nil {
		t.Fatalf("ролик не найден: %s", clip)
	}
	cfg := loadConfig("settings.ini")
	if err := ffmpegReady(cfg); err != nil {
		t.Skipf("%v", err)
	}
	if !typeWhisperReady(cfg) && !whisperCppReady(cfg) {
		t.Skip("распознавание речи не отвечает — запусти TypeWhisper")
	}
	return cfg, clip
}

// Полный разбор: речь обязана превратиться в текст, кадры — в описание.
func TestLiveVideoAnalysis(t *testing.T) {
	cfg, clip := liveMediaClip(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	started := time.Now()
	digest, err := analyzeMedia(ctx, cfg, mediaAnalyzeOpts{
		Source:   clip,
		Question: "о чём этот ролик",
		Frames:   -1,
		Speech:   true,
		ChatID:   webChatID,
		Progress: func(s string) { t.Logf("… %s", s) },
	})
	if err != nil {
		t.Fatalf("analyzeMedia: %v", err)
	}
	t.Logf("разбор занял %s", time.Since(started).Round(time.Second))
	t.Logf("метаданные: %s", strings.ReplaceAll(digest.Info.Render(), "\n", " · "))
	t.Logf("расшифровка (%s): %s", digest.TranscriptFrom, capAgentText(digest.Transcript, 600))
	for _, f := range digest.Frames {
		t.Logf("кадр: %s", f)
	}

	if strings.TrimSpace(digest.Transcript) == "" {
		t.Fatal("речь не распознана — из ролика не вышло ни слова")
	}
	if digest.TranscriptPath == "" {
		t.Error("полная расшифровка не сохранена файлом")
	} else if st, err := os.Stat(digest.TranscriptPath); err != nil || st.Size() == 0 {
		t.Errorf("файл расшифровки пуст или отсутствует: %s", digest.TranscriptPath)
	}
	if digest.Info.Duration <= 0 {
		t.Error("длительность не определилась")
	}
	if r := digest.Render(); !strings.Contains(r, "Разбор видео") {
		t.Errorf("Render() не похож на разбор: %s", capAgentText(r, 200))
	}
}

// Пересказ длинного текста картой-свёрткой: главный механизм «любых объёмов».
func TestLiveTranscriptSummary(t *testing.T) {
	cfg, _ := liveMediaClip(t)
	if !brainReady(cfg.BrainPort) {
		t.Skip("мозг не готов")
	}
	// ~15 000 символов: гарантированно несколько кусков и минимум один раунд свёртки.
	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString("Докладчик разбирает очередной шаг настройки, описывает параметры индикатора, " +
			"предупреждает о проскальзывании на тонком рынке и советует проверять всё на " +
			"исторических данных перед тем, как запускать стратегию на реальные деньги. ")
	}
	long := b.String()
	if len([]rune(long)) < transcriptChunkChars*2 {
		t.Fatalf("тестовый текст слишком короткий: %d символов", len([]rune(long)))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	started := time.Now()
	sum, err := summariseTranscript(ctx, cfg, "что советует автор", long)
	if err != nil {
		t.Fatalf("summariseTranscript: %v", err)
	}
	t.Logf("свёртка %d → %d символов за %s",
		len([]rune(long)), len([]rune(sum)), time.Since(started).Round(time.Second))
	t.Logf("выжимка: %s", capAgentText(sum, 800))
	if strings.TrimSpace(sum) == "" {
		t.Fatal("выжимка пустая")
	}
	if len([]rune(sum)) >= len([]rune(long)) {
		t.Errorf("выжимка не короче исходника: %d против %d", len([]rune(sum)), len([]rune(long)))
	}
}
