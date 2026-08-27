package main

// Как веб-разбор попадает в Telegram: тот же markdown, но режем по секциям
// (не посередине абзаца) и числовые блоки кладём в ``` — у Telegram появляется
// квадратик с кнопкой «копировать».

import (
	"strings"
)

// telegramizeReport оборачивает числовые блоки разбора в fence, чтобы они
// копировались одним тапом. Заголовки остаются снаружи — жирный/эмодзи работают.
func telegramizeReport(md string) string {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var out []string
	i := 0
	for i < len(lines) {
		line := lines[i]
		if shouldFenceFollowingList(line) {
			out = append(out, line)
			i++
			var block []string
			for i < len(lines) && isReportListLine(lines[i]) {
				block = append(block, stripMDForCopy(lines[i]))
				i++
			}
			if len(block) > 0 {
				out = append(out, "```")
				out = append(out, block...)
				out = append(out, "```")
			}
			continue
		}
		out = append(out, line)
		i++
	}
	return strings.Join(out, "\n")
}

func shouldFenceFollowingList(line string) bool {
	t := strings.TrimSpace(line)
	switch {
	case strings.Contains(t, "План сделки"):
		return true
	case strings.HasPrefix(t, "📏") && strings.Contains(t, "Уровни"):
		return true
	case strings.Contains(t, "**ЛОНГ**") || strings.Contains(t, "**ШОРТ**"):
		return true
	default:
		return false
	}
}

func isReportListLine(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	if strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "• ") {
		return true
	}
	// Строки сценария без маркера: «Модель: …», «Вход …»
	if strings.HasPrefix(t, "Модель:") || strings.HasPrefix(t, "Вход ") ||
		strings.Contains(t, "вход `") {
		return true
	}
	return false
}

// stripMDForCopy убирает **жирный** внутри копируемого блока — в <pre> он
// всё равно не рендерится, а звёздочки мешают вставлять числа в ордер.
func stripMDForCopy(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	return s
}

// sendTelegramSections шлёт текст кусками по смысловым секциям. Мелкие секции
// склеиваются, пока влезают в лимит; одна секция длиннее лимита уходит в splitMessage.
func sendTelegramSections(botAPI string, chatID int64, text string) {
	for _, chunk := range packSections(splitBySectionHeaders(text), 3000) {
		sendTelegramMessage(botAPI, chatID, chunk)
	}
}

// packSections склеивает соседние секции, пока они влезают в limit байт.
func packSections(parts []string, limit int) []string {
	if limit <= 0 {
		limit = 3000
	}
	var out []string
	var buf strings.Builder
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		out = append(out, buf.String())
		buf.Reset()
	}
	for _, p := range parts {
		p = strings.TrimRight(p, "\n")
		if p == "" {
			continue
		}
		need := len(p)
		if buf.Len() > 0 {
			need += 2
		}
		if buf.Len() > 0 && buf.Len()+need > limit {
			flush()
		}
		if len(p) > limit {
			flush()
			out = append(out, splitMessage(p, limit)...)
			continue
		}
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(p)
	}
	flush()
	return out
}

// splitBySectionHeaders режет отчёт по строкам, которые начинаются с эмодзи-заголовка.
// Строки-пункты («- 🟢 …») не считаются заголовками.
func splitBySectionHeaders(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var parts []string
	var cur strings.Builder
	for i, line := range lines {
		if i > 0 && isSectionHeader(line) && cur.Len() > 0 {
			parts = append(parts, strings.TrimRight(cur.String(), "\n"))
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteByte('\n')
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		parts = append(parts, strings.TrimRight(cur.String(), "\n"))
	}
	return parts
}

func isSectionHeader(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "-") || strings.HasPrefix(t, "•") {
		return false
	}
	switch {
	case strings.HasPrefix(t, "📊 "):
		return true
	case strings.Contains(t, "План сделки"):
		return true
	case strings.Contains(t, "Компоненты скора"):
		return true
	case strings.Contains(t, "Разбор по Герчику"):
		return true
	case strings.Contains(t, "**ЛОНГ**"):
		return true
	case strings.Contains(t, "**ШОРТ**"):
		return true
	case strings.Contains(t, "RSI-контекст"):
		return true
	case strings.Contains(t, "Поток OI"):
		return true
	case strings.HasPrefix(t, "📋 **Причины"):
		return true
	case strings.HasPrefix(t, "📋 Предпосылки"):
		return true
	case strings.HasPrefix(t, "⏱ "):
		return true
	case strings.Contains(t, "Комментарий нейросети"):
		return true
	case strings.HasPrefix(t, "⚠️ **Флаги"):
		return true
	default:
		return false
	}
}
