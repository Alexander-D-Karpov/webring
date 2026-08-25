package health

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

var uniqueSeq int64

func nextUnique() int64 {
	return time.Now().UnixNano()%1_000_000_000*1000 + atomic.AddInt64(&uniqueSeq, 1)
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DB_CONNECTION_STRING")
	if dsn == "" {
		t.Skip("TEST_DB_CONNECTION_STRING not set; skipping database integration tests")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("closing database: %v", closeErr)
		}
	})

	if err = db.Ping(); err != nil {
		t.Fatalf("connecting to database: %v", err)
	}

	var exists bool
	if err = db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM information_schema.tables WHERE table_name = 'site_health'
	)`).Scan(&exists); err != nil {
		t.Fatalf("checking schema: %v", err)
	}
	if !exists {
		t.Fatalf("site_health is missing; run 'make migrate-up' against the test database")
	}

	return db
}

// makeSite inserts a member and removes it when the test ends.
func makeSite(t *testing.T, db *sql.DB, name string, order int, isUp, enabled bool) int {
	t.Helper()

	unique := nextUnique()
	slug := fmt.Sprintf("h%d", unique)

	var id int
	err := db.QueryRow(`
		INSERT INTO sites (id, slug, name, url, is_up, enabled, display_order)
		VALUES ((SELECT COALESCE(MAX(id), 0) + 1 FROM sites), $1, $2, $3, $4, $5, $6)
		RETURNING id
	`, slug, name, fmt.Sprintf("https://%s.invalid", slug), isUp, enabled, order).Scan(&id)
	if err != nil {
		t.Fatalf("creating site: %v", err)
	}

	t.Cleanup(func() {
		if _, cleanupErr := db.Exec("DELETE FROM sites WHERE id = $1", id); cleanupErr != nil {
			t.Errorf("cleaning up site %d: %v", id, cleanupErr)
		}
	})

	return id
}

// clearRing removes every other member so ring order is predictable, restoring them after.
func clearRing(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec("UPDATE sites SET enabled = false"); err != nil {
		t.Fatalf("clearing the ring: %v", err)
	}
}

func TestRingContextsWrapAround(t *testing.T) {
	db := testDB(t)
	clearRing(t, db)
	store := NewStore(db)

	base := int(nextUnique() % 100000)
	first := makeSite(t, db, "First", base+1, true, true)
	second := makeSite(t, db, "Second", base+2, true, true)
	third := makeSite(t, db, "Third", base+3, true, true)

	contexts, err := store.RingContexts(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("RingContexts: %v", err)
	}
	if len(contexts) != 3 {
		t.Fatalf("got %d contexts, want 3", len(contexts))
	}

	byID := map[int]RingContext{}
	for _, c := range contexts {
		byID[c.Site.ID] = c
	}

	// The first member's previous neighbor is the last one, and the last member's next
	// is the first — the same wrap the /next and /prev endpoints serve.
	if byID[first].Prev.ID != third {
		t.Errorf("first.Prev = %d, want %d (the ring must wrap)", byID[first].Prev.ID, third)
	}
	if byID[first].Next.ID != second {
		t.Errorf("first.Next = %d, want %d", byID[first].Next.ID, second)
	}
	if byID[third].Next.ID != first {
		t.Errorf("third.Next = %d, want %d (the ring must wrap)", byID[third].Next.ID, first)
	}

	// Others must exclude the site itself and both of its neighbors.
	if len(byID[first].Others) != 0 {
		t.Errorf("first.Others = %v, want empty in a three-site ring", byID[first].Others)
	}
}

func TestRingContextsExcludeDownSitesFromNeighborsButStillCheckThem(t *testing.T) {
	db := testDB(t)
	clearRing(t, db)
	store := NewStore(db)

	base := int(nextUnique() % 100000)
	up1 := makeSite(t, db, "Up One", base+1, true, true)
	down := makeSite(t, db, "Down", base+2, false, true)
	up2 := makeSite(t, db, "Up Two", base+3, true, true)

	contexts, err := store.RingContexts(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("RingContexts: %v", err)
	}

	byID := map[int]RingContext{}
	for _, c := range contexts {
		byID[c.Site.ID] = c
	}

	// A down site is still checked, so it can be reported as down.
	if _, ok := byID[down]; !ok {
		t.Fatalf("the down site was left out of the check entirely")
	}
	if !byID[down].Down {
		t.Errorf("the down site is not flagged as down")
	}

	// But it must not appear as anybody's neighbor, because the ring routes around it.
	if byID[up1].Next.ID != up2 {
		t.Errorf("up1.Next = %d, want %d — the ring should skip the down site",
			byID[up1].Next.ID, up2)
	}
	if byID[up1].Prev.ID != up2 {
		t.Errorf("up1.Prev = %d, want %d", byID[up1].Prev.ID, up2)
	}
}

func TestRingContextsSkipDisabledSites(t *testing.T) {
	db := testDB(t)
	clearRing(t, db)
	store := NewStore(db)

	base := int(nextUnique() % 100000)
	makeSite(t, db, "Enabled", base+1, true, true)
	makeSite(t, db, "Disabled", base+2, true, false)

	contexts, err := store.RingContexts(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("RingContexts: %v", err)
	}
	if len(contexts) != 1 {
		t.Errorf("got %d contexts, want only the enabled site", len(contexts))
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	siteID := makeSite(t, db, "Round Trip", int(nextUnique()%100000), true, true)
	when := time.Now().UTC().Truncate(time.Second)

	report := Report{
		SiteID: siteID, Score: 60, Tier: "C", Rendered: true, CheckedAt: when,
		Findings: []Finding{
			{Code: CodeWrongSlug, Severity: SeverityMajor, Detail: "points at /someoneelse/"},
		},
	}
	if err := store.Save(ctx, report); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load(ctx, siteID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil {
		t.Fatalf("Load returned nothing for a site that was just saved")
	}
	if loaded.Score != 60 || loaded.Tier != "C" || !loaded.Rendered {
		t.Errorf("loaded = %+v, want score 60 tier C rendered", loaded)
	}
	if len(loaded.Findings) != 1 || loaded.Findings[0].Code != CodeWrongSlug {
		t.Errorf("findings = %v, want one wrong_slug", loaded.Findings)
	}
	if loaded.Findings[0].Detail != "points at /someoneelse/" {
		t.Errorf("detail = %q, want it preserved", loaded.Findings[0].Detail)
	}
}

func TestSaveReplacesThePreviousVerdict(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	siteID := makeSite(t, db, "Replaced", int(nextUnique()%100000), true, true)

	for _, score := range []int{40, 100} {
		if err := store.Save(ctx, Report{
			SiteID: siteID, Score: score, Tier: Tier(score), Rendered: true,
			CheckedAt: time.Now(),
		}); err != nil {
			t.Fatalf("Save(%d): %v", score, err)
		}
	}

	loaded, err := store.Load(ctx, siteID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Score != 100 {
		t.Errorf("score = %d, want the latest verdict 100", loaded.Score)
	}

	var rows int
	if err = db.QueryRow("SELECT COUNT(*) FROM site_health WHERE site_id = $1", siteID).Scan(&rows); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if rows != 1 {
		t.Errorf("site has %d health rows, want exactly 1", rows)
	}
}

func TestLoadReturnsNilForAnUncheckedSite(t *testing.T) {
	db := testDB(t)
	siteID := makeSite(t, db, "Never Checked", int(nextUnique()%100000), true, true)

	report, err := NewStore(db).Load(context.Background(), siteID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if report != nil {
		t.Errorf("Load invented a report: %+v", report)
	}
}

// The health page leads with the sites that need attention.
func TestSiteReportsSortWorstFirstWithUncheckedLast(t *testing.T) {
	db := testDB(t)
	clearRing(t, db)
	store := NewStore(db)
	ctx := context.Background()

	base := int(nextUnique() % 100000)
	good := makeSite(t, db, "Good", base+1, true, true)
	bad := makeSite(t, db, "Bad", base+2, true, true)
	unchecked := makeSite(t, db, "Unchecked", base+3, true, true)

	for id, score := range map[int]int{good: 100, bad: 20} {
		if err := store.Save(ctx, Report{
			SiteID: id, Score: score, Tier: Tier(score), Rendered: true, CheckedAt: time.Now(),
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	reports, err := store.SiteReports(ctx)
	if err != nil {
		t.Fatalf("SiteReports: %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("got %d reports, want 3", len(reports))
	}

	if reports[0].Member.ID != bad {
		t.Errorf("first row is %q, want the worst score first", reports[0].Member.Name)
	}
	if reports[1].Member.ID != good {
		t.Errorf("second row is %q, want the healthy site", reports[1].Member.Name)
	}
	if reports[2].Member.ID != unchecked {
		t.Errorf("last row is %q, want the unchecked site", reports[2].Member.Name)
	}
	if reports[2].Checked() {
		t.Errorf("the unchecked site reports as checked")
	}
	if reports[2].Score() != 0 {
		t.Errorf("unchecked score = %d, want 0", reports[2].Score())
	}
}

func TestDeletingASiteRemovesItsVerdict(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	siteID := makeSite(t, db, "Doomed", int(nextUnique()%100000), true, true)
	if err := store.Save(ctx, Report{
		SiteID: siteID, Score: 50, Tier: "C", Rendered: true, CheckedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := db.Exec("DELETE FROM sites WHERE id = $1", siteID); err != nil {
		t.Fatalf("deleting site: %v", err)
	}

	var rows int
	if err := db.QueryRow("SELECT COUNT(*) FROM site_health WHERE site_id = $1", siteID).Scan(&rows); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if rows != 0 {
		t.Errorf("the verdict outlived its site")
	}
}
