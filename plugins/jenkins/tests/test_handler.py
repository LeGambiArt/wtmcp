"""Tests for handler.py - protocol and init tests."""

import json
import sys
from pathlib import Path

# Import from parent directory
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
import handler


class TestProtocol:
    """Test protocol functions."""

    def test_gen_id_increments(self):
        id1 = handler._gen_id("test")
        id2 = handler._gen_id("test")
        assert id1 != id2
        assert "test-" in id1

    def test_send_formats_json(self, capsys):
        msg = {"type": "response", "data": "test"}
        handler._send(msg)
        captured = capsys.readouterr()
        assert json.loads(captured.out.strip()) == msg


class TestInit:
    """Test init sequence."""

    def test_init_stores_config(self, capsys):
        config_msg = {"config": {"some_key": "some_value"}}

        handler._init(config_msg)

        assert handler.config == config_msg["config"]

        captured = capsys.readouterr()
        assert "init:" in captured.err
        assert "ready" in captured.err

    def test_init_empty_config(self, capsys):
        config_msg = {}

        handler._init(config_msg)

        assert handler.config == {}

        captured = capsys.readouterr()
        assert "init:" in captured.err


class TestValidateJenkinsUrl:
    """Test jenkins_url validation."""

    def test_valid_https(self):
        err = handler.set_jenkins_url("https://jenkins.example.com")
        assert err is None
        assert handler._jenkins_url == "https://jenkins.example.com"

    def test_valid_http(self):
        err = handler.set_jenkins_url("http://localhost:8080")
        assert err is None
        assert handler._jenkins_url == "http://localhost:8080"

    def test_trailing_slash_stripped(self):
        err = handler.set_jenkins_url("https://jenkins.example.com/")
        assert err is None
        assert handler._jenkins_url == "https://jenkins.example.com"

    def test_invalid_scheme_rejected(self):
        err = handler.set_jenkins_url("ftp://jenkins.example.com")
        assert err is not None
        assert "invalid" in err["error"]

    def test_empty_string_rejected(self):
        err = handler.set_jenkins_url("")
        assert err is not None
        assert "invalid" in err["error"]

    def test_none_rejected(self):
        err = handler.set_jenkins_url(None)
        assert err is not None
        assert "invalid" in err["error"]

    def test_whitespace_only_rejected(self):
        err = handler.set_jenkins_url("   ")
        assert err is not None
        assert "invalid" in err["error"]


class TestHttpUrlConstruction:
    """Test that http() constructs full URLs from _jenkins_url."""

    def test_url_constructed_from_jenkins_url(self, capsys):
        handler._jenkins_url = "https://jenkins.example.com"  # type: ignore

        captured_msgs = []
        original_send = handler._send

        def mock_send(msg):
            captured_msgs.append(msg)

        handler._send = mock_send  # type: ignore
        handler._recv = lambda: {"status": 200, "body": {}, "headers": {}}  # type: ignore

        try:
            handler.http("GET", "/api/json")
        finally:
            handler._send = original_send

        assert len(captured_msgs) == 1
        msg = captured_msgs[0]
        assert msg["url"] == "https://jenkins.example.com/api/json"
        assert msg["domains"] == ["jenkins.example.com"]

    def test_explicit_url_overrides_jenkins_url(self, capsys):
        handler._jenkins_url = "https://jenkins.example.com"  # type: ignore

        captured_msgs = []
        original_send = handler._send

        def mock_send(msg):
            captured_msgs.append(msg)

        handler._send = mock_send  # type: ignore
        handler._recv = lambda: {"status": 200, "body": {}, "headers": {}}  # type: ignore

        try:
            handler.http("GET", "/unused", url="https://other.example.com/api")
        finally:
            handler._send = original_send

        msg = captured_msgs[0]
        assert msg["url"] == "https://other.example.com/api"
        assert "domains" not in msg
