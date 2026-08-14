package main

import (
	"strings"
	"testing"
)

func TestSpellRuInt(t *testing.T) {
	cases := map[int64]string{
		0:     "ноль",
		8:     "восемь",
		21:    "двадцать один",
		68:    "шестьдесят восемь",
		100:   "сто",
		1000:  "одна тысяча",
		2000:  "две тысячи",
		5000:  "пять тысяч",
		68000: "шестьдесят восемь тысяч",
		68432: "шестьдесят восемь тысяч четыреста тридцать два",
	}
	for n, want := range cases {
		if got := spellRuInt(n); got != want {
			t.Errorf("%d: got %q want %q", n, got, want)
		}
	}
}

func TestAnnotateNumbersAddsWords(t *testing.T) {
	got := annotateNumbers("Курс биткоина 68432 $ на Binance.")
	if !strings.Contains(got, "68432") {
		t.Fatalf("цифры пропали: %q", got)
	}
	if !strings.Contains(got, "шестьдесят восемь тысяч четыреста тридцать два") {
		t.Fatalf("нет прописи: %q", got)
	}
	if !strings.Contains(got, "доллар") {
		t.Fatalf("нет валюты: %q", got)
	}
}

func TestAnnotateSkipsURLAndYear(t *testing.T) {
	in := "Смотри https://example.com/btc/68432 и год 2026."
	got := annotateNumbers(in)
	if strings.Contains(got, "example.com/btc/68432 (") {
		t.Fatalf("число в URL тронуто: %q", got)
	}
	if strings.Contains(got, "2026 (") {
		t.Fatalf("год не должен дублироваться: %q", got)
	}
}

func TestAnnotateSkipsAlreadySpelled(t *testing.T) {
	in := "Цена 68000 (шестьдесят восемь тысяч) долларов."
	got := annotateNumbers(in)
	if strings.Count(got, "шестьдесят восемь тысяч") != 1 {
		t.Fatalf("удвоили пропись: %q", got)
	}
}

func TestSpeakDigitsReplacesFigures(t *testing.T) {
	got := speechText("Курс биткоина 68 000 $ за один биткоин.")
	if strings.ContainsAny(got, "0123456789") {
		t.Fatalf("цифры ушли в озвучку: %q", got)
	}
	if !strings.Contains(got, "шестьдесят восемь тысяч") {
		t.Fatalf("нет прописи в озвучке: %q", got)
	}
	if !strings.Contains(got, "доллар") {
		t.Fatalf("валюта не проговорена: %q", got)
	}
}

func TestSpeakDigitsNoDoubleAfterAnnotate(t *testing.T) {
	text := annotateNumbers("Биткоин 68000 $.")
	got := speechText(text)
	if n := strings.Count(got, "шестьдесят восемь тысяч"); n != 1 {
		t.Fatalf("пропись %d раз: %q", n, got)
	}
}

func TestParseGroupedAndDecimal(t *testing.T) {
	n, frac, ok := parseNumToken("68 000")
	if !ok || n != 68000 || frac != "" {
		t.Fatalf("68 000 → %d %q %v", n, frac, ok)
	}
	n, frac, ok = parseNumToken("68,432")
	if !ok || n != 68432 || frac != "" {
		t.Fatalf("68,432 → %d %q %v", n, frac, ok)
	}
	n, frac, ok = parseNumToken("3.6%")
	if !ok || n != 3 || frac != "6" {
		t.Fatalf("3.6%% → %d %q %v", n, frac, ok)
	}
	n, frac, ok = parseNumToken("1 883,3 $")
	if !ok || n != 1883 || frac != "3" {
		t.Fatalf("1 883,3 $ → %d %q %v", n, frac, ok)
	}
	n, frac, ok = parseNumToken("1,003 $")
	if !ok || n != 1 || frac != "003" {
		t.Fatalf("1,003 $ → %d %q %v — это доллар, не тысяча", n, frac, ok)
	}
}

func TestAnnotateDoesNotDoubleExistingWords(t *testing.T) {
	in := "Bitcoin — 63 087 $ (шестьдесят три тысячи восемьдесят семь долларов). Изменение -0,03%."
	got := annotateNumbers(in)
	if n := strings.Count(got, "шестьдесят три тысячи восемьдесят семь"); n != 1 {
		t.Fatalf("пропись %d раз: %q", n, got)
	}
}

func TestCollapseAdjacentNumberParens(t *testing.T) {
	in := "63 087 $ (шестьдесят три тысячи восемьдесят семь долларов) (шестьдесят три тысячи восемьдесят семь долларов)."
	got := collapseAdjacentParens(in)
	if strings.Count(got, "шестьдесят три тысячи") != 1 {
		t.Fatalf("двойные скобки: %q", got)
	}
}

func TestSpeakMoneyOnceWithCents(t *testing.T) {
	got := speechText("Ethereum — 1 883,3 $. Источник: Интерфакс")
	if n := strings.Count(got, "восемьсот восемьдесят три"); n != 1 {
		t.Fatalf("ETH пропись %d раз: %q", n, got)
	}
	if strings.Contains(got, "восемнадцать тысяч") {
		t.Fatalf("1 883,3 прочитали как 18833: %q", got)
	}
	if !strings.Contains(got, "цент") {
		t.Fatalf("нет центов: %q", got)
	}
	if strings.Contains(got, "целых") {
		t.Fatalf("деньги не должны звучать как «целых сотых»: %q", got)
	}
}

func TestSpeakXRPNotAThousand(t *testing.T) {
	got := speechText("Курс рипла 1,003 $ за монету.")
	if strings.Contains(got, "тысяча") {
		t.Fatalf("1,003 $ стали тысячей: %q", got)
	}
	if !strings.Contains(got, "один доллар") {
		t.Fatalf("нет «один доллар»: %q", got)
	}
}

func TestSpeakSourcesNotURLLetters(t *testing.T) {
	got := speechText("Факт. Источник: [Интерфакс](https://www.interfax.ru/digital/1045123)")
	if strings.Contains(got, "https") || strings.Contains(got, "interfax.ru") || strings.Contains(got, "1045123") {
		t.Fatalf("URL ушёл в озвучку: %q", got)
	}
	if !strings.Contains(got, "Интерфакс") {
		t.Fatalf("нет имени источника: %q", got)
	}
}

func TestSpeakTripleNumberCollapsed(t *testing.T) {
	raw := "Bitcoin — 63 087 $ (шестьдесят три тысячи восемьдесят семь долларов) (шестьдесят три тысячи восемьдесят семь долларов)."
	got := speechText(raw)
	if n := strings.Count(got, "шестьдесят три тысячи восемьдесят семь"); n != 1 {
		t.Fatalf("число %d раз: %q", n, got)
	}
}

func TestSpeakDigitsNumberInsideAlreadySpelledParens(t *testing.T) {
	// Число внутри скобок прописи раньше роняло TTS: last уезжал за скобку,
	// а FindAllStringIndex всё ещё отдавал внутреннее «50» с lo < last.
	in := "Цена 100 $ (около 50 долларов) и ещё 200."
	got := speakDigits(in, "ru")
	if strings.ContainsAny(got, "0123456789") {
		t.Fatalf("цифры ушли в озвучку: %q", got)
	}
	if !strings.Contains(got, "сто") {
		t.Fatalf("нет 100: %q", got)
	}
	if !strings.Contains(got, "двести") {
		t.Fatalf("нет 200: %q", got)
	}
}

func TestSpeechTextLongReplyWithNestedFigures(t *testing.T) {
	// Реалистичный ответ панели: пропись + ещё цифры в скобках и дальше по тексту.
	var b strings.Builder
	b.WriteString("Bitcoin — 63 087 $ (шестьдесят три тысячи восемьдесят семь долларов, см. уровень 65700). ")
	for i := 0; i < 20; i++ {
		b.WriteString("ATR 2340 (две тысячи триста сорок, стоп 15%). ")
	}
	b.WriteString("Цель 1: 68000 (шестьдесят восемь тысяч).")
	got := speechText(b.String())
	if strings.ContainsAny(got, "0123456789") {
		t.Fatalf("цифры ушли в озвучку: %q", got)
	}
}
