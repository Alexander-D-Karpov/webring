package public

import (
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"html/template"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/gorilla/mux"
	"github.com/lib/pq"

	"webring/internal/auth"
	"webring/internal/models"
)

const uniqueViolation = "unique_violation"

type TemplateData struct {
	Sites       []models.PublicSite
	ContactLink string
	User        *models.User
}

var (
	templates   *template.Template
	templatesMu sync.RWMutex
	slugRegex   = regexp.MustCompile(`^[a-z0-9-]{3,50}$`)
)

func InitTemplates(t *template.Template) {
	templatesMu.Lock()
	defer templatesMu.Unlock()
	templates = t
}

func sanitizeInput(input string) string {
	trimmed := strings.TrimSpace(input)
	return html.EscapeString(trimmed)
}

func sanitizeURL(input string) string {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		trimmed = "https://" + trimmed
	}
	return html.EscapeString(trimmed)
}

func RegisterHandlers(r *mux.Router, db *sql.DB) {
	r.HandleFunc("/", listSitesHandler(db)).Methods("GET")
}

func RegisterSubmissionHandlers(r *mux.Router, db *sql.DB) {
	r.HandleFunc("/submit", submitSitePageHandler()).Methods("GET")
	r.HandleFunc("/submit", submitSiteHandler(db)).Methods("POST")
}

func listSitesHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sites, err := getRespondingSites(db)
		if err != nil {
			log.Printf("Error fetching sites: %v", err)
			http.Error(w, "Error fetching sites", http.StatusInternalServerError)
			return
		}

		templatesMu.RLock()
		t := templates
		templatesMu.RUnlock()

		if t == nil {
			log.Println("Templates not initialized")
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		var user *models.User
		if sessionID := auth.GetSessionFromRequest(r); sessionID != "" {
			user, err = auth.GetSessionUser(db, sessionID)
			if err != nil {
				log.Printf("Error getting session user: %v", err)
			}
		}

		data := TemplateData{
			Sites:       sites,
			ContactLink: os.Getenv("CONTACT_LINK"),
			User:        user,
		}

		if err = t.ExecuteTemplate(w, "sites.html", data); err != nil {
			log.Printf("Error rendering template: %v", err)
			http.Error(w, "Error rendering template", http.StatusInternalServerError)
			return
		}
	}
}

func submitSitePageHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		templatesMu.RLock()
		t := templates
		templatesMu.RUnlock()

		if t == nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		if err := t.ExecuteTemplate(w, "submit_site.html", nil); err != nil {
			log.Printf("Error rendering submit site template: %v", err)
			http.Error(w, "Error rendering template", http.StatusInternalServerError)
			return
		}
	}
}

func submitSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := sanitizeInput(r.FormValue("slug"))
		name := sanitizeInput(r.FormValue("name"))
		url := sanitizeURL(r.FormValue("url"))
		telegramUsername := sanitizeInput(r.FormValue("telegram_username"))

		if slug == "" || name == "" || url == "" || telegramUsername == "" {
			http.Error(w, "All fields are required", http.StatusBadRequest)
			return
		}

		if len(name) > 100 {
			http.Error(w, "Site name too long (max 100 characters)", http.StatusBadRequest)
			return
		}

		if len(url) > 500 {
			http.Error(w, "URL too long (max 500 characters)", http.StatusBadRequest)
			return
		}

		if len(telegramUsername) > 50 {
			http.Error(w, "Telegram username too long (max 50 characters)", http.StatusBadRequest)
			return
		}

		if !slugRegex.MatchString(slug) {
			http.Error(w, "Invalid Slug format", http.StatusBadRequest)
			return
		}

		telegramUsernameClean := strings.TrimPrefix(telegramUsername, "@")
		if telegramUsernameClean == "" {
			http.Error(w, "Invalid Telegram username", http.StatusBadRequest)
			return
		}

		userID, err := findOrCreateUserByTelegramUsername(db, telegramUsernameClean)
		if err != nil {
			log.Printf("Error handling telegram username: %v", err)
			http.Error(w, "Error processing submission", http.StatusInternalServerError)
			return
		}

		changedFields := map[string]interface{}{
			"slug": slug,
			"name": name,
			"url":  url,
		}

		if err = createUpdateRequest(db, *userID, nil, "create", changedFields); err != nil {
			log.Printf("Error creating submission request: %v", err)
			http.Error(w, "Error submitting site", http.StatusInternalServerError)
			return
		}

		templatesMu.RLock()
		t := templates
		templatesMu.RUnlock()

		if t == nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		if err = t.ExecuteTemplate(w, "submit_success.html", nil); err != nil {
			log.Printf("Error rendering success template: %v", err)
			http.Error(w, "Error rendering template", http.StatusInternalServerError)
			return
		}
	}
}

func getRespondingSites(db *sql.DB) ([]models.PublicSite, error) {
	rows, err := db.Query("SELECT slug, name, url, favicon FROM sites WHERE is_up = true ORDER BY display_order")
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			log.Printf("Error closing rows: %v", cerr)
		}
	}()

	var sites []models.PublicSite
	for rows.Next() {
		var site models.PublicSite
		if scanErr := rows.Scan(&site.Slug, &site.Name, &site.URL, &site.Favicon); scanErr != nil {
			return nil, scanErr
		}
		sites = append(sites, site)
	}

	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}

	return sites, nil
}

func createUpdateRequest(db *sql.DB, userID int, siteID *int, requestType string,
	changedFields map[string]interface{}) error {
	changedFieldsJSON, err := json.Marshal(changedFields)
	if err != nil {
		return err
	}

	_, err = db.Exec(`
		INSERT INTO update_requests (user_id, site_id, request_type, changed_fields)
		VALUES ($1, $2, $3, $4)
	`, userID, siteID, requestType, changedFieldsJSON)

	return err
}

func findOrCreateUserByTelegramUsername(db *sql.DB, username string) (*int, error) {
	var userID int

	err := db.QueryRow("SELECT id FROM users WHERE telegram_username = $1", username).Scan(&userID)
	if err == nil {
		return &userID, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	err = db.QueryRow(`
		INSERT INTO users (telegram_username, telegram_id) 
		VALUES ($1, NULL) 
		RETURNING id
	`, username).Scan(&userID)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code.Name() == uniqueViolation {
			findErr := db.QueryRow("SELECT id FROM users WHERE telegram_username = $1", username).Scan(&userID)
			if findErr == nil {
				return &userID, nil
			}
			return nil, findErr
		}
		return nil, err
	}

	return &userID, nil
}
