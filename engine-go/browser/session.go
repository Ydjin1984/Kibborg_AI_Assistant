package browser

import (
	"context"
	"encoding/json"
	"fmt"
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
	pageCtx     context.Context
	pageCancel  context.CancelFunc
	targetID    string

	netMu    sync.Mutex // guards the captured network/websocket buffers
	requests []CapturedRequest
	reqIndex map[string]int // requestID -> position in requests
	wsFrames []WSMessage

	artMu     sync.Mutex
	artifacts []string // file paths produced this turn (screenshots, clones, downloads)
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

// browserWSURL resolves the browser-level WebSocket debugger URL from /json/version. This
// is what chromedp's remote allocator needs to attach to an existing Chrome.
func (s *Session) browserWSURL() (string, error) {
	resp, err := httpClient.Get(s.debugURL + "/json/version")
	if err != nil {
		return "", fmt.Errorf("Chrome не отвечает на %s — запусти Chrome с --remote-debugging-port=9222 (%w)", s.debugURL, err)
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
	resp, err := httpClient.Get(s.debugURL + "/json/list")
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
func (s *Session) ensureAlloc() error {
	if s.allocCtx != nil {
		return nil
	}
	wsURL, err := s.browserWSURL()
	if err != nil {
		return err
	}
	s.allocCtx, s.allocCancel = chromedp.NewRemoteAllocator(
		context.Background(), wsURL, chromedp.NoModifyURL)
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
	if s.targetID == "" {
		tabs, err := s.ListTabs()
		if err != nil {
			return err
		}
		if len(tabs) == 0 {
			return fmt.Errorf("в Chrome нет открытых вкладок — открой страницу и повтори")
		}
		s.targetID = tabs[0].ID
	}
	return s.attach(s.targetID)
}

// detachPage releases the current page context WITHOUT closing the real tab. chromedp's
// cancel-cleanup runs target.CloseTarget for every context created under a RemoteAllocator
// (it marks them all non-first), so cancelling naively would close the user's tab. We detach
// the DevTools session ourselves and hide the target from the cleanup before cancelling.
// Caller must hold actMu.
func (s *Session) detachPage() {
	if s.pageCancel == nil {
		return
	}
	if c := chromedp.FromContext(s.pageCtx); c != nil && c.Target != nil && c.Browser != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if id := c.Target.SessionID; id != "" {
			_ = target.DetachFromTarget().WithSessionID(id).Do(cdp.WithExecutor(ctx, c.Browser))
		}
		cancel()
		c.Target = nil // cleanup goroutine sees no target → it won't CloseTarget the tab
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
	// Listener must be installed before enabling the domains so we don't miss early events.
	chromedp.ListenTarget(ctx, s.onCDPEvent)
	rctx, rcancel := context.WithTimeout(ctx, 15*time.Second)
	defer rcancel()
	if err := chromedp.Run(rctx, enableCaptureDomains()); err != nil {
		// detachPage (not a bare cancel) so a half-attached user tab is never closed;
		// clear targetID so the next action re-picks a live tab instead of retrying a dead id.
		s.detachPage()
		s.targetID = ""
		return fmt.Errorf("не смог присоединиться к вкладке %s: %w", shortID(targetID), err)
	}
	s.targetID = targetID
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
	ctx, cancel := context.WithTimeout(s.pageCtx, 45*time.Second)
	defer cancel()
	err := chromedp.Run(ctx, actions...)
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
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
