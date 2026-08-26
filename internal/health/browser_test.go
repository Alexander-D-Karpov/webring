package health

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testBrowser starts a real Chromium, or skips when none is installed. Everything below
// drives an actual browser against a local server, because the point of this layer is
// what a browser sees — a fake would only test the fake.
func testBrowser(t *testing.T) *Browser {
	t.Helper()

	execPath, err := FindBrowser()
	if err != nil {
		t.Skipf("no Chromium available: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	browser, err := NewBrowser(ctx, execPath)
	if err != nil {
		t.Fatalf("starting Chromium: %v", err)
	}
	t.Cleanup(browser.Close)

	return browser
}

// servePage returns the URL of a server that answers every path with the given HTML.
func servePage(t *testing.T, html string) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := w.Write([]byte(html)); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	return server.URL
}

func visit(t *testing.T, browser *Browser, url string) PageObservation {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	return browser.Visit(ctx, url)
}

// linkFor finds a collected anchor by the tail of its href.
func linkFor(t *testing.T, obs PageObservation, suffix string) Link {
	t.Helper()
	for _, l := range obs.Links {
		if strings.HasSuffix(l.Href, suffix) {
			return l
		}
	}
	t.Fatalf("no link ending in %q; collected: %v", suffix, hrefs(obs))
	return Link{}
}

func hrefs(obs PageObservation) []string {
	out := make([]string, 0, len(obs.Links))
	for _, l := range obs.Links {
		out = append(out, l.Href)
	}
	return out
}

func TestVisitCollectsAnchorsAndResolvesThem(t *testing.T) {
	browser := testBrowser(t)
	url := servePage(t, `<html><body>
		<a href="https://otor.ing/mine/prev">prev</a>
		<a href="/relative/page">relative</a>
		<p>some text</p>
	</body></html>`)

	obs := visit(t, browser, url)

	if !obs.Rendered {
		t.Fatalf("page did not render: %s", obs.RenderError)
	}
	if len(obs.Links) != 2 {
		t.Fatalf("collected %d links, want 2: %v", len(obs.Links), hrefs(obs))
	}

	// Relative hrefs must come back absolute, or the analyzer cannot compare them.
	relative := linkFor(t, obs, "/relative/page")
	if !strings.HasPrefix(relative.Href, "http://") {
		t.Errorf("relative href was not resolved: %q", relative.Href)
	}
	if obs.FinalURL == "" {
		t.Errorf("FinalURL is empty")
	}
}

func TestVisitSeesAWidgetAtTheTopOfThePage(t *testing.T) {
	browser := testBrowser(t)
	url := servePage(t, `<html><body>
		<a href="https://otor.ing/mine/next">next</a>
		<p>content</p>
	</body></html>`)

	link := linkFor(t, visit(t, browser, url), "/mine/next")

	if !link.Visible {
		t.Errorf("a plain visible link was reported invisible")
	}
	if !link.aboveFold(ViewportDesktop) {
		t.Errorf("a link at the top of the page is not above the desktop fold")
	}
	if !link.aboveFold(ViewportMobile) {
		t.Errorf("a link at the top of the page is not above the mobile fold")
	}
}

// The whole reason for driving a browser: a link pushed down the page is only detectable
// by laying the page out.
func TestVisitDetectsALinkBelowTheFold(t *testing.T) {
	browser := testBrowser(t)
	url := servePage(t, `<html><body>
		<div style="height:4000px">a very tall page</div>
		<a href="https://otor.ing/mine/next">next</a>
	</body></html>`)

	link := linkFor(t, visit(t, browser, url), "/mine/next")

	if !link.Visible {
		t.Errorf("link below the fold should still be visible")
	}
	if link.aboveFold(ViewportDesktop) {
		t.Errorf("link 4000px down was reported above the desktop fold")
	}
	if link.aboveFold(ViewportMobile) {
		t.Errorf("link 4000px down was reported above the mobile fold")
	}
}

func TestVisitDetectsHiddenLinks(t *testing.T) {
	cases := map[string]string{
		"display none":    `style="display:none"`,
		"visibility":      `style="visibility:hidden"`,
		"zero size":       `style="display:block;width:0;height:0;overflow:hidden"`,
		"zero opacity":    `style="opacity:0"`,
		"hidden ancestor": `data-x="1"`,
	}

	browser := testBrowser(t)
	for name, attr := range cases {
		t.Run(name, func(t *testing.T) {
			body := fmt.Sprintf(`<a href="https://otor.ing/mine/next" %s>next</a>`, attr)
			if name == "hidden ancestor" {
				body = fmt.Sprintf(`<div style="display:none">%s</div>`, body)
			}
			url := servePage(t, "<html><body><p>text</p>"+body+"</body></html>")

			link := linkFor(t, visit(t, browser, url), "/mine/next")
			if link.Visible {
				t.Errorf("%s link was reported visible", name)
			}
			if link.aboveFold(ViewportDesktop) || link.aboveFold(ViewportMobile) {
				t.Errorf("%s link was reported above the fold", name)
			}
		})
	}
}

// A widget that a narrow layout hides is exactly the case the mobile pass exists for.
func TestVisitSeesAWidgetHiddenOnlyOnMobile(t *testing.T) {
	browser := testBrowser(t)
	url := servePage(t, `<html><head><style>
		@media (max-width: 500px) { .ring { display: none } }
	</style></head><body>
		<a class="ring" href="https://otor.ing/mine/next">next</a>
		<p>content</p>
	</body></html>`)

	link := linkFor(t, visit(t, browser, url), "/mine/next")

	if !link.aboveFold(ViewportDesktop) {
		t.Errorf("link should be above the fold on desktop")
	}
	if link.aboveFold(ViewportMobile) {
		t.Errorf("link hidden by a media query was reported above the mobile fold")
	}
}

// Widgets injected by script are invisible to a plain HTML fetch.
func TestVisitSeesScriptInjectedWidgets(t *testing.T) {
	browser := testBrowser(t)
	url := servePage(t, `<html><body><div id="slot"></div><script>
		document.getElementById('slot').innerHTML =
			'<a href="https://otor.ing/mine/next">next</a>';
	</script></body></html>`)

	link := linkFor(t, visit(t, browser, url), "/mine/next")
	if !link.Visible {
		t.Errorf("script-injected link was not seen as visible")
	}
}

func TestVisitReportsAnEmptyBodyAsNotRendered(t *testing.T) {
	browser := testBrowser(t)
	obs := visit(t, browser, servePage(t, `<html><body></body></html>`))

	if obs.Rendered {
		t.Errorf("an empty body was reported as rendered")
	}
	if obs.RenderError == "" {
		t.Errorf("no explanation for the failed render")
	}
}

func TestVisitReportsAnUnreachableHostAsNotRendered(t *testing.T) {
	browser := testBrowser(t)
	// .invalid can never resolve, by RFC 2606.
	obs := visit(t, browser, "https://this-host-does-not-exist.invalid/")

	if obs.Rendered {
		t.Errorf("an unreachable host was reported as rendered")
	}
	if obs.RenderError == "" {
		t.Errorf("no explanation for the failed render")
	}
}

func TestVisitFollowsRedirectsAndReportsTheFinalURL(t *testing.T) {
	browser := testBrowser(t)

	final := servePage(t, `<html><body><p>arrived</p>
		<a href="https://otor.ing/mine/next">next</a></body></html>`)
	start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final, http.StatusFound)
	}))
	t.Cleanup(start.Close)

	obs := visit(t, browser, start.URL)

	if !obs.Rendered {
		t.Fatalf("page did not render: %s", obs.RenderError)
	}
	if !strings.HasPrefix(obs.FinalURL, final) {
		t.Errorf("FinalURL = %q, want it to be under %q", obs.FinalURL, final)
	}
}

// End to end through the real browser: a page laid out this way must come out as a
// below-the-fold finding, not as a healthy widget.
func TestBrowserAndAnalyzerAgreeOnABuriedWidget(t *testing.T) {
	browser := testBrowser(t)
	url := servePage(t, `<html><body>
		<div style="height:3000px">tall</div>
		<a href="https://otor.ing/mine/next">next</a>
	</body></html>`)

	obs := visit(t, browser, url)
	ctx := ring()
	ctx.Site.URL = url

	report := Analyze(obs, ctx)

	if !report.Has(CodeBelowFold) {
		t.Errorf("expected a below_fold finding, got %v", codes(report))
	}
	// The exact score belongs to the analyzer's own tests; what matters here is that a
	// page laid out this way survives the round trip through a real browser as a burial.
	if report.Score >= 100 {
		t.Errorf("score = %d, want a deduction for the burial", report.Score)
	}
}

func TestFindBrowserPrefersChromePath(t *testing.T) {
	t.Setenv("CHROME_PATH", "/definitely/not/here")

	if _, err := FindBrowser(); err == nil {
		t.Errorf("FindBrowser accepted a CHROME_PATH that does not exist")
	}
}

// pagesFrom builds a per-viewport measurement set from one list per size.
func pagesFrom(byViewport map[Viewport][]rawLink, height float64) map[Viewport]rawPage {
	out := map[Viewport]rawPage{}
	for v, links := range byViewport {
		out[v] = rawPage{Links: links, ViewportHeight: height}
	}
	return out
}

func TestMergeViewportsRecordsEachSizeSeparately(t *testing.T) {
	merged := mergeViewports(pagesFrom(map[Viewport][]rawLink{
		ViewportDesktop: {{Href: "https://a.example", Visible: true, Top: 10, Bottom: 30, Size: 20}},
		ViewportMobile:  {{Href: "https://a.example", Visible: true, Top: 1400, Bottom: 1420, Size: 20}},
	}, 700))

	if len(merged) != 1 {
		t.Fatalf("merged %d links, want 1", len(merged))
	}
	if got := merged[0].Screens[ViewportDesktop]; got != 0 {
		t.Errorf("desktop screens = %d, want 0", got)
	}
	if got := merged[0].Screens[ViewportMobile]; got != 2 {
		t.Errorf("mobile screens = %d, want 2", got)
	}
	if merged[0].TapSize != 20 {
		t.Errorf("tap size = %v, want the mobile measurement", merged[0].TapSize)
	}
}

func TestMergeViewportsDeduplicatesByHref(t *testing.T) {
	merged := mergeViewports(pagesFrom(map[Viewport][]rawLink{
		ViewportDesktop: {
			{Href: "https://a.example", Visible: false, Top: 10, Bottom: 30},
			{Href: "https://a.example", Visible: true, Top: 10, Bottom: 30, Size: 40},
		},
	}, 700))

	if len(merged) != 1 {
		t.Fatalf("merged %d links, want the duplicate collapsed", len(merged))
	}
	if !merged[0].Visible {
		t.Errorf("merged = %+v, want the more favorable sighting kept", merged[0])
	}
}

func TestWorthRetryingRecognizesTransientFailures(t *testing.T) {
	cases := map[string]bool{
		"net::ERR_CONNECTION_CLOSED":                 true,
		"net::ERR_CONNECTION_RESET":                  true,
		"net::ERR_HTTP2_PROTOCOL_ERROR":              true,
		"net::ERR_NAME_NOT_RESOLVED":                 false,
		"net::ERR_CONNECTION_REFUSED":                false,
		"the page did not finish loading within 45s": false,
		"the page loaded but its body is empty":      false,
	}

	for renderError, want := range cases {
		if got := worthRetrying(renderError); got != want {
			t.Errorf("worthRetrying(%q) = %v, want %v", renderError, got, want)
		}
	}
}

// A page that drops the connection once and answers the second time must not be scored as
// if it were down.
func TestVisitRetriesAConnectionThatDropsOnce(t *testing.T) {
	browser := testBrowser(t)

	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			// Hijack and close without a response, which Chromium reports as a dropped
			// connection.
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Errorf("the test server cannot hijack connections")
				return
			}
			conn, _, hijackErr := hijacker.Hijack()
			if hijackErr != nil {
				t.Errorf("hijacking: %v", hijackErr)
				return
			}
			if closeErr := conn.Close(); closeErr != nil {
				t.Errorf("closing hijacked connection: %v", closeErr)
			}
			return
		}
		w.Header().Set("Content-Type", "text/html")
		if _, err := io.WriteString(w, `<html><body><p>second time lucky</p>
			<a href="https://otor.ing/mine/next">next</a></body></html>`); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	obs := visit(t, browser, server.URL)

	if !obs.Rendered {
		t.Fatalf("the retry did not recover the page: %s", obs.RenderError)
	}
	if attempts < 2 {
		t.Errorf("the page was fetched %d times, want a retry", attempts)
	}
}
