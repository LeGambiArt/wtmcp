# Snyk Plugin Context

## Tool Usage Guidelines

### Discovery Flow

Every Snyk session starts with discovering the org ID:

1. Call `snyk_list_orgs` (no args) to get available organizations
2. Use the org `id` from the result in all subsequent calls
3. Call `snyk_list_projects` with `org_id` to browse projects

### Issue Identifiers

Each issue has two identifiers — using the wrong one for ignores
will silently fail:

- `id` — global issue ID, use with `snyk_get_issue`
- `key` — project-scoped finding ID, use with `snyk_ignore_issue` / `snyk_delete_ignore`

### Triage Workflow

1. List issues: `snyk_list_issues` with `org_id` and optionally `project_id`
   - Filter by `severity`: critical, high, medium, low
   - Filter by `issue_type`: package_vulnerability, code, license, cloud, custom
2. Prioritize critical and high severity first
3. Check the `ignored` field to skip already-suppressed issues
4. Get details: `snyk_get_issue` for remediation info
5. Present findings grouped by severity

### Write Operations

Ignore tools default to `dry_run=true`. Always preview before applying:

1. Call `snyk_ignore_issue` with `dry_run: true` (default)
2. Show the preview to the user
3. Only set `dry_run: false` after explicit approval

Ignore reason types:
- `not-vulnerable` — false positive, the finding does not apply
- `wont-fix` — accepted risk, acknowledged but won't be fixed
- `temporary-ignore` — deferred, will be addressed later

### Reviewing Ignores

- `snyk_list_ignores` returns all suppressed issues for a project
- Use this to audit existing ignores or check for duplicates before adding

### API Notes

- REST API (`/rest/`) is used for read operations (orgs, projects, issues)
- V1 API (`/v1/`) is used for ignore management (deprecated but no REST replacement yet)
- Snyk API requires an Enterprise plan for full access
