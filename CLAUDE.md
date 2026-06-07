# CLAUDE.md

## Build & Test

```bash
make build       # Build wtmcp, wtmcpctl, and all plugins
make test        # go test -v -race ./... (requires libarapuca)
make lint        # golangci-lint run ./...
make fmt         # gofmt -l -w .
make tidy        # go mod tidy

# Single Go test (use make test for full suite — handles PKG_CONFIG_PATH)
go test -v -run TestName ./internal/plugin/

# Nosandbox stub tests (no libarapuca needed)
make test-nosandbox

# Python plugin tests
pytest plugins/ -v
pytest plugins/jira/tests/ -k test_name

# All checks
pre-commit run --all-files
```

Go toolchain is pinned — see `go.mod` and `Makefile` for the current version.

Sandbox (libarapuca) is built by default. The Makefile auto-detects
system libarapuca via pkg-config; if not found, it builds from the
`third_party/arapuca` submodule (requires Rust toolchain). Use
`make test`/`make vet`/`make lint` instead of bare `go` commands to
ensure `PKG_CONFIG_PATH` is set correctly.

## Linting

- **Go**: golangci-lint v2 (see `.golangci.yml`)
- **Python**: ruff (line-length 120, py39), ty for type checking
- Python plugin handlers must be executable (`chmod +x handler.py`)

## Workdir

Default: `~/.config/wtmcp`. Plugins discovered from `workdir/plugins/`, credentials from `env.d/*.env`.
