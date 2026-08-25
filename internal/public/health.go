package public

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"time"

	"webring/internal/health"
)

// healthPageData backs the /health page.
type healthPageData struct {
	Sites []health.SiteReport
	// CheckedAt reads as a plain sentence rather than a timestamp, because the exact
	// minute of a six-hourly sweep tells the reader nothing.
	CheckedAt string
	Checks    []health.Code
	Request   *http.Request
}

// tierRow is one grade band of the tier list.
type tierRow struct {
	Tier  string
	Sites []health.SiteReport
}

type tiersPageData struct {
	Rows []tierRow
	// Unchecked are members with no verdict yet; they have no tier to sit in.
	Unchecked []health.SiteReport
	CheckedAt string
	Request   *http.Request
}

// checkedAt describes when the sweep last ran, in words.
//
// The oldest verdict is the honest one to quote: it says how stale the worst of the page
// is rather than how fresh the best of it is.
func checkedAt(reports []health.SiteReport) string {
	var oldest time.Time
	for _, r := range reports {
		if r.Report == nil {
			continue
		}
		if oldest.IsZero() || r.Report.CheckedAt.Before(oldest) {
			oldest = r.Report.CheckedAt
		}
	}
	if oldest.IsZero() {
		return ""
	}
	return humanizeSince(time.Since(oldest))
}

// humanizeSince turns an age into the sort of phrase a person would use.
func humanizeSince(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour") + " ago"
	case d < 30*24*time.Hour:
		return plural(int(d.Hours()/24), "day") + " ago"
	default:
		return "over a month ago"
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func healthPageHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reports, err := health.NewStore(db).SiteReports(r.Context())
		if err != nil {
			log.Printf("Error loading ring health: %v", err)
			http.Error(w, "Error loading ring health", http.StatusInternalServerError)
			return
		}

		renderPublic(w, "health.html", healthPageData{
			Sites:     reports,
			CheckedAt: checkedAt(reports),
			Checks:    health.Checks,
			Request:   r,
		})
	}
}

func tiersPageHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reports, err := health.NewStore(db).SiteReports(r.Context())
		if err != nil {
			log.Printf("Error loading ring health: %v", err)
			http.Error(w, "Error loading ring health", http.StatusInternalServerError)
			return
		}

		data := tiersPageData{CheckedAt: checkedAt(reports), Request: r}

		byTier := map[string][]health.SiteReport{}
		for _, report := range reports {
			if report.Report == nil {
				data.Unchecked = append(data.Unchecked, report)
				continue
			}
			byTier[report.Report.Tier] = append(byTier[report.Report.Tier], report)
		}

		// An empty band is left out rather than rendered with a placeholder: a tier
		// nobody reached says nothing worth a row of its own.
		for _, tier := range health.Tiers {
			if members := byTier[tier]; len(members) > 0 {
				data.Rows = append(data.Rows, tierRow{Tier: tier, Sites: members})
			}
		}

		renderPublic(w, "tiers.html", data)
	}
}

// renderPublic executes a public template against the shared template set.
func renderPublic(w http.ResponseWriter, name string, data interface{}) {
	templatesMu.RLock()
	t := templates
	templatesMu.RUnlock()

	if t == nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if err := t.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("Error rendering %s: %v", name, err)
		http.Error(w, "Error rendering template", http.StatusInternalServerError)
	}
}
