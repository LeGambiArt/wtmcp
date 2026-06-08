package plugin

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestLoadManifest(t *testing.T) {
	dir := t.TempDir()

	// Create a handler executable
	handlerPath := filepath.Join(dir, "handler.sh")
	if err := os.WriteFile(handlerPath, []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}

	manifest := `
name: test-plugin
version: "1.0.0"
description: "A test plugin"
execution: persistent
handler: ./handler.sh
depends_on: []

services:
  http:
    base_url: "https://api.example.com/v1"
  cache:
    default_ttl: 300

config:
  api_key: "${API_KEY:-default}"

tools:
  - name: test_tool
    description: "A test tool"
    params:
      query:
        type: string
        required: true
        description: "Search query"
      limit:
        type: integer
        default: 10

context_files:
  - context.md

priority: 50
enabled: true
`

	manifestPath := filepath.Join(dir, "plugin.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil { //nolint:gosec // test config file
		t.Fatal(err)
	}

	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}

	if m.Name != "test-plugin" {
		t.Errorf("Name = %q", m.Name)
	}
	if m.Version != "1.0.0" {
		t.Errorf("Version = %q", m.Version)
	}
	if m.Execution != "persistent" {
		t.Errorf("Execution = %q", m.Execution)
	}
	if m.Concurrency != 1 {
		t.Errorf("Concurrency = %d, want 1 (default)", m.Concurrency)
	}
	if m.Services.HTTP.BaseURL != "https://api.example.com/v1" {
		t.Errorf("BaseURL = %q", m.Services.HTTP.BaseURL)
	}
	if len(m.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(m.Tools))
	}
	if m.Tools[0].Name != "test_tool" {
		t.Errorf("Tool name = %q", m.Tools[0].Name)
	}
	if !m.IsEnabled() {
		t.Error("should be enabled")
	}
	if m.Dir != dir {
		t.Errorf("Dir = %q, want %q", m.Dir, dir)
	}
}

func TestLoadManifestDefaults(t *testing.T) {
	dir := t.TempDir()
	handlerPath := filepath.Join(dir, "handler")
	if err := os.WriteFile(handlerPath, []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}

	manifest := `
name: simple
version: "1.0.0"
description: "Minimal"
handler: ./handler
tools: []
`
	path := filepath.Join(dir, "plugin.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil { //nolint:gosec // test config file
		t.Fatal(err)
	}

	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}

	if m.Execution != "persistent" {
		t.Errorf("default Execution = %q, want persistent", m.Execution)
	}
	if m.Concurrency != 1 {
		t.Errorf("default Concurrency = %d, want 1", m.Concurrency)
	}
	if !m.IsEnabled() {
		t.Error("default should be enabled")
	}
	if !m.CacheEnabled() {
		t.Error("default cache should be enabled")
	}
	if m.CacheNamespace() != "simple" {
		t.Errorf("default CacheNamespace = %q, want %q", m.CacheNamespace(), "simple")
	}
}

func TestManifestValidation(t *testing.T) {
	dir := t.TempDir()
	handlerPath := filepath.Join(dir, "handler")
	if err := os.WriteFile(handlerPath, []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "invalid name - uppercase",
			yaml:    `name: BadName` + "\nversion: '1.0'\nhandler: ./handler\ntools: []",
			wantErr: "invalid plugin name",
		},
		{
			name:    "invalid name - too short",
			yaml:    `name: x` + "\nversion: '1.0'\nhandler: ./handler\ntools: []",
			wantErr: "invalid plugin name",
		},
		{
			name:    "invalid execution",
			yaml:    `name: ok-name` + "\nversion: '1.0'\nexecution: invalid\nhandler: ./handler\ntools: []",
			wantErr: "execution must be",
		},
		{
			name:    "missing handler",
			yaml:    `name: ok-name` + "\nversion: '1.0'\ntools: []",
			wantErr: "handler is required",
		},
		{
			name:    "handler escapes dir",
			yaml:    `name: ok-name` + "\nversion: '1.0'\nhandler: ../../etc/passwd\ntools: []",
			wantErr: "escapes plugin directory",
		},
		{
			name:    "base_url with query",
			yaml:    `name: ok-name` + "\nversion: '1.0'\nhandler: ./handler\ntools: []\nservices:\n  http:\n    base_url: 'https://api.com?foo=bar'",
			wantErr: "must not contain query",
		},
		{
			name:    "empty tool name",
			yaml:    `name: ok-name` + "\nversion: '1.0'\nhandler: ./handler\ntools:\n  - name: ''\n    description: test",
			wantErr: "tool name is required",
		},
		{
			name:    "invalid tool access",
			yaml:    `name: ok-name` + "\nversion: '1.0'\nhandler: ./handler\ntools:\n  - name: test_tool\n    description: test\n    access: admin",
			wantErr: "access must be 'read' or 'write'",
		},
		{
			name:    "invalid tool visibility",
			yaml:    `name: ok-name` + "\nversion: '1.0'\nhandler: ./handler\ntools:\n  - name: test_tool\n    description: test\n    visibility: hidden",
			wantErr: "visibility must be 'primary' or 'deferred'",
		},
		{
			name:    "invalid credential_group",
			yaml:    `name: ok-name` + "\nversion: '1.0'\nhandler: ./handler\ncredential_group: '../../etc'\ntools: []",
			wantErr: "invalid credential_group",
		},
		{
			name:    "invalid env_passthrough",
			yaml:    `name: ok-name` + "\nversion: '1.0'\nhandler: ./handler\nenv_passthrough: 'YES'\ntools: []",
			wantErr: "env_passthrough must be 'all' or empty",
		},
		{
			name:    "allow_private_ips without domains or base_url",
			yaml:    `name: ok-name` + "\nversion: '1.0'\nhandler: ./handler\ntools: []\nservices:\n  http:\n    allow_private_ips: true",
			wantErr: "allow_private_ips requires allowed_domains or base_url",
		},
		{
			name:    "tls client_cert without client_key",
			yaml:    `name: ok-name` + "\nversion: '1.0'\nhandler: ./handler\ntools: []\nservices:\n  http:\n    tls:\n      client_cert: /tmp/cert.pem",
			wantErr: "client_cert and client_key must both be set",
		},
		{
			name:    "tls client_key without client_cert",
			yaml:    `name: ok-name` + "\nversion: '1.0'\nhandler: ./handler\ntools: []\nservices:\n  http:\n    tls:\n      client_key: /tmp/key.pem",
			wantErr: "client_cert and client_key must both be set",
		},
		{
			name:    "tls client_cert with services.auth",
			yaml:    `name: ok-name` + "\nversion: '1.0'\nhandler: ./handler\ntools: []\nservices:\n  http:\n    tls:\n      client_cert: /tmp/cert.pem\n      client_key: /tmp/key.pem\n  auth:\n    type: bearer\n    token: xyz",
			wantErr: "client_cert (mTLS) and services.auth cannot both be set",
		},
		{
			name:    "tls client_cert with auth variants",
			yaml:    `name: ok-name` + "\nversion: '1.0'\nhandler: ./handler\ntools: []\nservices:\n  http:\n    tls:\n      client_cert: /tmp/cert.pem\n      client_key: /tmp/key.pem\n  auth:\n    select: auto\n    variants:\n      v1:\n        type: bearer\n        token: xyz",
			wantErr: "client_cert (mTLS) and services.auth cannot both be set",
		},
		{
			name:    "tool description too long",
			yaml:    `name: ok-name` + "\nversion: '1.0'\nhandler: ./handler\ntools:\n  - name: big_tool\n    description: '" + strings.Repeat("x", 4097) + "'",
			wantErr: "description exceeds",
		},
		{
			name:    "param description too long",
			yaml:    `name: ok-name` + "\nversion: '1.0'\nhandler: ./handler\ntools:\n  - name: ok_tool\n    description: short\n    params:\n      arg1:\n        type: string\n        description: '" + strings.Repeat("x", 1025) + "'",
			wantErr: "description exceeds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, "plugin.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0o644); err != nil { //nolint:gosec // test config file
				t.Fatal(err)
			}
			_, err := LoadManifest(path)
			if err == nil {
				t.Fatal("expected error")
			}
			if tt.wantErr != "" {
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}

func TestDescriptionLengthAtLimit(t *testing.T) {
	dir := t.TempDir()
	handlerPath := filepath.Join(dir, "handler")
	if err := os.WriteFile(handlerPath, []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}

	yaml := `name: ok-name` + "\nversion: '1.0'\nhandler: ./handler\ntools:\n  - name: ok_tool\n    description: '" + strings.Repeat("x", 4096) + "'\n    params:\n      arg1:\n        type: string\n        description: '" + strings.Repeat("x", 1024) + "'"
	path := filepath.Join(dir, "plugin.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil { //nolint:gosec // test config file
		t.Fatal(err)
	}
	_, err := LoadManifest(path)
	if err != nil {
		t.Errorf("descriptions at exact limit should be accepted: %v", err)
	}
}

func TestParamsSchema(t *testing.T) {
	tool := ToolDef{
		Name: "test",
		Params: map[string]ParamDef{
			"query": {
				Type:        "string",
				Description: "Search query",
				Required:    true,
			},
			"limit": {
				Type:    "integer",
				Default: 10,
			},
			"fields": {
				Type:  "array",
				Items: &ItemsDef{Type: "string"},
			},
		},
	}

	schema := tool.ParamsSchema()

	if schema["type"] != "object" {
		t.Errorf("type = %v", schema["type"])
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties is not a map")
	}
	if len(props) != 3 {
		t.Errorf("got %d properties, want 3", len(props))
	}

	queryProp := props["query"].(map[string]any)
	if queryProp["type"] != "string" {
		t.Errorf("query type = %v", queryProp["type"])
	}
	if queryProp["description"] != "Search query" {
		t.Errorf("query description = %v", queryProp["description"])
	}

	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatal("required is not a string slice")
	}
	if len(required) != 1 || required[0] != "query" {
		t.Errorf("required = %v, want [query]", required)
	}

	fieldsProp := props["fields"].(map[string]any)
	items, ok := fieldsProp["items"].(map[string]any)
	if !ok {
		t.Fatal("fields items is not a map")
	}
	if items["type"] != "string" {
		t.Errorf("fields items type = %v", items["type"])
	}
}

func TestManifestAuthVariantOrder(t *testing.T) {
	dir := t.TempDir()
	handlerPath := filepath.Join(dir, "handler")
	if err := os.WriteFile(handlerPath, []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}

	manifest := `
name: test-auth
version: "1.0.0"
description: "Auth variants test"
handler: ./handler
tools: []
services:
  auth:
    select: auto
    variants:
      cloud:
        type: basic
        username: user
        password: pass
      server-token:
        type: bearer
        token: tok
      server-kerberos:
        type: bearer
        token: tok2
`
	path := filepath.Join(dir, "plugin.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil { //nolint:gosec // test config file
		t.Fatal(err)
	}

	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}

	expected := []string{"cloud", "server-token", "server-kerberos"}
	if len(m.Services.Auth.VariantOrder) != len(expected) {
		t.Fatalf("VariantOrder = %v, want %v", m.Services.Auth.VariantOrder, expected)
	}
	for i, name := range expected {
		if m.Services.Auth.VariantOrder[i] != name {
			t.Errorf("VariantOrder[%d] = %q, want %q", i, m.Services.Auth.VariantOrder[i], name)
		}
	}
}

func TestProvidesAuth(t *testing.T) {
	m := &Manifest{}
	if m.ProvidesAuth() {
		t.Error("empty manifest should not provide auth")
	}

	m.Provides.Auth = &ProvidesAuthConfig{Type: "custom-sso/v1"}
	if !m.ProvidesAuth() {
		t.Error("should provide auth")
	}
}

func TestManifestSetup(t *testing.T) {
	dir := t.TempDir()
	handlerPath := filepath.Join(dir, "handler")
	if err := os.WriteFile(handlerPath, []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}

	manifest := `
name: test-setup
version: "1.0.0"
description: "Setup section test"
handler: ./handler
tools:
  - name: test_validate
    description: "validation tool"
    params: {}

setup:
  credentials:
    API_URL:
      description: "Base URL"
      example: "https://api.example.com"
      secret: false
    API_TOKEN:
      description: "API token"
      help_url: "https://example.com/tokens"
      instructions: "Create a token in Settings > API."
      secret: true
  variants:
    cloud:
      label: "Cloud"
      description: "Hosted instance"
      required: [API_URL, API_TOKEN]
    server:
      label: "Self-hosted"
      description: "On-premise"
      required: [API_URL, API_TOKEN]
  validation_tool: test_validate
  post_setup_message: "Restart for changes to take effect."
`
	path := filepath.Join(dir, "plugin.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil { //nolint:gosec // test config file
		t.Fatal(err)
	}

	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}

	if len(m.Setup.Credentials) != 2 {
		t.Fatalf("got %d credentials, want 2", len(m.Setup.Credentials))
	}

	url := m.Setup.Credentials["API_URL"]
	if url.Description != "Base URL" {
		t.Errorf("API_URL description = %q", url.Description)
	}
	if url.Example != "https://api.example.com" {
		t.Errorf("API_URL example = %q", url.Example)
	}
	if url.Secret {
		t.Error("API_URL should not be secret")
	}

	token := m.Setup.Credentials["API_TOKEN"]
	if !token.Secret {
		t.Error("API_TOKEN should be secret")
	}
	if token.HelpURL != "https://example.com/tokens" {
		t.Errorf("API_TOKEN help_url = %q", token.HelpURL)
	}

	if len(m.Setup.Variants) != 2 {
		t.Fatalf("got %d variants, want 2", len(m.Setup.Variants))
	}

	cloud := m.Setup.Variants["cloud"]
	if cloud.Label != "Cloud" {
		t.Errorf("cloud label = %q", cloud.Label)
	}
	if len(cloud.Required) != 2 {
		t.Errorf("cloud required = %v, want 2 items", cloud.Required)
	}

	if m.Setup.ValidationTool != "test_validate" {
		t.Errorf("ValidationTool = %q", m.Setup.ValidationTool)
	}
	if m.Setup.PostSetupMessage != "Restart for changes to take effect." {
		t.Errorf("PostSetupMessage = %q", m.Setup.PostSetupMessage)
	}
}

func TestValidateDomain(t *testing.T) {
	valid := []string{
		"api.example.com",
		"jira.corp.redhat.com",
		"sub.domain.co.uk",
	}
	for _, d := range valid {
		if err := validateDomain(d); err != nil {
			t.Errorf("validateDomain(%q) = %v, want nil", d, err)
		}
	}

	invalid := []struct {
		domain  string
		wantErr string
	}{
		{"", "empty domain"},
		{"*.example.com", "wildcards"},
		{"localhost", "localhost"},
		{"127.0.0.1", "IP addresses"},
		{"192.168.1.1", "IP addresses"},
		{"10.0.0.1", "IP addresses"},
		{"::1", "IP addresses"},
		{"[::1]", "IP addresses"},
	}
	for _, tt := range invalid {
		err := validateDomain(tt.domain)
		if err == nil {
			t.Errorf("validateDomain(%q) = nil, want error", tt.domain)
			continue
		}
		if !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("validateDomain(%q) = %q, want substring %q", tt.domain, err, tt.wantErr)
		}
	}
}

func TestManifestValidationAllowedDomains(t *testing.T) {
	dir := t.TempDir()
	handlerPath := filepath.Join(dir, "handler")
	if err := os.WriteFile(handlerPath, []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}

	manifest := `
name: test-domains
version: "1.0.0"
handler: ./handler
tools: []
services:
  http:
    base_url: "https://api.example.com"
    allowed_domains:
      - "127.0.0.1"
`
	path := filepath.Join(dir, "plugin.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil { //nolint:gosec // test config file
		t.Fatal(err)
	}

	_, err := LoadManifest(path)
	if err == nil {
		t.Fatal("expected error for IP in allowed_domains")
	}
	if !strings.Contains(err.Error(), "IP addresses") {
		t.Errorf("error = %q, want substring 'IP addresses'", err.Error())
	}
}

func TestManifestAllowPrivateIPsValid(t *testing.T) {
	dir := t.TempDir()
	handlerPath := filepath.Join(dir, "handler")
	if err := os.WriteFile(handlerPath, []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}

	manifest := `
name: test-private
version: "1.0.0"
handler: ./handler
tools: []
services:
  http:
    base_url: "https://internal.corp.com"
    allowed_domains:
      - "internal.corp.com"
      - "auth.corp.com"
    allow_private_ips: true
`
	path := filepath.Join(dir, "plugin.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil { //nolint:gosec // test config file
		t.Fatal(err)
	}

	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}
	if !m.Services.HTTP.AllowPrivateIPs {
		t.Error("AllowPrivateIPs should be true")
	}
	if len(m.Services.HTTP.AllowedDomains) != 2 {
		t.Errorf("AllowedDomains = %v, want 2 entries", m.Services.HTTP.AllowedDomains)
	}
}

func TestManifestTLSConfigValid(t *testing.T) {
	dir := t.TempDir()
	handlerPath := filepath.Join(dir, "handler")
	if err := os.WriteFile(handlerPath, []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}

	manifest := `
name: test-tls
version: "1.0.0"
handler: ./handler
tools: []
services:
  http:
    base_url: "https://service.example.com"
    tls:
      ca_cert: "${CA_CERT}"
      client_cert: "${CLIENT_CERT}"
      client_key: "${CLIENT_KEY}"
`
	path := filepath.Join(dir, "plugin.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil { //nolint:gosec // test config file
		t.Fatal(err)
	}

	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}
	if m.Services.HTTP.TLS.CACert != "${CA_CERT}" {
		t.Errorf("CACert = %q, want ${CA_CERT}", m.Services.HTTP.TLS.CACert)
	}
	if m.Services.HTTP.TLS.ClientCert != "${CLIENT_CERT}" {
		t.Errorf("ClientCert = %q, want ${CLIENT_CERT}", m.Services.HTTP.TLS.ClientCert)
	}
}

func TestManifestAllowedDomainsTemplateSkipped(t *testing.T) {
	dir := t.TempDir()
	handlerPath := filepath.Join(dir, "handler")
	if err := os.WriteFile(handlerPath, []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}

	// Template entries like ${VAR} should not be validated by validateDomain
	// since they are resolved later at load time.
	manifest := `
name: test-template
version: "1.0.0"
handler: ./handler
tools: []
services:
  http:
    base_url: "https://api.example.com"
    allowed_domains:
      - "${REGISTRAR_URL:-https://localhost:8891}"
      - api.example.com
`
	path := filepath.Join(dir, "plugin.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil { //nolint:gosec // test config file
		t.Fatal(err)
	}

	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}
	if len(m.Services.HTTP.AllowedDomains) != 2 {
		t.Errorf("AllowedDomains = %v, want 2 entries", m.Services.HTTP.AllowedDomains)
	}
}

func TestToolDefAccess(t *testing.T) {
	read := ToolDef{Name: "search", Access: "read"}
	if !read.IsReadOnly() {
		t.Error("access=read should be read-only")
	}

	write := ToolDef{Name: "create", Access: "write"}
	if write.IsReadOnly() {
		t.Error("access=write should not be read-only")
	}

	unset := ToolDef{Name: "default"}
	if unset.IsReadOnly() {
		t.Error("unset access should default to write (not read-only)")
	}
}

func TestToolDefLocalWrite(t *testing.T) {
	readLW := ToolDef{Name: "export", Access: "read", LocalWrite: true}
	if !readLW.IsReadOnly() {
		t.Error("access=read + local_write=true should still be read-only")
	}
}

func TestManifestValidateConcurrencyMixedAccess(t *testing.T) {
	base := func(concurrency int, access1, access2 string) *Manifest {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "handler"), []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec // test needs executable
			t.Fatal(err)
		}
		return &Manifest{
			Name:        "test",
			Handler:     "handler",
			Execution:   "persistent",
			Concurrency: concurrency,
			Dir:         dir,
			Tools: []ToolDef{
				{Name: "tool_a", Description: "d", Access: access1},
				{Name: "tool_b", Description: "d", Access: access2},
			},
		}
	}

	if err := base(2, "read", "write").Validate(); err == nil {
		t.Error("concurrency=2 with mixed access should fail")
	} else if !strings.Contains(err.Error(), "same access level") {
		t.Errorf("wrong error: %v", err)
	}

	if err := base(2, "read", "read").Validate(); err != nil {
		t.Errorf("concurrency=2 with uniform read should pass: %v", err)
	}

	if err := base(2, "write", "write").Validate(); err != nil {
		t.Errorf("concurrency=2 with uniform write should pass: %v", err)
	}

	if err := base(1, "read", "write").Validate(); err != nil {
		t.Errorf("concurrency=1 with mixed access should pass: %v", err)
	}
}

func TestManifestValidateConcurrencyMixedLocalWrite(t *testing.T) {
	base := func(lw1, lw2 bool) *Manifest {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "handler"), []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec // test needs executable
			t.Fatal(err)
		}
		return &Manifest{
			Name:        "test",
			Handler:     "handler",
			Execution:   "persistent",
			Concurrency: 2,
			Dir:         dir,
			Tools: []ToolDef{
				{Name: "tool_a", Description: "d", Access: "read", LocalWrite: lw1},
				{Name: "tool_b", Description: "d", Access: "read", LocalWrite: lw2},
			},
		}
	}

	if err := base(true, false).Validate(); err == nil {
		t.Error("concurrency=2 with mixed local_write should fail")
	} else if !strings.Contains(err.Error(), "same local_write") {
		t.Errorf("wrong error: %v", err)
	}

	if err := base(true, true).Validate(); err != nil {
		t.Errorf("concurrency=2 with uniform local_write=true should pass: %v", err)
	}

	if err := base(false, false).Validate(); err != nil {
		t.Errorf("concurrency=2 with uniform local_write=false should pass: %v", err)
	}
}

func TestManifestValidateLocalWrite(t *testing.T) {
	base := func(access string, localWrite bool) *Manifest {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "handler"), []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec // test needs executable
			t.Fatal(err)
		}
		return &Manifest{
			Name:      "test",
			Handler:   "handler",
			Execution: "oneshot",
			Dir:       dir,
			Tools: []ToolDef{{
				Name:        "t",
				Description: "d",
				Access:      access,
				LocalWrite:  localWrite,
			}},
		}
	}

	if err := base("read", true).Validate(); err != nil {
		t.Errorf("access=read + local_write=true should pass: %v", err)
	}
	if err := base("read", false).Validate(); err != nil {
		t.Errorf("access=read + local_write=false should pass: %v", err)
	}
	if err := base("write", true).Validate(); err == nil {
		t.Error("access=write + local_write=true should fail")
	}
	if err := base("", true).Validate(); err == nil {
		t.Error("access='' + local_write=true should fail")
	}
	if err := base("write", false).Validate(); err != nil {
		t.Errorf("access=write + local_write=false should pass: %v", err)
	}
}

func TestToolDefVisibility(t *testing.T) {
	primary := ToolDef{Name: "search", Visibility: "primary"}
	if !primary.IsPrimary() {
		t.Error("visibility=primary should be primary")
	}

	deferred := ToolDef{Name: "export", Visibility: "deferred"}
	if deferred.IsPrimary() {
		t.Error("visibility=deferred should not be primary")
	}

	unset := ToolDef{Name: "default"}
	if unset.IsPrimary() {
		t.Error("unset visibility should default to deferred (not primary)")
	}
}

func TestManifestHandlerSymlinkEscape(t *testing.T) {
	dir := t.TempDir()

	// Create a handler outside the plugin dir
	outsideDir := t.TempDir()
	outsideHandler := filepath.Join(outsideDir, "evil")
	if err := os.WriteFile(outsideHandler, []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec // test needs executable
		t.Fatal(err)
	}

	// Create a symlink inside the plugin dir pointing outside
	if err := os.Symlink(outsideHandler, filepath.Join(dir, "handler")); err != nil {
		t.Fatal(err)
	}

	manifest := `
name: symlink-escape
version: "1.0.0"
handler: ./handler
tools: []
`
	path := filepath.Join(dir, "plugin.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil { //nolint:gosec // test config file
		t.Fatal(err)
	}

	_, err := LoadManifest(path)
	if err == nil {
		t.Fatal("expected error for symlink escaping plugin dir")
	}
	if !strings.Contains(err.Error(), "escapes plugin directory") {
		t.Errorf("error = %q, want substring 'escapes plugin directory'", err.Error())
	}
}

func TestLoadManifest_SizeLimit(t *testing.T) {
	dir := t.TempDir()
	handlerPath := filepath.Join(dir, "handler.sh")
	if err := os.WriteFile(handlerPath, []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec // test
		t.Fatal(err)
	}

	header := "name: test-plugin\nversion: \"1.0\"\nhandler: ./handler.sh\n"

	t.Run("at limit accepted", func(t *testing.T) {
		padding := strings.Repeat("#", maxManifestSize-len(header))
		data := header + padding
		yamlPath := filepath.Join(dir, "at-limit.yaml")
		if err := os.WriteFile(yamlPath, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadManifest(yamlPath); err != nil {
			t.Errorf("manifest at size limit should be accepted: %v", err)
		}
	})

	t.Run("over limit rejected", func(t *testing.T) {
		padding := strings.Repeat("#", maxManifestSize-len(header)+1)
		data := header + padding
		yamlPath := filepath.Join(dir, "over-limit.yaml")
		if err := os.WriteFile(yamlPath, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadManifest(yamlPath)
		if err == nil {
			t.Fatal("manifest over size limit should be rejected")
		}
		if !strings.Contains(err.Error(), "byte limit") {
			t.Errorf("expected size limit error, got: %v", err)
		}
	})
}

func TestManifestCloneIndependence(t *testing.T) {
	boolTrue := true
	boolFalse := false
	original := &Manifest{
		Name:         "test",
		Enabled:      &boolTrue,
		DependsOn:    []string{"dep1"},
		Env:          []string{"VAR1"},
		ContextFiles: []string{"context.md"},
		Config:       map[string]string{"k": "v"},
		Tools: []ToolDef{
			{
				Name:   "tool1",
				Params: map[string]ParamDef{"p1": {Type: "string"}},
			},
		},
		Services: ServiceConfig{
			Auth: AuthServiceConfig{
				SPNEGOProactive: &boolFalse,
				Scopes:          []string{"scope1"},
				VariantOrder:    []string{"v1"},
				Variants: map[string]AuthServiceConfig{
					"v1": {
						Scopes: []string{"inner-scope"},
					},
				},
			},
			HTTP: HTTPServiceConfig{
				AllowedDomains: []string{"example.com"},
				TLS:            TLSConfig{CACertPEM: []byte("cert-data")},
			},
			Cache: CacheServiceConfig{
				Enabled: &boolTrue,
			},
		},
		Provides: ProvidesConfig{
			Auth: &ProvidesAuthConfig{Type: "custom", Description: "test auth"},
		},
		Setup: SetupConfig{
			Credentials: map[string]CredentialMeta{"cred1": {Description: "desc"}},
			Variants: map[string]SetupVariant{
				"sv1": {Required: []string{"req1"}},
			},
		},
	}

	pristine := original.Clone()
	clone := original.Clone()

	// Mutate every mutable field on the clone.
	clone.DependsOn[0] = "MUTATED"
	clone.Env[0] = "MUTATED"
	clone.ContextFiles[0] = "MUTATED"
	clone.Config["k"] = "MUTATED"
	clone.Tools[0].Params["p1"] = ParamDef{Type: "MUTATED"}
	clone.Services.Auth.Scopes[0] = "MUTATED"
	clone.Services.Auth.VariantOrder[0] = "MUTATED"
	clone.Services.Auth.Variants["v1"] = AuthServiceConfig{Scopes: []string{"MUTATED"}}
	clone.Services.HTTP.AllowedDomains[0] = "MUTATED"
	clone.Services.HTTP.TLS.CACertPEM[0] = 'X'
	clone.Setup.Credentials["cred1"] = CredentialMeta{Description: "MUTATED"}
	clone.Setup.Variants["sv1"] = SetupVariant{Required: []string{"MUTATED"}}

	// Mutate pointer fields on the clone.
	*clone.Enabled = false
	*clone.Services.Auth.SPNEGOProactive = true
	*clone.Services.Cache.Enabled = false
	clone.Provides.Auth.Type = "MUTATED"

	if !reflect.DeepEqual(original, pristine) {
		t.Error("mutating clone should not affect original")
	}
}

func TestValidateUserHandlerRejectsSymlink(t *testing.T) {
	pluginDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideHandler := filepath.Join(outsideDir, "evil")
	if err := os.WriteFile(outsideHandler, []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	if err := os.Symlink(outsideHandler, filepath.Join(pluginDir, "handler")); err != nil {
		t.Fatal(err)
	}

	m := &Manifest{
		Handler:      "./handler",
		Dir:          pluginDir,
		IsUserPlugin: true,
	}
	err := ValidateUserHandler(m)
	if err == nil {
		t.Fatal("expected error for symlink handler in user plugin")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error = %q, want substring 'symlink'", err.Error())
	}
}

func TestValidateUserHandlerAllowsSymlinkInSystemPlugin(t *testing.T) {
	pluginDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideHandler := filepath.Join(outsideDir, "handler")
	if err := os.WriteFile(outsideHandler, []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	if err := os.Symlink(outsideHandler, filepath.Join(pluginDir, "handler")); err != nil {
		t.Fatal(err)
	}

	m := &Manifest{
		Handler:      "./handler",
		Dir:          pluginDir,
		IsUserPlugin: false,
	}
	err := ValidateUserHandler(m)
	if err != nil {
		t.Errorf("system plugin symlink should be allowed, got %v", err)
	}
}

func TestValidateUserHandlerRejectsDanglingSymlink(t *testing.T) {
	pluginDir := t.TempDir()

	if err := os.Symlink("/nonexistent/target", filepath.Join(pluginDir, "handler")); err != nil {
		t.Fatal(err)
	}

	m := &Manifest{
		Handler:      "./handler",
		Dir:          pluginDir,
		IsUserPlugin: true,
	}
	err := ValidateUserHandler(m)
	if err == nil {
		t.Fatal("expected error for dangling symlink handler in user plugin")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error = %q, want substring 'symlink'", err.Error())
	}
}

func TestValidateUserHandlerAllowsRegularFile(t *testing.T) {
	pluginDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pluginDir, "handler"), []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	m := &Manifest{
		Handler:      "./handler",
		Dir:          pluginDir,
		IsUserPlugin: true,
	}
	if err := ValidateUserHandler(m); err != nil {
		t.Errorf("regular file handler in user plugin should be allowed, got %v", err)
	}
}

func TestValidateUserHandlerNonexistentRejects(t *testing.T) {
	pluginDir := t.TempDir()

	m := &Manifest{
		Handler:      "./handler",
		Dir:          pluginDir,
		IsUserPlugin: true,
	}
	err := ValidateUserHandler(m)
	if err == nil {
		t.Fatal("nonexistent handler should be rejected")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected 'does not exist' error, got %v", err)
	}
}

func TestValidateUserHandlerIntermediateDirSymlink(t *testing.T) {
	pluginDir := t.TempDir()
	outsideDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(outsideDir, "bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "bin", "handler"), []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	if err := os.Symlink(filepath.Join(outsideDir, "bin"), filepath.Join(pluginDir, "bin")); err != nil {
		t.Fatal(err)
	}

	m := &Manifest{
		Handler:      "./bin/handler",
		Dir:          pluginDir,
		IsUserPlugin: true,
	}
	err := ValidateUserHandler(m)
	if err == nil {
		t.Fatal("expected error for handler via symlinked intermediate directory")
	}
	if !strings.Contains(err.Error(), "escapes plugin directory") {
		t.Errorf("error = %q, want 'escapes plugin directory'", err.Error())
	}
}

func TestClonePreservesIsUserPlugin(t *testing.T) {
	m := &Manifest{
		Name:         "test",
		IsUserPlugin: true,
	}
	c := m.Clone()
	if !c.IsUserPlugin {
		t.Error("Clone() should preserve IsUserPlugin")
	}
}

func TestValidateUserHandlerRejectsHardlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hardlink detection not available on Windows")
	}
	pluginDir := t.TempDir()
	handler := filepath.Join(pluginDir, "handler")
	if err := os.WriteFile(handler, []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	hardlink := filepath.Join(pluginDir, "handler-link")
	if err := os.Link(handler, hardlink); err != nil {
		t.Fatal(err)
	}

	m := &Manifest{
		Handler:      "./handler",
		Dir:          pluginDir,
		IsUserPlugin: true,
	}
	err := ValidateUserHandler(m)
	if err == nil {
		t.Fatal("expected error for hardline handler in user plugin")
	}
	if !strings.Contains(err.Error(), "hardlink") {
		t.Errorf("error = %q, want substring 'hardlink'", err.Error())
	}
}

func TestValidateUserHandlerAllowsSingleLinkFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hardlink detection not available on Windows")
	}
	pluginDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pluginDir, "handler"), []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	m := &Manifest{
		Handler:      "./handler",
		Dir:          pluginDir,
		IsUserPlugin: true,
	}
	if err := ValidateUserHandler(m); err != nil {
		t.Errorf("single-link file should be allowed, got %v", err)
	}
}

// ─── validateHandlerAtLaunch Tests ───────────────────────────────

func TestValidateHandlerAtLaunchSkipsSystemPlugin(t *testing.T) {
	m := &Manifest{
		Handler:      "./handler",
		Dir:          t.TempDir(),
		IsUserPlugin: false,
	}
	if err := validateHandlerAtLaunch(m); err != nil {
		t.Errorf("system plugin should skip launch validation, got %v", err)
	}
}

func TestValidateHandlerAtLaunchAllowsRegularFile(t *testing.T) {
	pluginDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pluginDir, "handler"), []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	m := &Manifest{
		Handler:      "./handler",
		Dir:          pluginDir,
		IsUserPlugin: true,
	}
	if err := validateHandlerAtLaunch(m); err != nil {
		t.Errorf("regular handler should pass, got %v", err)
	}
}

func TestValidateHandlerAtLaunchRejectsSymlink(t *testing.T) {
	pluginDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideHandler := filepath.Join(outsideDir, "evil")
	if err := os.WriteFile(outsideHandler, []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	if err := os.Symlink(outsideHandler, filepath.Join(pluginDir, "handler")); err != nil {
		t.Fatal(err)
	}
	m := &Manifest{
		Handler:      "./handler",
		Dir:          pluginDir,
		IsUserPlugin: true,
	}
	err := validateHandlerAtLaunch(m)
	if err == nil {
		t.Fatal("expected error for symlink handler at launch")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error = %q, want substring 'symlink'", err.Error())
	}
}

func TestValidateHandlerAtLaunchRejectsNonexistent(t *testing.T) {
	m := &Manifest{
		Handler:      "./handler",
		Dir:          t.TempDir(),
		IsUserPlugin: true,
	}
	err := validateHandlerAtLaunch(m)
	if err == nil {
		t.Fatal("expected error for nonexistent handler at launch")
	}
	if !strings.Contains(err.Error(), "lstat handler at launch") {
		t.Errorf("error = %q, want substring 'lstat handler at launch'", err.Error())
	}
}

func TestValidateHandlerAtLaunchRejectsIntermediateDirSymlink(t *testing.T) {
	pluginDir := t.TempDir()
	outsideDir := t.TempDir()
	realSubdir := filepath.Join(outsideDir, "real")
	if err := os.MkdirAll(realSubdir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realSubdir, "handler"), []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	if err := os.Symlink(realSubdir, filepath.Join(pluginDir, "sub")); err != nil {
		t.Fatal(err)
	}
	m := &Manifest{
		Handler:      "./sub/handler",
		Dir:          pluginDir,
		IsUserPlugin: true,
	}
	err := validateHandlerAtLaunch(m)
	if err == nil {
		t.Fatal("expected error for handler through intermediate symlink dir")
	}
	if !strings.Contains(err.Error(), "escapes plugin directory at launch") {
		t.Errorf("error = %q, want substring 'escapes plugin directory at launch'", err.Error())
	}
}

func TestValidateHandlerAtLaunchRejectsHardlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hardlink detection not available on Windows")
	}
	pluginDir := t.TempDir()
	handler := filepath.Join(pluginDir, "handler")
	if err := os.WriteFile(handler, []byte("#!/bin/bash\n"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	hardlink := filepath.Join(pluginDir, "handler-link")
	if err := os.Link(handler, hardlink); err != nil {
		t.Fatal(err)
	}
	m := &Manifest{
		Handler:      "./handler",
		Dir:          pluginDir,
		IsUserPlugin: true,
	}
	err := validateHandlerAtLaunch(m)
	if err == nil {
		t.Fatal("expected error for hardlink handler at launch")
	}
	if !strings.Contains(err.Error(), "hardlink") {
		t.Errorf("error = %q, want substring 'hardlink'", err.Error())
	}
}

// ─── Fuzz Tests ──────────────────────────────────────────────────

var pluginNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}[a-z0-9]$`)

func FuzzValidatePluginName(f *testing.F) {
	f.Add("jira")
	f.Add("google-drive")
	f.Add("my_plugin")
	f.Add("a")
	f.Add("")
	f.Add("A")
	f.Add("-bad")
	f.Add("bad-")
	f.Add("has space")
	f.Add("has.dot")
	f.Add(strings.Repeat("a", 100))
	f.Add("münchen")
	f.Add("a\x00b")

	f.Fuzz(func(t *testing.T, name string) {
		err := ValidatePluginName(name)
		if err != nil {
			return
		}
		if !pluginNameRE.MatchString(name) {
			t.Fatalf("ValidatePluginName(%q) passed but does not match pattern %s",
				name, pluginNameRE.String())
		}
	})
}
