package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveEnvVars(t *testing.T) {
	// Set up test env vars
	t.Setenv("TEST_VAR", "hello")
	t.Setenv("EMPTY_VAR", "")

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple var",
			input:    "${TEST_VAR}",
			expected: "hello",
		},
		{
			name:     "unset var",
			input:    "${UNSET_VAR}",
			expected: "",
		},
		{
			name:     "var with default",
			input:    "${UNSET_VAR:-fallback}",
			expected: "fallback",
		},
		{
			name:     "set var ignores default",
			input:    "${TEST_VAR:-fallback}",
			expected: "hello",
		},
		{
			name:     "empty var uses default",
			input:    "${EMPTY_VAR:-fallback}",
			expected: "fallback",
		},
		{
			name:     "literal dollar",
			input:    "$$price",
			expected: "$price",
		},
		{
			name:     "mixed text and vars",
			input:    "https://${TEST_VAR}.example.com/api",
			expected: "https://hello.example.com/api",
		},
		{
			name:     "no vars",
			input:    "plain string",
			expected: "plain string",
		},
		{
			name:     "empty default",
			input:    "${UNSET_VAR:-}",
			expected: "",
		},
		{
			name:     "multiple vars",
			input:    "${TEST_VAR}:${TEST_VAR}",
			expected: "hello:hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ResolveEnvVars(tt.input)
			if result != tt.expected {
				t.Errorf("ResolveEnvVars(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestResolveVars(t *testing.T) {
	vars := map[string]string{
		"TOKEN": "secret123",
	}

	if got := ResolveVars("${TOKEN}", vars); got != "secret123" {
		t.Errorf("ResolveVars(${TOKEN}) = %q, want secret123", got)
	}
	if got := ResolveVars("${MISSING:-fallback}", vars); got != "fallback" {
		t.Errorf("ResolveVars(${MISSING:-fallback}) = %q, want fallback", got)
	}
	// Must NOT resolve from process env
	t.Setenv("SHELL_VAR", "from_shell")
	if got := ResolveVars("${SHELL_VAR}", vars); got != "" {
		t.Errorf("ResolveVars(${SHELL_VAR}) = %q, want empty (should not read process env)", got)
	}
}

func TestResolveVarsMap(t *testing.T) {
	vars := map[string]string{
		"TOKEN": "secret123",
	}

	m := map[string]string{
		"url":   "https://api.example.com",
		"token": "${TOKEN}",
		"file":  "${MISSING:-default.json}",
	}

	resolved := ResolveVarsMap(m, vars)

	if resolved["url"] != "https://api.example.com" {
		t.Errorf("url = %q, want %q", resolved["url"], "https://api.example.com")
	}
	if resolved["token"] != "secret123" {
		t.Errorf("token = %q, want %q", resolved["token"], "secret123")
	}
	if resolved["file"] != "default.json" {
		t.Errorf("file = %q, want %q", resolved["file"], "default.json")
	}
}

func TestLoadConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")

	yaml := `
plugin_dirs:
  - /opt/plugins
  - /home/user/plugins
output:
  format: toon
  toon_fallback: true
cache:
  backend: filesystem
  dir: /tmp/cache
`
	if err := os.WriteFile(cfgFile, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgFile, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.PluginDirs) != 2 {
		t.Errorf("PluginDirs = %v, want 2 entries", cfg.PluginDirs)
	}
	if cfg.Output.Format != "toon" {
		t.Errorf("Output.Format = %q, want toon", cfg.Output.Format)
	}
	if cfg.Cache.Backend != "filesystem" {
		t.Errorf("Cache.Backend = %q, want filesystem", cfg.Cache.Backend)
	}
	// Defaults should still apply for unset fields
	if cfg.Plugins.ToolCallTimeout == 0 {
		t.Error("ToolCallTimeout should have default value")
	}
}

func TestLoadConfigMissing(t *testing.T) {
	cfg, err := Load("/nonexistent/config.yaml", "/nonexistent")
	if err != nil {
		t.Fatalf("should not error on missing file: %v", err)
	}
	if cfg.Output.Format != "toon" {
		t.Errorf("should return defaults, got format=%q", cfg.Output.Format)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Plugins.MaxMessageSize != 10*1024*1024 {
		t.Errorf("MaxMessageSize = %d, want %d", cfg.Plugins.MaxMessageSize, 10*1024*1024)
	}
	if cfg.Output.Format != "toon" {
		t.Errorf("Output.Format = %q, want %q", cfg.Output.Format, "toon")
	}
	if cfg.Cache.Backend != "memory" {
		t.Errorf("Cache.Backend = %q, want %q", cfg.Cache.Backend, "memory")
	}
	if cfg.Tools.Discovery != "full" {
		t.Errorf("Tools.Discovery = %q, want full", cfg.Tools.Discovery)
	}
}

func TestLoadConfigToolDiscovery(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	cfgYAML := `
tools:
  discovery: progressive
`
	if err := os.WriteFile(cfgFile, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgFile, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tools.Discovery != "progressive" {
		t.Errorf("Discovery = %q, want progressive", cfg.Tools.Discovery)
	}
}

func TestLoadConfigInvalidDiscovery(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	cfgYAML := `
tools:
  discovery: lazy
`
	if err := os.WriteFile(cfgFile, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(cfgFile, dir)
	if err == nil {
		t.Fatal("expected error for invalid discovery value")
	}
}

func TestLoadConfigDisabledPlugins(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	cfgYAML := `
plugins:
  disabled:
    - testing-farm
    - gitlab
`
	if err := os.WriteFile(cfgFile, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgFile, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Plugins.Disabled) != 2 {
		t.Fatalf("Disabled = %v, want 2 entries", cfg.Plugins.Disabled)
	}
	if cfg.Plugins.Disabled[0] != "testing-farm" {
		t.Errorf("Disabled[0] = %q, want testing-farm", cfg.Plugins.Disabled[0])
	}
	if cfg.Plugins.Disabled[1] != "gitlab" {
		t.Errorf("Disabled[1] = %q, want gitlab", cfg.Plugins.Disabled[1])
	}
}

func TestLoadConfigDisabledPluginsDefault(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Plugins.Disabled != nil {
		t.Errorf("Disabled should default to nil, got %v", cfg.Plugins.Disabled)
	}
}

func TestDefaultPluginDirs(t *testing.T) {
	userDir := "/tmp/test-user-plugins"

	t.Run("user plugins enabled", func(t *testing.T) {
		dirs := defaultPluginDirs(userDir, true)

		// User dir must be last
		if dirs[len(dirs)-1] != userDir {
			t.Errorf("last dir = %q, want user dir %q", dirs[len(dirs)-1], userDir)
		}

		// Must have at least 2 entries (some system path + user)
		if len(dirs) < 2 {
			t.Errorf("got %d dirs, want at least 2", len(dirs))
		}

		// No duplicates
		seen := make(map[string]bool)
		for _, d := range dirs {
			cleaned := filepath.Clean(d)
			if seen[cleaned] {
				t.Errorf("duplicate dir: %s", cleaned)
			}
			seen[cleaned] = true
		}
	})

	t.Run("user plugins disabled", func(t *testing.T) {
		dirs := defaultPluginDirs(userDir, false)

		// User dir must NOT be included
		for _, d := range dirs {
			if filepath.Clean(d) == filepath.Clean(userDir) {
				t.Errorf("user dir %q should not be included when disabled", userDir)
			}
		}

		// Must still have system paths
		if len(dirs) < 1 {
			t.Error("should have at least one system path")
		}
	})
}

func TestContainsPath(t *testing.T) {
	dirs := []string{"/usr/share/" + AppName + "/plugins", "/home/user/plugins"}

	if !containsPath(dirs, "/usr/share/"+AppName+"/plugins") {
		t.Error("should contain exact path")
	}
	if !containsPath(dirs, "/usr/share/"+AppName+"/plugins/") {
		t.Error("should match with trailing slash")
	}
	if containsPath(dirs, "/opt/plugins") {
		t.Error("should not contain unknown path")
	}
}
