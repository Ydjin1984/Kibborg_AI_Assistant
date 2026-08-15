package main

// Отрисовка RSI-фильтра для /analyze. Числа только из готового RSIReport —
// здесь ничего не считается, иначе в тексте появились бы цифры, которых нет в движке.

import (
	"fmt"
	"strings"

	"kibborg/engine/trading"
)

func renderRSI(b *strings.Builder, r trading.RSIReport) {
	b.WriteString("\n📉 **RSI-контекст** — фильтр, не вход\n")
	if r.Verdict == "" && r.Value == 0 && r.Period == 0 {
		b.WriteString("- ⚠️ RSI не посчитан\n")
		return
	}
	if strings.Contains(r.Verdict, "недоступен") {
		for _, n := range r.Notes {
			b.WriteString("- ⚠️ " + n + "\n")
		}
		b.WriteString("- **вердикт:** " + r.Verdict + "\n")
		return
	}
	fmt.Fprintf(b, "режим **%s** · период %d · рабочие уровни **%.0f–%.0f**\n",
		r.Regime, r.Period, r.ZoneLow, r.ZoneHigh)
	if r.ZoneNote != "" {
		b.WriteString("- " + r.ZoneNote + "\n")
	}
	if r.Value != 0 || r.Period > 0 {
		sma := ""
		if r.SMA > 0 {
			sma = fmt.Sprintf(" · SMA%d `%.1f`", r.SMAPeriod, r.SMA)
		}
		fmt.Fprintf(b, "- RSI **%.1f** · наклон %+.1f · ускорение %+.1f%s\n",
			r.Value, r.Slope, r.Accel, sma)
		fmt.Fprintf(b, "- до 30: %+.1f · 40: %+.1f · 50: %+.1f · 60: %+.1f · 70: %+.1f\n",
			r.Dist30, r.Dist40, r.Dist50, r.Dist60, r.Dist70)
	}
	crosses := rsiCrossLine(r)
	if crosses != "" {
		b.WriteString("- пересечения: " + crosses + "\n")
	}
	if r.TimeAbove70 > 0 {
		fmt.Fprintf(b, "- RSI держится выше 70 уже **%d** свечей — в тренде это сила, не шорт\n", r.TimeAbove70)
	}
	if r.TimeBelow30 > 0 {
		fmt.Fprintf(b, "- RSI держится ниже 30 уже **%d** свечей — в тренде это сила продавцов, не лонг\n", r.TimeBelow30)
	}
	if r.MFI > 0 || r.MFISlope != 0 {
		split := "с RSI согласен"
		switch r.MFIRSISplit {
		case "volume_lag":
			split = "расхождение: импульс цены есть, денег в движении нет"
		case "agree":
			split = "RSI и MFI смотрят в одну сторону"
		case "":
			split = "нейтрально относительно RSI"
		}
		fmt.Fprintf(b, "- MFI **%.1f** (наклон %+.1f) — %s\n", r.MFI, r.MFISlope, split)
	}
	if len(r.MultiTF) > 0 {
		b.WriteString("- мульти-ТФ RSI:")
		for _, tf := range []string{"15m", "1h", "4h", "1d"} {
			if v, ok := r.MultiTF[tf]; ok {
				fmt.Fprintf(b, " %s `%.0f`", tf, v)
			}
		}
		if r.MultiBias != "" {
			fmt.Fprintf(b, " · импульс **%s**", r.MultiBias)
		}
		b.WriteString("\n")
	}
	for _, sc := range r.Scenarios {
		icon := "·"
		switch sc.Status {
		case "signal":
			icon = "✅"
		case "noise", "broken":
			icon = "⚠️"
		case "strength", "healthy":
			icon = "🟢"
		case "wait":
			icon = "⏳"
		}
		fmt.Fprintf(b, "- %s %s: %s\n", icon, rsiScenarioName(sc.ID), sc.Text)
	}
	long, short := "открыт", "открыт"
	if !r.AllowLong {
		long = "запрещён фильтром"
	}
	if !r.AllowShort {
		short = "запрещён фильтром"
	}
	fmt.Fprintf(b, "- фильтр сторон: лонг %s · шорт %s\n", long, short)
	if r.Verdict != "" {
		b.WriteString("- **вердикт:** " + r.Verdict + "\n")
	}
	for _, n := range r.Notes {
		b.WriteString("- ⚠️ " + n + "\n")
	}
}

func rsiScenarioName(id string) string {
	switch id {
	case "divergence":
		return "дивергенция"
	case "trend_filter":
		return "зона 40–60"
	case "extreme_return":
		return "возврат из зоны"
	default:
		return id
	}
}

func rsiCrossLine(r trading.RSIReport) string {
	var parts []string
	add := func(level int, dir string) {
		if dir == "" {
			return
		}
		arrow := "↑"
		if dir == "down" {
			arrow = "↓"
		}
		parts = append(parts, fmt.Sprintf("%d%s", level, arrow))
	}
	add(30, r.Cross30)
	add(40, r.Cross40)
	add(50, r.Cross50)
	add(60, r.Cross60)
	add(70, r.Cross70)
	if r.SMACross != "" {
		arrow := "↑"
		if r.SMACross == "down" {
			arrow = "↓"
		}
		parts = append(parts, "SMA"+arrow)
	}
	return strings.Join(parts, " · ")
}
