#!/usr/bin/env python3
"""GitHub plugin handler.

Persistent handler with init/shutdown lifecycle. All HTTP calls go
through the core proxy (no HTTP libraries needed). Auth is handled
by the core. Cache is provided by the core cache service.

Zero dependencies beyond Python stdlib.
"""

import json
import sys

_next_id = 0

# Plugin state set during init.
config = {}
username = ""

# GitHub API headers injected on every request.
_API_HEADERS = {
    "Accept": "application/vnd.github+json",
    "X-GitHub-Api-Version": "2022-11-28",
}


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


def http(method, path, query=None, body=None, headers=None):
    """Make an HTTP request via the core proxy.

    Returns (status, body, headers). Status 0 means transport error.
    GitHub API headers are automatically injected.
    """
    merged_headers = dict(_API_HEADERS)
    if headers:
        merged_headers.update(headers)

    msg = {
        "id": _gen_id("http"),
        "type": "http_request",
        "method": method,
        "path": path,
        "headers": merged_headers,
    }
    if query:
        msg["query"] = query
    if body is not None:
        msg["body"] = body
    _send(msg)
    resp = _recv()
    status = resp.get("status", 0)
    if status == 0:
        return 0, {"error": resp.get("error", "request failed")}, {}
    return status, resp.get("body", {}), resp.get("headers", {})


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


def cache_flush():
    """Flush all entries in this plugin's cache namespace."""
    _send({"id": _gen_id("cache"), "type": "cache_flush"})
    _recv()  # consume ack


def invalidate_cache():
    """Flush plugin cache after a write operation (best-effort)."""
    try:
        cache_flush()
    except Exception as e:
        log(f"cache invalidation failed (best-effort): {e}")


def _discover_username():
    """Get the authenticated user's login name from /user."""
    status, body, _ = http("GET", "/user")
    if status < 200 or status >= 300 or not isinstance(body, dict):
        return ""
    return body.get("login", "")


def _init(msg):
    """Handle init message: store config, discover username."""
    global config, username
    config = msg.get("config", {})
    username = _discover_username()
    if not username:
        log(
            "WARNING: failed to discover username from /user — "
            "search queries requiring username will not work. "
            "Check that GITHUB_TOKEN is valid."
        )
    log(f"init: username={username}, url={config.get('github_url', '?')}")


def log(message):
    """Write a log message to stderr (captured by core)."""
    print(f"github: {message}", file=sys.stderr)


def main():
    from tools import TOOLS
    from tools_write import WRITE_TOOLS

    TOOLS.update(WRITE_TOOLS)

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
