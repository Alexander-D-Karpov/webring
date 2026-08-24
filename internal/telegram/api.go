package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
	"unicode/utf16"
)

const (
	// DefaultAPIBaseURL is the public Bot API endpoint. Tests point a Client at a
	// httptest server instead.
	DefaultAPIBaseURL = "https://api.telegram.org"

	// Bot API limits. Exceeding either makes sendPoll fail outright, so poll text is
	// truncated to fit rather than lost.
	MaxPollQuestionLen = 300
	MaxPollOptionLen   = 100

	// LongPollTimeout is the getUpdates timeout in seconds. The HTTP deadline adds
	// headroom on top so the request outlives the long poll itself.
	LongPollTimeout   = 30
	longPollHeadroom  = 15 * time.Second
	optionApproveText = "Approve"
	optionDeclineText = "Decline"
)

// OptionApprove and OptionDecline are the poll option indexes. They are persisted in
// request_poll_votes.option_id, so their values are part of the stored data.
const (
	OptionApprove = 0
	OptionDecline = 1
)

// PollOptions returns the poll answer options in index order.
func PollOptions() []string {
	return []string{optionApproveText, optionDeclineText}
}

// APIError is a non-transport failure reported by Telegram itself.
type APIError struct {
	Method      string
	Code        int
	Description string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s failed (%d): %s", e.Method, e.Code, e.Description)
}

// IsConflict reports whether Telegram refused the call because another consumer owns
// the update stream — in practice, a webhook is set or a second process is polling.
func (e *APIError) IsConflict() bool {
	return e.Code == http.StatusConflict
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
}

// Client talks to the Telegram Bot API. The zero value is not usable; use NewClient.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

func NewClient(token string) *Client {
	return NewClientWithBaseURL(token, DefaultAPIBaseURL)
}

func NewClientWithBaseURL(token, baseURL string) *Client {
	return &Client{
		token:   token,
		baseURL: baseURL,
		http:    &http.Client{Timeout: LongPollTimeout*time.Second + longPollHeadroom},
	}
}

func (c *Client) call(ctx context.Context, method string, payload, result interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling %s payload: %w", method, err)
	}

	url := fmt.Sprintf("%s/bot%s/%s", c.baseURL, c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sending %s request: %w", method, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			logDebugf("Error closing response body: %v", closeErr)
		}
	}()

	var envelope apiResponse
	if decodeErr := json.NewDecoder(resp.Body).Decode(&envelope); decodeErr != nil {
		return fmt.Errorf("decoding %s response: %w", method, decodeErr)
	}

	if !envelope.OK {
		code := envelope.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return &APIError{Method: method, Code: code, Description: envelope.Description}
	}

	if result == nil {
		return nil
	}
	if unmarshalErr := json.Unmarshal(envelope.Result, result); unmarshalErr != nil {
		return fmt.Errorf("decoding %s result: %w", method, unmarshalErr)
	}
	return nil
}

// SendMessageParams describes a sendMessage call. ReplyToMessageID is omitted when zero.
type SendMessageParams struct {
	ChatID           int64  `json:"chat_id"`
	Text             string `json:"text"`
	ParseMode        string `json:"parse_mode"`
	ReplyToMessageID int64  `json:"reply_to_message_id,omitempty"`
}

// SentMessage is the subset of Telegram's Message we care about.
type SentMessage struct {
	MessageID int64    `json:"message_id"`
	Poll      *APIPoll `json:"poll"`
	Chat      *APIChat `json:"chat"`
}

type APIChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type APIUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type APIPoll struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	IsClosed bool   `json:"is_closed"`
}

// SendMessage sends a MarkdownV2 message and returns the resulting message.
func (c *Client) SendMessage(ctx context.Context, params SendMessageParams) (*SentMessage, error) {
	if params.ParseMode == "" {
		params.ParseMode = "MarkdownV2"
	}
	var msg SentMessage
	if err := c.call(ctx, "sendMessage", params, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// SendPollParams describes a sendPoll call.
type SendPollParams struct {
	ChatID           int64    `json:"chat_id"`
	Question         string   `json:"question"`
	Options          []string `json:"options"`
	IsAnonymous      bool     `json:"is_anonymous"`
	ReplyToMessageID int64    `json:"reply_to_message_id,omitempty"`
}

// SentPoll identifies a poll well enough to tally and later close it.
type SentPoll struct {
	PollID    string
	MessageID int64
	ChatID    int64
}

// SendPoll posts a poll and returns its identifiers. Question and option text are
// truncated to the Bot API limits so an over-long site name degrades instead of failing.
func (c *Client) SendPoll(ctx context.Context, params SendPollParams) (*SentPoll, error) {
	params.Question = TruncateForTelegram(params.Question, MaxPollQuestionLen)
	truncated := make([]string, len(params.Options))
	for i, opt := range params.Options {
		truncated[i] = TruncateForTelegram(opt, MaxPollOptionLen)
	}
	params.Options = truncated

	var msg SentMessage
	if err := c.call(ctx, "sendPoll", params, &msg); err != nil {
		return nil, err
	}
	if msg.Poll == nil || msg.Poll.ID == "" {
		return nil, fmt.Errorf("sendPoll returned no poll in message %d", msg.MessageID)
	}

	chatID := params.ChatID
	if msg.Chat != nil {
		chatID = msg.Chat.ID
	}
	return &SentPoll{PollID: msg.Poll.ID, MessageID: msg.MessageID, ChatID: chatID}, nil
}

type stopPollParams struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int64 `json:"message_id"`
}

// StopPoll closes a poll so Telegram stops accepting votes for it.
func (c *Client) StopPoll(ctx context.Context, chatID, messageID int64) error {
	return c.call(ctx, "stopPoll", stopPollParams{ChatID: chatID, MessageID: messageID}, nil)
}

type getUpdatesParams struct {
	Offset         int64    `json:"offset"`
	Timeout        int      `json:"timeout"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}

// Update is the subset of Telegram's Update we consume. Only poll answers are requested.
type Update struct {
	UpdateID   int64       `json:"update_id"`
	PollAnswer *PollAnswer `json:"poll_answer"`
}

// PollAnswer is one user's current selection in a non-anonymous poll. An empty OptionIDs
// means the user retracted their vote.
type PollAnswer struct {
	PollID    string   `json:"poll_id"`
	User      *APIUser `json:"user"`
	VoterChat *APIChat `json:"voter_chat"`
	OptionIDs []int    `json:"option_ids"`
}

// GetUpdates long-polls for updates starting at offset.
func (c *Client) GetUpdates(ctx context.Context, offset int64, allowed []string) ([]Update, error) {
	var updates []Update
	err := c.call(ctx, "getUpdates", getUpdatesParams{
		Offset:         offset,
		Timeout:        LongPollTimeout,
		AllowedUpdates: allowed,
	}, &updates)
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// TruncateForTelegram trims s to at most limit UTF-16 code units, which is how Telegram
// counts text length. Truncation happens on a rune boundary so the result stays valid.
func TruncateForTelegram(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(utf16.Encode([]rune(s))) <= limit {
		return s
	}

	units := 0
	for i, r := range s {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if units+w > limit {
			return s[:i]
		}
		units += w
	}
	return s
}
