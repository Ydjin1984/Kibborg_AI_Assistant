package main

// Data layer for /analyze: pulls klines from Binance's public REST API (no key needed),
// computes the deterministic indicator set (trading/indicators.go) and feeds
// trading.AnalyzeSymbol. The LLM is not involved — every number comes from the engine.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"kibborg/engine/trading"
)

var marketHTTP = &http.Client{Timeout: 15 * time.Second}

// klineLimit is how many candles per timeframe we pull. 500 gives the Wilder-smoothed
// indicators (RSI/ATR/MACD) a long warm-up so they converge, and EMA20/50 plenty of history.
// Binance allows up to 1000 per request, so this stays well within the limit.
const klineLimit = 500

type candle struct {
	high, low, close, volume float64
}

// fetchKlines pulls up to limit candles for a Binance spot symbol/interval.
func fetchKlines(symbol, interval string, limit int) ([]candle, error) {
	u := fmt.Sprintf("https://api.binance.com/api/v3/klines?symbol=%s&interval=%s&limit=%d",
		symbol, interval, limit)
	resp, err := marketHTTP.Get(u)
	if err != nil {
		return nil, fmt.Errorf("Binance недоступен: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		var apiErr struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Code == -1121 {
			return nil, fmt.Errorf("тикер %s не найден на Binance (spot)", symbol)
		}
		return nil, fmt.Errorf("Binance HTTP %d: %s", resp.StatusCode, capLogTail(string(raw)))
	}
	// Each kline: [openTime, "open", "high", "low", "close", "volume", closeTime, ...]
	var rows [][]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("Binance вернул не-JSON: %w", err)
	}
	out := make([]candle, 0, len(rows))
	for _, k := range rows {
		if len(k) < 6 {
			continue
		}
		out = append(out, candle{
			high:   anyToF(k[2]),
			low:    anyToF(k[3]),
			close:  anyToF(k[4]),
			volume: anyToF(k[5]),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("Binance вернул пустые данные по %s %s", symbol, interval)
	}
	// Binance returns the still-forming current candle as the last element. Drop it so every
	// indicator (RSI/ATR/MACD/trend/volume/change_pct) is computed only from CLOSED candles.
	// Otherwise volume≈0 early in a candle silently disables the panic/volume checks, and two
	// /analyze runs a minute apart produce different regimes — breaking determinism.
	if len(out) > 1 {
		out = out[:len(out)-1]
	}
	return out, nil
}

func anyToF(v any) float64 {
	switch x := v.(type) {
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	case float64:
		return x
	default:
		return 0
	}
}

// timeframeData computes the indicator map one timeframe of the trading engine expects.
func timeframeData(cs []candle) map[string]any {
	n := len(cs)
	closes := make([]float64, n)
	highs := make([]float64, n)
	lows := make([]float64, n)
	vols := make([]float64, n)
	for i, c := range cs {
		closes[i], highs[i], lows[i], vols[i] = c.close, c.high, c.low, c.volume
	}
	last := cs[n-1]
	changePct := 0.0
	if n >= 2 && cs[n-2].close > 0 {
		changePct = (last.close - cs[n-2].close) / cs[n-2].close * 100
	}
	atr := trading.ATR(highs, lows, closes, 14)
	ema20 := trading.EMALast(closes, 20)
	// atr_dist = how many ATRs price sits above/below EMA20 → overextension gauge for structure.
	atrDist := 0.0
	if atr > 0 {
		atrDist = (last.close - ema20) / atr
	}
	return map[string]any{
		"close":        last.close,
		"atr14":        atr,
		"change_pct":   changePct,
		"volume":       last.volume,
		"volume_sma20": trading.SMALast(vols, 20),
		"rsi14":        trading.RSI(closes, 14),
		"macd":         map[string]any{"histogram": trading.MACDHist(closes)},
		"trend":        trading.TrendLabel(closes),
		"ema20":        ema20,
		"ema50":        trading.EMALast(closes, 50),
		"swing":        trading.SwingStructure(highs, lows),
		"atr_dist":     atrDist,
	}
}

// normalizeSymbol turns user input ("btc", "eth/usdt", "SOL-USDT") into a Binance symbol.
// It keeps ONLY [A-Z0-9] after upper-casing, so stray separators, invisible whitespace or
// look-alike Cyrillic letters can't reach Binance (which rejects anything else with HTTP 400).
func normalizeSymbol(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	s = b.String()
	if s == "" {
		return s
	}
	for _, quote := range []string{"USDT", "USDC", "FDUSD", "BTC", "ETH", "BNB", "EUR", "TRY"} {
		if strings.HasSuffix(s, quote) && len(s) > len(quote) {
			return s
		}
	}
	return s + "USDT"
}

// analyzeTicker runs the full deterministic pipeline for one symbol.
func analyzeTicker(symbol string) (trading.DecisionReport, error) {
	symbol = normalizeSymbol(symbol)
	if symbol == "" {
		return trading.DecisionReport{}, fmt.Errorf("пустой тикер")
	}
	timeframes := map[string]any{}
	for _, tf := range []string{"15m", "1h", "4h"} {
		cs, err := fetchKlines(symbol, tf, klineLimit)
		if err != nil {
			return trading.DecisionReport{}, err
		}
		timeframes[tf] = timeframeData(cs)
	}
	// Futures-derived context (funding / OI / orderflow) enables the squeeze & panic regimes.
	// Best-effort: a spot-only coin without a perpetual simply yields nil and the analysis
	// proceeds on candles alone.
	extra := fetchFuturesContext(symbol)
	report := trading.AnalyzeSymbol(symbol, "spot", false, timeframes, extra,
		[]string{"regime_classifier", "scoring"})
	return report, nil
}

// narrateSystemPrompt turns the LLM into an INTERPRETER of the deterministic report: it
// explains what the pre-computed numbers mean, but is forbidden from inventing new ones —
// preserving the core invariant "the LLM never invents numbers".
const narrateSystemPrompt = `Ты — опытный трейдинг-аналитик. Ниже ДЕТЕРМИНИРОВАННЫЙ разбор от торгового движка: все числа (режим, скор, вероятность, тренды по таймфреймам, RSI/ATR/MACD, объёмы) уже рассчитаны движком и являются истиной.

Твоя задача — понятным языком объяснить трейдеру, что эти цифры значат на практике:
- какой сейчас режим рынка и что он подразумевает;
- о чём говорят скор и вероятность (сильный сигнал или слабый);
- стоит ли рассматривать вход в этом направлении и почему;
- какие риски и на что обратить внимание.

ЖЁСТКОЕ ПРАВИЛО: нельзя выдумывать НОВЫЕ числа — цены, уровни входа/стопа/тейка, проценты, которых нет в разборе. Используй только те значения, что уже приведены. Если данных для уверенного вывода мало — прямо скажи об этом.

Пиши кратко и по делу, по-русски, в Markdown. Без вступлений вроде «конечно» — сразу суть.`

// narrateReport asks the LLM to interpret a finished DecisionReport, streaming its narration
// through onDelta. Returns the full narration text and generation stats. The rendered
// deterministic report is the LLM's only factual input.
func narrateReport(cfg Config, r trading.DecisionReport, label string, onDelta func(string)) (string, GenStats, error) {
	msgs := []map[string]any{
		{"role": "system", "content": narrateSystemPrompt},
		{"role": "user", "content": "Вот разбор, прокомментируй его:\n\n" + renderReport(r)},
	}
	live.begin(label)
	text, stats, err := llmChatStream(cfg.BrainPort, msgs, 0.4, onDelta)
	live.finish(stats)
	return text, stats, err
}

// renderReport formats a DecisionReport for Telegram in the bot's Markdown style.
// Presentation only — every number is read from the report, never computed here.
func renderReport(r trading.DecisionReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📊 **%s** — детерминированный разбор (%s)\n", r.Symbol, r.Market)
	b.WriteString("━━━━━━━━━━━━━━━\n")

	dirIcon := "⚪"
	switch r.Direction {
	case "long":
		dirIcon = "🟢"
	case "short":
		dirIcon = "🔴"
	}
	fmt.Fprintf(&b, "🧭 **Режим**: %s · уверенность %.0f%%\n", r.Regime, r.Confidence*100)
	fmt.Fprintf(&b, "%s **Направление**: %s · скор **%.1f/100**\n", dirIcon, strings.ToUpper(r.Direction), r.FinalScore)
	fmt.Fprintf(&b, "📈 Вероятность: %.0f%%\n", r.Probability*100)
	if decision, ok := r.Meta["decision"].(string); ok {
		fmt.Fprintf(&b, "%s **Вердикт**: %s\n", verdictIcon(decision), decision)
	}

	renderPlan(&b, r)

	if regime, ok := r.Meta["regime"].(trading.RegimeResult); ok {
		if len(regime.Trends) > 0 {
			fmt.Fprintf(&b, "🕒 Тренды: 15m %s · 1h %s · 4h %s\n",
				regime.Trends["15m"], regime.Trends["1h"], regime.Trends["4h"])
		}
		if v, ok := regime.Metrics["atr_pct_15m"].(float64); ok {
			fmt.Fprintf(&b, "🌡 ATR 15m: %.2f%% от цены\n", v)
		}
	}

	// Derivatives context (Binance Futures) — funding / OI / order-flow. If none of these were
	// available (spot-only coin, or fapi geo-blocked), say so explicitly so the user knows the
	// squeeze/panic regimes couldn't be evaluated — silence would read as "all clear".
	shownDeriv := false
	if regime, ok := r.Meta["regime"].(trading.RegimeResult); ok {
		if v, ok := regime.Metrics["funding_rate"].(float64); ok && v != 0 {
			fmt.Fprintf(&b, "💸 Funding: %+.4f%%\n", v*100)
			shownDeriv = true
		}
		if v, ok := regime.Metrics["oi_change_pct"].(float64); ok && v != 0 {
			fmt.Fprintf(&b, "📉 OI за 15m: %+.2f%%\n", v)
			shownDeriv = true
		}
	}
	if ctx, ok := r.Meta["contexts"].(map[string]interface{}); ok {
		if of, ok := ctx["orderflow"].(map[string]interface{}); ok {
			if bias, ok := of["bias"].(string); ok && bias != "" {
				label := map[string]string{"buy_pressure": "🟢 покупатели", "sell_pressure": "🔴 продавцы", "neutral": "⚪ баланс"}[bias]
				if label == "" {
					label = bias
				}
				fmt.Fprintf(&b, "🌊 Order-flow: %s\n", label)
				shownDeriv = true
			}
		}
	}
	if !shownDeriv {
		b.WriteString("💸 Деривативы: недоступны — разбор по свечам (funding/OI/order-flow не получены).\n")
	}

	if breakdown, ok := r.Meta["scoring"].(trading.ScoreBreakdown); ok && len(breakdown.Components) > 0 {
		b.WriteString("\n🔍 **Компоненты скора**:\n")
		for _, c := range breakdown.Components {
			fmt.Fprintf(&b, "- `%s`: %.1f (вес %.2f) — %s\n", c.Name, c.Score, c.Weight, c.Reason)
		}
	}

	warned := false
	for _, f := range r.ContextFlags {
		sev, _ := f["severity"].(string)
		msg, _ := f["message"].(string)
		if sev == "warning" && msg != "" {
			if !warned {
				b.WriteString("\n⚠️ **Флаги**:\n")
				warned = true
			}
			b.WriteString("- 🟡 " + msg + "\n")
		}
	}

	if regime, ok := r.Meta["regime"].(trading.RegimeResult); ok && len(regime.Reasons) > 0 {
		b.WriteString("\n📋 **Причины**:\n")
		for _, reason := range regime.Reasons {
			b.WriteString("- " + reason + "\n")
		}
	}

	fmt.Fprintf(&b, "\n⏱ %s · engine `kibborg-go` (числа детерминированы, LLM не участвует)",
		r.Timestamp.Format("2006-01-02 15:04 UTC"))
	return b.String()
}

// verdictIcon maps the decision gate's verdict to a status marker.
func verdictIcon(decision string) string {
	switch decision {
	case "ALLOW":
		return "🟢"
	case "WATCH":
		return "🟡"
	case "REJECT":
		return "🔴"
	default: // WAIT
		return "⚪"
	}
}

// renderPlan prints the concrete trade levels (entry / DCA / SL / TP / R:R) for a directional
// setup. Non-directional setups (wait/range) have no levels, so the section is skipped.
// Every value is read from the report's Plan/Risk — computed by the engine, never here.
func renderPlan(b *strings.Builder, r trading.DecisionReport) {
	dir := strings.ToLower(r.Direction)
	if dir != "long" && dir != "short" {
		return
	}
	entry, ok := r.Plan["entry"].(float64)
	if !ok || entry == 0 {
		return
	}
	b.WriteString("\n🎯 **План сделки**:\n")
	fmt.Fprintf(b, "- Вход: `%s`\n", numStr(entry))
	if dca, ok := r.Plan["averaging"].([]float64); ok && len(dca) > 0 {
		fmt.Fprintf(b, "- Усреднение (DCA): %s\n", numList(dca))
	}
	if stop, ok := r.Plan["stop"].(float64); ok {
		line := fmt.Sprintf("- 🛑 Стоп: `%s`", numStr(stop))
		if pct, ok := r.Risk["stop_distance_pct"].(float64); ok && pct > 0 {
			line += fmt.Sprintf(" (%.2f%% от входа)", pct)
		}
		b.WriteString(line + "\n")
	}
	if tps, ok := r.Plan["take_profit"].([]float64); ok && len(tps) > 0 {
		fmt.Fprintf(b, "- 🎯 Тейки: %s\n", numList(tps))
	}
	if rr, ok := r.Plan["rr"].(float64); ok && rr > 0 {
		fmt.Fprintf(b, "- ⚖️ R:R (до дальнего тейка): **1:%.1f**\n", rr)
	}
	// Risk flags (tight/wide stop, low R:R on the first target) — actionable cautions.
	if flags, ok := r.Risk["flags"].([]map[string]string); ok {
		for _, f := range flags {
			if f["message"] != "" {
				b.WriteString("- 🟡 " + f["message"] + "\n")
			}
		}
	}
}

// numStr formats a price without trailing zeros; numList joins several in backticks.
func numStr(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func numList(xs []float64) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = "`" + numStr(x) + "`"
	}
	return strings.Join(parts, " · ")
}
