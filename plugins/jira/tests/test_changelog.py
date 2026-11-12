"""Tests for the Jira issue changelog reader."""

from unittest.mock import patch

import handler
import pytest
import tools_read

HISTORY = {
    "id": "10001",
    "created": "2026-11-01T12:00:00.000+0000",
    "author": {"displayName": "Alice", "emailAddress": "alice@example.com"},
    "items": [
        {
            "field": "Component",
            "fieldId": "components",
            "from": "123",
            "fromString": "Security",
            "to": "456",
            "toString": "Other",
        }
    ],
}


def test_cloud_changelog_page_and_path():
    body = {"startAt": 0, "total": 2, "isLast": False, "values": [HISTORY]}
    response = {"status": 200, "body": body, "headers": {}}
    with (
        patch.object(handler, "is_cloud", True),
        patch.object(handler, "_send") as mock_send,
        patch.object(handler, "_recv", return_value=response),
    ):
        result = tools_read.get_changelog({"issue_key": "proj-1", "max_results": 1})

    request = mock_send.call_args[0][0]
    assert request["path"] == "/rest/api/3/issue/PROJ-1/changelog"
    assert request["query"] == {"startAt": "0", "maxResults": "1"}
    assert result["issue_key"] == "PROJ-1"
    assert result["has_more"] is True
    assert result["next_start_at"] == 1
    assert result["histories"][0]["author"] == "Alice"
    assert result["histories"][0]["items"][0]["fromString"] == "Security"
    assert "emailAddress" not in result["histories"][0]


def test_server_changelog_endpoint():
    body = {"startAt": 2, "total": 3, "values": [HISTORY]}
    with (
        patch.object(handler, "is_cloud", False),
        patch.object(handler, "http", return_value=(200, body, {})) as mock_http,
    ):
        result = tools_read.get_changelog({"issue_key": "PROJ-1", "start_at": 2})

    assert mock_http.call_args.args[1] == "/rest/api/2/issue/PROJ-1/changelog"
    assert result["has_more"] is False
    assert result["count"] == 1


def test_server_expansion_warns_when_truncated():
    issue = {"changelog": {"startAt": 0, "total": 10, "histories": [HISTORY]}}
    with (
        patch.object(handler, "is_cloud", False),
        patch.object(handler, "http", side_effect=[(404, {}, {}), (200, issue, {})]) as mock_http,
    ):
        result = tools_read.get_changelog({"issue_key": "PROJ-1"})

    assert mock_http.call_count == 2
    assert mock_http.call_args.args[1] == "/rest/api/2/issue/PROJ-1"
    assert mock_http.call_args.kwargs["query"] == {"expand": "changelog", "fields": "none"}
    assert result["complete"] is False
    assert "initial field values" in result["warning"]


def test_server_expansion_complete():
    issue = {"changelog": {"startAt": 0, "total": 1, "histories": [HISTORY]}}
    with (
        patch.object(handler, "is_cloud", False),
        patch.object(handler, "http", side_effect=[(404, {}, {}), (200, issue, {})]),
    ):
        result = tools_read.get_changelog({"issue_key": "PROJ-1"})

    assert result["complete"] is True
    assert "warning" not in result


def test_http_error():
    with (
        patch.object(handler, "is_cloud", True),
        patch.object(handler, "http", return_value=(403, {"errorMessages": ["Forbidden"]}, {})),
    ):
        result = tools_read.get_changelog({"issue_key": "PROJ-1"})
    assert result == {"error": "HTTP 403", "detail": ["Forbidden"]}


def test_invalid_issue_key():
    with pytest.raises(ValueError):
        tools_read.get_changelog({"issue_key": "PROJ-1/../../admin"})


def test_page_bounds():
    body = {"startAt": 0, "total": 0, "values": []}
    with (
        patch.object(handler, "is_cloud", True),
        patch.object(handler, "http", return_value=(200, body, {})) as mock_http,
    ):
        tools_read.get_changelog({"issue_key": "PROJ-1", "start_at": -4, "max_results": 999})
    assert mock_http.call_args.kwargs["query"] == {"startAt": "0", "maxResults": "100"}
