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

	if len(pending) == 0 && m.settledByReminder(ctx, poll, tally) {
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

func (m *Manager) settledByReminder(ctx context.Context, poll *Poll, tally map[int]int) bool {
	if err := m.settle(ctx, poll, tally); err != nil {
		log.Printf("Error settling poll %s from a reminder: %v", poll.PollID, err)
		return true
	}

	current, err := m.store.PollByID(ctx, poll.PollID)
	if err != nil {
		log.Printf("Error rereading poll %s after settling it: %v", poll.PollID, err)
		return true
	}
	return current == nil || current.Status != StatusOpen
}

func (m *Manager) RunReminders(ctx context.Context, interval time.Duration) {
	log.Printf("Telegram poll reminders started, every %s", interval)

	for ctx.Err() == nil {
		if err := m.SendReminders(ctx, interval); err != nil {
			log.Printf("Error sending poll reminders: %v", err)
		}
		sleepCtx(ctx, reminderCheckInterval)
	}

	log.Printf("Telegram poll reminders stopped")
}
