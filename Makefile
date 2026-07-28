.PHONY: setup format lint test test-race validate sync export build serve audit db-migrate db-snapshot db-rebuild db-verify site-build ci

CLI := ./cmd/spotwufamily
BUILD_DIR := build

setup:
	go mod download

format:
	gofmt -w cmd internal

lint:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

validate:
	go run $(CLI) artists validate

sync:
	go run $(CLI) sync

export:
	go run $(CLI) export

build:
	go build -o $(BUILD_DIR)/spotwufamily $(CLI)

serve:
	hugo server --source site --bind 127.0.0.1

audit:
	go run $(CLI) audit

db-verify:
	go run $(CLI) db verify

db-migrate:
	go run $(CLI) db migrate

db-snapshot:
	go run $(CLI) db snapshot

db-rebuild:
	go run $(CLI) db rebuild

site-build:
	go run $(CLI) site build

ci: format validate db-verify export site-build test lint build
