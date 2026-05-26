.PHONY: all build build-sandbox plugins test test-sandbox lint fmt vet tidy clean help

# Go toolchain version — must match CI (prevents go.mod bumps from newer local Go)
GO_TOOLCHAIN_VERSION := 1.25.5

VERSION ?= $(shell cat VERSION 2>/dev/null || echo "dev")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -s -w -X main.Version=$(VERSION) -X main.BuildDate=$(BUILD_DATE)
GOFLAGS ?= -trimpath -buildmode=pie
export PKG_CONFIG_PATH := /usr/local/lib/pkgconfig:$(PKG_CONFIG_PATH)

# Default target
all: build

# Build everything
build: wtmcp wtmcpctl plugins

# Build wtmcp binary
wtmcp: $(shell find cmd/wtmcp -name '*.go') $(shell find internal -name '*.go')
	@echo "Building wtmcp..."
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o wtmcp ./cmd/wtmcp

# Build wtmcpctl binary
wtmcpctl: $(shell find cmd/wtmcpctl -name '*.go') $(shell find internal -name '*.go')
	@echo "Building wtmcpctl..."
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o wtmcpctl ./cmd/wtmcpctl

# Build with sandbox support (requires libarapuca via pkg-config)
build-sandbox: wtmcpctl plugins
	@echo "Building wtmcp (sandbox)..."
	go build $(GOFLAGS) -tags sandbox -ldflags "$(LDFLAGS)" -o wtmcp ./cmd/wtmcp

# Build all plugins that have a Makefile
plugins:
	@for dir in plugins/*/; do \
		if [ -f "$${dir}Makefile" ]; then \
			echo "  Building plugin: $${dir}"; \
			$(MAKE) -C $${dir} build || exit 1; \
		fi; \
	done

# Run tests
test:
	@echo "Running tests..."
	go test -v -race ./...

# Run tests with sandbox tag (requires libarapuca)
test-sandbox:
	@echo "Running tests (sandbox)..."
	go test -v -race -tags sandbox ./...

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

# Tidy modules (pinned to CI toolchain)
tidy:
	GOTOOLCHAIN=go$(GO_TOOLCHAIN_VERSION) go mod tidy

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
	rm -f wtmcp wtmcpctl coverage.out
	rm -f plugins/*/handler
	@for dir in plugins/*/; do \
		if [ -f "$${dir}Makefile" ]; then \
			$(MAKE) -C $${dir} clean; \
		fi; \
	done

# Show help
help:
	@echo "wtmcp Makefile"
	@echo ""
	@echo "Available targets:"
	@echo "  all            - Build everything (default)"
	@echo "  build          - Build all binaries and plugins"
	@echo "  build-sandbox  - Build with sandbox support (requires libarapuca)"
	@echo "  wtmcp          - Build wtmcp binary"
	@echo "  wtmcpctl       - Build wtmcpctl binary"
	@echo "  plugins        - Build all plugins with Makefiles"
	@echo "  test           - Run tests"
	@echo "  test-sandbox   - Run tests with sandbox tag (requires libarapuca)"
	@echo "  test-cover     - Run tests with coverage report"
	@echo "  lint           - Run golangci-lint"
	@echo "  fmt            - Format code with gofmt"
	@echo "  vet            - Run go vet"
	@echo "  tidy           - Run go mod tidy (pinned toolchain)"
	@echo "  hooks          - Install pre-commit hooks"
	@echo "  pre-commit     - Run pre-commit on all files"
	@echo "  clean          - Remove build artifacts"
	@echo "  help           - Show this help"
