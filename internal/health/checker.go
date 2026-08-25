package health

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"webring/internal/telegram"
)

const (
	defaultInterval = 6 * time.Hour
	// pauseBetweenSites keeps the checker from hammering the ring's members back to back.
	pauseBetweenSites = 2 * time.Second
)

// Config is how the checker is wired from the environment.
type Config struct {
	// Interval is how long to wait between full passes.
	Interval time.Duration
	// Notify enables Telegram messages when a site's verdict changes. Off by default.
	Notify bool
	// BrowserPath is the Chromium to drive.
	BrowserPath string
}

// LoadConfig reads the checker's settings, resolving the browser as it goes.
func LoadConfig() (Config, error) {
	cfg := Config{Interval: defaultInterval}

	if raw := os.Getenv("HEALTH_INTERVAL"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d >= time.Minute {
			cfg.Interval = d
		} else {
			log.Printf("Invalid HEALTH_INTERVAL, using %s", cfg.Interval)
		}
	}

	if raw := os.Getenv("HEALTH_NOTIFY"); raw != "" {
		if v, err := strconv.ParseBool(raw); err == nil {
			cfg.Notify = v
		}
	}

	path, err := FindBrowser()
	if err != nil {
		return cfg, err
	}
	cfg.BrowserPath = path

	return cfg, nil
}

// Checker walks the ring, judges each member, and stores the verdicts.
type Checker struct {
	store *Store
	db    *sql.DB
	cfg   Config
	// now is injectable so tests do not depend on the clock.
	now func() time.Time
}

func NewChecker(db *sql.DB, cfg Config) *Checker {
	return &Checker{store: NewStore(db), db: db, cfg: cfg, now: time.Now}
}

// Run checks every member on the configured interval until the context is canceled. The
// first pass starts immediately.
func (c *Checker) Run(ctx context.Context) {
	log.Printf("Ring integrity checker started: every %s, browser %s",
		c.cfg.Interval, c.cfg.BrowserPath)

	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	for {
		if err := c.RunOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("Ring integrity pass failed: %v", err)
		}

		select {
		case <-ctx.Done():
			log.Printf("Ring integrity checker stopped")
			return
		case <-ticker.C:
		}
	}
}

// RunOnce checks every member exactly once.
//
// The sweep runs in two passes: the first visits every site, the second judges them. The
// split exists because one thing cannot be learned from a single page — which hosts serve
// this ring. A member whose widget links its neighbors directly never mentions the ring,
// so its link back to the listing stays unrecognizable until other members' widgets have
// shown where the ring lives.
//
// Sites are visited one at a time in a single browser. Running them in parallel would
// finish sooner but each tab costs real memory, and this is a background job with hours
// of slack — there is nothing to buy with the concurrency.
func (c *Checker) RunOnce(ctx context.Context) error {
	contexts, err := c.store.RingContexts(ctx, c.now())
	if err != nil {
		return fmt.Errorf("loading the ring: %w", err)
	}
	if len(contexts) == 0 {
		log.Printf("Ring integrity: no enabled sites to check")
		return nil
	}

	browser, err := NewBrowser(ctx, c.cfg.BrowserPath)
	if err != nil {
		return fmt.Errorf("starting Chromium: %w", err)
	}
	defer browser.Close()

	start := c.now()

	seen := make([]visited, 0, len(contexts))
	ringHosts := map[string]bool{}

	for i := range contexts {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		ring := contexts[i]
		ring.Now = c.now()

		var obs PageObservation
		if !ring.Down {
			obs = browser.Visit(ctx, ring.Site.URL)
			ring.Followed = followRingLinks(ctx, browser, obs, ring)
			for host := range RingHostsIn(obs, ring.Slugs) {
				ringHosts[host] = true
			}
		}

		seen = append(seen, visited{ring: ring, obs: obs})

		if i < len(contexts)-1 {
			sleep(ctx, pauseBetweenSites)
		}
	}

	for i := range seen {
		seen[i].ring.RingHosts = ringHosts
		c.record(ctx, seen[i])
	}

	log.Printf("Ring integrity: checked %d sites in %s, ring answers on %d hosts",
		len(seen), c.now().Sub(start).Round(time.Second), len(ringHosts))
	return nil
}

// visited is one member's page, kept until the whole sweep has been seen.
type visited struct {
	ring RingContext
	obs  PageObservation
}

// record judges one visited page and stores the verdict.
func (c *Checker) record(ctx context.Context, v visited) {
	report := Analyze(v.obs, v.ring)

	previous, err := c.store.Load(ctx, v.ring.Site.ID)
	if err != nil {
		log.Printf("Ring integrity: cannot read the previous verdict for %s: %v",
			v.ring.Site.Slug, err)
	}

	if err := c.store.Save(ctx, report); err != nil {
		log.Printf("Ring integrity: cannot store the verdict for %s: %v", v.ring.Site.Slug, err)
		return
	}

	if changed(previous, report) {
		log.Printf("Ring integrity: %s %s (%d) %s", v.ring.Site.Slug, report.Tier, report.Score,
			summarize(report))
		c.notify(v.ring.Site, previous, report)
	}
}

// followRingLinks walks the widget's own ring endpoints to check they still answer.
func followRingLinks(ctx context.Context, browser *Browser,
	obs PageObservation, ring RingContext) map[string]NeighborCheck {
	endpoints := RingEndpoints(obs, ring)
	if len(endpoints) == 0 {
		return nil
	}

	results := make(map[string]NeighborCheck, len(endpoints))
	for _, href := range endpoints {
		results[href] = browser.FollowRingLink(ctx, href)
	}
	return results
}

// changed reports whether a verdict differs from the one before it in a way worth
// announcing. A score that moved but kept the same findings is still a change.
func changed(previous *Report, current Report) bool {
	if previous == nil {
		return true
	}
	if previous.Score != current.Score || len(previous.Findings) != len(current.Findings) {
		return true
	}
	for i := range current.Findings {
		if previous.Findings[i].Code != current.Findings[i].Code {
			return true
		}
	}
	return false
}

func summarize(report Report) string {
	if len(report.Findings) == 0 {
		return "no problems"
	}
	parts := make([]string, 0, len(report.Findings))
	for _, f := range report.Findings {
		parts = append(parts, string(f.Code))
	}
	return strings.Join(parts, ", ")
}

// notify tells the admins that a site's verdict changed. It is off unless HEALTH_NOTIFY
// is set, so turning the checker on does not start a flood of messages about a ring that
// has never been measured before.
func (c *Checker) notify(site Member, previous *Report, current Report) {
	if !c.cfg.Notify {
		return
	}

	token := telegram.BotToken()
	chatID := telegram.AdminChatID()
	if token == "" || chatID == 0 {
		return
	}

	was := "not checked before"
	if previous != nil {
		was = fmt.Sprintf("%s (%d)", previous.Tier, previous.Score)
	}

	text := telegram.EscapeMarkdownV2(fmt.Sprintf(
		"Ring check: %s is now %s (%d), was %s — %s",
		site.Name, current.Tier, current.Score, was, summarize(current)))

	telegram.SendMessage(token, chatID, text)
}

func sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
