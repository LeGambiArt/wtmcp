package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

func TestExtractFileID(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "google doc URL",
			url:  "https://docs.google.com/document/d/1abc123xyz/edit",
			want: "1abc123xyz",
		},
		{
			name: "google sheet URL",
			url:  "https://docs.google.com/spreadsheets/d/1abc123xyz/edit#gid=0",
			want: "1abc123xyz",
		},
		{
			name: "google slides URL",
			url:  "https://docs.google.com/presentation/d/1abc123xyz/edit",
			want: "1abc123xyz",
		},
		{
			name: "drive file URL",
			url:  "https://drive.google.com/file/d/1abc123xyz/view",
			want: "1abc123xyz",
		},
		{
			name: "drive open URL with id param",
			url:  "https://drive.google.com/open?id=1abc123xyz",
			want: "1abc123xyz",
		},
		{
			name: "file ID with hyphens and underscores",
			url:  "https://docs.google.com/document/d/1a-b_c123XYZ/edit",
			want: "1a-b_c123XYZ",
		},
		{
			name: "not a google URL",
			url:  "https://example.com/page",
			want: "",
		},
		{
			name: "empty string",
			url:  "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFileID(tt.url)
			if got != tt.want {
				t.Errorf("extractFileID(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestCleanGoogleDocsCSS(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no CSS",
			input: "# Hello\n\nSome text",
			want:  "# Hello\n\nSome text",
		},
		{
			name:  "strips @import lines",
			input: "# Title\n@import url('https://fonts.googleapis.com');\nReal content",
			want:  "# Title\nReal content",
		},
		{
			name:  "strips list-style-type blocks",
			input: "# Title\n.lst-kix_abc { list-style-type: disc; }\nReal content",
			want:  "# Title\nReal content",
		},
		{
			name:  "empty input",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanGoogleDocsCSS(tt.input)
			if got != tt.want {
				t.Errorf("cleanGoogleDocsCSS() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildSearchQuery(t *testing.T) {
	tests := []struct {
		name           string
		text           string
		inNameOnly     bool
		mimeTypes      []string
		owners         []string
		includeTrashed bool
		wantContains   []string
		wantNotContain []string
	}{
		{
			name:         "text only full-text",
			text:         "quarterly report",
			wantContains: []string{"fullText contains 'quarterly report'", "name contains 'quarterly report'", "trashed = false"},
		},
		{
			name:           "name only",
			text:           "budget",
			inNameOnly:     true,
			wantContains:   []string{"name contains 'budget'"},
			wantNotContain: []string{"fullText"},
		},
		{
			name:         "single mime type",
			text:         "doc",
			mimeTypes:    []string{"application/vnd.google-apps.document"},
			wantContains: []string{"mimeType = 'application/vnd.google-apps.document'"},
		},
		{
			name:         "multiple mime types ORed",
			text:         "doc",
			mimeTypes:    []string{"application/vnd.google-apps.document", "application/pdf"},
			wantContains: []string{"(mimeType = 'application/vnd.google-apps.document' or mimeType = 'application/pdf')"},
		},
		{
			name:         "single owner",
			text:         "design",
			owners:       []string{"me"},
			wantContains: []string{"'me' in owners"},
		},
		{
			name:         "multiple owners ORed",
			text:         "design",
			owners:       []string{"alice@example.com", "bob@example.com"},
			wantContains: []string{"('alice@example.com' in owners or 'bob@example.com' in owners)"},
		},
		{
			name:           "include trashed",
			text:           "old",
			includeTrashed: true,
			wantNotContain: []string{"trashed"},
		},
		{
			name:         "combined filters",
			text:         "meeting",
			mimeTypes:    []string{"application/vnd.google-apps.document"},
			owners:       []string{"me"},
			wantContains: []string{"fullText contains", "mimeType =", "'me' in owners", "trashed = false"},
		},
		{
			name:         "escapes quotes in text",
			text:         "it's a test",
			wantContains: []string{"it\\'s a test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSearchQuery(tt.text, tt.inNameOnly, tt.mimeTypes, tt.owners, tt.includeTrashed)
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("query %q should contain %q", got, want)
				}
			}
			for _, notWant := range tt.wantNotContain {
				if strings.Contains(got, notWant) {
					t.Errorf("query %q should not contain %q", got, notWant)
				}
			}
		})
	}
}

func TestTitleSanitization(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"normal title", "normal title.md"},
		{"file<>with:bad/chars", "file__with_bad_chars.md"},
		{"../../etc/evil", ".._.._etc_evil.md"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			safeTitle := reUnsafeFileChars.ReplaceAllString(tt.input, "_")
			safeTitle = filepath.Base(safeTitle) + ".md"
			if safeTitle != tt.want {
				t.Errorf("sanitized %q = %q, want %q", tt.input, safeTitle, tt.want)
			}
		})
	}
}

// --- httptest integration tests ---

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func setupDriveTest(t *testing.T, handler http.Handler) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	svc, err := drive.NewService(context.Background(),
		option.WithHTTPClient(ts.Client()),
		option.WithEndpoint(ts.URL))
	if err != nil {
		t.Fatal(err)
	}
	driveSvc = svc
}

func TestParseCommentFileID(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		want       string
		wantErrMsg string
	}{
		{name: "raw ID", input: "abc_123-XYZ", want: "abc_123-XYZ"},
		{name: "document URL", input: "https://docs.google.com/document/d/doc123/edit?tab=t.0#heading=h.1", want: "doc123"},
		{name: "document URL without edit", input: "https://docs.google.com/document/d/doc123", want: "doc123"},
		{name: "non-Google host", input: "https://example.com/document/d/doc123/edit", wantErrMsg: "host must be exactly docs.google.com"},
		{name: "lookalike host", input: "https://docs.google.com.example.com/document/d/doc123/edit", wantErrMsg: "host must be exactly docs.google.com"},
		{name: "HTTP URL", input: "http://docs.google.com/document/d/doc123/edit", wantErrMsg: "scheme must be https"},
		{name: "user information", input: "https://user@docs.google.com/document/d/doc123/edit", wantErrMsg: "must not include user information"},
		{name: "unsupported path", input: "https://docs.google.com/spreadsheets/d/doc123/edit", wantErrMsg: "unsupported Google Docs URL path"},
		{name: "surrounding text", input: "see https://docs.google.com/document/d/doc123/edit", wantErrMsg: "invalid file_id_or_url URL"},
		{name: "surrounding whitespace", input: " doc123", wantErrMsg: "surrounding whitespace"},
		{name: "invalid ID characters", input: "doc/123", wantErrMsg: "invalid raw file ID"},
		{name: "malformed URL", input: "https://[invalid", wantErrMsg: "invalid file_id_or_url URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCommentFileID(tt.input)
			if tt.wantErrMsg != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Fatalf("parseCommentFileID(%q) error = %v, want it to contain %q", tt.input, err, tt.wantErrMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCommentFileID(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("parseCommentFileID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// wantCommentFields mirrors the Drive API v3 comments.list schema. The
// tool's fields mask must match it exactly, or the real API returns an
// error or silently drops fields.
const wantCommentFields = "nextPageToken,comments(id,content,author(displayName,emailAddress),createdTime,modifiedTime,resolved,quotedFileContent(value),anchor,replies(id,content,author(displayName,emailAddress),createdTime,action))"

func TestToolListCommentsNormalized(t *testing.T) {
	setupDriveTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/files/doc123/comments" {
			t.Errorf("request = %s %s, want GET comments for doc123", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("fields"); got != wantCommentFields {
			t.Errorf("fields mask = %q, want %q", got, wantCommentFields)
		}
		if r.URL.Query().Get("includeDeleted") == "true" {
			t.Error("request should leave deleted comments excluded")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"comments":[{"id":"open1","content":"Clarify this","author":{"displayName":"Reviewer","emailAddress":"reviewer@example.com"},"createdTime":"2026-09-28T10:00:00Z","modifiedTime":"2026-09-28T10:05:00Z","resolved":false,"quotedFileContent":{"value":"Quoted text"},"anchor":"opaque-location","replies":[{"id":"reply1","content":"Will do","author":{"displayName":"Author"},"createdTime":"2026-09-28T10:05:00Z","action":"reopen"}]},{"id":"resolved1","content":"Done","resolved":true},{"id":"bare1","content":"No optional fields"}]}`)
	}))

	result, err := toolListComments(mustJSON(t, map[string]any{
		"file_id_or_url": "https://docs.google.com/document/d/doc123/edit",
	}), nil)
	if err != nil {
		t.Fatalf("toolListComments: %v", err)
	}
	response, ok := result.(listCommentsResponse)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if response.FileID != "doc123" || len(response.Comments) != 3 || response.NextPageToken != "" {
		t.Fatalf("response = %#v", response)
	}
	if response.Comments[0].Author == nil || response.Comments[0].Author.EmailAddress != "reviewer@example.com" {
		t.Errorf("author = %#v", response.Comments[0].Author)
	}
	if response.Comments[0].QuotedFileContent != "Quoted text" || response.Comments[0].Anchor["opaque"] != "opaque-location" {
		t.Errorf("quote/anchor = %q / %#v", response.Comments[0].QuotedFileContent, response.Comments[0].Anchor)
	}
	if len(response.Comments[0].Replies) != 1 || response.Comments[0].Replies[0].Action != "reopen" {
		t.Errorf("replies = %#v", response.Comments[0].Replies)
	}
	if !response.Comments[1].Resolved || response.Comments[2].Resolved {
		t.Errorf("resolved states = %t, %t", response.Comments[1].Resolved, response.Comments[2].Resolved)
	}
	if response.Comments[1].Author != nil || response.Comments[1].CreatedTime != "" || response.Comments[1].QuotedFileContent != "" || response.Comments[1].Anchor != nil {
		t.Errorf("unavailable optional fields should be omitted: %#v", response.Comments[1])
	}
	if response.Comments[1].Replies == nil || len(response.Comments[1].Replies) != 0 {
		t.Errorf("empty replies must be an array, got %#v", response.Comments[1].Replies)
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]any
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatal(err)
	}
	comments := shape["comments"].([]any)
	thread := comments[0].(map[string]any)
	if _, ok := thread["author"].(map[string]any); !ok {
		t.Errorf("normalized author missing or malformed: %s", encoded)
	}
	anchor := thread["anchor"].(map[string]any)
	if anchor["opaque"] != "opaque-location" {
		t.Errorf("anchor is not explicitly opaque: %s", encoded)
	}
	bare := comments[2].(map[string]any)
	if _, ok := bare["author"]; ok {
		t.Errorf("unavailable author should be omitted: %s", encoded)
	}
	if replies, ok := bare["replies"].([]any); !ok || len(replies) != 0 {
		t.Errorf("empty replies should serialize as an array: %s", encoded)
	}
}

func TestToolListCommentsFiltersResolvedWithoutSkippingPage(t *testing.T) {
	requests := 0
	setupDriveTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.URL.Query().Get("fields"); got != wantCommentFields {
			t.Errorf("fields mask = %q, want %q", got, wantCommentFields)
		}
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			if got := r.URL.Query().Get("pageToken"); got != "" {
				t.Errorf("first page token = %q, want empty", got)
			}
			_, _ = fmt.Fprint(w, `{"comments":[{"id":"closed","content":"Resolved","resolved":true}],"nextPageToken":"page-2"}`)
			return
		}
		if got := r.URL.Query().Get("pageToken"); got != "page-2" {
			t.Errorf("second page token = %q, want page-2", got)
		}
		_, _ = fmt.Fprint(w, `{"comments":[{"id":"open","content":"Still open","resolved":false}],"nextPageToken":""}`)
	}))

	first, err := toolListComments(mustJSON(t, map[string]any{
		"file_id_or_url":   "doc123",
		"include_resolved": false,
	}), nil)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	firstPage := first.(listCommentsResponse)
	if len(firstPage.Comments) != 0 || firstPage.Comments == nil || firstPage.NextPageToken != "page-2" {
		t.Fatalf("resolved-only page = %#v", firstPage)
	}

	second, err := toolListComments(mustJSON(t, map[string]any{
		"file_id_or_url":   "doc123",
		"include_resolved": false,
		"page_token":       firstPage.NextPageToken,
	}), nil)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	secondPage := second.(listCommentsResponse)
	if requests != 2 || len(secondPage.Comments) != 1 || secondPage.Comments[0].ID != "open" || secondPage.NextPageToken != "" {
		t.Fatalf("second page = %#v (requests %d)", secondPage, requests)
	}
}

func TestToolListCommentsValidationAndAPIError(t *testing.T) {
	requests := 0
	setupDriveTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"error":{"code":403,"message":"permission denied"}}`)
	}))

	for _, params := range []map[string]any{
		{},
		{"file_id_or_url": "https://example.com/document/d/doc123/edit"},
	} {
		if _, err := toolListComments(mustJSON(t, params), nil); err == nil {
			t.Errorf("toolListComments(%v) should fail before the API request", params)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid input made %d API requests", requests)
	}

	_, err := toolListComments(mustJSON(t, map[string]any{"file_id_or_url": "doc123"}), nil)
	if err == nil || !strings.Contains(err.Error(), `list comments for file "doc123"`) || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("API error should retain context, got %v", err)
	}
	if requests != 1 {
		t.Fatalf("API error request count = %d, want 1", requests)
	}
}

func TestToolListCommentsEmptyResponse(t *testing.T) {
	setupDriveTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{}`)
	}))
	result, err := toolListComments(mustJSON(t, map[string]any{"file_id_or_url": "doc123"}), nil)
	if err != nil {
		t.Fatalf("toolListComments: %v", err)
	}
	response := result.(listCommentsResponse)
	if response.Comments == nil || len(response.Comments) != 0 || response.NextPageToken != "" {
		t.Fatalf("empty response = %#v", response)
	}
}

func TestToolGetFileByID(t *testing.T) {
	setupDriveTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"abc","name":"doc.txt","mimeType":"text/plain","webViewLink":"https://drive.google.com/file/d/abc/view"}`)
	}))

	result, err := toolGetFileByID(mustJSON(t, map[string]any{
		"file_id": "abc",
	}), nil)
	if err != nil {
		t.Fatalf("toolGetFileByID: %v", err)
	}

	file, ok := result.(*drive.File)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if file.Name != "doc.txt" {
		t.Errorf("name = %q", file.Name)
	}
}

func TestToolGetFileByIDMissing(t *testing.T) {
	_, err := toolGetFileByID(mustJSON(t, map[string]any{}), nil)
	if err == nil {
		t.Fatal("expected error for missing file_id")
	}
}

func TestToolGetFileByURL(t *testing.T) {
	setupDriveTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"xyz","name":"design.doc","mimeType":"application/vnd.google-apps.document"}`)
	}))

	result, err := toolGetFileByURL(mustJSON(t, map[string]any{
		"url": "https://docs.google.com/document/d/xyz/edit",
	}), nil)
	if err != nil {
		t.Fatalf("toolGetFileByURL: %v", err)
	}

	file, ok := result.(*drive.File)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if file.Id != "xyz" {
		t.Errorf("id = %q", file.Id)
	}
}

func TestToolGetFileByURLInvalid(t *testing.T) {
	result, err := toolGetFileByURL(mustJSON(t, map[string]any{
		"url": "https://example.com/not-a-drive-url",
	}), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m, ok := result.(map[string]string)
	if !ok {
		t.Fatalf("expected error map, got %T", result)
	}
	if _, hasErr := m["error"]; !hasErr {
		t.Error("expected error key")
	}
}

func TestToolSearchText(t *testing.T) {
	setupDriveTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"files":[{"id":"f1","name":"report.doc","mimeType":"application/vnd.google-apps.document"}]}`)
	}))

	result, err := toolSearchText(mustJSON(t, map[string]any{
		"text": "quarterly report",
	}), nil)
	if err != nil {
		t.Fatalf("toolSearchText: %v", err)
	}

	list, ok := result.(*drive.FileList)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if len(list.Files) != 1 {
		t.Fatalf("got %d files, want 1", len(list.Files))
	}
	if list.Files[0].Name != "report.doc" {
		t.Errorf("name = %q", list.Files[0].Name)
	}
}

func TestToolSearchTextMissing(t *testing.T) {
	_, err := toolSearchText(mustJSON(t, map[string]any{}), nil)
	if err == nil {
		t.Fatal("expected error for missing text")
	}
}

func TestToolSearchFiles(t *testing.T) {
	setupDriveTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"files":[{"id":"f2","name":"data.csv"}]}`)
	}))

	result, err := toolSearchFiles(mustJSON(t, map[string]any{
		"query": "name contains 'data'",
	}), nil)
	if err != nil {
		t.Fatalf("toolSearchFiles: %v", err)
	}

	list, ok := result.(*drive.FileList)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if len(list.Files) != 1 {
		t.Fatalf("got %d files, want 1", len(list.Files))
	}
}

// --- Write tool tests ---

func TestRequireWriteScope(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true

	hasWriteScope = true
	if err := requireWriteScope(); err != nil {
		t.Fatalf("expected nil when hasWriteScope=true, got %v", err)
	}

	hasWriteScope = false
	err := requireWriteScope()
	if err == nil {
		t.Fatal("expected error when hasWriteScope=false")
	}
	if !strings.Contains(err.Error(), "re-authorization") {
		t.Errorf("error should mention re-authorization, got: %v", err)
	}
}

func TestRequireWriteScopeTransientError(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})

	calls := 0
	setupDriveTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ids":["x"],"space":"drive","kind":"drive#generatedIds"}`)
	}))

	writeScopeProbed = false
	hasWriteScope = false

	// First call hits a 500 — should NOT cache the result.
	if err := requireWriteScope(); err != nil {
		t.Fatalf("transient error should not return scope error, got: %v", err)
	}
	if writeScopeProbed {
		t.Error("writeScopeProbed should remain false after transient error")
	}

	// Second call retries and succeeds.
	if err := requireWriteScope(); err != nil {
		t.Fatalf("retry after transient should succeed, got: %v", err)
	}
	if !writeScopeProbed {
		t.Error("writeScopeProbed should be true after successful probe")
	}
	if !hasWriteScope {
		t.Error("hasWriteScope should be true after successful probe")
	}
}

func sessionTempFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	origSD := sessionDir
	t.Cleanup(func() { sessionDir = origSD })

	dir := t.TempDir()
	sessionDir = dir

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestToolCreateFolderDryRun(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = true

	result, err := toolCreateFolder(mustJSON(t, map[string]any{
		"name":             "My Folder",
		"parent_folder_id": "parent123",
		"dry_run":          true,
	}), nil)
	if err != nil {
		t.Fatalf("toolCreateFolder dry_run: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if m["dry_run"] != true {
		t.Error("expected dry_run=true")
	}
	if m["action"] != "drive_create_folder" {
		t.Errorf("action = %v", m["action"])
	}
	if m["name"] != "My Folder" {
		t.Errorf("name = %v", m["name"])
	}
	if m["parent_folder_id"] != "parent123" {
		t.Errorf("parent_folder_id = %v", m["parent_folder_id"])
	}
}

func TestToolCreateFolderMissingName(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = true

	_, err := toolCreateFolder(mustJSON(t, map[string]any{}), nil)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestToolCreateFolderNoScope(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = false

	_, err := toolCreateFolder(mustJSON(t, map[string]any{
		"name": "test",
	}), nil)
	if err == nil {
		t.Fatal("expected scope error")
	}
	if !strings.Contains(err.Error(), "re-authorization") {
		t.Errorf("error should mention re-authorization: %v", err)
	}
}

func TestToolCreateFolderActual(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = true

	setupDriveTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"folder1","name":"New Folder","mimeType":"application/vnd.google-apps.folder","webViewLink":"https://drive.google.com/drive/folders/folder1"}`)
	}))

	result, err := toolCreateFolder(mustJSON(t, map[string]any{
		"name":    "New Folder",
		"dry_run": false,
	}), nil)
	if err != nil {
		t.Fatalf("toolCreateFolder: %v", err)
	}

	file, ok := result.(*drive.File)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if file.Id != "folder1" {
		t.Errorf("id = %q", file.Id)
	}
	if file.MimeType != "application/vnd.google-apps.folder" {
		t.Errorf("mimeType = %q", file.MimeType)
	}
}

func TestToolUploadFileDryRun(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = true

	testFile := sessionTempFile(t, "hello.txt", []byte("hello"))

	result, err := toolUploadFile(mustJSON(t, map[string]any{
		"file_path": testFile,
		"dry_run":   true,
	}), nil)
	if err != nil {
		t.Fatalf("toolUploadFile dry_run: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if m["dry_run"] != true {
		t.Error("expected dry_run=true")
	}
	if m["action"] != "drive_upload_file" {
		t.Errorf("action = %v", m["action"])
	}
	if m["name"] != "hello.txt" {
		t.Errorf("name = %v", m["name"])
	}
}

func TestToolUploadFileActual(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = true

	setupDriveTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"up1","name":"hello.txt","mimeType":"text/plain","size":"5","webViewLink":"https://drive.google.com/file/d/up1/view"}`)
	}))

	testFile := sessionTempFile(t, "upload-actual.txt", []byte("hello"))

	result, err := toolUploadFile(mustJSON(t, map[string]any{
		"file_path": testFile,
		"dry_run":   false,
	}), nil)
	if err != nil {
		t.Fatalf("toolUploadFile: %v", err)
	}

	file, ok := result.(*drive.File)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if file.Id != "up1" {
		t.Errorf("id = %q", file.Id)
	}
	if file.Name != "hello.txt" {
		t.Errorf("name = %q", file.Name)
	}
}

func TestToolUploadFileNoScope(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = false

	_, err := toolUploadFile(mustJSON(t, map[string]any{
		"file_path": "/tmp/any.txt",
	}), nil)
	if err == nil {
		t.Fatal("expected scope error")
	}
	if !strings.Contains(err.Error(), "re-authorization") {
		t.Errorf("error should mention re-authorization: %v", err)
	}
}

func TestToolUploadFileMissingPath(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = true

	_, err := toolUploadFile(mustJSON(t, map[string]any{}), nil)
	if err == nil {
		t.Fatal("expected error for missing file_path")
	}
}

func TestToolUploadFileRejectsOutsideSession(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	origSD := sessionDir
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
		sessionDir = origSD
	})
	writeScopeProbed = true
	hasWriteScope = true
	sessionDir = t.TempDir()

	_, err := toolUploadFile(mustJSON(t, map[string]any{
		"file_path": "/etc/passwd",
	}), nil)
	if err == nil {
		t.Fatal("expected error for path outside session dir")
	}
	if !strings.Contains(err.Error(), "session directory") {
		t.Errorf("error should mention session directory, got: %v", err)
	}
}

func TestToolRenameFileDryRun(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = true

	setupDriveTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"abc123","name":"old-name.txt","mimeType":"text/plain"}`)
	}))

	result, err := toolRenameFile(mustJSON(t, map[string]any{
		"file_id": "abc123",
		"name":    "new-name.txt",
		"dry_run": true,
	}), nil)
	if err != nil {
		t.Fatalf("toolRenameFile dry_run: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if m["dry_run"] != true {
		t.Error("expected dry_run=true")
	}
	if m["action"] != "drive_rename_file" {
		t.Errorf("action = %v", m["action"])
	}
	if m["new_name"] != "new-name.txt" {
		t.Errorf("new_name = %v", m["new_name"])
	}
	if m["file_name"] != "old-name.txt" {
		t.Errorf("file_name = %v", m["file_name"])
	}
}

func TestToolRenameFileActual(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = true

	setupDriveTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"ren1","name":"new-name.txt","mimeType":"text/plain","webViewLink":"https://drive.google.com/file/d/ren1/view"}`)
	}))

	result, err := toolRenameFile(mustJSON(t, map[string]any{
		"file_id": "ren1",
		"name":    "new-name.txt",
		"dry_run": false,
	}), nil)
	if err != nil {
		t.Fatalf("toolRenameFile: %v", err)
	}

	file, ok := result.(*drive.File)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if file.Id != "ren1" {
		t.Errorf("id = %q", file.Id)
	}
	if file.Name != "new-name.txt" {
		t.Errorf("name = %q", file.Name)
	}
}

func TestToolRenameFileNoFields(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = true

	_, err := toolRenameFile(mustJSON(t, map[string]any{
		"file_id": "abc",
	}), nil)
	if err == nil {
		t.Fatal("expected error when no name/parents fields provided")
	}
}

func TestToolCopyFileDryRun(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = true

	result, err := toolCopyFile(mustJSON(t, map[string]any{
		"file_id": "abc123",
		"name":    "copy-of-doc.txt",
		"dry_run": true,
	}), nil)
	if err != nil {
		t.Fatalf("toolCopyFile dry_run: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if m["dry_run"] != true {
		t.Error("expected dry_run=true")
	}
	if m["action"] != "drive_copy_file" {
		t.Errorf("action = %v", m["action"])
	}
}

func TestToolCopyFileActual(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = true

	setupDriveTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"cp1","name":"copy-of-doc.txt","mimeType":"text/plain","size":"100","webViewLink":"https://drive.google.com/file/d/cp1/view"}`)
	}))

	result, err := toolCopyFile(mustJSON(t, map[string]any{
		"file_id": "abc123",
		"name":    "copy-of-doc.txt",
		"dry_run": false,
	}), nil)
	if err != nil {
		t.Fatalf("toolCopyFile: %v", err)
	}

	file, ok := result.(*drive.File)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if file.Id != "cp1" {
		t.Errorf("id = %q", file.Id)
	}
	if file.Name != "copy-of-doc.txt" {
		t.Errorf("name = %q", file.Name)
	}
}

func TestToolCopyFileMissingID(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = true

	_, err := toolCopyFile(mustJSON(t, map[string]any{}), nil)
	if err == nil {
		t.Fatal("expected error for missing file_id")
	}
}

func TestToolDeleteFileDryRun(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = true

	setupDriveTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"del1","name":"old-file.txt","mimeType":"text/plain","size":"42"}`)
	}))

	result, err := toolDeleteFile(mustJSON(t, map[string]any{
		"file_id": "del1",
		"dry_run": true,
	}), nil)
	if err != nil {
		t.Fatalf("toolDeleteFile dry_run: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if m["dry_run"] != true {
		t.Error("expected dry_run=true")
	}
	if m["action"] != "drive_delete_file" {
		t.Errorf("action = %v", m["action"])
	}
	if m["file_name"] != "old-file.txt" {
		t.Errorf("file_name = %v", m["file_name"])
	}
}

func TestToolDeleteFileActual(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = true

	setupDriveTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"del1","name":"old-file.txt","trashed":true}`)
	}))

	result, err := toolDeleteFile(mustJSON(t, map[string]any{
		"file_id": "del1",
		"dry_run": false,
	}), nil)
	if err != nil {
		t.Fatalf("toolDeleteFile: %v", err)
	}

	file, ok := result.(*drive.File)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if file.Id != "del1" {
		t.Errorf("id = %q", file.Id)
	}
	if !file.Trashed {
		t.Error("expected trashed=true")
	}
}

func TestToolDeleteFileMissingID(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = true

	_, err := toolDeleteFile(mustJSON(t, map[string]any{}), nil)
	if err == nil {
		t.Fatal("expected error for missing file_id")
	}
}

func TestWriteToolsDefaultDryRunTrue(t *testing.T) {
	orig := hasWriteScope
	origProbed := writeScopeProbed
	t.Cleanup(func() {
		hasWriteScope = orig
		writeScopeProbed = origProbed
	})
	writeScopeProbed = true
	hasWriteScope = true

	setupDriveTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"abc","name":"default.txt","mimeType":"text/plain","size":"10"}`)
	}))

	testFile := sessionTempFile(t, "default-test.txt", []byte("x"))

	// Upload: omit dry_run entirely — should default to true.
	result, err := toolUploadFile(mustJSON(t, map[string]any{
		"file_path": testFile,
	}), nil)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if m, ok := result.(map[string]any); !ok || m["dry_run"] != true {
		t.Error("upload should default to dry_run=true")
	}

	// Rename: omit dry_run.
	result, err = toolRenameFile(mustJSON(t, map[string]any{
		"file_id": "abc",
		"name":    "new",
	}), nil)
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if m, ok := result.(map[string]any); !ok || m["dry_run"] != true {
		t.Error("rename should default to dry_run=true")
	}

	// Copy: omit dry_run.
	result, err = toolCopyFile(mustJSON(t, map[string]any{
		"file_id": "abc",
	}), nil)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if m, ok := result.(map[string]any); !ok || m["dry_run"] != true {
		t.Error("copy should default to dry_run=true")
	}

	// Delete: omit dry_run.
	result, err = toolDeleteFile(mustJSON(t, map[string]any{
		"file_id": "abc",
	}), nil)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if m, ok := result.(map[string]any); !ok || m["dry_run"] != true {
		t.Error("delete should default to dry_run=true")
	}

	// CreateFolder: omit dry_run.
	result, err = toolCreateFolder(mustJSON(t, map[string]any{
		"name": "test-folder",
	}), nil)
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	if m, ok := result.(map[string]any); !ok || m["dry_run"] != true {
		t.Error("create folder should default to dry_run=true")
	}
}

func TestConfineToSessionDir(t *testing.T) {
	origSD := sessionDir
	t.Cleanup(func() { sessionDir = origSD })

	dir := t.TempDir()
	sessionDir = dir

	inner := filepath.Join(dir, "sub")
	if err := os.MkdirAll(inner, 0o750); err != nil {
		t.Fatal(err)
	}
	innerFile := filepath.Join(inner, "file.txt")
	if err := os.WriteFile(innerFile, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"inside session dir", innerFile, false},
		{"session dir itself", dir, false},
		{"etc passwd", "/etc/passwd", true},
		{"root", "/", true},
		{"tmp", "/tmp/file.txt", true},
		{"traversal", filepath.Join(dir, "..", "escape"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := confineToSessionDir(tc.path)
			if tc.wantErr && err == nil {
				t.Errorf("expected error for %s", tc.path)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for %s: %v", tc.path, err)
			}
		})
	}

	t.Run("relative path resolves against session dir", func(t *testing.T) {
		resolved, err := confineToSessionDir("sub/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasSuffix(resolved, "sub/file.txt") {
			t.Errorf("resolved = %q, want suffix sub/file.txt", resolved)
		}
	})

	t.Run("symlink escape rejected", func(t *testing.T) {
		link := filepath.Join(dir, "escape-link")
		if err := os.Symlink("/etc/passwd", link); err != nil {
			t.Skipf("symlinks not supported: %v", err)
		}

		// Outside the sandbox, EvalSymlinks succeeds and resolves the
		// symlink target, so the prefix check rejects it. Inside the
		// sandbox, EvalSymlinks fails (ancestor lstat denied) but the
		// OS sandbox blocks access to the target at the kernel level.
		if _, err := confineToSessionDir(link); err == nil {
			t.Error("expected error for symlink pointing outside session dir")
		}
	})

	t.Run("empty session dir", func(t *testing.T) {
		sessionDir = ""
		_, err := confineToSessionDir("/any/path")
		if err == nil {
			t.Fatal("expected error for empty sessionDir")
		}
		if !strings.Contains(err.Error(), "session directory") {
			t.Errorf("error should mention session directory: %v", err)
		}
		sessionDir = dir
	})

	t.Run("home path outside session dir rejected", func(t *testing.T) {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("cannot determine home dir")
		}
		_, err = confineToSessionDir(filepath.Join(home, ".ssh", "id_rsa"))
		if err == nil {
			t.Error("expected error for path under $HOME but outside session dir")
		}
	})

	t.Run("relative traversal rejected", func(t *testing.T) {
		_, err := confineToSessionDir("../../../etc/passwd")
		if err == nil {
			t.Fatal("expected error for relative path traversal")
		}
	})
}
