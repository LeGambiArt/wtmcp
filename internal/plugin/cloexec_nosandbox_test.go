//go:build nosandbox

package plugin

import (
	"testing"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/sandbox"
)

// TestDecryptCredentialSecurefileStubDisabled pins the fourth
// quadrant of the CLOEXEC predicate: a sandbox manager that is
// present but disabled (the nosandbox stub) must keep the shadow
// securefile inheritable — there the shadow dir symlink is the
// credential delivery mechanism. Dropping Enabled() from the
// predicate would flip this to close-on-exec and break vault
// delivery in nosandbox builds.
func TestDecryptCredentialSecurefileStubDisabled(t *testing.T) {
	t.Setenv("WTMCP_UNSANDBOXED", "1")
	sb, err := sandbox.NewManager(config.SandboxConfig{}, t.TempDir())
	if err != nil {
		t.Fatalf("stub NewManager: %v", err)
	}
	if sb.Enabled() {
		t.Fatal("stub manager must report disabled")
	}

	m := newTestManager(t)
	m.SetSandbox(sb)
	m.decryptor = &testCredentialDecryptor{plaintext: []byte("decrypted-secret")}

	path, err := m.decryptCredentialToSecurefile("probe", "creds.json", []byte("$ANSIBLE_VAULT;1.1;AES256\nbody"))
	if err != nil {
		t.Fatalf("decryptCredentialToSecurefile: %v", err)
	}
	if fdIsCloexec(t, path) {
		t.Error("fd must stay inheritable when the sandbox manager is present but disabled")
	}
}
