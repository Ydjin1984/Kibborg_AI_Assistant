package main

// Правило «нет опоры — переспроси». Тест написан несимметрично намеренно: случаев «молчи»
// здесь втрое больше, чем «спроси». Пропустить указание дёшево (работает прежний путь), а
// лишний вопрос вместо работы — это то самое поведение, которое из движка выкорчёвывали.

import (
	"strings"
	"testing"
)

// freshChat даёт пустой чат: ни истории, ни сумки ссылок.
func freshChat(t *testing.T) int64 {
	t.Helper()
	id := int64(-424242)
	histMu.Lock()
	delete(history, id)
	histMu.Unlock()
	clearAgentURLs(id)
	t.Cleanup(func() {
		histMu.Lock()
		delete(history, id)
		histMu.Unlock()
		clearAgentURLs(id)
	})
	return id
}

// Живой случай и его родня: указание есть, опоры нет — обязан спросить.
func TestMissingReferentAsks(t *testing.T) {
	chat := freshChat(t)
	cases := []struct{ input, wantPhrase string }{
		{"Найди мне этот проект на гитхабе", "этот проект"}, // ровно то, что провалилось
		{"Скачай это видео", "это видео"},
		{"Разбери эту статью", "эту статью"},
		{"Покажи ту таблицу", "ту таблицу"},
		{"что это за проект?", "это … проект"},
		{"Открой его", "его"},
		{"скачай их", "их"},
	}
	for _, c := range cases {
		phrase, question, ok := missingReferent(c.input, chat)
		if !ok {
			t.Errorf("%q: должен был переспросить, но промолчал", c.input)
			continue
		}
		if phrase != c.wantPhrase {
			t.Errorf("%q: процитировал %q, ждали %q", c.input, phrase, c.wantPhrase)
		}
		// Вопрос обязан цитировать слова пользователя и предлагать выход, а не отказывать.
		if !strings.Contains(question, phrase) {
			t.Errorf("%q: вопрос не цитирует фразу пользователя: %s", c.input, question)
		}
		for _, must := range []string{"ссылк", "назван"} {
			if !strings.Contains(strings.ToLower(question), must) {
				t.Errorf("%q: вопрос не предлагает дать опору (%s): %s", c.input, must, question)
			}
		}
		for _, forbidden := range []string{"не могу", "недоступ", "ограничен"} {
			if strings.Contains(strings.ToLower(question), forbidden) {
				t.Errorf("%q: вопрос звучит как отказ (%q): %s", c.input, forbidden, question)
			}
		}
	}
}

// Опора внутри самого сообщения — спрашивать нечего.
func TestMissingReferentSilentWhenSelfContained(t *testing.T) {
	chat := freshChat(t)
	quiet := []string{
		"Найди этот проект https://github.com/firecrawl/firecrawl",
		"разбери этот файл D:\\видео\\урок.mp4",
		"найди этот проект freqtrade",
		"найди этот проект «Firecrawl»",
		"открой эту страницу www.example.com",
		"Найди мне этот проект на гитхабе\n\n[Разбор приложенного видео: речь распознана, кадры описаны]\nFirecrawl…",
	}
	for _, in := range quiet {
		if _, _, ok := missingReferent(in, chat); ok {
			t.Errorf("%q: предмет назван в сообщении — переспрашивать нельзя", in)
		}
	}
}

// Указание на то, что и так вокруг, и запросы вообще без указания.
func TestMissingReferentSilentWithoutDeixis(t *testing.T) {
	chat := freshChat(t)
	quiet := []string{
		"привет",
		"Найди проекты для скрейпинга на GitHub",
		"что сегодня по BTC",
		"сделай скриншот этого экрана",
		"что умеет этот бот",
		"какие процессы на этом компьютере",
		"покажи этот чат",
		"это интересно",   // нет ни поручения, ни вопроса
		"вот это да",      // то же самое
		"разбери ETHUSDT", // предмет назван прямо
	}
	for _, in := range quiet {
		if phrase, _, ok := missingReferent(in, chat); ok {
			t.Errorf("%q: переспрашивать не о чем, а спросил про %q", in, phrase)
		}
	}
}

// Опора в разговоре: как только в чате есть история или найденные ссылки — молчим.
func TestMissingReferentSilentWithConversationAnchor(t *testing.T) {
	chat := freshChat(t)
	input := "Найди мне этот проект на гитхабе"
	if _, _, ok := missingReferent(input, chat); !ok {
		t.Fatal("в пустом чате правило обязано сработать — иначе тест ниже ничего не проверяет")
	}

	// История появилась — предмет, скорее всего, назван в ней.
	recordHistory(chat, "разбери видео про Firecrawl", "Firecrawl — это инструмент для…")
	if _, _, ok := missingReferent(input, chat); ok {
		t.Error("история диалога есть — опора могла быть там, спрашивать нельзя")
	}

	// Только сумка ссылок, без истории.
	chat2 := freshChat(t)
	rememberToolURLs(chat2, "web_search", "нашёл https://github.com/firecrawl/firecrawl", "")
	if _, _, ok := missingReferent(input, chat2); ok {
		t.Error("в сумке есть ссылки прошлой задачи — они и есть опора")
	}
}

func TestSelfContainedRequestForms(t *testing.T) {
	yes := []string{
		"открой https://ya.ru", "смотри www.ya.ru", `прочитай C:\tmp\a.txt`,
		"найди «Firecrawl»", `найди "freqtrade"`, "проверь firecrawl",
	}
	no := []string{"найди этот проект", "открой его", "покажи ту таблицу", "разбери это"}
	for _, s := range yes {
		if !selfContainedRequest(s) {
			t.Errorf("%q: предмет назван, должно считаться самодостаточным", s)
		}
	}
	for _, s := range no {
		if selfContainedRequest(s) {
			t.Errorf("%q: предмет НЕ назван, самодостаточным быть не может", s)
		}
	}
}
