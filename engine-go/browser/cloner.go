package browser

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// This file is the website_cloner from the spec: download the rendered page plus its CSS,
// JS, images, fonts, SVG and video, preserve a directory layout, rewrite absolute references
// to local ones, and leave a working offline copy at outDir/index.html.

const (
	maxCloneAssets   = 400
	maxAssetBytes    = 15 << 20 // 15 MB per asset
	maxCSSRecurseLen = 2 << 20  // only scan url()/@import in CSS up to 2 MB
)

// CloneResult summarizes a clone run for the agent.
type CloneResult struct {
	Dir    string         `json:"dir"`
	Page   string         `json:"page"`
	Counts map[string]int `json:"counts"`
	Total  int            `json:"total_assets"`
	Errors int            `json:"errors"`
}

// cloneHTTP serves local search engines (httptest / loopback) AND asset fetches that already
// passed safeRemoteURL. Redirects to internal hosts are blocked; dial pin is not applied
// here so 127.0.0.1 search backends keep working.
var cloneHTTP = redirectSafeClient(30 * time.Second)

// publicHTTP is for untrusted remote downloads (SSRF dial pin + redirect guard).
var publicHTTP = safeHTTPClient(30 * time.Second)

type cloner struct {
	base      *url.URL
	outDir    string
	seen      map[string]string // absolute URL -> local relative path
	usedNames map[string]bool
	counts    map[string]int
	errs      int
}

// CloneWebsite clones the current page into outDir (auto-named under runtime/browser when
// empty) and returns a summary.
func (s *Session) CloneWebsite(outDir string) (CloneResult, error) {
	html, pageURL, err := s.htmlAndURL("html")
	if err != nil {
		return CloneResult{}, err
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return CloneResult{}, fmt.Errorf("не разобрал URL страницы: %w", err)
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return CloneResult{}, err
	}
	if strings.TrimSpace(outDir) == "" {
		host := strings.ReplaceAll(base.Host, ":", "_")
		outDir = filepath.Join(artifactDir, fmt.Sprintf("clone-%s-%s", host, time.Now().Format("20060102-150405")))
	} else {
		// A model-supplied dir must not escape the artifact directory: use only its base name
		// under artifactDir, so clone_website can never overwrite arbitrary local files.
		outDir = filepath.Join(artifactDir, sanitizeName(filepath.Base(filepath.Clean(outDir))))
	}
	for _, sub := range []string{"css", "js", "img", "fonts", "media", "assets"} {
		if err := os.MkdirAll(filepath.Join(outDir, sub), 0o755); err != nil {
			return CloneResult{}, err
		}
	}
	c := &cloner{base: base, outDir: outDir, seen: map[string]string{}, usedNames: map[string]bool{}, counts: map[string]int{}}

	// Rewrite tag references to local copies. Each (selector, attr) pair is a download source.
	// hint forces the CSS pipeline for stylesheets referenced by extensionless URLs
	// (fonts.googleapis.com/css2?family=… is the most common real-world case).
	type ref struct{ sel, attr, hint string }
	for _, r := range []ref{
		{`link[rel~="stylesheet"][href]`, "href", "css"},
		{`link[rel="icon"][href]`, "href", ""},
		{`link[rel="shortcut icon"][href]`, "href", ""},
		{`script[src]`, "src", ""},
		{`img[src]`, "src", ""},
		{`img[data-src]`, "data-src", ""},
		{`source[src]`, "src", ""},
		{`video[src]`, "src", ""},
		{`audio[src]`, "src", ""},
		{`use[href]`, "href", ""},
	} {
		doc.Find(r.sel).Each(func(_ int, e *goquery.Selection) {
			raw, ok := e.Attr(r.attr)
			if !ok {
				return
			}
			local := c.fetchAs(raw, r.hint)
			if local != "" {
				e.SetAttr(r.attr, local)
			}
		})
	}

	out, err := doc.Html()
	if err != nil {
		return CloneResult{}, err
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "<!") {
		out = "<!DOCTYPE html>\n" + out
	}
	if err := os.WriteFile(filepath.Join(outDir, "index.html"), []byte(out), 0o644); err != nil {
		return CloneResult{}, err
	}
	s.addArtifact(filepath.Join(outDir, "index.html"))
	return CloneResult{Dir: outDir, Page: pageURL, Counts: c.counts, Total: len(c.seen), Errors: c.errs}, nil
}

// fetchAs downloads one asset (resolving rawRef against the page base), stores it under the
// right subfolder, and returns the local relative path to use in the rewritten HTML. It
// dedups by absolute URL and skips data:/about:/javascript: refs. Optional category hint
// ("css") is for assets whose URL path has no extension but HTML context fixes the type.
func (c *cloner) fetchAs(rawRef, hint string) string {
	rawRef = strings.TrimSpace(rawRef)
	if rawRef == "" || strings.HasPrefix(rawRef, "data:") || strings.HasPrefix(rawRef, "about:") || strings.HasPrefix(rawRef, "javascript:") || strings.HasPrefix(rawRef, "#") {
		return ""
	}
	abs := resolveURL(c.base.String(), rawRef)
	u, err := url.Parse(abs)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	if local, ok := c.seen[abs]; ok {
		return local
	}
	if len(c.seen) >= maxCloneAssets {
		return "" // safety cap; leave the original (absolute) reference in place
	}
	sub, category := assetSubdir(u.Path)
	if hint == "css" && category != "css" {
		sub, category = "css", "css"
	}
	name := c.uniqueName(sub, u)
	rel := sub + "/" + name
	full := filepath.Join(c.outDir, sub, name)
	c.seen[abs] = rel // reserve before download so recursion/dedup is stable

	data, err := download(abs)
	if err != nil {
		delete(c.seen, abs) // don't leave a dead local path for later references
		c.errs++
		return ""
	}
	// CSS may reference fonts/images via url(); pull those too and localize the CSS.
	if category == "css" && len(data) <= maxCSSRecurseLen {
		data = []byte(c.localizeCSS(string(data), u))
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		delete(c.seen, abs)
		c.errs++
		return ""
	}
	c.counts[category]++
	return rel
}

// localizeCSS downloads url()/@import targets inside a stylesheet and rewrites them to local
// relative paths (relative to the css/ folder, e.g. ../fonts/x.woff2).
var cssURLRe = regexp.MustCompile(`url\(\s*['"]?([^'")]+)['"]?\s*\)`)
var cssImportRe = regexp.MustCompile(`@import\s+['"]([^'"]+)['"]`)

func (c *cloner) localizeCSS(css string, cssURL *url.URL) string {
	rewrite := func(ref, hint string) string {
		ref = strings.TrimSpace(ref)
		// fragment-only refs (url(#blur) — SVG filters/gradients) must stay same-document:
		// resolving them against the stylesheet URL would re-download the CSS as a duplicate
		if ref == "" || strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "#") {
			return ""
		}
		absRef := resolveURL(cssURL.String(), ref)
		local := c.fetchAs(absRef, hint)
		if local == "" {
			return ""
		}
		// local is "sub/name" relative to outDir; CSS lives in css/, so step up one level.
		return "../" + local
	}
	css = cssURLRe.ReplaceAllStringFunc(css, func(m string) string {
		sub := cssURLRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		if loc := rewrite(sub[1], ""); loc != "" {
			return "url(" + loc + ")"
		}
		return m
	})
	css = cssImportRe.ReplaceAllStringFunc(css, func(m string) string {
		sub := cssImportRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		if loc := rewrite(sub[1], "css"); loc != "" {
			return `@import "` + loc + `"`
		}
		return m
	})
	return css
}

// uniqueName derives a collision-free filename for an asset inside a subfolder.
func (c *cloner) uniqueName(sub string, u *url.URL) string {
	name := path.Base(u.Path)
	if name == "" || name == "/" || name == "." {
		name = "index"
	}
	name = sanitizeName(name)
	if filepath.Ext(name) == "" {
		name += defaultExt(sub)
	}
	key := sub + "/" + name
	if !c.usedNames[key] {
		c.usedNames[key] = true
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if !c.usedNames[sub+"/"+cand] {
			c.usedNames[sub+"/"+cand] = true
			return cand
		}
	}
}

// assetSubdir maps a URL path to a subfolder + category by file extension.
func assetSubdir(p string) (sub, category string) {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".css":
		return "css", "css"
	case ".js", ".mjs":
		return "js", "js"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".bmp", ".avif":
		return "img", "img"
	case ".svg":
		return "assets", "svg"
	case ".woff", ".woff2", ".ttf", ".otf", ".eot":
		return "fonts", "fonts"
	case ".mp4", ".webm", ".ogg", ".ogv", ".mov", ".m4v", ".mp3", ".wav":
		return "media", "media"
	case ".json":
		return "assets", "json"
	default:
		return "assets", "other"
	}
}

func defaultExt(sub string) string {
	switch sub {
	case "css":
		return ".css"
	case "js":
		return ".js"
	case "img":
		return ".img"
	default:
		return ""
	}
}

var nameSanitizeRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitizeName(s string) string {
	s = nameSanitizeRe.ReplaceAllString(s, "_")
	if len(s) > 80 {
		ext := filepath.Ext(s)
		if len(ext) > 20 {
			// ".{60 chars}" is not a real extension — keeping it would make the slice
			// below go negative and panic on model/web-supplied names
			ext = ""
		}
		s = s[:80-len(ext)] + ext
	}
	return s
}

// download fetches an asset with a size cap. It refuses internal/private targets (SSRF): a
// cloned page's assets come from an untrusted DOM and could point at 127.0.0.1/metadata IPs.
func download(absURL string) ([]byte, error) {
	safe, err := safeRemoteURL(absURL)
	if err != nil {
		return nil, err
	}
	resp, err := publicHTTP.Get(safe)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxAssetBytes))
}
