package main

// Tools that live in package main rather than in browser/: the trade desk and the security
// desk (ТЗ §5). They are declared as real OpenAI schemas — not "a handler outside the loop" —
// because otherwise «разбери ETH и залогируй» cannot be assembled in the middle of a chain:
// the model has no way to call the second half.
//
// v1 border for `trade` is READ + local journal. Real orders (place_order/cancel_order) do
// NOT belong in this pack when they arrive — see §5 and alwaysAskTools in guard.go.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"kibborg/engine/browser"
	"kibborg/engine/journal"
	"kibborg/engine/secops"
	"kibborg/engine/trading"
)

// localToolNames are the tools dispatched here instead of in browser.Session.Dispatch.
var localToolNames = map[string]bool{
	"analyze_ticker": true, "size_position": true, "journal_add": true, "journal_stats": true,
	"analyze_log": true, "scan_text": true, "audit_file": true,
	"search_hacker_tools": true, "probe_url": true, "write_security_report": true, "download_url": true,
	"analyze_video": true, "transcribe_media": true, "video_frames": true,
	"media_info": true, "convert_media": true, "speak_text": true, "read_document": true,
	"request_pack": true,
}

// tradeToolSpecs is the `trade` pack.
func tradeToolSpecs() []browser.ToolSpec {
	return []browser.ToolSpec{
		spec("analyze_ticker", "Разбор тикера по Binance (числа рынка).", objSchema(map[string]any{
			"symbol": strSchema("BTC, ETHUSDT"),
		}, "symbol")),
		spec("size_position", "Размер позиции по риску.", objSchema(map[string]any{
			"symbol":   strSchema(""),
			"entry":    numSchema(""),
			"stop":     numSchema(""),
			"targets":  strSchema("через запятую"),
			"risk_pct": numSchema("% от счёта"),
			"leverage": numSchema(""),
			"balance":  numSchema(""),
		}, "entry", "stop")),
		spec("journal_add", "Запись сделки в журнал.", objSchema(map[string]any{
			"symbol":    strSchema(""),
			"direction": strSchema("long|short"),
			"entry":     numSchema(""),
			"stop":      numSchema(""),
			"targets":   strSchema("через запятую"),
			"leverage":  numSchema(""),
			"qty":       numSchema("иначе из риска"),
			"note":      strSchema(""),
		}, "symbol", "direction", "entry", "stop")),
		spec("journal_stats", "Статистика журнала.", objSchema(map[string]any{
			"status": strSchema("open|win|loss"),
		})),
	}
}

// secopsToolSpecs is the `secops` pack — defensive analysis + authorized strength-test helpers.
func secopsToolSpecs() []browser.ToolSpec {
	return []browser.ToolSpec{
		spec("analyze_log", "Разбор лога.", objSchema(map[string]any{
			"path": strSchema("пусто = свои логи"),
		})),
		spec("scan_text", "Скан текста на IOC.", objSchema(map[string]any{
			"text": strSchema(""),
		}, "text")),
		spec("audit_file", "Аудит файла: хеши, энтропия.", objSchema(map[string]any{
			"path": strSchema(""),
		}, "path")),
		spec("search_hacker_tools", "Каталог Awesome-Hacking (локальный).", objSchema(map[string]any{
			"query": strSchema("web, appsec, pentest…"),
			"limit": numSchema("макс. записей"),
		})),
		spec("probe_url", "HTTP-проба: статус/заголовки/cookies/пути. НЕ тело JSON — для API download_url/http_get.", objSchema(map[string]any{
			"url": strSchema("https://…"),
		}, "url")),
		spec("download_url", "Скачать URL (JSON/файл) → evidence + /api/files. Для доказательств утечек API.", objSchema(map[string]any{
			"url":      strSchema("https://…"),
			"filename": strSchema("имя файла"),
		}, "url")),
		spec("write_security_report", "MD-отчёт в runtime/browser/security. Нужны target=URL и body=полный Markdown.", objSchema(map[string]any{
			"title":  strSchema(""),
			"target": strSchema("URL цели"),
			"body":   strSchema("находки markdown"),
		}, "body")),
	}
}

// requestPackSpec is the always-on escalation tool (§4.3).
func requestPackSpec() browser.ToolSpec {
	return spec("request_pack",
		"Подключить набор инструментов: web|browser.read|browser.act|console|files|system|media|trade|secops.",
		objSchema(map[string]any{
			"pack":   strSchema(""),
			"reason": strSchema("зачем"),
		}, "pack"))
}

// dispatchLocalTool executes a main-package tool. ok=false means "not mine" so the caller
// falls through to the browser session.
func dispatchLocalTool(t *Task, cfg Config, name, toolCallID string, args map[string]any) (res ToolResult, ok bool) {
	if res, ok := dispatchSystemTool(t, cfg, name, args); ok {
		return res, true
	}
	if res, ok := dispatchVideoTool(t, cfg, name, args); ok {
		return res, true
	}
	if res, ok := dispatchDocumentTool(t, cfg, name, args); ok {
		return res, true
	}
	switch name {
	case "analyze_ticker":
		return toolAnalyzeTicker(cfg, args), true
	case "size_position":
		return toolSizePosition(cfg, args), true
	case "journal_add":
		return toolJournalAdd(t, cfg, toolCallID, args), true
	case "journal_stats":
		return toolJournalStats(t, args), true
	case "analyze_log":
		return toolAnalyzeLog(args), true
	case "scan_text":
		return toolScanText(args), true
	case "audit_file":
		return toolAuditFile(args), true
	case "search_hacker_tools":
		return toolSearchHackerTools(args), true
	case "probe_url":
		return toolProbeURL(args), true
	case "download_url":
		return toolDownloadURL(t, args), true
	case "write_security_report":
		return toolWriteSecurityReport(t, args), true
	}
	return ToolResult{}, false
}

// ===== trade =====

func toolAnalyzeTicker(cfg Config, args map[string]any) ToolResult {
	symbol := argString(args, "symbol")
	if symbol == "" {
		return failResult("нужен symbol (например BTC)", nil)
	}
	report, err := analyzeTicker(symbol)
	if err != nil {
		return failResult("разбор не получился: "+err.Error(), err)
	}
	// Numbers come from Binance, never from a screenshot (§7).
	return okResult(renderReport(report)+sizingBlock(cfg, report), nil)
}

func toolSizePosition(cfg Config, args map[string]any) ToolResult {
	in := trading.SizingInput{
		Direction:   strings.ToLower(argString(args, "direction")),
		Entry:       argFloat(args, "entry"),
		Stop:        argFloat(args, "stop"),
		Balance:     argFloatOr(args, "balance", cfg.TradeBalance),
		RiskPct:     argFloatOr(args, "risk_pct", cfg.TradeRiskPct),
		Leverage:    argFloatOr(args, "leverage", cfg.TradeLeverage),
		Targets:     parseFloatList(argString(args, "targets")),
		MaintMargin: cfg.TradeMMR,
	}
	if in.Entry == 0 || in.Stop == 0 {
		return failResult("нужны entry и stop", nil)
	}
	if in.Direction != "long" && in.Direction != "short" {
		if in.Stop < in.Entry {
			in.Direction = "long"
		} else {
			in.Direction = "short"
		}
	}
	if in.Balance <= 0 {
		return failResult("не задан баланс счёта: передай balance или впиши TRADE_BALANCE в settings.ini", nil)
	}
	s := trading.SizePosition(in)
	if !s.OK {
		return failResult(s.Err, nil)
	}
	return okResult(renderSizing(s, strings.ToUpper(argString(args, "symbol"))), nil)
}

func toolJournalAdd(t *Task, cfg Config, toolCallID string, args map[string]any) ToolResult {
	if tradeJournal == nil {
		return failResult("журнал сделок недоступен", nil)
	}
	symbol := strings.ToUpper(argString(args, "symbol"))
	dir := strings.ToLower(argString(args, "direction"))
	entry, stop := argFloat(args, "entry"), argFloat(args, "stop")
	if symbol == "" || entry == 0 || stop == 0 {
		return failResult("нужны symbol, entry, stop", nil)
	}
	if dir != "long" && dir != "short" {
		return failResult("direction должен быть long или short", nil)
	}
	tr := journal.Trade{
		ChatID:    t.ChatID,
		Symbol:    symbol,
		Direction: dir,
		Entry:     entry,
		Stop:      stop,
		Targets:   parseFloatList(argString(args, "targets")),
		Leverage:  argFloatOr(args, "leverage", cfg.TradeLeverage),
		Note:      argString(args, "note"),
		// §4.2: the ONE place a real idempotency key is needed — a repeated call after a
		// timeout must not open a second position in the journal.
		ClientID: t.ID + ":" + toolCallID,
	}
	sizing := trading.SizingInput{
		Direction: dir, Entry: entry, Stop: stop,
		Balance: cfg.TradeBalance, RiskPct: cfg.TradeRiskPct,
		Leverage: tr.Leverage, Targets: tr.Targets, MaintMargin: cfg.TradeMMR,
	}
	if sizing.Balance > 0 && sizing.RiskPct > 0 {
		if s := trading.SizePosition(sizing); s.OK {
			tr.Qty, tr.Notional, tr.RiskAmount = s.Qty, s.Notional, s.RiskAmount
		}
	}
	if q := argFloat(args, "qty"); q > 0 {
		tr.Qty = q
	}
	id, dup, err := tradeJournal.AddIdempotent(tr)
	if err != nil {
		return failResult("не записал сделку: "+err.Error(), err)
	}
	if dup {
		return okResult(fmt.Sprintf("эта сделка уже записана ранее (#%d) — повторной записи не сделал", id), nil)
	}
	return okResult(fmt.Sprintf("сделка #%d записана: %s %s, вход %s, стоп %s",
		id, strings.ToUpper(dir), symbol, fnum(entry), fnum(stop)), nil)
}

func toolJournalStats(t *Task, args map[string]any) ToolResult {
	if tradeJournal == nil {
		return failResult("журнал сделок недоступен", nil)
	}
	return okResult(handleJournalCommand(t.ChatID, argString(args, "status")), nil)
}

// ===== secops =====

func toolAnalyzeLog(args map[string]any) ToolResult {
	path := argString(args, "path")
	var content, source string
	if path == "" {
		content, source = readOwnLogs()
		if content == "" {
			return failResult("свои логи пусты или недоступны", nil)
		}
	} else {
		abs, err := safeEnginePath(path)
		if err != nil {
			return failResult(err.Error(), err)
		}
		data, rerr := os.ReadFile(abs)
		if rerr != nil {
			return failResult("не прочитал файл: "+redactErr(rerr), rerr)
		}
		content, source = string(capBytes(data, 2<<20)), path
	}
	return okResult(secops.AnalyzeLog(content, source).RenderMarkdown(), nil)
}

func toolScanText(args map[string]any) ToolResult {
	text := argString(args, "text")
	if text == "" {
		return failResult("нужен text", nil)
	}
	return okResult(secops.ScanText(text).RenderMarkdown("Скан текста"), nil)
}

func toolAuditFile(args map[string]any) ToolResult {
	path := argString(args, "path")
	if path == "" {
		return failResult("нужен path", nil)
	}
	abs, err := resolveAuditPath(path)
	if err != nil {
		return failResult(err.Error(), err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return failResult("не прочитал файл: "+redactErr(err), err)
	}
	audit := secops.AuditFile(abs, data)
	out := audit.RenderMarkdown()
	if looksTextish(data) {
		out += "\n\n" + secops.ScanText(string(capBytes(data, 2<<20))).RenderMarkdown("Скан содержимого")
	}
	return okResult(out, nil)
}

// resolveAuditPath allows files under the engine cwd or hands roots — never arbitrary
// system paths like %USERPROFILE%\.ssh (secrets would land in chat via ScanText).
func resolveAuditPath(path string) (string, error) {
	if abs, err := safeEnginePath(path); err == nil {
		return abs, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("путь не разбирается: %w", err)
	}
	abs = filepath.Clean(abs)
	if insideHandsRoots(abs) {
		return abs, nil
	}
	return "", fmt.Errorf("путь вне рабочих каталогов — audit_file читает только engine cwd и hands roots")
}

func toolSearchHackerTools(args map[string]any) ToolResult {
	query := argString(args, "query")
	limit := int(argFloatOr(args, "limit", 20))
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	cat, err := secops.SearchCatalog(query, limit)
	if err != nil {
		return failResult("каталог недоступен: "+err.Error(), err)
	}
	title := "Hacker Tools"
	if query != "" {
		title = "Hacker Tools · " + query
	}
	return okResult(secops.RenderCatalogMarkdown(cat, title), nil)
}

func toolProbeURL(args map[string]any) ToolResult {
	raw := argString(args, "url")
	if raw == "" {
		return failResult("нужен url", nil)
	}
	rep, err := secops.ProbeURL(raw)
	if err != nil {
		return failResult("проба не удалась: "+err.Error(), err)
	}
	return okResult(rep.RenderMarkdown(), nil)
}

func toolDownloadURL(t *Task, args map[string]any) ToolResult {
	res, err := secops.DownloadURL(argString(args, "url"), argString(args, "filename"))
	if err != nil {
		return failResult(err.Error(), err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "сохранено: %s\n", res.Path)
	fmt.Fprintf(&b, "скачать: /api/files/%s\n", res.URL)
	fmt.Fprintf(&b, "bytes=%d content-type=%s\n", res.Bytes, res.ContentType)
	if res.FinalURL != "" {
		fmt.Fprintf(&b, "final-url: %s\n", res.FinalURL)
	}
	if res.Warning != "" {
		fmt.Fprintf(&b, "⚠ %s\n", res.Warning)
	}
	if res.Preview != "" {
		fmt.Fprintf(&b, "preview:\n%s\n", res.Preview)
	}
	b.WriteString("В ответе человеку ОБЯЗАТЕЛЬНО дай кликабельную ссылку /api/files/… — панель покажет кнопку скачивания.")
	// Путь только в Artifacts результата — appendToolMsg сам AddArtifacts (без двойной записи).
	return okResult(b.String(), []string{res.Path})
}

func toolWriteSecurityReport(t *Task, args map[string]any) ToolResult {
	res, err := secops.WriteSecurityReport(secops.SecurityReportInput{
		Title:  argString(args, "title"),
		Target: argString(args, "target"),
		Body:   argString(args, "body"),
	})
	if err != nil {
		return failResult(err.Error(), err)
	}
	return okResult("отчёт записан: "+res.Path+"\nскачать: /api/files/"+res.URL+
		"\nЭто ФИНАЛ задачи: кратко скажи человеку, что проверка завершена, и дай ссылку на отчёт. "+
		"Не уходи в web_search/OWASP после записи отчёта.", []string{res.Path})
}

// ===== schema helpers (mirror browser/tools.go so specs read the same) =====

func spec(name, desc string, params map[string]any) browser.ToolSpec {
	return browser.ToolSpec{
		Type:     "function",
		Function: browser.ToolFunction{Name: name, Description: desc, Parameters: params},
	}
}

func objSchema(p map[string]any, required ...string) map[string]any {
	if p == nil {
		p = map[string]any{}
	}
	m := map[string]any{"type": "object", "properties": p}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

// strSchema/numSchema omit an empty description — see browser.param: every dropped line is
// ~35 bytes off the pack budget checked by TestPackSchemaBudget.
func strSchema(desc string) map[string]any  { return typedSchema("string", desc) }
func numSchema(desc string) map[string]any  { return typedSchema("number", desc) }
func boolSchema(desc string) map[string]any { return typedSchema("boolean", desc) }

func typedSchema(kind, desc string) map[string]any {
	m := map[string]any{"type": kind}
	if desc != "" {
		m["description"] = desc
	}
	return m
}

// argFloat reads a numeric argument that the model may send as a number OR a string.
func argFloat(args map[string]any, key string) float64 {
	return argFloatOr(args, key, 0)
}

func argFloatOr(args map[string]any, key string, def float64) float64 {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case string:
		s := strings.TrimSpace(strings.ReplaceAll(n, ",", "."))
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
	}
	return def
}

func parseFloatList(s string) []float64 {
	var out []float64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(strings.ReplaceAll(part, " ", ""))
		if part == "" {
			continue
		}
		if f, err := strconv.ParseFloat(part, 64); err == nil && f > 0 {
			out = append(out, f)
		}
	}
	return out
}
