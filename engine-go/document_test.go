package main

// Слой документов (§23). Чистые функции проверяются всегда; распознавание живого скана —
// только по требованию: нужны poppler и tesseract, а в CI их нет.
//
//	KIBBORG_LIVE_PDF=путь\к\скану.pdf go test -run TestLivePDF -timeout 20m .

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParsePageRangeForms(t *testing.T) {
	cases := []struct {
		in       string
		from, to int
	}{
		{"", 0, 0},
		{"7", 7, 7},
		{"2-10", 2, 10},
		{"2 - 10", 2, 10},
		{"2–10", 2, 10}, // длинное тире: так пишет человек
		{"-20", 0, 20},
		{"5-", 5, 0},
		{"чепуха", 0, 0},
	}
	for _, c := range cases {
		from, to := parsePageRange(c.in)
		if from != c.from || to != c.to {
			t.Errorf("parsePageRange(%q) = %d–%d, ждали %d–%d", c.in, from, to, c.from, c.to)
		}
	}
}

// PDF обязан попадать в свою ветку, а не в «текстовый файл» и не в «неизвестное».
func TestClassifyMediaPDF(t *testing.T) {
	cases := []struct{ name, mime string }{
		{"акт.pdf", ""},
		{"акт.pdf", "application/octet-stream"}, // Telegram часто присылает так
		{"скан", "application/pdf"},             // а браузер — так
		{"ДОГОВОР.PDF", ""},
	}
	for _, c := range cases {
		if got := classifyMedia(c.name, c.mime); got != mediaPDF {
			t.Errorf("classifyMedia(%q, %q) = %v, ждали pdf", c.name, c.mime, got)
		}
	}
	if got := classifyMedia("readme.md", ""); got == mediaPDF {
		t.Error("markdown не может быть PDF")
	}
}

// Отчёт обязан говорить, ОТКУДА взялся текст: слой, распознавание или зрение. Без этого
// пользователь не отличит точный текст от распознанного с ошибками.
func TestDocDigestRenderShowsSource(t *testing.T) {
	d := docDigest{
		Path: `D:\док.pdf`, Pages: 12, From: 1, To: 12,
		TextPages: 2, OCRPages: 9, EyePages: 1, EmptyPage: 0,
		Text: "текст", TextPath: `D:\док.txt`,
	}
	out := d.Render()
	for _, must := range []string{"текстовый слой — 2", "распознано с картинки — 9", "прочитано зрением — 1", `D:\док.txt`} {
		if !strings.Contains(out, must) {
			t.Errorf("в отчёте нет %q:\n%s", must, out)
		}
	}
}

// Язык распознавания выбирается по РЕАЛЬНО доступным файлам: просить rus там, где его нет,
// значит получить отказ tesseract на весь вызов вместо распознавания хотя бы латиницы.
// Каталог языков должен находиться по явной настройке — от рабочего каталога зависеть нельзя.
// Молчаливый откат на английский выдаёт кириллицу латиницей: текст есть, ошибок нет, смысла нет.
func TestTessdataResolutionAndLanguages(t *testing.T) {
	// Пусто везде → английский, но НИКОГДА не пустая строка (иначе tesseract откажет совсем).
	if got := ocrLanguages(Config{TessdataDir: filepath.Join(t.TempDir(), "нет")}); got != "eng" {
		t.Errorf("без языковых пакетов ждали eng, получили %q", got)
	}

	// Явный каталог из настроек обязан находиться и попадать в список языков.
	dir := t.TempDir()
	for _, l := range []string{"rus", "eng"} {
		if err := os.WriteFile(filepath.Join(dir, l+".traineddata"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{TessdataDir: dir}
	if got := tessdataDir(cfg); got == "" {
		t.Fatal("каталог из настроек не найден")
	}
	langs := ocrLanguages(cfg)
	if !strings.Contains(langs, "rus") || !strings.Contains(langs, "eng") {
		t.Errorf("ждали rus+eng, получили %q", langs)
	}
	if note := missingRussianNote(cfg); note != "" {
		t.Errorf("русский на месте — предупреждения быть не должно: %s", note)
	}

	// Русского нет → обязано быть громкое предупреждение, а не тихая латиница.
	only := t.TempDir()
	if err := os.WriteFile(filepath.Join(only, "eng.traineddata"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	note := missingRussianNote(Config{TessdataDir: only})
	if note == "" {
		t.Error("без русского пакета кириллица выйдет латиницей — об этом обязано быть сказано")
	}
	if !strings.Contains(note, "rus.traineddata") {
		t.Errorf("предупреждение должно называть, что именно положить: %s", note)
	}
}

// read_document должен быть в паке files и диспетчеризоваться локально.
func TestDocumentToolWired(t *testing.T) {
	specs := docToolSpecs()
	if len(specs) != 1 || specs[0].Function.Name != "read_document" {
		t.Fatalf("ждали один инструмент read_document, получили %+v", specs)
	}
	if !localToolNames["read_document"] {
		t.Error("read_document не зарегистрирован как локальный")
	}
	task := newTask(safeActor(), "тест")
	defer task.Close()
	res, ok := dispatchDocumentTool(task, Config{}, "read_document", map[string]any{})
	if !ok {
		t.Fatal("read_document должен диспетчеризоваться этим файлом")
	}
	if res.Status != StatusFailed || !strings.Contains(res.Text, "path") {
		t.Errorf("без path ждали внятный отказ, получили %s / %q", res.Status, res.Text)
	}
	if _, ok := dispatchDocumentTool(task, Config{}, "read_file", nil); ok {
		t.Error("чужой инструмент не должен перехватываться")
	}
}

// Поиск по документу — то, ради чего справочники вообще открывают. Регистр и ё/е значения
// иметь не должны: иначе поиск зависит от того, как документ набирали.
func TestFindInPages(t *testing.T) {
	pages := []docPage{
		{Num: 1, Text: "Оглавление. Предохранители и реле. Схема подключения фар."},
		{Num: 7, Text: "Предохранитель F12 отвечает за ближний свет фар, 15 А."},
		{Num: 9, Text: "Ничего интересного."},
		{Num: 12, Text: "Клеммная колодка X6: питание фар, сечение 2.5 мм."},
	}
	hits := findInPages(pages, "фар", 10)
	if len(hits) != 3 {
		t.Fatalf("ждали 3 находки, получили %d: %+v", len(hits), hits)
	}
	if hits[0].Page != 1 || hits[1].Page != 7 || hits[2].Page != 12 {
		t.Errorf("страницы найдены неверно: %+v", hits)
	}
	if !strings.Contains(hits[1].Text, "F12") {
		t.Errorf("вокруг находки должен быть контекст: %q", hits[1].Text)
	}
	// Регистр не важен.
	if len(findInPages(pages, "ПРЕДОХРАНИТЕЛЬ", 10)) == 0 {
		t.Error("поиск обязан игнорировать регистр")
	}
	// ё и е — одна буква.
	if len(findInPages([]docPage{{Num: 1, Text: "Замена щёток стартера"}}, "щеток", 5)) == 0 {
		t.Error("ё и е должны считаться одной буквой")
	}
	// Потолок находок соблюдается.
	many := []docPage{{Num: 1, Text: strings.Repeat("реле ", 100)}}
	if got := findInPages(many, "реле", 5); len(got) != 5 {
		t.Errorf("потолок находок не соблюдён: %d", len(got))
	}
	if findInPages(pages, "  ", 5) != nil {
		t.Error("пустой запрос не должен ничего находить")
	}
}

func TestQuestionTerms(t *testing.T) {
	got := questionTerms("Какой предохранитель отвечает за фары?")
	if len(got) == 0 {
		t.Fatal("из вопроса не выделено ни одного слова для поиска")
	}
	for _, w := range got {
		if strings.EqualFold(w, "какой") {
			t.Errorf("служебное слово попало в поиск: %v", got)
		}
		if len([]rune(w)) < 4 {
			t.Errorf("слишком короткое слово для поиска: %q", w)
		}
	}
	if len(questionTerms("что где как")) != 0 {
		t.Error("из одних служебных слов искать нечего")
	}
}

// Чистка извлечённого текста трогает ОФОРМЛЕНИЕ и только его: содержание, номера и колонки
// таблиц обязаны уцелеть.
func TestTidyDocTextKeepsData(t *testing.T) {
	raw := "ÁÁÁÁ ÁÁÁÁÁÁÁÁÁÁÁÁ ÁÁÁÁ ÁÁÁÁÁÁÁÁÁÁÁ\n" +
		" 0670.1  Fuse chart                         2460.12 Sequential M–transmission\n" +
		"------------------------\n" +
		"Предохранитель F12, 15 А, номер детали 0000123\n"
	got := tidyDocText(raw)
	for _, must := range []string{"0670.1", "Fuse chart", "2460.12", "F12, 15 А", "0000123"} {
		if !strings.Contains(got, must) {
			t.Errorf("данные потерялись (%q):\n%s", must, got)
		}
	}
	if strings.Contains(got, "ÁÁÁÁ") {
		t.Errorf("заливка ячеек осталась:\n%s", got)
	}
	if strings.Contains(got, "------") {
		t.Errorf("линейка-разделитель осталась:\n%s", got)
	}
	// Отступы, держащие колонки, ломать нельзя.
	if !strings.Contains(got, "  ") {
		t.Error("выравнивание колонок уничтожено")
	}
	// Строка из цифр — это данные, а не оформление.
	if isFillerLine("00000000") {
		t.Error("строка из цифр принята за заливку")
	}
	if isFillerLine("аааа") {
		t.Error("короткая строка не может быть заливкой")
	}
}

// ===== живой скан =====

func TestLivePDFScan(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("KIBBORG_LIVE_PDF"))
	if path == "" {
		t.Skip("KIBBORG_LIVE_PDF не задан — живая проверка пропущена")
	}
	cfg := loadConfig("settings.ini")
	// TestMain уводит тесты во временный каталог, поэтому относительный runtime/tessdata тут
	// не найдётся — каталог языков передаётся явно, как это сделал бы settings.ini.
	if td := strings.TrimSpace(os.Getenv("KIBBORG_TESSDATA")); td != "" {
		cfg.TessdataDir = td
	}
	if err := documentReady(cfg); err != nil {
		t.Skipf("%v", err)
	}
	if !ocrAvailable(cfg) {
		t.Skip("tesseract не найден")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// Диапазон, режим и вопрос задаются снаружи — иначе на большом документе нечем проверить
	// ни отдельную страницу, ни чтение глазами.
	from, to := parsePageRange(os.Getenv("KIBBORG_LIVE_PAGES"))
	question := os.Getenv("KIBBORG_LIVE_Q")
	if question == "" {
		question = "что это за документ"
	}
	started := time.Now()
	d, err := readDocument(ctx, cfg, docReadOpts{
		Path:     path,
		From:     from,
		To:       to,
		Question: question,
		Find:     os.Getenv("KIBBORG_LIVE_FIND"),
		Mode:     os.Getenv("KIBBORG_LIVE_MODE"),
		ChatID:   webChatID,
		Progress: func(s string) { t.Logf("… %s", s) },
	})
	if err != nil {
		t.Fatalf("readDocument: %v", err)
	}
	t.Logf("разбор занял %s", time.Since(started).Round(time.Second))
	t.Logf("страниц %d: слой %d, OCR %d (%s), зрение %d, пусто %d",
		d.Pages, d.TextPages, d.OCRPages, d.OCRLangs, d.EyePages, d.EmptyPage)
	t.Logf("текст:\n%s", capAgentText(d.Text, 1500))

	if strings.TrimSpace(d.Text) == "" {
		t.Fatal("из скана не вышло ни строчки")
	}
	if d.TextPath == "" {
		t.Error("полный текст не сохранён файлом")
	}
}
