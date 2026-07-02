package secrets

import "testing"

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want Format
	}{
		{"ansible vault 1.1", []byte("$ANSIBLE_VAULT;1.1;AES256\nhexdata"), FormatAnsibleVault},
		{"ansible vault 1.2", []byte("$ANSIBLE_VAULT;1.2;AES256;myid\nhexdata"), FormatAnsibleVault},
		{"wtmcp vault password", []byte("$WTMCP_VAULT;1;PASSWORD;AES256GCM\nbinarydata"), FormatWtmcpVault},
		{"wtmcp vault fido2", []byte("$WTMCP_VAULT;1;FIDO2;AES256GCM\nbinarydata"), FormatWtmcpVault},
		{"wtmcp vault pkcs11", []byte("$WTMCP_VAULT;1;PKCS11;AES256GCM\nbinarydata"), FormatWtmcpVault},
		{"plaintext env", []byte("TOKEN=secret123\n"), FormatPlaintext},
		{"empty", []byte{}, FormatPlaintext},
		{"short data", []byte("$AN"), FormatPlaintext},
		{"partial wtmcp", []byte("$WTMCP_VAU"), FormatPlaintext},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectFormat(tt.data); got != tt.want {
				t.Errorf("DetectFormat() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestIsEncrypted(t *testing.T) {
	if IsEncrypted([]byte("plaintext")) {
		t.Error("plaintext should not be encrypted")
	}
	if !IsEncrypted([]byte("$ANSIBLE_VAULT;1.1;AES256\n")) {
		t.Error("ansible vault should be encrypted")
	}
	if !IsEncrypted([]byte("$WTMCP_VAULT;1;PASSWORD;AES256GCM\n")) {
		t.Error("wtmcp vault should be encrypted")
	}
}
