package profile

import (
	"fmt"
	"log"
	"maps"
	"slices"

	"github.com/LeGambiArt/wtmcp/internal/config"
)

// Identity match field names used in rule keys and diagnostics. Aliased to
// the config definitions so there is a single source of truth.
const (
	FieldCN       = config.FieldCN
	FieldSANURI   = config.FieldSANURI
	FieldSANDNS   = config.FieldSANDNS
	FieldSANEmail = config.FieldSANEmail
)

// ruleKey is a single exact-match criterion: one identity field and its
// exact value. Rules become a map keyed by this, so matching is an O(1)
// set lookup that does NOT depend on file/rule order.
type ruleKey struct {
	field string // FieldCN | FieldSANURI | FieldSANDNS | FieldSANEmail
	value string
}

func (k ruleKey) String() string {
	return fmt.Sprintf("%s=%q", k.field, k.value)
}

// ruleKeyOf returns the single (field, value) key for a match. It errors
// unless exactly one field is set.
func ruleKeyOf(m config.ProfileMatch) (ruleKey, error) {
	fields := m.Fields()
	switch len(fields) {
	case 0:
		return ruleKey{}, fmt.Errorf("rule match has no field set (need exactly one of cn, san_uri, san_dns, san_email)")
	case 1:
		return ruleKey{fields[0].Field, fields[0].Value}, nil
	default:
		return ruleKey{}, fmt.Errorf("rule match has %d fields set, must have exactly one", len(fields))
	}
}

// ruleProblem categorizes why a rule is invalid (ruleOK when it is fine).
type ruleProblem int

const (
	ruleOK               ruleProblem = iota
	ruleMalformed                    // ruleKeyOf failed (0 or >1 match fields)
	ruleDuplicateKey                 // (field,value) already used by an earlier rule
	ruleUndefinedProfile             // Profile is not a defined profile
)

// ruleRef records where a rule key was first used.
type ruleRef struct {
	Profile string
	File    string
}

// ruleOutcome is the classification of one rule from loaded.Rules.
type ruleOutcome struct {
	rule     config.ProfileRule
	key      ruleKey // valid unless problem == ruleMalformed
	problem  ruleProblem
	keyErr   error   // set when problem == ruleMalformed
	firstUse ruleRef // set when problem == ruleDuplicateKey (the earlier rule)
}

// classifyRules validates each rule in order and returns one outcome per
// rule. It is the single source of rule validity shared by NewResolver
// (which stops at the first problem) and Validate (which reports them all),
// so the two cannot drift. Detection order per rule: malformed match, then
// duplicate key, then undefined profile; the first rule to use a key owns
// it, so a later collision is the duplicate. A rule's key is recorded even
// when its profile is undefined, so a later exact repeat is still a
// duplicate. defined reports whether a profile name is defined.
func classifyRules(rules []config.ProfileRule, defined func(name string) bool) []ruleOutcome {
	outcomes := make([]ruleOutcome, 0, len(rules))
	seen := make(map[ruleKey]ruleRef)
	for _, rule := range rules {
		out := ruleOutcome{rule: rule}
		key, err := ruleKeyOf(rule.Match)
		switch {
		case err != nil:
			out.problem = ruleMalformed
			out.keyErr = err
		default:
			out.key = key
			if prev, dup := seen[key]; dup {
				out.problem = ruleDuplicateKey
				out.firstUse = prev
			} else {
				seen[key] = ruleRef{Profile: rule.Profile, File: rule.File}
				if !defined(rule.Profile) {
					out.problem = ruleUndefinedProfile
				}
			}
		}
		outcomes = append(outcomes, out)
	}
	return outcomes
}

// Resolver maps a verified client identity to a tool Filter, built from
// the merged ProfileLoadResult.
type Resolver struct {
	configured     bool               // any profile definition or rule loaded?
	defaultProfile string             // "" => fail closed on no match
	rules          map[ruleKey]string // exact (field,value) -> profile
	ruleFiles      map[ruleKey]string // exact (field,value) -> source filename
	filters        map[string]*Filter // profile name -> compiled filter
	denyAll        *Filter            // allows only exempt introspection tools
}

// Resolution is the detailed outcome of resolving an identity. It is
// returned by Resolve for diagnostics (wtmcpctl profile test); FilterFor
// returns only the Filter.
type Resolution struct {
	Filter       *Filter  // the effective filter (never nil once configured)
	Profile      string   // resolved profile name ("" for deny-all)
	MatchedField string   // identity field that matched a rule ("" if none)
	MatchedValue string   // the value that matched ("" if none)
	MatchedFile  string   // source file of the matched rule ("" if none)
	Matched      []string // all distinct profiles the identity matched
	UsedDefault  bool     // true if the default profile was applied
	Ambiguous    bool     // true if >1 profile matched (deny-all)
	Configured   bool     // false when no profiles are configured
}

// NewResolver compiles filters and builds the rule lookup. It is strict:
// every one of these is a hard error — a rule with != exactly one match
// field, a duplicate (field,value) rule key, a rule referencing an
// undefined profile, or a default naming an undefined profile.
func NewResolver(defaultProfile string, loaded *config.ProfileLoadResult) (*Resolver, error) {
	if loaded == nil {
		loaded = &config.ProfileLoadResult{}
	}
	r := &Resolver{
		configured:     len(loaded.Definitions) > 0 || len(loaded.Rules) > 0,
		defaultProfile: defaultProfile,
		rules:          make(map[ruleKey]string),
		ruleFiles:      make(map[ruleKey]string),
		filters:        make(map[string]*Filter, len(loaded.Definitions)),
		denyAll:        newDenyAllFilter(),
	}

	for name, def := range loaded.Definitions {
		f, err := compileFilter(name, def)
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", name, err)
		}
		r.filters[name] = f
	}

	defined := func(name string) bool { _, ok := r.filters[name]; return ok }
	for _, out := range classifyRules(loaded.Rules, defined) {
		switch out.problem {
		case ruleOK:
			r.rules[out.key] = out.rule.Profile
			r.ruleFiles[out.key] = out.rule.File
		case ruleMalformed:
			return nil, out.keyErr
		case ruleDuplicateKey:
			return nil, fmt.Errorf("duplicate rule %v -> %q and %q", out.key, out.firstUse.Profile, out.rule.Profile)
		case ruleUndefinedProfile:
			return nil, fmt.Errorf("rule %v: undefined profile %q", out.key, out.rule.Profile)
		}
	}

	if defaultProfile != "" {
		if _, ok := r.filters[defaultProfile]; !ok {
			return nil, fmt.Errorf("default profile %q is not defined", defaultProfile)
		}
	}
	return r, nil
}

// Configured reports whether any profile definition or rule was loaded.
// When false, FilterFor returns nil and filtering is inert.
func (r *Resolver) Configured() bool {
	return r != nil && r.configured
}

// FilterFor is the single decision point for a connection. It implements
// the decision table from the design ("Default Behavior and Fail-Closed"):
//   - no profiles configured           -> nil (no filtering)
//   - exactly one rule matches         -> that profile's filter
//   - no rule matches, default set     -> default profile's filter
//   - no rule matches, no default      -> deny-all (fail closed)
//   - identity matches >1 profile      -> deny-all (fail closed) + log
func (r *Resolver) FilterFor(id Identity) *Filter {
	return r.Resolve(id).Filter
}

// Resolve returns the full decision for an identity, including which
// rule matched. It is used by FilterFor and by diagnostics tooling.
func (r *Resolver) Resolve(id Identity) Resolution {
	if r == nil || !r.configured {
		return Resolution{Configured: false}
	}

	// Collect matched profiles, remembering the first matching rule for
	// diagnostics (deterministic: cn, then san_uri, san_dns, san_email).
	matched := map[string]struct{}{}
	var firstKey ruleKey
	haveFirst := false
	lookup := func(field string, values ...string) {
		for _, v := range values {
			if v == "" {
				continue
			}
			key := ruleKey{field, v}
			if p, ok := r.rules[key]; ok {
				matched[p] = struct{}{}
				if !haveFirst {
					firstKey = key
					haveFirst = true
				}
			}
		}
	}
	lookup(FieldCN, id.CN)
	lookup(FieldSANURI, id.SANURIs...)
	lookup(FieldSANDNS, id.SANDNSs...)
	lookup(FieldSANEmail, id.SANEmails...)

	res := Resolution{Configured: true, Matched: profileNames(matched)}

	switch len(matched) {
	case 0:
		if r.defaultProfile == "" {
			res.Filter = r.denyAll // fail closed: unmatched agent gets nothing
			return res
		}
		res.Filter = r.filters[r.defaultProfile]
		res.Profile = r.defaultProfile
		res.UsedDefault = true
		return res
	case 1:
		res.Filter = r.filters[res.Matched[0]]
		res.Profile = res.Matched[0]
		res.MatchedField = firstKey.field
		res.MatchedValue = firstKey.value
		res.MatchedFile = r.ruleFiles[firstKey]
		return res
	}
	// Ambiguous: one identity matched >1 distinct profile — fail closed.
	log.Printf("profile: identity (CN=%q) matched multiple profiles %v; denying all", id.CN, res.Matched)
	res.Filter = r.denyAll
	res.Ambiguous = true
	return res
}

// FilterByName returns the compiled filter for a named profile. Used by
// the stdio --profile flag to resolve a filter once at startup.
func (r *Resolver) FilterByName(name string) (*Filter, bool) {
	if r == nil {
		return nil, false
	}
	f, ok := r.filters[name]
	return f, ok
}

// DefaultProfile returns the configured default profile name ("" if none).
func (r *Resolver) DefaultProfile() string {
	if r == nil {
		return ""
	}
	return r.defaultProfile
}

// DenyAll returns the fail-closed filter (only exempt tools pass).
func (r *Resolver) DenyAll() *Filter {
	if r == nil {
		return newDenyAllFilter()
	}
	return r.denyAll
}

// profileNames returns a sorted slice of the matched profile names.
func profileNames(m map[string]struct{}) []string {
	return slices.Sorted(maps.Keys(m))
}
