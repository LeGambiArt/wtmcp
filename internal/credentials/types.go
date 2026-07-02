// Package credentials provides credential storage and resolution for wtmcp.
//
// It supports multiple credential sources (OS keyring, env.d files,
// environment variables) with in-memory caching and migration tracking.
package credentials

import (
	"errors"
	"time"
)

// CredentialSource identifies where a credential was resolved from.
type CredentialSource int

const (
	// SourceKeyring indicates the credential was resolved from the OS keyring
	// (Linux Secret Service / macOS Keychain).
	SourceKeyring CredentialSource = iota

	// SourceEnvD indicates the credential was resolved from an env.d file.
	SourceEnvD

	// SourceEnvVar indicates the credential was resolved from the process
	// environment.
	SourceEnvVar

	// SourceNotFound indicates the credential could not be resolved from
	// any source.
	SourceNotFound
)

// String returns a human-readable name for the credential source.
func (s CredentialSource) String() string {
	switch s {
	case SourceKeyring:
		return "keyring"
	case SourceEnvD:
		return "env.d"
	case SourceEnvVar:
		return "envvar"
	case SourceNotFound:
		return "not_found"
	default:
		return "unknown"
	}
}

// CachedValue holds a resolved credential along with metadata about
// its source and when it was cached.
type CachedValue struct {
	Value    string
	Source   CredentialSource
	LoadedAt time.Time
}

// CredentialProvider is the interface implemented by all credential
// sources (KeyringStore, EnvDStore, EnvVarProvider). It provides
// polymorphic access to credentials regardless of the backing store.
type CredentialProvider interface {
	Get(group, key string) (string, error)
}

// Sentinel errors for credential operations.
var (
	// ErrKeyringUnavailable is returned when the OS keyring cannot be
	// accessed (e.g., no D-Bus session on Linux, or keychain locked).
	ErrKeyringUnavailable = errors.New("keyring unavailable")

	// ErrCredentialNotFound is returned when a credential does not
	// exist in the requested store.
	ErrCredentialNotFound = errors.New("credential not found")

	// ErrGroupNotMigrated is returned when a credential group has not
	// been migrated to the keyring yet.
	ErrGroupNotMigrated = errors.New("group not migrated to keyring")

	// ErrInvalidFormat is returned when a credential file or migration
	// state file has an invalid format.
	ErrInvalidFormat = errors.New("invalid format")
)
