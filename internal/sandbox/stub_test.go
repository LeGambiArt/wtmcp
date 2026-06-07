//go:build nosandbox

package sandbox

import (
	"context"
	"strings"
	"testing"
)

func TestStubNewManagerWithoutEnvVar(t *testing.T) {
	cfg := testConfig()
	t.Setenv("WTMCP_UNSANDBOXED", "")

	_, err := NewManager(cfg, "", t.TempDir())
	if err == nil {
		t.Fatal("NewManager without WTMCP_UNSANDBOXED=1 should error")
	}
	if !strings.Contains(err.Error(), "WTMCP_UNSANDBOXED") {
		t.Errorf("error should mention WTMCP_UNSANDBOXED, got: %v", err)
	}
}

func TestStubNewManagerWithEnvVar(t *testing.T) {
	cfg := testConfig()
	t.Setenv("WTMCP_UNSANDBOXED", "1")

	mgr, err := NewManager(cfg, "", t.TempDir())
	if err != nil {
		t.Fatalf("WTMCP_UNSANDBOXED=1 should allow startup: %v", err)
	}
	defer mgr.Close()

	if mgr == nil {
		t.Fatal("manager should be non-nil")
	}
}

func TestStubEnabled(t *testing.T) {
	t.Setenv("WTMCP_UNSANDBOXED", "1")
	mgr, _ := NewManager(testConfig(), "", t.TempDir())
	defer mgr.Close()

	if mgr.Enabled() {
		t.Error("stub Enabled() must always return false")
	}
}

func TestStubLaunchReturnsError(t *testing.T) {
	t.Setenv("WTMCP_UNSANDBOXED", "1")
	mgr, _ := NewManager(testConfig(), "", t.TempDir())
	defer mgr.Close()

	info := PluginInfo{Name: "test", Dir: "/tmp", Handler: "./handler"}
	_, err := mgr.Launch(context.Background(), info, nil)
	if err == nil {
		t.Fatal("stub Launch() must return an error")
	}
}

func TestStubCloseIdempotent(t *testing.T) {
	t.Setenv("WTMCP_UNSANDBOXED", "1")
	mgr, _ := NewManager(testConfig(), "", t.TempDir())
	mgr.Close()
	mgr.Close()
}
