package public

import (
	"database/sql"
	"github.com/gorilla/mux"
	"html/template"
	"log"
	"net/http"
	"os"
	"sync"
	"webring/internal/models"
)

type TemplateData struct {
	Sites       []models.PublicSite
	ContactLink string
}

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
	r.HandleFunc("/", listSitesHandler(db)).Methods("GET")
}

func listSitesHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sites, err := getRespondingSites(db)
		if err != nil {
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

		data := TemplateData{sites, os.Getenv("CONTACT_LINK")}
		err = t.ExecuteTemplate(w, "sites.html", data)
		if err != nil {
			log.Printf("Error rendering template: %v", err)
			http.Error(w, "Error rendering template", http.StatusInternalServerError)
		}
	}
}

func getRespondingSites(db *sql.DB) ([]models.PublicSite, error) {
	rows, err := db.Query("SELECT slug, name, url, favicon FROM sites WHERE is_up = true ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func(rows *sql.Rows) {
		err := rows.Close()
		if err != nil {
			log.Printf("Error closing rows: %v", err)
		}
	}(rows)

	var sites []models.PublicSite
	for rows.Next() {
		var site models.PublicSite
		if err := rows.Scan(&site.Slug, &site.Name, &site.URL, &site.Favicon); err != nil {
			return nil, err
		}
		sites = append(sites, site)
	}
	return sites, nil
}
