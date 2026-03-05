export PATH := $(PATH):/usr/local/go/bin
BINARY_NAME := omo-core
BACKEND_DIR := backend
BIN_DIR := $(BACKEND_DIR)/bin

.PHONY: all build backfill test test-v test-race test-cover test-integration clean lint migrate fmt debug-chrome debug-chrome-headless install-hooks

all: test build

## Build the omo-core binary
build:
	@mkdir -p $(BIN_DIR)
	cd $(BACKEND_DIR) && go build -o bin/$(BINARY_NAME) ./cmd/omo-core

## Build the omo-backfill binary
backfill:
	@mkdir -p $(BIN_DIR)
	cd $(BACKEND_DIR) && go build -o bin/omo-backfill ./cmd/omo-backfill

## Run all tests
test:
	cd $(BACKEND_DIR) && go test ./...

## Run tests with verbose output
test-v:
	cd $(BACKEND_DIR) && go test -v ./...

## Run tests with race detector
test-race:
	cd $(BACKEND_DIR) && go test -race ./...

## Run integration tests (requires TimescaleDB)
## Creates opentrade_test DB and runs migrations if needed, then executes tests.
test-integration:
	@docker exec omo-timescaledb psql -U opentrade -d postgres -tc "SELECT 1 FROM pg_database WHERE datname = 'opentrade_test'" | grep -q 1 || \
		docker exec omo-timescaledb psql -U opentrade -d postgres -c "CREATE DATABASE opentrade_test OWNER opentrade;"
	@PGHOST=localhost PGPORT=5432 PGUSER=opentrade PGPASSWORD=$${TIMESCALEDB_PASSWORD:-changeme} PGDATABASE=opentrade_test ./scripts/migrate.sh migrations
	TEST_DATABASE_URL="postgres://opentrade:$${TIMESCALEDB_PASSWORD:-changeme}@localhost:5432/opentrade_test?sslmode=disable" \
		cd $(BACKEND_DIR) && go test -tags integration -race -count=1 ./...
## Run tests with coverage
test-cover:
	cd $(BACKEND_DIR) && go test -coverprofile=coverage.out ./...
	cd $(BACKEND_DIR) && go tool cover -html=coverage.out -o coverage.html

## Clean build artifacts
clean:
	rm -rf $(BIN_DIR)
	rm -f $(BACKEND_DIR)/coverage.out $(BACKEND_DIR)/coverage.html

## Run go vet
lint:
	cd $(BACKEND_DIR) && go vet ./...

## Run database migrations
migrate:
	PGHOST=$${PGHOST:-localhost} PGPORT=$${PGPORT:-5432} PGUSER=$${PGUSER:-opentrade} PGDATABASE=$${PGDATABASE:-opentrade} ./scripts/migrate.sh migrations

## Format code
fmt:
	cd $(BACKEND_DIR) && gofmt -w .

## Launch Chrome with remote debugging for DevTools MCP
debug-chrome:
	./scripts/debug-chrome.sh

## Launch Chrome headless with remote debugging
debug-chrome-headless:
	./scripts/debug-chrome.sh --headless

## Install git hooks (run once after cloning)
install-hooks:
	git config core.hooksPath .githooks
	@echo "Git hooks installed (.githooks/pre-commit)"
