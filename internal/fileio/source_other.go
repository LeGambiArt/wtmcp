//go:build !linux && !windows

package fileio

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// readSourcePath reads content from a source file in the plugin's
// tmpdir using stat-based validation. This is the fallback for
// non-Linux platforms where /proc/self/fd is unavailable.
//
// This is weaker than the Linux implementation (Lstat-then-Open has
// a TOCTOU gap where the file could be swapped between stat and open).
// The Linux implementation at source_linux.go uses O_NOFOLLOW + Fstat +
// /proc/self/fd readlink to close this gap. Production deployments
// should use Linux with the sandbox enabled for full protection.
//
// Returns the data and the verified cleanup path (for the caller to
// delete after a successful write). Does NOT delete the source.
func readSourcePath(resolvedTmpDir, sourcePath string, sizeLimit int64) ([]byte, string, error) {
	if resolvedTmpDir == "" {
		return nil, "", fmt.Errorf("no tmpdir configured")
	}

	// Resolve and check containment.
	resolved, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return nil, "", fmt.Errorf("resolve source: %w", err)
	}
	if !strings.HasPrefix(resolved, resolvedTmpDir+string(os.PathSeparator)) {
		return nil, "", fmt.Errorf("source_path must be within plugin tmpdir")
	}

	// Check file type, size, and hardlinks.
	info, err := os.Lstat(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("stat source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("not a regular file")
	}
	if info.Size() > sizeLimit {
		return nil, "", fmt.Errorf("exceeds size limit")
	}

	// Check Nlink on platforms that expose it via Sys().
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
		return nil, "", fmt.Errorf("hardlink detected (nlink=%d)", st.Nlink)
	}

	// Read with bounded size (defense against concurrent append).
	f, err := os.Open(resolved) //nolint:gosec // path validated above
	if err != nil {
		return nil, "", fmt.Errorf("open source: %w", err)
	}
	defer f.Close() //nolint:errcheck,gosec

	data, err := io.ReadAll(io.LimitReader(f, sizeLimit+1))
	if err != nil {
		return nil, "", fmt.Errorf("read source: %w", err)
	}
	if int64(len(data)) > sizeLimit {
		return nil, "", fmt.Errorf("exceeds size limit")
	}

	return data, sourcePath, nil
}
