//go:build nosandbox

// Package sandbox provides a degraded-mode stub when built without
// libarapuca (via -tags nosandbox). Plugins run unsandboxed.
// Requires WTMCP_UNSANDBOXED=1 at runtime to start with default
// config, and loudly warns about the lack of isolation.
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

// NewManager creates a degraded-mode sandbox manager. The behavior
// depends on the config and the WTMCP_UNSANDBOXED env var:
//
//   - enabled: false  → allow (sandbox not expected)
//   - enabled: true   → error (explicit enable can't be satisfied)
//   - enabled: nil    → error unless WTMCP_UNSANDBOXED=1
func NewManager(cfg config.SandboxConfig, credDir, dataDir string) (*Manager, error) {
	if cfg.Enabled != nil && *cfg.Enabled {
		return nil, fmt.Errorf(
			"sandbox explicitly enabled in config but binary built without libarapuca; " +
				"rebuild with libarapuca or set sandbox.enabled: false in config")
	}

	if cfg.Enabled == nil {
		if os.Getenv("WTMCP_UNSANDBOXED") != "1" {
			return nil, fmt.Errorf(
				"binary built without sandbox support (libarapuca not linked)\n" +
					"To continue without sandbox isolation:\n" +
					"  1. Set WTMCP_UNSANDBOXED=1 in your environment, or\n" +
					"  2. Set sandbox.enabled: false in your config, or\n" +
					"  3. Rebuild with libarapuca installed (recommended)")
		}
		fmt.Fprintln(os.Stderr,
			"WARNING: UNSANDBOXED MODE — binary built without libarapuca, plugins run without OS-level isolation")
	}

	return &Manager{base: base{cfg: cfg, credDir: credDir, dataDir: dataDir}}, nil
}

// Close is a no-op.
func (m *Manager) Close() {}

// Enabled always returns false in builds without libarapuca.
func (m *Manager) Enabled() bool { return false }

// Available returns whether the binary was built with sandbox support.
func (m *Manager) Available() bool { return false }

// Launch is not supported without libarapuca.
func (m *Manager) Launch(_ context.Context, _ PluginInfo, _ map[string]string) (*Process, error) {
	return nil, fmt.Errorf("sandbox not available: binary built without libarapuca")
}

// Process is a compilation stub. Its methods are unreachable at
// runtime because consumers check Enabled() before calling Launch.
type Process struct{}

func (p *Process) Stdin() io.WriteCloser        { return nil }                                     //nolint:revive
func (p *Process) Stdout() io.ReadCloser        { return nil }                                     //nolint:revive
func (p *Process) Stderr() io.ReadCloser        { return nil }                                     //nolint:revive
func (p *Process) Wait() (int, error)           { return -1, fmt.Errorf("sandbox not available") } //nolint:revive
func (p *Process) PID() int                     { return -1 }                                      //nolint:revive
func (p *Process) ResourceStats() ResourceUsage { return ResourceUsage{} }                         //nolint:revive
func (p *Process) OOMCount() int                { return 0 }                                       //nolint:revive
func (p *Process) Cleanup()                     {}                                                 //nolint:revive
