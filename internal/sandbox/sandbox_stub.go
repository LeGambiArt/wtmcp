//go:build !sandbox

// Package sandbox provides a stub implementation when built without
// the sandbox tag. All plugins run unsandboxed via exec.Command.
package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/LeGambiArt/wtmcp/internal/config"
)

// Built reports whether the binary includes sandbox support.
func Built() bool { return false }

// Manager is a no-op sandbox manager for builds without arapuca.
type Manager struct {
	base
}

// NewManager creates a stub sandbox manager. Returns an error if
// the config explicitly enables sandbox (Enabled == &true), since
// the binary cannot provide it. Logs a warning to stderr if the
// config uses the default (nil, which defaults to enabled).
func NewManager(cfg config.SandboxConfig, credDir, dataDir string) (*Manager, error) {
	if cfg.Enabled != nil && *cfg.Enabled {
		return nil, fmt.Errorf(
			"sandbox explicitly enabled in config but binary built without -tags sandbox")
	}
	if cfg.Enabled == nil {
		fmt.Fprintln(os.Stderr,
			"WARNING: sandbox not available (binary built without -tags sandbox), plugins will run unsandboxed")
	}
	return &Manager{base: base{cfg: cfg, credDir: credDir, dataDir: dataDir}}, nil
}

// Close is a no-op.
func (m *Manager) Close() {}

// Enabled always returns false in builds without the sandbox tag.
func (m *Manager) Enabled() bool { return false }

// Available returns whether the binary was built with sandbox support.
func (m *Manager) Available() bool { return false }

// Launch is not supported without the sandbox tag.
func (m *Manager) Launch(_ context.Context, _ PluginInfo, _ map[string]string) (*Process, error) {
	return nil, fmt.Errorf("sandbox not available: build with -tags sandbox and libarapuca installed")
}

// Process is a compilation stub. Its methods are unreachable at
// runtime because consumers nil-check sbProc before calling them.
type Process struct{}

func (p *Process) Stdin() io.WriteCloser        { return nil }                                     //nolint:revive
func (p *Process) Stdout() io.ReadCloser        { return nil }                                     //nolint:revive
func (p *Process) Stderr() io.ReadCloser        { return nil }                                     //nolint:revive
func (p *Process) Wait() (int, error)           { return -1, fmt.Errorf("sandbox not available") } //nolint:revive
func (p *Process) PID() int                     { return -1 }                                      //nolint:revive
func (p *Process) ResourceStats() ResourceUsage { return ResourceUsage{} }                         //nolint:revive
func (p *Process) OOMCount() int                { return 0 }                                       //nolint:revive
func (p *Process) Cleanup()                     {}                                                 //nolint:revive
