package api

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"webring/internal/models"

	"github.com/gorilla/mux"
)

func RegisterHandlers(r *mux.Router, db *sql.DB) {
	r.HandleFunc("/site/{id}/previous", previousSiteHandler(db)).Methods("GET")
	r.HandleFunc("/site/{id}/next", nextSiteHandler(db)).Methods("GET")
	r.HandleFunc("/site/{id}/previous/", previousSiteRedirectHandler(db)).Methods("GET")
	r.HandleFunc("/site/{id}/next/", nextSiteRedirectHandler(db)).Methods("GET")
}

func previousSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		site, err := getPreviousSite(db, id)
		if err != nil {
			http.Error(w, "Site not found", http.StatusNotFound)
			return
		}
		err = json.NewEncoder(w).Encode(site)
		if err != nil {
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
		err = json.NewEncoder(w).Encode(site)
		if err != nil {
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

func getPreviousSite(db *sql.DB, currentID string) (*models.PublicSite, error) {
	var site models.PublicSite
	err := db.QueryRow(`
        WITH ring AS (
            SELECT id, name, url, is_up,
                   LEAD(id) OVER (ORDER BY id) AS next_id,
                   LAG(id) OVER (ORDER BY id) AS prev_id
            FROM sites
            WHERE is_up = true
        )
        SELECT id, name, url
        FROM ring
        WHERE (id = $1 AND prev_id IS NOT NULL AND prev_id = (SELECT MAX(id) FROM ring))
           OR (id < $1 AND is_up = true)
           OR (id = (SELECT MAX(id) FROM ring WHERE is_up = true) AND $1 = (SELECT MIN(id) FROM ring WHERE is_up = true))
        ORDER BY CASE
            WHEN id < $1 THEN id
            ELSE 0
        END DESC
        LIMIT 1
    `, currentID).Scan(&site.ID, &site.Name, &site.URL)
	if err != nil {
		return nil, err
	}
	return &site, nil
}

func getNextSite(db *sql.DB, currentID string) (*models.PublicSite, error) {
	var site models.PublicSite
	err := db.QueryRow(`
        WITH ring AS (
            SELECT id, name, url, is_up,
                   LEAD(id) OVER (ORDER BY id) AS next_id,
                   LAG(id) OVER (ORDER BY id) AS prev_id
            FROM sites
            WHERE is_up = true
        )
        SELECT id, name, url
        FROM ring
        WHERE (id = $1 AND next_id IS NOT NULL AND next_id = (SELECT MIN(id) FROM ring))
           OR (id > $1 AND is_up = true)
           OR (id = (SELECT MIN(id) FROM ring WHERE is_up = true) AND $1 = (SELECT MAX(id) FROM ring WHERE is_up = true))
        ORDER BY CASE
            WHEN id > $1 THEN id
            ELSE (SELECT MAX(id) FROM ring) + 1
        END
        LIMIT 1
    `, currentID).Scan(&site.ID, &site.Name, &site.URL)
	if err != nil {
		return nil, err
	}
	return &site, nil
}
