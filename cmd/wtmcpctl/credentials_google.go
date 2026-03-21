package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func init() {
	// Register Google credentials service
	registerCredentialService(credentialService{
		Name:        "google",
		Description: "Google Cloud Platform OAuth 2.0 desktop credentials",
		SetupFunc:   setupGoogleCredentials,
	})
}

// setupGoogleCredentials runs the interactive setup wizard for Google OAuth credentials.
func setupGoogleCredentials() error {
	credDir, err := credentialsDirForGoogle()
	if err != nil {
		return err
	}
	credPath := filepath.Join(credDir, "client-credentials.json")

	// Pre-flight check: warn if credentials already exist.
	if _, err := os.Stat(credPath); err == nil {
		fmt.Printf("Credentials file already exists at:\n  %s\n\n", credPath)
		if !promptYesNo("Continue and overwrite? [y/N] ") {
			fmt.Println("Aborted.")
			return nil
		}
		fmt.Println()
	}

	fmt.Println("Setting up Google OAuth 2.0 credentials for wtmcp")
	fmt.Println("==================================================")
	fmt.Println()
	fmt.Println("This guide will walk you through creating OAuth 2.0 desktop")
	fmt.Println("application credentials in the Google Cloud Console.")
	fmt.Println()

	// Step 1
	fmt.Println("Step 1/5: Create a Google Cloud Project")
	fmt.Println("----------------------------------------")
	fmt.Println("Create a new project (or select an existing one):")
	promptAndOpenURL("https://console.cloud.google.com/projectcreate")
	fmt.Println()
	waitForEnter("Press Enter when your project is ready...")
	fmt.Println()

	// Step 2
	fmt.Println("Step 2/5: Enable Required APIs")
	fmt.Println("-------------------------------")
	fmt.Println("Enable the Google APIs you plan to use:")
	fmt.Println()
	fmt.Println("Google Drive API:")
	promptAndOpenURL("https://console.cloud.google.com/apis/library/drive.googleapis.com")
	fmt.Println("Google Calendar API:")
	promptAndOpenURL("https://console.cloud.google.com/apis/library/calendar-json.googleapis.com")
	fmt.Println("Gmail API:")
	promptAndOpenURL("https://console.cloud.google.com/apis/library/gmail.googleapis.com")
	fmt.Println("Google Docs API:")
	promptAndOpenURL("https://console.cloud.google.com/apis/library/docs.googleapis.com")
	fmt.Println("You only need to enable the APIs for the plugins you plan to use.")
	fmt.Println()
	waitForEnter("Press Enter when you have enabled the APIs...")
	fmt.Println()

	// Step 3
	fmt.Println("Step 3/5: Configure OAuth Consent Screen")
	fmt.Println("-----------------------------------------")
	fmt.Println("Configure the consent screen:")
	promptAndOpenURL("https://console.cloud.google.com/apis/credentials/consent")
	fmt.Println("  1. Select \"External\" as the user type (or \"Internal\" for Workspace)")
	fmt.Println("  2. Fill in the app name (e.g., \"wtmcp\")")
	fmt.Println("  3. Add your email as a test user")
	fmt.Println("  4. Save the consent screen configuration")
	fmt.Println()
	fmt.Println("For personal use, set the publishing status to \"Testing\" and add")
	fmt.Println("your Google account as a test user.")
	fmt.Println()
	waitForEnter("Press Enter when the consent screen is configured...")
	fmt.Println()

	// Step 4
	fmt.Println("Step 4/5: Create OAuth 2.0 Client Credentials")
	fmt.Println("-----------------------------------------------")
	fmt.Println("Create OAuth 2.0 client credentials:")
	promptAndOpenURL("https://console.cloud.google.com/apis/credentials")
	fmt.Println("  1. Click \"Create Credentials\" > \"OAuth client ID\"")
	fmt.Println("  2. Select \"Desktop app\" as the application type")
	fmt.Println("  3. Give it a name (e.g., \"wtmcp-desktop\")")
	fmt.Println("  4. Click \"Create\"")
	fmt.Println("  5. Click \"Download JSON\" to save the credentials file")
	fmt.Println()
	waitForEnter("Press Enter when you have downloaded the credentials JSON file...")
	fmt.Println()

	// Step 5
	fmt.Println("Step 5/5: Place the Credentials File")
	fmt.Println("--------------------------------------")

	// Create credentials directory if it does not exist.
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		return fmt.Errorf("failed to create credentials directory %s: %w", credDir, err)
	}

	fmt.Println("The credentials file needs to be placed at:")
	fmt.Println()
	fmt.Printf("  Target: %s\n", credPath)
	fmt.Println()
	fmt.Println("You can either:")
	fmt.Println("  1. Provide the path to the downloaded file (e.g., ~/Downloads/client_secret_*.json)")
	fmt.Println("     and this tool will copy it for you")
	fmt.Println("  2. Press Enter if you've already placed the file manually")
	fmt.Println()

	sourcePath := promptForFilePath("Enter path to downloaded credentials file (or press Enter to skip): ")

	if sourcePath != "" {
		// User provided a path, copy the file
		fmt.Printf("Copying %s to %s...\n", sourcePath, credPath)
		if err := copyFile(sourcePath, credPath); err != nil {
			return fmt.Errorf("failed to copy credentials file: %w", err)
		}
		fmt.Println("✓ File copied successfully")
	} else {
		// User skipped, assume file is already in place
		fmt.Println("Skipping copy, assuming file is already in place...")
	}
	fmt.Println()

	// Verification
	fmt.Println("Verifying credentials...")
	fmt.Println()

	if err := validateGoogleCredentialsFile(credPath); err != nil {
		return fmt.Errorf("credential verification failed: %w", err)
	}

	fmt.Println()
	fmt.Println("✓ Credentials verified successfully!")
	fmt.Println()
	fmt.Println("Next step: authenticate your plugins by running:")
	fmt.Println("  wtmcpctl oauth auth --all")

	return nil
}

// credentialsDirForGoogle returns the credentials directory for Google,
// without requiring plugin discovery (which may fail during initial setup).
// Resolution order: GOOGLE_CREDENTIALS_DIR env var, --workdir flag, default.
func credentialsDirForGoogle() (string, error) {
	// Highest priority: GOOGLE_CREDENTIALS_DIR env var
	if dir := os.Getenv("GOOGLE_CREDENTIALS_DIR"); dir != "" {
		return dir, nil
	}

	// Second priority: --workdir flag
	if globalWorkdir != "" {
		return filepath.Join(globalWorkdir, "credentials", "google"), nil
	}

	// Default: ~/.config/wtmcp/credentials/google
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "wtmcp", "credentials", "google"), nil
}

// clientCredentials represents the Google OAuth2 client credentials file structure.
type clientCredentials struct {
	Installed *clientCredentialsData `json:"installed"`
	Web       *clientCredentialsData `json:"web"`
}

type clientCredentialsData struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// validateGoogleCredentialsFile validates the Google OAuth2 credentials file structure.
func validateGoogleCredentialsFile(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // credential file path from user input
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("file not found at %s", path)
		}
		return fmt.Errorf("cannot read file: %w", err)
	}

	if len(data) == 0 {
		return fmt.Errorf("file is empty: %s", path)
	}

	var creds clientCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return fmt.Errorf("invalid JSON: %w\nMake sure you downloaded the correct credentials file", err)
	}

	if creds.Web != nil && creds.Installed == nil {
		return fmt.Errorf("found 'web' credentials, but wtmcp requires 'Desktop app' credentials.\nPlease create a new OAuth client ID with application type \"Desktop app\"")
	}

	if creds.Installed == nil {
		return fmt.Errorf("no 'installed' credentials found in file.\nPlease create an OAuth client ID with application type \"Desktop app\"")
	}

	if creds.Installed.ClientID == "" {
		return fmt.Errorf("client_id is missing from credentials file")
	}

	if creds.Installed.ClientSecret == "" {
		return fmt.Errorf("client_secret is missing from credentials file")
	}

	fmt.Printf("  ✓ File exists\n")
	fmt.Printf("  ✓ Valid JSON\n")
	fmt.Printf("  ✓ Desktop app credentials (installed type)\n")
	fmt.Printf("  ✓ client_id: %s\n", creds.Installed.ClientID)
	fmt.Printf("  ✓ client_secret: %s...%s\n",
		creds.Installed.ClientSecret[:min(4, len(creds.Installed.ClientSecret))],
		creds.Installed.ClientSecret[max(0, len(creds.Installed.ClientSecret)-4):],
	)

	return nil
}
