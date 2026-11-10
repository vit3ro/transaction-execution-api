BINARY := bin/api
COVERAGE_FILE := coverage.out

.PHONY: run build test test-cover lint fmt up down

run:
	go run ./cmd/api

build:
	go build -o $(BINARY) ./cmd/api

test:
	go test -race ./...

test-cover:
	go test -race -coverprofile=$(COVERAGE_FILE) ./...
	go tool cover -func=$(COVERAGE_FILE)

lint:
	golangci-lint run

fmt:
	gofmt -s -w .

up:
	docker compose up -d --build

down:
	docker compose down
