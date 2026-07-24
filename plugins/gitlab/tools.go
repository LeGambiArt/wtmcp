package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	gogitlab "gitlab.com/gitlab-org/api/client-go"
)

const maxLogBytes = 50000

// instanceParam is embedded in every tool's params struct.
type instanceParam struct {
	Instance string `json:"instance"`
}

// --- gitlab_get_commits ---

type getCommitsParams struct {
	instanceParam
	ProjectID  string `json:"project_id"`
	RefName    string `json:"ref_name"`
	Since      string `json:"since"`
	Until      string `json:"until"`
	Path       string `json:"path"`
	Author     string `json:"author"`
	MaxResults int    `json:"max_results"`
}

func toolGetCommits(params, _ json.RawMessage) (any, error) {
	var p getCommitsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	if p.RefName == "" {
		p.RefName = "main"
	}
	if p.MaxResults <= 0 {
		p.MaxResults = 20
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	perPage := int64(p.MaxResults)
	if perPage > 100 {
		perPage = 100
	}

	opts := &gogitlab.ListCommitsOptions{
		RefName:     &p.RefName,
		ListOptions: gogitlab.ListOptions{PerPage: perPage},
	}
	if p.Since != "" {
		t, err := parseTime(p.Since)
		if err != nil {
			return nil, fmt.Errorf("invalid since: %w", err)
		}
		opts.Since = t
	}
	if p.Until != "" {
		t, err := parseTime(p.Until)
		if err != nil {
			return nil, fmt.Errorf("invalid until: %w", err)
		}
		opts.Until = t
	}
	if p.Path != "" {
		opts.Path = &p.Path
	}
	if p.Author != "" {
		opts.Author = &p.Author
	}

	commits, _, err := client.Commits.ListCommits(p.ProjectID, opts)
	if err != nil {
		return nil, fmt.Errorf("list commits: %w", err)
	}

	var result []map[string]any
	for _, c := range commits {
		result = append(result, map[string]any{
			"id":            c.ID,
			"short_id":      c.ShortID,
			"title":         c.Title,
			"message":       c.Message,
			"author_name":   c.AuthorName,
			"author_email":  c.AuthorEmail,
			"authored_date": timeStr(c.AuthoredDate),
			"web_url":       c.WebURL,
		})
	}
	return result, nil
}

// --- gitlab_get_commit_diff ---

type getCommitDiffParams struct {
	instanceParam
	ProjectID string `json:"project_id"`
	CommitSHA string `json:"commit_sha"`
	Format    string `json:"format"`
	FilesOnly bool   `json:"files_only"`
	MaxFiles  int    `json:"max_files"`
}

const maxDiffBytes = 10000

func toolGetCommitDiff(params, _ json.RawMessage) (any, error) {
	var p getCommitDiffParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.ProjectID == "" || p.CommitSHA == "" {
		return nil, fmt.Errorf("project_id and commit_sha are required")
	}
	if p.Format == "" {
		p.Format = "json"
	}
	if p.MaxFiles <= 0 {
		p.MaxFiles = 20
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	diffs, _, err := client.Commits.GetCommitDiff(p.ProjectID, p.CommitSHA, nil)
	if err != nil {
		return nil, fmt.Errorf("get commit diff: %w", err)
	}

	totalFiles := len(diffs)
	truncatedFiles := totalFiles > p.MaxFiles
	if truncatedFiles {
		diffs = diffs[:p.MaxFiles]
	}

	var diffList []map[string]any
	for _, d := range diffs {
		entry := map[string]any{
			"old_path":     d.OldPath,
			"new_path":     d.NewPath,
			"new_file":     d.NewFile,
			"renamed_file": d.RenamedFile,
			"deleted_file": d.DeletedFile,
		}
		if !p.FilesOnly {
			diff := d.Diff
			if len(diff) > maxDiffBytes {
				diff = diff[:maxDiffBytes] + "\n... [TRUNCATED]"
				entry["truncated"] = true
			}
			entry["diff"] = diff
		}
		diffList = append(diffList, entry)
	}

	result := map[string]any{
		"commit_id":   p.CommitSHA,
		"format":      p.Format,
		"diffs":       diffList,
		"total_files": totalFiles,
	}
	if truncatedFiles {
		result["files_truncated"] = true
	}
	return result, nil
}

// --- gitlab_get_merge_request ---

type getMergeRequestParams struct {
	instanceParam
	ProjectID          string `json:"project_id"`
	MrIID              int64  `json:"mr_iid"`
	IncludeChanges     bool   `json:"include_changes"`
	IncludeSystemNotes bool   `json:"include_system_notes"`
}

func toolGetMergeRequest(params, _ json.RawMessage) (any, error) {
	var p getMergeRequestParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.ProjectID == "" || p.MrIID == 0 {
		return nil, fmt.Errorf("project_id and mr_iid are required")
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	mr, _, err := client.MergeRequests.GetMergeRequest(p.ProjectID, p.MrIID, nil)
	if err != nil {
		return nil, fmt.Errorf("get merge request: %w", err)
	}

	// Get notes/comments (filter system notes by default)
	notes, _, err := client.Notes.ListMergeRequestNotes(p.ProjectID, p.MrIID,
		&gogitlab.ListMergeRequestNotesOptions{ListOptions: gogitlab.ListOptions{PerPage: 100}})
	if err != nil {
		return nil, fmt.Errorf("list MR notes: %w", err)
	}

	var comments []map[string]any
	for _, n := range notes {
		if n.System && !p.IncludeSystemNotes {
			continue
		}
		comments = append(comments, map[string]any{
			"id":         n.ID,
			"author":     n.Author.Username,
			"created_at": timeStr(n.CreatedAt),
			"body":       n.Body,
			"system":     n.System,
			"resolvable": n.Resolvable,
			"resolved":   n.Resolved,
		})
	}

	result := map[string]any{
		"iid":           mr.IID,
		"title":         mr.Title,
		"description":   mr.Description,
		"state":         mr.State,
		"source_branch": mr.SourceBranch,
		"target_branch": mr.TargetBranch,
		"web_url":       mr.WebURL,
		"merge_status":  mr.DetailedMergeStatus,
		"sha":           mr.SHA,
		"created_at":    timeStr(mr.CreatedAt),
		"updated_at":    timeStr(mr.UpdatedAt),
		"draft":         mr.Draft,
		"comments":      comments,
	}
	if mr.Author != nil {
		result["author"] = mr.Author.Username
	}

	if mr.DiffRefs.BaseSha != "" {
		result["diff_refs"] = map[string]string{
			"base_sha":  mr.DiffRefs.BaseSha,
			"head_sha":  mr.DiffRefs.HeadSha,
			"start_sha": mr.DiffRefs.StartSha,
		}
	}

	// Only fetch diffs when explicitly requested
	if p.IncludeChanges {
		diffs, _, diffErr := client.MergeRequests.ListMergeRequestDiffs(p.ProjectID, p.MrIID, nil)
		if diffErr != nil {
			result["changes_error"] = diffErr.Error()
		} else {
			var changeList []map[string]string
			for _, d := range diffs {
				changeList = append(changeList, map[string]string{
					"old_path": d.OldPath,
					"new_path": d.NewPath,
					"diff":     d.Diff,
				})
			}
			result["changes"] = changeList
		}
	}

	return result, nil
}

// --- gitlab_get_pipeline_jobs ---

type getPipelineJobsParams struct {
	instanceParam
	ProjectID   string `json:"project_id"`
	PipelineID  int64  `json:"pipeline_id"`
	IncludeLogs bool   `json:"include_logs"`
}

func toolGetPipelineJobs(params, _ json.RawMessage) (any, error) {
	var p getPipelineJobsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.ProjectID == "" || p.PipelineID == 0 {
		return nil, fmt.Errorf("project_id and pipeline_id are required")
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	jobs, _, err := client.Jobs.ListPipelineJobs(p.ProjectID, p.PipelineID,
		&gogitlab.ListJobsOptions{ListOptions: gogitlab.ListOptions{PerPage: 100}})
	if err != nil {
		return nil, fmt.Errorf("list pipeline jobs: %w", err)
	}

	var jobsData []map[string]any
	for _, j := range jobs {
		jd := map[string]any{
			"id":             j.ID,
			"name":           j.Name,
			"stage":          j.Stage,
			"status":         j.Status,
			"created_at":     timeStr(j.CreatedAt),
			"started_at":     timeStr(j.StartedAt),
			"finished_at":    timeStr(j.FinishedAt),
			"duration":       j.Duration,
			"web_url":        j.WebURL,
			"failure_reason": j.FailureReason,
			"allow_failure":  j.AllowFailure,
		}

		if p.IncludeLogs && j.Status == "failed" {
			trace, _, traceErr := client.Jobs.GetTraceFile(p.ProjectID, j.ID)
			if traceErr != nil {
				jd["logs_error"] = traceErr.Error()
			} else if trace != nil {
				data, readErr := io.ReadAll(io.LimitReader(trace, int64(maxLogBytes)+1))
				if readErr != nil {
					jd["logs_error"] = readErr.Error()
				} else if len(data) > 0 {
					logStr := string(data)
					if len(logStr) > maxLogBytes {
						logStr = logStr[:maxLogBytes] + "\n... [TRUNCATED]"
						jd["logs_truncated"] = true
					}
					jd["logs"] = logStr
				}
			}
		}

		jobsData = append(jobsData, jd)
	}

	return map[string]any{
		"pipeline_id": p.PipelineID,
		"jobs":        jobsData,
		"total_jobs":  len(jobsData),
	}, nil
}

// --- gitlab_get_project_pipelines ---

type getProjectPipelinesParams struct {
	instanceParam
	ProjectID  string `json:"project_id"`
	Ref        string `json:"ref"`
	Status     string `json:"status"`
	MaxResults int    `json:"max_results"`
}

func toolGetProjectPipelines(params, _ json.RawMessage) (any, error) {
	var p getProjectPipelinesParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	if p.MaxResults <= 0 {
		p.MaxResults = 10
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	perPage := int64(p.MaxResults)
	if perPage > 100 {
		perPage = 100
	}

	opts := &gogitlab.ListProjectPipelinesOptions{
		ListOptions: gogitlab.ListOptions{PerPage: perPage},
	}
	if p.Ref != "" {
		opts.Ref = &p.Ref
	}
	if p.Status != "" {
		status := gogitlab.BuildStateValue(p.Status)
		opts.Status = &status
	}

	pipelines, _, err := client.Pipelines.ListProjectPipelines(p.ProjectID, opts)
	if err != nil {
		return nil, fmt.Errorf("list pipelines: %w", err)
	}

	var result []map[string]any
	for _, pl := range pipelines {
		result = append(result, map[string]any{
			"id":         pl.ID,
			"iid":        pl.IID,
			"status":     pl.Status,
			"ref":        pl.Ref,
			"sha":        pl.SHA,
			"web_url":    pl.WebURL,
			"created_at": timeStr(pl.CreatedAt),
			"updated_at": timeStr(pl.UpdatedAt),
			"source":     pl.Source,
		})
	}
	return map[string]any{"pipelines": result, "total": len(result)}, nil
}

// --- gitlab_get_project_issues ---

type getProjectIssuesParams struct {
	instanceParam
	ProjectID     string   `json:"project_id"`
	State         string   `json:"state"`
	Assignee      string   `json:"assignee"`
	Author        string   `json:"author"`
	Labels        []string `json:"labels"`
	Milestone     string   `json:"milestone"`
	Search        string   `json:"search"`
	CreatedAfter  string   `json:"created_after"`
	CreatedBefore string   `json:"created_before"`
	UpdatedAfter  string   `json:"updated_after"`
	UpdatedBefore string   `json:"updated_before"`
	OrderBy       string   `json:"order_by"`
	Sort          string   `json:"sort"`
	MaxResults    int      `json:"max_results"`
}

func toolGetProjectIssues(params, _ json.RawMessage) (any, error) {
	var p getProjectIssuesParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	if p.MaxResults <= 0 {
		p.MaxResults = 20
	}
	if p.OrderBy == "" {
		p.OrderBy = "created_at"
	}
	if p.Sort == "" {
		p.Sort = "desc"
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	perPage := int64(p.MaxResults)
	if perPage > 100 {
		perPage = 100
	}

	opts := &gogitlab.ListProjectIssuesOptions{
		ListOptions: gogitlab.ListOptions{PerPage: perPage},
		OrderBy:     &p.OrderBy,
		Sort:        &p.Sort,
	}
	if p.State != "" {
		opts.State = &p.State
	}
	if p.Assignee != "" {
		opts.AssigneeUsername = &p.Assignee
	}
	if p.Author != "" {
		opts.AuthorUsername = &p.Author
	}
	if len(p.Labels) > 0 {
		labels := gogitlab.LabelOptions(p.Labels)
		opts.Labels = &labels
	}
	if p.Milestone != "" {
		opts.Milestone = &p.Milestone
	}
	if p.Search != "" {
		opts.Search = &p.Search
	}
	if p.CreatedAfter != "" {
		t, err := parseTime(p.CreatedAfter)
		if err != nil {
			return nil, fmt.Errorf("invalid created_after: %w", err)
		}
		opts.CreatedAfter = t
	}
	if p.CreatedBefore != "" {
		t, err := parseTime(p.CreatedBefore)
		if err != nil {
			return nil, fmt.Errorf("invalid created_before: %w", err)
		}
		opts.CreatedBefore = t
	}
	if p.UpdatedAfter != "" {
		t, err := parseTime(p.UpdatedAfter)
		if err != nil {
			return nil, fmt.Errorf("invalid updated_after: %w", err)
		}
		opts.UpdatedAfter = t
	}
	if p.UpdatedBefore != "" {
		t, err := parseTime(p.UpdatedBefore)
		if err != nil {
			return nil, fmt.Errorf("invalid updated_before: %w", err)
		}
		opts.UpdatedBefore = t
	}

	issues, _, err := client.Issues.ListProjectIssues(p.ProjectID, opts)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}

	var result []map[string]any
	for _, i := range issues {
		result = append(result, issueToMap(i))
	}
	return map[string]any{"issues": result, "total": len(result)}, nil
}

// --- gitlab_get_issue_details ---

type getIssueDetailsParams struct {
	instanceParam
	ProjectID    string `json:"project_id"`
	IssueIID     int64  `json:"issue_iid"`
	IncludeNotes bool   `json:"include_notes"`
}

func toolGetIssueDetails(params, _ json.RawMessage) (any, error) {
	p := getIssueDetailsParams{IncludeNotes: true}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.ProjectID == "" || p.IssueIID == 0 {
		return nil, fmt.Errorf("project_id and issue_iid are required")
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	issue, _, err := client.Issues.GetIssue(p.ProjectID, p.IssueIID)
	if err != nil {
		return nil, fmt.Errorf("get issue: %w", err)
	}

	result := issueToMap(issue)

	if p.IncludeNotes {
		notes, _, notesErr := client.Notes.ListIssueNotes(p.ProjectID, p.IssueIID,
			&gogitlab.ListIssueNotesOptions{ListOptions: gogitlab.ListOptions{PerPage: 100}})
		if notesErr != nil {
			result["notes_error"] = notesErr.Error()
		} else {
			var notesList []map[string]any
			for _, n := range notes {
				notesList = append(notesList, map[string]any{
					"id":         n.ID,
					"author":     n.Author.Username,
					"created_at": timeStr(n.CreatedAt),
					"body":       n.Body,
					"system":     n.System,
				})
			}
			result["notes"] = notesList
		}
	}

	return result, nil
}

// --- gitlab_my_issues ---

type myIssuesParams struct {
	instanceParam
	Labels     []string `json:"labels"`
	Sort       string   `json:"sort"`
	OrderBy    string   `json:"order_by"`
	MaxResults int      `json:"max_results"`
}

func toolMyIssues(params, _ json.RawMessage) (any, error) {
	var p myIssuesParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.MaxResults <= 0 {
		p.MaxResults = 30
	}
	if p.OrderBy == "" {
		p.OrderBy = "updated_at"
	}
	if p.Sort == "" {
		p.Sort = "desc"
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	perPage := int64(p.MaxResults)
	if perPage > 100 {
		perPage = 100
	}

	scope := "assigned_to_me"
	state := "opened"
	opts := &gogitlab.ListIssuesOptions{
		ListOptions: gogitlab.ListOptions{PerPage: perPage},
		Scope:       &scope,
		State:       &state,
		OrderBy:     &p.OrderBy,
		Sort:        &p.Sort,
	}
	if len(p.Labels) > 0 {
		labels := gogitlab.LabelOptions(p.Labels)
		opts.Labels = &labels
	}

	issues, _, err := client.Issues.ListIssues(opts)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}

	var result []map[string]any
	for _, i := range issues {
		result = append(result, issueToMap(i))
	}
	return map[string]any{"issues": result, "total": len(result)}, nil
}

// --- gitlab_get_todos ---

type getTodosParams struct {
	instanceParam
	Action     string `json:"action"`
	AuthorID   int64  `json:"author_id"`
	ProjectID  int64  `json:"project_id"`
	GroupID    int64  `json:"group_id"`
	State      string `json:"state"`
	TargetType string `json:"target_type"`
	MaxResults int    `json:"max_results"`
	Page       int64  `json:"page"`
}

func toolGetTodos(params, _ json.RawMessage) (any, error) {
	var p getTodosParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.State == "" {
		p.State = "pending"
	}
	if p.MaxResults <= 0 {
		p.MaxResults = 20
	}
	if p.Page < 1 {
		p.Page = 1
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	perPage := int64(p.MaxResults)
	if perPage > 100 {
		perPage = 100
	}

	opts := &gogitlab.ListTodosOptions{
		ListOptions: gogitlab.ListOptions{PerPage: perPage, Page: p.Page},
		State:       &p.State,
	}
	if p.Action != "" {
		action := gogitlab.TodoAction(p.Action)
		opts.Action = &action
	}
	if p.AuthorID != 0 {
		opts.AuthorID = &p.AuthorID
	}
	if p.ProjectID != 0 {
		opts.ProjectID = &p.ProjectID
	}
	if p.GroupID != 0 {
		opts.GroupID = &p.GroupID
	}
	if p.TargetType != "" {
		opts.Type = &p.TargetType
	}

	todos, resp, err := client.Todos.ListTodos(opts)
	if err != nil {
		return nil, fmt.Errorf("list todos: %w", err)
	}

	var items []map[string]any
	for _, t := range todos {
		body := t.Body
		if runes := []rune(body); len(runes) > 200 {
			body = string(runes[:200]) + "..."
		}

		td := map[string]any{
			"action_name": t.ActionName,
			"target_type": t.TargetType,
			"body":        body,
			"created_at":  timeStr(t.CreatedAt),
		}

		// Keep target_url for types where it's not reconstructable
		// (Commit, Epic, Alert, Design). Omit for Issue/MergeRequest.
		if t.TargetType != "Issue" && t.TargetType != "MergeRequest" {
			td["target_url"] = t.TargetURL
		}

		if t.Project != nil {
			td["project_path"] = t.Project.PathWithNamespace
		}
		if t.Author != nil {
			td["author"] = t.Author.Username
		}
		if t.Target != nil {
			td["target_iid"] = t.Target.IID
			td["target_title"] = t.Target.Title
			td["target_state"] = t.Target.State
		}
		items = append(items, td)
	}

	out := map[string]any{
		"todos": items,
		"count": len(items),
		"state": p.State,
	}
	if resp != nil {
		out["page"] = resp.CurrentPage
		out["total_pages"] = resp.TotalPages
		out["total_items"] = resp.TotalItems
		out["has_next_page"] = resp.NextPage > 0
		if resp.NextPage > 0 {
			out["next_page"] = resp.NextPage
		}
	}
	return out, nil
}

// --- gitlab_list_merge_requests ---

type listMergeRequestsParams struct {
	instanceParam
	State            string   `json:"state"`
	Scope            string   `json:"scope"`
	AuthorUsername   string   `json:"author_username"`
	AssigneeUsername string   `json:"assignee_username"`
	ReviewerUsername string   `json:"reviewer_username"`
	ProjectID        string   `json:"project_id"`
	Labels           []string `json:"labels"`
	Milestone        string   `json:"milestone"`
	Search           string   `json:"search"`
	SourceBranch     string   `json:"source_branch"`
	TargetBranch     string   `json:"target_branch"`
	OrderBy          string   `json:"order_by"`
	Sort             string   `json:"sort"`
	MaxResults       int      `json:"max_results"`
}

func toolListMergeRequests(params, _ json.RawMessage) (any, error) {
	var p listMergeRequestsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.State == "" {
		p.State = "opened"
	}
	if p.MaxResults <= 0 {
		p.MaxResults = 20
	}
	if p.OrderBy == "" {
		p.OrderBy = "created_at"
	}
	if p.Sort == "" {
		p.Sort = "desc"
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	perPage := int64(p.MaxResults)
	if perPage > 100 {
		perPage = 100
	}

	if p.ProjectID != "" {
		return listProjectMRs(client, p, perPage)
	}
	return listGlobalMRs(client, p, perPage)
}

func listProjectMRs(client *gogitlab.Client, p listMergeRequestsParams, perPage int64) (any, error) {
	opts := &gogitlab.ListProjectMergeRequestsOptions{
		ListOptions: gogitlab.ListOptions{PerPage: perPage},
		State:       &p.State,
		OrderBy:     &p.OrderBy,
		Sort:        &p.Sort,
	}
	if p.Scope != "" {
		opts.Scope = &p.Scope
	}
	if p.AuthorUsername != "" {
		opts.AuthorUsername = &p.AuthorUsername
	}
	if p.AssigneeUsername != "" {
		uid, err := resolveUserID(client, p.AssigneeUsername)
		if err != nil {
			return nil, err
		}
		opts.AssigneeID = gogitlab.AssigneeID(uid)
	}
	if p.ReviewerUsername != "" {
		opts.ReviewerUsername = &p.ReviewerUsername
	}
	if len(p.Labels) > 0 {
		labels := gogitlab.LabelOptions(p.Labels)
		opts.Labels = &labels
	}
	if p.Milestone != "" {
		opts.Milestone = &p.Milestone
	}
	if p.Search != "" {
		opts.Search = &p.Search
	}
	if p.SourceBranch != "" {
		opts.SourceBranch = &p.SourceBranch
	}
	if p.TargetBranch != "" {
		opts.TargetBranch = &p.TargetBranch
	}

	mrs, _, err := client.MergeRequests.ListProjectMergeRequests(p.ProjectID, opts)
	if err != nil {
		return nil, fmt.Errorf("list project MRs: %w", err)
	}
	return mrListResult(mrs), nil
}

func listGlobalMRs(client *gogitlab.Client, p listMergeRequestsParams, perPage int64) (any, error) {
	opts := &gogitlab.ListMergeRequestsOptions{
		ListOptions: gogitlab.ListOptions{PerPage: perPage},
		State:       &p.State,
		OrderBy:     &p.OrderBy,
		Sort:        &p.Sort,
	}
	if p.Scope != "" {
		opts.Scope = &p.Scope
	}
	if p.AuthorUsername != "" {
		opts.AuthorUsername = &p.AuthorUsername
	}
	if p.AssigneeUsername != "" {
		uid, err := resolveUserID(client, p.AssigneeUsername)
		if err != nil {
			return nil, err
		}
		opts.AssigneeID = gogitlab.AssigneeID(uid)
	}
	if p.ReviewerUsername != "" {
		opts.ReviewerUsername = &p.ReviewerUsername
	}
	if len(p.Labels) > 0 {
		labels := gogitlab.LabelOptions(p.Labels)
		opts.Labels = &labels
	}
	if p.Milestone != "" {
		opts.Milestone = &p.Milestone
	}
	if p.Search != "" {
		opts.Search = &p.Search
	}
	if p.SourceBranch != "" {
		opts.SourceBranch = &p.SourceBranch
	}
	if p.TargetBranch != "" {
		opts.TargetBranch = &p.TargetBranch
	}

	mrs, _, err := client.MergeRequests.ListMergeRequests(opts)
	if err != nil {
		return nil, fmt.Errorf("list MRs: %w", err)
	}
	return mrListResult(mrs), nil
}

func mrListResult(mrs []*gogitlab.BasicMergeRequest) map[string]any {
	var result []map[string]any
	for _, mr := range mrs {
		author := ""
		if mr.Author != nil {
			author = mr.Author.Username
		}
		result = append(result, map[string]any{
			"iid":           mr.IID,
			"title":         mr.Title,
			"state":         mr.State,
			"web_url":       mr.WebURL,
			"source_branch": mr.SourceBranch,
			"target_branch": mr.TargetBranch,
			"author":        author,
			"created_at":    timeStr(mr.CreatedAt),
			"updated_at":    timeStr(mr.UpdatedAt),
			"draft":         mr.Draft,
			"labels":        mr.Labels,
		})
	}
	return map[string]any{"merge_requests": result, "total": len(result)}
}

// --- helpers ---

func issueToMap(i *gogitlab.Issue) map[string]any {
	var assignees []string
	for _, a := range i.Assignees {
		assignees = append(assignees, a.Username)
	}

	m := map[string]any{
		"iid":         i.IID,
		"title":       i.Title,
		"description": i.Description,
		"state":       i.State,
		"created_at":  timeStr(i.CreatedAt),
		"updated_at":  timeStr(i.UpdatedAt),
		"web_url":     i.WebURL,
		"assignees":   assignees,
		"labels":      i.Labels,
	}
	if i.Author != nil {
		m["author"] = i.Author.Username
	}
	if i.Milestone != nil {
		m["milestone"] = i.Milestone.Title
	}
	if i.DueDate != nil {
		m["due_date"] = i.DueDate.String()
	}
	return m
}

func parseTime(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t, nil
		}
	}
	return nil, fmt.Errorf("invalid date format %q (expected RFC3339 or YYYY-MM-DD)", s)
}

var userIDCache sync.Map

func resolveUserID(client *gogitlab.Client, username string) (int64, error) {
	cacheKey := client.BaseURL().Host + "/" + username
	if id, ok := userIDCache.Load(cacheKey); ok {
		return id.(int64), nil
	}
	users, _, err := client.Users.ListUsers(&gogitlab.ListUsersOptions{
		Username: &username,
	})
	if err != nil {
		return 0, fmt.Errorf("lookup user %q: %w", username, err)
	}
	if len(users) == 0 {
		return 0, fmt.Errorf("user %q not found", username)
	}
	userIDCache.Store(cacheKey, users[0].ID)
	return users[0].ID, nil
}

func timeStr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

// --- gitlab_get_file_contents ---

const maxFileContentBytes = 50000

type getFileContentsParams struct {
	instanceParam
	ProjectID string `json:"project_id"`
	Path      string `json:"path"`
	Ref       string `json:"ref"`
}

func toolGetFileContents(params, _ json.RawMessage) (any, error) {
	var p getFileContentsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	if err := validatePath(p.Path); err != nil {
		return nil, err
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	opts := &gogitlab.GetFileOptions{}
	if p.Ref != "" {
		opts.Ref = &p.Ref
	}

	file, _, err := client.RepositoryFiles.GetFile(p.ProjectID, p.Path, opts)
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}

	result := map[string]any{
		"file_name":      file.FileName,
		"file_path":      file.FilePath,
		"size":           file.Size,
		"encoding":       file.Encoding,
		"ref":            file.Ref,
		"blob_id":        file.BlobID,
		"last_commit_id": file.LastCommitID,
	}

	if file.Size > maxFileContentBytes {
		result["content_available"] = false
		return result, nil
	}

	if file.Encoding == "base64" && file.Content != "" {
		decoded, decodeErr := io.ReadAll(io.LimitReader(
			base64.NewDecoder(base64.StdEncoding, strings.NewReader(file.Content)),
			maxFileContentBytes+1,
		))
		if decodeErr != nil {
			result["content_available"] = false
			return result, nil //nolint:nilerr // graceful degradation for undecodable content
		}
		for _, b := range decoded[:min(len(decoded), 1024)] {
			if b == 0 {
				result["content_available"] = false
				result["binary"] = true
				return result, nil
			}
		}
		text := string(decoded)
		if len(decoded) > maxFileContentBytes {
			text = strings.ToValidUTF8(text[:maxFileContentBytes], "") + fmt.Sprintf("\n[TRUNCATED at %dKB]", maxFileContentBytes/1000)
		}
		result["content"] = text
	} else {
		result["content"] = file.Content
	}

	return result, nil
}

// --- gitlab_search_projects ---

type searchProjectsParams struct {
	instanceParam
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

func toolSearchProjects(params, _ json.RawMessage) (any, error) {
	var p searchProjectsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.Query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if p.MaxResults <= 0 {
		p.MaxResults = 30
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	perPage := int64(p.MaxResults)
	if perPage > 100 {
		perPage = 100
	}

	projects, _, err := client.Projects.ListProjects(&gogitlab.ListProjectsOptions{
		Search:      &p.Query,
		ListOptions: gogitlab.ListOptions{PerPage: perPage},
	})
	if err != nil {
		return nil, fmt.Errorf("search projects: %w", err)
	}

	var items []map[string]any
	for _, proj := range projects {
		m := map[string]any{
			"id":                  proj.ID,
			"path_with_namespace": proj.PathWithNamespace,
			"description":         proj.Description,
			"web_url":             proj.WebURL,
			"default_branch":      proj.DefaultBranch,
			"archived":            proj.Archived,
			"star_count":          proj.StarCount,
			"forks_count":         proj.ForksCount,
		}
		if proj.LastActivityAt != nil {
			m["last_activity_at"] = proj.LastActivityAt.Format(time.RFC3339)
		}
		items = append(items, m)
	}

	return map[string]any{
		"count": len(items),
		"items": items,
	}, nil
}

// --- gitlab_search_code ---

type searchCodeParams struct {
	instanceParam
	ProjectID  string `json:"project_id"`
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

func toolSearchCode(params, _ json.RawMessage) (any, error) {
	var p searchCodeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	if p.Query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if p.MaxResults <= 0 {
		p.MaxResults = 30
	}

	client, err := resolveInstance(p.Instance)
	if err != nil {
		return nil, err
	}

	perPage := int64(p.MaxResults)
	if perPage > 100 {
		perPage = 100
	}

	blobs, _, err := client.Search.BlobsByProject(p.ProjectID, p.Query, &gogitlab.SearchOptions{
		ListOptions: gogitlab.ListOptions{PerPage: perPage},
	})
	if err != nil {
		return nil, fmt.Errorf("search code: %w", err)
	}

	var items []map[string]any
	for _, blob := range blobs {
		items = append(items, map[string]any{
			"basename":   blob.Basename,
			"data":       blob.Data,
			"path":       blob.Path,
			"filename":   blob.Filename,
			"ref":        blob.Ref,
			"startline":  blob.Startline,
			"project_id": blob.ProjectID,
		})
	}

	return map[string]any{
		"count": len(items),
		"items": items,
	}, nil
}
