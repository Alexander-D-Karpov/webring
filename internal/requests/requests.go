// Package requests owns the lifecycle of site update requests: creating them, loading
// them with their related user and site, and applying or discarding them.
//
// It exists as its own package so that both the admin dashboard and the Telegram
// approval poll can decide a request without importing each other.
package requests

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"

	"webring/internal/favicon"
	"webring/internal/models"
	"webring/internal/telegram"
)

// ErrNotFound is returned when a request no longer exists — normally because it was
// already approved or declined by someone else.
var ErrNotFound = errors.New("update request not found")

// Create inserts a new update request and returns its ID.
func Create(db *sql.DB, userID int, siteID *int, requestType string,
	changedFields map[string]interface{}) (int, error) {
	changedFieldsJSON, err := json.Marshal(changedFields)
	if err != nil {
		return 0, err
	}

	var id int
	err = db.QueryRow(`
		INSERT INTO update_requests (user_id, site_id, request_type, changed_fields)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, userID, siteID, requestType, changedFieldsJSON).Scan(&id)
	if err != nil {
		return 0, err
	}

	return id, nil
}

func nullableString(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	value := ns.String
	return &value
}

// Load fetches a request together with its submitting user and, for updates, the site
// it targets. It returns ErrNotFound when the request is gone.
func Load(db *sql.DB, requestID int) (*models.UpdateRequest, error) {
	var req models.UpdateRequest
	var changedFieldsJSON []byte
	var userTgID sql.NullInt64
	var userTgUsername, userFirstName, userLastName sql.NullString
	var siteSlug, siteName, siteURL sql.NullString

	err := db.QueryRow(`
		SELECT ur.id, ur.user_id, ur.site_id, ur.request_type, ur.changed_fields, ur.created_at,
		       u.telegram_id, u.telegram_username, u.first_name, u.last_name,
		       s.slug, s.name, s.url
		FROM update_requests ur
		JOIN users u ON ur.user_id = u.id
		LEFT JOIN sites s ON ur.site_id = s.id
		WHERE ur.id = $1
	`, requestID).Scan(&req.ID, &req.UserID, &req.SiteID, &req.RequestType,
		&changedFieldsJSON, &req.CreatedAt,
		&userTgID, &userTgUsername, &userFirstName, &userLastName,
		&siteSlug, &siteName, &siteURL)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	if err = json.Unmarshal(changedFieldsJSON, &req.ChangedFields); err != nil {
		return nil, fmt.Errorf("decoding changed fields: %w", err)
	}

	req.User = &models.User{
		ID:               req.UserID,
		TelegramUsername: nullableString(userTgUsername),
		FirstName:        nullableString(userFirstName),
		LastName:         nullableString(userLastName),
	}
	if userTgID.Valid {
		req.User.TelegramID = userTgID.Int64
	}

	if req.SiteID != nil {
		req.Site = &models.Site{
			Slug: siteSlug.String,
			Name: siteName.String,
			URL:  siteURL.String,
		}
	}

	return &req, nil
}

// Delete removes a request row.
func Delete(db *sql.DB, requestID int) error {
	_, err := db.Exec("DELETE FROM update_requests WHERE id = $1", requestID)
	return err
}

// Apply writes a request's changes into the sites table without removing the request.
func Apply(db *sql.DB, req *models.UpdateRequest) error {
	if req.RequestType == "create" {
		return applyCreate(db, req)
	}
	return applyUpdate(db, req)
}

// Approve applies a request, removes it, and notifies the submitter. The request row is
// left in place when applying fails so the caller can report the problem and retry.
func Approve(db *sql.DB, req *models.UpdateRequest) error {
	if err := Apply(db, req); err != nil {
		return err
	}

	if err := Delete(db, req.ID); err != nil {
		log.Printf("Error deleting request %d after approval: %v", req.ID, err)
	}

	go telegram.NotifyUserOfApprovedRequest(req, req.User)
	return nil
}

// Decline removes a request and notifies the submitter.
func Decline(db *sql.DB, req *models.UpdateRequest) error {
	if err := Delete(db, req.ID); err != nil {
		return err
	}

	go telegram.NotifyUserOfDeclinedRequest(req, req.User)
	return nil
}

func applyCreate(db *sql.DB, req *models.UpdateRequest) error {
	slug, slugOk := req.ChangedFields["slug"].(string)
	name, nameOk := req.ChangedFields["name"].(string)
	url, urlOk := req.ChangedFields["url"].(string)

	if !slugOk || !nameOk || !urlOk {
		return fmt.Errorf("missing required fields")
	}

	var existingID int
	err := db.QueryRow("SELECT id FROM sites WHERE slug = $1", slug).Scan(&existingID)
	if err == nil {
		return fmt.Errorf("slug '%s' is already in use by site ID %d", slug, existingID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("error checking slug availability: %w", err)
	}

	var nextID int
	if err = db.QueryRow("SELECT COALESCE(MAX(id), 0) + 1 FROM sites").Scan(&nextID); err != nil {
		return fmt.Errorf("error getting next ID: %w", err)
	}

	err = db.QueryRow("SELECT id FROM sites WHERE id = $1", nextID).Scan(&existingID)
	if err == nil {
		return fmt.Errorf("ID %d is already in use", nextID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("error checking ID availability: %w", err)
	}

	var maxDisplayOrder int
	if err = db.QueryRow("SELECT COALESCE(MAX(display_order), 0) FROM sites").Scan(&maxDisplayOrder); err != nil {
		return fmt.Errorf("error getting max display order: %w", err)
	}

	if _, err = db.Exec(`
		INSERT INTO sites (id, slug, name, url, user_id, display_order)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, nextID, slug, name, url, req.UserID, maxDisplayOrder+1); err != nil {
		return fmt.Errorf("error inserting site: %w", err)
	}

	go refreshFavicon(db, url, nextID)

	return nil
}

func applyUpdate(db *sql.DB, req *models.UpdateRequest) error {
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
		go refreshFavicon(db, newURL, *req.SiteID)
	}

	return nil
}

func refreshFavicon(db *sql.DB, url string, siteID int) {
	mediaFolder := os.Getenv("MEDIA_FOLDER")
	if mediaFolder == "" {
		mediaFolder = "media"
	}

	faviconPath, err := favicon.GetAndStoreFavicon(url, mediaFolder, siteID)
	if err != nil {
		log.Printf("Error retrieving favicon for %s: %v", url, err)
		return
	}

	if _, err = db.Exec("UPDATE sites SET favicon = $1 WHERE id = $2", faviconPath, siteID); err != nil {
		log.Printf("Error updating favicon for site %d: %v", siteID, err)
	}
}
