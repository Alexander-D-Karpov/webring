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

	apiRouter.HandleFunc("/{slug}/prev/data", previousSiteHandler(db)).Methods("GET")
	apiRouter.HandleFunc("/{slug}/next/data", nextSiteHandler(db)).Methods("GET")
	apiRouter.HandleFunc("/{slug}/prev", previousSiteRedirectHandler(db)).Methods("GET")
	apiRouter.HandleFunc("/{slug}/next", nextSiteRedirectHandler(db)).Methods("GET")
	apiRouter.HandleFunc("/{slug}/data", siteDataHandler(db)).Methods("GET")
	apiRouter.HandleFunc("/{slug}/random/data", randomSiteHandler(db)).Methods("GET")
	apiRouter.HandleFunc("/{slug}/random", randomSiteRedirectHandler(db)).Methods("GET")
	apiRouter.HandleFunc("/sites", listPublicSitesHandler(db)).Methods("GET")
	apiRouter.HandleFunc("/{slug}", currentSiteRedirectHandler(db)).Methods("GET")
}

func previousSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := mux.Vars(r)["slug"]
		site, err := getPreviousSite(db, slug)
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
		slug := mux.Vars(r)["slug"]
		site, err := getNextSite(db, slug)
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
		currentSlug := mux.Vars(r)["slug"]
		site, err := getRandomSite(db, currentSlug)
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
		slug := mux.Vars(r)["slug"]

		data, err := getSiteData(db, slug)
		if err != nil {
			http.Error(w, "Site not found", http.StatusNotFound)
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

func currentSiteRedirectHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := mux.Vars(r)["slug"]
		data, err := getCurrentSite(db, slug)
		if err != nil {
			http.Error(w, "Site not found", http.StatusNotFound)
			return
		}
		http.Redirect(w, r, data.URL, http.StatusFound)
	}
}

func previousSiteRedirectHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := mux.Vars(r)["slug"]
		site, err := getPreviousSite(db, slug)
		if err != nil {
			http.Error(w, "Site not found", http.StatusNotFound)
			return
		}
		http.Redirect(w, r, site.URL, http.StatusFound)
	}
}

func nextSiteRedirectHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := mux.Vars(r)["slug"]
		site, err := getNextSite(db, slug)
		if err != nil {
			http.Error(w, "Site not found", http.StatusNotFound)
			return
		}
		http.Redirect(w, r, site.URL, http.StatusFound)
	}
}

func randomSiteRedirectHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		currentSlug := mux.Vars(r)["slug"]
		site, err := getRandomSite(db, currentSlug)
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
	rows, err := db.Query("SELECT id, slug, name, url, favicon FROM sites WHERE is_up = true ORDER BY id")
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
		if err := rows.Scan(&site.ID, &site.Slug, &site.Name, &site.URL, &site.Favicon); err != nil {
			return nil, err
		}
		sites = append(sites, site)
	}
	return sites, nil
}

func getCurrentSite(db *sql.DB, currentSlug string) (*models.PublicSite, error) {
	var site models.PublicSite
	err := db.QueryRow(`
        SELECT slug, name, url, favicon
        FROM sites
        WHERE is_up = true AND slug = $1
        LIMIT 1
    `, currentSlug).Scan(&site.Slug, &site.Name, &site.URL, &site.Favicon)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("no available sites found")
		}
		return nil, fmt.Errorf("database error: %v", err)
	}
	return &site, nil
}

func getNextSite(db *sql.DB, currentSlug string) (*models.PublicSite, error) {
	query := `
        WITH c AS (
            SELECT id as cid
            FROM sites
            WHERE slug = $1
        ),
        pick AS (
            SELECT COALESCE(
                (SELECT MIN(s2.id)
                 FROM sites s2
                 WHERE s2.is_up = TRUE
                   AND s2.id > c.cid),
                (SELECT MIN(s3.id)
                 FROM sites s3
                 WHERE s3.is_up = TRUE)
            ) AS next_id
            FROM c
        )
        SELECT s.id, s.slug, s.name, s.url, s.favicon
        FROM pick
        LEFT JOIN sites s ON s.id = pick.next_id
    `

	var site models.PublicSite
	err := db.QueryRow(query, currentSlug).Scan(&site.ID, &site.Slug, &site.Name, &site.URL, &site.Favicon)
	if err != nil {
		return nil, fmt.Errorf("no next site found: %w", err)
	}
	// If we get 0, it means no up sites at all
	if site.ID == 0 {
		return nil, fmt.Errorf("no available sites found (zero up sites)")
	}
	return &site, nil
}

func getPreviousSite(db *sql.DB, currentSlug string) (*models.PublicSite, error) {
	query := `
        WITH c AS (
            SELECT id as cid
            FROM sites
            WHERE slug = $1
        ),
        pick AS (
            SELECT COALESCE(
                (SELECT MAX(s2.id)
                 FROM sites s2
                 WHERE s2.is_up = TRUE
                   AND s2.id < c.cid),
                (SELECT MAX(s3.id)
                 FROM sites s3
                 WHERE s3.is_up = TRUE)
            ) AS prev_id
            FROM c
        )
        SELECT s.id, s.slug, s.name, s.url, s.favicon
        FROM pick
        LEFT JOIN sites s ON s.id = pick.prev_id
    `
	var site models.PublicSite
	err := db.QueryRow(query, currentSlug).Scan(&site.ID, &site.Slug, &site.Name, &site.URL, &site.Favicon)
	if err != nil {
		return nil, fmt.Errorf("no previous site found: %w", err)
	}
	if site.ID == 0 {
		return nil, fmt.Errorf("no available sites found (zero up sites)")
	}
	return &site, nil
}

func getSiteData(db *sql.DB, slug string) (*models.SiteData, error) {
	query := `
        WITH current_site AS (
            SELECT id, slug, name, url, favicon, is_up
            FROM sites
            WHERE slug = $1
        ),
        ring AS (
            SELECT
                c.id          AS curr_id,
                c.slug        AS curr_slug,
                c.name        AS curr_name,
                c.url         AS curr_url,
                c.favicon     AS curr_favicon,
                c.is_up       AS curr_is_up,

                /* Largest up-site ID < curr_id; if none, wrap to largest up-site ID overall */
                COALESCE(
                    (SELECT MAX(s2.id)
                     FROM sites s2
                     WHERE s2.is_up = TRUE AND s2.id < c.id),
                    (SELECT MAX(s2.id)
                     FROM sites s2
                     WHERE s2.is_up = TRUE)
                ) AS final_prev_id,

                /* Smallest up-site ID > curr_id; if none, wrap to smallest up-site ID overall */
                COALESCE(
                    (SELECT MIN(s2.id)
                     FROM sites s2
                     WHERE s2.is_up = TRUE AND s2.id > c.id),
                    (SELECT MIN(s2.id)
                     FROM sites s2
                     WHERE s2.is_up = TRUE)
                ) AS final_next_id
            FROM current_site c
        )
        SELECT
          /* Prev site */
          COALESCE(prevs.id, 0)       AS prev_id,
          COALESCE(prevs.slug, '')    AS prev_slug,
          COALESCE(prevs.name, '')    AS prev_name,
          COALESCE(prevs.url, '')     AS prev_url,
          COALESCE(prevs.favicon, '') AS prev_favicon,

          /* Current site (could be up or down) */
          ring.curr_id                AS curr_id,
          ring.curr_slug              AS curr_slug,
          ring.curr_name              AS curr_name,
          ring.curr_url               AS curr_url,
          COALESCE(ring.curr_favicon, '') AS curr_favicon,

          /* Next site */
          COALESCE(nexts.id, 0)       AS next_id,
          COALESCE(nexts.slug, '')    AS next_slug,
          COALESCE(nexts.name, '')    AS next_name,
          COALESCE(nexts.url, '')     AS next_url,
          COALESCE(nexts.favicon, '') AS next_favicon

        FROM ring
        /* LEFT JOIN the prev/next IDs to get their details */
        LEFT JOIN sites prevs ON prevs.id = ring.final_prev_id
        LEFT JOIN sites nexts ON nexts.id = ring.final_next_id
    `

	var data models.SiteData
	err := db.QueryRow(query, slug).Scan(
		&data.Prev.ID, &data.Prev.Slug, &data.Prev.Name, &data.Prev.URL, &data.Prev.Favicon,
		&data.Curr.ID, &data.Curr.Slug, &data.Curr.Name, &data.Curr.URL, &data.Curr.Favicon,
		&data.Next.ID, &data.Next.Slug, &data.Next.Name, &data.Next.URL, &data.Next.Favicon,
	)
	if err != nil {
		// If we got sql.ErrNoRows, it means there's no site with this ID
		return nil, err
	}

	return &data, nil
}

func getRandomSite(db *sql.DB, currentSlug string) (*models.PublicSite, error) {
	var site models.PublicSite
	err := db.QueryRow(`
        SELECT id, slug, name, url, favicon
        FROM sites
        WHERE is_up = true AND slug != $1
        ORDER BY RANDOM()
        LIMIT 1
    `, currentSlug).Scan(&site.ID, &site.Slug, &site.Name, &site.URL, &site.Favicon)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("no available sites found")
		}
		return nil, fmt.Errorf("database error: %v", err)
	}
	return &site, nil
}
