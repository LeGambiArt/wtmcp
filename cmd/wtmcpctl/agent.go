package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"
)

const mcpServerKey = "what-the-mcp"

// agentSpec defines how a specific AI agent stores its MCP server configuration.
type agentSpec struct {
	// configPath returns the path to the agent's config file,
	// given the project directory.
	configPath func(dir string) string
	// needsType indicates whether "type": "stdio" must be included
	// in the server entry.
	needsType bool
	// dirPrefix is the subdirectory that may need to be created
	// (empty string if config is directly in project root).
	dirPrefix string
	// canRemoveFile indicates whether the config file can be removed
	// when it becomes empty (false for Gemini which may have other settings).
	canRemoveFile bool
}

var (
	claudeCodeSpec = agentSpec{
		configPath: func(dir string) string {
			return filepath.Join(dir, ".mcp.json")
		},
		needsType:     true,
		dirPrefix:     "",
		canRemoveFile: true,
	}

	supportedAgents = map[string]agentSpec{
		"claude":      claudeCodeSpec,
		"claude-code": claudeCodeSpec,
		"gemini": {
			configPath: func(dir string) string {
				return filepath.Join(dir, ".gemini", "settings.json")
			},
			needsType:     false,
			dirPrefix:     ".gemini",
			canRemoveFile: false,
		},
		"cursor": {
			configPath: func(dir string) string {
				return filepath.Join(dir, ".cursor", "mcp.json")
			},
			needsType:     false,
			dirPrefix:     ".cursor",
			canRemoveFile: true,
		},
	}
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Configure wtmcp for AI agents (Claude, Gemini, Cursor)",
	Long: `Configure wtmcp MCP server for AI agents.

Supported agents:
  - claude / claude-code
  - gemini
  - cursor`,
}

var agentEnableCmd = &cobra.Command{
	Use:               "enable <agent-name>",
	Short:             "Enable wtmcp for an AI agent",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeAgentNames,
	RunE:              runAgentEnable,
}

var agentDisableCmd = &cobra.Command{
	Use:               "disable <agent-name>",
	Short:             "Disable wtmcp for an AI agent",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeAgentNames,
	RunE:              runAgentDisable,
}

func init() {
	agentEnableCmd.Flags().StringP("dir", "d", "",
		"Project directory (default: current directory)")
	if err := agentEnableCmd.MarkFlagDirname("dir"); err != nil {
		panic(err)
	}

	agentDisableCmd.Flags().StringP("dir", "d", "",
		"Project directory (default: current directory)")
	if err := agentDisableCmd.MarkFlagDirname("dir"); err != nil {
		panic(err)
	}

	agentCmd.AddCommand(agentEnableCmd, agentDisableCmd)
}

// completeAgentNames returns supported agent names for completion.
func completeAgentNames(
	_ *cobra.Command, _ []string, _ string,
) ([]string, cobra.ShellCompDirective) {
	return validAgentNames(), cobra.ShellCompDirectiveNoFileComp
}

func runAgentEnable(cmd *cobra.Command, args []string) error {
	agentName := args[0]

	// Validate agent name
	if _, ok := supportedAgents[agentName]; !ok {
		return fmt.Errorf("unknown agent: %s\nSupported agents: %v", agentName, validAgentNames())
	}

	// Resolve directory
	dir, _ := cmd.Flags().GetString("dir")
	resolvedDir, err := resolveDir(dir)
	if err != nil {
		return err
	}

	// Execute enable
	return agentEnable(agentName, resolvedDir)
}

func runAgentDisable(cmd *cobra.Command, args []string) error {
	agentName := args[0]

	// Validate agent name
	if _, ok := supportedAgents[agentName]; !ok {
		return fmt.Errorf("unknown agent: %s\nSupported agents: %v", agentName, validAgentNames())
	}

	// Resolve directory
	dir, _ := cmd.Flags().GetString("dir")
	resolvedDir, err := resolveDir(dir)
	if err != nil {
		return err
	}

	// Execute disable
	return agentDisable(agentName, resolvedDir)
}

// agentEnable enables the wtmcp MCP server for the specified agent.
func agentEnable(agentName, dir string) error {
	// Resolve wtmcp binary path
	wtmcpPath, err := resolveWtmcpBinary()
	if err != nil {
		return err
	}

	// Get agent spec
	spec := supportedAgents[agentName]

	// Compute config file path
	configPath := spec.configPath(dir)

	// Ensure directory exists if needed
	if spec.dirPrefix != "" {
		dirPath := filepath.Join(dir, spec.dirPrefix)
		if err := os.MkdirAll(dirPath, 0o750); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dirPath, err)
		}
	}

	// Read existing config
	config, err := readJSONConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	// Get or create mcpServers map
	mcpServers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		mcpServers = make(map[string]any)
	}

	// Build server entry
	serverEntry := map[string]any{
		"command": wtmcpPath,
		"args":    []any{},
	}

	// Add type field if needed
	if spec.needsType {
		serverEntry["type"] = "stdio"
	}

	// Set the server entry
	mcpServers[mcpServerKey] = serverEntry
	config["mcpServers"] = mcpServers

	// Write config
	if err := writeJSONConfig(configPath, config); err != nil {
		return fmt.Errorf("failed to write config file %s: %w", configPath, err)
	}

	fmt.Printf("✓ Enabled wtmcp for %s\n", agentName)
	fmt.Printf("Config file: %s\n", configPath)
	return nil
}

// agentDisable disables the wtmcp MCP server for the specified agent.
func agentDisable(agentName, dir string) error {
	// Get agent spec
	spec := supportedAgents[agentName]

	// Compute config file path
	configPath := spec.configPath(dir)

	// Read existing config
	config, err := readJSONConfig(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Printf("%s is not configured for %s in %s\n", mcpServerKey, agentName, dir)
			return nil
		}
		return fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	// Get mcpServers map
	mcpServers, ok := config["mcpServers"].(map[string]any)
	if !ok || mcpServers == nil {
		fmt.Printf("%s is not configured for %s in %s\n", mcpServerKey, agentName, dir)
		return nil
	}

	// Check if entry exists
	if _, exists := mcpServers[mcpServerKey]; !exists {
		fmt.Printf("%s is not configured for %s in %s\n", mcpServerKey, agentName, dir)
		return nil
	}

	// Remove the entry
	delete(mcpServers, mcpServerKey)

	// If mcpServers is now empty, remove it from config
	if len(mcpServers) == 0 {
		delete(config, "mcpServers")
	} else {
		config["mcpServers"] = mcpServers
	}

	// Decide whether to remove file or write it back
	if len(config) == 0 && spec.canRemoveFile {
		// Remove empty config file
		if err := os.Remove(configPath); err != nil {
			return fmt.Errorf("failed to remove config file %s: %w", configPath, err)
		}
		fmt.Printf("✓ Disabled wtmcp for %s (removed config file)\n", agentName)
	} else {
		// Write config back
		if err := writeJSONConfig(configPath, config); err != nil {
			return fmt.Errorf("failed to write config file %s: %w", configPath, err)
		}
		fmt.Printf("✓ Disabled wtmcp for %s\n", agentName)
	}

	fmt.Printf("Config file: %s\n", configPath)
	return nil
}

// resolveWtmcpBinary attempts to locate the wtmcp binary.
// It first tries to find it as a sibling of wtmcpctl, then falls back to PATH.
func resolveWtmcpBinary() (string, error) {
	// Try to find wtmcp as a sibling of wtmcpctl
	exe, err := os.Executable()
	if err == nil {
		// Resolve symlinks
		resolved, err := filepath.EvalSymlinks(exe)
		if err == nil {
			// Look for wtmcp in the same directory
			candidate := filepath.Join(filepath.Dir(resolved), "wtmcp")
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				// Convert to absolute path
				absPath, err := filepath.Abs(candidate)
				if err == nil {
					return absPath, nil
				}
			}
		}
	}

	// Fallback: look in PATH
	path, err := exec.LookPath("wtmcp")
	if err != nil {
		return "", fmt.Errorf("wtmcp binary not found: not alongside wtmcpctl and not in PATH")
	}

	// Convert to absolute path
	return filepath.Abs(path)
}

// resolveDir resolves the directory flag to an absolute path.
// If dirFlag is empty, it uses the current working directory.
func resolveDir(dirFlag string) (string, error) {
	if dirFlag == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get current directory: %w", err)
		}
		return cwd, nil
	}

	// Convert to absolute path
	absPath, err := filepath.Abs(dirFlag)
	if err != nil {
		return "", fmt.Errorf("failed to resolve directory path: %w", err)
	}

	return absPath, nil
}

// readJSONConfig reads a JSON config file into a map.
// Returns an empty map if the file doesn't exist.
func readJSONConfig(path string) (map[string]any, error) {
	// Check if file exists
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[string]any), nil
		}
		return nil, err
	}

	// Read file
	data, err := os.ReadFile(path) // #nosec G304 -- path is from trusted command-line argument
	if err != nil {
		return nil, err
	}

	// Parse JSON
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	return config, nil
}

// writeJSONConfig writes a map to a JSON config file with formatting.
func writeJSONConfig(path string, data map[string]any) error {
	// Marshal with indentation
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	// Add trailing newline
	jsonData = append(jsonData, '\n')

	// Write to file
	if err := os.WriteFile(path, jsonData, 0o600); err != nil {
		return err
	}

	return nil
}

// validAgentNames returns a sorted list of supported agent names.
func validAgentNames() []string {
	names := make([]string, 0, len(supportedAgents))
	for name := range supportedAgents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
