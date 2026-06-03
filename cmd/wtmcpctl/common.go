package main

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/LeGambiArt/wtmcp/internal/plugin"
)

// atomicWriteFile writes data to path atomically using a temp file and
// rename. This prevents partial writes from corrupting the target file.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()        //nolint:errcheck,gosec // closing before cleanup
		os.Remove(tmpName) //nolint:errcheck,gosec // best-effort cleanup
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()        //nolint:errcheck,gosec // closing before cleanup
		os.Remove(tmpName) //nolint:errcheck,gosec // best-effort cleanup
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName) //nolint:errcheck,gosec // best-effort cleanup
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName) //nolint:errcheck,gosec // best-effort cleanup
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName) //nolint:errcheck,gosec // best-effort cleanup
		return fmt.Errorf("rename config file: %w", err)
	}

	return nil
}

// updateConfigStringList sets or removes a string list at doc[section][key]
// in the YAML config file. If values is empty, the key is deleted; if the
// section becomes empty, it is also deleted.
func updateConfigStringList(configPath, section, key string, values []string) error {
	data, err := os.ReadFile(configPath) //nolint:gosec // config file path from user
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var doc map[string]any
	if len(data) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
	}
	if doc == nil {
		doc = make(map[string]any)
	}

	sectionRaw, ok := doc[section]
	var sectionMap map[string]any
	if ok {
		sectionMap, _ = sectionRaw.(map[string]any)
	}
	if sectionMap == nil {
		sectionMap = make(map[string]any)
	}

	if len(values) > 0 {
		sectionMap[key] = values
	} else {
		delete(sectionMap, key)
	}

	if len(sectionMap) > 0 {
		doc[section] = sectionMap
	} else {
		delete(doc, section)
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	return atomicWriteFile(configPath, out, 0o600)
}

// globalWorkdir is set by the --workdir flag and used for plugin discovery.
var globalWorkdir string

// globalVerbose controls whether discovery/diagnostic log output is shown.
var globalVerbose bool

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

// updateConfigBool sets or removes a boolean value at doc[section][key]
// in the YAML config file. If val is nil, the key is deleted; if the
// section becomes empty, it is also deleted.
func updateConfigBool(configPath, section, key string, val *bool) error {
	data, err := os.ReadFile(configPath) //nolint:gosec // config file path from user
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var doc map[string]any
	if len(data) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
	}
	if doc == nil {
		doc = make(map[string]any)
	}

	sectionRaw, ok := doc[section]
	var sectionMap map[string]any
	if ok {
		sectionMap, _ = sectionRaw.(map[string]any)
	}
	if sectionMap == nil {
		sectionMap = make(map[string]any)
	}

	if val != nil {
		sectionMap[key] = *val
	} else {
		delete(sectionMap, key)
	}

	if len(sectionMap) > 0 {
		doc[section] = sectionMap
	} else {
		delete(doc, section)
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	return atomicWriteFile(configPath, out, 0o600)
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
