"""Unit tests for GitHub plugin write tools."""

from unittest.mock import patch

import handler
import pytest
import tools_write


def _mock_http(status, body, headers=None):
    return patch.object(handler, "http", return_value=(status, body, headers or {}))


def _mock_invalidate():
    return patch.object(handler, "invalidate_cache")


PR_HEAD_RESPONSE = {"head": {"sha": "abc123def456", "ref": "feature"}}


# --- github_create_review ---


class TestCreateReview:
    def test_dry_run_basic(self):
        with _mock_http(200, PR_HEAD_RESPONSE):
            result = tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "body": "LGTM",
                }
            )
        assert result["dry_run"] is True
        assert result["action"] == "github_create_review"
        assert result["event"] == "COMMENT"
        assert result["body_preview"] == "LGTM"

    def test_dry_run_with_comments(self):
        with _mock_http(200, PR_HEAD_RESPONSE):
            result = tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "body": "Review",
                    "comments": [{"path": "main.go", "line": 10, "body": "fix"}],
                }
            )
        assert result["comment_count"] == 1
        assert result["commit_id"] == "abc123def456"

    def test_success_comment(self):
        responses = [
            (200, PR_HEAD_RESPONSE, {}),
            (200, {"id": 1, "state": "COMMENTED", "html_url": "https://github.com/..."}, {}),
        ]
        with patch.object(handler, "http", side_effect=responses), _mock_invalidate() as inv:
            result = tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "body": "Looks good",
                    "dry_run": False,
                }
            )
        assert result["id"] == 1
        assert result["state"] == "COMMENTED"
        inv.assert_called_once()

    def test_success_request_changes(self):
        responses = [
            (200, PR_HEAD_RESPONSE, {}),
            (200, {"id": 2, "state": "CHANGES_REQUESTED", "html_url": "url"}, {}),
        ]
        with patch.object(handler, "http", side_effect=responses), _mock_invalidate():
            result = tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "body": "Please fix",
                    "event": "REQUEST_CHANGES",
                    "comments": [{"path": "a.py", "line": 5, "body": "bug here"}],
                    "dry_run": False,
                }
            )
        assert result["state"] == "CHANGES_REQUESTED"

    def test_approve_without_confirm_returns_warning(self):
        result = tools_write.create_review(
            {
                "repo": "org/repo",
                "pr_number": 42,
                "event": "APPROVE",
            }
        )
        assert "warning" in result
        assert result["dry_run"] is True
        assert result["event"] == "APPROVE"

    def test_approve_with_confirm(self):
        responses = [
            (200, PR_HEAD_RESPONSE, {}),
            (200, {"id": 3, "state": "APPROVED", "html_url": "url"}, {}),
        ]
        with patch.object(handler, "http", side_effect=responses), _mock_invalidate():
            result = tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "event": "APPROVE",
                    "confirm_approve": True,
                    "dry_run": False,
                }
            )
        assert result["state"] == "APPROVED"

    def test_invalid_event(self):
        with pytest.raises(ValueError, match="invalid event"):
            tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "event": "REJECTED",
                }
            )

    def test_event_case_insensitive(self):
        with _mock_http(200, PR_HEAD_RESPONSE):
            result = tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "event": "comment",
                }
            )
        assert result["event"] == "COMMENT"

    def test_request_changes_no_body_no_comments(self):
        with pytest.raises(ValueError, match="REQUEST_CHANGES requires"):
            tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "event": "REQUEST_CHANGES",
                    "dry_run": False,
                }
            )

    def test_comments_not_a_list(self):
        with pytest.raises(ValueError, match="comments must be a list"):
            tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "comments": {"path": "a.py", "line": 1, "body": "x"},
                }
            )

    def test_comment_missing_path(self):
        with pytest.raises(ValueError, match="'path' is required"):
            tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "comments": [{"line": 1, "body": "x"}],
                }
            )

    def test_comment_missing_body(self):
        with pytest.raises(ValueError, match="'body' is required"):
            tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "comments": [{"path": "a.py", "line": 1}],
                }
            )

    def test_comment_line_zero(self):
        with pytest.raises(ValueError, match="'line' is required"):
            tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "comments": [{"path": "a.py", "line": 0, "body": "x"}],
                }
            )

    def test_comment_start_line_gte_line(self):
        with pytest.raises(ValueError, match="start_line.*must be less"):
            tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "comments": [{"path": "a.py", "line": 5, "start_line": 10, "body": "x"}],
                }
            )

    def test_too_many_comments(self):
        comments = [{"path": "a.py", "line": i + 1, "body": "x"} for i in range(51)]
        with pytest.raises(ValueError, match="too many comments"):
            tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "comments": comments,
                }
            )

    def test_body_too_long(self):
        with pytest.raises(ValueError, match="character limit"):
            tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "body": "x" * 65001,
                }
            )

    def test_commit_id_provided_skips_fetch(self):
        with _mock_http(200, {"id": 1, "state": "COMMENTED", "html_url": "url"}) as mock_http, _mock_invalidate():
            result = tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "body": "ok",
                    "commit_id": "provided_sha",
                    "dry_run": False,
                }
            )
        assert result["id"] == 1
        mock_http.assert_called_once()

    def test_commit_id_auto_fetch_pr_not_found(self):
        with _mock_http(404, {"message": "Not Found"}):
            with pytest.raises(ValueError, match="could not resolve HEAD SHA"):
                tools_write.create_review(
                    {
                        "repo": "org/repo",
                        "pr_number": 999,
                        "dry_run": False,
                    }
                )

    def test_http_error(self):
        responses = [
            (200, PR_HEAD_RESPONSE, {}),
            (422, {"message": "Validation Failed"}, {}),
        ]
        with patch.object(handler, "http", side_effect=responses):
            result = tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "body": "x",
                    "dry_run": False,
                }
            )
        assert "error" in result
        assert "422" in result["error"]

    def test_invalid_repo(self):
        with pytest.raises(ValueError, match="invalid repo"):
            tools_write.create_review({"repo": "bad", "pr_number": 1})

    def test_dry_run_does_not_invalidate(self):
        with _mock_http(200, PR_HEAD_RESPONSE), _mock_invalidate() as inv:
            tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "body": "x",
                }
            )
        inv.assert_not_called()


# --- github_add_pr_comment ---


class TestAddPrComment:
    def test_dry_run(self):
        with _mock_http(200, PR_HEAD_RESPONSE):
            result = tools_write.add_pr_comment(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "path": "main.go",
                    "line": 10,
                    "body": "fix this",
                }
            )
        assert result["dry_run"] is True
        assert result["action"] == "github_add_pr_comment"
        assert result["path"] == "main.go"

    def test_success(self):
        responses = [
            (200, PR_HEAD_RESPONSE, {}),
            (201, {"id": 5, "html_url": "url"}, {}),
        ]
        with patch.object(handler, "http", side_effect=responses) as mock_http, _mock_invalidate() as inv:
            result = tools_write.add_pr_comment(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "path": "main.go",
                    "line": 10,
                    "body": "fix",
                    "dry_run": False,
                }
            )
        assert result["id"] == 5
        inv.assert_called_once()
        post_call = mock_http.call_args_list[1]
        assert post_call[0][0] == "POST"
        api_body = post_call[1]["body"]
        assert api_body["subject_type"] == "line"

    def test_multi_line(self):
        responses = [
            (200, PR_HEAD_RESPONSE, {}),
            (201, {"id": 6, "html_url": "url"}, {}),
        ]
        with patch.object(handler, "http", side_effect=responses) as mock_http, _mock_invalidate():
            tools_write.add_pr_comment(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "path": "a.py",
                    "line": 15,
                    "start_line": 10,
                    "body": "range",
                    "dry_run": False,
                }
            )
        api_body = mock_http.call_args_list[1][1]["body"]
        assert api_body["start_line"] == 10
        assert api_body["start_side"] == "RIGHT"

    def test_invalid_side(self):
        with pytest.raises(ValueError, match="invalid side"):
            tools_write.add_pr_comment(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "path": "a.py",
                    "line": 10,
                    "body": "x",
                    "side": "MIDDLE",
                }
            )

    def test_empty_body(self):
        with pytest.raises(ValueError, match="body is required"):
            tools_write.add_pr_comment(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "path": "a.py",
                    "line": 10,
                    "body": "",
                }
            )

    def test_line_zero(self):
        with pytest.raises(ValueError, match="line must be a positive"):
            tools_write.add_pr_comment(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "path": "a.py",
                    "line": 0,
                    "body": "x",
                }
            )

    def test_start_line_gte_line(self):
        with pytest.raises(ValueError, match="start_line.*must be less"):
            tools_write.add_pr_comment(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "path": "a.py",
                    "line": 5,
                    "start_line": 10,
                    "body": "x",
                }
            )

    def test_dry_run_includes_start_line(self):
        with _mock_http(200, PR_HEAD_RESPONSE):
            result = tools_write.add_pr_comment(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "path": "a.py",
                    "line": 15,
                    "start_line": 10,
                    "body": "range comment",
                }
            )
        assert result["start_line"] == 10
        assert result["side"] == "RIGHT"

    def test_http_error(self):
        responses = [
            (200, PR_HEAD_RESPONSE, {}),
            (422, {"message": "Validation Failed"}, {}),
        ]
        with patch.object(handler, "http", side_effect=responses):
            result = tools_write.add_pr_comment(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "path": "a.py",
                    "line": 10,
                    "body": "x",
                    "dry_run": False,
                }
            )
        assert "error" in result

    def test_invalid_repo(self):
        with pytest.raises(ValueError, match="invalid repo"):
            tools_write.add_pr_comment(
                {
                    "repo": "bad",
                    "pr_number": 1,
                    "path": "a.py",
                    "line": 1,
                    "body": "x",
                }
            )

    def test_commit_id_auto_fetch(self):
        responses = [
            (200, PR_HEAD_RESPONSE, {}),
            (201, {"id": 7, "html_url": "url"}, {}),
        ]
        with patch.object(handler, "http", side_effect=responses) as mock_http, _mock_invalidate():
            result = tools_write.add_pr_comment(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "path": "a.py",
                    "line": 10,
                    "body": "x",
                    "dry_run": False,
                }
            )
        assert result["id"] == 7
        assert mock_http.call_count == 2


# --- github_add_comment ---


class TestAddComment:
    def test_dry_run(self):
        result = tools_write.add_comment(
            {
                "repo": "org/repo",
                "issue_number": 10,
                "body": "Thanks for the report!",
            }
        )
        assert result["dry_run"] is True
        assert result["action"] == "github_add_comment"
        assert result["body_preview"] == "Thanks for the report!"

    def test_success(self):
        with _mock_http(201, {"id": 99, "html_url": "url"}), _mock_invalidate() as inv:
            result = tools_write.add_comment(
                {
                    "repo": "org/repo",
                    "issue_number": 10,
                    "body": "Fixed in v2",
                    "dry_run": False,
                }
            )
        assert result["id"] == 99
        inv.assert_called_once()

    def test_empty_body(self):
        with pytest.raises(ValueError, match="body is required"):
            tools_write.add_comment(
                {
                    "repo": "org/repo",
                    "issue_number": 10,
                    "body": "",
                }
            )

    def test_whitespace_only_body(self):
        with pytest.raises(ValueError, match="body is required"):
            tools_write.add_comment(
                {
                    "repo": "org/repo",
                    "issue_number": 10,
                    "body": "   ",
                }
            )

    def test_http_error(self):
        with _mock_http(422, {"message": "Validation Failed"}):
            result = tools_write.add_comment(
                {
                    "repo": "org/repo",
                    "issue_number": 10,
                    "body": "x",
                    "dry_run": False,
                }
            )
        assert "error" in result

    def test_invalid_repo(self):
        with pytest.raises(ValueError, match="invalid repo"):
            tools_write.add_comment(
                {
                    "repo": "bad",
                    "issue_number": 1,
                    "body": "x",
                }
            )

    def test_body_too_long(self):
        with pytest.raises(ValueError, match="character limit"):
            tools_write.add_comment(
                {
                    "repo": "org/repo",
                    "issue_number": 10,
                    "body": "x" * 65001,
                }
            )


# --- github_create_pull_request ---


class TestCreatePullRequest:
    def test_dry_run_default(self):
        result = tools_write.create_pull_request(
            {
                "repo": "org/repo",
                "title": "Add new feature",
                "head": "feature-branch",
                "base": "main",
                "body": "This PR adds a new feature.",
            }
        )
        assert result["dry_run"] is True
        assert result["action"] == "github_create_pull_request"
        assert result["head"] == "feature-branch"
        assert result["base"] == "main"
        assert result["title"] == "Add new feature"
        assert result["body_preview"] == "This PR adds a new feature."
        assert result["draft"] is False
        assert result["maintainer_can_modify"] is True

    def test_dry_run_no_body(self):
        result = tools_write.create_pull_request(
            {
                "repo": "org/repo",
                "title": "Fix bug",
                "head": "fix-branch",
                "base": "main",
            }
        )
        assert result["dry_run"] is True
        assert "body_preview" not in result

    def test_dry_run_draft(self):
        result = tools_write.create_pull_request(
            {
                "repo": "org/repo",
                "title": "WIP: new feature",
                "head": "wip-branch",
                "base": "main",
                "draft": True,
            }
        )
        assert result["dry_run"] is True
        assert result["draft"] is True

    def test_dry_run_body_truncated(self):
        result = tools_write.create_pull_request(
            {
                "repo": "org/repo",
                "title": "t",
                "head": "h",
                "base": "b",
                "body": "x" * 300,
            }
        )
        assert len(result["body_preview"]) == 200

    def test_success(self):
        pr_response = {
            "id": 1234567,
            "number": 42,
            "html_url": "https://github.com/org/repo/pull/42",
            "state": "open",
            "draft": False,
        }
        with _mock_http(201, pr_response) as mock_http, _mock_invalidate() as inv:
            result = tools_write.create_pull_request(
                {
                    "repo": "org/repo",
                    "title": "Add feature",
                    "head": "feature-branch",
                    "base": "main",
                    "body": "Description here",
                    "dry_run": False,
                }
            )
        assert result["id"] is not None
        assert result["number"] == 42
        assert result["html_url"] == "https://github.com/org/repo/pull/42"
        assert result["state"] == "open"
        inv.assert_called_once()
        _, kwargs = mock_http.call_args
        assert kwargs["body"]["title"] == "Add feature"
        assert kwargs["body"]["head"] == "feature-branch"
        assert kwargs["body"]["base"] == "main"
        assert kwargs["body"]["body"] == "Description here"

    def test_success_no_body(self):
        pr_response = {"number": 7, "html_url": "url", "state": "open", "draft": False}
        with _mock_http(201, pr_response), _mock_invalidate():
            result = tools_write.create_pull_request(
                {
                    "repo": "org/repo",
                    "title": "Fix bug",
                    "head": "fix-branch",
                    "base": "main",
                    "dry_run": False,
                }
            )
        assert result["number"] == 7

    def test_http_error(self):
        with _mock_http(422, {"message": "Validation Failed"}):
            result = tools_write.create_pull_request(
                {
                    "repo": "org/repo",
                    "title": "t",
                    "head": "h",
                    "base": "b",
                    "dry_run": False,
                }
            )
        assert "error" in result
        assert "422" in result["error"]

    def test_missing_repo(self):
        with pytest.raises(ValueError, match="invalid repo"):
            tools_write.create_pull_request({"title": "t", "head": "h", "base": "b"})

    def test_missing_title(self):
        with pytest.raises(ValueError, match="title is required"):
            tools_write.create_pull_request({"repo": "org/repo", "head": "h", "base": "b"})

    def test_missing_head(self):
        with pytest.raises(ValueError, match="head is required"):
            tools_write.create_pull_request({"repo": "org/repo", "title": "t", "base": "b"})

    def test_missing_base(self):
        with pytest.raises(ValueError, match="base is required"):
            tools_write.create_pull_request({"repo": "org/repo", "title": "t", "head": "h"})

    def test_whitespace_head(self):
        with pytest.raises(ValueError, match="head is required"):
            tools_write.create_pull_request({"repo": "org/repo", "title": "t", "head": "   ", "base": "b"})

    def test_whitespace_base(self):
        with pytest.raises(ValueError, match="base is required"):
            tools_write.create_pull_request({"repo": "org/repo", "title": "t", "head": "h", "base": "   "})

    def test_empty_title(self):
        with pytest.raises(ValueError, match="title is required"):
            tools_write.create_pull_request({"repo": "org/repo", "title": "  ", "head": "h", "base": "b"})

    def test_body_too_long(self):
        with pytest.raises(ValueError, match="character limit"):
            tools_write.create_pull_request(
                {
                    "repo": "org/repo",
                    "title": "t",
                    "head": "h",
                    "base": "b",
                    "body": "x" * 65001,
                }
            )

    def test_invalid_repo(self):
        with pytest.raises(ValueError, match="invalid repo"):
            tools_write.create_pull_request({"repo": "bad", "title": "t", "head": "h", "base": "b"})

    def test_dry_run_does_not_call_api(self):
        with _mock_http(201, {}) as mock_http, _mock_invalidate() as inv:
            tools_write.create_pull_request(
                {
                    "repo": "org/repo",
                    "title": "t",
                    "head": "h",
                    "base": "b",
                }
            )
        mock_http.assert_not_called()
        inv.assert_not_called()

    def test_error_does_not_invalidate(self):
        with _mock_http(422, {"message": "err"}), _mock_invalidate() as inv:
            tools_write.create_pull_request(
                {
                    "repo": "org/repo",
                    "title": "t",
                    "head": "h",
                    "base": "b",
                    "dry_run": False,
                }
            )
        inv.assert_not_called()


# --- Cache invalidation ---


class TestCacheInvalidation:
    def test_create_review_invalidates_on_success(self):
        responses = [
            (200, PR_HEAD_RESPONSE, {}),
            (200, {"id": 1, "state": "COMMENTED", "html_url": "url"}, {}),
        ]
        with patch.object(handler, "http", side_effect=responses), _mock_invalidate() as inv:
            tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "body": "ok",
                    "dry_run": False,
                }
            )
        inv.assert_called_once()

    def test_create_review_error_does_not_invalidate(self):
        responses = [
            (200, PR_HEAD_RESPONSE, {}),
            (422, {"message": "error"}, {}),
        ]
        with patch.object(handler, "http", side_effect=responses), _mock_invalidate() as inv:
            tools_write.create_review(
                {
                    "repo": "org/repo",
                    "pr_number": 42,
                    "body": "x",
                    "dry_run": False,
                }
            )
        inv.assert_not_called()

    def test_add_comment_invalidates_on_success(self):
        with _mock_http(201, {"id": 1, "html_url": "url"}), _mock_invalidate() as inv:
            tools_write.add_comment(
                {
                    "repo": "org/repo",
                    "issue_number": 1,
                    "body": "x",
                    "dry_run": False,
                }
            )
        inv.assert_called_once()

    def test_add_comment_dry_run_does_not_invalidate(self):
        with _mock_invalidate() as inv:
            tools_write.add_comment(
                {
                    "repo": "org/repo",
                    "issue_number": 1,
                    "body": "x",
                }
            )
        inv.assert_not_called()


# --- github_create_branch ---

REF_RESPONSE = {"object": {"sha": "a" * 40, "type": "commit"}, "ref": "refs/heads/main"}
CREATE_REF_RESPONSE = {"ref": "refs/heads/new-branch", "object": {"sha": "a" * 40}}


class TestCreateBranch:
    def test_dry_run(self):
        with _mock_http(200, REF_RESPONSE), _mock_invalidate() as inv:
            result = tools_write.create_branch(
                {"repo": "org/repo", "branch_name": "feature-x", "source_branch": "main"}
            )
        assert result["dry_run"] is True
        assert result["action"] == "github_create_branch"
        assert result["source_sha"] == "a" * 12
        inv.assert_not_called()

    def test_success(self):
        responses = [
            (200, REF_RESPONSE, {}),
            (201, CREATE_REF_RESPONSE, {}),
        ]
        with patch.object(handler, "http", side_effect=responses), _mock_invalidate() as inv:
            result = tools_write.create_branch({"repo": "org/repo", "branch_name": "feature-x", "dry_run": False})
        assert result["created"] is True
        assert result["ref"] == "refs/heads/new-branch"
        inv.assert_called_once()

    def test_source_not_found(self):
        with _mock_http(404, {"message": "Not Found"}):
            result = tools_write.create_branch({"repo": "org/repo", "branch_name": "x", "source_branch": "nonexistent"})
        assert "error" in result

    def test_invalid_branch_double_dot(self):
        with pytest.raises(ValueError, match="contains \\.\\. or //"):
            tools_write.create_branch({"repo": "org/repo", "branch_name": "a..b"})

    def test_invalid_branch_dot_lock(self):
        with pytest.raises(ValueError, match="ends with \\.lock"):
            tools_write.create_branch({"repo": "org/repo", "branch_name": "test.lock"})

    def test_invalid_branch_dot_component(self):
        with pytest.raises(ValueError, match="component starts with"):
            tools_write.create_branch({"repo": "org/repo", "branch_name": "feature/.hidden"})

    def test_invalid_repo(self):
        with pytest.raises(ValueError, match="invalid repo"):
            tools_write.create_branch({"repo": "bad", "branch_name": "x"})

    def test_single_char_branch(self):
        with _mock_http(200, REF_RESPONSE):
            result = tools_write.create_branch({"repo": "org/repo", "branch_name": "v"})
        assert result["dry_run"] is True

    def test_error_does_not_invalidate(self):
        responses = [
            (200, REF_RESPONSE, {}),
            (422, {"message": "Reference already exists"}, {}),
        ]
        with patch.object(handler, "http", side_effect=responses), _mock_invalidate() as inv:
            result = tools_write.create_branch({"repo": "org/repo", "branch_name": "main", "dry_run": False})
        assert "error" in result
        inv.assert_not_called()


# --- github_create_or_update_file ---


class TestCreateOrUpdateFile:
    def test_dry_run_new_file(self):
        with patch.object(handler, "http", return_value=(404, {"message": "Not Found"}, {})), _mock_invalidate() as inv:
            result = tools_write.create_or_update_file(
                {"repo": "org/repo", "path": "test.py", "content": "print(1)", "message": "add test"}
            )
        assert result["dry_run"] is True
        assert result["is_update"] is False
        inv.assert_not_called()

    def test_dry_run_existing_file(self):
        with patch.object(handler, "http", return_value=(200, {"sha": "old_sha"}, {})):
            result = tools_write.create_or_update_file(
                {"repo": "org/repo", "path": "test.py", "content": "print(2)", "message": "update test"}
            )
        assert result["dry_run"] is True
        assert result["is_update"] is True

    def test_create_success(self):
        responses = [
            (404, {"message": "Not Found"}, {}),
            (201, {"content": {"path": "test.py", "sha": "new_sha", "html_url": "url"}}, {}),
        ]
        with patch.object(handler, "http", side_effect=responses), _mock_invalidate() as inv:
            result = tools_write.create_or_update_file(
                {"repo": "org/repo", "path": "test.py", "content": "print(1)", "message": "add", "dry_run": False}
            )
        assert result["created"] is True
        assert result["is_update"] is False
        inv.assert_called_once()

    def test_update_includes_sha(self):
        responses = [
            (200, {"sha": "old_sha_value"}, {}),
            (200, {"content": {"path": "test.py", "sha": "new_sha", "html_url": "url"}}, {}),
        ]
        with patch.object(handler, "http", side_effect=responses) as mock_http, _mock_invalidate():
            tools_write.create_or_update_file(
                {"repo": "org/repo", "path": "test.py", "content": "updated", "message": "upd", "dry_run": False}
            )
        put_call = mock_http.call_args_list[1]
        api_body = put_call[1].get("body") or put_call[0][3] if len(put_call[0]) > 3 else put_call[1]["body"]
        assert api_body["sha"] == "old_sha_value"

    def test_missing_message(self):
        with pytest.raises(ValueError, match="message is required"):
            tools_write.create_or_update_file({"repo": "org/repo", "path": "f.py", "content": "x"})

    def test_missing_content(self):
        with pytest.raises(ValueError, match="content is required"):
            tools_write.create_or_update_file({"repo": "org/repo", "path": "f.py", "message": "m"})

    def test_missing_path(self):
        with pytest.raises(ValueError, match="path is required"):
            tools_write.create_or_update_file({"repo": "org/repo", "content": "x", "message": "m"})

    def test_path_traversal_rejected(self):
        with pytest.raises(ValueError, match="path traversal"):
            tools_write.create_or_update_file(
                {"repo": "org/repo", "path": "../../etc/passwd", "content": "x", "message": "m"}
            )

    def test_http_error(self):
        responses = [
            (404, {"message": "Not Found"}, {}),
            (422, {"message": "Validation Failed"}, {}),
        ]
        with patch.object(handler, "http", side_effect=responses), _mock_invalidate() as inv:
            result = tools_write.create_or_update_file(
                {"repo": "org/repo", "path": "f.py", "content": "x", "message": "m", "dry_run": False}
            )
        assert "error" in result
        inv.assert_not_called()

    def test_get_error_surfaces_not_swallowed(self):
        """Non-404 GET errors should be returned, not silently treated as 'file missing'."""
        with patch.object(handler, "http", return_value=(500, {"message": "Internal Server Error"}, {})):
            result = tools_write.create_or_update_file(
                {"repo": "org/repo", "path": "f.py", "content": "x", "message": "m", "dry_run": False}
            )
        assert "error" in result
        assert "500" in str(result["error"])
