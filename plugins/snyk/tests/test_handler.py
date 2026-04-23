"""Unit tests for the Snyk plugin handler."""

from unittest.mock import patch

import handler


def _mock_http(status, body, headers=None):
    return patch.object(handler, "http", return_value=(status, body, headers or {}))


# ---------- Test data ----------

ORG_ITEM = {
    "id": "org-1",
    "attributes": {"name": "My Org", "slug": "my-org"},
}

PROJECT_ITEM = {
    "id": "proj-1",
    "attributes": {
        "name": "my-repo",
        "type": "npm",
        "origin": "github",
        "status": "active",
    },
}

ISSUE_ITEM = {
    "id": "issue-1",
    "attributes": {
        "title": "SQL Injection",
        "effective_severity_level": "high",
        "status": "open",
        "type": "vuln",
        "ignored": False,
        "key": "SNYK-JS-LODASH-1",
    },
}

IGNORES_RESPONSE = {
    "SNYK-JS-LODASH-1": [
        {
            "ignorePath": "*",
            "reason": "not applicable",
            "reasonType": "not-vulnerable",
            "created": "2024-01-01T00:00:00.000Z",
        }
    ]
}


# ---------- TestSnykListOrgs ----------


class TestSnykListOrgs:
    def test_happy_path(self):
        body = {"data": [ORG_ITEM]}
        with _mock_http(200, body):
            result = handler.snyk_list_orgs({})
        assert result["count"] == 1
        assert result["orgs"][0]["id"] == "org-1"
        assert result["orgs"][0]["name"] == "My Org"
        assert result["orgs"][0]["slug"] == "my-org"

    def test_api_error_raises_runtime_error(self):
        with _mock_http(403, {"message": "Forbidden"}):
            try:
                handler.snyk_list_orgs({})
                assert False, "should have raised RuntimeError"
            except RuntimeError as e:
                assert "403" in str(e)

    def test_pagination(self):
        """Two pages of results are concatenated."""
        page1 = {
            "data": [ORG_ITEM],
            "links": {"next": "https://api.snyk.io/rest/orgs?page=2"},
        }
        page2 = {"data": [{"id": "org-2", "attributes": {"name": "Org Two", "slug": "org-two"}}]}
        with patch.object(handler, "http", side_effect=[(200, page1, {}), (200, page2, {})]):
            result = handler.snyk_list_orgs({})
        assert result["count"] == 2
        assert result["orgs"][0]["id"] == "org-1"
        assert result["orgs"][1]["id"] == "org-2"


# ---------- TestSnykListProjects ----------


class TestSnykListProjects:
    def test_happy_path(self):
        body = {"data": [PROJECT_ITEM]}
        with _mock_http(200, body):
            result = handler.snyk_list_projects({"org_id": "org-1"})
        assert result["count"] == 1
        assert result["projects"][0]["id"] == "proj-1"
        assert result["projects"][0]["name"] == "my-repo"
        assert result["projects"][0]["type"] == "npm"
        assert result["projects"][0]["origin"] == "github"
        assert result["projects"][0]["status"] == "active"

    def test_name_filter_applied(self):
        """name_filter is passed in the query and used for client-side filtering."""
        items = [
            PROJECT_ITEM,
            {
                "id": "proj-2",
                "attributes": {"name": "other-repo", "type": "pip", "origin": "github", "status": "active"},
            },
        ]
        body = {"data": items}
        with _mock_http(200, body) as mock_http:
            result = handler.snyk_list_projects({"org_id": "org-1", "name_filter": "my-repo"})
        # names param should appear in the query
        call_kwargs = mock_http.call_args
        query_arg = call_kwargs[1].get("query") or (call_kwargs[0][2] if len(call_kwargs[0]) > 2 else None)
        assert query_arg is not None
        assert "names" in query_arg
        # client-side filter keeps only matching project
        assert result["count"] == 1
        assert result["projects"][0]["id"] == "proj-1"

    def test_missing_org_id_raises(self):
        """KeyError when org_id is absent."""
        try:
            handler.snyk_list_projects({})
            assert False, "should have raised KeyError"
        except KeyError:
            pass


# ---------- TestSnykListIssues ----------


class TestSnykListIssues:
    def test_happy_path(self):
        body = {"data": [ISSUE_ITEM]}
        with _mock_http(200, body):
            result = handler.snyk_list_issues({"org_id": "org-1"})
        assert result["count"] == 1
        issue = result["issues"][0]
        assert issue["id"] == "issue-1"
        assert issue["title"] == "SQL Injection"
        assert issue["severity"] == "high"
        assert issue["status"] == "open"
        assert issue["ignored"] is False

    def test_filter_params_sent_in_query(self):
        """project_id, severity and issue_type are encoded as query parameters."""
        body = {"data": [ISSUE_ITEM]}
        with _mock_http(200, body) as mock_http:
            handler.snyk_list_issues(
                {
                    "org_id": "org-1",
                    "project_id": "proj-1",
                    "severity": "high",
                    "issue_type": "vuln",
                }
            )
        call_kwargs = mock_http.call_args
        query_arg = call_kwargs[1].get("query") or (call_kwargs[0][2] if len(call_kwargs[0]) > 2 else None)
        assert query_arg is not None
        assert query_arg.get("scan_item.id") == "proj-1"
        assert query_arg.get("scan_item.type") == "project"
        assert query_arg.get("severity") == "high"
        assert query_arg.get("type") == "vuln"


# ---------- TestSnykGetIssue ----------


class TestSnykGetIssue:
    def test_happy_path(self):
        body = {"data": {"id": "issue-1", "attributes": {"title": "SQL Injection"}}}
        with _mock_http(200, body):
            result = handler.snyk_get_issue({"org_id": "org-1", "issue_id": "issue-1"})
        assert result["id"] == "issue-1"
        assert result["attributes"]["title"] == "SQL Injection"

    def test_404_raises_runtime_error(self):
        with _mock_http(404, {"message": "Not found"}):
            try:
                handler.snyk_get_issue({"org_id": "org-1", "issue_id": "bad-id"})
                assert False, "should have raised RuntimeError"
            except RuntimeError as e:
                assert "404" in str(e)


# ---------- TestSnykListIgnores ----------


class TestSnykListIgnores:
    def test_happy_path(self):
        with _mock_http(200, IGNORES_RESPONSE):
            result = handler.snyk_list_ignores({"org_id": "org-1", "project_id": "proj-1"})
        assert "SNYK-JS-LODASH-1" in result
        assert result["SNYK-JS-LODASH-1"][0]["reasonType"] == "not-vulnerable"


# ---------- TestSnykIgnoreIssue ----------


class TestSnykIgnoreIssue:
    def test_dry_run_no_http(self):
        """dry_run=True returns a preview dict without making HTTP calls."""
        with patch.object(handler, "http") as mock_http:
            result = handler.snyk_ignore_issue(
                {
                    "org_id": "org-1",
                    "project_id": "proj-1",
                    "issue_id": "issue-1",
                    "reason": "FP",
                    "dry_run": True,
                }
            )
        mock_http.assert_not_called()
        assert result["dry_run"] is True
        assert result["action"] == "snyk_ignore_issue"
        assert result["body"]["reasonType"] == "not-vulnerable"

    def test_real_run_posts(self):
        """dry_run=False performs a V1 POST and returns success."""
        with _mock_http(200, {"ok": True}):
            result = handler.snyk_ignore_issue(
                {
                    "org_id": "org-1",
                    "project_id": "proj-1",
                    "issue_id": "issue-1",
                    "reason": "FP",
                    "reason_type": "wont-fix",
                    "dry_run": False,
                }
            )
        assert result["success"] is True
        assert result["issue_id"] == "issue-1"

    def test_invalid_reason_type_raises_value_error(self):
        try:
            handler.snyk_ignore_issue(
                {
                    "org_id": "org-1",
                    "project_id": "proj-1",
                    "issue_id": "issue-1",
                    "reason": "test",
                    "reason_type": "invalid-type",
                    "dry_run": False,
                }
            )
            assert False, "should have raised ValueError"
        except ValueError as e:
            assert "invalid-type" in str(e)


# ---------- TestSnykDeleteIgnore ----------


class TestSnykDeleteIgnore:
    def test_dry_run_no_http(self):
        """dry_run=True returns a preview dict without making HTTP calls."""
        with patch.object(handler, "http") as mock_http:
            result = handler.snyk_delete_ignore(
                {
                    "org_id": "org-1",
                    "project_id": "proj-1",
                    "issue_id": "issue-1",
                    "dry_run": True,
                }
            )
        mock_http.assert_not_called()
        assert result["dry_run"] is True
        assert result["action"] == "snyk_delete_ignore"

    def test_real_run_deletes(self):
        """dry_run=False performs a V1 DELETE and returns success."""
        with _mock_http(200, {}):
            result = handler.snyk_delete_ignore(
                {
                    "org_id": "org-1",
                    "project_id": "proj-1",
                    "issue_id": "issue-1",
                    "dry_run": False,
                }
            )
        assert result["success"] is True
        assert result["issue_id"] == "issue-1"
