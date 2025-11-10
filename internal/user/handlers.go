package user

import (
	"database/sql"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"

	"webring/internal/auth"
	"webring/internal/models"

	"github.com/gorilla/mux"
)

var (
	templates   *template.Template
	templatesMu sync.RWMutex
)

func InitTemplates(t *template.Template) {
	templatesMu.Lock()
	defer templatesMu.Unlock()
	templates = t
}

func userAuthMiddleware(db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sessionID := auth.GetSessionFromRequest(r)
			if sessionID == "" {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}

			user, err := auth.GetSessionUser(db, sessionID)
			if err != nil {
				auth.ClearSessionCookie(w)
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}

			r = r.WithContext(SetUserContext(r.Context(), user))
			next.ServeHTTP(w, r)
		})
	}
}

func adminAuthMiddleware(db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sessionID := auth.GetSessionFromRequest(r)
			if sessionID == "" {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}

			user, err := auth.GetSessionUser(db, sessionID)
			if err != nil || !user.IsAdmin {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}

			r = r.WithContext(SetUserContext(r.Context(), user))
			next.ServeHTTP(w, r)
		})
	}
}

func mixedAuthMiddleware(db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// First, try session authentication
			sessionID := auth.GetSessionFromRequest(r)
			if sessionID != "" {
				user, err := auth.GetSessionUser(db, sessionID)
				if err == nil && user.IsAdmin {
					// Session auth successful
					r = r.WithContext(SetUserContext(r.Context(), user))
					next.ServeHTTP(w, r)
					return
				}
			}

			// Session auth failed or user not admin, try basic auth
			username, password, ok := r.BasicAuth()
			if !ok || username != os.Getenv("DASHBOARD_USER") || password != os.Getenv("DASHBOARD_PASSWORD") {
				// Both auth methods failed
				w.Header().Set("WWW-Authenticate", `Basic realm="Admin Access Required"`)
				http.Error(w, "Authentication required", http.StatusUnauthorized)
				return
			}

			// Basic auth successful - create dummy user context
			dummyUser := &models.User{ID: -1, IsAdmin: true}
			r = r.WithContext(SetUserContext(r.Context(), dummyUser))
			next.ServeHTTP(w, r)
		})
	}
}

func RegisterHandlers(r *mux.Router, db *sql.DB) {
	r.HandleFunc("/login", loginPageHandler()).Methods("GET")
	r.HandleFunc("/auth/telegram", telegramAuthHandler(db)).Methods("GET")
	r.HandleFunc("/logout", logoutHandler(db)).Methods("POST")

	userRouter := r.PathPrefix("/user").Subrouter()
	userRouter.Use(userAuthMiddleware(db))
	userRouter.HandleFunc("", userDashboardHandler(db)).Methods("GET")
	userRouter.HandleFunc("/sites/create", createSiteRequestHandler(db)).Methods("POST")
	userRouter.HandleFunc("/sites/{id}/update", updateSiteRequestHandler(db)).Methods("POST")

	adminRouter := r.PathPrefix("/admin").Subrouter()
	adminRouter.Use(adminAuthMiddleware(db))
	adminRouter.HandleFunc("/requests", adminDashboardHandler(db)).Methods("GET")
	adminRouter.HandleFunc("/requests/{id}/approve", approveRequestHandler(db)).Methods("POST")
	adminRouter.HandleFunc("/requests/{id}/reject", rejectRequestHandler(db)).Methods("POST")
	adminRouter.HandleFunc("/api/sites/{id}/move/{position}", moveSiteToPositionHandler(db)).Methods("POST")

	userMgmtRouter := r.PathPrefix("/admin/users").Subrouter()
	userMgmtRouter.Use(mixedAuthMiddleware(db))
	userMgmtRouter.HandleFunc("", mixedAuthUsersHandler(db)).Methods("GET")
	userMgmtRouter.HandleFunc("/{id}/toggle-admin", mixedAuthToggleAdminHandler(db)).Methods("POST")
}

func mixedAuthUsersHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		currentUser := GetUserFromContext(r.Context())

		users, err := getAllUsers(db)
		if err != nil {
			log.Printf("Error fetching users: %v", err)
			http.Error(w, "Error fetching users", http.StatusInternalServerError)
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

		data := struct {
			CurrentUser *models.User
			Users       []models.User
			Request     *http.Request
		}{
			CurrentUser: currentUser,
			Users:       users,
			Request:     r,
		}

		if err = t.ExecuteTemplate(w, "users_management.html", data); err != nil {
			log.Printf("Error rendering users management template: %v", err)
			http.Error(w, "Error rendering template", http.StatusInternalServerError)
			return
		}
	}
}

func mixedAuthToggleAdminHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userIDStr := mux.Vars(r)["id"]
		userID, err := strconv.Atoi(userIDStr)
		if err != nil {
			http.Error(w, "Invalid user ID", http.StatusBadRequest)
			return
		}

		currentUser := GetUserFromContext(r.Context())

		// Don't allow modifying your own admin status (only applies to session users)
		if currentUser.ID != -1 && userID == currentUser.ID {
			http.Error(w, "Cannot modify your own admin status", http.StatusForbidden)
			return
		}

		if err = clearUserSessions(db, userID); err != nil {
			log.Printf("Warning: Failed to clear sessions for user %d: %v", userID, err)
		}

		if _, err = db.Exec("UPDATE users SET is_admin = NOT is_admin WHERE id = $1", userID); err != nil {
			log.Printf("Error toggling admin status: %v", err)
			http.Error(w, "Error updating user", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

func clearUserSessions(db *sql.DB, userID int) error {
	_, err := db.Exec("DELETE FROM sessions WHERE user_id = $1", userID)
	if err != nil {
		log.Printf("Error clearing sessions for user %d: %v", userID, err)
	}
	return err
}

func loginPageHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		templatesMu.RLock()
		t := templates
		templatesMu.RUnlock()

		if t == nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		data := struct {
			BotUsername string
		}{
			BotUsername: os.Getenv("TELEGRAM_BOT_USERNAME"),
		}

		if err := t.ExecuteTemplate(w, "login.html", data); err != nil {
			log.Printf("Error rendering login template: %v", err)
			http.Error(w, "Error rendering template", http.StatusInternalServerError)
			return
		}
	}
}

func telegramAuthHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
		if botToken == "" {
			http.Error(w, "Bot token not configured", http.StatusInternalServerError)
			return
		}

		tgUser, err := auth.VerifyTelegramAuth(r.URL.Query(), botToken)
		if err != nil {
			log.Printf("Telegram auth verification failed: %v", err)
			http.Error(w, "Authentication failed", http.StatusUnauthorized)
			return
		}

		user, err := getOrCreateUser(db, tgUser)
		if err != nil {
			log.Printf("Error getting/creating user: %v", err)
			http.Error(w, "Error processing authentication", http.StatusInternalServerError)
			return
		}

		session, err := auth.CreateSession(db, user.ID)
		if err != nil {
			log.Printf("Error creating session: %v", err)
			http.Error(w, "Error creating session", http.StatusInternalServerError)
			return
		}

		auth.SetSessionCookie(w, session.ID)

		if user.IsAdmin {
			http.Redirect(w, r, "/admin/requests", http.StatusSeeOther)
		} else {
			http.Redirect(w, r, "/user", http.StatusSeeOther)
		}
	}
}

func logoutHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := auth.GetSessionFromRequest(r)
		if sessionID != "" {
			if err := auth.DeleteSession(db, sessionID); err != nil {
				log.Printf("Error deleting session: %v", err)
			}
		}
		auth.ClearSessionCookie(w)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func getOrCreateUser(db *sql.DB, tgUser *auth.TelegramUser) (*models.User, error) {
	var user models.User

	err := db.QueryRow(`
		SELECT id, telegram_id, telegram_username, first_name, last_name, is_admin, created_at
		FROM users WHERE telegram_id = $1
	`, tgUser.ID).Scan(
		&user.ID, &user.TelegramID, &user.TelegramUsername,
		&user.FirstName, &user.LastName, &user.IsAdmin, &user.CreatedAt)

	if err == nil {
		if _, err = db.Exec(`
			UPDATE users SET telegram_username = $1, first_name = $2, last_name = $3
			WHERE telegram_id = $4
		`, &tgUser.Username, &tgUser.FirstName, &tgUser.LastName, tgUser.ID); err != nil {
			return nil, err
		}
		return &user, nil
	}

	if err != sql.ErrNoRows {
		return nil, err
	}

	err = db.QueryRow(`
		INSERT INTO users (telegram_id, telegram_username, first_name, last_name)
		VALUES ($1, $2, $3, $4)
		RETURNING id, telegram_id, telegram_username, first_name, last_name, is_admin, created_at
	`, tgUser.ID, &tgUser.Username, &tgUser.FirstName, &tgUser.LastName).Scan(
		&user.ID, &user.TelegramID, &user.TelegramUsername,
		&user.FirstName, &user.LastName, &user.IsAdmin, &user.CreatedAt)

	if err != nil {
		return nil, err
	}

	return &user, nil
}
