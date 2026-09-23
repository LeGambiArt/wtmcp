package main

import (
	"testing"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/profile"
)

// newTestResolver builds a resolver with two profiles ("read-only" and
// "full") and the given default profile name.
func newTestResolver(t *testing.T, defaultProfile string) *profile.Resolver {
	t.Helper()
	loaded := &config.ProfileLoadResult{
		Definitions: map[string]config.ProfileDefinition{
			"read-only": {Allow: map[string][]string{"*": {"_get_.*"}}},
			"full":      {Allow: map[string][]string{"*": {".*"}}},
		},
	}
	r, err := profile.NewResolver(defaultProfile, loaded)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	return r
}

func TestResolveStdioFilter(t *testing.T) {
	tests := []struct {
		name           string
		defaultProfile string
		profileFlag    string
		wantProfile    string // filter.Name(); "" means nil filter (fail-open)
		wantErr        bool
	}{
		{
			name:           "no flag, no default => fail-open (nil filter)",
			defaultProfile: "",
			profileFlag:    "",
			wantProfile:    "",
		},
		{
			name:           "no flag, default set => default profile applied",
			defaultProfile: "read-only",
			profileFlag:    "",
			wantProfile:    "read-only",
		},
		{
			name:           "flag set => flag profile applied",
			defaultProfile: "",
			profileFlag:    "full",
			wantProfile:    "full",
		},
		{
			name:           "flag overrides default",
			defaultProfile: "read-only",
			profileFlag:    "full",
			wantProfile:    "full",
		},
		{
			name:           "flag names undefined profile => error",
			defaultProfile: "read-only",
			profileFlag:    "nope",
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestResolver(t, tt.defaultProfile)
			filter, err := resolveStdioFilter(r, tt.profileFlag)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got filter=%v", profile.FilterName(filter))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantProfile == "" {
				if filter != nil {
					t.Fatalf("expected nil filter (fail-open), got %q", filter.Name())
				}
				return
			}
			if filter == nil {
				t.Fatalf("expected filter %q, got nil (fail-open)", tt.wantProfile)
			}
			if got := filter.Name(); got != tt.wantProfile {
				t.Fatalf("filter = %q, want %q", got, tt.wantProfile)
			}
		})
	}
}

// TestProfileTransportOptionsStdioDefault verifies the end-to-end wiring:
// with a default profile configured and no --profile flag, stdio gets a
// transport option (the filter is injected), and with neither, it does
// not (fail-open).
func TestProfileTransportOptionsStdioDefault(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.Transport = config.TransportStdio

	t.Run("default profile yields a transport option", func(t *testing.T) {
		r := newTestResolver(t, "read-only")
		opts, err := profileTransportOptions(cfg, r, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(opts) != 1 {
			t.Fatalf("expected 1 transport option, got %d", len(opts))
		}
	})

	t.Run("no default, no flag stays fail-open", func(t *testing.T) {
		r := newTestResolver(t, "")
		opts, err := profileTransportOptions(cfg, r, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if opts != nil {
			t.Fatalf("expected no transport options (fail-open), got %d", len(opts))
		}
	})
}
