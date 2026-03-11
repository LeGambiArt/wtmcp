package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/LeGambiArt/oauth2flow"

	googleauth "github.com/LeGambiArt/wtmcp/internal/google"
)

// OAuthPlugin represents an OAuth plugin configuration.
type OAuthPlugin struct {
	Name            string
	TokenFile       string
	CredentialsFile string
	Scopes          []string
}

// handleOAuthCommand processes the oauth command and its subcommands.
func handleOAuthCommand(args []string) {
	// Create flagset for oauth command
	fs := flag.NewFlagSet("oauth", flag.ExitOnError)
	showHelp := fs.Bool("help", false, "Show help information")
	fs.BoolVar(showHelp, "h", false, "Show help information (short)")

	// Custom usage function
	fs.Usage = func() {
		printOAuthUsage()
	}

	// Parse oauth flags
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	// Handle help flag
	if *showHelp {
		printOAuthUsage()
		os.Exit(0)
	}

	// Get subcommand arguments
	subArgs := fs.Args()
	if len(subArgs) < 1 {
		printOAuthUsage()
		os.Exit(1)
	}

	subcommand := subArgs[0]

	// Handle subcommands
	switch subcommand {
	case "help":
		printOAuthUsage()
		os.Exit(0)
	case "list":
		handleOAuthList(subArgs[1:])
	case "auth":
		handleOAuthAuth(subArgs[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown oauth subcommand: %s\n", subcommand)
		printOAuthUsage()
		os.Exit(1)
	}
}

// handleOAuthList handles the 'oauth list' subcommand.
func handleOAuthList(args []string) {
	// Create flagset for list subcommand
	fs := flag.NewFlagSet("oauth list", flag.ExitOnError)
	showHelp := fs.Bool("help", false, "Show help information")
	fs.BoolVar(showHelp, "h", false, "Show help information (short)")

	// Custom usage function
	fs.Usage = func() {
		fmt.Println("List OAuth plugins and their authentication status")
		fmt.Println()
		fmt.Println("Usage:")
		fmt.Println("  wtmcpctl oauth list [options]")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -h, --help    Show this help message")
	}

	// Parse list flags
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	// Handle help flag
	if *showHelp {
		fs.Usage()
		os.Exit(0)
	}

	if err := oauthList(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// handleOAuthAuth handles the 'oauth auth' subcommand.
func handleOAuthAuth(args []string) {
	// Create flagset for auth subcommand
	fs := flag.NewFlagSet("oauth auth", flag.ExitOnError)
	authAll := fs.Bool("all", false, "Authenticate all non-authenticated plugins")
	fs.BoolVar(authAll, "a", false, "Authenticate all non-authenticated plugins (short)")
	showHelp := fs.Bool("help", false, "Show help information")
	fs.BoolVar(showHelp, "h", false, "Show help information (short)")

	// Custom usage function
	fs.Usage = func() {
		fmt.Println("Authenticate one or more plugins using OAuth")
		fmt.Println()
		fmt.Println("Usage:")
		fmt.Println("  wtmcpctl oauth auth [options] <plugin-name> [<plugin-name>...]")
		fmt.Println("  wtmcpctl oauth auth --all")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -a, --all     Authenticate all non-authenticated plugins")
		fmt.Println("  -h, --help    Show this help message")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  wtmcpctl oauth auth google-drive")
		fmt.Println("  wtmcpctl oauth auth google-drive google-calendar")
		fmt.Println("  wtmcpctl oauth auth --all")
	}

	// Parse auth flags
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	// Handle help flag
	if *showHelp {
		fs.Usage()
		os.Exit(0)
	}

	// Get plugin names from remaining arguments
	pluginNames := fs.Args()

	// Validate arguments
	if !*authAll && len(pluginNames) == 0 {
		fmt.Fprintf(os.Stderr, "oauth auth requires plugin name(s) or --all flag\n")
		fs.Usage()
		os.Exit(1)
	}

	if *authAll && len(pluginNames) > 0 {
		fmt.Fprintf(os.Stderr, "cannot specify both --all flag and plugin names\n")
		fs.Usage()
		os.Exit(1)
	}

	// Discover OAuth plugins
	oauthPlugins, err := discoverOAuthPlugins()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error discovering plugins: %v\n", err)
		os.Exit(1)
	}

	var pluginsToAuth []string

	// Determine which plugins to authenticate
	if *authAll {
		// Get all non-authenticated plugins
		credDir, err := getCredentialsDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot determine credentials directory: %v\n", err)
			os.Exit(1)
		}

		for _, plugin := range oauthPlugins {
			if !isAuthenticated(credDir, &plugin) {
				pluginsToAuth = append(pluginsToAuth, plugin.Name)
			}
		}

		if len(pluginsToAuth) == 0 {
			fmt.Println("All plugins are already authenticated.")
			os.Exit(0)
		}
	} else {
		// Use provided plugin names
		pluginsToAuth = pluginNames
	}

	// Authenticate each plugin
	var failed []string
	for i, pluginName := range pluginsToAuth {
		if i > 0 {
			fmt.Println()
			fmt.Println("---")
			fmt.Println()
		}

		if err := oauthAuth(pluginName, oauthPlugins); err != nil {
			fmt.Fprintf(os.Stderr, "Error authenticating %s: %v\n", pluginName, err)
			failed = append(failed, pluginName)
		}
	}

	// Print summary if multiple plugins
	if len(pluginsToAuth) > 1 {
		fmt.Println()
		fmt.Println("---")
		fmt.Println()
		fmt.Println("Authentication Summary:")
		fmt.Printf("  Success: %d/%d\n", len(pluginsToAuth)-len(failed), len(pluginsToAuth))
		if len(failed) > 0 {
			fmt.Println("  Failed:")
			for _, name := range failed {
				fmt.Printf("    - %s\n", name)
			}
			os.Exit(1)
		}
	} else if len(failed) > 0 {
		os.Exit(1)
	}
}

// printOAuthUsage displays help for the oauth command.
func printOAuthUsage() {
	fmt.Println("Manage OAuth authentication for plugins")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  wtmcpctl oauth <subcommand> [options]")
	fmt.Println()
	fmt.Println("Subcommands:")
	fmt.Println("  list                         List OAuth plugins and their authentication status")
	fmt.Println("  auth <plugin-name>...        Authenticate one or more plugins using OAuth")
	fmt.Println("  auth --all                   Authenticate all non-authenticated plugins")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  wtmcpctl oauth list")
	fmt.Println("  wtmcpctl oauth auth google-drive")
	fmt.Println("  wtmcpctl oauth auth google-drive google-calendar")
	fmt.Println("  wtmcpctl oauth auth --all")
}

// discoverOAuthPlugins scans the plugins directory and builds a list of OAuth plugins.
func discoverOAuthPlugins() ([]OAuthPlugin, error) {
	manifests, err := discoverPlugins()
	if err != nil {
		return nil, err
	}

	var plugins []OAuthPlugin
	for _, manifest := range manifests {
		// Only include plugins with OAuth2 authentication
		if manifest.Services.Auth.Type == "oauth2" &&
			manifest.Services.Auth.TokenFile != "" &&
			len(manifest.Services.Auth.Scopes) > 0 {
			plugins = append(plugins, OAuthPlugin{
				Name:            manifest.Name,
				TokenFile:       manifest.Services.Auth.TokenFile,
				CredentialsFile: manifest.Services.Auth.CredentialsFile,
				Scopes:          manifest.Services.Auth.Scopes,
			})
		}
	}

	return plugins, nil
}

// isAuthenticated checks if a plugin has a valid or refreshable token.
func isAuthenticated(credDir string, plugin *OAuthPlugin) bool {
	tokenPath := filepath.Join(credDir, plugin.TokenFile)

	if _, err := os.Stat(tokenPath); err != nil {
		return false
	}

	tok, err := googleauth.LoadToken(tokenPath)
	if err != nil {
		return false
	}

	// Consider authenticated if valid or has refresh token
	return tok.Valid() || tok.RefreshToken != ""
}

func oauthList() error {
	plugins, err := discoverOAuthPlugins()
	if err != nil {
		return fmt.Errorf("discover plugins: %w", err)
	}

	if len(plugins) == 0 {
		fmt.Println("No OAuth plugins found.")
		return nil
	}

	credDir, err := getCredentialsDir()
	if err != nil {
		return fmt.Errorf("cannot determine credentials directory: %w", err)
	}

	fmt.Println("OAuth Plugin Status:")
	fmt.Println()

	for _, plugin := range plugins {
		tokenPath := filepath.Join(credDir, plugin.TokenFile)
		status := "✗"
		statusText := "not authenticated"

		if _, err := os.Stat(tokenPath); err == nil {
			// Token file exists, check if it's valid
			if tok, err := googleauth.LoadToken(tokenPath); err == nil {
				if tok.Valid() {
					status = "✓"
					statusText = "authenticated (valid)"
				} else if tok.RefreshToken != "" {
					status = "✓"
					statusText = "authenticated (needs refresh)"
				} else {
					status = "!"
					statusText = "expired (no refresh token)"
				}
			} else {
				status = "!"
				statusText = "invalid token file"
			}
		}

		fmt.Printf("  %s  %-20s  %s\n", status, plugin.Name, statusText)
	}

	fmt.Println()
	fmt.Printf("Credentials directory: %s\n", credDir)
	return nil
}

func oauthAuth(pluginName string, plugins []OAuthPlugin) error {
	// Find the plugin
	var plugin *OAuthPlugin
	for i := range plugins {
		if plugins[i].Name == pluginName {
			plugin = &plugins[i]
			break
		}
	}

	if plugin == nil {
		return fmt.Errorf("unknown plugin: %s", pluginName)
	}

	credDir, err := getCredentialsDir()
	if err != nil {
		return fmt.Errorf("cannot determine credentials directory: %w", err)
	}

	clientCredsPath := filepath.Join(credDir, plugin.CredentialsFile)
	tokenPath := filepath.Join(credDir, plugin.TokenFile)

	// Check if client credentials exist
	if _, err := os.Stat(clientCredsPath); err != nil {
		return fmt.Errorf("client credentials not found at %s\nPlease ensure you have set up OAuth credentials", clientCredsPath)
	}

	fmt.Printf("Authenticating plugin: %s\n", plugin.Name)
	fmt.Println()
	fmt.Println("Starting OAuth2 flow...")
	fmt.Println("Your browser will open automatically for authorization.")
	fmt.Println()

	// Use oauth2flow to handle the complete OAuth2 flow
	token, err := oauth2flow.Run(context.Background(), oauth2flow.Config{
		ClientCredentialsFile: clientCredsPath,
		TokenFile:             tokenPath,
		Scopes:                plugin.Scopes,
	})
	if err != nil {
		return fmt.Errorf("oauth2 flow failed: %w", err)
	}

	// Verify token was saved
	if token == nil {
		return fmt.Errorf("oauth2 flow completed but no token received")
	}

	fmt.Println()
	fmt.Printf("✓ Successfully authenticated %s\n", plugin.Name)
	fmt.Printf("Token saved to: %s\n", tokenPath)
	return nil
}
