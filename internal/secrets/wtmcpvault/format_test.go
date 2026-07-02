package wtmcpvault

import "testing"

func TestIsWtmcpVault(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"password header", []byte("$WTMCP_VAULT;1;PASSWORD;AES256GCM\n"), true},
		{"fido2 header", []byte("$WTMCP_VAULT;1;FIDO2;AES256GCM\n"), true},
		{"ansible vault", []byte("$ANSIBLE_VAULT;1.1;AES256\n"), false},
		{"plaintext", []byte("TOKEN=secret\n"), false},
		{"empty", []byte{}, false},
		{"partial prefix", []byte("$WTMCP_V"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsWtmcpVault(tt.data); got != tt.want {
				t.Errorf("IsWtmcpVault() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseHeader(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		want    Header
		wantErr bool
	}{
		{
			name: "password",
			line: "$WTMCP_VAULT;1;PASSWORD;AES256GCM",
			want: Header{Version: 1, Backend: "PASSWORD", Cipher: "AES256GCM"},
		},
		{
			name: "fido2",
			line: "$WTMCP_VAULT;1;FIDO2;AES256GCM",
			want: Header{Version: 1, Backend: "FIDO2", Cipher: "AES256GCM"},
		},
		{
			name: "pkcs11",
			line: "$WTMCP_VAULT;1;PKCS11;AES256GCM",
			want: Header{Version: 1, Backend: "PKCS11", Cipher: "AES256GCM"},
		},
		{
			name:    "wrong prefix",
			line:    "$ANSIBLE_VAULT;1.1;AES256",
			wantErr: true,
		},
		{
			name:    "unsupported version",
			line:    "$WTMCP_VAULT;2;PASSWORD;AES256GCM",
			wantErr: true,
		},
		{
			name:    "unsupported backend",
			line:    "$WTMCP_VAULT;1;BITWARDEN;AES256GCM",
			wantErr: true,
		},
		{
			name:    "unsupported cipher",
			line:    "$WTMCP_VAULT;1;PASSWORD;AES128GCM",
			wantErr: true,
		},
		{
			name:    "too few fields",
			line:    "$WTMCP_VAULT;1;PASSWORD",
			wantErr: true,
		},
		{
			name:    "too many fields",
			line:    "$WTMCP_VAULT;1;PASSWORD;AES256GCM;extra",
			wantErr: true,
		},
		{
			name: "trailing newline",
			line: "$WTMCP_VAULT;1;PASSWORD;AES256GCM\n",
			want: Header{Version: 1, Backend: "PASSWORD", Cipher: "AES256GCM"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseHeader(tt.line)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseHeader() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseHeader() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFormatHeader(t *testing.T) {
	got := FormatHeader(BackendPassword)
	want := "$WTMCP_VAULT;1;PASSWORD;AES256GCM"
	if got != want {
		t.Errorf("FormatHeader(PASSWORD) = %q, want %q", got, want)
	}
}
