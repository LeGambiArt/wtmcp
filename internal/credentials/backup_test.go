package credentials

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Test: Round-trip backup and restore with merge mode ---

func TestBackupRestore_RoundTripMerge(t *testing.T) {
	svc := newTestService(t)

	// Set up credentials.
	if err := svc.migrationState.MarkMigrated("google"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}
	if err := svc.keyringStore.Set("google", "CLIENT_ID", "goog-id"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := svc.migrationState.TrackKey("google", "CLIENT_ID"); err != nil {
		t.Fatalf("track: %v", err)
	}
	if err := svc.keyringStore.Set("google", "CLIENT_SECRET", "goog-secret"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := svc.migrationState.TrackKey("google", "CLIENT_SECRET"); err != nil {
		t.Fatalf("track: %v", err)
	}

	// Create backup.
	backupPath := filepath.Join(t.TempDir(), "backup.enc")
	if err := svc.CreateBackup([]byte("test-password"), backupPath); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Create a new service to restore into.
	svc2 := newTestService(t)

	if err := svc2.RestoreBackup(backupPath, []byte("test-password"), true); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	// Verify credentials were restored.
	val, err := svc2.keyringStore.Get("google", "CLIENT_ID")
	if err != nil {
		t.Fatalf("get CLIENT_ID: %v", err)
	}
	if val != "goog-id" {
		t.Errorf("CLIENT_ID = %q, want %q", val, "goog-id")
	}

	val, err = svc2.keyringStore.Get("google", "CLIENT_SECRET")
	if err != nil {
		t.Fatalf("get CLIENT_SECRET: %v", err)
	}
	if val != "goog-secret" {
		t.Errorf("CLIENT_SECRET = %q, want %q", val, "goog-secret")
	}

	// Verify migration state was restored.
	if !svc2.migrationState.IsMigrated("google") {
		t.Error("google should be migrated after restore")
	}

	// Verify keys are tracked.
	keys := svc2.migrationState.GetGroupKeys("google")
	if len(keys) != 2 {
		t.Errorf("tracked keys = %v, want 2 keys", keys)
	}
}

// --- Test: Round-trip backup and restore with overwrite mode ---

func TestBackupRestore_RoundTripOverwrite(t *testing.T) {
	svc := newTestService(t)

	// Set up credentials.
	if err := svc.migrationState.MarkMigrated("jira"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}
	if err := svc.keyringStore.Set("jira", "JIRA_URL", "https://jira.example.com"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := svc.migrationState.TrackKey("jira", "JIRA_URL"); err != nil {
		t.Fatalf("track: %v", err)
	}
	if err := svc.keyringStore.Set("jira", "JIRA_TOKEN", "jira-token-123"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := svc.migrationState.TrackKey("jira", "JIRA_TOKEN"); err != nil {
		t.Fatalf("track: %v", err)
	}

	// Create backup.
	backupPath := filepath.Join(t.TempDir(), "backup.enc")
	if err := svc.CreateBackup([]byte("password123"), backupPath); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Create a new service with existing data.
	svc2 := newTestService(t)
	if err := svc2.keyringStore.Set("jira", "JIRA_URL", "https://old.example.com"); err != nil {
		t.Fatalf("set existing: %v", err)
	}

	// Restore with overwrite.
	if err := svc2.RestoreBackup(backupPath, []byte("password123"), false); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	// Existing value should be overwritten.
	val, err := svc2.keyringStore.Get("jira", "JIRA_URL")
	if err != nil {
		t.Fatalf("get JIRA_URL: %v", err)
	}
	if val != "https://jira.example.com" {
		t.Errorf("JIRA_URL = %q, want %q", val, "https://jira.example.com")
	}
}

// --- Test: Merge mode skips existing keys ---

func TestBackupRestore_MergeSkipsExisting(t *testing.T) {
	svc := newTestService(t)

	// Set up credentials in source.
	if err := svc.migrationState.MarkMigrated("google"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}
	if err := svc.keyringStore.Set("google", "KEY_A", "backup-value-a"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := svc.migrationState.TrackKey("google", "KEY_A"); err != nil {
		t.Fatalf("track: %v", err)
	}
	if err := svc.keyringStore.Set("google", "KEY_B", "backup-value-b"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := svc.migrationState.TrackKey("google", "KEY_B"); err != nil {
		t.Fatalf("track: %v", err)
	}

	// Create backup.
	backupPath := filepath.Join(t.TempDir(), "backup.enc")
	if err := svc.CreateBackup([]byte("pw"), backupPath); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Create target with one existing key.
	svc2 := newTestService(t)
	if err := svc2.keyringStore.Set("google", "KEY_A", "existing-value-a"); err != nil {
		t.Fatalf("set existing: %v", err)
	}

	// Restore with merge.
	if err := svc2.RestoreBackup(backupPath, []byte("pw"), true); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	// KEY_A should keep existing value.
	val, err := svc2.keyringStore.Get("google", "KEY_A")
	if err != nil {
		t.Fatalf("get KEY_A: %v", err)
	}
	if val != "existing-value-a" {
		t.Errorf("KEY_A = %q, want %q (existing should be preserved)", val, "existing-value-a")
	}

	// KEY_B should be imported.
	val, err = svc2.keyringStore.Get("google", "KEY_B")
	if err != nil {
		t.Fatalf("get KEY_B: %v", err)
	}
	if val != "backup-value-b" {
		t.Errorf("KEY_B = %q, want %q", val, "backup-value-b")
	}
}

// --- Test: Overwrite mode replaces existing keys ---

func TestBackupRestore_OverwriteReplacesExisting(t *testing.T) {
	svc := newTestService(t)

	// Set up credentials in source.
	if err := svc.migrationState.MarkMigrated("google"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}
	if err := svc.keyringStore.Set("google", "TOKEN", "new-token"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := svc.migrationState.TrackKey("google", "TOKEN"); err != nil {
		t.Fatalf("track: %v", err)
	}

	// Create backup.
	backupPath := filepath.Join(t.TempDir(), "backup.enc")
	if err := svc.CreateBackup([]byte("pw"), backupPath); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Create target with existing key.
	svc2 := newTestService(t)
	if err := svc2.keyringStore.Set("google", "TOKEN", "old-token"); err != nil {
		t.Fatalf("set existing: %v", err)
	}

	// Restore with overwrite (mergeMode=false).
	if err := svc2.RestoreBackup(backupPath, []byte("pw"), false); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	// Existing value should be overwritten.
	val, err := svc2.keyringStore.Get("google", "TOKEN")
	if err != nil {
		t.Fatalf("get TOKEN: %v", err)
	}
	if val != "new-token" {
		t.Errorf("TOKEN = %q, want %q", val, "new-token")
	}
}

// --- Test: Backup includes migration state ---

func TestBackupRestore_IncludesMigrationState(t *testing.T) {
	svc := newTestService(t)

	// Mark multiple groups as migrated.
	for _, group := range []string{"google", "jira", "slack"} {
		if err := svc.migrationState.MarkMigrated(group); err != nil {
			t.Fatalf("mark migrated %s: %v", group, err)
		}
	}

	// Create backup.
	backupPath := filepath.Join(t.TempDir(), "backup.enc")
	if err := svc.CreateBackup([]byte("pw"), backupPath); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Restore to new service.
	svc2 := newTestService(t)
	if err := svc2.RestoreBackup(backupPath, []byte("pw"), false); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	// Verify all groups are migrated.
	for _, group := range []string{"google", "jira", "slack"} {
		if !svc2.migrationState.IsMigrated(group) {
			t.Errorf("%s should be migrated after restore", group)
		}
	}
}

// --- Test: Invalid password fails decryption ---

func TestBackupRestore_InvalidPassword(t *testing.T) {
	svc := newTestService(t)

	if err := svc.migrationState.MarkMigrated("google"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}
	if err := svc.keyringStore.Set("google", "KEY", "value"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := svc.migrationState.TrackKey("google", "KEY"); err != nil {
		t.Fatalf("track: %v", err)
	}

	backupPath := filepath.Join(t.TempDir(), "backup.enc")
	if err := svc.CreateBackup([]byte("correct-password"), backupPath); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	svc2 := newTestService(t)
	err := svc2.RestoreBackup(backupPath, []byte("wrong-password"), false)
	if err == nil {
		t.Fatal("expected error for wrong password")
	}
}

// --- Test: Corrupted backup file fails ---

func TestBackupRestore_CorruptedFile(t *testing.T) {
	svc := newTestService(t)

	backupPath := filepath.Join(t.TempDir(), "corrupted.enc")
	if err := os.WriteFile(backupPath, []byte("not valid json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := svc.RestoreBackup(backupPath, []byte("password"), false)
	if err == nil {
		t.Fatal("expected error for corrupted file")
	}
}

// --- Test: Tampered ciphertext fails GCM auth ---

func TestBackupRestore_TamperedCiphertext(t *testing.T) {
	svc := newTestService(t)

	if err := svc.migrationState.MarkMigrated("google"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}
	if err := svc.keyringStore.Set("google", "KEY", "value"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := svc.migrationState.TrackKey("google", "KEY"); err != nil {
		t.Fatalf("track: %v", err)
	}

	backupPath := filepath.Join(t.TempDir(), "backup.enc")
	if err := svc.CreateBackup([]byte("password"), backupPath); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Read and tamper with the ciphertext.
	data, err := os.ReadFile(backupPath) //nolint:gosec // test file path
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var backup BackupFile
	if err := json.Unmarshal(data, &backup); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Decode, tamper, re-encode.
	ct, err := base64.StdEncoding.DecodeString(backup.Ciphertext)
	if err != nil {
		t.Fatalf("decode ciphertext: %v", err)
	}
	if len(ct) > 0 {
		ct[0] ^= 0xFF // Flip bits.
	}
	backup.Ciphertext = base64.StdEncoding.EncodeToString(ct)

	tamperedData, err := json.Marshal(backup)
	if err != nil {
		t.Fatalf("marshal tampered: %v", err)
	}

	tamperedPath := filepath.Join(t.TempDir(), "tampered.enc")
	if err := os.WriteFile(tamperedPath, tamperedData, 0o600); err != nil {
		t.Fatalf("write tampered: %v", err)
	}

	svc2 := newTestService(t)
	err = svc2.RestoreBackup(tamperedPath, []byte("password"), false)
	if err == nil {
		t.Fatal("expected error for tampered ciphertext")
	}
}

// --- Test: Empty credentials backup/restore ---

func TestBackupRestore_EmptyCredentials(t *testing.T) {
	svc := newTestService(t)

	// No credentials, no migrated groups.
	backupPath := filepath.Join(t.TempDir(), "empty.enc")
	if err := svc.CreateBackup([]byte("password"), backupPath); err != nil {
		t.Fatalf("CreateBackup empty: %v", err)
	}

	svc2 := newTestService(t)
	if err := svc2.RestoreBackup(backupPath, []byte("password"), false); err != nil {
		t.Fatalf("RestoreBackup empty: %v", err)
	}

	// No groups should be migrated.
	groups := svc2.migrationState.GetMigratedGroups()
	if len(groups) != 0 {
		t.Errorf("expected 0 migrated groups, got %v", groups)
	}
}

// --- Test: Backup file permissions are 0600 ---

func TestBackupRestore_FilePermissions(t *testing.T) {
	svc := newTestService(t)

	backupPath := filepath.Join(t.TempDir(), "perms.enc")
	if err := svc.CreateBackup([]byte("password"), backupPath); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("file permissions = %04o, want 0600", perm)
	}
}

// --- Test: Empty password is rejected ---

func TestBackupRestore_EmptyPassword(t *testing.T) {
	svc := newTestService(t)

	err := svc.CreateBackup([]byte(""), filepath.Join(t.TempDir(), "backup.enc"))
	if err == nil {
		t.Fatal("expected error for empty password in CreateBackup")
	}

	// Write a dummy file so RestoreBackup doesn't fail on file read.
	backupPath := filepath.Join(t.TempDir(), "dummy.enc")
	if err := os.WriteFile(backupPath, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	err = svc.RestoreBackup(backupPath, []byte(""), false)
	if err == nil {
		t.Fatal("expected error for empty password in RestoreBackup")
	}
}

// --- Test: Unsupported backup version ---

func TestBackupRestore_UnsupportedVersion(t *testing.T) {
	svc := newTestService(t)

	backup := BackupFile{
		Version:    99,
		CreatedAt:  "2026-04-29T10:00:00Z",
		KDF:        "argon2id",
		IV:         base64.StdEncoding.EncodeToString([]byte("testnonce123")),
		Ciphertext: base64.StdEncoding.EncodeToString([]byte("test")),
		Tag:        base64.StdEncoding.EncodeToString([]byte("tag1234567890123")),
		KDFParams: Argon2Params{
			Memory:      argon2Memory,
			Iterations:  argon2Iterations,
			Parallelism: argon2Parallelism,
			Salt:        base64.StdEncoding.EncodeToString([]byte("1234567890123456")),
		},
	}

	data, _ := json.Marshal(backup)
	backupPath := filepath.Join(t.TempDir(), "v99.enc")
	if err := os.WriteFile(backupPath, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := svc.RestoreBackup(backupPath, []byte("password"), false)
	if err == nil {
		t.Fatal("expected error for unsupported version")
	}
}

// --- Test: Backup with multiple groups ---

func TestBackupRestore_MultipleGroups(t *testing.T) {
	svc := newTestService(t)

	// Set up multiple groups.
	groups := map[string]map[string]string{
		"google": {"CLIENT_ID": "goog-id", "CLIENT_SECRET": "goog-secret"},
		"jira":   {"JIRA_URL": "https://jira.example.com", "JIRA_TOKEN": "token-123"},
	}

	for group, keys := range groups {
		if err := svc.migrationState.MarkMigrated(group); err != nil {
			t.Fatalf("mark migrated %s: %v", group, err)
		}
		for k, v := range keys {
			if err := svc.keyringStore.Set(group, k, v); err != nil {
				t.Fatalf("set %s/%s: %v", group, k, err)
			}
			if err := svc.migrationState.TrackKey(group, k); err != nil {
				t.Fatalf("track %s/%s: %v", group, k, err)
			}
		}
	}

	// Create backup.
	backupPath := filepath.Join(t.TempDir(), "multi.enc")
	if err := svc.CreateBackup([]byte("multi-pw"), backupPath); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Restore to new service.
	svc2 := newTestService(t)
	if err := svc2.RestoreBackup(backupPath, []byte("multi-pw"), false); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	// Verify all credentials.
	for group, keys := range groups {
		for k, v := range keys {
			got, err := svc2.keyringStore.Get(group, k)
			if err != nil {
				t.Fatalf("get %s/%s: %v", group, k, err)
			}
			if got != v {
				t.Errorf("%s/%s = %q, want %q", group, k, got, v)
			}
		}
	}
}

// --- Test: Nonexistent backup file ---

func TestBackupRestore_NonexistentFile(t *testing.T) {
	svc := newTestService(t)

	err := svc.RestoreBackup("/nonexistent/path/backup.enc", []byte("password"), false)
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

// --- Test: Default output path ---

func TestBackup_DefaultOutputPath(t *testing.T) {
	svc := newTestService(t)

	// CreateBackup with empty outputPath uses default path.
	// We can't easily test the default path without mocking os.UserHomeDir,
	// but we can verify it doesn't error.
	if err := svc.CreateBackup([]byte("password"), ""); err != nil {
		t.Fatalf("CreateBackup with default path: %v", err)
	}

	// Verify a backup file was created in the expected location.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	credDir := filepath.Join(homeDir, ".config", "wtmcp", "credentials")
	entries, err := os.ReadDir(credDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	found := false
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".enc" {
			found = true
			// Clean up.
			_ = os.Remove(filepath.Join(credDir, e.Name()))
			break
		}
	}
	if !found {
		t.Error("no .enc file found in default backup directory")
	}
}

// --- Test: deriveKey produces deterministic output ---

func TestDeriveKey_Deterministic(t *testing.T) {
	salt := []byte("1234567890123456")
	key1 := deriveKey([]byte("password"), salt, argon2Memory, argon2Iterations, argon2Parallelism)
	key2 := deriveKey([]byte("password"), salt, argon2Memory, argon2Iterations, argon2Parallelism)

	if len(key1) != argon2KeyLen {
		t.Errorf("key length = %d, want %d", len(key1), argon2KeyLen)
	}

	for i := range key1 {
		if key1[i] != key2[i] {
			t.Fatal("same password + salt should produce same key")
		}
	}
}

// --- Test: deriveKey different passwords produce different keys ---

func TestDeriveKey_DifferentPasswords(t *testing.T) {
	salt := []byte("1234567890123456")
	key1 := deriveKey([]byte("password1"), salt, argon2Memory, argon2Iterations, argon2Parallelism)
	key2 := deriveKey([]byte("password2"), salt, argon2Memory, argon2Iterations, argon2Parallelism)

	same := true
	for i := range key1 {
		if key1[i] != key2[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("different passwords should produce different keys")
	}
}

// --- Test: encrypt/decrypt round-trip ---

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	salt := []byte("1234567890123456")
	key := deriveKey([]byte("test-password"), salt, argon2Memory, argon2Iterations, argon2Parallelism)

	payload := &BackupPayload{
		CredentialGroups: map[string]map[string]string{
			"test": {"KEY": "VALUE"},
		},
		MigrationState: []string{"test"},
	}

	backup, err := encryptBackupPayload(key, salt, payload)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	decrypted, err := decryptBackupPayload(key, backup)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if len(decrypted.MigrationState) != 1 || decrypted.MigrationState[0] != "test" {
		t.Errorf("migration state = %v, want [test]", decrypted.MigrationState)
	}

	if val, ok := decrypted.CredentialGroups["test"]["KEY"]; !ok || val != "VALUE" {
		t.Errorf("credential = %q, want %q", val, "VALUE")
	}
}

// --- Test: MigrationState TrackKey and GetGroupKeys ---

func TestMigrationState_TrackKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.yaml")
	state, err := LoadMigrationState(path)
	if err != nil {
		t.Fatalf("LoadMigrationState: %v", err)
	}

	// Track keys for a group.
	if err := state.TrackKey("google", "CLIENT_ID"); err != nil {
		t.Fatalf("TrackKey: %v", err)
	}
	if err := state.TrackKey("google", "CLIENT_SECRET"); err != nil {
		t.Fatalf("TrackKey: %v", err)
	}

	// Duplicate tracking should be a no-op.
	if err := state.TrackKey("google", "CLIENT_ID"); err != nil {
		t.Fatalf("TrackKey duplicate: %v", err)
	}

	keys := state.GetGroupKeys("google")
	if len(keys) != 2 {
		t.Fatalf("GetGroupKeys = %v, want 2 keys", keys)
	}

	// Verify keys are persisted.
	reloaded, err := LoadMigrationState(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	reloadedKeys := reloaded.GetGroupKeys("google")
	if len(reloadedKeys) != 2 {
		t.Errorf("reloaded keys = %v, want 2 keys", reloadedKeys)
	}
}

// --- Test: MigrationState UntrackKey ---

func TestMigrationState_UntrackKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.yaml")
	state, err := LoadMigrationState(path)
	if err != nil {
		t.Fatalf("LoadMigrationState: %v", err)
	}

	if err := state.TrackKey("google", "KEY_A"); err != nil {
		t.Fatalf("TrackKey: %v", err)
	}
	if err := state.TrackKey("google", "KEY_B"); err != nil {
		t.Fatalf("TrackKey: %v", err)
	}

	// Untrack one key.
	if err := state.UntrackKey("google", "KEY_A"); err != nil {
		t.Fatalf("UntrackKey: %v", err)
	}

	keys := state.GetGroupKeys("google")
	if len(keys) != 1 || keys[0] != "KEY_B" {
		t.Errorf("keys after untrack = %v, want [KEY_B]", keys)
	}

	// Untrack the last key should remove the group entry.
	if err := state.UntrackKey("google", "KEY_B"); err != nil {
		t.Fatalf("UntrackKey last: %v", err)
	}

	keys = state.GetGroupKeys("google")
	if keys != nil {
		t.Errorf("keys after untrack all = %v, want nil", keys)
	}

	// Untrack from empty group (no-op).
	if err := state.UntrackKey("nonexistent", "KEY"); err != nil {
		t.Fatalf("UntrackKey nonexistent: %v", err)
	}
}

// --- Test: GetGroupKeys returns copy ---

func TestMigrationState_GetGroupKeysReturnsCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.yaml")
	state, err := LoadMigrationState(path)
	if err != nil {
		t.Fatalf("LoadMigrationState: %v", err)
	}

	if err := state.TrackKey("google", "KEY"); err != nil {
		t.Fatalf("TrackKey: %v", err)
	}

	keys := state.GetGroupKeys("google")
	keys[0] = "TAMPERED"

	// Internal state should be unaffected.
	actual := state.GetGroupKeys("google")
	if actual[0] != "KEY" {
		t.Error("internal state was modified by changing returned slice")
	}
}

// --- Test: Migrated group with no tracked keys backs up empty map ---

func TestBackup_MigratedGroupNoTrackedKeys(t *testing.T) {
	svc := newTestService(t)

	if err := svc.migrationState.MarkMigrated("empty-group"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}

	backupPath := filepath.Join(t.TempDir(), "backup.enc")
	if err := svc.CreateBackup([]byte("pw"), backupPath); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Restore and verify the group exists in migration state.
	svc2 := newTestService(t)
	if err := svc2.RestoreBackup(backupPath, []byte("pw"), false); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	if !svc2.migrationState.IsMigrated("empty-group") {
		t.Error("empty-group should be migrated after restore")
	}
}

// --- Test: Service.Set tracks keys ---

func TestServiceSet_TracksKeys(t *testing.T) {
	svc := newTestService(t)

	if err := svc.migrationState.MarkMigrated("google"); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}

	if err := svc.Set("google", "API_KEY", "secret", false); err != nil {
		t.Fatalf("Set: %v", err)
	}

	keys := svc.migrationState.GetGroupKeys("google")
	if len(keys) != 1 || keys[0] != "API_KEY" {
		t.Errorf("tracked keys = %v, want [API_KEY]", keys)
	}
}

// --- Test: Service.Set with forceKeyring also tracks keys ---

func TestServiceSet_ForceKeyringTracksKeys(t *testing.T) {
	svc := newTestService(t)

	if err := svc.Set("newgroup", "TOKEN", "value", true); err != nil {
		t.Fatalf("Set forceKeyring: %v", err)
	}

	keys := svc.migrationState.GetGroupKeys("newgroup")
	if len(keys) != 1 || keys[0] != "TOKEN" {
		t.Errorf("tracked keys = %v, want [TOKEN]", keys)
	}
}

// --- Test: validateKDFParams boundary values ---

func TestValidateKDFParams(t *testing.T) {
	tests := []struct {
		name    string
		params  Argon2Params
		wantErr bool
	}{
		{
			name:    "production defaults",
			params:  Argon2Params{Memory: argon2Memory, Iterations: argon2Iterations, Parallelism: argon2Parallelism},
			wantErr: false,
		},
		{
			name:    "minimum valid",
			params:  Argon2Params{Memory: 1, Iterations: 1, Parallelism: 1},
			wantErr: false,
		},
		{
			name:    "maximum valid",
			params:  Argon2Params{Memory: maxKDFMemory, Iterations: maxKDFIterations, Parallelism: maxKDFParallelism},
			wantErr: false,
		},
		{
			name:    "memory zero",
			params:  Argon2Params{Memory: 0, Iterations: 3, Parallelism: 4},
			wantErr: true,
		},
		{
			name:    "memory exceeds max",
			params:  Argon2Params{Memory: maxKDFMemory + 1, Iterations: 3, Parallelism: 4},
			wantErr: true,
		},
		{
			name:    "iterations zero",
			params:  Argon2Params{Memory: 65536, Iterations: 0, Parallelism: 4},
			wantErr: true,
		},
		{
			name:    "iterations exceeds max",
			params:  Argon2Params{Memory: 65536, Iterations: maxKDFIterations + 1, Parallelism: 4},
			wantErr: true,
		},
		{
			name:    "parallelism zero",
			params:  Argon2Params{Memory: 65536, Iterations: 3, Parallelism: 0},
			wantErr: true,
		},
		{
			name:    "parallelism exceeds max",
			params:  Argon2Params{Memory: 65536, Iterations: 3, Parallelism: maxKDFParallelism + 1},
			wantErr: true,
		},
		{
			name:    "all at max",
			params:  Argon2Params{Memory: maxKDFMemory, Iterations: maxKDFIterations, Parallelism: maxKDFParallelism},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateKDFParams(tt.params)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateKDFParams(%+v) error = %v, wantErr %v", tt.params, err, tt.wantErr)
			}
		})
	}
}

// --- Test: RestoreBackup rejects oversized files ---

func TestRestoreBackup_RejectsOversizedFile(t *testing.T) {
	svc := newTestService(t)

	// Create a file that exceeds maxBackupFileSize by writing the header.
	oversizedPath := filepath.Join(t.TempDir(), "huge.enc")
	f, err := os.Create(oversizedPath) //nolint:gosec // test file path
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Truncate to just over the limit without actually writing that much data.
	if err := f.Truncate(maxBackupFileSize + 1); err != nil {
		_ = f.Close()
		t.Fatalf("truncate: %v", err)
	}
	_ = f.Close()

	err = svc.RestoreBackup(oversizedPath, []byte("password"), false)
	if err == nil {
		t.Fatal("expected error for oversized file")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %q, want it to mention 'too large'", err)
	}
}
