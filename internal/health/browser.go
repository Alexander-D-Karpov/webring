package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

const (
	// pageTimeout bounds one site. Slow member sites are common, but a page that needs
	// longer than this is a finding in itself.
	pageTimeout = 45 * time.Second
	// settleDelay gives client-rendered widgets a moment to appear after load.
	settleDelay = 2 * time.Second
	// reflowDelay is the pause after resizing, which only has to cover a relayout.
	reflowDelay = 400 * time.Millisecond
	// readyPollInterval is how often the document is asked whether it has parsed yet.
	readyPollInterval = 100 * time.Millisecond
	// followTimeout bounds a request that follows a widget link to see if it answers.
	followTimeout = 15 * time.Second
	// retryDelay is the pause before a second attempt at a page whose connection dropped.
	retryDelay = 3 * time.Second
)

// chromeCandidates are the binary names to look for when CHROME_PATH is unset.
var chromeCandidates = []string{
	"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome",
}

// ErrNoBrowser means no Chromium could be found, so checks cannot run.
var ErrNoBrowser = errors.New("no Chromium binary found")

// FindBrowser locates a Chromium to drive. CHROME_PATH wins; otherwise PATH is searched.
func FindBrowser() (string, error) {
	if path := os.Getenv("CHROME_PATH"); path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("CHROME_PATH %q is not usable: %w", path, err)
		}
		return path, nil
	}

	for _, name := range chromeCandidates {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", ErrNoBrowser
}

// Browser drives one long-lived Chromium process. Sites are checked one at a time, each
// in a fresh tab, so a page that hangs or leaks cannot affect the next one.
type Browser struct {
	allocCtx context.Context
	cancel   context.CancelFunc
	http     *http.Client
}

// NewBrowser starts Chromium. Call Close when finished.
func NewBrowser(ctx context.Context, execPath string) (*Browser, error) {
	opts := append([]chromedp.ExecAllocatorOption{},
		chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.ExecPath(execPath),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		// Containers run this as root, where the sandbox refuses to start.
		chromedp.NoSandbox,
		chromedp.WindowSize(Viewports[0].Width, Viewports[0].Height),
		chromedp.UserAgent(browserUserAgent),
	)

	allocCtx, cancel := chromedp.NewExecAllocator(ctx, opts...)
	return &Browser{
		allocCtx: allocCtx,
		cancel:   cancel,
		http:     &http.Client{Timeout: followTimeout},
	}, nil
}

// browserUserAgent identifies the checker while still looking enough like a browser that
// sites do not serve it a stripped-down page.
const browserUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) " +
	"Chrome/120.0.0.0 Safari/537.36 webring-ring-check (+https://otor.ing)"

func (b *Browser) Close() {
	if b != nil && b.cancel != nil {
		b.cancel()
	}
}

// collectScript reads every anchor on the page: where it goes, what it says, what it
// shows, and where it sits relative to the current viewport.
//
// It walks into frames. Plenty of members are old-school framesets whose navigation lives
// in a child document, and the modern equivalent — a widget dropped in as an iframe — is
// just as invisible to a scan of the top-level document alone. Cross-origin frames cannot
// be read, so their src is recorded as a link in its own right: a widget embedded from the
// ring points at the ring by its very address.
//
// It also reports elements the page itself labels as webring furniture. A member building
// the widget in SVG with click handlers has no anchors to find, and would otherwise look
// like it had no widget at all.
const collectScript = `(() => {
  const out = [];
  const markers = new Set();
  const seen = new Set();

  function destination(a, doc) {
    if (a.getAttribute('href')) return a.href;

    const onclick = a.getAttribute('onclick');
    if (!onclick) return null;

    for (const quoted of onclick.match(/['"]([^'"]+)['"]/g) || []) {
      const raw = quoted.slice(1, -1);
      try {
        const url = new URL(raw, doc.baseURI);
        if (url.protocol === 'http:' || url.protocol === 'https:') return url.href;
      } catch (e) { /* not a URL */ }
    }
    return null;
  }

  function label(el) {
    const parts = [el.textContent || '', el.getAttribute('title') || '',
                   el.getAttribute('aria-label') || ''];
    for (const img of el.querySelectorAll('img[alt]')) parts.push(img.getAttribute('alt') || '');
    return parts.join(' ').replace(/\s+/g, ' ').trim().slice(0, 300);
  }

  function pictures(el) {
    const srcs = [];
    for (const img of el.querySelectorAll('img[src]')) srcs.push(img.src);
    if (el.querySelector('svg, picture, canvas')) srcs.push('inline');
    return srcs;
  }

  function measure(el, win, offsetY) {
    const rect = el.getBoundingClientRect();
    const style = win.getComputedStyle(el);
    const visible = rect.width > 0 && rect.height > 0 &&
      style.visibility !== 'hidden' && style.display !== 'none' &&
      parseFloat(style.opacity || '1') > 0;
    return {
      visible: visible,
      top: rect.top + offsetY,
      bottom: rect.bottom + offsetY,
      size: Math.min(rect.width, rect.height),
    };
  }

  function collect(win, offsetY) {
    let doc;
    try { doc = win.document; } catch (e) { return; }
    if (!doc || seen.has(doc)) return;
    seen.add(doc);

    for (const el of doc.querySelectorAll('[id*="webring" i], [class*="webring" i]')) {
      const name = (el.id || el.className || '').toString().slice(0, 40);
      if (name) markers.add(name);
    }

    for (const a of doc.querySelectorAll('a')) {
      const target = destination(a, doc);
      if (!target) continue;
      const box = measure(a, win, offsetY);
      out.push({
        href: target, text: label(a), images: pictures(a),
        visible: box.visible, top: box.top, bottom: box.bottom, size: box.size,
      });
    }

    for (const frame of doc.querySelectorAll('iframe[src], frame[src]')) {
      const box = measure(frame, win, offsetY);
      // A frameset's frames have no useful box of their own; treat them as visible.
      const isFrameset = frame.tagName.toLowerCase() === 'frame';
      out.push({
        href: frame.src, text: frame.getAttribute('title') || '', images: [],
        visible: box.visible || isFrameset,
        top: isFrameset ? 0 : box.top,
        bottom: isFrameset ? 1 : box.bottom,
        size: isFrameset ? 999 : box.size,
      });

      let inner = null;
      try { inner = frame.contentWindow; } catch (e) { inner = null; }
      if (inner) collect(inner, offsetY + frame.getBoundingClientRect().top);
    }
  }

  collect(window, 0);

  const body = document.body;
  return JSON.stringify({
    links: out,
    markers: Array.from(markers).slice(0, 8),
    viewportHeight: window.innerHeight,
    bodyLength: body ? body.innerText.trim().length : 0,
    elementCount: body ? body.querySelectorAll('*').length : 0,
  });
})()`

// rawLink mirrors one entry produced by collectScript.
type rawLink struct {
	Href    string   `json:"href"`
	Text    string   `json:"text"`
	Images  []string `json:"images"`
	Visible bool     `json:"visible"`
	Top     float64  `json:"top"`
	Bottom  float64  `json:"bottom"`
	Size    float64  `json:"size"`
}

// screens is how far down the page the link sits, counted in viewport-fulls. A negative
// result means it does not render at this size at all.
func (l rawLink) screens(viewportHeight float64) int {
	if !l.Visible || viewportHeight <= 0 || l.Bottom <= 0 {
		return -1
	}
	if l.Top < viewportHeight {
		return 0
	}
	return int(l.Top / viewportHeight)
}

type rawPage struct {
	Links          []rawLink `json:"links"`
	Markers        []string  `json:"markers"`
	ViewportHeight float64   `json:"viewportHeight"`
	BodyLength     int       `json:"bodyLength"`
	ElementCount   int       `json:"elementCount"`
}

// Visit loads a URL and reports what the page looks like at every viewport.
//
// A connection that drops mid-handshake is retried once. Those failures are transient —
// the same sites load perfectly on the next sweep — and scoring a member zero because a
// socket closed is worse than the second's delay. A timeout is not retried: the page was
// reachable and genuinely slow, which is the finding.
func (b *Browser) Visit(ctx context.Context, target string) PageObservation {
	obs := b.visitOnce(ctx, target)
	if obs.Rendered || !worthRetrying(obs.RenderError) {
		return obs
	}

	log.Printf("Ring integrity: retrying %s after %q", target, obs.RenderError)
	sleepCtx(ctx, retryDelay)
	return b.visitOnce(ctx, target)
}

// transientErrors are the network failures that come and go between sweeps.
var transientErrors = []string{
	"ERR_CONNECTION_CLOSED", "ERR_CONNECTION_RESET", "ERR_CONNECTION_FAILED",
	"ERR_EMPTY_RESPONSE", "ERR_SOCKET_NOT_CONNECTED", "ERR_NETWORK_CHANGED",
	"ERR_SSL_PROTOCOL_ERROR", "ERR_HTTP2_PROTOCOL_ERROR", "ERR_QUIC_PROTOCOL_ERROR",
}

func worthRetrying(renderError string) bool {
	for _, e := range transientErrors {
		if strings.Contains(renderError, e) {
			return true
		}
	}
	return false
}

func sleepCtx(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// visitOnce is a single attempt at loading and measuring a page.
//
// The page is navigated once, then resized and re-measured for each size. Reflowing beats
// navigating again: it is faster and it compares the same page rather than several
// possibly different responses.
func (b *Browser) visitOnce(ctx context.Context, target string) PageObservation {
	tabCtx, cancelTab := chromedp.NewContext(b.allocCtx)
	defer cancelTab()

	tabCtx, cancelTimeout := context.WithTimeout(tabCtx, pageTimeout)
	defer cancelTimeout()

	// Canceling the caller's context still aborts the visit.
	defer context.AfterFunc(ctx, cancelTimeout)()

	pages := make(map[Viewport]rawPage, len(Viewports))
	var finalURL string
	started := time.Now()
	var renderedAt time.Duration

	actions := []chromedp.Action{
		chromedp.EmulateViewport(int64(Viewports[0].Width), int64(Viewports[0].Height)),
		navigate(target),
		waitForDOM(),
		chromedp.ActionFunc(func(context.Context) error {
			renderedAt = time.Since(started)
			return nil
		}),
		chromedp.Sleep(settleDelay),
		chromedp.Location(&finalURL),
	}

	for i, size := range Viewports {
		if i > 0 {
			actions = append(actions,
				chromedp.EmulateViewport(int64(size.Width), int64(size.Height)),
				chromedp.Sleep(reflowDelay))
		}
		actions = append(actions, collectInto(pages, size.Viewport))
	}

	if err := chromedp.Run(tabCtx, actions...); err != nil {
		return PageObservation{Rendered: false, RenderError: renderError(err), FinalURL: finalURL}
	}

	first := pages[Viewports[0].Viewport]

	// A page that navigated but produced no text and almost no elements did not really
	// render: think a blank body left behind by a failed client-side app.
	if first.BodyLength == 0 && first.ElementCount < 2 {
		return PageObservation{
			Rendered:    false,
			RenderError: "the page loaded but its body is empty",
			FinalURL:    finalURL,
		}
	}

	return PageObservation{
		Rendered:      true,
		FinalURL:      finalURL,
		Links:         mergeViewports(pages),
		NoScriptHrefs: b.noScriptHrefs(ctx, target),
		TimeToRender:  renderedAt,
		WidgetMarkers: first.Markers,
	}
}

// collectInto runs the collector and files the result under a viewport.
func collectInto(pages map[Viewport]rawPage, v Viewport) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		var encoded string
		if err := chromedp.Evaluate(collectScript, &encoded).Do(ctx); err != nil {
			return err
		}
		var result rawPage
		if err := decodeJSON(encoded, &result); err != nil {
			return err
		}
		pages[v] = result
		return nil
	})
}

// noScriptHrefs loads the page again with scripting switched off and reports the links
// that survive.
//
// This is the only honest way to ask whether a widget works without JavaScript. Comparing
// the rendered links against the served markup looked equivalent and was not: a site can
// build one widget in script and ship a different one in a noscript fallback, and the two
// need not share a single URL. Rendering with scripts disabled sees exactly what a reader
// with them off would see, noscript blocks included.
//
// A failure here returns nothing and is treated as no evidence rather than as proof the
// site needs scripts.
func (b *Browser) noScriptHrefs(ctx context.Context, target string) []string {
	tabCtx, cancelTab := chromedp.NewContext(b.allocCtx)
	defer cancelTab()

	tabCtx, cancelTimeout := context.WithTimeout(tabCtx, pageTimeout)
	defer cancelTimeout()

	defer context.AfterFunc(ctx, cancelTimeout)()

	var collected rawPage
	err := chromedp.Run(tabCtx,
		// Must be set before navigating, or the page's scripts have already run.
		emulation.SetScriptExecutionDisabled(true),
		chromedp.EmulateViewport(int64(Viewports[0].Width), int64(Viewports[0].Height)),
		navigate(target),
		waitForDOM(),
		chromedp.Sleep(settleDelay),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var encoded string
			if evalErr := chromedp.Evaluate(collectScript, &encoded).Do(ctx); evalErr != nil {
				return evalErr
			}
			return decodeJSON(encoded, &collected)
		}),
	)
	if err != nil {
		log.Printf("Ring integrity: could not load %s with scripts off: %v", target, err)
		return nil
	}

	// A document that produced no text and almost no elements did not load; saying
	// nothing survived would be an accusation drawn from a blank page.
	if collected.BodyLength == 0 && collected.ElementCount < 2 {
		log.Printf("Ring integrity: %s rendered empty with scripts off, drawing no conclusion", target)
		return nil
	}

	hrefs := make([]string, 0, len(collected.Links))
	for _, l := range collected.Links {
		hrefs = append(hrefs, l.Href)
	}
	return hrefs
}

// mergeViewports folds the per-viewport measurements into one set of links.
//
// The result is deduplicated by href. The same link often appears more than once — a
// widget repeated in a frame and in the page around it — and counting it twice would
// distort how many links the widget appears to have.
func mergeViewports(pages map[Viewport]rawPage) []Link {
	index := map[string]int{}
	var links []Link

	for _, size := range Viewports {
		measured, ok := pages[size.Viewport]
		if !ok {
			continue
		}

		for _, l := range measured.Links {
			at, seen := index[l.Href]
			if !seen {
				index[l.Href] = len(links)
				links = append(links, Link{Href: l.Href, Screens: map[Viewport]int{}})
				at = len(links) - 1
			}

			link := &links[at]
			if l.Visible {
				link.Visible = true
			}
			if link.Text == "" {
				link.Text = l.Text
			}
			if len(link.Images) == 0 {
				link.Images = l.Images
			}
			if screens := l.screens(measured.ViewportHeight); screens >= 0 {
				if existing, ok := link.Screens[size.Viewport]; !ok || screens < existing {
					link.Screens[size.Viewport] = screens
				}
			}
			if size.Viewport == ViewportMobile && l.Visible && l.Size > 0 {
				if link.TapSize == 0 || l.Size < link.TapSize {
					link.TapSize = l.Size
				}
			}
		}
	}

	return links
}

// navigate starts a navigation without waiting for the load event.
//
// chromedp.Navigate blocks until the page has finished loading everything, which for a
// member site pulling in slow third-party images means a timeout and a render_failed on a
// page whose markup arrived long ago. The widget lives in the DOM, so that is what we
// wait for.
func navigate(target string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		_, _, errorText, _, err := page.Navigate(target).Do(ctx)
		if err != nil {
			return err
		}
		if errorText != "" {
			return fmt.Errorf("%s", errorText)
		}
		return nil
	})
}

// waitForDOM blocks until the requested document is parsed.
//
// It insists the tab has actually left about:blank first. A fresh tab starts there with a
// readyState of "complete", and navigate returns as soon as the navigation is *initiated*,
// so waiting on readyState alone can be satisfied by the blank page the browser was
// already showing and hand the collector an empty document.
func waitForDOM() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		for {
			var encoded string
			err := chromedp.Evaluate(
				`JSON.stringify({ready: document.readyState, url: location.href})`,
				&encoded).Do(ctx)
			if err == nil {
				var state struct {
					Ready string `json:"ready"`
					URL   string `json:"url"`
				}
				if decodeJSON(encoded, &state) == nil &&
					state.URL != "" && state.URL != "about:blank" &&
					(state.Ready == "interactive" || state.Ready == "complete") {
					return nil
				}
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(readyPollInterval):
			}
		}
	})
}

// FollowRingLink follows a widget link to check that it still works.
//
// Only failure is interesting. A /{slug}/next URL resolves through the ring, so whoever
// it arrives at is current by definition — there is nothing for the member to get wrong
// except the slug, and a wrong slug is already its own finding. What this catches is an
// endpoint that answers with an error, which means the slug no longer exists at all.
func (b *Browser) FollowRingLink(ctx context.Context, href string) NeighborCheck {
	ctx, cancel := context.WithTimeout(ctx, followTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, href, http.NoBody)
	if err != nil {
		return NeighborCheck{Err: err.Error()}
	}
	req.Header.Set("User-Agent", browserUserAgent)

	resp, err := b.http.Do(req)
	if err != nil {
		return NeighborCheck{Err: "could not be followed"}
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("Error closing followed link body: %v", closeErr)
		}
	}()
	if _, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16)); err != nil {
		log.Printf("Error draining followed link body: %v", err)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return NeighborCheck{Err: fmt.Sprintf("answers %d", resp.StatusCode)}
	}

	return NeighborCheck{Reached: hostOf(resp.Request.URL.String()), OK: true}
}

func renderError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("the page did not finish loading within %s", pageTimeout)
	}
	return err.Error()
}

func decodeJSON(encoded string, out interface{}) error {
	if encoded == "" {
		return nil
	}
	return json.Unmarshal([]byte(encoded), out)
}
