package main

// Data layer for /analyze: pulls klines from Binance's public REST API (no key needed),
// computes the deterministic indicator set (trading/indicators.go) and feeds
// trading.AnalyzeSymbol. The LLM is not involved — every number comes from the engine.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"kibborg/engine/trading"
)

// newBinanceHTTP ходит только по IPv4: у api/fapi.binance.com AAAA часто отвечает,
// а пакеты до IPv6 не доходят, и запрос висит на «awaiting headers».
func newBinanceHTTP(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				d := net.Dialer{Timeout: 4 * time.Second}
				return d.DialContext(ctx, "tcp4", addr)
			},
			TLSHandshakeTimeout:   4 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
			IdleConnTimeout:       30 * time.Second,
			ForceAttemptHTTP2:     true,
		},
	}
}

var marketHTTP = newBinanceHTTP(8 * time.Second)

// klineHosts — публичные зеркала одного и того же /api/v3/klines. Первый живой
// запоминается: иначе каждый ТФ (15m/1h/4h/1d) по 8 секунд упирается в мёртвый хост.
var klineHosts = []string{
	"https://data-api.binance.vision",
	"https://api.binance.com",
	"https://api1.binance.com",
	"https://api2.binance.com",
	"https://api3.binance.com",
}

var (
	klineHostMu   sync.Mutex
	klineHostPref string
)

// klineLimit is how many candles per timeframe we pull. 500 gives the Wilder-smoothed
// indicators (RSI/ATR/MACD) a long warm-up so they converge, and EMA20/50 plenty of history.
// Binance allows up to 1000 per request, so this stays well within the limit.
const klineLimit = 500

type candle struct {
	high, low, close, volume float64
}

// fetchKlineRows pulls raw klines. Both the indicator path (fetchKlines) and the Gerchik
// daily path (fetchDailyBars) go through it: the second one needs open price and open time,
// which the indicator candle deliberately drops.
func fetchKlineRows(symbol, interval string, limit int) ([][]any, error) {
	return fetchKlineRowsFrom(klineHostOrder(), symbol, interval, limit)
}

func klineHostOrder() []string {
	klineHostMu.Lock()
	pref := klineHostPref
	klineHostMu.Unlock()
	if pref == "" {
		return append([]string(nil), klineHosts...)
	}
	out := []string{pref}
	for _, h := range klineHosts {
		if h != pref {
			out = append(out, h)
		}
	}
	return out
}

func rememberKlineHost(host string) {
	klineHostMu.Lock()
	klineHostPref = host
	klineHostMu.Unlock()
}

func fetchKlineRowsFrom(hosts []string, symbol, interval string, limit int) ([][]any, error) {
	if len(hosts) == 0 {
		return nil, fmt.Errorf("binance недоступен: нет хостов")
	}
	var last error
	for _, host := range hosts {
		rows, err := getKlineRows(host, symbol, interval, limit)
		if err == nil {
			rememberKlineHost(host)
			return rows, nil
		}
		if isUnknownBinanceSymbol(err) {
			return nil, err
		}
		last = err
		log.Printf("[MARKET] %s %s через %s: %v — пробую другое зеркало", symbol, interval, host, err)
	}
	return nil, fmt.Errorf("binance недоступен: %w", last)
}

func getKlineRows(host, symbol, interval string, limit int) ([][]any, error) {
	u := fmt.Sprintf("%s/api/v3/klines?symbol=%s&interval=%s&limit=%d",
		strings.TrimRight(host, "/"), symbol, interval, limit)
	resp, err := marketHTTP.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return parseKlineRows(raw, resp.StatusCode, symbol, interval)
}

func parseKlineRows(raw []byte, status int, symbol, interval string) ([][]any, error) {
	if status != 200 {
		var apiErr struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Code == -1121 {
			return nil, fmt.Errorf("тикер %s не найден на Binance (spot)", symbol)
		}
		return nil, fmt.Errorf("binance HTTP %d: %s", status, capLogTail(string(raw)))
	}
	// Each kline: [openTime, "open", "high", "low", "close", "volume", closeTime, ...]
	var rows [][]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("binance вернул не-JSON: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("binance вернул пустые данные по %s %s", symbol, interval)
	}
	return rows, nil
}

func isUnknownBinanceSymbol(err error) bool {
	return err != nil && strings.Contains(err.Error(), "не найден на Binance")
}

// fetchKlines pulls up to limit candles for a Binance spot symbol/interval.
func fetchKlines(symbol, interval string, limit int) ([]candle, error) {
	rows, err := fetchKlineRows(symbol, interval, limit)
	if err != nil {
		return nil, err
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
	// Binance returns the still-forming current candle as the last element. Drop it so every
	// indicator (RSI/ATR/MACD/trend/volume/change_pct) is computed only from CLOSED candles.
	// Otherwise volume≈0 early in a candle silently disables the panic/volume checks, and two
	// /analyze runs a minute apart produce different regimes — breaking determinism.
	if len(out) > 1 {
		out = out[:len(out)-1]
	}
	return out, nil
}

// gerchikDailyLimit — сколько дневных свечей тянем под методику: 180 дней истории (§2.1)
// плюс запас на EMA-200 и текущий незакрытый день.
const gerchikDailyLimit = 220

// fetchDailyBars returns CLOSED daily bars plus the still-forming current day. Метод Герчика
// живёт на дневках: уровни и ATR считаются по закрытым дням, а правило 75% — по открытию
// СЕГОДНЯШНЕГО дня, поэтому текущая свеча не выбрасывается, а возвращается отдельно.
func fetchDailyBars(symbol string) ([]trading.Bar, trading.Bar, error) {
	rows, err := fetchKlineRows(symbol, "1d", gerchikDailyLimit)
	if err != nil {
		return nil, trading.Bar{}, err
	}
	bars := make([]trading.Bar, 0, len(rows))
	for _, k := range rows {
		if len(k) < 6 {
			continue
		}
		bars = append(bars, trading.Bar{
			Time:   time.UnixMilli(int64(anyToF(k[0]))).UTC(),
			Open:   anyToF(k[1]),
			High:   anyToF(k[2]),
			Low:    anyToF(k[3]),
			Close:  anyToF(k[4]),
			Volume: anyToF(k[5]),
		})
	}
	if len(bars) < 2 {
		return nil, trading.Bar{}, fmt.Errorf("binance вернул мало дневных свечей по %s", symbol)
	}
	return bars[:len(bars)-1], bars[len(bars)-1], nil
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
	multiRSI := map[string]float64{}
	for _, tf := range []string{"15m", "1h", "4h"} {
		cs, err := fetchKlines(symbol, tf, klineLimit)
		if err != nil {
			return trading.DecisionReport{}, err
		}
		td := timeframeData(cs)
		timeframes[tf] = td
		if v, ok := td["rsi14"].(float64); ok {
			multiRSI[tf] = v
		}
	}
	// Futures-derived context (funding / OI / orderflow) enables the squeeze & panic regimes.
	// Best-effort: a spot-only coin without a perpetual simply yields nil and the analysis
	// proceeds on candles alone.
	extra := fetchFuturesContext(symbol)
	attachFlowToTimeframes(symbol, timeframes)
	report := trading.AnalyzeSymbol(symbol, "spot", false, timeframes, extra,
		[]string{"regime_classifier", "scoring"})
	if report.Meta == nil {
		report.Meta = map[string]any{}
	}
	report.Meta["flow"] = trading.AnalyzeFlow(trading.FlowSnapsFrom(timeframes))

	// Разбор по Герчику — вторая, независимая точка зрения на тот же инструмент: дневные
	// уровни, ATR по методике курса и готовый сценарий со стопом и целью. Считается отдельно
	// от скоринга и НЕ влияет на его вердикт: две методики должны спорить открыто, а не
	// смешиваться в одно усреднённое число. Дневки не пришли — разбор идёт без этого блока.
	if closed, today, derr := fetchDailyBars(symbol); derr == nil {
		if report.Meta == nil {
			report.Meta = map[string]any{}
		}
		g := trading.AnalyzeGerchik(symbol, closed, today, time.Now().UTC())
		report.Meta["gerchik"] = g
		report.Meta["rsi"] = rsiFromBars(closed, report.Regime, multiRSI, g)
	} else {
		log.Printf("[MARKET] дневные свечи по %s не получены: %v", symbol, derr)
		if cs, kerr := fetchKlines(symbol, "1d", klineLimit); kerr == nil {
			if report.Meta == nil {
				report.Meta = map[string]any{}
			}
			report.Meta["rsi"] = rsiFromCandles(cs, report.Regime, multiRSI, trading.GerchikReport{})
		}
	}
	return report, nil
}

// rsiFromBars считает RSI-фильтр по закрытым дневкам. Текущий незакрытый день
// сюда не попадает — иначе два запуска в одну сессию давали бы разный RSI.
func rsiFromBars(bars []trading.Bar, regime string, multi map[string]float64, g trading.GerchikReport) trading.RSIReport {
	n := len(bars)
	highs := make([]float64, n)
	lows := make([]float64, n)
	closes := make([]float64, n)
	vols := make([]float64, n)
	for i, b := range bars {
		highs[i], lows[i], closes[i], vols[i] = b.High, b.Low, b.Close, b.Volume
	}
	in := trading.RSIInput{
		Highs: highs, Lows: lows, Closes: closes, Volumes: vols,
		Regime:  regime,
		MultiTF: multi,
		ATR:     g.ATR.ATR,
	}
	if g.Support != nil {
		in.Support = g.Support.Price
	}
	if g.Resistance != nil {
		in.Resistance = g.Resistance.Price
	}
	r := trading.AnalyzeRSI(in)
	if len(bars) >= 16 {
		if r.MultiTF == nil {
			r.MultiTF = map[string]float64{}
		}
		r.MultiTF["1d"] = r.Value
	}
	return r
}

func rsiFromCandles(cs []candle, regime string, multi map[string]float64, g trading.GerchikReport) trading.RSIReport {
	n := len(cs)
	bars := make([]trading.Bar, n)
	for i, c := range cs {
		bars[i] = trading.Bar{High: c.high, Low: c.low, Close: c.close, Volume: c.volume}
	}
	return rsiFromBars(bars, regime, multi, g)
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

В разборе может быть блок «Разбор по Герчику (D1)» — это ВТОРАЯ, независимая методика: дневные уровни, ATR как High−Low трёх нормальных дней, и ДВА сценария — ЛОНГ и ШОРТ, каждый от своего уровня. Объясни оба:
- что за уровень держит каждую сторону и почему он сильный (касания, зеркальность, слом тренда, ЛП, круглая цифра);
- какой сценарий ближе к исполнению и что должно произойти, чтобы он сработал;
- у каждого сценария есть статус: ✅ вход по алгоритму, ⏳ заготовка (цена ещё не у уровня), 🚫 запрещён. Заготовку НЕЛЬЗЯ подавать как сигнал ко входу;
- какая модель распознана (ЛП 1/2/3+ барами, отбой БСУ-БПУ, пробой) и на каком расстоянии стоп и цели;
- ЗАПРЕТЫ методики («методика запрещает вход») — это не мелкий шрифт, а вывод: если они есть, сделки по Герчику НЕТ, как бы хорошо ни выглядел скор. Не смягчай их и не предлагай «зайти половиной объёма».
Если две методики расходятся (скоринг говорит long, Герчик запрещает вход), так и скажи — расхождение это факт, а не повод усреднить.

В разборе может быть блок «RSI-контекст» — это ТРЕТИЙ слой, фильтр, не торговая система и не скоринг. RSI не показывает «дорого/дёшево»: это отношение среднего роста к среднему падению за окно. Правила, которые уже посчитаны движком:
- сначала режим, потом RSI: в тренде высокий RSI — сила, шортить его нельзя; в боковике сигнал — возврат из зоны (был >70 и ушёл под 70), а не сам факт нахождения в ней;
- уровни 30/70 сдвигаются: бычий рынок 40–80, медвежий 20–60;
- дивергенция имеет смысл только на границе структуры (уровень Герчика или экстремум окна); посреди тренда это шум;
- зона 40–60 — проверка здоровья тренда (отскок держать, закрепление за зоной — структура теряет силу), не вход;
- лучший партнёр RSI — MFI (объём). Расхождение «импульс есть, денег нет» — осторожность, не кнопка входа.
AllowLong/AllowShort в этом блоке — запрет фильтра, не приказ открыть сделку. Не предлагай вход «по RSI».

В разборе может быть блок «Поток OI / CVD» — четвёртый слой, тоже независимый. OI — приходят ли новые деньги, CVD — кто агрессор (тейкер buy−sell). Квадранты уже посчитаны движком:
- цена↑ и OI↑ = новые лонги; цена↓ и OI↑ = новые шорты;
- цена↑ и OI↓ = закрытие шортов (не набор лонга); цена↓ и OI↓ = выход лонгов (не набор шорта);
- CVD должен соглашаться со стороной, иначе сторона снимается.
Если режим был боковик/переход, а поток на нескольких ТФ сошёлся — направление в шапке могло прийти из потока. Это явно написано в блоке. Если поток спорит со скорингом или Герчиком — назови расхождение, не усредняй. Не предлагай вход «по CVD».

ЖЁСТКОЕ ПРАВИЛО: нельзя выдумывать НОВЫЕ числа — цены, уровни входа/стопа/тейка, проценты, которых нет в разборе. Используй только те значения, что уже приведены. Если данных для уверенного вывода мало — прямо скажи об этом.

Пиши по-русски, в Markdown, СТРОГО по этой структуре (те же секции, что на веб-панели):

### 1. Основной сигнал (Скоринг)
режим, направление, скор/100, вероятность, суть в 1–2 предложениях.

### 2. План сделки (по скорингу)
вход, стоп, тейки, R:R, DCA — только числа из разбора.

### 3. Разбор по Герчику (D1)
оба сценария со статусом. Заготовку не выдавай за вход.

### 4. RSI-контекст
значение, рабочий диапазон, есть ли сигнал. Не предлагай вход «по RSI».

### 5. Поток OI / CVD
сводка сторон. Не предлагай вход «по потоку».

---

### ИТОГОВЫЙ ВЕРДИКТ
одно действие (входить / ждать / не входить), почему, риски списком, план действий списком.
Числа — только из разбора. Без вступлений вроде «конечно».`

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

	if g, ok := r.Meta["gerchik"].(trading.GerchikReport); ok {
		renderGerchik(&b, g)
	}

	if rsi, ok := r.Meta["rsi"].(trading.RSIReport); ok {
		renderRSI(&b, rsi)
	}

	if flow, ok := r.Meta["flow"].(trading.FlowReport); ok {
		renderFlow(&b, flow)
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
