package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProfileFile represents a single profiles.d/*.yaml file. Each file is
// a standalone YAML document defining one or more profiles and the
// agent matching rules that select them.
type ProfileFile struct {
	Definitions map[string]ProfileDefinition `yaml:"definitions"`
	Rules       []ProfileRule                `yaml:"rules"`
}

// ProfileDefinition defines which tools are visible for a profile.
// Allow/Deny map a plugin name (or "*") to a list of anchored tool
// name regexps.
type ProfileDefinition struct {
	Description string              `yaml:"description"`
	Allow       map[string][]string `yaml:"allow"`
	Deny        map[string][]string `yaml:"deny"`
}

// ProfileRule maps an identity to a profile name.
type ProfileRule struct {
	Match   ProfileMatch `yaml:"match"`
	Profile string       `yaml:"profile"`
	// File records the source filename (not full path) for diagnostics.
	// Populated during loading, not from YAML.
	File string `yaml:"-"`
}

// ProfileMatch defines identity matching criteria. Exactly one field
// must be set (validated at load); the value is matched by exact string
// equality, not regexp.
type ProfileMatch struct {
	CN       string `yaml:"cn"`
	SANURI   string `yaml:"san_uri"`
	SANDNS   string `yaml:"san_dns"`
	SANEmail string `yaml:"san_email"`
}

// Identity match field names (mirror the ProfileMatch yaml tags). Used in
// rule keys and diagnostics.
const (
	FieldCN       = "cn"
	FieldSANURI   = "san_uri"
	FieldSANDNS   = "san_dns"
	FieldSANEmail = "san_email"
)

// MatchField is one populated identity criterion of a ProfileMatch.
type MatchField struct {
	Field string // FieldCN | FieldSANURI | FieldSANDNS | FieldSANEmail
	Value string
}

// Fields returns the identity criteria set on the match, in canonical
// order (cn, san_uri, san_dns, san_email). A well-formed rule sets exactly
// one; enforcing that is left to the caller.
func (m ProfileMatch) Fields() []MatchField {
	var fields []MatchField
	if m.CN != "" {
		fields = append(fields, MatchField{FieldCN, m.CN})
	}
	if m.SANURI != "" {
		fields = append(fields, MatchField{FieldSANURI, m.SANURI})
	}
	if m.SANDNS != "" {
		fields = append(fields, MatchField{FieldSANDNS, m.SANDNS})
	}
	if m.SANEmail != "" {
		fields = append(fields, MatchField{FieldSANEmail, m.SANEmail})
	}
	return fields
}

// ProfileLoadResult holds the merged result of loading profiles.d/.
type ProfileLoadResult struct {
	Definitions map[string]ProfileDefinition // merged from all files
	Rules       []ProfileRule                // collected from all files
	Errors      []ProfileLoadError           // per-file and cross-file errors
	// DefSource records which file first defined each profile name,
	// for diagnostics (profile list SOURCE column, duplicate messages).
	DefSource map[string]string
	// Files lists the profile filenames that were scanned (sorted),
	// regardless of whether they parsed successfully.
	Files []string
}

// ProfileLoadError describes a problem found during profile loading.
type ProfileLoadError struct {
	File    string // filename (not full path), empty for cross-file errors
	Message string
	Fatal   bool // true = cannot proceed; false = warning
}

// HasFatal reports whether any error in the slice is fatal.
func HasFatal(errs []ProfileLoadError) bool {
	for _, e := range errs {
		if e.Fatal {
			return true
		}
	}
	return false
}

// ResolveProfilesDir returns the profiles.d directory path from config
// or the workdir default.
func ResolveProfilesDir(cfg *Config, workdir string) string {
	if cfg != nil && cfg.Profiles.Dir != "" {
		return ResolveEnvVars(cfg.Profiles.Dir)
	}
	return filepath.Join(workdir, "profiles.d")
}

// LoadProfiles reads *.yaml (and *.yml) files from profilesDir, merges
// definitions and rules, and detects duplicate profile names. Files are
// processed in lexicographic order by filename so error messages are
// deterministic. Rule order is irrelevant to matching (see
// profile.NewResolver — rules become a (field,value)->profile map).
//
// A missing directory is not an error: it means no profiles are
// configured and filtering stays inert (backward compatible).
func LoadProfiles(profilesDir string) (*ProfileLoadResult, error) {
	result := &ProfileLoadResult{
		Definitions: make(map[string]ProfileDefinition),
		DefSource:   make(map[string]string),
	}

	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil // no profiles configured
		}
		return nil, fmt.Errorf("read profiles.d: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && (strings.HasSuffix(e.Name(), ".yaml") ||
			strings.HasSuffix(e.Name(), ".yml")) {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files) // deterministic order
	result.Files = files

	for _, name := range files {
		path := filepath.Join(profilesDir, name)
		pf, err := loadProfileFile(path)
		if err != nil {
			result.Errors = append(result.Errors, ProfileLoadError{
				File:    name,
				Message: fmt.Sprintf("parse error: %v", err),
				Fatal:   true,
			})
			continue
		}

		// Merge definitions, detecting duplicates.
		for defName, def := range pf.Definitions {
			if firstFile, exists := result.DefSource[defName]; exists {
				result.Errors = append(result.Errors, ProfileLoadError{
					File: name,
					Message: fmt.Sprintf(
						"duplicate profile %q: already defined in %s",
						defName, firstFile),
					Fatal: true,
				})
				continue
			}
			result.DefSource[defName] = name
			result.Definitions[defName] = def
		}

		// Collect rules; order is irrelevant (matching is a set lookup).
		// Tag each rule with its source file for diagnostics.
		for i := range pf.Rules {
			pf.Rules[i].File = name
			result.Rules = append(result.Rules, pf.Rules[i])
		}

		log.Printf("loaded profile file: %s (%d definitions, %d rules)",
			name, len(pf.Definitions), len(pf.Rules))
	}

	return result, nil
}

// maxProfileFileSize bounds a single profiles.d file (1 MB).
const maxProfileFileSize = 1 << 20

// loadProfileFile reads and parses a single profiles.d file. Symlinks
// are rejected (defense in depth — profile files are security policy).
func loadProfileFile(path string) (*ProfileFile, error) {
	if err := RejectSymlink(path); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxProfileFileSize {
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", info.Size(), maxProfileFileSize)
	}

	data, err := os.ReadFile(path) //nolint:gosec // path validated above
	if err != nil {
		return nil, err
	}

	var pf ProfileFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		return nil, err
	}
	return &pf, nil
}
