package database

import (
	"database/sql"
	_ "github.com/lib/pq"
	"os"
)

func Connect() (*sql.DB, error) {
	connStr := os.Getenv("DB_CONNECTION_STRING")
	return sql.Open("postgres", connStr)
}
