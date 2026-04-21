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

// Pre-compiled regexps used across tool functions.
var (
	reDocURL        = regexp.MustCompile(`/document/d/([A-Za-z0-9_-]+)`)
	reDocIDParam    = regexp.MustCompile(`[?&]id=([A-Za-z0-9_-]+)`)
	reDocIDPlain    = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	reUnsafeChars   = regexp.MustCompile(`[<>:"/\\|?*]`)
	reDocsURL       = regexp.MustCompile(`https?://docs\.google\.com/document/[\w\-/\?=&#%.]+`)
	reOrderedList   = regexp.MustCompile(`^(\s*)\d+\.\s+(.+)$`)
	reUnorderedList = regexp.MustCompile(`^(\s*)([-*+])\s+(.+)$`)
	reDateChip      = regexp.MustCompile(`@date\((\d{4}-\d{2}-\d{2})\)`)
	rePersonChip    = regexp.MustCompile(`@\(([^)]+)\)`)
	reLinkInline    = regexp.MustCompile(`\[([^\]]+)\]\(([^\)]+)\)`)
	// Table detection patterns
	reTableRow       = regexp.MustCompile(`^\s*\|(.+\|)+\s*$`)
	reTableSeparator = regexp.MustCompile(`^\s*\|[\s\-:]*\-[\s\-:]*(\|[\s\-:]*\-[\s\-:]*)*\|\s*$`)
)

// extractDocumentID extracts a Google Docs document ID from a URL.
func extractDocumentID(input string) string {
	if m := reDocURL.FindStringSubmatch(input); len(m) > 1 {
		return m[1]
	}
	if m := reDocIDParam.FindStringSubmatch(input); len(m) > 1 {
		return m[1]
	}
	if reDocIDPlain.MatchString(input) {
		return input
	}
	return ""
}

// saveDocumentFile saves document content to a local file.
// If outputPath is empty, saves to ./docs/<title>.<ext>.
func saveDocumentFile(title, outputPath, content, ext string) (string, error) {
	baseDir := "docs"

	if outputPath == "" {
		safeTitle := reUnsafeChars.ReplaceAllString(title, "_")
		safeTitle = filepath.Base(safeTitle)
		outputPath = filepath.Join(baseDir, safeTitle+ext)
	}

	// Validate that the resolved path stays within the base directory.
	// This must happen before any filesystem side effects.
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve base dir: %w", err)
	}
	absOutput, err := filepath.Abs(outputPath)
	if err != nil {
		return "", fmt.Errorf("resolve output path: %w", err)
	}
	if !strings.HasPrefix(absOutput, absBase+string(os.PathSeparator)) {
		return "", fmt.Errorf("output path escapes base directory: %s", outputPath)
	}

	dir := filepath.Dir(absOutput)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}

	if err := os.WriteFile(absOutput, []byte(content), 0o600); err != nil {
		return "", err
	}

	return absOutput, nil
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

	// Detect code annotations before processing
	annotations := detectCode(doc)

	for i, elem := range doc.Body.Content {
		extractElementMarkdown(&md, elem, annotations, i)
	}

	return md.String()
}

// extractElementMarkdown recursively converts document elements to Markdown.
func extractElementMarkdown(sb *strings.Builder, elem *docs.StructuralElement, annotations *CodeAnnotations, paraIndex int) {
	if elem.Paragraph != nil {
		para := elem.Paragraph

		// Check if this is a code block
		if annotations.CodeBlocks[paraIndex] {
			// Extract plain text from code block (strip all formatting)
			var codeText strings.Builder
			for _, pe := range para.Elements {
				if pe.TextRun != nil {
					codeText.WriteString(pe.TextRun.Content)
				}
			}

			text := codeText.String()
			if text != "" {
				sb.WriteString("```\n")
				sb.WriteString(text)
				// Ensure code block ends with newline before closing fence
				if !strings.HasSuffix(text, "\n") {
					sb.WriteString("\n")
				}
				sb.WriteString("```\n\n")
			}
			return
		}

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
		for runIndex, pe := range para.Elements {
			if pe.TextRun != nil {
				text := pe.TextRun.Content
				style := pe.TextRun.TextStyle

				// Check if this run is inline code
				isInline := annotations.InlineCode[makeInlineCodeKey(paraIndex, runIndex)]
				if isInline {
					// Inline code: wrap in single backticks
					text = "`" + strings.TrimSpace(text) + "`"
				}
				if style != nil {
					// Apply formatting (works for both inline code and regular text)
					if !isInline {
						// Only trim for non-inline-code (inline code already trimmed above)
						if style.Bold {
							text = "**" + strings.TrimSpace(text) + "**"
						}
						if style.Italic {
							text = "*" + strings.TrimSpace(text) + "*"
						}
						if style.Underline {
							text = "__" + strings.TrimSpace(text) + "__"
						}
					} else {
						// For inline code, apply formatting INSIDE the backticks
						// Strip backticks, apply formatting, re-wrap
						inner := text[1 : len(text)-1] // remove surrounding backticks
						if style.Bold {
							inner = "**" + inner + "**"
						}
						if style.Italic {
							inner = "*" + inner + "*"
						}
						if style.Underline && (style.Link == nil || style.Link.Url == "") {
							inner = "__" + inner + "__"
						}
						text = "`" + inner + "`"
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
	var paragraphs, headings, lists, tables int
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
	if runes := []rune(preview); len(runes) > 500 {
		preview = string(runes[:500]) + "..."
	}

	summary["stats"] = map[string]int{
		"paragraphs": paragraphs,
		"headings":   headings,
		"lists":      lists,
		"tables":     tables,
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

	urls := reDocsURL.FindAllString(p.Text, -1)

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
	strikethrough     bool
	linkURL           string
	heading           int    // 0 for normal, 1-6 for heading levels
	headingLineID     int    // unique ID per heading line, used to detect paragraph boundaries
	orderedListItem   bool   // true if this is an ordered list item
	unorderedListItem bool   // true if this is an unordered list item
	listDepth         int    // nesting level: 0=top-level list item, 1=first nested, etc.
	isDateField       bool   // true if this should be a date field (@today or @date(YYYY-MM-DD))
	dateValue         string // specific date in YYYY-MM-DD format, empty means @today
	isPersonField     bool   // true if this should be a person field (@(name or email))
	personIdentifier  string // name or email for person field
	isInlineCode      bool   // true if text should be wrapped in single backticks
	isCodeBlock       bool   // true if text is part of a code block (triple backticks)
	codeLanguage      string // language identifier for code block (e.g., ```language syntax)
	// Table support
	isTable bool
	table   *tableSegment // Non-nil when isTable=true
}

// tableCell represents a single cell in a markdown table.
type tableCell struct {
	segments []markdownSegment // Cell content as formatted segments
}

// tableRow represents a single row in a markdown table.
type tableRow struct {
	cells []tableCell
}

// tableSegment represents a parsed markdown table.
type tableSegment struct {
	rows       []tableRow
	numColumns int
	isTable    bool // Always true, used for type discrimination
}

// CodeAnnotations holds code detection results for a document.
type CodeAnnotations struct {
	// Maps "paragraphIndex:textRunIndex" to inline code status
	InlineCode map[string]bool

	// Maps paragraph index to code block status
	CodeBlocks map[int]bool

	// Maps paragraph index to language hint (future use)
	Languages map[int]string
}

// makeInlineCodeKey creates a composite key for inline code annotation.
func makeInlineCodeKey(paraIndex, runIndex int) string {
	return fmt.Sprintf("%d:%d", paraIndex, runIndex)
}

// monospaceFontSet contains lowercase monospace font names for efficient lookup.
var monospaceFontSet = func() map[string]bool {
	fonts := []string{"courier new", "courier", "consolas", "monaco", "menlo",
		"source code pro", "sf mono", "inconsolata", "roboto mono"}
	m := make(map[string]bool, len(fonts))
	for _, f := range fonts {
		m[f] = true
	}
	return m
}()

// isMonospaceFont checks if the given font family is a monospace font.
// Comparison is case-insensitive.
func isMonospaceFont(fontFamily string) bool {
	if fontFamily == "" {
		return false
	}
	return monospaceFontSet[strings.ToLower(fontFamily)]
}

// detectCode analyzes a Google Docs document and identifies code blocks and
// inline code based on monospace font usage.
//
// A paragraph where ALL text runs use monospace fonts is marked as a code block.
// A paragraph where SOME text runs use monospace fonts has those runs marked as
// inline code. Headings (HEADING_1..6, TITLE, SUBTITLE) are skipped even if
// they use monospace fonts, because heading formatting takes precedence.
func detectCode(doc *docs.Document) *CodeAnnotations {
	annotations := &CodeAnnotations{
		InlineCode: make(map[string]bool),
		CodeBlocks: make(map[int]bool),
		Languages:  make(map[int]string),
	}

	if doc.Body == nil || doc.Body.Content == nil {
		return annotations
	}

	for i, elem := range doc.Body.Content {
		if elem.Paragraph == nil {
			continue
		}
		para := elem.Paragraph

		// Skip headings: heading formatting takes precedence over code detection.
		if para.ParagraphStyle != nil {
			switch para.ParagraphStyle.NamedStyleType {
			case "HEADING_1", "HEADING_2", "HEADING_3",
				"HEADING_4", "HEADING_5", "HEADING_6",
				"TITLE", "SUBTITLE":
				continue
			}
		}

		// Count monospace vs total text runs.
		totalRuns := 0
		monoRuns := 0
		monoIndices := []int{}

		for j, pe := range para.Elements {
			if pe.TextRun == nil {
				continue
			}
			// Skip runs that are only whitespace (e.g., trailing newlines).
			content := strings.TrimSpace(pe.TextRun.Content)
			if content == "" {
				continue
			}
			totalRuns++
			if pe.TextRun.TextStyle != nil &&
				pe.TextRun.TextStyle.WeightedFontFamily != nil &&
				isMonospaceFont(pe.TextRun.TextStyle.WeightedFontFamily.FontFamily) {
				monoRuns++
				monoIndices = append(monoIndices, j)
			}
		}

		if totalRuns == 0 {
			continue
		}

		if monoRuns == totalRuns {
			// All text runs are monospace: mark as code block.
			annotations.CodeBlocks[i] = true
		} else if monoRuns > 0 {
			// Some text runs are monospace: mark specific runs as inline code.
			for _, j := range monoIndices {
				annotations.InlineCode[makeInlineCodeKey(i, j)] = true
			}
		}
	}

	return annotations
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
	// Normalize line endings
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")
	markdown = strings.ReplaceAll(markdown, "\r", "\n")

	var segments []markdownSegment
	lines := strings.Split(markdown, "\n")

	lastWasHeading := false
	nextHeadingLineID := 1

	// Code block state tracking
	inCodeBlock := false
	var codeBlockLines []string
	var codeLanguage string

	// Table buffering state
	var tableBuffer []string
	inTable := false

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)

		// Check for code fence boundaries (``` with optional language)
		if strings.HasPrefix(trimmedLine, "```") {
			// Close any open table before code block
			if inTable {
				inTable = false
				if table := parseMarkdownTable(tableBuffer); table != nil {
					segments = append(segments, markdownSegment{
						isTable: true,
						table:   table,
					})
				} else {
					for _, bufferedLine := range tableBuffer {
						segments = append(segments, parseSimpleFormatting(bufferedLine+"\n")...)
					}
				}
				tableBuffer = nil
			}

			if !inCodeBlock {
				// Opening fence: start accumulating code block lines
				inCodeBlock = true
				codeBlockLines = nil
				codeLanguage = strings.TrimSpace(trimmedLine[3:])
				if strings.ContainsAny(codeLanguage, " \t") {
					codeLanguage = strings.Fields(codeLanguage)[0]
				}
				lastWasHeading = false
				continue
			}
			// Closing fence: emit accumulated code block as a segment
			inCodeBlock = false
			if len(codeBlockLines) > 0 {
				codeText := strings.Join(codeBlockLines, "\n") + "\n"
				segments = append(segments, markdownSegment{
					text:         codeText,
					isCodeBlock:  true,
					codeLanguage: codeLanguage,
				})
			}
			codeBlockLines = nil
			codeLanguage = ""
			continue
		}

		// If inside a code block, accumulate lines verbatim
		if inCodeBlock {
			codeBlockLines = append(codeBlockLines, line)
			continue
		}

		// Check if this line looks like a table row
		isTableRow := reTableRow.MatchString(line)

		if isTableRow {
			// Start or continue buffering table lines
			if !inTable {
				inTable = true
				tableBuffer = []string{}
			}
			tableBuffer = append(tableBuffer, line)
			continue
		}

		// Not a table row - process any buffered table
		if inTable {
			inTable = false
			// Try to parse buffered lines as a table
			if table := parseMarkdownTable(tableBuffer); table != nil {
				// Valid table
				segments = append(segments, markdownSegment{
					isTable: true,
					table:   table,
				})
			} else {
				// Invalid table - treat as plain text paragraphs
				for _, bufferedLine := range tableBuffer {
					segments = append(segments, parseSimpleFormatting(bufferedLine+"\n")...)
				}
			}
			tableBuffer = nil
		}

		// Process non-table line normally
		if trimmedLine == "" {
			if lastWasHeading {
				// Skip blank lines immediately after headings
				continue
			}
			// Preserve blank lines between normal text as empty paragraphs
			segments = append(segments, markdownSegment{text: "\n"})
			continue
		}

		// Check for headings
		headingLevel := 0
		if strings.HasPrefix(trimmedLine, "#") {
		loop:
			for j, ch := range trimmedLine {
				switch ch {
				case '#':
					headingLevel++
				case ' ':
					trimmedLine = trimmedLine[j+1:]
					break loop
				default:
					headingLevel = 0
					break loop
				}
			}
		}

		if headingLevel > 0 && headingLevel <= 6 {
			lastWasHeading = true
			inlineSegs := parseSimpleFormatting(trimmedLine)
			lineID := nextHeadingLineID
			nextHeadingLineID++
			for j := range inlineSegs {
				inlineSegs[j].heading = headingLevel
				inlineSegs[j].headingLineID = lineID
			}
			segments = append(segments, inlineSegs...)
			continue
		}

		lastWasHeading = false

		// Check for ordered list
		if orderedListMatch := reOrderedList.FindStringSubmatch(line); orderedListMatch != nil {
			depth := indentDepth(orderedListMatch[1])
			listItemText := orderedListMatch[2]
			inlineSegs := parseSimpleFormatting(listItemText + "\n")
			for j := range inlineSegs {
				inlineSegs[j].orderedListItem = true
				inlineSegs[j].listDepth = depth
			}
			segments = append(segments, inlineSegs...)
			continue
		}

		// Check for unordered list
		if unorderedListMatch := reUnorderedList.FindStringSubmatch(line); unorderedListMatch != nil {
			depth := indentDepth(unorderedListMatch[1])
			listItemText := unorderedListMatch[3]
			inlineSegs := parseSimpleFormatting(listItemText + "\n")
			for j := range inlineSegs {
				inlineSegs[j].unorderedListItem = true
				inlineSegs[j].listDepth = depth
			}
			segments = append(segments, inlineSegs...)
			continue
		}

		// Parse inline formatting
		segments = append(segments, parseSimpleFormatting(line+"\n")...)
	}

	// Handle unclosed code fence: emit accumulated content as code block
	if inCodeBlock && len(codeBlockLines) > 0 {
		codeText := strings.Join(codeBlockLines, "\n") + "\n"
		segments = append(segments, markdownSegment{
			text:         codeText,
			isCodeBlock:  true,
			codeLanguage: codeLanguage,
		})
	}

	// Process any remaining buffered table at end of document
	if inTable {
		if table := parseMarkdownTable(tableBuffer); table != nil {
			segments = append(segments, markdownSegment{
				isTable: true,
				table:   table,
			})
		} else {
			for _, bufferedLine := range tableBuffer {
				segments = append(segments, parseSimpleFormatting(bufferedLine+"\n")...)
			}
		}
	}

	return segments
}

// parseMarkdownTable parses a markdown table from buffered lines.
// Returns nil if the lines don't form a valid table.
func parseMarkdownTable(lines []string) *tableSegment {
	// Minimum: separator + at least one data row
	if len(lines) < 2 {
		return nil
	}

	var rows []tableRow
	var numColumns int
	var dataStartIdx int

	// Check if first line is a separator (table without header)
	if reTableSeparator.MatchString(lines[0]) {
		// Table without header - get column count from separator
		separatorCells := splitTableRow(lines[0])
		numColumns = len(separatorCells)
		dataStartIdx = 1
	} else {
		// Table with header - second line should be separator
		if len(lines) < 3 {
			return nil
		}
		if !reTableSeparator.MatchString(lines[1]) {
			return nil
		}

		// Parse header row
		headerCells := splitTableRow(lines[0])
		if len(headerCells) == 0 {
			return nil
		}
		numColumns = len(headerCells)

		// Add header row with bold formatting
		headerRow := tableRow{cells: make([]tableCell, 0, numColumns)}
		for _, cellText := range headerCells {
			segments := parseSimpleFormatting(strings.TrimSpace(cellText))
			// Make all header segments bold
			for i := range segments {
				segments[i].bold = true
			}
			segments = mergeSegments(segments)
			headerRow.cells = append(headerRow.cells, tableCell{segments: segments})
		}
		rows = append(rows, headerRow)
		dataStartIdx = 2
	}

	// Parse data rows
	for i := dataStartIdx; i < len(lines); i++ {
		cells := splitTableRow(lines[i])
		if len(cells) != numColumns {
			// Inconsistent column count
			return nil
		}

		dataRow := tableRow{cells: make([]tableCell, 0, numColumns)}
		for _, cellText := range cells {
			segments := parseSimpleFormatting(strings.TrimSpace(cellText))
			segments = mergeSegments(segments)
			dataRow.cells = append(dataRow.cells, tableCell{segments: segments})
		}
		rows = append(rows, dataRow)
	}

	return &tableSegment{
		rows:       rows,
		numColumns: numColumns,
		isTable:    true,
	}
}

// splitTableRow splits a table row by pipe delimiters.
// Returns cell contents without the leading/trailing pipes.
func splitTableRow(line string) []string {
	// Remove leading/trailing whitespace
	line = strings.TrimSpace(line)

	// Remove leading and trailing pipes
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")

	// Split by pipe
	parts := strings.Split(line, "|")

	// Trim whitespace from each part
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, strings.TrimSpace(part))
	}

	return cells
}

// parseSimpleFormatting parses bold, italic, underline, and strikethrough formatting.
func parseSimpleFormatting(text string) []markdownSegment {
	return parseSimpleFormattingWithDepth(text, 0)
}

// parseSimpleFormattingWithDepth is the recursive implementation of parseSimpleFormatting
// with a depth limit to prevent stack overflow from crafted input.
func parseSimpleFormattingWithDepth(text string, depth int) []markdownSegment {
	if depth > 10 {
		return []markdownSegment{{text: text}}
	}

	var segments []markdownSegment
	pos := 0

	for pos < len(text) {
		// Check for `inline code` (single backtick, not triple)
		if text[pos] == '`' && !strings.HasPrefix(text[pos:], "```") {
			endPos := strings.Index(text[pos+1:], "`")
			if endPos != -1 {
				endPos += pos + 1
				// Parse formatting INSIDE backticks
				codeText := text[pos+1 : endPos]
				innerSegs := parseSimpleFormattingWithDepth(codeText, depth+1)
				// Mark ALL inner segments as inline code
				for i := range innerSegs {
					innerSegs[i].isInlineCode = true
				}
				segments = append(segments, innerSegs...)
				pos = endPos + 1
				continue
			}
		}

		// Check for ~~strikethrough~~
		if strings.HasPrefix(text[pos:], "~~") {
			endPos := strings.Index(text[pos+2:], "~~")
			if endPos != -1 {
				endPos += pos + 2
				innerSegs := parseSimpleFormattingWithDepth(text[pos+2:endPos], depth+1)
				for i := range innerSegs {
					innerSegs[i].strikethrough = true
				}
				segments = append(segments, innerSegs...)
				pos = endPos + 2
				continue
			}
		}

		// Check for **bold**
		if strings.HasPrefix(text[pos:], "**") {
			endPos := strings.Index(text[pos+2:], "**")
			if endPos != -1 {
				endPos += pos + 2
				innerSegs := parseSimpleFormattingWithDepth(text[pos+2:endPos], depth+1)
				for i := range innerSegs {
					innerSegs[i].bold = true
				}
				segments = append(segments, innerSegs...)
				pos = endPos + 2
				continue
			}
		}

		// Check for __underline__
		if strings.HasPrefix(text[pos:], "__") {
			endPos := strings.Index(text[pos+2:], "__")
			if endPos != -1 {
				endPos += pos + 2
				innerSegs := parseSimpleFormattingWithDepth(text[pos+2:endPos], depth+1)
				for i := range innerSegs {
					innerSegs[i].underline = true
				}
				segments = append(segments, innerSegs...)
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
				innerSegs := parseSimpleFormattingWithDepth(text[pos+1:endPos], depth+1)
				for i := range innerSegs {
					innerSegs[i].italic = true
				}
				segments = append(segments, innerSegs...)
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
				innerSegs := parseSimpleFormattingWithDepth(text[pos+1:endPos], depth+1)
				for i := range innerSegs {
					innerSegs[i].italic = true
				}
				segments = append(segments, innerSegs...)
				pos = endPos + 1
				continue
			}
		}

		// Check for @date(YYYY-MM-DD) - must come before @today check
		if dateMatch := reDateChip.FindStringSubmatchIndex(text[pos:]); dateMatch != nil && dateMatch[0] == 0 {
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
		if personMatch := rePersonChip.FindStringSubmatchIndex(text[pos:]); personMatch != nil && personMatch[0] == 0 {
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
		if linkMatch := reLinkInline.FindStringSubmatchIndex(text[pos:]); linkMatch != nil && linkMatch[0] == 0 {
			linkText := text[pos+linkMatch[2] : pos+linkMatch[3]]
			linkURL := text[pos+linkMatch[4] : pos+linkMatch[5]]
			if isAllowedLinkScheme(linkURL) {
				segments = append(segments, markdownSegment{
					text:    linkText,
					linkURL: linkURL,
				})
				pos += linkMatch[1]
				continue
			}
			// Rejected scheme — emit opening bracket as plain text and
			// let the rest be parsed normally on subsequent iterations.
		}

		// Plain character
		segments = append(segments, markdownSegment{text: text[pos : pos+1]})
		pos++
	}

	return segments
}

// isAllowedLinkScheme checks whether a URL has an allowed scheme.
// Only https, http, and mailto are permitted. Comparison is case-insensitive
// per RFC 3986 §3.1.
func isAllowedLinkScheme(rawURL string) bool {
	u := strings.TrimSpace(rawURL)
	lower := strings.ToLower(u)
	return strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "mailto:")
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

		// Never merge segments from different heading lines — each heading
		// line is a separate paragraph and must remain a distinct segment.
		if (curr.heading > 0 || prev.heading > 0) &&
			curr.headingLineID != prev.headingLineID {
			merged = append(merged, curr)
			continue
		}

		// Check if segments can be merged (same formatting, not special fields)
		if curr.bold == prev.bold &&
			curr.italic == prev.italic &&
			curr.underline == prev.underline &&
			curr.strikethrough == prev.strikethrough &&
			curr.linkURL == prev.linkURL &&
			curr.heading == prev.heading &&
			curr.headingLineID == prev.headingLineID &&
			curr.orderedListItem == prev.orderedListItem &&
			curr.unorderedListItem == prev.unorderedListItem &&
			curr.listDepth == prev.listDepth &&
			!curr.isDateField && !prev.isDateField &&
			!curr.isPersonField && !prev.isPersonField &&
			curr.isInlineCode == prev.isInlineCode &&
			curr.isCodeBlock == prev.isCodeBlock &&
			curr.codeLanguage == prev.codeLanguage &&
			!curr.isTable && !prev.isTable {
			// Merge by concatenating text
			prev.text += curr.text
		} else {
			// Cannot merge, append as new segment
			merged = append(merged, curr)
		}
	}

	return merged
}

// convertTableToRequests converts a table segment to Google Docs API requests.
// convertTableToRequests creates just the empty table structure.
// Cell content must be populated in a separate batch after querying the document.
func convertTableToRequests(table *tableSegment, startIndex int64) []*docs.Request {
	return []*docs.Request{
		{
			InsertTable: &docs.InsertTableRequest{
				Rows:     int64(len(table.rows)),
				Columns:  int64(table.numColumns),
				Location: &docs.Location{Index: startIndex},
			},
		},
	}
}

// insertMarkdownWithTables handles insertion of markdown segments that may contain tables.
// It properly creates and populates tables using the multi-phase approach:
// 1. Create empty table structure
// 2. Query document to get cell indices
// 3. Populate all cells in a single batch
// Returns a result map with document info and status.
func insertMarkdownWithTables(docID string, segments []markdownSegment, insertIndex int64) (map[string]any, error) {
	// Get the document to retrieve title
	doc, err := docsSvc.Documents.Get(docID).Do()
	if err != nil {
		return nil, fmt.Errorf("get document: %w", err)
	}

	// Check if there are any table segments
	hasTable := false
	var tableSegments []*tableSegment
	for _, seg := range segments {
		if seg.isTable && seg.table != nil {
			hasTable = true
			tableSegments = append(tableSegments, seg.table)
		}
	}

	if hasTable {
		// Process each table completely (create + populate) before moving to next table
		// This avoids index confusion when multiple tables are present
		currentIndex := insertIndex
		tableIdx := 0

		for _, seg := range segments {
			if seg.isTable && seg.table != nil {
				// Create this table structure
				tableRequests := convertTableToRequests(seg.table, currentIndex)
				batchUpdateReq := &docs.BatchUpdateDocumentRequest{Requests: tableRequests}
				_, err := docsSvc.Documents.BatchUpdate(docID, batchUpdateReq).Do()
				if err != nil {
					return nil, fmt.Errorf("create table %d: %w", tableIdx, err)
				}

				// Query document to get the table we just created
				doc, err = docsSvc.Documents.Get(docID).Do()
				if err != nil {
					return nil, fmt.Errorf("get document after creating table %d: %w", tableIdx, err)
				}
				if doc.Body == nil {
					return nil, fmt.Errorf("document body is nil after creating table %d", tableIdx)
				}

				// Find the table we just created (it's the last table at or after currentIndex)
				var elem *docs.StructuralElement
				for _, e := range doc.Body.Content {
					if e.Table != nil && e.StartIndex >= currentIndex {
						elem = e
						break
					}
				}

				if elem == nil || elem.Table == nil {
					return nil, fmt.Errorf("table %d not found after creation", tableIdx)
				}

				// Validate table dimensions match what was requested
				expectedRows := len(seg.table.rows)
				actualRows := len(elem.Table.TableRows)
				if actualRows < expectedRows {
					return nil, fmt.Errorf("table %d dimension mismatch: requested %d rows, API returned %d", tableIdx, expectedRows, actualRows)
				}

				// Populate cells one-by-one with a re-query after each cell.
				// In a freshly created empty table, even cells within the same row have
				// overlapping Content[0].StartIndex values. We must process cells sequentially,
				// re-querying after each to get updated indices for the next cell.
				for rowIdx, row := range seg.table.rows {
					for colIdx, cell := range row.cells {
						// Re-validate table structure on each iteration
						if rowIdx >= len(elem.Table.TableRows) {
							return nil, fmt.Errorf("table %d row %d: index out of bounds (table has %d rows)", tableIdx, rowIdx, len(elem.Table.TableRows))
						}
						tableRow := elem.Table.TableRows[rowIdx]

						if colIdx >= len(tableRow.TableCells) {
							return nil, fmt.Errorf("table %d cell[%d,%d]: index out of bounds (row has %d columns)", tableIdx, rowIdx, colIdx, len(tableRow.TableCells))
						}
						tableCell := tableRow.TableCells[colIdx]

						if len(tableCell.Content) == 0 {
							continue
						}

						cellStartIndex := tableCell.Content[0].StartIndex
						cellReqs := populateTableCell(&cell, cellStartIndex)

						// Execute this cell's requests
						if len(cellReqs) > 0 {
							batchUpdateReq := &docs.BatchUpdateDocumentRequest{Requests: cellReqs}
							_, err = docsSvc.Documents.BatchUpdate(docID, batchUpdateReq).Do()
							if err != nil {
								return nil, fmt.Errorf("populate table %d cell[%d,%d]: %w", tableIdx, rowIdx, colIdx, err)
							}

							// Re-query to get updated table structure for next cell.
							// Skip if this is the last cell to save one API call.
							isLastCell := (rowIdx == len(seg.table.rows)-1) && (colIdx == len(row.cells)-1)
							if !isLastCell {
								doc, err = docsSvc.Documents.Get(docID).Do()
								if err != nil {
									return nil, fmt.Errorf("get document after table %d cell[%d,%d]: %w", tableIdx, rowIdx, colIdx, err)
								}
								if doc.Body == nil {
									return nil, fmt.Errorf("document body is nil after table %d cell[%d,%d]", tableIdx, rowIdx, colIdx)
								}

								// Re-find the table for next iteration
								elem = nil
								for _, e := range doc.Body.Content {
									if e.Table != nil && e.StartIndex >= currentIndex {
										elem = e
										break
									}
								}
								if elem == nil || elem.Table == nil {
									return nil, fmt.Errorf("table %d not found after populating cell[%d,%d]", tableIdx, rowIdx, colIdx)
								}
							}
						}
					}
				}

				// Update currentIndex to after this table for next segment
				// Re-query one more time to get final table structure
				doc, err = docsSvc.Documents.Get(docID).Do()
				if err != nil {
					return nil, fmt.Errorf("get document after completing table %d: %w", tableIdx, err)
				}
				if doc.Body == nil {
					return nil, fmt.Errorf("document body is nil after completing table %d", tableIdx)
				}

				// Reset elem to nil before searching to avoid retaining stale value
				elem = nil
				for _, e := range doc.Body.Content {
					if e.Table != nil && e.StartIndex >= currentIndex {
						elem = e
						currentIndex = e.EndIndex
						break
					}
				}

				// Validate we found the table
				if elem == nil {
					return nil, fmt.Errorf("table %d not found after populating cells", tableIdx)
				}

				tableIdx++
			} else {
				// Handle non-table segments
				segRequests := convertMarkdownToRequests([]markdownSegment{seg}, currentIndex)
				if len(segRequests) > 0 {
					batchUpdateReq := &docs.BatchUpdateDocumentRequest{Requests: segRequests}
					_, err := docsSvc.Documents.BatchUpdate(docID, batchUpdateReq).Do()
					if err != nil {
						return nil, fmt.Errorf("insert content: %w", err)
					}

					// Update currentIndex - track all content insertions
					for _, req := range segRequests {
						switch {
						case req.InsertText != nil:
							currentIndex += int64(utf8.RuneCountInString(req.InsertText.Text))
						case req.InsertDate != nil:
							// Date chips take 1 character in the index
							currentIndex++
						case req.InsertPerson != nil:
							// Person chips take 1 character in the index
							currentIndex++
						}
					}
				}
			}
		}

		return map[string]any{
			"document_id":  docID,
			"title":        doc.Title,
			"status":       "success",
			"insert_index": insertIndex,
			"tables":       len(tableSegments),
		}, nil
	}

	// No tables - use single-batch insertion via convertMarkdownToRequests
	requests := convertMarkdownToRequests(segments, insertIndex)
	batchUpdateReq := &docs.BatchUpdateDocumentRequest{Requests: requests}
	_, err = docsSvc.Documents.BatchUpdate(docID, batchUpdateReq).Do()
	if err != nil {
		return nil, fmt.Errorf("batch update: %w", err)
	}

	return map[string]any{
		"document_id":  docID,
		"title":        doc.Title,
		"status":       "success",
		"insert_index": insertIndex,
	}, nil
}

// populateTableCell creates requests to populate a single table cell with content.
func populateTableCell(cell *tableCell, cellStartIndex int64) []*docs.Request {
	var requests []*docs.Request

	// Skip empty cells - Google Docs already creates cells with an empty paragraph containing \n
	if len(cell.segments) == 0 || (len(cell.segments) == 1 && cell.segments[0].text == "") {
		return requests
	}

	// Table cells are created with an empty paragraph containing just '\n' at cellStartIndex.
	// Insert content at cellStartIndex, which pushes the newline forward.
	// This places content inside the cell paragraph before the newline.
	currentIndex := cellStartIndex
	for _, seg := range cell.segments {
		if seg.text == "" && !seg.isDateField && !seg.isPersonField {
			continue
		}

		// Handle date chips
		if seg.isDateField {
			// Parse the date value and create RFC3339 timestamp
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

			// Insert the date chip
			requests = append(requests, &docs.Request{
				InsertDate: &docs.InsertDateRequest{
					Location: &docs.Location{Index: currentIndex},
					DateElementProperties: &docs.DateElementProperties{
						Timestamp:  timestamp,
						DateFormat: "DATE_FORMAT_MONTH_DAY_YEAR_ABBREVIATED",
					},
				},
			})

			// Date elements take 1 character in the document index
			currentIndex++

			// Apply text formatting to the date chip if needed
			if seg.bold || seg.italic || seg.underline || seg.strikethrough {
				textStyle := &docs.TextStyle{
					Bold:          seg.bold,
					Italic:        seg.italic,
					Underline:     seg.underline,
					Strikethrough: seg.strikethrough,
				}

				fields := []string{"bold", "italic", "underline", "strikethrough"}

				requests = append(requests, &docs.Request{
					UpdateTextStyle: &docs.UpdateTextStyleRequest{
						Range: &docs.Range{
							StartIndex: currentIndex - 1,
							EndIndex:   currentIndex,
						},
						TextStyle: textStyle,
						Fields:    strings.Join(fields, ","),
					},
				})
			}

			continue
		}

		// Handle person chips
		if seg.isPersonField {
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
			currentIndex++

			// Apply text formatting to the person chip if needed
			if seg.bold || seg.italic || seg.underline || seg.strikethrough {
				textStyle := &docs.TextStyle{
					Bold:          seg.bold,
					Italic:        seg.italic,
					Underline:     seg.underline,
					Strikethrough: seg.strikethrough,
				}

				fields := []string{"bold", "italic", "underline", "strikethrough"}

				requests = append(requests, &docs.Request{
					UpdateTextStyle: &docs.UpdateTextStyleRequest{
						Range: &docs.Range{
							StartIndex: currentIndex - 1,
							EndIndex:   currentIndex,
						},
						TextStyle: textStyle,
						Fields:    strings.Join(fields, ","),
					},
				})
			}

			continue
		}

		// Insert regular text
		requests = append(requests, &docs.Request{
			InsertText: &docs.InsertTextRequest{
				Text:     seg.text,
				Location: &docs.Location{Index: currentIndex},
			},
		})

		segLength := int64(utf8.RuneCountInString(seg.text))
		segEndIndex := currentIndex + segLength

		// Apply text formatting if needed
		if seg.bold || seg.italic || seg.underline || seg.strikethrough || seg.linkURL != "" {
			textStyle := &docs.TextStyle{
				Bold:          seg.bold,
				Italic:        seg.italic,
				Underline:     seg.underline,
				Strikethrough: seg.strikethrough,
			}

			if seg.linkURL != "" {
				textStyle.Link = &docs.Link{Url: seg.linkURL}
			}

			// Build fields list
			fields := []string{"bold", "italic", "underline", "strikethrough"}
			if seg.linkURL != "" {
				fields = append(fields, "link")
			}

			requests = append(requests, &docs.Request{
				UpdateTextStyle: &docs.UpdateTextStyleRequest{
					Range: &docs.Range{
						StartIndex: currentIndex,
						EndIndex:   segEndIndex,
					},
					TextStyle: textStyle,
					Fields:    strings.Join(fields, ","),
				},
			})
		}

		currentIndex = segEndIndex
	}

	return requests
}

// convertMarkdownToRequests converts markdown segments to Google Docs API requests.
func convertMarkdownToRequests(segments []markdownSegment, startIndex int64) []*docs.Request {
	var requests []*docs.Request
	currentIndex := startIndex

	// Merge consecutive segments with identical formatting to reduce API requests
	segments = mergeSegments(segments)

	// Remove trailing \n from the last text segment.
	// The document already ends with \n, so our trailing \n
	// would create an unwanted empty paragraph.
	for i := len(segments) - 1; i >= 0; i-- {
		if segments[i].text != "" && !segments[i].isDateField && !segments[i].isPersonField {
			segments[i].text = strings.TrimSuffix(segments[i].text, "\n")
			break
		}
	}

	// Track list ranges for batch processing
	type listRange struct {
		startIndex int64
		endIndex   int64
		isOrdered  bool
	}
	var listRanges []listRange
	var currentListStart int64 = -1
	var currentListIsOrdered bool

	// Track text style requests for headings - we defer them until after UpdateParagraphStyle
	// to prevent the paragraph style from wiping out the text formatting
	var headingTextStyleRequests []*docs.Request
	var inHeading bool
	var currentHeadingLineID int

	for i, seg := range segments {
		// Table segments should never reach this function - they're handled by insertMarkdownWithTables.
		// If we encounter one, it's a programming error.
		if seg.isTable && seg.table != nil {
			// This should never happen - insertMarkdownWithTables handles all table processing.
			// If we get here, it means the caller didn't properly filter table segments.
			panic("convertMarkdownToRequests called with table segment - tables must be handled by insertMarkdownWithTables")
		}

		if seg.text == "" && !seg.isDateField && !seg.isPersonField {
			continue
		}

		// Handle code blocks (triple backticks) - insert text then apply Courier New font
		if seg.isCodeBlock {
			// Close any open list before code block
			if currentListStart >= 0 {
				listRanges = append(listRanges, listRange{
					startIndex: currentListStart,
					endIndex:   currentIndex,
					isOrdered:  currentListIsOrdered,
				})
				currentListStart = -1
			}

			insertText := seg.text
			// Append \n if this is not the last segment and text doesn't already end with \n
			if i < len(segments)-1 && !strings.HasSuffix(insertText, "\n") {
				insertText += "\n"
			}

			requests = append(requests, &docs.Request{
				InsertText: &docs.InsertTextRequest{
					Text:     insertText,
					Location: &docs.Location{Index: currentIndex},
				},
			})

			endIndex := currentIndex + int64(utf8.RuneCountInString(insertText))

			// Apply Courier New font and clear formatting flags
			requests = append(requests, &docs.Request{
				UpdateTextStyle: &docs.UpdateTextStyleRequest{
					Range: &docs.Range{
						StartIndex: currentIndex,
						EndIndex:   endIndex,
					},
					TextStyle: &docs.TextStyle{
						WeightedFontFamily: &docs.WeightedFontFamily{
							FontFamily: "Courier New",
						},
						Bold:          false,
						Italic:        false,
						Underline:     false,
						Strikethrough: false,
					},
					Fields: "weightedFontFamily,bold,italic,underline,strikethrough",
				},
			})

			// Set paragraph style to NORMAL_TEXT
			requests = append(requests, &docs.Request{
				UpdateParagraphStyle: &docs.UpdateParagraphStyleRequest{
					ParagraphStyle: &docs.ParagraphStyle{
						NamedStyleType: "NORMAL_TEXT",
					},
					Range: &docs.Range{
						StartIndex: currentIndex,
						EndIndex:   endIndex,
					},
					Fields: "namedStyleType",
				},
			})

			currentIndex = endIndex
			continue
		}

		// Detect start of a new heading paragraph using headingLineID.
		// Each heading line in the source gets a unique ID assigned in
		// parseMarkdown, so consecutive same-level headings like "# A\n# B"
		// are correctly identified as separate paragraphs.
		if seg.heading > 0 && seg.headingLineID != currentHeadingLineID {
			inHeading = true
			currentHeadingLineID = seg.headingLineID
			headingTextStyleRequests = nil
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

			// Apply paragraph style (heading or normal text)
			// Always apply to prevent heading styles from persisting
			var paragraphStyle string
			if seg.heading > 0 {
				paragraphStyle = fmt.Sprintf("HEADING_%d", seg.heading)
			} else {
				paragraphStyle = "NORMAL_TEXT"
			}
			requests = append(requests, &docs.Request{
				UpdateParagraphStyle: &docs.UpdateParagraphStyleRequest{
					Range: &docs.Range{
						StartIndex: currentIndex - 1,
						EndIndex:   currentIndex,
					},
					ParagraphStyle: &docs.ParagraphStyle{
						NamedStyleType: paragraphStyle,
					},
					Fields: "namedStyleType",
				},
			})

			// Apply text formatting (bold, italic, underline, strikethrough) to the date chip
			if seg.bold || seg.italic || seg.underline || seg.strikethrough {
				textStyle := &docs.TextStyle{
					Bold:          seg.bold,
					Italic:        seg.italic,
					Underline:     seg.underline,
					Strikethrough: seg.strikethrough,
				}
				fields := []string{"bold", "italic", "underline", "strikethrough"}

				requests = append(requests, &docs.Request{
					UpdateTextStyle: &docs.UpdateTextStyleRequest{
						Range: &docs.Range{
							StartIndex: currentIndex - 1,
							EndIndex:   currentIndex,
						},
						TextStyle: textStyle,
						Fields:    strings.Join(fields, ","),
					},
				})
			}

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

			// Apply paragraph style (heading or normal text)
			// Always apply to prevent heading styles from persisting
			var paragraphStyle string
			if seg.heading > 0 {
				paragraphStyle = fmt.Sprintf("HEADING_%d", seg.heading)
			} else {
				paragraphStyle = "NORMAL_TEXT"
			}
			requests = append(requests, &docs.Request{
				UpdateParagraphStyle: &docs.UpdateParagraphStyleRequest{
					Range: &docs.Range{
						StartIndex: currentIndex - 1,
						EndIndex:   currentIndex,
					},
					ParagraphStyle: &docs.ParagraphStyle{
						NamedStyleType: paragraphStyle,
					},
					Fields: "namedStyleType",
				},
			})

			// Apply text formatting (bold, italic, underline, strikethrough) to the person chip
			if seg.bold || seg.italic || seg.underline || seg.strikethrough {
				textStyle := &docs.TextStyle{
					Bold:          seg.bold,
					Italic:        seg.italic,
					Underline:     seg.underline,
					Strikethrough: seg.strikethrough,
				}
				fields := []string{"bold", "italic", "underline", "strikethrough"}

				requests = append(requests, &docs.Request{
					UpdateTextStyle: &docs.UpdateTextStyleRequest{
						Range: &docs.Range{
							StartIndex: currentIndex - 1,
							EndIndex:   currentIndex,
						},
						TextStyle: textStyle,
						Fields:    strings.Join(fields, ","),
					},
				})
			}

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

		// Append \n to headings only after the LAST segment of that heading line.
		// Check if the next segment has a different headingLineID (or doesn't exist).
		// This ensures multi-segment headings (e.g., "# **Bold** Normal") stay as one paragraph,
		// while consecutive same-level headings (e.g., "# A\n# B") get separate paragraphs.
		if seg.heading > 0 && !strings.HasSuffix(insertText, "\n") {
			isLastHeadingSegment := i == len(segments)-1 || segments[i+1].headingLineID != seg.headingLineID
			// Don't add \n if this is the very last segment of the document
			if isLastHeadingSegment && i < len(segments)-1 {
				insertText += "\n"
			}
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

		// Prepare text style request - ALWAYS apply to ensure formatting is explicitly reset
		// This prevents bold/italic/underline/strikethrough from "sticking" to subsequent text
		var textStyle *docs.TextStyle
		var fields []string

		if seg.isInlineCode {
			// Inline code: apply Courier New font, preserve bold/italic/underline
			textStyle = &docs.TextStyle{
				WeightedFontFamily: &docs.WeightedFontFamily{
					FontFamily: "Courier New",
				},
				Bold:          seg.bold,
				Italic:        seg.italic,
				Underline:     seg.underline,
				Strikethrough: seg.strikethrough,
			}
			fields = []string{"weightedFontFamily", "bold", "italic", "underline", "strikethrough"}
			if seg.linkURL != "" {
				textStyle.Link = &docs.Link{
					Url: seg.linkURL,
				}
				fields = append(fields, "link")
			}
		} else {
			textStyle = &docs.TextStyle{
				Bold:          seg.bold,
				Italic:        seg.italic,
				Underline:     seg.underline,
				Strikethrough: seg.strikethrough,
			}

			if seg.linkURL != "" {
				textStyle.Link = &docs.Link{
					Url: seg.linkURL,
				}
			}

			// Always specify all basic formatting fields to ensure proper reset.
			// Include weightedFontFamily so that any previously applied
			// monospace font (e.g. from an adjacent inline-code segment) is
			// cleared and the document default font is restored.
			fields = []string{"weightedFontFamily", "bold", "italic", "underline", "strikethrough"}
			if seg.linkURL != "" {
				fields = append(fields, "link")
			}
		}

		textStyleRequest := &docs.Request{
			UpdateTextStyle: &docs.UpdateTextStyleRequest{
				Range: &docs.Range{
					StartIndex: currentIndex,
					EndIndex:   endIndex,
				},
				TextStyle: textStyle,
				Fields:    strings.Join(fields, ","),
			},
		}

		// For headings: defer text style requests until after UpdateParagraphStyle
		// to prevent the paragraph style from wiping out text formatting
		if inHeading {
			headingTextStyleRequests = append(headingTextStyleRequests, textStyleRequest)
		} else {
			// For normal text: apply text style immediately
			requests = append(requests, textStyleRequest)
		}

		// Apply heading style — only at the last segment of a heading line.
		// We apply UpdateParagraphStyle first, then all the deferred UpdateTextStyle
		// requests, so that text formatting overrides the paragraph style defaults.
		if seg.heading > 0 {
			isLastHeadingSegment := i == len(segments)-1 || segments[i+1].headingLineID != seg.headingLineID
			if isLastHeadingSegment {
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
				// Now apply all deferred text styles for this heading
				requests = append(requests, headingTextStyleRequests...)
				inHeading = false
				headingTextStyleRequests = nil
			}
		}

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
	isAppendingToNonEmptyDoc := false
	if p.AppendToEnd {
		// Find the end of the document (before the final newline)
		if doc.Body == nil || doc.Body.Content == nil || len(doc.Body.Content) == 0 {
			return nil, fmt.Errorf("document body is empty or invalid")
		}
		insertIndex = doc.Body.Content[len(doc.Body.Content)-1].EndIndex - 1
		// Check if document has existing content (insertIndex > 1 means non-empty)
		isAppendingToNonEmptyDoc = insertIndex > 1
	} else if insertIndex < 1 {
		return nil, fmt.Errorf("insert_index must be >= 1 (got %d); set append_to_end=true to append", insertIndex)
	}

	if p.IsMarkdown {
		// When appending to non-empty document, prepend newline to start new paragraph
		textToInsert := p.Text
		if isAppendingToNonEmptyDoc && !strings.HasPrefix(textToInsert, "\n") {
			textToInsert = "\n" + textToInsert
		}
		// Parse markdown and convert to requests
		segments := parseMarkdown(textToInsert)

		// Use the shared helper for handling markdown with potential tables
		result, err := insertMarkdownWithTables(docID, segments, insertIndex)
		if err != nil {
			return nil, err
		}

		// Add character count to result
		result["characters"] = len(p.Text)
		return result, nil
	}

	// Plain text insertion
	requests := []*docs.Request{
		{
			InsertText: &docs.InsertTextRequest{
				Text:     p.Text,
				Location: &docs.Location{Index: insertIndex},
			},
		},
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
	isAppendingToNonEmptyDoc := false
	if p.AppendToEnd {
		// Find the end of the document (before the final newline)
		if doc.Body == nil || doc.Body.Content == nil || len(doc.Body.Content) == 0 {
			return nil, fmt.Errorf("document body is empty or invalid")
		}
		insertIndex = doc.Body.Content[len(doc.Body.Content)-1].EndIndex - 1
		// Check if document has existing content (insertIndex > 1 means non-empty)
		isAppendingToNonEmptyDoc = insertIndex > 1
	} else if insertIndex < 1 {
		return nil, fmt.Errorf("insert_index must be >= 1 (got %d); set append_to_end=true to append", insertIndex)
	}

	// When appending to non-empty document, prepend newline to start new paragraph
	markdownToInsert := p.Markdown
	if isAppendingToNonEmptyDoc && !strings.HasPrefix(markdownToInsert, "\n") {
		markdownToInsert = "\n" + markdownToInsert
	}

	// Parse markdown and use the shared helper for handling tables
	segments := parseMarkdown(markdownToInsert)
	result, err := insertMarkdownWithTables(docID, segments, insertIndex)
	if err != nil {
		return nil, err
	}

	// Add character count to result
	result["characters"] = len(p.Markdown)
	return result, nil
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
