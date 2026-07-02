// Package secrets provides vault format detection and multi-backend
// decryption for $ANSIBLE_VAULT and $WTMCP_VAULT encrypted files.
package secrets

import "bytes"

// Format identifies the encryption format of a file.
type Format int

// Vault format constants.
const (
	FormatPlaintext    Format = iota // FormatPlaintext indicates unencrypted data.
	FormatAnsibleVault               // FormatAnsibleVault indicates $ANSIBLE_VAULT format.
	FormatWtmcpVault                 // FormatWtmcpVault indicates $WTMCP_VAULT format.
)

var (
	ansibleMagic = []byte("$ANSIBLE_VAULT;")
	wtmcpMagic   = []byte("$WTMCP_VAULT;")
)

// DetectFormat identifies the encryption format of data by inspecting
// the header prefix.
func DetectFormat(data []byte) Format {
	switch {
	case bytes.HasPrefix(data, wtmcpMagic):
		return FormatWtmcpVault
	case bytes.HasPrefix(data, ansibleMagic):
		return FormatAnsibleVault
	default:
		return FormatPlaintext
	}
}

// IsEncrypted reports whether data begins with a recognized vault
// header prefix ($ANSIBLE_VAULT; or $WTMCP_VAULT;).
func IsEncrypted(data []byte) bool {
	return DetectFormat(data) != FormatPlaintext
}
