# GitHub App Auth Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `github_app` auth provider that authenticates with GitHub App installation tokens via JWT signing and token exchange.

**Architecture:** Single self-contained `GitHubAppProvider` struct following the `RefreshTokenProvider` pattern — mutex-guarded in-memory token cache, 90% lifetime refresh, `httptest.NewTLSServer`-based tests. Config plumbed through `AuthServiceConfig` → `SingleAuthConfig` → `providerFromConfig()`.

**Tech Stack:** Go, `github.com/golang-jwt/jwt/v5`, `crypto/rsa`, `encoding/pem`, `httptest`

---

### Task 1: Add `golang-jwt/jwt/v5` dependency

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Add the dependency**

Run:
```bash
go get github.com/golang-jwt/jwt/v5
```
Expected: `go.mod` and `go.sum` updated with `golang-jwt/jwt/v5`

- [ ] **Step 2: Tidy**

Run:
```bash
go mod tidy
```
Expected: clean exit

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add golang-jwt/jwt/v5 for GitHub App auth"
```

---

### Task 2: Add config fields to `SingleAuthConfig` and `AuthServiceConfig`

**Files:**
- Modify: `internal/auth/variants.go:11-13` (KnownProviderTypes)
- Modify: `internal/auth/variants.go:46-61` (SingleAuthConfig)
- Modify: `internal/plugin/manifest.go:88-108` (AuthServiceConfig)

- [ ] **Step 1: Add `"github_app"` to KnownProviderTypes**

In `internal/auth/variants.go`, change line 11-13 from:

```go
var KnownProviderTypes = []string{
	"bearer", "basic", "kerberos/spnego", "oauth2", "refresh_token",
}
```

To:

```go
var KnownProviderTypes = []string{
	"bearer", "basic", "kerberos/spnego", "oauth2", "refresh_token", "github_app",
}
```

- [ ] **Step 2: Add fields to `SingleAuthConfig`**

In `internal/auth/variants.go`, add these fields after `ClientID` (line 59):

```go
	ClientID        string
	AppID          string
	InstallationID string
	PrivateKey     string            // raw PEM content (fallback)
	PrivateKeyFile string            // path to PEM file (preferred)
	BaseURL        string            // GitHub API base URL
	Transport      http.RoundTripper // safe transport injected by plugin manager
```

(The `Transport` field already exists — just add the five new fields before it.)

- [ ] **Step 3: Add fields to `AuthServiceConfig`**

In `internal/plugin/manifest.go`, add these fields after `ClientID` (line 104):

```go
	ClientID        string                       `yaml:"client_id"`
	AppID           string                       `yaml:"app_id"`
	InstallationID  string                       `yaml:"installation_id"`
	PrivateKey      string                       `yaml:"private_key"`
	PrivateKeyFile  string                       `yaml:"private_key_file"`
	Select          string                       `yaml:"select"`
```

(Add the four new fields `AppID`, `InstallationID`, `PrivateKey`, `PrivateKeyFile` after `ClientID`, before `Select`.)

- [ ] **Step 4: Verify it compiles**

Run:
```bash
go build ./...
```
Expected: clean build

- [ ] **Step 5: Run existing auth tests to confirm no regressions**

Run:
```bash
go test -v -race ./internal/auth/
```
Expected: all existing tests pass

- [ ] **Step 6: Commit**

```bash
git add internal/auth/variants.go internal/plugin/manifest.go
git commit -m "auth: add config fields for github_app provider"
```

---

### Task 3: Implement `GitHubAppProvider` constructor and interface methods

**Files:**
- Create: `internal/auth/github_app.go`
- Test: `internal/auth/github_app_test.go`

- [ ] **Step 1: Write failing tests for constructor validation and interface methods**

Create `internal/auth/github_app_test.go`:

```go
package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
)

// generateTestKey creates a throwaway RSA key pair for testing.
func generateTestKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return key, pemBytes
}

func TestGitHubAppName(t *testing.T) {
	_, pemBytes := generateTestKey(t)
	p, err := NewGitHubAppProvider("app-123", "inst-456", pemBytes, "https://api.github.com", http.DefaultTransport)
	if err != nil {
		t.Fatalf("NewGitHubAppProvider: %v", err)
	}
	if p.Name() != "github_app" {
		t.Errorf("Name() = %q, want %q", p.Name(), "github_app")
	}
}

func TestGitHubAppAvailable(t *testing.T) {
	_, pemBytes := generateTestKey(t)
	tests := []struct {
		name           string
		appID          string
		installationID string
		pem            []byte
		want           bool
	}{
		{"all set", "app", "inst", pemBytes, true},
		{"empty appID", "", "inst", pemBytes, false},
		{"empty installationID", "app", "", pemBytes, false},
		{"nil pem", "app", "inst", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewGitHubAppProvider(tt.appID, tt.installationID, tt.pem, "https://api.github.com", http.DefaultTransport)
			if tt.want {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !p.Available() {
					t.Error("Available() = false, want true")
				}
			} else {
				if err == nil && p != nil && p.Available() {
					t.Error("Available() = true, want false")
				}
			}
		})
	}
}

func TestGitHubAppConstructorValidation(t *testing.T) {
	_, pemBytes := generateTestKey(t)

	tests := []struct {
		name           string
		appID          string
		installationID string
		pem            []byte
		baseURL        string
		wantErr        string
	}{
		{"empty appID", "", "inst", pemBytes, "https://api.github.com", "app_id must not be empty"},
		{"empty installationID", "app", "", pemBytes, "https://api.github.com", "installation_id must not be empty"},
		{"invalid PEM", "app", "inst", []byte("not-a-pem"), "https://api.github.com", "decode PEM"},
		{"non-RSA PEM", "app", "inst", []byte("-----BEGIN CERTIFICATE-----\nMQ==\n-----END CERTIFICATE-----\n"), "https://api.github.com", "parse"},
		{"http baseURL", "app", "inst", pemBytes, "http://api.github.com", "must use https"},
		{"invalid baseURL", "app", "inst", pemBytes, "://bad", "invalid base_url"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewGitHubAppProvider(tt.appID, tt.installationID, tt.pem, tt.baseURL, http.DefaultTransport)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestGitHubAppDefaultBaseURL(t *testing.T) {
	_, pemBytes := generateTestKey(t)
	p, err := NewGitHubAppProvider("app", "inst", pemBytes, "", http.DefaultTransport)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.baseURL != "https://api.github.com" {
		t.Errorf("baseURL = %q, want %q", p.baseURL, "https://api.github.com")
	}
}

func TestGitHubAppTrailingSlashStripped(t *testing.T) {
	_, pemBytes := generateTestKey(t)
	p, err := NewGitHubAppProvider("app", "inst", pemBytes, "https://api.github.com/", http.DefaultTransport)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.baseURL != "https://api.github.com" {
		t.Errorf("baseURL = %q, want trailing slash stripped", p.baseURL)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
go test -v -run "TestGitHubApp" ./internal/auth/
```
Expected: FAIL — `NewGitHubAppProvider` undefined

- [ ] **Step 3: Implement the provider struct and constructor**

Create `internal/auth/github_app.go`:

```go
package auth

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/LeGambiArt/wtmcp/internal/secrets/vault"
)

// GitHubAppProvider authenticates using GitHub App installation tokens.
// It signs JWTs with the app's private key, exchanges them for
// short-lived installation tokens, and caches them in memory.
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

// NewGitHubAppProvider creates a GitHub App auth provider.
// privateKeyPEM is the raw PEM-encoded RSA private key.
// baseURL defaults to "https://api.github.com" if empty.
func NewGitHubAppProvider(appID, installationID string, privateKeyPEM []byte, baseURL string, transport http.RoundTripper) (*GitHubAppProvider, error) {
	if appID == "" {
		return nil, fmt.Errorf("github_app: app_id must not be empty")
	}
	if installationID == "" {
		return nil, fmt.Errorf("github_app: installation_id must not be empty")
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

	block, _ := pem.Decode(privateKeyPEM)
	if block == nil {
		return nil, fmt.Errorf("github_app: failed to decode PEM block from private key")
	}

	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS8 as fallback.
		pkcs8Key, pkcs8Err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if pkcs8Err != nil {
			return nil, fmt.Errorf("github_app: parse private key: %w", err)
		}
		var ok bool
		key, ok = pkcs8Key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("github_app: private key is not RSA")
		}
	}

	vault.ZeroBytes(privateKeyPEM)

	return &GitHubAppProvider{
		appID:          appID,
		installationID: installationID,
		privateKey:     key,
		baseURL:        baseURL,
		client:         &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}, nil
}

// Name returns "github_app".
func (g *GitHubAppProvider) Name() string { return "github_app" }

// Available reports whether a private key and required IDs are configured.
func (g *GitHubAppProvider) Available() bool {
	return g.privateKey != nil && g.appID != "" && g.installationID != ""
}

// Authenticate returns a Bearer authorization header with a GitHub
// App installation token. Tokens are cached and refreshed at 90%
// of their lifetime.
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test -v -run "TestGitHubApp" ./internal/auth/
```
Expected: all constructor and interface tests PASS

- [ ] **Step 5: Commit**

```bash
git add internal/auth/github_app.go internal/auth/github_app_test.go
git commit -m "auth: add GitHubAppProvider constructor and interface methods"
```

---

### Task 4: Implement JWT creation and token exchange

**Files:**
- Modify: `internal/auth/github_app.go` (add `refreshLocked`, `createJWT`)
- Modify: `internal/auth/github_app_test.go` (add exchange tests)

- [ ] **Step 1: Write failing tests for successful token exchange**

Add to `internal/auth/github_app_test.go`:

```go
import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
)

// newGitHubTestServer creates a TLS server that validates JWT auth
// and responds with installation tokens.
func newGitHubTestServer(t *testing.T, key *rsa.PrivateKey, handler func(r *http.Request) (any, int)) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, status := handler(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newGitHubAppProvider creates a GitHubAppProvider pointing at the test server.
func newGitHubAppProvider(t *testing.T, srv *httptest.Server, pemBytes []byte) *GitHubAppProvider {
	t.Helper()
	p, err := NewGitHubAppProvider("app-123", "inst-456", pemBytes, srv.URL, srv.Client().Transport)
	if err != nil {
		t.Fatalf("NewGitHubAppProvider: %v", err)
	}
	p.client = srv.Client()
	return p
}

// validateJWT parses and validates the JWT from the Authorization header.
func validateJWT(t *testing.T, r *http.Request, pubKey *rsa.PublicKey) jwt.MapClaims {
	t.Helper()
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		t.Fatalf("missing Bearer prefix: %q", auth)
	}
	tokenStr := strings.TrimPrefix(auth, "Bearer ")

	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return pubKey, nil
	})
	if err != nil {
		t.Fatalf("parse JWT: %v", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("unexpected claims type")
	}
	return claims
}

func TestGitHubAppSuccessfulExchange(t *testing.T) {
	key, pemBytes := generateTestKey(t)

	srv := newGitHubTestServer(t, key, func(r *http.Request) (any, int) {
		// Validate request path.
		if r.URL.Path != "/app/installations/inst-456/access_tokens" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q", r.Method)
		}

		// Validate headers.
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("X-GitHub-Api-Version = %q", got)
		}

		// Validate JWT.
		claims := validateJWT(t, r, &key.PublicKey)
		if iss, _ := claims["iss"].(string); iss != "app-123" {
			t.Errorf("iss = %q, want %q", iss, "app-123")
		}

		return map[string]string{
			"token":      "ghs_installation_token_123",
			"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		}, 201
	})

	p := newGitHubAppProvider(t, srv, pemBytes)

	h, err := p.Authenticate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got := h.Get("Authorization"); got != "Bearer ghs_installation_token_123" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestGitHubAppJWTClaims(t *testing.T) {
	key, pemBytes := generateTestKey(t)

	srv := newGitHubTestServer(t, key, func(r *http.Request) (any, int) {
		claims := validateJWT(t, r, &key.PublicKey)

		// iat should be backdated ~60 seconds.
		iat, _ := claims.GetIssuedAt()
		if iat == nil {
			t.Fatal("missing iat claim")
		}
		iatDelta := time.Since(iat.Time)
		if iatDelta < 50*time.Second || iatDelta > 70*time.Second {
			t.Errorf("iat delta = %v, want ~60s", iatDelta)
		}

		// exp should be ~10 minutes from now.
		exp, _ := claims.GetExpirationTime()
		if exp == nil {
			t.Fatal("missing exp claim")
		}
		expDelta := time.Until(exp.Time)
		if expDelta < 9*time.Minute || expDelta > 11*time.Minute {
			t.Errorf("exp delta = %v, want ~10m", expDelta)
		}

		return map[string]string{
			"token":      "tok",
			"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		}, 201
	})

	p := newGitHubAppProvider(t, srv, pemBytes)
	_, err := p.Authenticate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
go test -v -run "TestGitHubAppSuccessfulExchange|TestGitHubAppJWTClaims" ./internal/auth/
```
Expected: FAIL — `refreshLocked` not implemented (compilation error or panic)

- [ ] **Step 3: Implement `refreshLocked` and JWT creation**

Add to `internal/auth/github_app.go` (after the `Authenticate` method):

```go
import (
	// add to existing imports:
	"encoding/json"
	"io"
	"log"

	jwt "github.com/golang-jwt/jwt/v5"
)

type installationTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

func (g *GitHubAppProvider) refreshLocked(ctx context.Context) error {
	tokenStr, err := g.createJWT()
	if err != nil {
		return fmt.Errorf("github_app: create JWT: %w", err)
	}

	tokenURL := fmt.Sprintf("%s/app/installations/%s/access_tokens", g.baseURL, g.installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, nil) //nolint:gosec // tokenURL from validated config
	if err != nil {
		return fmt.Errorf("github_app: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := g.client.Do(req) //nolint:gosec
	if err != nil {
		return fmt.Errorf("github_app: request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("github_app: read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("github_app: HTTP %d from token endpoint: %s", resp.StatusCode, truncateBody(body, 200))
	}

	var tok installationTokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return fmt.Errorf("github_app: parse response: %w", err)
	}

	if tok.Token == "" {
		return fmt.Errorf("github_app: empty token in response")
	}
	if tok.ExpiresAt == "" {
		return fmt.Errorf("github_app: missing expires_at in response")
	}

	expiresAt, err := time.Parse(time.RFC3339, tok.ExpiresAt)
	if err != nil {
		return fmt.Errorf("github_app: parse expires_at %q: %w", tok.ExpiresAt, err)
	}

	now := time.Now()
	lifetime := expiresAt.Sub(now)
	g.accessToken = tok.Token
	g.expiry = now.Add(time.Duration(float64(lifetime) * 0.9))

	log.Printf("github_app: token refreshed (expires in %s)", lifetime.Round(time.Second))
	return nil
}

func (g *GitHubAppProvider) createJWT() (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Issuer:    g.appID,
		IssuedAt:  jwt.NewNumericDate(now.Add(-60 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute)),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(g.privateKey)
}

func truncateBody(body []byte, maxLen int) string {
	if len(body) <= maxLen {
		return string(body)
	}
	return string(body[:maxLen]) + "..."
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test -v -run "TestGitHubAppSuccessfulExchange|TestGitHubAppJWTClaims" ./internal/auth/
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/auth/github_app.go internal/auth/github_app_test.go
git commit -m "auth: implement JWT creation and token exchange for github_app"
```

---

### Task 5: Implement token caching and refresh tests

**Files:**
- Modify: `internal/auth/github_app_test.go`

- [ ] **Step 1: Write token caching test**

Add to `internal/auth/github_app_test.go`:

```go
func TestGitHubAppTokenReuse(t *testing.T) {
	key, pemBytes := generateTestKey(t)
	var calls atomic.Int32

	srv := newGitHubTestServer(t, key, func(_ *http.Request) (any, int) {
		calls.Add(1)
		return map[string]string{
			"token":      "tok",
			"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		}, 201
	})
	p := newGitHubAppProvider(t, srv, pemBytes)

	for range 5 {
		if _, err := p.Authenticate(context.Background(), nil); err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
	}
	if c := calls.Load(); c != 1 {
		t.Errorf("expected 1 token request, got %d", c)
	}
}

func TestGitHubAppAutoRefreshOnExpiry(t *testing.T) {
	key, pemBytes := generateTestKey(t)
	var calls atomic.Int32

	srv := newGitHubTestServer(t, key, func(_ *http.Request) (any, int) {
		n := calls.Add(1)
		return map[string]string{
			"token":      fmt.Sprintf("tok-%d", n),
			"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		}, 201
	})
	p := newGitHubAppProvider(t, srv, pemBytes)

	// First call — triggers exchange.
	h1, _ := p.Authenticate(context.Background(), nil)

	// Force expiry.
	p.mu.Lock()
	p.expiry = time.Now().Add(-1 * time.Second)
	p.mu.Unlock()

	// Second call — should refresh.
	h2, _ := p.Authenticate(context.Background(), nil)

	if calls.Load() != 2 {
		t.Errorf("expected 2 exchanges, got %d", calls.Load())
	}
	if h1.Get("Authorization") == h2.Get("Authorization") {
		t.Error("expected different tokens after refresh")
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run:
```bash
go test -v -run "TestGitHubAppTokenReuse|TestGitHubAppAutoRefresh" ./internal/auth/
```
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/auth/github_app_test.go
git commit -m "auth: add token caching and refresh tests for github_app"
```

---

### Task 6: Implement error response tests

**Files:**
- Modify: `internal/auth/github_app_test.go`

- [ ] **Step 1: Write error response tests**

Add to `internal/auth/github_app_test.go`:

```go
func TestGitHubAppHTTPErrors(t *testing.T) {
	key, pemBytes := generateTestKey(t)

	tests := []struct {
		name    string
		status  int
		wantErr string
	}{
		{"401 unauthorized", 401, "HTTP 401"},
		{"403 forbidden", 403, "HTTP 403"},
		{"404 not found", 404, "HTTP 404"},
		{"422 validation", 422, "HTTP 422"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newGitHubTestServer(t, key, func(_ *http.Request) (any, int) {
				return map[string]string{"message": "error"}, tt.status
			})
			p := newGitHubAppProvider(t, srv, pemBytes)
			_, err := p.Authenticate(context.Background(), nil)
			if err == nil {
				t.Fatalf("expected error on %d", tt.status)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestGitHubAppEmptyToken(t *testing.T) {
	key, pemBytes := generateTestKey(t)

	srv := newGitHubTestServer(t, key, func(_ *http.Request) (any, int) {
		return map[string]string{
			"token":      "",
			"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		}, 201
	})
	p := newGitHubAppProvider(t, srv, pemBytes)

	_, err := p.Authenticate(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error on empty token")
	}
	if !strings.Contains(err.Error(), "empty token") {
		t.Errorf("error = %q", err)
	}
}

func TestGitHubAppMissingExpiresAt(t *testing.T) {
	key, pemBytes := generateTestKey(t)

	srv := newGitHubTestServer(t, key, func(_ *http.Request) (any, int) {
		return map[string]string{"token": "tok"}, 201
	})
	p := newGitHubAppProvider(t, srv, pemBytes)

	_, err := p.Authenticate(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error on missing expires_at")
	}
	if !strings.Contains(err.Error(), "missing expires_at") {
		t.Errorf("error = %q", err)
	}
}

func TestGitHubAppUnparseableExpiresAt(t *testing.T) {
	key, pemBytes := generateTestKey(t)

	srv := newGitHubTestServer(t, key, func(_ *http.Request) (any, int) {
		return map[string]string{
			"token":      "tok",
			"expires_at": "not-a-date",
		}, 201
	})
	p := newGitHubAppProvider(t, srv, pemBytes)

	_, err := p.Authenticate(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error on unparseable expires_at")
	}
	if !strings.Contains(err.Error(), "parse expires_at") {
		t.Errorf("error = %q", err)
	}
}

func TestGitHubAppMalformedJSON(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(201)
		_, _ = fmt.Fprint(w, "<html>Bad Gateway</html>")
	}))
	t.Cleanup(srv.Close)

	_, pemBytes := generateTestKey(t)
	p, _ := NewGitHubAppProvider("app", "inst", pemBytes, srv.URL, srv.Client().Transport)
	p.client = srv.Client()

	_, err := p.Authenticate(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error on malformed JSON")
	}
	if !strings.Contains(err.Error(), "parse response") {
		t.Errorf("error = %q", err)
	}
}

func TestGitHubAppOversizedResponse(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"token":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", 2<<20)))
		_, _ = w.Write([]byte(`","expires_at":"2026-01-01T00:00:00Z"}`))
	}))
	t.Cleanup(srv.Close)

	_, pemBytes := generateTestKey(t)
	p, _ := NewGitHubAppProvider("app", "inst", pemBytes, srv.URL, srv.Client().Transport)
	p.client = srv.Client()

	_, err := p.Authenticate(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error on oversized response")
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run:
```bash
go test -v -run "TestGitHubAppHTTPErrors|TestGitHubAppEmptyToken|TestGitHubAppMissing|TestGitHubAppUnparseable|TestGitHubAppMalformed|TestGitHubAppOversized" ./internal/auth/
```
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/auth/github_app_test.go
git commit -m "auth: add error response tests for github_app"
```

---

### Task 7: Implement concurrency and context cancellation tests

**Files:**
- Modify: `internal/auth/github_app_test.go`

- [ ] **Step 1: Write concurrency and context tests**

Add to `internal/auth/github_app_test.go`:

```go
func TestGitHubAppConcurrentAccess(t *testing.T) {
	key, pemBytes := generateTestKey(t)
	var calls atomic.Int32

	srv := newGitHubTestServer(t, key, func(_ *http.Request) (any, int) {
		calls.Add(1)
		return map[string]string{
			"token":      "tok",
			"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		}, 201
	})
	p := newGitHubAppProvider(t, srv, pemBytes)

	var wg sync.WaitGroup
	errs := make(chan error, 20)

	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := p.Authenticate(context.Background(), nil)
			if err != nil {
				errs <- err
				return
			}
			if h.Get("Authorization") != "Bearer tok" {
				errs <- fmt.Errorf("unexpected auth: %q", h.Get("Authorization"))
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	if c := calls.Load(); c != 1 {
		t.Errorf("expected 1 exchange, got %d", c)
	}
}

func TestGitHubAppContextCancellation(t *testing.T) {
	arrived := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		close(arrived)
		time.Sleep(2 * time.Second)
	}))
	t.Cleanup(srv.Close)

	_, pemBytes := generateTestKey(t)
	p, _ := NewGitHubAppProvider("app", "inst", pemBytes, srv.URL, srv.Client().Transport)
	p.client = srv.Client()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := p.Authenticate(ctx, nil)
		done <- err
	}()

	<-arrived

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error on context cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Authenticate did not return within 5s after context timeout")
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run:
```bash
go test -v -run "TestGitHubAppConcurrent|TestGitHubAppContext" ./internal/auth/
```
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/auth/github_app_test.go
git commit -m "auth: add concurrency and context tests for github_app"
```

---

### Task 8: Wire `providerFromConfig` and `resolveAuth`

**Files:**
- Modify: `internal/auth/variants.go:102-117` (providerFromConfig switch)
- Modify: `internal/plugin/manager.go:1474-1513` (resolveAuth mappings)

- [ ] **Step 1: Add github_app case to `providerFromConfig`**

In `internal/auth/variants.go`, add before the `default` case (line 114):

```go
	case "github_app":
		return NewGitHubAppProvider(cfg.AppID, cfg.InstallationID, []byte(cfg.PrivateKey), cfg.BaseURL, cfg.Transport)
```

The full switch becomes:

```go
func providerFromConfig(typeName string, cfg SingleAuthConfig) (Provider, error) {
	switch typeName {
	case "bearer":
		return NewBearerProvider(cfg.Token, cfg.Header, cfg.Prefix)
	case "basic":
		return NewBasicProvider(cfg.Username, cfg.Password), nil
	case "kerberos/spnego":
		return NewKerberosProvider(cfg.SPN), nil
	case "oauth2":
		return NewOAuth2Provider(cfg.TokenFile, cfg.CredentialsFile, cfg.Scopes, cfg.CredentialsDir, cfg.Transport)
	case "refresh_token":
		return NewRefreshTokenProvider(cfg.TokenURL, cfg.ClientID, cfg.Token, cfg.Transport, cfg.TokenFile)
	case "github_app":
		return NewGitHubAppProvider(cfg.AppID, cfg.InstallationID, []byte(cfg.PrivateKey), cfg.BaseURL, cfg.Transport)
	default:
		return nil, fmt.Errorf("unknown auth type: %s", typeName)
	}
}
```

- [ ] **Step 2: Add field mappings in `resolveAuth` variant path**

In `internal/plugin/manager.go`, in the variant config builder (around line 1474-1489), add the new fields after `Transport`:

```go
			variantCfg.Variants[name] = auth.SingleAuthConfig{
				Type:            v.Type,
				Token:           resolve(v.Token),
				Header:          v.Header,
				Prefix:          v.Prefix,
				Username:        resolve(v.Username),
				Password:        resolve(v.Password),
				SPN:             resolve(v.SPN),
				Scopes:          v.Scopes,
				CredentialsFile: decryptCredFile(resolve(v.CredentialsFile)),
				TokenFile:       resolve(v.TokenFile),
				CredentialsDir:  credDir,
				TokenURL:        resolve(v.TokenURL),
				ClientID:        resolve(v.ClientID),
				Transport:       safeTransport,
				AppID:          resolve(v.AppID),
				InstallationID: resolve(v.InstallationID),
				PrivateKey:     resolve(v.PrivateKey),
				PrivateKeyFile: decryptCredFile(resolve(v.PrivateKeyFile)),
				BaseURL:        resolve(manifest.Services.HTTP.BaseURL),
			}
```

- [ ] **Step 3: Add field mappings in `resolveAuth` single config path**

In `internal/plugin/manager.go`, in the single config builder (around line 1496-1513), add the new fields after `Transport`:

```go
			"default": {
				Type:            authCfg.Type,
				Token:           resolve(authCfg.Token),
				Header:          authCfg.Header,
				Prefix:          authCfg.Prefix,
				Username:        resolve(authCfg.Username),
				Password:        resolve(authCfg.Password),
				SPN:             resolve(authCfg.SPN),
				Scopes:          authCfg.Scopes,
				CredentialsFile: decryptCredFile(resolve(authCfg.CredentialsFile)),
				TokenFile:       resolve(authCfg.TokenFile),
				CredentialsDir:  credDir,
				TokenURL:        resolve(authCfg.TokenURL),
				ClientID:        resolve(authCfg.ClientID),
				Transport:       safeTransport,
				AppID:          resolve(authCfg.AppID),
				InstallationID: resolve(authCfg.InstallationID),
				PrivateKey:     resolve(authCfg.PrivateKey),
				PrivateKeyFile: decryptCredFile(resolve(authCfg.PrivateKeyFile)),
				BaseURL:        resolvedBaseURL,
			},
```

Note: In the single config path, `resolvedBaseURL` is already available (set at line 618 of `manager.go`). In the variant path, we use `resolve(manifest.Services.HTTP.BaseURL)` to get the same value.

- [ ] **Step 4: Handle `PrivateKeyFile` loading in `providerFromConfig`**

The `PrivateKeyFile` field contains the resolved/decrypted file path. The actual file reading should happen in `providerFromConfig` since that's where other credential files are consumed. Update the `github_app` case in `variants.go`:

```go
	case "github_app":
		privateKeyPEM := []byte(cfg.PrivateKey)
		if cfg.PrivateKeyFile != "" {
			data, err := loadPrivateKeyFile(cfg.PrivateKeyFile)
			if err != nil {
				return nil, fmt.Errorf("github_app: load private key file: %w", err)
			}
			privateKeyPEM = data
		}
		return NewGitHubAppProvider(cfg.AppID, cfg.InstallationID, privateKeyPEM, cfg.BaseURL, cfg.Transport)
```

Add the `loadPrivateKeyFile` function to `internal/auth/github_app.go`:

```go
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
```

Also add `"os"` to the imports in `github_app.go`.

- [ ] **Step 5: Verify it compiles**

Run:
```bash
go build ./...
```
Expected: clean build

- [ ] **Step 6: Run all auth tests**

Run:
```bash
go test -v -race ./internal/auth/
```
Expected: all tests PASS

- [ ] **Step 7: Commit**

```bash
git add internal/auth/variants.go internal/auth/github_app.go internal/plugin/manager.go
git commit -m "auth: wire github_app into providerFromConfig and resolveAuth"
```

---

### Task 9: Add private key file loading tests

**Files:**
- Modify: `internal/auth/github_app_test.go`

- [ ] **Step 1: Write key file loading tests**

Add to `internal/auth/github_app_test.go`:

```go
func TestLoadPrivateKeyFile(t *testing.T) {
	_, pemBytes := generateTestKey(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "app.pem")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	data, err := loadPrivateKeyFile(path)
	if err != nil {
		t.Fatalf("loadPrivateKeyFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty PEM data")
	}
}

func TestLoadPrivateKeyFile_LoosePermissions(t *testing.T) {
	_, pemBytes := generateTestKey(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "app.pem")
	if err := os.WriteFile(path, pemBytes, 0o644); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	_, err := loadPrivateKeyFile(path)
	if err == nil {
		t.Fatal("expected error for loose permissions")
	}
	if !strings.Contains(err.Error(), "group/other") {
		t.Errorf("error = %q, want group/other permission error", err)
	}
}

func TestLoadPrivateKeyFile_Symlink(t *testing.T) {
	_, pemBytes := generateTestKey(t)

	dir := t.TempDir()
	target := filepath.Join(dir, "real.pem")
	link := filepath.Join(dir, "link.pem")

	if err := os.WriteFile(target, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	_, err := loadPrivateKeyFile(link)
	if err == nil {
		t.Fatal("expected error for symlink")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error = %q, want symlink error", err)
	}
}

func TestLoadPrivateKeyFile_MissingFile(t *testing.T) {
	_, err := loadPrivateKeyFile(filepath.Join(t.TempDir(), "nonexistent.pem"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestPrivateKeyFileTakesPrecedence(t *testing.T) {
	key, pemBytes := generateTestKey(t)

	// Write key to file.
	dir := t.TempDir()
	path := filepath.Join(dir, "app.pem")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	// Create a provider via providerFromConfig with both file and raw PEM.
	// File should take precedence.
	cfg := SingleAuthConfig{
		Type:           "github_app",
		AppID:          "app-from-file",
		InstallationID: "inst",
		PrivateKey:     "this-is-not-valid-pem",
		PrivateKeyFile: path,
		BaseURL:        "",
		Transport:      http.DefaultTransport,
	}

	p, err := providerFromConfig("github_app", cfg)
	if err != nil {
		t.Fatalf("providerFromConfig: %v", err)
	}

	gap := p.(*GitHubAppProvider)
	if gap.appID != "app-from-file" {
		t.Errorf("appID = %q, want %q", gap.appID, "app-from-file")
	}
	// Verify the key was loaded from file by checking it matches.
	if gap.privateKey.N.Cmp(key.N) != 0 {
		t.Error("private key does not match file key")
	}
}
```

Add these imports to the test file (if not already present):

```go
import (
	"os"
	"path/filepath"
)
```

- [ ] **Step 2: Run tests to verify they pass**

Run:
```bash
go test -v -run "TestLoadPrivateKeyFile|TestPrivateKeyFile" ./internal/auth/
```
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/auth/github_app_test.go
git commit -m "auth: add private key file loading tests for github_app"
```

---

### Task 10: Run full test suite and lint

**Files:**
- None (verification only)

- [ ] **Step 1: Run full auth test suite with race detector**

Run:
```bash
go test -v -race ./internal/auth/
```
Expected: all tests PASS

- [ ] **Step 2: Run linter**

Run:
```bash
make lint 2>&1 | head -50
```
Expected: no new lint errors in `github_app.go` or `github_app_test.go`

- [ ] **Step 3: Run fmt**

Run:
```bash
make fmt
```
Expected: no formatting changes needed

- [ ] **Step 4: Verify build**

Run:
```bash
go build ./...
```
Expected: clean build

- [ ] **Step 5: Commit any lint/fmt fixes if needed**

```bash
git add -u
git commit -m "auth: fix lint/fmt issues in github_app provider"
```

(Skip if no changes needed.)
