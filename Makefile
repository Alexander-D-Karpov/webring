-include .env
export

MIGRATE := migrate -database "${DB_CONNECTION_STRING}" -path migrations

.PHONY: migrate-up migrate-down migrate-force migrate-goto migrate-version lint test build clean install-tools run dev

# Migration commands
migrate-up:
	@echo "Running migrations up..."
	@$(MIGRATE) up

migrate-down:
	@echo "Running migrations down..."
	@$(MIGRATE) down

migrate-force:
	@echo "Forcing migration version..."
	@$(MIGRATE) force $(version)

migrate-goto:
	@echo "Migrating to version $(version)..."
	@$(MIGRATE) goto $(version)

migrate-version:
	@echo "Checking migration version..."
	@$(MIGRATE) version

# Linting and testing
lint:
	@echo "Running linter..."
	@golangci-lint run

lint-fix:
	@echo "Running linter with auto-fix..."
	@golangci-lint run --fix

test:
	@echo "Running tests..."
	@go test -v ./...

test-coverage:
	@echo "Running tests with coverage..."
	@go test -v -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html

# Build commands
build:
	@echo "Building application..."
	@go build -v -o webring cmd/server/main.go

build-linux:
	@echo "Building for Linux..."
	@GOOS=linux GOARCH=amd64 go build -v -o webring-linux cmd/server/main.go

build-windows:
	@echo "Building for Windows..."
	@GOOS=windows GOARCH=amd64 go build -v -o webring.exe cmd/server/main.go

# Development commands
run:
	@echo "Running application..."
	@go run cmd/server/main.go

dev:
	@echo "Running in development mode..."
	@air

# Cleanup
clean:
	@echo "Cleaning build artifacts..."
	@rm -f webring webring-linux webring.exe
	@rm -f coverage.out coverage.html

# Tool installation
install-tools:
	@echo "Installing development tools..."
	@go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
	@go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	@go install github.com/air-verse/air@latest

# Database commands
db-reset:
	@echo "Resetting database..."
	@$(MIGRATE) down -all
	@$(MIGRATE) up

# Docker commands
docker-build:
	@echo "Building Docker image..."
	@docker build -t webring:latest .

docker-run:
	@echo "Running Docker container..."
	@docker run -p 8080:8080 --env-file .env webring:latest

# Go module management
mod-tidy:
	@echo "Tidying Go modules..."
	@go mod tidy

mod-download:
	@echo "Downloading Go modules..."
	@go mod download

mod-verify:
	@echo "Verifying Go modules..."
	@go mod verify

# Help
help:
	@echo "Available commands:"
	@echo "  migrate-up      - Run database migrations"
	@echo "  migrate-down    - Rollback database migrations"
	@echo "  migrate-force   - Force migration version (use: make migrate-force version=N)"
	@echo "  migrate-goto    - Go to specific migration (use: make migrate-goto version=N)"
	@echo "  migrate-version - Check current migration version"
	@echo "  lint           - Run golangci-lint"
	@echo "  lint-fix       - Run golangci-lint with auto-fix"
	@echo "  test           - Run tests"
	@echo "  test-coverage  - Run tests with coverage report"
	@echo "  build          - Build application"
	@echo "  build-linux    - Build for Linux"
	@echo "  build-windows  - Build for Windows"
	@echo "  run            - Run application"
	@echo "  dev            - Run in development mode with air"
	@echo "  clean          - Clean build artifacts"
	@echo "  install-tools  - Install development tools"
	@echo "  db-reset       - Reset database (down-all then up)"
	@echo "  docker-build   - Build Docker image"
	@echo "  docker-run     - Run Docker container"
	@echo "  mod-tidy       - Tidy Go modules"
	@echo "  mod-download   - Download Go modules"
	@echo "  mod-verify     - Verify Go modules"
	@echo "  help           - Show this help message"