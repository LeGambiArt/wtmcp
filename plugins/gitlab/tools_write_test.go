package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// --- isDryRun ---

func TestIsDryRunNil(t *testing.T) {
	if !isDryRun(nil) {
		t.Error("isDryRun(nil) should be true")
	}
}

func TestIsDryRunTrue(t *testing.T) {
	v := true
	if !isDryRun(&v) {
		t.Error("isDryRun(&true) should be true")
	}
}

func TestIsDryRunFalse(t *testing.T) {
	v := false
	if isDryRun(&v) {
		t.Error("isDryRun(&false) should be false")
	}
}

// --- gitlab_create_merge_request ---

func TestCreateMergeRequestDryRun(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not make any HTTP calls in dry run")
		http.NotFound(w, r)
	})

	result, err := toolCreateMergeRequest(mustJSON(t, map[string]any{
		"project_id":    "team/proj",
		"source_branch": "feature-x",
		"target_branch": "main",
		"title":         "Add feature X",
	}), nil)
	if err != nil {
		t.Fatalf("toolCreateMergeRequest: %v", err)
	}

	m := result.(map[string]any)
	if m["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", m["dry_run"])
	}
	if m["action"] != "gitlab_create_merge_request" {
		t.Errorf("action = %v", m["action"])
	}
	if m["title"] != "Add feature X" {
		t.Errorf("title = %v", m["title"])
	}
}

func TestCreateMergeRequestDraftTitle(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not make any HTTP calls in dry run")
		http.NotFound(w, r)
	})

	result, err := toolCreateMergeRequest(mustJSON(t, map[string]any{
		"project_id":    "team/proj",
		"source_branch": "wip",
		"target_branch": "main",
		"title":         "Work in progress",
		"draft":         true,
	}), nil)
	if err != nil {
		t.Fatalf("toolCreateMergeRequest: %v", err)
	}

	m := result.(map[string]any)
	if m["title"] != "Draft: Work in progress" {
		t.Errorf("title = %q, want 'Draft: Work in progress'", m["title"])
	}
}

func TestCreateMergeRequestDraftTitleAlreadyPrefixed(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not make any HTTP calls in dry run")
		http.NotFound(w, r)
	})

	result, err := toolCreateMergeRequest(mustJSON(t, map[string]any{
		"project_id":    "team/proj",
		"source_branch": "wip",
		"target_branch": "main",
		"title":         "Draft: Already prefixed",
		"draft":         true,
	}), nil)
	if err != nil {
		t.Fatalf("toolCreateMergeRequest: %v", err)
	}

	m := result.(map[string]any)
	if m["title"] != "Draft: Already prefixed" {
		t.Errorf("title = %q, want no double prefix", m["title"])
	}
}


func TestCreateMergeRequestDryRunPreview(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not make any HTTP calls in dry run")
		http.NotFound(w, r)
	})

	result, err := toolCreateMergeRequest(mustJSON(t, map[string]any{
		"project_id":    "team/proj",
		"source_branch": "feat",
		"target_branch": "main",
		"title":         "My MR",
		"description":   "A detailed description",
		"labels":        []string{"bug", "help wanted"},
	}), nil)
	if err != nil {
		t.Fatalf("toolCreateMergeRequest: %v", err)
	}

	m := result.(map[string]any)
	if m["description_preview"] != "A detailed description" {
		t.Errorf("description_preview = %v", m["description_preview"])
	}
	labels, ok := m["labels"].([]string)
	if !ok || len(labels) != 2 {
		t.Errorf("labels = %v", m["labels"])
	}
}

func TestCreateMergeRequestExecute(t *testing.T) {
	var gotBody map[string]any
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/merge_requests") {
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			jsonResponse(w, `{
				"iid": 42,
				"title": "Add feature X",
				"web_url": "https://gitlab.com/team/proj/-/merge_requests/42",
				"state": "opened",
				"source_branch": "feature-x",
				"target_branch": "main"
			}`)
			return
		}
		http.NotFound(w, r)
	})

	dryRun := false
	result, err := toolCreateMergeRequest(mustJSON(t, map[string]any{
		"project_id":    "team/proj",
		"source_branch": "feature-x",
		"target_branch": "main",
		"title":         "Add feature X",
		"description":   "Implements the X feature",
		"dry_run":       dryRun,
	}), nil)
	if err != nil {
		t.Fatalf("toolCreateMergeRequest: %v", err)
	}

	m := result.(map[string]any)
	if m["iid"] != int64(42) {
		t.Errorf("iid = %v (%T), want 42", m["iid"], m["iid"])
	}
	if m["state"] != "opened" {
		t.Errorf("state = %v, want opened", m["state"])
	}
	if m["web_url"] == "" {
		t.Error("web_url should not be empty")
	}

	if gotBody == nil {
		t.Fatal("expected POST body to be captured")
	}
	if m["created"] != true {
		t.Errorf("created = %v, want true", m["created"])
	}
	if m["project_id"] != "team/proj" {
		t.Errorf("project_id = %v, want team/proj", m["project_id"])
	}
	if gotBody["description"] != "Implements the X feature" {
		t.Errorf("posted description = %v", gotBody["description"])
	}
	if gotBody["title"] != "Add feature X" {
		t.Errorf("posted title = %v", gotBody["title"])
	}
}

func TestCreateMergeRequestExecuteDraft(t *testing.T) {
	var gotTitle string
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/merge_requests") {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			gotTitle, _ = body["title"].(string)
			jsonResponse(w, `{
				"iid": 7,
				"title": "Draft: My feature",
				"web_url": "https://gitlab.com/team/proj/-/merge_requests/7",
				"state": "opened",
				"source_branch": "feat",
				"target_branch": "main"
			}`)
			return
		}
		http.NotFound(w, r)
	})

	dryRun := false
	_, err := toolCreateMergeRequest(mustJSON(t, map[string]any{
		"project_id":    "team/proj",
		"source_branch": "feat",
		"target_branch": "main",
		"title":         "My feature",
		"draft":         true,
		"dry_run":       dryRun,
	}), nil)
	if err != nil {
		t.Fatalf("toolCreateMergeRequest: %v", err)
	}
	if gotTitle != "Draft: My feature" {
		t.Errorf("sent title = %q, want 'Draft: My feature'", gotTitle)
	}
}

func TestCreateMergeRequestValidation(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		errMsg string
	}{
		{
			name:   "missing project_id",
			params: map[string]any{"source_branch": "feat", "target_branch": "main", "title": "t"},
			errMsg: "project_id is required",
		},
		{
			name:   "missing source_branch",
			params: map[string]any{"project_id": "p", "target_branch": "main", "title": "t"},
			errMsg: "source_branch is required",
		},
		{
			name:   "missing target_branch",
			params: map[string]any{"project_id": "p", "source_branch": "feat", "title": "t"},
			errMsg: "target_branch is required",
		},
		{
			name:   "missing title",
			params: map[string]any{"project_id": "p", "source_branch": "feat", "target_branch": "main"},
			errMsg: "title is required",
		},
		{
			name:   "whitespace title",
			params: map[string]any{"project_id": "p", "source_branch": "feat", "target_branch": "main", "title": "   "},
			errMsg: "title is required",
		},
		{
			name:   "whitespace source_branch",
			params: map[string]any{"project_id": "p", "source_branch": "   ", "target_branch": "main", "title": "t"},
			errMsg: "source_branch is required",
		},
		{
			name:   "whitespace target_branch",
			params: map[string]any{"project_id": "p", "source_branch": "feat", "target_branch": "   ", "title": "t"},
			errMsg: "target_branch is required",
		},
		{
			name:   "description too long",
			params: map[string]any{"project_id": "p", "source_branch": "feat", "target_branch": "main", "title": "t", "description": strings.Repeat("x", maxBodyLen+1)},
			errMsg: "description exceeds",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := toolCreateMergeRequest(mustJSON(t, tc.params), nil)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.errMsg) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tc.errMsg)
			}
		})
	}
}

// --- gitlab_create_mr_discussion ---

func TestCreateMRDiscussionDryRun(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/merge_requests/10") {
			jsonResponse(w, `{
				"iid":10,"title":"Test MR","state":"opened",
				"author":{"username":"alice"},
				"source_branch":"feat","target_branch":"main",
				"diff_refs":{"base_sha":"aaa","head_sha":"bbb","start_sha":"ccc"}
			}`)
			return
		}
		http.NotFound(w, r)
	})

	result, err := toolCreateMRDiscussion(mustJSON(t, map[string]any{
		"project_id": "team/proj",
		"mr_iid":     10,
		"body":       "Consider using a constant here",
		"new_path":   "src/main.go",
		"new_line":   15,
	}), nil)
	if err != nil {
		t.Fatalf("toolCreateMRDiscussion: %v", err)
	}

	m := result.(map[string]any)
	if m["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", m["dry_run"])
	}
	if m["action"] != "gitlab_create_mr_discussion" {
		t.Errorf("action = %v", m["action"])
	}
	if m["new_path"] != "src/main.go" {
		t.Errorf("new_path = %v", m["new_path"])
	}
	if m["base_sha"] != "aaa" {
		t.Errorf("base_sha = %v, want aaa (auto-fetched)", m["base_sha"])
	}
	if m["new_line"] != int64(15) {
		t.Errorf("new_line = %v, want 15", m["new_line"])
	}
	if _, hasOld := m["old_line"]; hasOld {
		t.Error("old_line should not be in dry run when not provided")
	}
}

func TestCreateMRDiscussionDryRunWithProvidedSHAs(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not make any HTTP calls when SHAs are provided in dry run")
		http.NotFound(w, r)
	})

	result, err := toolCreateMRDiscussion(mustJSON(t, map[string]any{
		"project_id": "team/proj",
		"mr_iid":     10,
		"body":       "Suggestion",
		"new_path":   "main.go",
		"new_line":   5,
		"base_sha":   "xxx",
		"head_sha":   "yyy",
		"start_sha":  "zzz",
	}), nil)
	if err != nil {
		t.Fatalf("toolCreateMRDiscussion: %v", err)
	}

	m := result.(map[string]any)
	if m["base_sha"] != "xxx" {
		t.Errorf("base_sha = %v, want xxx", m["base_sha"])
	}
}

func TestCreateMRDiscussionExecute(t *testing.T) {
	var gotBody map[string]any
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/discussions") {
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			jsonResponse(w, `{"id":"disc-1","notes":[{"id":100,"body":"Nice","author":{"username":"bot"}}]}`)
			return
		}
		if strings.Contains(r.URL.Path, "/merge_requests/10") {
			jsonResponse(w, `{
				"iid":10,"title":"Test","state":"opened",
				"author":{"username":"alice"},
				"diff_refs":{"base_sha":"aaa","head_sha":"bbb","start_sha":"ccc"}
			}`)
			return
		}
		http.NotFound(w, r)
	})

	dryRun := false
	result, err := toolCreateMRDiscussion(mustJSON(t, map[string]any{
		"project_id": "team/proj",
		"mr_iid":     10,
		"body":       "Nice catch",
		"new_path":   "pkg/foo.go",
		"new_line":   42,
		"dry_run":    dryRun,
	}), nil)
	if err != nil {
		t.Fatalf("toolCreateMRDiscussion: %v", err)
	}

	m := result.(map[string]any)
	if m["created"] != true {
		t.Errorf("created = %v, want true", m["created"])
	}
	if m["id"] != "disc-1" {
		t.Errorf("id = %v, want disc-1", m["id"])
	}

	if gotBody == nil {
		t.Fatal("expected POST body to be captured")
	}
	if gotBody["body"] != "Nice catch" {
		t.Errorf("posted body = %v", gotBody["body"])
	}
	pos, ok := gotBody["position"].(map[string]any)
	if !ok {
		t.Fatalf("position type = %T", gotBody["position"])
	}
	if pos["position_type"] != "text" {
		t.Errorf("position_type = %v", pos["position_type"])
	}
	if pos["new_path"] != "pkg/foo.go" {
		t.Errorf("new_path = %v", pos["new_path"])
	}
	if pos["base_sha"] != "aaa" {
		t.Errorf("base_sha = %v, want aaa", pos["base_sha"])
	}
	if pos["head_sha"] != "bbb" {
		t.Errorf("head_sha = %v, want bbb", pos["head_sha"])
	}
	if pos["start_sha"] != "ccc" {
		t.Errorf("start_sha = %v, want ccc", pos["start_sha"])
	}
}

func TestCreateMRDiscussionOldLineOnly(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/merge_requests/10") && r.Method == http.MethodGet {
			jsonResponse(w, `{
				"iid":10,"title":"Test","state":"opened",
				"author":{"username":"alice"},
				"diff_refs":{"base_sha":"aaa","head_sha":"bbb","start_sha":"ccc"}
			}`)
			return
		}
		http.NotFound(w, r)
	})

	result, err := toolCreateMRDiscussion(mustJSON(t, map[string]any{
		"project_id": "team/proj",
		"mr_iid":     10,
		"body":       "This removed line was important",
		"new_path":   "main.go",
		"old_line":   7,
	}), nil)
	if err != nil {
		t.Fatalf("toolCreateMRDiscussion: %v", err)
	}

	m := result.(map[string]any)
	if m["old_line"] != int64(7) {
		t.Errorf("old_line = %v, want 7", m["old_line"])
	}
	if _, hasNew := m["new_line"]; hasNew {
		t.Error("new_line should not be set when only old_line provided")
	}
}

func TestCreateMRDiscussionExecuteOldLineOnly(t *testing.T) {
	var gotBody map[string]any
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/discussions") {
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			jsonResponse(w, `{"id":"disc-2","notes":[{"id":101,"body":"old line","author":{"username":"bot"}}]}`)
			return
		}
		if strings.Contains(r.URL.Path, "/merge_requests/10") {
			jsonResponse(w, `{
				"iid":10,"title":"Test","state":"opened",
				"author":{"username":"alice"},
				"diff_refs":{"base_sha":"aaa","head_sha":"bbb","start_sha":"ccc"}
			}`)
			return
		}
		http.NotFound(w, r)
	})

	dryRun := false
	result, err := toolCreateMRDiscussion(mustJSON(t, map[string]any{
		"project_id": "team/proj",
		"mr_iid":     10,
		"body":       "This removed line was important",
		"new_path":   "main.go",
		"old_line":   7,
		"dry_run":    dryRun,
	}), nil)
	if err != nil {
		t.Fatalf("toolCreateMRDiscussion: %v", err)
	}

	m := result.(map[string]any)
	if m["created"] != true {
		t.Errorf("created = %v, want true", m["created"])
	}

	pos, ok := gotBody["position"].(map[string]any)
	if !ok {
		t.Fatalf("position type = %T", gotBody["position"])
	}
	if pos["old_line"] != float64(7) {
		t.Errorf("old_line = %v, want 7", pos["old_line"])
	}
	if _, hasNew := pos["new_line"]; hasNew {
		t.Error("new_line should not be in position when only old_line provided")
	}
}

func TestCreateMRDiscussionValidation(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		errMsg string
	}{
		{
			name:   "missing project_id",
			params: map[string]any{"mr_iid": 1, "body": "x", "new_path": "f", "new_line": 1},
			errMsg: "project_id and mr_iid are required",
		},
		{
			name:   "missing mr_iid",
			params: map[string]any{"project_id": "p", "body": "x", "new_path": "f", "new_line": 1},
			errMsg: "project_id and mr_iid are required",
		},
		{
			name:   "empty body",
			params: map[string]any{"project_id": "p", "mr_iid": 1, "new_path": "f", "new_line": 1},
			errMsg: "body is required",
		},
		{
			name:   "missing new_path",
			params: map[string]any{"project_id": "p", "mr_iid": 1, "body": "x", "new_line": 1},
			errMsg: "new_path is required",
		},
		{
			name:   "no line numbers",
			params: map[string]any{"project_id": "p", "mr_iid": 1, "body": "x", "new_path": "f"},
			errMsg: "at least one of new_line or old_line must be positive",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := toolCreateMRDiscussion(mustJSON(t, tc.params), nil)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.errMsg) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tc.errMsg)
			}
		})
	}
}

func TestCreateMRDiscussionOldPathDefault(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/merge_requests/10") {
			jsonResponse(w, `{
				"iid":10,"title":"Test","state":"opened",
				"author":{"username":"alice"},
				"diff_refs":{"base_sha":"aaa","head_sha":"bbb","start_sha":"ccc"}
			}`)
			return
		}
		http.NotFound(w, r)
	})

	result, err := toolCreateMRDiscussion(mustJSON(t, map[string]any{
		"project_id": "team/proj",
		"mr_iid":     10,
		"body":       "comment",
		"new_path":   "src/app.go",
		"new_line":   1,
	}), nil)
	if err != nil {
		t.Fatalf("toolCreateMRDiscussion: %v", err)
	}

	m := result.(map[string]any)
	if m["old_path"] != "src/app.go" {
		t.Errorf("old_path = %v, want src/app.go (should default to new_path)", m["old_path"])
	}
}

func TestCreateMRDiscussionBodyPreviewTruncation(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/merge_requests/10") {
			jsonResponse(w, `{
				"iid":10,"title":"Test","state":"opened",
				"author":{"username":"alice"},
				"diff_refs":{"base_sha":"aaa","head_sha":"bbb","start_sha":"ccc"}
			}`)
			return
		}
		http.NotFound(w, r)
	})

	longBody := strings.Repeat("x", 300)
	result, err := toolCreateMRDiscussion(mustJSON(t, map[string]any{
		"project_id": "team/proj",
		"mr_iid":     10,
		"body":       longBody,
		"new_path":   "f.go",
		"new_line":   1,
	}), nil)
	if err != nil {
		t.Fatalf("toolCreateMRDiscussion: %v", err)
	}

	m := result.(map[string]any)
	preview := m["body_preview"].(string)
	if len([]rune(preview)) != 200 {
		t.Errorf("body_preview length = %d runes, want 200", len([]rune(preview)))
	}
}

// --- gitlab_add_mr_note ---

func TestAddMRNoteDryRun(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not make any HTTP calls in dry run")
		http.NotFound(w, r)
	})

	result, err := toolAddMRNote(mustJSON(t, map[string]any{
		"project_id": "team/proj",
		"mr_iid":     10,
		"body":       "Overall LGTM",
	}), nil)
	if err != nil {
		t.Fatalf("toolAddMRNote: %v", err)
	}

	m := result.(map[string]any)
	if m["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", m["dry_run"])
	}
	if m["action"] != "gitlab_add_mr_note" {
		t.Errorf("action = %v", m["action"])
	}
	if m["body_preview"] != "Overall LGTM" {
		t.Errorf("body_preview = %v", m["body_preview"])
	}
}

func TestAddMRNoteExecute(t *testing.T) {
	var gotBody map[string]any
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/notes") {
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			jsonResponse(w, `{"id":200,"body":"Great work","author":{"username":"bot"}}`)
			return
		}
		http.NotFound(w, r)
	})

	dryRun := false
	result, err := toolAddMRNote(mustJSON(t, map[string]any{
		"project_id": "team/proj",
		"mr_iid":     10,
		"body":       "Great work",
		"dry_run":    dryRun,
	}), nil)
	if err != nil {
		t.Fatalf("toolAddMRNote: %v", err)
	}

	m := result.(map[string]any)
	if m["created"] != true {
		t.Errorf("created = %v, want true", m["created"])
	}
	if m["id"] != int64(200) {
		t.Errorf("id = %v (%T), want 200", m["id"], m["id"])
	}
	if gotBody["body"] != "Great work" {
		t.Errorf("posted body = %v", gotBody["body"])
	}
}

func TestAddMRNoteValidation(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		errMsg string
	}{
		{
			name:   "missing project_id",
			params: map[string]any{"mr_iid": 1, "body": "x"},
			errMsg: "project_id and mr_iid are required",
		},
		{
			name:   "missing mr_iid",
			params: map[string]any{"project_id": "p", "body": "x"},
			errMsg: "project_id and mr_iid are required",
		},
		{
			name:   "empty body",
			params: map[string]any{"project_id": "p", "mr_iid": 1},
			errMsg: "body is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := toolAddMRNote(mustJSON(t, tc.params), nil)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.errMsg) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tc.errMsg)
			}
		})
	}
}

// --- fetchDiffRefs ---

func TestFetchDiffRefs(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/merge_requests/5") {
			jsonResponse(w, `{
				"iid":5,"title":"Test","state":"opened",
				"author":{"username":"alice"},
				"diff_refs":{"base_sha":"b1","head_sha":"h1","start_sha":"s1"}
			}`)
			return
		}
		http.NotFound(w, r)
	})

	base, head, start, err := fetchDiffRefs(instances["default"].Client, "team/proj", 5)
	if err != nil {
		t.Fatalf("fetchDiffRefs: %v", err)
	}
	if base != "b1" || head != "h1" || start != "s1" {
		t.Errorf("got base=%q head=%q start=%q", base, head, start)
	}
}

func TestFetchDiffRefsEmptySHAs(t *testing.T) {
	setupGitLabTest(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/merge_requests/99") {
			jsonResponse(w, `{
				"iid":99,"title":"Empty MR","state":"opened",
				"author":{"username":"alice"},
				"diff_refs":{"base_sha":"","head_sha":"","start_sha":""}
			}`)
			return
		}
		http.NotFound(w, r)
	})

	_, _, _, err := fetchDiffRefs(instances["default"].Client, "team/proj", 99)
	if err == nil {
		t.Fatal("expected error for empty diff refs")
	}
	if !strings.Contains(err.Error(), "has no diff refs") {
		t.Errorf("error = %q, want to contain 'has no diff refs'", err.Error())
	}
}
