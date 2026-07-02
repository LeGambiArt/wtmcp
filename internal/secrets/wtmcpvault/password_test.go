package wtmcpvault

import (
	"bytes"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	plaintext := []byte("GITHUB_TOKEN=ghp_abc123secret456\nJIRA_TOKEN=tok_xyz")
	password := []byte("test-vault-password")
	aad := BuildAAD(FormatHeader(BackendPassword), "github")

	encrypted, err := EncryptPassword(plaintext, password, aad)
	if err != nil {
		t.Fatalf("EncryptPassword: %v", err)
	}

	if !IsWtmcpVault(encrypted) {
		t.Error("encrypted output should start with $WTMCP_VAULT;")
	}

	decrypted, err := DecryptPassword(encrypted, password, aad)
	if err != nil {
		t.Fatalf("DecryptPassword: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("round-trip mismatch:\n  got:  %q\n  want: %q", decrypted, plaintext)
	}
}

func TestDecryptWrongPassword(t *testing.T) {
	plaintext := []byte("SECRET=value")
	password := []byte("correct-password")
	aad := BuildAAD(FormatHeader(BackendPassword), "test")

	encrypted, err := EncryptPassword(plaintext, password, aad)
	if err != nil {
		t.Fatalf("EncryptPassword: %v", err)
	}

	_, err = DecryptPassword(encrypted, []byte("wrong-password"), aad)
	if err == nil {
		t.Fatal("expected error with wrong password")
	}
	if !strings.Contains(err.Error(), "decryption failed") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDecryptAADMismatch(t *testing.T) {
	plaintext := []byte("SECRET=value")
	password := []byte("password")
	header := FormatHeader(BackendPassword)

	encrypted, err := EncryptPassword(plaintext, password, BuildAAD(header, "github"))
	if err != nil {
		t.Fatalf("EncryptPassword: %v", err)
	}

	_, err = DecryptPassword(encrypted, password, BuildAAD(header, "jira"))
	if err == nil {
		t.Fatal("expected error with wrong AAD (file-swap attack)")
	}
}

func TestDecryptTamperedCiphertext(t *testing.T) {
	plaintext := []byte("SECRET=value")
	password := []byte("password")
	aad := BuildAAD(FormatHeader(BackendPassword), "test")

	encrypted, err := EncryptPassword(plaintext, password, aad)
	if err != nil {
		t.Fatalf("EncryptPassword: %v", err)
	}

	// Flip a byte in the ciphertext area
	encrypted[len(encrypted)-5] ^= 0xff

	_, err = DecryptPassword(encrypted, password, aad)
	if err == nil {
		t.Fatal("expected error with tampered ciphertext")
	}
}

func TestEncryptEmptyPassword(t *testing.T) {
	_, err := EncryptPassword([]byte("data"), []byte{}, nil)
	if err == nil {
		t.Fatal("expected error with empty password")
	}
}

func TestEncryptEmptyPlaintext(t *testing.T) {
	password := []byte("password")
	aad := BuildAAD(FormatHeader(BackendPassword), "test")

	encrypted, err := EncryptPassword([]byte{}, password, aad)
	if err != nil {
		t.Fatalf("EncryptPassword: %v", err)
	}

	decrypted, err := DecryptPassword(encrypted, password, aad)
	if err != nil {
		t.Fatalf("DecryptPassword: %v", err)
	}

	if len(decrypted) != 0 {
		t.Errorf("expected empty plaintext, got %d bytes", len(decrypted))
	}
}

func TestEncryptDecryptWithContext(t *testing.T) {
	plaintext := []byte("TOKEN=abc123")
	password := []byte("my-password")
	group := "my-plugin"

	encrypted, err := EncryptPasswordWithContext(plaintext, password, group)
	if err != nil {
		t.Fatalf("EncryptPasswordWithContext: %v", err)
	}

	decrypted, err := DecryptPasswordWithContext(encrypted, password, group)
	if err != nil {
		t.Fatalf("DecryptPasswordWithContext: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Error("round-trip mismatch with context helpers")
	}
}

func TestDecryptWithContextWrongGroup(t *testing.T) {
	plaintext := []byte("TOKEN=abc123")
	password := []byte("my-password")

	encrypted, err := EncryptPasswordWithContext(plaintext, password, "github")
	if err != nil {
		t.Fatalf("EncryptPasswordWithContext: %v", err)
	}

	_, err = DecryptPasswordWithContext(encrypted, password, "jira")
	if err == nil {
		t.Fatal("expected error when credential group doesn't match")
	}
}

func TestDecryptTruncatedPayload(t *testing.T) {
	password := []byte("password")
	aad := BuildAAD(FormatHeader(BackendPassword), "test")

	header := FormatHeader(BackendPassword) + "\n"
	// payload too short
	short := append([]byte(header), 1, 2, 3)

	_, err := DecryptPassword(short, password, aad)
	if err == nil {
		t.Fatal("expected error with truncated payload")
	}
}

func TestDecryptWrongBackend(t *testing.T) {
	data := []byte("$WTMCP_VAULT;1;FIDO2;AES256GCM\n" + string(make([]byte, minPayloadLen)))
	_, err := DecryptPassword(data, []byte("pw"), nil)
	if err == nil {
		t.Fatal("expected error for wrong backend")
	}
	if !strings.Contains(err.Error(), "expected PASSWORD") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFormatInfo(t *testing.T) {
	plaintext := []byte("data")
	password := []byte("password")
	aad := BuildAAD(FormatHeader(BackendPassword), "test")

	encrypted, err := EncryptPassword(plaintext, password, aad)
	if err != nil {
		t.Fatal(err)
	}

	info := FormatInfo(encrypted)
	if !strings.Contains(info, "password") {
		t.Errorf("FormatInfo should mention password, got: %q", info)
	}
	if !strings.Contains(info, "argon2id") {
		t.Errorf("FormatInfo should mention argon2id, got: %q", info)
	}
}

func TestBuildAAD(t *testing.T) {
	aad := BuildAAD("$WTMCP_VAULT;1;PASSWORD;AES256GCM", "github")
	if !bytes.Contains(aad, []byte("$WTMCP_VAULT")) {
		t.Error("AAD should contain header")
	}
	if !bytes.Contains(aad, []byte("github")) {
		t.Error("AAD should contain context")
	}
	if !bytes.Contains(aad, []byte{0}) {
		t.Error("AAD should contain null separator")
	}
}

func TestNilAAD(t *testing.T) {
	plaintext := []byte("SECRET=val")
	password := []byte("password")

	encrypted, err := EncryptPassword(plaintext, password, nil)
	if err != nil {
		t.Fatal(err)
	}

	decrypted, err := DecryptPassword(encrypted, password, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Error("round-trip with nil AAD failed")
	}
}

func TestDecryptNoNewline(t *testing.T) {
	_, err := DecryptPassword([]byte("$WTMCP_VAULT;1;PASSWORD;AES256GCM"), []byte("pw"), nil)
	if err == nil {
		t.Fatal("expected error with no newline")
	}
}
