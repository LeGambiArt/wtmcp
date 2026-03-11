# Google Docs Plugin

This plugin provides tools to retrieve and summarize documents from Google Docs using the Google Docs API v1.

## Features

- Retrieve full document content and structure
- Extract plain text from documents
- Convert documents to Markdown format
- Generate document summaries with structure analysis
- Extract and process multiple Google Docs URLs from text

## Authentication

The plugin uses OAuth2 authentication with the Google Docs API. It requires:

- **Scope**: `https://www.googleapis.com/auth/documents.readonly`
- **Token file**: `token-docs.json` (stored in Google credentials directory)
- **Credentials**: `client-credentials.json` (standard Google OAuth2 credentials)

The credentials directory defaults to `~/.config/wtmcp/credentials/google/` but can be customized with the `GOOGLE_CREDENTIALS_DIR` environment variable.

## Tools

### gdocs_get_document

Get the complete document structure including all content and formatting.

**Parameters:**
- `document_id_or_url` (required): Google Docs document ID or full URL

**Returns:** Full document object with structure

### gdocs_get_document_text

Extract plain text content from a Google Doc.

**Parameters:**
- `document_id_or_url` (required): Document ID or URL
- `save_to_file` (default: false): Save to local file
- `output_path` (optional): Custom output path (default: `./docs/<title>.txt`)

**Returns:** Document text with metadata

### gdocs_get_document_markdown

Convert a Google Doc to Markdown format with formatting preserved.

**Parameters:**
- `document_id_or_url` (required): Document ID or URL
- `save_to_file` (default: true): Save to local file
- `output_path` (optional): Custom output path (default: `./docs/<title>.md`)

**Returns:** Markdown content with metadata

**Supported formatting:**
- Headings (H1-H6)
- Bold, italic, underline text
- Links
- Tables
- Lists

### gdocs_summarize_document

Generate a summary of the document including structure analysis and statistics.

**Parameters:**
- `document_id_or_url` (required): Document ID or URL
- `include_structure` (default: true): Include list of headings

**Returns:**
- Title, document ID, revision ID
- Statistics: paragraph count, heading count, list count, table count, word count, character count
- Text preview (first 500 characters)
- List of headings (if `include_structure` is true)

### gdocs_extract_and_get_from_text

Extract Google Docs URLs from text and fetch summaries for each document.

**Parameters:**
- `text` (required): Text containing Google Docs URLs
- `max_docs` (default: 5): Maximum number of documents to fetch

**Returns:** Array of document summaries

## URL Formats Supported

The plugin can extract document IDs from various Google Docs URL formats:
- `https://docs.google.com/document/d/{id}/edit`
- `https://docs.google.com/document/d/{id}`
- Any URL with `?id={id}` parameter

It also accepts raw document IDs directly.

## Examples

### Get document as Markdown
```json
{
  "document_id_or_url": "https://docs.google.com/document/d/ABC123/edit",
  "save_to_file": true
}
```

### Summarize a document
```json
{
  "document_id_or_url": "ABC123",
  "include_structure": true
}
```

### Extract docs from text
```json
{
  "text": "Check out these docs: https://docs.google.com/document/d/ABC123/edit and https://docs.google.com/document/d/XYZ789/edit",
  "max_docs": 10
}
```

## File Output

When `save_to_file` is enabled, files are saved to:
- Default directory: `./docs/`
- Default filename: `<document-title>.<ext>` (sanitized)
- Custom path can be specified with `output_path`

Files are saved with permissions `0600` (owner read/write only).

## Notes

- The plugin runs in persistent mode for better performance
- All requests use the readonly scope for safety
- Document structure is parsed to extract formatted content
- Markdown conversion preserves headings, formatting, tables, and links
- Text extraction strips all formatting for plain text output
