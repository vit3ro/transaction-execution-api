BINARY := bin/api
COVERAGE_FILE := coverage.out

TEST_DATABASE_URL ?= postgres://transactions:transactions@localhost:5432/transactions?sslmode=disable

.PHONY: run run-mock build test test-cover test-integration lint fmt up down

run:
	go run ./cmd/api

run-mock:
	go run ./mock-provider

build:
	go build -o $(BINARY) ./cmd/api

test:
	go test -race ./...

test-cover:
	go test -race -coverprofile=$(COVERAGE_FILE) ./...
	go tool cover -func=$(COVERAGE_FILE)

test-integration:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -tags=integration -count=1 ./internal/adapters/postgres/...

lint:
	golangci-lint run

fmt:
	gofmt -s -w .

up:
	docker compose up -d --build

down:
	docker compose down
