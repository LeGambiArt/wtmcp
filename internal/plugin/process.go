package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/LeGambiArt/wtmcp/internal/protocol"
	"github.com/LeGambiArt/wtmcp/internal/sandbox"
)

// State represents the plugin process lifecycle state.
type State int

// Plugin process lifecycle states.
const (
	StateUnloaded State = iota
	StateStarting
	StateRunning
	StateFailed
	StateStopping
)

// Process manages a plugin handler's OS process and transport.
type Process struct {
	cmd               *exec.Cmd
	sbMgr             *sandbox.Manager
	sbProc            *sandbox.Process
	cancel            context.CancelFunc
	Transport         *Transport
	manifest          *Manifest
	handler           ServiceHandler
	groupVars         map[string]string
	sessionDir        string
	outputDir         string
	resolvedConfig    json.RawMessage // snapshot, avoids racing on manifest field
	state             State
	Resources         []protocol.ResourceDef // resources discovered at init
	Domains           []string               // dynamic domains from init_ok
	AuthBindings      map[string]string      // per-domain auth bindings from init_ok
	initTimeout       time.Duration
	shutdownTimeout   time.Duration
	shutdownKillAfter time.Duration
	maxMessageSize    int
}

// ProcessConfig holds process management settings.
type ProcessConfig struct {
	InitTimeout       time.Duration
	ShutdownTimeout   time.Duration
	ShutdownKillAfter time.Duration
	MaxMessageSize    int
}

// NewProcess creates a Process for the given manifest. groupVars are
// the scoped env.d variables for this plugin's credential_group.
func NewProcess(manifest *Manifest, handler ServiceHandler, cfg ProcessConfig, groupVars map[string]string) *Process {
	return &Process{
		manifest:          manifest,
		handler:           handler,
		groupVars:         groupVars,
		state:             StateUnloaded,
		initTimeout:       cfg.InitTimeout,
		shutdownTimeout:   cfg.ShutdownTimeout,
		shutdownKillAfter: cfg.ShutdownKillAfter,
		maxMessageSize:    cfg.MaxMessageSize,
	}
}

// SetSandbox configures the sandbox manager for this process.
// When set and enabled, Start() launches via arapuca instead of
// exec.Command.
func (p *Process) SetSandbox(sb *sandbox.Manager) {
	p.sbMgr = sb
}

// State returns the current process state.
func (p *Process) State() State { return p.state }

// Start launches the plugin handler process and sends init for
// persistent plugins.
func (p *Process) Start(ctx context.Context) error {
	p.state = StateStarting

	var stdin io.WriteCloser
	var stdout, stderr io.ReadCloser

	if p.sbMgr != nil && p.sbMgr.Enabled() {
		if err := p.startSandboxed(&stdin, &stdout, &stderr); err != nil {
			return err
		}
	} else {
		if p.sbMgr != nil {
			warnUnsandboxedOnce.Do(func() {
				log.Println("WARNING: sandbox not available (binary built without libarapuca) — plugin processes are not isolated")
			})
		}
		if err := p.startUnsandboxed(&stdin, &stdout, &stderr); err != nil {
			return err
		}
	}

	p.Transport = NewTransport(stdin, stdout, stderr, p.maxMessageSize)

	go p.Transport.ForwardStderr(p.manifest.Name)
	go p.Transport.ReadLoop(p.manifest.Name, p.manifest.Concurrency, p.handler)

	if p.manifest.Execution == "persistent" {
		if err := p.doInit(ctx); err != nil {
			return err
		}
	}

	if p.manifest.ProvidesResources() {
		if err := p.queryResources(ctx); err != nil {
			return err
		}
	}

	p.state = StateRunning
	return nil
}

func (p *Process) startSandboxed(stdin *io.WriteCloser, stdout, stderr *io.ReadCloser) error {
	if err := validateHandlerAtLaunch(p.manifest); err != nil {
		p.state = StateFailed
		return err
	}

	launchCtx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	env := buildPluginEnvMap(p.manifest, p.groupVars)
	sbProc, err := p.sbMgr.Launch(launchCtx, sandbox.PluginInfo{
		Name:            p.manifest.Name,
		Dir:             p.manifest.Dir,
		Handler:         p.manifest.Handler,
		CredentialGroup: p.manifest.CredentialGroup,
		SessionDir:      p.sessionDir,
		OutputDir:       p.outputDir,
	}, env)
	if err != nil {
		cancel()
		p.state = StateFailed
		return fmt.Errorf("sandbox launch: %w", err)
	}

	p.sbProc = sbProc
	*stdin = sbProc.Stdin()
	*stdout = sbProc.Stdout()
	*stderr = sbProc.Stderr()
	return nil
}

func (p *Process) startUnsandboxed(stdin *io.WriteCloser, stdout, stderr *io.ReadCloser) error {
	if err := validateHandlerAtLaunch(p.manifest); err != nil {
		p.state = StateFailed
		return err
	}
	p.cmd = exec.Command(p.manifest.HandlerPath()) //nolint:gosec // handler path is validated by Manifest.Validate()
	p.cmd.Dir = p.manifest.Dir
	p.cmd.Env = buildPluginEnv(p.manifest, p.groupVars)

	var err error
	*stdin, err = p.cmd.StdinPipe()
	if err != nil {
		p.state = StateFailed
		return fmt.Errorf("stdin pipe: %w", err)
	}
	*stdout, err = p.cmd.StdoutPipe()
	if err != nil {
		p.state = StateFailed
		return fmt.Errorf("stdout pipe: %w", err)
	}
	*stderr, err = p.cmd.StderrPipe()
	if err != nil {
		p.state = StateFailed
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := p.cmd.Start(); err != nil {
		p.state = StateFailed
		return fmt.Errorf("start handler: %w", err)
	}
	return nil
}

func (p *Process) doInit(ctx context.Context) error {
	initCtx, cancel := context.WithTimeout(ctx, p.initTimeout)
	defer cancel()

	id := p.Transport.GenerateID("init")
	resp, err := p.Transport.SendAndWait(initCtx, id, protocol.Message{
		Type:     protocol.TypeInit,
		Protocol: protocol.ProtocolVersion,
		Config:   p.resolvedConfig,
	})
	if err != nil {
		p.kill()
		p.state = StateFailed
		return fmt.Errorf("plugin %s init timed out: %w", p.manifest.Name, err)
	}
	if resp.Type == protocol.TypeInitError {
		p.kill()
		p.state = StateFailed
		errMsg := "unknown error"
		if resp.Error != nil {
			errMsg = resp.Error.Message
		}
		return fmt.Errorf("plugin %s init failed: %s", p.manifest.Name, errMsg)
	}
	p.Domains = resp.Domains
	p.AuthBindings = resp.AuthBindings
	return nil
}

func (p *Process) queryResources(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, p.initTimeout)
	defer cancel()

	id := p.Transport.GenerateID("res")
	resp, err := p.Transport.SendAndWait(ctx, id, protocol.Message{
		Type: protocol.TypeListResources,
	})
	if err != nil {
		p.kill()
		p.state = StateFailed
		return fmt.Errorf("plugin %s list_resources: %w", p.manifest.Name, err)
	}
	if resp.Error != nil {
		p.kill()
		p.state = StateFailed
		return fmt.Errorf("plugin %s list_resources failed: %s", p.manifest.Name, resp.Error.Message)
	}
	p.Resources = resp.Resources
	log.Printf("[%s] discovered %d resources", p.manifest.Name, len(p.Resources))
	return nil
}

// Stop gracefully shuts down the plugin process.
func (p *Process) Stop(ctx context.Context) error {
	if p.sbProc == nil && (p.cmd == nil || p.cmd.Process == nil) {
		return nil
	}

	p.state = StateStopping

	if p.manifest.Execution == "persistent" {
		shutdownCtx, cancel := context.WithTimeout(ctx, p.shutdownTimeout)
		defer cancel()

		id := p.Transport.GenerateID("shutdown")
		_, err := p.Transport.SendAndWait(shutdownCtx, id, protocol.Message{Type: protocol.TypeShutdown})
		if err != nil {
			log.Printf("[%s] shutdown timed out, force stopping", p.manifest.Name)
			return p.forceStop()
		}
	}

	return p.wait()
}

func (p *Process) wait() error {
	if p.sbProc != nil {
		if p.cancel != nil {
			p.cancel()
		}
		_, err := p.sbProc.Wait()
		p.sbProc.Stdin().Close()  //nolint:errcheck,gosec // best effort
		p.sbProc.Stdout().Close() //nolint:errcheck,gosec // best effort
		p.sbProc.Stderr().Close() //nolint:errcheck,gosec // best effort
		p.logResourceStats()
		p.sbProc.Cleanup()
		p.sbMgr.CleanupTmpDir(p.manifest.Name)
		return err
	}
	return p.cmd.Wait()
}

func (p *Process) forceStop() error {
	if p.sbProc != nil {
		if p.cancel != nil {
			p.cancel()
		}
		return p.wait()
	}

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		p.cmd.Process.Kill() //nolint:errcheck,gosec // best effort
		return p.cmd.Wait()
	}

	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-time.After(p.shutdownKillAfter):
		log.Printf("[%s] SIGTERM timed out, sending SIGKILL", p.manifest.Name)
		p.cmd.Process.Kill() //nolint:errcheck,gosec // best effort
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			log.Printf("[%s] WARNING: process did not exit after SIGKILL", p.manifest.Name)
			return fmt.Errorf("process %d did not exit after SIGKILL", p.cmd.Process.Pid)
		}
	}
}

func (p *Process) kill() {
	if p.sbProc != nil {
		if p.cancel != nil {
			p.cancel()
		}
		p.sbProc.Wait()           //nolint:errcheck,gosec // reap process
		p.sbProc.Stdin().Close()  //nolint:errcheck,gosec // best effort
		p.sbProc.Stdout().Close() //nolint:errcheck,gosec // best effort
		p.sbProc.Stderr().Close() //nolint:errcheck,gosec // best effort
		p.logResourceStats()
		p.sbProc.Cleanup()
		p.sbMgr.CleanupTmpDir(p.manifest.Name)
		return
	}
	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill() //nolint:errcheck,gosec // best effort
		p.cmd.Wait()         //nolint:errcheck,gosec // reap zombie
	}
}

func (p *Process) logResourceStats() {
	if p.sbProc == nil {
		return
	}
	stats := p.sbProc.ResourceStats()
	oom := p.sbProc.OOMCount()
	log.Printf("[%s] sandbox stats: mem_peak=%dMB cpu=%.1fs pids=%d io_r=%dKB io_w=%dKB oom=%d",
		p.manifest.Name,
		stats.MemoryPeakBytes/(1024*1024),
		stats.CPUUsageSeconds,
		stats.PIDCount,
		stats.IOReadBytes/1024,
		stats.IOWriteBytes/1024,
		oom,
	)
}

// buildPluginEnv constructs a filtered environment for plugin processes.
// Only safe system variables are passed from the process environment.
// Plugin-specific variables come exclusively from the scoped env.d
// vars map (matched by credential_group) — never from the process
// environment.
func buildPluginEnv(manifest *Manifest, groupVars map[string]string) []string {
	m := buildPluginEnvMap(manifest, groupVars)
	env := make([]string, 0, len(m))
	for k, v := range m {
		env = append(env, k+"="+v)
	}
	return env
}

func buildPluginEnvMap(manifest *Manifest, groupVars map[string]string) map[string]string {
	allowlist := []string{
		"PATH", "HOME", "USER", "SHELL", "LANG", "TERM", "TZ", "TMPDIR",
		"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME",
	}

	env := make(map[string]string, len(allowlist)+len(manifest.Env))
	for _, key := range allowlist {
		if val, ok := os.LookupEnv(key); ok {
			env[key] = val
		}
	}

	if manifest.EnvPassthrough == "all" {
		for key, val := range groupVars {
			env[key] = val
		}
	} else {
		for _, key := range manifest.Env {
			if val, ok := groupVars[key]; ok {
				env[key] = val
			}
		}
	}

	return env
}
