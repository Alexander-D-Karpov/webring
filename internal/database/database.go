package database

import (
	"database/sql"
	"os"

	_ "github.com/lib/pq" // PostgreSQL driver
)

func Connect() (*sql.DB, error) {
	connStr := os.Getenv("DB_CONNECTION_STRING")
	return sql.Open("postgres", connStr)
}
