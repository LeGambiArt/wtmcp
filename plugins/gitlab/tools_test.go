package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gogitlab "gitlab.com/gitlab-org/api/client-go"
)

func TestTimeStrNil(t *testing.T) {
	got := timeStr(nil)
	if got != "" {
		t.Errorf("timeStr(nil) = %q, want empty", got)
	}
}

func TestTimeStrValid(t *testing.T) {
	ts := time.Date(2026, 3, 10, 14, 30, 0, 0, time.UTC)
	got := timeStr(&ts)
	want := "2026-03-10T14:30:00Z"
	if got != want {
		t.Errorf("timeStr() = %q, want %q", got, want)
	}
}

func TestIssueToMapFull(t *testing.T) {
	created := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC)
	due := gogitlab.ISOTime(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	issue := &gogitlab.Issue{
		IID:         42,
		Title:       "Fix login bug",
		Description: "The login page crashes on mobile",
		State:       "opened",
		CreatedAt:   &created,
		UpdatedAt:   &updated,
		WebURL:      "https://gitlab.example.com/proj/-/issues/42",
		Labels:      gogitlab.Labels{"bug", "critical"},
		Author:      &gogitlab.IssueAuthor{Username: "alice"},
		Assignees: []*gogitlab.IssueAssignee{
			{Username: "bob"},
			{Username: "carol"},
		},
		Milestone: &gogitlab.Milestone{Title: "v2.0"},
		DueDate:   &due,
	}

	m := issueToMap(issue)

	if m["iid"] != int64(42) {
		t.Errorf("iid = %v", m["iid"])
	}
	if m["title"] != "Fix login bug" {
		t.Errorf("title = %v", m["title"])
	}
	if m["state"] != "opened" {
		t.Errorf("state = %v", m["state"])
	}
	if m["author"] != "alice" {
		t.Errorf("author = %v", m["author"])
	}
	assignees, ok := m["assignees"].([]string)
	if !ok {
		t.Fatalf("assignees type = %T", m["assignees"])
	}
	if len(assignees) != 2 || assignees[0] != "bob" {
		t.Errorf("assignees = %v", assignees)
	}
	if m["milestone"] != "v2.0" {
		t.Errorf("milestone = %v", m["milestone"])
	}
	if _, ok := m["due_date"]; !ok {
		t.Error("expected due_date key")
	}
	labels, ok := m["labels"].(gogitlab.Labels)
	if !ok {
		t.Fatalf("labels type = %T", m["labels"])
	}
	if len(labels) != 2 {
		t.Errorf("labels = %v", labels)
	}
}

func TestIssueToMapNilFields(t *testing.T) {
	issue := &gogitlab.Issue{
		IID:   1,
		Title: "Minimal",
		State: "closed",
	}

	m := issueToMap(issue)

	if m["iid"] != int64(1) {
		t.Errorf("iid = %v", m["iid"])
	}
	// Author, Milestone, DueDate should not panic
	if _, ok := m["author"]; ok {
		t.Error("author should not be set for nil Author")
	}
	if _, ok := m["milestone"]; ok {
		t.Error("milestone should not be set for nil Milestone")
	}
	if _, ok := m["due_date"]; ok {
		t.Error("due_date should not be set for nil DueDate")
	}
}

func TestIssueToMapNoAssignees(t *testing.T) {
	issue := &gogitlab.Issue{
		IID:   2,
		Title: "Unassigned",
		State: "opened",
	}

	m := issueToMap(issue)

	assignees, ok := m["assignees"].([]string)
	if ok && len(assignees) > 0 {
		t.Errorf("expected nil/empty assignees, got %v", assignees)
	}
}

func TestMRListResultMultiple(t *testing.T) {
	created := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC)
	mrs := []*gogitlab.BasicMergeRequest{
		{
			IID:          10,
			Title:        "Add feature X",
			State:        "merged",
			WebURL:       "https://gitlab.example.com/proj/-/merge_requests/10",
			SourceBranch: "feature-x",
			TargetBranch: "main",
			Author:       &gogitlab.BasicUser{Username: "alice"},
			CreatedAt:    &created,
			UpdatedAt:    &updated,
			Draft:        false,
			Labels:       gogitlab.Labels{"enhancement"},
		},
		{
			IID:          11,
			Title:        "Fix bug Y",
			State:        "opened",
			WebURL:       "https://gitlab.example.com/proj/-/merge_requests/11",
			SourceBranch: "fix-y",
			TargetBranch: "main",
			Author:       &gogitlab.BasicUser{Username: "bob"},
			CreatedAt:    &created,
			UpdatedAt:    &updated,
			Draft:        true,
			Labels:       gogitlab.Labels{},
		},
	}

	result := mrListResult(mrs)

	if result["total"] != 2 {
		t.Errorf("total = %v, want 2", result["total"])
	}
	items, ok := result["merge_requests"].([]map[string]any)
	if !ok {
		t.Fatalf("merge_requests type = %T", result["merge_requests"])
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0]["iid"] != int64(10) {
		t.Errorf("first MR iid = %v", items[0]["iid"])
	}
	if items[0]["author"] != "alice" {
		t.Errorf("first MR author = %v", items[0]["author"])
	}
	if items[0]["draft"] != false {
		t.Errorf("first MR draft = %v", items[0]["draft"])
	}
	if items[1]["draft"] != true {
		t.Errorf("second MR draft = %v", items[1]["draft"])
	}
}

func TestMRListResultEmpty(t *testing.T) {
	result := mrListResult(nil)

	if result["total"] != 0 {
		t.Errorf("total = %v, want 0", result["total"])
	}
}

func TestMRListResultNilAuthor(t *testing.T) {
	mrs := []*gogitlab.BasicMergeRequest{
		{
			IID:   1,
			Title: "No author",
			State: "opened",
		},
	}

	result := mrListResult(mrs)
	items := result["merge_requests"].([]map[string]any)
	if items[0]["author"] != "" {
		t.Errorf("author should be empty for nil Author, got %v", items[0]["author"])
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

// setupGitLabTest creates a test HTTP server and injects it as the
// default GitLab instance. Uses empty token to match production
// behavior where the core proxy injects PRIVATE-TOKEN.
func setupGitLabTest(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(ts.Close)

	client, err := gogitlab.NewClient("", gogitlab.WithBaseURL(ts.URL+"/api/v4"))
	if err != nil {
		t.Fatal(err)
	}
	instances = map[string]*instance{
		"default": {Name: "default", URL: ts.URL, Client: client},
	}
	defaultInstance = "default"
}

func jsonResponse(w http.ResponseWriter, data string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, data)
}

func TestToolGetCommits(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/repository/commits") {
			jsonResponse(w, `[{"id":"abc123","short_id":"abc","title":"Initial commit","message":"Initial commit\n\nSigned-off-by: Alice <alice@example.com>","author_name":"Alice","author_email":"alice@example.com","web_url":"https://gitlab.example.com/commit/abc123"}]`)
			return
		}
		http.NotFound(w, r)
	})

	result, err := toolGetCommits(mustJSON(t, map[string]any{
		"project_id": "team/myproject",
	}), nil)
	if err != nil {
		t.Fatalf("toolGetCommits: %v", err)
	}

	commits, ok := result.([]map[string]any)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if len(commits) != 1 {
		t.Fatalf("got %d commits, want 1", len(commits))
	}
	if commits[0]["id"] != "abc123" {
		t.Errorf("id = %v", commits[0]["id"])
	}
	if commits[0]["author_name"] != "Alice" {
		t.Errorf("author_name = %v", commits[0]["author_name"])
	}
	if commits[0]["message"] != "Initial commit\n\nSigned-off-by: Alice <alice@example.com>" {
		t.Errorf("message = %v", commits[0]["message"])
	}
}

func TestToolGetCommitsMissingProject(t *testing.T) {
	_, err := toolGetCommits(mustJSON(t, map[string]any{}), nil)
	if err == nil {
		t.Fatal("expected error for missing project_id")
	}
}

func TestToolGetCommitDiff(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/diff") {
			jsonResponse(w, `[{"old_path":"main.go","new_path":"main.go","diff":"@@ -1 +1 @@\n-old\n+new","new_file":false,"renamed_file":false,"deleted_file":false}]`)
			return
		}
		http.NotFound(w, r)
	})

	result, err := toolGetCommitDiff(mustJSON(t, map[string]any{
		"project_id": "team/proj",
		"commit_sha": "abc123",
	}), nil)
	if err != nil {
		t.Fatalf("toolGetCommitDiff: %v", err)
	}

	m := result.(map[string]any)
	if m["commit_id"] != "abc123" {
		t.Errorf("commit_id = %v", m["commit_id"])
	}
	diffs := m["diffs"].([]map[string]any)
	if len(diffs) != 1 {
		t.Fatalf("got %d diffs, want 1", len(diffs))
	}
	if diffs[0]["new_path"] != "main.go" {
		t.Errorf("new_path = %v", diffs[0]["new_path"])
	}
}

func TestToolGetCommitDiffTruncation(t *testing.T) {
	longDiff := strings.Repeat("x", maxDiffBytes+100)
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/diff") {
			data, _ := json.Marshal([]map[string]any{
				{"old_path": "big.txt", "new_path": "big.txt", "diff": longDiff, "new_file": false, "renamed_file": false, "deleted_file": false},
			})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(data)
			return
		}
		http.NotFound(w, r)
	})

	result, err := toolGetCommitDiff(mustJSON(t, map[string]any{
		"project_id": "team/proj",
		"commit_sha": "abc",
	}), nil)
	if err != nil {
		t.Fatalf("toolGetCommitDiff: %v", err)
	}

	m := result.(map[string]any)
	diffs := m["diffs"].([]map[string]any)
	diff := diffs[0]["diff"].(string)
	if !strings.HasSuffix(diff, "[TRUNCATED]") {
		t.Error("expected diff to be truncated")
	}
	if diffs[0]["truncated"] != true {
		t.Error("expected truncated=true")
	}
}

func TestToolGetCommitDiffFilesOnly(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/diff") {
			jsonResponse(w, `[{"old_path":"a.go","new_path":"a.go","diff":"some diff","new_file":false,"renamed_file":false,"deleted_file":false}]`)
			return
		}
		http.NotFound(w, r)
	})

	result, err := toolGetCommitDiff(mustJSON(t, map[string]any{
		"project_id": "team/proj",
		"commit_sha": "abc",
		"files_only": true,
	}), nil)
	if err != nil {
		t.Fatalf("toolGetCommitDiff: %v", err)
	}

	m := result.(map[string]any)
	diffs := m["diffs"].([]map[string]any)
	if _, hasDiff := diffs[0]["diff"]; hasDiff {
		t.Error("files_only=true should omit diff content")
	}
}

func TestToolGetProjectPipelines(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/pipelines") {
			jsonResponse(w, `[{"id":100,"iid":1,"status":"success","ref":"main","sha":"abc","web_url":"https://gitlab.example.com/pipelines/100","source":"push"}]`)
			return
		}
		http.NotFound(w, r)
	})

	result, err := toolGetProjectPipelines(mustJSON(t, map[string]any{
		"project_id": "team/proj",
	}), nil)
	if err != nil {
		t.Fatalf("toolGetProjectPipelines: %v", err)
	}

	m := result.(map[string]any)
	if m["total"] != 1 {
		t.Errorf("total = %v", m["total"])
	}
}

func TestToolGetProjectIssues(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/issues") {
			jsonResponse(w, `[{"id":1,"iid":1,"title":"Bug","state":"opened","web_url":"https://gitlab.example.com/issues/1","labels":[],"author":{"username":"alice"},"assignees":[],"task_completion_status":{"count":0,"completed_count":0}}]`)
			return
		}
		http.NotFound(w, r)
	})

	result, err := toolGetProjectIssues(mustJSON(t, map[string]any{
		"project_id": "team/proj",
	}), nil)
	if err != nil {
		t.Fatalf("toolGetProjectIssues: %v", err)
	}

	m := result.(map[string]any)
	if m["total"] != 1 {
		t.Errorf("total = %v", m["total"])
	}
}

func TestToolListMergeRequestsGlobal(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/merge_requests" {
			jsonResponse(w, `[{"iid":5,"title":"Global MR","state":"opened","source_branch":"feat","target_branch":"main"}]`)
			return
		}
		http.NotFound(w, r)
	})

	result, err := toolListMergeRequests(mustJSON(t, map[string]any{}), nil)
	if err != nil {
		t.Fatalf("toolListMergeRequests: %v", err)
	}

	m := result.(map[string]any)
	if m["total"] != 1 {
		t.Errorf("total = %v", m["total"])
	}
}

func TestToolListMergeRequestsProject(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/merge_requests") {
			jsonResponse(w, `[{"iid":10,"title":"Project MR","state":"merged","source_branch":"fix","target_branch":"main"}]`)
			return
		}
		http.NotFound(w, r)
	})

	result, err := toolListMergeRequests(mustJSON(t, map[string]any{
		"project_id": "team/proj",
	}), nil)
	if err != nil {
		t.Fatalf("toolListMergeRequests: %v", err)
	}

	m := result.(map[string]any)
	if m["total"] != 1 {
		t.Errorf("total = %v", m["total"])
	}
}

func TestToolMyIssues(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/issues" {
			if r.URL.Query().Get("scope") != "assigned_to_me" {
				t.Errorf("scope = %q, want assigned_to_me", r.URL.Query().Get("scope"))
			}
			if r.URL.Query().Get("state") != "opened" {
				t.Errorf("state = %q, want opened", r.URL.Query().Get("state"))
			}
			jsonResponse(w, `[{"id":10,"iid":10,"title":"Fix bug","state":"opened","web_url":"https://gitlab.example.com/issues/10","author":{"username":"alice"},"assignees":[{"username":"testuser"}],"labels":["bug"],"created_at":"2026-03-20T10:00:00Z","updated_at":"2026-03-25T10:00:00Z"}]`)
			return
		}
		http.NotFound(w, r)
	})

	result, err := toolMyIssues(mustJSON(t, map[string]any{}), nil)
	if err != nil {
		t.Fatalf("toolMyIssues: %v", err)
	}

	m := result.(map[string]any)
	if m["total"] != 1 {
		t.Errorf("total = %v, want 1", m["total"])
	}
	issues := m["issues"].([]map[string]any)
	if issues[0]["title"] != "Fix bug" {
		t.Errorf("title = %v", issues[0]["title"])
	}
}

func TestToolGetMergeRequest(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/merge_requests/42/notes") {
			jsonResponse(w, `[{"id":1,"author":{"username":"alice"},"body":"LGTM","system":false,"resolvable":false,"resolved":false}]`)
			return
		}
		if strings.Contains(r.URL.Path, "/merge_requests/42") {
			jsonResponse(w, `{
				"iid":42,"title":"Add feature","description":"Adds feature X",
				"state":"opened","sha":"def456",
				"author":{"username":"alice"},
				"source_branch":"feat","target_branch":"main",
				"web_url":"https://gitlab.example.com/proj/-/merge_requests/42",
				"detailed_merge_status":"mergeable",
				"draft":false,
				"diff_refs":{"base_sha":"aaa111","head_sha":"bbb222","start_sha":"ccc333"}
			}`)
			return
		}
		http.NotFound(w, r)
	})

	result, err := toolGetMergeRequest(mustJSON(t, map[string]any{
		"project_id": "team/proj",
		"mr_iid":     42,
	}), nil)
	if err != nil {
		t.Fatalf("toolGetMergeRequest: %v", err)
	}

	m := result.(map[string]any)
	if m["iid"] != int64(42) {
		t.Errorf("iid = %v", m["iid"])
	}
	if m["title"] != "Add feature" {
		t.Errorf("title = %v", m["title"])
	}
	if m["sha"] != "def456" {
		t.Errorf("sha = %v, want def456", m["sha"])
	}

	refs, ok := m["diff_refs"].(map[string]string)
	if !ok {
		t.Fatalf("diff_refs type = %T, want map[string]string", m["diff_refs"])
	}
	if refs["base_sha"] != "aaa111" {
		t.Errorf("base_sha = %v", refs["base_sha"])
	}
	if refs["head_sha"] != "bbb222" {
		t.Errorf("head_sha = %v", refs["head_sha"])
	}
	if refs["start_sha"] != "ccc333" {
		t.Errorf("start_sha = %v", refs["start_sha"])
	}

	comments, ok := m["comments"].([]map[string]any)
	if !ok {
		t.Fatalf("comments type = %T", m["comments"])
	}
	if len(comments) != 1 {
		t.Fatalf("got %d comments, want 1", len(comments))
	}
	if comments[0]["body"] != "LGTM" {
		t.Errorf("comment body = %v", comments[0]["body"])
	}
}
