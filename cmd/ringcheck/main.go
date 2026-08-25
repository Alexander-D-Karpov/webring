// Command ringcheck drives a headless Chromium over every member of the webring and
// records how well each one is holding up its end of it.
//
// It ships as its own binary and image so the web server does not have to carry a
// browser. Both talk to the same database: this writes site_health, the server only
// reads it.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"webring/internal/database"
	"webring/internal/health"

	"github.com/joho/godotenv"
)

func main() {
	os.Exit(run())
}

// run does the work in a function so deferred cleanup unwinds before the process exits.
func run() int {
	once := flag.Bool("once", false, "run a single pass and exit instead of looping")
	flag.Parse()

	if err := godotenv.Load(); err != nil {
		log.Println("No .env file loaded:", err)
	}

	cfg, err := health.LoadConfig()
	if err != nil {
		if errors.Is(err, health.ErrNoBrowser) {
			log.Print("No Chromium found. Install chromium or set CHROME_PATH; " +
				"the ring integrity checker cannot run without a browser.")
			return 1
		}
		log.Printf("Failed to configure the checker: %v", err)
		return 1
	}

	db, err := database.Connect()
	if err != nil {
		log.Printf("Failed to connect to the database: %v", err)
		return 1
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			log.Printf("Failed to close the database connection: %v", closeErr)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	checker := health.NewChecker(db, cfg)

	if *once {
		if err := checker.RunOnce(ctx); err != nil {
			log.Printf("Ring integrity pass failed: %v", err)
			return 1
		}
		return 0
	}

	checker.Run(ctx)
	return 0
}
