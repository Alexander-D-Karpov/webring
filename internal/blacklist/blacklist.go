// Package blacklist keeps two kinds of block out of the submission path: entries an
// admin added by hand, which are permanent until removed, and cooldowns, which are
// recorded automatically when a request is rejected and expire on their own.
//
// Subjects are normalized strings so both kinds can share one lookup: a user is their
// lowercase Telegram username, or "#<id>" when they have none, and a site is its slug
// and the host of its URL.
package blacklist

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"webring/internal/models"

	"github.com/lib/pq"
)

const (
	SubjectUser = "user"
	SubjectSite = "site"

	DefaultCooldown = 24 * time.Hour
	cooldownEnvVar  = "REJECT_COOLDOWN"

	maxReasonRunes = 200
)

var ErrInvalidSubject = errors.New("invalid blacklist subject")

type Entry struct {
	ID          int
	SubjectType string
	Subject     string
	Reason      string
	AddedBy     string
	CreatedAt   time.Time
}

type Cooldown struct {
	ID          int
	SubjectType string
	Subject     string
	Reason      string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

func (c Cooldown) Remaining() string {
	return FormatDuration(time.Until(c.ExpiresAt))
}

// Block is why a submission was refused. A zero ExpiresAt means a blacklist entry
// rather than a cooldown.
type Block struct {
	SubjectType string
	Subject     string
	Reason      string
	ExpiresAt   time.Time
}

func (b *Block) Permanent() bool {
	return b != nil && b.ExpiresAt.IsZero()
}

func (b *Block) Message() string {
	if b == nil {
		return ""
	}

	var message string
	switch {
	case b.Permanent() && b.SubjectType == SubjectSite:
		message = fmt.Sprintf("'%s' is blocked from the webring.", b.Subject)
	case b.Permanent():
		message = "Your account is blocked from submitting to the webring."
	case b.SubjectType == SubjectSite:
		message = fmt.Sprintf("A request for '%s' was rejected recently. You can submit it again in %s.",
			b.Subject, FormatDuration(time.Until(b.ExpiresAt)))
	default:
		message = fmt.Sprintf("A previous request of yours was rejected. You can submit again in %s.",
			FormatDuration(time.Until(b.ExpiresAt)))
	}

	if b.Reason != "" {
		message += " Reason: " + b.Reason
	}
	return message
}

// CooldownDuration reads REJECT_COOLDOWN as a Go duration string ("24h", "24h5m").
func CooldownDuration() time.Duration {
	raw := strings.TrimSpace(os.Getenv(cooldownEnvVar))
	if raw == "" {
		return DefaultCooldown
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		log.Printf("Invalid %s value; falling back to %s", cooldownEnvVar, DefaultCooldown)
		return DefaultCooldown
	}
	return parsed
}

func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return "less than a minute"
	}

	d = d.Round(time.Minute)
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60

	switch {
	case hours > 0 && minutes > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	case minutes > 0:
		return fmt.Sprintf("%dm", minutes)
	default:
		return "less than a minute"
	}
}

func NormalizeUser(value string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "@")
}

func NormalizeSite(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.TrimPrefix(normalized, "http://")
	normalized = strings.TrimPrefix(normalized, "https://")
	normalized = strings.TrimPrefix(normalized, "www.")

	if idx := strings.IndexAny(normalized, "/?#"); idx > 0 {
		normalized = normalized[:idx]
	}
	if idx := strings.Index(normalized, ":"); idx > 0 {
		normalized = normalized[:idx]
	}

	return strings.Trim(normalized, "/")
}

func Normalize(subjectType, value string) string {
	if subjectType == SubjectSite {
		return NormalizeSite(value)
	}
	return NormalizeUser(value)
}

// UserSubjects returns the identifiers a user can be blocked under. An anonymous
// account is shared by every submission that carried no Telegram handle, so it is
// deliberately unidentifiable and gets no subjects at all.
func UserSubjects(user *models.User) []string {
	if user == nil {
		return nil
	}

	var subjects []string
	if user.TelegramUsername != nil {
		if name := NormalizeUser(*user.TelegramUsername); name != "" {
			subjects = append(subjects, name)
		}
	}

	if len(subjects) == 0 && user.TelegramID == 0 {
		return nil
	}

	return append(subjects, fmt.Sprintf("#%d", user.ID))
}

func UsernameSubjects(username string) []string {
	if name := NormalizeUser(username); name != "" {
		return []string{name}
	}
	return nil
}

func SiteSubjects(slug, rawURL string) []string {
	var subjects []string
	if normalized := NormalizeSite(slug); normalized != "" {
		subjects = append(subjects, normalized)
	}

	if host := hostOf(rawURL); host != "" {
		for _, existing := range subjects {
			if existing == host {
				return subjects
			}
		}
		subjects = append(subjects, host)
	}

	return subjects
}

func RequestSiteSubjects(request *models.UpdateRequest) []string {
	if request == nil {
		return nil
	}

	slug, siteURL := "", ""
	if request.Site != nil {
		slug, siteURL = request.Site.Slug, request.Site.URL
	}
	if value, ok := request.ChangedFields["slug"].(string); ok && value != "" {
		slug = value
	}
	if value, ok := request.ChangedFields["url"].(string); ok && value != "" {
		siteURL = value
	}

	return SiteSubjects(slug, siteURL)
}

func hostOf(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	return NormalizeSite(parsed.Host)
}

// Check applies both blacklist entries and cooldowns. Use it for new submissions.
func Check(db *sql.DB, userSubjects, siteSubjects []string) (*Block, error) {
	block, err := checkBlacklist(db, userSubjects, siteSubjects)
	if err != nil || block != nil {
		return block, err
	}
	return CheckCooldown(db, userSubjects, siteSubjects)
}

// CheckCooldown applies cooldowns only, leaving blacklist entries out. Use it for
// updates to sites that are already in the ring.
func CheckCooldown(db *sql.DB, userSubjects, siteSubjects []string) (*Block, error) {
	if len(userSubjects) == 0 && len(siteSubjects) == 0 {
		return nil, nil
	}

	var block Block
	err := db.QueryRow(`
		SELECT subject_type, subject_value, COALESCE(reason, ''), expires_at
		FROM request_cooldowns
		WHERE expires_at > NOW()
		  AND ((subject_type = 'user' AND subject_value = ANY($1))
		    OR (subject_type = 'site' AND subject_value = ANY($2)))
		ORDER BY expires_at DESC
		LIMIT 1
	`, pq.Array(userSubjects), pq.Array(siteSubjects)).Scan(
		&block.SubjectType, &block.Subject, &block.Reason, &block.ExpiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return &block, nil
}

func checkBlacklist(db *sql.DB, userSubjects, siteSubjects []string) (*Block, error) {
	if len(userSubjects) == 0 && len(siteSubjects) == 0 {
		return nil, nil
	}

	var block Block
	err := db.QueryRow(`
		SELECT subject_type, subject_value, COALESCE(reason, '')
		FROM blacklist
		WHERE (subject_type = 'user' AND subject_value = ANY($1))
		   OR (subject_type = 'site' AND subject_value = ANY($2))
		ORDER BY CASE subject_type WHEN 'user' THEN 0 ELSE 1 END
		LIMIT 1
	`, pq.Array(userSubjects), pq.Array(siteSubjects)).Scan(
		&block.SubjectType, &block.Subject, &block.Reason)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return &block, nil
}

// RecordRejection starts a cooldown for the submitter and for the site the request
// concerned, as separate rows, so removing one does not lift the other.
func RecordRejection(db *sql.DB, request *models.UpdateRequest) error {
	if request == nil {
		return nil
	}

	expiresAt := time.Now().Add(CooldownDuration())
	reason := fmt.Sprintf("Request #%d rejected", request.ID)

	for _, subject := range UserSubjects(request.User) {
		if err := upsertCooldown(db, SubjectUser, subject, reason, expiresAt); err != nil {
			return err
		}
	}
	for _, subject := range RequestSiteSubjects(request) {
		if err := upsertCooldown(db, SubjectSite, subject, reason, expiresAt); err != nil {
			return err
		}
	}

	return nil
}

func upsertCooldown(db *sql.DB, subjectType, subject, reason string, expiresAt time.Time) error {
	_, err := db.Exec(`
		INSERT INTO request_cooldowns (subject_type, subject_value, reason, expires_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (subject_type, subject_value)
		DO UPDATE SET reason = EXCLUDED.reason, expires_at = EXCLUDED.expires_at, created_at = NOW()
	`, subjectType, subject, reason, expiresAt)
	return err
}

func Entries(db *sql.DB, subjectType string) ([]Entry, error) {
	rows, err := db.Query(`
		SELECT b.id, b.subject_type, b.subject_value, COALESCE(b.reason, ''),
		       COALESCE('@' || u.telegram_username, u.first_name, ''), b.created_at
		FROM blacklist b
		LEFT JOIN users u ON b.created_by = u.id
		WHERE b.subject_type = $1
		ORDER BY b.created_at DESC
	`, subjectType)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)

	var entries []Entry
	for rows.Next() {
		var entry Entry
		if scanErr := rows.Scan(&entry.ID, &entry.SubjectType, &entry.Subject,
			&entry.Reason, &entry.AddedBy, &entry.CreatedAt); scanErr != nil {
			return nil, scanErr
		}
		entries = append(entries, entry)
	}

	return entries, rows.Err()
}

func ActiveCooldowns(db *sql.DB) ([]Cooldown, error) {
	rows, err := db.Query(`
		SELECT id, subject_type, subject_value, COALESCE(reason, ''), created_at, expires_at
		FROM request_cooldowns
		WHERE expires_at > NOW()
		ORDER BY expires_at
	`)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)

	var cooldowns []Cooldown
	for rows.Next() {
		var cooldown Cooldown
		if scanErr := rows.Scan(&cooldown.ID, &cooldown.SubjectType, &cooldown.Subject,
			&cooldown.Reason, &cooldown.CreatedAt, &cooldown.ExpiresAt); scanErr != nil {
			return nil, scanErr
		}
		cooldowns = append(cooldowns, cooldown)
	}

	return cooldowns, rows.Err()
}

func Add(db *sql.DB, subjectType, value, reason string, createdBy *int) error {
	if subjectType != SubjectUser && subjectType != SubjectSite {
		return ErrInvalidSubject
	}

	subject := Normalize(subjectType, value)
	if subject == "" {
		return ErrInvalidSubject
	}

	if runes := []rune(reason); len(runes) > maxReasonRunes {
		reason = string(runes[:maxReasonRunes])
	}

	_, err := db.Exec(`
		INSERT INTO blacklist (subject_type, subject_value, reason, created_by)
		VALUES ($1, $2, NULLIF($3, ''), $4)
		ON CONFLICT (subject_type, subject_value)
		DO UPDATE SET reason = EXCLUDED.reason, created_by = EXCLUDED.created_by, created_at = NOW()
	`, subjectType, subject, strings.TrimSpace(reason), createdBy)
	return err
}

func Remove(db *sql.DB, id int) error {
	_, err := db.Exec("DELETE FROM blacklist WHERE id = $1", id)
	return err
}

func RemoveCooldown(db *sql.DB, id int) error {
	_, err := db.Exec("DELETE FROM request_cooldowns WHERE id = $1", id)
	return err
}

func PurgeExpired(db *sql.DB) {
	if _, err := db.Exec("DELETE FROM request_cooldowns WHERE expires_at <= NOW()"); err != nil {
		log.Printf("Error purging expired cooldowns: %v", err)
	}
}

func closeRows(rows *sql.Rows) {
	if err := rows.Close(); err != nil {
		log.Printf("Error closing rows: %v", err)
	}
}
