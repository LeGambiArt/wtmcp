package pathutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolvePath_RealPath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ResolvePath(file)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("ResolvePath = %q, want %q", got, want)
	}
}

func TestResolvePath_Symlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	got, err := ResolvePath(link)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("ResolvePath(symlink) = %q, want %q (resolved target)", got, want)
	}
}

func TestResolvePath_DanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "dangling")
	if err := os.Symlink("/nonexistent/target", link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	_, err := ResolvePath(link)
	if err == nil {
		t.Fatal("expected error for dangling symlink")
	}
}

func TestResolvePath_NonexistentPath(t *testing.T) {
	_, err := ResolvePath("/tmp/nonexistent-path-for-test-" + t.Name())
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

func TestResolvePath_SymlinkLoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink loops behave differently on Windows")
	}

	dir := t.TempDir()
	linkA := filepath.Join(dir, "loop-a")
	linkB := filepath.Join(dir, "loop-b")
	if err := os.Symlink(linkB, linkA); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	if err := os.Symlink(linkA, linkB); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	_, err := ResolvePath(linkA)
	if err == nil {
		t.Fatal("expected error for symlink loop")
	}
}

func TestResolvePath_PermissionDeniedFallback(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission denial as root")
	}
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on Windows")
	}

	outer := t.TempDir()
	inner := filepath.Join(outer, "restricted", "subdir")
	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(inner, "file.txt")
	if err := os.WriteFile(target, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Remove read+execute from the restricted directory so lstat
	// on subdir fails with EPERM/EACCES — simulating the sandbox.
	restricted := filepath.Join(outer, "restricted")
	if err := os.Chmod(restricted, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(restricted, 0o700) }) //nolint:gosec // directory needs execute bit

	got, err := ResolvePath(target)
	if err != nil {
		t.Fatalf("ResolvePath should fall back on permission error, got: %v", err)
	}

	want, err := filepath.Abs(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("ResolvePath = %q, want %q (Clean+Abs fallback)", got, want)
	}
}

func TestResolvePath_Directory(t *testing.T) {
	dir := t.TempDir()

	got, err := ResolvePath(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("ResolvePath(dir) = %q, want %q", got, want)
	}
}
