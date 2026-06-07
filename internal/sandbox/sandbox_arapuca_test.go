//go:build !nosandbox

package sandbox

import (
	"runtime"
	"strings"
	"testing"

	"github.com/LeGambiArt/wtmcp/internal/config"
)

func TestBuildProfileReadPaths(t *testing.T) {
	m := &Manager{
		base: base{cfg: testConfig(), credDir: "/creds", dataDir: "/data"},
	}

	info := PluginInfo{
		Name:            "gitlab",
		Dir:             "/opt/wtmcp/plugins/gitlab",
		Handler:         "./handler",
		CredentialGroup: "gitlab",
	}

	profile := m.buildProfile(info)

	mustContain := []string{
		"/opt/wtmcp/plugins/gitlab",
		"/creds/gitlab",
		"/usr",
		"/lib",
		"/proc/self",
		"/dev/null",
		"/dev/urandom",
	}
	for _, p := range mustContain {
		if !contains(profile.ReadPaths, p) {
			t.Errorf("ReadPaths missing %q", p)
		}
	}

	if runtime.GOOS == "linux" {
		if !contains(profile.ReadPaths, "/lib64") {
			t.Error("ReadPaths missing /lib64 on Linux")
		}
	}
}

func TestBuildProfileWritePaths(t *testing.T) {
	m := &Manager{
		base: base{cfg: testConfig(), dataDir: "/data"},
	}

	info := PluginInfo{Name: "test-plugin", Dir: "/plugins/test", Handler: "./handler"}
	profile := m.buildProfile(info)

	if len(profile.WritePaths) != 2 {
		t.Fatalf("WritePaths = %v, want 2 entries", profile.WritePaths)
	}

	tmpDir := m.TmpDir("test-plugin")
	dataDir := m.DataDir("test-plugin")

	if profile.WritePaths[0] != tmpDir {
		t.Errorf("WritePaths[0] = %q, want %q", profile.WritePaths[0], tmpDir)
	}
	if profile.WritePaths[1] != dataDir {
		t.Errorf("WritePaths[1] = %q, want %q", profile.WritePaths[1], dataDir)
	}
}

func TestBuildProfileSessionDir(t *testing.T) {
	m := &Manager{base: base{cfg: testConfig(), dataDir: "/data"}}

	t.Run("included when set", func(t *testing.T) {
		info := PluginInfo{Name: "test", Dir: "/p", Handler: "./handler", SessionDir: "/home/user/project"}
		profile := m.buildProfile(info)
		found := false
		for _, p := range profile.ReadPaths {
			if p == "/home/user/project" {
				found = true
			}
		}
		if !found {
			t.Errorf("ReadPaths should include sessionDir, got %v", profile.ReadPaths)
		}
	})

	t.Run("excluded when empty", func(t *testing.T) {
		info := PluginInfo{Name: "test", Dir: "/p", Handler: "./handler"}
		profile := m.buildProfile(info)
		for _, p := range profile.ReadPaths {
			if p == "" {
				t.Error("ReadPaths should not contain empty string")
			}
		}
	})
}

func TestBuildProfileOutputDir(t *testing.T) {
	m := &Manager{base: base{cfg: testConfig(), dataDir: "/data"}}

	t.Run("output excluded from profile", func(t *testing.T) {
		info := PluginInfo{Name: "test", Dir: "/p", Handler: "./handler", OutputDir: "/home/user/project/.wtmcp-data/test"}
		profile := m.buildProfile(info)
		if len(profile.WritePaths) != 2 {
			t.Fatalf("WritePaths = %v, want 2 entries (tmp + data only)", profile.WritePaths)
		}
		for _, p := range profile.ReadPaths {
			if p == "/home/user/project/.wtmcp-data/test" {
				t.Error("outputDir should NOT be in ReadPaths — all I/O goes through core")
			}
		}
	})

	t.Run("excluded when empty", func(t *testing.T) {
		info := PluginInfo{Name: "test", Dir: "/p", Handler: "./handler"}
		profile := m.buildProfile(info)
		if len(profile.WritePaths) != 2 {
			t.Fatalf("WritePaths = %v, want 2 entries (tmp + data only)", profile.WritePaths)
		}
	})
}

func TestBuildProfileNetNS(t *testing.T) {
	m := &Manager{base: base{cfg: testConfig(), dataDir: "/data"}}
	info := PluginInfo{Name: "test", Dir: "/p", Handler: "./handler"}
	profile := m.buildProfile(info)

	if !profile.UseNetNS {
		t.Error("UseNetNS should always be true")
	}
}

func TestBuildProfileResourceDefaults(t *testing.T) {
	m := &Manager{base: base{cfg: testConfig(), dataDir: "/data"}}
	info := PluginInfo{Name: "test", Dir: "/p", Handler: "./handler"}
	profile := m.buildProfile(info)

	if profile.MaxMemoryMB != 512 {
		t.Errorf("MaxMemoryMB = %d, want 512", profile.MaxMemoryMB)
	}
	if profile.MaxCPUPct != 100 {
		t.Errorf("MaxCPUPct = %d, want 100", profile.MaxCPUPct)
	}
	if profile.MaxPIDs != 64 {
		t.Errorf("MaxPIDs = %d, want 64", profile.MaxPIDs)
	}
	if profile.MaxFileSizeMB != 100 {
		t.Errorf("MaxFileSizeMB = %d, want 100", profile.MaxFileSizeMB)
	}
}

func TestBuildProfileResourceOverrides(t *testing.T) {
	cfg := testConfig()
	cfg.Plugins = map[string]config.SandboxResourceLimits{
		"big-plugin": {MaxMemoryMB: 2048, MaxPIDs: 128},
	}
	m := &Manager{base: base{cfg: cfg, dataDir: "/data"}}

	info := PluginInfo{Name: "big-plugin", Dir: "/p", Handler: "./handler"}
	profile := m.buildProfile(info)

	if profile.MaxMemoryMB != 2048 {
		t.Errorf("MaxMemoryMB = %d, want 2048 (override)", profile.MaxMemoryMB)
	}
	if profile.MaxPIDs != 128 {
		t.Errorf("MaxPIDs = %d, want 128 (override)", profile.MaxPIDs)
	}
	if profile.MaxCPUPct != 100 {
		t.Errorf("MaxCPUPct = %d, want 100 (default, not overridden)", profile.MaxCPUPct)
	}
}

func TestBuildProfilePythonPlugin(t *testing.T) {
	m := &Manager{base: base{cfg: testConfig(), dataDir: "/data"}}

	goInfo := PluginInfo{Name: "go-plugin", Dir: "/p", Handler: "./handler"}
	pyInfo := PluginInfo{Name: "py-plugin", Dir: "/p", Handler: "./handler.py"}

	goProfile := m.buildProfile(goInfo)
	pyProfile := m.buildProfile(pyInfo)

	if len(pyProfile.ReadPaths) <= len(goProfile.ReadPaths) {
		t.Error("Python plugin should have more ReadPaths than Go plugin (interpreter)")
	}
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func TestBuildProfileNoCredentialGroup(t *testing.T) {
	m := &Manager{base: base{cfg: testConfig(), credDir: "/creds", dataDir: "/data"}}
	info := PluginInfo{Name: "test", Dir: "/p", Handler: "./handler"}

	profile := m.buildProfile(info)

	for _, p := range profile.ReadPaths {
		if strings.HasPrefix(p, "/creds") {
			t.Errorf("ReadPaths should not include creds dir when no credential group: %v", p)
		}
	}
}
