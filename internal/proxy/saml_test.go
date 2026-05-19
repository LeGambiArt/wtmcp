package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeGambiArt/wtmcp/internal/protocol"
)

func TestIsAuthRedirect(t *testing.T) {
	tests := []struct {
		status      int
		contentType string
		want        bool
	}{
		{401, "text/html; charset=utf-8", true},
		{403, "text/html", true},
		{401, "application/json", false},
		{200, "text/html", false},
		{302, "text/html", false},
		{403, "", false},
	}
	for _, tt := range tests {
		got := isAuthRedirect(tt.status, tt.contentType)
		if got != tt.want {
			t.Errorf("isAuthRedirect(%d, %q) = %v, want %v", tt.status, tt.contentType, got, tt.want)
		}
	}
}

func TestExtractRedirectURL(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		baseURL string
		want    string
	}{
		{
			name:    "meta refresh absolute",
			body:    `<html><meta content="0;url=https://idp.example.com/auth/realms/test" /></html>`,
			baseURL: "https://jenkins.example.com",
			want:    "https://idp.example.com/auth/realms/test",
		},
		{
			name:    "meta refresh relative",
			body:    `<html><meta content="0;url=/securityRealm/commenceLogin" /></html>`,
			baseURL: "https://jenkins.example.com",
			want:    "https://jenkins.example.com/securityRealm/commenceLogin",
		},
		{
			name:    "data-redirect-url attribute",
			body:    `<html><div data-redirect-url="https://idp.example.com/login"></div></html>`,
			baseURL: "https://jenkins.example.com",
			want:    "https://idp.example.com/login",
		},
		{
			name:    "no redirect URL",
			body:    `<html><body>Access denied</body></html>`,
			baseURL: "https://jenkins.example.com",
			want:    "",
		},
		{
			name:    "base URL trailing slash",
			body:    `<html><meta content="0;url=/login" /></html>`,
			baseURL: "https://jenkins.example.com/",
			want:    "https://jenkins.example.com/login",
		},
		{
			name:    "html entity in URL",
			body:    `<html><meta content="0;url=https://idp.example.com/auth?client_id=x&amp;state=y" /></html>`,
			baseURL: "https://jenkins.example.com",
			want:    "https://idp.example.com/auth?client_id=x&state=y",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRedirectURL([]byte(tt.body), tt.baseURL)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseSAMLForm(t *testing.T) {
	t.Run("valid SAML form", func(t *testing.T) {
		body := `<html>
		<form action="https://jenkins.example.com/securityRealm/finishLogin">
			<input type="hidden" name="SAMLResponse" value="PHN...base64..." />
			<input type="hidden" name="RelayState" value="token123" />
		</form></html>`

		action, formData, ok := parseSAMLForm([]byte(body), "")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if action != "https://jenkins.example.com/securityRealm/finishLogin" {
			t.Errorf("action = %q", action)
		}
		if formData.Get("SAMLResponse") != "PHN...base64..." {
			t.Errorf("SAMLResponse = %q", formData.Get("SAMLResponse"))
		}
		if formData.Get("RelayState") != "token123" {
			t.Errorf("RelayState = %q", formData.Get("RelayState"))
		}
	})

	t.Run("html entity in action", func(t *testing.T) {
		body := `<html><form action="https://jenkins.example.com/finish?a=1&amp;b=2">
			<input type="hidden" name="SAMLResponse" value="data" />
		</form></html>`
		action, _, ok := parseSAMLForm([]byte(body), "")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if action != "https://jenkins.example.com/finish?a=1&b=2" {
			t.Errorf("action = %q, want decoded HTML entities", action)
		}
	})

	t.Run("no form", func(t *testing.T) {
		body := `<html><body>No form here</body></html>`
		_, _, ok := parseSAMLForm([]byte(body), "")
		if ok {
			t.Error("expected ok=false for body without form")
		}
	})

	t.Run("form without hidden inputs", func(t *testing.T) {
		body := `<html><form action="https://example.com/login">
			<input type="text" name="username" />
		</form></html>`
		_, _, ok := parseSAMLForm([]byte(body), "")
		if ok {
			t.Error("expected ok=false for form without hidden inputs")
		}
	})
}

func TestIsDomainAllowedForSSO(t *testing.T) {
	tests := []struct {
		name           string
		rawURL         string
		baseURL        string
		allowedDomains []string
		want           bool
	}{
		{
			name:           "same as base URL",
			rawURL:         "https://jenkins.example.com/finishLogin",
			baseURL:        "https://jenkins.example.com",
			allowedDomains: nil,
			want:           true,
		},
		{
			name:           "in allowed domains",
			rawURL:         "https://idp.example.com/auth",
			baseURL:        "https://jenkins.example.com",
			allowedDomains: []string{"jenkins.example.com", "idp.example.com"},
			want:           true,
		},
		{
			name:           "not in allowed domains",
			rawURL:         "https://evil.com/steal",
			baseURL:        "https://jenkins.example.com",
			allowedDomains: []string{"jenkins.example.com"},
			want:           false,
		},
		{
			name:           "HTTP scheme rejected",
			rawURL:         "http://jenkins.example.com/finishLogin",
			baseURL:        "https://jenkins.example.com",
			allowedDomains: []string{"jenkins.example.com"},
			want:           false,
		},
		{
			name:           "case insensitive",
			rawURL:         "https://JENKINS.EXAMPLE.COM/path",
			baseURL:        "https://jenkins.example.com",
			allowedDomains: nil,
			want:           true,
		},
		{
			name:           "FTP scheme rejected",
			rawURL:         "ftp://jenkins.example.com/file",
			baseURL:        "https://jenkins.example.com",
			allowedDomains: nil,
			want:           false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isDomainAllowedForSSO(tt.rawURL, tt.baseURL, tt.allowedDomains)
			if got != tt.want {
				t.Errorf("isDomainAllowedForSSO(%q) = %v, want %v", tt.rawURL, got, tt.want)
			}
		})
	}
}

func TestHandleSAMLSSO(t *testing.T) {
	var (
		idpHit      atomic.Int32
		samlPostHit atomic.Int32
	)

	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	srvHost, _ := url.Parse(srv.URL)

	mux.HandleFunc("/securityRealm/commenceLogin", func(w http.ResponseWriter, _ *http.Request) {
		idpHit.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><body class="saml-post-binding">
			<form action="%s/securityRealm/finishLogin">
				<input type="hidden" name="SAMLResponse" value="base64data" />
				<input type="hidden" name="RelayState" value="relay" />
			</form></body></html>`, srv.URL)
	})
	mux.HandleFunc("/securityRealm/finishLogin", func(w http.ResponseWriter, r *http.Request) {
		samlPostHit.Add(1)
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB limit
		if r.FormValue("SAMLResponse") == "" {
			w.WriteHeader(400)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "JSESSIONID", Value: "test-session", Path: "/"})
		w.WriteHeader(200)
	})

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	authRedirectBody := fmt.Sprintf(
		`<html><meta content="0;url=%s/securityRealm/commenceLogin" /></html>`,
		srv.URL,
	)

	ok := handleSAMLSSO(context.Background(), client, []byte(authRedirectBody), srv.URL, []string{srvHost.Hostname()})
	if !ok {
		t.Fatal("expected handleSAMLSSO to succeed")
	}
	if idpHit.Load() != 1 {
		t.Errorf("IdP hit count = %d, want 1", idpHit.Load())
	}
	if samlPostHit.Load() != 1 {
		t.Errorf("SAML POST hit count = %d, want 1", samlPostHit.Load())
	}

	u, _ := url.Parse(srv.URL)
	cookies := jar.Cookies(u)
	found := false
	for _, c := range cookies {
		if c.Name == "JSESSIONID" {
			found = true
		}
	}
	if !found {
		t.Error("expected JSESSIONID cookie after SSO login")
	}
}

func TestHandleSAMLSSONoRedirect(t *testing.T) {
	body := `<html><body>Access denied, no redirect</body></html>`
	ok := handleSAMLSSO(context.Background(), &http.Client{}, []byte(body), "https://example.com", nil)
	if ok {
		t.Error("expected failure when no redirect URL found")
	}
}

func TestHandleSAMLSSOBlocksDisallowedDomain(t *testing.T) {
	body := `<html><meta content="0;url=https://evil.com/steal" /></html>`
	ok := handleSAMLSSO(context.Background(), &http.Client{}, []byte(body), "https://jenkins.example.com", []string{"jenkins.example.com"})
	if ok {
		t.Error("expected failure for redirect to disallowed domain")
	}
}

func TestTrySAMLSSOIntegration(t *testing.T) {
	var requestCount atomic.Int32

	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	mux.HandleFunc("/api/json", func(w http.ResponseWriter, _ *http.Request) {
		n := requestCount.Add(1)
		if n == 1 {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(403)
			_, _ = fmt.Fprintf(w, `<html><meta content="0;url=%s/idp/login" /></html>`, srv.URL)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"mode":"normal"}`)
	})
	mux.HandleFunc("/idp/login", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><body class="saml-post-binding">
			<form action="%s/finishLogin">
				<input type="hidden" name="SAMLResponse" value="data" />
				<input type="hidden" name="RelayState" value="r" />
			</form></body></html>`, srv.URL)
	})
	mux.HandleFunc("/finishLogin", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(200)
		}
	})

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	p := newTestProxy(client)
	pa := testPluginAuth(srv.URL)
	pa.Client = client
	pa.IsKerberos = true
	p.RegisterPlugin("jenkins", pa)

	resp := p.Execute(context.Background(), "jenkins", protocol.Message{
		ID:     "req-saml",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		Path:   "/api/json",
	})

	if resp.Status != 200 {
		t.Errorf("status = %d, want 200 (error = %v)", resp.Status, resp.Error)
	}
	if !strings.Contains(string(resp.Body), "normal") {
		t.Errorf("body = %s, expected JSON with mode:normal", resp.Body)
	}
	if requestCount.Load() != 2 {
		t.Errorf("request count = %d, want 2 (initial + retry)", requestCount.Load())
	}
}

func TestTrySAMLSSONonKerberos(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(403)
		_, _ = fmt.Fprint(w, `<html><meta content="0;url=/login" /></html>`)
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	pa := testPluginAuth(srv.URL)
	pa.IsKerberos = false
	p.RegisterPlugin("test", pa)

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-no-saml",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		Path:   "/api/json",
	})

	if resp.Status != 403 {
		t.Errorf("status = %d, want 403 (non-Kerberos should not trigger SSO)", resp.Status)
	}
}

// --- InitSAMLSession tests ---

func TestInitSAMLSessionFormFirst(t *testing.T) {
	var step atomic.Int32
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	// Step 1: SAML init URL returns form with SAMLRequest
	mux.HandleFunc("/saml.redirect", func(w http.ResponseWriter, _ *http.Request) {
		step.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><body><form action="%s/idp/saml">
			<input type="hidden" name="SAMLRequest" value="base64encodedrequest"/>
			<input type="hidden" name="RelayState" value="/"/>
		</form></body></html>`, srv.URL)
	})

	// Step 2: IdP processes SAMLRequest, returns SAMLResponse
	mux.HandleFunc("/idp/saml", func(w http.ResponseWriter, r *http.Request) {
		step.Add(1)
		if r.Method != "POST" {
			t.Errorf("IdP expected POST, got %s", r.Method)
		}
		_ = r.ParseForm() //nolint:gosec // test handler, no real request body
		if r.Form.Get("SAMLRequest") == "" {
			t.Error("IdP request missing SAMLRequest")
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><body><form action="%s/saml.digest">
			<input type="hidden" name="SAMLResponse" value="base64encodedresponse"/>
			<input type="hidden" name="RelayState" value="/"/>
		</form></body></html>`, srv.URL)
	})

	// Step 3: Origin processes SAMLResponse, sets session
	mux.HandleFunc("/saml.digest", func(w http.ResponseWriter, r *http.Request) {
		step.Add(1)
		if r.Method != "POST" {
			t.Errorf("digest expected POST, got %s", r.Method)
		}
		_ = r.ParseForm() //nolint:gosec // test handler, no real request body
		if r.Form.Get("SAMLResponse") == "" {
			t.Error("digest request missing SAMLResponse")
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "authenticated"})
		w.WriteHeader(200)
	})

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	err := InitSAMLSession(context.Background(), client, "/saml.redirect", srv.URL, []string{})
	if err != nil {
		t.Fatalf("InitSAMLSession failed: %v", err)
	}
	if step.Load() != 3 {
		t.Errorf("expected 3 steps, got %d", step.Load())
	}

	u, _ := url.Parse(srv.URL)
	cookies := jar.Cookies(u)
	found := false
	for _, c := range cookies {
		if c.Name == "session" && c.Value == "authenticated" {
			found = true
		}
	}
	if !found {
		t.Error("session cookie not set after SAML init")
	}
}

func TestInitSAMLSessionRedirectFirst(t *testing.T) {
	var ssoHandled atomic.Bool
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	// Init URL returns a page with a meta-refresh redirect
	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><meta content="0;url=%s/idp/sso" /></html>`, srv.URL)
	})

	// IdP returns SAML form
	mux.HandleFunc("/idp/sso", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><body>
			<div id="saml-post-binding">
			<form action="%s/saml/consume">
				<input type="hidden" name="SAMLResponse" value="response123"/>
			</form></div></body></html>`, srv.URL)
	})

	// Origin consumes SAMLResponse
	mux.HandleFunc("/saml/consume", func(w http.ResponseWriter, _ *http.Request) {
		ssoHandled.Store(true)
		w.WriteHeader(200)
	})

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	err := InitSAMLSession(context.Background(), client, "/login", srv.URL, []string{})
	if err != nil {
		t.Fatalf("InitSAMLSession redirect-first failed: %v", err)
	}
	if !ssoHandled.Load() {
		t.Error("SSO handler was not called")
	}
}

func TestInitSAMLSessionNoSAMLContent(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><body>Just a normal page</body></html>`)
	}))
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	err := InitSAMLSession(context.Background(), client, "/", srv.URL, []string{})
	if err == nil {
		t.Fatal("expected error for page with no SAML content")
	}
	if !strings.Contains(err.Error(), "no SAML form or redirect") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestInitSAMLSessionIdPDomainBlocked(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	mux.HandleFunc("/saml.redirect", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// Form action points to a different domain not in allowedDomains
		_, _ = fmt.Fprint(w, `<html><form action="https://evil.example.com/idp">
			<input type="hidden" name="SAMLRequest" value="req"/>
		</form></html>`)
	})

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	err := InitSAMLSession(context.Background(), client, "/saml.redirect", srv.URL, []string{})
	if err == nil {
		t.Fatal("expected error for blocked IdP domain")
	}
	if !strings.Contains(err.Error(), "not in allowed domains") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestInitSAMLSessionAbsoluteURL(t *testing.T) {
	var called atomic.Bool
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	mux.HandleFunc("/saml.redirect", func(w http.ResponseWriter, _ *http.Request) {
		called.Store(true)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><form action="%s/idp">
			<input type="hidden" name="SAMLRequest" value="req"/>
		</form></html>`, srv.URL)
	})

	mux.HandleFunc("/idp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><form action="%s/digest">
			<input type="hidden" name="SAMLResponse" value="resp"/>
		</form></html>`, srv.URL)
	})

	mux.HandleFunc("/digest", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	// Pass absolute URL instead of relative path
	err := InitSAMLSession(context.Background(), client, srv.URL+"/saml.redirect", srv.URL, []string{})
	if err != nil {
		t.Fatalf("InitSAMLSession with absolute URL failed: %v", err)
	}
	if !called.Load() {
		t.Error("init URL was not called")
	}
}

func TestTrySAMLSSOSkippedWithNoAuth(t *testing.T) {
	var requestCount atomic.Int32

	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	mux.HandleFunc("/api/json", func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(403)
		_, _ = fmt.Fprintf(w, `<html><meta content="0;url=%s/idp/login" /></html>`, srv.URL)
	})

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	p := newTestProxy(client)
	pa := testPluginAuth(srv.URL)
	pa.Client = client
	pa.IsKerberos = true
	p.RegisterPlugin("jenkins", pa)

	resp := p.Execute(context.Background(), "jenkins", protocol.Message{
		ID:     "req-noauth",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		Path:   "/api/json",
		NoAuth: true,
	})

	if resp.Status != 403 {
		t.Errorf("status = %d, want 403 (noAuth should skip SSO)", resp.Status)
	}
	if requestCount.Load() != 1 {
		t.Errorf("request count = %d, want 1 (no SSO retry with noAuth)", requestCount.Load())
	}
}

// --- Regression tests for review findings ---

func TestParseSAMLFormReversedAttributes(t *testing.T) {
	// F2: attribute ordering — name before type
	body := `<html><form action="https://idp.example.com/saml">
		<input name="SAMLRequest" type="hidden" value="req123"/>
		<input value="/" name="RelayState" type="hidden"/>
	</form></html>`
	action, formData, ok := parseSAMLForm([]byte(body), "")
	if !ok {
		t.Fatal("parseSAMLForm should handle reversed attribute ordering")
	}
	if action != "https://idp.example.com/saml" {
		t.Errorf("action = %q, want https://idp.example.com/saml", action)
	}
	if formData.Get("SAMLRequest") != "req123" {
		t.Errorf("SAMLRequest = %q, want req123", formData.Get("SAMLRequest"))
	}
	if formData.Get("RelayState") != "/" {
		t.Errorf("RelayState = %q, want /", formData.Get("RelayState"))
	}
}

func TestParseSAMLFormRelativeAction(t *testing.T) {
	// F8: relative action URL resolved against baseURL
	body := `<html><form action="/saml/consume">
		<input type="hidden" name="SAMLResponse" value="resp"/>
	</form></html>`
	action, _, ok := parseSAMLForm([]byte(body), "https://app.example.com")
	if !ok {
		t.Fatal("parseSAMLForm should handle relative action")
	}
	if action != "https://app.example.com/saml/consume" {
		t.Errorf("action = %q, want https://app.example.com/saml/consume", action)
	}
}

func TestIsDomainAllowedForSSOTrailingDot(t *testing.T) {
	// F3: domain normalization — trailing dot should match
	allowed := []string{"idp.example.com."}
	if !isDomainAllowedForSSO("https://idp.example.com/saml", "https://app.example.com", allowed) {
		t.Error("trailing dot in allowed domain should match normalized request host")
	}
}

func TestHandleSAMLSSOBlocksRedirectToUnknownDomain(t *testing.T) {
	// F1: redirect to unallowed domain must be blocked
	var evilCalled atomic.Bool
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	evil := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		evilCalled.Store(true)
	}))
	defer evil.Close()

	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// IdP redirects to an evil domain via meta-refresh
		_, _ = fmt.Fprintf(w, `<html><meta content="0;url=%s/steal" /></html>`, evil.URL)
	})

	body := fmt.Sprintf(`<html><meta content="0;url=%s/login" /></html>`, srv.URL)

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	// Only allow srv.URL's domain, not the evil domain
	srvHost, _ := url.Parse(srv.URL)
	ok := handleSAMLSSO(context.Background(), client, []byte(body), srv.URL, []string{srvHost.Hostname()})
	if ok {
		t.Error("handleSAMLSSO should fail when IdP redirects to unallowed domain")
	}
	if evilCalled.Load() {
		t.Error("evil server was contacted despite domain not being allowed")
	}
}

func TestTrySAMLSSOSkipsReplayForPOST(t *testing.T) {
	// F4: non-idempotent methods should not be replayed after SSO
	var apiCalls atomic.Int32
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	srvHost, _ := url.Parse(srv.URL)

	mux.HandleFunc("/api/create", func(w http.ResponseWriter, _ *http.Request) {
		apiCalls.Add(1)
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(401)
		_, _ = fmt.Fprintf(w, `<html><meta content="0;url=%s/idp/sso" /></html>`, srv.URL)
	})

	mux.HandleFunc("/idp/sso", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><body>
			<div id="saml-post-binding">
			<form action="%s/saml/consume">
				<input type="hidden" name="SAMLResponse" value="resp"/>
			</form></div></body></html>`, srv.URL)
	})

	mux.HandleFunc("/saml/consume", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	p := newTestProxy(client)
	pa := testPluginAuth(srv.URL)
	pa.Client = client
	pa.IsKerberos = true
	pa.AllowedDomains = []string{srvHost.Hostname()}
	p.RegisterPlugin("test", pa)

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-post",
		Type:   protocol.TypeHTTPRequest,
		Method: "POST",
		Path:   "/api/create",
	})

	// SSO should run but the POST should NOT be replayed
	if apiCalls.Load() != 1 {
		t.Errorf("api calls = %d, want 1 (POST should not be replayed after SSO)", apiCalls.Load())
	}
	// The original 401 response should be returned since POST is not replayed
	if resp.Status != 401 {
		t.Errorf("status = %d, want 401 (original response for non-idempotent)", resp.Status)
	}
}
