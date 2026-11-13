package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
)

const (
	integrationClientID = "integration-client"
	integrationSecret   = "integration-secret"
	integrationRedirect = "https://webring.example/auth/telegram"
)

type integrationCode struct {
	challenge string
	token     string
}

// Intercept only Telegram's server-to-server requests; the application is exercised
// through its real HTTPS router, CSRF middleware, templates and database.
type integrationTelegram struct {
	key   *rsa.PrivateKey
	mu    sync.Mutex
	codes map[string]integrationCode
}

func (p *integrationTelegram) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "oauth.telegram.org" {
		return nil, fmt.Errorf("unexpected external request to %s", r.URL.Host)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var body any
	status := http.StatusOK
	switch r.URL.Path {
	case "/.well-known/jwks.json":
		body = map[string]any{"keys": []any{map[string]string{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "integration-key",
			"n": base64.RawURLEncoding.EncodeToString(p.key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(p.key.E)).Bytes()),
		}}}
	case "/token":
		id, secret, ok := r.BasicAuth()
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		code, exists := p.codes[r.PostForm.Get("code")]
		delete(p.codes, r.PostForm.Get("code"))
		digest := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || id != integrationClientID || secret != integrationSecret || !exists ||
			r.PostForm.Get("client_id") != integrationClientID ||
			r.PostForm.Get("redirect_uri") != integrationRedirect ||
			base64.RawURLEncoding.EncodeToString(digest[:]) != code.challenge {
			status, body = http.StatusBadRequest, map[string]string{"error": "invalid_grant"}
		} else {
			body = map[string]string{"access_token": "access-token", "token_type": "Bearer", "id_token": code.token}
		}
	default:
		status, body = http.StatusNotFound, map[string]string{"error": "not_found"}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(string(encoded))), Request: r,
	}, nil
}

func (p *integrationTelegram) issue(t *testing.T, code string, q url.Values, id any, username string) {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "integration-key"})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := json.Marshal(map[string]any{
		"iss": "https://oauth.telegram.org", "aud": integrationClientID, "sub": "not-the-telegram-id",
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "nonce": q.Get("nonce"),
		"id": id, "given_name": "Integration", "preferred_username": username,
	})
	if err != nil {
		t.Fatal(err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.codes[code] = integrationCode{
		challenge: q.Get("code_challenge"),
		token:     unsigned + "." + base64.RawURLEncoding.EncodeToString(signature),
	}
	p.mu.Unlock()
}

func integrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DB_CONNECTION_STRING")
	if dsn == "" {
		t.Skip("TEST_DB_CONNECTION_STRING not set; skipping database integration test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing database: %v", err)
		}
	})
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	return db
}

func integrationBrowser(t *testing.T, app *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := *app.Client()
	client.Jar = jar
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

func readResponse(t *testing.T, res *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func startIntegrationLogin(t *testing.T, client *http.Client, appURL string) url.Values {
	t.Helper()
	res, err := client.Get(appURL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("login page status = %d", res.StatusCode)
	}
	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := res.Body.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	token, ok := doc.Find(`form[action="/auth/telegram/start"] input[name="gorilla.csrf.Token"]`).Attr("value")
	if !ok || token == "" {
		t.Fatal("login form has no CSRF token")
	}
	form := url.Values{"gorilla.csrf.Token": {token}}
	req, err := http.NewRequest(http.MethodPost, appURL+"/auth/telegram/start", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", appURL+"/login")
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := readResponse(t, res)
	if closeErr := res.Body.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login start status = %d: %s", res.StatusCode, body)
	}
	target, err := url.Parse(res.Header.Get("Location"))
	if err != nil || target.Host != "oauth.telegram.org" || target.Path != "/auth" {
		t.Fatalf("unexpected authorization URL: %s (%v)", res.Header.Get("Location"), err)
	}
	return target.Query()
}

func TestTelegramLoginPreservesSiteOwnership(t *testing.T) {
	db := integrationDB(t)
	t.Setenv("TELEGRAM_OIDC_CLIENT_ID", integrationClientID)
	t.Setenv("TELEGRAM_OIDC_CLIENT_SECRET", integrationSecret)
	t.Setenv("TELEGRAM_OIDC_REDIRECT_URL", integrationRedirect)
	t.Setenv("TELEGRAM_OIDC_ADDITIONAL_REDIRECT_URLS", "")
	t.Setenv("CSRF_AUTH_KEY", strings.Repeat("c", 32))
	t.Setenv("CSRF_SECURE", "true")
	t.Setenv("SESSION_SECURE_COOKIE", "true")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	provider := &integrationTelegram{key: key, codes: make(map[string]integrationCode)}
	previousTransport := http.DefaultTransport
	http.DefaultTransport = provider
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	router := mux.NewRouter()
	registerHandlers(router, db)
	initializeTemplates(parseTemplates())
	app := httptest.NewTLSServer(setupCSRFMiddleware(router))
	t.Cleanup(app.Close)

	client := integrationBrowser(t, app)
	res, err := client.Post(app.URL+"/auth/telegram/start", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	readResponse(t, res)
	if closeErr := res.Body.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("login start without CSRF token returned %d", res.StatusCode)
	}
	res, err = client.Get(app.URL + "/auth/telegram?id=123&hash=old-widget")
	if err != nil {
		t.Fatal(err)
	}
	readResponse(t, res)
	if closeErr := res.Body.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("legacy login returned %d", res.StatusCode)
	}

	for i, tc := range []struct {
		name        string
		admin       bool
		placeholder bool
		stringID    bool
		destination string
	}{
		{name: "existing user with numeric ID", destination: "/user"},
		{name: "existing user with string ID", stringID: true, destination: "/user"},
		{name: "existing admin with numeric ID", admin: true, destination: "/admin/requests"},
		{name: "existing admin with string ID", admin: true, stringID: true, destination: "/admin/requests"},
		{name: "username placeholder with numeric ID", placeholder: true, destination: "/user"},
		{name: "username placeholder with string ID", placeholder: true, stringID: true, destination: "/user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := time.Now().UnixNano()%1_000_000_000_000 + int64(i)
			username := fmt.Sprintf("oidc_integration_%d", id)
			var storedID any = id
			if tc.placeholder {
				storedID = nil
			}
			var userID, siteID int
			err := db.QueryRow(`INSERT INTO users (telegram_id, telegram_username, is_admin)
				VALUES ($1, $2, $3) RETURNING id`, storedID, username, tc.admin).Scan(&userID)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, cleanupErr := db.Exec("DELETE FROM users WHERE id = $1", userID); cleanupErr != nil {
					t.Errorf("deleting user: %v", cleanupErr)
				}
			})
			siteName := fmt.Sprintf("OIDC site %d", id)
			err = db.QueryRow(`INSERT INTO sites (name, url, slug, user_id, display_order)
				VALUES ($1, $2, $3, $4, (SELECT COALESCE(MAX(display_order), 0) + 1 FROM sites)) RETURNING id`,
				siteName, "https://example.com", username, userID).Scan(&siteID)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, cleanupErr := db.Exec("DELETE FROM sites WHERE id = $1", siteID); cleanupErr != nil {
					t.Errorf("deleting site: %v", cleanupErr)
				}
			})
			browser := integrationBrowser(t, app)
			query := startIntegrationLogin(t, browser, app.URL)
			code := fmt.Sprintf("integration-code-%d", id)
			var claimID any = id
			if tc.stringID {
				claimID = strconv.FormatInt(id, 10)
			}
			provider.issue(t, code, query, claimID, strings.ToUpper(username))
			callback := url.Values{"state": {query.Get("state")}, "code": {code}}
			res, err := browser.Get(app.URL + "/auth/telegram?" + callback.Encode())
			if err != nil {
				t.Fatal(err)
			}
			body := readResponse(t, res)
			if closeErr := res.Body.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != tc.destination {
				t.Fatalf("callback status = %d, location = %q, body = %s", res.StatusCode,
					res.Header.Get("Location"), body)
			}
			var matchedID int64
			var ownerID int
			if queryErr := db.QueryRow("SELECT telegram_id FROM users WHERE id = $1", userID).Scan(&matchedID); queryErr != nil {
				t.Fatal(queryErr)
			}
			if queryErr := db.QueryRow("SELECT user_id FROM sites WHERE id = $1", siteID).Scan(&ownerID); queryErr != nil {
				t.Fatal(queryErr)
			}
			if matchedID != id || ownerID != userID {
				t.Fatalf("Telegram ID = %d, site owner = %d; wanted %d, %d", matchedID, ownerID, id, userID)
			}
			res, err = browser.Get(app.URL + tc.destination)
			if err != nil {
				t.Fatal(err)
			}
			body = readResponse(t, res)
			if closeErr := res.Body.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if res.StatusCode != http.StatusOK || (!tc.admin && !strings.Contains(body, siteName)) {
				t.Fatalf("dashboard status = %d, site visible = %v", res.StatusCode, strings.Contains(body, siteName))
			}
		})
	}
}
