package approval

import (
	"context"
	"database/sql"
	"log"
	"sync"
	"time"

	"webring/internal/models"
	"webring/internal/telegram"
)

// announceTimeout bounds the Telegram calls made when a request is first published.
const announceTimeout = 30 * time.Second

var (
	defaultManager *Manager
	defaultMu      sync.RWMutex
)

// Init builds the shared Manager from the environment and makes it available to HTTP
// handlers via Default. It returns nil when approval polls are not configured, which
// leaves requests to be decided from the dashboard exactly as before.
func Init(db *sql.DB) *Manager {
	token := telegram.BotToken()
	chatID := telegram.AdminChatID()

	var manager *Manager
	if token != "" && chatID != 0 {
		manager = NewManager(NewStore(db), telegram.NewClient(token), NewDecider(db), chatID)
	}

	defaultMu.Lock()
	defaultManager = manager
	defaultMu.Unlock()

	return manager
}

// Default returns the shared Manager, which may be nil. All Manager methods tolerate a
// nil receiver, so callers do not need to check.
func Default() *Manager {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultManager
}

// Announce publishes a newly created request to the admins: a direct message to each of
// them and, when an admin group chat is configured, an approval poll in that group.
//
// It is meant to be called in a goroutine — Telegram is slow and must not hold up the
// user's request.
func Announce(db *sql.DB, req *models.UpdateRequest, submitter *models.User) {
	telegram.NotifyAdminsOfNewRequest(db, req, submitter)

	manager := Default()
	if !manager.Enabled() {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), announceTimeout)
	defer cancel()

	if err := manager.CreatePoll(ctx, req, submitter); err != nil {
		log.Printf("Error opening approval poll for request %d: %v", req.ID, err)
	}
}
