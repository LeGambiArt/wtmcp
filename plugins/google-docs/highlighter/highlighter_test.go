package highlighter

import (
	"testing"
)

func TestHighlightCode(t *testing.T) {
	// Load Python config for testing
	cfg, err := LoadConfig("python")
	if err != nil {
		t.Fatalf("LoadConfig() failed: %v", err)
	}

	code := `def hello(name):
    """Say hello."""
    print(f"Hello, {name}!")
    return True
`

	segments, err := HighlightCode(code, "python", cfg)
	if err != nil {
		t.Fatalf("HighlightCode() failed: %v", err)
	}

	if len(segments) == 0 {
		t.Errorf("HighlightCode() returned no segments")
	}

	// Verify some segments have color applied
	hasColor := false
	for _, seg := range segments {
		if seg.Color.Red > 0 || seg.Color.Green > 0 || seg.Color.Blue > 0 {
			hasColor = true
			break
		}
	}
	if !hasColor {
		t.Errorf("HighlightCode() no segments have color applied")
	}

	// Verify text reconstruction
	var reconstructed string
	for _, seg := range segments {
		reconstructed += seg.Text
	}
	if reconstructed != code {
		t.Errorf("HighlightCode() text mismatch\ngot:  %q\nwant: %q", reconstructed, code)
	}
}

func TestHighlightCodeUnsupportedLanguage(t *testing.T) {
	cfg := &Config{
		Language: "unknown",
		Styles: map[string]Style{
			"default": {Color: "#24292E", Bold: false, Italic: false},
		},
	}

	_, err := HighlightCode("some code", "unknownlang", cfg)
	if err == nil {
		t.Errorf("HighlightCode() expected error for unsupported language, got nil")
	}
}
