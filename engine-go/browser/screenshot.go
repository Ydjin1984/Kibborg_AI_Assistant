package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// This file is the screenshot_service from the spec. Per the rules, screenshots are a
// LAST resort — used only when the user explicitly asks for a visual analysis (chart,
// image recognition, "how does the page look"). Everything else goes through the DOM and
// network data. The agent prompt enforces this; this code just performs the capture.

// artifactDir is where screenshots, clones and downloads are written.
const artifactDir = "runtime/browser"

// CaptureScreenshot saves a PNG of the page (or a single element) and returns its path.
// fullPage captures the entire scrollable page; otherwise just the viewport. A non-empty
// selector captures only that element.
func (s *Session) CaptureScreenshot(selector string, fullPage bool) (string, error) {
	var buf []byte
	var action chromedp.Action
	switch {
	case strings.TrimSpace(selector) != "":
		action = chromedp.Screenshot(selector, &buf, chromedp.ByQuery, chromedp.NodeVisible)
	case fullPage:
		action = chromedp.FullScreenshot(&buf, 90)
	default:
		action = chromedp.CaptureScreenshot(&buf)
	}
	if err := s.run(action); err != nil {
		return "", err
	}
	if len(buf) == 0 {
		return "", fmt.Errorf("скриншот пустой")
	}
	path, err := s.writeArtifact("shot", "png", buf)
	if err != nil {
		return "", err
	}
	return path, nil
}

// writeArtifact saves bytes under artifactDir with a timestamped name, records the path as
// a session artifact (so the agent can deliver it), and returns the absolute-ish path.
func (s *Session) writeArtifact(prefix, ext string, data []byte) (string, error) {
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s.%s", prefix, time.Now().Format("20060102-150405.000"), ext)
	path := filepath.Join(artifactDir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	s.addArtifact(path)
	return path, nil
}
