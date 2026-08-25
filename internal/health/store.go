package health

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"time"
)

// Store reads the ring and persists verdicts.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// SiteReport pairs a member with its latest verdict, for the public pages. Report is nil
// when the site has not been checked yet.
type SiteReport struct {
	Member  Member
	Favicon *string
	Report  *Report
}

// Score is the site's score, or zero when it has never been checked.
func (s SiteReport) Score() int {
	if s.Report == nil {
		return 0
	}
	return s.Report.Score
}

// Checked reports whether a verdict exists yet.
func (s SiteReport) Checked() bool { return s.Report != nil }

// ringRow is one member as loaded from the database.
type ringRow struct {
	member Member
	isUp   bool
}

// loadRing returns every enabled member in ring order.
func (s *Store) loadRing(ctx context.Context) ([]ringRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, slug, name, url, is_up
		FROM sites
		WHERE enabled = true
		ORDER BY display_order
	`)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)

	var ring []ringRow
	for rows.Next() {
		var r ringRow
		if scanErr := rows.Scan(&r.member.ID, &r.member.Slug, &r.member.Name,
			&r.member.URL, &r.isUp); scanErr != nil {
			return nil, scanErr
		}
		ring = append(ring, r)
	}
	return ring, rows.Err()
}

// RingContexts builds the checking context for every enabled member.
//
// Neighbors are taken from the sites that are both enabled and up, wrapping around, which
// is exactly what the /{slug}/next and /prev endpoints serve. Judging a member against a
// different set of neighbors than the ring actually hands out would produce findings
// nobody could act on.
func (s *Store) RingContexts(ctx context.Context, now time.Time) ([]RingContext, error) {
	members, err := s.loadRing(ctx)
	if err != nil {
		return nil, err
	}

	var live []Member
	for _, r := range members {
		if r.isUp {
			live = append(live, r.member)
		}
	}

	// Every enabled member's slug, including the ones that are down: a widget pointing at
	// a temporarily unreachable member is still a widget.
	slugs := make(map[string]bool, len(members))
	for _, r := range members {
		slugs[r.member.Slug] = true
	}

	position := make(map[int]int, len(live))
	for i, m := range live {
		position[m.ID] = i
	}

	contexts := make([]RingContext, 0, len(members))
	for _, r := range members {
		rc := RingContext{
			Slugs: slugs,
			Site:  r.member,
			Down:  !r.isUp,
			Now:   now,
		}

		if i, ok := position[r.member.ID]; ok && len(live) > 1 {
			rc.Prev = live[(i-1+len(live))%len(live)]
			rc.Next = live[(i+1)%len(live)]
		}

		for _, other := range live {
			if other.ID != r.member.ID && other.ID != rc.Prev.ID && other.ID != rc.Next.ID {
				rc.Others = append(rc.Others, other)
			}
		}

		contexts = append(contexts, rc)
	}

	return contexts, nil
}

// Save writes a verdict, replacing any previous one for that site.
func (s *Store) Save(ctx context.Context, report Report) error {
	findings := report.Findings
	if findings == nil {
		findings = []Finding{}
	}
	encoded, err := json.Marshal(findings)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO site_health (site_id, score, tier, rendered, findings, checked_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (site_id) DO UPDATE SET
			score = EXCLUDED.score, tier = EXCLUDED.tier, rendered = EXCLUDED.rendered,
			findings = EXCLUDED.findings, checked_at = EXCLUDED.checked_at
	`, report.SiteID, report.Score, report.Tier, report.Rendered, encoded, report.CheckedAt)
	return err
}

// Load returns the stored verdict for a site, or nil when there is none.
func (s *Store) Load(ctx context.Context, siteID int) (*Report, error) {
	var report Report
	var findings []byte

	err := s.db.QueryRowContext(ctx, `
		SELECT site_id, score, tier, rendered, findings, checked_at
		FROM site_health WHERE site_id = $1
	`, siteID).Scan(&report.SiteID, &report.Score, &report.Tier,
		&report.Rendered, &findings, &report.CheckedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	if unmarshalErr := json.Unmarshal(findings, &report.Findings); unmarshalErr != nil {
		return nil, unmarshalErr
	}
	return &report, nil
}

// SiteReports lists every enabled member with its latest verdict, worst score first so
// the sites that need attention are at the top. Unchecked sites sort last.
func (s *Store) SiteReports(ctx context.Context) ([]SiteReport, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, s.slug, s.name, s.url, s.favicon,
		       h.score, h.tier, h.rendered, h.findings, h.checked_at
		FROM sites s
		LEFT JOIN site_health h ON h.site_id = s.id
		WHERE s.enabled = true
		ORDER BY h.score ASC NULLS LAST, s.display_order
	`)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)

	var out []SiteReport
	for rows.Next() {
		var sr SiteReport
		var score sql.NullInt64
		var tier sql.NullString
		var rendered sql.NullBool
		var findings []byte
		var checkedAt sql.NullTime

		if scanErr := rows.Scan(&sr.Member.ID, &sr.Member.Slug, &sr.Member.Name, &sr.Member.URL,
			&sr.Favicon, &score, &tier, &rendered, &findings, &checkedAt); scanErr != nil {
			return nil, scanErr
		}

		if score.Valid {
			report := Report{
				SiteID:    sr.Member.ID,
				Score:     int(score.Int64),
				Tier:      tier.String,
				Rendered:  rendered.Bool,
				CheckedAt: checkedAt.Time,
			}
			if len(findings) > 0 {
				if unmarshalErr := json.Unmarshal(findings, &report.Findings); unmarshalErr != nil {
					return nil, unmarshalErr
				}
			}
			sr.Report = &report
		}

		out = append(out, sr)
	}
	return out, rows.Err()
}

func closeRows(rows *sql.Rows) {
	if err := rows.Close(); err != nil {
		log.Printf("Error closing rows: %v", err)
	}
}
