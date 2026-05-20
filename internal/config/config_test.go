package config

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestResolveVarsHostnameModifier(t *testing.T) {
	vars := map[string]string{
		"JENKINS_URL":   "https://jenkins.example.com",
		"URL_WITH_PORT": "https://jenkins.example.com:8080",
		"URL_WITH_PATH": "https://jenkins.example.com/jenkins",
		"INVALID_URL":   "not-a-url",
	}

	// Test |hostname modifier with valid URL
	if got := ResolveVars("${JENKINS_URL|hostname}", vars); got != "jenkins.example.com" {
		t.Errorf("ResolveVars(${JENKINS_URL|hostname}) = %q, want jenkins.example.com", got)
	}

	// Test |hostname with port
	if got := ResolveVars("${URL_WITH_PORT|hostname}", vars); got != "jenkins.example.com" {
		t.Errorf("ResolveVars(${URL_WITH_PORT|hostname}) = %q, want jenkins.example.com", got)
	}

	// Test |hostname with path
	if got := ResolveVars("${URL_WITH_PATH|hostname}", vars); got != "jenkins.example.com" {
		t.Errorf("ResolveVars(${URL_WITH_PATH|hostname}) = %q, want jenkins.example.com", got)
	}

	// Test |hostname with invalid URL returns empty
	if got := ResolveVars("${INVALID_URL|hostname}", vars); got != "" {
		t.Errorf("ResolveVars(${INVALID_URL|hostname}) = %q, want empty string", got)
	}

	// Test |hostname with missing variable returns empty
	if got := ResolveVars("${MISSING|hostname}", vars); got != "" {
		t.Errorf("ResolveVars(${MISSING|hostname}) = %q, want empty string", got)
	}

	// Test |hostname exact match — |hostname2 should NOT trigger hostname extraction
	vars["SOME_VAR"] = "https://example.com"
	if got := ResolveVars("${SOME_VAR|hostname2}", vars); got == "example.com" {
		t.Error("${SOME_VAR|hostname2} should not extract hostname (unknown modifier)")
	}

	// Test |hostnamestrip suffix overlap — should NOT trigger hostname extraction
	if got := ResolveVars("${SOME_VAR|hostnamestrip}", vars); got == "example.com" {
		t.Error("${SOME_VAR|hostnamestrip} should not extract hostname (unknown modifier)")
	}

	// Test |hostname with scheme-less URL returns empty
	vars["BARE_HOST"] = "example.com"
	if got := ResolveVars("${BARE_HOST|hostname}", vars); got != "" {
		t.Errorf("${BARE_HOST|hostname} = %q, want empty (no scheme)", got)
	}

	// Test |hostname with protocol-relative URL
	vars["PROTO_REL"] = "//example.com:8080/path"
	if got := ResolveVars("${PROTO_REL|hostname}", vars); got != "example.com" {
		t.Errorf("${PROTO_REL|hostname} = %q, want example.com", got)
	}
}

func TestResolveVarsNestedDefaults(t *testing.T) {
	// Test nested defaults: ${VAR:-${OTHER|hostname}}
	vars := map[string]string{
		"JENKINS_URL": "https://jenkins.example.com",
		"REALM":       "IPA.REDHAT.COM",
	}

	// HOST not set, should fall back to URL|hostname
	if got := ResolveVars("${HOST:-${JENKINS_URL|hostname}}", vars); got != "jenkins.example.com" {
		t.Errorf("ResolveVars with nested default = %q, want jenkins.example.com", got)
	}

	// HOST is set, should use it
	vars["HOST"] = "override.example.com"
	if got := ResolveVars("${HOST:-${JENKINS_URL|hostname}}", vars); got != "override.example.com" {
		t.Errorf("ResolveVars with HOST set = %q, want override.example.com", got)
	}

	// Test complex SPN construction
	delete(vars, "HOST")
	spn := "HTTP/${HOST:-${JENKINS_URL|hostname}}${REALM:+@}${REALM}"
	if got := ResolveVars(spn, vars); got != "HTTP/jenkins.example.com@IPA.REDHAT.COM" {
		t.Errorf("ResolveVars SPN = %q, want HTTP/jenkins.example.com@IPA.REDHAT.COM", got)
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
	if cfg.Tools.Discovery != "progressive" {
		t.Errorf("Tools.Discovery = %q, want progressive", cfg.Tools.Discovery)
	}

	for _, domain := range []string{
		"docs.googleapis.com", "www.googleapis.com",
		"gmail.googleapis.com",
	} {
		if v := cfg.HTTP.RateLimit.PerDomain[domain]; v != "300/m" {
			t.Errorf("PerDomain[%q] = %q, want %q", domain, v, "300/m")
		}
	}
}

func TestLoadConfigPerDomainMerge(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	cfgYAML := `
http:
  rate_limit:
    per_domain:
      custom.api.com: "50/m"
`
	if err := os.WriteFile(cfgFile, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgFile, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if v := cfg.HTTP.RateLimit.PerDomain["custom.api.com"]; v != "50/m" {
		t.Errorf("user domain = %q, want %q", v, "50/m")
	}
	if v := cfg.HTTP.RateLimit.PerDomain["docs.googleapis.com"]; v != "300/m" {
		t.Errorf("google default lost: docs.googleapis.com = %q, want %q", v, "300/m")
	}
}

func TestLoadConfigPerDomainEmptyMap(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	cfgYAML := "http:\n  rate_limit:\n    per_domain: {}"
	if err := os.WriteFile(cfgFile, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgFile, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Verify that per_domain: {} still gets Google API defaults.
	// This is intentional: the elevated limits protect against
	// Google API rate rejection.
	for _, domain := range []string{
		"docs.googleapis.com",
		"www.googleapis.com",
		"gmail.googleapis.com",
	} {
		if v := cfg.HTTP.RateLimit.PerDomain[domain]; v != "300/m" {
			t.Errorf("%s = %q, want %q", domain, v, "300/m")
		}
	}
}

func TestLoadConfigPerDomainUserOverride(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	cfgYAML := `
http:
  rate_limit:
    per_domain:
      docs.googleapis.com: "50/m"
`
	if err := os.WriteFile(cfgFile, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgFile, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if v := cfg.HTTP.RateLimit.PerDomain["docs.googleapis.com"]; v != "50/m" {
		t.Errorf("user override lost: docs.googleapis.com = %q, want %q", v, "50/m")
	}
	if v := cfg.HTTP.RateLimit.PerDomain["www.googleapis.com"]; v != "300/m" {
		t.Errorf("other google default lost: www.googleapis.com = %q, want %q", v, "300/m")
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

func TestLoadConfigTimeoutOrderingWarning(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")

	// http.timeout (90s) >= plugins.tool_call_timeout (60s) — should warn
	cfgYAML := `
http:
  timeout: 90s
plugins:
  tool_call_timeout: 60s
`
	if err := os.WriteFile(cfgFile, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	// Capture log output
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	cfg, err := Load(cfgFile, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Config should still load successfully
	if cfg.HTTP.Timeout != 90*time.Second {
		t.Errorf("HTTP.Timeout = %v, want 90s", cfg.HTTP.Timeout)
	}

	// Warning should have been logged
	if !strings.Contains(logBuf.String(), "http.timeout") {
		t.Error("expected warning about http.timeout >= tool_call_timeout")
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
