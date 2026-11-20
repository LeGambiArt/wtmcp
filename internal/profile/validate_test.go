package profile

import (
	"strings"
	"testing"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/plugin"
)

// findProblem returns the first problem whose message contains sub.
func findProblem(errs []config.ProfileLoadError, sub string) (config.ProfileLoadError, bool) {
	for _, e := range errs {
		if strings.Contains(e.Message, sub) {
			return e, true
		}
	}
	return config.ProfileLoadError{}, false
}

func TestValidateClean(t *testing.T) {
	loaded := &config.ProfileLoadResult{
		Definitions: map[string]config.ProfileDefinition{
			"code-review": {Allow: map[string][]string{"gitlab": {"gitlab_get_.*"}}},
		},
		DefSource: map[string]string{"code-review": "cr.yaml"},
		Rules: []config.ProfileRule{
			{Match: config.ProfileMatch{CN: "bot"}, Profile: "code-review", File: "cr.yaml"},
		},
	}
	problems := Validate(loaded, "")
	if config.HasFatal(problems) {
		t.Errorf("clean config should have no fatal errors: %v", problems)
	}
	if len(problems) != 0 {
		t.Errorf("clean config should have no problems, got %v", problems)
	}
}

func TestValidateDuplicateRuleKey(t *testing.T) {
	loaded := &config.ProfileLoadResult{
		Definitions: map[string]config.ProfileDefinition{
			"p": {Allow: map[string][]string{"x": {".*"}}},
		},
		DefSource: map[string]string{"p": "a.yaml"},
		Rules: []config.ProfileRule{
			{Match: config.ProfileMatch{CN: "dup"}, Profile: "p", File: "a.yaml"},
			{Match: config.ProfileMatch{CN: "dup"}, Profile: "p", File: "b.yaml"},
		},
	}
	problems := Validate(loaded, "")
	e, ok := findProblem(problems, "duplicate rule")
	if !ok || !e.Fatal {
		t.Fatalf("expected fatal duplicate rule error, got %v", problems)
	}
	if !strings.Contains(e.Message, "a.yaml") {
		t.Errorf("duplicate error should name first file, got %q", e.Message)
	}
}

func TestValidateUndefinedRef(t *testing.T) {
	loaded := &config.ProfileLoadResult{
		Definitions: map[string]config.ProfileDefinition{},
		DefSource:   map[string]string{},
		Rules: []config.ProfileRule{
			{Match: config.ProfileMatch{CN: "x"}, Profile: "ghost", File: "f.yaml"},
		},
	}
	problems := Validate(loaded, "")
	if e, ok := findProblem(problems, "undefined profile"); !ok || !e.Fatal {
		t.Fatalf("expected fatal undefined profile error, got %v", problems)
	}
}

func TestValidateInvalidRegexp(t *testing.T) {
	loaded := &config.ProfileLoadResult{
		Definitions: map[string]config.ProfileDefinition{
			"p": {Allow: map[string][]string{"x": {"gitlab_["}}},
		},
		DefSource: map[string]string{"p": "f.yaml"},
	}
	problems := Validate(loaded, "")
	if e, ok := findProblem(problems, "not a valid regexp"); !ok || !e.Fatal {
		t.Fatalf("expected fatal invalid regexp error, got %v", problems)
	}
}

func TestValidateSingleFieldMatch(t *testing.T) {
	loaded := &config.ProfileLoadResult{
		Definitions: map[string]config.ProfileDefinition{
			"p": {Allow: map[string][]string{"x": {".*"}}},
		},
		DefSource: map[string]string{"p": "f.yaml"},
		Rules: []config.ProfileRule{
			{Match: config.ProfileMatch{CN: "a", SANURI: "b"}, Profile: "p", File: "f.yaml"},
		},
	}
	problems := Validate(loaded, "")
	if e, ok := findProblem(problems, "exactly one"); !ok || !e.Fatal {
		t.Fatalf("expected fatal single-field error, got %v", problems)
	}
}

func TestValidateUndefinedDefault(t *testing.T) {
	loaded := &config.ProfileLoadResult{
		Definitions: map[string]config.ProfileDefinition{
			"p": {Allow: map[string][]string{"x": {".*"}}},
		},
		DefSource: map[string]string{"p": "f.yaml"},
	}
	problems := Validate(loaded, "ghost")
	if e, ok := findProblem(problems, "default profile"); !ok || !e.Fatal {
		t.Fatalf("expected fatal undefined default error, got %v", problems)
	}
}

func TestValidateWarnUnreferenced(t *testing.T) {
	loaded := &config.ProfileLoadResult{
		Definitions: map[string]config.ProfileDefinition{
			"used":   {Allow: map[string][]string{"x": {".*"}}},
			"unused": {Allow: map[string][]string{"y": {".*"}}},
		},
		DefSource: map[string]string{"used": "f.yaml", "unused": "f.yaml"},
		Rules: []config.ProfileRule{
			{Match: config.ProfileMatch{CN: "a"}, Profile: "used", File: "f.yaml"},
		},
	}
	problems := Validate(loaded, "")
	e, ok := findProblem(problems, "no rule references it")
	if !ok {
		t.Fatalf("expected unreferenced-profile warning, got %v", problems)
	}
	if e.Fatal {
		t.Errorf("unreferenced profile should be a warning, not fatal")
	}
	if config.HasFatal(problems) {
		t.Errorf("should have no fatal errors, got %v", problems)
	}
}

func TestValidateWarnWildcard(t *testing.T) {
	loaded := &config.ProfileLoadResult{
		Definitions: map[string]config.ProfileDefinition{
			"full": {Allow: map[string][]string{"*": {".*"}}},
		},
		DefSource: map[string]string{"full": "f.yaml"},
		Rules: []config.ProfileRule{
			{Match: config.ProfileMatch{CN: "a"}, Profile: "full", File: "f.yaml"},
		},
	}
	problems := Validate(loaded, "")
	if e, ok := findProblem(problems, "unrestricted access"); !ok || e.Fatal {
		t.Fatalf("expected wildcard warning (non-fatal), got %v", problems)
	}
}

// fakeLister satisfies PluginLister for ValidatePluginRefs tests.
type fakeLister struct {
	names []string
}

func (f fakeLister) Manifests() map[string]*plugin.Manifest {
	m := make(map[string]*plugin.Manifest, len(f.names))
	for _, n := range f.names {
		m[n] = &plugin.Manifest{Name: n}
	}
	return m
}

func TestValidatePluginRefs(t *testing.T) {
	loaded := &config.ProfileLoadResult{
		Definitions: map[string]config.ProfileDefinition{
			"p": {Allow: map[string][]string{
				"gitlab": {".*"}, // known
				"gitlba": {".*"}, // typo -> unknown
				"*":      {".*"}, // wildcard, not checked
			}},
		},
		DefSource: map[string]string{"p": "f.yaml"},
	}
	problems := ValidatePluginRefs(loaded, fakeLister{names: []string{"gitlab", "jira"}})
	e, ok := findProblem(problems, "unknown plugin \"gitlba\"")
	if !ok {
		t.Fatalf("expected unknown plugin warning, got %v", problems)
	}
	if e.Fatal {
		t.Errorf("unknown plugin should be a warning")
	}
	if !strings.Contains(e.Message, "did you mean \"gitlab\"") {
		t.Errorf("expected did-you-mean suggestion, got %q", e.Message)
	}
	// The wildcard "*" and known "gitlab" should not be flagged.
	if _, ok := findProblem(problems, "unknown plugin \"*\""); ok {
		t.Errorf("wildcard should not be flagged")
	}
	if _, ok := findProblem(problems, "unknown plugin \"gitlab\""); ok {
		t.Errorf("known plugin should not be flagged")
	}
}
