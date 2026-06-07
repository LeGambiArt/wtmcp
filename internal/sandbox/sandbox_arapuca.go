//go:build !nosandbox

// Package sandbox wraps go-arapuca to provide OS-level isolation
// for plugin handler processes.
package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/LeGambiArt/wtmcp/internal/config"
	arapuca "github.com/sergio-correia/go-arapuca"
)

// Built reports whether the binary includes sandbox support.
func Built() bool { return true }

// Manager manages sandboxed plugin process lifecycles.
type Manager struct {
	base
	sb *arapuca.Sandbox
}

// NewManager creates a sandbox manager. credDir is the base
// credentials directory; dataDir is the base persistent data
// directory (e.g., ~/.local/share/wtmcp/data).
func NewManager(cfg config.SandboxConfig, credDir, dataDir string) (*Manager, error) {
	sb, err := arapuca.New()
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	return &Manager{
		base: base{cfg: cfg, credDir: credDir, dataDir: dataDir},
		sb:   sb,
	}, nil
}

// Close releases the sandbox resources.
func (m *Manager) Close() {
	if m.sb != nil {
		m.sb.Close()
	}
}

// Enabled returns whether sandboxing is active per configuration.
func (m *Manager) Enabled() bool {
	return m.cfg.SandboxEnabled()
}

// Available returns whether the binary was built with sandbox support.
func (m *Manager) Available() bool { return true }

// buildProfile constructs an arapuca Profile from plugin metadata
// and server configuration.
func (m *Manager) buildProfile(info PluginInfo) arapuca.Profile {
	limits := m.limitsFor(info.Name)

	read := systemReadPaths()
	read = append(read, info.Dir)

	if m.credDir != "" && info.CredentialGroup != "" {
		groupDir := filepath.Join(m.credDir, info.CredentialGroup)
		read = append(read, groupDir)
	}

	if isPython(info.Handler) {
		read = append(read, pythonReadPaths()...)
	}

	if info.SessionDir != "" {
		read = append(read, info.SessionDir)
	}

	// OutputDir is not in the sandbox profile — all file I/O
	// (reads and writes) goes through the core's HandleFileIO.
	// The core creates outputDir lazily on first write.

	tmpDir := m.TmpDir(info.Name)
	dataDir := m.DataDir(info.Name)

	write := []string{tmpDir, dataDir}

	return arapuca.Profile{
		ReadPaths:     read,
		WritePaths:    write,
		MaxMemoryMB:   limits.MaxMemoryMB,
		MaxCPUPct:     limits.MaxCPUPct,
		MaxPIDs:       limits.MaxPIDs,
		MaxFileSizeMB: limits.MaxFileSizeMB,
		UseNetNS:      true,
	}
}

// Launch starts a sandboxed process for the given plugin. Returns
// a Process with stdin/stdout/stderr pipes for the parent.
func (m *Manager) Launch(ctx context.Context, info PluginInfo, env map[string]string) (*Process, error) {
	profile := m.buildProfile(info)
	tmpDir, _, err := m.PrepareDirs(info)
	if err != nil {
		return nil, err
	}

	pipes, err := newPipeSet()
	if err != nil {
		return nil, err
	}

	if env == nil {
		env = make(map[string]string)
	}
	env["TMPDIR"] = tmpDir

	cfg := arapuca.Config{
		Profile: profile,
		TaskID:  sanitizeTaskID(info.Name),
		WorkDir: info.Dir,
		Stdin:   pipes.stdinR,
		Stdout:  pipes.stdoutW,
		Stderr:  pipes.stderrW,
		Env:     env,
	}

	handlerPath := filepath.Join(info.Dir, info.Handler)
	proc, err := m.sb.Launch(ctx, cfg, handlerPath, nil, nil)
	if err != nil {
		pipes.closeAll()
		return nil, err
	}

	pipes.closeChildSide()

	return &Process{
		proc:   proc,
		stdin:  pipes.stdinW,
		stdout: pipes.stdoutR,
		stderr: pipes.stderrR,
		name:   info.Name,
	}, nil
}

// Process wraps an arapuca sandboxed process, providing the parent-
// side pipes and lifecycle methods.
type Process struct {
	proc   *arapuca.Process
	stdin  *os.File
	stdout *os.File
	stderr *os.File
	name   string
}

// Stdin returns the write end of the child's stdin pipe.
func (p *Process) Stdin() io.WriteCloser { return p.stdin }

// Stdout returns the read end of the child's stdout pipe.
func (p *Process) Stdout() io.ReadCloser { return p.stdout }

// Stderr returns the read end of the child's stderr pipe.
func (p *Process) Stderr() io.ReadCloser { return p.stderr }

// Wait waits for the sandboxed process to exit. Returns the exit code.
func (p *Process) Wait() (int, error) { return p.proc.Wait() }

// PID returns the sandboxed process ID.
func (p *Process) PID() int { return p.proc.PID() }

// ResourceStats returns cgroup v2 resource usage. Must be called
// after Wait() and before Cleanup().
func (p *Process) ResourceStats() ResourceUsage {
	r := p.proc.ResourceStats()
	return ResourceUsage{
		MemoryCurrentBytes: r.MemoryCurrentBytes,
		MemoryPeakBytes:    r.MemoryPeakBytes,
		CPUUsageSeconds:    r.CPUUsageSeconds,
		PIDCount:           r.PIDCount,
		IOReadBytes:        r.IOReadBytes,
		IOWriteBytes:       r.IOWriteBytes,
	}
}

// OOMCount returns the number of OOM kills detected.
func (p *Process) OOMCount() int {
	return p.proc.OOMCount()
}

// Cleanup releases cgroup, tmpdir, and other kernel resources.
// Must be called after Wait(). Safe to call multiple times.
func (p *Process) Cleanup() {
	p.proc.Cleanup()
}

func (m *Manager) limitsFor(pluginName string) config.SandboxResourceLimits {
	limits := m.cfg.Defaults
	if override, ok := m.cfg.Plugins[pluginName]; ok {
		if override.MaxMemoryMB > 0 {
			limits.MaxMemoryMB = override.MaxMemoryMB
		}
		if override.MaxCPUPct > 0 {
			limits.MaxCPUPct = override.MaxCPUPct
		}
		if override.MaxPIDs > 0 {
			limits.MaxPIDs = override.MaxPIDs
		}
		if override.MaxFileSizeMB > 0 {
			limits.MaxFileSizeMB = override.MaxFileSizeMB
		}
	}
	return limits
}

// sanitizeTaskID converts a plugin name to a valid arapuca task ID.
// Arapuca allows [a-zA-Z0-9-] only; underscores become hyphens.
func sanitizeTaskID(name string) string {
	return strings.ReplaceAll(name, "_", "-")
}

func systemReadPaths() []string {
	paths := []string{
		"/usr",
		"/lib",
		"/etc/ssl/certs",
		"/proc/self",
		"/dev/null",
		"/dev/urandom",
		"/dev/zero",
	}
	if runtime.GOOS == "linux" {
		paths = append(paths, "/lib64", "/etc/pki")
	}
	return paths
}

func pythonReadPaths() []string {
	interp, err := exec.LookPath("python3")
	if err != nil {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(interp)
	if err != nil {
		return []string{interp}
	}
	return []string{resolved, filepath.Dir(resolved)}
}

// pipeSet holds the six FDs for stdin/stdout/stderr pipe pairs.
type pipeSet struct {
	stdinR, stdinW   *os.File
	stdoutR, stdoutW *os.File
	stderrR, stderrW *os.File
}

func newPipeSet() (*pipeSet, error) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		closeFDs(stdinR, stdinW)
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		closeFDs(stdinR, stdinW, stdoutR, stdoutW)
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	return &pipeSet{stdinR, stdinW, stdoutR, stdoutW, stderrR, stderrW}, nil
}

func (p *pipeSet) closeAll() {
	closeFDs(p.stdinR, p.stdinW, p.stdoutR, p.stdoutW, p.stderrR, p.stderrW)
}

func (p *pipeSet) closeChildSide() {
	closeFDs(p.stdinR, p.stdoutW, p.stderrW)
}

func closeFDs(fds ...*os.File) {
	for _, f := range fds {
		f.Close() //nolint:errcheck,gosec // best effort cleanup
	}
}
