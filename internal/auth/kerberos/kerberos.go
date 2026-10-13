// Package kerberos provides GSSAPI/SPNEGO authentication for HTTP requests.
//
// Platform support:
//   - Linux: dlopen libgssapi_krb5.so.2 (via sassoftware/gssapi, no link-time CGO)
//   - macOS: CGO linking GSS.framework (uses pure Kerberos V5 instead of SPNEGO
//     because GSS.framework/Heimdal does not properly support SPNEGO)
//
// Credentials are acquired fresh on each call from the system's default
// credential cache, so kinit renewals are picked up automatically.
package kerberos

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type redirectOriginKey struct{}

// WithRedirectOrigin records the initial request's hostname for
// cross-host redirect detection. Set by the proxy before client.Do().
func WithRedirectOrigin(ctx context.Context, hostname string) context.Context {
	return context.WithValue(ctx, redirectOriginKey{}, hostname)
}

func redirectOriginFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(redirectOriginKey{}).(string); ok {
		return v
	}
	return ""
}

const (
	skipHostTTL             = 5 * time.Minute
	skipHostTimeoutTTL      = 30 * time.Second
	defaultProactiveTimeout = 2 * time.Second
)

// skipEntry records when and why a host was added to skipHosts.
type skipEntry struct {
	when    time.Time
	timeout bool // true if skipped due to timeout (shorter TTL)
}

// SPNEGORoundTripper wraps an http.RoundTripper to add SPNEGO
// authentication. Uses proactive-first strategy: sends the Negotiate
// header on the first request. Falls back to reactive 401
// challenge-response if proactive token generation fails.
//
// Proactive auth avoids mod_auth_gssapi CSRF protection issues on
// POST requests (e.g., FreeIPA). Reactive fallback handles SSO
// redirect flows where the initial host has no SPN.
//
// Mutual authentication is OPTIONAL — the server's proof token in
// 200 responses is not verified. TLS provides server authentication.
//
// If spn is empty, the SPN is derived dynamically as "HTTP@<hostname>"
// from each request's URL. If the GSSAPI call fails (e.g., no SPN
// registered in the KDC for that hostname), the request proceeds
// without a Negotiate header — this allows redirect-based flows
// (like OIDC) where the initial host has no SPN but the SSO server
// does.
//
// The proactive SPNEGO attempt has a timeout (default 2s) to avoid
// blocking on unreachable KDCs. The timeout is a tradeoff between
// responsiveness (normal TGS exchange is <100ms) and resilience to
// network jitter. The reactive 401 path is unaffected by this timeout.
type SPNEGORoundTripper struct {
	spn              string
	next             http.RoundTripper
	proactiveTimeout time.Duration
	proactive        bool     // false = reactive-only (skip proactive SPNEGO)
	skipHosts        sync.Map // hostname -> skipEntry (expiry after TTL)
}

// NewSPNEGORoundTripper creates a new round tripper that adds SPNEGO headers.
// If spn is empty, the SPN is derived from each request's hostname.
// proactiveTimeout caps the time spent on proactive GSSAPI token generation;
// use 0 for the default (2s). When proactive is false, SPNEGO tokens are
// only sent after a 401 Negotiate challenge (useful for plugins that auth
// via SSO/SAML redirects rather than direct Kerberos).
func NewSPNEGORoundTripper(spn string, next http.RoundTripper, proactiveTimeout time.Duration, proactive bool) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	if proactiveTimeout <= 0 {
		proactiveTimeout = defaultProactiveTimeout
	}
	return &SPNEGORoundTripper{
		spn:              spn,
		next:             next,
		proactiveTimeout: proactiveTimeout,
		proactive:        proactive,
	}
}

// RoundTrip implements http.RoundTripper with proactive-first SPNEGO.
//
// Flow:
//  1. Try to generate a SPNEGO token and send it on the FIRST request
//  2. If token generation fails (no SPN, no ticket, wrong realm),
//     send without auth — hostname is cached to skip future attempts
//  3. If the server returns 401 + WWW-Authenticate: Negotiate,
//     generate a fresh token and retry (reactive fallback)
func (s *SPNEGORoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	hostname := req.URL.Hostname()

	// Suppress Kerberos tokens after a cross-host redirect.
	if origin := redirectOriginFromContext(req.Context()); origin != "" && origin != hostname {
		log.Printf("kerberos: suppressing token for %s (redirected from %s)", hostname, origin) //nolint:gosec // hostname from parsed URL
		return s.next.RoundTrip(req)
	}

	spn := s.spn
	if spn == "" {
		spn = "HTTP@" + hostname
	}

	// Proactive: try to attach Negotiate token on first request.
	// Skip if proactive is disabled or this host previously failed.
	authReq := req.Clone(req.Context())

	skip := !s.proactive
	if !skip {
		if v, ok := s.skipHosts.Load(hostname); ok {
			e := v.(skipEntry)
			ttl := skipHostTTL
			if e.timeout {
				ttl = skipHostTimeoutTTL
			}
			if time.Since(e.when) < ttl {
				skip = true
			} else {
				s.skipHosts.Delete(hostname)
			}
		}
	}
	if !skip {
		// Run GetSPNEGOToken in a goroutine with a timeout to avoid
		// blocking on unreachable KDCs. gss_init_sec_context has no
		// cancellation mechanism, so the goroutine may leak briefly
		// (~5s) on timeout — this is bounded and acceptable.
		//
		// The goroutine returns the token via channel; the header is
		// only set here in the main goroutine to avoid a data race.
		// Timeouts use a shorter skipHosts TTL (30s) than definitive
		// GSSAPI errors (5m). A timeout is "unknown" — the KDC may
		// be transiently slow. The short TTL prevents repeated 2s
		// penalties on multi-request init flows while allowing
		// recovery once the KDC becomes reachable.
		type spnegoResult struct {
			token string
			err   error
		}
		ch := make(chan spnegoResult, 1)
		go func() {
			token, err := GetSPNEGOToken(spn)
			ch <- spnegoResult{token, err}
		}()
		select {
		case r := <-ch:
			if r.err != nil {
				log.Printf("kerberos: proactive SPNEGO skipped for %s: %v", //nolint:gosec // hostname from parsed URL
					hostname, r.err)
				s.skipHosts.Store(hostname, skipEntry{when: time.Now()})
			} else {
				authReq.Header.Set("Authorization", "Negotiate "+r.token)
			}
		case <-time.After(s.proactiveTimeout):
			log.Printf("kerberos: proactive SPNEGO timed out after %s for %s", //nolint:gosec
				s.proactiveTimeout, hostname)
			s.skipHosts.Store(hostname, skipEntry{when: time.Now(), timeout: true})
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}

	resp, err := s.next.RoundTrip(authReq)
	if err != nil {
		return nil, err
	}

	// If not 401, we're done (auth succeeded or not needed)
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	// 401 received — check for Negotiate challenge
	authHeader := resp.Header.Get("WWW-Authenticate")
	log.Printf("kerberos: 401 received for %s, WWW-Authenticate: %q", hostname, authHeader) //nolint:gosec
	if !strings.Contains(authHeader, "Negotiate") {
		log.Printf("kerberos: no Negotiate challenge for %s", hostname) //nolint:gosec
		return resp, nil
	}

	log.Printf("kerberos: Negotiate challenge found for %s, attempting reactive fallback", hostname) //nolint:gosec
	// Reactive fallback: server wants challenge-response.
	// Creates a fresh GSSAPI context (standard for HTTP SPNEGO).
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	retryReq := req.Clone(req.Context())
	if err := resetBody(retryReq, req); err != nil {
		return nil, err
	}

	if err := SetSPNEGOHeader(retryReq, spn); err != nil {
		log.Printf("kerberos: SPNEGO failed for %s after 401: %v", //nolint:gosec
			hostname, err)
		fallbackReq := req.Clone(req.Context())
		_ = resetBody(fallbackReq, req)
		return s.next.RoundTrip(fallbackReq)
	}

	log.Printf("kerberos: reactive auth for %s after 401", hostname) //nolint:gosec
	return s.next.RoundTrip(retryReq)
}

// resetBody obtains a fresh body reader for a cloned request.
// After the first RoundTrip consumes the body's io.Reader, Clone()
// inherits the exhausted reader. GetBody() provides a fresh copy,
// matching what http.Client does for redirect replays.
func resetBody(cloned, orig *http.Request) error {
	if orig.GetBody == nil {
		return nil // no body or body doesn't support replay
	}
	body, err := orig.GetBody()
	if err != nil {
		return fmt.Errorf("kerberos: reset body for retry: %w", err)
	}
	cloned.Body = body
	return nil
}
