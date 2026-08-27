package secops

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// LocalTool is one CLI from the stress / cybersecurity toolchain.
type LocalTool struct {
	Name    string `json:"name"`
	Binary  string `json:"binary"`
	Class   string `json:"class,omitempty"`
	Tier    string `json:"tier,omitempty"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	OK      bool   `json:"ok"`
	Note    string `json:"note,omitempty"`
	Install string `json:"install,omitempty"`
	Repo    string `json:"repo,omitempty"`
}

type toolsFile struct {
	Tools []struct {
		Name    string `json:"name"`
		Binary  string `json:"binary"`
		Class   string `json:"class"`
		Tier    string `json:"tier"`
		Install string `json:"install"`
		Repo    string `json:"repo"`
		Note    string `json:"note"`
	} `json:"tools"`
	Dictionaries map[string]string `json:"dictionaries"`
}

type toolSpec struct {
	Name    string
	Binary  string
	Class   string
	Tier    string
	Install string
	Repo    string
	Note    string
	Verify  func(bin string, out string) bool
}

var (
	toolSpecsOnce sync.Once
	toolSpecs     []toolSpec
	dictPaths     map[string]string
)

// builtinCore is used when tools.json is missing.
var builtinCore = []toolSpec{
	{Name: "nuclei", Binary: "nuclei", Class: "dast-known-vuln", Tier: "core",
		Install: "go install -v github.com/projectdiscovery/nuclei/v3/cmd/nuclei@latest", Verify: verifyAnyVersion},
	{Name: "nmap", Binary: "nmap", Class: "recon-ports", Tier: "core",
		Install: "winget install Insecure.Nmap", Verify: verifyAnyVersion},
	{Name: "ffuf", Binary: "ffuf", Class: "discovery-paths", Tier: "core",
		Install: "go install -v github.com/ffuf/ffuf/v2@latest", Verify: verifyAnyVersion},
	{Name: "httpx", Binary: "httpx", Class: "recon-http", Tier: "core",
		Install: "go install -v github.com/projectdiscovery/httpx/cmd/httpx@latest", Verify: verifyProjectDiscoveryHttpx},
	{Name: "subfinder", Binary: "subfinder", Class: "recon-subdomains", Tier: "core",
		Install: "go install -v github.com/projectdiscovery/subfinder/v2/cmd/subfinder@latest", Verify: verifyAnyVersion},
	{Name: "katana", Binary: "katana", Class: "recon-crawl", Tier: "core",
		Install: "go install -v github.com/projectdiscovery/katana/cmd/katana@latest", Verify: verifyAnyVersion},
	{Name: "naabu", Binary: "naabu", Class: "recon-ports", Tier: "core",
		Install: "go install -v github.com/projectdiscovery/naabu/v2/cmd/naabu@latest", Verify: verifyAnyVersion},
	{Name: "dnsx", Binary: "dnsx", Class: "recon-dns", Tier: "core",
		Install: "go install -v github.com/projectdiscovery/dnsx/cmd/dnsx@latest", Verify: verifyAnyVersion},
}

func loadToolSpecs() {
	toolSpecsOnce.Do(func() {
		toolSpecs = append([]toolSpec(nil), builtinCore...)
		dictPaths = map[string]string{}
		path, err := findHackerToolsFile("tools.json")
		if err != nil {
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		var raw toolsFile
		if err := json.Unmarshal(data, &raw); err != nil {
			return
		}
		dictPaths = raw.Dictionaries
		seen := map[string]bool{}
		var merged []toolSpec
		for _, t := range raw.Tools {
			if strings.TrimSpace(t.Binary) == "" {
				continue // builtins like probe_url
			}
			seen[t.Binary] = true
			v := verifyAnyVersion
			if t.Binary == "httpx" {
				v = verifyProjectDiscoveryHttpx
			}
			merged = append(merged, toolSpec{
				Name: t.Name, Binary: t.Binary, Class: t.Class, Tier: t.Tier,
				Install: t.Install, Repo: t.Repo, Note: t.Note, Verify: v,
			})
		}
		for _, b := range builtinCore {
			if !seen[b.Binary] {
				merged = append(merged, b)
			}
		}
		if len(merged) > 0 {
			toolSpecs = merged
		}
	})
}

func verifyAnyVersion(_ string, _ string) bool { return true }

func verifyProjectDiscoveryHttpx(bin string, out string) bool {
	low := strings.ToLower(out)
	if strings.Contains(low, "butterfly") || strings.Contains(low, "next generation http client") {
		return false
	}
	if strings.Contains(low, "projectdiscovery") || strings.Contains(low, "current version") ||
		strings.Contains(low, "-u ") || strings.Contains(low, "-l ") || strings.Contains(low, "probe") ||
		strings.Contains(out, "__    __") { // PD ascii banner
		return true
	}
	// Prefer go/bin over Python Scripts even if -version is noisy.
	lowPath := strings.ToLower(bin)
	if strings.Contains(lowPath, `\go\bin\`) || strings.Contains(lowPath, "/go/bin/") {
		return !strings.Contains(lowPath, `python`)
	}
	return false
}

// ProbeLocalTools reports which toolchain CLIs are usable on this machine.
func ProbeLocalTools() []LocalTool {
	loadToolSpecs()
	out := make([]LocalTool, 0, len(toolSpecs))
	for _, spec := range toolSpecs {
		t := LocalTool{
			Name: spec.Name, Binary: spec.Binary, Class: spec.Class, Tier: spec.Tier,
			Install: spec.Install, Repo: spec.Repo, Note: spec.Note,
		}
		path, err := lookPathPreferGoBin(spec.Binary)
		if err != nil || path == "" {
			if t.Note == "" {
				t.Note = "не найден в PATH"
			}
			out = append(out, t)
			continue
		}
		ver := toolVersion(path, spec.Binary)
		if spec.Verify != nil && !spec.Verify(path, ver) {
			t.Path = path
			t.Version = firstLine(ver)
			t.Note = "в PATH другой одноимённый бинарь (не тот инструмент)"
			out = append(out, t)
			continue
		}
		t.OK = true
		t.Path = path
		t.Version = firstLine(ver)
		t.Note = ""
		out = append(out, t)
	}
	return out
}

// DictionaryPaths returns local paths from tools.json (SecLists, nuclei-templates, …).
func DictionaryPaths() map[string]string {
	loadToolSpecs()
	out := map[string]string{}
	for k, rel := range dictPaths {
		name := filepath.FromSlash(strings.TrimPrefix(rel, "hacker-tools/"))
		if abs, err := findHackerToolsDir(name); err == nil {
			out[k] = abs
			continue
		}
		if abs, err := filepath.Abs(rel); err == nil {
			if st, e := os.Stat(abs); e == nil && st.IsDir() {
				out[k] = abs
			}
		}
	}
	return out
}

func findHackerToolsDir(name string) (string, error) {
	for _, p := range hackerToolsCandidates(name) {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			abs, _ := filepath.Abs(p)
			if abs != "" {
				return abs, nil
			}
			return p, nil
		}
	}
	return "", os.ErrNotExist
}

// LocalToolsSummary is a one-liner for agent briefs / Telegram help.
func LocalToolsSummary(tools []LocalTool) string {
	var ok, miss []string
	for _, t := range tools {
		if t.OK {
			ok = append(ok, t.Name)
		} else if t.Tier == "core" || t.Tier == "" {
			miss = append(miss, t.Name)
		}
	}
	var b strings.Builder
	b.WriteString("Локальные CLI: ")
	if len(ok) > 0 {
		b.WriteString("есть " + strings.Join(ok, ", "))
	} else {
		b.WriteString("нет ни одного core")
	}
	if len(miss) > 0 {
		b.WriteString("; нет core: " + strings.Join(miss, ", "))
	}
	if dicts := DictionaryPaths(); len(dicts) > 0 {
		var names []string
		for k := range dicts {
			names = append(names, k)
		}
		b.WriteString("; словари: " + strings.Join(names, ", "))
	}
	return b.String()
}

func lookPathPreferGoBin(name string) (string, error) {
	var candidates []string
	add := func(dir string) {
		if dir == "" {
			return
		}
		candidates = append(candidates, filepath.Join(dir, exeName(name)))
		if runtime.GOOS == "windows" {
			// pip/git wrappers: sqlmap.cmd, jwt_tool.cmd, arjun.exe from Scripts
			candidates = append(candidates,
				filepath.Join(dir, name+".cmd"),
				filepath.Join(dir, name+".bat"),
			)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, "go", "bin"))
	}
	add(os.Getenv("GOBIN"))
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		add(filepath.Join(gopath, "bin"))
	}
	if name == "nmap" {
		candidates = append(candidates,
			filepath.Join(`C:\Program Files (x86)\Nmap`, exeName(name)),
			filepath.Join(`C:\Program Files\Nmap`, exeName(name)),
		)
	}
	if name == "zap" || name == "hashcat" {
		candidates = append(candidates,
			filepath.Join(`C:\Program Files\ZAP\Zed Attack Proxy`, "ZAP.exe"),
			filepath.Join(`C:\Program Files\ZAP\Zed Attack Proxy`, "zap.bat"),
			filepath.Join(`C:\Program Files\hashcat`, "hashcat.exe"),
			filepath.Join(`C:\hashcat`, "hashcat.exe"),
		)
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return exec.LookPath(name)
}

func exeName(name string) string {
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		return name + ".exe"
	}
	return name
}

func toolVersion(path, binary string) string {
	argsList := [][]string{{"-version"}, {"--version"}, {"version"}, {"-V"}}
	switch binary {
	case "httpx":
		argsList = [][]string{{"-version"}, {"--version"}, {"-h"}}
	case "ffuf":
		argsList = [][]string{{"-V"}, {"version"}, {"--help"}}
	case "trufflehog", "gitleaks", "trivy", "osv-scanner":
		argsList = [][]string{{"--version"}, {"version"}, {"-version"}}
	}
	for _, args := range argsList {
		cmd := exec.Command(path, args...)
		out, _ := runCmdQuick(cmd, 4*time.Second)
		if strings.TrimSpace(out) != "" {
			return out
		}
	}
	return ""
}

func runCmdQuick(cmd *exec.Cmd, d time.Duration) (string, error) {
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
		return string(out), err
	case <-time.After(d):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return "", os.ErrDeadlineExceeded
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for {
		i := strings.IndexByte(s, '\x1b')
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i:], 'm')
		if j < 0 {
			break
		}
		s = s[:i] + s[i+j+1:]
	}
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		rest := strings.TrimSpace(s[i+1:])
		s = strings.TrimSpace(s[:i])
		if strings.HasPrefix(s, "flag provided but not defined") && rest != "" {
			return firstLine(rest)
		}
	}
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
