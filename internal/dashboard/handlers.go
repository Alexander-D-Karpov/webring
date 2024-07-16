package dashboard

import (
	"database/sql"
	"html/template"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"sync"

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

func RegisterHandlers(r *mux.Router, db *sql.DB) {
	dashboardRouter := r.PathPrefix("/dashboard").Subrouter()
	dashboardRouter.Use(basicAuthMiddleware)

	dashboardRouter.HandleFunc("", dashboardHandler(db)).Methods("GET")
	dashboardRouter.HandleFunc("/add", addSiteHandler(db)).Methods("POST")
	dashboardRouter.HandleFunc("/remove/{id}", removeSiteHandler(db)).Methods("POST")
	dashboardRouter.HandleFunc("/update/{id}", updateSiteHandler(db)).Methods("POST")
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
		name := r.FormValue("name")
		url := r.FormValue("url")

		if idStr == "" || name == "" || url == "" {
			http.Error(w, "ID, Name, and URL are required", http.StatusBadRequest)
			return
		}

		id, err := strconv.Atoi(idStr)
		if err != nil {
			http.Error(w, "Invalid ID", http.StatusBadRequest)
			return
		}

		_, err = db.Exec("INSERT INTO sites (id, name, url) VALUES ($1, $2, $3)", id, name, url)
		if err != nil {
			http.Error(w, "Error adding site", http.StatusInternalServerError)
			return
		}

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
		name := r.FormValue("name")
		url := r.FormValue("url")

		if name == "" || url == "" {
			http.Error(w, "Name and URL are required", http.StatusBadRequest)
			return
		}

		_, err := db.Exec("UPDATE sites SET name = $1, url = $2 WHERE id = $3", name, url, id)
		if err != nil {
			http.Error(w, "Error updating site", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	}
}

func getAllSites(db *sql.DB) ([]models.Site, error) {
	rows, err := db.Query("SELECT id, name, url, is_up, last_check FROM sites ORDER BY id")
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
		if err := rows.Scan(&site.ID, &site.Name, &site.URL, &site.IsUp, &site.LastCheck); err != nil {
			return nil, err
		}
		site.LastCheck = math.Round(site.LastCheck * 1000)
		sites = append(sites, site)
	}
	return sites, nil
}
