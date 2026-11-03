package requests

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"webring/internal/models"

	_ "github.com/lib/pq"
)

var uniqueSeq int64

func approveOffline(db *sql.DB, req *models.UpdateRequest) error {
	return ApproveWithVerifier(db, req,
		func(_ context.Context, raw string) (string, error) { return raw, nil })
}

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

func createUser(t *testing.T, db *sql.DB) int {
	t.Helper()

	telegramID := nextUnique()
	username := fmt.Sprintf("reqtest_%d", telegramID)

	var userID int
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

	return userID
}

func cleanupSite(t *testing.T, db *sql.DB, slug string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := db.Exec("DELETE FROM sites WHERE slug = $1", slug); err != nil {
			t.Errorf("cleaning up site %s: %v", slug, err)
		}
	})
}

func TestCreateReturnsTheNewRequestID(t *testing.T) {
	db := testDB(t)
	userID := createUser(t, db)

	slug := fmt.Sprintf("slug-%d", nextUnique())
	requestID, err := Create(db, userID, nil, "create", map[string]interface{}{
		"slug": slug,
		"name": "Example",
		"url":  "https://example.test",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if requestID == 0 {
		t.Fatalf("Create returned ID 0; the approval poll could not be linked to it")
	}
	t.Cleanup(func() {
		if _, cleanupErr := db.Exec("DELETE FROM update_requests WHERE id = $1", requestID); cleanupErr != nil {
			t.Errorf("cleanup: %v", cleanupErr)
		}
	})

	loaded, err := Load(db, requestID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ID != requestID {
		t.Errorf("loaded ID = %d, want %d", loaded.ID, requestID)
	}
	if loaded.ChangedFields["slug"] != slug {
		t.Errorf("loaded slug = %v, want %q", loaded.ChangedFields["slug"], slug)
	}
	if loaded.User == nil {
		t.Errorf("Load did not populate the submitting user")
	}
}

func TestLoadReportsMissingRequests(t *testing.T) {
	db := testDB(t)

	_, err := Load(db, -1)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Load of a missing request returned %v, want ErrNotFound", err)
	}
}

func TestApproveCreatesTheSiteAndRemovesTheRequest(t *testing.T) {
	db := testDB(t)
	userID := createUser(t, db)

	slug := fmt.Sprintf("slug-%d", nextUnique())
	cleanupSite(t, db, slug)

	requestID, err := Create(db, userID, nil, "create", map[string]interface{}{
		"slug": slug,
		"name": "Example Site",
		"url":  "https://example.test",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	req, err := Load(db, requestID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err = approveOffline(db, req); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	var name string
	if err = db.QueryRow("SELECT name FROM sites WHERE slug = $1", slug).Scan(&name); err != nil {
		t.Fatalf("the site was not created: %v", err)
	}
	if name != "Example Site" {
		t.Errorf("site name = %q, want %q", name, "Example Site")
	}

	if _, err = Load(db, requestID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the request survived approval: %v", err)
	}
}

// A duplicate slug must leave the request in place so the error can be shown and the
// decision retried.
func TestApproveLeavesTheRequestWhenApplyingFails(t *testing.T) {
	db := testDB(t)
	userID := createUser(t, db)

	slug := fmt.Sprintf("slug-%d", nextUnique())
	cleanupSite(t, db, slug)

	firstID, err := Create(db, userID, nil, "create", map[string]interface{}{
		"slug": slug, "name": "First", "url": "https://first.test",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	first, err := Load(db, firstID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err = approveOffline(db, first); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	secondID, err := Create(db, userID, nil, "create", map[string]interface{}{
		"slug": slug, "name": "Second", "url": "https://second.test",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		if _, cleanupErr := db.Exec("DELETE FROM update_requests WHERE id = $1", secondID); cleanupErr != nil {
			t.Errorf("cleanup: %v", cleanupErr)
		}
	})

	second, err := Load(db, secondID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err = approveOffline(db, second); err == nil {
		t.Fatalf("Approve succeeded despite the slug already being taken")
	}

	if _, err = Load(db, secondID); err != nil {
		t.Errorf("the request was discarded even though it was never applied: %v", err)
	}
}

func TestApproveUpdateChangesTheSite(t *testing.T) {
	db := testDB(t)
	userID := createUser(t, db)

	slug := fmt.Sprintf("slug-%d", nextUnique())
	cleanupSite(t, db, slug)

	createID, err := Create(db, userID, nil, "create", map[string]interface{}{
		"slug": slug, "name": "Before", "url": "https://before.test",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	created, err := Load(db, createID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err = approveOffline(db, created); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	var siteID int
	if err = db.QueryRow("SELECT id FROM sites WHERE slug = $1", slug).Scan(&siteID); err != nil {
		t.Fatalf("finding the created site: %v", err)
	}

	updateID, err := Create(db, userID, &siteID, "update", map[string]interface{}{
		"name": "After",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	update, err := Load(db, updateID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if update.Site == nil || update.Site.Slug != slug {
		t.Errorf("Load did not attach the target site: %+v", update.Site)
	}
	if err = approveOffline(db, update); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	var name string
	if err = db.QueryRow("SELECT name FROM sites WHERE id = $1", siteID).Scan(&name); err != nil {
		t.Fatalf("reading the site: %v", err)
	}
	if name != "After" {
		t.Errorf("site name = %q, want %q", name, "After")
	}
}

func TestDeclineRemovesTheRequestWithoutTouchingSites(t *testing.T) {
	db := testDB(t)
	userID := createUser(t, db)

	slug := fmt.Sprintf("slug-%d", nextUnique())
	cleanupSite(t, db, slug)

	requestID, err := Create(db, userID, nil, "create", map[string]interface{}{
		"slug": slug, "name": "Rejected", "url": "https://rejected.test",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	req, err := Load(db, requestID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err = Decline(db, req); err != nil {
		t.Fatalf("Decline: %v", err)
	}

	if _, err = Load(db, requestID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the request survived being declined: %v", err)
	}

	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM sites WHERE slug = $1", slug).Scan(&count); err != nil {
		t.Fatalf("counting sites: %v", err)
	}
	if count != 0 {
		t.Errorf("a declined request created %d sites", count)
	}
}

func TestApprovalRechecksOldInvalidURL(t *testing.T) {
	db := testDB(t)
	userID := createUser(t, db)
	slug := fmt.Sprintf("old-request-%d", nextUnique())
	requestID, err := Create(db, userID, nil, "create", map[string]interface{}{
		"slug": slug, "name": "Test Site", "url": "https://singlelabel",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, cleanupErr := db.Exec("DELETE FROM update_requests WHERE id = $1", requestID); cleanupErr != nil {
			t.Errorf("cleaning up request: %v", cleanupErr)
		}
	})
	req, err := Load(db, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := Approve(db, req); err == nil {
		t.Fatal("approved an old request with an invalid URL")
	}
	if _, err := Load(db, requestID); err != nil {
		t.Fatalf("rejected request was removed: %v", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sites WHERE slug = $1", slug).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("invalid request created a site")
	}
}

func TestApprovalRejectsInvalidUpdateBeforeWriting(t *testing.T) {
	db := testDB(t)
	userID := createUser(t, db)
	slug := fmt.Sprintf("old-update-%d", nextUnique())
	cleanupSite(t, db, slug)
	var siteID int
	if err := db.QueryRow(`INSERT INTO sites (slug, name, url, user_id, display_order)
		VALUES ($1, 'Original', 'https://site.invalid', $2,
		(SELECT COALESCE(MAX(display_order), 0) + 1 FROM sites)) RETURNING id`, slug, userID).Scan(&siteID); err != nil {
		t.Fatal(err)
	}
	requestID, err := Create(db, userID, &siteID, "update", map[string]interface{}{
		"name": "Changed", "url": "https://singlelabel",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, cleanupErr := db.Exec("DELETE FROM update_requests WHERE id = $1", requestID); cleanupErr != nil {
			t.Errorf("cleaning up request: %v", cleanupErr)
		}
	})
	req, err := Load(db, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := Approve(db, req); err == nil {
		t.Fatal("approved an invalid URL update")
	}
	var name, storedURL string
	if err := db.QueryRow("SELECT name, url FROM sites WHERE id = $1", siteID).Scan(&name, &storedURL); err != nil {
		t.Fatal(err)
	}
	if name != "Original" || storedURL != "https://site.invalid" {
		t.Fatalf("invalid update changed site: %q %q", name, storedURL)
	}
	if _, err := Load(db, requestID); err != nil {
		t.Fatalf("rejected request was removed: %v", err)
	}
}
