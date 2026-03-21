package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateGoogleCredentialsFile(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantErr     bool
		errContains string
	}{
		{
			name: "valid installed credentials",
			content: `{
				"installed": {
					"client_id": "test-client-id.apps.googleusercontent.com",
					"client_secret": "test-secret-123",
					"auth_uri": "https://accounts.google.com/o/oauth2/auth",
					"token_uri": "https://oauth2.googleapis.com/token"
				}
			}`,
			wantErr: false,
		},
		{
			name: "web credentials (should fail)",
			content: `{
				"web": {
					"client_id": "test-client-id.apps.googleusercontent.com",
					"client_secret": "test-secret-123"
				}
			}`,
			wantErr:     true,
			errContains: "Desktop app",
		},
		{
			name:        "empty file",
			content:     "",
			wantErr:     true,
			errContains: "empty",
		},
		{
			name:        "invalid JSON",
			content:     `{invalid json`,
			wantErr:     true,
			errContains: "invalid JSON",
		},
		{
			name: "missing client_id",
			content: `{
				"installed": {
					"client_secret": "test-secret-123"
				}
			}`,
			wantErr:     true,
			errContains: "client_id is missing",
		},
		{
			name: "missing client_secret",
			content: `{
				"installed": {
					"client_id": "test-client-id.apps.googleusercontent.com"
				}
			}`,
			wantErr:     true,
			errContains: "client_secret is missing",
		},
		{
			name: "no installed or web",
			content: `{
				"other": {
					"client_id": "test-client-id"
				}
			}`,
			wantErr:     true,
			errContains: "no 'installed' credentials",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file
			tmpDir := t.TempDir()
			tmpFile := filepath.Join(tmpDir, "credentials.json")

			if tt.content != "" {
				if err := os.WriteFile(tmpFile, []byte(tt.content), 0o600); err != nil {
					t.Fatalf("failed to write test file: %v", err)
				}
			}

			// Test validation
			err := validateGoogleCredentialsFile(tmpFile)

			if tt.wantErr {
				if err == nil {
					t.Errorf("validateGoogleCredentialsFile() expected error, got nil")
					return
				}
				if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("validateGoogleCredentialsFile() error = %v, want error containing %q", err, tt.errContains)
				}
			} else if err != nil {
				t.Errorf("validateGoogleCredentialsFile() unexpected error = %v", err)
			}
		})
	}
}

func TestValidateGoogleCredentialsFile_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	nonExistentPath := filepath.Join(tmpDir, "does-not-exist.json")

	err := validateGoogleCredentialsFile(nonExistentPath)
	if err == nil {
		t.Error("validateGoogleCredentialsFile() expected error for non-existent file, got nil")
	}
	if !contains(err.Error(), "not found") {
		t.Errorf("validateGoogleCredentialsFile() error = %v, want error containing 'not found'", err)
	}
}

func TestCredentialsDirForGoogle(t *testing.T) {
	// Save original env var and globalWorkdir
	origEnv := os.Getenv("GOOGLE_CREDENTIALS_DIR")
	origWorkdir := globalWorkdir
	defer func() {
		if origEnv != "" {
			_ = os.Setenv("GOOGLE_CREDENTIALS_DIR", origEnv)
		} else {
			_ = os.Unsetenv("GOOGLE_CREDENTIALS_DIR")
		}
		globalWorkdir = origWorkdir
	}()

	t.Run("uses env var when set", func(t *testing.T) {
		testDir := "/custom/creds/path"
		_ = os.Setenv("GOOGLE_CREDENTIALS_DIR", testDir)
		globalWorkdir = ""

		dir, err := credentialsDirForGoogle()
		if err != nil {
			t.Fatalf("credentialsDirForGoogle() error = %v", err)
		}
		if dir != testDir {
			t.Errorf("credentialsDirForGoogle() = %q, want %q", dir, testDir)
		}
	})

	t.Run("uses workdir flag when env var not set", func(t *testing.T) {
		_ = os.Unsetenv("GOOGLE_CREDENTIALS_DIR")
		globalWorkdir = "/tmp/test-workdir"

		dir, err := credentialsDirForGoogle()
		if err != nil {
			t.Fatalf("credentialsDirForGoogle() error = %v", err)
		}
		expected := "/tmp/test-workdir/credentials/google"
		if dir != expected {
			t.Errorf("credentialsDirForGoogle() = %q, want %q", dir, expected)
		}
	})

	t.Run("env var takes precedence over workdir flag", func(t *testing.T) {
		testDir := "/custom/creds/path"
		_ = os.Setenv("GOOGLE_CREDENTIALS_DIR", testDir)
		globalWorkdir = "/tmp/test-workdir"

		dir, err := credentialsDirForGoogle()
		if err != nil {
			t.Fatalf("credentialsDirForGoogle() error = %v", err)
		}
		if dir != testDir {
			t.Errorf("credentialsDirForGoogle() = %q, want %q (env var should take precedence)", dir, testDir)
		}
	})

	t.Run("uses default when neither env var nor workdir set", func(t *testing.T) {
		_ = os.Unsetenv("GOOGLE_CREDENTIALS_DIR")
		globalWorkdir = ""

		dir, err := credentialsDirForGoogle()
		if err != nil {
			t.Fatalf("credentialsDirForGoogle() error = %v", err)
		}
		if !contains(dir, ".config/wtmcp/credentials/google") {
			t.Errorf("credentialsDirForGoogle() = %q, want path containing .config/wtmcp/credentials/google", dir)
		}
	})
}

func TestCopyFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create source file
	srcPath := filepath.Join(tmpDir, "source.json")
	content := []byte(`{"installed":{"client_id":"test","client_secret":"secret"}}`)
	if err := os.WriteFile(srcPath, content, 0o600); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	t.Run("copy to new directory", func(t *testing.T) {
		dstPath := filepath.Join(tmpDir, "subdir", "dest.json")
		if err := copyFile(srcPath, dstPath); err != nil {
			t.Fatalf("copyFile failed: %v", err)
		}

		// Verify destination file exists and has correct content
		got, err := os.ReadFile(dstPath) //nolint:gosec // test file path
		if err != nil {
			t.Fatalf("Failed to read destination file: %v", err)
		}

		if string(got) != string(content) {
			t.Errorf("Content mismatch: got %q, want %q", got, content)
		}

		// Verify permissions
		info, err := os.Stat(dstPath)
		if err != nil {
			t.Fatalf("Failed to stat destination file: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("Wrong permissions: got %o, want 0600", info.Mode().Perm())
		}
	})

	t.Run("source file not found", func(t *testing.T) {
		nonExistent := filepath.Join(tmpDir, "does-not-exist.json")
		dstPath := filepath.Join(tmpDir, "dest2.json")
		err := copyFile(nonExistent, dstPath)
		if err == nil {
			t.Error("Expected error for non-existent source file, got nil")
		}
	})
}

// contains is a helper to check if a string contains a substring.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
