// Package config handles core configuration loading and environment
// variable resolution for wtmcp.
package config

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// AppName is used for FHS paths (/usr/share/<AppName>/plugins,
// /usr/libexec/<AppName>/plugins, etc.).
const AppName = "wtmcp"

// Config holds the core server configuration.
type Config struct {
	PluginDirs     []string        `yaml:"plugin_dirs"`
	CredentialsDir string          `yaml:"credentials_dir"`
	LogFile        string          `yaml:"log_file"`
	EnvDir         string          `yaml:"env_dir"`
	UserPluginDir  string          `yaml:"-"` // set internally, not from config file
	ReadOnly       bool            `yaml:"read_only"`
	HTTP           HTTPConfig      `yaml:"http"`
	Cache          CacheConfig     `yaml:"cache"`
	Plugins        PluginsConfig   `yaml:"plugins"`
	Output         OutputConfig    `yaml:"output"`
	Tools          ToolsConfig     `yaml:"tools"`
	Stats          StatsConfig     `yaml:"stats"`
	Security       SecurityConfig  `yaml:"security"`
	Audit          AuditConfig     `yaml:"audit"`
	Sandbox        SandboxConfig   `yaml:"sandbox"`
	Providers      ProvidersConfig `yaml:"providers"`
	Secrets        SecretsConfig   `yaml:"secrets"`
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
// Rates use the format "<number>/<unit>" where unit is s, m, or h.
// Example: "60/m" = 60 requests per minute.
type RateLimitConfig struct {
	Global    string            `yaml:"global"`
	Default   string            `yaml:"default"`
	PerPlugin map[string]string `yaml:"per_plugin"`
	PerDomain map[string]string `yaml:"per_domain"`
}

const (
	defaultRateLimit = "120/m"
	globalRateLimit  = "600/m"

	// Google Workspace APIs support higher per-user rate limits than
	// our conservative default.  Docs allows 300 reads/min and
	// 60 writes/min; Drive and Gmail use quota-unit accounting that
	// maps to well over 300 raw requests/min.  Setting 300/m keeps
	// us within the lowest Google per-user limit (Docs reads) while
	// avoiding unnecessary self-throttling.
	googleAPIRateLimit = "300/m"
)

// googleAPIDomains lists the API domains served by Google Workspace
// plugins.  Each gets googleAPIRateLimit as its default per-domain
// rate limit.
var googleAPIDomains = []string{
	"docs.googleapis.com",
	"www.googleapis.com",
	"gmail.googleapis.com",
}

func defaultPerDomainRateLimits() map[string]string {
	m := make(map[string]string, len(googleAPIDomains))
	for _, d := range googleAPIDomains {
		m[d] = googleAPIRateLimit
	}
	return m
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
	Enabled           []string      `yaml:"enabled"`
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

// StatsConfig controls tool usage stats collection.
type StatsConfig struct {
	Enabled       bool   `yaml:"enabled"`
	Tokenizer     string `yaml:"tokenizer"`
	LogCalls      bool   `yaml:"log_calls"`
	Persist       bool   `yaml:"persist"`
	RetentionDays int    `yaml:"retention_days"`
}

// SecurityConfig controls prompt injection defense features.
type SecurityConfig struct {
	TagToolOutput     *bool `yaml:"tag_tool_output"`
	Elicitation       *bool `yaml:"elicitation"`
	ElicitationStrict *bool `yaml:"elicitation_strict"`
}

// TagToolOutputEnabled returns whether tool output tagging is enabled
// (default: true when not explicitly set).
func (s SecurityConfig) TagToolOutputEnabled() bool {
	if s.TagToolOutput == nil {
		return true
	}
	return *s.TagToolOutput
}

// ElicitationEnabled returns whether write tool confirmation prompts
// are enabled (default: true when not explicitly set).
func (s SecurityConfig) ElicitationEnabled() bool {
	if s.Elicitation == nil {
		return true
	}
	return *s.Elicitation
}

// ElicitationStrictEnabled returns whether write tools should be
// blocked when the MCP client does not support elicitation
// (default: true -- block execution).
func (s SecurityConfig) ElicitationStrictEnabled() bool {
	if s.ElicitationStrict == nil {
		return true
	}
	return *s.ElicitationStrict
}

// AuditConfig controls security audit logging.
type AuditConfig struct {
	LogFile     string   `yaml:"log_file"`
	Stdout      bool     `yaml:"stdout"`
	ScrubFields []string `yaml:"scrub_fields"`
}

// SandboxConfig controls plugin process sandboxing via arapuca.
// Sandbox is always active when the binary is built with libarapuca
// (the default). Resource limits can be tuned globally or per-plugin.
type SandboxConfig struct {
	Defaults SandboxResourceLimits            `yaml:"defaults"`
	Plugins  map[string]SandboxResourceLimits `yaml:"plugins"`
}

// SandboxResourceLimits defines resource caps for sandboxed plugins.
type SandboxResourceLimits struct {
	MaxMemoryMB   uint64 `yaml:"max_memory_mb"`
	MaxCPUPct     uint32 `yaml:"max_cpu_pct"`
	MaxPIDs       uint32 `yaml:"max_pids"`
	MaxFileSizeMB uint64 `yaml:"max_file_size_mb"`
}

// ProvidersConfig controls which auth providers are active.
type ProvidersConfig struct {
	Disabled []string `yaml:"disabled"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		PluginDirs: []string{},
		HTTP: HTTPConfig{
			Timeout: 45 * time.Second,
			Retries: RetryConfig{
				Max:     3,
				Backoff: "exponential",
				RetryOn: []int{500, 502, 503, 504},
			},
			RateLimit: RateLimitConfig{
				Default:   defaultRateLimit,
				Global:    globalRateLimit,
				PerDomain: defaultPerDomainRateLimits(),
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
			Discovery: "progressive",
		},
		Stats: StatsConfig{
			Enabled:       true,
			Tokenizer:     "chars",
			Persist:       true,
			RetentionDays: 90,
		},
		Sandbox: SandboxConfig{
			Defaults: SandboxResourceLimits{
				MaxMemoryMB:   512,
				MaxCPUPct:     100,
				MaxPIDs:       64,
				MaxFileSizeMB: 100,
			},
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

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err == nil {
		if sb, ok := raw["sandbox"].(map[string]any); ok {
			if _, hasEnabled := sb["enabled"]; hasEnabled {
				log.Printf("WARNING: sandbox.enabled was removed — sandbox is now always active "+
					"when built with libarapuca; to build without sandbox use -tags nosandbox. "+
					"Remove the field from %s to silence this warning.", configPath)
			}
		}
	}

	// Backfill Google API rate-limit defaults for domains the user
	// did not explicitly override.  yaml.Unmarshal replaces maps
	// wholesale, so user-supplied per_domain entries wipe the defaults.
	// Users can override individual domains to lower values but cannot
	// disable the backfill entirely via per_domain: {}.
	if cfg.HTTP.RateLimit.PerDomain == nil {
		cfg.HTTP.RateLimit.PerDomain = make(map[string]string)
	}
	for _, d := range googleAPIDomains {
		if _, ok := cfg.HTTP.RateLimit.PerDomain[d]; !ok {
			cfg.HTTP.RateLimit.PerDomain[d] = googleAPIRateLimit
		}
	}

	if cfg.Tools.Discovery != "full" && cfg.Tools.Discovery != "progressive" {
		return nil, fmt.Errorf("tools.discovery must be 'full' or 'progressive', got %q", cfg.Tools.Discovery)
	}

	if cfg.HTTP.Retries.Max < 0 || cfg.HTTP.Retries.Max > 10 {
		return nil, fmt.Errorf("http.retries.max must be 0-10, got %d", cfg.HTTP.Retries.Max)
	}

	if cfg.Stats.Tokenizer != "chars" {
		return nil, fmt.Errorf("stats.tokenizer must be 'chars', got %q", cfg.Stats.Tokenizer)
	}
	if cfg.Stats.RetentionDays < 0 {
		return nil, fmt.Errorf("stats.retention_days must be >= 0, got %d", cfg.Stats.RetentionDays)
	}

	if err := ValidateVaultIDConfigs(cfg.Secrets.VaultIDs); err != nil {
		return nil, err
	}

	if cfg.HTTP.Timeout > 0 && cfg.Plugins.ToolCallTimeout > 0 &&
		cfg.HTTP.Timeout >= cfg.Plugins.ToolCallTimeout {
		log.Printf("WARNING: http.timeout (%s) >= plugins.tool_call_timeout (%s); "+
			"HTTP requests may outlive tool calls, reducing cancellation effectiveness",
			cfg.HTTP.Timeout, cfg.Plugins.ToolCallTimeout)
	}

	if cfg.Security.ElicitationStrictEnabled() && !cfg.Security.ElicitationEnabled() {
		log.Printf("WARNING: security.elicitation_strict has no effect when security.elicitation is disabled")
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

	if cfg.Secrets.VaultPasswordFile != "" {
		cfg.Secrets.VaultPasswordFile = ResolveEnvVars(cfg.Secrets.VaultPasswordFile)
	}
	for id, path := range cfg.Secrets.VaultIDs {
		if path != "" {
			cfg.Secrets.VaultIDs[id] = ResolveEnvVars(path)
		}
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
	var result strings.Builder
	i := 0
	for i < len(s) {
		// Check for $$
		if i+1 < len(s) && s[i] == '$' && s[i+1] == '$' {
			result.WriteByte('$')
			i += 2
			continue
		}

		// Check for ${
		if i+1 < len(s) && s[i] == '$' && s[i+1] == '{' {
			// Find matching }
			depth := 1
			j := i + 2
			for j < len(s) && depth > 0 {
				switch {
				case j+1 < len(s) && s[j] == '$' && s[j+1] == '{':
					depth++
					j += 2
				case s[j] == '}':
					depth--
					j++
				default:
					j++
				}
			}

			if depth == 0 {
				// Extract inner content
				inner := s[i+2 : j-1]
				resolved := resolveVarExpression(inner, lookup)
				result.WriteString(resolved)
				i = j
				continue
			}
		}

		// Regular character
		result.WriteByte(s[i])
		i++
	}
	return result.String()
}

// findTopLevelPattern finds the first occurrence of pattern in s that is not inside ${...}
func findTopLevelPattern(s, pattern string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		if i+1 < len(s) && s[i] == '$' && s[i+1] == '{' {
			depth++
			i++ // skip the {
			continue
		}
		if s[i] == '}' && depth > 0 {
			depth--
			continue
		}
		if depth == 0 && strings.HasPrefix(s[i:], pattern) {
			return i
		}
	}
	return -1
}

func resolveVarExpression(inner string, lookup func(string) (string, bool)) string {
	// Check for |hostname modifier: ${VAR|hostname}
	// Only match if not inside nested ${...}
	if idx := findTopLevelPattern(inner, "|hostname"); idx >= 0 && idx+len("|hostname") == len(inner) {
		varName := inner[:idx]
		if val, ok := lookup(varName); ok && val != "" {
			if u, err := url.Parse(val); err == nil && u.Hostname() != "" {
				return u.Hostname()
			}
			log.Printf("config: ${%s|hostname}: could not extract hostname (missing scheme?)", varName)
		}
		return ""
	}

	// Use positional disambiguation: whichever operator appears first wins.
	idxDefault := findTopLevelPattern(inner, ":-")
	idxConditional := findTopLevelPattern(inner, ":+")

	if idxDefault >= 0 && (idxConditional < 0 || idxDefault < idxConditional) {
		varName := inner[:idxDefault]
		defaultVal := inner[idxDefault+2:]
		if val, ok := lookup(varName); ok && val != "" {
			return val
		}
		return resolveVarsFunc(defaultVal, lookup)
	} else if idxConditional >= 0 {
		varName := inner[:idxConditional]
		valueIfSet := inner[idxConditional+2:]
		if val, ok := lookup(varName); ok && val != "" {
			return resolveVarsFunc(valueIfSet, lookup)
		}
		return ""
	}

	// Simple ${VAR}
	val, _ := lookup(inner)
	return val
}
