package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// tomlAgentFormat manages MCP server entries in a TOML agent configuration.
type tomlAgentFormat struct {
	serverTable          string
	inheritedConfigPaths func(string) ([]string, error)
}

func (f tomlAgentFormat) readConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- trusted configuration path
	if err != nil {
		return nil, err
	}
	return f.decodeConfig(data)
}

func (f tomlAgentFormat) decodeConfig(data []byte) (map[string]any, error) {
	config, err := decodeTOMLConfig(data)
	if err != nil {
		return nil, err
	}
	if value, exists := config[f.serverTable]; exists {
		servers, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s is not a TOML table", f.serverTable)
		}
		for _, name := range []string{mcpServerKey, legacyServerKey} {
			if value, exists := servers[name]; exists {
				if _, ok := value.(map[string]any); !ok {
					return nil, fmt.Errorf("%s.%s is not a TOML table", f.serverTable, name)
				}
			}
		}
	}
	return config, nil
}

func (f tomlAgentFormat) enable(spec agentSpec, agentName, dir, wtmcpPath string, readOnly bool) error {
	return f.update(spec.configPath(dir), agentName, true, wtmcpPath, readOnly)
}

func (f tomlAgentFormat) disable(spec agentSpec, agentName, dir string) error {
	return f.update(spec.configPath(dir), agentName, false, "", false)
}

func (f tomlAgentFormat) update(configPath, agentName string, enable bool, command string, readOnly bool) error {
	data, err := os.ReadFile(configPath) // #nosec G304 -- trusted configuration path
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}
	inherited, err := f.inheritedServers(configPath)
	if err != nil {
		return err
	}
	updated, err := f.editWithInherited(data, enable, command, readOnly, inherited)
	if err != nil {
		return fmt.Errorf("failed to edit config file %s: %w", configPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
		return err
	}
	if err := writeTOMLConfig(configPath, updated); err != nil {
		return fmt.Errorf("failed to write config file %s: %w", configPath, err)
	}
	action := "Disabled"
	if enable {
		action = "Enabled"
	}
	fmt.Printf("✓ %s wtmcp for %s\nConfig file: %s\n", action, agentName, configPath)
	return nil
}

// Inspect inherited layers only to choose a compatible transport for disabled
// overrides. A disabled table needs a transport if no lower layer supplies one.
func (f tomlAgentFormat) inheritedServers(configPath string) (map[string]map[string]any, error) {
	if f.inheritedConfigPaths == nil {
		return nil, nil
	}
	paths, err := f.inheritedConfigPaths(configPath)
	if err != nil {
		return nil, err
	}
	result := make(map[string]map[string]any)
	for _, path := range paths {
		if filepath.Clean(path) == filepath.Clean(configPath) {
			continue
		}
		config, err := f.readConfig(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read inherited config %s: %w", path, err)
		}
		for _, name := range []string{mcpServerKey, legacyServerKey} {
			if result[name] == nil {
				result[name] = make(map[string]any)
			}
			for key, value := range tomlTable(config, toml.Key{f.serverTable, name}) {
				result[name][key] = value
			}
		}
	}
	return result, nil
}

func (f tomlAgentFormat) edit(data []byte, enable bool, command string, readOnly bool) ([]byte, error) {
	return f.editWithInherited(data, enable, command, readOnly, nil)
}

func (f tomlAgentFormat) editWithInherited(data []byte, enable bool, command string, readOnly bool, inherited map[string]map[string]any) ([]byte, error) {
	config, err := f.decodeConfig(data)
	if err != nil {
		return nil, err
	}
	current, legacy := tomlTable(config, toml.Key{f.serverTable, mcpServerKey}),
		tomlTable(config, toml.Key{f.serverTable, legacyServerKey})
	if enable {
		if current["url"] != nil || inherited[mcpServerKey]["url"] != nil || current == nil && legacy["url"] != nil {
			return nil, errors.New("wtmcp uses a URL transport; remove it before configuring a stdio server")
		}
		if current == nil && legacy != nil {
			data, err = renameTOMLTable(data,
				toml.Key{f.serverTable, legacyServerKey}, toml.Key{f.serverTable, mcpServerKey})
			if err != nil {
				return nil, err
			}
			current = legacy
		}
	}
	// Both names need overrides: removing a legacy table exposes any inherited
	// registration. Preserve local transport/options; supply a transport only
	// when absent so standalone disabled entries also have a valid transport.
	for _, name := range []string{mcpServerKey, legacyServerKey} {
		local := current
		if name == legacyServerKey {
			refreshed, err := f.decodeConfig(data)
			if err != nil {
				return nil, err
			}
			local = tomlTable(refreshed, toml.Key{f.serverTable, name})
		}
		fields := map[string]any{"enabled": false}
		if enable && name == mcpServerKey {
			args := []string{}
			if readOnly {
				args = append(args, "--read-only")
			}
			fields = map[string]any{"enabled": true, "command": command, "args": args}
		} else if local["command"] == nil && local["url"] == nil {
			if url, ok := inherited[name]["url"]; ok {
				fields["url"] = url
			} else {
				fields["command"] = "wtmcp" // Disabled; never executed.
			}
		}
		data, err = setTOMLFields(data, toml.Key{f.serverTable, name}, fields,
			[]string{"command", "url", "args", "enabled"})
		if err != nil {
			return nil, err
		}
		updated, err := f.decodeConfig(data)
		if err != nil {
			return nil, fmt.Errorf("edited TOML is invalid: %w", err)
		}
		if err := validateTOMLFields(updated, toml.Key{f.serverTable, name}, fields); err != nil {
			return nil, err
		}
	}
	return data, nil
}
