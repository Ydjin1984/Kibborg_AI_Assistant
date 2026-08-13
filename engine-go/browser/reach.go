package browser

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Reach tools — native Go ports of the highest-value Agent Reach capabilities
// (https://github.com/Panniantong/Agent-Reach): clean page reading (Jina), optional Exa
// semantic search via mcporter, agent-reach doctor, YouTube subtitles via yt-dlp.
// Everything else (Twitter, Reddit, gh, bili…) goes through run_command once agent-reach
// (or the upstream CLI) is installed.

const (
	jinaReaderPrefix = "https://r.jina.ai/"
	maxReachBody     = 120_000
	reachHTTPTimeout = 60 * time.Second
	// subtitleTimeout caps the yt-dlp subtitle pass (§4.2: it used to run unbounded off
	// context.Background and survived /stop).
	subtitleTimeout = 180 * time.Second
)

var reachHTTP = &http.Client{Timeout: reachHTTPTimeout}

// ReadURL fetches a clean Markdown/text rendering of any public URL via Jina Reader
// (zero API key). Falls back to a plain HTTP GET + light tag strip if Jina fails.
func ReadURL(ctx context.Context, rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("нужен url")
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	safe, err := safeRemoteURL(rawURL)
	if err != nil {
		return "", err
	}

	jinaURL := jinaReaderPrefix + safe
	body, status, err := httpGetText(ctx, jinaURL, map[string]string{
		"Accept":          "text/plain, text/markdown, */*",
		"User-Agent":      searchUA,
		"X-Return-Format": "markdown",
	})
	if err == nil && status == 200 && strings.TrimSpace(body) != "" {
		return capStr(fmt.Sprintf("source=jina\nurl=%s\n\n%s", safe, body), maxReachBody), nil
	}
	if ctx != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}

	body, status, err = httpGetText(ctx, safe, map[string]string{
		"User-Agent": searchUA,
		"Accept":     "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.8",
	})
	if err != nil {
		return "", fmt.Errorf("не удалось прочитать URL (jina status=%d): %w", status, err)
	}
	if status != 200 {
		return "", fmt.Errorf("HTTP %d при чтении %s", status, safe)
	}
	text := stripHTMLLight(body)
	return capStr(fmt.Sprintf("source=direct\nurl=%s\n\n%s", safe, text), maxReachBody), nil
}

// HTTPGet performs a raw GET against a public URL (for APIs/JSON). Prefer read_url for articles.
func HTTPGet(ctx context.Context, rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("нужен url")
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	safe, err := safeRemoteURL(rawURL)
	if err != nil {
		return "", err
	}
	body, status, err := httpGetText(ctx, safe, map[string]string{
		"User-Agent": searchUA,
		"Accept":     "application/json, text/plain, */*",
	})
	if err != nil {
		return "", err
	}
	return capStr(fmt.Sprintf("status=%d\nurl=%s\n\n%s", status, safe, body), maxReachBody), nil
}

// SemanticSearch tries Exa via mcporter (Agent Reach path). Falls back to DuckDuckGo web_search.
func SemanticSearch(ctx context.Context, query string, limit int) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("пустой query")
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 15 {
		limit = 15
	}

	if mcporter, err := exec.LookPath("mcporter"); err == nil {
		arg := fmt.Sprintf(`exa.web_search_exa(query: %q, numResults: %d)`, query, limit)
		out, runErr := runQuick(ctx, 90*time.Second, mcporter, "call", arg)
		if runErr == nil && strings.TrimSpace(out) != "" {
			return capStr("source=exa/mcporter\n"+out, maxReachBody), nil
		}
		if ctx != nil && ctx.Err() != nil {
			return "", ctx.Err()
		}
	}

	results, err := WebSearch(ctx, query, limit)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "source=duckduckgo\nПоиск не дал результатов.", nil
	}
	return "source=duckduckgo\n" + toJSON(results), nil
}

// AgentReachDoctor runs `agent-reach doctor` (or --json) if the CLI is installed.
func AgentReachDoctor(ctx context.Context, asJSON bool) (string, error) {
	bin, err := lookAgentReach()
	if err != nil {
		return "", fmt.Errorf("agent-reach не найден в PATH. Установка: pip install agent-reach (см. https://github.com/Panniantong/Agent-Reach). Пока доступны встроенные: web_search, read_url, semantic_search, youtube_transcript, github_search, run_command, браузер")
	}
	args := []string{"doctor"}
	if asJSON {
		args = append(args, "--json")
	}
	out, err := runQuick(ctx, 90*time.Second, bin, args...)
	if err != nil && strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("agent-reach doctor: %w", err)
	}
	return capStr(out, maxReachBody), nil
}

// AgentReachRun runs an arbitrary `agent-reach <args…>` subcommand.
func AgentReachRun(ctx context.Context, args []string) (string, error) {
	bin, err := lookAgentReach()
	if err != nil {
		return "", fmt.Errorf("agent-reach не найден: %w", err)
	}
	if len(args) == 0 {
		args = []string{"--help"}
	}
	if len(args) > 0 && (args[0] == "agent-reach" || strings.HasSuffix(args[0], "agent-reach.exe")) {
		args = args[1:]
	}
	out, err := runQuick(ctx, 180*time.Second, bin, args...)
	if err != nil && strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("agent-reach %v: %w", args, err)
	}
	return capStr(out, maxReachBody), nil
}

// YouTubeTranscript extracts subtitles/auto-captions for a video URL via yt-dlp (no full download).
func YouTubeTranscript(ctx context.Context, videoURL, lang string) (string, error) {
	videoURL = strings.TrimSpace(videoURL)
	if videoURL == "" {
		return "", fmt.Errorf("нужен url видео")
	}
	if _, err := safeRemoteURL(videoURL); err != nil {
		return "", err
	}
	lang = strings.TrimSpace(lang)
	if lang == "" {
		lang = "ru,en"
	}
	ytdlp, prefix, err := resolveYtDlp()
	if err != nil {
		return "", fmt.Errorf("yt-dlp не найден: %w", err)
	}
	tmpDir, err := os.MkdirTemp("", "kibborg-subs-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	outTpl := filepath.Join(tmpDir, "%(id)s")
	args := append(append([]string{}, prefix...),
		"--skip-download",
		"--write-subs",
		"--write-auto-subs",
		"--sub-langs", lang,
		"--sub-format", "vtt/srt/best",
		"-o", outTpl,
		"--no-playlist",
		videoURL,
	)
	// Subtitle extraction is a child process too: bind it to the task ctx so /stop kills it.
	raw, runErr := runQuick(ctx, subtitleTimeout, ytdlp, args...)
	if ctx != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}

	entries, _ := os.ReadDir(tmpDir)
	var parts []string
	for _, e := range entries {
		name := e.Name()
		low := strings.ToLower(name)
		if !strings.HasSuffix(low, ".vtt") && !strings.HasSuffix(low, ".srt") && !strings.HasSuffix(low, ".ttml") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(tmpDir, name))
		if rerr != nil {
			continue
		}
		text := cleanSubtitle(string(data))
		if strings.TrimSpace(text) == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("### %s\n%s", name, text))
	}
	if len(parts) == 0 {
		msg := strings.TrimSpace(raw)
		if msg == "" {
			msg = "субтитры не найдены"
		}
		if runErr != nil {
			return "", fmt.Errorf("%s (%v)", msg, runErr)
		}
		return "", fmt.Errorf("%s", msg)
	}
	return capStr(strings.Join(parts, "\n\n"), maxReachBody), nil
}

// GitHubSearch is a thin convenience over `gh search` when gh is installed.
func GitHubSearch(ctx context.Context, kind, query string, limit int) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("пустой query")
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		kind = "repos"
	}
	switch kind {
	case "repos", "code", "issues", "commits":
	default:
		return "", fmt.Errorf("kind должен быть repos|code|issues|commits")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 30 {
		limit = 30
	}
	gh, err := exec.LookPath("gh")
	if err != nil {
		return "", fmt.Errorf("gh CLI не найден. Установи GitHub CLI (https://cli.github.com) или используй run_command / web_search")
	}
	out, err := runQuick(ctx, 60*time.Second, gh, "search", kind, query, "--limit", fmt.Sprintf("%d", limit))
	if err != nil && strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("gh search: %w", err)
	}
	return capStr(out, maxReachBody), nil
}

// ----- helpers -----

func httpGetText(ctx context.Context, raw string, headers map[string]string) (body string, status int, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := reachHTTP.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReachBody+1))
	if err != nil {
		return "", resp.StatusCode, err
	}
	s := string(data)
	if len(data) > maxReachBody {
		s = capStr(s, maxReachBody)
	}
	return s, resp.StatusCode, nil
}

func stripHTMLLight(s string) string {
	var b strings.Builder
	b.Grow(len(s) / 2)
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
			b.WriteByte(' ')
		case !inTag:
			b.WriteRune(r)
		}
	}
	out := b.String()
	out = strings.ReplaceAll(out, "\r", "")
	for strings.Contains(out, "  ") {
		out = strings.ReplaceAll(out, "  ", " ")
	}
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(out)
}

func lookAgentReach() (string, error) {
	if p, err := exec.LookPath("agent-reach"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	var candidates []string
	if home != "" {
		if runtime.GOOS == "windows" {
			candidates = append(candidates,
				filepath.Join(home, ".agent-reach-venv", "Scripts", "agent-reach.exe"),
				filepath.Join(home, "AppData", "Local", "Programs", "Python", "Python312", "Scripts", "agent-reach.exe"),
				filepath.Join(home, "AppData", "Roaming", "Python", "Python312", "Scripts", "agent-reach.exe"),
			)
		} else {
			candidates = append(candidates,
				filepath.Join(home, ".agent-reach-venv", "bin", "agent-reach"),
				filepath.Join(home, ".local", "bin", "agent-reach"),
			)
		}
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("agent-reach не найден")
}

// runQuick shells out with BOTH the task context and its own timeout: /stop cancels the
// task ctx and the child (agent-reach, gh, yt-dlp) dies with it instead of running to its
// own deadline while the user waits for "остановлено".
func runQuick(parent context.Context, timeout time.Duration, bin string, args ...string) (string, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if parent.Err() != nil {
		return string(out), context.Canceled
	}
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), context.DeadlineExceeded
	}
	return string(out), err
}

func cleanSubtitle(s string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "WEBVTT" || strings.HasPrefix(line, "NOTE") {
			continue
		}
		if strings.HasPrefix(line, "Kind:") || strings.HasPrefix(line, "Language:") {
			continue
		}
		if isAllDigits(line) {
			continue
		}
		if strings.Contains(line, "-->") {
			continue
		}
		line = stripHTMLLight(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	var out []string
	var prev string
	for _, l := range lines {
		if l == prev {
			continue
		}
		out = append(out, l)
		prev = l
	}
	return strings.Join(out, "\n")
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
