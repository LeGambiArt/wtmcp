package diagnostic

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/plugin"
)

func TestVaultPasswordSource_File(t *testing.T) {
	cfg := &config.Config{}
	cfg.Secrets.VaultPasswordFile = "/tmp/vault-pass"

	got := VaultPasswordSource(cfg, nil)
	want := "file (/tmp/vault-pass)"
	if got != want {
		t.Errorf("VaultPasswordSource = %q, want %q", got, want)
	}
}

func TestVaultPasswordSource_VaultIDs(t *testing.T) {
	cfg := &config.Config{}
	cfg.Secrets.VaultIDs = map[string]string{"prod": "/tmp/prod-pass"}

	got := VaultPasswordSource(cfg, nil)
	if got != "vault IDs" {
		t.Errorf("VaultPasswordSource = %q, want %q", got, "vault IDs")
	}
}

func TestVaultPasswordSource_EnvVar(t *testing.T) {
	cfg := &config.Config{}
	resolver := func(_ string) ([]byte, error) {
		return []byte("secret"), nil
	}

	got := VaultPasswordSource(cfg, resolver)
	if got != "env var" {
		t.Errorf("VaultPasswordSource = %q, want %q", got, "env var")
	}
}

func TestVaultPasswordSource_NotConfigured(t *testing.T) {
	cfg := &config.Config{}

	got := VaultPasswordSource(cfg, nil)
	if got != "not configured" {
		t.Errorf("VaultPasswordSource = %q, want %q", got, "not configured")
	}
}

func TestVaultPasswordSource_FileTakesPrecedence(t *testing.T) {
	cfg := &config.Config{}
	cfg.Secrets.VaultPasswordFile = "/tmp/vault-pass"
	cfg.Secrets.VaultIDs = map[string]string{"prod": "/tmp/prod-pass"}

	got := VaultPasswordSource(cfg, func(_ string) ([]byte, error) {
		return []byte("secret"), nil
	})
	if got != "file (/tmp/vault-pass)" {
		t.Errorf("file should take precedence, got %q", got)
	}
}

func TestPrintEnvGroups_Sorted(t *testing.T) {
	result := &plugin.DiscoveryResult{
		EnvGroups: map[string]map[string]string{
			"zeta":  {"K": "V"},
			"alpha": {"K": "V"},
			"mid":   {"K": "V"},
		},
	}

	var buf bytes.Buffer
	PrintEnvGroups(&buf, result)

	got := buf.String()
	want := "env groups: 3\n  - alpha\n  - mid\n  - zeta\n"
	if got != want {
		t.Errorf("PrintEnvGroups output:\n%s\nwant:\n%s", got, want)
	}
}

func TestPrintEnvGroups_WithErrors(t *testing.T) {
	result := &plugin.DiscoveryResult{
		EnvGroups: map[string]map[string]string{},
		EnvErrors: map[string]string{
			"beta":  "permission denied",
			"alpha": "file not found",
		},
	}

	var buf bytes.Buffer
	PrintEnvGroups(&buf, result)

	got := buf.String()
	if !bytes.Contains([]byte(got), []byte("alpha: file not found")) {
		t.Error("expected alpha error in output")
	}
	if !bytes.Contains([]byte(got), []byte("beta: permission denied")) {
		t.Error("expected beta error in output")
	}
}

func TestPrintVaultStatus_NoVault(t *testing.T) {
	result := &plugin.DiscoveryResult{
		Config: &config.Config{},
	}

	var buf bytes.Buffer
	PrintVaultStatus(&buf, result)

	got := buf.String()
	if got != "vault password: not configured\n" {
		t.Errorf("PrintVaultStatus = %q, want %q", got, "vault password: not configured\n")
	}
}

func TestPrintCredentialFileStatus_ReadError(t *testing.T) {
	dir := t.TempDir()
	groupDir := filepath.Join(dir, "testgroup")
	if err := os.Mkdir(groupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	credFile := filepath.Join(groupDir, "creds.json")
	if err := os.WriteFile(credFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(credFile, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(credFile, 0o600) })

	cfg := &config.Config{CredentialsDir: dir}
	resolver := func(_ string) ([]byte, error) {
		return []byte("pw"), nil
	}

	var buf bytes.Buffer
	PrintCredentialFileStatus(&buf, cfg, resolver)

	got := buf.String()
	if got != "" && !bytes.Contains([]byte(got), []byte("read error")) {
		t.Errorf("expected empty output or read error, got: %q", got)
	}
}
