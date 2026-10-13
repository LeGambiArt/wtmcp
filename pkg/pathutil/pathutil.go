// Package pathutil provides path resolution utilities for plugin
// handlers running inside OS sandboxes (Seatbelt, Landlock).
package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolvePath resolves a filesystem path using EvalSymlinks when
// possible. If EvalSymlinks fails with a permission error (expected
// inside OS sandboxes where lstat on ancestor directories is denied),
// it falls back to Clean+Abs. Any other EvalSymlinks failure (ELOOP,
// ENOENT, EIO) is treated as a hard error to prevent symlink bypass
// when running without a sandbox.
func ResolvePath(path string) (string, error) {
	abs, err := filepath.Abs(path) // Abs calls Clean internally
	if err != nil {
		return "", fmt.Errorf("abs path: %w", err)
	}

	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !os.IsPermission(err) {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	// Permission denied — expected inside the OS sandbox where
	// lstat on ancestor directories (e.g. /Users) is denied.
	// Fall back to the cleaned absolute path; the sandbox enforces
	// symlink confinement at the kernel level.
	return abs, nil
}

// RejectSensitivePath checks whether a session-relative path passes
// through sensitive directories. It rejects paths with dot-prefixed
// components (e.g., .ssh, .gnupg, .config) or the env.d credential
// directory. The .wtmcp-data prefix is exempted since exported plugin
// artifacts there are intentional.
//
// Accepts both relative and absolute paths. For absolute paths, pass
// sessionDir so the session prefix can be stripped before checking
// components. The path is cleaned before inspection.
//
// Trade-off: this also blocks legitimate dotfiles like .github/
// workflows or .editorconfig. Users can copy such files to
// .wtmcp-data/<plugin>/ as a workaround.
func RejectSensitivePath(path, sessionDir string) error {
	cleaned := filepath.Clean(path)
	rel := cleaned
	if filepath.IsAbs(cleaned) && sessionDir != "" {
		var err error
		rel, err = filepath.Rel(sessionDir, cleaned)
		if err != nil {
			return fmt.Errorf("cannot make path relative to session directory")
		}
	}
	if rel == "." {
		return nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("path escapes session directory")
	}

	parts := strings.Split(rel, string(os.PathSeparator))
	for i, part := range parts {
		if part == ".wtmcp-data" {
			return nil
		}
		if strings.HasPrefix(part, ".") && part != "." {
			return fmt.Errorf("path contains sensitive component %q", part)
		}
		if part == "env.d" && i < len(parts)-1 {
			return fmt.Errorf("path contains credential directory %q", part)
		}
	}
	return nil
}
