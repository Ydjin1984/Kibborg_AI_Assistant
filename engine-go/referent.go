package main

// «Нет опоры — переспроси» (ТЗ §7, в духе правила про тикер со скриншота: не определился —
// спрашиваем, а не угадываем).
//
// Живой случай: «Найди мне этот проект на гитхабе» первым сообщением в пустом чате. Слово
// «этот» указывает на предмет, которого в разговоре нет. Вместо вопроса «какой?» агент выдал
// подборку популярных open-source проектов 2026 года — шесть шагов, сорок секунд поиска и
// ответ на другой вопрос. Источник он даже привёл: это не выдумка, это подмена вопроса.
//
// Правило намеренно срабатывает РЕДКО. Ложное срабатывание тут дороже пропуска: агент,
// который переспрашивает вместо работы, — ровно то поведение, которое из этого движка
// выкорчёвывали (см. armouryNote). Поэтому вопрос задаётся, только когда доказано, что опоры
// нет НИГДЕ: ни в самом сообщении, ни в окне диалога, ни в сумке найденных ссылок. Во всех
// сомнительных случаях правило молчит, и работает прежний путь.

import (
	"strings"
	"unicode"
)

// deicticDeterminers — указательные слова, после которых идёт существительное
// («этот проект», «ту статью»).
var deicticDeterminers = map[string]bool{
	"этот": true, "эта": true, "эту": true, "эти": true, "это": true,
	"этого": true, "этой": true, "этом": true, "этих": true, "этим": true, "этими": true,
	"тот": true, "та": true, "ту": true, "те": true,
	"того": true, "той": true, "тех": true, "тем": true, "теми": true,
	"данный": true, "данная": true, "данную": true, "данного": true, "данном": true,
}

// deicticPronouns — местоимения БЕЗ существительного: «найди его», «скачай это», «открой там».
var deicticPronouns = map[string]bool{
	"его": true, "её": true, "ее": true, "их": true, "это": true, "этого": true,
	"там": true, "туда": true, "оттуда": true,
}

// selfEvidentStems — то, на что «этот» указывает БЕЗ разговора, потому что оно и так вокруг:
// сам бот, эта машина, этот экран. Спрашивать «какой компьютер?» — глупость, которая
// дискредитирует правило целиком.
//
// Сравнение по ОСНОВЕ, а не по слову: «этого экрана», «на этом компьютере» — те же предметы в
// других падежах, и словарь форм тут разросся бы до бесполезности. Основа заведомо ловит
// лишнее («эту компанию» подпадёт под «комп») — и это правильная сторона ошибки: лишнее
// совпадение означает МОЛЧАНИЕ, то есть прежнее поведение, а пропуск — глупый вопрос.
var selfEvidentStems = []string{
	"бот", "кибборг", "киборг", "агент", "ассистент",
	"компьютер", "комп", "пк", "машин", "сервер", "систем",
	"экран", "монитор", "рабоч", "стол",
	"чат", "диалог", "разговор", "переписк",
	"движок", "движк", "модел",
}

func isSelfEvidentNoun(noun string) bool {
	for _, stem := range selfEvidentStems {
		if strings.HasPrefix(noun, stem) {
			return true
		}
	}
	return false
}

// taskVerbs — глаголы-поручения. Голое местоимение считается указанием на предмет только
// рядом с поручением: «найди его» — да, «это интересно» — нет.
var taskVerbs = map[string]bool{
	"найди": true, "найти": true, "поищи": true, "искать": true, "ищи": true,
	"открой": true, "открыть": true, "скачай": true, "скачать": true, "загрузи": true,
	"разбери": true, "разобрать": true, "проанализируй": true, "изучи": true,
	"покажи": true, "показать": true, "прочитай": true, "прочти": true, "читай": true,
	"сделай": true, "выполни": true, "запусти": true, "проверь": true, "сравни": true,
	"переведи": true, "перескажи": true, "объясни": true, "опиши": true, "купи": true,
	"удали": true, "сохрани": true, "отправь": true, "напиши": true,
}

// missingReferent решает, нужно ли переспросить. Возвращает фразу пользователя (для цитаты в
// вопросе) и готовый вопрос. ok=false — работаем как обычно.
func missingReferent(input string, chatID int64) (phrase, question string, ok bool) {
	text := strings.TrimSpace(input)
	if text == "" {
		return "", "", false
	}
	// Опора внутри самого сообщения — ссылка, путь, кавычки, разбор вложения.
	if selfContainedRequest(text) {
		return "", "", false
	}
	// Опора в разговоре — окно диалога или ссылки, найденные прошлой задачей.
	if conversationHasAnchor(chatID) {
		return "", "", false
	}
	phrase, ok = deicticPhrase(text)
	if !ok {
		return "", "", false
	}
	return phrase, referentQuestion(phrase), true
}

// questionWords — вопрос о предмете («что это за проект», «какой это репозиторий»).
var questionWords = map[string]bool{
	"что": true, "какой": true, "какая": true, "какое": true, "какие": true,
	"где": true, "кто": true, "зачем": true, "почему": true, "сколько": true, "чей": true,
}

// skipWords — служебные слова между указательным и существительным: «что это ЗА проект».
var skipWords = map[string]bool{
	"за": true, "в": true, "во": true, "на": true, "из": true, "про": true, "для": true,
	"с": true, "со": true, "по": true, "у": true, "от": true, "к": true, "о": true, "об": true,
	"же": true, "вот": true, "самый": true, "самая": true, "самое": true,
}

// deicticPhrase находит указание на предмет вне сообщения и возвращает его КАК НАПИСАЛ
// пользователь — цитата его словами всегда грамматична, в отличие от нашей попытки
// просклонять существительное.
//
// Обязательное условие — намерение: поручение («найди») или вопрос («что это за…»). Без него
// «это интересно» и «вот это да» превращались бы в допрос на ровном месте.
func deicticPhrase(text string) (string, bool) {
	tokens := wordTokens(text)
	if len(tokens) == 0 {
		return "", false
	}
	lower := make([]string, len(tokens))
	for i, t := range tokens {
		lower[i] = strings.ToLower(strings.ReplaceAll(t, "ё", "е"))
	}

	hasVerb, hasIntent := false, false
	for _, w := range lower {
		if taskVerbs[w] {
			hasVerb, hasIntent = true, true
		}
		if questionWords[w] {
			hasIntent = true
		}
	}
	if !hasIntent {
		return "", false
	}

	// 1. Указательное + существительное, через служебные слова: «что это за проект».
	for i, w := range lower {
		if !deicticDeterminers[w] {
			continue
		}
		for j := i + 1; j < len(tokens) && j <= i+3; j++ {
			noun := lower[j]
			if skipWords[noun] {
				continue
			}
			if isSelfEvidentNoun(noun) {
				return "", false // «этот компьютер», «этот бот» — опора и так вокруг нас
			}
			if deicticDeterminers[noun] || taskVerbs[noun] || questionWords[noun] {
				break // «это сделай» — не пара «указательное + предмет»
			}
			if j == i+1 {
				return tokens[i] + " " + tokens[j], true
			}
			return tokens[i] + " … " + tokens[j], true
		}
	}

	// 2. Голое местоимение рядом с поручением: «найди его», «скачай это».
	if !hasVerb || len(tokens) > 8 {
		return "", false
	}
	for i, w := range lower {
		if deicticPronouns[w] {
			return tokens[i], true
		}
	}
	return "", false
}

// selfContainedRequest: предмет назван в самом сообщении, спрашивать нечего.
func selfContainedRequest(text string) bool {
	l := strings.ToLower(text)
	if strings.Contains(l, "http://") || strings.Contains(l, "https://") || strings.Contains(l, "www.") {
		return true
	}
	// Вложение уже превращено в текст (§4, §21): разбор видео или описание картинки.
	if strings.Contains(text, "[Разбор приложенного видео") ||
		strings.Contains(text, "[Описание приложенной картинки") ||
		strings.Contains(text, "[Расшифровка") {
		return true
	}
	// Путь к файлу: «разбери D:\видео\урок.mp4».
	if strings.Contains(text, `:\`) || strings.Contains(text, ":/") || strings.Contains(text, "\\\\") {
		return true
	}
	// Пользователь сам взял предмет в кавычки.
	for _, q := range []string{"«", "\"", "'", "`"} {
		if i := strings.Index(text, q); i >= 0 && strings.Index(text[i+len(q):], closingQuote(q)) > 0 {
			return true
		}
	}
	// Латиница длиннее двух букв — почти всегда имя предмета (Firecrawl, freqtrade, BTC).
	for _, tok := range wordTokens(text) {
		if len([]rune(tok)) < 3 {
			continue
		}
		latin := true
		for _, r := range tok {
			if r > unicode.MaxASCII || !unicode.IsLetter(r) {
				latin = false
				break
			}
		}
		if latin {
			return true
		}
	}
	return false
}

func closingQuote(open string) string {
	if open == "«" {
		return "»"
	}
	return open
}

// conversationHasAnchor: в этом чате уже есть то, на что можно указать.
//
// Проверяется РАБОЧЕЕ окно диалога и сумка ссылок, но НЕ долговременная память: «этот проект»
// указывает на предмет текущего разговора, а не на то, что обсуждали неделю назад.
func conversationHasAnchor(chatID int64) bool {
	histMu.Lock()
	n := len(history[chatID])
	histMu.Unlock()
	if n > 0 {
		return true
	}
	return len(getAgentURLs(chatID)) > 0
}

// referentQuestion — вопрос, который НЕ похож на отказ: он называет три способа дать опору.
func referentQuestion(phrase string) string {
	return "🤔 Уточни: «" + phrase + "» — это про что?\n\n" +
		"В этом чате об этом ещё не было речи, а угадывать не буду — подсуну не то.\n" +
		"Дай опору, дальше сделаю сам:\n" +
		"• ссылку;\n" +
		"• точное название;\n" +
		"• видео, скриншот или файл, где это упоминается — разберу и найду по нему."
}

// wordTokens режет текст на слова, сохраняя исходный регистр.
func wordTokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
