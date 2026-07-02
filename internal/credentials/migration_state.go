package credentials

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// MigrationState tracks which credential groups have been migrated
// from env.d files to the OS keyring. State is persisted as a YAML
// file with restricted permissions (0600).
//
// GroupKeys tracks the credential key names stored in the keyring for
// each migrated group. This enables backup/restore without requiring
// keyring enumeration (which go-keyring does not support).
type MigrationState struct {
	mu sync.Mutex `yaml:"-"`

	Version        int                 `yaml:"version"`
	MigratedGroups []string            `yaml:"migrated_groups"`
	GroupKeys      map[string][]string `yaml:"group_keys,omitempty"`
	LastUpdated    time.Time           `yaml:"last_updated"`

	filePath string `yaml:"-"`
}

// LoadMigrationState reads migration state from a YAML file. If the
// file does not exist, returns a new empty state that will be saved
// to filePath on the next write.
func LoadMigrationState(filePath string) (*MigrationState, error) {
	state := &MigrationState{
		Version:  1,
		filePath: filePath,
	}

	data, err := os.ReadFile(filePath) //nolint:gosec // config file path
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return nil, fmt.Errorf("read migration state: %w", err)
	}

	if err := yaml.Unmarshal(data, state); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidFormat, err)
	}
	state.filePath = filePath

	return state, nil
}

// IsMigrated checks whether a credential group has been migrated
// to the keyring.
func (s *MigrationState) IsMigrated(group string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isMigrated(group)
}

// isMigrated is the non-locking implementation of IsMigrated.
// Callers must hold s.mu.
func (s *MigrationState) isMigrated(group string) bool {
	for _, g := range s.MigratedGroups {
		if g == group {
			return true
		}
	}
	return false
}

// MarkMigrated marks a credential group as migrated and persists
// the state to disk.
func (s *MigrationState) MarkMigrated(group string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isMigrated(group) {
		return nil
	}
	s.MigratedGroups = append(s.MigratedGroups, group)
	s.LastUpdated = time.Now().UTC()
	return s.save()
}

// Unmark removes a credential group from the migrated list and
// persists the state to disk. No-op if the group is not migrated.
func (s *MigrationState) Unmark(group string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, g := range s.MigratedGroups {
		if g == group {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	s.MigratedGroups = append(s.MigratedGroups[:idx], s.MigratedGroups[idx+1:]...)
	s.LastUpdated = time.Now().UTC()
	return s.save()
}

// GetMigratedGroups returns a copy of the migrated group list.
func (s *MigrationState) GetMigratedGroups() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]string, len(s.MigratedGroups))
	copy(result, s.MigratedGroups)
	return result
}

// TrackKey records a key name for a migrated group. This information
// is used by backup/restore to enumerate keyring contents without
// requiring keyring listing support.
func (s *MigrationState) TrackKey(group, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.GroupKeys == nil {
		s.GroupKeys = make(map[string][]string)
	}

	// Check if the key is already tracked.
	for _, k := range s.GroupKeys[group] {
		if k == key {
			return nil
		}
	}

	s.GroupKeys[group] = append(s.GroupKeys[group], key)
	s.LastUpdated = time.Now().UTC()
	return s.save()
}

// UntrackKey removes a key name from a migrated group's tracked keys.
func (s *MigrationState) UntrackKey(group, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.GroupKeys == nil {
		return nil
	}

	keys := s.GroupKeys[group]
	idx := -1
	for i, k := range keys {
		if k == key {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}

	s.GroupKeys[group] = append(keys[:idx], keys[idx+1:]...)
	if len(s.GroupKeys[group]) == 0 {
		delete(s.GroupKeys, group)
	}
	s.LastUpdated = time.Now().UTC()
	return s.save()
}

// GetGroupKeys returns a copy of the tracked key names for a group.
// Returns nil if no keys are tracked for the group.
func (s *MigrationState) GetGroupKeys(group string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys := s.GroupKeys[group]
	if keys == nil {
		return nil
	}
	result := make([]string, len(keys))
	copy(result, keys)
	return result
}

// save persists the migration state to disk with 0600 permissions.
// It writes to a temporary file in the same directory and renames it
// atomically to prevent a partial write from corrupting the state file.
func (s *MigrationState) save() error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal migration state: %w", err)
	}

	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create migration state directory: %w", err)
	}

	// Write to a temp file in the same directory so rename is atomic.
	tmp, err := os.CreateTemp(dir, ".migration-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("set temp file permissions: %w", err)
	}

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write migration state: %w", err)
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, s.filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename migration state: %w", err)
	}

	return nil
}
