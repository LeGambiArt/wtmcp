# Jenkins Plugin

Read-only access to Jenkins pipeline results, build logs, and test reports.

## Quick Start

- **What jobs exist?** → jenkins_list_jobs
- **What happened in the last build?** → jenkins_get_build with build_number="lastBuild"
- **Why did the build fail?** → jenkins_get_build_log (check the tail)
- **Filter logs for errors** → jenkins_get_build_log with grep="ERROR"

## Common Workflows

### Investigate a build failure

1. jenkins_get_build with build_number="lastFailedBuild"
   - Check result, duration, causes
2. jenkins_get_build_log with tail_lines=200 (default)
   - Look for ERROR, FAILED, Exception
3. Use grep to filter: jenkins_get_build_log with grep="ERROR"
4. If still unclear, increase tail_lines or use grep="WARN"

### Monitor a pipeline

1. jenkins_list_jobs to see all jobs and their health
2. jenkins_get_build with build_number="lastBuild" for latest status
3. Compare with build_number="lastSuccessfulBuild" to see if broken

### Navigate folder structure

Jobs in Jenkins are organized in folders. Use slash-separated paths:
- Root level: "my-pipeline"
- Nested: "team/project/my-pipeline"

Use jenkins_list_jobs with folder parameter to browse:
- jenkins_list_jobs with folder="team" → lists jobs in team folder
- jenkins_list_jobs with folder="team/project" → lists jobs in team/project folder

## Job Names

Job names include the folder path with forward slashes:
- Simple: "my-pipeline" (root level)
- In folder: "team/my-pipeline"
- Nested: "team/project/my-pipeline"

Always use the full folder path when specifying job_name.

## Build Number Aliases

Instead of a specific build number, you can use these aliases:
- **"lastBuild"** - Most recent build (any result)
- **"lastSuccessfulBuild"** - Most recent SUCCESS
- **"lastFailedBuild"** - Most recent FAILURE
- **"lastStableBuild"** - Most recent SUCCESS or UNSTABLE
- **"lastCompletedBuild"** - Most recent finished build (not in progress)

## Log Handling

Build logs can be very large (50MB+). The plugin returns the **last 200 lines by default** to avoid overwhelming context.

### Controlling log size

- **tail_lines**: Number of lines from the end (default 200, max 5000)
  - tail_lines=10 → last 10 lines only
  - tail_lines=0 → full log (use with caution!)

### Filtering logs

- **grep**: Case-insensitive pattern to match lines
  - grep="ERROR" → only lines containing "error" (case-insensitive)
  - grep="FAILED" → only lines with "failed"
  - grep="Exception" → only exception lines

### Secret redaction

Logs are automatically scrubbed before being returned. Secrets are replaced with **[REDACTED]**:
- AWS access keys and secret keys
- JWT tokens (eyJ... patterns)
- Password, token, api_key patterns (password=..., token=...)
- Connection strings (jdbc://, mongodb://, etc.)
- Private key blocks

If you see [REDACTED] in logs, it means sensitive data was detected and removed.

## Authentication

This plugin supports three authentication methods:

1. **Username + API Token** (most common)
   - Set JENKINS_USER and JENKINS_TOKEN
   - Get API token from Jenkins: User menu → Configure → API Token

2. **Bearer Token** (OAuth2 proxies)
   - Set JENKINS_TOKEN only
   - Used when Jenkins is behind an OAuth2 proxy

3. **Kerberos** (enterprise)
   - No credentials needed in env file
   - Uses system Kerberos ticket (kinit)

## Limitations

- **Read-only**: Cannot trigger builds, modify configuration, or delete jobs
- **Secrets are redacted**: Logs show [REDACTED] instead of actual secrets
- **Log size limits**: Maximum 5000 lines per request to prevent context overflow
- **No real-time monitoring**: Not suitable for watching builds in progress

## Tips

- Start with jenkins_list_jobs to explore the Jenkins instance
- Use build aliases (lastFailedBuild) instead of specific numbers when investigating failures
- Use grep to find specific errors instead of reading full logs
- Check jenkins_get_build first to see if the failure is in tests, compilation, or deployment
- Logs are cached permanently for completed builds - subsequent requests are instant

## Phase 2 Tools (Extended Operations)

These tools provide deeper Jenkins integration for power users.

### jenkins_get_job

Get detailed job metadata:
- **job_name**: Full job path (required)
- **max_builds**: Recent build numbers to return (default 10, max 50)

Returns:
- Job type, description, URL
- Build parameters (name, type, default value, choices)
- Health score
- Last N build numbers

Example use:
- Check what parameters a job accepts before running
- See job health trend
- Get recent build numbers for further investigation

### jenkins_list_builds

List build history with filtering:
- **job_name**: Full job path (required)
- **result_filter**: SUCCESS, FAILURE, UNSTABLE, etc.
- **max_results**: Builds to return (default 20, max 100)
- **offset**: Skip first N builds (pagination)

Returns list of builds with number, result, timestamp, duration.

Example use:
- Find all failures: `result_filter="FAILURE"`
- Paginate through 1000 builds: `offset=0, max_results=100`, then `offset=100`, etc.
- Get last 50 successes: `result_filter="SUCCESS", max_results=50`

### jenkins_list_views

List all Jenkins views.

Use before jenkins_get_view to discover available views.

### jenkins_get_view

Get jobs in a specific view:
- **view_name**: View name (required)
- **name_filter**: Filter jobs by name

Returns jobs in the view (similar to jenkins_list_jobs).

Example use:
- List production jobs: `view_name="Production"`
- Find deploy jobs in prod view: `view_name="Production", name_filter="deploy"`

### jenkins_get_queue

Get current build queue (never cached):
- **stuck_only**: Show only stuck items (default false)

Returns pending builds waiting for executors.

Example use:
- Monitor queue length during peak times
- Find stuck builds: `stuck_only=true`
- Check why builds are waiting

### jenkins_flush_cache

Clear plugin cache (admin tool):
- **confirm**: Must be true to proceed

Use when:
- Jenkins configuration changed (new jobs, renamed jobs)
- Debugging stale data issues
- After bulk operations

**Warning:** Flushes all cached job lists, builds, logs. Next requests will be slower.

## Advanced Workflows

### Find all recent failures across multiple jobs

1. jenkins_list_jobs to get all jobs
2. For each job: jenkins_list_builds with result_filter="FAILURE", max_results=5
3. jenkins_get_build_log for each failure to investigate

### Monitor a specific view

1. jenkins_get_view with view_name="Production"
2. For each job in view: jenkins_get_job to check health
3. If health < 80: jenkins_list_builds with result_filter="FAILURE"

### Investigate queue congestion

1. jenkins_get_queue to see pending builds
2. If queue > 10 items: check stuck_only=true
3. For stuck items: jenkins_get_job to check if job is disabled or misconfigured

## Phase 3 Tools (Pipeline & Test Results)

These tools provide pipeline stage analysis and test result parsing for CI/CD workflows.

### jenkins_get_test_results

Get JUnit test results for a build:
- **job_name**: Full job path (required)
- **build_number**: Build number or alias (required)

Returns:
- Total, passed, failed, skipped counts
- Duration in seconds
- Failure details with test name, class, message, stack trace (500 char limit)

Example use:
- Find which tests failed: `jenkins_get_test_results(job_name="my-job", build_number="lastFailedBuild")`
- Analyze test trends across builds
- Debug specific test failures with stack traces

**Note:** Requires builds to publish JUnit XML reports. Returns error if no test results available.

### jenkins_get_pipeline_stages

Get pipeline stage breakdown:
- **job_name**: Full job path (required)
- **build_number**: Build number or alias (required)

Returns:
- Stage list with ID, name, status, start time, duration
- Building flag (true if pipeline still running)

Example use:
- Find which stage failed: Look for `status: "FAILED"`
- Analyze stage durations to optimize pipeline
- Get stage IDs for jenkins_get_stage_log

**Note:** Requires Pipeline plugin. Returns graceful error if plugin not installed or job is not a Pipeline.

### jenkins_get_stage_log

Get console log for a specific pipeline stage:
- **job_name**: Full job path (required)
- **build_number**: Build number or alias (required)
- **stage_id**: Stage ID from jenkins_get_pipeline_stages (required)
- **tail_lines**: Lines from end (default 200, max 5000)

Returns:
- Scrubbed stage log (secrets replaced with [REDACTED])
- Line count

Example use:
1. jenkins_get_pipeline_stages to get stage IDs
2. jenkins_get_stage_log with stage_id to see logs for that stage only
3. Use tail_lines to limit output for long stages

**Note:** Same security as jenkins_get_build_log - all secrets are scrubbed.

## Advanced Phase 3 Workflows

### Investigate test failures in failed build

1. jenkins_get_build with build_number="lastFailedBuild" to confirm failure
2. jenkins_get_test_results to see which tests failed
3. For each failure, examine stack trace to identify root cause
4. If needed, jenkins_get_build_log to see full console output

### Debug pipeline stage failure

1. jenkins_get_pipeline_stages to find which stage failed
2. jenkins_get_stage_log with failed stage's ID
3. Look for ERROR, Exception, or exit code messages
4. If stage log is too long, use tail_lines parameter

### Monitor pipeline performance

1. jenkins_get_pipeline_stages to get all stage durations
2. Identify slowest stages (highest duration_ms)
3. jenkins_get_stage_log for slow stages to find bottlenecks
4. Compare durations across multiple builds to detect regressions

### Full Pipeline + Test Analysis

1. jenkins_get_build to get overall build status
2. jenkins_get_pipeline_stages to see stage breakdown
3. jenkins_get_test_results to see test results
4. For failures: jenkins_get_stage_log + jenkins_get_build_log as needed
