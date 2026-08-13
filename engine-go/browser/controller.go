package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// This file is the page_controller from the spec: it drives the browser — navigation, tabs,
// clicks, typing, forms, scrolling, uploads, selects, drag-and-drop.

// OpenURL navigates the current tab to url (or opens it in a new tab when newTab is true).
// It waits for the body to be ready so subsequent DOM reads see a loaded page.
func (s *Session) OpenURL(rawURL string, newTab bool) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("пустой URL")
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	// Block SSRF/LFI: only public http(s) targets (no file://, chrome://, 127.0.0.1, metadata IP).
	safe, err := safeRemoteURL(rawURL)
	if err != nil {
		return "", err
	}
	rawURL = safe
	if newTab {
		return s.openNewTab(rawURL)
	}
	if err := s.run(chromedp.Navigate(rawURL), chromedp.WaitReady("body", chromedp.ByQuery)); err != nil {
		return "", err
	}
	return s.CurrentURL()
}

// openNewTab creates a fresh tab, wires capture onto it, navigates, and makes it current.
func (s *Session) openNewTab(rawURL string) (string, error) {
	s.actMu.Lock()
	defer s.actMu.Unlock()
	if err := s.ensureAlloc(); err != nil {
		return "", err
	}
	s.detachPage()
	ctx, cancel := chromedp.NewContext(s.allocCtx)
	chromedp.ListenTarget(ctx, s.onCDPEvent)
	// Bounded run: WaitReady("body") never fires on body-less documents (XML/SVG feeds) and a
	// tar-pit server can stall Navigate — without a deadline this would hold actMu forever and
	// deadlock every subsequent tool call. On timeout cancel() closes OUR half-created tab only.
	rctx, rcancel := context.WithTimeout(ctx, 45*time.Second)
	defer rcancel()
	if err := chromedp.Run(rctx, enableCaptureDomains(), chromedp.Navigate(rawURL), chromedp.WaitReady("body", chromedp.ByQuery)); err != nil {
		cancel()
		return "", err
	}
	s.pageCtx, s.pageCancel = ctx, cancel
	if c := chromedp.FromContext(ctx); c != nil && c.Target != nil {
		s.targetID = c.Target.TargetID.String()
	}
	var url string
	lctx, lcancel := context.WithTimeout(ctx, 5*time.Second)
	defer lcancel()
	_ = chromedp.Run(lctx, chromedp.Location(&url))
	return url, nil
}

// ClosePage closes a tab by index (from list_tabs) or, when index<0, the current tab. After
// closing the current tab the session detaches; the next tool re-attaches to another tab.
func (s *Session) ClosePage(index int) error {
	s.actMu.Lock()
	defer s.actMu.Unlock()
	closeID := s.targetID // read under actMu — attach/openNewTab write it under the same lock
	if index >= 0 {
		tabs, err := s.ListTabs()
		if err != nil {
			return err
		}
		if index >= len(tabs) {
			return fmt.Errorf("индекс вкладки %d вне диапазона", index)
		}
		closeID = tabs[index].ID
	}
	if closeID == "" {
		return fmt.Errorf("нет активной вкладки для закрытия")
	}
	// CloseTarget is a browser-level command: run it on the browser executor of an attached
	// page context. A fresh chromedp.NewContext here would first create (and briefly leak) an
	// about:blank tab in the user's Chrome.
	if err := s.ensurePage(); err != nil {
		return err
	}
	closingCurrent := closeID == s.targetID
	c := chromedp.FromContext(s.pageCtx)
	if c == nil || c.Browser == nil {
		return fmt.Errorf("нет активного подключения к Chrome")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := target.CloseTarget(target.ID(closeID)).Do(cdp.WithExecutor(ctx, c.Browser)); err != nil {
		return fmt.Errorf("не смог закрыть вкладку: %w", err)
	}
	if closingCurrent {
		s.detachPage()
		s.targetID = "" // force re-pick on next action
	}
	return nil
}

// Click clicks the first element matching the CSS selector (waits for it to be visible).
func (s *Session) Click(selector string) error {
	if strings.TrimSpace(selector) == "" {
		return fmt.Errorf("нужен CSS-селектор элемента")
	}
	return s.run(chromedp.Click(selector, chromedp.ByQuery, chromedp.NodeVisible))
}

// ClickText clicks the first visible element whose text matches — то, что делает человек.
//
// Живой прогон на Telegram Web показал, чего не хватало: get_html на большом SPA упирается в
// таймаут и обрезается, модель начинает УГАДЫВАТЬ селекторы, а chromedp.Click ждёт несуществующий
// узел все 45 секунд. Шесть таких попыток подряд — восемь минут в никуда. Кнопку «Сжать контекст»
// человек находит по надписи, а не по классу; здесь то же самое, и делает это JS в самой странице,
// без разрешения узлов через DevTools.
//
// Точное совпадение надписи имеет приоритет над вхождением: на странице с кнопками «Стоп» и
// «Остановить всё» просьба нажать «Стоп» должна попадать в первую.
func (s *Session) ClickText(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("нужен текст элемента")
	}
	raw, _ := json.Marshal(text)
	js := `(() => {
  const want = ` + string(raw) + `.trim().toLowerCase();
  const norm = s => (s || "").replace(/\s+/g, " ").trim().toLowerCase();
  const visible = el => {
    if (!(el instanceof Element)) return false;
    const r = el.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) return false;
    const st = getComputedStyle(el);
    return st.visibility !== "hidden" && st.display !== "none" && st.pointerEvents !== "none";
  };
  const clickable = "button,a,[role=button],[role=menuitem],[role=tab],input[type=button],input[type=submit],.btn,.button";
  const pools = [Array.from(document.querySelectorAll(clickable)), Array.from(document.querySelectorAll("*"))];
  for (const exact of [true, false]) {
    for (const pool of pools) {
      for (const el of pool) {
        if (!visible(el)) continue;
        const t = norm(el.innerText || el.textContent);
        if (!t || t.length > 200) continue;
        const hit = exact ? t === want : t.includes(want);
        if (!hit) continue;
        // Внутри найденного мог оказаться более точный вложенный элемент — берём самый глубокий,
        // иначе клик уходит в контейнер и промахивается мимо кнопки.
        let target = el;
        for (const child of el.querySelectorAll(clickable)) {
          if (visible(child) && norm(child.innerText || child.textContent).includes(want)) { target = child; break; }
        }
        target.scrollIntoView({block: "center"});
        const r = target.getBoundingClientRect();
        return JSON.stringify({
          x: r.left + r.width / 2, y: r.top + r.height / 2,
          tag: target.tagName.toLowerCase(),
          label: norm(target.innerText || target.textContent).slice(0, 80),
        });
      }
    }
  }
  return "MISS";
})()`
	var res string
	if err := s.run(chromedp.Evaluate(js, &res)); err != nil {
		return err
	}
	if res == "MISS" || res == "" {
		return fmt.Errorf("на странице нет видимого элемента с текстом %q — проверь надпись через get_text", text)
	}
	var hit struct {
		X, Y  float64
		Tag   string
		Label string
	}
	if err := json.Unmarshal([]byte(res), &hit); err != nil {
		return fmt.Errorf("не разобрал координаты элемента: %w", err)
	}
	// Жмём НАСТОЯЩИМ событием мыши через CDP, а не el.click() из JS.
	//
	// Живой прогон на Telegram Web: JS-клик отрабатывал «успешно», а чат не открывался.
	// Приложение слушает pointerdown/mousedown, и синтетический click мимо них проходит
	// незамеченным. Отчёт при этом был бодрый — ровно тот сорт лжи, который здесь ловят.
	// Поиск по надписи остаётся за JS (он видит текст), а нажатие делает браузер.
	return s.run(chromedp.MouseClickXY(hit.X, hit.Y))
}

// TypeText focuses the selector and types text. When clearFirst is set the field is cleared
// before typing (so "type" replaces instead of appends).
func (s *Session) TypeText(selector, text string, clearFirst bool) error {
	if strings.TrimSpace(selector) == "" {
		return fmt.Errorf("нужен CSS-селектор поля")
	}
	if clearFirst {
		// Clear may fail on non-clearable nodes — best-effort, ignore its error.
		_ = s.run(chromedp.Clear(selector, chromedp.ByQuery))
	}
	return s.run(chromedp.SendKeys(selector, text, chromedp.ByQuery))
}

// Scroll scrolls the page. amount is pixels (positive = down); "bottom"/"top" jump to edges.
// A non-empty selector scrolls that element into view instead.
func (s *Session) Scroll(selector, to string, amount int) error {
	if strings.TrimSpace(selector) != "" {
		return s.run(chromedp.ScrollIntoView(selector, chromedp.ByQuery))
	}
	var js string
	switch strings.ToLower(strings.TrimSpace(to)) {
	case "bottom":
		js = "window.scrollTo(0, document.body.scrollHeight)"
	case "top":
		js = "window.scrollTo(0, 0)"
	default:
		if amount == 0 {
			amount = 600
		}
		js = fmt.Sprintf("window.scrollBy(0, %d)", amount)
	}
	return s.run(chromedp.Evaluate(js, nil))
}

// SelectOption sets a <select> to the given value (or visible option text) and fires the
// change event so frameworks react.
func (s *Session) SelectOption(selector, value string) error {
	if strings.TrimSpace(selector) == "" {
		return fmt.Errorf("нужен CSS-селектор <select>")
	}
	js := fmt.Sprintf(`(() => {
  const el = document.querySelector(%q);
  if (!el) return "not found";
  const want = %q;
  let matched = false;
  for (const o of el.options) {
    if (o.value === want || o.text.trim() === want) { el.value = o.value; matched = true; break; }
  }
  if (!matched) return "option not found";
  el.dispatchEvent(new Event('change', {bubbles: true}));
  return "ok";
})()`, selector, value)
	var res string
	if err := s.run(chromedp.Evaluate(js, &res)); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("select: %s", res)
	}
	return nil
}

// SubmitForm submits the form matching the selector (the form itself or any element in it).
func (s *Session) SubmitForm(selector string) error {
	if strings.TrimSpace(selector) == "" {
		selector = "form"
	}
	return s.run(chromedp.Submit(selector, chromedp.ByQuery))
}

// UploadFile sets the chosen local file paths on a file <input>.
func (s *Session) UploadFile(selector string, paths []string) error {
	if strings.TrimSpace(selector) == "" {
		selector = "input[type=file]"
	}
	if len(paths) == 0 {
		return fmt.Errorf("не указаны пути к файлам для загрузки")
	}
	// Only allow uploading files the agent itself produced (screenshots/downloads/clones),
	// never arbitrary local files — otherwise indirect prompt injection could exfiltrate
	// secrets (e.g. ~/.ssh/id_rsa) by uploading them to an attacker's form.
	safePaths := make([]string, 0, len(paths))
	for _, p := range paths {
		abs, err := safeArtifactPath(p)
		if err != nil {
			return err
		}
		safePaths = append(safePaths, abs)
	}
	return s.run(chromedp.SetUploadFiles(selector, safePaths, chromedp.ByQuery))
}

// DragElement simulates an HTML5 drag-and-drop from one selector to another. This is a
// best-effort JS simulation that works with most DnD libraries; native OS drags aren't
// reproduced. Use specific selectors for reliability.
func (s *Session) DragElement(fromSelector, toSelector string) error {
	if strings.TrimSpace(fromSelector) == "" || strings.TrimSpace(toSelector) == "" {
		return fmt.Errorf("нужны селекторы источника и цели")
	}
	js := fmt.Sprintf(`(() => {
  const src = document.querySelector(%q), dst = document.querySelector(%q);
  if (!src || !dst) return "not found";
  const dt = new DataTransfer();
  const fire = (el, type) => el.dispatchEvent(new DragEvent(type, {bubbles: true, cancelable: true, dataTransfer: dt}));
  fire(src, 'dragstart'); fire(dst, 'dragenter'); fire(dst, 'dragover'); fire(dst, 'drop'); fire(src, 'dragend');
  return "ok";
})()`, fromSelector, toSelector)
	var res string
	if err := s.run(chromedp.Evaluate(js, &res)); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("drag: %s", res)
	}
	return nil
}

// CurrentURL returns the URL of the current tab.
func (s *Session) CurrentURL() (string, error) {
	var url string
	if err := s.run(chromedp.Location(&url)); err != nil {
		return "", err
	}
	return url, nil
}
