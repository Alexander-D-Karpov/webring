# Webring Relay Service

This project is a webring relay service built with Go. It manages a list of websites, checks their uptime, and provides a dashboard for administration.

## Features

- Dashboard for managing websites in the webring
- Automatic uptime checking of websites (with proxy support)
- API endpoints for navigating the webring
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
