package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/LeGambiArt/wtmcp/plugins/google-docs/highlighter"
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
	reLinkInline    = regexp.MustCompile(`\[([^\]]+)\]\(((?:[^\s()]*(?:\([^\s()]*\))?)*)\)`)
	// Table detection patterns
	reTableRow       = regexp.MustCompile(`^\s*\|(.+\|)+\s*$`)
	reTableSeparator = regexp.MustCompile(`^\s*\|[\s\-:]*\-[\s\-:]*(\|[\s\-:]*\-[\s\-:]*)*\|\s*$`)
)

// Config cache for syntax highlighting
var (
	highlightConfigCache = make(map[string]*highlighter.Config)
	highlightConfigMutex sync.RWMutex
)

// getHighlightConfig retrieves or loads a highlighting config for a language.
func getHighlightConfig(language string) (*highlighter.Config, error) {
	highlightConfigMutex.RLock()
	if cfg, ok := highlightConfigCache[language]; ok {
		highlightConfigMutex.RUnlock()
		return cfg, nil
	}
	highlightConfigMutex.RUnlock()

	// Load config
	cfg, err := highlighter.LoadConfig(language)
	if err != nil {
		return nil, err
	}

	// Cache it
	highlightConfigMutex.Lock()
	highlightConfigCache[language] = cfg
	highlightConfigMutex.Unlock()

	return cfg, nil
}

const (
	maxTableRows    = 50
	maxTableColumns = 20

	tablePipePlaceholder      = "\ue000" // U+E000 Private Use Area
	tableBackslashPlaceholder = "\ue001" // U+E001 Private Use Area
)

// monospaceStyleRequest creates an UpdateTextStyle request for basic
// Courier New monospace formatting, clearing all text decorations.
func monospaceStyleRequest(startIndex, endIndex int64) *docs.Request {
	return &docs.Request{
		UpdateTextStyle: &docs.UpdateTextStyleRequest{
			Range: &docs.Range{
				StartIndex: startIndex,
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
	}
}

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

// writeDocumentFile writes document content via the core's file I/O service.
func writeDocumentFile(title, outputPath, content, ext string) (string, error) {
	if outputPath == "" {
		safeTitle := reUnsafeChars.ReplaceAllString(title, "_")
		safeTitle = filepath.Base(safeTitle)
		outputPath = safeTitle + ext
	} else if filepath.IsAbs(outputPath) {
		outputPath = filepath.Base(outputPath)
	}

	return plug.FileWrite(outputPath, []byte(content))
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

		for rowIdx, row := range table.TableRows {
			sb.WriteString("|")
			for _, cell := range row.TableCells {
				var cellMD strings.Builder
				for _, cellElem := range cell.Content {
					if cellElem.Table != nil {
						extractElementText(&cellMD, cellElem)
					} else {
						// NOTE: paraIndex here is the parent table's index, not the
						// cell paragraph's. Code annotations (inline code, code blocks)
						// won't be detected for paragraphs inside table cells since
						// detectCode only indexes top-level paragraphs.
						extractElementMarkdown(&cellMD, cellElem, annotations, paraIndex)
					}
				}
				cellContent := strings.TrimRight(cellMD.String(), "\n")
				cellContent = strings.ReplaceAll(cellContent, "\n", " ")
				cellContent = strings.TrimSpace(cellContent)
				cellContent = strings.ReplaceAll(cellContent, "|", `\|`)
				sb.WriteString(" ")
				sb.WriteString(cellContent)
				sb.WriteString(" |")
			}
			sb.WriteString("\n")

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
		"characters": utf8.RuneCountInString(fullText),
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
		"characters":  utf8.RuneCountInString(text),
		"word_count":  len(strings.Fields(text)),
	}

	if p.SaveToFile {
		savedPath, err := writeDocumentFile(doc.Title, p.OutputPath, text, ".txt")
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
		"characters":  utf8.RuneCountInString(markdown),
	}

	if p.SaveToFile {
		savedPath, err := writeDocumentFile(doc.Title, p.OutputPath, markdown, ".md")
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
	table   *tableSegment
	// Horizontal rule
	isHorizontalRule bool
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

	// Body text buffer: consecutive non-special lines are joined with
	// a space into a single paragraph. In markdown, a single newline
	// within a paragraph is a soft wrap — only a blank line (double
	// newline) is a paragraph break.
	var bodyBuffer []string
	flushBody := func() {
		if len(bodyBuffer) == 0 {
			return
		}
		joined := strings.Join(bodyBuffer, " ")
		segments = append(segments, parseSimpleFormatting(joined+"\n")...)
		bodyBuffer = nil
	}

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)

		// Check for code fence boundaries (``` with optional language)
		if strings.HasPrefix(trimmedLine, "```") {
			flushBody()
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
		isTableRow := reTableRow.MatchString(escapeTablePipes(line))

		if isTableRow {
			if !inTable {
				flushBody()
				inTable = true
				tableBuffer = []string{}
			}
			tableBuffer = append(tableBuffer, line)
			continue
		}

		// Not a table row - process any buffered table
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

		// Process non-table line normally
		if trimmedLine == "" {
			flushBody()
			if lastWasHeading {
				// Skip blank lines immediately after headings
				continue
			}
			// Preserve blank lines between normal text as empty paragraphs
			segments = append(segments, markdownSegment{text: "\n"})
			continue
		}

		// Save trimmedLine before the heading parser which may mutate it
		originalTrimmed := trimmedLine

		// Check for headings — requires space after # per CommonMark
		headingLevel := 0
		if strings.HasPrefix(trimmedLine, "#") {
			foundSpace := false
		loop:
			for j, ch := range trimmedLine {
				switch ch {
				case '#':
					headingLevel++
				case ' ':
					trimmedLine = trimmedLine[j+1:]
					foundSpace = true
					break loop
				default:
					headingLevel = 0
					break loop
				}
			}
			if !foundSpace || strings.TrimSpace(trimmedLine) == "" {
				headingLevel = 0
			}
		}

		if headingLevel > 0 && headingLevel <= 6 {
			flushBody()
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
			flushBody()
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
			flushBody()
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

		// Check for horizontal rule (thematic break): 3+ identical -, *, or _
		// Note: spaced variants like "- - -" are not supported (intentional
		// simplification). "paragraph\n---" is treated as paragraph + HR,
		// not a Setext H2 (Setext headings are not supported).
		isHR := len(originalTrimmed) >= 3 &&
			(strings.Repeat("-", len(originalTrimmed)) == originalTrimmed ||
				strings.Repeat("*", len(originalTrimmed)) == originalTrimmed ||
				strings.Repeat("_", len(originalTrimmed)) == originalTrimmed)
		if isHR {
			flushBody()
			segments = append(segments, markdownSegment{isHorizontalRule: true})
			lastWasHeading = false
			continue
		}

		// Accumulate body text — joined into a single paragraph on flush
		bodyBuffer = append(bodyBuffer, originalTrimmed)
	}

	// Flush any remaining body text
	flushBody()

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
	}
}

// splitTableRow splits a table row by pipe delimiters.
// Returns cell contents without the leading/trailing pipes.
// escapeTablePipes replaces \\ then \| with placeholders so that
// pipe splitting and regex matching see the correct column structure.
func escapeTablePipes(line string) string {
	line = strings.ReplaceAll(line, `\\`, tableBackslashPlaceholder)
	line = strings.ReplaceAll(line, `\|`, tablePipePlaceholder)
	return line
}

// restoreTableEscapes restores placeholders back to their literal characters.
func restoreTableEscapes(s string) string {
	s = strings.ReplaceAll(s, tablePipePlaceholder, "|")
	s = strings.ReplaceAll(s, tableBackslashPlaceholder, `\`)
	return s
}

func splitTableRow(line string) []string {
	line = escapeTablePipes(line)
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")

	parts := strings.Split(line, "|")

	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, restoreTableEscapes(strings.TrimSpace(part)))
	}

	return cells
}

// isWordChar reports whether b is an ASCII alphanumeric character.
// Used for emphasis word-boundary checks: intra-word delimiters
// (e.g., WTMCP_FOO_BAR) are treated as literal per CommonMark rules.
func isWordChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
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
				if endPos == pos+2 {
					segments = append(segments, markdownSegment{text: "~~"})
					pos += 2
					continue
				}
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
			if pos > 0 && isWordChar(text[pos-1]) {
				segments = append(segments, markdownSegment{text: "**"})
				pos += 2
				continue
			}
			endPos := strings.Index(text[pos+2:], "**")
			if endPos != -1 {
				endPos += pos + 2
				if endPos == pos+2 {
					segments = append(segments, markdownSegment{text: "**"})
					pos += 2
					continue
				}
				if endPos+2 < len(text) && isWordChar(text[endPos+2]) {
					segments = append(segments, markdownSegment{text: text[pos : pos+1]})
					pos++
					continue
				}
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
			if pos > 0 && isWordChar(text[pos-1]) {
				segments = append(segments, markdownSegment{text: "__"})
				pos += 2
				continue
			}
			endPos := strings.Index(text[pos+2:], "__")
			if endPos != -1 {
				endPos += pos + 2
				if endPos == pos+2 {
					segments = append(segments, markdownSegment{text: "__"})
					pos += 2
					continue
				}
				if endPos+2 < len(text) && isWordChar(text[endPos+2]) {
					segments = append(segments, markdownSegment{text: text[pos : pos+1]})
					pos++
					continue
				}
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
			if pos > 0 && isWordChar(text[pos-1]) {
				segments = append(segments, markdownSegment{text: text[pos : pos+1]})
				pos++
				continue
			}
			endPos := strings.Index(text[pos+1:], "*")
			if endPos != -1 {
				endPos += pos + 1
				// Make sure it's not part of **
				if endPos+1 < len(text) && text[endPos+1] == '*' {
					segments = append(segments, markdownSegment{text: text[pos : pos+1]})
					pos++
					continue
				}
				if endPos+1 < len(text) && isWordChar(text[endPos+1]) {
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
			if pos > 0 && isWordChar(text[pos-1]) {
				segments = append(segments, markdownSegment{text: text[pos : pos+1]})
				pos++
				continue
			}
			endPos := strings.Index(text[pos+1:], "_")
			if endPos != -1 {
				endPos += pos + 1
				// Make sure it's not part of __
				if endPos+1 < len(text) && text[endPos+1] == '_' {
					segments = append(segments, markdownSegment{text: text[pos : pos+1]})
					pos++
					continue
				}
				if endPos+1 < len(text) && isWordChar(text[endPos+1]) {
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

		// Plain character — advance by full rune to avoid splitting multi-byte UTF-8
		_, size := utf8.DecodeRuneInString(text[pos:])
		segments = append(segments, markdownSegment{text: text[pos : pos+size]})
		pos += size
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
			!curr.isTable && !prev.isTable &&
			!curr.isHorizontalRule && !prev.isHorizontalRule {
			// Merge by concatenating text
			prev.text += curr.text
		} else {
			// Cannot merge, append as new segment
			merged = append(merged, curr)
		}
	}

	return merged
}

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

// maxCellReqsPerBatch caps the number of cell-population requests per
// BatchUpdate.  This is a conservative heuristic — the Google Docs API
// limits payload size (~10 MB), not request count.
const maxCellReqsPerBatch = 500

// tableRecord holds a table's API-reported cell indices alongside its parsed
// content, for deferred cell population in Phase 2.
type tableRecord struct {
	apiRows    []*docs.TableRow
	parsedRows []tableRow
	tableIdx   int
}

// buildCellBatches collects cell-population requests for all tables in
// reverse document order, then splits them into batches that respect
// the maxReqs limit. A single table's requests may be split across
// batches when it exceeds the limit — this is safe because
// collectTableCellRequests produces requests in strict reverse index
// order, so any contiguous sub-slice is a valid independent batch.
func buildCellBatches(tables []tableRecord, maxReqs int) [][]*docs.Request {
	if maxReqs <= 0 {
		maxReqs = maxCellReqsPerBatch
	}

	var allCells [][]*docs.Request
	for i := len(tables) - 1; i >= 0; i-- {
		cellGroups := collectTableCellRequests(tables[i].apiRows, tables[i].parsedRows)
		allCells = append(allCells, cellGroups...)
	}

	if len(allCells) == 0 {
		return nil
	}

	// Pack cells into batches without splitting any cell's requests.
	// A single cell exceeding maxReqs gets its own (oversized) batch;
	// this is correct because the API limits payload size, not count.
	var batches [][]*docs.Request
	var current []*docs.Request
	for _, cell := range allCells {
		if len(current) > 0 && len(current)+len(cell) > maxReqs {
			batches = append(batches, current)
			current = nil
		}
		current = append(current, cell...)
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

// insertMarkdownWithTables handles insertion of markdown segments that may
// contain tables. It uses a two-phase approach to minimize API calls:
//
// Phase 1 — Build document structure: for each table, flush any preceding
// text (using the returned final index to avoid a Get), create the empty
// table, then Get to discover cell indices. Cell population is deferred.
//
// Phase 2 — Populate all table cells in chunked BatchUpdate calls,
// processing tables in reverse document order so that insertions at later
// positions don't invalidate earlier cell indices.
func insertMarkdownWithTables(docID string, title string, segments []markdownSegment, insertIndex int64) (map[string]any, error) {
	hasTable := false
	tableCount := 0
	for _, seg := range segments {
		if seg.isTable && seg.table != nil {
			hasTable = true
			tableCount++
			if len(seg.table.rows) > maxTableRows || seg.table.numColumns > maxTableColumns {
				return nil, fmt.Errorf("table exceeds maximum dimensions (%d rows, %d columns); received %dx%d",
					maxTableRows, maxTableColumns, len(seg.table.rows), seg.table.numColumns)
			}
		}
	}

	if hasTable {
		currentIndex := insertIndex
		tableIdx := 0
		totalReplies := 0
		var pendingSegments []markdownSegment
		var tables []tableRecord

		// --- Phase 1: create document structure (text + empty tables) ---

		for _, seg := range segments {
			if seg.isTable && seg.table != nil {
				// Flush preceding text via BatchUpdate; use the returned
				// final index instead of a Documents.Get round-trip.
				if len(pendingSegments) > 0 {
					textReqs, finalIdx := convertMarkdownToRequests(pendingSegments, currentIndex, false)
					if len(textReqs) > 0 {
						resp, err := docsSvc.Documents.BatchUpdate(docID, &docs.BatchUpdateDocumentRequest{
							Requests: textReqs,
						}).Do()
						if err != nil {
							return nil, fmt.Errorf("insert content before table %d: %w", tableIdx, err)
						}
						totalReplies += len(resp.Replies)
						currentIndex = finalIdx
					}
					pendingSegments = nil
				}

				// Create the empty table structure.
				tableReqs := convertTableToRequests(seg.table, currentIndex)
				resp, err := docsSvc.Documents.BatchUpdate(docID, &docs.BatchUpdateDocumentRequest{
					Requests: tableReqs,
				}).Do()
				if err != nil {
					return nil, fmt.Errorf("create table %d: %w", tableIdx, err)
				}
				totalReplies += len(resp.Replies)

				// Get the document to find the table and its cell indices.
				doc, err := docsSvc.Documents.Get(docID).Do()
				if err != nil {
					return nil, fmt.Errorf("get document after creating table %d: %w", tableIdx, err)
				}
				if doc.Body == nil {
					return nil, fmt.Errorf("document body is nil after creating table %d", tableIdx)
				}

				expectedRows := len(seg.table.rows)
				expectedCols := seg.table.numColumns

				// Find the first table at or after our insertion point,
				// then validate its dimensions separately so the error
				// is descriptive on mismatch rather than "not found".
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

				actualRows := len(elem.Table.TableRows)
				actualCols := 0
				if actualRows > 0 {
					actualCols = len(elem.Table.TableRows[0].TableCells)
				}
				if actualRows != expectedRows || actualCols != expectedCols {
					return nil, fmt.Errorf("table %d dimension mismatch: requested %dx%d, API returned %dx%d",
						tableIdx, expectedRows, expectedCols, actualRows, actualCols)
				}

				if elem.StartIndex != currentIndex {
					fmt.Fprintf(os.Stderr, "WARNING: table %d predicted index %d != API index %d; using API value\n",
						tableIdx, currentIndex, elem.StartIndex)
				}

				tables = append(tables, tableRecord{
					apiRows:    elem.Table.TableRows,
					parsedRows: seg.table.rows,
					tableIdx:   tableIdx,
				})
				currentIndex = elem.EndIndex
				tableIdx++
			} else {
				pendingSegments = append(pendingSegments, seg)
			}
		}

		// Flush any trailing text after the last table.
		if len(pendingSegments) > 0 {
			textReqs, _ := convertMarkdownToRequests(pendingSegments, currentIndex, true)
			if len(textReqs) > 0 {
				resp, err := docsSvc.Documents.BatchUpdate(docID, &docs.BatchUpdateDocumentRequest{
					Requests: textReqs,
				}).Do()
				if err != nil {
					return nil, fmt.Errorf("insert trailing content: %w", err)
				}
				totalReplies += len(resp.Replies)
			}
		}

		// --- Phase 2: populate all table cells (chunked) ---
		//
		// Tables are processed in reverse document order so that cell
		// insertions at later positions don't shift earlier cells'
		// indices. Within each table, collectTableCellRequests already
		// produces requests in reverse cell order.

		batches := buildCellBatches(tables, maxCellReqsPerBatch)
		for bi, batch := range batches {
			resp, err := docsSvc.Documents.BatchUpdate(docID, &docs.BatchUpdateDocumentRequest{
				Requests: batch,
			}).Do()
			if err != nil {
				indices := make([]string, len(tables))
				for ti, tr := range tables {
					indices[ti] = fmt.Sprintf("%d", tr.tableIdx)
				}
				return nil, fmt.Errorf("populate table cells (batch %d/%d failed; %d/%d committed, tables [%s]): %w",
					bi+1, len(batches), bi, len(batches), strings.Join(indices, ","), err)
			}
			totalReplies += len(resp.Replies)
		}

		return map[string]any{
			"document_id":  docID,
			"title":        title,
			"status":       "success",
			"insert_index": insertIndex,
			"replies":      totalReplies,
			"tables":       tableCount,
		}, nil
	}

	// No tables — single-batch insertion.
	requests, _ := convertMarkdownToRequests(segments, insertIndex, true)
	batchUpdateReq := &docs.BatchUpdateDocumentRequest{Requests: requests}
	resp, err := docsSvc.Documents.BatchUpdate(docID, batchUpdateReq).Do()
	if err != nil {
		return nil, fmt.Errorf("batch update: %w", err)
	}

	return map[string]any{
		"document_id":  docID,
		"title":        title,
		"status":       "success",
		"insert_index": insertIndex,
		"replies":      len(resp.Replies),
		"tables":       0,
	}, nil
}

// collectTableCellRequests builds all cell-population requests for a table in
// reverse document order (last row/col first), suitable for a single BatchUpdate.
//
// INVARIANT: cells must be iterated in reverse document order. The Google Docs
// BatchUpdate API processes requests sequentially. Inserting content at a later
// document position shifts indices forward past that point but does not affect
// earlier positions. By processing from the last cell to the first, each cell's
// StartIndex (read from a single Get call) remains valid when its requests
// execute. Within each cell, populateTableCell interleaves InsertText and
// UpdateTextStyle per segment, so each style request sees the text that was
// just inserted. InsertDate and InsertPerson follow the same index-shifting
// rules (each consumes 1 character of index space).
func collectTableCellRequests(apiRows []*docs.TableRow, parsedRows []tableRow) [][]*docs.Request {
	var cellGroups [][]*docs.Request
	for r := len(parsedRows) - 1; r >= 0; r-- {
		if r >= len(apiRows) {
			continue
		}
		row := parsedRows[r]
		apiRow := apiRows[r]
		for c := len(row.cells) - 1; c >= 0; c-- {
			if c >= len(apiRow.TableCells) {
				continue
			}
			apiCell := apiRow.TableCells[c]
			if len(apiCell.Content) == 0 {
				continue
			}
			cellStartIndex := apiCell.Content[0].StartIndex
			cellReqs := populateTableCell(&row.cells[c], cellStartIndex)
			if len(cellReqs) > 0 {
				cellGroups = append(cellGroups, cellReqs)
			}
		}
	}
	return cellGroups
}

// populateTableCell creates requests to populate a single table cell with content.
func populateTableCell(cell *tableCell, cellStartIndex int64) []*docs.Request {
	var requests []*docs.Request

	// Skip empty cells - Google Docs already creates cells with an empty paragraph containing \n
	if len(cell.segments) == 0 || (len(cell.segments) == 1 && cell.segments[0].text == "" && !cell.segments[0].isDateField && !cell.segments[0].isPersonField) {
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
			var timestamp string
			if seg.dateValue == "" {
				timestamp = time.Now().UTC().Format(time.RFC3339)
			} else if specificDate, err := time.Parse("2006-01-02", seg.dateValue); err != nil {
				timestamp = time.Now().UTC().Format(time.RFC3339)
			} else {
				timestamp = specificDate.UTC().Format(time.RFC3339)
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
		if seg.bold || seg.italic || seg.underline || seg.strikethrough || seg.linkURL != "" || seg.isInlineCode {
			textStyle := &docs.TextStyle{
				Bold:          seg.bold,
				Italic:        seg.italic,
				Underline:     seg.underline,
				Strikethrough: seg.strikethrough,
			}

			if seg.linkURL != "" {
				textStyle.Link = &docs.Link{Url: seg.linkURL}
			}

			if seg.isInlineCode {
				textStyle.WeightedFontFamily = &docs.WeightedFontFamily{
					FontFamily: "Courier New",
				}
			}

			// Build fields list
			fields := []string{"bold", "italic", "underline", "strikethrough"}
			if seg.linkURL != "" {
				fields = append(fields, "link")
			}
			if seg.isInlineCode {
				fields = append(fields, "weightedFontFamily")
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
// When stripTrailingNewline is true, the trailing \n is removed from the last
// text segment to avoid an unwanted empty paragraph at the document end.
// Pass false when flushing mid-document text before a table.
func convertMarkdownToRequests(segments []markdownSegment, startIndex int64, stripTrailingNewline bool) ([]*docs.Request, int64) {
	var requests []*docs.Request
	currentIndex := startIndex
	var tabsInserted int64

	// Merge consecutive segments with identical formatting to reduce API requests
	segments = mergeSegments(segments)

	if stripTrailingNewline {
		for i := len(segments) - 1; i >= 0; i-- {
			if segments[i].text != "" && !segments[i].isDateField && !segments[i].isPersonField {
				segments[i].text = strings.TrimSuffix(segments[i].text, "\n")
				break
			}
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
	needsNormalTextReset := true

	for i, seg := range segments {
		if seg.isTable && seg.table != nil {
			fmt.Fprintf(os.Stderr, "WARNING: convertMarkdownToRequests called with table segment (skipping) — tables must be handled by insertMarkdownWithTables\n")
			continue
		}

		// Horizontal rule: insert empty paragraph with bottom border
		if seg.isHorizontalRule {
			if currentListStart >= 0 {
				listRanges = append(listRanges, listRange{
					startIndex: currentListStart,
					endIndex:   currentIndex,
					isOrdered:  currentListIsOrdered,
				})
				currentListStart = -1
			}
			requests = append(requests, &docs.Request{
				InsertText: &docs.InsertTextRequest{
					Text:     "\n",
					Location: &docs.Location{Index: currentIndex},
				},
			})
			requests = append(requests, &docs.Request{
				UpdateParagraphStyle: &docs.UpdateParagraphStyleRequest{
					Range: &docs.Range{
						StartIndex: currentIndex,
						EndIndex:   currentIndex + 1,
					},
					ParagraphStyle: &docs.ParagraphStyle{
						NamedStyleType: "NORMAL_TEXT",
						BorderBottom: &docs.ParagraphBorder{
							Color: &docs.OptionalColor{
								Color: &docs.Color{
									RgbColor: &docs.RgbColor{
										Red: 0.8, Green: 0.8, Blue: 0.8,
									},
								},
							},
							DashStyle: "SOLID",
							Width: &docs.Dimension{
								Magnitude: 1,
								Unit:      "PT",
							},
							Padding: &docs.Dimension{
								Magnitude: 6,
								Unit:      "PT",
							},
						},
					},
					Fields: "namedStyleType,borderBottom",
				},
			})
			currentIndex++
			continue
		}

		if seg.text == "" && !seg.isDateField && !seg.isPersonField {
			continue
		}

		// Handle code blocks (triple backticks) - insert text then apply syntax highlighting
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

			// Set paragraph style to NORMAL_TEXT FIRST to prevent it from wiping out text formatting
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

			// Apply syntax highlighting if language specified
			if seg.codeLanguage != "" {
				cfg, err := getHighlightConfig(seg.codeLanguage)
				if err == nil {
					// Apply highlighting
					highlighted, err := highlighter.HighlightCode(insertText, seg.codeLanguage, cfg)
					if err == nil {
						// Apply highlighting segment by segment
						segIndex := currentIndex
						for _, hseg := range highlighted {
							segLen := int64(utf8.RuneCountInString(hseg.Text))

							requests = append(requests, &docs.Request{
								UpdateTextStyle: &docs.UpdateTextStyleRequest{
									Range: &docs.Range{
										StartIndex: segIndex,
										EndIndex:   segIndex + segLen,
									},
									TextStyle: &docs.TextStyle{
										WeightedFontFamily: &docs.WeightedFontFamily{
											FontFamily: "Courier New",
										},
										ForegroundColor: &docs.OptionalColor{
											Color: &docs.Color{
												RgbColor: hseg.Color,
											},
										},
										Bold:          hseg.Bold,
										Italic:        hseg.Italic,
										Underline:     false,
										Strikethrough: false,
									},
									Fields: "weightedFontFamily,foregroundColor,bold,italic,underline,strikethrough",
								},
							})

							segIndex += segLen
						}
					} else {
						fmt.Fprintf(os.Stderr, "INFO: highlighting failed for %s: %v, using basic monospace\n", seg.codeLanguage, err)
						requests = append(requests, monospaceStyleRequest(currentIndex, endIndex))
					}
				} else {
					fmt.Fprintf(os.Stderr, "INFO: no highlighting config for %s: %v, using basic monospace\n", seg.codeLanguage, err)
					requests = append(requests, monospaceStyleRequest(currentIndex, endIndex))
				}
			} else {
				requests = append(requests, monospaceStyleRequest(currentIndex, endIndex))
			}

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
				timestamp = time.Now().UTC().Format(time.RFC3339)
			} else if specificDate, err := time.Parse("2006-01-02", seg.dateValue); err != nil {
				timestamp = time.Now().UTC().Format(time.RFC3339)
			} else {
				timestamp = specificDate.UTC().Format(time.RFC3339)
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

		// For nested list items, prepend one tab per nesting level to each
		// paragraph-starting line.  CreateParagraphBullets counts leading
		// tabs to determine nesting level and removes them automatically.
		//
		// When a list item has inline formatting, parseSimpleFormatting
		// splits it into multiple segments sharing the same listDepth.
		// Only the first line of the first segment in each paragraph
		// should get tabs — continuation segments within the same
		// paragraph must not, or the tab becomes a literal character.
		// We detect continuations by checking segments[i-1]: if the
		// previous segment is a list item at the same depth whose text
		// doesn't end with \n, this segment continues that paragraph.
		insertText := seg.text
		if isListItem && seg.listDepth > 0 {
			tabs := strings.Repeat("\t", seg.listDepth)

			isContinuation := i > 0 &&
				(segments[i-1].orderedListItem || segments[i-1].unorderedListItem) &&
				segments[i-1].listDepth == seg.listDepth &&
				!strings.HasSuffix(segments[i-1].text, "\n")

			var outLines []string
			firstLine := true
			for _, line := range strings.Split(seg.text, "\n") {
				if line != "" {
					if firstLine && isContinuation {
						outLines = append(outLines, line)
					} else {
						outLines = append(outLines, tabs+line)
						tabsInserted += int64(seg.listDepth)
					}
				} else {
					outLines = append(outLines, line)
				}
				firstLine = false
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

		// Reset paragraph style to NORMAL_TEXT once per paragraph for
		// non-heading body text. Without this, Google Docs inherits the
		// preceding heading style. Only apply at a paragraph boundary
		// (text containing \n marks the end of a paragraph — the next
		// segment starts a new one that needs the reset).
		if seg.heading == 0 && !seg.isCodeBlock && !inHeading && needsNormalTextReset {
			requests = append(requests, &docs.Request{
				UpdateParagraphStyle: &docs.UpdateParagraphStyleRequest{
					Range: &docs.Range{
						StartIndex: currentIndex,
						EndIndex:   endIndex,
					},
					ParagraphStyle: &docs.ParagraphStyle{
						NamedStyleType: "NORMAL_TEXT",
					},
					Fields: "namedStyleType",
				},
			})
			needsNormalTextReset = false
		}

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
				needsNormalTextReset = true
			}
		}

		// After a paragraph boundary (\n in text), the next segment
		// starts a new paragraph that needs a NORMAL_TEXT reset
		if strings.HasSuffix(insertText, "\n") && seg.heading == 0 {
			needsNormalTextReset = true
		}

		currentIndex = endIndex
	}

	// Close any remaining open list after all segments are processed.
	if currentListStart >= 0 {
		listRanges = append(listRanges, listRange{
			startIndex: currentListStart,
			endIndex:   currentIndex,
			isOrdered:  currentListIsOrdered,
		})
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

	// CreateParagraphBullets removes the leading tabs that were prepended
	// for list nesting.  Subtract them so the returned index reflects the
	// document state after the BatchUpdate executes.
	return requests, currentIndex - tabsInserted
}

const maxReadFileSize = 10 << 20 // 10 MB

func readFileForWrite(filePath string) ([]byte, error) {
	if sessionDir == "" {
		return nil, fmt.Errorf("file_path requires a configured session directory")
	}

	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(sessionDir, filePath)
	}

	resolvedSession, err := filepath.EvalSymlinks(sessionDir)
	if err != nil {
		return nil, fmt.Errorf("resolve session dir: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	absSession, err := filepath.Abs(resolvedSession)
	if err != nil {
		return nil, fmt.Errorf("abs session dir: %w", err)
	}
	absResolved, err := filepath.Abs(resolved)
	if err != nil {
		return nil, fmt.Errorf("abs file path: %w", err)
	}
	if !strings.HasPrefix(absResolved, absSession+string(os.PathSeparator)) &&
		absResolved != absSession {
		return nil, fmt.Errorf("file path escapes session directory: %s", filePath)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", filePath)
	}
	if info.Size() > maxReadFileSize {
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", info.Size(), maxReadFileSize)
	}

	return os.ReadFile(resolved)
}

type writeParams struct {
	DocumentIDOrURL string `json:"document_id_or_url"`
	FilePath        string `json:"file_path"`
	Content         string `json:"content"`
	IsMarkdown      bool   `json:"is_markdown"`
	AppendToEnd     bool   `json:"append_to_end"`
	InsertIndex     int64  `json:"insert_index"`
}

func toolWrite(params, _ json.RawMessage) (any, error) {
	p := writeParams{
		IsMarkdown:  true,
		AppendToEnd: true,
		InsertIndex: 1,
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.DocumentIDOrURL == "" {
		return nil, fmt.Errorf("document_id_or_url is required")
	}

	text := p.Content
	usedFilePath := false
	if text == "" && p.FilePath != "" {
		data, err := readFileForWrite(p.FilePath)
		if err != nil {
			return nil, err
		}
		text = string(data)
		usedFilePath = true
	}
	if text == "" {
		return nil, fmt.Errorf("either 'content' or 'file_path' must be provided")
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

	insertIndex := p.InsertIndex
	isAppendingToNonEmptyDoc := false
	if p.AppendToEnd {
		if doc.Body == nil || doc.Body.Content == nil || len(doc.Body.Content) == 0 {
			return nil, fmt.Errorf("document body is empty or invalid")
		}
		insertIndex = doc.Body.Content[len(doc.Body.Content)-1].EndIndex - 1
		isAppendingToNonEmptyDoc = insertIndex > 1
	} else if insertIndex < 1 {
		return nil, fmt.Errorf("insert_index must be >= 1 (got %d); set append_to_end=true to append", insertIndex)
	}

	if p.IsMarkdown {
		textToInsert := text
		if isAppendingToNonEmptyDoc && !strings.HasPrefix(textToInsert, "\n") {
			textToInsert = "\n" + textToInsert
		}
		segments := parseMarkdown(textToInsert)

		result, err := insertMarkdownWithTables(docID, doc.Title, segments, insertIndex)
		if err != nil {
			return nil, err
		}

		result["characters"] = utf8.RuneCountInString(text)
		if usedFilePath {
			result["source_file"] = p.FilePath
			result["source_bytes"] = len(text)
		}
		return result, nil
	}

	// Plain text insertion
	resp, err := docsSvc.Documents.BatchUpdate(docID, &docs.BatchUpdateDocumentRequest{
		Requests: []*docs.Request{
			{
				InsertText: &docs.InsertTextRequest{
					Text:     text,
					Location: &docs.Location{Index: insertIndex},
				},
			},
		},
	}).Do()
	if err != nil {
		return nil, fmt.Errorf("batch update: %w", err)
	}

	result := map[string]any{
		"document_id":  docID,
		"title":        doc.Title,
		"status":       "success",
		"insert_index": insertIndex,
		"characters":   utf8.RuneCountInString(text),
		"replies":      len(resp.Replies),
		"tables":       0,
	}
	if usedFilePath {
		result["source_file"] = p.FilePath
		result["source_bytes"] = len(text)
	}
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
