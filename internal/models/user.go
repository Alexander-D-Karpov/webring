package models

import "time"

type User struct {
	ID               int       `json:"id"`
	TelegramID       int64     `json:"telegram_id"`
	TelegramUsername *string   `json:"telegram_username"`
	FirstName        *string   `json:"first_name"`
	LastName         *string   `json:"last_name"`
	IsAdmin          bool      `json:"is_admin"`
	CreatedAt        time.Time `json:"created_at"`
}

type Session struct {
	ID        string    `json:"id"`
	UserID    int       `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type UpdateRequest struct {
	ID            int                    `json:"id"`
	UserID        int                    `json:"user_id"`
	SiteID        *int                   `json:"site_id"`
	RequestType   string                 `json:"request_type"`
	ChangedFields map[string]interface{} `json:"changed_fields"`
	CreatedAt     time.Time              `json:"created_at"`
	User          *User                  `json:"user,omitempty"`
	Site          *Site                  `json:"site,omitempty"`
}
