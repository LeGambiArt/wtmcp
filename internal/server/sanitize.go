package server

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// htmlCommentRE matches HTML comments including multi-line.
var htmlCommentRE = regexp.MustCompile(`<!--[\s\S]*?-->`)

// zeroWidthChars strips Unicode zero-width characters that can
// encode hidden instructions. U+200D (zero-width joiner) is excluded
// because it has legitimate uses in emoji sequences and Indic scripts.
var zeroWidthChars = strings.NewReplacer(
	"\u200b", "", // zero-width space
	"\u200c", "", // zero-width non-joiner
	"\u200e", "", // left-to-right mark
	"\u200f", "", // right-to-left mark
	"\u2060", "", // word joiner
	"\ufeff", "", // byte order mark
)

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
	text = zeroWidthChars.Replace(text)
	text = htmlCommentRE.ReplaceAllString(text, "")
	return text
}
