package main

import (
	"fmt"
	"strings"

	"kibborg/engine/trading"
)

func renderFlow(b *strings.Builder, r trading.FlowReport) {
	b.WriteString("\n🌊 **Поток OI / CVD** — деньги и агрессия, не цена\n")
	if !r.Available {
		if r.Verdict != "" {
			b.WriteString("- ⚠️ " + r.Verdict + "\n")
		}
		for _, n := range r.Notes {
			b.WriteString("- ⚠️ " + n + "\n")
		}
		return
	}
	side := "стороны нет"
	if r.Side == "long" {
		side = "ЛОНГ"
	}
	if r.Side == "short" {
		side = "ШОРТ"
	}
	fmt.Fprintf(b, "сводка: **%s** · скоры лонг %.0f / шорт %.0f\n", side, r.LongScore, r.ShortScore)
	for _, s := range r.Snaps {
		line := fmt.Sprintf("- %s: ", s.TF)
		if s.HasOI {
			line += fmt.Sprintf("OI %+.2f%%", s.OIChangePct)
		} else {
			line += "OI нет"
		}
		if s.HasCVD {
			line += fmt.Sprintf(" · CVD Δ %+.4g", s.CVDDelta)
		}
		if s.Quadrant != "" {
			line += " · " + flowQuadName(s.Quadrant)
		}
		if s.Note != "" {
			line += " — " + s.Note
		}
		b.WriteString(line + "\n")
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

func flowQuadName(q string) string {
	switch q {
	case "new_longs":
		return "новые лонги"
	case "new_shorts":
		return "новые шорты"
	case "short_cover":
		return "закрытие шортов"
	case "long_liq":
		return "выход лонгов"
	default:
		return q
	}
}
