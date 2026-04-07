package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestParseInlineFormatting(t *testing.T) {
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
		requests := convertMarkdownToRequests(segments, 1)

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
		if foundNormalTextStyle {
			t.Errorf("NORMAL_TEXT style should NOT be applied (found %d instances), as it wipes run-level formatting", normalTextStyleCount)
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

func TestNoTrailingEmptyParagraph(t *testing.T) {
	t.Run("last text segment has no trailing newline", func(t *testing.T) {
		segments := parseMarkdown("# Heading\n\nText")
		requests := convertMarkdownToRequests(segments, 1)

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
		requests := convertMarkdownToRequests(segments, 1)

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
		requests := convertMarkdownToRequests(segments, 1)

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

		expected := "line1\nline2\nline3\nline4\n"
		if fullText != expected {
			t.Errorf("Normalized text = %q, want %q", fullText, expected)
		}
	})
}

func TestHeadingWithFormattedText(t *testing.T) {
	t.Run("heading with bold text creates single heading", func(t *testing.T) {
		segments := parseMarkdown("# **Bold** Normal")
		requests := convertMarkdownToRequests(segments, 1)

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
		requests := convertMarkdownToRequests(segments, 1)

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
		requests := convertMarkdownToRequests(segments, 1)

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

func TestSaveDocumentFile(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	t.Run("saves with title-derived path", func(t *testing.T) {
		got, err := saveDocumentFile("My Document", "", "content", ".txt")
		if err != nil {
			t.Fatalf("saveDocumentFile: %v", err)
		}
		wantAbs := filepath.Join(tmpDir, "docs", "My Document.txt")
		if got != wantAbs {
			t.Errorf("path = %q, want %q", got, wantAbs)
		}
	})

	t.Run("saves with explicit path inside base", func(t *testing.T) {
		outPath := filepath.Join("docs", "custom.txt")
		got, err := saveDocumentFile("", outPath, "content", ".txt")
		if err != nil {
			t.Fatalf("saveDocumentFile: %v", err)
		}
		wantAbs := filepath.Join(tmpDir, "docs", "custom.txt")
		if got != wantAbs {
			t.Errorf("path = %q, want %q", got, wantAbs)
		}
	})

	t.Run("rejects path traversal", func(t *testing.T) {
		_, err := saveDocumentFile("", "../../etc/evil.txt", "pwned", ".txt")
		if err == nil {
			t.Fatal("expected error for path traversal, got nil")
		}
	})

	t.Run("sanitizes title with traversal characters", func(t *testing.T) {
		got, err := saveDocumentFile("../../etc/evil", "", "content", ".txt")
		if err != nil {
			t.Fatalf("saveDocumentFile: %v", err)
		}
		if !strings.HasPrefix(got, filepath.Join(tmpDir, "docs")+string(os.PathSeparator)) {
			t.Errorf("path %q escapes docs directory", got)
		}
	})

	t.Run("file permissions are 0600", func(t *testing.T) {
		outPath := filepath.Join("docs", "perms.txt")
		got, err := saveDocumentFile("", outPath, "secret", ".txt")
		if err != nil {
			t.Fatalf("saveDocumentFile: %v", err)
		}
		info, err := os.Stat(got)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("permissions = %o, want 0600", perm)
		}
	})
}
