package approval

import (
	"context"
	"testing"

	"webring/internal/telegram"
)

func TestVoteSummaryProgressHelpers(t *testing.T) {
	cases := []struct {
		name          string
		summary       *VoteSummary
		wantLeading   int
		wantRemaining int
		wantOpen      bool
	}{
		{
			name:          "nil summary",
			summary:       nil,
			wantLeading:   0,
			wantRemaining: 0,
			wantOpen:      false,
		},
		{
			name:          "no votes yet",
			summary:       &VoteSummary{Threshold: 4, Status: StatusOpen},
			wantLeading:   0,
			wantRemaining: 4,
			wantOpen:      true,
		},
		{
			name: "approvals lead",
			summary: &VoteSummary{Threshold: 4, Status: StatusOpen,
				Approve: make([]Voter, 3), Decline: make([]Voter, 1)},
			wantLeading:   3,
			wantRemaining: 1,
			wantOpen:      true,
		},
		{
			name: "declines lead",
			summary: &VoteSummary{Threshold: 4, Status: StatusOpen,
				Approve: make([]Voter, 1), Decline: make([]Voter, 2)},
			wantLeading:   2,
			wantRemaining: 2,
			wantOpen:      true,
		},
		{
			name: "tied",
			summary: &VoteSummary{Threshold: 4, Status: StatusOpen,
				Approve: make([]Voter, 2), Decline: make([]Voter, 2)},
			wantLeading:   2,
			wantRemaining: 2,
			wantOpen:      true,
		},
		{
			name: "decided",
			summary: &VoteSummary{Threshold: 4, Status: StatusApproved,
				Approve: make([]Voter, 4)},
			wantLeading:   4,
			wantRemaining: 0,
			wantOpen:      false,
		},
		{
			// A threshold that shrank below the standing votes must not read as negative.
			name: "more votes than the threshold",
			summary: &VoteSummary{Threshold: 2, Status: StatusOpen,
				Approve: make([]Voter, 5)},
			wantLeading:   5,
			wantRemaining: 0,
			wantOpen:      true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.summary.Leading(); got != tc.wantLeading {
				t.Errorf("Leading() = %d, want %d", got, tc.wantLeading)
			}
			if got := tc.summary.Remaining(); got != tc.wantRemaining {
				t.Errorf("Remaining() = %d, want %d", got, tc.wantRemaining)
			}
			if got := tc.summary.Open(); got != tc.wantOpen {
				t.Errorf("Open() = %v, want %v", got, tc.wantOpen)
			}
		})
	}
}

func TestVoteSummariesReportsBothSides(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	for _, tgID := range sevenAdmins[:2] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}
	if err := h.vote(t, sevenAdmins[2], telegram.OptionDecline); err != nil {
		t.Fatalf("vote: %v", err)
	}

	summaries := h.manager.VoteSummaries(context.Background(), []int{h.poll.RequestID})
	summary := summaries[h.poll.RequestID]
	if summary == nil {
		t.Fatalf("no summary for request %d", h.poll.RequestID)
	}

	if summary.Threshold != 4 {
		t.Errorf("Threshold = %d, want 4", summary.Threshold)
	}
	if len(summary.Approve) != 2 {
		t.Errorf("Approve = %v, want 2 voters", summary.Approve)
	}
	if len(summary.Decline) != 1 {
		t.Errorf("Decline = %v, want 1 voter", summary.Decline)
	}
	if !summary.Open() {
		t.Errorf("summary reports closed while the poll is still running")
	}
	if summary.Remaining() != 2 {
		t.Errorf("Remaining() = %d, want 2", summary.Remaining())
	}
	if summary.Approve[0].Name != "@admin1" {
		t.Errorf("first approver = %q, want @admin1", summary.Approve[0].Name)
	}
}

func TestVoteSummariesOmitsRequestsWithoutAPoll(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	summaries := h.manager.VoteSummaries(context.Background(), []int{h.poll.RequestID, 9999})
	if _, ok := summaries[9999]; ok {
		t.Errorf("a request with no poll got a summary")
	}
	if _, ok := summaries[h.poll.RequestID]; !ok {
		t.Errorf("the request with a poll is missing its summary")
	}
}

// A poll that nobody has voted in yet must still render, showing 0 / threshold.
func TestVoteSummariesIncludesPollsWithNoVotes(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	summaries := h.manager.VoteSummaries(context.Background(), []int{h.poll.RequestID})
	summary := summaries[h.poll.RequestID]
	if summary == nil {
		t.Fatalf("a poll with no votes produced no summary")
	}
	if summary.Leading() != 0 || summary.Threshold != 4 {
		t.Errorf("summary = %+v, want 0 of 4", summary)
	}
}

func TestVoteSummariesExcludesDemotedAdmins(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	for _, tgID := range sevenAdmins[:2] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}
	h.store.demote(sevenAdmins[0])

	summary := h.manager.VoteSummaries(context.Background(), []int{h.poll.RequestID})[h.poll.RequestID]
	if summary == nil {
		t.Fatalf("no summary")
	}
	if len(summary.Approve) != 1 {
		t.Errorf("Approve = %v, want only the remaining admin", summary.Approve)
	}
}

func TestVoteSummariesReflectsADecidedPoll(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	for _, tgID := range sevenAdmins[:4] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}

	summary := h.manager.VoteSummaries(context.Background(), []int{h.poll.RequestID})[h.poll.RequestID]
	if summary == nil {
		t.Fatalf("no summary")
	}
	if summary.Open() {
		t.Errorf("a decided poll still reports as open")
	}
	if summary.Status != StatusApproved {
		t.Errorf("Status = %q, want %q", summary.Status, StatusApproved)
	}
	if summary.Remaining() != 0 {
		t.Errorf("Remaining() = %d, want 0", summary.Remaining())
	}
}

// With no admin chat configured there are no polls, and the dashboard must not be handed
// a half-populated map.
func TestVoteSummariesIsEmptyWhenPollsAreDisabled(t *testing.T) {
	manager := NewManager(newFakeStore(sevenAdmins...), newFakeBot(), &fakeDecider{}, 0)

	if got := manager.VoteSummaries(context.Background(), []int{1, 2, 3}); len(got) != 0 {
		t.Errorf("VoteSummaries = %v, want empty when polls are disabled", got)
	}
}

func TestVoteSummariesOnNilManagerIsEmpty(t *testing.T) {
	var manager *Manager

	if got := manager.VoteSummaries(context.Background(), []int{1}); len(got) != 0 {
		t.Errorf("VoteSummaries = %v, want empty", got)
	}
}

func TestVoteSummariesWithNoRequestsMakesNoQuery(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	if got := h.manager.VoteSummaries(context.Background(), nil); len(got) != 0 {
		t.Errorf("VoteSummaries = %v, want empty", got)
	}
}
