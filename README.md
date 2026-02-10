# Webring Relay Service

This project is a webring relay service built with Go. It manages a list of websites, checks their uptime, and provides a dashboard for administration.

## Features

- Dashboard for managing websites in the webring
- Automatic uptime checking of websites (with proxy support)
- API endpoints for navigating the webring
- Telegram authentication and user management
- Site submission and update request workflow with admin approval
- Telegram notifications for status changes, submissions and approvals
- Customizable notification messages via template files
- Basic authentication for the dashboard

## Prerequisites

- Go 1.16 or later
- PostgreSQL database

## Installation

edit .env to set correct path to database
```
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
go mod tidy
cp .env.template .env
make migrate-up
```

## Local Run
```
go run cmd/server/main.go
```

or download prebuild version
```
wget https://github.com/Alexander-D-Karpov/webring/releases/latest/download/webring
chmod +x webring
./webring
```

## Customizing Notification Messages

Telegram notification templates live in the `messages/` directory (configurable via `MESSAGES_DIR` env var). 
Each file is plain text with Go template syntax and MarkdownV2 formatting. 

To customize a message, edit the corresponding `.txt` file. 

Available templates:

| File                        | Event                                       |
|-----------------------------|---------------------------------------------|
| `new_request_create.txt`    | Admin notification: new site submitted      |
| `new_request_update.txt`    | Admin notification: site update requested   |
| `approved_create.txt`       | User notification: site submission approved |
| `approved_update.txt`       | User notification: site update approved     |
| `declined_create.txt`       | User notification: site submission declined |
| `declined_update.txt`       | User notification: site update declined     |
| `admin_approved_create.txt` | Other admins: site creation approved        |
| `admin_approved_update.txt` | Other admins: site update approved          |
| `admin_declined_create.txt` | Other admins: site creation declined        |
| `admin_declined_update.txt` | Other admins: site update declined          |
| `site_online.txt`           | Owner notification: site back online        |
| `site_offline.txt`          | Owner notification: site went offline       |

## Usage

- Access the dashboard at `http://localhost:8080/dashboard` (use the credentials set in your `.env` file)
- API endpoints:
    - Next site: `GET /{slug}/next/data`
    - Previous site: `GET /{slug}/prev/data`
    - Random site: `GET /{slug}/random/data`
    - Full data for a site: `GET /{slug}/data`
- Redirect endpoints:
    - Visit site: `GET /{slug}`
    - Next site: `GET /{slug}/next`
    - Previous site: `GET /{slug}/prev`
    - Random site: `GET /{slug}/random`