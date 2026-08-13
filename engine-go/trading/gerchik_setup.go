package trading

// Сценарии входа по Герчику: модели (§6), стоп (§7), цели (§8) и запреты методики.
//
// Сценариев ВСЕГДА два — лонговый и шортовый, каждый от своего уровня. Один общий сценарий
// «от ближайшего уровня» выдавал только ту сторону, к которой цена случайно оказалась ближе:
// на живом BTC это давало сплошные шорты, а лонговая половина графика оставалась невидимой.
// Трейдеру нужны обе границы сразу: где он покупает и где продаёт.

import (
	"fmt"
	"math"
)

const (
	// takerFeePct / slippagePct — издержки крипты (§10): комиссия за сторону и проскальзывание.
	// RR без них завышен: на стопе в 0.2% цены комиссия съедает заметную часть плеча.
	takerFeePct = 0.0004
	slippagePct = 0.0002
	// smallApproachBars — сколько последних баров смотрим на «подход маленькими барами» (§5.1).
	smallApproachBars = 3
	// breakoutTrendOnly — пробой берётся только в сильном тренде (§3.3, §6.5).
	breakoutTrendOnly = true
	// lpPierceATR — какой выход за уровень считается проколом. Уровень собран с допуском
	// levelTolATR, поэтому без отдельного порога «ложным пробоем» становилось бы любое
	// касание уровня хвостом.
	lpPierceATR = 0.05
)

// buildSetups строит оба сценария сразу. Каждый знает свой уровень, модель и запреты.
func buildSetups(rep *GerchikReport, bars []Bar, atr float64) (long, short GerchikSetup) {
	return buildDirectional(rep, bars, atr, "long"), buildDirectional(rep, bars, atr, "short")
}

// buildDirectional собирает сценарий одного направления. Логика выбора опоры:
// работа ОТ уровня (отбой или ложный пробой) — основная; пробой берётся, когда цена подошла
// вплотную к встречному уровню по тренду и на маленьких барах (§6.5).
func buildDirectional(rep *GerchikReport, bars []Bar, atr float64, dir string) GerchikSetup {
	s := GerchikSetup{Direction: dir}
	if len(bars) < 3 || atr <= 0 {
		s.Blocks = append(s.Blocks, "данных по дням не хватает для сценария")
		return s
	}

	// Опора направления: лонг работает от поддержки, шорт — от сопротивления.
	base, counter := rep.Support, rep.Resistance
	if dir == "short" {
		base, counter = rep.Resistance, rep.Support
	}

	// Пробой встречного уровня — сделка В ЕГО СТОРОНУ, а не от него. Условия жёсткие:
	// сильный тренд, цена вплотную и аккумуляция маленькими барами.
	if counter != nil && counter.DistATR <= nearLevelATR && trendMatches(rep.GlobalTrend, dir) &&
		(!breakoutTrendOnly || rep.GlobalTrend != "боковик") && smallApproach(bars, atr) {
		return breakoutSetup(rep, bars, atr, dir, counter)
	}
	if base == nil {
		side := "поддержки"
		if dir == "short" {
			side = "сопротивления"
		}
		s.Blocks = append(s.Blocks, "ближайшей "+side+" на D1 нет — сценарий строить не от чего")
		return s
	}

	s.Level = base.Price
	s.Pending = base.DistATR > nearLevelATR
	if s.Pending {
		s.Blocks = append(s.Blocks, fmt.Sprintf(
			"цена ещё не у уровня (%.2f ATR до него) — это заготовка, а не сигнал ко входу", base.DistATR))
	}

	model := detectModel(bars, base.Price, atr, dir)
	s.Model, s.Order = model.name, model.order
	s.Reasons = append(s.Reasons, model.reasons...)
	s.Blocks = append(s.Blocks, model.blocks...)

	finishSetup(&s, rep, bars, atr, base, model)
	return s
}

// modelInfo — распознанная модель входа со всем, что из неё следует.
type modelInfo struct {
	name    string
	order   string
	lpDepth float64 // насколько глубоко цена уходила за уровень (0 = ЛП нет)
	tail    float64 // экстремум ложного пробоя — кандидат в технический стоп
	hasTail bool
	reasons []string
	blocks  []string
}

// detectModel разбирает поведение последних баров у уровня и называет модель методики:
// ЛП одним, двумя или тремя+ барами (§6.2–6.4) либо отбой БСУ-БПУ (§6.1).
//
// Направление задаёт, где лежит «зона пробоя»: для лонга от поддержки это ПОД уровнем,
// для шорта от сопротивления — НАД ним. Дальше правила курса одинаковы для обеих сторон,
// поэтому вся зеркальность собрана в знаке z и живёт только здесь.
func detectModel(bars []Bar, level, atr float64, dir string) modelInfo {
	tol := levelTolATR * atr
	z := 1.0 // зона пробоя выше уровня (шорт от сопротивления)
	if dir == "long" {
		z = -1 // зона пробоя ниже уровня (лонг от поддержки)
	}
	n := len(bars)
	last := bars[n-1]

	// «Бар в зоне пробоя» — это ЗАКРЫТИЕ за уровнем, без допусков: методика говорит именно
	// про закрытие, и лишний допуск здесь съедал бы модели ЛП 2 и 3 барами.
	inZone := func(b Bar) bool { return (b.Close-level)*z > 0 }
	// А вот прокол должен быть заметным: уровень собран с допуском levelTolATR, и считать
	// проколом каждое касание значит объявлять ложным пробоем любое касание уровня.
	pierced := func(b Bar) bool {
		e := b.High
		if z < 0 {
			e = b.Low
		}
		return (e-level)*z > lpPierceATR*atr
	}
	inside := func(b Bar) bool { return (b.Close-level)*z <= 0 }
	extreme := func(b Bar) float64 {
		if z > 0 {
			return b.High
		}
		return b.Low
	}
	// Как глубоко цена уходила за уровень на отрезке баров — это и есть глубина ЛП (§6.2).
	depth := func(from int) (d, ex float64) {
		ex = level
		for i := from; i < n; i++ {
			if e := extreme(bars[i]); (e-level)*z > (ex-level)*z {
				ex = e
			}
		}
		return (ex - level) * z, ex
	}

	// Сколько ПОСЛЕДНИХ баров закрылись за уровнем — цена сидит в зоне пробоя прямо сейчас.
	zoneRun := 0
	for i := n - 1; i >= 0 && inZone(bars[i]); i-- {
		zoneRun++
	}
	if zoneRun > 0 {
		d, ex := depth(n - zoneRun)
		m := modelInfo{order: "стоп-ордер", lpDepth: d, tail: ex, hasTail: true}
		switch {
		case zoneRun == 1:
			m.name = "ЛП 2 барами (в работе)"
			m.reasons = append(m.reasons, "пробойный бар закрылся за уровнем — ждём возврата внутрь (§6.3)")
		default:
			m.name = fmt.Sprintf("ЛП %d барами (в работе)", min(zoneRun+1, 3))
			m.reasons = append(m.reasons,
				fmt.Sprintf("%d бара подряд закрылись за уровнем и не ушли дальше — сложный ЛП (§6.4)", zoneRun))
		}
		if d > lpDepthMax*atr {
			m.blocks = append(m.blocks,
				"цена ушла за уровень больше чем на треть ATR — это уже пробой, а не ЛП (§6.2)")
		}
		return m
	}

	// Цена внутри. Сначала считаем, сколько баров ДО последнего сидели за уровнем: это
	// решает, ЛП это одним баром или двумя-тремя. Порядок здесь важен — если сперва
	// проверять прокол последнего бара, любой сложный ЛП схлопнется в «ЛП 1 баром».
	prevRun := 0
	for i := n - 2; i >= 0 && inZone(bars[i]); i-- {
		prevRun++
	}
	if prevRun == 0 && pierced(last) && inside(last) {
		d, ex := depth(n - 1)
		m := modelInfo{name: "ЛП 1 баром", order: "стоп-ордер", lpDepth: d, tail: ex, hasTail: true}
		m.reasons = append(m.reasons, "последний бар прокололся за уровень и закрылся обратно (§6.2)")
		if d > lpDepthMax*atr {
			m.blocks = append(m.blocks, "глубина ЛП больше трети ATR — модель не берётся (§6.2)")
		}
		return m
	}
	if prevRun > 0 && inside(last) {
		d, ex := depth(n - 1 - prevRun)
		m := modelInfo{order: "стоп-ордер", lpDepth: d, tail: ex, hasTail: true}
		if prevRun == 1 {
			m.name = "ЛП 2 барами"
			m.reasons = append(m.reasons, "пробойный бар закрылся за уровнем, следующий вернулся внутрь (§6.3)")
		} else {
			m.name = "ЛП 3+ барами"
			m.reasons = append(m.reasons,
				fmt.Sprintf("%d бара держались за уровнем и цена вернулась внутрь (§6.4)", prevRun))
		}
		if d > lpDepthMax*atr {
			m.blocks = append(m.blocks, "глубина ЛП больше трети ATR — модель не берётся (§6.2)")
		}
		return m
	}

	// Ложного пробоя нет — значит отбой. БСУ уже стоит в истории (это сам уровень),
	// смотрим подтверждающие бары: БПУ1 и БПУ2 обязаны стоять в одной плоскости (§6.1).
	m := modelInfo{name: "отбой (БСУ-БПУ)", order: "лимитный ордер"}
	touch := func(b Bar) bool { return math.Abs(extreme(b)-level) <= tol }
	switch {
	case n >= 2 && touch(last) && touch(bars[n-2]):
		// БПУ2 не должен ломать БПУ1: иначе модель сломана и ордер снимается (§6.1).
		if (extreme(last)-extreme(bars[n-2]))*z > tol {
			m.blocks = append(m.blocks, "БПУ2 пробил БПУ1 — модель отбоя сломана (§6.1)")
			break
		}
		m.reasons = append(m.reasons, "БПУ1 и БПУ2 стоят в одной плоскости — модель собрана (§6.1)")
	case touch(last):
		m.reasons = append(m.reasons, "БПУ1 есть, ждём БПУ2 у той же цены (§6.1)")
	default:
		m.reasons = append(m.reasons, "подтверждающих баров у уровня ещё нет — модель в работе")
	}
	return m
}

// breakoutSetup — пробой (§6.5): вход стоп-ордером ЗА уровень по тренду, до самого пробоя.
// Стоп ложится за уровень с обратной стороны: если импульса нет, это уже ложный пробой.
func breakoutSetup(rep *GerchikReport, bars []Bar, atr float64, dir string, lvl *Level) GerchikSetup {
	s := GerchikSetup{Direction: dir, Model: "пробой", Order: "стоп-ордер", Level: lvl.Price}
	s.Reasons = append(s.Reasons,
		"подход на маленьких барах по тренду — аккумуляция перед пробоем (§5.1, §6.5)",
		"вход ставится ДО пробоя, стоп-ордером за уровень (§6.5)")
	finishSetup(&s, rep, bars, atr, lvl, modelInfo{name: "пробой", order: "стоп-ордер"})
	// Нет импульса после пробоя — выходить: об этом методика говорит прямо, и промолчать
	// тут значит оставить человека в сделке, которая уже превратилась в ЛП против него.
	s.Reasons = append(s.Reasons, "после пробоя нет импульса — выходить, это ЛП (§6.5)")
	return s
}

// finishSetup досчитывает общую для всех моделей часть: стоп, люфт, вход, цели, RR и запреты.
func finishSetup(s *GerchikSetup, rep *GerchikReport, bars []Bar, atr float64, lvl *Level, m modelInfo) {
	byTrend := trendMatches(rep.GlobalTrend, s.Direction)
	slPct := slCounterPct
	if byTrend {
		slPct = slTrendPct
	}
	s.SLSize = slPct * atr
	s.Luft = luftPct * s.SLSize

	// Вход: лимит с люфтом внутрь от уровня (отбой) либо стоп-ордер у самого уровня.
	// Для пробоя вход ЗА уровнем по направлению сделки — в этом вся разница моделей.
	sign := 1.0
	if s.Direction == "short" {
		sign = -1
	}
	switch {
	case s.Model == "пробой":
		s.Entry = lvl.Price + sign*s.Luft
		s.Stop = lvl.Price - sign*s.SLSize
	case s.Order == "лимитный ордер":
		s.Entry = lvl.Price + sign*s.Luft
		s.Stop = lvl.Price - sign*s.SLSize
	default:
		s.Entry = lvl.Price
		s.Stop = lvl.Price - sign*s.SLSize
	}

	// Технический стоп за хвостом ложного пробоя — но только пока он умещается в расчётный
	// плюс 20% (§7.1–7.2). Не умещается — это не повод растянуть риск, а повод не входить.
	if m.hasTail && m.lpDepth > 0 {
		techSize := (s.Entry - m.tail) * sign
		switch {
		case techSize <= 0:
			// хвост не выходит за вход — расчётный стоп и так дальше
		case techSize <= s.SLSize*slTechCap:
			s.Stop, s.SLSize = m.tail, techSize
			s.Luft = luftPct * s.SLSize
			s.Reasons = append(s.Reasons, "стоп технический — за хвостом ЛП (§7.2)")
		default:
			s.Blocks = append(s.Blocks,
				"технический стоп за хвостом ЛП больше расчётного на 20%+ — сделка не берётся (§7.1)")
		}
	}

	// Цели — выход частями 3:1 / 4:1 / 5:1 (§8), не дальше следующего уровня (§4.2).
	next := nextLevelToward(rep.Levels, s.Entry, s.Direction)
	if next != nil {
		s.LevelTarget = next.Price
		s.TechATR = math.Abs(next.Price-lvl.Price) / atr
	}
	for _, mult := range []float64{3, 4, 5} {
		tp := s.Entry + sign*mult*s.SLSize
		if next != nil && (tp-next.Price)*sign > 0 {
			break
		}
		s.Takes = append(s.Takes, tp)
	}
	switch {
	case len(s.Takes) > 0:
		s.Take = s.Takes[len(s.Takes)-1]
		s.TakeIsRR = true
		if next == nil {
			s.Reasons = append(s.Reasons, "уровня впереди нет — цели по RR 3:1 / 4:1 / 5:1 (§8)")
		}
	case next != nil:
		s.Take = next.Price // до уровня меньше трёх стопов — это отказ, а не «почти»
	}
	if s.SLSize > 0 {
		s.RR = math.Abs(s.Take-s.Entry) / s.SLSize
		// Издержки крипты (§10): две комиссии тейкера плюс проскальзывание. Считаются от
		// цены входа и утяжеляют обе стороны — и прибыль, и убыток.
		cost := s.Entry * (2*takerFeePct + slippagePct)
		s.Costs = cost
		if d := math.Abs(s.Take-s.Entry) - cost; d > 0 {
			s.RRNet = d / (s.SLSize + cost)
		}
	}

	applyBlocks(s, rep, atr, lvl, byTrend)
	s.Ready = len(s.Blocks) == 0
}

// applyBlocks собирает запреты методики. Их больше, чем разрешений, и это нормально:
// «трейдер должен уметь ждать — 90% времени это ожидание» (§1).
func applyBlocks(s *GerchikSetup, rep *GerchikReport, atr float64, lvl *Level, byTrend bool) {
	// RR проверяется ПО НЕТТО: комиссия и проскальзывание уже съели часть плеча, и
	// «3:1 до издержек» на коротком стопе легко оказывается 2.6:1 после них.
	if s.RRNet < minRR {
		s.Blocks = append(s.Blocks, fmt.Sprintf(
			"RR с издержками 1:%.1f — ниже 3:1, сделка не берётся (§8, §10)", s.RRNet))
	}
	if !s.TakeIsRR && s.TechATR > 0 && s.TechATR < 1 {
		s.Blocks = append(s.Blocks,
			"технический ATR меньше расчётного: до цели меньше дневного хода — не торговать (§4.2)")
	}
	if atr < atrToSLMin*s.SLSize {
		s.Blocks = append(s.Blocks, "ATR инструмента меньше пяти стопов — не торговать (§4.2)")
	}
	if math.Abs(rep.Energy) >= energyBlockPct && byTrend {
		s.Blocks = append(s.Blocks, "за сегодня пройдено ≥75% ATR — по тренду не заходить (§4.3)")
	}
	// Несвежий уровень прощается, если цена идёт к нему через пустую зону (§2.4).
	if !lvl.Fresh && !lvl.EmptyZone {
		s.Blocks = append(s.Blocks, fmt.Sprintf(
			"уровень не подтверждался %d дней и подход не через пустую зону (§2.4)", lvl.AgeDays))
	}
	if lvl.Kind == "воздушный" {
		s.Blocks = append(s.Blocks, "воздушный уровень — не торгуется (§2.2)")
	}
	if lvl.Squeezed {
		s.Blocks = append(s.Blocks, "уровень зажат между более сильными — внутренние не торгуются (§2.1)")
	}
	if s.Model == "пробой" && rep.GlobalTrend == "боковик" {
		s.Blocks = append(s.Blocks, "пробои торгуются только в сильном тренде (§3.3)")
	}
	switch {
	case byTrend:
		s.Reasons = append(s.Reasons, "сделка по глобальному тренду")
	case rep.GlobalTrend == "лонг" || rep.GlobalTrend == "шорт":
		s.Reasons = append(s.Reasons, "сделка против глобального тренда — стоп короче")
	default:
		// Боковик и «смешанный» — это не «против тренда», а отсутствие тренда. Называть
		// причину надо честно, иначе отчёт врёт о рынке.
		s.Reasons = append(s.Reasons, "глобального тренда нет ("+rep.GlobalTrend+")")
	}
	// Про РАСЧЁТНЫЙ размер стопа говорим, только если он и применён: когда стоп взят
	// технический (за хвостом ЛП), строка «стоп 15% ATR» противоречила бы соседней строке
	// отчёта, где стоит фактические 5%.
	calc := slCounterPct
	if byTrend {
		calc = slTrendPct
	}
	if math.Abs(s.SLSize-calc*atr) < 1e-9 {
		s.Reasons = append(s.Reasons, fmt.Sprintf("стоп расчётный — %.0f%% ATR (§7.1)", calc*100))
	}
}

// trendMatches отвечает, совпадает ли направление сделки с глобальным трендом.
func trendMatches(trend, dir string) bool {
	return (dir == "long" && trend == "лонг") || (dir == "short" && trend == "шорт")
}

// smallApproach — подход на маленьких барах: обязательное условие пробоя (§6.5).
func smallApproach(bars []Bar, atr float64) bool {
	n := len(bars)
	if n < smallApproachBars || atr <= 0 {
		return false
	}
	var sum float64
	for _, b := range bars[n-smallApproachBars:] {
		sum += b.High - b.Low
	}
	return sum/smallApproachBars <= smallBarATR*atr
}

// nextLevelToward ищет ближайший уровень в направлении сделки — это цель по методике.
func nextLevelToward(levels []Level, from float64, dir string) *Level {
	var best *Level
	for i := range levels {
		l := &levels[i]
		if dir == "long" && l.Price > from && (best == nil || l.Price < best.Price) {
			best = l
		}
		if dir == "short" && l.Price < from && (best == nil || l.Price > best.Price) {
			best = l
		}
	}
	return best
}
