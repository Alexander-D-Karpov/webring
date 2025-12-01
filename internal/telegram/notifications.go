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
	"strings"
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

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
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

		template := getEnvOrDefault(
			"TELEGRAM_MESSAGE_SITE_CREATED",
			"*Request Approved*\n\n"+
				"Your site submission has been approved\\!\n\n"+
				"*Site:* %s\n\nYour site is now part of the webring\\.",
		)

		if strings.Contains(template, "%s") {
			message = fmt.Sprintf(template, siteNameEsc)
		} else {
			message = template
		}

	case "update":
		template := getEnvOrDefault(
			"TELEGRAM_MESSAGE_SITE_UPDATED",
			"*Update Approved*\n\nYour site update request has been approved and the changes have been applied\\.",
		)
		message = template

		if len(request.ChangedFields) > 0 {
			changesTemplate := getEnvOrDefault(
				"TELEGRAM_MESSAGE_CHANGES_LIST",
				"\n\n*Applied changes:*\n",
			)
			message += changesTemplate

			for field, value := range request.ChangedFields {
				fieldEsc := escapeMarkdownV2(field)
				valueStr := fmt.Sprintf("%v", value)
				valueEsc := escapeMarkdownV2(valueStr)

				itemTemplate := getEnvOrDefault(
					"TELEGRAM_MESSAGE_CHANGE_ITEM",
					"• *%s:* %s\n",
				)

				if strings.Count(itemTemplate, "%s") >= 2 {
					message += fmt.Sprintf(itemTemplate, fieldEsc, valueEsc)
				} else {
					message += itemTemplate
				}
			}
		}
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
		siteName := "your site"
		if name, ok := request.ChangedFields["name"].(string); ok {
			siteName = name
		}
		siteNameEsc := escapeMarkdownV2(siteName)

		template := getEnvOrDefault(
			"TELEGRAM_MESSAGE_REQUEST_DECLINED_CREATE",
			"*Request Declined*\n\n"+
				"Your site submission request for *%s* has been declined by an administrator\\.\n\n"+
				"If you have questions, please contact the webring administrator\\.",
		)

		if strings.Contains(template, "%s") {
			message = fmt.Sprintf(template, siteNameEsc)
		} else {
			message = template
		}

	case "update":
		siteInfo := "your site"
		if request.Site != nil {
			siteInfo = request.Site.Name
		}
		siteInfoEsc := escapeMarkdownV2(siteInfo)

		template := getEnvOrDefault(
			"TELEGRAM_MESSAGE_REQUEST_DECLINED_UPDATE",
			"*Update Request Declined*\n\n"+
				"Your update request for *%s* has been declined by an administrator\\.\n\n"+
				"If you have questions, please contact the webring administrator\\.",
		)

		if strings.Contains(template, "%s") {
			message = fmt.Sprintf(template, siteInfoEsc)
		} else {
			message = template
		}
	}

	SendMessage(botToken, user.TelegramID, message)
}

func NotifyAdminsOfAction(db *sql.DB, action string, request *models.UpdateRequest, performedBy *models.User) {
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

	message := formatAdminActionMessage(action, request, performedBy)

	for _, adminID := range admins {
		if adminID == performedBy.TelegramID {
			continue
		}
		go SendMessage(botToken, adminID, message)
	}
}

func formatAdminActionMessage(action string, request *models.UpdateRequest, performedBy *models.User) string {
	var message string

	adminName := "Admin"
	if performedBy.FirstName != nil && *performedBy.FirstName != "" {
		adminName = *performedBy.FirstName
		if performedBy.LastName != nil && *performedBy.LastName != "" {
			adminName += " " + *performedBy.LastName
		}
	} else if performedBy.TelegramUsername != nil && *performedBy.TelegramUsername != "" {
		adminName = "@" + *performedBy.TelegramUsername
	}
	adminNameEsc := escapeMarkdownV2(adminName)

	userName := "Unknown User"
	if request.User != nil {
		if request.User.FirstName != nil && *request.User.FirstName != "" {
			userName = *request.User.FirstName
			if request.User.LastName != nil && *request.User.LastName != "" {
				userName += " " + *request.User.LastName
			}
		} else if request.User.TelegramUsername != nil && *request.User.TelegramUsername != "" {
			userName = "@" + *request.User.TelegramUsername
		}
	}
	userNameEsc := escapeMarkdownV2(userName)

	switch action {
	case "approved":
		switch request.RequestType {
		case "create":
			siteName := "Unknown Site"
			if name, ok := request.ChangedFields["name"].(string); ok {
				siteName = name
			}
			siteNameEsc := escapeMarkdownV2(siteName)

			template := getEnvOrDefault(
				"TELEGRAM_MESSAGE_ADMIN_APPROVED_CREATE",
				"*Request Approved*\n\n*Admin:* %s\n*Action:* Approved site creation\n*User:* %s\n*Site:* %s",
			)

			if strings.Count(template, "%s") >= 3 {
				message = fmt.Sprintf(template, adminNameEsc, userNameEsc, siteNameEsc)
			} else {
				message = template
			}

		case "update":
			siteName := "Unknown Site"
			if request.Site != nil {
				siteName = request.Site.Name
			}
			siteNameEsc := escapeMarkdownV2(siteName)

			template := getEnvOrDefault(
				"TELEGRAM_MESSAGE_ADMIN_APPROVED_UPDATE",
				"*Update Approved*\n\n*Admin:* %s\n*Action:* Approved site update\n*User:* %s\n*Site:* %s",
			)

			if strings.Count(template, "%s") >= 3 {
				message = fmt.Sprintf(template, adminNameEsc, userNameEsc, siteNameEsc)
			} else {
				message = template
			}

			if len(request.ChangedFields) > 0 {
				changesTemplate := getEnvOrDefault(
					"TELEGRAM_MESSAGE_ADMIN_CHANGES_LIST",
					"\n\n*Changes:*\n",
				)
				message += changesTemplate

				for field, value := range request.ChangedFields {
					fieldEsc := escapeMarkdownV2(field)
					valueStr := fmt.Sprintf("%v", value)
					valueEsc := escapeMarkdownV2(valueStr)

					itemTemplate := getEnvOrDefault(
						"TELEGRAM_MESSAGE_ADMIN_CHANGE_ITEM",
						"• *%s:* %s\n",
					)

					if strings.Count(itemTemplate, "%s") >= 2 {
						message += fmt.Sprintf(itemTemplate, fieldEsc, valueEsc)
					} else {
						message += itemTemplate
					}
				}
			}
		}

	case "declined":
		switch request.RequestType {
		case "create":
			siteName := "Unknown Site"
			if name, ok := request.ChangedFields["name"].(string); ok {
				siteName = name
			}
			siteNameEsc := escapeMarkdownV2(siteName)

			template := getEnvOrDefault(
				"TELEGRAM_MESSAGE_ADMIN_DECLINED_CREATE",
				"*Request Declined*\n\n*Admin:* %s\n*Action:* Declined site creation\n*User:* %s\n*Site:* %s",
			)

			if strings.Count(template, "%s") >= 3 {
				message = fmt.Sprintf(template, adminNameEsc, userNameEsc, siteNameEsc)
			} else {
				message = template
			}

		case "update":
			siteName := "Unknown Site"
			if request.Site != nil {
				siteName = request.Site.Name
			}
			siteNameEsc := escapeMarkdownV2(siteName)

			template := getEnvOrDefault(
				"TELEGRAM_MESSAGE_ADMIN_DECLINED_UPDATE",
				"*Update Declined*\n\n*Admin:* %s\n*Action:* Declined site update\n*User:* %s\n*Site:* %s",
			)

			if strings.Count(template, "%s") >= 3 {
				message = fmt.Sprintf(template, adminNameEsc, userNameEsc, siteNameEsc)
			} else {
				message = template
			}
		}
	}

	return message
}
