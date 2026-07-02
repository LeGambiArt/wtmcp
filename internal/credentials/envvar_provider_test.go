package credentials

import (
	"errors"
	"testing"
)

func TestEnvVarProviderGet(t *testing.T) {
	provider := NewEnvVarProvider()

	const key = "WTMCP_TEST_CRED_VAR"
	const value = "test-secret-value"

	// Set env var for test
	t.Setenv(key, value)

	got, err := provider.Get("any-group", key)
	if err != nil {
		t.Fatalf("Get(%q, %q): %v", "any-group", key, err)
	}
	if got != value {
		t.Errorf("Get = %q, want %q", got, value)
	}
}

func TestEnvVarProviderGetUnset(t *testing.T) {
	provider := NewEnvVarProvider()

	_, err := provider.Get("ignored", "WTMCP_DEFINITELY_NOT_SET_12345")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("Get unset var: got err=%v, want ErrCredentialNotFound", err)
	}
}

func TestEnvVarProviderGroupIgnored(t *testing.T) {
	provider := NewEnvVarProvider()

	const key = "WTMCP_TEST_GROUP_IGNORED"
	t.Setenv(key, "value")

	// Different groups should return the same value
	v1, err := provider.Get("group-a", key)
	if err != nil {
		t.Fatalf("Get group-a: %v", err)
	}
	v2, err := provider.Get("group-b", key)
	if err != nil {
		t.Fatalf("Get group-b: %v", err)
	}

	if v1 != v2 {
		t.Errorf("different groups returned different values: %q vs %q", v1, v2)
	}
}

func TestEnvVarProviderEmptyValue(t *testing.T) {
	provider := NewEnvVarProvider()

	const key = "WTMCP_TEST_EMPTY"
	t.Setenv(key, "")

	got, err := provider.Get("any", key)
	if err != nil {
		t.Fatalf("Get empty var: %v", err)
	}
	if got != "" {
		t.Errorf("Get empty var = %q, want empty string", got)
	}
}
