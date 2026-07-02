package credentials

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// =================================================================
// Integration tests for complete credential flows.
//
// These tests exercise end-to-end scenarios that span multiple
// components (service, keyring, env.d, migration state, cache,
// token encryption, backup/restore).
// =================================================================

// --- Flow 1: Fresh install ──────────────────────────────────────

// TestIntegration_FreshInstall_SetAndResolve verifies that a new
// user can store credentials in the keyring and resolve them.
func TestIntegration_FreshInstall_SetAndResolve(t *testing.T) {
	svc := newTestService(t)

	// Step 1: Store credentials with forceKeyring (no migration yet).
	if err := svc.Set("jira", "JIRA_TOKEN", "token123", true); err != nil {
		t.Fatalf("Set JIRA_TOKEN: %v", err)
	}
	t.Cleanup(func() { _ = svc.keyringStore.Delete("jira", "JIRA_TOKEN") })

	if err := svc.Set("jira", "JIRA_URL", "https://jira.example.com", true); err != nil {
		t.Fatalf("Set JIRA_URL: %v", err)
	}
	t.Cleanup(func() { _ = svc.keyringStore.Delete("jira", "JIRA_URL") })

	// Step 2: Resolve the credentials via the service.
	val, src, err := svc.Get("jira", "JIRA_TOKEN")
	if err != nil {
		t.Fatalf("Get JIRA_TOKEN: %v", err)
	}
	if val != "token123" || src != SourceKeyring {
		t.Errorf("JIRA_TOKEN: got (%q, %v), want (%q, %v)", val, src, "token123", SourceKeyring)
	}

	val, src, err = svc.Get("jira", "JIRA_URL")
	if err != nil {
		t.Fatalf("Get JIRA_URL: %v", err)
	}
	if val != "https://jira.example.com" || src != SourceKeyring {
		t.Errorf("JIRA_URL: got (%q, %v), want (%q, %v)", val, src, "https://jira.example.com", SourceKeyring)
	}

	// Step 3: Keys should be tracked in migration state.
	keys := svc.GetMigrationState().GetGroupKeys("jira")
	if len(keys) != 2 {
		t.Errorf("tracked keys = %v, want 2 keys", keys)
	}
}

// TestIntegration_FreshInstall_MultipleGroups stores credentials
// in multiple groups and verifies isolation between groups.
func TestIntegration_FreshInstall_MultipleGroups(t *testing.T) {
	svc := newTestService(t)

	groups := map[string]map[string]string{
		"google": {"CLIENT_ID": "goog-id", "CLIENT_SECRET": "goog-secret"},
		"jira":   {"JIRA_TOKEN": "jira-tok", "JIRA_URL": "https://jira.example.com"},
		"slack":  {"SLACK_TOKEN": "slack-tok"},
	}

	// Store all credentials.
	for group, creds := range groups {
		for key, val := range creds {
			group, key := group, key // shadow for closure capture
			if err := svc.Set(group, key, val, true); err != nil {
				t.Fatalf("Set %s/%s: %v", group, key, err)
			}
			t.Cleanup(func() { _ = svc.keyringStore.Delete(group, key) })
		}
	}

	// Verify isolation: each group only contains its own keys.
	for group, creds := range groups {
		for key, want := range creds {
			got, _, err := svc.Get(group, key)
			if err != nil {
				t.Fatalf("Get %s/%s: %v", group, key, err)
			}
			if got != want {
				t.Errorf("Get %s/%s = %q, want %q", group, key, got, want)
			}
		}
	}

	// ListGroups returns the union of env.d groups and migrated groups.
	// Set with forceKeyring tracks keys but does not mark groups as
	// migrated, so they only appear if we also mark them as migrated
	// or have env.d files. Verify via GetGroupInfo instead.
	for group, creds := range groups {
		info := svc.GetGroupInfo(group)
		if len(info.KeyringKeys) != len(creds) {
			t.Errorf("GetGroupInfo(%s): KeyringKeys = %v, want %d keys", group, info.KeyringKeys, len(creds))
		}
	}
}

// --- Flow 2: Migration ─────────────────────────────────────────

// TestIntegration_Migration_EnvDToKeyring simulates the full
// migration workflow: env.d credentials exist, migrate them to
// keyring, then verify the plugin still resolves the same values.
func TestIntegration_Migration_EnvDToKeyring(t *testing.T) {
	svc := newTestService(t)

	// Step 1: Start with env.d credentials (simulating legacy setup).
	writeEnvDFile(t, svc, "jira", "JIRA_TOKEN=legacy-token\nJIRA_URL=https://old.example.com\n")

	// Verify env.d resolution works.
	val, src, err := svc.Get("jira", "JIRA_TOKEN")
	if err != nil {
		t.Fatalf("Get JIRA_TOKEN before migration: %v", err)
	}
	if val != "legacy-token" || src != SourceEnvD {
		t.Errorf("before migration: got (%q, %v), want (%q, %v)", val, src, "legacy-token", SourceEnvD)
	}

	// Step 2: Read all env.d credentials.
	creds := svc.GetAll("jira")
	if len(creds) != 2 {
		t.Fatalf("GetAll(jira) returned %d creds, want 2", len(creds))
	}

	// Step 3: Store all credentials in keyring.
	svc.ClearCache() // clear cache so post-migration reads are fresh
	for key, value := range creds {
		key := key // shadow for closure capture
		if err := svc.Set("jira", key, value, true); err != nil {
			t.Fatalf("Set %s in keyring: %v", key, err)
		}
		t.Cleanup(func() { _ = svc.keyringStore.Delete("jira", key) })
	}

	// Step 4: Mark the group as migrated.
	if err := svc.GetMigrationState().MarkMigrated("jira"); err != nil {
		t.Fatalf("MarkMigrated: %v", err)
	}

	// Step 5: Invalidate cache and verify resolution now comes from keyring.
	svc.ClearCache()
	val, src, err = svc.Get("jira", "JIRA_TOKEN")
	if err != nil {
		t.Fatalf("Get JIRA_TOKEN after migration: %v", err)
	}
	if val != "legacy-token" || src != SourceKeyring {
		t.Errorf("after migration: got (%q, %v), want (%q, %v)", val, src, "legacy-token", SourceKeyring)
	}

	// Step 6: Update a credential in the keyring.
	svc.ClearCache()
	if err := svc.Set("jira", "JIRA_TOKEN", "new-token", false); err != nil {
		t.Fatalf("Set JIRA_TOKEN (updated): %v", err)
	}

	val, _, err = svc.Get("jira", "JIRA_TOKEN")
	if err != nil {
		t.Fatalf("Get JIRA_TOKEN after update: %v", err)
	}
	if val != "new-token" {
		t.Errorf("JIRA_TOKEN after update = %q, want %q", val, "new-token")
	}

	// Step 7: Verify the env.d file still has the old value (unmodified).
	envdVal, envdErr := svc.envdStore.Get("jira", "JIRA_TOKEN")
	if envdErr != nil {
		t.Fatalf("envdStore.Get: %v", envdErr)
	}
	if envdVal != "legacy-token" {
		t.Errorf("env.d JIRA_TOKEN = %q, should still be %q", envdVal, "legacy-token")
	}
}

// TestIntegration_Migration_PrecedenceChanges verifies that migration
// flips the precedence order: before migration env.d wins, after
// migration keyring wins.
func TestIntegration_Migration_PrecedenceChanges(t *testing.T) {
	svc := newTestService(t)

	// Same key in both env.d and keyring with different values.
	writeEnvDFile(t, svc, "app", "TOKEN=envd-value\n")
	if err := svc.keyringStore.Set("app", "TOKEN", "keyring-value"); err != nil {
		t.Fatalf("keyring set: %v", err)
	}
	t.Cleanup(func() { _ = svc.keyringStore.Delete("app", "TOKEN") })

	// Before migration: env.d wins.
	val, src, err := svc.Get("app", "TOKEN")
	if err != nil {
		t.Fatalf("Get before migration: %v", err)
	}
	if val != "envd-value" || src != SourceEnvD {
		t.Errorf("before migration: got (%q, %v), want (%q, %v)", val, src, "envd-value", SourceEnvD)
	}

	// Mark as migrated.
	if err := svc.GetMigrationState().MarkMigrated("app"); err != nil {
		t.Fatalf("MarkMigrated: %v", err)
	}
	svc.ClearCache()

	// After migration: keyring wins.
	val, src, err = svc.Get("app", "TOKEN")
	if err != nil {
		t.Fatalf("Get after migration: %v", err)
	}
	if val != "keyring-value" || src != SourceKeyring {
		t.Errorf("after migration: got (%q, %v), want (%q, %v)", val, src, "keyring-value", SourceKeyring)
	}
}

// TestIntegration_Migration_StatePersistedToDisk verifies that
// migration state is persisted and survives service restart.
func TestIntegration_Migration_StatePersistedToDisk(t *testing.T) {
	tmpDir := t.TempDir()
	envdDir := filepath.Join(tmpDir, "env.d")
	if err := os.MkdirAll(envdDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	migrationPath := filepath.Join(tmpDir, "migration.yaml")

	// First service instance: migrate a group.
	svc1, err := NewService(envdDir, migrationPath)
	if err != nil {
		t.Fatalf("NewService 1: %v", err)
	}
	if err := svc1.GetMigrationState().MarkMigrated("google"); err != nil {
		t.Fatalf("MarkMigrated: %v", err)
	}
	if err := svc1.Set("google", "CLIENT_ID", "id123", true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	t.Cleanup(func() { _ = svc1.keyringStore.Delete("google", "CLIENT_ID") })

	// Second service instance: verify migration state survived restart.
	svc2, err := NewService(envdDir, migrationPath)
	if err != nil {
		t.Fatalf("NewService 2: %v", err)
	}

	if !svc2.GetMigrationState().IsMigrated("google") {
		t.Error("google should be migrated after service restart")
	}

	keys := svc2.GetMigrationState().GetGroupKeys("google")
	if len(keys) != 1 || keys[0] != "CLIENT_ID" {
		t.Errorf("tracked keys after restart = %v, want [CLIENT_ID]", keys)
	}
}

// --- Flow 3: OAuth token encryption ────────────────────────────

// TestIntegration_OAuthTokenEncryption_FullLifecycle tests the
// complete OAuth token lifecycle: save, load, refresh, verify.
func TestIntegration_OAuthTokenEncryption_FullLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewKeyringStore()
	te := NewTokenEncryption(store, nil, tmpDir)

	group := "google"
	plugin := "calendar"

	// Step 1: Save initial token.
	initialToken := &oauth2.Token{
		AccessToken:  "initial-access",
		RefreshToken: "initial-refresh",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour).Truncate(time.Second),
	}

	if err := te.SaveToken(group, plugin, initialToken); err != nil {
		t.Fatalf("SaveToken initial: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Delete(group, "__encryption_key")
		_ = store.Delete(group, "__oauth_access_"+plugin)
	})

	// Step 2: Load and verify.
	loaded, err := te.LoadToken(group, plugin)
	if err != nil {
		t.Fatalf("LoadToken initial: %v", err)
	}
	if loaded.AccessToken != "initial-access" {
		t.Errorf("initial AccessToken = %q, want %q", loaded.AccessToken, "initial-access")
	}
	if loaded.RefreshToken != "initial-refresh" {
		t.Errorf("initial RefreshToken = %q, want %q", loaded.RefreshToken, "initial-refresh")
	}

	// Step 3: Simulate token refresh (new access token, same refresh token).
	refreshedToken := &oauth2.Token{
		AccessToken:  "refreshed-access",
		RefreshToken: "initial-refresh",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(2 * time.Hour).Truncate(time.Second),
	}

	if err := te.SaveToken(group, plugin, refreshedToken); err != nil {
		t.Fatalf("SaveToken refresh: %v", err)
	}

	// Step 4: Verify the refreshed token.
	loaded2, err := te.LoadToken(group, plugin)
	if err != nil {
		t.Fatalf("LoadToken refreshed: %v", err)
	}
	if loaded2.AccessToken != "refreshed-access" {
		t.Errorf("refreshed AccessToken = %q, want %q", loaded2.AccessToken, "refreshed-access")
	}
	if loaded2.RefreshToken != "initial-refresh" {
		t.Errorf("refreshed RefreshToken = %q, want %q", loaded2.RefreshToken, "initial-refresh")
	}
	if !loaded2.Expiry.Equal(refreshedToken.Expiry) {
		t.Errorf("refreshed Expiry = %v, want %v", loaded2.Expiry, refreshedToken.Expiry)
	}

	// Step 5: Verify encrypted file exists on disk (not plaintext).
	encPath := filepath.Join(tmpDir, group, "token-"+plugin+".json.enc")
	data, err := os.ReadFile(encPath) //nolint:gosec
	if err != nil {
		t.Fatalf("read encrypted file: %v", err)
	}

	var encFile EncryptedTokenFile
	if err := json.Unmarshal(data, &encFile); err != nil {
		t.Fatalf("unmarshal encrypted file: %v", err)
	}
	if encFile.Version != 1 {
		t.Errorf("encrypted file version = %d, want 1", encFile.Version)
	}

	// Step 6: Verify access token is in keyring, not on disk.
	accessKey := "__oauth_access_" + plugin
	stored, err := store.Get(group, accessKey)
	if err != nil {
		t.Fatalf("get access token from keyring: %v", err)
	}
	if stored != "refreshed-access" {
		t.Errorf("keyring access token = %q, want %q", stored, "refreshed-access")
	}
}

// TestIntegration_OAuthTokenEncryption_MultiplePlugins tests
// multiple plugins in the same group sharing the same encryption
// key but having separate token files.
func TestIntegration_OAuthTokenEncryption_MultiplePlugins(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewKeyringStore()
	te := NewTokenEncryption(store, nil, tmpDir)

	group := "google"
	plugins := []struct {
		name  string
		token *oauth2.Token
	}{
		{"calendar", &oauth2.Token{AccessToken: "cal-access", RefreshToken: "cal-refresh", TokenType: "Bearer"}},
		{"drive", &oauth2.Token{AccessToken: "drive-access", RefreshToken: "drive-refresh", TokenType: "Bearer"}},
		{"gmail", &oauth2.Token{AccessToken: "gmail-access", RefreshToken: "gmail-refresh", TokenType: "Bearer"}},
	}

	// Save all tokens.
	for _, p := range plugins {
		if err := te.SaveToken(group, p.name, p.token); err != nil {
			t.Fatalf("SaveToken %s: %v", p.name, err)
		}
		t.Cleanup(func() {
			_ = store.Delete(group, "__oauth_access_"+p.name)
		})
	}
	t.Cleanup(func() {
		_ = store.Delete(group, "__encryption_key")
	})

	// Load and verify each plugin's token is independent.
	for _, p := range plugins {
		loaded, err := te.LoadToken(group, p.name)
		if err != nil {
			t.Fatalf("LoadToken %s: %v", p.name, err)
		}
		if loaded.AccessToken != p.token.AccessToken {
			t.Errorf("%s AccessToken = %q, want %q", p.name, loaded.AccessToken, p.token.AccessToken)
		}
		if loaded.RefreshToken != p.token.RefreshToken {
			t.Errorf("%s RefreshToken = %q, want %q", p.name, loaded.RefreshToken, p.token.RefreshToken)
		}
	}

	// Verify all plugins share the same encryption key.
	encKey, err := store.Get(group, "__encryption_key")
	if err != nil {
		t.Fatalf("get encryption key: %v", err)
	}
	if encKey == "" {
		t.Error("encryption key should not be empty")
	}
}

// TestIntegration_OAuthTokenMigration_PlaintextToEncrypted tests
// the automatic migration of plaintext token files to encrypted
// format, then verifies the encrypted token still loads correctly.
func TestIntegration_OAuthTokenMigration_PlaintextToEncrypted(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewKeyringStore()
	te := NewTokenEncryption(store, nil, tmpDir)

	group := "google"
	plugin := "calendar"

	// Step 1: Create a legacy plaintext token file.
	groupDir := filepath.Join(tmpDir, group)
	if err := os.MkdirAll(groupDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	plainToken := map[string]string{
		"access_token":  "plain-access",
		"refresh_token": "plain-refresh",
		"token_type":    "Bearer",
		"expiry":        expiry.Format(time.RFC3339),
	}
	data, _ := json.Marshal(plainToken)
	plaintextPath := filepath.Join(groupDir, "token-"+plugin+".json")
	if err := os.WriteFile(plaintextPath, data, 0o600); err != nil {
		t.Fatalf("write plaintext token: %v", err)
	}

	// Step 2: Migrate.
	if err := te.MigrateUnencryptedToken(group, plugin); err != nil {
		t.Fatalf("MigrateUnencryptedToken: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Delete(group, "__encryption_key")
		_ = store.Delete(group, "__oauth_access_"+plugin)
	})

	// Step 3: Plaintext file should be removed.
	if _, err := os.Stat(plaintextPath); !os.IsNotExist(err) {
		t.Error("plaintext file should be removed after migration")
	}

	// Step 4: Encrypted file should exist.
	encPath := filepath.Join(groupDir, "token-"+plugin+".json.enc")
	if _, err := os.Stat(encPath); err != nil {
		t.Errorf("encrypted file should exist: %v", err)
	}

	// Step 5: Load and verify the token data survived migration.
	loaded, err := te.LoadToken(group, plugin)
	if err != nil {
		t.Fatalf("LoadToken after migration: %v", err)
	}
	if loaded.AccessToken != "plain-access" {
		t.Errorf("AccessToken = %q, want %q", loaded.AccessToken, "plain-access")
	}
	if loaded.RefreshToken != "plain-refresh" {
		t.Errorf("RefreshToken = %q, want %q", loaded.RefreshToken, "plain-refresh")
	}

	// Step 6: Subsequent migration should be a no-op.
	if err := te.MigrateUnencryptedToken(group, plugin); err != nil {
		t.Fatalf("second MigrateUnencryptedToken should be no-op: %v", err)
	}
}

// --- Flow 4: Backup and restore ────────────────────────────────

// TestIntegration_BackupRestore_FullCycle exercises the complete
// backup/restore workflow: set credentials, backup, delete, restore,
// and verify credentials are accessible again.
func TestIntegration_BackupRestore_FullCycle(t *testing.T) {
	svc := newTestService(t)

	// Step 1: Set up credentials in multiple groups.
	if err := svc.GetMigrationState().MarkMigrated("jira"); err != nil {
		t.Fatalf("mark migrated jira: %v", err)
	}
	if err := svc.GetMigrationState().MarkMigrated("google"); err != nil {
		t.Fatalf("mark migrated google: %v", err)
	}

	creds := map[string]map[string]string{
		"jira":   {"JIRA_TOKEN": "jira-secret", "JIRA_URL": "https://jira.example.com"},
		"google": {"CLIENT_ID": "goog-id", "CLIENT_SECRET": "goog-secret"},
	}

	for group, keys := range creds {
		for k, v := range keys {
			group, k := group, k // shadow for closure capture
			if err := svc.Set(group, k, v, false); err != nil {
				t.Fatalf("Set %s/%s: %v", group, k, err)
			}
			t.Cleanup(func() { _ = svc.keyringStore.Delete(group, k) })
		}
	}

	// Step 2: Create backup.
	backupPath := filepath.Join(t.TempDir(), "backup.enc")
	if err := svc.CreateBackup([]byte("test-password-123"), backupPath); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Step 3: Verify backup file was created.
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if info.Size() == 0 {
		t.Error("backup file is empty")
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("backup permissions = %04o, want 0600", info.Mode().Perm())
	}

	// Step 4: Delete all credentials from the keyring (simulating
	// data loss or fresh install on a new machine).
	for group, keys := range creds {
		for k := range keys {
			_ = svc.keyringStore.Delete(group, k)
		}
	}

	// Verify they are gone from the keyring.
	_, krErr := svc.keyringStore.Get("jira", "JIRA_TOKEN")
	if !errors.Is(krErr, ErrCredentialNotFound) {
		t.Fatalf("keyring should be empty after deletion, got %v", krErr)
	}

	// Step 5: Create a new service and restore from backup.
	svc2 := newTestService(t)

	if err := svc2.RestoreBackup(backupPath, []byte("test-password-123"), false); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	for group, keys := range creds {
		for k := range keys {
			group, k := group, k // shadow for closure capture
			t.Cleanup(func() { _ = svc2.keyringStore.Delete(group, k) })
		}
	}

	// Step 6: Verify all credentials are accessible.
	for group, keys := range creds {
		for k, v := range keys {
			got, err := svc2.keyringStore.Get(group, k)
			if err != nil {
				t.Fatalf("after restore: get %s/%s: %v", group, k, err)
			}
			if got != v {
				t.Errorf("after restore: %s/%s = %q, want %q", group, k, got, v)
			}
		}
	}

	// Step 7: Verify migration state was restored.
	if !svc2.GetMigrationState().IsMigrated("jira") {
		t.Error("jira should be migrated after restore")
	}
	if !svc2.GetMigrationState().IsMigrated("google") {
		t.Error("google should be migrated after restore")
	}

	// Step 8: Verify key tracking was restored.
	jiraKeys := svc2.GetMigrationState().GetGroupKeys("jira")
	if len(jiraKeys) != 2 {
		t.Errorf("jira tracked keys = %v, want 2 keys", jiraKeys)
	}
}

// TestIntegration_BackupRestore_WrongPassword verifies that
// restoring with the wrong password fails safely without
// corrupting any state.
func TestIntegration_BackupRestore_WrongPassword(t *testing.T) {
	svc := newTestService(t)

	if err := svc.GetMigrationState().MarkMigrated("wrongpw-group"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}
	if err := svc.Set("wrongpw-group", "SECRET", "value", false); err != nil {
		t.Fatalf("set: %v", err)
	}
	t.Cleanup(func() { _ = svc.keyringStore.Delete("wrongpw-group", "SECRET") })

	backupPath := filepath.Join(t.TempDir(), "backup.enc")
	if err := svc.CreateBackup([]byte("correct-pw"), backupPath); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Delete the key from keyring so we can verify restore failure.
	_ = svc.keyringStore.Delete("wrongpw-group", "SECRET")

	// Attempt restore with wrong password.
	svc2 := newTestService(t)
	err := svc2.RestoreBackup(backupPath, []byte("wrong-pw"), false)
	if err == nil {
		t.Fatal("expected error for wrong password")
	}

	// Verify no partial state was written.
	if svc2.GetMigrationState().IsMigrated("wrongpw-group") {
		t.Error("wrongpw-group should not be migrated after failed restore")
	}
	_, krErr := svc2.keyringStore.Get("wrongpw-group", "SECRET")
	if !errors.Is(krErr, ErrCredentialNotFound) {
		t.Errorf("after failed restore: keyring should not have the key, got %v", krErr)
	}
}

// TestIntegration_BackupRestore_MergeMode verifies that merge mode
// preserves existing credentials while importing missing ones.
func TestIntegration_BackupRestore_MergeMode(t *testing.T) {
	// Source service.
	svc1 := newTestService(t)
	if err := svc1.GetMigrationState().MarkMigrated("app"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}
	if err := svc1.Set("app", "KEY_A", "backup-a", false); err != nil {
		t.Fatalf("set KEY_A: %v", err)
	}
	t.Cleanup(func() { _ = svc1.keyringStore.Delete("app", "KEY_A") })
	if err := svc1.Set("app", "KEY_B", "backup-b", false); err != nil {
		t.Fatalf("set KEY_B: %v", err)
	}
	t.Cleanup(func() { _ = svc1.keyringStore.Delete("app", "KEY_B") })
	if err := svc1.Set("app", "KEY_C", "backup-c", false); err != nil {
		t.Fatalf("set KEY_C: %v", err)
	}
	t.Cleanup(func() { _ = svc1.keyringStore.Delete("app", "KEY_C") })

	backupPath := filepath.Join(t.TempDir(), "merge.enc")
	if err := svc1.CreateBackup([]byte("pw"), backupPath); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Target service with pre-existing KEY_A.
	svc2 := newTestService(t)
	if err := svc2.keyringStore.Set("app", "KEY_A", "existing-a"); err != nil {
		t.Fatalf("set existing: %v", err)
	}
	t.Cleanup(func() {
		_ = svc2.keyringStore.Delete("app", "KEY_A")
		_ = svc2.keyringStore.Delete("app", "KEY_B")
		_ = svc2.keyringStore.Delete("app", "KEY_C")
	})

	// Restore with merge mode.
	if err := svc2.RestoreBackup(backupPath, []byte("pw"), true); err != nil {
		t.Fatalf("RestoreBackup merge: %v", err)
	}

	// KEY_A should keep the existing value.
	got, err := svc2.keyringStore.Get("app", "KEY_A")
	if err != nil {
		t.Fatalf("get KEY_A: %v", err)
	}
	if got != "existing-a" {
		t.Errorf("KEY_A = %q, want %q (existing should be preserved)", got, "existing-a")
	}

	// KEY_B and KEY_C should be imported.
	got, err = svc2.keyringStore.Get("app", "KEY_B")
	if err != nil {
		t.Fatalf("get KEY_B: %v", err)
	}
	if got != "backup-b" {
		t.Errorf("KEY_B = %q, want %q", got, "backup-b")
	}

	got, err = svc2.keyringStore.Get("app", "KEY_C")
	if err != nil {
		t.Fatalf("get KEY_C: %v", err)
	}
	if got != "backup-c" {
		t.Errorf("KEY_C = %q, want %q", got, "backup-c")
	}
}

// --- Flow 5: Graceful degradation ──────────────────────────────

// TestIntegration_GracefulDegradation_FallbackToEnvD verifies that
// when a migrated group has no keyring entry for a key, the service
// falls back to env var (env.d is not checked for migrated groups).
func TestIntegration_GracefulDegradation_FallbackToEnvD(t *testing.T) {
	svc := newTestService(t)

	// Set up env.d and mark migrated, but do NOT store in keyring.
	writeEnvDFile(t, svc, "app", "TOKEN=envd-val\n")
	if err := svc.GetMigrationState().MarkMigrated("app"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}

	// For migrated groups, resolution is: keyring -> envvar.
	// env.d is NOT in the chain for migrated groups.
	// So TOKEN should not be found (not in keyring, not in env var).
	_, _, err := svc.Get("app", "TOKEN")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("migrated group with env.d only: expected ErrCredentialNotFound, got %v (value may have leaked from env.d)", err)
	}
}

// TestIntegration_GracefulDegradation_FallbackToEnvVar verifies that
// when keyring and env.d don't have a credential, environment
// variables are used as a last resort.
func TestIntegration_GracefulDegradation_FallbackToEnvVar(t *testing.T) {
	svc := newTestService(t)

	// Set environment variable.
	t.Setenv("MY_SECRET", "from-env")

	// Not in keyring, not in env.d, but in environment.
	val, src, err := svc.Get("app", "MY_SECRET")
	if err != nil {
		t.Fatalf("Get MY_SECRET: %v", err)
	}
	if val != "from-env" || src != SourceEnvVar {
		t.Errorf("MY_SECRET: got (%q, %v), want (%q, %v)", val, src, "from-env", SourceEnvVar)
	}
}

// TestIntegration_GracefulDegradation_NonMigratedChain verifies
// the full fallback chain for non-migrated groups:
// env.d -> keyring -> envvar
func TestIntegration_GracefulDegradation_NonMigratedChain(t *testing.T) {
	svc := newTestService(t)

	// Test env.d as primary source (non-migrated).
	writeEnvDFile(t, svc, "app", "ENVD_KEY=envd-val\n")
	val, src, err := svc.Get("app", "ENVD_KEY")
	if err != nil {
		t.Fatalf("Get ENVD_KEY: %v", err)
	}
	if val != "envd-val" || src != SourceEnvD {
		t.Errorf("ENVD_KEY: got (%q, %v), want (%q, %v)", val, src, "envd-val", SourceEnvD)
	}

	// Test keyring fallback (key not in env.d).
	if err := svc.keyringStore.Set("app", "KR_KEY", "kr-val"); err != nil {
		t.Fatalf("keyring set: %v", err)
	}
	t.Cleanup(func() { _ = svc.keyringStore.Delete("app", "KR_KEY") })

	val, src, err = svc.Get("app", "KR_KEY")
	if err != nil {
		t.Fatalf("Get KR_KEY: %v", err)
	}
	if val != "kr-val" || src != SourceKeyring {
		t.Errorf("KR_KEY: got (%q, %v), want (%q, %v)", val, src, "kr-val", SourceKeyring)
	}

	// Test envvar fallback (key not in env.d or keyring).
	t.Setenv("ENV_KEY", "env-val")
	val, src, err = svc.Get("app", "ENV_KEY")
	if err != nil {
		t.Fatalf("Get ENV_KEY: %v", err)
	}
	if val != "env-val" || src != SourceEnvVar {
		t.Errorf("ENV_KEY: got (%q, %v), want (%q, %v)", val, src, "env-val", SourceEnvVar)
	}

	// Test not found.
	_, _, err = svc.Get("app", "MISSING")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("MISSING: expected ErrCredentialNotFound, got %v", err)
	}
}

// --- Flow cross-cutting: GetAll integration ────────────────────

// TestIntegration_GetAll_NonMigratedEnvD verifies GetAll returns
// env.d vars for a non-migrated group.
func TestIntegration_GetAll_NonMigratedEnvD(t *testing.T) {
	svc := newTestService(t)

	writeEnvDFile(t, svc, "jira", "JIRA_TOKEN=tok1\nJIRA_URL=https://jira.example.com\n")

	vars := svc.GetAll("jira")
	if len(vars) != 2 {
		t.Fatalf("GetAll returned %d vars, want 2", len(vars))
	}
	if vars["JIRA_TOKEN"] != "tok1" {
		t.Errorf("JIRA_TOKEN = %q, want %q", vars["JIRA_TOKEN"], "tok1")
	}
	if vars["JIRA_URL"] != "https://jira.example.com" {
		t.Errorf("JIRA_URL = %q, want %q", vars["JIRA_URL"], "https://jira.example.com")
	}
}

// TestIntegration_GetAll_MigratedGroupOverlay verifies GetAll
// overlays keyring values for migrated groups on top of env.d.
func TestIntegration_GetAll_MigratedGroupOverlay(t *testing.T) {
	svc := newTestService(t)

	// Write env.d file with two keys.
	writeEnvDFile(t, svc, "google", "CLIENT_ID=envd-id\nCLIENT_SECRET=envd-secret\n")

	// Store one key in keyring and mark migrated.
	if err := svc.keyringStore.Set("google", "CLIENT_SECRET", "kr-secret"); err != nil {
		t.Fatalf("keyring set: %v", err)
	}
	t.Cleanup(func() { _ = svc.keyringStore.Delete("google", "CLIENT_SECRET") })

	if err := svc.migrationState.MarkMigrated("google"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}
	if err := svc.migrationState.TrackKey("google", "CLIENT_SECRET"); err != nil {
		t.Fatalf("track key: %v", err)
	}

	vars := svc.GetAll("google")

	// env.d CLIENT_ID should be present.
	if vars["CLIENT_ID"] != "envd-id" {
		t.Errorf("CLIENT_ID = %q, want %q", vars["CLIENT_ID"], "envd-id")
	}

	// Keyring CLIENT_SECRET should overlay env.d value.
	if vars["CLIENT_SECRET"] != "kr-secret" {
		t.Errorf("CLIENT_SECRET = %q, want %q (keyring should overlay env.d)", vars["CLIENT_SECRET"], "kr-secret")
	}
}

// TestIntegration_GetAll_NonexistentGroup verifies GetAll returns
// empty map for a nonexistent group.
func TestIntegration_GetAll_NonexistentGroup(t *testing.T) {
	svc := newTestService(t)

	vars := svc.GetAll("nonexistent")
	if len(vars) != 0 {
		t.Errorf("GetAll returned %d vars for nonexistent group, want 0", len(vars))
	}
}

// TestIntegration_GetAll_ExcludesEnvironmentVariables verifies
// GetAll does not include environment variables (they cannot be
// enumerated).
func TestIntegration_GetAll_ExcludesEnvironmentVariables(t *testing.T) {
	svc := newTestService(t)

	const envKey = "GETALL_TEST_ENVVAR"
	t.Setenv(envKey, "env-value")

	writeEnvDFile(t, svc, "myapp", "FROM_ENVD=envd-value\n")

	vars := svc.GetAll("myapp")
	if len(vars) != 1 {
		t.Errorf("GetAll returned %d vars, want 1 (env.d only)", len(vars))
	}
	if vars["FROM_ENVD"] != "envd-value" {
		t.Errorf("FROM_ENVD = %q, want %q", vars["FROM_ENVD"], "envd-value")
	}
	if _, ok := vars[envKey]; ok {
		t.Errorf("GetAll should not include environment variable %q", envKey)
	}

	// But Get() should resolve the env var individually.
	value, source, err := svc.Get("myapp", envKey)
	if err != nil {
		t.Fatalf("Get(%q) should resolve env var: %v", envKey, err)
	}
	if value != "env-value" {
		t.Errorf("Get(%q) = %q, want %q", envKey, value, "env-value")
	}
	if source != SourceEnvVar {
		t.Errorf("source = %v, want %v", source, SourceEnvVar)
	}
}

// --- Flow cross-cutting: IsKeyringAvailable ────────────────────

func TestIntegration_IsKeyringAvailable(t *testing.T) {
	svc := newTestService(t)

	if !svc.IsKeyringAvailable() {
		t.Error("IsKeyringAvailable() = false with mock keyring, want true")
	}
}

// TestIntegration_KeyringStoreAccessor verifies the accessor.
func TestIntegration_KeyringStoreAccessor(t *testing.T) {
	svc := newTestService(t)

	if svc.KeyringStore() == nil {
		t.Error("KeyringStore() returned nil")
	}
}

// --- Flow cross-cutting: Delete operations ─────────────────────

// TestIntegration_Delete_FromKeyringAndEnvD tests that Delete
// removes from all available stores.
func TestIntegration_Delete_FromKeyringAndEnvD(t *testing.T) {
	svc := newTestService(t)

	// Put same key in both stores.
	writeEnvDFile(t, svc, "app", "TOKEN=envd-val\n")
	if err := svc.keyringStore.Set("app", "TOKEN", "kr-val"); err != nil {
		t.Fatalf("keyring set: %v", err)
	}
	if err := svc.migrationState.TrackKey("app", "TOKEN"); err != nil {
		t.Fatalf("track key: %v", err)
	}

	// Delete from all stores.
	sources, err := svc.Delete("app", "TOKEN")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(sources) != 2 {
		t.Errorf("Delete returned %d sources, want 2 (keyring + env.d)", len(sources))
	}

	// Should no longer be accessible.
	_, _, err = svc.Get("app", "TOKEN")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("after delete: expected ErrCredentialNotFound, got %v", err)
	}
}

// --- Flow cross-cutting: Cache behaviour ───────────────────────

// TestIntegration_Cache_InvalidationOnDelete tests that the cache
// is properly invalidated when credentials are deleted.
func TestIntegration_Cache_InvalidationOnDelete(t *testing.T) {
	svc := newTestService(t)

	if err := svc.keyringStore.Set("app", "KEY", "cached"); err != nil {
		t.Fatalf("keyring set: %v", err)
	}
	t.Cleanup(func() { _ = svc.keyringStore.Delete("app", "KEY") })

	// Populate cache.
	v1, _, err := svc.Get("app", "KEY")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v1 != "cached" {
		t.Errorf("v1 = %q, want %q", v1, "cached")
	}

	// Delete should invalidate cache.
	if _, delErr := svc.Delete("app", "KEY"); delErr != nil {
		t.Fatalf("Delete: %v", delErr)
	}

	// Next Get should not return the cached value.
	_, _, err = svc.Get("app", "KEY")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("after delete: expected ErrCredentialNotFound, got %v", err)
	}
}

// --- Flow cross-cutting: Credential service resolution chain ───

// TestIntegration_FullResolutionChain tests all sources in one shot.
func TestIntegration_FullResolutionChain(t *testing.T) {
	svc := newTestService(t)

	// Set up env.d, keyring, and envvar for different keys in same group.
	writeEnvDFile(t, svc, "myapp", "FROM_ENVD=envd-value\n")

	if err := svc.keyringStore.Set("myapp", "FROM_KEYRING", "kr-value"); err != nil {
		t.Fatalf("keyring set: %v", err)
	}
	t.Cleanup(func() { _ = svc.keyringStore.Delete("myapp", "FROM_KEYRING") })

	const envKey = "FROM_ENVVAR_TEST_UNIQUE"
	t.Setenv(envKey, "envvar-value")

	// Non-migrated: env.d -> keyring -> envvar
	v1, s1, err := svc.Get("myapp", "FROM_ENVD")
	if err != nil {
		t.Fatalf("Get FROM_ENVD: %v", err)
	}
	if v1 != "envd-value" || s1 != SourceEnvD {
		t.Errorf("FROM_ENVD: value=%q source=%v, want envd-value/env.d", v1, s1)
	}

	v2, s2, err := svc.Get("myapp", "FROM_KEYRING")
	if err != nil {
		t.Fatalf("Get FROM_KEYRING: %v", err)
	}
	if v2 != "kr-value" || s2 != SourceKeyring {
		t.Errorf("FROM_KEYRING: value=%q source=%v, want kr-value/keyring", v2, s2)
	}

	v3, s3, err := svc.Get("myapp", envKey)
	if err != nil {
		t.Fatalf("Get %s: %v", envKey, err)
	}
	if v3 != "envvar-value" || s3 != SourceEnvVar {
		t.Errorf("%s: value=%q source=%v, want envvar-value/envvar", envKey, v3, s3)
	}

	// Not found.
	_, _, err = svc.Get("myapp", "NONEXISTENT")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("NONEXISTENT: err=%v, want ErrCredentialNotFound", err)
	}
}

// --- Flow cross-cutting: Migrated group credential precedence ──

// TestIntegration_MigratedPrecedence tests that for migrated groups,
// keyring takes precedence over env.d.
func TestIntegration_MigratedPrecedence(t *testing.T) {
	svc := newTestService(t)

	// Put same key in both env.d and keyring.
	writeEnvDFile(t, svc, "google", "TOKEN=envd-loses\n")

	if err := svc.keyringStore.Set("google", "TOKEN", "kr-wins"); err != nil {
		t.Fatalf("keyring set: %v", err)
	}
	t.Cleanup(func() { _ = svc.keyringStore.Delete("google", "TOKEN") })

	if err := svc.migrationState.MarkMigrated("google"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}

	// For migrated group, keyring should take precedence.
	value, source, err := svc.Get("google", "TOKEN")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if value != "kr-wins" {
		t.Errorf("value = %q, want %q (keyring should win for migrated)", value, "kr-wins")
	}
	if source != SourceKeyring {
		t.Errorf("source = %v, want %v", source, SourceKeyring)
	}
}

// --- Flow cross-cutting: Token encryption round-trip ───────────

// TestIntegration_TokenEncryption_SaveLoadRoundTrip is a quick
// round-trip test for token encryption via the service layer.
func TestIntegration_TokenEncryption_SaveLoadRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewKeyringStore()
	te := NewTokenEncryption(store, nil, tmpDir)

	token := &oauth2.Token{
		AccessToken:  "access-123",
		RefreshToken: "refresh-456",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour).Truncate(time.Second),
	}

	if err := te.SaveToken("google", "calendar", token); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Delete("google", "__encryption_key")
		_ = store.Delete("google", "__oauth_access_calendar")
	})

	loaded, err := te.LoadToken("google", "calendar")
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}

	if loaded.AccessToken != token.AccessToken {
		t.Errorf("AccessToken = %q, want %q", loaded.AccessToken, token.AccessToken)
	}
	if loaded.RefreshToken != token.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", loaded.RefreshToken, token.RefreshToken)
	}
	if loaded.TokenType != token.TokenType {
		t.Errorf("TokenType = %q, want %q", loaded.TokenType, token.TokenType)
	}
	if !loaded.Expiry.Equal(token.Expiry) {
		t.Errorf("Expiry = %v, want %v", loaded.Expiry, token.Expiry)
	}
}

// --- Flow cross-cutting: Service init with empty dirs ──────────

// TestIntegration_NewService_EmptyDirs verifies that the service
// initialises correctly with empty directories.
func TestIntegration_NewService_EmptyDirs(t *testing.T) {
	tmpDir := t.TempDir()
	envdDir := filepath.Join(tmpDir, "env.d")
	if err := os.MkdirAll(envdDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	migPath := filepath.Join(tmpDir, "migration.yaml")

	svc, err := NewService(envdDir, migPath)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	vars := svc.GetAll("empty")
	if len(vars) != 0 {
		t.Errorf("GetAll returned %d vars for empty, want 0", len(vars))
	}

	if !svc.IsKeyringAvailable() {
		t.Error("IsKeyringAvailable should be true with mock")
	}
}

// --- Flow cross-cutting: Token encryption with multiple plugins ─

// TestIntegration_TokenEncryption_MultiplePluginsSameGroup tests
// token isolation for multiple plugins in the same group.
func TestIntegration_TokenEncryption_MultiplePluginsSameGroup(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewKeyringStore()
	te := NewTokenEncryption(store, nil, tmpDir)

	token1 := &oauth2.Token{
		AccessToken:  "access-calendar",
		RefreshToken: "refresh-calendar",
		TokenType:    "Bearer",
	}
	token2 := &oauth2.Token{
		AccessToken:  "access-drive",
		RefreshToken: "refresh-drive",
		TokenType:    "Bearer",
	}

	if err := te.SaveToken("google", "calendar", token1); err != nil {
		t.Fatalf("SaveToken calendar: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Delete("google", "__encryption_key")
		_ = store.Delete("google", "__oauth_access_calendar")
	})
	if err := te.SaveToken("google", "drive", token2); err != nil {
		t.Fatalf("SaveToken drive: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Delete("google", "__oauth_access_drive")
	})

	loaded1, err := te.LoadToken("google", "calendar")
	if err != nil {
		t.Fatalf("LoadToken calendar: %v", err)
	}
	loaded2, err := te.LoadToken("google", "drive")
	if err != nil {
		t.Fatalf("LoadToken drive: %v", err)
	}

	if loaded1.AccessToken != "access-calendar" {
		t.Errorf("calendar AccessToken = %q, want %q", loaded1.AccessToken, "access-calendar")
	}
	if loaded2.AccessToken != "access-drive" {
		t.Errorf("drive AccessToken = %q, want %q", loaded2.AccessToken, "access-drive")
	}
	if loaded1.RefreshToken != "refresh-calendar" {
		t.Errorf("calendar RefreshToken = %q, want %q", loaded1.RefreshToken, "refresh-calendar")
	}
	if loaded2.RefreshToken != "refresh-drive" {
		t.Errorf("drive RefreshToken = %q, want %q", loaded2.RefreshToken, "refresh-drive")
	}
}

// --- Flow: GroupInfo and ListGroups ─────────────────────────────

// TestIntegration_GroupInfo_CombinesAllSources verifies GroupInfo
// returns correct data from both env.d and keyring sources.
func TestIntegration_GroupInfo_CombinesAllSources(t *testing.T) {
	svc := newTestService(t)

	writeEnvDFile(t, svc, "app", "ENVD_KEY=val\n")

	if err := svc.Set("app", "KR_KEY", "val", true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	t.Cleanup(func() { _ = svc.keyringStore.Delete("app", "KR_KEY") })

	info := svc.GetGroupInfo("app")
	if info.Name != "app" {
		t.Errorf("Name = %q, want %q", info.Name, "app")
	}
	if len(info.EnvDKeys) != 1 || info.EnvDKeys[0] != "ENVD_KEY" {
		t.Errorf("EnvDKeys = %v, want [ENVD_KEY]", info.EnvDKeys)
	}
	if len(info.KeyringKeys) != 1 || info.KeyringKeys[0] != "KR_KEY" {
		t.Errorf("KeyringKeys = %v, want [KR_KEY]", info.KeyringKeys)
	}
}

// TestIntegration_ListGroups_UnionOfSources verifies ListGroups
// returns the union of env.d groups and migrated groups.
func TestIntegration_ListGroups_UnionOfSources(t *testing.T) {
	svc := newTestService(t)

	// Create env.d group.
	writeEnvDFile(t, svc, "envd-only", "KEY=val\n")

	// Create keyring-only group.
	if err := svc.GetMigrationState().MarkMigrated("kr-only"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}

	groups := svc.ListGroups()
	hasEnvD := false
	hasKR := false
	for _, g := range groups {
		if g == "envd-only" {
			hasEnvD = true
		}
		if g == "kr-only" {
			hasKR = true
		}
	}

	if !hasEnvD {
		t.Error("ListGroups should include env.d-only group")
	}
	if !hasKR {
		t.Error("ListGroups should include keyring-only (migrated) group")
	}
}
