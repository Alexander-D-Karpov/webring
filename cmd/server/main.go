package main

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"webring"
	"webring/internal/api"
	"webring/internal/auth"
	"webring/internal/dashboard"
	"webring/internal/database"
	"webring/internal/public"
	"webring/internal/telegram"
	"webring/internal/uptime"
	"webring/internal/user"

	"github.com/gorilla/csrf"
	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
)

const (
	filePerm          = 0o600
	readTimeout       = 15 * time.Second
	writeTimeout      = 15 * time.Second
	serverIdleTimeout = 60 * time.Second
)

func setupLogging() (*os.File, error) {
	logFilePath := os.Getenv("LOG_FILE_PATH")
	if logFilePath == "" {
		logFilePath = "webring.log"
	}

	cleaned := filepath.Clean(logFilePath)
	if !filepath.IsAbs(cleaned) {
		cleaned = filepath.Join(".", cleaned)
	}

	absBase, err := filepath.Abs(".")
	if err != nil {
		return nil, err
	}
	absTarget, err := filepath.Abs(cleaned)
	if err != nil {
		return nil, err
	}
	if absTarget != absBase && !strings.HasPrefix(absTarget, absBase+string(os.PathSeparator)) {
		return nil, fmt.Errorf("invalid log path: %s", logFilePath)
	}

	dir := filepath.Dir(absTarget)
	if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
		return nil, mkErr
	}

	logFile, err := os.OpenFile(absTarget, os.O_CREATE|os.O_WRONLY|os.O_APPEND, filePerm) // #nosec G304
	if err != nil {
		return nil, err
	}

	log.SetOutput(io.MultiWriter(os.Stdout, logFile))
	return logFile, nil
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("Error loading .env file:", err)
	}

	logFile, err := setupLogging()
	if err != nil {
		log.Fatal("Failed to set up logging:", err)
	}
	defer func() {
		if closeErr := logFile.Close(); closeErr != nil {
			log.Printf("Failed to close log file: %v", closeErr)
		}
	}()

	log.Println("Logging initialized. Log file:", logFile.Name())

	db, err := database.Connect()
	if err != nil {
		log.Printf("Failed to connect to database: %v", err)
		return
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("Failed to close database connection: %v", err)
		}
	}()

	messagesDir := os.Getenv("MESSAGES_DIR")
	if messagesDir == "" {
		messagesDir = "messages"
	}
	telegram.InitTemplates(messagesDir)

	startBackgroundServices(db)

	r := mux.NewRouter()
	registerHandlers(r, db)

	setupStaticFiles(r)
	setupMediaDirectory(r)
	templates := parseTemplates()
	initializeTemplates(templates)

	protected := setupCSRFMiddleware(r)

	startServer(protected)
}

func startBackgroundServices(db *sql.DB) {
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			auth.CleanExpiredSessions(db)
		}
	}()

	checker := uptime.NewChecker(db)
	go checker.Start()
}

func registerHandlers(r *mux.Router, db *sql.DB) {
	dashboard.RegisterHandlers(r, db)
	user.RegisterHandlers(r, db)
	public.RegisterSubmissionHandlers(r, db)
	api.RegisterSwaggerHandlers(r)
	api.RegisterHandlers(r, db)
	public.RegisterHandlers(r, db)
}

func setupStaticFiles(r *mux.Router) {
	staticFiles, err := fs.Sub(webring.Files, "static")
	if err != nil {
		log.Fatalf("Error accessing static files: %v", err)
	}
	r.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(http.FS(staticFiles))))
}

func parseTemplates() *template.Template {
	funcMap := template.FuncMap{
		"add":       func(a, b int) int { return a + b },
		"sub":       func(a, b int) int { return a - b },
		"csrfField": csrf.TemplateField,
		"csrfToken": csrf.Token,
	}

	t := template.New("").Funcs(funcMap)
	t, err := t.ParseFS(webring.Files,
		"internal/dashboard/templates/*.html",
		"internal/public/templates/*.html",
		"internal/user/templates/*.html")
	if err != nil {
		log.Fatalf("Error parsing templates: %v", err)
	}
	return t
}

func initializeTemplates(t *template.Template) {
	dashboard.InitTemplates(t)
	public.InitTemplates(t)
	user.InitTemplates(t)
}

func setupMediaDirectory(r *mux.Router) {
	mediaFolder := os.Getenv("MEDIA_FOLDER")
	if mediaFolder == "" {
		mediaFolder = "media"
	}

	if err := os.MkdirAll(mediaFolder, 0o750); err != nil {
		log.Fatalf("Failed to create media folder: %v", err)
	}

	r.PathPrefix("/media/").Handler(http.StripPrefix("/media/", http.FileServer(http.Dir(mediaFolder))))
}

func setupCSRFMiddleware(r *mux.Router) http.Handler {
	var key []byte
	if envKey := os.Getenv("CSRF_AUTH_KEY"); envKey != "" {
		key = []byte(envKey)
	} else {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			log.Fatalf("CSRF key generation failed: %v", err)
		}
		log.Printf("WARNING: CSRF_AUTH_KEY not set; using ephemeral key (tokens reset on restart).")
	}
	if len(key) < 32 {
		log.Fatalf("CSRF_AUTH_KEY must be at least 32 bytes; got %d", len(key))
	}

	secure := strings.EqualFold(os.Getenv("CSRF_SECURE"), "true")
	if os.Getenv("CSRF_SECURE") == "" {
		secure = true
	}

	sameSite := csrf.SameSiteLaxMode
	switch strings.ToLower(os.Getenv("CSRF_SAMESITE")) {
	case "strict":
		sameSite = csrf.SameSiteStrictMode
	case "none":
		sameSite = csrf.SameSiteNoneMode
	case "lax", "":
		sameSite = csrf.SameSiteLaxMode
	}

	cookieName := os.Getenv("CSRF_COOKIE_NAME")
	if cookieName == "" {
		cookieName = "_csrf"
	}

	opts := []csrf.Option{
		csrf.CookieName(cookieName),
		csrf.Secure(secure),
		csrf.HttpOnly(true),
		csrf.Path("/"),
		csrf.SameSite(sameSite),
		csrf.TrustedOrigins(parseTrustedOrigins()),
		csrf.ErrorHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		})),
	}

	protect := csrf.Protect(key, opts...)
	protected := protect(r)

	return withCSRFTokHeader(protected)
}

func parseTrustedOrigins() []string {
	raw := os.Getenv("CSRF_TRUSTED_ORIGINS")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func withCSRFTokHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			w.Header().Set("X-CSRF-Token", csrf.Token(r))
		}
		next.ServeHTTP(w, r)
	})
}

func startServer(handler http.Handler) {
	port := os.Getenv("PORT")
	if port == "" {
		fmt.Println("PORT environment variable not set. Defaulting to 8080")
		port = "8080"
	}

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  serverIdleTimeout,
	}

	log.Printf("Starting server on :%s", port)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal("Server failed to start:", err)
	}
}
