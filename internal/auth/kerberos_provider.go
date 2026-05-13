package auth

import (
	"context"
	"fmt"
	"net/http"

	"github.com/LeGambiArt/wtmcp/internal/auth/kerberos"
)

// KerberosProvider injects SPNEGO authentication via GSSAPI.
// Credentials are acquired fresh per request from the system's
// default credential cache, so kinit renewals are picked up automatically.
type KerberosProvider struct {
	spn string
}

// NewKerberosProvider creates a Kerberos/SPNEGO auth provider.
// The spn should be in "HTTP@hostname" format.
func NewKerberosProvider(spn string) *KerberosProvider {
	return &KerberosProvider{spn: spn}
}

// Name returns "kerberos/spnego".
func (k *KerberosProvider) Name() string { return "kerberos/spnego" }

// SPN returns the configured service principal name.
func (k *KerberosProvider) SPN() string { return k.spn }

// Available reports whether GSSAPI is initialized. When spn is empty,
// the SPN is derived dynamically from each request's hostname.
func (k *KerberosProvider) Available() bool {
	return kerberos.Available()
}

// Authenticate acquires a SPNEGO token and returns the Negotiate header.
func (k *KerberosProvider) Authenticate(_ context.Context, _ *http.Request) (http.Header, error) {
	if k.spn == "" {
		return nil, fmt.Errorf("kerberos SPN not configured")
	}
	if !kerberos.Available() {
		return nil, fmt.Errorf("GSSAPI library not available")
	}

	// Create a temporary request to get the header set by SetSPNEGOHeader
	tmpReq, err := http.NewRequest("GET", "https://placeholder", nil)
	if err != nil {
		return nil, fmt.Errorf("create temp request: %w", err)
	}

	if err := kerberos.SetSPNEGOHeader(tmpReq, k.spn); err != nil {
		return nil, fmt.Errorf("kerberos auth: %w", err)
	}

	h := make(http.Header)
	h.Set("Authorization", tmpReq.Header.Get("Authorization"))
	return h, nil
}

// InitKerberos initializes the GSSAPI subsystem.
// Should be called once at startup. Returns nil if GSSAPI is not available
// (e.g., library not installed).
func InitKerberos() error {
	return kerberos.Init()
}

// CloseKerberos cleans up the GSSAPI subsystem.
func CloseKerberos() {
	kerberos.Close()
}

// KerberosAvailable reports whether GSSAPI is available on this platform.
func KerberosAvailable() bool {
	return kerberos.Available()
}
