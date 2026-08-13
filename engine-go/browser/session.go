package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// DefaultDebugURL is the DevTools HTTP endpoint of a Chrome launched with
// --remote-debugging-port=9222.
const DefaultDebugURL = "http://127.0.0.1:9222"

// Session is a live attachment to a running Chrome. It is safe for concurrent use: every
// browser action is serialized through actMu, because chromedp page contexts are not
// meant to run overlapping actions, and the network buffers are guarded separately.
//
// Lifecycle is lazy: the browser allocator and the attached page context are created on
// first use (so merely constructing a Session never touches Chrome) and reused afterwards.
type Session struct {
	debugURL string

	// FFmpegPath is optional; download_video uses it (or PATH) to mux best video+audio.
	FFmpegPath string

	actMu       sync.Mutex // serializes browser actions + (re)attachment
	allocCtx    context.Context
	allocCancel context.CancelFunc
	allocWS     string // browser-level ws URL the allocator was built on
	pageCtx     context.Context
	pageCancel  context.CancelFunc
	targetID    string
	netEnabled  bool // домен Network включён на текущей вкладке

	netMu    sync.Mutex // guards the captured network/websocket buffers
	requests []CapturedRequest
	reqIndex map[string]int // requestID -> position in requests
	wsFrames []WSMessage

	artMu     sync.Mutex
	artifacts []string // file paths produced this turn (screenshots, clones, downloads)

	// pageMu guards lastReadURL — the URL the model last actually LOOKED at. Every
	// browser.act tool compares the live URL against it before touching the page.
	pageMu      sync.Mutex
	lastReadURL string
}

// New returns a Session bound to the given DevTools URL (empty → DefaultDebugURL). It does
// not connect yet; the first tool call establishes the CDP connection.
func New(debugURL string) *Session {
	if strings.TrimSpace(debugURL) == "" {
		debugURL = DefaultDebugURL
	}
	return &Session{debugURL: debugURL, reqIndex: map[string]int{}}
}

// httpClient is shared for the small DevTools HTTP discovery calls (/json/*).
var httpClient = &http.Client{Timeout: 5 * time.Second}

// devtoolsGet запрашивает /json/… с запасным адресом по IPv6.
//
// Chrome не всегда открывает отладочный порт на 127.0.0.1: на этой машине он поднял его
// ТОЛЬКО на ::1, и всё, что стучалось на IPv4, получало «connection refused». Со стороны это
// выглядит как «Chrome умер сразу после подключения агента» — процесс жив, окна на месте, а
// порт не отвечает. Час на диагностику именно из-за этого и ушёл. Удачный адрес запоминается,
// чтобы не платить за перебор на каждом вызове.
func (s *Session) devtoolsGet(path string) (*http.Response, error) {
	bases := []string{s.debugURL}
	if alt := altLoopback(s.debugURL); alt != "" {
		bases = append(bases, alt)
	}
	var firstErr error
	for _, base := range bases {
		resp, err := httpClient.Get(base + path)
		if err == nil {
			if base != s.debugURL {
				log.Printf("[BROWSER] отладочный порт Chrome отвечает по %s (на %s его нет) — перехожу туда",
					base, s.debugURL)
				s.debugURL = base
			}
			return resp, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

// altLoopback swaps the loopback host between IPv4 and IPv6 forms.
func altLoopback(base string) string {
	switch {
	case strings.Contains(base, "127.0.0.1"):
		return strings.Replace(base, "127.0.0.1", "[::1]", 1)
	case strings.Contains(base, "[::1]"):
		return strings.Replace(base, "[::1]", "127.0.0.1", 1)
	case strings.Contains(base, "localhost"):
		return strings.Replace(base, "localhost", "127.0.0.1", 1)
	}
	return ""
}

// browserWSURL resolves the browser-level WebSocket debugger URL from /json/version. This
// is what chromedp's remote allocator needs to attach to an existing Chrome.
func (s *Session) browserWSURL() (string, error) {
	resp, err := s.devtoolsGet("/json/version")
	if err != nil {
		return "", fmt.Errorf("chrome не отвечает на %s — запусти Chrome с --remote-debugging-port=9222 (%w)", s.debugURL, err)
	}
	defer resp.Body.Close()
	var v struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	if v.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("в ответе /json/version нет webSocketDebuggerUrl")
	}
	return v.WebSocketDebuggerURL, nil
}

// ListTabs returns the open page targets (already-open tabs) via /json/list.
func (s *Session) ListTabs() ([]Tab, error) {
	resp, err := s.devtoolsGet("/json/list")
	if err != nil {
		return nil, fmt.Errorf("не получил список вкладок с %s: %w", s.debugURL, err)
	}
	defer resp.Body.Close()
	var raw []struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	var tabs []Tab
	for _, t := range raw {
		if t.Type != "page" {
			continue // skip service workers, iframes, extension background pages, ...
		}
		tabs = append(tabs, Tab{ID: t.ID, Title: t.Title, URL: t.URL, Type: t.Type})
	}
	return tabs, nil
}

// ensureAlloc lazily creates the remote allocator attached to the running Chrome.
// Caller must hold actMu.
//
// Адрес сверяется КАЖДЫЙ раз, а не берётся из кэша навсегда. Браузерный WebSocket содержит
// идентификатор запуска (ws://127.0.0.1:9222/devtools/browser/<guid>), и после перезапуска
// Chrome старый адрес отвечает 404. На живом прогоне это выглядело так: list_tabs (обычный
// HTTP) работает и показывает вкладки, а switch_tab и get_text падают с «404 на ws://…» —
// и так до перезапуска ВСЕГО движка. Лишний локальный HTTP-запрос стоит доли миллисекунды,
// а /json/list мы всё равно дёргаем рядом.
func (s *Session) ensureAlloc() error {
	wsURL, err := s.browserWSURL()
	if err != nil {
		return err
	}
	if s.allocCtx != nil {
		if s.allocWS == wsURL {
			return nil
		}
		log.Printf("[BROWSER] Chrome перезапустился (новый адрес отладки) — пересоздаю подключение")
		s.detachPage()
		if s.allocCancel != nil {
			s.allocCancel()
		}
		s.allocCtx, s.allocCancel = nil, nil
		s.targetID = ""
	}
	s.allocCtx, s.allocCancel = chromedp.NewRemoteAllocator(
		context.Background(), wsURL, chromedp.NoModifyURL)
	s.allocWS = wsURL
	return nil
}

// ensurePage lazily attaches a chromedp context to a concrete page target and starts the
// network/websocket capture on it. Caller must hold actMu. If no target is chosen yet it
// picks the first real page tab (so the agent works on what the user already has open).
func (s *Session) ensurePage() error {
	if s.pageCtx != nil && s.pageCtx.Err() == nil {
		return nil
	}
	if err := s.ensureAlloc(); err != nil {
		return err
	}
	if s.targetID != "" {
		return s.attach(s.targetID)
	}
	tabs, err := s.ListTabs()
	if err != nil {
		return err
	}
	if len(tabs) == 0 {
		return fmt.Errorf("в Chrome нет открытых вкладок — открой страницу и повтори")
	}
	// Try tabs in order instead of always the first one. /json/list keeps listing targets that
	// no longer accept a WebSocket connection, and attaching to tabs[0] unconditionally pinned
	// the session to a dead tab on the first live run: four browser tools in a row failed with
	// «could not dial ws://…» for the same target, and browser work stayed broken until the
	// process was restarted.
	var firstErr error
	for i, tab := range tabs {
		if i >= maxAttachAttempts {
			break
		}
		s.targetID = tab.ID
		if aerr := s.attach(tab.ID); aerr == nil {
			return nil
		} else if firstErr == nil {
			firstErr = aerr
		}
	}
	s.targetID = ""
	return firstErr
}

// maxAttachAttempts bounds the search for a live tab: with many open tabs a full sweep would
// cost a full attach timeout each.
const maxAttachAttempts = 3

// attachTimeout ограничивает ожидание присоединения к вкладке. Ждём снаружи, через select:
// отменять контекст страницы ради таймаута нельзя — это закроет вкладку пользователя.
const attachTimeout = 15 * time.Second

// detachPage releases the current page context WITHOUT closing the real tab. chromedp's
// cancel-cleanup runs target.CloseTarget for every context created under a RemoteAllocator
// (it marks them all non-first), so cancelling naively would close the user's tab. We detach
// the DevTools session ourselves and hide the target from the cleanup before cancelling.
// Caller must hold actMu.
func (s *Session) detachPage() {
	if s.pageCancel == nil {
		return
	}
	// Writing to c.Target is only safe while chromedp's cleanup goroutine is still parked on
	// <-ctx.Done(). Once the context is cancelled that goroutine owns c: it checks
	// `c.Target == nil`, then dereferences c.Target and c.Browser several lines later without
	// re-checking. A nil written into that window panics INSIDE the library, in a goroutine
	// nobody can recover — the whole bot dies.
	//
	// That is exactly what happened twice on the first live run: chromedp cancels a page
	// context by itself when its target goes away, ensurePage then re-attaches, and attach
	// calls detachPage on the already-cancelled context.
	//
	// So: touch c ONLY while the context is still alive. If it is already cancelled, chromedp
	// is mid-cleanup and there is nothing of ours left to protect — let go.
	if s.pageCtx.Err() == nil {
		if c := chromedp.FromContext(s.pageCtx); c != nil {
			if c.Target != nil && c.Browser != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				if id := c.Target.SessionID; id != "" {
					_ = target.DetachFromTarget().WithSessionID(id).Do(cdp.WithExecutor(ctx, c.Browser))
				}
				cancel()
			}
			// Cleared unconditionally (not only when Browser != nil): a half-finished attach
			// leaves Target set with Browser still nil, and the cleanup goroutine would then
			// dereference that nil Browser. Nil-ing Target is also what stops chromedp from
			// closing the user's own tab, which it would otherwise do for every context
			// created under a RemoteAllocator (c.first is always false there).
			c.Target = nil
		}
	}
	s.pageCancel()
	s.pageCtx, s.pageCancel = nil, nil
}

// attach (re)binds the page context to a target id and wires up network capture.
// Caller must hold actMu.
func (s *Session) attach(targetID string) error {
	s.detachPage()
	ctx, cancel := chromedp.NewContext(s.allocCtx, chromedp.WithTargetID(target.ID(targetID)))
	s.pageCtx, s.pageCancel = ctx, cancel
	s.netEnabled = false
	// Listener must be installed before enabling the domains so we don't miss early events.
	chromedp.ListenTarget(ctx, s.onCDPEvent)
	// Первый Run идёт на САМОМ pageCtx, а не на дочернем контексте с таймаутом.
	//
	// Это была причина «Chrome умирает, стоит агенту подключиться». RemoteAllocator.Allocate
	// вешает свою уборку на ТОТ контекст, на котором произошла аллокация:
	//
	//	go func() { <-ctx.Done(); Cancel(ctx); закрыть вебсокет }()
	//
	// Мы передавали туда 15-секундный дочерний контекст и отменяли его тем же `defer` в конце
	// attach. Через мгновение уборка отменяла УЖЕ САМ pageCtx, а её последний шаг —
	// target.CloseTarget: chromedp закрывал вкладку, к которой мы только что присоединились.
	// Вкладка была единственной — Chrome выходил целиком. Отсюда и обманчивая картина: с тремя
	// вкладками «всё работает», а на одной «браузер падает от подключения».
	//
	// Ограничение по времени осталось, но снаружи: ждём в select и НЕ отменяем pageCtx, а
	// уходим через detachPage — он сначала снимает Target, и уборке уже нечего закрывать.
	//
	// Присоединяемся БЕЗ включения домена Network — перехват сети поднимается лениво, только
	// когда его действительно спросили (см. ensureNetworkCapture).
	//
	// Измерено, а не предположено: подключение с network.Enable роняло рендерер Telegram Web
	// КАЖДЫЙ раз. В прогоне с двумя вкладками умирала ровно вкладка Telegram, а example.com и
	// сам браузер оставались живы; когда Telegram был единственной вкладкой, вместе с ней
	// уходил весь Chrome — и это выглядело как «браузер умирает от подключения агента».
	// Живому веб-приложению с постоянным потоком (вебсокеты, медиа, воркеры) инструментовка
	// сети обходится слишком дорого, а нужна она в одной задаче из двадцати.
	done := make(chan error, 1)
	go func() { done <- chromedp.Run(ctx) }()
	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(attachTimeout):
		runErr = fmt.Errorf("вкладка не ответила за %s", attachTimeout)
	}
	if runErr != nil {
		// detachPage (not a bare cancel) so a half-attached user tab is never closed;
		// clear targetID so the next action re-picks a live tab instead of retrying a dead id.
		s.detachPage()
		s.targetID = ""
		return fmt.Errorf("не смог присоединиться к вкладке %s: %w", shortID(targetID), runErr)
	}
	s.targetID = targetID
	return nil
}

// ensureNetworkCapture turns the Network domain on for the current page, once.
// Caller must hold actMu.
func (s *Session) ensureNetworkCapture() error {
	if s.netEnabled {
		return nil
	}
	if err := s.ensurePage(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.pageCtx, 15*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx, enableCaptureDomains()); err != nil {
		return fmt.Errorf("не смог включить перехват сети: %w", err)
	}
	s.netEnabled = true
	return nil
}

// targetAlive reports whether the attached target still exists in Chrome. Caller must hold actMu.
func (s *Session) targetAlive() bool {
	if s.targetID == "" {
		return false
	}
	tabs, err := s.ListTabs()
	if err != nil {
		return false
	}
	for _, t := range tabs {
		if t.ID == s.targetID {
			return true
		}
	}
	return false
}

// run executes chromedp actions on the current page under the action lock, attaching first
// if needed. This is the single funnel every controller/dom method goes through.
func (s *Session) run(actions ...chromedp.Action) error {
	s.actMu.Lock()
	defer s.actMu.Unlock()
	if err := s.ensurePage(); err != nil {
		return err
	}
	err := s.runAttached(actions...)
	// A page context can go stale between tasks (the tab navigated, Chrome dropped the
	// DevTools session). chromedp then reports a bare "context canceled", which is neither
	// the user's doing nor a permanent failure — seen on the first live run, where the next
	// task's get_text failed this way and the model told the user their request had been
	// interrupted. Re-attach once and retry before giving up.
	if err != nil && isStalePageErr(err) && s.targetAlive() {
		s.detachPage()
		if aerr := s.ensurePage(); aerr == nil {
			err = s.runAttached(actions...)
		}
	}
	if err != nil && !s.targetAlive() {
		// The user closed the tab we were attached to: an externally closed target does NOT
		// cancel the page context, so without this the session would error forever. Heal the
		// state and tell the model what happened.
		s.detachPage()
		s.targetID = ""
		return fmt.Errorf("вкладка, с которой работал агент, была закрыта — повтори действие, я переключусь на другую открытую вкладку")
	}
	return err
}

// runAttached runs the actions on the already-attached page. Caller must hold actMu.
func (s *Session) runAttached(actions ...chromedp.Action) error {
	ctx, cancel := context.WithTimeout(s.pageCtx, 45*time.Second)
	defer cancel()
	return chromedp.Run(ctx, actions...)
}

// isStalePageErr reports whether the error means "this DevTools session is gone", as opposed
// to a genuine page/selector problem.
func isStalePageErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "session closed") ||
		strings.Contains(msg, "target closed")
}

// SwitchTab points the session at another open tab, by 0-based index or by a substring of
// its URL/title. Subsequent tools act on this tab.
func (s *Session) SwitchTab(index int, match string) (Tab, error) {
	tabs, err := s.ListTabs()
	if err != nil {
		return Tab{}, err
	}
	if len(tabs) == 0 {
		return Tab{}, fmt.Errorf("нет открытых вкладок")
	}
	var chosen *Tab
	if match != "" {
		m := strings.ToLower(match)
		for i := range tabs {
			if strings.Contains(strings.ToLower(tabs[i].URL), m) || strings.Contains(strings.ToLower(tabs[i].Title), m) {
				chosen = &tabs[i]
				break
			}
		}
		if chosen == nil {
			return Tab{}, fmt.Errorf("вкладка по совпадению %q не найдена", match)
		}
	} else {
		if index < 0 || index >= len(tabs) {
			return Tab{}, fmt.Errorf("индекс вкладки %d вне диапазона 0..%d", index, len(tabs)-1)
		}
		chosen = &tabs[index]
	}
	s.actMu.Lock()
	defer s.actMu.Unlock()
	if err := s.ensureAlloc(); err != nil {
		return Tab{}, err
	}
	if err := s.attach(chosen.ID); err != nil {
		return Tab{}, err
	}
	return *chosen, nil
}

// tabURL reports the URL of the tab the session is bound to, over the DevTools HTTP endpoint.
//
// Deliberately NOT chromedp: the tab-change anchor must be cheap and must never attach a page
// by itself. Driving the page just to learn its URL is what made `list_tabs` attach a target
// on the first live run and turned a latent chromedp cleanup race into a process crash.
func (s *Session) tabURL() string {
	tabs, err := s.ListTabs()
	if err != nil || len(tabs) == 0 {
		return ""
	}
	s.actMu.Lock()
	id := s.targetID
	s.actMu.Unlock()
	if id != "" {
		for _, t := range tabs {
			if t.ID == id {
				return t.URL
			}
		}
		return "" // our tab is gone; run() reports that properly on the next action
	}
	return tabs[0].URL // nothing attached yet — ensurePage would pick the first page tab
}

// noteCurrentPage remembers the URL of the tab the model just read, so a later browser.act
// call can tell "same page" from "the page moved under us".
func (s *Session) noteCurrentPage() {
	if url := s.tabURL(); url != "" {
		s.pageMu.Lock()
		s.lastReadURL = url
		s.pageMu.Unlock()
	}
}

// ensureSamePage refuses a mutating browser action when the active tab navigated away since
// the last read (redirect, popup, user switching tabs). Returning an error — instead of
// clicking blind — is what stops "жми кнопку" from landing on a completely different site.
func (s *Session) ensureSamePage() error {
	s.pageMu.Lock()
	want := s.lastReadURL
	s.pageMu.Unlock()
	if want == "" {
		return nil // nothing read yet this task — nothing to contradict
	}
	got := s.tabURL()
	if got == "" || sameLocation(want, got) {
		// Unknown URL is not evidence of a change: let the action proceed and let the real
		// browser error (closed tab, missing selector) speak for itself.
		return nil
	}
	return fmt.Errorf("страница сменилась (было %s, стало %s) — перечитай её (get_text/analyze_dom) и повтори действие", want, got)
}

// ResetPageAnchor clears the "last read page" anchor. The agent calls it at task start so a
// stale URL from a previous task never blocks the first action of the next one.
func (s *Session) ResetPageAnchor() {
	s.pageMu.Lock()
	s.lastReadURL = ""
	s.pageMu.Unlock()
}

// sameLocation compares two URLs ignoring the fragment: an in-page #anchor is not a
// navigation, a different path or host is.
func sameLocation(a, b string) bool {
	strip := func(u string) string {
		if i := strings.IndexByte(u, '#'); i >= 0 {
			u = u[:i]
		}
		return strings.TrimRight(u, "/")
	}
	return strip(a) == strip(b)
}

// addArtifact records a file produced during a tool call (screenshot, clone, download) so
// the agent layer can deliver it to the user.
func (s *Session) addArtifact(path string) {
	s.artMu.Lock()
	s.artifacts = append(s.artifacts, path)
	s.artMu.Unlock()
}

// TakeArtifacts returns and clears the artifacts gathered since the last call.
func (s *Session) TakeArtifacts() []string {
	s.artMu.Lock()
	defer s.artMu.Unlock()
	out := s.artifacts
	s.artifacts = nil
	return out
}

// Close releases the chromedp contexts. It does NOT close the user's Chrome — we only
// detach our DevTools session.
func (s *Session) Close() {
	s.actMu.Lock()
	defer s.actMu.Unlock()
	s.detachPage()
	if s.allocCancel != nil {
		s.allocCancel()
		s.allocCtx, s.allocCancel = nil, nil
	}
	s.allocWS = ""
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
