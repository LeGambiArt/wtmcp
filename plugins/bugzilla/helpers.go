// Package main implements the Bugzilla plugin handler for wtmcp.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LeGambiArt/wtmcp/pkg/handler"
	"github.com/LeGambiArt/wtmcp/pkg/pathutil"
)

var cfg struct {
	bugzillaURL string
	outputDir   string
	sessionDir  string
}

func initConfig(raw json.RawMessage) error {
	var c map[string]string
	if err := json.Unmarshal(raw, &c); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	cfg.bugzillaURL = c["bugzilla_url"]
	if cfg.bugzillaURL == "" {
		return fmt.Errorf("bugzilla_url is required")
	}
	cfg.bugzillaURL = strings.TrimRight(cfg.bugzillaURL, "/")
	cfg.outputDir = c["_output_dir"]
	cfg.sessionDir = c["_session_dir"]
	return nil
}

const (
	maxBugIDs           = 50
	maxLimit            = 200
	defaultSearchLimit  = 50
	defaultCommentLimit = 100
	maxAttachMB         = 6
	maxAttachBytes      = maxAttachMB * 1024 * 1024
)

func parseBugID(v any) (int, error) {
	switch val := v.(type) {
	case float64:
		id := int(val)
		if float64(id) != val {
			return 0, fmt.Errorf("bug_id must be a whole number, got %v", val)
		}
		if id <= 0 {
			return 0, fmt.Errorf("bug_id must be positive, got %d", id)
		}
		return id, nil
	case string:
		id, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil {
			return 0, fmt.Errorf("bug_id must be numeric, got %q", val)
		}
		if id <= 0 {
			return 0, fmt.Errorf("bug_id must be positive, got %d", id)
		}
		return id, nil
	case json.Number:
		id, err := strconv.Atoi(val.String())
		if err != nil {
			return 0, fmt.Errorf("bug_id must be numeric, got %q", val)
		}
		if id <= 0 {
			return 0, fmt.Errorf("bug_id must be positive, got %d", id)
		}
		return id, nil
	case nil:
		return 0, fmt.Errorf("bug_id is required")
	default:
		return 0, fmt.Errorf("bug_id must be a number, got %T", v)
	}
}

func validateBugIDs(ids string) ([]int, error) {
	ids = strings.TrimSpace(ids)
	if ids == "" {
		return nil, fmt.Errorf("bug_ids is required")
	}
	parts := strings.Split(ids, ",")
	seen := make(map[int]bool, len(parts))
	var result []int
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid bug ID %q: must be numeric", p)
		}
		if id <= 0 {
			return nil, fmt.Errorf("invalid bug ID %d: must be positive", id)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, id)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no valid bug IDs provided")
	}
	if len(result) > maxBugIDs {
		return nil, fmt.Errorf("too many bug IDs (%d): maximum is %d", len(result), maxBugIDs)
	}
	return result, nil
}

func capLimit(limit, ceiling int) int {
	if limit <= 0 {
		return ceiling
	}
	if limit > ceiling {
		return ceiling
	}
	return limit
}

func bugURL(id int) string {
	return fmt.Sprintf("%s/show_bug.cgi?id=%d", cfg.bugzillaURL, id)
}

func bugBrief(bug map[string]any) map[string]any {
	id, _ := bug["id"].(float64)
	brief := map[string]any{
		"id":        int(id),
		"summary":   bug["summary"],
		"status":    bug["status"],
		"priority":  bug["priority"],
		"severity":  bug["severity"],
		"product":   bug["product"],
		"component": bug["component"],
	}
	if id > 0 {
		brief["url"] = bugURL(int(id))
	}
	if v, ok := bug["assigned_to"]; ok {
		brief["assigned_to"] = v
	}
	if v, ok := bug["resolution"]; ok && v != "" {
		brief["resolution"] = v
	}
	return brief
}

func parseTime(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("empty date string")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format("2006-01-02T15:04:05Z"), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC().Format("2006-01-02T15:04:05Z"), nil
	}
	return "", fmt.Errorf("invalid date %q: expected RFC3339 or YYYY-MM-DD", s)
}

func dryRunPreview(method, path string, body any) map[string]any {
	preview := map[string]any{
		"dry_run": true,
		"method":  method,
		"path":    path,
	}
	if body != nil {
		preview["body"] = body
	}
	return preview
}

func parseAPIError(resp *handler.HTTPResponse) error {
	var apiErr struct {
		Error   bool   `json:"error"`
		Message string `json:"message"`
		Code    int    `json:"code"`
	}
	if err := json.Unmarshal(resp.Body, &apiErr); err == nil && apiErr.Message != "" {
		return &handler.Error{
			Code:    fmt.Sprintf("bugzilla_%d", resp.Status),
			Message: apiErr.Message,
		}
	}
	return &handler.Error{
		Code:    fmt.Sprintf("http_%d", resp.Status),
		Message: fmt.Sprintf("Bugzilla returned HTTP %d", resp.Status),
	}
}

func isDryRun(v *bool) bool {
	return v == nil || *v
}

func isBrief(v *bool) bool {
	return v == nil || *v
}

// confineRead is a transitional fallback for reading files in sessionDir
// that are not in outputDir (e.g., attachments). Should be removed when
// the core file I/O service supports sessionDir reads directly.
func confineRead(filePath string, allowedDirs ...string) (string, error) {
	if filePath == "" {
		return "", fmt.Errorf("file path is required")
	}

	if !filepath.IsAbs(filePath) && len(allowedDirs) > 0 && allowedDirs[0] != "" {
		filePath = filepath.Join(allowedDirs[0], filePath)
	}

	absFile, err := pathutil.ResolvePath(filePath)
	if err != nil {
		return "", err
	}

	for _, dir := range allowedDirs {
		if dir == "" {
			continue
		}
		absDir, err := pathutil.ResolvePath(dir)
		if err != nil {
			continue
		}
		if strings.HasPrefix(absFile, absDir+string(os.PathSeparator)) {
			info, err := os.Stat(absFile)
			if err != nil {
				return "", fmt.Errorf("stat: %w", err)
			}
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("not a regular file")
			}
			return absFile, nil
		}
	}
	return "", fmt.Errorf("path escapes allowed directories")
}

func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

func base64Encode(data string) string {
	return base64.StdEncoding.EncodeToString([]byte(data))
}

func parseIntIDs(strs []string) ([]int, error) {
	result := make([]int, 0, len(strs))
	for _, s := range strs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		id, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("invalid bug ID %q: must be numeric", s)
		}
		if id <= 0 {
			return nil, fmt.Errorf("invalid bug ID %d: must be positive", id)
		}
		result = append(result, id)
	}
	return result, nil
}

const maxFilenameLen = 200

func sanitizeFilename(name string, attID int) string {
	base := filepath.Base(name)
	if strings.ContainsRune(base, 0) || base == "" || base == "." || base == ".." {
		base = "attachment"
	}
	base = strings.ReplaceAll(base, ":", "_")
	if len(base) > maxFilenameLen {
		ext := filepath.Ext(base)
		if len(ext) >= maxFilenameLen {
			base = base[:maxFilenameLen]
		} else {
			base = base[:maxFilenameLen-len(ext)] + ext
		}
	}
	return fmt.Sprintf("%d_%s", attID, base)
}
