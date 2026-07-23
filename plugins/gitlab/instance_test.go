package main

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	gogitlab "gitlab.com/gitlab-org/api/client-go"
)

func TestScanMultiInstanceNone(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "tok")
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		if key != "GITLAB_TOKEN" && key != "GITLAB_URL" &&
			len(key) > 7 && key[:7] == "GITLAB_" && key[len(key)-6:] == "_TOKEN" {
			t.Setenv(key, "")
		}
	}

	entries := scanMultiInstance()
	if len(entries) != 0 {
		t.Errorf("expected no multi-instance, got %v", entries)
	}
}

func TestScanMultiInstanceFound(t *testing.T) {
	t.Setenv("GITLAB_PUBLIC_TOKEN", "pub-token")
	t.Setenv("GITLAB_PUBLIC_URL", "https://gitlab.com")
	t.Setenv("GITLAB_INTERNAL_TOKEN", "int-token")
	t.Setenv("GITLAB_INTERNAL_URL", "https://gitlab.cee.redhat.com")

	entries := scanMultiInstance()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	found := map[string]bool{}
	for _, e := range entries {
		found[e.Name] = true
		if e.Name == "public" {
			if e.URL != "https://gitlab.com" {
				t.Errorf("public URL = %q", e.URL)
			}
			if e.TokenVar != "GITLAB_PUBLIC_TOKEN" {
				t.Errorf("public TokenVar = %q", e.TokenVar)
			}
		}
		if e.Name == "internal" {
			if e.URL != "https://gitlab.cee.redhat.com" {
				t.Errorf("internal URL = %q", e.URL)
			}
			if e.TokenVar != "GITLAB_INTERNAL_TOKEN" {
				t.Errorf("internal TokenVar = %q", e.TokenVar)
			}
		}
	}
	if !found["public"] || !found["internal"] {
		t.Errorf("missing instance: found=%v", found)
	}
}

func TestScanMultiInstanceDefaultURL(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "")
	t.Setenv("GITLAB_MYINST_TOKEN", "tok")

	entries := scanMultiInstance()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].URL != "https://gitlab.com" {
		t.Errorf("URL = %q, want https://gitlab.com", entries[0].URL)
	}
}

func TestExtractHost(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://gitlab.com", "gitlab.com"},
		{"https://gitlab.example.com/path", "gitlab.example.com"},
		{"https://gitlab.internal:8443", "gitlab.internal"},
		{"", ""},
		{"://invalid", ""},
	}
	for _, tt := range tests {
		got := extractHost(tt.url)
		if got != tt.want {
			t.Errorf("extractHost(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestResolveInstanceSingle(t *testing.T) {
	ts := httptest.NewServer(nil)
	defer ts.Close()

	client, err := gogitlab.NewClient("", gogitlab.WithBaseURL(ts.URL+"/api/v4"))
	if err != nil {
		t.Fatal(err)
	}
	instances = map[string]*instance{
		"default": {Name: "default", URL: ts.URL, Client: client},
	}
	defaultInstance = "default"

	got, err := resolveInstance("")
	if err != nil {
		t.Fatalf("resolveInstance: %v", err)
	}
	if got == nil {
		t.Fatal("client is nil")
	}
}

func TestResolveInstanceMulti(t *testing.T) {
	ts := httptest.NewServer(nil)
	defer ts.Close()

	client1, _ := gogitlab.NewClient("", gogitlab.WithBaseURL(ts.URL+"/api/v4"))
	client2, _ := gogitlab.NewClient("", gogitlab.WithBaseURL(ts.URL+"/api/v4"))
	instances = map[string]*instance{
		"public":   {Name: "public", URL: ts.URL, Client: client1},
		"internal": {Name: "internal", URL: ts.URL, Client: client2},
	}
	defaultInstance = ""

	_, err := resolveInstance("")
	if err == nil {
		t.Fatal("expected error when no default with multi-instance")
	}

	got, err := resolveInstance("internal")
	if err != nil {
		t.Fatalf("resolveInstance(internal): %v", err)
	}
	if got == nil {
		t.Fatal("client is nil")
	}
}

func TestResolveInstanceUnknown(t *testing.T) {
	ts := httptest.NewServer(nil)
	defer ts.Close()

	client, err := gogitlab.NewClient("", gogitlab.WithBaseURL(ts.URL+"/api/v4"))
	if err != nil {
		t.Fatal(err)
	}
	instances = map[string]*instance{
		"default": {Name: "default", URL: ts.URL, Client: client},
	}
	defaultInstance = "default"

	_, err = resolveInstance("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown instance")
	}
}

func TestParseTime(t *testing.T) {
	if pt, err := parseTime("2026-03-10T14:00:00Z"); pt == nil || err != nil {
		t.Errorf("parseTime(RFC3339) = %v, %v", pt, err)
	}
	if pt, err := parseTime("2026-03-10"); pt == nil || err != nil {
		t.Errorf("parseTime(date) = %v, %v", pt, err)
	}
	if pt, err := parseTime("not-a-date"); pt != nil || err == nil {
		t.Error("parseTime(invalid) should return nil and error")
	}
	if pt, err := parseTime(""); pt != nil || err != nil {
		t.Errorf("parseTime(empty) = %v, %v; want nil, nil", pt, err)
	}
}
