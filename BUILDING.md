# Building wtmcp from source

## Requirements

- **Go** — see `go.mod` for the required version
- **Git**
- **Python 3.9+** — for Python-based plugins (Jira, Confluence, GitHub,
  GitLab, Jenkins, Snyk, Testing Farm)

Go auto-downloads the correct toolchain when `GOTOOLCHAIN=auto` is set.
Check with `go env GOTOOLCHAIN`; enable with `go env -w GOTOOLCHAIN=auto`.

## Build

```bash
git clone https://github.com/LeGambiArt/wtmcp.git
cd wtmcp
make build
```

Binaries are built in the repository root: `wtmcp` (MCP server) and
`wtmcpctl` (management CLI). Go plugins are compiled in their plugin
directories.

## Sandbox support

The default build includes OS-level plugin sandboxing via
[arapuca](https://github.com/sergio-correia/arapuca). The Makefile
auto-detects system libarapuca via pkg-config; if not found, it builds
from the `third_party/arapuca` submodule (requires a Rust toolchain).

To build without sandbox support:

```bash
make build-nosandbox
```

## Verify

```bash
./wtmcpctl check
```
