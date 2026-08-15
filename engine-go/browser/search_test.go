package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseBing(t *testing.T) {
	html := `<html><body>
<li class="b_algo"><h2><a href="https://example.com/a">Первый хит</a></h2>
<div class="b_caption"><p>сниппет один</p></div></li>
<li class="b_algo"><h2><a href="https://example.com/b">Второй</a></h2>
<p>ещё сниппет</p></li>
</body></html>`
	got := parseBing([]byte(html))
	if len(got) != 2 || got[0].URL != "https://example.com/a" || got[0].Title != "Первый хит" {
		t.Fatalf("parseBing = %+v", got)
	}
	if !strings.Contains(got[0].Snippet, "сниппет") {
		t.Errorf("snippet = %q", got[0].Snippet)
	}
}

func TestParseYandexAndGoogle(t *testing.T) {
	ya := `<html><body>
<a class="OrganicTitle-Link" href="https://music.yandex.ru/chart">Чарт</a>
<div class="OrganicText">новые треки</div>
</body></html>`
	got := parseYandex([]byte(ya))
	if len(got) != 1 || got[0].URL != "https://music.yandex.ru/chart" {
		t.Fatalf("yandex = %+v", got)
	}
	gg := `<html><body>
<a href="/url?q=https%3A%2F%2Fbillboard.com%2Fhot-100&amp;sa=U"><h3>Hot 100</h3></a>
</body></html>`
	got = parseGoogle([]byte(gg))
	if len(got) != 1 || got[0].URL != "https://billboard.com/hot-100" || got[0].Title != "Hot 100" {
		t.Fatalf("google = %+v", got)
	}
}

func TestBlockedReason(t *testing.T) {
	if blockedReason([]byte(`<title>Вы не робот?</title>`)) != "капча Яндекса" {
		t.Error("yandex captcha")
	}
	if blockedReason([]byte(`anomaly-modal__mask anomaly.js`)) != "антибот DuckDuckGo" {
		t.Error("ddg challenge")
	}
	if blockedReason([]byte(`{"blocked":true}`)) == "" {
		t.Error("chrome blocked flag")
	}
	if blockedReason([]byte(`<li class="b_algo">ok</li>`)) != "" {
		t.Error("clean page is not blocked")
	}
}

func TestMergeSearchRoundRobinAndDedup(t *testing.T) {
	ya := []SearchResult{{Title: "Ya", URL: "https://a.example/", Source: "yandex"}}
	gg := []SearchResult{{Title: "G", URL: "https://www.a.example", Source: "google"}} // дубль Ya
	gg2 := []SearchResult{{Title: "G2", URL: "https://b.example", Source: "google"}}
	bi := []SearchResult{{Title: "B", URL: "https://c.example", Source: "bing"}}
	got := mergeSearch(5, ya, gg, bi)
	if len(got) != 2 || got[0].Source != "yandex" || got[1].Source != "bing" {
		t.Fatalf("dedup+order = %+v", got)
	}
	got = mergeSearch(5, ya, gg2, bi)
	if len(got) != 3 || got[0].Source != "yandex" || got[1].Source != "google" || got[2].Source != "bing" {
		t.Errorf("round-robin = %+v", got)
	}
}

func TestWebSearchSurvivesDeadEngine(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("text") != "": // yandex
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<title>Вы не робот?</title>`))
		case strings.Contains(r.URL.Path, "google"):
			w.WriteHeader(http.StatusTooManyRequests)
		case strings.Contains(r.Host, "bing") || q.Get("setlang") == "ru":
			_, _ = w.Write([]byte(`<li class="b_algo"><h2><a href="https://live.example/hit">Живой хит</a></h2><p>ok</p></li>`))
		default: // ddg
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`<form id="challenge-form" action="//duckduckgo.com/anomaly.js"></form>`))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ya := searchEngine{name: "yandex", url: func(q string) string { return srv.URL + "/?text=" + q }, parse: parseYandex}
	bi := searchEngine{name: "bing", url: func(q string) string { return srv.URL + "/bing?q=" + q + "&setlang=ru" }, parse: parseBing}
	hits, err := fetchEngine(context.Background(), ya, "треки")
	if err == nil || !strings.Contains(err.Error(), "капча") {
		t.Fatalf("yandex captcha: hits=%v err=%v", hits, err)
	}
	hits, err = fetchEngine(context.Background(), bi, "треки")
	if err != nil || len(hits) != 1 || hits[0].Title != "Живой хит" {
		t.Fatalf("bing should survive: %+v %v", hits, err)
	}
}

func TestSkipHarvestAndShell(t *testing.T) {
	if !skipHarvestHost("https://www.tiktok.com/discover/x") || !skipHarvestHost("https://vk.ru/music/playlist/1") {
		t.Fatal("tiktok/vk music must be skipped")
	}
	if skipHarvestHost("https://www.maximonline.ru/entertainment/top-5") {
		t.Fatal("article host must be harvested")
	}
	if harvestPriority("https://billboard.com/charts/hot-100") >= harvestPriority("https://example.com/about") {
		t.Fatal("charts should rank above generic pages")
	}
	if !looksLikePageShell("source=jina\nurl=https://x\n\nfunction(){window.gtag()} function(){window.foo}") {
		t.Fatal("script soup must look like a shell")
	}
	if looksLikePageShell("source=jina\nurl=https://x\n\n" + strings.Repeat("Песня исполнителя занимает первое место в чарте. ", 20)) {
		t.Fatal("real article text is not a shell")
	}
	if !SearchHasExcerpts(`параллельно прочитано 3 статей. [{"excerpt":"a"},{"excerpt":"b"}]`) {
		t.Fatal("harvested search must count as a read")
	}
}

func TestFetchEngineHTTP202IsErrorNotEmptyOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`<form action="//duckduckgo.com/anomaly.js"></form>`))
	}))
	t.Cleanup(srv.Close)
	eng := searchEngine{name: "duckduckgo", url: func(string) string { return srv.URL }, parse: parseDDG}
	_, err := fetchEngine(context.Background(), eng, "q")
	if err == nil || !strings.Contains(err.Error(), "антибот") && !strings.Contains(err.Error(), "HTTP 202") {
		t.Fatalf("202 must fail the engine, got %v", err)
	}
}
