package profile

import (
	"fmt"
	"regexp"

	"github.com/LeGambiArt/wtmcp/internal/config"
)

// exemptTools are read-only introspection tools that are always visible
// and callable regardless of profile. They are needed for basic MCP
// discovery and cannot leak state-changing capability.
//
// NOTE: plugin_reload is a mutating control tool and is deliberately
// NOT exempt — it obeys the profile like any other tool.
var exemptTools = map[string]bool{
	"plugin_list": true,
	"tool_stats":  true,
	"tool_search": true,
}

// isExemptTool reports whether a tool is always allowed.
func isExemptTool(name string) bool {
	return exemptTools[name]
}

// pluginMatcher holds the compiled anchored tool regexps for one plugin
// key ("*" matches any plugin) within a profile's allow or deny map.
type pluginMatcher struct {
	plugin   string // plugin name, or "*" for any
	patterns []*regexp.Regexp
}

// matches reports whether this matcher applies to pluginName and one of
// its patterns matches toolName.
func (m pluginMatcher) matches(pluginName, toolName string) bool {
	if m.plugin != "*" && m.plugin != pluginName {
		return false
	}
	for _, re := range m.patterns {
		if re.MatchString(toolName) {
			return true
		}
	}
	return false
}

// Filter determines whether a tool is visible/callable in a profile.
// The zero-allow Filter produced by newDenyAllFilter permits only exempt
// introspection tools — this is the fail-closed filter.
type Filter struct {
	name  string          // profile name, for audit logging ("" for deny-all)
	allow []pluginMatcher // compiled from ProfileDefinition.Allow (anchored)
	deny  []pluginMatcher // compiled from ProfileDefinition.Deny (anchored)
}

// Name returns the profile name backing this filter ("" for deny-all).
func (f *Filter) Name() string {
	if f == nil {
		return ""
	}
	return f.name
}

// IsAllowed returns true if the tool passes the profile's allow rules
// and does not match any deny rule. Read-only introspection tools are
// always exempt (see exemptTools); deny takes precedence over allow.
func (f *Filter) IsAllowed(pluginName, toolName string) bool {
	if isExemptTool(toolName) {
		return true
	}
	if f == nil {
		return false
	}
	if !f.matchesAllow(pluginName, toolName) {
		return false
	}
	return !f.matchesDeny(pluginName, toolName)
}

func (f *Filter) matchesAllow(pluginName, toolName string) bool {
	for _, m := range f.allow {
		if m.matches(pluginName, toolName) {
			return true
		}
	}
	return false
}

func (f *Filter) matchesDeny(pluginName, toolName string) bool {
	for _, m := range f.deny {
		if m.matches(pluginName, toolName) {
			return true
		}
	}
	return false
}

// newDenyAllFilter returns the fail-closed filter: no allow rules, so
// only exempt introspection tools pass IsAllowed.
func newDenyAllFilter() *Filter {
	return &Filter{name: ""}
}

// compileFilter compiles a profile definition's allow/deny tool patterns
// into a Filter. Tool patterns are compiled as anchored regexps (^...$)
// so a short pattern cannot match a longer tool name.
func compileFilter(name string, def config.ProfileDefinition) (*Filter, error) {
	f := &Filter{name: name}
	allow, err := compileMatchers(def.Allow)
	if err != nil {
		return nil, err
	}
	deny, err := compileMatchers(def.Deny)
	if err != nil {
		return nil, err
	}
	f.allow = allow
	f.deny = deny
	return f, nil
}

// compileMatchers compiles a plugin->patterns map into anchored matchers.
func compileMatchers(m map[string][]string) ([]pluginMatcher, error) {
	if len(m) == 0 {
		return nil, nil
	}
	matchers := make([]pluginMatcher, 0, len(m))
	for plugin, patterns := range m {
		pm := pluginMatcher{plugin: plugin}
		for _, pat := range patterns {
			re, err := compileAnchored(pat)
			if err != nil {
				return nil, fmt.Errorf("plugin %q: invalid regexp %q: %w", plugin, pat, err)
			}
			pm.patterns = append(pm.patterns, re)
		}
		matchers = append(matchers, pm)
	}
	return matchers, nil
}

// compileAnchored compiles a tool pattern anchored at both ends.
func compileAnchored(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile("^(?:" + pattern + ")$")
}

// FilterName returns a human-readable label for a filter for logging:
// the profile name, "deny-all" for the fail-closed filter, or "none"
// when filtering is inert (nil filter).
func FilterName(f *Filter) string {
	switch {
	case f == nil:
		return "none"
	case f.name == "":
		return "deny-all"
	default:
		return f.name
	}
}
