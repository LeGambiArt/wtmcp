// Package pathutil provides path resolution utilities for plugin
// handlers running inside OS sandboxes (Seatbelt, Landlock).
package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
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
