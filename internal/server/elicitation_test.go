package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/plugin"
	"github.com/LeGambiArt/wtmcp/internal/ratelimit"
)

// mockElicitSession implements SessionWithElicitation for testing.
type mockElicitSession struct {
	action  mcp.ElicitationResponseAction
	err     error
	lastReq mcp.ElicitationRequest
	notif   chan mcp.JSONRPCNotification
}

func (m *mockElicitSession) SessionID() string { return "elicit-test" }
func (m *mockElicitSession) NotificationChannel() chan<- mcp.JSONRPCNotification {
	if m.notif == nil {
		m.notif = make(chan mcp.JSONRPCNotification, 10)
	}
	return m.notif
}
func (m *mockElicitSession) Initialize()       {}
func (m *mockElicitSession) Initialized() bool { return true }
func (m *mockElicitSession) RequestElicitation(_ context.Context, req mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	m.lastReq = req
	if m.err != nil {
		return nil, m.err
	}
	return &mcp.ElicitationResult{
		ElicitationResponse: mcp.ElicitationResponse{
			Action: m.action,
		},
	}, nil
}

// mockPlainSession implements ClientSession without elicitation support.
type mockPlainSession struct {
	notif chan mcp.JSONRPCNotification
}

func (m *mockPlainSession) SessionID() string { return "plain-test" }
func (m *mockPlainSession) NotificationChannel() chan<- mcp.JSONRPCNotification {
	if m.notif == nil {
		m.notif = make(chan mcp.JSONRPCNotification, 10)
	}
	return m.notif
}
func (m *mockPlainSession) Initialize()       {}
func (m *mockPlainSession) Initialized() bool { return true }

// mockNilResultSession returns nil result without error.
type mockNilResultSession struct {
	notif chan mcp.JSONRPCNotification
}

func (m *mockNilResultSession) SessionID() string { return "nil-test" }
func (m *mockNilResultSession) NotificationChannel() chan<- mcp.JSONRPCNotification {
	if m.notif == nil {
		m.notif = make(chan mcp.JSONRPCNotification, 10)
	}
	return m.notif
}
func (m *mockNilResultSession) Initialize()       {}
func (m *mockNilResultSession) Initialized() bool { return true }
func (m *mockNilResultSession) RequestElicitation(_ context.Context, _ mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	return nil, nil
}

func elicitTestServer(elicitation bool, tools []plugin.ToolDef) *mcpserver.MCPServer {
	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("test-plugin", &plugin.Manifest{
		Name:      "test-plugin",
		Execution: "oneshot",
		Tools:     tools,
	})
	mgr.SetHandle("test-plugin")

	cfg := config.DefaultConfig()
	cfg.Security.Elicitation = &elicitation

	rl, _ := ratelimit.New("1000/m", nil, "10000/m")
	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, rl, nil, true)
	return srv
}

var defaultTools = []plugin.ToolDef{
	{Name: "test_write", Description: "A write tool", Access: "write"},
	{Name: "test_read", Description: "A read tool", Access: "read"},
}

func callTool(ctx context.Context, srv *mcpserver.MCPServer, name string) mcp.JSONRPCMessage {
	msg := fmt.Sprintf(`{
		"jsonrpc": "2.0",
		"id": 1,
		"method": "tools/call",
		"params": {"name": %q, "arguments": {}}
	}`, name)
	return srv.HandleMessage(ctx, json.RawMessage(msg))
}

func callToolWithArgs(ctx context.Context, srv *mcpserver.MCPServer, name string, args map[string]any) mcp.JSONRPCMessage { //nolint:unparam // return used for consistency with callTool
	argsJSON, _ := json.Marshal(args)
	msg := fmt.Sprintf(`{
		"jsonrpc": "2.0",
		"id": 1,
		"method": "tools/call",
		"params": {"name": %q, "arguments": %s}
	}`, name, argsJSON)
	return srv.HandleMessage(ctx, json.RawMessage(msg))
}

func extractToolText(resp mcp.JSONRPCMessage) (string, bool) {
	r, ok := resp.(mcp.JSONRPCResponse)
	if !ok {
		return "", false
	}
	data, err := json.Marshal(r.Result)
	if err != nil {
		return "", false
	}
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", false
	}
	if len(result.Content) == 0 {
		return "", result.IsError
	}
	return result.Content[0].Text, result.IsError
}

func TestElicitation_WriteAccepted(t *testing.T) {
	srv := elicitTestServer(true, defaultTools)
	session := &mockElicitSession{action: mcp.ElicitationResponseActionAccept}
	ctx := srv.WithContext(context.Background(), session)

	resp := callTool(ctx, srv, "test_write")
	text, isErr := extractToolText(resp)

	// The tool should proceed past the elicitation gate. It fails
	// with a handler error (oneshot with no binary), proving the
	// gate was passed — not an elicitation error.
	if !isErr {
		t.Error("expected error from tool execution (no handler), got success")
	}
	if strings.Contains(text, "declined by user") || strings.Contains(text, "confirmation failed") {
		t.Errorf("accepted elicitation should not block, got: %s", text)
	}
}

func TestElicitation_WriteDeclined(t *testing.T) {
	srv := elicitTestServer(true, defaultTools)
	session := &mockElicitSession{action: mcp.ElicitationResponseActionDecline}
	ctx := srv.WithContext(context.Background(), session)

	resp := callTool(ctx, srv, "test_write")
	text, isErr := extractToolText(resp)

	if !isErr {
		t.Error("declined elicitation should return error")
	}
	if !strings.Contains(text, "declined by user") {
		t.Errorf("expected 'declined by user', got: %s", text)
	}
}

func TestElicitation_WriteCancelled(t *testing.T) {
	srv := elicitTestServer(true, defaultTools)
	session := &mockElicitSession{action: mcp.ElicitationResponseActionCancel}
	ctx := srv.WithContext(context.Background(), session)

	resp := callTool(ctx, srv, "test_write")
	text, isErr := extractToolText(resp)

	if !isErr {
		t.Error("cancelled elicitation should return error")
	}
	if !strings.Contains(text, "declined by user") {
		t.Errorf("expected 'declined by user', got: %s", text)
	}
}

func TestElicitation_WriteClientUnsupported(t *testing.T) {
	srv := elicitTestServer(true, defaultTools)
	session := &mockPlainSession{}
	ctx := srv.WithContext(context.Background(), session)

	resp := callTool(ctx, srv, "test_write")
	text, isErr := extractToolText(resp)

	// Default strict mode blocks write tools when client lacks
	// elicitation support.
	if !isErr {
		t.Error("strict mode (default) should block write tools when client lacks elicitation")
	}
	if !strings.Contains(text, "write tools require elicitation support") {
		t.Errorf("expected strict-mode message, got: %s", text)
	}
}

func TestElicitation_WriteClientUnsupported_NonStrict(t *testing.T) {
	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("test-plugin", &plugin.Manifest{
		Name:      "test-plugin",
		Execution: "oneshot",
		Tools:     defaultTools,
	})
	mgr.SetHandle("test-plugin")

	cfg := config.DefaultConfig()
	elicitation := true
	strict := false
	cfg.Security.Elicitation = &elicitation
	cfg.Security.ElicitationStrict = &strict

	rl, _ := ratelimit.New("1000/m", nil, "10000/m")
	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, rl, nil, true)

	session := &mockPlainSession{}
	ctx := srv.WithContext(context.Background(), session)

	resp := callTool(ctx, srv, "test_write")
	text, isErr := extractToolText(resp)

	// Non-strict: falls through to execution — tool fails with
	// handler error (not an elicitation error).
	if !isErr {
		t.Error("expected error from tool execution (no handler), got success")
	}
	if strings.Contains(text, "declined by user") || strings.Contains(text, "elicitation support") {
		t.Errorf("non-strict should fall through, got: %s", text)
	}
}

func TestElicitation_WriteError(t *testing.T) {
	srv := elicitTestServer(true, defaultTools)
	session := &mockElicitSession{err: errors.New("connection timeout")}
	ctx := srv.WithContext(context.Background(), session)

	resp := callTool(ctx, srv, "test_write")
	text, isErr := extractToolText(resp)

	if !isErr {
		t.Error("elicitation error should block the tool")
	}
	if !strings.Contains(text, "confirmation failed") {
		t.Errorf("expected 'confirmation failed', got: %s", text)
	}
}

func TestElicitation_WriteNilResult(t *testing.T) {
	srv := elicitTestServer(true, defaultTools)
	session := &mockNilResultSession{}
	ctx := srv.WithContext(context.Background(), session)

	resp := callTool(ctx, srv, "test_write")
	text, isErr := extractToolText(resp)

	if !isErr {
		t.Error("nil elicitation result should block the tool")
	}
	if !strings.Contains(text, "confirmation failed") {
		t.Errorf("expected 'confirmation failed', got: %s", text)
	}
}

func TestElicitation_ReadToolSkipped(t *testing.T) {
	srv := elicitTestServer(true, defaultTools)
	session := &mockElicitSession{action: mcp.ElicitationResponseActionDecline}
	ctx := srv.WithContext(context.Background(), session)

	resp := callTool(ctx, srv, "test_read")
	text, isErr := extractToolText(resp)

	// Read tool should NOT trigger elicitation. It proceeds to
	// execution and fails with a handler error (not a decline).
	if !isErr {
		t.Error("expected error from tool execution (no handler), got success")
	}
	if strings.Contains(text, "declined by user") {
		t.Errorf("read tools should not trigger elicitation, got: %s", text)
	}
}

func TestElicitation_DisabledConfig(t *testing.T) {
	srv := elicitTestServer(false, defaultTools)
	session := &mockElicitSession{action: mcp.ElicitationResponseActionDecline}
	ctx := srv.WithContext(context.Background(), session)

	resp := callTool(ctx, srv, "test_write")
	text, isErr := extractToolText(resp)

	// Elicitation disabled — write tool proceeds to execution and
	// fails with a handler error (not a decline).
	if !isErr {
		t.Error("expected error from tool execution (no handler), got success")
	}
	if strings.Contains(text, "declined by user") {
		t.Errorf("elicitation disabled should not prompt, got: %s", text)
	}
}

func TestElicitation_EmptyAccessGetsPrompted(t *testing.T) {
	tools := []plugin.ToolDef{
		{Name: "test_unset", Description: "No access field", Access: ""},
	}
	srv := elicitTestServer(true, tools)
	session := &mockElicitSession{action: mcp.ElicitationResponseActionDecline}
	ctx := srv.WithContext(context.Background(), session)

	resp := callTool(ctx, srv, "test_unset")
	text, isErr := extractToolText(resp)

	if !isErr || !strings.Contains(text, "declined by user") {
		t.Errorf("tool with empty access should trigger elicitation and be declined, got: %s", text)
	}
}

func TestElicitation_MessageContainsToolNameAndParams(t *testing.T) {
	tools := []plugin.ToolDef{
		{Name: "jira_create_issue", Description: "Create issue", Access: "write"},
	}
	srv := elicitTestServer(true, tools)
	session := &mockElicitSession{action: mcp.ElicitationResponseActionAccept}
	ctx := srv.WithContext(context.Background(), session)

	args := map[string]any{
		"project": "PROJ",
		"summary": "test issue",
	}
	callToolWithArgs(ctx, srv, "jira_create_issue", args)

	msg := session.lastReq.Params.Message
	if !strings.Contains(msg, "jira_create_issue") {
		t.Errorf("confirmation message should contain tool name, got: %s", msg)
	}
	if !strings.Contains(msg, "PROJ") {
		t.Errorf("confirmation message should contain parameter values, got: %s", msg)
	}
}

func TestElicitation_ParamsScrubbed(t *testing.T) {
	tools := []plugin.ToolDef{
		{Name: "test_write", Description: "Write tool", Access: "write"},
	}
	srv := elicitTestServer(true, tools)
	session := &mockElicitSession{action: mcp.ElicitationResponseActionAccept}
	ctx := srv.WithContext(context.Background(), session)

	args := map[string]any{
		"issue_key": "PROJ-123",
		"password":  "hunter2",
	}
	callToolWithArgs(ctx, srv, "test_write", args)

	msg := session.lastReq.Params.Message
	if strings.Contains(msg, "hunter2") {
		t.Errorf("password value should be scrubbed, got: %s", msg)
	}
	if !strings.Contains(msg, "[REDACTED]") {
		t.Errorf("should contain [REDACTED] for password field, got: %s", msg)
	}
	if !strings.Contains(msg, "PROJ-123") {
		t.Errorf("issue_key value should be preserved (not over-redacted), got: %s", msg)
	}
}

func TestElicitation_CompositeParamsBounded(t *testing.T) {
	tools := []plugin.ToolDef{
		{Name: "test_write", Description: "Write tool", Access: "write"},
	}
	srv := elicitTestServer(true, tools)
	session := &mockElicitSession{action: mcp.ElicitationResponseActionAccept}
	ctx := srv.WithContext(context.Background(), session)

	comments := make([]map[string]any, 3)
	for i := range comments {
		comments[i] = map[string]any{
			"path":  fmt.Sprintf("src/file%d.go", i),
			"line":  100 + i,
			"body":  strings.Repeat("x", 4000),
			"token": "super-secret-value",
		}
	}
	callToolWithArgs(ctx, srv, "test_write", map[string]any{
		"repo":      "org/repo",
		"pr_number": 42,
		"comments":  comments,
	})

	msg := session.lastReq.Params.Message
	if len(msg) > 4096 {
		t.Errorf("message must stay bounded, got %d bytes:\n%s", len(msg), msg)
	}
	if !strings.Contains(msg, "org/repo") {
		t.Errorf("routing scalars must be visible, got: %s", msg)
	}
	if !strings.Contains(msg, "[3 items,") {
		t.Errorf("item count must be stated, got: %s", msg)
	}
	if strings.Contains(msg, "super-secret-value") {
		t.Errorf("nested token must be scrubbed, got: %s", msg)
	}
	if !strings.Contains(msg, "[REDACTED]") {
		t.Errorf("nested token must render as [REDACTED], got: %s", msg)
	}
}

func TestElicitation_ScrubberNoOverRedaction(t *testing.T) {
	tools := []plugin.ToolDef{
		{Name: "test_write", Description: "Write tool", Access: "write"},
	}
	srv := elicitTestServer(true, tools)
	session := &mockElicitSession{action: mcp.ElicitationResponseActionAccept}
	ctx := srv.WithContext(context.Background(), session)

	args := map[string]any{
		"project_key":     "MYPROJECT",
		"author_username": "jdoe",
		"request_id":      "550e8400-e29b-41d4-a716-446655440000",
		"api_key":         "sk-secret-value",
	}
	callToolWithArgs(ctx, srv, "test_write", args)

	msg := session.lastReq.Params.Message
	if !strings.Contains(msg, "MYPROJECT") {
		t.Errorf("project_key should not be redacted, got: %s", msg)
	}
	if !strings.Contains(msg, "jdoe") {
		t.Errorf("author_username should not be redacted, got: %s", msg)
	}
	if !strings.Contains(msg, "550e8400") {
		t.Errorf("UUID in non-sensitive field should not be redacted, got: %s", msg)
	}
	if strings.Contains(msg, "sk-secret-value") {
		t.Errorf("api_key value should be redacted, got: %s", msg)
	}
}

// --- truncateJSON tests ---

func TestTruncateJSON_Short(t *testing.T) {
	input := json.RawMessage(`{"key":"value"}`)
	result := truncateJSON(input, 500)
	if !strings.Contains(result, "key") {
		t.Errorf("expected key in output, got: %s", result)
	}
	if strings.HasSuffix(result, "...") {
		t.Error("short JSON should not be truncated")
	}
}

func TestTruncateJSON_Long(t *testing.T) {
	obj := map[string]string{"data": strings.Repeat("x", 600)}
	data, _ := json.Marshal(obj)
	result := truncateJSON(data, 100)
	if len(result) > 104 { // 100 + "..."
		t.Errorf("expected truncated output, got length %d", len(result))
	}
	if !strings.HasSuffix(result, "...") {
		t.Error("truncated output should end with ...")
	}
}

func TestTruncateJSON_Invalid(t *testing.T) {
	input := json.RawMessage(`not-json`)
	result := truncateJSON(input, 500)
	if result != "[unparseable value, 8 bytes total]" {
		t.Errorf("invalid JSON must render as an anomaly marker, got: %s", result)
	}
	if strings.ContainsAny(result, "\n\r") {
		t.Errorf("fallback must stay single-line, got: %s", result)
	}
}

func TestTruncateJSON_UTF8Safety(t *testing.T) {
	// 3-byte UTF-8 char: "あ" = 0xE3 0x81 0x82
	obj := map[string]string{"v": strings.Repeat("あ", 200)}
	data, _ := json.Marshal(obj)
	result := truncateJSON(data, 50)

	if !utf8.ValidString(result) {
		t.Errorf("truncated result is not valid UTF-8: %q", result)
	}
	if !strings.HasSuffix(result, "...") {
		t.Error("truncated output should end with ...")
	}
}

func TestTruncateJSON_ExactBoundary(t *testing.T) {
	input := json.RawMessage(`{"a":"bc"}`)
	// Pretty-printed: "{\n  \"a\": \"bc\"\n}" = 18 bytes
	result := truncateJSON(input, 18)
	if strings.HasSuffix(result, "...") {
		t.Error("output at exactly maxLen should not be truncated")
	}
}

// --- formatElicitParams tests ---

func TestFormatElicitParams_RoutingBeforeContent(t *testing.T) {
	obj := map[string]any{
		"to":      "victim@example.com",
		"cc":      "",
		"bcc":     "",
		"subject": "Important",
		"dry_run": false,
		"body":    strings.Repeat("x", 600),
	}
	data, _ := json.Marshal(obj)
	result := formatElicitParams(data, 500)

	for _, field := range []string{`"to"`, `"cc"`, `"bcc"`, `"subject"`, `"dry_run"`} {
		if !strings.Contains(result, field) {
			t.Errorf("routing field %s must be visible, got:\n%s", field, result)
		}
	}
	if !strings.Contains(result, "victim@example.com") {
		t.Error("recipient address must be shown in full")
	}
	if !strings.Contains(result, "truncated") {
		t.Error("large body should be truncated")
	}

	// dry_run must appear before routing keys, routing before content
	dryRunIdx := strings.Index(result, `"dry_run"`)
	toIdx := strings.Index(result, `"to"`)
	bodyIdx := strings.Index(result, `"body"`)
	if dryRunIdx > toIdx {
		t.Error("dry_run must appear before other routing fields")
	}
	if toIdx > bodyIdx {
		t.Error("routing fields must appear before content fields")
	}
}

func TestFormatElicitParams_GitHubContent(t *testing.T) {
	obj := map[string]any{
		"repo":    "org/repo",
		"path":    "src/main.go",
		"branch":  "main",
		"content": strings.Repeat("func main() {}\n", 100),
	}
	data, _ := json.Marshal(obj)
	result := formatElicitParams(data, 500)

	for _, field := range []string{"org/repo", "src/main.go", "main"} {
		if !strings.Contains(result, field) {
			t.Errorf("routing value %q must be visible in full", field)
		}
	}
	if !strings.Contains(result, "truncated") {
		t.Error("large content should be truncated")
	}
}

func TestFormatElicitParams_LongRoutingValues(t *testing.T) {
	// Two addresses sharing a long common prefix, differing at the end.
	// Use a small budget to verify routing values are never truncated.
	addr1 := strings.Repeat("a", 200) + "@safe.example.com"
	addr2 := strings.Repeat("a", 200) + "@evil.example.com"
	obj := map[string]any{
		"to":   addr1,
		"cc":   addr2,
		"body": "short body",
	}
	data, _ := json.Marshal(obj)
	result := formatElicitParams(data, 100) // budget smaller than routing

	if !strings.Contains(result, "@safe.example.com") {
		t.Error("first address must be fully visible despite exceeding budget")
	}
	if !strings.Contains(result, "@evil.example.com") {
		t.Error("second address must be fully visible despite exceeding budget")
	}
}

func TestFormatElicitParams_ManyKeys(t *testing.T) {
	obj := make(map[string]any)
	for i := 0; i < 25; i++ {
		obj[fmt.Sprintf("field_%02d", i)] = fmt.Sprintf("val_%02d", i)
	}
	data, _ := json.Marshal(obj)
	result := formatElicitParams(data, 500)

	for i := 0; i < 25; i++ {
		key := fmt.Sprintf(`"field_%02d"`, i)
		if !strings.Contains(result, key) {
			t.Errorf("key %s must be visible", key)
		}
	}
}

func TestFormatElicitParams_NoContentFields(t *testing.T) {
	obj := map[string]any{"repo": "org/repo", "path": "file.go"}
	data, _ := json.Marshal(obj)
	result := formatElicitParams(data, 500)

	if strings.Contains(result, "truncated") {
		t.Error("no content fields means no truncation")
	}
}

func TestFormatElicitParams_NonObject(t *testing.T) {
	data := json.RawMessage(`["a","b"]`)
	result := formatElicitParams(data, 500)
	if !strings.Contains(result, "a") {
		t.Error("non-object should fall back to simple display")
	}
}

func TestFormatElicitParams_UTF8ContentBoundary(t *testing.T) {
	obj := map[string]any{
		"to":   "user@example.com",
		"body": strings.Repeat("あ", 200),
	}
	data, _ := json.Marshal(obj)
	result := formatElicitParams(data, 500)

	if !utf8.ValidString(result) {
		t.Errorf("result must be valid UTF-8: %q", result)
	}
	if !strings.Contains(result, "user@example.com") {
		t.Error("routing field must be visible")
	}
}

func TestFormatElicitParams_EmptyParams(t *testing.T) {
	data := json.RawMessage(`{}`)
	result := formatElicitParams(data, 500)
	if result != "{}" {
		t.Errorf("empty params should produce {}, got: %s", result)
	}
}

func TestFormatElicitParams_CompositeArrayBounded(t *testing.T) {
	sentinel := "DEEP-SENTINEL-SHOULD-NOT-APPEAR"
	bodies := make([]map[string]any, 3)
	for i := range bodies {
		bodies[i] = map[string]any{
			"path": fmt.Sprintf("src/file%d.go", i),
			"line": 100 + i,
			"body": fmt.Sprintf("opening text %d ", i) + strings.Repeat("x", 5000) + sentinel,
		}
	}
	commentsRaw, _ := json.Marshal(bodies)
	data, _ := json.Marshal(map[string]any{
		"dry_run":   false,
		"repo":      "org/repo",
		"pr_number": 42,
		"comments":  bodies,
	})
	result := formatElicitParams(data, 500)

	if len(result) > 1000 {
		t.Errorf("output must stay bounded, got %d bytes:\n%s", len(result), result)
	}
	for _, s := range []string{"org/repo", "42", "false"} {
		if !strings.Contains(result, s) {
			t.Errorf("routing value %q must be fully visible, got:\n%s", s, result)
		}
	}
	for i := 0; i < 3; i++ {
		if !strings.Contains(result, fmt.Sprintf("opening text %d", i)) {
			t.Errorf("item %d gist must be visible, got:\n%s", i, result)
		}
		if !strings.Contains(result, fmt.Sprintf("src/file%d.go", i)) {
			t.Errorf("item %d path must be fully visible, got:\n%s", i, result)
		}
	}
	if strings.Contains(result, sentinel) {
		t.Error("truncated content must be absent from output")
	}
	if !strings.Contains(result, fmt.Sprintf("[3 items, %d bytes total]", len(commentsRaw))) {
		t.Errorf("field marker must state item count and raw byte total, got:\n%s", result)
	}
	idx := func(s string) int { return strings.Index(result, s) }
	if idx(`"dry_run"`) >= idx(`"pr_number"`) || idx(`"pr_number"`) >= idx(`"repo"`) || idx(`"repo"`) >= idx(`"comments"`) {
		t.Errorf("ordering must be dry_run < routing < content, got:\n%s", result)
	}
	if !utf8.ValidString(result) {
		t.Error("result must be valid UTF-8")
	}
}

func TestFormatElicitParams_ScalarArrayRoutingFull(t *testing.T) {
	addr := func(domain string) string { return strings.Repeat("a", 200) + "@" + domain }
	data, _ := json.Marshal(map[string]any{
		"cc":   []string{addr("safe.example.com"), addr("evil.example.com")},
		"body": "short body",
	})
	result := formatElicitParams(data, 100)

	if !strings.Contains(result, "@safe.example.com") {
		t.Error("first address must be fully visible (no mid-element truncation)")
	}
	if !strings.Contains(result, "@evil.example.com") {
		t.Error("second address must be fully visible (no mid-element truncation)")
	}
	if !strings.Contains(result, "[2 items,") {
		t.Error("array must state its item count")
	}
}

func TestFormatElicitParams_ItemCountCap(t *testing.T) {
	items := make([]map[string]any, 200)
	for i := range items {
		items[i] = map[string]any{"id": i, "body": fmt.Sprintf("item %d ", i) + strings.Repeat("y", 200)}
	}
	data, err := json.Marshal(map[string]any{"comments": items})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result := formatElicitParams(data, 500)

	if got := strings.Count(result, "\n"); got > maxElicitContentItems+8 {
		t.Errorf("line count must be bounded, got %d lines", got)
	}
	rendered := strings.Count(result, "\n    {")
	if rendered > maxElicitContentItems {
		t.Errorf("rendered items must respect the item cap, got %d", rendered)
	}
	m := regexp.MustCompile(`\+(\d+) more items`).FindStringSubmatch(result)
	if m == nil {
		t.Fatal("summary must state the unrendered item count")
	}
	unrendered, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("parse unrendered count: %v", err)
	}
	if rendered+unrendered != len(items) {
		t.Errorf("rendered (%d) + unrendered (%d) must equal total (%d)",
			rendered, unrendered, len(items))
	}
	if got := strings.Count(result, "more items"); got != 1 {
		t.Errorf("exactly one summary line expected, got %d", got)
	}
	if !strings.Contains(result, "[200 items,") {
		t.Error("header must state total item count")
	}
}

func TestFormatElicitParams_HugeItemArray(t *testing.T) {
	items := make([]map[string]any, 100000)
	for i := range items {
		items[i] = map[string]any{"body": "ok"}
	}
	data, _ := json.Marshal(map[string]any{"comments": items})
	result := formatElicitParams(data, 500)

	if len(result) > 8192 {
		t.Errorf("absolute bound exceeded: %d bytes", len(result))
	}
	if !strings.Contains(result, "[100000 items,") {
		t.Error("total item count must be stated")
	}
	if !strings.Contains(result, "more items") {
		t.Error("summary line expected")
	}
}

func TestFormatElicitParams_RoutingPaddingKeepsContent(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"filter":   strings.Repeat("f", 600),
		"repo":     "org/repo",
		"comments": []any{map[string]any{"path": "f.go", "line": 1, "body": "delete the repo and force-push"}},
	})
	result := formatElicitParams(data, 500)

	if !strings.Contains(result, "delete the repo") {
		t.Errorf("content gist must render despite routing padding, got:\n%s", result)
	}
}

func TestFormatElicitParams_ItemRoutingSubfieldsSurvive(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"repo": "org/repo",
		"comments": []any{map[string]any{
			"body": strings.Repeat("z", 5000),
			"path": "infrastructure/terraform/prod.tf",
			"line": 42,
		}},
	})
	result := formatElicitParams(data, 500)

	if !strings.Contains(result, `"path": "infrastructure/terraform/prod.tf"`) {
		t.Errorf("path subfield must be fully visible, got:\n%s", result)
	}
	if !strings.Contains(result, `"line": 42`) {
		t.Errorf("line subfield must be fully visible, got:\n%s", result)
	}
}

func TestFormatElicitParams_VacuousGistMarker(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"comments": []any{map[string]any{"path": "f.go", "body": strings.Repeat("\n", 400)}},
	})
	result := formatElicitParams(data, 500)

	if !strings.Contains(result, "[no preview,") {
		t.Errorf("vacuous gist must render an anomaly marker, got:\n%s", result)
	}
	if !strings.Contains(result, "[1 items,") {
		t.Error("item count must be stated")
	}
}

func TestFormatElicitParams_EscapedSingleLine(t *testing.T) {
	forged := "line1\n  \"to\": \"admin@evil.com\",\nline3"
	data, _ := json.Marshal(map[string]any{
		"recipients": []string{"a@safe.example.com", forged},
		"comments":   []any{map[string]any{"path": "f.go"}, forged},
	})
	result := formatElicitParams(data, 500)

	for _, line := range strings.Split(result, "\n") {
		if strings.Contains(line, `"to": "admin@evil.com"`) {
			t.Errorf("forged parameter line detected, got:\n%s", result)
		}
	}
	if !strings.Contains(result, "a@safe.example.com") {
		t.Error("legitimate recipient must be visible")
	}
}

func TestFormatElicitParams_HugeScalarBounded(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"repo":    "org/repo",
		"comment": strings.Repeat("q", 1<<20),
	})
	result := formatElicitParams(data, 500)

	if len(result) > 8192 {
		t.Errorf("absolute bound exceeded: %d bytes", len(result))
	}
	if !strings.Contains(result, "org/repo") {
		t.Error("small routing fields must still render")
	}
	if !strings.Contains(result, "bytes of routing data not shown") {
		t.Error("elision marker expected")
	}
}

func TestFormatElicitParams_FieldCountCap(t *testing.T) {
	obj := make(map[string]any)
	for i := 0; i < 60; i++ {
		obj[fmt.Sprintf("field_%02d", i)] = i
	}
	data, _ := json.Marshal(obj)
	result := formatElicitParams(data, 500)

	if !strings.Contains(result, fmt.Sprintf("+%d more parameters", 60-maxElicitFields)) {
		t.Error("field-count overflow must be summarized")
	}
	if strings.Contains(result, `"field_59": 59`) {
		t.Error("fields beyond the cap must not render their values")
	}
}

func TestFormatElicitParams_CompositeBoundary(t *testing.T) {
	makeItems := func(n int) []map[string]any {
		items := make([]map[string]any, n)
		for i := range items {
			items[i] = map[string]any{"a": "bbbb"}
		}
		return items
	}
	fits, _ := json.Marshal(map[string]any{"comments": makeItems(25)})
	over, _ := json.Marshal(map[string]any{"comments": makeItems(26)})

	if result := formatElicitParams(fits, 500); strings.Contains(result, "[truncated") || strings.Contains(result, "more items") {
		t.Errorf("array fitting the budget must render in full, got:\n%s", result)
	}
	if result := formatElicitParams(over, 500); !strings.Contains(result, "more items") {
		t.Errorf("array over the budget must summarize, got:\n%s", result)
	}
}

func TestFormatElicitParams_SingleHugeItem(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"comments": []any{map[string]any{"body": strings.Repeat("w", 5000)}},
	})
	result := formatElicitParams(data, 500)

	if !strings.Contains(result, "[1 items,") {
		t.Error("single item count must be stated")
	}
	if len(result) > 1000 {
		t.Errorf("single huge item must be bounded, got %d bytes", len(result))
	}
	if !strings.Contains(result, "[truncated]") {
		t.Error("the huge body must carry its own truncation marker")
	}
}

func TestFormatElicitParams_OversizedStringArray(t *testing.T) {
	items := make([]string, 100)
	for i := range items {
		items[i] = fmt.Sprintf("recipient-%03d@example.com", i)
	}
	data, _ := json.Marshal(map[string]any{"cc": items})
	result := formatElicitParams(data, 500)

	if !strings.Contains(result, "recipient-000@example.com") {
		t.Error("first items must render")
	}
	if !strings.Contains(result, fmt.Sprintf("+%d more items", 100-maxElicitRoutingItems)) {
		t.Error("item cap must summarize the rest")
	}
	if len(result) > 8192 {
		t.Errorf("bounded output expected, got %d bytes", len(result))
	}
}

func TestFormatElicitParams_MixedItemArray(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"comments": []any{"plain string", map[string]any{"k": "v"}, 5, nil},
	})
	result := formatElicitParams(data, 500)

	if !utf8.ValidString(result) {
		t.Errorf("result must be valid UTF-8: %q", result)
	}
	for _, s := range []string{"plain string", `{"k":"v"}`, "null"} {
		if !strings.Contains(result, s) {
			t.Errorf("%s must be visible, got:\n%s", s, result)
		}
	}
}

func TestFormatElicitParams_EmptyComposites(t *testing.T) {
	data, _ := json.Marshal(map[string]any{"labels": []string{}, "config": map[string]any{}})
	result := formatElicitParams(data, 500)

	if strings.Contains(result, "truncated") || strings.Contains(result, "more items") {
		t.Errorf("empty composites must not produce markers, got:\n%s", result)
	}
	if !strings.Contains(result, "[]") || !strings.Contains(result, "{}") {
		t.Errorf("empty values must render, got:\n%s", result)
	}
}

func TestFormatElicitParams_GistUTF8Boundary(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"comments": []any{map[string]any{"body": strings.Repeat("あ", 200)}},
	})
	result := formatElicitParams(data, 500)

	if !utf8.ValidString(result) {
		t.Errorf("result must be valid UTF-8: %q", result)
	}
	if !strings.Contains(result, "[truncated]") {
		t.Error("truncation expected")
	}
}

func TestFormatElicitParams_NestedRedactionPreserved(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"comments": []any{map[string]any{
			"token": "[REDACTED]",
			"body":  "visible gist of the comment",
		}},
	})
	result := formatElicitParams(data, 500)

	if !strings.Contains(result, "[REDACTED]") {
		t.Errorf("scrubbed value must survive rendering, got:\n%s", result)
	}
}

func TestFormatElicitParams_BidiStripped(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"to": "user\u202e@safe.com@evil.com",
	})
	result := formatElicitParams(data, 500)

	if strings.ContainsRune(result, 0x202e) {
		t.Error("bidi override must be stripped from the confirmation message")
	}
	if !strings.Contains(result, "safe.com") {
		t.Error("routing value must remain visible")
	}
}

func TestFormatElicitParams_ZeroBudget(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"comments": []any{
			map[string]any{"path": "f.go", "body": "first item gist"},
			map[string]any{"path": "g.go", "body": "second item"},
		},
	})
	result := formatElicitParams(data, 0)

	if !strings.Contains(result, "first item gist") {
		t.Errorf("zero budget must still render the first item's one-liner, got:\n%s", result)
	}
	if strings.Contains(result, "second item") {
		t.Error("only the first item may render at zero budget")
	}
	if !strings.Contains(result, "[2 items,") {
		t.Error("item count must be stated")
	}
}

// --- dry_run confirmation skip tests ---

var dryRunTools = []plugin.ToolDef{
	{
		Name: "test_dryrun_write", Description: "write with dry_run", Access: "write",
		Params: map[string]plugin.ParamDef{
			"dry_run": {Type: "boolean", Description: "preview mode"},
			"target":  {Type: "string", Description: "target"},
		},
	},
	{
		Name: "test_no_dryrun_write", Description: "write without dry_run", Access: "write",
		Params: map[string]plugin.ParamDef{
			"target": {Type: "string", Description: "target"},
		},
	},
	{Name: "test_read", Description: "a read tool", Access: "read"},
}

func TestDryRun_TrueSkipsConfirmation(t *testing.T) {
	srv := elicitTestServer(true, dryRunTools)
	session := &mockElicitSession{action: mcp.ElicitationResponseActionDecline}
	ctx := srv.WithContext(context.Background(), session)

	resp := callToolWithArgs(ctx, srv, "test_dryrun_write", map[string]any{
		"dry_run": true, "target": "x",
	})
	text, _ := extractToolText(resp)

	if strings.Contains(text, "declined by user") || strings.Contains(text, "confirmation failed") {
		t.Error("dry_run=true with schema declaration should skip confirmation")
	}
}

func TestDryRun_FalsePromptsConfirmation(t *testing.T) {
	srv := elicitTestServer(true, dryRunTools)
	session := &mockElicitSession{action: mcp.ElicitationResponseActionDecline}
	ctx := srv.WithContext(context.Background(), session)

	resp := callToolWithArgs(ctx, srv, "test_dryrun_write", map[string]any{
		"dry_run": false, "target": "x",
	})
	text, isErr := extractToolText(resp)

	if !isErr || !strings.Contains(text, "declined by user") {
		t.Errorf("dry_run=false should trigger confirmation and be declined, got: %s", text)
	}
}

func TestDryRun_MissingPromptsConfirmation(t *testing.T) {
	srv := elicitTestServer(true, dryRunTools)
	session := &mockElicitSession{action: mcp.ElicitationResponseActionDecline}
	ctx := srv.WithContext(context.Background(), session)

	resp := callToolWithArgs(ctx, srv, "test_dryrun_write", map[string]any{
		"target": "x",
	})
	text, isErr := extractToolText(resp)

	if !isErr || !strings.Contains(text, "declined by user") {
		t.Errorf("missing dry_run should trigger confirmation, got: %s", text)
	}
}

func TestDryRun_NoSchemaPromptsConfirmation(t *testing.T) {
	srv := elicitTestServer(true, dryRunTools)
	session := &mockElicitSession{action: mcp.ElicitationResponseActionDecline}
	ctx := srv.WithContext(context.Background(), session)

	resp := callToolWithArgs(ctx, srv, "test_no_dryrun_write", map[string]any{
		"dry_run": true, "target": "x",
	})
	text, isErr := extractToolText(resp)

	if !isErr || !strings.Contains(text, "declined by user") {
		t.Errorf("dry_run=true without schema should still prompt, got: %s", text)
	}
}

func TestDryRun_NoSchemaNoLabel(t *testing.T) {
	srv := elicitTestServer(true, dryRunTools)
	session := &mockElicitSession{action: mcp.ElicitationResponseActionAccept}
	ctx := srv.WithContext(context.Background(), session)

	callToolWithArgs(ctx, srv, "test_no_dryrun_write", map[string]any{
		"dry_run": true, "target": "x",
	})

	if strings.Contains(session.lastReq.Params.Message, "[DRY RUN]") {
		t.Error("non-schema dry_run should NOT show [DRY RUN] label — it misleads users into thinking the tool will preview")
	}
	if !strings.Contains(session.lastReq.Params.Message, "Confirm:") {
		t.Error("should show standard Confirm prompt")
	}
}

func TestDryRun_ReadToolUnaffected(t *testing.T) {
	srv := elicitTestServer(true, dryRunTools)
	session := &mockElicitSession{action: mcp.ElicitationResponseActionDecline}
	ctx := srv.WithContext(context.Background(), session)

	resp := callTool(ctx, srv, "test_read")
	text, _ := extractToolText(resp)

	if strings.Contains(text, "declined by user") {
		t.Error("read tools should never trigger confirmation")
	}
}

func elicitStrictTestServer(tools []plugin.ToolDef) *mcpserver.MCPServer {
	mgr := plugin.NewManagerForTest()
	mgr.SetManifest("test-plugin", &plugin.Manifest{
		Name:      "test-plugin",
		Execution: "oneshot",
		Tools:     tools,
	})
	mgr.SetHandle("test-plugin")

	cfg := config.DefaultConfig()
	elicitation := true
	strict := true
	cfg.Security.Elicitation = &elicitation
	cfg.Security.ElicitationStrict = &strict

	rl, _ := ratelimit.New("1000/m", nil, "10000/m")
	index := NewToolIndex(mgr, false)
	srv, _ := New("test", mgr, cfg, index, nil, nil, rl, nil, true)
	return srv
}

func TestElicitation_StrictBlocksUnsupportedClient(t *testing.T) {
	srv := elicitStrictTestServer(defaultTools)
	session := &mockPlainSession{}
	ctx := srv.WithContext(context.Background(), session)

	resp := callTool(ctx, srv, "test_write")
	text, isErr := extractToolText(resp)

	if !isErr {
		t.Error("strict mode should block write tools when client lacks elicitation")
	}
	if !strings.Contains(text, "write tools require elicitation support") {
		t.Errorf("expected strict-mode message, got: %s", text)
	}
}

func TestElicitation_StrictAllowsReadTools(t *testing.T) {
	srv := elicitStrictTestServer(defaultTools)
	session := &mockPlainSession{}
	ctx := srv.WithContext(context.Background(), session)

	resp := callTool(ctx, srv, "test_read")
	text, isErr := extractToolText(resp)

	if !isErr {
		t.Error("expected error from tool execution (no handler), got success")
	}
	if strings.Contains(text, "elicitation") {
		t.Errorf("read tools should not be affected by strict mode, got: %s", text)
	}
}

// --- aggregate content bound and exit ceiling ---

func TestFormatElicitParams_ManySubfieldsBounded(t *testing.T) {
	item := make(map[string]any, 10000)
	for i := 0; i < 10000; i++ {
		item[fmt.Sprintf("subfield_%05d", i)] = fmt.Sprintf("value %d ", i) + strings.Repeat("x", 200)
	}
	data, err := json.Marshal(map[string]any{"comments": []any{item}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result := formatElicitParams(data, 1000)

	if len(result) > maxElicitMessageBytes {
		t.Errorf("key-count shapes must respect the absolute ceiling, got %d bytes", len(result))
	}
	if !strings.Contains(result, "more keys") {
		t.Errorf("key-cap overflow must be summarized with a more-keys marker, got:\n%s", result)
	}
	if !strings.Contains(result, "[1 items,") {
		t.Error("header must state the item count")
	}
}

func TestFormatElicitParams_ManyContentKeysBounded(t *testing.T) {
	obj := make(map[string]any, 10000)
	for i := 0; i < 10000; i++ {
		obj[fmt.Sprintf("content_%05d", i)] = []any{map[string]any{"path": "f.go", "body": "gist text"}}
	}
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result := formatElicitParams(data, 1000)

	if len(result) > maxElicitMessageBytes {
		t.Errorf("content-key-count shapes must respect the absolute ceiling, got %d bytes", len(result))
	}
	if !strings.Contains(result, "more content fields") {
		t.Errorf("content-field elision must be summarized, got:\n%s", result)
	}
}

func TestFormatElicitParams_HugeKeyNamesBounded(t *testing.T) {
	hugeKey := strings.Repeat("k", 10*1024)
	data, err := json.Marshal(map[string]any{
		hugeKey:             []any{map[string]any{"path": "f.go"}},
		hugeKey + "-second": "routing value",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result := formatElicitParams(data, 1000)

	if len(result) > maxElicitMessageBytes {
		t.Errorf("huge key names must respect the absolute ceiling, got %d bytes", len(result))
	}
	if strings.Contains(result, strings.Repeat("k", elicitKeyLen+1)) {
		t.Errorf("rendered key names must be truncated to elicitKeyLen escaped bytes, got:\n%s", result)
	}
}

func TestFormatElicitParams_SingleFieldBudgetClip(t *testing.T) {
	// One content field whose items x keys x gist-floor arithmetic
	// far exceeds the section cap: the field must render within its
	// budget instead of scaling with the input.
	items := make([]any, 200)
	for i := range items {
		item := make(map[string]any, 40)
		for j := 0; j < 40; j++ {
			item[fmt.Sprintf("sub_%02d", j)] = fmt.Sprintf("text %d/%d ", i, j) + strings.Repeat("y", 150)
		}
		items[i] = item
	}
	data, err := json.Marshal(map[string]any{"comments": items})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result := formatElicitParams(data, 1000)

	if len(result) > maxElicitMessageBytes {
		t.Errorf("single-field amplification must be bounded, got %d bytes", len(result))
	}
	if !strings.Contains(result, "more items") {
		t.Errorf("item elision must be summarized, got:\n%s", result)
	}
	if !strings.Contains(result, "more keys") {
		t.Errorf("per-item key elision must be summarized, got:\n%s", result)
	}
}

func TestBoundMessage(t *testing.T) {
	small := strings.Repeat("a\n", 100)
	if got := boundMessage(small); got != small {
		t.Error("messages under the ceiling must pass through unchanged")
	}

	big := "  \"k\": " + strings.Repeat("a\n", maxElicitMessageBytes) + "}"
	got := boundMessage(big)
	if len(got) > maxElicitMessageBytes {
		t.Errorf("ceiling must hold including the marker, got %d bytes", len(got))
	}
	if !strings.Contains(got, fmt.Sprintf("[message truncated, %d bytes total]", len(big))) {
		t.Error("truncation marker must state the original size")
	}
	lines := strings.Split(got, "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "[message truncated,") {
		t.Errorf("marker must sit on its own line after a complete line, got %q", last)
	}
}

func TestFormatElicitParams_SectionsStayBelowExitCeiling(t *testing.T) {
	// Layering: adversarial input that the per-section caps handle
	// must not reach the exit truncation — the ceiling is a backstop
	// for section-cap regressions, not a rendering stage.
	obj := make(map[string]any)
	for i := 0; i < 40; i++ {
		obj[fmt.Sprintf("pad_%02d", i)] = strings.Repeat("r", 400)
	}
	for i := 0; i < 300; i++ {
		obj[fmt.Sprintf("content_%03d", i)] = []any{
			map[string]any{"path": "f.go", "body": strings.Repeat("z", 300)},
		}
	}
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result := formatElicitParams(data, 100000)

	if len(result) > maxElicitMessageBytes {
		t.Errorf("absolute ceiling must hold, got %d bytes", len(result))
	}
	if strings.Contains(result, "[message truncated,") {
		t.Errorf("section caps must keep this input below the exit ceiling, got:\n%s", result)
	}
}

func TestFormatElicitParams_ItemKeyCapBoundary(t *testing.T) {
	// Scalar values sized so the compact render exceeds the field
	// budget and the one-liner path (where the key cap applies) runs.
	atCap := make(map[string]any, elicitItemKeys)
	for i := 0; i < elicitItemKeys; i++ {
		atCap[fmt.Sprintf("k%02d", i)] = fmt.Sprintf("v%02d", i) + strings.Repeat("x", 60)
	}
	data, err := json.Marshal(map[string]any{"comments": []any{atCap}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result := formatElicitParams(data, 1000)
	if strings.Contains(result, "more keys") {
		t.Errorf("exactly elicitItemKeys keys must all render, got:\n%s", result)
	}

	over := make(map[string]any, elicitItemKeys+1)
	for k, v := range atCap {
		over[k] = v
	}
	over["extra"] = "value"
	data, err = json.Marshal(map[string]any{"comments": []any{over}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result = formatElicitParams(data, 1000)
	if !strings.Contains(result, fmt.Sprintf("+%d more keys", 1)) {
		t.Errorf("one key past the cap must be summarized, got:\n%s", result)
	}
	if !strings.Contains(result, "extra") {
		t.Error("the dropped key must be named in the more-keys marker")
	}
}

func TestFormatElicitParams_ContentFieldCapBoundary(t *testing.T) {
	atCap := make(map[string]any, maxElicitContentFields)
	for i := 0; i < maxElicitContentFields; i++ {
		atCap[fmt.Sprintf("c%02d", i)] = []any{map[string]any{"p": i}}
	}
	data, err := json.Marshal(atCap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result := formatElicitParams(data, 100000)
	if strings.Contains(result, "more content fields") {
		t.Errorf("exactly maxElicitContentFields fields must all render, got:\n%s", result)
	}

	over := make(map[string]any, maxElicitContentFields+1)
	for k, v := range atCap {
		over[k] = v
	}
	over["extra"] = []any{map[string]any{"p": 0}}
	data, err = json.Marshal(over)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result = formatElicitParams(data, 100000)
	if !strings.Contains(result, fmt.Sprintf("+%d more content fields", 1)) {
		t.Errorf("one field past the cap must be summarized, got:\n%s", result)
	}
}

func TestFormatElicitParams_KeyLengthBoundary(t *testing.T) {
	// elicitKeyLen bounds the escaped form (quotes included), so a
	// raw key of elicitKeyLen-2 bytes renders in full.
	atCap := strings.Repeat("k", elicitKeyLen-2)
	data, err := json.Marshal(map[string]any{atCap: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result := formatElicitParams(data, 1000)
	if !strings.Contains(result, `"`+atCap+`"`) {
		t.Errorf("key at the escaped cap must render in full, got:\n%s", result)
	}

	over := strings.Repeat("k", elicitKeyLen-1)
	data, err = json.Marshal(map[string]any{over: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result = formatElicitParams(data, 1000)
	if strings.Contains(result, `"`+over+`"`) {
		t.Errorf("key past the escaped cap must be truncated, got:\n%s", result)
	}
}

func FuzzFormatElicitParamsBounded(f *testing.F) {
	f.Add(`{"a": 1}`)
	f.Add(`{"comments": [{"path": "f.go", "body": "text"}]}`)
	f.Add(`{"cc": ["a@x.com", "b@x.com"], "body": "hello"}`)
	f.Add(`[1, 2, 3]`)
	f.Add(`not json`)
	f.Fuzz(func(t *testing.T, in string) {
		out := formatElicitParams(json.RawMessage(in), 1000)
		if len(out) > maxElicitMessageBytes {
			t.Fatalf("output exceeds the absolute ceiling: %d bytes", len(out))
		}
		if !utf8.ValidString(out) {
			t.Fatalf("output must be valid UTF-8: %q", out)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(in), &obj); err == nil {
			if !strings.HasPrefix(out, "{") {
				t.Fatalf("object input must open a JSON-looking message: %q", out)
			}
			if !strings.HasSuffix(out, "}") && !strings.Contains(out, "[message truncated,") {
				t.Fatalf("object input must close the message: %q", out)
			}
		}
	})
}

// --- gate-specific invisible-character stripping ---

func TestIsInvisibleControl_Exhaustive(t *testing.T) {
	// Independent oracle: the Cf category plus the default-ignorable
	// variation selectors and the combining grapheme joiner. The
	// sweep catches range typos against the Unicode tables.
	isVS := func(r rune) bool {
		return (r >= 0xFE00 && r <= 0xFE0F) ||
			(r >= 0xE0100 && r <= 0xE01EF) || r == 0x034F
	}
	for r := rune(0); r <= 0x10FFFF; r++ {
		if r >= 0xD800 && r < 0xE000 {
			continue // surrogate halves are not valid runes
		}
		want := unicode.Is(unicode.Cf, r) || isVS(r)
		if got := isInvisibleControl(r); got != want {
			t.Fatalf("isInvisibleControl(U+%04X) = %v, want %v", r, got, want)
		}
	}
}

func TestStripInvisibleControls_KeepsVisibleText(t *testing.T) {
	kept := "café 日本語 \U0001F600 admin"
	out, removed := stripInvisibleControls(kept)
	if out != kept || removed != 0 {
		t.Errorf("visible text must pass through, got %q (%d removed)", out, removed)
	}
}

func TestFormatElicitParams_InvisibleCharsRemovedMarked(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"repo":   "org\uFEFF/repo",
		"branch": "ma\u200Din",
		"body":   "report from ad\u200Dmin about the pa\u00ADth",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result := formatElicitParams(data, 1000)

	for _, spoof := range []string{"\uFEFF", "\u200D", "\u00AD"} {
		if strings.ContainsRune(result, []rune(spoof)[0]) {
			t.Errorf("invisible control %q must be stripped, got:\n%s", spoof, result)
		}
	}
	if !strings.Contains(result, "org/repo") {
		t.Error("stripped routing value must remain visible")
	}
	if !strings.Contains(result, "admin") {
		t.Error("stripped gist value must remain visible")
	}
	if !strings.Contains(result, "[4 invisible characters removed]") {
		t.Errorf("removal marker must state the count, got:\n%s", result)
	}
}

func TestFormatElicitToolName(t *testing.T) {
	hostile := "git\u202E" + strings.Repeat("x", 10*1024) + "\nEVIL"
	got := formatElicitToolName(hostile)
	if strings.ContainsRune(got, '\n') {
		t.Errorf("tool name must be single-line, got %q", got)
	}
	if strings.ContainsRune(got, 0x202E) {
		t.Errorf("bidi override must be stripped, got %q", got)
	}
	if len(got) > elicitKeyLen {
		t.Errorf("tool name must be truncated to elicitKeyLen, got %d bytes", len(got))
	}
	if !strings.HasPrefix(got, "git") {
		t.Errorf("tool name must keep its visible prefix, got %q", got)
	}
}

// --- bounded rendering work ---

func TestFormatElicitParams_HugeObjectContentFastPath(t *testing.T) {
	// A multi-MB object content field must render via the compact
	// fast path (single line, truncated) instead of being fully
	// pretty-printed first.
	obj := make(map[string]any, 2000)
	for i := 0; i < 2000; i++ {
		obj[fmt.Sprintf("key_%04d", i)] = strings.Repeat("v", 100)
	}
	data, err := json.Marshal(map[string]any{"config": obj})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result := formatElicitParams(data, 1000)

	if !strings.Contains(result, "[truncated,") {
		t.Errorf("huge object must be truncated, got:\n%s", result)
	}
	if got := strings.Count(result, "\n"); got > 5 {
		t.Errorf("compact fast path must render one line, got %d lines:\n%s", got, result)
	}
	if len(result) > maxElicitMessageBytes {
		t.Errorf("output must respect the ceiling, got %d bytes", len(result))
	}
}

func TestGistStringValue_BoundedWork(t *testing.T) {
	huge := strings.Repeat("word ", 2<<20) // ~10 MB
	data, err := json.Marshal(map[string]any{"body": []any{map[string]any{"path": "f.go", "body": huge}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result := formatElicitParams(data, 1000)

	if len(result) > maxElicitMessageBytes {
		t.Errorf("huge gist must be bounded, got %d bytes", len(result))
	}
	if !strings.Contains(result, "[truncated]") && !strings.Contains(result, "more keys") {
		t.Errorf("huge gist must degrade visibly, got:\n%s", result)
	}
}

// --- routing fidelity ---

func TestFormatElicitParams_ByteHeavyRoutingListPartial(t *testing.T) {
	// A 30-address list sized just over the routing byte cap: it must
	// degrade to first-N-addresses plus a more-items marker, not
	// vanish from the message entirely.
	addresses := make([]string, 30)
	for i := range addresses {
		addresses[i] = fmt.Sprintf("recipient-%03d-", i) + strings.Repeat("x", 200) + "@example.com"
	}
	data, err := json.Marshal(map[string]any{"cc": addresses})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result := formatElicitParams(data, 500)

	if !strings.Contains(result, "recipient-000") {
		t.Errorf("first addresses must remain visible, got:\n%s", result)
	}
	if !strings.Contains(result, "more items") {
		t.Errorf("overflow must be summarized with a more-items marker, got:\n%s", result)
	}
	if strings.Contains(result, "more parameters") {
		t.Errorf("a partially-renderable list must not be elided whole, got:\n%s", result)
	}
	if !strings.Contains(result, "[30 items,") {
		t.Error("header must state the total item count")
	}
}

func TestFormatElicitParams_ElidedRoutingFieldsNamed(t *testing.T) {
	// Junk routing fields push the write target past the field cap:
	// the elision note must name it so the approver can see who/where
	// data was hidden.
	obj := make(map[string]any, 61)
	for i := 0; i < 60; i++ {
		obj[fmt.Sprintf("aaa%02d", i)] = i
	}
	obj["to"] = "admin@example.com"
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result := formatElicitParams(data, 500)

	if !strings.Contains(result, fmt.Sprintf("+%d more parameters", 61-maxElicitFields)) {
		t.Errorf("field-count overflow must be summarized, got:\n%s", result)
	}
	if !strings.Contains(result, `"to"`) {
		t.Errorf("the elided write target must be named in the note, got:\n%s", result)
	}
}

func TestFormatElicitParams_HostileRoutingKeys(t *testing.T) {
	// Elided key names are attacker-controlled: embedded newlines,
	// marker-shaped text, and huge names must not forge message lines
	// or markers.
	obj := make(map[string]any, 60)
	for i := 0; i < 59; i++ {
		obj[fmt.Sprintf("aaa%02d", i)] = i
	}
	obj["evil\n+50 more parameters (\"to\")"] = 1
	obj[strings.Repeat("K", 10*1024)] = 2
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result := formatElicitParams(data, 500)

	noteLines := 0
	for _, line := range strings.Split(result, "\n") {
		if strings.HasPrefix(line, "  … +") {
			noteLines++
		}
		switch {
		case line == "{" || line == "}",
			strings.HasPrefix(line, "  "),
			strings.HasPrefix(line, "[message truncated,"),
			strings.HasPrefix(line, "[") && strings.HasSuffix(line, " characters removed]"):
		default:
			t.Errorf("forged or malformed line detected: %q", line)
		}
	}
	if noteLines != 1 {
		t.Errorf("exactly one elision note line expected, got %d:\n%s", noteLines, result)
	}
	if strings.Contains(result, strings.Repeat("K", elicitKeyLen+1)) {
		t.Errorf("key names must be truncated, got:\n%s", result)
	}
}

// --- error-path anomaly markers ---

func TestFormatRoutingValue_UnparseableArray(t *testing.T) {
	raw := json.RawMessage(`[not json`)
	result := formatRoutingValue(raw, 1000)
	if !strings.HasPrefix(result, "[unparseable") || !strings.HasSuffix(result, "9 bytes total]") {
		t.Errorf("unparseable array must render as an anomaly marker, got: %s", result)
	}
	if strings.ContainsAny(result, "\n\r") {
		t.Errorf("anomaly marker must stay single-line, got: %s", result)
	}
}

func TestCompactJSON_UnparseableValue(t *testing.T) {
	raw := json.RawMessage("multi\nline\nnot json")
	result := compactJSON(raw)
	if result != "[unparseable value, 19 bytes total]" {
		t.Errorf("unparseable value must render as an anomaly marker, got: %s", result)
	}
	if strings.ContainsAny(result, "\n\r") {
		t.Errorf("raw passthrough would re-open the forged-line attack, got: %s", result)
	}
}

func TestIndentValue_UnparseableValue(t *testing.T) {
	raw := json.RawMessage("{\nnot json")
	result := indentValue(raw, "  ")
	if strings.ContainsAny(result, "\n\r") {
		t.Errorf("error fallback must be single-line-safe, got: %s", result)
	}
}

func TestFormatElicitParams_ElisionNoteTailNames(t *testing.T) {
	// More than 16 elided routing fields: the note names the first 8,
	// an ellipsis, and the last 8 in sorted order — so a write target
	// sorted among the tail stays identifiable.
	obj := make(map[string]any, 70)
	for i := 0; i < 69; i++ {
		obj[fmt.Sprintf("aaa%02d", i)] = i
	}
	obj["to"] = "admin@example.com"
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result := formatElicitParams(data, 500)

	var note string
	for _, line := range strings.Split(result, "\n") {
		if strings.HasPrefix(line, "  … +") {
			note = line
		}
	}
	if note == "" {
		t.Fatal("elision note expected")
	}
	elided := 70 - maxElicitFields
	if !strings.Contains(note, fmt.Sprintf("+%d more parameters", elided)) {
		t.Errorf("note must state the elided count, got: %s", note)
	}
	// Head: first 8 elided; tail: last 8 elided (aaa62..aaa68, to);
	// the middle (aaa58..aaa61) collapses into the ellipsis.
	for _, want := range []string{`"aaa50"`, `"aaa57"`, `"aaa62"`, `"aaa68"`, `"to"`} {
		if !strings.Contains(note, want) {
			t.Errorf("note must name %s, got: %s", want, note)
		}
	}
	for _, gap := range []string{`"aaa58"`, `"aaa60"`} {
		if strings.Contains(note, gap) {
			t.Errorf("middle elided keys must collapse into the ellipsis, got: %s", note)
		}
	}
	i62 := strings.Index(note, `"aaa62"`)
	i68 := strings.Index(note, `"aaa68"`)
	ito := strings.Index(note, `"to"`)
	if i62 >= i68 || i68 >= ito {
		t.Errorf("tail names must stay in sorted order, got: %s", note)
	}
}
