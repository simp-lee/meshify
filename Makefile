.DEFAULT_GOAL := check

.PHONY: build test lint check tidy

GO ?= go
PKGS ?= ./...
GOLANGCI_LINT ?= golangci-lint
BINARY ?= meshify

build:
	$(GO) build -o $(BINARY) ./cmd/meshify

test:
	$(GO) test $(PKGS)

lint:
	$(GOLANGCI_LINT) run $(PKGS)

tidy:
	$(GO) mod tidy

check: build test