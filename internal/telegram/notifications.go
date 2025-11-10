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

var markdownV2Escape = regexp.MustCompile(`([_*\[\]()~` + "`" + `>#+\-=|{}.!\\])`)

func escapeMarkdownV2(text string) string {
	return markdownV2Escape.ReplaceAllString(text, `\$1`)
}

func isDebugMode() bool {
	if debugStr := os.Getenv("TELEGRAM_DEBUG"); debugStr != "" {
		if debug, err := strconv.ParseBool(debugStr); err == nil {
			return debug
		}
	}
	return false
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
	rows, err := db.QueryContext(
		context.Background(), `
		SELECT telegram_id 
		FROM users 
		WHERE is_admin = true AND telegram_id IS NOT NULL
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
	var message string

	userName := "Unknown User"
	if user.FirstName != nil && *user.FirstName != "" {
		userName = *user.FirstName
		if user.LastName != nil && *user.LastName != "" {
			userName += " " + *user.LastName
		}
	} else if user.TelegramUsername != nil && *user.TelegramUsername != "" {
		userName = "@" + *user.TelegramUsername
	}
	userName = escapeMarkdownV2(userName)

	switch request.RequestType {
	case "create":
		message = "*New Site Submission Request*\n\n"
		message += fmt.Sprintf("*User:* %s\n", userName)

		if slug, ok := request.ChangedFields["slug"].(string); ok {
			message += fmt.Sprintf("*Slug:* `%s`\n", escapeMarkdownV2(slug))
		}
		if name, ok := request.ChangedFields["name"].(string); ok {
			message += fmt.Sprintf("*Site Name:* %s\n", escapeMarkdownV2(name))
		}
		if url, ok := request.ChangedFields["url"].(string); ok {
			message += fmt.Sprintf("*URL:* %s\n", escapeMarkdownV2(url))
		}

	case "update":
		message = "*Site Update Request*\n\n"
		message += fmt.Sprintf("*User:* %s\n", userName)

		if request.Site != nil {
			siteName := escapeMarkdownV2(request.Site.Name)
			siteSlug := escapeMarkdownV2(request.Site.Slug)
			message += fmt.Sprintf("*Site:* %s \\(`%s`\\)\n", siteName, siteSlug)
		}

		message += "*Changes:*\n"
		for field, value := range request.ChangedFields {
			fieldEsc := escapeMarkdownV2(field)
			valueStr := fmt.Sprintf("%v", value)
			valueEsc := escapeMarkdownV2(valueStr)
			message += fmt.Sprintf("  • *%s:* %s\n", fieldEsc, valueEsc)
		}
	}

	dateStr := request.CreatedAt.Format("15:04 02\\.01\\.2006")
	message += fmt.Sprintf("\n*Submitted:* %s", dateStr)

	return message
}

func SendMessage(botToken string, chatID int64, text string) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)

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

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
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
		siteName := "Your site"
		if name, ok := request.ChangedFields["name"].(string); ok {
			siteName = name
		}
		siteNameEsc := escapeMarkdownV2(siteName)
		message = fmt.Sprintf("*Request Approved*\n\nYour site submission has been approved\\!\n\n"+
			"*Site:* %s\n\nYour site is now part of the webring\\.", siteNameEsc)
	case "update":
		message = "*Update Approved*\n\nYour site update request has been approved and the changes have been applied\\."
		if len(request.ChangedFields) > 0 {
			message += "\n\n*Applied changes:*\n"
			for field, value := range request.ChangedFields {
				fieldEsc := escapeMarkdownV2(field)
				valueStr := fmt.Sprintf("%v", value)
				valueEsc := escapeMarkdownV2(valueStr)
				message += fmt.Sprintf("• *%s:* %s\n", fieldEsc, valueEsc)
			}
		}
	}

	SendMessage(botToken, user.TelegramID, message)
}
