package trading

// RSI как контекстный фильтр, а не генератор входа. Источник правил — разбор Уайлдера
// (соотношение среднего роста и среднего падения) плюс три рабочих сценария: дивергенция
// только на границе структуры, зона 40–60 как проверка здоровья тренда, возврат из
// экстремума только в боковике. Высокий RSI в тренде здесь сила, а не шорт.

import "math"

const (
	rsiDefaultPeriod = 14
	rsiDefaultSMA    = 9
	rsiSwingPad      = 3
	rsiDivLookback   = 60
	rsiDivMinDelta   = 3.0
	rsiNearLevelATR  = 0.5
	rsiExtremeHigh   = 70.0
	rsiExtremeLow    = 30.0
)

// RSIInput — свечи одного ТФ плюс уже известный режим и уровни методики.
// Уровни необязательны: без них граница структуры определяется только экстремумом окна.
type RSIInput struct {
	Highs, Lows, Closes, Volumes []float64
	Period, SMAPeriod            int
	Regime                       string
	MultiTF                      map[string]float64
	Support, Resistance, ATR     float64
}

// RSIScenario — один из трёх рабочих сценариев. Active=true не значит «входи»:
// Status говорит, сигнал это, шум, живая структура или слом.
type RSIScenario struct {
	ID     string `json:"id"`
	Active bool   `json:"active"`
	Side   string `json:"side"`
	Status string `json:"status"`
	Text   string `json:"text"`
}

// OscMark — точка на свече, которую панель рисует на цене и/или на осцилляторе.
type OscMark struct {
	Index   int     `json:"i"`
	Kind    string  `json:"kind"`
	Price   float64 `json:"price"`
	RSI     float64 `json:"rsi"`
	OnPrice bool    `json:"on_price"`
}

// OscillatorPane — ряд RSI/MFI/SMA в одной длине со свечами. Числа считает движок,
// чтобы график панели не расходился с блоком в отчёте.
type OscillatorPane struct {
	Period    int       `json:"period"`
	SMAPeriod int       `json:"sma_period"`
	From      int       `json:"from"`
	SMAFrom   int       `json:"sma_from"`
	MFIFrom   int       `json:"mfi_from"`
	Regime    string    `json:"regime"`
	ZoneLow   float64   `json:"zone_low"`
	ZoneHigh  float64   `json:"zone_high"`
	RSI       []float64 `json:"rsi"`
	SMA       []float64 `json:"sma"`
	MFI       []float64 `json:"mfi"`
	Marks     []OscMark `json:"marks"`
}

// RSIReport — набор признаков и вердикт-фильтр. Это не план сделки: скоринг и Герчик
// считаются отдельно и здесь не переписываются.
type RSIReport struct {
	Period      int                `json:"period"`
	SMAPeriod   int                `json:"sma_period"`
	Regime      string             `json:"regime"`
	ZoneLow     float64            `json:"zone_low"`
	ZoneHigh    float64            `json:"zone_high"`
	ZoneNote    string             `json:"zone_note"`
	Value       float64            `json:"value"`
	Slope       float64            `json:"slope"`
	Accel       float64            `json:"acceleration"`
	SMA         float64            `json:"sma"`
	Dist30      float64            `json:"dist_30"`
	Dist40      float64            `json:"dist_40"`
	Dist50      float64            `json:"dist_50"`
	Dist60      float64            `json:"dist_60"`
	Dist70      float64            `json:"dist_70"`
	Cross30     string             `json:"cross_30"`
	Cross40     string             `json:"cross_40"`
	Cross50     string             `json:"cross_50"`
	Cross60     string             `json:"cross_60"`
	Cross70     string             `json:"cross_70"`
	SMACross    string             `json:"sma_cross"`
	TimeAbove70 int                `json:"time_above_70"`
	TimeBelow30 int                `json:"time_below_30"`
	MFI         float64            `json:"mfi"`
	MFISlope    float64            `json:"mfi_slope"`
	MFIRSISplit string             `json:"mfi_rsi_split"`
	Divergence  string             `json:"divergence"`
	DivBoundary bool               `json:"div_at_boundary"`
	MultiTF     map[string]float64 `json:"multi_tf,omitempty"`
	MultiBias   string             `json:"multi_bias"`
	Scenarios   []RSIScenario      `json:"scenarios"`
	Verdict     string             `json:"verdict"`
	AllowLong   bool               `json:"allow_long"`
	AllowShort  bool               `json:"allow_short"`
	Notes       []string           `json:"notes"`
}

// AnalyzeRSI собирает признаки и три сценария по одному ряду свечей.
func AnalyzeRSI(in RSIInput) RSIReport {
	period, smaP := in.Period, in.SMAPeriod
	if period <= 0 {
		period = rsiDefaultPeriod
	}
	if smaP <= 0 {
		smaP = rsiDefaultSMA
	}
	regime := normalizeRSIRegime(in.Regime)
	zoneLow, zoneHigh, zoneNote := rsiZones(regime)
	rep := RSIReport{
		Period:     period,
		SMAPeriod:  smaP,
		Regime:     regime,
		ZoneLow:    zoneLow,
		ZoneHigh:   zoneHigh,
		ZoneNote:   zoneNote,
		AllowLong:  true,
		AllowShort: true,
		MultiTF:    in.MultiTF,
	}
	if len(in.Closes) < period+2 {
		rep.Notes = append(rep.Notes, "мало свечей для RSI — фильтр выключен, не выдумываю значения")
		rep.Verdict = "RSI недоступен: недостаточно закрытых свечей."
		return rep
	}

	pane := BuildOscillatorPane(in.Highs, in.Lows, in.Closes, in.Volumes, regime, period, smaP, in.Support, in.Resistance, in.ATR)
	last := len(in.Closes) - 1
	rep.Value = mathRound(pane.RSI[last], 2)
	if last-1 >= pane.From {
		rep.Slope = mathRound(pane.RSI[last]-pane.RSI[last-1], 2)
	}
	if last-2 >= pane.From {
		prevSlope := pane.RSI[last-1] - pane.RSI[last-2]
		rep.Accel = mathRound(rep.Slope-prevSlope, 2)
	}
	if last >= pane.SMAFrom && pane.SMAFrom >= 0 {
		rep.SMA = mathRound(pane.SMA[last], 2)
		if last-1 >= pane.SMAFrom {
			rep.SMACross = crossed(pane.RSI[last-1], pane.RSI[last], pane.SMA[last-1], pane.SMA[last])
		}
	}
	rep.Dist30 = mathRound(rep.Value-30, 2)
	rep.Dist40 = mathRound(rep.Value-40, 2)
	rep.Dist50 = mathRound(rep.Value-50, 2)
	rep.Dist60 = mathRound(rep.Value-60, 2)
	rep.Dist70 = mathRound(rep.Value-70, 2)
	if last-1 >= pane.From {
		prev := pane.RSI[last-1]
		rep.Cross30 = levelCross(prev, pane.RSI[last], 30)
		rep.Cross40 = levelCross(prev, pane.RSI[last], 40)
		rep.Cross50 = levelCross(prev, pane.RSI[last], 50)
		rep.Cross60 = levelCross(prev, pane.RSI[last], 60)
		rep.Cross70 = levelCross(prev, pane.RSI[last], 70)
	}
	rep.TimeAbove70 = consecFromEnd(pane.RSI, pane.From, func(v float64) bool { return v > rsiExtremeHigh })
	rep.TimeBelow30 = consecFromEnd(pane.RSI, pane.From, func(v float64) bool { return v < rsiExtremeLow })

	if pane.MFIFrom >= 0 && last >= pane.MFIFrom {
		rep.MFI = mathRound(pane.MFI[last], 2)
		if last-1 >= pane.MFIFrom {
			rep.MFISlope = mathRound(pane.MFI[last]-pane.MFI[last-1], 2)
		}
		rep.MFIRSISplit = mfiSplit(rep.Value, rep.Slope, rep.MFI, rep.MFISlope)
	} else {
		rep.Notes = append(rep.Notes, "MFI нет: в свечах нет объёма")
	}

	div, atBound := detectDivergence(in.Highs, in.Lows, pane.RSI, pane.From, in.Support, in.Resistance, in.ATR)
	rep.Divergence = div
	rep.DivBoundary = atBound
	rep.MultiBias = multiTFBias(in.MultiTF)
	rep.Scenarios = []RSIScenario{
		scenarioDivergence(div, atBound),
		scenarioTrendFilter(regime, pane.RSI, pane.From, last),
		scenarioExtremeReturn(regime, rep.Cross70, rep.Cross30, rep.Value),
	}
	rep.AllowLong, rep.AllowShort, rep.Verdict = rsiVerdict(regime, rep)
	return rep
}

// BuildOscillatorPane считает ряды и метки для графика. period/smaP ≤ 0 → дефолт 14/9.
func BuildOscillatorPane(highs, lows, closes, vols []float64, regime string, period, smaP int, support, resistance, atr float64) OscillatorPane {
	if period <= 0 {
		period = rsiDefaultPeriod
	}
	if smaP <= 0 {
		smaP = rsiDefaultSMA
	}
	regime = normalizeRSIRegime(regime)
	zoneLow, zoneHigh, _ := rsiZones(regime)
	n := len(closes)
	pane := OscillatorPane{
		Period:    period,
		SMAPeriod: smaP,
		From:      -1,
		SMAFrom:   -1,
		MFIFrom:   -1,
		Regime:    regime,
		ZoneLow:   zoneLow,
		ZoneHigh:  zoneHigh,
		RSI:       make([]float64, n),
		SMA:       make([]float64, n),
		MFI:       make([]float64, n),
	}
	rsi, from := RSISeries(closes, period)
	if rsi == nil {
		return pane
	}
	pane.RSI, pane.From = rsi, from
	smaFrom := from + smaP - 1
	if smaFrom < n {
		pane.SMAFrom = smaFrom
		for i := smaFrom; i < n; i++ {
			sum := 0.0
			for j := i - smaP + 1; j <= i; j++ {
				sum += rsi[j]
			}
			pane.SMA[i] = sum / float64(smaP)
		}
	}
	mfi, mFrom := MFISeries(highs, lows, closes, vols, period)
	if mfi != nil {
		pane.MFI, pane.MFIFrom = mfi, mFrom
	}
	pane.Marks = collectOscMarks(highs, lows, closes, pane, support, resistance, atr)
	return pane
}

// MFISeries — Money Flow Index (RSI, взвешенный объёмом типичной цены). Без объёма
// ряд пустой: нули здесь нельзя выдавать за «нейтральный MFI», это была бы ложь.
func MFISeries(highs, lows, closes, vols []float64, period int) ([]float64, int) {
	n := len(closes)
	if period <= 0 || n < period+1 || len(highs) != n || len(lows) != n || len(vols) != n {
		return nil, -1
	}
	volSum := 0.0
	for _, v := range vols {
		volSum += v
	}
	if volSum <= 0 {
		return nil, -1
	}
	tp := make([]float64, n)
	for i := 0; i < n; i++ {
		tp[i] = (highs[i] + lows[i] + closes[i]) / 3
	}
	pos := make([]float64, n)
	neg := make([]float64, n)
	for i := 1; i < n; i++ {
		mf := tp[i] * vols[i]
		switch {
		case tp[i] > tp[i-1]:
			pos[i] = mf
		case tp[i] < tp[i-1]:
			neg[i] = mf
		}
	}
	out := make([]float64, n)
	var pSum, nSum float64
	for i := 1; i <= period; i++ {
		pSum += pos[i]
		nSum += neg[i]
	}
	out[period] = mfiFrom(pSum, nSum)
	for i := period + 1; i < n; i++ {
		pSum += pos[i] - pos[i-period]
		nSum += neg[i] - neg[i-period]
		out[i] = mfiFrom(pSum, nSum)
	}
	return out, period
}

func mfiFrom(pos, neg float64) float64 {
	if neg == 0 {
		if pos == 0 {
			return 50
		}
		return 100
	}
	return 100 - 100/(1+pos/neg)
}

func normalizeRSIRegime(s string) string {
	switch s {
	case "trend_up", "bullish", "up", "long":
		return "trend_up"
	case "trend_down", "bearish", "down", "short", "panic":
		return "trend_down"
	case "range", "wait/range":
		return "range"
	default:
		// squeeze / volatile / transition — не чистый боковик: уровни как у диапазона,
		// но возврат из экстремума как сигнал не берём (это проверяет scenarioExtremeReturn).
		return s
	}
}

func rsiZones(regime string) (low, high float64, note string) {
	switch regime {
	case "trend_up":
		return 40, 80, "в бычьем тренде рабочий диапазон 40–80: зона 40–50 держит импульс, 80 — перегрев, не шорт"
	case "trend_down":
		return 20, 60, "в медвежьем тренде рабочий диапазон 20–60: отскоки упираются в 50–60, 20 — перепроданность, не лонг"
	default:
		return 30, 70, "в диапазоне (и в переходных режимах) уровни 30/70; сигнал — возврат из зоны, не нахождение в ней"
	}
}

func levelCross(prev, curr, level float64) string {
	if prev < level && curr >= level {
		return "up"
	}
	if prev > level && curr <= level {
		return "down"
	}
	return ""
}

func crossed(prevA, currA, prevB, currB float64) string {
	if prevA < prevB && currA >= currB {
		return "up"
	}
	if prevA > prevB && currA <= currB {
		return "down"
	}
	return ""
}

func consecFromEnd(s []float64, from int, pred func(float64) bool) int {
	if from < 0 || len(s) == 0 {
		return 0
	}
	n := 0
	for i := len(s) - 1; i >= from; i-- {
		if !pred(s[i]) {
			break
		}
		n++
	}
	return n
}

func mfiSplit(rsi, rsiSlope, mfi, mfiSlope float64) string {
	if rsi > 55 && rsiSlope > 0 && mfiSlope < 0 {
		return "volume_lag"
	}
	if rsi < 45 && rsiSlope < 0 && mfiSlope > 0 {
		return "volume_lag"
	}
	if (rsiSlope > 0 && mfiSlope > 0) || (rsiSlope < 0 && mfiSlope < 0) {
		return "agree"
	}
	return ""
}

func multiTFBias(tf map[string]float64) string {
	if len(tf) == 0 {
		return ""
	}
	high, low := 0, 0
	for _, v := range tf {
		if v >= 60 {
			high++
		} else if v <= 40 {
			low++
		}
	}
	switch {
	case high == len(tf):
		return "bullish"
	case low == len(tf):
		return "bearish"
	default:
		return "mixed"
	}
}

func detectDivergence(highs, lows, rsi []float64, from int, support, resistance, atr float64) (kind string, atBoundary bool) {
	n := len(rsi)
	if from < 0 || n < from+10 || len(highs) != n || len(lows) != n {
		return "", false
	}
	start := n - rsiDivLookback
	if start < from+rsiSwingPad {
		start = from + rsiSwingPad
	}
	hi := swings(highs, start, n, true)
	lo := swings(lows, start, n, false)
	if len(hi) >= 2 {
		a, b := hi[len(hi)-2], hi[len(hi)-1]
		if highs[b] > highs[a] && rsi[b] < rsi[a]-rsiDivMinDelta {
			return "bearish", nearStructure(highs[b], support, resistance, atr, highs, start, n, true)
		}
	}
	if len(lo) >= 2 {
		a, b := lo[len(lo)-2], lo[len(lo)-1]
		if lows[b] < lows[a] && rsi[b] > rsi[a]+rsiDivMinDelta {
			return "bullish", nearStructure(lows[b], support, resistance, atr, lows, start, n, false)
		}
	}
	return "", false
}

func swings(v []float64, start, n int, highs bool) []int {
	var out []int
	for i := start + rsiSwingPad; i < n-rsiSwingPad; i++ {
		ok := true
		for k := 1; k <= rsiSwingPad && ok; k++ {
			if highs {
				if v[i-k] > v[i] || v[i+k] >= v[i] {
					ok = false
				}
			} else if v[i-k] < v[i] || v[i+k] <= v[i] {
				ok = false
			}
		}
		if ok {
			out = append(out, i)
		}
	}
	return out
}

func nearStructure(price, support, resistance, atr float64, ext []float64, start, n int, high bool) bool {
	if atr > 0 {
		if resistance > 0 && math.Abs(price-resistance)/atr <= rsiNearLevelATR {
			return true
		}
		if support > 0 && math.Abs(price-support)/atr <= rsiNearLevelATR {
			return true
		}
	}
	if len(ext) == 0 || start >= n {
		return false
	}
	m := ext[start]
	for _, x := range ext[start:n] {
		if high && x > m {
			m = x
		}
		if !high && x < m {
			m = x
		}
	}
	if m == 0 {
		return math.Abs(price-m) < 1e-12
	}
	return math.Abs(price-m)/math.Abs(m) <= 0.002
}

func scenarioDivergence(div string, atBound bool) RSIScenario {
	s := RSIScenario{ID: "divergence", Status: "none", Text: "классической дивергенции нет"}
	if div == "" {
		return s
	}
	s.Active = true
	if div == "bearish" {
		s.Side = "short"
	} else {
		s.Side = "long"
	}
	if !atBound {
		s.Status = "noise"
		s.Text = "дивергенция " + div + " посреди движения — шум, игнорировать (не на границе структуры)"
		return s
	}
	s.Status = "signal"
	s.Text = "дивергенция " + div + " на границе структуры: цена обновила экстремум, RSI — нет. Это фильтр к развороту, не вход сам по себе"
	return s
}

func scenarioTrendFilter(regime string, rsi []float64, from, last int) RSIScenario {
	s := RSIScenario{ID: "trend_filter", Status: "n/a", Text: "фильтр 40–60 работает только в тренде"}
	if last < from {
		return s
	}
	v := rsi[last]
	held := func(below, bars int) bool {
		if last-bars+1 < from {
			return false
		}
		for i := last - bars + 1; i <= last; i++ {
			if below == 1 && rsi[i] >= 40 {
				return false
			}
			if below == 0 && rsi[i] <= 60 {
				return false
			}
		}
		return true
	}
	switch regime {
	case "trend_up":
		s.Active = true
		switch {
		case held(1, 2):
			s.Status, s.Side = "broken", "cut"
			s.Text = "RSI закрепился под 40 — восходящая структура теряет силу ещё до слома цены; сократить или подтянуть стоп"
		case v >= 40 && v <= 55 && last-1 >= from && rsi[last] > rsi[last-1]:
			s.Status, s.Side = "healthy", "hold"
			s.Text = "отскок RSI от зоны 40–50: тренд живой, позицию держать, не шортить"
		case v > 70:
			s.Status, s.Side = "strength", "hold"
			s.Text = "RSI выше 70 в бычьем тренде — сила покупателей, не сигнал шортить"
		default:
			s.Status = "neutral"
			s.Text = "RSI внутри бычьего диапазона 40–80, структура не сломана"
		}
	case "trend_down":
		s.Active = true
		switch {
		case held(0, 2):
			s.Status, s.Side = "broken", "cut"
			s.Text = "RSI закрепился над 60 — нисходящая структура выдыхается; сократить шорт или подтянуть стоп"
		case v >= 45 && v <= 60 && last-1 >= from && rsi[last] < rsi[last-1]:
			s.Status, s.Side = "healthy", "hold"
			s.Text = "отбой RSI от зоны 50–60: нисходящий тренд живой, не лонговать от «перепроданности»"
		case v < 30:
			s.Status, s.Side = "strength", "hold"
			s.Text = "RSI ниже 30 в медвежьем тренде — сила продавцов, не сигнал лонговать"
		default:
			s.Status = "neutral"
			s.Text = "RSI внутри медвежьего диапазона 20–60, структура не сломана"
		}
	default:
		s.Text = "режим не трендовый — фильтр 40–60 не применяется"
	}
	return s
}

func scenarioExtremeReturn(regime, cross70, cross30 string, value float64) RSIScenario {
	s := RSIScenario{ID: "extreme_return", Status: "n/a", Text: "возврат из экстремума почти только для боковика"}
	if regime != "range" {
		if value > 70 || value < 30 {
			s.Text = "RSI в экстремальной зоне, но рынок не в диапазоне — нахождение в зоне не сигнал"
		}
		return s
	}
	s.Active = true
	switch {
	case cross70 == "down":
		s.Status, s.Side = "signal", "short"
		s.Text = "RSI был выше 70 и вернулся под 70 — это событие в боковике, не сам факт нахождения в зоне"
	case cross30 == "up":
		s.Status, s.Side = "signal", "long"
		s.Text = "RSI был ниже 30 и вышел выше 30 — возврат из перепроданности в диапазоне"
	case value > 70:
		s.Status = "wait"
		s.Text = "RSI выше 70 в боковике — ещё не сигнал, ждём возврат под 70"
	case value < 30:
		s.Status = "wait"
		s.Text = "RSI ниже 30 в боковике — ещё не сигнал, ждём выход выше 30"
	default:
		s.Status = "none"
		s.Text = "экстремальной зоны сейчас нет"
	}
	return s
}

func rsiVerdict(regime string, r RSIReport) (allowLong, allowShort bool, verdict string) {
	allowLong, allowShort = true, true
	var parts []string
	for _, sc := range r.Scenarios {
		if sc.ID == "divergence" && sc.Status == "signal" {
			parts = append(parts, sc.Text)
		}
		if sc.ID == "trend_filter" && sc.Status == "broken" {
			parts = append(parts, sc.Text)
		}
		if sc.ID == "extreme_return" && sc.Status == "signal" {
			parts = append(parts, sc.Text)
		}
	}
	if regime == "trend_up" && r.Value >= 70 && r.Slope >= 0 {
		bearDiv := r.Divergence == "bearish" && r.DivBoundary
		if !bearDiv {
			allowShort = false
			parts = append(parts, "SHORT по RSI запрещён: высокий RSI в восходящем тренде — сила, не разворот")
		}
	}
	if regime == "trend_down" && r.Value <= 30 && r.Slope <= 0 {
		bullDiv := r.Divergence == "bullish" && r.DivBoundary
		if !bullDiv {
			allowLong = false
			parts = append(parts, "LONG по RSI запрещён: низкий RSI в нисходящем тренде — сила продавцов, не дно")
		}
	}
	if r.MultiBias == "bullish" && r.Value >= 70 {
		parts = append(parts, "мульти-ТФ импульс вверх: высокий RSI на нескольких ТФ сразу — это continuation, не шорт")
	}
	if r.MultiBias == "bearish" && r.Value <= 30 {
		parts = append(parts, "мульти-ТФ импульс вниз: низкий RSI на нескольких ТФ — continuation, не лонг")
	}
	if r.MFIRSISplit == "volume_lag" {
		parts = append(parts, "RSI и MFI расходятся: импульс цены есть, денег в движении нет — усиливает осторожность, не вход")
	}
	if len(parts) == 0 {
		verdict = "RSI — фильтр, не система. Сейчас подтверждения к развороту нет; смотреть цену и методику, а не уровень 30/70."
		return
	}
	verdict = join(parts, " ")
	return
}

func collectOscMarks(highs, lows, closes []float64, pane OscillatorPane, support, resistance, atr float64) []OscMark {
	from, n := pane.From, len(pane.RSI)
	if from < 0 || n < from+2 {
		return nil
	}
	var marks []OscMark
	push := func(i int, kind string, onPrice bool) {
		price := closes[i]
		if kind == "div_bear" {
			price = highs[i]
		}
		if kind == "div_bull" {
			price = lows[i]
		}
		marks = append(marks, OscMark{Index: i, Kind: kind, Price: price, RSI: pane.RSI[i], OnPrice: onPrice})
	}
	for i := from + 1; i < n; i++ {
		prev, curr := pane.RSI[i-1], pane.RSI[i]
		if c := levelCross(prev, curr, 30); c != "" {
			push(i, "cross_30_"+c, c == "up")
		}
		if c := levelCross(prev, curr, 40); c != "" {
			push(i, "cross_40_"+c, pane.Regime == "trend_up" && c == "down")
		}
		if c := levelCross(prev, curr, 50); c != "" {
			push(i, "cross_50_"+c, false)
		}
		if c := levelCross(prev, curr, 60); c != "" {
			push(i, "cross_60_"+c, pane.Regime == "trend_down" && c == "up")
		}
		if c := levelCross(prev, curr, 70); c != "" {
			push(i, "cross_70_"+c, c == "down")
		}
		if i-1 >= pane.SMAFrom && pane.SMAFrom >= 0 {
			if c := crossed(prev, curr, pane.SMA[i-1], pane.SMA[i]); c != "" {
				push(i, "sma_cross_"+c, false)
			}
		}
	}
	// Дивергенция — одна метка на последнем свинге, иначе график покроется ложными вершинами.
	if div, atBound := detectDivergence(highs, lows, pane.RSI, from, support, resistance, atr); div != "" {
		kind := "div_bear"
		if div == "bullish" {
			kind = "div_bull"
		}
		hi := swings(highs, maxInt(from+rsiSwingPad, n-rsiDivLookback), n, true)
		lo := swings(lows, maxInt(from+rsiSwingPad, n-rsiDivLookback), n, false)
		idx := -1
		if div == "bearish" && len(hi) > 0 {
			idx = hi[len(hi)-1]
		}
		if div == "bullish" && len(lo) > 0 {
			idx = lo[len(lo)-1]
		}
		if idx >= 0 {
			if !atBound {
				kind += "_noise"
			}
			push(idx, kind, atBound)
		}
	}
	return marks
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
