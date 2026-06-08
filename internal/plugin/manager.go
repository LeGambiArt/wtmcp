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
	"sync"
	"time"

	"github.com/LeGambiArt/wtmcp/internal/auth"
	"github.com/LeGambiArt/wtmcp/internal/cache"
	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/fileio"
	"github.com/LeGambiArt/wtmcp/internal/protocol"
	"github.com/LeGambiArt/wtmcp/internal/proxy"
	"github.com/LeGambiArt/wtmcp/internal/sandbox"
	"github.com/LeGambiArt/wtmcp/internal/secrets/securefile"
	"github.com/LeGambiArt/wtmcp/internal/secrets/vault"
)

const dataDir = ".wtmcp-data"

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
	reloadMu       sync.Mutex
	handlesMu      sync.RWMutex
	handles        map[string]*Handle
	manifests      map[string]*Manifest
	disabled       map[string]DisabledPlugin
	configDisabled map[string]*Manifest // plugins skipped via plugins.disabled config
	envGroups      config.EnvGroups
	envErrors      map[string]string // credential group → error message
	envDirError    string            // env.d directory-level error
	workdir        string
	sessionDir     string
	envDir         string
	envLoadOpts    config.EnvLoadOptions
	vaultPassword  func(vaultID string) ([]byte, error)
	secureTracker  *securefile.Tracker
	authReg        *auth.Registry
	proxy          *proxy.Proxy
	cache          cache.Store
	cfg            *config.Config
	sbMgr          *sandbox.Manager
	svcHandler     *serviceHandlerImpl
	loadDone       chan struct{} // closed when LoadAll completes
	pending        [][]string    // prepared plugin levels awaiting Start
}

// NewManager creates a plugin manager. envErrors maps credential
// group names to their load errors (from LoadEnvGroups). Plugins
// whose credential_group appears in envErrors will be disabled
// during LoadAll instead of loaded. envDirError, if non-empty,
// indicates the env.d directory itself has a problem (bad
// permissions, stat failure) — all credential-dependent plugins
// will be disabled. envDir is the resolved env.d directory path
// used to re-read env files on plugin reload.
func NewManager(authReg *auth.Registry, p *proxy.Proxy, c cache.Store, cfg *config.Config, envGroups config.EnvGroups, envErrors map[string]string, envDirError, workdir, envDir string, envLoadOpts config.EnvLoadOptions, sessionDir string) *Manager {
	if envGroups == nil {
		envGroups = make(config.EnvGroups)
	}
	if envErrors == nil {
		envErrors = make(map[string]string)
	}
	return &Manager{
		handles:        make(map[string]*Handle),
		manifests:      make(map[string]*Manifest),
		disabled:       make(map[string]DisabledPlugin),
		configDisabled: make(map[string]*Manifest),
		envGroups:      envGroups,
		envErrors:      envErrors,
		envDirError:    envDirError,
		workdir:        workdir,
		sessionDir:     sessionDir,
		envDir:         envDir,
		envLoadOpts:    envLoadOpts,
		vaultPassword:  envLoadOpts.VaultPassword,
		secureTracker:  securefile.NewTracker(),
		authReg:        authReg,
		proxy:          p,
		cache:          c,
		cfg:            cfg,
		svcHandler:     &serviceHandlerImpl{proxy: p, cache: c},
		loadDone:       make(chan struct{}),
	}
}

// SetSandbox configures the sandbox manager. When set, all plugin
// processes are launched in arapuca sandboxes. Must be called before
// LoadAll.
func (m *Manager) SetSandbox(sb *sandbox.Manager) {
	m.sbMgr = sb
}

// Discover scans directories for plugin.yaml files and loads manifests.
// First directory wins for a given plugin name; duplicates in later
// directories are skipped with a warning. userDir, if non-empty,
// identifies the user plugins directory — plugins from it are
// restricted (e.g., cannot declare provides.auth).
func (m *Manager) Discover(dirs []string, userDir string) error {
	// Track credential groups claimed by system plugins so user
	// plugins cannot steal credentials by declaring the same group.
	systemGroups := make(map[string]string)     // group → plugin name
	sanitizedTaskIDs := make(map[string]string) // sanitized → original name

	// Track all plugin names seen during discovery (including
	// manifest-disabled) for post-loop validation of plugins.disabled.
	seenNames := make(map[string]bool)

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				log.Printf("WARNING: skipping plugin dir %s: %v", dir, err)
			}
			continue
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
			seenNames[manifest.Name] = true
			if !manifest.IsEnabled() {
				log.Printf("plugin %s is disabled", manifest.Name)
				continue
			}
			if len(m.cfg.Plugins.Enabled) > 0 {
				// Allowlist mode: only load plugins in the enabled list
				if !slices.Contains(m.cfg.Plugins.Enabled, manifest.Name) {
					log.Printf("plugin %s not in allowlist", manifest.Name)
					m.configDisabled[manifest.Name] = manifest
					continue
				}
			} else if slices.Contains(m.cfg.Plugins.Disabled, manifest.Name) {
				log.Printf("plugin %s is disabled via config", manifest.Name)
				m.configDisabled[manifest.Name] = manifest
				continue
			}
			if existing, ok := m.manifests[manifest.Name]; ok {
				log.Printf("WARNING: plugin %q in %s skipped — already registered from %s",
					manifest.Name, manifest.Dir, existing.Dir)
				continue
			}
			taskID, err := sandbox.SanitizeTaskID(manifest.Name)
			if err != nil {
				log.Printf("WARNING: plugin %q: %v — skipped", manifest.Name, err)
				continue
			}
			if owner, ok := sanitizedTaskIDs[taskID]; ok && owner != manifest.Name {
				log.Printf("WARNING: plugin %q skipped — sandbox task ID %q collides with %q",
					manifest.Name, taskID, owner)
				continue
			}
			sanitizedTaskIDs[taskID] = manifest.Name
			if isUserDir {
				manifest.IsUserPlugin = true
				if err := ValidateUserHandler(manifest); err != nil {
					log.Printf("WARNING: user plugin %q: %v — skipped", manifest.Name, err)
					continue
				}
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

	// Warn about enabled/disabled entries that don't match any discovered plugin.
	if len(m.cfg.Plugins.Enabled) > 0 {
		if len(m.cfg.Plugins.Disabled) > 0 {
			log.Printf("WARNING: both plugins.enabled and plugins.disabled are set; using allowlist (plugins.enabled)")
		}
		for _, name := range m.cfg.Plugins.Enabled {
			if !seenNames[name] {
				log.Printf("WARNING: config enables plugin %q but no such plugin was found", name)
			}
		}
	} else {
		for _, name := range m.cfg.Plugins.Disabled {
			if !seenNames[name] {
				log.Printf("WARNING: config disables plugin %q but no such plugin was found", name)
			}
		}
	}

	return nil
}

// LoadAll prepares all discovered plugins synchronously: resolves
// dependencies, loads auth providers, filters disabled plugins, and
// prepares handles (config, proxy, TLS). This is the fast phase.
//
// After LoadAll returns, m.disabled is fully populated and all
// handles are prepared. Call StartPending to launch the plugin
// processes in the background (the slow phase).
func (m *Manager) LoadAll(ctx context.Context) error {
	sorted, skipped, err := m.topologicalSort()
	if err != nil {
		return fmt.Errorf("dependency resolution: %w", err)
	}

	// Disable plugins that were skipped due to dependency issues.
	for name, reason := range skipped {
		m.disablePlugin(name, reason)
	}

	// Pass 1: load auth-providing plugins fully (fast, typically 0-1).
	for _, name := range sorted {
		manifest := m.manifests[name]
		if manifest.ProvidesAuth() {
			if err := m.Load(ctx, name); err != nil {
				log.Printf("failed to load auth provider %s: %v", name, err)
			}
		}
	}

	// Pass 2: filter disabled plugins, prepare remaining.
	// After this loop, m.disabled is fully populated.
	var pass2 []string
	for _, name := range sorted {
		manifest := m.manifests[name]
		if manifest.ProvidesAuth() {
			continue
		}

		if manifest.CredentialGroup != "" {
			if m.envDirError != "" {
				m.disablePlugin(name, "[env.d directory] "+m.envDirError)
				continue
			}
			if errMsg, ok := m.envErrors[manifest.CredentialGroup]; ok {
				m.disablePlugin(name, errMsg)
				continue
			}
		}

		if reason := m.checkDisabledProvider(manifest); reason != "" {
			m.disablePlugin(name, reason)
			continue
		}

		pass2 = append(pass2, name)
	}

	// Prepare handles for all pass 2 plugins (config, proxy, TLS).
	// This is fast — no subprocess spawning. Store dependency levels
	// with prepared plugin names for StartPending.
	levels := m.dependencyLevels(pass2)
	for i, level := range levels {
		var prepared []string
		for _, name := range level {
			handle, err := m.preparePlugin(ctx, name)
			if err != nil {
				m.disablePlugin(name, err.Error())
				continue
			}

			// Store the handle so startLevel can find it.
			m.handlesMu.Lock()
			m.handles[name] = handle
			m.handlesMu.Unlock()

			// Non-persistent plugins are already fully loaded.
			if handle.manifest.Execution != "persistent" {
				m.registerPluginDirs(name, handle)
				log.Printf("loaded plugin %s (v%s, %s)", name, handle.manifest.Version, handle.manifest.Execution)
				continue
			}

			prepared = append(prepared, name)
		}
		levels[i] = prepared
	}
	m.pending = levels

	return nil
}

// StartPending starts all prepared plugin processes by dependency
// level. This is the slow phase — each plugin spawns a subprocess
// and blocks on the init handshake (up to InitTimeout per plugin).
// Independent plugins within a level start in parallel.
//
// Closes loadDone when complete. Safe to call from a goroutine.
func (m *Manager) StartPending(ctx context.Context) {
	defer close(m.loadDone)

	for _, level := range m.pending {
		if len(level) == 0 {
			continue
		}
		m.startLevel(ctx, level)
	}
	m.pending = nil
}

// IsLoading returns true if StartPending is still running.
func (m *Manager) IsLoading() bool {
	select {
	case <-m.loadDone:
		return false
	default:
		return true
	}
}

// WaitLoaded blocks until StartPending completes.
func (m *Manager) WaitLoaded() {
	<-m.loadDone
}

// startResult holds the outcome of a parallel plugin start.
type startResult struct {
	name     string
	handle   *Handle
	err      error
	duration time.Duration
}

// startLevel starts all pre-prepared plugins in a dependency level.
// Plugins are started in parallel using goroutines. Each goroutine
// writes its result to a pre-allocated slice at its own index.
// After all goroutines complete, results are collected sequentially.
func (m *Manager) startLevel(ctx context.Context, names []string) {
	// Start all plugins in parallel.
	results := make([]startResult, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		m.handlesMu.RLock()
		handle := m.handles[name]
		m.handlesMu.RUnlock()
		if handle == nil {
			results[i] = startResult{name: name, err: fmt.Errorf("handle not found")}
			continue
		}

		wg.Add(1)
		go func(idx int, n string, h *Handle) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					results[idx] = startResult{name: n, err: fmt.Errorf("panic: %v", r)}
				}
			}()
			start := time.Now()
			err := h.Start(ctx)
			results[idx] = startResult{name: n, handle: h, err: err, duration: time.Since(start)}
		}(i, name, handle)
	}

	wg.Wait()

	// Collect results sequentially (map writes, proxy cleanup).
	// Failed plugins are added to m.disabled so their tools appear
	// as [DISABLED] stubs after SwapStartFailedTools runs.
	m.handlesMu.Lock()
	for _, r := range results {
		if r.err != nil {
			log.Printf("failed to start plugin %s (%s): %v", r.name, r.duration.Round(time.Millisecond), r.err)
			m.proxy.UnregisterPlugin(r.name)
			delete(m.handles, r.name)
			m.disabled[r.name] = DisabledPlugin{
				Name:     r.name,
				Reason:   m.sanitizeReason(r.err.Error()),
				Manifest: m.manifests[r.name],
			}
			continue
		}
		log.Printf("loaded plugin %s (v%s, %s, %s)", r.name,
			r.handle.manifest.Version, r.handle.manifest.Execution, r.duration.Round(time.Millisecond))
		m.registerPluginDirs(r.name, r.handle)
		m.registerDynamicDomains(r.name, r.handle)
		m.registerAuthBindings(r.name, r.handle)
	}
	m.handlesMu.Unlock()
}

// Load starts a single plugin by name.
func (m *Manager) Load(ctx context.Context, name string) error {
	handle, err := m.preparePlugin(ctx, name)
	if err != nil {
		return err
	}

	start := time.Now()
	if handle.manifest.Execution == "persistent" {
		if err := handle.Start(ctx); err != nil {
			m.proxy.UnregisterPlugin(name)
			return err
		}
	}

	// Register file I/O directories after Start() so PrepareDirs has
	// created them and EvalSymlinks can resolve through symlinks.
	m.registerPluginDirs(name, handle)

	m.registerDynamicDomains(name, handle)
	m.registerAuthBindings(name, handle)

	m.handlesMu.Lock()
	m.handles[name] = handle
	m.handlesMu.Unlock()
	log.Printf("loaded plugin %s (v%s, %s, %s)", name,
		handle.manifest.Version, handle.manifest.Execution, time.Since(start).Round(time.Millisecond))
	return nil
}

const maxDynamicDomains = 10

// registerPluginDirs resolves and registers file I/O directories for
// a plugin. Called after Start() so PrepareDirs has created the dirs
// and EvalSymlinks can resolve through any symlinks (e.g., container
// runtimes that symlink /tmp).
func (m *Manager) registerPluginDirs(name string, handle *Handle) {
	if handle.outputDir == "" {
		return
	}
	resolvedOutput, err := filepath.EvalSymlinks(handle.outputDir)
	if err != nil {
		resolvedOutput = handle.outputDir
	}
	var resolvedTmp string
	if m.sbMgr != nil && m.sbMgr.Enabled() {
		resolvedTmp, err = filepath.EvalSymlinks(m.sbMgr.TmpDir(name))
		if err != nil {
			resolvedTmp = m.sbMgr.TmpDir(name)
		}
	}
	m.svcHandler.registerPluginDirs(name, resolvedOutput, resolvedTmp)
}

// registerDynamicDomains validates and registers domains from the
// plugin's init_ok response with the proxy allowlist.
func (m *Manager) registerDynamicDomains(name string, handle *Handle) {
	domains := handle.InitDomains()
	if len(domains) == 0 {
		return
	}

	if len(domains) > maxDynamicDomains {
		log.Printf("[%s] WARNING: init_ok declared %d domains, capped at %d",
			name, len(domains), maxDynamicDomains)
		domains = domains[:maxDynamicDomains]
	}

	var valid []string
	for _, d := range domains {
		if err := validateDomain(d); err != nil {
			log.Printf("[%s] rejected dynamic domain %q: %v", name, d, err)
			continue
		}
		valid = append(valid, d)
	}

	if len(valid) > 0 {
		m.proxy.AddBaseDomains(name, valid)
		log.Printf("[%s] registered dynamic base domains: %v", name, valid)
	}
}

// registerAuthBindings resolves per-domain auth bindings from the
// plugin's init_ok response and installs a DomainProvider on the
// proxy. Must be called after registerDynamicDomains (ordering
// invariant: domains are allowed before auth is installed).
func (m *Manager) registerAuthBindings(name string, handle *Handle) {
	bindings := handle.AuthBindings()
	if len(bindings) == 0 {
		return
	}

	if len(bindings) > maxDynamicDomains {
		log.Printf("[%s] WARNING: init_ok declared %d auth bindings, capped at %d",
			name, len(bindings), maxDynamicDomains)
		capped := make(map[string]string, maxDynamicDomains)
		for domain, envVar := range bindings {
			capped[domain] = envVar
			if len(capped) >= maxDynamicDomains {
				break
			}
		}
		bindings = capped
	}

	manifest := handle.manifest
	authCfg := manifest.Services.Auth
	if authCfg.Type == "" {
		log.Printf("[%s] WARNING: auth_bindings present but no services.auth configured; ignoring", name)
		return
	}

	vars := m.pluginVars(manifest)
	resolve := func(s string) string { return config.ResolveVars(s, vars) }

	providers := make(map[string]auth.Provider, len(bindings))
	for domain, envVar := range bindings {
		if err := validateDomain(domain); err != nil {
			log.Printf("[%s] rejected auth binding domain %q: %v", name, domain, err)
			continue
		}

		token := resolve("${" + envVar + "}")
		if token == "" {
			log.Printf("[%s] WARNING: auth binding %q → $%s resolved to empty; skipping", name, domain, envVar)
			continue
		}

		p, err := auth.NewBearerProvider(token, authCfg.Header, authCfg.Prefix)
		if err != nil {
			log.Printf("[%s] WARNING: auth binding %q: %v", name, domain, err)
			continue
		}
		providers[domain] = p
	}

	if len(providers) == 0 {
		log.Printf("[%s] WARNING: no valid auth bindings resolved", name)
		return
	}

	dp, err := auth.NewDomainProvider(providers)
	if err != nil {
		log.Printf("[%s] WARNING: create domain provider: %v", name, err)
		return
	}

	m.proxy.SetPluginProvider(name, dp)
	log.Printf("[%s] installed per-domain auth for %d domain(s)", name, len(providers))
}

// preparePlugin resolves config, registers with the proxy, and creates
// a Handle for the plugin — but does NOT start the process. This is the
// fast, sequential phase of plugin loading. The caller is responsible
// for calling handle.Start() and storing the handle in m.handles.
//
// IMPORTANT: This method writes to m.proxy (RegisterPlugin) and reads
// from m.manifests/m.envGroups — it must be called sequentially, never
// from concurrent goroutines.
func (m *Manager) preparePlugin(ctx context.Context, name string) (*Handle, error) {
	manifest, ok := m.manifests[name]
	if !ok {
		return nil, fmt.Errorf("unknown plugin: %s", name)
	}

	// Resolve config
	resolvedCfg := m.resolveConfig(name, manifest)
	cfgJSON, err := json.Marshal(resolvedCfg)
	if err != nil {
		return nil, fmt.Errorf("marshal config for %s: %w", name, err)
	}
	m.handlesMu.Lock()
	manifest.SetResolvedConfig(cfgJSON)
	m.handlesMu.Unlock()

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
		if err := config.RejectSymlink(tlsCfg.CACert); err != nil {
			return nil, fmt.Errorf("[%s] ca_cert: %w", name, err)
		}
		pem, err := os.ReadFile(tlsCfg.CACert) //nolint:gosec // path from env.d (permission-checked)
		if err != nil {
			return nil, fmt.Errorf("[%s] read ca_cert %s: %w", name, tlsCfg.CACert, err)
		}
		tlsCfg.CACertPEM = pem
		log.Printf("[%s] loaded CA cert from %s", name, tlsCfg.CACert)
	}

	// Validate and optionally decrypt TLS client cert/key
	if tlsCfg.ClientCert != "" {
		if err := config.RejectSymlink(tlsCfg.ClientCert); err != nil {
			return nil, fmt.Errorf("[%s] client_cert: %w", name, err)
		}
		decrypted, err := m.decryptCredentialIfVault(name, tlsCfg.ClientCert)
		if err != nil {
			return nil, fmt.Errorf("[%s] client_cert: %w", name, err)
		}
		tlsCfg.ClientCert = decrypted
	}
	if tlsCfg.ClientKey != "" {
		if err := config.RejectSymlink(tlsCfg.ClientKey); err != nil {
			return nil, fmt.Errorf("[%s] client_key: %w", name, err)
		}
		info, err := os.Lstat(tlsCfg.ClientKey)
		if err != nil {
			return nil, fmt.Errorf("[%s] stat client_key %s: %w", name, tlsCfg.ClientKey, err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("[%s] client_key %s mode %04o, must not be group/other accessible",
				name, tlsCfg.ClientKey, info.Mode().Perm())
		}
		decrypted, err := m.decryptCredentialIfVault(name, tlsCfg.ClientKey)
		if err != nil {
			return nil, fmt.Errorf("[%s] client_key: %w", name, err)
		}
		tlsCfg.ClientKey = decrypted
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
		proactive := manifest.Services.Auth.SPNEGOProactive == nil || *manifest.Services.Auth.SPNEGOProactive
		client, err := proxy.NewKerberosClient(spn, proactive, pa.AllowPrivateIPs, pa.TLS, m.cfg.HTTP.Timeout)
		if err != nil {
			return nil, fmt.Errorf("[%s] create kerberos client: %w", name, err)
		}
		pa.Client = client
		pa.IsKerberos = true
		tlsTransport, err := proxy.SafeTransportWithTLS(pa.AllowPrivateIPs, pa.TLS)
		if err != nil {
			return nil, fmt.Errorf("[%s] create TLS transport for noAuth: %w", name, err)
		}
		pa.TLSClient = &http.Client{
			Transport:     tlsTransport,
			Timeout:       m.cfg.HTTP.Timeout,
			Jar:           client.Jar,
			CheckRedirect: proxy.StripAuthOnCrossDomainRedirect,
		}
		log.Printf("[%s] using kerberos client (spn=%q, proactive=%v)", name, spn, proactive)
	case pa.TLS.HasConfig():
		pa.Provider = m.resolveAuth(name, manifest)
		if kp, ok := pa.Provider.(*auth.KerberosProvider); ok {
			spn := kp.SPN()
			proactive := manifest.Services.Auth.SPNEGOProactive == nil || *manifest.Services.Auth.SPNEGOProactive
			client, err := proxy.NewKerberosClient(spn, proactive, pa.AllowPrivateIPs, pa.TLS, m.cfg.HTTP.Timeout)
			if err != nil {
				return nil, fmt.Errorf("[%s] create kerberos client: %w", name, err)
			}
			pa.Client = client
			pa.IsKerberos = true
			transport, err := proxy.SafeTransportWithTLS(pa.AllowPrivateIPs, pa.TLS)
			if err != nil {
				return nil, fmt.Errorf("[%s] create TLS transport for noAuth: %w", name, err)
			}
			pa.TLSClient = &http.Client{
				Transport:     transport,
				Timeout:       m.cfg.HTTP.Timeout,
				Jar:           client.Jar,
				CheckRedirect: proxy.StripAuthOnCrossDomainRedirect,
			}
			log.Printf("[%s] variant resolved to kerberos with TLS, using kerberos client (spn=%q, proactive=%v)", name, spn, proactive)
		} else {
			transport, err := proxy.SafeTransportWithTLS(pa.AllowPrivateIPs, pa.TLS)
			if err != nil {
				return nil, fmt.Errorf("[%s] create TLS transport: %w", name, err)
			}
			pa.TLSTransport = transport
			pa.TLSClient = &http.Client{
				Transport:     transport,
				Timeout:       m.cfg.HTTP.Timeout,
				CheckRedirect: proxy.StripAuthOnCrossDomainRedirect,
			}
			log.Printf("[%s] using TLS transport (ca=%v, mtls=%v, skip_hostname=%v)",
				name, pa.TLS.CACert != "", pa.TLS.ClientCert != "", pa.TLS.SkipHostnameVerify)
		}
	default:
		pa.Provider = m.resolveAuth(name, manifest)
		if kp, ok := pa.Provider.(*auth.KerberosProvider); ok {
			spn := kp.SPN()
			proactive := manifest.Services.Auth.SPNEGOProactive == nil || *manifest.Services.Auth.SPNEGOProactive
			client, err := proxy.NewKerberosClient(spn, proactive, pa.AllowPrivateIPs, pa.TLS, m.cfg.HTTP.Timeout)
			if err != nil {
				return nil, fmt.Errorf("[%s] create kerberos client: %w", name, err)
			}
			pa.Client = client
			pa.IsKerberos = true
			tlsTransport, err := proxy.SafeTransportWithTLS(pa.AllowPrivateIPs, pa.TLS)
			if err != nil {
				return nil, fmt.Errorf("[%s] create TLS transport for noAuth: %w", name, err)
			}
			pa.TLSClient = &http.Client{
				Transport:     tlsTransport,
				Timeout:       m.cfg.HTTP.Timeout,
				Jar:           client.Jar,
				CheckRedirect: proxy.StripAuthOnCrossDomainRedirect,
			}
			log.Printf("[%s] variant resolved to kerberos, using kerberos client (spn=%q, proactive=%v)", name, spn, proactive)
		}
	}

	// IMPORTANT: RegisterPlugin is a plain map write — it must be called
	// sequentially, never from concurrent goroutines.
	m.proxy.RegisterPlugin(name, pa)

	if samlInit := manifest.Services.Auth.SAMLInit; samlInit != "" && pa.IsKerberos && pa.Client != nil {
		resolved := config.ResolveVars(samlInit, vars)
		if err := proxy.InitSAMLSession(ctx, pa.Client, resolved, pa.BaseURL, pa.AllowedDomains); err != nil {
			return nil, fmt.Errorf("[%s] SAML init login failed: %w", name, err)
		}
		log.Printf("[%s] SAML init login completed", name)
	}

	processCfg := ProcessConfig{
		InitTimeout:       m.cfg.Plugins.InitTimeout,
		ShutdownTimeout:   m.cfg.Plugins.ShutdownTimeout,
		ShutdownKillAfter: m.cfg.Plugins.ShutdownKillAfter,
		MaxMessageSize:    int(m.cfg.Plugins.MaxMessageSize),
	}

	handle := NewHandle(manifest, m.svcHandler, processCfg, m.cfg.Plugins.ToolCallTimeout, vars)
	handle.resolvedConfig = cfgJSON
	handle.sessionDir = m.sessionDir
	if m.sessionDir != "" {
		handle.outputDir = filepath.Join(m.sessionDir, dataDir, name)
	}
	if m.sbMgr != nil {
		handle.SetSandbox(m.sbMgr)
	}

	if handle.outputDir != "" {
		fileio.CleanupStaleTempFiles(handle.outputDir)
	}

	return handle, nil
}

// Unload stops a plugin. Removes the handle from the map before
// stopping the process so new tool calls are not dispatched to a
// stopping plugin.
func (m *Manager) Unload(ctx context.Context, name string) error {
	m.handlesMu.Lock()
	handle, ok := m.handles[name]
	if !ok {
		m.handlesMu.Unlock()
		return fmt.Errorf("plugin not loaded: %s", name)
	}
	delete(m.handles, name)
	m.handlesMu.Unlock()

	if err := handle.Stop(ctx); err != nil {
		return err
	}
	m.svcHandler.unregisterPluginDirs(name)
	log.Printf("unloaded plugin %s", name)
	return nil
}

// Reload stops and restarts a plugin. Re-reads the env.d file
// to pick up any changes (e.g., new IPA_CA_CERT added by
// create_config). If the plugin was disabled due to an env.d
// error, enables it if the issue is resolved.
func (m *Manager) Reload(ctx context.Context, name string) error {
	// Wait for initial loading to complete before reloading.
	m.WaitLoaded()

	// Serialize reloads: the control watcher and MCP dispatch can
	// both trigger Reload concurrently. The reloadMu serialization
	// makes the handlesMu lock gaps between helpers safe — no
	// concurrent reload can modify state between reads and writes.
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()

	group, needEnvReread, envDirErr := m.reloadReadState(name)

	if needEnvReread {
		if envDirErr != "" {
			dirInfo, err := os.Stat(m.envDir)
			if err != nil {
				return fmt.Errorf("env.d directory still has issues: %w", err)
			}
			if err := config.CheckPermissions(m.envDir, dirInfo); err != nil {
				return fmt.Errorf("env.d directory still has issues: %w", err)
			}
			m.reloadClearEnvDirError(name)
		}

		vars, err := config.LoadSingleEnvGroup(m.envDir, group, m.envLoadOpts)
		if err := m.reloadUpdateEnv(name, group, vars, err); err != nil {
			return err
		}
	}

	if m.reloadIsLoaded(name) {
		if err := m.Unload(ctx, name); err != nil {
			return err
		}
	}
	m.secureTracker.ClosePlugin(name)
	if err := m.Load(ctx, name); err != nil {
		m.disablePlugin(name, err.Error())
		return err
	}
	return nil
}

// reloadReadState reads the credential group and env state for a
// plugin under handlesMu. Returns the group name, whether an env
// re-read is needed, and any env directory error.
func (m *Manager) reloadReadState(name string) (group string, needReread bool, envDirErr string) {
	m.handlesMu.Lock()
	defer m.handlesMu.Unlock()

	if dp, ok := m.disabled[name]; ok {
		group = dp.Manifest.CredentialGroup
	} else if manifest, ok := m.manifests[name]; ok {
		group = manifest.CredentialGroup
	}
	needReread = group != "" && m.envDir != ""
	envDirErr = m.envDirError
	return
}

// reloadClearEnvDirError clears the env directory error under handlesMu.
func (m *Manager) reloadClearEnvDirError(name string) {
	m.handlesMu.Lock()
	defer m.handlesMu.Unlock()
	m.envDirError = ""
	log.Printf("[%s] env.d directory permissions fixed", name)
}

// reloadUpdateEnv updates the env group state after a re-read attempt.
// If loadErr is non-nil and the plugin was disabled, returns the error.
// Otherwise logs a warning and continues.
func (m *Manager) reloadUpdateEnv(name, group string, vars map[string]string, loadErr error) error {
	m.handlesMu.Lock()
	defer m.handlesMu.Unlock()

	if loadErr != nil {
		if _, wasDisabled := m.disabled[name]; wasDisabled {
			return fmt.Errorf("env group %s still has issues: %w", group, loadErr)
		}
		log.Printf("[%s] warning: env group %s re-read failed: %v", name, group, loadErr)
		return nil
	}
	m.envGroups[group] = vars
	delete(m.envErrors, group)
	delete(m.disabled, name)
	log.Printf("[%s] env group %s re-read (%d vars)", name, group, len(vars))
	return nil
}

// reloadIsLoaded checks whether the plugin has an active handle.
func (m *Manager) reloadIsLoaded(name string) bool {
	m.handlesMu.RLock()
	defer m.handlesMu.RUnlock()
	_, loaded := m.handles[name]
	return loaded
}

// ShutdownAll stops all loaded plugins.
func (m *Manager) ShutdownAll(ctx context.Context) {
	m.handlesMu.RLock()
	snapshot := make(map[string]*Handle, len(m.handles))
	for name, handle := range m.handles {
		snapshot[name] = handle
	}
	m.handlesMu.RUnlock()

	for name, handle := range snapshot {
		if err := handle.Stop(ctx); err != nil {
			log.Printf("error stopping %s: %v", name, err)
		}
	}
	m.secureTracker.CloseAll()
}

// CallTool dispatches a tool call to the appropriate plugin.
func (m *Manager) CallTool(_ context.Context, toolName string) (string, *Handle) {
	m.handlesMu.RLock()
	defer m.handlesMu.RUnlock()
	for name, manifest := range m.manifests {
		for _, tool := range manifest.Tools {
			if tool.Name == toolName {
				handle := m.handles[name]
				if handle == nil || !handle.IsReady() {
					return name, nil
				}
				return name, handle
			}
		}
	}
	return "", nil
}

// Manifests returns a snapshot of all discovered manifests.
func (m *Manager) Manifests() map[string]*Manifest {
	m.handlesMu.RLock()
	defer m.handlesMu.RUnlock()
	snapshot := make(map[string]*Manifest, len(m.manifests))
	for k, v := range m.manifests {
		snapshot[k] = v.Clone()
	}
	return snapshot
}

// DisabledPlugins returns a snapshot of plugins that were discovered
// but could not be loaded (e.g., bad env.d permissions, TLS/cert
// errors, missing dependencies, process start failures). Thread-safe.
func (m *Manager) DisabledPlugins() map[string]DisabledPlugin {
	m.handlesMu.RLock()
	defer m.handlesMu.RUnlock()
	snapshot := make(map[string]DisabledPlugin, len(m.disabled))
	for k, v := range m.disabled {
		dp := v
		dp.Manifest = v.Manifest.Clone()
		snapshot[k] = dp
	}
	return snapshot
}

// disablePlugin marks a plugin as disabled with an actionable reason.
// This is the single entry point for all failure paths — env.d errors,
// prepare failures, start failures, dependency issues. The plugin's
// tools are registered as [DISABLED] stubs so the LLM can inform the
// user and suggest fixes.
//
// Caller must NOT hold handlesMu.
func (m *Manager) disablePlugin(name, reason string) {
	m.handlesMu.Lock()
	manifest := m.manifests[name]
	m.disabled[name] = DisabledPlugin{
		Name:     name,
		Reason:   m.sanitizeReason(reason),
		Manifest: manifest,
	}
	delete(m.handles, name)
	m.svcHandler.unregisterPluginDirs(name)
	m.handlesMu.Unlock()

	if m.proxy != nil {
		m.proxy.UnregisterPlugin(name)
	}
	log.Printf("plugin %s disabled: %s", name, reason)
}

// sanitizeReason strips the workdir and envDir prefixes from paths
// in error messages to avoid leaking the full filesystem layout
// (including username) in [DISABLED] stubs visible to the LLM.
func (m *Manager) sanitizeReason(reason string) string {
	if m.workdir != "" {
		reason = strings.ReplaceAll(reason, m.workdir+"/", "")
	}
	if m.envDir != "" && !strings.HasPrefix(m.envDir, m.workdir+"/") {
		reason = strings.ReplaceAll(reason, m.envDir, "env.d")
	}
	if m.cfg != nil && m.cfg.Secrets.VaultPasswordFile != "" {
		reason = strings.ReplaceAll(reason, m.cfg.Secrets.VaultPasswordFile, "<vault-password-file>")
	}
	if m.cfg == nil {
		return reason
	}
	if m.cfg.CredentialsDir != "" {
		reason = strings.ReplaceAll(reason, m.cfg.CredentialsDir+"/", "credentials/")
	}
	for _, path := range m.cfg.Secrets.VaultIDs {
		if path != "" {
			reason = strings.ReplaceAll(reason, path, "<vault-password-file>")
		}
	}
	return reason
}

// decryptCredentialIfVault reads a credential file and, if it is
// Ansible Vault encrypted, decrypts it to a securefile. Returns
// the original path for plaintext files, or the securefile path
// for encrypted files.
func (m *Manager) decryptCredentialIfVault(pluginName, path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > 1<<20 {
		return "", fmt.Errorf("credential file too large: %d bytes (max 1MB)", info.Size())
	}
	data, err := os.ReadFile(path) //nolint:gosec // path from resolved plugin config
	if err != nil {
		return "", err
	}
	if !vault.IsAnsibleVault(data) {
		return path, nil
	}
	if m.vaultPassword == nil {
		return "", fmt.Errorf("encrypted credential file but no vault password configured")
	}

	header, err := vault.ParseHeader(strings.SplitN(string(data), "\n", 2)[0])
	if err != nil {
		return "", fmt.Errorf("invalid vault file format")
	}

	password, err := m.vaultPassword(header.VaultID)
	if err != nil {
		return "", err
	}

	plaintext, err := vault.Decrypt(data, password)
	vault.ZeroBytes(password)
	if err != nil {
		return "", err
	}

	sf, err := securefile.CreateCloexec(filepath.Base(path))
	if err != nil {
		vault.ZeroBytes(plaintext)
		return "", fmt.Errorf("create securefile: %w", err)
	}
	if err := sf.Write(plaintext); err != nil {
		vault.ZeroBytes(plaintext)
		_ = sf.Close()
		return "", fmt.Errorf("write securefile: %w", err)
	}
	vault.ZeroBytes(plaintext)
	m.secureTracker.TrackForPlugin(pluginName, sf)
	return sf.Path(), nil
}

// ConfigDisabledPlugins returns a snapshot of plugins that were
// skipped during discovery because they are listed in
// plugins.disabled config.
func (m *Manager) ConfigDisabledPlugins() map[string]*Manifest {
	snapshot := make(map[string]*Manifest, len(m.configDisabled))
	for k, v := range m.configDisabled {
		snapshot[k] = v.Clone()
	}
	return snapshot
}

// LoadedPlugins returns the names of successfully loaded plugins.
func (m *Manager) LoadedPlugins() []string {
	m.handlesMu.RLock()
	defer m.handlesMu.RUnlock()
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

func (m *Manager) resolveConfig(name string, manifest *Manifest) map[string]string {
	resolved := config.ResolveVarsMap(manifest.Config, m.pluginVars(manifest))
	if m.cfg.CredentialsDir != "" && manifest.CredentialGroup != "" {
		credDir := filepath.Join(m.cfg.CredentialsDir, manifest.CredentialGroup)
		if hasVaultFiles(credDir) {
			shadowDir, err := m.buildShadowCredentialsDir(name, credDir)
			if err != nil {
				log.Printf("[%s] shadow dir failed, using original: %v", name, err)
				resolved["_credentials_dir"] = credDir
			} else {
				resolved["_credentials_dir"] = shadowDir
			}
		} else {
			resolved["_credentials_dir"] = credDir
		}
	}
	if m.workdir != "" {
		resolved["_work_dir"] = m.workdir
	}
	if m.sessionDir != "" {
		resolved["_session_dir"] = m.sessionDir
		resolved["_output_dir"] = filepath.Join(m.sessionDir, dataDir, name)
	}
	if name == "testing-farm" {
		if keys := discoverSSHPublicKeys(); keys != "" {
			resolved["_ssh_public_keys"] = keys
		}
	}
	return resolved
}

// discoverSSHPublicKeys reads SSH public keys from ~/.ssh/id_*.pub.
// Runs in the unsandboxed core process so plugins don't need
// filesystem access to ~/.ssh/.
func discoverSSHPublicKeys() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	pattern := filepath.Join(home, ".ssh", "id_*.pub")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return ""
	}

	const maxSSHKeys = 20

	var keys []string
	for _, m := range matches {
		// Skip symlinks — a symlink named id_evil.pub could point
		// to an arbitrary file, exfiltrating its contents to
		// Testing Farm. Note: Lstat-then-ReadFile TOCTOU gap exists
		// but is acceptable (same as source_other.go).
		info, err := os.Lstat(m)
		if err != nil || info.Mode().Type()&os.ModeSymlink != 0 {
			continue
		}
		if info.Size() > 4096 {
			continue
		}
		data, err := os.ReadFile(m) //nolint:gosec // reading public keys only, validated above
		if err != nil {
			continue
		}
		k := strings.TrimSpace(string(data))
		if k == "" {
			continue
		}
		if !hasSSHKeyPrefix(k) {
			continue
		}
		keys = append(keys, k)
		if len(keys) >= maxSSHKeys {
			log.Printf("WARNING: SSH key discovery capped at %d keys", maxSSHKeys)
			break
		}
	}
	if len(matches) > 0 && len(keys) == 0 {
		log.Printf("WARNING: found %d SSH .pub files but none passed validation", len(matches))
	}
	return strings.Join(keys, "\n")
}

func hasSSHKeyPrefix(s string) bool {
	for _, prefix := range []string{
		"ssh-rsa ", "ssh-ed25519 ", "ecdsa-sha2-", "ssh-dss ",
		"sk-ssh-ed25519 ", "sk-ecdsa-sha2-",
	} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// hasVaultFiles reports whether any files in a directory start with
// the Ansible Vault magic header. Only reads the first 15 bytes of
// each file to avoid loading full credential file contents.
func hasVaultFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		f, err := os.Open(path) //nolint:gosec // credential dir from config
		if err != nil {
			continue
		}
		header := make([]byte, 15)
		n, _ := f.Read(header)
		_ = f.Close()
		if vault.IsAnsibleVault(header[:n]) {
			return true
		}
	}
	return false
}

// buildShadowCredentialsDir creates a per-plugin shadow directory
// with symlinks to credential files. Encrypted files are decrypted
// to inheritable securefiles; plaintext files are symlinked to the
// originals. Token file writes follow the symlink back to the
// original file.
func (m *Manager) buildShadowCredentialsDir(pluginName, originalDir string) (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		var err error
		base, err = os.MkdirTemp(os.TempDir(), "wtmcp-")
		if err != nil {
			return "", fmt.Errorf("create shadow base: %w", err)
		}
		m.secureTracker.TrackDirForPlugin(pluginName+"-base", base)
	}

	shadowDir := filepath.Join(base, "wtmcp-shadow", pluginName)
	_ = os.RemoveAll(shadowDir)                           //nolint:gosec // shadowDir under XDG_RUNTIME_DIR
	if err := os.MkdirAll(shadowDir, 0o700); err != nil { //nolint:gosec
		return "", fmt.Errorf("create shadow dir: %w", err)
	}
	m.secureTracker.TrackDirForPlugin(pluginName, shadowDir)

	entries, err := os.ReadDir(originalDir)
	if err != nil {
		return "", fmt.Errorf("read credentials dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		originalPath := filepath.Join(originalDir, entry.Name())
		shadowPath := filepath.Join(shadowDir, entry.Name())

		fi, err := entry.Info()
		if err != nil || fi.Size() > 1<<20 {
			continue
		}
		data, err := os.ReadFile(originalPath) //nolint:gosec // credential dir from config
		if err != nil {
			continue
		}

		if vault.IsAnsibleVault(data) {
			sfPath, err := m.decryptCredentialToInheritable(pluginName, entry.Name(), data)
			if err != nil {
				log.Printf("[%s] shadow decrypt %s: %v", pluginName, entry.Name(), err)
				continue
			}
			if err := os.Symlink(sfPath, shadowPath); err != nil {
				return "", fmt.Errorf("symlink %s: %w", entry.Name(), err)
			}
		} else {
			if err := os.Symlink(originalPath, shadowPath); err != nil {
				return "", fmt.Errorf("symlink %s: %w", entry.Name(), err)
			}
		}
	}

	return shadowDir, nil
}

// decryptCredentialToInheritable decrypts vault data to an
// inheritable securefile (no MFD_CLOEXEC) so plugin subprocesses
// can read via /proc/self/fd/N.
func (m *Manager) decryptCredentialToInheritable(pluginName, fileName string, data []byte) (string, error) {
	if m.vaultPassword == nil {
		return "", fmt.Errorf("encrypted credential file but no vault password configured")
	}

	header, err := vault.ParseHeader(strings.SplitN(string(data), "\n", 2)[0])
	if err != nil {
		return "", fmt.Errorf("invalid vault file format")
	}

	password, err := m.vaultPassword(header.VaultID)
	if err != nil {
		return "", err
	}

	plaintext, err := vault.Decrypt(data, password)
	vault.ZeroBytes(password)
	if err != nil {
		return "", err
	}

	sf, err := securefile.Create(fileName)
	if err != nil {
		vault.ZeroBytes(plaintext)
		return "", fmt.Errorf("create securefile: %w", err)
	}
	if err := sf.Write(plaintext); err != nil {
		vault.ZeroBytes(plaintext)
		_ = sf.Close()
		return "", fmt.Errorf("write securefile: %w", err)
	}
	vault.ZeroBytes(plaintext)
	m.secureTracker.TrackForPlugin(pluginName, sf)
	return sf.Path(), nil
}

// Handle returns the handle for a loaded plugin, or nil if not loaded.
func (m *Manager) Handle(name string) *Handle {
	m.handlesMu.RLock()
	defer m.handlesMu.RUnlock()
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

// isProviderDisabled checks whether a provider type is in the disabled list,
// normalizing aliases (e.g., "kerberos" → "kerberos/spnego").
func (m *Manager) isProviderDisabled(typeName string) bool {
	return slices.Contains(m.cfg.Providers.Disabled, auth.NormalizeProviderType(typeName))
}

// checkDisabledProvider returns a reason string if the plugin's auth
// depends entirely on disabled providers. Returns "" if the plugin
// can still operate.
func (m *Manager) checkDisabledProvider(manifest *Manifest) string {
	authCfg := manifest.Services.Auth

	// No auth configured — not affected
	if authCfg.Type == "" && len(authCfg.Variants) == 0 {
		return ""
	}

	// Single-type plugin (no variants)
	if len(authCfg.Variants) == 0 {
		normalized := auth.NormalizeProviderType(authCfg.Type)
		if m.isProviderDisabled(normalized) {
			return fmt.Sprintf("auth provider %q is disabled", normalized)
		}
		return ""
	}

	// Variant-based plugin: check if the explicit selection uses a disabled provider
	vars := m.pluginVars(manifest)
	sel := config.ResolveVars(authCfg.Select, vars)

	if sel != "auto" && sel != "" {
		if v, ok := authCfg.Variants[sel]; ok {
			normalized := auth.NormalizeProviderType(v.Type)
			if m.isProviderDisabled(normalized) {
				return fmt.Sprintf("auth variant %q uses disabled provider %q", sel, normalized)
			}
		}
		return ""
	}

	// Auto-select: check if ALL variants use disabled providers
	for _, name := range authCfg.VariantOrder {
		v := authCfg.Variants[name]
		if !m.isProviderDisabled(auth.NormalizeProviderType(v.Type)) {
			return "" // at least one variant is still viable
		}
	}
	return "all auth variants use disabled providers"
}

func (m *Manager) resolveAuth(pluginName string, manifest *Manifest) auth.Provider {
	authCfg := manifest.Services.Auth
	if authCfg.Type == "" && len(authCfg.Variants) == 0 {
		return nil
	}

	credDir := m.cfg.CredentialsDir
	if credDir != "" && manifest.CredentialGroup != "" {
		credDir = filepath.Join(credDir, manifest.CredentialGroup)
	}

	vars := m.pluginVars(manifest)
	resolve := func(s string) string { return config.ResolveVars(s, vars) }

	decryptCredFile := func(path string) string {
		if path == "" {
			return ""
		}
		decrypted, err := m.decryptCredentialIfVault(pluginName, path)
		if err != nil {
			log.Printf("[%s] credential file decrypt: %v", pluginName, err)
			return path
		}
		return decrypted
	}

	safeTransport, err := proxy.SafeTransportWithTLS(false, proxy.TLSConfig{})
	if err != nil {
		log.Printf("[%s] safe transport for auth: %v", manifest.Name, err)
		return nil
	}

	var variantCfg auth.VariantConfig
	if len(authCfg.Variants) > 0 {
		variantCfg.Select = resolve(authCfg.Select)
		variantCfg.Variants = make(map[string]auth.SingleAuthConfig)

		// Filter out variants whose provider is disabled (auto-select only;
		// explicit selection is already caught by checkDisabledProvider).
		for _, name := range authCfg.VariantOrder {
			v := authCfg.Variants[name]
			if m.isProviderDisabled(auth.NormalizeProviderType(v.Type)) {
				log.Printf("[%s] skipping variant %q: provider %q is disabled",
					manifest.Name, name, auth.NormalizeProviderType(v.Type))
				continue
			}
			variantCfg.VariantOrder = append(variantCfg.VariantOrder, name)
			variantCfg.Variants[name] = auth.SingleAuthConfig{
				Type:            v.Type,
				Token:           resolve(v.Token),
				Header:          v.Header,
				Prefix:          v.Prefix,
				Username:        resolve(v.Username),
				Password:        resolve(v.Password),
				SPN:             resolve(v.SPN),
				Scopes:          v.Scopes,
				CredentialsFile: decryptCredFile(resolve(v.CredentialsFile)),
				TokenFile:       resolve(v.TokenFile),
				CredentialsDir:  credDir,
				TokenURL:        resolve(v.TokenURL),
				ClientID:        resolve(v.ClientID),
				Transport:       safeTransport,
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
				CredentialsFile: decryptCredFile(resolve(authCfg.CredentialsFile)),
				TokenFile:       resolve(authCfg.TokenFile),
				CredentialsDir:  credDir,
				TokenURL:        resolve(authCfg.TokenURL),
				ClientID:        resolve(authCfg.ClientID),
				Transport:       safeTransport,
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

func (m *Manager) topologicalSort() ([]string, map[string]string, error) {
	// Pre-filter: skip plugins with unresolvable or skipped
	// dependencies. Propagate transitively until stable.
	// Track root-cause reasons so disabled stubs are actionable.
	skipped := make(map[string]bool)
	reasons := make(map[string]string) // name → root-cause reason
	changed := true
	for changed {
		changed = false
		for name, manifest := range m.manifests {
			if skipped[name] {
				continue
			}
			for _, dep := range manifest.DependsOn {
				if _, exists := m.manifests[dep]; !exists {
					reasons[name] = fmt.Sprintf("requires unavailable plugin %q", dep)
					skipped[name] = true
					changed = true
					break
				}
				if skipped[dep] {
					// Use root cause from the dependency, not "depends on skipped"
					reasons[name] = fmt.Sprintf("requires plugin %q which is disabled: %s", dep, reasons[dep])
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
			return nil, nil, err
		}
	}

	return sorted, reasons, nil
}

// dependencyLevels groups plugin names by their topological depth so
// that all plugins in the same level can be started in parallel. Level 0
// contains plugins with no dependencies; level N contains plugins whose
// deepest dependency is at level N-1.
//
// The sorted input must be a valid topological ordering (from
// topologicalSort), guaranteeing that each plugin's dependencies appear
// before it. Within each level, the original sorted order is preserved.
func (m *Manager) dependencyLevels(sorted []string) [][]string {
	depth := make(map[string]int, len(sorted))
	for _, name := range sorted {
		d := 0
		for _, dep := range m.manifests[name].DependsOn {
			if ld, ok := depth[dep]; ok && ld+1 > d {
				d = ld + 1
			}
		}
		depth[name] = d
	}

	// Group by depth, preserving sorted order within each level.
	var levels [][]string
	for _, name := range sorted {
		d := depth[name]
		for len(levels) <= d {
			levels = append(levels, nil)
		}
		levels[d] = append(levels[d], name)
	}
	return levels
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

// pluginDirs holds the EvalSymlinks-resolved directories for a plugin.
type pluginDirs struct {
	outputDir string
	tmpDir    string
}

// serviceHandlerImpl implements ServiceHandler by delegating to proxy, cache, and fileio.
type serviceHandlerImpl struct {
	proxy *proxy.Proxy
	cache cache.Store
	dirs  sync.Map // pluginName -> pluginDirs
}

func (s *serviceHandlerImpl) HandleHTTP(ctx context.Context, pluginName string, req protocol.Message) protocol.Message {
	return s.proxy.Execute(ctx, pluginName, req)
}

func (s *serviceHandlerImpl) HandleCache(ctx context.Context, pluginName string, req protocol.Message) protocol.Message {
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
		// Namespace is intentionally hardcoded to pluginName (not
		// CacheNamespace()) for security isolation between plugins.
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

func (s *serviceHandlerImpl) HandleFileIO(ctx context.Context, pluginName string, req protocol.Message) protocol.Message {
	v, ok := s.dirs.Load(pluginName)
	if !ok {
		return fileIOError(req.ID, req.Type, "plugin not configured for file I/O")
	}
	dirs := v.(pluginDirs)

	switch req.Type {
	case protocol.TypeFileWrite:
		if access := proxy.ToolAccessFromContext(ctx); access == "read" && !proxy.LocalWriteFromContext(ctx) {
			return fileIOError(req.ID, req.Type, "read-only tool cannot write files")
		}
		writeReq := fileio.WriteRequest{
			Path:        req.Path,
			Content:     req.Content,
			HasContent:  req.HasContent,
			SourcePath:  req.SourcePath,
			Encoding:    req.BodyEncoding,
			Permissions: req.Permissions,
			Mkdir:       req.Mkdir,
		}
		cfg := fileio.Config{
			OutputDir: dirs.outputDir,
			TmpDir:    dirs.tmpDir,
		}
		result, err := fileio.WriteFile(cfg, writeReq)
		if err != nil {
			return fileIOError(req.ID, req.Type, err.Error())
		}
		size := result.Size
		return protocol.Message{
			ID:   req.ID,
			Type: protocol.TypeFileWriteResponse,
			Path: result.Path,
			Size: &size,
		}

	case protocol.TypeFileRead:
		readReq := fileio.ReadRequest{
			Path:     req.Path,
			Encoding: req.BodyEncoding,
		}
		cfg := fileio.Config{
			OutputDir: dirs.outputDir,
			TmpDir:    dirs.tmpDir,
		}
		result, err := fileio.ReadFile(cfg, readReq)
		if err != nil {
			return fileIOError(req.ID, req.Type, err.Error())
		}
		return protocol.Message{
			ID:      req.ID,
			Type:    protocol.TypeFileReadResponse,
			Content: result.Content,
			Path:    result.Path,
		}

	default:
		return fileIOError(req.ID, req.Type, "unknown file I/O operation: "+req.Type)
	}
}

// registerPluginDirs stores EvalSymlinks-resolved directories for a plugin.
func (s *serviceHandlerImpl) registerPluginDirs(name, outputDir, tmpDir string) {
	s.dirs.Store(name, pluginDirs{outputDir: outputDir, tmpDir: tmpDir})
}

// unregisterPluginDirs removes a plugin's directory registration.
func (s *serviceHandlerImpl) unregisterPluginDirs(name string) {
	s.dirs.Delete(name)
}

func cacheError(id, msgType string, err error) protocol.Message {
	return protocol.Message{
		ID:    id,
		Type:  msgType,
		Error: &protocol.Error{Code: "cache_error", Message: err.Error()},
	}
}
