# Google Drive Plugin

## Tool Usage Guidelines

### Access Levels

Read tools (`drive_get_*`, `drive_search_*`, `drive_export_*`) are
always available. Write tools (`drive_upload_file`, `drive_rename_file`,
`drive_copy_file`, `drive_delete_file`, `drive_create_folder`) require
a read-write OAuth scope. If the current token is read-only, write
tools return an error guiding the user to re-authorize.

All write tools default to `dry_run=true`. Set `dry_run=false` to
execute the operation. Deletions are soft-deletes (moved to trash).

### Choosing the Right Search Tool

- **`drive_search_text`** — best for most searches. Builds the query
  for you from text, MIME types, and owner filters.
- **`drive_search_files`** — advanced. Pass a raw Drive API query
  string when you need operators not covered by `drive_search_text`.

### Search Patterns

**Find files by name:**
```
drive_search_text(text="quarterly report")
```

**Find my recent documents:**
```
drive_search_text(text="design", owners=["me"])
```

**Find Google Docs only:**
```
drive_search_text(text="meeting notes",
                  mime_types=["application/vnd.google-apps.document"])
```

**Search by name only (no full-text):**
```
drive_search_text(text="budget", in_name_only=true)
```

**Raw Drive query (advanced):**
```
drive_search_files(
    query="name contains 'design' and trashed = false and modifiedTime > '2026-01-01'")
```

**Find files in trash:**
```
drive_search_text(text="old report", include_trashed=true)
```

### Getting File Metadata

Three ways to get file info, use the most appropriate:

- **By ID:** `drive_get_file_by_id(file_id="1abc...")` when you
  have the file ID
- **By URL:** `drive_get_file_by_url(url="https://docs.google.com/...")`
  when pasting from a browser
- **From text:** `drive_extract_and_get_from_text(text="see this doc: https://...")`
  when scanning messages or notes for links

Supported URL formats:
- `https://docs.google.com/document/d/FILE_ID/edit`
- `https://docs.google.com/spreadsheets/d/FILE_ID/edit`
- `https://drive.google.com/file/d/FILE_ID/view`

### Reading Comment Threads

Use `drive_list_comments(file_id_or_url="...")` to retrieve comment
threads and replies from a Google Doc. The tool includes resolved
threads by default; set `include_resolved=false` to omit them. It accepts
a raw file ID or an HTTPS URL on `docs.google.com` with the path
`/document/d/FILE_ID` or `/document/d/FILE_ID/edit`.

Follow `next_page_token` with another call's `page_token` until the token
is empty. The `include_resolved` filter applies per request, so repeat
`include_resolved=false` on every paginated call. A page can contain no
comments and still have a next-page token when resolved threads are being
filtered. The Drive API omits deleted
comments by default. Comment anchors, when present, are returned as
opaque values and are not stable document positions. This tool reads
comments only; it does not create or modify comments or suggestions.

Unlike other read tools, which mirror the Drive API's camelCase field
names, this tool returns a normalized snake_case response.

### Exporting Documents

All text export tools save to local files by default to avoid
consuming context tokens. Set `save_to_file: false` only for
small documents where you need the content inline.

**Export a Google Doc as text:**
```
drive_export_google_doc_text(file_id="1abc...")
```
Saves to `.wtmcp-data/google-drive/<file_id>.txt`, returns path and stats.

**Export as Markdown (preserves formatting):**
```
drive_export_google_doc_markdown(file_id_or_url="1abc...")
```
Saves to `.wtmcp-data/google-drive/<title>.md`. Accepts file ID or URL.

**Export a Sheet as CSV:**
```
drive_export_google_sheet_csv(file_id="1abc...")
```
Exports the first worksheet only.

**Export Slides as PDF:**
```
drive_export_slides_pdf(file_id="1abc...")
```
Returns base64-encoded PDF (cannot be saved to file).

**Get content inline (not recommended for large files):**
```
drive_export_google_doc_text(file_id="1abc...", save_to_file=false)
```

### Common MIME Types

Use these with `drive_search_text(mime_types=[...])`:

- `application/vnd.google-apps.document` — Google Docs
- `application/vnd.google-apps.spreadsheet` — Google Sheets
- `application/vnd.google-apps.presentation` — Google Slides
- `application/vnd.google-apps.folder` — Folder
- `application/pdf` — PDF files

### File IDs

File IDs come from search results or URL extraction. Don't
fabricate IDs — always get them from a previous call or a URL.

### Write Operations

**Upload a local file:**
```
drive_upload_file(file_path="/home/user/report.pdf", dry_run=false)
```
File paths must be under the user's home directory.

**Create a folder:**
```
drive_create_folder(name="Project Files", dry_run=false)
```

**Rename a file:**
```
drive_rename_file(file_id="1abc...", name="new-name.pdf", dry_run=false)
```

**Copy a file:**
```
drive_copy_file(file_id="1abc...", name="backup-copy.pdf", dry_run=false)
```

**Move a file to trash (recoverable):**
```
drive_delete_file(file_id="1abc...", dry_run=false)
```

**Move a file to another folder:**
```
drive_rename_file(file_id="1abc...",
                  add_parents="folder_id",
                  remove_parents="old_folder_id",
                  dry_run=false)
```
