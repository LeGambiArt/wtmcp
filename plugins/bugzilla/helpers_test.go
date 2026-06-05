package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/LeGambiArt/wtmcp/pkg/handler"
)

func TestParseBugID(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    int
		wantErr string
	}{
		{"int via float64", float64(12345), 12345, ""},
		{"string", "12345", 12345, ""},
		{"string with spaces", " 12345 ", 12345, ""},
		{"json.Number", json.Number("99"), 99, ""},
		{"boundary 1", float64(1), 1, ""},
		{"zero", float64(0), 0, "must be positive"},
		{"negative", float64(-5), 0, "must be positive"},
		{"float", float64(12345.5), 0, "whole number"},
		{"non-numeric string", "abc", 0, "must be numeric"},
		{"empty string", "", 0, "must be numeric"},
		{"nil", nil, 0, "required"},
		{"bool", true, 0, "must be a number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseBugID(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestValidateBugIDs(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int
		wantErr string
	}{
		{"single", "123", 1, ""},
		{"multiple", "1,2,3", 3, ""},
		{"with spaces", "1, 2, 3", 3, ""},
		{"dedup", "1,2,1,3", 3, ""},
		{"empty commas", "1,,3", 2, ""},
		{"trailing comma", "1,2,", 2, ""},
		{"leading comma", ",1,2", 2, ""},
		{"empty", "", 0, "required"},
		{"whitespace only", "   ", 0, "required"},
		{"non-numeric", "abc", 0, "must be numeric"},
		{"negative", "-1", 0, "must be positive"},
		{"zero", "0", 0, "must be positive"},
		{"mixed valid invalid", "1,abc,3", 0, "must be numeric"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateBugIDs(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tt.want {
				t.Errorf("got %d IDs, want %d", len(got), tt.want)
			}
		})
	}
}

func TestValidateBugIDsTooMany(t *testing.T) {
	parts := make([]string, maxBugIDs+1)
	for i := range parts {
		parts[i] = strconv.Itoa(i + 1)
	}
	_, err := validateBugIDs(strings.Join(parts, ","))
	if err == nil {
		t.Fatal("expected error for too many IDs")
	}
	if !strings.Contains(err.Error(), "too many") {
		t.Errorf("error = %q, want 'too many'", err.Error())
	}
}

func TestCapLimit(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		max   int
		want  int
	}{
		{"normal", 10, 200, 10},
		{"over max", 500, 200, 200},
		{"zero", 0, 200, 200},
		{"negative", -1, 200, 200},
		{"exact max", 200, 200, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := capLimit(tt.limit, tt.max)
			if got != tt.want {
				t.Errorf("capLimit(%d, %d) = %d, want %d", tt.limit, tt.max, got, tt.want)
			}
		})
	}
}

func TestBugURL(t *testing.T) {
	cfg.bugzillaURL = "https://bugzilla.example.com"
	got := bugURL(12345)
	want := "https://bugzilla.example.com/show_bug.cgi?id=12345"
	if got != want {
		t.Errorf("bugURL(12345) = %q, want %q", got, want)
	}
}

func TestBugBrief(t *testing.T) {
	cfg.bugzillaURL = "https://bugzilla.example.com"
	full := map[string]any{
		"id":          float64(123),
		"summary":     "test bug",
		"status":      "NEW",
		"priority":    "high",
		"severity":    "urgent",
		"product":     "TestProduct",
		"component":   "TestComponent",
		"assigned_to": "user@example.com",
		"resolution":  "FIXED",
		"description": "long description that should be stripped",
		"cc":          []string{"a@b.com"},
	}
	brief := bugBrief(full)
	if brief["id"] != 123 {
		t.Errorf("id = %v, want 123", brief["id"])
	}
	if brief["url"] != "https://bugzilla.example.com/show_bug.cgi?id=123" {
		t.Errorf("url = %v", brief["url"])
	}
	if _, ok := brief["description"]; ok {
		t.Error("brief should not include description")
	}
	if _, ok := brief["cc"]; ok {
		t.Error("brief should not include cc")
	}
	if brief["resolution"] != "FIXED" {
		t.Errorf("resolution = %v, want FIXED", brief["resolution"])
	}
}

func TestBugBriefMissingFields(t *testing.T) {
	cfg.bugzillaURL = "https://bugzilla.example.com"
	brief := bugBrief(map[string]any{})
	if brief["id"] != 0 {
		t.Errorf("id = %v, want 0", brief["id"])
	}
	if _, ok := brief["resolution"]; ok {
		t.Error("empty resolution should be omitted")
	}
}

func TestParseTime(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{"rfc3339 utc", "2026-05-06T10:00:00Z", "2026-05-06T10:00:00Z", ""},
		{"rfc3339 offset", "2026-05-06T10:00:00+05:30", "2026-05-06T04:30:00Z", ""},
		{"date only", "2026-05-06", "2026-05-06T00:00:00Z", ""},
		{"empty", "", "", "empty"},
		{"whitespace", "   ", "", "empty"},
		{"invalid", "not-a-date", "", "invalid date"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTime(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseTime(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDryRunPreview(t *testing.T) {
	preview := dryRunPreview("POST", "/rest/bug", map[string]any{"summary": "test"})
	if preview["dry_run"] != true {
		t.Error("dry_run should be true")
	}
	if preview["method"] != "POST" {
		t.Errorf("method = %v", preview["method"])
	}
	if preview["path"] != "/rest/bug" {
		t.Errorf("path = %v", preview["path"])
	}
	body, ok := preview["body"].(map[string]any)
	if !ok {
		t.Fatal("body missing or wrong type")
	}
	if body["summary"] != "test" {
		t.Errorf("body.summary = %v", body["summary"])
	}
}

func TestDryRunPreviewNilBody(t *testing.T) {
	preview := dryRunPreview("GET", "/test", nil)
	if _, ok := preview["body"]; ok {
		t.Error("nil body should be omitted")
	}
}

func TestParseAPIError(t *testing.T) {
	t.Run("valid bugzilla error", func(t *testing.T) {
		resp := &handler.HTTPResponse{
			Status: 404,
			Body:   mustJSON(t, map[string]any{"error": true, "message": "Bug #999 does not exist.", "code": 101}),
		}
		err := parseAPIError(resp)
		he := &handler.Error{}
		ok := errors.As(err, &he)
		if !ok {
			t.Fatalf("expected *handler.Error, got %T", err)
		}
		if he.Code != "bugzilla_404" {
			t.Errorf("code = %q", he.Code)
		}
		if !strings.Contains(he.Message, "does not exist") {
			t.Errorf("message = %q", he.Message)
		}
	})

	t.Run("html error page", func(t *testing.T) {
		resp := &handler.HTTPResponse{
			Status: 500,
			Body:   json.RawMessage(`<html>Internal Server Error</html>`),
		}
		err := parseAPIError(resp)
		he := &handler.Error{}
		ok := errors.As(err, &he)
		if !ok {
			t.Fatalf("expected *handler.Error, got %T", err)
		}
		if he.Code != "http_500" {
			t.Errorf("code = %q", he.Code)
		}
	})

	t.Run("empty body", func(t *testing.T) {
		resp := &handler.HTTPResponse{
			Status: 502,
			Body:   nil,
		}
		err := parseAPIError(resp)
		he := &handler.Error{}
		ok := errors.As(err, &he)
		if !ok {
			t.Fatalf("expected *handler.Error, got %T", err)
		}
		if he.Code != "http_502" {
			t.Errorf("code = %q", he.Code)
		}
	})
}

func TestIsDryRun(t *testing.T) {
	if !isDryRun(nil) {
		t.Error("nil should be dry_run=true")
	}
	tr := true
	if !isDryRun(&tr) {
		t.Error("*true should be dry_run=true")
	}
	fa := false
	if isDryRun(&fa) {
		t.Error("*false should not be dry_run")
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		name string
		file string
		id   int
		want string
	}{
		{"normal", "patch.diff", 42, "42_patch.diff"},
		{"path traversal", "../../etc/passwd", 1, "1_passwd"},
		{"empty", "", 5, "5_attachment"},
		{"dot", ".", 5, "5_attachment"},
		{"dotdot", "..", 5, "5_attachment"},
		{"null byte", "file\x00.txt", 5, "5_attachment"},
		{"long name", strings.Repeat("A", 300) + ".txt", 7, "7_" + strings.Repeat("A", 196) + ".txt"},
		{"long name no ext", strings.Repeat("B", 300), 8, "8_" + strings.Repeat("B", 200)},
		{"long ext only", "." + strings.Repeat("C", 250), 9, "9_." + strings.Repeat("C", 199)},
		{"long ext with stem", "x." + strings.Repeat("D", 250), 10, "10_x." + strings.Repeat("D", 198)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeFilename(tt.file, tt.id)
			if got != tt.want {
				t.Errorf("sanitizeFilename(%q, %d) = %q, want %q", tt.file, tt.id, got, tt.want)
			}
		})
	}
}

func TestConfineRead(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("normal", func(t *testing.T) {
		got, err := confineRead(testFile, dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		resolvedDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(got, resolvedDir) {
			t.Errorf("result %q not under dir %q", got, resolvedDir)
		}
	})

	t.Run("outside allowed", func(t *testing.T) {
		other := t.TempDir()
		otherFile := filepath.Join(other, "secret.txt")
		if err := os.WriteFile(otherFile, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := confineRead(otherFile, dir)
		if err == nil {
			t.Fatal("expected error for path outside allowed dirs")
		}
	})

	t.Run("nonexistent", func(t *testing.T) {
		_, err := confineRead(filepath.Join(dir, "nope.txt"), dir)
		if err == nil {
			t.Fatal("expected error for nonexistent file")
		}
	})

	t.Run("directory", func(t *testing.T) {
		_, err := confineRead(dir, dir)
		if err == nil {
			t.Fatal("expected error for directory")
		}
	})

	t.Run("empty path", func(t *testing.T) {
		_, err := confineRead("", dir)
		if err == nil {
			t.Fatal("expected error for empty path")
		}
	})
}

func TestParseIntIDs(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    int
		wantErr string
	}{
		{"valid", []string{"1", "2", "3"}, 3, ""},
		{"with spaces", []string{" 1 ", " 2 "}, 2, ""},
		{"skip empty", []string{"1", "", "3"}, 2, ""},
		{"non-numeric", []string{"abc"}, 0, "must be numeric"},
		{"negative", []string{"-1"}, 0, "must be positive"},
		{"zero", []string{"0"}, 0, "must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseIntIDs(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tt.want {
				t.Errorf("got %d IDs, want %d", len(got), tt.want)
			}
		})
	}
}

func TestInitConfig(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]string{
			"bugzilla_url": "https://bz.example.com/",
			"_output_dir":  "/tmp/out",
			"_session_dir": "/tmp/session",
		})
		err := initConfig(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.bugzillaURL != "https://bz.example.com" {
			t.Errorf("bugzillaURL = %q (trailing slash should be stripped)", cfg.bugzillaURL)
		}
		if cfg.outputDir != "/tmp/out" {
			t.Errorf("outputDir = %q", cfg.outputDir)
		}
	})

	t.Run("missing url", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]string{})
		err := initConfig(raw)
		if err == nil {
			t.Fatal("expected error for missing bugzilla_url")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		err := initConfig(json.RawMessage(`{invalid`))
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})
}
