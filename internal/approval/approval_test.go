package approval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"webring/internal/models"
	"webring/internal/telegram"
)

func TestThresholdIsSimpleMajority(t *testing.T) {
	cases := []struct {
		admins int
		want   int
	}{
		{admins: 0, want: 1},
		{admins: 1, want: 1},
		{admins: 2, want: 2},
		{admins: 3, want: 2},
		{admins: 4, want: 3},
		{admins: 5, want: 3},
		{admins: 6, want: 4},
		{admins: 7, want: 4},
		{admins: 9, want: 5},
	}

	for _, tc := range cases {
		if got := Threshold(tc.admins); got != tc.want {
			t.Errorf("Threshold(%d) = %d, want %d", tc.admins, got, tc.want)
		}
	}
}

func TestVotesBelowThresholdDoNotDecide(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	for _, tgID := range sevenAdmins[:3] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}

	approved, declined := h.decider.counts()
	if approved != 0 || declined != 0 {
		t.Errorf("3 of 7 votes decided the request: approved=%d declined=%d", approved, declined)
	}
	if got := h.status(t); got != StatusOpen {
		t.Errorf("poll status = %q, want %q", got, StatusOpen)
	}
	if h.bot.stopCount() != 0 {
		t.Errorf("poll was closed after %d votes", 3)
	}
}

func TestFourthApprovalDecidesAndClosesPoll(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	for _, tgID := range sevenAdmins[:4] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}

	approved, declined := h.decider.counts()
	if approved != 1 {
		t.Errorf("approved = %d, want 1", approved)
	}
	if declined != 0 {
		t.Errorf("declined = %d, want 0", declined)
	}
	if got := h.status(t); got != StatusApproved {
		t.Errorf("poll status = %q, want %q", got, StatusApproved)
	}
	if got := h.bot.stopCount(); got != 1 {
		t.Errorf("StopPoll called %d times, want exactly 1", got)
	}
}

func TestFourDeclinesDecline(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	for _, tgID := range sevenAdmins[:4] {
		if err := h.vote(t, tgID, telegram.OptionDecline); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}

	approved, declined := h.decider.counts()
	if declined != 1 || approved != 0 {
		t.Errorf("approved=%d declined=%d, want 0 and 1", approved, declined)
	}
	if got := h.status(t); got != StatusDeclined {
		t.Errorf("poll status = %q, want %q", got, StatusDeclined)
	}
}

func TestSplitVoteDoesNotDecide(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	// Three each way plus one abstention: nobody reaches four.
	for _, tgID := range sevenAdmins[:3] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}
	for _, tgID := range sevenAdmins[3:6] {
		if err := h.vote(t, tgID, telegram.OptionDecline); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}

	if approved, declined := h.decider.counts(); approved != 0 || declined != 0 {
		t.Errorf("split vote decided the request: approved=%d declined=%d", approved, declined)
	}
}

func TestNonAdminVotesAreIgnored(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	// Three real admins plus three outsiders would be six votes if outsiders counted.
	for _, tgID := range sevenAdmins[:3] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}
	for _, intruder := range []int64{9001, 9002, 9003} {
		if err := h.vote(t, intruder, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}

	if approved, _ := h.decider.counts(); approved != 0 {
		t.Errorf("non-admin votes reached the threshold")
	}

	tally, err := h.store.Tally(context.Background(), h.poll.PollID)
	if err != nil {
		t.Fatalf("Tally: %v", err)
	}
	if tally[telegram.OptionApprove] != 3 {
		t.Errorf("tally counted %d approvals, want 3 (outsiders must not be recorded)",
			tally[telegram.OptionApprove])
	}
}

func TestVoteFromChannelWithoutUserIsIgnored(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	err := h.manager.HandlePollAnswer(context.Background(), &telegram.PollAnswer{
		PollID:    h.poll.PollID,
		VoterChat: &telegram.APIChat{ID: -5000, Type: "channel"},
		OptionIDs: []int{telegram.OptionApprove},
	})
	if err != nil {
		t.Fatalf("HandlePollAnswer: %v", err)
	}

	tally := h.tally(t)
	if len(tally) != 0 {
		t.Errorf("channel vote was recorded: %v", tally)
	}
}

func TestChangedVoteMovesInsteadOfAdding(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	for _, tgID := range sevenAdmins[:3] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}

	// One admin switches sides. Approvals must drop to two, not stay at three.
	if err := h.vote(t, sevenAdmins[0], telegram.OptionDecline); err != nil {
		t.Fatalf("vote: %v", err)
	}

	tally := h.tally(t)
	if tally[telegram.OptionApprove] != 2 {
		t.Errorf("approvals = %d, want 2", tally[telegram.OptionApprove])
	}
	if tally[telegram.OptionDecline] != 1 {
		t.Errorf("declines = %d, want 1", tally[telegram.OptionDecline])
	}
}

func TestRetractedVoteFallsBackBelowThreshold(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	for _, tgID := range sevenAdmins[:3] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}
	if err := h.retract(t, sevenAdmins[0]); err != nil {
		t.Fatalf("retract: %v", err)
	}

	tally := h.tally(t)
	if tally[telegram.OptionApprove] != 2 {
		t.Fatalf("approvals after retraction = %d, want 2", tally[telegram.OptionApprove])
	}

	// The fourth admin now only brings the count back to three.
	if err := h.vote(t, sevenAdmins[3], telegram.OptionApprove); err != nil {
		t.Fatalf("vote: %v", err)
	}
	if approved, _ := h.decider.counts(); approved != 0 {
		t.Errorf("request was approved with only 3 standing votes")
	}
}

func TestRepeatedVoteForSameOptionCountsOnce(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	for i := 0; i < 4; i++ {
		if err := h.vote(t, sevenAdmins[0], telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}

	tally := h.tally(t)
	if tally[telegram.OptionApprove] != 1 {
		t.Errorf("one admin voting four times produced %d votes, want 1",
			tally[telegram.OptionApprove])
	}
	if approved, _ := h.decider.counts(); approved != 0 {
		t.Errorf("one admin was able to approve alone")
	}
}

// A restart replays updates Telegram never had acknowledged. The decision must not be
// applied a second time.
func TestRedeliveredVotesDoNotReapply(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	for _, tgID := range sevenAdmins[:4] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}
	if approved, _ := h.decider.counts(); approved != 1 {
		t.Fatalf("setup: approved = %d, want 1", approved)
	}

	for _, tgID := range sevenAdmins[:4] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("replayed vote: %v", err)
		}
	}

	if approved, _ := h.decider.counts(); approved != 1 {
		t.Errorf("approved = %d after replay, want 1", approved)
	}
	if got := h.bot.stopCount(); got != 1 {
		t.Errorf("StopPoll called %d times after replay, want 1", got)
	}
}

func TestVoteOnUnknownPollIsIgnored(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	err := h.manager.HandlePollAnswer(context.Background(), &telegram.PollAnswer{
		PollID:    "some-other-poll",
		User:      &telegram.APIUser{ID: sevenAdmins[0]},
		OptionIDs: []int{telegram.OptionApprove},
	})
	if err != nil {
		t.Fatalf("HandlePollAnswer: %v", err)
	}

	if approved, _ := h.decider.counts(); approved != 0 {
		t.Errorf("a vote on an unrelated poll decided our request")
	}
}

func TestVotesOnClosedPollAreIgnored(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	for _, tgID := range sevenAdmins[:4] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}

	// Votes cast just before the poll closed can still arrive afterwards.
	for _, tgID := range sevenAdmins[4:] {
		if err := h.vote(t, tgID, telegram.OptionDecline); err != nil {
			t.Fatalf("late vote: %v", err)
		}
	}

	approved, declined := h.decider.counts()
	if approved != 1 || declined != 0 {
		t.Errorf("late votes changed the outcome: approved=%d declined=%d", approved, declined)
	}
	if got := h.bot.stopCount(); got != 1 {
		t.Errorf("StopPoll called %d times, want 1", got)
	}
}

// The threshold is snapshotted when the poll opens, so growing the admin roster mid-vote
// must not move the goalposts.
func TestThresholdIsFrozenWhenPollIsCreated(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	if h.poll.Threshold != 4 {
		t.Fatalf("snapshot threshold = %d, want 4", h.poll.Threshold)
	}

	h.store.mu.Lock()
	for _, tgID := range []int64{108, 109} {
		h.store.admins[tgID] = &models.User{ID: int(tgID), TelegramID: tgID, IsAdmin: true}
	}
	h.store.mu.Unlock()

	for _, tgID := range sevenAdmins[:4] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}

	if approved, _ := h.decider.counts(); approved != 1 {
		t.Errorf("approved = %d, want 1 — nine admins must not raise a running poll to five",
			approved)
	}
}

// A vote is only as good as the voter's current standing: demoting an admin has to take
// their vote out of the count.
func TestDemotedAdminVoteStopsCounting(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	for _, tgID := range sevenAdmins[:3] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}
	h.store.demote(sevenAdmins[0])

	if err := h.vote(t, sevenAdmins[3], telegram.OptionApprove); err != nil {
		t.Fatalf("vote: %v", err)
	}

	if approved, _ := h.decider.counts(); approved != 0 {
		t.Errorf("a demoted admin's vote still counted towards the majority")
	}
}

func TestApprovalAnnouncesVotersToGroupAndAdmins(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	for _, tgID := range sevenAdmins[:4] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}

	var announcement *telegram.SendMessageParams
	h.bot.mu.Lock()
	for i := range h.bot.messages {
		if strings.Contains(h.bot.messages[i].Text, "Approved by") {
			announcement = &h.bot.messages[i]
			break
		}
	}
	h.bot.mu.Unlock()

	if announcement == nil {
		t.Fatalf("no approval announcement was sent; sent texts: %q", h.bot.sentTexts())
	}
	if announcement.ChatID != h.poll.ChatID {
		t.Errorf("announcement chat = %d, want the admin group %d", announcement.ChatID, h.poll.ChatID)
	}
	if announcement.ReplyToMessageID != h.poll.MessageID {
		t.Errorf("announcement replies to %d, want the poll message %d",
			announcement.ReplyToMessageID, h.poll.MessageID)
	}

	for i := 1; i <= 4; i++ {
		want := telegram.EscapeMarkdownV2("@admin" + string(rune('0'+i)))
		if !strings.Contains(announcement.Text, want) {
			t.Errorf("announcement is missing voter %s:\n%s", want, announcement.Text)
		}
	}
	if strings.Contains(announcement.Text, "@admin5") {
		t.Errorf("announcement lists an admin who did not vote:\n%s", announcement.Text)
	}

	// Every admin gets a copy on top of the group post.
	var dms int
	h.bot.mu.Lock()
	for _, m := range h.bot.messages {
		if m.ChatID > 0 && strings.Contains(m.Text, "Approved by") {
			dms++
		}
	}
	h.bot.mu.Unlock()
	if dms != len(sevenAdmins) {
		t.Errorf("sent %d direct messages, want %d", dms, len(sevenAdmins))
	}
}

func TestDeclineAnnouncementListsDecliners(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	for _, tgID := range sevenAdmins[:4] {
		if err := h.vote(t, tgID, telegram.OptionDecline); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}

	found := false
	for _, text := range h.bot.sentTexts() {
		if strings.Contains(text, "Declined by") {
			found = true
		}
	}
	if !found {
		t.Errorf("no decline announcement was sent; sent texts: %q", h.bot.sentTexts())
	}
}

// If applying the decision fails the request is still pending, so the poll must be handed
// back rather than left looking decided.
func TestFailedApplyCancelsPollAndReportsToGroup(t *testing.T) {
	h := newHarness(t, sevenAdmins)
	h.decider.approveErr = errApplyFailed

	var err error
	for _, tgID := range sevenAdmins[:4] {
		err = h.vote(t, tgID, telegram.OptionApprove)
	}

	if !errors.Is(err, errApplyFailed) {
		t.Errorf("HandlePollAnswer error = %v, want %v", err, errApplyFailed)
	}
	if got := h.status(t); got != StatusCanceled {
		t.Errorf("poll status = %q, want %q so the dashboard can still decide it", got, StatusCanceled)
	}

	found := false
	for _, text := range h.bot.sentTexts() {
		if strings.Contains(text, "could not be applied") {
			found = true
		}
	}
	if !found {
		t.Errorf("admins were not told the vote could not be applied: %q", h.bot.sentTexts())
	}
}

// A dashboard click that lands first wins; the vote must then apply nothing.
func TestVoteDoesNotApplyWhenRequestAlreadyGone(t *testing.T) {
	h := newHarness(t, sevenAdmins)
	h.decider.missing = true

	for _, tgID := range sevenAdmins[:4] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}

	if approved, declined := h.decider.counts(); approved != 0 || declined != 0 {
		t.Errorf("a vanished request was still decided: approved=%d declined=%d", approved, declined)
	}
	if got := h.status(t); got != StatusCanceled {
		t.Errorf("poll status = %q, want %q", got, StatusCanceled)
	}
	if got := h.bot.stopCount(); got != 1 {
		t.Errorf("StopPoll called %d times, want 1", got)
	}
}

func TestLosingClaimSkipsApplying(t *testing.T) {
	h := newHarness(t, sevenAdmins)
	h.store.failClaim = true

	for _, tgID := range sevenAdmins[:4] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}

	if approved, _ := h.decider.counts(); approved != 0 {
		t.Errorf("applied a decision the manager did not win the claim for")
	}
	if h.store.claimAttempts == 0 {
		t.Errorf("no claim was attempted")
	}
}
