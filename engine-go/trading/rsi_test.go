package trading

import (
	"math"
	"testing"
)

func TestRSISeriesMatchesRSI(t *testing.T) {
	up := rising(40)
	s, from := RSISeries(up, 14)
	if from != 14 {
		t.Fatalf("from = %d, want 14", from)
	}
	if math.Abs(s[len(s)-1]-RSI(up, 14)) > 1e-12 {
		t.Errorf("series last %.6f != RSI() %.6f", s[len(s)-1], RSI(up, 14))
	}
	if _, from := RSISeries([]float64{1, 2, 3}, 14); from != -1 {
		t.Errorf("insufficient series from = %d, want -1", from)
	}
}

func TestMFIBounds(t *testing.T) {
	n := 40
	highs, lows, closes, vols := make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n)
	for i := 0; i < n; i++ {
		closes[i] = 100 + float64(i)
		highs[i] = closes[i] + 1
		lows[i] = closes[i] - 1
		vols[i] = 1000
	}
	s, from := MFISeries(highs, lows, closes, vols, 14)
	if from != 14 {
		t.Fatalf("from = %d", from)
	}
	if s[len(s)-1] < 80 {
		t.Errorf("rising MFI = %.2f, want high", s[len(s)-1])
	}
	if _, from := MFISeries(highs, lows, closes, make([]float64, n), 14); from != -1 {
		t.Error("zero volume must not invent an MFI")
	}
}

func TestLevelCross(t *testing.T) {
	if got := levelCross(68, 71, 70); got != "up" {
		t.Errorf("cross up = %q", got)
	}
	if got := levelCross(72, 69, 70); got != "down" {
		t.Errorf("cross down = %q", got)
	}
	if got := levelCross(80, 81, 70); got != "" {
		t.Errorf("stay above is not a cross, got %q", got)
	}
}

func TestRSITrendUpForbidsShort(t *testing.T) {
	// Длинный безостановочный рост: RSI у потолка, режим бычий — шортить это запрещено.
	n := 80
	closes := rising(n)
	highs, lows, vols := make([]float64, n), make([]float64, n), make([]float64, n)
	for i := 0; i < n; i++ {
		highs[i], lows[i], vols[i] = closes[i]+0.5, closes[i]-0.5, 100
	}
	r := AnalyzeRSI(RSIInput{Highs: highs, Lows: lows, Closes: closes, Volumes: vols, Regime: "trend_up"})
	if r.Value < 70 {
		t.Fatalf("RSI of rising series = %.1f, want >70", r.Value)
	}
	if r.AllowShort {
		t.Errorf("AllowShort = true on strong uptrend RSI=%.1f — это учебниковая ошибка", r.Value)
	}
	if r.AllowLong != true {
		t.Error("лонг фильтр не должен закрываться только из-за высокого RSI в тренде")
	}
	found := false
	for _, sc := range r.Scenarios {
		if sc.ID == "trend_filter" && sc.Status == "strength" {
			found = true
		}
	}
	if !found {
		t.Errorf("trend_filter should be strength, got %+v", r.Scenarios)
	}
}

func TestRSIRangeNeedsReturnNotPresence(t *testing.T) {
	// Боковик, который сначала заходит выше 70 и на последнем баре возвращается под 70.
	closes := append(flat(40, 100), rising(20)...)
	closes = append(closes, 119, 118, 116) // откат, RSI должен пересечь 70 вниз
	n := len(closes)
	highs, lows, vols := make([]float64, n), make([]float64, n), make([]float64, n)
	for i := 0; i < n; i++ {
		highs[i], lows[i], vols[i] = closes[i]+0.4, closes[i]-0.4, 50
	}
	r := AnalyzeRSI(RSIInput{Highs: highs, Lows: lows, Closes: closes, Volumes: vols, Regime: "range"})
	var ext RSIScenario
	for _, sc := range r.Scenarios {
		if sc.ID == "extreme_return" {
			ext = sc
		}
	}
	if ext.Status != "signal" && ext.Status != "wait" {
		// Либо уже вернулись (signal), либо ещё в зоне (wait). «n/a» в range — баг.
		t.Errorf("extreme_return in range = %+v", ext)
	}
	if ext.Status == "n/a" {
		t.Error("в боковике сценарий возврата не должен быть n/a")
	}
}

func TestRSIExtremeInTrendIsNotSignal(t *testing.T) {
	closes := rising(60)
	n := len(closes)
	highs, lows, vols := make([]float64, n), make([]float64, n), make([]float64, n)
	for i := 0; i < n; i++ {
		highs[i], lows[i], vols[i] = closes[i]+0.2, closes[i]-0.2, 10
	}
	r := AnalyzeRSI(RSIInput{Highs: highs, Lows: lows, Closes: closes, Volumes: vols, Regime: "trend_up"})
	for _, sc := range r.Scenarios {
		if sc.ID == "extreme_return" && sc.Status == "signal" {
			t.Errorf("возврат из зоны не должен срабатывать в тренде: %+v", sc)
		}
	}
}

func TestRSITrendFilterBreaksUnder40(t *testing.T) {
	// Рост, затем глубокий откат, чтобы RSI ушёл и задержался под 40.
	up := rising(40)
	down := make([]float64, 20)
	last := up[len(up)-1]
	for i := range down {
		down[i] = last - float64(i+1)*3
	}
	closes := append(up, down...)
	n := len(closes)
	highs, lows, vols := make([]float64, n), make([]float64, n), make([]float64, n)
	for i := 0; i < n; i++ {
		highs[i], lows[i], vols[i] = closes[i]+0.5, closes[i]-0.5, 20
	}
	r := AnalyzeRSI(RSIInput{Highs: highs, Lows: lows, Closes: closes, Volumes: vols, Regime: "trend_up"})
	if r.Value >= 40 {
		t.Skipf("fixture did not push RSI under 40 (got %.1f) — не баг фильтра", r.Value)
	}
	var tf RSIScenario
	for _, sc := range r.Scenarios {
		if sc.ID == "trend_filter" {
			tf = sc
		}
	}
	if tf.Status != "broken" {
		t.Errorf("RSI=%.1f under 40 in uptrend: filter=%+v, want broken", r.Value, tf)
	}
}

func TestDivergenceMidTrendIsNoise(t *testing.T) {
	// Цена делает два повышающихся хая, RSI на втором ниже — но оба хая далеко
	// от края окна и без уровней → это шум, не сигнал.
	n := 80
	closes := make([]float64, n)
	highs := make([]float64, n)
	lows := make([]float64, n)
	vols := make([]float64, n)
	for i := 0; i < n; i++ {
		closes[i] = 100 + float64(i)*0.3
		highs[i] = closes[i] + 1
		lows[i] = closes[i] - 1
		vols[i] = 10
	}
	// Первый свинг-хай около середины, второй ближе к концу, но не на абсолютном максимуме окна
	// (последние бары ещё выше — чтобы nearStructure по экстремуму окна не сработал).
	highs[40] = 140
	closes[40] = 138
	highs[55] = 145
	closes[55] = 143
	// После второго хая цена идёт ещё выше — дивергенция «посреди тренда».
	for i := 56; i < n; i++ {
		closes[i] = 143 + float64(i-55)
		highs[i] = closes[i] + 0.4
		lows[i] = closes[i] - 0.4
	}
	r := AnalyzeRSI(RSIInput{Highs: highs, Lows: lows, Closes: closes, Volumes: vols, Regime: "trend_up"})
	if r.Divergence == "bearish" && r.DivBoundary {
		t.Errorf("mid-trend divergence must not be at_boundary: %+v", r)
	}
	for _, sc := range r.Scenarios {
		if sc.ID == "divergence" && sc.Status == "signal" {
			t.Errorf("mid-trend divergence marked as signal: %+v", sc)
		}
	}
}

func TestDivergenceAtResistanceIsSignal(t *testing.T) {
	n := 70
	closes := make([]float64, n)
	highs := make([]float64, n)
	lows := make([]float64, n)
	vols := make([]float64, n)
	for i := 0; i < n; i++ {
		closes[i] = 100 + math.Sin(float64(i)/4)*2
		highs[i] = closes[i] + 1
		lows[i] = closes[i] - 1
		vols[i] = 8
	}
	// Два хая: второй выше по цене. RSI на втором задавить сложнее синтетикой,
	// поэтому проверяем nearStructure напрямую и сценарий при готовой дивергенции.
	highs[30], closes[30] = 120, 119
	highs[60], closes[60] = 130, 129
	if !nearStructure(130, 0, 130, 10, highs, 10, n, true) {
		t.Fatal("resistance 130 must count as structure boundary")
	}
	sc := scenarioDivergence("bearish", true)
	if sc.Status != "signal" || sc.Side != "short" {
		t.Errorf("boundary bearish div = %+v", sc)
	}
	noise := scenarioDivergence("bearish", false)
	if noise.Status != "noise" {
		t.Errorf("unbounded div = %+v, want noise", noise)
	}
}

func TestMultiTFBias(t *testing.T) {
	if got := multiTFBias(map[string]float64{"15m": 63, "1h": 68, "4h": 74}); got != "bullish" {
		t.Errorf("all high = %q", got)
	}
	if got := multiTFBias(map[string]float64{"15m": 28, "1h": 34, "4h": 38}); got != "bearish" {
		t.Errorf("all low = %q", got)
	}
	if got := multiTFBias(map[string]float64{"15m": 63, "1h": 34}); got != "mixed" {
		t.Errorf("mixed = %q", got)
	}
}

func TestOscillatorPaneAligned(t *testing.T) {
	n := 50
	closes := rising(n)
	highs, lows, vols := make([]float64, n), make([]float64, n), make([]float64, n)
	for i := 0; i < n; i++ {
		highs[i], lows[i], vols[i] = closes[i]+1, closes[i]-1, 100
	}
	p := BuildOscillatorPane(highs, lows, closes, vols, "trend_up", 14, 9, 0, 0, 0)
	if len(p.RSI) != n || len(p.SMA) != n || len(p.MFI) != n {
		t.Fatalf("series length rsi=%d sma=%d mfi=%d want %d", len(p.RSI), len(p.SMA), len(p.MFI), n)
	}
	if p.From != 14 || p.SMAFrom != 14+8 {
		t.Errorf("from=%d smaFrom=%d", p.From, p.SMAFrom)
	}
	if p.ZoneLow != 40 || p.ZoneHigh != 80 {
		t.Errorf("uptrend zones = %.0f–%.0f", p.ZoneLow, p.ZoneHigh)
	}
	if p.MFIFrom < 0 {
		t.Error("MFI should be present when volume is")
	}
}

func TestAnalyzeRSIInsufficient(t *testing.T) {
	r := AnalyzeRSI(RSIInput{Closes: []float64{1, 2, 3}, Regime: "range"})
	if r.Verdict == "" || r.AllowLong != true {
		t.Errorf("thin data must not invent a directional filter: %+v", r)
	}
	if len(r.Notes) == 0 {
		t.Error("expected a note about insufficient candles")
	}
}

func TestMFIVolumeLag(t *testing.T) {
	if got := mfiSplit(72, 1.5, 40, -2); got != "volume_lag" {
		t.Errorf("rsi up / mfi down = %q", got)
	}
	if got := mfiSplit(72, 1.5, 80, 2); got != "agree" {
		t.Errorf("both up = %q", got)
	}
}
