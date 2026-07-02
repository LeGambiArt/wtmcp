package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func testEnvDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "test-env.d")
}

func TestEnvDStoreGet(t *testing.T) {
	store := NewEnvDStore(testEnvDir(t))

	tests := []struct {
		group string
		key   string
		want  string
	}{
		{"jira", "JIRA_URL", "https://jira.example.com"},
		{"jira", "JIRA_TOKEN", "test-token-abc123"},
		{"jira", "JIRA_EMAIL", "test@example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, err := store.Get(tt.group, tt.key)
			if err != nil {
				t.Fatalf("Get(%q, %q): %v", tt.group, tt.key, err)
			}
			if got != tt.want {
				t.Errorf("Get(%q, %q) = %q, want %q", tt.group, tt.key, got, tt.want)
			}
		})
	}
}

func TestEnvDStoreGetNotFound(t *testing.T) {
	store := NewEnvDStore(testEnvDir(t))

	// Missing key in existing group
	_, err := store.Get("jira", "MISSING_KEY")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("Get missing key: got err=%v, want ErrCredentialNotFound", err)
	}

	// Missing group entirely
	_, err = store.Get("nonexistent", "ANY_KEY")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("Get missing group: got err=%v, want ErrCredentialNotFound", err)
	}
}

func TestEnvDStoreGetAll(t *testing.T) {
	store := NewEnvDStore(testEnvDir(t))

	vars, err := store.GetAll("jira")
	if err != nil {
		t.Fatalf("GetAll(jira): %v", err)
	}

	if len(vars) != 3 {
		t.Fatalf("GetAll(jira) returned %d vars, want 3", len(vars))
	}

	expected := map[string]string{
		"JIRA_URL":   "https://jira.example.com",
		"JIRA_TOKEN": "test-token-abc123",
		"JIRA_EMAIL": "test@example.com",
	}

	for k, want := range expected {
		got, ok := vars[k]
		if !ok {
			t.Errorf("GetAll missing key %q", k)
			continue
		}
		if got != want {
			t.Errorf("GetAll[%q] = %q, want %q", k, got, want)
		}
	}
}

func TestEnvDStoreGetAllNotFound(t *testing.T) {
	store := NewEnvDStore(testEnvDir(t))

	_, err := store.GetAll("nonexistent")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("GetAll missing group: got err=%v, want ErrCredentialNotFound", err)
	}
}

func TestEnvDStoreListKeys(t *testing.T) {
	store := NewEnvDStore(testEnvDir(t))

	keys, err := store.ListKeys("jira")
	if err != nil {
		t.Fatalf("ListKeys(jira): %v", err)
	}

	sort.Strings(keys)
	expected := []string{"JIRA_EMAIL", "JIRA_TOKEN", "JIRA_URL"}

	if len(keys) != len(expected) {
		t.Fatalf("ListKeys returned %d keys, want %d", len(keys), len(expected))
	}

	for i, want := range expected {
		if keys[i] != want {
			t.Errorf("ListKeys[%d] = %q, want %q", i, keys[i], want)
		}
	}
}

func TestEnvDStoreListKeysNotFound(t *testing.T) {
	store := NewEnvDStore(testEnvDir(t))

	_, err := store.ListKeys("nonexistent")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("ListKeys missing group: got err=%v, want ErrCredentialNotFound", err)
	}
}

func TestEnvDStoreExists(t *testing.T) {
	store := NewEnvDStore(testEnvDir(t))

	if !store.Exists("jira") {
		t.Error("Exists(jira) = false, want true")
	}
	if store.Exists("nonexistent") {
		t.Error("Exists(nonexistent) = true, want false")
	}
}

func TestEnvDStoreCommentsAndEmptyLines(t *testing.T) {
	// Create a temp env file with comments and empty lines.
	dir := t.TempDir()
	content := `# This is a comment
KEY_A=valueA

# Another comment

KEY_B=valueB
`
	if err := os.WriteFile(filepath.Join(dir, "test.env"), []byte(content), 0o600); err != nil {
		t.Fatalf("write test.env: %v", err)
	}

	store := NewEnvDStore(dir)
	vars, err := store.GetAll("test")
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}

	if len(vars) != 2 {
		t.Fatalf("expected 2 vars, got %d", len(vars))
	}
	if vars["KEY_A"] != "valueA" {
		t.Errorf("KEY_A = %q, want %q", vars["KEY_A"], "valueA")
	}
	if vars["KEY_B"] != "valueB" {
		t.Errorf("KEY_B = %q, want %q", vars["KEY_B"], "valueB")
	}
}

func TestEnvDStoreQuotedValues(t *testing.T) {
	dir := t.TempDir()
	content := `DOUBLE_QUOTED="hello world"
SINGLE_QUOTED='hello world'
UNQUOTED=plain
`
	if err := os.WriteFile(filepath.Join(dir, "quotes.env"), []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	store := NewEnvDStore(dir)
	vars, err := store.GetAll("quotes")
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}

	if vars["DOUBLE_QUOTED"] != "hello world" {
		t.Errorf("DOUBLE_QUOTED = %q, want %q", vars["DOUBLE_QUOTED"], "hello world")
	}
	if vars["SINGLE_QUOTED"] != "hello world" {
		t.Errorf("SINGLE_QUOTED = %q, want %q", vars["SINGLE_QUOTED"], "hello world")
	}
	if vars["UNQUOTED"] != "plain" {
		t.Errorf("UNQUOTED = %q, want %q", vars["UNQUOTED"], "plain")
	}
}

func TestEnvDStoreExportPrefix(t *testing.T) {
	dir := t.TempDir()
	content := "export MY_VAR=exported_value\n"
	if err := os.WriteFile(filepath.Join(dir, "exports.env"), []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	store := NewEnvDStore(dir)
	got, err := store.Get("exports", "MY_VAR")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "exported_value" {
		t.Errorf("MY_VAR = %q, want %q", got, "exported_value")
	}
}
