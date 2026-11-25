"""Unit tests for tools_read.py — mock HTTP protocol calls."""

from unittest.mock import patch

import handler
import tools_read


def _mock_http(status, body):
    return patch.object(handler, "http", return_value=(status, body, {}))


STATUS_RESPONSE = {
    "osidb_service": {},
    "osidb_data": {"flaw_count": 85000},
    "dt": "2024-01-01T00:00:00Z",
    "revision": "abc123",
    "version": "5.23.0",
    "env": "prod",
}

FLAW_RESPONSE = {
    "uuid": "11111111-1111-1111-1111-111111111111",
    "cve_id": "CVE-2024-1234",
    "title": "Test vulnerability",
    "impact": "IMPORTANT",
    "workflow_state": "TRIAGE",
    "owner": "analyst@example.com",
    "embargoed": False,
    "classification": {"state": "TRIAGE", "workflow": "DEFAULT"},
    "labels": [],
    "affects": [],
    "created_dt": "2024-01-01T00:00:00Z",
    "updated_dt": "2024-06-01T00:00:00Z",
}

FLAW_LIST_RESPONSE = {
    "count": 1,
    "results": [FLAW_RESPONSE],
    "next": None,
    "previous": None,
}


class TestStatus:
    def test_returns_status(self):
        with _mock_http(200, STATUS_RESPONSE):
            result = tools_read.status({})
            assert result["version"] == "5.23.0"
            assert result["osidb_data"]["flaw_count"] == 85000

    def test_http_error(self):
        with _mock_http(503, {"detail": "Service unavailable"}):
            result = tools_read.status({})
            assert "error" in result


class TestFlawGet:
    def test_get_by_cve_id(self):
        with _mock_http(200, FLAW_RESPONSE) as mock:
            result = tools_read.flaw_get({"flaw_id": "CVE-2024-1234"})
            assert result["cve_id"] == "CVE-2024-1234"
            assert result["impact"] == "IMPORTANT"
            call_args = mock.call_args
            assert call_args[0][1] == "/osidb/api/v1/flaws/CVE-2024-1234"

    def test_get_with_include_fields(self):
        with _mock_http(200, {"cve_id": "CVE-2024-1234", "title": "Test"}) as mock:
            result = tools_read.flaw_get(
                {
                    "flaw_id": "CVE-2024-1234",
                    "include_fields": ["cve_id", "title"],
                }
            )
            assert result["cve_id"] == "CVE-2024-1234"
            query = mock.call_args[1].get("query") or mock.call_args[0][2]
            assert "include_fields" in query

    def test_not_found(self):
        with _mock_http(404, {"detail": "Not found"}):
            result = tools_read.flaw_get({"flaw_id": "CVE-9999-0000"})
            assert result["error"] == "HTTP 404"


class TestFlawList:
    def test_basic_list(self):
        with _mock_http(200, FLAW_LIST_RESPONSE):
            result = tools_read.flaw_list({"limit": 10})
            assert result["count"] == 1
            assert len(result["results"]) == 1

    def test_with_filters(self):
        with _mock_http(200, FLAW_LIST_RESPONSE) as mock:
            tools_read.flaw_list(
                {
                    "impact": "CRITICAL",
                    "owner": "analyst@example.com",
                    "workflow_state": "NEW",
                    "limit": 50,
                }
            )
            query = mock.call_args[1].get("query") or mock.call_args[0][2]
            assert query["impact"] == "CRITICAL"
            assert query["owner"] == "analyst@example.com"
            assert query["workflow_state"] == "NEW"

    def test_with_djangoql_query(self):
        with _mock_http(200, FLAW_LIST_RESPONSE) as mock:
            tools_read.flaw_list(
                {
                    "query": 'impact = "CRITICAL" AND owner = "test"',
                }
            )
            query = mock.call_args[1].get("query") or mock.call_args[0][2]
            assert "query" in query


class TestFlawSearch:
    def test_search(self):
        with _mock_http(200, FLAW_LIST_RESPONSE) as mock:
            result = tools_read.flaw_search({"query": "buffer overflow", "limit": 10})
            assert result["count"] == 1
            query = mock.call_args[1].get("query") or mock.call_args[0][2]
            assert query["search"] == "buffer overflow"


class TestMyFlaws:
    def test_owner_query(self):
        with _mock_http(200, FLAW_LIST_RESPONSE):
            result = tools_read.my_flaws({"email": "analyst@example.com", "limit": 10})
            assert result["count"] == 1

    def test_analyse_mode(self):
        flaw_with_label = {
            **FLAW_RESPONSE,
            "owner": "analyst@example.com",
            "labels": [{"name": "hybrid_bu", "state": "NEW", "contributor": "analyst@example.com"}],
        }
        list_resp = {"count": 1, "results": [flaw_with_label], "next": None, "previous": None}
        with _mock_http(200, list_resp):
            result = tools_read.my_flaws(
                {
                    "email": "analyst@example.com",
                    "analyse": True,
                }
            )
            assert "summary" in result
            assert "needs_attention" in result
            assert result["summary"]["total"] == 1


class TestFlawSubresources:
    def test_flaw_comments(self):
        response = {"count": 1, "results": [{"uuid": "c1", "text": "A comment"}]}
        with _mock_http(200, response):
            result = tools_read.flaw_comments({"flaw_id": "CVE-2024-1234"})
            assert result["count"] == 1

    def test_flaw_references(self):
        response = {"count": 0, "results": []}
        with _mock_http(200, response):
            result = tools_read.flaw_references({"flaw_id": "CVE-2024-1234"})
            assert result["count"] == 0

    def test_flaw_labels(self):
        response = {"count": 1, "results": [{"name": "hybrid_bu", "state": "NEW"}]}
        with _mock_http(200, response):
            result = tools_read.flaw_labels({"flaw_id": "CVE-2024-1234"})
            assert result["count"] == 1


class TestAffects:
    def test_affect_get(self):
        affect = {"uuid": "a1", "ps_module": "rhel-9", "ps_component": "kernel", "affectedness": "AFFECTED"}
        with _mock_http(200, affect):
            result = tools_read.affect_get({"affect_id": "a1"})
            assert result["ps_module"] == "rhel-9"

    def test_affect_list(self):
        response = {"count": 1, "results": [{"uuid": "a1", "ps_module": "rhel-9"}]}
        with _mock_http(200, response) as mock:
            result = tools_read.affect_list({"flaw_cve_id": "CVE-2024-1234", "limit": 10})
            assert result["count"] == 1
            query = mock.call_args[1].get("query") or mock.call_args[0][2]
            assert query["flaw__cve_id"] == "CVE-2024-1234"


class TestTrackers:
    def test_tracker_list(self):
        response = {"count": 1, "results": [{"uuid": "t1", "type": "BUGZILLA"}]}
        with _mock_http(200, response):
            result = tools_read.tracker_list({"flaw_cve_id": "CVE-2024-1234"})
            assert result["count"] == 1

    def test_tracker_suggest(self):
        resp = {"streams_components": [{"ps_update_stream": "rhel-9.0"}]}
        with patch.object(handler, "http", return_value=(200, resp, {})) as mock:
            result = tools_read.tracker_suggest({"flaw_uuids": ["uuid1", "uuid2"]})
            assert "streams_components" in result
            assert mock.call_args[0][0] == "POST"
            assert mock.call_args[1]["body"] == {"flaw_uuids": ["uuid1", "uuid2"]}


class TestLabels:
    def test_label_list(self):
        response = {"count": 2, "results": [{"name": "hybrid_bu"}, {"name": "core_bu"}]}
        with _mock_http(200, response):
            result = tools_read.label_list({})
            assert result["count"] == 2

    def test_label_get(self):
        response = {"uuid": "l1", "name": "hybrid_bu", "description": "Hybrid Platforms BU"}
        with _mock_http(200, response):
            result = tools_read.label_get({"label_id": "l1"})
            assert result["name"] == "hybrid_bu"


class TestPromotionStatus:
    def test_workflows_404(self):
        with _mock_http(404, "Not found"):
            result = tools_read.flaw_promotion_status({"flaw_id": "11111111-1111-1111-1111-111111111111"})
            assert "error" in result
            assert "404" in result["error"]

    def test_with_cve_resolves_uuid(self):
        flaw_resp = {"uuid": "11111111-1111-1111-1111-111111111111"}
        workflows_resp = {
            "classification": {"state": "TRIAGE", "workflow": "DEFAULT"},
            "workflows": [
                {
                    "name": "DEFAULT",
                    "states": [
                        {"name": "NEW", "requirements": []},
                        {"name": "TRIAGE", "requirements": []},
                        {"name": "DONE", "requirements": [{"name": "has owner", "accepts": True}]},
                    ],
                }
            ],
        }
        responses = [
            (200, flaw_resp, {}),
            (200, workflows_resp, {}),
        ]
        with patch.object(handler, "http", side_effect=responses) as mock:
            result = tools_read.flaw_promotion_status({"flaw_id": "CVE-2024-1234"})
            assert mock.call_count == 2
            assert result["current_state"] == "TRIAGE"
            assert result["next_state"] == "DONE"
