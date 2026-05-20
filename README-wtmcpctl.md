# wtmcpctl

Command-line tool for managing wtmcp: agent configuration, plugin
and provider state, OAuth authentication, credential vault
encryption, and usage statistics.

## Installation

```bash
make build
```

This creates `wtmcpctl` in the repository root.

## Global Flags

| Flag | Description |
|---|---|
| `--workdir <path>` | Override the wtmcp working directory (default: `~/.config/wtmcp`) |
| `--verbose`, `-v` | Show verbose output (discovery logs, etc.) |
| `--version` | Show version information |

## Commands

### agent

Configure wtmcp as an MCP server for AI agents.

```bash
wtmcpctl agent enable <agent>    # Add wtmcp to agent's MCP config
wtmcpctl agent disable <agent>   # Remove wtmcp from agent's MCP config
```

**Supported agents:** `claude` (or `claude-code`), `gemini`, `cursor`

**Flags:**

| Flag | Command | Description |
|---|---|---|
| `--dir`, `-d` | enable, disable | Project directory (default: current directory) |
| `--read-only` | enable | Configure in read-only mode |

**Config file locations:**
- Claude/Claude Code: `<dir>/.mcp.json`
- Gemini: `<dir>/.gemini/settings.json`
- Cursor: `<dir>/.cursor/mcp.json`

**Examples:**

```bash
wtmcpctl agent enable claude            # Enable for Claude in current dir
wtmcpctl agent enable gemini -d ~/proj  # Enable for Gemini in ~/proj
wtmcpctl agent enable cursor --read-only
wtmcpctl agent disable claude
```

---

### oauth

Manage OAuth authentication for plugins that use OAuth2 flows
(Google Calendar, Google Docs, Google Drive, Google Gmail).

```bash
wtmcpctl oauth list                   # Show OAuth plugins and auth status
wtmcpctl oauth auth [name...]         # Authenticate one or more plugins
wtmcpctl oauth credentials <service>  # Interactive credential setup wizard
```

**Flags:**

| Flag | Command | Description |
|---|---|---|
| `--all`, `-a` | auth | Authenticate all discovered OAuth plugins |

**Examples:**

```bash
wtmcpctl oauth list                     # Show all OAuth plugins and status
wtmcpctl oauth auth google-drive        # Authenticate a single plugin
wtmcpctl oauth auth --all              # Authenticate all plugins
wtmcpctl oauth credentials google       # Interactive Google OAuth setup
```

#### OAuth Credentials Setup

The `oauth credentials google` wizard walks through:

1. Creating a Google Cloud project
2. Enabling required APIs (Calendar, Docs, Drive, Gmail)
3. Configuring the OAuth consent screen
4. Creating OAuth client credentials
5. Downloading and installing the credentials JSON

---

### plugin

Manage which plugins are loaded. Supports two modes:
**blocklist** (default -- all plugins loaded except disabled ones)
and **allowlist** (`plugin only` -- only listed plugins loaded).

```bash
wtmcpctl plugin                        # Interactive TUI (multi-select)
wtmcpctl plugin list                   # List plugins and status
wtmcpctl plugin enable <name...>       # Enable plugins
wtmcpctl plugin disable <name...>      # Disable plugins
wtmcpctl plugin only <name...>         # Set allowlist
```

**Flags:**

| Flag | Command | Description |
|---|---|---|
| `--plain`, `-p` | list | Plain text output (no colors or borders) |
| `--clear` | only | Remove allowlist, return to blocklist mode |

**Examples:**

```bash
wtmcpctl plugin                        # Interactive enable/disable
wtmcpctl plugin list                   # Show all plugins and status
wtmcpctl plugin disable confluence     # Disable a plugin
wtmcpctl plugin only jira github       # Load only jira and github
wtmcpctl plugin only --clear           # Remove allowlist
```

---

### provider

Manage auth provider enable/disable state. Providers handle
authentication for plugin HTTP requests (bearer tokens, basic
auth, Kerberos/SPNEGO, OAuth2, refresh tokens).

```bash
wtmcpctl provider list                 # List providers and status
wtmcpctl provider enable <name...>     # Enable providers
wtmcpctl provider disable <name...>    # Disable providers
```

**Flags:**

| Flag | Command | Description |
|---|---|---|
| `--json` | list | JSON output |
| `--plain`, `-p` | list | Plain text output (no colors or borders) |

**Examples:**

```bash
wtmcpctl provider list                 # Show providers, variants, used-by
wtmcpctl provider disable kerberos     # Disable Kerberos auth
wtmcpctl provider enable bearer        # Re-enable bearer auth
```

---

### stats

View tool usage statistics from the last server session.

```bash
wtmcpctl stats                         # Show tool usage summary
wtmcpctl stats config                  # View stats configuration
wtmcpctl stats config set <key> <val>  # Set a config field
wtmcpctl stats reset                   # Clear accumulated stats
```

**Flags:**

| Flag | Command | Description |
|---|---|---|
| `--schemas` | stats | Include tool schema token costs |
| `--resources` | stats | Include resource read stats |
| `--sort`, `-s` | stats | Sort by: `calls`, `tokens`, `errors`, `name` (default: calls) |
| `--plain`, `-p` | stats | Plain text output |
| `--json` | stats | Raw JSON output |
| `--since` | stats | Show stats from date (YYYY-MM-DD) |
| `--until` | stats | Show stats until date (YYYY-MM-DD) |
| `--days`, `-d` | stats | Show stats for last N days (today = 1) |
| `--plugin`, `-P` | stats | Filter to a single plugin |

**Config keys** (for `stats config set`):

| Key | Type | Description |
|---|---|---|
| `enabled` | bool | Enable/disable stats collection |
| `tokenizer` | string | Token counting method (`chars`) |
| `log_calls` | bool | Log individual tool call durations |
| `persist` | bool | Persist stats to disk across sessions |
| `retention_days` | int | Days to retain daily stats |

**Examples:**

```bash
wtmcpctl stats                         # Summary of all tool usage
wtmcpctl stats --sort tokens           # Sort by token consumption
wtmcpctl stats --days 7 --schemas      # Last week with schema costs
wtmcpctl stats --plugin jira --json    # Jira stats as JSON
wtmcpctl stats config set persist true # Enable persistence
wtmcpctl stats reset                   # Clear all stats
```

---

### vault

Encrypt and decrypt files using Ansible Vault format. Compatible
with `ansible-vault` -- encrypted files can be used interchangeably.

```bash
wtmcpctl vault encrypt <file...>       # Encrypt files
wtmcpctl vault decrypt <file...>       # Decrypt files
wtmcpctl vault view <file>             # View decrypted content to stdout
```

**Persistent flags** (available on all vault subcommands):

| Flag | Description |
|---|---|
| `--vault-password-file <path>` | Read vault password from file |
| `--ask-vault-pass` | Prompt for vault password interactively |

**Flags:**

| Flag | Command | Description |
|---|---|---|
| `--vault-id <label>` | encrypt | Vault ID label (uses Vault 1.2 format) |
| `--check` | decrypt | Verify decryption without modifying the file |

**Password resolution priority:**

1. `--vault-password-file` flag
2. `WTMCP_VAULT_PASSWORD` environment variable
3. Config file `secrets.vault_password_file`
4. Interactive terminal prompt

**Examples:**

```bash
wtmcpctl vault encrypt env.d/jira.env
wtmcpctl vault encrypt env.d/*.env --vault-id prod
wtmcpctl vault decrypt env.d/jira.env --check
wtmcpctl vault view env.d/jira.env
wtmcpctl vault encrypt credentials/jira/client-credentials.json \
  --vault-password-file ~/.vault_pass
```

---

### version

```bash
wtmcpctl version                       # Show version and build date
```

## Environment Variables

| Variable | Description |
|---|---|
| `WTMCP_VAULT_PASSWORD` | Vault password (cleared from env after first read) |
| `GOOGLE_CREDENTIALS_DIR` | Override Google OAuth credentials directory |

## See Also

- [wtmcp README](README.md) -- main server documentation
