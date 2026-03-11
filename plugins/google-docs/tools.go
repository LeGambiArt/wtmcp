package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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

	if doc.Body == nil {
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

	if doc.Body == nil {
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
		"title":        doc.Title,
		"document_id":  doc.DocumentId,
		"revision_id":  doc.RevisionId,
	}

	if doc.Body == nil {
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

		if elem.SectionBreak != nil {
			// Section breaks don't add to counts
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
	var p getDocumentMarkdownParams
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
	var p summarizeDocumentParams
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
