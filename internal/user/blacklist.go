package user

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"

	"webring/internal/blacklist"
	"webring/internal/models"

	"github.com/gorilla/mux"
)

const blacklistPath = "/admin/blacklist"

var blacklistErrors = map[string]string{
	"invalid": "Enter a valid subject: a Telegram username (or #id) for users, a slug or domain for sites.",
	"save":    "Could not save that blacklist entry.",
	"remove":  "Could not remove that entry.",
}

func RegisterBlacklistHandlers(r *mux.Router, db *sql.DB) {
	blacklistRouter := r.PathPrefix(blacklistPath).Subrouter()
	blacklistRouter.Use(adminAuthMiddleware(db))
	blacklistRouter.HandleFunc("", blacklistPageHandler(db)).Methods("GET")
	blacklistRouter.HandleFunc("/add", addBlacklistEntryHandler(db)).Methods("POST")
	blacklistRouter.HandleFunc("/{id}/remove", removeBlacklistEntryHandler(db)).Methods("POST")
	blacklistRouter.HandleFunc("/cooldowns/{id}/remove", removeCooldownHandler(db)).Methods("POST")
}

func blacklistPageHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := blacklist.Entries(db, blacklist.SubjectUser)
		if err != nil {
			log.Printf("Error fetching blacklisted users: %v", err)
			http.Error(w, "Error fetching blacklist", http.StatusInternalServerError)
			return
		}

		sites, err := blacklist.Entries(db, blacklist.SubjectSite)
		if err != nil {
			log.Printf("Error fetching blacklisted sites: %v", err)
			http.Error(w, "Error fetching blacklist", http.StatusInternalServerError)
			return
		}

		cooldowns, err := blacklist.ActiveCooldowns(db)
		if err != nil {
			log.Printf("Error fetching cooldowns: %v", err)
			http.Error(w, "Error fetching cooldowns", http.StatusInternalServerError)
			return
		}

		templatesMu.RLock()
		t := templates
		templatesMu.RUnlock()

		if t == nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		data := struct {
			CurrentUser    *models.User
			Users          []blacklist.Entry
			Sites          []blacklist.Entry
			Cooldowns      []blacklist.Cooldown
			CooldownWindow string
			Error          string
			Request        *http.Request
		}{
			CurrentUser:    GetUserFromContext(r.Context()),
			Users:          users,
			Sites:          sites,
			Cooldowns:      cooldowns,
			CooldownWindow: blacklist.FormatDuration(blacklist.CooldownDuration()),
			Error:          blacklistErrors[r.URL.Query().Get("error")],
			Request:        r,
		}

		if err = t.ExecuteTemplate(w, "blacklist.html", data); err != nil {
			log.Printf("Error rendering blacklist template: %v", err)
			http.Error(w, "Error rendering template", http.StatusInternalServerError)
			return
		}
	}
}

func addBlacklistEntryHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)

		currentUser := GetUserFromContext(r.Context())
		var createdBy *int
		if currentUser != nil && currentUser.ID > 0 {
			createdBy = &currentUser.ID
		}

		err := blacklist.Add(db,
			r.FormValue("subject_type"),
			r.FormValue("subject_value"),
			sanitizeInput(r.FormValue("reason")),
			createdBy)
		switch {
		case errors.Is(err, blacklist.ErrInvalidSubject):
			redirectToBlacklist(w, r, "invalid")
		case err != nil:
			log.Printf("Error adding blacklist entry: %v", err)
			redirectToBlacklist(w, r, "save")
		default:
			redirectToBlacklist(w, r, "")
		}
	}
}

func removeBlacklistEntryHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(mux.Vars(r)["id"])
		if err != nil {
			http.Error(w, "Invalid entry ID", http.StatusBadRequest)
			return
		}

		if err = blacklist.Remove(db, id); err != nil {
			log.Printf("Error removing blacklist entry %d: %v", id, err)
			redirectToBlacklist(w, r, "remove")
			return
		}

		redirectToBlacklist(w, r, "")
	}
}

func removeCooldownHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(mux.Vars(r)["id"])
		if err != nil {
			http.Error(w, "Invalid cooldown ID", http.StatusBadRequest)
			return
		}

		if err = blacklist.RemoveCooldown(db, id); err != nil {
			log.Printf("Error removing cooldown %d: %v", id, err)
			redirectToBlacklist(w, r, "remove")
			return
		}

		redirectToBlacklist(w, r, "")
	}
}

func redirectToBlacklist(w http.ResponseWriter, r *http.Request, errorCode string) {
	target := blacklistPath
	if errorCode != "" {
		target += "?error=" + errorCode
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
