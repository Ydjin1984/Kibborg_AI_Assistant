package main

// Отрисовка разбора по Герчику (trading/gerchik.go) для отчёта /analyze. Только подача:
// каждое число читается из готового GerchikReport и ни одно не считается здесь — иначе
// в отчёте появились бы цифры, которых нет в детерминированном разборе.

import (
	"fmt"
	"strings"

	"kibborg/engine/trading"
)

// renderGerchik печатает блок методики: энергия, тренд, уровни, сценарий и — обязательно —
// запреты. Сценарий без запретов читается как «можно входить», поэтому блокировки идут
// сразу за ним, а не прячутся в конце сообщения.
func renderGerchik(b *strings.Builder, g trading.GerchikReport) {
	b.WriteString("\n🧱 **Разбор по Герчику (D1)**\n")
	if g.Days == 0 || g.ATR.ATR == 0 {
		for _, n := range g.Notes {
			b.WriteString("- ⚠️ " + n + "\n")
		}
		return
	}

	// ATR: показываем и то, как он получен — иначе число невозможно проверить.
	skipped := ""
	if g.ATR.SkippedBig > 0 || g.ATR.SkippedSmall > 0 {
		skipped = fmt.Sprintf(", отброшено %d больших / %d малых", g.ATR.SkippedBig, g.ATR.SkippedSmall)
	}
	fmt.Fprintf(b, "⚡ ATR: **%s** (H−L, %d нормальных дня%s) · энергия дня **%.0f%%**\n",
		numStr(round4(g.ATR.ATR)), g.ATR.NormalDays, skipped, g.Energy*100)
	fmt.Fprintf(b, "🧭 Тренд: глобальный **%s** · локальный %s · EMA-200 `%s`\n",
		g.GlobalTrend, g.LocalTrend, numStr(round4(g.EMA200)))

	if len(g.Levels) > 0 {
		b.WriteString("📏 Уровни D1:\n")
		for i := len(g.Levels) - 1; i >= 0; i-- { // сверху вниз, как на графике
			l := g.Levels[i]
			marks := []string{l.Kind, fmt.Sprintf("касаний %d", l.Touches)}
			if l.Round {
				marks = append(marks, "круглый")
			}
			if l.FromLP {
				marks = append(marks, "после ЛП")
			}
			if !l.Fresh {
				marks = append(marks, fmt.Sprintf("не свежий (%d дн.)", l.AgeDays))
			}
			side := "поддержка"
			if l.Above {
				side = "сопротивление"
			}
			fmt.Fprintf(b, "- `%s` — %s · %s · %.2f ATR от цены\n",
				numStr(round4(l.Price)), side, strings.Join(marks, ", "), l.DistATR)
		}
	}

	// Оба сценария печатаются ВСЕГДА и рядом: трейдеру нужны обе границы — где он покупает
	// и где продаёт. Один «ближайший» сценарий скрывал половину картины.
	renderSetup(b, g, g.Long, "🟢 **ЛОНГ**")
	renderSetup(b, g, g.Short, "🔴 **ШОРТ**")

	if len(g.Premises) > 0 {
		b.WriteString("📋 Предпосылки: " + strings.Join(g.Premises, "; ") + "\n")
	}
	for _, n := range g.Notes {
		b.WriteString("- ⚠️ " + n + "\n")
	}
}

// renderSetup печатает один сценарий. Первый символ строки — это вердикт: можно входить,
// ждать подхода или нельзя вовсе. Человек читает отчёт сверху вниз и по диагонали, поэтому
// статус стоит раньше цифр, а не после них.
func renderSetup(b *strings.Builder, g trading.GerchikReport, s trading.GerchikSetup, title string) {
	if s.Model == "" {
		fmt.Fprintf(b, "\n%s — сценария нет\n", title)
		for _, blk := range s.Blocks {
			b.WriteString("- 🚫 " + blk + "\n")
		}
		return
	}
	status := "✅ вход по алгоритму"
	switch {
	case len(s.Blocks) > 0 && s.Pending:
		status = "⏳ заготовка — ждём подхода к уровню"
	case len(s.Blocks) > 0:
		status = "🚫 вход запрещён методикой"
	}
	fmt.Fprintf(b, "\n%s · %s\n", title, status)
	fmt.Fprintf(b, "- Модель: %s (%s) от уровня `%s`\n",
		s.Model, s.Order, numStr(round4(s.Level)))
	// Размер стопа печатается значащими цифрами, а не по правилу цен: на монете за $77
	// стоп в 0.09857143 читается как ошибка, хотя это просто хвост float.
	fmt.Fprintf(b, "- Вход `%s` · 🛑 стоп `%s` (%.4g = %.0f%% ATR)\n",
		numStr(round4(s.Entry)), numStr(round4(s.Stop)), s.SLSize, s.SLSize/g.ATR.ATR*100)
	if len(s.Takes) > 0 {
		parts := make([]string, len(s.Takes))
		for i, tp := range s.Takes {
			parts[i] = fmt.Sprintf("`%s` (%d:1)", numStr(round4(tp)), i+3)
		}
		fmt.Fprintf(b, "- 🎯 Цели частями: %s\n", strings.Join(parts, " · "))
	} else if s.Take > 0 {
		fmt.Fprintf(b, "- 🎯 Цель `%s` — ближе трёх стопов%s\n", numStr(round4(s.Take)), takeNote(s))
	}
	if s.LevelTarget > 0 {
		fmt.Fprintf(b, "- 📏 Ход ограничен уровнем `%s` (%.1f ATR)\n",
			numStr(round4(s.LevelTarget)), s.TechATR)
	}
	// RR показывается и грязный, и чистый: разница между ними — это комиссия с
	// проскальзыванием, и на коротком стопе она решает, берётся сделка или нет.
	fmt.Fprintf(b, "- ⚖️ RR **1:%.1f**, с издержками **1:%.1f** (комиссия %.4g)",
		s.RR, s.RRNet, s.Costs)
	if s.Order == "лимитный ордер" {
		fmt.Fprintf(b, " · люфт %.4g", s.Luft)
	}
	b.WriteString("\n")
	for _, r := range s.Reasons {
		b.WriteString("- " + r + "\n")
	}
	for _, blk := range s.Blocks {
		b.WriteString("- 🚫 " + blk + "\n")
	}
}

// takeNote поясняет происхождение цели: уровень или расчёт от RR. Разница существенная —
// уровень стоит на графике, расчётное число не стоит нигде.
func takeNote(s trading.GerchikSetup) string {
	if s.TakeIsRR {
		return " _(расчёт от RR)_"
	}
	return " _(следующий уровень D1)_"
}

// gerchikSizingBlock считает объём позиции под стоп ПО МЕТОДИКЕ. Основной sizingBlock
// работает от плана скоринга; когда тот не направленный, а сетап Герчика есть, объём всё
// равно нужен — иначе методика остаётся без единственного, что реально ограничивает убыток.
func gerchikSizingBlock(cfg Config, report trading.DecisionReport) string {
	if cfg.TradeBalance <= 0 {
		return ""
	}
	g, ok := report.Meta["gerchik"].(trading.GerchikReport)
	if !ok {
		return ""
	}
	var out strings.Builder
	// Объём считается только для сценария, который методика РАЗРЕШАЕТ. Считать его для
	// заблокированного значило бы обойти запрет расчётом: цифры выглядят как приглашение.
	for _, setup := range []trading.GerchikSetup{g.Long, g.Short} {
		if !setup.Ready {
			continue
		}
		s := trading.SizePosition(trading.SizingInput{
			Direction:   setup.Direction,
			Entry:       setup.Entry,
			Stop:        setup.Stop,
			Balance:     cfg.TradeBalance,
			RiskPct:     cfg.TradeRiskPct,
			Leverage:    cfg.TradeLeverage,
			Targets:     setup.Takes,
			MaintMargin: cfg.TradeMMR,
		})
		if !s.OK {
			continue
		}
		side := "ЛОНГ"
		if setup.Direction == "short" {
			side = "ШОРТ"
		}
		out.WriteString("\n\n🧱 **Объём по Герчику · " + side + "**\n" + renderSizing(s, report.Symbol))
	}
	return out.String()
}

// round4 срезает хвост плавающей точки, который появляется после деления на ATR:
// «110000.00000000001» в отчёте выглядит как ошибка счёта, хотя это представление float.
func round4(f float64) float64 {
	if f == 0 {
		return 0
	}
	var scale float64
	switch a := absF(f); {
	case a >= 100: // цены BTC/ETH и размеры стопов — копейки тут только мешают читать
		scale = 1e2
	case a >= 1:
		scale = 1e3
	default: // альткоины по 0.00012 — знаки после запятой и есть вся цена
		scale = 1e8
	}
	return float64(int64(f*scale+signHalf(f))) / scale
}

func absF(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func signHalf(f float64) float64 {
	if f < 0 {
		return -0.5
	}
	return 0.5
}
