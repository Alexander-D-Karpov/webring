package uptime

import (
	"context"
	"database/sql"
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
	defaultCheckInterval = 5 * time.Minute
	debugCheckInterval   = 5 * time.Second
	httpTimeout          = 10 * time.Second
	tlsTimeout           = 10 * time.Second
	maxIdleConns         = 100
	idleTimeout          = 90 * time.Second
	serverErrorCode      = 400
	logPerm              = 0o644
	userAgent            = "webring-checker (+https://otor.ing)"
	defaultWorkers       = 5
	defaultDownThreshold = 3
)

type checkTask struct {
	site      models.Site
	useProxy  bool
	currentUp bool
}

type checkResult struct {
	siteID       int
	siteName     string
	userID       *int
	isUp         bool
	wasUp        bool
	responseTime float64
	errorMsg     string
	useProxy     bool
	proxyError   bool
}

type Checker struct {
	db              *sql.DB
	proxy           *url.URL
	proxyAlive      bool
	proxyMu         sync.RWMutex
	debug           bool
	notifyStates    sync.Map
	failureCounters sync.Map
	downThreshold   int
	workers         int
	checkInterval   time.Duration
	taskQueue       chan checkTask
	resultQueue     chan checkResult
	wg              sync.WaitGroup
	stopCh          chan struct{}
}

type NotifyState struct {
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

	workers := defaultWorkers
	if workersStr := os.Getenv("CHECKER_WORKERS"); workersStr != "" {
		if w, err := strconv.Atoi(workersStr); err == nil && w > 0 {
			workers = w
		} else {
			log.Printf("Warning: Invalid CHECKER_WORKERS value: %s, using default %d", workersStr, defaultWorkers)
		}
	}

	downThreshold := defaultDownThreshold
	if thresholdStr := os.Getenv("CHECKER_DOWN_THRESHOLD"); thresholdStr != "" {
		if t, err := strconv.Atoi(thresholdStr); err == nil && t > 0 {
			downThreshold = t
		} else {
			log.Printf("Warning: Invalid CHECKER_DOWN_THRESHOLD value: %s, using default %d", thresholdStr, defaultDownThreshold)
		}
	}

	checkInterval := defaultCheckInterval
	if debug {
		checkInterval = debugCheckInterval
	}

	if intervalStr := os.Getenv("CHECKER_INTERVAL"); intervalStr != "" {
		if d, err := time.ParseDuration(intervalStr); err == nil && d >= time.Second {
			checkInterval = d
		} else {
			log.Printf("Warning: Invalid CHECKER_INTERVAL value: %s, using %v", intervalStr, checkInterval)
		}
	}

	checker := &Checker{
		db:            db,
		proxy:         proxyURL,
		proxyAlive:    true,
		debug:         debug,
		downThreshold: downThreshold,
		workers:       workers,
		checkInterval: checkInterval,
		taskQueue:     make(chan checkTask, 1000),
		resultQueue:   make(chan checkResult, 1000),
		stopCh:        make(chan struct{}),
	}

	checker.loadInitialNotifyStates()
	checker.validateCapacity()

	log.Printf("Checker initialized with down threshold: %d consecutive failures required", downThreshold)

	return checker
}

func (c *Checker) validateCapacity() {
	count, err := c.getSiteCount()
	if err != nil {
		log.Printf("Warning: Could not validate checker capacity: %v", err)
		return
	}

	maxTasksPerInterval := float64(c.workers) * (c.checkInterval.Seconds() / httpTimeout.Seconds())

	if float64(count) > maxTasksPerInterval {
		log.Printf("WARNING: Checker capacity may be insufficient!")
		log.Printf("  Sites to check: %d", count)
		log.Printf("  Workers: %d", c.workers)
		log.Printf("  Check interval: %v", c.checkInterval)
		log.Printf("  HTTP timeout: %v", httpTimeout)
		log.Printf("  Max sites per interval: %.0f", maxTasksPerInterval)
		log.Printf("  Consider increasing CHECKER_WORKERS or check interval")
	} else {
		c.debugLogf("Capacity OK: %d sites, %d workers, max %.0f sites/interval",
			count, c.workers, maxTasksPerInterval)
	}
}

func (c *Checker) getSiteCount() (int, error) {
	var count int
	err := c.db.QueryRow("SELECT COUNT(*) FROM sites").Scan(&count)
	return count, err
}

func (c *Checker) loadInitialNotifyStates() {
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
		c.notifyStates.Store(id, &NotifyState{
			LastNotifiedState: isUp,
			NotifiedAt:        time.Now(),
		})
		c.failureCounters.Store(id, 0)
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
	log.Printf("Starting checker with %d workers...", c.workers)
	if c.debug {
		c.debugLogf("Debug mode enabled, check interval: %v", c.checkInterval)
	}

	for i := 0; i < c.workers; i++ {
		c.wg.Add(1)
		go c.worker(i)
	}

	c.wg.Add(1)
	go c.resultProcessor()

	c.wg.Add(1)
	go c.scheduler()

	c.wg.Wait()
}

func (c *Checker) Stop() {
	close(c.stopCh)
	c.wg.Wait()
}

func (c *Checker) scheduler() {
	defer c.wg.Done()

	c.scheduleTasks()

	ticker := time.NewTicker(c.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			close(c.taskQueue)
			return
		case <-ticker.C:
			c.scheduleTasks()
		}
	}
}

func (c *Checker) scheduleTasks() {
	sites, err := c.getAllSitesWithStatus()
	if err != nil {
		log.Printf("Error getting sites for scheduling: %v", err)
		return
	}

	c.debugLogf("Scheduling %d sites for checking", len(sites))

	c.proxyMu.RLock()
	useProxy := c.proxy != nil && c.proxyAlive
	c.proxyMu.RUnlock()

	for _, site := range sites {
		select {
		case <-c.stopCh:
			return
		case c.taskQueue <- checkTask{site: site.Site, useProxy: useProxy, currentUp: site.IsUp}:
			c.debugLogf("Scheduled site %s (ID: %d, currentUp: %t)", site.URL, site.ID, site.IsUp)
		default:
			log.Printf("Warning: Task queue full, skipping site %s (ID: %d)", site.URL, site.ID)
		}
	}
}

func (c *Checker) worker(id int) {
	defer c.wg.Done()

	transport := &http.Transport{
		TLSHandshakeTimeout: tlsTimeout,
		DisableKeepAlives:   false,
		MaxIdleConns:        maxIdleConns,
		IdleConnTimeout:     idleTimeout,
	}

	client := &http.Client{
		Timeout:   httpTimeout,
		Transport: transport,
	}

	c.debugLogf("Worker %d started", id)

	for task := range c.taskQueue {
		c.debugLogf("Worker %d checking site %s (ID: %d, dbState: %t)", id, task.site.URL, task.site.ID, task.currentUp)

		result := c.checkSite(client, transport, &task.site, task.useProxy)
		result.userID = task.site.UserID
		result.siteName = task.site.Name
		result.wasUp = task.currentUp

		select {
		case c.resultQueue <- result:
		case <-c.stopCh:
			return
		}

		if !result.isUp && result.proxyError && task.useProxy {
			c.debugLogf("Worker %d retrying site %s without proxy", id, task.site.URL)
			retryResult := c.checkSite(client, transport, &task.site, false)
			retryResult.userID = task.site.UserID
			retryResult.siteName = task.site.Name
			retryResult.wasUp = task.currentUp

			select {
			case c.resultQueue <- retryResult:
			case <-c.stopCh:
				return
			}
		}
	}

	c.debugLogf("Worker %d stopped", id)
}

func (c *Checker) checkSite(client *http.Client,
	transport *http.Transport, site *models.Site, useProxy bool) checkResult {
	result := checkResult{
		siteID:   site.ID,
		useProxy: useProxy,
	}

	if useProxy && c.proxy != nil {
		transport.Proxy = http.ProxyURL(c.proxy)
	} else {
		transport.Proxy = nil
	}

	siteURL := site.URL
	if !hasProtocol(siteURL) {
		siteURL = "https://" + siteURL
	}

	start := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", siteURL, http.NoBody)
	if err != nil {
		result.errorMsg = fmt.Sprintf("Error creating request: %v", err)
		result.responseTime = time.Since(start).Seconds()
		return result
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	result.responseTime = time.Since(start).Seconds()

	if err != nil {
		result.errorMsg = fmt.Sprintf("Error checking site: %v", err)
		result.proxyError = isProxyError(err)
		return result
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			c.debugLogf("Error closing response body: %v", cerr)
		}
	}()

	result.isUp = resp.StatusCode < serverErrorCode
	c.debugLogf("Checked site %s (ID: %d): status %d, isUp: %t, responseTime: %.2fs",
		site.URL, site.ID, resp.StatusCode, result.isUp, result.responseTime)
	return result
}

func (c *Checker) resultProcessor() {
	defer c.wg.Done()
	defer close(c.resultQueue)

	proxyFailures := 0
	proxySuccesses := 0
	const proxyThreshold = 5

	for {
		select {
		case <-c.stopCh:
			return
		case result, ok := <-c.resultQueue:
			if !ok {
				return
			}

			shouldUpdateDB, newIsUp := c.processCheckResult(result)

			if shouldUpdateDB {
				c.updateSiteStatus(result.siteID, newIsUp, result.responseTime)

				if !newIsUp && result.errorMsg != "" {
					c.logError(fmt.Sprintf("site-%d", result.siteID), result.errorMsg)
				}

				c.checkAndNotifyStatusChange(result.siteID, result.userID, result.siteName, newIsUp)
			} else {
				c.updateSiteResponseTime(result.siteID, result.responseTime)
			}

			if result.useProxy {
				if result.proxyError {
					proxyFailures++
					proxySuccesses = 0
				} else if result.isUp {
					proxySuccesses++
					proxyFailures = 0
				}

				if proxyFailures >= proxyThreshold {
					c.proxyMu.Lock()
					if c.proxyAlive {
						log.Printf("Proxy appears to be down after %d consecutive failures", proxyFailures)
						c.proxyAlive = false
					}
					c.proxyMu.Unlock()
					proxyFailures = 0
				}

				if proxySuccesses >= proxyThreshold {
					c.proxyMu.Lock()
					if !c.proxyAlive {
						log.Printf("Proxy recovered after %d consecutive successes", proxySuccesses)
						c.proxyAlive = true
					}
					c.proxyMu.Unlock()
					proxySuccesses = 0
				}
			}
		}
	}
}

func (c *Checker) processCheckResult(result checkResult) (shouldUpdateDB, newIsUp bool) {
	dbIsUp := result.wasUp

	if result.isUp {
		c.failureCounters.Store(result.siteID, 0)

		if !dbIsUp {
			c.debugLogf("Site %d is UP (check passed), DB says DOWN, updating to UP", result.siteID)
			return true, true
		}
		return false, true
	}

	if !dbIsUp {
		c.debugLogf("Site %d check failed, DB already says DOWN, just updating response time", result.siteID)
		return false, false
	}

	counterInterface, _ := c.failureCounters.LoadOrStore(result.siteID, 0)
	currentCount, ok := counterInterface.(int)
	if !ok {
		currentCount = 0
	}
	newCount := currentCount + 1
	c.failureCounters.Store(result.siteID, newCount)

	c.debugLogf("Site %d check failed (DB says UP), failure count: %d/%d", result.siteID, newCount, c.downThreshold)

	if newCount >= c.downThreshold {
		c.debugLogf("Site %d reached failure threshold (%d), marking as DOWN", result.siteID, c.downThreshold)
		c.failureCounters.Store(result.siteID, 0)
		return true, false
	}

	return false, true
}

func (c *Checker) checkAndNotifyStatusChange(siteID int, userID *int, siteName string, currentIsUp bool) {
	stateInterface, exists := c.notifyStates.Load(siteID)
	if !exists {
		c.notifyStates.Store(siteID, &NotifyState{
			LastNotifiedState: currentIsUp,
			NotifiedAt:        time.Now(),
		})
		return
	}

	state, ok := stateInterface.(*NotifyState)
	if !ok {
		log.Printf("Error: invalid state type for site %d", siteID)
		return
	}

	statusChanged := state.LastNotifiedState != currentIsUp

	if statusChanged {
		now := time.Now()
		timeSinceLastNotification := now.Sub(state.NotifiedAt)

		if timeSinceLastNotification >= 30*time.Second {
			if userID != nil && *userID != 0 {
				go c.notifyOwner(*userID, siteName, currentIsUp)
			}

			state.LastNotifiedState = currentIsUp
			state.NotifiedAt = now
		}
	}

	c.notifyStates.Store(siteID, state)
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
			"*Site Status: Offline*\n\nYour site *%s* is currently not responding after %d consecutive checks\\. "+
				"Please check your server\\.",
			siteNameEscaped,
			c.downThreshold,
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

func (c *Checker) updateSiteStatus(id int, isUp bool, responseTime float64) {
	_, err := c.db.Exec("UPDATE sites SET is_up = $1, last_check = $2 WHERE id = $3", isUp, responseTime, id)
	if err != nil {
		log.Printf("Error updating site status: %v", err)
	}
}

func (c *Checker) updateSiteResponseTime(id int, responseTime float64) {
	_, err := c.db.Exec("UPDATE sites SET last_check = $1 WHERE id = $2", responseTime, id)
	if err != nil {
		log.Printf("Error updating site response time: %v", err)
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

type siteWithStatus struct {
	models.Site
	IsUp bool
}

func (c *Checker) getAllSitesWithStatus() ([]siteWithStatus, error) {
	rows, err := c.db.Query("SELECT id, name, url, user_id, is_up FROM sites")
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			log.Printf("Error closing rows: %v", cerr)
		}
	}()

	var sites []siteWithStatus
	for rows.Next() {
		var site siteWithStatus
		if scanErr := rows.Scan(&site.ID, &site.Name, &site.URL, &site.UserID, &site.IsUp); scanErr != nil {
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
