# CLAUDE.md

## Build & Test

```bash
make build       # Build wtmcp, wtmcpctl, and all plugins
make test        # go test -v -race ./...
make lint        # golangci-lint run ./...
make fmt         # gofmt -l -w .
make tidy        # go mod tidy

# Single Go test
go test -v -run TestName ./internal/plugin/

# Python plugin tests
pytest plugins/ -v
pytest plugins/jira/tests/ -k test_name

# All checks
pre-commit run --all-files
```

Go toolchain is pinned — see `go.mod` and `Makefile` for the current version.

## Linting

- **Go**: golangci-lint v2 (see `.golangci.yml`)
- **Python**: ruff (line-length 120, py39), ty for type checking
- Python plugin handlers must be executable (`chmod +x handler.py`)

## Workdir

Default: `~/.config/wtmcp`. Plugins discovered from `workdir/plugins/`, credentials from `env.d/*.env`.
