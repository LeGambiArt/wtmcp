"""Unit tests for tools_read.py — mock HTTP/cache protocol calls."""

from unittest.mock import patch

import handler
import pytest
import tools_read


def _mock_http(status, body):
    """Return a mock for handler.http that returns (status, body, headers)."""
    return patch.object(handler, "http", return_value=(status, body, {}))


def _mock_cache_get(value=None):
    """Return a mock for handler.cache_get. None means cache miss."""
    return patch.object(handler, "cache_get", return_value=value)


def _mock_cache_set():
    """Return a mock for handler.cache_set."""
    return patch.object(handler, "cache_set")


MYSELF_RESPONSE = {
    "accountId": "abc123",
    "name": "jdoe",
    "displayName": "Jane Doe",
    "emailAddress": "jane@example.com",
    "active": True,
    "timeZone": "UTC",
    "avatarUrls": {"48x48": "https://example.com/avatar.png"},
    "locale": "en_US",
}

SAMPLE_ISSUES: list[dict] = [
    {
        "key": "PROJ-1",
        "fields": {
            "summary": "First issue",
            "status": {"name": "Open"},
            "assignee": {"displayName": "Alice"},
            "priority": {"name": "High"},
        },
    },
    {
        "key": "PROJ-2",
        "fields": {
            "summary": "Second issue",
            "status": {"name": "Closed"},
            "assignee": None,
            "priority": {"name": "Low"},
        },
    },
]

SEARCH_RESPONSE = {"total": 2, "issues": SAMPLE_ISSUES}


# --- jira_get_myself ---


class TestGetMyself:
    def test_cache_miss_fetches_and_caches(self):
        with _mock_cache_get(None), _mock_http(200, MYSELF_RESPONSE), _mock_cache_set() as mock_set:
            result = tools_read.get_myself({})
            assert result["accountId"] == "abc123"
            assert result["displayName"] == "Jane Doe"
            # Should not include raw fields like avatarUrls
            assert "avatarUrls" not in result
            # Should have cached
            mock_set.assert_called_once()
            assert mock_set.call_args[1]["ttl"] == 3600

    def test_cache_hit_returns_cached(self):
        cached = {"accountId": "abc123", "displayName": "Jane Doe"}
        with _mock_cache_get(cached) as mock_get:
            result = tools_read.get_myself({})
            assert result == cached
            mock_get.assert_called_once_with("myself")

    def test_http_error_returns_error_body(self):
        with _mock_cache_get(None), _mock_http(401, {"error": "Unauthorized"}):
            result = tools_read.get_myself({})
            assert result["error"] == "HTTP 401"


# --- jira_search ---


class TestSearch:
    def test_brief_mode(self):
        with _mock_cache_get(None), _mock_http(200, SEARCH_RESPONSE), _mock_cache_set():
            result = tools_read.search({"jql": "project = PROJ", "brief": True})
            assert result["total"] == 2
            assert result["count"] == 2
            assert result["issues"][0] == {
                "key": "PROJ-1",
                "summary": "First issue",
                "status": "Open",
                "assignee": "Alice",
                "priority": "High",
            }
            # Null assignee should become empty string
            assert result["issues"][1]["assignee"] == ""

    def test_full_mode(self):
        with _mock_cache_get(None), _mock_http(200, SEARCH_RESPONSE), _mock_cache_set():
            result = tools_read.search({"jql": "project = PROJ", "brief": False})
            # Full mode returns clean extracted fields
            assert result["issues"][0]["status"] == "Open"
            assert result["issues"][0]["key"] == "PROJ-1"
            assert "fields" not in result["issues"][0]

    def test_truncated_warning(self):
        truncated = {"total": 100, "issues": SEARCH_RESPONSE["issues"]}
        with _mock_cache_get(None), _mock_http(200, truncated), _mock_cache_set():
            result = tools_read.search({"jql": "project = PROJ"})
            assert result["truncated"] is True
            assert "warning" in result

    def test_max_results_capped_at_200(self):
        with _mock_cache_get(None), _mock_http(200, SEARCH_RESPONSE) as mock_http, _mock_cache_set():
            tools_read.search({"jql": "project = PROJ", "max_results": 500})
            query = mock_http.call_args[1].get("query") or mock_http.call_args[0][2]
            assert query["maxResults"] == "200"

    def test_cache_hit(self):
        cached = {"total": 1, "count": 1, "issues": []}
        with _mock_cache_get(cached):
            result = tools_read.search({"jql": "project = PROJ"})
            assert result == cached

    def test_cache_key_varies_with_params(self):
        """Different max_results/fields/brief must produce different cache keys."""
        keys = set()
        base = {"jql": "project = PROJ"}
        combos = [
            base,
            {**base, "max_results": 10},
            {**base, "fields": "summary"},
            {**base, "brief": False},
        ]
        for params in combos:
            with _mock_cache_get(None), _mock_http(200, SEARCH_RESPONSE), _mock_cache_set() as mock_set:
                tools_read.search(params)
                cache_key = mock_set.call_args[0][0]
                keys.add(cache_key)
        assert len(keys) == len(combos), f"Expected {len(combos)} unique keys, got {len(keys)}"

    def test_http_error(self):
        with _mock_cache_get(None), _mock_http(500, {"error": "Server Error"}):
            result = tools_read.search({"jql": "bad"})
            assert result["error"] == "HTTP 500"

    def test_start_at_passed_in_query(self):
        with _mock_cache_get(None), _mock_http(200, SEARCH_RESPONSE) as mock_http, _mock_cache_set():
            tools_read.search({"jql": "project = PROJ", "start_at": 25})
            query = mock_http.call_args[1].get("query") or mock_http.call_args[0][2]
            assert query["startAt"] == "25"

    def test_start_at_default_is_zero(self):
        with _mock_cache_get(None), _mock_http(200, SEARCH_RESPONSE) as mock_http, _mock_cache_set():
            tools_read.search({"jql": "project = PROJ"})
            query = mock_http.call_args[1].get("query") or mock_http.call_args[0][2]
            assert query["startAt"] == "0"

    def test_start_at_in_response(self):
        with _mock_cache_get(None), _mock_http(200, SEARCH_RESPONSE), _mock_cache_set():
            result = tools_read.search({"jql": "project = PROJ", "start_at": 10})
            assert result["start_at"] == 10

    def test_start_at_negative_clamped(self):
        with _mock_cache_get(None), _mock_http(200, SEARCH_RESPONSE) as mock_http, _mock_cache_set():
            tools_read.search({"jql": "project = PROJ", "start_at": -5})
            query = mock_http.call_args[1].get("query") or mock_http.call_args[0][2]
            assert query["startAt"] == "0"

    def test_cache_key_varies_with_start_at(self):
        keys = set()
        base = {"jql": "project = PROJ"}
        for start in [0, 50, 100]:
            with _mock_cache_get(None), _mock_http(200, SEARCH_RESPONSE), _mock_cache_set() as mock_set:
                tools_read.search({**base, "start_at": start})
                cache_key = mock_set.call_args[0][0]
                keys.add(cache_key)
        assert len(keys) == 3, f"Expected 3 unique keys, got {len(keys)}"

    def test_truncated_warning_with_offset(self):
        truncated = {"total": 150, "issues": SEARCH_RESPONSE["issues"]}
        with _mock_cache_get(None), _mock_http(200, truncated), _mock_cache_set():
            result = tools_read.search({"jql": "project = PROJ", "start_at": 50})
            assert result["truncated"] is True
            assert "start_at=52" in result["warning"]


# --- jira_search (Cloud pagination) ---

CLOUD_PAGE_1 = {
    "issues": SAMPLE_ISSUES[:1],
    "isLast": False,
    "nextPageToken": "token-page-2",
}

CLOUD_PAGE_2 = {
    "issues": SAMPLE_ISSUES[1:],
    "isLast": True,
    "nextPageToken": "",
}

CLOUD_SINGLE_PAGE = {
    "issues": SAMPLE_ISSUES,
    "isLast": True,
}


class TestSearchCloud:
    def test_single_page(self):
        with (
            patch.object(handler, "is_cloud", True),
            _mock_cache_get(None),
            _mock_http(200, CLOUD_SINGLE_PAGE),
            _mock_cache_set(),
        ):
            result = tools_read.search({"jql": "project = PROJ"})
            assert result["count"] == 2
            assert result["total"] == 2

    def test_multi_page_pagination(self):
        responses = [
            (200, CLOUD_PAGE_1, {}),
            (200, CLOUD_PAGE_2, {}),
        ]
        with (
            patch.object(handler, "is_cloud", True),
            _mock_cache_get(None),
            patch.object(handler, "http", side_effect=responses),
            _mock_cache_set(),
        ):
            result = tools_read.search({"jql": "project = PROJ"})
            assert result["count"] == 2
            assert result["total"] == 2
            assert result["issues"][0]["key"] == "PROJ-1"
            assert result["issues"][1]["key"] == "PROJ-2"

    def test_multi_page_sends_next_page_token(self):
        responses = [
            (200, CLOUD_PAGE_1, {}),
            (200, CLOUD_PAGE_2, {}),
        ]
        with (
            patch.object(handler, "is_cloud", True),
            _mock_cache_get(None),
            patch.object(handler, "http", side_effect=responses) as mock_http,
            _mock_cache_set(),
        ):
            tools_read.search({"jql": "project = PROJ"})
            # First call: no nextPageToken
            first_query = mock_http.call_args_list[0][1].get("query") or mock_http.call_args_list[0][0][2]
            assert "nextPageToken" not in first_query
            # Second call: nextPageToken from first response
            second_query = mock_http.call_args_list[1][1].get("query") or mock_http.call_args_list[1][0][2]
            assert second_query["nextPageToken"] == "token-page-2"

    def test_start_at_slices_results(self):
        with (
            patch.object(handler, "is_cloud", True),
            _mock_cache_get(None),
            _mock_http(200, CLOUD_SINGLE_PAGE),
            _mock_cache_set(),
        ):
            result = tools_read.search({"jql": "project = PROJ", "start_at": 1})
            assert result["count"] == 1
            assert result["start_at"] == 1
            assert result["issues"][0]["key"] == "PROJ-2"

    def test_start_at_beyond_results_returns_empty(self):
        with (
            patch.object(handler, "is_cloud", True),
            _mock_cache_get(None),
            _mock_http(200, CLOUD_SINGLE_PAGE),
            _mock_cache_set(),
        ):
            result = tools_read.search({"jql": "project = PROJ", "start_at": 10})
            assert result["count"] == 0
            assert result["issues"] == []

    def test_max_results_caps_output(self):
        with (
            patch.object(handler, "is_cloud", True),
            _mock_cache_get(None),
            _mock_http(200, CLOUD_SINGLE_PAGE),
            _mock_cache_set(),
        ):
            result = tools_read.search({"jql": "project = PROJ", "max_results": 1})
            assert result["count"] == 1

    def test_empty_results(self):
        empty = {"issues": [], "isLast": True}
        with patch.object(handler, "is_cloud", True), _mock_cache_get(None), _mock_http(200, empty), _mock_cache_set():
            result = tools_read.search({"jql": "project = PROJ"})
            assert result["count"] == 0
            assert result["total"] == 0
            assert result["issues"] == []

    def test_mid_pagination_error(self):
        responses = [
            (200, CLOUD_PAGE_1, {}),
            (500, {"error": "Server Error"}, {}),
        ]
        with (
            patch.object(handler, "is_cloud", True),
            _mock_cache_get(None),
            patch.object(handler, "http", side_effect=responses),
        ):
            result = tools_read.search({"jql": "project = PROJ"})
            assert result["error"] == "HTTP 500"

    def test_missing_is_last_field_terminates(self):
        no_is_last = {"issues": SAMPLE_ISSUES}
        with (
            patch.object(handler, "is_cloud", True),
            _mock_cache_get(None),
            _mock_http(200, no_is_last),
            _mock_cache_set(),
        ):
            result = tools_read.search({"jql": "project = PROJ"})
            assert result["count"] == 2

    def test_cache_key_differs_from_server(self):
        keys = set()
        for cloud in [True, False]:
            with (
                patch.object(handler, "is_cloud", cloud),
                _mock_cache_get(None),
                _mock_http(200, SEARCH_RESPONSE),
                _mock_cache_set() as mock_set,
            ):
                tools_read.search({"jql": "project = PROJ"})
                cache_key = mock_set.call_args[0][0]
                keys.add(cache_key)
        assert len(keys) == 2

    def test_no_start_at_in_cloud_query(self):
        """Cloud search should not send startAt query parameter."""
        with (
            patch.object(handler, "is_cloud", True),
            _mock_cache_get(None),
            _mock_http(200, CLOUD_SINGLE_PAGE) as mock_http,
            _mock_cache_set(),
        ):
            tools_read.search({"jql": "project = PROJ"})
            query = mock_http.call_args[1].get("query") or mock_http.call_args[0][2]
            assert "startAt" not in query

    def test_error_not_cached(self):
        with (
            patch.object(handler, "is_cloud", True),
            _mock_cache_get(None),
            _mock_http(500, {"error": "fail"}),
            _mock_cache_set() as mock_set,
        ):
            tools_read.search({"jql": "bad"})
            mock_set.assert_not_called()


# --- jira_get_issues ---


class TestGetIssues:
    def test_single_key_brief(self):
        response = {"issues": [SAMPLE_ISSUES[0]]}
        with _mock_cache_get(None), _mock_http(200, response), _mock_cache_set():
            result = tools_read.get_issues({"issue_keys": "PROJ-1"})
            assert result["count"] == 1
            assert result["issues"][0]["key"] == "PROJ-1"
            assert result["issues"][0]["status"] == "Open"

    def test_multiple_keys(self):
        with _mock_cache_get(None), _mock_http(200, SEARCH_RESPONSE), _mock_cache_set():
            result = tools_read.get_issues({"issue_keys": "PROJ-1,PROJ-2"})
            assert result["count"] == 2

    def test_missing_key(self):
        response = {"issues": [SAMPLE_ISSUES[0]]}
        with _mock_cache_get(None), _mock_http(200, response), _mock_cache_set():
            result = tools_read.get_issues({"issue_keys": "PROJ-1,PROJ-999"})
            assert result["count"] == 2
            assert result["issues"][0]["key"] == "PROJ-1"
            assert result["issues"][1]["error"] == "Issue not found or not accessible"

    def test_full_mode(self):
        response = {"issues": [SAMPLE_ISSUES[0]]}
        with _mock_cache_get(None), _mock_http(200, response), _mock_cache_set():
            result = tools_read.get_issues({"issue_keys": "PROJ-1", "brief": False})
            # Full mode returns clean extracted fields
            assert result["issues"][0]["key"] == "PROJ-1"
            assert result["issues"][0]["status"] == "Open"
            assert "fields" not in result["issues"][0]

    def test_empty_keys(self):
        result = tools_read.get_issues({"issue_keys": ""})
        assert result == {"issues": [], "count": 0}

    def test_invalid_key_raises(self):
        with pytest.raises(ValueError, match="Invalid issue key"):
            tools_read.get_issues({"issue_keys": "bad-key"})

    def test_http_error(self):
        with _mock_cache_get(None), _mock_http(403, {"error": "Forbidden"}):
            result = tools_read.get_issues({"issue_keys": "PROJ-1"})
            assert result["error"] == "HTTP 403"

    def test_cache_hit(self):
        cached = {"issues": [{"key": "PROJ-1", "summary": "cached"}], "count": 1}
        with _mock_cache_get(cached):
            result = tools_read.get_issues({"issue_keys": "PROJ-1"})
            assert result == cached

    def test_cache_key_varies_with_params(self):
        """Different fields/brief must produce different cache keys."""
        keys = set()
        base = {"issue_keys": "PROJ-1"}
        combos = [
            base,
            {**base, "fields": "summary"},
            {**base, "brief": False},
        ]
        response = {"issues": [SAMPLE_ISSUES[0]]}
        for params in combos:
            with _mock_cache_get(None), _mock_http(200, response), _mock_cache_set() as mock_set:
                tools_read.get_issues(params)
                cache_key = mock_set.call_args[0][0]
                keys.add(cache_key)
        assert len(keys) == len(combos)

    def test_cache_key_order_independent(self):
        """PROJ-1,PROJ-2 and PROJ-2,PROJ-1 should produce same cache key."""
        keys_seen = set()
        response = {"issues": SAMPLE_ISSUES}
        for key_order in ["PROJ-1,PROJ-2", "PROJ-2,PROJ-1"]:
            with _mock_cache_get(None), _mock_http(200, response), _mock_cache_set() as mock_set:
                tools_read.get_issues({"issue_keys": key_order})
                cache_key = mock_set.call_args[0][0]
                keys_seen.add(cache_key)
        assert len(keys_seen) == 1


# --- jira_get_user ---


class TestGetUser:
    def test_alias_me(self):
        with _mock_http(200, MYSELF_RESPONSE):
            result = tools_read.get_user({"username": "me"})
            assert result["accountId"] == "abc123"
            assert result["displayName"] == "Jane Doe"

    def test_alias_myself(self):
        with _mock_http(200, MYSELF_RESPONSE):
            result = tools_read.get_user({"username": "myself"})
            assert result["displayName"] == "Jane Doe"

    def test_cloud_search(self):
        users = [{"accountId": "x", "displayName": "Bob", "name": "bob"}]
        with patch.object(handler, "is_cloud", True), _mock_http(200, users) as mock_http:
            result = tools_read.get_user({"username": "bob"})
            assert result["accountId"] == "x"
            query = mock_http.call_args[1].get("query") or mock_http.call_args[0][2]
            assert query == {"query": "bob"}

    def test_server_search(self):
        users = [{"key": "bob", "displayName": "Bob", "name": "bob"}]
        with patch.object(handler, "is_cloud", False), _mock_http(200, users) as mock_http:
            result = tools_read.get_user({"username": "bob"})
            assert result["accountId"] == "bob"
            query = mock_http.call_args[1].get("query") or mock_http.call_args[0][2]
            assert query == {"username": "bob"}

    def test_user_not_found(self):
        with patch.object(handler, "is_cloud", False), _mock_http(200, []):
            result = tools_read.get_user({"username": "nobody"})
            assert "error" in result
            assert "not found" in result["error"]

    def test_empty_username(self):
        result = tools_read.get_user({"username": ""})
        assert result == {"error": "username is required"}


# --- jira_get_transitions ---


class TestGetTransitions:
    def test_success(self):
        transitions = {
            "transitions": [
                {"id": "1", "name": "Start Progress"},
                {"id": "2", "name": "Close"},
            ]
        }
        with _mock_http(200, transitions):
            result = tools_read.get_transitions({"issue_key": "PROJ-1"})
            assert result["transitions"][0]["name"] == "Start Progress"

    def test_invalid_key(self):
        with pytest.raises(ValueError, match="Invalid issue key"):
            tools_read.get_transitions({"issue_key": "bad"})

    def test_http_error(self):
        with _mock_http(404, {"error": "Not Found"}):
            result = tools_read.get_transitions({"issue_key": "PROJ-999"})
            assert result["error"] == "HTTP 404"


# --- jira_get_resolutions ---


class TestGetResolutions:
    def test_success(self):
        raw = [
            {"id": "1", "name": "Done", "description": "Work is complete"},
            {"id": "2", "name": "Won't Do", "description": "Not going to do this"},
        ]
        with _mock_cache_get(None), _mock_http(200, raw), _mock_cache_set() as mock_set:
            result = tools_read.get_resolutions({})
            assert len(result["resolutions"]) == 2
            assert result["resolutions"][0] == {"id": "1", "name": "Done"}
            # Descriptions are stripped
            assert "description" not in result["resolutions"][0]
            # Cached with 1 hour TTL
            mock_set.assert_called_once()
            assert mock_set.call_args[1]["ttl"] == 3600

    def test_cache_hit(self):
        cached = {"resolutions": [{"id": "1", "name": "Done"}]}
        with _mock_cache_get(cached):
            result = tools_read.get_resolutions({})
            assert result == cached

    def test_http_error(self):
        with _mock_cache_get(None), _mock_http(500, {"error": "Server Error"}):
            result = tools_read.get_resolutions({})
            assert result["error"] == "HTTP 500"


# --- jira_get_link_types ---


class TestGetLinkTypes:
    def test_success(self):
        link_types = {
            "issueLinkTypes": [
                {"id": "1", "name": "Blocks", "inward": "is blocked by", "outward": "blocks"},
                {"id": "2", "name": "Clones", "inward": "is cloned by", "outward": "clones"},
            ]
        }
        with _mock_cache_get(None), _mock_http(200, link_types), _mock_cache_set() as mock_set:
            result = tools_read.get_link_types({})
            assert result["issueLinkTypes"][0]["name"] == "Blocks"
            # Cached with 1 hour TTL
            mock_set.assert_called_once()
            assert mock_set.call_args[1]["ttl"] == 3600

    def test_cache_hit(self):
        cached = {"issueLinkTypes": [{"id": "1", "name": "Blocks"}]}
        with _mock_cache_get(cached):
            result = tools_read.get_link_types({})
            assert result == cached

    def test_http_error(self):
        with _mock_cache_get(None), _mock_http(403, {"error": "Forbidden"}):
            result = tools_read.get_link_types({})
            assert result["error"] == "HTTP 403"


# --- jira_get_issue_types ---


class TestGetIssueTypes:
    def test_project_specific(self):
        project_body = {
            "key": "PROJ",
            "issueTypes": [
                {"id": "1", "name": "Bug", "subtask": False},
                {"id": "2", "name": "Story", "subtask": False},
                {"id": "3", "name": "Sub-task", "subtask": True},
            ],
        }
        with _mock_cache_get(None), _mock_http(200, project_body) as mock_http, _mock_cache_set() as mock_set:
            result = tools_read.get_issue_types({"project_key": "PROJ"})
            assert result["project_key"] == "PROJ"
            assert len(result["issue_types"]) == 3
            assert result["issue_types"][0] == {"id": "1", "name": "Bug", "subtask": False}
            assert result["issue_types"][2]["subtask"] is True
            mock_http.assert_called_once_with("GET", "/rest/api/2/project/PROJ")
            mock_set.assert_called_once()
            assert mock_set.call_args[0][0] == "issue_types:project:PROJ"
            assert mock_set.call_args[1]["ttl"] == 3600

    def test_all_instance_types(self):
        all_types = [
            {"id": "10", "name": "Epic", "subtask": False},
            {"id": "11", "name": "Task", "subtask": False},
        ]
        with _mock_cache_get(None), _mock_http(200, all_types) as mock_http, _mock_cache_set() as mock_set:
            result = tools_read.get_issue_types({})
            assert "project_key" not in result
            assert len(result["issue_types"]) == 2
            mock_http.assert_called_once_with("GET", "/rest/api/2/issuetype")
            assert mock_set.call_args[0][0] == "issue_types:global"

    def test_cache_hit(self):
        cached = {"issue_types": [{"id": "1", "name": "Bug", "subtask": False}], "project_key": "PROJ"}
        with _mock_cache_get(cached):
            result = tools_read.get_issue_types({"project_key": "PROJ"})
            assert result == cached

    def test_cache_key_varies_by_project(self):
        keys = set()
        body = {"key": "X", "issueTypes": [{"id": "1", "name": "Bug"}]}
        for proj in ["PROJ", "OTHER"]:
            with _mock_cache_get(None), _mock_http(200, body), _mock_cache_set() as mock_set:
                tools_read.get_issue_types({"project_key": proj})
                keys.add(mock_set.call_args[0][0])
        assert len(keys) == 2

    def test_http_error(self):
        with _mock_cache_get(None), _mock_http(404, {"error": "Project not found"}):
            result = tools_read.get_issue_types({"project_key": "NOPE"})
            assert result["error"] == "HTTP 404"

    def test_invalid_project_key_raises(self):
        with pytest.raises(ValueError, match="Invalid project key"):
            tools_read.get_issue_types({"project_key": "../evil"})

    def test_lowercase_key_normalized(self):
        body = {"key": "PROJ", "issueTypes": [{"id": "1", "name": "Bug"}]}
        with _mock_cache_get(None), _mock_http(200, body) as mock_http, _mock_cache_set():
            result = tools_read.get_issue_types({"project_key": "proj"})
            assert result["project_key"] == "PROJ"
            mock_http.assert_called_once_with("GET", "/rest/api/2/project/PROJ")


# --- jira_flush_cache ---


class TestFlushCache:
    def test_flush_calls_cache_flush(self):
        with patch.object(handler, "cache_flush") as mock_flush:
            result = tools_read.flush_cache({})
            mock_flush.assert_called_once()
            assert result["success"] is True
