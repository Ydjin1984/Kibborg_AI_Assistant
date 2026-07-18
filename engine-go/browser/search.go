package browser

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Web search rounds out the agent's "полноценный веб-сёрчинг": a keyless search over the
// DuckDuckGo HTML endpoint, parsed with goquery. It needs no API key and no running Chrome —
// the agent uses it to FIND pages, then open_url's the best result to work with it.

// SearchResult is one search hit.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// searchUA is a browser-like User-Agent; DuckDuckGo's HTML endpoint rejects the default Go UA.
const searchUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

// WebSearch queries DuckDuckGo and returns up to limit results. It is independent of the
// CDP session (a plain HTTP call), so it works before any tab is open.
func WebSearch(query string, limit int) ([]SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("пустой поисковый запрос")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 25 {
		limit = 25 // the tool description promises "максимум 25", so clamp, don't reset
	}
	req, err := http.NewRequest(http.MethodGet, "https://html.duckduckgo.com/html/?q="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", searchUA)
	resp, err := cloneHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("поиск вернул HTTP %d", resp.StatusCode)
	}
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}
	var results []SearchResult
	doc.Find(".result").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		a := s.Find("a.result__a").First()
		title := strings.TrimSpace(a.Text())
		href, _ := a.Attr("href")
		link := decodeDDG(href)
		if title == "" || link == "" {
			return true // skip ads/empty blocks, keep scanning
		}
		results = append(results, SearchResult{
			Title:   title,
			URL:     link,
			Snippet: strings.TrimSpace(s.Find(".result__snippet").First().Text()),
		})
		return len(results) < limit
	})
	return results, nil
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
		return uddg // url.Values already percent-decodes it
	}
	return href
}
