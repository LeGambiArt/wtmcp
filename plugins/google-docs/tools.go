package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	text      string
	bold      bool
	italic    bool
	underline bool
	linkURL   string
	heading   int // 0 for normal, 1-6 for heading levels
}

// parseMarkdown parses markdown text and returns segments with formatting info.
func parseMarkdown(markdown string) []markdownSegment {
	var segments []markdownSegment
	lines := strings.Split(markdown, "\n")

	for _, line := range lines {
		// Check for headings
		headingLevel := 0
		trimmedLine := strings.TrimSpace(line)
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

		// Parse inline formatting
		segments = append(segments, parseInlineFormatting(line+"\n")...)
	}

	return segments
}

// parseInlineFormatting parses inline markdown formatting (bold, italic, links, etc).
func parseInlineFormatting(text string) []markdownSegment {
	var segments []markdownSegment
	pos := 0

	for pos < len(text) {
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
				if pos < len(text) && pos > 0 && text[pos-1:pos] != " " {
					// Not a valid bold marker
					segments = append(segments, markdownSegment{text: text[pos : pos+1]})
					pos++
					continue
				}
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
			curr.heading == prev.heading {
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

	for _, seg := range segments {
		if seg.text == "" {
			continue
		}

		// Insert the text
		requests = append(requests, &docs.Request{
			InsertText: &docs.InsertTextRequest{
				Text:     seg.text,
				Location: &docs.Location{Index: currentIndex},
			},
		})

		// Use rune count, not byte length! Multi-byte UTF-8 characters need proper counting
		endIndex := currentIndex + int64(utf8.RuneCountInString(seg.text))

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

		// Apply text formatting
		if seg.bold || seg.italic || seg.underline || seg.linkURL != "" {
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

			fields := []string{}
			if seg.bold {
				fields = append(fields, "bold")
			}
			if seg.italic {
				fields = append(fields, "italic")
			}
			if seg.underline {
				fields = append(fields, "underline")
			}
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
		}

		currentIndex = endIndex
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
