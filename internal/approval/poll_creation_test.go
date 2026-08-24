package approval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"webring/internal/models"
	"webring/internal/telegram"
)

func TestCreatePollIsSkippedWithoutAdminChat(t *testing.T) {
	store := newFakeStore(sevenAdmins...)
	bot := newFakeBot()
	manager := NewManager(store, bot, &fakeDecider{}, 0)

	if manager.Enabled() {
		t.Fatalf("manager reports enabled without an admin chat ID")
	}
	if err := manager.CreatePoll(context.Background(), newRequest(), nil); err != nil {
		t.Fatalf("CreatePoll: %v", err)
	}

	if len(bot.polls) != 0 {
		t.Errorf("sent %d polls with polling disabled, want 0", len(bot.polls))
	}
	if len(store.polls) != 0 {
		t.Errorf("stored %d polls with polling disabled, want 0", len(store.polls))
	}
}

func TestNilManagerIsSafeToUse(t *testing.T) {
	var manager *Manager

	if manager.Enabled() {
		t.Errorf("nil manager reports enabled")
	}
	if err := manager.CreatePoll(context.Background(), newRequest(), nil); err != nil {
		t.Errorf("CreatePoll on nil manager: %v", err)
	}
	if poll := manager.PollForRequest(context.Background(), 1); poll != nil {
		t.Errorf("PollForRequest on nil manager returned %v", poll)
	}
	// A nil manager means polls are off, so the caller owns the decision outright.
	claimed, err := manager.Claim(context.Background(), 1, StatusApproved)
	if err != nil || !claimed {
		t.Errorf("Claim on nil manager = (%v, %v), want (true, nil)", claimed, err)
	}
	manager.ClosePoll(context.Background(), &Poll{MessageID: 1})
	manager.Release(context.Background(), &Poll{PollID: "x"})
}

func TestCreatePollSendsNonAnonymousPollBelowTheDetails(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	if len(h.bot.polls) != 1 {
		t.Fatalf("sent %d polls, want 1", len(h.bot.polls))
	}
	sent := h.bot.polls[0]

	// Anonymous polls emit no poll_answer updates, so votes could never be counted.
	if sent.IsAnonymous {
		t.Errorf("poll was sent as anonymous; votes would be invisible to the bot")
	}
	if sent.ChatID != -1001 {
		t.Errorf("poll chat = %d, want the configured admin chat -1001", sent.ChatID)
	}
	if want := telegram.PollOptions(); len(sent.Options) != len(want) {
		t.Fatalf("poll has %d options, want %d", len(sent.Options), len(want))
	}
	if sent.Options[telegram.OptionApprove] != "Approve" ||
		sent.Options[telegram.OptionDecline] != "Decline" {
		t.Errorf("poll options = %q, want Approve then Decline in that order", sent.Options)
	}
	if sent.ReplyToMessageID == 0 {
		t.Errorf("poll does not reply to the request details message")
	}
	if !strings.Contains(sent.Question, "Example Site") {
		t.Errorf("poll question %q does not name the site", sent.Question)
	}

	// The details message must go out first so the poll can hang under it.
	if len(h.bot.messages) == 0 {
		t.Fatalf("no request details were posted to the admin chat")
	}
	if !strings.Contains(h.bot.messages[0].Text, "example") {
		t.Errorf("details message does not describe the request:\n%s", h.bot.messages[0].Text)
	}
}

func TestCreatePollStoresThresholdAndIdentifiers(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	if h.poll.Threshold != 4 {
		t.Errorf("stored threshold = %d, want 4 for seven admins", h.poll.Threshold)
	}
	if h.poll.RequestID != 42 {
		t.Errorf("stored request ID = %d, want 42", h.poll.RequestID)
	}
	if h.poll.Status != StatusOpen {
		t.Errorf("stored status = %q, want %q", h.poll.Status, StatusOpen)
	}
	if h.poll.MessageID == 0 || h.poll.PollID == "" {
		t.Errorf("stored poll is missing identifiers needed to close it: %+v", h.poll)
	}
}

// The details message is a nicety; losing it must not cost us the vote itself.
func TestCreatePollContinuesWhenDetailsFailToSend(t *testing.T) {
	store := newFakeStore(sevenAdmins...)
	bot := newFakeBot()
	bot.sendMsgErr = errors.New("chat not found")
	manager := NewManager(store, bot, &fakeDecider{}, -1001)

	if err := manager.CreatePoll(context.Background(), newRequest(), nil); err != nil {
		t.Fatalf("CreatePoll: %v", err)
	}

	if len(bot.polls) != 1 {
		t.Fatalf("sent %d polls, want 1", len(bot.polls))
	}
	if bot.polls[0].ReplyToMessageID != 0 {
		t.Errorf("poll replies to a message that was never sent")
	}
}

func TestCreatePollFailsWhenPollCannotBeSent(t *testing.T) {
	store := newFakeStore(sevenAdmins...)
	bot := newFakeBot()
	bot.sendPollErr = errors.New("not enough rights to send polls")
	manager := NewManager(store, bot, &fakeDecider{}, -1001)

	err := manager.CreatePoll(context.Background(), newRequest(), nil)
	if err == nil {
		t.Fatalf("CreatePoll succeeded despite sendPoll failing")
	}
	if len(store.polls) != 0 {
		t.Errorf("stored a poll that was never sent")
	}
}

func TestPollQuestionDescribesBothRequestTypes(t *testing.T) {
	create := PollQuestion(newRequest())
	if !strings.Contains(create, "add site") || !strings.Contains(create, "Example Site") {
		t.Errorf("create question = %q", create)
	}

	siteID := 3
	update := PollQuestion(&models.UpdateRequest{
		ID:          7,
		SiteID:      &siteID,
		RequestType: "update",
		Site:        &models.Site{Name: "Old Site", Slug: "old"},
	})
	if !strings.Contains(update, "update site") || !strings.Contains(update, "Old Site") {
		t.Errorf("update question = %q", update)
	}
	if !strings.Contains(update, "#7") {
		t.Errorf("question %q does not identify the request", update)
	}
}

// Telegram shows the question as plain text, so MarkdownV2 escapes would be displayed
// verbatim rather than interpreted.
func TestPollQuestionIsNotMarkdownEscaped(t *testing.T) {
	req := newRequest()
	req.ChangedFields["name"] = "a_b.c"

	if got := PollQuestion(req); strings.Contains(got, `\_`) || strings.Contains(got, `\.`) {
		t.Errorf("poll question contains MarkdownV2 escapes: %q", got)
	}
}

func TestPollForRequestFindsTheOpenPoll(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	poll := h.manager.PollForRequest(context.Background(), 42)
	if poll == nil {
		t.Fatalf("PollForRequest returned nil for a request with an open poll")
	}
	if poll.PollID != h.poll.PollID {
		t.Errorf("PollForRequest returned poll %q, want %q", poll.PollID, h.poll.PollID)
	}

	if other := h.manager.PollForRequest(context.Background(), 999); other != nil {
		t.Errorf("PollForRequest invented a poll for an unknown request: %+v", other)
	}
}

// Release is what lets the dashboard retry after a failed apply.
func TestReleaseReopensThePoll(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	claimed, err := h.manager.Claim(context.Background(), 42, StatusApproved)
	if err != nil || !claimed {
		t.Fatalf("Claim = (%v, %v), want (true, nil)", claimed, err)
	}
	if got := h.status(t); got != StatusApproved {
		t.Fatalf("status after claim = %q", got)
	}

	h.manager.Release(context.Background(), &h.poll)

	if got := h.status(t); got != StatusOpen {
		t.Errorf("status after release = %q, want %q", got, StatusOpen)
	}
	claimed, err = h.manager.Claim(context.Background(), 42, StatusDeclined)
	if err != nil || !claimed {
		t.Errorf("released request could not be claimed again: (%v, %v)", claimed, err)
	}
}

func TestClaimIsGrantedOnlyOnce(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	first, err := h.manager.Claim(context.Background(), 42, StatusApproved)
	if err != nil || !first {
		t.Fatalf("first claim = (%v, %v), want (true, nil)", first, err)
	}

	second, err := h.manager.Claim(context.Background(), 42, StatusDeclined)
	if err != nil {
		t.Fatalf("second claim errored: %v", err)
	}
	if second {
		t.Errorf("the same request was claimed twice")
	}
}

// Requests created while polls were disabled have no poll row, and the dashboard must
// still be able to decide them.
func TestClaimSucceedsForRequestWithoutPoll(t *testing.T) {
	store := newFakeStore(sevenAdmins...)
	manager := NewManager(store, newFakeBot(), &fakeDecider{}, -1001)

	claimed, err := manager.Claim(context.Background(), 1234, StatusApproved)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !claimed {
		t.Errorf("a request with no poll could not be claimed")
	}
}
