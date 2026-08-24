package approval

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"webring/internal/telegram"

	_ "github.com/lib/pq"
)

// uniqueSeq keeps rows created by concurrent tests from colliding on the unique columns
// in users.
var uniqueSeq int64

func nextUnique() int64 {
	return time.Now().UnixNano()%1_000_000_000*1000 + atomic.AddInt64(&uniqueSeq, 1)
}

// testDB connects to the database named by TEST_DB_CONNECTION_STRING, skipping the test
// when it is not set. The schema is expected to be migrated already ("make migrate-up").
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
		SELECT 1 FROM information_schema.tables WHERE table_name = 'request_polls'
	)`).Scan(&exists); err != nil {
		t.Fatalf("checking schema: %v", err)
	}
	if !exists {
		t.Fatalf("request_polls table is missing; run 'make migrate-up' against the test database")
	}

	return db
}

// createUser inserts a user and removes it, along with everything referencing it, when
// the test finishes.
func createUser(t *testing.T, db *sql.DB, isAdmin bool) (userID int, telegramID int64) {
	t.Helper()

	telegramID = nextUnique()
	username := fmt.Sprintf("tester_%d", telegramID)

	err := db.QueryRow(`
		INSERT INTO users (telegram_id, telegram_username, first_name, is_admin)
		VALUES ($1, $2, $3, $4) RETURNING id
	`, telegramID, username, username, isAdmin).Scan(&userID)
	if err != nil {
		t.Fatalf("creating user: %v", err)
	}

	t.Cleanup(func() {
		if _, cleanupErr := db.Exec("DELETE FROM users WHERE id = $1", userID); cleanupErr != nil {
			t.Errorf("cleaning up user %d: %v", userID, cleanupErr)
		}
	})

	return userID, telegramID
}

func createRequest(t *testing.T, db *sql.DB, userID int) int {
	t.Helper()

	var requestID int
	err := db.QueryRow(`
		INSERT INTO update_requests (user_id, request_type, changed_fields)
		VALUES ($1, 'create', $2) RETURNING id
	`, userID, `{"slug":"integration","name":"Integration","url":"https://example.test"}`).Scan(&requestID)
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}

	t.Cleanup(func() {
		if _, cleanupErr := db.Exec("DELETE FROM update_requests WHERE id = $1", requestID); cleanupErr != nil {
			t.Errorf("cleaning up request %d: %v", requestID, cleanupErr)
		}
	})

	return requestID
}

func createStoredPoll(t *testing.T, db *sql.DB, store Store, requestID, threshold int) Poll {
	t.Helper()

	poll := Poll{
		PollID:    fmt.Sprintf("poll-%d", nextUnique()),
		RequestID: requestID,
		ChatID:    -1001,
		MessageID: 55,
		Threshold: threshold,
		Status:    StatusOpen,
	}
	if err := store.CreatePoll(context.Background(), poll); err != nil {
		t.Fatalf("creating poll: %v", err)
	}

	t.Cleanup(func() {
		if _, err := db.Exec("DELETE FROM request_polls WHERE poll_id = $1", poll.PollID); err != nil {
			t.Errorf("cleaning up poll %s: %v", poll.PollID, err)
		}
	})

	return poll
}

// The claim is the only thing standing between a dashboard click and a winning vote both
// applying the same request, so it has to hold under real concurrency.
func TestClaimDecisionHasExactlyOneWinner(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	userID, _ := createUser(t, db, false)
	requestID := createRequest(t, db, userID)
	createStoredPoll(t, db, store, requestID, 4)

	const racers = 8
	var wg sync.WaitGroup
	var wins int64

	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status := StatusApproved
			if i%2 == 1 {
				status = StatusDeclined
			}
			<-start
			claimed, err := store.ClaimDecision(ctx, requestID, status)
			if err != nil {
				t.Errorf("ClaimDecision: %v", err)
				return
			}
			if claimed {
				atomic.AddInt64(&wins, 1)
			}
		}(i)
	}

	close(start)
	wg.Wait()

	if got := atomic.LoadInt64(&wins); got != 1 {
		t.Errorf("%d of %d racers claimed the request, want exactly 1", got, racers)
	}
}

func TestClaimDecisionSucceedsForRequestWithoutPoll(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)

	userID, _ := createUser(t, db, false)
	requestID := createRequest(t, db, userID)

	claimed, err := store.ClaimDecision(context.Background(), requestID, StatusApproved)
	if err != nil {
		t.Fatalf("ClaimDecision: %v", err)
	}
	if !claimed {
		t.Errorf("a request with no poll could not be claimed from the dashboard")
	}
}

// Canceling is how a failed apply hands the request back, so a canceled poll must stay
// claimable.
func TestCanceledPollCanBeClaimedAgain(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	userID, _ := createUser(t, db, false)
	requestID := createRequest(t, db, userID)
	poll := createStoredPoll(t, db, store, requestID, 4)

	if err := store.SetStatus(ctx, poll.PollID, StatusCanceled); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	claimed, err := store.ClaimDecision(ctx, requestID, StatusApproved)
	if err != nil {
		t.Fatalf("ClaimDecision: %v", err)
	}
	if !claimed {
		t.Errorf("a canceled poll blocked the request from ever being decided")
	}
}

func TestDecidedPollCannotBeClaimedAgain(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	userID, _ := createUser(t, db, false)
	requestID := createRequest(t, db, userID)
	createStoredPoll(t, db, store, requestID, 4)

	if _, err := store.ClaimDecision(ctx, requestID, StatusApproved); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	claimed, err := store.ClaimDecision(ctx, requestID, StatusDeclined)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed {
		t.Errorf("an already decided request was claimed a second time")
	}
}

func TestAdminByTelegramIDRejectsNonAdmins(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	_, adminTelegramID := createUser(t, db, true)
	_, plainTelegramID := createUser(t, db, false)

	admin, err := store.AdminByTelegramID(ctx, adminTelegramID)
	if err != nil {
		t.Fatalf("AdminByTelegramID: %v", err)
	}
	if admin == nil {
		t.Errorf("an admin was not recognized")
	}

	plain, err := store.AdminByTelegramID(ctx, plainTelegramID)
	if err != nil {
		t.Fatalf("AdminByTelegramID: %v", err)
	}
	if plain != nil {
		t.Errorf("a non-admin passed the admin check: %+v", plain)
	}

	unknown, err := store.AdminByTelegramID(ctx, nextUnique())
	if err != nil {
		t.Fatalf("AdminByTelegramID: %v", err)
	}
	if unknown != nil {
		t.Errorf("an unknown Telegram ID passed the admin check: %+v", unknown)
	}
}

func TestRecordVoteReplacesAPreviousVote(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	adminID, adminTelegramID := createUser(t, db, true)
	requestID := createRequest(t, db, adminID)
	poll := createStoredPoll(t, db, store, requestID, 1)

	if err := store.RecordVote(ctx, poll.PollID, adminID, adminTelegramID, telegram.OptionApprove); err != nil {
		t.Fatalf("RecordVote: %v", err)
	}
	if err := store.RecordVote(ctx, poll.PollID, adminID, adminTelegramID, telegram.OptionDecline); err != nil {
		t.Fatalf("RecordVote: %v", err)
	}

	tally, err := store.Tally(ctx, poll.PollID)
	if err != nil {
		t.Fatalf("Tally: %v", err)
	}
	if tally[telegram.OptionApprove] != 0 {
		t.Errorf("the replaced vote still counts: %v", tally)
	}
	if tally[telegram.OptionDecline] != 1 {
		t.Errorf("declines = %d, want 1: %v", tally[telegram.OptionDecline], tally)
	}
}

func TestTallyAndVoterNamesIgnoreDemotedAdmins(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	keeperID, keeperTelegramID := createUser(t, db, true)
	demotedID, demotedTelegramID := createUser(t, db, true)
	requestID := createRequest(t, db, keeperID)
	poll := createStoredPoll(t, db, store, requestID, 2)

	for _, v := range []struct {
		userID     int
		telegramID int64
	}{{keeperID, keeperTelegramID}, {demotedID, demotedTelegramID}} {
		if err := store.RecordVote(ctx, poll.PollID, v.userID, v.telegramID, telegram.OptionApprove); err != nil {
			t.Fatalf("RecordVote: %v", err)
		}
	}

	tally, err := store.Tally(ctx, poll.PollID)
	if err != nil {
		t.Fatalf("Tally: %v", err)
	}
	if tally[telegram.OptionApprove] != 2 {
		t.Fatalf("setup: approvals = %d, want 2", tally[telegram.OptionApprove])
	}

	if _, err = db.Exec("UPDATE users SET is_admin = false WHERE id = $1", demotedID); err != nil {
		t.Fatalf("demoting user: %v", err)
	}

	tally, err = store.Tally(ctx, poll.PollID)
	if err != nil {
		t.Fatalf("Tally: %v", err)
	}
	if tally[telegram.OptionApprove] != 1 {
		t.Errorf("approvals = %d after demotion, want 1", tally[telegram.OptionApprove])
	}

	names, err := store.VoterNames(ctx, poll.PollID, telegram.OptionApprove)
	if err != nil {
		t.Fatalf("VoterNames: %v", err)
	}
	if len(names) != 1 {
		t.Errorf("VoterNames returned %v, want only the remaining admin", names)
	}
}

func TestDeleteVoteRemovesTheVote(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	adminID, adminTelegramID := createUser(t, db, true)
	requestID := createRequest(t, db, adminID)
	poll := createStoredPoll(t, db, store, requestID, 1)

	if err := store.RecordVote(ctx, poll.PollID, adminID, adminTelegramID, telegram.OptionApprove); err != nil {
		t.Fatalf("RecordVote: %v", err)
	}
	if err := store.DeleteVote(ctx, poll.PollID, adminTelegramID); err != nil {
		t.Fatalf("DeleteVote: %v", err)
	}

	tally, err := store.Tally(ctx, poll.PollID)
	if err != nil {
		t.Fatalf("Tally: %v", err)
	}
	if len(tally) != 0 {
		t.Errorf("tally after retraction = %v, want empty", tally)
	}
}

// Deleting the request must not take the poll with it, or a late vote would look like a
// vote on an unknown poll instead of one on a closed poll.
func TestPollOutlivesTheRequestItDecided(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	userID, _ := createUser(t, db, false)
	requestID := createRequest(t, db, userID)
	poll := createStoredPoll(t, db, store, requestID, 4)

	if _, err := store.ClaimDecision(ctx, requestID, StatusApproved); err != nil {
		t.Fatalf("ClaimDecision: %v", err)
	}
	if _, err := db.Exec("DELETE FROM update_requests WHERE id = $1", requestID); err != nil {
		t.Fatalf("deleting request: %v", err)
	}

	stored, err := store.PollByID(ctx, poll.PollID)
	if err != nil {
		t.Fatalf("PollByID: %v", err)
	}
	if stored == nil {
		t.Fatalf("the poll was deleted along with its request")
	}
	if stored.Status != StatusApproved {
		t.Errorf("poll status = %q, want %q", stored.Status, StatusApproved)
	}
	if stored.RequestID != 0 {
		t.Errorf("request ID = %d, want 0 now that the request is gone", stored.RequestID)
	}
	if stored.ChatID != poll.ChatID || stored.MessageID != poll.MessageID {
		t.Errorf("the identifiers needed to close the poll were lost: %+v", stored)
	}
}

func TestAdminCountOnlyCountsAdminsWithTelegramIDs(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	before, err := store.AdminCount(ctx)
	if err != nil {
		t.Fatalf("AdminCount: %v", err)
	}

	createUser(t, db, true)
	createUser(t, db, false)

	after, err := store.AdminCount(ctx)
	if err != nil {
		t.Fatalf("AdminCount: %v", err)
	}
	if after != before+1 {
		t.Errorf("AdminCount went from %d to %d, want an increase of exactly 1", before, after)
	}
}

// The dashboard query is two outer joins deep, so it gets exercised against real SQL.
func TestVoteSummariesAgainstPostgres(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	approverID, approverTelegramID := createUser(t, db, true)
	declinerID, declinerTelegramID := createUser(t, db, true)
	outsiderID, outsiderTelegramID := createUser(t, db, false)

	votedRequest := createRequest(t, db, approverID)
	votedPoll := createStoredPoll(t, db, store, votedRequest, 4)

	// A second request whose poll nobody has voted in yet.
	emptyRequest := createRequest(t, db, approverID)
	createStoredPoll(t, db, store, emptyRequest, 4)

	// A third request with no poll at all.
	pollessRequest := createRequest(t, db, approverID)

	for _, v := range []struct {
		userID     int
		telegramID int64
		option     int
	}{
		{approverID, approverTelegramID, telegram.OptionApprove},
		{declinerID, declinerTelegramID, telegram.OptionDecline},
		{outsiderID, outsiderTelegramID, telegram.OptionApprove},
	} {
		if err := store.RecordVote(ctx, votedPoll.PollID, v.userID, v.telegramID, v.option); err != nil {
			t.Fatalf("RecordVote: %v", err)
		}
	}

	summaries, err := store.VoteSummaries(ctx, []int{votedRequest, emptyRequest, pollessRequest})
	if err != nil {
		t.Fatalf("VoteSummaries: %v", err)
	}

	voted := summaries[votedRequest]
	if voted == nil {
		t.Fatalf("no summary for the request that was voted on")
	}
	if voted.Threshold != 4 || voted.Status != StatusOpen {
		t.Errorf("summary = %+v, want threshold 4 and status open", voted)
	}
	if len(voted.Approve) != 1 {
		t.Errorf("Approve = %v, want just the one admin — the outsider must be excluded", voted.Approve)
	}
	if len(voted.Decline) != 1 {
		t.Errorf("Decline = %v, want one admin", voted.Decline)
	}
	// first_name is set to the username by the fixture, so the handle rides alongside it.
	if voted.Approve[0].Username == "" {
		t.Errorf("approver %+v has no handle", voted.Approve[0])
	}

	empty := summaries[emptyRequest]
	if empty == nil {
		t.Fatalf("a poll with no votes produced no summary")
	}
	if empty.Leading() != 0 || empty.Threshold != 4 {
		t.Errorf("summary for an unvoted poll = %+v, want 0 of 4", empty)
	}

	if _, ok := summaries[pollessRequest]; ok {
		t.Errorf("a request with no poll got a summary")
	}
}

func TestVoteSummariesDropsDemotedAdminsAgainstPostgres(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	keeperID, keeperTelegramID := createUser(t, db, true)
	demotedID, demotedTelegramID := createUser(t, db, true)

	requestID := createRequest(t, db, keeperID)
	poll := createStoredPoll(t, db, store, requestID, 4)

	for _, v := range []struct {
		userID     int
		telegramID int64
	}{{keeperID, keeperTelegramID}, {demotedID, demotedTelegramID}} {
		if err := store.RecordVote(ctx, poll.PollID, v.userID, v.telegramID, telegram.OptionApprove); err != nil {
			t.Fatalf("RecordVote: %v", err)
		}
	}
	if _, err := db.Exec("UPDATE users SET is_admin = false WHERE id = $1", demotedID); err != nil {
		t.Fatalf("demoting user: %v", err)
	}

	summaries, err := store.VoteSummaries(ctx, []int{requestID})
	if err != nil {
		t.Fatalf("VoteSummaries: %v", err)
	}
	if got := summaries[requestID]; got == nil || len(got.Approve) != 1 {
		t.Errorf("summary = %+v, want only the admin who kept their rights", got)
	}
}

func TestVoteSummariesWithNoRequestIDs(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)

	summaries, err := store.VoteSummaries(context.Background(), nil)
	if err != nil {
		t.Fatalf("VoteSummaries: %v", err)
	}
	if len(summaries) != 0 {
		t.Errorf("VoteSummaries(nil) = %v, want empty", summaries)
	}
}

func TestPollByIDReturnsNilForUnknownPoll(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)

	poll, err := store.PollByID(context.Background(), fmt.Sprintf("missing-%d", nextUnique()))
	if err != nil {
		t.Fatalf("PollByID: %v", err)
	}
	if poll != nil {
		t.Errorf("PollByID invented a poll: %+v", poll)
	}
}
