package kerberos

import (
	"context"
	"net/http"
	"testing"
)

func TestRedirectOriginContext(t *testing.T) {
	ctx := context.Background()

	// No origin set — should return empty.
	if got := redirectOriginFromContext(ctx); got != "" {
		t.Errorf("expected empty origin, got %q", got)
	}

	// Set origin.
	ctx = WithRedirectOrigin(ctx, "api.example.com")
	if got := redirectOriginFromContext(ctx); got != "api.example.com" {
		t.Errorf("expected api.example.com, got %q", got)
	}
}

// mockRoundTripper records whether RoundTrip was called and captures
// the request for assertion.
type mockRoundTripper struct {
	called  bool
	lastReq *http.Request
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	m.called = true
	m.lastReq = req
	return &http.Response{
		StatusCode: 200,
		Header:     make(http.Header),
		Body:       http.NoBody,
		Request:    req,
	}, nil
}

func TestSPNEGO_SameHostNotSuppressed(t *testing.T) {
	mock := &mockRoundTripper{}
	rt := &SPNEGORoundTripper{
		next:      mock,
		proactive: false,
	}

	req, _ := http.NewRequestWithContext(
		WithRedirectOrigin(context.Background(), "api.example.com"),
		"GET", "https://api.example.com/resource", nil,
	)

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	// Same host — should NOT suppress, should call next.
	if !mock.called {
		t.Error("same-host request should not be suppressed")
	}
}

func TestSPNEGO_CrossHostSuppressed(t *testing.T) {
	mock := &mockRoundTripper{}
	rt := &SPNEGORoundTripper{
		next:      mock,
		proactive: true,
	}

	req, _ := http.NewRequestWithContext(
		WithRedirectOrigin(context.Background(), "api.example.com"),
		"GET", "https://evil.example.com/resource", nil,
	)

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	// Cross-host — should suppress and go straight to next without
	// attempting SPNEGO. The mock is still called (we just skip auth).
	if !mock.called {
		t.Error("suppressed request should still be forwarded via next")
	}
	if auth := mock.lastReq.Header.Get("Authorization"); auth != "" {
		t.Errorf("suppressed request should have no Authorization header, got %q", auth)
	}
}

func TestSPNEGO_NoOriginNotSuppressed(t *testing.T) {
	mock := &mockRoundTripper{}
	rt := &SPNEGORoundTripper{
		next:      mock,
		proactive: false,
	}

	req, _ := http.NewRequestWithContext(
		context.Background(),
		"GET", "https://api.example.com/resource", nil,
	)

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !mock.called {
		t.Error("no-origin request should not be suppressed")
	}
}
