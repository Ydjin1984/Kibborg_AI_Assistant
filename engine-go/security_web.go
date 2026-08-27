package main

// Web twin of /stress: catalog browser + strength-test runner + report list.
// Parity with Telegram — same agent brief (stressAuditTask) and same secops tools.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"kibborg/engine/secops"
)

func registerSecurityRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/hacker-tools", sameOriginGuard(handleAPIHackerTools))
	mux.HandleFunc("/api/security_audit", sameOriginGuard(func(w http.ResponseWriter, r *http.Request) {
		handleAPISecurityAudit(w, r, curWebCfg())
	}))
	mux.HandleFunc("/api/security_reports", sameOriginGuard(handleAPISecurityReports))
}

// handleAPIHackerTools returns the local Awesome-Hacking catalog (optionally filtered).
func handleAPIHackerTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 40
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}
	cat, err := secops.SearchCatalog(q, limit)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error(), "entries": []any{}, "count": 0})
		return
	}
	playbook, _ := secops.PlaybookPath()
	local := secops.ProbeLocalTools()
	writeJSON(w, map[string]any{
		"source":        cat.Source,
		"count":         cat.Count,
		"query":         q,
		"playbook":      playbook,
		"entries":       cat.Entries,
		"local_tools":   local,
		"dictionaries":  secops.DictionaryPaths(),
		"local_note":    "Awesome-каталог — ссылки. Сканеры — local_tools (PATH). Словари — dictionaries (SecLists, nuclei-templates).",
		"local_summary": secops.LocalToolsSummary(local),
	})
}

// handleAPISecurityAudit runs the same layered agent as Telegram /stress.
// Accepts JSON {url,focus,mode,task} or multipart/form-data with the same fields + optional file.
func handleAPISecurityAudit(w http.ResponseWriter, r *http.Request, cfg Config) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	urlVal, focusVal, modeVal, taskVal, attachNote, err := parseSecurityAuditRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	mode := normalizeStressMode(modeVal)
	task := strings.TrimSpace(taskVal)
	var target string
	if task == "" {
		combined := strings.TrimSpace(urlVal + " " + focusVal)
		var focus string
		var parsedMode StressMode
		target, focus, parsedMode = splitStressArg(combined)
		if target == "" {
			writeJSON(w, map[string]any{"error": "Укажи URL: например https://example.com"})
			return
		}
		if strings.TrimSpace(modeVal) != "" {
			mode = normalizeStressMode(modeVal)
		} else {
			mode = parsedMode
		}
		if focus == "" {
			focus = strings.TrimSpace(focusVal)
		}
		// attachNote НЕ смешиваем в «Доп. фокус» — отдельным блоком в начале брифа.
		task = stressAuditTask(target, focus, attachNote, mode)
	} else if attachNote != "" {
		task = strings.TrimSpace(attachNote + "\n\n" + task)
	}
	if !brainReady(cfg.BrainPort) {
		writeJSON(w, map[string]any{"error": "Модель ещё грузится. Повтори запрос чуть позже."})
		return
	}

	emit := ndjsonEmitter(w)
	live.begin("security-audit")
	if attachNote != "" {
		webStatus(emit)(statusInfo("📎 Файл из контекста передан агенту (в начале задания)"))
	}
	actor := actorFor(channelWeb, webChatID, true)
	hints := stressHintPacks(mode)
	if attachNote != "" && !hasPack(hints, packFiles) {
		hints = append(hints, packFiles)
	}
	base := withMemory(cfg, webChatID, task, baseMessages(webChatID))
	res := runLayeredAgent(agentRequest{
		cfg: cfg, actor: actor, baseMsgs: base, input: task,
		status:     webStatus(emit),
		memSummary: memorySummaryFor(webChatID),
		hintPacks:  hints,
	})
	live.finish(GenStats{})
	emitAgentResult(emit, task, res, "")
	maybeAutoCompact(cfg, webChatID, nil)
}

func parseSecurityAuditRequest(r *http.Request) (url, focus, mode, task, attachNote string, err error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err = r.ParseMultipartForm(64 << 20); err != nil {
			return "", "", "", "", "", fmt.Errorf("не разобрал форму: %w", err)
		}
		url = strings.TrimSpace(r.FormValue("url"))
		focus = strings.TrimSpace(r.FormValue("focus"))
		mode = strings.TrimSpace(r.FormValue("mode"))
		task = strings.TrimSpace(r.FormValue("task"))
		file, hdr, ferr := r.FormFile("file")
		if ferr == nil {
			defer file.Close()
			data, rerr := io.ReadAll(io.LimitReader(file, 64<<20))
			if rerr != nil {
				return "", "", "", "", "", fmt.Errorf("не прочитал файл: %w", rerr)
			}
			name := hdr.Filename
			mime := hdr.Header.Get("Content-Type")
			attachNote, err = securityAttachmentNote(name, mime, data)
			if err != nil {
				return "", "", "", "", "", err
			}
		}
		return url, focus, mode, task, attachNote, nil
	}
	var req struct {
		URL   string `json:"url"`
		Focus string `json:"focus"`
		Mode  string `json:"mode"`
		Task  string `json:"task"`
	}
	if derr := json.NewDecoder(r.Body).Decode(&req); derr != nil {
		return "", "", "", "", "", fmt.Errorf("нужен JSON {url} или multipart с файлом")
	}
	return strings.TrimSpace(req.URL), strings.TrimSpace(req.Focus),
		strings.TrimSpace(req.Mode), strings.TrimSpace(req.Task), "", nil
}

// securityAttachmentNote saves the upload and returns the prompt block for the agent.
// Text/code is also inlined; office/binaries get a disk path the tools can open.
func securityAttachmentNote(name, mime string, data []byte) (string, error) {
	saved, err := secops.SaveUpload(name, data)
	if err != nil {
		return "", err
	}
	note := secops.AttachmentBrief(name, mime, saved)
	switch classifyMedia(name, mime) {
	case mediaTextFile:
		if utf8.Valid(data) && !bytesHaveNUL(data) {
			content := string(data)
			if len(content) > maxFileChars {
				cut := maxFileChars
				for cut > 0 && !utf8.RuneStart(content[cut]) {
					cut--
				}
				content = content[:cut] + "\n…(обрезано)"
			}
			lang := codeLang[strings.ToLower(filepath.Ext(name))]
			note += "\nПолный текст файла встроен ниже — можно не читать с диска, если хватает:\n```" +
				lang + "\n" + content + "\n```\n"
		}
	case mediaImage:
		note += "\nЭто картинка: при необходимости разбери через зрение/скрин-инструменты или опиши в отчёте путь к файлу.\n"
	case mediaPDF:
		note += "\nЭто PDF: читай через read_document(path=…).\n"
	case mediaVideo, mediaAudio:
		note += "\nМедиафайл: разбор через analyze_video / transcribe_media по пути выше.\n"
	}
	return note, nil
}

// handleAPISecurityReports lists Markdown reports under runtime/browser/security.
func handleAPISecurityReports(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	paths, err := secops.ListSecurityReports(50)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error(), "files": []any{}})
		return
	}
	type item struct {
		Name string `json:"name"`
		Rel  string `json:"rel"`
		URL  string `json:"url"`
		Path string `json:"path"`
	}
	out := make([]item, 0, len(paths))
	for _, p := range paths {
		name := filepath.Base(p)
		rel := filepath.ToSlash(filepath.Join("security", name))
		out = append(out, item{
			Name: name,
			Rel:  rel,
			URL:  "/api/files/" + rel,
			Path: p,
		})
	}
	writeJSON(w, map[string]any{"files": out, "total": len(out)})
}
