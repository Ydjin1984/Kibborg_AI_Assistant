package browser

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// Capture buffers are ring-bounded so a long-lived session on a busy page can't grow without
// limit. Newest entries win once the cap is hit.
const (
	maxRequests = 600
	maxWSFrames = 600
)

// enableCaptureDomains turns on the CDP Network domain so request/response/websocket events
// start flowing to onCDPEvent. Called once per attach (see Session.attach).
// captureBufferBytes ограничивает буфер тел ответов, который Chrome держит РАДИ НАС, пока
// включён домен Network.
//
// По умолчанию Chrome не ограничивает его почти ничем. На статичной странице это незаметно, а
// на живом веб-приложении с непрерывным потоком (Telegram Web: вебсокеты, медиа, воркеры)
// буфер растёт, пока рендерер не падает — а с ним и весь браузер, если вкладка была
// единственной. Ровно это и выглядело как «Chrome умирает, стоит агенту подключиться»:
// example.com переживал подключение спокойно, а Telegram Web валил браузер каждый раз.
//
// 8 МБ на всё и 2 МБ на один ответ: get_response_body по-прежнему отдаёт нормальные JSON-ответы
// API, а поток видео просто не копится.
const (
	captureBufferBytes   = 8 << 20
	captureResourceBytes = 2 << 20
)

func enableCaptureDomains() chromedp.Action {
	return network.Enable().
		WithMaxTotalBufferSize(captureBufferBytes).
		WithMaxResourceBufferSize(captureResourceBytes)
}

// onCDPEvent is the single listener installed on the page context. It records network and
// websocket activity into the session buffers — this is the "Network/XHR/Fetch/WebSocket"
// data source the spec wants the agent to read instead of scraping the screen.
func (s *Session) onCDPEvent(ev any) {
	switch e := ev.(type) {
	case *network.EventRequestWillBeSent:
		s.recordRequest(CapturedRequest{
			RequestID:    string(e.RequestID),
			URL:          e.Request.URL,
			Method:       e.Request.Method,
			ResourceType: e.Type.String(),
			StartedAt:    time.Now(),
		})
	case *network.EventResponseReceived:
		s.updateResponse(string(e.RequestID), int(e.Response.Status), e.Response.MimeType)
	case *network.EventWebSocketCreated:
		s.recordWS(WSMessage{URL: e.URL, Direction: "created", At: time.Now()})
	case *network.EventWebSocketFrameReceived:
		s.recordWS(WSMessage{Direction: "received", Opcode: e.Response.Opcode, Payload: e.Response.PayloadData, At: time.Now()})
	case *network.EventWebSocketFrameSent:
		s.recordWS(WSMessage{Direction: "sent", Opcode: e.Response.Opcode, Payload: e.Response.PayloadData, At: time.Now()})
	}
}

func (s *Session) recordRequest(r CapturedRequest) {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	s.requests = append(s.requests, r)
	if len(s.requests) > maxRequests {
		drop := len(s.requests) - maxRequests
		s.requests = s.requests[drop:]
		s.reqIndex = map[string]int{} // indices shifted — rebuild lazily on next update
		for i, req := range s.requests {
			s.reqIndex[req.RequestID] = i
		}
	} else {
		s.reqIndex[r.RequestID] = len(s.requests) - 1
	}
}

func (s *Session) updateResponse(reqID string, status int, mime string) {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	if idx, ok := s.reqIndex[reqID]; ok && idx < len(s.requests) {
		s.requests[idx].Status = status
		s.requests[idx].MimeType = mime
	}
}

func (s *Session) recordWS(m WSMessage) {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	s.wsFrames = append(s.wsFrames, m)
	if len(s.wsFrames) > maxWSFrames {
		s.wsFrames = s.wsFrames[len(s.wsFrames)-maxWSFrames:]
	}
}

// EnableNetworkCapture turns network recording on for the current tab and reports whether it
// had to be switched on just now (in which case the buffers are empty by definition).
//
// Перехват сети больше не включается при подключении к вкладке: на живом веб-приложении он
// роняет рендерер (см. комментарий в Session.attach). Поэтому его включают инструменты,
// которым он реально нужен, — и честно сообщают, что запись начинается с этого момента.
func (s *Session) EnableNetworkCapture() (justEnabled bool, err error) {
	s.actMu.Lock()
	defer s.actMu.Unlock()
	if s.netEnabled {
		return false, nil
	}
	if err := s.ensureNetworkCapture(); err != nil {
		return false, err
	}
	return true, nil
}

// networkJustEnabledNote is what the model is told instead of an empty (and misleading) list.
const networkJustEnabledNote = "Перехват сети включён только что — до этого момента запросы не " +
	"записывались, поэтому список пуст. Повтори действие на странице (или перезагрузи её через " +
	"open_url) и спроси снова: теперь всё будет записано."

// Requests returns a snapshot of captured network requests. A non-empty filter keeps only
// requests whose resource type matches case-insensitively (e.g. "xhr", "fetch", "document").
func (s *Session) Requests(filter string) []CapturedRequest {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	filter = strings.ToLower(strings.TrimSpace(filter))
	out := make([]CapturedRequest, 0, len(s.requests))
	for _, r := range s.requests {
		if filter != "" && !strings.Contains(strings.ToLower(r.ResourceType), filter) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// WebSocketMessages returns a snapshot of captured WebSocket frames.
func (s *Session) WebSocketMessages() []WSMessage {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	out := make([]WSMessage, len(s.wsFrames))
	copy(out, s.wsFrames)
	return out
}

// GetResponseBody fetches the body of a previously captured response by request id. CDP can
// only return a body while it is still buffered, so this works best right after the request.
func (s *Session) GetResponseBody(reqID string) (string, error) {
	if strings.TrimSpace(reqID) == "" {
		return "", fmt.Errorf("нужен request_id (возьми его из get_network_requests)")
	}
	var body string
	err := s.run(chromedp.ActionFunc(func(ctx context.Context) error {
		raw, err := network.GetResponseBody(network.RequestID(reqID)).Do(ctx)
		if err != nil {
			return err
		}
		body = string(raw)
		return nil
	}))
	if err != nil {
		return "", fmt.Errorf("тело ответа недоступно: %w", err)
	}
	// cdproto's GetResponseBodyParams.Do already base64-decodes bodies the CDP marks as
	// encoded — a second guess-decode here would corrupt legit alnum bodies (hex digests,
	// tokens, real base64 payloads).
	return body, nil
}
