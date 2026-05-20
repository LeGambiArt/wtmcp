package highlighter

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config represents a language highlighting configuration.
type Config struct {
	Language    string           `toml:"language"`
	Description string           `toml:"description"`
	Styles      map[string]Style `toml:"styles"`
}

// Style defines the visual appearance for a token type.
type Style struct {
	Color  string `toml:"color"`
	Bold   bool   `toml:"bold"`
	Italic bool   `toml:"italic"`
}

const maxLanguageNameLen = 64

// isValidLanguageName checks that a language name contains only safe characters.
// Allows alphanumeric, hyphen, underscore, plus, and hash (for languages like c++ and c#).
// Rejects dots and slashes to prevent path traversal.
func isValidLanguageName(name string) bool {
	if name == "" || len(name) > maxLanguageNameLen {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') && r != '-' && r != '_' && r != '+' && r != '#' {
			return false
		}
	}
	return true
}

// LoadConfig loads the highlighting configuration for a language.
// Checks user override first (~/.config/wtmcp/assets/google-docs/highlights/),
// then falls back to embedded default.
func LoadConfig(language string) (*Config, error) {
	if !isValidLanguageName(language) {
		return nil, fmt.Errorf("invalid language name: %q", language)
	}

	// Try user override first
	userConfigPath := getUserConfigPath(language)
	//nolint:gosec // language is validated by isValidLanguageName above (no dots, slashes, or traversal)
	if data, err := os.ReadFile(userConfigPath); err == nil {
		var cfg Config
		if err := toml.Unmarshal(data, &cfg); err != nil {
			// Invalid user config, log and fall through to embedded
			fmt.Fprintf(os.Stderr, "WARN: Invalid user config for %s: %v, using default\n", language, err)
		} else {
			return &cfg, nil
		}
	}

	// Fall back to embedded default
	data, err := GetEmbeddedConfig(language)
	if err != nil {
		return nil, fmt.Errorf("no config available for language %s: %w", language, err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse embedded config for %s: %w", language, err)
	}

	return &cfg, nil
}

// getUserConfigPath returns the path to user config file for a language.
func getUserConfigPath(language string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "wtmcp", "assets", "google-docs", "highlights", language+".toml")
}
