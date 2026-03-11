package main

import (
	"os"
	"path/filepath"

	"github.com/LeGambiArt/wtmcp/internal/plugin"
)

// globalWorkdir is set by the --workdir flag and used for plugin discovery.
var globalWorkdir string

// setWorkdir sets the global workdir for plugin discovery.
func setWorkdir(workdir string) {
	globalWorkdir = workdir
}

// discoveryResult caches the discovery result to avoid redundant discovery calls.
var discoveryResult *plugin.DiscoveryResult

// getDiscoveryResult performs plugin discovery once and caches the result.
func getDiscoveryResult() (*plugin.DiscoveryResult, error) {
	if discoveryResult != nil {
		return discoveryResult, nil
	}

	result, err := plugin.Discover(plugin.DiscoveryOptions{
		WorkdirOverride: globalWorkdir,
	})
	if err != nil {
		return nil, err
	}

	discoveryResult = result
	return result, nil
}

// discoverPlugins performs plugin discovery and returns all discovered manifests.
// This is the common discovery logic shared across all commands.
func discoverPlugins() (map[string]*plugin.Manifest, error) {
	result, err := getDiscoveryResult()
	if err != nil {
		return nil, err
	}

	return result.Manager.Manifests(), nil
}

// getCredentialsDir returns the credentials directory for the current workdir.
// Respects GOOGLE_CREDENTIALS_DIR env var (from process environment, not scoped
// env.d), otherwise uses workdir/credentials/google.
func getCredentialsDir() (string, error) {
	// Respect GOOGLE_CREDENTIALS_DIR environment variable
	// (same behavior as googleauth.CredentialsDir())
	if dir := os.Getenv("GOOGLE_CREDENTIALS_DIR"); dir != "" {
		return dir, nil
	}

	result, err := getDiscoveryResult()
	if err != nil {
		return "", err
	}

	// Use workdir/credentials/google as default
	return filepath.Join(result.Workdir, "credentials", "google"), nil
}
