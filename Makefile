.PHONY: build test test-race clean install lint fmt tidy run tui help dev-setup release-snapshot

# Binary name
BINARY=caam

# Build variables
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS=-ldflags "-s -w \
	-X github.com/Dicklesworthstone/coding_agent_account_manager/internal/version.Version=$(VERSION) \
	-X github.com/Dicklesworthstone/coding_agent_account_manager/internal/version.Commit=$(COMMIT) \
	-X github.com/Dicklesworthstone/coding_agent_account_manager/internal/version.Date=$(DATE)"

# Default target
all: build

# Build the binary
build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/caam

# Run tests
test:
	go test -v ./...

# Run tests with race detector
test-race:
	go test -race -v ./...

# Run linter
lint:
	golangci-lint run ./...

# Format code
fmt:
	go fmt ./...
	goimports -w .

# Tidy dependencies
tidy:
	go mod tidy

# Install to GOPATH/bin
install:
	go install $(LDFLAGS) ./cmd/caam

# Clean build artifacts
clean:
	rm -f $(BINARY)
	rm -rf dist/

# Run the application
run: build
	./$(BINARY)

# Run the TUI
tui: build
	./$(BINARY)

# Build for all platforms (requires goreleaser)
release-snapshot:
	goreleaser release --snapshot --clean

# Development setup
dev-setup:
	go install golang.org/x/tools/cmd/goimports@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install github.com/goreleaser/goreleaser@latest

# Show help
help:
	@echo "Available targets:"
	@echo "  build           - Build the binary"
	@echo "  test            - Run tests"
	@echo "  test-race       - Run tests with race detector"
	@echo "  lint            - Run linter"
	@echo "  fmt             - Format code"
	@echo "  tidy            - Tidy dependencies"
	@echo "  install         - Install to GOPATH/bin"
	@echo "  clean           - Clean build artifacts"
	@echo "  run             - Build and run"
	@echo "  tui             - Build and run TUI"
	@echo "  release-snapshot - Build for all platforms"
	@echo "  dev-setup       - Install development tools"
