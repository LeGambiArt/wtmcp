package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
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
	if result != "not-json" {
		t.Errorf("invalid JSON should pass through, got: %s", result)
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
