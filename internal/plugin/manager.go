package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/LeGambiArt/wtmcp/internal/auth"
	"github.com/LeGambiArt/wtmcp/internal/cache"
	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/protocol"
	"github.com/LeGambiArt/wtmcp/internal/proxy"
)

// DisabledPlugin records a plugin that was discovered but could not
// be loaded due to a configuration issue (e.g., env.d file with bad
// permissions). Its tools are registered with [DISABLED] descriptions
// so the LLM can tell the user how to fix it.
type DisabledPlugin struct {
	Name     string
	Reason   string
	Manifest *Manifest
}

// Manager discovers, loads, and manages plugin lifecycles.
type Manager struct {
	handles    map[string]*Handle
	manifests  map[string]*Manifest
	disabled   map[string]DisabledPlugin
	envGroups  config.EnvGroups
	envErrors  map[string]string // credential group → error message
	workdir    string
	authReg    *auth.Registry
	proxy      *proxy.Proxy
	cache      cache.Store
	cfg        *config.Config
	svcHandler *serviceHandlerImpl
}

// NewManager creates a plugin manager. envErrors maps credential
// group names to their load errors (from LoadEnvGroups). Plugins
// whose credential_group appears in envErrors will be disabled
// during LoadAll instead of loaded. workdir is needed to re-read
// env.d files on plugin reload.
func NewManager(authReg *auth.Registry, p *proxy.Proxy, c cache.Store, cfg *config.Config, envGroups config.EnvGroups, envErrors map[string]string, workdir string) *Manager {
	if envErrors == nil {
		envErrors = make(map[string]string)
	}
	return &Manager{
		handles:    make(map[string]*Handle),
		manifests:  make(map[string]*Manifest),
		disabled:   make(map[string]DisabledPlugin),
		envGroups:  envGroups,
		envErrors:  envErrors,
		workdir:    workdir,
		authReg:    authReg,
		proxy:      p,
		cache:      c,
		cfg:        cfg,
		svcHandler: &serviceHandlerImpl{proxy: p, cache: c},
	}
}

// Discover scans directories for plugin.yaml files and loads manifests.
// First directory wins for a given plugin name; duplicates in later
// directories are skipped with a warning. userDir, if non-empty,
// identifies the user plugins directory — plugins from it are
// restricted (e.g., cannot declare provides.auth).
func (m *Manager) Discover(dirs []string, userDir string) error {
	// Track credential groups claimed by system plugins so user
	// plugins cannot steal credentials by declaring the same group.
	systemGroups := make(map[string]string) // group → plugin name

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read plugin dir %s: %w", dir, err)
		}
		isUserDir := userDir != "" && dir == userDir

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			manifestPath := filepath.Join(dir, entry.Name(), "plugin.yaml")
			if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
				continue
			}
			manifest, err := LoadManifest(manifestPath)
			if err != nil {
				log.Printf("skipping plugin %s: %v", entry.Name(), err)
				continue
			}
			if !manifest.IsEnabled() {
				log.Printf("plugin %s is disabled", manifest.Name)
				continue
			}
			if slices.Contains(m.cfg.Plugins.Disabled, manifest.Name) {
				log.Printf("plugin %s is disabled via config", manifest.Name)
				continue
			}
			if existing, ok := m.manifests[manifest.Name]; ok {
				log.Printf("WARNING: plugin %q in %s skipped — already registered from %s",
					manifest.Name, manifest.Dir, existing.Dir)
				continue
			}
			if isUserDir {
				if manifest.ProvidesAuth() {
					log.Printf("WARNING: user plugin %q declares provides.auth — skipped (not allowed)",
						manifest.Name)
					continue
				}
				if manifest.CredentialGroup != "" {
					if owner, ok := systemGroups[manifest.CredentialGroup]; ok {
						log.Printf("WARNING: user plugin %q declares credential_group %q (owned by %s) — skipped",
							manifest.Name, manifest.CredentialGroup, owner)
						continue
					}
				}
			} else if manifest.CredentialGroup != "" {
				systemGroups[manifest.CredentialGroup] = manifest.Name
			}
			m.manifests[manifest.Name] = manifest
		}
	}
	return nil
}

// LoadAll loads all discovered plugins in dependency order.
// Auth-providing plugins are loaded first (two-pass loading).
func (m *Manager) LoadAll(ctx context.Context) error {
	sorted, err := m.topologicalSort()
	if err != nil {
		return fmt.Errorf("dependency resolution: %w", err)
	}

	// Pass 1: load auth-providing plugins
	for _, name := range sorted {
		manifest := m.manifests[name]
		if manifest.ProvidesAuth() {
			if err := m.Load(ctx, name); err != nil {
				log.Printf("failed to load auth provider %s: %v", name, err)
			}
		}
	}

	// Pass 2: load everything else
	for _, name := range sorted {
		manifest := m.manifests[name]
		if manifest.ProvidesAuth() {
			continue // already loaded
		}

		// Check if the plugin's credential group has an env.d error
		if manifest.CredentialGroup != "" {
			if errMsg, ok := m.envErrors[manifest.CredentialGroup]; ok {
				m.disabled[name] = DisabledPlugin{
					Name:     name,
					Reason:   errMsg,
					Manifest: manifest,
				}
				log.Printf("plugin %s disabled: credential group %q: %s",
					name, manifest.CredentialGroup, errMsg)
				continue
			}
		}

		if err := m.Load(ctx, name); err != nil {
			log.Printf("failed to load plugin %s: %v", name, err)
		}
	}

	return nil
}

// Load starts a single plugin by name.
func (m *Manager) Load(ctx context.Context, name string) error {
	manifest, ok := m.manifests[name]
	if !ok {
		return fmt.Errorf("unknown plugin: %s", name)
	}

	// Resolve config
	resolvedCfg := m.resolveConfig(manifest)
	cfgJSON, err := json.Marshal(resolvedCfg)
	if err != nil {
		return fmt.Errorf("marshal config for %s: %w", name, err)
	}
	manifest.SetResolvedConfig(cfgJSON)

	// Register with proxy
	vars := m.pluginVars(manifest)
	resolve := func(s string) string { return config.ResolveVars(s, vars) }

	// Resolve allowed_domains from env.d, then extract hostnames
	// from any entries that resolved to full URLs.
	domains := make([]string, 0, len(manifest.Services.HTTP.AllowedDomains)+1)
	for _, d := range manifest.Services.HTTP.AllowedDomains {
		resolved := resolve(d)
		// Extract hostname from full URLs (e.g., https://host:8891 → host)
		if strings.Contains(resolved, "://") {
			if u, err := url.Parse(resolved); err == nil && u.Hostname() != "" {
				resolved = u.Hostname()
			}
		}
		domains = append(domains, resolved)
	}

	// Auto-add base_url hostname to allowed_domains. This bypasses
	// validateDomain() since it is derived from the already-configured
	// base_url, not user-declared (allows localhost from defaults).
	resolvedBaseURL := resolve(manifest.Services.HTTP.BaseURL)
	if resolvedBaseURL != "" {
		if u, err := url.Parse(resolvedBaseURL); err == nil && u.Hostname() != "" {
			domains = append(domains, u.Hostname())
		}
	}

	// Resolve TLS config paths from env.d
	tlsCfg := proxy.TLSConfig{
		CACert:             resolve(manifest.Services.HTTP.TLS.CACert),
		ClientCert:         resolve(manifest.Services.HTTP.TLS.ClientCert),
		ClientKey:          resolve(manifest.Services.HTTP.TLS.ClientKey),
		SkipHostnameVerify: manifest.Services.HTTP.TLS.SkipHostnameVerify,
	}

	// Load CA cert bytes once (TOCTOU prevention)
	if tlsCfg.CACert != "" {
		pem, err := os.ReadFile(tlsCfg.CACert) //nolint:gosec // path from env.d (permission-checked)
		if err != nil {
			return fmt.Errorf("[%s] read ca_cert %s: %w", name, tlsCfg.CACert, err)
		}
		tlsCfg.CACertPEM = pem
		log.Printf("[%s] loaded CA cert from %s", name, tlsCfg.CACert)
	}

	// Validate client key permissions
	if tlsCfg.ClientKey != "" {
		info, err := os.Stat(tlsCfg.ClientKey)
		if err != nil {
			return fmt.Errorf("[%s] stat client_key %s: %w", name, tlsCfg.ClientKey, err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("[%s] client_key %s mode %04o, must not be group/other accessible",
				name, tlsCfg.ClientKey, info.Mode().Perm())
		}
	}

	pa := &proxy.PluginAuth{
		BaseURL:         resolvedBaseURL,
		AllowedDomains:  domains,
		AllowPrivateIPs: manifest.Services.HTTP.AllowPrivateIPs,
		TLS:             tlsCfg,
	}

	// Build per-plugin HTTP client based on auth and TLS config.
	// Kerberos gets a cookie jar + SPNEGORoundTripper.
	// mTLS gets a client with custom TLS transport.
	// Both may include custom CA certs.
	switch {
	case m.isKerberosAuth(manifest):
		spn := config.ResolveVars(manifest.Services.Auth.SPN, vars)
		client, err := proxy.NewKerberosClient(spn, pa.AllowPrivateIPs, pa.TLS)
		if err != nil {
			return fmt.Errorf("[%s] create kerberos client: %w", name, err)
		}
		pa.Client = client
		pa.IsKerberos = true
		log.Printf("[%s] using kerberos client (spn=%q)", name, spn)
	case pa.TLS.HasConfig():
		transport, err := proxy.SafeTransportWithTLS(pa.AllowPrivateIPs, pa.TLS)
		if err != nil {
			return fmt.Errorf("[%s] create TLS transport: %w", name, err)
		}
		pa.Client = &http.Client{
			Transport:     transport,
			CheckRedirect: proxy.StripAuthOnCrossDomainRedirect,
		}
		log.Printf("[%s] using TLS client (ca=%v, mtls=%v, skip_hostname=%v)",
			name, pa.TLS.CACert != "", pa.TLS.ClientCert != "", pa.TLS.SkipHostnameVerify)
	default:
		pa.Provider = m.resolveAuth(manifest)
	}

	m.proxy.RegisterPlugin(name, pa)

	// Create handle and start
	processCfg := ProcessConfig{
		InitTimeout:       m.cfg.Plugins.InitTimeout,
		ShutdownTimeout:   m.cfg.Plugins.ShutdownTimeout,
		ShutdownKillAfter: m.cfg.Plugins.ShutdownKillAfter,
		MaxMessageSize:    int(m.cfg.Plugins.MaxMessageSize),
	}

	handle := NewHandle(manifest, m.svcHandler, processCfg, m.cfg.Plugins.ToolCallTimeout, vars)

	if manifest.Execution == "persistent" {
		if err := handle.Start(ctx); err != nil {
			return err
		}
	}

	m.handles[name] = handle
	log.Printf("loaded plugin %s (v%s, %s)", name, manifest.Version, manifest.Execution)
	return nil
}

// Unload stops a plugin.
func (m *Manager) Unload(ctx context.Context, name string) error {
	handle, ok := m.handles[name]
	if !ok {
		return fmt.Errorf("plugin not loaded: %s", name)
	}
	if err := handle.Stop(ctx); err != nil {
		return err
	}
	delete(m.handles, name)
	log.Printf("unloaded plugin %s", name)
	return nil
}

// Reload stops and restarts a plugin. Re-reads the env.d file
// to pick up any changes (e.g., new IPA_CA_CERT added by
// create_config). If the plugin was disabled due to an env.d
// error, enables it if the issue is resolved.
func (m *Manager) Reload(ctx context.Context, name string) error {
	// Re-read the env.d file for this plugin's credential group.
	// This picks up vars added/changed since startup (e.g., by
	// create_config writing IPA_CA_CERT to the env.d file).
	var group string
	if dp, ok := m.disabled[name]; ok {
		group = dp.Manifest.CredentialGroup
	} else if manifest, ok := m.manifests[name]; ok {
		group = manifest.CredentialGroup
	}
	if group != "" && m.workdir != "" {
		vars, err := config.LoadSingleEnvGroup(m.workdir, group)
		if err != nil {
			if _, wasDisabled := m.disabled[name]; wasDisabled {
				return fmt.Errorf("env group %s still has issues: %w", group, err)
			}
			log.Printf("[%s] warning: env group %s re-read failed: %v", name, group, err)
		} else {
			m.envGroups[group] = vars
			delete(m.envErrors, group)
			delete(m.disabled, name)
			log.Printf("[%s] env group %s re-read (%d vars)", name, group, len(vars))
		}
	}

	if _, ok := m.handles[name]; ok {
		if err := m.Unload(ctx, name); err != nil {
			return err
		}
	}
	return m.Load(ctx, name)
}

// ShutdownAll stops all loaded plugins.
func (m *Manager) ShutdownAll(ctx context.Context) {
	for name, handle := range m.handles {
		if err := handle.Stop(ctx); err != nil {
			log.Printf("error stopping %s: %v", name, err)
		}
	}
}

// CallTool dispatches a tool call to the appropriate plugin.
func (m *Manager) CallTool(_ context.Context, toolName string) (string, *Handle) {
	for name, manifest := range m.manifests {
		for _, tool := range manifest.Tools {
			if tool.Name == toolName {
				handle, ok := m.handles[name]
				if !ok {
					return name, nil
				}
				return name, handle
			}
		}
	}
	return "", nil
}

// Manifests returns all discovered manifests.
func (m *Manager) Manifests() map[string]*Manifest {
	return m.manifests
}

// DisabledPlugins returns plugins that were discovered but could
// not be loaded due to configuration issues.
func (m *Manager) DisabledPlugins() map[string]DisabledPlugin {
	return m.disabled
}

// LoadedPlugins returns the names of successfully loaded plugins.
func (m *Manager) LoadedPlugins() []string {
	names := make([]string, 0, len(m.handles))
	for name := range m.handles {
		names = append(names, name)
	}
	return names
}

// ToolOwner returns the plugin name that owns a tool.
func (m *Manager) ToolOwner(toolName string) string {
	name, _ := m.CallTool(context.Background(), toolName)
	return name
}

// pluginVars returns the scoped env.d variables for a plugin based
// on its credential_group. Returns nil if no group is declared or
// no matching env.d file exists.
func (m *Manager) pluginVars(manifest *Manifest) map[string]string {
	if manifest.CredentialGroup == "" {
		return nil
	}
	return m.envGroups.Get(manifest.CredentialGroup)
}

func (m *Manager) resolveConfig(manifest *Manifest) map[string]string {
	resolved := config.ResolveVarsMap(manifest.Config, m.pluginVars(manifest))
	// Inject per-group credentials dir so plugins can find credential
	// files (e.g., OAuth2 tokens). Uses underscore prefix to avoid
	// collisions with plugin-declared config keys.
	if m.cfg.CredentialsDir != "" && manifest.CredentialGroup != "" {
		resolved["_credentials_dir"] = filepath.Join(
			m.cfg.CredentialsDir, manifest.CredentialGroup)
	}
	// Inject work_dir so plugins can access the working directory
	if m.workdir != "" {
		resolved["_work_dir"] = m.workdir
	}
	return resolved
}

// Handle returns the handle for a loaded plugin, or nil if not loaded.
func (m *Manager) Handle(name string) *Handle {
	return m.handles[name]
}

// isKerberosAuth checks if a plugin uses Kerberos auth (without variants).
// Variant-based Kerberos (like Jira's server-kerberos) goes through the
// normal resolveAuth path; only pure Kerberos plugins get a per-plugin client.
func (m *Manager) isKerberosAuth(manifest *Manifest) bool {
	authCfg := manifest.Services.Auth
	if len(authCfg.Variants) > 0 {
		return false
	}
	return authCfg.Type == "kerberos" || authCfg.Type == "kerberos/spnego"
}

func (m *Manager) resolveAuth(manifest *Manifest) auth.Provider {
	authCfg := manifest.Services.Auth
	if authCfg.Type == "" && len(authCfg.Variants) == 0 {
		return nil
	}

	vars := m.pluginVars(manifest)
	resolve := func(s string) string { return config.ResolveVars(s, vars) }

	var variantCfg auth.VariantConfig
	if len(authCfg.Variants) > 0 {
		variantCfg.Select = resolve(authCfg.Select)
		variantCfg.VariantOrder = authCfg.VariantOrder
		variantCfg.Variants = make(map[string]auth.SingleAuthConfig)
		for name, v := range authCfg.Variants {
			variantCfg.Variants[name] = auth.SingleAuthConfig{
				Type:            v.Type,
				Token:           resolve(v.Token),
				Header:          v.Header,
				Prefix:          v.Prefix,
				Username:        resolve(v.Username),
				Password:        resolve(v.Password),
				SPN:             resolve(v.SPN),
				Scopes:          v.Scopes,
				CredentialsFile: resolve(v.CredentialsFile),
				TokenFile:       resolve(v.TokenFile),
				CredentialsDir:  m.cfg.CredentialsDir,
				TokenURL:        resolve(v.TokenURL),
				ClientID:        resolve(v.ClientID),
			}
		}
	} else {
		// Single auth type — resolve vars and wrap as a single
		// variant so ResolveVariant gets the full config.
		variantCfg.Select = "default"
		variantCfg.VariantOrder = []string{"default"}
		variantCfg.Variants = map[string]auth.SingleAuthConfig{
			"default": {
				Type:            authCfg.Type,
				Token:           resolve(authCfg.Token),
				Header:          authCfg.Header,
				Prefix:          authCfg.Prefix,
				Username:        resolve(authCfg.Username),
				Password:        resolve(authCfg.Password),
				SPN:             resolve(authCfg.SPN),
				Scopes:          authCfg.Scopes,
				CredentialsFile: resolve(authCfg.CredentialsFile),
				TokenFile:       resolve(authCfg.TokenFile),
				CredentialsDir:  m.cfg.CredentialsDir,
				TokenURL:        resolve(authCfg.TokenURL),
				ClientID:        resolve(authCfg.ClientID),
			},
		}
	}

	provider, err := auth.ResolveVariant(variantCfg)
	if err != nil {
		log.Printf("[%s] auth resolution failed: %v", manifest.Name, err)
		return nil
	}
	return provider
}

func (m *Manager) topologicalSort() ([]string, error) {
	// Pre-filter: skip plugins with unresolvable or skipped
	// dependencies. Propagate transitively until stable.
	skipped := make(map[string]bool)
	changed := true
	for changed {
		changed = false
		for name, manifest := range m.manifests {
			if skipped[name] {
				continue
			}
			for _, dep := range manifest.DependsOn {
				if _, exists := m.manifests[dep]; !exists {
					log.Printf("WARNING: plugin %s depends on %s which is not available — skipping",
						name, dep)
					skipped[name] = true
					changed = true
					break
				}
				if skipped[dep] {
					log.Printf("WARNING: plugin %s depends on skipped plugin %s — skipping",
						name, dep)
					skipped[name] = true
					changed = true
					break
				}
			}
		}
	}

	// Build adjacency from depends_on (excluding skipped)
	deps := make(map[string][]string)
	for name, manifest := range m.manifests {
		if skipped[name] {
			continue
		}
		deps[name] = manifest.DependsOn
	}

	var sorted []string
	visited := make(map[string]bool)
	visiting := make(map[string]bool)

	var visit func(string) error
	visit = func(name string) error {
		if visited[name] {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("circular dependency involving %s", name)
		}
		visiting[name] = true

		for _, dep := range deps[name] {
			if err := visit(dep); err != nil {
				return err
			}
		}

		visiting[name] = false
		visited[name] = true
		sorted = append(sorted, name)
		return nil
	}

	// Visit all plugins, sorted by priority for deterministic order
	names := m.sortedByPriority()
	for _, name := range names {
		if skipped[name] {
			continue
		}
		if err := visit(name); err != nil {
			return nil, err
		}
	}

	return sorted, nil
}

func (m *Manager) sortedByPriority() []string {
	type entry struct {
		name     string
		priority int
	}
	var entries []entry
	for name, manifest := range m.manifests {
		entries = append(entries, entry{name: name, priority: manifest.Priority})
	}
	// Simple insertion sort — plugin count is small
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].priority < entries[j-1].priority; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.name
	}
	return names
}

// serviceHandlerImpl implements ServiceHandler by delegating to proxy and cache.
type serviceHandlerImpl struct {
	proxy *proxy.Proxy
	cache cache.Store
}

func (s *serviceHandlerImpl) HandleHTTP(pluginName string, req protocol.Message) protocol.Message {
	return s.proxy.Execute(context.Background(), pluginName, req)
}

func (s *serviceHandlerImpl) HandleCache(pluginName string, req protocol.Message) protocol.Message {
	ctx := context.Background()
	namespace := pluginName // default namespace

	switch req.Type {
	case protocol.TypeCacheGet:
		if err := cache.ValidateKey(req.Key); err != nil {
			return cacheError(req.ID, req.Type, err)
		}
		value, hit, err := s.cache.Get(ctx, namespace, req.Key)
		if err != nil {
			return cacheError(req.ID, req.Type, err)
		}
		h := hit
		resp := protocol.Message{ID: req.ID, Type: protocol.TypeCacheGet, Hit: &h}
		if hit {
			resp.Value = value
		}
		return resp

	case protocol.TypeCacheSet:
		if err := cache.ValidateKey(req.Key); err != nil {
			return cacheError(req.ID, req.Type, err)
		}
		ttl := time.Duration(0)
		if req.TTL != nil {
			ttl = time.Duration(*req.TTL) * time.Second
		}
		err := s.cache.Set(ctx, namespace, req.Key, req.Value, ttl)
		ok := err == nil
		resp := protocol.Message{ID: req.ID, Type: protocol.TypeCacheSet, OK: &ok}
		if err != nil {
			resp.Error = &protocol.Error{Code: "cache_error", Message: err.Error()}
		}
		return resp

	case protocol.TypeCacheDel:
		if err := cache.ValidateKey(req.Key); err != nil {
			return cacheError(req.ID, req.Type, err)
		}
		deleted, err := s.cache.Del(ctx, namespace, req.Key)
		if err != nil {
			return cacheError(req.ID, req.Type, err)
		}
		ok := true
		return protocol.Message{ID: req.ID, Type: protocol.TypeCacheDel, OK: &ok, Deleted: &deleted}

	case protocol.TypeCacheList:
		keys, err := s.cache.List(ctx, namespace, req.Pattern)
		if err != nil {
			return cacheError(req.ID, req.Type, err)
		}
		return protocol.Message{ID: req.ID, Type: protocol.TypeCacheList, Keys: keys}

	case protocol.TypeCacheFlush:
		count, err := s.cache.Flush(ctx, namespace)
		if err != nil {
			return cacheError(req.ID, req.Type, err)
		}
		ok := true
		return protocol.Message{ID: req.ID, Type: protocol.TypeCacheFlush, OK: &ok, Count: &count}

	default:
		return protocol.Message{
			ID:    req.ID,
			Type:  req.Type,
			Error: &protocol.Error{Code: "unknown_cache_op", Message: "unknown cache operation: " + req.Type},
		}
	}
}

func cacheError(id, msgType string, err error) protocol.Message {
	return protocol.Message{
		ID:    id,
		Type:  msgType,
		Error: &protocol.Error{Code: "cache_error", Message: err.Error()},
	}
}
