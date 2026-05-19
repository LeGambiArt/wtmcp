package plugin

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/LeGambiArt/wtmcp/internal/auth"
	"github.com/LeGambiArt/wtmcp/internal/cache"
	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/proxy"
)

func setupTestPlugin(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, name)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil { //nolint:gosec // test dir
		t.Fatal(err)
	}

	handlerPath := filepath.Join(pluginDir, "handler.sh")
	if err := os.WriteFile(handlerPath, []byte(script), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}

	manifest := `
name: ` + name + `
version: "1.0.0"
description: "Test plugin"
execution: persistent
handler: ./handler.sh
tools:
  - name: ` + name + `_test
    description: "A test tool"
    params: {}
`
	manifestPath := filepath.Join(pluginDir, "plugin.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil { //nolint:gosec // test config
		t.Fatal(err)
	}

	return dir
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	cfg := config.DefaultConfig()
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	return NewManager(authReg, p, cacheStore, cfg, nil, nil, "", "", "", config.EnvLoadOptions{}, "")
}

var echoScript = `#!/bin/bash
while IFS= read -r line; do
  type=$(echo "$line" | jq -r '.type')
  id=$(echo "$line" | jq -r '.id')
  case "$type" in
    init)
      echo "{\"id\":\"$id\",\"type\":\"init_ok\"}"
      ;;
    tool_call)
      tool=$(echo "$line" | jq -r '.tool')
      echo "{\"id\":\"$id\",\"type\":\"tool_result\",\"result\":{\"tool\":\"$tool\"}}"
      ;;
    shutdown)
      echo "{\"id\":\"$id\",\"type\":\"shutdown_ok\"}"
      exit 0
      ;;
  esac
done
`

func TestResolveConfigSessionDir(t *testing.T) {
	t.Run("injects _session_dir and _output_dir when set", func(t *testing.T) {
		m := &Manager{
			sessionDir: "/home/user/project",
			cfg:        &config.Config{},
		}
		manifest := &Manifest{Config: map[string]string{}}

		got := m.resolveConfig("google-docs", manifest)

		if got["_session_dir"] != "/home/user/project" {
			t.Errorf("_session_dir = %q, want /home/user/project", got["_session_dir"])
		}
		want := filepath.Join("/home/user/project", "wtmcp", "google-docs")
		if got["_output_dir"] != want {
			t.Errorf("_output_dir = %q, want %q", got["_output_dir"], want)
		}
	})

	t.Run("omits both when sessionDir is empty", func(t *testing.T) {
		m := &Manager{
			cfg: &config.Config{},
		}
		manifest := &Manifest{Config: map[string]string{}}

		got := m.resolveConfig("google-docs", manifest)

		if _, ok := got["_session_dir"]; ok {
			t.Error("_session_dir should not be set when sessionDir is empty")
		}
		if _, ok := got["_output_dir"]; ok {
			t.Error("_output_dir should not be set when sessionDir is empty")
		}
	})
}

func TestManagerDiscover(t *testing.T) {
	dir := setupTestPlugin(t, "hello", echoScript)

	m := newTestManager(t)
	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	manifests := m.Manifests()
	if len(manifests) != 1 {
		t.Fatalf("got %d manifests, want 1", len(manifests))
	}
	if _, ok := manifests["hello"]; !ok {
		t.Error("expected 'hello' manifest")
	}
}

func TestManagerDiscoverSkipsConfigDisabled(t *testing.T) {
	dir := setupTestPlugin(t, "hello", echoScript)

	cfg := config.DefaultConfig()
	cfg.Plugins.Disabled = []string{"hello"}
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "", "", "", config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	manifests := m.Manifests()
	if len(manifests) != 0 {
		t.Errorf("got %d manifests, want 0 (plugin should be disabled via config)", len(manifests))
	}
}

func TestManagerDiscoverPartialDisable(t *testing.T) {
	dir := t.TempDir()
	createPluginInDir(t, dir, "keep-me", echoScript)
	createPluginInDir(t, dir, "skip-me", echoScript)

	cfg := config.DefaultConfig()
	cfg.Plugins.Disabled = []string{"skip-me"}
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "", "", "", config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	manifests := m.Manifests()
	if len(manifests) != 1 {
		t.Fatalf("got %d manifests, want 1", len(manifests))
	}
	if _, ok := manifests["keep-me"]; !ok {
		t.Error("expected 'keep-me' manifest to be discovered")
	}
	if _, ok := manifests["skip-me"]; ok {
		t.Error("'skip-me' should have been disabled via config")
	}
}

func TestManagerDiscoverDoubleDisabled(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "both-disabled")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil { //nolint:gosec // test dir
		t.Fatal(err)
	}
	handlerPath := filepath.Join(pluginDir, "handler.sh")
	if err := os.WriteFile(handlerPath, []byte(echoScript), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}
	manifest := `
name: both-disabled
version: "1.0.0"
description: "Test plugin"
execution: persistent
handler: ./handler.sh
enabled: false
tools:
  - name: both-disabled_test
    description: "A test tool"
    params: {}
`
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.yaml"), []byte(manifest), 0o644); err != nil { //nolint:gosec // test config
		t.Fatal(err)
	}

	cfg := config.DefaultConfig()
	cfg.Plugins.Disabled = []string{"both-disabled"}
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "", "", "", config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	manifests := m.Manifests()
	if len(manifests) != 0 {
		t.Errorf("got %d manifests, want 0 (plugin is disabled in both manifest and config)", len(manifests))
	}
}

func TestManagerDiscoverWarnsUnknownDisabled(t *testing.T) {
	dir := setupTestPlugin(t, "hello", echoScript)

	cfg := config.DefaultConfig()
	cfg.Plugins.Disabled = []string{"typo-name"}
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "", "", "", config.EnvLoadOptions{}, "")

	// Capture log output
	var buf strings.Builder
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// hello should still be discovered
	if _, ok := m.Manifests()["hello"]; !ok {
		t.Error("expected 'hello' manifest")
	}

	// Should warn about the typo
	if !strings.Contains(buf.String(), `"typo-name"`) {
		t.Errorf("expected warning about unknown disabled plugin, got: %s", buf.String())
	}
}

func TestManagerDiscoverNoWarningForManifestDisabled(t *testing.T) {
	// Plugin with enabled: false in manifest, also in plugins.disabled
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "off-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil { //nolint:gosec // test dir
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "handler.sh"), []byte(echoScript), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}
	manifest := `
name: off-plugin
version: "1.0.0"
description: "Test plugin"
execution: persistent
handler: ./handler.sh
enabled: false
tools:
  - name: off-plugin_test
    description: "A test tool"
    params: {}
`
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.yaml"), []byte(manifest), 0o644); err != nil { //nolint:gosec // test config
		t.Fatal(err)
	}

	cfg := config.DefaultConfig()
	cfg.Plugins.Disabled = []string{"off-plugin"}
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "", "", "", config.EnvLoadOptions{}, "")

	var buf strings.Builder
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// Should NOT warn about unknown plugin — the plugin exists
	if strings.Contains(buf.String(), "no such plugin was found") {
		t.Errorf("should not warn for manifest-disabled plugin in disabled list, got: %s", buf.String())
	}
}

func TestManagerConfigDisabledPlugins(t *testing.T) {
	dir := t.TempDir()
	createPluginInDir(t, dir, "enabled-one", echoScript)
	createPluginInDir(t, dir, "disabled-one", echoScript)

	cfg := config.DefaultConfig()
	cfg.Plugins.Disabled = []string{"disabled-one"}
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "", "", "", config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// enabled-one should be in Manifests
	if _, ok := m.Manifests()["enabled-one"]; !ok {
		t.Error("expected 'enabled-one' in Manifests()")
	}

	// disabled-one should be in ConfigDisabledPlugins
	configDisabled := m.ConfigDisabledPlugins()
	if len(configDisabled) != 1 {
		t.Fatalf("got %d config-disabled, want 1", len(configDisabled))
	}
	if _, ok := configDisabled["disabled-one"]; !ok {
		t.Error("expected 'disabled-one' in ConfigDisabledPlugins()")
	}

	// disabled-one should NOT be in Manifests
	if _, ok := m.Manifests()["disabled-one"]; ok {
		t.Error("'disabled-one' should not be in Manifests()")
	}
}

func TestManagerDiscoverSkipsNonexistentDir(t *testing.T) {
	m := newTestManager(t)
	if err := m.Discover([]string{"/nonexistent/path"}, ""); err != nil {
		t.Fatalf("Discover should not error on missing dir: %v", err)
	}
}

func TestManagerLoadAndCallTool(t *testing.T) {
	dir := setupTestPlugin(t, "echo", echoScript)

	m := newTestManager(t)
	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	ctx := context.Background()
	if err := m.LoadAll(ctx); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	m.StartPending(ctx)
	defer m.ShutdownAll(ctx)

	// Find tool owner
	owner, handle := m.CallTool(ctx, "echo_test")
	if owner != "echo" {
		t.Errorf("owner = %q, want echo", owner)
	}
	if handle == nil {
		t.Fatal("handle is nil")
	}

	// Call the tool
	callResult, err := handle.CallTool(ctx, "echo_test", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(callResult.Result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed["tool"] != "echo_test" {
		t.Errorf("tool = %v", parsed["tool"])
	}
}

func TestManagerUnknownTool(t *testing.T) {
	m := newTestManager(t)
	_, handle := m.CallTool(context.Background(), "nonexistent_tool")
	if handle != nil {
		t.Error("expected nil handle for unknown tool")
	}
}

func TestManagerTopologicalSort(t *testing.T) {
	m := newTestManager(t)

	// Create manifests with dependencies
	m.manifests["aa-base"] = &Manifest{Name: "aa-base", Priority: 10}
	m.manifests["bb-derived"] = &Manifest{Name: "bb-derived", DependsOn: []string{"aa-base"}, Priority: 20}
	m.manifests["cc-top"] = &Manifest{Name: "cc-top", DependsOn: []string{"bb-derived"}, Priority: 30}

	sorted, _, err := m.topologicalSort()
	if err != nil {
		t.Fatalf("topologicalSort: %v", err)
	}

	if len(sorted) != 3 {
		t.Fatalf("got %d, want 3: %v", len(sorted), sorted)
	}

	// base must come before derived, derived before top
	indexOf := func(name string) int {
		for i, n := range sorted {
			if n == name {
				return i
			}
		}
		return -1
	}

	if indexOf("aa-base") >= indexOf("bb-derived") {
		t.Errorf("aa-base should come before bb-derived: %v", sorted)
	}
	if indexOf("bb-derived") >= indexOf("cc-top") {
		t.Errorf("bb-derived should come before cc-top: %v", sorted)
	}
}

func TestManagerCircularDependency(t *testing.T) {
	m := newTestManager(t)
	m.manifests["aa"] = &Manifest{Name: "aa", DependsOn: []string{"bb"}}
	m.manifests["bb"] = &Manifest{Name: "bb", DependsOn: []string{"aa"}}

	_, _, err := m.topologicalSort()
	if err == nil {
		t.Error("expected circular dependency error")
	}
}

func TestManagerMissingDependencySkips(t *testing.T) {
	m := newTestManager(t)
	m.manifests["aa"] = &Manifest{Name: "aa", DependsOn: []string{"missing"}}
	m.manifests["bb"] = &Manifest{Name: "bb", Priority: 10}

	sorted, skipped, err := m.topologicalSort()
	if err != nil {
		t.Fatalf("topologicalSort should not error: %v", err)
	}

	// aa should be skipped, bb should still be present
	if len(sorted) != 1 {
		t.Fatalf("got %d sorted, want 1: %v", len(sorted), sorted)
	}
	if sorted[0] != "bb" {
		t.Errorf("sorted[0] = %q, want bb", sorted[0])
	}

	// Verify skipped map has root-cause reason
	if reason, ok := skipped["aa"]; !ok {
		t.Error("aa should be in skipped map")
	} else if !strings.Contains(reason, "missing") {
		t.Errorf("reason should mention missing dep, got %q", reason)
	}
}

func TestManagerTransitiveDependencySkips(t *testing.T) {
	m := newTestManager(t)
	// cc depends on missing → skipped
	// bb depends on cc → transitively skipped
	// aa depends on bb → transitively skipped
	// dd is independent → kept
	m.manifests["cc"] = &Manifest{Name: "cc", DependsOn: []string{"missing"}, Priority: 30}
	m.manifests["bb"] = &Manifest{Name: "bb", DependsOn: []string{"cc"}, Priority: 20}
	m.manifests["aa"] = &Manifest{Name: "aa", DependsOn: []string{"bb"}, Priority: 10}
	m.manifests["dd"] = &Manifest{Name: "dd", Priority: 40}

	sorted, skipped, err := m.topologicalSort()
	if err != nil {
		t.Fatalf("topologicalSort: %v", err)
	}

	if len(sorted) != 1 {
		t.Fatalf("got %d sorted, want 1 (only dd): %v", len(sorted), sorted)
	}
	if sorted[0] != "dd" {
		t.Errorf("sorted[0] = %q, want dd", sorted[0])
	}

	// All three should be skipped with root-cause reasons
	if len(skipped) != 3 {
		t.Fatalf("got %d skipped, want 3: %v", len(skipped), skipped)
	}
	// cc: direct missing dep
	if !strings.Contains(skipped["cc"], "missing") {
		t.Errorf("cc reason should mention 'missing', got %q", skipped["cc"])
	}
	// bb: transitive, should mention cc and root cause
	if !strings.Contains(skipped["bb"], "cc") {
		t.Errorf("bb reason should mention 'cc', got %q", skipped["bb"])
	}
}

func TestManagerDiscoverRejectsUserCredentialGroup(t *testing.T) {
	sysDir := t.TempDir()
	userDir := t.TempDir()

	// System plugin claims credential_group "jira"
	createPluginInDir(t, sysDir, "jira", echoScript)
	// Manually add credential_group to the manifest
	manifestPath := filepath.Join(sysDir, "jira", "plugin.yaml")
	data, err := os.ReadFile(manifestPath) //nolint:gosec // test file path
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(data, []byte("credential_group: jira\n")...), 0o644); err != nil { //nolint:gosec // test config
		t.Fatal(err)
	}

	// User plugin tries to claim same credential_group
	createPluginInDir(t, userDir, "evil-jira", echoScript)
	manifestPath = filepath.Join(userDir, "evil-jira", "plugin.yaml")
	data, err = os.ReadFile(manifestPath) //nolint:gosec // test file path
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(data, []byte("credential_group: jira\n")...), 0o644); err != nil { //nolint:gosec // test config
		t.Fatal(err)
	}

	m := newTestManager(t)
	if err := m.Discover([]string{sysDir, userDir}, userDir); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	manifests := m.Manifests()
	if _, ok := manifests["jira"]; !ok {
		t.Error("system jira plugin should be registered")
	}
	if _, ok := manifests["evil-jira"]; ok {
		t.Error("user plugin with stolen credential_group should be rejected")
	}
}

func TestLoadAllDisablesPluginsOnEnvDirError(t *testing.T) {
	dir := t.TempDir()

	// Plugin WITH credential_group — should be disabled
	createPluginWithManifest(t, dir, "cred-plugin", `
name: cred-plugin
version: "1.0.0"
description: "Plugin with credentials"
execution: persistent
handler: ./handler.sh
credential_group: myservice
tools:
  - name: cred_tool
    description: "needs creds"
`)

	// Plugin WITHOUT credential_group — should NOT be disabled
	createPluginWithManifest(t, dir, "no-cred-plugin", `
name: no-cred-plugin
version: "1.0.0"
description: "Plugin without credentials"
execution: persistent
handler: ./handler.sh
tools:
  - name: nocred_tool
    description: "no creds needed"
`)

	cfg := config.DefaultConfig()
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "env.d has mode 0755, must not be accessible by group/other", dir, "", config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if err := m.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	disabled := m.DisabledPlugins()
	if dp, ok := disabled["cred-plugin"]; !ok {
		t.Error("cred-plugin should be disabled due to envDirError")
	} else {
		if !strings.Contains(dp.Reason, "must not be accessible") {
			t.Errorf("reason = %q, want dir permission error", dp.Reason)
		}
		if !strings.Contains(dp.Reason, "[env.d directory]") {
			t.Errorf("reason = %q, want [env.d directory] prefix", dp.Reason)
		}
	}
	if _, ok := disabled["no-cred-plugin"]; ok {
		t.Error("no-cred-plugin should NOT be disabled (no credential_group)")
	}
}

func TestReloadRechecksEnvDirPermissions(t *testing.T) {
	dir := t.TempDir()
	envDir := filepath.Join(dir, "env.d")
	if err := os.MkdirAll(envDir, 0o755); err != nil { //nolint:gosec // intentionally insecure for test
		t.Fatal(err)
	}

	createPluginWithManifest(t, dir, "svc-plugin", `
name: svc-plugin
version: "1.0.0"
description: "Service plugin"
execution: persistent
handler: ./handler.sh
credential_group: svc
tools:
  - name: svc_tool
    description: "a tool"
`)

	cfg := config.DefaultConfig()
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil,
		"env.d has mode 0755, must not be accessible by group/other",
		dir, envDir, config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if err := m.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	close(m.loadDone) // simulate StartPending completion

	// Plugin should be disabled.
	if _, ok := m.DisabledPlugins()["svc-plugin"]; !ok {
		t.Fatal("svc-plugin should be disabled")
	}

	// Reload while dir is still 0755 — should fail.
	if err := m.Reload(context.Background(), "svc-plugin"); err == nil {
		t.Fatal("Reload should fail while dir still has bad permissions")
	}
	if m.envDirError == "" {
		t.Error("envDirError should still be set")
	}

	// Fix dir permissions and create env file.
	if err := os.Chmod(envDir, 0o700); err != nil { //nolint:gosec // fixing perms in test
		t.Fatal(err)
	}
	envFile := filepath.Join(envDir, "svc.env")
	if err := os.WriteFile(envFile, []byte("SVC_TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Reload after fix — envDirError should clear, plugin should re-enable.
	// Load will fail (no real handler binary), but envDirError should
	// be cleared and the env group should be populated.
	_ = m.Reload(context.Background(), "svc-plugin")

	if m.envDirError != "" {
		t.Errorf("envDirError should be cleared after dir fix, got: %q", m.envDirError)
	}
	if vars := m.envGroups.Get("svc"); vars == nil {
		t.Error("env group 'svc' should be populated after reload")
	} else if vars["SVC_TOKEN"] != "secret" {
		t.Errorf("SVC_TOKEN = %q, want 'secret'", vars["SVC_TOKEN"])
	}
}

func TestReloadDirFixedButFileBadPerms(t *testing.T) {
	dir := t.TempDir()
	envDir := filepath.Join(dir, "env.d")
	if err := os.MkdirAll(envDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Create env file with bad permissions (0644).
	envFile := filepath.Join(envDir, "svc.env")
	if err := os.WriteFile(envFile, []byte("SVC_TOKEN=secret\n"), 0o644); err != nil { //nolint:gosec // intentionally insecure for test
		t.Fatal(err)
	}

	createPluginWithManifest(t, dir, "svc-plugin", `
name: svc-plugin
version: "1.0.0"
description: "Service plugin"
execution: persistent
handler: ./handler.sh
credential_group: svc
tools:
  - name: svc_tool
    description: "a tool"
`)

	cfg := config.DefaultConfig()
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil,
		"env.d had mode 0755, directory error",
		dir, envDir, config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if err := m.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	close(m.loadDone)

	if _, ok := m.DisabledPlugins()["svc-plugin"]; !ok {
		t.Fatal("svc-plugin should be disabled")
	}

	// Dir is now 0700 (fixed), but file is still 0644 (bad).
	// Reload should clear envDirError but fail on the file check.
	err := m.Reload(context.Background(), "svc-plugin")
	if err == nil {
		t.Fatal("Reload should fail due to bad file permissions")
	}

	if m.envDirError != "" {
		t.Error("envDirError should be cleared (directory is fine)")
	}
	if _, ok := m.DisabledPlugins()["svc-plugin"]; !ok {
		t.Error("svc-plugin should still be disabled (file perms bad)")
	}
}

// createPluginInDir creates a plugin inside an existing parent directory.
func createPluginInDir(t *testing.T, parentDir, name, script string) {
	t.Helper()
	pluginDir := filepath.Join(parentDir, name)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil { //nolint:gosec // test dir
		t.Fatal(err)
	}
	handlerPath := filepath.Join(pluginDir, "handler.sh")
	if err := os.WriteFile(handlerPath, []byte(script), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}
	manifest := `
name: ` + name + `
version: "1.0.0"
description: "Test plugin"
execution: persistent
handler: ./handler.sh
tools:
  - name: ` + name + `_test
    description: "A test tool"
    params: {}
`
	manifestPath := filepath.Join(pluginDir, "plugin.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil { //nolint:gosec // test config
		t.Fatal(err)
	}
}

func TestManagerDiscoverFirstWins(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	createPluginInDir(t, dir1, "samename", echoScript)
	createPluginInDir(t, dir2, "samename", echoScript)

	m := newTestManager(t)
	if err := m.Discover([]string{dir1, dir2}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	manifests := m.Manifests()
	if len(manifests) != 1 {
		t.Fatalf("got %d manifests, want 1", len(manifests))
	}

	got := manifests["samename"]
	if got == nil {
		t.Fatal("expected 'samename' manifest")
	}
	if !strings.HasPrefix(got.Dir, dir1) {
		t.Errorf("manifest Dir = %q, want prefix %q (first dir should win)", got.Dir, dir1)
	}
}

func TestManagerDiscoverUserCanAddNew(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	createPluginInDir(t, dir1, "system-only", echoScript)
	createPluginInDir(t, dir2, "user-only", echoScript)

	m := newTestManager(t)
	if err := m.Discover([]string{dir1, dir2}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	manifests := m.Manifests()
	if len(manifests) != 2 {
		t.Fatalf("got %d manifests, want 2", len(manifests))
	}
	if _, ok := manifests["system-only"]; !ok {
		t.Error("expected 'system-only' manifest")
	}
	if _, ok := manifests["user-only"]; !ok {
		t.Error("expected 'user-only' manifest")
	}
}

// createPluginWithManifest creates a plugin with a custom manifest YAML.
func createPluginWithManifest(t *testing.T, parentDir, name, manifestYAML string) {
	t.Helper()
	pluginDir := filepath.Join(parentDir, name)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil { //nolint:gosec // test dir
		t.Fatal(err)
	}
	handlerPath := filepath.Join(pluginDir, "handler.sh")
	if err := os.WriteFile(handlerPath, []byte(echoScript), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}
	manifestPath := filepath.Join(pluginDir, "plugin.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifestYAML), 0o644); err != nil { //nolint:gosec // test config
		t.Fatal(err)
	}
}

func TestCheckDisabledProvider_SingleType(t *testing.T) {
	dir := t.TempDir()
	createPluginWithManifest(t, dir, "krb-plugin", `
name: krb-plugin
version: "1.0.0"
description: "Kerberos plugin"
execution: persistent
handler: ./handler.sh
services:
  auth:
    type: kerberos/spnego
    spn: "HTTP@host"
tools:
  - name: krb_test
    description: "test"
`)

	cfg := config.DefaultConfig()
	cfg.Providers.Disabled = []string{"kerberos/spnego"}
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "", "", "", config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	manifest := m.manifests["krb-plugin"]
	reason := m.checkDisabledProvider(manifest)
	if reason == "" {
		t.Fatal("expected disabled reason for single-type kerberos plugin")
	}
	if !strings.Contains(reason, "kerberos/spnego") {
		t.Errorf("reason should mention kerberos/spnego, got: %s", reason)
	}
}

func TestCheckDisabledProvider_SingleTypeAlias(t *testing.T) {
	dir := t.TempDir()
	createPluginWithManifest(t, dir, "krb-alias", `
name: krb-alias
version: "1.0.0"
description: "Kerberos alias plugin"
execution: persistent
handler: ./handler.sh
services:
  auth:
    type: kerberos
    spn: "HTTP@host"
tools:
  - name: krb_alias_test
    description: "test"
`)

	cfg := config.DefaultConfig()
	cfg.Providers.Disabled = []string{"kerberos/spnego"}
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "", "", "", config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	manifest := m.manifests["krb-alias"]
	reason := m.checkDisabledProvider(manifest)
	if reason == "" {
		t.Fatal("expected disabled reason for kerberos alias")
	}
}

func TestCheckDisabledProvider_VariantAutoSelect(t *testing.T) {
	dir := t.TempDir()
	createPluginWithManifest(t, dir, "jira-like", `
name: jira-like
version: "1.0.0"
description: "Jira-like plugin"
execution: persistent
handler: ./handler.sh
services:
  auth:
    select: auto
    variants:
      cloud:
        type: bearer
        token: "tok"
      server-kerberos:
        type: kerberos/spnego
        spn: "HTTP@host"
tools:
  - name: jira_test
    description: "test"
`)

	cfg := config.DefaultConfig()
	cfg.Providers.Disabled = []string{"kerberos/spnego"}
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "", "", "", config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// With auto-select and bearer still available, plugin should NOT be disabled
	manifest := m.manifests["jira-like"]
	reason := m.checkDisabledProvider(manifest)
	if reason != "" {
		t.Errorf("plugin with viable variant should not be disabled, got: %s", reason)
	}
}

func TestCheckDisabledProvider_VariantAutoSelectAllDisabled(t *testing.T) {
	dir := t.TempDir()
	createPluginWithManifest(t, dir, "all-disabled", `
name: all-disabled
version: "1.0.0"
description: "All variants disabled"
execution: persistent
handler: ./handler.sh
services:
  auth:
    select: auto
    variants:
      krb1:
        type: kerberos/spnego
        spn: "HTTP@host1"
      krb2:
        type: kerberos/spnego
        spn: "HTTP@host2"
tools:
  - name: all_disabled_test
    description: "test"
`)

	cfg := config.DefaultConfig()
	cfg.Providers.Disabled = []string{"kerberos/spnego"}
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "", "", "", config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	manifest := m.manifests["all-disabled"]
	reason := m.checkDisabledProvider(manifest)
	if reason == "" {
		t.Fatal("expected disabled reason when all variants use disabled providers")
	}
}

func TestCheckDisabledProvider_ExplicitSelectDisabled(t *testing.T) {
	dir := t.TempDir()
	createPluginWithManifest(t, dir, "explicit-krb", `
name: explicit-krb
version: "1.0.0"
description: "Explicit kerberos select"
execution: persistent
handler: ./handler.sh
services:
  auth:
    select: server-kerberos
    variants:
      cloud:
        type: bearer
        token: "tok"
      server-kerberos:
        type: kerberos/spnego
        spn: "HTTP@host"
tools:
  - name: explicit_krb_test
    description: "test"
`)

	cfg := config.DefaultConfig()
	cfg.Providers.Disabled = []string{"kerberos/spnego"}
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "", "", "", config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// Explicit select of disabled variant should disable the plugin
	manifest := m.manifests["explicit-krb"]
	reason := m.checkDisabledProvider(manifest)
	if reason == "" {
		t.Fatal("expected disabled reason for explicit select of disabled variant")
	}
	if !strings.Contains(reason, "server-kerberos") {
		t.Errorf("reason should mention variant name, got: %s", reason)
	}
}

func TestCheckDisabledProvider_NoAuth(t *testing.T) {
	dir := t.TempDir()
	createPluginInDir(t, dir, "no-auth", echoScript)

	cfg := config.DefaultConfig()
	cfg.Providers.Disabled = []string{"kerberos/spnego"}
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "", "", "", config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	manifest := m.manifests["no-auth"]
	reason := m.checkDisabledProvider(manifest)
	if reason != "" {
		t.Errorf("plugin without auth should not be affected, got: %s", reason)
	}
}

func TestAllowlistOnlyLoadsEnabledPlugins(t *testing.T) {
	dir := t.TempDir()
	createPluginInDir(t, dir, "alpha", echoScript)
	createPluginInDir(t, dir, "beta", echoScript)
	createPluginInDir(t, dir, "gamma", echoScript)

	cfg := config.DefaultConfig()
	cfg.Plugins.Enabled = []string{"alpha", "gamma"}
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "", "", "", config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	manifests := m.Manifests()
	if _, ok := manifests["alpha"]; !ok {
		t.Error("expected 'alpha' in Manifests()")
	}
	if _, ok := manifests["gamma"]; !ok {
		t.Error("expected 'gamma' in Manifests()")
	}
	if _, ok := manifests["beta"]; ok {
		t.Error("'beta' should not be in Manifests() with allowlist")
	}

	configDisabled := m.ConfigDisabledPlugins()
	if _, ok := configDisabled["beta"]; !ok {
		t.Error("expected 'beta' in ConfigDisabledPlugins()")
	}
}

func TestAllowlistOverridesDisabled(t *testing.T) {
	dir := t.TempDir()
	createPluginInDir(t, dir, "alpha", echoScript)
	createPluginInDir(t, dir, "beta", echoScript)

	cfg := config.DefaultConfig()
	cfg.Plugins.Enabled = []string{"alpha", "beta"}
	cfg.Plugins.Disabled = []string{"beta"} // should be ignored

	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "", "", "", config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// Both should be loaded since enabled takes precedence
	manifests := m.Manifests()
	if _, ok := manifests["alpha"]; !ok {
		t.Error("expected 'alpha' in Manifests()")
	}
	if _, ok := manifests["beta"]; !ok {
		t.Error("expected 'beta' in Manifests() — allowlist overrides disabled")
	}
}

func TestEmptyAllowlistUsesDefault(t *testing.T) {
	dir := t.TempDir()
	createPluginInDir(t, dir, "alpha", echoScript)
	createPluginInDir(t, dir, "beta", echoScript)

	cfg := config.DefaultConfig()
	cfg.Plugins.Enabled = []string{} // empty = not active
	cfg.Plugins.Disabled = []string{"beta"}

	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "", "", "", config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	manifests := m.Manifests()
	if _, ok := manifests["alpha"]; !ok {
		t.Error("expected 'alpha' in Manifests()")
	}
	if _, ok := manifests["beta"]; ok {
		t.Error("'beta' should be disabled via blocklist when allowlist is empty")
	}
}

func TestDependencyLevelsAllIndependent(t *testing.T) {
	m := newTestManager(t)
	m.manifests["aa"] = &Manifest{Name: "aa", Priority: 10}
	m.manifests["bb"] = &Manifest{Name: "bb", Priority: 20}
	m.manifests["cc"] = &Manifest{Name: "cc", Priority: 30}

	sorted, _, err := m.topologicalSort()
	if err != nil {
		t.Fatalf("topologicalSort: %v", err)
	}

	levels := m.dependencyLevels(sorted)
	if len(levels) != 1 {
		t.Fatalf("got %d levels, want 1: %v", len(levels), levels)
	}
	if len(levels[0]) != 3 {
		t.Errorf("level 0 has %d plugins, want 3", len(levels[0]))
	}
}

func TestDependencyLevelsChain(t *testing.T) {
	m := newTestManager(t)
	m.manifests["aa"] = &Manifest{Name: "aa", Priority: 10}
	m.manifests["bb"] = &Manifest{Name: "bb", DependsOn: []string{"aa"}, Priority: 20}
	m.manifests["cc"] = &Manifest{Name: "cc", DependsOn: []string{"bb"}, Priority: 30}

	sorted, _, err := m.topologicalSort()
	if err != nil {
		t.Fatalf("topologicalSort: %v", err)
	}

	levels := m.dependencyLevels(sorted)
	if len(levels) != 3 {
		t.Fatalf("got %d levels, want 3: %v", len(levels), levels)
	}
	if levels[0][0] != "aa" {
		t.Errorf("level 0 = %v, want [aa]", levels[0])
	}
	if levels[1][0] != "bb" {
		t.Errorf("level 1 = %v, want [bb]", levels[1])
	}
	if levels[2][0] != "cc" {
		t.Errorf("level 2 = %v, want [cc]", levels[2])
	}
}

func TestDependencyLevelsMixed(t *testing.T) {
	m := newTestManager(t)
	// aa, bb independent (level 0)
	// cc depends on aa (level 1)
	// dd depends on cc (level 2)
	m.manifests["aa"] = &Manifest{Name: "aa", Priority: 10}
	m.manifests["bb"] = &Manifest{Name: "bb", Priority: 20}
	m.manifests["cc"] = &Manifest{Name: "cc", DependsOn: []string{"aa"}, Priority: 30}
	m.manifests["dd"] = &Manifest{Name: "dd", DependsOn: []string{"cc"}, Priority: 40}

	sorted, _, err := m.topologicalSort()
	if err != nil {
		t.Fatalf("topologicalSort: %v", err)
	}

	levels := m.dependencyLevels(sorted)
	if len(levels) != 3 {
		t.Fatalf("got %d levels, want 3: %v", len(levels), levels)
	}
	// Level 0 should have aa and bb
	if len(levels[0]) != 2 {
		t.Errorf("level 0 has %d plugins, want 2: %v", len(levels[0]), levels[0])
	}
	// Level 1 should have cc
	if len(levels[1]) != 1 || levels[1][0] != "cc" {
		t.Errorf("level 1 = %v, want [cc]", levels[1])
	}
	// Level 2 should have dd
	if len(levels[2]) != 1 || levels[2][0] != "dd" {
		t.Errorf("level 2 = %v, want [dd]", levels[2])
	}
}

func TestDependencyLevelsEmpty(t *testing.T) {
	m := newTestManager(t)
	levels := m.dependencyLevels(nil)
	if len(levels) != 0 {
		t.Errorf("got %d levels, want 0", len(levels))
	}
}

func TestLoadAllParallelMultiplePlugins(t *testing.T) {
	dir := t.TempDir()
	createPluginInDir(t, dir, "alpha", echoScript)
	createPluginInDir(t, dir, "beta", echoScript)
	createPluginInDir(t, dir, "gamma", echoScript)

	m := newTestManager(t)
	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	ctx := context.Background()
	if err := m.LoadAll(ctx); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	m.StartPending(ctx)
	defer m.ShutdownAll(ctx)

	loaded := m.LoadedPlugins()
	if len(loaded) != 3 {
		t.Fatalf("loaded %d plugins, want 3: %v", len(loaded), loaded)
	}

	// Verify each plugin is callable
	for _, name := range []string{"alpha", "beta", "gamma"} {
		owner, handle := m.CallTool(ctx, name+"_test")
		if owner != name {
			t.Errorf("owner of %s_test = %q, want %q", name, owner, name)
		}
		if handle == nil {
			t.Errorf("handle for %s is nil", name)
		}
	}
}

func TestLoadAllParallelOneFailsOthersSucceed(t *testing.T) {
	dir := t.TempDir()

	// good plugins use echoScript, bad plugin exits immediately
	createPluginInDir(t, dir, "good-one", echoScript)
	createPluginInDir(t, dir, "good-two", echoScript)
	createPluginInDir(t, dir, "bad-one", `#!/bin/bash
exit 1
`)

	m := newTestManager(t)
	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	ctx := context.Background()
	if err := m.LoadAll(ctx); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	m.StartPending(ctx)
	defer m.ShutdownAll(ctx)

	// bad-one fails during Start and is removed from handles
	loaded := m.LoadedPlugins()
	if len(loaded) != 2 {
		t.Fatalf("loaded %d plugins, want 2: %v", len(loaded), loaded)
	}

	// Good plugins should be callable
	for _, name := range []string{"good-one", "good-two"} {
		if h := m.Handle(name); h == nil {
			t.Errorf("expected handle for %s", name)
		}
	}

	// Bad plugin should not be loaded
	if h := m.Handle("bad-one"); h != nil {
		t.Error("bad-one should not have a handle")
	}
}

func TestPreparePluginDoesNotStart(t *testing.T) {
	dir := setupTestPlugin(t, "nostart", echoScript)

	m := newTestManager(t)
	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	handle, err := m.preparePlugin(context.Background(), "nostart")
	if err != nil {
		t.Fatalf("preparePlugin: %v", err)
	}

	// Handle should exist but process should not have been created
	if handle.process != nil {
		t.Error("expected nil process before Start()")
	}

	// Handle should not be in the loaded list
	if m.Handle("nostart") != nil {
		t.Error("preparePlugin should not store the handle in Manager")
	}
}

func TestSanitizeReason(t *testing.T) {
	tests := []struct {
		name    string
		workdir string
		envDir  string
		reason  string
		want    string
	}{
		{
			name:    "strips workdir prefix",
			workdir: "/home/user/.config/wtmcp",
			reason:  "[keylime] read ca_cert /home/user/.config/wtmcp/credentials/keylime/cacert.crt: no such file",
			want:    "[keylime] read ca_cert credentials/keylime/cacert.crt: no such file",
		},
		{
			name:    "strips multiple occurrences",
			workdir: "/home/user/.config/wtmcp",
			reason:  "stat /home/user/.config/wtmcp/env.d/jira.env: /home/user/.config/wtmcp/env.d/jira.env has mode 0644",
			want:    "stat env.d/jira.env: env.d/jira.env has mode 0644",
		},
		{
			name:    "no workdir is no-op",
			workdir: "",
			reason:  "some error message",
			want:    "some error message",
		},
		{
			name:    "no match is no-op",
			workdir: "/home/user/.config/wtmcp",
			reason:  "auth provider disabled",
			want:    "auth provider disabled",
		},
		{
			name:    "strips custom envDir outside workdir",
			workdir: "/home/user/.config/wtmcp",
			envDir:  "/opt/secrets/env.d",
			reason:  "/opt/secrets/env.d has mode 0755, must not be accessible",
			want:    "env.d has mode 0755, must not be accessible",
		},
		{
			name:    "envDir under workdir uses workdir stripping only",
			workdir: "/home/user/.config/wtmcp",
			envDir:  "/home/user/.config/wtmcp/env.d",
			reason:  "/home/user/.config/wtmcp/env.d has mode 0755",
			want:    "env.d has mode 0755",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Manager{workdir: tt.workdir, envDir: tt.envDir}
			got := m.sanitizeReason(tt.reason)
			if got != tt.want {
				t.Errorf("sanitizeReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDisablePlugin(t *testing.T) {
	m := newTestManager(t)
	m.manifests["test-plug"] = &Manifest{
		Name: "test-plug",
		Tools: []ToolDef{
			{Name: "test_plug_get", Description: "Get things", Access: "read"},
		},
	}

	m.disablePlugin("test-plug", "CA cert not found")

	disabled := m.DisabledPlugins()
	dp, ok := disabled["test-plug"]
	if !ok {
		t.Fatal("test-plug should be in disabled map")
	}
	if dp.Reason != "CA cert not found" {
		t.Errorf("reason = %q, want %q", dp.Reason, "CA cert not found")
	}
	if dp.Manifest == nil {
		t.Fatal("disabled plugin should have manifest")
	}
	if len(dp.Manifest.Tools) != 1 {
		t.Errorf("manifest should have 1 tool, got %d", len(dp.Manifest.Tools))
	}
}

func TestReloadConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	envDir := filepath.Join(dir, "env.d")
	if err := os.MkdirAll(envDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envDir, "svc.env"),
		[]byte("TOKEN=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	createPluginWithManifest(t, dir, "svc-plugin", `
name: svc-plugin
version: "1.0.0"
description: "Service plugin"
execution: persistent
handler: ./handler.sh
credential_group: svc
tools:
  - name: svc_tool
    description: "a tool"
`)

	cfg := config.DefaultConfig()
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "",
		dir, envDir, config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if err := m.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	close(m.loadDone)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.Reload(context.Background(), "svc-plugin")
		}()
	}

	// Concurrently read DisabledPlugins while reloads are in flight.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.DisabledPlugins()
		}()
	}

	wg.Wait()
}

func TestCallToolDuringReload(t *testing.T) {
	dir := t.TempDir()
	envDir := filepath.Join(dir, "env.d")
	if err := os.MkdirAll(envDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envDir, "svc.env"),
		[]byte("TOKEN=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	createPluginWithManifest(t, dir, "svc-plugin", `
name: svc-plugin
version: "1.0.0"
description: "Service plugin"
execution: persistent
handler: ./handler.sh
credential_group: svc
tools:
  - name: svc_tool
    description: "a tool"
`)

	cfg := config.DefaultConfig()
	authReg := auth.NewRegistry()
	cacheStore := cache.NewMemoryStore()
	p := proxy.New(nil, cfg.Plugins.MaxMessageSize, cfg.HTTP.Timeout)
	m := NewManager(authReg, p, cacheStore, cfg, nil, nil, "",
		dir, envDir, config.EnvLoadOptions{}, "")

	if err := m.Discover([]string{dir}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if err := m.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	close(m.loadDone)

	ctx := context.Background()
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.CallTool(ctx, "svc_tool")
			}
		}()
	}

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_ = m.Reload(ctx, "svc-plugin")
			}
		}()
	}

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.Manifests()
		}()
	}

	wg.Wait()
}
