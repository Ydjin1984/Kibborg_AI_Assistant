package main

import (
	"strings"
	"testing"
)

func TestTelegramizeReportWrapsPlanAndSetups(t *testing.T) {
	in := "" +
		"📊 **BTCUSDT** — детерминированный разбор (spot)\n" +
		"━━━━━━━━━━━━━━━\n" +
		"🧭 **Режим**: trend_down\n" +
		"🔴 **Направление**: SHORT\n" +
		"🟢 **Вердикт**: ALLOW\n" +
		"\n🎯 **План сделки**:\n" +
		"- Вход: `63069.43`\n" +
		"- 🛑 Стоп: `63192.73`\n" +
		"- ⚖️ R:R: **1:4.0**\n" +
		"🕒 Тренды: 15m range\n" +
		"\n🧱 **Разбор по Герчику (D1)**\n" +
		"📏 Уровни D1:\n" +
		"- `65014.66` — сопротивление\n" +
		"- `60792.61` — поддержка\n" +
		"\n🟢 **ЛОНГ** · ⏳ заготовка\n" +
		"- Модель: отбой от уровня `60792.61`\n" +
		"- Вход `60815.81` · 🛑 стоп `60676.63`\n" +
		"\n🔴 **ШОРТ** · ⏳ заготовка\n" +
		"- Модель: отбой от уровня `65014.66`\n"

	got := telegramizeReport(in)
	if !strings.Contains(got, "🎯 **План сделки**:\n```\n- Вход:") {
		t.Fatalf("план должен уйти в fence:\n%s", got)
	}
	if !strings.Contains(got, "1:4.0") || strings.Contains(got, "**1:4.0**") {
		t.Fatalf("звёздочки внутри fence мешают копировать: %s", got)
	}
	if !strings.Contains(got, "📏 Уровни D1:\n```\n- `65014.66`") {
		t.Fatalf("уровни D1 должны быть в fence:\n%s", got)
	}
	if !strings.Contains(got, "🟢 **ЛОНГ**") || !strings.Contains(got, "```\n- Модель: отбой") {
		t.Fatalf("лонг должен получить копируемый блок:\n%s", got)
	}
	// Шапка не должна превратиться в набор fence.
	if strings.Count(got, "```") < 6 {
		t.Fatalf("ждали минимум 3 блока (план/уровни/лонг или шорт), fences=%d\n%s", strings.Count(got, "```"), got)
	}
}

func TestSplitBySectionHeadersKeepsHeaderTogether(t *testing.T) {
	in := "📊 **BTCUSDT** — разбор\n🧭 **Режим**: x\n🟢 **Вердикт**: ALLOW\n\n🎯 **План сделки**:\n- Вход: `1`\n\n🧱 **Разбор по Герчику (D1)**\n⚡ ATR: 1\n\n🟢 **ЛОНГ** · ⏳\n- Вход `2`"
	parts := splitBySectionHeaders(in)
	if len(parts) < 3 {
		t.Fatalf("секций мало: %#v", parts)
	}
	if !strings.Contains(parts[0], "📊") || !strings.Contains(parts[0], "Вердикт") {
		t.Fatalf("шапка должна остаться одним куском: %q", parts[0])
	}
	joined := strings.Join(parts, "\n")
	// Не теряем строки (склейка без лишних пустых — только trim справа).
	for _, needle := range []string{"BTCUSDT", "План сделки", "Герчику", "ЛОНГ"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("потеряли %q", needle)
		}
	}
}

func TestPackSectionsRespectsLimit(t *testing.T) {
	parts := []string{"AAA", "BBB", "CCC"}
	got := packSections(parts, 10)
	if len(got) < 2 {
		t.Fatalf("ждали нарезку, получили %#v", got)
	}
	for _, c := range got {
		if len(c) > 12 { // 10 + "\n\n" запас
			t.Fatalf("кусок длиннее лимита: %q", c)
		}
	}
}

func TestIsSectionHeaderIgnoresListItems(t *testing.T) {
	if isSectionHeader("- 🟢 покупатели") {
		t.Fatal("пункт списка не заголовок")
	}
	if isSectionHeader("🟢 **Вердикт**: ALLOW") {
		t.Fatal("вердикт в шапке не отдельная секция")
	}
	if !isSectionHeader("🟢 **ЛОНГ** · ⏳ заготовка") {
		t.Fatal("лонг — секция")
	}
}
