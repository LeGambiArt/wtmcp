# wtmcpctl - Plugin Management CLI

`wtmcpctl` is a command-line utility for managing OAuth authentication for wtmcp plugins.

## Quick Start

```bash
# Build the tool
make build

# List OAuth plugins and their authentication status
./wtmcpctl oauth list

# Authenticate a single plugin
./wtmcpctl oauth auth google-drive

# Authenticate multiple plugins
./wtmcpctl oauth auth google-drive google-calendar

# Authenticate all non-authenticated plugins
./wtmcpctl oauth auth --all

# Use a custom working directory
./wtmcpctl --workdir /path/to/workdir oauth list
```

## Features

- **Automatic plugin discovery**: Discovers OAuth-enabled plugins by reading their `plugin.yaml` manifests
- **OAuth2 flow automation**: Opens your browser for authorization and handles the callback automatically
- **Token management**: Saves access and refresh tokens to the location specified in each plugin's manifest
- **Status checking**: View authentication status for all OAuth-enabled plugins
- **Custom workdir support**: Use `--workdir` to manage plugins in different directories

## Commands

### oauth list

Lists all OAuth-enabled plugins and their authentication status.

```bash
wtmcpctl oauth list
```

**Output:**
```
OAuth Plugin Status:

  ✓  google-drive          authenticated (valid)
  ✓  google-calendar       authenticated (needs refresh)
  ✗  google-gmail          not authenticated

Credentials directory: /home/user/.config/wtmcp/credentials/google
```

Status indicators:
- ✓ - Plugin is authenticated with a valid token
- ! - Plugin has a token but it may be invalid or expired
- ✗ - Plugin is not authenticated

### oauth auth

Authenticates one or more plugins using Google OAuth consent flow.

```bash
wtmcpctl oauth auth <plugin-name> [<plugin-name>...]
wtmcpctl oauth auth --all
wtmcpctl oauth auth -a
```

**Arguments:**
- `<plugin-name>` - One or more plugin names to authenticate (e.g., `google-drive`, `google-calendar`)
- `--all` or `-a` - Authenticate all non-authenticated plugins

**Example - Single plugin:**
```bash
$ wtmcpctl oauth auth google-drive
Authenticating plugin: google-drive

Starting OAuth2 flow...
Your browser will open automatically for authorization.

[Browser opens automatically]
[User authorizes in browser]
[Browser shows success message and can be closed]

✓ Successfully authenticated google-drive
Token saved to: /home/user/.config/wtmcp/credentials/google/token-drive.json
```

**Example - Multiple plugins:**
```bash
$ wtmcpctl oauth auth google-drive google-calendar
Authenticating plugin: google-drive

Starting OAuth2 flow...
Your browser will open automatically for authorization.

✓ Successfully authenticated google-drive
Token saved to: /home/user/.config/wtmcp/credentials/google/token-drive.json

---

Authenticating plugin: google-calendar

Starting OAuth2 flow...
Your browser will open automatically for authorization.

✓ Successfully authenticated google-calendar
Token saved to: /home/user/.config/wtmcp/credentials/google/token-calendar.json

---

Authentication Summary:
  Success: 2/2
```

## Setup

### Prerequisites

Before authenticating plugins, you need to set up OAuth credentials:

1. **Create a Google Cloud Project** (if you don't have one):
   - Go to [Google Cloud Console](https://console.cloud.google.com/)
   - Create a new project

2. **Enable Required APIs**:
   - Google Drive API (for google-drive plugin)
   - Google Calendar API (for google-calendar plugin)
   - Gmail API (for google-gmail plugin)

3. **Create OAuth 2.0 Credentials**:
   - Go to "APIs & Services" > "Credentials"
   - Click "Create Credentials" > "OAuth client ID"
   - Choose "Desktop app" as the application type
   - Download the credentials JSON file
   - Note: No need to configure redirect URIs - oauth2flow handles this automatically with a local callback server

4. **Set up the credentials file**:
   Each plugin specifies its credentials file in `plugin.yaml` via `services.auth.credentials_file`. For Google plugins, this is typically `client-credentials.json`:
   ```bash
   mkdir -p ~/.config/wtmcp/credentials/google
   cp ~/Downloads/client_secret_*.json ~/.config/wtmcp/credentials/google/client-credentials.json
   ```

   Or set a custom credentials directory:
   ```bash
   export GOOGLE_CREDENTIALS_DIR=/path/to/your/credentials
   ```

## OAuth Flow

The authentication process follows these steps:

1. `wtmcpctl` discovers plugins and reads their OAuth configuration from `plugin.yaml` (credentials file path, token file path, and scopes)
2. Reads the OAuth client credentials from the specified credentials file (e.g., `client-credentials.json`)
3. Starts a local HTTP server to handle the OAuth callback
4. Opens your default browser to the Google authorization page
5. User authorizes the application in the browser
6. Google redirects back to the local server with an authorization code
7. `wtmcpctl` automatically exchanges the code for access and refresh tokens
8. Tokens are saved to the plugin's specified token file with restricted permissions (0600)
9. The local server shuts down automatically

This automatic OAuth flow is powered by [oauth2flow](https://github.com/LeGambiArt/oauth2flow), which handles the browser interaction and callback server management.

## Token Files

Token files are stored in the credentials directory (default: `~/.config/wtmcp/credentials/google/`):

- `token-drive.json` - Google Drive authentication token
- `token-calendar.json` - Google Calendar authentication token
- `token-gmail.json` - Gmail authentication token

These files contain:
- Access token (for API requests)
- Refresh token (for automatic token renewal)
- Token expiry timestamp
- Token type

## OAuth Scopes

Each plugin declares its required OAuth scopes in its `plugin.yaml` manifest. Current Google plugin scopes:

| Plugin | Scopes |
|--------|--------|
| google-drive | `https://www.googleapis.com/auth/drive.readonly` |
| google-calendar | `https://www.googleapis.com/auth/calendar` |
| google-gmail | `https://www.googleapis.com/auth/gmail.modify` |

## Global Options

### --workdir

Specify a custom working directory for plugin discovery and credentials:

```bash
wtmcpctl --workdir /path/to/workdir oauth list
```

When `--workdir` is specified:
- Plugin discovery uses `<workdir>/plugins/`
- Credentials are stored in `<workdir>/credentials/google/` (unless `GOOGLE_CREDENTIALS_DIR` is set)
- Configuration is read from `<workdir>/config.yaml`

Default workdir: `~/.config/wtmcp`

## Environment Variables

- `GOOGLE_CREDENTIALS_DIR` - Custom directory for Google credentials (overrides `<workdir>/credentials/google/`)

## Troubleshooting

### "client credentials not found" error

The plugin's specified credentials file (e.g., `client-credentials.json`) is missing. You need to set up OAuth credentials first. See the [Prerequisites](#prerequisites) section.

### Invalid or expired token

Re-authenticate the plugin:
```bash
wtmcpctl oauth auth <plugin-name>
```

### Permission denied errors

Ensure the credentials directory has proper permissions:
```bash
chmod 700 ~/.config/wtmcp/credentials/google
chmod 600 ~/.config/wtmcp/credentials/google/*.json
```

## Security

- Token files are created with restrictive permissions (0600) to prevent unauthorized access
- The credentials directory is created with 0700 permissions
- Never commit token files or client credentials to version control
- Add the credentials directory to `.gitignore`

## See Also

- [wtmcp README](README.md)
- [wtmcpctl Architecture](docs/wtmcpctl.md)
- [Google OAuth 2.0 Documentation](https://developers.google.com/identity/protocols/oauth2)
