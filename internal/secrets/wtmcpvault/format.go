// Package wtmcpvault implements the $WTMCP_VAULT encryption format with
// AES-256-GCM authenticated encryption and pluggable key-derivation
// backends (PASSWORD, FIDO2, PKCS11).
package wtmcpvault

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	magic   = "$WTMCP_VAULT;"
	version = 1

	// BackendPassword is the password-based key derivation backend.
	BackendPassword = "PASSWORD"
	// BackendFIDO2 is the FIDO2 hmac-secret hardware backend.
	BackendFIDO2 = "FIDO2"
	// BackendPKCS11 is the PKCS#11 envelope encryption backend.
	BackendPKCS11 = "PKCS11"

	// CipherAES256GCM is the authenticated encryption cipher.
	CipherAES256GCM = "AES256GCM"

	maxFileSize = 1 << 20
)

var validBackend = regexp.MustCompile(`^(PASSWORD|FIDO2|PKCS11)$`)

// Header holds the parsed fields from a $WTMCP_VAULT header line.
type Header struct {
	Version int
	Backend string // PASSWORD, FIDO2, or PKCS11
	Cipher  string // AES256GCM
}

// FormatHeader returns the header line for a $WTMCP_VAULT file.
func FormatHeader(backend string) string {
	return fmt.Sprintf("%s%d;%s;%s", magic, version, backend, CipherAES256GCM)
}

// IsWtmcpVault reports whether data begins with the $WTMCP_VAULT; prefix.
func IsWtmcpVault(data []byte) bool {
	return len(data) >= len(magic) && string(data[:len(magic)]) == magic
}

// ParseHeader parses a $WTMCP_VAULT header line and validates its fields.
func ParseHeader(headerLine string) (Header, error) {
	headerLine = strings.TrimRight(headerLine, "\r\n")
	parts := strings.Split(headerLine, ";")

	if len(parts) != 4 {
		return Header{}, fmt.Errorf("invalid wtmcp vault header: expected 4 fields, got %d", len(parts))
	}
	if parts[0] != "$WTMCP_VAULT" {
		return Header{}, fmt.Errorf("invalid wtmcp vault header: missing $WTMCP_VAULT prefix")
	}

	if parts[1] != "1" {
		return Header{}, fmt.Errorf("unsupported wtmcp vault version: %s", parts[1])
	}

	backend := parts[2]
	if !validBackend.MatchString(backend) {
		return Header{}, fmt.Errorf("unsupported wtmcp vault backend: %s", backend)
	}

	cipher := parts[3]
	if cipher != CipherAES256GCM {
		return Header{}, fmt.Errorf("unsupported wtmcp vault cipher: %s", cipher)
	}

	return Header{
		Version: version,
		Backend: backend,
		Cipher:  cipher,
	}, nil
}
