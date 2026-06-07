package proxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeGambiArt/wtmcp/internal/audit"
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

func TestCheckIPCGNAT(t *testing.T) {
	blocked := []string{
		"100.64.0.1",      // first usable in CGNAT range
		"100.127.255.254", // last usable in CGNAT range
		"100.100.100.100", // common cloud metadata
	}
	for _, ip := range blocked {
		if err := checkIP(ip); err == nil {
			t.Errorf("checkIP(%q) = nil, want CGNAT blocked", ip)
		}
	}

	allowed := []string{
		"100.63.255.255", // just below CGNAT range
		"100.128.0.0",    // just above CGNAT range
	}
	for _, ip := range allowed {
		if err := checkIP(ip); err != nil {
			t.Errorf("checkIP(%q) = %v, want nil", ip, err)
		}
	}
}

func TestCheckIPBenchmark(t *testing.T) {
	blocked := []string{
		"198.18.0.1",     // first usable in benchmark range
		"198.19.255.254", // last usable in benchmark range
	}
	for _, ip := range blocked {
		if err := checkIP(ip); err == nil {
			t.Errorf("checkIP(%q) = nil, want benchmark blocked", ip)
		}
	}

	allowed := []string{
		"198.17.255.255", // just below benchmark range
		"198.20.0.0",     // just above benchmark range
	}
	for _, ip := range allowed {
		if err := checkIP(ip); err != nil {
			t.Errorf("checkIP(%q) = %v, want nil", ip, err)
		}
	}
}

func TestCheckIPv6MappedCGNAT(t *testing.T) {
	blocked := []string{
		"::ffff:100.64.0.1",
		"::ffff:198.18.0.1",
	}
	for _, ip := range blocked {
		if err := checkIP(ip); err == nil {
			t.Errorf("checkIP(%q) = nil, want reserved blocked", ip)
		}
	}
}

func TestCheckMetadataIPBlocked(t *testing.T) {
	blocked := []string{
		"169.254.169.254",        // AWS/GCP/Azure IMDS
		"fd00:ec2::254",          // AWS IMDSv2 IPv6
		"fd20:ce::254",           // GCP IMDS IPv6
		"168.63.129.16",          // Azure Wire Server
		"100.100.100.200",        // Alibaba Cloud ECS metadata
		"::ffff:169.254.169.254", // IPv6-mapped IMDS
		"::ffff:168.63.129.16",   // IPv6-mapped Azure Wire Server
		"::ffff:100.100.100.200", // IPv6-mapped Alibaba
	}
	for _, ip := range blocked {
		if err := checkMetadataIP(ip); err == nil {
			t.Errorf("checkMetadataIP(%q) = nil, want blocked", ip)
		}
	}
}

func TestCheckMetadataIPAllowed(t *testing.T) {
	allowed := []string{
		"10.0.0.1",        // RFC 1918 — not metadata
		"192.168.1.1",     // RFC 1918 — not metadata
		"127.0.0.1",       // loopback — not metadata
		"8.8.8.8",         // public DNS
		"169.254.1.1",     // link-local but not metadata
		"100.100.100.100", // CGNAT — not metadata
	}
	for _, ip := range allowed {
		if err := checkMetadataIP(ip); err != nil {
			t.Errorf("checkMetadataIP(%q) = %v, want allowed", ip, err)
		}
	}
}

func TestSafeDialerBlocksMetadataWithAllowPrivate(t *testing.T) {
	d := &safeDialer{
		dialer:       net.Dialer{Timeout: 1 * time.Second},
		allowPrivate: true,
	}
	_, err := d.DialContext(context.Background(), "tcp", "169.254.169.254:80")
	if err == nil || !strings.Contains(err.Error(), "SSRF blocked") {
		t.Errorf("metadata IP should be blocked even with allowPrivate, got: %v", err)
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

	t.Run("cross domain strips auth and custom headers", func(t *testing.T) {
		via := []*http.Request{{URL: mustParse("https://a.example.com/path")}}
		req := &http.Request{
			URL: mustParse("https://b.example.com/other"),
			Header: http.Header{
				"Authorization":   {"Bearer tok"},
				"Cookie":          {"session=abc"},
				"Private-Token":   {"glpat-xxx"},
				"X-Api-Key":       {"key-123"},
				"X-Internal-Auth": {"custom-secret"},
				"X-Request-Id":    {"trace-123"},
			},
		}
		if err := StripAuthOnCrossDomainRedirect(req, via); err != nil {
			t.Fatal(err)
		}
		stripped := []string{
			"Authorization", "Cookie", "Private-Token",
			"X-Api-Key", "X-Internal-Auth", "X-Request-Id",
		}
		for _, h := range stripped {
			if req.Header.Get(h) != "" {
				t.Errorf("header %s should be stripped on cross-domain redirect", h)
			}
		}
	})

	t.Run("cross domain preserves safe headers", func(t *testing.T) {
		via := []*http.Request{{URL: mustParse("https://a.example.com/path")}}
		req := &http.Request{
			URL: mustParse("https://b.example.com/other"),
			Header: http.Header{
				"Accept":          {"application/json"},
				"Content-Type":    {"application/json"},
				"User-Agent":      {"wtmcp/1.0"},
				"Range":           {"bytes=0-100"},
				"If-None-Match":   {`"etag123"`},
				"Authorization":   {"Bearer tok"},
				"X-Custom-Secret": {"should-be-stripped"},
			},
		}
		if err := StripAuthOnCrossDomainRedirect(req, via); err != nil {
			t.Fatal(err)
		}
		preserved := []string{"Accept", "User-Agent", "Range", "If-None-Match"}
		for _, h := range preserved {
			if req.Header.Get(h) == "" {
				t.Errorf("safe header %s should be preserved on cross-domain redirect", h)
			}
		}
		if req.Header.Get("Authorization") != "" {
			t.Error("Authorization should be stripped")
		}
		if req.Header.Get("X-Custom-Secret") != "" {
			t.Error("custom header should be stripped")
		}
	})

	t.Run("same domain preserves all headers", func(t *testing.T) {
		via := []*http.Request{{URL: mustParse("https://api.example.com/a")}}
		req := &http.Request{
			URL: mustParse("https://api.example.com/b"),
			Header: http.Header{
				"Authorization":   {"Bearer tok"},
				"X-Internal-Auth": {"custom-secret"},
				"X-Request-Id":    {"trace-123"},
			},
		}
		if err := StripAuthOnCrossDomainRedirect(req, via); err != nil {
			t.Fatal(err)
		}
		for _, h := range []string{"Authorization", "X-Internal-Auth", "X-Request-Id"} {
			if req.Header.Get(h) == "" {
				t.Errorf("header %s should be preserved on same-domain redirect", h)
			}
		}
	})

	t.Run("case insensitive domain comparison", func(t *testing.T) {
		via := []*http.Request{{URL: mustParse("https://API.EXAMPLE.COM/a")}}
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
			t.Error("same domain (case-insensitive) should preserve headers")
		}
	})

	t.Run("cross domain strips request body", func(t *testing.T) {
		via := []*http.Request{{URL: mustParse("https://api.example.com/action")}}
		req := &http.Request{
			URL:           mustParse("https://evil.com/capture"),
			Method:        "POST",
			Body:          io.NopCloser(strings.NewReader(`{"api_key":"secret"}`)),
			ContentLength: 20,
			Header: http.Header{
				"Content-Type":   {"application/json"},
				"Content-Length": {"20"},
			},
		}
		if err := StripAuthOnCrossDomainRedirect(req, via); err != nil {
			t.Fatal(err)
		}
		if req.Body != http.NoBody {
			t.Error("request body should be stripped on cross-domain redirect")
		}
		if req.ContentLength != 0 {
			t.Errorf("ContentLength should be 0, got %d", req.ContentLength)
		}
		if req.Header.Get("Content-Type") != "" {
			t.Error("Content-Type should be stripped with body")
		}
	})

	t.Run("same domain preserves request body", func(t *testing.T) {
		via := []*http.Request{{URL: mustParse("https://api.example.com/a")}}
		req := &http.Request{
			URL:           mustParse("https://api.example.com/b"),
			Method:        "POST",
			Body:          io.NopCloser(strings.NewReader(`{"data":"value"}`)),
			ContentLength: 16,
			Header: http.Header{
				"Content-Type": {"application/json"},
			},
		}
		if err := StripAuthOnCrossDomainRedirect(req, via); err != nil {
			t.Fatal(err)
		}
		if req.Body == http.NoBody {
			t.Error("request body should be preserved on same-domain redirect")
		}
		if req.ContentLength != 16 {
			t.Errorf("ContentLength should be preserved, got %d", req.ContentLength)
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

	t.Run("both hosts fail normalization strips headers", func(t *testing.T) {
		// Labels > 63 chars fail IDNA normalization
		longLabel := strings.Repeat("a", 64)
		via := []*http.Request{{URL: mustParse("https://" + longLabel + ".example.com/a")}}
		req := &http.Request{
			URL: mustParse("https://" + longLabel + ".other.com/b"),
			Header: http.Header{
				"Authorization": {"Bearer tok"},
				"Cookie":        {"session=abc"},
			},
		}
		if err := StripAuthOnCrossDomainRedirect(req, via); err != nil {
			t.Fatal(err)
		}
		if req.Header.Get("Authorization") != "" {
			t.Error("Authorization should be stripped when both hosts fail normalization")
		}
		if req.Header.Get("Cookie") != "" {
			t.Error("Cookie should be stripped when both hosts fail normalization")
		}
	})

	t.Run("one host fails normalization strips headers", func(t *testing.T) {
		longLabel := strings.Repeat("b", 64)
		via := []*http.Request{{URL: mustParse("https://valid.example.com/a")}}
		req := &http.Request{
			URL: mustParse("https://" + longLabel + ".evil.com/b"),
			Header: http.Header{
				"Authorization": {"Bearer tok"},
			},
		}
		if err := StripAuthOnCrossDomainRedirect(req, via); err != nil {
			t.Fatal(err)
		}
		if req.Header.Get("Authorization") != "" {
			t.Error("Authorization should be stripped when redirect host fails normalization")
		}
	})
}

func TestSelectClient(t *testing.T) {
	defaultClient := &http.Client{}
	privateClient := &http.Client{}
	kerberosClient := &http.Client{}
	tlsClient := &http.Client{}

	p := &Proxy{
		client:        defaultClient,
		privateClient: privateClient,
	}

	tests := []struct {
		name   string
		pa     *PluginAuth
		noAuth bool
		want   *http.Client
	}{
		{
			name:   "noAuth Kerberos with TLSClient returns TLSClient",
			pa:     &PluginAuth{Client: kerberosClient, IsKerberos: true, TLSClient: tlsClient},
			noAuth: true,
			want:   tlsClient,
		},
		{
			name:   "noAuth non-Kerberos TLS returns TLSClient",
			pa:     &PluginAuth{TLSClient: tlsClient},
			noAuth: true,
			want:   tlsClient,
		},
		{
			name:   "noAuth AllowPrivateIPs returns privateClient",
			pa:     &PluginAuth{AllowPrivateIPs: true},
			noAuth: true,
			want:   privateClient,
		},
		{
			name:   "noAuth default returns p.client",
			pa:     &PluginAuth{},
			noAuth: true,
			want:   defaultClient,
		},
		{
			name:   "auth Kerberos returns pa.Client",
			pa:     &PluginAuth{Client: kerberosClient, IsKerberos: true},
			noAuth: false,
			want:   kerberosClient,
		},
		{
			name:   "auth TLSClient (non-Kerberos TLS) returns TLSClient",
			pa:     &PluginAuth{TLSClient: tlsClient},
			noAuth: false,
			want:   tlsClient,
		},
		{
			name:   "auth AllowPrivateIPs returns privateClient",
			pa:     &PluginAuth{AllowPrivateIPs: true},
			noAuth: false,
			want:   privateClient,
		},
		{
			name:   "auth default returns p.client",
			pa:     &PluginAuth{},
			noAuth: false,
			want:   defaultClient,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.selectClient(tt.pa, tt.noAuth)
			if got != tt.want {
				t.Errorf("selectClient() returned wrong client")
			}
		})
	}
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

func TestNoAuthBlocksHTTPWhenAuthConfigured(t *testing.T) {
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

	if resp.Error == nil || !strings.Contains(resp.Error.Message, "HTTPS required") {
		t.Errorf("no_auth with auth configured should still require HTTPS, got %v", resp.Error)
	}
}

func TestNoAuthAllowsHTTPSWhenAuthConfigured(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("no_auth request should not have Authorization header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	pa := testPluginAuth(srv.URL)
	pa.Provider, _ = auth.NewBearerProvider("token", "", "")
	p.RegisterPlugin("test", pa)

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-https-noauth",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		Path:   "/public",
		NoAuth: true,
	})

	if resp.Error != nil {
		t.Errorf("no_auth over HTTPS should succeed, got error: %v", resp.Error)
	}
}

func TestNoAuthWithoutProviderAllowsHTTP(t *testing.T) {
	p := newTestProxy(nil)
	pa := testPluginAuth("http://api.example.com")
	p.RegisterPlugin("test", pa)

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-http-noprovider",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		URL:    "http://api.example.com/public",
	})

	// No provider configured — HTTP should be allowed (transport error, not HTTPS error)
	if resp.Error != nil && strings.Contains(resp.Error.Message, "HTTPS required") {
		t.Error("no auth provider should allow HTTP")
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

func TestHTTPSRequiredWithKerberos(t *testing.T) {
	p := newTestProxy(nil)
	pa := testPluginAuth("https://internal.corp.com")
	pa.IsKerberos = true
	pa.Client = &http.Client{}
	p.RegisterPlugin("test", pa)

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:     "req-krb-http",
		Type:   protocol.TypeHTTPRequest,
		Method: "GET",
		URL:    "http://internal.corp.com/api",
	})

	if resp.Error == nil || !strings.Contains(resp.Error.Message, "HTTPS required") {
		t.Errorf("Kerberos plugin should require HTTPS, got %v", resp.Error)
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
	// Pre-register test server's domain (which is an IP address) via init-time path
	// to allow the test to work. Also add example.com as a base domain to allow
	// testing subdomain validation.
	srvHostname := mustHostname(srv.URL)
	p.RegisterPlugin("test", &PluginAuth{
		BaseURL:        srv.URL,
		AllowedDomains: []string{srvHostname, "example.com"},
	})

	// Attempt to add a valid subdomain via per-request path
	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:      "dyn-1",
		Type:    protocol.TypeHTTPRequest,
		Method:  "GET",
		URL:     srv.URL + "/api/test",
		Domains: []string{"api.example.com"},
	})

	if resp.Status != 200 {
		t.Errorf("status = %d, want 200 (error = %v)", resp.Status, resp.Error)
	}

	p.pluginsMu.RLock()
	pa := p.plugins["test"]
	domains := pa.AllowedDomains
	p.pluginsMu.RUnlock()

	// Verify the valid domain was added
	if !containsDomain(domains, "api.example.com") {
		t.Errorf("expected domain %q in AllowedDomains %v", "api.example.com", domains)
	}

	// Now verify that an IP address is rejected via per-request path
	resp = p.Execute(context.Background(), "test", protocol.Message{
		ID:      "dyn-2",
		Type:    protocol.TypeHTTPRequest,
		Method:  "GET",
		URL:     srv.URL + "/api/test2",
		Domains: []string{"192.168.1.1"},
	})

	// Request should still succeed (using pre-allowed server domain)
	if resp.Status != 200 {
		t.Errorf("status = %d, want 200 (error = %v)", resp.Status, resp.Error)
	}

	p.pluginsMu.RLock()
	domains = p.plugins["test"].AllowedDomains
	p.pluginsMu.RUnlock()

	// Verify the IP was rejected and NOT added
	if containsDomain(domains, "192.168.1.1") {
		t.Errorf("IP address 192.168.1.1 should have been rejected, but found in AllowedDomains %v", domains)
	}
}

func mustHostname(rawURL string) string {
	u, _ := url.Parse(rawURL)
	return u.Hostname()
}

// TestRequestDomainValidation verifies that per-request domains are validated
// to prevent security vulnerabilities like credential exfiltration to attacker-
// controlled servers (localhost, IP addresses, wildcards are all rejected).
func TestRequestDomainValidation(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	// Register plugin with test server's domain pre-allowed so requests can succeed
	// Also set example.com as a base domain to allow testing subdomain validation
	srvHostname := mustHostname(srv.URL)
	p.RegisterPlugin("test", &PluginAuth{
		BaseURL:        srv.URL,
		AllowedDomains: []string{srvHostname, "example.com"},
	})

	tests := []struct {
		name         string
		domains      []string
		wantRejected []string
		wantAccepted []string
		description  string
	}{
		{
			name:         "reject localhost",
			domains:      []string{"localhost", "example.com"},
			wantRejected: []string{"localhost"},
			wantAccepted: []string{"example.com"},
			description:  "localhost should be rejected to prevent local credential theft",
		},
		{
			name:         "reject IPv4",
			domains:      []string{"192.168.1.1", "example.com"},
			wantRejected: []string{"192.168.1.1"},
			wantAccepted: []string{"example.com"},
			description:  "IPv4 addresses should be rejected",
		},
		{
			name:         "reject IPv6",
			domains:      []string{"::1", "2001:db8::1", "example.com"},
			wantRejected: []string{"::1", "2001:db8::1"},
			wantAccepted: []string{"example.com"},
			description:  "IPv6 addresses should be rejected",
		},
		{
			name:         "reject IPv6 brackets",
			domains:      []string{"[::1]", "example.com"},
			wantRejected: []string{"[::1]"},
			wantAccepted: []string{"example.com"},
			description:  "IPv6 addresses in brackets should be rejected",
		},
		{
			name:         "reject wildcards",
			domains:      []string{"*.example.com", "example.com"},
			wantRejected: []string{"*.example.com"},
			wantAccepted: []string{"example.com"},
			description:  "wildcard domains should be rejected",
		},
		{
			name:         "reject empty",
			domains:      []string{"", "example.com"},
			wantRejected: []string{""},
			wantAccepted: []string{"example.com"},
			description:  "empty domains should be rejected",
		},
		{
			name:         "enforce 10 domain cap",
			domains:      []string{"d1.example.com", "d2.example.com", "d3.example.com", "d4.example.com", "d5.example.com", "d6.example.com", "d7.example.com", "d8.example.com", "d9.example.com", "d10.example.com", "d11.example.com", "d12.example.com"},
			wantRejected: []string{"d11.example.com", "d12.example.com"},
			wantAccepted: []string{"d1.example.com", "d2.example.com", "d3.example.com", "d4.example.com", "d5.example.com", "d6.example.com", "d7.example.com", "d8.example.com", "d9.example.com", "d10.example.com"},
			description:  "should cap at 10 domains per request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset AllowedDomains and BaseDomains for each test
			p.pluginsMu.Lock()
			p.plugins["test"].AllowedDomains = []string{srvHostname, "example.com"}
			p.plugins["test"].BaseDomains = []string{"example.com"}
			p.pluginsMu.Unlock()

			// Make request with domains
			resp := p.Execute(context.Background(), "test", protocol.Message{
				ID:      "test-1",
				Type:    protocol.TypeHTTPRequest,
				Method:  "GET",
				URL:     srv.URL + "/api/test",
				Domains: tt.domains,
			})

			// Request should succeed even if some domains are rejected
			if resp.Status != 200 && len(tt.wantAccepted) > 0 {
				t.Errorf("status = %d, want 200 (error = %v)", resp.Status, resp.Error)
			}

			// Check which domains were added
			p.pluginsMu.RLock()
			allowed := p.plugins["test"].AllowedDomains
			p.pluginsMu.RUnlock()

			// Verify rejected domains are not in allowlist
			for _, rejected := range tt.wantRejected {
				if containsDomain(allowed, rejected) {
					t.Errorf("%s: rejected domain %q should not be in AllowedDomains %v", tt.description, rejected, allowed)
				}
			}

			// Verify accepted domains are in allowlist
			for _, accepted := range tt.wantAccepted {
				if !containsDomain(allowed, accepted) {
					t.Errorf("%s: accepted domain %q should be in AllowedDomains %v", tt.description, accepted, allowed)
				}
			}
		})
	}
}

// TestSubdomainValidationInExecute verifies that Execute() rejects per-request
// domains that are not subdomains of base domains, preventing credential theft.
func TestSubdomainValidationInExecute(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	srvHostname := mustHostname(srv.URL)

	// Register plugin with jenkins.example.com as a base domain
	p.RegisterPlugin("test", &PluginAuth{
		BaseURL:        srv.URL,
		AllowedDomains: []string{srvHostname, "jenkins.example.com"},
	})

	tests := []struct {
		name         string
		domains      []string
		wantAccepted []string
		wantRejected []string
	}{
		{
			name:         "accept exact match",
			domains:      []string{"jenkins.example.com"},
			wantAccepted: []string{"jenkins.example.com"},
			wantRejected: []string{},
		},
		{
			name:         "accept subdomain",
			domains:      []string{"api.jenkins.example.com"},
			wantAccepted: []string{"api.jenkins.example.com"},
			wantRejected: []string{},
		},
		{
			name:         "reject attacker domain",
			domains:      []string{"attacker.com"},
			wantAccepted: []string{},
			wantRejected: []string{"attacker.com"},
		},
		{
			name:         "reject prefix that looks like subdomain",
			domains:      []string{"eviljenkins.example.com"},
			wantAccepted: []string{},
			wantRejected: []string{"eviljenkins.example.com"},
		},
		{
			name:         "mixed valid and invalid",
			domains:      []string{"ci.jenkins.example.com", "evil.com", "api.jenkins.example.com"},
			wantAccepted: []string{"ci.jenkins.example.com", "api.jenkins.example.com"},
			wantRejected: []string{"evil.com"},
		},
		{
			name:         "reject when base domains empty",
			domains:      []string{"any.domain.com"},
			wantAccepted: []string{},
			wantRejected: []string{"any.domain.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset AllowedDomains to base domains only
			p.pluginsMu.Lock()
			p.plugins["test"].AllowedDomains = []string{srvHostname, "jenkins.example.com"}
			// For the "empty base domains" test, clear BaseDomains
			if tt.name == "reject when base domains empty" {
				p.plugins["test"].BaseDomains = []string{}
			} else {
				p.plugins["test"].BaseDomains = []string{"jenkins.example.com"}
			}
			p.pluginsMu.Unlock()

			// Make request with domains
			resp := p.Execute(context.Background(), "test", protocol.Message{
				ID:      "test-" + tt.name,
				Type:    protocol.TypeHTTPRequest,
				Method:  "GET",
				URL:     srv.URL + "/api/test",
				Domains: tt.domains,
			})

			// Request should succeed using pre-allowed server hostname
			if resp.Status != 200 {
				t.Errorf("status = %d, want 200 (error = %v)", resp.Status, resp.Error)
			}

			// Check which domains were added
			p.pluginsMu.RLock()
			allowed := p.plugins["test"].AllowedDomains
			p.pluginsMu.RUnlock()

			// Verify accepted domains are in allowlist
			for _, accepted := range tt.wantAccepted {
				if !containsDomain(allowed, accepted) {
					t.Errorf("accepted domain %q should be in AllowedDomains %v", accepted, allowed)
				}
			}

			// Verify rejected domains are NOT in allowlist
			for _, rejected := range tt.wantRejected {
				if containsDomain(allowed, rejected) {
					t.Errorf("rejected domain %q should NOT be in AllowedDomains %v", rejected, allowed)
				}
			}
		})
	}
}

// TestCumulativeDomainCap verifies that the cumulative domain cap
// prevents unlimited domain accumulation across multiple requests.
func TestCumulativeDomainCap(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	srvHostname := mustHostname(srv.URL)

	// Register plugin with example.com as base domain
	p.RegisterPlugin("test", &PluginAuth{
		BaseURL:        srv.URL,
		AllowedDomains: []string{srvHostname, "example.com"},
	})

	// The plugin starts with 2 domains (srvHostname + example.com)
	// maxTotalDomains is 50, so we can add 48 more

	// Add domains in batches to approach the cap
	for batch := 0; batch < 5; batch++ {
		domains := make([]string, 10)
		for i := 0; i < 10; i++ {
			domains[i] = fmt.Sprintf("batch%d-api%d.example.com", batch, i)
		}

		resp := p.Execute(context.Background(), "test", protocol.Message{
			ID:      fmt.Sprintf("batch-%d", batch),
			Type:    protocol.TypeHTTPRequest,
			Method:  "GET",
			URL:     srv.URL + "/api/test",
			Domains: domains,
		})

		if resp.Status != 200 {
			t.Errorf("batch %d: status = %d, want 200 (error = %v)", batch, resp.Status, resp.Error)
		}
	}

	// Check how many domains were actually added
	p.pluginsMu.RLock()
	totalDomains := len(p.plugins["test"].AllowedDomains)
	p.pluginsMu.RUnlock()

	// Should be capped at maxTotalDomains (50)
	if totalDomains > 50 {
		t.Errorf("total domains = %d, want <= 50 (cumulative cap not enforced)", totalDomains)
	}

	// Try to add more domains - should all be rejected
	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:      "overflow",
		Type:    protocol.TypeHTTPRequest,
		Method:  "GET",
		URL:     srv.URL + "/api/test",
		Domains: []string{"overflow1.example.com", "overflow2.example.com"},
	})

	if resp.Status != 200 {
		t.Errorf("overflow request: status = %d, want 200 (error = %v)", resp.Status, resp.Error)
	}

	// Verify overflow domains were NOT added
	p.pluginsMu.RLock()
	allowed := p.plugins["test"].AllowedDomains
	finalCount := len(allowed)
	p.pluginsMu.RUnlock()

	if finalCount > 50 {
		t.Errorf("final domain count = %d, want <= 50 (cap should prevent overflow)", finalCount)
	}

	if containsDomain(allowed, "overflow1.example.com") || containsDomain(allowed, "overflow2.example.com") {
		t.Errorf("overflow domains should be rejected but found in allowlist: %v", allowed)
	}
}

func TestExecuteReadOnlyToolBlocksMutatingMethods(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("request should never reach the server for read-only tool with mutating method")
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		t.Run(method, func(t *testing.T) {
			ctx := WithToolAccess(context.Background(), "read")
			resp := p.Execute(ctx, "test", protocol.Message{
				ID: "ro-" + method, Type: protocol.TypeHTTPRequest,
				Method: method, Path: "/api/modify",
			})
			if resp.Error == nil || resp.Error.Code != "method_not_allowed" {
				t.Errorf("read-only tool %s should be blocked, got error=%v", method, resp.Error)
			}
		})
	}
}

func TestExecuteReadOnlyToolAllowsReadMethods(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	for _, method := range []string{"GET", "HEAD", "OPTIONS"} {
		t.Run(method, func(t *testing.T) {
			ctx := WithToolAccess(context.Background(), "read")
			resp := p.Execute(ctx, "test", protocol.Message{
				ID: "ro-" + method, Type: protocol.TypeHTTPRequest,
				Method: method, Path: "/api/read",
			})
			if resp.Error != nil {
				t.Errorf("read-only tool %s should be allowed, got error=%v", method, resp.Error)
			}
		})
	}
}

func TestExecuteWriteAccessAllowsMutatingMethods(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	ctx := WithToolAccess(context.Background(), "write")
	resp := p.Execute(ctx, "test", protocol.Message{
		ID: "wa-1", Type: protocol.TypeHTTPRequest,
		Method: "POST", Path: "/api/create",
	})
	if resp.Error != nil {
		t.Errorf("write-access tool POST should be allowed, got error=%v", resp.Error)
	}
}

func TestExecuteNoAccessAnnotationAllowsMutatingMethods(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	// No WithToolAccess — simulates a tool with no access annotation
	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID: "na-1", Type: protocol.TypeHTTPRequest,
		Method: "POST", Path: "/api/create",
	})
	if resp.Error != nil {
		t.Errorf("unannotated tool POST should be allowed, got error=%v", resp.Error)
	}
}

func TestScrubErrorFallbackWithoutAuditor(t *testing.T) {
	p := newTestProxy(nil)

	jwt := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature"
	got := p.scrubError("auth failed: token " + jwt + " is expired")
	if strings.Contains(got, "eyJhbGci") {
		t.Errorf("JWT should be scrubbed even without auditor, got: %s", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("scrubbed output should contain [REDACTED], got: %s", got)
	}
}

func TestScrubErrorWithAuditor(t *testing.T) {
	p := newTestProxy(nil)
	logFile := filepath.Join(t.TempDir(), "audit.log")
	auditor, err := audit.New(audit.Config{LogFile: logFile})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = auditor.Close() }()
	p.SetAuditor(auditor)

	jwt := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature"
	got := p.scrubError("auth failed: token " + jwt + " is expired")
	if strings.Contains(got, "eyJhbGci") {
		t.Errorf("JWT should be scrubbed with auditor, got: %s", got)
	}
}

func TestScrubErrorPassesThroughSafeMessages(t *testing.T) {
	p := newTestProxy(nil)

	safe := "connection refused: dial tcp 1.2.3.4:443"
	got := p.scrubError(safe)
	if got != safe {
		t.Errorf("safe message should pass through unchanged, got: %s", got)
	}
}

// --- Rate-limit wait tests ---

// mockRateLimiter returns pre-configured delays in sequence.
// After all delays are consumed, Allow() returns 0 (token acquired).
type mockRateLimiter struct {
	delays []time.Duration
	mu     sync.Mutex
	idx    int
}

func (m *mockRateLimiter) Allow(_ string) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.delays) {
		return 0
	}
	d := m.delays[m.idx]
	m.idx++
	return d
}

func TestRateLimitTokenAcquiredFirstTry(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck,gosec
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.rateLimiter = &mockRateLimiter{} // empty delays → immediate token
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	var sleepCount atomic.Int32
	p.sleepFn = func(_ context.Context, _ time.Duration) error {
		sleepCount.Add(1)
		return nil
	}

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:   "rl1",
		Type: protocol.TypeHTTPRequest,
		Path: "/api/test",
	})

	if resp.Status != 200 {
		t.Errorf("expected 200, got %d", resp.Status)
	}
	if sleepCount.Load() != 0 {
		t.Errorf("expected 0 sleeps, got %d", sleepCount.Load())
	}
}

func TestRateLimitTokenAcquiredAfterWait(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck,gosec
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.rateLimiter = &mockRateLimiter{
		delays: []time.Duration{500 * time.Millisecond}, // wait once, then token acquired
	}
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	var sleepCount atomic.Int32
	var sleepDuration atomic.Int64
	p.sleepFn = func(_ context.Context, d time.Duration) error {
		sleepCount.Add(1)
		sleepDuration.Store(int64(d))
		return nil
	}

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID:   "rl2",
		Type: protocol.TypeHTTPRequest,
		Path: "/api/test",
	})

	if resp.Status != 200 {
		t.Errorf("expected 200, got %d", resp.Status)
	}
	if sleepCount.Load() != 1 {
		t.Errorf("expected 1 sleep, got %d", sleepCount.Load())
	}
	if d := time.Duration(sleepDuration.Load()); d != 500*time.Millisecond {
		t.Errorf("expected 500ms sleep, got %s", d)
	}
}

func TestRateLimitContextCancelled(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.rateLimiter = &mockRateLimiter{
		delays: []time.Duration{time.Second, time.Second, time.Second},
	}
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	p.sleepFn = func(_ context.Context, _ time.Duration) error {
		return context.Canceled
	}

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID: "rl3", Type: protocol.TypeHTTPRequest, Path: "/api/test",
	})
	if resp.Error == nil || resp.Error.Code != "request_cancelled" {
		t.Errorf("expected request_cancelled error, got %+v", resp.Error)
	}
}

func TestRateLimitMaxRetriesExceeded(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	// 3 delays = 3 Allow() calls returning d>0. With maxRateLimitRetries=2,
	// the loop sleeps on attempts 0 and 1, then gives up on attempt 2.
	p := newTestProxy(srv.Client())
	p.rateLimiter = &mockRateLimiter{
		delays: []time.Duration{time.Second, time.Second, time.Second},
	}
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	var sleepCount atomic.Int32
	p.sleepFn = func(_ context.Context, _ time.Duration) error {
		sleepCount.Add(1)
		return nil
	}

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID: "rl4", Type: protocol.TypeHTTPRequest, Path: "/api/test",
	})
	if resp.Error == nil || resp.Error.Code != "rate_limited" {
		t.Errorf("expected rate_limited error, got %+v", resp.Error)
	}
	// maxRateLimitRetries=2: sleeps on attempts 0 and 1, fails on attempt 2
	if sleepCount.Load() != 2 {
		t.Errorf("expected exactly 2 sleeps (maxRateLimitRetries), got %d", sleepCount.Load())
	}
}

func TestRateLimitWaitExceedsMaxWait(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.rateLimiter = &mockRateLimiter{
		delays: []time.Duration{maxRateLimitWait + time.Millisecond},
	}
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	var sleepCount atomic.Int32
	p.sleepFn = func(_ context.Context, _ time.Duration) error {
		sleepCount.Add(1)
		return nil
	}

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID: "rl5", Type: protocol.TypeHTTPRequest, Path: "/api/test",
	})
	if resp.Error == nil || resp.Error.Code != "rate_limited" {
		t.Errorf("expected rate_limited error, got %+v", resp.Error)
	}
	if sleepCount.Load() != 0 {
		t.Errorf("expected 0 sleeps (duration exceeds maxRateLimitWait), got %d", sleepCount.Load())
	}
}

func TestRateLimitWaitAtExactBoundary(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck,gosec
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.rateLimiter = &mockRateLimiter{
		delays: []time.Duration{maxRateLimitWait}, // exactly at boundary → accepted
	}
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	p.sleepFn = func(_ context.Context, _ time.Duration) error { return nil }

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID: "rl6", Type: protocol.TypeHTTPRequest, Path: "/api/test",
	})
	if resp.Status != 200 {
		t.Errorf("expected 200 (wait at boundary accepted), got status %d, error %+v", resp.Status, resp.Error)
	}
}

func TestRateLimitWaitPlusFiveXXRetry(t *testing.T) {
	var httpAttempts atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if httpAttempts.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck,gosec
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client())
	p.rateLimiter = &mockRateLimiter{
		delays: []time.Duration{200 * time.Millisecond}, // one rate-limit wait
	}
	p.SetRetryConfig(config.RetryConfig{
		Max:     3,
		Backoff: "exponential",
		RetryOn: []int{503},
	})
	p.RegisterPlugin("test", testPluginAuth(srv.URL))

	var sleepCount atomic.Int32
	p.sleepFn = func(_ context.Context, _ time.Duration) error {
		sleepCount.Add(1)
		return nil
	}

	resp := p.Execute(context.Background(), "test", protocol.Message{
		ID: "rl7", Type: protocol.TypeHTTPRequest, Path: "/api/test",
	})
	if resp.Status != 200 {
		t.Errorf("expected 200, got %d", resp.Status)
	}
	if httpAttempts.Load() != 2 {
		t.Errorf("expected 2 HTTP attempts, got %d", httpAttempts.Load())
	}
	// 1 rate-limit wait sleep + 1 retry backoff sleep = 2 total
	if sleepCount.Load() != 2 {
		t.Errorf("expected 2 sleeps (rate-limit + retry backoff), got %d", sleepCount.Load())
	}
}

// ─── Fuzz Tests ──────────────────────────────────────────────────

func FuzzCheckIP(f *testing.F) {
	f.Add("127.0.0.1")
	f.Add("10.0.0.1")
	f.Add("172.16.0.1")
	f.Add("192.168.1.1")
	f.Add("100.64.0.1")
	f.Add("198.18.0.1")
	f.Add("169.254.1.1")
	f.Add("224.0.0.1")
	f.Add("8.8.8.8")
	f.Add("1.1.1.1")
	f.Add("::1")
	f.Add("::ffff:10.0.0.1")
	f.Add("::ffff:127.0.0.1")
	f.Add("2001::1")
	f.Add("2002::1")
	f.Add("fe80::1")
	f.Add("ff02::1")
	f.Add("")
	f.Add("not-an-ip")
	f.Add("999.999.999.999")

	f.Fuzz(func(t *testing.T, ipStr string) {
		err := checkIP(ipStr)
		ip := net.ParseIP(ipStr)
		if ip == nil {
			if err == nil {
				t.Fatalf("checkIP(%q) passed for unparseable IP", ipStr)
			}
			return
		}
		if err != nil {
			return
		}
		// If checkIP passed, verify the IP is actually public.
		ipv4 := ip.To4()
		check := ip
		if ipv4 != nil {
			check = ipv4
		}
		if check.IsLoopback() {
			t.Fatalf("checkIP(%q) passed for loopback", ipStr)
		}
		if check.IsPrivate() {
			t.Fatalf("checkIP(%q) passed for private", ipStr)
		}
		if check.IsLinkLocalUnicast() {
			t.Fatalf("checkIP(%q) passed for link-local", ipStr)
		}
		if check.IsMulticast() {
			t.Fatalf("checkIP(%q) passed for multicast", ipStr)
		}
		if check.IsUnspecified() {
			t.Fatalf("checkIP(%q) passed for unspecified", ipStr)
		}
	})
}

func FuzzCheckMetadataIP(f *testing.F) {
	f.Add("169.254.169.254")
	f.Add("fd00:ec2::254")
	f.Add("fe80::1")
	f.Add("::ffff:169.254.169.254")
	f.Add("100.100.100.200")
	f.Add("8.8.8.8")
	f.Add("1.1.1.1")
	f.Add("")
	f.Add("not-an-ip")

	f.Fuzz(func(t *testing.T, ipStr string) {
		err := checkMetadataIP(ipStr)
		ip := net.ParseIP(ipStr)
		if ip == nil {
			if err == nil {
				t.Fatalf("checkMetadataIP(%q) passed for unparseable IP", ipStr)
			}
			return
		}
		// If it passed, verify it's not a known metadata IP.
		if err == nil {
			for _, entry := range cloudMetadataNets {
				if entry.net.Contains(ip) {
					t.Fatalf("checkMetadataIP(%q) passed but matches metadata net %s",
						ipStr, entry.desc)
				}
				if ipv4 := ip.To4(); ipv4 != nil && len(ip) == net.IPv6len {
					if entry.net.Contains(ipv4) {
						t.Fatalf("checkMetadataIP(%q) passed but IPv4-mapped form matches %s",
							ipStr, entry.desc)
					}
				}
			}
		}
	})
}
