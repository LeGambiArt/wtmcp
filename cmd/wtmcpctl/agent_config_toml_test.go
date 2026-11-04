package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestTOMLEditorWithUnrelatedTable(t *testing.T) {
	source := []byte("# keep this comment\n[services.alpha]\ncommand = 'old'\n\n[other]\nvalue = 7\n")
	updated, err := setTOMLFields(source, toml.Key{"services", "alpha"},
		map[string]any{"command": "new", "enabled": true}, []string{"command", "enabled"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "# keep this comment") {
		t.Fatal("comment was removed")
	}
	updated, err = renameTOMLTable(updated, toml.Key{"services", "alpha"}, toml.Key{"services", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	config, err := decodeTOMLConfig(updated)
	if err != nil {
		t.Fatal(err)
	}
	services := config["services"].(map[string]any)
	if _, exists := services["alpha"]; exists {
		t.Fatal("old table remains")
	}
	server := services["beta"].(map[string]any)
	if server["command"] != "new" || server["enabled"] != true {
		t.Errorf("table fields changed: %v", server)
	}
	if err := validateTOMLFields(config, toml.Key{"services", "beta"},
		map[string]any{"command": "new", "enabled": true}); err != nil {
		t.Fatalf("valid fields were rejected: %v", err)
	}
	if err := validateTOMLFields(config, toml.Key{"services", "beta"},
		map[string]any{"command": "wrong"}); err == nil {
		t.Fatal("mismatched field was accepted")
	}
	if tomlTable(config, toml.Key{"services", "missing"}) != nil {
		t.Fatal("missing table should return nil")
	}
	if config["other"].(map[string]any)["value"] != int64(7) {
		t.Error("unrelated table changed")
	}
}

func TestTOMLAgentFormatWithAnotherServerTable(t *testing.T) {
	format := tomlAgentFormat{serverTable: "services"}
	path := filepath.Join(t.TempDir(), "settings.toml")
	initial := []byte("# keep this comment\n[services.what-the-mcp]\ncommand = 'old'\ndisabled_tools = ['dangerous']\n")
	if err := os.WriteFile(path, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := format.update(path, "example", true, "/new/wtmcp", true); err != nil {
		t.Fatal(err)
	}
	config, err := format.readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	server := tomlTable(config, toml.Key{"services", mcpServerKey})
	if server["enabled"] != true || server["command"] != "/new/wtmcp" || server["disabled_tools"].([]any)[0] != "dangerous" {
		t.Fatalf("wrong server after enable: %v", server)
	}
	if err := format.update(path, "example", false, "", false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is in the test temp directory
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# keep this comment") {
		t.Fatal("comment was removed")
	}
	config, err = format.decodeConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{mcpServerKey, legacyServerKey} {
		if tomlTable(config, toml.Key{"services", name})["enabled"] != false {
			t.Fatalf("%s remains enabled", name)
		}
	}
}
