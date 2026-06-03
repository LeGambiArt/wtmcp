package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeGambiArt/wtmcp/internal/protocol"
)

func TestBuildPluginEnv(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/home/test")
	t.Setenv("SECRET_TOKEN", "should-not-appear")

	m := &Manifest{
		Name: "test-plugin",
		Env:  []string{"CUSTOM_VAR"},
	}

	// CUSTOM_VAR comes from the scoped group vars, not process env
	groupVars := map[string]string{"CUSTOM_VAR": "custom-value"}
	env := buildPluginEnv(m, groupVars)

	envMap := make(map[string]string)
	for _, e := range env {
		parts := splitEnvVar(e)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["PATH"] != "/usr/bin" {
		t.Errorf("PATH = %q, want /usr/bin", envMap["PATH"])
	}
	if envMap["HOME"] != "/home/test" {
		t.Errorf("HOME = %q, want /home/test", envMap["HOME"])
	}
	if _, ok := envMap["SECRET_TOKEN"]; ok {
		t.Error("SECRET_TOKEN should not be in plugin env")
	}
	if envMap["CUSTOM_VAR"] != "custom-value" {
		t.Errorf("CUSTOM_VAR = %q, want custom-value", envMap["CUSTOM_VAR"])
	}
}

func TestBuildPluginEnvIgnoresProcessEnv(t *testing.T) {
	// Even if a var exists in the process env, it should NOT be
	// passed through unless it's in the group vars
	t.Setenv("JIRA_TOKEN", "process-secret")

	m := &Manifest{
		Name: "test-plugin",
		Env:  []string{"JIRA_TOKEN"},
	}

	// No group vars — plugin should not get JIRA_TOKEN
	env := buildPluginEnv(m, nil)

	for _, e := range env {
		parts := splitEnvVar(e)
		if len(parts) == 2 && parts[0] == "JIRA_TOKEN" {
			t.Error("JIRA_TOKEN should not be passed from process env")
		}
	}
}

func TestBuildPluginEnvPassthroughAll(t *testing.T) {
	m := &Manifest{
		Name:           "test-gitlab",
		EnvPassthrough: "all",
		Env:            []string{"GITLAB_TOKEN"}, // listed but not used as filter
	}

	groupVars := map[string]string{
		"GITLAB_TOKEN":          "tok1",
		"GITLAB_INTERNAL_TOKEN": "tok2",
		"GITLAB_INTERNAL_URL":   "https://internal.example.com",
		"GITLAB_SSL_VERIFY":     "false",
	}
	env := buildPluginEnv(m, groupVars)

	envMap := make(map[string]string)
	for _, e := range env {
		parts := splitEnvVar(e)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// All group vars should be passed when env_passthrough is "all"
	for key, want := range groupVars {
		if envMap[key] != want {
			t.Errorf("%s = %q, want %q", key, envMap[key], want)
		}
	}
}

func splitEnvVar(s string) []string {
	for i, c := range s {
		if c == '=' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}

func TestProcessStartStopPersistent(t *testing.T) {
	dir := t.TempDir()

	// Create a simple persistent handler that responds to init and shutdown
	script := `#!/bin/bash
while IFS= read -r line; do
  type=$(echo "$line" | jq -r '.type')
  id=$(echo "$line" | jq -r '.id')
  case "$type" in
    init)
      echo "{\"id\":\"$id\",\"type\":\"init_ok\"}"
      ;;
    shutdown)
      echo "{\"id\":\"$id\",\"type\":\"shutdown_ok\"}"
      exit 0
      ;;
  esac
done
`
	handlerPath := filepath.Join(dir, "handler.sh")
	if err := os.WriteFile(handlerPath, []byte(script), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}

	m := &Manifest{
		Name:        "test-persistent",
		Execution:   "persistent",
		Handler:     "./handler.sh",
		Concurrency: 1,
		Dir:         dir,
	}

	handler := &mockServiceHandler{}
	proc := NewProcess(m, handler, ProcessConfig{
		InitTimeout:       5 * time.Second,
		ShutdownTimeout:   5 * time.Second,
		ShutdownKillAfter: 2 * time.Second,
		MaxMessageSize:    1024 * 1024,
	}, nil)

	if proc.State() != StateUnloaded {
		t.Errorf("initial state = %d, want Unloaded", proc.State())
	}

	ctx := context.Background()
	if err := proc.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if proc.State() != StateRunning {
		t.Errorf("after start state = %d, want Running", proc.State())
	}

	if err := proc.Stop(ctx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestProcessSurvivesContextCancellation(t *testing.T) {
	dir := t.TempDir()

	// Handler that responds to init, tool_call, and shutdown
	script := `#!/bin/bash
while IFS= read -r line; do
  type=$(echo "$line" | jq -r '.type')
  id=$(echo "$line" | jq -r '.id')
  case "$type" in
    init)
      echo "{\"id\":\"$id\",\"type\":\"init_ok\"}"
      ;;
    tool_call)
      echo "{\"id\":\"$id\",\"type\":\"tool_result\",\"result\":{\"alive\":true}}"
      ;;
    shutdown)
      echo "{\"id\":\"$id\",\"type\":\"shutdown_ok\"}"
      exit 0
      ;;
  esac
done
`
	handlerPath := filepath.Join(dir, "handler.sh")
	if err := os.WriteFile(handlerPath, []byte(script), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}

	m := &Manifest{
		Name:        "test-ctx-cancel",
		Execution:   "persistent",
		Handler:     "./handler.sh",
		Concurrency: 1,
		Dir:         dir,
	}

	proc := NewProcess(m, &mockServiceHandler{}, ProcessConfig{
		InitTimeout:       5 * time.Second,
		ShutdownTimeout:   5 * time.Second,
		ShutdownKillAfter: 2 * time.Second,
		MaxMessageSize:    1024 * 1024,
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	if err := proc.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Cancel the context (simulates MCP request context being cancelled
	// after plugin_reload returns).
	cancel()
	// Give the runtime time to deliver the cancellation signal; if the
	// old exec.CommandContext behaviour were still in place, the process
	// would be killed within this window.
	time.Sleep(100 * time.Millisecond)

	// Process must still be alive — verify by sending a tool call.
	id := proc.Transport.GenerateID("test")
	resp, err := proc.Transport.SendAndWait(context.Background(), id, protocol.Message{
		Type: protocol.TypeToolCall,
		Tool: "ping",
	})
	if err != nil {
		t.Fatalf("tool call after context cancel failed: %v", err)
	}
	if resp.Type != protocol.TypeToolResult {
		t.Errorf("response type = %q, want tool_result", resp.Type)
	}

	// Explicit stop must still work.
	if err := proc.Stop(context.Background()); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestProcessStartOneshot(t *testing.T) {
	dir := t.TempDir()

	// Oneshot handler just reads and exits (no init needed)
	script := `#!/bin/bash
read -r INPUT
ID=$(echo "$INPUT" | jq -r '.id')
echo "{\"id\":\"$ID\",\"type\":\"tool_result\",\"result\":{\"ok\":true}}"
`
	handlerPath := filepath.Join(dir, "handler.sh")
	if err := os.WriteFile(handlerPath, []byte(script), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}

	m := &Manifest{
		Name:        "test-oneshot",
		Execution:   "oneshot",
		Handler:     "./handler.sh",
		Concurrency: 1,
		Dir:         dir,
	}

	handler := &mockServiceHandler{}
	proc := NewProcess(m, handler, ProcessConfig{
		InitTimeout:       5 * time.Second,
		ShutdownTimeout:   5 * time.Second,
		ShutdownKillAfter: 2 * time.Second,
		MaxMessageSize:    1024 * 1024,
	}, nil)

	// Oneshot plugins start without init
	ctx := context.Background()
	if err := proc.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if proc.State() != StateRunning {
		t.Errorf("state = %d, want Running", proc.State())
	}
}

func TestProcessInitFailure(t *testing.T) {
	dir := t.TempDir()

	// Handler that returns init_error
	script := `#!/bin/bash
while IFS= read -r line; do
  id=$(echo "$line" | jq -r '.id')
  echo "{\"id\":\"$id\",\"type\":\"init_error\",\"error\":{\"code\":\"bad_config\",\"message\":\"missing key\"}}"
  exit 1
done
`
	handlerPath := filepath.Join(dir, "handler.sh")
	if err := os.WriteFile(handlerPath, []byte(script), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}

	m := &Manifest{
		Name:        "test-fail",
		Execution:   "persistent",
		Handler:     "./handler.sh",
		Concurrency: 1,
		Dir:         dir,
	}

	proc := NewProcess(m, &mockServiceHandler{}, ProcessConfig{
		InitTimeout:       5 * time.Second,
		ShutdownTimeout:   5 * time.Second,
		ShutdownKillAfter: 2 * time.Second,
		MaxMessageSize:    1024 * 1024,
	}, nil)

	err := proc.Start(context.Background())
	if err == nil {
		t.Fatal("expected init to fail")
	}
	if proc.State() != StateFailed {
		t.Errorf("state = %d, want Failed", proc.State())
	}
}
