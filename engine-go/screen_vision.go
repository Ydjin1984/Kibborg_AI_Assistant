package main

// Глаза на рабочем столе (ТЗ §7 + §5, пак `system`).
//
// Руки у агента появились раньше глаз, и это была дыра, а не мелочь: `capture_screen` отдаёт
// МОДЕЛИ путь к файлу, а пиксели уходят человеку. Мышь при этом умеет кликать только по
// координатам. То есть агент мог нажать куда угодно — и не мог узнать, куда именно надо.
//
// Здесь снимок прогоняется через то же зрение, которым разбираются присланные пользователем
// картинки, и возвращается модели ТЕКСТОМ. Архитектурно это единственный допустимый способ:
// §7 запрещает смешивать image_url со схемами инструментов в одном запросе к llama-server —
// поэтому зрение работает отдельным вызовом БЕЗ инструментов, а в цикл возвращается описание.
//
// Координаты зрение даёт в нормированной сетке 0..1000, а перевод в пиксели экрана делает
// код: это ровно та арифметика, которую модель ошибается делать в уме, а проверить её потом
// нельзя. Точность зрения при этом остаётся зрением — см. предупреждение в ответе инструмента.

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"regexp"
	"strconv"
	"strings"
)

// visionMaxSide — до какой стороны ужимается снимок перед отправкой зрению.
//
// 3440×1440 в проектор влезает, но стоит тысячи image-токенов и съедает окно контекста, ради
// которого мы только что делали /compact. 1200 — компромисс: элементы интерфейса (кнопка
// Telegram ~200×60 реальных пикселей) остаются различимыми, а картинка стоит ~400 токенов.
const visionMaxSide = 1200

// screenLookPrompt просит зрение сделать ровно две вещи: описать и показать пальцем.
const screenLookPrompt = `Это снимок экрана компьютера. Ответь по-русски и строго по делу.

1. Сначала строкой «ВИЖУ:» — что на экране: какие окна, какое активное, что в нём происходит.
2. Если в вопросе просят найти элемент (кнопку, поле, вкладку, чат) — найди его и дай координаты
   ЦЕНТРА элемента в нормированной сетке, где левый верхний угол снимка = 0,0, а правый нижний = 1000,1000.
   Формат строго такой, отдельной строкой:
   НАЙДЕНО: <что это> @ <x>,<y>
   Если элементов несколько — по одной строке НАЙДЕНО на каждый.
   Если элемента на экране НЕТ — напиши одной строкой «НЕ НАЙДЕНО: <что искали>» и не выдумывай координаты.

Ничего не додумывай: описывай только то, что действительно видно.

Вопрос: `

// screenFinding is one element the vision model claims to have located.
type screenFinding struct {
	Label string
	X, Y  int // абсолютные координаты экрана
}

// lookAtScreen runs vision over a screenshot and returns the description plus located points.
//
// rect — где снимок находится НА ЭКРАНЕ (у снимка окна начало координат не в нуле, а у
// виртуального рабочего стола с монитором слева от основного X вообще отрицательный).
func lookAtScreen(cfg Config, chatID int64, shot []byte, rect screenRect, question string) (string, []screenFinding, error) {
	if !brainHasVision(cfg.BrainPort) {
		return "", nil, fmt.Errorf("зрение выключено: в settings.ini не задан MMPROJ_PATH, " +
			"поэтому смотреть на снимок я не могу — только сохранить его тебе")
	}
	small, sw, sh, err := shrinkPNG(shot, visionMaxSide)
	if err != nil {
		return "", nil, err
	}
	q := strings.TrimSpace(question)
	if q == "" {
		q = "что сейчас на экране?"
	}
	// sysPrompt пустой → обычная роль зрения; память сюда не подмешиваем: описание экрана
	// должно зависеть от экрана, а не от того, что мы обсуждали час назад.
	desc, err := describeImageBytes(cfg, chatID, screenLookPrompt+q,
		"Ты смотришь на снимок экрана и отвечаешь только тем, что видно. Не выдумывай.",
		"image/png", small)
	if err != nil {
		return "", nil, err
	}
	return desc, parseFindings(desc, rect, sw, sh), nil
}

// findingRe разбирает строку «НАЙДЕНО: кнопка Сжать @ 412,733».
var findingRe = regexp.MustCompile(`(?i)НАЙДЕНО:\s*(.+?)\s*@\s*(\d{1,4})\s*[,;\s]\s*(\d{1,4})`)

// parseFindings converts the model's normalized points into absolute screen pixels.
func parseFindings(desc string, rect screenRect, sw, sh int) []screenFinding {
	if rect.W <= 0 || rect.H <= 0 {
		return nil
	}
	var out []screenFinding
	for _, m := range findingRe.FindAllStringSubmatch(desc, -1) {
		nx, err1 := strconv.Atoi(m[2])
		ny, err2 := strconv.Atoi(m[3])
		if err1 != nil || err2 != nil {
			continue
		}
		if nx < 0 || nx > 1000 || ny < 0 || ny > 1000 {
			continue // не сетка 0..1000 — доверять такому нельзя
		}
		out = append(out, screenFinding{
			Label: strings.TrimSpace(m[1]),
			X:     rect.X + nx*rect.W/1000,
			Y:     rect.Y + ny*rect.H/1000,
		})
	}
	_, _ = sw, sh
	return out
}

// renderFindings turns located points into the lines the model will act on.
func renderFindings(fs []screenFinding) string {
	if len(fs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n📍 Координаты ЭКРАНА (пересчитаны из снимка, передавай прямо в mouse_action):\n")
	for _, f := range fs {
		fmt.Fprintf(&b, "- %s → x=%d, y=%d\n", f.Label, f.X, f.Y)
	}
	b.WriteString("⚠️ Это оценка зрения, а не точное измерение: после клика ОБЯЗАТЕЛЬНО сделай " +
		"capture_screen с look= и убедись, что нажалось то, что нужно.")
	return b.String()
}

// ===== уменьшение снимка =====

// shrinkPNG scales a screenshot down so its longest side is at most maxSide, and re-encodes it.
//
// Усреднение по блоку, а не выбор одного пикселя: при уменьшении в три раза «ближайший
// сосед» рвёт тонкий шрифт интерфейса до нечитаемого, и зрение начинает угадывать текст
// кнопок вместо того, чтобы его читать.
func shrinkPNG(data []byte, maxSide int) ([]byte, int, int, error) {
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("не разобрал снимок: %w", err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxSide && h <= maxSide {
		return data, w, h, nil
	}
	scale := float64(maxSide) / float64(w)
	if h > w {
		scale = float64(maxSide) / float64(h)
	}
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0, y1 := y*h/nh, (y+1)*h/nh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < nw; x++ {
			x0, x1 := x*w/nw, (x+1)*w/nw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var sr, sg, sb uint32
			n := uint32(0)
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					r, g, bl, _ := src.At(b.Min.X+xx, b.Min.Y+yy).RGBA()
					sr += r >> 8
					sg += g >> 8
					sb += bl >> 8
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i] = uint8(sr / n)
			dst.Pix[i+1] = uint8(sg / n)
			dst.Pix[i+2] = uint8(sb / n)
			dst.Pix[i+3] = 255
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return nil, 0, 0, err
	}
	return out.Bytes(), nw, nh, nil
}
