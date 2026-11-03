package dashboard

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
)

func TestAdminAddRejectsInvalidSiteBeforeDatabaseWrites(t *testing.T) {
	form := url.Values{"id": {"1"}, "slug": {"sample"}, "name": {"Sample"}, "url": {"https://singlelabel"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	addSiteHandler(nil).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid admin add returned %d: %s", w.Code, w.Body.String())
	}
}

func TestAdminUpdateRejectsInvalidURL(t *testing.T) {
	dsn := os.Getenv("TEST_DB_CONNECTION_STRING")
	if dsn == "" {
		t.Skip("TEST_DB_CONNECTION_STRING not set; skipping database integration test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("closing database: %v", closeErr)
		}
	})
	slug := fmt.Sprintf("admin-url-test-%d", time.Now().UnixNano())
	var siteID int
	if queryErr := db.QueryRow(`INSERT INTO sites (slug, name, url, display_order)
		VALUES ($1, 'Original', 'https://site.invalid',
		(SELECT COALESCE(MAX(display_order), 0) + 1 FROM sites)) RETURNING id`, slug).Scan(&siteID); queryErr != nil {
		t.Fatal(queryErr)
	}
	t.Cleanup(func() {
		if _, cleanupErr := db.Exec("DELETE FROM sites WHERE id = $1", siteID); cleanupErr != nil {
			t.Errorf("deleting site: %v", cleanupErr)
		}
	})
	form := url.Values{"slug": {slug}, "name": {"Changed"}, "url": {"https://singlelabel"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/update/1", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = mux.SetURLVars(req, map[string]string{"id": fmt.Sprint(siteID)})
	w := httptest.NewRecorder()
	updateSiteHandler(db).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid admin update returned %d: %s", w.Code, w.Body.String())
	}
	var name, storedURL string
	if queryErr := db.QueryRow("SELECT name, url FROM sites WHERE id = $1", siteID).
		Scan(&name, &storedURL); queryErr != nil {
		t.Fatal(queryErr)
	}
	if name != "Original" || storedURL != "https://site.invalid" {
		t.Fatalf("invalid update changed site: %q %q", name, storedURL)
	}
}
