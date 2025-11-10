package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type TelegramUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
	PhotoURL  string `json:"photo_url,omitempty"`
	AuthDate  int64  `json:"auth_date"`
	Hash      string `json:"hash"`
}

func VerifyTelegramAuth(values url.Values, botToken string) (*TelegramUser, error) {
	hash := values.Get("hash")
	if hash == "" {
		return nil, fmt.Errorf("missing hash parameter")
	}

	var dataStrings []string
	for key, value := range values {
		if key != "hash" && len(value) > 0 {
			dataStrings = append(dataStrings, fmt.Sprintf("%s=%s", key, value[0]))
		}
	}
	sort.Strings(dataStrings)
	dataString := strings.Join(dataStrings, "\n")

	// Create secret key
	secretKey := sha256.Sum256([]byte(botToken))

	// Create HMAC
	h := hmac.New(sha256.New, secretKey[:])
	h.Write([]byte(dataString))
	expectedHash := hex.EncodeToString(h.Sum(nil))

	if hash != expectedHash {
		return nil, fmt.Errorf("invalid hash")
	}

	// Parse user data
	id, err := strconv.ParseInt(values.Get("id"), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid id")
	}

	authDate, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid auth_date")
	}
	if time.Since(time.Unix(authDate, 0)) > 24*time.Hour {
		return nil, fmt.Errorf("stale login payload")
	}

	return &TelegramUser{
		ID:        id,
		FirstName: values.Get("first_name"),
		LastName:  values.Get("last_name"),
		Username:  values.Get("username"),
		PhotoURL:  values.Get("photo_url"),
		AuthDate:  authDate,
		Hash:      hash,
	}, nil
}
