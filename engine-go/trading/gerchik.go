package trading

// Алгоритм Герчика для крипты (BTC/USDT, ETH/USDT) — детерминированный разбор дневного
// графика: уровни D1, ATR по методике курса, тренд, энергия дня и торговый сценарий
// (ложный пробой / пробой / отбой) с готовыми стопом, целью и RR.
//
// Модуль СЧИТАЕТ, но не решает за человека: там, где методика запрещает вход (энергия
// выбрана, цель ближе стопа, RR ниже 3), сценарий возвращается с явными блокировками,
// а не подгоняется под красивый вердикт. Ни одно число здесь не берётся у модели —
// источник только свечи Binance, как и во всём остальном разборе (§1 инварианта).
//
// Границы методики: она описана для BTC/USDT и ETH/USDT. На других тикерах считается
// то же самое, но отчёт обязан говорить, что инструмент вне курса.

import (
	"math"
	"sort"
	"time"
)

// Bar — дневная свеча. Open нужен для правила 75% (энергия дня отсчитывается от открытия),
// Time — для свежести уровня; поэтому обычного candle из market.go тут мало.
type Bar struct {
	Time                   time.Time
	Open, High, Low, Close float64
}

const (
	// GerchikLookbackDays — горизонт построения уровней (§2.1: последние 6 месяцев).
	GerchikLookbackDays = 180
	// gerchikMaxLevels — сколько уровней остаётся в разборе (§2.1: 3–5, не больше).
	gerchikMaxLevels = 5
	// gerchikATRDays — ATR считается по 3 нормальным дням (§4.1), а не по 14 сглаженным.
	gerchikATRDays = 3
	// gerchikATRBaseDays — окно для БАЗОВОГО ATR, относительно которого бар признаётся
	// паранормальным. Определение циклично (паранормальность через ATR, ATR по нормальным
	// барам), поэтому порядок зафиксирован здесь и проверяется тестом: база → фильтр →
	// пересчёт по 3 нормальным, вторая итерация не делается.
	gerchikATRBaseDays = 10
	// paranormalBig / paranormalSmall — границы нормального бара (§4.1).
	paranormalBig   = 1.5
	paranormalSmall = 0.5
	// levelFreshDays — подтверждение уровня свежее 7 дней (§2.4).
	levelFreshDays = 7
	// staleLevelPenalty — во сколько раз слабее старый уровень, до которого цена идёт
	// НЕ через пустую зону (§2.4: понижать приоритет).
	staleLevelPenalty = 0.7
	// slTrendPct / slCounterPct — расчётный стоп (§7.1).
	slTrendPct   = 0.15
	slCounterPct = 0.10
	// slTechCap — технический стоп не больше расчётного на 20% (§7.1).
	slTechCap = 1.2
	// luftPct — люфт лимитного ордера для отбоя (§7.3).
	luftPct = 0.20
	// minRR — минимальное соотношение (§8).
	minRR = 3.0
	// energyBlockPct — правило 75% (§4.3).
	energyBlockPct = 0.75
	// lpDepthMax — глубина ложного пробоя не больше трети ATR (§6.2).
	lpDepthMax = 1.0 / 3.0
	// atrToSLMin — ATR инструмента не меньше пяти стопов (§4.2).
	atrToSLMin = 5.0
	// levelTolATR — допуск «в одну цену» при сборке уровня: доля дневного хода. Без допуска
	// каждое касание давало бы свой уровень, и «3–5 уровней» превращались бы в сотни.
	levelTolATR = 0.20
	// maxTouchBonus — потолок прибавки за касания (см. describeLevel).
	maxTouchBonus = 4
	// minLevelGapATR — минимальный разнос между отобранными уровнями. Без него пятёрка
	// «сильнейших» садится в одну зону шириной в пару процентов: формально это разные
	// уровни, практически — одна стена, а вторая половина графика остаётся без разметки.
	minLevelGapATR = 0.75
	// nearLevelATR — насколько близко цена должна подойти, чтобы уровень считался рабочим.
	nearLevelATR = 0.35
	// smallBarATR — «подход на маленьких барах» (§5.1, предпосылка к пробою).
	smallBarATR = 0.6
	// bigBarATR — «подход на больших барах» (§5.2, предпосылка к отбою).
	bigBarATR = 1.5
	// gapMinATR — с какого разрыва между закрытием и открытием считаем это гэпом (§2.2).
	gapMinATR = 0.3
	// swingWindow — сколько баров смотрим с каждой стороны, ища слом тренда (§2.2, тип 1).
	swingWindow = 10
	// swingAwayATR — насколько цена должна уйти от экстремума, чтобы разворот состоялся.
	swingAwayATR = 1.5
	// squeezeATR — на каком расстоянии более сильный уровень «зажимает» соседний (§2.1).
	squeezeATR = 2.0
)

// Level — уровень D1 со всем, что о нём известно из истории.
type Level struct {
	Price     float64   `json:"price"`
	Kind      string    `json:"kind"` // зеркальный | исторический | ЛП | паранормальный | воздушный
	Touches   int       `json:"touches"`
	Round     bool      `json:"round"`     // круглая цифра усиливает (§2.1)
	Mirror    bool      `json:"mirror"`    // работал и поддержкой, и сопротивлением
	FromLP    bool      `json:"from_lp"`   // образован после ложного пробоя
	LongTail  bool      `json:"long_tail"` // у уровня есть бар с длинным хвостом
	LastTouch time.Time `json:"last_touch"`
	AgeDays   int       `json:"age_days"`   // сколько дней с последнего касания
	Fresh     bool      `json:"fresh"`      // ≤7 дней (§2.4)
	Strength  float64   `json:"strength"`   // итоговая сила с усилителями
	Above     bool      `json:"above"`      // выше текущей цены → сопротивление
	DistATR   float64   `json:"dist_atr"`   // расстояние от цены в ATR
	EmptyZone bool      `json:"empty_zone"` // между ценой и уровнем других уровней нет (§2.4)
	Squeezed  bool      `json:"squeezed"`   // зажат между более сильными — внутренний (§2.1)
	Limit     bool      `json:"limit"`      // лимитный игрок: не пробит ни разу (§2.2)
	Paranorm  bool      `json:"paranorm"`   // образован концом паранормального бара (§2.2)
	Swing     bool      `json:"swing"`      // слом тренда — сильнейший тип (§2.2)
	Gap       bool      `json:"gap"`        // край гэпа (§2.2)
}

// GerchikATR — результат расчёта энергии инструмента вместе с тем, как он получен:
// отчёт обязан показывать, сколько баров отброшено, иначе «ATR = 900» невозможно проверить.
type GerchikATRResult struct {
	ATR          float64 `json:"atr"`
	Base         float64 `json:"base"`
	NormalDays   int     `json:"normal_days"`
	SkippedBig   int     `json:"skipped_big"`
	SkippedSmall int     `json:"skipped_small"`
}

// GerchikSetup — торговый сценарий по методике. Пустой Model означает «входа сейчас нет»,
// и причины этого лежат в Blocks — молчание тут было бы хуже отказа.
type GerchikSetup struct {
	Model       string    `json:"model"`     // ЛП 1 баром | ЛП 2 барами | отбой (БСУ-БПУ) | пробой
	Order       string    `json:"order"`     // стоп-ордер | лимитный ордер
	Direction   string    `json:"direction"` // long | short
	Level       float64   `json:"level"`
	Entry       float64   `json:"entry"`
	Stop        float64   `json:"stop"`
	Take        float64   `json:"take"`         // дальняя цель — она же последняя часть выхода
	Takes       []float64 `json:"takes"`        // 3:1 / 4:1 / 5:1, обрезанные уровнем (§8)
	LevelTarget float64   `json:"level_target"` // следующий уровень D1 — граница хода
	Luft        float64   `json:"luft"`
	SLSize      float64   `json:"sl_size"`
	RR          float64   `json:"rr"`
	RRNet       float64   `json:"rr_net"`     // RR после комиссии и проскальзывания (§10)
	Costs       float64   `json:"costs"`      // сами издержки, чтобы число можно было проверить
	TechATR     float64   `json:"tech_atr"`   // ход до следующего уровня в ATR (§4.2)
	TakeIsRR    bool      `json:"take_is_rr"` // цель посчитана от RR, а не по уровню впереди
	Pending     bool      `json:"pending"`    // цена ещё не у уровня: это заготовка, а не сигнал
	Ready       bool      `json:"ready"`      // модель собрана и запретов нет — вход по алгоритму
	Reasons     []string  `json:"reasons"`
	Blocks      []string  `json:"blocks"`
}

// GerchikReport — полный разбор по методике.
type GerchikReport struct {
	Symbol      string           `json:"symbol"`
	InScope     bool             `json:"in_scope"` // BTC/ETH — инструменты курса
	Price       float64          `json:"price"`
	DayOpen     float64          `json:"day_open"`
	ATR         GerchikATRResult `json:"atr"`
	Energy      float64          `json:"energy"` // доля ATR, пройденная за сегодня
	GlobalTrend string           `json:"global_trend"`
	LocalTrend  string           `json:"local_trend"`
	EMA200      float64          `json:"ema200"`
	Levels      []Level          `json:"levels"`
	Support     *Level           `json:"support"`
	Resistance  *Level           `json:"resistance"`
	Long        GerchikSetup     `json:"long"`     // сценарий покупки: от поддержки или пробоем вверх
	Short       GerchikSetup     `json:"short"`    // сценарий продажи: от сопротивления или пробоем вниз
	Premises    []string         `json:"premises"` // формализуемые предпосылки (§5)
	Notes       []string         `json:"notes"`
	Days        int              `json:"days"` // сколько дневных баров реально разобрано
}

// AnalyzeGerchik — вход в методику. closed — ЗАКРЫТЫЕ дневные бары (старые первыми),
// today — текущий незакрытый день (Open + текущая цена в Close); нулевой today допустим:
// тогда энергия дня считается по последнему закрытому бару.
func AnalyzeGerchik(symbol string, closed []Bar, today Bar, now time.Time) GerchikReport {
	rep := GerchikReport{Symbol: symbol, InScope: symbol == "BTCUSDT" || symbol == "ETHUSDT"}
	if len(closed) < 20 {
		rep.Notes = append(rep.Notes, "дневных свечей меньше 20 — уровни D1 строить не на чем")
		return rep
	}
	if len(closed) > GerchikLookbackDays {
		closed = closed[len(closed)-GerchikLookbackDays:]
	}
	rep.Days = len(closed)

	last := closed[len(closed)-1]
	price, dayOpen := last.Close, last.Open
	if today.Close > 0 {
		price, dayOpen = today.Close, today.Open
	}
	rep.Price, rep.DayOpen = price, dayOpen

	rep.ATR = gerchikATR(closed)
	atr := rep.ATR.ATR
	if atr <= 0 {
		rep.Notes = append(rep.Notes, "ATR посчитать не удалось — данные по дням неполные")
		return rep
	}
	rep.Energy = (price - dayOpen) / atr

	rep.Levels = buildLevels(closed, atr, price, now)
	rep.GlobalTrend, rep.EMA200 = globalTrend(closed)
	rep.LocalTrend = localTrend(closed, rep.Levels, price)
	rep.Support, rep.Resistance = nearestLevels(rep.Levels, price)
	rep.Premises = premises(closed, atr, rep.Support, rep.Resistance, price, rep.GlobalTrend, rep.LocalTrend)
	rep.Long, rep.Short = buildSetups(&rep, closed, atr)

	if !rep.InScope {
		rep.Notes = append(rep.Notes,
			"методика курса описана для BTC/USDT и ETH/USDT — на этом инструменте расчёт справочный")
	}
	return rep
}

// ===== ATR =====

// gerchikATR считает энергию инструмента так, как учит курс: дневной размах High−Low,
// три НОРМАЛЬНЫХ дня, паранормальные выброшены. Wilder-сглаживание из ATR() тут не годится
// принципиально — оно даёт другое число и другую логику стопа.
func gerchikATR(bars []Bar) GerchikATRResult {
	n := len(bars)
	if n == 0 {
		return GerchikATRResult{}
	}
	baseFrom := max(n-gerchikATRBaseDays, 0)
	var sum float64
	for _, b := range bars[baseFrom:] {
		sum += b.High - b.Low
	}
	base := sum / float64(n-baseFrom)
	if base <= 0 {
		return GerchikATRResult{}
	}

	res := GerchikATRResult{Base: base}
	var normal []float64
	// Идём от свежих дней к старым и набираем три нормальных: методика смотрит на то, чем
	// инструмент дышит СЕЙЧАС, а не на средний размах за квартал.
	for i := n - 1; i >= 0 && len(normal) < gerchikATRDays; i-- {
		rng := bars[i].High - bars[i].Low
		switch {
		case rng >= paranormalBig*base:
			res.SkippedBig++
		case rng <= paranormalSmall*base:
			res.SkippedSmall++
		default:
			normal = append(normal, rng)
		}
	}
	if len(normal) == 0 {
		res.ATR = base // все дни паранормальные — честнее вернуть базу, чем ноль
		return res
	}
	var s float64
	for _, r := range normal {
		s += r
	}
	res.ATR = s / float64(len(normal))
	res.NormalDays = len(normal)
	return res
}

// ===== уровни =====

type levelSeed struct {
	price  float64
	t      time.Time
	fromLP bool
}

// buildLevels строит уровни по правилу экстремумов (§2.3), склеивает близкие в один и
// оставляет сильнейшие. Ровно здесь чаще всего ломаются реализации: уровни берут с рабочего
// ТФ или считают каждый экстремум отдельным уровнем.
func buildLevels(bars []Bar, atr, price float64, now time.Time) []Level {
	tol := levelTolATR * atr
	if tol <= 0 {
		return nil
	}
	var seeds []levelSeed
	for i := 1; i < len(bars); i++ {
		switch {
		case bars[i].Close > bars[i-1].Close:
			// Закрылись выше предыдущего → уровень по хаю ЭТОГО бара.
			seeds = append(seeds, levelSeed{price: bars[i].High, t: bars[i].Time})
		case bars[i].Close < bars[i-1].Close:
			// Закрылись ниже → это ложный пробой: уровень по хаю И лоу ПРЕДЫДУЩЕГО бара.
			seeds = append(seeds,
				levelSeed{price: bars[i-1].High, t: bars[i-1].Time, fromLP: true},
				levelSeed{price: bars[i-1].Low, t: bars[i-1].Time, fromLP: true})
		}
	}
	if len(seeds) == 0 {
		return nil
	}
	sort.Slice(seeds, func(a, b int) bool { return seeds[a].price < seeds[b].price })

	// Склейка соседних кандидатов в кластеры «в одну цену».
	var levels []Level
	cluster := []levelSeed{seeds[0]}
	flush := func(c []levelSeed) {
		var sum float64
		var lp bool
		var lastT time.Time
		for _, s := range c {
			sum += s.price
			lp = lp || s.fromLP
			if s.t.After(lastT) {
				lastT = s.t
			}
		}
		levels = append(levels, Level{Price: sum / float64(len(c)), FromLP: lp, LastTouch: lastT})
	}
	for _, s := range seeds[1:] {
		if s.price-cluster[len(cluster)-1].price <= tol {
			cluster = append(cluster, s)
			continue
		}
		flush(cluster)
		cluster = []levelSeed{s}
	}
	flush(cluster)

	for i := range levels {
		describeLevel(&levels[i], bars, atr, tol, price, now)
	}
	// Воздушные уровни (одно касание) торговать нельзя — они и не должны попадать в разбор.
	kept := levels[:0]
	for _, l := range levels {
		if l.Touches >= 2 {
			kept = append(kept, l)
		}
	}
	levels = kept
	sort.Slice(levels, func(a, b int) bool { return levels[a].Strength > levels[b].Strength })
	return pickLevels(levels, price, atr)
}

// pickLevels оставляет 3–5 рабочих уровней (§2.1). Одной сортировки по силе мало по двум
// причинам, и обе видны на живом графике: сильные уровни сбиваются в одну зону (методика
// зовёт зажатые между ними внутренними и торговать их запрещает), а если все отобранные
// оказались по одну сторону от цены, разбор остаётся без цели и без второй границы —
// сценарий не построить в принципе.
func pickLevels(sorted []Level, price, atr float64) []Level {
	gap := minLevelGapATR * atr
	var out []Level
	fits := func(l Level) bool {
		for _, p := range out {
			if math.Abs(p.Price-l.Price) < gap {
				return false
			}
		}
		return true
	}
	for _, l := range sorted {
		if len(out) >= gerchikMaxLevels {
			break
		}
		if fits(l) {
			out = append(out, l)
		}
	}
	// Достраиваем недостающую сторону: без поддержки или без сопротивления цель и стоп
	// брать неоткуда.
	hasBelow, hasAbove := false, false
	for _, l := range out {
		if l.Price <= price {
			hasBelow = true
		} else {
			hasAbove = true
		}
	}
	for _, want := range []bool{false, true} { // false = нужен уровень снизу, true = сверху
		if (want && hasAbove) || (!want && hasBelow) {
			continue
		}
		for _, l := range sorted {
			if (l.Price > price) != want {
				continue
			}
			out = append(out, l)
			if len(out) > gerchikMaxLevels {
				// вытесняем самый слабый, а не отобранную только что вторую сторону
				weakest := 0
				for i := 1; i < len(out)-1; i++ {
					if out[i].Strength < out[weakest].Strength {
						weakest = i
					}
				}
				out = append(out[:weakest], out[weakest+1:]...)
			}
			break
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Price < out[b].Price })
	markZones(out, price, atr)
	return out
}

// markZones расставляет два признака, которые видны только на ГОТОВОМ наборе уровней:
// пустая зона до уровня (§2.4 — прощает несвежесть: цене нечего цеплять по дороге) и
// «зажатость» между двумя более сильными уровнями (§2.1 — внутренние не торгуются).
func markZones(levels []Level, price, atr float64) {
	for i := range levels {
		l := &levels[i]
		l.EmptyZone = true
		for j := range levels {
			if i == j {
				continue
			}
			o := levels[j]
			// уровень o стоит МЕЖДУ ценой и уровнем l — значит зона не пуста
			if (o.Price-price)*(l.Price-price) > 0 && math.Abs(o.Price-price) < math.Abs(l.Price-price) {
				l.EmptyZone = false
				break
			}
		}
		var strongBelow, strongAbove bool
		for j := range levels {
			if i == j || levels[j].Strength <= l.Strength {
				continue
			}
			if d := levels[j].Price - l.Price; d < 0 && -d <= squeezeATR*atr {
				strongBelow = true
			} else if d > 0 && d <= squeezeATR*atr {
				strongAbove = true
			}
		}
		l.Squeezed = strongBelow && strongAbove
	}
}

// describeLevel досчитывает по истории всё, из чего складывается сила уровня.
func describeLevel(l *Level, bars []Bar, atr, tol, price float64, now time.Time) {
	var lpCount int
	var lastLP time.Time
	for _, b := range bars {
		// Касание — это ЭКСТРЕМУМ у уровня, а не любой бар, накрывший его телом. Иначе при
		// проходе цены через зону касанием считается каждый бар подряд, и рядовой уровень
		// получает «30 касаний» и силу, которой у него нет.
		touched := math.Abs(b.High-l.Price) <= tol || math.Abs(b.Low-l.Price) <= tol
		if touched {
			l.Touches++
			if b.Time.After(l.LastTouch) {
				l.LastTouch = b.Time
			}
			// Длинный хвост у уровня — след борьбы, а не случайного прокола.
			body := math.Abs(b.Close - b.Open)
			rng := b.High - b.Low
			if rng > 0 && body/rng < 0.5 && rng >= 0.8*atr {
				l.LongTail = true
			}
			// Хвостом пробил, закрылся обратно — тот самый ложный пробой.
			if (b.High > l.Price+tol && b.Close < l.Price) || (b.Low < l.Price-tol && b.Close > l.Price) {
				lpCount++
				if b.Time.After(lastLP) {
					lastLP = b.Time
				}
			}
		}
	}
	// Лимитный игрок (§2.2): в уровень било три раза и больше, но цена ни разу не закрылась
	// за ним «ни на копейку» — там стоит крупный лимит, а не случайное скопление ордеров.
	if l.Touches >= 3 {
		var beyondUp, beyondDown int
		for _, b := range bars {
			if b.Close > l.Price+tol {
				beyondUp++
			}
			if b.Close < l.Price-tol {
				beyondDown++
			}
		}
		l.Limit = beyondUp == 0 || beyondDown == 0
	}
	// Паранормальная свеча (§2.2): оба её конца — уровни. Значим бар размахом ≥1.5 ATR.
	for _, b := range bars {
		if b.High-b.Low < paranormalBig*atr {
			continue
		}
		if math.Abs(b.High-l.Price) <= tol || math.Abs(b.Low-l.Price) <= tol {
			l.Paranorm = true
			break
		}
	}
	// Гэп (§2.2): разрыв между закрытием и следующим открытием. В крипте редкость (рынок
	// круглосуточный), но на выходных данных и после остановок торгов случается.
	for i := 1; i < len(bars); i++ {
		gap := math.Abs(bars[i].Open - bars[i-1].Close)
		if gap < gapMinATR*atr {
			continue
		}
		if math.Abs(bars[i].Open-l.Price) <= tol || math.Abs(bars[i-1].Close-l.Price) <= tol {
			l.Gap = true
			break
		}
	}
	l.Swing = isSwingBreak(bars, l.Price, tol, atr)
	// Ложный пробой раз за полгода есть почти у каждого уровня — как признак это шум.
	// Значимым считаем повторный ЛП либо свежий (последний месяц).
	l.FromLP = lpCount >= 2 || (lpCount == 1 && !lastLP.IsZero() && now.Sub(lastLP).Hours() <= 30*24)
	// Зеркальность — это СМЕНА РОЛИ, а не просто закрытия по обе стороны: последнее верно
	// для любого уровня внутри полугодового диапазона, и тогда «зеркальными» становятся все.
	l.Mirror = mirrorRole(bars, l.Price, tol)
	l.Round = isRound(l.Price)
	l.Above = l.Price > price
	if atr > 0 {
		l.DistATR = math.Abs(l.Price-price) / atr
	}
	if !l.LastTouch.IsZero() {
		l.AgeDays = int(now.Sub(l.LastTouch).Hours() / 24)
	}
	l.Fresh = l.AgeDays <= levelFreshDays

	// Сила: тип задаёт базу, остальное — усилители. Порядок веток повторяет иерархию §2.2
	// сверху вниз: слом тренда сильнее зеркального, зеркальный сильнее исторического и т.д.
	switch {
	case l.Swing:
		l.Kind, l.Strength = "слом тренда", 5
	case l.Mirror:
		l.Kind, l.Strength = "зеркальный", 4.5
	case l.Touches >= 3 && l.Limit:
		l.Kind, l.Strength = "лимитный игрок", 4
	case l.Touches >= 3:
		l.Kind, l.Strength = "исторический", 3.5
	case l.FromLP:
		l.Kind, l.Strength = "ЛП", 3
	case l.Paranorm:
		l.Kind, l.Strength = "паранормальная свеча", 2.5
	case l.Gap:
		l.Kind, l.Strength = "гэп", 2.5
	case l.Touches == 2:
		l.Kind, l.Strength = "проторговка", 2
	default:
		l.Kind, l.Strength = "воздушный", 1
	}
	// Касания усиливают, но не решают: иерархия §2.2 ставит ТИП уровня выше их числа.
	// Без потолка уровень, вдоль которого цена простояла два месяца, набирает силу в разы
	// больше слома тренда и вытесняет из разбора всю остальную разметку.
	if l.Touches > 2 {
		l.Strength += math.Min(float64(l.Touches-2), maxTouchBonus)
	}
	if l.FromLP && l.Kind != "ЛП" {
		l.Strength++
	}
	if l.LongTail {
		l.Strength++
	}
	if l.Round {
		l.Strength *= 1.2
	}
	if !l.Fresh {
		l.Strength *= staleLevelPenalty
	}
}

// isSwingBreak ищет слом тренда — сильнейший тип уровня (§2.2). Формализуемая часть:
// экстремум, который не перебит на окне с обеих сторон, и цена ушла от него больше чем на
// swingAwayATR. Это и есть «точка, где тренд сменил направление»; всё остальное в этом
// определении — субъективное чтение графика, и выдумывать его движок не станет.
func isSwingBreak(bars []Bar, price, tol, atr float64) bool {
	n := len(bars)
	for i := swingWindow; i < n-swingWindow; i++ {
		isHigh, isLow := true, true
		for j := i - swingWindow; j <= i+swingWindow; j++ {
			if j == i {
				continue
			}
			if bars[j].High > bars[i].High {
				isHigh = false
			}
			if bars[j].Low < bars[i].Low {
				isLow = false
			}
		}
		switch {
		case isHigh && math.Abs(bars[i].High-price) <= tol:
			// разворот вниз состоялся, если цена после него ушла достаточно далеко
			if lowestAfter(bars, i) <= bars[i].High-swingAwayATR*atr {
				return true
			}
		case isLow && math.Abs(bars[i].Low-price) <= tol:
			if highestAfter(bars, i) >= bars[i].Low+swingAwayATR*atr {
				return true
			}
		}
	}
	return false
}

func lowestAfter(bars []Bar, i int) float64 {
	lo := bars[i].Low
	for _, b := range bars[i+1:] {
		if b.Low < lo {
			lo = b.Low
		}
	}
	return lo
}

func highestAfter(bars []Bar, i int) float64 {
	hi := bars[i].High
	for _, b := range bars[i+1:] {
		if b.High > hi {
			hi = b.High
		}
	}
	return hi
}

// mirrorRole отвечает на вопрос «работал ли уровень в обеих ролях»: цена должна была ПОЖИТЬ
// над ним и ПОЖИТЬ под ним (серии закрытий), причём сменить сторону хотя бы дважды. Разовые
// заходы туда-сюда зеркальность не делают — иначе признак получают все уровни подряд.
func mirrorRole(bars []Bar, price, tol float64) bool {
	const minRun = 5
	var maxAbove, maxBelow, crossings, run, side int
	for _, b := range bars {
		s := 0
		switch {
		case b.Close > price+tol:
			s = 1
		case b.Close < price-tol:
			s = -1
		default:
			continue // внутри допуска — стороны нет
		}
		if s == side {
			run++
		} else {
			if side != 0 {
				crossings++
			}
			side, run = s, 1
		}
		if s > 0 && run > maxAbove {
			maxAbove = run
		}
		if s < 0 && run > maxBelow {
			maxBelow = run
		}
	}
	return maxAbove >= minRun && maxBelow >= minRun && crossings >= 2
}

// isRound проверяет круглость цены в масштабе самой цены: для BTC круглое — тысячи,
// для монеты за $2 — десятые. Жёсткий шаг тут работать не может.
func isRound(p float64) bool {
	if p <= 0 {
		return false
	}
	step := math.Pow(10, math.Floor(math.Log10(p))-1)
	if step <= 0 {
		return false
	}
	for _, mult := range []float64{10, 5, 2.5} {
		s := step * mult
		if r := math.Mod(p, s); r < s*0.02 || r > s*0.98 {
			return true
		}
	}
	return false
}

// nearestLevels возвращает ближайшую поддержку снизу и сопротивление сверху.
func nearestLevels(levels []Level, price float64) (support, resistance *Level) {
	for i := range levels {
		l := &levels[i]
		if l.Price <= price && (support == nil || l.Price > support.Price) {
			support = l
		}
		if l.Price > price && (resistance == nil || l.Price < resistance.Price) {
			resistance = l
		}
	}
	return support, resistance
}

// ===== тренд =====

// globalTrend определяет направление по структуре хаёв/лоу на D1 и EMA-200 (§3.1).
func globalTrend(bars []Bar) (string, float64) {
	highs := make([]float64, len(bars))
	lows := make([]float64, len(bars))
	closes := make([]float64, len(bars))
	for i, b := range bars {
		highs[i], lows[i], closes[i] = b.High, b.Low, b.Close
	}
	ema200 := EMALast(closes, 200)
	if ema200 == 0 {
		ema200 = EMALast(closes, len(closes)/2)
	}
	structure := SwingStructure(highs, lows)
	price := closes[len(closes)-1]
	switch {
	case structure == "up" && price > ema200:
		return "лонг", ema200
	case structure == "down" && price < ema200:
		return "шорт", ema200
	case structure == "up" || structure == "down":
		return "смешанный (структура и EMA-200 расходятся)", ema200
	default:
		return "боковик", ema200
	}
}

// localTrend смотрит, где цена закрылась относительно уровней и куда смотрят последние
// закрытия (§3.2): «где закрылись относительно уровня — туда и идём».
func localTrend(bars []Bar, levels []Level, price float64) string {
	n := len(bars)
	if n < 4 {
		return "неопределён"
	}
	up, down := 0, 0
	for i := n - 3; i < n; i++ {
		switch {
		case bars[i].Close > bars[i-1].Close:
			up++
		case bars[i].Close < bars[i-1].Close:
			down++
		}
	}
	support, resistance := nearestLevels(levels, price)
	switch {
	case up == 3:
		return "лонг"
	case down == 3:
		return "шорт"
	case up > down && resistance != nil:
		return "лонг (к сопротивлению)"
	case down > up && support != nil:
		return "шорт (к поддержке)"
	default:
		return "боковик"
	}
}

// ===== предпосылки =====

// premises отмечает те предпосылки из §5, которые считаются по цифрам. Субъективные
// («дистрибуция», «зона заражённости») сюда не попадают — придумывать их нельзя.
func premises(bars []Bar, atr float64, support, resistance *Level, price float64, global, local string) []string {
	var out []string
	// Совпадение трендов — предпосылка к пробою (§5.1 п.11): глобальный и локальный смотрят
	// в одну сторону, значит сопротивления по пути меньше.
	if global != "боковик" && len(local) >= len(global) && local[:len(global)] == global {
		out = append(out, "глобальный и локальный тренд совпадают ("+global+") — предпосылка к пробою (§5.1)")
	}
	n := len(bars)
	if n < 4 || atr <= 0 {
		return out
	}
	// Размер трёх последних баров: подход на маленьких — к пробою, на больших — к отбою.
	var sum float64
	for _, b := range bars[n-3:] {
		sum += b.High - b.Low
	}
	avg3 := sum / 3
	switch {
	case avg3 <= smallBarATR*atr:
		out = append(out, "подход на маленьких барах — предпосылка к пробою (§5.1)")
	case avg3 >= bigBarATR*atr:
		out = append(out, "подход на больших барах — предпосылка к отбою (§5.2)")
	}
	// Безоткатное движение больше трёх баров — у уровня захотят выйти.
	runUp, runDown := 0, 0
	for i := n - 1; i > 0 && i > n-6; i-- {
		if bars[i].Close > bars[i-1].Close {
			runUp++
			if runDown > 0 {
				break
			}
			continue
		}
		if bars[i].Close < bars[i-1].Close {
			runDown++
			if runUp > 0 {
				break
			}
		}
	}
	if runUp > 3 || runDown > 3 {
		out = append(out, "длинное безоткатное движение (>3 баров) — предпосылка к отбою (§5.2)")
	}
	// Закрытие близко к уровню — меньше путь до пробоя.
	near := resistance
	if support != nil && (near == nil || price-support.Price < near.Price-price) {
		near = support
	}
	if near != nil {
		if near.DistATR < 0.5 {
			out = append(out, "закрытие близко к уровню (<50% ATR) — предпосылка к пробою (§5.1)")
		} else if near.DistATR > 0.5 {
			out = append(out, "закрытие далеко от уровня (>50% ATR) — предпосылка к отбою (§5.2)")
		}
		if near.AgeDays <= 10 {
			out = append(out, "ближний ретест (≤10 дней) — предпосылка к пробою (§5.1)")
		} else if near.AgeDays > 30 {
			out = append(out, "дальний ретест (>30 дней) — с первого раза не пробьёт (§5.2)")
		}
		if near.EmptyZone {
			out = append(out, "за уровнем пусто — есть куда идти после пробоя (§5.1)")
		}
	}

	// Поджатие: последние бары сжимаются к уровню — накопление энергии перед пробоем.
	if n >= 6 {
		firstHalf := avgRange(bars[n-6 : n-3])
		secondHalf := avgRange(bars[n-3:])
		if secondHalf > 0 && secondHalf <= firstHalf*0.7 {
			out = append(out, "консолидация с поджатием — волатильность сужается (§5.1)")
		}
	}
	// Длинная консолидация: чем дольше стоят в коридоре, тем сильнее выход из него.
	if n >= 10 {
		hi, lo := bars[n-10].High, bars[n-10].Low
		for _, b := range bars[n-10:] {
			hi, lo = math.Max(hi, b.High), math.Min(lo, b.Low)
		}
		if hi-lo <= 2*atr {
			out = append(out, "длинная консолидация (10 дней в коридоре 2 ATR) — копится энергия (§5.1)")
		}
	}
	// Закрытие под самый хай/лоу без хвоста: крупный игрок не успел зайти (§5.1 п.8).
	lastBar := bars[n-1]
	if rng := lastBar.High - lastBar.Low; rng > 0 {
		if (lastBar.High-lastBar.Close)/rng < 0.1 {
			out = append(out, "закрытие под хаем без хвоста — предпосылка к пробою вверх (§5.1)")
		}
		if (lastBar.Close-lastBar.Low)/rng < 0.1 {
			out = append(out, "закрытие под лоем без хвоста — предпосылка к пробою вниз (§5.1)")
		}
	}
	// День открылся с разрывом больше половины ATR — дойти и пробить шансов мало (§5.2 п.4).
	if n >= 2 && math.Abs(lastBar.Open-bars[n-2].Close) > 0.5*atr {
		out = append(out, "день открылся с разрывом >50% ATR — предпосылка к отбою (§5.2)")
	}
	// Экстремум полугодия: наверху никто не хочет быть первым (§5.2 п.9).
	hi, lo := bars[0].High, bars[0].Low
	for _, b := range bars {
		hi, lo = math.Max(hi, b.High), math.Min(lo, b.Low)
	}
	if price >= hi-atr {
		out = append(out, "цена у полугодового максимума — экстремум (§5.2)")
	}
	if price <= lo+atr {
		out = append(out, "цена у полугодового минимума — экстремум (§5.2)")
	}
	return out
}

// avgRange — средний размах группы баров: через него считаются и поджатие, и подход
// маленькими барами.
func avgRange(bars []Bar) float64 {
	if len(bars) == 0 {
		return 0
	}
	var sum float64
	for _, b := range bars {
		sum += b.High - b.Low
	}
	return sum / float64(len(bars))
}
