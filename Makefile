.ONESHELL:

SHELL := /bin/bash
.SHELLFLAGS := -ec

.PHONY: default
default: check ;

export GOFLAGS=-mod=vendor

generate:
	@echo "Generating mock files..."
	@find . -name "mock_*.go" -delete
	@go generate ./...
	@echo "Mock files generated"

format:
	@echo "Applying format..."
	@go run mvdan.cc/gofumpt -d -s -w {cmd,internal}
	@echo "Format applied"

lint:
	@echo "Running linter over project..."
	@go run github.com/golangci/golangci-lint/cmd/golangci-lint run
	@echo "Linter finished"

test:
	@echo "Running tests..."
	@go test -race -coverprofile=coverage.txt -covermode=atomic ./...
	@echo "Tests finished"

check: lint test