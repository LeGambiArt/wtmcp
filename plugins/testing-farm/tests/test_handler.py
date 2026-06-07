"""Unit tests for Testing Farm plugin handler."""

import base64
from unittest.mock import patch

import handler


def _mock_http(status, body):
    """Return a mock for handler.http that returns (status, body, headers)."""
    return patch.object(handler, "http", return_value=(status, body, {}))


def _mock_cache_get(value=None):
    """Return a mock for handler.cache_get. None means cache miss."""
    return patch.object(handler, "cache_get", return_value=value)


def _mock_cache_set():
    """Return a mock for handler.cache_set."""
    return patch.object(handler, "cache_set")


def _mock_cache_del():
    """Return a mock for handler.cache_del."""
    return patch.object(handler, "cache_del")


ABOUT_RESPONSE = {
    "version": "0.1",
    "name": "Testing Farm",
}

WHOAMI_RESPONSE = {
    "id": "user-123",
    "name": "testuser",
}

REQ_ID = "30a35fea-17f8-4399-9e67-fd3b074ed1ac"
REQ_ID_2 = "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"

SAMPLE_REQUEST = {
    "id": REQ_ID,
    "state": "complete",
    "result": {"overall": "passed"},
    "created": "2025-01-15T10:00:00Z",
    "updated": "2025-01-15T10:30:00Z",
    "test": {"fmf": {"url": "https://example.com/tests", "ref": "main", "name": "/plan/test1"}},
    "environments_requested": [
        {
            "os": {"compose": "Fedora-Rawhide"},
            "arch": "x86_64",
        }
    ],
    "run": {"log": "https://example.com/log", "stages": [], "artifacts": f"https://artifacts.example.com/{REQ_ID}"},
}

RESERVE_REQUEST = {
    "id": REQ_ID_2,
    "state": "running",
    "result": {"overall": "unknown"},
    "created": "2025-01-15T11:00:00Z",
    "test": {"fmf": {"url": "https://gitlab.com/testing-farm/tests", "ref": "main", "name": "/testing-farm/reserve"}},
    "environments_requested": [
        {
            "os": {"compose": "Fedora-41"},
            "arch": "x86_64",
            "variables": {"TF_RESERVATION_DURATION": "60"},
        }
    ],
}


# --- _validate_request_id ---


class TestValidateRequestId:
    def test_valid_uuid(self):
        handler._validate_request_id("30a35fea-17f8-4399-9e67-fd3b074ed1ac")

    def test_invalid_format(self):
        try:
            handler._validate_request_id("not-a-uuid")
            assert False, "Should have raised"
        except Exception as e:
            assert "Invalid request ID" in str(e)

    def test_path_traversal(self):
        try:
            handler._validate_request_id("../../admin/secrets")
            assert False, "Should have raised"
        except Exception as e:
            assert "Invalid request ID" in str(e)


# --- _fetch_request ---


class TestFetchRequest:
    def test_cache_miss_fetches_and_caches_terminal(self):
        with _mock_cache_get(None), _mock_http(200, SAMPLE_REQUEST), _mock_cache_set() as mock_set:
            result = handler._fetch_request("req-abc-123")
            assert result == SAMPLE_REQUEST
            mock_set.assert_called_once()
            assert mock_set.call_args[0][0] == "tf:raw:req-abc-123"
            assert mock_set.call_args[1]["ttl"] == 3600

    def test_cache_miss_running_not_cached(self):
        running_req = {**SAMPLE_REQUEST, "state": "running"}
        with _mock_cache_get(None), _mock_http(200, running_req), _mock_cache_set() as mock_set:
            result = handler._fetch_request("req-abc-123")
            assert result["state"] == "running"
            mock_set.assert_not_called()

    def test_cache_hit(self):
        with _mock_cache_get(SAMPLE_REQUEST):
            result = handler._fetch_request("req-abc-123")
            assert result == SAMPLE_REQUEST

    def test_http_error(self):
        with _mock_cache_get(None), _mock_http(500, {"error": "fail"}):
            try:
                handler._fetch_request("bad-id")
                assert False, "Should have raised"
            except Exception as e:
                assert "500" in str(e)

    def test_caches_error_state(self):
        error_req = {**SAMPLE_REQUEST, "state": "error"}
        with _mock_cache_get(None), _mock_http(200, error_req), _mock_cache_set() as mock_set:
            handler._fetch_request("req-err")
            mock_set.assert_called_once()

    def test_caches_canceled_state(self):
        canceled_req = {**SAMPLE_REQUEST, "state": "canceled"}
        with _mock_cache_get(None), _mock_http(200, canceled_req), _mock_cache_set() as mock_set:
            handler._fetch_request("req-cancel")
            mock_set.assert_called_once()


# --- testing_farm_about ---


class TestAbout:
    def test_cache_miss_fetches_and_caches(self):
        with _mock_cache_get(None), _mock_http(200, ABOUT_RESPONSE), _mock_cache_set() as mock_set:
            result = handler.testing_farm_about({})
            assert result["version"] == "0.1"
            mock_set.assert_called_once()

    def test_cache_hit(self):
        with _mock_cache_get(ABOUT_RESPONSE):
            result = handler.testing_farm_about({})
            assert result == ABOUT_RESPONSE

    def test_http_error(self):
        with _mock_cache_get(None), _mock_http(500, {"error": "fail"}):
            try:
                handler.testing_farm_about({})
                assert False, "Should have raised"
            except Exception as e:
                assert "500" in str(e)


# --- testing_farm_whoami ---


class TestWhoami:
    def test_cache_miss(self):
        with _mock_cache_get(None), _mock_http(200, WHOAMI_RESPONSE), _mock_cache_set() as mock_set:
            result = handler.testing_farm_whoami({})
            assert result["id"] == "user-123"
            mock_set.assert_called_once()

    def test_cache_hit(self):
        with _mock_cache_get(WHOAMI_RESPONSE):
            result = handler.testing_farm_whoami({})
            assert result == WHOAMI_RESPONSE


# --- testing_farm_list_requests ---


class TestListRequests:
    def test_basic_list(self):
        body = [SAMPLE_REQUEST]
        with _mock_cache_get(None), _mock_http(200, body), _mock_cache_set():
            result = handler.testing_farm_list_requests({})
            assert result["count"] == 1
            assert result["requests"][0]["id"] == REQ_ID
            assert result["requests"][0]["state"] == "complete"
            assert result["requests"][0]["compose"] == "Fedora-Rawhide"

    def test_cache_hit(self):
        cached = {"requests": [{"id": "cached"}], "count": 1}
        with _mock_cache_get(cached):
            result = handler.testing_farm_list_requests({})
            assert result == cached

    def test_with_filters(self):
        with _mock_cache_get(None), _mock_http(200, []) as mock, _mock_cache_set():
            handler.testing_farm_list_requests({"state": "running", "limit": 5})
            call_args = mock.call_args
            query = call_args[1].get("query") or call_args[0][2]
            assert query["state"] == "running"
            assert query["limit"] == 5

    def test_http_error(self):
        with _mock_cache_get(None), _mock_http(500, {"error": "fail"}):
            try:
                handler.testing_farm_list_requests({})
                assert False, "Should have raised"
            except Exception as e:
                assert "500" in str(e)


# --- testing_farm_get_request ---


class TestGetRequest:
    def test_success(self):
        with _mock_cache_get(None), _mock_http(200, SAMPLE_REQUEST), _mock_cache_set():
            result = handler.testing_farm_get_request({"request_id": REQ_ID})
            assert result["id"] == REQ_ID
            assert result["state"] == "complete"
            assert result["result"] == "passed"
            assert result["compose"] == "Fedora-Rawhide"
            assert result["arch"] == "x86_64"
            assert result["artifacts_url"] == f"https://artifacts.example.com/{REQ_ID}"

    def test_summary_included_when_present(self):
        req = {**SAMPLE_REQUEST, "result": {"overall": "passed", "summary": "2 of 2 tests passed"}}
        with _mock_cache_get(None), _mock_http(200, req), _mock_cache_set():
            result = handler.testing_farm_get_request({"request_id": REQ_ID})
            assert result["summary"] == "2 of 2 tests passed"

    def test_summary_omitted_when_empty(self):
        with _mock_cache_get(None), _mock_http(200, SAMPLE_REQUEST), _mock_cache_set():
            result = handler.testing_farm_get_request({"request_id": REQ_ID})
            assert "summary" not in result

    def test_running(self):
        running_req = {**SAMPLE_REQUEST, "state": "running"}
        with _mock_cache_get(None), _mock_http(200, running_req), _mock_cache_set():
            result = handler.testing_farm_get_request({"request_id": REQ_ID})
            assert result["state"] == "running"

    def test_stages_trimmed(self):
        req_with_stages = {
            **SAMPLE_REQUEST,
            "run": {
                "log": "https://example.com/log",
                "stages": [{"name": "provision", "status": "ok", "extra_data": "big blob"}],
            },
        }
        with _mock_cache_get(None), _mock_http(200, req_with_stages), _mock_cache_set():
            result = handler.testing_farm_get_request({"request_id": REQ_ID})
            assert result["run_stages"] == [{"name": "provision", "status": "ok"}]

    def test_cache_hit(self):
        with _mock_cache_get(SAMPLE_REQUEST):
            result = handler.testing_farm_get_request({"request_id": REQ_ID})
            assert result["id"] == REQ_ID
            assert result["state"] == "complete"

    def test_invalid_request_id(self):
        try:
            handler.testing_farm_get_request({"request_id": "not-a-valid-uuid"})
            assert False, "Should have raised"
        except Exception as e:
            assert "Invalid request ID" in str(e)


# --- testing_farm_list_composes ---


class TestListComposes:
    def test_cache_miss_trims_response(self):
        composes = {"Fedora": [{"name": "Fedora-Rawhide", "allowed_arches": ["x86_64"]}]}
        with _mock_cache_get(None), _mock_http(200, composes), _mock_cache_set() as mock_set:
            result = handler.testing_farm_list_composes({})
            # Should trim to just names, stripping metadata.
            assert result == {"Fedora": ["Fedora-Rawhide"]}
            mock_set.assert_called_once()
            assert mock_set.call_args[1]["ttl"] == 14400

    def test_plain_string_composes(self):
        composes = {"Fedora": ["Fedora-Rawhide", "Fedora-41"]}
        with _mock_cache_get(None), _mock_http(200, composes), _mock_cache_set():
            result = handler.testing_farm_list_composes({})
            assert result == {"Fedora": ["Fedora-Rawhide", "Fedora-41"]}


# --- testing_farm_list_reservations ---


class TestListReservations:
    def test_filters_for_reserve_plan(self):
        body = [RESERVE_REQUEST, SAMPLE_REQUEST]
        with _mock_cache_get(None), _mock_http(200, body), _mock_cache_set():
            result = handler.testing_farm_list_reservations({})
            assert result["count"] == 1
            assert result["reservations"][0]["id"] == REQ_ID_2
            assert result["reservations"][0]["duration_min"] == "60"

    def test_empty_when_no_reservations(self):
        with _mock_cache_get(None), _mock_http(200, [SAMPLE_REQUEST]), _mock_cache_set():
            result = handler.testing_farm_list_reservations({})
            assert result["count"] == 0

    def test_cache_hit(self):
        cached = {"reservations": [{"id": "cached"}], "count": 1}
        with _mock_cache_get(cached):
            result = handler.testing_farm_list_reservations({})
            assert result == cached


# --- _extract_result ---


class TestExtractResult:
    def test_dict_result(self):
        assert handler._extract_result({"state": "complete", "result": {"overall": "passed"}}) == "passed"

    def test_string_result(self):
        assert handler._extract_result({"state": "error", "result": "error"}) == "error"

    def test_empty_result_terminal(self):
        assert handler._extract_result({"state": "complete", "result": {}}) == "unknown"

    def test_missing_result_terminal(self):
        assert handler._extract_result({"state": "complete"}) == "unknown"

    def test_none_result_terminal(self):
        assert handler._extract_result({"state": "complete", "result": None}) == "unknown"

    def test_running_returns_pending(self):
        assert handler._extract_result({"state": "running", "result": {}}) == "pending"

    def test_new_returns_pending(self):
        assert handler._extract_result({"state": "new"}) == "pending"

    def test_queued_returns_pending(self):
        assert handler._extract_result({"state": "queued"}) == "pending"

    def test_cancel_requested_returns_pending(self):
        assert handler._extract_result({"state": "cancel-requested"}) == "pending"

    def test_canceled_returns_canceled(self):
        assert handler._extract_result({"state": "canceled"}) == "canceled"


# --- _parse_ssh_from_results_xml ---


class TestParseSshFromResultsXml:
    def test_from_connectable_host_hostname(self):
        xml = """<testsuites>
            <testsuite name="/testing-farm/reserve">
                <testcase name="/testing-farm/reserve-system">
                    <properties>
                        <property name="baseosci.connectable_host"
                                  value="server01.example.com"/>
                    </properties>
                </testcase>
            </testsuite>
        </testsuites>"""
        result = handler._parse_ssh_from_results_xml(xml, "https://artifacts")
        assert result == "server01.example.com"

    def test_from_connectable_host_ip(self):
        xml = """<testsuites>
            <testsuite>
                <testcase>
                    <properties>
                        <property name="baseosci.connectable_host"
                                  value="10.31.8.213"/>
                    </properties>
                </testcase>
            </testsuite>
        </testsuites>"""
        result = handler._parse_ssh_from_results_xml(xml, "https://artifacts")
        assert result == "10.31.8.213"

    def test_from_guests_yaml(self):
        xml = '<testsuite><logs><log href="work-dir/" name="workdir"/></logs></testsuite>'
        yaml_content = """default-0:
            -OPTIONLESS-FIELDS:
              - primary_address
              - topology_address
              - facts
            primary-address: 10.31.8.213
            topology-address: 10.31.8.213
        """

        def mock_http(method, path, url=None, **kwargs):
            if url and url.endswith("guests.yaml"):
                return 200, yaml_content, {}
            return 404, "", {}

        with patch.object(handler, "http", side_effect=mock_http):
            ip = handler._parse_ssh_from_results_xml(xml, "https://artifacts")
            assert ip == "10.31.8.213"


# --- _extract_ip_from_console ---


class TestExtractIpFromConsole:
    def test_cloud_init_pattern(self):
        text = "ci-info: | eth0 | True | 10.0.0.42 | 255.255.255.0 | fd00::1 | global |"
        assert handler._extract_ip_from_console(text) == "10.0.0.42"

    def test_ipv4_address_pattern(self):
        text = "Using IPv4 address: 192.168.1.100"
        assert handler._extract_ip_from_console(text) == "192.168.1.100"

    def test_ssh_command_pattern(self):
        text = "You can connect using: ssh root@172.16.0.5"
        assert handler._extract_ip_from_console(text) == "172.16.0.5"

    def test_no_match(self):
        assert handler._extract_ip_from_console("no ip here") is None

    def test_cloud_init_skips_non_global(self):
        text = "ci-info: | lo | True | 127.0.0.1 | 255.0.0.0 | ::1 | host |"
        assert handler._extract_ip_from_console(text) is None


# --- _parse_xunit ---


class TestParseXunit:
    def test_passed_tests(self):
        xml = """<testsuite>
            <testcase name="test1" classname="suite" time="1.5"/>
            <testcase name="test2" classname="suite" time="0.3"/>
        </testsuite>"""
        tests = handler._parse_xunit(xml)
        assert len(tests) == 2
        assert tests[0]["result"] == "passed"
        assert tests[0]["name"] == "test1"
        # classname and time are trimmed to save tokens.
        assert "classname" not in tests[0]
        assert "time" not in tests[0]

    def test_failed_test(self):
        xml = """<testsuite>
            <testcase name="test1" classname="suite" time="1.0">
                <failure message="assertion failed"/>
            </testcase>
        </testsuite>"""
        tests = handler._parse_xunit(xml)
        assert tests[0]["result"] == "failure"
        assert tests[0]["message"] == "assertion failed"

    def test_error_test(self):
        xml = """<testsuite>
            <testcase name="test1" classname="suite" time="0.1">
                <error message="crash"/>
            </testcase>
        </testsuite>"""
        tests = handler._parse_xunit(xml)
        assert tests[0]["result"] == "error"

    def test_skipped_test(self):
        xml = """<testsuite>
            <testcase name="test1" classname="suite" time="0.0">
                <skipped/>
            </testcase>
        </testsuite>"""
        tests = handler._parse_xunit(xml)
        assert tests[0]["result"] == "skipped"

    def test_invalid_xml(self):
        tests = handler._parse_xunit("not xml")
        assert tests == []


# --- testing_farm_get_results ---


class TestGetResults:
    def test_with_xunit(self):
        body = {
            "state": "complete",
            "result": {
                "overall": "passed",
                "xunit": '<testsuite><testcase name="t1" classname="s" time="1.0"/></testsuite>',
            },
        }
        with _mock_cache_get(None), _mock_http(200, body), _mock_cache_set():
            result = handler.testing_farm_get_results({"request_id": REQ_ID})
            assert result["result"] == "passed"
            assert result["test_count"] == 1
            assert result["tests"][0]["name"] == "t1"

    def test_summary_included_when_present(self):
        body = {
            "state": "complete",
            "result": {
                "overall": "passed",
                "summary": "3 of 3 tests passed",
                "xunit": '<testsuite><testcase name="t1" classname="s" time="1.0"/></testsuite>',
            },
        }
        with _mock_cache_get(None), _mock_http(200, body), _mock_cache_set():
            result = handler.testing_farm_get_results({"request_id": REQ_ID})
            assert result["summary"] == "3 of 3 tests passed"

    def test_running_returns_message(self):
        body = {"state": "running", "result": {}}
        with _mock_cache_get(None), _mock_http(200, body):
            result = handler.testing_farm_get_results({"request_id": REQ_ID})
            assert result["state"] == "running"
            assert result["result"] == "pending"
            assert "not available" in result["message"].lower()
            assert "tests" not in result

    def test_queued_returns_message(self):
        body = {"state": "queued", "result": {}}
        with _mock_cache_get(None), _mock_http(200, body):
            result = handler.testing_farm_get_results({"request_id": REQ_ID})
            assert result["state"] == "queued"
            assert "message" in result

    def test_error_state_cached(self):
        body = {"state": "error", "result": "error"}
        with _mock_cache_get(None), _mock_http(200, body), _mock_cache_set() as mock_set:
            result = handler.testing_farm_get_results({"request_id": REQ_ID})
            assert result["state"] == "error"
            # Error state should be cached (terminal).
            cache_calls = [c for c in mock_set.call_args_list if c[0][0] == f"tf:results:{REQ_ID}"]
            assert len(cache_calls) == 1

    def test_cache_hit(self):
        cached = {"request_id": REQ_ID, "result": "passed"}
        with _mock_cache_get(cached):
            result = handler.testing_farm_get_results({"request_id": REQ_ID})
            assert result == cached


# --- testing_farm_get_logs ---


class TestGetLogs:
    def test_success(self):
        with _mock_cache_get(None), _mock_http(200, SAMPLE_REQUEST), _mock_cache_set():
            result = handler.testing_farm_get_logs({"request_id": REQ_ID})
            assert result["pipeline_log"] == "https://example.com/log"
            assert result["artifacts_url"] == f"https://artifacts.example.com/{REQ_ID}"

    def test_cache_hit(self):
        with _mock_cache_get(SAMPLE_REQUEST):
            result = handler.testing_farm_get_logs({"request_id": REQ_ID})
            assert result["pipeline_log"] == "https://example.com/log"


# --- testing_farm_reserve ---


class TestReserve:
    def test_dry_run(self):
        with patch.object(handler, "ssh_keys", ["ssh-ed25519 AAAA testkey"]):
            result = handler.testing_farm_reserve({"compose": "Fedora-Rawhide", "arch": "x86_64", "dry_run": True})
            assert result["dry_run"] is True
            assert result["ssh_keys_count"] == 1
            payload = result["payload"]
            assert payload["test"]["fmf"]["name"] == "/testing-farm/reserve"
            env = payload["environments"][0]
            assert env["os"]["compose"] == "Fedora-Rawhide"
            assert env["arch"] == "x86_64"
            assert env["variables"]["TF_RESERVATION_DURATION"] == "60"
            # Verify SSH keys are base64 encoded.
            decoded = base64.b64decode(env["secrets"]["TF_RESERVATION_AUTHORIZED_KEYS_BASE64"]).decode()
            assert "ssh-ed25519 AAAA testkey" in decoded

    def test_submit(self):
        resp = {"id": "new-req", "state": "new"}
        with patch.object(handler, "ssh_keys", ["ssh-ed25519 AAAA testkey"]), _mock_http(200, resp):
            result = handler.testing_farm_reserve({"compose": "Fedora-41", "arch": "aarch64", "dry_run": False})
            assert result["id"] == "new-req"

    def test_extra_ssh_keys(self):
        with patch.object(handler, "ssh_keys", ["ssh-ed25519 AAAA auto"]):
            result = handler.testing_farm_reserve(
                {"compose": "Fedora-Rawhide", "arch": "x86_64", "ssh_keys": "ssh-rsa BBBB extra", "dry_run": True}
            )
            assert result["ssh_keys_count"] == 2

    def test_no_keys_raises(self):
        with patch.object(handler, "ssh_keys", []):
            try:
                handler.testing_farm_reserve({"compose": "Fedora-Rawhide", "arch": "x86_64", "dry_run": True})
                assert False, "Should have raised"
            except Exception as e:
                assert "SSH public keys" in str(e)

    def test_custom_duration(self):
        with patch.object(handler, "ssh_keys", ["ssh-ed25519 AAAA key"]):
            result = handler.testing_farm_reserve(
                {"compose": "Fedora-41", "arch": "x86_64", "duration": 120, "dry_run": True}
            )
            env = result["payload"]["environments"][0]
            assert env["variables"]["TF_RESERVATION_DURATION"] == "120"

    def test_hardware_specs(self):
        hw = {"cpu": {"processors": ">= 4"}, "memory": ">= 16 GB"}
        with patch.object(handler, "ssh_keys", ["ssh-ed25519 AAAA key"]):
            result = handler.testing_farm_reserve(
                {"compose": "Fedora-41", "arch": "x86_64", "hardware": hw, "dry_run": True}
            )
            env = result["payload"]["environments"][0]
            assert env["hardware"] == hw


# --- testing_farm_submit_test ---


class TestSubmitTest:
    def test_dry_run(self):
        result = handler.testing_farm_submit_test(
            {"git_url": "https://example.com/tests.git", "compose": "Fedora-Rawhide", "arch": "x86_64", "dry_run": True}
        )
        assert result["dry_run"] is True
        payload = result["payload"]
        assert payload["test"]["fmf"]["url"] == "https://example.com/tests.git"
        assert payload["test"]["fmf"]["ref"] == "main"
        env = payload["environments"][0]
        assert env["os"]["compose"] == "Fedora-Rawhide"

    def test_with_plan_name(self):
        result = handler.testing_farm_submit_test(
            {
                "git_url": "https://example.com/tests.git",
                "plan_name": "/plan/smoke",
                "compose": "Fedora-41",
                "arch": "x86_64",
                "dry_run": True,
            }
        )
        assert result["payload"]["test"]["fmf"]["name"] == "/plan/smoke"

    def test_with_timeout(self):
        result = handler.testing_farm_submit_test(
            {
                "git_url": "https://example.com/tests.git",
                "compose": "Fedora-41",
                "arch": "x86_64",
                "timeout": 120,
                "dry_run": True,
            }
        )
        assert result["payload"]["settings"]["pipeline"]["timeout"] == 7200

    def test_submit(self):
        resp = {"id": "test-req", "state": "new"}
        with _mock_http(200, resp):
            result = handler.testing_farm_submit_test(
                {
                    "git_url": "https://example.com/tests.git",
                    "compose": "Fedora-41",
                    "arch": "x86_64",
                    "dry_run": False,
                }
            )
            assert result["id"] == "test-req"


# --- testing_farm_cancel ---


class TestCancel:
    def test_dry_run(self):
        with _mock_http(200, {"state": "running"}):
            result = handler.testing_farm_cancel({"request_id": REQ_ID})
            assert result["dry_run"] is True
            assert result["request_id"] == REQ_ID
            assert result["current_state"] == "running"

    def test_success_invalidates_cache(self):
        with _mock_http(200, {}), _mock_cache_del() as mock_del:
            result = handler.testing_farm_cancel({"request_id": REQ_ID, "dry_run": False})
            assert result["request_id"] == REQ_ID
            assert "cancelled" in result["message"]
            # Should invalidate all cached data for this request.
            assert mock_del.call_count == 3
            deleted_keys = [c[0][0] for c in mock_del.call_args_list]
            assert f"tf:raw:{REQ_ID}" in deleted_keys
            assert f"tf:ssh:{REQ_ID}" in deleted_keys
            assert f"tf:results:{REQ_ID}" in deleted_keys

    def test_204_success(self):
        with _mock_http(204, {}), _mock_cache_del():
            result = handler.testing_farm_cancel({"request_id": REQ_ID, "dry_run": False})
            assert result["request_id"] == REQ_ID

    def test_invalid_request_id(self):
        try:
            handler.testing_farm_cancel({"request_id": "not-a-valid-uuid"})
            assert False, "Should have raised"
        except Exception as e:
            assert "Invalid request ID" in str(e)


# --- _discover_ssh_keys ---


class TestDiscoverSshKeys:
    def test_from_config_path(self, tmp_path):
        key_file = tmp_path / "id_test.pub"
        key_file.write_text("ssh-ed25519 AAAA configured")
        with patch.object(handler, "config", {"ssh_key_path": str(key_file)}):
            keys = handler._discover_ssh_keys()
            assert keys == ["ssh-ed25519 AAAA configured"]

    def test_auto_discover(self, tmp_path):
        ssh_dir = tmp_path / ".ssh"
        ssh_dir.mkdir()
        (ssh_dir / "id_ed25519.pub").write_text("ssh-ed25519 AAAA auto1")
        (ssh_dir / "id_rsa.pub").write_text("ssh-rsa BBBB auto2")
        (ssh_dir / "known_hosts").write_text("not a key")

        with (
            patch.object(handler, "config", {}),
            patch("os.path.expanduser", return_value=str(tmp_path)),
        ):
            keys = handler._discover_ssh_keys()
            assert len(keys) == 2
            assert "ssh-ed25519 AAAA auto1" in keys

    def test_missing_config_path(self, tmp_path):
        with patch.object(handler, "config", {"ssh_key_path": str(tmp_path / "nonexistent.pub")}):
            keys = handler._discover_ssh_keys()
            assert keys == []

    def test_empty_ssh_dir(self, tmp_path):
        ssh_dir = tmp_path / ".ssh"
        ssh_dir.mkdir()

        with (
            patch.object(handler, "config", {}),
            patch("os.path.expanduser", return_value=str(tmp_path)),
        ):
            keys = handler._discover_ssh_keys()
            assert keys == []

    def test_config_injected_keys_preferred(self):
        config = {
            "_ssh_public_keys": "ssh-ed25519 AAAA key1\nssh-rsa BBBB key2",
            "ssh_key_path": "/should/not/be/read",
        }
        with patch.object(handler, "config", config):
            keys = handler._discover_ssh_keys()
            assert len(keys) == 2
            assert keys[0] == "ssh-ed25519 AAAA key1"
            assert keys[1] == "ssh-rsa BBBB key2"

    def test_config_injected_single_key(self):
        config = {"_ssh_public_keys": "ssh-ed25519 AAAA only"}
        with patch.object(handler, "config", config):
            keys = handler._discover_ssh_keys()
            assert keys == ["ssh-ed25519 AAAA only"]

    def test_empty_config_keys_falls_through(self, tmp_path):
        ssh_dir = tmp_path / ".ssh"
        ssh_dir.mkdir()
        (ssh_dir / "id_ed25519.pub").write_text("ssh-ed25519 AAAA fallback")

        with (
            patch.object(handler, "config", {"_ssh_public_keys": ""}),
            patch("os.path.expanduser", return_value=str(tmp_path)),
        ):
            keys = handler._discover_ssh_keys()
            assert keys == ["ssh-ed25519 AAAA fallback"]

    def test_config_keys_filters_blank_lines(self):
        config = {"_ssh_public_keys": "ssh-ed25519 AAAA key1\n\n  \nssh-rsa BBBB key2\n"}
        with patch.object(handler, "config", config):
            keys = handler._discover_ssh_keys()
            assert len(keys) == 2


# --- testing_farm_get_ssh ---


class TestGetSsh:
    def test_cache_hit(self):
        cached = {"host": "10.0.0.1", "ssh_command": "ssh root@10.0.0.1"}
        with _mock_cache_get(cached):
            result = handler.testing_farm_get_ssh({"request_id": REQ_ID})
            assert result == cached

    def test_not_running(self):
        req = {**SAMPLE_REQUEST, "state": "queued"}
        req["run"] = {"artifacts": "https://artifacts.example.com/req-1"}
        with _mock_cache_get(None), _mock_http(200, req):
            result = handler.testing_farm_get_ssh({"request_id": REQ_ID})
            assert "error" in result
            assert result["state"] == "queued"
            assert result["artifacts_url"] == "https://artifacts.example.com/req-1"

    def test_complete(self):
        req = {**SAMPLE_REQUEST, "state": "complete"}
        req["run"] = {"artifacts": "https://artifacts.example.com/req-1"}
        with _mock_cache_get(None), _mock_http(200, req), _mock_cache_set():
            result = handler.testing_farm_get_ssh({"request_id": REQ_ID})
            assert "error" in result
            assert result["state"] == "complete"
            assert "returned" in result["error"]
            assert result["artifacts_url"] == "https://artifacts.example.com/req-1"

    def test_results_xml_not_available(self):
        req = {**SAMPLE_REQUEST, "state": "running"}
        req["run"] = {"artifacts": "https://artifacts.example.com/req-1"}

        def mock_http(method, path, url=None, **kwargs):
            if url and "results.xml" in url:
                return 404, "Not Found", {}
            return 200, req, {}

        with _mock_cache_get(None), patch.object(handler, "http", side_effect=mock_http):
            result = handler.testing_farm_get_ssh({"request_id": REQ_ID})
            assert "error" in result
            assert "provisioning" in result["error"].lower()
            assert result["state"] == "running"
            assert result["artifacts_url"] == "https://artifacts.example.com/req-1"
            assert "hint" in result

    def test_no_artifacts_url(self):
        req = {**SAMPLE_REQUEST, "run": {}}
        with _mock_cache_get(None), _mock_http(200, req), _mock_cache_set():
            try:
                handler.testing_farm_get_ssh({"request_id": REQ_ID})
                assert False, "Should have raised"
            except Exception as e:
                assert "artifacts" in str(e).lower()
