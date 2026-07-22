#!/usr/bin/env python3
"""Jira plugin handler.

Persistent handler with init/shutdown lifecycle. All HTTP calls go
through the core proxy (no HTTP libraries needed). Auth is handled
by the core. Cache is provided by the core cache service.

Zero dependencies beyond Python stdlib.
"""

import base64
import json
import sys

_next_id = 0

# Plugin state set during init.
config = {}
is_cloud = False
sprint_field = "sprint"  # custom field ID, auto-detected at init


def _send(msg):
    """Write a JSON message to stdout (core reads it)."""
    print(json.dumps(msg, separators=(",", ":")), flush=True)


def _gen_id(prefix="svc"):
    global _next_id
    _next_id += 1
    return f"{prefix}-{_next_id}"


def _recv():
    """Read a JSON message from stdin (core writes it)."""
    line = sys.stdin.readline()
    if not line:
        sys.exit(0)
    return json.loads(line.strip())


_api_path_logged = False


def _api_path(path):
    """Rewrite REST API paths for Jira Cloud (v2 → v3).

    Jira Cloud has removed /rest/api/2/ endpoints in favor of v3.
    The search endpoint also changed path structure. Server/DC
    instances continue using v2.

    Note: v3 search path changed from /rest/api/2/search to
    /rest/api/3/search/jql but the query parameter name "jql"
    remains the same.
    """
    global _api_path_logged
    if not is_cloud or "/rest/api/2/" not in path:
        return path
    if path == "/rest/api/2/search":
        rewritten = "/rest/api/3/search/jql"
    else:
        rewritten = path.replace("/rest/api/2/", "/rest/api/3/", 1)
    if not _api_path_logged:
        log(f"rewriting API paths: v2 -> v3 (cloud={is_cloud})")
        _api_path_logged = True
    return rewritten


def http(method, path, query=None, body=None, headers=None, url=None):
    """Make an HTTP request via the core proxy.

    Returns (status, body, headers). Status 0 means transport error.
    When url is provided, it overrides base_url + path (must match
    an allowed domain).
    """
    msg = {
        "id": _gen_id("http"),
        "type": "http_request",
        "method": method,
        "path": _api_path(path),
    }
    if url:
        msg["url"] = url
    if query:
        msg["query"] = query
    if body is not None:
        msg["body"] = body
    if headers:
        msg["headers"] = headers
    _send(msg)
    resp = _recv()
    status = resp.get("status", 0)
    if status == 0:
        return 0, {"error": resp.get("error", "request failed")}, {}
    resp_body = resp.get("body", {})
    resp_headers = resp.get("headers", {})
    if resp.get("body_encoding") == "base64" and isinstance(resp_body, str):
        import base64

        resp_body = base64.b64decode(resp_body)
    return status, resp_body, resp_headers


def http_upload(method, path, field, filename, content, content_type=None, extra_fields=None, headers=None, query=None):
    """Upload a file via the core proxy using multipart/form-data.

    Args:
        method: HTTP method (usually "POST").
        path: API path relative to base_url.
        field: Form field name for the file (e.g., "file").
        filename: Filename for Content-Disposition.
        content: File content as bytes.
        content_type: MIME type (default: application/octet-stream).
        extra_fields: Optional dict of additional text form fields.
        headers: Optional extra headers (do NOT set Content-Type).
        query: Optional query parameters.

    Returns (status, body, headers).
    """
    import base64 as b64

    parts = [
        {
            "field": field,
            "filename": filename,
            "body": b64.b64encode(content).decode("ascii"),
            "body_encoding": "base64",
        }
    ]
    if content_type:
        parts[0]["content_type"] = content_type

    if extra_fields:
        for k, v in extra_fields.items():
            parts.append({"field": k, "body": str(v)})

    msg = {"id": _gen_id("http"), "type": "http_request", "method": method, "path": _api_path(path), "multipart": parts}
    if query:
        msg["query"] = query
    if headers:
        msg["headers"] = headers
    _send(msg)
    resp = _recv()
    status = resp.get("status", 0)
    if status == 0:
        return 0, {"error": resp.get("error", "request failed")}, {}
    resp_body = resp.get("body", {})
    resp_headers = resp.get("headers", {})
    if resp.get("body_encoding") == "base64" and isinstance(resp_body, str):
        import base64

        resp_body = base64.b64decode(resp_body)
    return status, resp_body, resp_headers


def cache_get(key):
    """Get a value from the core cache. Returns value or None."""
    _send({"id": _gen_id("cache"), "type": "cache_get", "key": key})
    resp = _recv()
    if resp.get("hit"):
        return resp["value"]
    return None


def cache_set(key, value, ttl=None):
    """Set a value in the core cache."""
    msg = {
        "id": _gen_id("cache"),
        "type": "cache_set",
        "key": key,
        "value": value,
    }
    if ttl is not None:
        msg["ttl"] = ttl
    _send(msg)
    _recv()  # consume ack


def cache_del(key):
    """Delete a key from the core cache."""
    _send({"id": _gen_id("cache"), "type": "cache_del", "key": key})
    _recv()  # consume ack


def cache_flush():
    """Flush all entries in this plugin's cache namespace."""
    _send({"id": _gen_id("cache"), "type": "cache_flush"})
    _recv()  # consume ack


def invalidate_cache(*_issue_keys):
    """Flush plugin cache after modifying issues.

    Accepts issue keys for future targeted invalidation, but currently
    flushes the entire namespace.
    """
    try:
        cache_flush()
    except Exception:
        pass  # best-effort — don't mask a successful write


def file_write(path, content, encoding="text", permissions="0600", mkdir=True):
    """Write a file to the plugin's output directory via the core.

    Returns the absolute resolved path of the written file.
    For binary content, pass bytes and encoding="base64".
    """
    if encoding == "base64" and isinstance(content, bytes):
        content = base64.b64encode(content).decode()
    msg = {
        "id": _gen_id("fw"),
        "type": "file_write",
        "path": path,
        "content": content,
        "body_encoding": encoding,
        "permissions": permissions,
    }
    if not mkdir:
        msg["mkdir"] = False
    _send(msg)
    resp = _recv()
    if resp.get("error"):
        raise IOError(resp["error"]["message"])
    return resp["path"]


def file_write_from(path, source_path, permissions="0600", mkdir=True):
    """Write a file using source_path handoff (for large files).

    The source file must be in the plugin's tmpdir and have Nlink == 1.
    Returns the absolute resolved path of the written file.
    """
    msg = {
        "id": _gen_id("fw"),
        "type": "file_write",
        "path": path,
        "source_path": source_path,
        "permissions": permissions,
    }
    if not mkdir:
        msg["mkdir"] = False
    _send(msg)
    resp = _recv()
    if resp.get("error"):
        raise IOError(resp["error"]["message"])
    return resp["path"]


def file_read(path, encoding="text"):
    """Read a file from the plugin's output directory via the core.

    Returns the file content as a string. For binary files, use
    encoding="base64" and decode the result.
    """
    msg = {
        "id": _gen_id("fr"),
        "type": "file_read",
        "path": path,
        "body_encoding": encoding,
    }
    _send(msg)
    resp = _recv()
    if resp.get("error"):
        raise IOError(resp["error"]["message"])
    return resp.get("content", "")


def _detect_cloud():
    """Detect if Jira instance is Cloud based on serverInfo.

    Returns (is_cloud, auth_ok). If auth fails (403/401), the selected
    auth variant is likely wrong for this instance type.
    """
    status, body, _ = http("GET", "/rest/api/2/serverInfo")

    # Auth failure - cannot determine deployment type
    if status in (401, 403):
        return False, False

    # 410 Gone - v2 API removed, this is Cloud. Try v3 to confirm.
    if status == 410:
        log("v2 serverInfo returned 410, attempting v3 fallback")
        status, body, _ = http("GET", "/rest/api/3/serverInfo")
        if 200 <= status < 300 and isinstance(body, dict):
            log("v3 serverInfo succeeded - confirmed Cloud")
            return True, True
        # v3 also failed, but 410 on v2 is a Cloud signal
        log("v3 serverInfo also failed, assuming Cloud (v2 removal indicates Cloud)")
        return True, True

    # Success - check deploymentType
    if 200 <= status < 300 and isinstance(body, dict):
        deployment = body.get("deploymentType", "")
        if deployment.lower() == "cloud":
            return True, True

    return False, True


def _detect_sprint_field():
    """Detect the sprint custom field ID.

    On Cloud, the sprint field is "sprint". On Server, it's a
    custom field (e.g., customfield_12310940). Queries the field
    list and looks for a field named "Sprint".
    """
    if is_cloud:
        return "sprint"
    status, body, _ = http("GET", "/rest/api/2/field")
    if 200 <= status < 300 and isinstance(body, list):
        for field in body:
            if field.get("name", "").lower() == "sprint":
                return field.get("id", "sprint")
    return "sprint"


def _init(msg):
    """Handle init message: store config, detect Cloud vs Server."""
    global config, is_cloud, sprint_field
    config = msg.get("config", {})
    auth_type = config.get("auth_type", "auto")
    if auth_type == "cloud":
        is_cloud, auth_ok = True, True
        log("init: JIRA_AUTH_TYPE=cloud, skipping cloud detection")
    else:
        is_cloud, auth_ok = _detect_cloud()
        if not auth_ok:
            log(
                "WARNING: authentication failed (HTTP 401/403). "
                "If both JIRA_EMAIL and JIRA_TOKEN are set but this is "
                "a Jira Server/DC instance, set JIRA_AUTH_TYPE=server-token "
                "to use bearer auth instead of basic auth. "
                "If this is Jira Cloud, verify your API token."
            )
    sprint_field = _detect_sprint_field()
    log(f"init: cloud={is_cloud}, sprint_field={sprint_field}, url={config.get('jira_url', '?')}")


def log(message):
    """Write a log message to stderr (captured by core)."""
    print(f"jira: {message}", file=sys.stderr)


def main():
    # Import here to avoid circular import — handler defines protocol
    # functions that tools_read needs, and handler needs TOOLS from
    # tools_read.
    from tools_cache import TOOLS as CACHE_TOOLS
    from tools_read import TOOLS
    from tools_sprint import TOOLS as SPRINT_TOOLS
    from tools_write import TOOLS as WRITE_TOOLS

    TOOLS.update(WRITE_TOOLS)
    TOOLS.update(SPRINT_TOOLS)
    TOOLS.update(CACHE_TOOLS)

    log("handler starting")

    while True:
        msg = _recv()
        msg_id = msg.get("id")
        msg_type = msg.get("type")

        if msg_type == "init":
            _init(msg)
            _send({"id": msg_id, "type": "init_ok"})
            continue

        if msg_type == "shutdown":
            log("shutting down")
            _send({"id": msg_id, "type": "shutdown_ok"})
            break

        if msg_type == "tool_call":
            tool = msg.get("tool")
            handler_fn = TOOLS.get(tool)
            if not handler_fn:
                _send(
                    {
                        "id": msg_id,
                        "type": "tool_result",
                        "error": {
                            "code": "unknown_tool",
                            "message": f"Unknown: {tool}",
                        },
                    }
                )
                continue

            try:
                result = handler_fn(msg.get("params", {}))
                _send({"id": msg_id, "type": "tool_result", "result": result})
            except Exception as e:
                log(f"error in {tool}: {e}")
                _send(
                    {
                        "id": msg_id,
                        "type": "tool_result",
                        "error": {"code": "handler_error", "message": str(e)},
                    }
                )
            continue

        log(f"unknown message type: {msg_type}")


if __name__ == "__main__":
    sys.modules["handler"] = sys.modules[__name__]
    main()
