// Package config handles core configuration loading and environment
// variable resolution for wtmcp.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// AppName is used for FHS paths (/usr/share/<AppName>/plugins,
// /usr/libexec/<AppName>/plugins, etc.).
const AppName = "wtmcp"

// Config holds the core server configuration.
type Config struct {
	PluginDirs     []string      `yaml:"plugin_dirs"`
	CredentialsDir string        `yaml:"credentials_dir"`
	UserPluginDir  string        `yaml:"-"` // set internally, not from config file
	HTTP           HTTPConfig    `yaml:"http"`
	Cache          CacheConfig   `yaml:"cache"`
	Plugins        PluginsConfig `yaml:"plugins"`
	Output         OutputConfig  `yaml:"output"`
	Tools          ToolsConfig   `yaml:"tools"`
}

// HTTPConfig controls the HTTP proxy behavior.
type HTTPConfig struct {
	Timeout   time.Duration   `yaml:"timeout"`
	Retries   RetryConfig     `yaml:"retries"`
	RateLimit RateLimitConfig `yaml:"rate_limit"`
}

// RetryConfig controls retry behavior for HTTP requests.
type RetryConfig struct {
	Max     int    `yaml:"max"`
	Backoff string `yaml:"backoff"`
	RetryOn []int  `yaml:"retry_on"`
}

// RateLimitConfig controls request rate limiting.
type RateLimitConfig struct {
	Default   string            `yaml:"default"`
	PerPlugin map[string]string `yaml:"per_plugin"`
	PerDomain map[string]string `yaml:"per_domain"`
}

// CacheConfig controls the cache backend.
type CacheConfig struct {
	Backend             string        `yaml:"backend"`
	Dir                 string        `yaml:"dir"`
	MaxEntriesPerPlugin int           `yaml:"max_entries_per_plugin"`
	MaxEntrySize        int64         `yaml:"max_entry_size"`
	Eviction            string        `yaml:"eviction"`
	CleanupInterval     time.Duration `yaml:"cleanup_interval"`
}

// PluginsConfig controls plugin process management.
type PluginsConfig struct {
	MaxMessageSize    int64         `yaml:"max_message_size"`
	ToolCallTimeout   time.Duration `yaml:"tool_call_timeout"`
	InitTimeout       time.Duration `yaml:"init_timeout"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
	ShutdownKillAfter time.Duration `yaml:"shutdown_kill_after"`
	UserPlugins       bool          `yaml:"user_plugins"`
	Disabled          []string      `yaml:"disabled"`
}

// OutputConfig controls tool result encoding.
type OutputConfig struct {
	Format       string `yaml:"format"`
	ToonFallback bool   `yaml:"toon_fallback"`
}

// ToolsConfig controls progressive tool discovery.
type ToolsConfig struct {
	// Discovery mode: "full" registers all tools normally;
	// "progressive" marks non-primary tools with defer_loading.
	Discovery string `yaml:"discovery"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		PluginDirs: []string{},
		HTTP: HTTPConfig{
			Timeout: 30 * time.Second,
			Retries: RetryConfig{
				Max:     3,
				Backoff: "exponential",
				RetryOn: []int{500, 502, 503, 504},
			},
		},
		Cache: CacheConfig{
			Backend:             "memory",
			MaxEntriesPerPlugin: 10000,
			MaxEntrySize:        1024 * 1024, // 1MB
			Eviction:            "lru",
			CleanupInterval:     60 * time.Second,
		},
		Plugins: PluginsConfig{
			MaxMessageSize:    10 * 1024 * 1024, // 10MB
			ToolCallTimeout:   60 * time.Second,
			InitTimeout:       30 * time.Second,
			ShutdownTimeout:   10 * time.Second,
			ShutdownKillAfter: 5 * time.Second,
		},
		Output: OutputConfig{
			Format:       "toon",
			ToonFallback: true,
		},
		Tools: ToolsConfig{
			Discovery: "full",
		},
	}
}

// Load reads a config file and merges with defaults. If configPath is empty,
// uses workdir/config.yaml. After loading, applies workdir-based defaults
// for any paths not explicitly set in the config file.
func Load(configPath, workdir string) (*Config, error) {
	cfg := DefaultConfig()

	if configPath == "" {
		configPath = filepath.Join(workdir, "config.yaml")
	}

	data, err := os.ReadFile(configPath) //nolint:gosec // config file path from user
	if err != nil {
		if os.IsNotExist(err) {
			applyWorkdirDefaults(cfg, workdir)
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", configPath, err)
	}

	if cfg.Tools.Discovery != "full" && cfg.Tools.Discovery != "progressive" {
		return nil, fmt.Errorf("tools.discovery must be 'full' or 'progressive', got %q", cfg.Tools.Discovery)
	}

	applyWorkdirDefaults(cfg, workdir)
	return cfg, nil
}

// applyWorkdirDefaults fills in paths that weren't set in the config
// using the standard workdir layout.
func applyWorkdirDefaults(cfg *Config, workdir string) {
	paths := Paths(workdir)

	if cfg.CredentialsDir == "" {
		cfg.CredentialsDir = paths.CredentialsDir
	} else {
		cfg.CredentialsDir = ResolveEnvVars(cfg.CredentialsDir)
	}

	if cfg.Cache.Dir == "" {
		cfg.Cache.Dir = paths.CacheDir
	} else {
		cfg.Cache.Dir = ResolveEnvVars(cfg.Cache.Dir)
	}

	// Build plugin dirs: system dirs, then user dir (if enabled).
	if len(cfg.PluginDirs) == 0 {
		cfg.PluginDirs = defaultPluginDirs(paths.PluginsDir, cfg.Plugins.UserPlugins)
	}
	if cfg.Plugins.UserPlugins {
		cfg.UserPluginDir = paths.PluginsDir
	}
}

// defaultPluginDirs returns the plugin search path. System dirs are
// checked first; user dir is last and only included when
// enableUserPlugins is true. Non-existent directories are included
// but silently skipped by Manager.Discover().
//
// Search order:
//  1. {binary}/plugins (dev: plugins next to binary)
//  2. {binary}/../share/<AppName>/plugins (installed: share layout)
//  3. {binary}/../libexec/<AppName>/plugins (installed: libexec layout)
//  4. /usr/share/<AppName>/plugins (system share)
//  5. /usr/libexec/<AppName>/plugins (system libexec — RPM)
//  6. /usr/local/share/<AppName>/plugins (local installs, Homebrew)
//  7. {workdir}/plugins (user plugins, only if enabled)
func defaultPluginDirs(userDir string, enableUserPlugins bool) []string {
	var dirs []string

	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			binDir := filepath.Dir(resolved)

			// Dev: plugins/ next to the binary (build directory)
			devPlugins := filepath.Join(binDir, "plugins")
			dirs = append(dirs, filepath.Clean(devPlugins))

			// Installed: {prefix}/share/<AppName>/plugins
			installed := filepath.Join(binDir, "..", "share", AppName, "plugins")
			cleaned := filepath.Clean(installed)
			if !containsPath(dirs, cleaned) {
				dirs = append(dirs, cleaned)
			}

			// Installed: {prefix}/libexec/<AppName>/plugins
			libexec := filepath.Join(binDir, "..", "libexec", AppName, "plugins")
			cleaned = filepath.Clean(libexec)
			if !containsPath(dirs, cleaned) {
				dirs = append(dirs, cleaned)
			}
		}
	}

	// Standard system paths
	for _, sysDir := range []string{
		filepath.Join("/usr/share", AppName, "plugins"),
		filepath.Join("/usr/libexec", AppName, "plugins"),
		filepath.Join("/usr/local/share", AppName, "plugins"),
	} {
		if !containsPath(dirs, sysDir) {
			dirs = append(dirs, sysDir)
		}
	}

	// User plugins last (only if explicitly enabled)
	if enableUserPlugins {
		dirs = append(dirs, userDir)
	}
	return dirs
}

func containsPath(dirs []string, path string) bool {
	cleaned := filepath.Clean(path)
	for _, d := range dirs {
		if filepath.Clean(d) == cleaned {
			return true
		}
	}
	return false
}

// envVarPattern matches ${VAR} and ${VAR:-default} syntax.
var envVarPattern = regexp.MustCompile(`\$\$|\$\{([^}]+)\}`)

// ResolveEnvVars expands environment variable references in a string
// using the process environment. Use this only for server-level config
// (credentials_dir, cache.dir) — never for plugin-scoped values.
//
// Supported syntax:
//   - ${VAR}           — value of VAR, empty string if unset
//   - ${VAR:-default}  — value of VAR, or "default" if unset/empty
//   - $$               — literal dollar sign
func ResolveEnvVars(s string) string {
	return resolveVarsFunc(s, os.LookupEnv)
}

// ResolveVars expands ${VAR} references using only the provided vars
// map. Shell-exported environment variables are not consulted. This
// is the scoped resolver for plugin configuration — each plugin only
// sees variables from its own credential_group env.d file.
func ResolveVars(s string, vars map[string]string) string {
	return resolveVarsFunc(s, func(key string) (string, bool) {
		val, ok := vars[key]
		return val, ok
	})
}

// ResolveVarsMap resolves all ${VAR} references in a string map using
// only the provided vars map.
func ResolveVarsMap(m map[string]string, vars map[string]string) map[string]string {
	resolved := make(map[string]string, len(m))
	for k, v := range m {
		resolved[k] = ResolveVars(v, vars)
	}
	return resolved
}

func resolveVarsFunc(s string, lookup func(string) (string, bool)) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		if match == "$$" {
			return "$"
		}

		// Strip ${ and }
		inner := match[2 : len(match)-1]

		// Check for :-default syntax
		if idx := strings.Index(inner, ":-"); idx >= 0 {
			varName := inner[:idx]
			defaultVal := inner[idx+2:]
			if val, ok := lookup(varName); ok && val != "" {
				return val
			}
			return defaultVal
		}

		// Simple ${VAR}
		val, _ := lookup(inner)
		return val
	})
}
