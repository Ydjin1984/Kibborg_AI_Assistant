package secops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSecurityReport(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	res, err := WriteSecurityReport(SecurityReportInput{
		Title:  "Тест",
		Target: "https://example.com/app",
		Body:   "### [HIGH] Нет HSTS\n- **Где:** https://example.com\n- **Как лечить:** включить HSTS\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, need := range []string{"# Тест", "example.com", "Нет HSTS", "Как лечить"} {
		if !strings.Contains(text, need) {
			t.Errorf("в отчёте нет %q", need)
		}
	}
	if !strings.Contains(res.URL, "security/") || !strings.HasSuffix(res.URL, ".md") {
		t.Errorf("странный URL %q", res.URL)
	}
	list, err := ListSecurityReports(10)
	if err != nil || len(list) == 0 {
		t.Fatalf("ListSecurityReports: %v %v", list, err)
	}
	if filepath.Base(list[0]) != filepath.Base(res.Path) {
		t.Errorf("список не содержит свежий отчёт: %v", list)
	}
}

func TestWriteSecurityReport_RequiresFields(t *testing.T) {
	if _, err := WriteSecurityReport(SecurityReportInput{Title: "x"}); err == nil {
		t.Fatal("нужен target или URL в body")
	}
	if _, err := WriteSecurityReport(SecurityReportInput{Target: "https://x.test"}); err == nil {
		t.Fatal("нужен body")
	}
}

func TestWriteSecurityReport_InfersTargetFromBody(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	// Live bug: model sent body with Цель URL but omitted target= → "нужен target" ×3.
	res, err := WriteSecurityReport(SecurityReportInput{
		Title: "Отчёт",
		Body:  "**Цель:** `https://profi.sysx.uz/`\n\n### [HIGH] IDOR\n- где: /api/v1/staff\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "profi.sysx.uz") {
		t.Fatalf("target не подтянулся из body: %s", data)
	}
}
