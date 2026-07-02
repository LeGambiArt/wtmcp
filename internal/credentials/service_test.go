package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newTestService creates a Service wired to a temp directory for
// env.d files and migration state. The mock keyring is initialised
// by the package-level init() in keyring_store_test.go.
func newTestService(t *testing.T) *Service {
	t.Helper()

	tmpDir := t.TempDir()
	envdDir := filepath.Join(tmpDir, "env.d")
	if err := os.MkdirAll(envdDir, 0o700); err != nil {
		t.Fatalf("create env.d dir: %v", err)
	}
	migrationPath := filepath.Join(tmpDir, "migration.yaml")

	svc, err := NewService(envdDir, migrationPath)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// writeEnvDFile writes a KEY=VALUE file into the service's env.d
// directory.
func writeEnvDFile(t *testing.T, svc *Service, group string, content string) {
	t.Helper()
	path := filepath.Join(svc.envdStore.envDir, group+".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env.d file %s: %v", group, err)
	}
}

// --- Test: Migrated group with keyring value returns keyring ---

func TestServiceGet_MigratedGroupKeyring(t *testing.T) {
	svc := newTestService(t)

	// Store a credential in keyring and mark group migrated.
	if err := svc.keyringStore.Set("google", "TOKEN", "kr-secret"); err != nil {
		t.Fatalf("keyring set: %v", err)
	}
	t.Cleanup(func() { _ = svc.keyringStore.Delete("google", "TOKEN") })

	if err := svc.migrationState.MarkMigrated("google"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}

	value, source, err := svc.Get("google", "TOKEN")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if value != "kr-secret" {
		t.Errorf("value = %q, want %q", value, "kr-secret")
	}
	if source != SourceKeyring {
		t.Errorf("source = %v, want %v", source, SourceKeyring)
	}
}

// --- Test: Migrated group with env.d file still present logs warning ---

func TestServiceGet_MigratedGroupEnvDStillPresent(t *testing.T) {
	svc := newTestService(t)

	// Store in keyring and create env.d file.
	if err := svc.keyringStore.Set("google", "TOKEN", "kr-value"); err != nil {
		t.Fatalf("keyring set: %v", err)
	}
	t.Cleanup(func() { _ = svc.keyringStore.Delete("google", "TOKEN") })

	writeEnvDFile(t, svc, "google", "TOKEN=envd-value\n")

	if err := svc.migrationState.MarkMigrated("google"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}

	// Should return the keyring value (not env.d), and the warning
	// is logged (we verify behavior, not log output).
	value, source, err := svc.Get("google", "TOKEN")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if value != "kr-value" {
		t.Errorf("value = %q, want %q", value, "kr-value")
	}
	if source != SourceKeyring {
		t.Errorf("source = %v, want %v", source, SourceKeyring)
	}
}

// --- Test: Migrated group, keyring has no value, falls back to envvar ---

func TestServiceGet_MigratedGroupFallbackEnvVar(t *testing.T) {
	svc := newTestService(t)

	if err := svc.migrationState.MarkMigrated("google"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}

	const envKey = "GOOGLE_API_KEY"
	t.Setenv(envKey, "env-value")

	value, source, err := svc.Get("google", envKey)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if value != "env-value" {
		t.Errorf("value = %q, want %q", value, "env-value")
	}
	if source != SourceEnvVar {
		t.Errorf("source = %v, want %v", source, SourceEnvVar)
	}
}

// --- Test: Non-migrated group with env.d returns env.d ---

func TestServiceGet_LegacyGroupEnvD(t *testing.T) {
	svc := newTestService(t)

	writeEnvDFile(t, svc, "jira", "JIRA_TOKEN=envd-token\n")

	value, source, err := svc.Get("jira", "JIRA_TOKEN")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if value != "envd-token" {
		t.Errorf("value = %q, want %q", value, "envd-token")
	}
	if source != SourceEnvD {
		t.Errorf("source = %v, want %v", source, SourceEnvD)
	}
}

// --- Test: Non-migrated group without env.d, with keyring ---

func TestServiceGet_LegacyGroupFallbackKeyring(t *testing.T) {
	svc := newTestService(t)

	if err := svc.keyringStore.Set("jira", "JIRA_TOKEN", "kr-fallback"); err != nil {
		t.Fatalf("keyring set: %v", err)
	}
	t.Cleanup(func() { _ = svc.keyringStore.Delete("jira", "JIRA_TOKEN") })

	value, source, err := svc.Get("jira", "JIRA_TOKEN")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if value != "kr-fallback" {
		t.Errorf("value = %q, want %q", value, "kr-fallback")
	}
	if source != SourceKeyring {
		t.Errorf("source = %v, want %v", source, SourceKeyring)
	}
}

// --- Test: Non-migrated group without env.d or keyring, with envvar ---

func TestServiceGet_LegacyGroupFallbackEnvVar(t *testing.T) {
	svc := newTestService(t)

	const envKey = "JIRA_TOKEN"
	t.Setenv(envKey, "env-fallback")

	value, source, err := svc.Get("jira", envKey)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if value != "env-fallback" {
		t.Errorf("value = %q, want %q", value, "env-fallback")
	}
	if source != SourceEnvVar {
		t.Errorf("source = %v, want %v", source, SourceEnvVar)
	}
}

// --- Test: Not found in any source ---

func TestServiceGet_NotFound(t *testing.T) {
	svc := newTestService(t)

	_, source, err := svc.Get("nonexistent", "MISSING_KEY")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("err = %v, want ErrCredentialNotFound", err)
	}
	if source != SourceNotFound {
		t.Errorf("source = %v, want %v", source, SourceNotFound)
	}
}

// --- Test: Caching works (second Get returns cached value) ---

func TestServiceGet_CacheHit(t *testing.T) {
	svc := newTestService(t)

	writeEnvDFile(t, svc, "jira", "JIRA_TOKEN=original\n")

	// First call populates cache.
	v1, s1, err := svc.Get("jira", "JIRA_TOKEN")
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if v1 != "original" || s1 != SourceEnvD {
		t.Fatalf("first Get: value=%q source=%v", v1, s1)
	}

	// Overwrite env.d file with new value.
	writeEnvDFile(t, svc, "jira", "JIRA_TOKEN=modified\n")

	// Second call should return cached value.
	v2, s2, err := svc.Get("jira", "JIRA_TOKEN")
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if v2 != "original" {
		t.Errorf("cached value = %q, want %q (original)", v2, "original")
	}
	if s2 != SourceEnvD {
		t.Errorf("cached source = %v, want %v", s2, SourceEnvD)
	}
}

// --- Test: Cache invalidation on Set ---

func TestServiceSet_InvalidatesCache(t *testing.T) {
	svc := newTestService(t)

	writeEnvDFile(t, svc, "jira", "JIRA_TOKEN=envd-val\n")

	// Populate cache.
	if _, _, err := svc.Get("jira", "JIRA_TOKEN"); err != nil {
		t.Fatalf("initial Get: %v", err)
	}

	// Set with forceKeyring invalidates cache and stores in keyring.
	if err := svc.Set("jira", "JIRA_TOKEN", "new-kr-val", true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	t.Cleanup(func() { _ = svc.keyringStore.Delete("jira", "JIRA_TOKEN") })

	// Get should now resolve from keyring (legacy: env.d -> keyring).
	// But env.d still has the old value, so env.d wins for legacy groups.
	v, src, err := svc.Get("jira", "JIRA_TOKEN")
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	// For non-migrated group, env.d takes precedence.
	if v != "envd-val" {
		t.Errorf("value = %q, want %q (env.d takes precedence for legacy)", v, "envd-val")
	}
	if src != SourceEnvD {
		t.Errorf("source = %v, want %v", src, SourceEnvD)
	}
}

// --- Test: Set with forceKeyring flag ---

func TestServiceSet_ForceKeyring(t *testing.T) {
	svc := newTestService(t)

	if err := svc.Set("newgroup", "API_KEY", "secret", true); err != nil {
		t.Fatalf("Set forceKeyring: %v", err)
	}
	t.Cleanup(func() { _ = svc.keyringStore.Delete("newgroup", "API_KEY") })

	// Verify stored in keyring.
	got, err := svc.keyringStore.Get("newgroup", "API_KEY")
	if err != nil {
		t.Fatalf("keyring Get: %v", err)
	}
	if got != "secret" {
		t.Errorf("keyring value = %q, want %q", got, "secret")
	}
}

// --- Test: Set for migrated group stores in keyring ---

func TestServiceSet_MigratedGroup(t *testing.T) {
	svc := newTestService(t)

	if err := svc.migrationState.MarkMigrated("google"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}

	if err := svc.Set("google", "TOKEN", "migrated-secret", false); err != nil {
		t.Fatalf("Set migrated: %v", err)
	}
	t.Cleanup(func() { _ = svc.keyringStore.Delete("google", "TOKEN") })

	got, err := svc.keyringStore.Get("google", "TOKEN")
	if err != nil {
		t.Fatalf("keyring Get: %v", err)
	}
	if got != "migrated-secret" {
		t.Errorf("keyring value = %q, want %q", got, "migrated-secret")
	}
}

// --- Test: Set for non-migrated group without forceKeyring returns error ---

func TestServiceSet_NonMigratedGroupError(t *testing.T) {
	svc := newTestService(t)

	err := svc.Set("jira", "TOKEN", "value", false)
	if err == nil {
		t.Fatal("Set non-migrated without forceKeyring should error")
	}
	if err.Error() != "writing to env.d files not supported" {
		t.Errorf("err = %q, want %q", err.Error(), "writing to env.d files not supported")
	}
}

// --- Test: InvalidateCache ---

func TestServiceInvalidateCache(t *testing.T) {
	svc := newTestService(t)

	writeEnvDFile(t, svc, "jira", "TOKEN=cached-val\n")

	// Populate cache.
	if _, _, err := svc.Get("jira", "TOKEN"); err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Invalidate.
	svc.InvalidateCache("jira", "TOKEN")

	// Update the file.
	writeEnvDFile(t, svc, "jira", "TOKEN=new-val\n")

	// Should resolve fresh.
	v, _, err := svc.Get("jira", "TOKEN")
	if err != nil {
		t.Fatalf("Get after invalidate: %v", err)
	}
	if v != "new-val" {
		t.Errorf("value = %q, want %q", v, "new-val")
	}
}

// --- Test: ClearCache ---

func TestServiceClearCache(t *testing.T) {
	svc := newTestService(t)

	writeEnvDFile(t, svc, "jira", "TOKEN=val1\n")
	writeEnvDFile(t, svc, "google", "KEY=val2\n")

	// Populate cache with two entries.
	if _, _, err := svc.Get("jira", "TOKEN"); err != nil {
		t.Fatalf("Get jira: %v", err)
	}
	if _, _, err := svc.Get("google", "KEY"); err != nil {
		t.Fatalf("Get google: %v", err)
	}

	// Clear cache.
	svc.ClearCache()

	// Update both files.
	writeEnvDFile(t, svc, "jira", "TOKEN=updated1\n")
	writeEnvDFile(t, svc, "google", "KEY=updated2\n")

	// Both should resolve fresh values.
	v1, _, err := svc.Get("jira", "TOKEN")
	if err != nil {
		t.Fatalf("Get jira after clear: %v", err)
	}
	if v1 != "updated1" {
		t.Errorf("jira value = %q, want %q", v1, "updated1")
	}

	v2, _, err := svc.Get("google", "KEY")
	if err != nil {
		t.Fatalf("Get google after clear: %v", err)
	}
	if v2 != "updated2" {
		t.Errorf("google value = %q, want %q", v2, "updated2")
	}
}

// --- Test: GetMigrationState ---

func TestServiceGetMigrationState(t *testing.T) {
	svc := newTestService(t)

	ms := svc.GetMigrationState()
	if ms == nil {
		t.Fatal("GetMigrationState returned nil")
	}

	// Initially no groups migrated.
	if groups := ms.GetMigratedGroups(); len(groups) != 0 {
		t.Errorf("expected 0 migrated groups, got %v", groups)
	}

	// Mark one and verify through service.
	if err := ms.MarkMigrated("test"); err != nil {
		t.Fatalf("MarkMigrated: %v", err)
	}
	if !svc.GetMigrationState().IsMigrated("test") {
		t.Error("IsMigrated(test) = false after MarkMigrated")
	}
}

// --- Test: NewService with invalid migration path ---

func TestNewService_InvalidMigrationFormat(t *testing.T) {
	tmpDir := t.TempDir()
	migPath := filepath.Join(tmpDir, "bad.yaml")
	if err := os.WriteFile(migPath, []byte("not: [valid: yaml: content"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := NewService(tmpDir, migPath)
	if err == nil {
		t.Fatal("expected error for invalid migration file")
	}
}

// --- Test: NewService with nonexistent migration file (OK) ---

func TestNewService_MissingMigrationFile(t *testing.T) {
	tmpDir := t.TempDir()
	migPath := filepath.Join(tmpDir, "nonexistent.yaml")

	svc, err := NewService(tmpDir, migPath)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if svc == nil {
		t.Fatal("NewService returned nil service")
	}
}

// --- Test: Migrated group not found anywhere ---

func TestServiceGet_MigratedGroupNotFound(t *testing.T) {
	svc := newTestService(t)

	if err := svc.migrationState.MarkMigrated("empty"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}

	_, source, err := svc.Get("empty", "MISSING")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("err = %v, want ErrCredentialNotFound", err)
	}
	if source != SourceNotFound {
		t.Errorf("source = %v, want %v", source, SourceNotFound)
	}
}

// --- Test: CredentialProvider interface compliance ---

func TestCredentialProviderInterface(_ *testing.T) {
	// Verify that all stores satisfy the CredentialProvider interface.
	var _ CredentialProvider = (*KeyringStore)(nil)
	var _ CredentialProvider = (*EnvDStore)(nil)
	var _ CredentialProvider = (*EnvVarProvider)(nil)
}

// --- Test: Precedence - env.d takes priority over keyring for legacy ---

func TestServiceGet_LegacyPrecedenceEnvDOverKeyring(t *testing.T) {
	svc := newTestService(t)

	// Put same key in both env.d and keyring.
	writeEnvDFile(t, svc, "jira", "TOKEN=envd-wins\n")

	if err := svc.keyringStore.Set("jira", "TOKEN", "kr-loses"); err != nil {
		t.Fatalf("keyring set: %v", err)
	}
	t.Cleanup(func() { _ = svc.keyringStore.Delete("jira", "TOKEN") })

	value, source, err := svc.Get("jira", "TOKEN")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if value != "envd-wins" {
		t.Errorf("value = %q, want %q", value, "envd-wins")
	}
	if source != SourceEnvD {
		t.Errorf("source = %v, want %v", source, SourceEnvD)
	}
}

// --- Test: Migrated group with keyring truly unavailable (not just missing) ---

func TestServiceGet_MigratedGroupKeyringUnavailable(t *testing.T) {
	// TODO: This requires mocking a keyring error that's NOT ErrCredentialNotFound
	// Current keyring.MockInit() doesn't support this scenario
	// Skip for now, will address when adding keyring error simulation
	t.Skip("Keyring error simulation not yet implemented in mock")
}

// --- Test: warnKeyringUnavailable only logs once ---

func TestServiceWarnKeyringUnavailableOnce(t *testing.T) {
	svc := newTestService(t)

	// Call multiple times.
	svc.warnKeyringUnavailable()
	svc.warnKeyringUnavailable()
	svc.warnKeyringUnavailable()

	// Verify the flag is set.
	svc.mu.Lock()
	warned := svc.keyringUnavailableWarned
	svc.mu.Unlock()

	if !warned {
		t.Error("keyringUnavailableWarned = false after warnKeyringUnavailable()")
	}
}
