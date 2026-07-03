package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/LeGambiArt/wtmcp/internal/domaincheck"
	"github.com/LeGambiArt/wtmcp/internal/protocol"
)

const (
	samlUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"
	samlAccept    = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	samlMaxLegs   = 5
)

func setSAMLHeaders(req *http.Request, origin string) {
	req.Header.Set("User-Agent", samlUserAgent)
	req.Header.Set("Accept", samlAccept)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
}

func isAuthRedirect(statusCode int, contentType string) bool {
	return (statusCode == 401 || statusCode == 403) && strings.Contains(contentType, "text/html")
}

// getAttr returns the value of the named attribute on a token, or "".
func getAttr(t html.Token, name string) string {
	for _, a := range t.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

// extractRedirectURL finds a redirect URL in an HTML document, checking
// meta-refresh tags, data-redirect-url attributes, and JavaScript
// redirect assignments (var redirectUrl = '...' or window.location).
func extractRedirectURL(body []byte, baseURL string) string {
	z := html.NewTokenizer(bytes.NewReader(body))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		t := z.Token()

		switch t.Data {
		case "meta":
			if !strings.EqualFold(getAttr(t, "http-equiv"), "refresh") {
				continue
			}
			content := getAttr(t, "content")
			if idx := strings.Index(strings.ToLower(content), "url="); idx >= 0 {
				redirect := strings.TrimSpace(content[idx+len("url="):])
				if redirect != "" {
					return resolveRelativeURL(redirect, baseURL)
				}
			}
		case "script":
			if next := z.Next(); next == html.TextToken {
				// Copy z.Text() — the slice is invalidated by the
				// next z.Next() call in the outer loop.
				scriptContent := append([]byte(nil), z.Text()...)
				if redirect := extractJSRedirect(scriptContent); redirect != "" {
					return resolveRelativeURL(redirect, baseURL)
				}
			}
		default:
			if redirect := getAttr(t, "data-redirect-url"); redirect != "" {
				return resolveRelativeURL(redirect, baseURL)
			}
		}
	}
	return ""
}

var jsRedirectRe = regexp.MustCompile(
	`(?:var\s+redirectUrl|window\.location(?:\.href)?)\s*=\s*['"]([^'"]+)['"]`,
)

func extractJSRedirect(script []byte) string {
	m := jsRedirectRe.FindSubmatch(script)
	if m != nil {
		return string(m[1])
	}
	return ""
}

// parseSAMLForm extracts the action URL and hidden input fields from
// the first <form> in an HTML document. Relative action URLs are
// resolved against baseURL.
func parseSAMLForm(body []byte, baseURL string) (action string, formData url.Values, ok bool) {
	z := html.NewTokenizer(bytes.NewReader(body))
	formData = url.Values{}
	var inForm, seenForm bool

	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}

		if tt == html.EndTagToken {
			tn, _ := z.TagName()
			if string(tn) == "form" && inForm {
				inForm = false
			}
			continue
		}

		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		t := z.Token()

		switch t.Data {
		case "form":
			if seenForm {
				break
			}
			seenForm = true
			if a := getAttr(t, "action"); a != "" {
				action = resolveRelativeURL(a, baseURL)
				inForm = true
			}
		case "input":
			if inForm && strings.EqualFold(getAttr(t, "type"), "hidden") {
				name := getAttr(t, "name")
				if name != "" {
					formData.Set(name, getAttr(t, "value"))
				}
			}
		}
	}

	if action == "" || len(formData) == 0 {
		return "", nil, false
	}
	return action, formData, true
}

func resolveRelativeURL(rawURL, baseURL string) string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return rawURL
	}
	ref, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return base.ResolveReference(ref).String()
}

// isDomainAllowedForSSO validates that a URL targets an allowed domain
// or the plugin's base URL host. Requires HTTPS.
func isDomainAllowedForSSO(rawURL string, baseURL string, allowedDomains []string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil {
		return false
	}
	host := parsed.Hostname()

	if baseHost := extractHostname(baseURL); baseHost != "" {
		if domaincheck.Contains([]string{baseHost}, host) {
			return true
		}
	}
	return domaincheck.Contains(allowedDomains, host)
}

func samlCooldownKey(pluginName, fullURL string) string {
	u, err := url.Parse(fullURL)
	if err != nil {
		return pluginName + "|" + fullURL
	}
	return pluginName + "|" + u.Host + u.Path
}

func extractHostname(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// safeRedirectClient returns a copy of client that only follows
// redirects to allowed domains. Redirects to unknown domains are
// stopped, preventing SPNEGO token leakage while allowing
// legitimate intra-IdP redirect chains.
func safeRedirectClient(client *http.Client, baseURL string, allowedDomains []string) *http.Client {
	c := *client
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		if !isDomainAllowedForSSO(req.URL.String(), baseURL, allowedDomains) {
			return http.ErrUseLastResponse
		}
		if len(via) > 0 {
			origHost := via[0].URL.Hostname()
			newHost := req.URL.Hostname()
			origNorm, err1 := domaincheck.Normalize(origHost)
			newNorm, err2 := domaincheck.Normalize(newHost)
			if err1 != nil || err2 != nil || origNorm != newNorm {
				req.Header.Del("Authorization")
				req.Header.Del("Cookie")
				req.Header.Del("Private-Token")
				req.Header.Del("X-Api-Key")
			}
		}
		return nil
	}
	return &c
}

// handleSAMLSSO follows a SAML SSO redirect flow using the given
// Kerberos-authenticated HTTP client. The flow is:
//  1. Extract redirect URL from the HTML response (meta-refresh or
//     data-redirect-url attribute)
//  2. Validate the URL against allowed domains
//  3. Follow it to the IdP — SPNEGORoundTripper handles Kerberos
//  4. IdP returns a SAML POST binding form
//  5. Validate the form action URL against allowed domains
//  6. Submit the form back to the origin to establish a session cookie
//
// Returns true if login succeeded.
func handleSAMLSSO(ctx context.Context, client *http.Client, body []byte, baseURL string, allowedDomains []string) bool {
	redirectURL := extractRedirectURL(body, baseURL)
	if redirectURL == "" {
		return false
	}

	if !isDomainAllowedForSSO(redirectURL, baseURL, allowedDomains) {
		log.Printf("proxy: saml: redirect URL %q not in allowed domains", redirectURL)
		return false
	}

	safeClient := safeRedirectClient(client, baseURL, allowedDomains)

	idpReq, err := http.NewRequestWithContext(ctx, "GET", redirectURL, nil)
	if err != nil {
		log.Printf("proxy: saml: invalid redirect URL %q: %v", redirectURL, err)
		return false
	}
	setSAMLHeaders(idpReq, "")

	idpResp, err := safeClient.Do(idpReq)
	if err != nil {
		log.Printf("proxy: saml: IdP request failed: %v", err)
		return false
	}
	defer func() {
		if err := idpResp.Body.Close(); err != nil {
			log.Printf("proxy: saml: failed to close IdP response body: %v", err)
		}
	}()

	idpBody, err := io.ReadAll(io.LimitReader(idpResp.Body, samlInitBodyLimit))
	if err != nil {
		return false
	}

	idpText := string(idpBody)
	if !strings.Contains(idpText, "saml-post-binding") && !strings.Contains(idpText, "SAMLResponse") {
		return false
	}

	idpBase := idpResp.Request.URL.String()
	action, formData, ok := parseSAMLForm(idpBody, idpBase)
	if !ok {
		log.Printf("proxy: saml: could not parse SAML form from IdP response")
		return false
	}

	if !isDomainAllowedForSSO(action, baseURL, allowedDomains) {
		log.Printf("proxy: saml: SAML form action URL %q not in allowed domains", action)
		return false
	}

	formReq, err := http.NewRequestWithContext(ctx, "POST", action, strings.NewReader(formData.Encode()))
	if err != nil {
		return false
	}
	formReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSAMLHeaders(formReq, idpBase)

	formResp, err := safeClient.Do(formReq)
	if err != nil {
		log.Printf("proxy: saml: SAML form POST failed: %v", err)
		return false
	}
	acsBody, err := io.ReadAll(io.LimitReader(formResp.Body, samlInitBodyLimit))
	formResp.Body.Close() //nolint:errcheck,gosec
	if err != nil {
		log.Printf("proxy: saml: error reading ACS response body: %v", err)
	}

	if formResp.StatusCode < 200 || formResp.StatusCode >= 400 {
		log.Printf("proxy: saml: SAML form POST returned status %d", formResp.StatusCode)
		return false
	}

	if legs := followSAMLChain(ctx, safeClient, acsBody, formResp.Request.URL.String(), baseURL, allowedDomains); legs > 0 {
		log.Printf("proxy: saml: SSO login completed (%d post-ACS legs followed)", legs)
	} else {
		log.Printf("proxy: saml: SSO login completed")
	}
	return true
}

// followSAMLChain follows a chain of auto-submit forms or JS
// redirects in an ACS response. Some SPs (notably Workday) return
// an HTML page after processing the SAMLResponse that contains a
// form targeting the auth gateway, which must be submitted to
// complete session establishment.
func followSAMLChain(ctx context.Context, client *http.Client, body []byte, currentBase, baseURL string, allowedDomains []string) int {
	completed := 0
	for leg := range samlMaxLegs {
		if len(body) == 0 {
			break
		}

		if nextAction, nextData, ok := parseSAMLForm(body, currentBase); ok && len(nextData) > 0 {
			if !isDomainAllowedForSSO(nextAction, baseURL, allowedDomains) {
				log.Printf("proxy: saml: post-ACS form action %q not in allowed domains (leg %d)", nextAction, leg+1)
				break
			}
			log.Printf("proxy: saml: ACS response contains form, following (leg %d)", leg+1)
			followReq, err := http.NewRequestWithContext(ctx, "POST", nextAction, strings.NewReader(nextData.Encode()))
			if err != nil {
				log.Printf("proxy: saml: post-ACS chain leg %d: build request failed: %v", leg+1, err)
				break
			}
			followReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			setSAMLHeaders(followReq, "")
			followResp, err := client.Do(followReq)
			if err != nil {
				log.Printf("proxy: saml: post-ACS chain leg %d: request failed: %v", leg+1, err)
				break
			}
			body, _ = io.ReadAll(io.LimitReader(followResp.Body, samlInitBodyLimit))
			followResp.Body.Close() //nolint:errcheck,gosec
			currentBase = followResp.Request.URL.String()
			completed++
			continue
		}

		if jsRedirect := extractRedirectURL(body, currentBase); jsRedirect != "" {
			if !isDomainAllowedForSSO(jsRedirect, baseURL, allowedDomains) {
				log.Printf("proxy: saml: post-ACS redirect %q not in allowed domains (leg %d)", jsRedirect, leg+1)
				break
			}
			log.Printf("proxy: saml: ACS response contains redirect, following (leg %d)", leg+1)
			followReq, err := http.NewRequestWithContext(ctx, "GET", jsRedirect, nil)
			if err != nil {
				log.Printf("proxy: saml: post-ACS chain leg %d: build request failed: %v", leg+1, err)
				break
			}
			setSAMLHeaders(followReq, "")
			followResp, err := client.Do(followReq)
			if err != nil {
				log.Printf("proxy: saml: post-ACS chain leg %d: request failed: %v", leg+1, err)
				break
			}
			body, _ = io.ReadAll(io.LimitReader(followResp.Body, samlInitBodyLimit))
			followResp.Body.Close() //nolint:errcheck,gosec
			currentBase = followResp.Request.URL.String()
			completed++
			continue
		}

		break
	}
	return completed
}

const samlInitBodyLimit = 1 << 20 // 1MB
const samlPeekSize = 8 * 1024     // 8KB
const samlCooldownDuration = 60 * time.Second

// peekReadCloser wraps a MultiReader (peek + original body) while
// preserving the original body's Close(). Without this, wrapping in
// io.NopCloser would leak the TCP connection.
type peekReadCloser struct {
	io.Reader
	orig io.Closer
}

func (p *peekReadCloser) Close() error { return p.orig.Close() }

// isSAMLChallenge checks if an 8KB peek from a 200 text/html
// response looks like a SAML challenge. Fast substring pre-filter;
// structural confirmation via parseSAMLForm or handleSAMLSSO happens
// after the full body is read.
func isSAMLChallenge(peek []byte) bool {
	lower := bytes.ToLower(peek)
	if bytes.Contains(lower, []byte("<form")) &&
		(bytes.Contains(peek, []byte("SAMLRequest")) ||
			bytes.Contains(peek, []byte("SAMLResponse"))) {
		return true
	}
	return bytes.Contains(lower, []byte("<script")) && jsRedirectRe.Match(peek)
}

// trySAMLSSO checks if an HTTP response is a SAML SSO auth challenge
// and, if so, follows the SSO flow. On success, it retries the
// original request (only for idempotent methods) and returns the new
// response. On failure (or if not an auth challenge), it returns the
// original response unchanged.
//
// Handles two trigger patterns:
//   - Classic: 401/403 + text/html (via handleReactiveSAML)
//   - Session expiry: 200 + text/html with SAMLRequest form in body
//     (some SPs return 200 with a SAML form instead of 401/403)
//
// The caller must not have read resp.Body yet.
func (p *Proxy) trySAMLSSO(ctx context.Context, pluginName string, pa *PluginAuth, resp *http.Response, client *http.Client, fullURL string, req protocol.Message, idempotent bool) *http.Response {
	ct := resp.Header.Get("Content-Type")

	if isAuthRedirect(resp.StatusCode, ct) {
		return p.handleReactiveSAML(ctx, pa, resp, client, fullURL, req, idempotent)
	}

	if resp.StatusCode != 200 || !strings.Contains(ct, "text/html") {
		return resp
	}

	cooldownKey := samlCooldownKey(pluginName, fullURL)
	if v, ok := p.samlCooldowns.Load(cooldownKey); ok {
		if ts, ok := v.(int64); ok && time.Since(time.Unix(0, ts)) < samlCooldownDuration {
			return resp
		}
		p.samlCooldowns.Delete(cooldownKey)
	}

	peek := make([]byte, samlPeekSize)
	n, err := io.ReadFull(resp.Body, peek)
	peek = peek[:n]
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		resp.Body = &peekReadCloser{
			Reader: io.MultiReader(bytes.NewReader(peek), resp.Body),
			orig:   resp.Body,
		}
		return resp
	}

	if !isSAMLChallenge(peek) {
		resp.Body = &peekReadCloser{
			Reader: io.MultiReader(bytes.NewReader(peek), resp.Body),
			orig:   resp.Body,
		}
		return resp
	}

	rest, err := io.ReadAll(io.LimitReader(resp.Body, samlInitBodyLimit-int64(n)))
	resp.Body.Close() //nolint:errcheck,gosec
	if err != nil {
		log.Printf("proxy: saml: error reading SAML body: %v", err)
		body := append(peek, rest...) //nolint:gocritic
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp
	}
	body := make([]byte, 0, n+len(rest))
	body = append(body, peek...)
	body = append(body, rest...)

	log.Printf("proxy: saml: detected SAML challenge in 200 response from %s", fullURL) //nolint:gosec

	baseURL := pa.BaseURL
	if baseURL == "" {
		if u, err := url.Parse(fullURL); err == nil {
			baseURL = u.Scheme + "://" + u.Host
		}
	}

	ssoOK := false
	respBase := resp.Request.URL.String()
	action, formData, ok := parseSAMLForm(body, respBase)
	if ok && formData.Get("SAMLRequest") != "" {
		err := followSAMLFormChain(ctx, client, action, formData, baseURL, pa.AllowedDomains)
		ssoOK = (err == nil)
	}
	if !ssoOK {
		ssoOK = handleSAMLSSO(ctx, client, body, baseURL, pa.AllowedDomains)
	}

	if ssoOK {
		p.samlCooldowns.Store(cooldownKey, time.Now().UnixNano())
	}

	if !ssoOK {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp
	}

	if !idempotent {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp
	}

	retryReq, err := p.buildRequest(ctx, fullURL, req)
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp
	}
	retryResp, err := client.Do(retryReq)
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp
	}
	return retryResp
}

// handleReactiveSAML handles the classic 401/403 auth redirect SAML
// flow. Extracted from trySAMLSSO to keep the 200-detection path
// separate.
func (p *Proxy) handleReactiveSAML(ctx context.Context, pa *PluginAuth, resp *http.Response, client *http.Client, fullURL string, req protocol.Message, idempotent bool) *http.Response {
	body, err := io.ReadAll(io.LimitReader(resp.Body, samlInitBodyLimit))
	resp.Body.Close() //nolint:errcheck,gosec
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp
	}

	baseURL := pa.BaseURL
	if baseURL == "" {
		if u, err := url.Parse(fullURL); err == nil {
			baseURL = u.Scheme + "://" + u.Host
		}
	}

	if !handleSAMLSSO(ctx, client, body, baseURL, pa.AllowedDomains) {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp
	}

	if !idempotent {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp
	}

	retryReq, err := p.buildRequest(ctx, fullURL, req)
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp
	}
	retryResp, err := client.Do(retryReq)
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp
	}
	return retryResp
}

// InitSAMLSession proactively authenticates by following a SAML SSO
// flow. It GETs initURL, detects whether the response contains a
// SAML form (form-first pattern) or a redirect (redirect-first
// pattern), and follows the chain to establish session cookies in
// the client's jar.
func InitSAMLSession(ctx context.Context, client *http.Client, initURL, baseURL string, allowedDomains []string) error {
	fullURL := initURL
	if strings.HasPrefix(initURL, "/") {
		fullURL = strings.TrimRight(baseURL, "/") + initURL
	}

	if !isDomainAllowedForSSO(fullURL, baseURL, allowedDomains) {
		return fmt.Errorf("saml_init URL %q not in allowed domains", fullURL)
	}

	safeClient := safeRedirectClient(client, baseURL, allowedDomains)

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", fullURL, err)
	}
	setSAMLHeaders(req, "")

	resp, err := safeClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", fullURL, err)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, samlInitBodyLimit))
	resp.Body.Close() //nolint:errcheck,gosec
	if err != nil {
		return fmt.Errorf("read response from %s: %w", fullURL, err)
	}

	respBase := resp.Request.URL.String()
	action, formData, ok := parseSAMLForm(body, respBase)
	if ok && formData.Get("SAMLRequest") != "" {
		return followSAMLFormChain(ctx, client, action, formData, baseURL, allowedDomains)
	}

	if handleSAMLSSO(ctx, client, body, baseURL, allowedDomains) {
		return nil
	}

	return fmt.Errorf("no SAML form or redirect found at %s (status %d)", fullURL, resp.StatusCode)
}

// followSAMLFormChain handles the form-first SAML pattern:
//  1. POST SAMLRequest to the IdP (form action URL)
//  2. Parse SAMLResponse from IdP response
//  3. POST SAMLResponse back to the origin server
func followSAMLFormChain(ctx context.Context, client *http.Client, idpURL string, formData url.Values, baseURL string, allowedDomains []string) error {
	if !isDomainAllowedForSSO(idpURL, baseURL, allowedDomains) {
		return fmt.Errorf("IdP URL %q not in allowed domains", idpURL)
	}

	safeClient := safeRedirectClient(client, baseURL, allowedDomains)

	idpReq, err := http.NewRequestWithContext(ctx, "POST", idpURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return fmt.Errorf("build IdP request: %w", err)
	}
	idpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSAMLHeaders(idpReq, "")

	idpResp, err := safeClient.Do(idpReq)
	if err != nil {
		return fmt.Errorf("IdP request failed: %w", err)
	}
	idpBody, err := io.ReadAll(io.LimitReader(idpResp.Body, samlInitBodyLimit))
	idpResp.Body.Close() //nolint:errcheck,gosec
	if err != nil {
		return fmt.Errorf("read IdP response: %w", err)
	}

	idpBase := idpResp.Request.URL.String()
	respAction, respFormData, ok := parseSAMLForm(idpBody, idpBase)
	if !ok || respFormData.Get("SAMLResponse") == "" {
		return fmt.Errorf("no SAMLResponse in IdP response (status %d)", idpResp.StatusCode)
	}

	if !isDomainAllowedForSSO(respAction, baseURL, allowedDomains) {
		return fmt.Errorf("SAMLResponse action %q not in allowed domains", respAction)
	}

	finalReq, err := http.NewRequestWithContext(ctx, "POST", respAction, strings.NewReader(respFormData.Encode()))
	if err != nil {
		return fmt.Errorf("build final SAML request: %w", err)
	}
	finalReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSAMLHeaders(finalReq, idpBase)

	finalResp, err := safeClient.Do(finalReq)
	if err != nil {
		return fmt.Errorf("final SAML POST failed: %w", err)
	}
	finalBody, _ := io.ReadAll(io.LimitReader(finalResp.Body, samlInitBodyLimit))
	finalResp.Body.Close() //nolint:errcheck,gosec

	if finalResp.StatusCode < 200 || finalResp.StatusCode >= 400 {
		return fmt.Errorf("final SAML POST returned status %d", finalResp.StatusCode)
	}

	if legs := followSAMLChain(ctx, safeClient, finalBody, finalResp.Request.URL.String(), baseURL, allowedDomains); legs > 0 {
		log.Printf("proxy: saml: proactive SSO login completed (%d post-ACS legs followed)", legs)
	} else {
		log.Printf("proxy: saml: proactive SSO login completed")
	}
	return nil
}
