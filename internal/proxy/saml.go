package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"

	"github.com/LeGambiArt/wtmcp/internal/domaincheck"
	"github.com/LeGambiArt/wtmcp/internal/protocol"
)

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
// meta-refresh tags and data-redirect-url attributes.
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
			content := getAttr(t, "content")
			if idx := strings.Index(strings.ToLower(content), "url="); idx >= 0 {
				redirect := strings.TrimSpace(content[idx+4:])
				if redirect != "" {
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

// parseSAMLForm extracts the action URL and hidden input fields from
// the first <form> in an HTML document. Relative action URLs are
// resolved against baseURL.
func parseSAMLForm(body []byte, baseURL string) (action string, formData url.Values, ok bool) {
	z := html.NewTokenizer(bytes.NewReader(body))
	formData = url.Values{}

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
		case "form":
			if a := getAttr(t, "action"); a != "" && action == "" {
				action = resolveRelativeURL(a, baseURL)
			}
		case "input":
			if strings.EqualFold(getAttr(t, "type"), "hidden") {
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
	if strings.HasPrefix(rawURL, "/") {
		return strings.TrimRight(baseURL, "/") + rawURL
	}
	return rawURL
}

// isDomainAllowedForSSO validates that a URL targets an allowed domain
// or the plugin's base URL host. Requires HTTPS.
func isDomainAllowedForSSO(rawURL string, baseURL string, allowedDomains []string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" {
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

	idpBody, err := io.ReadAll(io.LimitReader(idpResp.Body, 1<<20))
	if err != nil {
		return false
	}

	idpText := string(idpBody)
	if !strings.Contains(idpText, "saml-post-binding") && !strings.Contains(idpText, "SAMLResponse") {
		return false
	}

	action, formData, ok := parseSAMLForm(idpBody, baseURL)
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

	formResp, err := safeClient.Do(formReq)
	if err != nil {
		log.Printf("proxy: saml: SAML form POST failed: %v", err)
		return false
	}
	io.Copy(io.Discard, formResp.Body) //nolint:errcheck,gosec
	formResp.Body.Close()              //nolint:errcheck,gosec

	if formResp.StatusCode >= 200 && formResp.StatusCode < 400 {
		log.Printf("proxy: saml: SSO login completed")
		return true
	}
	log.Printf("proxy: saml: SAML form POST returned status %d", formResp.StatusCode)
	return false
}

// trySAMLSSO checks if an HTTP response is a SAML SSO auth redirect
// and, if so, follows the SSO flow. On success, it retries the
// original request (only for idempotent methods) and returns the new
// response. On failure (or if not an auth redirect), it returns the
// original response unchanged.
//
// The caller must not have read resp.Body yet.
func (p *Proxy) trySAMLSSO(ctx context.Context, pa *PluginAuth, resp *http.Response, client *http.Client, fullURL string, req protocol.Message, idempotent bool) *http.Response {
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

const samlInitBodyLimit = 1 << 20 // 1MB

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

	safeClient := safeRedirectClient(client, baseURL, allowedDomains)

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", fullURL, err)
	}

	resp, err := safeClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", fullURL, err)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, samlInitBodyLimit))
	resp.Body.Close() //nolint:errcheck,gosec
	if err != nil {
		return fmt.Errorf("read response from %s: %w", fullURL, err)
	}

	action, formData, ok := parseSAMLForm(body, baseURL)
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

	idpResp, err := safeClient.Do(idpReq)
	if err != nil {
		return fmt.Errorf("IdP request failed: %w", err)
	}
	idpBody, err := io.ReadAll(io.LimitReader(idpResp.Body, samlInitBodyLimit))
	idpResp.Body.Close() //nolint:errcheck,gosec
	if err != nil {
		return fmt.Errorf("read IdP response: %w", err)
	}

	respAction, respFormData, ok := parseSAMLForm(idpBody, baseURL)
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

	finalResp, err := safeClient.Do(finalReq)
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
