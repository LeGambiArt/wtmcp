# Agent Profiles Guide

Agent profiles restrict **which tools an AI agent can discover and
call**, based on the agent's identity. Without profiles, every client
connected to wtmcp sees every tool from every plugin. With profiles, a
read-only code-review agent can be limited to `gitlab_get_*` and
`jira_get_issue`, while a CI agent gets a different set — all from the
same wtmcp instance.

- **Streamable-HTTP:** agents are identified by their **TLS client
  certificate** (mTLS). The certificate's CN or SAN maps to a profile.
- **Stdio:** a single agent selects its profile with the `--profile`
  flag, or via `profiles.default` in `config.yaml` when the flag is
  omitted.

Profiles are **fail-closed on streamable-HTTP**: once any profile is
configured, an agent that matches no rule gets *no* tools unless you
explicitly set a permissive default. With no profiles configured at all,
behavior is unchanged (all tools visible) — the feature is fully backward
compatible.

> **Stdio is different.** The stdio transport has no certificate identity,
> so it selects a profile from the `--profile` flag, falling back to
> `profiles.default` from `config.yaml` when the flag is omitted. If
> neither is set, stdio is **fail-open**: all tools are visible. Profiles
> restrict a stdio session only when you pass `--profile` or configure
> `profiles.default`. Do not rely on profiles as an access boundary on
> stdio; the fail-closed guarantee applies to streamable-HTTP (mTLS)
> only. See [Stdio transport](#stdio-transport).

> Design rationale and alternatives considered: see
> [docs/design/profiles.md](design/profiles.md).

## Contents

- [Quick start](#quick-start)
- [Directory layout](#directory-layout)
- [Profile file format](#profile-file-format)
- [Identity matching rules](#identity-matching-rules)
- [Allow / deny evaluation](#allow--deny-evaluation)
- [Resolution and fail-closed behavior](#resolution-and-fail-closed-behavior)
- [Server TLS (mTLS) configuration](#server-tls-mtls-configuration)
- [Stdio transport](#stdio-transport)
- [Validation and debugging (`wtmcpctl profile`)](#validation-and-debugging-wtmcpctl-profile)
- [Security notes](#security-notes)
- [Limitations](#limitations)

## Quick start

1. Create a profile file:

   ```bash
   mkdir -p ~/.config/wtmcp/profiles.d
   cat > ~/.config/wtmcp/profiles.d/code-review.yaml <<'EOF'
   definitions:
     code-review:
       description: "Read-only access for code review agents"
       allow:
         gitlab:
           - "gitlab_get_.*"
           - "gitlab_list_.*"
         jira:
           - "jira_get_issue"
   rules:
     - match: { cn: "code-review-bot" }
       profile: code-review
   EOF
   ```

2. Validate it:

   ```bash
   wtmcpctl profile check
   ```

3. **Stdio:** apply the profile with a flag:

   ```bash
   wtmcp --profile code-review
   ```

   **Streamable-HTTP:** configure mTLS (see below) and connect with a
   client certificate whose CN is `code-review-bot`.

## Directory layout

Profiles use a **`profiles.d/` drop-in directory**, mirroring the
`env.d/` convention for credentials. Each YAML file defines one or more
profiles and the rules that select them, so different teams can add a
profile by dropping in a file without editing shared config.

```
~/.config/wtmcp/
├── config.yaml                  # server, plugins, TLS, profiles.default
├── env.d/                       # credentials (existing)
└── profiles.d/                  # profiles (this feature)
    ├── code-review.yaml
    ├── ci-automation.yaml
    └── full-access.yaml
```

The directory defaults to `{workdir}/profiles.d`. Override it in
`config.yaml`:

```yaml
profiles:
  dir: /etc/wtmcp/profiles.d     # optional (default: {workdir}/profiles.d)
  default: "read-only"           # optional fallback; omit to fail closed
```

## Profile file format

Each file in `profiles.d/` has two top-level keys: `definitions` (what a
profile allows) and `rules` (which identity maps to which profile).

```yaml
# profiles.d/ci-automation.yaml
definitions:
  ci-automation:
    description: "CI/CD pipeline agent"
    allow:
      gitlab:
        - ".*"                    # all gitlab tools ...
      jira:
        - "jira_get_issue"
        - "jira_transition_issue"
    deny:
      gitlab:
        - "gitlab_delete_.*"      # ... except deletes

rules:
  - match: { san_uri: "spiffe://example.com/agent/ci" }
    profile: ci-automation
  - match: { san_email: "ci-bot@example.com" }
    profile: ci-automation
```

- `allow` / `deny` map a **plugin name** (or `"*"` for any plugin) to a
  list of **tool-name regexps**.
- `description` is free text shown in `wtmcpctl profile` output.

## Identity matching rules

Two intentionally strict rules govern matching:

- **Match values are exact strings, not patterns.** A rule matches a
  certificate field by exact equality — this avoids the footgun of an
  unescaped `.` in a regex silently widening the match. To match several
  identities, write several rules.
- **Each rule matches exactly one field** — one of `cn`, `san_uri`,
  `san_dns`, or `san_email`. Matching is therefore **order-independent**:
  a `(field, value)` pair maps to exactly one profile via a set lookup,
  so file/rule order never affects the result. A rule with zero or more
  than one field is a fatal error.

```yaml
rules:
  - match: { cn: "code-review-bot" }                          # by Common Name
    profile: code-review
  - match: { san_uri: "spiffe://example.com/agent/review" }   # by SAN URI
    profile: code-review
  - match: { san_dns: "review.agents.example.com" }           # by SAN DNS
    profile: code-review
  - match: { san_email: "review@example.com" }                # by SAN email
    profile: code-review
```

The same `(field, value)` key used by two rules — even across files — is
a fatal error (it would be ambiguous which profile the identity gets).

## Allow / deny evaluation

For a tool `<tool_name>` owned by plugin `<plugin_name>`:

1. **Allowed** if the plugin (or `"*"`) has an entry in `allow` *and* at
   least one of its regexps matches the tool name. An empty/absent
   `allow` allows nothing.
2. **Denied** if the plugin (or `"*"`) has an entry in `deny` *and* any
   of its regexps matches the tool name.
3. **Deny takes precedence** — a tool must pass allow **and** not match
   deny.

Tool patterns are compiled as **anchored** regexps (`^...$`), so
`gitlab_get` does *not* match `gitlab_get_and_delete`, and
`jira_get_issue` does *not* match `jira_get_issues`.

**Introspection exemption.** The read-only discovery tools `plugin_list`,
`tool_stats`, and `tool_search` are **always visible and callable**,
regardless of profile — they are needed for basic MCP discovery and
cannot change state. Everything else is gated, including the mutating
`plugin_reload` (a restricted profile simply does not list it).

> `tool_search` is exempt as a *tool*, but its *results* are still
> filtered by the profile — an agent never discovers a tool it cannot
> call. This matters because progressive discovery (the default) surfaces
> most tools through `tool_search` rather than `tools/list`.

## Resolution and fail-closed behavior

When a connection is established, its identity resolves to one filter:

| Situation | Result |
|-----------|--------|
| No `profiles.d/`, or it is empty | **No filtering** — all tools visible (unchanged from today) |
| A rule matches | The matched profile's filter |
| No rule matches, `default` is set | The default profile's filter |
| No rule matches, no `default` | **Deny all** (fail closed) — only exempt tools remain |
| Identity matches **two different profiles** | **Deny all** (fail closed) + logged warning |

The design **fails closed once profiles exist**: an agent you did not
account for gets nothing rather than everything. Fail-open is available
only by explicit opt-in — set `profiles.default` to a permissive profile.

## Server TLS (mTLS) configuration

For the streamable-HTTP transport, identity comes from a **verified**
client certificate. Configure TLS in `config.yaml`:

```yaml
server:
  transport: streamable-http
  host: 0.0.0.0
  port: 8443
  tls:
    cert_file: /etc/wtmcp/server.crt
    key_file: /etc/wtmcp/server.key
    ca_file: /etc/wtmcp/ca.crt        # CA that signs client certs
    client_auth: require              # require | request | none

profiles:
  default: "read-only"                # omit for fail-closed
```

`client_auth` values map to Go's TLS client-auth modes:

| Value | Behavior |
|-------|----------|
| `require` | Client **must** present a CA-verified certificate |
| `request` | Certificate optional, verified if presented |
| `none` | No client certificate requested |

> **When any profile is configured on `streamable-http`, `client_auth`
> must be `require`.** Anything weaker would let a client connect with no
> certificate and fall through to the default profile — that is a fatal
> startup error. Combined with fail-closed defaults, an agent must
> present a CA-verified cert matching a rule to get any tools.

## Stdio transport

Stdio is a 1:1 connection with no TLS, so there is no certificate to
extract an identity from. Select the profile with a flag; it is resolved
once at startup and applied to the session:

```bash
wtmcp --profile code-review
```

When the flag is omitted, wtmcp falls back to `profiles.default` from
`config.yaml`, so an agent harness that launches `wtmcp` with no extra
arguments still gets a restricted tool set:

```yaml
# config.yaml
profiles:
  default: "code-review"   # applied on stdio when --profile is omitted
```

Selection precedence on stdio:

- `--profile <name>` — resolved once at startup; overrides the default.
- otherwise `profiles.default` — the configured fallback profile.
- otherwise no filter is applied (all tools visible).

If `--profile` (or `profiles.default`) names a profile that is not
defined, startup fails.

> **Security caveat — stdio is fail-open with no profile selected.**
> Unlike streamable-HTTP, when neither `--profile` nor `profiles.default`
> is set, stdio does **not** deny tools; it shows all of them. This is
> intentional (stdio is a local, single-user, 1:1 channel with no
> identity to enforce against), but it means profiles are not an access
> boundary on stdio. If you need guaranteed restriction, set
> `profiles.default` (or always pass `--profile`), or use the
> streamable-HTTP transport with `client_auth: require`, which is
> fail-closed. Consider `--read-only` as an orthogonal, always-on guard
> for stdio sessions.

## Validation and debugging (`wtmcpctl profile`)

Validate and inspect your configuration offline, without starting the
server. See [README-wtmcpctl.md](../README-wtmcpctl.md#profile) for the
full command reference.

```bash
# Validate profiles.d/ — reports every error and warning at once.
# Exit code 0 = valid, 1 = fatal errors. Add --with-plugins to also
# check that allow/deny plugin names match discovered plugins.
wtmcpctl profile check
wtmcpctl profile check --with-plugins

# List defined profiles with a summary of their allow/deny rules.
wtmcpctl profile list

# Test which profile a given identity would match, and see the
# resulting allowed/denied tool split.
wtmcpctl profile test --cn code-review-bot
wtmcpctl profile test --san-email ci-bot@example.com
```

Run `wtmcpctl profile check` before deploying a new file — a single
invalid drop-in file aborts server startup (fail-closed for
correctness).

## Security notes

- **Single enforcement point** — one filter governs both `tools/list`
  (hiding) and `tools/call` (rejection before the handler runs). There is
  no gap between "hidden" and "callable."
- **Verified identity only** — `client_auth: require` is mandatory with
  profiles on HTTP, so identity cannot be spoofed by omitting a cert.
- **Deny takes precedence** within a profile, guarding against overly
  broad `allow` patterns.
- **Exact identity matching** eliminates the unescaped-`.` regex
  widening footgun; tool patterns are anchored so a short pattern cannot
  match a longer tool name.
- **No hierarchy, error on collision** — duplicate profile names and
  duplicate rule keys are fatal; there is no silent override that could
  quietly widen access.
- **Denied calls** return an ordinary MCP "tool not found," which does
  not confirm the tool exists. The primary audit signal is the
  per-connection profile assignment logged at connect time.
- **Stdio is fail-open with no profile selected** — profiles are an
  access boundary on streamable-HTTP (mTLS) only. On stdio, when neither
  `--profile` nor `profiles.default` is set, all tools are shown; set
  `profiles.default`, always pass `--profile`, or use `--read-only` if
  you need restriction there. See [Stdio transport](#stdio-transport).

## Limitations

- **Tools only** — profiles filter tools, not MCP resources. (Planned as
  a follow-up.)
- **No certificate revocation** — CRL/OCSP checking is out of scope for
  now; a revoked-but-unexpired cert still matches its rule.
- **No profile inheritance** — profiles are flat; duplicate entries or
  use broad regexps instead of composing.
- **Reload on reconnect** — existing sessions keep their profile until
  they reconnect.
