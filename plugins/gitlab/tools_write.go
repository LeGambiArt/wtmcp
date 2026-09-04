package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	gogitlab "gitlab.com/gitlab-org/api/client-go"
)

const maxBodyLen = 65000

var validBranch = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9._/-]*[a-zA-Z0-9])?$`)

// validProjectPath matches GitLab namespace paths (max 255 chars enforced separately).
var validProjectPath = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]*[a-zA-Z0-9]$`)

func validateBranch(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("branch name is required")
	}
	if !validBranch.MatchString(name) {
		return fmt.Errorf("invalid branch name: %q", name)
	}
	if strings.Contains(name, "..") || strings.Contains(name, "//") {
		return fmt.Errorf("invalid branch name: %q (contains .. or //)", name)
	}
	if strings.HasSuffix(name, ".lock") {
		return fmt.Errorf("invalid branch name: %q (ends with .lock)", name)
	}
	if strings.Contains(name, "/.") {
		return fmt.Errorf("invalid branch name: %q (component starts with '.')", name)
	}
	return nil
}

func validatePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path is required")
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." {
			return fmt.Errorf("path traversal in path: %q", path)
		}
	}
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("absolute path not allowed: %q", path)
	}
	return nil
}

func isDryRun(v *bool) bool {
	return v == nil || *v
}

// --- gitlab_create_mr_discussion ---

type createMRDiscussionParams struct {
	instanceParam
	ProjectID string `json:"project_id"`
	MrIID     int64  `json:"mr_iid"`
	Body      string `json:"body"`
	NewPath   string `json:"new_path"`
	OldPath   string `json:"old_path"`
	NewLine   int64  `json:"new_line"`
	OldLine   int64  `json:"old_line"`
	BaseSHA   string `json:"base_sha"`
	HeadSHA   string `json:"head_sha"`
	StartSHA  string `json:"start_sha"`
	DryRun    *bool  `json:"dry_run"`
}

func toolCreateMRDiscussion(params, _ json.RawMessage) (any, error) {
	var p createMRDiscussionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.ProjectID == "" || p.MrIID <= 0 {
		return nil, fmt.Errorf("project_id and mr_iid are required")
	}
	if strings.TrimSpace(p.Body) == "" {
		return nil, fmt.Errorf("body is required")
	}
	if len([]rune(p.Body)) > maxBodyLen {
		return nil, fmt.Errorf("body exceeds %d character limit (%d chars)", maxBodyLen, len([]rune(p.Body)))
	}
	if p.NewPath == "" {
		return nil, fmt.Errorf("new_path is required")
	}
	if p.NewLine <= 0 && p.OldLine <= 0 {
		return nil, fmt.Errorf("at least one of new_line or old_line must be positive")
	}
	if p.OldPath == "" {
		p.OldPath = p.NewPath
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	baseSHA, headSHA, startSHA := p.BaseSHA, p.HeadSHA, p.StartSHA
	if baseSHA == "" || headSHA == "" || startSHA == "" {
		baseSHA, headSHA, startSHA, err = fetchDiffRefs(client, p.ProjectID, p.MrIID)
		if err != nil {
			return nil, err
		}
	}

	if isDryRun(p.DryRun) {
		preview := map[string]any{
			"dry_run":    true,
			"action":     "gitlab_create_mr_discussion",
			"project_id": p.ProjectID,
			"mr_iid":     p.MrIID,
			"new_path":   p.NewPath,
			"old_path":   p.OldPath,
			"base_sha":   baseSHA,
			"head_sha":   headSHA,
			"start_sha":  startSHA,
		}
		if p.NewLine > 0 {
			preview["new_line"] = p.NewLine
		}
		if p.OldLine > 0 {
			preview["old_line"] = p.OldLine
		}
		runes := []rune(p.Body)
		if len(runes) > 200 {
			runes = runes[:200]
		}
		preview["body_preview"] = string(runes)
		return preview, nil
	}

	pos := &gogitlab.PositionOptions{
		BaseSHA:      &baseSHA,
		HeadSHA:      &headSHA,
		StartSHA:     &startSHA,
		PositionType: gogitlab.Ptr("text"),
		NewPath:      &p.NewPath,
		OldPath:      &p.OldPath,
	}
	if p.NewLine > 0 {
		pos.NewLine = gogitlab.Ptr(p.NewLine)
	}
	if p.OldLine > 0 {
		pos.OldLine = gogitlab.Ptr(p.OldLine)
	}

	disc, _, err := client.Discussions.CreateMergeRequestDiscussion(
		p.ProjectID, p.MrIID,
		&gogitlab.CreateMergeRequestDiscussionOptions{
			Body:     &p.Body,
			Position: pos,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("create discussion (check that position SHAs match the current MR diff and the file path exists in the diff): %w", err)
	}

	return map[string]any{
		"id":         disc.ID,
		"project_id": p.ProjectID,
		"mr_iid":     p.MrIID,
		"new_path":   p.NewPath,
		"created":    true,
	}, nil
}

// --- gitlab_add_mr_note ---

type addMRNoteParams struct {
	instanceParam
	ProjectID string `json:"project_id"`
	MrIID     int64  `json:"mr_iid"`
	Body      string `json:"body"`
	DryRun    *bool  `json:"dry_run"`
}

func toolAddMRNote(params, _ json.RawMessage) (any, error) {
	var p addMRNoteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.ProjectID == "" || p.MrIID <= 0 {
		return nil, fmt.Errorf("project_id and mr_iid are required")
	}
	if strings.TrimSpace(p.Body) == "" {
		return nil, fmt.Errorf("body is required")
	}
	if len([]rune(p.Body)) > maxBodyLen {
		return nil, fmt.Errorf("body exceeds %d character limit (%d chars)", maxBodyLen, len([]rune(p.Body)))
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	if isDryRun(p.DryRun) {
		runes := []rune(p.Body)
		if len(runes) > 200 {
			runes = runes[:200]
		}
		return map[string]any{
			"dry_run":      true,
			"action":       "gitlab_add_mr_note",
			"project_id":   p.ProjectID,
			"mr_iid":       p.MrIID,
			"body_preview": string(runes),
		}, nil
	}

	note, _, err := client.Notes.CreateMergeRequestNote(
		p.ProjectID, p.MrIID,
		&gogitlab.CreateMergeRequestNoteOptions{
			Body: &p.Body,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("create MR note: %w", err)
	}

	return map[string]any{
		"id":         note.ID,
		"project_id": p.ProjectID,
		"mr_iid":     p.MrIID,
		"created":    true,
	}, nil
}

// --- gitlab_create_merge_request ---

type createMergeRequestParams struct {
	instanceParam
	ProjectID          string   `json:"project_id"`
	SourceBranch       string   `json:"source_branch"`
	TargetBranch       string   `json:"target_branch"`
	TargetProjectID    string   `json:"target_project_id"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	Labels             []string `json:"labels"`
	RemoveSourceBranch *bool    `json:"remove_source_branch"`
	Squash             *bool    `json:"squash"`
	Draft              bool     `json:"draft"`
	DryRun             *bool    `json:"dry_run"`
}

func toolCreateMergeRequest(params, _ json.RawMessage) (any, error) {
	var p createMergeRequestParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	if strings.TrimSpace(p.SourceBranch) == "" {
		return nil, fmt.Errorf("source_branch is required")
	}
	if strings.TrimSpace(p.TargetBranch) == "" {
		return nil, fmt.Errorf("target_branch is required")
	}
	if strings.TrimSpace(p.Title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	if len([]rune(p.Description)) > maxBodyLen {
		return nil, fmt.Errorf("description exceeds %d character limit (%d chars)", maxBodyLen, len([]rune(p.Description)))
	}

	title := p.Title
	if p.Draft && !strings.HasPrefix(strings.ToLower(p.Title), "draft: ") {
		title = "Draft: " + title
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	// Resolve target_project_id to a numeric ID if provided.
	// When the value is a path string (not a numeric ID), a live GET /projects/:path
	// is issued to resolve it — this occurs even in dry-run mode.
	var targetProjectID *int64
	if tid := strings.TrimSpace(p.TargetProjectID); tid != "" {
		if n, err := strconv.ParseInt(tid, 10, 64); err == nil {
			if n <= 0 {
				return nil, fmt.Errorf("target_project_id: numeric ID must be positive")
			}
			targetProjectID = &n
		} else {
			if len(tid) > 255 || !validProjectPath.MatchString(tid) {
				return nil, fmt.Errorf("target_project_id: invalid project path %q", tid)
			}
			proj, _, err := client.Projects.GetProject(tid, nil)
			if err != nil {
				return nil, fmt.Errorf("target_project_id: project not found or not accessible")
			}
			n := proj.ID
			targetProjectID = &n
		}
	}

	if isDryRun(p.DryRun) {
		m := map[string]any{
			"dry_run":       true,
			"action":        "gitlab_create_merge_request",
			"project_id":    p.ProjectID,
			"source_branch": p.SourceBranch,
			"target_branch": p.TargetBranch,
			"title":         title,
		}
		if targetProjectID != nil {
			m["target_project_id"] = *targetProjectID
		}
		if p.Description != "" {
			runes := []rune(p.Description)
			if len(runes) > 200 {
				runes = runes[:200]
			}
			m["description_preview"] = string(runes)
		}
		if len(p.Labels) > 0 {
			m["labels"] = p.Labels
		}
		if p.Draft {
			m["draft"] = true
		}
		if p.RemoveSourceBranch != nil {
			m["remove_source_branch"] = *p.RemoveSourceBranch
		}
		if p.Squash != nil {
			m["squash"] = *p.Squash
		}
		return m, nil
	}

	opts := &gogitlab.CreateMergeRequestOptions{
		Title:           &title,
		SourceBranch:    &p.SourceBranch,
		TargetBranch:    &p.TargetBranch,
		TargetProjectID: targetProjectID,
	}
	if p.RemoveSourceBranch != nil {
		opts.RemoveSourceBranch = p.RemoveSourceBranch
	}
	if p.Squash != nil {
		opts.Squash = p.Squash
	}
	if p.Description != "" {
		opts.Description = &p.Description
	}
	if len(p.Labels) > 0 {
		labels := gogitlab.LabelOptions(p.Labels)
		opts.Labels = &labels
	}

	mr, _, err := client.MergeRequests.CreateMergeRequest(p.ProjectID, opts)
	if err != nil {
		return nil, fmt.Errorf("create merge request: %w", err)
	}

	result := map[string]any{
		"iid":           mr.IID,
		"project_id":    p.ProjectID,
		"title":         mr.Title,
		"web_url":       mr.WebURL,
		"state":         mr.State,
		"source_branch": mr.SourceBranch,
		"target_branch": mr.TargetBranch,
		"created":       true,
	}
	if mr.TargetProjectID != 0 {
		result["target_project_id"] = mr.TargetProjectID
	}
	return result, nil
}

// --- gitlab_create_branch ---

type createBranchParams struct {
	instanceParam
	ProjectID    string `json:"project_id"`
	BranchName   string `json:"branch_name"`
	SourceBranch string `json:"source_branch"`
	DryRun       *bool  `json:"dry_run"`
}

func toolCreateBranch(params, _ json.RawMessage) (any, error) {
	var p createBranchParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	if err := validateBranch(p.BranchName); err != nil {
		return nil, err
	}
	if p.SourceBranch == "" {
		p.SourceBranch = "main"
	}
	if err := validateBranch(p.SourceBranch); err != nil {
		return nil, fmt.Errorf("source_branch: %w", err)
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	if isDryRun(p.DryRun) {
		return map[string]any{
			"dry_run":       true,
			"action":        "gitlab_create_branch",
			"project_id":    p.ProjectID,
			"branch_name":   p.BranchName,
			"source_branch": p.SourceBranch,
		}, nil
	}

	branch, _, err := client.Branches.CreateBranch(p.ProjectID, &gogitlab.CreateBranchOptions{
		Branch: &p.BranchName,
		Ref:    &p.SourceBranch,
	})
	if err != nil {
		return nil, fmt.Errorf("create branch: %w", err)
	}

	result := map[string]any{
		"name":       branch.Name,
		"web_url":    branch.WebURL,
		"created":    true,
		"project_id": p.ProjectID,
	}
	if branch.Commit != nil {
		result["commit_id"] = branch.Commit.ShortID
	}
	return result, nil
}

// --- gitlab_create_or_update_file ---

type createOrUpdateFileParams struct {
	instanceParam
	ProjectID     string `json:"project_id"`
	Path          string `json:"path"`
	Content       string `json:"content"`
	CommitMessage string `json:"message"`
	Branch        string `json:"branch"`
	DryRun        *bool  `json:"dry_run"`
}

func toolCreateOrUpdateFile(params, _ json.RawMessage) (any, error) {
	var p createOrUpdateFileParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	if err := validatePath(p.Path); err != nil {
		return nil, err
	}
	if p.Content == "" {
		return nil, fmt.Errorf("content is required")
	}
	if len(p.Content) > 1_000_000 {
		return nil, fmt.Errorf("content exceeds 1MB limit (%d bytes)", len(p.Content))
	}
	if strings.TrimSpace(p.CommitMessage) == "" {
		return nil, fmt.Errorf("message is required")
	}
	if p.Branch != "" {
		if err := validateBranch(p.Branch); err != nil {
			return nil, fmt.Errorf("branch: %w", err)
		}
	}
	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	getOpts := &gogitlab.GetFileOptions{}
	if p.Branch != "" {
		getOpts.Ref = &p.Branch
	}
	_, getResp, getErr := client.RepositoryFiles.GetFile(p.ProjectID, p.Path, getOpts)
	isUpdate := getErr == nil
	if getErr != nil {
		is404 := getResp != nil && getResp.StatusCode == http.StatusNotFound
		if !is404 {
			return nil, fmt.Errorf("check if file exists: %w", getErr)
		}
	}

	if isDryRun(p.DryRun) {
		m := map[string]any{
			"dry_run":         true,
			"action":          "gitlab_create_or_update_file",
			"project_id":      p.ProjectID,
			"path":            p.Path,
			"branch":          p.Branch,
			"is_update":       isUpdate,
			"message_preview": p.CommitMessage,
		}
		if len(p.CommitMessage) > 200 {
			m["message_preview"] = p.CommitMessage[:200]
		}
		return m, nil
	}

	updateOpts := &gogitlab.UpdateFileOptions{
		Content:       &p.Content,
		CommitMessage: &p.CommitMessage,
	}
	createOpts := &gogitlab.CreateFileOptions{
		Content:       &p.Content,
		CommitMessage: &p.CommitMessage,
	}
	if p.Branch != "" {
		updateOpts.Branch = &p.Branch
		createOpts.Branch = &p.Branch
	}

	if isUpdate {
		_, _, err = client.RepositoryFiles.UpdateFile(p.ProjectID, p.Path, updateOpts)
	} else {
		_, _, err = client.RepositoryFiles.CreateFile(p.ProjectID, p.Path, createOpts)
	}
	if err != nil {
		action := "create"
		if isUpdate {
			action = "update"
		}
		return nil, fmt.Errorf("%s file: %w", action, err)
	}

	return map[string]any{
		"file_path":  p.Path,
		"branch":     p.Branch,
		"is_update":  isUpdate,
		"project_id": p.ProjectID,
		"created":    true,
	}, nil
}

// --- gitlab_commit_files ---

type commitFileEntry struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type commitFilesParams struct {
	instanceParam
	ProjectID string            `json:"project_id"`
	Branch    string            `json:"branch"`
	Files     []commitFileEntry `json:"files"`
	Message   string            `json:"message"`
	DryRun    *bool             `json:"dry_run"`
}

func toolCommitFiles(params, _ json.RawMessage) (any, error) {
	var p commitFilesParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	if len(p.Files) == 0 {
		return nil, fmt.Errorf("files is required and must be a non-empty array")
	}
	if strings.TrimSpace(p.Message) == "" {
		return nil, fmt.Errorf("message is required")
	}
	if p.Branch != "" {
		if err := validateBranch(p.Branch); err != nil {
			return nil, fmt.Errorf("branch: %w", err)
		}
	}

	for i, f := range p.Files {
		if err := validatePath(f.Path); err != nil {
			return nil, fmt.Errorf("files[%d]: %w", i, err)
		}
		if f.Content == "" {
			return nil, fmt.Errorf("files[%d]: content is required", i)
		}
		if len(f.Content) > 1_000_000 {
			return nil, fmt.Errorf("files[%d]: content exceeds 1MB limit (%d bytes)", i, len(f.Content))
		}
	}

	// Build file summary with modes for dry_run and result
	type fileSummary struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
	}
	fileSummaries := make([]fileSummary, len(p.Files))
	for i, f := range p.Files {
		mode := "100644"
		if strings.HasSuffix(f.Path, ".sh") {
			mode = "100755"
		}
		fileSummaries[i] = fileSummary{Path: f.Path, Mode: mode}
	}

	if isDryRun(p.DryRun) {
		return map[string]any{
			"dry_run":    true,
			"action":     "gitlab_commit_files",
			"project_id": p.ProjectID,
			"branch":     p.Branch,
			"message":    p.Message,
			"file_count": len(p.Files),
			"files":      fileSummaries,
		}, nil
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	// Check which files exist to determine create vs update action
	actions := make([]*gogitlab.CommitActionOptions, len(p.Files))
	for i, f := range p.Files {
		action := gogitlab.FileCreate
		getOpts := &gogitlab.GetFileOptions{}
		if p.Branch != "" {
			getOpts.Ref = &p.Branch
		}
		_, getResp, getErr := client.RepositoryFiles.GetFile(p.ProjectID, f.Path, getOpts)
		if getErr == nil {
			action = gogitlab.FileUpdate
		} else if getResp == nil || getResp.StatusCode != http.StatusNotFound {
			return nil, fmt.Errorf("check if file %q exists: %w", f.Path, getErr)
		}

		content := f.Content
		isExecutable := strings.HasSuffix(f.Path, ".sh")
		actions[i] = &gogitlab.CommitActionOptions{
			Action:          &action,
			FilePath:        &f.Path,
			Content:         &content,
			ExecuteFilemode: &isExecutable,
		}
	}

	opts := &gogitlab.CreateCommitOptions{
		Branch:        &p.Branch,
		CommitMessage: &p.Message,
		Actions:       actions,
	}

	commit, _, err := client.Commits.CreateCommit(p.ProjectID, opts)
	if err != nil {
		return nil, fmt.Errorf("create commit: %w", err)
	}

	return map[string]any{
		"commit":     commit.ShortID,
		"branch":     p.Branch,
		"project_id": p.ProjectID,
		"file_count": len(p.Files),
		"files":      fileSummaries,
	}, nil
}

// --- helpers ---

func fetchDiffRefs(client *gogitlab.Client, pid string, mrIID int64) (baseSHA, headSHA, startSHA string, err error) {
	mr, _, err := client.MergeRequests.GetMergeRequest(pid, mrIID, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("fetch MR diff refs: %w", err)
	}
	if mr.DiffRefs.BaseSha == "" || mr.DiffRefs.HeadSha == "" || mr.DiffRefs.StartSha == "" {
		return "", "", "", fmt.Errorf("MR %d has no diff refs (may be empty or in a conflicted state)", mrIID)
	}
	return mr.DiffRefs.BaseSha, mr.DiffRefs.HeadSha, mr.DiffRefs.StartSha, nil
}
