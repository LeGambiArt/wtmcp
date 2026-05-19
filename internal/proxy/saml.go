package proxy

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/LeGambiArt/wtmcp/internal/protocol"
)

var (
	metaRefreshRe  = regexp.MustCompile(`(?i)content=['"][^'"]*url=([^'">\s]+)`)
	dataRedirectRe = regexp.MustCompile(`(?i)data-redirect-url=['"]([^'"]+)`)
	formActionRe   = regexp.MustCompile(`(?i)<form[^>]*action=['"]([^'"]+)`)
	hiddenInputRe  = regexp.MustCompile(
		`(?i)<input[^>]*type=['"]hidden['"][^>]*name=['"]([^'"]+)['"][^>]*value=['"]([^'"]*)['"]`,
	)
)

func isAuthRedirect(statusCode int, contentType string) bool {
	return (statusCode == 401 || statusCode == 403) && strings.Contains(contentType, "text/html")
}

func extractRedirectURL(body []byte, baseURL string) string {
	text := string(body)
	match := metaRefreshRe.FindStringSubmatch(text)
	if match == nil {
		match = dataRedirectRe.FindStringSubmatch(text)
	}
	if match == nil {
		return ""
	}
	redirect := html.UnescapeString(match[1])
	if strings.HasPrefix(redirect, "/") {
		return strings.TrimRight(baseURL, "/") + redirect
	}
	return redirect
}

func parseSAMLForm(body []byte) (action string, formData url.Values, ok bool) {
	text := string(body)
	actionMatch := formActionRe.FindStringSubmatch(text)
	if actionMatch == nil {
		return "", nil, false
	}
	action = html.UnescapeString(actionMatch[1])
	formData = url.Values{}
	for _, m := range hiddenInputRe.FindAllStringSubmatch(text, -1) {
		formData.Set(html.UnescapeString(m[1]), html.UnescapeString(m[2]))
	}
	if len(formData) == 0 {
		return "", nil, false
	}
	return action, formData, true
}

// isDomainAllowedForSSO validates that a URL targets an allowed domain
// or the plugin's base URL host. Requires HTTPS.
func isDomainAllowedForSSO(rawURL string, baseURL string, allowedDomains []string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	host := parsed.Hostname()

	if baseHost := extractHostname(baseURL); strings.EqualFold(host, baseHost) {
		return true
	}
	for _, d := range allowedDomains {
		if strings.EqualFold(host, d) {
			return true
		}
	}
	return false
}

func extractHostname(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// handleSAMLSSO follows a SAML SSO redirect flow using the given
// Kerberos-authenticated HTTP client. The flow is:
//  1. Extract redirect URL from Jenkins HTML (meta-refresh)
//  2. Validate the URL against allowed domains
//  3. Follow it to the IdP — SPNEGORoundTripper handles Kerberos
//  4. IdP returns a SAML POST binding form
//  5. Validate the form action URL against allowed domains
//  6. Submit the form back to Jenkins to establish a session cookie
//
// Returns true if login succeeded.
func handleSAMLSSO(client *http.Client, body []byte, baseURL string, allowedDomains []string) bool {
	redirectURL := extractRedirectURL(body, baseURL)
	if redirectURL == "" {
		return false
	}

	if !isDomainAllowedForSSO(redirectURL, baseURL, allowedDomains) {
		log.Printf("proxy: saml: redirect URL %q not in allowed domains", redirectURL)
		return false
	}

	idpReq, err := http.NewRequest("GET", redirectURL, nil)
	if err != nil {
		log.Printf("proxy: saml: invalid redirect URL %q: %v", redirectURL, err)
		return false
	}

	idpResp, err := client.Do(idpReq)
	if err != nil {
		log.Printf("proxy: saml: IdP request failed: %v", err)
		return false
	}
	defer func() {
		if err := idpResp.Body.Close(); err != nil {
			log.Printf("proxy: saml: failed to close IdP response body: %v", err)
		}
	}()

	idpBody, err := io.ReadAll(io.LimitReader(idpResp.Body, 1<<20))
	if err != nil {
		return false
	}

	idpText := string(idpBody)
	if !strings.Contains(idpText, "saml-post-binding") && !strings.Contains(idpText, "SAMLResponse") {
		return idpResp.StatusCode == 200
	}

	action, formData, ok := parseSAMLForm(idpBody)
	if !ok {
		log.Printf("proxy: saml: could not parse SAML form from IdP response")
		return false
	}

	if !isDomainAllowedForSSO(action, baseURL, allowedDomains) {
		log.Printf("proxy: saml: SAML form action URL %q not in allowed domains", action)
		return false
	}

	formReq, err := http.NewRequest("POST", action, strings.NewReader(formData.Encode()))
	if err != nil {
		return false
	}
	formReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	formResp, err := client.Do(formReq)
	if err != nil {
		log.Printf("proxy: saml: SAML form POST failed: %v", err)
		return false
	}
	io.Copy(io.Discard, formResp.Body) //nolint:errcheck,gosec
	formResp.Body.Close()              //nolint:errcheck,gosec

	if formResp.StatusCode == 200 {
		log.Printf("proxy: saml: SSO login completed")
		return true
	}
	log.Printf("proxy: saml: SAML form POST returned status %d", formResp.StatusCode)
	return false
}

// trySAMLSSO checks if an HTTP response is a SAML SSO auth redirect
// and, if so, follows the SSO flow. On success, it retries the
// original request and returns the new response. On failure (or if
// not an auth redirect), it returns the original response unchanged.
//
// The caller must not have read resp.Body yet.
func (p *Proxy) trySAMLSSO(ctx context.Context, pa *PluginAuth, resp *http.Response, client *http.Client, fullURL string, req protocol.Message) *http.Response {
	ct := resp.Header.Get("Content-Type")
	if !isAuthRedirect(resp.StatusCode, ct) {
		return resp
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, p.maxBodySize))
	resp.Body.Close() //nolint:errcheck,gosec
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp
	}

	// Use pa.BaseURL if configured, otherwise extract from fullURL
	baseURL := pa.BaseURL
	if baseURL == "" {
		if u, err := url.Parse(fullURL); err == nil {
			baseURL = u.Scheme + "://" + u.Host
		}
	}

	if !handleSAMLSSO(client, body, baseURL, pa.AllowedDomains) {
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

const samlInitBodyLimit = 1 << 20 // 1MB

// InitSAMLSession proactively authenticates by following a SAML SSO
// flow. It GETs initURL, detects whether the response contains a
// SAML form (form-first pattern) or a redirect (redirect-first
// pattern), and follows the chain to establish session cookies in
// the client's jar.
func InitSAMLSession(client *http.Client, initURL, baseURL string, allowedDomains []string) error {
	fullURL := initURL
	if strings.HasPrefix(initURL, "/") {
		fullURL = strings.TrimRight(baseURL, "/") + initURL
	}

	resp, err := client.Get(fullURL) //nolint:gosec // initURL is from plugin config, not user input
	if err != nil {
		return fmt.Errorf("GET %s: %w", fullURL, err)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, samlInitBodyLimit))
	resp.Body.Close() //nolint:errcheck,gosec
	if err != nil {
		return fmt.Errorf("read response from %s: %w", fullURL, err)
	}

	action, formData, ok := parseSAMLForm(body)
	if ok && formData.Get("SAMLRequest") != "" {
		return followSAMLFormChain(client, action, formData, baseURL, allowedDomains)
	}

	if handleSAMLSSO(client, body, baseURL, allowedDomains) {
		return nil
	}

	return fmt.Errorf("no SAML form or redirect found at %s (status %d)", fullURL, resp.StatusCode)
}

// followSAMLFormChain handles the form-first SAML pattern:
//  1. POST SAMLRequest to the IdP (form action URL)
//  2. Parse SAMLResponse from IdP response
//  3. POST SAMLResponse back to the origin server
func followSAMLFormChain(client *http.Client, idpURL string, formData url.Values, baseURL string, allowedDomains []string) error {
	if !isDomainAllowedForSSO(idpURL, baseURL, allowedDomains) {
		return fmt.Errorf("IdP URL %q not in allowed domains", idpURL)
	}

	idpReq, err := http.NewRequest("POST", idpURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return fmt.Errorf("build IdP request: %w", err)
	}
	idpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	idpResp, err := client.Do(idpReq)
	if err != nil {
		return fmt.Errorf("IdP request failed: %w", err)
	}
	idpBody, err := io.ReadAll(io.LimitReader(idpResp.Body, samlInitBodyLimit))
	idpResp.Body.Close() //nolint:errcheck,gosec
	if err != nil {
		return fmt.Errorf("read IdP response: %w", err)
	}

	respAction, respFormData, ok := parseSAMLForm(idpBody)
	if !ok || respFormData.Get("SAMLResponse") == "" {
		return fmt.Errorf("no SAMLResponse in IdP response (status %d)", idpResp.StatusCode)
	}

	if !isDomainAllowedForSSO(respAction, baseURL, allowedDomains) {
		return fmt.Errorf("SAMLResponse action %q not in allowed domains", respAction)
	}

	finalReq, err := http.NewRequest("POST", respAction, strings.NewReader(respFormData.Encode()))
	if err != nil {
		return fmt.Errorf("build final SAML request: %w", err)
	}
	finalReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	finalResp, err := client.Do(finalReq)
	if err != nil {
		return fmt.Errorf("final SAML POST failed: %w", err)
	}
	io.Copy(io.Discard, finalResp.Body) //nolint:errcheck,gosec
	finalResp.Body.Close()              //nolint:errcheck,gosec

	if finalResp.StatusCode >= 200 && finalResp.StatusCode < 400 {
		log.Printf("proxy: saml: proactive SSO login completed")
		return nil
	}
	return fmt.Errorf("final SAML POST returned status %d", finalResp.StatusCode)
}
