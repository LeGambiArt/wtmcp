package profile

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"net/url"
	"reflect"
	"testing"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse url %q: %v", s, err)
	}
	return u
}

func TestExtractIdentity(t *testing.T) {
	cert := &x509.Certificate{
		Subject:        pkix.Name{CommonName: "code-review-bot"},
		DNSNames:       []string{"agent.example.com"},
		EmailAddresses: []string{"ci-bot@example.com"},
		URIs: []*url.URL{
			mustURL(t, "spiffe://example.com/agent/code-review"),
		},
	}

	id := ExtractIdentity(cert)
	if id.CN != "code-review-bot" {
		t.Errorf("CN = %q", id.CN)
	}
	if !reflect.DeepEqual(id.SANDNSs, []string{"agent.example.com"}) {
		t.Errorf("SANDNSs = %v", id.SANDNSs)
	}
	if !reflect.DeepEqual(id.SANEmails, []string{"ci-bot@example.com"}) {
		t.Errorf("SANEmails = %v", id.SANEmails)
	}
	if !reflect.DeepEqual(id.SANURIs, []string{"spiffe://example.com/agent/code-review"}) {
		t.Errorf("SANURIs = %v", id.SANURIs)
	}
}

func TestExtractIdentityNil(t *testing.T) {
	id := ExtractIdentity(nil)
	if id.CN != "" || id.SANURIs != nil || id.SANDNSs != nil || id.SANEmails != nil {
		t.Errorf("nil cert should yield empty identity, got %+v", id)
	}
}

func TestExtractIdentityEmpty(t *testing.T) {
	id := ExtractIdentity(&x509.Certificate{})
	if id.CN != "" || len(id.SANURIs) != 0 {
		t.Errorf("empty cert should yield empty identity, got %+v", id)
	}
}
