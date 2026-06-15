.DEFAULT_GOAL := check

.PHONY: build test lint check tidy

GO ?= go
PKGS ?= ./...
GOLANGCI_LINT ?= golangci-lint
BINARY ?= lanpanel

build:
	$(GO) build -o $(BINARY) ./cmd/lanpanel

test:
	$(GO) test $(PKGS)

lint:
	$(GOLANGCI_LINT) run $(PKGS)

tidy:
	$(GO) mod tidy

check: build test