package health

import (
	"strings"
	"testing"
	"time"
)

// ring is a three-member ring: the site under test sits between prev and next, with two
// unrelated members available to play the part of stale links.
func ring() RingContext {
	return RingContext{
		Slugs: map[string]bool{
			"mine": true, "prev": true, "next": true,
			"old": true, "other": true, "someoneelse": true,
		},
		Site: Member{ID: 1, Slug: "mine", Name: "Mine", URL: "https://mine.example"},
		Prev: Member{ID: 2, Slug: "prev", Name: "Prev Site", URL: "https://prev.example"},
		Next: Member{ID: 3, Slug: "next", Name: "Next Site", URL: "https://next.example"},
		Others: []Member{
			{ID: 4, Slug: "old", Name: "Old Neighbor", URL: "https://old.example"},
			{ID: 5, Slug: "other", Name: "Other Member", URL: "https://other.example"},
		},
		Now: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC),
	}
}

// firstScreen puts a link in the opening screen at every size.
func firstScreen() map[Viewport]int {
	out := map[Viewport]int{}
	for _, v := range Viewports {
		out[v.Viewport] = 0
	}
	return out
}

// buried puts a link the given number of screens down at every size.
func buriedBy(screens int) map[Viewport]int {
	out := map[Viewport]int{}
	for _, v := range Viewports {
		out[v.Viewport] = screens
	}
	return out
}

// link is a plain, visible, server-rendered anchor with no text or pictures.
func link(href string) Link {
	return Link{Href: href, Visible: true, Screens: firstScreen(), TapSize: 40, InRawHTML: true}
}

// seen builds an observation from bare links: they work, but say nothing about where they
// lead. This is a functioning widget, not a good one.
func seen(hrefs ...string) PageObservation {
	obs := PageObservation{Rendered: true, FinalURL: "https://mine.example/"}
	for _, h := range hrefs {
		obs.Links = append(obs.Links, link(h))
	}
	return obs
}

// perfect is the widget a member has to write to score 100: both ways round the ring
// through the ring's own endpoints, naming its neighbors, showing their icons, linking
// the ring itself, in the opening screen at every size, and present without scripts.
func perfect() PageObservation {
	obs := PageObservation{Rendered: true, FinalURL: "https://mine.example/"}

	prev := link("https://otor.ing/mine/prev")
	prev.Text = "Prev Site"
	prev.Images = []string{"https://otor.ing/media/prev.png"}

	next := link("https://otor.ing/mine/next")
	next.Text = "Next Site"
	next.Images = []string{"https://otor.ing/media/next.png"}

	obs.Links = []Link{prev, next, link("https://otor.ing/")}
	return obs
}

func codes(r Report) []Code {
	out := make([]Code, 0, len(r.Findings))
	for _, f := range r.Findings {
		out = append(out, f.Code)
	}
	return out
}

func assertCodes(t *testing.T, r Report, want ...Code) {
	t.Helper()
	got := codes(r)
	if len(got) != len(want) {
		t.Fatalf("findings = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("finding[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func assertScore(t *testing.T, r Report, want int) {
	t.Helper()
	if r.Score != want {
		t.Errorf("score = %d, want %d (findings: %v)", r.Score, want, codes(r))
	}
}

// finding returns the finding with a given code, failing when it is absent.
func finding(t *testing.T, r Report, code Code) Finding {
	t.Helper()
	for _, f := range r.Findings {
		if f.Code == code {
			return f
		}
	}
	t.Fatalf("no %q finding in %v", code, codes(r))
	return Finding{}
}

func TestOnlyAThoroughWidgetScoresPerfect(t *testing.T) {
	r := Analyze(perfect(), ring())

	assertCodes(t, r)
	assertScore(t, r, 100)
	if r.Tier != "S" {
		t.Errorf("tier = %q, want S", r.Tier)
	}
}

// The old bar — two working links — is no longer enough. Bare arrays of arrows still work
// for a visitor, so they are not faults, but they leave the widget short of perfect.
func TestBareWorkingLinksFallShortOfPerfect(t *testing.T) {
	r := Analyze(seen("https://otor.ing/mine/prev", "https://otor.ing/mine/next"), ring())

	assertCodes(t, r, CodeNoNeighborName, CodeNoNeighborIcon, CodeNoRingLink)
	assertScore(t, r, 100-10-8-5)
	if r.Tier != "B" {
		t.Errorf("tier = %q, want B", r.Tier)
	}
}

// Linking the current neighbors directly is not a fault. A widget that resolves who its
// neighbors are — server-side or in the browser — is the better kind, and one frozen on
// an old neighbor is caught by stale_neighbors instead.
func TestDirectLinksToCurrentNeighborsAreNotAFault(t *testing.T) {
	r := Analyze(seen("https://prev.example", "https://next.example"), ring())

	assertCodes(t, r, CodeNoNeighborName, CodeNoNeighborIcon, CodeNoRingLink)
	assertScore(t, r, 100-10-8-5)
}

// The widget the ring's own author writes: neighbors resolved and named, their icons
// shown, a link back to the listing, and no ring endpoints anywhere in sight.
func TestResolvedNeighborWidgetScoresPerfect(t *testing.T) {
	ctx := ring()
	ctx.RingHosts = map[string]bool{"otor.ing": true}

	prev := link("https://prev.example")
	prev.Text = "← Prev Site"
	prev.Images = []string{"https://mine.example/icons/prev.png"}

	next := link("https://next.example")
	next.Text = "Next Site →"
	next.Images = []string{"https://mine.example/icons/next.png"}

	obs := PageObservation{Rendered: true, FinalURL: "https://mine.example/"}
	obs.Links = []Link{prev, next, link("https://otor.ing/")}

	r := Analyze(obs, ctx)
	assertCodes(t, r)
	assertScore(t, r, 100)
}

func TestNoWidgetAtAll(t *testing.T) {
	r := Analyze(seen("https://unrelated.example", "https://mine.example/about"), ring())

	assertCodes(t, r, CodeNoWidget)
	assertScore(t, r, 40)
}

func TestPageWithNoLinksAtAll(t *testing.T) {
	r := Analyze(PageObservation{Rendered: true, FinalURL: "https://mine.example/"}, ring())
	assertCodes(t, r, CodeNoWidget)
}

// A widget built in script with click handlers instead of links leaves nothing to find.
// Saying so beats reporting a bare "no widget" on a page that plainly has one.
func TestWidgetBuiltEntirelyInScript(t *testing.T) {
	obs := PageObservation{
		Rendered: true, FinalURL: "https://mine.example/",
		WidgetMarkers: []string{"WEBRING_LEFT", "WEBRING_RIGHT"},
	}
	r := Analyze(obs, ring())

	assertCodes(t, r, CodeJSOnly, CodeNoWidget)
	if !strings.Contains(finding(t, r, CodeJSOnly).Detail, "WEBRING_LEFT") {
		t.Errorf("detail does not name the markers: %q", finding(t, r, CodeJSOnly).Detail)
	}
}

// Links a script adds after the fact are invisible to a reader with scripts off.
func TestLinksAddedByScriptAreFlagged(t *testing.T) {
	obs := perfect()
	for i := range obs.Links {
		obs.Links[i].InRawHTML = false
	}

	r := Analyze(obs, ring())
	assertCodes(t, r, CodeJSOnly)
	assertScore(t, r, 100-18)
}

func TestCopiedWidgetPointsAtAnotherMember(t *testing.T) {
	r := Analyze(seen("https://otor.ing/someoneelse/next", "https://otor.ing/someoneelse/prev"), ring())

	if !r.Has(CodeWrongSlug) {
		t.Fatalf("no wrong_slug finding in %v", codes(r))
	}
	detail := finding(t, r, CodeWrongSlug).Detail
	if !strings.Contains(detail, "someoneelse") || !strings.Contains(detail, "mine") {
		t.Errorf("detail = %q, want both slugs named", detail)
	}
}

func TestForeignRingLinkAlongsideOwnIsFine(t *testing.T) {
	r := Analyze(seen("https://otor.ing/mine/next", "https://otor.ing/someoneelse/next",
		"https://otor.ing/mine/prev"), ring())

	if r.Has(CodeWrongSlug) {
		t.Errorf("a stray foreign link was treated as a copied widget")
	}
}

// A ring that can only be walked forwards is half a ring.
func TestOneWayWidget(t *testing.T) {
	t.Run("forward only", func(t *testing.T) {
		r := Analyze(seen("https://otor.ing/mine/next"), ring())
		if !r.Has(CodeOneWay) {
			t.Fatalf("no one_way finding in %v", codes(r))
		}
		if !strings.Contains(finding(t, r, CodeOneWay).Detail, "previous") {
			t.Errorf("detail = %q, want it to name the missing direction",
				finding(t, r, CodeOneWay).Detail)
		}
	})

	t.Run("backward only", func(t *testing.T) {
		r := Analyze(seen("https://otor.ing/mine/prev"), ring())
		if !r.Has(CodeOneWay) {
			t.Fatalf("no one_way finding in %v", codes(r))
		}
	})

	t.Run("random counts as forward", func(t *testing.T) {
		r := Analyze(seen("https://otor.ing/mine/prev", "https://otor.ing/mine/random"), ring())
		if r.Has(CodeOneWay) {
			t.Errorf("random plus prev was still called one-way")
		}
	})
}

func TestStaleNeighborLinksWithNoWayOnward(t *testing.T) {
	r := Analyze(seen("https://old.example", "https://other.example"), ring())

	if !r.Has(CodeStaleNeighbors) {
		t.Fatalf("no stale_neighbors finding in %v", codes(r))
	}
	if !strings.Contains(finding(t, r, CodeStaleNeighbors).Detail, "Old Neighbor") {
		t.Errorf("detail does not name the stale neighbor")
	}
}

func TestLinksToOtherMembersAreFineWhenTheWidgetWorks(t *testing.T) {
	r := Analyze(seen("https://otor.ing/mine/next", "https://otor.ing/mine/prev",
		"https://old.example", "https://other.example"), ring())

	if r.Has(CodeStaleNeighbors) {
		t.Errorf("a blogroll was treated as a stale widget")
	}
}

func TestManyMemberLinksReadAsABlogrollNotAStaleWidget(t *testing.T) {
	ctx := ring()
	ctx.Others = []Member{
		{ID: 4, Name: "A", URL: "https://a.example"},
		{ID: 5, Name: "B", URL: "https://b.example"},
		{ID: 6, Name: "C", URL: "https://c.example"},
		{ID: 7, Name: "D", URL: "https://d.example"},
	}

	r := Analyze(seen("https://a.example", "https://b.example",
		"https://c.example", "https://d.example"), ctx)
	assertCodes(t, r, CodeNoWidget)
}

func TestHiddenWidgetStopsTheOtherChecks(t *testing.T) {
	obs := seen("https://otor.ing/mine/prev", "https://otor.ing/mine/next")
	for i := range obs.Links {
		obs.Links[i].Visible = false
	}

	r := Analyze(obs, ring())
	assertCodes(t, r, CodeHidden)
	assertScore(t, r, 100-35)
}

// Burial is charged per screen and weighted per size: a widget lost on a desktop costs
// more than one lost on a phone, where scrolling is expected anyway.
func TestScrollPenaltyGrowsWithDistance(t *testing.T) {
	oneScreen := perfect()
	for i := range oneScreen.Links {
		oneScreen.Links[i].Screens = buriedBy(1)
	}

	twoScreens := perfect()
	for i := range twoScreens.Links {
		twoScreens.Links[i].Screens = buriedBy(2)
	}

	near := Analyze(oneScreen, ring())
	far := Analyze(twoScreens, ring())

	assertCodes(t, near, CodeBelowFold)
	assertCodes(t, far, CodeBelowFold)
	if far.Score >= near.Score {
		t.Errorf("two screens down scored %d, no worse than one screen down at %d",
			far.Score, near.Score)
	}
	if !strings.Contains(finding(t, near, CodeBelowFold).Detail, "1 screen on desktop") {
		t.Errorf("detail = %q, want it to say how far", finding(t, near, CodeBelowFold).Detail)
	}
}

func TestDesktopBurialCostsMoreThanMobile(t *testing.T) {
	desktopOnly := perfect()
	mobileOnly := perfect()
	for i := range desktopOnly.Links {
		desktopOnly.Links[i].Screens = map[Viewport]int{
			ViewportDesktop: 1, ViewportWide: 0, ViewportTablet: 0, ViewportMobile: 0,
		}
		mobileOnly.Links[i].Screens = map[Viewport]int{
			ViewportDesktop: 0, ViewportWide: 0, ViewportTablet: 0, ViewportMobile: 1,
		}
	}

	desktop := Analyze(desktopOnly, ring())
	mobile := Analyze(mobileOnly, ring())

	if desktop.Score >= mobile.Score {
		t.Errorf("burial on desktop scored %d, mobile %d — desktop must cost more",
			desktop.Score, mobile.Score)
	}
}

func TestScrollPenaltyIsCapped(t *testing.T) {
	obs := perfect()
	for i := range obs.Links {
		obs.Links[i].Screens = buriedBy(50)
	}

	r := Analyze(obs, ring())
	if cost := finding(t, r, CodeBelowFold).Cost; cost != maxScrollPenalty {
		t.Errorf("cost = %d, want it capped at %d", cost, maxScrollPenalty)
	}
}

// One reachable link is enough for a reader, so the widget is judged by its best part.
func TestOneReachableLinkIsEnough(t *testing.T) {
	obs := perfect()
	obs.Links[0].Screens = buriedBy(4)

	r := Analyze(obs, ring())
	if r.Has(CodeBelowFold) {
		t.Errorf("a widget with one link in the opening screen was called buried")
	}
}

func TestNeighborNamesAndIcons(t *testing.T) {
	t.Run("named by host", func(t *testing.T) {
		obs := perfect()
		obs.Links[0].Text = "prev.example"
		obs.Links[1].Text = "next.example"
		if r := Analyze(obs, ring()); r.Has(CodeNoNeighborName) {
			t.Errorf("naming a neighbor by host did not count")
		}
	})

	t.Run("no names", func(t *testing.T) {
		obs := perfect()
		for i := range obs.Links {
			obs.Links[i].Text = "<-"
		}
		if r := Analyze(obs, ring()); !r.Has(CodeNoNeighborName) {
			t.Errorf("bare arrows were treated as naming the neighbors")
		}
	})

	t.Run("no icons", func(t *testing.T) {
		obs := perfect()
		for i := range obs.Links {
			obs.Links[i].Images = nil
		}
		if r := Analyze(obs, ring()); !r.Has(CodeNoNeighborIcon) {
			t.Errorf("a widget with no pictures was not flagged")
		}
	})
}

func TestRingHomeLink(t *testing.T) {
	obs := perfect()
	obs.Links = obs.Links[:2] // drop the link to the ring itself

	r := Analyze(obs, ring())
	if !r.Has(CodeNoRingLink) {
		t.Errorf("no no_ring_link finding in %v", codes(r))
	}
}

// A member whose widget links its neighbors directly never names the ring, so its link
// back to the listing is only recognizable once the sweep has learned where the ring
// answers.
func TestRingHomeIsRecognizedFromTheSweepsKnowledge(t *testing.T) {
	obs := PageObservation{Rendered: true, FinalURL: "https://mine.example/", Links: []Link{
		link("https://prev.example"), link("https://next.example"),
		link("https://webring.otomir23.me/"),
	}}

	t.Run("without it the link is invisible", func(t *testing.T) {
		if r := Analyze(obs, ring()); !r.Has(CodeNoRingLink) {
			t.Errorf("the ring host was recognized with nothing to recognize it by")
		}
	})

	t.Run("with it the link counts", func(t *testing.T) {
		ctx := ring()
		ctx.RingHosts = map[string]bool{"webring.otomir23.me": true}
		if r := Analyze(obs, ctx); r.Has(CodeNoRingLink) {
			t.Errorf("a link to a known ring host was not counted: %v", codes(r))
		}
	})
}

func TestTinyTapTargets(t *testing.T) {
	obs := perfect()
	for i := range obs.Links {
		obs.Links[i].TapSize = 9
	}

	r := Analyze(obs, ring())
	if !r.Has(CodeTinyTarget) {
		t.Fatalf("no tiny_target finding in %v", codes(r))
	}
	if !strings.Contains(finding(t, r, CodeTinyTarget).Detail, "9px") {
		t.Errorf("detail = %q, want the measured size", finding(t, r, CodeTinyTarget).Detail)
	}
}

// A ring endpoint resolves through the ring, so wherever it arrives is current by
// definition. Landing somewhere unexpected says nothing about the member's widget.
func TestFollowedLinkLandingElsewhereIsNotAFault(t *testing.T) {
	ctx := ring()
	ctx.Followed = map[string]NeighborCheck{
		"https://otor.ing/mine/next": {Reached: "somewhere.example", OK: true},
	}

	if r := Analyze(perfect(), ctx); r.Has(CodeBrokenLink) {
		t.Errorf("a working endpoint was called broken just for resolving elsewhere")
	}
}

func TestFollowedLinkThatCannotBeReached(t *testing.T) {
	ctx := ring()
	ctx.Followed = map[string]NeighborCheck{
		"https://otor.ing/mine/next": {Err: "answers 404", OK: false},
	}

	r := Analyze(perfect(), ctx)
	if !strings.Contains(finding(t, r, CodeBrokenLink).Detail, "404") {
		t.Errorf("detail does not carry the error")
	}
}

func TestFollowedLinksThatWorkAreNotReported(t *testing.T) {
	ctx := ring()
	ctx.Followed = map[string]NeighborCheck{
		"https://otor.ing/mine/next": {Reached: "next.example", OK: true},
		"https://otor.ing/mine/prev": {Reached: "prev.example", OK: true},
	}

	if r := Analyze(perfect(), ctx); r.Has(CodeBrokenLink) {
		t.Errorf("working links were reported as broken")
	}
}

func TestSlowPageIsFlagged(t *testing.T) {
	obs := perfect()
	obs.TimeToRender = 20 * time.Second

	r := Analyze(obs, ring())
	if !r.Has(CodeSlowRender) {
		t.Fatalf("no slow_render finding in %v", codes(r))
	}
	if !strings.Contains(finding(t, r, CodeSlowRender).Detail, "20s") {
		t.Errorf("detail does not carry the measured time")
	}
}

func TestRenderFailureStopsEverythingElse(t *testing.T) {
	r := Analyze(PageObservation{Rendered: false, RenderError: "net::ERR_CONNECTION_REFUSED"}, ring())

	assertCodes(t, r, CodeRenderFailed)
	assertScore(t, r, 0)
	if r.Tier != "F" {
		t.Errorf("tier = %q, want F", r.Tier)
	}
}

func TestDownSiteSkipsTheRest(t *testing.T) {
	ctx := ring()
	ctx.Down = true

	r := Analyze(perfect(), ctx)
	assertCodes(t, r, CodeSiteDown)
	assertScore(t, r, 0)
	if r.Rendered {
		t.Errorf("Rendered = true for a site that is down")
	}
}

func TestRedirectedToAnotherHost(t *testing.T) {
	obs := perfect()
	obs.FinalURL = "https://parked.example/for-sale"

	r := Analyze(obs, ring())
	assertCodes(t, r, CodeRedirected)
	assertScore(t, r, 90)
}

func TestSameHostRedirectIsNotAFinding(t *testing.T) {
	for _, final := range []string{
		"https://www.mine.example/", "https://mine.example/home", "http://mine.example",
	} {
		t.Run(final, func(t *testing.T) {
			obs := perfect()
			obs.FinalURL = final
			assertCodes(t, Analyze(obs, ring()))
		})
	}
}

func TestScoreNeverGoesNegative(t *testing.T) {
	findings := []Finding{
		{Code: CodeNoWidget, Cost: 60}, {Code: CodeWrongSlug, Cost: 40},
		{Code: CodeHidden, Cost: 35}, {Code: CodeStaleNeighbors, Cost: 25},
	}
	if got := scoreFor(findings); got != 0 {
		t.Errorf("scoreFor(everything) = %d, want 0", got)
	}
}

// S is reserved for a widget with nothing wrong with it at all.
func TestTierBoundaries(t *testing.T) {
	cases := []struct {
		score int
		want  string
	}{
		{100, "S"}, {99, "A"}, {88, "A"}, {87, "B"}, {72, "B"},
		{71, "C"}, {55, "C"}, {54, "D"}, {30, "D"}, {29, "F"}, {0, "F"},
	}
	for _, tc := range cases {
		if got := Tier(tc.score); got != tc.want {
			t.Errorf("Tier(%d) = %q, want %q", tc.score, got, tc.want)
		}
	}
}

func TestEveryCheckHasATitleAndSeverity(t *testing.T) {
	for _, code := range Checks {
		if code.Title() == string(code) {
			t.Errorf("%s has no readable title", code)
		}
		if code.Severity() == "" {
			t.Errorf("%s has no severity", code)
		}
	}
}

func TestNormalizeURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://example.com", "example.com"},
		{"https://example.com/", "example.com"},
		{"http://www.Example.com/", "example.com"},
		{"https://example.com/blog/", "example.com/blog"},
		{"  https://example.com  ", "example.com"},
		{"not a url", ""},
		{"/relative/path", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeURL(tc.in); got != tc.want {
			t.Errorf("normalizeURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRingLinkSlug(t *testing.T) {
	slugs := ring().Slugs

	cases := []struct {
		href         string
		wantSlug     string
		wantEndpoint string
		wantOK       bool
	}{
		{"https://otor.ing/mine/next", "mine", "next", true},
		{"https://otor.ing/mine/prev", "mine", "prev", true},
		{"https://otor.ing/mine/random", "mine", "random", true},
		// The ring answers on more than one host, so the domain carries no signal.
		{"https://otomir23.me/webring/mine/next", "mine", "next", true},
		{"https://webring.otomir23.me/mine/next", "mine", "next", true},
		{"https://www.otor.ing/mine/next", "mine", "next", true},
		// A bare slug has no endpoint to identify it by.
		{"https://otor.ing/mine", "", "", false},
		{"https://otor.ing/", "", "", false},
		// The second-to-last segment must be a member the ring knows.
		{"https://otor.ing/notamember/next", "", "", false},
		{"https://otor.ing/mine/data", "", "", false},
		{"/mine/next", "", "", false},
	}

	for _, tc := range cases {
		slug, endpoint, ok := ringLinkSlug(tc.href, slugs)
		if slug != tc.wantSlug || ok != tc.wantOK || (ok && endpoint != tc.wantEndpoint) {
			t.Errorf("ringLinkSlug(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.href, slug, endpoint, ok, tc.wantSlug, tc.wantEndpoint, tc.wantOK)
		}
	}
}

func TestSelfLinksAreIgnored(t *testing.T) {
	r := Analyze(seen("https://mine.example/", "https://mine.example/posts"), ring())
	assertCodes(t, r, CodeNoWidget)
}

func TestRingEndpointsListsOnlyThisSitesOwn(t *testing.T) {
	obs := seen(
		"https://otor.ing/mine/next",
		"https://otor.ing/mine/next", // a duplicate must not be followed twice
		"https://otor.ing/someoneelse/next",
		"https://prev.example",
	)

	got := RingEndpoints(obs, ring())
	if len(got) != 1 || got[0] != "https://otor.ing/mine/next" {
		t.Errorf("RingEndpoints = %v, want just this site's own endpoint", got)
	}
}

// A widget that resolves its neighbors live links to whoever is currently next, so the
// address alone cannot say which side of the ring a link covers. The label can.
func TestDirectionIsReadFromTheLabelWhenTheURLCannotSayIt(t *testing.T) {
	ctx := ring()
	// Neither link points at a member the ring currently calls a neighbor.
	prev := link("https://old.example")
	prev.Text = "< sanspie"
	next := link("https://other.example")
	next.Text = "polina4096 >"

	obs := PageObservation{Rendered: true, FinalURL: "https://mine.example/"}
	obs.Links = []Link{prev, next}

	if r := Analyze(obs, ctx); r.Has(CodeOneWay) {
		t.Errorf("a widget labeled both ways was called one-way: %v", codes(r))
	}
}

func TestDirectionLabelsCoverTheCommonForms(t *testing.T) {
	for _, pair := range [][2]string{
		{"← previous", "next →"},
		{"<- prev", "next ->"},
		{"« back", "forward »"},
		{"< sanspie", "polina4096 >"},
	} {
		t.Run(pair[0]+" / "+pair[1], func(t *testing.T) {
			prev := link("https://old.example")
			prev.Text = pair[0]
			next := link("https://other.example")
			next.Text = pair[1]

			obs := PageObservation{Rendered: true, FinalURL: "https://mine.example/"}
			obs.Links = []Link{prev, next}

			if r := Analyze(obs, ring()); r.Has(CodeOneWay) {
				t.Errorf("labels %q / %q were not read as two directions", pair[0], pair[1])
			}
		})
	}
}

// A widget that genuinely only goes one way is still reported.
func TestGenuinelyOneWayWidgetIsStillCaught(t *testing.T) {
	next := link("https://next.example")
	next.Text = "next site →"

	obs := PageObservation{Rendered: true, FinalURL: "https://mine.example/"}
	obs.Links = []Link{next}

	if r := Analyze(obs, ring()); !r.Has(CodeOneWay) {
		t.Errorf("a one-way widget was not caught: %v", codes(r))
	}
}

// A page whose widget is drawn in script can still carry a stray link to some member.
// Reporting the script is the useful answer; picking over the stray link is not.
func TestScriptBuiltWidgetIsNamedEvenWithAStrayMemberLink(t *testing.T) {
	obs := seen("https://old.example")
	obs.WidgetMarkers = []string{"WEBRING_LEFT", "WEBRING_RIGHT"}

	r := Analyze(obs, ring())
	if !r.Has(CodeJSOnly) {
		t.Errorf("no js_only finding in %v", codes(r))
	}
}

// The marker only speaks up when there is no way onward; a working widget beside one is
// not an accusation.
func TestWidgetMarkersAreQuietWhenTheWidgetWorks(t *testing.T) {
	obs := perfect()
	obs.WidgetMarkers = []string{"webring-box"}

	if r := Analyze(obs, ring()); r.Has(CodeJSOnly) {
		t.Errorf("a working widget was called script-only: %v", codes(r))
	}
}

// However poor a working widget is, it keeps the ring joined up. It must never rank below
// a member who never added one.
func TestAWorkingWidgetAlwaysOutranksNoWidget(t *testing.T) {
	none := Analyze(seen("https://unrelated.example"), ring())
	if !none.Has(CodeNoWidget) {
		t.Fatalf("setup: expected a no_widget finding, got %v", codes(none))
	}

	// A widget with everything wrong with it short of being unusable.
	obs := seen("https://otor.ing/mine/prev", "https://otor.ing/mine/next")
	for i := range obs.Links {
		obs.Links[i].Screens = buriedBy(9)
		obs.Links[i].TapSize = 6
		obs.Links[i].InRawHTML = false
	}
	obs.TimeToRender = 30 * time.Second

	poor := Analyze(obs, ring())
	if len(poor.Findings) < 5 {
		t.Fatalf("setup: expected a pile of findings, got %v", codes(poor))
	}
	if poor.Score <= none.Score {
		t.Errorf("a working widget scored %d, no better than having none at %d (%v)",
			poor.Score, none.Score, codes(poor))
	}
}
