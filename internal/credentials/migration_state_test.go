package credentials

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMigrationStateFromFixture(t *testing.T) {
	state, err := LoadMigrationState(filepath.Join("testdata", "test-migration.yaml"))
	if err != nil {
		t.Fatalf("LoadMigrationState: %v", err)
	}

	if state.Version != 1 {
		t.Errorf("Version = %d, want 1", state.Version)
	}

	if !state.IsMigrated("google") {
		t.Error("IsMigrated(google) = false, want true")
	}

	if state.IsMigrated("jira") {
		t.Error("IsMigrated(jira) = true, want false")
	}

	groups := state.GetMigratedGroups()
	if len(groups) != 1 || groups[0] != "google" {
		t.Errorf("GetMigratedGroups() = %v, want [google]", groups)
	}
}

func TestLoadMigrationStateNotExist(t *testing.T) {
	state, err := LoadMigrationState(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("LoadMigrationState missing file: %v", err)
	}

	if state.Version != 1 {
		t.Errorf("Version = %d, want 1", state.Version)
	}

	if len(state.GetMigratedGroups()) != 0 {
		t.Errorf("expected empty groups for missing file")
	}
}

func TestLoadMigrationStateInvalidFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("not: [valid: yaml: content"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadMigrationState(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestMigrationStateMarkAndUnmark(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.yaml")
	state, err := LoadMigrationState(path)
	if err != nil {
		t.Fatalf("LoadMigrationState: %v", err)
	}

	// Mark jira
	if err := state.MarkMigrated("jira"); err != nil {
		t.Fatalf("MarkMigrated(jira): %v", err)
	}
	if !state.IsMigrated("jira") {
		t.Error("IsMigrated(jira) = false after mark")
	}

	// Mark again (no-op)
	if err := state.MarkMigrated("jira"); err != nil {
		t.Fatalf("MarkMigrated(jira) second: %v", err)
	}

	// Mark google
	if err := state.MarkMigrated("google"); err != nil {
		t.Fatalf("MarkMigrated(google): %v", err)
	}

	groups := state.GetMigratedGroups()
	if len(groups) != 2 {
		t.Fatalf("GetMigratedGroups() = %v, want 2 groups", groups)
	}

	// Unmark jira
	if err := state.Unmark("jira"); err != nil {
		t.Fatalf("Unmark(jira): %v", err)
	}
	if state.IsMigrated("jira") {
		t.Error("IsMigrated(jira) = true after unmark")
	}

	// Unmark nonexistent (no-op)
	if err := state.Unmark("nonexistent"); err != nil {
		t.Fatalf("Unmark(nonexistent): %v", err)
	}

	// Only google remains
	groups = state.GetMigratedGroups()
	if len(groups) != 1 || groups[0] != "google" {
		t.Errorf("GetMigratedGroups() = %v, want [google]", groups)
	}
}

func TestMigrationStatePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persist.yaml")

	// Create and save
	state, err := LoadMigrationState(path)
	if err != nil {
		t.Fatalf("LoadMigrationState: %v", err)
	}
	if err := state.MarkMigrated("jira"); err != nil {
		t.Fatalf("MarkMigrated: %v", err)
	}
	if err := state.MarkMigrated("google"); err != nil {
		t.Fatalf("MarkMigrated: %v", err)
	}

	// Reload from disk
	loaded, err := LoadMigrationState(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	if !loaded.IsMigrated("jira") {
		t.Error("reloaded: IsMigrated(jira) = false")
	}
	if !loaded.IsMigrated("google") {
		t.Error("reloaded: IsMigrated(google) = false")
	}

	groups := loaded.GetMigratedGroups()
	if len(groups) != 2 {
		t.Errorf("reloaded: GetMigratedGroups() = %v, want 2 groups", groups)
	}
}

func TestMigrationStateFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "perms.yaml")

	state, err := LoadMigrationState(path)
	if err != nil {
		t.Fatalf("LoadMigrationState: %v", err)
	}
	if err := state.MarkMigrated("test"); err != nil {
		t.Fatalf("MarkMigrated: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("file permissions = %04o, want 0600", perm)
	}
}

func TestGetMigratedGroupsReturnsCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "copy.yaml")
	state, err := LoadMigrationState(path)
	if err != nil {
		t.Fatalf("LoadMigrationState: %v", err)
	}
	if err := state.MarkMigrated("original"); err != nil {
		t.Fatalf("MarkMigrated: %v", err)
	}

	// Modify the returned slice
	groups := state.GetMigratedGroups()
	groups[0] = "tampered"

	// Internal state should be unaffected
	if !state.IsMigrated("original") {
		t.Error("internal state was modified by changing returned slice")
	}
}
