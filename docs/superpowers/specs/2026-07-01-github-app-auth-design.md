# GitHub App Auth Provider — Design Spec

**Issue**: [#126](https://github.com/LeGambiArt/wtmcp/issues/126)
**Date**: 2026-07-01
**Approach**: Single-struct provider (Approach A)

## Overview

Add a `github_app` auth type to wtmcp core that authenticates using GitHub App installation tokens. The provider handles JWT signing, token exchange, in-memory caching, and auto-refresh — following the same self-contained pattern as `RefreshTokenProvider`.

## Provider Struct

```go
type GitHubAppProvider struct {
    mu             sync.Mutex
    appID          string
    installationID string
    privateKey     *rsa.PrivateKey
    baseURL        string           // defaults to https://api.github.com
    accessToken    string
    expiry         time.Time
    client         *http.Client
}
```

### Constructor

```go
func NewGitHubAppProvider(appID, installationID string, privateKeyPEM []byte, baseURL string, transport http.RoundTripper) (*GitHubAppProvider, error)
```

- Parses PEM, extracts RSA private key, validates it — fails fast on invalid key
- Defaults `baseURL` to `https://api.github.com` if empty
- Wraps `transport` in `&http.Client{}`

### Interface Implementation

- `Name()` → `"github_app"`
- `Available()` → `privateKey != nil && appID != "" && installationID != ""`
- `Authenticate(ctx, req)` → returns `Authorization: Bearer <installation_token>`

## Authentication Flow

1. Lock mutex, check if `accessToken` is non-empty and within 90% of lifetime
2. If valid: return cached token as `Authorization: Bearer <token>`
3. If expired/missing, call `refreshLocked(ctx)`:
   a. **Create JWT** (RS256 via `golang-jwt/jwt/v5`):
      - `iss`: app ID (GitHub accepts both App ID and Client ID)
      - `iat`: `now - 60s` (clock skew tolerance per GitHub docs)
      - `exp`: `now + 10m` (maximum allowed by GitHub)
      - Header: `alg: RS256`, `typ: JWT`
   b. **Exchange for installation token**:
      - `POST {baseURL}/app/installations/{installationID}/access_tokens`
      - Headers: `Authorization: Bearer <jwt>`, `Accept: application/vnd.github+json`
   c. **Parse response**: extract `token` and `expires_at` from JSON body
   d. **Cache**: store `accessToken` and parsed `expiry`
   e. **Return**: `Authorization: Bearer <accessToken>`

### Error Handling

- JWT signing failure → error (should not happen if key valid at construction)
- HTTP non-201 → error with status code and body context
  - 401: invalid/expired JWT
  - 403: insufficient permissions or suspended installation
  - 404: installation not found
  - 422: validation failed
- Empty `token` in response → error
- Missing `expires_at` in response → error
- Response body capped at 1MB (same as `refresh_token` provider)

## Token Caching

- **In-memory only** — no disk persistence
- Refresh trigger: 90% of token lifetime elapsed (consistent with `refresh_token` provider)
- GitHub installation tokens have a 1-hour lifetime, so refresh happens at ~54 minutes

## Private Key Loading

Two sources, file takes precedence:

1. **`private_key_file`**: path relative to credentials dir. Uses existing `resolveCredentialPath()` + `config.RejectSymlink()` + `config.CheckPermissions()` (0600 enforcement)
2. **`private_key`**: raw PEM content from env var (fallback if file not specified)

If both are empty, `Available()` returns false (skipped during auto-detect).

## Plugin YAML Config

```yaml
services:
  auth:
    type: github_app
    app_id: "${GITHUB_APP_ID}"
    installation_id: "${GITHUB_INSTALLATION_ID}"
    private_key_file: "github-app.pem"          # relative to credentials dir
    # OR
    private_key: "${GITHUB_APP_PRIVATE_KEY}"     # raw PEM content
  http:
    base_url: "${GITHUB_URL:-https://api.github.com}"
```

## Integration Points

### New Files

| File | Purpose |
|------|---------|
| `internal/auth/github_app.go` | Provider struct, constructor, Authenticate, JWT creation, token exchange |
| `internal/auth/github_app_test.go` | Full test suite |

### Modified Files

| File | Change |
|------|--------|
| `internal/auth/variants.go` | Add `"github_app"` to `KnownProviderTypes`; add `AppID`, `InstallationID`, `PrivateKey`, `PrivateKeyFile` to `SingleAuthConfig`; add case to `providerFromConfig()` |
| `internal/plugin/manifest.go` | Add `AppID`, `InstallationID`, `PrivateKey`, `PrivateKeyFile` to `AuthServiceConfig` |
| `internal/plugin/manager.go` | Map new `AuthServiceConfig` fields to `SingleAuthConfig` in `resolveAuth()` (single config and variant config paths) |
| `go.mod` / `go.sum` | Add `github.com/golang-jwt/jwt/v5` |

## Dependencies

- **New**: `github.com/golang-jwt/jwt/v5` — JWT creation and RS256 signing
- **Stdlib**: `crypto/rsa`, `crypto/x509`, `encoding/pem` — private key parsing

## Test Plan

Test server: `httptest.NewTLSServer` mocking GitHub's installation token endpoint.

### Token Exchange
- Successful exchange — verify request path, headers (`Accept`, `Authorization`), parse `token` + `expires_at`
- Token caching — second `Authenticate()` reuses cached token, no HTTP call
- Auto-refresh at 90% of lifetime — third call after time advance triggers new exchange

### JWT Validation (in test server)
- `iss` matches configured app ID
- `iat` is backdated ~60 seconds from request time
- `exp` is within 10 minutes of `iat`
- Algorithm is RS256

### Error Responses
- HTTP 401 → error (invalid JWT)
- HTTP 403 → error (suspended installation)
- HTTP 404 → error (bad installation ID)
- HTTP 422 → error (validation failed)
- Empty `token` in 201 response → error
- Missing `expires_at` in response → error
- Oversized response (>1MB) → error

### Key Loading
- Invalid private key at construction → error
- Private key from file with permission checks (0600)
- Private key from file with symlink rejection
- Private key from raw PEM content
- File takes precedence over raw PEM

### Provider Interface
- `Available()` returns false when no key provided
- `Name()` returns `"github_app"`

### Concurrency
- Concurrent `Authenticate()` calls with `sync.WaitGroup` — mutex correctness

### Context
- Context cancellation during HTTP request → error

## References

- [GitHub: Generating a JWT for a GitHub App](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app)
- [GitHub: Create an installation access token](https://docs.github.com/en/rest/apps/apps?apiVersion=2022-11-28#create-an-installation-access-token)
