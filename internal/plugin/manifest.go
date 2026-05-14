// Package plugin implements plugin process management, discovery,
// lifecycle, and the bidirectional JSON-lines transport.
package plugin

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/LeGambiArt/wtmcp/internal/domaincheck"
	"gopkg.in/yaml.v3"
)

const (
	maxToolDescriptionLen  = 4096
	maxParamDescriptionLen = 1024
)

// pluginNamePattern defines valid plugin names:
// lowercase alphanumeric, hyphens, underscores, 2-64 chars.
var pluginNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}[a-z0-9]$`)

// ValidatePluginName checks whether name matches the plugin name
// pattern. Returns an error if the name is invalid.
func ValidatePluginName(name string) error {
	if !pluginNamePattern.MatchString(name) {
		return fmt.Errorf("must be 2-64 lowercase alphanumeric chars, hyphens, or underscores")
	}
	return nil
}

// Manifest holds plugin metadata loaded from plugin.yaml.
type Manifest struct {
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	Description string `yaml:"description"`

	Execution   string `yaml:"execution"`   // "oneshot" or "persistent"
	Concurrency int    `yaml:"concurrency"` // default: 1
	Handler     string `yaml:"handler"`

	DependsOn       []string `yaml:"depends_on"`
	CredentialGroup string   `yaml:"credential_group"` // scopes env.d access
	EnvPassthrough  string   `yaml:"env_passthrough"`  // "all" to pass all group vars
	Env             []string `yaml:"env"`              // env vars to pass from credential group

	Services ServiceConfig     `yaml:"services"`
	Provides ProvidesConfig    `yaml:"provides"`
	Config   map[string]string `yaml:"config"`

	Tools        []ToolDef `yaml:"tools"`
	ContextFiles []string  `yaml:"context_files"`
	Priority     int       `yaml:"priority"`
	Enabled      *bool     `yaml:"enabled"`

	Output OutputConfig `yaml:"output"`
	Setup  SetupConfig  `yaml:"setup"`

	// Dir is the directory containing this manifest (set at load time).
	Dir string `yaml:"-"`

	// resolvedConfig holds the resolved config values (env vars expanded).
	// Set by the plugin manager after loading.
	resolvedConfig json.RawMessage `yaml:"-"`
}

// ServiceConfig declares what services a plugin requires.
type ServiceConfig struct {
	Auth  AuthServiceConfig  `yaml:"auth"`
	HTTP  HTTPServiceConfig  `yaml:"http"`
	Cache CacheServiceConfig `yaml:"cache"`
}

// AuthServiceConfig declares auth requirements.
type AuthServiceConfig struct {
	Type string `yaml:"type"`
	// Token holds a bearer token (type=bearer) or refresh/offline
	// token (type=refresh_token). Typically set via env var: "${MY_TOKEN}".
	Token           string                       `yaml:"token"`
	Header          string                       `yaml:"header"`
	Prefix          string                       `yaml:"prefix"`
	Username        string                       `yaml:"username"`
	Password        string                       `yaml:"password"`
	SPN             string                       `yaml:"spn"`
	SPNEGOProactive *bool                        `yaml:"spnego_proactive"`
	Scopes          []string                     `yaml:"scopes"`
	CredentialsFile string                       `yaml:"credentials_file"`
	TokenFile       string                       `yaml:"token_file"`
	TokenURL        string                       `yaml:"token_url"`
	ClientID        string                       `yaml:"client_id"`
	Select          string                       `yaml:"select"`
	Variants        map[string]AuthServiceConfig `yaml:"variants"`
	VariantOrder    []string                     `yaml:"-"` // populated from YAML key order
}

// HTTPServiceConfig declares HTTP proxy settings.
type HTTPServiceConfig struct {
	BaseURL         string    `yaml:"base_url"`
	AllowedDomains  []string  `yaml:"allowed_domains"`
	AllowPrivateIPs bool      `yaml:"allow_private_ips"`
	TLS             TLSConfig `yaml:"tls"`
}

// TLSConfig declares per-plugin TLS settings for custom CAs and mTLS.
type TLSConfig struct {
	CACert             string `yaml:"ca_cert"`
	ClientCert         string `yaml:"client_cert"`
	ClientKey          string `yaml:"client_key"`
	SkipHostnameVerify bool   `yaml:"skip_hostname_verify"`

	// CACertPEM holds the pre-loaded CA cert bytes (set at load time,
	// not from YAML). Prevents TOCTOU between validation and use.
	CACertPEM []byte `yaml:"-"`
}

// CacheServiceConfig declares cache settings.
type CacheServiceConfig struct {
	Enabled    *bool  `yaml:"enabled"`
	Namespace  string `yaml:"namespace"`
	DefaultTTL int    `yaml:"default_ttl"`
}

// ProvidesConfig declares what services a plugin provides.
type ProvidesConfig struct {
	Auth      *ProvidesAuthConfig `yaml:"auth"`
	Resources bool                `yaml:"resources"`
}

// ProvidesAuthConfig describes a plugin-provided auth type.
type ProvidesAuthConfig struct {
	Type        string `yaml:"type"`
	Description string `yaml:"description"`
}

// OutputConfig allows per-plugin output format override.
type OutputConfig struct {
	Format string `yaml:"format"`
}

// SetupConfig holds human-facing metadata for configuration wizards.
// The core parses it but does not act on it — consumed by CLI tools.
type SetupConfig struct {
	Credentials      map[string]CredentialMeta `yaml:"credentials"`
	Variants         map[string]SetupVariant   `yaml:"variants"`
	ValidationTool   string                    `yaml:"validation_tool"`
	PostSetupMessage string                    `yaml:"post_setup_message"`
}

// CredentialMeta describes how to obtain a credential value.
type CredentialMeta struct {
	Description  string `yaml:"description"`
	Example      string `yaml:"example"`
	HelpURL      string `yaml:"help_url"`
	Instructions string `yaml:"instructions"`
	Secret       bool   `yaml:"secret"`
}

// SetupVariant adds human-facing labels to auth variants.
type SetupVariant struct {
	Label       string   `yaml:"label"`
	Description string   `yaml:"description"`
	Required    []string `yaml:"required"`
}

// ToolDef declares an MCP tool with its parameter schema.
// ToolDef describes a single tool exposed by a plugin.
type ToolDef struct {
	Name        string              `yaml:"name"`
	Description string              `yaml:"description"`
	Access      string              `yaml:"access"`     // "read" or "write" (default: "write")
	Visibility  string              `yaml:"visibility"` // "primary" or "deferred" (default: "deferred")
	Params      map[string]ParamDef `yaml:"params"`
}

// IsReadOnly returns true if the tool is declared as read-only
// (no side effects).
func (t ToolDef) IsReadOnly() bool {
	return t.Access == "read"
}

// IsPrimary returns true if the tool should be registered without
// the defer_loading flag. Tools default to deferred.
func (t ToolDef) IsPrimary() bool {
	return t.Visibility == "primary"
}

// ParamDef describes a tool parameter.
type ParamDef struct {
	Type        string    `yaml:"type"`
	Description string    `yaml:"description"`
	Required    bool      `yaml:"required"`
	Default     any       `yaml:"default"`
	Items       *ItemsDef `yaml:"items"`
}

// ItemsDef describes array item types.
type ItemsDef struct {
	Type string `yaml:"type"`
}

// IsEnabled returns whether the plugin is enabled (defaults to true).
func (m *Manifest) IsEnabled() bool {
	if m.Enabled == nil {
		return true
	}
	return *m.Enabled
}

// CacheEnabled returns whether cache is enabled (defaults to true).
func (m *Manifest) CacheEnabled() bool {
	if m.Services.Cache.Enabled == nil {
		return true
	}
	return *m.Services.Cache.Enabled
}

// CacheNamespace returns the cache namespace (defaults to plugin name).
func (m *Manifest) CacheNamespace() string {
	if m.Services.Cache.Namespace != "" {
		return m.Services.Cache.Namespace
	}
	return m.Name
}

// SetResolvedConfig sets the resolved config JSON for the plugin.
func (m *Manifest) SetResolvedConfig(cfg json.RawMessage) {
	m.resolvedConfig = cfg
}

// ProvidesAuth returns true if this plugin provides an auth type.
func (m *Manifest) ProvidesAuth() bool {
	return m.Provides.Auth != nil && m.Provides.Auth.Type != ""
}

// ProvidesResources returns true if the plugin provides dynamic resources.
func (m *Manifest) ProvidesResources() bool {
	return m.Provides.Resources
}

// HandlerPath returns the absolute path to the handler executable.
func (m *Manifest) HandlerPath() string {
	return filepath.Join(m.Dir, m.Handler)
}

const maxManifestSize = 256 * 1024 // 256KB

// LoadManifest reads and validates a plugin.yaml file.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path) //nolint:gosec // plugin loading requires reading from variable paths
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	if len(data) > maxManifestSize {
		return nil, fmt.Errorf("manifest %s exceeds %d byte limit (%d bytes)", path, maxManifestSize, len(data))
	}

	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}

	m.Dir = filepath.Dir(path)

	// Set defaults
	if m.Concurrency == 0 {
		m.Concurrency = 1
	}
	if m.Execution == "" {
		m.Execution = "persistent"
	}

	// Extract variant order from the raw YAML to preserve declaration order
	if m.Services.Auth.Variants != nil {
		m.Services.Auth.VariantOrder, err = extractVariantOrder(data)
		if err != nil {
			return nil, fmt.Errorf("parse auth variants order: %w", err)
		}
	}

	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("invalid manifest %s: %w", path, err)
	}

	return &m, nil
}

// Validate checks the manifest for correctness.
func (m *Manifest) Validate() error {
	if !pluginNamePattern.MatchString(m.Name) {
		return fmt.Errorf("invalid plugin name %q: must match [a-z0-9][a-z0-9_-]{0,62}[a-z0-9]", m.Name)
	}

	if m.Execution != "oneshot" && m.Execution != "persistent" {
		return fmt.Errorf("execution must be 'oneshot' or 'persistent', got %q", m.Execution)
	}

	if m.CredentialGroup != "" && !pluginNamePattern.MatchString(m.CredentialGroup) {
		return fmt.Errorf("invalid credential_group %q: must match [a-z0-9][a-z0-9_-]{0,62}[a-z0-9]", m.CredentialGroup)
	}

	if m.EnvPassthrough != "" && m.EnvPassthrough != "all" {
		return fmt.Errorf("env_passthrough must be 'all' or empty, got %q", m.EnvPassthrough)
	}

	if m.ProvidesResources() && m.Execution == "oneshot" {
		return fmt.Errorf("provides.resources requires execution: persistent")
	}

	if m.Handler == "" {
		return fmt.Errorf("handler is required")
	}

	// Verify handler path stays within plugin directory.
	// Resolve symlinks to prevent escaping via symlink chains.
	handlerPath := filepath.Join(m.Dir, m.Handler)
	absHandler, err := filepath.Abs(handlerPath)
	if err != nil {
		return fmt.Errorf("cannot resolve handler path: %w", err)
	}
	// EvalSymlinks also calls Abs, but requires the path to exist.
	// Only resolve if the handler file exists (it may not during
	// manifest-only validation).
	if resolved, err := filepath.EvalSymlinks(handlerPath); err == nil {
		absHandler = resolved
	}
	absDir, err := filepath.Abs(m.Dir)
	if err != nil {
		return fmt.Errorf("cannot resolve plugin dir: %w", err)
	}
	if resolvedDir, err := filepath.EvalSymlinks(m.Dir); err == nil {
		absDir = resolvedDir
	}
	if !strings.HasPrefix(absHandler, absDir+string(filepath.Separator)) {
		return fmt.Errorf("handler path escapes plugin directory: %s", m.Handler)
	}

	// Validate base_url if set and not a template (${VAR} resolved later)
	if m.Services.HTTP.BaseURL != "" && !strings.Contains(m.Services.HTTP.BaseURL, "${") {
		u, err := url.Parse(m.Services.HTTP.BaseURL)
		if err != nil {
			return fmt.Errorf("invalid base_url: %w", err)
		}
		if u.Scheme != "https" && u.Scheme != "http" {
			return fmt.Errorf("base_url must use http or https: %s", m.Services.HTTP.BaseURL)
		}
		if u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("base_url must not contain query or fragment: %s", m.Services.HTTP.BaseURL)
		}
	}

	// Validate token_url for refresh_token auth (must be HTTPS).
	// Skips template strings (${VAR}) — those are validated by the
	// provider constructor after env var resolution.
	authCfg := m.Services.Auth
	if authCfg.Type == "refresh_token" && authCfg.TokenURL != "" &&
		!strings.Contains(authCfg.TokenURL, "${") {
		u, err := url.Parse(authCfg.TokenURL)
		if err != nil {
			return fmt.Errorf("invalid token_url: %w", err)
		}
		if u.Scheme != "https" {
			return fmt.Errorf("token_url must use https: %s", authCfg.TokenURL)
		}
	}

	// Validate TLS config
	tlsCfg := m.Services.HTTP.TLS
	if (tlsCfg.ClientCert != "") != (tlsCfg.ClientKey != "") {
		return fmt.Errorf("client_cert and client_key must both be set or both be empty")
	}
	hasAuth := m.Services.Auth.Type != "" || len(m.Services.Auth.Variants) > 0
	if tlsCfg.ClientCert != "" && hasAuth {
		return fmt.Errorf("client_cert (mTLS) and services.auth cannot both be set")
	}

	// allow_private_ips requires domain restrictions as defense in depth.
	// Domains come from either explicit allowed_domains or the base_url
	// hostname (auto-added at load time by manager.go).
	if m.Services.HTTP.AllowPrivateIPs && len(m.Services.HTTP.AllowedDomains) == 0 && m.Services.HTTP.BaseURL == "" {
		return fmt.Errorf("allow_private_ips requires allowed_domains or base_url to be set")
	}

	// Validate allowed_domains — skip template entries (${VAR})
	// which are resolved later at load time in manager.go.
	for _, domain := range m.Services.HTTP.AllowedDomains {
		if strings.Contains(domain, "${") {
			continue
		}
		if err := validateDomain(domain); err != nil {
			return fmt.Errorf("allowed_domains: %w", err)
		}
	}

	// Validate tools
	for _, tool := range m.Tools {
		if tool.Name == "" {
			return fmt.Errorf("tool name is required")
		}
		if len(tool.Description) > maxToolDescriptionLen {
			return fmt.Errorf("tool %s: description exceeds %d bytes", tool.Name, maxToolDescriptionLen)
		}
		if tool.Access != "" && tool.Access != "read" && tool.Access != "write" {
			return fmt.Errorf("tool %s: access must be 'read' or 'write', got %q", tool.Name, tool.Access)
		}
		if tool.Visibility != "" && tool.Visibility != "primary" && tool.Visibility != "deferred" {
			return fmt.Errorf("tool %s: visibility must be 'primary' or 'deferred', got %q", tool.Name, tool.Visibility)
		}
		for pname, param := range tool.Params {
			if len(param.Description) > maxParamDescriptionLen {
				return fmt.Errorf("tool %s param %s: description exceeds %d bytes", tool.Name, pname, maxParamDescriptionLen)
			}
		}
	}

	return nil
}

// ParamsSchema converts the tool's parameter definitions to JSON Schema
// as required by the MCP spec.
func (t *ToolDef) ParamsSchema() map[string]any {
	properties := make(map[string]any)
	var required []string

	for name, p := range t.Params {
		prop := map[string]any{"type": p.Type}
		if p.Description != "" {
			prop["description"] = p.Description
		}
		if p.Default != nil {
			prop["default"] = p.Default
		}
		if p.Type == "array" && p.Items != nil {
			prop["items"] = map[string]any{"type": p.Items.Type}
		}
		properties[name] = prop
		if p.Required {
			required = append(required, name)
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// extractVariantOrder parses the YAML to get auth variant keys in
// declaration order, since Go maps don't preserve insertion order.
// validateDomain rejects domain entries that are IP addresses,
// localhost, or private/link-local ranges.
// validateDomain validates a domain using the shared domaincheck package.
// This ensures consistent validation across init-time and per-request paths.
func validateDomain(domain string) error {
	return domaincheck.Validate(domain)
}

func extractVariantOrder(data []byte) ([]string, error) {
	var raw struct {
		Services struct {
			Auth struct {
				Variants yaml.Node `yaml:"variants"`
			} `yaml:"auth"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	node := &raw.Services.Auth.Variants
	if node.Kind != yaml.MappingNode {
		return nil, nil
	}

	var order []string
	for i := 0; i < len(node.Content)-1; i += 2 {
		order = append(order, node.Content[i].Value)
	}
	return order, nil
}
