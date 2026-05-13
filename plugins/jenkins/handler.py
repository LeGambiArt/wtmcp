#!/usr/bin/env python3
"""Jenkins plugin handler.

Persistent handler with init/shutdown lifecycle. Auth and SAML SSO
are handled by the Go core proxy. Cache is provided by the core
cache service.
"""

import json
import sys
from urllib.parse import urlparse

_next_id = 0

# Plugin state set during init
config = {}
jenkins_version = "unknown"

# Runtime Jenkins URL (set per-request by tools)
_jenkins_url = ""


def _send(msg):
    """Write a JSON message to stdout (core reads it)."""
    print(json.dumps(msg, separators=(",", ":")), flush=True)


def _gen_id(prefix="svc"):
    """Generate unique ID for requests."""
    global _next_id
    _next_id += 1
    return f"{prefix}-{_next_id}"


def _recv():
    """Read a JSON message from stdin (core writes it)."""
    line = sys.stdin.readline()
    if not line:
        sys.exit(0)
    return json.loads(line.strip())


def log(msg):
    """Log to stderr (captured by core)."""
    print(f"jenkins: {msg}", file=sys.stderr, flush=True)


def validate_jenkins_url(raw_url):
    """Validate and normalize a Jenkins URL.

    Returns the normalized URL or None if invalid.
    """
    if not raw_url or not isinstance(raw_url, str):
        return None
    raw_url = raw_url.strip().rstrip("/")
    if not raw_url:
        return None
    parsed = urlparse(raw_url)
    if parsed.scheme not in ("https", "http"):
        return None
    if not parsed.hostname:
        return None
    return raw_url


def set_jenkins_url(raw_url):
    """Set the module-level Jenkins URL for subsequent http() calls.

    Returns an error dict if invalid, None on success.
    """
    global _jenkins_url
    validated = validate_jenkins_url(raw_url)
    if validated is None:
        return {"error": "invalid_input", "detail": f"invalid jenkins_url: {raw_url!r}"}
    _jenkins_url = validated
    return None


def http(method, path, query=None, body=None, headers=None, url=None):
    """Make an HTTP request via the core proxy.

    Returns (status, body, headers). Status 0 means transport error.
    When url is provided, it overrides base_url + path.
    Uses _jenkins_url to construct full URLs and register domains.
    """
    msg = {
        "id": _gen_id("http"),
        "type": "http_request",
        "method": method,
        "path": path,
    }
    if url:
        msg["url"] = url
    elif _jenkins_url:
        msg["url"] = _jenkins_url + path
        hostname = urlparse(_jenkins_url).hostname
        if hostname:
            msg["domains"] = [hostname]
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
    return status, resp_body, resp_headers


def cache_get(key):
    """Get a value from the core cache. Returns value or None."""
    _send({"id": _gen_id("cache"), "type": "cache_get", "key": key})
    resp = _recv()
    if resp.get("hit"):
        return resp["value"]
    return None


def cache_set(key, value, ttl=None):
    """Set a value in the core cache.

    Args:
        key: Cache key
        value: Value to cache (must be JSON-serializable)
        ttl: Time to live in seconds (0 or None = permanent)
    """
    msg = {
        "id": _gen_id("cache"),
        "type": "cache_set",
        "key": key,
        "value": value,
    }
    if ttl is not None:
        msg["ttl"] = ttl
    _send(msg)
    _recv()  # Wait for acknowledgment


def cache_del(key):
    """Delete a value from the core cache."""
    _send({"id": _gen_id("cache"), "type": "cache_del", "key": key})
    _recv()


def cache_flush(namespace):
    """Flush all cache entries for a namespace."""
    msg = {
        "id": _gen_id("cache"),
        "type": "cache_flush",
        "namespace": namespace,
    }
    _send(msg)
    resp = _recv()
    return resp.get("ok", False)


def _init(msg):
    """Initialize the plugin.

    Jenkins URL is now provided per-request via tool parameters,
    so init only stores config.
    """
    global config

    config = msg.get("config", {})
    log("init: ready (jenkins_url provided per-request)")


def _shutdown(msg):
    """Shutdown the plugin."""
    log("shutdown")


def main():
    """Main message loop."""
    # Import tools module and register valid tools
    import tools_read

    # Valid tools whitelist (defense-in-depth against core bugs)
    TOOLS = tools_read.TOOLS

    while True:
        msg = _recv()
        msg_type = msg.get("type", "")

        if msg_type == "init":
            _init(msg)
            _send({"id": msg.get("id"), "type": "init_ok"})
        elif msg_type == "shutdown":
            _shutdown(msg)
            _send({"id": msg.get("id"), "type": "shutdown_ok"})
            break
        elif msg_type == "tool_call":
            tool = msg.get("tool", "")
            params = msg.get("params", {})

            # Tool dispatch with whitelist check and error handling
            if tool in TOOLS:
                try:
                    func = TOOLS[tool]
                    result = func(params)
                    _send({"id": msg["id"], "type": "tool_result", "result": result})
                except Exception as e:
                    log(f"ERROR: tool {tool} failed: {e}")
                    _send(
                        {
                            "id": msg["id"],
                            "type": "tool_result",
                            "result": {"error": "handler_error", "detail": f"Tool execution failed: {str(e)}"},
                        }
                    )
            else:
                _send({"id": msg["id"], "type": "tool_result", "result": {"error": f"unknown tool: {tool}"}})
        else:
            _send({"id": msg.get("id"), "type": "error", "error": f"unknown message type: {msg_type}"})


if __name__ == "__main__":
    # Register handler module for tools_read.py imports
    sys.modules["handler"] = sys.modules[__name__]
    main()
