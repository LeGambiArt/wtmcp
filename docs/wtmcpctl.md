# wtmcpctl - wtmcp Plugin Management Tool

`wtmcpctl` is a command-line utility for managing wtmcp plugins, particularly for handling OAuth authentication for Google plugins.

## Installation

Build the tool using:

```bash
make build
```

This will create the `wtmcpctl` binary in the project root directory.

## Architecture

### Overview

`wtmcpctl` is designed with a modular, plugin-based architecture where each command is implemented as a separate module. The tool uses Go's `flag` package for consistent CLI option parsing across all commands and subcommands.

```
wtmcpctl/
├── main.go          # Entry point, command routing, global flags
└── oauth.go         # OAuth command implementation
```

### Command Structure

The tool follows a hierarchical command structure:

```
wtmcpctl [global-flags] <command> [command-flags] <subcommand> [subcommand-flags] [arguments]
```

**Example:**
```bash
wtmcpctl --version                    # Global flag
wtmcpctl oauth list                   # Command + subcommand
wtmcpctl oauth auth --all             # Command + subcommand + flag
wtmcpctl oauth auth google-drive      # Command + subcommand + argument
```

### Flag-Based Parsing

Each level of the command hierarchy uses its own `flag.FlagSet`:

1. **Top-level** (`main.go`): Handles global flags (`-h`, `--help`, `-v`, `--version`)
2. **Command level** (e.g., `oauth.go`): Handles command-specific flags
3. **Subcommand level**: Each subcommand has its own flagset for maximum flexibility

This approach provides:
- Consistent flag parsing across all commands
- Automatic help generation
- Proper error handling and validation
- Easy extensibility for new flags and commands

### Command as Plugin Design

Each command in `wtmcpctl` is implemented as a self-contained "plugin" in its own file:

**Key characteristics:**
- **Isolated file**: Each command has its own `.go` file (e.g., `oauth.go`)
- **Handler function**: Exposes a main handler function called from `main.go` (e.g., `handleOAuthCommand()`)
- **Independent flagsets**: Manages its own flags and subcommands
- **Encapsulated logic**: Contains all logic for that command domain

**Benefits:**
- Easy to add new commands without modifying existing code
- Clear separation of concerns
- Testable in isolation
- Simple code organization and maintenance

### Data Flow

```
User Input (os.Args)
    ↓
main.go (top-level FlagSet)
    ↓
Command Router (switch on command name)
    ↓
Command Handler (e.g., handleOAuthCommand)
    ↓
Command FlagSet (parse command flags)
    ↓
Subcommand Router (switch on subcommand)
    ↓
Subcommand Handler (e.g., handleOAuthAuth)
    ↓
Subcommand FlagSet (parse subcommand flags)
    ↓
Business Logic
```

### Example: OAuth Command Architecture

The `oauth` command demonstrates the plugin pattern:

**File: `oauth.go`**
- `handleOAuthCommand(args []string)` - Main entry point, parses command-level flags
- `handleOAuthList(args []string)` - Implements `oauth list` subcommand
- `handleOAuthAuth(args []string)` - Implements `oauth auth` subcommand
- Helper functions: `discoverOAuthPlugins()`, `oauthList()`, `oauthAuth()`, etc.

**Flag hierarchy:**
```
wtmcpctl
    ├── [global flags: -h, --help, -v, --version]
    └── oauth
        ├── [command flags: -h, --help]
        ├── list
        │   └── [subcommand flags: -h, --help]
        └── auth
            └── [subcommand flags: -h, --help, -a, --all]
```

## Writing New Commands

Follow these steps to add a new command to `wtmcpctl`:

### 1. Create Command File

Create a new file in `cmd/wtmcpctl/` named after your command (e.g., `plugin.go` for a `plugin` command):

```go
package main

import (
	"flag"
	"fmt"
	"os"
)

// handlePluginCommand processes the plugin command and its subcommands.
func handlePluginCommand(args []string) {
	// Create flagset for the command
	fs := flag.NewFlagSet("plugin", flag.ExitOnError)
	showHelp := fs.Bool("help", false, "Show help information")
	fs.BoolVar(showHelp, "h", false, "Show help information (short)")

	// Custom usage function
	fs.Usage = func() {
		printPluginUsage()
	}

	// Parse command flags
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	// Handle help flag
	if *showHelp {
		printPluginUsage()
		os.Exit(0)
	}

	// Get subcommand arguments
	subArgs := fs.Args()
	if len(subArgs) < 1 {
		printPluginUsage()
		os.Exit(1)
	}

	subcommand := subArgs[0]

	// Route to subcommand handlers
	switch subcommand {
	case "help":
		printPluginUsage()
		os.Exit(0)
	case "list":
		handlePluginList(subArgs[1:])
	case "install":
		handlePluginInstall(subArgs[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown plugin subcommand: %s\n", subcommand)
		printPluginUsage()
		os.Exit(1)
	}
}

// printPluginUsage displays help for the plugin command.
func printPluginUsage() {
	fmt.Println("Manage wtmcp plugins")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  wtmcpctl plugin <subcommand> [options]")
	fmt.Println()
	fmt.Println("Subcommands:")
	fmt.Println("  list                   List installed plugins")
	fmt.Println("  install <name>         Install a plugin")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  wtmcpctl plugin list")
	fmt.Println("  wtmcpctl plugin install google-drive")
}
```

### 2. Implement Subcommand Handlers

For each subcommand, create a handler function with its own flagset:

```go
// handlePluginList handles the 'plugin list' subcommand.
func handlePluginList(args []string) {
	// Create flagset for subcommand
	fs := flag.NewFlagSet("plugin list", flag.ExitOnError)
	showAll := fs.Bool("all", false, "Show all plugins including disabled")
	fs.BoolVar(showAll, "a", false, "Show all plugins (short)")
	showHelp := fs.Bool("help", false, "Show help information")
	fs.BoolVar(showHelp, "h", false, "Show help information (short)")

	// Custom usage
	fs.Usage = func() {
		fmt.Println("List installed plugins")
		fmt.Println()
		fmt.Println("Usage:")
		fmt.Println("  wtmcpctl plugin list [options]")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -a, --all     Show all plugins including disabled")
		fmt.Println("  -h, --help    Show this help message")
	}

	// Parse flags
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if *showHelp {
		fs.Usage()
		os.Exit(0)
	}

	// Implement business logic
	if err := listPlugins(*showAll); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// listPlugins implements the actual business logic.
func listPlugins(showAll bool) error {
	// Your implementation here
	fmt.Println("Listing plugins...")
	return nil
}
```

### 3. Register Command in main.go

Add your command to the switch statement in `main.go`:

```go
func main() {
	// ... existing code ...

	switch command {
	case "help":
		// existing code
	case "version":
		// existing code
	case "oauth":
		handleOAuthCommand(args[1:])
	case "plugin":  // Add your new command
		handlePluginCommand(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}
```

### 4. Update Help Text in main.go

Add your command to the usage output in `printUsage()`:

```go
func printUsage() {
	fmt.Printf("wtmcpctl %s - wtmcp plugin management tool\n", Version)
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  wtmcpctl <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  oauth        Manage OAuth authentication for plugins")
	fmt.Println("  plugin       Manage wtmcp plugins")  // Add your command
	fmt.Println("  version      Show version information")
	fmt.Println("  help         Show this help message")
	fmt.Println()
	fmt.Println("Use 'wtmcpctl <command> --help' for more information about a command.")
}
```

### 5. Update Help Command Handler

Add your command to `handleHelpCommand()`:

```go
func handleHelpCommand(cmd string) {
	switch cmd {
	case "oauth":
		printOAuthUsage()
	case "plugin":  // Add your command
		printPluginUsage()
	case "version":
		// existing code
	// ... rest of cases
	}
}
```

### 6. Build and Test

```bash
# Build
go build -o wtmcpctl ./cmd/wtmcpctl

# Test help
./wtmcpctl plugin --help

# Test subcommand help
./wtmcpctl plugin list --help

# Test functionality
./wtmcpctl plugin list
./wtmcpctl plugin install my-plugin
```

### Best Practices

1. **Flag Naming**:
   - Use lowercase with hyphens: `--all-plugins`, not `--allPlugins`
   - Provide short versions for common flags: `-a` for `--all`
   - Always provide `-h`/`--help` for every command and subcommand

2. **Error Handling**:
   - Print errors to `stderr` using `fmt.Fprintf(os.Stderr, ...)`
   - Exit with non-zero status on errors: `os.Exit(1)`
   - Provide actionable error messages

3. **Usage Messages**:
   - Keep command descriptions concise (one line)
   - Show examples for complex commands
   - Use consistent formatting across all commands

4. **Code Organization**:
   - One file per command (e.g., `oauth.go`, `plugin.go`)
   - Group related helper functions in the same file
   - Keep business logic separate from flag parsing

5. **Flag Validation**:
   - Validate flag combinations after parsing
   - Provide clear error messages for invalid combinations
   - Show usage on validation errors

6. **Testing**:
   - Test all flag combinations
   - Test help output for all commands/subcommands
   - Test error cases and edge conditions

## Commands

### oauth list

Lists all OAuth-enabled plugins and their authentication status.

```bash
wtmcpctl oauth list
```

**Output:**
- ✓ - Plugin is authenticated with a valid token
- ! - Plugin has a token but it may be invalid or expired
- ✗ - Plugin is not authenticated

**Example:**
```
$ wtmcpctl oauth list
OAuth Plugin Status:

  ✓  google-drive          authenticated (valid)
  ✓  google-calendar       authenticated (needs refresh)
  ✗  google-gmail          not authenticated

Credentials directory: /home/user/.config/wtmcp/credentials/google
```

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

**Available plugins:**
- `google-drive` - Google Drive plugin
- `google-calendar` - Google Calendar plugin
- `google-gmail` - Gmail plugin

**Example - Single plugin:**
```
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
```
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

**Example - Authenticate all non-authenticated plugins:**
```
$ wtmcpctl oauth auth --all
Authenticating plugin: google-gmail

Starting OAuth2 flow...
Your browser will open automatically for authorization.

✓ Successfully authenticated google-gmail
Token saved to: /home/user/.config/wtmcp/credentials/google/token-gmail.json

---

Authentication Summary:
  Success: 1/1
```

## Prerequisites

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

## Environment Variables

- `GOOGLE_CREDENTIALS_DIR` - Custom directory for Google credentials (default: `~/.config/wtmcp/credentials/google/`)

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

- [wtmcp README](../README.md)
- [Google OAuth 2.0 Documentation](https://developers.google.com/identity/protocols/oauth2)
