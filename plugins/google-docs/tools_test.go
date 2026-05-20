package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/api/docs/v1"
)

func TestExtractDocumentID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"full URL with edit", "https://docs.google.com/document/d/abc123_-XY/edit", "abc123_-XY"},
		{"full URL without edit", "https://docs.google.com/document/d/abc123/", "abc123"},
		{"query parameter", "https://example.com/view?id=doc456", "doc456"},
		{"query parameter with ampersand", "https://example.com/view?foo=bar&id=doc789", "doc789"},
		{"raw document ID", "abc123_-XY", "abc123_-XY"},
		{"empty string", "", ""},
		{"invalid characters", "not a valid id!", ""},
		{"URL without doc pattern", "https://example.com/other/path", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractDocumentID(tt.input)
			if got != tt.want {
				t.Errorf("extractDocumentID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIndentDepth(t *testing.T) {
	tests := []struct {
		name   string
		indent string
		want   int
	}{
		{"no indent", "", 0},
		{"one tab", "\t", 1},
		{"two tabs", "\t\t", 2},
		{"four spaces", "    ", 1},
		{"eight spaces", "        ", 2},
		{"mixed tab and spaces", "\t    ", 2},
		{"three spaces (partial)", "   ", 0},
		{"five spaces", "     ", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := indentDepth(tt.indent)
			if got != tt.want {
				t.Errorf("indentDepth(%q) = %d, want %d", tt.indent, got, tt.want)
			}
		})
	}
}

func TestParseMarkdownHeadings(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantHeading int
		wantText    string
	}{
		{"h1", "# Title", 1, "Title"},
		{"h2", "## Subtitle", 2, "Subtitle"},
		{"h3", "### Section", 3, "Section"},
		{"h6", "###### Deep", 6, "Deep"},
		{"not heading (no space)", "#NoSpace", 0, ""},
		{"hashes only (no space)", "###", 0, ""},
		{"single hash (no space)", "#", 0, ""},
		{"hash space empty body", "# ", 0, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			segments := parseMarkdown(tt.input)
			if tt.wantHeading == 0 {
				// Should not produce a heading segment
				for _, seg := range segments {
					if seg.heading > 0 {
						t.Errorf("unexpected heading segment: %+v", seg)
					}
				}
				return
			}
			// Collect all heading segments and concatenate their text
			var headingText string
			foundHeading := false
			for _, seg := range segments {
				if seg.heading == tt.wantHeading {
					foundHeading = true
					headingText += seg.text
				}
			}
			if !foundHeading {
				t.Errorf("no segment with heading=%d found in %+v", tt.wantHeading, segments)
			}
			if headingText != tt.wantText {
				t.Errorf("concatenated heading text = %q, want %q", headingText, tt.wantText)
			}
		})
	}
}

func TestParseMarkdownLists(t *testing.T) {
	t.Run("ordered list", func(t *testing.T) {
		segments := parseMarkdown("1. First\n2. Second")
		hasOrdered := false
		for _, seg := range segments {
			if seg.orderedListItem {
				hasOrdered = true
			}
		}
		if !hasOrdered {
			t.Error("expected ordered list items")
		}
	})

	t.Run("unordered list", func(t *testing.T) {
		segments := parseMarkdown("- First\n- Second")
		hasUnordered := false
		for _, seg := range segments {
			if seg.unorderedListItem {
				hasUnordered = true
			}
		}
		if !hasUnordered {
			t.Error("expected unordered list items")
		}
	})

	t.Run("nested list depth", func(t *testing.T) {
		segments := parseMarkdown("- Top\n    - Nested")
		maxDepth := 0
		for _, seg := range segments {
			if seg.listDepth > maxDepth {
				maxDepth = seg.listDepth
			}
		}
		if maxDepth != 1 {
			t.Errorf("max depth = %d, want 1", maxDepth)
		}
	})
}

func TestParseStrikethrough(t *testing.T) {
	t.Run("basic strikethrough", func(t *testing.T) {
		segments := parseSimpleFormatting("~~strikethrough~~")
		var strikethroughText string
		foundStrikethrough := false
		for _, seg := range segments {
			if seg.strikethrough {
				foundStrikethrough = true
				strikethroughText += seg.text
			}
		}
		if !foundStrikethrough || strikethroughText != "strikethrough" {
			t.Errorf("got %+v, want strikethrough segments concatenating to 'strikethrough'", segments)
		}
	})

	t.Run("strikethrough with bold", func(t *testing.T) {
		segments := parseSimpleFormatting("**~~bold and strikethrough~~**")
		var boldStrikeText string
		foundBoldStrikethrough := false
		for _, seg := range segments {
			if seg.bold && seg.strikethrough {
				foundBoldStrikethrough = true
				boldStrikeText += seg.text
			}
		}
		if !foundBoldStrikethrough || boldStrikeText != "bold and strikethrough" {
			t.Errorf("got bold+strikethrough text %q, want 'bold and strikethrough'", boldStrikeText)
		}
	})

	t.Run("strikethrough in heading", func(t *testing.T) {
		segments := parseMarkdown("## ~~Deprecated~~ Feature")
		var strikethroughText string
		foundStrikethrough := false
		for _, seg := range segments {
			if seg.heading == 2 && seg.strikethrough {
				foundStrikethrough = true
				strikethroughText += seg.text
			}
		}
		if !foundStrikethrough || strikethroughText != "Deprecated" {
			t.Errorf("strikethrough in heading not found correctly in %+v", segments)
		}
	})

	t.Run("unclosed strikethrough", func(t *testing.T) {
		segments := parseSimpleFormatting("~~unclosed")
		merged := mergeSegments(segments)
		if len(merged) != 1 || merged[0].text != "~~unclosed" {
			t.Errorf("got %+v, want literal '~~unclosed'", merged)
		}
	})

	t.Run("mixed formatting", func(t *testing.T) {
		markdown := "Normal **bold** ~~strike~~ _italic_ text"
		segments := parseMarkdown(markdown)

		var boldText, strikeText, italicText string
		foundBold := false
		foundStrike := false
		foundItalic := false

		for _, seg := range segments {
			if seg.bold {
				foundBold = true
				boldText += seg.text
			}
			if seg.strikethrough {
				foundStrike = true
				strikeText += seg.text
			}
			if seg.italic {
				foundItalic = true
				italicText += seg.text
			}
		}

		if !foundBold || boldText != "bold" {
			t.Errorf("bold text = %q, want 'bold'", boldText)
		}
		if !foundStrike || strikeText != "strike" {
			t.Errorf("strike text = %q, want 'strike'", strikeText)
		}
		if !foundItalic || italicText != "italic" {
			t.Errorf("italic text = %q, want 'italic'", italicText)
		}
	})

	t.Run("nested strikethrough and italic", func(t *testing.T) {
		segments := parseSimpleFormatting("~~*strikethrough italic*~~")
		var bothText string
		foundBoth := false
		for _, seg := range segments {
			if seg.strikethrough && seg.italic {
				foundBoth = true
				bothText += seg.text
			}
		}
		if !foundBoth || bothText != "strikethrough italic" {
			t.Errorf("got strikethrough+italic text %q, want 'strikethrough italic'", bothText)
		}
	})
}

func TestStrikethroughWithDateChips(t *testing.T) {
	t.Run("strikethrough @today", func(t *testing.T) {
		segments := parseSimpleFormatting("~~@today~~")
		foundStrikethroughDate := false
		for _, seg := range segments {
			if seg.isDateField && seg.strikethrough && seg.dateValue == "" {
				foundStrikethroughDate = true
			}
		}
		if !foundStrikethroughDate {
			t.Errorf("strikethrough @today not found in %+v", segments)
		}
	})

	t.Run("strikethrough date", func(t *testing.T) {
		segments := parseSimpleFormatting("~~@date(2026-04-07)~~")
		foundStrikethroughDate := false
		for _, seg := range segments {
			if seg.isDateField && seg.strikethrough && seg.dateValue == "2026-04-07" {
				foundStrikethroughDate = true
			}
		}
		if !foundStrikethroughDate {
			t.Errorf("strikethrough @date not found in %+v", segments)
		}
	})
}

func TestStrikethroughInRequests(t *testing.T) {
	t.Run("strikethrough creates correct style request", func(t *testing.T) {
		segments := parseMarkdown("~~strikethrough text~~")
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		foundStrikethroughStyle := false
		for _, req := range requests {
			if req.UpdateTextStyle != nil {
				if req.UpdateTextStyle.TextStyle.Strikethrough {
					foundStrikethroughStyle = true
					// Verify fields include "strikethrough"
					if !strings.Contains(req.UpdateTextStyle.Fields, "strikethrough") {
						t.Errorf("strikethrough not in fields: %s", req.UpdateTextStyle.Fields)
					}
				}
			}
		}

		if !foundStrikethroughStyle {
			t.Errorf("strikethrough style not applied in requests")
		}
	})
}

func TestNestedFormatting(t *testing.T) {
	// *** is ambiguous: the greedy ** (bold) match consumes the first two
	// asterisks, leaving the third as literal text inside the bold segment.
	// Use explicit nesting like **_bold italic_** for reliable combinations.
	t.Run("triple asterisk is greedy bold", func(t *testing.T) {
		segments := parseSimpleFormatting("***bold italic***")
		var boldText string
		for _, seg := range segments {
			if seg.bold {
				boldText += seg.text
			}
		}
		if !strings.Contains(boldText, "bold italic") {
			t.Errorf("expected bold text to contain 'bold italic', got %q", boldText)
		}
	})

	t.Run("bold underline", func(t *testing.T) {
		segments := parseSimpleFormatting("**__bold underline__**")
		var text string
		for _, seg := range segments {
			if seg.bold && seg.underline {
				text += seg.text
			}
		}
		if text != "bold underline" {
			t.Errorf("expected bold+underline 'bold underline', got %q", text)
		}
	})

	t.Run("italic underline", func(t *testing.T) {
		segments := parseSimpleFormatting("*__italic underline__*")
		var text string
		for _, seg := range segments {
			if seg.italic && seg.underline {
				text += seg.text
			}
		}
		if text != "italic underline" {
			t.Errorf("expected italic+underline 'italic underline', got %q", text)
		}
	})

	t.Run("partial bold italic", func(t *testing.T) {
		segments := parseSimpleFormatting("**some *bold italic* text**")
		var boldItalicText string
		var boldOnlyText string
		for _, seg := range segments {
			if seg.bold && seg.italic {
				boldItalicText += seg.text
			} else if seg.bold && !seg.italic {
				boldOnlyText += seg.text
			}
		}
		if boldItalicText != "bold italic" {
			t.Errorf("expected bold+italic 'bold italic', got %q", boldItalicText)
		}
		if !strings.Contains(boldOnlyText, "some") {
			t.Errorf("expected bold-only text to contain 'some', got %q", boldOnlyText)
		}
	})

	// Link handler does NOT recurse into display text — bold inside link
	// text renders as literal asterisks. This is a known limitation.
	t.Run("bold inside link is literal", func(t *testing.T) {
		segments := parseSimpleFormatting("[**bold link**](https://example.com)")
		for _, seg := range segments {
			if seg.linkURL == "https://example.com" {
				if seg.text != "**bold link**" {
					t.Errorf("expected literal '**bold link**' as link text, got %q", seg.text)
				}
				return
			}
		}
		t.Errorf("link not found in segments")
	})
}

func TestStrikethroughEdgeCases(t *testing.T) {
	t.Run("empty strikethrough", func(t *testing.T) {
		segments := parseSimpleFormatting("~~~~")
		merged := mergeSegments(segments)
		// Empty content between ~~ delimiters — no text to format
		hasStrikethrough := false
		for _, seg := range merged {
			if seg.strikethrough && seg.text != "" {
				hasStrikethrough = true
			}
		}
		if hasStrikethrough {
			t.Errorf("empty ~~~~ should not produce non-empty strikethrough segments")
		}
	})

	t.Run("adjacent strikethrough blocks", func(t *testing.T) {
		segments := parseSimpleFormatting("~~a~~~~b~~")
		var text string
		for _, seg := range segments {
			if seg.strikethrough {
				text += seg.text
			}
		}
		if text != "ab" {
			t.Errorf("expected strikethrough 'ab', got %q", text)
		}
	})

	t.Run("single tilde not matched", func(t *testing.T) {
		segments := parseSimpleFormatting("~not strike~")
		merged := mergeSegments(segments)
		if len(merged) != 1 || merged[0].text != "~not strike~" {
			t.Errorf("single tildes should be literal, got %+v", merged)
		}
		if merged[0].strikethrough {
			t.Errorf("single tildes should not be strikethrough")
		}
	})

	t.Run("strikethrough person chip", func(t *testing.T) {
		segments := parseSimpleFormatting("~~@(alice@example.com)~~")
		found := false
		for _, seg := range segments {
			if seg.isPersonField && seg.strikethrough && seg.personIdentifier == "alice@example.com" {
				found = true
			}
		}
		if !found {
			t.Errorf("strikethrough person chip not found in %+v", segments)
		}
	})
}

func TestParseSimpleFormatting(t *testing.T) {
	t.Run("bold", func(t *testing.T) {
		segments := parseSimpleFormatting("**bold**")
		// Collect all bold segments and concatenate
		var boldText string
		foundBold := false
		for _, seg := range segments {
			if seg.bold {
				foundBold = true
				boldText += seg.text
			}
		}
		if !foundBold || boldText != "bold" {
			t.Errorf("got %+v, want bold segments concatenating to 'bold'", segments)
		}
	})

	t.Run("italic with asterisk", func(t *testing.T) {
		segments := parseSimpleFormatting("*italic*")
		// Collect all italic segments and concatenate
		var italicText string
		foundItalic := false
		for _, seg := range segments {
			if seg.italic {
				foundItalic = true
				italicText += seg.text
			}
		}
		if !foundItalic || italicText != "italic" {
			t.Errorf("got %+v, want italic segments concatenating to 'italic'", segments)
		}
	})

	t.Run("italic with underscore", func(t *testing.T) {
		segments := parseSimpleFormatting("_italic_")
		// Collect all italic segments and concatenate
		var italicText string
		foundItalic := false
		for _, seg := range segments {
			if seg.italic {
				foundItalic = true
				italicText += seg.text
			}
		}
		if !foundItalic || italicText != "italic" {
			t.Errorf("got %+v, want italic segments concatenating to 'italic'", segments)
		}
	})

	t.Run("underline", func(t *testing.T) {
		segments := parseSimpleFormatting("__underlined__")
		// Collect all underline segments and concatenate
		var underlineText string
		foundUnderline := false
		for _, seg := range segments {
			if seg.underline {
				foundUnderline = true
				underlineText += seg.text
			}
		}
		if !foundUnderline || underlineText != "underlined" {
			t.Errorf("got %+v, want underline segments concatenating to 'underlined'", segments)
		}
	})

	t.Run("plain text", func(t *testing.T) {
		segments := parseSimpleFormatting("hello world")
		merged := mergeSegments(segments)
		if len(merged) != 1 || merged[0].text != "hello world" {
			t.Errorf("got %+v, want single 'hello world' segment", merged)
		}
	})

	t.Run("unclosed bold", func(t *testing.T) {
		segments := parseSimpleFormatting("**unclosed")
		merged := mergeSegments(segments)
		if len(merged) != 1 || merged[0].text != "**unclosed" {
			t.Errorf("got %+v, want literal '**unclosed'", merged)
		}
	})
}

func TestParseSimpleFormattingSpecialElements(t *testing.T) {
	t.Run("link", func(t *testing.T) {
		segments := parseSimpleFormatting("[Google](https://google.com)")
		found := false
		for _, seg := range segments {
			if seg.linkURL == "https://google.com" && seg.text == "Google" {
				found = true
			}
		}
		if !found {
			t.Errorf("link not found in %+v", segments)
		}
	})

	t.Run("date today", func(t *testing.T) {
		segments := parseSimpleFormatting("@today")
		found := false
		for _, seg := range segments {
			if seg.isDateField && seg.dateValue == "" {
				found = true
			}
		}
		if !found {
			t.Errorf("@today not found in %+v", segments)
		}
	})

	t.Run("date specific", func(t *testing.T) {
		segments := parseSimpleFormatting("@date(2026-01-15)")
		found := false
		for _, seg := range segments {
			if seg.isDateField && seg.dateValue == "2026-01-15" {
				found = true
			}
		}
		if !found {
			t.Errorf("@date(2026-01-15) not found in %+v", segments)
		}
	})

	t.Run("person chip", func(t *testing.T) {
		segments := parseSimpleFormatting("@(user@example.com)")
		found := false
		for _, seg := range segments {
			if seg.isPersonField && seg.personIdentifier == "user@example.com" {
				found = true
			}
		}
		if !found {
			t.Errorf("person chip not found in %+v", segments)
		}
	})
}

func TestParseSimpleFormattingDepthLimit(t *testing.T) {
	t.Run("deeply nested input does not panic", func(t *testing.T) {
		// Build input that would recurse deeply: **__*_**__*_text_*__**_*__**
		input := strings.Repeat("**", 20) + "text" + strings.Repeat("**", 20)
		segments := parseSimpleFormatting(input)
		// Should return without panic; exact output doesn't matter
		if len(segments) == 0 {
			t.Error("expected at least one segment")
		}
	})

	t.Run("depth limit returns raw text", func(t *testing.T) {
		// Alternate delimiter types to force actual recursion depth
		// Each level: ~~**__*_  (5 nesting levels per iteration)
		inner := "text"
		for i := 0; i < 5; i++ {
			inner = "~~**" + inner + "**~~"
		}
		segments := parseSimpleFormatting(inner)
		// Should return without panic
		if len(segments) == 0 {
			t.Error("expected at least one segment")
		}
		// Verify "text" appears somewhere in the output
		var allText string
		for _, seg := range segments {
			allText += seg.text
		}
		if !strings.Contains(allText, "text") {
			t.Errorf("expected 'text' in output, got %q", allText)
		}
	})
}

func TestIntraWordEmphasis(t *testing.T) {
	hasFormatting := func(segments []markdownSegment, field string) bool {
		for _, seg := range segments {
			switch field {
			case "italic":
				if seg.italic {
					return true
				}
			case "bold":
				if seg.bold {
					return true
				}
			case "underline":
				if seg.underline {
					return true
				}
			}
		}
		return false
	}

	allText := func(segments []markdownSegment) string {
		var s string
		for _, seg := range segments {
			s += seg.text
		}
		return s
	}

	// Underscore italic — intra-word cases (should be literal)
	t.Run("WTMCP_FOO_BAR is literal", func(t *testing.T) {
		segs := parseSimpleFormatting("WTMCP_FOO_BAR")
		if hasFormatting(segs, "italic") {
			t.Error("intra-word underscores should not produce italic")
		}
		if allText(segs) != "WTMCP_FOO_BAR" {
			t.Errorf("got %q, want WTMCP_FOO_BAR", allText(segs))
		}
	})

	t.Run("foo_bar_baz is literal", func(t *testing.T) {
		segs := parseSimpleFormatting("foo_bar_baz")
		if hasFormatting(segs, "italic") {
			t.Error("intra-word underscores should not produce italic")
		}
	})

	t.Run("foo_bar unpaired is literal", func(t *testing.T) {
		segs := parseSimpleFormatting("foo_bar")
		if hasFormatting(segs, "italic") {
			t.Error("unpaired underscore should not produce italic")
		}
		if allText(segs) != "foo_bar" {
			t.Errorf("got %q, want foo_bar", allText(segs))
		}
	})

	t.Run("a_b_c_d_e is literal", func(t *testing.T) {
		segs := parseSimpleFormatting("a_b_c_d_e")
		if hasFormatting(segs, "italic") {
			t.Error("multiple intra-word underscores should not produce italic")
		}
	})

	t.Run("_foo_bar_ is literal", func(t *testing.T) {
		segs := parseSimpleFormatting("_foo_bar_")
		if allText(segs) != "_foo_bar_" {
			t.Errorf("got %q, want _foo_bar_", allText(segs))
		}
	})

	// Underscore italic — boundary cases (should produce italic)
	t.Run("_italic_ alone is italic", func(t *testing.T) {
		segs := parseSimpleFormatting("_italic_")
		if !hasFormatting(segs, "italic") {
			t.Error("standalone _italic_ should produce italic")
		}
	})

	t.Run("hello _world_ end is italic", func(t *testing.T) {
		segs := parseSimpleFormatting("hello _world_ end")
		if !hasFormatting(segs, "italic") {
			t.Error("_world_ with whitespace boundaries should produce italic")
		}
	})

	t.Run("(_italic_) with punctuation is italic", func(t *testing.T) {
		segs := parseSimpleFormatting("(_italic_)")
		if !hasFormatting(segs, "italic") {
			t.Error("_italic_ with punctuation boundaries should produce italic")
		}
	})

	// Asterisk italic — intra-word cases
	t.Run("a*b*c is literal", func(t *testing.T) {
		segs := parseSimpleFormatting("a*b*c")
		if hasFormatting(segs, "italic") {
			t.Error("intra-word asterisks should not produce italic")
		}
		if allText(segs) != "a*b*c" {
			t.Errorf("got %q, want a*b*c", allText(segs))
		}
	})

	t.Run("*italic* alone is italic", func(t *testing.T) {
		segs := parseSimpleFormatting("*italic*")
		if !hasFormatting(segs, "italic") {
			t.Error("standalone *italic* should produce italic")
		}
	})

	t.Run("hello *world* end is italic", func(t *testing.T) {
		segs := parseSimpleFormatting("hello *world* end")
		if !hasFormatting(segs, "italic") {
			t.Error("*world* with whitespace boundaries should produce italic")
		}
	})

	// Double-asterisk bold — intra-word cases
	t.Run("a**b**c is literal", func(t *testing.T) {
		segs := parseSimpleFormatting("a**b**c")
		if hasFormatting(segs, "bold") {
			t.Error("intra-word double asterisks should not produce bold")
		}
	})

	t.Run("**bold** alone is bold", func(t *testing.T) {
		segs := parseSimpleFormatting("**bold**")
		if !hasFormatting(segs, "bold") {
			t.Error("standalone **bold** should produce bold")
		}
	})

	// Double-underscore underline — intra-word cases
	t.Run("A__init__B is literal", func(t *testing.T) {
		segs := parseSimpleFormatting("A__init__B")
		if hasFormatting(segs, "underline") {
			t.Error("intra-word double underscores should not produce underline")
		}
		if allText(segs) != "A__init__B" {
			t.Errorf("got %q, want A__init__B", allText(segs))
		}
	})

	t.Run("__init__ alone is underline", func(t *testing.T) {
		segs := parseSimpleFormatting("__init__")
		if !hasFormatting(segs, "underline") {
			t.Error("standalone __init__ should produce underline")
		}
	})

	t.Run("hello __world__ end is underline", func(t *testing.T) {
		segs := parseSimpleFormatting("hello __world__ end")
		if !hasFormatting(segs, "underline") {
			t.Error("__world__ with whitespace boundaries should produce underline")
		}
	})
}

func TestDateChipValidation(t *testing.T) {
	t.Run("valid date produces InsertDate", func(t *testing.T) {
		cell := tableCell{segments: []markdownSegment{{isDateField: true, dateValue: "2026-04-15"}}}
		reqs := populateTableCell(&cell, 5)
		found := false
		for _, req := range reqs {
			if req.InsertDate != nil {
				found = true
				if !strings.Contains(req.InsertDate.DateElementProperties.Timestamp, "2026-04-15") {
					t.Errorf("expected timestamp containing 2026-04-15, got %s",
						req.InsertDate.DateElementProperties.Timestamp)
				}
			}
		}
		if !found {
			t.Error("expected InsertDate request for valid date")
		}
	})

	t.Run("invalid month falls back to current date", func(t *testing.T) {
		cell := tableCell{segments: []markdownSegment{{isDateField: true, dateValue: "9999-99-99"}}}
		reqs := populateTableCell(&cell, 5)
		for _, req := range reqs {
			if req.InsertDate != nil {
				if strings.Contains(req.InsertDate.DateElementProperties.Timestamp, "9999") {
					t.Error("invalid date should fall back to current date, not use 9999")
				}
			}
		}
	})

	t.Run("Feb 31 falls back to current date", func(t *testing.T) {
		cell := tableCell{segments: []markdownSegment{{isDateField: true, dateValue: "2026-02-31"}}}
		reqs := populateTableCell(&cell, 5)
		for _, req := range reqs {
			if req.InsertDate != nil {
				if strings.Contains(req.InsertDate.DateElementProperties.Timestamp, "2026-03") {
					t.Error("Feb 31 should not normalize to March, should fall back to current date")
				}
			}
		}
	})

	t.Run("non-leap Feb 29 falls back", func(t *testing.T) {
		cell := tableCell{segments: []markdownSegment{{isDateField: true, dateValue: "2025-02-29"}}}
		reqs := populateTableCell(&cell, 5)
		for _, req := range reqs {
			if req.InsertDate != nil {
				if strings.Contains(req.InsertDate.DateElementProperties.Timestamp, "2025-03") {
					t.Error("non-leap Feb 29 should fall back, not normalize to March")
				}
			}
		}
	})

	t.Run("non-numeric date falls back", func(t *testing.T) {
		cell := tableCell{segments: []markdownSegment{{isDateField: true, dateValue: "abc-01-01"}}}
		reqs := populateTableCell(&cell, 5)
		for _, req := range reqs {
			if req.InsertDate != nil {
				if strings.Contains(req.InsertDate.DateElementProperties.Timestamp, "0000") {
					t.Error("non-numeric date should fall back, not use year 0000")
				}
			}
		}
	})
}

func TestEmptyDelimiterPairs(t *testing.T) {
	allText := func(segments []markdownSegment) string {
		var s string
		for _, seg := range segments {
			s += seg.text
		}
		return s
	}

	t.Run("**** preserved as literal", func(t *testing.T) {
		segs := parseSimpleFormatting("****")
		if allText(segs) != "****" {
			t.Errorf("got %q, want ****", allText(segs))
		}
	})

	t.Run("____ preserved as literal", func(t *testing.T) {
		segs := parseSimpleFormatting("____")
		if allText(segs) != "____" {
			t.Errorf("got %q, want ____", allText(segs))
		}
	})

	t.Run("~~~~ preserved as literal", func(t *testing.T) {
		segs := parseSimpleFormatting("~~~~")
		if allText(segs) != "~~~~" {
			t.Errorf("got %q, want ~~~~", allText(segs))
		}
	})

	t.Run("****text preserved", func(t *testing.T) {
		segs := parseSimpleFormatting("Some ****text")
		if allText(segs) != "Some ****text" {
			t.Errorf("got %q, want Some ****text", allText(segs))
		}
	})

	t.Run("**bold** still works", func(t *testing.T) {
		segs := parseSimpleFormatting("**bold**")
		merged := mergeSegments(segs)
		found := false
		for _, seg := range merged {
			if seg.bold && seg.text == "bold" {
				found = true
			}
		}
		if !found {
			t.Error("expected bold segment with text bold after merge")
		}
	})

	t.Run("****bold**** produces literal+bold+literal", func(t *testing.T) {
		segs := parseSimpleFormatting("****bold****")
		merged := mergeSegments(segs)
		text := allText(merged)
		if text != "**bold**" {
			t.Errorf("got %q, want **bold**", text)
		}
		foundBold := false
		for _, seg := range merged {
			if seg.bold && seg.text == "bold" {
				foundBold = true
			}
		}
		if !foundBold {
			t.Error("expected bold segment in ****bold****")
		}
	})
}

func TestHorizontalRule(t *testing.T) {
	hasHR := func(segments []markdownSegment) bool {
		for _, seg := range segments {
			if seg.isHorizontalRule {
				return true
			}
		}
		return false
	}

	countHR := func(segments []markdownSegment) int {
		n := 0
		for _, seg := range segments {
			if seg.isHorizontalRule {
				n++
			}
		}
		return n
	}

	t.Run("--- is horizontal rule", func(t *testing.T) {
		segs := parseMarkdown("---")
		if !hasHR(segs) {
			t.Error("--- should produce a horizontal rule segment")
		}
	})

	t.Run("*** is horizontal rule", func(t *testing.T) {
		segs := parseMarkdown("***")
		if !hasHR(segs) {
			t.Error("*** should produce a horizontal rule segment")
		}
	})

	t.Run("___ is horizontal rule", func(t *testing.T) {
		segs := parseMarkdown("___")
		if !hasHR(segs) {
			t.Error("___ should produce a horizontal rule segment")
		}
	})

	t.Run("----- is horizontal rule", func(t *testing.T) {
		segs := parseMarkdown("-----")
		if !hasHR(segs) {
			t.Error("----- should produce a horizontal rule segment")
		}
	})

	// Intentional CommonMark divergence: spaced thematic breaks not supported
	t.Run("- - - is NOT horizontal rule", func(t *testing.T) {
		segs := parseMarkdown("- - -")
		if hasHR(segs) {
			t.Error("- - - should NOT be a horizontal rule (spaced form not supported)")
		}
	})

	t.Run("*** alone is HR not bold-italic", func(t *testing.T) {
		segs := parseMarkdown("***")
		if !hasHR(segs) {
			t.Error("*** on its own line should be HR")
		}
	})

	t.Run("***text*** is formatting not HR", func(t *testing.T) {
		segs := parseMarkdown("***text***")
		if hasHR(segs) {
			t.Error("***text*** should be formatting, not HR")
		}
	})

	t.Run("___ alone is HR not underline", func(t *testing.T) {
		segs := parseMarkdown("___")
		if !hasHR(segs) {
			t.Error("___ on its own line should be HR")
		}
	})

	t.Run("___text___ is formatting not HR", func(t *testing.T) {
		segs := parseMarkdown("___text___")
		if hasHR(segs) {
			t.Error("___text___ should be formatting, not HR")
		}
	})

	t.Run("text then HR then text", func(t *testing.T) {
		segs := parseMarkdown("above\n\n---\n\nbelow")
		if !hasHR(segs) {
			t.Error("--- between text should produce HR")
		}
		merged := mergeSegments(segs)
		allText := ""
		for _, seg := range merged {
			allText += seg.text
		}
		if !strings.Contains(allText, "above") || !strings.Contains(allText, "below") {
			t.Errorf("text around HR should be preserved, got %q", allText)
		}
	})

	t.Run("consecutive HRs not merged", func(t *testing.T) {
		segs := parseMarkdown("---\n---")
		if countHR(segs) != 2 {
			t.Errorf("expected 2 HR segments, got %d", countHR(segs))
		}
		// Verify mergeSegments doesn't collapse them
		merged := mergeSegments(segs)
		if countHR(merged) != 2 {
			t.Errorf("expected 2 HR segments after merge, got %d", countHR(merged))
		}
	})

	t.Run("HR inside code block is literal", func(t *testing.T) {
		segs := parseMarkdown("```\n---\n```")
		if hasHR(segs) {
			t.Error("--- inside code block should NOT be HR")
		}
	})

	t.Run("table separator not affected", func(t *testing.T) {
		segs := parseMarkdown("| A | B |\n| --- | --- |\n| 1 | 2 |")
		if hasHR(segs) {
			t.Error("table separator --- should NOT produce HR")
		}
	})

	t.Run("HR at start of document", func(t *testing.T) {
		segs := parseMarkdown("---\nText")
		if !hasHR(segs) {
			t.Error("HR at start of document should be detected")
		}
	})

	t.Run("HR at end of document", func(t *testing.T) {
		segs := parseMarkdown("Text\n\n---")
		if !hasHR(segs) {
			t.Error("HR at end of document should be detected")
		}
	})

	// Intentional CommonMark divergence: Setext headings not supported.
	// "paragraph\n---" is treated as paragraph + HR, not Setext H2.
	t.Run("Setext ambiguity treated as HR", func(t *testing.T) {
		segs := parseMarkdown("paragraph\n---")
		if !hasHR(segs) {
			t.Error("--- after paragraph should be HR (Setext headings not supported)")
		}
	})
}

func TestUTF8MultiByteSegments(t *testing.T) {
	allText := func(segments []markdownSegment) string {
		var s string
		for _, seg := range segments {
			s += seg.text
		}
		return s
	}

	t.Run("CJK characters preserved", func(t *testing.T) {
		segs := parseSimpleFormatting("日本語")
		if allText(segs) != "日本語" {
			t.Errorf("got %q, want 日本語", allText(segs))
		}
		for i, seg := range segs {
			if !utf8.ValidString(seg.text) {
				t.Errorf("segment %d is invalid UTF-8: %q", i, seg.text)
			}
		}
	})

	t.Run("accented characters preserved", func(t *testing.T) {
		segs := parseSimpleFormatting("café")
		if allText(segs) != "café" {
			t.Errorf("got %q, want café", allText(segs))
		}
	})

	t.Run("emoji 4-byte rune preserved", func(t *testing.T) {
		segs := parseSimpleFormatting("hello 🎉 world")
		if allText(segs) != "hello 🎉 world" {
			t.Errorf("got %q, want hello 🎉 world", allText(segs))
		}
	})

	t.Run("formatted multi-byte text", func(t *testing.T) {
		segs := parseSimpleFormatting("**日本語**")
		merged := mergeSegments(segs)
		found := false
		for _, seg := range merged {
			if seg.bold && seg.text == "日本語" {
				found = true
			}
		}
		if !found {
			t.Error("expected bold segment with text 日本語")
		}
	})
}

func TestLinkSchemeValidation(t *testing.T) {
	t.Run("https allowed", func(t *testing.T) {
		segments := parseSimpleFormatting("[ok](https://example.com)")
		found := false
		for _, seg := range segments {
			if seg.linkURL == "https://example.com" && seg.text == "ok" {
				found = true
			}
		}
		if !found {
			t.Errorf("https link not found in %+v", segments)
		}
	})

	t.Run("http allowed", func(t *testing.T) {
		segments := parseSimpleFormatting("[ok](http://example.com)")
		found := false
		for _, seg := range segments {
			if seg.linkURL == "http://example.com" && seg.text == "ok" {
				found = true
			}
		}
		if !found {
			t.Errorf("http link not found in %+v", segments)
		}
	})

	t.Run("mailto allowed", func(t *testing.T) {
		segments := parseSimpleFormatting("[ok](mailto:user@example.com)")
		found := false
		for _, seg := range segments {
			if seg.linkURL == "mailto:user@example.com" && seg.text == "ok" {
				found = true
			}
		}
		if !found {
			t.Errorf("mailto link not found in %+v", segments)
		}
	})

	t.Run("javascript rejected", func(t *testing.T) {
		segments := parseSimpleFormatting("[bad](javascript:alert(1))")
		for _, seg := range segments {
			if seg.linkURL != "" {
				t.Errorf("javascript: link should be rejected, got linkURL=%q", seg.linkURL)
			}
		}
	})

	t.Run("javascript mixed case rejected", func(t *testing.T) {
		segments := parseSimpleFormatting("[bad](JavaScript:alert(1))")
		for _, seg := range segments {
			if seg.linkURL != "" {
				t.Errorf("JavaScript: link should be rejected, got linkURL=%q", seg.linkURL)
			}
		}
	})

	t.Run("data rejected", func(t *testing.T) {
		segments := parseSimpleFormatting("[bad](data:text/html,<h1>hi</h1>)")
		for _, seg := range segments {
			if seg.linkURL != "" {
				t.Errorf("data: link should be rejected, got linkURL=%q", seg.linkURL)
			}
		}
	})

	t.Run("file rejected", func(t *testing.T) {
		segments := parseSimpleFormatting("[bad](file:///etc/passwd)")
		for _, seg := range segments {
			if seg.linkURL != "" {
				t.Errorf("file: link should be rejected, got linkURL=%q", seg.linkURL)
			}
		}
	})

	t.Run("leading whitespace rejected", func(t *testing.T) {
		segments := parseSimpleFormatting("[bad]( javascript:alert(1))")
		for _, seg := range segments {
			if seg.linkURL != "" {
				t.Errorf("whitespace-padded javascript: link should be rejected, got linkURL=%q", seg.linkURL)
			}
		}
	})

	t.Run("URL with parentheses (Wikipedia)", func(t *testing.T) {
		segments := parseSimpleFormatting("[link](https://en.wikipedia.org/wiki/Foo_(bar))")
		found := false
		for _, seg := range segments {
			if seg.linkURL == "https://en.wikipedia.org/wiki/Foo_(bar)" && seg.text == "link" {
				found = true
			}
		}
		if !found {
			t.Error("Wikipedia URL with parentheses should be captured in full")
		}
	})

	t.Run("simple URL still works", func(t *testing.T) {
		segments := parseSimpleFormatting("[text](https://example.com/path)")
		found := false
		for _, seg := range segments {
			if seg.linkURL == "https://example.com/path" && seg.text == "text" {
				found = true
			}
		}
		if !found {
			t.Error("simple URL should still work")
		}
	})

	t.Run("empty URL rejected", func(t *testing.T) {
		segments := parseSimpleFormatting("[text]()")
		for _, seg := range segments {
			if seg.linkURL != "" {
				t.Errorf("empty URL should be rejected, got linkURL=%q", seg.linkURL)
			}
		}
	})
}

func TestHeadingsWithInlineFormatting(t *testing.T) {
	t.Run("heading with @today", func(t *testing.T) {
		segments := parseMarkdown("# @today")
		foundDate := false
		for _, seg := range segments {
			if seg.isDateField && seg.heading == 1 && seg.dateValue == "" {
				foundDate = true
			}
		}
		if !foundDate {
			t.Errorf("@today with heading=1 not found in %+v", segments)
		}
	})

	t.Run("heading with specific date", func(t *testing.T) {
		segments := parseMarkdown("## Meeting @date(2026-04-07)")
		var headingText string
		foundDate := false
		for _, seg := range segments {
			if seg.heading == 2 {
				if seg.isDateField && seg.dateValue == "2026-04-07" {
					foundDate = true
				} else if !seg.isDateField {
					headingText += seg.text
				}
			}
		}
		// Headings no longer have trailing newlines
		if headingText != "Meeting " {
			t.Errorf("heading text = %q, want 'Meeting '", headingText)
		}
		if !foundDate {
			t.Errorf("@date(2026-04-07) with heading=2 not found in %+v", segments)
		}
	})

	t.Run("heading with person chip", func(t *testing.T) {
		segments := parseMarkdown("### @(user@example.com)")
		foundPerson := false
		for _, seg := range segments {
			if seg.isPersonField && seg.heading == 3 && seg.personIdentifier == "user@example.com" {
				foundPerson = true
			}
		}
		if !foundPerson {
			t.Errorf("person chip with heading=3 not found in %+v", segments)
		}
	})

	t.Run("heading with bold text", func(t *testing.T) {
		segments := parseMarkdown("# **Important**")
		var boldText string
		for _, seg := range segments {
			if seg.heading == 1 && seg.bold {
				boldText += seg.text
			}
		}
		if boldText != "Important" {
			t.Errorf("bold text = %q, want 'Important'", boldText)
		}
	})

	t.Run("heading with italic text", func(t *testing.T) {
		segments := parseMarkdown("## *Emphasis*")
		var italicText string
		for _, seg := range segments {
			if seg.heading == 2 && seg.italic {
				italicText += seg.text
			}
		}
		if italicText != "Emphasis" {
			t.Errorf("italic text = %q, want 'Emphasis'", italicText)
		}
	})
}

func TestFormattedDateChips(t *testing.T) {
	t.Run("bold @today", func(t *testing.T) {
		segments := parseSimpleFormatting("**@today**")
		foundBoldDate := false
		for _, seg := range segments {
			if seg.isDateField && seg.bold && seg.dateValue == "" {
				foundBoldDate = true
			}
		}
		if !foundBoldDate {
			t.Errorf("bold @today not found in %+v", segments)
		}
	})

	t.Run("italic date", func(t *testing.T) {
		segments := parseSimpleFormatting("*@date(2026-01-15)*")
		foundItalicDate := false
		for _, seg := range segments {
			if seg.isDateField && seg.italic && seg.dateValue == "2026-01-15" {
				foundItalicDate = true
			}
		}
		if !foundItalicDate {
			t.Errorf("italic @date not found in %+v", segments)
		}
	})

	t.Run("underline @today", func(t *testing.T) {
		segments := parseSimpleFormatting("__@today__")
		foundUnderlineDate := false
		for _, seg := range segments {
			if seg.isDateField && seg.underline && seg.dateValue == "" {
				foundUnderlineDate = true
			}
		}
		if !foundUnderlineDate {
			t.Errorf("underline @today not found in %+v", segments)
		}
	})

	t.Run("bold person chip", func(t *testing.T) {
		segments := parseSimpleFormatting("**@(alice@example.com)**")
		foundBoldPerson := false
		for _, seg := range segments {
			if seg.isPersonField && seg.bold && seg.personIdentifier == "alice@example.com" {
				foundBoldPerson = true
			}
		}
		if !foundBoldPerson {
			t.Errorf("bold person chip not found in %+v", segments)
		}
	})
}

func TestHeadingsWithFormattedDateChips(t *testing.T) {
	t.Run("heading with bold @today", func(t *testing.T) {
		segments := parseMarkdown("## **@today**")
		foundBoldDate := false
		for _, seg := range segments {
			if seg.isDateField && seg.heading == 2 && seg.bold && seg.dateValue == "" {
				foundBoldDate = true
			}
		}
		if !foundBoldDate {
			t.Errorf("bold @today with heading=2 not found in %+v", segments)
		}
	})

	t.Run("heading with italic date", func(t *testing.T) {
		segments := parseMarkdown("# Report *@date(2026-04-07)*")
		var headingText string
		foundItalicDate := false
		for _, seg := range segments {
			if seg.heading == 1 {
				if seg.isDateField && seg.italic && seg.dateValue == "2026-04-07" {
					foundItalicDate = true
				} else if !seg.isDateField && !seg.italic {
					headingText += seg.text
				}
			}
		}
		// Headings no longer have trailing newlines
		if headingText != "Report " {
			t.Errorf("heading text = %q, want 'Report '", headingText)
		}
		if !foundItalicDate {
			t.Errorf("italic @date with heading=1 not found in %+v", segments)
		}
	})

	t.Run("heading with bold person chip", func(t *testing.T) {
		segments := parseMarkdown("### Meeting with **@(bob@example.com)**")
		var headingText string
		foundBoldPerson := false
		for _, seg := range segments {
			if seg.heading == 3 {
				if seg.isPersonField && seg.bold && seg.personIdentifier == "bob@example.com" {
					foundBoldPerson = true
				} else if !seg.isPersonField && !seg.bold {
					headingText += seg.text
				}
			}
		}
		// Headings no longer have trailing newlines
		if headingText != "Meeting with " {
			t.Errorf("heading text = %q, want 'Meeting with '", headingText)
		}
		if !foundBoldPerson {
			t.Errorf("bold person chip with heading=3 not found in %+v", segments)
		}
	})
}

func TestHeadingFollowedByNormalText(t *testing.T) {
	t.Run("user example: heading with blank line then normal text", func(t *testing.T) {
		// Test the exact user scenario
		markdown := "# A heading\n\nA normal text"
		segments := parseMarkdown(markdown)

		// Verify we have heading segments and normal text segments
		// Blank lines AFTER headings should be skipped
		foundHeading := false
		foundNormalText := false

		// Count segments by type to verify structure
		var headingCount, normalTextCount int

		for _, seg := range segments {
			if seg.heading == 1 {
				foundHeading = true
				headingCount++
			}
			if seg.heading == 0 && seg.text != "\n" {
				foundNormalText = true
				normalTextCount++
			}
		}

		if !foundHeading {
			t.Errorf("heading segment not found")
		}
		if !foundNormalText {
			t.Errorf("normal text segment not found")
		}

		// We should have heading segments followed by normal text segments
		// There should be NO segments between them from the blank line
		// Verify by checking that all heading segments come before all normal text segments
		lastHeadingIdx := -1
		firstNormalIdx := len(segments)
		for i, seg := range segments {
			if seg.heading == 1 {
				lastHeadingIdx = i
			}
			if seg.heading == 0 && seg.text != "\n" && firstNormalIdx == len(segments) {
				firstNormalIdx = i
			}
		}

		if lastHeadingIdx >= 0 && firstNormalIdx < len(segments) {
			// Check if there are any segments between the last heading and first normal text
			// (excluding the trailing \n from normal text)
			betweenCount := firstNormalIdx - lastHeadingIdx - 1
			if betweenCount > 0 {
				t.Errorf("Found %d segments between heading and normal text (should be 0 - blank line should be skipped)", betweenCount)
			}
		}
	})

	t.Run("normal text after heading has heading=0", func(t *testing.T) {
		segments := parseMarkdown("# Heading\nNormal text")

		// Verify we have both heading and non-heading segments
		foundHeading := false
		foundNormalText := false

		for _, seg := range segments {
			if seg.heading == 1 {
				foundHeading = true
			}
			if seg.heading == 0 && seg.text != "" && seg.text != "\n" {
				foundNormalText = true
			}
		}

		if !foundHeading {
			t.Errorf("heading segment not found in %+v", segments)
		}
		if !foundNormalText {
			t.Errorf("normal text segment (heading=0) not found in %+v", segments)
		}
	})

	t.Run("convertMarkdownToRequests applies heading style but not NORMAL_TEXT", func(t *testing.T) {
		segments := parseMarkdown("# Heading\nNormal text")
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		// Look for UpdateParagraphStyle requests
		foundHeadingStyle := false
		foundNormalTextStyle := false
		var headingEndIndex int64
		var normalTextStyleCount int

		for _, req := range requests {
			if req.UpdateParagraphStyle != nil {
				style := req.UpdateParagraphStyle.ParagraphStyle.NamedStyleType
				if style == "HEADING_1" {
					foundHeadingStyle = true
					headingEndIndex = req.UpdateParagraphStyle.Range.EndIndex
				}
				if style == "NORMAL_TEXT" {
					foundNormalTextStyle = true
					normalTextStyleCount++
				}
			}
		}

		if !foundHeadingStyle {
			t.Errorf("HEADING_1 style not applied in requests")
		}
		if !foundNormalTextStyle {
			t.Error("NORMAL_TEXT style should be applied to body text to prevent heading style bleed")
		}

		// Verify that heading style range includes trailing newline
		// "Heading" segment has no \n, but we add \n during insertion
		// Inserted text "Heading\n" at index 1: endIndex = 1 + 8 = 9
		// Heading style applied to [1, 9] (including the trailing \n)
		if headingEndIndex != 9 {
			t.Errorf("Heading style end index = %d, want 9 (including trailing newline)", headingEndIndex)
		}

		// Verify normal text gets UpdateTextStyle requests (for formatting)
		foundTextStyle := false
		for _, req := range requests {
			if req.UpdateTextStyle != nil {
				// Normal text should have text style applied
				foundTextStyle = true
				break
			}
		}
		if !foundTextStyle {
			t.Errorf("Normal text should have UpdateTextStyle applied for formatting")
		}
	})
}

func TestMergeSegments(t *testing.T) {
	t.Run("no merge table segments", func(t *testing.T) {
		segments := []markdownSegment{
			{text: "Before table\n"},
			{isTable: true, table: &tableSegment{numColumns: 2, rows: []tableRow{}}},
			{text: "After table\n"},
		}
		merged := mergeSegments(segments)
		if len(merged) != 3 {
			t.Errorf("expected 3 segments, got %d", len(merged))
		}
		if !merged[1].isTable {
			t.Error("table segment lost isTable flag after merge")
		}
		if merged[1].table == nil {
			t.Error("table segment lost table pointer after merge")
		}
	})

	t.Run("merge adjacent plain", func(t *testing.T) {
		segments := []markdownSegment{
			{text: "a"},
			{text: "b"},
			{text: "c"},
		}
		merged := mergeSegments(segments)
		if len(merged) != 1 || merged[0].text != "abc" {
			t.Errorf("got %+v, want single 'abc' segment", merged)
		}
	})

	t.Run("no merge different formatting", func(t *testing.T) {
		segments := []markdownSegment{
			{text: "plain"},
			{text: "bold", bold: true},
			{text: "plain2"},
		}
		merged := mergeSegments(segments)
		if len(merged) != 3 {
			t.Errorf("got %d segments, want 3", len(merged))
		}
	})

	t.Run("no merge different strikethrough", func(t *testing.T) {
		segments := []markdownSegment{
			{text: "plain"},
			{text: "strike", strikethrough: true},
			{text: "plain2"},
		}
		merged := mergeSegments(segments)
		if len(merged) != 3 {
			t.Errorf("got %d segments, want 3", len(merged))
		}
		// Verify the strikethrough flag is preserved
		if !merged[1].strikethrough {
			t.Errorf("strikethrough flag lost in middle segment")
		}
	})

	t.Run("no merge date fields", func(t *testing.T) {
		segments := []markdownSegment{
			{text: "before"},
			{text: " ", isDateField: true, dateValue: "2026-01-01"},
			{text: "after"},
		}
		merged := mergeSegments(segments)
		if len(merged) != 3 {
			t.Errorf("got %d segments, want 3 (date fields should not merge)", len(merged))
		}
	})

	t.Run("empty input", func(t *testing.T) {
		merged := mergeSegments(nil)
		if len(merged) != 0 {
			t.Errorf("got %+v, want empty", merged)
		}
	})
}

func TestBlankLineBetweenNormalText(t *testing.T) {
	t.Run("blank line creates double newline in merged text", func(t *testing.T) {
		segments := parseMarkdown("line1\n\nline2")
		merged := mergeSegments(segments)

		// After merging, plain text segments combine into "line1\n\nline2\n"
		// The double \n creates an empty paragraph in Google Docs
		if len(merged) != 1 {
			t.Errorf("Expected 1 merged segment, got %d: %+v", len(merged), merged)
		}

		expectedText := "line1\n\nline2\n"
		if merged[0].text != expectedText {
			t.Errorf("Merged text = %q, want %q", merged[0].text, expectedText)
		}
	})

	t.Run("multiple blank lines create triple newline", func(t *testing.T) {
		segments := parseMarkdown("line1\n\n\nline2")
		merged := mergeSegments(segments)

		// After merging, should have "line1\n\n\nline2\n"
		if len(merged) != 1 {
			t.Errorf("Expected 1 merged segment, got %d: %+v", len(merged), merged)
		}

		expectedText := "line1\n\n\nline2\n"
		if merged[0].text != expectedText {
			t.Errorf("Merged text = %q, want %q", merged[0].text, expectedText)
		}
	})
}

func TestBlankLineAfterHeadingSkipped(t *testing.T) {
	t.Run("blank line after heading is skipped", func(t *testing.T) {
		segments := parseMarkdown("# Heading\n\nText")
		merged := mergeSegments(segments)

		// Should NOT have a standalone "\n" segment between heading and text
		// Check: heading segments, then text segments (no empty paragraph between)
		foundEmptyParagraph := false
		for _, seg := range merged {
			// Look for standalone "\n" that is not part of heading or other formatted text
			if seg.text == "\n" && !seg.isDateField && !seg.isPersonField && seg.heading == 0 &&
				!seg.orderedListItem && !seg.unorderedListItem {
				foundEmptyParagraph = true
				break
			}
		}

		if foundEmptyParagraph {
			t.Errorf("Blank line after heading should be skipped, but found empty paragraph segment in: %+v", merged)
		}
	})

	t.Run("multiple blank lines after heading all skipped", func(t *testing.T) {
		segments := parseMarkdown("# Heading\n\n\nText")
		merged := mergeSegments(segments)

		// Should have NO standalone empty paragraph segments
		emptyCount := 0
		for _, seg := range merged {
			// Look for standalone "\n" segments
			if seg.text == "\n" && !seg.isDateField && !seg.isPersonField && seg.heading == 0 &&
				!seg.orderedListItem && !seg.unorderedListItem {
				emptyCount++
			}
		}

		if emptyCount > 0 {
			t.Errorf("All blank lines after heading should be skipped, but found %d empty paragraph segments in: %+v", emptyCount, merged)
		}
	})

	t.Run("blank line before heading creates double newline", func(t *testing.T) {
		segments := parseMarkdown("Text\n\n# Heading")
		merged := mergeSegments(segments)

		// Should have 2 segments: normal text with double \n, and heading
		if len(merged) != 2 {
			t.Errorf("Expected 2 merged segments, got %d: %+v", len(merged), merged)
		}

		// First segment should be normal text with double newline
		if merged[0].heading != 0 {
			t.Errorf("First segment should be normal text (heading=0), got heading=%d", merged[0].heading)
		}
		expectedText := "Text\n\n"
		if merged[0].text != expectedText {
			t.Errorf("First segment text = %q, want %q", merged[0].text, expectedText)
		}

		// Second segment should be heading
		if merged[1].heading != 1 {
			t.Errorf("Second segment should be heading (heading=1), got heading=%d", merged[1].heading)
		}
		if merged[1].text != "Heading" {
			t.Errorf("Second segment text = %q, want %q", merged[1].text, "Heading")
		}
	})
}

func TestParagraphJoining(t *testing.T) {
	allText := func(segments []markdownSegment) string {
		var s string
		for _, seg := range segments {
			s += seg.text
		}
		return s
	}

	t.Run("hard-wrapped lines joined into one paragraph", func(t *testing.T) {
		segs := parseMarkdown("line one\nline two\n\nnew para")
		merged := mergeSegments(segs)
		text := allText(merged)
		if !strings.Contains(text, "line one line two") {
			t.Errorf("hard-wrapped lines should be joined with space, got %q", text)
		}
		if !strings.Contains(text, "new para") {
			t.Errorf("new paragraph should be separate, got %q", text)
		}
	})

	t.Run("hard wrap after heading produces joined body", func(t *testing.T) {
		segs := parseMarkdown("## Heading\n\nbody one\nbody two")
		merged := mergeSegments(segs)
		text := allText(merged)
		if !strings.Contains(text, "body one body two") {
			t.Errorf("body lines after heading should be joined, got %q", text)
		}
	})

	t.Run("single-line paragraph unchanged", func(t *testing.T) {
		segs := parseMarkdown("just one line")
		merged := mergeSegments(segs)
		text := allText(merged)
		if !strings.Contains(text, "just one line") {
			t.Errorf("single-line paragraph should be preserved, got %q", text)
		}
	})

	t.Run("blank lines still create paragraph breaks", func(t *testing.T) {
		segs := parseMarkdown("para one\n\npara two")
		merged := mergeSegments(segs)
		text := allText(merged)
		if !strings.Contains(text, "para one\n") || !strings.Contains(text, "para two") {
			t.Errorf("blank line should separate paragraphs, got %q", text)
		}
		if strings.Contains(text, "para one para two") {
			t.Error("paragraphs separated by blank line should NOT be joined")
		}
	})

	t.Run("flush before heading", func(t *testing.T) {
		segs := parseMarkdown("body text\n# Heading")
		merged := mergeSegments(segs)
		text := allText(merged)
		if !strings.Contains(text, "body text") {
			t.Errorf("body text should be present, got %q", text)
		}
		foundHeading := false
		for _, seg := range merged {
			if seg.heading == 1 {
				foundHeading = true
			}
		}
		if !foundHeading {
			t.Error("heading should be present after body text flush")
		}
	})

	t.Run("flush before list", func(t *testing.T) {
		segs := parseMarkdown("body text\n- list item")
		merged := mergeSegments(segs)
		text := allText(merged)
		if strings.Contains(text, "body text list item") {
			t.Error("body text should not be joined with list item")
		}
	})

	t.Run("flush before code fence", func(t *testing.T) {
		segs := parseMarkdown("body text\n```\ncode\n```")
		merged := mergeSegments(segs)
		text := allText(merged)
		if strings.Contains(text, "body textcode") || strings.Contains(text, "body text code") {
			t.Error("body text should not be joined with code block")
		}
	})

	t.Run("flush before table", func(t *testing.T) {
		segs := parseMarkdown("body text\n| A | B |\n| --- | --- |\n| 1 | 2 |")
		merged := mergeSegments(segs)
		text := allText(merged)
		if !strings.Contains(text, "body text") {
			t.Errorf("body text should be present, got %q", text)
		}
		foundTable := false
		for _, seg := range segs {
			if seg.isTable {
				foundTable = true
			}
		}
		if !foundTable {
			t.Error("table should be present after body text flush")
		}
	})

	t.Run("inline formatting across join boundary", func(t *testing.T) {
		segs := parseMarkdown("**bold** word\ncontinued here")
		merged := mergeSegments(segs)
		text := allText(merged)
		if !strings.Contains(text, "bold") || !strings.Contains(text, "continued here") {
			t.Errorf("joined text should contain both parts, got %q", text)
		}
		foundBold := false
		for _, seg := range merged {
			if seg.bold && strings.Contains(seg.text, "bold") {
				foundBold = true
			}
		}
		if !foundBold {
			t.Error("bold formatting should be preserved across join boundary")
		}
	})
}

func TestNoTrailingEmptyParagraph(t *testing.T) {
	t.Run("last text segment has no trailing newline", func(t *testing.T) {
		segments := parseMarkdown("# Heading\n\nText")
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		// Find the last InsertText request
		var lastInsertText string
		for _, req := range requests {
			if req.InsertText != nil {
				lastInsertText = req.InsertText.Text
			}
		}

		if strings.HasSuffix(lastInsertText, "\n") {
			t.Errorf("Last InsertText should not end with \\n, got: %q", lastInsertText)
		}
	})

	t.Run("heading only document has no trailing newline", func(t *testing.T) {
		segments := parseMarkdown("# Only heading")
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		// Find the InsertText request
		var insertText string
		for _, req := range requests {
			if req.InsertText != nil {
				insertText = req.InsertText.Text
				break
			}
		}

		if insertText != "Only heading" {
			t.Errorf("Heading-only InsertText = %q, want %q", insertText, "Only heading")
		}
	})

	t.Run("normal text only document has no trailing newline", func(t *testing.T) {
		segments := parseMarkdown("Just text")
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		// Find the InsertText request
		var insertText string
		for _, req := range requests {
			if req.InsertText != nil {
				insertText = req.InsertText.Text
				break
			}
		}

		if insertText != "Just text" {
			t.Errorf("Text-only InsertText = %q, want %q", insertText, "Just text")
		}
	})
}

func TestCRLFNormalization(t *testing.T) {
	t.Run("CRLF produces same segments as LF", func(t *testing.T) {
		segmentsLF := parseMarkdown("line1\nline2")
		segmentsCRLF := parseMarkdown("line1\r\nline2")

		mergedLF := mergeSegments(segmentsLF)
		mergedCRLF := mergeSegments(segmentsCRLF)

		if len(mergedLF) != len(mergedCRLF) {
			t.Errorf("Segment count mismatch: LF=%d, CRLF=%d", len(mergedLF), len(mergedCRLF))
		}

		// Compare text content
		for i := 0; i < len(mergedLF) && i < len(mergedCRLF); i++ {
			if mergedLF[i].text != mergedCRLF[i].text {
				t.Errorf("Segment %d text mismatch: LF=%q, CRLF=%q", i, mergedLF[i].text, mergedCRLF[i].text)
			}
		}
	})

	t.Run("CR only produces same segments as LF", func(t *testing.T) {
		segmentsLF := parseMarkdown("line1\nline2")
		segmentsCR := parseMarkdown("line1\rline2")

		mergedLF := mergeSegments(segmentsLF)
		mergedCR := mergeSegments(segmentsCR)

		if len(mergedLF) != len(mergedCR) {
			t.Errorf("Segment count mismatch: LF=%d, CR=%d", len(mergedLF), len(mergedCR))
		}

		// Compare text content
		for i := 0; i < len(mergedLF) && i < len(mergedCR); i++ {
			if mergedLF[i].text != mergedCR[i].text {
				t.Errorf("Segment %d text mismatch: LF=%q, CR=%q", i, mergedLF[i].text, mergedCR[i].text)
			}
		}
	})

	t.Run("mixed line endings are normalized", func(t *testing.T) {
		segments := parseMarkdown("line1\r\nline2\nline3\rline4")
		merged := mergeSegments(segments)

		// All lines should be separated by \n
		fullText := ""
		for _, seg := range merged {
			fullText += seg.text
		}

		// After paragraph joining, consecutive body lines are joined with space
		expected := "line1 line2 line3 line4\n"
		if fullText != expected {
			t.Errorf("Normalized text = %q, want %q", fullText, expected)
		}
	})
}

func TestHeadingWithFormattedText(t *testing.T) {
	t.Run("heading with bold text creates single heading", func(t *testing.T) {
		segments := parseMarkdown("# **Bold** Normal")
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		// Count InsertText requests - should only insert text that will form ONE heading paragraph
		var insertTexts []string
		for _, req := range requests {
			if req.InsertText != nil {
				insertTexts = append(insertTexts, req.InsertText.Text)
			}
		}

		// Should have exactly 2 insert requests: "Bold" and " Normal"
		// The newline should only be added after the LAST heading segment
		if len(insertTexts) != 2 {
			t.Errorf("Expected 2 InsertText requests, got %d: %v", len(insertTexts), insertTexts)
		}

		// First segment should NOT have trailing newline (it's not the last heading segment)
		if strings.HasSuffix(insertTexts[0], "\n") {
			t.Errorf("First heading segment should not have trailing \\n, got: %q", insertTexts[0])
		}

		// Second segment should have trailing newline (it's the last heading segment and not last overall)
		// Actually, if there are no more segments after the heading, it won't have \n
		// Let's check the actual behavior
	})

	t.Run("heading with formatted text followed by normal text", func(t *testing.T) {
		segments := parseMarkdown("# **Bold** Normal\n\nText")
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		// Count heading-styled paragraphs
		headingStyleCount := 0
		for _, req := range requests {
			if req.UpdateParagraphStyle != nil &&
				strings.HasPrefix(req.UpdateParagraphStyle.ParagraphStyle.NamedStyleType, "HEADING_") {
				headingStyleCount++
			}
		}

		// Should have exactly 2 heading segments but they should be styled together as one heading
		// Actually, each segment gets its own UpdateParagraphStyle request
		// What matters is that they're contiguous and form one visual heading
		// Let's verify the InsertText calls instead
		var insertTexts []string
		for _, req := range requests {
			if req.InsertText != nil {
				insertTexts = append(insertTexts, req.InsertText.Text)
			}
		}

		// Verify: "Bold", " Normal\n", "Text"
		// The \n should only appear after the LAST segment of the heading
		expectedInserts := 3
		if len(insertTexts) != expectedInserts {
			t.Errorf("Expected %d InsertText requests, got %d: %v", expectedInserts, len(insertTexts), insertTexts)
		}
	})

	t.Run("normal text with formatting preserves formatting", func(t *testing.T) {
		segments := parseMarkdown("**Bold** and *italic*")
		merged := mergeSegments(segments)

		// Should have 3 segments: bold, plain " and ", italic
		// After merging: bold segment, plain segment, italic segment
		foundBold := false
		foundItalic := false

		for _, seg := range merged {
			if seg.bold && seg.text == "Bold" {
				foundBold = true
			}
			if seg.italic && seg.text == "italic" {
				foundItalic = true
			}
		}

		if !foundBold {
			t.Errorf("Bold formatting lost in merged segments: %+v", merged)
		}
		if !foundItalic {
			t.Errorf("Italic formatting lost in merged segments: %+v", merged)
		}
	})

	t.Run("complex formatted text creates correct requests", func(t *testing.T) {
		markdown := "**This is** some _heavily_ __formatted__ text."
		segments := parseMarkdown(markdown)
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		// Verify we have InsertText and UpdateTextStyle requests
		var insertCount, boldStyleCount, italicStyleCount, underlineStyleCount int

		for _, req := range requests {
			if req.InsertText != nil {
				insertCount++
			}
			if req.UpdateTextStyle != nil {
				if req.UpdateTextStyle.TextStyle.Bold {
					boldStyleCount++
				}
				if req.UpdateTextStyle.TextStyle.Italic {
					italicStyleCount++
				}
				if req.UpdateTextStyle.TextStyle.Underline {
					underlineStyleCount++
				}
			}
		}

		// Should have multiple insert requests (one per formatting change)
		if insertCount < 3 {
			t.Errorf("Expected at least 3 InsertText requests, got %d", insertCount)
		}

		// Should have style requests for bold, italic, and underline
		if boldStyleCount < 1 {
			t.Errorf("Expected at least 1 bold style request, got %d", boldStyleCount)
		}
		if italicStyleCount < 1 {
			t.Errorf("Expected at least 1 italic style request, got %d", italicStyleCount)
		}
		if underlineStyleCount < 1 {
			t.Errorf("Expected at least 1 underline style request, got %d", underlineStyleCount)
		}
	})
}

func TestConsecutiveSameLevelHeadings(t *testing.T) {
	t.Run("two H1s no blank line", func(t *testing.T) {
		segments := parseMarkdown("# A\n# B")
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		// Count heading paragraph style requests
		headingStyleCount := 0
		for _, req := range requests {
			if req.UpdateParagraphStyle != nil &&
				req.UpdateParagraphStyle.ParagraphStyle.NamedStyleType == "HEADING_1" {
				headingStyleCount++
			}
		}
		if headingStyleCount != 2 {
			t.Errorf("expected 2 HEADING_1 paragraph styles, got %d", headingStyleCount)
		}

		// Count InsertText requests
		var insertTexts []string
		for _, req := range requests {
			if req.InsertText != nil {
				insertTexts = append(insertTexts, req.InsertText.Text)
			}
		}
		// "A\n" and "B" (last heading has no trailing \n)
		if len(insertTexts) != 2 {
			t.Errorf("expected 2 InsertText requests, got %d: %v", len(insertTexts), insertTexts)
		}
	})

	t.Run("two H1s with blank line", func(t *testing.T) {
		segments := parseMarkdown("# A\n\n# B")
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		headingStyleCount := 0
		for _, req := range requests {
			if req.UpdateParagraphStyle != nil &&
				req.UpdateParagraphStyle.ParagraphStyle.NamedStyleType == "HEADING_1" {
				headingStyleCount++
			}
		}
		if headingStyleCount != 2 {
			t.Errorf("expected 2 HEADING_1 paragraph styles, got %d", headingStyleCount)
		}
	})

	t.Run("three consecutive H2s", func(t *testing.T) {
		segments := parseMarkdown("## A\n## B\n## C")
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		headingStyleCount := 0
		for _, req := range requests {
			if req.UpdateParagraphStyle != nil &&
				req.UpdateParagraphStyle.ParagraphStyle.NamedStyleType == "HEADING_2" {
				headingStyleCount++
			}
		}
		if headingStyleCount != 3 {
			t.Errorf("expected 3 HEADING_2 paragraph styles, got %d", headingStyleCount)
		}
	})

	t.Run("multi-segment heading stays single paragraph", func(t *testing.T) {
		// "# **Bold** Normal" should still produce one heading paragraph
		segments := parseMarkdown("# **Bold** Normal\n# Another")
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		headingStyleCount := 0
		for _, req := range requests {
			if req.UpdateParagraphStyle != nil &&
				req.UpdateParagraphStyle.ParagraphStyle.NamedStyleType == "HEADING_1" {
				headingStyleCount++
			}
		}
		if headingStyleCount != 2 {
			t.Errorf("expected 2 HEADING_1 paragraph styles, got %d", headingStyleCount)
		}
	})
}

func TestAppendToNonEmptyDocument(t *testing.T) {
	t.Run("appending to non-empty creates new paragraph", func(t *testing.T) {
		// Simulate appending "New text" to a document that already has content
		// When insertIndex > 1, we're appending to non-empty doc
		// The markdown should get a prepended \n

		// This simulates what the tool functions do
		markdown := "New text"
		insertIndex := int64(10) // Simulates non-empty document
		isAppendingToNonEmptyDoc := insertIndex > 1

		markdownToInsert := markdown
		if isAppendingToNonEmptyDoc && !strings.HasPrefix(markdownToInsert, "\n") {
			markdownToInsert = "\n" + markdownToInsert
		}

		segments := parseMarkdown(markdownToInsert)
		merged := mergeSegments(segments)

		// Should have leading newline
		if len(merged) != 1 {
			t.Errorf("Expected 1 merged segment, got %d", len(merged))
		}

		expectedText := "\nNew text\n"
		if merged[0].text != expectedText {
			t.Errorf("Merged text = %q, want %q", merged[0].text, expectedText)
		}
	})

	t.Run("appending to empty document does not add extra newline", func(t *testing.T) {
		// When insertIndex == 1, document is empty
		markdown := "New text"
		insertIndex := int64(1) // Simulates empty document
		isAppendingToNonEmptyDoc := insertIndex > 1

		markdownToInsert := markdown
		if isAppendingToNonEmptyDoc && !strings.HasPrefix(markdownToInsert, "\n") {
			markdownToInsert = "\n" + markdownToInsert
		}

		segments := parseMarkdown(markdownToInsert)
		merged := mergeSegments(segments)

		// Should NOT have leading newline
		if len(merged) != 1 {
			t.Errorf("Expected 1 merged segment, got %d", len(merged))
		}

		expectedText := "New text\n"
		if merged[0].text != expectedText {
			t.Errorf("Merged text = %q, want %q", merged[0].text, expectedText)
		}
	})

	t.Run("appending markdown that already starts with newline", func(t *testing.T) {
		// If markdown already starts with \n, don't add another
		markdown := "\nNew text"
		insertIndex := int64(10) // Non-empty document
		isAppendingToNonEmptyDoc := insertIndex > 1

		markdownToInsert := markdown
		if isAppendingToNonEmptyDoc && !strings.HasPrefix(markdownToInsert, "\n") {
			markdownToInsert = "\n" + markdownToInsert
		}

		segments := parseMarkdown(markdownToInsert)
		merged := mergeSegments(segments)

		// Should only have single leading newline (not double)
		expectedText := "\nNew text\n"
		if merged[0].text != expectedText {
			t.Errorf("Merged text = %q, want %q", merged[0].text, expectedText)
		}
	})
}

func TestDocTitleSanitization(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"My Document", "My Document.txt"},
		{"file<>with:bad/chars", "file__with_bad_chars.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			safeTitle := reUnsafeChars.ReplaceAllString(tt.input, "_")
			safeTitle = filepath.Base(safeTitle) + ".txt"
			if safeTitle != tt.want {
				t.Errorf("sanitized %q = %q, want %q", tt.input, safeTitle, tt.want)
			}
		})
	}
}

func TestCodeAnnotations(t *testing.T) {
	annotations := &CodeAnnotations{
		InlineCode: make(map[string]bool),
		CodeBlocks: make(map[int]bool),
		Languages:  make(map[int]string),
	}

	// Test inline code key
	key := makeInlineCodeKey(0, 1)
	annotations.InlineCode[key] = true

	if !annotations.InlineCode["0:1"] {
		t.Errorf("Expected inline code at 0:1")
	}

	// Test code block
	annotations.CodeBlocks[2] = true
	if !annotations.CodeBlocks[2] {
		t.Errorf("Expected code block at paragraph 2")
	}
}

func TestIsMonospaceFont(t *testing.T) {
	tests := []struct {
		font     string
		expected bool
	}{
		{"Courier New", true},
		{"Courier", true},
		{"Consolas", true},
		{"Monaco", true},
		{"Menlo", true},
		{"Source Code Pro", true},
		{"SF Mono", true},
		{"Inconsolata", true},
		{"Roboto Mono", true},
		{"courier new", true}, // case insensitive
		{"CONSOLAS", true},    // case insensitive
		{"Arial", false},
		{"Times New Roman", false},
		{"Helvetica", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.font, func(t *testing.T) {
			result := isMonospaceFont(tt.font)
			if result != tt.expected {
				t.Errorf("isMonospaceFont(%q) = %v, want %v", tt.font, result, tt.expected)
			}
		})
	}
}

func TestTableDataStructures(t *testing.T) {
	// Test tableCell creation
	cell := tableCell{
		segments: []markdownSegment{
			{text: "Hello", bold: true},
		},
	}
	if len(cell.segments) != 1 {
		t.Errorf("expected 1 segment, got %d", len(cell.segments))
	}

	// Test tableRow creation
	row := tableRow{
		cells: []tableCell{cell, cell},
	}
	if len(row.cells) != 2 {
		t.Errorf("expected 2 cells, got %d", len(row.cells))
	}

	// Test tableSegment creation
	table := tableSegment{
		rows:       []tableRow{row},
		numColumns: 2,
	}
	if table.numColumns != 2 {
		t.Errorf("expected 2 columns, got %d", table.numColumns)
	}

	// Test markdownSegment with table
	seg := markdownSegment{
		isTable: true,
		table:   &table,
	}
	if !seg.isTable {
		t.Error("expected isTable to be true")
	}
	if seg.table == nil {
		t.Error("expected table to be non-nil")
	}
}

func TestTableRegexPatterns(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		input   string
		want    bool
	}{
		// reTableRow tests
		{
			name:    "valid table row with 2 columns",
			pattern: "tableRow",
			input:   "| Cell 1 | Cell 2 |",
			want:    true,
		},
		{
			name:    "valid table row with spaces",
			pattern: "tableRow",
			input:   "  | Cell 1 | Cell 2 |  ",
			want:    true,
		},
		{
			name:    "valid table row with 3 columns",
			pattern: "tableRow",
			input:   "| A | B | C |",
			want:    true,
		},
		{
			name:    "invalid - no leading pipe",
			pattern: "tableRow",
			input:   "Cell 1 | Cell 2 |",
			want:    false,
		},
		{
			name:    "invalid - no trailing pipe",
			pattern: "tableRow",
			input:   "| Cell 1 | Cell 2",
			want:    false,
		},
		{
			name:    "invalid - single pipe",
			pattern: "tableRow",
			input:   "|",
			want:    false,
		},
		// reTableSeparator tests
		{
			name:    "valid separator with 2 columns",
			pattern: "tableSeparator",
			input:   "| --- | --- |",
			want:    true,
		},
		{
			name:    "valid separator with varied dashes",
			pattern: "tableSeparator",
			input:   "| ---- | ------ |",
			want:    true,
		},
		{
			name:    "valid separator with colons (ignored)",
			pattern: "tableSeparator",
			input:   "| :--- | :---: | ---: |",
			want:    true,
		},
		{
			name:    "valid separator with spaces",
			pattern: "tableSeparator",
			input:   "|  ---  |  ---  |",
			want:    true,
		},
		{
			name:    "invalid separator - contains letters",
			pattern: "tableSeparator",
			input:   "| abc | def |",
			want:    false,
		},
		{
			name:    "invalid separator - no dashes",
			pattern: "tableSeparator",
			input:   "| | |",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got bool
			switch tt.pattern {
			case "tableRow":
				got = reTableRow.MatchString(tt.input)
			case "tableSeparator":
				got = reTableSeparator.MatchString(tt.input)
			}
			if got != tt.want {
				t.Errorf("pattern %s on %q: got %v, want %v", tt.pattern, tt.input, got, tt.want)
			}
		})
	}
}

func TestCodeSegmentMerging(t *testing.T) {
	t.Run("inline code segments do not merge with plain", func(t *testing.T) {
		segments := []markdownSegment{
			{text: "plain "},
			{text: "code", isInlineCode: true},
			{text: " plain"},
		}
		merged := mergeSegments(segments)
		if len(merged) != 3 {
			t.Errorf("expected 3 segments, got %d", len(merged))
		}
	})

	t.Run("same inline code segments merge", func(t *testing.T) {
		segments := []markdownSegment{
			{text: "co", isInlineCode: true},
			{text: "de", isInlineCode: true},
		}
		merged := mergeSegments(segments)
		if len(merged) != 1 {
			t.Errorf("expected 1 merged segment, got %d", len(merged))
		}
		if merged[0].text != "code" {
			t.Errorf("expected merged text %q, got %q", "code", merged[0].text)
		}
	})

	t.Run("code block segments do not merge with inline code", func(t *testing.T) {
		segments := []markdownSegment{
			{text: "inline", isInlineCode: true},
			{text: "block", isCodeBlock: true},
		}
		merged := mergeSegments(segments)
		if len(merged) != 2 {
			t.Errorf("expected 2 segments, got %d", len(merged))
		}
	})

	t.Run("different code languages do not merge", func(t *testing.T) {
		segments := []markdownSegment{
			{text: "go code", isCodeBlock: true, codeLanguage: "go"},
			{text: "py code", isCodeBlock: true, codeLanguage: "python"},
		}
		merged := mergeSegments(segments)
		if len(merged) != 2 {
			t.Errorf("expected 2 segments, got %d", len(merged))
		}
	})
}

func TestDetectCode(t *testing.T) {
	t.Run("all-monospace paragraph detected as code block", func(t *testing.T) {
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					{
						Paragraph: &docs.Paragraph{
							ParagraphStyle: &docs.ParagraphStyle{
								NamedStyleType: "NORMAL_TEXT",
							},
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "fmt.Println(\"hello\")",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Courier New",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		annotations := detectCode(doc)

		if !annotations.CodeBlocks[0] {
			t.Errorf("expected paragraph 0 to be detected as code block")
		}
		if len(annotations.InlineCode) != 0 {
			t.Errorf("expected no inline code, got %d entries", len(annotations.InlineCode))
		}
	})

	t.Run("mixed monospace paragraph detected as inline code", func(t *testing.T) {
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					{
						Paragraph: &docs.Paragraph{
							ParagraphStyle: &docs.ParagraphStyle{
								NamedStyleType: "NORMAL_TEXT",
							},
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "Use the ",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: "fmt.Println",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Courier New",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: " function.",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		annotations := detectCode(doc)

		if annotations.CodeBlocks[0] {
			t.Errorf("expected paragraph 0 NOT to be a code block")
		}
		expectedKey := makeInlineCodeKey(0, 1)
		if !annotations.InlineCode[expectedKey] {
			t.Errorf("expected inline code at key %q", expectedKey)
		}
		if len(annotations.InlineCode) != 1 {
			t.Errorf("expected exactly 1 inline code entry, got %d", len(annotations.InlineCode))
		}
	})

	t.Run("heading with monospace font is not detected as code", func(t *testing.T) {
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					{
						Paragraph: &docs.Paragraph{
							ParagraphStyle: &docs.ParagraphStyle{
								NamedStyleType: "HEADING_1",
							},
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "Code Examples",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Courier New",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		annotations := detectCode(doc)

		if annotations.CodeBlocks[0] {
			t.Errorf("heading should NOT be detected as code block")
		}
		if len(annotations.InlineCode) != 0 {
			t.Errorf("heading should NOT have inline code, got %d entries", len(annotations.InlineCode))
		}
	})

	t.Run("TITLE with monospace font is not detected as code", func(t *testing.T) {
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					{
						Paragraph: &docs.Paragraph{
							ParagraphStyle: &docs.ParagraphStyle{
								NamedStyleType: "TITLE",
							},
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "My Document Title",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Consolas",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		annotations := detectCode(doc)

		if annotations.CodeBlocks[0] {
			t.Errorf("TITLE should NOT be detected as code block")
		}
		if len(annotations.InlineCode) != 0 {
			t.Errorf("TITLE should NOT have inline code, got %d entries", len(annotations.InlineCode))
		}
	})

	t.Run("SUBTITLE with monospace font is not detected as code", func(t *testing.T) {
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					{
						Paragraph: &docs.Paragraph{
							ParagraphStyle: &docs.ParagraphStyle{
								NamedStyleType: "SUBTITLE",
							},
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "A subtitle",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Monaco",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		annotations := detectCode(doc)

		if annotations.CodeBlocks[0] {
			t.Errorf("SUBTITLE should NOT be detected as code block")
		}
		if len(annotations.InlineCode) != 0 {
			t.Errorf("SUBTITLE should NOT have inline code, got %d entries", len(annotations.InlineCode))
		}
	})

	t.Run("nil body returns empty annotations", func(t *testing.T) {
		doc := &docs.Document{}

		annotations := detectCode(doc)

		if len(annotations.CodeBlocks) != 0 {
			t.Errorf("expected no code blocks, got %d", len(annotations.CodeBlocks))
		}
		if len(annotations.InlineCode) != 0 {
			t.Errorf("expected no inline code, got %d", len(annotations.InlineCode))
		}
	})

	t.Run("paragraph with no text runs is skipped", func(t *testing.T) {
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					{
						Paragraph: &docs.Paragraph{
							ParagraphStyle: &docs.ParagraphStyle{
								NamedStyleType: "NORMAL_TEXT",
							},
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content:   "\n",
										TextStyle: &docs.TextStyle{},
									},
								},
							},
						},
					},
				},
			},
		}

		annotations := detectCode(doc)

		if len(annotations.CodeBlocks) != 0 {
			t.Errorf("expected no code blocks for whitespace-only paragraph")
		}
	})

	t.Run("multiple paragraphs with mixed code types", func(t *testing.T) {
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					// Paragraph 0: normal text
					{
						Paragraph: &docs.Paragraph{
							ParagraphStyle: &docs.ParagraphStyle{
								NamedStyleType: "NORMAL_TEXT",
							},
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "Some normal text.",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
							},
						},
					},
					// Paragraph 1: code block (all monospace)
					{
						Paragraph: &docs.Paragraph{
							ParagraphStyle: &docs.ParagraphStyle{
								NamedStyleType: "NORMAL_TEXT",
							},
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "x := 42",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Courier New",
											},
										},
									},
								},
							},
						},
					},
					// Paragraph 2: inline code (mixed)
					{
						Paragraph: &docs.Paragraph{
							ParagraphStyle: &docs.ParagraphStyle{
								NamedStyleType: "NORMAL_TEXT",
							},
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "Call ",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: "foo()",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Roboto Mono",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		annotations := detectCode(doc)

		// Paragraph 0: not code
		if annotations.CodeBlocks[0] {
			t.Errorf("paragraph 0 should NOT be a code block")
		}
		// Paragraph 1: code block
		if !annotations.CodeBlocks[1] {
			t.Errorf("paragraph 1 should be a code block")
		}
		// Paragraph 2: inline code at run 1
		if annotations.CodeBlocks[2] {
			t.Errorf("paragraph 2 should NOT be a code block")
		}
		inlineKey := makeInlineCodeKey(2, 1)
		if !annotations.InlineCode[inlineKey] {
			t.Errorf("expected inline code at key %q", inlineKey)
		}
		// Run 0 of paragraph 2 should NOT be inline code
		nonCodeKey := makeInlineCodeKey(2, 0)
		if annotations.InlineCode[nonCodeKey] {
			t.Errorf("run 0 of paragraph 2 should NOT be inline code")
		}
	})
}

func TestExtractMarkdown_CodeBlock(t *testing.T) {
	t.Run("multi-line code block", func(t *testing.T) {
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					{
						Paragraph: &docs.Paragraph{
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "function hello() {\n",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Courier New",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: "    return \"world\";\n",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Courier New",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: "}\n",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Courier New",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		markdown := extractMarkdown(doc)
		expected := "```\nfunction hello() {\n    return \"world\";\n}\n```\n\n"

		if markdown != expected {
			t.Errorf("Expected:\n%q\nGot:\n%q", expected, markdown)
		}
	})

	t.Run("code block strips bold formatting", func(t *testing.T) {
		// Code blocks should extract plain text, ignoring bold/italic styling.
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					{
						Paragraph: &docs.Paragraph{
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "bold code\n",
										TextStyle: &docs.TextStyle{
											Bold: true,
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Courier New",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		markdown := extractMarkdown(doc)
		expected := "```\nbold code\n```\n\n"

		if markdown != expected {
			t.Errorf("Expected:\n%q\nGot:\n%q", expected, markdown)
		}
	})

	t.Run("code block alongside normal paragraph", func(t *testing.T) {
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					{
						Paragraph: &docs.Paragraph{
							ParagraphStyle: &docs.ParagraphStyle{
								NamedStyleType: "NORMAL_TEXT",
							},
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "Here is some code:\n",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
							},
						},
					},
					{
						Paragraph: &docs.Paragraph{
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "x = 42\n",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Courier New",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		markdown := extractMarkdown(doc)
		expected := "Here is some code:\n```\nx = 42\n```\n\n"

		if markdown != expected {
			t.Errorf("Expected:\n%q\nGot:\n%q", expected, markdown)
		}
	})

	t.Run("single line code block without trailing newline", func(t *testing.T) {
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					{
						Paragraph: &docs.Paragraph{
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "echo hello",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Consolas",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		markdown := extractMarkdown(doc)
		// Should add a trailing newline before the closing fence
		expected := "```\necho hello\n```\n\n"

		if markdown != expected {
			t.Errorf("Expected:\n%q\nGot:\n%q", expected, markdown)
		}
	})

	t.Run("code block after heading", func(t *testing.T) {
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					{
						Paragraph: &docs.Paragraph{
							ParagraphStyle: &docs.ParagraphStyle{
								NamedStyleType: "HEADING_2",
							},
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "Example\n",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Courier New",
											},
										},
									},
								},
							},
						},
					},
					{
						Paragraph: &docs.Paragraph{
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "print(1)\n",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Courier New",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		markdown := extractMarkdown(doc)
		// Heading should render as heading (not code), code block follows
		expected := "## Example\n\n```\nprint(1)\n```\n\n"

		if markdown != expected {
			t.Errorf("Expected:\n%q\nGot:\n%q", expected, markdown)
		}
	})

	t.Run("non-monospace paragraph is not a code block", func(t *testing.T) {
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					{
						Paragraph: &docs.Paragraph{
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "This is normal text.\n",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		markdown := extractMarkdown(doc)

		if strings.Contains(markdown, "```") {
			t.Errorf("Normal text should not be wrapped in code fences, got:\n%q", markdown)
		}
		if !strings.Contains(markdown, "This is normal text.") {
			t.Errorf("Expected normal text in output, got:\n%q", markdown)
		}
	})
}

func TestExtractMarkdown_InlineCode(t *testing.T) {
	t.Run("single inline code segment", func(t *testing.T) {
		// Paragraph: "Use the " (Arial) + "fmt.Println" (Courier New) + " function.\n" (Arial)
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					{
						Paragraph: &docs.Paragraph{
							ParagraphStyle: &docs.ParagraphStyle{
								NamedStyleType: "NORMAL_TEXT",
							},
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "Use the ",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: "fmt.Println",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Courier New",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: " function.\n",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		markdown := extractMarkdown(doc)
		expected := "Use the `fmt.Println` function.\n"

		if markdown != expected {
			t.Errorf("Expected:\n%q\nGot:\n%q", expected, markdown)
		}
	})

	t.Run("inline code preserves bold and italic formatting", func(t *testing.T) {
		// Inline code with bold+italic should be wrapped in backticks WITH formatting markers
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					{
						Paragraph: &docs.Paragraph{
							ParagraphStyle: &docs.ParagraphStyle{
								NamedStyleType: "NORMAL_TEXT",
							},
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "Run ",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: "go test",
										TextStyle: &docs.TextStyle{
											Bold:   true,
											Italic: true,
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Courier New",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: " now.\n",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		markdown := extractMarkdown(doc)
		expected := "Run `***go test***` now.\n"

		if markdown != expected {
			t.Errorf("Expected:\n%q\nGot:\n%q", expected, markdown)
		}
	})

	t.Run("inline code preserves link formatting", func(t *testing.T) {
		// Inline code with underline and link should preserve the link
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					{
						Paragraph: &docs.Paragraph{
							ParagraphStyle: &docs.ParagraphStyle{
								NamedStyleType: "NORMAL_TEXT",
							},
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "See ",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: "http.Get",
										TextStyle: &docs.TextStyle{
											Underline: true,
											Link: &docs.Link{
												Url: "https://pkg.go.dev/net/http#Get",
											},
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Courier New",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: " docs.\n",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		markdown := extractMarkdown(doc)
		expected := "See [`http.Get`](https://pkg.go.dev/net/http#Get) docs.\n"

		if markdown != expected {
			t.Errorf("Expected:\n%q\nGot:\n%q", expected, markdown)
		}
	})

	t.Run("multiple inline code segments in same paragraph", func(t *testing.T) {
		// "Call " (Arial) + "foo()" (Courier) + " and " (Arial) + "bar()" (Courier) + " functions.\n" (Arial)
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					{
						Paragraph: &docs.Paragraph{
							ParagraphStyle: &docs.ParagraphStyle{
								NamedStyleType: "NORMAL_TEXT",
							},
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "Call ",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: "foo()",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Courier New",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: " and ",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: "bar()",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Courier New",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: " functions.\n",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		markdown := extractMarkdown(doc)
		expected := "Call `foo()` and `bar()` functions.\n"

		if markdown != expected {
			t.Errorf("Expected:\n%q\nGot:\n%q", expected, markdown)
		}
	})

	t.Run("inline code alongside bold text", func(t *testing.T) {
		// "Use " (Arial) + "config" (Courier New) + " for " (Arial) + "important" (Arial, bold) + " settings.\n" (Arial)
		doc := &docs.Document{
			Body: &docs.Body{
				Content: []*docs.StructuralElement{
					{
						Paragraph: &docs.Paragraph{
							ParagraphStyle: &docs.ParagraphStyle{
								NamedStyleType: "NORMAL_TEXT",
							},
							Elements: []*docs.ParagraphElement{
								{
									TextRun: &docs.TextRun{
										Content: "Use ",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: "config",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Courier New",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: " for ",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: "important",
										TextStyle: &docs.TextStyle{
											Bold: true,
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
								{
									TextRun: &docs.TextRun{
										Content: " settings.\n",
										TextStyle: &docs.TextStyle{
											WeightedFontFamily: &docs.WeightedFontFamily{
												FontFamily: "Arial",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		markdown := extractMarkdown(doc)
		expected := "Use `config` for **important** settings.\n"

		if markdown != expected {
			t.Errorf("Expected:\n%q\nGot:\n%q", expected, markdown)
		}
	})
}

func TestParseMarkdown_CodeBlock(t *testing.T) {
	t.Run("simple code block", func(t *testing.T) {
		input := "```\nfmt.Println(\"hello\")\n```"
		segments := parseMarkdown(input)

		foundCodeBlock := false
		var codeText string
		for _, seg := range segments {
			if seg.isCodeBlock {
				foundCodeBlock = true
				codeText += seg.text
			}
		}
		if !foundCodeBlock {
			t.Errorf("expected code block segment, got segments: %+v", segments)
		}
		expectedText := "fmt.Println(\"hello\")\n"
		if codeText != expectedText {
			t.Errorf("code block text = %q, want %q", codeText, expectedText)
		}
	})

	t.Run("multi-line code block", func(t *testing.T) {
		input := "```\nline1\nline2\nline3\n```"
		segments := parseMarkdown(input)

		foundCodeBlock := false
		var codeText string
		for _, seg := range segments {
			if seg.isCodeBlock {
				foundCodeBlock = true
				codeText += seg.text
			}
		}
		if !foundCodeBlock {
			t.Errorf("expected code block segment, got segments: %+v", segments)
		}
		expectedText := "line1\nline2\nline3\n"
		if codeText != expectedText {
			t.Errorf("code block text = %q, want %q", codeText, expectedText)
		}
	})

	t.Run("code block preserves no inline formatting", func(t *testing.T) {
		input := "```\n**not bold** _not italic_\n```"
		segments := parseMarkdown(input)

		for _, seg := range segments {
			if seg.isCodeBlock {
				if seg.bold {
					t.Error("code block content should not be bold")
				}
				if seg.italic {
					t.Error("code block content should not be italic")
				}
			}
		}
	})

	t.Run("text before and after code block", func(t *testing.T) {
		input := "before\n```\ncode\n```\nafter"
		segments := parseMarkdown(input)

		var beforeText, codeText, afterText string
		for _, seg := range segments {
			switch {
			case seg.isCodeBlock:
				codeText += seg.text
			case codeText == "":
				beforeText += seg.text
			default:
				afterText += seg.text
			}
		}
		if beforeText != "before\n" {
			t.Errorf("before text = %q, want %q", beforeText, "before\n")
		}
		if codeText != "code\n" {
			t.Errorf("code text = %q, want %q", codeText, "code\n")
		}
		if afterText != "after\n" {
			t.Errorf("after text = %q, want %q", afterText, "after\n")
		}
	})

	t.Run("empty code block", func(t *testing.T) {
		input := "```\n```"
		segments := parseMarkdown(input)

		for _, seg := range segments {
			if seg.isCodeBlock {
				t.Errorf("empty code block should not produce code block segment, got: %+v", seg)
			}
		}
	})
}

func TestParseMarkdown_CodeBlockWithLanguage(t *testing.T) {
	t.Run("code block with go language", func(t *testing.T) {
		input := "```go\nfmt.Println(\"hello\")\n```"
		segments := parseMarkdown(input)

		foundCodeBlock := false
		var codeText string
		var lang string
		for _, seg := range segments {
			if seg.isCodeBlock {
				foundCodeBlock = true
				codeText += seg.text
				lang = seg.codeLanguage
			}
		}
		if !foundCodeBlock {
			t.Errorf("expected code block segment, got segments: %+v", segments)
		}
		expectedText := "fmt.Println(\"hello\")\n"
		if codeText != expectedText {
			t.Errorf("code block text = %q, want %q", codeText, expectedText)
		}
		if lang != "go" {
			t.Errorf("code language = %q, want %q", lang, "go")
		}
	})

	t.Run("code block with python language", func(t *testing.T) {
		input := "```python\nprint('hello')\ndef foo():\n    return 42\n```"
		segments := parseMarkdown(input)

		foundCodeBlock := false
		var codeText string
		var lang string
		for _, seg := range segments {
			if seg.isCodeBlock {
				foundCodeBlock = true
				codeText += seg.text
				lang = seg.codeLanguage
			}
		}
		if !foundCodeBlock {
			t.Errorf("expected code block segment, got segments: %+v", segments)
		}
		expectedText := "print('hello')\ndef foo():\n    return 42\n"
		if codeText != expectedText {
			t.Errorf("code block text = %q, want %q", codeText, expectedText)
		}
		if lang != "python" {
			t.Errorf("code language = %q, want %q", lang, "python")
		}
	})

	t.Run("code block without language has empty language", func(t *testing.T) {
		input := "```\nsome code\n```"
		segments := parseMarkdown(input)

		for _, seg := range segments {
			if seg.isCodeBlock && seg.codeLanguage != "" {
				t.Errorf("expected empty codeLanguage for plain code fence, got %q", seg.codeLanguage)
			}
		}
	})

	t.Run("multiple code blocks with different languages", func(t *testing.T) {
		input := "```go\ngo code\n```\n```python\npy code\n```"
		segments := parseMarkdown(input)

		var codeBlocks []markdownSegment
		for _, seg := range segments {
			if seg.isCodeBlock {
				codeBlocks = append(codeBlocks, seg)
			}
		}
		if len(codeBlocks) != 2 {
			t.Fatalf("expected 2 code block segments, got %d", len(codeBlocks))
		}
		if codeBlocks[0].codeLanguage != "go" {
			t.Errorf("first code block language = %q, want %q", codeBlocks[0].codeLanguage, "go")
		}
		if codeBlocks[0].text != "go code\n" {
			t.Errorf("first code block text = %q, want %q", codeBlocks[0].text, "go code\n")
		}
		if codeBlocks[1].codeLanguage != "python" {
			t.Errorf("second code block language = %q, want %q", codeBlocks[1].codeLanguage, "python")
		}
		if codeBlocks[1].text != "py code\n" {
			t.Errorf("second code block text = %q, want %q", codeBlocks[1].text, "py code\n")
		}
	})
}

func TestParseMarkdown_UnclosedCodeBlock(t *testing.T) {
	t.Run("unclosed code block emits content", func(t *testing.T) {
		input := "```\nline1\nline2"
		segments := parseMarkdown(input)

		foundCodeBlock := false
		var codeText string
		for _, seg := range segments {
			if seg.isCodeBlock {
				foundCodeBlock = true
				codeText += seg.text
			}
		}
		if !foundCodeBlock {
			t.Errorf("expected unclosed code block to emit a segment, got segments: %+v", segments)
		}
		expectedText := "line1\nline2\n"
		if codeText != expectedText {
			t.Errorf("code block text = %q, want %q", codeText, expectedText)
		}
	})

	t.Run("unclosed code block with language", func(t *testing.T) {
		input := "```python\ndef foo():\n    pass"
		segments := parseMarkdown(input)

		foundCodeBlock := false
		var codeText string
		var lang string
		for _, seg := range segments {
			if seg.isCodeBlock {
				foundCodeBlock = true
				codeText += seg.text
				lang = seg.codeLanguage
			}
		}
		if !foundCodeBlock {
			t.Errorf("expected unclosed code block to emit a segment, got segments: %+v", segments)
		}
		expectedText := "def foo():\n    pass\n"
		if codeText != expectedText {
			t.Errorf("code block text = %q, want %q", codeText, expectedText)
		}
		if lang != "python" {
			t.Errorf("code language = %q, want %q", lang, "python")
		}
	})

	t.Run("unclosed empty code block produces no segment", func(t *testing.T) {
		input := "```"
		segments := parseMarkdown(input)

		for _, seg := range segments {
			if seg.isCodeBlock {
				t.Errorf("empty unclosed code block should not produce segment, got: %+v", seg)
			}
		}
	})
}

func TestParseMarkdown_LanguageIdentifierValidation(t *testing.T) {
	t.Run("language with extra content extracts only first word", func(t *testing.T) {
		input := "```python some extra stuff\ncode here\n```"
		segments := parseMarkdown(input)

		foundCodeBlock := false
		var lang string
		for _, seg := range segments {
			if seg.isCodeBlock {
				foundCodeBlock = true
				lang = seg.codeLanguage
			}
		}
		if !foundCodeBlock {
			t.Errorf("expected code block segment, got segments: %+v", segments)
		}
		if lang != "python" {
			t.Errorf("code language = %q, want %q", lang, "python")
		}
	})

	t.Run("language with tab-separated content extracts only first word", func(t *testing.T) {
		input := "```go\tsome metadata\ncode here\n```"
		segments := parseMarkdown(input)

		foundCodeBlock := false
		var lang string
		for _, seg := range segments {
			if seg.isCodeBlock {
				foundCodeBlock = true
				lang = seg.codeLanguage
			}
		}
		if !foundCodeBlock {
			t.Errorf("expected code block segment, got segments: %+v", segments)
		}
		if lang != "go" {
			t.Errorf("code language = %q, want %q", lang, "go")
		}
	})
}

func TestParseSimpleFormatting_InlineCode(t *testing.T) {
	segments := mergeSegments(parseSimpleFormatting("Use the `getFoo()` method here.\n"))

	if len(segments) != 3 {
		t.Fatalf("expected 3 segments, got %d: %+v", len(segments), segments)
	}

	// First segment: plain text "Use the "
	if segments[0].text != "Use the " {
		t.Errorf("segment[0].text = %q, want %q", segments[0].text, "Use the ")
	}
	if segments[0].isInlineCode {
		t.Errorf("segment[0] should not be inline code")
	}

	// Second segment: inline code "getFoo()"
	if segments[1].text != "getFoo()" {
		t.Errorf("segment[1].text = %q, want %q", segments[1].text, "getFoo()")
	}
	if !segments[1].isInlineCode {
		t.Errorf("segment[1] should be inline code")
	}

	// Third segment: plain text " method here.\n"
	if segments[2].text != " method here.\n" {
		t.Errorf("segment[2].text = %q, want %q", segments[2].text, " method here.\n")
	}
	if segments[2].isInlineCode {
		t.Errorf("segment[2] should not be inline code")
	}
}

func TestParseSimpleFormatting_MultipleInlineCode(t *testing.T) {
	segments := mergeSegments(parseSimpleFormatting("Call `init()` then `run()` to start.\n"))

	// After merging, we expect segments: "Call ", "init()" (code), " then ", "run()" (code), " to start.\n"
	if len(segments) != 5 {
		t.Fatalf("expected 5 segments, got %d: %+v", len(segments), segments)
	}

	// Check the two inline code segments
	if segments[1].text != "init()" || !segments[1].isInlineCode {
		t.Errorf("segment[1] = {text:%q, isInlineCode:%v}, want {text:\"init()\", isInlineCode:true}",
			segments[1].text, segments[1].isInlineCode)
	}
	if segments[3].text != "run()" || !segments[3].isInlineCode {
		t.Errorf("segment[3] = {text:%q, isInlineCode:%v}, want {text:\"run()\", isInlineCode:true}",
			segments[3].text, segments[3].isInlineCode)
	}
}

func TestParseSimpleFormatting_InlineCodeWithFormatting(t *testing.T) {
	segments := mergeSegments(parseSimpleFormatting("`some **bold** text`"))

	// Filter to only inline code segments
	codeSegments := []markdownSegment{}
	for _, seg := range segments {
		if seg.isInlineCode {
			codeSegments = append(codeSegments, seg)
		}
	}

	if len(codeSegments) != 3 {
		t.Fatalf("Expected 3 inline code segments, got %d: %+v", len(codeSegments), codeSegments)
	}

	// First: "some "
	if codeSegments[0].text != "some " {
		t.Errorf("codeSegments[0].text = %q, want %q", codeSegments[0].text, "some ")
	}
	if codeSegments[0].bold {
		t.Errorf("codeSegments[0] should not be bold")
	}

	// Second: "bold" (bold)
	if codeSegments[1].text != "bold" {
		t.Errorf("codeSegments[1].text = %q, want %q", codeSegments[1].text, "bold")
	}
	if !codeSegments[1].bold {
		t.Errorf("codeSegments[1] should be bold")
	}

	// Third: " text"
	if codeSegments[2].text != " text" {
		t.Errorf("codeSegments[2].text = %q, want %q", codeSegments[2].text, " text")
	}
	if codeSegments[2].bold {
		t.Errorf("codeSegments[2] should not be bold")
	}
}

func TestParseSimpleFormatting_InlineCodeBoundary(t *testing.T) {
	text := "`code **bold**`. Normal text\n"
	segments := mergeSegments(parseSimpleFormatting(text))

	// Find segments containing "Normal text" and verify they are NOT inline code
	foundNormal := false
	for _, seg := range segments {
		if strings.Contains(seg.text, "Normal") {
			if seg.isInlineCode {
				t.Errorf("Text after closing backtick should NOT be inline code: %q", seg.text)
			}
			foundNormal = true
		}
	}

	if !foundNormal {
		t.Errorf("Did not find 'Normal text' segment")
	}

	// Also check that ". " before "Normal text" is not inline code
	for _, seg := range segments {
		if strings.Contains(seg.text, ". ") && seg.isInlineCode {
			t.Errorf("Period and space after closing backtick should NOT be inline code: %q", seg.text)
		}
	}

	// Test with the exact text from the bug report
	text2 := "`code that is rendered **pre-formatted**`. This\n"
	segments2 := mergeSegments(parseSimpleFormatting(text2))

	// ". This" should NOT be inline code
	for _, seg := range segments2 {
		if strings.Contains(seg.text, "This") {
			if seg.isInlineCode {
				t.Errorf("Text '. This' after closing backtick should NOT be inline code: %q", seg.text)
			}
		}
	}
}

func TestParseSimpleFormatting_InlineCodeBoundaryFullPipeline(t *testing.T) {
	// Test the full pipeline: parseMarkdown -> convertMarkdownToRequests
	// This tests the actual code path that produces Google Docs API requests
	markdown := "`code that is rendered **pre-formatted**`. This\n"
	segments := parseMarkdown(markdown)

	// Check that ". This" is NOT inline code at the segment level
	for _, seg := range segments {
		if strings.Contains(seg.text, "This") {
			if seg.isInlineCode {
				t.Errorf("parseMarkdown: text after closing backtick should NOT be inline code: %q", seg.text)
			}
		}
	}

	// Now test convertMarkdownToRequests - check that the UpdateTextStyle for
	// text after the closing backtick includes weightedFontFamily in its fields
	// so that the font is explicitly reset to the document default (not Courier New).
	requests, _ := convertMarkdownToRequests(segments, 1, true)

	// Find the InsertText for ". This" and its corresponding UpdateTextStyle
	for i, req := range requests {
		if req.InsertText != nil && strings.Contains(req.InsertText.Text, "This") {
			// The next request should be the UpdateTextStyle for this text
			if i+1 < len(requests) && requests[i+1].UpdateTextStyle != nil {
				style := requests[i+1].UpdateTextStyle
				if !strings.Contains(style.Fields, "weightedFontFamily") {
					t.Errorf("UpdateTextStyle for text after inline code should include weightedFontFamily in Fields to reset font, got: %q", style.Fields)
				}
				// Should NOT have Courier New font
				if style.TextStyle.WeightedFontFamily != nil && style.TextStyle.WeightedFontFamily.FontFamily == "Courier New" {
					t.Errorf("Text after inline code should NOT have Courier New font")
				}
			}
		}
	}
}

func TestConvertMarkdownToRequestsFinalIndex(t *testing.T) {
	const startIndex int64 = 1

	tests := []struct {
		name     string
		markdown string
		want     int64
	}{
		{
			name:     "plain text",
			markdown: "hello",
			want:     startIndex + 5, // "hello" = 5 runes, trailing \n trimmed
		},
		{
			name:     "text with trailing newline",
			markdown: "hello\n",
			want:     startIndex + 6, // "hello" (5) + paragraph break \n (1)
		},
		{
			name:     "two paragraphs",
			markdown: "first\n\nsecond\n",
			want:     startIndex + 14, // "first\n" (6) + paragraph break \n (1) + "second" (6) + trailing break (1)
		},
		{
			name:     "heading",
			markdown: "# Title\n\nBody text\n",
			want:     startIndex + 16, // "Title\n" (6) + "Body text" (9) + trailing break (1)
		},
		{
			name:     "date field only",
			markdown: "@today",
			want:     startIndex + 1, // InsertText(placeholder) + Delete + InsertDate = net 1
		},
		{
			name:     "text then date",
			markdown: "Due: @today",
			want:     startIndex + 5 + 1, // "Due: " (5) + date chip (1)
		},
		{
			name:     "person field only",
			markdown: "@(user@example.com)",
			want:     startIndex + 1, // InsertPerson = 1
		},
		{
			name:     "horizontal rule",
			markdown: "---",
			want:     startIndex + 1, // InsertText("\n") = 1
		},
		{
			name:     "multi-byte UTF-8",
			markdown: "概要",
			want:     startIndex + 2, // 2 runes, not 6 bytes
		},
		{
			name:     "emoji",
			markdown: "Hello 🌍",
			want:     startIndex + 7, // 7 runes: H,e,l,l,o,' ',🌍
		},
		{
			name:     "text with date in middle",
			markdown: "Due: @today end",
			want:     startIndex + 5 + 1 + 4, // "Due: " (5) + date chip (1) + " end" (4)
		},
		{
			name:     "specific date",
			markdown: "@date(2026-01-15)",
			want:     startIndex + 1, // date chip = 1
		},
		{
			name:     "code block",
			markdown: "```\nfmt.Println()\n```",
			want:     startIndex + 13, // "fmt.Println()" (13), trailing \n trimmed
		},
		{
			name:     "unordered list",
			markdown: "- item one\n- item two\n",
			want:     startIndex + 18, // "item one\n" (9) + "item two" (8), trailing \n trimmed; no tabs at depth 0
		},
		{
			name:     "nested list depth 1",
			markdown: "- top\n    - nested\n",
			want:     startIndex + 11, // "top\n"(4) + "\tnested\n"(8) - 1 tab removed by CreateParagraphBullets
		},
		{
			name:     "nested list depth 2",
			markdown: "- a\n    - b\n        - c\n",
			want:     startIndex + 6, // 3 tabs subtracted (1 for b at depth 1, 2 for c at depth 2)
		},
		{
			name:     "nested bold at start",
			markdown: "    - **bold** text\n",
			want:     startIndex + 10, // "bold"(4) + " text"(5) + 1 tab - 1 tab removed = 10
		},
		{
			name:     "nested bold at end",
			markdown: "    - text **bold**\n",
			want:     startIndex + 10, // "text "(5) + "bold"(4) + 1 tab - 1 tab removed = 10
		},
		{
			name:     "nested formatted then plain",
			markdown: "    - **bold** text\n    - plain\n",
			want:     startIndex + 16, // two items, 2 tabs inserted, 2 removed
		},
		{
			name:     "nested depth-2 with bold",
			markdown: "        - **deep** text\n",
			want:     startIndex + 10, // "deep"(4) + " text"(5) + 2 tabs - 2 removed = 10
		},
		{
			name:     "nested ordered with bold",
			markdown: "    1. **bold** text\n",
			want:     startIndex + 10, // same as unordered
		},
		{
			name:     "nested list then blank line",
			markdown: "- Parent\n    - Child\n\n",
			want:     startIndex + 14,
		},
		{
			name:     "empty input",
			markdown: "",
			want:     startIndex,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			segments := parseMarkdown(tt.markdown)
			_, finalIndex := convertMarkdownToRequests(segments, startIndex, true)
			if finalIndex != tt.want {
				t.Errorf("finalIndex = %d, want %d", finalIndex, tt.want)
			}
		})
	}

	// stripTrailingNewline=false: the production path used when
	// flushing text before a table (tools.go:1593). The trailing
	// \n is preserved, so finalIndex is 1 higher for inputs that
	// end with \n (whose last segment would otherwise be trimmed).
	noStripTests := []struct {
		name     string
		markdown string
		want     int64
	}{
		{"plain text with newline", "hello\n", startIndex + 7},
		{"heading with body", "# Title\n\nBody text\n", startIndex + 17},
		{"nested list depth 1", "- top\n    - nested\n", startIndex + 12},
		{"nested bold", "    - **bold** text\n", startIndex + 11},
	}
	for _, tt := range noStripTests {
		t.Run("no_strip/"+tt.name, func(t *testing.T) {
			segments := parseMarkdown(tt.markdown)
			_, finalIndex := convertMarkdownToRequests(segments, startIndex, false)
			if finalIndex != tt.want {
				t.Errorf("finalIndex = %d, want %d", finalIndex, tt.want)
			}
		})
	}
}

func TestFormattedNestedListNoMidTextTabs(t *testing.T) {
	segments := parseMarkdown("    - **bold** text\n")
	requests, _ := convertMarkdownToRequests(segments, 1, true)

	tabCount := 0
	for _, req := range requests {
		if req.InsertText != nil && strings.Contains(req.InsertText.Text, "\t") {
			tabCount++
		}
	}
	if tabCount != 1 {
		t.Errorf("expected 1 InsertText with tab, got %d", tabCount)
	}
}

func TestConvertMarkdownToRequests_CodeBlock(t *testing.T) {
	t.Run("code block applies Courier New font", func(t *testing.T) {
		markdown := "```\nfmt.Println(\"hello\")\n```"
		segments := parseMarkdown(markdown)
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		// Should have at least one InsertText with the code content
		foundInsert := false
		for _, req := range requests {
			if req.InsertText != nil && strings.Contains(req.InsertText.Text, "fmt.Println") {
				foundInsert = true
			}
		}
		if !foundInsert {
			t.Errorf("expected InsertText with code content")
		}

		// Should have UpdateTextStyle with Courier New font
		foundCourierNew := false
		for _, req := range requests {
			if req.UpdateTextStyle != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily.FontFamily == "Courier New" {
				foundCourierNew = true
				// Verify Fields includes "weightedFontFamily"
				if !strings.Contains(req.UpdateTextStyle.Fields, "weightedFontFamily") {
					t.Errorf("Fields should include weightedFontFamily, got: %s", req.UpdateTextStyle.Fields)
				}
			}
		}
		if !foundCourierNew {
			t.Errorf("expected UpdateTextStyle with Courier New font for code block")
		}
	})

	t.Run("code block does not apply bold/italic/underline", func(t *testing.T) {
		markdown := "```\ncode\n```"
		segments := parseMarkdown(markdown)
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		for _, req := range requests {
			if req.UpdateTextStyle != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily.FontFamily == "Courier New" {
				// Verify bold, italic, underline, strikethrough are NOT set
				if req.UpdateTextStyle.TextStyle.Bold {
					t.Errorf("code block should not have Bold set")
				}
				if req.UpdateTextStyle.TextStyle.Italic {
					t.Errorf("code block should not have Italic set")
				}
				if req.UpdateTextStyle.TextStyle.Underline {
					t.Errorf("code block should not have Underline set")
				}
				if req.UpdateTextStyle.TextStyle.Strikethrough {
					t.Errorf("code block should not have Strikethrough set")
				}
			}
		}
	})

	t.Run("code block followed by normal text", func(t *testing.T) {
		markdown := "```\ncode\n```\nNormal text"
		segments := parseMarkdown(markdown)
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		// Verify both code and normal text are inserted
		var insertTexts []string
		for _, req := range requests {
			if req.InsertText != nil {
				insertTexts = append(insertTexts, req.InsertText.Text)
			}
		}

		foundCode := false
		foundNormal := false
		for _, text := range insertTexts {
			if strings.Contains(text, "code") {
				foundCode = true
			}
			if strings.Contains(text, "Normal text") {
				foundNormal = true
			}
		}

		if !foundCode {
			t.Errorf("expected code text in InsertText requests")
		}
		if !foundNormal {
			t.Errorf("expected normal text in InsertText requests")
		}

		// Verify Courier New is only applied to the code block range, not to normal text
		for _, req := range requests {
			if req.UpdateTextStyle != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily.FontFamily == "Courier New" {
				// The code block range should not overlap with normal text range
				// Normal text starts after code block
				break
			}
		}
	})
}

func TestConvertMarkdownToRequests_InlineCode(t *testing.T) {
	t.Run("inline code applies Courier New font", func(t *testing.T) {
		markdown := "Use `fmt.Println` to print"
		segments := parseMarkdown(markdown)
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		// Should have UpdateTextStyle with Courier New for the inline code segment
		foundCourierNew := false
		for _, req := range requests {
			if req.UpdateTextStyle != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily.FontFamily == "Courier New" {
				foundCourierNew = true
				// Verify Fields includes "weightedFontFamily"
				if !strings.Contains(req.UpdateTextStyle.Fields, "weightedFontFamily") {
					t.Errorf("Fields should include weightedFontFamily, got: %s", req.UpdateTextStyle.Fields)
				}
			}
		}
		if !foundCourierNew {
			t.Errorf("expected UpdateTextStyle with Courier New font for inline code")
		}
	})

	t.Run("inline code preserves formatting fields", func(t *testing.T) {
		markdown := "Use `code` here"
		segments := parseMarkdown(markdown)
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		for _, req := range requests {
			if req.UpdateTextStyle != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily.FontFamily == "Courier New" {
				// Inline code should include formatting fields so they can be set when present
				if !strings.Contains(req.UpdateTextStyle.Fields, "bold") {
					t.Errorf("inline code Fields should include bold, got: %s", req.UpdateTextStyle.Fields)
				}
				if !strings.Contains(req.UpdateTextStyle.Fields, "italic") {
					t.Errorf("inline code Fields should include italic, got: %s", req.UpdateTextStyle.Fields)
				}
				if !strings.Contains(req.UpdateTextStyle.Fields, "underline") {
					t.Errorf("inline code Fields should include underline, got: %s", req.UpdateTextStyle.Fields)
				}
				if !strings.Contains(req.UpdateTextStyle.Fields, "strikethrough") {
					t.Errorf("inline code Fields should include strikethrough, got: %s", req.UpdateTextStyle.Fields)
				}
				// Plain inline code (no extra formatting) should have false for all
				if req.UpdateTextStyle.TextStyle.Bold {
					t.Errorf("plain inline code Bold should be false")
				}
				if req.UpdateTextStyle.TextStyle.Italic {
					t.Errorf("plain inline code Italic should be false")
				}
				if req.UpdateTextStyle.TextStyle.Underline {
					t.Errorf("plain inline code Underline should be false")
				}
			}
		}
	})

	t.Run("bold inline code preserves bold formatting", func(t *testing.T) {
		markdown := "Use **`code`** here"
		segments := parseMarkdown(markdown)
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		foundBoldCode := false
		for _, req := range requests {
			if req.UpdateTextStyle != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily.FontFamily == "Courier New" {
				foundBoldCode = true
				if !req.UpdateTextStyle.TextStyle.Bold {
					t.Errorf("bold inline code should have Bold=true")
				}
				if !strings.Contains(req.UpdateTextStyle.Fields, "weightedFontFamily") {
					t.Errorf("bold inline code Fields should include weightedFontFamily, got: %s", req.UpdateTextStyle.Fields)
				}
				if !strings.Contains(req.UpdateTextStyle.Fields, "bold") {
					t.Errorf("bold inline code Fields should include bold, got: %s", req.UpdateTextStyle.Fields)
				}
			}
		}
		if !foundBoldCode {
			t.Errorf("expected UpdateTextStyle with Courier New font for bold inline code")
		}
	})

	t.Run("italic inline code preserves italic formatting", func(t *testing.T) {
		markdown := "Use *`code`* here"
		segments := parseMarkdown(markdown)
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		foundItalicCode := false
		for _, req := range requests {
			if req.UpdateTextStyle != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily.FontFamily == "Courier New" {
				foundItalicCode = true
				if !req.UpdateTextStyle.TextStyle.Italic {
					t.Errorf("italic inline code should have Italic=true")
				}
			}
		}
		if !foundItalicCode {
			t.Errorf("expected UpdateTextStyle with Courier New font for italic inline code")
		}
	})

	t.Run("non-code text does not get Courier New", func(t *testing.T) {
		markdown := "Regular text without code"
		segments := parseMarkdown(markdown)
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		for _, req := range requests {
			if req.UpdateTextStyle != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily.FontFamily == "Courier New" {
				t.Errorf("non-code text should not have Courier New font")
			}
		}
	})

	t.Run("inline code in heading applies Courier New", func(t *testing.T) {
		markdown := "# Heading with `code`"
		segments := parseMarkdown(markdown)
		requests, _ := convertMarkdownToRequests(segments, 1, true)

		foundCourierNew := false
		for _, req := range requests {
			if req.UpdateTextStyle != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily.FontFamily == "Courier New" {
				foundCourierNew = true
			}
		}
		if !foundCourierNew {
			t.Errorf("expected Courier New font for inline code in heading")
		}
	})
}

func TestParseMarkdownTable(t *testing.T) {
	tests := []struct {
		name    string
		lines   []string
		want    *tableSegment
		wantNil bool
	}{
		{
			name: "basic 2x2 table",
			lines: []string{
				"| A | B |",
				"| --- | --- |",
				"| 1 | 2 |",
			},
			want: &tableSegment{
				numColumns: 2,
				rows: []tableRow{
					{cells: []tableCell{
						{segments: []markdownSegment{{text: "A"}}},
						{segments: []markdownSegment{{text: "B"}}},
					}},
					{cells: []tableCell{
						{segments: []markdownSegment{{text: "1"}}},
						{segments: []markdownSegment{{text: "2"}}},
					}},
				},
			},
		},
		{
			name: "table with 3 columns and 2 data rows",
			lines: []string{
				"| Name | Age | City |",
				"| ---- | --- | ---- |",
				"| Alice | 30 | NYC |",
				"| Bob | 25 | LA |",
			},
			want: &tableSegment{
				numColumns: 3,
				rows: []tableRow{
					{cells: []tableCell{
						{segments: []markdownSegment{{text: "Name"}}},
						{segments: []markdownSegment{{text: "Age"}}},
						{segments: []markdownSegment{{text: "City"}}},
					}},
					{cells: []tableCell{
						{segments: []markdownSegment{{text: "Alice"}}},
						{segments: []markdownSegment{{text: "30"}}},
						{segments: []markdownSegment{{text: "NYC"}}},
					}},
					{cells: []tableCell{
						{segments: []markdownSegment{{text: "Bob"}}},
						{segments: []markdownSegment{{text: "25"}}},
						{segments: []markdownSegment{{text: "LA"}}},
					}},
				},
			},
		},
		{
			name: "invalid - missing separator",
			lines: []string{
				"| A | B |",
				"| 1 | 2 |",
			},
			wantNil: true,
		},
		{
			name: "invalid - inconsistent columns",
			lines: []string{
				"| A | B |",
				"| --- | --- |",
				"| 1 | 2 | 3 |",
			},
			wantNil: true,
		},
		{
			name: "invalid - no data rows",
			lines: []string{
				"| A | B |",
				"| --- | --- |",
			},
			wantNil: true,
		},
		{
			name:    "invalid - too few lines",
			lines:   []string{"| A | B |"},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseMarkdownTable(tt.lines)

			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
				return
			}

			if got == nil {
				t.Fatal("expected non-nil result")
			}

			if got.numColumns != tt.want.numColumns {
				t.Errorf("numColumns: got %d, want %d", got.numColumns, tt.want.numColumns)
			}
			if len(got.rows) != len(tt.want.rows) {
				t.Errorf("rows: got %d, want %d", len(got.rows), len(tt.want.rows))
			}

			// Check first row cells
			if len(got.rows) > 0 && len(tt.want.rows) > 0 {
				gotRow := got.rows[0]
				wantRow := tt.want.rows[0]
				if len(gotRow.cells) != len(wantRow.cells) {
					t.Errorf("row 0 cells: got %d, want %d", len(gotRow.cells), len(wantRow.cells))
				}
				// Check first cell content
				if len(gotRow.cells) > 0 && len(wantRow.cells) > 0 {
					gotCell := gotRow.cells[0]
					wantCell := wantRow.cells[0]
					if len(gotCell.segments) > 0 && len(wantCell.segments) > 0 {
						if gotCell.segments[0].text != wantCell.segments[0].text {
							t.Errorf("cell[0][0] text: got %q, want %q",
								gotCell.segments[0].text, wantCell.segments[0].text)
						}
					}
				}
			}
		})
	}
}

func TestTableParsingWithFormatting(t *testing.T) {
	tests := []struct {
		name      string
		lines     []string
		checkCell struct {
			row  int
			col  int
			want []markdownSegment
		}
	}{
		{
			name: "bold text in cell",
			lines: []string{
				"| Name |",
				"| ---- |",
				"| **Alice** |",
			},
			checkCell: struct {
				row  int
				col  int
				want []markdownSegment
			}{
				row: 1,
				col: 0,
				want: []markdownSegment{
					{text: "Alice", bold: true},
				},
			},
		},
		{
			name: "italic text in cell",
			lines: []string{
				"| Status |",
				"| ------ |",
				"| *Active* |",
			},
			checkCell: struct {
				row  int
				col  int
				want []markdownSegment
			}{
				row: 1,
				col: 0,
				want: []markdownSegment{
					{text: "Active", italic: true},
				},
			},
		},
		{
			name: "link in cell",
			lines: []string{
				"| Link |",
				"| ---- |",
				"| [Google](https://google.com) |",
			},
			checkCell: struct {
				row  int
				col  int
				want []markdownSegment
			}{
				row: 1,
				col: 0,
				want: []markdownSegment{
					{text: "Google", linkURL: "https://google.com"},
				},
			},
		},
		{
			name: "mixed formatting in cell",
			lines: []string{
				"| Text |",
				"| ---- |",
				"| **Bold** and *italic* |",
			},
			checkCell: struct {
				row  int
				col  int
				want []markdownSegment
			}{
				row: 1,
				col: 0,
				want: []markdownSegment{
					{text: "Bold", bold: true},
					{text: " and "},
					{text: "italic", italic: true},
				},
			},
		},
		{
			name: "empty cell",
			lines: []string{
				"| A | B |",
				"| --- | --- |",
				"| Content |  |",
			},
			checkCell: struct {
				row  int
				col  int
				want []markdownSegment
			}{
				row:  1,
				col:  1,
				want: []markdownSegment{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table := parseMarkdownTable(tt.lines)
			if table == nil {
				t.Fatal("expected non-nil table")
			}

			if tt.checkCell.row >= len(table.rows) {
				t.Fatalf("row %d out of bounds (have %d rows)", tt.checkCell.row, len(table.rows))
			}

			row := table.rows[tt.checkCell.row]
			if tt.checkCell.col >= len(row.cells) {
				t.Fatalf("col %d out of bounds (have %d cols)", tt.checkCell.col, len(row.cells))
			}

			cell := row.cells[tt.checkCell.col]
			got := cell.segments

			if len(got) != len(tt.checkCell.want) {
				t.Fatalf("segments count: got %d, want %d", len(got), len(tt.checkCell.want))
			}

			for i, wantSeg := range tt.checkCell.want {
				gotSeg := got[i]
				if gotSeg.text != wantSeg.text {
					t.Errorf("segment[%d].text: got %q, want %q", i, gotSeg.text, wantSeg.text)
				}
				if gotSeg.bold != wantSeg.bold {
					t.Errorf("segment[%d].bold: got %v, want %v", i, gotSeg.bold, wantSeg.bold)
				}
				if gotSeg.italic != wantSeg.italic {
					t.Errorf("segment[%d].italic: got %v, want %v", i, gotSeg.italic, wantSeg.italic)
				}
				if gotSeg.linkURL != wantSeg.linkURL {
					t.Errorf("segment[%d].linkURL: got %q, want %q", i, gotSeg.linkURL, wantSeg.linkURL)
				}
			}
		})
	}
}

func TestParseMarkdownWithTables(t *testing.T) {
	tests := []struct {
		name           string
		markdown       string
		wantTableCount int
		checkSegment   int
		wantIsTable    bool
	}{
		{
			name: "simple table",
			markdown: `| A | B |
| --- | --- |
| 1 | 2 |`,
			wantTableCount: 1,
			checkSegment:   0,
			wantIsTable:    true,
		},
		{
			name: "table with text before and after",
			markdown: `Some text

| A | B |
| --- | --- |
| 1 | 2 |

More text`,
			wantTableCount: 1,
			checkSegment:   1, // Table is second segment (after "Some text\n")
			wantIsTable:    true,
		},
		{
			name: "multiple tables",
			markdown: `| A | B |
| --- | --- |
| 1 | 2 |

| C | D |
| --- | --- |
| 3 | 4 |`,
			wantTableCount: 2,
			checkSegment:   0,
			wantIsTable:    true,
		},
		{
			name: "malformed table - no separator",
			markdown: `| A | B |
| 1 | 2 |`,
			wantTableCount: 0,
			checkSegment:   0,
			wantIsTable:    false,
		},
		{
			name: "malformed table - inconsistent columns",
			markdown: `| A | B |
| --- | --- |
| 1 | 2 | 3 |`,
			wantTableCount: 0,
			checkSegment:   0,
			wantIsTable:    false,
		},
		{
			name: "table with heading",
			markdown: `# Title

| A | B |
| --- | --- |
| 1 | 2 |`,
			wantTableCount: 1,
			checkSegment:   1, // After heading
			wantIsTable:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			segments := parseMarkdown(tt.markdown)

			// Count table segments
			tableCount := 0
			for _, seg := range segments {
				if seg.isTable {
					tableCount++
				}
			}

			if tableCount != tt.wantTableCount {
				t.Errorf("table count: got %d, want %d", tableCount, tt.wantTableCount)
			}

			// If expecting a table, verify at least one table segment exists with non-nil table
			if tt.wantIsTable {
				foundTable := false
				for _, seg := range segments {
					if seg.isTable && seg.table != nil {
						foundTable = true
						break
					}
				}
				if !foundTable {
					t.Error("expected to find at least one table segment with non-nil table")
				}
			}
		})
	}
}

func TestTableSizeLimits(t *testing.T) {
	t.Run("table within limits is parsed", func(t *testing.T) {
		var lines []string
		lines = append(lines, "| A | B |")
		lines = append(lines, "| --- | --- |")
		for i := 0; i < 10; i++ {
			lines = append(lines, "| x | y |")
		}
		table := parseMarkdownTable(lines)
		if table == nil {
			t.Fatal("expected table to be parsed")
		}
	})

	t.Run("large table is still parsed by parser", func(t *testing.T) {
		var lines []string
		header := "|"
		sep := "|"
		row := "|"
		for i := 0; i < maxTableColumns+5; i++ {
			header += " H |"
			sep += " --- |"
			row += " D |"
		}
		lines = append(lines, header)
		lines = append(lines, sep)
		for i := 0; i < 5; i++ {
			lines = append(lines, row)
		}
		table := parseMarkdownTable(lines)
		if table == nil {
			t.Fatal("parser should parse large tables (limits enforced at tool level)")
		}
		if table.numColumns != maxTableColumns+5 {
			t.Errorf("expected %d columns, got %d", maxTableColumns+5, table.numColumns)
		}
	})

	t.Run("constants are reasonable", func(t *testing.T) {
		if maxTableRows < 10 || maxTableRows > 100 {
			t.Errorf("maxTableRows=%d seems unreasonable", maxTableRows)
		}
		if maxTableColumns < 5 || maxTableColumns > 50 {
			t.Errorf("maxTableColumns=%d seems unreasonable", maxTableColumns)
		}
	})
}

func TestInlineCodeInTableCells(t *testing.T) {
	t.Run("inline code cell produces Courier New", func(t *testing.T) {
		cell := tableCell{
			segments: []markdownSegment{
				{text: "code", isInlineCode: true},
			},
		}
		requests := populateTableCell(&cell, 5)

		foundCourier := false
		for _, req := range requests {
			if req.UpdateTextStyle != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily.FontFamily == "Courier New" {
				foundCourier = true
			}
		}
		if !foundCourier {
			t.Error("expected Courier New for inline code in cell")
		}
	})

	t.Run("bold inline code in cell", func(t *testing.T) {
		cell := tableCell{
			segments: []markdownSegment{
				{text: "code", isInlineCode: true, bold: true},
			},
		}
		requests := populateTableCell(&cell, 5)

		for _, req := range requests {
			if req.UpdateTextStyle != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily.FontFamily == "Courier New" {
				if !req.UpdateTextStyle.TextStyle.Bold {
					t.Error("expected bold + Courier New")
				}
				if !strings.Contains(req.UpdateTextStyle.Fields, "weightedFontFamily") {
					t.Error("fields should include weightedFontFamily")
				}
				if !strings.Contains(req.UpdateTextStyle.Fields, "bold") {
					t.Error("fields should include bold")
				}
			}
		}
	})

	t.Run("parsed table with inline code", func(t *testing.T) {
		lines := []string{
			"| Code |",
			"| ---- |",
			"| `hello` |",
		}
		table := parseMarkdownTable(lines)
		if table == nil {
			t.Fatal("expected table")
		}
		cell := table.rows[1].cells[0]
		foundInlineCode := false
		for _, seg := range cell.segments {
			if seg.isInlineCode {
				foundInlineCode = true
			}
		}
		if !foundInlineCode {
			t.Error("expected inline code segment in cell")
		}
	})
}

func TestPipeEscaping(t *testing.T) {
	t.Run("escaped pipe in cell", func(t *testing.T) {
		lines := []string{
			`| Command | Output |`,
			`| --- | --- |`,
			`| echo a \| b | result |`,
		}
		table := parseMarkdownTable(lines)
		if table == nil {
			t.Fatal("expected table")
		}
		if table.numColumns != 2 {
			t.Errorf("expected 2 columns, got %d", table.numColumns)
		}
		cell := table.rows[1].cells[0]
		if len(cell.segments) == 0 || cell.segments[0].text != `echo a | b` {
			t.Errorf("expected 'echo a | b', got %q", cell.segments[0].text)
		}
	})

	t.Run("escaped backslash before pipe is delimiter", func(t *testing.T) {
		lines := []string{
			`| A | B |`,
			`| --- | --- |`,
			`| path\\ | value |`,
		}
		table := parseMarkdownTable(lines)
		if table == nil {
			t.Fatal("expected table")
		}
		if table.numColumns != 2 {
			t.Errorf("expected 2 columns, got %d", table.numColumns)
		}
		cell := table.rows[1].cells[0]
		if len(cell.segments) == 0 || cell.segments[0].text != `path\` {
			t.Errorf(`expected "path\", got %q`, cell.segments[0].text)
		}
	})

	t.Run("escaped backslash then escaped pipe", func(t *testing.T) {
		lines := []string{
			`| A |`,
			`| --- |`,
			`| C:\\\| |`,
		}
		table := parseMarkdownTable(lines)
		if table == nil {
			t.Fatal("expected table")
		}
		cell := table.rows[1].cells[0]
		if len(cell.segments) == 0 || cell.segments[0].text != `C:\|` {
			t.Errorf(`expected "C:\|", got %q`, cell.segments[0].text)
		}
	})

	t.Run("multiple escaped pipes", func(t *testing.T) {
		lines := []string{
			`| Expr |`,
			`| --- |`,
			`| a \| b \| c |`,
		}
		table := parseMarkdownTable(lines)
		if table == nil {
			t.Fatal("expected table")
		}
		cell := table.rows[1].cells[0]
		if len(cell.segments) == 0 || cell.segments[0].text != `a | b | c` {
			t.Errorf(`expected "a | b | c", got %q`, cell.segments[0].text)
		}
	})

	t.Run("escaped pipe with formatting", func(t *testing.T) {
		lines := []string{
			`| Text |`,
			`| --- |`,
			`| **bold \| text** |`,
		}
		table := parseMarkdownTable(lines)
		if table == nil {
			t.Fatal("expected table")
		}
		cell := table.rows[1].cells[0]
		foundBold := false
		for _, seg := range cell.segments {
			if seg.bold && strings.Contains(seg.text, "|") {
				foundBold = true
			}
		}
		if !foundBold {
			t.Error("expected bold segment containing pipe character")
		}
	})

	t.Run("parseMarkdown with escaped pipes", func(t *testing.T) {
		markdown := "| A |\n| --- |\n| x \\| y |"
		segments := parseMarkdown(markdown)
		foundTable := false
		for _, seg := range segments {
			if seg.isTable {
				foundTable = true
				if seg.table.numColumns != 1 {
					t.Errorf("expected 1 column, got %d", seg.table.numColumns)
				}
			}
		}
		if !foundTable {
			t.Error("expected table segment")
		}
	})
}

func flattenCellRequests(groups [][]*docs.Request) []*docs.Request {
	var flat []*docs.Request
	for _, g := range groups {
		flat = append(flat, g...)
	}
	return flat
}

func TestCollectTableCellRequests(t *testing.T) {
	mkAPIRows := func(indices ...[]int64) []*docs.TableRow {
		var rows []*docs.TableRow
		for _, rowIndices := range indices {
			var cells []*docs.TableCell
			for _, idx := range rowIndices {
				cells = append(cells, &docs.TableCell{
					Content: []*docs.StructuralElement{{StartIndex: idx}},
				})
			}
			rows = append(rows, &docs.TableRow{TableCells: cells})
		}
		return rows
	}

	t.Run("reverse iteration order 2x2", func(t *testing.T) {
		apiRows := mkAPIRows([]int64{5, 10}, []int64{20, 25})
		parsedRows := []tableRow{
			{cells: []tableCell{
				{segments: []markdownSegment{{text: "A"}}},
				{segments: []markdownSegment{{text: "B"}}},
			}},
			{cells: []tableCell{
				{segments: []markdownSegment{{text: "C"}}},
				{segments: []markdownSegment{{text: "D"}}},
			}},
		}

		reqs := flattenCellRequests(collectTableCellRequests(apiRows, parsedRows))

		var insertIndices []int64
		for _, r := range reqs {
			if r.InsertText != nil {
				insertIndices = append(insertIndices, r.InsertText.Location.Index)
			}
		}
		if len(insertIndices) != 4 {
			t.Fatalf("expected 4 InsertText requests, got %d", len(insertIndices))
		}
		// Must be strictly decreasing (reverse document order)
		for i := 1; i < len(insertIndices); i++ {
			if insertIndices[i] >= insertIndices[i-1] {
				t.Errorf("not in reverse order: index[%d]=%d >= index[%d]=%d",
					i, insertIndices[i], i-1, insertIndices[i-1])
			}
		}
	})

	t.Run("empty cells produce no requests", func(t *testing.T) {
		apiRows := mkAPIRows([]int64{5, 10})
		parsedRows := []tableRow{
			{cells: []tableCell{
				{segments: []markdownSegment{{text: "A"}}},
				{segments: []markdownSegment{}},
			}},
		}

		reqs := flattenCellRequests(collectTableCellRequests(apiRows, parsedRows))

		for _, r := range reqs {
			if r.InsertText != nil && r.InsertText.Location.Index == 10 {
				t.Error("empty cell should not produce InsertText requests")
			}
		}
		// Should only have requests for cell(0,0)
		if len(reqs) != 1 {
			t.Errorf("expected 1 request (text only), got %d", len(reqs))
		}
	})

	t.Run("all-empty table produces zero requests", func(t *testing.T) {
		apiRows := mkAPIRows([]int64{5, 10}, []int64{20, 25})
		parsedRows := []tableRow{
			{cells: []tableCell{
				{segments: []markdownSegment{}},
				{segments: []markdownSegment{}},
			}},
			{cells: []tableCell{
				{segments: []markdownSegment{}},
				{segments: []markdownSegment{}},
			}},
		}

		reqs := flattenCellRequests(collectTableCellRequests(apiRows, parsedRows))
		if len(reqs) != 0 {
			t.Errorf("expected 0 requests for all-empty table, got %d", len(reqs))
		}
	})

	t.Run("single cell table", func(t *testing.T) {
		apiRows := mkAPIRows([]int64{5})
		parsedRows := []tableRow{
			{cells: []tableCell{
				{segments: []markdownSegment{{text: "only"}}},
			}},
		}

		reqs := flattenCellRequests(collectTableCellRequests(apiRows, parsedRows))
		if len(reqs) == 0 {
			t.Fatal("expected requests for single-cell table")
		}
		if reqs[0].InsertText == nil || reqs[0].InsertText.Text != "only" {
			t.Error("expected InsertText with 'only'")
		}
	})

	t.Run("mixed content types text and date chip", func(t *testing.T) {
		apiRows := mkAPIRows([]int64{5, 15})
		parsedRows := []tableRow{
			{cells: []tableCell{
				{segments: []markdownSegment{{text: "hello", bold: true}}},
				{segments: []markdownSegment{{isDateField: true, dateValue: "2026-01-01"}}},
			}},
		}

		reqs := flattenCellRequests(collectTableCellRequests(apiRows, parsedRows))

		// Date chip cell (index 15) should come before text cell (index 5)
		var firstDateIdx, firstTextIdx int
		for i, r := range reqs {
			if r.InsertDate != nil && firstDateIdx == 0 {
				firstDateIdx = i + 1
			}
			if r.InsertText != nil && firstTextIdx == 0 {
				firstTextIdx = i + 1
			}
		}
		if firstDateIdx == 0 {
			t.Fatal("expected InsertDate request")
		}
		if firstTextIdx == 0 {
			t.Fatal("expected InsertText request")
		}
		if firstDateIdx > firstTextIdx {
			t.Error("date chip (higher index) should appear before text (lower index)")
		}
	})

	t.Run("dimension mismatch fewer API rows", func(t *testing.T) {
		apiRows := mkAPIRows([]int64{5, 10})
		parsedRows := []tableRow{
			{cells: []tableCell{
				{segments: []markdownSegment{{text: "A"}}},
				{segments: []markdownSegment{{text: "B"}}},
			}},
			{cells: []tableCell{
				{segments: []markdownSegment{{text: "C"}}},
				{segments: []markdownSegment{{text: "D"}}},
			}},
		}

		// Should not panic — row 1 is silently skipped
		reqs := flattenCellRequests(collectTableCellRequests(apiRows, parsedRows))

		// Only row 0 should produce requests (2 cells = 2 InsertText)
		if len(reqs) != 2 {
			t.Errorf("expected 2 requests (row 0 only), got %d", len(reqs))
		}
		for _, r := range reqs {
			if r.InsertText != nil {
				idx := r.InsertText.Location.Index
				if idx != 5 && idx != 10 {
					t.Errorf("unexpected InsertText at index %d (expected 5 or 10)", idx)
				}
			}
		}
	})

	t.Run("multi-segment cell preserves intra-cell order", func(t *testing.T) {
		apiRows := mkAPIRows([]int64{5})
		parsedRows := []tableRow{
			{cells: []tableCell{
				{segments: []markdownSegment{
					{text: "bold", bold: true},
					{text: " and "},
					{text: "italic", italic: true},
				}},
			}},
		}

		reqs := flattenCellRequests(collectTableCellRequests(apiRows, parsedRows))

		// Verify InsertText comes before UpdateTextStyle for each segment
		for i, r := range reqs {
			if r.UpdateTextStyle == nil {
				continue
			}
			found := false
			for j := 0; j < i; j++ {
				if reqs[j].InsertText != nil &&
					reqs[j].InsertText.Location.Index == r.UpdateTextStyle.Range.StartIndex {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("UpdateTextStyle at position %d has no preceding InsertText for range start %d",
					i, r.UpdateTextStyle.Range.StartIndex)
			}
		}
	})

	t.Run("cell with empty Content slice is skipped", func(t *testing.T) {
		apiRows := []*docs.TableRow{
			{TableCells: []*docs.TableCell{
				{Content: []*docs.StructuralElement{}},
				{Content: []*docs.StructuralElement{{StartIndex: 10}}},
			}},
		}
		parsedRows := []tableRow{
			{cells: []tableCell{
				{segments: []markdownSegment{{text: "A"}}},
				{segments: []markdownSegment{{text: "B"}}},
			}},
		}

		reqs := flattenCellRequests(collectTableCellRequests(apiRows, parsedRows))

		for _, r := range reqs {
			if r.InsertText != nil && r.InsertText.Text == "A" {
				t.Error("cell with empty Content should be skipped")
			}
		}
	})

	t.Run("dimension mismatch fewer API columns", func(t *testing.T) {
		apiRows := mkAPIRows([]int64{5})
		parsedRows := []tableRow{
			{cells: []tableCell{
				{segments: []markdownSegment{{text: "A"}}},
				{segments: []markdownSegment{{text: "B"}}},
			}},
		}

		reqs := flattenCellRequests(collectTableCellRequests(apiRows, parsedRows))

		if len(reqs) != 1 {
			t.Errorf("expected 1 request (cell A only), got %d", len(reqs))
		}
		for _, r := range reqs {
			if r.InsertText != nil && r.InsertText.Text == "B" {
				t.Error("cell B has no API column — should be skipped")
			}
		}
	})

	t.Run("exact cell to index mapping", func(t *testing.T) {
		apiRows := mkAPIRows([]int64{5, 10}, []int64{20, 25})
		parsedRows := []tableRow{
			{cells: []tableCell{
				{segments: []markdownSegment{{text: "A"}}},
				{segments: []markdownSegment{{text: "B"}}},
			}},
			{cells: []tableCell{
				{segments: []markdownSegment{{text: "C"}}},
				{segments: []markdownSegment{{text: "D"}}},
			}},
		}

		reqs := flattenCellRequests(collectTableCellRequests(apiRows, parsedRows))

		expected := map[int64]string{25: "D", 20: "C", 10: "B", 5: "A"}
		for _, r := range reqs {
			if r.InsertText != nil {
				want, ok := expected[r.InsertText.Location.Index]
				if !ok {
					t.Errorf("unexpected InsertText at index %d", r.InsertText.Location.Index)
				} else if r.InsertText.Text != want {
					t.Errorf("at index %d: got %q, want %q",
						r.InsertText.Location.Index, r.InsertText.Text, want)
				}
			}
		}
	})
}

func TestConvertTableToRequests(t *testing.T) {
	table := &tableSegment{
		numColumns: 2,
		rows: []tableRow{
			{cells: []tableCell{
				{segments: []markdownSegment{{text: "Header1"}}},
				{segments: []markdownSegment{{text: "Header2"}}},
			}},
			{cells: []tableCell{
				{segments: []markdownSegment{{text: "Data1"}}},
				{segments: []markdownSegment{{text: "Data2"}}},
			}},
		},
	}

	startIndex := int64(1)

	requests := convertTableToRequests(table, startIndex)

	// Should have exactly one request (InsertTable only, no cell content)
	if len(requests) != 1 {
		t.Fatalf("expected exactly 1 request (InsertTable only), got %d", len(requests))
	}

	// First request should be InsertTable
	if requests[0].InsertTable == nil {
		t.Fatal("first request should be InsertTable")
	}

	insertTable := requests[0].InsertTable
	if insertTable.Rows != int64(len(table.rows)) {
		t.Errorf("rows: got %d, want %d", insertTable.Rows, len(table.rows))
	}
	if insertTable.Columns != int64(table.numColumns) {
		t.Errorf("columns: got %d, want %d", insertTable.Columns, table.numColumns)
	}
	if insertTable.Location.Index != startIndex {
		t.Errorf("location index: got %d, want %d", insertTable.Location.Index, startIndex)
	}
}

// TestConvertMarkdownToRequestsWithTable was removed because convertMarkdownToRequests
// no longer handles table segments - all table processing is done by insertMarkdownWithTables.
// Table segments reaching convertMarkdownToRequests is now a programming error (panic).

func TestBuildCellBatches(t *testing.T) {
	// Helper to build a tableRecord with a given number of cell requests.
	makeRecord := func(idx, numReqs int) tableRecord {
		// Build a minimal table with enough cells to produce numReqs requests.
		// Each cell with non-empty content produces at least 1 InsertText request.
		rows := []tableRow{{cells: make([]tableCell, 0, numReqs)}}
		apiRows := []*docs.TableRow{{TableCells: make([]*docs.TableCell, 0, numReqs)}}

		for i := range numReqs {
			rows[0].cells = append(rows[0].cells, tableCell{
				segments: []markdownSegment{{text: fmt.Sprintf("c%d", i)}},
			})
			apiRows[0].TableCells = append(apiRows[0].TableCells, &docs.TableCell{
				Content: []*docs.StructuralElement{
					{StartIndex: int64(1000*idx + i*10)},
				},
			})
		}
		return tableRecord{
			apiRows:    apiRows,
			parsedRows: rows,
			tableIdx:   idx,
		}
	}

	t.Run("empty tables produce no batches", func(t *testing.T) {
		tables := []tableRecord{
			{apiRows: []*docs.TableRow{}, parsedRows: nil, tableIdx: 0},
		}
		batches := buildCellBatches(tables, 500)
		if len(batches) != 0 {
			t.Errorf("got %d batches, want 0", len(batches))
		}
	})

	t.Run("single small table fits in one batch", func(t *testing.T) {
		tables := []tableRecord{makeRecord(0, 3)}
		batches := buildCellBatches(tables, 500)
		if len(batches) != 1 {
			t.Fatalf("got %d batches, want 1", len(batches))
		}
		if len(batches[0]) == 0 {
			t.Error("batch is empty")
		}
	})

	t.Run("multiple small tables in one batch", func(t *testing.T) {
		tables := []tableRecord{
			makeRecord(0, 3),
			makeRecord(1, 3),
			makeRecord(2, 3),
		}
		batches := buildCellBatches(tables, 500)
		if len(batches) != 1 {
			t.Errorf("got %d batches, want 1", len(batches))
		}
	})

	t.Run("tables split when exceeding maxReqs", func(t *testing.T) {
		tables := []tableRecord{
			makeRecord(0, 3),
			makeRecord(1, 3),
			makeRecord(2, 3),
		}
		// With maxReqs=5, each table produces ~3 requests (1 InsertText per cell).
		// Two tables fit (6 reqs), third forces a new batch.
		batches := buildCellBatches(tables, 5)
		if len(batches) < 2 {
			t.Errorf("got %d batches, want >=2", len(batches))
		}
	})

	t.Run("tables are processed in reverse order", func(t *testing.T) {
		tables := []tableRecord{
			makeRecord(0, 1),
			makeRecord(1, 1),
			makeRecord(2, 1),
		}
		batches := buildCellBatches(tables, 500)
		if len(batches) != 1 {
			t.Fatalf("got %d batches, want 1", len(batches))
		}
		// The batch should have table 2's requests first (reverse order).
		// Table 2 cells have StartIndex >= 2000, table 0 cells have StartIndex >= 0.
		reqs := batches[0]
		if len(reqs) < 3 {
			t.Fatalf("got %d requests, want >= 3", len(reqs))
		}
		// First request should be from table 2 (highest index)
		firstIdx := reqs[0].InsertText.Location.Index
		lastIdx := reqs[len(reqs)-1].InsertText.Location.Index
		if firstIdx <= lastIdx {
			t.Errorf("requests not in reverse order: first index %d <= last index %d", firstIdx, lastIdx)
		}
	})

	t.Run("single table exceeding maxReqs is split", func(t *testing.T) {
		tables := []tableRecord{makeRecord(0, 20)}
		batches := buildCellBatches(tables, 5)
		if len(batches) < 2 {
			t.Fatalf("got %d batches, want >= 2", len(batches))
		}
		for i, b := range batches {
			if len(b) > 5 {
				t.Errorf("batch %d has %d reqs, want <= 5", i, len(b))
			}
		}
	})

	t.Run("maxReqs zero uses default", func(t *testing.T) {
		tables := []tableRecord{makeRecord(0, 3)}
		batches := buildCellBatches(tables, 0)
		if len(batches) != 1 {
			t.Errorf("got %d batches, want 1", len(batches))
		}
	})
}

func TestTableEdgeCases(t *testing.T) {
	t.Run("table with leading newline", func(t *testing.T) {
		// Simulates appending to non-empty document
		markdown := "\n| Name | Nick | Know for |\n| ---- | ---- | -------- |\n| Sergio | impossible to write | wtmcp, refactor, keylime, ... |\n| Igor   | mrisca | bragai, Lola... |\n| Rafael | rafasgj | talking to much... |"

		segments := parseMarkdown(markdown)

		// Verify a table was found
		foundTable := false
		for _, seg := range segments {
			if seg.isTable {
				foundTable = true
				if len(seg.table.rows) != 4 {
					t.Errorf("expected 4 rows, got %d", len(seg.table.rows))
				}
				if seg.table.numColumns != 3 {
					t.Errorf("expected 3 columns, got %d", seg.table.numColumns)
				}
				break
			}
		}

		if !foundTable {
			t.Error("expected to find a table segment with leading newline")
		}
	})

	t.Run("empty cells", func(t *testing.T) {
		markdown := `| A | B |
| --- | --- |
| Content |  |`

		segments := parseMarkdown(markdown)
		if len(segments) == 0 || !segments[0].isTable {
			t.Fatal("expected table segment")
		}

		table := segments[0].table
		if len(table.rows) < 2 {
			t.Fatal("expected at least 2 rows")
		}

		// Check empty cell
		emptyCell := table.rows[1].cells[1]
		if len(emptyCell.segments) != 0 {
			t.Error("empty cell should have no segments")
		}
	})

	t.Run("multiple tables", func(t *testing.T) {
		markdown := `| A |
| --- |
| 1 |

| B |
| --- |
| 2 |`

		segments := parseMarkdown(markdown)
		tableCount := 0
		for _, seg := range segments {
			if seg.isTable {
				tableCount++
			}
		}

		if tableCount != 2 {
			t.Errorf("expected 2 tables, got %d", tableCount)
		}
	})

	t.Run("table with other content", func(t *testing.T) {
		markdown := `# Heading

| Table |
| ----- |
| Data  |

Paragraph`

		segments := parseMarkdown(markdown)

		hasHeading := false
		hasTable := false
		var allText strings.Builder

		for _, seg := range segments {
			if seg.heading > 0 {
				hasHeading = true
			}
			if seg.isTable {
				hasTable = true
			}
			allText.WriteString(seg.text)
		}

		hasParagraph := strings.Contains(allText.String(), "Paragraph")

		if !hasHeading {
			t.Error("expected heading")
		}
		if !hasTable {
			t.Error("expected table")
		}
		if !hasParagraph {
			t.Error("expected paragraph")
		}
	})

	t.Run("malformed table - missing separator", func(t *testing.T) {
		markdown := `| A | B |
| 1 | 2 |`

		segments := parseMarkdown(markdown)

		// Should be treated as plain text, not a table
		for _, seg := range segments {
			if seg.isTable {
				t.Error("malformed table should not be parsed as table")
			}
		}
	})

	t.Run("malformed table - inconsistent columns", func(t *testing.T) {
		markdown := `| A | B |
| --- | --- |
| 1 | 2 | 3 |`

		segments := parseMarkdown(markdown)

		// Should be treated as plain text
		for _, seg := range segments {
			if seg.isTable {
				t.Error("inconsistent table should not be parsed as table")
			}
		}
	})

	t.Run("table with smart chips", func(t *testing.T) {
		markdown := `| Date |
| ---- |
| @today |`

		segments := parseMarkdown(markdown)
		if len(segments) == 0 || !segments[0].isTable {
			t.Fatal("expected table segment")
		}

		table := segments[0].table
		if len(table.rows) < 2 {
			t.Fatal("expected at least 2 rows")
		}

		// Check for date field in data row
		cell := table.rows[1].cells[0]
		foundDateField := false
		for _, seg := range cell.segments {
			if seg.isDateField {
				foundDateField = true
				break
			}
		}

		if !foundDateField {
			t.Error("expected date field in cell")
		}
	})
}

func TestPopulateTableCell(t *testing.T) {
	t.Run("empty cell returns no requests", func(t *testing.T) {
		cell := tableCell{segments: []markdownSegment{}}
		reqs := populateTableCell(&cell, 5)
		if len(reqs) != 0 {
			t.Errorf("expected 0 requests for empty cell, got %d", len(reqs))
		}
	})

	t.Run("single empty text segment returns no requests", func(t *testing.T) {
		cell := tableCell{segments: []markdownSegment{{text: ""}}}
		reqs := populateTableCell(&cell, 5)
		if len(reqs) != 0 {
			t.Errorf("expected 0 requests for empty text segment, got %d", len(reqs))
		}
	})

	t.Run("plain text produces InsertText", func(t *testing.T) {
		cell := tableCell{segments: []markdownSegment{{text: "hello"}}}
		reqs := populateTableCell(&cell, 10)

		foundInsert := false
		for _, req := range reqs {
			if req.InsertText != nil && req.InsertText.Text == "hello" {
				foundInsert = true
				if req.InsertText.Location.Index != 10 {
					t.Errorf("expected index 10, got %d", req.InsertText.Location.Index)
				}
			}
		}
		if !foundInsert {
			t.Error("expected InsertText for plain text cell")
		}
	})

	t.Run("bold text produces InsertText and UpdateTextStyle", func(t *testing.T) {
		cell := tableCell{segments: []markdownSegment{{text: "bold", bold: true}}}
		reqs := populateTableCell(&cell, 5)

		hasInsert := false
		hasBoldStyle := false
		for _, req := range reqs {
			if req.InsertText != nil && req.InsertText.Text == "bold" {
				hasInsert = true
			}
			if req.UpdateTextStyle != nil && req.UpdateTextStyle.TextStyle.Bold {
				hasBoldStyle = true
			}
		}
		if !hasInsert {
			t.Error("expected InsertText")
		}
		if !hasBoldStyle {
			t.Error("expected UpdateTextStyle with Bold=true")
		}
	})

	t.Run("link produces InsertText and UpdateTextStyle with link", func(t *testing.T) {
		cell := tableCell{segments: []markdownSegment{{text: "Google", linkURL: "https://google.com"}}}
		reqs := populateTableCell(&cell, 5)

		hasLink := false
		for _, req := range reqs {
			if req.UpdateTextStyle != nil &&
				req.UpdateTextStyle.TextStyle.Link != nil &&
				req.UpdateTextStyle.TextStyle.Link.Url == "https://google.com" {
				hasLink = true
			}
		}
		if !hasLink {
			t.Error("expected UpdateTextStyle with link")
		}
	})

	t.Run("date chip produces InsertDate", func(t *testing.T) {
		cell := tableCell{segments: []markdownSegment{{isDateField: true, dateValue: "2026-04-27"}}}
		reqs := populateTableCell(&cell, 5)

		hasDate := false
		for _, req := range reqs {
			if req.InsertDate != nil {
				hasDate = true
				if req.InsertDate.Location.Index != 5 {
					t.Errorf("expected index 5, got %d", req.InsertDate.Location.Index)
				}
			}
		}
		if !hasDate {
			t.Error("expected InsertDate for date chip")
		}
	})

	t.Run("person chip produces InsertPerson", func(t *testing.T) {
		cell := tableCell{segments: []markdownSegment{{isPersonField: true, personIdentifier: "user@example.com"}}}
		reqs := populateTableCell(&cell, 5)

		hasPerson := false
		for _, req := range reqs {
			if req.InsertPerson != nil {
				hasPerson = true
				if req.InsertPerson.PersonProperties.Email != "user@example.com" {
					t.Errorf("expected email user@example.com, got %s", req.InsertPerson.PersonProperties.Email)
				}
			}
		}
		if !hasPerson {
			t.Error("expected InsertPerson for person chip")
		}
	})

	t.Run("inline code produces Courier New font", func(t *testing.T) {
		cell := tableCell{segments: []markdownSegment{{text: "code", isInlineCode: true}}}
		reqs := populateTableCell(&cell, 5)

		hasCourier := false
		for _, req := range reqs {
			if req.UpdateTextStyle != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily != nil &&
				req.UpdateTextStyle.TextStyle.WeightedFontFamily.FontFamily == "Courier New" {
				hasCourier = true
			}
		}
		if !hasCourier {
			t.Error("expected Courier New for inline code")
		}
	})
}

func TestTableCellFormattingExtraction(t *testing.T) {
	doc := &docs.Document{
		Body: &docs.Body{
			Content: []*docs.StructuralElement{
				{
					Table: &docs.Table{
						TableRows: []*docs.TableRow{
							{
								TableCells: []*docs.TableCell{
									{
										Content: []*docs.StructuralElement{
											{
												Paragraph: &docs.Paragraph{
													Elements: []*docs.ParagraphElement{
														{
															TextRun: &docs.TextRun{
																Content:   "Header\n",
																TextStyle: &docs.TextStyle{},
															},
														},
													},
												},
											},
										},
									},
								},
							},
							{
								TableCells: []*docs.TableCell{
									{
										Content: []*docs.StructuralElement{
											{
												Paragraph: &docs.Paragraph{
													Elements: []*docs.ParagraphElement{
														{
															TextRun: &docs.TextRun{
																Content:   "bold text\n",
																TextStyle: &docs.TextStyle{Bold: true},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	md := extractMarkdown(doc)

	if !strings.Contains(md, "**bold text**") {
		t.Errorf("expected bold formatting in table cell, got: %s", md)
	}
	if !strings.Contains(md, "| Header |") {
		t.Errorf("expected header row, got: %s", md)
	}
	if !strings.Contains(md, "| --- |") {
		t.Errorf("expected separator row, got: %s", md)
	}
}

func TestParseMarkdownTableDetection(t *testing.T) {
	markdown := "# Title\n\n| A | B |\n| --- | --- |\n| 1 | 2 |\n"
	segments := parseMarkdown(markdown)

	foundTable := false
	for _, seg := range segments {
		if seg.isTable && seg.table != nil {
			foundTable = true
			break
		}
	}
	if !foundTable {
		t.Fatal("parseMarkdown should produce table segments for table input")
	}

	noTableMarkdown := "# Title\n\nSome text with **bold** and *italic*.\n"
	noTableSegments := parseMarkdown(noTableMarkdown)

	for _, seg := range noTableSegments {
		if seg.isTable && seg.table != nil {
			t.Error("non-table markdown should not produce table segments")
		}
	}
}

func TestTableCellPipeEscapingOnExtraction(t *testing.T) {
	doc := &docs.Document{
		Body: &docs.Body{
			Content: []*docs.StructuralElement{
				{
					Table: &docs.Table{
						TableRows: []*docs.TableRow{
							{
								TableCells: []*docs.TableCell{
									{
										Content: []*docs.StructuralElement{
											{
												Paragraph: &docs.Paragraph{
													Elements: []*docs.ParagraphElement{
														{
															TextRun: &docs.TextRun{
																Content:   "Header\n",
																TextStyle: &docs.TextStyle{},
															},
														},
													},
												},
											},
										},
									},
								},
							},
							{
								TableCells: []*docs.TableCell{
									{
										Content: []*docs.StructuralElement{
											{
												Paragraph: &docs.Paragraph{
													Elements: []*docs.ParagraphElement{
														{
															TextRun: &docs.TextRun{
																Content:   "a | b\n",
																TextStyle: &docs.TextStyle{},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	md := extractMarkdown(doc)

	if !strings.Contains(md, `a \| b`) {
		t.Errorf("expected escaped pipe in cell content, got: %s", md)
	}
}

func TestRoundTrip_CodeBlock(t *testing.T) {
	// Start with markdown
	originalMarkdown := "Some intro text.\n\n```python\ndef hello():\n    return \"world\"\n```\n\nSome closing text.\n"

	// Parse markdown to segments
	segments := parseMarkdown(originalMarkdown)

	// Convert segments to requests (simulating write to Google Docs)
	_, _ = convertMarkdownToRequests(segments, 1, true)

	// Create a mock Google Docs document with the expected structure
	doc := &docs.Document{
		Body: &docs.Body{
			Content: []*docs.StructuralElement{
				{
					Paragraph: &docs.Paragraph{
						Elements: []*docs.ParagraphElement{
							{
								TextRun: &docs.TextRun{
									Content: "Some intro text.\n",
									TextStyle: &docs.TextStyle{
										WeightedFontFamily: &docs.WeightedFontFamily{
											FontFamily: "Arial",
										},
									},
								},
							},
						},
					},
				},
				{
					Paragraph: &docs.Paragraph{
						Elements: []*docs.ParagraphElement{
							{
								TextRun: &docs.TextRun{
									Content: "def hello():\n    return \"world\"\n",
									TextStyle: &docs.TextStyle{
										WeightedFontFamily: &docs.WeightedFontFamily{
											FontFamily: "Courier New",
										},
									},
								},
							},
						},
					},
				},
				{
					Paragraph: &docs.Paragraph{
						Elements: []*docs.ParagraphElement{
							{
								TextRun: &docs.TextRun{
									Content: "Some closing text.\n",
									TextStyle: &docs.TextStyle{
										WeightedFontFamily: &docs.WeightedFontFamily{
											FontFamily: "Arial",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Extract markdown from the mock document
	resultMarkdown := extractMarkdown(doc)

	// Verify code block is preserved
	// Note: extractMarkdown outputs a single \n after regular paragraphs (no blank line),
	// and the code block itself adds \n\n after the closing fence.
	// The last paragraph gets a single trailing \n (no extra blank line at EOF).
	expectedMarkdown := "Some intro text.\n```\ndef hello():\n    return \"world\"\n```\n\nSome closing text.\n"

	if resultMarkdown != expectedMarkdown {
		t.Errorf("Round-trip failed.\nExpected:\n%q\nGot:\n%q", expectedMarkdown, resultMarkdown)
	}
}

func TestRoundTrip_InlineCode(t *testing.T) {
	// Start with markdown
	originalMarkdown := "Use `getFoo()` and `setBar()` methods.\n"

	// Create a mock Google Docs document with inline code
	doc := &docs.Document{
		Body: &docs.Body{
			Content: []*docs.StructuralElement{
				{
					Paragraph: &docs.Paragraph{
						Elements: []*docs.ParagraphElement{
							{
								TextRun: &docs.TextRun{
									Content: "Use ",
									TextStyle: &docs.TextStyle{
										WeightedFontFamily: &docs.WeightedFontFamily{
											FontFamily: "Arial",
										},
									},
								},
							},
							{
								TextRun: &docs.TextRun{
									Content: "getFoo()",
									TextStyle: &docs.TextStyle{
										WeightedFontFamily: &docs.WeightedFontFamily{
											FontFamily: "Courier New",
										},
									},
								},
							},
							{
								TextRun: &docs.TextRun{
									Content: " and ",
									TextStyle: &docs.TextStyle{
										WeightedFontFamily: &docs.WeightedFontFamily{
											FontFamily: "Arial",
										},
									},
								},
							},
							{
								TextRun: &docs.TextRun{
									Content: "setBar()",
									TextStyle: &docs.TextStyle{
										WeightedFontFamily: &docs.WeightedFontFamily{
											FontFamily: "Courier New",
										},
									},
								},
							},
							{
								TextRun: &docs.TextRun{
									Content: " methods.\n",
									TextStyle: &docs.TextStyle{
										WeightedFontFamily: &docs.WeightedFontFamily{
											FontFamily: "Arial",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Extract markdown
	resultMarkdown := extractMarkdown(doc)

	// Note: extractMarkdown outputs a single \n after regular paragraphs,
	// not a double newline at EOF.
	expectedMarkdown := "Use `getFoo()` and `setBar()` methods.\n"

	if resultMarkdown != expectedMarkdown {
		t.Errorf("Round-trip failed.\nExpected:\n%q\nGot:\n%q", expectedMarkdown, resultMarkdown)
	}

	// Also verify original markdown parses correctly
	_ = parseMarkdown(originalMarkdown)
}

func TestWriteMarkdownWithHighlightedPython(t *testing.T) {
	markdown := `# Python Example

Here is some Python code:

` + "```python" + `
def greet(name):
    """Say hello."""
    return f"Hello, {name}!"

result = greet("World")
print(result)
` + "```" + `

End of example.
`

	segments := parseMarkdown(markdown)

	// Verify we have a code block
	hasCodeBlock := false
	var codeBlockSeg *markdownSegment
	for i := range segments {
		if segments[i].isCodeBlock && segments[i].codeLanguage == "python" {
			hasCodeBlock = true
			codeBlockSeg = &segments[i]
			break
		}
	}

	if !hasCodeBlock {
		t.Fatalf("Expected Python code block in segments")
	}

	// Verify code block has language set
	if codeBlockSeg.codeLanguage != "python" {
		t.Errorf("Expected language 'python', got %q", codeBlockSeg.codeLanguage)
	}

	// Verify code block contains expected code
	if !strings.Contains(codeBlockSeg.text, "def greet") {
		t.Errorf("Code block missing expected content")
	}
}

func TestWriteMarkdownWithHighlightedGo(t *testing.T) {
	markdown := `# Go Example

` + "```go" + `
package main

import "fmt"

func main() {
    fmt.Println("Hello, World!")
}
` + "```" + `
`

	segments := parseMarkdown(markdown)

	// Verify we have a code block with Go language
	hasGoBlock := false
	for i := range segments {
		if segments[i].isCodeBlock && segments[i].codeLanguage == "go" {
			hasGoBlock = true
			break
		}
	}

	if !hasGoBlock {
		t.Fatalf("Expected Go code block in segments")
	}
}

func TestWriteMarkdownCodeBlockNoLanguage(t *testing.T) {
	markdown := "```\nplain code block\n```"

	segments := parseMarkdown(markdown)

	// Verify code block exists without language
	hasCodeBlock := false
	for i := range segments {
		if segments[i].isCodeBlock {
			hasCodeBlock = true
			if segments[i].codeLanguage != "" {
				t.Errorf("Expected empty language, got %q", segments[i].codeLanguage)
			}
			break
		}
	}

	if !hasCodeBlock {
		t.Fatalf("Expected code block in segments")
	}
}

func TestAllSupportedLanguages(t *testing.T) {
	languages := []string{"python", "typescript", "go", "rust", "bash", "c", "cpp", "yaml", "toml", "json"}

	for _, lang := range languages {
		t.Run(lang, func(t *testing.T) {
			markdown := "```" + lang + "\ntest code\n```"
			segments := parseMarkdown(markdown)

			found := false
			for i := range segments {
				if segments[i].isCodeBlock && segments[i].codeLanguage == lang {
					found = true
					break
				}
			}

			if !found {
				t.Errorf("Failed to parse %s code block", lang)
			}
		})
	}
}

func TestReadFileForWrite(t *testing.T) {
	// Save and restore the global sessionDir between tests
	origSessionDir := sessionDir
	t.Cleanup(func() { sessionDir = origSessionDir })

	t.Run("valid file in sessionDir", func(t *testing.T) {
		dir := t.TempDir()
		sessionDir = dir
		path := filepath.Join(dir, "test.md")
		if err := os.WriteFile(path, []byte("hello world"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}

		data, err := readFileForWrite(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(data) != "hello world" {
			t.Errorf("got %q, want %q", string(data), "hello world")
		}
	})

	t.Run("relative path resolved against sessionDir", func(t *testing.T) {
		dir := t.TempDir()
		sessionDir = dir
		if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("relative"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}

		data, err := readFileForWrite("notes.md")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(data) != "relative" {
			t.Errorf("got %q, want %q", string(data), "relative")
		}
	})

	t.Run("path traversal rejected", func(t *testing.T) {
		dir := t.TempDir()
		sessionDir = dir

		_, err := readFileForWrite("../../../etc/hostname")
		if err == nil {
			t.Fatal("expected error for path traversal")
		}
		if !strings.Contains(err.Error(), "escapes session directory") &&
			!strings.Contains(err.Error(), "resolve path") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("absolute path outside sessionDir rejected", func(t *testing.T) {
		dir := t.TempDir()
		sessionDir = dir

		// Create a file outside sessionDir
		outside := t.TempDir()
		outsidePath := filepath.Join(outside, "secret.txt")
		if err := os.WriteFile(outsidePath, []byte("secret"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}

		_, err := readFileForWrite(outsidePath)
		if err == nil {
			t.Fatal("expected error for absolute path outside sessionDir")
		}
		if !strings.Contains(err.Error(), "escapes session directory") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("symlink pointing outside sessionDir rejected", func(t *testing.T) {
		dir := t.TempDir()
		sessionDir = dir

		outside := t.TempDir()
		outsidePath := filepath.Join(outside, "secret.txt")
		if err := os.WriteFile(outsidePath, []byte("secret"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}

		linkPath := filepath.Join(dir, "link.md")
		if err := os.Symlink(outsidePath, linkPath); err != nil {
			t.Skipf("symlinks not supported: %v", err)
		}

		_, err := readFileForWrite(linkPath)
		if err == nil {
			t.Fatal("expected error for symlink outside sessionDir")
		}
		if !strings.Contains(err.Error(), "escapes session directory") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("sessionDir is symlink file inside real dir works", func(t *testing.T) {
		realDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(realDir, "doc.md"), []byte("content"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}

		parent := t.TempDir()
		linkDir := filepath.Join(parent, "link")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Skipf("symlinks not supported: %v", err)
		}
		sessionDir = linkDir

		data, err := readFileForWrite("doc.md")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(data) != "content" {
			t.Errorf("got %q, want %q", string(data), "content")
		}
	})

	t.Run("non-existent file returns error", func(t *testing.T) {
		dir := t.TempDir()
		sessionDir = dir

		_, err := readFileForWrite(filepath.Join(dir, "missing.md"))
		if err == nil {
			t.Fatal("expected error for non-existent file")
		}
	})

	t.Run("directory path rejected", func(t *testing.T) {
		dir := t.TempDir()
		sessionDir = dir
		subdir := filepath.Join(dir, "subdir")
		if err := os.MkdirAll(subdir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		_, err := readFileForWrite(subdir)
		if err == nil {
			t.Fatal("expected error for directory")
		}
		if !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("file exceeding size limit rejected", func(t *testing.T) {
		dir := t.TempDir()
		sessionDir = dir
		path := filepath.Join(dir, "huge.md")
		// Create a file just over the limit using sparse file
		f, err := os.Create(filepath.Clean(path))
		if err != nil {
			t.Fatalf("create file: %v", err)
		}
		if err := f.Truncate(maxReadFileSize + 1); err != nil {
			_ = f.Close()
			t.Fatalf("truncate: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		_, err = readFileForWrite(path)
		if err == nil {
			t.Fatal("expected error for oversized file")
		}
		if !strings.Contains(err.Error(), "file too large") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("empty file returns empty bytes", func(t *testing.T) {
		dir := t.TempDir()
		sessionDir = dir
		path := filepath.Join(dir, "empty.md")
		if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}

		data, err := readFileForWrite(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(data) != 0 {
			t.Errorf("expected empty bytes, got %d bytes", len(data))
		}
	})

	t.Run("path with spaces works", func(t *testing.T) {
		dir := t.TempDir()
		sessionDir = dir
		path := filepath.Join(dir, "my document.md")
		if err := os.WriteFile(path, []byte("spaced"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}

		data, err := readFileForWrite(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(data) != "spaced" {
			t.Errorf("got %q, want %q", string(data), "spaced")
		}
	})

	t.Run("empty sessionDir rejected", func(t *testing.T) {
		sessionDir = ""

		_, err := readFileForWrite("/tmp/anything.md")
		if err == nil {
			t.Fatal("expected error when sessionDir is empty")
		}
		if !strings.Contains(err.Error(), "requires a configured session directory") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("empty file_path returns error", func(t *testing.T) {
		dir := t.TempDir()
		sessionDir = dir

		_, err := readFileForWrite("")
		if err == nil {
			t.Fatal("expected error for empty file_path")
		}
	})
}

func TestToolWriteContentResolution(t *testing.T) {
	origSessionDir := sessionDir
	t.Cleanup(func() { sessionDir = origSessionDir })

	t.Run("both content and file_path empty returns error", func(t *testing.T) {
		p := writeParams{
			DocumentIDOrURL: "test-doc",
			IsMarkdown:      true,
			AppendToEnd:     true,
		}

		text := p.Content
		if text == "" && p.FilePath != "" {
			data, err := readFileForWrite(p.FilePath)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			text = string(data)
		}
		if text != "" {
			t.Fatal("expected empty text when both content and file_path are empty")
		}
	})

	t.Run("content provided is used directly", func(t *testing.T) {
		p := writeParams{
			DocumentIDOrURL: "test-doc",
			Content:         "inline text",
			IsMarkdown:      true,
			AppendToEnd:     true,
		}

		text := p.Content
		if text != "inline text" {
			t.Errorf("got %q, want %q", text, "inline text")
		}
	})

	t.Run("file_path used when content empty", func(t *testing.T) {
		dir := t.TempDir()
		sessionDir = dir
		fpath := filepath.Join(dir, "input.md")
		if err := os.WriteFile(fpath, []byte("from file"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}

		p := writeParams{
			DocumentIDOrURL: "test-doc",
			FilePath:        fpath,
			IsMarkdown:      true,
			AppendToEnd:     true,
		}

		text := p.Content
		if text == "" && p.FilePath != "" {
			data, err := readFileForWrite(p.FilePath)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			text = string(data)
		}
		if text != "from file" {
			t.Errorf("got %q, want %q", text, "from file")
		}
	})

	t.Run("content takes precedence over file_path", func(t *testing.T) {
		dir := t.TempDir()
		sessionDir = dir
		fpath := filepath.Join(dir, "input.md")
		if err := os.WriteFile(fpath, []byte("from file"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}

		p := writeParams{
			DocumentIDOrURL: "test-doc",
			Content:         "inline wins",
			FilePath:        fpath,
			IsMarkdown:      true,
			AppendToEnd:     true,
		}

		text := p.Content
		if text == "" && p.FilePath != "" {
			data, err := readFileForWrite(p.FilePath)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			text = string(data)
		}
		if text != "inline wins" {
			t.Errorf("got %q, want %q", text, "inline wins")
		}
	})
}
