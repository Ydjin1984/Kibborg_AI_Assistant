package main

import (
	"strings"
	"testing"
)

func TestParseStressModeToken(t *testing.T) {
	cases := map[string]StressMode{
		"light":        stressModeLight,
		"лайт":         stressModeLight,
		"лёгкий":       stressModeLight,
		"required":     stressModeRequired,
		"обязательный": stressModeRequired,
		"full":         stressModeFull,
		"полный":       stressModeFull,
		"ALL":          stressModeFull,
	}
	for in, want := range cases {
		got, ok := parseStressModeToken(in)
		if !ok || got != want {
			t.Errorf("parseStressModeToken(%q) = %q,%v; want %q,true", in, got, ok, want)
		}
	}
	if _, ok := parseStressModeToken("headers"); ok {
		t.Fatal("headers не должен считаться режимом")
	}
	if normalizeStressMode("") != stressModeRequired {
		t.Fatal("пустой mode → required")
	}
	if normalizeStressMode("nope") != stressModeRequired {
		t.Fatal("неизвестный mode → required")
	}
}

func TestSplitStressArgModes(t *testing.T) {
	type want struct {
		target, focus string
		mode          StressMode
	}
	cases := []struct {
		in   string
		want want
	}{
		{"", want{"", "", stressModeRequired}},
		{"https://example.com", want{"https://example.com", "", stressModeRequired}},
		{"light https://example.com", want{"https://example.com", "", stressModeLight}},
		{"https://example.com full", want{"https://example.com", "", stressModeFull}},
		{"полный example.com headers cookies", want{"https://example.com", "headers cookies", stressModeFull}},
		{"https://a.test обязательный admin", want{"https://a.test", "admin", stressModeRequired}},
		{"лайт example.org", want{"https://example.org", "", stressModeLight}},
	}
	for _, c := range cases {
		target, focus, mode := splitStressArg(c.in)
		if target != c.want.target || focus != c.want.focus || mode != c.want.mode {
			t.Errorf("splitStressArg(%q) = (%q,%q,%q); want (%q,%q,%q)",
				c.in, target, focus, mode, c.want.target, c.want.focus, c.want.mode)
		}
	}
}

func TestStressAuditTaskByMode(t *testing.T) {
	light := stressAuditTask("https://example.com", "", "", stressModeLight)
	if !strings.Contains(light, "Режим глубины: light") {
		t.Fatal("light brief должен помечать режим")
	}
	if strings.Contains(light, "search_hacker_tools") && !strings.Contains(light, "Не вызывай search_hacker_tools") {
		t.Fatal("light не должен требовать каталог")
	}
	if !strings.Contains(light, "Не вызывай search_hacker_tools") {
		t.Fatal("light brief должен запрещать каталог/браузер/CLI")
	}

	req := stressAuditTask("https://example.com", "headers", "", stressModeRequired)
	if !strings.Contains(req, "Доп. фокус: headers") {
		t.Fatal("required должен пробрасывать фокус")
	}
	if !strings.Contains(req, "Чеклист") && !strings.Contains(req, "чеклист") && !strings.Contains(req, "минимум") {
		t.Fatal("required должен ссылаться на минимум плейбука")
	}

	full := stressAuditTask("https://example.com", "", "", stressModeFull)
	withFile := stressAuditTask("https://example.com", "headers", "Приложение пользователя:\n- путь: `runtime/x.md`", stressModeRequired)
	if !strings.Contains(withFile, "ПРИЛОЖЕНИЕ ПОЛЬЗОВАТЕЛЯ") || !strings.Contains(withFile, "runtime/x.md") {
		t.Fatal("вложение должно стоять отдельным блоком в начале брифа")
	}
	idxAtt := strings.Index(withFile, "ПРИЛОЖЕНИЕ")
	idxMeth := strings.Index(withFile, "Авторизованный тест")
	if idxAtt < 0 || idxMeth < 0 || idxAtt > idxMeth {
		t.Fatal("приложение должно идти ДО методологии, иначе модель его не видит")
	}
	for _, needle := range []string{"nuclei", "nmap", "ffuf", "httpx", "browser.read", "ОБЯЗАТЕЛЬНО"} {
		if !strings.Contains(full, needle) {
			t.Fatalf("full brief должен содержать %q", needle)
		}
	}
}

func TestStressHintPacks(t *testing.T) {
	if got := stressHintPacks(stressModeLight); len(got) != 1 || got[0] != packSecops {
		t.Fatalf("light packs = %v", got)
	}
	req := stressHintPacks(stressModeRequired)
	if len(req) != 3 || req[0] != packSecops || req[1] != packWeb || req[2] != packFiles {
		t.Fatalf("required packs = %v", req)
	}
	full := stressHintPacks(stressModeFull)
	if len(full) != 3 || full[0] != packSecops || full[1] != packWeb || full[2] != packConsole {
		t.Fatalf("full packs = %v", full)
	}
}
