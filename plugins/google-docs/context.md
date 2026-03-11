# Google Docs Plugin

This plugin provides tools to retrieve, summarize, and write to documents from Google Docs using the Google Docs API v1.

## Features

- Create new Google Docs with a specified title
- Retrieve full document content and structure
- Extract plain text from documents
- Convert documents to Markdown format
- Generate document summaries with structure analysis
- Extract and process multiple Google Docs URLs from text
- Write and append text with rich formatting support (markdown to rich text)

## Authentication

The plugin uses OAuth2 authentication with the Google Docs API. It requires:

- **Scope**: `https://www.googleapis.com/auth/documents` (read and write access)
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

### gdocs_write_text

Write or append text to a Google Doc with optional markdown formatting. When markdown is enabled, the text is parsed and converted to rich text with proper formatting.

**Parameters:**
- `document_id_or_url` (required): Document ID or URL
- `text` (required): Text content to write
- `is_markdown` (default: true): Parse text as markdown and apply rich formatting
- `append_to_end` (default: true): Append text to the end of the document
- `insert_index` (default: 0): Character index for insertion (used if append_to_end is false)

**Returns:** Document ID, title, status, insert index, character count

**Supported markdown formatting:**
- Headings: `# H1`, `## H2`, `### H3`, `#### H4`, `##### H5`, `###### H6`
- Bold: `**text**`
- Italic: `*text*`
- Underline: `__text__`
- Links: `[text](url)`

When `is_markdown` is false, text is inserted as plain text without formatting.

### gdocs_create_document

Create a new Google Doc with a specified title.

**Parameters:**
- `title` (required): Title for the new document

**Returns:** Document ID, title, URL, revision ID, status

**IMPORTANT:** When a new document is created, the full document URL **MUST** be provided to the user so they can access it. The URL is returned in the `url` field of the response.

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

### Write markdown text to a document
```json
{
  "document_id_or_url": "https://docs.google.com/document/d/ABC123/edit",
  "text": "# Meeting Notes\n\nDiscussed **important** topics:\n- Item 1\n- Item 2\n\nSee [documentation](https://example.com) for details.",
  "is_markdown": true,
  "append_to_end": true
}
```

### Write plain text to a document
```json
{
  "document_id_or_url": "ABC123",
  "text": "This is plain text without formatting.",
  "is_markdown": false,
  "append_to_end": true
}
```

### Create a new document
```json
{
  "title": "Meeting Notes - March 2026"
}
```

**Response:**
```json
{
  "status": "success",
  "document_id": "ABC123XYZ456",
  "title": "Meeting Notes - March 2026",
  "url": "https://docs.google.com/document/d/ABC123XYZ456/edit",
  "revision_id": "ALm..."
}
```

**Note:** Always provide the `url` field to the user so they can access the newly created document.

## File Output

When `save_to_file` is enabled, files are saved to:
- Default directory: `./docs/`
- Default filename: `<document-title>.<ext>` (sanitized)
- Custom path can be specified with `output_path`

Files are saved with permissions `0600` (owner read/write only).

## Notes

- The plugin runs in persistent mode for better performance
- Write operations require full document access scope (not readonly)
- Document structure is parsed to extract formatted content
- Markdown conversion preserves headings, formatting, tables, and links
- Text extraction strips all formatting for plain text output
- When writing markdown, the plugin automatically converts it to Google Docs rich text format
- Writing operations use the BatchUpdate API for efficient multi-request updates
- Authentication tokens may need to be refreshed after changing scopes from readonly to full access
- **IMPORTANT:** When creating a new document with `gdocs_create_document`, the document URL **MUST** always be provided to the user in the response so they can access the document

## Current Limitations

This is an **initial implementation** of document creation and modification support. The markdown-to-Google Docs formatting conversion has several limitations:

- **Lists**: Bullet points and numbered lists are not yet converted to Google Docs list format (they appear as plain text with list markers)
- **Tables**: Markdown tables are not converted to Google Docs table structures
- **Nested formatting**: Complex nested formatting combinations may not render correctly
- **Code blocks**: Code blocks and inline code formatting are not yet supported
- **Blockquotes**: Blockquotes are not converted to Google Docs quote styling
- **Strikethrough**: Strikethrough formatting is not supported
- **Images**: Inline images cannot be inserted via markdown

Future updates will expand the markdown conversion capabilities to handle these additional formatting features.
