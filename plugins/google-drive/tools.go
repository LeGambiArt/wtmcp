package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"google.golang.org/api/drive/v3"
	googleapi "google.golang.org/api/googleapi"
)

const defaultFields = "id,name,webViewLink,mimeType,owners,modifiedTime,size"

// extractFileID extracts a Google Drive file ID from a URL.
func extractFileID(url string) string {
	re := regexp.MustCompile(`/(?:d|file|document|spreadsheets|presentation)/d/([A-Za-z0-9_-]+)`)
	if m := re.FindStringSubmatch(url); len(m) > 1 {
		return m[1]
	}
	// Try ?id= query parameter
	re2 := regexp.MustCompile(`[?&]id=([A-Za-z0-9_-]+)`)
	if m := re2.FindStringSubmatch(url); len(m) > 1 {
		return m[1]
	}
	return ""
}

type getFileByIDParams struct {
	FileID string `json:"file_id"`
	Fields string `json:"fields"`
}

func toolGetFileByID(params, _ json.RawMessage) (any, error) {
	var p getFileByIDParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.FileID == "" {
		return nil, fmt.Errorf("file_id is required")
	}
	if p.Fields == "" {
		p.Fields = defaultFields
	}

	res, err := driveSvc.Files.Get(p.FileID).
		Fields(googleapi.Field(p.Fields)).
		SupportsAllDrives(true).
		Do()
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}
	return res, nil
}

type getFileByURLParams struct {
	URL    string `json:"url"`
	Fields string `json:"fields"`
}

func toolGetFileByURL(params, _ json.RawMessage) (any, error) {
	var p getFileByURLParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.URL == "" {
		return nil, fmt.Errorf("url is required")
	}
	if p.Fields == "" {
		p.Fields = defaultFields
	}

	fileID := extractFileID(p.URL)
	if fileID == "" {
		return map[string]string{
			"error": "could not extract file ID from URL",
			"url":   p.URL,
		}, nil
	}

	res, err := driveSvc.Files.Get(fileID).
		Fields(googleapi.Field(p.Fields)).
		SupportsAllDrives(true).
		Do()
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}
	return res, nil
}

type extractAndGetParams struct {
	Text     string `json:"text"`
	MaxFiles int    `json:"max_files"`
	Fields   string `json:"fields"`
}

func toolExtractAndGet(params, _ json.RawMessage) (any, error) {
	var p extractAndGetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.Text == "" {
		return nil, fmt.Errorf("text is required")
	}
	if p.MaxFiles == 0 {
		p.MaxFiles = 5
	}
	if p.Fields == "" {
		p.Fields = defaultFields
	}

	re := regexp.MustCompile(`https?://(?:drive|docs)\.google\.com/[\w\-/\?=&#%.]+`)
	urls := re.FindAllString(p.Text, -1)

	var results []any
	for i, u := range urls {
		if i >= p.MaxFiles {
			break
		}
		fileID := extractFileID(u)
		if fileID == "" {
			continue
		}
		res, err := driveSvc.Files.Get(fileID).
			Fields(googleapi.Field(p.Fields)).
			SupportsAllDrives(true).
			Do()
		if err != nil {
			results = append(results, map[string]string{
				"error": err.Error(),
				"url":   u,
			})
			continue
		}
		results = append(results, res)
	}

	return map[string]any{"files": results}, nil
}

type exportParams struct {
	FileID     string `json:"file_id"`
	MIMEType   string `json:"mime_type"`
	SaveToFile bool   `json:"save_to_file"`
	OutputPath string `json:"output_path"`
}

func toolExportDocText(params, _ json.RawMessage) (any, error) {
	p := exportParams{SaveToFile: true}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.FileID == "" {
		return nil, fmt.Errorf("file_id is required")
	}
	if p.MIMEType == "" {
		p.MIMEType = "text/plain"
	}

	if !p.SaveToFile {
		return exportFile(p.FileID, p.MIMEType)
	}
	return exportFileToLocal(p.FileID, p.MIMEType, p.OutputPath, ".txt")
}

func toolExportSheetCSV(params, _ json.RawMessage) (any, error) {
	p := exportParams{SaveToFile: true}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.FileID == "" {
		return nil, fmt.Errorf("file_id is required")
	}

	if !p.SaveToFile {
		return exportFile(p.FileID, "text/csv")
	}
	return exportFileToLocal(p.FileID, "text/csv", p.OutputPath, ".csv")
}

func toolExportSlidesPDF(params, _ json.RawMessage) (any, error) {
	var p struct {
		FileID string `json:"file_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.FileID == "" {
		return nil, fmt.Errorf("file_id is required")
	}

	return exportFile(p.FileID, "application/pdf")
}

func exportFile(fileID, mimeType string) (any, error) {
	resp, err := driveSvc.Files.Export(fileID, mimeType).Download()
	if err != nil {
		return nil, fmt.Errorf("export file: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read export: %w", err)
	}

	// Text content returned as UTF-8, binary as base64
	if strings.HasPrefix(mimeType, "text/") {
		return map[string]string{
			"encoding": "utf-8",
			"content":  string(buf),
		}, nil
	}
	return map[string]string{
		"encoding": "base64",
		"content":  base64.StdEncoding.EncodeToString(buf),
	}, nil
}

func exportFileToLocal(fileID, mimeType, outputPath, ext string) (any, error) {
	resp, err := driveSvc.Files.Export(fileID, mimeType).Download()
	if err != nil {
		return nil, fmt.Errorf("export file: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read export: %w", err)
	}
	content := string(buf)

	if outputPath == "" {
		outputPath = fmt.Sprintf("%s%s", fileID, ext)
	}
	savedPath, err := writeExportFile("", outputPath, content)
	if err != nil {
		return nil, fmt.Errorf("save file: %w", err)
	}

	lines := strings.Count(content, "\n") + 1
	words := len(strings.Fields(content))

	return map[string]any{
		"status":      "saved",
		"file_id":     fileID,
		"output_path": savedPath,
		"stats": map[string]int{
			"lines":      lines,
			"words":      words,
			"characters": len(content),
		},
		"note": fmt.Sprintf("Saved to %s. File is NOT loaded into context.", savedPath),
	}, nil
}

// cleanGoogleDocsCSS removes CSS artifacts that Google Docs injects into
// exported HTML (list styles, @import rules, etc.).
func cleanGoogleDocsCSS(md string) string {
	var cleaned []string
	skip := false
	for _, line := range strings.Split(md, "\n") {
		if strings.Contains(line, "@import") ||
			strings.Contains(line, "list-style-type") ||
			strings.Contains(line, ".lst-kix") {
			skip = true
			continue
		}
		if skip && strings.TrimSpace(line) != "" &&
			!strings.Contains(line, "@import") &&
			!strings.Contains(line, "list-style") &&
			!strings.Contains(line, ".lst-kix") &&
			!strings.Contains(line, "ul.") &&
			!strings.Contains(line, "> li:before") {
			skip = false
		}
		if !skip {
			cleaned = append(cleaned, line)
		}
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

type exportMarkdownParams struct {
	FileIDOrURL string `json:"file_id_or_url"`
	SaveToFile  bool   `json:"save_to_file"`
	OutputPath  string `json:"output_path"`
}

func toolExportDocMarkdown(params, _ json.RawMessage) (any, error) {
	p := exportMarkdownParams{SaveToFile: true}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.FileIDOrURL == "" {
		return nil, fmt.Errorf("file_id_or_url is required")
	}

	fileID := p.FileIDOrURL
	if strings.Contains(fileID, "google.com") {
		extracted := extractFileID(fileID)
		if extracted == "" {
			return map[string]string{
				"error": "could not extract file ID from URL",
				"url":   p.FileIDOrURL,
			}, nil
		}
		fileID = extracted
	}

	// Get metadata
	meta, err := driveSvc.Files.Get(fileID).
		Fields("id,name,mimeType").
		SupportsAllDrives(true).
		Do()
	if err != nil {
		return nil, fmt.Errorf("get file metadata: %w", err)
	}

	if meta.MimeType != "application/vnd.google-apps.document" {
		return map[string]any{
			"error":      fmt.Sprintf("file is not a Google Doc (MIME type: %s)", meta.MimeType),
			"file_id":    fileID,
			"file_name":  meta.Name,
			"suggestion": "Use drive_export_google_sheet_csv for Sheets or drive_export_slides_pdf for Slides",
		}, nil
	}

	// Export as HTML and convert to Markdown
	resp, err := driveSvc.Files.Export(fileID, "text/html").Download()
	if err != nil {
		return nil, fmt.Errorf("export doc: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	htmlBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read export: %w", err)
	}

	content, err := htmltomarkdown.ConvertString(string(htmlBytes))
	if err != nil {
		return nil, fmt.Errorf("convert to markdown: %w", err)
	}

	// Clean up Google Docs CSS artifacts
	content = cleanGoogleDocsCSS(content)

	if !p.SaveToFile {
		return map[string]any{
			"file_id":        fileID,
			"document_title": meta.Name,
			"content":        content,
			"warning":        "Full content returned — consumes tokens. Use save_to_file=true to save locally.",
		}, nil
	}

	outputPath, err := writeExportFile(meta.Name, p.OutputPath, content)
	if err != nil {
		return nil, fmt.Errorf("save file: %w", err)
	}

	lines := strings.Count(content, "\n") + 1
	words := len(strings.Fields(content))

	return map[string]any{
		"status":         "saved",
		"file_id":        fileID,
		"document_title": meta.Name,
		"output_path":    outputPath,
		"stats": map[string]int{
			"lines":      lines,
			"words":      words,
			"characters": len(content),
		},
		"note": fmt.Sprintf("Saved to %s. File is NOT loaded into context.", outputPath),
	}, nil
}

type searchFilesParams struct {
	Query    string `json:"query"`
	PageSize int    `json:"page_size"`
	OrderBy  string `json:"order_by"`
	Fields   string `json:"fields"`
}

func toolSearchFiles(params, _ json.RawMessage) (any, error) {
	var p searchFilesParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.Query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if p.PageSize == 0 {
		p.PageSize = 25
	}
	if p.OrderBy == "" {
		p.OrderBy = "modifiedTime desc"
	}
	if p.Fields == "" {
		p.Fields = "files(id,name,webViewLink,mimeType,owners,modifiedTime,size),nextPageToken"
	}

	res, err := driveSvc.Files.List().
		Q(p.Query).
		PageSize(int64(p.PageSize)).
		OrderBy(p.OrderBy).
		Fields(googleapi.Field(p.Fields)).
		IncludeItemsFromAllDrives(true).
		SupportsAllDrives(true).
		Do()
	if err != nil {
		return nil, fmt.Errorf("search files: %w", err)
	}
	return res, nil
}

type searchTextParams struct {
	Text           string   `json:"text"`
	PageSize       int      `json:"page_size"`
	InNameOnly     bool     `json:"in_name_only"`
	MIMETypes      []string `json:"mime_types"`
	Owners         []string `json:"owners"`
	IncludeTrashed bool     `json:"include_trashed"`
	OrderBy        string   `json:"order_by"`
	Fields         string   `json:"fields"`
}

func toolSearchText(params, _ json.RawMessage) (any, error) {
	var p searchTextParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.Text == "" {
		return nil, fmt.Errorf("text is required")
	}
	if p.PageSize == 0 {
		p.PageSize = 25
	}
	if p.OrderBy == "" {
		p.OrderBy = "modifiedTime desc"
	}
	if p.Fields == "" {
		p.Fields = "files(id,name,webViewLink,mimeType,owners,modifiedTime,size),nextPageToken"
	}

	q := buildSearchQuery(p.Text, p.InNameOnly, p.MIMETypes, p.Owners, p.IncludeTrashed)

	res, err := driveSvc.Files.List().
		Q(q).
		PageSize(int64(p.PageSize)).
		OrderBy(p.OrderBy).
		Fields(googleapi.Field(p.Fields)).
		IncludeItemsFromAllDrives(true).
		SupportsAllDrives(true).
		Do()
	if err != nil {
		return nil, fmt.Errorf("search text: %w", err)
	}
	return res, nil
}

// buildSearchQuery constructs a Drive API query string from search
// parameters. Pure function — no service access.
func buildSearchQuery(text string, inNameOnly bool, mimeTypes, owners []string, includeTrashed bool) string {
	escaped := strings.ReplaceAll(text, "'", "\\'")
	var clauses []string

	if inNameOnly {
		clauses = append(clauses, fmt.Sprintf("name contains '%s'", escaped))
	} else {
		clauses = append(clauses, fmt.Sprintf("(name contains '%s' or fullText contains '%s')", escaped, escaped))
	}

	if len(mimeTypes) > 0 {
		var mt []string
		for _, m := range mimeTypes {
			mt = append(mt, fmt.Sprintf("mimeType = '%s'", strings.ReplaceAll(m, "'", "\\'")))
		}
		if len(mt) == 1 {
			clauses = append(clauses, mt[0])
		} else {
			clauses = append(clauses, "("+strings.Join(mt, " or ")+")")
		}
	}

	if len(owners) > 0 {
		var own []string
		for _, o := range owners {
			own = append(own, fmt.Sprintf("'%s' in owners", strings.ReplaceAll(o, "'", "\\'")))
		}
		if len(own) == 1 {
			clauses = append(clauses, own[0])
		} else {
			clauses = append(clauses, "("+strings.Join(own, " or ")+")")
		}
	}

	if !includeTrashed {
		clauses = append(clauses, "trashed = false")
	}

	return strings.Join(clauses, " and ")
}

func confineToSessionDir(filePath string) (string, error) {
	if sessionDir == "" {
		return "", fmt.Errorf("file_path requires a configured session directory")
	}

	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(sessionDir, filePath)
	}

	resolvedSession, err := filepath.EvalSymlinks(sessionDir)
	if err != nil {
		return "", fmt.Errorf("resolve session dir: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	absSession, err := filepath.Abs(resolvedSession)
	if err != nil {
		return "", fmt.Errorf("abs session dir: %w", err)
	}
	absResolved, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("abs file path: %w", err)
	}

	if !strings.HasPrefix(absResolved, absSession+string(os.PathSeparator)) &&
		absResolved != absSession {
		return "", fmt.Errorf("file_path must be under the session directory")
	}
	return absResolved, nil
}

var errNoWriteScope = fmt.Errorf("write operations require re-authorization with drive (read-write) scope; " +
	"current token has drive.readonly — run: wtmcpctl credentials google drive")

var (
	writeScopeMu     sync.Mutex
	writeScopeProbed bool
	hasWriteScope    bool
)

func requireWriteScope() error {
	writeScopeMu.Lock()
	defer writeScopeMu.Unlock()

	if !writeScopeProbed {
		// Probe write capability: GenerateIds requires drive (not
		// drive.readonly) scope and creates no state.
		if driveSvc != nil {
			if _, err := driveSvc.Files.GenerateIds().Count(1).Do(); err != nil {
				var ae *googleapi.Error
				if errors.As(err, &ae) && ae.Code == http.StatusForbidden {
					hasWriteScope = false
					writeScopeProbed = true
					log.Println("write scope not available, write tools will return re-auth guidance")
				} else {
					// Transient errors (network, 500) leave
					// writeScopeProbed false so the next call retries.
					// Allow the call through — the actual API operation
					// will surface a meaningful error if needed.
					log.Printf("warning: write scope probe failed: %v", err)
					return nil
				}
			} else {
				hasWriteScope = true
				writeScopeProbed = true
			}
		} else {
			hasWriteScope = true
			writeScopeProbed = true
		}
	}

	if !hasWriteScope {
		return errNoWriteScope
	}
	return nil
}

// --- Write tool param types ---

type uploadFileParams struct {
	FilePath       string `json:"file_path"`
	Name           string `json:"name"`
	MIMEType       string `json:"mime_type"`
	ParentFolderID string `json:"parent_folder_id"`
	DryRun         bool   `json:"dry_run"`
}

type renameFileParams struct {
	FileID        string `json:"file_id"`
	Name          string `json:"name"`
	AddParents    string `json:"add_parents"`
	RemoveParents string `json:"remove_parents"`
	DryRun        bool   `json:"dry_run"`
}

type copyFileParams struct {
	FileID         string `json:"file_id"`
	Name           string `json:"name"`
	ParentFolderID string `json:"parent_folder_id"`
	DryRun         bool   `json:"dry_run"`
}

type deleteFileParams struct {
	FileID string `json:"file_id"`
	DryRun bool   `json:"dry_run"`
}

type createFolderParams struct {
	Name           string `json:"name"`
	ParentFolderID string `json:"parent_folder_id"`
	DryRun         bool   `json:"dry_run"`
}

// --- Write tool implementations ---

func toolCreateFolder(params, _ json.RawMessage) (any, error) {
	if err := requireWriteScope(); err != nil {
		return nil, err
	}

	p := createFolderParams{DryRun: true}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	if p.DryRun {
		result := map[string]any{
			"dry_run": true,
			"action":  "drive_create_folder",
			"name":    p.Name,
		}
		if p.ParentFolderID != "" {
			result["parent_folder_id"] = p.ParentFolderID
		}
		return result, nil
	}

	meta := &drive.File{
		Name:     p.Name,
		MimeType: "application/vnd.google-apps.folder",
	}
	if p.ParentFolderID != "" {
		meta.Parents = []string{p.ParentFolderID}
	}

	res, err := driveSvc.Files.Create(meta).
		SupportsAllDrives(true).
		Fields("id,name,webViewLink,mimeType").
		Do()
	if err != nil {
		return nil, fmt.Errorf("create folder: %w", err)
	}
	return res, nil
}

func toolUploadFile(params, _ json.RawMessage) (any, error) {
	if err := requireWriteScope(); err != nil {
		return nil, err
	}

	p := uploadFileParams{DryRun: true}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.FilePath == "" {
		return nil, fmt.Errorf("file_path is required")
	}

	resolved, err := confineToSessionDir(p.FilePath)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}

	name := p.Name
	if name == "" {
		name = filepath.Base(resolved)
	}

	if p.DryRun {
		result := map[string]any{
			"dry_run": true,
			"action":  "drive_upload_file",
			"name":    name,
			"size":    info.Size(),
		}
		if p.MIMEType != "" {
			result["mime_type"] = p.MIMEType
		}
		if p.ParentFolderID != "" {
			result["parent_folder_id"] = p.ParentFolderID
		}
		return result, nil
	}

	f, err := os.Open(resolved) //nolint:gosec // path validated above
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close() //nolint:errcheck,gosec // read-only file

	meta := &drive.File{Name: name}
	if p.ParentFolderID != "" {
		meta.Parents = []string{p.ParentFolderID}
	}

	var call *drive.FilesCreateCall
	if p.MIMEType != "" {
		call = driveSvc.Files.Create(meta).Media(f, googleapi.ContentType(p.MIMEType))
	} else {
		call = driveSvc.Files.Create(meta).Media(f)
	}
	call = call.SupportsAllDrives(true).Fields("id,name,webViewLink,mimeType,size")

	res, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("upload file: %w", err)
	}
	return res, nil
}

func toolRenameFile(params, _ json.RawMessage) (any, error) {
	if err := requireWriteScope(); err != nil {
		return nil, err
	}

	p := renameFileParams{DryRun: true}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.FileID == "" {
		return nil, fmt.Errorf("file_id is required")
	}
	if p.Name == "" && p.AddParents == "" && p.RemoveParents == "" {
		return nil, fmt.Errorf("at least one of name, add_parents, or remove_parents is required")
	}

	if p.DryRun {
		// Fetch file metadata for the preview.
		file, err := driveSvc.Files.Get(p.FileID).
			SupportsAllDrives(true).
			Fields("id,name,mimeType").
			Do()
		if err != nil {
			return nil, fmt.Errorf("get file for preview: %w", err)
		}

		result := map[string]any{
			"dry_run":   true,
			"action":    "drive_rename_file",
			"file_id":   file.Id,
			"file_name": file.Name,
			"mime_type": file.MimeType,
		}
		if p.Name != "" {
			result["new_name"] = p.Name
		}
		if p.AddParents != "" {
			result["add_parents"] = p.AddParents
		}
		if p.RemoveParents != "" {
			result["remove_parents"] = p.RemoveParents
		}
		return result, nil
	}

	meta := &drive.File{}
	if p.Name != "" {
		meta.Name = p.Name
	}

	call := driveSvc.Files.Update(p.FileID, meta).SupportsAllDrives(true)
	if p.AddParents != "" {
		call = call.AddParents(p.AddParents)
	}
	if p.RemoveParents != "" {
		call = call.RemoveParents(p.RemoveParents)
	}
	call = call.Fields("id,name,webViewLink,mimeType,parents")

	res, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("rename file: %w", err)
	}
	return res, nil
}

func toolCopyFile(params, _ json.RawMessage) (any, error) {
	if err := requireWriteScope(); err != nil {
		return nil, err
	}

	p := copyFileParams{DryRun: true}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.FileID == "" {
		return nil, fmt.Errorf("file_id is required")
	}

	if p.DryRun {
		result := map[string]any{
			"dry_run": true,
			"action":  "drive_copy_file",
			"file_id": p.FileID,
		}
		if p.Name != "" {
			result["name"] = p.Name
		}
		if p.ParentFolderID != "" {
			result["parent_folder_id"] = p.ParentFolderID
		}
		return result, nil
	}

	meta := &drive.File{}
	if p.Name != "" {
		meta.Name = p.Name
	}
	if p.ParentFolderID != "" {
		meta.Parents = []string{p.ParentFolderID}
	}

	res, err := driveSvc.Files.Copy(p.FileID, meta).
		SupportsAllDrives(true).
		Fields("id,name,webViewLink,mimeType,size").
		Do()
	if err != nil {
		return nil, fmt.Errorf("copy file: %w", err)
	}
	return res, nil
}

func toolDeleteFile(params, _ json.RawMessage) (any, error) {
	if err := requireWriteScope(); err != nil {
		return nil, err
	}

	p := deleteFileParams{DryRun: true}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.FileID == "" {
		return nil, fmt.Errorf("file_id is required")
	}

	if p.DryRun {
		// Fetch file metadata for the preview.
		file, err := driveSvc.Files.Get(p.FileID).
			SupportsAllDrives(true).
			Fields("id,name,mimeType,size").
			Do()
		if err != nil {
			return nil, fmt.Errorf("get file for preview: %w", err)
		}
		return map[string]any{
			"dry_run":   true,
			"action":    "drive_delete_file",
			"file_id":   file.Id,
			"file_name": file.Name,
			"mime_type": file.MimeType,
			"size":      file.Size,
		}, nil
	}

	// Soft delete: move to trash (recoverable).
	res, err := driveSvc.Files.Update(p.FileID, &drive.File{Trashed: true}).
		SupportsAllDrives(true).
		Fields("id,name,trashed").
		Do()
	if err != nil {
		return nil, fmt.Errorf("trash file: %w", err)
	}
	return res, nil
}
