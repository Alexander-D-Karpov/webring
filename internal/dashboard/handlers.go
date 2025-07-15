package dashboard

import (
	"database/sql"
	"errors"
	"html/template"
	"log"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"sync"

	"webring/internal/favicon"

	"github.com/gorilla/mux"
	"github.com/lib/pq"

	"webring/internal/auth"
	"webring/internal/models"
)

const (
	millisecondsMultiplier = 1000
	uniqueViolation        = "unique_violation"
)

var slugRegex = regexp.MustCompile(`^(?:[a-z0-9-]{3,50}|\d+)$`)
var (
	templates   *template.Template
	templatesMu sync.RWMutex
)

func InitTemplates(t *template.Template) {
	templatesMu.Lock()
	defer templatesMu.Unlock()
	templates = t
}

func adminSessionMiddleware(db *sql.DB) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sid := auth.GetSessionFromRequest(r)
			if sid == "" {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}

			user, err := auth.GetSessionUser(db, sid)
			if err != nil {
				auth.ClearSessionCookie(w)
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}

			if !user.IsAdmin {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func RegisterHandlers(r *mux.Router, db *sql.DB) {
	adminRouter := r.PathPrefix("/admin").Subrouter()
	adminRouter.Use(adminSessionMiddleware(db))

	adminRouter.HandleFunc("", dashboardHandler(db)).Methods("GET")
	adminRouter.HandleFunc("/add", addSiteHandler(db)).Methods("POST")
	adminRouter.HandleFunc("/remove/{id}", removeSiteHandler(db)).Methods("POST")
	adminRouter.HandleFunc("/update/{id}", updateSiteHandler(db)).Methods("POST")
	adminRouter.HandleFunc("/reorder/{id}/{direction}", reorderSiteHandler(db)).Methods("POST")
	adminRouter.HandleFunc("/move/{id}/{position}", moveSiteHandler(db)).Methods("POST")
}

func basicAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != os.Getenv("DASHBOARD_USER") || pass != os.Getenv("DASHBOARD_PASSWORD") {
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func dashboardHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		templatesMu.RLock()
		t := templates
		templatesMu.RUnlock()

		if t == nil {
			log.Println("Templates not initialized")
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		sites, err := getAllSites(db)
		if err != nil {
			log.Printf("Error fetching sites: %v", err)
			http.Error(w, "Error fetching sites", http.StatusInternalServerError)
			return
		}

		if err = t.ExecuteTemplate(w, "dashboard.html", sites); err != nil {
			log.Printf("Error rendering template: %v", err)
			http.Error(w, "Error rendering template", http.StatusInternalServerError)
			return
		}
	}
}

func addSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := r.FormValue("id")
		slug := r.FormValue("slug")
		name := r.FormValue("name")
		url := r.FormValue("url")
		telegramUsername := r.FormValue("telegram_username")

		if slug == "" || idStr == "" || name == "" || url == "" {
			http.Error(w, "ID, Slug, Name, and URL are required", http.StatusBadRequest)
			return
		}

		id, err := strconv.Atoi(idStr)
		if err != nil {
			http.Error(w, "Invalid ID", http.StatusBadRequest)
			return
		}

		if !slugRegex.MatchString(slug) {
			http.Error(w, "Invalid Slug format", http.StatusBadRequest)
			return
		}

		var maxDisplayOrder int
		err = db.QueryRow("SELECT COALESCE(MAX(display_order), 0) FROM sites").Scan(&maxDisplayOrder)
		if err != nil {
			log.Printf("Error determining display order: %v", err)
			http.Error(w, "Error determining display order", http.StatusInternalServerError)
			return
		}

		var userID *int
		if telegramUsername != "" {
			userID, err = findOrCreateUserByTelegramUsername(db, telegramUsername)
			if err != nil {
				log.Printf("Error handling telegram username: %v", err)
				http.Error(w, "Error processing telegram username", http.StatusInternalServerError)
				return
			}
		}

		_, err = db.Exec("INSERT INTO sites (id, slug, name, url, display_order, user_id) VALUES ($1, $2, $3, $4, $5, $6)",
			id, slug, name, url, maxDisplayOrder+1, userID)
		if err != nil {
			var pqErr *pq.Error
			if errors.As(err, &pqErr) && pqErr.Code.Name() == uniqueViolation {
				switch pqErr.Constraint {
				case "sites_pkey":
					http.Error(w, "Site ID is already in use", http.StatusConflict)
				case "sites_slug_key":
					http.Error(w, "Slug is already in use", http.StatusConflict)
				default:
					http.Error(w, "Unique constraint violation", http.StatusConflict)
				}
				return
			}
			log.Printf("Error adding site: %v", err)
			http.Error(w, "Error adding site", http.StatusInternalServerError)
			return
		}

		go func() {
			mediaFolder := os.Getenv("MEDIA_FOLDER")
			if mediaFolder == "" {
				mediaFolder = "media"
			}

			faviconPath, faviconErr := favicon.GetAndStoreFavicon(url, mediaFolder, id)
			if faviconErr != nil {
				log.Printf("Error retrieving favicon for %s: %v", url, faviconErr)
				return
			}

			if _, faviconErr = db.Exec("UPDATE sites SET favicon = $1 WHERE id = $2", faviconPath, id); faviconErr != nil {
				log.Printf("Error updating favicon for site %d: %v", id, faviconErr)
			}
		}()

		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	}
}

func removeSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		_, err := db.Exec("DELETE FROM sites WHERE id = $1", id)
		if err != nil {
			log.Printf("Error removing site: %v", err)
			http.Error(w, "Error removing site", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	}
}

func updateSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		slug := r.FormValue("slug")
		name := r.FormValue("name")
		url := r.FormValue("url")
		telegramUsername := r.FormValue("telegram_username")

		if slug == "" || name == "" || url == "" {
			http.Error(w, "Slug, Name and URL are required", http.StatusBadRequest)
			return
		}

		if !slugRegex.MatchString(slug) {
			http.Error(w, "Invalid Slug format", http.StatusBadRequest)
			return
		}

		var userID *int
		if telegramUsername != "" {
			var findErr error
			userID, findErr = findOrCreateUserByTelegramUsername(db, telegramUsername)
			if findErr != nil {
				log.Printf("Error handling telegram username: %v", findErr)
				http.Error(w, "Error processing telegram username", http.StatusInternalServerError)
				return
			}
		}

		_, err := db.Exec("UPDATE sites SET slug = $1, name = $2, url = $3, user_id = $4 WHERE id = $5",
			slug, name, url, userID, id)
		if err != nil {
			var pqErr *pq.Error
			if errors.As(err, &pqErr) && pqErr.Code.Name() == uniqueViolation {
				switch pqErr.Constraint {
				case "sites_slug_key":
					http.Error(w, "Slug is already in use", http.StatusConflict)
				default:
					http.Error(w, "Unique constraint violation", http.StatusConflict)
				}
				return
			}
			log.Printf("Error updating site: %v", err)
			http.Error(w, "Error updating site", http.StatusInternalServerError)
			return
		}

		go func() {
			mediaFolder := os.Getenv("MEDIA_FOLDER")
			if mediaFolder == "" {
				mediaFolder = "media"
			}

			siteID, parseErr := strconv.Atoi(id)
			if parseErr != nil {
				log.Printf("Error converting site ID to int: %v", parseErr)
				return
			}

			faviconPath, faviconErr := favicon.GetAndStoreFavicon(url, mediaFolder, siteID)
			if faviconErr != nil {
				log.Printf("Error retrieving favicon for %s: %v", url, faviconErr)
				return
			}

			if _, faviconErr = db.Exec("UPDATE sites SET favicon = $1 WHERE id = $2", faviconPath, siteID); faviconErr != nil {
				log.Printf("Error updating favicon for site %d: %v", siteID, faviconErr)
			}
		}()

		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	}
}

func reorderSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := mux.Vars(r)["id"]
		direction := mux.Vars(r)["direction"]

		id, err := strconv.Atoi(idStr)
		if err != nil {
			http.Error(w, "Invalid ID", http.StatusBadRequest)
			return
		}

		var offset int
		switch direction {
		case "up":
			offset = -1
		case "down":
			offset = 1
		default:
			http.Error(w, "Invalid direction", http.StatusBadRequest)
			return
		}

		var currentOrder int
		err = db.QueryRow("SELECT display_order FROM sites WHERE id = $1", id).Scan(&currentOrder)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "Site not found", http.StatusNotFound)
			} else {
				log.Printf("Error fetching site order: %v", err)
				http.Error(w, "Error fetching site", http.StatusInternalServerError)
			}
			return
		}

		targetOrder := currentOrder + offset

		_, err = db.Exec(`
			UPDATE sites 
			SET display_order = CASE 
				WHEN id = $1 THEN $2
				WHEN display_order = $2 THEN $3
			END
			WHERE id = $1 OR display_order = $2
		`, id, targetOrder, currentOrder)
		if err != nil {
			log.Printf("Error reordering sites: %v", err)
			http.Error(w, "Error reordering sites", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	}
}

func moveSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		var currentOrder int
		err = db.QueryRow("SELECT display_order FROM sites WHERE id = $1", id).Scan(&currentOrder)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "Site not found", http.StatusNotFound)
			} else {
				log.Printf("Error fetching site order: %v", err)
				http.Error(w, "Error fetching site", http.StatusInternalServerError)
			}
			return
		}

		if currentOrder == targetPosition {
			w.WriteHeader(http.StatusOK)
			return
		}

		tx, err := db.Begin()
		if err != nil {
			log.Printf("Error starting transaction: %v", err)
			http.Error(w, "Error moving site", http.StatusInternalServerError)
			return
		}
		defer func() {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
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

		w.WriteHeader(http.StatusOK)
	}
}

func getAllSites(db *sql.DB) ([]models.Site, error) {
	rows, err := db.Query(`
		SELECT s.id, s.slug, s.name, s.url, s.is_up, s.last_check, s.favicon, s.user_id, u.telegram_username
		FROM sites s 
		LEFT JOIN users u ON s.user_id = u.id 
		ORDER BY s.display_order
	`)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			log.Printf("Error closing rows: %v", closeErr)
		}
	}()

	var sites []models.Site
	for rows.Next() {
		var site models.Site
		var telegramUsername sql.NullString
		scanErr := rows.Scan(&site.ID, &site.Slug, &site.Name, &site.URL, &site.IsUp,
			&site.LastCheck, &site.Favicon, &site.UserID, &telegramUsername)
		if scanErr != nil {
			return nil, scanErr
		}
		site.LastCheck = math.Round(site.LastCheck * millisecondsMultiplier)

		if telegramUsername.Valid {
			site.TelegramUsername = &telegramUsername.String
		}

		sites = append(sites, site)
	}

	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}

	return sites, nil
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
