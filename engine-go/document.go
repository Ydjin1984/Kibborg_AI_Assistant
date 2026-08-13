package main

// Слой документов (ТЗ §23). Тот же принцип, что и с видео: PDF — это КОНТЕЙНЕР, а не
// модальность. Внутри он либо уже текст, либо картинки, и оба случая движок умеет читать:
//
//	текстовый слой → pdftotext              → точный текст, мгновенно и бесплатно
//	скан страницы  → pdftoppm → tesseract   → буквальный текст (rus+eng)
//	плохой скан    → pdftoppm → зрение      → текст там, где OCR сдался
//
// Порядок именно такой. Текстовый слой точнее любого распознавания, а tesseract для документа
// лучше зрения по одной причине: он возвращает СИМВОЛЫ, а не пересказ. Модель, глядя на скан,
// склонна «улучшать» текст — для договора или акта это порча, а не польза. Зрение включается
// там, где tesseract вернул пустоту: кривой скан, рукопись, печать поверх текста.

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"log"
	"net/http"
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
	// pdfPageTextMin — ниже этого числа символов страница считается КАРТИНКОЙ, а не текстом.
	// Сканы часто несут тонкий текстовый слой из колонтитула или номера страницы; принять
	// его за содержание значит молча вернуть пользователю номера страниц вместо документа.
	pdfPageTextMin = 40
	// pdfRenderDPI — плотность рендера под OCR. 300 — то, на чём tesseract обучен; 150 даёт
	// заметно больше ошибок на мелком шрифте, 600 только замедляет.
	pdfRenderDPI = 300
	// pdfMaxOCRPages — потолок страниц, распознаваемых за один вызов.
	pdfMaxOCRPages = 300
	// pdfVisionRetryPages — сколько страниц, где tesseract сдался, отдать зрению.
	pdfVisionRetryPages = 10
	// docInlineChars — до этого размера текст уходит модели дословно, дальше — выжимка.
	docInlineChars = 6000
	// docSummaryMaxChars — потолок, выше которого документ НЕ пересказывается целиком.
	//
	// Взято с живого файла: руководство по электрике BMW E36 — 577 страниц и 3.7 млн символов.
	// Карта-свёртка на нём — это 600+ обращений к модели, то есть часы работы ради поверхностного
	// пересказа справочника, который никто не читает подряд. Такие документы не пересказывают,
	// в них ИЩУТ, поэтому выше порога включается поиск, а не свёртка.
	docSummaryMaxChars = 120_000
	// docFindMaxHits / docFindContext — сколько мест показывать и сколько символов вокруг.
	docFindMaxHits = 12
	docFindContext = 500
	// docIngestTimeout — потолок разбора документа, присланного в чат.
	docIngestTimeout = 30 * time.Minute
)

// ===== доступность инструментов =====

// popplerExe резолвит утилиту poppler: настройка → PATH → места, куда её кладёт winget.
//
// Последний шаг не роскошь: winget дописывает свои ярлыки в PATH ПОЛЬЗОВАТЕЛЯ, и уже
// запущенные процессы этого не видят до перезапуска. Без поиска по известным каталогам
// свежепоставленный poppler «не найден», хотя лежит на диске, — и виновата тут не установка,
// а наш способ его искать.
func popplerExe(cfg Config, name string) string {
	fileIn := func(dir string) string {
		for _, n := range []string{name + ".exe", name} {
			cand := filepath.Join(dir, n)
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				return cand
			}
		}
		return ""
	}
	if dir := strings.TrimSpace(cfg.PopplerDir); dir != "" {
		if p := fileIn(dir); p != "" {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		// Ярлыки winget.
		if p := fileIn(filepath.Join(la, "Microsoft", "WinGet", "Links")); p != "" {
			return p
		}
		// Сам пакет: …\WinGet\Packages\oschwartz10612.Poppler_…\poppler-XX\Library\bin
		pattern := filepath.Join(la, "Microsoft", "WinGet", "Packages", "*Poppler*", "poppler-*", "Library", "bin")
		if dirs, err := filepath.Glob(pattern); err == nil {
			sort.Sort(sort.Reverse(sort.StringSlice(dirs))) // свежая версия первой
			for _, d := range dirs {
				if p := fileIn(d); p != "" {
					return p
				}
			}
		}
	}
	return name
}

// tesseractExe резолвит tesseract: настройка → PATH → стандартный путь установки Windows.
func tesseractExe(cfg Config) string {
	if p := strings.TrimSpace(cfg.TesseractPath); p != "" {
		return p
	}
	if _, err := exec.LookPath("tesseract"); err == nil {
		return "tesseract"
	}
	def := `C:\Program Files\Tesseract-OCR\tesseract.exe`
	if st, err := os.Stat(def); err == nil && !st.IsDir() {
		return def
	}
	return "tesseract"
}

// tessdataDir — свой каталог языковых моделей. Он нужен затем, что установленный tesseract
// обычно несёт только eng, а дописывать в Program Files требует прав администратора: русский
// пакет кладётся рядом с движком и подключается флагом --tessdata-dir.
//
// Ищется в трёх местах, и последнее — рядом с ИСПОЛНЯЕМЫМ файлом. Относительный путь от
// рабочего каталога хрупок: запусти движок из другой папки — и распознавание молча съедет на
// английский, выдав кириллицу латиницей («AKT npnéma-nepezaun» вместо «АКТ приёма-передачи»).
// Такую подмену не видно ни в логе, ни в статусе: текст есть, ошибок нет, смысла нет.
func tessdataDir(cfg Config) string {
	var dirs []string
	if d := strings.TrimSpace(cfg.TessdataDir); d != "" {
		dirs = append(dirs, d)
	}
	dirs = append(dirs, filepath.Join("runtime", "tessdata"))
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "runtime", "tessdata"))
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".traineddata") {
				if abs, aerr := filepath.Abs(dir); aerr == nil {
					return abs
				}
				return dir
			}
		}
	}
	return ""
}

// ocrLanguages — какие языки РЕАЛЬНО доступны. Просить rus там, где его нет, значит получить
// от tesseract отказ на весь вызов вместо распознавания хотя бы латиницы.
func ocrLanguages(cfg Config) string {
	dir := tessdataDir(cfg)
	if dir == "" {
		return "eng"
	}
	var langs []string
	for _, l := range []string{"rus", "eng"} {
		if _, err := os.Stat(filepath.Join(dir, l+".traineddata")); err == nil {
			langs = append(langs, l)
		}
	}
	if len(langs) == 0 {
		return "eng"
	}
	return strings.Join(langs, "+")
}

// missingRussianNote предупреждает о молчаливой подмене языка. Без русского пакета tesseract
// не откажет — он выдаст правдоподобный латинский мусор, и заметить это может только человек.
func missingRussianNote(cfg Config) string {
	if strings.Contains(ocrLanguages(cfg), "rus") {
		return ""
	}
	return "русский языковой пакет для распознавания не найден — кириллица выйдет латиницей. " +
		"Положи rus.traineddata в runtime/tessdata (github.com/tesseract-ocr/tessdata) или укажи TESSDATA_DIR в settings.ini"
}

func haveExe(path string) bool {
	if _, err := exec.LookPath(path); err == nil {
		return true
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// documentReady объясняет, чего не хватает и как поставить. Текстовые PDF читаются одним
// pdftotext, поэтому отсутствие tesseract — не отказ, а лишь потеря сканов.
func documentReady(cfg Config) error {
	if !haveExe(popplerExe(cfg, "pdftotext")) || !haveExe(popplerExe(cfg, "pdftoppm")) {
		return fmt.Errorf("не найден poppler (pdftotext/pdftoppm). Поставь одной командой: " +
			"winget install oschwartz10612.Poppler — затем перезапусти Kibborg")
	}
	return nil
}

// ocrAvailable сообщает, есть ли чем распознавать сканы.
func ocrAvailable(cfg Config) bool { return haveExe(tesseractExe(cfg)) }

// ===== чтение PDF =====

// docPage — одна страница документа и происхождение её текста.
type docPage struct {
	Num    int
	Text   string
	Source string // текстовый слой | OCR | зрение | пусто
}

// docDigest — результат разбора документа.
type docDigest struct {
	Path      string
	Pages     int
	From, To  int
	TextPages int    // страниц, взятых из текстового слоя
	OCRPages  int    // страниц, распознанных tesseract
	EyePages  int    // страниц, прочитанных зрением
	EmptyPage int    // страниц, с которых не вышло ничего
	OCRLangs  string // какими языками распознавали — иначе подмену языка не заметить
	FullChars int    // сколько текста в СОХРАНЁННОМ файле, до обрезки для контекста
	Language  string // язык документа — по нему видно, на каком языке искать
	Text      string
	Summary   string
	Hits      []docHit // найденные места с номерами страниц (поиск вместо пересказа)
	TextPath  string
	Notes     []string
}

// Render — то, что видят и модель, и человек.
func (d docDigest) Render() string {
	var b strings.Builder
	b.WriteString("📄 **Разбор документа**\n")
	b.WriteString("Файл: " + d.Path + "\n")
	if d.Pages > 0 {
		fmt.Fprintf(&b, "Страниц: %d", d.Pages)
		if d.From > 1 || (d.To > 0 && d.To < d.Pages) {
			fmt.Fprintf(&b, " (разобраны %d–%d)", d.From, d.To)
		}
		b.WriteString("\n")
	}
	var how []string
	if d.TextPages > 0 {
		how = append(how, fmt.Sprintf("текстовый слой — %d", d.TextPages))
	}
	if d.OCRPages > 0 {
		langs := ""
		if d.OCRLangs != "" {
			langs = " (" + d.OCRLangs + ")"
		}
		how = append(how, fmt.Sprintf("распознано с картинки — %d%s", d.OCRPages, langs))
	}
	if d.EyePages > 0 {
		how = append(how, fmt.Sprintf("прочитано зрением — %d", d.EyePages))
	}
	if d.EmptyPage > 0 {
		how = append(how, fmt.Sprintf("пустых — %d", d.EmptyPage))
	}
	if len(how) > 0 {
		b.WriteString("Откуда текст: " + strings.Join(how, ", ") + "\n")
	}
	if d.Language != "" {
		b.WriteString("Язык документа: " + d.Language + "\n")
	}
	if len(d.Hits) > 0 {
		fmt.Fprintf(&b, "\n**Найдено мест: %d** (номер страницы — чтобы открыть документ):\n%s\n",
			len(d.Hits), hitsText(d.Hits))
	}
	if d.Summary != "" {
		b.WriteString("\n**О чём документ** (выжимка):\n" + d.Summary + "\n")
	} else if d.Text != "" && len(d.Hits) == 0 {
		b.WriteString("\n**Текст:**\n" + d.Text + "\n")
	}
	if d.TextPath != "" {
		// Считаем то, что ЛЕЖИТ В ФАЙЛЕ, а не то, что осталось в d.Text после обрезки под
		// контекст. На руководстве в 577 страниц отчёт сообщал «4893 символа» при 3.7 млн на
		// диске — число, которому нельзя верить, хуже отсутствующего числа.
		fmt.Fprintf(&b, "\n📄 Полный текст: %s (%d символов) — если нужны точные формулировки, читай файл.\n",
			d.TextPath, d.FullChars)
	}
	for _, n := range d.Notes {
		b.WriteString("⚠️ " + n + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// pdfPageCount спрашивает число страниц у pdfinfo.
func pdfPageCount(ctx context.Context, cfg Config, path string) (int, error) {
	out, err := runToolOut(ctx, popplerExe(cfg, "pdfinfo"), 2*time.Minute, path)
	if err != nil {
		return 0, fmt.Errorf("pdfinfo не прочитал файл: %v", err)
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(strings.ToLower(line), "pages:") {
			continue
		}
		if n, cerr := strconv.Atoi(strings.TrimSpace(line[len("Pages:"):])); cerr == nil {
			return n, nil
		}
	}
	return 0, fmt.Errorf("не смог определить число страниц")
}

// extractPDFText берёт текстовый слой ОДНИМ вызовом и режет его по страницам: pdftotext
// разделяет страницы символом перевода формы (\f), так что второй проход не нужен.
func extractPDFText(ctx context.Context, cfg Config, path string, from, to int) ([]string, error) {
	args := []string{"-layout", "-enc", "UTF-8"}
	if from > 0 {
		args = append(args, "-f", strconv.Itoa(from))
	}
	if to > 0 {
		args = append(args, "-l", strconv.Itoa(to))
	}
	args = append(args, path, "-") // "-" = вывод в stdout
	// Строго stdout: жалобы poppler на повреждённый файл идут в stderr и не имеют никакого
	// отношения к содержанию документа.
	out, err := runToolOut(ctx, popplerExe(cfg, "pdftotext"), 10*time.Minute, args...)
	if err != nil {
		return nil, fmt.Errorf("pdftotext: %v", err)
	}
	pages := strings.Split(out, "\f")
	// Последний \f даёт пустой хвост — он не страница.
	if n := len(pages); n > 0 && strings.TrimSpace(pages[n-1]) == "" {
		pages = pages[:n-1]
	}
	return pages, nil
}

// renderPDFPage рендерит одну страницу в PNG для распознавания.
func renderPDFPage(ctx context.Context, cfg Config, path string, page int, dir string) (string, error) {
	prefix := filepath.Join(dir, fmt.Sprintf("p%04d", page))
	err := func() error {
		_, e := runTool(ctx, popplerExe(cfg, "pdftoppm"), 10*time.Minute,
			"-png", "-r", strconv.Itoa(pdfRenderDPI),
			"-f", strconv.Itoa(page), "-l", strconv.Itoa(page),
			path, prefix)
		return e
	}()
	if err != nil {
		return "", err
	}
	// pdftoppm сам дописывает к префиксу номер страницы, ширина которого зависит от размера
	// документа («-1», «-01», «-001»), поэтому файл ищется маской, а не собирается строкой.
	matches, _ := filepath.Glob(prefix + "*.png")
	if len(matches) == 0 {
		return "", fmt.Errorf("страница %d не отрисовалась", page)
	}
	sort.Strings(matches)
	return matches[0], nil
}

// ocrImageFile распознаёт картинку tesseract'ом. Возвращает буквальный текст.
func ocrImageFile(ctx context.Context, cfg Config, imgPath string) (string, error) {
	args := []string{imgPath, "stdout", "-l", ocrLanguages(cfg)}
	if dir := tessdataDir(cfg); dir != "" {
		args = append(args, "--tessdata-dir", dir)
	}
	// tesseract пишет в stderr прогресс и предупреждения — в текст они попасть не должны.
	out, err := runToolOut(ctx, tesseractExe(cfg), 5*time.Minute, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// tidyDocText выкидывает из извлечённого текста типографский мусор.
//
// Живой пример: руководство BMW свёрстано в Interleaf 1999 года, и заливка ячеек таблицы
// вышла из pdftotext строками вида «ÁÁÁÁ ÁÁÁÁÁÁÁÁÁÁÁÁ ÁÁÁÁ». Смысла в них ноль, а места они
// занимают заметную долю страницы — и в предпросмотре, и в окрестностях находок при поиске.
//
// Убирается ТОЛЬКО оформление: строка удаляется, если один и тот же символ занимает почти всю
// её непробельную часть. Цифры не трогаем вовсе — «0000» бывает настоящим номером.
func tidyDocText(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, ln := range lines {
		if isFillerLine(ln) {
			continue
		}
		ln = collapseRuns(strings.TrimRight(ln, " \t"))
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

// isFillerLine: в строке ≥8 непробельных символов и ≥80% из них — один и тот же не-цифровой знак.
func isFillerLine(ln string) bool {
	counts := map[rune]int{}
	total := 0
	for _, r := range ln {
		if r == ' ' || r == '\t' {
			continue
		}
		if r >= '0' && r <= '9' {
			return false // цифры в строке — это данные, а не заливка
		}
		counts[r]++
		total++
	}
	if total < 8 {
		return false
	}
	for _, n := range counts {
		if n*100 >= total*80 {
			return true
		}
	}
	return false
}

// collapseRuns схлопывает подряд идущие одинаковые знаки (от четырёх) в один. Цифры и пробелы
// не трогает: «0000» может быть номером, а отступы держат колонки таблиц.
func collapseRuns(ln string) string {
	runes := []rune(ln)
	var b strings.Builder
	for i := 0; i < len(runes); {
		j := i
		for j < len(runes) && runes[j] == runes[i] {
			j++
		}
		run := j - i
		r := runes[i]
		digit := r >= '0' && r <= '9'
		if run >= 4 && !digit && r != ' ' && r != '\t' {
			b.WriteRune(r)
		} else {
			b.WriteString(string(runes[i:j]))
		}
		i = j
	}
	return b.String()
}

// docVisionPrompt просит зрение ПЕРЕПИСАТЬ страницу, а не пересказать её.
const docVisionPrompt = `Перед тобой скан страницы документа. Перепиши ВЕСЬ видимый текст ДОСЛОВНО, сохраняя порядок строк и абзацев.
Не пересказывай, не исправляй ошибки, не дополняй от себя. Числа, даты, номера и фамилии переписывай ровно как напечатано.
Если часть текста не читается — поставь на её месте [нечитаемо]. Если страница пустая — напиши «пусто».`

const docVisionSystemPrompt = `Ты переписываешь текст со скана документа буква в букву. Ты не редактор и не пересказчик: любое «улучшение» текста здесь — это порча документа.`

// docVisionAsk добавляет к переписыванию конкретный вопрос, если он есть.
//
// Нужно для страниц, где ответ выражен НЕ ТЕКСТОМ, а расположением. Живой пример —
// сводная таблица предохранителей BMW: номера в шапке, системы в строках, а связь между
// ними — отметка в ячейке. В текстовом слое эта связь теряется полностью: остаются подписи
// и отдельно ряд «30A 15A 30A…». Прочитать такое можно только ГЛАЗАМИ.
func docVisionAsk(opts docReadOpts, mode string) string {
	q := strings.TrimSpace(opts.Question)
	if q == "" || mode != "vision" {
		return docVisionPrompt
	}
	return docVisionPrompt + "\n\nОтдельно ответь на вопрос: «" + capAgentText(q, 300) + "».\n" +
		"Если ответ виден по РАСПОЛОЖЕНИЮ (отметка в ячейке таблицы, столбец над строкой, стрелка на схеме) — " +
		"напиши его строкой «ОТВЕТ: …» и укажи, из чего он следует. Если на странице ответа нет — напиши «ОТВЕТ: нет на этой странице»."
}

// ===== разбор =====

type docReadOpts struct {
	Path     string
	From, To int
	Question string
	Find     string // искать по документу вместо пересказа
	Mode     string // auto | text | ocr | vision
	ChatID   int64
	Progress func(string)
}

// docHit — найденное место с номером страницы: по нему человек открывает документ.
type docHit struct {
	Page int
	Text string
}

// findInPages ищет вхождения по всем страницам и возвращает окрестности находок.
// Регистр не важен, ё и е считаются одной буквой — иначе поиск по документу зависит от того,
// как его набирали.
func findInPages(pages []docPage, query string, max int) []docHit {
	needle := normalizeSearch(query)
	if needle == "" {
		return nil
	}
	needleRunes := []rune(needle)
	var out []docHit
	for _, p := range pages {
		if p.Text == "" || len(out) >= max {
			continue
		}
		orig := []rune(p.Text)
		hay := []rune(normalizeSearch(p.Text))
		// Приведение к нижнему регистру для кириллицы и латиницы длину в рунах сохраняет; если
		// вдруг нет — показываем приведённый текст, лишь бы позиции не разъехались.
		src := orig
		if len(hay) != len(orig) {
			src = hay
		}
		for i := 0; i+len(needleRunes) <= len(hay) && len(out) < max; {
			if !runesEqual(hay[i:i+len(needleRunes)], needleRunes) {
				i++
				continue
			}
			start := max0(i - docFindContext/2)
			end := i + len(needleRunes) + docFindContext/2
			if end > len(src) {
				end = len(src)
			}
			out = append(out, docHit{Page: p.Num, Text: strings.TrimSpace(string(src[start:end]))})
			i += len(needleRunes)
		}
	}
	return out
}

func runesEqual(a, b []rune) bool {
	for i := range b {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// normalizeSearch приводит текст к виду, в котором сравнение не зависит от регистра и ё/е.
// Длина в рунах сохраняется — на неё опирается пересчёт позиций.
func normalizeSearch(s string) string {
	return strings.ToLower(strings.NewReplacer("ё", "е", "Ё", "Е").Replace(s))
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// questionTerms достаёт из вопроса слова, по которым имеет смысл искать: длинные и не
// служебные. «Какой предохранитель отвечает за фары» → [предохранитель, отвечает, фары].
func questionTerms(q string) []string {
	stop := map[string]bool{
		"какой": true, "какая": true, "какие": true, "какое": true, "что": true, "где": true,
		"когда": true, "почему": true, "зачем": true, "сколько": true, "кто": true, "как": true,
		"это": true, "для": true, "или": true, "если": true, "надо": true, "нужно": true,
		"есть": true, "быть": true, "документ": true, "документе": true, "файл": true,
	}
	var out []string
	seen := map[string]bool{}
	for _, w := range wordTokens(q) {
		lw := normalizeSearch(w)
		if len([]rune(lw)) < 4 || stop[lw] || seen[lw] {
			continue
		}
		seen[lw] = true
		out = append(out, w)
		if len(out) >= 4 {
			break
		}
	}
	return out
}

// hitsText склеивает находки для показа и для выжимки.
func hitsText(hits []docHit) string {
	var b strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&b, "\n— стр. %d —\n%s\n", h.Page, h.Text)
	}
	return strings.TrimSpace(b.String())
}

// readDocument — весь слой одним вызовом.
func readDocument(ctx context.Context, cfg Config, opts docReadOpts) (docDigest, error) {
	var d docDigest
	if err := documentReady(cfg); err != nil {
		return d, err
	}
	abs, err := filepath.Abs(strings.TrimSpace(opts.Path))
	if err != nil {
		return d, fmt.Errorf("путь не разбирается: %s", opts.Path)
	}
	if st, serr := os.Stat(abs); serr != nil {
		return d, fmt.Errorf("файл не найден: %s", abs)
	} else if st.IsDir() {
		return d, fmt.Errorf("это каталог, а не файл: %s", abs)
	}
	d.Path = abs
	note := func(s string) {
		if opts.Progress != nil {
			opts.Progress(s)
		}
	}

	total, perr := pdfPageCount(ctx, cfg, abs)
	if perr != nil {
		return d, perr
	}
	d.Pages = total
	from, to := opts.From, opts.To
	if from <= 0 {
		from = 1
	}
	if to <= 0 || to > total {
		to = total
	}
	if from > to {
		return d, fmt.Errorf("диапазон страниц пуст: %d–%d при %d страницах", from, to, total)
	}
	d.From, d.To = from, to

	mode := strings.ToLower(strings.TrimSpace(opts.Mode))
	pages := make([]docPage, 0, to-from+1)

	// 0. Готовый текст с прошлого разбора. Повторный вызов на том же файле обязан быть
	// мгновенным: модель обращается к большому документу много раз подряд, и каждый такой
	// вызов заново перемалывал 577 страниц и заново писал 3.6 МБ на диск.
	if mode != "ocr" && mode != "vision" && from == 1 && to == total {
		if cached, ok := loadDocCache(docCachePath(abs)); ok && len(cached) >= total/2 {
			note("беру текст из прошлого разбора")
			pages = cached
		}
	}

	// 1. Текстовый слой — бесплатно и точнее любого распознавания.
	if len(pages) > 0 {
		// уже взят из кэша
	} else if mode != "ocr" && mode != "vision" {
		note("читаю текстовый слой")
		raw, terr := extractPDFText(ctx, cfg, abs, from, to)
		if terr != nil {
			d.Notes = append(d.Notes, "текстовый слой прочитать не вышло: "+terr.Error())
		}
		for i := 0; i < to-from+1; i++ {
			p := docPage{Num: from + i}
			if i < len(raw) {
				if txt := tidyDocText(raw[i]); len([]rune(txt)) >= pdfPageTextMin {
					p.Text, p.Source = txt, "текстовый слой"
				}
			}
			pages = append(pages, p)
		}
	} else {
		for i := from; i <= to; i++ {
			pages = append(pages, docPage{Num: i})
		}
	}

	// 2. Страницы-картинки — рендер и распознавание.
	var needScan []int
	for i := range pages {
		if pages[i].Text == "" {
			needScan = append(needScan, i)
		}
	}
	if mode == "text" && len(needScan) > 0 {
		d.Notes = append(d.Notes, fmt.Sprintf("%d страниц без текстового слоя пропущены (режим text)", len(needScan)))
		needScan = nil
	}
	if len(needScan) > pdfMaxOCRPages {
		d.Notes = append(d.Notes, fmt.Sprintf(
			"страниц-картинок %d, распознаны первые %d — остальные пропущены",
			len(needScan), pdfMaxOCRPages))
		needScan = needScan[:pdfMaxOCRPages]
	}

	if len(needScan) > 0 {
		useOCR := mode != "vision" && ocrAvailable(cfg)
		if !useOCR && mode != "vision" {
			d.Notes = append(d.Notes, "tesseract не найден — сканы читает зрение, это медленнее и менее буквально")
		}
		if useOCR {
			d.OCRLangs = ocrLanguages(cfg)
			if note := missingRussianNote(cfg); note != "" {
				d.Notes = append(d.Notes, note)
			}
		}
		work, werr := os.MkdirTemp(mediaWorkDir, "pdf-*")
		if werr != nil {
			if mkerr := os.MkdirAll(mediaWorkDir, 0o755); mkerr == nil {
				work, werr = os.MkdirTemp(mediaWorkDir, "pdf-*")
			}
		}
		if werr != nil {
			return d, fmt.Errorf("не смог создать рабочий каталог: %w", werr)
		}
		defer os.RemoveAll(work)

		note(fmt.Sprintf("страниц-картинок %d — распознаю", len(needScan)))
		visionLeft := pdfVisionRetryPages
		for n, idx := range needScan {
			if ctx.Err() != nil {
				d.Notes = append(d.Notes, fmt.Sprintf("время вышло: распознано %d страниц из %d", n, len(needScan)))
				break
			}
			img, rerr := renderPDFPage(ctx, cfg, abs, pages[idx].Num, work)
			if rerr != nil {
				log.Printf("[DOC] страница %d не отрисовалась: %v", pages[idx].Num, rerr)
				continue
			}
			if useOCR {
				if txt, oerr := ocrImageFile(ctx, cfg, img); oerr == nil &&
					len([]rune(strings.TrimSpace(txt))) >= pdfPageTextMin {
					pages[idx].Text, pages[idx].Source = txt, "OCR"
					_ = os.Remove(img)
					if (n+1)%10 == 0 {
						note(fmt.Sprintf("распознано %d из %d страниц", n+1, len(needScan)))
					}
					continue
				}
			}
			// tesseract сдался (кривой скан, рукопись) либо его нет — пробуем зрением.
			if visionLeft > 0 && requireVisionOK(cfg) == nil {
				if data, rderr := os.ReadFile(img); rderr == nil {
					if txt, verr := describeImageBytes(cfg, opts.ChatID, docVisionAsk(opts, mode),
						docVisionSystemPrompt, "image/png", data); verr == nil {
						if t := strings.TrimSpace(txt); t != "" && !strings.EqualFold(t, "пусто") {
							pages[idx].Text, pages[idx].Source = t, "зрение"
							visionLeft--
						}
					}
				}
			}
			_ = os.Remove(img)
		}
	}

	// 3. Сборка.
	var b strings.Builder
	for _, p := range pages {
		switch p.Source {
		case "текстовый слой":
			d.TextPages++
		case "OCR":
			d.OCRPages++
		case "зрение":
			d.EyePages++
		default:
			d.EmptyPage++
			continue
		}
		fmt.Fprintf(&b, "\n=== Страница %d ===\n%s\n", p.Num, strings.TrimSpace(p.Text))
	}
	d.Text = strings.TrimSpace(b.String())
	if d.Text == "" {
		if len(d.Notes) > 0 {
			return d, fmt.Errorf("%s", strings.Join(d.Notes, "; "))
		}
		return d, fmt.Errorf("из документа не удалось достать ни строчки текста")
	}

	d.FullChars = len([]rune(d.Text))
	d.Language = docLanguage(d.Text)
	if path, werr := saveDocumentText(abs, d.Text); werr == nil {
		d.TextPath = path
	}

	// Что делать с текстом дальше — зависит от размера. Справочник на 577 страниц не
	// пересказывают, в нём ищут: свёртка там означала бы сотни обращений к модели ради
	// поверхностного пересказа того, что никто не читает подряд.
	textLen := len([]rune(d.Text))
	huge := textLen > docSummaryMaxChars
	switch {
	case strings.TrimSpace(opts.Find) != "":
		note("ищу по документу: " + opts.Find)
		d.Hits = findInPages(pages, opts.Find, docFindMaxHits)
		if len(d.Hits) == 0 {
			d.Notes = append(d.Notes, "по запросу «"+opts.Find+"» в документе ничего не нашлось")
		}

	case huge && strings.TrimSpace(opts.Question) != "":
		terms := questionTerms(opts.Question)
		note("документ большой — ищу по словам вопроса: " + strings.Join(terms, ", "))
		for _, t := range terms {
			d.Hits = append(d.Hits, findInPages(pages, t, docFindMaxHits-len(d.Hits))...)
			if len(d.Hits) >= docFindMaxHits {
				break
			}
		}
		if len(d.Hits) == 0 {
			d.Notes = append(d.Notes, fmt.Sprintf(
				"документ большой (%d стр., %d символов) — целиком не пересказываю. "+
					"По словам вопроса ничего не нашлось: задай точный термин через find=", d.Pages, textLen))
		} else if sum, serr := summariseDocument(ctx, cfg, opts.Question, hitsText(d.Hits)); serr == nil {
			d.Summary = sum
		}

	case huge:
		d.Notes = append(d.Notes, fmt.Sprintf(
			"документ большой (%d стр., %d символов) — целиком не пересказываю: это заняло бы часы "+
				"и всё равно вышло бы поверхностно. Спроси конкретное или передай find=«термин» — "+
				"найду нужные места с номерами страниц. Полный текст уже лежит файлом.", d.Pages, textLen))
		if d.Language != "" && d.Language != "русский" {
			d.Notes = append(d.Notes, "язык документа — "+d.Language+
				": ищи термины НА ЭТОМ ЯЗЫКЕ (fuse, relay, headlight), русские слова в нём не встретятся")
		}

	case textLen > docInlineChars:
		note("документ длинный — сворачиваю в выжимку")
		sum, serr := summariseDocument(ctx, cfg, opts.Question, d.Text)
		if serr != nil {
			d.Notes = append(d.Notes, "выжимку сделать не вышло ("+serr.Error()+") — ниже начало текста, полный в файле")
			d.Text = capAgentText(d.Text, docInlineChars)
		} else {
			d.Summary = sum
		}
	}
	if huge {
		d.Text = capAgentText(d.Text, docInlineChars) // в контекст уходит начало, остальное в файле
	}
	return d, nil
}

// summariseDocument сворачивает длинный документ. Роль отличается от расшифровки видео:
// в документе ценны реквизиты, а не «о чём говорят».
func summariseDocument(ctx context.Context, cfg Config, question, text string) (string, error) {
	sys := "Ты сжимаешь текст документа для самого себя — это рабочая выжимка, а не пересказ человеку.\n" +
		"Сохрани: тип документа, стороны и их реквизиты, даты, номера, суммы, сроки, предмет, " +
		"обязанности и ответственность, подписи и печати, если о них сказано.\n" +
		"Числа, даты и имена переноси ДОСЛОВНО, не округляй и не исправляй.\n" +
		"Если текст распознан с плохого скана и место нечитаемо — так и пиши, не додумывай.\n" +
		"Пиши по-русски, плотными пунктами, без вступления."
	if q := strings.TrimSpace(question); q != "" {
		sys += "\nОсобое внимание тому, что относится к вопросу: «" + capAgentText(q, 300) + "»."
	}
	return mapReduceSummary(ctx, cfg, sys, text)
}

// docCachePath — ИМЯ ФАЙЛА ТЕКСТА, ОДНО И ТО ЖЕ для одного и того же PDF.
//
// Имя выводится из пути, размера и времени изменения исходника, поэтому повторный разбор
// находит готовый текст вместо того, чтобы извлекать заново. Без этого один живой прогон по
// руководству BMW оставил на диске ШЕСТНАДЦАТЬ копий текста по 3.6 МБ: модель вызывала
// read_document раз за разом, и каждый вызов заново перемалывал 577 страниц по три секунды.
func docCachePath(src string) string {
	stem := safeFileStem(stripStampPrefix(strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))))
	key := strings.ToLower(src)
	if st, err := os.Stat(src); err == nil {
		key = fmt.Sprintf("%s|%d|%d", key, st.Size(), st.ModTime().UnixNano())
	}
	sum := sha1.Sum([]byte(key))
	return filepath.Join(mediaWorkDir, fmt.Sprintf("%s-%x.txt", stem, sum[:5]))
}

const docCacheHeader = "Источник: "

// loadDocCache восстанавливает страницы из ранее сохранённого текста.
func loadDocCache(path string) ([]docPage, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	body := string(raw)
	if i := strings.Index(body, "\n\n"); i >= 0 && strings.HasPrefix(body, docCacheHeader) {
		body = body[i+2:]
	}
	var pages []docPage
	for _, chunk := range strings.Split(body, "\n=== Страница ") {
		chunk = strings.TrimPrefix(chunk, "=== Страница ")
		i := strings.Index(chunk, " ===\n")
		if i <= 0 {
			continue
		}
		num, cerr := strconv.Atoi(strings.TrimSpace(chunk[:i]))
		if cerr != nil {
			continue
		}
		pages = append(pages, docPage{
			Num:    num,
			Text:   strings.TrimSpace(chunk[i+len(" ===\n"):]),
			Source: "текстовый слой",
		})
	}
	return pages, len(pages) > 0
}

// saveDocumentText кладёт полный текст рядом с артефактами — в контекст он не влезает,
// но нужен целиком и человеку, и модели через read_file.
func saveDocumentText(src, text string) (string, error) {
	if err := os.MkdirAll(mediaWorkDir, 0o755); err != nil {
		return "", err
	}
	full := docCachePath(src)
	if err := os.WriteFile(full, []byte(docCacheHeader+src+"\n\n"+text), 0o644); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(full)
	if err != nil {
		return full, nil
	}
	return abs, nil
}

// docLanguage определяет, на каком языке документ. Нужно не для красоты: на руководстве BMW
// (английский) вопрос был задан по-русски, поиск по словам вопроса не нашёл ничего — и модель
// потратила тринадцать шагов, не догадавшись искать английские термины.
func docLanguage(text string) string {
	var cyr, lat int
	for i, r := range text {
		if i > 200_000 {
			break // хватает выборки
		}
		switch {
		case r >= 'а' && r <= 'я', r >= 'А' && r <= 'Я', r == 'ё', r == 'Ё':
			cyr++
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			lat++
		}
	}
	total := cyr + lat
	if total < 200 {
		return ""
	}
	switch {
	case cyr*100 >= total*80:
		return "русский"
	case lat*100 >= total*80:
		return "английский (или другой на латинице)"
	default:
		return "смешанный"
	}
}

// ===== приём из чата =====
//
// Документ, присланный в чат, разбирается ДО диспетчера и превращается в текст — ровно как
// картинка (§4) и видео (§21). Поэтому «что это за документ и чем он мне грозит» работает
// само собой: к моменту выбора инструментов задача уже текстовая.

// handleTelegramPDF разбирает присланный PDF и передаёт РЕЗУЛЬТАТ дальше обычным путём.
func handleTelegramPDF(cfg Config, botAPI string, allow map[int64]bool, chatID int64, doc *tgDocument, caption string) {
	if err := documentReady(cfg); err != nil {
		sendTelegramMessage(botAPI, chatID, "❌ "+err.Error())
		return
	}
	if doc.FileSize > telegramFileLimit {
		sendTelegramMessage(botAPI, chatID, videoTooBigNote(doc.FileSize))
		return
	}
	stop := startTyping(botAPI, chatID)
	defer stop()
	sendTelegramMessage(botAPI, chatID, "📄 Взял документ — читаю. Если это скан, распознавание займёт время.")

	filePath, err := getTelegramFilePath(botAPI, doc.FileID)
	if err != nil {
		sendTelegramMessage(botAPI, chatID, "❌ Не смог получить файл из Telegram: "+err.Error())
		return
	}
	if err := os.MkdirAll(mediaWorkDir, 0o755); err != nil {
		sendTelegramMessage(botAPI, chatID, "❌ "+err.Error())
		return
	}
	stem := safeFileStem(stripStampPrefix(strings.TrimSuffix(doc.FileName, filepath.Ext(doc.FileName))))
	dst := filepath.Join(mediaWorkDir, fmt.Sprintf("%s-%s.pdf", time.Now().Format("20060102-150405"), stem))
	if _, err := downloadTelegramFileTo(cfg.TelegramToken, filePath, dst); err != nil {
		sendTelegramMessage(botAPI, chatID, "❌ Не смог скачать документ: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), docIngestTimeout)
	defer cancel()
	notice := throttledNotice(func(s string) { sendTelegramMessage(botAPI, chatID, s) }, 45*time.Second)
	d, err := readDocument(ctx, cfg, docReadOpts{
		Path: dst, Question: caption, ChatID: chatID, Progress: notice,
	})
	stop()
	if err != nil {
		sendTelegramMessage(botAPI, chatID, "❌ Разбор документа не вышел: "+err.Error())
		return
	}
	log.Printf("[DOC] telegram %d: %d стр (слой %d, OCR %d, зрение %d), текста %d символов",
		chatID, d.Pages, d.TextPages, d.OCRPages, d.EyePages, len([]rune(d.Text)))

	sendTelegramMessage(botAPI, chatID, d.Render())
	if d.TextPath != "" {
		if serr := sendTelegramDocumentFile(botAPI, chatID, d.TextPath, "📄 Распознанный текст"); serr != nil {
			log.Printf("[DOC] текст файлом не отправил: %v", serr)
		}
	}
	if strings.TrimSpace(caption) == "" || allow == nil {
		recordHistory(chatID, "[документ] "+caption, capAgentText(d.Render(), ingestDigestChars))
		return
	}
	runTelegramAgent(cfg, botAPI, allow, chatID,
		strings.TrimSpace(caption)+"\n\n[Разбор приложенного документа]\n"+d.Render())
}

// handleWebPDFTurn — веб-близнец: тот же разбор, та же маршрутизация результата.
func handleWebPDFTurn(w http.ResponseWriter, cfg Config, caption, name string, src io.Reader) {
	if err := documentReady(cfg); err != nil {
		webChatReply(w, "❌ "+err.Error())
		return
	}
	if err := os.MkdirAll(mediaWorkDir, 0o755); err != nil {
		webChatReply(w, "❌ "+err.Error())
		return
	}
	stem := safeFileStem(stripStampPrefix(strings.TrimSuffix(name, filepath.Ext(name))))
	dst := filepath.Join(mediaWorkDir, fmt.Sprintf("%s-%s.pdf", time.Now().Format("20060102-150405"), stem))
	f, err := os.Create(dst)
	if err != nil {
		webChatReply(w, "❌ Не смог сохранить документ: "+err.Error())
		return
	}
	size, cerr := io.Copy(f, io.LimitReader(src, webVideoUploadLimit))
	f.Close()
	if cerr != nil || size == 0 {
		os.Remove(dst)
		webChatReply(w, "❌ Не смог прочитать загруженный файл")
		return
	}

	emit := ndjsonEmitter(w)
	emit(map[string]any{"status": "📄 Читаю документ " + name + " (" + humanBytes(size) + ")"})
	ctx, cancel := context.WithTimeout(context.Background(), docIngestTimeout)
	defer cancel()
	d, derr := readDocument(ctx, cfg, docReadOpts{
		Path: dst, Question: caption, ChatID: webChatID,
		Progress: func(s string) { emit(map[string]any{"status": "📄 " + s}) },
	})
	if derr != nil {
		emit(map[string]any{"done": true, "text": "❌ Разбор документа не вышел: " + derr.Error()})
		return
	}
	log.Printf("[WEB] документ %s: %d стр (слой %d, OCR %d, зрение %d)",
		name, d.Pages, d.TextPages, d.OCRPages, d.EyePages)

	if strings.TrimSpace(caption) == "" {
		recordHistory(webChatID, "[документ "+name+"]", capAgentText(d.Render(), ingestDigestChars))
		var files []string
		if d.TextPath != "" {
			files = append(files, d.TextPath)
		}
		emit(map[string]any{"done": true, "text": d.Render(), "files": webArtifactLinks(files)})
		return
	}
	streamWebAgent(w, cfg, strings.TrimSpace(caption)+"\n\n[Разбор приложенного документа]\n"+d.Render(),
		"[документ "+name+"] "+caption, nil, "")
}

// ===== инструмент =====

// docToolSpecs — половина пака `files`, которой нужны poppler, tesseract, зрение и модель.
func docToolSpecs() []browser.ToolSpec {
	return []browser.ToolSpec{
		spec("read_document",
			"PDF в текст: текстовый слой или распознавание скана.",
			objSchema(map[string]any{
				"path":     strSchema(""),
				"pages":    strSchema("2-10; пусто = все"),
				"question": strSchema("что нужно узнать"),
				"find":     strSchema("искать термин по документу"),
				"mode":     strSchema("auto|text|ocr|vision"),
			}, "path")),
	}
}

// dispatchDocumentTool исполняет инструмент пака. ok=false — «не мой».
func dispatchDocumentTool(t *Task, cfg Config, name string, args map[string]any) (ToolResult, bool) {
	if name != "read_document" {
		return ToolResult{}, false
	}
	path := argString(args, "path")
	if path == "" {
		return failResult("нужен path — путь к PDF", nil), true
	}
	from, to := parsePageRange(argString(args, "pages"))
	d, err := readDocument(t.Context(), cfg, docReadOpts{
		Path:     path,
		From:     from,
		To:       to,
		Question: argString(args, "question"),
		Find:     argString(args, "find"),
		Mode:     argString(args, "mode"),
		ChatID:   t.ChatID,
	})
	if err != nil {
		return failResult("документ прочитать не вышло: "+err.Error(), err), true
	}
	var artifacts []string
	if d.TextPath != "" {
		artifacts = append(artifacts, d.TextPath)
	}
	return okResult(d.Render(), artifacts), true
}

// parsePageRange понимает «5», «2-10», «-20», «7-» — так пишет и человек, и модель.
func parsePageRange(s string) (from, to int) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "–", "-"))
	if s == "" {
		return 0, 0
	}
	if i := strings.Index(s, "-"); i >= 0 {
		from, _ = strconv.Atoi(strings.TrimSpace(s[:i]))
		to, _ = strconv.Atoi(strings.TrimSpace(s[i+1:]))
		return from, to
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, 0
	}
	return n, n
}
