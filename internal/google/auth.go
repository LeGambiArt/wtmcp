// Package google provides shared OAuth2 token loading for Google API plugins.
package google

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

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

	// Load client credentials
	clientData, err := os.ReadFile(clientCredsPath) //nolint:gosec // known credential path
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
	base      oauth2.TokenSource
	tokenPath string
	lastToken *oauth2.Token
}

func (s *savingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := s.base.Token()
	if err != nil {
		return nil, err
	}

	// Save if the token changed (was refreshed)
	if s.lastToken == nil || tok.AccessToken != s.lastToken.AccessToken {
		s.lastToken = tok
		_ = saveToken(s.tokenPath, tok) // best-effort save
	}

	return tok, nil
}

func LoadToken(path string) (*oauth2.Token, error) {
	data, err := os.ReadFile(path) //nolint:gosec // known token path
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
	tj := TokenJSON{
		AccessToken:  tok.AccessToken,
		TokenType:    tok.TokenType,
		RefreshToken: tok.RefreshToken,
	}
	if !tok.Expiry.IsZero() {
		tj.Expiry = tok.Expiry.Format(time.RFC3339)
	}

	data, err := json.MarshalIndent(tj, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0o600)
}
