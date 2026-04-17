// Package highlighter provides syntax highlighting for code blocks in Google Docs.
package highlighter

import (
	"fmt"
	"strconv"

	"google.golang.org/api/docs/v1"
)

// ParseColor converts a hex color string to Google Docs RGB format.
// Hex format: #RRGGBB (e.g., "#D73A49")
// Returns RGB with values 0.0-1.0 for use with Google Docs API.
func ParseColor(hex string) (*docs.RgbColor, error) {
	if len(hex) != 7 || hex[0] != '#' {
		return nil, fmt.Errorf("invalid hex color format: %s (expected #RRGGBB)", hex)
	}

	r, err := strconv.ParseUint(hex[1:3], 16, 8)
	if err != nil {
		return nil, fmt.Errorf("invalid red component in %s: %w", hex, err)
	}

	g, err := strconv.ParseUint(hex[3:5], 16, 8)
	if err != nil {
		return nil, fmt.Errorf("invalid green component in %s: %w", hex, err)
	}

	b, err := strconv.ParseUint(hex[5:7], 16, 8)
	if err != nil {
		return nil, fmt.Errorf("invalid blue component in %s: %w", hex, err)
	}

	return &docs.RgbColor{
		Red:   float64(r) / 255.0,
		Green: float64(g) / 255.0,
		Blue:  float64(b) / 255.0,
	}, nil
}
