package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"webring"
	"webring/internal/public"

	"webring/internal/api"
	"webring/internal/dashboard"
	"webring/internal/database"
	"webring/internal/uptime"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
)

func setupLogging() (*os.File, error) {
	logFilePath := os.Getenv("LOG_FILE_PATH")
	if logFilePath == "" {
		logFilePath = "webring.log"
	}

	// Ensure the directory exists
	dir := filepath.Dir(logFilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	// Open the log file
	logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	// Set up multi-writer to write logs to both file and console
	multiWriter := io.MultiWriter(os.Stdout, logFile)
	log.SetOutput(multiWriter)

	return logFile, nil
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Println("Error loading .env file:", err)
	}

	logFile, err := setupLogging()
	if err != nil {
		log.Fatal("Failed to set up logging:", err)
	}
	defer func(logFile *os.File) {
		err := logFile.Close()
		if err != nil {
			log.Fatalf("Failed to close log file: %v", err)
		}
	}(logFile)

	log.Println("Logging initialized. Log file:", logFile.Name())

	db, err := database.Connect()
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer func(db *sql.DB) {
		err := db.Close()
		if err != nil {
			log.Fatalf("Failed to close database connection: %v", err)
		}
	}(db)

	checker := uptime.NewChecker(db)
	go checker.Start()

	r := mux.NewRouter()
	dashboard.RegisterHandlers(r, db)
	api.RegisterHandlers(r, db)

	// Serve static files
	staticFiles, err := fs.Sub(webring.Files, "static")
	if err != nil {
		log.Fatalf("Error accessing static files: %v", err)
	}
	r.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(http.FS(staticFiles))))

	// Parse templates
	t, err := template.ParseFS(webring.Files, "internal/dashboard/templates/*.html", "internal/public/templates/*.html")
	if err != nil {
		log.Fatalf("Error parsing templates: %v", err)
	}

	// Initialize dashboard templates
	dashboard.InitTemplates(t)

	// Initialize public templates
	public.InitTemplates(t)

	mediaFolder := os.Getenv("MEDIA_FOLDER")
	if mediaFolder == "" {
		mediaFolder = "media"
	}
	err = os.MkdirAll(mediaFolder, os.ModePerm)
	if err != nil {
		return
	}

	// Serve media files
	r.PathPrefix("/media/").Handler(http.StripPrefix("/media/", http.FileServer(http.Dir(mediaFolder))))

	// Register public handlers
	public.RegisterHandlers(r, db)

	port := os.Getenv("PORT")
	if port == "" {
		fmt.Println("PORT environment variable not set. Defaulting to 8080")
		port = "8080"
	}

	log.Printf("Starting server on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}
