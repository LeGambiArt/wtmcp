package secrets

import (
	"bytes"
	"strings"
	"testing"

	"github.com/LeGambiArt/wtmcp/internal/secrets/vault"
	"github.com/LeGambiArt/wtmcp/internal/secrets/wtmcpvault"
)

func TestMultiDecryptorAnsibleVault(t *testing.T) {
	password := []byte("test-password")
	plaintext := []byte("TOKEN=secret123")

	encrypted, err := vault.Encrypt(plaintext, password)
	if err != nil {
		t.Fatal(err)
	}

	d, err := NewMultiDecryptor(MultiDecryptorConfig{
		AnsibleResolver: func(_ string) ([]byte, error) {
			pw := make([]byte, len(password))
			copy(pw, password)
			return pw, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	decrypted, err := d.Decrypt(encrypted, "test-group")
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("got %q, want %q", decrypted, plaintext)
	}
}

func TestMultiDecryptorWtmcpVault(t *testing.T) {
	password := []byte("test-password")
	plaintext := []byte("GITHUB_TOKEN=ghp_abc123")
	group := "github"

	encrypted, err := wtmcpvault.EncryptPasswordWithContext(plaintext, password, group)
	if err != nil {
		t.Fatal(err)
	}

	d, err := NewMultiDecryptor(MultiDecryptorConfig{
		AnsibleResolver: func(_ string) ([]byte, error) {
			pw := make([]byte, len(password))
			copy(pw, password)
			return pw, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	decrypted, err := d.Decrypt(encrypted, group)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("got %q, want %q", decrypted, plaintext)
	}
}

func TestMultiDecryptorPasswordCaching(t *testing.T) {
	password := []byte("cached-pw")
	callCount := 0

	d, err := NewMultiDecryptor(MultiDecryptorConfig{
		AnsibleResolver: func(_ string) ([]byte, error) {
			callCount++
			pw := make([]byte, len(password))
			copy(pw, password)
			return pw, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	for i := range 3 {
		group := "test"
		encrypted, err := wtmcpvault.EncryptPasswordWithContext(
			[]byte("SECRET=value"), password, group)
		if err != nil {
			t.Fatalf("encrypt %d: %v", i, err)
		}

		_, err = d.Decrypt(encrypted, group)
		if err != nil {
			t.Fatalf("decrypt %d: %v", i, err)
		}
	}

	if callCount != 1 {
		t.Errorf("resolver called %d times, want 1 (password should be cached)", callCount)
	}
}

func TestMultiDecryptorPlaintext(t *testing.T) {
	d, err := NewMultiDecryptor(MultiDecryptorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	_, err = d.Decrypt([]byte("TOKEN=plaintext"), "test")
	if err == nil {
		t.Fatal("expected error for plaintext data")
	}
}

func TestMultiDecryptorNoResolver(t *testing.T) {
	d, err := NewMultiDecryptor(MultiDecryptorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	encrypted, _ := vault.Encrypt([]byte("data"), []byte("pw"))
	_, err = d.Decrypt(encrypted, "test")
	if err == nil {
		t.Fatal("expected error with no resolver")
	}
}

func TestMultiDecryptorClose(t *testing.T) {
	password := []byte("pw")
	d, err := NewMultiDecryptor(MultiDecryptorConfig{
		AnsibleResolver: func(_ string) ([]byte, error) {
			pw := make([]byte, len(password))
			copy(pw, password)
			return pw, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	group := "test"
	encrypted, _ := wtmcpvault.EncryptPasswordWithContext([]byte("data"), password, group)
	_, err = d.Decrypt(encrypted, group)
	if err != nil {
		t.Fatal(err)
	}

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	// Cached password should be zeroed.
	if d.cachedPassword != nil {
		t.Error("cached password should be nil after Close")
	}
}

func TestMultiDecryptorFIDO2NotImplemented(t *testing.T) {
	d, err := NewMultiDecryptor(MultiDecryptorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	data := []byte("$WTMCP_VAULT;1;FIDO2;AES256GCM\n" + string(make([]byte, 50)))
	_, err = d.Decrypt(data, "test")
	if err == nil {
		t.Fatal("expected error for FIDO2")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMultiDecryptorPKCS11NotImplemented(t *testing.T) {
	d, err := NewMultiDecryptor(MultiDecryptorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	data := []byte("$WTMCP_VAULT;1;PKCS11;AES256GCM\n" + string(make([]byte, 50)))
	_, err = d.Decrypt(data, "test")
	if err == nil {
		t.Fatal("expected error for PKCS11")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("unexpected error: %v", err)
	}
}
