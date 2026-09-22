package profile

import (
	"testing"

	"github.com/LeGambiArt/wtmcp/internal/config"
)

// loadResult builds a ProfileLoadResult from definitions and rules for
// resolver tests. Rules are given as (field, value, profile) triples.
func loadResult(defs map[string]config.ProfileDefinition, rules []config.ProfileRule) *config.ProfileLoadResult {
	return &config.ProfileLoadResult{Definitions: defs, Rules: rules}
}

func TestNewResolverValid(t *testing.T) {
	loaded := loadResult(
		map[string]config.ProfileDefinition{
			"code-review": {Allow: map[string][]string{"gitlab": {"gitlab_get_.*"}}},
		},
		[]config.ProfileRule{
			{Match: config.ProfileMatch{CN: "bot"}, Profile: "code-review", File: "f.yaml"},
		},
	)
	r, err := NewResolver("", loaded)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	if !r.Configured() {
		t.Errorf("resolver should be configured")
	}
}

func TestNewResolverErrors(t *testing.T) {
	def := map[string]config.ProfileDefinition{
		"p": {Allow: map[string][]string{"x": {".*"}}},
	}
	tests := []struct {
		name           string
		defaultProfile string
		defs           map[string]config.ProfileDefinition
		rules          []config.ProfileRule
	}{
		{
			name: "no match field",
			defs: def,
			rules: []config.ProfileRule{
				{Match: config.ProfileMatch{}, Profile: "p"},
			},
		},
		{
			name: "multiple match fields",
			defs: def,
			rules: []config.ProfileRule{
				{Match: config.ProfileMatch{CN: "a", SANURI: "b"}, Profile: "p"},
			},
		},
		{
			name: "duplicate rule key",
			defs: def,
			rules: []config.ProfileRule{
				{Match: config.ProfileMatch{CN: "a"}, Profile: "p", File: "a.yaml"},
				{Match: config.ProfileMatch{CN: "a"}, Profile: "p", File: "b.yaml"},
			},
		},
		{
			name: "undefined profile ref",
			defs: def,
			rules: []config.ProfileRule{
				{Match: config.ProfileMatch{CN: "a"}, Profile: "nonexistent"},
			},
		},
		{
			name:           "undefined default",
			defaultProfile: "ghost",
			defs:           def,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewResolver(tt.defaultProfile, loadResult(tt.defs, tt.rules))
			if err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
		})
	}
}

func TestNewResolverInvalidRegexp(t *testing.T) {
	loaded := loadResult(map[string]config.ProfileDefinition{
		"p": {Allow: map[string][]string{"x": {"["}}},
	}, nil)
	if _, err := NewResolver("", loaded); err == nil {
		t.Fatalf("expected error for invalid tool regexp")
	}
}

func newTestResolver(t *testing.T) *Resolver {
	t.Helper()
	loaded := loadResult(
		map[string]config.ProfileDefinition{
			"code-review": {Allow: map[string][]string{"gitlab": {"gitlab_get_.*"}}},
			"ci":          {Allow: map[string][]string{"gitlab": {".*"}}},
			"read-only":   {Allow: map[string][]string{"gitlab": {"gitlab_list_.*"}}},
		},
		[]config.ProfileRule{
			{Match: config.ProfileMatch{CN: "review-bot"}, Profile: "code-review", File: "cr.yaml"},
			{Match: config.ProfileMatch{SANURI: "spiffe://ex/cr"}, Profile: "code-review", File: "cr.yaml"},
			{Match: config.ProfileMatch{SANEmail: "ci@ex.com"}, Profile: "ci", File: "ci.yaml"},
		},
	)
	r, err := NewResolver("read-only", loaded)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	return r
}

func TestFilterForExactMatch(t *testing.T) {
	r := newTestResolver(t)
	f := r.FilterFor(Identity{CN: "review-bot"})
	if f.Name() != "code-review" {
		t.Errorf("CN match: got %q, want code-review", f.Name())
	}

	f = r.FilterFor(Identity{SANEmails: []string{"ci@ex.com"}})
	if f.Name() != "ci" {
		t.Errorf("email match: got %q, want ci", f.Name())
	}

	f = r.FilterFor(Identity{SANURIs: []string{"spiffe://ex/cr"}})
	if f.Name() != "code-review" {
		t.Errorf("uri match: got %q, want code-review", f.Name())
	}
}

func TestFilterForOrderIndependence(t *testing.T) {
	// Same identity fields, different rule field types resolve to the
	// correct profile regardless of which is checked; two identities
	// pointing at the same profile are stable.
	r := newTestResolver(t)
	a := r.FilterFor(Identity{CN: "review-bot"})
	b := r.FilterFor(Identity{SANURIs: []string{"spiffe://ex/cr"}})
	if a.Name() != b.Name() {
		t.Errorf("both should resolve to code-review: %q vs %q", a.Name(), b.Name())
	}
}

func TestFilterForDefaultFallback(t *testing.T) {
	r := newTestResolver(t)
	f := r.FilterFor(Identity{CN: "unknown-agent"})
	if f.Name() != "read-only" {
		t.Errorf("no match should use default read-only, got %q", f.Name())
	}
}

func TestFilterForFailClosedNoDefault(t *testing.T) {
	loaded := loadResult(
		map[string]config.ProfileDefinition{
			"p": {Allow: map[string][]string{"gitlab": {".*"}}},
		},
		[]config.ProfileRule{
			{Match: config.ProfileMatch{CN: "known"}, Profile: "p"},
		},
	)
	r, err := NewResolver("", loaded) // no default
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	f := r.FilterFor(Identity{CN: "unknown"})
	if FilterName(f) != "deny-all" {
		t.Errorf("no match + no default should be deny-all, got %q", FilterName(f))
	}
	// deny-all still exempts introspection.
	if f.IsAllowed("gitlab", "gitlab_get_x") {
		t.Errorf("deny-all should deny normal tools")
	}
	if !f.IsAllowed("", "tool_search") {
		t.Errorf("deny-all should exempt tool_search")
	}
}

func TestFilterForAmbiguousFailsClosed(t *testing.T) {
	loaded := loadResult(
		map[string]config.ProfileDefinition{
			"a": {Allow: map[string][]string{"x": {".*"}}},
			"b": {Allow: map[string][]string{"y": {".*"}}},
		},
		[]config.ProfileRule{
			{Match: config.ProfileMatch{CN: "shared"}, Profile: "a", File: "a.yaml"},
			{Match: config.ProfileMatch{SANEmail: "shared@ex.com"}, Profile: "b", File: "b.yaml"},
		},
	)
	r, err := NewResolver("", loaded)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	// Identity matches both a (via CN) and b (via email) -> deny all.
	id := Identity{CN: "shared", SANEmails: []string{"shared@ex.com"}}
	f := r.FilterFor(id)
	if FilterName(f) != "deny-all" {
		t.Errorf("ambiguous match should be deny-all, got %q", FilterName(f))
	}
	res := r.Resolve(id)
	if !res.Ambiguous {
		t.Errorf("Resolve should report Ambiguous")
	}
	if len(res.Matched) != 2 {
		t.Errorf("Resolve.Matched = %v, want 2 entries", res.Matched)
	}
}

func TestFilterForNotConfigured(t *testing.T) {
	r, err := NewResolver("", loadResult(nil, nil))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	if r.Configured() {
		t.Errorf("empty result should not be configured")
	}
	if f := r.FilterFor(Identity{CN: "x"}); f != nil {
		t.Errorf("unconfigured resolver should return nil filter (no filtering)")
	}
}

func TestResolveDetails(t *testing.T) {
	r := newTestResolver(t)
	res := r.Resolve(Identity{CN: "review-bot"})
	if res.Profile != "code-review" || res.MatchedField != FieldCN ||
		res.MatchedValue != "review-bot" || res.MatchedFile != "cr.yaml" {
		t.Errorf("Resolve details wrong: %+v", res)
	}

	res = r.Resolve(Identity{CN: "nobody"})
	if !res.UsedDefault || res.Profile != "read-only" {
		t.Errorf("Resolve default: %+v", res)
	}
}

func TestFilterByName(t *testing.T) {
	r := newTestResolver(t)
	if f, ok := r.FilterByName("ci"); !ok || f.Name() != "ci" {
		t.Errorf("FilterByName(ci) = %v, %v", f, ok)
	}
	if _, ok := r.FilterByName("ghost"); ok {
		t.Errorf("FilterByName(ghost) should be false")
	}
}
