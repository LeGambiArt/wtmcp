package main

import (
	"reflect"
	"testing"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/plugin"
	"github.com/LeGambiArt/wtmcp/internal/profile"
)

// testManifests returns a fixed catalog: 3 jira tools + 2 confluence tools.
func testManifests() map[string]*plugin.Manifest {
	return map[string]*plugin.Manifest{
		"jira": {
			Name: "jira",
			Tools: []plugin.ToolDef{
				{Name: "create_issue"},
				{Name: "get_issue"},
				{Name: "delete_issue"},
			},
		},
		"confluence": {
			Name: "confluence",
			Tools: []plugin.ToolDef{
				{Name: "get_page"},
				{Name: "create_page"},
			},
		},
	}
}

// TestProfileListToolCount verifies the TOOLS column reports the number of
// discovered tools a profile can actually call, not a raw pattern count. In
// particular a ".*" scoped to a single plugin must not report "all".
func TestProfileListToolCount(t *testing.T) {
	defs := map[string]config.ProfileDefinition{
		// Wildcard plugin with ".*": grants every tool -> "all".
		"full": {Allow: map[string][]string{"*": {".*"}}},
		// ".*" under one plugin only: all jira tools, no confluence.
		"jira-only": {Allow: map[string][]string{"jira": {".*"}}},
		// Mixed: all jira tools + one confluence tool. Previously mislabeled
		// "all" because a ".*" appeared anywhere.
		"mixed": {Allow: map[string][]string{
			"jira":       {".*"},
			"confluence": {"get_page"},
		}},
		// Anchored pattern: only get_issue among jira's tools.
		"restricted": {Allow: map[string][]string{"jira": {"get_.*"}}},
		// Deny takes precedence: all jira tools minus delete_issue.
		"deny-subset": {
			Allow: map[string][]string{"jira": {".*"}},
			Deny:  map[string][]string{"jira": {"delete_.*"}},
		},
	}

	resolver, err := profile.NewResolver("", &config.ProfileLoadResult{Definitions: defs})
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	manifests := testManifests()

	// Catalog has 5 tools total (3 jira + 2 confluence); DENY is the count
	// of tools the profile cannot call (5 - allowed).
	tests := []struct {
		profile  string
		wantTool string
		wantDeny int
	}{
		{"full", "all", 0},
		{"jira-only", "3", 2},
		{"mixed", "4", 1},
		{"restricted", "1", 4},
		{"deny-subset", "2", 3},
	}
	for _, tt := range tests {
		t.Run(tt.profile, func(t *testing.T) {
			filter, ok := resolver.FilterByName(tt.profile)
			if !ok {
				t.Fatalf("FilterByName(%q): not found", tt.profile)
			}
			allowed, denied := classifyTools(filter, manifests)
			if got := toolCountLabel(len(allowed), len(allowed)+len(denied)); got != tt.wantTool {
				t.Errorf("TOOLS = %q, want %q (allowed=%v denied=%v)", got, tt.wantTool, allowed, denied)
			}
			if got := len(denied); got != tt.wantDeny {
				t.Errorf("DENY = %d, want %d (denied=%v)", got, tt.wantDeny, denied)
			}
		})
	}
}

func TestToolCountLabel(t *testing.T) {
	tests := []struct {
		allowed, total int
		want           string
	}{
		{5, 5, "all"}, // every tool -> all
		{3, 5, "3"},   // partial
		{0, 5, "0"},   // none
		{0, 0, "0"},   // no tools discovered: never "all"
	}
	for _, tt := range tests {
		if got := toolCountLabel(tt.allowed, tt.total); got != tt.want {
			t.Errorf("toolCountLabel(%d, %d) = %q, want %q", tt.allowed, tt.total, got, tt.want)
		}
	}
}

func TestPluginSummary(t *testing.T) {
	tests := []struct {
		name string
		def  config.ProfileDefinition
		want string
	}{
		{"one plugin", config.ProfileDefinition{Allow: map[string][]string{"jira": {".*"}}}, "1"},
		{"wildcard plugin", config.ProfileDefinition{Allow: map[string][]string{"*": {".*"}}}, "1 (*)"},
		{"wildcard plus named", config.ProfileDefinition{Allow: map[string][]string{"*": {".*"}, "jira": {"get_.*"}}}, "2 (*)"},
		{"empty", config.ProfileDefinition{}, "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pluginSummary(tt.def); got != tt.want {
				t.Errorf("pluginSummary = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyToolsSorted(t *testing.T) {
	resolver, err := profile.NewResolver("", &config.ProfileLoadResult{
		Definitions: map[string]config.ProfileDefinition{
			"jira-only": {Allow: map[string][]string{"jira": {".*"}}},
		},
	})
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	filter, ok := resolver.FilterByName("jira-only")
	if !ok {
		t.Fatal("FilterByName: not found")
	}
	allowed, denied := classifyTools(filter, testManifests())
	if want := []string{"create_issue", "delete_issue", "get_issue"}; !reflect.DeepEqual(allowed, want) {
		t.Errorf("allowed = %v, want %v", allowed, want)
	}
	if want := []string{"create_page", "get_page"}; !reflect.DeepEqual(denied, want) {
		t.Errorf("denied = %v, want %v", denied, want)
	}
}
