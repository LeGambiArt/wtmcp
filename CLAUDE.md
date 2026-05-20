# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test

```bash
make build              # Build wtmcp, wtmcpctl, and all plugins
make wtmcp              # Build only the MCP server
make wtmcpctl           # Build only the CLI tool
make plugins            # Build Go plugins (those with Makefiles)

make test               # go test -v -race ./...
make test-cover         # Same with coverage report
make lint               # golangci-lint run ./...
make fmt                # gofmt -l -w .
make tidy               # go mod tidy (pinned to Go 1.25.0 toolchain)

# Single Go test
go test -v -run TestName ./internal/plugin/

# Python plugin tests
pytest plugins/ -v                          # All plugins
pytest plugins/jira/tests/test_handler.py   # Single file
pytest plugins/jira/tests/ -k test_name     # Single test

# Pre-commit (runs golangci-lint, ruff, ty, pytest, go vet, go test)
pre-commit run --all-files
```

**Go toolchain is pinned to 1.25.0** — `.envrc` sets `GOTOOLCHAIN=go1.25.0` and `make tidy` enforces it. This prevents accidental `go.mod` bumps from a newer local Go.

## Architecture

wtmcp is an MCP server (using `mcp-go`) with a language-agnostic plugin system. The core handles auth, HTTP proxying, caching, and output encoding. Plugins are child processes that communicate over JSON-lines on stdin/stdout and never touch HTTP or auth directly.

### Core (`internal/`)

- **`server/`** — MCP server setup, tool registration, progressive discovery (`tool_search`), control directory (file-based commands for reload/status), and stats endpoint
- **`plugin/`** — Plugin lifecycle: manifest parsing (`manifest.go`), process management (`process.go`), JSON-lines transport (`transport.go`), tool call dispatch (`dispatch.go`), and the manager that ties it together (`manager.go`)
- **`proxy/`** — HTTP proxy that auth-injects requests from plugins. Plugins send `http_request` messages; the proxy makes the real call with credentials and returns `http_response`
- **`auth/`** — Auth provider abstraction: bearer, basic, Kerberos/SPNEGO, OAuth2 with token refresh. Auth is resolved per-plugin from manifest `services.auth` config
- **`cache/`** — In-memory key-value cache with namespace isolation and TTL, exposed to plugins via `cache_get`/`cache_set` protocol messages
- **`config/`** — Workdir config loading, env var resolution (`${VAR}` syntax in YAML), and `.env`/`env.d/*.env` file support
- **`encoding/`** — TOON output encoding for token-efficient responses
- **`protocol/`** — Wire protocol message type definitions for plugin communication
- **`google/`** — Shared OAuth2 helper used by all Google plugins
- **`pluginctx/`** — Plugin-scoped context (service resolution, data directory)
- **`stats/`** — Tool call statistics tracking and persistence

### Two plugin languages

- **Python plugins** (jira, confluence, gitlab, snyk, testing-farm, github): `handler.py` executable + optional `tools_*.py` modules. Tests in `plugins/<name>/tests/`. Python plugins must have `handler.py` marked executable (pre-commit hook enforces this).
- **Go plugins** (google-calendar, google-drive, google-gmail, google-docs): Compiled binaries with `main.go` + `tools.go`. Built via per-plugin `Makefile`. Tests are `tools_test.go` colocated.

### Plugin manifest (`plugin.yaml`)

Each plugin has a `plugin.yaml` declaring name, version, execution mode (oneshot/persistent), handler path, services (auth, http), and tool definitions with params. Tools can be marked `visibility: primary` for progressive discovery.

### Plugin context files (`context.md`)

Each plugin has a `context.md` with domain-specific instructions that get loaded into model context when the plugin's tools are active.

## Linting & Style

- **Go**: golangci-lint v2 with `bodyclose`, `errcheck`, `errorlint`, `gocritic`, `gosec`, `govet`, `revive`, `staticcheck`, and others. See `.golangci.yml`.
- **Python**: ruff for linting + formatting (line-length 120, target py39). Type checking via `ty`.
- Pre-commit runs all checks including `go vet`, `go test`, `gofmt`, `pytest`, and handler executable check.

## Workdir layout (runtime)

The server runs with `--workdir` (default `~/.config/wtmcp`). Plugins are discovered from `workdir/plugins/`. Environment files go in `env.d/*.env` (see `env.d/*.env.example` for templates).
