package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// --- Test helpers ---

// generateTestKey creates a throwaway 2048-bit RSA key pair and
// returns both the private key and its PEM-encoded form.
func generateTestKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	pemBlock := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return key, pemBlock
}

// newGitHubTestServer returns an httptest.TLS server that calls
// handler for every request. The handler receives the request and
// returns the JSON response body and HTTP status code.
func newGitHubTestServer(t *testing.T, handler func(r *http.Request) (any, int)) *httptest.Server {
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

// newGitHubAppProvider creates a GitHubAppProvider pointing at the
// test server with default test values.
func newGitHubAppProvider(t *testing.T, srv *httptest.Server, pemBytes []byte) *GitHubAppProvider {
	t.Helper()
	// Make a copy of pemBytes since the constructor zeros the original.
	pemCopy := make([]byte, len(pemBytes))
	copy(pemCopy, pemBytes)

	p, err := NewGitHubAppProvider("12345", "67890", pemCopy, srv.URL, srv.Client().Transport)
	if err != nil {
		t.Fatalf("NewGitHubAppProvider: %v", err)
	}
	p.client = srv.Client()
	return p
}

// validateJWT parses and validates the JWT from the Authorization
// header, returning the claims map.
func validateJWT(t *testing.T, r *http.Request, pubKey *rsa.PublicKey) jwt.MapClaims {
	t.Helper()
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		t.Fatalf("Authorization header = %q, want Bearer prefix", auth)
	}
	tokenStr := strings.TrimPrefix(auth, "Bearer ")

	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return pubKey, nil
	})
	if err != nil {
		t.Fatalf("parse JWT: %v", err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("claims are not MapClaims")
	}
	return claims
}

// --- Constructor and interface tests ---

func TestGitHubAppName(t *testing.T) {
	_, pemBytes := generateTestKey(t)
	srv := newGitHubTestServer(t, func(_ *http.Request) (any, int) {
		return nil, 200
	})
	p := newGitHubAppProvider(t, srv, pemBytes)
	if got := p.Name(); got != "github_app" {
		t.Errorf("Name() = %q, want %q", got, "github_app")
	}
}

func TestGitHubAppAvailable(t *testing.T) {
	key, _ := generateTestKey(t)

	tests := []struct {
		name           string
		appID          string
		installationID string
		privateKey     *rsa.PrivateKey
		want           bool
	}{
		{"all set", "123", "456", key, true},
		{"empty appID", "", "456", key, false},
		{"empty installationID", "123", "", key, false},
		{"nil key", "123", "456", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &GitHubAppProvider{
				appID:          tt.appID,
				installationID: tt.installationID,
				privateKey:     tt.privateKey,
			}
			if got := p.Available(); got != tt.want {
				t.Errorf("Available() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGitHubAppConstructorValidation(t *testing.T) {
	_, validPEM := generateTestKey(t)

	// Generate an EC key for non-RSA test.
	ecPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: []byte("not a real ec key but triggers pem decode then parse failure"),
	})

	tests := []struct {
		name           string
		appID          string
		installationID string
		pem            []byte
		baseURL        string
		transport      http.RoundTripper
		wantErr        string
	}{
		{
			name:           "empty appID",
			appID:          "",
			installationID: "456",
			pem:            validPEM,
			transport:      http.DefaultTransport,
			wantErr:        "app_id must not be empty",
		},
		{
			name:           "empty installationID",
			appID:          "123",
			installationID: "",
			pem:            validPEM,
			transport:      http.DefaultTransport,
			wantErr:        "installation_id must not be empty",
		},
		{
			name:           "nil transport",
			appID:          "123",
			installationID: "456",
			pem:            validPEM,
			transport:      nil,
			wantErr:        "transport must not be nil",
		},
		{
			name:           "invalid PEM",
			appID:          "123",
			installationID: "456",
			pem:            []byte("not a pem"),
			transport:      http.DefaultTransport,
			wantErr:        "no PEM block",
		},
		{
			name:           "non-RSA PEM",
			appID:          "123",
			installationID: "456",
			pem:            ecPEM,
			transport:      http.DefaultTransport,
			wantErr:        "parse private key",
		},
		{
			name:           "non-numeric installationID",
			appID:          "123",
			installationID: "not-a-number",
			pem:            validPEM,
			transport:      http.DefaultTransport,
			wantErr:        "installation_id must be numeric",
		},
		{
			name:           "http baseURL",
			appID:          "123",
			installationID: "456",
			pem:            validPEM,
			transport:      http.DefaultTransport,
			baseURL:        "http://github.example.com",
			wantErr:        "must use https",
		},
		{
			name:           "invalid baseURL",
			appID:          "123",
			installationID: "456",
			pem:            validPEM,
			transport:      http.DefaultTransport,
			baseURL:        "://bad",
			wantErr:        "invalid base_url",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pemCopy := make([]byte, len(tt.pem))
			copy(pemCopy, tt.pem)
			_, err := NewGitHubAppProvider(tt.appID, tt.installationID, pemCopy, tt.baseURL, tt.transport)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestGitHubAppDefaultBaseURL(t *testing.T) {
	_, pemBytes := generateTestKey(t)
	pemCopy := make([]byte, len(pemBytes))
	copy(pemCopy, pemBytes)

	// Cannot fully construct without a valid HTTPS server, but we can
	// verify the default is set by passing empty baseURL. The
	// constructor will use https://api.github.com. Since transport is
	// nil we just check the field.
	p, err := NewGitHubAppProvider("123", "456", pemCopy, "", http.DefaultTransport)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.baseURL != "https://api.github.com" {
		t.Errorf("baseURL = %q, want %q", p.baseURL, "https://api.github.com")
	}
}

func TestGitHubAppTrailingSlashStripped(t *testing.T) {
	_, pemBytes := generateTestKey(t)
	pemCopy := make([]byte, len(pemBytes))
	copy(pemCopy, pemBytes)

	p, err := NewGitHubAppProvider("123", "456", pemCopy, "https://ghes.example.com/api/v3/", http.DefaultTransport)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.HasSuffix(p.baseURL, "/") {
		t.Errorf("baseURL = %q, should not end with /", p.baseURL)
	}
}

// --- Token exchange tests ---

func TestGitHubAppSuccessfulExchange(t *testing.T) {
	key, pemBytes := generateTestKey(t)

	var calls atomic.Int32
	srv := newGitHubTestServer(t, func(r *http.Request) (any, int) {
		calls.Add(1)

		// Validate request.
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		wantPath := "/app/installations/67890/access_tokens"
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("X-GitHub-Api-Version = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "wtmcp" {
			t.Errorf("User-Agent = %q, want %q", got, "wtmcp")
		}

		// Validate JWT.
		claims := validateJWT(t, r, &key.PublicKey)
		if iss, _ := claims["iss"].(string); iss != "12345" {
			t.Errorf("JWT iss = %q, want %q", iss, "12345")
		}

		return installationTokenResponse{ //nolint:gosec // test token //nolint:gosec // test token
			Token:     "ghs_test_token_123",
			ExpiresAt: time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
		}, 201
	})

	p := newGitHubAppProvider(t, srv, pemBytes)

	h, err := p.Authenticate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got := h.Get("Authorization"); got != "Bearer ghs_test_token_123" {
		t.Errorf("Authorization = %q", got)
	}
	if c := calls.Load(); c != 1 {
		t.Errorf("expected 1 call, got %d", c)
	}
}

func TestGitHubAppJWTClaims(t *testing.T) {
	key, pemBytes := generateTestKey(t)

	srv := newGitHubTestServer(t, func(r *http.Request) (any, int) {
		claims := validateJWT(t, r, &key.PublicKey)

		// iat should be backdated ~60s.
		iatFloat, ok := claims["iat"].(float64)
		if !ok {
			t.Fatal("iat not a float64")
		}
		iat := time.Unix(int64(iatFloat), 0)
		drift := time.Since(iat)
		if drift < 50*time.Second || drift > 70*time.Second {
			t.Errorf("iat drift = %v, want ~60s", drift)
		}

		// exp should be ~10m from now.
		expFloat, ok := claims["exp"].(float64)
		if !ok {
			t.Fatal("exp not a float64")
		}
		exp := time.Unix(int64(expFloat), 0)
		remaining := time.Until(exp)
		if remaining < 9*time.Minute || remaining > 11*time.Minute {
			t.Errorf("exp remaining = %v, want ~10m", remaining)
		}

		return installationTokenResponse{ //nolint:gosec // test token //nolint:gosec // test token
			Token:     "ghs_claims_test",
			ExpiresAt: time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
		}, 201
	})

	p := newGitHubAppProvider(t, srv, pemBytes)
	_, err := p.Authenticate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
}

func TestGitHubAppTokenReuse(t *testing.T) {
	_, pemBytes := generateTestKey(t)

	var calls atomic.Int32
	srv := newGitHubTestServer(t, func(_ *http.Request) (any, int) {
		calls.Add(1)
		return installationTokenResponse{ //nolint:gosec // test token //nolint:gosec // test token
			Token:     "ghs_cached",
			ExpiresAt: time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
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
	_, pemBytes := generateTestKey(t)

	var calls atomic.Int32
	srv := newGitHubTestServer(t, func(_ *http.Request) (any, int) {
		n := calls.Add(1)
		return installationTokenResponse{ //nolint:gosec // test token
			Token:     fmt.Sprintf("ghs_tok_%d", n),
			ExpiresAt: time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
		}, 201
	})
	p := newGitHubAppProvider(t, srv, pemBytes)

	h1, _ := p.Authenticate(context.Background(), nil)

	// Force expiry.
	p.mu.Lock()
	p.expiry = time.Now().Add(-1 * time.Second)
	p.mu.Unlock()

	h2, _ := p.Authenticate(context.Background(), nil)

	if calls.Load() != 2 {
		t.Errorf("expected 2 refreshes, got %d", calls.Load())
	}
	if h1.Get("Authorization") == h2.Get("Authorization") {
		t.Error("expected different tokens after refresh")
	}
}

func TestGitHubAppHTTPErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    map[string]string
		wantErr string
	}{
		{"401 bad credentials", 401, map[string]string{"message": "Bad credentials"}, "Bad credentials"},
		{"403 suspended", 403, map[string]string{"message": "This installation has been suspended"}, "suspended"},
		{"404 not found", 404, map[string]string{"message": "Not Found"}, "Not Found"},
		{"422 validation", 422, map[string]string{"message": "Invalid request"}, "Invalid request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, pemBytes := generateTestKey(t)
			srv := newGitHubTestServer(t, func(_ *http.Request) (any, int) {
				return tt.body, tt.status
			})
			p := newGitHubAppProvider(t, srv, pemBytes)

			_, err := p.Authenticate(context.Background(), nil)
			if err == nil {
				t.Fatalf("expected error on %d", tt.status)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", tt.status)) {
				t.Errorf("error = %q, want HTTP %d", err, tt.status)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestGitHubAppEmptyToken(t *testing.T) {
	_, pemBytes := generateTestKey(t)

	srv := newGitHubTestServer(t, func(_ *http.Request) (any, int) {
		return installationTokenResponse{ //nolint:gosec // test token
			Token:     "",
			ExpiresAt: time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
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
	_, pemBytes := generateTestKey(t)

	srv := newGitHubTestServer(t, func(_ *http.Request) (any, int) {
		return map[string]string{"token": "ghs_tok"}, 201
	})
	p := newGitHubAppProvider(t, srv, pemBytes)

	_, err := p.Authenticate(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error on missing expires_at")
	}
	if !strings.Contains(err.Error(), "expires_at") {
		t.Errorf("error = %q", err)
	}
}

func TestGitHubAppInvalidExpiresAt(t *testing.T) {
	_, pemBytes := generateTestKey(t)

	srv := newGitHubTestServer(t, func(_ *http.Request) (any, int) {
		return installationTokenResponse{ //nolint:gosec // test token
			Token:     "ghs_tok",
			ExpiresAt: "not-a-date",
		}, 201
	})
	p := newGitHubAppProvider(t, srv, pemBytes)

	_, err := p.Authenticate(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error on invalid expires_at")
	}
	if !strings.Contains(err.Error(), "expires_at") {
		t.Errorf("error = %q", err)
	}
}

func TestGitHubAppExpiryComputed(t *testing.T) {
	_, pemBytes := generateTestKey(t)

	srv := newGitHubTestServer(t, func(_ *http.Request) (any, int) {
		return installationTokenResponse{ //nolint:gosec // test token
			Token:     "ghs_tok",
			ExpiresAt: time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
		}, 201
	})
	p := newGitHubAppProvider(t, srv, pemBytes)

	_, err := p.Authenticate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	p.mu.Lock()
	remaining := time.Until(p.expiry)
	p.mu.Unlock()

	// Should be ~54min (90% of 60min).
	expected := float64(1*time.Hour) * 0.9
	if math.Abs(float64(remaining)-expected) > float64(2*time.Minute) {
		t.Errorf("expiry in %v, expected ~%v", remaining, time.Duration(expected))
	}
}

func TestGitHubAppConcurrentAccess(t *testing.T) {
	_, pemBytes := generateTestKey(t)

	var calls atomic.Int32
	srv := newGitHubTestServer(t, func(_ *http.Request) (any, int) {
		calls.Add(1)
		return installationTokenResponse{ //nolint:gosec // test token
			Token:     "ghs_concurrent",
			ExpiresAt: time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
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
			if h.Get("Authorization") != "Bearer ghs_concurrent" {
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
		t.Errorf("expected 1 refresh, got %d", c)
	}
}

func TestGitHubAppContextCancellation(t *testing.T) {
	_, pemBytes := generateTestKey(t)

	arrived := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		close(arrived)
		time.Sleep(2 * time.Second)
	}))
	t.Cleanup(srv.Close)

	p := newGitHubAppProvider(t, srv, pemBytes)

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

func TestGitHubAppTruncateBody(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		maxLen int
		want   string
	}{
		{"short", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"truncated", "hello world", 5, "hello..."},
		{"empty", "", 5, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateBody([]byte(tt.body), tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateBody(%q, %d) = %q, want %q", tt.body, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestGitHubAppPKCS8Key(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: pkcs8Bytes,
	})

	srv := newGitHubTestServer(t, func(_ *http.Request) (any, int) {
		return installationTokenResponse{ //nolint:gosec // test token
			Token:     "ghs_pkcs8",
			ExpiresAt: time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
		}, 201
	})

	p := newGitHubAppProvider(t, srv, pemBytes)

	h, err := p.Authenticate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got := h.Get("Authorization"); got != "Bearer ghs_pkcs8" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestGitHubAppTrailingPEMData(t *testing.T) {
	_, pemBytes := generateTestKey(t)
	doubled := slices.Concat(pemBytes, pemBytes)

	_, err := NewGitHubAppProvider("123", "456", doubled, "https://api.github.com", http.DefaultTransport)
	if err == nil {
		t.Fatal("expected error for trailing PEM data")
	}
	if !strings.Contains(err.Error(), "trailing data") {
		t.Errorf("error = %q, want trailing data error", err)
	}
}

func TestGitHubAppMalformedJSON(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(201)
		_, _ = fmt.Fprint(w, "<html>Bad Gateway</html>")
	}))
	t.Cleanup(srv.Close)

	_, pemBytes := generateTestKey(t)
	pemCopy := make([]byte, len(pemBytes))
	copy(pemCopy, pemBytes)

	p, err := NewGitHubAppProvider("123", "456", pemCopy, srv.URL, srv.Client().Transport)
	if err != nil {
		t.Fatalf("NewGitHubAppProvider: %v", err)
	}
	p.client = srv.Client()

	_, err = p.Authenticate(context.Background(), nil)
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
	pemCopy := make([]byte, len(pemBytes))
	copy(pemCopy, pemBytes)

	p, err := NewGitHubAppProvider("123", "456", pemCopy, srv.URL, srv.Client().Transport)
	if err != nil {
		t.Fatalf("NewGitHubAppProvider: %v", err)
	}
	p.client = srv.Client()

	_, err = p.Authenticate(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error on oversized response")
	}
}

func TestGitHubAppNegativeExpiryFloor(t *testing.T) {
	_, pemBytes := generateTestKey(t)

	srv := newGitHubTestServer(t, func(_ *http.Request) (any, int) {
		return installationTokenResponse{ //nolint:gosec // test token
			Token:     "ghs_past_expiry",
			ExpiresAt: time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339),
		}, 201
	})
	p := newGitHubAppProvider(t, srv, pemBytes)

	_, err := p.Authenticate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	p.mu.Lock()
	remaining := time.Until(p.expiry)
	p.mu.Unlock()

	// Floor is 5 minutes × 0.9 = 4.5 minutes.
	if remaining < 4*time.Minute || remaining > 5*time.Minute {
		t.Errorf("expiry in %v, expected ~4.5min (floored)", remaining)
	}
}

// --- Private key file loading tests ---

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

	dir := t.TempDir()
	path := filepath.Join(dir, "app.pem")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := SingleAuthConfig{
		Type:           "github_app",
		AppID:          "app-from-file",
		InstallationID: "789",
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
	if gap.privateKey.N.Cmp(key.N) != 0 {
		t.Error("private key does not match file key")
	}
}
