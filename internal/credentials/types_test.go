package credentials

import (
	"errors"
	"testing"
)

func TestCredentialSourceString(t *testing.T) {
	tests := []struct {
		source CredentialSource
		want   string
	}{
		{SourceKeyring, "keyring"},
		{SourceEnvD, "env.d"},
		{SourceEnvVar, "envvar"},
		{SourceNotFound, "not_found"},
		{CredentialSource(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.source.String()
			if got != tt.want {
				t.Errorf("CredentialSource(%d).String() = %q, want %q", tt.source, got, tt.want)
			}
		})
	}
}

func TestCredentialSourceValues(t *testing.T) {
	// Verify iota ordering is stable.
	if SourceKeyring != 0 {
		t.Errorf("SourceKeyring = %d, want 0", SourceKeyring)
	}
	if SourceEnvD != 1 {
		t.Errorf("SourceEnvD = %d, want 1", SourceEnvD)
	}
	if SourceEnvVar != 2 {
		t.Errorf("SourceEnvVar = %d, want 2", SourceEnvVar)
	}
	if SourceNotFound != 3 {
		t.Errorf("SourceNotFound = %d, want 3", SourceNotFound)
	}
}

func TestSentinelErrors(t *testing.T) {
	// Verify errors are distinct and non-nil.
	errs := []error{
		ErrKeyringUnavailable,
		ErrCredentialNotFound,
		ErrGroupNotMigrated,
		ErrInvalidFormat,
	}
	for i, e := range errs {
		if e == nil {
			t.Fatalf("sentinel error at index %d is nil", i)
		}
		for j, other := range errs {
			if i != j && errors.Is(e, other) {
				t.Errorf("sentinel errors at index %d and %d are the same object", i, j)
			}
		}
	}
}
