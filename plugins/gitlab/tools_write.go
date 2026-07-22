package main

import (
	"encoding/json"
	"fmt"
	"strings"

	gogitlab "gitlab.com/gitlab-org/api/client-go"
)

const maxBodyLen = 65000

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

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
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
	if p.Draft && !strings.HasPrefix(p.Title, "Draft: ") {
		title = "Draft: " + title
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
		return m, nil
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	opts := &gogitlab.CreateMergeRequestOptions{
		Title:        &title,
		SourceBranch: &p.SourceBranch,
		TargetBranch: &p.TargetBranch,
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

	return map[string]any{
		"iid":           mr.IID,
		"project_id":    p.ProjectID,
		"title":         mr.Title,
		"web_url":       mr.WebURL,
		"state":         mr.State,
		"source_branch": mr.SourceBranch,
		"target_branch": mr.TargetBranch,
		"created":       true,
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
