package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingServer answers like a page and keeps every request header it was sent.
type recordingServer struct {
	url string

	mu       sync.Mutex
	requests []http.Header
}

func serveRecording(t *testing.T, html string) *recordingServer {
	t.Helper()

	rec := &recordingServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.requests = append(rec.requests, r.Header.Clone())
		rec.mu.Unlock()

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := w.Write([]byte(html)); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	rec.url = server.URL
	return rec
}

func (r *recordingServer) headers() []http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]http.Header{}, r.requests...)
}

// userAgents returns the User-Agent of every request that reached the server.
func (r *recordingServer) userAgents() []string {
	var out []string
	for _, h := range r.headers() {
		out = append(out, h.Get("User-Agent"))
	}
	return out
}

// A member who can recognize the checker can serve it a page nobody else is shown, and the
// score would then describe that page instead of the real one. The old User-Agent said
// "webring-ring-check" outright.
func TestCheckerDoesNotAnnounceItself(t *testing.T) {
	browser := testBrowser(t)
	rec := serveRecording(t, `<html><body><p>hello</p></body></html>`)

	visit(t, browser, rec.url)

	agents := rec.userAgents()
	if len(agents) == 0 {
		t.Fatal("the page was never requested")
	}
	for _, ua := range agents {
		for _, tell := range []string{"webring", "ring-check", "Headless", "otor.ing", "bot"} {
			if strings.Contains(strings.ToLower(ua), strings.ToLower(tell)) {
				t.Errorf("User-Agent %q gives the checker away with %q", ua, tell)
			}
		}
		if !strings.Contains(ua, "Chrome/") || !strings.HasPrefix(ua, "Mozilla/5.0 (") {
			t.Errorf("User-Agent %q is not an ordinary browser string", ua)
		}
	}
}

// The disguise has to be the same everywhere it can be read. A page that finds Windows in
// the client hints behind a Linux User-Agent, or a navigator.userAgent that disagrees with
// the header, has spotted the checker just as surely as a name would have let it.
func TestIdentityIsConsistentAcrossHeaderHintsAndScripts(t *testing.T) {
	browser := testBrowser(t)

	// The page reports back what scripts can see, as a link the collector will pick up.
	rec := serveRecording(t, `<html><body><a id="probe" href="/x">probe</a>
		<script>
		  document.getElementById('probe').href =
		    '/seen?ua=' + encodeURIComponent(navigator.userAgent) +
		    '&platform=' + encodeURIComponent(navigator.platform);
		</script></body></html>`)

	obs := visit(t, browser, rec.url)
	if !obs.Rendered {
		t.Fatalf("page did not render: %s", obs.RenderError)
	}

	var reported string
	for _, l := range obs.Links {
		if strings.Contains(l.Href, "/seen?") {
			reported = l.Href
			break
		}
	}
	if !strings.Contains(reported, "/seen?") {
		t.Fatalf("the page never reported what scripts saw; got %v", hrefs(obs))
	}

	scriptUA := valueOf(t, reported, "ua")
	scriptPlatform := valueOf(t, reported, "platform")

	header := rec.headers()[0]
	headerUA := header.Get("User-Agent")

	if scriptUA != headerUA {
		t.Errorf("navigator.userAgent %q does not match the header %q", scriptUA, headerUA)
	}

	// navigator.platform and the User-Agent have to describe the same machine.
	wantPlatform := map[string]string{
		"Windows NT":   "Win32",
		"Macintosh":    "MacIntel",
		"Linux x86_64": "Linux x86_64",
	}
	matched := false
	for inUA, wantNav := range wantPlatform {
		if strings.Contains(headerUA, inUA) {
			matched = true
			if scriptPlatform != wantNav {
				t.Errorf("User-Agent says %q but navigator.platform says %q", inUA, scriptPlatform)
			}
		}
	}
	if !matched {
		t.Errorf("User-Agent %q names no platform we recognize", headerUA)
	}

	// Chrome sends the platform again in the client hints, where a mismatch is just as
	// visible. httptest speaks HTTP/1.1 without TLS, so hints are only sent when present.
	if hint := header.Get("Sec-CH-UA-Platform"); hint != "" {
		wantHint := map[string]string{
			"Windows NT":   `"Windows"`,
			"Macintosh":    `"macOS"`,
			"Linux x86_64": `"Linux"`,
		}
		for inUA, want := range wantHint {
			if strings.Contains(headerUA, inUA) && hint != want {
				t.Errorf("User-Agent says %q but Sec-CH-UA-Platform says %s", inUA, hint)
			}
		}
	}
	if hint := header.Get("Sec-CH-UA"); hint != "" {
		major := majorIn(headerUA)
		if major != "" && !strings.Contains(hint, `"`+major+`"`) {
			t.Errorf("Sec-CH-UA %s does not carry the version %s from the User-Agent", hint, major)
		}
	}
}

// Every load of one site — four viewports and the scripts-off pass — has to come from the
// same identity. A site that varies its markup by platform would otherwise be compared
// against a different version of itself and reported for a difference it never made.
func TestOneIdentityPerSite(t *testing.T) {
	browser := testBrowser(t)
	rec := serveRecording(t, `<html><body><a href="/next">next</a></body></html>`)

	visit(t, browser, rec.url)

	agents := rec.userAgents()
	if len(agents) < 2 {
		t.Fatalf("expected the page to be loaded more than once, got %d", len(agents))
	}
	for _, ua := range agents[1:] {
		if ua != agents[0] {
			t.Errorf("one visit used two identities:\n  %q\n  %q", agents[0], ua)
		}
	}
}

// Across sites the identity has to move, or it is just a fixed name again.
func TestIdentityVariesBetweenSites(t *testing.T) {
	// This is the random draw itself rather than a browser, so it can be sampled cheaply
	// enough to be conclusive.
	const draws = 200

	seen := map[string]int{}
	for i := 0; i < draws; i++ {
		seen[randomIdentity(140).UserAgent]++
	}

	if len(seen) < 4 {
		t.Errorf("only %d distinct identities in %d draws: %v", len(seen), draws, seen)
	}

	// A draw that is random but lopsided is still predictable. Every combination of the
	// three platforms and three versions should turn up.
	if want := len(platforms) * versionsBack; len(seen) != want {
		t.Errorf("drew %d of the %d possible identities: %v", len(seen), want, seen)
	}
}

// The version is read off the running Chromium so it does not rot into a giveaway.
func TestChromeMajorReadsTheVersion(t *testing.T) {
	cases := map[string]int{
		"HeadlessChrome/139.0.7258.5": 139,
		"Chrome/141.0.0.0":            141,
		"Chrome/99.0.1.2":             99,
		"":                            0,
		"Firefox/130.0":               0,
		"Chrome/notanumber.0":         0,
	}
	for product, want := range cases {
		if got := chromeMajor(product); got != want {
			t.Errorf("chromeMajor(%q) = %d, want %d", product, got, want)
		}
	}
}

// A Chromium too old to name, or one that would not answer, must not produce a version
// from the distant past — that is conspicuous in its own right.
func TestIdentityFallsBackToAPlausibleVersion(t *testing.T) {
	for _, current := range []int{0, -1, 3} {
		id := randomIdentity(current)
		if id.Major <= fallbackMajor-versionsBack {
			t.Errorf("randomIdentity(%d) claimed Chrome %d, too old to be believable",
				current, id.Major)
		}
	}

	// A real version is used as given, spread over the last few releases.
	for i := 0; i < 50; i++ {
		id := randomIdentity(200)
		if id.Major > 200 || id.Major <= 200-versionsBack {
			t.Errorf("randomIdentity(200) claimed Chrome %d, outside the last %d releases",
				id.Major, versionsBack)
		}
	}
}

func TestRandIndexStaysInRange(t *testing.T) {
	for _, n := range []int{0, 1, 2, 5} {
		for i := 0; i < 100; i++ {
			got := randIndex(n)
			if got < 0 || (n > 0 && got >= n) {
				t.Fatalf("randIndex(%d) = %d, out of range", n, got)
			}
		}
	}
}

// The request that follows a widget link must not announce the checker either.
func TestFollowedLinksAreAlsoDisguised(t *testing.T) {
	browser := testBrowser(t)
	rec := serveRecording(t, `<html><body>ok</body></html>`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if check := browser.FollowRingLink(ctx, rec.url+"/mine/next"); !check.OK {
		t.Fatalf("following the link failed: %s", check.Err)
	}

	agents := rec.userAgents()
	if len(agents) == 0 {
		t.Fatal("the link was never followed")
	}
	if strings.Contains(strings.ToLower(agents[0]), "webring") {
		t.Errorf("followed link announced the checker: %q", agents[0])
	}
	if !strings.Contains(agents[0], "Chrome/") {
		t.Errorf("followed link sent %q, not an ordinary browser string", agents[0])
	}
}

// valueOf pulls one query parameter out of a URL the page built.
func valueOf(t *testing.T, raw, key string) string {
	t.Helper()

	_, query, ok := strings.Cut(raw, "?")
	if !ok {
		t.Fatalf("no query in %q", raw)
	}
	for _, pair := range strings.Split(query, "&") {
		k, v, found := strings.Cut(pair, "=")
		if found && k == key {
			decoded, err := url.QueryUnescape(v)
			if err != nil {
				t.Fatalf("decoding %q: %v", v, err)
			}
			return decoded
		}
	}
	t.Fatalf("no %q in %q", key, raw)
	return ""
}

// majorIn reads the Chrome major version out of a User-Agent string.
func majorIn(ua string) string {
	_, after, ok := strings.Cut(ua, "Chrome/")
	if !ok {
		return ""
	}
	major, _, _ := strings.Cut(after, ".")
	return major
}
