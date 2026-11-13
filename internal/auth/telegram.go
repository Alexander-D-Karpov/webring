package auth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const telegramIssuer = "https://oauth.telegram.org"

type TelegramUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"given_name"`
	LastName  string `json:"family_name"`
	Username  string `json:"preferred_username"`
	Name      string `json:"name"`
}

type TelegramLogin struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	client   *http.Client
}

func NewTelegramLogin() *TelegramLogin {
	id := os.Getenv("TELEGRAM_OIDC_CLIENT_ID")
	client := &http.Client{Timeout: 8 * time.Second}
	ctx := oidc.ClientContext(context.Background(), client)
	keys := oidc.NewRemoteKeySet(ctx, telegramIssuer+"/.well-known/jwks.json")
	return &TelegramLogin{
		client: client,
		verifier: oidc.NewVerifier(telegramIssuer, keys, &oidc.Config{
			ClientID: id, SupportedSigningAlgs: []string{oidc.RS256},
		}),
		oauth: oauth2.Config{
			ClientID: id, ClientSecret: os.Getenv("TELEGRAM_OIDC_CLIENT_SECRET"),
			RedirectURL: os.Getenv("TELEGRAM_OIDC_REDIRECT_URL"),
			Scopes:      []string{"openid", "profile", "telegram:bot_access"},
			Endpoint: oauth2.Endpoint{
				AuthURL: telegramIssuer + "/auth", TokenURL: telegramIssuer + "/token",
				AuthStyle: oauth2.AuthStyleInHeader,
			},
		},
	}
}

func flowCookie(state, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: "__Host-webring-tg-" + state, Value: value, MaxAge: maxAge,
		Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
}

func (l *TelegramLogin) Start(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if l.oauth.ClientID == "" || l.oauth.ClientSecret == "" || l.oauth.RedirectURL == "" {
		http.Error(w, "Telegram login is not configured", http.StatusServiceUnavailable)
		return
	}
	state, nonce, verifier := rand.Text(), rand.Text(), oauth2.GenerateVerifier()
	http.SetCookie(w, flowCookie(state, nonce+"."+verifier, 600))
	target := l.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (l *TelegramLogin) User(w http.ResponseWriter, r *http.Request) (*TelegramUser, error) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")

	q := r.URL.Query()
	state := q.Get("state")
	cookie, err := r.Cookie("__Host-webring-tg-" + state)
	if err != nil || state == "" {
		return nil, errors.New("unknown Telegram login state")
	}
	http.SetCookie(w, flowCookie(state, "", -1))
	nonce, verifier, ok := strings.Cut(cookie.Value, ".")
	if !ok || nonce == "" || verifier == "" || q.Get("error") != "" || q.Get("code") == "" {
		return nil, errors.New("incomplete Telegram login")
	}

	ctx := oidc.ClientContext(r.Context(), l.client)
	tokens, err := l.oauth.Exchange(ctx, q.Get("code"),
		oauth2.VerifierOption(verifier), oauth2.SetAuthURLParam("client_id", l.oauth.ClientID))
	if err != nil {
		return nil, errors.New("failed to exchange Telegram authorization code")
	}
	raw, ok := tokens.Extra("id_token").(string)
	if !ok || raw == "" {
		return nil, errors.New("missing Telegram ID token")
	}
	token, err := l.verifier.Verify(ctx, raw)
	if err != nil || token.Nonce != nonce || token.Subject == "" {
		return nil, errors.New("invalid Telegram ID token")
	}
	var claims struct {
		TelegramUser
		ID json.Number `json:"id"`
	}
	if err = token.Claims(&claims); err != nil {
		return nil, errors.New("missing or invalid Telegram profile ID")
	}
	// Telegram may encode the profile ID as either a JSON number or a decimal
	// string. Only the verified profile ID, not the OIDC subject, identifies an
	// existing Telegram account.
	id, err := claims.ID.Int64()
	if err != nil || id <= 0 {
		return nil, errors.New("missing or invalid Telegram profile ID")
	}
	user := claims.TelegramUser
	user.ID = id
	if user.FirstName == "" {
		user.FirstName = user.Name
	}
	return &user, nil
}
