package user

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

	"webring/internal/models"

	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
)

func TestUserCreateRejectsInvalidURL(t *testing.T) {
	form := url.Values{"slug": {"sample"}, "name": {"Sample"}, "url": {"https://singlelabel"}}
	req := httptest.NewRequest(http.MethodPost, "/user/sites/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(SetUserContext(req.Context(), &models.User{ID: 1}))
	w := httptest.NewRecorder()
	createSiteRequestHandler(nil).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid user submission returned %d: %s", w.Code, w.Body.String())
	}
}

func TestUserUpdateRejectsInvalidURL(t *testing.T) {
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
	username := fmt.Sprintf("user_url_test_%d", time.Now().UnixNano())
	var userID, siteID int
	if queryErr := db.QueryRow("INSERT INTO users (telegram_username) VALUES ($1) RETURNING id", username).
		Scan(&userID); queryErr != nil {
		t.Fatal(queryErr)
	}
	t.Cleanup(func() {
		if _, cleanupErr := db.Exec("DELETE FROM users WHERE id = $1", userID); cleanupErr != nil {
			t.Errorf("deleting user: %v", cleanupErr)
		}
	})
	if queryErr := db.QueryRow(`INSERT INTO sites (slug, name, url, user_id, display_order)
		VALUES ($1, 'Original', 'https://site.invalid', $2,
		(SELECT COALESCE(MAX(display_order), 0) + 1 FROM sites)) RETURNING id`, username, userID).
		Scan(&siteID); queryErr != nil {
		t.Fatal(queryErr)
	}
	t.Cleanup(func() {
		if _, cleanupErr := db.Exec("DELETE FROM sites WHERE id = $1", siteID); cleanupErr != nil {
			t.Errorf("deleting site: %v", cleanupErr)
		}
	})
	form := url.Values{"name": {"Changed"}, "url": {"https://singlelabel"}}
	req := httptest.NewRequest(http.MethodPost, "/user/sites/1/update", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = mux.SetURLVars(req, map[string]string{"id": fmt.Sprint(siteID)})
	req = req.WithContext(SetUserContext(req.Context(), &models.User{ID: userID}))
	w := httptest.NewRecorder()
	updateSiteRequestHandler(db).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid user update returned %d: %s", w.Code, w.Body.String())
	}
	var count int
	if queryErr := db.QueryRow("SELECT COUNT(*) FROM update_requests WHERE user_id = $1", userID).
		Scan(&count); queryErr != nil {
		t.Fatal(queryErr)
	}
	if count != 0 {
		t.Fatal("invalid update request was created")
	}
}
