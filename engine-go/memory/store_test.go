package memory

import (
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestKeywordRecall(t *testing.T) {
	st := openTemp(t)
	const chat = int64(1)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.AddEpisode(chat, "как настроить стоп-лосс на бирже", "ставь стоп ниже локального минимума", nil))
	must(st.AddEpisode(chat, "какая сегодня погода", "не знаю, я торговый бот", nil))
	must(st.AddEpisode(chat, "расскажи про риск-менеджмент", "рискуй не более 2% депозита на сделку", nil))

	got, err := st.Recall(chat, nil, "напомни про стоп-лосс и риск", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("ожидались релевантные эпизоды, получили 0")
	}
	// The погода episode shares no keywords with the query and must not surface.
	for _, e := range got {
		if e.User == "какая сегодня погода" {
			t.Errorf("нерелевантный эпизод попал в выдачу: %q", e.User)
		}
	}
}

func TestVectorRecallRanksBySimilarity(t *testing.T) {
	st := openTemp(t)
	const chat = int64(7)
	// Three orthogonal-ish unit vectors.
	near := []float32{1, 0, 0}
	mid := []float32{0.6, 0.8, 0}
	far := []float32{0, 0, 1}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.AddEpisode(chat, "near", "a", near))
	must(st.AddEpisode(chat, "mid", "b", mid))
	must(st.AddEpisode(chat, "far", "c", far))

	got, err := st.Recall(chat, []float32{1, 0, 0}, "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("ожидалось ≥2 совпадения выше порога, получили %d", len(got))
	}
	if got[0].User != "near" {
		t.Errorf("самый похожий должен быть 'near', получили %q", got[0].User)
	}
	// 'far' is orthogonal (cosine 0) → below the floor, must be excluded.
	for _, e := range got {
		if e.User == "far" {
			t.Errorf("ортогональный эпизод не должен проходить порог: %q", e.User)
		}
	}
}

func TestSummaryRoundTripAndForget(t *testing.T) {
	st := openTemp(t)
	const chat = int64(42)

	if s, _ := st.GetSummary(chat); s != "" {
		t.Errorf("пустая сводка ожидалась, получили %q", s)
	}
	if err := st.SetSummary(chat, "пользователь торгует BTC, риск 2%"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSummary(chat, "обновлено: торгует BTC и ETH"); err != nil { // upsert
		t.Fatal(err)
	}
	if s, _ := st.GetSummary(chat); s != "обновлено: торгует BTC и ETH" {
		t.Errorf("сводка не обновилась: %q", s)
	}

	_ = st.AddEpisode(chat, "u", "a", nil)
	if n, _ := st.CountEpisodes(chat); n != 1 {
		t.Errorf("ожидался 1 эпизод, получили %d", n)
	}
	if err := st.Forget(chat); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountEpisodes(chat); n != 0 {
		t.Errorf("после Forget эпизодов быть не должно, получили %d", n)
	}
	if s, _ := st.GetSummary(chat); s != "" {
		t.Errorf("после Forget сводка должна быть пустой, получили %q", s)
	}
}

func TestRecentEpisodesChronological(t *testing.T) {
	st := openTemp(t)
	const chat = int64(3)
	_ = st.AddEpisode(chat, "first", "a", nil)
	_ = st.AddEpisode(chat, "second", "b", nil)
	_ = st.AddEpisode(chat, "third", "c", nil)

	got, err := st.RecentEpisodes(chat, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ожидалось 2, получили %d", len(got))
	}
	// Newest two, oldest-first: [second, third].
	if got[0].User != "second" || got[1].User != "third" {
		t.Errorf("неверный порядок: %q, %q", got[0].User, got[1].User)
	}
}
