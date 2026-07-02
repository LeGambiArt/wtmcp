package credentials

import (
	"errors"
	"fmt"
	"log"
	"sync"
)

// Service is the main credential resolution API. It resolves
// credentials from multiple sources with precedence rules that
// depend on whether a credential group has been migrated to the
// OS keyring.
//
// Resolution order for migrated groups: keyring -> envvar
// Resolution order for non-migrated groups: env.d -> keyring -> envvar
//
// All resolved credentials are cached with a 15-minute TTL.
//
// Service is safe for concurrent use. mu guards all multi-step read
// and write operations; warnMu guards the one-time warning flag.
type Service struct {
	keyringStore   *KeyringStore
	envdStore      *EnvDStore
	envvarProvider *EnvVarProvider
	migrationState *MigrationState
	cache          *Cache

	mu                       sync.RWMutex
	warnMu                   sync.Mutex
	keyringUnavailableWarned bool
}

// NewService creates a new credential resolution service. It
// initialises all backing stores and loads migration state from disk.
// envDDir is the directory containing env.d credential files.
// migrationFilePath is the path to the YAML migration state file.
func NewService(envDDir, migrationFilePath string) (*Service, error) {
	ms, err := LoadMigrationState(migrationFilePath)
	if err != nil {
		return nil, fmt.Errorf("load migration state: %w", err)
	}

	return &Service{
		keyringStore:   NewKeyringStore(),
		envdStore:      NewEnvDStore(envDDir),
		envvarProvider: NewEnvVarProvider(),
		migrationState: ms,
		cache:          NewCache(),
	}, nil
}

// Get resolves a credential by group and key. It checks the cache
// first, then applies precedence rules based on whether the group
// is migrated.
//
// Returns the credential value, the source it was resolved from,
// and any error.
func (s *Service) Get(group, key string) (string, CredentialSource, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check cache first.
	if cached, ok := s.cache.Get(group, key); ok {
		return cached.Value, cached.Source, nil
	}

	var (
		value  string
		source CredentialSource
		err    error
	)

	if s.migrationState.IsMigrated(group) {
		value, source, err = s.getMigratedCredential(group, key)
	} else {
		value, source, err = s.getLegacyCredential(group, key)
	}

	if err != nil {
		return "", SourceNotFound, err
	}

	// Cache the resolved credential.
	s.cache.Set(group, key, value, source)
	return value, source, nil
}

// getMigratedCredential resolves a credential for a group that has
// been migrated to the keyring. Precedence: keyring -> envvar.
func (s *Service) getMigratedCredential(group, key string) (string, CredentialSource, error) {
	// Try keyring first.
	value, err := s.keyringStore.Get(group, key)
	if err == nil {
		// Warn if the env.d file still exists for this group.
		if s.envdStore.Exists(group) {
			log.Printf("[credentials] warning: %s is migrated but env.d/%s.env still exists", group, group)
		}
		return value, SourceKeyring, nil
	}

	// If the keyring error is something other than "not found",
	// the keyring may be unavailable.
	if !errors.Is(err, ErrCredentialNotFound) {
		s.warnKeyringUnavailable()
	}

	// Fall back to environment variable.
	value, err = s.envvarProvider.Get(group, key)
	if err == nil {
		return value, SourceEnvVar, nil
	}

	return "", SourceNotFound, ErrCredentialNotFound
}

// getLegacyCredential resolves a credential for a group that has
// not been migrated. Precedence: env.d -> keyring -> envvar.
func (s *Service) getLegacyCredential(group, key string) (string, CredentialSource, error) {
	// Try env.d first.
	value, err := s.envdStore.Get(group, key)
	if err == nil {
		return value, SourceEnvD, nil
	}

	// Try keyring as fallback (partial migration support).
	value, err = s.keyringStore.Get(group, key)
	if err == nil {
		return value, SourceKeyring, nil
	}

	// Try environment variable.
	value, err = s.envvarProvider.Get(group, key)
	if err == nil {
		return value, SourceEnvVar, nil
	}

	return "", SourceNotFound, ErrCredentialNotFound
}

// warnKeyringUnavailable logs a one-time warning that the OS
// keyring is not accessible.
func (s *Service) warnKeyringUnavailable() {
	s.warnMu.Lock()
	defer s.warnMu.Unlock()
	if !s.keyringUnavailableWarned {
		log.Printf("[credentials] warning: keyring unavailable, falling back to environment variables")
		s.keyringUnavailableWarned = true
	}
}

// Set stores a credential. If forceKeyring is true or the group is
// migrated, the credential is stored in the keyring. Otherwise an
// error is returned because writing to env.d files is not supported.
func (s *Service) Set(group, key, value string, forceKeyring bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Invalidate cache for this credential.
	s.cache.Invalidate(group, key)

	if forceKeyring || s.migrationState.IsMigrated(group) {
		if err := s.keyringStore.Set(group, key, value); err != nil {
			return err
		}
		// Track the key in migration state for backup inventory.
		if err := s.migrationState.TrackKey(group, key); err != nil {
			log.Printf("[credentials] warning: failed to track key %s/%s: %v", group, key, err)
		}
		return nil
	}

	return fmt.Errorf("writing to env.d files not supported")
}

// InvalidateCache removes a specific credential from the cache.
func (s *Service) InvalidateCache(group, key string) {
	s.cache.Invalidate(group, key)
}

// ClearCache removes all entries from the cache.
func (s *Service) ClearCache() {
	s.cache.Clear()
}

// GetMigrationState returns the migration state for CLI access.
func (s *Service) GetMigrationState() *MigrationState {
	return s.migrationState
}

// IsKeyringAvailable returns true if the OS keyring is accessible.
func (s *Service) IsKeyringAvailable() bool {
	return s.keyringStore.IsAvailable()
}

// KeyringStore returns the underlying keyring store. This is
// needed by TokenEncryption which stores encryption keys and
// access tokens directly in the keyring.
func (s *Service) KeyringStore() *KeyringStore {
	return s.keyringStore
}

// Delete removes a credential from the keyring and/or env.d.
// It tries both stores and reports success if removed from at least
// one. Returns ErrCredentialNotFound if the key was not found in any
// store.
func (s *Service) Delete(group, key string) (deletedFrom []CredentialSource, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cache.Invalidate(group, key)

	var sources []CredentialSource

	// Try keyring deletion.
	if kerr := s.keyringStore.Delete(group, key); kerr == nil {
		sources = append(sources, SourceKeyring)
		// Untrack from migration state.
		if terr := s.migrationState.UntrackKey(group, key); terr != nil {
			log.Printf("[credentials] warning: failed to untrack key %s/%s: %v", group, key, terr)
		}
	}

	// Try env.d deletion.
	if derr := s.envdStore.Delete(group, key); derr == nil {
		sources = append(sources, SourceEnvD)
	}

	if len(sources) == 0 {
		return nil, ErrCredentialNotFound
	}

	return sources, nil
}

// DeleteFromKeyring removes a credential only from the keyring.
func (s *Service) DeleteFromKeyring(group, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cache.Invalidate(group, key)
	if err := s.keyringStore.Delete(group, key); err != nil {
		return err
	}
	if terr := s.migrationState.UntrackKey(group, key); terr != nil {
		log.Printf("[credentials] warning: failed to untrack key %s/%s: %v", group, key, terr)
	}
	return nil
}

// DeleteFromEnvD removes a credential only from the env.d file.
func (s *Service) DeleteFromEnvD(group, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cache.Invalidate(group, key)
	return s.envdStore.Delete(group, key)
}

// ListGroups returns all known credential groups from env.d files
// and migration state (keyring groups).
func (s *Service) ListGroups() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[string]bool)
	var groups []string

	// Add env.d groups.
	for _, g := range s.envdStore.ListGroups() {
		if !seen[g] {
			seen[g] = true
			groups = append(groups, g)
		}
	}

	// Add migrated groups.
	for _, g := range s.migrationState.GetMigratedGroups() {
		if !seen[g] {
			seen[g] = true
			groups = append(groups, g)
		}
	}

	return groups
}

// GroupInfo holds summary information about a credential group.
type GroupInfo struct {
	Name        string
	Migrated    bool
	EnvDKeys    []string
	KeyringKeys []string
}

// GetGroupInfo returns detailed information about a credential group.
func (s *Service) GetGroupInfo(group string) GroupInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	info := GroupInfo{
		Name:     group,
		Migrated: s.migrationState.IsMigrated(group),
	}

	// Get env.d keys.
	if keys, err := s.envdStore.ListKeys(group); err == nil {
		info.EnvDKeys = keys
	}

	// Get keyring keys from migration state tracking.
	info.KeyringKeys = s.migrationState.GetGroupKeys(group)

	return info
}

// GetWithSource resolves a credential and returns its value and source.
// This is a convenience wrapper around Get for CLI use.
func (s *Service) GetWithSource(group, key string) (value string, source CredentialSource, err error) {
	return s.Get(group, key)
}

// EnvDDir returns the env.d directory path.
func (s *Service) EnvDDir() string {
	return s.envdStore.Dir()
}

// GetAll returns all credentials for a group from env.d and keyring
// sources. Environment variables are not included because they cannot
// be enumerated. Use Get(group, key) to resolve individual credentials
// including env vars.
//
// This is used by the plugin manager to build plugin vars for template
// resolution. Any credential accessible only via environment variable
// will not appear here; plugins should declare such keys in their
// manifest so they can be resolved individually with Get().
func (s *Service) GetAll(group string) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	vars := make(map[string]string)

	// Start with env.d values (always available for legacy).
	if envdVars, err := s.envdStore.GetAll(group); err == nil {
		for k, v := range envdVars {
			vars[k] = v
		}
	}

	// Overlay keyring values for any group with tracked keys. This
	// covers both fully migrated groups and individual keys stored
	// via "wtmcpctl credentials set --keyring" before full migration.
	keys := s.migrationState.GetGroupKeys(group)
	for _, key := range keys {
		if value, err := s.keyringStore.Get(group, key); err == nil {
			vars[key] = value
		}
	}

	return vars
}
