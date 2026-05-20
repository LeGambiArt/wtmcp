package fileio //nolint:gosec // G116: test file intentionally contains bidi control characters for testing

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	outputDir := t.TempDir()
	tmpDir := t.TempDir()
	return Config{
		OutputDir: outputDir,
		TmpDir:    tmpDir,
		SizeLimit: defaultSizeLimit,
	}
}

// --- Path confinement (P0) ---

func TestWriteFile_PathConfinement(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
		errMsg  string
	}{
		{"relative path", "data.json", false, ""},
		{"subdirectory path", "sub/dir/data.json", false, ""},
		{"traversal dotdot", "../../etc/passwd", true, "path escapes"},
		{"traversal after clean", "sub/../../../etc/shadow", true, "path escapes"},
		{"absolute path", "/etc/passwd", true, "path escapes"},
		{"null byte in path", "file\x00.json", true, "null byte"},
		{"null byte in middle", "sub/fi\x00le/x", true, "null byte"},
		{"empty path", "", true, "path is required"},
		{"dot path", ".", true, "invalid path"},
		{"dotdot path", "..", true, "invalid path"},
		{"slash only", "/", true, "path escapes"},
		{"very long path", strings.Repeat("a", 4096), true, "filename too long"},
		{"max length filename", strings.Repeat("a", 255), false, ""},
		{"over max length filename", strings.Repeat("a", 256), true, "filename too long"},
		{"control char", "file\x01name.json", true, "control character"},
		{"DEL char", "file\x7fname.json", true, "control character"},
		{"RTL override U+202E", "file" + "\u202e" + "name.json", true, "invisible character"},
		{"zero-width joiner U+200D", "file" + "\u200d" + "name.json", true, "invisible character"},
		{"Windows reserved CON.txt", "CON.txt", true, "reserved name"},
		{"colon in filename", "file:name.json", true, "colon"},
		{"clean to dot", "sub/..", true, "invalid path"},
		{"clean to dot nested", "a/b/../..", true, "invalid path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			req := WriteRequest{
				Path:       tt.path,
				Content:    "test content",
				HasContent: true,
			}
			result, err := WriteFile(cfg, req)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errMsg)
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errMsg)
				}
				// Verify no file was created outside outputDir.
				verifyNoEscape(t, cfg.OutputDir)
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result == nil {
					t.Fatal("result is nil")
				}
				if !filepath.IsAbs(result.Path) {
					t.Errorf("result path should be absolute: %q", result.Path)
				}
			}
		})
	}
}

func TestWriteFile_SymlinkParentEscape(t *testing.T) {
	cfg := testConfig(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(cfg.OutputDir, "evil")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	_, err := WriteFile(cfg, WriteRequest{
		Path: "evil/data.json", Content: "pwned", HasContent: true,
	})
	if err == nil {
		t.Fatal("expected error for symlink escape")
	}
	if !strings.Contains(err.Error(), "path escapes") {
		t.Errorf("wrong error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "data.json")); err == nil {
		t.Fatal("file was created outside outputDir")
	}
}

func TestWriteFile_SymlinkOutputDirItself(t *testing.T) {
	realDir := t.TempDir()
	linkDir := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	cfg := Config{OutputDir: linkDir, TmpDir: t.TempDir(), SizeLimit: defaultSizeLimit}
	result, err := WriteFile(cfg, WriteRequest{
		Path: "ok.json", Content: "data", HasContent: true,
	})
	if err != nil {
		t.Fatalf("symlinked outputDir should work: %v", err)
	}
	if _, err := os.Stat(result.Path); err != nil {
		t.Fatalf("file does not exist at returned path: %v", err)
	}
}

func TestWriteFile_SiblingDirectoryPrefix(t *testing.T) {
	root := t.TempDir()
	outputDir := filepath.Join(root, "plugin")
	siblingDir := filepath.Join(root, "plugin-evil")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(siblingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(siblingDir, filepath.Join(outputDir, "escape")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	cfg := Config{OutputDir: outputDir, TmpDir: t.TempDir(), SizeLimit: defaultSizeLimit}
	_, err := WriteFile(cfg, WriteRequest{
		Path: "escape/secret.txt", Content: "pwned", HasContent: true,
	})
	if err == nil {
		t.Fatal("expected error for sibling directory escape")
	}
	if _, err := os.Stat(filepath.Join(siblingDir, "secret.txt")); err == nil {
		t.Fatal("file was created in sibling directory")
	}
}

// --- source_path validation (P0) ---

func TestWriteFile_SourcePath(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(cfg Config) string // returns source_path
		setContent bool                    // also set Content field (mutual exclusion test)
		wantErr    bool
		errMsg     string
	}{
		{
			name: "valid regular file",
			setup: func(cfg Config) string {
				p := filepath.Join(cfg.TmpDir, "valid.tmp")
				_ = os.WriteFile(p, []byte("data"), 0o600)
				return p
			},
		},
		{
			name: "outside tmpdir",
			setup: func(cfg Config) string {
				d := filepath.Join(cfg.TmpDir, "..", "outside-"+filepath.Base(cfg.TmpDir))
				_ = os.MkdirAll(d, 0o700)
				p := filepath.Join(d, "outside.tmp")
				_ = os.WriteFile(p, []byte("data"), 0o600)
				return p
			},
			wantErr: true, errMsg: "source_path must be within",
		},
		{
			name: "nonexistent file",
			setup: func(cfg Config) string {
				return filepath.Join(cfg.TmpDir, "nonexistent.tmp")
			},
			wantErr: true, errMsg: "open",
		},
		{
			name: "directory not file",
			setup: func(cfg Config) string {
				d := filepath.Join(cfg.TmpDir, "subdir")
				_ = os.MkdirAll(d, 0o700)
				return d
			},
			wantErr: true, errMsg: "not a regular file",
		},
		{
			name: "empty 0-byte file",
			setup: func(cfg Config) string {
				p := filepath.Join(cfg.TmpDir, "empty.tmp")
				_ = os.WriteFile(p, []byte{}, 0o600)
				return p
			},
		},
		{
			name: "both content and source_path",
			setup: func(cfg Config) string {
				p := filepath.Join(cfg.TmpDir, "both.tmp")
				_ = os.WriteFile(p, []byte("data"), 0o600)
				return p
			},
			setContent: true,
			wantErr:    true, errMsg: "cannot specify both",
		},
		{
			name:    "neither content nor source_path",
			setup:   func(_ Config) string { return "" },
			wantErr: true, errMsg: "content or source_path is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			sourcePath := tt.setup(cfg)

			req := WriteRequest{Path: "output.bin", SourcePath: sourcePath}
			if tt.setContent {
				req.Content = "inline"
				req.HasContent = true
			}

			result, err := WriteFile(cfg, req)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q", tt.errMsg)
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result == nil {
					t.Fatal("result is nil")
				}
			}
		})
	}
}

func TestWriteFile_SourcePath_SymlinkEscape(t *testing.T) {
	cfg := testConfig(t)
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.tmp")
	_ = os.WriteFile(target, []byte("secret"), 0o600)
	link := filepath.Join(cfg.TmpDir, "symlink.tmp")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	_, err := WriteFile(cfg, WriteRequest{
		Path: "out.bin", SourcePath: link,
	})
	if err == nil {
		t.Fatal("expected error for symlink source_path")
	}
}

func TestWriteFile_SourcePath_HardlinkRejection(t *testing.T) {
	// Hardlinks have Nlink > 1 and must be rejected.
	// Create source and hardlink within a single tmpdir (same filesystem).
	cfg := testConfig(t)
	original := filepath.Join(cfg.TmpDir, "original.tmp")
	_ = os.WriteFile(original, []byte("data"), 0o600)
	hardlink := filepath.Join(cfg.TmpDir, "hardlink.tmp")
	if err := os.Link(original, hardlink); err != nil {
		t.Skipf("cannot create hardlink: %v", err)
	}
	_, err := WriteFile(cfg, WriteRequest{
		Path: "out.bin", SourcePath: hardlink,
	})
	if err == nil {
		t.Fatal("expected error for hardlink source_path (Nlink > 1)")
	}
	if !strings.Contains(err.Error(), "hardlink detected") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestWriteFile_SourcePath_CleanupOnSuccess(t *testing.T) {
	cfg := testConfig(t)
	src := filepath.Join(cfg.TmpDir, "upload.tmp")
	if err := os.WriteFile(src, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := WriteFile(cfg, WriteRequest{
		Path: "result.bin", SourcePath: src,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source file should be deleted after successful write")
	}
}

func TestWriteFile_SourcePath_PreservedOnFailure(t *testing.T) {
	cfg := testConfig(t)
	src := filepath.Join(cfg.TmpDir, "upload.tmp")
	if err := os.WriteFile(src, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := WriteFile(cfg, WriteRequest{
		Path: "../../escape.bin", SourcePath: src,
	})
	if err == nil {
		t.Fatal("expected traversal error")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("source file should be preserved on write failure for retry")
	}
}

func TestWriteFile_SourcePath_CleanupRace(t *testing.T) {
	cfg := testConfig(t)
	src := filepath.Join(cfg.TmpDir, "ephemeral.tmp")
	if err := os.WriteFile(src, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	TestHookBeforeSourceCleanup = func() {
		_ = os.Remove(src)
	}
	defer func() { TestHookBeforeSourceCleanup = nil }()

	_, err := WriteFile(cfg, WriteRequest{
		Path: "result.bin", SourcePath: src,
	})
	if err != nil {
		t.Fatalf("should tolerate source deletion during cleanup: %v", err)
	}
}

func TestWriteFile_SourcePath_BodyEncodingIgnored(t *testing.T) {
	cfg := testConfig(t)
	content := []byte{0x00, 0xFF, 0xDE, 0xAD}
	src := filepath.Join(cfg.TmpDir, "binary.tmp")
	if err := os.WriteFile(src, content, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := WriteFile(cfg, WriteRequest{
		Path: "output.bin", SourcePath: src, Encoding: "base64",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(result.Path)
	if string(got) != string(content) {
		t.Errorf("encoding should be ignored for source_path; got %x, want %x", got, content)
	}
}

// --- HasContent zero-byte file tests (P0) ---

func TestWriteFile_HasContent_EmptyString(t *testing.T) {
	cfg := testConfig(t)
	result, err := WriteFile(cfg, WriteRequest{
		Path: "empty.txt", Content: "", HasContent: true,
	})
	if err != nil {
		t.Fatalf("HasContent=true with empty content should write zero-byte file: %v", err)
	}
	info, err := os.Stat(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Errorf("expected zero-byte file, got %d bytes", info.Size())
	}
}

func TestWriteFile_NoHasContent_EmptyString(t *testing.T) {
	cfg := testConfig(t)
	_, err := WriteFile(cfg, WriteRequest{
		Path: "empty.txt", Content: "", HasContent: false,
	})
	if err == nil {
		t.Fatal("HasContent=false with empty Content should error")
	}
	if !strings.Contains(err.Error(), "content or source_path is required") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestWriteFile_HasContent_WithSourcePath_MutualExclusion(t *testing.T) {
	cfg := testConfig(t)
	src := filepath.Join(cfg.TmpDir, "src.tmp")
	_ = os.WriteFile(src, []byte("data"), 0o600)
	_, err := WriteFile(cfg, WriteRequest{
		Path: "out.txt", Content: "", HasContent: true, SourcePath: src,
	})
	if err == nil {
		t.Fatal("HasContent=true with SourcePath should error")
	}
	if !strings.Contains(err.Error(), "cannot specify both") {
		t.Errorf("wrong error: %v", err)
	}
}

// --- Permissions (P1) ---

func TestWriteFile_Permissions(t *testing.T) {
	tests := []struct {
		name    string
		perms   string
		wantErr bool
		errMsg  string
		want    os.FileMode
	}{
		{"default empty", "", false, "", 0o600},
		{"custom 0640", "0640", false, "", 0o640},
		{"world readable 0644", "0644", true, "world access", 0},
		{"world writable 0666", "0666", true, "world access", 0},
		{"world executable 0601", "0601", true, "world access", 0},
		{"setuid 4755", "4755", true, "setuid/setgid/sticky", 0},
		{"setgid 2755", "2755", true, "setuid/setgid/sticky", 0},
		{"sticky 1755", "1755", true, "setuid/setgid/sticky", 0},
		{"owner only 0400", "0400", false, "", 0o400},
		{"invalid non-octal", "abc", true, "must be octal", 0},
		{"decimal ambiguity 600", "600", false, "", 0o600},
		{"no owner-read 0200", "0200", true, "owner-read required", 0},
		{"no owner-read 0000", "0000", true, "owner-read required", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			result, err := WriteFile(cfg, WriteRequest{
				Path: "test.txt", Content: "data", HasContent: true,
				Permissions: tt.perms,
			})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q", tt.errMsg)
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				info, err := os.Stat(result.Path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != tt.want {
					t.Errorf("permissions = %o, want %o", info.Mode().Perm(), tt.want)
				}
			}
		})
	}
}

// --- Atomic write (P2) ---

func TestWriteFile_AtomicBasic(t *testing.T) {
	cfg := testConfig(t)
	result, err := WriteFile(cfg, WriteRequest{
		Path: "data.json", Content: `{"key":"value"}`, HasContent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"key":"value"}` {
		t.Errorf("content mismatch: got %q", got)
	}
}

func TestWriteFile_AtomicOverwrite(t *testing.T) {
	cfg := testConfig(t)
	req := WriteRequest{Path: "data.json", Content: "v1", HasContent: true}
	if _, err := WriteFile(cfg, req); err != nil {
		t.Fatal(err)
	}
	req.Content = "v2"
	result, err := WriteFile(cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(result.Path)
	if string(got) != "v2" {
		t.Errorf("overwrite failed: got %q, want %q", got, "v2")
	}
}

func TestWriteFile_TempFileCleanup(t *testing.T) {
	cfg := testConfig(t)
	if _, err := WriteFile(cfg, WriteRequest{
		Path: "data.json", Content: "test", HasContent: true,
	}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(cfg.OutputDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempFilePrefix) {
			t.Errorf("stale temp file found: %s", e.Name())
		}
	}
}

func TestWriteFile_ConcurrentSamePath(t *testing.T) {
	cfg := testConfig(t)
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := range 10 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			content := strings.Repeat(string(rune('A'+n)), 100) //nolint:gosec // test only, n is 0-9
			if _, err := WriteFile(cfg, WriteRequest{
				Path: "shared.txt", Content: content, HasContent: true,
			}); err == nil {
				successes.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if successes.Load() == 0 {
		t.Fatal("no concurrent writes succeeded")
	}
	got, err := os.ReadFile(filepath.Join(cfg.OutputDir, "shared.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 {
		t.Errorf("file should contain exactly 100 bytes from one writer, got %d", len(got))
	}
	first := got[0]
	for _, b := range got {
		if b != first {
			t.Fatal("file contains mixed content from multiple writers — not atomic")
		}
	}
}

func TestWriteFile_ConcurrentDifferentPaths(t *testing.T) {
	cfg := testConfig(t)
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := range 10 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			path := filepath.Join("concurrent", strings.Repeat(string(rune('a'+n)), 1)+".txt") //nolint:gosec // test only
			if _, err := WriteFile(cfg, WriteRequest{
				Path: path, Content: "data", HasContent: true,
			}); err == nil {
				successes.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if successes.Load() != 10 {
		t.Errorf("expected all 10 writes to succeed, got %d", successes.Load())
	}
	entries, _ := os.ReadDir(filepath.Join(cfg.OutputDir, "concurrent"))
	if len(entries) != 10 {
		t.Errorf("expected 10 files, got %d", len(entries))
	}
}

func TestWriteFile_DirectoryPermissions(t *testing.T) {
	cfg := testConfig(t)
	if _, err := WriteFile(cfg, WriteRequest{
		Path: "sub/dir/data.json", Content: "test", HasContent: true,
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(cfg.OutputDir, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != dirMode {
		t.Errorf("directory permissions = %o, want %o", info.Mode().Perm(), dirMode)
	}
}

func TestWriteFile_AtomicCrashAfterSync(t *testing.T) {
	// Simulates a crash between f.Sync() and os.Rename() via
	// TestHookAfterSync. The original file should be intact and no
	// stale temp files should remain (deferred cleanup fires).
	// Must NOT use t.Parallel().
	cfg := testConfig(t)

	// Write the original file first.
	if _, err := WriteFile(cfg, WriteRequest{
		Path: "data.json", Content: "original", HasContent: true,
	}); err != nil {
		t.Fatal(err)
	}

	// Attempt overwrite with hook that panics to simulate crash.
	// We recover from the panic to verify state.
	TestHookAfterSync = func() {
		panic("simulated crash after sync")
	}
	func() {
		defer func() {
			recover() //nolint:errcheck // expected panic
			TestHookAfterSync = nil
		}()
		_, _ = WriteFile(cfg, WriteRequest{ //nolint:gosec // will panic
			Path: "data.json", Content: "overwrite", HasContent: true,
		})
	}()

	// Original file should still have "original" content.
	got, err := os.ReadFile(filepath.Join(cfg.OutputDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Errorf("original file corrupted: got %q, want %q", got, "original")
	}

	// No stale temp files should remain.
	entries, _ := os.ReadDir(cfg.OutputDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempFilePrefix) {
			t.Errorf("stale temp file after simulated crash: %s", e.Name())
		}
	}
}

func TestWriteFile_PermissionsOverwrite(t *testing.T) {
	cfg := testConfig(t)
	if _, err := WriteFile(cfg, WriteRequest{
		Path: "perms.txt", Content: "v1", HasContent: true,
		Permissions: "0600",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := WriteFile(cfg, WriteRequest{
		Path: "perms.txt", Content: "v2", HasContent: true,
		Permissions: "0640",
	})
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(result.Path)
	if info.Mode().Perm() != 0o640 {
		t.Errorf("permissions after overwrite = %o, want 0640", info.Mode().Perm())
	}
}

// --- Encoding (P2) ---

func TestWriteFile_Encoding(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		encoding string
		wantErr  bool
		errMsg   string
		wantData string
	}{
		{"text hello", "hello world", "text", false, "", "hello world"},
		{"text utf8 multibyte", "\xe4\xb8\xad\xe6\x96\x87", "text", false, "", "\xe4\xb8\xad\xe6\x96\x87"},
		{"text invalid utf8", "\xff\xfe", "text", true, "not valid UTF-8", ""},
		{"base64 valid", base64.StdEncoding.EncodeToString([]byte{0xDE, 0xAD}), "base64", false, "", "\xDE\xAD"},
		{"base64 invalid padding", "not-valid-base64!!!", "base64", true, "invalid base64", ""},
		{"base64 empty", "", "base64", false, "", ""},
		{"unknown encoding", "data", "gzip", true, "unsupported encoding", ""},
		{"case sensitive Base64", "data", "Base64", true, "unsupported encoding", ""},
		{"text default empty encoding", "hello", "", false, "", "hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			result, err := WriteFile(cfg, WriteRequest{
				Path: "test.bin", Content: tt.content, HasContent: true,
				Encoding: tt.encoding,
			})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q", tt.errMsg)
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				got, _ := os.ReadFile(result.Path)
				if string(got) != tt.wantData {
					t.Errorf("data mismatch: got %x, want %x", got, tt.wantData)
				}
			}
		})
	}
}

// --- Size limits (P2) ---

func TestWriteFile_SizeLimits(t *testing.T) {
	t.Run("under limit", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.SizeLimit = 1024
		_, err := WriteFile(cfg, WriteRequest{
			Path: "small.txt", Content: "hello", HasContent: true,
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("at limit", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.SizeLimit = 100
		_, err := WriteFile(cfg, WriteRequest{
			Path: "exact.txt", Content: strings.Repeat("x", 100), HasContent: true,
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("over limit", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.SizeLimit = 100
		_, err := WriteFile(cfg, WriteRequest{
			Path: "big.txt", Content: strings.Repeat("x", 101), HasContent: true,
		})
		if err == nil {
			t.Fatal("expected size limit error")
		}
	})

	t.Run("zero length", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.SizeLimit = 100
		_, err := WriteFile(cfg, WriteRequest{
			Path: "zero.txt", Content: "", HasContent: true,
		})
		if err != nil {
			t.Fatalf("zero-length should succeed: %v", err)
		}
	})

	t.Run("custom limit", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.SizeLimit = 10
		_, err := WriteFile(cfg, WriteRequest{
			Path: "toobig.txt", Content: strings.Repeat("x", 20), HasContent: true,
		})
		if err == nil {
			t.Fatal("expected size limit error")
		}
	})
}

// --- TOCTOU symlink race (P0) ---

func TestWriteFile_TOCTOU_SymlinkRace(t *testing.T) {
	// This test uses TestHookAfterMkdir to inject a symlink between
	// MkdirAll and the post-mkdir EvalSymlinks re-check.
	// Must NOT use t.Parallel().

	t.Run("intermediate directory replaced", func(t *testing.T) {
		cfg := testConfig(t)
		outside := t.TempDir()

		TestHookAfterMkdir = func() {
			subDir := filepath.Join(cfg.OutputDir, "sub")
			_ = os.RemoveAll(subDir)
			_ = os.Symlink(outside, subDir)
		}
		defer func() { TestHookAfterMkdir = nil }()

		_, err := WriteFile(cfg, WriteRequest{
			Path: "sub/data.json", Content: "pwned", HasContent: true,
		})
		if err == nil {
			t.Fatal("expected error for symlink race")
		}
		if _, err := os.Stat(filepath.Join(outside, "data.json")); err == nil {
			t.Fatal("file was created outside outputDir via symlink race")
		}
	})

	t.Run("parent directory replaced", func(t *testing.T) {
		cfg := testConfig(t)
		outside := t.TempDir()

		TestHookAfterMkdir = func() {
			parentDir := filepath.Join(cfg.OutputDir, "deep", "nested")
			_ = os.RemoveAll(filepath.Join(cfg.OutputDir, "deep"))
			_ = os.MkdirAll(filepath.Join(cfg.OutputDir, "deep"), 0o700)
			_ = os.Symlink(outside, parentDir)
		}
		defer func() { TestHookAfterMkdir = nil }()

		_, err := WriteFile(cfg, WriteRequest{
			Path: "deep/nested/data.json", Content: "pwned", HasContent: true,
		})
		if err == nil {
			t.Fatal("expected error for parent symlink race")
		}
		if _, err := os.Stat(filepath.Join(outside, "data.json")); err == nil {
			t.Fatal("file was created outside outputDir via parent symlink race")
		}
	})
}

// --- Directory creation (P2) ---

func TestWriteFile_Mkdir(t *testing.T) {
	t.Run("mkdir true nested", func(t *testing.T) {
		cfg := testConfig(t)
		_, err := WriteFile(cfg, WriteRequest{
			Path: "a/b/c/file.txt", Content: "data", HasContent: true,
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("mkdir false parent missing", func(t *testing.T) {
		cfg := testConfig(t)
		mkdirFalse := false
		_, err := WriteFile(cfg, WriteRequest{
			Path: "missing/file.txt", Content: "data", HasContent: true,
			Mkdir: &mkdirFalse,
		})
		if err == nil {
			t.Fatal("expected error when mkdir=false and parent missing")
		}
	})

	t.Run("mkdir true existing", func(t *testing.T) {
		cfg := testConfig(t)
		_ = os.MkdirAll(filepath.Join(cfg.OutputDir, "existing"), 0o700)
		_, err := WriteFile(cfg, WriteRequest{
			Path: "existing/file.txt", Content: "data", HasContent: true,
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("mkdir nil defaults to true", func(t *testing.T) {
		cfg := testConfig(t)
		_, err := WriteFile(cfg, WriteRequest{
			Path: "auto/created/file.txt", Content: "data", HasContent: true,
			Mkdir: nil,
		})
		if err != nil {
			t.Fatalf("nil mkdir should default to true: %v", err)
		}
		if _, err := os.Stat(filepath.Join(cfg.OutputDir, "auto", "created")); err != nil {
			t.Fatal("directories should have been created when mkdir is nil")
		}
	})

	t.Run("mkdir explicit false", func(t *testing.T) {
		cfg := testConfig(t)
		mkdirFalse := false
		_, err := WriteFile(cfg, WriteRequest{
			Path: "nope/file.txt", Content: "data", HasContent: true,
			Mkdir: &mkdirFalse,
		})
		if err == nil {
			t.Fatal("mkdir=false should fail when parent missing")
		}
	})
}

// --- Error message and audit log (P2) ---

func TestWriteFile_ErrorMessageNoResolvedPath(t *testing.T) {
	cfg := testConfig(t)
	_, err := WriteFile(cfg, WriteRequest{
		Path: "../../escape.txt", Content: "data", HasContent: true,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), cfg.OutputDir) {
		t.Errorf("error message should not contain outputDir path: %v", err)
	}
}

// --- ReadFile tests (P3) ---

func TestReadFile_Basic(t *testing.T) {
	cfg := testConfig(t)
	writeResult, err := WriteFile(cfg, WriteRequest{
		Path: "data.json", Content: `{"key":"value"}`, HasContent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	readResult, err := ReadFile(cfg, ReadRequest{Path: "data.json"})
	if err != nil {
		t.Fatal(err)
	}
	if readResult.Content != `{"key":"value"}` {
		t.Errorf("content mismatch: got %q", readResult.Content)
	}
	if readResult.Path != writeResult.Path {
		t.Errorf("path mismatch: read=%q, write=%q", readResult.Path, writeResult.Path)
	}
}

func TestReadFile_Base64(t *testing.T) {
	cfg := testConfig(t)
	data := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	writeResult, _ := WriteFile(cfg, WriteRequest{
		Path:       "binary.bin",
		Content:    base64.StdEncoding.EncodeToString(data),
		HasContent: true, Encoding: "base64",
	})
	if writeResult == nil {
		t.Fatal("write failed")
	}
	readResult, err := ReadFile(cfg, ReadRequest{
		Path: "binary.bin", Encoding: "base64",
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(readResult.Content)
	if string(decoded) != string(data) {
		t.Errorf("round-trip failed: got %x, want %x", decoded, data)
	}
}

func TestReadFile_Errors(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{"file not found", "nonexistent.txt", "file not found"},
		{"traversal nonexistent target", "../../etc/passwd", "file not found"},
		{"empty path", "", "path is required"},
		{"null byte", "file\x00.txt", "null byte"},
		{"dot path", ".", "invalid path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			_, err := ReadFile(cfg, ReadRequest{Path: tt.path})
			if err == nil {
				t.Fatalf("expected error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestReadFile_TraversalExistingTarget(t *testing.T) {
	// Tests that the prefix check fires for existing-but-outside files.
	// The traversal-nonexistent test above only hits EvalSymlinks ENOENT.
	// This test creates a real file in a sibling dir to exercise the
	// confinement logic (resolveReadPath line 318).
	root := t.TempDir()
	outputDir := filepath.Join(root, "plugin")
	siblingDir := filepath.Join(root, "sibling")
	_ = os.MkdirAll(outputDir, 0o700)
	_ = os.MkdirAll(siblingDir, 0o700)
	_ = os.WriteFile(filepath.Join(siblingDir, "secret.txt"), []byte("secret"), 0o600)

	cfg := Config{OutputDir: outputDir, TmpDir: t.TempDir(), SizeLimit: defaultSizeLimit}
	_, err := ReadFile(cfg, ReadRequest{Path: "../sibling/secret.txt"})
	if err == nil {
		t.Fatal("expected error for read traversal to existing file")
	}
	if !strings.Contains(err.Error(), "path escapes") {
		t.Errorf("wrong error: expected 'path escapes', got %v", err)
	}
}

func TestReadFile_SymlinkEscape(t *testing.T) {
	cfg := testConfig(t)
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	_ = os.WriteFile(target, []byte("secret"), 0o600)

	if err := os.Symlink(target, filepath.Join(cfg.OutputDir, "link.txt")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	_, err := ReadFile(cfg, ReadRequest{Path: "link.txt"})
	if err == nil {
		t.Fatal("expected error for symlink escape")
	}
	if !strings.Contains(err.Error(), "path escapes") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestReadFile_DirectoryNotFile(t *testing.T) {
	cfg := testConfig(t)
	_ = os.MkdirAll(filepath.Join(cfg.OutputDir, "subdir"), 0o700)
	_, err := ReadFile(cfg, ReadRequest{Path: "subdir"})
	if err == nil {
		t.Fatal("expected error for directory")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestReadFile_CaseSensitiveEncoding(t *testing.T) {
	cfg := testConfig(t)
	_, _ = WriteFile(cfg, WriteRequest{
		Path: "data.txt", Content: "hello", HasContent: true,
	})
	_, err := ReadFile(cfg, ReadRequest{Path: "data.txt", Encoding: "Base64"})
	if err == nil {
		t.Fatal("case-insensitive encoding should be rejected")
	}
	if !strings.Contains(err.Error(), "unsupported encoding") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestReadFile_SizeLimit(t *testing.T) {
	cfg := testConfig(t)
	cfg.SizeLimit = 10
	_, _ = WriteFile(Config{OutputDir: cfg.OutputDir, TmpDir: cfg.TmpDir, SizeLimit: 1024},
		WriteRequest{Path: "big.txt", Content: strings.Repeat("x", 100), HasContent: true})

	_, err := ReadFile(cfg, ReadRequest{Path: "big.txt"})
	if err == nil {
		t.Fatal("expected size limit error on read")
	}
	if !strings.Contains(err.Error(), "exceeds size limit") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestReadFile_NoOutputDir(t *testing.T) {
	_, err := ReadFile(Config{}, ReadRequest{Path: "file.txt"})
	if err == nil {
		t.Fatal("expected error for empty outputDir")
	}
	if !strings.Contains(err.Error(), "no output directory") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestWriteFile_NoOutputDir(t *testing.T) {
	_, err := WriteFile(Config{}, WriteRequest{
		Path: "file.txt", Content: "data", HasContent: true,
	})
	if err == nil {
		t.Fatal("expected error for empty outputDir")
	}
	if !strings.Contains(err.Error(), "no output directory") {
		t.Errorf("wrong error: %v", err)
	}
}

// --- CleanupStaleTempFiles ---

func TestCleanupStaleTempFiles(t *testing.T) {
	dir := t.TempDir()

	// Create a stale temp file (old).
	stale := filepath.Join(dir, "wtmcp-fw-stale.tmp")
	_ = os.WriteFile(stale, []byte("old"), 0o600)
	past := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(stale, past, past)

	// Create a fresh temp file (recent).
	fresh := filepath.Join(dir, "wtmcp-fw-fresh.tmp")
	_ = os.WriteFile(fresh, []byte("new"), 0o600)

	// Create a non-matching file.
	other := filepath.Join(dir, "data.json")
	_ = os.WriteFile(other, []byte("keep"), 0o600)

	CleanupStaleTempFiles(dir)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale temp file should have been cleaned up")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh temp file should not be cleaned up")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("non-matching file should not be cleaned up")
	}
}

func TestCleanupStaleTempFiles_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	CleanupStaleTempFiles(dir) // should not panic
}

func TestCleanupStaleTempFiles_NonexistentDir(_ *testing.T) {
	CleanupStaleTempFiles("/nonexistent/path") // should not panic
}

// --- Returned path correctness ---

func TestWriteFile_ReturnedPath(t *testing.T) {
	cfg := testConfig(t)
	result, err := WriteFile(cfg, WriteRequest{
		Path: "sub/data.json", Content: "test", HasContent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(result.Path) {
		t.Errorf("returned path should be absolute: %q", result.Path)
	}
	if _, err := os.Stat(result.Path); err != nil {
		t.Errorf("file does not exist at returned path: %v", err)
	}
	if result.Size != 4 {
		t.Errorf("size = %d, want 4", result.Size)
	}
}

// --- Helper ---

func verifyNoEscape(t *testing.T, outputDir string) {
	t.Helper()
	parent := filepath.Dir(outputDir)
	_ = filepath.WalkDir(parent, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // skip errors and dirs
		}
		if !strings.HasPrefix(path, outputDir) {
			t.Errorf("file created outside outputDir: %s", path)
		}
		return nil
	})
}

// --- Lazy outputDir creation (Phase 5) ---

func TestWriteFile_LazyOutputDirCreation(t *testing.T) {
	// outputDir does not exist — WriteFile should create it lazily.
	root := t.TempDir()
	outputDir := filepath.Join(root, "lazy", "plugin")
	cfg := Config{
		OutputDir: outputDir,
		TmpDir:    t.TempDir(),
		SizeLimit: defaultSizeLimit,
	}

	result, err := WriteFile(cfg, WriteRequest{
		Path: "data.json", Content: "hello", HasContent: true,
	})
	if err != nil {
		t.Fatalf("WriteFile should lazily create outputDir: %v", err)
	}
	if _, err := os.Stat(result.Path); err != nil {
		t.Fatalf("file should exist at returned path: %v", err)
	}
	got, _ := os.ReadFile(result.Path)
	if string(got) != "hello" {
		t.Errorf("content = %q, want hello", got)
	}
}

func TestReadFile_NonexistentOutputDir(t *testing.T) {
	// outputDir does not exist — ReadFile should return "file not found".
	cfg := Config{
		OutputDir: filepath.Join(t.TempDir(), "nonexistent", "plugin"),
		TmpDir:    t.TempDir(),
		SizeLimit: defaultSizeLimit,
	}

	_, err := ReadFile(cfg, ReadRequest{Path: "data.json"})
	if err == nil {
		t.Fatal("expected error for non-existent outputDir")
	}
	if !strings.Contains(err.Error(), "file not found") {
		t.Errorf("error = %q, want 'file not found'", err.Error())
	}
}
