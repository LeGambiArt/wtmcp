package pinentry

import (
	"os"
	"path/filepath"
	"testing"
)

// mockPinentryScript creates a shell script that speaks the Assuan
// protocol and returns a fixed password.
func mockPinentryScript(t *testing.T, password string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "mock-pinentry")

	content := `#!/bin/sh
echo "OK Pleased to meet you"
while IFS= read -r line; do
    case "$line" in
        SETDESC*|SETPROMPT*|SETTITLE*|SETOK*|SETCANCEL*|SETERROR*|SETQUALITYBAR*|OPTION*)
            echo "OK"
            ;;
        GETPIN)
            echo "D ` + password + `"
            echo "OK"
            ;;
        BYE)
            echo "OK closing connection"
            exit 0
            ;;
        *)
            echo "OK"
            ;;
    esac
done
`
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil { //nolint:gosec // test mock script needs execute permission
		t.Fatal(err)
	}
	return script
}

// mockPinentryCancel creates a script that simulates the user pressing Cancel.
func mockPinentryCancel(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "mock-pinentry")

	content := `#!/bin/sh
echo "OK Pleased to meet you"
while IFS= read -r line; do
    case "$line" in
        GETPIN)
            echo "ERR 83886179 Operation cancelled"
            ;;
        BYE)
            echo "OK closing connection"
            exit 0
            ;;
        *)
            echo "OK"
            ;;
    esac
done
`
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil { //nolint:gosec // test mock script needs execute permission
		t.Fatal(err)
	}
	return script
}

func TestGetPassword(t *testing.T) {
	script := mockPinentryScript(t, "my-secret-password")

	c, err := newFromPath(script)
	if err != nil {
		t.Fatalf("newFromPath: %v", err)
	}
	defer func() { _ = c.Close() }()

	pw, err := c.GetPassword("Password:", "Enter your vault password")
	if err != nil {
		t.Fatalf("GetPassword: %v", err)
	}

	if string(pw) != "my-secret-password" {
		t.Errorf("got %q, want %q", string(pw), "my-secret-password")
	}
}

func TestGetPasswordCancel(t *testing.T) {
	script := mockPinentryCancel(t)

	c, err := newFromPath(script)
	if err != nil {
		t.Fatalf("newFromPath: %v", err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.GetPassword("Password:", "Enter password")
	if err == nil {
		t.Fatal("expected error on cancel")
	}
}

func TestGetPasswordWithSpecialChars(t *testing.T) {
	script := mockPinentryScript(t, "p%25ss%0Aw%0Drd")

	c, err := newFromPath(script)
	if err != nil {
		t.Fatalf("newFromPath: %v", err)
	}
	defer func() { _ = c.Close() }()

	pw, err := c.GetPassword("Password:", "Test")
	if err != nil {
		t.Fatalf("GetPassword: %v", err)
	}

	if string(pw) != "p%ss\nw\rrd" {
		t.Errorf("decoded password = %q, want %q", string(pw), "p%ss\nw\rrd")
	}
}

func TestEncodeDecode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"hello%world", "hello%25world"},
		{"line1\nline2", "line1%0Aline2"},
		{"cr\rhere", "cr%0Dhere"},
		{"all%\n\r", "all%25%0A%0D"},
	}
	for _, tt := range tests {
		encoded := encode(tt.input)
		if encoded != tt.want {
			t.Errorf("encode(%q) = %q, want %q", tt.input, encoded, tt.want)
		}
		decoded := decode(encoded)
		if decoded != tt.input {
			t.Errorf("decode(encode(%q)) = %q", tt.input, decoded)
		}
	}
}

func TestDecodeInvalidPercent(t *testing.T) {
	if got := decode("abc%ZZdef"); got != "abc%ZZdef" {
		t.Errorf("decode with invalid %% = %q, want %q", got, "abc%ZZdef")
	}
	if got := decode("abc%2"); got != "abc%2" {
		t.Errorf("decode truncated %% = %q, want %q", got, "abc%2")
	}
}

func TestPinentrySearchOrder(t *testing.T) {
	candidates := pinentrySearchOrder()
	if len(candidates) == 0 {
		t.Error("expected at least one candidate")
	}
	// Last candidate should always be generic "pinentry"
	if candidates[len(candidates)-1] != "pinentry" {
		t.Errorf("last candidate should be 'pinentry', got %q", candidates[len(candidates)-1])
	}
}

func TestAvailable(_ *testing.T) {
	// Just verify it doesn't panic. Actual availability depends on
	// the test machine.
	_ = Available()
}

func TestFindPinentryEnvVar(t *testing.T) {
	script := mockPinentryScript(t, "unused")
	t.Setenv("WTMCP_PINENTRY", script)

	path, err := findPinentry()
	if err != nil {
		t.Fatalf("findPinentry with WTMCP_PINENTRY: %v", err)
	}
	if path != script {
		t.Errorf("found %q, want %q", path, script)
	}
}

func TestFindPinentryEnvVarNotFound(t *testing.T) {
	t.Setenv("WTMCP_PINENTRY", "/nonexistent/pinentry-fake")

	_, err := findPinentry()
	if err == nil {
		t.Fatal("expected error for nonexistent WTMCP_PINENTRY")
	}
}
