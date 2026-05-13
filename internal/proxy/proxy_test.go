package proxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeGambiArt/wtmcp/internal/auth"
	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/protocol"
)

func newTestProxy(client *http.Client) *Proxy {
	return New(client, 10*1024*1024, 45*time.Second)
}

// testPluginAuth creates a PluginAuth with the base URL hostname
// auto-added to AllowedDomains, simulating what manager.go does.
func testPluginAuth(baseURL string) *PluginAuth {
	pa := &PluginAuth{BaseURL: baseURL}
	if u, err := url.Parse(baseURL); err == nil && u.Hostname() != "" {
		pa.AllowedDomains = []string{u.Hostname()}
	}
	return pa
}

func TestExecuteGET(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/test" {
			t.Errorf("path = %q, want /api/test", r.URL.Path)
		}
		if r.URL.Query().Get("foo") != "bar" {
			t.Errorf("query foo = %q, want bar", r.URL.Query().Get("foo"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-1",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		Path:   "/api/test",
		Query:  map[string]any{"foo": "bar"},
	})

	if resp.Status != 200 {
		t.Errorf("status = %d, want 200", resp.Status)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %v", resp.Error)
	}
	if string(resp.Body) != `{"ok":true}` {
		t.Errorf("body = %s", resp.Body)
	}
}

func TestExecutePOST(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":true}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:      "req-2",
		Type:    protocol.TypeHTTPRequest,
		Method:  "POST",
		Path:    "/items",
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    json.RawMessage(`{"name":"item1"}`),
	})

	if resp.Status != 200 {
		t.Errorf("status = %d", resp.Status)
	}
}

func TestExecuteWithAuth(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer test-token" {
			t.Errorf("Authorization = %q, want 'Bearer test-token'", authHeader)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	pa := testPluginAuth(srv.URL)
	pa.Provider, _ = auth.NewBearerProvider("test-token", "", "")
	p.RegisterPlugin("test", pa)

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-3",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		Path:   "/secure",
	})

	if resp.Status != 200 {
		t.Errorf("status = %d, error = %v", resp.Status, resp.Error)
	}
}

func TestExecuteQueryArrays(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields := r.URL.Query()["field"]
		if len(fields) != 2 || fields[0] != "summary" || fields[1] != "status" {
			t.Errorf("field = %v, want [summary, status]", fields)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-4",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		Path:   "/search",
		Query:  map[string]any{"field": []any{"summary", "status"}},
	})

	if resp.Status != 200 {
		t.Errorf("status = %d, error = %v", resp.Status, resp.Error)
	}
}

func TestExecuteResponseBodyLimit(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Write a body larger than the limit
		data := make([]byte, 1024)
		for i := range data {
			data[i] = 'x'
		}
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	// Set a tiny max body size (srv.Client has its own transport, no SSRF check)
	p := New(srv.Client(), 100, 45*time.Second)
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-5",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		Path:   "/big",
	})

	if resp.Error == nil {
		t.Error("expected error for oversized response")
	}
	if resp.Error != nil && resp.Error.Code != "response_too_large" {
		t.Errorf("error code = %q, want response_too_large", resp.Error.Code)
	}
}

func TestExecuteNonJSONResponse(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-6",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		Path:   "/text",
	})

	if resp.Status != 200 {
		t.Errorf("status = %d", resp.Status)
	}
	// Non-JSON is returned as a quoted string
	if string(resp.Body) != `"hello world"` {
		t.Errorf("body = %s, want %q", resp.Body, `"hello world"`)
	}
}

func TestExecuteUnknownPlugin(t *testing.T) {
	p := newTestProxy(nil)

	resp := p.Execute(context.Background(), "nonexistent", protocol.Message{
		ID: "req-7", Type: protocol.TypeHTTPRequest, Method: "GET", Path: "/",
	})

	if resp.Error == nil || resp.Error.Code != "no_config" {
		t.Errorf("expected no_config error, got %v", resp.Error)
	}
}

func TestIsDomainAllowed(t *testing.T) {
	p := newTestProxy(nil)

	tests := []struct {
		name    string
		pa      *PluginAuth
		rawURL  string
		allowed bool
	}{
		{
			name:    "same domain",
			pa:      &PluginAuth{BaseURL: "https://api.example.com", AllowedDomains: []string{"api.example.com"}},
			rawURL:  "https://api.example.com/other",
			allowed: true,
		},
		{
			name:    "case insensitive",
			pa:      &PluginAuth{BaseURL: "https://api.example.com", AllowedDomains: []string{"api.example.com"}},
			rawURL:  "https://API.EXAMPLE.COM/other",
			allowed: true,
		},
		{
			name:    "allowed domain",
			pa:      &PluginAuth{BaseURL: "https://api.example.com", AllowedDomains: []string{"api.example.com", "cdn.example.com"}},
			rawURL:  "https://cdn.example.com/file",
			allowed: true,
		},
		{
			name:    "different domain",
			pa:      &PluginAuth{BaseURL: "https://api.example.com", AllowedDomains: []string{"api.example.com"}},
			rawURL:  "https://evil.com/steal",
			allowed: false,
		},
		{
			name:    "userinfo rejects",
			pa:      &PluginAuth{BaseURL: "https://api.example.com", AllowedDomains: []string{"api.example.com"}},
			rawURL:  "https://evil@api.example.com/path",
			allowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.isDomainAllowed("test", tt.pa, tt.rawURL)
			if got != tt.allowed {
				t.Errorf("isDomainAllowed = %v, want %v", got, tt.allowed)
			}
		})
	}
}

func TestExecuteFullURLOverride(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"override":true}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-8",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		URL:    srv.URL + "/full-override",
		Path:   "/ignored",
	})

	if resp.Status != 200 {
		t.Errorf("status = %d, error = %v", resp.Status, resp.Error)
	}
}

func TestResponseHeaders(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom", "test-value")
		w.Header().Set("Content-Disposition", `attachment; filename="report.pdf"`)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID: "req-headers", Type: protocol.TypeHTTPRequest, Method: "GET", Path: "/",
	})

	if resp.Headers == nil {
		t.Fatal("expected response headers")
	}
	if resp.Headers["X-Custom"] != "test-value" {
		t.Errorf("X-Custom = %q", resp.Headers["X-Custom"])
	}
	if resp.Headers["Content-Disposition"] != `attachment; filename="report.pdf"` {
		t.Errorf("Content-Disposition = %q", resp.Headers["Content-Disposition"])
	}
}

func TestBinaryResponse(t *testing.T) {
	// PNG magic bytes
	pngData := []byte{0x89, 0x50, 0x4e, 0x47}

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngData)
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID: "req-bin", Type: protocol.TypeHTTPRequest, Method: "GET", Path: "/image.png",
	})

	if resp.Status != 200 {
		t.Fatalf("status = %d, error = %v", resp.Status, resp.Error)
	}
	if resp.BodyEncoding != "base64" {
		t.Errorf("BodyEncoding = %q, want base64", resp.BodyEncoding)
	}

	// Body should be a JSON string containing the base64 data
	var b64str string
	if err := json.Unmarshal(resp.Body, &b64str); err != nil {
		t.Fatalf("unmarshal base64 string: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(b64str)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	if string(decoded) != string(pngData) {
		t.Errorf("decoded = %x, want %x", decoded, pngData)
	}
}

func TestTextResponse(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID: "req-text", Type: protocol.TypeHTTPRequest, Method: "GET", Path: "/text",
	})

	if resp.Status != 200 {
		t.Fatalf("status = %d, error = %v", resp.Status, resp.Error)
	}
	if resp.BodyEncoding != "" {
		t.Errorf("BodyEncoding = %q, want empty for text", resp.BodyEncoding)
	}

	var text string
	if err := json.Unmarshal(resp.Body, &text); err != nil {
		t.Fatalf("unmarshal text: %v", err)
	}
	if text != "hello world" {
		t.Errorf("text = %q", text)
	}
}

func TestMultipartFileUpload(t *testing.T) {
	pngData := []byte{0x89, 0x50, 0x4e, 0x47}

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "multipart/form-data") {
			t.Fatalf("Content-Type = %q, want multipart/form-data", ct)
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil { //nolint:gosec // test server
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("FormFile: %v", err)
		}
		defer func() { _ = file.Close() }()

		if header.Filename != "test.png" {
			t.Errorf("filename = %q, want test.png", header.Filename)
		}
		if header.Header.Get("Content-Type") != "image/png" {
			t.Errorf("part Content-Type = %q, want image/png", header.Header.Get("Content-Type"))
		}
		content, _ := io.ReadAll(file)
		if string(content) != string(pngData) {
			t.Errorf("content = %x, want %x", content, pngData)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"att-1","filename":"test.png"}]`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-mp-1",
		Type:   protocol.TypeHTTPRequest,
		Method: "POST",
		Path:   "/upload",
		Multipart: []protocol.MultipartPart{{
			Field:        "file",
			Filename:     "test.png",
			ContentType:  "image/png",
			Body:         base64.StdEncoding.EncodeToString(pngData),
			BodyEncoding: "base64",
		}},
	})

	if resp.Status != 200 {
		t.Errorf("status = %d, error = %v", resp.Status, resp.Error)
	}
}

func TestMultipartTextField(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(10 << 20); err != nil { //nolint:gosec // test server
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		comment := r.FormValue("comment") //nolint:gosec // test server
		if comment != "test comment" {
			t.Errorf("comment = %q, want 'test comment'", comment)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-mp-2",
		Type:   protocol.TypeHTTPRequest,
		Method: "POST",
		Path:   "/form",
		Multipart: []protocol.MultipartPart{
			{Field: "comment", Body: "test comment"},
		},
	})

	if resp.Status != 200 {
		t.Errorf("status = %d, error = %v", resp.Status, resp.Error)
	}
}

func TestMultipartInvalidBase64(t *testing.T) {
	p := newTestProxy(nil)
	p.RegisterPlugin("test", testPluginAuth("https://example.com"))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-mp-bad",
		Type:   protocol.TypeHTTPRequest,
		Method: "POST",
		Path:   "/upload",
		Multipart: []protocol.MultipartPart{
			{Field: "file", Filename: "bad.bin", Body: "not-valid-base64!!!", BodyEncoding: "base64"},
		},
	})

	if resp.Error == nil || resp.Error.Code != "build_request" {
		t.Errorf("expected build_request error, got %v", resp.Error)
	}
}

func TestStripDangerousHeaders(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// These headers must have been stripped
		stripped := []string{
			"Cookie", "Authorization", "Proxy-Authorization",
			"Private-Token", "X-Api-Key",
			"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto",
			"X-Real-Ip", "X-Original-Url", "X-Rewrite-Url",
			"Connection", "Upgrade", "Transfer-Encoding",
			"Te", "Trailer", "Forwarded",
		}
		for _, h := range stripped {
			if v := r.Header.Get(h); v != "" {
				t.Errorf("header %s = %q, should have been stripped", h, v)
			}
		}
		// Safe headers should pass through
		if v := r.Header.Get("X-Custom"); v != "keep-me" {
			t.Errorf("X-Custom = %q, want keep-me", v)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID: "req-headers", Type: protocol.TypeHTTPRequest, Method: "GET", Path: "/",
		Headers: map[string]string{
			"Cookie":              "session=stolen",
			"Authorization":       "Bearer stolen-token",
			"Proxy-Authorization": "Basic creds",
			"Private-Token":       "glpat-stolen",
			"X-Api-Key":           "stolen-api-key",
			"X-Forwarded-For":     "1.2.3.4",
			"X-Forwarded-Host":    "evil.com",
			"X-Forwarded-Proto":   "http",
			"X-Real-Ip":           "10.0.0.1",
			"X-Original-Url":      "/admin",
			"X-Rewrite-Url":       "/secret",
			"Connection":          "keep-alive",
			"Upgrade":             "websocket",
			"Transfer-Encoding":   "chunked",
			"Te":                  "trailers",
			"Trailer":             "X-Checksum",
			"Forwarded":           "for=1.2.3.4",
			"X-Custom":            "keep-me",
		},
	})

	if resp.Status != 200 {
		t.Fatalf("status = %d, error = %v", resp.Status, resp.Error)
	}
}

func TestMultipartOverridesContentType(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "multipart/form-data; boundary=") {
			t.Errorf("Content-Type = %q, want multipart/form-data with boundary", ct)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:      "req-mp-ct",
		Type:    protocol.TypeHTTPRequest,
		Method:  "POST",
		Path:    "/upload",
		Headers: map[string]string{"Content-Type": "application/json"},
		Multipart: []protocol.MultipartPart{
			{Field: "file", Filename: "f.txt", Body: "data"},
		},
	})

	if resp.Status != 200 {
		t.Errorf("status = %d, error = %v", resp.Status, resp.Error)
	}
}

func TestSafeDialerRejectsLoopback(t *testing.T) {
	// Default proxy (nil client) uses safe dialer — should reject localhost
	p := New(nil, 10*1024*1024, 45*time.Second)
	p.RegisterPlugin("test", testPluginAuth("https://127.0.0.1"))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID: "req-ssrf", Type: protocol.TypeHTTPRequest, Method: "GET", Path: "/",
	})

	if resp.Error == nil || resp.Error.Code != "transport_error" {
		t.Errorf("expected transport_error for loopback, got status=%d error=%v", resp.Status, resp.Error)
	}
	if resp.Error != nil && !strings.Contains(resp.Error.Message, "SSRF blocked") {
		t.Errorf("expected SSRF blocked message, got %q", resp.Error.Message)
	}
}

func TestCheckIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1",
		"10.0.0.1",
		"192.168.1.1",
		"172.16.0.1",
		"0.0.0.0",
		"::1",
	}
	for _, ip := range blocked {
		if err := checkIP(ip); err == nil {
			t.Errorf("checkIP(%q) = nil, want error", ip)
		}
	}

	allowed := []string{
		"8.8.8.8",
		"1.1.1.1",
		"93.184.216.34",
	}
	for _, ip := range allowed {
		if err := checkIP(ip); err != nil {
			t.Errorf("checkIP(%q) = %v, want nil", ip, err)
		}
	}
}

func TestCheckIPv6MappedIPv4(t *testing.T) {
	blocked := []string{
		"::ffff:127.0.0.1",
		"::ffff:10.0.0.1",
		"::ffff:192.168.1.1",
	}
	for _, ip := range blocked {
		if err := checkIP(ip); err == nil {
			t.Errorf("checkIP(%q) = nil, want SSRF blocked", ip)
		}
	}
}

func TestCheckIPLinkLocal(t *testing.T) {
	blocked := []string{
		"169.254.1.1",
		"fe80::1",
	}
	for _, ip := range blocked {
		if err := checkIP(ip); err == nil {
			t.Errorf("checkIP(%q) = nil, want blocked", ip)
		}
	}
}

func TestCheckIPMulticast(t *testing.T) {
	blocked := []string{
		"224.0.0.1",
		"239.255.0.1",
		"ff02::1",
		"ff05::1",
	}
	for _, ip := range blocked {
		if err := checkIP(ip); err == nil {
			t.Errorf("checkIP(%q) = nil, want blocked", ip)
		}
	}
}

func TestStripAuthOnCrossDomainRedirect(t *testing.T) {
	mustParse := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	t.Run("cross domain strips auth headers", func(t *testing.T) {
		via := []*http.Request{{URL: mustParse("https://a.example.com/path")}}
		req := &http.Request{
			URL: mustParse("https://b.example.com/other"),
			Header: http.Header{
				"Authorization": {"Bearer tok"},
				"Cookie":        {"session=abc"},
				"Private-Token": {"glpat-xxx"},
				"X-Api-Key":     {"key-123"},
			},
		}
		if err := StripAuthOnCrossDomainRedirect(req, via); err != nil {
			t.Fatal(err)
		}
		for _, h := range []string{"Authorization", "Cookie", "Private-Token", "X-Api-Key"} {
			if req.Header.Get(h) != "" {
				t.Errorf("header %s should be stripped on cross-domain redirect", h)
			}
		}
	})

	t.Run("same domain preserves headers", func(t *testing.T) {
		via := []*http.Request{{URL: mustParse("https://api.example.com/a")}}
		req := &http.Request{
			URL: mustParse("https://api.example.com/b"),
			Header: http.Header{
				"Authorization": {"Bearer tok"},
			},
		}
		if err := StripAuthOnCrossDomainRedirect(req, via); err != nil {
			t.Fatal(err)
		}
		if req.Header.Get("Authorization") == "" {
			t.Error("header should be preserved on same-domain redirect")
		}
	})

	t.Run("redirect limit", func(t *testing.T) {
		via := make([]*http.Request, 10)
		for i := range via {
			via[i] = &http.Request{URL: mustParse("https://example.com")}
		}
		req := &http.Request{URL: mustParse("https://example.com/11")}
		if err := StripAuthOnCrossDomainRedirect(req, via); err == nil {
			t.Error("expected error after 10 redirects")
		}
	})
}

func TestSafeDialerAllowPrivate(t *testing.T) {
	d := &safeDialer{allowPrivate: true}
	// Should not error when connecting to localhost with allowPrivate
	conn, err := d.DialContext(context.Background(), "tcp", "127.0.0.1:0")
	// Will fail to connect (nothing listening on port 0) but should not
	// fail with SSRF error
	if err != nil && strings.Contains(err.Error(), "SSRF blocked") {
		t.Errorf("allowPrivate should not block: %v", err)
	}
	if conn != nil {
		_ = conn.Close()
	}
}

func TestAllowPrivateIPsUsesPrivateClient(t *testing.T) {
	// Start a TLS server on localhost — this resolves to 127.0.0.1
	// which is normally blocked by the SSRF dialer.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"private":true}`))
	}))
	defer srv.Close()

	// Create proxy with the test server's TLS client as the
	// privateClient so the test can reach the local test server.
	p := &Proxy{
		plugins: make(map[string]*PluginAuth),
		client: &http.Client{
			Transport:     safeTransport(false),
			CheckRedirect: StripAuthOnCrossDomainRedirect,
		},
		privateClient: srv.Client(),
		maxBodySize:   10 * 1024 * 1024,
	}

	// Plugin with AllowPrivateIPs should reach the server
	pa := testPluginAuth(srv.URL)
	pa.AllowPrivateIPs = true
	p.RegisterPlugin("private-ok", pa)

	resp := p.Execute(context.Background(), "private-ok", protocol.Message{
		ID: "req-priv", Type: protocol.TypeHTTPRequest, Method: "GET", Path: "/",
	})
	if resp.Status != 200 {
		t.Errorf("AllowPrivateIPs plugin: status = %d, error = %v", resp.Status, resp.Error)
	}
	if string(resp.Body) != `{"private":true}` {
		t.Errorf("body = %s", resp.Body)
	}
}

func TestDefaultPluginBlocksPrivateIPs(t *testing.T) {
	// Default proxy should block loopback even when a server is there
	p := New(nil, 10*1024*1024, 45*time.Second)
	pa := testPluginAuth("https://127.0.0.1")
	pa.AllowPrivateIPs = false
	p.RegisterPlugin("strict", pa)

	resp := p.Execute(context.Background(), "strict", protocol.Message{
		ID: "req-strict", Type: protocol.TypeHTTPRequest, Method: "GET", Path: "/",
	})

	if resp.Error == nil || resp.Error.Code != "transport_error" {
		t.Errorf("expected transport_error, got status=%d error=%v", resp.Status, resp.Error)
	}
	if resp.Error != nil && !strings.Contains(resp.Error.Message, "SSRF blocked") {
		t.Errorf("expected SSRF blocked, got %q", resp.Error.Message)
	}
}

func TestAllowPrivateIPsWithAuth(t *testing.T) {
	// Verify that AllowPrivateIPs + auth provider injects auth correctly
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer private-token" {
			t.Errorf("Authorization = %q, want 'Bearer private-token'", authHeader)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authed":true}`))
	}))
	defer srv.Close()

	p := &Proxy{
		plugins: make(map[string]*PluginAuth),
		client: &http.Client{
			Transport:     safeTransport(false),
			CheckRedirect: StripAuthOnCrossDomainRedirect,
		},
		privateClient: srv.Client(),
		maxBodySize:   10 * 1024 * 1024,
	}

	paAuth := testPluginAuth(srv.URL)
	paAuth.AllowPrivateIPs = true
	paAuth.Provider, _ = auth.NewBearerProvider("private-token", "", "")
	p.RegisterPlugin("priv-auth", paAuth)

	resp := p.Execute(context.Background(), "priv-auth", protocol.Message{
		ID: "req-priv-auth", Type: protocol.TypeHTTPRequest, Method: "GET", Path: "/secure",
	})

	if resp.Status != 200 {
		t.Errorf("status = %d, error = %v", resp.Status, resp.Error)
	}
}

func TestAllowPrivateIPsWithPerPluginClient(t *testing.T) {
	// When a per-plugin Client is set (e.g., Kerberos), it takes
	// precedence over AllowPrivateIPs client selection.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"custom":true}`))
	}))
	defer srv.Close()

	p := New(nil, 10*1024*1024, 45*time.Second)
	paCustom := testPluginAuth(srv.URL)
	paCustom.AllowPrivateIPs = true
	paCustom.Client = srv.Client() // per-plugin client overrides
	p.RegisterPlugin("custom-client", paCustom)

	resp := p.Execute(context.Background(), "custom-client", protocol.Message{
		ID: "req-custom", Type: protocol.TypeHTTPRequest, Method: "GET", Path: "/",
	})

	if resp.Status != 200 {
		t.Errorf("status = %d, error = %v", resp.Status, resp.Error)
	}
}

func TestNoAuthSkipsAuthInjection(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("expected no Authorization header with no_auth")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	pa := testPluginAuth(srv.URL)
	pa.Provider, _ = auth.NewBearerProvider("secret-token", "", "")
	p.RegisterPlugin("test", pa)

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-noauth",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		Path:   "/public",
		NoAuth: true,
	})

	if resp.Status != 200 {
		t.Errorf("status = %d, error = %v", resp.Status, resp.Error)
	}
}

func TestNoAuthAllowsHTTPWithHeaderAuth(t *testing.T) {
	p := newTestProxy(nil)
	pa := testPluginAuth("https://api.example.com")
	pa.Provider, _ = auth.NewBearerProvider("token", "", "")
	p.RegisterPlugin("test", pa)

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-http-noauth",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		URL:    "http://api.example.com/public",
		NoAuth: true,
	})

	// Should not get "HTTPS required" — transport error expected (no server)
	if resp.Error != nil && strings.Contains(resp.Error.Message, "HTTPS required") {
		t.Error("no_auth should bypass HTTPS enforcement for header auth")
	}
}

func TestHTTPSRequiredWithClientCert(t *testing.T) {
	p := newTestProxy(nil)
	pa := testPluginAuth("https://service.example.com")
	pa.TLS = TLSConfig{ClientCert: "/tmp/cert.pem", ClientKey: "/tmp/key.pem"}
	p.RegisterPlugin("test", pa)

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-mtls-http",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		URL:    "http://service.example.com/api",
	})

	if resp.Error == nil || !strings.Contains(resp.Error.Message, "HTTPS required when client certificates") {
		t.Errorf("expected HTTPS required error for mTLS, got %v", resp.Error)
	}
}

func TestHTTPSRequiredWithClientCertNoAuth(t *testing.T) {
	// mTLS HTTPS enforcement should NOT be bypassable by no_auth
	p := newTestProxy(nil)
	pa := testPluginAuth("https://service.example.com")
	pa.TLS = TLSConfig{ClientCert: "/tmp/cert.pem", ClientKey: "/tmp/key.pem"}
	p.RegisterPlugin("test", pa)

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-mtls-noauth",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		URL:    "http://service.example.com/api",
		NoAuth: true,
	})

	if resp.Error == nil || !strings.Contains(resp.Error.Message, "HTTPS required when client certificates") {
		t.Errorf("no_auth should NOT bypass mTLS HTTPS, got %v", resp.Error)
	}
}

func TestSchemeValidation(t *testing.T) {
	p := newTestProxy(nil)
	pa := testPluginAuth("https://example.com")
	p.RegisterPlugin("test", pa)

	blocked := []string{
		"ftp://example.com/file",
		"gopher://example.com",
	}
	for _, u := range blocked {
		resp := p.Execute(context.Background(), "test", protocol.Message{
			ID:     "req-scheme",
			Type:   protocol.TypeHTTPRequest,
			Method: "GET",
			URL:    u,
		})
		if resp.Error == nil || !strings.Contains(resp.Error.Message, "unsupported scheme") {
			t.Errorf("URL %q: expected scheme validation error, got %v", u, resp.Error)
		}
	}

	// file:// is rejected by domain validation (no host to match)
	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-file",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		URL:    "file:///etc/passwd",
	})
	if resp.Error == nil {
		t.Error("file:// URL should be rejected")
	}
}

func TestExecuteClientTimeout(t *testing.T) {
	// Server that accepts connections but responds slowly
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// Block until request context is done (server shuts down or client disconnects)
		<-r.Context().Done()
	}))
	defer srv.Close()

	// Create proxy with a very short timeout
	client := srv.Client()
	p := &Proxy{
		plugins: make(map[string]*PluginAuth),
		client: &http.Client{
			Transport: client.Transport,
			Timeout:   200 * time.Millisecond,
		},
		privateClient: &http.Client{
			Timeout: 200 * time.Millisecond,
		},
		maxBodySize: 10 * 1024 * 1024,
	}
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-timeout",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		Path:   "/slow",
	})

	if resp.Error == nil {
		t.Fatal("expected timeout error")
	}
	if resp.Status != 0 {
		t.Errorf("status = %d, want 0 for timeout", resp.Status)
	}
}

func TestExecuteContextCancellation(t *testing.T) {
	// Server that blocks until request context is done
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan protocol.Message, 1)
	go func() {
		done <- p.Execute(ctx, "test", protocol.Message{
			ID:     "req-cancel",
			Type:   protocol.TypeHTTPRequest,
			Method: "GET",
			Path:   "/blocked",
		})
	}()

	// Cancel the context after a short delay
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case resp := <-done:
		if resp.Error == nil {
			t.Fatal("expected error from cancelled request")
		}
		if resp.Error.Code != "request_cancelled" {
			t.Errorf("error code = %q, want request_cancelled", resp.Error.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return after context cancellation")
	}
}

// --- Retry tests ---

func retryProxy(client *http.Client, maxRetries int, retryOn []int) *Proxy {
	p := newTestProxy(client)
	p.SetRetryConfig(config.RetryConfig{
		Max:     maxRetries,
		Backoff: "exponential",
		RetryOn: retryOn,
	})
	p.sleepFn = func(_ context.Context, _ time.Duration) error { return nil }
	return p
}

func TestRetrySucceedsOnSecondAttempt(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck,gosec
	}))
	defer srv.Close()

	p := retryProxy(srv.Client(), 3, []int{503})
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:   "r1",
		Type: protocol.TypeHTTPRequest,
		Path: "/api/test",
	})

	if resp.Status != 200 {
		t.Errorf("expected 200, got %d", resp.Status)
	}
	if attempts.Load() != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts.Load())
	}
}

func TestRetryExhausted(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(503)
	}))
	defer srv.Close()

	p := retryProxy(srv.Client(), 2, []int{503})
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:   "r2",
		Type: protocol.TypeHTTPRequest,
		Path: "/api/test",
	})

	if resp.Status != 503 {
		t.Errorf("expected 503, got %d", resp.Status)
	}
	if attempts.Load() != 3 {
		t.Errorf("expected 3 attempts (1 + 2 retries), got %d", attempts.Load())
	}
}

func TestRetryNotAppliedToPOST(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(503)
	}))
	defer srv.Close()

	p := retryProxy(srv.Client(), 3, []int{503})
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "r3",
		Type:   protocol.TypeHTTPRequest,
		Method: "POST",
		Path:   "/api/test",
	})

	if resp.Status != 503 {
		t.Errorf("expected 503, got %d", resp.Status)
	}
	if attempts.Load() != 1 {
		t.Errorf("POST should not be retried, got %d attempts", attempts.Load())
	}
}

func TestRetryContextCancellation(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()

	p := retryProxy(srv.Client(), 3, []int{503})
	p.sleepFn = func(_ context.Context, _ time.Duration) error {
		return context.Canceled
	}
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:   "r4",
		Type: protocol.TypeHTTPRequest,
		Path: "/api/test",
	})

	if resp.Error == nil || resp.Error.Code != "request_cancelled" {
		t.Errorf("expected request_cancelled, got %+v", resp.Error)
	}
}

func TestRetryAfterHeaderRespected(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(503)
	}))
	defer srv.Close()

	var recordedDelay time.Duration
	p := retryProxy(srv.Client(), 1, []int{503})
	p.sleepFn = func(_ context.Context, d time.Duration) error {
		recordedDelay = d
		return nil
	}
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	p.Execute(context.Background(), "test", protocol.Message{
		ID:   "r5",
		Type: protocol.TypeHTTPRequest,
		Path: "/api/test",
	})

	if recordedDelay != 5*time.Second {
		t.Errorf("expected 5s delay from Retry-After, got %s", recordedDelay)
	}
}

func TestRetryNonRetryableStatus(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(400)
	}))
	defer srv.Close()

	p := retryProxy(srv.Client(), 3, []int{500, 502, 503, 504})
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:   "r6",
		Type: protocol.TypeHTTPRequest,
		Path: "/api/test",
	})

	if resp.Status != 400 {
		t.Errorf("expected 400, got %d", resp.Status)
	}
	if attempts.Load() != 1 {
		t.Errorf("400 should not be retried, got %d attempts", attempts.Load())
	}
}

func TestRetryDisabled(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(503)
	}))
	defer srv.Close()

	p := retryProxy(srv.Client(), 0, []int{503})
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:   "r7",
		Type: protocol.TypeHTTPRequest,
		Path: "/api/test",
	})

	if resp.Status != 503 {
		t.Errorf("expected 503, got %d", resp.Status)
	}
	if attempts.Load() != 1 {
		t.Errorf("max=0 should mean no retries, got %d attempts", attempts.Load())
	}
}

func TestRetryBodyReplayedCorrectly(t *testing.T) {
	var attempts atomic.Int32
	var mu sync.Mutex
	var lastBody string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		lastBody = string(b)
		mu.Unlock()
		if attempts.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck,gosec
	}))
	defer srv.Close()

	p := retryProxy(srv.Client(), 3, []int{503})
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	body, _ := json.Marshal(map[string]string{"key": "value"})
	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "r8",
		Type:   protocol.TypeHTTPRequest,
		Method: "PUT",
		Path:   "/api/test",
		Body:   body,
	})

	if resp.Status != 200 {
		t.Errorf("expected 200, got %d", resp.Status)
	}
	if attempts.Load() != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts.Load())
	}
	mu.Lock()
	gotBody := lastBody
	mu.Unlock()
	if !strings.Contains(gotBody, `"key":"value"`) && !strings.Contains(gotBody, `"key": "value"`) {
		t.Errorf("body not replayed on retry, got: %s", gotBody)
	}
}

func TestRetryDelay(t *testing.T) {
	tests := []struct {
		name       string
		attempt    int
		retryAfter string
		minDelay   time.Duration
		maxDelay   time.Duration
	}{
		{"attempt 1 backoff", 1, "", 750 * time.Millisecond, 1250 * time.Millisecond},
		{"attempt 2 backoff", 2, "", 1500 * time.Millisecond, 2500 * time.Millisecond},
		{"attempt 5 shift capped", 5, "", 12 * time.Second, 20 * time.Second},
		{"attempt 10 same as 5", 10, "", 12 * time.Second, 20 * time.Second},
		{"retry-after 5", 1, "5", 5 * time.Second, 5 * time.Second},
		{"retry-after 0 falls to backoff", 1, "0", 750 * time.Millisecond, 1250 * time.Millisecond},
		{"retry-after negative falls to backoff", 1, "-5", 750 * time.Millisecond, 1250 * time.Millisecond},
		{"retry-after 999 capped", 1, "999", 30 * time.Second, 30 * time.Second},
		{"retry-after invalid falls to backoff", 1, "not-a-number", 750 * time.Millisecond, 1250 * time.Millisecond},
		{"retry-after http-date future capped", 1, time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat), 29 * time.Second, 30 * time.Second},
		{"retry-after http-date past floors to 1s", 1, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Format(http.TimeFormat), 1 * time.Second, 1 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := retryDelay(tt.attempt, tt.retryAfter)
			if d < tt.minDelay || d > tt.maxDelay {
				t.Errorf("retryDelay(%d, %q) = %s, want [%s, %s]",
					tt.attempt, tt.retryAfter, d, tt.minDelay, tt.maxDelay)
			}
		})
	}
}

func TestConcurrentRegisterAndExecute(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())

	var wg sync.WaitGroup
	var noConfig atomic.Int64

	for i := range 20 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			name := "plugin"
			if n%2 == 0 {
				p.RegisterPlugin(name, testPluginAuth(srv.URL))
			} else {
				resp := p.Execute(context.Background(), name, protocol.Message{
					ID:     "req",
					Type:   protocol.TypeHTTPRequest,
					Method: "GET",
					Path:   "/test",
				})
				if resp.Error != nil && resp.Error.Code == "no_config" {
					noConfig.Add(1)
				}
			}
		}(i)
	}
	wg.Wait()

	t.Logf("no_config errors (expected some): %d", noConfig.Load())
}

func TestConcurrentUnregisterAndExecute(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	var wg sync.WaitGroup
	var ops atomic.Int64

	for range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			p.UnregisterPlugin("test")
			p.RegisterPlugin("test", testPluginAuth(srv.URL))
			ops.Add(1)
		}()
		go func() {
			defer wg.Done()
			p.Execute(context.Background(), "test", protocol.Message{
				ID:     "req",
				Type:   protocol.TypeHTTPRequest,
				Method: "GET",
				Path:   "/test",
			})
			ops.Add(1)
		}()
	}
	wg.Wait()

	if ops.Load() != 40 {
		t.Errorf("expected 40 operations, got %d", ops.Load())
	}
}

func TestAddAllowedDomainsDeduplicate(t *testing.T) {
	p := newTestProxy(nil)
	p.RegisterPlugin("test", &PluginAuth{AllowedDomains: []string{"example.com"}})

	p.AddAllowedDomains("test", []string{"example.com", "new.com"})

	p.pluginsMu.RLock()
	pa := p.plugins["test"]
	p.pluginsMu.RUnlock()

	if len(pa.AllowedDomains) != 2 {
		t.Errorf("expected 2 domains (deduped), got %d: %v", len(pa.AllowedDomains), pa.AllowedDomains)
	}
}

func TestAddAllowedDomainsCaseInsensitive(t *testing.T) {
	p := newTestProxy(nil)
	p.RegisterPlugin("test", &PluginAuth{AllowedDomains: []string{"example.com"}})

	p.AddAllowedDomains("test", []string{"Example.COM"})

	p.pluginsMu.RLock()
	pa := p.plugins["test"]
	p.pluginsMu.RUnlock()

	if len(pa.AllowedDomains) != 1 {
		t.Errorf("case-insensitive dedup failed, got %d: %v", len(pa.AllowedDomains), pa.AllowedDomains)
	}
}

func TestAddAllowedDomainsNewDomain(t *testing.T) {
	p := newTestProxy(nil)
	p.RegisterPlugin("test", &PluginAuth{AllowedDomains: []string{"a.com"}})

	p.AddAllowedDomains("test", []string{"b.com"})

	p.pluginsMu.RLock()
	pa := p.plugins["test"]
	p.pluginsMu.RUnlock()

	if len(pa.AllowedDomains) != 2 {
		t.Errorf("expected 2 domains, got %d: %v", len(pa.AllowedDomains), pa.AllowedDomains)
	}
}

func TestExecuteWithDynamicDomains(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", &PluginAuth{})

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:      "dyn-1",
		Type:    protocol.TypeHTTPRequest,
		Method:  "GET",
		URL:     srv.URL + "/api/test",
		Domains: []string{mustHostname(srv.URL)},
	})

	if resp.Status != 200 {
		t.Errorf("status = %d, want 200 (error = %v)", resp.Status, resp.Error)
	}

	p.pluginsMu.RLock()
	pa := p.plugins["test"]
	domains := pa.AllowedDomains
	p.pluginsMu.RUnlock()

	if !containsDomain(domains, mustHostname(srv.URL)) {
		t.Errorf("expected domain %q in AllowedDomains %v", mustHostname(srv.URL), domains)
	}
}

func mustHostname(rawURL string) string {
	u, _ := url.Parse(rawURL)
	return u.Hostname()
}
