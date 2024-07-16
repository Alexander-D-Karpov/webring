include .env
export

MIGRATE := migrate -database "${DB_CONNECTION_STRING}" -path migrations

migrate-up:
	@echo "Running migrations up..."
	@$(MIGRATE) up

migrate-down:
	@echo "Running migrations down..."
	@$(MIGRATE) down

migrate-force:
	@echo "Forcing migration version..."
	@$(MIGRATE) force $(version)

migrate-version:
	@echo "Checking migration version..."
	@$(MIGRATE) version

.PHONY: migrate-up migrate-down migrate-force migrate-version