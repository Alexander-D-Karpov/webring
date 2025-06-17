package dashboard

import (
	"database/sql"
	"html/template"
	"log"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"sync"
	"webring/internal/favicon"

	"webring/internal/models"

	"github.com/gorilla/mux"
)

var slugRegex = regexp.MustCompile(`^[a-z0-9-]{3,50}$`)
var (
	templates   *template.Template
	templatesMu sync.RWMutex
)

func InitTemplates(t *template.Template) {
	templatesMu.Lock()
	defer templatesMu.Unlock()
	templates = t
}

func RegisterHandlers(r *mux.Router, db *sql.DB) {
	dashboardRouter := r.PathPrefix("/dashboard").Subrouter()
	dashboardRouter.Use(basicAuthMiddleware)

	dashboardRouter.HandleFunc("", dashboardHandler(db)).Methods("GET")
	dashboardRouter.HandleFunc("/add", addSiteHandler(db)).Methods("POST")
	dashboardRouter.HandleFunc("/remove/{id}", removeSiteHandler(db)).Methods("POST")
	dashboardRouter.HandleFunc("/update/{id}", updateSiteHandler(db)).Methods("POST")
	dashboardRouter.HandleFunc("/reorder/{id}/{offset}", reorderSiteHandler(db)).Methods("POST")
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
	return func(w http.ResponseWriter, r *http.Request) {
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

		err = t.ExecuteTemplate(w, "dashboard.html", sites)
		if err != nil {
			log.Printf("Error rendering template: %v", err)
			http.Error(w, "Error rendering template", http.StatusInternalServerError)
		}
	}
}

func addSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := r.FormValue("id")
		slug := r.FormValue("slug")
		name := r.FormValue("name")
		url := r.FormValue("url")

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
			http.Error(w, "Invalid Slug", http.StatusBadRequest)
			return
		}

		result, err := db.Exec("INSERT INTO sites (id, slug, name, url) VALUES ($1, $2, $3, $4)", id, slug, name, url)
		if err != nil {
			http.Error(w, "Error adding site", http.StatusInternalServerError)
			return
		}
		insertedID, _ := result.LastInsertId()

		// Start a goroutine to fetch and store the favicon
		go func() {
			mediaFolder := os.Getenv("MEDIA_FOLDER")
			if mediaFolder == "" {
				mediaFolder = "media"
			}

			faviconPath, err := favicon.GetAndStoreFavicon(url, mediaFolder, int(insertedID))
			if err != nil {
				log.Printf("Error retrieving favicon for %s: %v", url, err)
				return
			}

			_, err = db.Exec("UPDATE sites SET favicon = $1 WHERE id = $2", faviconPath, insertedID)
			if err != nil {
				log.Printf("Error updating favicon for site %d: %v", insertedID, err)
			}
		}()

		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	}
}

func removeSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		_, err := db.Exec("DELETE FROM sites WHERE id = $1", id)
		if err != nil {
			http.Error(w, "Error removing site", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	}
}

func updateSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		slug := r.FormValue("slug")
		name := r.FormValue("name")
		url := r.FormValue("url")

		if slug == "" || name == "" || url == "" {
			http.Error(w, "Slug, Name and URL are required", http.StatusBadRequest)
			return
		}

		if !slugRegex.MatchString(slug) {
			http.Error(w, "Invalid Slug", http.StatusBadRequest)
			return
		}

		_, err := db.Exec("UPDATE sites SET slug = $1, name = $2, url = $3 WHERE id = $4", slug, name, url, id)
		if err != nil {
			http.Error(w, "Error updating site", http.StatusInternalServerError)
			return
		}

		go func() {
			mediaFolder := os.Getenv("MEDIA_FOLDER")
			if mediaFolder == "" {
				mediaFolder = "media"
			}

			siteId, _ := strconv.Atoi(id)
			faviconPath, err := favicon.GetAndStoreFavicon(url, mediaFolder, siteId)
			if err != nil {
				log.Printf("Error retrieving favicon for %s: %v", url, err)
				return
			}

			_, err = db.Exec("UPDATE sites SET favicon = $1 WHERE id = $2", faviconPath, id)
			if err != nil {
				log.Printf("Error updating favicon for site %d: %v", id, err)
			}
		}()

		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	}
}

func reorderSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := mux.Vars(r)["id"]
		offsetStr := mux.Vars(r)["offset"]

		id, err := strconv.Atoi(idStr)
		if err != nil {
			http.Error(w, "Invalid ID", http.StatusBadRequest)
			return
		}

		offset, err := strconv.Atoi(offsetStr)
		if err != nil {
			http.Error(w, "Invalid Offset", http.StatusBadRequest)
			return
		}

		swapId := id + offset

		_, err = db.Exec(`
			UPDATE sites
			SET id = CASE id
				 WHEN $1 THEN $2
				 WHEN $2 THEN $1
			END
			WHERE id IN ($1, $2);
		`, id, swapId)
		if err != nil {
			println(err.Error())
			http.Error(w, "Error updating site", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	}
}

func getAllSites(db *sql.DB) ([]models.Site, error) {
	rows, err := db.Query("SELECT id, slug, name, url, is_up, last_check, favicon FROM sites ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func(rows *sql.Rows) {
		err := rows.Close()
		if err != nil {
			log.Printf("Error closing rows: %v", err)
		}
	}(rows)

	var sites []models.Site
	for rows.Next() {
		var site models.Site
		err := rows.Scan(&site.ID, &site.Slug, &site.Name, &site.URL, &site.IsUp, &site.LastCheck, &site.Favicon)
		if err != nil {
			return nil, err
		}
		site.LastCheck = math.Round(site.LastCheck * 1000)
		sites = append(sites, site)
	}
	return sites, nil
}
