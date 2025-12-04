package uptime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"webring/internal/models"
	"webring/internal/telegram"
)

const (
	checkInterval      = 5 * time.Minute
	checkIntervalDebug = 5 * time.Second
	httpTimeout        = 10 * time.Second
	tlsTimeout         = 10 * time.Second
	maxIdleConns       = 100
	idleTimeout        = 90 * time.Second
	serverErrorCode    = 500
	logPerm            = 0o644
	userAgent          = "webring-checker (+https://otor.ing)"
)

type Checker struct {
	db         *sql.DB
	proxy      *url.URL
	proxyAlive bool
	debug      bool
	siteStates sync.Map
}

type SiteState struct {
	IsUp              bool
	LastNotifiedState bool
	NotifiedAt        time.Time
}

var markdownV2Escape = regexp.MustCompile(`([_*\[\]()~` + "`" + `>#+\-=|{}.!\\])`)

func escapeMarkdownV2(text string) string {
	return markdownV2Escape.ReplaceAllString(text, `\$1`)
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

	debug := false
	if debugStr := os.Getenv("CHECKER_DEBUG"); debugStr != "" {
		var err error
		debug, err = strconv.ParseBool(debugStr)
		if err != nil {
			log.Printf("Warning: Invalid CHECKER_DEBUG value: %v", err)
		}
	}

	checker := &Checker{
		db:         db,
		proxy:      proxyURL,
		proxyAlive: true,
		debug:      debug,
	}

	checker.loadInitialStates()
	return checker
}

func (c *Checker) loadInitialStates() {
	rows, err := c.db.Query("SELECT id, is_up FROM sites")
	if err != nil {
		log.Printf("Error loading initial site states: %v", err)
		return
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			log.Printf("Error closing rows: %v", cerr)
		}
	}()

	for rows.Next() {
		var id int
		var isUp bool
		if scanErr := rows.Scan(&id, &isUp); scanErr != nil {
			log.Printf("Error scanning site state: %v", scanErr)
			continue
		}
		c.siteStates.Store(id, &SiteState{
			IsUp:              isUp,
			LastNotifiedState: isUp,
			NotifiedAt:        time.Now(),
		})
	}

	if rowsErr := rows.Err(); rowsErr != nil {
		log.Printf("Error iterating rows: %v", rowsErr)
	}
}

func (c *Checker) debugLogf(format string, args ...interface{}) {
	if c.debug {
		log.Printf("[DEBUG] "+format, args...)
	}
}

func (c *Checker) Start() {
	fmt.Println("Starting checker...")
	if c.debug {
		c.debugLogf("Checker started with proxy: %v, debug mode: true", c.proxy != nil)
	}

	ticker := time.NewTicker(checkInterval)
	if c.debug {
		ticker = time.NewTicker(checkIntervalDebug)
	}
	for range ticker.C {
		c.checkAllSites()
	}
}

func isProxyError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	proxyErrors := []string{
		"cannot connect to proxy",
		"proxy refused connection",
		"no route to host",
		"proxy authentication required",
		"bad gateway",
	}
	for _, proxyErr := range proxyErrors {
		if strings.Contains(errStr, proxyErr) {
			return true
		}
	}
	return false
}

func (c *Checker) checkAndNotifyStatusChange(siteID int, userID *int, siteName string, currentIsUp bool) {
	if userID == nil || *userID == 0 {
		return
	}

	stateInterface, exists := c.siteStates.Load(siteID)
	if !exists {
		c.siteStates.Store(siteID, &SiteState{
			IsUp:              currentIsUp,
			LastNotifiedState: currentIsUp,
			NotifiedAt:        time.Now(),
		})
		return
	}

	state, ok := stateInterface.(*SiteState)
	if !ok {
		log.Printf("Error: invalid state type for site %d", siteID)
		return
	}

	statusChanged := state.LastNotifiedState != currentIsUp

	if statusChanged {
		now := time.Now()
		timeSinceLastNotification := now.Sub(state.NotifiedAt)

		if timeSinceLastNotification >= 30*time.Second {
			go c.notifyOwner(*userID, siteName, currentIsUp)

			state.LastNotifiedState = currentIsUp
			state.NotifiedAt = now
		}
	}

	state.IsUp = currentIsUp
	c.siteStates.Store(siteID, state)
}

func (c *Checker) notifyOwner(userID int, siteName string, isUp bool) {
	user, err := c.getUserByID(userID)
	if err != nil {
		log.Printf("Error getting user for notification: %v", err)
		return
	}

	if user.TelegramID == 0 {
		return
	}

	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		return
	}

	siteNameEscaped := escapeMarkdownV2(siteName)

	var message string
	if isUp {
		message = fmt.Sprintf(
			"*Site Status: Online*\n\nYour site *%s* is now responding and back online\\.",
			siteNameEscaped,
		)
	} else {
		message = fmt.Sprintf(
			"*Site Status: Offline*\n\nYour site *%s* is currently not responding\\. "+
				"Please check your server\\.",
			siteNameEscaped,
		)
	}

	telegram.SendMessage(botToken, user.TelegramID, message)
}

func (c *Checker) getUserByID(userID int) (*models.User, error) {
	var user models.User
	var telegramID sql.NullInt64
	err := c.db.QueryRow(`
		SELECT id, telegram_id, telegram_username, first_name, last_name, is_admin
		FROM users WHERE id = $1
	`, userID).Scan(&user.ID, &telegramID, &user.TelegramUsername,
		&user.FirstName, &user.LastName, &user.IsAdmin)

	if err != nil {
		return nil, err
	}

	if telegramID.Valid {
		user.TelegramID = telegramID.Int64
	}

	return &user, nil
}

func (c *Checker) checkAllSites() {
	sites, err := c.getAllSites()
	if err != nil {
		log.Printf("Error getting sites: %v", err)
		return
	}

	c.debugLogf("Starting check of %d sites", len(sites))

	sem := make(chan struct{}, 20)
	var wg sync.WaitGroup

	if c.proxy != nil {
		var (
			proxySuccess   bool
			allProxyErrors = true
			mu             sync.Mutex
		)

		for _, site := range sites {
			sem <- struct{}{}
			wg.Add(1)

			go func(s models.Site) {
				defer func() {
					<-sem
					wg.Done()
				}()

				c.debugLogf("Checking site %s (ID: %d) via proxy", s.URL, s.ID)
				isUp, responseTime, errorMsg := c.doCheckSite(&s, true)

				mu.Lock()
				if isUp {
					c.debugLogf("Site %s is up (proxy), response time: %.2fs", s.URL, responseTime)
					proxySuccess = true
					allProxyErrors = false
				} else {
					c.debugLogf("Site %s is down (proxy): %s", s.URL, errorMsg)
					if !isProxyError(errors.New(errorMsg)) {
						allProxyErrors = false
					}
				}
				mu.Unlock()

				c.updateSiteStatus(s.ID, isUp, responseTime)
				if !isUp {
					c.logError(s.URL, errorMsg)
				}
				c.checkAndNotifyStatusChange(s.ID, s.UserID, s.Name, isUp)
			}(site)
		}
		wg.Wait()

		c.proxyAlive = proxySuccess || !allProxyErrors
		if !c.proxyAlive {
			log.Printf("Proxy appears to be down, retrying with direct connections")
			c.debugLogf("All sites failed with proxy errors, switching to direct connections")

			for _, site := range sites {
				sem <- struct{}{}
				wg.Add(1)

				go func(s models.Site) {
					defer func() {
						<-sem
						wg.Done()
					}()

					c.debugLogf("Retrying site %s (ID: %d) without proxy", s.URL, s.ID)
					isUp, responseTime, errorMsg := c.doCheckSite(&s, false)

					if isUp {
						c.debugLogf("Site %s is up (direct), response time: %.2fs", s.URL, responseTime)
					} else {
						c.debugLogf("Site %s is down (direct): %s", s.URL, errorMsg)
					}

					c.updateSiteStatus(s.ID, isUp, responseTime)
					if !isUp {
						c.logError(s.URL, errorMsg)
					}
					c.checkAndNotifyStatusChange(s.ID, s.UserID, s.Name, isUp)
				}(site)
			}
			wg.Wait()
		} else {
			c.debugLogf("Proxy is working correctly, no need for direct connection retries")
		}
		return
	}

	c.debugLogf("No proxy configured, checking sites directly")
	for _, site := range sites {
		sem <- struct{}{}
		wg.Add(1)

		go func(s models.Site) {
			defer func() {
				<-sem
				wg.Done()
			}()

			c.debugLogf("Checking site %s (ID: %d) directly", s.URL, s.ID)
			isUp, responseTime, errorMsg := c.doCheckSite(&s, false)

			if isUp {
				c.debugLogf("Site %s is up, response time: %.2fs", s.URL, responseTime)
			} else {
				c.debugLogf("Site %s is down: %s", s.URL, errorMsg)
			}

			c.updateSiteStatus(s.ID, isUp, responseTime)
			if !isUp {
				c.logError(s.URL, errorMsg)
			}
			c.checkAndNotifyStatusChange(s.ID, s.UserID, s.Name, isUp)
		}(site)
	}
	wg.Wait()
}

func (c *Checker) doCheckSite(site *models.Site, useProxy bool) (isUp bool, responseTime float64, errorMsg string) {
	transport := &http.Transport{
		TLSHandshakeTimeout: tlsTimeout,
		DisableKeepAlives:   false,
		MaxIdleConns:        maxIdleConns,
		IdleConnTimeout:     idleTimeout,
	}

	if useProxy && c.proxy != nil {
		transport.Proxy = http.ProxyURL(c.proxy)
	}

	client := &http.Client{
		Timeout:   httpTimeout,
		Transport: transport,
	}

	siteURL := site.URL
	if !hasProtocol(siteURL) {
		siteURL = "https://" + siteURL
	}

	c.debugLogf("Making request to %s (proxy: %v)", siteURL, useProxy)
	start := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "HEAD", siteURL, http.NoBody)
	if err != nil {
		errorMsg := fmt.Sprintf("Error creating request: %v", err)
		c.debugLogf("Request creation failed for %s: %v", siteURL, err)
		return false, 0, errorMsg
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	elapsed := time.Since(start).Seconds()

	if err != nil {
		errorMsg := fmt.Sprintf("Error checking site: %v", err)
		c.debugLogf("Request failed for %s: %v (took %.2fs)", siteURL, err, elapsed)
		return false, elapsed, errorMsg
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			c.debugLogf("Error closing response body for %s: %v", siteURL, cerr)
		}
	}()

	c.debugLogf("Request to %s completed with status %d (took %.2fs)", siteURL, resp.StatusCode, elapsed)
	return resp.StatusCode < serverErrorCode, elapsed, ""
}

func (c *Checker) updateSiteStatus(id int, isUp bool, responseTime float64) {
	_, err := c.db.Exec("UPDATE sites SET is_up = $1, last_check = $2 WHERE id = $3", isUp, responseTime, id)
	if err != nil {
		log.Printf("Error updating site status: %v", err)
	}
}

func (c *Checker) logError(siteURL, errorMsg string) {
	f, err := os.OpenFile("checker_error.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, logPerm)
	if err != nil {
		log.Printf("Error opening log file: %v", err)
		return
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			log.Printf("Error closing log file: %v", cerr)
		}
	}()

	if _, werr := fmt.Fprintf(f, "%s failed to respond: %s\n", siteURL, errorMsg); werr != nil {
		log.Printf("Error writing to log file: %v", werr)
	}
}

func (c *Checker) getAllSites() ([]models.Site, error) {
	rows, err := c.db.Query("SELECT id, name, url, user_id FROM sites")
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			log.Printf("Error closing rows: %v", cerr)
		}
	}()

	var sites []models.Site
	for rows.Next() {
		var site models.Site
		if scanErr := rows.Scan(&site.ID, &site.Name, &site.URL, &site.UserID); scanErr != nil {
			return nil, scanErr
		}
		sites = append(sites, site)
	}

	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}

	return sites, nil
}

func hasProtocol(u string) bool {
	return len(u) > 8 && (strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://"))
}
