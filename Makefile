.PHONY: setup format lint test test-race validate sync export build serve audit db-verify ci

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
	@echo "Hugo site is planned for a later phase."

audit:
	go run $(CLI) audit

db-verify:
	go run $(CLI) db verify

ci: format validate test lint build
