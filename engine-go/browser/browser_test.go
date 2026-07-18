package browser

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
)

func TestResolveURL(t *testing.T) {
	cases := []struct{ base, ref, want string }{
		{"https://x.com/a/b", "/c", "https://x.com/c"},
		{"https://x.com/a/b", "c", "https://x.com/a/c"},
		{"https://x.com/a/b", "https://y.com/z", "https://y.com/z"},
		{"https://x.com/a/b", "//cdn.com/s.js", "https://cdn.com/s.js"},
		{"https://x.com", "", ""},
	}
	for _, c := range cases {
		if got := resolveURL(c.base, c.ref); got != c.want {
			t.Errorf("resolveURL(%q,%q)=%q, want %q", c.base, c.ref, got, c.want)
		}
	}
}

func tableFromHTML(t *testing.T, html string) Table {
	t.Helper()
	d, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatal(err)
	}
	return parseTable(d.Find("table").First())
}

func TestParseTableWithThead(t *testing.T) {
	tbl := tableFromHTML(t, `<table>
		<thead><tr><th>Pair</th><th>Price</th></tr></thead>
		<tbody><tr><td>BTC</td><td>100</td></tr><tr><td>ETH</td><td>20</td></tr></tbody>
	</table>`)
	if len(tbl.Headers) != 2 || tbl.Headers[0] != "Pair" || tbl.Headers[1] != "Price" {
		t.Fatalf("headers = %v", tbl.Headers)
	}
	if len(tbl.Rows) != 2 || tbl.Rows[0][0] != "BTC" || tbl.Rows[1][1] != "20" {
		t.Fatalf("rows = %v", tbl.Rows)
	}
}

func TestParseTableNoThead(t *testing.T) {
	// First row is treated as the header when there is no <thead>.
	tbl := tableFromHTML(t, `<table>
		<tr><td>A</td><td>B</td></tr>
		<tr><td>1</td><td>2</td></tr>
	</table>`)
	if len(tbl.Headers) != 2 || tbl.Headers[0] != "A" {
		t.Fatalf("headers = %v", tbl.Headers)
	}
	if len(tbl.Rows) != 1 || tbl.Rows[0][0] != "1" || tbl.Rows[0][1] != "2" {
		t.Fatalf("rows = %v", tbl.Rows)
	}
}

func TestTableRecords(t *testing.T) {
	tbl := Table{Headers: []string{"a", "b"}, Rows: [][]string{{"1", "2"}, {"3"}}}
	recs := tbl.Records()
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d", len(recs))
	}
	if recs[0]["a"] != "1" || recs[0]["b"] != "2" {
		t.Errorf("rec0 = %v", recs[0])
	}
	if recs[1]["a"] != "3" || recs[1]["b"] != "" { // short row padded
		t.Errorf("rec1 = %v", recs[1])
	}
}

func TestExportTableFormats(t *testing.T) {
	tbl := Table{Headers: []string{"Pair", "Price"}, Rows: [][]string{{"BTC", "100"}}}

	csv, ext := exportTable(tbl, "csv")
	if ext != "csv" || !strings.Contains(csv, "Pair,Price") || !strings.Contains(csv, "BTC,100") {
		t.Errorf("csv = %q", csv)
	}

	md, ext := exportTable(tbl, "markdown")
	if ext != "md" || !strings.Contains(md, "| Pair | Price |") || !strings.Contains(md, "| BTC | 100 |") {
		t.Errorf("md = %q", md)
	}

	rec, ext := exportTable(tbl, "records")
	if ext != "json" || !strings.Contains(rec, `"Pair": "BTC"`) {
		t.Errorf("records = %q", rec)
	}

	js, ext := exportTable(tbl, "anything-else")
	if ext != "json" || !strings.Contains(js, `"headers"`) {
		t.Errorf("json = %q", js)
	}
}

func TestMarkdownEscapesPipes(t *testing.T) {
	tbl := Table{Headers: []string{"x"}, Rows: [][]string{{"a|b"}}}
	md := tableMarkdown(tbl)
	if !strings.Contains(md, `a\|b`) {
		t.Errorf("pipe not escaped: %q", md)
	}
}

// Regression: sanitizeName used to panic (slice bounds) when the whole overlong name
// parsed as one giant "extension" — reachable from model/web-supplied URLs.
func TestSanitizeNameLongExtension(t *testing.T) {
	got := sanitizeName("x." + strings.Repeat("a", 120))
	if len(got) > 80 {
		t.Errorf("sanitizeName did not cap length: %d", len(got))
	}
	normal := sanitizeName(strings.Repeat("b", 100) + ".css")
	if !strings.HasSuffix(normal, ".css") || len(normal) > 80 {
		t.Errorf("sanitizeName mangled a normal long name: %q", normal)
	}
}

// Regression: capStr used to byte-slice UTF-8 and could split a Cyrillic rune in half.
func TestCapStrKeepsValidUTF8(t *testing.T) {
	s := strings.Repeat("ф", 100)
	got := capStr(s, 51) // 51 lands mid-rune (ф is 2 bytes)
	if !utf8.ValidString(got) {
		t.Errorf("capStr produced invalid UTF-8: %q", got)
	}
}

func TestDecodeDDG(t *testing.T) {
	cases := []struct{ in, want string }{
		// DuckDuckGo redirect wrapper → real target.
		{"//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa%3Fx%3D1&rut=abc", "https://example.com/a?x=1"},
		// Already a direct link → unchanged.
		{"https://example.com/page", "https://example.com/page"},
		// Protocol-relative direct link → https-prefixed.
		{"//cdn.example.com/x", "https://cdn.example.com/x"},
		{"", ""},
	}
	for _, c := range cases {
		if got := decodeDDG(c.in); got != c.want {
			t.Errorf("decodeDDG(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}

func TestToolsCatalogShape(t *testing.T) {
	s := New("")
	tools := s.Tools()
	if len(tools) < 20 {
		t.Fatalf("expected the full tool catalog, got %d tools", len(tools))
	}
	seen := map[string]bool{}
	for _, tl := range tools {
		if tl.Type != "function" {
			t.Errorf("tool %s has type %q, want function", tl.Function.Name, tl.Type)
		}
		if tl.Function.Name == "" || tl.Function.Description == "" {
			t.Errorf("tool with empty name/description: %+v", tl.Function)
		}
		if tl.Function.Parameters["type"] != "object" {
			t.Errorf("tool %s params not an object schema", tl.Function.Name)
		}
		seen[tl.Function.Name] = true
	}
	// Spot-check a few names required by the spec exist.
	for _, want := range []string{"web_search", "open_url", "extract_table", "extract_links", "clone_website", "capture_screenshot", "get_network_requests", "get_websocket_messages", "download_video"} {
		if !seen[want] {
			t.Errorf("required tool %q missing from catalog", want)
		}
	}
}

func TestIsVideoURL(t *testing.T) {
	yes := []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ",
		"https://www.instagram.com/reel/ABC123/",
		"https://www.tiktok.com/@user/video/123",
	}
	no := []string{
		"",
		"not a url",
		"https://example.com/page",
		"скачай https://youtube.com/watch?v=x пожалуйста", // multi-word → use ExtractVideoURL
	}
	for _, u := range yes {
		if !IsVideoURL(u) {
			t.Errorf("IsVideoURL(%q) = false, want true", u)
		}
	}
	for _, u := range no {
		if IsVideoURL(u) {
			t.Errorf("IsVideoURL(%q) = true, want false", u)
		}
	}
	got := ExtractVideoURL("бро скачай https://youtu.be/dQw4w9WgXcQ плиз")
	if !strings.Contains(got, "youtu.be/dQw4w9WgXcQ") {
		t.Errorf("ExtractVideoURL = %q", got)
	}
	if ExtractVideoURL("https://example.com/x") != "" {
		t.Error("example.com should not extract as video URL")
	}
}
