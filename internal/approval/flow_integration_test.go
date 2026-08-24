package approval

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"webring/internal/requests"
	"webring/internal/telegram"

	_ "github.com/lib/pq"
)

// flowFixture wires a Manager to the real store and decider, with only Telegram faked
// out. Everything between a vote arriving and a site appearing in the database is the
// production code path.
type flowFixture struct {
	db      *sql.DB
	bot     *fakeBot
	manager *Manager

	adminTelegramIDs []int64
	requestID        int
	slug             string
	poll             Poll
}

func newFlowFixture(t *testing.T, requestType string) *flowFixture {
	t.Helper()

	db := testDB(t)
	bot := newFakeBot()
	bot.nextPollID = fmt.Sprintf("flow-poll-%d", nextUnique())
	manager := NewManager(NewStore(db), bot, NewDecider(db), -1001)

	f := &flowFixture{db: db, bot: bot, manager: manager, slug: fmt.Sprintf("flow-%d", nextUnique())}

	for i := 0; i < 7; i++ {
		_, telegramID := createUser(t, db, true)
		f.adminTelegramIDs = append(f.adminTelegramIDs, telegramID)
	}

	submitterID, _ := createUser(t, db, false)

	var siteID *int
	fields := map[string]interface{}{
		"slug": f.slug,
		"name": "Flow Site",
		// A .invalid host can never resolve, so the background favicon fetch fails fast
		// instead of reaching the network.
		"url": "https://flow.invalid",
	}
	if requestType == "update" {
		fields = map[string]interface{}{"name": "Renamed Site"}
		siteID = new(int)
		*siteID = f.createSite(t, submitterID)
	}

	requestID, err := requests.Create(db, submitterID, siteID, requestType, fields)
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	f.requestID = requestID
	t.Cleanup(func() {
		if _, cleanupErr := db.Exec("DELETE FROM update_requests WHERE id = $1", requestID); cleanupErr != nil {
			t.Errorf("cleaning up request: %v", cleanupErr)
		}
	})

	req, err := requests.Load(db, requestID)
	if err != nil {
		t.Fatalf("loading request: %v", err)
	}
	if err = manager.CreatePoll(context.Background(), req, req.User); err != nil {
		t.Fatalf("CreatePoll: %v", err)
	}
	t.Cleanup(func() {
		if _, cleanupErr := db.Exec("DELETE FROM request_polls WHERE poll_id = $1", bot.nextPollID); cleanupErr != nil {
			t.Errorf("cleaning up poll: %v", cleanupErr)
		}
	})

	stored, err := manager.store.PollByID(context.Background(), bot.nextPollID)
	if err != nil || stored == nil {
		t.Fatalf("the poll was not stored: %v", err)
	}
	f.poll = *stored

	return f
}

func (f *flowFixture) createSite(t *testing.T, ownerID int) int {
	t.Helper()

	var siteID int
	err := f.db.QueryRow(`
		INSERT INTO sites (id, slug, name, url, user_id, display_order)
		VALUES ((SELECT COALESCE(MAX(id), 0) + 1 FROM sites), $1, 'Original Site',
		        'https://original.invalid', $2, (SELECT COALESCE(MAX(display_order), 0) + 1 FROM sites))
		RETURNING id
	`, f.slug, ownerID).Scan(&siteID)
	if err != nil {
		t.Fatalf("creating site: %v", err)
	}
	return siteID
}

func (f *flowFixture) cleanupSite(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := f.db.Exec("DELETE FROM sites WHERE slug = $1", f.slug); err != nil {
			t.Errorf("cleaning up site: %v", err)
		}
	})
}

func (f *flowFixture) vote(t *testing.T, index, option int) {
	t.Helper()
	err := f.manager.HandlePollAnswer(context.Background(), &telegram.PollAnswer{
		PollID:    f.poll.PollID,
		User:      &telegram.APIUser{ID: f.adminTelegramIDs[index]},
		OptionIDs: []int{option},
	})
	if err != nil {
		t.Fatalf("HandlePollAnswer: %v", err)
	}
}

func (f *flowFixture) pollStatus(t *testing.T) Status {
	t.Helper()
	poll, err := f.manager.store.PollByID(context.Background(), f.poll.PollID)
	if err != nil || poll == nil {
		t.Fatalf("reading poll: %v", err)
	}
	return poll.Status
}

// The whole point of the feature, exercised against a real database: four of seven admins
// vote to approve, and the site appears.
func TestFourVotesCreateTheSiteEndToEnd(t *testing.T) {
	f := newFlowFixture(t, "create")
	f.cleanupSite(t)

	if f.poll.Threshold != 4 {
		t.Fatalf("threshold = %d, want 4 for seven admins", f.poll.Threshold)
	}

	for i := 0; i < 3; i++ {
		f.vote(t, i, telegram.OptionApprove)
	}

	var count int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM sites WHERE slug = $1", f.slug).Scan(&count); err != nil {
		t.Fatalf("counting sites: %v", err)
	}
	if count != 0 {
		t.Fatalf("the site was created after only three votes")
	}

	f.vote(t, 3, telegram.OptionApprove)

	var name string
	if err := f.db.QueryRow("SELECT name FROM sites WHERE slug = $1", f.slug).Scan(&name); err != nil {
		t.Fatalf("the site was not created after the fourth vote: %v", err)
	}
	if name != "Flow Site" {
		t.Errorf("site name = %q, want %q", name, "Flow Site")
	}

	if _, err := requests.Load(f.db, f.requestID); !errors.Is(err, requests.ErrNotFound) {
		t.Errorf("the request survived approval: %v", err)
	}
	if got := f.pollStatus(t); got != StatusApproved {
		t.Errorf("poll status = %q, want %q", got, StatusApproved)
	}
	if got := f.bot.stopCount(); got != 1 {
		t.Errorf("StopPoll called %d times, want 1", got)
	}

	announcement := ""
	for _, text := range f.bot.sentTexts() {
		if strings.Contains(text, "Approved by") {
			announcement = text
			break
		}
	}
	if announcement == "" {
		t.Fatalf("no approval announcement was sent")
	}
	for i := 0; i < 4; i++ {
		name := telegram.EscapeMarkdownV2(fmt.Sprintf("tester_%d", f.adminTelegramIDs[i]))
		if !strings.Contains(announcement, name) {
			t.Errorf("announcement is missing voter %s:\n%s", name, announcement)
		}
	}
	for i := 4; i < 7; i++ {
		name := telegram.EscapeMarkdownV2(fmt.Sprintf("tester_%d", f.adminTelegramIDs[i]))
		if strings.Contains(announcement, name) {
			t.Errorf("announcement lists non-voter %s:\n%s", name, announcement)
		}
	}
}

func TestFourVotesApplyAnUpdateEndToEnd(t *testing.T) {
	f := newFlowFixture(t, "update")
	f.cleanupSite(t)

	for i := 0; i < 4; i++ {
		f.vote(t, i, telegram.OptionApprove)
	}

	var name string
	if err := f.db.QueryRow("SELECT name FROM sites WHERE slug = $1", f.slug).Scan(&name); err != nil {
		t.Fatalf("reading the site: %v", err)
	}
	if name != "Renamed Site" {
		t.Errorf("site name = %q, want %q", name, "Renamed Site")
	}
}

func TestFourDeclinesDiscardTheRequestEndToEnd(t *testing.T) {
	f := newFlowFixture(t, "create")
	f.cleanupSite(t)

	for i := 0; i < 4; i++ {
		f.vote(t, i, telegram.OptionDecline)
	}

	if _, err := requests.Load(f.db, f.requestID); !errors.Is(err, requests.ErrNotFound) {
		t.Errorf("the request survived being declined: %v", err)
	}

	var count int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM sites WHERE slug = $1", f.slug).Scan(&count); err != nil {
		t.Fatalf("counting sites: %v", err)
	}
	if count != 0 {
		t.Errorf("a declined request created %d sites", count)
	}
	if got := f.pollStatus(t); got != StatusDeclined {
		t.Errorf("poll status = %q, want %q", got, StatusDeclined)
	}
}

// Outsiders in the admin group must not be able to push a request through, no matter how
// many of them vote.
func TestOutsidersCannotCarryAVoteEndToEnd(t *testing.T) {
	f := newFlowFixture(t, "create")
	f.cleanupSite(t)

	// Two real admins, then six votes from users who are not admins at all.
	f.vote(t, 0, telegram.OptionApprove)
	f.vote(t, 1, telegram.OptionApprove)

	for i := 0; i < 6; i++ {
		_, outsiderTelegramID := createUser(t, f.db, false)
		err := f.manager.HandlePollAnswer(context.Background(), &telegram.PollAnswer{
			PollID:    f.poll.PollID,
			User:      &telegram.APIUser{ID: outsiderTelegramID},
			OptionIDs: []int{telegram.OptionApprove},
		})
		if err != nil {
			t.Fatalf("HandlePollAnswer: %v", err)
		}
	}

	var count int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM sites WHERE slug = $1", f.slug).Scan(&count); err != nil {
		t.Fatalf("counting sites: %v", err)
	}
	if count != 0 {
		t.Errorf("non-admin votes created the site")
	}
	if got := f.pollStatus(t); got != StatusOpen {
		t.Errorf("poll status = %q, want it to still be %q", got, StatusOpen)
	}
}

// A dashboard decision and a winning vote racing on the same request: whoever claims it
// first wins, and the request is applied exactly once.
func TestDashboardDecisionBeatsTheVoteEndToEnd(t *testing.T) {
	f := newFlowFixture(t, "create")
	f.cleanupSite(t)

	// The dashboard claims and declines the request first.
	claimed, err := f.manager.Claim(context.Background(), f.requestID, StatusDeclined)
	if err != nil || !claimed {
		t.Fatalf("dashboard claim = (%v, %v), want (true, nil)", claimed, err)
	}
	req, err := requests.Load(f.db, f.requestID)
	if err != nil {
		t.Fatalf("loading request: %v", err)
	}
	if err = requests.Decline(f.db, req); err != nil {
		t.Fatalf("Decline: %v", err)
	}

	// The votes arrive afterwards and must not resurrect anything.
	for i := 0; i < 4; i++ {
		f.vote(t, i, telegram.OptionApprove)
	}

	var count int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM sites WHERE slug = $1", f.slug).Scan(&count); err != nil {
		t.Fatalf("counting sites: %v", err)
	}
	if count != 0 {
		t.Errorf("the vote applied a request the dashboard had already declined")
	}
	if got := f.pollStatus(t); got != StatusDeclined {
		t.Errorf("poll status = %q, want %q", got, StatusDeclined)
	}
}
