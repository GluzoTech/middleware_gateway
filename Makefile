.PHONY: fmt vet test test-race check build run tidy docker-up docker-down

BINARY  ?= bin/server
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

fmt:
	gofmt -l -w .

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

check: fmt vet test

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/server

run:
	go run ./cmd/server

tidy:
	go mod tidy

docker-up:
	docker compose up --build -d

docker-down:
	docker compose down
