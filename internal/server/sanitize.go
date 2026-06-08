package server

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// htmlCommentRE matches HTML comments including multi-line.
var htmlCommentRE = regexp.MustCompile(`<!--[\s\S]*?-->`)

// stripFormatChars removes all Unicode format characters (category
// Cf) that can encode hidden instructions: zero-width spaces, bidi
// overrides, tag characters, soft hyphens, etc. U+200D (zero-width
// joiner) is preserved for emoji sequences and Indic scripts.
func stripFormatChars(r rune) rune {
	if r == '\u200d' {
		return r
	}
	if unicode.Is(unicode.Cf, r) {
		return -1
	}
	return r
}

// sanitizeContent strips HTML comments and zero-width Unicode
// characters from tool output text.
func sanitizeContent(text string) string {
	// Strip invalid UTF-8 first — byte-level replacement of
	// zero-width chars can reassemble new codepoints from fragments
	// of malformed UTF-8 (found by fuzzing).
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "")
	}
	// Strip zero-width chars before HTML comments: a zero-width
	// char inside a delimiter (e.g. <​!--) prevents the regex
	// from matching, then stripping afterward reassembles a valid
	// comment in the output.
	text = strings.Map(stripFormatChars, text)
	text = htmlCommentRE.ReplaceAllString(text, "")
	return text
}
