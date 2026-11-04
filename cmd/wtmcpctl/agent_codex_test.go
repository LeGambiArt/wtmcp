package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

var codexTestFormat = supportedAgents["codex"].format.(tomlAgentFormat)

func codexAgentEnable(path, command string) error {
	return codexTestFormat.update(path, "codex", true, command, false)
}

func codexAgentDisable(path, _ string) error {
	return codexTestFormat.update(path, "codex", false, "", false)
}

func decodeCodexConfig(data []byte) (map[string]any, error) {
	return codexTestFormat.decodeConfig(data)
}

func editCodexConfig(data []byte, enable bool, command string, readOnly bool) ([]byte, error) {
	return codexTestFormat.edit(data, enable, command, readOnly)
}

func TestCodexAgentEnableAndDisable(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	dir := t.TempDir()
	binDir := t.TempDir()
	wtmcpPath := filepath.Join(binDir, "wtmcp")
	if err := os.WriteFile(wtmcpPath, []byte("#!/bin/sh\n"), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	path := supportedAgents["codex"].configPath(dir)
	if err := agentEnable("codex", dir, true); err != nil {
		t.Fatalf("enable codex: %v", err)
	}

	config, err := readTOMLConfig(path)
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	servers := config["mcp_servers"].(map[string]any)
	wtmcp := servers[mcpServerKey].(map[string]any)
	if wtmcp["command"] != wtmcpPath {
		t.Errorf("command = %v", wtmcp["command"])
	}
	args := wtmcp["args"].([]any)
	if len(args) != 1 || args[0] != "--read-only" {
		t.Errorf("args = %v, want [--read-only]", args)
	}
	if _, ok := wtmcp["type"]; ok {
		t.Error("Codex config should not contain a type field")
	}

	if err := agentDisable("codex", dir); err != nil {
		t.Fatalf("disable codex: %v", err)
	}
	config, err = readTOMLConfig(path)
	if err != nil {
		t.Fatalf("read disabled codex config: %v", err)
	}
	wtmcp = config["mcp_servers"].(map[string]any)[mcpServerKey].(map[string]any)
	if wtmcp["enabled"] != false {
		t.Errorf("enabled = %v, want false", wtmcp["enabled"])
	}
}

func TestCodexAgentPreservesOtherSettings(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	dir := t.TempDir()
	path := supportedAgents["codex"].configPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	initial := []byte("model = \"test-model\"\n\n[mcp_servers.other]\ncommand = \"other-server\"\n\n[mcp_servers.what-the-mcp]\ncommand = \"old-server\"\n")
	if err := os.WriteFile(path, initial, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := codexAgentEnable(path, "/path/to/wtmcp"); err != nil {
		t.Fatalf("enable codex: %v", err)
	}
	config, err := readTOMLConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config["model"] != "test-model" {
		t.Errorf("model setting changed: %v", config["model"])
	}
	servers := config["mcp_servers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Error("other MCP server was removed")
	}
	if servers[legacyServerKey].(map[string]any)["enabled"] != false {
		t.Error("legacy server was not disabled")
	}
	wtmcp := servers[mcpServerKey].(map[string]any)
	if args := wtmcp["args"].([]any); len(args) != 0 {
		t.Errorf("args = %v, want empty", args)
	}

	if err := codexAgentDisable(path, dir); err != nil {
		t.Fatalf("disable codex: %v", err)
	}
	config, err = readTOMLConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config["model"] != "test-model" {
		t.Errorf("model setting changed after disable: %v", config["model"])
	}
	servers = config["mcp_servers"].(map[string]any)
	if servers["other"] == nil || servers[mcpServerKey].(map[string]any)["enabled"] != false {
		t.Errorf("other MCP server changed after disable: %v", servers)
	}
}

func TestCodexAgentWritesExplicitOverrides(t *testing.T) {
	project, err := editCodexConfig(nil, true, "/path/to/wtmcp", false)
	if err != nil {
		t.Fatal(err)
	}
	var enabled map[string]any
	if _, err := toml.Decode(string(project), &enabled); err != nil {
		t.Fatal(err)
	}
	entry := enabled["mcp_servers"].(map[string]any)[mcpServerKey].(map[string]any)
	if entry["enabled"] != true {
		t.Errorf("enable did not set enabled=true: %v", entry)
	}
	project, err = editCodexConfig(project, false, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var disabled map[string]any
	if _, err := toml.Decode(string(project), &disabled); err != nil {
		t.Fatal(err)
	}
	entry = disabled["mcp_servers"].(map[string]any)[mcpServerKey].(map[string]any)
	if entry["enabled"] != false {
		t.Errorf("disable did not set enabled=false: %v", entry)
	}
	project, err = editCodexConfig([]byte("# project note\n"), false, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(project), "# project note") {
		t.Error("comment-only config was discarded")
	}
	disabled = nil
	if _, err := toml.Decode(string(project), &disabled); err != nil {
		t.Fatal(err)
	}
	entry = disabled["mcp_servers"].(map[string]any)[mcpServerKey].(map[string]any)
	if entry["enabled"] != false {
		t.Errorf("missing project entry did not create a disable override: %v", entry)
	}
}

func TestCodexAgentPreservesCustomizedServerAndComments(t *testing.T) {
	initial := "# keep this explanation\nmodel = \"test-model\"\n# model = \"old-model\"\n\n" +
		"[mcp_servers.wtmcp]\ncommand = \"old-wtmcp\"\nargs = [\"--old\"]\nenabled = false\n" +
		"disabled_tools = [\"dangerous\"]\nstartup_timeout_sec = 25\n" +
		"[mcp_servers.wtmcp.env]\nTOKEN = \"from-env\"\n\n" +
		"[mcp_servers.other]\ncommand = \"other\"\n# keep this too\n"
	updated, err := editCodexConfig([]byte(initial), true, "/new/wtmcp", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, comment := range []string{"# keep this explanation", "# model = \"old-model\"", "# keep this too"} {
		if !strings.Contains(string(updated), comment) {
			t.Errorf("comment lost: %s", comment)
		}
	}
	var config map[string]any
	if _, err := toml.Decode(string(updated), &config); err != nil {
		t.Fatal(err)
	}
	servers := config["mcp_servers"].(map[string]any)
	entry := servers[mcpServerKey].(map[string]any)
	if entry["disabled_tools"].([]any)[0] != "dangerous" || entry["startup_timeout_sec"] != int64(25) {
		t.Errorf("customized fields lost: %v", entry)
	}
	if entry["env"].(map[string]any)["TOKEN"] != "from-env" {
		t.Errorf("environment lost: %v", entry)
	}
	if entry["enabled"] != true || entry["command"] != "/new/wtmcp" {
		t.Errorf("managed fields not updated: %v", entry)
	}
	if _, ok := servers["other"]; !ok {
		t.Error("unrelated server lost")
	}

	disabled, err := editCodexConfig(updated, false, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(disabled), "# model = \"old-model\"") {
		t.Error("comment lost on disable")
	}
}

func TestCodexAgentMigratesCustomizedLegacyServer(t *testing.T) {
	initial := "[mcp_servers.what-the-mcp]\ncommand = \"old\"\n" +
		"disabled_tools = [\"dangerous\"]\n\n[mcp_servers.what-the-mcp.env]\nTOKEN = \"from-env\"\n"
	updated, err := editCodexConfig([]byte(initial), true, "/new/wtmcp", false)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if _, err := toml.Decode(string(updated), &config); err != nil {
		t.Fatal(err)
	}
	servers := config["mcp_servers"].(map[string]any)
	if servers[legacyServerKey].(map[string]any)["enabled"] != false {
		t.Error("legacy server was not disabled")
	}
	entry := servers[mcpServerKey].(map[string]any)
	if entry["disabled_tools"].([]any)[0] != "dangerous" || entry["env"].(map[string]any)["TOKEN"] != "from-env" {
		t.Errorf("legacy settings lost: %v", entry)
	}
}

func TestCodexAgentRejectsURLTransport(t *testing.T) {
	initial := []byte("[mcp_servers.wtmcp]\nurl = \"https://example.com/mcp\"\n")
	if _, err := editCodexConfig(initial, true, "/path/to/wtmcp", false); err == nil {
		t.Fatal("expected URL transport error")
	}
	if _, err := editCodexConfig(initial, false, "", false); err != nil {
		t.Fatalf("disabling URL transport should be allowed: %v", err)
	}
}

func TestCodexAgentRejectsInvalidConfig(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	dir := t.TempDir()
	path := supportedAgents["codex"].configPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"invalid syntax":         "invalid = [",
		"server group not table": "mcp_servers = 'invalid'\n",
		"server not table":       "[mcp_servers]\nwtmcp = 'invalid'\n",
		"legacy not table":       "[mcp_servers]\nwhat-the-mcp = 'invalid'\n",
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			for _, action := range []struct {
				name string
				run  func() error
			}{
				{name: "enable", run: func() error { return codexAgentEnable(path, "/path/to/wtmcp") }},
				{name: "disable", run: func() error { return codexAgentDisable(path, dir) }},
			} {
				t.Run(action.name, func(t *testing.T) {
					if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := action.run(); err == nil {
						t.Fatal("expected invalid TOML error")
					}
					data, err := os.ReadFile(path) // #nosec G304 -- path is in the test temp directory
					if err != nil {
						t.Fatal(err)
					}
					if string(data) != source {
						t.Errorf("invalid config was changed: %q", data)
					}
				})
			}
		})
	}
}

func TestCodexStatementEditing(t *testing.T) {
	cases := map[string]string{
		"multiline arguments":              "[mcp_servers.wtmcp]\ncommand = \"old\"\nargs = [\n \"--old\",\n]\n",
		"quoted keys":                      "[\"mcp_servers\" . 'wtmcp']\n\"command\" = 'old' # command note\n'args' = []\n\"enabled\" = false\n",
		"multiline command":                "[mcp_servers.wtmcp]\ncommand = '''old\ncommand'''\n",
		"string containing table":          "developer_instructions = \"\"\"\n[mcp_servers.wtmcp]\ncommand = \"example\"\n\"\"\"\n",
		"literal string containing legacy": "developer_instructions = '''\n[mcp_servers.what-the-mcp]\ncommand = \"example\"\n'''\n",
		"nested table first":               "[mcp_servers.wtmcp.env]\nTOKEN = \"keep\"\n",
		"dotted root":                      "mcp_servers.wtmcp.command = \"old\"\n",
		"dotted parent":                    "[mcp_servers]\nwtmcp.command = \"old\"\nother.command = \"keep\"\n",
		"CRLF":                             "# note\r\n[mcp_servers.wtmcp]\r\ncommand = 'old' # keep\r\nargs = [\r\n 'old',\r\n]\r\n",
		"no final newline":                 "[mcp_servers.wtmcp]\ncommand = 'old'",
		"quoted equals":                    "[mcp_servers.wtmcp.env]\n'TOKEN=NAME' = 'value'\n",
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			before, err := decodeCodexConfig([]byte(source))
			if err != nil {
				t.Fatal(err)
			}
			updated, err := editCodexConfig([]byte(source), true, "/new/wtmcp", true)
			if err != nil {
				t.Fatal(err)
			}
			config, err := decodeCodexConfig(updated)
			if err != nil {
				t.Fatal(err)
			}
			entry := tomlTable(config, toml.Key{"mcp_servers", mcpServerKey})
			if entry["command"] != "/new/wtmcp" || entry["enabled"] != true || !reflect.DeepEqual(entry["args"], []any{"--read-only"}) {
				t.Fatalf("wrong server: %v", entry)
			}
			if !reflect.DeepEqual(config["developer_instructions"], before["developer_instructions"]) {
				t.Fatal("instructions changed")
			}
			if !reflect.DeepEqual(entry["env"], tomlTable(before, toml.Key{"mcp_servers", mcpServerKey})["env"]) {
				t.Fatal("environment changed")
			}
			if name == "quoted keys" && !strings.Contains(string(updated), "# command note") {
				t.Fatal("trailing comment lost")
			}
			again, err := editCodexConfig(updated, true, "/new/wtmcp", true)
			if err != nil {
				t.Fatal(err)
			}
			if string(again) != string(updated) {
				t.Fatalf("enable is not idempotent:\n%s\n%s", updated, again)
			}
			disabled, err := editCodexConfig(updated, false, "", false)
			if err != nil {
				t.Fatal(err)
			}
			config, err = decodeCodexConfig(disabled)
			if err != nil {
				t.Fatal(err)
			}
			for _, server := range []string{mcpServerKey, legacyServerKey} {
				if tomlTable(config, toml.Key{"mcp_servers", server})["enabled"] != false {
					t.Fatalf("%s still enabled", server)
				}
			}
		})
	}
}

func TestCodexDisabledTransport(t *testing.T) {
	for _, source := range []string{"", "# preserve this\n", "[mcp_servers.other]\ncommand='other'\n"} {
		updated, err := editCodexConfig([]byte(source), false, "", false)
		if err != nil {
			t.Fatal(err)
		}
		config, err := decodeCodexConfig(updated)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{mcpServerKey, legacyServerKey} {
			entry := tomlTable(config, toml.Key{"mcp_servers", name})
			if entry["enabled"] != false || entry["command"] == nil && entry["url"] == nil {
				t.Fatalf("invalid disabled server %s: %v", name, entry)
			}
		}
	}
}

func TestCodexRejectsInlineTablesWithoutWriting(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"mcp_servers = { wtmcp = { command = 'old' } }\n", "[mcp_servers]\nwtmcp = { command = 'old' }\n"} {
		for _, action := range []struct {
			name string
			run  func() error
		}{
			{name: "enable", run: func() error { return codexAgentEnable(path, "/new/wtmcp") }},
			{name: "disable", run: func() error { return codexAgentDisable(path, dir) }},
		} {
			t.Run(action.name, func(t *testing.T) {
				if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := action.run(); err == nil {
					t.Fatal("expected unsupported inline table error")
				}
				data, err := os.ReadFile(path) // #nosec G304 -- path is in the test temp directory
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != source {
					t.Fatal("rejected config was changed")
				}
			})
		}
	}
}

// Exercise the real client's schema and layer merging when Codex is installed.
// These commands only list configuration; no MCP server is launched.
func TestCodexAgentEffectiveConfiguration(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("Codex CLI not installed")
	}
	cases := []struct {
		name, global, parent, local, wantURL string
		enable                               bool
	}{
		{name: "disable absent"},
		{name: "disable global", global: "[mcp_servers.wtmcp]\ncommand='global'\nenabled=true\n"},
		{name: "disable legacy", global: "[mcp_servers.what-the-mcp]\ncommand='legacy'\n", local: "[mcp_servers.what-the-mcp]\ncommand='project-legacy'\n"},
		{name: "disable HTTP", global: "[mcp_servers.wtmcp]\nurl='https://example.invalid/mcp'\n"},
		{name: "disable nearest parent HTTP", global: "[mcp_servers.wtmcp]\nurl='https://global.invalid/mcp'\n", parent: "[mcp_servers.wtmcp]\nurl='https://parent.invalid/mcp'\n", wantURL: "https://parent.invalid/mcp"},
		{name: "enable disabled", global: "[mcp_servers.wtmcp]\ncommand='global'\nenabled=false\n", enable: true},
		{name: "enable legacy", global: "[mcp_servers.what-the-mcp]\ncommand='legacy'\n", local: "[mcp_servers.what-the-mcp]\ncommand='old'\ndisabled_tools=['dangerous']\n", enable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			parent, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			project := filepath.Join(parent, "child")
			if err := os.MkdirAll(project, 0o750); err != nil {
				t.Fatal(err)
			}
			trust, err := toml.Marshal(map[string]any{"projects": map[string]any{project: map[string]any{"trust_level": "trusted"}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, "config.toml"), append(trust, []byte(tc.global)...), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.parent != "" {
				parentConfig := filepath.Join(parent, ".codex", "config.toml")
				if err := os.MkdirAll(filepath.Dir(parentConfig), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(parentConfig, []byte(tc.parent), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(project, ".codex", "config.toml")
			if tc.local != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.local), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.enable {
				err = codexAgentEnable(path, "/new/wtmcp")
			} else {
				err = codexAgentDisable(path, project)
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantURL != "" {
				config, err := readTOMLConfig(path)
				if err != nil {
					t.Fatal(err)
				}
				if got := tomlTable(config, toml.Key{"mcp_servers", mcpServerKey})["url"]; got != tc.wantURL {
					t.Fatalf("project URL = %v, want %s", got, tc.wantURL)
				}
			}
			cmd := exec.Command(codex, "mcp", "list", "--json")
			cmd.Dir = project
			out, err := cmd.Output()
			if err != nil {
				var exit *exec.ExitError
				if errors.As(err, &exit) {
					t.Fatalf("Codex rejected config: %s", exit.Stderr)
				}
				t.Fatal(err)
			}
			if tc.wantURL != "" && !strings.Contains(string(out), tc.wantURL) {
				t.Fatalf("Codex did not use the nearest parent URL: %s", out)
			}
			var servers []struct {
				Name    string `json:"name"`
				Enabled bool   `json:"enabled"`
			}
			if err := json.Unmarshal(out, &servers); err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, server := range servers {
				if server.Name == mcpServerKey || server.Name == legacyServerKey {
					seen[server.Name] = true
					want := tc.enable && server.Name == mcpServerKey
					if server.Enabled != want {
						t.Fatalf("%s enabled=%v, want %v", server.Name, server.Enabled, want)
					}
				}
			}
			if !seen[mcpServerKey] || !seen[legacyServerKey] {
				t.Fatalf("missing overrides: %s", out)
			}
		})
	}
}

func TestCodexLegacyArrayAndStringPreservation(t *testing.T) {
	source := "developer_instructions = '''\n[mcp_servers.what-the-mcp]\nexample only\n'''\n" +
		"[mcp_servers.what-the-mcp]\ncommand='old'\n" +
		"[[mcp_servers.what-the-mcp.extra]] # preserve array\nvalue='keep'\n"
	before, err := decodeCodexConfig([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := editCodexConfig([]byte(source), true, "/new/wtmcp", false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := decodeCodexConfig(updated)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before["developer_instructions"], after["developer_instructions"]) || !reflect.DeepEqual(tomlTable(before, toml.Key{"mcp_servers", legacyServerKey})["extra"], tomlTable(after, toml.Key{"mcp_servers", mcpServerKey})["extra"]) {
		t.Fatalf("migration changed unrelated values: %s", updated)
	}
}

func TestCodexInheritedParentTransportAndPermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[mcp_servers.wtmcp]\nurl='https://global.invalid/mcp'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	parentConfig := filepath.Join(parent, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(parentConfig), 0o750); err != nil {
		t.Fatal(err)
	}
	parentSource := []byte("[mcp_servers.wtmcp]\nurl='https://parent.invalid/mcp'\n")
	if err := os.WriteFile(parentConfig, parentSource, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "child", ".codex", "config.toml")
	if err := codexAgentDisable(path, filepath.Dir(filepath.Dir(path))); err != nil {
		t.Fatal(err)
	}
	config, err := readTOMLConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if tomlTable(config, toml.Key{"mcp_servers", mcpServerKey})["url"] != "https://parent.invalid/mcp" {
		t.Fatal("closest parent transport not retained")
	}
	data, err := os.ReadFile(parentConfig) // #nosec G304 -- parentConfig is in the test temp directory
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(parentSource) {
		t.Fatal("parent config changed")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("new config permissions: %o", info.Mode().Perm())
	}
	if err := os.Chmod(path, 0o640); err != nil { // #nosec G302 -- tests preservation of existing permissions
		t.Fatal(err)
	}
	if err := codexAgentDisable(path, parent); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatal("existing permissions changed")
	}
}
