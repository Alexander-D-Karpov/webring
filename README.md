# Webring Relay Service

This project is a webring relay service built with Go. It manages a list of websites, checks their uptime, and provides a dashboard for administration.

## Features

- Dashboard for managing websites in the webring
- Automatic uptime checking of websites
- API endpoints for navigating the webring
- Basic authentication for the dashboard

## Prerequisites

- Go 1.16 or later
- PostgreSQL database

## Installation
```
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
go mod tidy
make migrate-up
```

## Usage

- Access the dashboard at `http://localhost:8080/dashboard` (use the credentials set in your `.env` file)
- API endpoints:
  - Next site: `GET /{id}/next/`
  - Previous site: `GET /{id}/prev/`
  - Random site: `GET /{id}/random/`
  - Full data for a site: `GET /{id}/data`
- Redirect endpoints:
    - Next site: `GET /{id}/next`
    - Previous site: `GET /{id}/prev`
    - Random site: `GET /{id}/random`
