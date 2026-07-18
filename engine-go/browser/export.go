package browser

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
)

// This file covers the spec's "автоматически преобразовывать данные в JSON / CSV / DataFrame /
// Markdown". Go has no pandas; the DataFrame equivalent is "records" — a slice of objects,
// which we expose as JSON. So the supported formats are: json, records, csv, markdown.

// toJSON marshals v to indented JSON, never erroring (falls back to a quoted message).
func toJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return `"<json error: ` + err.Error() + `>"`
	}
	return string(b)
}

// tableCSV renders a Table as CSV text (RFC 4180, via encoding/csv).
func tableCSV(t Table) string {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if len(t.Headers) > 0 {
		_ = w.Write(t.Headers)
	}
	for _, row := range t.Rows {
		_ = w.Write(row)
	}
	w.Flush()
	return buf.String()
}

// tableMarkdown renders a Table as a GitHub-flavored Markdown table.
func tableMarkdown(t Table) string {
	var b strings.Builder
	headers := t.Headers
	if len(headers) == 0 && len(t.Rows) > 0 {
		// Synthesize column names when the table had no header row.
		for i := range t.Rows[0] {
			headers = append(headers, "col"+itoa(i+1))
		}
	}
	b.WriteString("| " + strings.Join(escapeCells(headers), " | ") + " |\n")
	b.WriteString("|" + strings.Repeat(" --- |", len(headers)) + "\n")
	for _, row := range t.Rows {
		// Pad/truncate the row to the header width so the table stays well-formed.
		cells := make([]string, len(headers))
		for i := range headers {
			if i < len(row) {
				cells[i] = row[i]
			}
		}
		b.WriteString("| " + strings.Join(escapeCells(cells), " | ") + " |\n")
	}
	return b.String()
}

// exportTable converts a table to the requested format and returns the content plus a file
// extension hint. Unknown formats default to JSON records.
func exportTable(t Table, format string) (content, ext string) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "csv":
		return tableCSV(t), "csv"
	case "markdown", "md":
		return tableMarkdown(t), "md"
	case "records":
		return toJSON(t.Records()), "json"
	default: // json
		return toJSON(t), "json"
	}
}

// escapeCells makes cell text safe inside a Markdown table (pipes and newlines).
func escapeCells(cells []string) []string {
	out := make([]string, len(cells))
	for i, c := range cells {
		c = strings.ReplaceAll(c, "|", "\\|")
		c = strings.ReplaceAll(c, "\n", " ")
		out[i] = c
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
