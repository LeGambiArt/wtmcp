package credentials

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// EnvDStore reads credentials from env.d files on disk. Each
// credential group corresponds to a file at <envDir>/<group>.env.
// Files use the standard KEY=VALUE format with support for comments
// and quoted values.
type EnvDStore struct {
	envDir string
}

// NewEnvDStore creates a new EnvDStore rooted at envDir.
func NewEnvDStore(envDir string) *EnvDStore {
	return &EnvDStore{envDir: envDir}
}

// Get retrieves a single credential from the env.d file for the
// given group. Returns ErrCredentialNotFound if the group file does
// not exist or the key is not present.
func (s *EnvDStore) Get(group, key string) (string, error) {
	if err := validateGroupName(group); err != nil {
		return "", err
	}
	vars, err := s.loadGroup(group)
	if err != nil {
		return "", err
	}
	value, ok := vars[key]
	if !ok {
		return "", ErrCredentialNotFound
	}
	return value, nil
}

// GetAll returns all key-value pairs from the env.d file for the
// given group. Returns ErrCredentialNotFound if the group file does
// not exist.
func (s *EnvDStore) GetAll(group string) (map[string]string, error) {
	if err := validateGroupName(group); err != nil {
		return nil, err
	}
	vars, err := s.loadGroup(group)
	if err != nil {
		return nil, err
	}
	return vars, nil
}

// ListKeys returns all keys from the env.d file for the given
// group. Returns ErrCredentialNotFound if the group file does not
// exist.
func (s *EnvDStore) ListKeys(group string) ([]string, error) {
	if err := validateGroupName(group); err != nil {
		return nil, err
	}
	vars, err := s.loadGroup(group)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	return keys, nil
}

// Exists checks whether an env.d file exists for the given group.
func (s *EnvDStore) Exists(group string) bool {
	if err := validateGroupName(group); err != nil {
		return false
	}
	path := s.groupPath(group)
	_, err := os.Stat(path)
	return err == nil
}

// ListGroups returns the names of all credential groups that have
// env.d files (i.e., files matching <envDir>/*.env). Returns nil
// if the env.d directory does not exist or cannot be read.
func (s *EnvDStore) ListGroups() []string {
	entries, err := os.ReadDir(s.envDir)
	if err != nil {
		return nil
	}

	var groups []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".env") {
			groups = append(groups, strings.TrimSuffix(name, ".env"))
		}
	}
	return groups
}

// Delete removes a key from the env.d file for the given group.
// If the key is the last one in the file, the file is left empty
// (with only comments preserved). Returns ErrCredentialNotFound
// if the group file does not exist or the key is not present.
func (s *EnvDStore) Delete(group, key string) error {
	if err := validateGroupName(group); err != nil {
		return err
	}
	path := s.groupPath(group)

	data, err := os.ReadFile(path) //nolint:gosec // env file path from config
	if err != nil {
		if os.IsNotExist(err) {
			return ErrCredentialNotFound
		}
		return fmt.Errorf("read %s: %w", path, err)
	}

	lines := strings.Split(string(data), "\n")
	var newLines []string
	found := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Keep empty lines and comments.
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			newLines = append(newLines, line)
			continue
		}

		// Strip "export " prefix for matching.
		matchLine := strings.TrimPrefix(trimmed, "export ")

		idx := strings.IndexByte(matchLine, '=')
		if idx < 0 {
			newLines = append(newLines, line)
			continue
		}

		lineKey := strings.TrimSpace(matchLine[:idx])
		if lineKey == key {
			found = true
			continue // skip this line
		}
		newLines = append(newLines, line)
	}

	if !found {
		return ErrCredentialNotFound
	}

	output := strings.Join(newLines, "\n")
	if err := os.WriteFile(path, []byte(output), 0o600); err != nil { //nolint:gosec // path validated by resolveEnvFilePath
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

// Dir returns the env.d directory path.
func (s *EnvDStore) Dir() string {
	return s.envDir
}

// validateGroupName rejects group names that could cause path traversal.
// Group names must be non-empty and contain only alphanumeric characters,
// hyphens, and underscores.
func validateGroupName(group string) error {
	if group == "" {
		return fmt.Errorf("group name must not be empty")
	}
	for _, c := range group {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') &&
			(c < '0' || c > '9') && c != '-' && c != '_' {
			return fmt.Errorf("invalid group name %q: only letters, digits, hyphens, and underscores are allowed", group)
		}
	}
	return nil
}

// groupPath returns the filesystem path for a group's env file.
func (s *EnvDStore) groupPath(group string) string {
	return filepath.Join(s.envDir, group+".env")
}

// loadGroup parses the env.d file for the given group. Returns
// ErrCredentialNotFound if the file does not exist.
func (s *EnvDStore) loadGroup(group string) (map[string]string, error) {
	path := s.groupPath(group)

	f, err := os.Open(path) //nolint:gosec // env file path from config
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrCredentialNotFound
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	// Warn if the env.d file has overly permissive permissions.
	if info, statErr := f.Stat(); statErr == nil {
		perm := info.Mode().Perm()
		if perm&0o077 != 0 {
			log.Printf("[credentials] warning: %s has permissions %04o (should be 0600)", path, perm)
		}
	}

	vars := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Strip "export " prefix.
		line = strings.TrimPrefix(line, "export ")

		// Split on first '='.
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}

		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])

		// Strip surrounding double quotes.
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}
		// Strip surrounding single quotes.
		if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = value[1 : len(value)-1]
		}

		vars[key] = value
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return vars, nil
}
