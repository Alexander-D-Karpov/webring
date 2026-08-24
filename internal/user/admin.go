package user

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"webring/internal/approval"
	"webring/internal/models"
	"webring/internal/requests"
	"webring/internal/telegram"

	"github.com/gorilla/mux"
)

func adminDashboardHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := getAllRequests(db)
		if err != nil {
			log.Printf("Error fetching requests: %v", err)
			http.Error(w, "Error fetching requests", http.StatusInternalServerError)
			return
		}

		requestIDs := make([]int, 0, len(list))
		for _, req := range list {
			requestIDs = append(requestIDs, req.ID)
		}

		user := GetUserFromContext(r.Context())
		data := struct {
			User     *models.User
			Requests []models.UpdateRequest
			Votes    map[int]*approval.VoteSummary
			Request  *http.Request
		}{
			User:     user,
			Requests: list,
			Votes:    approval.Default().VoteSummaries(r.Context(), requestIDs),
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

// loadRequestForDecision fetches a request and takes ownership of deciding it, so that a
// dashboard click and a winning Telegram vote can never both act on the same request. It
// writes the HTTP response and returns nil when the caller should stop.
func loadRequestForDecision(w http.ResponseWriter, r *http.Request, db *sql.DB,
	requestID int, status approval.Status) (*models.UpdateRequest, *approval.Poll) {
	req, err := requests.Load(db, requestID)
	if err != nil {
		if errors.Is(err, requests.ErrNotFound) {
			http.Error(w, "Request not found", http.StatusNotFound)
		} else {
			log.Printf("Error fetching request: %v", err)
			http.Error(w, "Error fetching request", http.StatusInternalServerError)
		}
		return nil, nil
	}

	manager := approval.Default()

	// The poll has to be looked up before the request row is removed, because deleting
	// the request clears the link between them.
	poll := manager.PollForRequest(r.Context(), requestID)

	claimed, err := manager.Claim(r.Context(), requestID, status)
	if err != nil {
		log.Printf("Error claiming request %d: %v", requestID, err)
		http.Error(w, "Error processing request", http.StatusInternalServerError)
		return nil, nil
	}
	if !claimed {
		// A Telegram vote already decided this one; the request is gone.
		http.Redirect(w, r, "/admin/requests", http.StatusSeeOther)
		return nil, nil
	}

	return req, poll
}

func rejectRequestHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := GetUserFromContext(r.Context())
		if user == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		requestID, err := strconv.Atoi(mux.Vars(r)["id"])
		if err != nil {
			http.Error(w, "Invalid request ID", http.StatusBadRequest)
			return
		}

		req, poll := loadRequestForDecision(w, r, db, requestID, approval.StatusDeclined)
		if req == nil {
			return
		}

		manager := approval.Default()

		if err = requests.Decline(db, req); err != nil {
			log.Printf("Error deleting request: %v", err)
			manager.Release(r.Context(), poll)
			http.Error(w, "Error rejecting request", http.StatusInternalServerError)
			return
		}

		manager.ClosePoll(r.Context(), poll)

		go telegram.NotifyAdminsOfAction(db, "declined", req, user)

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

	var list []models.UpdateRequest
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

		list = append(list, req)
	}

	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}

	return list, nil
}

func approveRequestHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := GetUserFromContext(r.Context())
		if user == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		requestID, err := strconv.Atoi(mux.Vars(r)["id"])
		if err != nil {
			http.Error(w, "Invalid request ID", http.StatusBadRequest)
			return
		}

		req, poll := loadRequestForDecision(w, r, db, requestID, approval.StatusApproved)
		if req == nil {
			return
		}

		manager := approval.Default()

		if applyErr := requests.Approve(db, req); applyErr != nil {
			log.Printf("Error applying request: %v", applyErr)
			// The request is still pending, so hand it back for another attempt.
			manager.Release(r.Context(), poll)
			renderRequestError(w, req, applyErr)
			return
		}

		manager.ClosePoll(r.Context(), poll)

		go telegram.NotifyAdminsOfAction(db, "approved", req, user)

		http.Redirect(w, r, "/admin/requests", http.StatusSeeOther)
	}
}

func renderRequestError(w http.ResponseWriter, req *models.UpdateRequest, cause error) {
	templatesMu.RLock()
	t := templates
	templatesMu.RUnlock()

	if t == nil {
		http.Error(w, fmt.Sprintf("Error applying changes: %v", cause), http.StatusInternalServerError)
		return
	}

	data := struct {
		Error   string
		Request *models.UpdateRequest
	}{
		Error:   cause.Error(),
		Request: req,
	}

	w.WriteHeader(http.StatusBadRequest)
	if err := t.ExecuteTemplate(w, "request_error.html", data); err != nil {
		log.Printf("Error rendering error template: %v", err)
		http.Error(w, fmt.Sprintf("Error applying changes: %v", cause), http.StatusInternalServerError)
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
