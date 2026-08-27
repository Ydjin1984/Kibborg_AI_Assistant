package secops

import (
	"strings"
	"testing"
)

func TestProbeLocalToolsShape(t *testing.T) {
	tools := ProbeLocalTools()
	if len(tools) < 4 {
		t.Fatalf("ожидали ≥4 тулов, got %d", len(tools))
	}
	names := map[string]bool{}
	for _, tl := range tools {
		if tl.Name == "" || tl.Binary == "" {
			t.Fatalf("пустые поля: %+v", tl)
		}
		names[tl.Name] = true
	}
	for _, want := range []string{"nuclei", "nmap", "ffuf", "httpx"} {
		if !names[want] {
			t.Errorf("нет записи %s", want)
		}
	}
	sum := LocalToolsSummary(tools)
	if !strings.Contains(sum, "Локальные CLI") {
		t.Errorf("summary: %q", sum)
	}
}

func TestVerifyProjectDiscoveryHttpxRejectsPython(t *testing.T) {
	py := "HTTPX 🦋\nA next generation HTTP client.\nUsage: httpx <URL>"
	if verifyProjectDiscoveryHttpx("", py) {
		t.Fatal("python httpx must be rejected")
	}
	pd := "[INF] Current Version: v1.6.0\nprojectdiscovery"
	if !verifyProjectDiscoveryHttpx("", pd) {
		t.Fatal("projectdiscovery httpx must be accepted")
	}
}
