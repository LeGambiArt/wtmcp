package auth

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/secrets/vault"
	"github.com/golang-jwt/jwt/v5"
)

// GitHubAppProvider authenticates HTTP requests using a GitHub App
// installation token. It creates short-lived JWTs signed with the
// app's private key and exchanges them for installation access tokens
// via the GitHub API. Tokens are cached and refreshed automatically.
type GitHubAppProvider struct {
	mu             sync.Mutex
	appID          string
	installationID string
	privateKey     *rsa.PrivateKey
	baseURL        string
	accessToken    string
	expiry         time.Time
	client         *http.Client
}

// installationTokenResponse is the JSON response from the GitHub
// installation access token endpoint.
type installationTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// NewGitHubAppProvider creates a GitHub App auth provider.
// privateKeyPEM is zeroed after parsing. baseURL defaults to
// "https://api.github.com" if empty and must use HTTPS.
func NewGitHubAppProvider(appID, installationID string, privateKeyPEM []byte, baseURL string, transport http.RoundTripper) (*GitHubAppProvider, error) {
	defer vault.ZeroBytes(privateKeyPEM)

	if appID == "" {
		return nil, fmt.Errorf("github_app: app_id must not be empty")
	}
	if installationID == "" {
		return nil, fmt.Errorf("github_app: installation_id must not be empty")
	}
	if transport == nil {
		return nil, fmt.Errorf("github_app: transport must not be nil")
	}

	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("github_app: invalid base_url: %w", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("github_app: base_url must use https: %s", baseURL)
	}

	key, err := parseRSAPrivateKey(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("github_app: %w", err)
	}

	return &GitHubAppProvider{
		appID:          appID,
		installationID: installationID,
		privateKey:     key,
		baseURL:        baseURL,
		client:         &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}, nil
}

// parseRSAPrivateKey decodes a PEM block and tries PKCS1 then PKCS8.
func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in private key")
	}

	// Try PKCS1 first.
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	// Fall back to PKCS8.
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key (tried PKCS1 and PKCS8): %w", err)
	}

	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not RSA (got %T)", parsed)
	}
	return key, nil
}

// Name returns "github_app".
func (g *GitHubAppProvider) Name() string { return "github_app" }

// Available reports whether the provider has all required credentials.
func (g *GitHubAppProvider) Available() bool {
	return g.privateKey != nil && g.appID != "" && g.installationID != ""
}

// Authenticate returns a Bearer authorization header with an
// installation access token, refreshing it if expired.
func (g *GitHubAppProvider) Authenticate(ctx context.Context, _ *http.Request) (http.Header, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.accessToken == "" || !time.Now().Before(g.expiry) {
		if err := g.refreshLocked(ctx); err != nil {
			return nil, err
		}
	}

	h := make(http.Header)
	h.Set("Authorization", "Bearer "+g.accessToken)
	return h, nil
}

// refreshLocked exchanges a JWT for an installation access token.
// Must be called with g.mu held.
func (g *GitHubAppProvider) refreshLocked(ctx context.Context) error {
	jwtToken, err := g.createJWT()
	if err != nil {
		return fmt.Errorf("github_app: create JWT: %w", err)
	}

	tokenURL := fmt.Sprintf("%s/app/installations/%s/access_tokens", g.baseURL, g.installationID) //nolint:gosec // baseURL from validated config

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, nil) //nolint:gosec // tokenURL from validated config
	if err != nil {
		return fmt.Errorf("github_app: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "wtmcp")
	// Pinned to 2022-11-28; supported until 2028-03-10.
	// See https://docs.github.com/en/rest/about-the-rest-api/api-versions
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := g.client.Do(req) //nolint:gosec
	if err != nil {
		return fmt.Errorf("github_app: request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB cap
	if err != nil {
		return fmt.Errorf("github_app: read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("github_app: HTTP %d from token endpoint: %s",
			resp.StatusCode, extractErrorMessage(body))
	}

	var tok installationTokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return fmt.Errorf("github_app: parse response: %w", err)
	}

	if tok.Token == "" {
		return fmt.Errorf("github_app: empty token in response")
	}

	expiresAt, err := time.Parse(time.RFC3339, tok.ExpiresAt)
	if err != nil {
		expiresAt, err = time.Parse(time.RFC3339Nano, tok.ExpiresAt)
		if err != nil {
			return fmt.Errorf("github_app: parse expires_at %q: %w", tok.ExpiresAt, err)
		}
	}

	// Refresh at 90% of the remaining lifetime to avoid edge-case failures.
	now := time.Now()
	remaining := expiresAt.Sub(now)
	g.accessToken = tok.Token
	g.expiry = now.Add(time.Duration(float64(remaining) * 0.9))

	log.Printf("github_app: token refreshed (expires in %s)", remaining.Truncate(time.Second))
	return nil
}

// createJWT builds a short-lived RS256 JWT for GitHub App authentication.
func (g *GitHubAppProvider) createJWT() (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Issuer:    g.appID,
		IssuedAt:  jwt.NewNumericDate(now.Add(-60 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute)),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(g.privateKey)
	if err != nil {
		return "", fmt.Errorf("sign JWT: %w", err)
	}
	return signed, nil
}

// extractErrorMessage tries to extract a GitHub API error message from
// a JSON response body. Falls back to the truncated raw body.
func extractErrorMessage(body []byte) string {
	var gh struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &gh) == nil && gh.Message != "" {
		return gh.Message
	}
	return truncateBody(body, 200)
}

// truncateBody returns body as a string, truncated to maxLen with an
// ellipsis if it exceeds the limit.
func truncateBody(body []byte, maxLen int) string {
	if len(body) <= maxLen {
		return string(body)
	}
	return string(body[:maxLen]) + "..."
}

// loadPrivateKeyFile reads a PEM private key file after verifying it is
// not a symlink and has restrictive permissions (no group/other access).
func loadPrivateKeyFile(path string) ([]byte, error) {
	if err := config.RejectSymlink(path); err != nil {
		return nil, fmt.Errorf("private key file: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if err := config.CheckPermissions(path, info); err != nil {
		return nil, fmt.Errorf("private key file: %w", err)
	}
	return os.ReadFile(path) //nolint:gosec // path validated above
}
