// Package audit provides structured security audit logging for tool
// invocations, HTTP proxy requests, and authentication events.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ctxKey struct{}

// WithCorrelationID returns a new context with a UUIDv7 correlation ID.
func WithCorrelationID(ctx context.Context) context.Context {
	id := uuid.Must(uuid.NewV7()).String()
	return context.WithValue(ctx, ctxKey{}, id)
}

// CorrelationID extracts the correlation ID from context, or empty string.
func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// Logger writes structured audit events to a dedicated log file.
type Logger struct {
	logger     *slog.Logger
	scrubber   *Scrubber
	file       *os.File
	maxErrLen  int
	configured bool
}

// Config holds audit logging configuration.
type Config struct {
	LogFile     string   `yaml:"log_file"`
	Stdout      bool     `yaml:"stdout"`
	ScrubFields []string `yaml:"scrub_fields"`
}

// New creates an audit logger. If cfg.LogFile is empty, audit logging
// is disabled and all methods are no-ops.
func New(cfg Config) (*Logger, error) {
	if cfg.LogFile == "" && !cfg.Stdout {
		return &Logger{}, nil
	}

	scrubFields := cfg.ScrubFields
	if len(scrubFields) == 0 {
		scrubFields = DefaultScrubFields
	}

	var handler slog.Handler
	var file *os.File
	if cfg.LogFile != "" {
		var err error
		file, err = os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		handler = slog.NewJSONHandler(file, &slog.HandlerOptions{})
	}

	if cfg.Stdout {
		stdoutHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{})
		if handler != nil {
			handler = &multiHandler{handlers: []slog.Handler{handler, stdoutHandler}}
		} else {
			handler = stdoutHandler
		}
	}

	logger := slog.New(handler).With(slog.Int("pid", os.Getpid()))

	return &Logger{
		logger:     logger,
		scrubber:   NewScrubber(scrubFields),
		file:       file,
		maxErrLen:  500,
		configured: true,
	}, nil
}

// ToolCall logs a tool invocation event.
func (l *Logger) ToolCall(ctx context.Context, plugin, tool string, params json.RawMessage, duration time.Duration, errMsg string) {
	if l == nil || !l.configured {
		return
	}

	attrs := []slog.Attr{
		slog.String("event", "tool_call"),
		slog.String("correlation_id", CorrelationID(ctx)),
		slog.String("plugin", plugin),
		slog.String("tool", tool),
		slog.String("duration", duration.String()),
	}

	if params != nil {
		scrubbed := l.scrubber.ScrubJSON(params)
		attrs = append(attrs, slog.String("params", string(scrubbed)))
	}

	if errMsg != "" {
		attrs = append(attrs, slog.String("error", truncate(l.scrubber.ScrubString(errMsg), l.maxErrLen)))
		attrs = append(attrs, slog.Bool("is_error", true))
	}

	l.log(ctx, attrs)
}

// Elicitation logs a user confirmation prompt event.
func (l *Logger) Elicitation(ctx context.Context, plugin, tool, action string) {
	if l == nil || !l.configured {
		return
	}

	attrs := []slog.Attr{
		slog.String("event", "elicitation"),
		slog.String("correlation_id", CorrelationID(ctx)),
		slog.String("plugin", plugin),
		slog.String("tool", tool),
		slog.String("action", action),
	}

	l.log(ctx, attrs)
}

// HTTPRequest logs an HTTP proxy request event.
func (l *Logger) HTTPRequest(ctx context.Context, plugin, method, host, path string, status int, size int64) {
	if l == nil || !l.configured {
		return
	}

	attrs := []slog.Attr{
		slog.String("event", "http_request"),
		slog.String("correlation_id", CorrelationID(ctx)),
		slog.String("plugin", plugin),
		slog.String("method", method),
		slog.String("host", host),
		slog.String("path", path),
		slog.Int("status", status),
		slog.Int64("size", size),
	}

	l.log(ctx, attrs)
}

// ControlAction logs a control-plane action (e.g., plugin reload).
func (l *Logger) ControlAction(ctx context.Context, action, pluginName, status, errMsg string) {
	if l == nil || !l.configured {
		return
	}

	attrs := []slog.Attr{
		slog.String("event", "control_action"),
		slog.String("correlation_id", CorrelationID(ctx)),
		slog.String("action", action),
		slog.String("plugin", pluginName),
		slog.String("status", status),
	}

	if errMsg != "" {
		attrs = append(attrs, slog.String("error", truncate(l.scrubber.ScrubString(errMsg), l.maxErrLen)))
	}

	l.log(ctx, attrs)
}

// ScrubErrorText redacts sensitive patterns in a plain error string.
func (l *Logger) ScrubErrorText(s string) string {
	if l == nil || l.scrubber == nil {
		return s
	}
	return l.scrubber.ScrubString(s)
}

// Close flushes and closes the audit log file (if any).
func (l *Logger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}

func (l *Logger) log(ctx context.Context, attrs []slog.Attr) {
	l.logger.LogAttrs(ctx, slog.LevelInfo, "audit", attrs...)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// multiHandler fans out log records to multiple handlers.
type multiHandler struct {
	handlers []slog.Handler
}

func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, record slog.Record) error {
	var firstErr error
	for _, h := range m.handlers {
		if err := h.Handle(ctx, record.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: handlers}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: handlers}
}

// DefaultScrubFields is the default set of field name patterns to scrub
// in audit logs. Intentionally broad (e.g., "key" matches issue_key)
// because over-redaction is acceptable in audit output. The elicitation
// scrubber in server.go uses a tighter list to avoid hiding values
// users need to see in confirmation prompts.
var DefaultScrubFields = []string{
	"password", "passwd", "token", "secret", "key", "credential",
	"auth", "api_key", "apikey", "private_key", "bearer",
	"refresh_token", "access_token", "client_secret", "session_id",
	"passcode", "passphrase", "signing", "certificate", "jwt",
}

// Scrubber redacts sensitive values from JSON payloads.
type Scrubber struct {
	fields      []string
	scrubValues bool
}

// NewScrubber creates a scrubber that matches both field names and
// value patterns (JWTs, high-entropy strings). Use for audit logs
// where over-redaction is acceptable.
func NewScrubber(fields []string) *Scrubber {
	return newScrubber(fields, true)
}

// NewFieldScrubber creates a scrubber that only matches field names,
// skipping value-based heuristics. Use for user-facing display where
// over-redaction hides information the user needs to see.
func NewFieldScrubber(fields []string) *Scrubber {
	return newScrubber(fields, false)
}

func newScrubber(fields []string, scrubValues bool) *Scrubber {
	lower := make([]string, len(fields))
	for i, f := range fields {
		lower[i] = strings.ToLower(f)
	}
	return &Scrubber{fields: lower, scrubValues: scrubValues}
}

// ScrubJSON redacts values of sensitive fields in a JSON payload.
func (s *Scrubber) ScrubJSON(data json.RawMessage) json.RawMessage {
	// Try object first.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err == nil {
		return s.scrubObject(obj, data)
	}

	// Try array.
	var arr []json.RawMessage
	if err := json.Unmarshal(data, &arr); err == nil {
		return s.scrubArray(arr, data)
	}

	return data
}

func (s *Scrubber) scrubObject(obj map[string]json.RawMessage, original json.RawMessage) json.RawMessage {
	changed := false
	for key, val := range obj {
		if s.isSensitive(key) {
			obj[key] = json.RawMessage(`"[REDACTED]"`)
			changed = true
			continue
		}
		if s.scrubValues && s.isSensitiveValue(val) {
			obj[key] = json.RawMessage(`"[REDACTED]"`)
			changed = true
			continue
		}
		scrubbed := s.ScrubJSON(val)
		if string(scrubbed) != string(val) {
			obj[key] = scrubbed
			changed = true
		}
	}

	if !changed {
		return original
	}

	result, err := json.Marshal(obj)
	if err != nil {
		return original
	}
	return result
}

func (s *Scrubber) scrubArray(arr []json.RawMessage, original json.RawMessage) json.RawMessage {
	changed := false
	for i, elem := range arr {
		if s.scrubValues && s.isSensitiveValue(elem) {
			arr[i] = json.RawMessage(`"[REDACTED]"`)
			changed = true
			continue
		}
		scrubbed := s.ScrubJSON(elem)
		if string(scrubbed) != string(elem) {
			arr[i] = scrubbed
			changed = true
		}
	}

	if !changed {
		return original
	}

	result, err := json.Marshal(arr)
	if err != nil {
		return original
	}
	return result
}

// knownTokenPrefixes are prefixes for well-known API token formats.
// A word matching a prefix with total length >= minTokenPrefixLen is
// redacted regardless of entropy.
var knownTokenPrefixes = []string{
	"ghp_", "gho_", "ghu_", "ghs_", // GitHub
	"glpat-",         // GitLab
	"sk-",            // OpenAI, Stripe
	"xoxb-", "xoxp-", // Slack
	"AKIA", // AWS
}

const minTokenPrefixLen = 20

// ScrubString redacts sensitive patterns in a plain string (e.g.,
// error messages that may embed tokens or credentials).
func (s *Scrubber) ScrubString(str string) string {
	if strings.HasPrefix(str, "eyJ") && len(str) > 32 {
		return "[REDACTED]"
	}
	words := strings.Fields(str)
	for i, w := range words {
		switch {
		case strings.HasPrefix(w, "eyJ") && len(w) > 32:
			words[i] = "[REDACTED]"
		case hasKnownTokenPrefix(w):
			words[i] = "[REDACTED]"
		case s.scrubValues && (strings.Contains(w, "://") || strings.Contains(w, "?")):
			scrubbed := s.scrubURLParams(w)
			words[i] = scrubbed
			if scrubbed == w && len(w) >= 32 && isHighEntropy(w) {
				words[i] = "[REDACTED]"
			}
		case len(w) >= 32 && isHighEntropy(w):
			words[i] = "[REDACTED]"
		}
	}
	return strings.Join(words, " ")
}

func hasKnownTokenPrefix(w string) bool {
	if len(w) < minTokenPrefixLen {
		return false
	}
	for _, prefix := range knownTokenPrefixes {
		if strings.HasPrefix(w, prefix) {
			return true
		}
	}
	return false
}

// scrubURLParams redacts sensitive query parameter values, userinfo
// credentials, and fragment-embedded tokens in URLs.
func (s *Scrubber) scrubURLParams(word string) string {
	u, err := url.Parse(word)
	if err != nil {
		return word
	}

	changed := false

	// Redact userinfo credentials (https://user:token@host/path)
	if u.User != nil {
		u.User = url.UserPassword("[REDACTED]", "[REDACTED]")
		changed = true
	}

	// Redact sensitive query parameter values
	if u.RawQuery != "" {
		q := u.Query()
		for key, vals := range q {
			if s.isSensitive(key) {
				for j := range vals {
					q[key][j] = "[REDACTED]"
					changed = true
				}
				continue
			}
			for j, v := range vals {
				decoded, decErr := url.QueryUnescape(v)
				if decErr != nil {
					decoded = v
				}
				if (strings.HasPrefix(decoded, "eyJ") && len(decoded) > 32) ||
					(len(decoded) >= 32 && isHighEntropy(decoded)) ||
					hasKnownTokenPrefix(decoded) {
					q[key][j] = "[REDACTED]"
					changed = true
				}
			}
		}
		if changed {
			u.RawQuery = q.Encode()
		}
	}

	// Redact fragment-embedded tokens (OAuth2 implicit flow:
	// https://example.com/callback#access_token=xxx&token_type=bearer)
	if u.Fragment != "" {
		fragParams, parseErr := url.ParseQuery(u.Fragment)
		if parseErr == nil && len(fragParams) > 0 {
			fragChanged := false
			for key, vals := range fragParams {
				if s.isSensitive(key) {
					for j := range vals {
						fragParams[key][j] = "[REDACTED]"
						fragChanged = true
					}
					continue
				}
				for j, v := range vals {
					if (strings.HasPrefix(v, "eyJ") && len(v) > 32) ||
						(len(v) >= 32 && isHighEntropy(v)) ||
						hasKnownTokenPrefix(v) {
						fragParams[key][j] = "[REDACTED]"
						fragChanged = true
					}
				}
			}
			if fragChanged {
				encoded := fragParams.Encode()
				decoded, _ := url.QueryUnescape(encoded)
				u.Fragment = decoded
				u.RawFragment = encoded
				changed = true
			}
		}
	}

	if !changed {
		return word
	}
	return strings.ReplaceAll(u.String(), "%5BREDACTED%5D", "[REDACTED]")
}

func (s *Scrubber) isSensitive(fieldName string) bool {
	lower := strings.ToLower(fieldName)
	for _, pattern := range s.fields {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func (s *Scrubber) isSensitiveValue(val json.RawMessage) bool {
	var str string
	if err := json.Unmarshal(val, &str); err != nil {
		return false
	}
	if strings.HasPrefix(str, "eyJ") && len(str) > 32 {
		return true
	}
	if len(str) >= 32 && isHighEntropy(str) {
		return true
	}
	return false
}

func isHighEntropy(s string) bool {
	if len(s) < 32 {
		return false
	}
	alnumCount := 0
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '+' || c == '/' || c == '=' || c == '-' || c == '_' {
			alnumCount++
		}
	}
	return float64(alnumCount)/float64(len(s)) > 0.9
}
