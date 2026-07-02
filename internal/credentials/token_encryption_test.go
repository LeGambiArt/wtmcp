package credentials

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// newTestTokenEncryption creates a TokenEncryption wired to a temp
// directory. The mock keyring is initialised by the package-level
// init() in keyring_store_test.go.
func newTestTokenEncryption(t *testing.T) (*TokenEncryption, string) {
	t.Helper()
	tokensDir := filepath.Join(t.TempDir(), "tokens")
	if err := os.MkdirAll(tokensDir, 0o700); err != nil {
		t.Fatalf("create tokens dir: %v", err)
	}
	store := NewKeyringStore()
	te := NewTokenEncryption(store, nil, tokensDir)
	return te, tokensDir
}

// --- Test: SaveToken -> LoadToken round-trip with all token fields ---

func TestTokenEncryption_RoundTrip(t *testing.T) {
	te, _ := newTestTokenEncryption(t)

	group := "test-roundtrip"
	plugin := "calendar"
	expiry := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

	original := &oauth2.Token{
		AccessToken:  "access-token-abc123",
		RefreshToken: "refresh-token-xyz789",
		TokenType:    "Bearer",
		Expiry:       expiry,
	}

	// Save.
	if err := te.SaveToken(group, plugin, original); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	// Load.
	loaded, err := te.LoadToken(group, plugin)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}

	// Verify all fields.
	if loaded.AccessToken != original.AccessToken {
		t.Errorf("AccessToken = %q, want %q", loaded.AccessToken, original.AccessToken)
	}
	if loaded.RefreshToken != original.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", loaded.RefreshToken, original.RefreshToken)
	}
	if loaded.TokenType != original.TokenType {
		t.Errorf("TokenType = %q, want %q", loaded.TokenType, original.TokenType)
	}
	if !loaded.Expiry.Equal(original.Expiry) {
		t.Errorf("Expiry = %v, want %v", loaded.Expiry, original.Expiry)
	}

	// Cleanup keyring entries.
	t.Cleanup(func() {
		_ = te.keyringStore.Delete(group, "__encryption_key")
		_ = te.keyringStore.Delete(group, "__oauth_access_"+plugin)
	})
}

// --- Test: Encryption key generation on first use ---

func TestTokenEncryption_KeyGeneratedOnFirstUse(t *testing.T) {
	te, _ := newTestTokenEncryption(t)

	group := "test-keygen"

	// Key should not exist yet.
	_, err := te.keyringStore.Get(group, "__encryption_key")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("expected ErrCredentialNotFound before first save, got: %v", err)
	}

	// Save a token (triggers key generation).
	token := &oauth2.Token{
		AccessToken:  "access",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
	}
	if err := te.SaveToken(group, "test", token); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	// Key should now exist.
	encoded, err := te.keyringStore.Get(group, "__encryption_key")
	if err != nil {
		t.Fatalf("key not found after save: %v", err)
	}

	// Verify it's valid base64 encoding of a 32-byte key.
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("key is not valid base64: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("key length = %d, want 32", len(key))
	}

	// Cleanup.
	t.Cleanup(func() {
		_ = te.keyringStore.Delete(group, "__encryption_key")
		_ = te.keyringStore.Delete(group, "__oauth_access_test")
	})
}

// --- Test: Encryption key reuse on subsequent saves ---

func TestTokenEncryption_KeyReusedOnSubsequentSaves(t *testing.T) {
	te, _ := newTestTokenEncryption(t)

	group := "test-keyreuse"

	// First save creates the key.
	token1 := &oauth2.Token{
		AccessToken:  "access1",
		RefreshToken: "refresh1",
		TokenType:    "Bearer",
	}
	if err := te.SaveToken(group, "plugin1", token1); err != nil {
		t.Fatalf("SaveToken 1: %v", err)
	}

	// Record the key.
	key1, err := te.keyringStore.Get(group, "__encryption_key")
	if err != nil {
		t.Fatalf("get key after first save: %v", err)
	}

	// Second save should reuse the same key.
	token2 := &oauth2.Token{
		AccessToken:  "access2",
		RefreshToken: "refresh2",
		TokenType:    "Bearer",
	}
	if err := te.SaveToken(group, "plugin2", token2); err != nil {
		t.Fatalf("SaveToken 2: %v", err)
	}

	key2, err := te.keyringStore.Get(group, "__encryption_key")
	if err != nil {
		t.Fatalf("get key after second save: %v", err)
	}

	if key1 != key2 {
		t.Error("encryption key changed between saves; expected same key")
	}

	// Verify both tokens can be loaded.
	loaded1, err := te.LoadToken(group, "plugin1")
	if err != nil {
		t.Fatalf("LoadToken plugin1: %v", err)
	}
	if loaded1.RefreshToken != "refresh1" {
		t.Errorf("plugin1 RefreshToken = %q, want %q", loaded1.RefreshToken, "refresh1")
	}

	loaded2, err := te.LoadToken(group, "plugin2")
	if err != nil {
		t.Fatalf("LoadToken plugin2: %v", err)
	}
	if loaded2.RefreshToken != "refresh2" {
		t.Errorf("plugin2 RefreshToken = %q, want %q", loaded2.RefreshToken, "refresh2")
	}

	// Cleanup.
	t.Cleanup(func() {
		_ = te.keyringStore.Delete(group, "__encryption_key")
		_ = te.keyringStore.Delete(group, "__oauth_access_plugin1")
		_ = te.keyringStore.Delete(group, "__oauth_access_plugin2")
	})
}

// --- Test: MigrateUnencryptedToken from plaintext to encrypted ---

func TestTokenEncryption_MigrateUnencryptedToken(t *testing.T) {
	te, tokensDir := newTestTokenEncryption(t)

	group := "test-migrate"
	plugin := "gmail"

	// Create a plaintext token file.
	groupDir := filepath.Join(tokensDir, group)
	if err := os.MkdirAll(groupDir, 0o700); err != nil {
		t.Fatalf("create group dir: %v", err)
	}

	plaintextToken := oauth2.Token{
		AccessToken:  "plain-access",
		RefreshToken: "plain-refresh",
		TokenType:    "Bearer",
	}
	data, err := json.Marshal(plaintextToken) //nolint:gosec // test fixture
	if err != nil {
		t.Fatalf("marshal plaintext token: %v", err)
	}
	plaintextPath := filepath.Join(groupDir, "token-"+plugin+".json")
	if err := os.WriteFile(plaintextPath, data, 0o600); err != nil {
		t.Fatalf("write plaintext token: %v", err)
	}

	// Migrate.
	if err := te.MigrateUnencryptedToken(group, plugin); err != nil {
		t.Fatalf("MigrateUnencryptedToken: %v", err)
	}

	// Plaintext file should be deleted.
	if _, err := os.Stat(plaintextPath); !os.IsNotExist(err) {
		t.Error("plaintext file still exists after migration")
	}

	// Encrypted file should exist.
	encryptedPath := filepath.Join(groupDir, "token-"+plugin+".json.enc")
	if _, err := os.Stat(encryptedPath); err != nil {
		t.Errorf("encrypted file not found: %v", err)
	}

	// Load the migrated token.
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

	// Cleanup.
	t.Cleanup(func() {
		_ = te.keyringStore.Delete(group, "__encryption_key")
		_ = te.keyringStore.Delete(group, "__oauth_access_"+plugin)
	})
}

// --- Test: MigrateUnencryptedToken when already migrated (no-op) ---

func TestTokenEncryption_MigrateAlreadyMigrated(t *testing.T) {
	te, _ := newTestTokenEncryption(t)

	// No plaintext file exists. Should be a no-op.
	err := te.MigrateUnencryptedToken("nonexistent-group", "nonexistent-plugin")
	if err != nil {
		t.Errorf("MigrateUnencryptedToken for non-existent file: %v", err)
	}
}

// --- Test: Error - Missing encryption key on LoadToken ---

func TestTokenEncryption_LoadTokenMissingKey(t *testing.T) {
	te, tokensDir := newTestTokenEncryption(t)

	group := "test-nokey"
	plugin := "drive"

	// Create a fake encrypted file without storing an encryption key.
	groupDir := filepath.Join(tokensDir, group)
	if err := os.MkdirAll(groupDir, 0o700); err != nil {
		t.Fatalf("create group dir: %v", err)
	}
	fakeEnc := EncryptedTokenFile{
		Version:    1,
		IV:         base64.StdEncoding.EncodeToString([]byte("012345678901")),
		Ciphertext: base64.StdEncoding.EncodeToString([]byte("fake")),
		Tag:        base64.StdEncoding.EncodeToString([]byte("0123456789012345")),
	}
	data, _ := json.Marshal(fakeEnc)
	filePath := filepath.Join(groupDir, "token-"+plugin+".json.enc")
	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		t.Fatalf("write fake enc file: %v", err)
	}

	_, err := te.LoadToken(group, plugin)
	if err == nil {
		t.Fatal("expected error for missing encryption key, got nil")
	}
}

// --- Test: Error - Corrupted encrypted file ---

func TestTokenEncryption_LoadTokenCorruptedFile(t *testing.T) {
	te, tokensDir := newTestTokenEncryption(t)

	group := "test-corrupt"
	plugin := "sheets"

	// Store an encryption key.
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := te.keyringStore.Set(group, "__encryption_key", encoded); err != nil {
		t.Fatalf("set key: %v", err)
	}

	// Write a corrupted (non-JSON) file.
	groupDir := filepath.Join(tokensDir, group)
	if err := os.MkdirAll(groupDir, 0o700); err != nil {
		t.Fatalf("create group dir: %v", err)
	}
	filePath := filepath.Join(groupDir, "token-"+plugin+".json.enc")
	if err := os.WriteFile(filePath, []byte("not valid json"), 0o600); err != nil {
		t.Fatalf("write corrupted file: %v", err)
	}

	_, err := te.LoadToken(group, plugin)
	if err == nil {
		t.Fatal("expected error for corrupted file, got nil")
	}

	// Cleanup.
	t.Cleanup(func() {
		_ = te.keyringStore.Delete(group, "__encryption_key")
	})
}

// --- Test: Error - Invalid base64 in encrypted file ---

func TestTokenEncryption_LoadTokenInvalidBase64(t *testing.T) {
	te, tokensDir := newTestTokenEncryption(t)

	group := "test-base64"
	plugin := "docs"

	// Store an encryption key.
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := te.keyringStore.Set(group, "__encryption_key", encoded); err != nil {
		t.Fatalf("set key: %v", err)
	}

	// Write a file with invalid base64 in the IV field.
	groupDir := filepath.Join(tokensDir, group)
	if err := os.MkdirAll(groupDir, 0o700); err != nil {
		t.Fatalf("create group dir: %v", err)
	}
	badEnc := EncryptedTokenFile{
		Version:    1,
		IV:         "!!!not-base64!!!",
		Ciphertext: base64.StdEncoding.EncodeToString([]byte("fake")),
		Tag:        base64.StdEncoding.EncodeToString([]byte("0123456789012345")),
	}
	data, _ := json.Marshal(badEnc)
	filePath := filepath.Join(groupDir, "token-"+plugin+".json.enc")
	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		t.Fatalf("write bad base64 file: %v", err)
	}

	_, err := te.LoadToken(group, plugin)
	if err == nil {
		t.Fatal("expected error for invalid base64, got nil")
	}

	// Cleanup.
	t.Cleanup(func() {
		_ = te.keyringStore.Delete(group, "__encryption_key")
	})
}

// --- Test: Error - GCM authentication failure (tampered data) ---

func TestTokenEncryption_LoadTokenTamperedData(t *testing.T) {
	te, tokensDir := newTestTokenEncryption(t)

	group := "test-tamper"
	plugin := "meet"

	// Save a valid token.
	token := &oauth2.Token{
		AccessToken:  "valid-access",
		RefreshToken: "valid-refresh",
		TokenType:    "Bearer",
	}
	if err := te.SaveToken(group, plugin, token); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	// Tamper with the encrypted file (modify the ciphertext).
	filePath := filepath.Join(tokensDir, group, "token-"+plugin+".json.enc")
	data, err := os.ReadFile(filePath) //nolint:gosec // test file path
	if err != nil {
		t.Fatalf("read encrypted file: %v", err)
	}

	var encrypted EncryptedTokenFile
	if err := json.Unmarshal(data, &encrypted); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Decode ciphertext, flip a byte, re-encode.
	ct, _ := base64.StdEncoding.DecodeString(encrypted.Ciphertext)
	if len(ct) > 0 {
		ct[0] ^= 0xFF
	}
	encrypted.Ciphertext = base64.StdEncoding.EncodeToString(ct)

	tamperedData, _ := json.Marshal(encrypted)
	if err := os.WriteFile(filePath, tamperedData, 0o600); err != nil {
		t.Fatalf("write tampered file: %v", err)
	}

	// Loading should fail with GCM authentication error.
	_, err = te.LoadToken(group, plugin)
	if err == nil {
		t.Fatal("expected error for tampered data, got nil")
	}

	// Cleanup.
	t.Cleanup(func() {
		_ = te.keyringStore.Delete(group, "__encryption_key")
		_ = te.keyringStore.Delete(group, "__oauth_access_"+plugin)
	})
}

// --- Test: Token with all optional fields (scopes, etc.) ---

func TestTokenEncryption_RoundTripWithScopes(t *testing.T) {
	te, _ := newTestTokenEncryption(t)

	group := "test-scopes"
	expiry := time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC)

	// Test scopes by directly exercising encryptPayload/decryptPayload,
	// since SaveToken does not currently populate scopes.
	key, err := te.getOrCreateEncryptionKey(group)
	if err != nil {
		t.Fatalf("getOrCreateEncryptionKey: %v", err)
	}

	payload := TokenPayload{
		RefreshToken: "scoped-refresh",
		Expiry:       expiry.Format(time.RFC3339),
		TokenType:    "Bearer",
		Scopes:       []string{"email", "calendar.readonly", "drive.file"},
	}

	encrypted, err := te.encryptPayload(key, group, "test-plugin", payload)
	if err != nil {
		t.Fatalf("encryptPayload: %v", err)
	}

	var decrypted TokenPayload
	if err := te.decryptPayload(key, group, "test-plugin", encrypted, &decrypted); err != nil {
		t.Fatalf("decryptPayload: %v", err)
	}

	if decrypted.RefreshToken != payload.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", decrypted.RefreshToken, payload.RefreshToken)
	}
	if decrypted.TokenType != payload.TokenType {
		t.Errorf("TokenType = %q, want %q", decrypted.TokenType, payload.TokenType)
	}
	if decrypted.Expiry != payload.Expiry {
		t.Errorf("Expiry = %q, want %q", decrypted.Expiry, payload.Expiry)
	}
	if len(decrypted.Scopes) != len(payload.Scopes) {
		t.Fatalf("Scopes length = %d, want %d", len(decrypted.Scopes), len(payload.Scopes))
	}
	for i, s := range payload.Scopes {
		if decrypted.Scopes[i] != s {
			t.Errorf("Scopes[%d] = %q, want %q", i, decrypted.Scopes[i], s)
		}
	}

	// Cleanup.
	t.Cleanup(func() {
		_ = te.keyringStore.Delete(group, "__encryption_key")
	})
}

// --- Test: Token with zero expiry ---

func TestTokenEncryption_RoundTripZeroExpiry(t *testing.T) {
	te, _ := newTestTokenEncryption(t)

	group := "test-zero-expiry"
	plugin := "drive"

	original := &oauth2.Token{ //nolint:gosec // test fixture
		AccessToken:  "no-expiry-access",
		RefreshToken: "no-expiry-refresh",
		TokenType:    "Bearer",
		// Expiry is zero value (no expiry).
	}

	if err := te.SaveToken(group, plugin, original); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	loaded, err := te.LoadToken(group, plugin)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}

	if !loaded.Expiry.IsZero() {
		t.Errorf("Expiry should be zero, got %v", loaded.Expiry)
	}
	if loaded.AccessToken != original.AccessToken {
		t.Errorf("AccessToken = %q, want %q", loaded.AccessToken, original.AccessToken)
	}
	if loaded.RefreshToken != original.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", loaded.RefreshToken, original.RefreshToken)
	}

	// Cleanup.
	t.Cleanup(func() {
		_ = te.keyringStore.Delete(group, "__encryption_key")
		_ = te.keyringStore.Delete(group, "__oauth_access_"+plugin)
	})
}

// --- Test: Encrypted file has correct permissions ---

func TestTokenEncryption_FilePermissions(t *testing.T) {
	te, tokensDir := newTestTokenEncryption(t)

	group := "test-perms"
	plugin := "calendar"

	token := &oauth2.Token{
		AccessToken:  "perm-access",
		RefreshToken: "perm-refresh",
		TokenType:    "Bearer",
	}

	if err := te.SaveToken(group, plugin, token); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	// Check file permissions.
	filePath := filepath.Join(tokensDir, group, "token-"+plugin+".json.enc")
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat encrypted file: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}

	// Check directory permissions.
	dirInfo, err := os.Stat(filepath.Join(tokensDir, group))
	if err != nil {
		t.Fatalf("stat group dir: %v", err)
	}
	dirPerm := dirInfo.Mode().Perm()
	if dirPerm != 0o700 {
		t.Errorf("directory permissions = %o, want 0700", dirPerm)
	}

	// Cleanup.
	t.Cleanup(func() {
		_ = te.keyringStore.Delete(group, "__encryption_key")
		_ = te.keyringStore.Delete(group, "__oauth_access_"+plugin)
	})
}

// --- Test: LoadToken with non-existent file ---

func TestTokenEncryption_LoadTokenFileNotFound(t *testing.T) {
	te, _ := newTestTokenEncryption(t)

	// Store an encryption key so we get past key lookup.
	group := "test-notfound"
	key := make([]byte, 32)
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := te.keyringStore.Set(group, "__encryption_key", encoded); err != nil {
		t.Fatalf("set key: %v", err)
	}

	_, err := te.LoadToken(group, "nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent file, got nil")
	}

	// Cleanup.
	t.Cleanup(func() {
		_ = te.keyringStore.Delete(group, "__encryption_key")
	})
}

// --- Test: EncryptedTokenFile version field ---

func TestTokenEncryption_EncryptedFileVersion(t *testing.T) {
	te, tokensDir := newTestTokenEncryption(t)

	group := "test-version"
	plugin := "sheets"

	token := &oauth2.Token{
		AccessToken:  "ver-access",
		RefreshToken: "ver-refresh",
		TokenType:    "Bearer",
	}

	if err := te.SaveToken(group, plugin, token); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	// Read the encrypted file and check version.
	filePath := filepath.Join(tokensDir, group, "token-"+plugin+".json.enc")
	data, err := os.ReadFile(filePath) //nolint:gosec // test file path
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	var encrypted EncryptedTokenFile
	if err := json.Unmarshal(data, &encrypted); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if encrypted.Version != 1 {
		t.Errorf("Version = %d, want 1", encrypted.Version)
	}

	// Cleanup.
	t.Cleanup(func() {
		_ = te.keyringStore.Delete(group, "__encryption_key")
		_ = te.keyringStore.Delete(group, "__oauth_access_"+plugin)
	})
}

// --- Test: Invalid base64 in ciphertext field ---

func TestTokenEncryption_LoadTokenInvalidBase64Ciphertext(t *testing.T) {
	te, tokensDir := newTestTokenEncryption(t)

	group := "test-base64-ct"
	plugin := "drive"

	// Store an encryption key.
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := te.keyringStore.Set(group, "__encryption_key", encoded); err != nil {
		t.Fatalf("set key: %v", err)
	}

	// Write a file with invalid base64 in the Ciphertext field.
	groupDir := filepath.Join(tokensDir, group)
	if err := os.MkdirAll(groupDir, 0o700); err != nil {
		t.Fatalf("create group dir: %v", err)
	}
	badEnc := EncryptedTokenFile{
		Version:    1,
		IV:         base64.StdEncoding.EncodeToString([]byte("012345678901")),
		Ciphertext: "!!!bad-base64!!!",
		Tag:        base64.StdEncoding.EncodeToString([]byte("0123456789012345")),
	}
	data, _ := json.Marshal(badEnc)
	filePath := filepath.Join(groupDir, "token-"+plugin+".json.enc")
	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, err := te.LoadToken(group, plugin)
	if err == nil {
		t.Fatal("expected error for invalid base64 ciphertext, got nil")
	}

	// Cleanup.
	t.Cleanup(func() {
		_ = te.keyringStore.Delete(group, "__encryption_key")
	})
}

// --- Test: Invalid base64 in tag field ---

func TestTokenEncryption_LoadTokenInvalidBase64Tag(t *testing.T) {
	te, tokensDir := newTestTokenEncryption(t)

	group := "test-base64-tag"
	plugin := "meet"

	// Store an encryption key.
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := te.keyringStore.Set(group, "__encryption_key", encoded); err != nil {
		t.Fatalf("set key: %v", err)
	}

	// Write a file with invalid base64 in the Tag field.
	groupDir := filepath.Join(tokensDir, group)
	if err := os.MkdirAll(groupDir, 0o700); err != nil {
		t.Fatalf("create group dir: %v", err)
	}
	badEnc := EncryptedTokenFile{
		Version:    1,
		IV:         base64.StdEncoding.EncodeToString([]byte("012345678901")),
		Ciphertext: base64.StdEncoding.EncodeToString([]byte("fake")),
		Tag:        "!!!bad-base64-tag!!!",
	}
	data, _ := json.Marshal(badEnc)
	filePath := filepath.Join(groupDir, "token-"+plugin+".json.enc")
	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, err := te.LoadToken(group, plugin)
	if err == nil {
		t.Fatal("expected error for invalid base64 tag, got nil")
	}

	// Cleanup.
	t.Cleanup(func() {
		_ = te.keyringStore.Delete(group, "__encryption_key")
	})
}

// --- Test: NewTokenEncryption constructor ---

func TestNewTokenEncryption(t *testing.T) {
	store := NewKeyringStore()
	te := NewTokenEncryption(store, nil, "/tmp/test-tokens")

	if te == nil {
		t.Fatal("NewTokenEncryption returned nil")
		return
	}
	if te.keyringStore != store {
		t.Error("keyringStore not set correctly")
	}
	if te.migrationState != nil {
		t.Error("migrationState should be nil when passed nil")
	}
	if te.tokensDir != "/tmp/test-tokens" {
		t.Errorf("tokensDir = %q, want %q", te.tokensDir, "/tmp/test-tokens")
	}
}

// --- Test: Access token stored in keyring ---

func TestTokenEncryption_AccessTokenInKeyring(t *testing.T) {
	te, _ := newTestTokenEncryption(t)

	group := "test-access-kr"
	plugin := "calendar"

	token := &oauth2.Token{
		AccessToken:  "my-secret-access-token",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
	}

	if err := te.SaveToken(group, plugin, token); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	// Verify access token is stored directly in keyring.
	accessKeyName := "__oauth_access_" + plugin
	stored, err := te.keyringStore.Get(group, accessKeyName)
	if err != nil {
		t.Fatalf("get access token from keyring: %v", err)
	}
	if stored != "my-secret-access-token" {
		t.Errorf("keyring access token = %q, want %q", stored, "my-secret-access-token")
	}

	// Cleanup.
	t.Cleanup(func() {
		_ = te.keyringStore.Delete(group, "__encryption_key")
		_ = te.keyringStore.Delete(group, accessKeyName)
	})
}
