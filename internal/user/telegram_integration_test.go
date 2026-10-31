package user

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"webring/internal/auth"

	_ "github.com/lib/pq"
)

func TestPlaceholderClaimDoesNotAuthenticateAnotherUser(t *testing.T) {
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
	username := fmt.Sprintf("oidc_race_%d", time.Now().UnixNano())
	var userID int
	if queryErr := db.QueryRow(`INSERT INTO users (telegram_username, is_admin)
		VALUES ($1, true) RETURNING id`, username).Scan(&userID); queryErr != nil {
		t.Fatal(queryErr)
	}
	t.Cleanup(func() {
		if _, cleanupErr := db.Exec("DELETE FROM users WHERE id = $1", userID); cleanupErr != nil {
			t.Errorf("deleting user: %v", cleanupErr)
		}
	})

	// Keep the winning claim uncommitted so the other login sees the old placeholder
	// and waits on its UPDATE. PostgreSQL rechecks its WHERE clause after the commit.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			t.Errorf("rolling back transaction: %v", rollbackErr)
		}
	}()
	winnerID := time.Now().UnixNano() % 1_000_000_000_000
	if _, err := tx.Exec("UPDATE users SET telegram_id = $1 WHERE id = $2", winnerID, userID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		userID int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		user, err := getOrCreateUser(db, &auth.TelegramUser{ID: winnerID + 1, Username: username})
		if user != nil {
			done <- result{userID: user.ID, err: err}
		} else {
			done <- result{err: err}
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			AND query LIKE '%SET telegram_id = $1, first_name = $2, last_name = $3, telegram_username = $4%')`).
			Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("login did not reach the blocked placeholder update")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err == nil || got.userID != 0 {
			t.Fatalf("losing claim authenticated user %d: %v", got.userID, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("login did not finish after placeholder claim committed")
	}
	var storedID int64
	if err := db.QueryRow("SELECT telegram_id FROM users WHERE id = $1", userID).Scan(&storedID); err != nil {
		t.Fatal(err)
	}
	if storedID != winnerID {
		t.Fatalf("placeholder belongs to %d, wanted %d", storedID, winnerID)
	}
}

func TestExistingUsernameOnAnotherTelegramIDDoesNotRetryForever(t *testing.T) {
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
	id := time.Now().UnixNano() % 1_000_000_000_000
	username := fmt.Sprintf("oidc_conflict_%d", id)
	var userID int
	if queryErr := db.QueryRow(`INSERT INTO users (telegram_id, telegram_username)
		VALUES ($1, $2) RETURNING id`, id, username).Scan(&userID); queryErr != nil {
		t.Fatal(queryErr)
	}
	t.Cleanup(func() {
		if _, cleanupErr := db.Exec("DELETE FROM users WHERE id = $1", userID); cleanupErr != nil {
			t.Errorf("deleting user: %v", cleanupErr)
		}
	})
	user, err := getOrCreateUser(db, &auth.TelegramUser{ID: id + 1, Username: username})
	if err == nil || user != nil {
		t.Fatalf("conflicting username was accepted: %+v, %v", user, err)
	}
	var storedID int64
	if queryErr := db.QueryRow("SELECT telegram_id FROM users WHERE id = $1", userID).Scan(&storedID); queryErr != nil {
		t.Fatal(queryErr)
	}
	if storedID != id {
		t.Fatalf("original Telegram ID = %d, wanted %d", storedID, id)
	}
}
