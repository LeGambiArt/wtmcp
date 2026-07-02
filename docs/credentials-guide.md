# Credential Management Guide

wtmcp stores credentials used by plugins (API tokens, URLs, OAuth
secrets) in the OS-native keyring rather than plaintext files. This
guide covers setup, migration, CLI usage, and the security model.

## Why Use the Keyring?

Traditional `env.d/*.env` files store credentials as plaintext on
disk. Any process running as your user can read them. The keyring
approach offers several advantages:

| Feature | env.d (legacy) | Keyring |
|---------|----------------|---------|
| Storage | Plaintext files | OS-encrypted store |
| Access control | File permissions (0600) | Desktop session + file permissions |
| AI agent isolation | File readable by user | Keyring access not exposed to MCP |
| OAuth tokens | Plaintext JSON | AES-256-GCM encrypted files + keyring |
| Backup | Manual file copies | Encrypted, password-protected archives |
| Rotation | Edit files manually | CLI with confirmation and reload |

Existing `env.d` files continue to work. Migration is opt-in and
can be done one credential group at a time.

## Platform Setup

### Linux (Secret Service API)

wtmcp uses the [Secret Service API](https://specifications.freedesktop.org/secret-service/latest/)
via D-Bus. Most desktop environments provide a compatible backend:

- **GNOME**: GNOME Keyring (installed by default)
- **KDE**: KWallet (with `kwalletd` and Secret Service integration)
- **Other**: Install `gnome-keyring` or another Secret Service provider

Verify that a keyring is available:

```bash
# Check for Secret Service
secret-tool store --label='wtmcp-test' service wtmcp.test user test <<< "hello"
secret-tool lookup service wtmcp.test user test
secret-tool clear service wtmcp.test user test
```

If this works, your keyring is ready.

**Headless servers and containers**: No desktop session means no
keyring. wtmcp falls back to `env.d` files and environment variables
automatically. See [CI/CD Integration](#cicd-integration) below.

### macOS (Keychain)

macOS Keychain works out of the box. No additional setup is needed.
Credentials are stored in the login keychain, protected by your
user password.

You may see a system prompt the first time wtmcp accesses the
keychain. Grant access to allow credential storage.

### Windows

Windows is not currently supported. Keyring integration with the
Windows Credential Manager is planned for a future release. In the
meantime, use `env.d` files for credential storage on Windows.

## Quick Start

```bash
# Check system status
wtmcpctl credentials show-config

# Set a credential directly in the keyring
wtmcpctl credentials set jira JIRA_TOKEN --keyring

# Or migrate existing env.d credentials
wtmcpctl credentials migrate jira

# Verify credentials resolve correctly
wtmcpctl credentials test jira
```

## Migration from env.d to Keyring

### Before You Begin

1. Make sure your system keyring is available (see [Platform Setup](#platform-setup))
2. Identify which credential groups to migrate:
   ```bash
   wtmcpctl credentials list
   ```
3. Back up your env.d files (the migration tool offers this automatically)

### Step-by-Step Migration

Migrate one credential group at a time. For example, migrating `jira`:

```bash
wtmcpctl credentials migrate jira
```

The command walks you through interactively:

```
Reading credentials from env.d/jira.env
Found 3 keys: JIRA_EMAIL, JIRA_TOKEN, JIRA_URL

Create backup before migration? [Y/n] y
Enter backup password: [hidden]
Backup created: ~/.config/wtmcp/credentials/backup-20260429-103000.enc

Storing credentials in keyring...
  + JIRA_EMAIL
  + JIRA_TOKEN
  + JIRA_URL

Marked 'jira' as migrated in .migration.yaml

Delete env.d/jira.env? [y/N] n
Migration complete. You can delete env.d/jira.env manually when ready.
```

### Migration Options

```bash
# Preview what would happen without making changes
wtmcpctl credentials migrate jira --dry-run

# Automatically delete env.d file after migration
wtmcpctl credentials migrate jira --delete-env-d

# Skip plugin validation after migration
wtmcpctl credentials migrate jira --skip-validation
```

### Rollback

If something goes wrong, restore from the pre-migration backup:

```bash
wtmcpctl credentials restore backup-20260429-103000.enc
```

The env.d file is not deleted by default, so you can also revert by
unmarking the group in `~/.config/wtmcp/credentials/.migration.yaml`.

## CLI Command Reference

All commands are subcommands of `wtmcpctl credentials`.

### list

Show credential groups or keys within a group.

```bash
# List all credential groups with status
wtmcpctl credentials list

# List keys in a specific group
wtmcpctl credentials list jira

# Plain text output (no colors or borders)
wtmcpctl credentials list --plain
```

**Example output (all groups):**

```
Credential Groups:
  google         migrated        3 keys in keyring
  jira           not migrated    3 keys in env.d
  confluence     not migrated    2 keys in env.d
```

**Example output (single group):**

```
Credential Group: jira [not migrated]

  Key             Source
  JIRA_EMAIL      env.d
  JIRA_TOKEN      env.d
  JIRA_URL        env.d
```

### get

Retrieve a credential value.

```bash
# Get credential (value masked by default)
wtmcpctl credentials get jira JIRA_TOKEN

# Show the full value
wtmcpctl credentials get jira JIRA_TOKEN --show-value

# Show only the source (keyring, env.d, etc.)
wtmcpctl credentials get jira JIRA_TOKEN --source-only
```

**Example output:**

```
JIRA_TOKEN
  Value:  abc1...
  Source: keyring
```

### set

Store a credential value.

```bash
# Set with inline value
wtmcpctl credentials set jira JIRA_URL https://jira.example.com

# Set interactively (hidden input, recommended for secrets)
wtmcpctl credentials set jira JIRA_TOKEN

# Force storage in keyring even if group is not migrated
wtmcpctl credentials set jira JIRA_TOKEN --keyring
```

When the value is omitted, you are prompted to enter it with hidden
input so the secret does not appear in your shell history.

### delete

Remove a credential from keyring and/or env.d.

```bash
# Delete from both keyring and env.d (prompts for confirmation)
wtmcpctl credentials delete jira JIRA_TOKEN

# Delete without confirmation prompt
wtmcpctl credentials delete jira JIRA_TOKEN --force

# Delete only from keyring
wtmcpctl credentials delete jira JIRA_TOKEN --keyring-only

# Delete only from env.d
wtmcpctl credentials delete jira JIRA_TOKEN --env-d-only
```

### migrate

Migrate credentials from env.d files to the OS keyring.

```bash
# Interactive migration with backup prompt
wtmcpctl credentials migrate jira

# Preview without making changes
wtmcpctl credentials migrate jira --dry-run

# Automatically delete env.d file after migration
wtmcpctl credentials migrate jira --delete-env-d

# Skip validation tool check
wtmcpctl credentials migrate jira --skip-validation
```

The migration is atomic: all keys are stored in the keyring before
the group is marked as migrated. If any keyring write fails, all
changes are rolled back.

### export

Export credentials to a file or stdout.

```bash
# Export to default env.d location
wtmcpctl credentials export jira

# Export to a custom file
wtmcpctl credentials export jira /tmp/jira-creds.env

# Export to stdout as JSON
wtmcpctl credentials export jira --stdout --format=json

# Export as YAML
wtmcpctl credentials export jira --stdout --format=yaml
```

**Supported formats:** `env` (default), `json`, `yaml`

Files are created with 0600 permissions.

### import

Import credentials from a file.

```bash
# Import and mark group as migrated
wtmcpctl credentials import jira jira-creds.json --mark-migrated

# Import with explicit format
wtmcpctl credentials import jira creds.txt --format=env

# Overwrite existing keys without prompting
wtmcpctl credentials import jira creds.json --overwrite
```

The file format is auto-detected from the extension (`.json`,
`.yaml`/`.yml`, everything else as `env`), or you can specify it
explicitly with `--format`.

The group must be migrated (or `--mark-migrated` must be set) for
import to store credentials in the keyring.

### test

Verify that credentials can be resolved for a group.

```bash
wtmcpctl credentials test jira
```

**Example output:**

```
Found 3 credentials for 'jira':
  JIRA_EMAIL = user...
  JIRA_TOKEN = abc1...
  JIRA_URL = http...
```

This checks that credentials exist and can be loaded from their
configured source (keyring, env.d, or environment variables). Values
are shown masked.

### rotate

Update a credential value with confirmation.

```bash
wtmcpctl credentials rotate jira JIRA_TOKEN
```

**Interactive flow:**

```
Enter new value for JIRA_TOKEN: [hidden]
Confirm new value: [hidden]
Rotated jira/JIRA_TOKEN
```

The credential must already exist. The new value is entered twice to
prevent typos. If the group is migrated, the value is updated in the
keyring; otherwise it is updated in the env.d file.

### backup

Create an encrypted backup of all keyring credentials.

```bash
# Backup to default location
wtmcpctl credentials backup

# Backup to a specific file
wtmcpctl credentials backup /path/to/backup.enc
```

**Interactive flow:**

```
Enter backup password: [hidden]
Confirm password: [hidden]
Backup created: ~/.config/wtmcp/credentials/backup-20260429-103000.enc
```

Backups are encrypted with AES-256-GCM using a key derived from your
password via Argon2id. Keep the backup file and password in a secure
location.

**Default path:** `~/.config/wtmcp/credentials/backup-<timestamp>.enc`

### restore

Restore credentials from an encrypted backup.

```bash
# Restore with merge (only import missing keys, default)
wtmcpctl credentials restore backup-20260429-103000.enc

# Restore and overwrite existing keys
wtmcpctl credentials restore backup-20260429-103000.enc --overwrite
```

**Interactive flow:**

```
Enter backup password: [hidden]
Credentials restored from backup (mode: merge)
```

By default, existing keys are not overwritten (merge mode). Use
`--overwrite` to replace all keys with the backup values.

### show-config

Display credential system status.

```bash
wtmcpctl credentials show-config
```

**Example output:**

```
Keyring: Available
env.d directory: /home/user/.config/wtmcp/env.d

Credential Groups:
  google [migrated] - 3 keys (3 in keyring)
  jira [not migrated] - 3 keys (3 in env.d)
  confluence [not migrated] - 2 keys (2 in env.d)
```

## Security Model

### Credential Resolution Order

When a plugin needs a credential like `${JIRA_TOKEN}`, wtmcp
resolves it through a precedence chain that depends on the group's
migration status.

**Migrated groups** (keyring-first):

1. Keyring (`service="wtmcp.<group>"`, `user="<key>"`)
2. Environment variable (CI/CD fallback)

**Non-migrated groups** (env.d-first):

1. `env.d/<group>.env` file
2. Keyring (supports partial migration)
3. Environment variable

Resolved values are cached in memory for 15 minutes. OAuth access
tokens are never cached (always read fresh from the keyring).

### Encryption

**OAuth token files** use AES-256-GCM authenticated encryption:

- Access tokens are stored directly in the keyring
- Refresh tokens and metadata are encrypted on disk as
  `token-<plugin>.json.enc`
- The encryption key is a 256-bit random key stored in the keyring
  (one per credential group)

**Backup files** use AES-256-GCM with password-based key derivation:

- Argon2id parameters: memory=64MB, iterations=3, parallelism=4
- Unique 16-byte salt per backup
- Protects against brute-force attacks on the backup password

### Credential Isolation

AI agents communicating via MCP never see credentials. The isolation
works at multiple levels:

```
AI Agent
  | (MCP protocol - no credentials visible)
  v
wtmcp Core (Go)
  | (credentials resolved internally)
  |-- Keyring / env.d --> credential values
  |-- Config resolver ---> plugin process env vars (set at startup)
  \-- HTTP proxy ---------> auth header injection
  |
  v
Plugin Process
  | has credentials in its process environment (env vars)
  | but does NOT access the keyring directly
  | (sends HTTP requests without auth headers)
  v
HTTP Proxy (Go core injects auth headers)
  |
  v
External API
```

**Security boundary:** Plugins receive credential values as
environment variables in their process environment at startup. This
means credentials live in the plugin's memory space but are never
exposed over the MCP protocol channel -- the AI agent cannot read
them. Plugins do not have direct access to the keyring or the
credential resolution mechanism.

The HTTP proxy adds authentication headers transparently, so plugins
send plain (unauthenticated) HTTP requests which the proxy enriches
before forwarding to external APIs.

### Threat Model

**Mitigated threats:**

- Plaintext credential exposure on disk (credentials in encrypted
  keyring, not env.d files)
- Filesystem compromise (encrypted token files are unreadable
  without keyring access)
- AI agent credential access (agents never see credential storage)

**Out of scope (OS-level limitations):**

- Root compromise (root can access the keyring)
- Keyring-aware malware (malware with keyring access can read
  credentials)
- Physical access to an unlocked machine

### File Permissions

All sensitive files are created with restrictive permissions:

| File | Permissions | Contents |
|------|-------------|----------|
| `env.d/*.env` | 0600 | Legacy plaintext credentials |
| `.migration.yaml` | 0600 | Migration state |
| `token-*.json.enc` | 0600 | Encrypted OAuth tokens |
| `backup-*.enc` | 0600 | Encrypted credential backups |
| `credentials/` directory | 0700 | Credential files directory |

## CI/CD Integration

In headless environments (CI pipelines, containers, SSH sessions
without desktop forwarding), no system keyring is available. wtmcp
handles this automatically:

1. On startup, wtmcp checks keyring availability
2. If unavailable, it logs a warning and falls back to environment
   variables and env.d files
3. All read operations work normally through the fallback chain
4. Write operations (`set`, `migrate`, `rotate`) return a clear error

### Recommended CI/CD Setup

Set credentials as environment variables in your CI system:

```yaml
# GitHub Actions example
env:
  JIRA_URL: ${{ secrets.JIRA_URL }}
  JIRA_TOKEN: ${{ secrets.JIRA_TOKEN }}
  JIRA_EMAIL: ${{ secrets.JIRA_EMAIL }}
```

```yaml
# GitLab CI example
variables:
  JIRA_URL: $JIRA_URL
  JIRA_TOKEN: $JIRA_TOKEN
```

wtmcp resolves these automatically when the keyring is unavailable,
even for migrated groups.

### Verifying CI/CD Fallback

```bash
# Check that credentials resolve via environment variables
wtmcpctl credentials show-config
# Should show: Keyring: Not available

wtmcpctl credentials test jira
# Should find credentials from environment variables
```

## Troubleshooting

### Keyring not available

**Symptom:** `wtmcpctl credentials show-config` reports
"Keyring: Not available"

**Cause:** No Secret Service provider is running (Linux) or Keychain
is inaccessible (macOS).

**Fix (Linux):**

```bash
# Install GNOME Keyring
sudo apt install gnome-keyring    # Debian/Ubuntu
sudo dnf install gnome-keyring    # Fedora

# Start the keyring daemon (usually automatic in desktop sessions)
eval $(gnome-keyring-daemon --start --components=secrets)
```

**Fix (macOS):** Ensure you are logged in to a desktop session.
If running via SSH, use `security unlock-keychain` to unlock.

### Permission denied accessing keyring

**Symptom:** "Keyring access denied" or
"org.freedesktop.Secret.Collection.Locked"

**Cause:** The keyring is locked (common after screen lock or SSH).

**Fix:**

```bash
# Unlock the keyring by storing a test secret
secret-tool store --label='wtmcp-unlock' service wtmcp.unlock user test
# Enter your keyring password when prompted
```

### Migration fails with "keyring is not available"

**Symptom:** `wtmcpctl credentials migrate` fails.

**Cause:** Cannot write to the keyring. Common in containers, SSH
sessions, or headless servers.

**Fix:** Migration requires a working keyring. Either:

- Run the migration on a machine with a desktop session
- Use `wtmcpctl credentials import` with `--mark-migrated` on the
  target system (requires keyring there)
- Keep using env.d files in headless environments

### Credentials not found after migration

**Symptom:** Plugin fails to load after migration.

**Cause:** The keyring was locked or a different keyring session was
active during migration.

**Fix:**

```bash
# Check what the credential service sees
wtmcpctl credentials list jira
wtmcpctl credentials test jira

# If empty, restore from backup
wtmcpctl credentials restore backup-pre-migration-jira-*.enc
```

### Wrong format when importing

**Symptom:** `wtmcpctl credentials import` fails to parse the file.

**Cause:** Format auto-detection guessed wrong, or the file has an
unexpected structure.

**Fix:** Specify the format explicitly:

```bash
wtmcpctl credentials import jira creds.txt --format=env
wtmcpctl credentials import jira creds.txt --format=json
```

### Backup password forgotten

**Symptom:** Cannot restore from backup.

**Cause:** Argon2id encryption is designed to be irreversible without
the password.

**Fix:** There is no recovery mechanism for forgotten backup
passwords. If you still have access to the keyring, create a new
backup with a password you will remember. If credentials are lost,
re-create them from their original sources (API consoles, etc.).

## Data Files Reference

### Migration State

**Path:** `~/.config/wtmcp/credentials/.migration.yaml`

Tracks which credential groups have been migrated to the keyring:

```yaml
version: 1
migrated_groups:
  - google
  - jira
last_updated: "2026-04-29T10:30:00Z"
```

### Keyring Entry Format

Credentials are stored in the system keyring using:

- **Service name:** `wtmcp.<credential_group>` (e.g., `wtmcp.jira`)
- **User/account:** The key name (e.g., `JIRA_TOKEN`)

Special entries (prefixed with `__`):

| User | Purpose |
|------|---------|
| `__oauth_access_<plugin>` | OAuth access token |
| `__encryption_key` | Token file encryption key |

### Encrypted Token Files

**Path:** `~/.config/wtmcp/credentials/<group>/token-<plugin>.json.enc`

These contain OAuth refresh tokens and metadata, encrypted with
AES-256-GCM. The encryption key is stored in the keyring.

### Backup Files

**Path:** `~/.config/wtmcp/credentials/backup-<timestamp>.enc`

Password-encrypted archives containing all keyring credentials and
migration state. Encrypted with AES-256-GCM using an Argon2id-derived
key.
