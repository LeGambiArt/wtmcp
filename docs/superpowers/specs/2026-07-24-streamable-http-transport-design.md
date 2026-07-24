# Streamable HTTP Transport

**Issue:** #179
**Date:** 2026-07-24

## Problem

wtmcp only supports stdio transport. This limits it to local, co-process usage where an MCP client spawns the binary directly. There is no way to run wtmcp as a standalone service that remote MCP clients connect to over the network.

The primary use case is running wtmcp as a sidecar container in AI workflow pods (e.g. Alcove skiff pods), where agents connect over localhost to access the full plugin toolkit without per-tool bridge actions in the orchestrator.

## Decision Record

- **Transport:** Streamable HTTP only (MCP spec 2025-03-26). SSE is legacy and excluded from this scope.
- **Auth:** None in v1. Localhost binding provides sufficient isolation for the sidecar use case. Enterprise-Managed Authorization (#116) is the intended auth mechanism for future HTTP transport security.
- **Defaults:** `localhost:8080`. Binding to `0.0.0.0` requires an explicit `--host` flag or config.

## Design

### Config

New `ServerConfig` struct in `internal/config/`:

```go
type ServerConfig struct {
    Transport string `yaml:"transport"` // "stdio" (default) | "streamable-http"
    Host      string `yaml:"host"`      // default: "localhost"
    Port      int    `yaml:"port"`      // default: 8080
}
```

Added to `Config` as:

```go
type Config struct {
    // ... existing fields ...
    Server ServerConfig `yaml:"server"`
}
```

Config file usage:

```yaml
server:
  transport: streamable-http
  host: localhost
  port: 8080
```

Defaults are applied during config loading: transport=`stdio`, host=`localhost`, port=`8080`.

### Transport Package

New `internal/transport/transport.go` with a single entry point:

```go
func ListenAndServe(ctx context.Context, srv *mcpserver.MCPServer, cfg *config.ServerConfig) error
```

Branches on `cfg.Transport`:

- **`stdio`** — creates `mcpserver.NewStdioServer(srv)`, calls `Listen(ctx, os.Stdin, os.Stdout)`. Extracted from current `main.go` logic.
- **`streamable-http`** — creates `mcpserver.NewStreamableHTTPServer(srv)`, calls `Start(host:port)`. A goroutine watches `ctx.Done()` and calls `Shutdown()` with a 5-second drain timeout.

### CLI Wiring

Three new flags on the `serve` command only:

```
--transport string   Transport: stdio, streamable-http
--host string        Bind address (default: localhost)
--port int           Listen port (default: 8080)
```

Flag values override config file values. Merge happens in `run()` before calling `transport.ListenAndServe()`.

The **root command** (`wtmcp` with no subcommand) remains stdio-only and ignores transport config, preserving backward compatibility with MCP clients that exec the binary directly.

When using HTTP transport, the server logs the listen address to stderr:
`wtmcp listening on http://localhost:8080/mcp`

### Shutdown and Lifecycle

For HTTP transport, `ListenAndServe()`:

1. Starts the HTTP server in a goroutine
2. Blocks on `<-ctx.Done()`
3. Calls `httpServer.Shutdown(shutdownCtx)` with a 5-second timeout to drain in-flight requests
4. Returns

The existing shutdown sequence (plugin shutdown, control watcher, cache, stats, audit) is unchanged — it's already wired to the same context cancellation in `main.go`. No changes to the control plane (`ControlWatcher`).

### Testing

1. **Config tests** (`internal/config/`) — `ServerConfig` parsing: defaults applied correctly (stdio/localhost/8080), explicit values parsed, partial overrides work, invalid transport value rejected.

2. **Transport tests** (`internal/transport/`) — `ListenAndServe` with `streamable-http`: start server, HTTP request to `/mcp` returns valid MCP JSON-RPC response, context cancellation triggers clean shutdown. Stdio path continues to work.

3. **CLI flag override tests** — `--transport`, `--host`, `--port` flags override config values.

## Files Changed

| File | Change |
|------|--------|
| `internal/config/config.go` | Add `ServerConfig` struct and `Server` field to `Config`, apply defaults |
| `internal/config/config_test.go` | Tests for `ServerConfig` parsing and defaults |
| `internal/transport/transport.go` | New package: `ListenAndServe()` with stdio and streamable-http branches |
| `internal/transport/transport_test.go` | Transport integration tests |
| `cmd/wtmcp/main.go` | Add `--transport`, `--host`, `--port` flags to `serve`; replace inline stdio setup with `transport.ListenAndServe()` call |

## Out of Scope

- SSE transport (legacy, can be added later)
- Authentication (#116 covers enterprise auth)
- TLS termination (handled by sidecar proxy or service mesh in production)
- Config file `server:` section validation beyond transport value
