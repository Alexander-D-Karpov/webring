package telegram

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"

	"webring/internal/models"
)

const requestTimeout = 10 * time.Second

type Message struct {
	ChatID    int64  `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

type Response struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

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

func resolveUserName(user *models.User) string {
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

func NotifyAdminsOfNewRequest(db *sql.DB, request *models.UpdateRequest, user *models.User) {
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		if isDebugMode() {
			log.Printf("TELEGRAM_BOT_TOKEN not set, skipping admin notification")
		}
		return
	}

	admins, err := getAdminTelegramIDs(db)
	if err != nil {
		log.Printf("Error fetching admin Telegram IDs: %v", err)
		return
	}

	if len(admins) == 0 {
		if isDebugMode() {
			log.Printf("No admins with Telegram IDs found")
		}
		return
	}

	message := formatRequestMessage(request, user)

	for _, adminID := range admins {
		go SendMessage(botToken, adminID, message)
	}
}

func getAdminTelegramIDs(db *sql.DB) ([]int64, error) {
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

func formatRequestMessage(request *models.UpdateRequest, user *models.User) string {
	userName := resolveUserName(user)
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

func SendMessage(botToken string, chatID int64, text string) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)

	msg := Message{
		ChatID:    chatID,
		Text:      text,
		ParseMode: "MarkdownV2",
	}

	jsonData, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error marshaling Telegram message: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("Error creating Telegram request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error sending Telegram message: %v", err)
		return
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("Error closing response body: %v", closeErr)
		}
	}()

	var telegramResp Response
	if decodeErr := json.NewDecoder(resp.Body).Decode(&telegramResp); decodeErr != nil {
		log.Printf("Error decoding Telegram response: %v", decodeErr)
		return
	}

	if !telegramResp.OK {
		log.Printf("Telegram API error: %s", telegramResp.Description)
		return
	}

	if isDebugMode() {
		log.Printf("Successfully sent Telegram notification to user %d", chatID)
	}
}

func NotifyUserOfApprovedRequest(request *models.UpdateRequest, user *models.User) {
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" || user.TelegramID == 0 {
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
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" || user.TelegramID == 0 {
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
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		return
	}

	admins, err := getAdminTelegramIDs(db)
	if err != nil {
		log.Printf("Error fetching admin Telegram IDs: %v", err)
		return
	}

	if len(admins) == 0 {
		return
	}

	message := formatAdminActionMessage(action, request, performedBy)

	for _, adminID := range admins {
		if adminID == performedBy.TelegramID {
			continue
		}
		go SendMessage(botToken, adminID, message)
	}
}

func formatAdminActionMessage(action string, request *models.UpdateRequest, performedBy *models.User) string {
	adminName := resolveUserName(performedBy)

	userName := "Unknown User"
	if request.User != nil {
		userName = resolveUserName(request.User)
	}

	siteName := "Unknown Site"
	if request.RequestType == "create" {
		if name := fieldStr(request.ChangedFields, "name"); name != "" {
			siteName = name
		}
	} else if request.Site != nil {
		siteName = request.Site.Name
	}

	tmplName := fmt.Sprintf("admin_%s_%s", action, request.RequestType)
	data := map[string]interface{}{
		"AdminName": adminName,
		"UserName":  userName,
		"SiteName":  siteName,
	}

	if request.RequestType == "update" && action == "approved" {
		data["Changes"] = BuildChanges(request.ChangedFields)
	}

	return RenderMessage(tmplName, data)
}
