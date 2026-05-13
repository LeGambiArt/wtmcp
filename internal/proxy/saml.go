package proxy

import (
	"bytes"
	"context"
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
