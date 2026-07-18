package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// This file is the dom_parser from the spec. The strategy is DOM-first: pull the rendered
// outerHTML over CDP once, then parse it in-process with goquery (Go's BeautifulSoup). No
// OCR, no screenshots — structure and text come straight from the DOM.

// DOMSummary is the high-level structure analyze_dom returns so the model can orient itself
// before drilling in.
type DOMSummary struct {
	URL      string   `json:"url"`
	Title    string   `json:"title"`
	Headings []string `json:"headings"`
	Links    int      `json:"links"`
	Images   int      `json:"images"`
	Forms    int      `json:"forms"`
	Tables   int      `json:"tables"`
	Scripts  int      `json:"scripts"`
	TextLen  int      `json:"text_chars"`
}

// htmlAndURL fetches the outerHTML of selector (default whole document) plus the page URL in
// a single CDP round-trip, so relative links can be resolved against the right base.
func (s *Session) htmlAndURL(selector string) (html string, pageURL string, err error) {
	if strings.TrimSpace(selector) == "" {
		selector = "html"
	}
	err = s.run(
		chromedp.Location(&pageURL),
		chromedp.OuterHTML(selector, &html, chromedp.ByQuery),
	)
	return html, pageURL, err
}

// doc parses the selector's HTML into a goquery document and returns the page base URL.
func (s *Session) doc(selector string) (*goquery.Document, string, error) {
	html, pageURL, err := s.htmlAndURL(selector)
	if err != nil {
		return nil, "", err
	}
	d, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, "", err
	}
	return d, pageURL, nil
}

// resolveURL turns a possibly-relative href/src into an absolute URL against the page base.
func resolveURL(base, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

// AnalyzeDOM returns a structural overview of the current page.
func (s *Session) AnalyzeDOM() (DOMSummary, error) {
	d, pageURL, err := s.doc("html")
	if err != nil {
		return DOMSummary{}, err
	}
	var headings []string
	d.Find("h1, h2, h3").Each(func(_ int, sel *goquery.Selection) {
		if t := strings.TrimSpace(sel.Text()); t != "" && len(headings) < 40 {
			headings = append(headings, t)
		}
	})
	return DOMSummary{
		URL:      pageURL,
		Title:    strings.TrimSpace(d.Find("title").First().Text()),
		Headings: headings,
		Links:    d.Find("a[href]").Length(),
		Images:   d.Find("img").Length(),
		Forms:    d.Find("form").Length(),
		Tables:   d.Find("table").Length(),
		Scripts:  d.Find("script").Length(),
		TextLen:  len(strings.TrimSpace(d.Find("body").Text())),
	}, nil
}

// GetHTML returns the outerHTML of the selector (whole document if empty).
func (s *Session) GetHTML(selector string) (string, error) {
	html, _, err := s.htmlAndURL(selector)
	return html, err
}

// GetText returns the visible text of the selector (whole body if empty), whitespace-collapsed.
func (s *Session) GetText(selector string) (string, error) {
	if strings.TrimSpace(selector) == "" {
		selector = "body"
	}
	d, _, err := s.doc(selector)
	if err != nil {
		return "", err
	}
	return strings.Join(strings.Fields(d.Text()), " "), nil
}

// GetAttributes returns the attributes of the first element matching the selector.
func (s *Session) GetAttributes(selector string) (map[string]string, error) {
	if strings.TrimSpace(selector) == "" {
		return nil, fmt.Errorf("нужен CSS-селектор элемента")
	}
	attrs := map[string]string{}
	err := s.run(chromedp.Attributes(selector, &attrs, chromedp.ByQuery))
	return attrs, err
}

// ExtractLinks returns every anchor on the page with absolute hrefs.
func (s *Session) ExtractLinks() ([]Link, error) {
	d, pageURL, err := s.doc("html")
	if err != nil {
		return nil, err
	}
	var links []Link
	d.Find("a[href]").Each(func(_ int, sel *goquery.Selection) {
		href, _ := sel.Attr("href")
		abs := resolveURL(pageURL, href)
		if abs == "" || strings.HasPrefix(abs, "javascript:") {
			return
		}
		links = append(links, Link{Text: strings.TrimSpace(sel.Text()), Href: abs})
	})
	return links, nil
}

// ExtractImages returns every <img> on the page with absolute srcs.
func (s *Session) ExtractImages() ([]ImageRef, error) {
	d, pageURL, err := s.doc("html")
	if err != nil {
		return nil, err
	}
	var imgs []ImageRef
	d.Find("img").Each(func(_ int, sel *goquery.Selection) {
		src, _ := sel.Attr("src")
		if src == "" {
			src, _ = sel.Attr("data-src") // common lazy-load attribute
		}
		abs := resolveURL(pageURL, src)
		if abs == "" {
			return
		}
		alt, _ := sel.Attr("alt")
		imgs = append(imgs, ImageRef{Src: abs, Alt: strings.TrimSpace(alt)})
	})
	return imgs, nil
}

// ExtractTables parses all <table> elements into structured headers+rows.
func (s *Session) ExtractTables() ([]Table, error) {
	d, _, err := s.doc("html")
	if err != nil {
		return nil, err
	}
	var tables []Table
	d.Find("table").Each(func(_ int, t *goquery.Selection) {
		tables = append(tables, parseTable(t))
	})
	return tables, nil
}

// parseTable reads one table: header cells from thead (or the first row), data from the rest.
func parseTable(t *goquery.Selection) Table {
	var tbl Table
	t.Find("thead th, thead td").Each(func(_ int, c *goquery.Selection) {
		tbl.Headers = append(tbl.Headers, strings.TrimSpace(c.Text()))
	})
	rows := t.Find("tbody tr")
	if rows.Length() == 0 {
		rows = t.Find("tr")
	}
	rows.Each(func(i int, tr *goquery.Selection) {
		cells := tr.Find("th, td")
		// If we had no <thead>, treat the first all-cell row as the header.
		if len(tbl.Headers) == 0 && i == 0 {
			cells.Each(func(_ int, c *goquery.Selection) {
				tbl.Headers = append(tbl.Headers, strings.TrimSpace(c.Text()))
			})
			return
		}
		var row []string
		cells.Each(func(_ int, c *goquery.Selection) {
			row = append(row, strings.TrimSpace(c.Text()))
		})
		if len(row) > 0 {
			tbl.Rows = append(tbl.Rows, row)
		}
	})
	return tbl
}

// ExtractForms describes every form so the model can locate login/search/etc. forms.
func (s *Session) ExtractForms() ([]FormInfo, error) {
	d, pageURL, err := s.doc("html")
	if err != nil {
		return nil, err
	}
	var forms []FormInfo
	d.Find("form").Each(func(_ int, f *goquery.Selection) {
		action, _ := f.Attr("action")
		method, _ := f.Attr("method")
		fi := FormInfo{Action: resolveURL(pageURL, action), Method: strings.ToUpper(strings.TrimSpace(method))}
		f.Find("input, select, textarea").Each(func(_ int, in *goquery.Selection) {
			name, _ := in.Attr("name")
			typ, _ := in.Attr("type")
			ph, _ := in.Attr("placeholder")
			val, _ := in.Attr("value")
			if typ == "" {
				typ = goquery.NodeName(in)
			}
			fi.Fields = append(fi.Fields, FormField{Name: name, Type: typ, Placeholder: ph, Value: val})
		})
		forms = append(forms, fi)
	})
	return forms, nil
}

// ExtractJSON harvests embedded JSON: <script type="application/json">, JSON-LD, and any
// element whose text parses as JSON. Returns the raw JSON payloads found.
func (s *Session) ExtractJSON(selector string) ([]json.RawMessage, error) {
	sel := selector
	if strings.TrimSpace(sel) == "" {
		sel = `script[type="application/json"], script[type="application/ld+json"]`
	}
	d, _, err := s.doc("html")
	if err != nil {
		return nil, err
	}
	var out []json.RawMessage
	d.Find(sel).Each(func(_ int, e *goquery.Selection) {
		txt := strings.TrimSpace(e.Text())
		if txt == "" {
			return
		}
		var probe any
		if json.Unmarshal([]byte(txt), &probe) == nil {
			out = append(out, json.RawMessage(txt))
		}
	})
	return out, nil
}

// GetStorage returns localStorage or sessionStorage as a flat key/value map. which is
// "local" or "session".
func (s *Session) GetStorage(which string) (map[string]string, error) {
	which = strings.ToLower(strings.TrimSpace(which))
	store := "localStorage"
	if strings.HasPrefix(which, "session") {
		store = "sessionStorage"
	}
	js := fmt.Sprintf(`(() => { const o = {}; for (let i=0;i<%s.length;i++){const k=%s.key(i); o[k]=%s.getItem(k);} return o; })()`, store, store, store)
	out := map[string]string{}
	if err := s.run(chromedp.Evaluate(js, &out)); err != nil {
		return nil, err
	}
	return out, nil
}

// SimpleCookie is a flattened cookie for the agent.
type SimpleCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires,omitempty"`
	HTTPOnly bool    `json:"http_only"`
	Secure   bool    `json:"secure"`
}

// GetCookies returns the cookies visible to the current page via the CDP Network domain.
func (s *Session) GetCookies() ([]SimpleCookie, error) {
	var cookies []SimpleCookie
	err := s.run(chromedp.ActionFunc(func(ctx context.Context) error {
		got, err := network.GetCookies().Do(ctx)
		if err != nil {
			return err
		}
		for _, c := range got {
			cookies = append(cookies, SimpleCookie{
				Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
				Expires: c.Expires, HTTPOnly: c.HTTPOnly, Secure: c.Secure,
			})
		}
		return nil
	}))
	return cookies, err
}
