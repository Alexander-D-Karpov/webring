package blacklist

import (
	"strings"
	"testing"
	"time"

	"webring/internal/models"
)

func strptr(s string) *string { return &s }

func TestNormalizeUserStripsCaseAtSignAndSpace(t *testing.T) {
	cases := map[string]string{
		"  @Alice ": "alice",
		"BOB":       "bob",
		"@":         "",
		"":          "",
		"  ":        "",
	}

	for input, want := range cases {
		if got := NormalizeUser(input); got != want {
			t.Errorf("NormalizeUser(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeSiteReducesURLsToABareHost(t *testing.T) {
	cases := map[string]string{
		"https://www.Example.com/path?q=1#frag": "example.com",
		"http://example.com:8080":               "example.com",
		"WWW.Example.COM":                       "example.com",
		"example.com/":                          "example.com",
		"  my-slug  ":                           "my-slug",
		"":                                      "",
	}

	for input, want := range cases {
		if got := NormalizeSite(input); got != want {
			t.Errorf("NormalizeSite(%q) = %q, want %q", input, got, want)
		}
	}
}

// A leading colon or slash is not a port or a path separator, so trimming at index 0
// would erase the whole value and silently turn a bad subject into an empty one.
func TestNormalizeSiteIgnoresLeadingSeparators(t *testing.T) {
	if got := NormalizeSite("/example.com"); got != "example.com" {
		t.Errorf("NormalizeSite(\"/example.com\") = %q, want %q", got, "example.com")
	}
}

func TestNormalizeDispatchesOnSubjectType(t *testing.T) {
	if got := Normalize(SubjectSite, "https://Example.com/x"); got != "example.com" {
		t.Errorf("Normalize(site) = %q, want example.com", got)
	}
	if got := Normalize(SubjectUser, "@Alice"); got != "alice" {
		t.Errorf("Normalize(user) = %q, want alice", got)
	}
}

func TestUserSubjectsIncludesUsernameAndID(t *testing.T) {
	user := &models.User{ID: 12, TelegramID: 999, TelegramUsername: strptr("@Alice")}

	got := UserSubjects(user)
	want := []string{"alice", "#12"}

	if len(got) != len(want) {
		t.Fatalf("UserSubjects() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("UserSubjects()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// A username-only placeholder account (created from a public submission) has no
// telegram_id yet but is still identifiable.
func TestUserSubjectsCoversPlaceholderAccounts(t *testing.T) {
	user := &models.User{ID: 7, TelegramID: 0, TelegramUsername: strptr("bob")}

	got := UserSubjects(user)
	if len(got) != 2 || got[0] != "bob" || got[1] != "#7" {
		t.Errorf("UserSubjects() = %v, want [bob #7]", got)
	}
}

// The anonymous row is shared by every handle-less submission, so blocking it would
// freeze all of them at once.
func TestUserSubjectsRefusesToIdentifyTheAnonymousUser(t *testing.T) {
	cases := map[string]*models.User{
		"nil user":         nil,
		"no handle at all": {ID: 3, TelegramID: 0, TelegramUsername: nil},
		"blank handle":     {ID: 3, TelegramID: 0, TelegramUsername: strptr("   ")},
	}

	for name, user := range cases {
		if got := UserSubjects(user); got != nil {
			t.Errorf("%s: UserSubjects() = %v, want nil", name, got)
		}
	}
}

// A real Telegram account with the username cleared is still worth blocking by ID.
func TestUserSubjectsFallsBackToIDForUsernamelessTelegramUsers(t *testing.T) {
	user := &models.User{ID: 5, TelegramID: 42, TelegramUsername: nil}

	got := UserSubjects(user)
	if len(got) != 1 || got[0] != "#5" {
		t.Errorf("UserSubjects() = %v, want [#5]", got)
	}
}

func TestUsernameSubjects(t *testing.T) {
	if got := UsernameSubjects("@Alice"); len(got) != 1 || got[0] != "alice" {
		t.Errorf("UsernameSubjects(@Alice) = %v, want [alice]", got)
	}
	if got := UsernameSubjects("  "); got != nil {
		t.Errorf("UsernameSubjects(blank) = %v, want nil", got)
	}
}

func TestSiteSubjectsCoversSlugAndHost(t *testing.T) {
	got := SiteSubjects("My-Site", "https://www.example.com/blog")
	if len(got) != 2 || got[0] != "my-site" || got[1] != "example.com" {
		t.Errorf("SiteSubjects() = %v, want [my-site example.com]", got)
	}
}

func TestSiteSubjectsAcceptsAURLWithoutAScheme(t *testing.T) {
	got := SiteSubjects("", "example.com/path")
	if len(got) != 1 || got[0] != "example.com" {
		t.Errorf("SiteSubjects() = %v, want [example.com]", got)
	}
}

// A slug that already equals the host must not produce a duplicate ANY() member.
func TestSiteSubjectsDeduplicates(t *testing.T) {
	got := SiteSubjects("example.com", "https://example.com")
	if len(got) != 1 || got[0] != "example.com" {
		t.Errorf("SiteSubjects() = %v, want [example.com]", got)
	}
}

func TestSiteSubjectsIsEmptyWhenNothingIsIdentifiable(t *testing.T) {
	if got := SiteSubjects("", ""); got != nil {
		t.Errorf("SiteSubjects() = %v, want nil", got)
	}
}

func TestRequestSiteSubjectsUsesTheCreateRequestFields(t *testing.T) {
	req := &models.UpdateRequest{
		RequestType:   "create",
		ChangedFields: map[string]interface{}{"slug": "new-site", "url": "https://new.example.com"},
	}

	got := RequestSiteSubjects(req)
	if len(got) != 2 || got[0] != "new-site" || got[1] != "new.example.com" {
		t.Errorf("RequestSiteSubjects() = %v, want [new-site new.example.com]", got)
	}
}

// An update names both the site as it stands and the values it wants; the changed
// fields win so the cooldown lands on what was actually asked for.
func TestRequestSiteSubjectsPrefersChangedFieldsOverTheCurrentSite(t *testing.T) {
	req := &models.UpdateRequest{
		RequestType:   "update",
		Site:          &models.Site{Slug: "old-slug", URL: "https://old.example.com"},
		ChangedFields: map[string]interface{}{"slug": "fresh-slug"},
	}

	got := RequestSiteSubjects(req)
	if len(got) != 2 || got[0] != "fresh-slug" || got[1] != "old.example.com" {
		t.Errorf("RequestSiteSubjects() = %v, want [fresh-slug old.example.com]", got)
	}
}

func TestRequestSiteSubjectsFallsBackToTheCurrentSite(t *testing.T) {
	req := &models.UpdateRequest{
		RequestType:   "update",
		Site:          &models.Site{Slug: "only-name", URL: "https://only.example.com"},
		ChangedFields: map[string]interface{}{"name": "New Name"},
	}

	got := RequestSiteSubjects(req)
	if len(got) != 2 || got[0] != "only-name" || got[1] != "only.example.com" {
		t.Errorf("RequestSiteSubjects() = %v, want [only-name only.example.com]", got)
	}
}

func TestRequestSiteSubjectsHandlesNil(t *testing.T) {
	if got := RequestSiteSubjects(nil); got != nil {
		t.Errorf("RequestSiteSubjects(nil) = %v, want nil", got)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{-time.Hour, "less than a minute"},
		{0, "less than a minute"},
		{20 * time.Second, "less than a minute"},
		{90 * time.Minute, "1h 30m"},
		{2 * time.Hour, "2h"},
		{45 * time.Minute, "45m"},
		{24 * time.Hour, "24h"},
	}

	for _, c := range cases {
		if got := FormatDuration(c.in); got != c.want {
			t.Errorf("FormatDuration(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCooldownDurationReadsTheEnvironment(t *testing.T) {
	t.Setenv(cooldownEnvVar, "90m")
	if got := CooldownDuration(); got != 90*time.Minute {
		t.Errorf("CooldownDuration() = %s, want 90m", got)
	}
}

func TestCooldownDurationFallsBackOnBadValues(t *testing.T) {
	for _, raw := range []string{"", "   ", "nonsense", "-5h", "0"} {
		t.Setenv(cooldownEnvVar, raw)
		if got := CooldownDuration(); got != DefaultCooldown {
			t.Errorf("CooldownDuration() with %q = %s, want %s", raw, got, DefaultCooldown)
		}
	}
}

func TestCooldownRemaining(t *testing.T) {
	c := Cooldown{ExpiresAt: time.Now().Add(2*time.Hour + 30*time.Minute)}
	if got := c.Remaining(); got != "2h 30m" {
		t.Errorf("Remaining() = %q, want 2h 30m", got)
	}

	expired := Cooldown{ExpiresAt: time.Now().Add(-time.Hour)}
	if got := expired.Remaining(); got != "less than a minute" {
		t.Errorf("Remaining() on expired = %q, want 'less than a minute'", got)
	}
}

func TestBlockPermanentDistinguishesEntriesFromCooldowns(t *testing.T) {
	entry := &Block{SubjectType: SubjectUser}
	if !entry.Permanent() {
		t.Error("a block with no expiry should be permanent")
	}

	cooldown := &Block{SubjectType: SubjectUser, ExpiresAt: time.Now().Add(time.Hour)}
	if cooldown.Permanent() {
		t.Error("a block with an expiry should not be permanent")
	}

	var nilBlock *Block
	if nilBlock.Permanent() {
		t.Error("a nil block should not be permanent")
	}
}

func TestBlockMessage(t *testing.T) {
	expires := time.Now().Add(3 * time.Hour)

	cases := []struct {
		name  string
		block *Block
		want  []string
	}{
		{
			name:  "permanent site entry names the site",
			block: &Block{SubjectType: SubjectSite, Subject: "spam.example"},
			want:  []string{"'spam.example' is blocked from the webring."},
		},
		{
			name:  "permanent user entry stays vague",
			block: &Block{SubjectType: SubjectUser, Subject: "alice"},
			want:  []string{"Your account is blocked"},
		},
		{
			name:  "site cooldown quotes the wait",
			block: &Block{SubjectType: SubjectSite, Subject: "my-site", ExpiresAt: expires},
			want:  []string{"my-site", "3h"},
		},
		{
			name:  "user cooldown quotes the wait",
			block: &Block{SubjectType: SubjectUser, Subject: "alice", ExpiresAt: expires},
			want:  []string{"A previous request of yours was rejected", "3h"},
		},
		{
			name:  "a reason is appended",
			block: &Block{SubjectType: SubjectUser, Subject: "alice", Reason: "spam"},
			want:  []string{"Reason: spam"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.block.Message()
			for _, fragment := range c.want {
				if !strings.Contains(got, fragment) {
					t.Errorf("Message() = %q, want it to contain %q", got, fragment)
				}
			}
		})
	}
}

// A permanent user block must not leak which handle the admin blacklisted.
func TestBlockMessageDoesNotRevealTheBlockedUsername(t *testing.T) {
	block := &Block{SubjectType: SubjectUser, Subject: "alice"}
	if strings.Contains(block.Message(), "alice") {
		t.Errorf("Message() = %q, should not name the user", block.Message())
	}
}

func TestNilBlockHasNoMessage(t *testing.T) {
	var block *Block
	if got := block.Message(); got != "" {
		t.Errorf("Message() on nil = %q, want empty", got)
	}
}
