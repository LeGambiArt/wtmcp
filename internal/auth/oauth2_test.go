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

	"golang.org/x/oauth2"
)

var testTransport = http.DefaultTransport

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
