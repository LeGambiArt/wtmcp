# GitLab Plugin

## Tool Usage Guidelines

### Multi-Instance Support

This plugin supports multiple GitLab instances. Each tool has an
optional `instance` parameter:

- If only one instance is configured, `instance` can be omitted
- With multiple instances, pass the instance name explicitly

**Example with multiple instances:**
```
gitlab_get_commits(instance="internal", project_id="team/myproject")
gitlab_list_merge_requests(instance="public", scope="assigned_to_me")
```

Instances are discovered from environment variables:
- no variables → anonymous access to `https://gitlab.com`
- `GITLAB_URL` only → anonymous access to that instance
- `GITLAB_TOKEN` + optional `GITLAB_URL` → authenticated single instance
- `GITLAB_PUBLIC_TOKEN` + `GITLAB_PUBLIC_URL` → instance "public"
- `GITLAB_INTERNAL_TOKEN` + `GITLAB_INTERNAL_URL` → instance "internal"

Authentication headers are handled by the core HTTP proxy; the plugin
only tracks whether a token is configured. For multi-instance,
per-domain auth binding routes the correct token to each GitLab server. Tools
that require the current user (`gitlab_my_issues`, `gitlab_get_todos`,
or user-specific merge request scopes) and all write operations still
require a token.

### Project IDs

Most tools require `project_id`. Use the project path:
```
project_id: "group/project"
project_id: "namespace/subgroup/project"
```

Numeric IDs also work but paths are more readable.

### Common Patterns

**My recent commits:**
```
gitlab_get_commits(project_id="team/myproject", max_results=10)
```

**Commits by a specific author:**
```
gitlab_get_commits(project_id="team/myproject", author="alice@example.com")
```

**Commits in a date range:**
```
gitlab_get_commits(project_id="team/myproject",
                   since="2026-03-01", until="2026-03-10")
```

**View a commit's changes:**
```
gitlab_get_commit_diff(project_id="team/myproject",
                       commit_sha="abc123", format="json")
```

### Merge Requests

**My open MRs across all projects:**
```
gitlab_list_merge_requests(scope="created_by_me")
```

**MRs assigned to me:**
```
gitlab_list_merge_requests(scope="assigned_to_me")
```

**MRs in a specific project:**
```
gitlab_list_merge_requests(project_id="team/myproject", state="opened")
```

**Full MR details with comments and diffs:**
```
gitlab_get_merge_request(project_id="team/myproject", mr_iid=42)
```

### CI/CD Pipelines

**Recent pipelines:**
```
gitlab_get_project_pipelines(project_id="team/myproject")
```

**Failed pipelines only:**
```
gitlab_get_project_pipelines(project_id="team/myproject", status="failed")
```

**Pipeline jobs with logs (for debugging failures):**
```
gitlab_get_pipeline_jobs(project_id="team/myproject",
                         pipeline_id=12345, include_logs=true)
```
Job logs are truncated at 50KB.

### Issues

**Open issues in a project:**
```
gitlab_get_project_issues(project_id="team/myproject", state="opened")
```

**Issues assigned to a user:**
```
gitlab_get_project_issues(project_id="team/myproject",
                          assignee="alice", state="opened")
```

**Search issues:**
```
gitlab_get_project_issues(project_id="team/myproject",
                          search="authentication bug")
```

**Full issue details with notes:**
```
gitlab_get_issue_details(project_id="team/myproject", issue_iid=15)
```

### To-Do Items

**Pending to-dos:**
```
gitlab_get_todos()
```

**To-dos for a specific type:**
```
gitlab_get_todos(target_type="MergeRequest")
```

**Pagination — fetch next page:**
```
gitlab_get_todos(page=2)
```

The response includes `page`, `total_pages`, `total_items`, and
`has_next_page` to help navigate results.

The `body` field is truncated to 200 characters for compactness.
For full content, use the detail tools (`gitlab_get_merge_request`,
`gitlab_get_issue_details`).

### IDs and IIDs

- **IID** (Internal ID): project-scoped number shown in the UI
  (e.g., MR !42, Issue #15). Use this in tool parameters.
- **ID**: global numeric ID. Returned in responses but rarely
  needed as input.

### Writing MR Comments

**Post an inline discussion on a diff line:**
```
gitlab_create_mr_discussion(project_id="team/myproject", mr_iid=42,
    body="Consider using a constant here",
    new_path="src/main.go", new_line=15)
```

For removed lines, use `old_line` instead of `new_line`. For context
(unchanged) lines, provide both. The DiffRefs SHAs (`base_sha`,
`head_sha`, `start_sha`) are auto-fetched from the MR if omitted.

**Post a general comment on an MR:**
```
gitlab_add_mr_note(project_id="team/myproject", mr_iid=42,
    body="Overall this looks good, just a few minor suggestions.")
```

Both tools default to `dry_run=true` — set `dry_run=false` to
actually post the comment.
