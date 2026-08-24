package telegram

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"time"

	"webring/internal/models"
)

const requestTimeout = 10 * time.Second

var markdownV2EscapeRe = regexp.MustCompile(`([_*\[\]()~` + "`" + `>#+\-=|{}.!\\])`)

func EscapeMarkdownV2(text string) string {
	return markdownV2EscapeRe.ReplaceAllString(text, `\$1`)
}

func isDebugMode() bool {
	if debugStr := os.Getenv("TELEGRAM_DEBUG"); debugStr != "" {
		if debug, err := strconv.ParseBool(debugStr); err == nil {
			return debug
		}
	}
	return false
}

func logDebugf(format string, args ...interface{}) {
	if isDebugMode() {
		log.Printf(format, args...)
	}
}

// BotToken returns the configured bot token, empty when Telegram is disabled.
func BotToken() string {
	return os.Getenv("TELEGRAM_BOT_TOKEN")
}

// AdminChatID returns the shared admin group chat used for approval polls. Zero means
// no group is configured and polls are disabled.
func AdminChatID() int64 {
	raw := os.Getenv("TELEGRAM_ADMIN_CHAT_ID")
	if raw == "" {
		return 0
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		// The value itself is not echoed: it comes straight from the environment and
		// would land unescaped in the log.
		log.Println("Invalid TELEGRAM_ADMIN_CHAT_ID: not a chat ID; approval polls are disabled")
		return 0
	}
	return id
}

// ResolveUserName renders a user's best available display name.
func ResolveUserName(user *models.User) string {
	if user == nil {
		return "Unknown User"
	}
	if user.FirstName != nil && *user.FirstName != "" {
		name := *user.FirstName
		if user.LastName != nil && *user.LastName != "" {
			name += " " + *user.LastName
		}
		return name
	}
	if user.TelegramUsername != nil && *user.TelegramUsername != "" {
		return "@" + *user.TelegramUsername
	}
	return "Unknown User"
}

func fieldStr(fields map[string]interface{}, key string) string {
	if v, ok := fields[key].(string); ok {
		return v
	}
	return ""
}

// RequestSiteName returns the best display name for whatever site a request concerns.
func RequestSiteName(request *models.UpdateRequest) string {
	if request == nil {
		return "Unknown Site"
	}
	if request.RequestType == "create" {
		if name := fieldStr(request.ChangedFields, "name"); name != "" {
			return name
		}
	} else if request.Site != nil && request.Site.Name != "" {
		return request.Site.Name
	}
	return "Unknown Site"
}

func NotifyAdminsOfNewRequest(db *sql.DB, request *models.UpdateRequest, user *models.User) {
	botToken := BotToken()
	if botToken == "" {
		logDebugf("TELEGRAM_BOT_TOKEN not set, skipping admin notification")
		return
	}

	admins, err := AdminTelegramIDs(db)
	if err != nil {
		log.Printf("Error fetching admin Telegram IDs: %v", err)
		return
	}

	if len(admins) == 0 {
		logDebugf("No admins with Telegram IDs found")
		return
	}

	message := FormatRequestMessage(request, user)

	for _, adminID := range admins {
		go SendMessage(botToken, adminID, message)
	}
}

// AdminTelegramIDs lists the Telegram IDs of every admin who can receive messages.
func AdminTelegramIDs(db *sql.DB) ([]int64, error) {
	rows, err := db.QueryContext(context.Background(), `
		SELECT telegram_id FROM users WHERE is_admin = true AND telegram_id IS NOT NULL
	`)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			log.Printf("Error closing rows: %v", closeErr)
		}
	}()

	var adminIDs []int64
	for rows.Next() {
		var telegramID int64
		if scanErr := rows.Scan(&telegramID); scanErr != nil {
			return nil, scanErr
		}
		adminIDs = append(adminIDs, telegramID)
	}

	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}

	return adminIDs, nil
}

// FormatRequestMessage renders the "a new request arrived" text shown to admins.
func FormatRequestMessage(request *models.UpdateRequest, user *models.User) string {
	userName := ResolveUserName(user)
	date := request.CreatedAt.Format("15:04 02.01.2006")

	switch request.RequestType {
	case "create":
		return RenderMessage("new_request_create", map[string]interface{}{
			"UserName": userName,
			"Slug":     fieldStr(request.ChangedFields, "slug"),
			"SiteName": fieldStr(request.ChangedFields, "name"),
			"URL":      fieldStr(request.ChangedFields, "url"),
			"Date":     date,
		})
	case "update":
		siteName, siteSlug := "", ""
		if request.Site != nil {
			siteName = request.Site.Name
			siteSlug = request.Site.Slug
		}
		return RenderMessage("new_request_update", map[string]interface{}{
			"UserName": userName,
			"SiteName": siteName,
			"SiteSlug": siteSlug,
			"Changes":  BuildChanges(request.ChangedFields),
			"Date":     date,
		})
	}
	return ""
}

// SendMessage delivers a MarkdownV2 message, logging rather than returning failures.
// It is the fire-and-forget path used by the notification helpers.
func SendMessage(botToken string, chatID int64, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	if _, err := NewClient(botToken).SendMessage(ctx, SendMessageParams{
		ChatID: chatID,
		Text:   text,
	}); err != nil {
		log.Printf("Error sending Telegram message to %d: %v", chatID, err)
		return
	}

	logDebugf("Successfully sent Telegram notification to user %d", chatID)
}

func NotifyUserOfApprovedRequest(request *models.UpdateRequest, user *models.User) {
	botToken := BotToken()
	if botToken == "" || user == nil || user.TelegramID == 0 {
		return
	}

	var message string
	switch request.RequestType {
	case "create":
		siteName := fieldStr(request.ChangedFields, "name")
		if siteName == "" {
			siteName = "Your site"
		}
		message = RenderMessage("approved_create", map[string]interface{}{
			"SiteName": siteName,
		})
	case "update":
		message = RenderMessage("approved_update", map[string]interface{}{
			"Changes": BuildChanges(request.ChangedFields),
		})
	}

	SendMessage(botToken, user.TelegramID, message)
}

func NotifyUserOfDeclinedRequest(request *models.UpdateRequest, user *models.User) {
	botToken := BotToken()
	if botToken == "" || user == nil || user.TelegramID == 0 {
		return
	}

	var message string
	switch request.RequestType {
	case "create":
		siteName := fieldStr(request.ChangedFields, "name")
		if siteName == "" {
			siteName = "your site"
		}
		message = RenderMessage("declined_create", map[string]interface{}{
			"SiteName": siteName,
		})
	case "update":
		siteInfo := "your site"
		if request.Site != nil {
			siteInfo = request.Site.Name
		}
		message = RenderMessage("declined_update", map[string]interface{}{
			"SiteName": siteInfo,
		})
	}

	SendMessage(botToken, user.TelegramID, message)
}

func NotifyAdminsOfAction(db *sql.DB, action string, request *models.UpdateRequest, performedBy *models.User) {
	botToken := BotToken()
	if botToken == "" {
		return
	}

	admins, err := AdminTelegramIDs(db)
	if err != nil {
		log.Printf("Error fetching admin Telegram IDs: %v", err)
		return
	}

	message := formatAdminActionMessage(action, request, performedBy)

	// The group chat carries the poll, so a decision taken in the dashboard has to be
	// echoed there too — otherwise the closed poll is the only trace of what happened.
	if chatID := AdminChatID(); chatID != 0 {
		go SendMessage(botToken, chatID, message)
	}

	for _, adminID := range admins {
		if performedBy != nil && adminID == performedBy.TelegramID {
			continue
		}
		go SendMessage(botToken, adminID, message)
	}
}

func formatAdminActionMessage(action string, request *models.UpdateRequest, performedBy *models.User) string {
	adminName := ResolveUserName(performedBy)

	userName := "Unknown User"
	if request.User != nil {
		userName = ResolveUserName(request.User)
	}

	tmplName := fmt.Sprintf("admin_%s_%s", action, request.RequestType)
	data := map[string]interface{}{
		"AdminName": adminName,
		"UserName":  userName,
		"SiteName":  RequestSiteName(request),
	}

	if request.RequestType == "update" && action == "approved" {
		data["Changes"] = BuildChanges(request.ChangedFields)
	}

	return RenderMessage(tmplName, data)
}
