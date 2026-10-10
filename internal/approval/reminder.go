package approval

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"webring/internal/telegram"
)

const (
	DefaultReminderInterval = 24 * time.Hour
	reminderCheckInterval   = 5 * time.Minute
	minReminderInterval     = reminderCheckInterval
	reminderIntervalEnvVar  = "POLL_REMINDER_INTERVAL"
)

func ReminderInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv(reminderIntervalEnvVar))
	if raw == "" {
		return DefaultReminderInterval
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < minReminderInterval {
		log.Printf("Invalid %s value; falling back to %s", reminderIntervalEnvVar, DefaultReminderInterval)
		return DefaultReminderInterval
	}
	return parsed
}

func (m *Manager) SendReminders(ctx context.Context, interval time.Duration) error {
	if !m.Enabled() {
		return nil
	}

	polls, err := m.store.ClaimDueReminders(ctx, interval)
	if err != nil {
		return fmt.Errorf("claiming due reminders: %w", err)
	}

	for i := range polls {
		m.remind(ctx, &polls[i])
	}
	return nil
}

func (m *Manager) remind(ctx context.Context, poll *Poll) {
	if poll.RequestID == 0 {
		if err := m.store.SetStatus(ctx, poll.PollID, StatusCanceled); err != nil {
			log.Printf("Error canceling poll %s: %v", poll.PollID, err)
		}
		m.ClosePoll(ctx, poll)
		return
	}

	req, err := m.decider.Load(poll.RequestID)
	if err != nil {
		log.Printf("Error loading request %d for a poll reminder: %v", poll.RequestID, err)
		return
	}
	if req == nil {
		return
	}

	tally, err := m.store.Tally(ctx, poll.PollID)
	if err != nil {
		log.Printf("Error tallying poll %s for a reminder: %v", poll.PollID, err)
		return
	}

	pending, err := m.store.PendingVoters(ctx, poll.PollID)
	if err != nil {
		log.Printf("Error listing admins yet to vote on poll %s: %v", poll.PollID, err)
		return
	}

	text := telegram.RenderMessage("poll_reminder", map[string]interface{}{
		"RequestID": req.ID,
		"SiteName":  telegram.RequestSiteName(req),
		"Approve":   tally[telegram.OptionApprove],
		"Decline":   tally[telegram.OptionDecline],
		"Threshold": poll.Threshold,
		"Pending":   pending,
	})
	if text == "" {
		return
	}

	m.send(ctx, telegram.SendMessageParams{
		ChatID:           poll.ChatID,
		Text:             text,
		ReplyToMessageID: poll.MessageID,
	})
}

type Reminder struct {
	manager  *Manager
	interval time.Duration

	wait func(ctx context.Context, d time.Duration)
}

func NewReminder(manager *Manager, interval time.Duration) *Reminder {
	return &Reminder{manager: manager, interval: interval, wait: sleepCtx}
}

func (r *Reminder) Run(ctx context.Context) {
	log.Printf("Telegram poll reminders started, every %s", r.interval)

	for ctx.Err() == nil {
		if err := r.manager.SendReminders(ctx, r.interval); err != nil {
			log.Printf("Error sending poll reminders: %v", err)
		}
		r.wait(ctx, reminderCheckInterval)
	}

	log.Printf("Telegram poll reminders stopped")
}
