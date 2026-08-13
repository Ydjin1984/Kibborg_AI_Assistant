package main

// Тесты «глаз на рабочем столе»: пересчёт координат снимка в координаты экрана и уменьшение
// картинки перед отправкой зрению.

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// Арифметику пересчёта делает КОД, а не модель: у снимка окна начало координат не в нуле, а у
// виртуального рабочего стола с монитором слева от основного X вообще отрицательный. Ошибись
// здесь — и клик уедет в другое окно, причём молча.
func TestParseFindingsMapsToScreenCoordinates(t *testing.T) {
	desc := "ВИЖУ: окно Telegram.\n" +
		"НАЙДЕНО: кнопка «Сжать контекст» @ 500,500\n" +
		"НАЙДЕНО: чат Kibborg @ 0,0\n" +
		"НАЙДЕНО: угол @ 1000,1000\n"

	// Окно, сдвинутое вправо и вниз.
	got := parseFindings(desc, screenRect{X: 100, Y: 200, W: 1000, H: 800}, 500, 400)
	if len(got) != 3 {
		t.Fatalf("разобрано %d находок, ждали 3: %+v", len(got), got)
	}
	if got[0].X != 600 || got[0].Y != 600 {
		t.Errorf("центр окна дал (%d,%d), ждали (600,600)", got[0].X, got[0].Y)
	}
	if got[1].X != 100 || got[1].Y != 200 {
		t.Errorf("левый верхний угол дал (%d,%d), ждали начало окна (100,200)", got[1].X, got[1].Y)
	}
	if got[2].X != 1100 || got[2].Y != 1000 {
		t.Errorf("правый нижний угол дал (%d,%d), ждали (1100,1000)", got[2].X, got[2].Y)
	}
	if !strings.Contains(got[0].Label, "Сжать") {
		t.Errorf("подпись находки потерялась: %q", got[0].Label)
	}

	// Монитор слева от основного: отрицательный X обязан пережить пересчёт.
	left := parseFindings("НАЙДЕНО: край @ 0,0", screenRect{X: -1920, Y: 0, W: 1920, H: 1080}, 0, 0)
	if len(left) != 1 || left[0].X != -1920 {
		t.Errorf("монитор слева от основного посчитан неверно: %+v", left)
	}
}

// Выдуманные координаты не должны становиться кликом.
func TestParseFindingsRejectsGarbage(t *testing.T) {
	rect := screenRect{X: 0, Y: 0, W: 1000, H: 1000}
	for _, bad := range []string{
		"НЕ НАЙДЕНО: кнопка Стоп",
		"НАЙДЕНО: кнопка @ 5000,20",  // вне сетки 0..1000
		"НАЙДЕНО: кнопка @ 10,99999", // вне сетки
		"кнопка находится примерно посередине экрана",
	} {
		if got := parseFindings(bad, rect, 0, 0); len(got) != 0 {
			t.Errorf("из %q не должно было получиться координат, вышло %+v", bad, got)
		}
	}
	// Без размеров области пересчитывать не во что.
	if got := parseFindings("НАЙДЕНО: x @ 1,1", screenRect{}, 0, 0); len(got) != 0 {
		t.Error("пустой прямоугольник не может давать координаты экрана")
	}
}

// Ответ инструмента обязан честно предупреждать, что это оценка зрения, а не измерение.
func TestRenderFindingsWarnsAboutPrecision(t *testing.T) {
	out := renderFindings([]screenFinding{{Label: "кнопка Стоп", X: 10, Y: 20}})
	for _, want := range []string{"x=10", "y=20", "mouse_action", "capture_screen"} {
		if !strings.Contains(out, want) {
			t.Errorf("в ответе нет %q: %s", want, out)
		}
	}
	if !strings.Contains(out, "оценка зрения") {
		t.Error("надо прямо сказать, что координаты приблизительные — иначе модель будет им верить")
	}
	if renderFindings(nil) != "" {
		t.Error("без находок блока координат быть не должно")
	}
}

// Снимок 3440×1440 в проектор влезает, но стоит тысячи image-токенов — то есть съедает то
// самое окно контекста, ради которого сделан /compact. Проверяем, что ужимаем и не портим.
func TestShrinkPNGKeepsAspectAndContent(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3440, 1440))
	// Левая половина красная, правая синяя — усреднение по блоку обязано это сохранить.
	for y := 0; y < 1440; y++ {
		for x := 0; x < 3440; x++ {
			c := color.RGBA{R: 220, A: 255}
			if x >= 1720 {
				c = color.RGBA{B: 220, A: 255}
			}
			src.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	small, w, h, err := shrinkPNG(buf.Bytes(), visionMaxSide)
	if err != nil {
		t.Fatal(err)
	}
	if w != visionMaxSide {
		t.Errorf("ширина после уменьшения = %d, ждали %d", w, visionMaxSide)
	}
	if want := 1440 * visionMaxSide / 3440; h != want {
		t.Errorf("пропорции не сохранены: %dx%d, ждали высоту %d", w, h, want)
	}
	if len(small) >= len(buf.Bytes()) {
		t.Error("уменьшенный снимок должен весить меньше исходного")
	}
	out, err := png.Decode(bytes.NewReader(small))
	if err != nil {
		t.Fatal(err)
	}
	lr, _, lb, _ := out.At(w/4, h/2).RGBA()
	rr, _, rb, _ := out.At(w*3/4, h/2).RGBA()
	if lr <= lb {
		t.Error("левая половина должна была остаться красной")
	}
	if rb <= rr {
		t.Error("правая половина должна была остаться синей")
	}

	// Маленькая картинка не трогается вовсе — лишняя перекодировка только портит текст.
	tiny := image.NewRGBA(image.Rect(0, 0, 100, 50))
	var tb bytes.Buffer
	_ = png.Encode(&tb, tiny)
	same, tw, th, err := shrinkPNG(tb.Bytes(), visionMaxSide)
	if err != nil || tw != 100 || th != 50 || !bytes.Equal(same, tb.Bytes()) {
		t.Errorf("картинка меньше порога должна возвращаться как есть (%dx%d, err=%v)", tw, th, err)
	}
}
