// Package approval runs the Telegram vote that decides update requests.
//
// A poll is posted to the shared admin group when a request is created. Votes arrive as
// poll_answer updates, are checked against the admin list in the database, and once one
// option reaches a majority of admins the outcome is applied and the poll is closed.
//
// The package talks to the outside world through the Store, Bot and Decider interfaces
// so the vote logic can be exercised without a database or network.
package approval

import (
	"context"
	"fmt"
	"log"

	"webring/internal/models"
	"webring/internal/telegram"
)

// Status is the lifecycle state of a poll, mirroring request_polls.status.
type Status string

const (
	// StatusOpen means the poll is accepting votes.
	StatusOpen Status = "open"
	// StatusApproved and StatusDeclined record which way the vote went.
	StatusApproved Status = "approved"
	StatusDeclined Status = "declined"
	// StatusCanceled means the poll was abandoned without deciding the request, which
	// leaves the request free to be resolved from the dashboard.
	StatusCanceled Status = "canceled"
)

// Poll is a vote in progress on a single update request.
type Poll struct {
	PollID    string
	RequestID int
	ChatID    int64
	MessageID int64
	Threshold int
	Status    Status
}

// Voter is an admin who has cast a vote, as shown in the dashboard.
type Voter struct {
	// Name is the admin's display name.
	Name string
	// Username is their Telegram handle, empty when Name already is one.
	Username string
}

// VoteSummary is the state of a request's poll, for display in the dashboard.
type VoteSummary struct {
	Threshold int
	Status    Status
	Approve   []Voter
	Decline   []Voter
}

// Open reports whether the poll is still accepting votes.
func (v *VoteSummary) Open() bool {
	return v != nil && v.Status == StatusOpen
}

// Leading is the highest vote count on any one option, which is how close the request is
// to being decided.
func (v *VoteSummary) Leading() int {
	if v == nil {
		return 0
	}
	if len(v.Approve) > len(v.Decline) {
		return len(v.Approve)
	}
	return len(v.Decline)
}

// Remaining is how many more votes for a single option would decide the request.
func (v *VoteSummary) Remaining() int {
	if v == nil {
		return 0
	}
	if left := v.Threshold - v.Leading(); left > 0 {
		return left
	}
	return 0
}

// Store is the persistence the vote logic needs.
type Store interface {
	// AdminCount returns how many admins are able to vote.
	AdminCount(ctx context.Context) (int, error)
	// AdminTelegramIDs lists the chat IDs to notify about an outcome.
	AdminTelegramIDs(ctx context.Context) ([]int64, error)
	// AdminByTelegramID returns the admin owning a Telegram ID, or nil when that user
	// is unknown or is not an admin.
	AdminByTelegramID(ctx context.Context, telegramID int64) (*models.User, error)

	CreatePoll(ctx context.Context, poll Poll) error
	// PollByID returns nil when no poll with that ID is known.
	PollByID(ctx context.Context, pollID string) (*Poll, error)
	// PollByRequestID returns nil when the request has no poll attached.
	PollByRequestID(ctx context.Context, requestID int) (*Poll, error)
	SetStatus(ctx context.Context, pollID string, status Status) error
	// ClaimDecision atomically takes ownership of deciding a request. It reports false
	// when another actor already decided it.
	ClaimDecision(ctx context.Context, requestID int, status Status) (bool, error)

	RecordVote(ctx context.Context, pollID string, userID int, telegramID int64, optionID int) error
	DeleteVote(ctx context.Context, pollID string, telegramID int64) error
	// Tally counts current votes per option ID.
	Tally(ctx context.Context, pollID string) (map[int]int, error)
	// VoterNames lists the display names of the admins who chose an option.
	VoterNames(ctx context.Context, pollID string, optionID int) ([]string, error)
	// VoteSummaries returns the poll state for each of the given requests. Requests
	// with no poll are absent from the result.
	VoteSummaries(ctx context.Context, requestIDs []int) (map[int]*VoteSummary, error)
}

// Bot is the slice of the Telegram API the vote needs.
type Bot interface {
	SendMessage(ctx context.Context, params telegram.SendMessageParams) (*telegram.SentMessage, error)
	SendPoll(ctx context.Context, params telegram.SendPollParams) (*telegram.SentPoll, error)
	StopPoll(ctx context.Context, chatID, messageID int64) error
}

// Decider applies the outcome of a vote to the request itself.
type Decider interface {
	// Load returns nil when the request no longer exists.
	Load(requestID int) (*models.UpdateRequest, error)
	Approve(req *models.UpdateRequest) error
	Decline(req *models.UpdateRequest) error
}

// Threshold is how many admins must agree for a vote to carry: a simple majority of the
// admins who can vote. Seven admins need four.
func Threshold(adminCount int) int {
	if adminCount < 1 {
		return 1
	}
	return adminCount/2 + 1
}

// Manager owns poll creation and vote counting.
type Manager struct {
	store   Store
	bot     Bot
	decider Decider
	chatID  int64
}

func NewManager(store Store, bot Bot, decider Decider, chatID int64) *Manager {
	return &Manager{store: store, bot: bot, decider: decider, chatID: chatID}
}

// Enabled reports whether an admin group chat is configured. When it is not, requests
// are decided exclusively from the dashboard.
func (m *Manager) Enabled() bool {
	return m != nil && m.chatID != 0
}

// CreatePoll posts the request details to the admin group, opens a vote below them, and
// records the poll so incoming votes can be matched to the request.
func (m *Manager) CreatePoll(ctx context.Context, req *models.UpdateRequest, submitter *models.User) error {
	if !m.Enabled() {
		return nil
	}

	adminCount, err := m.store.AdminCount(ctx)
	if err != nil {
		return fmt.Errorf("counting admins: %w", err)
	}

	// The threshold is frozen here. Promoting an admin mid-vote must not move the
	// goalposts for a poll people have already voted in.
	threshold := Threshold(adminCount)

	var replyTo int64
	if details := telegram.FormatRequestMessage(req, submitter); details != "" {
		msg, sendErr := m.bot.SendMessage(ctx, telegram.SendMessageParams{
			ChatID: m.chatID,
			Text:   details,
		})
		if sendErr != nil {
			// The poll alone is still useful, so carry on without the detail message.
			log.Printf("Error posting request %d details to admin chat: %v", req.ID, sendErr)
		} else {
			replyTo = msg.MessageID
		}
	}

	sent, err := m.bot.SendPoll(ctx, telegram.SendPollParams{
		ChatID:   m.chatID,
		Question: PollQuestion(req),
		Options:  telegram.PollOptions(),
		// Anonymous polls emit no poll_answer updates, so votes could never be counted
		// or attributed. This must stay false.
		IsAnonymous:      false,
		ReplyToMessageID: replyTo,
	})
	if err != nil {
		return fmt.Errorf("sending approval poll: %w", err)
	}

	return m.store.CreatePoll(ctx, Poll{
		PollID:    sent.PollID,
		RequestID: req.ID,
		ChatID:    sent.ChatID,
		MessageID: sent.MessageID,
		Threshold: threshold,
		Status:    StatusOpen,
	})
}

// PollQuestion renders the poll's question. Telegram shows it as plain text, so it must
// not carry MarkdownV2 escaping.
func PollQuestion(req *models.UpdateRequest) string {
	siteName := telegram.RequestSiteName(req)
	if req.RequestType == "create" {
		return fmt.Sprintf("Request #%d: add site %q. Approve?", req.ID, siteName)
	}
	return fmt.Sprintf("Request #%d: update site %q. Approve?", req.ID, siteName)
}

// HandlePollAnswer records one admin's vote and applies the outcome once an option
// reaches the threshold. Votes from non-admins are discarded.
func (m *Manager) HandlePollAnswer(ctx context.Context, answer *telegram.PollAnswer) error {
	if answer == nil || answer.PollID == "" {
		return nil
	}

	poll, err := m.store.PollByID(ctx, answer.PollID)
	if err != nil {
		return fmt.Errorf("looking up poll %s: %w", answer.PollID, err)
	}
	if poll == nil {
		// Not one of ours, or predates this feature.
		return nil
	}
	if poll.Status != StatusOpen {
		// Already decided. Votes still in flight when the poll closed land here.
		return nil
	}

	// Votes cast on behalf of a channel have no user attached, so there is no identity
	// to check against the admin list.
	if answer.User == nil {
		return nil
	}

	admin, err := m.store.AdminByTelegramID(ctx, answer.User.ID)
	if err != nil {
		return fmt.Errorf("verifying voter %d: %w", answer.User.ID, err)
	}
	if admin == nil {
		log.Printf("Ignoring poll %s vote from non-admin Telegram user %d", poll.PollID, answer.User.ID)
		return nil
	}

	// An empty selection means the admin retracted their vote.
	if len(answer.OptionIDs) == 0 {
		return m.store.DeleteVote(ctx, poll.PollID, answer.User.ID)
	}

	// The poll disallows multiple answers, so there is exactly one selection. Recording
	// it replaces any previous vote by the same admin rather than adding to it.
	if err = m.store.RecordVote(ctx, poll.PollID, admin.ID, answer.User.ID, answer.OptionIDs[0]); err != nil {
		return fmt.Errorf("recording vote: %w", err)
	}

	tally, err := m.store.Tally(ctx, poll.PollID)
	if err != nil {
		return fmt.Errorf("tallying poll %s: %w", poll.PollID, err)
	}

	switch {
	case tally[telegram.OptionApprove] >= poll.Threshold:
		return m.decide(ctx, poll, StatusApproved, telegram.OptionApprove)
	case tally[telegram.OptionDecline] >= poll.Threshold:
		return m.decide(ctx, poll, StatusDeclined, telegram.OptionDecline)
	}

	return nil
}

func (m *Manager) decide(ctx context.Context, poll *Poll, status Status, winningOption int) error {
	req, err := m.decider.Load(poll.RequestID)
	if err != nil {
		return fmt.Errorf("loading request %d: %w", poll.RequestID, err)
	}
	if req == nil {
		// The request was resolved elsewhere between the vote and now.
		if setErr := m.store.SetStatus(ctx, poll.PollID, StatusCanceled); setErr != nil {
			log.Printf("Error canceling poll %s: %v", poll.PollID, setErr)
		}
		m.ClosePoll(ctx, poll)
		return nil
	}

	// Take ownership before applying anything. This is what keeps a dashboard click and
	// a winning vote from both acting on the same request.
	claimed, err := m.store.ClaimDecision(ctx, poll.RequestID, status)
	if err != nil {
		return fmt.Errorf("claiming request %d: %w", poll.RequestID, err)
	}
	if !claimed {
		return nil
	}

	// Stop taking votes as soon as the outcome is locked in.
	m.ClosePoll(ctx, poll)

	if status == StatusApproved {
		err = m.decider.Approve(req)
	} else {
		err = m.decider.Decline(req)
	}
	if err != nil {
		// Applying failed, so the request is still pending. Release the claim by
		// canceling the poll and tell the admins to finish it in the dashboard.
		log.Printf("Error applying voted %s for request %d: %v", status, poll.RequestID, err)
		if setErr := m.store.SetStatus(ctx, poll.PollID, StatusCanceled); setErr != nil {
			log.Printf("Error canceling poll %s: %v", poll.PollID, setErr)
		}
		m.announceFailure(ctx, poll, req, err)
		return err
	}

	m.announce(ctx, poll, req, status, winningOption)
	return nil
}

// ClosePoll asks Telegram to stop accepting votes. Failure is logged, not propagated:
// the decision has already been made and stored.
func (m *Manager) ClosePoll(ctx context.Context, poll *Poll) {
	if m == nil || poll == nil || poll.MessageID == 0 {
		return
	}
	if err := m.bot.StopPoll(ctx, poll.ChatID, poll.MessageID); err != nil {
		log.Printf("Error closing Telegram poll %s: %v", poll.PollID, err)
	}
}

// PollForRequest returns the poll attached to a request, or nil when there is none.
// Callers deciding a request from the dashboard must fetch the poll before deleting the
// request, since the link is cleared when the request row goes away.
func (m *Manager) PollForRequest(ctx context.Context, requestID int) *Poll {
	if !m.Enabled() {
		return nil
	}
	poll, err := m.store.PollByRequestID(ctx, requestID)
	if err != nil {
		log.Printf("Error looking up poll for request %d: %v", requestID, err)
		return nil
	}
	return poll
}

// VoteSummaries returns the poll state for each of the given requests, for rendering in
// the admin dashboard. Requests with no poll are absent from the result, and a failure to
// read them is logged rather than propagated: the dashboard is still usable without the
// vote counts.
func (m *Manager) VoteSummaries(ctx context.Context, requestIDs []int) map[int]*VoteSummary {
	if !m.Enabled() || len(requestIDs) == 0 {
		return nil
	}

	summaries, err := m.store.VoteSummaries(ctx, requestIDs)
	if err != nil {
		log.Printf("Error loading vote summaries: %v", err)
		return nil
	}
	return summaries
}

// Claim takes ownership of deciding a request from outside the poll, so a dashboard
// action and a winning vote cannot both apply it. It reports false when the request has
// already been decided.
func (m *Manager) Claim(ctx context.Context, requestID int, status Status) (bool, error) {
	if m == nil || m.store == nil {
		return true, nil
	}
	return m.store.ClaimDecision(ctx, requestID, status)
}

// Release hands a claimed request back so it can be decided again. It is used when
// applying a decision failed and the request is still pending: the poll returns to
// accepting votes, and the dashboard can retry.
func (m *Manager) Release(ctx context.Context, poll *Poll) {
	if m == nil || poll == nil {
		return
	}
	if err := m.store.SetStatus(ctx, poll.PollID, StatusOpen); err != nil {
		log.Printf("Error reopening poll %s: %v", poll.PollID, err)
	}
}

func (m *Manager) announce(ctx context.Context, poll *Poll, req *models.UpdateRequest,
	status Status, winningOption int) {
	voters, err := m.store.VoterNames(ctx, poll.PollID, winningOption)
	if err != nil {
		log.Printf("Error listing voters for poll %s: %v", poll.PollID, err)
	}

	template := "poll_approved"
	if status == StatusDeclined {
		template = "poll_declined"
	}

	text := telegram.RenderMessage(template, map[string]interface{}{
		"SiteName": telegram.RequestSiteName(req),
		"UserName": telegram.ResolveUserName(req.User),
		"Voters":   voters,
	})
	if text == "" {
		return
	}

	m.send(ctx, telegram.SendMessageParams{
		ChatID:           poll.ChatID,
		Text:             text,
		ReplyToMessageID: poll.MessageID,
	})

	admins, err := m.store.AdminTelegramIDs(ctx)
	if err != nil {
		log.Printf("Error listing admins to notify: %v", err)
		return
	}
	for _, adminID := range admins {
		m.send(ctx, telegram.SendMessageParams{ChatID: adminID, Text: text})
	}
}

func (m *Manager) announceFailure(ctx context.Context, poll *Poll, req *models.UpdateRequest, cause error) {
	text := telegram.EscapeMarkdownV2(fmt.Sprintf(
		"Vote on request #%d (%s) could not be applied: %v. Please resolve it from the dashboard.",
		req.ID, telegram.RequestSiteName(req), cause))

	m.send(ctx, telegram.SendMessageParams{
		ChatID:           poll.ChatID,
		Text:             text,
		ReplyToMessageID: poll.MessageID,
	})
}

func (m *Manager) send(ctx context.Context, params telegram.SendMessageParams) {
	if _, err := m.bot.SendMessage(ctx, params); err != nil {
		log.Printf("Error sending message to %d: %v", params.ChatID, err)
	}
}
