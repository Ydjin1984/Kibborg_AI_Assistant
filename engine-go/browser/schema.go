// Package browser is the Kibborg Browser Agent: it gives the local LLM full access to an
// already-running Google Chrome over the Chrome DevTools Protocol (CDP) via chromedp.
//
// Design rules (from the spec, BROWSER_AGENT.md):
//   - Attach to a Chrome started with --remote-debugging-port=9222. Never launch a new
//     browser; operate on the tabs the user already has open.
//   - The primary data source is the DOM and network traffic — NOT OCR or screenshots.
//     Screenshots are produced only when the user explicitly asks for a visual analysis.
//   - Every capability is exposed as a single tool (OpenAI function-calling schema) so the
//     model can drive the browser autonomously.
package browser

import "time"

// Tab is one open Chrome target (page) as reported by the DevTools /json/list endpoint.
type Tab struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
	Type  string `json:"type"`
}

// Link is one anchor extracted from the DOM.
type Link struct {
	Text string `json:"text"`
	Href string `json:"href"`
}

// ImageRef is one image extracted from the DOM.
type ImageRef struct {
	Src string `json:"src"`
	Alt string `json:"alt,omitempty"`
}

// FormField describes a single input/select/textarea inside a form.
type FormField struct {
	Name        string `json:"name,omitempty"`
	Type        string `json:"type,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Value       string `json:"value,omitempty"`
}

// FormInfo describes a <form> and its fields, so the model can "find the login form".
type FormInfo struct {
	Action string      `json:"action,omitempty"`
	Method string      `json:"method,omitempty"`
	Fields []FormField `json:"fields"`
}

// Table is a parsed HTML <table>: header row + body rows (each cell a string).
type Table struct {
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
}

// Records returns the table as a DataFrame-like slice of objects (Go's pandas-equivalent),
// pairing each header with its cell. Rows with fewer cells than headers are padded.
func (t Table) Records() []map[string]string {
	out := make([]map[string]string, 0, len(t.Rows))
	for _, row := range t.Rows {
		rec := make(map[string]string, len(t.Headers))
		for i, h := range t.Headers {
			if i < len(row) {
				rec[h] = row[i]
			} else {
				rec[h] = ""
			}
		}
		out = append(out, rec)
	}
	return out
}

// CapturedRequest is one network request observed via CDP (Network domain events). It
// covers Document/XHR/Fetch/Script/etc.; the ResourceType field tells them apart so the
// model can ask specifically for XHR/Fetch traffic.
type CapturedRequest struct {
	RequestID    string    `json:"request_id"`
	URL          string    `json:"url"`
	Method       string    `json:"method"`
	ResourceType string    `json:"resource_type"`
	Status       int       `json:"status,omitempty"`
	MimeType     string    `json:"mime_type,omitempty"`
	StartedAt    time.Time `json:"started_at"`
}

// WSMessage is one WebSocket frame (sent or received) observed via CDP.
type WSMessage struct {
	URL       string    `json:"url"`
	Direction string    `json:"direction"` // "sent" | "received"
	Opcode    float64   `json:"opcode"`
	Payload   string    `json:"payload"`
	At        time.Time `json:"at"`
}

// ===== Tool-calling schema (OpenAI function format) =====

// ToolSpec is one tool advertised to the model.
type ToolSpec struct {
	Type     string       `json:"type"` // always "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction is the function description + JSON-Schema of its parameters.
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}
