package blacklist

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"webring/internal/models"

	_ "github.com/lib/pq"
)

var uniqueSeq int64

func nextUnique() int64 {
	return time.Now().UnixNano()%1_000_000_000*1000 + atomic.AddInt64(&uniqueSeq, 1)
}

// testDB connects to TEST_DB_CONNECTION_STRING, skipping when it is not configured. The
// schema is expected to be migrated already ("make migrate-up").
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
	return db
}

// uniqueSubject keeps every test's rows disjoint, since both tables are unique on
// (subject_type, subject_value) and the suite shares one database.
func uniqueSubject(t *testing.T, prefix string) string {
	t.Helper()

	subject := fmt.Sprintf("%s-%d", prefix, nextUnique())
	t.Cleanup(func() {
		db := testDB(t)
		if _, err := db.Exec("DELETE FROM blacklist WHERE subject_value = $1", subject); err != nil {
			t.Errorf("cleaning up blacklist row %s: %v", subject, err)
		}
		if _, err := db.Exec("DELETE FROM request_cooldowns WHERE subject_value = $1", subject); err != nil {
			t.Errorf("cleaning up request_cooldowns row %s: %v", subject, err)
		}
	})
	return subject
}

func createUser(t *testing.T, db *sql.DB) (userID int, username string) {
	t.Helper()

	telegramID := nextUnique()
	username = fmt.Sprintf("bltest_%d", telegramID)

	err := db.QueryRow(`
		INSERT INTO users (telegram_id, telegram_username, first_name, is_admin)
		VALUES ($1, $2, $3, false) RETURNING id
	`, telegramID, username, username).Scan(&userID)
	if err != nil {
		t.Fatalf("creating user: %v", err)
	}

	t.Cleanup(func() {
		if _, cleanupErr := db.Exec("DELETE FROM users WHERE id = $1", userID); cleanupErr != nil {
			t.Errorf("cleaning up user %d: %v", userID, cleanupErr)
		}
	})

	return userID, username
}

func TestAddNormalizesTheSubjectBeforeStoringIt(t *testing.T) {
	db := testDB(t)
	subject := uniqueSubject(t, "example.com")

	if err := Add(db, SubjectSite, "  HTTPS://WWW."+strings.ToUpper(subject)+"/path  ", "spam", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	entries, err := Entries(db, SubjectSite)
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}

	found := false
	for _, entry := range entries {
		if entry.Subject == subject {
			found = true
			if entry.Reason != "spam" {
				t.Errorf("Reason = %q, want spam", entry.Reason)
			}
			if entry.SubjectType != SubjectSite {
				t.Errorf("SubjectType = %q, want site", entry.SubjectType)
			}
		}
	}
	if !found {
		t.Fatalf("normalized subject %q not found in %v", subject, entries)
	}
}

func TestAddRejectsSubjectsItCannotIdentify(t *testing.T) {
	db := testDB(t)

	cases := map[string][2]string{
		"unknown type": {"group", "alice"},
		"empty value":  {SubjectUser, "   "},
		"bare at-sign": {SubjectUser, "@"},
		"empty site":   {SubjectSite, "  /  "},
	}

	for name, c := range cases {
		if err := Add(db, c[0], c[1], "", nil); err != ErrInvalidSubject {
			t.Errorf("%s: Add() error = %v, want ErrInvalidSubject", name, err)
		}
	}
}

func TestAddOverwritesAnExistingEntryInsteadOfFailing(t *testing.T) {
	db := testDB(t)
	subject := uniqueSubject(t, "dupe")

	if err := Add(db, SubjectUser, subject, "first", nil); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if err := Add(db, SubjectUser, subject, "second", nil); err != nil {
		t.Fatalf("second Add: %v", err)
	}

	entries, err := Entries(db, SubjectUser)
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}

	count, reason := 0, ""
	for _, entry := range entries {
		if entry.Subject == subject {
			count++
			reason = entry.Reason
		}
	}
	if count != 1 {
		t.Errorf("found %d rows for %q, want 1", count, subject)
	}
	if reason != "second" {
		t.Errorf("Reason = %q, want second", reason)
	}
}

func TestAddTruncatesAnOverlongReason(t *testing.T) {
	db := testDB(t)
	subject := uniqueSubject(t, "verbose")

	if err := Add(db, SubjectUser, subject, strings.Repeat("é", 500), nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	var reason string
	if err := db.QueryRow(
		"SELECT reason FROM blacklist WHERE subject_type = $1 AND subject_value = $2",
		SubjectUser, subject).Scan(&reason); err != nil {
		t.Fatalf("reading reason: %v", err)
	}

	if got := len([]rune(reason)); got != maxReasonRunes {
		t.Errorf("stored reason is %d runes, want %d", got, maxReasonRunes)
	}
}

func TestAddRecordsWhoAddedTheEntry(t *testing.T) {
	db := testDB(t)
	userID, username := createUser(t, db)
	subject := uniqueSubject(t, "attributed")

	if err := Add(db, SubjectSite, subject, "", &userID); err != nil {
		t.Fatalf("Add: %v", err)
	}

	entries, err := Entries(db, SubjectSite)
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}

	for _, entry := range entries {
		if entry.Subject == subject {
			if entry.AddedBy != "@"+username {
				t.Errorf("AddedBy = %q, want @%s", entry.AddedBy, username)
			}
			// An omitted reason is stored as NULL and must read back as empty.
			if entry.Reason != "" {
				t.Errorf("Reason = %q, want empty", entry.Reason)
			}
			return
		}
	}
	t.Fatalf("entry %q not found", subject)
}

func TestEntriesOnlyReturnsTheRequestedSubjectType(t *testing.T) {
	db := testDB(t)
	userSubject := uniqueSubject(t, "auser")
	siteSubject := uniqueSubject(t, "asite.example")

	if err := Add(db, SubjectUser, userSubject, "", nil); err != nil {
		t.Fatalf("Add user: %v", err)
	}
	if err := Add(db, SubjectSite, siteSubject, "", nil); err != nil {
		t.Fatalf("Add site: %v", err)
	}

	users, err := Entries(db, SubjectUser)
	if err != nil {
		t.Fatalf("Entries(user): %v", err)
	}
	for _, entry := range users {
		if entry.Subject == siteSubject {
			t.Errorf("site subject %q leaked into the user list", siteSubject)
		}
	}
}

func TestCheckBlocksABlacklistedUser(t *testing.T) {
	db := testDB(t)
	subject := uniqueSubject(t, "blocked")

	if err := Add(db, SubjectUser, subject, "spam", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	block, err := Check(db, []string{subject}, nil)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if block == nil {
		t.Fatal("Check() = nil, want a block")
	}
	if !block.Permanent() {
		t.Error("a blacklist entry should read as permanent")
	}
	if block.SubjectType != SubjectUser || block.Reason != "spam" {
		t.Errorf("block = %+v, want a user block reasoned 'spam'", block)
	}
}

func TestCheckBlocksABlacklistedSite(t *testing.T) {
	db := testDB(t)
	subject := uniqueSubject(t, "blocked.example")

	if err := Add(db, SubjectSite, subject, "", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	block, err := Check(db, nil, SiteSubjects("unrelated-slug", "https://"+subject))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if block == nil || block.SubjectType != SubjectSite {
		t.Fatalf("Check() = %+v, want a site block", block)
	}
}

func TestCheckPassesWhenNothingMatches(t *testing.T) {
	db := testDB(t)

	block, err := Check(db, []string{uniqueSubject(t, "clean")}, []string{uniqueSubject(t, "clean.example")})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if block != nil {
		t.Errorf("Check() = %+v, want nil", block)
	}
}

// An anonymous submitter yields no subjects at all; that must be a pass, not a query
// error or an accidental match.
func TestCheckPassesWithNoSubjects(t *testing.T) {
	db := testDB(t)

	block, err := Check(db, nil, nil)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if block != nil {
		t.Errorf("Check() = %+v, want nil", block)
	}
}

func TestCheckPrefersTheUserBlockWhenBothMatch(t *testing.T) {
	db := testDB(t)
	userSubject := uniqueSubject(t, "bothuser")
	siteSubject := uniqueSubject(t, "bothsite.example")

	if err := Add(db, SubjectSite, siteSubject, "site reason", nil); err != nil {
		t.Fatalf("Add site: %v", err)
	}
	if err := Add(db, SubjectUser, userSubject, "user reason", nil); err != nil {
		t.Fatalf("Add user: %v", err)
	}

	block, err := Check(db, []string{userSubject}, []string{siteSubject})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if block == nil || block.SubjectType != SubjectUser {
		t.Fatalf("Check() = %+v, want the user block to win", block)
	}
}

func TestCheckFallsThroughToCooldowns(t *testing.T) {
	db := testDB(t)
	subject := uniqueSubject(t, "cooling")

	if err := upsertCooldown(db, SubjectUser, subject, "rejected", time.Now().Add(2*time.Hour)); err != nil {
		t.Fatalf("upsertCooldown: %v", err)
	}

	block, err := Check(db, []string{subject}, nil)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if block == nil {
		t.Fatal("Check() = nil, want a cooldown block")
	}
	if block.Permanent() {
		t.Error("a cooldown should not read as permanent")
	}
}

// This is the whole point of the two entry points: an existing member editing their
// site is held by a cooldown but not by a blacklist entry.
func TestCheckCooldownIgnoresBlacklistEntries(t *testing.T) {
	db := testDB(t)
	subject := uniqueSubject(t, "listed")

	if err := Add(db, SubjectUser, subject, "banned", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	block, err := CheckCooldown(db, []string{subject}, nil)
	if err != nil {
		t.Fatalf("CheckCooldown: %v", err)
	}
	if block != nil {
		t.Errorf("CheckCooldown() = %+v, want nil — blacklist entries must not block updates", block)
	}
}

func TestCheckCooldownIgnoresExpiredRows(t *testing.T) {
	db := testDB(t)
	subject := uniqueSubject(t, "stale")

	if err := upsertCooldown(db, SubjectUser, subject, "old", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("upsertCooldown: %v", err)
	}

	block, err := CheckCooldown(db, []string{subject}, nil)
	if err != nil {
		t.Fatalf("CheckCooldown: %v", err)
	}
	if block != nil {
		t.Errorf("CheckCooldown() = %+v, want nil for an expired row", block)
	}
}

func TestRecordRejectionCoolsDownTheUserAndTheSiteSeparately(t *testing.T) {
	db := testDB(t)
	userID, username := createUser(t, db)

	slug := uniqueSubject(t, "rejected-slug")
	host := uniqueSubject(t, "rejected.example")
	t.Cleanup(func() {
		for _, value := range []string{username, fmt.Sprintf("#%d", userID)} {
			if _, err := db.Exec("DELETE FROM request_cooldowns WHERE subject_value = $1", value); err != nil {
				t.Errorf("cleaning up user cooldown %s: %v", value, err)
			}
		}
	})

	req := &models.UpdateRequest{
		ID:            4242,
		UserID:        userID,
		RequestType:   "create",
		User:          &models.User{ID: userID, TelegramID: 1, TelegramUsername: &username},
		ChangedFields: map[string]interface{}{"slug": slug, "url": "https://" + host},
	}

	if err := RecordRejection(db, req); err != nil {
		t.Fatalf("RecordRejection: %v", err)
	}

	for _, want := range []struct{ subjectType, subject string }{
		{SubjectUser, username},
		{SubjectUser, fmt.Sprintf("#%d", userID)},
		{SubjectSite, slug},
		{SubjectSite, host},
	} {
		var count int
		if err := db.QueryRow(`
			SELECT COUNT(*) FROM request_cooldowns
			WHERE subject_type = $1 AND subject_value = $2 AND expires_at > NOW()
		`, want.subjectType, want.subject).Scan(&count); err != nil {
			t.Fatalf("counting %s/%s: %v", want.subjectType, want.subject, err)
		}
		if count != 1 {
			t.Errorf("cooldown rows for %s/%s = %d, want 1", want.subjectType, want.subject, count)
		}
	}

	// The site cooldown alone must still stop a resubmission of the same site.
	block, err := Check(db, nil, SiteSubjects(slug, "https://"+host))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if block == nil || block.SubjectType != SubjectSite {
		t.Errorf("Check() = %+v, want a site cooldown", block)
	}
}

// Every handle-less public submission shares one user row, so cooling it down would
// freeze all of them. Only the site is held.
func TestRecordRejectionLeavesTheAnonymousUserAlone(t *testing.T) {
	db := testDB(t)
	slug := uniqueSubject(t, "anon-slug")

	req := &models.UpdateRequest{
		ID:            77,
		RequestType:   "create",
		User:          &models.User{ID: 1, TelegramID: 0, TelegramUsername: nil},
		ChangedFields: map[string]interface{}{"slug": slug},
	}

	if err := RecordRejection(db, req); err != nil {
		t.Fatalf("RecordRejection: %v", err)
	}

	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM request_cooldowns WHERE subject_type = $1 AND subject_value = $2",
		SubjectUser, "#1").Scan(&count); err != nil {
		t.Fatalf("counting anonymous cooldown: %v", err)
	}
	if count != 0 {
		t.Errorf("anonymous user got %d cooldown rows, want 0", count)
	}

	if err := db.QueryRow(
		"SELECT COUNT(*) FROM request_cooldowns WHERE subject_type = $1 AND subject_value = $2",
		SubjectSite, slug).Scan(&count); err != nil {
		t.Fatalf("counting site cooldown: %v", err)
	}
	if count != 1 {
		t.Errorf("site got %d cooldown rows, want 1", count)
	}
}

// A second rejection has to extend the window rather than trip the unique constraint.
func TestRecordRejectionTwiceExtendsTheWindow(t *testing.T) {
	db := testDB(t)
	slug := uniqueSubject(t, "repeat-slug")

	req := &models.UpdateRequest{
		ID:            1,
		RequestType:   "create",
		ChangedFields: map[string]interface{}{"slug": slug},
	}

	t.Setenv(cooldownEnvVar, "1h")
	if err := RecordRejection(db, req); err != nil {
		t.Fatalf("first RecordRejection: %v", err)
	}

	var first time.Time
	if err := db.QueryRow(
		"SELECT expires_at FROM request_cooldowns WHERE subject_type = $1 AND subject_value = $2",
		SubjectSite, slug).Scan(&first); err != nil {
		t.Fatalf("reading first expiry: %v", err)
	}

	t.Setenv(cooldownEnvVar, "10h")
	req.ID = 2
	if err := RecordRejection(db, req); err != nil {
		t.Fatalf("second RecordRejection: %v", err)
	}

	var second time.Time
	var reason string
	if err := db.QueryRow(
		"SELECT expires_at, reason FROM request_cooldowns WHERE subject_type = $1 AND subject_value = $2",
		SubjectSite, slug).Scan(&second, &reason); err != nil {
		t.Fatalf("reading second expiry: %v", err)
	}

	if !second.After(first) {
		t.Errorf("expiry did not extend: %s then %s", first, second)
	}
	if reason != "Request #2 rejected" {
		t.Errorf("reason = %q, want 'Request #2 rejected'", reason)
	}
}

func TestRecordRejectionIgnoresANilRequest(t *testing.T) {
	db := testDB(t)
	if err := RecordRejection(db, nil); err != nil {
		t.Errorf("RecordRejection(nil) = %v, want nil", err)
	}
}

func TestActiveCooldownsHidesExpiredRows(t *testing.T) {
	db := testDB(t)
	live := uniqueSubject(t, "live")
	dead := uniqueSubject(t, "dead")

	if err := upsertCooldown(db, SubjectUser, live, "", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("upsertCooldown live: %v", err)
	}
	if err := upsertCooldown(db, SubjectUser, dead, "", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("upsertCooldown dead: %v", err)
	}

	cooldowns, err := ActiveCooldowns(db)
	if err != nil {
		t.Fatalf("ActiveCooldowns: %v", err)
	}

	sawLive := false
	for _, c := range cooldowns {
		if c.Subject == dead {
			t.Errorf("expired cooldown %q was listed", dead)
		}
		if c.Subject == live {
			sawLive = true
		}
	}
	if !sawLive {
		t.Errorf("active cooldown %q was not listed", live)
	}
}

func TestRemoveDeletesTheEntry(t *testing.T) {
	db := testDB(t)
	subject := uniqueSubject(t, "removable")

	if err := Add(db, SubjectUser, subject, "", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	var id int
	if err := db.QueryRow(
		"SELECT id FROM blacklist WHERE subject_type = $1 AND subject_value = $2",
		SubjectUser, subject).Scan(&id); err != nil {
		t.Fatalf("reading id: %v", err)
	}

	if err := Remove(db, id); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	block, err := Check(db, []string{subject}, nil)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if block != nil {
		t.Errorf("Check() = %+v after Remove, want nil", block)
	}
}

func TestRemoveCooldownLiftsItEarly(t *testing.T) {
	db := testDB(t)
	subject := uniqueSubject(t, "lifted")

	if err := upsertCooldown(db, SubjectUser, subject, "", time.Now().Add(5*time.Hour)); err != nil {
		t.Fatalf("upsertCooldown: %v", err)
	}

	var id int
	if err := db.QueryRow(
		"SELECT id FROM request_cooldowns WHERE subject_type = $1 AND subject_value = $2",
		SubjectUser, subject).Scan(&id); err != nil {
		t.Fatalf("reading id: %v", err)
	}

	if err := RemoveCooldown(db, id); err != nil {
		t.Fatalf("RemoveCooldown: %v", err)
	}

	block, err := CheckCooldown(db, []string{subject}, nil)
	if err != nil {
		t.Fatalf("CheckCooldown: %v", err)
	}
	if block != nil {
		t.Errorf("CheckCooldown() = %+v after RemoveCooldown, want nil", block)
	}
}

func TestPurgeExpiredKeepsLiveCooldowns(t *testing.T) {
	db := testDB(t)
	live := uniqueSubject(t, "keep")
	dead := uniqueSubject(t, "purge")

	if err := upsertCooldown(db, SubjectSite, live, "", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("upsertCooldown live: %v", err)
	}
	if err := upsertCooldown(db, SubjectSite, dead, "", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("upsertCooldown dead: %v", err)
	}

	PurgeExpired(db)

	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM request_cooldowns WHERE subject_value = $1", dead).Scan(&count); err != nil {
		t.Fatalf("counting purged row: %v", err)
	}
	if count != 0 {
		t.Errorf("expired row survived PurgeExpired")
	}

	if err := db.QueryRow(
		"SELECT COUNT(*) FROM request_cooldowns WHERE subject_value = $1", live).Scan(&count); err != nil {
		t.Fatalf("counting live row: %v", err)
	}
	if count != 1 {
		t.Errorf("PurgeExpired removed a live cooldown")
	}
}
