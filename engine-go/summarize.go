package main

// Свёртка длинного текста картой-свёрткой. Общий механизм для всего, что не влезает в окно
// модели: расшифровка часового ролика (§21), текст двухсотстраничного скана (§23).
//
// Куски по transcriptChunkChars пересказываются по отдельности, пересказы склеиваются, и
// раунд повторяется, пока результат не влезет. «Любой объём» упирается во время, а не в окно.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// chunkText режет текст на куски не длиннее maxRunes, стараясь рвать по абзацам и границам
// предложений: разрыв посреди слова портит и пересказ, и поиск по тексту.
func chunkText(s string, maxRunes int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if len([]rune(s)) <= maxRunes {
		return []string{s}
	}
	var out []string
	var cur strings.Builder
	curLen := 0
	flush := func() {
		if curLen > 0 {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
			curLen = 0
		}
	}
	for _, para := range strings.Split(s, "\n") {
		pr := []rune(para)
		for len(pr) > 0 {
			room := maxRunes - curLen
			if room <= 0 {
				flush()
				room = maxRunes
			}
			take := len(pr)
			if take > room {
				take = room
				// Отступаем до ближайшего пробела, чтобы не рвать слово.
				for take > 0 && !isSpaceRune(pr[take-1]) {
					take--
				}
				if take == 0 {
					// В оставшееся место не влезает ни одного целого слова.
					if curLen > 0 {
						// Закрываем кусок и начинаем новый — там место точно найдётся.
						// Без этой ветки хвост куска добирался по одному символу, слова
						// рвались пополам, а при room==1 отступать было некуда и цикл
						// крутился вечно: движок вставал намертво на пересказе длинной
						// расшифровки. Оба дефекта поймал тест, а не живой ролик.
						flush()
						continue
					}
					take = room // слово длиннее целого куска — режем как есть
				}
			}
			cur.WriteString(string(pr[:take]))
			curLen += take
			pr = pr[take:]
			if curLen >= maxRunes {
				flush()
			}
		}
		cur.WriteString("\n")
		curLen++
	}
	flush()
	return out
}

func isSpaceRune(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }

// mapReduceSummary сворачивает текст любой длины под заданную роль (sys).
func mapReduceSummary(ctx context.Context, cfg Config, sys, text string) (string, error) {
	calls := 0
	one := func(part string) (string, error) {
		calls++
		cctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		defer cancel()
		out, _, err := llmChatTools(cctx, cfg.BrainPort, []map[string]any{
			{"role": "system", "content": sys},
			{"role": "user", "content": "Сожми этот фрагмент:\n\n" + part},
		}, nil, 0.2)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(stripThink(out.Content)), nil
	}

	cur := text
	for round := 0; round < 4; round++ {
		parts := chunkText(cur, transcriptChunkChars)
		if len(parts) == 1 {
			return one(parts[0])
		}
		var acc []string
		for _, p := range parts {
			if ctx.Err() != nil {
				break
			}
			if calls >= maxSummaryCalls {
				acc = append(acc, "(дальше не пересказано: исчерпан лимит вызовов модели на один разбор)")
				break
			}
			s, err := one(p)
			if err != nil {
				return "", err
			}
			if s != "" {
				acc = append(acc, s)
			}
		}
		if len(acc) == 0 {
			return "", fmt.Errorf("модель вернула пустой пересказ")
		}
		cur = strings.Join(acc, "\n")
		if len([]rune(cur)) <= transcriptChunkChars {
			return one(cur)
		}
	}
	return capAgentText(cur, transcriptInlineChars), nil
}
