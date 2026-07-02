package credentials

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// KeyringStore wraps zalando/go-keyring to provide credential
// storage in the OS-native secret store (Linux Secret Service,
// macOS Keychain, Windows Credential Manager).
//
// Service names use the format "wtmcp.<group>" to namespace
// credentials by group.
type KeyringStore struct{}

// NewKeyringStore creates a new KeyringStore.
func NewKeyringStore() *KeyringStore {
	return &KeyringStore{}
}

// serviceName builds the keyring service name for a credential group.
func serviceName(group string) string {
	return fmt.Sprintf("wtmcp.%s", group)
}

// Get retrieves a credential from the OS keyring.
// Returns ErrCredentialNotFound if the key does not exist.
func (k *KeyringStore) Get(group, key string) (string, error) {
	value, err := keyring.Get(serviceName(group), key)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrCredentialNotFound
		}
		return "", fmt.Errorf("keyring get %s/%s: %w", group, key, err)
	}
	return value, nil
}

// Set stores a credential in the OS keyring.
func (k *KeyringStore) Set(group, key, value string) error {
	err := keyring.Set(serviceName(group), key, value)
	if err != nil {
		return fmt.Errorf("keyring set %s/%s: %w", group, key, err)
	}
	return nil
}

// Delete removes a credential from the OS keyring.
// Returns ErrCredentialNotFound if the key does not exist.
func (k *KeyringStore) Delete(group, key string) error {
	err := keyring.Delete(serviceName(group), key)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return ErrCredentialNotFound
		}
		return fmt.Errorf("keyring delete %s/%s: %w", group, key, err)
	}
	return nil
}

// IsAvailable checks whether the OS keyring is accessible by
// attempting a get on a non-existent test key. If the keyring
// returns ErrNotFound, the keyring is available. Any other error
// indicates the keyring is unavailable.
func (k *KeyringStore) IsAvailable() bool {
	_, err := keyring.Get("wtmcp.__probe__", "__probe__")
	// ErrNotFound means the keyring is working — the key just
	// doesn't exist.
	return err == nil || errors.Is(err, keyring.ErrNotFound)
}
