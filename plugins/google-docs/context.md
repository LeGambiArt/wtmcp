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
- `save_to_file` (default: false): Save to local file
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
- Italic: `*text*` or `_text_`
- Underline: `__text__`
- Links: `[text](url)`
- Ordered lists: `1. Item`, `2. Item`, etc.
- Unordered lists: `- Item`, `* Item`, or `+ Item`
- Nested lists: indent by 4 spaces (or 1 tab) per nesting level
- Date smart chips: `@today` (current date) or `@date(YYYY-MM-DD)` (specific date)
- Person smart chips: `@(email)` (e.g., `@(user@example.com)`)

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

## Markdown Writing Support

The `gdocs_write_text` and `gdocs_write_markdown` tools support converting Markdown to Google Docs rich formatting. This section documents all supported features and syntax.

### Text Formatting

| Feature | Markdown Syntax | Example | Result |
|---------|----------------|---------|---------|
| Bold | `**text**` | `**important**` | **important** |
| Italic | `*text*` or `_text_` | `*emphasis*` or `_emphasis_` | *emphasis* |
| Underline | `__text__` | `__underlined__` | <u>underlined</u> |
| Link | `[text](url)` | `[Google](https://google.com)` | Hyperlinked text |

### Headings

Headings use hash symbols (`#`) at the start of a line:

| Markdown | Google Docs Style |
|----------|-------------------|
| `# Heading 1` | Heading 1 |
| `## Heading 2` | Heading 2 |
| `### Heading 3` | Heading 3 |
| `#### Heading 4` | Heading 4 |
| `##### Heading 5` | Heading 5 |
| `###### Heading 6` | Heading 6 |

### Lists

**Ordered Lists (Numbered):**
```markdown
1. First item
2. Second item
3. Third item
```

**Unordered Lists (Bullets):**
```markdown
- First item
- Second item
- Third item
```

Alternative bullet markers: `*` or `+` also work:
```markdown
* Item one
+ Item two
```

**Nested Lists:**

Indent by **4 spaces** (or 1 tab) per nesting level. Ordered and unordered lists can be mixed at different levels:

```markdown
1. Top-level ordered item
    * Nested unordered item
    * Another nested unordered item
2. Second ordered item
    1. Nested ordered item
3. Third ordered item

* Top-level bullet
    * Nested bullet
    * Another nested bullet
        * Double-nested bullet
```

Nested items are rendered at the correct indentation level in Google Docs. The outer list numbering is preserved even when nested items of a different type appear inside.

When using nested lists, the inner list format might not match the expected one due to Google Docs management of inner lists.

### Smart Chips

Smart chips are interactive Google Docs elements that provide rich functionality.

**Date Smart Chips:**

| Syntax | Description | Example |
|--------|-------------|---------|
| `@today` | Inserts current date | `Meeting scheduled for @today` |
| `@date(YYYY-MM-DD)` | Inserts specific date | `Deadline: @date(2026-12-31)` |

Date chips display according to the user's Google Docs date format preferences.

**Person Smart Chips:**

| Syntax | Description | Example |
|--------|-------------|---------|
| `@(email)` | Inserts person by email | `Contact @(user@example.com)` |

Person chips link to the person's profile and show their avatar in Google Docs.

### Complete Example

```markdown
# Project Meeting Notes

Meeting Date: @today
Attendees: @(Alice Smith), @(bob@company.com)

## Action Items

1. **Review** the quarterly report by @date(2026-04-15)
2. _Follow up_ with @(Charlie Brown) on budget
3. Update the [documentation](https://docs.example.com)

## Discussion Points

- Revenue **increased** by 15%
- New product launch scheduled for @date(2026-06-01)
- Team lead: @(dana@company.com)
```

### Feature Support Matrix

| Feature | Supported | Notes |
|---------|-----------|-------|
| Bold (`**text**`) | ✅ Yes | |
| Italic (`*text*`, `_text_`) | ✅ Yes | Both asterisk and underscore |
| Underline (`__text__`) | ✅ Yes | Double underscore |
| Links (`[text](url)`) | ✅ Yes | |
| Headings (`# H1` to `###### H6`) | ✅ Yes | |
| Ordered lists (`1.`, `2.`, etc.) | ✅ Yes | Converted to Google Docs numbered lists |
| Unordered lists (`-`, `*`, `+`) | ✅ Yes | Converted to Google Docs bullet lists |
| Nested lists (4 spaces or 1 tab per level) | ✅ Yes | Mixed ordered/unordered nesting supported |
| Date chips (`@today`) | ✅ Yes | Current date |
| Date chips (`@date(YYYY-MM-DD)`) | ✅ Yes | Specific date |
| Person chips (`@(email)`) | ✅ Yes | |
| Tables | ❌ No | Not yet supported |
| Code blocks | ❌ No | Not yet supported |
| Inline code | ❌ No | Not yet supported |
| Blockquotes | ❌ No | Not yet supported |
| Strikethrough (`~~text~~`) | ✅ Yes | Standard markdown strikethrough |
| Images | ❌ No | Not yet supported |

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

This is an **initial implementation** of document creation and modification support. The markdown-to-Google Docs formatting conversion has the following limitations:

- **Tables**: Markdown tables are not converted to Google Docs table structures
- **Code blocks**: Code blocks and inline code formatting are not yet supported
- **Blockquotes**: Blockquotes are not converted to Google Docs quote styling
- **Strikethrough**: Strikethrough formatting is supported using `~~text~~` syntax
- **Images**: Inline images cannot be inserted via markdown

Future updates will expand the markdown conversion capabilities to handle these additional formatting features.
