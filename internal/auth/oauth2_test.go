package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LeGambiArt/wtmcp/internal/credentials"
	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

var testTransport = http.DefaultTransport

func init() {
	// Use mock keyring for testing encrypted token storage.
	keyring.MockInit()
}

func TestOAuth2ProviderLoadToken(t *testing.T) {
	dir := t.TempDir()

	// Write a valid token file
	token := tokenJSON{
		AccessToken:  "access-123",
		TokenType:    "Bearer",
		RefreshToken: "refresh-456",
		Expiry:       time.Now().Add(1 * time.Hour).Format(time.RFC3339),
	}
	data, _ := json.Marshal(token) //nolint:gosec // test data
	tokenFile := filepath.Join(dir, "token.json")
	if err := os.WriteFile(tokenFile, data, 0o600); err != nil {
		t.Fatal(err)
	}

	p, _ := NewOAuth2Provider(tokenFile, "nonexistent-creds.json", []string{"scope1"}, dir, testTransport)

	if p.Name() != "oauth2" {
		t.Errorf("Name() = %q", p.Name())
	}
	if !p.Available() {
		t.Error("should be available with valid token")
	}

	headers, err := p.Authenticate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	auth := headers.Get("Authorization")
	if auth != "Bearer access-123" {
		t.Errorf("Authorization = %q", auth)
	}
}

func TestOAuth2ProviderExpiredNoRefresh(t *testing.T) {
	dir := t.TempDir()

	// Write an expired token with no refresh token
	token := tokenJSON{
		AccessToken: "expired-token",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
	}
	data, _ := json.Marshal(token) //nolint:gosec // test data
	tokenFile := filepath.Join(dir, "token.json")
	if err := os.WriteFile(tokenFile, data, 0o600); err != nil {
		t.Fatal(err)
	}

	p, _ := NewOAuth2Provider(tokenFile, "nonexistent.json", nil, dir, testTransport)

	// Available should be false — expired and no refresh token
	if p.Available() {
		t.Error("should not be available with expired token and no refresh")
	}

	_, err := p.Authenticate(context.Background(), nil)
	if err == nil {
		t.Error("should error with expired token and no refresh")
	}
}

func TestOAuth2ProviderNoToken(t *testing.T) {
	dir := t.TempDir()
	p, err := NewOAuth2Provider("token.json", "creds.json", nil, dir, testTransport)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if p.Available() {
		t.Error("should not be available without token")
	}

	_, err = p.Authenticate(context.Background(), nil)
	if err == nil {
		t.Error("should error without token")
	}
}

func TestOAuth2ProviderRejectsEscape(t *testing.T) {
	_, err := NewOAuth2Provider("../../etc/shadow", "creds.json", nil, "/opt/creds", testTransport)
	if err == nil {
		t.Fatal("expected error for path escape")
	}
}

func TestSaveToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "token.json")

	tok := tokenJSON{
		AccessToken:  "test-access",
		TokenType:    "Bearer",
		RefreshToken: "test-refresh",
		Expiry:       time.Now().Add(1 * time.Hour).Format(time.RFC3339),
	}

	expiry, _ := time.Parse(time.RFC3339, tok.Expiry)
	oauthTok := &oauth2.Token{
		AccessToken:  tok.AccessToken,
		TokenType:    tok.TokenType,
		RefreshToken: tok.RefreshToken,
		Expiry:       expiry,
	}

	if err := saveToken(path, oauthTok); err != nil {
		t.Fatalf("saveToken: %v", err)
	}

	// Verify file permissions
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("permissions = %o, want 600", info.Mode().Perm())
	}

	// Verify it can be read back
	loaded, err := loadToken(path)
	if err != nil {
		t.Fatalf("loadToken: %v", err)
	}
	if loaded.AccessToken != "test-access" {
		t.Errorf("AccessToken = %q", loaded.AccessToken)
	}
}

func TestResolveCredentialPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		dir     string
		wantAbs bool
	}{
		{
			name:    "absolute path within dir",
			path:    "/opt/creds/token.json",
			dir:     "/opt/creds",
			wantAbs: true,
		},
		{
			name:    "relative with credentials dir",
			path:    "token.json",
			dir:     "/opt/creds",
			wantAbs: true,
		},
		{
			name:    "relative without dir uses default",
			path:    "token.json",
			dir:     "",
			wantAbs: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := resolveCredentialPath(tt.path, tt.dir)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantAbs && !filepath.IsAbs(result) {
				t.Errorf("expected absolute path, got %q", result)
			}
		})
	}

	t.Run("traversal rejected", func(t *testing.T) {
		_, err := resolveCredentialPath("../../etc/passwd", "/opt/creds")
		if err == nil {
			t.Error("expected error for path traversal")
		}
	})

	t.Run("symlink in base dir resolved", func(t *testing.T) {
		realDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(realDir, "token.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		parent := t.TempDir()
		link := filepath.Join(parent, "creds-link")
		if err := os.Symlink(realDir, link); err != nil {
			t.Skipf("symlinks not supported: %v", err)
		}
		result, err := resolveCredentialPath("token.json", link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		realDirResolved, _ := filepath.EvalSymlinks(realDir)
		if !strings.HasPrefix(result, realDirResolved) {
			t.Errorf("expected resolved path under %s, got %s", realDirResolved, result)
		}
	})

	t.Run("absolute path through symlinked base with nonexistent file", func(t *testing.T) {
		realDir := t.TempDir()
		parent := t.TempDir()
		link := filepath.Join(parent, "creds-link")
		if err := os.Symlink(realDir, link); err != nil {
			t.Skipf("symlinks not supported: %v", err)
		}
		absPath := filepath.Join(link, "new-token.json")
		result, err := resolveCredentialPath(absPath, link)
		if err != nil {
			t.Fatalf("should accept absolute path through symlinked base for new file: %v", err)
		}
		realDirResolved, _ := filepath.EvalSymlinks(realDir)
		if !strings.HasPrefix(result, realDirResolved) {
			t.Errorf("expected resolved path under %s, got %s", realDirResolved, result)
		}
	})

	t.Run("symlink escaping base rejected", func(t *testing.T) {
		dir := t.TempDir()
		link := filepath.Join(dir, "escape")
		if err := os.Symlink("/etc/passwd", link); err != nil {
			t.Skipf("symlinks not supported: %v", err)
		}
		_, err := resolveCredentialPath("escape", dir)
		if err == nil {
			t.Error("expected error for symlink escaping credentials dir")
		}
	})
}

// --- C1 Fix Tests: OAuth2 Token Loading Fallback ---

// TestOAuth2Provider_PlaintextFallbackWhenEncryptionConfigured verifies
// that when token encryption is configured but no encrypted token exists
// (and migration is a no-op), the provider falls back to loading the
// plaintext token file. This was the critical C1 bug.
func TestOAuth2Provider_PlaintextFallbackWhenEncryptionConfigured(t *testing.T) {
	dir := t.TempDir()

	// Write a plaintext token file at the resolved token path.
	token := tokenJSON{ //nolint:gosec // test fixture
		AccessToken:  "plaintext-access",
		TokenType:    "Bearer",
		RefreshToken: "plaintext-refresh",
		Expiry:       time.Now().Add(1 * time.Hour).Format(time.RFC3339),
	}
	data, _ := json.Marshal(token) //nolint:gosec // test fixture
	tokenFile := filepath.Join(dir, "token.json")
	if err := os.WriteFile(tokenFile, data, 0o600); err != nil {
		t.Fatal(err)
	}

	// Create a TokenEncryption instance. The encrypted token file does
	// NOT exist, and MigrateUnencryptedToken will be a no-op because
	// the plaintext file is at a different path than what migration
	// expects (migration looks in tokensDir/group/token-plugin.json).
	store := credentials.NewKeyringStore()
	te := credentials.NewTokenEncryption(store, nil, dir)

	opts := &OAuth2Options{
		TokenEncryption: te,
		CredentialGroup: "testgroup",
		PluginName:      "testplugin",
	}

	p, _ := NewOAuth2Provider(tokenFile, "nonexistent-creds.json", nil, dir, testTransport, opts)

	if !p.Available() {
		t.Fatal("provider should be available — plaintext fallback should have loaded the token")
	}

	headers, err := p.Authenticate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	auth := headers.Get("Authorization")
	if auth != "Bearer plaintext-access" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer plaintext-access")
	}
}

// TestOAuth2Provider_EncryptedTokenTakesPrecedence verifies that when
// both encrypted and plaintext tokens exist, the encrypted token is
// used.
func TestOAuth2Provider_EncryptedTokenTakesPrecedence(t *testing.T) {
	dir := t.TempDir()

	store := credentials.NewKeyringStore()
	te := credentials.NewTokenEncryption(store, nil, dir)

	// Save an encrypted token.
	encToken := &oauth2.Token{
		AccessToken:  "encrypted-access",
		RefreshToken: "encrypted-refresh",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(1 * time.Hour).Truncate(time.Second),
	}
	if err := te.SaveToken("google", "calendar", encToken); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Delete("google", "__encryption_key")
		_ = store.Delete("google", "__oauth_access_calendar")
	})

	// Also write a plaintext token file with a different access token.
	plainToken := tokenJSON{ //nolint:gosec // test fixture
		AccessToken:  "plaintext-should-not-be-used",
		TokenType:    "Bearer",
		RefreshToken: "plaintext-refresh",
		Expiry:       time.Now().Add(1 * time.Hour).Format(time.RFC3339),
	}
	data, _ := json.Marshal(plainToken) //nolint:gosec // test fixture
	tokenFile := filepath.Join(dir, "token.json")
	if err := os.WriteFile(tokenFile, data, 0o600); err != nil {
		t.Fatal(err)
	}

	opts := &OAuth2Options{
		TokenEncryption: te,
		CredentialGroup: "google",
		PluginName:      "calendar",
	}

	p, _ := NewOAuth2Provider(tokenFile, "nonexistent-creds.json", nil, dir, testTransport, opts)

	if !p.Available() {
		t.Fatal("provider should be available")
	}

	headers, err := p.Authenticate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	auth := headers.Get("Authorization")
	if auth != "Bearer encrypted-access" {
		t.Errorf("Authorization = %q, want %q (encrypted should take precedence)", auth, "Bearer encrypted-access")
	}
}

// TestOAuth2Provider_NoTokenAnywhere verifies that when encryption is
// configured but no token exists anywhere, the provider is not available.
func TestOAuth2Provider_NoTokenAnywhere(t *testing.T) {
	dir := t.TempDir()

	store := credentials.NewKeyringStore()
	te := credentials.NewTokenEncryption(store, nil, dir)

	opts := &OAuth2Options{
		TokenEncryption: te,
		CredentialGroup: "empty",
		PluginName:      "noplugin",
	}

	p, _ := NewOAuth2Provider(
		filepath.Join(dir, "nonexistent-token.json"),
		"nonexistent-creds.json", nil, dir, testTransport, opts)

	if p.Available() {
		t.Error("provider should not be available without any token")
	}
}
