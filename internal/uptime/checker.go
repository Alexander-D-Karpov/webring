package uptime

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"webring/internal/models"
)

type Checker struct {
	db *sql.DB
}

func NewChecker(db *sql.DB) *Checker {
	return &Checker{db: db}
}

func (c *Checker) Start() {
	fmt.Println("Starting checker...")
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		c.checkAllSites()
	}
}

func (c *Checker) checkAllSites() {
	sites, err := c.getAllSites()
	if err != nil {
		log.Printf("Error getting sites: %v", err)
		return
	}

	var wg sync.WaitGroup
	for _, site := range sites {
		wg.Add(1)
		go func(s models.Site) {
			defer wg.Done()
			isUp, responseTime, errorMsg := c.checkSite(s)
			c.updateSiteStatus(s.ID, isUp, responseTime)
			if !isUp {
				c.logError(s.URL, errorMsg)
			}
		}(site)
	}
	wg.Wait()
}

func (c *Checker) checkSite(site models.Site) (bool, float64, string) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	url := site.URL
	if !hasProtocol(url) {
		url = "https://" + url
	}

	start := time.Now()
	resp, err := client.Head(url)
	if err != nil {
		return false, 0, fmt.Sprintf("Error checking site: %v", err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Printf("Error closing response body: %v", err)
		}
	}(resp.Body)

	elapsed := time.Since(start).Seconds()
	return resp.StatusCode < 500, elapsed, ""
}

func (c *Checker) updateSiteStatus(id int, isUp bool, responseTime float64) {
	_, err := c.db.Exec("UPDATE sites SET is_up = $1, last_check = $2 WHERE id = $3", isUp, responseTime, id)
	if err != nil {
		log.Printf("Error updating site status: %v", err)
	}
}

func (c *Checker) logError(url, errorMsg string) {
	f, err := os.OpenFile("checker_error.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Error opening log file: %v", err)
		return
	}
	defer func(f *os.File) {
		err := f.Close()
		if err != nil {
			log.Printf("Error closing log file: %v", err)
		}
	}(f)

	if _, err := f.WriteString(fmt.Sprintf("%s failed to respond: %s\n", url, errorMsg)); err != nil {
		log.Printf("Error writing to log file: %v", err)
	}
}

func (c *Checker) getAllSites() ([]models.Site, error) {
	rows, err := c.db.Query("SELECT id, url FROM sites")
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
		if err := rows.Scan(&site.ID, &site.URL); err != nil {
			return nil, err
		}
		sites = append(sites, site)
	}
	return sites, nil
}

func hasProtocol(url string) bool {
	return len(url) > 8 && (url[:7] == "http://" || url[:8] == "https://")
}
