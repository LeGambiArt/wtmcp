.PHONY: all build plugins test test-nosandbox lint fmt vet tidy clean help govulncheck

# Go toolchain version — derived from go.mod to stay in sync with dependency bumps
GO_TOOLCHAIN_VERSION := $(shell sed -n 's/^go //p' go.mod)

VERSION ?= $(shell cat VERSION 2>/dev/null || echo "dev")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -s -w -X main.Version=$(VERSION) -X main.BuildDate=$(BUILD_DATE)
GOFLAGS ?= -trimpath -buildmode=pie

# Arapuca: prefer system package (pkg-config), fall back to submodule build.
ARAPUCA_SYSTEM := $(strip $(shell \
    PKG_CONFIG_PATH="/usr/local/lib/pkgconfig:$$PKG_CONFIG_PATH" \
    pkg-config --atleast-version=0.2.0 arapuca 2>/dev/null && echo yes))
ARAPUCA_BUILD_DIR := $(CURDIR)/build/arapuca
ifneq ($(ARAPUCA_SYSTEM),yes)
  export PKG_CONFIG_PATH := $(ARAPUCA_BUILD_DIR)/lib/pkgconfig:$(PKG_CONFIG_PATH)
else
  export PKG_CONFIG_PATH := /usr/local/lib/pkgconfig:$(PKG_CONFIG_PATH)
endif

# Default target
all: build

# Build everything
build: wtmcp wtmcpctl plugins

# Build wtmcp binary
wtmcp: arapuca $(shell find cmd/wtmcp -name '*.go') $(shell find internal -name '*.go')
	@echo "Building wtmcp..."
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o wtmcp ./cmd/wtmcp

# Build wtmcpctl binary
wtmcpctl: arapuca $(shell find cmd/wtmcpctl -name '*.go') $(shell find internal -name '*.go')
	@echo "Building wtmcpctl..."
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o wtmcpctl ./cmd/wtmcpctl

# Build all plugins that have a Makefile
plugins:
	@for dir in plugins/*/; do \
		if [ -f "$${dir}Makefile" ]; then \
			echo "  Building plugin: $${dir}"; \
			$(MAKE) -C $${dir} build || exit 1; \
		fi; \
	done

# Build libarapuca from submodule when system package is not available.
# The submodule path uses a stamp file so Make skips rebuilds when
# the library is already installed. The system-package path is a
# .PHONY no-op (just an echo).
ARAPUCA_STAMP := $(ARAPUCA_BUILD_DIR)/.stamp

ifeq ($(ARAPUCA_SYSTEM),yes)
.PHONY: arapuca
arapuca:
	@echo "Using system libarapuca (pkg-config)"
else
arapuca: $(ARAPUCA_STAMP)

$(ARAPUCA_STAMP): third_party/arapuca/Cargo.toml $(wildcard third_party/arapuca/src/*.rs)
	@command -v cargo >/dev/null 2>&1 || \
	    { echo "ERROR: Rust toolchain required to build libarapuca. Install from https://rustup.rs/"; exit 1; }
	@test -f third_party/arapuca/Cargo.toml || \
	    git submodule update --init third_party/arapuca
	$(MAKE) -C third_party/arapuca package
	$(MAKE) -C third_party/arapuca install PREFIX=$(ARAPUCA_BUILD_DIR)
	@touch $@
endif

# Run tests
test: arapuca
	@echo "Running tests..."
	go test -v -race ./...

# Run nosandbox stub tests (no libarapuca needed)
test-nosandbox:
	@echo "Verifying nosandbox binary compiles..."
	go build -tags nosandbox -o /dev/null ./cmd/wtmcp
	@echo "Running nosandbox stub tests..."
	go test -v -race -tags nosandbox ./internal/sandbox/... ./internal/server/...

# Run tests with coverage
test-cover: arapuca
	@echo "Running tests with coverage..."
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

# Run govulncheck (handles PKG_CONFIG_PATH for cgo)
govulncheck: arapuca
	@echo "Running govulncheck..."
	govulncheck ./...

# Run linter
lint: arapuca
	@echo "Running linter..."
	golangci-lint run ./...

# Format code
fmt:
	@echo "Formatting code..."
	gofmt -l -w .

# Run go vet
vet: arapuca
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
	rm -rf build/
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
	@echo "  arapuca        - Build libarapuca (auto-detected: system or submodule)"
	@echo "  wtmcp          - Build wtmcp binary"
	@echo "  wtmcpctl       - Build wtmcpctl binary"
	@echo "  plugins        - Build all plugins with Makefiles"
	@echo "  test           - Run tests (sandbox built by default)"
	@echo "  test-nosandbox - Run nosandbox stub tests"
	@echo "  test-cover     - Run tests with coverage report"
	@echo "  lint           - Run golangci-lint"
	@echo "  fmt            - Format code with gofmt"
	@echo "  vet            - Run go vet"
	@echo "  tidy           - Run go mod tidy (pinned toolchain)"
	@echo "  hooks          - Install pre-commit hooks"
	@echo "  pre-commit     - Run pre-commit on all files"
	@echo "  clean          - Remove build artifacts"
	@echo "  help           - Show this help"
