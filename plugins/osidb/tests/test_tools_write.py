"""Unit tests for tools_write.py — mock HTTP protocol calls."""

from unittest.mock import patch

import handler
import tools_write


def _mock_http(status, body):
    return patch.object(handler, "http", return_value=(status, body, {}))


def _mock_http_sequence(responses):
    """Mock handler.http to return different responses on successive calls."""
    return patch.object(handler, "http", side_effect=responses)


FLAW_RESPONSE = {
    "uuid": "11111111-1111-1111-1111-111111111111",
    "cve_id": "CVE-2024-1234",
    "title": "Test vulnerability",
    "impact": "IMPORTANT",
    "embargoed": False,
    "updated_dt": "2024-06-01T00:00:00Z",
}


class TestFlawCreate:
    def test_dry_run_default(self):
        result = tools_write.flaw_create(
            {
                "title": "New flaw",
                "comment_zero": "Description",
            }
        )
        assert result["dry_run"] is True
        assert result["action"] == "osidb_flaw_create"
        assert result["data"]["title"] == "New flaw"

    def test_execute(self):
        created = {**FLAW_RESPONSE, "title": "New flaw"}
        with _mock_http(201, created):
            result = tools_write.flaw_create(
                {
                    "title": "New flaw",
                    "comment_zero": "Description",
                    "dry_run": False,
                }
            )
            assert result["cve_id"] == "CVE-2024-1234"


class TestFlawUpdate:
    def test_dry_run(self):
        result = tools_write.flaw_update(
            {
                "flaw_id": "CVE-2024-1234",
                "fields": {"impact": "CRITICAL"},
            }
        )
        assert result["dry_run"] is True

    def test_execute_fetches_then_puts(self):
        updated = {**FLAW_RESPONSE, "impact": "CRITICAL"}
        responses = [
            (200, FLAW_RESPONSE, {}),
            (200, updated, {}),
        ]
        with _mock_http_sequence(responses) as mock:
            result = tools_write.flaw_update(
                {
                    "flaw_id": "CVE-2024-1234",
                    "fields": {"impact": "CRITICAL"},
                    "dry_run": False,
                }
            )
            assert mock.call_count == 2
            assert mock.call_args_list[0][0][0] == "GET"
            assert mock.call_args_list[1][0][0] == "PUT"
            assert result["impact"] == "CRITICAL"


class TestFlawCommentAdd:
    def test_dry_run(self):
        responses = [(200, {"embargoed": False}, {})]
        with _mock_http_sequence(responses):
            result = tools_write.flaw_comment_add(
                {
                    "flaw_id": "CVE-2024-1234",
                    "text": "Test comment",
                }
            )
            assert result["dry_run"] is True

    def test_execute(self):
        comment = {"uuid": "c1", "text": "Test comment"}
        responses = [
            (200, {"embargoed": False}, {}),
            (201, comment, {}),
        ]
        with _mock_http_sequence(responses):
            result = tools_write.flaw_comment_add(
                {
                    "flaw_id": "CVE-2024-1234",
                    "text": "Test comment",
                    "dry_run": False,
                }
            )
            assert result["uuid"] == "c1"


class TestFlawReferenceAdd:
    def test_dry_run(self):
        responses = [(200, {"embargoed": False}, {})]
        with _mock_http_sequence(responses):
            result = tools_write.flaw_reference_add(
                {
                    "flaw_id": "CVE-2024-1234",
                    "url": "https://example.com/advisory",
                }
            )
            assert result["dry_run"] is True
            assert result["data"]["type"] == "EXTERNAL"


class TestFlawLabelAdd:
    def test_dry_run(self):
        responses = [(200, {"embargoed": False}, {})]
        with _mock_http_sequence(responses):
            result = tools_write.flaw_label_add(
                {
                    "flaw_id": "CVE-2024-1234",
                    "label_name": "hybrid_bu",
                    "state": "NEW",
                }
            )
            assert result["dry_run"] is True
            assert result["data"]["name"] == "hybrid_bu"


class TestAffectCreate:
    def test_dry_run(self):
        result = tools_write.affect_create(
            {
                "flaw": "CVE-2024-1234",
                "ps_module": "rhel-9",
                "ps_component": "kernel",
            }
        )
        assert result["dry_run"] is True
        assert result["data"]["ps_module"] == "rhel-9"

    def test_execute(self):
        created = {"uuid": "a1", "ps_module": "rhel-9", "ps_component": "kernel"}
        with _mock_http(201, created):
            result = tools_write.affect_create(
                {
                    "flaw": "CVE-2024-1234",
                    "ps_module": "rhel-9",
                    "ps_component": "kernel",
                    "dry_run": False,
                }
            )
            assert result["uuid"] == "a1"


class TestAffectUpdate:
    def test_execute_fetches_then_puts(self):
        current = {"uuid": "a1", "affectedness": "NEW", "updated_dt": "2024-01-01T00:00:00Z"}
        updated = {**current, "affectedness": "AFFECTED"}
        responses = [
            (200, current, {}),
            (200, updated, {}),
        ]
        with _mock_http_sequence(responses):
            result = tools_write.affect_update(
                {
                    "affect_id": "a1",
                    "fields": {"affectedness": "AFFECTED"},
                    "dry_run": False,
                }
            )
            assert result["affectedness"] == "AFFECTED"


class TestAffectDelete:
    def test_dry_run(self):
        result = tools_write.affect_delete({"affect_id": "a1"})
        assert result["dry_run"] is True

    def test_execute(self):
        with _mock_http(204, ""):
            result = tools_write.affect_delete({"affect_id": "a1", "dry_run": False})
            assert result["deleted"] is True


class TestAffectBulk:
    def test_bulk_create_dry_run(self):
        affects = [
            {"flaw": "CVE-2024-1234", "ps_module": "rhel-9", "ps_component": "kernel"},
            {"flaw": "CVE-2024-1234", "ps_module": "rhel-8", "ps_component": "kernel"},
        ]
        result = tools_write.affect_bulk_create({"affects": affects})
        assert result["dry_run"] is True
        assert result["count"] == 2

    def test_bulk_delete_dry_run(self):
        result = tools_write.affect_bulk_delete({"affect_ids": ["a1", "a2"]})
        assert result["dry_run"] is True
        assert result["count"] == 2


class TestTrackerCreate:
    def test_dry_run(self):
        result = tools_write.tracker_create({"flaw_uuids": ["uuid1", "uuid2"]})
        assert result["dry_run"] is True
        assert result["flaw_uuids"] == ["uuid1", "uuid2"]


class TestTrackerUpdate:
    def test_dry_run(self):
        result = tools_write.tracker_update(
            {
                "tracker_id": "t1",
                "fields": {"status": "CLOSED"},
            }
        )
        assert result["dry_run"] is True
