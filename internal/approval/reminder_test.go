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

func (h *harness) remind(t *testing.T) {
	t.Helper()
	if err := h.manager.SendReminders(context.Background(), day); err != nil {
		t.Fatalf("SendReminders: %v", err)
	}
}

func TestReminderComesOncePerInterval(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	h.store.advance(day - time.Minute)
	h.remind(t)
	if got := len(reminderMessages(h.bot)); got != 0 {
		t.Fatalf("sent %d reminders for a poll open less than a day", got)
	}

	h.store.advance(time.Minute)
	h.remind(t)
	h.remind(t)
	if got := len(reminderMessages(h.bot)); got != 1 {
		t.Fatalf("sent %d reminders on the first day, want 1", got)
	}

	h.store.advance(day)
	h.remind(t)
	if got := len(reminderMessages(h.bot)); got != 2 {
		t.Errorf("sent %d reminders after two days, want 2", got)
	}
}

func TestReminderPingsOnlyAdminsWhoHaveNotVoted(t *testing.T) {
	h := newHarness(t, sevenAdmins)

	name := "a_b"
	h.store.mu.Lock()
	h.store.admins[sevenAdmins[6]].FirstName = &name
	h.store.mu.Unlock()

	for i, tgID := range sevenAdmins[:3] {
		option := telegram.OptionApprove
		if i == 2 {
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
		t.Fatalf("sent %d reminders, want 1", len(got))
	}
	if got[0].ChatID != h.poll.ChatID || got[0].ReplyToMessageID != h.poll.MessageID {
		t.Errorf("reminder went to chat %d replying to %d, want chat %d replying to the poll %d",
			got[0].ChatID, got[0].ReplyToMessageID, h.poll.ChatID, h.poll.MessageID)
	}

	text := got[0].Text
	for i, tgID := range sevenAdmins {
		pinged := strings.Contains(text, fmt.Sprintf("(tg://user?id=%d)", tgID))
		if want := i >= 3; pinged != want {
			t.Errorf("admin %d pinged = %v, want %v:\n%s", tgID, pinged, want, text)
		}
	}
	for _, want := range []string{`[a\_b](tg://user?id=107)`, `\#42`, "Example Site", "2 of 4", "1 of 4"} {
		if !strings.Contains(text, want) {
			t.Errorf("reminder is missing %q:\n%s", want, text)
		}
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
	if strings.Contains(got[0].Text, "tg://user") || !strings.Contains(got[0].Text, "Everyone has voted") {
		t.Errorf("a deadlocked vote should be reported without pings:\n%s", got[0].Text)
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
}
