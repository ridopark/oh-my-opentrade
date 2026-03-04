export PATH := $(PATH):/usr/local/go/bin
BINARY_NAME := omo-core
BACKEND_DIR := backend
BIN_DIR := $(BACKEND_DIR)/bin

.PHONY: all build test test-v clean lint migrate

all: test build

## Build the omo-core binary
build:
	@mkdir -p $(BIN_DIR)
	cd $(BACKEND_DIR) && go build -o bin/$(BINARY_NAME) ./cmd/omo-core

## Run all tests
test:
	cd $(BACKEND_DIR) && go test ./...

## Run tests with verbose output
test-v:
	cd $(BACKEND_DIR) && go test -v ./...

## Run tests with race detector
test-race:
	cd $(BACKEND_DIR) && go test -race ./...

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
