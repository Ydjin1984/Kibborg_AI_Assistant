package secops

import (
	"strings"
	"testing"
)

func hasAnomaly(r LogReport, substr string) bool {
	for _, a := range r.Anomalies {
		if strings.Contains(a.Message, substr) {
			return true
		}
	}
	return false
}

func TestAnalyzeLog_LevelsAndPanic(t *testing.T) {
	log := `2026/07/19 10:00:00 [LLM] info: brain ready
2026/07/19 10:00:01 [TELEGRAM] error: send failed
2026/07/19 10:00:02 [HISTORY] warn: file large
2026/07/19 10:00:03 panic: assignment to entry in nil map`
	r := AnalyzeLog(log, "test.log")
	if r.TotalLines != 4 {
		t.Errorf("строк=%d, ожидалось 4", r.TotalLines)
	}
	if r.Levels["error"] < 1 {
		t.Errorf("ожидалась хотя бы 1 ошибка, levels=%v", r.Levels)
	}
	if r.Levels["fatal"] < 1 {
		t.Errorf("panic должен считаться fatal, levels=%v", r.Levels)
	}
	if !hasAnomaly(r, "panic/fatal") {
		t.Errorf("ожидалась аномалия про panic/fatal, got %+v", r.Anomalies)
	}
}

func TestAnalyzeLog_ErrorBurst(t *testing.T) {
	var lines []string
	for i := 0; i < 8; i++ {
		lines = append(lines, "2026/07/19 10:00:0"+string(rune('0'+i))+" error: connection refused")
	}
	r := AnalyzeLog(strings.Join(lines, "\n"), "burst.log")
	if !hasAnomaly(r, "всплеск") && !hasAnomaly(r, "Всплеск") {
		t.Errorf("ожидался всплеск ошибок, got %+v", r.Anomalies)
	}
	// Repeated identical error should surface as a top message.
	if len(r.TopMessages) == 0 || r.TopMessages[0].Count < 8 {
		t.Errorf("ожидалось частое повторяющееся сообщение, got %+v", r.TopMessages)
	}
}

func TestAnalyzeLog_SecretLeakFlagged(t *testing.T) {
	log := `2026/07/19 [TELEGRAM] error: Get "https://api.telegram.org/bot123456789:AAHrandomtokenvaluewithatleastthirtyfivechars/getUpdates": timeout`
	r := AnalyzeLog(log, "leak.log")
	if !hasAnomaly(r, "СЕКРЕТ") {
		t.Errorf("ожидалась аномалия про утечку секрета, got %+v", r.Anomalies)
	}
}

func TestAnalyzeLog_Empty(t *testing.T) {
	r := AnalyzeLog("", "empty.log")
	if r.TotalLines != 0 {
		t.Errorf("пустой лог должен иметь 0 строк, got %d", r.TotalLines)
	}
}
