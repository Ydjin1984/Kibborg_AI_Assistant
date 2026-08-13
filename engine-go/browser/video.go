package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// videoDownloadTimeout kills hung yt-dlp (network stall, extractor hang).
const videoDownloadTimeout = 45 * time.Minute

// VideoDownload is the result of download_video: local path + metadata for delivery.
type VideoDownload struct {
	Path     string `json:"path"`
	Title    string `json:"title,omitempty"`
	Ext      string `json:"ext,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
	URL      string `json:"url"`
	Provider string `json:"provider,omitempty"`
}

// videoOutDir is where yt-dlp writes files (same tree as other browser artifacts).
const videoOutDir = "runtime/browser/videos"

// videoHosts are sites download_video / auto-routing know about.
var videoHosts = []string{
	"youtube.com", "youtu.be", "youtube-nocookie.com",
	"instagram.com", "instagr.am",
	"tiktok.com", "vm.tiktok.com",
	"twitter.com", "x.com",
	"vimeo.com",
	"facebook.com", "fb.watch",
	"reddit.com", "v.redd.it",
	"twitch.tv", "clips.twitch.tv",
}

// IsVideoURL reports whether s is a bare YouTube / Instagram / TikTok / etc. video link
// (single token). Used for auto-routing a pasted URL with no extra words.
func IsVideoURL(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\n") {
		return false
	}
	return ExtractVideoURL(s) != ""
}

// ExtractVideoURL finds the first http(s) video URL in free text (e.g. "скачай https://youtu.be/…").
// Returns "" if none match a known host.
func ExtractVideoURL(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	// Split on whitespace and common wrappers; also scan substrings starting with http.
	for _, tok := range strings.Fields(text) {
		tok = strings.Trim(tok, "<>\"'.,;)]}")
		if u := matchVideoURL(tok); u != "" {
			return u
		}
	}
	// Fallback: find "http" inside the line (URL glued to punctuation).
	lower := strings.ToLower(text)
	for _, prefix := range []string{"https://", "http://"} {
		idx := 0
		for {
			i := strings.Index(lower[idx:], prefix)
			if i < 0 {
				break
			}
			i += idx
			end := i
			for end < len(text) && !strings.ContainsRune(" \t\n<>\"'", rune(text[end])) {
				end++
			}
			cand := strings.Trim(text[i:end], ".,;)]}")
			if u := matchVideoURL(cand); u != "" {
				return u
			}
			idx = end
		}
	}
	return ""
}

func matchVideoURL(s string) string {
	l := strings.ToLower(strings.TrimSpace(s))
	if !strings.HasPrefix(l, "http://") && !strings.HasPrefix(l, "https://") {
		return ""
	}
	for _, h := range videoHosts {
		if strings.Contains(l, h) {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// FetchVideo downloads a video (YouTube, Instagram, …) at the best available quality.
func FetchVideo(parent context.Context, rawURL, ffmpegPath string) (VideoDownload, error) {
	return FetchMedia(parent, rawURL, ffmpegPath, false)
}

// FetchMedia downloads a URL via yt-dlp and writes it under runtime/browser/videos. ffmpeg is
// used for muxing when present. This does not require Chrome — it shells out to yt-dlp /
// python -m yt_dlp.
//
// audioOnly grabs the best AUDIO stream and skips the video entirely. Speech analysis does not
// need pixels, and on an hour-long talk that is the difference between ~30 MB and a gigabyte —
// plus no muxing pass at the end.
//
// parent is the task context: /stop (or TaskTimeout) kills yt-dlp mid-download rather than
// leaving a 45-minute child running after the user was told "остановлено".
func FetchMedia(parent context.Context, rawURL, ffmpegPath string, audioOnly bool) (VideoDownload, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return VideoDownload{}, fmt.Errorf("нужен URL видео")
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return VideoDownload{}, fmt.Errorf("URL должен начинаться с http(s)://")
	}

	bin, argsPrefix, err := resolveYtDlp()
	if err != nil {
		return VideoDownload{}, err
	}

	if err := os.MkdirAll(videoOutDir, 0o755); err != nil {
		return VideoDownload{}, err
	}

	// Unique template: date + nanoseconds so concurrent downloads never share a prefix.
	stamp := time.Now().Format("20060102-150405.000000000")
	outTmpl := filepath.Join(videoOutDir, stamp+"-%(title).80B.%(ext)s")

	args := append([]string{}, argsPrefix...)
	// Best video+audio, fallback to single best stream. Prefer mp4 when merging.
	format, merge := "bv*+ba/b", "mp4"
	if audioOnly {
		// `ba/b`: лучший звук, иначе — что дали. Никакой перекодировки: ffmpeg на нашей
		// стороне всё равно приведёт файл к 16 кГц моно перед распознаванием.
		format, merge = "ba/b", ""
	}
	args = append(args,
		"--no-playlist",
		"--no-progress",
		"--newline",
		"-f", format,
		"-o", outTmpl,
		"--print", "after_move:filepath",
		"--print", "before_dl:title",
		"--print", "before_dl:ext",
	)
	if merge != "" {
		args = append(args, "--merge-output-format", merge)
	}
	if ff := resolveFFmpeg(ffmpegPath); ff != "" {
		args = append(args, "--ffmpeg-location", ff)
	}
	args = append(args, rawURL)

	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, videoDownloadTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = os.Environ()

	if err := cmd.Run(); err != nil {
		if parent.Err() != nil {
			return VideoDownload{}, context.Canceled
		}
		if ctx.Err() == context.DeadlineExceeded {
			return VideoDownload{}, fmt.Errorf("yt-dlp: таймаут %s — скачивание прервано", videoDownloadTimeout)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		// Cap error text so it fits a Telegram / tool reply.
		if len(msg) > 800 {
			msg = msg[len(msg)-800:]
		}
		return VideoDownload{}, fmt.Errorf("yt-dlp: %s", msg)
	}

	// --print order: before_dl title, before_dl ext, after_move filepath (one per line).
	// Some extractors only print filepath once; be tolerant.
	lines := nonEmptyLines(stdout.String())
	var title, ext, path string
	switch len(lines) {
	case 0:
		// Fallback: newest file in videoOutDir matching stamp prefix.
		path = newestWithPrefix(videoOutDir, stamp+"-")
	case 1:
		path = lines[0]
	case 2:
		title, path = lines[0], lines[1]
	default:
		title, ext, path = lines[0], lines[1], lines[len(lines)-1]
	}
	path = strings.TrimSpace(path)
	if path == "" || !fileExists(path) {
		// filepath print may be relative or yt-dlp wrote elsewhere
		if alt := newestWithPrefix(videoOutDir, stamp+"-"); alt != "" {
			path = alt
		}
	}
	if path == "" || !fileExists(path) {
		return VideoDownload{}, fmt.Errorf("yt-dlp завершился, но файл не найден (stdout: %q)", strings.TrimSpace(stdout.String()))
	}
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if ext == "" {
		ext = strings.TrimPrefix(filepath.Ext(path), ".")
	}
	var size int64
	if fi, err := os.Stat(path); err == nil {
		size = fi.Size()
	}
	return VideoDownload{
		Path:     path,
		Title:    title,
		Ext:      ext,
		Bytes:    size,
		URL:      rawURL,
		Provider: videoProvider(rawURL),
	}, nil
}

// DownloadVideo is the Session-bound tool wrapper: downloads and registers the file as an artifact.
func (s *Session) DownloadVideo(ctx context.Context, rawURL, ffmpegPath string) (VideoDownload, error) {
	res, err := FetchVideo(ctx, rawURL, ffmpegPath)
	if err != nil {
		return res, err
	}
	s.addArtifact(res.Path)
	return res, nil
}

// resolveYtDlp finds an invokable yt-dlp: PATH binary, local tools/, or python -m yt_dlp.
func resolveYtDlp() (bin string, prefix []string, err error) {
	// 1) yt-dlp on PATH / common local names
	candidates := []string{"yt-dlp", "yt-dlp.exe"}
	if runtime.GOOS == "windows" {
		candidates = append([]string{
			filepath.Join("runtime", "bin", "yt-dlp.exe"),
			filepath.Join("tools", "yt-dlp.exe"),
		}, candidates...)
	} else {
		candidates = append([]string{
			filepath.Join("runtime", "bin", "yt-dlp"),
			filepath.Join("tools", "yt-dlp"),
		}, candidates...)
	}
	for _, c := range candidates {
		if p, lookErr := exec.LookPath(c); lookErr == nil {
			return p, nil, nil
		}
		// relative path that exists
		if fileExists(c) {
			return c, nil, nil
		}
	}
	// 2) python -m yt_dlp (pip install)
	for _, py := range []string{"python", "python3", "py"} {
		p, lookErr := exec.LookPath(py)
		if lookErr != nil {
			continue
		}
		// On Windows `py` needs `-3`; try a quick module probe.
		args := []string{"-m", "yt_dlp", "--version"}
		if py == "py" {
			args = append([]string{"-3"}, args...)
		}
		cmd := exec.Command(p, args...)
		if out, runErr := cmd.CombinedOutput(); runErr == nil && len(bytes.TrimSpace(out)) > 0 {
			if py == "py" {
				return p, []string{"-3", "-m", "yt_dlp"}, nil
			}
			return p, []string{"-m", "yt_dlp"}, nil
		}
	}
	return "", nil, fmt.Errorf("yt-dlp не найден. Установи: pip install -U yt-dlp  (или winget install yt-dlp.yt-dlp) и перезапусти бота")
}

// resolveFFmpeg returns an ffmpeg binary path (explicit config, PATH, or empty).
func resolveFFmpeg(configured string) string {
	if configured = strings.TrimSpace(configured); configured != "" {
		if fileExists(configured) {
			return configured
		}
		if p, err := exec.LookPath(configured); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	if p, err := exec.LookPath("ffmpeg.exe"); err == nil {
		return p
	}
	return ""
}

func videoProvider(u string) string {
	l := strings.ToLower(u)
	switch {
	case strings.Contains(l, "youtu"):
		return "youtube"
	case strings.Contains(l, "instagram") || strings.Contains(l, "instagr.am"):
		return "instagram"
	case strings.Contains(l, "tiktok"):
		return "tiktok"
	case strings.Contains(l, "vimeo"):
		return "vimeo"
	case strings.Contains(l, "twitter.com") || strings.Contains(l, "x.com"):
		return "x"
	default:
		return ""
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func newestWithPrefix(dir, prefix string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var best string
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestMod) {
			best = filepath.Join(dir, e.Name())
			bestMod = info.ModTime()
		}
	}
	return best
}

// FormatVideoResult is a compact human/tool-readable summary of a download.
func FormatVideoResult(v VideoDownload) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
