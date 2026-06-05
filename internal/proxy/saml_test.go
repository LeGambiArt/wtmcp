package proxy

import (
	"context"
	"encoding/json"
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
			body:    `<html><meta http-equiv="refresh" content="0;url=https://idp.example.com/auth/realms/test" /></html>`,
			baseURL: "https://jenkins.example.com",
			want:    "https://idp.example.com/auth/realms/test",
		},
		{
			name:    "meta refresh relative",
			body:    `<html><meta http-equiv="refresh" content="0;url=/securityRealm/commenceLogin" /></html>`,
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
			body:    `<html><meta http-equiv="refresh" content="0;url=/login" /></html>`,
			baseURL: "https://jenkins.example.com/",
			want:    "https://jenkins.example.com/login",
		},
		{
			name:    "html entity in URL",
			body:    `<html><meta http-equiv="refresh" content="0;url=https://idp.example.com/auth?client_id=x&amp;state=y" /></html>`,
			baseURL: "https://jenkins.example.com",
			want:    "https://idp.example.com/auth?client_id=x&state=y",
		},
		{
			name:    "ignores non-refresh meta with description",
			body:    `<html><meta name="description" content="Visit url=https://evil.com" /></html>`,
			baseURL: "https://jenkins.example.com",
			want:    "",
		},
		{
			name:    "ignores wrong http-equiv value",
			body:    `<html><meta http-equiv="content-type" content="text/html; url=https://evil.com" /></html>`,
			baseURL: "https://jenkins.example.com",
			want:    "",
		},
		{
			name:    "js redirect var redirectUrl single-quoted",
			body:    `<html><script>var redirectUrl = 'https://example.com/login';</script></html>`,
			baseURL: "https://app.example.com",
			want:    "https://example.com/login",
		},
		{
			name:    "js redirect var redirectUrl double-quoted",
			body:    `<html><script>var redirectUrl = "https://example.com/login";</script></html>`,
			baseURL: "https://app.example.com",
			want:    "https://example.com/login",
		},
		{
			name:    "js redirect window.location",
			body:    `<html><script>window.location = 'https://idp.example.com/sso';</script></html>`,
			baseURL: "https://app.example.com",
			want:    "https://idp.example.com/sso",
		},
		{
			name:    "js redirect window.location.href",
			body:    `<html><script>window.location.href = 'https://idp.example.com/auth';</script></html>`,
			baseURL: "https://app.example.com",
			want:    "https://idp.example.com/auth",
		},
		{
			name:    "js redirect relative path",
			body:    `<html><script>var redirectUrl = '/auth/login';</script></html>`,
			baseURL: "https://app.example.com",
			want:    "https://app.example.com/auth/login",
		},
		{
			name:    "js no redirect in script",
			body:    `<html><script>var x = 42;</script></html>`,
			baseURL: "https://app.example.com",
			want:    "",
		},
		{
			name:    "meta refresh takes precedence over js redirect",
			body:    `<html><meta http-equiv="refresh" content="0;url=https://meta.example.com/login" /><script>var redirectUrl = 'https://js.example.com/login';</script></html>`,
			baseURL: "https://app.example.com",
			want:    "https://meta.example.com/login",
		},
		{
			name:    "external script tag ignored",
			body:    `<html><script src="/app.js"></script><meta http-equiv="refresh" content="0;url=https://example.com/login" /></html>`,
			baseURL: "https://app.example.com",
			want:    "https://example.com/login",
		},
		{
			name: "js redirect multi-line script with extra code",
			body: `


<html>
<body>
<script type="text/javascript">
	var redirectUrl = 'https://app.example.com/authgwy/corp/login.htmld?returnTo=%2fcorp%2fd%2ftask%2f1234';
	var anchor = encodeURIComponent(window.location.hash);
	if (anchor.length > 0) {
		redirectUrl = redirectUrl.replace(/([?|&]returnTo=[^\&;]+)/, '$1' + anchor);
	}
	window.location = redirectUrl;
</script>
</body>
</html>`,
			baseURL: "https://app.example.com",
			want:    "https://app.example.com/authgwy/corp/login.htmld?returnTo=%2fcorp%2fd%2ftask%2f1234",
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
		{
			name:           "userinfo rejected",
			rawURL:         "https://evil@idp.example.com/saml",
			baseURL:        "https://jenkins.example.com",
			allowedDomains: []string{"idp.example.com"},
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
		http.SetCookie(w, &http.Cookie{Name: "JSESSIONID", Value: "test-session", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
		w.WriteHeader(200)
	})

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	authRedirectBody := fmt.Sprintf(
		`<html><meta http-equiv="refresh" content="0;url=%s/securityRealm/commenceLogin" /></html>`,
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
	body := `<html><meta http-equiv="refresh" content="0;url=https://evil.com/steal" /></html>`
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
			_, _ = fmt.Fprintf(w, `<html><meta http-equiv="refresh" content="0;url=%s/idp/login" /></html>`, srv.URL)
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
		_, _ = fmt.Fprint(w, `<html><meta http-equiv="refresh" content="0;url=/login" /></html>`)
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
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "authenticated", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
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
		_, _ = fmt.Fprintf(w, `<html><meta http-equiv="refresh" content="0;url=%s/idp/sso" /></html>`, srv.URL)
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
		_, _ = fmt.Fprintf(w, `<html><meta http-equiv="refresh" content="0;url=%s/idp/login" /></html>`, srv.URL)
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

func TestParseSAMLFormMultipleForms(t *testing.T) {
	t.Run("second form SAMLResponse does not overwrite first", func(t *testing.T) {
		body := `<html>
		<form action="https://sp.example.com/acs">
			<input type="hidden" name="SAMLResponse" value="legit"/>
			<input type="hidden" name="RelayState" value="/"/>
		</form>
		<form action="https://other.example.com/feedback">
			<input type="hidden" name="SAMLResponse" value="overwritten"/>
			<input type="hidden" name="csrf" value="tok"/>
		</form></html>`
		action, formData, ok := parseSAMLForm([]byte(body), "")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if action != "https://sp.example.com/acs" {
			t.Errorf("action = %q, want first form's action", action)
		}
		if formData.Get("SAMLResponse") != "legit" {
			t.Errorf("SAMLResponse = %q, want legit (first form's value)", formData.Get("SAMLResponse"))
		}
		if formData.Get("csrf") != "" {
			t.Error("csrf from second form should not be collected")
		}
	})

	t.Run("input after closing form tag is excluded", func(t *testing.T) {
		body := `<html>
		<form action="https://sp.example.com/acs">
			<input type="hidden" name="SAMLResponse" value="legit"/>
		</form>
		<input type="hidden" name="injected" value="evil"/>
		</html>`
		_, formData, ok := parseSAMLForm([]byte(body), "")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if formData.Get("injected") != "" {
			t.Error("input outside form should not be collected")
		}
	})

	t.Run("first form without action returns ok=false", func(t *testing.T) {
		body := `<html>
		<form>
			<input type="hidden" name="SAMLResponse" value="data"/>
		</form>
		<form action="https://sp.example.com/acs">
			<input type="hidden" name="SAMLResponse" value="data2"/>
		</form></html>`
		_, _, ok := parseSAMLForm([]byte(body), "")
		if ok {
			t.Error("expected ok=false when first form has no action")
		}
	})

	t.Run("action comes from first form only", func(t *testing.T) {
		body := `<html>
		<form action="https://first.example.com/acs">
			<input type="hidden" name="SAMLResponse" value="data"/>
		</form>
		<form action="https://second.example.com/acs">
			<input type="hidden" name="Other" value="val"/>
		</form></html>`
		action, _, ok := parseSAMLForm([]byte(body), "")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if action != "https://first.example.com/acs" {
			t.Errorf("action = %q, want first form's action", action)
		}
	})
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
		_, _ = fmt.Fprintf(w, `<html><meta http-equiv="refresh" content="0;url=%s/steal" /></html>`, evil.URL)
	})

	body := fmt.Sprintf(`<html><meta http-equiv="refresh" content="0;url=%s/login" /></html>`, srv.URL)

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
		_, _ = fmt.Fprintf(w, `<html><meta http-equiv="refresh" content="0;url=%s/idp/sso" /></html>`, srv.URL)
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

// --- Tests for 200+SAML reactive SSO ---

func TestTrySAMLSSO200WithSAMLForm(t *testing.T) {
	var apiCalls atomic.Int32
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	mux.HandleFunc("/api/data", func(w http.ResponseWriter, _ *http.Request) {
		n := apiCalls.Add(1)
		if n == 1 {
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprintf(w, `<html><form action="%s/idp/sso">
				<input type="hidden" name="SAMLRequest" value="req"/>
			</form></html>`, srv.URL)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":"ok"}`)
	})
	mux.HandleFunc("/idp/sso", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><form action="%s/acs">
			<input type="hidden" name="SAMLResponse" value="resp"/>
		</form></html>`, srv.URL)
	})
	mux.HandleFunc("/acs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	p := newTestProxy(client)
	pa := testPluginAuth(srv.URL)
	pa.Client = client
	pa.IsKerberos = true
	p.RegisterPlugin("test200", pa)

	resp := p.Execute(context.Background(), "test200", protocol.Message{
		ID: "req-200saml", Type: protocol.TypeHTTPRequest,
		Method: "GET", Path: "/api/data",
	})
	if resp.Status != 200 {
		t.Errorf("status = %d, want 200", resp.Status)
	}
	if apiCalls.Load() != 2 {
		t.Errorf("api calls = %d, want 2 (initial + retry after SSO)", apiCalls.Load())
	}
}

func TestTrySAMLSSO200WithNormalHTML(t *testing.T) {
	htmlBody := "<html><body>" + strings.Repeat("<p>Normal content.</p>\n", 100) + "</body></html>"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(htmlBody))
	}))
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	p := newTestProxy(client)
	pa := testPluginAuth(srv.URL)
	pa.Client = client
	pa.IsKerberos = true
	p.RegisterPlugin("testhtml", pa)

	resp := p.Execute(context.Background(), "testhtml", protocol.Message{
		ID: "req-html", Type: protocol.TypeHTTPRequest,
		Method: "GET", Path: "/",
	})
	if resp.Status != 200 {
		t.Errorf("status = %d, want 200", resp.Status)
	}
	// Body is JSON-encoded as a quoted string for text/html
	var got string
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatalf("body is not a JSON string: %v", err)
	}
	if got != htmlBody {
		t.Errorf("body length = %d, want %d (body should be preserved)", len(got), len(htmlBody))
	}
}

func TestTrySAMLSSO200WithJSON(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	p := newTestProxy(client)
	pa := testPluginAuth(srv.URL)
	pa.Client = client
	pa.IsKerberos = true
	p.RegisterPlugin("testjson", pa)

	resp := p.Execute(context.Background(), "testjson", protocol.Message{
		ID: "req-json", Type: protocol.TypeHTTPRequest,
		Method: "GET", Path: "/",
	})
	if resp.Status != 200 {
		t.Errorf("status = %d, want 200", resp.Status)
	}
}

func TestTrySAMLSSO200LargeHTML(t *testing.T) {
	bigBody := "<html>" + strings.Repeat("x", 2*1024*1024) + "</html>"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(bigBody))
	}))
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	p := newTestProxy(client)
	pa := testPluginAuth(srv.URL)
	pa.Client = client
	pa.IsKerberos = true
	p.RegisterPlugin("testlarge", pa)

	resp := p.Execute(context.Background(), "testlarge", protocol.Message{
		ID: "req-large", Type: protocol.TypeHTTPRequest,
		Method: "GET", Path: "/",
	})
	if resp.Status != 200 {
		t.Errorf("status = %d, want 200", resp.Status)
	}
	// Body should NOT be truncated — peek only reads 8KB
	if len(resp.Body) < 2*1024*1024 {
		t.Errorf("body length = %d, want >= 2MB (body should not be truncated by peek)", len(resp.Body))
	}
}

func TestTrySAMLSSO200POSTNotReplayed(t *testing.T) {
	var apiCalls atomic.Int32
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	mux.HandleFunc("/api/create", func(w http.ResponseWriter, _ *http.Request) {
		apiCalls.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><form action="%s/idp">
			<input type="hidden" name="SAMLRequest" value="req"/>
		</form></html>`, srv.URL)
	})
	mux.HandleFunc("/idp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><form action="%s/acs">
			<input type="hidden" name="SAMLResponse" value="resp"/>
		</form></html>`, srv.URL)
	})
	mux.HandleFunc("/acs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	p := newTestProxy(client)
	pa := testPluginAuth(srv.URL)
	pa.Client = client
	pa.IsKerberos = true
	p.RegisterPlugin("testpost200", pa)

	resp := p.Execute(context.Background(), "testpost200", protocol.Message{
		ID: "req-post200", Type: protocol.TypeHTTPRequest,
		Method: "POST", Path: "/api/create",
	})
	if apiCalls.Load() != 1 {
		t.Errorf("api calls = %d, want 1 (POST should not be replayed)", apiCalls.Load())
	}
	if resp.Status != 200 {
		t.Errorf("status = %d, want 200", resp.Status)
	}
}

func TestTrySAMLSSO200FalsePositive(t *testing.T) {
	htmlBody := `<html><body>
		<form class="search"><input type="text" name="q"/></form>
		<p>The SAMLRequest parameter is used for SAML POST binding.</p>
	</body></html>`

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(htmlBody))
	}))
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	p := newTestProxy(client)
	pa := testPluginAuth(srv.URL)
	pa.Client = client
	pa.IsKerberos = true
	p.RegisterPlugin("testfp", pa)

	resp := p.Execute(context.Background(), "testfp", protocol.Message{
		ID: "req-fp", Type: protocol.TypeHTTPRequest,
		Method: "GET", Path: "/",
	})
	if resp.Status != 200 {
		t.Errorf("status = %d, want 200", resp.Status)
	}
	var got string
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatalf("body is not a JSON string: %v", err)
	}
	if got != htmlBody {
		t.Error("body should be returned unchanged on false positive")
	}
}

func TestTrySAMLSSO200Cooldown(t *testing.T) {
	var idpCalls atomic.Int32
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	mux.HandleFunc("/api", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><form action="%s/idp">
			<input type="hidden" name="SAMLRequest" value="req"/>
		</form></html>`, srv.URL)
	})
	mux.HandleFunc("/idp", func(w http.ResponseWriter, _ *http.Request) {
		idpCalls.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><form action="%s/acs">
			<input type="hidden" name="SAMLResponse" value="resp"/>
		</form></html>`, srv.URL)
	})
	mux.HandleFunc("/acs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	p := newTestProxy(client)
	pa := testPluginAuth(srv.URL)
	pa.Client = client
	pa.IsKerberos = true
	p.RegisterPlugin("testcooldown", pa)

	// First call triggers SSO
	p.Execute(context.Background(), "testcooldown", protocol.Message{
		ID: "req-cd1", Type: protocol.TypeHTTPRequest,
		Method: "GET", Path: "/api",
	})
	first := idpCalls.Load()

	// Second call should be skipped by cooldown
	p.Execute(context.Background(), "testcooldown", protocol.Message{
		ID: "req-cd2", Type: protocol.TypeHTTPRequest,
		Method: "GET", Path: "/api",
	})
	second := idpCalls.Load()

	if second != first {
		t.Errorf("IdP calls after cooldown = %d, want %d (cooldown should prevent second SSO)", second, first)
	}
}

func TestTrySAMLSSO200RetryStillSAML(t *testing.T) {
	var apiCalls atomic.Int32
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	mux.HandleFunc("/api", func(w http.ResponseWriter, _ *http.Request) {
		apiCalls.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><form action="%s/idp">
			<input type="hidden" name="SAMLRequest" value="req"/>
		</form></html>`, srv.URL)
	})
	mux.HandleFunc("/idp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><form action="%s/acs">
			<input type="hidden" name="SAMLResponse" value="resp"/>
		</form></html>`, srv.URL)
	})
	mux.HandleFunc("/acs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	p := newTestProxy(client)
	pa := testPluginAuth(srv.URL)
	pa.Client = client
	pa.IsKerberos = true
	p.RegisterPlugin("testloop", pa)

	p.Execute(context.Background(), "testloop", protocol.Message{
		ID: "req-loop", Type: protocol.TypeHTTPRequest,
		Method: "GET", Path: "/api",
	})
	if apiCalls.Load() > 2 {
		t.Errorf("api calls = %d, want at most 2 (initial + retry, no loop)", apiCalls.Load())
	}
}

func TestTrySAMLSSO200ContentTypeCharset(t *testing.T) {
	var apiCalls atomic.Int32
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		n := apiCalls.Add(1)
		if n == 1 {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprintf(w, `<html><form action="%s/idp">
				<input type="hidden" name="SAMLRequest" value="req"/>
			</form></html>`, srv.URL)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/idp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><form action="%s/acs">
			<input type="hidden" name="SAMLResponse" value="resp"/>
		</form></html>`, srv.URL)
	})
	mux.HandleFunc("/acs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	p := newTestProxy(client)
	pa := testPluginAuth(srv.URL)
	pa.Client = client
	pa.IsKerberos = true
	p.RegisterPlugin("testcharset", pa)

	resp := p.Execute(context.Background(), "testcharset", protocol.Message{
		ID: "req-charset", Type: protocol.TypeHTTPRequest,
		Method: "GET", Path: "/",
	})
	if apiCalls.Load() != 2 {
		t.Errorf("api calls = %d, want 2 (charset in content-type should be handled)", apiCalls.Load())
	}
	if resp.Status != 200 {
		t.Errorf("status = %d, want 200", resp.Status)
	}
}

func TestTrySAMLSSO200UppercaseForm(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><FORM action="https://idp.example.com/sso">
			<INPUT TYPE="hidden" NAME="SAMLRequest" VALUE="req"/>
		</FORM></html>`)
	}))
	defer srv.Close()

	peek := []byte(`<html><FORM action="https://idp.example.com/sso">
		<INPUT TYPE="hidden" NAME="SAMLRequest" VALUE="req"/>
	</FORM></html>`)
	if !isSAMLChallenge(peek) {
		t.Error("isSAMLChallenge should detect uppercase <FORM> tags")
	}
}

func TestIsSAMLChallengeNegative(t *testing.T) {
	tests := []struct {
		name string
		peek string
	}{
		{"no form", `<html><p>SAMLRequest mentioned in text</p></html>`},
		{"form without SAML", `<html><form action="/search"><input type="text"/></form></html>`},
		{"empty", ""},
		{"json-like", `{"SAMLRequest": "value"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if isSAMLChallenge([]byte(tt.peek)) {
				t.Errorf("isSAMLChallenge should return false for %q", tt.name)
			}
		})
	}
}
