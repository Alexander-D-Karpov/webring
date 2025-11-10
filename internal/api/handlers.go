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

func nextSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := mux.Vars(r)["slug"]
		site, err := getNextSite(db, slug)
		if err != nil {
			log.Printf("Error getting next site for %s: %v", slug, err)
			http.Error(w, "Site not found or no next site available", http.StatusNotFound)
			return
		}

		response := struct {
			Next *models.PublicSite `json:"next"`
		}{
			Next: site,
		}

		w.Header().Set("Content-Type", "application/json")
		if err = json.NewEncoder(w).Encode(response); err != nil {
			log.Printf("Error encoding response: %v", err)
			http.Error(w, "Error encoding response", http.StatusInternalServerError)
			return
		}
	}
}

func previousSiteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := mux.Vars(r)["slug"]
		site, err := getPreviousSite(db, slug)
		if err != nil {
			log.Printf("Error getting previous site for %s: %v", slug, err)
			http.Error(w, "Site not found or no previous site available", http.StatusNotFound)
			return
		}

		response := struct {
			Previous *models.PublicSite `json:"previous"`
		}{
			Previous: site,
		}

		w.Header().Set("Content-Type", "application/json")
		if err = json.NewEncoder(w).Encode(response); err != nil {
			log.Printf("Error encoding response: %v", err)
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
		if err = json.NewEncoder(w).Encode(response); err != nil {
			log.Printf("Error encoding response: %v", err)
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
		if err = json.NewEncoder(w).Encode(data); err != nil {
			http.Error(w, "Error encoding response", http.StatusInternalServerError)
			return
		}
	}
}

func currentSiteRedirectHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := mux.Vars(r)["slug"]

		var url string
		var isUp bool
		err := db.QueryRow("SELECT url, is_up FROM sites WHERE slug = $1", slug).Scan(&url, &isUp)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "Site not found", http.StatusNotFound)
			} else {
				log.Printf("Error fetching site %s: %v", slug, err)
				http.Error(w, "Error fetching site", http.StatusInternalServerError)
			}
			return
		}

		if !isUp {
			http.Error(w, "Site is currently down", http.StatusServiceUnavailable)
			return
		}

		http.Redirect(w, r, url, http.StatusFound)
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
	return func(w http.ResponseWriter, _ *http.Request) {
		sites, err := getRespondingSites(db)
		if err != nil {
			http.Error(w, "Error fetching sites", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err = json.NewEncoder(w).Encode(sites); err != nil {
			http.Error(w, "Error encoding response", http.StatusInternalServerError)
			return
		}
	}
}

func getNextSite(db *sql.DB, currentSlug string) (*models.PublicSite, error) {
	query := `
        WITH c AS (
            SELECT display_order as corder
            FROM sites
            WHERE slug = $1
        ),
        pick AS (
            SELECT COALESCE(
                (SELECT MIN(s2.display_order)
                 FROM sites s2
                 WHERE s2.is_up = TRUE
                   AND s2.display_order > c.corder),
                (SELECT MIN(s3.display_order)
                 FROM sites s3
                 WHERE s3.is_up = TRUE)
            ) AS next_order
            FROM c
        )
        SELECT s.slug, s.name, s.url, s.favicon
        FROM pick
        LEFT JOIN sites s ON s.display_order = pick.next_order
        WHERE s.is_up = TRUE
    `

	var site models.PublicSite
	err := db.QueryRow(query, currentSlug).Scan(&site.Slug, &site.Name, &site.URL, &site.Favicon)
	if err != nil {
		return nil, fmt.Errorf("no next site found: %w", err)
	}
	if site.Slug == "" {
		return nil, fmt.Errorf("no available sites found (zero up sites)")
	}
	return &site, nil
}

func getPreviousSite(db *sql.DB, currentSlug string) (*models.PublicSite, error) {
	query := `
        WITH c AS (
            SELECT display_order as corder
            FROM sites
            WHERE slug = $1
        ),
        pick AS (
            SELECT COALESCE(
                (SELECT MAX(s2.display_order)
                 FROM sites s2
                 WHERE s2.is_up = TRUE
                   AND s2.display_order < c.corder),
                (SELECT MAX(s3.display_order)
                 FROM sites s3
                 WHERE s3.is_up = TRUE)
            ) AS prev_order
            FROM c
        )
        SELECT s.slug, s.name, s.url, s.favicon
        FROM pick
        LEFT JOIN sites s ON s.display_order = pick.prev_order
        WHERE s.is_up = TRUE
    `
	var site models.PublicSite
	err := db.QueryRow(query, currentSlug).Scan(&site.Slug, &site.Name, &site.URL, &site.Favicon)
	if err != nil {
		return nil, fmt.Errorf("no previous site found: %w", err)
	}
	if site.Slug == "" {
		return nil, fmt.Errorf("no available sites found (zero up sites)")
	}
	return &site, nil
}

func getSiteData(db *sql.DB, slug string) (*models.SiteData, error) {
	query := `
        WITH current_site AS (
            SELECT slug, name, url, favicon, is_up, display_order
            FROM sites
            WHERE slug = $1
        ),
        ring AS (
            SELECT
                c.slug        AS curr_slug,
                c.name        AS curr_name,
                c.url         AS curr_url,
                c.favicon     AS curr_favicon,
                c.is_up       AS curr_is_up,
                c.display_order AS curr_order,

                COALESCE(
                    (SELECT MAX(s2.display_order)
                     FROM sites s2
                     WHERE s2.is_up = TRUE AND s2.display_order < c.display_order),
                    (SELECT MAX(s2.display_order)
                     FROM sites s2
                     WHERE s2.is_up = TRUE)
                ) AS final_prev_order,

                COALESCE(
                    (SELECT MIN(s2.display_order)
                     FROM sites s2
                     WHERE s2.is_up = TRUE AND s2.display_order > c.display_order),
                    (SELECT MIN(s2.display_order)
                     FROM sites s2
                     WHERE s2.is_up = TRUE)
                ) AS final_next_order
            FROM current_site c
        )
        SELECT
          COALESCE(prevs.slug, '')    AS prev_slug,
          COALESCE(prevs.name, '')    AS prev_name,
          COALESCE(prevs.url, '')     AS prev_url,
          COALESCE(prevs.favicon, '') AS prev_favicon,

          ring.curr_slug              AS curr_slug,
          ring.curr_name              AS curr_name,
          ring.curr_url               AS curr_url,
          COALESCE(ring.curr_favicon, '') AS curr_favicon,

          COALESCE(nexts.slug, '')    AS next_slug,
          COALESCE(nexts.name, '')    AS next_name,
          COALESCE(nexts.url, '')     AS next_url,
          COALESCE(nexts.favicon, '') AS next_favicon

        FROM ring
        LEFT JOIN sites prevs ON prevs.display_order = ring.final_prev_order AND prevs.is_up = TRUE
        LEFT JOIN sites nexts ON nexts.display_order = ring.final_next_order AND nexts.is_up = TRUE
    `

	var data models.SiteData
	err := db.QueryRow(query, slug).Scan(
		&data.Prev.Slug, &data.Prev.Name, &data.Prev.URL, &data.Prev.Favicon,
		&data.Curr.Slug, &data.Curr.Name, &data.Curr.URL, &data.Curr.Favicon,
		&data.Next.Slug, &data.Next.Name, &data.Next.URL, &data.Next.Favicon,
	)
	if err != nil {
		return nil, err
	}

	return &data, nil
}

func getRandomSite(db *sql.DB, currentSlug string) (*models.PublicSite, error) {
	var site models.PublicSite
	err := db.QueryRow(`
        SELECT slug, name, url, favicon
        FROM sites
        WHERE is_up = true AND slug != $1
        ORDER BY RANDOM()
        LIMIT 1
    `, currentSlug).Scan(&site.Slug, &site.Name, &site.URL, &site.Favicon)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = db.QueryRow(`
                SELECT slug, name, url, favicon
                FROM sites
                WHERE is_up = true
                ORDER BY RANDOM()
                LIMIT 1
            `).Scan(&site.Slug, &site.Name, &site.URL, &site.Favicon)

			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, fmt.Errorf("no available sites found")
				}
				return nil, fmt.Errorf("database error: %v", err)
			}
		} else {
			return nil, fmt.Errorf("database error: %v", err)
		}
	}
	return &site, nil
}

func getRespondingSites(db *sql.DB) ([]models.PublicSite, error) {
	rows, err := db.Query("SELECT slug, name, url, favicon FROM sites WHERE is_up = true ORDER BY display_order")
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			log.Printf("Error closing rows: %v", closeErr)
		}
	}()

	var sites []models.PublicSite
	for rows.Next() {
		var site models.PublicSite
		if scanErr := rows.Scan(&site.Slug, &site.Name, &site.URL, &site.Favicon); scanErr != nil {
			return nil, scanErr
		}
		sites = append(sites, site)
	}

	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}

	return sites, nil
}
