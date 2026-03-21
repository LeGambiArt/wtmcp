package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

// credentialService describes a supported service for credential setup.
type credentialService struct {
	Name        string
	Description string
	SetupFunc   func() error
}

var supportedCredentialServices = make(map[string]credentialService)

// registerCredentialService registers a service for credential setup.
// This is called from init() functions in service-specific files (e.g., credentials_google.go).
func registerCredentialService(svc credentialService) {
	supportedCredentialServices[svc.Name] = svc
}

var credentialsCmd = &cobra.Command{
	Use:   "credentials <service>",
	Short: "Set up OAuth client credentials for a service",
	Long: `Interactive guide for setting up OAuth client credentials.

This command walks you through creating OAuth 2.0 desktop application
credentials in the service's developer console.

Run 'wtmcpctl oauth credentials <service>' to see available services.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeCredentialServices,
	RunE:              runCredentials,
}

// completeCredentialServices returns supported service names for shell completion.
func completeCredentialServices(
	_ *cobra.Command, _ []string, _ string,
) ([]string, cobra.ShellCompDirective) {
	names := make([]string, 0, len(supportedCredentialServices))
	for name := range supportedCredentialServices {
		names = append(names, name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

func runCredentials(_ *cobra.Command, args []string) error {
	serviceName := args[0]

	svc, ok := supportedCredentialServices[serviceName]
	if !ok {
		// Show available services in error message
		available := make([]string, 0, len(supportedCredentialServices))
		for name := range supportedCredentialServices {
			available = append(available, name)
		}
		if len(available) > 0 {
			return fmt.Errorf("unknown service: %s\n\nAvailable services: %v", serviceName, available)
		}
		return fmt.Errorf("unknown service: %s", serviceName)
	}

	return svc.SetupFunc()
}

// waitForEnter displays a prompt and waits for the user to press Enter.
func waitForEnter(prompt string) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	_, _ = reader.ReadString('\n')
}

// promptYesNo displays a prompt and returns true if the user enters y/yes.
// Default is false (no).
func promptYesNo(prompt string) bool {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
}

// openBrowser opens the specified URL in the default browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	return cmd.Start()
}

// promptAndOpenURL displays a URL and optionally opens it in the browser.
// Prompts the user with [y/N], defaulting to No.
func promptAndOpenURL(url string) {
	fmt.Println()
	fmt.Printf("  %s\n", url)
	fmt.Println()
	if promptYesNo("Open in browser? [y/N] ") {
		if err := openBrowser(url); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to open browser: %v\n", err)
			fmt.Println("Please open the URL manually in your browser.")
		}
	}
}

// promptForFilePath prompts the user to enter a file path.
// Returns the trimmed path, or empty string if user just presses Enter.
func promptForFilePath(prompt string) string {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return ""
	}
	return strings.TrimSpace(line)
}

// copyFile copies a file from src to dst, creating the destination directory if needed.
func copyFile(src, dst string) error {
	// Expand tilde in source path
	if strings.HasPrefix(src, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("cannot determine home directory: %w", err)
		}
		src = filepath.Join(home, src[2:])
	}

	// Open source file
	srcFile, err := os.Open(src) //nolint:gosec // user-provided path
	if err != nil {
		return fmt.Errorf("cannot open source file: %w", err)
	}
	defer srcFile.Close() //nolint:errcheck // defer close on read-only file

	// Create destination directory if needed
	dstDir := filepath.Dir(dst)
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return fmt.Errorf("cannot create destination directory: %w", err)
	}

	// Create destination file
	dstFile, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // user-provided destination path
	if err != nil {
		return fmt.Errorf("cannot create destination file: %w", err)
	}
	defer dstFile.Close() //nolint:errcheck // safety net, explicit close below

	// Copy contents
	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("cannot copy file: %w", err)
	}

	// Explicitly close and check for errors (e.g., disk full, I/O errors)
	if err := dstFile.Close(); err != nil {
		return fmt.Errorf("cannot close destination file: %w", err)
	}

	return nil
}
