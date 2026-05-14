// Package proxy provides an HTTP proxy that makes authenticated
// requests on behalf of plugins. Auth headers, retries, rate limiting,
// and response body limits are handled centrally.
package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"

	"math/rand"
	"strconv"

	"github.com/LeGambiArt/wtmcp/internal/audit"
	"github.com/LeGambiArt/wtmcp/internal/auth"
	"github.com/LeGambiArt/wtmcp/internal/auth/kerberos"
	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/domaincheck"
	"github.com/LeGambiArt/wtmcp/internal/protocol"
	"github.com/LeGambiArt/wtmcp/internal/ratelimit"
)

const (
	// maxDynamicDomains is the maximum number of domains that can be added
	// to the allowlist in a single request. This prevents a single malicious
	// request from flooding the allowlist.
	maxDynamicDomains = 10

	// maxTotalDomains is the cumulative maximum number of domains allowed
	// per plugin across all requests. This prevents allowlist bloat and
	// DoS via unlimited domain accumulation across multiple requests.
	maxTotalDomains = 50
)

// TLSConfig holds per-plugin TLS settings for custom CAs and mTLS.
// Populated by the plugin manager after resolving env vars.
type TLSConfig struct {
	CACert             string // resolved path to PEM CA cert file
	ClientCert         string // resolved path to PEM client cert file
	ClientKey          string // resolved path to PEM client key file
	SkipHostnameVerify bool

	// CACertPEM holds pre-loaded CA cert bytes to prevent TOCTOU.
	CACertPEM []byte
}

// HasConfig returns true if any TLS setting is configured.
func (t TLSConfig) HasConfig() bool {
	return len(t.CACertPEM) > 0 || t.ClientCert != "" || t.SkipHostnameVerify
}

// PluginAuth holds the resolved auth and HTTP config for a plugin.
type PluginAuth struct {
	Provider        auth.Provider
	BaseURL         string
	AllowedDomains  []string
	AllowPrivateIPs bool
	TLS             TLSConfig

	// BaseDomains holds the trusted anchor domains registered at plugin
	// init time (from plugin.yaml allowed_domains or init_ok response).
	// Per-request domain additions must be subdomains of these base domains
	// to prevent credential exfiltration to attacker-controlled servers.
	BaseDomains []string

	// Client is an optional per-plugin HTTP client. When set (e.g., for
	// Kerberos or mTLS plugins), it is used instead of the shared proxy
	// client.
	Client     *http.Client
	IsKerberos bool // true when Client uses SPNEGO transport
}

// Proxy executes HTTP requests on behalf of plugins, injecting
// authentication headers and enforcing security policies.
type Proxy struct {
	pluginsMu     sync.RWMutex
	plugins       map[string]*PluginAuth
	client        *http.Client
	privateClient *http.Client // for plugins with allow_private_ips
	maxBodySize   int64
	auditor       *audit.Logger
	rateLimiter   *ratelimit.Registry
	retryConfig   config.RetryConfig
	sleepFn       func(context.Context, time.Duration) error // nil → sleepContext
}

// New creates a Proxy with the given HTTP client and max response body size.
// When client is nil, a default client with SSRF-safe dialer is used.
// A second internal client with relaxed SSRF policy is created for
// plugins that declare allow_private_ips (with required allowed_domains).
// The timeout is applied as a hard wall-clock deadline on all HTTP requests.
func New(client *http.Client, maxBodySize int64, timeout time.Duration) *Proxy {
	if client == nil {
		client = &http.Client{
			Transport:     safeTransport(false),
			Timeout:       timeout,
			CheckRedirect: StripAuthOnCrossDomainRedirect,
		}
	}
	return &Proxy{
		plugins: make(map[string]*PluginAuth),
		client:  client,
		privateClient: &http.Client{
			Transport:     safeTransport(true),
			Timeout:       timeout,
			CheckRedirect: StripAuthOnCrossDomainRedirect,
		},
		maxBodySize: maxBodySize,
	}
}

// SetAuditor configures the audit logger for HTTP request logging.
func (p *Proxy) SetAuditor(auditor *audit.Logger) {
	p.auditor = auditor
}

// SetRateLimiter configures per-domain rate limiting.
func (p *Proxy) SetRateLimiter(rl *ratelimit.Registry) {
	p.rateLimiter = rl
}

// SetRetryConfig configures HTTP retry behavior for transient errors.
func (p *Proxy) SetRetryConfig(cfg config.RetryConfig) {
	p.retryConfig = cfg
}

// AddAllowedDomains appends dynamically discovered domains to a
// plugin's proxy allowlist. Must be called before the plugin starts
// accepting tool calls (i.e., in the sequential result-collection
// phase after Start() returns).
//
// Enforces a cumulative cap of maxTotalDomains per plugin to prevent
// allowlist bloat and DoS via domain accumulation across multiple requests.
func (p *Proxy) AddAllowedDomains(pluginName string, domains []string) {
	p.pluginsMu.Lock()
	defer p.pluginsMu.Unlock()

	pa, ok := p.plugins[pluginName]
	if !ok {
		log.Printf("proxy: AddAllowedDomains for unknown plugin %q", pluginName)
		return
	}

	for _, d := range domains {
		// Check cumulative cap before adding
		if len(pa.AllowedDomains) >= maxTotalDomains {
			log.Printf("[%s] WARNING: domain allowlist cap reached (%d domains), rejecting %q",
				pluginName, maxTotalDomains, d)
			continue
		}

		// Use domaincheck.Contains for normalized comparison
		if !domaincheck.Contains(pa.AllowedDomains, d) {
			pa.AllowedDomains = append(pa.AllowedDomains, d)
		}
	}
}

// AddBaseDomains adds domains to both AllowedDomains and BaseDomains.
// This is used by the init-time registration path (registerDynamicDomains)
// to add domains from init_ok responses. These domains become trusted
// anchors for validating per-request domain additions.
func (p *Proxy) AddBaseDomains(pluginName string, domains []string) {
	p.pluginsMu.Lock()
	defer p.pluginsMu.Unlock()

	pa, ok := p.plugins[pluginName]
	if !ok {
		log.Printf("proxy: AddBaseDomains for unknown plugin %q", pluginName)
		return
	}
	for _, d := range domains {
		// Use domaincheck.Contains for normalized comparison
		if !domaincheck.Contains(pa.AllowedDomains, d) {
			pa.AllowedDomains = append(pa.AllowedDomains, d)
		}
		if !domaincheck.Contains(pa.BaseDomains, d) {
			pa.BaseDomains = append(pa.BaseDomains, d)
		}
	}
}

// SetPluginProvider replaces the auth provider for a plugin. Used by
// the manager to install a DomainProvider after init_ok auth bindings
// are resolved. Must be called after RegisterPlugin.
func (p *Proxy) SetPluginProvider(pluginName string, provider auth.Provider) {
	p.pluginsMu.Lock()
	defer p.pluginsMu.Unlock()

	pa, ok := p.plugins[pluginName]
	if !ok {
		log.Printf("proxy: SetPluginProvider for unknown plugin %q", pluginName)
		return
	}
	pa.Provider = provider
}

func (p *Proxy) auditHTTP(ctx context.Context, pluginName, method, rawURL string, status int, size int64) {
	if p.auditor == nil {
		return
	}
	parsed, _ := url.Parse(rawURL)
	host := ""
	path := ""
	if parsed != nil {
		host = parsed.Host
		path = parsed.Path
	}
	p.auditor.HTTPRequest(ctx, pluginName, method, host, path, status, size)
}

// RegisterPlugin associates auth and HTTP config with a plugin name.
func (p *Proxy) RegisterPlugin(name string, pa *PluginAuth) {
	p.pluginsMu.Lock()
	// Initialize BaseDomains with the static allowed_domains from plugin.yaml.
	// These serve as trusted anchors for per-request domain validation.
	pa.BaseDomains = append([]string(nil), pa.AllowedDomains...)
	p.plugins[name] = pa
	p.pluginsMu.Unlock()
}

// UnregisterPlugin removes a plugin's auth and HTTP config.
func (p *Proxy) UnregisterPlugin(name string) {
	p.pluginsMu.Lock()
	delete(p.plugins, name)
	p.pluginsMu.Unlock()
}

// NewKerberosClient creates an HTTP client with a cookie jar and
// SPNEGORoundTripper for Kerberos-authenticated plugins. If spn is
// empty, the SPN is derived dynamically from each request's hostname.
// When proactive is false, SPNEGO tokens are only sent after a 401
// challenge (reactive-only mode). The TLS config enables custom CA
// certs alongside Kerberos auth.
func NewKerberosClient(spn string, proactive bool, allowPrivateIPs bool, tlsCfg TLSConfig, timeout time.Duration) (*http.Client, error) {
	transport, err := SafeTransportWithTLS(allowPrivateIPs, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("create TLS transport: %w", err)
	}
	jar, _ := cookiejar.New(nil) // cookiejar.New only errors with non-nil options
	return &http.Client{
		Jar:           jar,
		Transport:     kerberos.NewSPNEGORoundTripper(spn, transport, 0, proactive),
		Timeout:       timeout,
		CheckRedirect: StripAuthOnCrossDomainRedirect,
	}, nil
}

// Execute handles an http_request message from a plugin.
func (p *Proxy) Execute(ctx context.Context, pluginName string, req protocol.Message) protocol.Message {
	p.pluginsMu.RLock()
	ptr, ok := p.plugins[pluginName]
	if !ok {
		p.pluginsMu.RUnlock()
		return errResponse(req.ID, "no_config", "no HTTP config registered for plugin "+pluginName)
	}
	// Snapshot value types and clone AllowedDomains and BaseDomains so
	// Execute works on immutable data after releasing the lock. Provider
	// and Client are shared by design: both are concurrency-safe
	// (write-once after init, and http.Client is documented thread-safe).
	paCopy := *ptr
	pa := &paCopy
	pa.AllowedDomains = append([]string(nil), ptr.AllowedDomains...)
	baseDomains := append([]string(nil), ptr.BaseDomains...)
	p.pluginsMu.RUnlock()

	if len(req.Domains) > 0 {
		// Cap at maxDynamicDomains to prevent unbounded allowlist growth
		domains := req.Domains
		if len(domains) > maxDynamicDomains {
			log.Printf("[%s] WARNING: request domains capped from %d to %d",
				pluginName, len(domains), maxDynamicDomains)
			domains = domains[:maxDynamicDomains]
		}

		// Validate each domain before adding to allowlist:
		// 1. Basic validation (no IPs, localhost, wildcards, etc.)
		// 2. Subdomain validation (must be subdomain of a base domain)
		var valid []string
		for _, d := range domains {
			if err := domaincheck.Validate(d); err != nil {
				log.Printf("[%s] rejected request domain %q: %v", pluginName, d, err)
				continue
			}

			// Verify the domain is a subdomain of (or exact match to) a base domain.
			// This prevents credential exfiltration to arbitrary attacker domains.
			if !domaincheck.IsSubdomain(d, baseDomains) {
				log.Printf("[%s] rejected request domain %q: not a subdomain of base domains %v",
					pluginName, d, baseDomains)
				continue
			}

			valid = append(valid, d)
		}

		if len(valid) > 0 {
			p.AddAllowedDomains(pluginName, valid)
			p.pluginsMu.RLock()
			if ptr, ok := p.plugins[pluginName]; ok {
				pa.AllowedDomains = append([]string(nil), ptr.AllowedDomains...)
			}
			p.pluginsMu.RUnlock()
		}
	}

	fullURL, err := p.resolveURL(pluginName, pa, req)
	if err != nil {
		return errResponse(req.ID, "invalid_url", err.Error())
	}

	if p.rateLimiter != nil {
		parsed, _ := url.Parse(fullURL)
		if parsed != nil {
			domain := parsed.Hostname()
			if d := p.rateLimiter.Allow(domain); d > 0 {
				return errResponse(req.ID, "rate_limited",
					fmt.Sprintf("domain %s rate limited — retry after %s", domain, d.Truncate(time.Millisecond)))
			}
		}
	}

	method := strings.ToUpper(req.Method)
	if method == "" {
		method = http.MethodGet
	}
	if access := toolAccessFromContext(ctx); access == "read" && !isReadOnlyMethod(method) {
		return errResponse(req.ID, "method_not_allowed",
			fmt.Sprintf("read-only tool cannot use %s method", method))
	}

	// Select HTTP client (used for all attempts).
	//
	// no_auth bypasses Kerberos client and auth injection but keeps
	// the TLS-aware client for HTTPS (custom CA / client certs).
	// mTLS plugins cannot downgrade to HTTP via no_auth.
	selectedClient := p.selectClient(pa, req.NoAuth)

	maxRetries := p.retryConfig.Max
	if maxRetries <= 0 || !isIdempotent(method) {
		maxRetries = 0
	}

	var resp *http.Response
	var doErr error
	var lastStatus int
	var lastRetryAfter string

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := retryDelay(attempt, lastRetryAfter)
			sleep := p.sleepFn
			if sleep == nil {
				sleep = sleepContext
			}
			if err := sleep(ctx, delay); err != nil {
				return errResponse(req.ID, "request_cancelled", "retry cancelled")
			}
			log.Printf("[%s] retry %d/%d after %d from %s",
				pluginName, attempt, maxRetries, lastStatus, fullURL)
		}

		httpReq, err := p.buildRequest(ctx, fullURL, req)
		if err != nil {
			return errResponse(req.ID, "build_request", err.Error())
		}

		if !req.NoAuth && pa.Provider != nil && pa.Client == nil {
			if err := p.injectAuth(ctx, pa.Provider, httpReq); err != nil {
				return errResponse(req.ID, "auth_failed", err.Error())
			}
		}

		resp, doErr = selectedClient.Do(httpReq)
		if doErr != nil {
			break
		}

		if attempt < maxRetries && shouldRetry(resp.StatusCode, p.retryConfig.RetryOn) {
			lastStatus = resp.StatusCode
			lastRetryAfter = resp.Header.Get("Retry-After")
			p.auditHTTP(ctx, pluginName, method, fullURL, resp.StatusCode, 0)
			resp.Body.Close() //nolint:errcheck,gosec // best-effort before retry
			continue
		}
		break
	}

	if doErr != nil {
		code := "transport_error"
		if ctx.Err() != nil {
			code = "request_cancelled"
		}
		p.auditHTTP(ctx, pluginName, method, fullURL, 0, 0)
		return protocol.Message{
			ID:     req.ID,
			Type:   protocol.TypeHTTPResponse,
			Status: 0,
			Error:  &protocol.Error{Code: code, Message: doErr.Error()},
		}
	}

	if pa.IsKerberos && pa.Client != nil && !req.NoAuth {
		resp = p.trySAMLSSO(ctx, pa, resp, pa.Client, fullURL, req)
	}

	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("proxy: failed to close response body: %v", err)
		}
	}()

	body, bodyEncoding, err := p.readBody(resp)
	if err != nil {
		p.auditHTTP(ctx, pluginName, method, fullURL, resp.StatusCode, 0)
		return errResponse(req.ID, "response_too_large", err.Error())
	}

	p.auditHTTP(ctx, pluginName, method, fullURL, resp.StatusCode, int64(len(body)))

	return protocol.Message{
		ID:           req.ID,
		Type:         protocol.TypeHTTPResponse,
		Status:       resp.StatusCode,
		Headers:      responseHeaders(resp),
		Body:         body,
		BodyEncoding: bodyEncoding,
		URL:          resp.Request.URL.String(),
	}
}

// selectClient picks the HTTP client for a request.
//
// no_auth bypasses the Kerberos client but keeps the TLS-aware
// client for HTTPS (preserves custom CA / client certs).
func (p *Proxy) selectClient(pa *PluginAuth, noAuth bool) *http.Client {
	switch {
	case noAuth && pa.Client != nil && !pa.IsKerberos:
		return pa.Client // mTLS client — keeps custom CA + client certs
	case noAuth && pa.AllowPrivateIPs:
		return p.privateClient
	case noAuth:
		return p.client
	case pa.Client != nil:
		return pa.Client // Kerberos or mTLS client
	case pa.AllowPrivateIPs:
		return p.privateClient
	default:
		return p.client
	}
}

func (p *Proxy) resolveURL(pluginName string, pa *PluginAuth, req protocol.Message) (string, error) {
	var fullURL string

	if req.URL != "" {
		if !p.isDomainAllowed(pluginName, pa, req.URL) {
			return "", fmt.Errorf("domain not allowed: %s", req.URL)
		}
		fullURL = req.URL
	} else {
		if pa.BaseURL == "" {
			return "", fmt.Errorf("no base_url configured and no full url provided")
		}
		joined, err := url.JoinPath(pa.BaseURL, req.Path)
		if err != nil {
			return "", fmt.Errorf("join path: %w", err)
		}
		fullURL = joined
	}

	parsed, err := url.Parse(fullURL)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}

	// Scheme validation — only http and https allowed
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q, only http/https allowed", parsed.Scheme)
	}

	// mTLS always requires HTTPS — no_auth cannot bypass this
	if pa.TLS.ClientCert != "" && parsed.Scheme != "https" {
		return "", fmt.Errorf("HTTPS required when client certificates are configured")
	}

	// Header-based auth requires HTTPS unless no_auth
	hasHeaderAuth := pa.Provider != nil || pa.IsKerberos
	if hasHeaderAuth && !req.NoAuth && parsed.Scheme != "https" {
		return "", fmt.Errorf("HTTPS required when auth is configured")
	}

	return fullURL, nil
}

func (p *Proxy) buildRequest(ctx context.Context, fullURL string, req protocol.Message) (*http.Request, error) {
	var bodyReader io.Reader
	var contentType string

	if len(req.Multipart) > 0 {
		var err error
		bodyReader, contentType, err = buildMultipart(req.Multipart)
		if err != nil {
			return nil, fmt.Errorf("build multipart: %w", err)
		}
	} else if req.Body != nil {
		switch {
		case req.BodyEncoding == "base64":
			var encoded string
			if err := json.Unmarshal(req.Body, &encoded); err != nil {
				return nil, fmt.Errorf("base64 body must be a JSON string: %w", err)
			}
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return nil, fmt.Errorf("decode base64 body: %w", err)
			}
			bodyReader = bytes.NewReader(decoded)
		case len(req.Body) > 0 && req.Body[0] == '"':
			var s string
			if err := json.Unmarshal(req.Body, &s); err == nil {
				bodyReader = strings.NewReader(s)
			} else {
				bodyReader = strings.NewReader(string(req.Body))
			}
		default:
			bodyReader = strings.NewReader(string(req.Body))
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, fullURL, bodyReader)
	if err != nil {
		return nil, err
	}

	// Add query params (supports string and []string values)
	if len(req.Query) > 0 {
		q := httpReq.URL.Query()
		for k, v := range req.Query {
			switch val := v.(type) {
			case string:
				q.Set(k, val)
			case []any:
				for _, item := range val {
					q.Add(k, fmt.Sprint(item))
				}
			default:
				q.Set(k, fmt.Sprint(v))
			}
		}
		httpReq.URL.RawQuery = q.Encode()
	}

	// Add plugin-specified headers, then strip security-sensitive ones
	// that plugins should not control.
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	stripDangerousHeaders(httpReq)

	// Proxy sets Content-Type for multipart (includes boundary).
	// Must come after plugin headers to override any plugin-set Content-Type.
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	} else if req.Body != nil && httpReq.Header.Get("Content-Type") == "" {
		// Default to application/json for requests with a body
		httpReq.Header.Set("Content-Type", "application/json")
	}

	return httpReq, nil
}

// buildMultipart assembles a multipart/form-data body from protocol parts.
func buildMultipart(parts []protocol.MultipartPart) (io.Reader, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	for _, part := range parts {
		var content []byte
		if part.BodyEncoding == "base64" {
			var err error
			content, err = base64.StdEncoding.DecodeString(part.Body)
			if err != nil {
				return nil, "", fmt.Errorf("base64 decode field %q: %w", part.Field, err)
			}
		} else {
			content = []byte(part.Body)
		}

		if part.Filename != "" {
			ct := part.ContentType
			if ct == "" {
				ct = "application/octet-stream"
			}
			h := make(textproto.MIMEHeader)
			h.Set("Content-Disposition",
				fmt.Sprintf(`form-data; name=%q; filename=%q`, part.Field, part.Filename))
			h.Set("Content-Type", ct)

			pw, err := w.CreatePart(h)
			if err != nil {
				return nil, "", fmt.Errorf("create file part %q: %w", part.Field, err)
			}
			if _, err := pw.Write(content); err != nil {
				return nil, "", fmt.Errorf("write file part %q: %w", part.Field, err)
			}
		} else {
			if err := w.WriteField(part.Field, string(content)); err != nil {
				return nil, "", fmt.Errorf("write field %q: %w", part.Field, err)
			}
		}
	}

	if err := w.Close(); err != nil {
		return nil, "", fmt.Errorf("close multipart writer: %w", err)
	}

	return &buf, w.FormDataContentType(), nil
}

func (p *Proxy) injectAuth(ctx context.Context, provider auth.Provider, httpReq *http.Request) error {
	authHeaders, err := provider.Authenticate(ctx, httpReq)
	if err != nil {
		return err
	}
	// Direct assign preserves multi-value headers and overwrites
	// any plugin-set auth headers.
	for k, vals := range authHeaders {
		httpReq.Header[k] = vals
	}
	return nil
}

func (p *Proxy) readBody(resp *http.Response) (json.RawMessage, string, error) {
	limited := io.LimitReader(resp.Body, p.maxBodySize+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", err
	}
	if int64(len(body)) > p.maxBodySize {
		return nil, "", fmt.Errorf("response body exceeds %d bytes", p.maxBodySize)
	}

	ct := resp.Header.Get("Content-Type")

	// JSON: return raw bytes as-is
	if strings.Contains(ct, "application/json") {
		return json.RawMessage(body), "", nil
	}

	// Text: return as quoted JSON string
	if strings.HasPrefix(ct, "text/") {
		b, err := json.Marshal(string(body))
		return json.RawMessage(b), "", err
	}

	// Binary (or unknown): base64-encode
	encoded := base64.StdEncoding.EncodeToString(body)
	b, err := json.Marshal(encoded)
	return json.RawMessage(b), "base64", err
}

// containsDomain is a wrapper around domaincheck.Contains for backward
// compatibility with tests. Uses normalized domain comparison.
func containsDomain(domains []string, d string) bool {
	return domaincheck.Contains(domains, d)
}

func (p *Proxy) isDomainAllowed(_ string, pa *PluginAuth, rawURL string) bool {
	reqURL, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	// Reject URLs with userinfo
	if reqURL.User != nil {
		return false
	}

	// AllowedDomains already includes the base_url hostname
	// (auto-added at load time by manager.go).
	reqHost := reqURL.Hostname()
	for _, domain := range pa.AllowedDomains {
		if strings.EqualFold(reqHost, domain) {
			return true
		}
	}
	return false
}

func responseHeaders(resp *http.Response) map[string]string {
	if len(resp.Header) == 0 {
		return nil
	}
	h := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		h[k] = resp.Header.Get(k)
	}
	return h
}

func errResponse(id, code, message string) protocol.Message {
	return protocol.Message{
		ID:     id,
		Type:   protocol.TypeHTTPResponse,
		Status: 0,
		Error:  &protocol.Error{Code: code, Message: message},
	}
}

// StripAuthOnCrossDomainRedirect is a CheckRedirect function that
// strips sensitive auth headers when a redirect crosses domain
// boundaries. This prevents credential leakage if a plugin's target
// server redirects to an attacker-controlled domain.
func StripAuthOnCrossDomainRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	if len(via) > 0 {
		origHost := via[0].URL.Hostname()
		newHost := req.URL.Hostname()
		if !strings.EqualFold(origHost, newHost) {
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
			req.Header.Del("Private-Token")
			req.Header.Del("X-Api-Key")
		}
	}
	return nil
}

// dangerousHeaders are headers that plugins must not control.
// Authorization is stripped here and re-added by injectAuth() when
// auth is configured. For Kerberos plugins, the SPNEGO round-tripper
// re-adds it. When no auth is configured, stripping prevents plugins
// from injecting arbitrary credentials.
//
// Note: Kerberos plugins have a per-plugin cookiejar that re-adds
// cookies from prior responses after stripping — this is intentional
// for SPNEGO auth flows.
var dangerousHeaders = []string{
	"Authorization",
	"Proxy-Authorization",
	"Private-Token",
	"X-Api-Key",
	"Host",
	"Cookie",
	"Set-Cookie",
	"Connection",
	"Upgrade",
	"Transfer-Encoding",
	"Te",
	"Trailer",
	"Forwarded",
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"X-Real-Ip",
	"X-Original-Url",
	"X-Rewrite-Url",
}

// stripDangerousHeaders removes security-sensitive headers that
// plugins should not set on proxied requests.
func stripDangerousHeaders(req *http.Request) {
	for _, h := range dangerousHeaders {
		req.Header.Del(h)
	}
}

// --- Retry helpers ---

const (
	maxBackoff    = 30 * time.Second
	maxRetryAfter = 30 * time.Second
)

func isIdempotent(method string) bool {
	switch method {
	case "GET", "HEAD", "OPTIONS", "PUT", "DELETE":
		return true
	}
	return false
}

func shouldRetry(status int, retryOn []int) bool {
	for _, s := range retryOn {
		if status == s {
			return true
		}
	}
	return false
}

func retryDelay(attempt int, retryAfter string) time.Duration {
	if retryAfter != "" {
		if secs, err := strconv.Atoi(retryAfter); err == nil && secs > 0 {
			d := time.Duration(secs) * time.Second
			if d > maxRetryAfter {
				d = maxRetryAfter
			}
			return d
		}
		if t, err := http.ParseTime(retryAfter); err == nil {
			d := time.Until(t)
			if d <= 0 {
				d = time.Second
			}
			if d > maxRetryAfter {
				d = maxRetryAfter
			}
			return d
		}
	}
	shift := attempt - 1
	if shift > 4 {
		shift = 4
	}
	base := time.Second << shift
	if base > maxBackoff {
		base = maxBackoff
	}
	jitter := time.Duration(rand.Int63n(int64(base)/2+1)) - base/4 //nolint:gosec // jitter does not need crypto-grade randomness
	return base + jitter
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
