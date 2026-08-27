package health

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Link is one anchor the browser found on a member's page.
type Link struct {
	// Href is already resolved against the page's final URL.
	Href string
	// Text is what the anchor reads as, including any title or alt text on it. It is
	// how the widget gets credit for naming its neighbors.
	Text string
	// Images are the sources of any pictures inside the anchor.
	Images []string
	// Visible is false when the anchor renders with no box, or is hidden by CSS.
	Visible bool
	// Screens is how many screens the reader must scroll at each viewport before the
	// link comes into view. Zero means it is already in the first screen; a missing
	// entry means it never renders at that size.
	Screens map[Viewport]int
	// TapSize is the smaller side of the link's box, in pixels, at the mobile viewport.
	TapSize float64
}

// aboveFold reports whether the link needs no scrolling at a viewport.
func (l Link) aboveFold(v Viewport) bool {
	screens, ok := l.Screens[v]
	return ok && screens == 0
}

// PageObservation is everything the browser layer reports about one page. Analyze works
// only from this, never from a live browser.
type PageObservation struct {
	// Rendered is false when navigation failed or the page came back empty.
	Rendered bool
	// RenderError explains a failed render.
	RenderError string
	// FinalURL is where the browser ended up after redirects.
	FinalURL string
	Links    []Link
	// NoScriptHrefs are the links still present when the page is rendered with scripting
	// switched off. Empty means the check could not be made, not that nothing survived.
	NoScriptHrefs []string
	// TimeToRender is how long the page took to become readable.
	TimeToRender time.Duration
	// WidgetMarkers are elements the page itself labeled as webring furniture, by id or
	// class. Some members build the widget in SVG with click handlers instead of
	// anchors; without this they would look like they had no widget at all.
	WidgetMarkers []string
}

// Member is a site in the ring, as far as the analyzer cares.
type Member struct {
	ID   int
	Slug string
	Name string
	URL  string
}

// NeighborCheck is the result of following a widget link to see where it lands.
type NeighborCheck struct {
	// Reached is the normalized URL the link ended at.
	Reached string
	// OK is false when the link did not arrive at the expected member.
	OK bool
	// Err explains a link that could not be followed at all.
	Err string
}

// RingContext is what the ring knows about the site being checked.
type RingContext struct {
	// Slugs is every member slug in the ring.
	//
	// Widget links are recognized by the shape of their path — a member slug followed by
	// next, prev or random — rather than by domain. The ring answers on more than one
	// host and is mounted under a path on at least one of them, so matching the domain
	// would miss whichever host a member happened to use.
	Slugs map[string]bool
	Site  Member
	Prev  Member
	Next  Member
	// Others are the remaining members, used to tell a stale neighbor link from a link
	// to some unrelated website.
	Others []Member
	// RingHosts are the hosts known to serve this ring, learned from the widgets seen
	// across the whole sweep. A member whose widget links its neighbors directly gives
	// no clue on its own page about where the ring lives, so the corpus supplies it.
	RingHosts map[string]bool
	// Followed holds the outcome of following each ring endpoint the widget uses.
	Followed map[string]NeighborCheck
	// Down short-circuits the whole check when the uptime checker already knows the
	// site is unreachable.
	Down bool
	// Now stamps the report.
	Now time.Time
}

// ringPaths are the endpoints a member is expected to link to.
var ringPaths = map[string]bool{"next": true, "prev": true, "random": true}

// maxWidgetLinks is how many ring links a widget plausibly shows: previous, next, and
// sometimes random. A page pointing at more former members than this is a blogroll of
// friends who happen to be in the ring, not a widget that fell out of date.
const maxWidgetLinks = 3

// minTapSize is the smallest comfortable touch target, in CSS pixels.
const minTapSize = 24

// slowRenderThreshold is when a page has taken long enough that a visitor may give up
// before the widget ever appears.
const slowRenderThreshold = 8 * time.Second

// normalizeURL reduces a URL to the part that identifies a site, so that
// https://www.Example.com/ and http://example.com compare equal.
func normalizeURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return ""
	}

	host := strings.ToLower(parsed.Host)
	host = strings.TrimPrefix(host, "www.")
	path := strings.TrimSuffix(parsed.Path, "/")

	return host + path
}

// ringLinkSlug returns the member slug a link addresses when it points at the ring's own
// endpoints, along with which endpoint it was.
//
// The match is on the tail of the path: a known member slug followed by next, prev or
// random. Nothing about the host or the prefix in front of it matters, which is what lets
// the same rule find a widget pointing at either of the ring's domains.
func ringLinkSlug(href string, slugs map[string]bool) (slug, endpoint string, ok bool) {
	if len(slugs) == 0 {
		return "", "", false
	}

	parsed, err := url.Parse(href)
	if err != nil || parsed.Host == "" {
		return "", "", false
	}

	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 {
		return "", "", false
	}

	endpoint = strings.ToLower(parts[len(parts)-1])
	slug = parts[len(parts)-2]
	if !ringPaths[endpoint] || !slugs[slug] {
		return "", "", false
	}

	return slug, endpoint, true
}

// isRingHome reports whether a link points at the ring's own front page rather than at
// any member. The ring is whichever host serves the endpoints, so this is recognized once
// the endpoints have shown which hosts those are.
func isRingHome(href string, ringHosts map[string]bool) bool {
	parsed, err := url.Parse(href)
	if err != nil || parsed.Host == "" {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Host), "www.")
	return ringHosts[host]
}

// classified holds the ring-related links found on a page, split by what they mean.
type classified struct {
	// ownRing are links to this site's own ring endpoints, by endpoint name.
	ownRing     []Link
	ownEndpoint map[string]bool
	// foreignRing are links to another member's ring endpoints.
	foreignRing []Link
	foreignSlug map[string]bool
	// neighbors are direct links to the current prev or next member.
	neighbors []Link
	// neighborSide records which direction each direct link covers.
	neighborSide map[string]bool
	// stale are direct links to members who are no longer neighbors.
	stale     []Link
	staleName map[string]bool
	// ringHome is a link to the ring's own front page.
	ringHome []Link
	// ringHosts are the hosts seen serving ring endpoints.
	ringHosts map[string]bool
}

func classify(obs PageObservation, ring RingContext) classified {
	out := classified{
		ownEndpoint:  map[string]bool{},
		foreignSlug:  map[string]bool{},
		neighborSide: map[string]bool{},
		staleName:    map[string]bool{},
		ringHosts:    map[string]bool{},
	}
	for host := range ring.RingHosts {
		out.ringHosts[host] = true
	}

	neighborURLs := map[string]string{}
	if n := normalizeURL(ring.Prev.URL); n != "" {
		neighborURLs[n] = "prev"
	}
	if n := normalizeURL(ring.Next.URL); n != "" {
		neighborURLs[n] = "next"
	}

	otherURLs := map[string]string{}
	for _, m := range ring.Others {
		if n := normalizeURL(m.URL); n != "" {
			if _, isNeighbor := neighborURLs[n]; !isNeighbor {
				otherURLs[n] = m.Name
			}
		}
	}

	// A site linking to itself is not a ring link; ignore it.
	self := normalizeURL(ring.Site.URL)

	var maybeHome []Link
	for _, link := range obs.Links {
		if slug, endpoint, ok := ringLinkSlug(link.Href, ring.Slugs); ok {
			if parsed, err := url.Parse(link.Href); err == nil {
				out.ringHosts[strings.TrimPrefix(strings.ToLower(parsed.Host), "www.")] = true
			}
			if slug == ring.Site.Slug {
				out.ownRing = append(out.ownRing, link)
				out.ownEndpoint[endpoint] = true
			} else {
				out.foreignRing = append(out.foreignRing, link)
				out.foreignSlug[slug] = true
			}
			continue
		}

		normalized := normalizeURL(link.Href)
		if normalized == "" || normalized == self {
			continue
		}
		if side, ok := neighborURLs[normalized]; ok {
			out.neighbors = append(out.neighbors, link)
			out.neighborSide[side] = true
			continue
		}
		if name, ok := otherURLs[normalized]; ok {
			out.stale = append(out.stale, link)
			out.staleName[name] = true
			continue
		}
		maybeHome = append(maybeHome, link)
	}

	// The ring's front page can only be recognized once the endpoints have revealed
	// which hosts are the ring's.
	for _, link := range maybeHome {
		if isRingHome(link.Href, out.ringHosts) {
			out.ringHome = append(out.ringHome, link)
		}
	}

	return out
}

// widgetLinks are every link that counts as part of the ring widget, whichever way the
// member wired it up.
func (c classified) widgetLinks() []Link {
	links := make([]Link, 0, len(c.ownRing)+len(c.foreignRing)+len(c.neighbors)+len(c.stale))
	links = append(links, c.ownRing...)
	links = append(links, c.foreignRing...)
	links = append(links, c.neighbors...)
	links = append(links, c.stale...)
	return links
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// analysis carries the state of one verdict while it is being assembled.
type analysis struct {
	report Report
	ring   RingContext
}

func (a *analysis) add(code Code, detail string) {
	a.addCost(code, detail, penalties[code])
}

// addCost records a problem, at most once. Several checks can notice the same fault from
// different angles — a page that builds its widget in script fails both the marker check
// and the scripts-off render — and a reader deserves one explanation, charged once,
// rather than the same complaint twice at double the cost. The first wording wins because
// checks run from the most specific to the most general.
func (a *analysis) addCost(code Code, detail string, cost int) {
	for _, existing := range a.report.Findings {
		if existing.Code == code {
			return
		}
	}
	a.report.Findings = append(a.report.Findings, Finding{
		Code:     code,
		Severity: code.Severity(),
		Detail:   detail,
		Cost:     cost,
	})
}

func (a *analysis) finish() Report {
	a.report.Score = scoreFor(a.report.Findings)
	a.report.Tier = Tier(a.report.Score)
	return a.report
}

// Analyze turns one page observation into a verdict.
func Analyze(obs PageObservation, ring RingContext) Report {
	a := &analysis{
		report: Report{SiteID: ring.Site.ID, Rendered: obs.Rendered, CheckedAt: ring.Now},
		ring:   ring,
	}

	// A site that is down, or a page that never rendered, tells us nothing about its
	// widget. Report just that and stop.
	switch {
	case ring.Down:
		a.report.Rendered = false
		a.add(CodeSiteDown, "the uptime checker cannot reach this site")
		return a.finish()
	case !obs.Rendered:
		detail := obs.RenderError
		if detail == "" {
			detail = "Chromium returned an empty page"
		}
		a.add(CodeRenderFailed, detail)
		return a.finish()
	}

	if host := hostOf(obs.FinalURL); host != "" {
		if expected := hostOf(ring.Site.URL); expected != "" && host != expected {
			a.add(CodeRedirected, fmt.Sprintf("now answers on %s", host))
		}
	}

	if obs.TimeToRender > slowRenderThreshold {
		a.add(CodeSlowRender, fmt.Sprintf("took %s to become readable",
			obs.TimeToRender.Round(time.Second)))
	}

	found := classify(obs, ring)
	widget := found.widgetLinks()

	// With no way onward, the only question is whether what is on the page looks like a
	// widget that has gone out of date or like no widget at all.
	if len(found.ownRing) == 0 && len(found.neighbors) == 0 && len(found.foreignRing) == 0 &&
		len(found.stale) > maxWidgetLinks {
		widget = nil
	}

	if len(widget) == 0 {
		a.noWidget(obs)
		return a.finish()
	}

	traversal := append(append([]Link{}, found.ownRing...), found.neighbors...)

	// A page that marks up a widget but offers no way onward built it in script. Saying
	// so is more useful than picking over whichever stray member link happens to remain.
	if len(traversal) == 0 && len(obs.WidgetMarkers) > 0 {
		a.add(CodeJSOnly, fmt.Sprintf(
			"the page marks up a widget (%s) but builds it with scripts and no links",
			strings.Join(obs.WidgetMarkers, ", ")))
	}

	a.checkSlug(found)
	a.checkStale(found, traversal)
	a.checkDirections(found)
	a.checkFollowed(found)

	if !a.checkVisibility(traversal, widget) {
		return a.finish()
	}

	a.checkPresentation(obs, found, traversal, widget)
	return a.finish()
}

// noWidget reports the absence of a widget, distinguishing a page that never had one from
// one whose widget only exists inside a script.
func (a *analysis) noWidget(obs PageObservation) {
	if len(obs.WidgetMarkers) > 0 {
		a.add(CodeJSOnly, fmt.Sprintf(
			"the page marks up a widget (%s) but builds it with scripts and no links",
			strings.Join(obs.WidgetMarkers, ", ")))
		a.add(CodeNoWidget, "nothing on the page links to the ring or to either neighbor")
		return
	}
	a.add(CodeNoWidget, "no link to the ring or to either neighbor")
}

func (a *analysis) checkSlug(found classified) {
	if len(found.foreignRing) > 0 && len(found.ownRing) == 0 {
		a.add(CodeWrongSlug, fmt.Sprintf("widget links to /%s/ instead of /%s/",
			strings.Join(sortedKeys(found.foreignSlug), ", /"), a.ring.Site.Slug))
	}
}

// checkStale reports links to former neighbors, but only when they are the only way
// onward. Plenty of members keep a blogroll of friends who happen to be in the ring.
func (a *analysis) checkStale(found classified, traversal []Link) {
	if len(traversal) == 0 && len(found.stale) > 0 {
		a.add(CodeStaleNeighbors, fmt.Sprintf("only links to %s, who are no longer neighbors",
			strings.Join(sortedKeys(found.staleName), ", ")))
	}
}

// backwardWords and forwardWords are how a widget labels its two directions. They are a
// second source of truth alongside the URLs: a widget that resolves its neighbors live
// links to whoever is currently next, and reading the label is the only way to know which
// side of the ring a link covers when the address alone does not say.
var backwardWords = []string{"prev", "previous", "back", "←", "<-", "<", "«"}
var forwardWords = []string{"next", "forward", "→", "->", ">", "»"}

func labelSuggests(links []Link, words []string) bool {
	for _, link := range links {
		text := strings.ToLower(link.Text)
		for _, word := range words {
			if strings.Contains(text, word) {
				return true
			}
		}
	}
	return false
}

// checkDirections reports a widget that only moves one way round the ring.
func (a *analysis) checkDirections(found classified) {
	sided := append(append([]Link{}, found.neighbors...), found.stale...)

	forward := found.ownEndpoint["next"] || found.ownEndpoint["random"] ||
		found.neighborSide["next"] || labelSuggests(sided, forwardWords)
	backward := found.ownEndpoint["prev"] || found.neighborSide["prev"] ||
		labelSuggests(sided, backwardWords)

	switch {
	case forward && backward:
	case forward:
		a.add(CodeOneWay, "there is a way to the next site but not the previous one")
	case backward:
		a.add(CodeOneWay, "there is a way to the previous site but not the next one")
	}
}

// checkFollowed reports widget links whose ring endpoint no longer answers.
func (a *analysis) checkFollowed(found classified) {
	if len(a.ring.Followed) == 0 {
		return
	}

	var broken []string
	for _, link := range found.ownRing {
		result, ok := a.ring.Followed[link.Href]
		if !ok || result.OK || result.Err == "" {
			continue
		}
		broken = append(broken, fmt.Sprintf("%s %s", link.Href, result.Err))
	}

	if len(broken) > 0 {
		sort.Strings(broken)
		a.add(CodeBrokenLink, strings.Join(broken, "; "))
	}
}

// checkVisibility reports a widget that cannot be seen, or that has to be scrolled to.
// It returns false when the widget is invisible, which makes the remaining checks moot.
func (a *analysis) checkVisibility(traversal, widget []Link) bool {
	judged := traversal
	if len(judged) == 0 {
		judged = widget
	}

	visible := filter(judged, func(l Link) bool { return l.Visible })
	if len(visible) == 0 {
		a.add(CodeHidden, "ring links are present but render invisible")
		return false
	}

	// The reader only needs one reachable link, so the widget is judged by its most
	// visible part at each size. Burial costs more on a desktop, where there is both
	// more room and less excuse for it.
	cost := 0
	var buried []string
	for _, size := range Viewports {
		best := -1
		for _, l := range visible {
			if screens, ok := l.Screens[size.Viewport]; ok && (best < 0 || screens < best) {
				best = screens
			}
		}
		if best <= 0 {
			continue
		}

		cost += best * size.Weight
		buried = append(buried, fmt.Sprintf("%d %s on %s", best, plural(best, "screen"), size.Label))
	}

	if cost > 0 {
		if cost > maxScrollPenalty {
			cost = maxScrollPenalty
		}
		a.addCost(CodeBelowFold, "the reader has to scroll "+strings.Join(buried, ", "), cost)
	}

	return true
}

// checkPresentation judges how much the widget tells the reader. A pair of bare arrows
// works, but it says nothing about where they lead.
func (a *analysis) checkPresentation(obs PageObservation, found classified, traversal, widget []Link) {
	judged := traversal
	if len(judged) == 0 {
		judged = widget
	}

	if !a.namesNeighbors(judged) {
		a.add(CodeNoNeighborName, "the links say next and previous without saying who they are")
	}

	if len(found.ringHome) == 0 {
		a.add(CodeNoRingLink, "nothing links back to the ring itself")
	}

	if a.needsScripts(obs) {
		a.add(CodeJSOnly, "with scripts off the page offers no way round the ring")
	}

	if smallest := smallestTapTarget(judged); smallest > 0 && smallest < minTapSize {
		a.add(CodeTinyTarget, fmt.Sprintf(
			"the links are %.0fpx across on a phone, under the %dpx a finger needs",
			smallest, minTapSize))
	}
}

// needsScripts reports whether the ring becomes unreachable with scripting off.
//
// The question is not whether the same links survive — a site may build one widget in
// script and ship another in a noscript fallback, and the two need not share a URL. What
// matters is that something still carries the reader onward.
//
// An empty result means the page could not be loaded that way at all, which is no
// evidence either way; the site keeps the benefit of the doubt.
func (a *analysis) needsScripts(obs PageObservation) bool {
	if len(obs.NoScriptHrefs) == 0 {
		return false
	}

	neighbors := map[string]bool{}
	for _, m := range []Member{a.ring.Prev, a.ring.Next} {
		if n := normalizeURL(m.URL); n != "" {
			neighbors[n] = true
		}
	}

	for _, href := range obs.NoScriptHrefs {
		if slug, _, ok := ringLinkSlug(href, a.ring.Slugs); ok && slug == a.ring.Site.Slug {
			return false
		}
		if neighbors[normalizeURL(href)] {
			return false
		}
	}
	return true
}

// namesNeighbors reports whether the widget says who its neighbors are, rather than just
// pointing at them.
func (a *analysis) namesNeighbors(links []Link) bool {
	wanted := []string{a.ring.Prev.Name, a.ring.Next.Name,
		hostOf(a.ring.Prev.URL), hostOf(a.ring.Next.URL)}

	for _, link := range links {
		text := strings.ToLower(link.Text)
		if text == "" {
			continue
		}
		for _, want := range wanted {
			if want == "" {
				continue
			}
			if strings.Contains(text, strings.ToLower(want)) {
				return true
			}
		}
	}
	return false
}

func smallestTapTarget(links []Link) float64 {
	smallest := 0.0
	for _, link := range links {
		if !link.Visible || link.TapSize <= 0 {
			continue
		}
		if smallest == 0 || link.TapSize < smallest {
			smallest = link.TapSize
		}
	}
	return smallest
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func filter(links []Link, keep func(Link) bool) []Link {
	var out []Link
	for _, l := range links {
		if keep(l) {
			out = append(out, l)
		}
	}
	return out
}

func hostOf(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(parsed.Host), "www.")
}

// RingHostsIn returns the hosts a page's widget uses to reach the ring. Gathering these
// across every member is how the checker learns where the ring answers without being
// told: the ring has several domains and gains more as people mirror it.
func RingHostsIn(obs PageObservation, slugs map[string]bool) map[string]bool {
	hosts := map[string]bool{}
	for _, link := range obs.Links {
		if _, _, ok := ringLinkSlug(link.Href, slugs); !ok {
			continue
		}
		if parsed, err := url.Parse(link.Href); err == nil && parsed.Host != "" {
			hosts[strings.TrimPrefix(strings.ToLower(parsed.Host), "www.")] = true
		}
	}
	return hosts
}

// RingEndpoints returns the ring links a widget uses for this site, so the caller can
// follow them and see where they actually land.
func RingEndpoints(obs PageObservation, ring RingContext) []string {
	seen := map[string]bool{}
	var out []string
	for _, link := range obs.Links {
		slug, _, ok := ringLinkSlug(link.Href, ring.Slugs)
		if !ok || slug != ring.Site.Slug || seen[link.Href] {
			continue
		}
		seen[link.Href] = true
		out = append(out, link.Href)
	}
	sort.Strings(out)
	return out
}
