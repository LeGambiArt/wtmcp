package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProfileFile is a helper to drop a file into a profiles.d dir.
func writeProfileFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestLoadProfilesValid(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, dir, "code-review.yaml", `
definitions:
  code-review:
    description: "Read-only"
    allow:
      gitlab:
        - "gitlab_get_.*"
rules:
  - match: { cn: "code-review-bot" }
    profile: code-review
`)

	result, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if _, ok := result.Definitions["code-review"]; !ok {
		t.Fatalf("code-review definition not loaded")
	}
	if len(result.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(result.Rules))
	}
	if result.Rules[0].File != "code-review.yaml" {
		t.Errorf("rule.File = %q, want code-review.yaml", result.Rules[0].File)
	}
	if result.DefSource["code-review"] != "code-review.yaml" {
		t.Errorf("DefSource = %q", result.DefSource["code-review"])
	}
}

func TestLoadProfilesMergeMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, dir, "a.yaml", `
definitions:
  alpha:
    allow: { p1: [".*"] }
rules:
  - match: { cn: "a" }
    profile: alpha
`)
	writeProfileFile(t, dir, "b.yaml", `
definitions:
  beta:
    allow: { p2: [".*"] }
rules:
  - match: { cn: "b" }
    profile: beta
`)

	result, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if len(result.Definitions) != 2 {
		t.Fatalf("expected 2 definitions, got %d", len(result.Definitions))
	}
	if len(result.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(result.Rules))
	}
}

func TestLoadProfilesDuplicateName(t *testing.T) {
	dir := t.TempDir()
	// Files load in lexicographic order: a.yaml first, then b.yaml.
	writeProfileFile(t, dir, "a.yaml", `
definitions:
  dup:
    allow: { p: [".*"] }
`)
	writeProfileFile(t, dir, "b.yaml", `
definitions:
  dup:
    allow: { q: [".*"] }
`)

	result, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if !HasFatal(result.Errors) {
		t.Fatalf("expected fatal duplicate error, got %v", result.Errors)
	}
	found := false
	for _, e := range result.Errors {
		if e.File == "b.yaml" && strings.Contains(e.Message, "duplicate profile") &&
			strings.Contains(e.Message, "a.yaml") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected duplicate error naming both files, got %v", result.Errors)
	}
	// First definition wins.
	if result.DefSource["dup"] != "a.yaml" {
		t.Errorf("DefSource = %q, want a.yaml", result.DefSource["dup"])
	}
}

func TestLoadProfilesParseError(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, dir, "bad.yaml", "definitions: [this is not valid: mapping")
	writeProfileFile(t, dir, "good.yaml", `
definitions:
  ok:
    allow: { p: [".*"] }
`)

	result, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if !HasFatal(result.Errors) {
		t.Fatalf("expected fatal parse error")
	}
	// good.yaml should still load despite bad.yaml failing.
	if _, ok := result.Definitions["ok"]; !ok {
		t.Errorf("good.yaml not loaded despite bad.yaml failing")
	}
	var sawParse bool
	for _, e := range result.Errors {
		if e.File == "bad.yaml" && strings.Contains(e.Message, "parse error") {
			sawParse = true
		}
	}
	if !sawParse {
		t.Errorf("expected parse error for bad.yaml, got %v", result.Errors)
	}
}

func TestLoadProfilesMissingDir(t *testing.T) {
	result, err := LoadProfiles(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if len(result.Definitions) != 0 || len(result.Rules) != 0 {
		t.Errorf("missing dir should yield empty result")
	}
}

func TestLoadProfilesEmptyDir(t *testing.T) {
	result, err := LoadProfiles(t.TempDir())
	if err != nil {
		t.Fatalf("empty dir should not error: %v", err)
	}
	if len(result.Definitions) != 0 || len(result.Rules) != 0 {
		t.Errorf("empty dir should yield empty result")
	}
}

func TestLoadProfilesYmlExtension(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, dir, "p.yml", `
definitions:
  short:
    allow: { p: [".*"] }
`)
	// A non-yaml file should be ignored.
	writeProfileFile(t, dir, "notes.txt", "ignore me")

	result, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if _, ok := result.Definitions["short"]; !ok {
		t.Errorf(".yml file not loaded")
	}
	if len(result.Files) != 1 {
		t.Errorf("expected 1 profile file scanned, got %d: %v", len(result.Files), result.Files)
	}
}

func TestLoadProfilesRejectSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "real.yaml")
	writeProfileFile(t, filepath.Dir(target), "real.yaml", `
definitions:
  x:
    allow: { p: [".*"] }
`)
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	result, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if !HasFatal(result.Errors) {
		t.Fatalf("expected symlink to be rejected as fatal error, got %v", result.Errors)
	}
}

func TestResolveProfilesDir(t *testing.T) {
	// Default: workdir/profiles.d
	cfg := &Config{}
	got := ResolveProfilesDir(cfg, "/wd")
	if got != filepath.Join("/wd", "profiles.d") {
		t.Errorf("default = %q", got)
	}

	// Override
	cfg.Profiles.Dir = "/etc/wtmcp/profiles.d"
	got = ResolveProfilesDir(cfg, "/wd")
	if got != "/etc/wtmcp/profiles.d" {
		t.Errorf("override = %q", got)
	}
}

func TestServerTLSConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		tls     ServerTLSConfig
		wantErr bool
	}{
		{"empty ok", ServerTLSConfig{}, false},
		{"cert without key", ServerTLSConfig{CertFile: "c"}, true},
		{"key without cert", ServerTLSConfig{KeyFile: "k"}, true},
		{"cert and key ok", ServerTLSConfig{CertFile: "c", KeyFile: "k"}, false},
		{"require needs ca", ServerTLSConfig{CertFile: "c", KeyFile: "k", ClientAuth: "require"}, true},
		{"require with ca ok", ServerTLSConfig{CertFile: "c", KeyFile: "k", CAFile: "ca", ClientAuth: "require"}, false},
		{"require without cert", ServerTLSConfig{CAFile: "ca", ClientAuth: "require"}, true},
		{"request without cert", ServerTLSConfig{CAFile: "ca", ClientAuth: "request"}, true},
		{"request with cert and ca ok", ServerTLSConfig{CertFile: "c", KeyFile: "k", CAFile: "ca", ClientAuth: "request"}, false},
		{"bad client_auth", ServerTLSConfig{ClientAuth: "bogus"}, true},
		{"none ok", ServerTLSConfig{ClientAuth: "none"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tls := tt.tls
			err := tls.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
