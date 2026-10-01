package profile

import (
	"testing"

	"github.com/LeGambiArt/wtmcp/internal/config"
)

func mustCompile(t *testing.T, name string, def config.ProfileDefinition) *Filter {
	t.Helper()
	f, err := compileFilter(name, def)
	if err != nil {
		t.Fatalf("compileFilter(%s): %v", name, err)
	}
	return f
}

func TestFilterIsAllowed(t *testing.T) {
	f := mustCompile(t, "code-review", config.ProfileDefinition{
		Allow: map[string][]string{
			"gitlab": {"gitlab_get_.*", "gitlab_list_.*"},
			"jira":   {"jira_get_issue"},
		},
		Deny: map[string][]string{
			"gitlab": {"gitlab_get_secret"},
		},
	})

	tests := []struct {
		plugin, tool string
		want         bool
	}{
		{"gitlab", "gitlab_get_project", true},       // allow match
		{"gitlab", "gitlab_list_mrs", true},          // allow match
		{"gitlab", "gitlab_delete_branch", false},    // not allowed
		{"gitlab", "gitlab_get_secret", false},       // deny takes precedence
		{"jira", "jira_get_issue", true},             // exact allow
		{"jira", "jira_get_issues", false},           // anchored: no suffix match
		{"jira", "xjira_get_issue", false},           // anchored: no prefix match
		{"confluence", "confluence_get_page", false}, // plugin not in allow
	}
	for _, tt := range tests {
		if got := f.IsAllowed(tt.plugin, tt.tool); got != tt.want {
			t.Errorf("IsAllowed(%q, %q) = %v, want %v", tt.plugin, tt.tool, got, tt.want)
		}
	}
}

func TestFilterWildcardPlugin(t *testing.T) {
	f := mustCompile(t, "full", config.ProfileDefinition{
		Allow: map[string][]string{"*": {".*"}},
	})
	if !f.IsAllowed("anything", "any_tool") {
		t.Errorf("wildcard should allow any tool")
	}
	if !f.IsAllowed("gitlab", "gitlab_delete_everything") {
		t.Errorf("wildcard should allow any tool")
	}
}

func TestFilterDenyPrecedence(t *testing.T) {
	f := mustCompile(t, "p", config.ProfileDefinition{
		Allow: map[string][]string{"*": {".*"}},
		Deny:  map[string][]string{"gitlab": {"gitlab_delete_.*"}},
	})
	if f.IsAllowed("gitlab", "gitlab_delete_branch") {
		t.Errorf("deny should take precedence over wildcard allow")
	}
	if !f.IsAllowed("gitlab", "gitlab_get_project") {
		t.Errorf("non-denied tool should be allowed")
	}
}

func TestFilterExemptTools(t *testing.T) {
	// A deny-all filter allows only exempt introspection tools.
	f := newDenyAllFilter()
	for _, tool := range []string{"plugin_list", "tool_stats", "tool_search"} {
		if !f.IsAllowed("", tool) {
			t.Errorf("%q should be exempt (always allowed)", tool)
		}
	}
	// plugin_reload is NOT exempt.
	if f.IsAllowed("", "plugin_reload") {
		t.Errorf("plugin_reload must NOT be exempt")
	}
	// A random tool is denied.
	if f.IsAllowed("gitlab", "gitlab_get_project") {
		t.Errorf("deny-all should deny normal tools")
	}
}

func TestFilterExemptEvenWhenNotAllowed(t *testing.T) {
	// Even a restrictive profile lets exempt tools through.
	f := mustCompile(t, "p", config.ProfileDefinition{
		Allow: map[string][]string{"gitlab": {"gitlab_get_.*"}},
	})
	if !f.IsAllowed("", "tool_search") {
		t.Errorf("tool_search must always be allowed")
	}
	if f.IsAllowed("", "plugin_reload") {
		t.Errorf("plugin_reload must obey the profile (not exempt)")
	}
}

func TestFilterEmptyAllowDeniesAll(t *testing.T) {
	f := mustCompile(t, "p", config.ProfileDefinition{})
	if f.IsAllowed("gitlab", "gitlab_get_project") {
		t.Errorf("empty allow should deny everything (except exempt)")
	}
	if !f.IsAllowed("", "plugin_list") {
		t.Errorf("exempt tool should still pass")
	}
}

func TestFilterNilIsAllowed(t *testing.T) {
	var f *Filter
	// nil filter denies normal tools but exempts introspection.
	if f.IsAllowed("gitlab", "gitlab_get_project") {
		t.Errorf("nil filter should deny normal tools")
	}
	if !f.IsAllowed("", "tool_search") {
		t.Errorf("nil filter should still exempt tool_search")
	}
}

func TestCompileFilterInvalidRegexp(t *testing.T) {
	_, err := compileFilter("bad", config.ProfileDefinition{
		Allow: map[string][]string{"gitlab": {"gitlab_["}},
	})
	if err == nil {
		t.Fatalf("expected error for invalid regexp")
	}
}

func TestFilterName(t *testing.T) {
	if got := FilterName(nil); got != "none" {
		t.Errorf("FilterName(nil) = %q, want none", got)
	}
	if got := FilterName(newDenyAllFilter()); got != "deny-all" {
		t.Errorf("FilterName(denyAll) = %q, want deny-all", got)
	}
	f := mustCompile(t, "code-review", config.ProfileDefinition{})
	if got := FilterName(f); got != "code-review" {
		t.Errorf("FilterName(f) = %q, want code-review", got)
	}
	if got := f.Name(); got != "code-review" {
		t.Errorf("f.Name() = %q", got)
	}
}
