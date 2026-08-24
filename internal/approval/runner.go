package approval

import (
	"context"
	"errors"
	"log"
	"time"

	"webring/internal/telegram"
)

const (
	initialBackoff = time.Second
	maxBackoff     = time.Minute
)

// pollAnswerUpdates limits getUpdates to the only update type this service consumes.
var pollAnswerUpdates = []string{"poll_answer"}

// Fetcher retrieves updates from Telegram.
type Fetcher interface {
	GetUpdates(ctx context.Context, offset int64, allowed []string) ([]telegram.Update, error)
}

// Runner long-polls Telegram for votes and feeds them to a Manager.
//
// The Bot API has no way to read a poll's current state, so votes are only observable as
// poll_answer updates. This loop is what "polls the poll".
type Runner struct {
	fetcher Fetcher
	manager *Manager
	offset  int64
	backoff time.Duration

	// sleep is overridable so tests do not wait out real backoff.
	sleep func(ctx context.Context, d time.Duration)
}

func NewRunner(fetcher Fetcher, manager *Manager) *Runner {
	return &Runner{fetcher: fetcher, manager: manager, sleep: sleepCtx}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// Run consumes updates until the context is canceled.
func (r *Runner) Run(ctx context.Context) {
	log.Printf("Telegram approval poll listener started")

	for ctx.Err() == nil {
		updates, err := r.fetcher.GetUpdates(ctx, r.offset, pollAnswerUpdates)
		if err != nil {
			r.handleFetchError(ctx, err)
			continue
		}

		r.backoff = 0
		for i := range updates {
			r.process(ctx, &updates[i])
		}
	}

	log.Printf("Telegram approval poll listener stopped")
}

func (r *Runner) process(ctx context.Context, update *telegram.Update) {
	// Advance the offset before handling so a single bad update cannot wedge the loop
	// into replaying it forever.
	if update.UpdateID >= r.offset {
		r.offset = update.UpdateID + 1
	}

	if update.PollAnswer == nil {
		return
	}

	if err := r.manager.HandlePollAnswer(ctx, update.PollAnswer); err != nil {
		log.Printf("Error handling poll answer for poll %s: %v", update.PollAnswer.PollID, err)
	}
}

// handleFetchError backs off without advancing the offset, so updates that were never
// successfully fetched are not skipped.
func (r *Runner) handleFetchError(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}

	var apiErr *telegram.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.IsConflict():
		log.Printf("Telegram getUpdates conflict: something else owns this bot's updates. "+
			"Either a webhook is set (call deleteWebhook) or another process is polling "+
			"with the same token. Votes will not be received until that is resolved. (%v)", err)
	default:
		log.Printf("Error fetching Telegram updates: %v", err)
	}

	if r.backoff == 0 {
		r.backoff = initialBackoff
	} else if r.backoff < maxBackoff {
		r.backoff *= 2
		if r.backoff > maxBackoff {
			r.backoff = maxBackoff
		}
	}

	r.sleep(ctx, r.backoff)
}
