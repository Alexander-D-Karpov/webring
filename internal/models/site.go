package models

type Site struct {
	ID        int     `json:"id"`
	Name      string  `json:"name"`
	URL       string  `json:"url"`
	IsUp      bool    `json:"is_up"`
	LastCheck float64 `json:"last_check"`
	Favicon   *string `json:"favicon"`
}

type PublicSite struct {
	ID      int     `json:"id"`
	Name    string  `json:"name"`
	URL     string  `json:"url"`
	Favicon *string `json:"favicon"`
}

type SiteData struct {
	Prev PublicSite `json:"prev"`
	Curr PublicSite `json:"curr"`
	Next PublicSite `json:"next"`
}
