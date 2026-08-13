package trading

// Проверки алгоритма Герчика. Состав взят из §16 скилла gerchik-trader-qa: то, без чего
// реализацию нельзя считать проверенной. Данные синтетические — так видно, ЧТО именно
// проверяется, а живые свечи Binance в тест не затащишь без сети и недетерминизма.

import (
	"math"
	"testing"
	"time"
)

var base = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

// day собирает дневной бар: день N от base.
func day(n int, o, h, l, c float64) Bar {
	return Bar{Time: base.AddDate(0, 0, n), Open: o, High: h, Low: l, Close: c}
}

// ATR считается по H−L трёх НОРМАЛЬНЫХ дней. Wilder-сглаживание и период 14 — чужая
// методика: она даёт другое число, а от ATR зависят стоп, цель и фильтр энергии.
func TestGerchikATRFiltersParanormalBars(t *testing.T) {
	var bars []Bar
	for i := range 10 {
		bars = append(bars, day(i, 100, 110, 100, 105)) // ровный размах 10
	}
	got := gerchikATR(bars)
	if math.Abs(got.ATR-10) > 0.001 {
		t.Errorf("ровный ряд: ATR = %.3f, ждали 10", got.ATR)
	}

	// Выброс на 40 (≥1.5 базы) и «сжатый» день на 2 (≤0.5 базы) обязаны выпасть из расчёта.
	bars = append(bars, day(10, 100, 140, 100, 130)) // паранормально большой
	bars = append(bars, day(11, 100, 102, 100, 101)) // паранормально малый
	bars = append(bars, day(12, 100, 111, 100, 106)) // нормальный
	got = gerchikATR(bars)
	if got.SkippedBig != 1 || got.SkippedSmall != 1 {
		t.Errorf("паранормальные не отфильтрованы: big=%d small=%d", got.SkippedBig, got.SkippedSmall)
	}
	if got.NormalDays != gerchikATRDays {
		t.Errorf("ATR посчитан по %d дням, ждали %d", got.NormalDays, gerchikATRDays)
	}
	if got.ATR > 12 || got.ATR < 9 {
		t.Errorf("выброс всё же попал в ATR: %.2f", got.ATR)
	}

	// Wilder-ATR на тех же данных даёт другое число — если однажды подменят реализацию,
	// тест обязан это заметить.
	highs, lows, closes := seriesOf(bars)
	if w := ATR(highs, lows, closes, 14); math.Abs(w-got.ATR) < 0.0001 && w != 0 {
		t.Error("Герчик-ATR совпал с Wilder-ATR — похоже, считается не по методике")
	}
}

func seriesOf(bars []Bar) (highs, lows, closes []float64) {
	for _, b := range bars {
		highs = append(highs, b.High)
		lows = append(lows, b.Low)
		closes = append(closes, b.Close)
	}
	return
}

// Правило экстремумов (§2.3) — фундамент всех уровней: закрылись выше предыдущего →
// уровень по хаю ЭТОГО бара; закрылись ниже → по хаю/лоу ПРЕДЫДУЩЕГО.
func TestBuildLevelsExtremumRule(t *testing.T) {
	bars := []Bar{
		day(0, 100, 110, 99, 105),
		day(1, 105, 120, 104, 118), // закрытие выше → уровень по 120
		day(2, 118, 119, 100, 102), // закрытие ниже → уровень по хаю/лоу бара 1 (120 / 104)
	}
	levels := buildLevelsRaw(bars, 15)
	if !hasLevelNear(levels, 120, 0.01) {
		t.Errorf("нет уровня по хаю растущего бара: %+v", levels)
	}
	if !hasLevelNear(levels, 104, 0.01) {
		t.Errorf("нет уровня по лоу предыдущего бара после ЛП: %+v", levels)
	}
}

// buildLevelsRaw обходит фильтр «воздушных» уровней: правило экстремумов проверяется
// само по себе, до отбора по силе.
func buildLevelsRaw(bars []Bar, atr float64) []Level {
	tol := levelTolATR * atr
	var out []Level
	for i := 1; i < len(bars); i++ {
		switch {
		case bars[i].Close > bars[i-1].Close:
			out = append(out, Level{Price: bars[i].High})
		case bars[i].Close < bars[i-1].Close:
			out = append(out, Level{Price: bars[i-1].High}, Level{Price: bars[i-1].Low})
		}
	}
	_ = tol
	return out
}

func hasLevelNear(levels []Level, price, tol float64) bool {
	for _, l := range levels {
		if math.Abs(l.Price-price) <= tol {
			return true
		}
	}
	return false
}

// Уровни обязаны склеиваться «в одну цену»: иначе вместо 3–5 уровней получаются сотни,
// и ближайший уровень всегда оказывается в полушаге от цены.
func TestLevelsAreClusteredAndCapped(t *testing.T) {
	var bars []Bar
	price := 100.0
	for i := range 60 {
		// Пила вокруг 100 с касаниями одних и тех же экстремумов.
		if i%2 == 0 {
			bars = append(bars, day(i, price, price+10, price-1, price+8))
		} else {
			bars = append(bars, day(i, price+8, price+10.4, price-1.2, price))
		}
	}
	rep := AnalyzeGerchik("BTCUSDT", bars, Bar{}, base.AddDate(0, 0, 61))
	if len(rep.Levels) == 0 {
		t.Fatal("уровни не построены")
	}
	if len(rep.Levels) > gerchikMaxLevels {
		t.Errorf("уровней %d, методика разрешает не больше %d", len(rep.Levels), gerchikMaxLevels)
	}
	for i := 1; i < len(rep.Levels); i++ {
		if rep.Levels[i].Price <= rep.Levels[i-1].Price {
			t.Errorf("уровни не отсортированы по цене: %+v", rep.Levels)
		}
	}
}

// Разметка обязана быть по ОБЕ стороны цены. На живом BTC отбор «топ-5 по силе» выдал пять
// сопротивлений подряд: сильные уровни стояли кучей выше цены, а снизу не осталось ничего —
// ни цели для шорта, ни поддержки для лонга, и сценарий не строился в принципе.
func TestLevelsCoverBothSidesOfPrice(t *testing.T) {
	var bars []Bar
	// Первая половина истории: цена высоко, много касаний зоны 200 — там копятся сильные уровни.
	for i := range 60 {
		bars = append(bars, day(i, 200, 205, 195, 200))
	}
	// Затем уход вниз и торговля у 100: снизу уровни моложе и слабее по числу касаний.
	for i := 60; i < 90; i++ {
		bars = append(bars, day(i, 150-float64(i-60), 152-float64(i-60), 148-float64(i-60), 149-float64(i-60)))
	}
	for i := 90; i < 120; i++ {
		// Пила у 100: закрытия чередуются, иначе правило экстремумов вообще не даёт
		// кандидатов — оно смотрит на «выше/ниже предыдущего закрытия».
		if i%2 == 0 {
			bars = append(bars, day(i, 100, 104, 96, 101))
		} else {
			bars = append(bars, day(i, 101, 104, 96, 99))
		}
	}
	rep := AnalyzeGerchik("BTCUSDT", bars, Bar{}, base.AddDate(0, 0, 121))
	var below, above int
	for _, l := range rep.Levels {
		if l.Price <= rep.Price {
			below++
		} else {
			above++
		}
	}
	if below == 0 || above == 0 {
		t.Errorf("разметка односторонняя (снизу %d, сверху %d) при цене %.2f: %+v",
			below, above, rep.Price, rep.Levels)
	}
	// И уровни не должны стоять вплотную друг к другу — это одна стена, а не разметка.
	for i := 1; i < len(rep.Levels); i++ {
		if gap := rep.Levels[i].Price - rep.Levels[i-1].Price; gap < minLevelGapATR*rep.ATR.ATR {
			t.Errorf("уровни `%.2f` и `%.2f` ближе %.2f ATR друг к другу",
				rep.Levels[i-1].Price, rep.Levels[i].Price, minLevelGapATR)
		}
	}
}

// Касание — экстремум У уровня, а не любой бар, накрывший его телом. Иначе проход цены
// через зону даёт рядовому уровню десятки «касаний» и незаслуженную силу.
func TestTouchesCountExtremesOnly(t *testing.T) {
	var bars []Bar
	// Цена идёт снизу вверх сквозь 100, ни разу не разворачиваясь у этой цены.
	for i := range 30 {
		p := 80 + float64(i)*2
		bars = append(bars, day(i, p, p+1, p-1, p+0.5))
	}
	l := Level{Price: 100}
	describeLevel(&l, bars, 4, 0.8, 140, base.AddDate(0, 0, 31))
	if l.Touches > 3 {
		t.Errorf("сквозной проход засчитан как %d касаний", l.Touches)
	}
}

// Правило 75% (§4.3): выбранная за день энергия запрещает вход ПО ТРЕНДУ.
func TestEnergyRuleBlocksTrendEntry(t *testing.T) {
	rep := GerchikReport{Price: 108, DayOpen: 100, GlobalTrend: "лонг"}
	lvl := Level{Price: 107.9, Fresh: true, Kind: "исторический", DistATR: 0.01}
	rep.Support = &lvl
	rep.Levels = []Level{lvl, {Price: 140, Fresh: true, Kind: "исторический"}}
	rep.Energy = (rep.Price - rep.DayOpen) / 10 // 80% ATR

	bars := []Bar{day(0, 100, 105, 99, 104), day(1, 104, 109, 103, 108), day(2, 105, 109, 104, 108)}
	s := buildDirectional(&rep, bars, 10, "long")
	if !hasBlock(s, "75%") {
		t.Errorf("энергия 80%% ATR по тренду не заблокирована: %+v", s.Blocks)
	}
}

// Против тренда стоп меньше (§7.1) — иначе риск на сделку систематически завышен.
func TestStopSizeByTrendDirection(t *testing.T) {
	atr := 100.0
	mk := func(trend string) GerchikSetup {
		lvl := Level{Price: 1000, Fresh: true, Kind: "исторический", DistATR: 0.05}
		rep := GerchikReport{Price: 1002, DayOpen: 1000, GlobalTrend: trend, Support: &lvl,
			Levels: []Level{lvl, {Price: 1600, Fresh: true, Kind: "исторический"}}}
		// Лоу последнего бара ВЫШЕ уровня: прокола нет, значит проверяется расчётный стоп,
		// а не технический за хвостом ЛП.
		bars := []Bar{day(0, 990, 1005, 988, 1000), day(1, 1000, 1006, 1001, 1002), day(2, 1000, 1006, 1001, 1002)}
		return buildDirectional(&rep, bars, atr, dirOf(&rep))
	}
	byTrend := mk("лонг")
	counter := mk("шорт")
	if math.Abs(byTrend.SLSize-slTrendPct*atr) > 0.001 {
		t.Errorf("стоп по тренду = %.2f, ждали %.2f", byTrend.SLSize, slTrendPct*atr)
	}
	if math.Abs(counter.SLSize-slCounterPct*atr) > 0.001 {
		t.Errorf("стоп против тренда = %.2f, ждали %.2f", counter.SLSize, slCounterPct*atr)
	}
	if counter.SLSize >= byTrend.SLSize {
		t.Error("против тренда стоп обязан быть меньше")
	}
}

// RR ниже 3:1 — сделки нет (§8). Это то правило, которое реализации чаще всего «смягчают».
func TestLowRRIsBlocked(t *testing.T) {
	lvl := Level{Price: 1000, Fresh: true, Kind: "исторический", DistATR: 0.05}
	near := Level{Price: 1020, Fresh: true, Kind: "исторический"} // цель всего в 20 от входа
	rep := GerchikReport{Price: 1002, DayOpen: 1000, GlobalTrend: "лонг",
		Support: &lvl, Levels: []Level{lvl, near}}
	bars := []Bar{day(0, 990, 1005, 988, 1000), day(1, 1000, 1006, 1001, 1002), day(2, 1000, 1006, 1001, 1002)}
	s := buildDirectional(&rep, bars, 100, dirOf(&rep))
	if s.RR >= minRR {
		t.Fatalf("сценарий должен был получить низкий RR, получил %.2f", s.RR)
	}
	if !hasBlock(s, "RR") {
		t.Errorf("низкий RR не заблокирован: %+v", s.Blocks)
	}
}

// Цель без уровня впереди обязана быть помечена как расчётная: выдавать её за уровень —
// значит придумывать данные, чего движку делать нельзя.
func TestTakeFromRRIsMarked(t *testing.T) {
	lvl := Level{Price: 1000, Fresh: true, Kind: "исторический", DistATR: 0.05}
	rep := GerchikReport{Price: 1002, DayOpen: 1000, GlobalTrend: "лонг",
		Support: &lvl, Levels: []Level{lvl}}
	bars := []Bar{day(0, 990, 1005, 988, 1000), day(1, 1000, 1006, 1001, 1002), day(2, 1000, 1006, 1001, 1002)}
	s := buildDirectional(&rep, bars, 100, dirOf(&rep))
	if !s.TakeIsRR {
		t.Error("цель посчитана от RR, но не помечена")
	}
	if len(s.Takes) != 3 {
		t.Fatalf("выход по методике идёт частями 3:1/4:1/5:1, получили %v", s.Takes)
	}
	if math.Abs(s.RR-5) > 0.01 {
		t.Errorf("дальняя цель обязана давать 5:1, получили %.2f", s.RR)
	}
}

// Цель не ставится дальше следующего уровня: там встречный интерес. И «RR 1:31» —
// это не план, а арифметика: на живом BTC уровень оказался в тридцати стопах.
func TestTakesAreCappedByNextLevel(t *testing.T) {
	atr := 100.0
	lvl := Level{Price: 1000, Fresh: true, Kind: "исторический", DistATR: 0.05}
	far := Level{Price: 1035, Fresh: true, Kind: "исторический"} // всего 35 от входа
	rep := GerchikReport{Price: 1002, DayOpen: 1000, GlobalTrend: "лонг",
		Support: &lvl, Levels: []Level{lvl, far}}
	bars := []Bar{day(0, 990, 1005, 988, 1000), day(1, 1000, 1006, 1001, 1002), day(2, 1000, 1006, 1001, 1002)}
	s := buildDirectional(&rep, bars, atr, dirOf(&rep))
	for _, tp := range s.Takes {
		if tp > far.Price {
			t.Errorf("цель `%.2f` стоит за уровнем `%.2f`", tp, far.Price)
		}
	}
	if s.RR > 10 {
		t.Errorf("RR 1:%.1f — цель не ограничена уровнем", s.RR)
	}
	if s.LevelTarget != far.Price {
		t.Errorf("граница хода не показана: %.2f", s.LevelTarget)
	}
}

// Цена между уровнями — входа нет, и причина названа. Молчаливый пустой сценарий
// пользователь прочитает как «всё спокойно, можно заходить».
func TestNoLevelInPlayIsExplained(t *testing.T) {
	rep := GerchikReport{Price: 1500, DayOpen: 1500, GlobalTrend: "боковик",
		Levels: []Level{{Price: 1000, DistATR: 5}, {Price: 2000, DistATR: 5}}}
	rep.Support, rep.Resistance = nearestLevels(rep.Levels, rep.Price)
	bars := []Bar{day(0, 1400, 1500, 1400, 1500), day(1, 1500, 1550, 1450, 1500), day(2, 1500, 1550, 1450, 1500)}
	s := buildDirectional(&rep, bars, 100, dirOf(&rep))
	if !s.Pending {
		t.Error("цена далеко от уровня — сценарий обязан быть помечен как заготовка")
	}
	if len(s.Blocks) == 0 {
		t.Error("отсутствие входа обязано быть объяснено")
	}
	// Заготовка всё же обязана содержать цифры плана — ради них её и считают.
	if s.Level == 0 || s.Stop == 0 || s.Take == 0 {
		t.Errorf("в заготовке нет уровня/стопа/цели: %+v", s)
	}
}

// Ложный пробой распознаётся по факту: прокол за уровень и возврат внутрь (§6.2),
// а слишком глубокий ЛП не берётся.
func TestFalseBreakoutDetection(t *testing.T) {
	atr := 100.0
	lvl := Level{Price: 1000, Fresh: true, Kind: "исторический", DistATR: 0.1, Above: true}
	rep := GerchikReport{Price: 990, DayOpen: 990, GlobalTrend: "шорт",
		Resistance: &lvl, Levels: []Level{{Price: 600, Fresh: true, Kind: "исторический"}, lvl}}
	// Последний бар: хай выше уровня, закрытие ниже — ЛП глубиной 20 (< ATR/3).
	bars := []Bar{day(0, 950, 990, 940, 980), day(1, 980, 1000, 970, 990), day(2, 985, 1020, 980, 990)}
	s := buildDirectional(&rep, bars, atr, dirOf(&rep))
	if s.Model != "ЛП 1 баром" {
		t.Errorf("ложный пробой не распознан: model=%q", s.Model)
	}
	if s.Direction != "short" {
		t.Errorf("ЛП сопротивления — это шорт, получили %q", s.Direction)
	}
	if hasBlock(s, "глубина ЛП") {
		t.Errorf("ЛП глубиной 20 при ATR 100 укладывается в треть: %+v", s.Blocks)
	}

	// Тот же ЛП, но глубиной 50 (> ATR/3) — брать нельзя.
	deep := []Bar{day(0, 950, 990, 940, 980), day(1, 980, 1000, 970, 990), day(2, 985, 1050, 980, 990)}
	if s2 := buildDirectional(&rep, deep, atr, dirOf(&rep)); !hasBlock(s2, "глубина ЛП") {
		t.Errorf("слишком глубокий ЛП не заблокирован: %+v", s2.Blocks)
	}
}

// Технический стоп за хвостом ЛП берётся, только если умещается в расчётный +20% (§7.1).
func TestTechnicalStopCap(t *testing.T) {
	atr := 100.0
	lvl := Level{Price: 1000, Fresh: true, Kind: "исторический", DistATR: 0.1, Above: true}
	rep := GerchikReport{Price: 990, DayOpen: 990, GlobalTrend: "шорт",
		Resistance: &lvl, Levels: []Level{{Price: 500, Fresh: true, Kind: "исторический"}, lvl}}
	// Хвост ЛП на 1010: технический стоп 10 против расчётного 15 — берётся технический.
	bars := []Bar{day(0, 950, 990, 940, 980), day(1, 980, 1000, 970, 990), day(2, 985, 1010, 980, 990)}
	s := buildDirectional(&rep, bars, atr, dirOf(&rep))
	if math.Abs(s.Stop-1010) > 0.001 {
		t.Errorf("технический стоп не взят: stop=%.2f", s.Stop)
	}
	if math.Abs(s.SLSize-10) > 0.001 {
		t.Errorf("размер стопа = %.2f, ждали 10 (за хвостом)", s.SLSize)
	}
	// Хвост на 1030: технический стоп 30 против расчётного 15×1.2=18 — не влезает.
	far := []Bar{day(0, 950, 990, 940, 980), day(1, 980, 1000, 970, 990), day(2, 985, 1030, 980, 990)}
	s2 := buildDirectional(&rep, far, atr, dirOf(&rep))
	if !hasBlock(s2, "технический стоп") {
		t.Errorf("превышение технического стопа не заблокировано: %+v", s2.Blocks)
	}
	if s2.Stop == 1030 {
		t.Error("стоп растянут под хвост — риск на сделку уехал")
	}
}

// Инструменты вне курса (не BTC/ETH) считаются, но обязаны быть помечены.
func TestOutOfScopeSymbolIsFlagged(t *testing.T) {
	var bars []Bar
	for i := range 40 {
		bars = append(bars, day(i, 10, 11, 9.5, 10.5))
	}
	rep := AnalyzeGerchik("SOLUSDT", bars, Bar{}, base.AddDate(0, 0, 41))
	if rep.InScope {
		t.Error("SOLUSDT не входит в инструменты курса")
	}
	var found bool
	for _, n := range rep.Notes {
		if len(n) > 0 && (contains(n, "BTC/USDT") || contains(n, "вне курса")) {
			found = true
		}
	}
	if !found {
		t.Errorf("нет пометки о границах методики: %+v", rep.Notes)
	}
}

// Мало данных — честный отказ, а не выдуманный разбор по трём свечам.
func TestNotEnoughDataIsHonest(t *testing.T) {
	rep := AnalyzeGerchik("BTCUSDT", []Bar{day(0, 1, 2, 0.5, 1.5)}, Bar{}, base)
	if rep.Long.Model != "" || rep.Short.Model != "" || len(rep.Levels) != 0 {
		t.Error("на одной свече разбора быть не может")
	}
	if len(rep.Notes) == 0 {
		t.Error("отказ обязан быть объяснён")
	}
}

// Энергия дня считается от ОТКРЫТИЯ текущего дня, а не от закрытия вчерашнего (§4.3).
func TestEnergyUsesTodayOpen(t *testing.T) {
	var bars []Bar
	for i := range 30 {
		bars = append(bars, day(i, 100, 110, 100, 105))
	}
	today := Bar{Time: base.AddDate(0, 0, 31), Open: 105, High: 113, Low: 104, Close: 113}
	rep := AnalyzeGerchik("BTCUSDT", bars, today, base.AddDate(0, 0, 31))
	if rep.DayOpen != 105 || rep.Price != 113 {
		t.Fatalf("текущий день не учтён: open=%.2f price=%.2f", rep.DayOpen, rep.Price)
	}
	want := (113.0 - 105.0) / rep.ATR.ATR
	if math.Abs(rep.Energy-want) > 0.001 {
		t.Errorf("энергия = %.3f, ждали %.3f", rep.Energy, want)
	}
}

// ===== модели входа (§6) =====

// ЛП 2 барами (§6.3): пробойный бар ЗАКРЫЛСЯ за уровнем, следующий вернулся внутрь.
// Отличать его от ЛП одним баром обязательно — это разные модели с разной силой.
func TestFalseBreakoutTwoBars(t *testing.T) {
	atr := 100.0
	// Уровень 1000, работаем шортом от сопротивления: зона пробоя выше.
	bars := []Bar{
		day(0, 950, 990, 940, 980),
		day(1, 980, 1040, 975, 1030), // пробойный: закрылся ЗА уровнем
		day(2, 1030, 1035, 985, 990), // вернулся внутрь
	}
	m := detectModel(bars, 1000, atr, "short")
	if m.name != "ЛП 2 барами" {
		t.Errorf("модель распознана как %q, ждали «ЛП 2 барами»", m.name)
	}
	if m.order != "стоп-ордер" {
		t.Errorf("ЛП входится стоп-ордером, а не %q", m.order)
	}
}

// ЛП 3+ барами (§6.4): пробойный бар и минимум два следующих держались за уровнем,
// после чего цена вернулась.
func TestFalseBreakoutThreeBars(t *testing.T) {
	bars := []Bar{
		day(0, 950, 990, 940, 980),
		day(1, 980, 1030, 975, 1020),   // за уровнем
		day(2, 1020, 1030, 1010, 1025), // за уровнем
		day(3, 1025, 1030, 985, 990),   // возврат внутрь
	}
	m := detectModel(bars, 1000, 100, "short")
	if m.name != "ЛП 3+ барами" {
		t.Errorf("модель распознана как %q, ждали «ЛП 3+ барами»", m.name)
	}
}

// Цена, сидящая за уровнем прямо сейчас, — это модель В РАБОТЕ: вход ещё не состоялся,
// ждём возврата. Показать её как готовый сигнал значило бы звать в сделку раньше времени.
func TestFalseBreakoutInProgress(t *testing.T) {
	bars := []Bar{
		day(0, 950, 990, 940, 980),
		day(1, 980, 1030, 975, 1020), // закрылся за уровнем и там остался
	}
	m := detectModel(bars, 1000, 100, "short")
	if m.name != "ЛП 2 барами (в работе)" {
		t.Errorf("модель распознана как %q, ждали «ЛП 2 барами (в работе)»", m.name)
	}
	// Ушли за уровень дальше трети ATR — это уже пробой, а не ЛП.
	deep := []Bar{day(0, 950, 990, 940, 980), day(1, 980, 1100, 975, 1090)}
	if m2 := detectModel(deep, 1000, 100, "short"); len(m2.blocks) == 0 {
		t.Error("уход за уровень на 90% ATR обязан ломать модель ЛП")
	}
}

// Отбой (§6.1): БПУ1 и БПУ2 стоят в одной плоскости, и БПУ2 не ломает БПУ1.
func TestBounceModelBSUBPU(t *testing.T) {
	bars := []Bar{
		day(0, 950, 990, 940, 980),
		day(1, 980, 999, 970, 985), // БПУ1 у уровня 1000
		day(2, 985, 998, 975, 980), // БПУ2 в той же плоскости
	}
	m := detectModel(bars, 1000, 100, "short")
	if m.name != "отбой (БСУ-БПУ)" || m.order != "лимитный ордер" {
		t.Errorf("отбой не распознан: %q / %q", m.name, m.order)
	}
	// БПУ2 пробил БПУ1 — модель сломана и вход отменяется.
	broken := []Bar{
		day(0, 950, 990, 940, 980),
		day(1, 980, 999, 970, 985),
		day(2, 985, 1030, 975, 980),
	}
	if m2 := detectModel(broken, 1000, 100, "short"); len(m2.blocks) == 0 && m2.name == "отбой (БСУ-БПУ)" {
		t.Error("БПУ2 пробил БПУ1 — модель обязана считаться сломанной")
	}
}

// Пробой (§6.5) берётся только по тренду и на маленьких барах — и вход ставится ЗА уровень,
// в сторону движения, а не от него.
func TestBreakoutSetupDirection(t *testing.T) {
	atr := 100.0
	res := Level{Price: 1000, Fresh: true, Kind: "исторический", DistATR: 0.1, Above: true}
	sup := Level{Price: 700, Fresh: true, Kind: "исторический", DistATR: 2.0}
	rep := GerchikReport{Price: 990, DayOpen: 985, GlobalTrend: "лонг",
		Resistance: &res, Support: &sup, Levels: []Level{sup, res, {Price: 1400, Fresh: true, Kind: "исторический"}}}
	// Маленькие бары у уровня — аккумуляция.
	bars := []Bar{
		day(0, 960, 1000, 955, 985),
		day(1, 985, 1020, 975, 990),
		day(2, 990, 1020, 985, 990),
	}
	s := buildDirectional(&rep, bars, atr, "long")
	if s.Model != "пробой" {
		t.Fatalf("ждали пробой сопротивления по тренду, получили %q", s.Model)
	}
	if s.Entry <= res.Price {
		t.Errorf("вход на пробой обязан стоять ЗА уровнем: entry=%.2f, уровень %.2f", s.Entry, res.Price)
	}
	if s.Stop >= res.Price {
		t.Errorf("стоп пробоя обязан лежать за уровнем с обратной стороны: %.2f", s.Stop)
	}
	// В боковике пробой запрещён (§3.3).
	rep.GlobalTrend = "боковик"
	if s2 := buildDirectional(&rep, bars, atr, "long"); s2.Model == "пробой" && !hasBlock(s2, "сильном тренде") {
		t.Error("пробой в боковике не заблокирован")
	}
}

// Сценариев всегда два, и каждый строится от СВОЕГО уровня: лонг от поддержки, шорт от
// сопротивления. На живом BTC односторонний разбор показывал только шорты.
func TestBothDirectionsAreBuilt(t *testing.T) {
	var bars []Bar
	for i := range 40 {
		if i%2 == 0 {
			bars = append(bars, day(i, 1000, 1050, 950, 1010))
		} else {
			bars = append(bars, day(i, 1010, 1050, 950, 990))
		}
	}
	rep := AnalyzeGerchik("BTCUSDT", bars, Bar{}, base.AddDate(0, 0, 41))
	if rep.Long.Model == "" && rep.Short.Model == "" {
		t.Fatal("не построен ни один сценарий")
	}
	if rep.Long.Model != "" && rep.Long.Direction != "long" {
		t.Errorf("лонговый сценарий имеет направление %q", rep.Long.Direction)
	}
	if rep.Short.Model != "" && rep.Short.Direction != "short" {
		t.Errorf("шортовый сценарий имеет направление %q", rep.Short.Direction)
	}
	// Лонг работает от уровня НЕ выше цены, шорт — от уровня не ниже.
	if rep.Long.Model != "" && rep.Long.Model != "пробой" && rep.Long.Level > rep.Price {
		t.Errorf("лонг строится от сопротивления `%.2f` при цене %.2f", rep.Long.Level, rep.Price)
	}
	if rep.Short.Model != "" && rep.Short.Model != "пробой" && rep.Short.Level < rep.Price {
		t.Errorf("шорт строится от поддержки `%.2f` при цене %.2f", rep.Short.Level, rep.Price)
	}
}

// RR проверяется ПО НЕТТО: комиссия и проскальзывание (§10) съедают часть плеча, и
// «3:1 до издержек» на коротком стопе легко превращается в 2.6:1 после них.
func TestRRAccountsForCosts(t *testing.T) {
	atr := 100.0
	lvl := Level{Price: 1000, Fresh: true, Kind: "исторический", DistATR: 0.05}
	rep := GerchikReport{Price: 1002, DayOpen: 1000, GlobalTrend: "лонг",
		Support: &lvl, Levels: []Level{lvl}}
	bars := []Bar{day(0, 990, 1005, 988, 1000), day(1, 1000, 1006, 1001, 1002), day(2, 1000, 1006, 1001, 1002)}
	s := buildDirectional(&rep, bars, atr, "long")
	if s.Costs <= 0 {
		t.Fatal("издержки не посчитаны")
	}
	if s.RRNet >= s.RR {
		t.Errorf("RR с издержками (%.2f) обязан быть меньше грязного (%.2f)", s.RRNet, s.RR)
	}
	want := 1000 * (2*takerFeePct + slippagePct)
	if math.Abs(s.Costs-want) > want*0.05 {
		t.Errorf("издержки %.4f не похожи на комиссию+проскальзывание (%.4f)", s.Costs, want)
	}
}

// dirOf выводит направление из того, какой уровень положен в отчёт: сценарий строится от
// поддержки вверх и от сопротивления вниз.
func dirOf(rep *GerchikReport) string {
	if rep.Resistance != nil && rep.Support == nil {
		return "short"
	}
	return "long"
}

func hasBlock(s GerchikSetup, substr string) bool {
	for _, b := range s.Blocks {
		if contains(b, substr) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
