package main

// Цифры в ответе и в озвучке. SuperTonic ломает «68432» в кашу, поэтому
// в текст кладём и цифры, и пропись, а в TTS уходят только слова.

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	reNumToken = regexp.MustCompile(`(?i)(?:[$€£]\s*)?(?:\d{1,3}(?:[ \x{00a0},]\d{3})+|\d+)(?:[.,]\d+)?(?:\s*(?:[$€£]|usd|usdt|eur|руб\.?|долл\.?))?|\d+\s*%`)
	reHTTPSpan = regexp.MustCompile(`https?://\S+`)
	reCodeTick = regexp.MustCompile("`[^`]+`")
	reMDLink   = regexp.MustCompile(`\[[^\]]*\]\([^)]+\)`)
	reAlready  = regexp.MustCompile(`(?i)^[\s$€£]*(?:\([^)]*[\p{L}][^)]*\)\s*)+`)
)

// annotateNumbers добавляет пропись к крупным числам в ответе человеку.
func annotateNumbers(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	var b strings.Builder
	inFence := false
	first := true
	for _, line := range strings.Split(text, "\n") {
		if !first {
			b.WriteByte('\n')
		}
		first = false
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") {
			inFence = !inFence
			b.WriteString(line)
			continue
		}
		if inFence {
			b.WriteString(line)
			continue
		}
		b.WriteString(collapseAdjacentParens(annotateLine(line, false, "ru")))
	}
	return b.String()
}

// speakDigits заменяет числа словами — то, что слышит TTS.
func speakDigits(text, lang string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	if lang == "" {
		lang = "ru"
	}
	out := annotateLine(text, true, lang)
	out = collapseDupParens(out)
	return strings.Join(strings.Fields(out), " ")
}

// collapseDupParens снимает «шестьдесят восемь (шестьдесят восемь)» после двойной прописи.
func collapseDupParens(s string) string {
	for i := 0; i < 4; i++ {
		next := collapseDupParensOnce(s)
		if next == s {
			return s
		}
		s = next
	}
	return s
}

func collapseDupParensOnce(s string) string {
	open := strings.IndexByte(s, '(')
	for open >= 0 {
		close := strings.IndexByte(s[open+1:], ')')
		if close < 0 {
			return s
		}
		close += open + 1
		inner := strings.TrimSpace(s[open+1 : close])
		if inner == "" {
			open = strings.IndexByte(s[open+1:], '(')
			if open >= 0 {
				open++
			}
			continue
		}
		before := strings.TrimSpace(s[:open])
		if strings.HasSuffix(strings.ToLower(before), strings.ToLower(inner)) {
			return strings.TrimSpace(s[:open]) + s[close+1:]
		}
		open = strings.IndexByte(s[close+1:], '(')
		if open >= 0 {
			open += close + 1
		}
	}
	return s
}

func annotateLine(line string, forSpeech bool, lang string) string {
	if line == "" {
		return line
	}
	skip := collectSpans(line, reHTTPSpan, reCodeTick, reMDLink)
	idxs := reNumToken.FindAllStringIndex(line, -1)
	if len(idxs) == 0 {
		return line
	}
	var b strings.Builder
	last := 0
	for _, idx := range idxs {
		lo, hi := idx[0], idx[1]
		if lo < last || overlaps(lo, hi, skip) {
			continue
		}
		raw := line[lo:hi]
		rest := line[hi:]
		if alreadySpelled(rest) {
			if forSpeech {
				b.WriteString(line[last:lo])
				if words := numberToWords(raw, lang); words != "" {
					b.WriteString(words)
				} else {
					b.WriteString(raw)
				}
				last = hi + skipSpelledParens(rest)
			}
			continue
		}
		n, frac, ok := parseNumToken(raw)
		if !ok {
			continue
		}
		if !forSpeech && !shouldAnnotate(n, frac, raw) {
			continue
		}
		words := numberToWords(raw, lang)
		if words == "" {
			continue
		}
		b.WriteString(line[last:lo])
		if forSpeech {
			b.WriteString(words)
		} else {
			b.WriteString(raw)
			b.WriteString(" (")
			b.WriteString(words)
			b.WriteByte(')')
		}
		last = hi
	}
	b.WriteString(line[last:])
	return b.String()
}

func shouldAnnotate(n int64, frac, raw string) bool {
	if n < 0 {
		n = -n
	}
	if strings.ContainsAny(raw, "$€£%") || hasCurrencyWord(raw) {
		return true
	}
	if strings.ContainsAny(raw, " \u00a0,") && n >= 1000 {
		return true
	}
	if frac != "" && n >= 10 {
		return true
	}
	if n >= 1900 && n <= 2100 && frac == "" && !strings.ContainsAny(raw, " \u00a0,") {
		return false
	}
	return n >= 100
}

func alreadySpelled(rest string) bool {
	return reAlready.MatchString(rest)
}

// skipSpelledParens — длина хвоста « $ (пропись) (пропись)», чтобы TTS не читал её снова.
func skipSpelledParens(rest string) int {
	loc := reAlready.FindStringIndex(rest)
	if loc == nil {
		return 0
	}
	return loc[1]
}

func collapseAdjacentParens(s string) string {
	for i := 0; i < 4; i++ {
		next := collapseAdjacentParensOnce(s)
		if next == s {
			return s
		}
		s = next
	}
	return s
}

func collapseAdjacentParensOnce(s string) string {
	open := strings.IndexByte(s, '(')
	for open >= 0 {
		close := strings.IndexByte(s[open+1:], ')')
		if close < 0 {
			return s
		}
		close += open + 1
		inner := strings.ToLower(strings.TrimSpace(s[open+1 : close]))
		rest := strings.TrimLeft(s[close+1:], " \t")
		if strings.HasPrefix(rest, "(") {
			end := strings.IndexByte(rest, ')')
			if end > 0 {
				inner2 := strings.ToLower(strings.TrimSpace(rest[1:end]))
				if inner != "" && inner == inner2 {
					cut := len(s) - len(strings.TrimLeft(s[close+1:], " \t")) + end + 1
					return s[:close+1] + s[cut:]
				}
			}
		}
		open = strings.IndexByte(s[close+1:], '(')
		if open >= 0 {
			open += close + 1
		}
	}
	return s
}

func collectSpans(s string, res ...*regexp.Regexp) [][2]int {
	var out [][2]int
	for _, re := range res {
		for _, idx := range re.FindAllStringIndex(s, -1) {
			out = append(out, [2]int{idx[0], idx[1]})
		}
	}
	return out
}

func overlaps(lo, hi int, spans [][2]int) bool {
	for _, s := range spans {
		if lo < s[1] && hi > s[0] {
			return true
		}
	}
	return false
}

func hasCurrencyWord(s string) bool {
	low := strings.ToLower(s)
	for _, w := range []string{"usd", "usdt", "eur", "руб", "долл"} {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

func numberToWords(raw, lang string) string {
	n, frac, ok := parseNumToken(raw)
	if !ok {
		return ""
	}
	ru := lang != "en"
	if ru && isMoneyToken(raw) {
		return spellRuMoney(n, frac, raw)
	}
	var b strings.Builder
	if ru {
		b.WriteString(spellRuInt(n))
		if frac != "" {
			b.WriteString(" целых ")
			b.WriteString(spellRuFrac(frac))
		}
	} else {
		b.WriteString(spellEnInt(n))
		if frac != "" {
			b.WriteString(" point ")
			b.WriteString(spellEnDigits(frac))
		}
	}
	if u := unitWord(raw, ru, n); u != "" {
		b.WriteByte(' ')
		b.WriteString(u)
	}
	return b.String()
}

func isMoneyToken(raw string) bool {
	low := strings.ToLower(raw)
	return strings.ContainsAny(raw, "$€£") ||
		strings.Contains(low, "usd") || strings.Contains(low, "usdt") ||
		strings.Contains(low, "eur") || strings.Contains(low, "долл") ||
		strings.Contains(low, "руб")
}

func spellRuMoney(n int64, frac, raw string) string {
	unitOne, unitFew, unitMany := "доллар", "доллара", "долларов"
	centOne, centFew, centMany := "цент", "цента", "центов"
	low := strings.ToLower(raw)
	switch {
	case strings.Contains(low, "руб"):
		unitOne, unitFew, unitMany = "рубль", "рубля", "рублей"
		centOne, centFew, centMany = "копейка", "копейки", "копеек"
	case strings.Contains(raw, "€") || strings.Contains(low, "eur"):
		unitOne, unitFew, unitMany = "евро", "евро", "евро"
		centOne, centFew, centMany = "цент", "цента", "центов"
	}
	intWords := spellRuInt(n)
	if strings.Contains(low, "руб") && (n%10 == 1 && n%100 != 11) {
		intWords = spellRuIntFem(n)
	}
	out := strings.TrimSpace(intWords + " " + ruNoun64(n, unitOne, unitFew, unitMany))
	cents := fracToCents(frac)
	if cents > 0 {
		cw := spellRuInt(int64(cents))
		if centOne == "копейка" {
			cw = spellRuIntFem(int64(cents))
		}
		out += " " + cw + " " + ruNoun(cents, centOne, centFew, centMany)
	}
	return out
}

func fracToCents(frac string) int {
	frac = digitsOnly(frac)
	if frac == "" {
		return 0
	}
	switch len(frac) {
	case 1:
		n, _ := strconv.Atoi(frac)
		return n * 10
	case 2:
		n, _ := strconv.Atoi(frac)
		return n
	default:
		// 1,003 $ → 0 центов после округления до сотых, не «тысяча три доллара».
		n, err := strconv.Atoi(frac[:2])
		if err != nil {
			return 0
		}
		if frac[2] >= '5' {
			n++
		}
		if n >= 100 {
			return 99
		}
		return n
	}
}

func unitWord(raw string, ru bool, n int64) string {
	low := strings.ToLower(raw)
	switch {
	case strings.Contains(raw, "%"):
		if ru {
			return ruNoun64(n, "процент", "процента", "процентов")
		}
		return "percent"
	case strings.Contains(low, "usdt"):
		if ru {
			return "тезер"
		}
		return "USDT"
	case strings.ContainsAny(raw, "$") || strings.Contains(low, "usd") || strings.Contains(low, "долл"):
		if ru {
			return ruNoun64(n, "доллар", "доллара", "долларов")
		}
		return "dollars"
	case strings.Contains(raw, "€") || strings.Contains(low, "eur"):
		return "евро"
	case strings.Contains(low, "руб"):
		if ru {
			return ruNoun64(n, "рубль", "рубля", "рублей")
		}
		return "rubles"
	}
	return ""
}

func parseNumToken(raw string) (int64, string, bool) {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, "\u00a0", " ")
	var cleaned strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) || r == '.' || r == ',' || r == ' ' || r == '-' {
			cleaned.WriteRune(r)
		}
	}
	s = strings.TrimSpace(cleaned.String())
	if s == "" {
		return 0, "", false
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	s = strings.TrimSpace(s)

	intPart, frac := splitIntFrac(s)
	if intPart == "" {
		return 0, "", false
	}
	n, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, "", false
	}
	if neg {
		n = -n
	}
	return n, frac, true
}

func splitIntFrac(s string) (string, string) {
	// Русская запись курса: пробел — тысячи, запятая — дробь («1 883,3»).
	// 68 000 / 68,000 / 68.000 → thousands if groups of 3.
	// 68.5 / 68,50 → decimal.
	if strings.Contains(s, " ") {
		lastComma := strings.LastIndex(s, ",")
		lastDot := strings.LastIndex(s, ".")
		dec := lastComma
		if lastDot > lastComma {
			dec = lastDot
		}
		if dec >= 0 {
			left := s[:dec]
			left = strings.ReplaceAll(left, " ", "")
			left = strings.ReplaceAll(left, ",", "")
			left = strings.ReplaceAll(left, ".", "")
			return digitsOnly(left), digitsOnly(s[dec+1:])
		}
		return digitsOnly(strings.ReplaceAll(s, " ", "")), ""
	}
	hasDot := strings.Contains(s, ".")
	hasComma := strings.Contains(s, ",")
	switch {
	case hasDot && hasComma:
		if strings.LastIndex(s, ",") > strings.LastIndex(s, ".") {
			s = strings.ReplaceAll(s, ".", "")
			parts := strings.Split(s, ",")
			return digitsOnly(parts[0]), parts[len(parts)-1]
		}
		s = strings.ReplaceAll(s, ",", "")
		parts := strings.Split(s, ".")
		return digitsOnly(parts[0]), parts[len(parts)-1]
	case hasComma:
		parts := strings.Split(s, ",")
		// «1,003 $» при русской запятой — это 1.003, не тысяча три.
		if thousandGroups(parts) && len(parts[0]) > 1 {
			return digitsOnly(strings.Join(parts, "")), ""
		}
		return digitsOnly(parts[0]), parts[len(parts)-1]
	case hasDot:
		parts := strings.Split(s, ".")
		if thousandGroups(parts) {
			return digitsOnly(strings.Join(parts, "")), ""
		}
		return digitsOnly(parts[0]), parts[len(parts)-1]
	default:
		return digitsOnly(s), ""
	}
}

func thousandGroups(parts []string) bool {
	if len(parts) < 2 {
		return false
	}
	if len(parts[0]) == 0 || len(parts[0]) > 3 {
		return false
	}
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) != 3 || !allDigits(parts[i]) {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
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

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String()
}

var (
	ruOnes    = []string{"", "один", "два", "три", "четыре", "пять", "шесть", "семь", "восемь", "девять"}
	ruOnesFem = []string{"", "одна", "две", "три", "четыре", "пять", "шесть", "семь", "восемь", "девять"}
	ruTeens   = []string{"десять", "одиннадцать", "двенадцать", "тринадцать", "четырнадцать", "пятнадцать", "шестнадцать", "семнадцать", "восемнадцать", "девятнадцать"}
	ruTens    = []string{"", "", "двадцать", "тридцать", "сорок", "пятьдесят", "шестьдесят", "семьдесят", "восемьдесят", "девяносто"}
	ruHund    = []string{"", "сто", "двести", "триста", "четыреста", "пятьсот", "шестьсот", "семьсот", "восемьсот", "девятьсот"}
)

func spellRuInt(n int64) string {
	if n == 0 {
		return "ноль"
	}
	if n < 0 {
		return "минус " + spellRuInt(-n)
	}
	type scale struct {
		v              int64
		one, few, many string
		fem            bool
	}
	scales := []scale{
		{1_000_000_000_000, "триллион", "триллиона", "триллионов", false},
		{1_000_000_000, "миллиард", "миллиарда", "миллиардов", false},
		{1_000_000, "миллион", "миллиона", "миллионов", false},
		{1_000, "тысяча", "тысячи", "тысяч", true},
	}
	var parts []string
	for _, sc := range scales {
		if n < sc.v {
			continue
		}
		q := n / sc.v
		n %= sc.v
		parts = append(parts, spellRuTriad(int(q), sc.fem), ruNoun64(q, sc.one, sc.few, sc.many))
	}
	if n > 0 {
		parts = append(parts, spellRuTriad(int(n), false))
	}
	return strings.Join(parts, " ")
}

func spellRuTriad(n int, fem bool) string {
	if n <= 0 {
		return ""
	}
	h, rem := n/100, n%100
	var parts []string
	if h > 0 {
		parts = append(parts, ruHund[h])
	}
	switch {
	case rem >= 10 && rem <= 19:
		parts = append(parts, ruTeens[rem-10])
	default:
		if rem >= 20 {
			parts = append(parts, ruTens[rem/10])
			rem %= 10
		}
		if rem > 0 {
			if fem {
				parts = append(parts, ruOnesFem[rem])
			} else {
				parts = append(parts, ruOnes[rem])
			}
		}
	}
	return strings.Join(parts, " ")
}

func spellRuFrac(frac string) string {
	frac = digitsOnly(frac)
	if frac == "" {
		return ""
	}
	for len(frac) > 1 && frac[len(frac)-1] == '0' {
		frac = frac[:len(frac)-1]
	}
	n, err := strconv.Atoi(frac)
	if err != nil {
		return frac
	}
	words := spellRuInt(int64(n))
	// женский род: десятая / сотая / тысячная
	ones := []string{"", "десятая", "сотая", "тысячная", "десятитысячная"}
	few := []string{"", "десятых", "сотых", "тысячных", "десятитысячных"}
	many := few
	k := utf8.RuneCountInString(frac)
	if k >= len(ones) {
		k = len(ones) - 1
	}
	noun := ruNoun(n, ones[k], few[k], many[k])
	// 1/21 — «одна», не «один»
	if k > 0 {
		words = spellRuIntFem(int64(n))
	}
	return strings.TrimSpace(words + " " + noun)
}

func spellRuIntFem(n int64) string {
	if n < 0 {
		return "минус " + spellRuIntFem(-n)
	}
	if n < 1000 {
		return spellRuTriad(int(n), true)
	}
	// крупные дроби редки — оставляем мужской разбор тысяч + женский хвост
	head := n / 1000
	tail := n % 1000
	var parts []string
	if head > 0 {
		parts = append(parts, spellRuInt(head*1000))
	}
	if tail > 0 {
		parts = append(parts, spellRuTriad(int(tail), true))
	}
	return strings.Join(parts, " ")
}

func ruNoun64(n int64, one, few, many string) string {
	if n < 0 {
		n = -n
	}
	return ruNoun(int(n%100), one, few, many)
}

func spellEnInt(n int64) string {
	if n == 0 {
		return "zero"
	}
	if n < 0 {
		return "minus " + spellEnInt(-n)
	}
	small := []string{"", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine",
		"ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen", "eighteen", "nineteen"}
	tens := []string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}
	var under1000 func(int) string
	under1000 = func(v int) string {
		switch {
		case v >= 100:
			rest := v % 100
			if rest == 0 {
				return small[v/100] + " hundred"
			}
			return small[v/100] + " hundred " + under1000(rest)
		case v >= 20:
			if v%10 == 0 {
				return tens[v/10]
			}
			return tens[v/10] + "-" + small[v%10]
		default:
			return small[v]
		}
	}
	scales := []struct {
		v    int64
		name string
	}{{1_000_000_000, "billion"}, {1_000_000, "million"}, {1_000, "thousand"}}
	var parts []string
	for _, sc := range scales {
		if n < sc.v {
			continue
		}
		q := n / sc.v
		n %= sc.v
		parts = append(parts, under1000(int(q)), sc.name)
	}
	if n > 0 {
		parts = append(parts, under1000(int(n)))
	}
	return strings.Join(parts, " ")
}

func spellEnDigits(s string) string {
	small := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}
	var parts []string
	for _, r := range s {
		if r >= '0' && r <= '9' {
			parts = append(parts, small[r-'0'])
		}
	}
	return strings.Join(parts, " ")
}
