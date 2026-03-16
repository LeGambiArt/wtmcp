package plugin

import (
	"fmt"

	"github.com/LeGambiArt/wtmcp/internal/config"
)

// DiscoveryOptions configures plugin discovery behavior.
type DiscoveryOptions struct {
	ConfigPath      string // Optional config file path
	WorkdirOverride string // Optional workdir override
}

// DiscoveryResult contains the results of plugin discovery.
type DiscoveryResult struct {
	Workdir   string
	Config    *config.Config
	EnvGroups map[string]map[string]string
	EnvErrors map[string]string
	Manager   *Manager
}

// Discover performs plugin discovery without loading plugins.
// This is the common discovery logic used by CLI tools and the check command.
// For the main server runtime, use the full initialization in run().
func Discover(opts DiscoveryOptions) (*DiscoveryResult, error) {
	// Resolve workdir
	workdir := config.WorkDir()
	if opts.WorkdirOverride != "" {
		workdir = opts.WorkdirOverride
	}

	// Load scoped env.d groups (not into process env)
	envResult, err := config.LoadEnvGroups(workdir)
	if err != nil {
		return nil, fmt.Errorf("load env: %w", err)
	}

	// Load config (uses workdir for defaults)
	cfg, err := config.Load(opts.ConfigPath, workdir)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	// Create manager with nil dependencies (discovery only)
	mgr := NewManager(nil, nil, nil, cfg, envResult.Groups, envResult.Errors, workdir)

	// Discover plugins (without loading/starting them)
	if err := mgr.Discover(cfg.PluginDirs, cfg.UserPluginDir); err != nil {
		return nil, fmt.Errorf("plugin discovery: %w", err)
	}

	return &DiscoveryResult{
		Workdir:   workdir,
		Config:    cfg,
		EnvGroups: envResult.Groups,
		EnvErrors: envResult.Errors,
		Manager:   mgr,
	}, nil
}
