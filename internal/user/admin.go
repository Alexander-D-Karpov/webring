package user

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"

	"webring/internal/telegram"

	"webring/internal/favicon"
	"webring/internal/models"

	"github.com/gorilla/mux"
)

func adminDashboardHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requests, err := getAllRequests(db)
		if err != nil {
			log.Printf("Error fetching requests: %v", err)
			http.Error(w, "Error fetching requests", http.StatusInternalServerError)
			return
		}

		user := GetUserFromContext(r.Context())
		data := struct {
			User     *models.User
			Requests []models.UpdateRequest
			Request  *http.Request
		}{
			User:     user,
			Requests: requests,
			Request:  r,
		}

		templatesMu.RLock()
		t := templates
		templatesMu.RUnlock()

		if t == nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		if err = t.ExecuteTemplate(w, "admin_dashboard.html", data); err != nil {
			log.Printf("Error rendering admin dashboard template: %v", err)
			http.Error(w, "Error rendering template", http.StatusInternalServerError)
			return
		}
	}
}

func moveSiteToPositionHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := GetUserFromContext(r.Context())
		if user == nil || !user.IsAdmin {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		idStr := mux.Vars(r)["id"]
		positionStr := mux.Vars(r)["position"]

		id, err := strconv.Atoi(idStr)
		if err != nil {
			http.Error(w, "Invalid ID", http.StatusBadRequest)
			return
		}

		targetPosition, err := strconv.Atoi(positionStr)
		if err != nil {
			http.Error(w, "Invalid position", http.StatusBadRequest)
			return
		}

		if targetPosition < 1 {
			http.Error(w, "Position must be greater than 0", http.StatusBadRequest)
			return
		}

		var currentOrder int
		err = db.QueryRow("SELECT display_order FROM sites WHERE id = $1", id).Scan(&currentOrder)
		if err != nil {
			if err == sql.ErrNoRows {
				http.Error(w, "Site not found", http.StatusNotFound)
			} else {
				log.Printf("Error fetching site order: %v", err)
				http.Error(w, "Error fetching site", http.StatusInternalServerError)
			}
			return
		}

		if currentOrder == targetPosition {
			w.Header().Set("Content-Type", "application/json")
			response := map[string]interface{}{
				"status": "no change needed",
			}
			if err = json.NewEncoder(w).Encode(response); err != nil {
				log.Printf("Error encoding response: %v", err)
			}
			return
		}

		tx, err := db.Begin()
		if err != nil {
			log.Printf("Error starting transaction: %v", err)
			http.Error(w, "Error moving site", http.StatusInternalServerError)
			return
		}
		defer func() {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
				log.Printf("Error rolling back transaction: %v", rollbackErr)
			}
		}()

		if currentOrder < targetPosition {
			_, err = tx.Exec(`
				UPDATE sites 
				SET display_order = display_order - 1 
				WHERE display_order > $1 AND display_order <= $2
			`, currentOrder, targetPosition)
		} else {
			_, err = tx.Exec(`
				UPDATE sites 
				SET display_order = display_order + 1 
				WHERE display_order >= $2 AND display_order < $1
			`, currentOrder, targetPosition)
		}

		if err != nil {
			log.Printf("Error updating display orders: %v", err)
			http.Error(w, "Error moving site", http.StatusInternalServerError)
			return
		}

		_, err = tx.Exec("UPDATE sites SET display_order = $1 WHERE id = $2", targetPosition, id)
		if err != nil {
			log.Printf("Error setting new position: %v", err)
			http.Error(w, "Error moving site", http.StatusInternalServerError)
			return
		}

		if err = tx.Commit(); err != nil {
			log.Printf("Error committing transaction: %v", err)
			http.Error(w, "Error moving site", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		response := map[string]interface{}{
			"status":       "success",
			"old_position": currentOrder,
			"new_position": targetPosition,
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			log.Printf("Error encoding response: %v", err)
		}
	}
}

func rejectRequestHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := GetUserFromContext(r.Context())
		if user == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		requestIDStr := mux.Vars(r)["id"]
		requestID, err := strconv.Atoi(requestIDStr)
		if err != nil {
			http.Error(w, "Invalid request ID", http.StatusBadRequest)
			return
		}

		if _, err = db.Exec("DELETE FROM update_requests WHERE id = $1", requestID); err != nil {
			log.Printf("Error deleting request: %v", err)
			http.Error(w, "Error rejecting request", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/admin/requests", http.StatusSeeOther)
	}
}

func getAllRequests(db *sql.DB) ([]models.UpdateRequest, error) {
	rows, err := db.Query(`
		SELECT ur.id, ur.user_id, ur.site_id, ur.request_type, ur.changed_fields, ur.created_at,
		       u.telegram_username, u.first_name, u.last_name,
		       s.slug, s.name, s.url
		FROM update_requests ur
		JOIN users u ON ur.user_id = u.id
		LEFT JOIN sites s ON ur.site_id = s.id
		ORDER BY ur.created_at DESC
	`)
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
		var userTgUsername, userFirstName, userLastName sql.NullString
		var siteSlug, siteName, siteURL sql.NullString

		scanErr := rows.Scan(&req.ID, &req.UserID, &req.SiteID, &req.RequestType,
			&changedFieldsJSON, &req.CreatedAt,
			&userTgUsername, &userFirstName, &userLastName,
			&siteSlug, &siteName, &siteURL)
		if scanErr != nil {
			return nil, scanErr
		}

		if unmarshalErr := json.Unmarshal(changedFieldsJSON, &req.ChangedFields); unmarshalErr != nil {
			return nil, unmarshalErr
		}

		req.User = &models.User{
			TelegramUsername: &userTgUsername.String,
			FirstName:        &userFirstName.String,
			LastName:         &userLastName.String,
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

func approveRequestHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := GetUserFromContext(r.Context())
		if user == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		requestIDStr := mux.Vars(r)["id"]
		requestID, err := strconv.Atoi(requestIDStr)
		if err != nil {
			http.Error(w, "Invalid request ID", http.StatusBadRequest)
			return
		}

		var req models.UpdateRequest
		var changedFieldsJSON []byte
		var userTgID sql.NullInt64
		var userTgUsername, userFirstName, userLastName sql.NullString
		err = db.QueryRow(`
			SELECT ur.user_id, ur.site_id, ur.request_type, ur.changed_fields,
			       u.telegram_id, u.telegram_username, u.first_name, u.last_name
			FROM update_requests ur
			JOIN users u ON ur.user_id = u.id
			WHERE ur.id = $1
		`, requestID).Scan(&req.UserID, &req.SiteID, &req.RequestType, &changedFieldsJSON,
			&userTgID, &userTgUsername, &userFirstName, &userLastName)
		if err != nil {
			if err == sql.ErrNoRows {
				http.Error(w, "Request not found", http.StatusNotFound)
			} else {
				log.Printf("Error fetching request: %v", err)
				http.Error(w, "Error fetching request", http.StatusInternalServerError)
			}
			return
		}

		if err = json.Unmarshal(changedFieldsJSON, &req.ChangedFields); err != nil {
			log.Printf("Error unmarshaling changed fields: %v", err)
			http.Error(w, "Error processing request", http.StatusInternalServerError)
			return
		}

		if req.RequestType == "create" {
			err = createSiteFromRequest(db, &req)
		} else {
			err = updateSiteFromRequest(db, &req)
		}

		if err != nil {
			log.Printf("Error applying request: %v", err)
			http.Error(w, "Error applying changes", http.StatusInternalServerError)
			return
		}

		if _, err = db.Exec("DELETE FROM update_requests WHERE id = $1", requestID); err != nil {
			log.Printf("Error deleting request: %v", err)
		}

		go func() {
			if userTgID.Valid && userTgID.Int64 != 0 {
				userForNotification := &models.User{
					TelegramID:       userTgID.Int64,
					TelegramUsername: &userTgUsername.String,
					FirstName:        &userFirstName.String,
					LastName:         &userLastName.String,
				}
				telegram.NotifyUserOfApprovedRequest(&req, userForNotification)
			}
		}()

		http.Redirect(w, r, "/admin/requests", http.StatusSeeOther)
	}
}

func getAllUsers(db *sql.DB) ([]models.User, error) {
	rows, err := db.Query(`
		SELECT id, telegram_id, telegram_username, first_name, last_name, is_admin, created_at
		FROM users ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			log.Printf("Error closing rows: %v", cerr)
		}
	}()

	var users []models.User
	for rows.Next() {
		var user models.User
		var telegramID sql.NullInt64
		if scanErr := rows.Scan(&user.ID, &telegramID, &user.TelegramUsername,
			&user.FirstName, &user.LastName, &user.IsAdmin, &user.CreatedAt); scanErr != nil {
			return nil, scanErr
		}

		if telegramID.Valid {
			user.TelegramID = telegramID.Int64
		} else {
			user.TelegramID = 0
		}

		users = append(users, user)
	}

	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}

	return users, nil
}

func createSiteFromRequest(db *sql.DB, req *models.UpdateRequest) error {
	slug, slugOk := req.ChangedFields["slug"].(string)
	name, nameOk := req.ChangedFields["name"].(string)
	url, urlOk := req.ChangedFields["url"].(string)

	if !slugOk || !nameOk || !urlOk {
		return fmt.Errorf("missing required fields")
	}

	var nextID int
	if err := db.QueryRow("SELECT COALESCE(MAX(id), 0) + 1 FROM sites").Scan(&nextID); err != nil {
		return fmt.Errorf("error getting next ID: %w", err)
	}

	var maxDisplayOrder int
	if err := db.QueryRow("SELECT COALESCE(MAX(display_order), 0) FROM sites").Scan(&maxDisplayOrder); err != nil {
		return fmt.Errorf("error getting max display order: %w", err)
	}

	if _, err := db.Exec(`
		INSERT INTO sites (id, slug, name, url, user_id, display_order)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, nextID, slug, name, url, req.UserID, maxDisplayOrder+1); err != nil {
		return fmt.Errorf("error inserting site: %w", err)
	}

	go func() {
		mediaFolder := os.Getenv("MEDIA_FOLDER")
		if mediaFolder == "" {
			mediaFolder = "media"
		}

		faviconPath, err := favicon.GetAndStoreFavicon(url, mediaFolder, nextID)
		if err != nil {
			log.Printf("Error retrieving favicon for %s: %v", url, err)
			return
		}

		if _, err = db.Exec("UPDATE sites SET favicon = $1 WHERE id = $2", faviconPath, nextID); err != nil {
			log.Printf("Error updating favicon for site %d: %v", nextID, err)
		}
	}()

	return nil
}

func updateSiteFromRequest(db *sql.DB, req *models.UpdateRequest) error {
	if req.SiteID == nil {
		return fmt.Errorf("site ID is required for update")
	}

	allowedFields := map[string]bool{
		"slug": true,
		"name": true,
		"url":  true,
	}

	updates := make(map[string]interface{})
	for field, value := range req.ChangedFields {
		if allowedFields[field] {
			updates[field] = value
		}
	}

	if len(updates) == 0 {
		return nil
	}

	if slug, ok := updates["slug"]; ok {
		if _, err := db.Exec("UPDATE sites SET slug = $1 WHERE id = $2", slug, *req.SiteID); err != nil {
			return fmt.Errorf("error updating slug: %w", err)
		}
	}
	if name, ok := updates["name"]; ok {
		if _, err := db.Exec("UPDATE sites SET name = $1 WHERE id = $2", name, *req.SiteID); err != nil {
			return fmt.Errorf("error updating name: %w", err)
		}
	}
	if url, ok := updates["url"]; ok {
		if _, err := db.Exec("UPDATE sites SET url = $1 WHERE id = $2", url, *req.SiteID); err != nil {
			return fmt.Errorf("error updating url: %w", err)
		}
	}

	if newURL, ok := updates["url"].(string); ok {
		go func() {
			mediaFolder := os.Getenv("MEDIA_FOLDER")
			if mediaFolder == "" {
				mediaFolder = "media"
			}

			faviconPath, err := favicon.GetAndStoreFavicon(newURL, mediaFolder, *req.SiteID)
			if err != nil {
				log.Printf("Error retrieving favicon for %s: %v", newURL, err)
				return
			}

			if _, err = db.Exec("UPDATE sites SET favicon = $1 WHERE id = $2", faviconPath, *req.SiteID); err != nil {
				log.Printf("Error updating favicon for site %d: %v", *req.SiteID, err)
			}
		}()
	}

	return nil
}
