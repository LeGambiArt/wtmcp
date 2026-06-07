package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LeGambiArt/wtmcp/internal/config"
)

func testConfig() config.SandboxConfig {
	return config.SandboxConfig{
		Defaults: config.SandboxResourceLimits{
			MaxMemoryMB:   512,
			MaxCPUPct:     100,
			MaxPIDs:       64,
			MaxFileSizeMB: 100,
		},
	}
}

func TestPrepareDirs(t *testing.T) {
	tmpBase := t.TempDir()
	dataBase := t.TempDir()

	t.Setenv("TMPDIR", tmpBase)

	b := &base{cfg: testConfig(), dataDir: dataBase}
	tmpDir, dataDir, err := b.PrepareDirs(PluginInfo{Name: "test-plugin"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
		t.Errorf("tmpdir not created: %s", tmpDir)
	}
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		t.Errorf("datadir not created: %s", dataDir)
	}

	info, _ := os.Stat(dataDir)
	if info.Mode().Perm() != 0o700 {
		t.Errorf("datadir mode = %o, want 700", info.Mode().Perm())
	}
}

func TestPrepareDirsOutputDir(t *testing.T) {
	tmpBase := t.TempDir()
	dataBase := t.TempDir()
	outDir := filepath.Join(t.TempDir(), ".wtmcp-data", "test-plugin")

	t.Setenv("TMPDIR", tmpBase)

	b := &base{cfg: testConfig(), dataDir: dataBase}
	_, _, err := b.PrepareDirs(PluginInfo{Name: "test-plugin", OutputDir: outDir})
	if err != nil {
		t.Fatal(err)
	}

	// outputDir should NOT be created by PrepareDirs — lazy creation
	// happens in the core's file I/O service on first write.
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Errorf("outputDir should NOT be created by PrepareDirs: %s", outDir)
	}
}

func TestCleanupTmpDir(t *testing.T) {
	tmpBase := t.TempDir()
	t.Setenv("TMPDIR", tmpBase)

	b := &base{cfg: testConfig(), dataDir: t.TempDir()}
	_, _, err := b.PrepareDirs(PluginInfo{Name: "cleanup-test"})
	if err != nil {
		t.Fatal(err)
	}

	tmpDir := b.TmpDir("cleanup-test")
	if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
		t.Fatal("tmpdir should exist before cleanup")
	}

	b.CleanupTmpDir("cleanup-test")

	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Error("tmpdir should be removed after cleanup")
	}
}

func TestIsPython(t *testing.T) {
	if !isPython("./handler.py") {
		t.Error("handler.py should be Python")
	}
	if isPython("./handler") {
		t.Error("./handler should not be Python")
	}
}
