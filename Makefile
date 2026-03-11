.PHONY: all build test lint fmt vet clean help

VERSION ?= $(shell cat VERSION 2>/dev/null || echo "dev")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -s -w -X main.Version=$(VERSION) -X main.BuildDate=$(BUILD_DATE)

# Default target
all: build

# Build the binary and plugin handlers
build:
	@echo "Building wtmcp..."
	go build -ldflags "$(LDFLAGS)" -o wtmcp ./cmd/wtmcp
	@echo "Building wtmcpctl..."
	go build -ldflags "$(LDFLAGS)" -o wtmcpctl ./cmd/wtmcpctl
	@echo "Building plugin handlers..."
	@for plugin in plugins/google-*/; do \
		if ls $$plugin*.go >/dev/null 2>&1; then \
			echo "  $$plugin"; \
			go build -o $${plugin}handler ./$${plugin}; \
		fi; \
	done

# Run tests
test:
	@echo "Running tests..."
	go test -v -race ./...

# Run tests with coverage
test-cover:
	@echo "Running tests with coverage..."
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

# Run linter
lint:
	@echo "Running linter..."
	golangci-lint run ./...

# Format code
fmt:
	@echo "Formatting code..."
	gofmt -l -w .

# Run go vet
vet:
	@echo "Running go vet..."
	go vet ./...

# Install pre-commit hooks
hooks:
	@echo "Installing pre-commit hooks..."
	pre-commit install

# Run pre-commit on all files
pre-commit:
	pre-commit run --all-files

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -f wtmcp wtmcpctl
	rm -f coverage.out
	rm -f plugins/google-*/handler

# Show help
help:
	@echo "wtmcp Makefile"
	@echo ""
	@echo "Available targets:"
	@echo "  all         - Build (default)"
	@echo "  build       - Build the binary"
	@echo "  test        - Run tests"
	@echo "  test-cover  - Run tests with coverage report"
	@echo "  lint        - Run golangci-lint"
	@echo "  fmt         - Format code with gofmt"
	@echo "  vet         - Run go vet"
	@echo "  hooks       - Install pre-commit hooks"
	@echo "  pre-commit  - Run pre-commit on all files"
	@echo "  clean       - Remove build artifacts"
	@echo "  help        - Show this help"
