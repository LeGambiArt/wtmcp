package highlighter

import (
	"embed"
	"fmt"
)

//go:embed defaults/*.toml
var defaultConfigs embed.FS

// GetEmbeddedConfig retrieves an embedded default config by language name.
func GetEmbeddedConfig(language string) ([]byte, error) {
	path := fmt.Sprintf("defaults/%s.toml", language)
	data, err := defaultConfigs.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("embedded config not found for language %s: %w", language, err)
	}
	return data, nil
}
