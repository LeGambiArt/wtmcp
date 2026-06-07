package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithCorrelationID(t *testing.T) {
	ctx := context.Background()
	if id := CorrelationID(ctx); id != "" {
		t.Fatalf("expected empty, got %q", id)
	}

	ctx = WithCorrelationID(ctx)
	id := CorrelationID(ctx)
	if id == "" {
		t.Fatal("expected non-empty correlation ID")
	}
	if len(id) != 36 {
		t.Fatalf("expected UUID length 36, got %d: %q", len(id), id)
	}

	ctx2 := WithCorrelationID(ctx)
	id2 := CorrelationID(ctx2)
	if id2 == id {
		t.Fatal("expected different correlation IDs")
	}
}

func TestScrubberFieldNames(t *testing.T) {
	s := NewScrubber(DefaultScrubFields)

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "scrub password",
			input: `{"username":"alice","password":"s3cret"}`,
			want:  `{"password":"[REDACTED]","username":"alice"}`,
		},
		{
			name:  "scrub api_key",
			input: `{"api_key":"abc123","query":"test"}`,
			want:  `{"api_key":"[REDACTED]","query":"test"}`,
		},
		{
			name:  "case insensitive",
			input: `{"API_KEY":"abc123","Password":"test"}`,
			want:  `{"API_KEY":"[REDACTED]","Password":"[REDACTED]"}`,
		},
		{
			name:  "substring match",
			input: `{"jira_token_id":"tok123","name":"test"}`,
			want:  `{"jira_token_id":"[REDACTED]","name":"test"}`,
		},
		{
			name:  "nested object",
			input: `{"config":{"secret":"val"},"name":"test"}`,
			want:  `{"config":{"secret":"[REDACTED]"},"name":"test"}`,
		},
		{
			name:  "no sensitive fields",
			input: `{"name":"alice","age":30}`,
			want:  `{"name":"alice","age":30}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.ScrubJSON(json.RawMessage(tt.input))
			var gotMap, wantMap map[string]any
			if err := json.Unmarshal(got, &gotMap); err != nil {
				t.Fatalf("unmarshal got: %v", err)
			}
			if err := json.Unmarshal([]byte(tt.want), &wantMap); err != nil {
				t.Fatalf("unmarshal want: %v", err)
			}
			gotJSON, _ := json.Marshal(gotMap)
			wantJSON, _ := json.Marshal(wantMap)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("got  %s\nwant %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestScrubberValueHeuristics(t *testing.T) {
	s := NewScrubber(DefaultScrubFields)

	tests := []struct {
		name     string
		input    string
		redacted bool
	}{
		{
			name:     "JWT prefix",
			input:    `{"data":"eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.payload.sig"}`,
			redacted: true,
		},
		{
			name:     "high entropy base64",
			input:    `{"data":"aGVsbG8gd29ybGQgdGhpcyBpcyBhIGxvbmcgYmFzZTY0IHN0cmluZw=="}`,
			redacted: true,
		},
		{
			name:     "short value not redacted",
			input:    `{"data":"hello"}`,
			redacted: false,
		},
		{
			name:     "normal text not redacted",
			input:    `{"data":"this is a normal sentence with spaces and punctuation!"}`,
			redacted: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.ScrubJSON(json.RawMessage(tt.input))
			var obj map[string]any
			_ = json.Unmarshal(got, &obj)
			isRedacted := obj["data"] == "[REDACTED]"
			if isRedacted != tt.redacted {
				t.Errorf("redacted=%v, want %v (output: %s)", isRedacted, tt.redacted, got)
			}
		})
	}
}

func TestScrubberArrays(t *testing.T) {
	s := NewScrubber(DefaultScrubFields)

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "array of objects with sensitive fields",
			input: `[{"password":"s3cret","name":"alice"},{"token":"tok123","name":"bob"}]`,
			want:  `[{"password":"[REDACTED]","name":"alice"},{"token":"[REDACTED]","name":"bob"}]`,
		},
		{
			name:  "nested array in object",
			input: `{"users":[{"password":"s3cret"}]}`,
			want:  `{"users":[{"password":"[REDACTED]"}]}`,
		},
		{
			name:  "array without sensitive fields",
			input: `[{"name":"alice"},{"name":"bob"}]`,
			want:  `[{"name":"alice"},{"name":"bob"}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.ScrubJSON(json.RawMessage(tt.input))
			var gotAny, wantAny any
			_ = json.Unmarshal(got, &gotAny)
			_ = json.Unmarshal([]byte(tt.want), &wantAny)
			gotJSON, _ := json.Marshal(gotAny)
			wantJSON, _ := json.Marshal(wantAny)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("got  %s\nwant %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestScrubString(t *testing.T) {
	s := NewScrubber(DefaultScrubFields)

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "JWT in error message",
			input: "auth failed for token eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.payload.sig",
			want:  "auth failed for token [REDACTED]",
		},
		{
			name:  "high entropy string in error",
			input: "connection failed with key abcdefghijklmnopqrstuvwxyz0123456789ABCDEF",
			want:  "connection failed with key [REDACTED]",
		},
		{
			name:  "normal error message unchanged",
			input: "plugin jira not loaded",
			want:  "plugin jira not loaded",
		},
		{
			name:  "entire string is JWT",
			input: "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.payload.sig",
			want:  "[REDACTED]",
		},
		{ //nolint:gosec // G101: test data, not a real credential
			name:  "GitHub PAT token prefix",
			input: "auth error with token ghp_ABCDEFghijklmnopqrstuvwxyz1234567890",
			want:  "auth error with token [REDACTED]",
		},
		{
			name:  "GitLab PAT token prefix",
			input: "unauthorized: glpat-xxxxxxxxxxxxxxxxxxxx rejected",
			want:  "unauthorized: [REDACTED] rejected",
		},
		{ //nolint:gosec // G101: test data, not a real credential
			name:  "AWS access key prefix",
			input: "invalid credentials AKIAIOSFODNN7EXAMPLE for region us-east-1",
			want:  "invalid credentials [REDACTED] for region us-east-1",
		},
		{
			name:  "short sk- is not redacted",
			input: "using sk-1 for testing",
			want:  "using sk-1 for testing",
		},
		{
			name:  "URL with sensitive query param name",
			input: "error fetching https://api.example.com/v1?token=mysecretvalue&page=5",
			want:  "error fetching https://api.example.com/v1?page=5&token=[REDACTED]",
		},
		{
			name:  "URL with api_key param",
			input: "failed: https://api.example.com?api_key=secret123&format=json",
			want:  "failed: https://api.example.com?api_key=[REDACTED]&format=json",
		},
		{
			name:  "URL with no sensitive params unchanged",
			input: "fetching https://api.example.com/v1?page=5&limit=10",
			want:  "fetching https://api.example.com/v1?page=5&limit=10",
		},
		{
			name:  "URL with high-entropy param value",
			input: "see https://example.com?data=abcdefghijklmnopqrstuvwxyz0123456789AB",
			want:  "see https://example.com?data=[REDACTED]",
		},
		{
			name:  "query string without scheme",
			input: "error at path?token=secretvalue123 during auth",
			want:  "error at path?token=[REDACTED] during auth",
		},
		{
			name:  "Slack token prefix",
			input: "slack auth failed: xoxb-1234567890123-1234567890123-ABCDEFghijklm",
			want:  "slack auth failed: [REDACTED]",
		},
		{ //nolint:gosec // G101: test data, not a real credential
			name:  "userinfo credentials in URL",
			input: "failed to connect to https://deploy:ghp_ABCDEFghijklmnopqrstuvwxyz1234567890@api.github.com/repos",
			want:  "failed to connect to https://[REDACTED]:[REDACTED]@api.github.com/repos",
		},
		{ //nolint:gosec // G101: test data, not a real credential
			name:  "userinfo with password only",
			input: "error at https://user:secretpass@internal.corp.com/api",
			want:  "error at https://[REDACTED]:[REDACTED]@internal.corp.com/api",
		},
		{
			name:  "percent-encoded JWT in query param",
			input: "see https://example.com?token=%65%79%4AhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.payload.sig",
			want:  "see https://example.com?token=[REDACTED]",
		},
		{ //nolint:gosec // G101: test data, not a real credential
			name:  "fragment-embedded access_token",
			input: "error at https://example.com/callback#access_token=ghp_ABCDEFghijklmnopqrstuvwxyz1234567890&state=xyz",
			want:  "error at https://example.com/callback#access_token=[REDACTED]&state=xyz",
		},
		{ //nolint:gosec // G101: test data, not a real credential
			name:  "fragment with sensitive key name",
			input: "redirect to https://example.com/cb#token=secretvalue123&state=abc",
			want:  "redirect to https://example.com/cb#state=abc&token=[REDACTED]",
		},
		{
			name:  "URL unchanged by scrubURLParams falls through to entropy check",
			input: "see https://example.com/abcdefghijklmnopqrstuvwxyz0123456789ABCDEF",
			want:  "see [REDACTED]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.ScrubString(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestScrubberInvalidJSON(t *testing.T) {
	s := NewScrubber(DefaultScrubFields)
	input := json.RawMessage(`not json`)
	got := s.ScrubJSON(input)
	if string(got) != string(input) {
		t.Errorf("expected passthrough for invalid JSON, got %s", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("got %q", got)
	}
	if got := truncate("this is a longer string", 10); got != "this is a ..." {
		t.Errorf("got %q", got)
	}
}

func TestNewDisabled(t *testing.T) {
	l, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	// Should not panic on disabled logger.
	l.ToolCall(context.Background(), "test", "tool", nil, 0, "")
	l.HTTPRequest(context.Background(), "test", "GET", "example.com", "/api", 200, 100)
}

func TestIsHighEntropy(t *testing.T) {
	if isHighEntropy("hello") {
		t.Error("short string should not be high entropy")
	}
	if !isHighEntropy("abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Error("alnum string should be high entropy")
	}
	if isHighEntropy("this has spaces and punctuation!!!???") {
		t.Error("natural text should not be high entropy")
	}
}

func TestFieldScrubber_SkipsValueHeuristics(t *testing.T) {
	s := NewFieldScrubber([]string{"password", "api_key"})

	// JWT-shaped value in a non-sensitive field should NOT be redacted.
	jwt := `{"data":"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"}`
	result := s.ScrubJSON(json.RawMessage(jwt))
	if string(result) != jwt {
		t.Errorf("field scrubber should not redact JWT in non-sensitive field, got: %s", result)
	}

	// High-entropy value in a non-sensitive field should NOT be redacted.
	entropy := `{"request_id":"550e8400e29b41d4a716446655440000aabbccdd"}`
	result = s.ScrubJSON(json.RawMessage(entropy))
	if string(result) != entropy {
		t.Errorf("field scrubber should not redact high-entropy non-sensitive field, got: %s", result)
	}

	// Sensitive field name should still be redacted.
	sensitive := `{"password":"hunter2","api_key":"sk-live-abc123"}`
	result = s.ScrubJSON(json.RawMessage(sensitive))
	var obj map[string]string
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatalf("failed to unmarshal scrubbed result: %v", err)
	}
	if obj["password"] != "[REDACTED]" {
		t.Errorf("password should be redacted, got: %s", obj["password"])
	}
	if obj["api_key"] != "[REDACTED]" {
		t.Errorf("api_key should be redacted, got: %s", obj["api_key"])
	}
}

func TestNewScrubber_StillScrubbsValues(t *testing.T) {
	s := NewScrubber([]string{"password"})

	// JWT in non-sensitive field SHOULD be redacted by NewScrubber.
	jwt := `{"data":"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"}`
	result := s.ScrubJSON(json.RawMessage(jwt))
	var obj map[string]string
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if obj["data"] != "[REDACTED]" {
		t.Errorf("NewScrubber should redact JWT values, got: %s", obj["data"])
	}
}

func TestScrubberArrayStringValues(t *testing.T) {
	s := NewScrubber([]string{"password"})
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"

	t.Run("bare JWT in array", func(t *testing.T) {
		input := json.RawMessage(fmt.Sprintf(`["%s"]`, jwt))
		result := s.ScrubJSON(input)
		var arr []string
		if err := json.Unmarshal(result, &arr); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if arr[0] != "[REDACTED]" {
			t.Errorf("expected [REDACTED], got %s", arr[0])
		}
	})

	t.Run("mixed safe and sensitive", func(t *testing.T) {
		input := json.RawMessage(fmt.Sprintf(`["hello", "%s", "world"]`, jwt))
		result := s.ScrubJSON(input)
		var arr []string
		if err := json.Unmarshal(result, &arr); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if arr[0] != "hello" {
			t.Errorf("safe value changed: %s", arr[0])
		}
		if arr[1] != "[REDACTED]" {
			t.Errorf("JWT not redacted: %s", arr[1])
		}
		if arr[2] != "world" {
			t.Errorf("safe value changed: %s", arr[2])
		}
	})

	t.Run("nested array in object", func(t *testing.T) {
		input := json.RawMessage(fmt.Sprintf(`{"tokens":["%s"]}`, jwt))
		result := s.ScrubJSON(input)
		var obj map[string][]string
		if err := json.Unmarshal(result, &obj); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if obj["tokens"][0] != "[REDACTED]" {
			t.Errorf("nested JWT not redacted: %s", obj["tokens"][0])
		}
	})
}

func TestControlAction_DisabledLogger(_ *testing.T) {
	logger := &Logger{}
	logger.ControlAction(context.Background(), "reload", "test-plugin", "success", "")
}

func TestControlAction_SuccessAndError(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "audit.log")
	cfg := Config{LogFile: logFile}
	logger, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	ctx := WithCorrelationID(context.Background())
	logger.ControlAction(ctx, "reload", "jira", "success", "")
	logger.ControlAction(ctx, "reload", "github", "error", "plugin not found")
	_ = logger.Close()

	data, err := os.ReadFile(logFile) //nolint:gosec // test file
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines, got %d", len(lines))
	}

	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("parse log line: %v", err)
	}
	if entry["event"] != "control_action" {
		t.Errorf("expected event=control_action, got %v", entry["event"])
	}
	if entry["plugin"] != "jira" {
		t.Errorf("expected plugin=jira, got %v", entry["plugin"])
	}
	if entry["status"] != "success" {
		t.Errorf("expected status=success, got %v", entry["status"])
	}
	if entry["action"] != "reload" {
		t.Errorf("expected action=reload, got %v", entry["action"])
	}
}

func TestControlAction_ScrubsErrorMessages(t *testing.T) {
	cfg := Config{Stdout: true}
	logger, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}
	defer func() { _ = logger.Close() }()

	ctx := WithCorrelationID(context.Background())
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"
	logger.ControlAction(ctx, "reload", "test", "error", "auth failed: "+jwt)
}

func TestScrubErrorText(t *testing.T) {
	cfg := Config{Stdout: true}
	logger, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}
	defer func() { _ = logger.Close() }()

	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"

	t.Run("redacts JWT in error", func(t *testing.T) {
		result := logger.ScrubErrorText("auth failed: " + jwt)
		if strings.Contains(result, "eyJ") {
			t.Errorf("JWT should be redacted, got: %s", result)
		}
		if !strings.Contains(result, "[REDACTED]") {
			t.Errorf("expected [REDACTED], got: %s", result)
		}
	})

	t.Run("normal error unchanged", func(t *testing.T) {
		msg := "connection refused: dial tcp 127.0.0.1:8080"
		result := logger.ScrubErrorText(msg)
		if result != msg {
			t.Errorf("normal error should pass through, got: %s", result)
		}
	})

	t.Run("short strings not over-redacted", func(t *testing.T) {
		msg := "not found"
		result := logger.ScrubErrorText(msg)
		if result != msg {
			t.Errorf("short string should not be redacted, got: %s", result)
		}
	})

	t.Run("nil logger passthrough", func(t *testing.T) {
		var nilLogger *Logger
		msg := "error with " + jwt
		result := nilLogger.ScrubErrorText(msg)
		if result != msg {
			t.Errorf("nil logger should pass through, got: %s", result)
		}
	})
}

// ─── Fuzz Tests ──────────────────────────────────────────────────

func FuzzScrubString(f *testing.F) {
	s := NewScrubber(DefaultScrubFields)

	f.Add("normal text without secrets")
	f.Add("eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.payload.sig")
	f.Add("auth failed for token eyJhbGciOiJIUzI1NiJ9.data.sig")
	f.Add("ghp_ABCDEFghijklmnopqrstuvwxyz1234567890") //nolint:gosec
	f.Add("glpat-xxxxxxxxxxxxxxxxxxxx")
	f.Add("AKIAIOSFODNN7EXAMPLE") //nolint:gosec
	f.Add("sk-abcdefghijklmnopqrstuvwxyz123456")
	f.Add("abcdefghijklmnopqrstuvwxyz0123456789ABCDEF")
	f.Add("https://api.example.com?token=secret123&page=5")
	f.Add("https://api.example.com?api_key=secret123")
	f.Add("")
	f.Add(strings.Repeat("A", 10000))

	f.Fuzz(func(t *testing.T, input string) {
		result := s.ScrubString(input)
		_ = result

		// Invariant: JWT prefixes longer than 32 chars must be redacted
		// (the scrubber's threshold is len > 32).
		for _, word := range strings.Fields(result) {
			if strings.HasPrefix(word, "eyJ") && len(word) > 32 {
				t.Fatalf("ScrubString left JWT-like token (len %d) in output: %s",
					len(word), truncateStr(word, 50))
			}
		}
	})
}

func FuzzScrubJSON(f *testing.F) {
	s := NewScrubber(DefaultScrubFields)

	f.Add([]byte(`{"password":"secret123"}`))
	f.Add([]byte(`{"token":"abc","data":"normal"}`))
	f.Add([]byte(`{"nested":{"secret":"hidden"}}`))
	f.Add([]byte(`[{"api_key":"key1"},{"api_key":"key2"}]`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`"just a string"`))
	f.Add([]byte(`null`))
	f.Add([]byte(`not json at all`))
	f.Add([]byte(``))
	f.Add([]byte(strings.Repeat(`{"a":`, 100) + `"deep"` + strings.Repeat("}", 100)))

	f.Fuzz(func(t *testing.T, data []byte) {
		result := s.ScrubJSON(data)

		// Invariant: if input was valid JSON, output must be valid JSON
		if json.Valid(data) && len(result) > 0 && !json.Valid(result) {
			t.Fatalf("ScrubJSON produced invalid JSON from valid input.\nInput:  %s\nOutput: %s",
				truncateStr(string(data), 200), truncateStr(string(result), 200))
		}
	})
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
