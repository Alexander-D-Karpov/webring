package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf16"
)

// capturedCall is one request the fake Bot API received.
type capturedCall struct {
	method string
	body   map[string]interface{}
}

// fakeAPI stands in for api.telegram.org. It records the calls it receives and replies
// with scripted JSON.
type fakeAPI struct {
	server *httptest.Server
	calls  []capturedCall

	// reply returns the raw JSON body for a method, or "" to fall through to a generic
	// ok:true response.
	reply func(method string) (status int, body string)
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()

	api := &fakeAPI{}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Paths look like /bot<token>/<method>.
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		method := parts[len(parts)-1]

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		var body map[string]interface{}
		if len(raw) > 0 {
			if err = json.Unmarshal(raw, &body); err != nil {
				t.Errorf("request body for %s is not JSON: %v", method, err)
			}
		}
		api.calls = append(api.calls, capturedCall{method: method, body: body})

		status, reply := http.StatusOK, `{"ok":true,"result":true}`
		if api.reply != nil {
			if s, b := api.reply(method); b != "" {
				status, reply = s, b
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err = io.WriteString(w, reply); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	t.Cleanup(api.server.Close)

	return api
}

func (a *fakeAPI) client() *Client {
	return NewClientWithBaseURL("test-token", a.server.URL)
}

func (a *fakeAPI) lastCall(t *testing.T) capturedCall {
	t.Helper()
	if len(a.calls) == 0 {
		t.Fatalf("no calls were made to the API")
	}
	return a.calls[len(a.calls)-1]
}

// numField, stringField and listField read a value out of a captured request body,
// failing the test when the field is absent or the wrong JSON type.
func numField(t *testing.T, body map[string]interface{}, key string) int64 {
	t.Helper()
	value, ok := body[key].(float64)
	if !ok {
		t.Fatalf("field %q = %v, want a number", key, body[key])
	}
	return int64(value)
}

func stringField(t *testing.T, body map[string]interface{}, key string) string {
	t.Helper()
	value, ok := body[key].(string)
	if !ok {
		t.Fatalf("field %q = %v, want a string", key, body[key])
	}
	return value
}

func listField(t *testing.T, body map[string]interface{}, key string) []interface{} {
	t.Helper()
	value, ok := body[key].([]interface{})
	if !ok {
		t.Fatalf("field %q = %v, want a list", key, body[key])
	}
	return value
}

func TestSendPollUsesTheTokenAndMethodPath(t *testing.T) {
	api := newFakeAPI(t)
	api.reply = pollReply("poll-abc", 55, -1001)

	if _, err := api.client().SendPoll(context.Background(), SendPollParams{
		ChatID:  -1001,
		Options: PollOptions(),
	}); err != nil {
		t.Fatalf("SendPoll: %v", err)
	}

	if got := api.lastCall(t).method; got != "sendPoll" {
		t.Errorf("called %q, want sendPoll", got)
	}
}

func pollReply(pollID string, messageID, chatID int64) func(string) (int, string) {
	body, err := json.Marshal(map[string]interface{}{
		"ok": true,
		"result": map[string]interface{}{
			"message_id": messageID,
			"chat":       map[string]interface{}{"id": chatID, "type": "supergroup"},
			"poll":       map[string]interface{}{"id": pollID, "question": "q"},
		},
	})
	if err != nil {
		panic(err)
	}
	return func(method string) (int, string) {
		if method == "sendPoll" {
			return http.StatusOK, string(body)
		}
		return 0, ""
	}
}

func TestSendPollSendsNonAnonymousPollWithBothOptions(t *testing.T) {
	api := newFakeAPI(t)
	api.reply = pollReply("poll-abc", 55, -1001)

	sent, err := api.client().SendPoll(context.Background(), SendPollParams{
		ChatID:           -1001,
		Question:         "Request #1: add site \"Example\". Approve?",
		Options:          PollOptions(),
		IsAnonymous:      false,
		ReplyToMessageID: 54,
	})
	if err != nil {
		t.Fatalf("SendPoll: %v", err)
	}

	body := api.lastCall(t).body
	if anonymous, ok := body["is_anonymous"].(bool); !ok || anonymous {
		t.Errorf("is_anonymous = %v, want false — anonymous polls emit no poll_answer updates",
			body["is_anonymous"])
	}
	if chatID := numField(t, body, "chat_id"); chatID != -1001 {
		t.Errorf("chat_id = %d, want -1001", chatID)
	}
	if replyTo := numField(t, body, "reply_to_message_id"); replyTo != 54 {
		t.Errorf("reply_to_message_id = %d, want 54", replyTo)
	}

	options := listField(t, body, "options")
	if len(options) != 2 {
		t.Fatalf("options = %v, want two entries", options)
	}
	if options[OptionApprove] != "Approve" || options[OptionDecline] != "Decline" {
		t.Errorf("options = %v, want Approve then Decline", options)
	}

	if sent.PollID != "poll-abc" {
		t.Errorf("poll ID = %q, want poll-abc", sent.PollID)
	}
	if sent.MessageID != 55 {
		t.Errorf("message ID = %d, want 55", sent.MessageID)
	}
	if sent.ChatID != -1001 {
		t.Errorf("chat ID = %d, want -1001", sent.ChatID)
	}
}

// A supergroup migration changes the chat ID mid-flight, and stopPoll later needs the one
// Telegram actually used.
func TestSendPollPrefersTheChatIDFromTheResponse(t *testing.T) {
	api := newFakeAPI(t)
	api.reply = pollReply("poll-migrated", 55, -1002000)

	sent, err := api.client().SendPoll(context.Background(), SendPollParams{
		ChatID:  -1001,
		Options: PollOptions(),
	})
	if err != nil {
		t.Fatalf("SendPoll: %v", err)
	}
	if sent.ChatID != -1002000 {
		t.Errorf("chat ID = %d, want the -1002000 reported by Telegram", sent.ChatID)
	}
	if sent.PollID != "poll-migrated" {
		t.Errorf("poll ID = %q, want poll-migrated", sent.PollID)
	}
}

func TestSendPollTruncatesToBotAPILimits(t *testing.T) {
	api := newFakeAPI(t)
	api.reply = pollReply("poll-abc", 1, -1)

	longQuestion := strings.Repeat("q", MaxPollQuestionLen+50)
	longOption := strings.Repeat("o", MaxPollOptionLen+50)

	if _, err := api.client().SendPoll(context.Background(), SendPollParams{
		ChatID:   -1,
		Question: longQuestion,
		Options:  []string{longOption, "Decline"},
	}); err != nil {
		t.Fatalf("SendPoll: %v", err)
	}

	body := api.lastCall(t).body
	if question := stringField(t, body, "question"); len(question) != MaxPollQuestionLen {
		t.Errorf("question length = %d, want %d", len(question), MaxPollQuestionLen)
	}

	options := listField(t, body, "options")
	first, ok := options[0].(string)
	if !ok {
		t.Fatalf("first option = %v, want a string", options[0])
	}
	if len(first) != MaxPollOptionLen {
		t.Errorf("option length = %d, want %d", len(first), MaxPollOptionLen)
	}
}

func TestSendPollFailsWhenResponseCarriesNoPoll(t *testing.T) {
	api := newFakeAPI(t)
	api.reply = func(method string) (int, string) {
		if method == "sendPoll" {
			return http.StatusOK, `{"ok":true,"result":{"message_id":9}}`
		}
		return 0, ""
	}

	if _, err := api.client().SendPoll(context.Background(), SendPollParams{ChatID: -1}); err == nil {
		t.Errorf("SendPoll succeeded without a poll in the response")
	}
}

func TestStopPollIdentifiesTheMessage(t *testing.T) {
	api := newFakeAPI(t)

	if err := api.client().StopPoll(context.Background(), -1001, 55); err != nil {
		t.Fatalf("StopPoll: %v", err)
	}

	call := api.lastCall(t)
	if call.method != "stopPoll" {
		t.Errorf("called %q, want stopPoll", call.method)
	}
	if chatID := numField(t, call.body, "chat_id"); chatID != -1001 {
		t.Errorf("chat_id = %d, want -1001", chatID)
	}
	if messageID := numField(t, call.body, "message_id"); messageID != 55 {
		t.Errorf("message_id = %d, want 55", messageID)
	}
}

func TestSendMessageDefaultsToMarkdownV2(t *testing.T) {
	api := newFakeAPI(t)
	api.reply = func(method string) (int, string) {
		if method == "sendMessage" {
			return http.StatusOK, `{"ok":true,"result":{"message_id":7}}`
		}
		return 0, ""
	}

	msg, err := api.client().SendMessage(context.Background(), SendMessageParams{
		ChatID: 42,
		Text:   "hello",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if msg.MessageID != 7 {
		t.Errorf("message ID = %d, want 7", msg.MessageID)
	}

	body := api.lastCall(t).body
	if body["parse_mode"] != "MarkdownV2" {
		t.Errorf("parse_mode = %v, want MarkdownV2", body["parse_mode"])
	}
	// A zero reply target must be left out rather than sent as 0.
	if _, present := body["reply_to_message_id"]; present {
		t.Errorf("reply_to_message_id was sent even though none was set")
	}
}

func TestGetUpdatesPassesOffsetAndAllowedUpdates(t *testing.T) {
	api := newFakeAPI(t)
	api.reply = func(method string) (int, string) {
		if method == "getUpdates" {
			return http.StatusOK, `{"ok":true,"result":[
				{"update_id":31,"poll_answer":{"poll_id":"p1","user":{"id":555},"option_ids":[0]}},
				{"update_id":32,"poll_answer":{"poll_id":"p1","user":{"id":556},"option_ids":[]}}
			]}`
		}
		return 0, ""
	}

	updates, err := api.client().GetUpdates(context.Background(), 30, []string{"poll_answer"})
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}

	body := api.lastCall(t).body
	if offset := numField(t, body, "offset"); offset != 30 {
		t.Errorf("offset = %d, want 30", offset)
	}
	if timeout := numField(t, body, "timeout"); timeout != LongPollTimeout {
		t.Errorf("timeout = %d, want %d", timeout, LongPollTimeout)
	}
	allowed := listField(t, body, "allowed_updates")
	if len(allowed) != 1 || allowed[0] != "poll_answer" {
		t.Errorf("allowed_updates = %v, want [poll_answer]", allowed)
	}

	if len(updates) != 2 {
		t.Fatalf("got %d updates, want 2", len(updates))
	}
	if updates[0].PollAnswer == nil || updates[0].PollAnswer.User.ID != 555 {
		t.Errorf("first update did not decode its voter: %+v", updates[0])
	}
	if len(updates[0].PollAnswer.OptionIDs) != 1 || updates[0].PollAnswer.OptionIDs[0] != OptionApprove {
		t.Errorf("first update options = %v, want [0]", updates[0].PollAnswer.OptionIDs)
	}
	// An empty selection is a retracted vote and must survive decoding as such.
	if len(updates[1].PollAnswer.OptionIDs) != 0 {
		t.Errorf("retraction decoded as %v, want no options", updates[1].PollAnswer.OptionIDs)
	}
}

// 409 is what Telegram returns when a webhook is set, and the runner keys its warning off
// this being reported as a conflict.
func TestConflictIsReportedAsAPIError(t *testing.T) {
	api := newFakeAPI(t)
	api.reply = func(string) (int, string) {
		return http.StatusConflict, `{"ok":false,"error_code":409,` +
			`"description":"Conflict: can't use getUpdates method while webhook is active"}`
	}

	_, err := api.client().GetUpdates(context.Background(), 0, []string{"poll_answer"})
	if err == nil {
		t.Fatalf("GetUpdates succeeded on a 409")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not an *APIError", err)
	}
	if !apiErr.IsConflict() {
		t.Errorf("409 was not reported as a conflict")
	}
	if !strings.Contains(apiErr.Error(), "webhook") {
		t.Errorf("error text drops Telegram's explanation: %q", apiErr.Error())
	}
}

func TestOKFalseIsAnErrorEvenOnHTTP200(t *testing.T) {
	api := newFakeAPI(t)
	api.reply = func(string) (int, string) {
		return http.StatusOK, `{"ok":false,"error_code":400,"description":"chat not found"}`
	}

	err := api.client().StopPoll(context.Background(), -1, 1)
	if err == nil {
		t.Fatalf("StopPoll succeeded on ok:false")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not an *APIError", err)
	}
	if apiErr.Code != 400 {
		t.Errorf("code = %d, want 400", apiErr.Code)
	}
	if apiErr.IsConflict() {
		t.Errorf("400 was misreported as a conflict")
	}
}

func TestMalformedResponseIsAnError(t *testing.T) {
	api := newFakeAPI(t)
	api.reply = func(string) (int, string) {
		return http.StatusOK, `not json at all`
	}

	if err := api.client().StopPoll(context.Background(), -1, 1); err == nil {
		t.Errorf("StopPoll succeeded on a malformed response")
	}
}

func TestCanceledContextStopsTheCall(t *testing.T) {
	api := newFakeAPI(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := api.client().StopPoll(ctx, -1, 1); err == nil {
		t.Errorf("StopPoll succeeded with a canceled context")
	}
}

func TestTruncateForTelegramCountsUTF16Units(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{name: "under the limit", in: "hello", limit: 10, want: "hello"},
		{name: "exactly at the limit", in: "hello", limit: 5, want: "hello"},
		{name: "plain truncation", in: "hello", limit: 3, want: "hel"},
		{name: "zero limit", in: "hello", limit: 0, want: ""},
		{name: "negative limit", in: "hello", limit: -1, want: ""},
		// Cyrillic is one UTF-16 unit per character but two bytes, so a byte-based
		// truncation would cut these in half.
		{name: "multi-byte runes", in: "заявка", limit: 3, want: "зая"},
		// An emoji is a surrogate pair: two units for one rune.
		{name: "surrogate pair does not fit", in: "ab😀", limit: 3, want: "ab"},
		{name: "surrogate pair fits", in: "ab😀", limit: 4, want: "ab😀"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TruncateForTelegram(tc.in, tc.limit)
			if got != tc.want {
				t.Errorf("TruncateForTelegram(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
			}
			if units := len(utf16.Encode([]rune(got))); tc.limit > 0 && units > tc.limit {
				t.Errorf("result is %d UTF-16 units, over the %d limit", units, tc.limit)
			}
		})
	}
}
