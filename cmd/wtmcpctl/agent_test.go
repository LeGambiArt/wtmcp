package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDir_Empty(t *testing.T) {
	dir, err := resolveDir("")
	if err != nil {
		t.Fatalf("resolveDir(\"\") failed: %v", err)
	}
	if dir == "" {
		t.Error("resolveDir(\"\") returned empty string")
	}
	// Should return current working directory
	cwd, _ := os.Getwd()
	if dir != cwd {
		t.Errorf("resolveDir(\"\") = %q, want %q", dir, cwd)
	}
}

func TestResolveDir_Relative(t *testing.T) {
	dir, err := resolveDir(".")
	if err != nil {
		t.Fatalf("resolveDir(\".\") failed: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("resolveDir(\".\") = %q is not absolute", dir)
	}
}

func TestReadJSONConfig_NonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "nonexistent.json")

	config, err := readJSONConfig(path)
	if err != nil {
		t.Fatalf("readJSONConfig() returned error for non-existent file: %v", err)
	}

	if config == nil {
		t.Fatal("readJSONConfig() returned nil map")
	}

	if len(config) != 0 {
		t.Errorf("readJSONConfig() returned non-empty map: %v", config)
	}
}

func TestReadJSONConfig_ValidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")

	// Write valid JSON
	content := `{"key": "value", "number": 42}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	config, err := readJSONConfig(path)
	if err != nil {
		t.Fatalf("readJSONConfig() failed: %v", err)
	}

	if config["key"] != "value" {
		t.Errorf("config[\"key\"] = %v, want \"value\"", config["key"])
	}

	// JSON numbers are float64
	if num, ok := config["number"].(float64); !ok || num != 42 {
		t.Errorf("config[\"number\"] = %v, want 42", config["number"])
	}
}

func TestReadJSONConfig_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "invalid.json")

	// Write invalid JSON
	content := `{invalid json`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, err := readJSONConfig(path)
	if err == nil {
		t.Error("readJSONConfig() should return error for invalid JSON")
	}
}

func TestReadJSONConfig_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "empty.json")

	// Write empty file
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, err := readJSONConfig(path)
	if err == nil {
		t.Error("readJSONConfig() should return error for empty file")
	}
}

func TestWriteJSONConfig(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "output.json")

	data := map[string]any{
		"key":    "value",
		"number": 42,
	}

	if err := writeJSONConfig(path, data); err != nil {
		t.Fatalf("writeJSONConfig() failed: %v", err)
	}

	// Read it back
	content, err := os.ReadFile(path) // #nosec G304 -- path is from test temp directory
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	// Check for trailing newline
	if len(content) == 0 || content[len(content)-1] != '\n' {
		t.Error("writeJSONConfig() should add trailing newline")
	}

	// Parse it
	var parsed map[string]any
	if err := json.Unmarshal(content, &parsed); err != nil {
		t.Fatalf("written JSON is invalid: %v", err)
	}

	if parsed["key"] != "value" {
		t.Errorf("parsed[\"key\"] = %v, want \"value\"", parsed["key"])
	}
}

func TestValidAgentNames(t *testing.T) {
	names := validAgentNames()

	if len(names) != len(supportedAgents) {
		t.Errorf("validAgentNames() returned %d names, want %d", len(names), len(supportedAgents))
	}

	// Check that it's sorted
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Errorf("validAgentNames() is not sorted: %v", names)
			break
		}
	}

	// Check that all expected agents are present (including aliases)
	expectedAgents := []string{"claude", "claude-code", "cursor", "gemini"}
	for _, expected := range expectedAgents {
		found := false
		for _, name := range names {
			if name == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("validAgentNames() missing expected agent: %s", expected)
		}
	}
}

func TestAgentEnable_Claude_Alias(t *testing.T) {
	t.Skip("test requires wtmcp binary in PATH")
	tmpDir := t.TempDir()

	// Enable using "claude" alias
	if err := agentEnable("claude", tmpDir); err != nil {
		t.Fatalf("agentEnable() failed: %v", err)
	}

	// Check that .mcp.json was created (same as claude-code)
	configPath := filepath.Join(tmpDir, ".mcp.json")
	content, err := os.ReadFile(configPath) // #nosec G304 -- path is from test temp directory
	if err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	// Parse the config
	var config map[string]any
	if err := json.Unmarshal(content, &config); err != nil {
		t.Fatalf("invalid JSON in config: %v", err)
	}

	// Check that it has the same structure as claude-code
	mcpServers := config["mcpServers"].(map[string]any)
	wtmcp := mcpServers[mcpServerKey].(map[string]any)

	// Should have type field (same as claude-code)
	if wtmcp["type"] != "stdio" {
		t.Errorf("type = %v, want \"stdio\"", wtmcp["type"])
	}
}

func TestAgentEnable_ClaudeVsClaudeCode_SameConfig(t *testing.T) {
	t.Skip("test requires wtmcp binary in PATH")
	// Test that "claude" and "claude-code" produce identical configs
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()

	// Enable with "claude"
	if err := agentEnable("claude", tmpDir1); err != nil {
		t.Fatalf("agentEnable('claude') failed: %v", err)
	}

	// Enable with "claude-code"
	if err := agentEnable("claude-code", tmpDir2); err != nil {
		t.Fatalf("agentEnable('claude-code') failed: %v", err)
	}

	// Read both configs
	config1, _ := readJSONConfig(filepath.Join(tmpDir1, ".mcp.json"))
	config2, _ := readJSONConfig(filepath.Join(tmpDir2, ".mcp.json"))

	// Both should have mcpServers
	if config1["mcpServers"] == nil || config2["mcpServers"] == nil {
		t.Fatal("one or both configs missing mcpServers")
	}

	// Both should have what-the-mcp entry
	servers1 := config1["mcpServers"].(map[string]any)
	servers2 := config2["mcpServers"].(map[string]any)

	wtmcp1 := servers1[mcpServerKey].(map[string]any)
	wtmcp2 := servers2[mcpServerKey].(map[string]any)

	// Compare key fields
	if wtmcp1["type"] != wtmcp2["type"] {
		t.Error("type field differs between claude and claude-code")
	}
	if wtmcp1["command"] != wtmcp2["command"] {
		t.Error("command field differs between claude and claude-code")
	}
}

func TestAgentDisable_Claude_Alias(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".mcp.json")

	// Create config
	config := map[string]any{
		"mcpServers": map[string]any{
			mcpServerKey: map[string]any{
				"command": "/path/to/wtmcp",
				"type":    "stdio",
			},
		},
	}
	if err := writeJSONConfig(configPath, config); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Disable using "claude" alias
	if err := agentDisable("claude", tmpDir); err != nil {
		t.Fatalf("agentDisable() failed: %v", err)
	}

	// File should be removed (same behavior as claude-code)
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Error("config file should be removed when empty")
	}
}

func TestAgentEnable_ClaudeCode_NewFile(t *testing.T) {
	t.Skip("test requires wtmcp binary in PATH")
	tmpDir := t.TempDir()

	if err := agentEnable("claude-code", tmpDir); err != nil {
		t.Fatalf("agentEnable() failed: %v", err)
	}

	// Check that .mcp.json was created
	configPath := filepath.Join(tmpDir, ".mcp.json")
	content, err := os.ReadFile(configPath) // #nosec G304 -- path is from test temp directory
	if err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	// Parse the config
	var config map[string]any
	if err := json.Unmarshal(content, &config); err != nil {
		t.Fatalf("invalid JSON in config: %v", err)
	}

	// Check mcpServers
	mcpServers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("mcpServers not found or wrong type")
	}

	// Check what-the-mcp entry
	wtmcp, ok := mcpServers[mcpServerKey].(map[string]any)
	if !ok {
		t.Fatalf("what-the-mcp entry not found or wrong type")
	}

	// Check type field (required for Claude Code)
	if wtmcp["type"] != "stdio" {
		t.Errorf("type = %v, want \"stdio\"", wtmcp["type"])
	}

	// Check command field
	if _, ok := wtmcp["command"].(string); !ok {
		t.Error("command field missing or wrong type")
	}

	// Check args field
	if _, ok := wtmcp["args"].([]any); !ok {
		t.Error("args field missing or wrong type")
	}
}

func TestAgentEnable_Gemini_CreatesDirectory(t *testing.T) {
	t.Skip("test requires wtmcp binary in PATH")
	tmpDir := t.TempDir()

	if err := agentEnable("gemini", tmpDir); err != nil {
		t.Fatalf("agentEnable() failed: %v", err)
	}

	// Check that .gemini directory was created
	geminiDir := filepath.Join(tmpDir, ".gemini")
	info, err := os.Stat(geminiDir)
	if err != nil {
		t.Fatalf(".gemini directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error(".gemini is not a directory")
	}

	// Check that settings.json was created
	configPath := filepath.Join(geminiDir, "settings.json")
	content, err := os.ReadFile(configPath) // #nosec G304 -- path is from test temp directory
	if err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}

	// Parse the config
	var config map[string]any
	if err := json.Unmarshal(content, &config); err != nil {
		t.Fatalf("invalid JSON in config: %v", err)
	}

	// Check that type field is NOT present (Gemini doesn't need it)
	mcpServers := config["mcpServers"].(map[string]any)
	wtmcp := mcpServers[mcpServerKey].(map[string]any)
	if _, hasType := wtmcp["type"]; hasType {
		t.Error("Gemini config should not have type field")
	}
}

func TestAgentEnable_Cursor_CreatesDirectory(t *testing.T) {
	t.Skip("test requires wtmcp binary in PATH")
	tmpDir := t.TempDir()

	if err := agentEnable("cursor", tmpDir); err != nil {
		t.Fatalf("agentEnable() failed: %v", err)
	}

	// Check that .cursor directory was created
	cursorDir := filepath.Join(tmpDir, ".cursor")
	info, err := os.Stat(cursorDir)
	if err != nil {
		t.Fatalf(".cursor directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error(".cursor is not a directory")
	}

	// Check that mcp.json was created
	configPath := filepath.Join(cursorDir, "mcp.json")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("mcp.json not created: %v", err)
	}
}

func TestAgentEnable_ExistingServers(t *testing.T) {
	t.Skip("test requires wtmcp binary in PATH")
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".mcp.json")

	// Write existing config with another server
	existingConfig := map[string]any{
		"mcpServers": map[string]any{
			"other-server": map[string]any{
				"command": "/path/to/other",
			},
		},
	}
	if err := writeJSONConfig(configPath, existingConfig); err != nil {
		t.Fatalf("failed to write existing config: %v", err)
	}

	// Enable wtmcp
	if err := agentEnable("claude-code", tmpDir); err != nil {
		t.Fatalf("agentEnable() failed: %v", err)
	}

	// Read and parse
	config, err := readJSONConfig(configPath)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}

	mcpServers := config["mcpServers"].(map[string]any)

	// Check that both servers exist
	if _, ok := mcpServers["other-server"]; !ok {
		t.Error("other-server was removed")
	}
	if _, ok := mcpServers[mcpServerKey]; !ok {
		t.Error("what-the-mcp was not added")
	}
}

func TestAgentEnable_Gemini_PreservesOtherSettings(t *testing.T) {
	t.Skip("test requires wtmcp binary in PATH")
	tmpDir := t.TempDir()
	geminiDir := filepath.Join(tmpDir, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o750); err != nil {
		t.Fatalf("failed to create .gemini dir: %v", err)
	}

	configPath := filepath.Join(geminiDir, "settings.json")

	// Write existing config with other settings
	existingConfig := map[string]any{
		"theme":    "dark",
		"language": "en",
		"mcpServers": map[string]any{
			"other-server": map[string]any{
				"command": "/path/to/other",
			},
		},
	}
	if err := writeJSONConfig(configPath, existingConfig); err != nil {
		t.Fatalf("failed to write existing config: %v", err)
	}

	// Enable wtmcp
	if err := agentEnable("gemini", tmpDir); err != nil {
		t.Fatalf("agentEnable() failed: %v", err)
	}

	// Read and parse
	config, err := readJSONConfig(configPath)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}

	// Check that other settings are preserved
	if config["theme"] != "dark" {
		t.Error("theme setting was not preserved")
	}
	if config["language"] != "en" {
		t.Error("language setting was not preserved")
	}

	// Check that both servers exist
	mcpServers := config["mcpServers"].(map[string]any)
	if _, ok := mcpServers["other-server"]; !ok {
		t.Error("other-server was removed")
	}
	if _, ok := mcpServers[mcpServerKey]; !ok {
		t.Error("what-the-mcp was not added")
	}
}

func TestAgentEnable_OverwriteExisting(t *testing.T) {
	t.Skip("test requires wtmcp binary in PATH")
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".mcp.json")

	// Write existing config with what-the-mcp
	existingConfig := map[string]any{
		"mcpServers": map[string]any{
			mcpServerKey: map[string]any{
				"command": "/old/path/to/wtmcp",
			},
		},
	}
	if err := writeJSONConfig(configPath, existingConfig); err != nil {
		t.Fatalf("failed to write existing config: %v", err)
	}

	// Re-enable (should update the path)
	if err := agentEnable("claude-code", tmpDir); err != nil {
		t.Fatalf("agentEnable() failed: %v", err)
	}

	// Read and check that path was updated
	config, err := readJSONConfig(configPath)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}

	mcpServers := config["mcpServers"].(map[string]any)
	wtmcp := mcpServers[mcpServerKey].(map[string]any)

	// Should have new command (not /old/path/to/wtmcp)
	command := wtmcp["command"].(string)
	if command == "/old/path/to/wtmcp" {
		t.Error("command was not updated")
	}
}

func TestAgentDisable_RemovesEntry(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".mcp.json")

	// Write config with multiple servers
	config := map[string]any{
		"mcpServers": map[string]any{
			mcpServerKey: map[string]any{
				"command": "/path/to/wtmcp",
			},
			"other-server": map[string]any{
				"command": "/path/to/other",
			},
		},
	}
	if err := writeJSONConfig(configPath, config); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Disable wtmcp
	if err := agentDisable("claude-code", tmpDir); err != nil {
		t.Fatalf("agentDisable() failed: %v", err)
	}

	// Read and check
	config, err := readJSONConfig(configPath)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}

	mcpServers := config["mcpServers"].(map[string]any)

	// what-the-mcp should be removed
	if _, ok := mcpServers[mcpServerKey]; ok {
		t.Error("what-the-mcp was not removed")
	}

	// other-server should still exist
	if _, ok := mcpServers["other-server"]; !ok {
		t.Error("other-server was removed")
	}
}

func TestAgentDisable_NoConfigFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Should not error when config doesn't exist
	if err := agentDisable("claude-code", tmpDir); err != nil {
		t.Errorf("agentDisable() should not error when config doesn't exist: %v", err)
	}
}

func TestAgentDisable_NoEntry(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".mcp.json")

	// Write config without what-the-mcp
	config := map[string]any{
		"mcpServers": map[string]any{
			"other-server": map[string]any{
				"command": "/path/to/other",
			},
		},
	}
	if err := writeJSONConfig(configPath, config); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Should not error when entry doesn't exist
	if err := agentDisable("claude-code", tmpDir); err != nil {
		t.Errorf("agentDisable() should not error when entry doesn't exist: %v", err)
	}

	// Config should be unchanged
	newConfig, _ := readJSONConfig(configPath)
	mcpServers := newConfig["mcpServers"].(map[string]any)
	if _, ok := mcpServers["other-server"]; !ok {
		t.Error("other-server was removed")
	}
}

func TestAgentDisable_ClaudeCode_RemovesFileWhenEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".mcp.json")

	// Write config with only what-the-mcp
	config := map[string]any{
		"mcpServers": map[string]any{
			mcpServerKey: map[string]any{
				"command": "/path/to/wtmcp",
			},
		},
	}
	if err := writeJSONConfig(configPath, config); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Disable wtmcp
	if err := agentDisable("claude-code", tmpDir); err != nil {
		t.Fatalf("agentDisable() failed: %v", err)
	}

	// File should be removed
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Error("config file should be removed when empty")
	}
}

func TestAgentDisable_Cursor_RemovesFileWhenEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	cursorDir := filepath.Join(tmpDir, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o750); err != nil {
		t.Fatalf("failed to create .cursor dir: %v", err)
	}

	configPath := filepath.Join(cursorDir, "mcp.json")

	// Write config with only what-the-mcp
	config := map[string]any{
		"mcpServers": map[string]any{
			mcpServerKey: map[string]any{
				"command": "/path/to/wtmcp",
			},
		},
	}
	if err := writeJSONConfig(configPath, config); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Disable wtmcp
	if err := agentDisable("cursor", tmpDir); err != nil {
		t.Fatalf("agentDisable() failed: %v", err)
	}

	// File should be removed
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Error("config file should be removed when empty")
	}
}

func TestAgentDisable_Gemini_KeepsFileWhenEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	geminiDir := filepath.Join(tmpDir, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o750); err != nil {
		t.Fatalf("failed to create .gemini dir: %v", err)
	}

	configPath := filepath.Join(geminiDir, "settings.json")

	// Write config with only what-the-mcp
	config := map[string]any{
		"mcpServers": map[string]any{
			mcpServerKey: map[string]any{
				"command": "/path/to/wtmcp",
			},
		},
	}
	if err := writeJSONConfig(configPath, config); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Disable wtmcp
	if err := agentDisable("gemini", tmpDir); err != nil {
		t.Fatalf("agentDisable() failed: %v", err)
	}

	// File should still exist (Gemini may have other settings)
	if _, err := os.Stat(configPath); err != nil {
		t.Error("config file should not be removed for Gemini")
	}

	// Config should be empty object
	newConfig, _ := readJSONConfig(configPath)
	if len(newConfig) != 0 {
		t.Errorf("config should be empty, got: %v", newConfig)
	}
}

func TestAgentDisable_Gemini_PreservesOtherSettings(t *testing.T) {
	tmpDir := t.TempDir()
	geminiDir := filepath.Join(tmpDir, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o750); err != nil {
		t.Fatalf("failed to create .gemini dir: %v", err)
	}

	configPath := filepath.Join(geminiDir, "settings.json")

	// Write config with other settings
	config := map[string]any{
		"theme":    "dark",
		"language": "en",
		"mcpServers": map[string]any{
			mcpServerKey: map[string]any{
				"command": "/path/to/wtmcp",
			},
		},
	}
	if err := writeJSONConfig(configPath, config); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Disable wtmcp
	if err := agentDisable("gemini", tmpDir); err != nil {
		t.Fatalf("agentDisable() failed: %v", err)
	}

	// Read and check that other settings are preserved
	newConfig, err := readJSONConfig(configPath)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}

	if newConfig["theme"] != "dark" {
		t.Error("theme setting was not preserved")
	}
	if newConfig["language"] != "en" {
		t.Error("language setting was not preserved")
	}

	// mcpServers should be removed since it's now empty
	if _, ok := newConfig["mcpServers"]; ok {
		t.Error("empty mcpServers should be removed")
	}
}
