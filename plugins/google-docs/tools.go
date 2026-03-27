package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/api/docs/v1"
)

// extractDocumentID extracts a Google Docs document ID from a URL.
func extractDocumentID(input string) string {
	// Try to extract from URL patterns
	re := regexp.MustCompile(`/document/d/([A-Za-z0-9_-]+)`)
	if m := re.FindStringSubmatch(input); len(m) > 1 {
		return m[1]
	}
	// Try ?id= query parameter
	re2 := regexp.MustCompile(`[?&]id=([A-Za-z0-9_-]+)`)
	if m := re2.FindStringSubmatch(input); len(m) > 1 {
		return m[1]
	}
	// If no URL pattern matches, assume it's already a document ID
	if regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(input) {
		return input
	}
	return ""
}

// saveDocumentFile saves document content to a local file.
// If outputPath is empty, saves to ./docs/<title>.<ext>.
func saveDocumentFile(title, outputPath, content, ext string) (string, error) {
	if outputPath == "" {
		docsDir := "docs"
		if err := os.MkdirAll(docsDir, 0o750); err != nil {
			return "", fmt.Errorf("create docs dir: %w", err)
		}
		safeTitle := regexp.MustCompile(`[<>:"/\\|?*]`).ReplaceAllString(title, "_")
		outputPath = filepath.Join(docsDir, safeTitle+ext)
	}

	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}

	if err := os.WriteFile(outputPath, []byte(content), 0o600); err != nil {
		return "", err
	}

	abs, err := filepath.Abs(outputPath)
	if err != nil {
		return outputPath, err
	}
	return abs, nil
}

// extractText extracts plain text from document content.
func extractText(doc *docs.Document) string {
	var text strings.Builder

	if doc.Body == nil || doc.Body.Content == nil {
		return ""
	}

	for _, elem := range doc.Body.Content {
		extractElementText(&text, elem)
	}

	return text.String()
}

// extractElementText recursively extracts text from a document element.
func extractElementText(sb *strings.Builder, elem *docs.StructuralElement) {
	if elem.Paragraph != nil {
		for _, pe := range elem.Paragraph.Elements {
			if pe.TextRun != nil {
				sb.WriteString(pe.TextRun.Content)
			}
		}
	}
	if elem.Table != nil {
		for _, row := range elem.Table.TableRows {
			for _, cell := range row.TableCells {
				for _, cellElem := range cell.Content {
					extractElementText(sb, cellElem)
				}
			}
		}
	}
}

// extractMarkdown converts document structure to Markdown format.
func extractMarkdown(doc *docs.Document) string {
	var md strings.Builder

	if doc.Body == nil || doc.Body.Content == nil {
		return ""
	}

	for _, elem := range doc.Body.Content {
		extractElementMarkdown(&md, elem)
	}

	return md.String()
}

// extractElementMarkdown recursively converts document elements to Markdown.
func extractElementMarkdown(sb *strings.Builder, elem *docs.StructuralElement) {
	if elem.Paragraph != nil {
		para := elem.Paragraph

		// Determine if this is a heading
		var prefix, suffix string
		if para.ParagraphStyle != nil && para.ParagraphStyle.NamedStyleType != "" {
			switch para.ParagraphStyle.NamedStyleType {
			case "HEADING_1":
				prefix = "# "
			case "HEADING_2":
				prefix = "## "
			case "HEADING_3":
				prefix = "### "
			case "HEADING_4":
				prefix = "#### "
			case "HEADING_5":
				prefix = "##### "
			case "HEADING_6":
				prefix = "###### "
			case "TITLE":
				prefix = "# "
			case "SUBTITLE":
				prefix = "## "
			}
		}

		// Extract text with formatting
		var paraText strings.Builder
		for _, pe := range para.Elements {
			if pe.TextRun != nil {
				text := pe.TextRun.Content
				style := pe.TextRun.TextStyle

				// Apply text formatting
				if style != nil {
					if style.Bold {
						text = "**" + strings.TrimSpace(text) + "**"
					}
					if style.Italic {
						text = "*" + strings.TrimSpace(text) + "*"
					}
					if style.Underline {
						text = "__" + strings.TrimSpace(text) + "__"
					}
					if style.Link != nil && style.Link.Url != "" {
						text = "[" + strings.TrimSpace(text) + "](" + style.Link.Url + ")"
					}
				}

				paraText.WriteString(text)
			}
		}

		// Write the paragraph
		fullText := strings.TrimSpace(paraText.String())
		if fullText != "" {
			sb.WriteString(prefix)
			sb.WriteString(fullText)
			sb.WriteString(suffix)
			sb.WriteString("\n")

			// Add extra newline after headings
			if prefix != "" {
				sb.WriteString("\n")
			}
		} else if paraText.Len() > 0 {
			// Preserve blank lines
			sb.WriteString("\n")
		}
	}

	if elem.Table != nil {
		table := elem.Table

		// Convert table to Markdown table
		for rowIdx, row := range table.TableRows {
			sb.WriteString("|")
			for _, cell := range row.TableCells {
				var cellText strings.Builder
				for _, cellElem := range cell.Content {
					extractElementText(&cellText, cellElem)
				}
				sb.WriteString(" ")
				sb.WriteString(strings.TrimSpace(cellText.String()))
				sb.WriteString(" |")
			}
			sb.WriteString("\n")

			// Add header separator after first row
			if rowIdx == 0 {
				sb.WriteString("|")
				for range row.TableCells {
					sb.WriteString(" --- |")
				}
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n")
	}

	if elem.TableOfContents != nil {
		sb.WriteString("*[Table of Contents]*\n\n")
	}
}

// summarizeDocument creates a summary of the document structure and content.
func summarizeDocument(doc *docs.Document, includeStructure bool) map[string]any {
	summary := map[string]any{
		"title":       doc.Title,
		"document_id": doc.DocumentId,
		"revision_id": doc.RevisionId,
	}

	if doc.Body == nil || doc.Body.Content == nil {
		return summary
	}

	// Count elements
	var paragraphs, headings, lists, tables, images int
	var wordCount int
	headingsList := []string{}

	for _, elem := range doc.Body.Content {
		if elem.Paragraph != nil {
			paragraphs++

			// Count headings
			if elem.Paragraph.ParagraphStyle != nil {
				styleType := elem.Paragraph.ParagraphStyle.NamedStyleType
				if strings.HasPrefix(styleType, "HEADING_") || styleType == "TITLE" || styleType == "SUBTITLE" {
					headings++

					// Extract heading text
					var headingText strings.Builder
					for _, pe := range elem.Paragraph.Elements {
						if pe.TextRun != nil {
							headingText.WriteString(pe.TextRun.Content)
						}
					}
					if includeStructure {
						headingsList = append(headingsList, strings.TrimSpace(headingText.String()))
					}
				}
			}

			// Count words
			for _, pe := range elem.Paragraph.Elements {
				if pe.TextRun != nil {
					words := strings.Fields(pe.TextRun.Content)
					wordCount += len(words)
				}
			}

			// Check for bullet lists
			if elem.Paragraph.Bullet != nil {
				lists++
			}
		}

		if elem.Table != nil {
			tables++
		}
	}

	// Extract text preview (first 500 characters)
	fullText := extractText(doc)
	preview := fullText
	if len(preview) > 500 {
		preview = preview[:500] + "..."
	}

	summary["stats"] = map[string]int{
		"paragraphs": paragraphs,
		"headings":   headings,
		"lists":      lists,
		"tables":     tables,
		"images":     images,
		"word_count": wordCount,
		"characters": len(fullText),
	}

	summary["preview"] = preview

	if includeStructure && len(headingsList) > 0 {
		summary["headings"] = headingsList
	}

	return summary
}

// Tool implementations

type getDocumentParams struct {
	DocumentIDOrURL string `json:"document_id_or_url"`
}

func toolGetDocument(params, _ json.RawMessage) (any, error) {
	var p getDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.DocumentIDOrURL == "" {
		return nil, fmt.Errorf("document_id_or_url is required")
	}

	docID := extractDocumentID(p.DocumentIDOrURL)
	if docID == "" {
		return map[string]string{
			"error": "could not extract document ID from input",
			"input": p.DocumentIDOrURL,
		}, nil
	}

	doc, err := docsSvc.Documents.Get(docID).Do()
	if err != nil {
		return nil, fmt.Errorf("get document: %w", err)
	}

	return doc, nil
}

type getDocumentTextParams struct {
	DocumentIDOrURL string `json:"document_id_or_url"`
	SaveToFile      bool   `json:"save_to_file"`
	OutputPath      string `json:"output_path"`
}

func toolGetDocumentText(params, _ json.RawMessage) (any, error) {
	var p getDocumentTextParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.DocumentIDOrURL == "" {
		return nil, fmt.Errorf("document_id_or_url is required")
	}

	docID := extractDocumentID(p.DocumentIDOrURL)
	if docID == "" {
		return map[string]string{
			"error": "could not extract document ID from input",
			"input": p.DocumentIDOrURL,
		}, nil
	}

	doc, err := docsSvc.Documents.Get(docID).Do()
	if err != nil {
		return nil, fmt.Errorf("get document: %w", err)
	}

	// Check for empty document body
	if doc.Body == nil || doc.Body.Content == nil || len(doc.Body.Content) == 0 {
		return nil, fmt.Errorf("document %s has no content body", docID)
	}

	text := extractText(doc)

	result := map[string]any{
		"document_id": doc.DocumentId,
		"title":       doc.Title,
		"text":        text,
		"characters":  len(text),
		"word_count":  len(strings.Fields(text)),
	}

	if p.SaveToFile {
		savedPath, err := saveDocumentFile(doc.Title, p.OutputPath, text, ".txt")
		if err != nil {
			return nil, fmt.Errorf("save file: %w", err)
		}
		result["status"] = "saved"
		result["output_path"] = savedPath
	}

	return result, nil
}

type getDocumentMarkdownParams struct {
	DocumentIDOrURL string `json:"document_id_or_url"`
	SaveToFile      bool   `json:"save_to_file"`
	OutputPath      string `json:"output_path"`
}

func toolGetDocumentMarkdown(params, _ json.RawMessage) (any, error) {
	// Initialize with defaults matching plugin.yaml contract
	p := getDocumentMarkdownParams{
		SaveToFile: false,
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.DocumentIDOrURL == "" {
		return nil, fmt.Errorf("document_id_or_url is required")
	}

	docID := extractDocumentID(p.DocumentIDOrURL)
	if docID == "" {
		return map[string]string{
			"error": "could not extract document ID from input",
			"input": p.DocumentIDOrURL,
		}, nil
	}

	doc, err := docsSvc.Documents.Get(docID).Do()
	if err != nil {
		return nil, fmt.Errorf("get document: %w", err)
	}

	// Check for empty document body
	if doc.Body == nil || doc.Body.Content == nil || len(doc.Body.Content) == 0 {
		return nil, fmt.Errorf("document %s has no content body", docID)
	}

	markdown := extractMarkdown(doc)

	result := map[string]any{
		"document_id": doc.DocumentId,
		"title":       doc.Title,
		"markdown":    markdown,
		"characters":  len(markdown),
	}

	if p.SaveToFile {
		savedPath, err := saveDocumentFile(doc.Title, p.OutputPath, markdown, ".md")
		if err != nil {
			return nil, fmt.Errorf("save file: %w", err)
		}
		result["status"] = "saved"
		result["output_path"] = savedPath
	}

	return result, nil
}

type summarizeDocumentParams struct {
	DocumentIDOrURL  string `json:"document_id_or_url"`
	IncludeStructure bool   `json:"include_structure"`
}

func toolSummarizeDocument(params, _ json.RawMessage) (any, error) {
	// Initialize with defaults matching plugin.yaml contract
	p := summarizeDocumentParams{
		IncludeStructure: true,
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.DocumentIDOrURL == "" {
		return nil, fmt.Errorf("document_id_or_url is required")
	}

	docID := extractDocumentID(p.DocumentIDOrURL)
	if docID == "" {
		return map[string]string{
			"error": "could not extract document ID from input",
			"input": p.DocumentIDOrURL,
		}, nil
	}

	doc, err := docsSvc.Documents.Get(docID).Do()
	if err != nil {
		return nil, fmt.Errorf("get document: %w", err)
	}

	// Check for empty document body
	if doc.Body == nil || doc.Body.Content == nil || len(doc.Body.Content) == 0 {
		return nil, fmt.Errorf("document %s has no content body", docID)
	}

	summary := summarizeDocument(doc, p.IncludeStructure)
	return summary, nil
}

type extractAndGetParams struct {
	Text    string `json:"text"`
	MaxDocs int    `json:"max_docs"`
}

func toolExtractAndGet(params, _ json.RawMessage) (any, error) {
	var p extractAndGetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.Text == "" {
		return nil, fmt.Errorf("text is required")
	}
	if p.MaxDocs == 0 {
		p.MaxDocs = 5
	}

	re := regexp.MustCompile(`https?://docs\.google\.com/document/[\w\-/\?=&#%.]+`)
	urls := re.FindAllString(p.Text, -1)

	var results []any
	seen := make(map[string]bool)

	for _, u := range urls {
		if len(results) >= p.MaxDocs {
			break
		}

		docID := extractDocumentID(u)
		if docID == "" || seen[docID] {
			continue
		}
		seen[docID] = true

		doc, err := docsSvc.Documents.Get(docID).Do()
		if err != nil {
			results = append(results, map[string]string{
				"error": err.Error(),
				"url":   u,
			})
			continue
		}

		summary := summarizeDocument(doc, true)
		summary["url"] = u
		results = append(results, summary)
	}

	return map[string]any{
		"documents": results,
		"count":     len(results),
	}, nil
}

// markdownSegment represents a segment of text with associated formatting.
type markdownSegment struct {
	text              string
	bold              bool
	italic            bool
	underline         bool
	linkURL           string
	heading           int    // 0 for normal, 1-6 for heading levels
	orderedListItem   bool   // true if this is an ordered list item
	unorderedListItem bool   // true if this is an unordered list item
	listDepth         int    // nesting level: 0=top-level list item, 1=first nested, etc.
	isDateField       bool   // true if this should be a date field (@today or @date(YYYY-MM-DD))
	dateValue         string // specific date in YYYY-MM-DD format, empty means @today
	isPersonField     bool   // true if this should be a person field (@(name or email))
	personIdentifier  string // name or email for person field
}

// indentDepth converts a leading whitespace string to a nesting depth.
// Per the markdown standard, each nesting level requires 4 spaces or 1 tab.
func indentDepth(indent string) int {
	depth := 0
	i := 0
	for i < len(indent) {
		switch {
		case indent[i] == '\t':
			depth++
			i++
		case strings.HasPrefix(indent[i:], "    "): // 4 spaces → 1 level
			depth++
			i += 4
		default:
			i++
		}
	}
	return depth
}

// parseMarkdown parses markdown text and returns segments with formatting info.
func parseMarkdown(markdown string) []markdownSegment {
	var segments []markdownSegment
	lines := strings.Split(markdown, "\n")

	for idx, line := range lines {
		trimmedLine := strings.TrimSpace(line)

		if trimmedLine == "" {
			// Blank lines before headings are skipped — headings carry their own
			// paragraph break. Blank lines elsewhere become a paragraph separator
			// so that visual gaps between blocks are preserved in the document.
			nextIsHeading := false
			for _, next := range lines[idx+1:] {
				if t := strings.TrimSpace(next); t != "" {
					nextIsHeading = strings.HasPrefix(t, "#")
					break
				}
			}
			if !nextIsHeading {
				segments = append(segments, markdownSegment{text: "\n"})
			}
			continue
		}

		// Check for headings
		headingLevel := 0
		if strings.HasPrefix(trimmedLine, "#") {
		loop:
			for i, ch := range trimmedLine {
				switch ch {
				case '#':
					headingLevel++
				case ' ':
					trimmedLine = trimmedLine[i+1:]
					break loop
				default:
					headingLevel = 0
					break loop
				}
			}
		}

		if headingLevel > 0 && headingLevel <= 6 {
			// Heading line
			segments = append(segments, markdownSegment{
				text:    trimmedLine + "\n",
				heading: headingLevel,
			})
			continue
		}

		// Check for ordered list (e.g., "1. Item", "2. Item") with optional leading indent
		if orderedListMatch := regexp.MustCompile(`^(\s*)\d+\.\s+(.+)$`).FindStringSubmatch(line); orderedListMatch != nil {
			depth := indentDepth(orderedListMatch[1])
			listItemText := orderedListMatch[2]
			inlineSegs := parseInlineFormatting(listItemText + "\n")
			for i := range inlineSegs {
				inlineSegs[i].orderedListItem = true
				inlineSegs[i].listDepth = depth
			}
			segments = append(segments, inlineSegs...)
			continue
		}

		// Check for unordered list (e.g., "- Item", "* Item", "+ Item") with optional leading indent
		if unorderedListMatch := regexp.MustCompile(`^(\s*)([-*+])\s+(.+)$`).FindStringSubmatch(line); unorderedListMatch != nil {
			depth := indentDepth(unorderedListMatch[1])
			listItemText := unorderedListMatch[3]
			inlineSegs := parseInlineFormatting(listItemText + "\n")
			for i := range inlineSegs {
				inlineSegs[i].unorderedListItem = true
				inlineSegs[i].listDepth = depth
			}
			segments = append(segments, inlineSegs...)
			continue
		}

		// Parse inline formatting
		segments = append(segments, parseInlineFormatting(line+"\n")...)
	}

	return segments
}

// parseInlineFormatting parses inline markdown formatting (bold, italic, links, smart chips, etc).
func parseInlineFormatting(text string) []markdownSegment {
	var segments []markdownSegment
	pos := 0

	for pos < len(text) {
		// Check for @date(YYYY-MM-DD) - must come before @today check
		if dateMatch := regexp.MustCompile(`@date\((\d{4}-\d{2}-\d{2})\)`).FindStringSubmatchIndex(text[pos:]); dateMatch != nil {
			// Add text before date field
			if dateMatch[0] > 0 {
				segments = append(segments, parseSimpleFormatting(text[pos:pos+dateMatch[0]])...)
			}
			// Add date field with specific date
			dateValue := text[pos+dateMatch[2] : pos+dateMatch[3]]
			segments = append(segments, markdownSegment{
				text:        " ", // Date fields need at least one character
				isDateField: true,
				dateValue:   dateValue,
			})
			pos += dateMatch[1]
			continue
		}

		// Check for @today (current date)
		if strings.HasPrefix(text[pos:], "@today") {
			segments = append(segments, markdownSegment{
				text:        " ", // Date fields need at least one character
				isDateField: true,
				dateValue:   "", // Empty means use current date
			})
			pos += len("@today")
			continue
		}

		// Check for @(name or email) - person smart chip
		if personMatch := regexp.MustCompile(`@\(([^)]+)\)`).FindStringSubmatchIndex(text[pos:]); personMatch != nil {
			// Add text before person field
			if personMatch[0] > 0 {
				segments = append(segments, parseSimpleFormatting(text[pos:pos+personMatch[0]])...)
			}
			// Add person field
			identifier := text[pos+personMatch[2] : pos+personMatch[3]]
			segments = append(segments, markdownSegment{
				text:             " ", // Person fields need at least one character
				isPersonField:    true,
				personIdentifier: identifier,
			})
			pos += personMatch[1]
			continue
		}

		// Check for link: [text](url)
		if linkMatch := regexp.MustCompile(`\[([^\]]+)\]\(([^\)]+)\)`).FindStringSubmatchIndex(text[pos:]); linkMatch != nil {
			// Add text before link
			if linkMatch[0] > 0 {
				segments = append(segments, parseSimpleFormatting(text[pos:pos+linkMatch[0]])...)
			}
			// Add link
			linkText := text[pos+linkMatch[2] : pos+linkMatch[3]]
			linkURL := text[pos+linkMatch[4] : pos+linkMatch[5]]
			segments = append(segments, markdownSegment{
				text:    linkText,
				linkURL: linkURL,
			})
			pos += linkMatch[1]
			continue
		}

		// No more special formatting, process rest as simple formatting
		segments = append(segments, parseSimpleFormatting(text[pos:])...)
		break
	}

	return segments
}

// parseSimpleFormatting parses bold, italic, and underline formatting.
func parseSimpleFormatting(text string) []markdownSegment {
	var segments []markdownSegment
	pos := 0

	for pos < len(text) {
		// Check for **bold**
		if strings.HasPrefix(text[pos:], "**") {
			endPos := strings.Index(text[pos+2:], "**")
			if endPos != -1 {
				endPos += pos + 2
				segments = append(segments, markdownSegment{
					text: text[pos+2 : endPos],
					bold: true,
				})
				pos = endPos + 2
				continue
			}
		}

		// Check for __underline__
		if strings.HasPrefix(text[pos:], "__") {
			endPos := strings.Index(text[pos+2:], "__")
			if endPos != -1 {
				endPos += pos + 2
				segments = append(segments, markdownSegment{
					text:      text[pos+2 : endPos],
					underline: true,
				})
				pos = endPos + 2
				continue
			}
		}

		// Check for *italic*
		if strings.HasPrefix(text[pos:], "*") && !strings.HasPrefix(text[pos:], "**") {
			endPos := strings.Index(text[pos+1:], "*")
			if endPos != -1 {
				endPos += pos + 1
				// Make sure it's not part of **
				if endPos+1 < len(text) && text[endPos+1] == '*' {
					segments = append(segments, markdownSegment{text: text[pos : pos+1]})
					pos++
					continue
				}
				segments = append(segments, markdownSegment{
					text:   text[pos+1 : endPos],
					italic: true,
				})
				pos = endPos + 1
				continue
			}
		}

		// Check for _italic_ (single underscore, not double)
		if strings.HasPrefix(text[pos:], "_") && !strings.HasPrefix(text[pos:], "__") {
			endPos := strings.Index(text[pos+1:], "_")
			if endPos != -1 {
				endPos += pos + 1
				// Make sure it's not part of __
				if endPos+1 < len(text) && text[endPos+1] == '_' {
					segments = append(segments, markdownSegment{text: text[pos : pos+1]})
					pos++
					continue
				}
				segments = append(segments, markdownSegment{
					text:   text[pos+1 : endPos],
					italic: true,
				})
				pos = endPos + 1
				continue
			}
		}

		// Plain character
		segments = append(segments, markdownSegment{text: text[pos : pos+1]})
		pos++
	}

	return segments
}

// mergeSegments combines consecutive segments with identical formatting properties
func mergeSegments(segments []markdownSegment) []markdownSegment {
	if len(segments) == 0 {
		return segments
	}

	merged := []markdownSegment{segments[0]}

	for i := 1; i < len(segments); i++ {
		curr := segments[i]
		prev := &merged[len(merged)-1]

		// Check if segments can be merged (same formatting, not special fields)
		if curr.bold == prev.bold &&
			curr.italic == prev.italic &&
			curr.underline == prev.underline &&
			curr.linkURL == prev.linkURL &&
			curr.heading == prev.heading &&
			curr.orderedListItem == prev.orderedListItem &&
			curr.unorderedListItem == prev.unorderedListItem &&
			curr.listDepth == prev.listDepth &&
			!curr.isDateField && !prev.isDateField &&
			!curr.isPersonField && !prev.isPersonField {
			// Merge by concatenating text
			prev.text += curr.text
		} else {
			// Cannot merge, append as new segment
			merged = append(merged, curr)
		}
	}

	return merged
}

// convertMarkdownToRequests converts markdown segments to Google Docs API requests.
func convertMarkdownToRequests(segments []markdownSegment, startIndex int64) []*docs.Request {
	var requests []*docs.Request
	currentIndex := startIndex

	// Merge consecutive segments with identical formatting to reduce API requests
	segments = mergeSegments(segments)

	// Track list ranges for batch processing
	type listRange struct {
		startIndex int64
		endIndex   int64
		isOrdered  bool
	}
	var listRanges []listRange
	var currentListStart int64 = -1
	var currentListIsOrdered bool

	for i, seg := range segments {
		if seg.text == "" && !seg.isDateField && !seg.isPersonField {
			continue
		}

		var endIndex int64

		// Handle date fields (@today or @date(YYYY-MM-DD))
		if seg.isDateField {
			// Close any open list before inserting date
			if currentListStart >= 0 {
				listRanges = append(listRanges, listRange{
					startIndex: currentListStart,
					endIndex:   currentIndex,
					isOrdered:  currentListIsOrdered,
				})
				currentListStart = -1
			}

			//  Insert placeholder text to create paragraph context for the date
			// Dates must be inside paragraph bounds, not at the start
			placeholderText := seg.text
			if placeholderText == "" {
				placeholderText = " "
			}
			requests = append(requests, &docs.Request{
				InsertText: &docs.InsertTextRequest{
					Text:     placeholderText,
					Location: &docs.Location{Index: currentIndex},
				},
			})

			var timestamp string
			if seg.dateValue == "" {
				// @today - RFC3339 format ending with Z (required by protobuf Timestamp)
				timestamp = time.Now().UTC().Format(time.RFC3339)
			} else {
				// @date(YYYY-MM-DD) - parse and format as RFC3339 with Z suffix
				dateParts := strings.Split(seg.dateValue, "-")
				if len(dateParts) == 3 {
					year, _ := strconv.Atoi(dateParts[0])
					month, _ := strconv.Atoi(dateParts[1])
					day, _ := strconv.Atoi(dateParts[2])
					specificDate := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
					timestamp = specificDate.UTC().Format(time.RFC3339)
				} else {
					// Invalid date format, fall back to current date
					timestamp = time.Now().UTC().Format(time.RFC3339)
				}
			}

			// Delete the placeholder text
			placeholderEndIndex := currentIndex + int64(utf8.RuneCountInString(placeholderText))
			requests = append(requests, &docs.Request{
				DeleteContentRange: &docs.DeleteContentRangeRequest{
					Range: &docs.Range{
						StartIndex: currentIndex,
						EndIndex:   placeholderEndIndex,
					},
				},
			})

			// Insert the date chip at the same location
			// Note: After deletion, currentIndex is now a valid position inside the paragraph
			requests = append(requests, &docs.Request{
				InsertDate: &docs.InsertDateRequest{
					Location: &docs.Location{
						Index: currentIndex,
					},
					DateElementProperties: &docs.DateElementProperties{
						Timestamp:  timestamp,
						DateFormat: "DATE_FORMAT_MONTH_DAY_YEAR_ABBREVIATED",
					},
				},
			})
			// Date elements take 1 character in the document index
			endIndex = currentIndex + 1
			currentIndex = endIndex
			continue
		}

		// Handle person fields (@(name or email))
		if seg.isPersonField {
			// Close any open list before inserting person
			if currentListStart >= 0 {
				listRanges = append(listRanges, listRange{
					startIndex: currentListStart,
					endIndex:   currentIndex,
					isOrdered:  currentListIsOrdered,
				})
				currentListStart = -1
			}

			// Check if the identifier is an email (contains @)
			personProps := &docs.PersonProperties{}
			if strings.Contains(seg.personIdentifier, "@") {
				// It's an email address
				personProps.Email = seg.personIdentifier
			} else {
				// It's a name - set both name and use it as email placeholder
				personProps.Name = seg.personIdentifier
				personProps.Email = seg.personIdentifier
			}

			requests = append(requests, &docs.Request{
				InsertPerson: &docs.InsertPersonRequest{
					Location:         &docs.Location{Index: currentIndex},
					PersonProperties: personProps,
				},
			})
			// Person elements take 1 character in the index
			endIndex = currentIndex + 1
			currentIndex = endIndex
			continue
		}

		// Track list boundaries
		isListItem := seg.orderedListItem || seg.unorderedListItem
		if isListItem {
			if currentListStart < 0 {
				// Start new list
				currentListStart = currentIndex
				currentListIsOrdered = seg.orderedListItem
			} else if (seg.orderedListItem && !currentListIsOrdered) || (seg.unorderedListItem && currentListIsOrdered) {
				// List type changed. Only split the list for top-level (depth=0) items;
				// nested items of a different type (e.g. unordered bullets inside a
				// numbered list) stay within the same outer list range so that the
				// outer numbering is not reset.
				if seg.listDepth == 0 {
					listRanges = append(listRanges, listRange{
						startIndex: currentListStart,
						endIndex:   currentIndex,
						isOrdered:  currentListIsOrdered,
					})
					currentListStart = currentIndex
					currentListIsOrdered = seg.orderedListItem
				}
			}
		} else {
			// Not a list item, close any open list
			if currentListStart >= 0 {
				listRanges = append(listRanges, listRange{
					startIndex: currentListStart,
					endIndex:   currentIndex,
					isOrdered:  currentListIsOrdered,
				})
				currentListStart = -1
			}
		}

		// For nested list items, prepend one tab per nesting level to EACH paragraph
		// line. CreateParagraphBullets counts leading tabs to determine nesting level
		// and removes them automatically — no manual deletion needed afterwards.
		insertText := seg.text
		if isListItem && seg.listDepth > 0 {
			tabs := strings.Repeat("\t", seg.listDepth)
			// seg.text may contain multiple \n-separated paragraphs after merging;
			// every non-empty line needs its own tab prefix.
			var outLines []string
			for _, line := range strings.Split(seg.text, "\n") {
				if line != "" {
					outLines = append(outLines, tabs+line)
				} else {
					outLines = append(outLines, line)
				}
			}
			insertText = strings.Join(outLines, "\n")
		}

		// Insert the text
		requests = append(requests, &docs.Request{
			InsertText: &docs.InsertTextRequest{
				Text:     insertText,
				Location: &docs.Location{Index: currentIndex},
			},
		})

		// Use rune count, not byte length! Multi-byte UTF-8 characters need proper counting
		endIndex = currentIndex + int64(utf8.RuneCountInString(insertText))

		// Apply heading style
		if seg.heading > 0 {
			headingStyle := fmt.Sprintf("HEADING_%d", seg.heading)
			requests = append(requests, &docs.Request{
				UpdateParagraphStyle: &docs.UpdateParagraphStyleRequest{
					Range: &docs.Range{
						StartIndex: currentIndex,
						EndIndex:   endIndex,
					},
					ParagraphStyle: &docs.ParagraphStyle{
						NamedStyleType: headingStyle,
					},
					Fields: "namedStyleType",
				},
			})
		}

		// Apply text formatting - ALWAYS apply to ensure formatting is explicitly reset
		// This prevents bold/italic/underline from "sticking" to subsequent text
		textStyle := &docs.TextStyle{
			Bold:      seg.bold,
			Italic:    seg.italic,
			Underline: seg.underline,
		}

		if seg.linkURL != "" {
			textStyle.Link = &docs.Link{
				Url: seg.linkURL,
			}
		}

		// Always specify all basic formatting fields to ensure proper reset
		fields := []string{"bold", "italic", "underline"}
		if seg.linkURL != "" {
			fields = append(fields, "link")
		}

		requests = append(requests, &docs.Request{
			UpdateTextStyle: &docs.UpdateTextStyleRequest{
				Range: &docs.Range{
					StartIndex: currentIndex,
					EndIndex:   endIndex,
				},
				TextStyle: textStyle,
				Fields:    strings.Join(fields, ","),
			},
		})

		currentIndex = endIndex

		// Check if this is the last segment and close any open list
		if i == len(segments)-1 && currentListStart >= 0 {
			listRanges = append(listRanges, listRange{
				startIndex: currentListStart,
				endIndex:   currentIndex,
				isOrdered:  currentListIsOrdered,
			})
		}
	}

	// Apply list formatting to collected ranges (do this after all text insertion)
	for _, lr := range listRanges {
		bulletPreset := "BULLET_DISC_CIRCLE_SQUARE"
		if lr.isOrdered {
			bulletPreset = "NUMBERED_DECIMAL_ALPHA_ROMAN"
		}
		requests = append(requests, &docs.Request{
			CreateParagraphBullets: &docs.CreateParagraphBulletsRequest{
				Range: &docs.Range{
					StartIndex: lr.startIndex,
					EndIndex:   lr.endIndex,
				},
				BulletPreset: bulletPreset,
			},
		})
	}

	return requests
}

type writeTextParams struct {
	DocumentIDOrURL string `json:"document_id_or_url"`
	Text            string `json:"text"`
	IsMarkdown      bool   `json:"is_markdown"`
	AppendToEnd     bool   `json:"append_to_end"`
	InsertIndex     int64  `json:"insert_index"`
}

func toolWriteText(params, _ json.RawMessage) (any, error) {
	// Initialize with defaults matching plugin.yaml contract
	p := writeTextParams{
		IsMarkdown:  true,
		AppendToEnd: true,
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.DocumentIDOrURL == "" {
		return nil, fmt.Errorf("document_id_or_url is required")
	}
	if p.Text == "" {
		return nil, fmt.Errorf("text is required")
	}

	docID := extractDocumentID(p.DocumentIDOrURL)
	if docID == "" {
		return map[string]string{
			"error": "could not extract document ID from input",
			"input": p.DocumentIDOrURL,
		}, nil
	}

	// Get current document to find the end index
	doc, err := docsSvc.Documents.Get(docID).Do()
	if err != nil {
		return nil, fmt.Errorf("get document: %w", err)
	}

	// Determine insertion index
	insertIndex := p.InsertIndex
	if p.AppendToEnd {
		// Find the end of the document (before the final newline)
		if doc.Body == nil || doc.Body.Content == nil || len(doc.Body.Content) == 0 {
			return nil, fmt.Errorf("document body is empty or invalid")
		}
		insertIndex = doc.Body.Content[len(doc.Body.Content)-1].EndIndex - 1
	}

	var requests []*docs.Request

	if p.IsMarkdown {
		// Parse markdown and convert to requests
		segments := parseMarkdown(p.Text)
		requests = convertMarkdownToRequests(segments, insertIndex)
	} else {
		// Plain text insertion
		requests = []*docs.Request{
			{
				InsertText: &docs.InsertTextRequest{
					Text:     p.Text,
					Location: &docs.Location{Index: insertIndex},
				},
			},
		}
	}

	// Execute the batch update
	batchUpdateReq := &docs.BatchUpdateDocumentRequest{
		Requests: requests,
	}

	resp, err := docsSvc.Documents.BatchUpdate(docID, batchUpdateReq).Do()
	if err != nil {
		return nil, fmt.Errorf("batch update: %w", err)
	}

	return map[string]any{
		"document_id":  docID,
		"title":        doc.Title,
		"status":       "success",
		"insert_index": insertIndex,
		"characters":   len(p.Text),
		"replies":      len(resp.Replies),
	}, nil
}

type writeMarkdownParams struct {
	DocumentIDOrURL string `json:"document_id_or_url"`
	Markdown        string `json:"markdown"`
	AppendToEnd     bool   `json:"append_to_end"`
	InsertIndex     int64  `json:"insert_index"`
}

func toolWriteMarkdown(params, _ json.RawMessage) (any, error) {
	var p writeMarkdownParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.DocumentIDOrURL == "" {
		return nil, fmt.Errorf("document_id_or_url is required")
	}
	if p.Markdown == "" {
		return nil, fmt.Errorf("markdown is required")
	}

	docID := extractDocumentID(p.DocumentIDOrURL)
	if docID == "" {
		return map[string]string{
			"error": "could not extract document ID from input",
			"input": p.DocumentIDOrURL,
		}, nil
	}

	// Get current document to find the end index
	doc, err := docsSvc.Documents.Get(docID).Do()
	if err != nil {
		return nil, fmt.Errorf("get document: %w", err)
	}

	// Determine insertion index
	insertIndex := p.InsertIndex
	if p.AppendToEnd {
		// Find the end of the document (before the final newline)
		if doc.Body == nil || doc.Body.Content == nil || len(doc.Body.Content) == 0 {
			return nil, fmt.Errorf("document body is empty or invalid")
		}
		insertIndex = doc.Body.Content[len(doc.Body.Content)-1].EndIndex - 1
	}

	// Parse markdown and convert to requests
	segments := parseMarkdown(p.Markdown)
	requests := convertMarkdownToRequests(segments, insertIndex)

	// Execute the batch update
	batchUpdateReq := &docs.BatchUpdateDocumentRequest{
		Requests: requests,
	}

	resp, err := docsSvc.Documents.BatchUpdate(docID, batchUpdateReq).Do()
	if err != nil {
		return nil, fmt.Errorf("batch update: %w", err)
	}

	return map[string]any{
		"document_id":  docID,
		"title":        doc.Title,
		"status":       "success",
		"insert_index": insertIndex,
		"characters":   len(p.Markdown),
		"replies":      len(resp.Replies),
	}, nil
}

type createDocumentParams struct {
	Title string `json:"title"`
}

func toolCreateDocument(params, _ json.RawMessage) (any, error) {
	var p createDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.Title == "" {
		return nil, fmt.Errorf("title is required")
	}

	// Create a new document with the specified title
	doc := &docs.Document{
		Title: p.Title,
	}

	createdDoc, err := docsSvc.Documents.Create(doc).Do()
	if err != nil {
		return nil, fmt.Errorf("create document: %w", err)
	}

	// Construct the full Google Docs URL
	documentURL := fmt.Sprintf("https://docs.google.com/document/d/%s/edit", createdDoc.DocumentId)

	return map[string]any{
		"status":      "success",
		"document_id": createdDoc.DocumentId,
		"title":       createdDoc.Title,
		"url":         documentURL,
		"revision_id": createdDoc.RevisionId,
	}, nil
}
