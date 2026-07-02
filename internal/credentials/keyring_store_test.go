package credentials

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

func init() {
	// Use the mock keyring provider for testing so tests do not
	// require a real D-Bus session or macOS Keychain.
	keyring.MockInit()
}

func TestKeyringStoreSetGetDelete(t *testing.T) {
	store := NewKeyringStore()

	group := "test-group"
	key := "API_KEY"
	value := "secret-123"

	// Set
	if err := store.Set(group, key, value); err != nil {
		t.Fatalf("Set(%q, %q): %v", group, key, err)
	}

	// Get
	got, err := store.Get(group, key)
	if err != nil {
		t.Fatalf("Get(%q, %q): %v", group, key, err)
	}
	if got != value {
		t.Errorf("Get(%q, %q) = %q, want %q", group, key, got, value)
	}

	// Delete
	if err := store.Delete(group, key); err != nil {
		t.Fatalf("Delete(%q, %q): %v", group, key, err)
	}

	// Get after delete
	_, err = store.Get(group, key)
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("Get after Delete: got err=%v, want ErrCredentialNotFound", err)
	}
}

func TestKeyringStoreGetNotFound(t *testing.T) {
	store := NewKeyringStore()

	_, err := store.Get("nonexistent", "MISSING_KEY")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("Get nonexistent: got err=%v, want ErrCredentialNotFound", err)
	}
}

func TestKeyringStoreDeleteNotFound(t *testing.T) {
	store := NewKeyringStore()

	err := store.Delete("nonexistent", "MISSING_KEY")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("Delete nonexistent: got err=%v, want ErrCredentialNotFound", err)
	}
}

func TestKeyringStoreOverwrite(t *testing.T) {
	store := NewKeyringStore()

	group := "overwrite-group"
	key := "TOKEN"

	if err := store.Set(group, key, "first"); err != nil {
		t.Fatalf("Set first: %v", err)
	}
	if err := store.Set(group, key, "second"); err != nil {
		t.Fatalf("Set second: %v", err)
	}

	got, err := store.Get(group, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "second" {
		t.Errorf("Get = %q, want %q", got, "second")
	}

	// Cleanup
	_ = store.Delete(group, key)
}

func TestKeyringStoreIsAvailable(t *testing.T) {
	store := NewKeyringStore()

	if !store.IsAvailable() {
		t.Error("IsAvailable() = false with mock keyring, want true")
	}
}

func TestServiceName(t *testing.T) {
	got := serviceName("jira")
	want := "wtmcp.jira"
	if got != want {
		t.Errorf("serviceName(%q) = %q, want %q", "jira", got, want)
	}
}

func TestKeyringStoreMultipleGroups(t *testing.T) {
	store := NewKeyringStore()

	// Store keys in different groups
	if err := store.Set("group-a", "KEY", "value-a"); err != nil {
		t.Fatalf("Set group-a: %v", err)
	}
	if err := store.Set("group-b", "KEY", "value-b"); err != nil {
		t.Fatalf("Set group-b: %v", err)
	}

	// Retrieve and verify isolation
	gotA, err := store.Get("group-a", "KEY")
	if err != nil {
		t.Fatalf("Get group-a: %v", err)
	}
	gotB, err := store.Get("group-b", "KEY")
	if err != nil {
		t.Fatalf("Get group-b: %v", err)
	}

	if gotA != "value-a" {
		t.Errorf("group-a KEY = %q, want %q", gotA, "value-a")
	}
	if gotB != "value-b" {
		t.Errorf("group-b KEY = %q, want %q", gotB, "value-b")
	}

	// Cleanup
	_ = store.Delete("group-a", "KEY")
	_ = store.Delete("group-b", "KEY")
}
