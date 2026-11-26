// Package profile implements per-connection tool filtering driven by
// named profiles. A client's identity (extracted from its verified TLS
// client certificate, or from a startup flag on stdio) is resolved to a
// Filter that decides which tools are visible and callable.
package profile

import "crypto/x509"

// Identity holds the fields extracted from a TLS client certificate
// that profile rules can match against.
type Identity struct {
	CN        string
	SANURIs   []string
	SANDNSs   []string
	SANEmails []string
}

// ExtractIdentity extracts identity fields from a peer certificate.
func ExtractIdentity(cert *x509.Certificate) Identity {
	if cert == nil {
		return Identity{}
	}
	id := Identity{CN: cert.Subject.CommonName}
	for _, u := range cert.URIs {
		if u != nil {
			id.SANURIs = append(id.SANURIs, u.String())
		}
	}
	id.SANDNSs = cert.DNSNames
	id.SANEmails = cert.EmailAddresses
	return id
}
