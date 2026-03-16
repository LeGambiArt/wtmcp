// wtmcp is an MCP server with a language-agnostic plugin protocol.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/LeGambiArt/wtmcp/internal/auth"
	"github.com/LeGambiArt/wtmcp/internal/cache"
	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/plugin"
	"github.com/LeGambiArt/wtmcp/internal/proxy"
	"github.com/LeGambiArt/wtmcp/internal/server"
)

// Version and BuildDate are set via ldflags at build time.
var (
	Version   = "dev"
	BuildDate = "unknown"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("wtmcp %s (built %s)\n", Version, BuildDate)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "check" {
		if err := runCheck(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// Parse flags
	configPath := ""
	workdirOverride := ""
	for i, arg := range os.Args[1:] {
		if arg == "--config" && i+2 < len(os.Args) {
			configPath = os.Args[i+2]
		}
		if arg == "--workdir" && i+2 < len(os.Args) {
			workdirOverride = os.Args[i+2]
		}
	}

	// Resolve workdir
	workdir := config.WorkDir()
	if workdirOverride != "" {
		workdir = workdirOverride
	}

	// Set up file logging in workdir/logs/
	logsDir := filepath.Join(workdir, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err == nil {
		logPath := filepath.Join(logsDir, "server.log")
		if logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil { //nolint:gosec // log file in user's config dir
			log.SetOutput(logFile)
			log.SetFlags(log.LstdFlags | log.Lshortfile)
		}
	}

	// Load scoped env.d groups (not into process env)
	envResult, err := config.LoadEnvGroups(workdir)
	if err != nil {
		return fmt.Errorf("load env: %w", err)
	}
	for group, msg := range envResult.Errors {
		log.Printf("WARNING: env group %s disabled: %s", group, msg)
	}

	// Load config (uses workdir for defaults)
	cfg, err := config.Load(configPath, workdir)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	authReg := auth.NewRegistry()

	// Initialize Kerberos if available
	if err := auth.InitKerberos(); err != nil {
		log.Printf("kerberos not available: %v", err)
	} else if auth.KerberosAvailable() {
		defer auth.CloseKerberos()
		log.Println("kerberos/spnego auth available")
	}

	cacheStore := cache.NewMemoryStore()
	httpProxy := proxy.New(nil, cfg.Plugins.MaxMessageSize)

	mgr := plugin.NewManager(authReg, httpProxy, cacheStore, cfg, envResult.Groups, envResult.Errors, workdir)

	if err := mgr.Discover(cfg.PluginDirs, cfg.UserPluginDir); err != nil {
		return fmt.Errorf("plugin discovery: %w", err)
	}

	if err := mgr.LoadAll(ctx); err != nil {
		return fmt.Errorf("plugin loading: %w", err)
	}

	index := server.NewToolIndex(mgr)
	srv := server.New(Version, mgr, cfg, index)

	// Start control directory watcher for external reload triggers
	controlWatcher := server.NewControlWatcher(workdir, srv, mgr, cfg, index)
	if err := controlWatcher.Start(); err != nil {
		log.Printf("control watcher disabled: %v", err)
	}

	go func() {
		<-ctx.Done()
		controlWatcher.Stop()
		log.Println("shutting down plugins...")
		mgr.ShutdownAll(context.Background())
	}()

	log.Printf("wtmcp %s starting (workdir: %s)", Version, workdir)
	return mcpserver.ServeStdio(srv)
}

// runCheck prints diagnostic info about the config and discovered plugins.
func runCheck() error {
	configPath := ""
	workdirOverride := ""
	for i, arg := range os.Args[2:] {
		if arg == "--config" && i+3 < len(os.Args) {
			configPath = os.Args[i+3]
		}
		if arg == "--workdir" && i+3 < len(os.Args) {
			workdirOverride = os.Args[i+3]
		}
	}

	// Discover plugins using shared discovery logic
	result, err := plugin.Discover(plugin.DiscoveryOptions{
		ConfigPath:      configPath,
		WorkdirOverride: workdirOverride,
	})
	if err != nil {
		return err
	}

	fmt.Printf("wtmcp %s\n", Version)
	fmt.Printf("workdir: %s\n", result.Workdir)
	fmt.Printf("user plugins: %v\n", result.Config.Plugins.UserPlugins)
	fmt.Printf("env groups: %d\n", len(result.EnvGroups))
	for group := range result.EnvGroups {
		fmt.Printf("  - %s\n", group)
	}
	if len(result.EnvErrors) > 0 {
		fmt.Printf("env group errors: %d\n", len(result.EnvErrors))
		for group, msg := range result.EnvErrors {
			fmt.Printf("  - %s: %s\n", group, msg)
		}
	}
	fmt.Printf("\nplugin search path:\n")
	for i, dir := range result.Config.PluginDirs {
		exists := "missing"
		if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
			exists = "ok"
		}
		fmt.Printf("  %d. %s [%s]\n", i+1, dir, exists)
	}

	manifests := result.Manager.Manifests()
	fmt.Printf("\ndiscovered plugins: %d\n", len(manifests))
	var totalPrimary, totalDeferred int
	for _, m := range manifests {
		var primaryCount, deferredCount int
		for _, t := range m.Tools {
			if t.IsPrimary() {
				primaryCount++
			} else {
				deferredCount++
			}
		}
		totalPrimary += primaryCount
		totalDeferred += deferredCount
		fmt.Printf("  - %s v%s (%s)\n", m.Name, m.Version, m.Dir)
		fmt.Printf("    handler: %s | execution: %s | tools: %d (primary: %d, deferred: %d)\n",
			m.Handler, m.Execution, len(m.Tools), primaryCount, deferredCount)
	}

	fmt.Printf("\ntool discovery: %s\n", result.Config.Tools.Discovery)
	fmt.Printf("primary tools: %d\n", totalPrimary)
	fmt.Printf("deferred tools: %d\n", totalDeferred)

	if len(manifests) == 0 {
		fmt.Println("\nno plugins found. check that plugin directories contain")
		fmt.Println("subdirectories with plugin.yaml files.")
	}

	return nil
}
