package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/PuerkitoBio/goquery"
)

// Web search: Яндекс и Google обязаны участвовать. Их HTML с голого HTTP почти всегда
// капча/429 (проверено: yandex «Вы не робот», google 429, DDG 202). Поэтому живой Chrome
// читает настоящую SERP, а HTTP (Bing + DDG) страхует, если Chrome не поднят.
// Один мёртвый движок больше не обнуляет поиск — в логе топ-5 треков было 4× HTTP 202.

// SearchResult is one search hit.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Source  string `json:"source,omitempty"`
	Excerpt string `json:"excerpt,omitempty"`
}

const searchUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

type searchEngine struct {
	name  string
	url   func(q string) string
	parse func(body []byte) []SearchResult
}

func engineDDG() searchEngine {
	return searchEngine{
		name:  "duckduckgo",
		url:   func(q string) string { return "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(q) },
		parse: parseDDG,
	}
}
func engineDDGLite() searchEngine {
	return searchEngine{
		name:  "duckduckgo",
		url:   func(q string) string { return "https://lite.duckduckgo.com/lite/?q=" + url.QueryEscape(q) },
		parse: parseDDGLite,
	}
}
func engineBing() searchEngine {
	return searchEngine{
		name:  "bing",
		url:   func(q string) string { return "https://www.bing.com/search?q=" + url.QueryEscape(q) + "&setlang=ru" },
		parse: parseBing,
	}
}
func engineYandexHTTP() searchEngine {
	return searchEngine{
		name:  "yandex",
		url:   func(q string) string { return "https://yandex.ru/search/?text=" + url.QueryEscape(q) },
		parse: parseYandex,
	}
}
func engineGoogleHTTP() searchEngine {
	return searchEngine{
		name: "google",
		url: func(q string) string {
			return "https://www.google.com/search?q=" + url.QueryEscape(q) + "&hl=ru&num=10&pws=0&gbv=1"
		},
		parse: parseGoogle,
	}
}

// WebSearch is the keyless HTTP path (no Chrome). Prefer Session.WebSearch — it adds
// Yandex and Google via the live browser when the HTML endpoints show a captcha.
func WebSearch(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	return webSearch(ctx, query, limit, nil)
}

// WebSearch runs Yandex + Google (Chrome) and HTTP engines, then merges unique hits.
func (s *Session) WebSearch(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	return webSearch(ctx, query, limit, s)
}

type enginePack struct {
	src  string
	hits []SearchResult
	err  string
}

func webSearch(ctx context.Context, query string, limit int, chrome *Session) ([]SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("пустой поисковый запрос")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 25 {
		limit = 25
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var mu sync.Mutex
	var packs []enginePack
	add := func(src string, hits []SearchResult, err string) {
		mu.Lock()
		packs = append(packs, enginePack{src, hits, err})
		mu.Unlock()
	}

	var wg sync.WaitGroup
	for _, eng := range []searchEngine{engineYandexHTTP(), engineGoogleHTTP(), engineBing(), engineDDG(), engineDDGLite()} {
		eng := eng
		wg.Add(1)
		go func() {
			defer wg.Done()
			hits, err := fetchEngine(ctx, eng, query)
			if err != nil {
				add(eng.name, nil, err.Error())
				return
			}
			add(eng.name, hits, "")
		}()
	}
	wg.Wait()

	hasHits := func(name string) bool {
		for _, p := range packs {
			if p.src == name && len(p.hits) > 0 {
				return true
			}
		}
		return false
	}
	if chrome != nil {
		if !hasHits("yandex") {
			hits, err := chromeSearch(chrome, "yandex", query)
			if err != nil {
				add("yandex", nil, err.Error())
			} else {
				add("yandex", hits, "")
			}
		}
		if !hasHits("google") {
			hits, err := chromeSearch(chrome, "google", query)
			if err != nil {
				add("google", nil, err.Error())
			} else {
				add("google", hits, "")
			}
		}
	}

	var batches [][]SearchResult
	for _, p := range packs {
		if len(p.hits) > 0 {
			batches = append(batches, p.hits)
		}
	}
	merged := mergeSearch(limit, batches...)
	if len(merged) > 0 {
		return merged, nil
	}
	var fails []string
	seenFail := map[string]bool{}
	for _, p := range packs {
		if p.err == "" || seenFail[p.src+p.err] {
			continue
		}
		seenFail[p.src+p.err] = true
		fails = append(fails, p.src+": "+p.err)
	}
	if len(fails) == 0 {
		return nil, fmt.Errorf("поиск не дал результатов")
	}
	return nil, fmt.Errorf("поиск не дал результатов (%s)", strings.Join(fails, "; "))
}

func fetchEngine(ctx context.Context, eng searchEngine, query string) ([]SearchResult, error) {
	status, body, err := searchGET(ctx, eng.url(query))
	if err != nil {
		return nil, err
	}
	if blockedReason(body) != "" {
		return nil, fmt.Errorf("%s", blockedReason(body))
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", status)
	}
	hits := eng.parse(body)
	if len(hits) == 0 {
		return nil, fmt.Errorf("пустая выдача")
	}
	return tagSource(hits, eng.name), nil
}

func searchGET(ctx context.Context, raw string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", searchUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ru,en;q=0.8")
	req.Header.Set("Cookie", "CONSENT=YES+cb.202104")
	resp, err := cloneHTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	return resp.StatusCode, body, nil
}

func chromeSearch(s *Session, engine, query string) ([]SearchResult, error) {
	var page, js string
	switch engine {
	case "yandex":
		page = "https://yandex.ru/search/?text=" + url.QueryEscape(query)
		js = yandexExtractJS
	case "google":
		page = "https://www.google.com/search?q=" + url.QueryEscape(query) + "&hl=ru&num=10&pws=0"
		js = googleExtractJS
	default:
		return nil, fmt.Errorf("неизвестный движок %s", engine)
	}
	raw, err := s.extractJSON(page, js)
	if err != nil {
		return nil, err
	}
	if blockedReason([]byte(raw)) != "" {
		return nil, fmt.Errorf("%s", blockedReason([]byte(raw)))
	}
	var hits []SearchResult
	if err := json.Unmarshal([]byte(raw), &hits); err != nil {
		return nil, fmt.Errorf("не разобрал выдачу: %w", err)
	}
	if len(hits) == 0 {
		return nil, fmt.Errorf("пустая выдача")
	}
	return tagSource(hits, engine), nil
}

func tagSource(hits []SearchResult, src string) []SearchResult {
	out := make([]SearchResult, 0, len(hits))
	for _, h := range hits {
		h.Source = src
		h.URL = cleanSearchURL(h.URL)
		if h.Title == "" || h.URL == "" {
			continue
		}
		out = append(out, h)
	}
	return out
}

func mergeSearch(limit int, batches ...[]SearchResult) []SearchResult {
	seen := map[string]bool{}
	var out []SearchResult
	more := true
	for i := 0; more && len(out) < limit; i++ {
		more = false
		for _, batch := range batches {
			if i >= len(batch) {
				continue
			}
			more = true
			h := batch[i]
			key := searchDedupKey(h.URL)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, h)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

func searchDedupKey(raw string) string {
	raw = cleanSearchURL(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return strings.ToLower(raw)
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	u.Host = host
	u.Scheme = "https"
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String()
}

func cleanSearchURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "/url?") || strings.Contains(raw, "google.com/url?") {
		if u, err := url.Parse(raw); err == nil {
			if q := u.Query().Get("q"); q != "" {
				return q
			}
			if q := u.Query().Get("url"); q != "" {
				return q
			}
		}
	}
	return decodeDDG(raw)
}

func blockedReason(body []byte) string {
	s := string(body)
	if len(s) > 4000 {
		s = s[:4000]
	}
	low := strings.ToLower(s)
	switch {
	case strings.Contains(s, "Вы не робот"), strings.Contains(s, "SmartCaptcha"),
		strings.Contains(s, "Подтвердите, что запросы"):
		return "капча Яндекса"
	case strings.Contains(low, "unusual traffic"), strings.Contains(low, "detected unusual"):
		return "капча Google"
	case strings.Contains(low, "anomaly-modal"), strings.Contains(low, "anomaly.js"):
		return "антибот DuckDuckGo"
	case strings.Contains(low, `"blocked":true`):
		return "выдача заблокирована"
	}
	return ""
}

func parseDDG(body []byte) []SearchResult {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return nil
	}
	var results []SearchResult
	doc.Find(".result").Each(func(_ int, s *goquery.Selection) {
		a := s.Find("a.result__a").First()
		title := strings.TrimSpace(a.Text())
		href, _ := a.Attr("href")
		link := decodeDDG(href)
		if title == "" || link == "" {
			return
		}
		results = append(results, SearchResult{
			Title: title, URL: link,
			Snippet: strings.TrimSpace(s.Find(".result__snippet").First().Text()),
		})
	})
	return results
}

func parseDDGLite(body []byte) []SearchResult {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return nil
	}
	var results []SearchResult
	doc.Find("a.result-link").Each(func(_ int, a *goquery.Selection) {
		title := strings.TrimSpace(a.Text())
		href, _ := a.Attr("href")
		link := decodeDDG(href)
		if title == "" || link == "" {
			return
		}
		results = append(results, SearchResult{Title: title, URL: link})
	})
	return results
}

func parseBing(body []byte) []SearchResult {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return nil
	}
	var results []SearchResult
	doc.Find("li.b_algo").Each(func(_ int, s *goquery.Selection) {
		a := s.Find("h2 a").First()
		title := strings.TrimSpace(a.Text())
		href, _ := a.Attr("href")
		if title == "" || href == "" {
			return
		}
		results = append(results, SearchResult{
			Title: title, URL: href,
			Snippet: strings.TrimSpace(s.Find(".b_caption p, p").First().Text()),
		})
	})
	return results
}

func parseYandex(body []byte) []SearchResult {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return nil
	}
	var results []SearchResult
	doc.Find("a.OrganicTitle-Link, a.organic__url, li.serp-item h2 a").Each(func(_ int, a *goquery.Selection) {
		title := strings.TrimSpace(a.Text())
		href, _ := a.Attr("href")
		if title == "" || href == "" || strings.Contains(href, "yandex.ru/support") {
			return
		}
		card := a.ParentsFiltered("li.serp-item, .Organic, .organic").First()
		results = append(results, SearchResult{
			Title: title, URL: href,
			Snippet: strings.TrimSpace(card.Find(".OrganicText, .organic__text").First().Text()),
		})
	})
	return results
}

func parseGoogle(body []byte) []SearchResult {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return nil
	}
	var results []SearchResult
	doc.Find("a").Each(func(_ int, a *goquery.Selection) {
		h3 := a.Find("h3").First()
		title := strings.TrimSpace(h3.Text())
		if title == "" {
			return
		}
		href, _ := a.Attr("href")
		href = cleanSearchURL(href)
		if href == "" || strings.Contains(href, "google.com") {
			return
		}
		results = append(results, SearchResult{Title: title, URL: href})
	})
	return results
}

// decodeDDG unwraps DuckDuckGo's redirect links (…/l/?uddg=<real-url>) into the real target.
func decodeDDG(href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if uddg := u.Query().Get("uddg"); uddg != "" {
		return uddg
	}
	return href
}

const yandexExtractJS = `(() => {
  const head = (document.title || "") + (document.body ? document.body.innerText.slice(0, 400) : "");
  if (/Вы не робот|SmartCaptcha|Подтвердите, что запросы/i.test(head)) return JSON.stringify({blocked:true});
  const out = [], seen = new Set();
  document.querySelectorAll("a.OrganicTitle-Link, a.organic__url, li.serp-item h2 a, .OrganicTitle a").forEach(a => {
    const title = (a.innerText || "").trim();
    let href = a.href || "";
    if (!title || !href.startsWith("http") || seen.has(href)) return;
    if (/yandex\.(ru|com)\/(support|search|showcaptcha)/.test(href)) return;
    seen.add(href);
    const card = a.closest("li.serp-item, .Organic, .organic");
    const snip = card ? ((card.querySelector(".OrganicText, .organic__text, .text-container") || {}).innerText || "") : "";
    out.push({title, url: href, snippet: snip.trim().slice(0, 240)});
  });
  return JSON.stringify(out.slice(0, 12));
})()`

const googleExtractJS = `(() => {
  const head = (document.title || "") + (document.body ? document.body.innerText.slice(0, 400) : "");
  if (/unusual traffic|не робот|captcha/i.test(head)) return JSON.stringify({blocked:true});
  const out = [], seen = new Set();
  document.querySelectorAll("a").forEach(a => {
    const h3 = a.querySelector("h3");
    if (!h3) return;
    let href = a.href || "";
    try { if (href.includes("/url?")) href = new URL(href).searchParams.get("q") || href; } catch (e) {}
    if (!href.startsWith("http") || /google\.(com|ru)\//.test(href) || seen.has(href)) return;
    seen.add(href);
    const parent = a.closest("div.g, div[data-sokoban-container]") || a.parentElement;
    const snip = parent ? ((parent.querySelector(".VwiC3b, [data-sncf]") || {}).innerText || "") : "";
    out.push({title: h3.innerText.trim(), url: href, snippet: snip.trim().slice(0, 240)});
  });
  return JSON.stringify(out.slice(0, 12));
})()`
