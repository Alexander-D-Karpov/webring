package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"webring/internal/models"

	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
)

const (
	siteOneSlug   = "site-one"
	siteTwoSlug   = "site-two"
	siteThreeSlug = "site-three"
)

type TestServer struct {
	server *httptest.Server
	url    string
}

type TestServers struct {
	servers []*TestServer
	mu      sync.Mutex
}

func NewTestServers(count int) *TestServers {
	ts := &TestServers{
		servers: make([]*TestServer, count),
	}

	for i := 0; i < count; i++ {
		ts.servers[i] = ts.createServer(i)
	}

	time.Sleep(100 * time.Millisecond)

	return ts
}

func (ts *TestServers) createServer(index int) *TestServer {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprintf(w, "<html><body>Test Server %d - OK</body></html>", index+1); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	server := httptest.NewServer(handler)

	testServer := &TestServer{
		server: server,
		url:    server.URL,
	}

	return testServer
}

func (ts *TestServers) GetURL(index int) string {
	if index < 0 || index >= len(ts.servers) {
		return ""
	}
	return ts.servers[index].url
}

func (ts *TestServers) Close() {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	for _, server := range ts.servers {
		if server != nil && server.server != nil {
			server.server.Close()
		}
	}
}

func (ts *TestServers) StopServer(index int) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if index < 0 || index >= len(ts.servers) {
		return
	}

	if ts.servers[index] != nil && ts.servers[index].server != nil {
		ts.servers[index].server.Close()
		ts.servers[index].server = nil
	}
}

func (ts *TestServers) StartServer(index int) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if index < 0 || index >= len(ts.servers) {
		return
	}

	if ts.servers[index].server == nil {
		ts.servers[index] = ts.createServer(index)
	}
}

func (ts *TestServers) IsServerUp(index int) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if index < 0 || index >= len(ts.servers) {
		return false
	}

	return ts.servers[index] != nil && ts.servers[index].server != nil
}

func setupTestDB(t *testing.T) *sql.DB {
	connStr := "postgres://postgres:postgres@localhost:5432/webring_test?sslmode=disable"
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}

	if err = db.Ping(); err != nil {
		t.Fatalf("Failed to ping test database: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS sites (
			id SERIAL PRIMARY KEY,
			slug TEXT UNIQUE NOT NULL,
			name TEXT NOT NULL,
			url TEXT NOT NULL,
			is_up BOOLEAN NOT NULL DEFAULT true,
			last_check FLOAT NOT NULL DEFAULT 0,
			favicon TEXT,
			user_id INTEGER,
			display_order INTEGER NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create sites table: %v", err)
	}

	_, err = db.Exec("TRUNCATE TABLE sites RESTART IDENTITY CASCADE")
	if err != nil {
		t.Fatalf("Failed to truncate sites table: %v", err)
	}

	return db
}

func setupTestData(t *testing.T, db *sql.DB, servers *TestServers) {
	testData := []struct {
		id    int
		slug  string
		name  string
		url   string
		isUp  bool
		order int
	}{
		{1, siteOneSlug, "Site One", servers.GetURL(0), true, 1},
		{2, siteTwoSlug, "Site Two", servers.GetURL(1), true, 2},
		{3, siteThreeSlug, "Site Three", servers.GetURL(2), true, 3},
	}

	for _, td := range testData {
		_, err := db.Exec(`
			INSERT INTO sites (id, slug, name, url, is_up, display_order)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, td.id, td.slug, td.name, td.url, td.isUp, td.order)
		if err != nil {
			t.Fatalf("Failed to insert test data: %v", err)
		}
	}
}

func teardownTestDB(t *testing.T, db *sql.DB) {
	_, err := db.Exec("TRUNCATE TABLE sites RESTART IDENTITY CASCADE")
	if err != nil {
		t.Errorf("Failed to cleanup test data: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Errorf("Failed to close database: %v", err)
	}
}

func TestListPublicSitesHandler(t *testing.T) {
	servers := NewTestServers(3)
	defer servers.Close()

	db := setupTestDB(t)
	defer teardownTestDB(t, db)

	setupTestData(t, db, servers)

	r := mux.NewRouter()
	RegisterHandlers(r, db)

	req := httptest.NewRequest("GET", "/sites", http.NoBody)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var sites []models.PublicSite
	if err := json.NewDecoder(w.Body).Decode(&sites); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if len(sites) != 3 {
		t.Errorf("Expected 3 sites, got %d", len(sites))
	}

	if sites[0].Slug != siteOneSlug {
		t.Errorf("Expected first site to be %q, got %q", siteOneSlug, sites[0].Slug)
	}

	for i, site := range sites {
		expectedURL := servers.GetURL(i)
		if site.URL != expectedURL {
			t.Errorf("Site %d: expected URL %s, got %s", i, expectedURL, site.URL)
		}
	}
}

func TestSiteDataHandler(t *testing.T) {
	servers := NewTestServers(3)
	defer servers.Close()

	db := setupTestDB(t)
	defer teardownTestDB(t, db)

	setupTestData(t, db, servers)

	r := mux.NewRouter()
	RegisterHandlers(r, db)

	req := httptest.NewRequest("GET", "/site-two/data", http.NoBody)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var data models.SiteData
	if err := json.NewDecoder(w.Body).Decode(&data); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if data.Curr.Slug != siteTwoSlug {
		t.Errorf("Expected current site to be %q, got %q", siteTwoSlug, data.Curr.Slug)
	}

	if data.Prev.Slug != siteOneSlug {
		t.Errorf("Expected previous site to be %q, got %q", siteOneSlug, data.Prev.Slug)
	}

	if data.Next.Slug != siteThreeSlug {
		t.Errorf("Expected next site to be %q, got %q", siteThreeSlug, data.Next.Slug)
	}

	if data.Curr.URL != servers.GetURL(1) {
		t.Errorf("Expected current URL %s, got %s", servers.GetURL(1), data.Curr.URL)
	}
}

func testNavigationHandler(t *testing.T, endpoint, expectedSlug string, expectedURLIndex int, jsonField string) {
	servers := NewTestServers(3)
	defer servers.Close()

	db := setupTestDB(t)
	defer teardownTestDB(t, db)

	setupTestData(t, db, servers)

	r := mux.NewRouter()
	RegisterHandlers(r, db)

	req := httptest.NewRequest("GET", endpoint, http.NoBody)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response map[string]*models.PublicSite
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	site := response[jsonField]
	if site == nil {
		t.Fatalf("Expected %s in response, got nil", jsonField)
	}

	if site.Slug != expectedSlug {
		t.Errorf("Expected %s site to be %q, got %q", jsonField, expectedSlug, site.Slug)
	}

	if site.URL != servers.GetURL(expectedURLIndex) {
		t.Errorf("Expected URL %s, got %s", servers.GetURL(expectedURLIndex), site.URL)
	}
}

func TestNextSiteHandler(t *testing.T) {
	testNavigationHandler(t, "/site-one/next/data", siteTwoSlug, 1, "next")
}

func TestPreviousSiteHandler(t *testing.T) {
	testNavigationHandler(t, "/site-two/prev/data", siteOneSlug, 0, "previous")
}

func TestRandomSiteHandler(t *testing.T) {
	servers := NewTestServers(3)
	defer servers.Close()

	db := setupTestDB(t)
	defer teardownTestDB(t, db)

	setupTestData(t, db, servers)

	r := mux.NewRouter()
	RegisterHandlers(r, db)

	req := httptest.NewRequest("GET", "/site-one/random/data", http.NoBody)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response struct {
		Random *models.PublicSite `json:"random"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response.Random == nil {
		t.Error("Expected random site, got nil")
	}

	validURLs := map[string]bool{
		servers.GetURL(0): true,
		servers.GetURL(1): true,
		servers.GetURL(2): true,
	}

	if !validURLs[response.Random.URL] {
		t.Errorf("Random site URL %s is not one of the test servers", response.Random.URL)
	}
}

func TestRedirectHandlers(t *testing.T) {
	servers := NewTestServers(3)
	defer servers.Close()

	db := setupTestDB(t)
	defer teardownTestDB(t, db)

	setupTestData(t, db, servers)

	r := mux.NewRouter()
	RegisterHandlers(r, db)

	tests := []struct {
		name         string
		path         string
		expectedCode int
		checkURL     bool
		urlIndex     int
	}{
		{"Current site redirect", "/site-one", http.StatusFound, true, 0},
		{"Next site redirect", "/site-one/next", http.StatusFound, true, 1},
		{"Previous site redirect", "/site-two/prev", http.StatusFound, true, 0},
		{"Random site redirect", "/site-one/random", http.StatusFound, false, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, http.NoBody)
			w := httptest.NewRecorder()

			r.ServeHTTP(w, req)

			if w.Code != tt.expectedCode {
				t.Errorf("Expected status %d, got %d", tt.expectedCode, w.Code)
			}

			location := w.Header().Get("Location")
			if location == "" {
				t.Error("Expected Location header, got empty")
			}

			if tt.checkURL && tt.urlIndex >= 0 {
				expectedURL := servers.GetURL(tt.urlIndex)
				if location != expectedURL {
					t.Errorf("Expected redirect to %s, got %s", expectedURL, location)
				}
			}
		})
	}
}

func TestNotFoundSite(t *testing.T) {
	servers := NewTestServers(3)
	defer servers.Close()

	db := setupTestDB(t)
	defer teardownTestDB(t, db)

	setupTestData(t, db, servers)

	r := mux.NewRouter()
	RegisterHandlers(r, db)

	req := httptest.NewRequest("GET", "/nonexistent-site/data", http.NoBody)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}
}

func TestCORSHeaders(t *testing.T) {
	servers := NewTestServers(3)
	defer servers.Close()

	db := setupTestDB(t)
	defer teardownTestDB(t, db)

	setupTestData(t, db, servers)

	r := mux.NewRouter()
	RegisterHandlers(r, db)

	req := httptest.NewRequest("GET", "/sites", http.NoBody)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	corsHeader := w.Header().Get("Access-Control-Allow-Origin")
	if corsHeader != "*" {
		t.Errorf("Expected CORS header '*', got '%s'", corsHeader)
	}
}

func TestServerDownSite(t *testing.T) {
	servers := NewTestServers(3)
	defer servers.Close()

	db := setupTestDB(t)
	defer teardownTestDB(t, db)

	setupTestData(t, db, servers)

	servers.StopServer(2)

	_, err := db.Exec("UPDATE sites SET is_up = false WHERE id = 3")
	if err != nil {
		t.Fatalf("Failed to update site status: %v", err)
	}

	r := mux.NewRouter()
	RegisterHandlers(r, db)

	req := httptest.NewRequest("GET", "/sites", http.NoBody)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	var sites []models.PublicSite
	if err := json.NewDecoder(w.Body).Decode(&sites); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if len(sites) != 2 {
		t.Errorf("Expected 2 sites (only up sites), got %d", len(sites))
	}

	for _, site := range sites {
		if site.Slug == siteThreeSlug {
			t.Errorf("Down site %q should not be in the list", siteThreeSlug)
		}
	}
}

func TestWrapAroundNavigation(t *testing.T) {
	servers := NewTestServers(3)
	defer servers.Close()

	db := setupTestDB(t)
	defer teardownTestDB(t, db)

	setupTestData(t, db, servers)

	r := mux.NewRouter()
	RegisterHandlers(r, db)

	t.Run("Next from last site wraps to first", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/site-three/next/data", http.NoBody)
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		var response struct {
			Next *models.PublicSite `json:"next"`
		}
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if response.Next.Slug != siteOneSlug {
			t.Errorf("Expected wrap to %q, got %q", siteOneSlug, response.Next.Slug)
		}
	})

	t.Run("Previous from first site wraps to last", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/site-one/prev/data", http.NoBody)
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		var response struct {
			Previous *models.PublicSite `json:"previous"`
		}
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if response.Previous.Slug != siteThreeSlug {
			t.Errorf("Expected wrap to %q, got %q", siteThreeSlug, response.Previous.Slug)
		}
	})
}

func TestServerConnectivity(t *testing.T) {
	servers := NewTestServers(3)
	defer servers.Close()

	db := setupTestDB(t)
	defer teardownTestDB(t, db)

	setupTestData(t, db, servers)

	for i := 0; i < 3; i++ {
		t.Run(fmt.Sprintf("Server %d connectivity", i+1), func(t *testing.T) {
			url := servers.GetURL(i)

			resp, err := http.Get(url) // #nosec G107
			if err != nil {
				t.Fatalf("Failed to connect to server %d at %s: %v", i+1, url, err)
			}
			defer func() {
				if cerr := resp.Body.Close(); cerr != nil {
					t.Errorf("Failed to close response body: %v", cerr)
				}
			}()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("Expected status 200 from server %d, got %d", i+1, resp.StatusCode)
			}
		})
	}
}

func TestServerStartStop(t *testing.T) {
	servers := NewTestServers(3)
	defer servers.Close()

	t.Run("Stop and start server", func(t *testing.T) {
		if !servers.IsServerUp(1) {
			t.Error("Server 1 should be up initially")
		}

		url := servers.GetURL(1)

		resp, err := http.Get(url) // #nosec G107
		if err != nil {
			t.Fatalf("Server 1 should be reachable: %v", err)
		}
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("Failed to close response body: %v", cerr)
		}

		servers.StopServer(1)

		if servers.IsServerUp(1) {
			t.Error("Server 1 should be down after stopping")
		}

		resp2, err := http.Get(url) // #nosec G107
		if err == nil {
			if cerr := resp2.Body.Close(); cerr != nil {
				t.Errorf("Failed to close response body: %v", cerr)
			}
			t.Error("Server 1 should not be reachable after stopping")
		}

		servers.StartServer(1)

		time.Sleep(50 * time.Millisecond)

		if !servers.IsServerUp(1) {
			t.Error("Server 1 should be up after starting")
		}

		newURL := servers.GetURL(1)
		resp3, err := http.Get(newURL) // #nosec G107
		if err != nil {
			t.Fatalf("Server 1 should be reachable after restart: %v", err)
		}
		if cerr := resp3.Body.Close(); cerr != nil {
			t.Errorf("Failed to close response body: %v", cerr)
		}
	})
}

func TestMultipleServersIndependence(t *testing.T) {
	servers := NewTestServers(3)
	defer servers.Close()

	t.Run("Servers are independent", func(t *testing.T) {
		servers.StopServer(1)

		resp0, err0 := http.Get(servers.GetURL(0)) // #nosec G107
		if err0 != nil {
			t.Errorf("Server 0 should still be reachable: %v", err0)
		} else {
			if cerr := resp0.Body.Close(); cerr != nil {
				t.Errorf("Failed to close response body: %v", cerr)
			}
		}

		resp2, err2 := http.Get(servers.GetURL(2)) // #nosec G107
		if err2 != nil {
			t.Errorf("Server 2 should still be reachable: %v", err2)
		} else {
			if cerr := resp2.Body.Close(); cerr != nil {
				t.Errorf("Failed to close response body: %v", cerr)
			}
		}

		resp1, err1 := http.Get(servers.GetURL(1)) // #nosec G107
		if err1 == nil {
			if cerr := resp1.Body.Close(); cerr != nil {
				t.Errorf("Failed to close response body: %v", cerr)
			}
			t.Error("Server 1 should not be reachable")
		}
	})
}

func TestConcurrentRequests(t *testing.T) {
	servers := NewTestServers(3)
	defer servers.Close()

	db := setupTestDB(t)
	defer teardownTestDB(t, db)

	setupTestData(t, db, servers)

	r := mux.NewRouter()
	RegisterHandlers(r, db)

	t.Run("Concurrent requests to different endpoints", func(t *testing.T) {
		var wg sync.WaitGroup
		errors := make(chan error, 10)

		endpoints := []string{
			"/sites",
			"/site-one/data",
			"/site-two/data",
			"/site-three/data",
			"/site-one/next/data",
			"/site-one/prev/data",
			"/site-two/next/data",
			"/site-two/prev/data",
			"/site-one/random/data",
			"/site-two/random/data",
		}

		for _, endpoint := range endpoints {
			wg.Add(1)
			go func(ep string) {
				defer wg.Done()

				req := httptest.NewRequest("GET", ep, http.NoBody)
				w := httptest.NewRecorder()

				r.ServeHTTP(w, req)

				if w.Code != http.StatusOK {
					errors <- fmt.Errorf("endpoint %s returned status %d", ep, w.Code)
				}
			}(endpoint)
		}

		wg.Wait()
		close(errors)

		for err := range errors {
			t.Error(err)
		}
	})
}
