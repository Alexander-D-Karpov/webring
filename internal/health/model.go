// Package health judges whether a member site is holding up its end of the webring.
//
// The work splits in two. Analyze is pure: it takes what a browser saw on a page plus
// what the ring knows about that site's neighbors, and returns findings and a score. The
// browser layer, the database and the worker loop all sit around it. That boundary is
// what makes the scoring rules testable without a browser or a network.
package health

import "time"

// Viewport is one screen size the page is measured at. A widget can be perfectly placed
// on a laptop and lost on a phone, so every size is measured separately.
type Viewport string

const (
	ViewportWide    Viewport = "wide"
	ViewportDesktop Viewport = "desktop"
	ViewportTablet  Viewport = "tablet"
	ViewportMobile  Viewport = "mobile"
)

// ViewportSize is the pixel size and how much a burial at that size counts against a
// site. Desktop weighs most: it is where a webring is actually browsed, and where there
// is no excuse for hiding the widget.
type ViewportSize struct {
	Viewport Viewport
	Width    int
	Height   int
	// Weight is the score cost of each screen the reader must scroll at this size.
	Weight int
	Label  string
}

// Viewports are measured in this order; the first is the one the page is navigated at.
var Viewports = []ViewportSize{
	{Viewport: ViewportDesktop, Width: 1280, Height: 720, Weight: 8, Label: "desktop"},
	{Viewport: ViewportWide, Width: 1920, Height: 1080, Weight: 5, Label: "wide"},
	{Viewport: ViewportTablet, Width: 768, Height: 1024, Weight: 3, Label: "tablet"},
	{Viewport: ViewportMobile, Width: 375, Height: 667, Weight: 2, Label: "mobile"},
}

// maxScrollPenalty caps what burial can cost. Past a few screens the reader is equally
// lost either way, and leaving room means the quality checks still separate sites.
const maxScrollPenalty = 34

// Severity ranks how badly a finding breaks the ring.
type Severity string

const (
	// SeverityCritical means the site is not participating in the ring at all.
	SeverityCritical Severity = "critical"
	// SeverityMajor means the ring links exist but do not work as intended.
	SeverityMajor Severity = "major"
	// SeverityMinor means the links work but the widget is poorer than it should be.
	SeverityMinor Severity = "minor"
)

// Code identifies a kind of problem. These values are stored in site_health.findings, so
// they are part of the persisted data.
type Code string

const (
	// CodeSiteDown means the uptime checker already had the site marked down, so no
	// browser was launched.
	CodeSiteDown Code = "site_down"
	// CodeRenderFailed means Chromium could not produce a usable page.
	CodeRenderFailed Code = "render_failed"
	// CodeNoWidget means the page carries no ring link and no neighbor link.
	CodeNoWidget Code = "no_widget"
	// CodeWrongSlug means the widget points at another member's ring endpoints, which
	// happens when someone copies a snippet without editing it.
	CodeWrongSlug Code = "wrong_slug"
	// CodeHidden means ring links are in the DOM but render invisible.
	CodeHidden Code = "hidden"
	// CodeStaleNeighbors means direct links point at members who have since stopped
	// being this site's neighbors.
	CodeStaleNeighbors Code = "stale_neighbors"
	// CodeBrokenLink means the widget's ring endpoint no longer answers.
	CodeBrokenLink Code = "broken_link"
	// CodeJSOnly means the widget exists only once scripts have run, so a reader with
	// scripts disabled — or a crawler — sees nothing.
	CodeJSOnly Code = "js_only"
	// CodeOneWay means the widget moves in one direction only.
	CodeOneWay Code = "one_way"
	// CodeBelowFold means the reader has to scroll to find the widget.
	CodeBelowFold Code = "below_fold"
	// CodeNoNeighborName means the widget says "next" rather than naming who is next.
	CodeNoNeighborName Code = "no_neighbor_name"
	// CodeNoRingLink means the widget never links to the ring itself, so a visitor
	// cannot find out what they have stumbled into.
	CodeNoRingLink Code = "no_ring_link"
	// CodeTinyTarget means the widget is too small to comfortably hit on a phone.
	CodeTinyTarget Code = "tiny_target"
	// CodeSlowRender means the page took long enough to appear that a visitor may leave
	// before the widget ever shows up.
	CodeSlowRender Code = "slow_render"
	// CodeRedirected means the site now answers on a different host than the one on
	// record.
	CodeRedirected Code = "redirected"
)

// penalties is what each finding costs. CodeBelowFold is absent: its cost depends on how
// far down the widget is and is computed per site.
var penalties = map[Code]int{
	CodeNoWidget:       60,
	CodeWrongSlug:      40,
	CodeHidden:         35,
	CodeStaleNeighbors: 25,
	CodeBrokenLink:     22,
	CodeJSOnly:         18,
	CodeOneWay:         14,
	CodeNoNeighborName: 10,
	CodeRedirected:     10,
	CodeTinyTarget:     6,
	CodeNoRingLink:     5,
	CodeSlowRender:     5,
}

// severities maps each code to its rank.
var severities = map[Code]Severity{
	CodeSiteDown:       SeverityCritical,
	CodeRenderFailed:   SeverityCritical,
	CodeNoWidget:       SeverityCritical,
	CodeWrongSlug:      SeverityMajor,
	CodeHidden:         SeverityMajor,
	CodeStaleNeighbors: SeverityMajor,
	CodeBrokenLink:     SeverityMajor,
	CodeJSOnly:         SeverityMajor,
	CodeOneWay:         SeverityMajor,
	CodeBelowFold:      SeverityMinor,
	CodeNoNeighborName: SeverityMinor,
	CodeNoRingLink:     SeverityMinor,
	CodeTinyTarget:     SeverityMinor,
	CodeSlowRender:     SeverityMinor,
	CodeRedirected:     SeverityMinor,
}

// titles are the human-readable names shown on the public pages.
var titles = map[Code]string{
	CodeSiteDown:       "Site is down",
	CodeRenderFailed:   "Page did not render",
	CodeNoWidget:       "No webring widget",
	CodeWrongSlug:      "Widget points at another member",
	CodeHidden:         "Ring links are invisible",
	CodeStaleNeighbors: "Neighbor links are out of date",
	CodeBrokenLink:     "Widget link is dead",
	CodeJSOnly:         "Widget only works with JavaScript",
	CodeOneWay:         "Ring can only be walked one way",
	CodeBelowFold:      "Ring links need scrolling",
	CodeNoNeighborName: "Widget does not name its neighbors",
	CodeNoRingLink:     "Widget does not link to the ring",
	CodeTinyTarget:     "Widget is hard to tap",
	CodeSlowRender:     "Page is slow to appear",
	CodeRedirected:     "Site moved to another host",
}

// advice tells a member what to actually do about a finding.
var advice = map[Code]string{
	CodeNoWidget:       "Add a widget linking to /{slug}/prev and /{slug}/next.",
	CodeWrongSlug:      "Replace the copied slug with your own.",
	CodeHidden:         "The links render with no visible box; check the CSS hiding them.",
	CodeStaleNeighbors: "Link through the ring instead of naming neighbors directly.",
	CodeBrokenLink: "The ring no longer answers for this slug; " +
		"check it is still the one you registered.",
	CodeJSOnly:         "Put the links in the HTML so they work with scripts off.",
	CodeOneWay:         "Add the missing direction so the ring can be walked both ways.",
	CodeBelowFold:      "Move the widget into the first screen.",
	CodeNoNeighborName: "Show who prev and next are, not just arrows.",
	CodeNoRingLink:     "Link the ring itself so visitors can find the other members.",
	CodeTinyTarget:     "Make the links big enough to tap on a phone.",
	CodeSlowRender:     "The page takes a while to appear; visitors may leave first.",
	CodeRedirected:     "Update the URL you registered with the ring.",
}

// Checks lists every check in the order the public pages show them.
var Checks = []Code{
	CodeSiteDown, CodeRenderFailed, CodeNoWidget, CodeWrongSlug, CodeHidden,
	CodeStaleNeighbors, CodeBrokenLink, CodeJSOnly, CodeOneWay, CodeBelowFold,
	CodeNoNeighborName, CodeNoRingLink,
	CodeTinyTarget, CodeSlowRender, CodeRedirected,
}

// Title returns the display name of a check.
func (c Code) Title() string {
	if t, ok := titles[c]; ok {
		return t
	}
	return string(c)
}

// Advice returns what to do about a check, empty when there is nothing to say.
func (c Code) Advice() string { return advice[c] }

// Severity returns how badly this code breaks the ring.
func (c Code) Severity() Severity {
	if s, ok := severities[c]; ok {
		return s
	}
	return SeverityMinor
}

// Finding is one problem found on a page.
type Finding struct {
	Code     Code     `json:"code"`
	Severity Severity `json:"severity"`
	// Detail names the specific thing that is wrong, such as which neighbor a stale
	// link still points at.
	Detail string `json:"detail,omitempty"`
	// Cost is what this finding took off the score.
	Cost int `json:"cost,omitempty"`
}

// Title returns the display name of the finding's check.
func (f Finding) Title() string { return f.Code.Title() }

// Advice returns what to do about the finding.
func (f Finding) Advice() string { return f.Code.Advice() }

// Report is the verdict on one site.
type Report struct {
	SiteID    int       `json:"site_id"`
	Score     int       `json:"score"`
	Tier      string    `json:"tier"`
	Rendered  bool      `json:"rendered"`
	Findings  []Finding `json:"findings"`
	CheckedAt time.Time `json:"checked_at"`
}

// Has reports whether the report contains a given finding.
func (r Report) Has(code Code) bool {
	for _, f := range r.Findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

// Tier grades a score on the S-F scale. A perfect widget is genuinely hard to build, so
// S is reserved for one.
func Tier(score int) string {
	switch {
	case score >= 100:
		return "S"
	case score >= 88:
		return "A"
	case score >= 72:
		return "B"
	case score >= 55:
		return "C"
	case score >= 30:
		return "D"
	default:
		return "F"
	}
}

// Tiers lists the grades from best to worst, for rendering the tier list.
var Tiers = []string{"S", "A", "B", "C", "D", "F"}

// workingWidgetFloor is the lowest a site with a usable widget can score.
//
// Without it the arithmetic can invert: a member whose widget works but is buried, unnamed
// and script-built loses more than the flat cost of having no widget at all, and ends up
// ranked below someone who never added one. However poor a working widget is, it keeps the
// ring joined up, and it has to score above nothing.
var workingWidgetFloor = 100 - penalties[CodeNoWidget] + 1

// scoreFor turns a set of findings into a 0-100 score. A critical finding that makes the
// remaining checks meaningless drops the score straight to zero: if the page never
// rendered, there is nothing to say about where its links sit.
func scoreFor(findings []Finding) int {
	hasWidget := true
	for _, f := range findings {
		if f.Code == CodeSiteDown || f.Code == CodeRenderFailed {
			return 0
		}
		if f.Code == CodeNoWidget {
			hasWidget = false
		}
	}

	score := 100
	for _, f := range findings {
		score -= f.Cost
	}

	if hasWidget && score < workingWidgetFloor {
		return workingWidgetFloor
	}
	if score < 0 {
		return 0
	}
	return score
}
