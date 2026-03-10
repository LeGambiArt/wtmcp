# wtmcp

MCP server with a language-agnostic plugin system. Plugins are simple
executables (Python, bash, or any language) that communicate with the
core over JSON-lines on stdin/stdout. The core handles auth, HTTP
proxying, caching, and output encoding so plugins stay minimal.

## Architecture

```
┌─────────────────────────────────────────────────┐
│  wtmcp (Go)                                     │
│                                                 │
│  MCP Server ─── Plugin Manager ─── HTTP Proxy   │
│  (mcp-go)       Discovery         Auth inject   │
│                 Lifecycle          TLS verify   │
│                 Dispatch           Rate limit   │
│                                                 │
│              Cache Store ─── Auth Providers     │
│              (memory/fs)     Bearer, Basic,     │
│                              Kerberos, OAuth2   │
└────────┬──────────────────────────┬─────────────┘
         │ stdio (MCP/JSON-RPC)     │ stdin/stdout (JSON-lines)
    ┌────┴────┐               ┌─────┴──────────┐
    │ AI      │               │ Plugins        │
    │ Client  │               │ Zero deps      │
    └─────────┘               │ No HTTP libs   │
                              │ No auth code   │
                              └────────────────┘
```

## Features

- **Plugin protocol**: JSON-lines over stdin/stdout, any language
- **Auth**: Bearer, Basic, Kerberos/SPNEGO, OAuth2 with token refresh,
  auto-detection from available credentials
- **HTTP proxy**: Auth injection, domain validation, TLS enforcement,
  binary response encoding, multipart upload support
- **Cache**: In-memory store with namespace isolation and TTL
- **Output**: TOON encoding for ~40% token savings (optional)
- **Plugin setup**: Manifest-declared wizard metadata for CLI tooling

## Building and Running

```bash
make build

# Run with a workdir (default: ~/.config/wtmcp)
./wtmcp --workdir ~/.config/wtmcp
```

The workdir layout:

```
~/.config/wtmcp/
  config.yaml           Core config (optional)
  .env                  Environment variables
  env.d/*.env           Additional env files
  plugins/
    jira/
      plugin.yaml       Plugin manifest
      handler.py        Plugin executable
```

## Writing Plugins

A plugin is a directory with a manifest (`plugin.yaml`) and a handler
executable. The core discovers plugins, starts handlers as child
processes, and routes tool calls over stdin/stdout using JSON-lines.

See [docs/plugin-guide.md](docs/plugin-guide.md) for the full guide
with examples in multiple languages.

### Minimal Example (bash)

A oneshot plugin that runs the handler once per tool call:

**plugin.yaml:**
```yaml
name: hello
version: "1.0.0"
description: "A greeting plugin"
execution: oneshot
handler: ./handler.sh
tools:
  - name: hello_world
    description: "Says hello to someone"
    params:
      name:
        type: string
        default: "World"
        description: "Who to greet"
enabled: true
```

**handler.sh:**
```bash
#!/bin/bash
read -r INPUT
ID=$(echo "$INPUT" | jq -r '.id')
NAME=$(echo "$INPUT" | jq -r '.params.name // "World"')

echo "{}" | jq -c --arg id "$ID" --arg name "$NAME" \
  '{id: $id, type: "tool_result", result: {message: ("Hello, " + $name + "!")}}'
```

### API Plugin Example (Python)

A persistent plugin that calls an API through the core's HTTP proxy.
The handler stays running and processes multiple tool calls. Auth
headers are injected automatically — the plugin never sees tokens.

**plugin.yaml:**
```yaml
name: myapi
version: "1.0.0"
description: "Example API plugin"
execution: persistent
handler: ./handler.py

services:
  auth:
    type: bearer
    token: "${MY_API_TOKEN}"
  http:
    base_url: "${MY_API_URL}"

tools:
  - name: myapi_get_status
    description: "Get API status"
    params: {}
  - name: myapi_search
    description: "Search the API"
    params:
      query:
        type: string
        required: true
enabled: true
```

**handler.py:**
```python
#!/usr/bin/env python3
import json, sys

def _send(msg):
    print(json.dumps(msg, separators=(",", ":")), flush=True)

def _recv():
    line = sys.stdin.readline()
    if not line:
        sys.exit(0)
    return json.loads(line.strip())

def http(method, path, query=None):
    msg = {"id": "1", "type": "http_request", "method": method, "path": path}
    if query:
        msg["query"] = query
    _send(msg)
    resp = _recv()
    return resp.get("status", 0), resp.get("body", {})

def get_status(_params):
    status, body = http("GET", "/status")
    return body

def search(params):
    status, body = http("GET", "/search", query={"q": params["query"]})
    return body

TOOLS = {"myapi_get_status": get_status, "myapi_search": search}

while True:
    msg = _recv()
    if msg.get("type") == "init":
        _send({"id": msg["id"], "type": "init_ok"})
    elif msg.get("type") == "shutdown":
        _send({"id": msg["id"], "type": "shutdown_ok"})
        break
    elif msg.get("type") == "tool_call":
        fn = TOOLS.get(msg.get("tool"))
        if fn:
            result = fn(msg.get("params", {}))
            _send({"id": msg["id"], "type": "tool_result", "result": result})
        else:
            _send({"id": msg["id"], "type": "tool_result",
                   "error": {"code": "unknown_tool", "message": msg.get("tool")}})
```

### Key Concepts

- **Oneshot** plugins are spawned per tool call. Simplest to write.
- **Persistent** plugins start once and handle many calls via a main loop.
- **HTTP proxy**: plugins send `http_request` messages, the core makes
  the call with auth and returns `http_response`. No HTTP library needed.
- **Cache**: plugins send `cache_get`/`cache_set` messages. The core
  manages storage and TTL.
- **Auth variants**: a single plugin can support multiple auth methods
  (e.g., Cloud Basic + Server Bearer + Kerberos) with auto-detection.

## Plugin Management

Plugins can be reloaded at runtime without restarting the server.

**From an AI assistant:**
```
plugin_reload(name="jira")
plugin_list()
```

**From a terminal** (control directory):
```bash
touch ~/.config/wtmcp/control/commands/reload-jira
touch ~/.config/wtmcp/control/commands/reload-all
```

Results appear in `~/.config/wtmcp/control/results/`. The server writes
its PID to `~/.config/wtmcp/control/mcp.pid` for process tracking.

MCP clients are automatically notified when tools or resources change.

## Jira Plugin

The included Jira plugin covers read, write, sprint, and export
operations:

| Category | Examples |
|----------|---------|
| Read | `jira_search`, `jira_get_myself`, `jira_get_transitions` |
| Write | `jira_create_issue`, `jira_add_comment`, `jira_assign_issue` |
| Sprint | `jira_list_available_sprints`, `jira_get_sprint_issues` |
| Export | `jira_export_sprint_data`, `jira_download_attachment` |

All write tools default to `dry_run=true`. Cloud-aware (ADF format,
accountId assignments). Auth variants: Cloud Basic, Server Bearer,
Server Kerberos.

## Testing

```bash
# Go core tests
go test ./...

# Python plugin tests
.venv/bin/pytest tests/ -v

# All pre-commit checks
pre-commit run --all-files
```

## Project Layout

```
cmd/                    Entry point
internal/
  auth/                 Auth providers (bearer, basic, kerberos, oauth2)
  cache/                Key-value cache with TTL
  config/               Env var resolution, YAML config
  encoding/             TOON output encoding
  plugin/               Manager, manifest, transport, dispatch
  protocol/             Wire protocol message types
  proxy/                HTTP proxy with auth injection
  server/               MCP server integration
plugins/
  jira/                 Jira plugin (Python, zero external deps)
tests/
  plugins/              Plugin unit tests
docs/
  plugin-guide.md       Plugin development guide
```

## License

This project is licensed under the GNU General Public License v3.0.
See [LICENSE](LICENSE) for the full text.
