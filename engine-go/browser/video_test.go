package browser

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestYtDlpCommonArgsYouTubeWorkarounds(t *testing.T) {
	args := ytDlpCommonArgs()
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--remote-components") || !strings.Contains(joined, "ejs:github") {
		t.Errorf("нужен ejs:github, получили %v", args)
	}
	if !strings.Contains(joined, "player_client=android") {
		t.Errorf("нужен android player client (иначе YouTube отвечает «не бот»), получили %v", args)
	}
	// Node is the usual Windows case; Deno is yt-dlp default and emits no flag.
	if resolveJSRuntime() != "" && !strings.Contains(joined, "--js-runtimes") {
		t.Errorf("рантайм %q найден, но --js-runtimes нет: %v", resolveJSRuntime(), args)
	}
}

func TestHintYtDlpErrorBotWall(t *testing.T) {
	got := hintYtDlpError("ERROR: Sign in to confirm you’re not a bot")
	if !strings.Contains(got, "yt-dlp-cookies.txt") {
		t.Errorf("на стене YouTube должна быть подсказка про cookies: %q", got)
	}
	plain := hintYtDlpError("ERROR: unable to download video data")
	if strings.Contains(plain, "cookies") {
		t.Errorf("на чужой ошибке подсказка не нужна: %q", plain)
	}
}

func TestYtDlpCookiesFileFromEnv(t *testing.T) {
	dir := t.TempDir()
	path := dir + string(os.PathSeparator) + "cookies.txt"
	if err := os.WriteFile(path, []byte("# Netscape HTTP Cookie File\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIBBORG_YTDLP_COOKIES", path)
	if got := ytDlpCookiesFile(); got != path {
		t.Errorf("ytDlpCookiesFile() = %q, ждали %q", got, path)
	}
	args := ytDlpCommonArgs()
	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--cookies" && args[i+1] == path {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("--cookies %s не попал в аргументы: %v", path, args)
	}
}

// Живой YouTube: без JS-рантайма и android-клиента yt-dlp отвечает «не бот».
// В CI не гоняется — нужен сеть и yt-dlp.
func TestFetchYouTubeShortClip(t *testing.T) {
	if os.Getenv("KIBBORG_LIVE_YOUTUBE") == "" {
		t.Skip("KIBBORG_LIVE_YOUTUBE не задан — живая проверка пропущена")
	}
	if _, _, err := resolveYtDlp(); err != nil {
		t.Skip(err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := FetchVideo(ctx, "https://www.youtube.com/watch?v=jNQXAC9IVRw", "")
	if err != nil {
		t.Fatalf("FetchVideo: %v", err)
	}
	if res.Bytes < 1000 {
		t.Errorf("файл слишком маленький: %+v", res)
	}
	if !fileExists(res.Path) {
		t.Errorf("файл не записан: %s", res.Path)
	}
	t.Logf("скачано %s (%d байт, %s)", res.Title, res.Bytes, res.Path)
	_ = os.Remove(res.Path)
}
