package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	testClientID     = "test-client"
	testClientSecret = "test-secret"
	testRedirectURL  = "https://webring.example/auth/telegram"
	testCodeParam    = "code"
)

type testFlow struct {
	query  url.Values
	cookie *http.Cookie
}

type testCallback struct {
	header  http.Header
	cookies []*http.Cookie
}

type pendingCode struct {
	challenge   string
	redirectURL string
	idToken     string
}

type testProvider struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	key     *rsa.PrivateKey
	keyID   string
	codes   map[string]pendingCode
	fetches int
}

func newTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func newTestProvider(t *testing.T) (*testProvider, *TelegramLogin) {
	t.Helper()
	p := &testProvider{t: t, key: newTestKey(t), keyID: "key-1", codes: make(map[string]pendingCode)}
	p.server = httptest.NewServer(http.HandlerFunc(p.serveHTTP))
	t.Cleanup(p.server.Close)
	client := p.server.Client()
	ctx := oidc.ClientContext(context.Background(), client)
	keys := oidc.NewRemoteKeySet(ctx, p.server.URL+"/.well-known/jwks.json")
	login := &TelegramLogin{
		client:   client,
		verifier: oidc.NewVerifier(p.server.URL, keys, &oidc.Config{ClientID: testClientID}),
		oauth: oauth2.Config{
			ClientID: testClientID, ClientSecret: testClientSecret, RedirectURL: testRedirectURL,
			Scopes: []string{"openid", "profile", "telegram:bot_access"},
			Endpoint: oauth2.Endpoint{
				AuthURL: p.server.URL + "/auth", TokenURL: p.server.URL + "/token",
				AuthStyle: oauth2.AuthStyleInHeader,
			},
		},
	}
	return p, login
}

func (p *testProvider) serveHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/.well-known/jwks.json":
		p.fetches++
		key := p.key.PublicKey
		if err := json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": p.keyID,
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}}); err != nil {
			p.t.Errorf("writing JWKS: %v", err)
		}
	case "/token":
		p.serveToken(w, r)
	default:
		http.NotFound(w, r)
	}
}

// serveToken is called with p.mu held.
func (p *testProvider) serveToken(w http.ResponseWriter, r *http.Request) {
	id, secret, ok := r.BasicAuth()
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		p.t.Errorf("parsing token request: %v", err)
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	code, found := p.codes[r.PostForm.Get(testCodeParam)]
	delete(p.codes, r.PostForm.Get(testCodeParam))
	digest := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	if r.Method != http.MethodPost || !ok || id != testClientID || secret != testClientSecret ||
		r.PostForm.Get("client_id") != testClientID || r.PostForm.Get("redirect_uri") != code.redirectURL ||
		r.PostForm.Get("grant_type") != "authorization_code" || !found || code.challenge != challenge {
		http.Error(w, "invalid token request", http.StatusBadRequest)
		return
	}
	if err := json.NewEncoder(w).Encode(map[string]any{
		"access_token": "test-access-token", "token_type": "Bearer", "id_token": code.idToken,
	}); err != nil {
		p.t.Errorf("writing token: %v", err)
	}
}

func (p *testProvider) issue(t *testing.T, code string, flow testFlow, claims map[string]any, key *rsa.PrivateKey) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if key == nil {
		key = p.key
	}
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": p.keyID})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	p.codes[code] = pendingCode{
		challenge:   flow.query.Get("code_challenge"),
		redirectURL: flow.query.Get("redirect_uri"),
		idToken:     unsigned + "." + base64.RawURLEncoding.EncodeToString(signature),
	}
}

func startTestFlow(t *testing.T, login *TelegramLogin) testFlow {
	t.Helper()
	return startTestFlowAt(t, login, "example.com", testRedirectURL)
}

func startTestFlowAt(t *testing.T, login *TelegramLogin, host, redirectURL string) testFlow {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/telegram/start", http.NoBody)
	req.Host = host
	login.Start(w, req)
	res := w.Result()
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Errorf("closing response: %v", err)
		}
	}()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("start status = %d", res.StatusCode)
	}
	target, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := target.Query()
	cookies := res.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("start returned %d cookies", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != "__Host-webring-tg-"+q.Get("state") || !cookie.Secure || !cookie.HttpOnly ||
		cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != 600 {
		t.Fatalf("invalid flow cookie: %+v", cookie)
	}
	if q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge") == "" ||
		q.Get("code_challenge_method") != "S256" || q.Get("response_type") != testCodeParam ||
		q.Get("redirect_uri") != redirectURL || q.Get("client_id") != testClientID ||
		q.Get("scope") != "openid profile telegram:bot_access" || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid authorization request: %s", target)
	}
	return testFlow{query: q, cookie: cookie}
}

func validClaims(p *testProvider, flow testFlow) map[string]any {
	return map[string]any{
		"iss": p.server.URL, "aud": testClientID, "sub": "different-from-telegram-id",
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		"nonce": flow.query.Get("nonce"), "id": int64(12345), "given_name": "Test",
		"family_name": "User", "preferred_username": "testuser",
	}
}

func finishTestFlow(t *testing.T, login *TelegramLogin, flow testFlow, values url.Values) (
	*TelegramUser, testCallback, error,
) {
	t.Helper()
	return finishTestFlowAt(t, login, flow, values, "example.com")
}

func finishTestFlowAt(t *testing.T, login *TelegramLogin, flow testFlow, values url.Values, host string) (
	*TelegramUser, testCallback, error,
) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/auth/telegram?"+values.Encode(), http.NoBody)
	req.Host = host
	if flow.cookie != nil {
		req.AddCookie(flow.cookie)
	}
	w := httptest.NewRecorder()
	user, err := login.User(w, req)
	res := w.Result()
	if closeErr := res.Body.Close(); closeErr != nil {
		t.Fatalf("closing response: %v", closeErr)
	}
	return user, testCallback{header: res.Header, cookies: res.Cookies()}, err
}

func TestTelegramLoginParallelFlowsAndReplay(t *testing.T) {
	p, login := newTestProvider(t)
	first, second := startTestFlow(t, login), startTestFlow(t, login)
	if first.query.Get("state") == second.query.Get("state") || first.query.Get("nonce") == second.query.Get("nonce") {
		t.Fatal("parallel login flows reused state or nonce")
	}
	stringIDClaims := validClaims(p, first)
	stringIDClaims["id"] = "12345"
	p.issue(t, "code-1", first, stringIDClaims, nil)
	p.issue(t, "code-2", second, validClaims(p, second), nil)
	for _, item := range []struct {
		flow testFlow
		code string
	}{{second, "code-2"}, {first, "code-1"}} {
		values := url.Values{"state": {item.flow.query.Get("state")}, testCodeParam: {item.code}}
		user, res, err := finishTestFlow(t, login, item.flow, values)
		if err != nil || user == nil || user.ID != 12345 || user.FirstName != "Test" ||
			user.LastName != "User" || user.Username != "testuser" {
			t.Fatalf("login = %+v, %v", user, err)
		}
		if res.header.Get("Cache-Control") != "no-store" || res.header.Get("Referrer-Policy") != "no-referrer" ||
			len(res.cookies) != 1 || res.cookies[0].MaxAge >= 0 {
			t.Fatal("callback did not clear the flow cookie or set security headers")
		}
		if _, _, err := finishTestFlow(t, login, item.flow, values); err == nil {
			t.Fatal("reused authorization code was accepted")
		}
	}
}

func TestTelegramLoginMultipleHosts(t *testing.T) {
	const alternate = "https://mirror.example/auth/telegram"
	t.Setenv("TELEGRAM_OIDC_REDIRECT_URL", testRedirectURL)
	t.Setenv("TELEGRAM_OIDC_ADDITIONAL_REDIRECT_URLS", alternate)
	configured := NewTelegramLogin()
	p, login := newTestProvider(t)
	login.redirectURLs, login.redirectInvalid = configured.redirectURLs, configured.redirectInvalid

	for _, tc := range []struct {
		host     string
		redirect string
	}{
		{"webring.example", testRedirectURL},
		{"mirror.example", alternate},
	} {
		t.Run(tc.host, func(t *testing.T) {
			flow := startTestFlowAt(t, login, tc.host, tc.redirect)
			if flow.cookie.Domain != "" {
				t.Fatal("login flow cookie must be secure and host-only")
			}
			p.issue(t, tc.host, flow, validClaims(p, flow), nil)
			values := url.Values{"state": {flow.query.Get("state")}, testCodeParam: {tc.host}}
			user, _, err := finishTestFlowAt(t, login, flow, values, tc.host)
			if err != nil || user == nil || user.ID != 12345 {
				t.Fatalf("login for %s failed: %v", tc.host, err)
			}
		})
	}

	flow := startTestFlowAt(t, login, "webring.example", testRedirectURL)
	p.issue(t, "wrong-host", flow, validClaims(p, flow), nil)
	values := url.Values{"state": {flow.query.Get("state")}, testCodeParam: {"wrong-host"}}
	if _, _, err := finishTestFlowAt(t, login, flow, values, "mirror.example"); err == nil {
		t.Fatal("callback accepted an authorization code issued for another host")
	}

	request := httptest.NewRequest(http.MethodPost, "https://untrusted.example/auth/telegram/start", http.NoBody)
	request.Header.Set("X-Forwarded-Host", "webring.example")
	w := httptest.NewRecorder()
	login.Start(w, request)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Location") != "" ||
		len(w.Header().Values("Set-Cookie")) != 0 {
		t.Fatal("unconfigured host was allowed to start a login")
	}
}

func TestTelegramRedirectURLsRejectInvalidConfiguration(t *testing.T) {
	for _, invalid := range []string{
		"http://mirror.example/auth/telegram",
		"https://user@mirror.example/auth/telegram",
		"https://mirror.example/other",
		"https://mirror.example/auth/telegram?next=elsewhere",
	} {
		if _, err := telegramRedirectURLs(testRedirectURL, invalid); err == nil {
			t.Errorf("accepted invalid redirect URL %q", invalid)
		}
	}
	t.Setenv("TELEGRAM_OIDC_CLIENT_ID", testClientID)
	t.Setenv("TELEGRAM_OIDC_CLIENT_SECRET", testClientSecret)
	t.Setenv("TELEGRAM_OIDC_REDIRECT_URL", testRedirectURL)
	t.Setenv("TELEGRAM_OIDC_ADDITIONAL_REDIRECT_URLS", "http://mirror.example/auth/telegram")
	w := httptest.NewRecorder()
	NewTelegramLogin().Start(w, httptest.NewRequest(http.MethodPost, "/auth/telegram/start", http.NoBody))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal("invalid multi-host configuration was not rejected")
	}
}

func TestTelegramLoginRejectsInvalidIDTokens(t *testing.T) {
	p, login := newTestProvider(t)
	wrongKey := newTestKey(t)
	tests := []struct {
		name   string
		mutate func(map[string]any)
		key    *rsa.PrivateKey
	}{
		{name: "bad signature", key: wrongKey},
		{name: "bad signature with string ID", key: wrongKey, mutate: func(c map[string]any) { c["id"] = "12345" }},
		{name: "wrong issuer", mutate: func(c map[string]any) { c["iss"] = "https://wrong.example" }},
		{name: "wrong audience", mutate: func(c map[string]any) { c["aud"] = "another-client" }},
		{name: "wrong nonce", mutate: func(c map[string]any) { c["nonce"] = "wrong" }},
		{name: "expired", mutate: func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }},
		{name: "missing subject", mutate: func(c map[string]any) { delete(c, "sub") }},
		{name: "missing profile ID", mutate: func(c map[string]any) { delete(c, "id") }},
		{name: "zero ID", mutate: func(c map[string]any) { c["id"] = 0 }},
		{name: "negative string ID", mutate: func(c map[string]any) { c["id"] = "-1" }},
		{name: "non-numeric string ID", mutate: func(c map[string]any) { c["id"] = "not-an-id" }},
		{name: "boolean ID", mutate: func(c map[string]any) { c["id"] = true }},
		{name: "fractional string ID", mutate: func(c map[string]any) { c["id"] = "1.5" }},
		{name: "overflow string ID", mutate: func(c map[string]any) { c["id"] = "9223372036854775808" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			flow := startTestFlow(t, login)
			claims := validClaims(p, flow)
			if tc.mutate != nil {
				tc.mutate(claims)
			}
			p.issue(t, tc.name, flow, claims, tc.key)
			values := url.Values{"state": {flow.query.Get("state")}, testCodeParam: {tc.name}}
			if user, _, err := finishTestFlow(t, login, flow, values); err == nil || user != nil {
				t.Fatalf("invalid token accepted: %+v, %v", user, err)
			}
		})
	}
}

func TestTelegramLoginRejectsInvalidCallbacks(t *testing.T) {
	_, login := newTestProvider(t)
	flow := startTestFlow(t, login)
	tests := []struct {
		name   string
		flow   testFlow
		values url.Values
	}{
		{"wrong state", flow, url.Values{"state": {"wrong"}, testCodeParam: {testCodeParam}}},
		{"missing state", flow, url.Values{testCodeParam: {testCodeParam}}},
		{"missing cookie", testFlow{}, url.Values{"state": {flow.query.Get("state")}, testCodeParam: {testCodeParam}}},
		{"missing code", flow, url.Values{"state": {flow.query.Get("state")}}},
		{"provider error", flow, url.Values{"state": {flow.query.Get("state")}, "error": {"access_denied"}}},
		{"legacy payload", testFlow{}, url.Values{"id": {"12345"}, "hash": {"legacy"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if user, _, err := finishTestFlow(t, login, tc.flow, tc.values); err == nil || user != nil {
				t.Fatalf("invalid callback accepted: %+v, %v", user, err)
			}
		})
	}
}

func TestTelegramLoginKeyRotationAndNameFallback(t *testing.T) {
	p, login := newTestProvider(t)
	first := startTestFlow(t, login)
	p.issue(t, "first", first, validClaims(p, first), nil)
	if _, _, err := finishTestFlow(t, login, first,
		url.Values{"state": {first.query.Get("state")}, testCodeParam: {"first"}}); err != nil {
		t.Fatal(err)
	}

	p.mu.Lock()
	p.key, p.keyID = newTestKey(t), "key-2"
	p.mu.Unlock()
	second := startTestFlow(t, login)
	claims := validClaims(p, second)
	delete(claims, "given_name")
	claims["name"] = "Fallback Name"
	p.issue(t, "second", second, claims, nil)
	user, _, err := finishTestFlow(t, login, second,
		url.Values{"state": {second.query.Get("state")}, testCodeParam: {"second"}})
	if err != nil || user == nil || user.FirstName != "Fallback Name" {
		t.Fatalf("login after key rotation = %+v, %v", user, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fetches < 2 {
		t.Errorf("JWKS was fetched %d times; expected a refresh after rotation", p.fetches)
	}
}

func TestTelegramLoginRequiresConfiguration(t *testing.T) {
	t.Setenv("TELEGRAM_OIDC_CLIENT_ID", "")
	t.Setenv("TELEGRAM_OIDC_CLIENT_SECRET", "")
	t.Setenv("TELEGRAM_OIDC_REDIRECT_URL", "")
	t.Setenv("TELEGRAM_OIDC_ADDITIONAL_REDIRECT_URLS", "")
	w := httptest.NewRecorder()
	NewTelegramLogin().Start(w, httptest.NewRequest(http.MethodPost, "/auth/telegram/start", http.NoBody))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "not configured") {
		t.Fatalf("unconfigured login status = %d, body = %q", w.Code, w.Body.String())
	}
}
