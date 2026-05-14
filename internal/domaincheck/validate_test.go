package domaincheck

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		input       string
		want        string
		wantErr     bool
		description string
	}{
		{"example.com", "example.com", false, "simple domain"},
		{"Example.COM", "example.com", false, "uppercase"},
		{"  example.com  ", "example.com", false, "whitespace"},
		{"example.com.", "example.com", false, "trailing dot"},
		{"Example.COM.", "example.com", false, "uppercase with trailing dot"},
		{"api.example.com", "api.example.com", false, "subdomain"},
		{"", "", false, "empty string"},

		// Punycode/IDNA
		{"münchen.de", "xn--mnchen-3ya.de", false, "Unicode to punycode"},
		{"日本.jp", "xn--wgv71a.jp", false, "Japanese to punycode"},

		// These should pass through or fail gracefully
		{"localhost", "localhost", false, "localhost"},
		{"127.0.0.1", "127.0.0.1", false, "IPv4 address"},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			got, err := Normalize(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Normalize(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("Normalize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		domain  string
		wantErr bool
		errMsg  string
	}{
		// Valid domains
		{"example.com", false, ""},
		{"sub.example.com", false, ""},
		{"api-v2.example.com", false, ""},
		{"Example.COM", false, ""},  // normalized to lowercase
		{"example.com.", false, ""}, // trailing dot removed

		// Punycode domains (valid after normalization)
		{"münchen.de", false, ""},

		// Invalid - empty
		{"", true, "empty domain"},
		{"   ", true, "empty domain"},

		// Invalid - localhost
		{"localhost", true, "localhost"},
		{"LOCALHOST", true, "localhost"},
		{"LocalHost", true, "localhost"},

		// Invalid - wildcards
		{"*.example.com", true, "wildcards"},
		{"*", true, "wildcards"},

		// Invalid - IP addresses
		{"127.0.0.1", true, "IP addresses"},
		{"192.168.1.1", true, "IP addresses"},
		{"10.0.0.1", true, "IP addresses"},
		{"::1", true, "IP addresses"},
		{"2001:db8::1", true, "IP addresses"},
		{"[::1]", true, "IP addresses"},
		{"[2001:db8::1]", true, "IP addresses"},
	}

	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			err := Validate(tt.domain)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tt.domain, err, tt.wantErr)
			}
			if err != nil && tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("Validate(%q) error = %v, want error containing %q", tt.domain, err, tt.errMsg)
			}
		})
	}
}

func TestContains(t *testing.T) {
	tests := []struct {
		domains []string
		domain  string
		want    bool
		desc    string
	}{
		// Basic matching
		{[]string{"example.com"}, "example.com", true, "exact match"},
		{[]string{"example.com", "test.com"}, "test.com", true, "match in list"},
		{[]string{"example.com"}, "other.com", false, "no match"},
		{[]string{}, "example.com", false, "empty list"},

		// Case insensitive
		{[]string{"example.com"}, "Example.COM", true, "case insensitive"},
		{[]string{"Example.COM"}, "example.com", true, "case insensitive reverse"},

		// Trailing dot normalization
		{[]string{"example.com"}, "example.com.", true, "trailing dot query"},
		{[]string{"example.com."}, "example.com", true, "trailing dot in list"},
		{[]string{"example.com."}, "example.com.", true, "both trailing dots"},

		// Whitespace
		{[]string{"example.com"}, "  example.com  ", true, "whitespace in query"},
		{[]string{"  example.com  "}, "example.com", true, "whitespace in list"},

		// Punycode normalization
		{[]string{"xn--mnchen-3ya.de"}, "münchen.de", true, "Unicode query, punycode list"},
		{[]string{"münchen.de"}, "xn--mnchen-3ya.de", true, "punycode query, Unicode list"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			got := Contains(tt.domains, tt.domain)
			if got != tt.want {
				t.Errorf("Contains(%v, %q) = %v, want %v", tt.domains, tt.domain, got, tt.want)
			}
		})
	}
}

func TestIsSubdomain(t *testing.T) {
	tests := []struct {
		domain      string
		baseDomains []string
		want        bool
		description string
	}{
		// Exact matches
		{"jenkins.example.com", []string{"jenkins.example.com"}, true, "exact match"},
		{"api.example.com", []string{"jenkins.example.com", "api.example.com"}, true, "exact match in list"},

		// Valid subdomains
		{"api.jenkins.example.com", []string{"jenkins.example.com"}, true, "subdomain"},
		{"ci.jenkins.example.com", []string{"jenkins.example.com"}, true, "subdomain"},
		{"deep.nested.jenkins.example.com", []string{"jenkins.example.com"}, true, "deeply nested subdomain"},

		// Invalid - not subdomains
		{"attacker.com", []string{"jenkins.example.com"}, false, "completely different domain"},
		{"eviljenkins.example.com", []string{"jenkins.example.com"}, false, "prefix but not subdomain"},
		{"jenkins.example.com.attacker.com", []string{"jenkins.example.com"}, false, "suffix but not subdomain"},

		// Edge cases
		{"jenkins.example.com", []string{}, false, "no base domains"},
		{"jenkins.example.com.", []string{"jenkins.example.com"}, true, "trailing dot normalized"},
		{"jenkins.example.com", []string{"jenkins.example.com."}, true, "base trailing dot normalized"},
		{"api.jenkins.example.com.", []string{"jenkins.example.com."}, true, "both trailing dots normalized"},

		// Case insensitivity
		{"API.Jenkins.Example.COM", []string{"jenkins.example.com"}, true, "case insensitive exact"},
		{"api.JENKINS.example.com", []string{"Jenkins.Example.Com"}, true, "case insensitive subdomain"},

		// Multiple base domains
		{"api.jenkins.com", []string{"gitlab.com", "jenkins.com", "github.com"}, true, "matches second base domain"},
		{"ci.gitlab.com", []string{"gitlab.com", "jenkins.com"}, true, "matches first base domain"},
		{"evil.com", []string{"gitlab.com", "jenkins.com"}, false, "matches no base domain"},

		// Prevent homograph-style attacks
		{"eviljenkins.com", []string{"jenkins.com"}, false, "prefix not subdomain"},
		{"jenkins.com.evil.com", []string{"jenkins.com"}, false, "fake subdomain"},

		// Whitespace handling
		{"api.jenkins.com", []string{"  jenkins.com  "}, true, "whitespace in base"},
		{"  api.jenkins.com  ", []string{"jenkins.com"}, true, "whitespace in domain"},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			got := IsSubdomain(tt.domain, tt.baseDomains)
			if got != tt.want {
				t.Errorf("IsSubdomain(%q, %v) = %v, want %v (%s)",
					tt.domain, tt.baseDomains, got, tt.want, tt.description)
			}
		})
	}
}
