package dashboard

import (
	"database/sql"
	"log"
	"net/http"
	"strconv"

	"webring/internal/models"

	"github.com/gorilla/mux"
)

func RegisterSuperAdminHandlers(r *mux.Router, db *sql.DB) {
	setupRouter := r.PathPrefix("/admin/setup").Subrouter()
	setupRouter.Use(basicAuthMiddleware)

	setupRouter.HandleFunc("", superAdminHandler(db)).Methods("GET")
	setupRouter.HandleFunc("/users/{id}/toggle-admin", toggleUserAdminHandler(db)).Methods("POST")
}

func superAdminHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		templatesMu.RLock()
		t := templates
		templatesMu.RUnlock()

		if t == nil {
			log.Println("Templates not initialized")
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		users, err := getAllUsers(db)
		if err != nil {
			log.Printf("Error fetching users: %v", err)
			http.Error(w, "Error fetching users", http.StatusInternalServerError)
			return
		}

		if err = t.ExecuteTemplate(w, "super_admin.html", users); err != nil {
			log.Printf("Error rendering template: %v", err)
			http.Error(w, "Error rendering template", http.StatusInternalServerError)
			return
		}
	}
}

func toggleUserAdminHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userIDStr := mux.Vars(r)["id"]
		userID, err := strconv.Atoi(userIDStr)
		if err != nil {
			http.Error(w, "Invalid user ID", http.StatusBadRequest)
			return
		}

		if err = ClearUserSessions(db, userID); err != nil {
			log.Printf("Warning: Failed to clear sessions for user %d: %v", userID, err)
		}

		_, err = db.Exec("UPDATE users SET is_admin = NOT is_admin WHERE id = $1", userID)
		if err != nil {
			log.Printf("Error toggling admin status: %v", err)
			http.Error(w, "Error updating user", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/admin/setup", http.StatusSeeOther)
	}
}

func ClearUserSessions(db *sql.DB, userID int) error {
	_, err := db.Exec("DELETE FROM sessions WHERE user_id = $1", userID)
	if err != nil {
		log.Printf("Error clearing sessions for user %d: %v", userID, err)
	}
	return err
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
		if closeErr := rows.Close(); closeErr != nil {
			log.Printf("Error closing rows: %v", closeErr)
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
