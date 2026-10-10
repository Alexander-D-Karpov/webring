package approval

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"webring/internal/telegram"
)

const day = 24 * time.Hour

func reminderMessages(bot *fakeBot) []telegram.SendMessageParams {
	var found []telegram.SendMessageParams
	for _, m := range bot.sent() {
		if strings.Contains(m.Text, "Not Enough Votes Yet") {
			found = append(found, m)
		}
	}
	return found
}

func mentionOf(telegramID int64) string {
	return fmt.Sprintf("(tg://user?id=%d)", telegramID)
}

func (h *harness) remind(t *testing.T) {
	t.Helper()
	if err := h.manager.SendReminders(context.Background(), day); err != nil {
		t.Fatalf("SendReminders: %v", err)
	}
}

func TestNoReminderBeforeTheIntervalPasses(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	h.store.advance(day - time.Minute)
	h.remind(t)

	if got := reminderMessages(h.bot); len(got) != 0 {
		t.Errorf("sent %d reminders for a poll open less than a day", len(got))
	}
}

func TestReminderPingsOnlyAdminsWhoHaveNotVoted(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	for _, tgID := range sevenAdmins[:2] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}
	if err := h.vote(t, sevenAdmins[2], telegram.OptionDecline); err != nil {
		t.Fatalf("vote: %v", err)
	}

	h.store.advance(day)
	h.remind(t)

	got := reminderMessages(h.bot)
	if len(got) != 1 {
		t.Fatalf("sent %d reminders, want 1", len(got))
	}
	reminder := got[0]

	if reminder.ChatID != h.poll.ChatID {
		t.Errorf("reminder went to chat %d, want the poll's chat %d", reminder.ChatID, h.poll.ChatID)
	}
	if reminder.ReplyToMessageID != h.poll.MessageID {
		t.Errorf("reminder replies to %d, want the poll message %d", reminder.ReplyToMessageID, h.poll.MessageID)
	}
	for _, tgID := range sevenAdmins[3:] {
		if !strings.Contains(reminder.Text, mentionOf(tgID)) {
			t.Errorf("reminder does not ping admin %d:\n%s", tgID, reminder.Text)
		}
	}
	for _, tgID := range sevenAdmins[:3] {
		if strings.Contains(reminder.Text, mentionOf(tgID)) {
			t.Errorf("reminder pings admin %d, who has already voted:\n%s", tgID, reminder.Text)
		}
	}
	for _, want := range []string{`\#42`, "Example Site", "2 of 4", "1 of 4"} {
		if !strings.Contains(reminder.Text, want) {
			t.Errorf("reminder is missing %q:\n%s", want, reminder.Text)
		}
	}
}

func TestReminderRepeatsOncePerInterval(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	h.store.advance(day)
	h.remind(t)
	h.remind(t)
	if got := len(reminderMessages(h.bot)); got != 1 {
		t.Fatalf("sent %d reminders on the first day, want 1", got)
	}

	h.store.advance(day - time.Minute)
	h.remind(t)
	if got := len(reminderMessages(h.bot)); got != 1 {
		t.Errorf("sent %d reminders before a second day passed, want 1", got)
	}

	h.store.advance(time.Minute)
	h.remind(t)
	if got := len(reminderMessages(h.bot)); got != 2 {
		t.Errorf("sent %d reminders after two days, want 2", got)
	}
}

func TestDecidedPollGetsNoReminder(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	for _, tgID := range sevenAdmins[:4] {
		if err := h.vote(t, tgID, telegram.OptionApprove); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}

	h.store.advance(2 * day)
	h.remind(t)

	if got := reminderMessages(h.bot); len(got) != 0 {
		t.Errorf("sent %d reminders for a decided poll", len(got))
	}
}

func TestDemotedAdminIsNotPinged(t *testing.T) {
	h := newHarness(t, sevenAdmins)
	h.store.demote(sevenAdmins[6])

	h.store.advance(day)
	h.remind(t)

	got := reminderMessages(h.bot)
	if len(got) != 1 {
		t.Fatalf("sent %d reminders, want 1", len(got))
	}
	if strings.Contains(got[0].Text, mentionOf(sevenAdmins[6])) {
		t.Errorf("reminder pings a demoted admin:\n%s", got[0].Text)
	}
}

func TestReminderWhenEveryoneHasVotedWithoutAMajority(t *testing.T) {
	admins := sevenAdmins[:6]
	h := newHarness(t, admins)

	for i, tgID := range admins {
		option := telegram.OptionApprove
		if i%2 == 1 {
			option = telegram.OptionDecline
		}
		if err := h.vote(t, tgID, option); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}

	h.store.advance(day)
	h.remind(t)

	got := reminderMessages(h.bot)
	if len(got) != 1 {
		t.Fatalf("a deadlocked vote got %d reminders, want 1", len(got))
	}
	if strings.Contains(got[0].Text, "tg://user") {
		t.Errorf("reminder pings someone although everyone has voted:\n%s", got[0].Text)
	}
	if !strings.Contains(got[0].Text, "Everyone has voted") {
		t.Errorf("reminder does not explain the deadlock:\n%s", got[0].Text)
	}
}

func TestReminderClosesPollWhoseRequestIsGone(t *testing.T) {
	h := newHarness(t, sevenAdmins)
	h.store.orphan(h.poll.PollID)

	h.store.advance(day)
	h.remind(t)

	if got := reminderMessages(h.bot); len(got) != 0 {
		t.Errorf("sent %d reminders about a request that no longer exists", len(got))
	}
	if got := h.status(t); got != StatusCanceled {
		t.Errorf("poll status = %q, want %q", got, StatusCanceled)
	}
	if got := h.bot.stopCount(); got != 1 {
		t.Errorf("StopPoll called %d times, want 1", got)
	}
	if unpins := h.bot.unpinned(); len(unpins) != 1 {
		t.Errorf("unpinned %d messages, want the orphaned poll", len(unpins))
	}
}

func TestReminderSkipsRequestDecidedInTheMeantime(t *testing.T) {
	h := newHarness(t, sevenAdmins)
	h.decider.missing = true

	h.store.advance(day)
	h.remind(t)

	if got := reminderMessages(h.bot); len(got) != 0 {
		t.Errorf("sent %d reminders about a request that was just decided", len(got))
	}
	if got := h.status(t); got != StatusOpen {
		t.Errorf("poll status = %q, want it left to whoever decided the request", got)
	}
	if got := h.bot.stopCount(); got != 0 {
		t.Errorf("StopPoll called %d times, want 0", got)
	}
}

func TestSendRemindersWithoutAdminChatDoesNothing(t *testing.T) {
	store := newFakeStore(sevenAdmins...)
	if err := store.CreatePoll(context.Background(), Poll{
		PollID: "poll-1", RequestID: 42, ChatID: -1001, MessageID: 7, Threshold: 4, Status: StatusOpen,
	}); err != nil {
		t.Fatalf("CreatePoll: %v", err)
	}
	store.advance(day)

	bot := newFakeBot()
	manager := NewManager(store, bot, &fakeDecider{req: newRequest()}, 0)
	if err := manager.SendReminders(context.Background(), day); err != nil {
		t.Fatalf("SendReminders: %v", err)
	}
	if got := bot.sent(); len(got) != 0 {
		t.Errorf("sent %d messages with polls disabled", len(got))
	}

	var nilManager *Manager
	if err := nilManager.SendReminders(context.Background(), day); err != nil {
		t.Errorf("SendReminders on nil manager: %v", err)
	}
}

func TestReminderIntervalComesFromTheEnvironment(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{raw: "", want: DefaultReminderInterval},
		{raw: "12h", want: 12 * time.Hour},
		{raw: " 90m ", want: 90 * time.Minute},
		{raw: "5m", want: 5 * time.Minute},
		{raw: "soon", want: DefaultReminderInterval},
		{raw: "4m", want: DefaultReminderInterval},
		{raw: "-24h", want: DefaultReminderInterval},
	}

	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv(reminderIntervalEnvVar, tc.raw)
			if got := ReminderInterval(); got != tc.want {
				t.Errorf("ReminderInterval() with %q = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestReminderLoopRemindsUntilCanceled(t *testing.T) {
	h := newHarness(t, sevenAdmins)
	h.store.advance(day)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reminder := NewReminder(h.manager, day)
	waits := 0
	reminder.wait = func(_ context.Context, d time.Duration) {
		if d != reminderCheckInterval {
			t.Errorf("waited %v between checks, want %v", d, reminderCheckInterval)
		}
		waits++
		h.store.advance(day)
		if waits == 3 {
			cancel()
		}
	}

	done := make(chan struct{})
	go func() {
		reminder.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Reminder did not stop when its context was canceled")
	}

	if got := len(reminderMessages(h.bot)); got != 3 {
		t.Errorf("sent %d reminders over three days, want 3", got)
	}
}
