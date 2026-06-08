# Plugin Development Guide

This guide covers everything you need to write a wtmcp plugin.
Plugins can be written in any language — the protocol is JSON-lines
over stdin/stdout.

## Plugin Structure

A plugin is a directory containing:

```
my-plugin/
  plugin.yaml       # Required: manifest declaring tools and services
  handler.py        # Required: executable that handles tool calls
  context.md        # Optional: instructions for AI assistants
```

## Manifest (plugin.yaml)

The manifest declares what the plugin does, what tools it exposes,
and what services it needs from the core.

```yaml
name: my-plugin
version: "1.0.0"
description: "What this plugin does"

# "oneshot" runs handler once per call, "persistent" keeps it running
execution: persistent
handler: ./handler.py

# Scopes env.d access — matches env.d/my-service.env
credential_group: my-service

# Env vars to pass to handler from credential group's env.d file
env:
  - MY_API_URL
  - MY_TOKEN

# Services the core provides to this plugin
services:
  auth:
    type: bearer
    token: "${MY_TOKEN}"
  http:
    base_url: "${MY_API_URL}"
  cache:
    enabled: true
    default_ttl: 300

# Config values passed to the handler (resolved from credential group)
config:
  api_url: "${MY_API_URL}"

# Tool declarations (registered as MCP tools)
tools:
  - name: my_search
    access: read
    description: "Search for items"
    params:
      query:
        type: string
        required: true
        description: "Search query"
      limit:
        type: integer
        default: 10
  - name: my_create
    access: write
    description: "Create a new item"
    params:
      name:
        type: string
        required: true

enabled: true
priority: 50
```

### Parameter Types

Tools declare parameters with JSON Schema types:

| Type | Description |
|------|-------------|
| `string` | Text value |
| `integer` | Whole number |
| `number` | Float/integer |
| `boolean` | true/false |
| `array` | List (use `items.type` for element type) |
| `object` | Nested JSON object |

### Tool Access Level

Each tool should declare an `access` field indicating whether it
modifies state:

| Value | MCP Annotation | Meaning |
|-------|---------------|---------|
| `read` | `readOnlyHint: true` | Tool only reads data, no side effects |
| `write` | `destructiveHint: true` | Tool creates, updates, or deletes data |

If `access` is omitted, it defaults to `write` (safe default).
MCP clients use these annotations to prompt users before calling
destructive tools.

```yaml
tools:
  - name: my_search
    access: read
    description: "Search for items"
    ...
  - name: my_delete
    access: write
    description: "Delete an item"
    ...
```

### Tool Visibility (Progressive Discovery)

Each tool can declare a `visibility` field to control how it is
presented to MCP clients:

| Value | Behavior | Default |
|-------|----------|---------|
| `primary` | Always loaded into model context | — |
| `deferred` | Marked with `defer_loading: true`; loaded on demand | Yes |

If `visibility` is omitted, the tool defaults to **deferred**.
Plugin authors only need to annotate the few most important tools
as `primary`.

```yaml
tools:
  - name: my_search
    access: read
    visibility: primary       # always in model context
    description: "Main search tool"
    ...
  - name: my_export
    access: read
    # no visibility → deferred by default
    description: "Export data to file"
    ...
```

**When to mark a tool as `primary`:**

- It is the main entry point for the plugin (e.g., a search tool)
- It is the most commonly used write operation (e.g., create, send)
- Users would expect it to be available without discovery

**Rule of thumb:** 2-6 primary tools per plugin. Everything else
is deferred. Deferred tools are discoverable via the built-in
`tool_search` meta-tool.

This feature requires `tools.discovery: progressive` in the server
config. In `full` mode (the default), all tools are loaded into
context regardless of visibility.

> **Security note:** Progressive discovery is a UX optimization, not
> a security boundary. Deferred tools are fully registered with the
> MCP server and callable by any client request. To restrict write
> tool access, use `tool_search_exclude_write: true` (hides write
> tools from search results) and `security.elicitation: true` (user
> confirmation before mutations).

### Auth Variants

For plugins that support multiple auth methods:

```yaml
services:
  auth:
    select: "${AUTH_TYPE:-auto}"
    variants:
      cloud:
        type: basic
        username: "${EMAIL}"
        password: "${TOKEN}"
      server:
        type: bearer
        token: "${TOKEN}"
      kerberos:
        type: kerberos/spnego
        spn: "HTTP@${HOST}"
```

When `select` is `auto`, the core picks the first variant with
valid credentials.

### SAML SSO Init

Kerberos-authenticated plugins that connect to services behind a
SAML SSO wall can use `saml_init` to proactively establish a SAML
session at plugin startup. Without this, the first request triggers
a reactive SSO flow (which only works for idempotent requests).

```yaml
services:
  auth:
    type: kerberos
    spnego_proactive: false
    saml_init: "/saml.redirect?relayState="
  http:
    base_url: "https://source.redhat.com"
    allowed_domains:
      - source.redhat.com
      - auth.redhat.com
```

The `saml_init` value is a URL (absolute or relative to `base_url`)
that triggers the SAML login flow. The IdP domain (e.g.,
`auth.redhat.com`) must be listed in `allowed_domains` for the
redirect chain to be followed.

The proxy follows the SAML flow using the plugin's Kerberos
credentials, collecting session cookies. Subsequent requests use
the established session.

### TLS Configuration

Plugins connecting to services with private CAs or requiring mutual
TLS (mTLS) can configure custom TLS settings:

```yaml
services:
  http:
    base_url: "https://internal.example.com"
    tls:
      ca_cert: "${MY_CA_CERT}"           # PEM CA certificate path
      client_cert: "${CLIENT_CERT}"       # PEM client cert for mTLS
      client_key: "${CLIENT_KEY}"         # PEM client key for mTLS
      skip_hostname_verify: false         # skip hostname check only
```

**Custom CA certificate:** Appended to the system CA pool (not
replacing it). Use this for services with self-signed or internal
CA certificates.

**Mutual TLS (mTLS):** For services requiring client certificate
authentication. Requirements:

- `client_cert` and `client_key` must both be set or both empty
- `client_key` file must have mode 0600 or 0400 (no group/other)
- Cannot be combined with `services.auth` — mTLS replaces
  header-based auth at the transport level
- HTTPS is always required when client certificates are configured

**Hostname verification skip:** For services with certificates
lacking proper SANs (e.g., auto-generated certs). The certificate
chain is still verified against the CA pool — only hostname
matching is skipped. A warning is logged at load time.

### Domain Allowlisting

The `base_url` hostname is automatically added to the allowed
domains list. You only need to declare `allowed_domains` for
additional hosts beyond the base URL:

```yaml
services:
  http:
    base_url: "https://api.example.com"
    allowed_domains:
      - secondary.example.com
```

Env var references in `allowed_domains` are resolved from the
credential group's env.d file. Full URLs are automatically
reduced to hostnames:

```yaml
allowed_domains:
  - ${OTHER_SERVICE_URL}
  # https://other.example.com:8443 → other.example.com
```

### Dynamic Domains (init_ok)

Plugins that discover their target URLs at init time (e.g., from
environment variables) can register additional allowed domains via
the `init_ok` response. In Go handlers, call `p.SetInitDomains()`
from within `OnInit`:

```go
p.OnInit(func(_ json.RawMessage) error {
    url := os.Getenv("MY_SERVICE_URL")
    p.SetInitDomains([]string{extractHost(url)})
    return nil
})
```

Dynamic domains are validated against the same rules as
`allowed_domains` (no wildcards, no IPs, no localhost). Maximum
10 domains per plugin.

### Per-Domain Auth Bindings

Plugins that connect to multiple instances of the same service
(e.g., multiple GitLab servers) can register per-domain auth
bindings. Each binding maps a domain to an env var name containing
the token for that domain. The core resolves env var names from
the plugin's credential group and creates per-domain auth
providers.

```go
p.OnInit(func(_ json.RawMessage) error {
    // Register domains for proxy allowlist
    p.SetInitDomains([]string{"gitlab.com", "gitlab.internal.com"})
    // Map each domain to its token env var
    p.SetAuthBindings(map[string]string{
        "gitlab.com":          "GITLAB_PUBLIC_TOKEN",
        "gitlab.internal.com": "GITLAB_INTERNAL_TOKEN",
    })
    return nil
})
```

When auth bindings are present, the proxy injects the correct
token for each request based on the target domain. Requests to
domains without a binding receive no auth (error). The plugin
never accesses token values — only env var names are transmitted.

Auth bindings require `services.auth` in plugin.yaml to define
the shared auth shape (type, header, prefix). The `token` field
in `services.auth` is used only for single-instance mode (when
no auth bindings are registered).

## Wire Protocol

All communication is JSON objects separated by newlines (JSON-lines).
Each message has an `id` and `type` field. Messages are correlated
by `id`.

### Lifecycle (persistent plugins only)

```
Core → Plugin:  {"id": "init-1", "type": "init", "protocol": "1.0",
                  "config": {"_session_dir": "/home/user/project",
                             "_output_dir": "/home/user/project/.wtmcp-data/myplugin",
                             "_credentials_dir": "/home/user/.config/wtmcp/credentials/mygroup",
                             "MY_API_URL": "https://api.example.com", ...}}
Plugin → Core:  {"id": "init-1", "type": "init_ok"}
...tool calls...
Core → Plugin:  {"id": "shutdown-2", "type": "shutdown"}
Plugin → Core:  {"id": "shutdown-2", "type": "shutdown_ok"}
```

Message IDs are generated with a counter suffix (`init-1`,
`req-2`, etc.). Handlers should echo back the received `id`.

If the handler cannot initialize, respond with `init_error`:

```json
{"id": "init-1", "type": "init_error",
 "error": {"code": "missing_config", "message": "MY_API_URL is required"}}
```

The `protocol` field contains the wire protocol version (`"1.0"`).
The `config` object contains resolved environment variables and
internal config values (`_session_dir`, `_output_dir`,
`_credentials_dir`).

### Tool Calls

```
Core → Plugin:  {"id": "req-1", "type": "tool_call", "tool": "my_tool",
                  "params": {"query": "test"},
                  "config": {"_session_dir": "...", "_output_dir": "...", ...}}
Plugin → Core:  {"id": "req-1", "type": "tool_result",
                  "result": {"items": [...]}}
```

The `config` field contains the same resolved configuration as the
init `config`, refreshed on each call (picks up changes from
`plugin_reload`).

Error response:

```json
{"id": "req-1", "type": "tool_result",
 "error": {"code": "not_found", "message": "No results"}}
```

### HTTP Proxy

Plugins never make HTTP calls directly. Instead, they send
`http_request` messages and the core handles auth, TLS, retries:

```
Plugin → Core:  {"id": "http-1", "type": "http_request",
                  "method": "GET", "path": "/api/items",
                  "query": {"q": "test", "limit": "10"}}

Core → Plugin:  {"id": "http-1", "type": "http_response",
                  "status": 200,
                  "headers": {"Content-Type": "application/json"},
                  "body": {"items": [...]}}
```

POST with body:

```json
{"id": "http-2", "type": "http_request",
 "method": "POST", "path": "/api/items",
 "headers": {"Content-Type": "application/json"},
 "body": {"name": "New Item"}}
```

Full URL override (for URLs outside base_url):

```json
{"id": "http-3", "type": "http_request",
 "method": "GET", "url": "https://other.example.com/api/data"}
```

#### Per-Request Auth Bypass (no_auth)

Plugins can send individual requests without auth injection. This
is useful for bootstrap endpoints that don't require auth (e.g.,
downloading a CA certificate from a public endpoint):

```json
{"id": "http-4", "type": "http_request",
 "method": "GET",
 "url": "http://server.example.com/public/ca.crt",
 "no_auth": true}
```

When `no_auth` is set:
- Auth headers (Kerberos, Bearer, Basic) are not injected
- HTTP scheme is allowed (normally HTTPS is required with auth)
- Domain allowlisting and SSRF protection still apply
- If the plugin uses mTLS (`client_cert`), HTTPS is still required
  regardless of `no_auth`

#### Binary Responses

- JSON responses: `body` is the parsed JSON object
- Text responses (`text/*`): `body` is a JSON string
- Binary responses: `body` is base64-encoded,
  `"body_encoding": "base64"` is set

#### Multipart Upload

For file uploads, use `multipart` instead of `body`:

```json
{"id": "http-5", "type": "http_request",
 "method": "POST", "path": "/api/upload",
 "multipart": [
   {"field": "file", "filename": "doc.pdf",
    "content_type": "application/pdf",
    "body": "<base64-encoded>", "body_encoding": "base64"},
   {"field": "comment", "body": "Uploaded via automation"}
 ]}
```

The core assembles the `multipart/form-data` body and sets the
`Content-Type` header with the boundary. Do not set `Content-Type`
yourself for multipart requests.

### Cache

The core provides a key-value cache. Plugins use it through the
protocol:

```
Plugin → Core:  {"id": "c-1", "type": "cache_get", "key": "my-data"}
Core → Plugin:  {"id": "c-1", "type": "cache_get", "hit": true,
                  "value": {"cached": "data"}}
```

```
Plugin → Core:  {"id": "c-2", "type": "cache_set", "key": "my-data",
                  "value": {"new": "data"}, "ttl": 3600}
Core → Plugin:  {"id": "c-2", "type": "cache_set", "ok": true}
```

Delete a key:

```
Plugin → Core:  {"id": "c-3", "type": "cache_del", "key": "item:123"}
Core → Plugin:  {"id": "c-3", "type": "cache_del", "ok": true, "deleted": true}
```

List keys by glob pattern:

```
Plugin → Core:  {"id": "c-4", "type": "cache_list", "pattern": "item:*"}
Core → Plugin:  {"id": "c-4", "type": "cache_list",
                  "keys": ["item:123", "item:456"]}
```

Flush all keys in the plugin's namespace:

```
Plugin → Core:  {"id": "c-5", "type": "cache_flush"}
Core → Plugin:  {"id": "c-5", "type": "cache_flush", "ok": true, "count": 42}
```

### File I/O

The core provides secure file write and read operations. Plugins
must NOT write to their output directory directly — all file I/O
goes through the core service, which enforces path confinement,
atomic writes, and permission validation.

**Write a file (inline content):**

```
Plugin → Core:  {"id": "fw-1", "type": "file_write",
                  "path": "case-01234567.json",
                  "content": "{\"key\": \"value\"}",
                  "body_encoding": "text",
                  "permissions": "0600"}
Core → Plugin:  {"id": "fw-1", "type": "file_write_response",
                  "path": "/abs/path/to/case-01234567.json",
                  "size": 18}
```

**Write a file (source_path handoff for large files):**

For files larger than a few MB (e.g., downloaded attachments),
write the content to tmpdir first, then pass the path:

```
Plugin → Core:  {"id": "fw-2", "type": "file_write",
                  "path": "attachment.bin",
                  "source_path": "/tmp/wtmcp-data/plugin/dl-123.tmp",
                  "permissions": "0600"}
Core → Plugin:  {"id": "fw-2", "type": "file_write_response",
                  "path": "/abs/path/to/attachment.bin",
                  "size": 1048576}
```

The source file must be in the plugin's tmpdir and have exactly
one hard link (Nlink == 1). Hardlink-based dedup in tmpdir is
not supported — this is a security requirement.

**Read a file:**

```
Plugin → Core:  {"id": "fr-1", "type": "file_read",
                  "path": "case-01234567.json",
                  "body_encoding": "text"}
Core → Plugin:  {"id": "fr-1", "type": "file_read_response",
                  "content": "{\"key\": \"value\"}",
                  "path": "/abs/path/to/case-01234567.json"}
```

**Fields:**

- `path` — relative path under the plugin's output directory.
  Supports subdirectories (e.g., `cache/data.json`).
- `content` — inline content (mutually exclusive with `source_path`).
- `source_path` — temp-file path for large content (mutually
  exclusive with `content`).
- `body_encoding` — `"text"` (default, validates UTF-8) or
  `"base64"` (for binary data). Case-sensitive.
- `permissions` — octal file mode (default `"0600"`). No world
  access, no setuid/setgid/sticky, owner-read required.
- `mkdir` — create parent directories (default `true`). Set to
  `false` to require the parent to exist.

**Go SDK helpers:**

```go
// Write text content
path, err := p.FileWrite("data.json", []byte(`{"key":"value"}`))

// Write binary content
path, err := p.FileWrite("image.png", pngBytes, handler.WithEncoding("base64"))

// Write via source_path handoff
path, err := p.FileWriteFrom("attachment.bin", tmpFilePath)

// Read a file
data, err := p.FileRead("data.json")
```

**Python handler functions:**

```python
# Write text content
path = handler.file_write("data.json", '{"key": "value"}')

# Write binary content (auto-encodes bytes to base64)
path = handler.file_write("image.png", png_bytes, encoding="base64")

# Write via source_path handoff
path = handler.file_write_from("attachment.bin", tmp_file_path)

# Read a file
content = handler.file_read("data.json")
```

### Resources

Plugins can provide MCP resources (read-only data accessible by
the AI client). Declare `provides_resources: true` in `plugin.yaml`
and respond to resource queries during init:

```
Core → Plugin:  {"id": "res-1", "type": "list_resources"}
Plugin → Core:  {"id": "res-1", "type": "list_resources_ok",
                  "resources": [
                    {"uri": "plugin://myplugin/status",
                     "name": "Plugin Status",
                     "description": "Current plugin status",
                     "mime_type": "application/json"}
                  ]}
```

When a client reads a resource:

```
Core → Plugin:  {"id": "res-2", "type": "read_resource",
                  "uri": "plugin://myplugin/status"}
Plugin → Core:  {"id": "res-2", "type": "read_resource_ok",
                  "content": "{\"status\": \"healthy\"}",
                  "mime_type": "application/json"}
```

## Complete Examples

### Bash Oneshot Plugin

The simplest possible plugin. No main loop needed — the core spawns
the handler for each tool call and sends one message on stdin.

**plugin.yaml:**
```yaml
name: hello
version: "1.0.0"
description: "A greeting plugin"
execution: oneshot
handler: ./handler.sh
services: {}
tools:
  - name: hello_world
    description: "Says hello to someone"
    params:
      name:
        type: string
        default: "World"
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

### Python Persistent Plugin with HTTP and Cache

A persistent plugin that queries an API and caches results. Zero
dependencies beyond Python stdlib.

**plugin.yaml:**
```yaml
name: weather
version: "1.0.0"
description: "Weather lookup"
execution: persistent
handler: ./handler.py
services:
  http:
    base_url: "https://api.weather.example.com"
  cache:
    default_ttl: 600
tools:
  - name: weather_get
    description: "Get weather for a city"
    params:
      city:
        type: string
        required: true
enabled: true
```

**handler.py:**
```python
#!/usr/bin/env python3
import json
import sys


def send(msg):
    print(json.dumps(msg, separators=(",", ":")), flush=True)


def recv():
    line = sys.stdin.readline()
    if not line:
        sys.exit(0)
    return json.loads(line.strip())


_next_id = 0


def gen_id():
    global _next_id
    _next_id += 1
    return f"svc-{_next_id}"


def http(method, path, query=None):
    msg = {"id": gen_id(), "type": "http_request", "method": method, "path": path}
    if query:
        msg["query"] = query
    send(msg)
    resp = recv()
    return resp.get("status", 0), resp.get("body", {})


def cache_get(key):
    send({"id": gen_id(), "type": "cache_get", "key": key})
    resp = recv()
    return resp["value"] if resp.get("hit") else None


def cache_set(key, value, ttl=None):
    msg = {"id": gen_id(), "type": "cache_set", "key": key, "value": value}
    if ttl:
        msg["ttl"] = ttl
    send(msg)
    recv()


def weather_get(params):
    city = params["city"]

    cached = cache_get(f"weather:{city}")
    if cached:
        return cached

    status, body = http("GET", f"/v1/weather", query={"city": city})
    if 200 <= status < 300:
        cache_set(f"weather:{city}", body, ttl=600)
    return body


TOOLS = {"weather_get": weather_get}

# Main loop
while True:
    msg = recv()
    msg_type = msg.get("type")

    if msg_type == "init":
        send({"id": msg["id"], "type": "init_ok"})
    elif msg_type == "shutdown":
        send({"id": msg["id"], "type": "shutdown_ok"})
        break
    elif msg_type == "tool_call":
        fn = TOOLS.get(msg.get("tool"))
        if fn:
            try:
                result = fn(msg.get("params", {}))
                send({"id": msg["id"], "type": "tool_result", "result": result})
            except Exception as e:
                send({"id": msg["id"], "type": "tool_result",
                      "error": {"code": "error", "message": str(e)}})
        else:
            send({"id": msg["id"], "type": "tool_result",
                  "error": {"code": "unknown_tool", "message": msg.get("tool")}})
```

## Setup Wizard Metadata

Plugins can declare a `setup` section with human-facing metadata for
configuration wizards:

```yaml
setup:
  credentials:
    MY_API_URL:
      description: "API base URL"
      example: "https://api.example.com"
      secret: false
    MY_TOKEN:
      description: "API authentication token"
      help_url: "https://docs.example.com/tokens"
      instructions: "Go to Settings > API Tokens > Create"
      secret: true
  validation_tool: my_get_status
  post_setup_message: "Restart the MCP server for changes to take effect."
```

The `auth_type` field is informational (for setup wizards):
`kerberos`, `bearer`, `basic`, `oauth2`, `mtls`.

For plugins with auth variants, add variant labels:

```yaml
setup:
  variants:
    cloud:
      label: "Cloud (hosted)"
      description: "For *.example.com instances"
      required: [MY_API_URL, MY_EMAIL, MY_TOKEN]
    server:
      label: "Self-hosted"
      required: [MY_API_URL, MY_TOKEN]
```

## Reloading Plugins

Plugins can be reloaded at runtime without restarting the MCP server.
This is useful when developing a plugin or deploying an update.

### From an AI assistant (MCP tool)

The built-in `plugin_reload` tool restarts a plugin and re-registers
its tools and context resources:

```
plugin_reload(name="jira")
```

Connected MCP clients receive `notifications/tools/list_changed`
and `notifications/resources/list_changed` automatically.

### From a terminal (control directory)

External tools can trigger reloads by writing command files to the
control directory at `{workdir}/control/commands/`:

```bash
# Reload a specific plugin
touch ~/.config/wtmcp/control/commands/reload-jira

# Reload all plugins
touch ~/.config/wtmcp/control/commands/reload-all

# List loaded plugins
touch ~/.config/wtmcp/control/commands/list
```

Results appear in `{workdir}/control/results/` as JSON:

```bash
cat ~/.config/wtmcp/control/results/reload-jira.json
```

The command file is consumed (deleted) after processing.

### What gets reloaded

- The handler process is stopped and restarted
- `plugin.yaml` is re-read (new/changed tools take effect)
- Tools are re-registered with the MCP server
- Context resources are re-registered
- MCP clients are notified of the changes

Note: context file **content** (e.g., `context.md`) is loaded from
disk on every access, so content changes take effect immediately
without any reload. Reload is only needed when:

- The handler code changes (Python script, Go binary)
- `plugin.yaml` changes (new tools, changed params, config)
- Context files are added or removed

### Process tracking

The server writes a PID file to `{workdir}/control/mcp.pid` on
startup and removes it on shutdown. Use this to check if the
server is running:

```bash
kill -0 $(cat ~/.config/wtmcp/control/mcp.pid) 2>/dev/null && echo "running" || echo "stopped"
```

## Plugin Environment

Plugins do **not** inherit the core's environment. They receive only
safe system variables (`PATH`, `HOME`, `SHELL`, etc.) plus variables
from their `credential_group`'s env.d file.

### credential_group

Every plugin that uses `${VAR}` references or needs env vars must
declare a `credential_group`. This scopes which `env.d/` file the
plugin can access:

```yaml
credential_group: jira    # matches env.d/jira.env
```

The group name matches the env.d filename (without `.env`). Multiple
plugins can share a group — e.g., `google-calendar`, `google-drive`,
and `google-gmail` all declare `credential_group: google` to share
one `env.d/google.env` credential file.

env.d files and credential files (`client-credentials.json`, TLS
cert/key) can be encrypted with Ansible Vault. The server
auto-detects encrypted files and decrypts them transparently —
plugins receive plaintext credentials as usual. Encrypted
credential files are decrypted to memory-backed file descriptors
(never written to disk). See the "Encrypted Credentials" section
in README.md for setup details.

Without `credential_group`, all `${VAR}` references resolve to empty
strings and no env.d vars are passed to the handler.

### env: list

The `env:` field lists which vars from the credential group's env.d
file are passed to the handler process:

```yaml
credential_group: jira
env:
  - JIRA_URL
  - JIRA_TOKEN
```

Only vars listed here AND present in `env.d/jira.env` reach the
handler. Shell-exported variables are ignored.

### env_passthrough: all

For plugins that discover configuration dynamically (e.g., scanning
env vars for multiple instances), list all group vars explicitly
is impossible. Use `env_passthrough: all` to pass everything from
the credential group's env.d file:

```yaml
credential_group: gitlab
env_passthrough: all

env:
  # Listed for documentation only — not used as filter
  - GITLAB_TOKEN
  - GITLAB_URL
```

The `env:` field still serves as documentation of expected vars for
setup wizards and human readers.

### ${VAR} resolution

`${VAR}` references in `config:`, `base_url`, and `services.auth`
fields are resolved exclusively from the plugin's credential group
env.d file — never from the process environment. This means
`export JIRA_TOKEN=xxx` in your shell has no effect on plugins.

### _credentials_dir

The server automatically injects `_credentials_dir` into the
resolved config for plugins that declare a `credential_group`. This
is the per-group credentials directory path (e.g.,
`~/.config/wtmcp/credentials/google/`). Go plugin handlers can read
it from the init config to find credential files:

```go
p.OnInit(func(cfgRaw json.RawMessage) error {
    var cfg map[string]string
    json.Unmarshal(cfgRaw, &cfg)
    credDir := cfg["_credentials_dir"]
    // credDir = "/home/user/.config/wtmcp/credentials/google"
    ...
})
```

Python plugins that use the HTTP proxy don't need this — the server
handles credential injection automatically.

### _session_dir and _output_dir

The server injects two directory paths into the resolved config:

- **`_session_dir`**: the user's project directory (the CWD when
  wtmcp started). Plugins can read files from this directory.
- **`_output_dir`**: a per-plugin output directory at
  `<session_dir>/.wtmcp-data/<plugin-name>`. Plugins should write output
  files here (exports, reports, downloaded attachments, etc.).

Both values are available in the `config` during init and on each
`tool_call`. If the user started wtmcp from a directory that is too
broad (e.g., `/`), both values are empty and file I/O is
unavailable.

**Path confinement:** plugins that accept file paths from LLM
parameters should validate them against these directories to
prevent path traversal. The recommended pattern (from the jira
plugin):

```python
from pathlib import Path

def _validate_export_path(file_path):
    output_dir = config.get("_output_dir", "")
    if not output_dir:
        raise ValueError("save requires a configured output directory")
    if not file_path:
        raise ValueError("output path is required")
    path = Path(file_path)
    if not path.is_absolute():
        path = Path(output_dir) / path
    resolved = path.resolve()
    base = Path(output_dir).resolve()
    try:
        resolved.relative_to(base)
    except ValueError:
        raise ValueError(f"path escapes output directory: {file_path}")
    return resolved
```

Under sandbox (`make build-sandbox`), `_session_dir` is in the
sandbox's read paths and `_output_dir` is in the write paths.
Attempting to access files outside these directories will fail with
`EACCES`.

## Security

- Plugins are semi-trusted: without sandboxing they run with the
  same OS privileges as the core. With sandbox enabled (`-tags
  sandbox`), plugins are confined via Landlock, cgroups, and network
  namespaces. Only install plugins you trust.
- **User plugins** (in `{workdir}/plugins/`) are disabled by default.
  Enable with `user_plugins: true` in `config.yaml`. User plugins
  cannot override system plugins, declare `provides.auth`, or claim
  credential groups owned by system plugins.
- Auth tokens are injected by the HTTP proxy. Plugins never see them.
- The proxy enforces HTTPS when auth or mTLS is configured.
  mTLS HTTPS enforcement cannot be bypassed by `no_auth`.
- The proxy validates that request URLs match the plugin's declared
  `base_url` domain or `allowed_domains` list. The `base_url`
  hostname is auto-added. IP addresses and localhost are rejected
  in user-declared `allowed_domains`.
- Only `http` and `https` URL schemes are permitted. Exotic schemes
  (`file://`, `ftp://`, etc.) are rejected.
- The proxy strips security-sensitive headers (Authorization, Cookie,
  etc.) from plugin-specified headers before forwarding.
- The proxy rejects connections to private/loopback IP addresses
  (SSRF protection).
- **TLS certificate files** (`ca_cert`, `client_cert`, `client_key`)
  are validated at plugin load time. Private key files must have
  restrictive permissions (0600/0400). CA cert PEM is pre-loaded to
  prevent TOCTOU attacks. Cert/key pairs are validated as matching.
- **Credential isolation:** plugins only see env.d variables from
  their own `credential_group`. Shell-exported environment variables
  are not used for plugin variable resolution.
- **env.d file permissions** are enforced (0600 files, 0700 dir) —
  the server refuses to start if they are world-readable.
- **Tool access annotations** (`access: read` / `access: write`)
  inform MCP clients about destructive operations. Read-only tools
  are **enforced** at the proxy layer: `access: read` tools can
  only make GET, HEAD, and OPTIONS requests. POST, PUT, PATCH,
  and DELETE are rejected with `method_not_allowed`.
- Cache namespaces are isolated — plugins cannot read other plugins'
  cached data.
- **Sandboxing** (optional, `make build-sandbox`): Landlock
  filesystem confinement restricts read/write to declared paths.
  cgroup v2 resource limits cap memory, CPU, PIDs, and file size
  per plugin. Network namespace isolation forces all traffic through
  the core proxy.
- **Rate limiting**: tool calls are rate-limited per plugin, and
  HTTP proxy requests are rate-limited per target domain (defaults:
  120 req/min, 600 req/min global). Configurable via `config.yaml`
  under `http.rate_limit`.
- **Audit logging**: all tool calls and HTTP requests are logged
  with UUIDv7 correlation IDs. Credential fields are automatically
  scrubbed from audit output.
- **HTTP retries**: idempotent requests (GET, HEAD, OPTIONS, PUT,
  DELETE) are automatically retried on transient failures (500, 502,
  503, 504) with exponential backoff (capped at 30s). POST/PATCH
  are never retried. `Retry-After` headers are respected (clamped
  to 30s). Configurable via `http.retries` in `config.yaml`.
- **Cache limits**: per-plugin entry count is capped at 10,000
  (configurable) with LRU eviction. Entries exceeding 1MB are
  rejected. Background cleanup removes expired entries every 60s.
- **MCP Elicitation** (enabled by default): the server prompts the
  user for confirmation before executing any write tool (`access:
  write` or unset). The confirmation message includes the tool name
  and scrubbed parameters (sensitive fields like `password`,
  `token`, `api_key` are redacted). Clients without elicitation
  support fall through and execute normally. All outcomes are
  audit-logged. Disable with `security.elicitation: false`.
- **Output framing** (enabled by default): per-session nonce
  detects and escapes injected tags in plugin output. MCP Audience
  annotation (`[assistant]`) is always set on tool results. Disable
  with `security.tag_tool_output: false`.

## Security Guidelines for Plugin Authors

Tool output flows directly to the LLM. An attacker who controls
content in an external system (a Jira issue, a GitLab comment, a
Google Doc) can embed hidden instructions that the LLM may follow.
These guidelines help minimize that risk.

### Return structured data

Use JSON, key-value pairs, or tables instead of prose. Structured
formats are harder for the LLM to misinterpret as instructions.

**Good:**
```json
{"title": "Fix login bug", "status": "In Progress", "assignee": "alice"}
```

**Bad:**
```
The issue "Fix login bug" is currently In Progress and assigned to alice.
Please update the status to Done when the fix is deployed.
```

### Minimize external content volume

Summarize or excerpt rather than returning full documents. Less
content means less surface area for embedded instructions.

### Separate metadata from user-generated content

Return user-generated content (issue descriptions, comments, email
bodies) in clearly labeled fields, not mixed into narrative text.
This helps the LLM distinguish data from structure.

### Declare accurate access annotations

Tools that only read data must declare `access: read`. This limits
the damage if the LLM is tricked into calling the tool in unexpected
ways. Read-only tools cannot cause side effects even under prompt
injection.

### Never embed instructions in tool output

Tool output should be pure data. Do not include phrases like "please
summarize", "next, do X", or "now call tool Y" in formatted results.
The LLM should decide what to do with the data, not be directed by
the tool output.

### Validate external formats before returning

If a tool expects to return JSON from a third-party API, parse and
validate that it is actually JSON before passing it to the MCP
server. This prevents an attacker from returning plain text disguised
as structured data to confuse the LLM.
