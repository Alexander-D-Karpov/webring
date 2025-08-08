package models

type Site struct {
	ID               int     `json:"id"`
	Slug             string  `json:"slug"`
	Name             string  `json:"name"`
	URL              string  `json:"url"`
	IsUp             bool    `json:"is_up"`
	LastCheck        float64 `json:"last_check"`
	Favicon          *string `json:"favicon"`
	UserID           *int    `json:"user_id"`
	User             *User   `json:"user,omitempty"`
	TelegramUsername *string `json:"telegram_username,omitempty"`
}

type PublicSite struct {
	ID      int     `json:"id"`
	Slug    string  `json:"slug"`
	Name    string  `json:"name"`
	URL     string  `json:"url"`
	Favicon *string `json:"favicon"`
}

type SiteData struct {
	Prev PublicSite `json:"prev"`
	Curr PublicSite `json:"curr"`
	Next PublicSite `json:"next"`
}
