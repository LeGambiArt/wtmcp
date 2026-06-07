// Package google provides shared OAuth2 token loading for Google API plugins.
package google

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// TokenJSON matches the on-disk format saved by oauth2flow.
type TokenJSON struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	Expiry       string `json:"expiry,omitempty"`
}

// CredentialsDir returns the Google credentials directory.
// Uses GOOGLE_CREDENTIALS_DIR from the process environment (not scoped
// env.d) — this is intentional server-level config, similar to WorkDir().
// Falls back to ~/.config/wtmcp/credentials/google/.
func CredentialsDir() string {
	if dir := os.Getenv("GOOGLE_CREDENTIALS_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "wtmcp", "credentials", "google")
}

// NewHTTPClientFromDir creates an HTTP client authenticated with OAuth2
// credentials from the specified directory. It loads client-credentials.json
// and the token file, and returns an http.Client that auto-refreshes the token.
func NewHTTPClientFromDir(ctx context.Context, credDir, tokenFile string, scopes []string) (*http.Client, error) {
	if credDir == "" {
		return nil, fmt.Errorf("credentials directory is empty")
	}

	clientCredsPath := filepath.Join(credDir, "client-credentials.json")
	tokenPath := filepath.Join(credDir, tokenFile)

	// Verify tokenPath stays within credDir after symlink resolution.
	resolvedDir := filepath.Clean(credDir)
	if rd, err := filepath.EvalSymlinks(resolvedDir); err == nil {
		resolvedDir = rd
	}
	resolvedToken := filepath.Clean(tokenPath)
	if rt, err := filepath.EvalSymlinks(resolvedToken); err == nil {
		resolvedToken = rt
	} else if dir := filepath.Dir(resolvedToken); dir != resolvedToken {
		if rd, err := filepath.EvalSymlinks(dir); err == nil {
			resolvedToken = filepath.Join(rd, filepath.Base(resolvedToken))
		}
	}
	if !strings.HasPrefix(resolvedToken, resolvedDir+string(filepath.Separator)) {
		return nil, fmt.Errorf("token path escapes credentials directory: %s", tokenFile)
	}
	tokenPath = resolvedToken

	// Validate client credentials file before reading.
	if err := config.RejectSymlink(clientCredsPath); err != nil {
		return nil, fmt.Errorf("client credentials: %w", err)
	}
	info, err := os.Stat(clientCredsPath)
	if err != nil {
		return nil, fmt.Errorf("stat client credentials: %w", err)
	}
	if err := config.CheckPermissions(clientCredsPath, info); err != nil {
		return nil, fmt.Errorf("client credentials: %w", err)
	}
	clientData, err := os.ReadFile(clientCredsPath) //nolint:gosec // validated above
	if err != nil {
		return nil, fmt.Errorf("read client credentials: %w", err)
	}

	cfg, err := google.ConfigFromJSON(clientData, scopes...)
	if err != nil {
		return nil, fmt.Errorf("parse client credentials: %w", err)
	}

	// Load token
	tok, err := LoadToken(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("load token from %s: %w", tokenPath, err)
	}

	// Create a token source that auto-refreshes and saves updated tokens
	ts := cfg.TokenSource(ctx, tok)
	return oauth2.NewClient(ctx, &savingTokenSource{
		base:      ts,
		tokenPath: tokenPath,
	}), nil
}

// NewHTTPClient creates an OAuth2 HTTP client using the default
// credentials directory. Prefer NewHTTPClientFromDir with the
// config-provided path when available.
func NewHTTPClient(ctx context.Context, tokenFile string, scopes []string) (*http.Client, error) {
	return NewHTTPClientFromDir(ctx, CredentialsDir(), tokenFile, scopes)
}

// savingTokenSource wraps a TokenSource and persists refreshed tokens to disk.
type savingTokenSource struct {
	mu        sync.Mutex
	base      oauth2.TokenSource
	tokenPath string
	lastToken *oauth2.Token
}

func (s *savingTokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tok, err := s.base.Token()
	if err != nil {
		return nil, err
	}

	if s.lastToken == nil || tok.AccessToken != s.lastToken.AccessToken {
		s.lastToken = tok
		if err := saveToken(s.tokenPath, tok); err != nil {
			log.Printf("google: failed to persist refreshed token: %v", err)
		}
	}

	return tok, nil
}

// LoadToken reads an OAuth2 token from the given JSON file path.
func LoadToken(path string) (*oauth2.Token, error) {
	if err := config.RejectSymlink(path); err != nil {
		return nil, fmt.Errorf("token file: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if err := config.CheckPermissions(path, info); err != nil {
		return nil, fmt.Errorf("token file: %w", err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // validated above
	if err != nil {
		return nil, err
	}

	var tj TokenJSON
	if err := json.Unmarshal(data, &tj); err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}

	tok := &oauth2.Token{
		AccessToken:  tj.AccessToken,
		TokenType:    tj.TokenType,
		RefreshToken: tj.RefreshToken,
	}

	if tj.Expiry != "" {
		t, err := time.Parse(time.RFC3339, tj.Expiry)
		if err != nil {
			return nil, fmt.Errorf("parse token expiry: %w", err)
		}
		tok.Expiry = t
	}

	return tok, nil
}

func saveToken(path string, tok *oauth2.Token) error {
	// Reject symlinks to prevent writing credentials to an
	// attacker-controlled location on every token refresh.
	if _, err := os.Lstat(path); err == nil {
		if err := config.RejectSymlink(path); err != nil {
			return fmt.Errorf("token save: %w", err)
		}
	}

	tj := TokenJSON{
		AccessToken:  tok.AccessToken,
		TokenType:    tok.TokenType,
		RefreshToken: tok.RefreshToken,
	}
	if !tok.Expiry.IsZero() {
		tj.Expiry = tok.Expiry.Format(time.RFC3339)
	}

	data, err := json.MarshalIndent(tj, "", "  ") //nolint:gosec // G117: intentional token serialization for on-disk storage
	if err != nil {
		return err
	}

	// Atomic write: temp file + fsync + rename.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".token-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()        //nolint:errcheck,gosec // best-effort cleanup
		os.Remove(tmpName) //nolint:errcheck,gosec // best-effort cleanup
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()        //nolint:errcheck,gosec // best-effort cleanup
		os.Remove(tmpName) //nolint:errcheck,gosec // best-effort cleanup
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()        //nolint:errcheck,gosec // best-effort cleanup
		os.Remove(tmpName) //nolint:errcheck,gosec // best-effort cleanup
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName) //nolint:errcheck,gosec // best-effort cleanup
		return err
	}
	return os.Rename(tmpName, path)
}
