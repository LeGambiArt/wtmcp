package highlighter

import (
	"fmt"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"google.golang.org/api/docs/v1"
)

// FormattedSegment represents a piece of code with applied styling.
type FormattedSegment struct {
	Text   string
	Color  *docs.RgbColor
	Bold   bool
	Italic bool
}

// HighlightCode applies syntax highlighting to code text using the specified language config.
func HighlightCode(code, language string, config *Config) ([]FormattedSegment, error) {
	// Get chroma lexer for the language
	lexer := lexers.Get(language)
	if lexer == nil {
		return nil, fmt.Errorf("unsupported language: %s", language)
	}

	// Tokenize the code
	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return nil, fmt.Errorf("tokenization failed: %w", err)
	}

	// Convert tokens to formatted segments
	segments := []FormattedSegment{}
	for _, token := range iterator.Tokens() {
		// Map token type to config style
		style := mapTokenToStyle(token.Type, config)

		// Convert hex color to RGB
		rgb, err := ParseColor(style.Color)
		if err != nil {
			// Fallback to default color on parse error
			rgb = &docs.RgbColor{
				Red:   0.141176, // #24292E
				Green: 0.160784,
				Blue:  0.180392,
			}
		}

		segments = append(segments, FormattedSegment{
			Text:   token.Value,
			Color:  rgb,
			Bold:   style.Bold,
			Italic: style.Italic,
		})
	}

	return segments, nil
}

// mapTokenToStyle maps a chroma token type to a config style.
func mapTokenToStyle(tokenType chroma.TokenType, config *Config) Style {
	// Check for specific token types and map to config styles
	// Use IsSubType to check token hierarchy
	switch {
	case isTokenType(tokenType, chroma.Keyword):
		if style, ok := config.Styles["keyword"]; ok {
			return style
		}
	case isTokenType(tokenType, chroma.String):
		if style, ok := config.Styles["string"]; ok {
			return style
		}
	case isTokenType(tokenType, chroma.Comment):
		if style, ok := config.Styles["comment"]; ok {
			return style
		}
	case isTokenType(tokenType, chroma.Number):
		if style, ok := config.Styles["number"]; ok {
			return style
		}
	case isTokenType(tokenType, chroma.Operator):
		if style, ok := config.Styles["operator"]; ok {
			return style
		}
	case isTokenType(tokenType, chroma.NameFunction):
		if style, ok := config.Styles["function"]; ok {
			return style
		}
	case isTokenType(tokenType, chroma.NameClass):
		if style, ok := config.Styles["type"]; ok {
			return style
		}
	case isTokenType(tokenType, chroma.NameConstant):
		if style, ok := config.Styles["constant"]; ok {
			return style
		}
	case isTokenType(tokenType, chroma.Name):
		if style, ok := config.Styles["variable"]; ok {
			return style
		}
	}

	// Default style for unrecognized tokens
	if style, ok := config.Styles["default"]; ok {
		return style
	}

	// Hardcoded fallback
	return Style{Color: "#24292E", Bold: false, Italic: false}
}

// isTokenType checks if a token type matches or is a subtype of the target type.
func isTokenType(tokenType, target chroma.TokenType) bool {
	for tokenType != chroma.None && tokenType != 0 {
		if tokenType == target {
			return true
		}
		tokenType = tokenType.Parent()
	}
	return false
}
