package approval

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"webring/internal/telegram"
)

// fakeFetcher replays a scripted sequence of getUpdates results and records the offsets
// it was asked for. It cancels the context once the script runs out so Run terminates.
type fakeFetcher struct {
	mu sync.Mutex

	responses []fetchResult
	calls     int
	offsets   []int64
	allowed   [][]string

	cancel context.CancelFunc
}

type fetchResult struct {
	updates []telegram.Update
	err     error
}

func (f *fakeFetcher) GetUpdates(_ context.Context, offset int64,
	allowed []string) ([]telegram.Update, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.offsets = append(f.offsets, offset)
	f.allowed = append(f.allowed, allowed)

	if f.calls >= len(f.responses) {
		f.cancel()
		return nil, nil
	}

	result := f.responses[f.calls]
	f.calls++
	return result.updates, result.err
}

// runScript drives a Runner through a fixed set of getUpdates results and returns the
// offsets that were requested.
func runScript(t *testing.T, manager *Manager, responses []fetchResult) ([]int64, []time.Duration) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fetcher := &fakeFetcher{responses: responses, cancel: cancel}
	runner := NewRunner(fetcher, manager)

	// Record the backoff durations instead of waiting them out.
	var slept []time.Duration
	runner.sleep = func(_ context.Context, d time.Duration) {
		slept = append(slept, d)
	}

	done := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Runner did not stop when its context was canceled")
	}

	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()
	return fetcher.offsets, slept
}

func pollAnswerUpdate(updateID int64, pollID string, telegramID int64, option int) telegram.Update {
	return telegram.Update{
		UpdateID: updateID,
		PollAnswer: &telegram.PollAnswer{
			PollID:    pollID,
			User:      &telegram.APIUser{ID: telegramID},
			OptionIDs: []int{option},
		},
	}
}

func TestRunnerAdvancesOffsetPastProcessedUpdates(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	offsets, _ := runScript(t, h.manager, []fetchResult{
		{updates: []telegram.Update{
			pollAnswerUpdate(10, h.poll.PollID, sevenAdmins[0], telegram.OptionApprove),
			pollAnswerUpdate(11, h.poll.PollID, sevenAdmins[1], telegram.OptionApprove),
		}},
		{updates: []telegram.Update{
			pollAnswerUpdate(12, h.poll.PollID, sevenAdmins[2], telegram.OptionApprove),
		}},
	})

	want := []int64{0, 12, 13}
	if len(offsets) != len(want) {
		t.Fatalf("offsets = %v, want %v", offsets, want)
	}
	for i, w := range want {
		if offsets[i] != w {
			t.Errorf("offset[%d] = %d, want %d (offsets: %v)", i, offsets[i], w, offsets)
		}
	}

	tally := h.tally(t)
	if tally[telegram.OptionApprove] != 3 {
		t.Errorf("recorded %d votes, want 3", tally[telegram.OptionApprove])
	}
}

// A failed fetch returned nothing, so its updates must be requested again.
func TestRunnerKeepsOffsetAfterFetchError(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	offsets, slept := runScript(t, h.manager, []fetchResult{
		{updates: []telegram.Update{
			pollAnswerUpdate(5, h.poll.PollID, sevenAdmins[0], telegram.OptionApprove),
		}},
		{err: errors.New("connection reset")},
		{err: errors.New("connection reset")},
	})

	want := []int64{0, 6, 6, 6}
	if len(offsets) != len(want) {
		t.Fatalf("offsets = %v, want %v", offsets, want)
	}
	for i, w := range want {
		if offsets[i] != w {
			t.Errorf("offset[%d] = %d, want %d (offsets: %v)", i, offsets[i], w, offsets)
		}
	}

	if len(slept) != 2 {
		t.Fatalf("backed off %d times, want 2", len(slept))
	}
	if slept[0] != initialBackoff {
		t.Errorf("first backoff = %v, want %v", slept[0], initialBackoff)
	}
	if slept[1] <= slept[0] {
		t.Errorf("backoff did not grow: %v then %v", slept[0], slept[1])
	}
}

func TestRunnerBackoffResetsAfterSuccess(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	_, slept := runScript(t, h.manager, []fetchResult{
		{err: errors.New("boom")},
		{err: errors.New("boom")},
		{updates: nil},
		{err: errors.New("boom")},
	})

	if len(slept) != 3 {
		t.Fatalf("backed off %d times, want 3", len(slept))
	}
	if slept[2] != initialBackoff {
		t.Errorf("backoff after a successful fetch = %v, want a reset to %v", slept[2], initialBackoff)
	}
}

func TestRunnerBackoffIsCapped(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	responses := make([]fetchResult, 20)
	for i := range responses {
		responses[i] = fetchResult{err: errors.New("boom")}
	}

	_, slept := runScript(t, h.manager, responses)

	for i, d := range slept {
		if d > maxBackoff {
			t.Fatalf("backoff[%d] = %v, exceeds the %v cap", i, d, maxBackoff)
		}
	}
	if slept[len(slept)-1] != maxBackoff {
		t.Errorf("final backoff = %v, want the %v cap", slept[len(slept)-1], maxBackoff)
	}
}

// 409 means a webhook is set or another process is polling. The loop must survive it
// rather than spin, because it will keep happening until an operator intervenes.
func TestRunnerSurvivesConflictFromWebhook(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	conflict := &telegram.APIError{
		Method:      "getUpdates",
		Code:        http.StatusConflict,
		Description: "Conflict: can't use getUpdates method while webhook is active",
	}
	if !conflict.IsConflict() {
		t.Fatalf("APIError with code 409 is not reported as a conflict")
	}

	offsets, slept := runScript(t, h.manager, []fetchResult{
		{err: conflict},
		{err: conflict},
	})

	if len(offsets) != 3 {
		t.Errorf("made %d fetches, want 3", len(offsets))
	}
	if len(slept) != 2 {
		t.Errorf("backed off %d times on conflict, want 2", len(slept))
	}
}

func TestRunnerRequestsOnlyPollAnswers(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fetcher := &fakeFetcher{cancel: cancel}
	runner := NewRunner(fetcher, h.manager)
	runner.sleep = func(context.Context, time.Duration) {}
	runner.Run(ctx)

	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()
	if len(fetcher.allowed) == 0 {
		t.Fatalf("no fetch was made")
	}
	got := fetcher.allowed[0]
	if len(got) != 1 || got[0] != "poll_answer" {
		t.Errorf("allowed_updates = %v, want [poll_answer]", got)
	}
}

// An update the handler chokes on must not wedge the loop into replaying it forever.
func TestRunnerAdvancesPastUpdatesThatFailToHandle(t *testing.T) {
	h := newHarness(t, sevenAdmins)
	h.store.tallyErr = errors.New("database is down")

	offsets, _ := runScript(t, h.manager, []fetchResult{
		{updates: []telegram.Update{
			pollAnswerUpdate(77, h.poll.PollID, sevenAdmins[0], telegram.OptionApprove),
		}},
	})

	if len(offsets) < 2 || offsets[1] != 78 {
		t.Errorf("offsets = %v, want the failing update to be skipped past (78)", offsets)
	}
}

// End to end through the loop: four decline votes arriving as updates must decline the
// request and close the poll.
func TestRunnerCarriesAVoteThroughToTheDecision(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	var updates []telegram.Update
	for i, tgID := range sevenAdmins[:4] {
		updates = append(updates, pollAnswerUpdate(int64(100+i), h.poll.PollID, tgID, telegram.OptionDecline))
	}

	runScript(t, h.manager, []fetchResult{{updates: updates}})

	approved, declined := h.decider.counts()
	if declined != 1 || approved != 0 {
		t.Errorf("approved=%d declined=%d, want 0 and 1", approved, declined)
	}
	if got := h.status(t); got != StatusDeclined {
		t.Errorf("poll status = %q, want %q", got, StatusDeclined)
	}
	if got := h.bot.stopCount(); got != 1 {
		t.Errorf("StopPoll called %d times, want 1", got)
	}
}

func TestRunnerIgnoresUpdatesWithoutPollAnswers(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	offsets, _ := runScript(t, h.manager, []fetchResult{
		{updates: []telegram.Update{{UpdateID: 3}, {UpdateID: 4}}},
	})

	if len(offsets) < 2 || offsets[1] != 5 {
		t.Errorf("offsets = %v, want the offset to advance to 5", offsets)
	}
	if approved, _ := h.decider.counts(); approved != 0 {
		t.Errorf("a non-vote update decided the request")
	}
}
