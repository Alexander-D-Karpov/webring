package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"webring/internal/api/middleware"
	"webring/internal/models"

	"github.com/gorilla/mux"
)

func RegisterHandlers(r *mux.Router, db *sql.DB) {
	apiRouter := r.PathPrefix("").Subrouter()
	apiRouter.Use(middleware.CORSMiddleware)

	apiRouter.HandleFunc("/{id}/prev/", previousSiteHandler(db)).Methods("GET")
	apiRouter.HandleFunc("/{id}/next/", nextSiteHandler(db)).Methods("GET")
	apiRouter.HandleFunc("/{id}/prev", previousSiteRedirectHandler(db)).Methods("GET")
	apiRouter.HandleFunc("/{id}/next", nextSiteRedirectHandler(db)).Methods("GET")
	apiRouter.HandleFunc("/{id}/data", siteDataHandler(db)).Methods("GET")
	apiRouter.HandleFunc("/{id}/random/", randomSiteHandler(db)).Methods("GET")
	apiRouter.HandleFunc("/{id}/random", randomSiteRedirectHandler(db)).Methods("GET")
	apiRouter.HandleFunc("/sites", listPublicSitesHandler(db)).Methods("GET")
}

func previousSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		site, err := getPreviousSite(db, id)
		if err != nil {
			http.Error(w, "Site not found", http.StatusNotFound)
			return
		}

		response := struct {
			Previous *models.PublicSite `json:"previous"`
		}{
			Previous: site,
		}

		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(response)
		if err != nil {
			http.Error(w, "Error encoding response", http.StatusInternalServerError)
			return
		}
	}
}

func nextSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		site, err := getNextSite(db, id)
		if err != nil {
			http.Error(w, "Site not found", http.StatusNotFound)
			return
		}

		response := struct {
			Next *models.PublicSite `json:"next"`
		}{
			Next: site,
		}

		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(response)
		if err != nil {
			http.Error(w, "Error encoding response", http.StatusInternalServerError)
			return
		}
	}
}

func randomSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		currentID := mux.Vars(r)["id"]
		site, err := getRandomSite(db, currentID)
		if err != nil {
			if err.Error() == "no available sites found" {
				http.Error(w, "No available sites found", http.StatusNotFound)
			} else {
				log.Printf("Error fetching random site: %v", err)
				http.Error(w, "Error fetching random site", http.StatusInternalServerError)
			}
			return
		}

		response := struct {
			Random *models.PublicSite `json:"random"`
		}{
			Random: site,
		}

		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(response)
		if err != nil {
			return
		}
	}
}

func siteDataHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]

		data, err := getSiteData(db, id)
		if err != nil {
			http.Error(w, "Error fetching site data", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(data)
		if err != nil {
			http.Error(w, "Error encoding response", http.StatusInternalServerError)
			return
		}
	}
}

func previousSiteRedirectHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		site, err := getPreviousSite(db, id)
		if err != nil {
			http.Error(w, "Site not found", http.StatusNotFound)
			return
		}
		http.Redirect(w, r, site.URL, http.StatusFound)
	}
}

func nextSiteRedirectHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		site, err := getNextSite(db, id)
		if err != nil {
			http.Error(w, "Site not found", http.StatusNotFound)
			return
		}
		http.Redirect(w, r, site.URL, http.StatusFound)
	}
}

func randomSiteRedirectHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		currentID := mux.Vars(r)["id"]
		site, err := getRandomSite(db, currentID)
		if err != nil {
			if err.Error() == "no available sites found" {
				http.Error(w, "No available sites found", http.StatusNotFound)
			} else {
				log.Printf("Error fetching random site: %v", err)
				http.Error(w, "Error fetching random site", http.StatusInternalServerError)
			}
			return
		}
		http.Redirect(w, r, site.URL, http.StatusFound)
	}
}

func listPublicSitesHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sites, err := getRespondingSites(db)
		if err != nil {
			http.Error(w, "Error fetching sites", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(sites)
		if err != nil {
			http.Error(w, "Error encoding response", http.StatusInternalServerError)
			return
		}
	}
}

func getRespondingSites(db *sql.DB) ([]models.PublicSite, error) {
	rows, err := db.Query("SELECT id, name, url, favicon FROM sites WHERE is_up = true ORDER BY id")
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
		if err := rows.Scan(&site.ID, &site.Name, &site.URL, &site.Favicon); err != nil {
			return nil, err
		}
		sites = append(sites, site)
	}
	return sites, nil
}

func getNextSite(db *sql.DB, currentID string) (*models.PublicSite, error) {
	var site models.PublicSite
	err := db.QueryRow(`
        WITH ring AS (
            SELECT id, name, url, favicon, is_up,
                   LEAD(id) OVER (ORDER BY id) AS next_id,
                   LAG(id) OVER (ORDER BY id) AS prev_id
            FROM sites
            WHERE is_up = true
        )
        SELECT id, name, url, favicon
        FROM ring
        WHERE (id = $1 AND next_id IS NOT NULL AND next_id = (SELECT MIN(id) FROM ring))
           OR (id > $1 AND is_up = true)
           OR (id = (SELECT MIN(id) FROM ring WHERE is_up = true) AND $1 = (SELECT MAX(id) FROM ring WHERE is_up = true))
        ORDER BY CASE
            WHEN id > $1 THEN id
            ELSE (SELECT MAX(id) FROM ring) + 1
        END
        LIMIT 1
    `, currentID).Scan(&site.ID, &site.Name, &site.URL, &site.Favicon)
	if err != nil {
		return nil, err
	}
	return &site, nil
}

func getPreviousSite(db *sql.DB, currentID string) (*models.PublicSite, error) {
	var site models.PublicSite
	err := db.QueryRow(`
        WITH ring AS (
            SELECT id, name, url, favicon, is_up,
                   LEAD(id) OVER (ORDER BY id) AS next_id,
                   LAG(id) OVER (ORDER BY id) AS prev_id
            FROM sites
            WHERE is_up = true
        )
        SELECT id, name, url, favicon
        FROM ring
        WHERE (id = $1 AND prev_id IS NOT NULL AND prev_id = (SELECT MAX(id) FROM ring))
           OR (id < $1 AND is_up = true)
           OR (id = (SELECT MAX(id) FROM ring WHERE is_up = true) AND $1 = (SELECT MIN(id) FROM ring WHERE is_up = true))
        ORDER BY CASE
            WHEN id < $1 THEN id
            ELSE 0
        END DESC
        LIMIT 1
    `, currentID).Scan(&site.ID, &site.Name, &site.URL, &site.Favicon)
	if err != nil {
		return nil, err
	}
	return &site, nil
}

func getSiteData(db *sql.DB, id string) (*models.SiteData, error) {
	var data models.SiteData
	err := db.QueryRow(`
        WITH ring AS (
            SELECT id, name, url, favicon, is_up,
                   LAG(id) OVER (ORDER BY id) AS prev_id,
                   LAG(name) OVER (ORDER BY id) AS prev_name,
                   LAG(url) OVER (ORDER BY id) AS prev_url,
                   LAG(favicon) OVER (ORDER BY id) AS prev_favicon,
                   LEAD(id) OVER (ORDER BY id) AS next_id,
                   LEAD(name) OVER (ORDER BY id) AS next_name,
                   LEAD(url) OVER (ORDER BY id) AS next_url,
                   LEAD(favicon) OVER (ORDER BY id) AS next_favicon
            FROM sites
            WHERE is_up = true
        ),
        wrapped AS (
            SELECT *,
                   FIRST_VALUE(id) OVER (ORDER BY id) AS first_id,
                   FIRST_VALUE(name) OVER (ORDER BY id) AS first_name,
                   FIRST_VALUE(url) OVER (ORDER BY id) AS first_url,
                   FIRST_VALUE(favicon) OVER (ORDER BY id) AS first_favicon,
                   LAST_VALUE(id) OVER (ORDER BY id RANGE BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) AS last_id,
                   LAST_VALUE(name) OVER (ORDER BY id RANGE BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) AS last_name,
                   LAST_VALUE(url) OVER (ORDER BY id RANGE BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) AS last_url,
                   LAST_VALUE(favicon) OVER (ORDER BY id RANGE BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) AS last_favicon
            FROM ring
        )
        SELECT 
            COALESCE(prev_id, last_id) AS prev_id,
            COALESCE(prev_name, last_name) AS prev_name,
            COALESCE(prev_url, last_url) AS prev_url,
            COALESCE(prev_favicon, last_favicon) AS prev_favicon,
            id AS curr_id,
            name AS curr_name,
            url AS curr_url,
            favicon AS curr_favicon,
            COALESCE(next_id, first_id) AS next_id,
            COALESCE(next_name, first_name) AS next_name,
            COALESCE(next_url, first_url) AS next_url,
            COALESCE(next_favicon, first_favicon) AS next_favicon
        FROM wrapped
        WHERE id = $1
    `, id).Scan(
		&data.Prev.ID, &data.Prev.Name, &data.Prev.URL, &data.Prev.Favicon,
		&data.Curr.ID, &data.Curr.Name, &data.Curr.URL, &data.Curr.Favicon,
		&data.Next.ID, &data.Next.Name, &data.Next.URL, &data.Next.Favicon,
	)
	if err != nil {
		return nil, err
	}
	return &data, nil
}

func getRandomSite(db *sql.DB, currentID string) (*models.PublicSite, error) {
	var site models.PublicSite
	err := db.QueryRow(`
        SELECT id, name, url, favicon
        FROM sites
        WHERE is_up = true AND id != $1
        ORDER BY RANDOM()
        LIMIT 1
    `, currentID).Scan(&site.ID, &site.Name, &site.URL, &site.Favicon)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("no available sites found")
		}
		return nil, fmt.Errorf("database error: %v", err)
	}
	return &site, nil
}
