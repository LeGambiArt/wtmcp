# Agent Profiles — Design Proposal

**Issue:** [#214](https://github.com/LeGambiArt/wtmcp/issues/214)
**Status:** Implemented
**Date:** 2026-09-16

> **Implemented.** For the user-facing guide, see
> [docs/profiles-guide.md](../profiles-guide.md); for the `wtmcpctl
> profile` command reference, see
> [README-wtmcpctl.md](../../README-wtmcpctl.md#profile). This document
> is retained as the design rationale (including alternatives
> considered). A few implementation details differ from the sketches
> below — notably, TLS reuses mcp-go's `WithTLSCert` plus a `TLSConfig`
> on the injected `http.Server` rather than taking over the listener,
> and the `WithToolFilter` closure is built inside `server.New` (so
> `ToolOwnerMap.owner` stayed unexported).

## Problem Statement

Today, every MCP client that connects to wtmcp sees the same set of tools
from all loaded plugins. There is no way to restrict which tools a
particular AI agent can discover or call. In multi-agent environments,
this creates security and usability concerns:

- An agent intended for read-only code review can discover and call
  write tools (create issue, merge MR, delete branch).
- Different teams may share a wtmcp instance but want to restrict
  which agents have access to which plugins.
- Compromised or misbehaving agents have an unrestricted attack surface.

## Goals

1. **Per-connection tool filtering** — different agents see different
   tool subsets based on a named profile.
2. **Agent-to-profile mapping via TLS client certificates** — use
   SAN (URI, email, DNS) or CN from mTLS client certs as the primary
   identity mechanism.
3. **Backward compatibility** — without profiles configured, all tools
   remain visible (current behavior).
4. **Extensibility** — the profile matching system should support future
   identity sources (API keys, token claims, etc.) without redesign.
5. **Minimal mcp-go coupling** — use upstream APIs, no fork needed.

## Current Architecture (Relevant Parts)

```
┌──────────────┐     stdio or HTTP      ┌──────────────────┐
│  AI Agent    │◄──────────────────────►│   wtmcp server    │
│ (Claude,     │                        │                   │
│  Gemini, ..) │   tools/list           │ MCPServer.AddTool │
└──────────────┘   tools/call           │ (all tools global)│
                                        │                   │
                                        │  ToolIndex        │
                                        │  ToolOwnerMap     │
                                        └─────────┬────────┘
                                                  │
                                        ┌─────────▼────────┐
                                        │  Plugin Manager   │
                                        │  (N plugins, each │
                                        │   with M tools)   │
                                        └──────────────────┘
```

Key observations:

- **No inbound TLS** — `ServerConfig` has only `transport`, `host`,
  `port`. The HTTP listener is plain HTTP with no auth.
- **No per-session state** — mcp-go's `StreamableHTTPServer` manages
  sessions internally via `Mcp-Session-Id` headers, but wtmcp does not
  access or extend them.
- **Tools are registered globally** — `MCPServer.AddTool()` makes every
  tool visible to every client.
- **Existing filtering** — only `ReadOnly` mode (compile/startup-time,
  global) and `plugins.disabled`/`plugins.enabled` (global).
- **Context propagation works** — tool handler closures receive
  `context.Context` from the HTTP request, so values set via mcp-go
  hooks are accessible in tool handlers.

### mcp-go v0.58.0 Native Capabilities

The upstream library provides the exact hooks needed for per-session
tool filtering. The claims below were **verified against the real
v0.58.0 source** (tag `v0.58.0`, commit `9740d3a`), not just docs:

| API | Verified behavior |
|-----|-------------------|
| `WithHTTPContextFunc(fn)` | `func(ctx, *http.Request) context.Context`. Invoked per HTTP request; the returned context flows into `HandleMessage` → `handleListTools`/`handleToolCall`. Values set here survive to the tool filter. Use for TLS client-cert extraction. |
| `WithToolFilter(fn)` | `ToolFilterFunc = func(ctx, []mcp.Tool) []mcp.Tool`. **Enforces at BOTH list and call time.** `tools/list` filters the returned set; `tools/call` runs `passesToolFilters(ctx, tool)` and rejects with `ErrToolNotFound` *before* invoking the handler. One filter is therefore a complete access boundary. |
| `WithStdioContextFunc(fn)` | `func(ctx) context.Context`, called once at `Listen`. The stdio analogue of `WithHTTPContextFunc` — used to inject a startup-resolved filter for the stdio transport. |

Two consequences that shaped this design:

- **A single `WithToolFilter` is sufficient.** Because the filter is
  consulted on `tools/call` as well as `tools/list`, there is no need
  for a separate `WithToolHandlerMiddleware` authorization layer — it
  would run *after* the filter has already rejected the call, so it can
  neither add enforcement nor observe denials. We use one filter.
- **No JSON-RPC interception or response rewriting is needed.**
  Filtering happens inside mcp-go's protocol handling, which correctly
  handles SSE, pagination, deferred tools, and session lifecycle. The
  filter is recomputed per `tools/list` (no stale caching).

## Proposed Design

### Architecture Overview

```
┌──────────────┐      mTLS            ┌───────────────────────────────────┐
│  AI Agent    │◄────────────────────►│   wtmcp server                    │
│ (cert: CN=X, │                      │                                   │
│  SAN=Y)      │                      │  ┌─────────────────────────────┐  │
└──────────────┘                      │  │ http.Server + TLS           │  │
                                      │  │ (mTLS, client_auth=require) │  │
                                      │  └─────────────┬───────────────┘  │
                                      │                │                  │
                                      │  ┌─────────────▼───────────────┐  │
                                      │  │ WithHTTPContextFunc         │  │
                                      │  │ (extract SAN/CN from cert,  │  │
                                      │  │  resolve profile → Filter,  │  │
                                      │  │  store Filter in context)   │  │
                                      │  └─────────────┬───────────────┘  │
                                      │                │ ctx w/ Filter    │
                                      │  ┌─────────────▼───────────────┐  │
                                      │  │ MCPServer (mcp-go)          │  │
                                      │  │                             │  │
                                      │  │ WithToolFilter:             │  │
                                      │  │   tools/list → hide tools   │  │
                                      │  │   tools/call → block tools  │  │
                                      │  │   (one filter, both paths)  │  │
                                      │  └─────────────────────────────┘  │
                                      └───────────────────────────────────┘
```

The design uses two mcp-go hooks — no custom HTTP middleware, no
handler middleware:

1. **`WithHTTPContextFunc`** (`WithStdioContextFunc` for stdio) —
   extracts client identity from the TLS peer certificate, resolves it
   to a `Filter` via the `Resolver`, and stores the `Filter` in the
   request context.
2. **`WithToolFilter`** — reads the `Filter` from context and applies
   it. mcp-go consults this same filter on both `tools/list` (hides
   tools) and `tools/call` (blocks the call before the handler runs),
   so a single filter is the complete access boundary.

### 1. Configuration Schema

Profile configuration uses a **`profiles.d/` drop-in directory** — the
same `conf.d` convention used by `env.d/` for credentials. Each YAML
file in the directory defines one or more profiles and their agent
matching rules. This lets users add profiles for new agents by
dropping a file, without touching existing configuration.

#### Directory Layout

```
~/.config/wtmcp/
├── config.yaml                  # server, plugins, etc. (unchanged)
├── env.d/                       # credential files (existing)
│   ├── gitlab.env
│   └── jira.env
└── profiles.d/                  # NEW: profile drop-in directory
    ├── code-review.yaml         # profile for code review agents
    ├── ci-automation.yaml       # profile for CI agents
    └── full-access.yaml         # unrestricted profile
```

The `profiles.d/` directory lives under the workdir (default
`~/.config/wtmcp/`), following the same convention as `env.d/`.
An alternative directory can be specified in `config.yaml`:

```yaml
# In config.yaml — only global profile settings, not definitions
profiles:
  dir: /etc/wtmcp/profiles.d     # optional override (default: {workdir}/profiles.d)
  default: "read-only"           # OPTIONAL fallback when no rule matches.
                                 # If omitted, unmatched agents are DENIED
                                 # all tools (fail closed). See §1 "Default
                                 # behavior and fail-closed".
```

#### Profile File Format

Each file in `profiles.d/` is a standalone YAML document defining one
or more profiles and the agent matching rules that select them.

**Two intentionally simple/restrictive rules govern matching** (see
§3 for the rationale):

- **Identity match values are exact strings, not patterns.** A rule
  matches a certificate field by exact equality. This avoids the
  footgun of a regex like `spiffe://example.com/...` where unescaped
  `.` silently widens the match. To match several identities, write
  several rules.
- **Each rule matches on exactly one field** (`cn`, `san_uri`,
  `san_dns`, or `san_email`). Rule evaluation is therefore
  **order-independent** — a `(field, value)` pair maps to exactly one
  profile, and matching is a set lookup, not a positional scan. File
  load order does not affect which profile an agent gets.

Tool allow/deny patterns *are* regexps (anchored — see §1 "Allow/Deny
Evaluation"), since matching families like `gitlab_get_*` is the core
requested capability and tool names are a constrained character set.

```yaml
# profiles.d/code-review.yaml
#
# Profile for read-only code review agents.

definitions:
  code-review:
    description: "Read-only access for code review agents"
    allow:
      gitlab:
        - "gitlab_get_.*"
        - "gitlab_list_.*"
        - "gitlab_search_.*"
      jira:
        - "jira_get_issue"
        - "jira_search"

rules:
  - match: { san_uri: "spiffe://example.com/agent/code-review" }
    profile: code-review
  - match: { cn: "code-review-bot" }
    profile: code-review
```

```yaml
# profiles.d/ci-automation.yaml

definitions:
  ci-automation:
    description: "CI/CD pipeline agent"
    allow:
      gitlab:
        - ".*"
      jira:
        - "jira_get_issue"
        - "jira_transition_issue"
    deny:
      gitlab:
        - "gitlab_delete_.*"

rules:
  - match: { san_uri: "spiffe://example.com/agent/ci" }
    profile: ci-automation
  - match: { san_email: "ci-bot@example.com" }
    profile: ci-automation
```

```yaml
# profiles.d/full-access.yaml

definitions:
  full-access:
    description: "Unrestricted access to all tools"
    allow:
      "*":
        - ".*"

rules:
  - match: { cn: "admin-agent" }
    profile: full-access
```

Server TLS configuration remains in `config.yaml` (it is server-level,
not per-profile):

```yaml
# config.yaml
server:
  transport: streamable-http
  host: 0.0.0.0
  port: 8443
  tls:
    cert_file: /etc/wtmcp/server.crt
    key_file: /etc/wtmcp/server.key
    ca_file: /etc/wtmcp/ca.crt
    client_auth: require    # MUST be "require" when profiles are
                            # configured — see §2. Any weaker setting
                            # is a fatal config error.

profiles:
  default: "read-only"      # omit for fail-closed (deny unmatched agents)
```

#### Config Structs

```go
// ProfilesConfig holds global profile settings from config.yaml.
// Profile definitions and rules are loaded from profiles.d/*.yaml.
type ProfilesConfig struct {
    Dir     string `yaml:"dir"`     // override profiles.d directory
    Default string `yaml:"default"` // fallback profile name
}

// ProfileFile represents a single profiles.d/*.yaml file.
type ProfileFile struct {
    Definitions map[string]ProfileDefinition `yaml:"definitions"`
    Rules       []ProfileRule                `yaml:"rules"`
}

// ProfileDefinition defines which tools are visible.
type ProfileDefinition struct {
    Description string              `yaml:"description"`
    Allow       map[string][]string `yaml:"allow"` // plugin -> tool name regexps
    Deny        map[string][]string `yaml:"deny"`  // plugin -> tool name regexps
}

// ProfileRule maps an identity to a profile name.
type ProfileRule struct {
    Match   ProfileMatch `yaml:"match"`
    Profile string       `yaml:"profile"`
}

// ProfileMatch defines identity matching criteria. Exactly one field
// must be set (validated at load); the value is matched by exact
// string equality, not regexp.
type ProfileMatch struct {
    CN       string `yaml:"cn"`
    SANURI   string `yaml:"san_uri"`
    SANDNS   string `yaml:"san_dns"`
    SANEmail string `yaml:"san_email"`
}

// ProfileLoadResult holds the merged result of loading profiles.d/.
type ProfileLoadResult struct {
    Definitions map[string]ProfileDefinition // merged from all files
    Rules       []ProfileRule                // collected from all files; order irrelevant (matching is a set lookup)
    Errors      []ProfileLoadError           // per-file and cross-file errors
}

// ProfileLoadError describes a problem found during profile loading.
type ProfileLoadError struct {
    File    string // filename (not full path), empty for cross-file errors
    Message string
    Fatal   bool   // true = cannot proceed; false = warning
}
```

#### Loading and Merging

The loading function follows the same pattern as `LoadEnvGroups`:

```go
// LoadProfiles reads *.yaml files from profilesDir, merges
// definitions and rules, and detects duplicates.
// Files are processed in lexicographic order by filename.
func LoadProfiles(profilesDir string) (*ProfileLoadResult, error) {
    result := &ProfileLoadResult{
        Definitions: make(map[string]ProfileDefinition),
    }

    entries, err := os.ReadDir(profilesDir)
    if err != nil {
        if os.IsNotExist(err) {
            return result, nil // no profiles configured
        }
        return nil, fmt.Errorf("read profiles.d: %w", err)
    }

    var files []string
    for _, e := range entries {
        if !e.IsDir() && (strings.HasSuffix(e.Name(), ".yaml") ||
            strings.HasSuffix(e.Name(), ".yml")) {
            files = append(files, e.Name())
        }
    }
    sort.Strings(files) // deterministic order

    seen := make(map[string]string) // profile name -> first file

    for _, name := range files {
        path := filepath.Join(profilesDir, name)
        pf, err := loadProfileFile(path)
        if err != nil {
            result.Errors = append(result.Errors, ProfileLoadError{
                File:    name,
                Message: fmt.Sprintf("parse error: %v", err),
                Fatal:   true,
            })
            continue
        }

        // Merge definitions, detecting duplicates
        for defName, def := range pf.Definitions {
            if firstFile, exists := seen[defName]; exists {
                result.Errors = append(result.Errors, ProfileLoadError{
                    File: name,
                    Message: fmt.Sprintf(
                        "duplicate profile %q: already defined in %s",
                        defName, firstFile),
                    Fatal: true,
                })
                continue
            }
            seen[defName] = name
            result.Definitions[defName] = def
        }

        // Collect rules; order is irrelevant (see NewResolver — rules
        // become a (field,value)->profile map, and duplicate keys are
        // fatal). Files are still walked in sorted order only so error
        // messages are deterministic.
        result.Rules = append(result.Rules, pf.Rules...)
    }

    return result, nil
}
```

#### Duplicate Detection

There is **no profile hierarchy, override, or merge** — the design
deliberately errors out on any collision rather than trying to
reconcile it. Two kinds of duplicate are **fatal**:

1. **Duplicate profile name** — the same profile name defined in more
   than one file (or twice in one file).
2. **Duplicate rule key** — the same `(field, value)` identity match
   appearing in more than one rule, since it would be ambiguous which
   profile that identity maps to.

On a collision the loader records a `ProfileLoadError{Fatal: true}`
naming *both* locations, then keeps scanning so it can report every
error in one pass (it does not stop at the first). If any fatal error
exists, wtmcp logs them all and **refuses to start** — the same way an
invalid `config.yaml` aborts startup. `wtmcpctl profile check` (§6)
reports the identical errors without starting the server.

Example error output:

```
ERROR: profile loading failed:
  ci-automation.yaml: duplicate profile "code-review": already defined in code-review.yaml
  ci-automation.yaml: duplicate rule cn="admin-agent": already used in full-access.yaml
  ci-automation.yaml: rule references undefined profile "nonexistent"
```

#### Allow/Deny Evaluation

For a tool named `<tool_name>` owned by plugin `<plugin_name>`:

1. Check **allow** rules. A tool is allowed if:
   - The plugin name (or `"*"`) has an entry in `allow`, AND
   - At least one regexp in that entry matches the tool name.
   - If `allow` is empty/absent, nothing is allowed (deny-all).
2. Check **deny** rules. A tool is denied if:
   - The plugin name (or `"*"`) has an entry in `deny`, AND
   - Any regexp in that entry matches the tool name.
3. **Deny takes precedence** — a tool must pass allow AND not match deny.

Tool patterns are compiled as **anchored** regexps (`^...$`) so
`gitlab_get` does not match `gitlab_get_and_delete`.

**Management-tool exemption (restricted).** Only the *read-only
introspection* tools are exempt from filtering and always visible:
`plugin_list`, `tool_stats`, and `tool_search`. These are needed for
basic MCP discovery and cannot leak state-changing capability.

`plugin_reload` is a **mutating control tool and is NOT exempt** — it
is subject to the profile like any other tool. A restricted profile
that should not reload plugins simply does not list it. (`tool_search`
is exempt as a tool, but its *results* are still filtered — see §5.)

#### Default Behavior and Fail-Closed

The resolution outcome for a connection is:

| Situation | Result |
|-----------|--------|
| No `profiles.d/` directory, or it is empty | **No filtering** — all tools visible (backward compatible; unchanged from today) |
| Profiles configured, a rule matches | The matched profile's filter |
| Profiles configured, no rule matches, `default` is set | The default profile's filter |
| Profiles configured, no rule matches, no `default` | **Deny all** (fail closed) — only the exempt introspection tools remain |
| Profiles configured, identity matches rules for **two different profiles** | **Deny all** (fail closed) + logged warning |

The design **fails closed once profiles exist**: an agent the operator
did not account for gets nothing, rather than everything. Fail-open is
still available, but only by explicit opt-in — set `default` to a
permissive profile. This is the single most important security choice
in the feature, so it is a deliberate, visible decision rather than an
accident of an empty field.

Backward compatibility is preserved because fail-closed only engages
when `profiles.d/` is non-empty; existing deployments with no profiles
behave exactly as before.

### 2. TLS Infrastructure

Add mTLS support to the streamable-http transport.

#### Changes to `internal/config/config.go`

```go
type ServerConfig struct {
    Transport string           `yaml:"transport"`
    Host      string           `yaml:"host"`
    Port      int              `yaml:"port"`
    TLS       *ServerTLSConfig `yaml:"tls"`
}

type ServerTLSConfig struct {
    CertFile   string `yaml:"cert_file"`
    KeyFile    string `yaml:"key_file"`
    CAFile     string `yaml:"ca_file"`
    ClientAuth string `yaml:"client_auth"` // require, request, none
}
```

#### Changes to `internal/transport/transport.go`

**Listener ownership.** Today `listenHTTP` calls `httpSrv.Start(addr)`
(mcp-go owns the listen loop) and `httpSrv.Shutdown(ctx)`. Whether
`Start` honors a `TLSConfig` on the injected `http.Server` is an
mcp-go internal we should not depend on. The simple, robust approach:
**we own the `http.Server`**, set its `TLSConfig`, mount the mcp-go
handler on the mux (it is already an `http.Handler`), and call
`ListenAndServeTLS`/`ListenAndServe` ourselves. `WithHTTPContextFunc`
still runs because it executes inside the handler's `ServeHTTP`,
independent of who owns the listener. Graceful shutdown calls both the
`http.Server.Shutdown` (stop accepting) and `httpSrv.Shutdown` (drain
MCP sessions).

```go
func listenHTTP(ctx context.Context, srv *mcpserver.MCPServer,
    cfg *config.ServerConfig, logger *slog.Logger,
    contextFunc mcpserver.HTTPContextFunc) error {

    mux := http.NewServeMux()
    httpServer := &http.Server{
        Handler:           mux,
        ReadHeaderTimeout: 10 * time.Second,
    }

    opts := []mcpserver.StreamableHTTPOption{ /* heartbeat, TTL, logger */ }
    if contextFunc != nil {
        opts = append(opts, mcpserver.WithHTTPContextFunc(contextFunc))
    }
    httpSrv := mcpserver.NewStreamableHTTPServer(srv, opts...)
    mux.Handle("/mcp", httpSrv)
    mux.HandleFunc("/healthz", handleHealthz)

    if cfg.TLS != nil && cfg.TLS.CertFile != "" {
        tlsConfig, err := buildServerTLS(cfg.TLS)
        if err != nil {
            return fmt.Errorf("server TLS: %w", err)
        }
        httpServer.TLSConfig = tlsConfig
    }

    // ... run httpServer.ListenAndServeTLS / ListenAndServe in a
    // goroutine; on ctx.Done() call httpServer.Shutdown then
    // httpSrv.Shutdown (same select/errCh structure as today) ...
}

func buildServerTLS(cfg *config.ServerTLSConfig) (*tls.Config, error) {
    tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}

    if cfg.CAFile != "" {
        caPEM, err := os.ReadFile(cfg.CAFile)
        if err != nil {
            return nil, fmt.Errorf("read CA file: %w", err)
        }
        pool := x509.NewCertPool()
        if !pool.AppendCertsFromPEM(caPEM) {
            return nil, fmt.Errorf("no valid certificates in CA file")
        }
        tlsCfg.ClientCAs = pool
    }

    switch cfg.ClientAuth {
    case "require":
        tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
    case "request":
        tlsCfg.ClientAuth = tls.VerifyClientCertIfGiven
    default:
        tlsCfg.ClientAuth = tls.NoClientCert
    }

    return tlsCfg, nil
}
```

#### Config Validation: profiles require `client_auth: require`

Profiles identify agents by their **verified** client certificate.
`client_auth: request` (`VerifyClientCertIfGiven`) or `none` would let
a client connect with no certificate — it would then fall through to
the default profile, and if that default were permissive the result is
*unauthenticated* access. To make this mistake impossible, config
validation is restrictive:

> If any profile is configured **and** the transport is
> `streamable-http`, then `server.tls.client_auth` **must** be
> `require`. Anything else is a fatal config error at startup.

Combined with fail-closed defaults (§1), an agent must present a
CA-verified cert that matches a rule to get any tools.

### 3. Profile Resolution

New package: `internal/profile/`

```go
// Identity holds fields extracted from a TLS client certificate.
type Identity struct {
    CN        string
    SANURIs   []string
    SANDNSs   []string
    SANEmails []string
}

// ExtractIdentity extracts identity fields from a peer certificate.
func ExtractIdentity(cert *x509.Certificate) Identity {
    id := Identity{CN: cert.Subject.CommonName}
    for _, u := range cert.URIs {
        id.SANURIs = append(id.SANURIs, u.String())
    }
    id.SANDNSs = cert.DNSNames
    id.SANEmails = cert.EmailAddresses
    return id
}

// ruleKey is a single exact-match criterion: one identity field and
// its exact value. Rules become a map keyed by this — so matching is
// an O(1) set lookup and does NOT depend on file/rule order.
type ruleKey struct {
    field string // "cn" | "san_uri" | "san_dns" | "san_email"
    value string
}

// Resolver maps a verified client identity to a tool Filter, built
// from the merged ProfileLoadResult.
type Resolver struct {
    configured     bool               // any profile file loaded?
    defaultProfile string             // "" => fail closed on no match
    rules          map[ruleKey]string // exact (field,value) -> profile
    filters        map[string]*Filter // profile name -> compiled filter
    denyAll        *Filter            // allows only exempt introspection tools
}

// NewResolver compiles filters and builds the rule lookup. It is
// strict — every one of these is a hard error (no silent recovery):
// a rule with != exactly one match field, a duplicate (field,value)
// rule key, a rule referencing an undefined profile, or a default
// naming an undefined profile.
func NewResolver(defaultProfile string, loaded *config.ProfileLoadResult) (*Resolver, error) {
    r := &Resolver{
        configured:     len(loaded.Definitions) > 0 || len(loaded.Rules) > 0,
        defaultProfile: defaultProfile,
        rules:          make(map[ruleKey]string),
        filters:        make(map[string]*Filter, len(loaded.Definitions)),
        denyAll:        newDenyAllFilter(),
    }

    for name, def := range loaded.Definitions {
        f, err := compileFilter(name, def) // compiles anchored tool regexps
        if err != nil {
            return nil, fmt.Errorf("profile %q: %w", name, err)
        }
        r.filters[name] = f
    }

    for _, rule := range loaded.Rules {
        key, err := ruleKeyOf(rule.Match) // errors unless exactly one field set
        if err != nil {
            return nil, err
        }
        if _, ok := r.filters[rule.Profile]; !ok {
            return nil, fmt.Errorf("rule %v: undefined profile %q", key, rule.Profile)
        }
        if existing, dup := r.rules[key]; dup {
            return nil, fmt.Errorf("duplicate rule %v -> %q and %q", key, existing, rule.Profile)
        }
        r.rules[key] = rule.Profile
    }

    if defaultProfile != "" {
        if _, ok := r.filters[defaultProfile]; !ok {
            return nil, fmt.Errorf("default profile %q is not defined", defaultProfile)
        }
    }
    return r, nil
}

// FilterFor is the single decision point for a connection. It
// implements the table in §1 "Default Behavior and Fail-Closed".
func (r *Resolver) FilterFor(id Identity) *Filter {
    if !r.configured {
        return nil // no profiles => no filtering (backward compatible)
    }
    matched := map[string]struct{}{}
    lookup := func(field string, values ...string) {
        for _, v := range values {
            if p, ok := r.rules[ruleKey{field, v}]; ok {
                matched[p] = struct{}{}
            }
        }
    }
    lookup("cn", id.CN)
    lookup("san_uri", id.SANURIs...)
    lookup("san_dns", id.SANDNSs...)
    lookup("san_email", id.SANEmails...)

    switch len(matched) {
    case 0:
        if r.defaultProfile == "" {
            return r.denyAll // fail closed: unmatched agent gets nothing
        }
        return r.filters[r.defaultProfile]
    case 1:
        for p := range matched {
            return r.filters[p]
        }
    }
    // Ambiguous: one identity matched >1 distinct profile — fail closed.
    log.Printf("profile: identity matched multiple profiles; denying all")
    return r.denyAll
}
```

```go
// Filter determines whether a tool is visible/callable in a profile.
// The zero-allow Filter produced by newDenyAllFilter() permits only
// exempt introspection tools — this is the fail-closed filter.
type Filter struct {
    name  string          // profile name, for audit logging ("" for deny-all)
    allow []pluginMatcher // compiled from ProfileDefinition.Allow (anchored regexps)
    deny  []pluginMatcher // compiled from ProfileDefinition.Deny (anchored regexps)
}

func (f *Filter) Name() string { return f.name }

// IsAllowed returns true if the tool passes allow rules and does not
// match any deny rule. Read-only introspection tools (plugin_list,
// tool_stats, tool_search) are always exempt; plugin_reload is NOT.
func (f *Filter) IsAllowed(pluginName, toolName string) bool {
    if isExemptTool(toolName) {
        return true
    }
    if !f.matchesAllow(pluginName, toolName) {
        return false
    }
    return !f.matchesDeny(pluginName, toolName)
}
```

### 4. Wiring the mcp-go Hooks

Two hooks, wired once at startup. No handler middleware.

#### Identity Extraction — `WithHTTPContextFunc` / `WithStdioContextFunc`

Extract the verified client identity, resolve it to a `Filter`, and
store the `Filter` in the request context. For stdio there is no
certificate, so the filter is resolved once at startup from the
`--profile` flag.

```go
// HTTP (streamable-http): per request, from the verified TLS peer cert.
// client_auth=require guarantees PeerCertificates[0] is CA-verified.
contextFunc := func(ctx context.Context, r *http.Request) context.Context {
    var id profile.Identity
    if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
        id = profile.ExtractIdentity(r.TLS.PeerCertificates[0])
    }
    filter := resolver.FilterFor(id) // nil | profile filter | deny-all
    log.Printf("profile: CN=%q -> %s", id.CN, profile.FilterName(filter))
    return profile.WithFilter(ctx, filter)
}

// stdio: called once at Listen; filter comes from --profile (startup).
stdioContextFunc := func(ctx context.Context) context.Context {
    return profile.WithFilter(ctx, startupFilter)
}
```

#### Enforcement — `WithToolFilter` (one filter, both paths)

A single filter covers `tools/list` (hides tools) and `tools/call`
(mcp-go rejects a disallowed call *before* the handler runs). No
separate middleware layer — it would run after mcp-go has already
rejected the call and could neither enforce nor observe denials.

```go
// In internal/server/server.go, when creating MCPServer.
// NB: ToolOwnerMap.owner() must be exported as ToolOwnerMap.Owner().
toolFilter := func(ctx context.Context, tools []mcp.Tool) []mcp.Tool {
    filter := profile.FilterFromContext(ctx)
    if filter == nil {
        return tools // no profiles configured — unfiltered (backward compat)
    }
    allowed := make([]mcp.Tool, 0, len(tools))
    for _, tool := range tools {
        if filter.IsAllowed(toolOwners.Owner(tool.Name), tool.Name) {
            allowed = append(allowed, tool)
        }
    }
    return allowed
}

srv := mcpserver.NewMCPServer("wtmcp", version,
    mcpserver.WithToolCapabilities(true),
    mcpserver.WithToolFilter(toolFilter),
    // ... existing options ...
)
```

**Denied calls and audit.** When a client calls a tool its profile
forbids, mcp-go rejects it with `ErrToolNotFound` before any handler
runs — the client sees an ordinary "tool not found", which usefully
does not confirm the tool exists. The primary audit signal is the
per-connection profile assignment logged in the context func. If
per-call denial auditing is required, attach mcp-go's `Hooks.OnError`
(the custom `frameErrorResult`/`ProfileDenied` middleware from earlier
drafts cannot fire, since the native filter rejects first).

### 5. tool_search Profile Awareness

This is **not** a secondary concern. Tool discovery defaults to
`progressive` mode (`config.go` `Discovery: "progressive"`), where
most tools are `defer_loading` and are surfaced to the agent through
`tool_search` rather than `tools/list`. So for the majority of tools,
`tool_search` result filtering — not the `tools/list` filter — is the
primary *discovery* control. (Call safety is still guaranteed by the
same `WithToolFilter` on `tools/call`, so an unfiltered search result
could not be invoked anyway; this is about not advertising tools the
agent cannot use.)

The `tool_search` tool uses `ToolIndex.Search()`, which operates on
its own index separate from mcp-go's tool list, so it must apply the
profile filter to its own results.

```go
// In registerToolSearch (internal/server/discovery.go):

srv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    // ... existing search logic ...
    results := index.Search(query, pluginFilter, limit, excludeWrite)

    // Filter results by profile
    if filter := profile.FilterFromContext(ctx); filter != nil {
        var filtered []ToolEntry
        for _, r := range results {
            if filter.IsAllowed(r.Plugin, r.Name) {
                filtered = append(filtered, r)
            }
        }
        results = filtered
    }

    // ... marshal and return ...
})
```

### 6. Profile Validation (`wtmcpctl profile check`)

A new `wtmcpctl profile` command group provides offline validation
of profile configuration. This lets users verify their `profiles.d/`
setup without starting the server, catch errors early, and debug
agent-to-profile matching.

#### `wtmcpctl profile check`

Loads all `profiles.d/*.yaml` files and performs comprehensive
validation. Reports all errors and warnings at once (does not
stop at the first error). Exit code 0 = valid, 1 = errors found.

```
$ wtmcpctl profile check

profiles.d directory: /home/user/.config/wtmcp/profiles.d

loading profile files:
  ✓ ci-automation.yaml (1 definition, 2 rules)
  ✓ code-review.yaml (1 definition, 2 rules)
  ✓ full-access.yaml (1 definition, 1 rule)

definitions: 3
  - ci-automation: 2 allow plugins, 1 deny plugin
  - code-review: 2 allow plugins
  - full-access: 1 allow plugin (wildcard)

rules: 5 (order-independent; each is one exact field match)
  san_uri = spiffe://example.com/agent/code-review → code-review    [code-review.yaml]
  cn      = code-review-bot                         → code-review    [code-review.yaml]
  san_uri = spiffe://example.com/agent/ci           → ci-automation  [ci-automation.yaml]
  san_email = ci-bot@example.com                    → ci-automation  [ci-automation.yaml]
  cn      = admin-agent                             → full-access    [full-access.yaml]

default profile: read-only  (unmatched agents → this profile)

status: ok
```

When errors are found:

```
$ wtmcpctl profile check

profiles.d directory: /home/user/.config/wtmcp/profiles.d

loading profile files:
  ✓ ci-automation.yaml (2 definitions, 2 rules)
  ✗ code-review.yaml: parse error: yaml: line 5: mapping values not allowed
  ✓ full-access.yaml (1 definition, 1 rule)

errors:
  ✗ ci-automation.yaml: duplicate profile "full-access": already defined in full-access.yaml
  ✗ code-review.yaml: parse error: yaml: line 5: mapping values not allowed
  ✗ ci-automation.yaml: rule 2: invalid regexp "gitlab_[": error parsing regexp: missing closing ]

warnings:
  ! ci-automation.yaml: rule 1: profile "staging" is defined but no rule references it
  ! full-access.yaml: allow pattern ".*" on plugin "*" grants unrestricted access

status: 3 errors, 2 warnings
```

#### Validation Checks

The validator performs the following checks, organized by severity:

**Fatal errors** (prevent server startup):

| Check | Description |
|-------|-------------|
| YAML parse error | File is not valid YAML |
| Duplicate profile name | Same profile name defined in more than one file (or twice in one) |
| Duplicate rule key | Same `(field, value)` identity match used by more than one rule |
| Rule match not single-field | A rule has zero, or more than one, match field set |
| Invalid tool regexp | An `allow`/`deny` tool pattern is not a valid Go regexp |
| Undefined profile reference | A rule's `profile:` names a profile not in any `definitions:` |
| Default profile undefined | `profiles.default` names an undefined profile |
| Weak client_auth | Profiles configured on streamable-http but `client_auth` != `require` |

(Identity match values are exact strings, so there is no "invalid
identity regexp" check — that footgun no longer exists.)

**Warnings** (server starts, but configuration may be unintentional):

| Check | Description |
|-------|-------------|
| Unreferenced profile | A profile is defined but no rule maps to it (and it's not the default) |
| No default set | Profiles configured with no `default` — unmatched agents are denied all tools (fail closed). Informational. |
| Wildcard allow | Profile uses `"*"` plugin with `".*"` tool pattern (unrestricted) |
| Unknown plugin name | An `allow`/`deny` key doesn't match any discovered plugin name |

The "unknown plugin name" check requires plugin discovery, so it only
runs when `--with-plugins` is passed (or during server startup). Without
it, plugin names are accepted without validation.

```
$ wtmcpctl profile check --with-plugins

  ...
  warnings:
    ! ci-automation.yaml: allow references unknown plugin "gitlba" (did you mean "gitlab"?)
```

#### `wtmcpctl profile list`

Lists all defined profiles with a summary of their allow/deny rules.

```
$ wtmcpctl profile list

PROFILE          PLUGINS   TOOLS   DENY   SOURCE
code-review      2         5       0      code-review.yaml
ci-automation    2         3       1      ci-automation.yaml
full-access      1 (*)     all     0      full-access.yaml
```

#### `wtmcpctl profile test`

Tests which profile a given identity would match. Useful for debugging
rule ordering.

```
$ wtmcpctl profile test --cn "ci-bot" --san-email "ci-bot@example.com"

matched rule: san_email = ci-bot@example.com   [ci-automation.yaml]
  profile: ci-automation

allowed tools (12):
  gitlab_get_project, gitlab_list_merge_requests, ...
  jira_get_issue, jira_transition_issue

denied tools (2):
  gitlab_delete_branch, gitlab_delete_tag
```

```
$ wtmcpctl profile test --cn "unknown-agent"

no rule matched
default profile: read-only   (if no default were set: DENY ALL, fail closed)

allowed tools: (read-only profile) ...
```

If an identity matches rules for two different profiles, `test`
reports the conflict and shows the fail-closed (deny-all) outcome,
matching runtime behavior.

#### Implementation

```go
// In cmd/wtmcpctl/profile.go:

var profileCmd = &cobra.Command{
    Use:   "profile",
    Short: "Manage and validate agent profiles",
}

var profileCheckCmd = &cobra.Command{
    Use:   "check",
    Short: "Validate profile configuration in profiles.d/",
    RunE:  runProfileCheck,
}

var profileListCmd = &cobra.Command{
    Use:   "list",
    Short: "List defined profiles",
    RunE:  runProfileList,
}

var profileTestCmd = &cobra.Command{
    Use:   "test",
    Short: "Test which profile matches a given identity",
    RunE:  runProfileTest,
}

func init() {
    profileCheckCmd.Flags().Bool("with-plugins", false,
        "Also validate plugin name references against discovered plugins")
    profileTestCmd.Flags().String("cn", "", "Common Name to test")
    profileTestCmd.Flags().String("san-uri", "", "SAN URI to test")
    profileTestCmd.Flags().String("san-dns", "", "SAN DNS name to test")
    profileTestCmd.Flags().String("san-email", "", "SAN email to test")
    profileCmd.AddCommand(profileCheckCmd, profileListCmd, profileTestCmd)
}

func runProfileCheck(cmd *cobra.Command, _ []string) error {
    cfg, workdir := loadConfigOrDie()
    profilesDir := config.ResolveProfilesDir(cfg, workdir)

    loaded, err := config.LoadProfiles(profilesDir)
    if err != nil {
        return err
    }

    // Run validation
    errors := profile.Validate(loaded, cfg.Profiles.Default)

    // Optionally validate against discovered plugins
    if withPlugins, _ := cmd.Flags().GetBool("with-plugins"); withPlugins {
        result, err := plugin.Discover(plugin.DiscoveryOptions{
            WorkdirOverride: globalWorkdir,
        })
        if err != nil {
            return err
        }
        defer result.Close()
        errors = append(errors, profile.ValidatePluginRefs(loaded, result.Manager)...)
    }

    // Print report...
    printProfileCheckReport(loaded, errors)

    if hasFatal(errors) {
        os.Exit(1)
    }
    return nil
}
```

### 7. Stdio Transport Considerations

Profiles are primarily designed for the streamable-http transport where
TLS client certificates provide identity. For stdio transport:

- **Single agent** — stdio is a 1:1 connection, so there's only one
  agent. A profile could be specified via CLI flag:
  `wtmcp --profile code-review`
- **No TLS** — identity comes from the CLI flag, not a certificate.
  The `--profile` value is resolved to a `Filter` once at startup and
  injected via mcp-go's `WithStdioContextFunc` (verified present in
  v0.58.0: `func(ctx) context.Context`, called once at `Listen`). The
  same `WithToolFilter` then enforces it. `WithStdioContextFunc` is the
  concrete mechanism — earlier drafts hand-waved this as a "default
  filter" with no way to reach the filter's context.
- **Future**: the stdio transport could read a profile name from the
  MCP `initialize` request's client info.

### 8. Implementation Plan

#### Phase 1: Configuration & Profile Loading

1. Add `ProfilesConfig` (with `Dir`, `Default` fields) and
   `ServerTLSConfig` to `config.go`.
2. Implement `LoadProfiles()` in `config.go` — reads `profiles.d/`,
   merges definitions and rules, detects duplicate profile names.
3. Add `ProfileLoadResult`, `ProfileLoadError`, `ProfileFile` structs.
4. Add `profiles.d/` path to `StandardPaths`.
5. Unit tests for profile loading: valid files, duplicates, parse
   errors, empty directory, missing directory.

#### Phase 2: Profile Resolution & Filtering Core

6. Create `internal/profile/` package with `Identity`,
   `Resolver`, `Filter`, and context helpers.
7. Implement `NewResolver()` — compile anchored tool regexps; enforce
   exactly-one-match-field per rule; reject duplicate rule keys,
   undefined profile references, and undefined default.
8. Implement `Filter.IsAllowed()` (allow/deny, exempt introspection
   tools) and `newDenyAllFilter()` for fail-closed.
9. Unit tests: resolution (exact match, order-independence,
   fail-closed on no-match / ambiguous-match, default fallback) and
   filtering (allow, deny, wildcards, `plugin_reload` gated,
   introspection tools exempt).

#### Phase 3: Validation Tooling

10. Add `wtmcpctl profile check` — offline validation with error
    reporting. Include `--with-plugins` flag for plugin name checks.
11. Add `wtmcpctl profile list` — summary of defined profiles.
12. Add `wtmcpctl profile test` — test identity-to-profile matching.
13. Implement `profile.Validate()` and `profile.ValidatePluginRefs()`
    functions used by both the server startup path and `wtmcpctl`.
14. Add profile validation to `wtmcp check` (server-side diagnostic).

#### Phase 4: TLS Infrastructure

15. Add `buildServerTLS()` to `transport.go`; have `listenHTTP` own the
    `http.Server` and mount the mcp-go handler (preserve graceful
    shutdown of both).
16. Add config validation: profiles + streamable-http ⇒ `client_auth`
    must be `require` (fatal otherwise).
17. Wire `WithHTTPContextFunc` (HTTP) and `WithStdioContextFunc`
    (stdio) for identity → filter injection.
18. Integration test: mTLS handshake with client cert → identity
    extraction; no-cert connection is refused under `require`.

#### Phase 5: Server-Side Filtering

19. Add the single `WithToolFilter` to `MCPServer` creation in
    `server.New()` (covers both list and call); export
    `ToolOwnerMap.Owner`.
20. Filter `tool_search` results by profile.
21. Add `--profile` CLI flag for stdio transport.
22. End-to-end tests: connect with different certs, verify different
    tool lists AND that a forbidden `tools/call` is rejected.

#### Phase 6: Observability & Polish

23. Audit the per-connection profile assignment (matched identity →
    profile). Optionally attach `Hooks.OnError` to record denied calls.
24. Add `profile_info` management tool (shows current profile name
    and allowed tool count).
25. Update `wtmcpctl agent enable` to support `--profile` flag.

### 9. File Changes Summary

| File | Change |
|------|--------|
| `internal/config/config.go` | Add `ProfilesConfig`, `ServerTLSConfig`, `ProfileFile`, `ProfileLoadResult` structs |
| `internal/config/profiles.go` | New: `LoadProfiles()`, `ResolveProfilesDir()`, duplicate detection |
| `internal/config/env.go` | Add `ProfilesDir` to `StandardPaths` |
| `internal/profile/identity.go` | New: `Identity` struct, `ExtractIdentity` from x509 cert |
| `internal/profile/resolver.go` | New: `Resolver` — rule compilation, identity-to-profile matching |
| `internal/profile/filter.go` | New: `Filter` — allow/deny evaluation with compiled regexps |
| `internal/profile/context.go` | New: context key helpers (`WithFilter`, `FilterFromContext`) |
| `internal/profile/validate.go` | New: `Validate()`, `ValidatePluginRefs()` — comprehensive checks |
| `internal/transport/transport.go` | Add TLS config, `buildServerTLS`, `WithHTTPContextFunc` wiring |
| `internal/server/server.go` | Add single `WithToolFilter` to `New()`; export `ToolOwnerMap.Owner` |
| `internal/server/discovery.go` | Filter `tool_search` results by profile |
| `internal/audit/audit.go` | Log per-connection profile assignment (and optionally denied calls via `Hooks.OnError`) |
| `cmd/wtmcp/main.go` | Add `--profile` flag, load profiles.d/, create `Resolver` |
| `cmd/wtmcpctl/profile.go` | New: `profile check`, `profile list`, `profile test` subcommands |
| `cmd/wtmcpctl/main.go` | Register `profileCmd` |
| `cmd/wtmcpctl/agent.go` | Add `--profile` flag to `agent enable` |

## Alternatives Considered

### Alternative A: HTTP Middleware JSON-RPC Interception

**Approach:** Wrap the `StreamableHTTPServer` handler with HTTP
middleware that intercepts JSON-RPC request/response bodies. Filter
`tools/list` responses by rewriting the JSON body before it reaches
the client. Block `tools/call` by parsing the request body and
rejecting disallowed tool names.

**Pros:**
- No dependency on mcp-go internal APIs.
- Works even if mcp-go lacks hook support.

**Cons:**
- Requires parsing and rewriting JSON-RPC in SSE `data:` frames.
- Fragile — coupled to mcp-go's wire format and pagination behavior.
- Must buffer and rewrite response bodies (breaks streaming).
- Complex `filteringResponseWriter` implementation.

**Verdict:** Unnecessary now that mcp-go provides `WithToolFilter`
(verified to enforce on both `tools/list` and `tools/call`). This
upstream API handles SSE framing, pagination, deferred tools, and
session lifecycle correctly with zero response rewriting.

### Alternative B: Per-Profile MCPServer Instances

**Approach:** Create a separate `MCPServer` instance for each profile,
registering only the allowed tools on each. Route incoming connections
to the correct instance based on identity.

**Pros:**
- No filter hooks needed — tools are simply not registered.
- Complete isolation between profiles.
- Simple mental model.

**Cons:**
- Memory overhead — each MCPServer duplicates tool registrations.
- Plugin reload must propagate to all instances.
- Management tools (plugin_reload, tool_stats) would need cross-instance
  coordination.
- Doesn't scale well with many profiles.
- `tool_search` index must be maintained per-profile.

**Verdict:** Too complex for the benefit. Would work for 2-3 profiles
but not as a general solution.

### Alternative C: Proxy Architecture (Separate Process)

**Approach:** Run a TLS-terminating reverse proxy in front of wtmcp.
The proxy handles identity extraction and tool filtering, forwarding
allowed requests to the wtmcp stdio or HTTP backend.

**Pros:**
- Zero changes to wtmcp core.
- Separation of concerns (auth vs. tool serving).
- Can be implemented in any language.

**Cons:**
- Extra operational complexity (two processes).
- Latency overhead.
- Configuration split across two systems.
- Harder to integrate with wtmcp's plugin reload notifications.

**Verdict:** Good for production deployments that already have a proxy
layer, but too heavyweight as the default solution.

### Alternative D: Static CLI-Only Profiles (No TLS)

**Approach:** Use only `--profile <name>` CLI flag. No TLS, no
per-connection identity. Each agent process launches wtmcp with its
own profile.

**Pros:**
- Simplest implementation.
- Works with stdio transport.
- No TLS infrastructure needed.

**Cons:**
- No dynamic identity — requires separate wtmcp process per agent.
- No mutual authentication.
- Doesn't address the multi-agent shared server use case.

**Verdict:** Useful as a building block. The full design subsumes
this as the stdio-transport case (`--profile` flag).

### Alternative E: Single-File Profile Configuration

**Approach:** Define all profiles, rules, and definitions in a single
`profiles:` section within `config.yaml`.

```yaml
# All in config.yaml
profiles:
  default: "full-access"
  definitions:
    code-review: { ... }
    ci-automation: { ... }
  rules:
    - match: { ... }
      profile: code-review
```

**Pros:**
- Simpler implementation — no directory scanning or merging logic.
- Single source of truth, no file ordering concerns.

**Cons:**
- Monolithic — adding a profile for a new agent means editing the
  shared config file, risking merge conflicts or accidental breakage.
- Poor operational fit — teams managing different agents cannot
  independently drop in their profile without coordinating on a
  single file.
- No per-agent ownership — cannot tell which file/team owns which
  profile definition.
- Doesn't follow the existing `env.d/` convention.

**Verdict:** Too rigid for multi-team / multi-agent environments.
The `profiles.d/` approach lets each agent owner manage their own
file independently, mirroring how `env.d/` works for credentials.

### Alternative F: Per-Session Tool Registration (`AddSessionTools`)

**Approach:** Use mcp-go's `AddSessionTools`/`DeleteSessionTools` API
to register tools scoped to individual sessions. On session creation,
compute the allowed tools and register them.

**Pros:**
- Most granular — tools truly scoped to a session.
- No global tool list leakage.

**Cons:**
- Requires duplicating tool handler closures per session.
- More complex session lifecycle management.
- Must hook into `OnRegisterSession` and track session cleanup.
- Plugin reload must update all session tool sets.

**Verdict:** Over-engineered. `WithToolFilter` achieves the same
visibility filtering with far less complexity and no per-session state.

## Security Considerations

1. **Single enforcement point** — one `WithToolFilter` governs both
   `tools/list` (hiding) and `tools/call` (mcp-go rejects a forbidden
   call before the handler runs). There is no gap between "hidden" and
   "callable", and no second layer to keep in sync.

2. **Fail closed** — once any profile exists, an identity that matches
   no rule (or matches two profiles) receives *no* tools unless the
   operator explicitly opts into fail-open by setting `default`. The
   secure posture is the default one, not something you must remember
   to configure.

3. **Verified mTLS identity only** — config validation requires
   `client_auth: require` whenever profiles are configured, so a client
   cannot connect without a CA-verified certificate. Identity cannot be
   spoofed by simply omitting a cert (which under weaker settings would
   fall through to the default profile).

4. **Mutating control tools are gated** — only read-only introspection
   (`plugin_list`, `tool_stats`, `tool_search`) is exempt from
   filtering. `plugin_reload` obeys the profile like any other tool, so
   a restricted agent cannot reload/mutate server state.

5. **Deny takes precedence** within a profile — prevents accidental
   exposure when broad `allow` patterns are used.

6. **Exact identity matching** — rule match values are literal strings,
   not regexps, eliminating the unescaped-`.` widening footgun. Tool
   `allow`/`deny` patterns are regexps but compiled **anchored**
   (`^...$`) so a short pattern cannot match a longer tool name.

7. **No hierarchy — error on collision** — duplicate profile names and
   duplicate rule keys are fatal; the server refuses to start. There is
   no silent override or merge that could quietly widen access. The
   trade-off is availability: one bad drop-in file aborts startup
   (fail-closed for correctness), so `wtmcpctl profile check` should be
   run before deploying a new file.

8. **Backward compatible** — with no `profiles.d/` present, filtering is
   entirely inert and behavior is identical to today.

9. **Certificate revocation** — CRL/OCSP checking is out of scope for
   the initial implementation but should be planned; a revoked-but-
   unexpired cert would still match its rule until then.

10. **Audit trail** — the per-connection profile assignment (matched
    identity → profile) is logged. Denied calls surface as mcp-go
    tool-not-found errors and can additionally be recorded via
    `Hooks.OnError` if explicit denial auditing is required.

## Open Questions

1. **Should profiles filter resources too?** Currently the design
   only filters tools. MCP resources (context files, plugin-provided
   resources) could also be sensitive. Recommendation: defer to a
   follow-up issue.

2. **Should `tool_search` respect profiles?** Yes — the proposed
   design filters `tool_search` results. An agent should not discover
   tools it cannot call.

3. **Hot-reload of profiles?** If `profiles.d/` files change, should
   profiles update without restart? Recommendation: yes, via the
   existing `control/commands/` reload mechanism. A `reload-profiles`
   control command would re-scan `profiles.d/`, rebuild the
   `Resolver`, and apply to subsequent connections. Existing sessions
   would keep their current profile until reconnection.

4. **Multiple identity matches?** *(Decided — kept restrictive.)* Rule
   matching is order-independent: each rule is one exact `(field,
   value)` pair, duplicate keys are fatal, and an identity that resolves
   to two different profiles is denied (fail closed). There is
   deliberately no priority/weighting/most-specific scheme and no
   dependence on filename order — a dropped-in file therefore cannot
   silently re-order or shadow another file's rules to widen access. If
   real deployments later need overlap, revisit with an explicit,
   validated precedence rather than positional ordering.

5. **Profile inheritance?** Should profiles compose (e.g.,
   `ci-automation` extends `code-review`)? Recommendation: not in
   v1. Keep it flat. Users can duplicate entries or use broad
   regexps.

6. **Claude Code TLS configuration?** Claude Code supports custom CA
   certificates and network configuration (see
   [Claude Code docs](https://code.claude.com/docs/en/network-config)).
   Client certificate configuration for mTLS would need to be
   documented (env vars `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`,
   or claude settings).

7. **File permissions on profiles.d/?** Should we enforce strict
   permissions (0700/0600) on the directory and files like `env.d/`
   does? Profile files don't contain secrets, but they are security
   policy. Recommendation: warn on group/other-writable permissions
   but don't refuse to load (unlike `env.d/` which contains
   credentials).
