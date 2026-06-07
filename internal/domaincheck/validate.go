// Package domaincheck provides domain validation and normalization
// for preventing security vulnerabilities like SSRF and credential
// exfiltration in the proxy's domain allowlist.
package domaincheck

import (
	"fmt"
	"net"
	"strings"

	"golang.org/x/net/idna"
)

// Normalize canonicalizes a domain name for consistent comparison:
//   - Converts to lowercase
//   - Strips leading/trailing whitespace
//   - Removes trailing dots (FQDN normalization)
//   - Converts Unicode domains to ASCII (punycode/IDNA)
//
// This prevents bypass via:
//   - Case variations (Example.COM vs example.com)
//   - Trailing dots (example.com. vs example.com)
//   - Unicode/homoglyph attacks (jеnkins.com with Cyrillic е)
func Normalize(domain string) (string, error) {
	domain = strings.TrimSpace(domain)
	domain = strings.ToLower(domain)
	domain = strings.TrimRight(domain, ".")

	// Convert Unicode domains to ASCII using IDNA (Internationalized
	// Domain Names in Applications). This handles punycode (xn--) and
	// prevents homoglyph attacks where visually similar Unicode
	// characters are used to spoof legitimate domains.
	//
	// Example: "exаmple.com" (with Cyrillic 'а') becomes invalid or
	// normalizes to a different punycode form than "example.com".
	ascii, err := idna.ToASCII(domain)
	if err != nil {
		return "", fmt.Errorf("invalid domain name (IDNA conversion failed): %w", err)
	}

	return ascii, nil
}

// Validate checks if a domain is allowed for use in the proxy allowlist.
// It rejects domains that pose security risks:
//   - Empty domains
//   - Wildcard domains (*.example.com)
//   - localhost (prevents credential theft from local services)
//   - IP addresses, both IPv4 and IPv6 (prevents SSRF to internal IPs)
//
// This validation is used for both init-time domain registration
// (plugin.yaml allowed_domains, init_ok response) and per-request
// domain additions.
func Validate(domain string) error {
	// Normalize first to catch Unicode wildcard attempts and handle whitespace
	normalized, err := Normalize(domain)
	if err != nil {
		return err
	}

	if normalized == "" {
		return fmt.Errorf("empty domain is not allowed")
	}

	if strings.HasPrefix(normalized, "*") {
		return fmt.Errorf("%q is not allowed (wildcards are not supported)", domain)
	}

	if normalized == "localhost" {
		return fmt.Errorf("%q is not allowed (localhost)", domain)
	}

	// Check for IP addresses (both IPv4 and IPv6)
	// We check the normalized form to catch Unicode IP address attempts
	ip := net.ParseIP(normalized)
	if ip != nil {
		return fmt.Errorf("%q is not allowed (IP addresses are not permitted, use domain names)", domain)
	}

	// Check for IPv6 in brackets (e.g., [::1])
	if strings.HasPrefix(normalized, "[") && strings.HasSuffix(normalized, "]") {
		return fmt.Errorf("%q is not allowed (IP addresses are not permitted)", domain)
	}

	return nil
}

// Contains checks if a domain exists in a list of domains, using
// normalized comparison to prevent bypass via case, trailing dots,
// or Unicode variations.
func Contains(domains []string, domain string) bool {
	normalized, err := Normalize(domain)
	if err != nil {
		// If normalization fails, domain is invalid and can't match
		return false
	}

	for _, d := range domains {
		if norm, err := Normalize(d); err == nil && norm == normalized {
			return true
		}
	}
	return false
}

// IsSubdomain checks if domain is an exact match or subdomain of any
// base domain. This prevents credential exfiltration by ensuring
// per-request domains can only expand the allowlist within the scope
// of domains pre-declared in plugin.yaml or init_ok.
//
// Examples:
//   - "api.jenkins.com" is subdomain of "jenkins.com" → true
//   - "jenkins.com" is subdomain of "jenkins.com" → true (exact match)
//   - "evil.com" is subdomain of "jenkins.com" → false
//   - "eviljenkins.com" is subdomain of "jenkins.com" → false
func IsSubdomain(domain string, baseDomains []string) bool {
	if len(baseDomains) == 0 {
		return false
	}

	normalized, err := Normalize(domain)
	if err != nil {
		return false
	}

	for _, base := range baseDomains {
		normalizedBase, err := Normalize(base)
		if err != nil {
			continue
		}

		// Exact match
		if normalized == normalizedBase {
			return true
		}

		// Subdomain match: must end with ".base"
		// This prevents "eviljenkins.com" matching "jenkins.com"
		if strings.HasSuffix(normalized, "."+normalizedBase) {
			return true
		}
	}

	return false
}
