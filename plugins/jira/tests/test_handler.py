"""Unit tests for handler.py — _api_path(), _detect_cloud(), and cache ops."""

from unittest.mock import patch

import handler


class TestApiPath:
    """Test _api_path() path rewriting logic."""

    def test_server_mode_no_rewriting(self):
        """Server mode: all paths pass through unchanged."""
        with patch.object(handler, "is_cloud", False):
            assert handler._api_path("/rest/api/2/myself") == "/rest/api/2/myself"
            assert handler._api_path("/rest/api/2/search") == "/rest/api/2/search"
            assert handler._api_path("/rest/agile/1.0/board") == "/rest/agile/1.0/board"

    def test_cloud_mode_basic_rewriting(self):
        """Cloud mode: /rest/api/2/ -> /rest/api/3/."""
        with patch.object(handler, "is_cloud", True):
            assert handler._api_path("/rest/api/2/myself") == "/rest/api/3/myself"
            assert handler._api_path("/rest/api/2/field") == "/rest/api/3/field"
            assert handler._api_path("/rest/api/2/issue/PROJ-1") == "/rest/api/3/issue/PROJ-1"

    def test_cloud_mode_search_special_case(self):
        """Cloud mode: search endpoint gets special path."""
        with patch.object(handler, "is_cloud", True):
            assert handler._api_path("/rest/api/2/search") == "/rest/api/3/search/jql"

    def test_search_trailing_slash_not_special_case(self):
        """Trailing slash should NOT match search special case."""
        with patch.object(handler, "is_cloud", True):
            # This is important - /rest/api/3/search/ may not exist or behave differently
            assert handler._api_path("/rest/api/2/search/") == "/rest/api/3/search/"

    def test_non_api_paths_unchanged(self):
        """Agile and other non-/rest/api/2/ paths unchanged."""
        with patch.object(handler, "is_cloud", True):
            assert handler._api_path("/rest/agile/1.0/board") == "/rest/agile/1.0/board"
            assert handler._api_path("/rest/greenhopper/1.0/sprint") == "/rest/greenhopper/1.0/sprint"
            assert handler._api_path("/some/other/path") == "/some/other/path"

    def test_path_with_2_substring(self):
        """Path containing /2/ as substring should only replace first /rest/api/2/."""
        with patch.object(handler, "is_cloud", True):
            # PROJ-2 contains "/2/" but should not cause double replacement
            result = handler._api_path("/rest/api/2/issue/PROJ-2/comment")
            assert result == "/rest/api/3/issue/PROJ-2/comment"
            assert result.count("/rest/api/3/") == 1

    def test_idempotency(self):
        """Calling _api_path twice should not double-rewrite."""
        with patch.object(handler, "is_cloud", True):
            path = "/rest/api/2/myself"
            once = handler._api_path(path)
            twice = handler._api_path(once)
            assert once == "/rest/api/3/myself"
            assert twice == "/rest/api/3/myself"  # Not /rest/api/4/myself


class TestDetectCloud:
    """Test _detect_cloud() deployment detection logic."""

    def test_cloud_deployment_type(self):
        """deploymentType=Cloud returns (True, True)."""
        response = (200, {"deploymentType": "Cloud", "version": "1001.0.0"}, {})
        with patch.object(handler, "http", return_value=response):
            is_cloud, auth_ok = handler._detect_cloud()
            assert is_cloud is True
            assert auth_ok is True

    def test_server_deployment_type(self):
        """deploymentType=Server returns (False, True)."""
        response = (200, {"deploymentType": "Server", "version": "9.12.0"}, {})
        with patch.object(handler, "http", return_value=response):
            is_cloud, auth_ok = handler._detect_cloud()
            assert is_cloud is False
            assert auth_ok is True

    def test_missing_deployment_type(self):
        """No deploymentType field returns (False, True)."""
        response = (200, {"version": "8.0.0"}, {})
        with patch.object(handler, "http", return_value=response):
            is_cloud, auth_ok = handler._detect_cloud()
            assert is_cloud is False
            assert auth_ok is True

    def test_auth_failure_401(self):
        """HTTP 401 returns (False, False)."""
        response = (401, {"message": "Unauthorized"}, {})
        with patch.object(handler, "http", return_value=response):
            is_cloud, auth_ok = handler._detect_cloud()
            assert is_cloud is False
            assert auth_ok is False

    def test_auth_failure_403(self):
        """HTTP 403 returns (False, False)."""
        response = (403, {"message": "Forbidden"}, {})
        with patch.object(handler, "http", return_value=response):
            is_cloud, auth_ok = handler._detect_cloud()
            assert is_cloud is False
            assert auth_ok is False

    def test_410_triggers_v3_fallback_success(self):
        """HTTP 410 on v2 triggers v3 fallback, v3 success confirms Cloud."""
        responses = [
            (410, {"message": "API removed"}, {}),  # v2 serverInfo
            (200, {"deploymentType": "Cloud", "version": "1001.0.0"}, {}),  # v3 serverInfo
        ]
        with patch.object(handler, "http", side_effect=responses):
            is_cloud, auth_ok = handler._detect_cloud()
            assert is_cloud is True
            assert auth_ok is True

    def test_410_triggers_v3_fallback_failure(self):
        """HTTP 410 on v2, v3 also fails - still assume Cloud."""
        responses = [
            (410, {"message": "API removed"}, {}),  # v2 serverInfo
            (500, {"error": "Internal error"}, {}),  # v3 serverInfo fails
        ]
        with patch.object(handler, "http", side_effect=responses):
            is_cloud, auth_ok = handler._detect_cloud()
            # 410 on v2 is strong signal of Cloud, even if v3 fails
            assert is_cloud is True
            assert auth_ok is True


class TestInitCloudShortcut:
    """Test _init() cloud detection shortcut via config auth_type."""

    def test_cloud_skips_detect(self):
        """auth_type=cloud skips _detect_cloud() entirely."""
        with (
            patch.object(handler, "_detect_cloud") as mock_detect,
            patch.object(handler, "_detect_sprint_field", return_value="sprint"),
        ):
            handler._init({"config": {"auth_type": "cloud"}})
            mock_detect.assert_not_called()
            assert handler.is_cloud is True

    def test_auto_calls_detect(self):
        """auth_type=auto (default) calls _detect_cloud()."""
        with (
            patch.object(handler, "_detect_cloud", return_value=(False, True)) as mock_detect,
            patch.object(handler, "_detect_sprint_field", return_value="sprint"),
        ):
            handler._init({"config": {"auth_type": "auto"}})
            mock_detect.assert_called_once()
            assert handler.is_cloud is False

    def test_missing_auth_type_calls_detect(self):
        """No auth_type in config defaults to auto (calls _detect_cloud)."""
        with (
            patch.object(handler, "_detect_cloud", return_value=(True, True)) as mock_detect,
            patch.object(handler, "_detect_sprint_field", return_value="sprint"),
        ):
            handler._init({"config": {}})
            mock_detect.assert_called_once()

    def test_server_token_calls_detect(self):
        """auth_type=server-token still calls _detect_cloud() to preserve auth diagnostics."""
        with (
            patch.object(handler, "_detect_cloud", return_value=(False, True)) as mock_detect,
            patch.object(handler, "_detect_sprint_field", return_value="sprint"),
        ):
            handler._init({"config": {"auth_type": "server-token"}})
            mock_detect.assert_called_once()
            assert handler.is_cloud is False


class TestCacheDel:
    """Test cache_del() protocol message."""

    def test_sends_cache_del(self):
        with patch.object(handler, "_send") as mock_send, patch.object(handler, "_recv", return_value={"ok": True}):
            handler.cache_del("mykey")
            msg = mock_send.call_args[0][0]
            assert msg["type"] == "cache_del"
            assert msg["key"] == "mykey"


class TestCacheFlush:
    """Test cache_flush() protocol message."""

    def test_sends_cache_flush(self):
        with patch.object(handler, "_send") as mock_send, patch.object(handler, "_recv", return_value={"ok": True}):
            handler.cache_flush()
            msg = mock_send.call_args[0][0]
            assert msg["type"] == "cache_flush"
            assert "key" not in msg


class TestInvalidateCache:
    """Test invalidate_cache() best-effort wrapper."""

    def test_calls_cache_flush(self):
        with patch.object(handler, "cache_flush") as mock_flush:
            handler.invalidate_cache("PROJ-1")
            mock_flush.assert_called_once()

    def test_swallows_exceptions(self):
        with patch.object(handler, "cache_flush", side_effect=RuntimeError("boom")):
            # Should not raise
            handler.invalidate_cache("PROJ-1")

    def test_works_without_issue_keys(self):
        with patch.object(handler, "cache_flush") as mock_flush:
            handler.invalidate_cache()
            mock_flush.assert_called_once()
