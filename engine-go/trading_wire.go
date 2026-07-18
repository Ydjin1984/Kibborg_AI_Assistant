package main

// Trader wiring: position sizing (/size) and the trade journal (/log, /journal, /close).
// The math lives in trading.SizePosition (deterministic); the journal lives in the journal
// package (SQLite). This file only parses the chat command, calls the engine, persists to the
// journal, and renders the result — it never computes trading numbers itself.

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"kibborg/engine/journal"
	"kibborg/engine/trading"
)

const journalDBPath = "runtime/journal.db"

// tradeJournal is the process-wide journal store (nil if it failed to open → commands degrade
// to a clear "journal unavailable" message). Set once in initJournal before workers start.
var tradeJournal *journal.Store

func initJournal() {
	if err := os.MkdirAll("runtime", 0o755); err != nil {
		log.Printf("[JOURNAL] не создать runtime/: %v — журнал сделок выключен", err)
		return
	}
	st, err := journal.Open(journalDBPath)
	if err != nil {
		log.Printf("[JOURNAL] не открылась БД (%v) — журнал сделок выключен", err)
		return
	}
	tradeJournal = st
	log.Printf("[JOURNAL] журнал сделок включён: %s", journalDBPath)
}

func closeJournal() {
	if tradeJournal != nil {
		_ = tradeJournal.Close()
	}
}

// tradeCommands route to the trader handlers.
var (
	sizeCommands    = []string{"/size", "/размер", "/риск"}
	logCommands     = []string{"/log", "/лог", "/trade"}
	journalCommands = []string{"/journal", "/журнал", "/trades", "/сделки"}
	closeCommands   = []string{"/close", "/закрыть"}
)

// ===== argument parsing =====

// cmdArgs holds a parsed trader command line: an optional symbol, key=value pairs, and any
// bare numeric tokens (positional entry/stop shorthand).
type cmdArgs struct {
	symbol   string
	kv       map[string]string
	bareNums []float64
	bareToks []string
}

func parseArgs(s string) cmdArgs {
	a := cmdArgs{kv: map[string]string{}}
	for _, tok := range strings.Fields(s) {
		if i := strings.IndexByte(tok, '='); i > 0 {
			a.kv[strings.ToLower(tok[:i])] = strings.TrimSpace(tok[i+1:])
			continue
		}
		if f, err := strconv.ParseFloat(tok, 64); err == nil {
			a.bareNums = append(a.bareNums, f)
			continue
		}
		a.bareToks = append(a.bareToks, tok)
		if a.symbol == "" {
			a.symbol = strings.ToUpper(tok)
		}
	}
	return a
}

// getF returns the first present key parsed as float, else def.
func (a cmdArgs) getF(def float64, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := a.kv[k]; ok {
			if f, err := strconv.ParseFloat(strings.ReplaceAll(v, ",", "."), 64); err == nil {
				return f
			}
		}
	}
	return def
}

func (a cmdArgs) getStr(keys ...string) string {
	for _, k := range keys {
		if v, ok := a.kv[k]; ok {
			return v
		}
	}
	return ""
}

// targets parses tp/target/targets (comma-separated) plus tp1/tp2/tp3.
func (a cmdArgs) targets() []float64 {
	var out []float64
	add := func(s string) {
		for _, part := range strings.Split(s, ",") {
			if f, err := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(part, " ", "")), 64); err == nil && f > 0 {
				out = append(out, f)
			}
		}
	}
	if v := a.getStr("tp", "target", "targets", "tps"); v != "" {
		add(v)
	}
	for _, k := range []string{"tp1", "tp2", "tp3"} {
		if v, ok := a.kv[k]; ok {
			add(v)
		}
	}
	return out
}

// resolveSizing turns a parsed command + config defaults into a SizingInput, filling entry/stop
// from key=value or positional numbers and inferring direction from the stop side.
func resolveSizing(cfg Config, a cmdArgs) trading.SizingInput {
	entry := a.getF(0, "entry", "e", "in", "вход")
	stop := a.getF(0, "stop", "sl", "s", "стоп")
	// Positional shorthand: "/size BTC 50000 49000" → entry, stop.
	if entry == 0 && len(a.bareNums) >= 1 {
		entry = a.bareNums[0]
	}
	if stop == 0 && len(a.bareNums) >= 2 {
		stop = a.bareNums[1]
	}
	dir := strings.ToLower(a.getStr("dir", "direction", "side"))
	if dir == "" && entry > 0 && stop > 0 {
		if stop < entry {
			dir = "long"
		} else {
			dir = "short"
		}
	}
	return trading.SizingInput{
		Direction:   dir,
		Entry:       entry,
		Stop:        stop,
		Balance:     a.getF(cfg.TradeBalance, "balance", "bal", "депо", "депозит"),
		RiskPct:     a.getF(cfg.TradeRiskPct, "risk", "r", "риск"),
		Leverage:    a.getF(cfg.TradeLeverage, "lev", "leverage", "плечо"),
		Targets:     a.targets(),
		MaintMargin: cfg.TradeMMR,
	}
}

// ===== command handlers (return the reply text) =====

func handleSizeCommand(cfg Config, arg string) string {
	a := parseArgs(arg)
	in := resolveSizing(cfg, a)
	if in.Entry == 0 || in.Stop == 0 {
		return "📐 **Размер позиции**\nУкажи вход и стоп:\n" +
			"`/size BTC entry=50000 stop=49000 tp=53000 risk=1.5 lev=10 balance=1000`\n" +
			"Коротко: `/size 50000 49000` (вход стоп; направление определю по стопу).\n" +
			"Баланс/риск/плечо берутся из settings.ini (`TRADE_BALANCE`, `TRADE_RISK_PCT`, `TRADE_LEVERAGE`), если не заданы."
	}
	if in.Balance <= 0 {
		return "📐 Не задан баланс счёта. Добавь `balance=1000` в команду или `TRADE_BALANCE` в settings.ini."
	}
	s := trading.SizePosition(in)
	if !s.OK {
		return "❌ " + s.Err
	}
	return renderSizing(s, a.symbol)
}

func handleLogCommand(cfg Config, chatID int64, arg string) string {
	if tradeJournal == nil {
		return "❌ Журнал сделок недоступен."
	}
	a := parseArgs(arg)
	in := resolveSizing(cfg, a)
	if a.symbol == "" || in.Entry == 0 || in.Stop == 0 {
		return "📝 **Записать сделку**\n" +
			"`/log BTC entry=50000 stop=49000 tp=53000 risk=1.5 lev=10`\n" +
			"Символ, вход и стоп обязательны. Размер посчитаю из баланса/риска (settings.ini) " +
			"или возьму `qty=` из команды."
	}
	if in.Direction != "long" && in.Direction != "short" {
		return "❌ Не понял направление — укажи `dir=long` или `dir=short`."
	}
	t := journal.Trade{
		ChatID:    chatID,
		Symbol:    a.symbol,
		Direction: in.Direction,
		Entry:     in.Entry,
		Stop:      in.Stop,
		Targets:   in.Targets,
		Leverage:  in.Leverage,
		Note:      a.getStr("note", "заметка"),
	}
	// Size from balance/risk if we can; else accept an explicit qty=.
	if in.Balance > 0 && in.RiskPct > 0 {
		if s := trading.SizePosition(in); s.OK {
			t.Qty = s.Qty
			t.Notional = s.Notional
			t.RiskAmount = s.RiskAmount
		}
	}
	if q := a.getF(0, "qty", "size", "объём"); q > 0 {
		t.Qty = q
	}
	id, err := tradeJournal.Add(t)
	if err != nil {
		return "❌ Не записал сделку: " + err.Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "✅ Сделка #%d записана: %s %s\n", id, strings.ToUpper(t.Direction), t.Symbol)
	fmt.Fprintf(&b, "Вход `%s` · Стоп `%s`", fnum(t.Entry), fnum(t.Stop))
	if t.Qty > 0 {
		fmt.Fprintf(&b, " · Объём `%s`", fnum(t.Qty))
	}
	if t.RiskAmount > 0 {
		fmt.Fprintf(&b, " · Риск `%s`", fnum(t.RiskAmount))
	}
	b.WriteString("\nЗакрыть: `/close " + strconv.FormatInt(id, 10) + " <цена выхода>`")
	return b.String()
}

func handleCloseCommand(arg string) string {
	if tradeJournal == nil {
		return "❌ Журнал сделок недоступен."
	}
	a := parseArgs(arg)
	if len(a.bareNums) < 1 && a.getF(0, "id") == 0 {
		return "🔒 Закрыть сделку: `/close <id> <цена выхода>` или `/close <id> cancel` (отменить)."
	}
	id := int64(a.getF(0, "id"))
	if id == 0 && len(a.bareNums) >= 1 {
		id = int64(a.bareNums[0])
	}
	// Cancel form: "/close 7 cancel".
	for _, tok := range a.bareToks {
		if strings.EqualFold(tok, "cancel") || strings.EqualFold(tok, "отмена") {
			if err := tradeJournal.Cancel(id); err != nil {
				return "❌ " + err.Error()
			}
			return fmt.Sprintf("🚫 Сделка #%d отменена.", id)
		}
	}
	exit := a.getF(0, "exit", "price", "цена")
	if exit == 0 && len(a.bareNums) >= 2 {
		exit = a.bareNums[1]
	}
	if exit == 0 {
		return "🔒 Укажи цену выхода: `/close " + strconv.FormatInt(id, 10) + " <цена>`."
	}
	t, err := tradeJournal.CloseTrade(id, exit)
	if err != nil {
		return "❌ " + err.Error()
	}
	icon := map[string]string{journal.StatusWin: "🟢", journal.StatusLoss: "🔴", journal.StatusBreakeven: "⚪"}[t.Status]
	var b strings.Builder
	fmt.Fprintf(&b, "%s Сделка #%d закрыта: %s %s\n", icon, t.ID, strings.ToUpper(t.Direction), t.Symbol)
	fmt.Fprintf(&b, "Вход `%s` → выход `%s`\n", fnum(t.Entry), fnum(t.ExitPrice))
	fmt.Fprintf(&b, "P&L: **%s**", signed(t.PnL))
	if t.RMultiple != 0 {
		fmt.Fprintf(&b, " (%sR)", signedNum(t.RMultiple))
	}
	return b.String()
}

func handleJournalCommand(chatID int64, arg string) string {
	if tradeJournal == nil {
		return "❌ Журнал сделок недоступен."
	}
	a := parseArgs(arg)
	statusFilter := ""
	for _, tok := range a.bareToks {
		switch strings.ToLower(tok) {
		case "open", "открытые":
			statusFilter = journal.StatusOpen
		case "win", "wins", "плюс":
			statusFilter = journal.StatusWin
		case "loss", "losses", "минус":
			statusFilter = journal.StatusLoss
		}
	}
	trades, err := tradeJournal.List(chatID, statusFilter, 15)
	if err != nil {
		return "❌ " + err.Error()
	}
	st, _ := tradeJournal.Stats(chatID)

	var b strings.Builder
	b.WriteString("📒 **Журнал сделок**\n")
	fmt.Fprintf(&b, "Закрыто: %d · 🟢 %d · 🔴 %d · ⚪ %d · открыто: %d\n",
		st.Total, st.Wins, st.Losses, st.Breakeven, st.Open)
	if st.Wins+st.Losses > 0 {
		fmt.Fprintf(&b, "Винрейт: **%.1f%%** · средний R: **%sR** · ожидание: **%s/сделка**\n",
			st.WinRate, signedNum(st.AvgR), signed(st.Expectancy))
		fmt.Fprintf(&b, "Итог P&L: **%s** · профит-фактор: %s\n",
			signed(st.TotalPnL), fnum(st.ProfitFactor))
	}
	if len(trades) == 0 {
		b.WriteString("\nПока нет записей. Логируй: `/log BTC entry=… stop=… dir=long`.")
		return b.String()
	}
	b.WriteString("\n")
	for _, t := range trades {
		icon := map[string]string{
			journal.StatusOpen: "⏳", journal.StatusWin: "🟢",
			journal.StatusLoss: "🔴", journal.StatusBreakeven: "⚪", journal.StatusCancelled: "🚫",
		}[t.Status]
		fmt.Fprintf(&b, "%s `#%d` %s %s вход `%s` стоп `%s`",
			icon, t.ID, strings.ToUpper(t.Direction), t.Symbol, fnum(t.Entry), fnum(t.Stop))
		if t.Status != journal.StatusOpen && t.Status != journal.StatusCancelled {
			fmt.Fprintf(&b, " → `%s` P&L %s", fnum(t.ExitPrice), signed(t.PnL))
			if t.RMultiple != 0 {
				fmt.Fprintf(&b, " (%sR)", signedNum(t.RMultiple))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ===== rendering =====

func renderSizing(s trading.Sizing, symbol string) string {
	var b strings.Builder
	title := "📐 **Размер позиции**"
	if symbol != "" {
		title += " · " + symbol
	}
	b.WriteString(title + "\n")
	fmt.Fprintf(&b, "Направление: **%s**\n", strings.ToUpper(s.Direction))
	fmt.Fprintf(&b, "Вход `%s` · Стоп `%s` (%.2f%% от входа)\n", fnum(s.Entry), fnum(s.Stop), s.StopDistancePct)
	fmt.Fprintf(&b, "Баланс `%s` · Риск `%.2f%%` = **%s**\n", fnum(s.Balance), s.RiskPct, fnum(s.RiskAmount))
	fmt.Fprintf(&b, "➡️ Объём позиции: **%s** ед. (номинал `%s`)\n", fnum(s.Qty), fnum(s.Notional))
	if s.Leverage > 1 {
		fmt.Fprintf(&b, "Плечо `%g×` · маржа **%s**\n", s.Leverage, fnum(s.Margin))
		if s.LiquidationPrice > 0 {
			fmt.Fprintf(&b, "⚠️ Ликвидация ~ `%s`\n", fnum(s.LiquidationPrice))
		}
	}
	if s.RR > 0 {
		fmt.Fprintf(&b, "⚖️ R:R (до дальнего тейка): **1:%.1f**\n", s.RR)
	}
	fmt.Fprintf(&b, "Максимальный убыток при стопе: **%s** (= заданный риск)\n", fnum(s.RiskAmount))
	for _, w := range s.Warnings {
		b.WriteString(w + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// sizingBlock appends a position-sizing suggestion to an /analyze report when a balance is
// configured and the setup is directional. Levels come straight from the deterministic plan.
func sizingBlock(cfg Config, report trading.DecisionReport) string {
	if cfg.TradeBalance <= 0 {
		return ""
	}
	dir := strings.ToLower(report.Direction)
	if dir != "long" && dir != "short" {
		return ""
	}
	entry, _ := report.Plan["entry"].(float64)
	stop, _ := report.Plan["stop"].(float64)
	if entry == 0 || stop == 0 {
		return ""
	}
	tps, _ := report.Plan["take_profit"].([]float64)
	s := trading.SizePosition(trading.SizingInput{
		Direction:   dir,
		Entry:       entry,
		Stop:        stop,
		Balance:     cfg.TradeBalance,
		RiskPct:     cfg.TradeRiskPct,
		Leverage:    cfg.TradeLeverage,
		Targets:     tps,
		MaintMargin: cfg.TradeMMR,
	})
	if !s.OK {
		return ""
	}
	return "\n\n" + renderSizing(s, report.Symbol) +
		"\n\n_Записать: `/log " + report.Symbol + " entry=" + fnum(entry) + " stop=" + fnum(stop) + " dir=" + dir + "`_"
}

// ===== number formatting =====

func fnum(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// signed formats a money value with an explicit +/- sign.
func signed(f float64) string {
	if f > 0 {
		return "+" + fnum(f)
	}
	return fnum(f)
}

func signedNum(f float64) string {
	if f > 0 {
		return "+" + fnum(f)
	}
	return fnum(f)
}
