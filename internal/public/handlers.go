package public

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"webring/internal/telegram"

	"webring/internal/auth"
	"webring/internal/models"

	"github.com/gorilla/mux"
	"github.com/lib/pq"
)

const uniqueViolation = "unique_violation"

type TemplateData struct {
	Sites       []models.PublicSite
	ContactLink string
	User        *models.User
	Request     *http.Request
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
	return strings.TrimSpace(input)
}

func sanitizeURL(input string) string {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		trimmed = "https://" + trimmed
	}
	return trimmed
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
			Request:     r,
		}

		if err = t.ExecuteTemplate(w, "sites.html", data); err != nil {
			log.Printf("Error rendering template: %v", err)
			http.Error(w, "Error rendering template", http.StatusInternalServerError)
			return
		}
	}
}

func submitSitePageHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if os.Getenv("REQUIRE_LOGIN_FOR_SUBMIT") == "true" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		templatesMu.RLock()
		t := templates
		templatesMu.RUnlock()

		if t == nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		data := struct {
			Error   string
			Request *http.Request
		}{
			Error:   "",
			Request: r,
		}

		if err := t.ExecuteTemplate(w, "submit_site.html", data); err != nil {
			log.Printf("Error rendering submit site template: %v", err)
			http.Error(w, "Error rendering template", http.StatusInternalServerError)
			return
		}
	}
}

func submitSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if os.Getenv("REQUIRE_LOGIN_FOR_SUBMIT") == "true" {
			sessionID := auth.GetSessionFromRequest(r)
			if sessionID == "" {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			_, err := auth.GetSessionUser(db, sessionID)
			if err != nil {
				auth.ClearSessionCookie(w)
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
		}

		slug := sanitizeInput(r.FormValue("slug"))
		name := sanitizeInput(r.FormValue("name"))
		url := sanitizeURL(r.FormValue("url"))
		telegramUsername := sanitizeInput(r.FormValue("telegram_username"))

		if slug == "" || name == "" || url == "" {
			http.Error(w, "Slug, Name, and URL are required", http.StatusBadRequest)
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

		var existingID int
		err := db.QueryRow("SELECT id FROM sites WHERE slug = $1", slug).Scan(&existingID)
		if err == nil {
			templatesMu.RLock()
			t := templates
			templatesMu.RUnlock()

			if t == nil {
				http.Error(w, fmt.Sprintf("Slug '%s' is already in use", slug), http.StatusConflict)
				return
			}

			data := struct {
				Error   string
				Request *http.Request
			}{
				Error:   fmt.Sprintf("The slug '%s' is already in use. Please choose a different slug and try again.", slug),
				Request: r,
			}

			w.WriteHeader(http.StatusConflict)
			if err = t.ExecuteTemplate(w, "submit_site.html", data); err != nil {
				log.Printf("Error rendering submit site template: %v", err)
				http.Error(w, fmt.Sprintf("Slug '%s' is already in use", slug), http.StatusConflict)
			}
			return
		}
		if err != sql.ErrNoRows {
			log.Printf("Error checking slug availability: %v", err)
			http.Error(w, "Error checking slug availability", http.StatusInternalServerError)
			return
		}

		var userID *int
		var submittingUser *models.User

		if telegramUsername != "" {
			telegramUsernameClean := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(telegramUsername), "@"))
			if matched, err := regexp.MatchString("^[a-zA-Z0-9_]{4,32}$", telegramUsernameClean); !matched {
				if err != nil {
					log.Printf("Error validating telegram username: %v", err)
				} else {
					log.Println("Invalid Telegram username format")
				}
				http.Error(w, "Invalid Telegram username format", http.StatusBadRequest)
				return
			}

			var err error
			userID, err = findOrCreateUserByTelegramUsername(db, telegramUsernameClean)
			if err != nil {
				log.Printf("Error handling telegram username: %v", err)
				http.Error(w, "Error processing submission", http.StatusInternalServerError)
				return
			}

			if userID != nil {
				submittingUser, err = getUserByID(db, *userID)
				if err != nil {
					log.Printf("Error getting user by ID: %v", err)
				}
			}
		}

		var requestUserID int
		if userID != nil {
			requestUserID = *userID
		} else {
			var err error
			requestUserID, err = getOrCreateAnonymousAdminUser(db)
			if err != nil {
				log.Printf("Error getting anonymous admin user: %v", err)
				http.Error(w, "Error processing submission", http.StatusInternalServerError)
				return
			}
		}

		changedFields := map[string]interface{}{
			"slug": slug,
			"name": name,
			"url":  url,
		}

		if err := createUpdateRequest(db, requestUserID, nil, "create", changedFields); err != nil {
			log.Printf("Error creating submission request: %v", err)
			http.Error(w, "Error submitting site", http.StatusInternalServerError)
			return
		}

		go func() {
			if submittingUser == nil {
				submittingUser = &models.User{
					ID: requestUserID,
					TelegramUsername: func() *string {
						if telegramUsername != "" {
							clean := strings.ToLower(strings.TrimPrefix(telegramUsername, "@"))
							return &clean
						}
						return nil
					}(),
				}
			}

			req := &models.UpdateRequest{
				UserID:        requestUserID,
				SiteID:        nil,
				RequestType:   "create",
				ChangedFields: changedFields,
				CreatedAt:     time.Now(),
			}
			telegram.NotifyAdminsOfNewRequest(db, req, submittingUser)
		}()

		templatesMu.RLock()
		t := templates
		templatesMu.RUnlock()

		if t == nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		if err := t.ExecuteTemplate(w, "submit_success.html", nil); err != nil {
			log.Printf("Error rendering success template: %v", err)
			http.Error(w, "Error rendering template", http.StatusInternalServerError)
			return
		}
	}
}

func getUserByID(db *sql.DB, userID int) (*models.User, error) {
	var user models.User
	var telegramID sql.NullInt64
	err := db.QueryRow(`
		SELECT id, telegram_id, telegram_username, first_name, last_name, is_admin, created_at 
		FROM users WHERE id = $1
	`, userID).Scan(&user.ID, &telegramID, &user.TelegramUsername,
		&user.FirstName, &user.LastName, &user.IsAdmin, &user.CreatedAt)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	if telegramID.Valid {
		user.TelegramID = telegramID.Int64
	}

	return &user, nil
}

func getOrCreateAnonymousAdminUser(db *sql.DB) (int, error) {
	var userID int

	err := db.QueryRow(`
		SELECT id FROM users 
		WHERE telegram_id IS NULL AND telegram_username IS NULL 
		LIMIT 1
		FOR UPDATE
	`).Scan(&userID)

	if err == nil {
		return userID, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("error querying anonymous user: %w", err)
	}

	err = db.QueryRow(`
		INSERT INTO users (telegram_username, telegram_id) 
		VALUES (NULL, NULL) 
		ON CONFLICT DO NOTHING
		RETURNING id
	`).Scan(&userID)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = db.QueryRow(`
				SELECT id FROM users 
				WHERE telegram_id IS NULL AND telegram_username IS NULL 
				LIMIT 1
			`).Scan(&userID)
			if err == nil {
				return userID, nil
			}
		}
		return 0, fmt.Errorf("error creating anonymous user: %w", err)
	}

	return userID, nil
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
	if username == "" {
		return nil, nil
	}

	var userID int
	usernameLower := strings.ToLower(username)

	err := db.QueryRow("SELECT id FROM users WHERE LOWER(telegram_username) = LOWER($1)", usernameLower).Scan(&userID)
	if err == nil {
		return &userID, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("error querying user: %w", err)
	}

	err = db.QueryRow(`
		INSERT INTO users (telegram_username, telegram_id) 
		VALUES ($1, NULL) 
		RETURNING id
	`, usernameLower).Scan(&userID)

	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code.Name() == uniqueViolation {
			err = db.QueryRow("SELECT id FROM users WHERE LOWER(telegram_username) = LOWER($1)", usernameLower).Scan(&userID)
			if err == nil {
				return &userID, nil
			}
		}
		return nil, fmt.Errorf("error creating user: %w", err)
	}

	return &userID, nil
}
