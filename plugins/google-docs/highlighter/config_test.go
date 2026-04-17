package highlighter

import (
	"testing"
)

func TestGetEmbeddedConfig(t *testing.T) {
	tests := []struct {
		name     string
		language string
		wantErr  bool
	}{
		{"python exists", "python", false},
		{"typescript exists", "typescript", false},
		{"go exists", "go", false},
		{"rust exists", "rust", false},
		{"bash exists", "bash", false},
		{"c exists", "c", false},
		{"cpp exists", "cpp", false},
		{"yaml exists", "yaml", false},
		{"toml exists", "toml", false},
		{"json exists", "json", false},
		{"unsupported language", "ruby", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := GetEmbeddedConfig(tt.language)
			if tt.wantErr {
				if err == nil {
					t.Errorf("GetEmbeddedConfig() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("GetEmbeddedConfig() unexpected error: %v", err)
				return
			}
			if len(data) == 0 {
				t.Errorf("GetEmbeddedConfig() returned empty data")
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name     string
		language string
		wantErr  bool
	}{
		{"python config loads", "python", false},
		{"go config loads", "go", false},
		{"unsupported falls back gracefully", "ruby", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := LoadConfig(tt.language)
			if tt.wantErr {
				if err == nil {
					t.Errorf("LoadConfig() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("LoadConfig() unexpected error: %v", err)
				return
			}
			if cfg.Language != tt.language {
				t.Errorf("LoadConfig() language = %v, want %v", cfg.Language, tt.language)
			}
			// Verify essential styles exist
			requiredStyles := []string{"keyword", "string", "comment", "default"}
			for _, style := range requiredStyles {
				if _, ok := cfg.Styles[style]; !ok {
					t.Errorf("LoadConfig() missing required style: %s", style)
				}
			}
		})
	}
}
