package profile

import (
	"fmt"
	"maps"
	"slices"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/plugin"
)

// Validate performs comprehensive, non-stopping validation of loaded
// profile configuration. It returns every problem found (fatal errors
// and warnings), including the load-time errors already recorded in
// loaded.Errors, so a caller can report them all in one pass.
func Validate(loaded *config.ProfileLoadResult, defaultProfile string) []config.ProfileLoadError {
	if loaded == nil {
		loaded = &config.ProfileLoadResult{}
	}
	var errs []config.ProfileLoadError

	// Start with load-time errors (parse errors, duplicate profile names).
	errs = append(errs, loaded.Errors...)

	// Compile-check tool regexps and flag wildcard-allow profiles.
	defNames := slices.Sorted(maps.Keys(loaded.Definitions))
	for _, name := range defNames {
		def := loaded.Definitions[name]
		src := loaded.DefSource[name]
		errs = append(errs, validateDefinition(name, src, def)...)
	}

	// Validate rules: single-field match, duplicate keys, undefined refs.
	seenKeys := make(map[ruleKey]ruleRef)
	referenced := make(map[string]bool)
	for i, rule := range loaded.Rules {
		key, err := ruleKeyOf(rule.Match)
		if err != nil {
			errs = append(errs, config.ProfileLoadError{
				File:    rule.File,
				Message: fmt.Sprintf("rule %d: %v", i+1, err),
				Fatal:   true,
			})
			continue
		}
		if prev, dup := seenKeys[key]; dup {
			errs = append(errs, config.ProfileLoadError{
				File: rule.File,
				Message: fmt.Sprintf("duplicate rule %v: already used in %s",
					key, prev.File),
				Fatal: true,
			})
			continue
		}
		seenKeys[key] = ruleRef{Profile: rule.Profile, File: rule.File}

		if _, ok := loaded.Definitions[rule.Profile]; !ok {
			errs = append(errs, config.ProfileLoadError{
				File:    rule.File,
				Message: fmt.Sprintf("rule %v references undefined profile %q", key, rule.Profile),
				Fatal:   true,
			})
			continue
		}
		referenced[rule.Profile] = true
	}

	// Default profile must be defined.
	if defaultProfile != "" {
		if _, ok := loaded.Definitions[defaultProfile]; !ok {
			errs = append(errs, config.ProfileLoadError{
				Message: fmt.Sprintf("default profile %q is not defined", defaultProfile),
				Fatal:   true,
			})
		} else {
			referenced[defaultProfile] = true
		}
	}

	// Warning: profile defined but never referenced by a rule or default.
	for _, name := range defNames {
		if !referenced[name] {
			errs = append(errs, config.ProfileLoadError{
				File:    loaded.DefSource[name],
				Message: fmt.Sprintf("profile %q is defined but no rule references it", name),
				Fatal:   false,
			})
		}
	}

	return errs
}

// ruleRef records where a rule key was first used.
type ruleRef struct {
	Profile string
	File    string
}

// forEachAllowDeny invokes fn for every plugin entry (sorted within each
// map) of the definition's allow then deny maps, labeling which map the
// entry came from. It centralizes the allow/deny + sorted-plugin walk
// shared by the validation passes.
func forEachAllowDeny(def config.ProfileDefinition, fn func(kind, plugin string, patterns []string)) {
	sections := []struct {
		kind string
		m    map[string][]string
	}{
		{"allow", def.Allow},
		{"deny", def.Deny},
	}
	for _, s := range sections {
		for _, p := range slices.Sorted(maps.Keys(s.m)) {
			fn(s.kind, p, s.m[p])
		}
	}
}

// validateDefinition compiles a definition's tool regexps (fatal on
// invalid) and flags wildcard-allow (warning).
func validateDefinition(name, src string, def config.ProfileDefinition) []config.ProfileLoadError {
	var errs []config.ProfileLoadError

	forEachAllowDeny(def, func(kind, p string, patterns []string) {
		for _, pat := range patterns {
			if _, err := compileAnchored(pat); err != nil {
				errs = append(errs, config.ProfileLoadError{
					File: src,
					Message: fmt.Sprintf("profile %q: %s pattern %q on plugin %q is not a valid regexp: %v",
						name, kind, pat, p, err),
					Fatal: true,
				})
			}
		}
	})

	// Warning: wildcard allow grants unrestricted access.
	for _, pat := range def.Allow["*"] {
		if pat == ".*" {
			errs = append(errs, config.ProfileLoadError{
				File: src,
				Message: fmt.Sprintf(
					"profile %q: allow pattern %q on plugin %q grants unrestricted access",
					name, ".*", "*"),
				Fatal: false,
			})
		}
	}

	return errs
}

// PluginLister enumerates discovered plugins for reference validation.
// *plugin.Manager satisfies this interface.
type PluginLister interface {
	Manifests() map[string]*plugin.Manifest
}

// ValidatePluginRefs checks that every plugin key in every profile's
// allow/deny map ("*" excepted) matches a discovered plugin name. Any
// mismatch is reported as a warning (server still starts). Requires
// plugin discovery, so it is only run when --with-plugins is passed.
func ValidatePluginRefs(loaded *config.ProfileLoadResult, mgr PluginLister) []config.ProfileLoadError {
	if loaded == nil || mgr == nil {
		return nil
	}
	known := make(map[string]bool)
	for name := range mgr.Manifests() {
		known[name] = true
	}

	var errs []config.ProfileLoadError
	for _, name := range slices.Sorted(maps.Keys(loaded.Definitions)) {
		def := loaded.Definitions[name]
		src := loaded.DefSource[name]
		forEachAllowDeny(def, func(kind, p string, _ []string) {
			if p == "*" || known[p] {
				return
			}
			msg := fmt.Sprintf("profile %q: %s references unknown plugin %q", name, kind, p)
			if suggestion := closestPlugin(p, known); suggestion != "" {
				msg += fmt.Sprintf(" (did you mean %q?)", suggestion)
			}
			errs = append(errs, config.ProfileLoadError{File: src, Message: msg, Fatal: false})
		})
	}
	return errs
}

// closestPlugin returns the known plugin name within edit distance 2 of
// target, or "" if none is close enough. Used for "did you mean" hints.
func closestPlugin(target string, known map[string]bool) string {
	best := ""
	bestDist := 3 // only suggest within distance <= 2
	for _, name := range slices.Sorted(maps.Keys(known)) {
		d := levenshtein(target, name)
		if d < bestDist {
			bestDist = d
			best = name
		}
	}
	return best
}

// levenshtein computes the edit distance between two strings.
func levenshtein(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
