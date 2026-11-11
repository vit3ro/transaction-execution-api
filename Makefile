BINARY := bin/api
COVERAGE_FILE := coverage.out

TEST_DATABASE_URL ?= postgres://transactions:transactions@localhost:5432/transactions?sslmode=disable
API_BASE_URL ?= http://localhost:8080

ifneq (,$(wildcard .env))
include .env
export
endif

.PHONY: run run-mock build test test-cover test-integration e2e lint fmt up down logs

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

e2e: up
	API_BASE_URL="$(API_BASE_URL)" go test -tags=e2e -count=1 -v ./test/e2e/...

lint:
	golangci-lint run

fmt:
	gofmt -s -w .

up:
	docker compose up -d --build --wait

down:
	docker compose down --volumes --remove-orphans

logs:
	docker compose logs -f
