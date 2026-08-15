package main

// Данные, которые движок уже знает, но панель раньше не показывала: переписка веб-чата
// (переживает F5), файлы задач одним списком и дневные свечи под график уровней.
//
// Все три — ЧТЕНИЕ того, что уже лежит в процессе или на диске: ни один из обработчиков не
// запускает агента и не трогает состояние. Поэтому GET, но всё равно за sameOriginGuard —
// 127.0.0.1 открыт любой странице в браузере, а список файлов и история диалога наружу не
// предназначены (§6.5).

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"kibborg/engine/trading"
)

//go:embed web/icons/*.svg
var webIcons embed.FS

// webHistoryLimit — сколько реплик отдавать панели. Окно истории в движке и так короткое
// (maxHistory), но лимит здесь фиксирует контракт: панель не должна зависеть от того,
// насколько длинным станет окно в будущем.
const webHistoryLimit = 40

// artifactsLimit — потолок списка файлов. runtime/browser копится месяцами (скрины, ролики,
// выгрузки), и отдавать всё — это мегабайты JSON ради экрана, где видно двадцать плиток.
const artifactsLimit = 200

// candleLimits ограничивает выборку свечей: меньше 20 не из чего строить график, больше 500
// Binance и сам не отдаст одним запросом.
const (
	candleMin     = 20
	candleMax     = 500
	candleDefault = 120
)

// allowedIntervals — таймфреймы, которые панель может попросить. Белый список, а не проверка
// формата: строка из запроса иначе уезжает в URL биржи как есть.
var allowedIntervals = map[string]bool{
	"5m": true, "15m": true, "30m": true, "1h": true, "4h": true, "1d": true,
}

// handleAPIHistory returns the web chat's dialog so a page reload restores it.
// Раньше история жила только в DOM: F5 стирал переписку с экрана, хотя на сервере она
// оставалась и модель её помнила — расхождение, которое читается как «бот всё забыл».
func handleAPIHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	histMu.Lock()
	stored := append([]chatMsg(nil), history[webChatID]...)
	histMu.Unlock()
	if len(stored) > webHistoryLimit {
		stored = stored[len(stored)-webHistoryLimit:]
	}
	msgs := make([]map[string]any, 0, len(stored))
	for _, m := range stored {
		role := "assistant"
		if m.Role == "user" {
			role = "user"
		}
		msgs = append(msgs, map[string]any{"role": role, "text": m.Content})
	}
	writeJSON(w, map[string]any{
		"messages": msgs,
		"context":  contextSnapshot(webCfg, webChatID),
		"pending":  pendingJSON(webChatID),
	})
}

// pendingJSON описывает вопрос, который ждёт ответа: какой инструмент, с какими аргументами,
// по какому правилу и сколько осталось до истечения. Кнопки «да/нет» без этого — предложение
// подтвердить неизвестно что; в Telegram текст вопроса виден всегда, в панели его не было.
func pendingJSON(chatID int64) map[string]any {
	p := peekPending(chatID)
	if p == nil {
		return nil
	}
	args := ""
	if len(p.Args) > 0 {
		if raw, err := json.Marshal(p.Args); err == nil {
			args = capAgentText(string(raw), 600)
		}
	}
	left := max(int(time.Until(p.Deadline).Seconds()), 0)
	return map[string]any{
		"tool":       p.Tool,
		"args":       args,
		"rule":       p.Rule,
		"reason":     p.Reason,
		"task_id":    p.TaskID,
		"expires_in": left,
	}
}

// artifactKind classifies a file for the gallery: картинки показываются превью, ролики —
// плеером, остальное — строкой со ссылкой.
func artifactKind(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp":
		return "image"
	case ".mp4", ".mov", ".mkv", ".webm", ".avi", ".m4v":
		return "video"
	case ".mp3", ".wav", ".ogg", ".oga", ".m4a", ".opus":
		return "audio"
	case ".pdf":
		return "pdf"
	case ".json", ".txt", ".md", ".csv", ".log", ".html", ".xml", ".yaml", ".yml":
		return "text"
	default:
		return "file"
	}
}

// handleAPIArtifacts lists files under runtime/browser, newest first. Это ровно тот корень,
// который отдаёт /api/files — гарантия, что каждая строка списка кликается, а не ведёт в 403.
func handleAPIArtifacts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	root := filepath.Join("runtime", "browser")
	type item struct {
		Name     string `json:"name"`
		Rel      string `json:"rel"`
		URL      string `json:"url"`
		Kind     string `json:"kind"`
		Size     int64  `json:"size"`
		Modified string `json:"modified"`
		AgeSec   int64  `json:"age_sec"`
	}
	var out []item
	now := time.Now()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // недоступная ветка не должна ронять весь список
		}
		if d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		out = append(out, item{
			Name:     d.Name(),
			Rel:      rel,
			URL:      "/api/files/" + rel,
			Kind:     artifactKind(d.Name()),
			Size:     info.Size(),
			Modified: info.ModTime().Format(time.RFC3339),
			AgeSec:   int64(now.Sub(info.ModTime()).Seconds()),
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	total := len(out)
	if len(out) > artifactsLimit {
		out = out[:artifactsLimit]
	}
	writeJSON(w, map[string]any{"files": out, "total": total, "root": filepath.ToSlash(root)})
}

// handleAPICandles отдаёт свечи для графика. Числа те же, что уходят в методику Герчика
// (fetchKlineRows), поэтому нарисованный уровень стоит ровно там, где его посчитал движок:
// собственный источник данных у панели означал бы, что картинка и разбор расходятся.
func handleAPICandles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	symbol := normalizeSymbol(r.URL.Query().Get("symbol"))
	if symbol == "" {
		http.Error(w, "нужен параметр symbol", http.StatusBadRequest)
		return
	}
	interval := strings.TrimSpace(r.URL.Query().Get("tf"))
	if interval == "" {
		interval = "1d"
	}
	if !allowedIntervals[interval] {
		http.Error(w, "таймфрейм не поддерживается: 5m, 15m, 30m, 1h, 4h, 1d", http.StatusBadRequest)
		return
	}
	limit := candleDefault
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if limit < candleMin {
		limit = candleMin
	}
	if limit > candleMax {
		limit = candleMax
	}
	rows, err := fetchKlineRows(symbol, interval, limit)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	type bar struct {
		T int64   `json:"t"`
		O float64 `json:"o"`
		H float64 `json:"h"`
		L float64 `json:"l"`
		C float64 `json:"c"`
		V float64 `json:"v"`
	}
	bars := make([]bar, 0, len(rows))
	for _, k := range rows {
		if len(k) < 6 {
			continue
		}
		bars = append(bars, bar{
			T: int64(anyToF(k[0])),
			O: anyToF(k[1]),
			H: anyToF(k[2]),
			L: anyToF(k[3]),
			C: anyToF(k[4]),
			V: anyToF(k[5]),
		})
	}
	// Формирующаяся свеча на графике нужна (последний тик), но RSI по ней плывёт.
	// Считаем осциллятор по всем пришедшим барам: панель рисует «сейчас», а разбор
	// /analyze по-прежнему берёт только закрытые — два слоя не смешиваются.
	n := len(bars)
	highs := make([]float64, n)
	lows := make([]float64, n)
	closes := make([]float64, n)
	vols := make([]float64, n)
	for i, b := range bars {
		highs[i], lows[i], closes[i], vols[i] = b.H, b.L, b.C, b.V
	}
	osc := trading.BuildOscillatorPane(highs, lows, closes, vols, trading.TrendLabel(closes), 14, 9, 0, 0, 0)
	opens := make([]int64, n)
	for i, b := range bars {
		opens[i] = b.T
	}
	funding := fundingMarksForBars(symbol, interval, opens)
	oiBars, cvdBars := flowPaneForBars(symbol, interval, opens)
	writeJSON(w, map[string]any{
		"symbol": symbol, "interval": interval, "candles": bars,
		"oscillator": osc, "funding": funding,
		"oi": oiBars, "cvd": cvdBars,
	})
}

// handleAPIIcons serves the button icons from web/icons. Они вшиты в бинарь тем же способом,
// что и страница: панель обязана рисоваться из одного exe, без внешней папки рядом.
func handleAPIIcons(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/icons/")
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") ||
		!strings.HasSuffix(name, ".svg") {
		http.NotFound(w, r)
		return
	}
	data, err := webIcons.ReadFile("web/icons/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(data)
}
