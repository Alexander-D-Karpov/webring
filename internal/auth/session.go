package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"webring/internal/models"
)

const (
	DefaultSessionTTL = 7 * 24 * time.Hour // 7 days
	sessionKeyLength  = 32
)

func GenerateSessionID() (string, error) {
	bytes := make([]byte, sessionKeyLength)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func GetSessionTTL() time.Duration {
	if ttlStr := os.Getenv("SESSION_TTL_HOURS"); ttlStr != "" {
		if hours, err := strconv.Atoi(ttlStr); err == nil {
			return time.Duration(hours) * time.Hour
		}
	}
	return DefaultSessionTTL
}

func CreateSession(db *sql.DB, userID int) (*models.Session, error) {
	sessionID, err := GenerateSessionID()
	if err != nil {
		return nil, err
	}

	expiresAt := time.Now().Add(GetSessionTTL())

	_, err = db.Exec("INSERT INTO sessions (id, user_id, expires_at) VALUES ($1, $2, $3)",
		sessionID, userID, expiresAt)
	if err != nil {
		return nil, err
	}

	return &models.Session{
		ID:        sessionID,
		UserID:    userID,
		CreatedAt: time.Now(),
		ExpiresAt: expiresAt,
	}, nil
}

func GetSessionUser(db *sql.DB, sessionID string) (*models.User, error) {
	var user models.User
	var telegramID sql.NullInt64
	err := db.QueryRow(`
		SELECT u.id, u.telegram_id, u.telegram_username, u.first_name, u.last_name, u.is_admin, u.created_at
		FROM users u
		JOIN sessions s ON u.id = s.user_id
		WHERE s.id = $1 AND s.expires_at > NOW()
	`, sessionID).Scan(
		&user.ID, &telegramID, &user.TelegramUsername,
		&user.FirstName, &user.LastName, &user.IsAdmin, &user.CreatedAt)

	if err != nil {
		return nil, err
	}

	if telegramID.Valid {
		user.TelegramID = telegramID.Int64
	}

	return &user, nil
}

func DeleteSession(db *sql.DB, sessionID string) error {
	_, err := db.Exec("DELETE FROM sessions WHERE id = $1", sessionID)
	return err
}

func CleanExpiredSessions(db *sql.DB) {
	_, err := db.Exec("DELETE FROM sessions WHERE expires_at <= NOW()")
	if err != nil {
		log.Printf("Error cleaning expired sessions: %v", err)
	}
}

func isSecureCookieEnabled() bool {
	// Default to true for production safety
	if secureStr := os.Getenv("SESSION_SECURE_COOKIE"); secureStr != "" {
		if secure, err := strconv.ParseBool(secureStr); err == nil {
			return secure
		}
	}
	// Default to true unless explicitly set to false
	return os.Getenv("ENV") != "development"
}

func SetSessionCookie(w http.ResponseWriter, sessionID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "session_id",
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureCookieEnabled(),
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(GetSessionTTL()),
	})
}

func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "session_id",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureCookieEnabled(),
		Expires:  time.Unix(0, 0),
	})
}

func GetSessionFromRequest(r *http.Request) string {
	cookie, err := r.Cookie("session_id")
	if err != nil {
		return ""
	}
	return cookie.Value
}
