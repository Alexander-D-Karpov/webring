package approval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"

	"webring/internal/models"
	"webring/internal/telegram"
)

// TestMain loads the built-in message templates. RenderMessage returns an empty string
// without them, which would hide broken announcements rather than fail on them.
func TestMain(m *testing.M) {
	telegram.InitTemplates("testdata-does-not-exist")
	os.Exit(m.Run())
}

type storedVote struct {
	userID int
	option int
	seq    int
}

// fakeStore is an in-memory Store that mirrors the behavior of the SQL implementation,
// including its rule that only current admins are counted.
type fakeStore struct {
	mu sync.Mutex

	// admins maps Telegram ID to the admin who owns it. Deleting an entry demotes them.
	admins map[int64]*models.User
	// adminCountOverride, when positive, decouples the voter pool size from the map so
	// a snapshot can be taken with a different number of admins than exist later.
	adminCountOverride int

	polls map[string]*Poll
	votes map[string]map[int64]storedVote
	seq   int

	claimAttempts int
	failClaim     bool
	tallyErr      error
}

func newFakeStore(adminTelegramIDs ...int64) *fakeStore {
	s := &fakeStore{
		admins: make(map[int64]*models.User),
		polls:  make(map[string]*Poll),
		votes:  make(map[string]map[int64]storedVote),
	}
	for i, tgID := range adminTelegramIDs {
		name := fmt.Sprintf("admin%d", i+1)
		s.admins[tgID] = &models.User{
			ID:               i + 1,
			TelegramID:       tgID,
			TelegramUsername: &name,
			IsAdmin:          true,
		}
	}
	return s
}

func (f *fakeStore) demote(telegramID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.admins, telegramID)
}

func (f *fakeStore) AdminCount(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.adminCountOverride > 0 {
		return f.adminCountOverride, nil
	}
	return len(f.admins), nil
}

func (f *fakeStore) AdminTelegramIDs(context.Context) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]int64, 0, len(f.admins))
	for id := range f.admins {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

func (f *fakeStore) AdminByTelegramID(_ context.Context, telegramID int64) (*models.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.admins[telegramID], nil
}

func (f *fakeStore) CreatePoll(_ context.Context, poll Poll) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored := poll
	f.polls[poll.PollID] = &stored
	return nil
}

func (f *fakeStore) PollByID(_ context.Context, pollID string) (*Poll, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	poll, ok := f.polls[pollID]
	if !ok {
		return nil, nil
	}
	copied := *poll
	return &copied, nil
}

func (f *fakeStore) PollByRequestID(_ context.Context, requestID int) (*Poll, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, poll := range f.polls {
		if poll.RequestID == requestID {
			copied := *poll
			return &copied, nil
		}
	}
	return nil, nil
}

func (f *fakeStore) SetStatus(_ context.Context, pollID string, status Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if poll, ok := f.polls[pollID]; ok {
		poll.Status = status
	}
	return nil
}

func (f *fakeStore) ClaimDecision(_ context.Context, requestID int, status Status) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.claimAttempts++
	if f.failClaim {
		return false, nil
	}

	found := false
	for _, poll := range f.polls {
		if poll.RequestID != requestID {
			continue
		}
		found = true
		if poll.Status == StatusOpen || poll.Status == StatusCanceled {
			poll.Status = status
			return true, nil
		}
	}
	// No poll at all means nothing to arbitrate, so the caller may proceed.
	return !found, nil
}

func (f *fakeStore) RecordVote(_ context.Context, pollID string, userID int,
	telegramID int64, optionID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.votes[pollID] == nil {
		f.votes[pollID] = make(map[int64]storedVote)
	}
	existing, replacing := f.votes[pollID][telegramID]
	seq := existing.seq
	if !replacing {
		f.seq++
		seq = f.seq
	}
	f.votes[pollID][telegramID] = storedVote{userID: userID, option: optionID, seq: seq}
	return nil
}

func (f *fakeStore) DeleteVote(_ context.Context, pollID string, telegramID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.votes[pollID], telegramID)
	return nil
}

// currentAdminUserIDs is the set of user IDs that still hold admin rights.
func (f *fakeStore) currentAdminUserIDs() map[int]bool {
	ids := make(map[int]bool, len(f.admins))
	for _, admin := range f.admins {
		ids[admin.ID] = true
	}
	return ids
}

func (f *fakeStore) Tally(_ context.Context, pollID string) (map[int]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.tallyErr != nil {
		return nil, f.tallyErr
	}

	admins := f.currentAdminUserIDs()
	tally := make(map[int]int)
	for _, vote := range f.votes[pollID] {
		if admins[vote.userID] {
			tally[vote.option]++
		}
	}
	return tally, nil
}

func (f *fakeStore) VoterNames(_ context.Context, pollID string, optionID int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	admins := f.currentAdminUserIDs()
	type entry struct {
		name string
		seq  int
	}
	var entries []entry
	for telegramID, vote := range f.votes[pollID] {
		if vote.option != optionID || !admins[vote.userID] {
			continue
		}
		entries = append(entries, entry{name: telegram.ResolveUserName(f.admins[telegramID]), seq: vote.seq})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].seq < entries[j].seq })

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.name)
	}
	return names, nil
}

func (f *fakeStore) VoteSummaries(_ context.Context, requestIDs []int) (map[int]*VoteSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	wanted := make(map[int]bool, len(requestIDs))
	for _, id := range requestIDs {
		wanted[id] = true
	}

	admins := f.currentAdminUserIDs()
	summaries := make(map[int]*VoteSummary)
	for _, poll := range f.polls {
		if !wanted[poll.RequestID] {
			continue
		}

		summary := &VoteSummary{Threshold: poll.Threshold, Status: poll.Status}
		summaries[poll.RequestID] = summary

		type entry struct {
			voter Voter
			vote  storedVote
		}
		var entries []entry
		for telegramID, vote := range f.votes[poll.PollID] {
			if !admins[vote.userID] {
				continue
			}
			entries = append(entries, entry{
				voter: Voter{Name: telegram.ResolveUserName(f.admins[telegramID])},
				vote:  vote,
			})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].vote.seq < entries[j].vote.seq })

		for _, e := range entries {
			if e.vote.option == telegram.OptionApprove {
				summary.Approve = append(summary.Approve, e.voter)
			} else {
				summary.Decline = append(summary.Decline, e.voter)
			}
		}
	}

	return summaries, nil
}

type stopCall struct {
	chatID    int64
	messageID int64
}

// fakeBot records outgoing Telegram calls instead of making them.
type fakeBot struct {
	mu sync.Mutex

	messages []telegram.SendMessageParams
	polls    []telegram.SendPollParams
	stops    []stopCall

	nextPollID    string
	nextMessageID int64
	sendMsgErr    error
	sendPollErr   error
}

func newFakeBot() *fakeBot {
	return &fakeBot{nextPollID: "poll-1", nextMessageID: 100}
}

func (b *fakeBot) SendMessage(_ context.Context,
	params telegram.SendMessageParams) (*telegram.SentMessage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.sendMsgErr != nil {
		return nil, b.sendMsgErr
	}
	b.messages = append(b.messages, params)
	b.nextMessageID++
	return &telegram.SentMessage{MessageID: b.nextMessageID}, nil
}

func (b *fakeBot) SendPoll(_ context.Context,
	params telegram.SendPollParams) (*telegram.SentPoll, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.sendPollErr != nil {
		return nil, b.sendPollErr
	}
	b.polls = append(b.polls, params)
	b.nextMessageID++
	return &telegram.SentPoll{
		PollID:    b.nextPollID,
		MessageID: b.nextMessageID,
		ChatID:    params.ChatID,
	}, nil
}

func (b *fakeBot) StopPoll(_ context.Context, chatID, messageID int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stops = append(b.stops, stopCall{chatID: chatID, messageID: messageID})
	return nil
}

func (b *fakeBot) stopCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.stops)
}

func (b *fakeBot) sentTexts() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	texts := make([]string, 0, len(b.messages))
	for _, m := range b.messages {
		texts = append(texts, m.Text)
	}
	return texts
}

// fakeDecider stands in for the requests package.
type fakeDecider struct {
	mu sync.Mutex

	req      *models.UpdateRequest
	missing  bool
	approved int
	declined int

	approveErr error
	declineErr error
}

func (d *fakeDecider) Load(int) (*models.UpdateRequest, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.missing {
		return nil, nil
	}
	return d.req, nil
}

func (d *fakeDecider) Approve(*models.UpdateRequest) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.approveErr != nil {
		return d.approveErr
	}
	d.approved++
	// Applying a request removes it, so later loads must not find it again.
	d.missing = true
	return nil
}

func (d *fakeDecider) Decline(*models.UpdateRequest) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.declineErr != nil {
		return d.declineErr
	}
	d.declined++
	d.missing = true
	return nil
}

func (d *fakeDecider) counts() (approved, declined int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.approved, d.declined
}

var errApplyFailed = errors.New("slug already in use")

// sevenAdmins is the deployment this feature was built for: seven admins, four votes.
var sevenAdmins = []int64{101, 102, 103, 104, 105, 106, 107}

func newRequest() *models.UpdateRequest {
	return &models.UpdateRequest{
		ID:          42,
		UserID:      7,
		RequestType: "create",
		ChangedFields: map[string]interface{}{
			"slug": "example",
			"name": "Example Site",
			"url":  "https://example.com",
		},
	}
}

// harness bundles a Manager with the fakes behind it and an already-open poll.
type harness struct {
	store   *fakeStore
	bot     *fakeBot
	decider *fakeDecider
	manager *Manager
	poll    Poll
}

func newHarness(t *testing.T, adminTelegramIDs []int64) *harness {
	t.Helper()

	store := newFakeStore(adminTelegramIDs...)
	bot := newFakeBot()
	decider := &fakeDecider{req: newRequest()}
	manager := NewManager(store, bot, decider, -1001)

	if err := manager.CreatePoll(context.Background(), decider.req, decider.req.User); err != nil {
		t.Fatalf("CreatePoll: %v", err)
	}

	stored, err := store.PollByID(context.Background(), bot.nextPollID)
	if err != nil || stored == nil {
		t.Fatalf("poll was not stored: %v", err)
	}

	return &harness{store: store, bot: bot, decider: decider, manager: manager, poll: *stored}
}

// vote casts one poll answer as the given Telegram user.
func (h *harness) vote(t *testing.T, telegramID int64, option int) error {
	t.Helper()
	return h.manager.HandlePollAnswer(context.Background(), &telegram.PollAnswer{
		PollID:    h.poll.PollID,
		User:      &telegram.APIUser{ID: telegramID},
		OptionIDs: []int{option},
	})
}

// retract casts an empty answer, which is how Telegram reports a withdrawn vote.
func (h *harness) retract(t *testing.T, telegramID int64) error {
	t.Helper()
	return h.manager.HandlePollAnswer(context.Background(), &telegram.PollAnswer{
		PollID:    h.poll.PollID,
		User:      &telegram.APIUser{ID: telegramID},
		OptionIDs: nil,
	})
}

// tally reads the current vote counts, failing the test if the store errors.
func (h *harness) tally(t *testing.T) map[int]int {
	t.Helper()
	tally, err := h.store.Tally(context.Background(), h.poll.PollID)
	if err != nil {
		t.Fatalf("Tally: %v", err)
	}
	return tally
}

func (h *harness) status(t *testing.T) Status {
	t.Helper()
	poll, err := h.store.PollByID(context.Background(), h.poll.PollID)
	if err != nil || poll == nil {
		t.Fatalf("poll lookup failed: %v", err)
	}
	return poll.Status
}
