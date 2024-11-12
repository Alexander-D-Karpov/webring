package uptime

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"webring/internal/models"
)

type Checker struct {
	db         *sql.DB
	proxy      *url.URL
	proxyAlive bool
	debug      bool
}

func NewChecker(db *sql.DB) *Checker {
	var proxyURL *url.URL
	if proxyStr := os.Getenv("CHECKER_PROXY"); proxyStr != "" {
		var err error
		proxyURL, err = url.Parse(proxyStr)
		if err != nil {
			log.Printf("Warning: Invalid proxy URL provided (%s): %v. Will proceed without proxy.", proxyStr, err)
		} else {
			log.Printf("Using proxy: %s", proxyStr)
		}
	}

	debug, _ := strconv.ParseBool(os.Getenv("CHECKER_DEBUG"))

	return &Checker{
		db:         db,
		proxy:      proxyURL,
		proxyAlive: true,
		debug:      debug,
	}
}

func (c *Checker) debugLog(format string, args ...interface{}) {
	if c.debug {
		log.Printf("[DEBUG] "+format, args...)
	}
}

func (c *Checker) Start() {
	fmt.Println("Starting checker...")
	if c.debug {
		log.Printf("[DEBUG] Checker started with proxy: %v, debug mode: true", c.proxy != nil)
	}
	ticker := time.NewTicker(5 * time.Minute)
	if c.debug {
		ticker = time.NewTicker(5 * time.Second)
	}
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

	c.debugLog("Starting check of %d sites", len(sites))

	if c.proxy != nil {
		proxySuccess := false
		allProxyErrors := true

		var wg sync.WaitGroup
		var mutex sync.Mutex

		for _, site := range sites {
			wg.Add(1)
			go func(s models.Site) {
				defer wg.Done()

				c.debugLog("Checking site %s (ID: %d) via proxy", s.URL, s.ID)
				isUp, responseTime, errorMsg := c.doCheckSite(s, true)

				mutex.Lock()
				if isUp {
					c.debugLog("Site %s is up (proxy), response time: %.2fs", s.URL, responseTime)
					proxySuccess = true
					allProxyErrors = false
				} else {
					c.debugLog("Site %s is down (proxy): %s", s.URL, errorMsg)
					if !strings.Contains(errorMsg, "cannot connect to proxy") &&
						!strings.Contains(errorMsg, "proxy refused connection") &&
						!strings.Contains(errorMsg, "no route to host") {
						c.debugLog("Error for %s appears to be site-specific, not proxy-related", s.URL)
						allProxyErrors = false
					}
				}
				mutex.Unlock()

				if isUp {
					c.updateSiteStatus(s.ID, true, responseTime)
				}
			}(site)
		}
		wg.Wait()

		c.proxyAlive = proxySuccess || !allProxyErrors

		if !c.proxyAlive {
			log.Printf("Proxy appears to be down, retrying with direct connections")
			c.debugLog("All sites failed with proxy errors, switching to direct connections")

			for _, site := range sites {
				wg.Add(1)
				go func(s models.Site) {
					defer wg.Done()

					c.debugLog("Retrying site %s (ID: %d) without proxy", s.URL, s.ID)
					isUp, responseTime, errorMsg := c.doCheckSite(s, false)

					if isUp {
						c.debugLog("Site %s is up (direct), response time: %.2fs", s.URL, responseTime)
					} else {
						c.debugLog("Site %s is down (direct): %s", s.URL, errorMsg)
					}

					c.updateSiteStatus(s.ID, isUp, responseTime)
					if !isUp {
						c.logError(s.URL, errorMsg)
					}
				}(site)
			}
			wg.Wait()
		} else {
			c.debugLog("Proxy is working correctly, no need for direct connection retries")
		}
	} else {
		c.debugLog("No proxy configured, checking sites directly")
		var wg sync.WaitGroup
		for _, site := range sites {
			wg.Add(1)
			go func(s models.Site) {
				defer wg.Done()

				c.debugLog("Checking site %s (ID: %d) directly", s.URL, s.ID)
				isUp, responseTime, errorMsg := c.doCheckSite(s, false)

				if isUp {
					c.debugLog("Site %s is up, response time: %.2fs", s.URL, responseTime)
				} else {
					c.debugLog("Site %s is down: %s", s.URL, errorMsg)
				}

				c.updateSiteStatus(s.ID, isUp, responseTime)
				if !isUp {
					c.logError(s.URL, errorMsg)
				}
			}(site)
		}
		wg.Wait()
	}
}

func (c *Checker) doCheckSite(site models.Site, useProxy bool) (bool, float64, string) {
	transport := &http.Transport{
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   false,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
	}

	if useProxy && c.proxy != nil {
		transport.Proxy = http.ProxyURL(c.proxy)
	}

	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: transport,
	}

	siteUrl := site.URL
	if !hasProtocol(siteUrl) {
		siteUrl = "https://" + siteUrl
	}

	c.debugLog("Making request to %s (proxy: %v)", siteUrl, useProxy)
	start := time.Now()
	resp, err := client.Head(siteUrl)
	elapsed := time.Since(start).Seconds()

	if err != nil {
		errorMsg := fmt.Sprintf("Error checking site: %v", err)
		c.debugLog("Request failed for %s: %v (took %.2fs)", siteUrl, err, elapsed)
		return false, elapsed, errorMsg
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			c.debugLog("Error closing response body for %s: %v", siteUrl, err)
		}
	}(resp.Body)

	c.debugLog("Request to %s completed with status %d (took %.2fs)", siteUrl, resp.StatusCode, elapsed)
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
