package secrets

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/LeGambiArt/wtmcp/internal/secrets/pinentry"
	"github.com/LeGambiArt/wtmcp/internal/secrets/vault"
	"github.com/LeGambiArt/wtmcp/internal/secrets/wtmcpvault"
)

// VaultDecryptor decrypts vault-encrypted data regardless of format.
// It dispatches to the correct backend based on the file header and
// caches derived keys per backend type to minimize prompts.
type VaultDecryptor interface {
	// Decrypt decrypts vault-encrypted data. The context string
	// (typically the credential group name) is used as part of AAD
	// for $WTMCP_VAULT files and as vault ID for Ansible Vault files.
	Decrypt(data []byte, context string) ([]byte, error)

	// Close zeros cached keys and releases resources.
	Close() error
}

// PasswordResolver resolves a password for a given vault ID or context.
// For Ansible Vault: vaultID is the label (or "" for 1.1).
// For $WTMCP_VAULT: vaultID is "" (pinentry handles prompting).
type PasswordResolver func(vaultID string) ([]byte, error)

// MultiDecryptor implements VaultDecryptor by dispatching to the
// correct backend based on file headers. It caches derived passwords
// per backend type so that only one prompt is needed per backend.
type MultiDecryptor struct {
	// ansibleResolver handles $ANSIBLE_VAULT password resolution
	// (env vars, files, config). May be nil.
	ansibleResolver PasswordResolver

	// pinentryClient is used for $WTMCP_VAULT PASSWORD backend.
	// May be nil if pinentry is unavailable.
	pinentryClient *pinentry.Client

	// cachedPassword is the cached password for the PASSWORD backend.
	// Populated on first use, either from pinentry or fallback resolver.
	cachedPassword []byte
	passwordCached bool

	mu sync.Mutex
}

// MultiDecryptorConfig configures a MultiDecryptor.
type MultiDecryptorConfig struct {
	// AnsibleResolver resolves passwords for Ansible Vault files.
	// Also used as fallback for $WTMCP_VAULT PASSWORD files when
	// pinentry is unavailable.
	AnsibleResolver PasswordResolver

	// UsePinentry enables pinentry-based password prompting for
	// $WTMCP_VAULT PASSWORD backend. When true and pinentry is
	// available, it takes priority over AnsibleResolver for new
	// vault format files.
	UsePinentry bool
}

// NewMultiDecryptor creates a VaultDecryptor that handles both
// $ANSIBLE_VAULT and $WTMCP_VAULT formats.
func NewMultiDecryptor(cfg MultiDecryptorConfig) (*MultiDecryptor, error) {
	d := &MultiDecryptor{
		ansibleResolver: cfg.AnsibleResolver,
	}

	if cfg.UsePinentry {
		if client, err := pinentry.New(); err == nil {
			d.pinentryClient = client
		}
		// Pinentry unavailability is not fatal — we fall back to
		// AnsibleResolver.
	}

	return d, nil
}

// Decrypt dispatches to the correct backend based on the file header.
func (d *MultiDecryptor) Decrypt(data []byte, context string) ([]byte, error) {
	format := DetectFormat(data)

	switch format {
	case FormatAnsibleVault:
		return d.decryptAnsibleVault(data, context)
	case FormatWtmcpVault:
		return d.decryptWtmcpVault(data, context)
	default:
		return nil, fmt.Errorf("data is not encrypted")
	}
}

// Close zeros cached keys and closes the pinentry client.
func (d *MultiDecryptor) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.cachedPassword != nil {
		zeroBytes(d.cachedPassword)
		d.cachedPassword = nil
		d.passwordCached = false
	}

	if d.pinentryClient != nil {
		err := d.pinentryClient.Close()
		d.pinentryClient = nil
		return err
	}

	return nil
}

func (d *MultiDecryptor) decryptAnsibleVault(data []byte, _ string) ([]byte, error) {
	if d.ansibleResolver == nil {
		return nil, fmt.Errorf("encrypted file but no vault password configured — " +
			"set WTMCP_VAULT_PASSWORD or secrets.vault_password_file in config.yaml")
	}

	// Parse header to get vault ID for password resolution.
	content := string(data)
	lines := strings.SplitN(content, "\n", 2)
	header, err := vault.ParseHeader(lines[0])
	if err != nil {
		return nil, fmt.Errorf("invalid vault header: %w", err)
	}

	password, err := d.ansibleResolver(header.VaultID)
	if err != nil {
		return nil, err
	}
	defer vault.ZeroBytes(password)

	return vault.Decrypt(data, password)
}

func (d *MultiDecryptor) decryptWtmcpVault(data []byte, context string) ([]byte, error) {
	headerLine, err := wtmcpvault.ExtractHeader(data)
	if err != nil {
		return nil, err
	}

	header, err := wtmcpvault.ParseHeader(headerLine)
	if err != nil {
		return nil, err
	}

	switch header.Backend {
	case wtmcpvault.BackendPassword:
		return d.decryptWtmcpPassword(data, headerLine, context)
	case wtmcpvault.BackendFIDO2:
		return nil, fmt.Errorf("FIDO2 backend not yet implemented")
	case wtmcpvault.BackendPKCS11:
		return nil, fmt.Errorf("PKCS#11 backend not yet implemented")
	default:
		return nil, fmt.Errorf("unknown backend: %s", header.Backend)
	}
}

func (d *MultiDecryptor) decryptWtmcpPassword(data []byte, headerLine, context string) ([]byte, error) {
	password, err := d.resolveWtmcpPassword()
	if err != nil {
		return nil, err
	}
	defer zeroBytes(password)

	aad := wtmcpvault.BuildAAD(headerLine, context)
	return wtmcpvault.DecryptPassword(data, password, aad)
}

// resolveWtmcpPassword gets the password for $WTMCP_VAULT PASSWORD
// backend. Uses cached password if available. Otherwise tries pinentry
// first, then falls back to the Ansible resolver.
func (d *MultiDecryptor) resolveWtmcpPassword() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.passwordCached {
		pw := make([]byte, len(d.cachedPassword))
		copy(pw, d.cachedPassword)
		return pw, nil
	}

	// Try pinentry first (preferred for new vault format).
	if d.pinentryClient != nil {
		pw, err := d.pinentryClient.GetPassword(
			"Password:",
			"Enter wtmcp vault password",
		)
		if errors.Is(err, pinentry.ErrCancelled) {
			return nil, err
		}
		if err == nil && len(pw) > 0 {
			d.cachedPassword = make([]byte, len(pw))
			copy(d.cachedPassword, pw)
			d.passwordCached = true
			return pw, nil
		}
		// Pinentry returned a non-cancellation error or empty — fall through.
	}

	// Fall back to Ansible-style resolution (env vars, files).
	if d.ansibleResolver != nil {
		pw, err := d.ansibleResolver("")
		if err == nil {
			d.cachedPassword = make([]byte, len(pw))
			copy(d.cachedPassword, pw)
			d.passwordCached = true
			return pw, nil
		}
	}

	return nil, fmt.Errorf("no vault password available — " +
		"install pinentry, set WTMCP_VAULT_PASSWORD, or configure secrets.vault_password_file")
}

// HasPinentry reports whether this decryptor has a working pinentry client.
func (d *MultiDecryptor) HasPinentry() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pinentryClient != nil
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
	runtime.KeepAlive(b)
}
