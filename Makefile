.ONESHELL:

SHELL := /bin/bash
.SHELLFLAGS := -ec

.PHONY: default
default: check ;

export GOFLAGS=-mod=vendor

format:
	go run mvdan.cc/gofumpt -d -s -w {cmd,internal}

lint:
	go run github.com/golangci/golangci-lint/cmd/golangci-lint run

test:
	go run gotest.tools/gotestsum

check: lint test