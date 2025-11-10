package user

import (
	"database/sql"
	"encoding/json"
	"html"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"webring/internal/models"
	"webring/internal/telegram"

	"github.com/gorilla/mux"
)

var slugRegex = regexp.MustCompile(`^[a-z0-9-]{3,50}$`)

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

func userDashboardHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := GetUserFromContext(r.Context())
		if user == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		templatesMu.RLock()
		t := templates
		templatesMu.RUnlock()

		if t == nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		sites, err := getUserSites(db, user.ID)
		if err != nil {
			log.Printf("Error fetching user sites: %v", err)
			http.Error(w, "Error fetching sites", http.StatusInternalServerError)
			return
		}

		requests, err := getUserRequests(db, user.ID)
		if err != nil {
			log.Printf("Error fetching user requests: %v", err)
			http.Error(w, "Error fetching requests", http.StatusInternalServerError)
			return
		}

		data := struct {
			User     *models.User
			Sites    []models.Site
			Requests []models.UpdateRequest
			Request  *http.Request
		}{
			User:     user,
			Sites:    sites,
			Requests: requests,
			Request:  r,
		}

		if err = t.ExecuteTemplate(w, "user_dashboard.html", data); err != nil {
			log.Printf("Error rendering user dashboard template: %v", err)
			http.Error(w, "Error rendering template", http.StatusInternalServerError)
			return
		}
	}
}

func createSiteRequestHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := GetUserFromContext(r.Context())
		if user == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		slug := sanitizeInput(r.FormValue("slug"))
		name := sanitizeInput(r.FormValue("name"))
		url := sanitizeURL(r.FormValue("url"))

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

		if !slugRegex.MatchString(slug) {
			http.Error(w, "Invalid Slug", http.StatusBadRequest)
			return
		}

		changedFields := map[string]interface{}{
			"slug": slug,
			"name": name,
			"url":  url,
		}

		if err := createUpdateRequest(db, user.ID, nil, "create", changedFields); err != nil {
			log.Printf("Error creating site request: %v", err)
			http.Error(w, "Error creating request", http.StatusInternalServerError)
			return
		}

		go func() {
			req := &models.UpdateRequest{
				UserID:        user.ID,
				RequestType:   "create",
				ChangedFields: changedFields,
				CreatedAt:     time.Now(),
			}
			telegram.NotifyAdminsOfNewRequest(db, req, user)
		}()

		http.Redirect(w, r, "/user", http.StatusSeeOther)
	}
}

func updateSiteRequestHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := GetUserFromContext(r.Context())
		if user == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		siteIDStr := mux.Vars(r)["id"]
		siteID, err := strconv.Atoi(siteIDStr)
		if err != nil {
			http.Error(w, "Invalid site ID", http.StatusBadRequest)
			return
		}

		var ownerID int
		err = db.QueryRow("SELECT user_id FROM sites WHERE id = $1", siteID).Scan(&ownerID)
		if err != nil {
			http.Error(w, "Site not found", http.StatusNotFound)
			return
		}

		if ownerID != user.ID {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		var currentSite models.Site
		err = db.QueryRow(`
			SELECT slug, name, url FROM sites WHERE id = $1
		`, siteID).Scan(&currentSite.Slug, &currentSite.Name, &currentSite.URL)
		if err != nil {
			http.Error(w, "Error fetching site", http.StatusInternalServerError)
			return
		}

		changedFields := make(map[string]interface{})

		if newSlug := sanitizeInput(r.FormValue("slug")); newSlug != "" && newSlug != currentSite.Slug {
			if !slugRegex.MatchString(newSlug) {
				http.Error(w, "Invalid Slug", http.StatusBadRequest)
				return
			}
			changedFields["slug"] = newSlug
		}

		if newName := sanitizeInput(r.FormValue("name")); newName != "" && newName != currentSite.Name {
			if len(newName) > 100 {
				http.Error(w, "Site name too long (max 100 characters)", http.StatusBadRequest)
				return
			}
			changedFields["name"] = newName
		}

		if newURL := sanitizeURL(r.FormValue("url")); newURL != "" && newURL != currentSite.URL {
			if len(newURL) > 500 {
				http.Error(w, "URL too long (max 500 characters)", http.StatusBadRequest)
				return
			}
			changedFields["url"] = newURL
		}

		if len(changedFields) == 0 {
			http.Redirect(w, r, "/user", http.StatusSeeOther)
			return
		}

		if err = createUpdateRequest(db, user.ID, &siteID, "update", changedFields); err != nil {
			log.Printf("Error creating update request: %v", err)
			http.Error(w, "Error creating request", http.StatusInternalServerError)
			return
		}

		go func() {
			req := &models.UpdateRequest{
				UserID:        user.ID,
				SiteID:        &siteID,
				RequestType:   "update",
				ChangedFields: changedFields,
				CreatedAt:     time.Now(),
				Site: &models.Site{
					Slug: currentSite.Slug,
					Name: currentSite.Name,
					URL:  currentSite.URL,
				},
			}
			telegram.NotifyAdminsOfNewRequest(db, req, user)
		}()

		http.Redirect(w, r, "/user", http.StatusSeeOther)
	}
}

func getUserSites(db *sql.DB, userID int) ([]models.Site, error) {
	rows, err := db.Query(`
		SELECT id, slug, name, url, is_up, last_check, favicon
		FROM sites WHERE user_id = $1 ORDER BY display_order
	`, userID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			log.Printf("Error closing rows: %v", cerr)
		}
	}()

	var sites []models.Site
	for rows.Next() {
		var site models.Site
		scanErr := rows.Scan(&site.ID, &site.Slug, &site.Name, &site.URL,
			&site.IsUp, &site.LastCheck, &site.Favicon)
		if scanErr != nil {
			return nil, scanErr
		}
		sites = append(sites, site)
	}

	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}

	return sites, nil
}

func getUserRequests(db *sql.DB, userID int) ([]models.UpdateRequest, error) {
	rows, err := db.Query(`
		SELECT ur.id, ur.site_id, ur.request_type, ur.changed_fields, ur.created_at,
		       s.slug, s.name, s.url
		FROM update_requests ur
		LEFT JOIN sites s ON ur.site_id = s.id
		WHERE ur.user_id = $1
		ORDER BY ur.created_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			log.Printf("Error closing rows: %v", cerr)
		}
	}()

	var requests []models.UpdateRequest
	for rows.Next() {
		var req models.UpdateRequest
		var changedFieldsJSON []byte
		var siteSlug, siteName, siteURL sql.NullString

		scanErr := rows.Scan(&req.ID, &req.SiteID, &req.RequestType, &changedFieldsJSON, &req.CreatedAt,
			&siteSlug, &siteName, &siteURL)
		if scanErr != nil {
			return nil, scanErr
		}

		if unmarshalErr := json.Unmarshal(changedFieldsJSON, &req.ChangedFields); unmarshalErr != nil {
			return nil, unmarshalErr
		}

		if req.SiteID != nil {
			req.Site = &models.Site{
				Slug: siteSlug.String,
				Name: siteName.String,
				URL:  siteURL.String,
			}
		}

		requests = append(requests, req)
	}

	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}

	return requests, nil
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
