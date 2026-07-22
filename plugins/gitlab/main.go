// gitlab handler is a persistent plugin for GitLab with multi-instance support.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/LeGambiArt/wtmcp/pkg/handler"
)

func main() {
	p := handler.New()

	p.OnInit(func(_ json.RawMessage) error {
		if err := discoverInstances(p); err != nil {
			return err
		}
		for _, inst := range instances {
			p.Log("instance %q: %s", inst.Name, inst.URL)
		}
		return nil
	})

	p.Handle("gitlab_get_commits", toolGetCommits)
	p.Handle("gitlab_get_commit_diff", toolGetCommitDiff)
	p.Handle("gitlab_get_merge_request", toolGetMergeRequest)
	p.Handle("gitlab_get_pipeline_jobs", toolGetPipelineJobs)
	p.Handle("gitlab_get_project_pipelines", toolGetProjectPipelines)
	p.Handle("gitlab_get_project_issues", toolGetProjectIssues)
	p.Handle("gitlab_get_issue_details", toolGetIssueDetails)
	p.Handle("gitlab_my_issues", toolMyIssues)
	p.Handle("gitlab_get_todos", toolGetTodos)
	p.Handle("gitlab_list_merge_requests", toolListMergeRequests)

	// Write tools
	p.Handle("gitlab_create_merge_request", toolCreateMergeRequest)
	p.Handle("gitlab_create_mr_discussion", toolCreateMRDiscussion)
	p.Handle("gitlab_add_mr_note", toolAddMRNote)

	if err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "handler: %v\n", err)
		os.Exit(1)
	}
}
