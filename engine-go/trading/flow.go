package trading

// OI + CVD — четвёртый слой разбора. Это не ещё один осциллятор по цене:
// открытый интерес говорит, приходят ли новые деньги, CVD — кто агрессор (тейкер).
// Герчик и RSI сюда не подмешиваются: расхождение показывается как расхождение.

// FlowSnap — снимок одного ТФ. Все числа уже посчитаны снаружи (свечи + фьючерсное API).
type FlowSnap struct {
	TF          string  `json:"tf"`
	PriceChange float64 `json:"price_change"`  // % закрытие к предыдущему
	OIChangePct float64 `json:"oi_change_pct"` // % OI к предыдущей точке
	CVDDelta    float64 `json:"cvd_delta"`     // buyVol − sellVol за бар
	HasOI       bool    `json:"has_oi"`
	HasCVD      bool    `json:"has_cvd"`
	Quadrant    string  `json:"quadrant"` // new_longs / new_shorts / short_cover / long_liq
	CVDAgree    bool    `json:"cvd_agree"`
	Side        string  `json:"side"` // long / short / ""
	Note        string  `json:"note"`
}

// FlowReport — вердикт слоя. Side ставится только когда ТФ согласны;
// иначе это не направление, а комментарий.
type FlowReport struct {
	Available  bool       `json:"available"`
	Side       string     `json:"side"`
	Quadrant   string     `json:"quadrant"`
	AllowLong  bool       `json:"allow_long"`
	AllowShort bool       `json:"allow_short"`
	LongScore  float64    `json:"long_score"`
	ShortScore float64    `json:"short_score"`
	Snaps      []FlowSnap `json:"snaps"`
	Verdict    string     `json:"verdict"`
	Notes      []string   `json:"notes"`
}

const flowTieMin = 2 // сколько ТФ должны согласиться, чтобы сменить wait/range

// ClassifyFlowSnap раскладывает бар в классический квадрант цена×OI.
func ClassifyFlowSnap(s FlowSnap) FlowSnap {
	if !s.HasOI {
		s.Note = "нет OI"
		return s
	}
	priceUp := s.PriceChange > 0
	oiUp := s.OIChangePct > 0
	switch {
	case priceUp && oiUp:
		s.Quadrant, s.Side, s.Note = "new_longs", "long", "цена и OI растут — приходят новые лонги"
	case priceUp && !oiUp:
		s.Quadrant, s.Note = "short_cover", "цена растёт, OI падает — закрытие шортов, тренд слабый"
	case !priceUp && oiUp:
		s.Quadrant, s.Side, s.Note = "new_shorts", "short", "цена падает, OI растёт — приходят новые шорты"
	default:
		s.Quadrant, s.Note = "long_liq", "цена и OI падают — выход лонгов, импульс слабый"
	}
	if s.HasCVD {
		cvdUp := s.CVDDelta > 0
		s.CVDAgree = (s.Side == "long" && cvdUp) || (s.Side == "short" && !cvdUp)
		if s.Side == "long" && !cvdUp {
			s.Note += "; CVD против — тейкеры продают"
			s.Side = ""
		}
		if s.Side == "short" && cvdUp {
			s.Note += "; CVD против — тейкеры покупают"
			s.Side = ""
		}
		if s.Quadrant == "short_cover" && cvdUp {
			s.Note += "; CVD ещё покупает — покрытие, не набор лонга"
		}
		if s.Quadrant == "long_liq" && !cvdUp {
			s.Note += "; CVD продаёт — ликвидация, не набор шорта"
		}
	}
	return s
}

// AnalyzeFlow сводит снимки 15m/1h/4h в один вердикт.
func AnalyzeFlow(snaps []FlowSnap) FlowReport {
	r := FlowReport{AllowLong: true, AllowShort: true}
	if len(snaps) == 0 {
		r.Notes = append(r.Notes, "нет снимков OI/CVD — слой выключен, направление не трогаю")
		r.Verdict = "Поток недоступен: перпетуала нет или фьючерсное API не ответило."
		return r
	}
	longV, shortV := 0, 0
	has := false
	out := make([]FlowSnap, 0, len(snaps))
	for _, raw := range snaps {
		s := ClassifyFlowSnap(raw)
		out = append(out, s)
		if s.HasOI || s.HasCVD {
			has = true
		}
		switch s.Side {
		case "long":
			longV++
		case "short":
			shortV++
		}
	}
	r.Snaps = out
	r.Available = has
	if !has {
		r.Notes = append(r.Notes, "фьючерсные ряды пустые")
		r.Verdict = "Поток недоступен: нет OI и нет CVD."
		return r
	}

	r.LongScore = flowSideScore(out, "long")
	r.ShortScore = flowSideScore(out, "short")

	switch {
	case longV >= flowTieMin && longV > shortV:
		r.Side, r.Quadrant = "long", majorityQuadrant(out, "long")
		r.AllowShort = false
		r.Verdict = "Поток подтверждает ЛОНГ: новые деньги входят в покупки, CVD не спорит. Шорт фильтром закрыт."
	case shortV >= flowTieMin && shortV > longV:
		r.Side, r.Quadrant = "short", majorityQuadrant(out, "short")
		r.AllowLong = false
		r.Verdict = "Поток подтверждает ШОРТ: новые деньги входят в продажи, CVD не спорит. Лонг фильтром закрыт."
	default:
		r.Verdict = "Поток не даёт стороны: квадранты по ТФ разошлись или CVD спорит с OI. Это не вход."
		if longV > 0 && shortV > 0 {
			r.Notes = append(r.Notes, "ТФ противоречат друг другу по потоку — не усредняю")
		}
	}
	return r
}

// FlowTiebreak — единственное место, где поток может ВЫБРАТЬ направление.
// Только из wait/range, и только если сторона набрана по правилу AnalyzeFlow.
func FlowTiebreak(snaps []FlowSnap) string {
	r := AnalyzeFlow(snaps)
	if !r.Available {
		return ""
	}
	return r.Side
}

// FlowSnapsFrom читает поля, которые main кладёт в timeframe после фьючерсного запроса.
func FlowSnapsFrom(timeframes map[string]interface{}) []FlowSnap {
	if timeframes == nil {
		return nil
	}
	var out []FlowSnap
	for _, name := range []string{"15m", "1h", "4h"} {
		tf, ok := timeframes[name].(map[string]interface{})
		if !ok {
			continue
		}
		_, hasOI := tf["oi_change_pct"].(float64)
		_, hasCVD := tf["cvd_delta"].(float64)
		if !hasOI && !hasCVD {
			continue
		}
		s := FlowSnap{
			TF:          name,
			PriceChange: _numF(tf["change_pct"], 0),
			HasOI:       hasOI,
			HasCVD:      hasCVD,
		}
		if hasOI {
			s.OIChangePct = _numF(tf["oi_change_pct"], 0)
		}
		if hasCVD {
			s.CVDDelta = _numF(tf["cvd_delta"], 0)
		}
		out = append(out, s)
	}
	return out
}

func flowSideScore(snaps []FlowSnap, dir string) float64 {
	if len(snaps) == 0 {
		return 50
	}
	sum := 0.0
	n := 0
	for _, s := range snaps {
		if !s.HasOI && !s.HasCVD {
			continue
		}
		n++
		sc := 50.0
		if s.Side == dir {
			sc = 88
			if s.CVDAgree {
				sc = 96
			}
		} else if s.Side != "" {
			sc = 18
		} else if dir == "long" && (s.Quadrant == "short_cover" || s.Quadrant == "long_liq") {
			sc = 35
		} else if dir == "short" && (s.Quadrant == "short_cover" || s.Quadrant == "long_liq") {
			sc = 35
		}
		sum += sc
	}
	if n == 0 {
		return 50
	}
	return sum / float64(n)
}

func majorityQuadrant(snaps []FlowSnap, side string) string {
	counts := map[string]int{}
	best, n := "", 0
	for _, s := range snaps {
		if s.Side != side || s.Quadrant == "" {
			continue
		}
		counts[s.Quadrant]++
		if counts[s.Quadrant] > n {
			best, n = s.Quadrant, counts[s.Quadrant]
		}
	}
	return best
}

const flowBlend = 0.12

// blendFlowScore подмешивает поток в финальный скор, не ломая веса остальных
// компонент, когда фьючерсных полей нет — старые тесты и старые отчёты не плывут.
func blendFlowScore(final float64, components []ScoreComponent, timeframes map[string]interface{}, dir string, w ScoreWeights) (float64, []ScoreComponent) {
	snaps := FlowSnapsFrom(timeframes)
	if len(snaps) == 0 || dir != "long" && dir != "short" {
		return final, components
	}
	sc := flowSideScore(ClassifyAll(snaps), dir)
	reason := "oi/cvd"
	if r := AnalyzeFlow(snaps); r.Verdict != "" {
		reason = r.Quadrant
		if reason == "" {
			reason = "mixed"
		}
	}
	comp := ScoreComponent{
		Name:   "order_flow",
		Score:  mathRound(sc, 2),
		Weight: flowBlend,
		Reason: reason + " → " + formatF(sc),
	}
	_ = w
	return clamp(final*(1-flowBlend)+sc*flowBlend, 0, 100), append(components, comp)
}

func ClassifyAll(snaps []FlowSnap) []FlowSnap {
	out := make([]FlowSnap, len(snaps))
	for i, s := range snaps {
		out[i] = ClassifyFlowSnap(s)
	}
	return out
}
