#!/usr/bin/env python3
"""OSIDB plugin handler.

Persistent handler with init/shutdown lifecycle. All HTTP calls go
through the core proxy (no HTTP libraries needed). Auth is handled
by the core. Cache is provided by the core cache service.

Zero dependencies beyond Python stdlib.
"""

import json
import sys

_next_id = 0

config = {}

_current_call_id = None


def _send(msg):
    print(json.dumps(msg, separators=(",", ":")), flush=True)


def _gen_id(prefix="svc"):
    global _next_id
    _next_id += 1
    return f"{prefix}-{_next_id}"


def _recv():
    line = sys.stdin.readline()
    if not line:
        sys.exit(0)
    return json.loads(line.strip())


def log(message):
    print(f"osidb: {message}", file=sys.stderr)


def http(method, path, query=None, body=None, headers=None, url=None):
    msg = {
        "id": _gen_id("http"),
        "type": "http_request",
        "method": method,
        "path": path,
    }
    if _current_call_id:
        msg["parent_id"] = _current_call_id
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
    return status, resp.get("body", {}), resp.get("headers", {})


def cache_get(key):
    msg = {"id": _gen_id("cache"), "type": "cache_get", "key": key}
    if _current_call_id:
        msg["parent_id"] = _current_call_id
    _send(msg)
    resp = _recv()
    if resp.get("hit"):
        return resp["value"]
    return None


def cache_set(key, value, ttl=None):
    msg = {
        "id": _gen_id("cache"),
        "type": "cache_set",
        "key": key,
        "value": value,
    }
    if _current_call_id:
        msg["parent_id"] = _current_call_id
    if ttl is not None:
        msg["ttl"] = ttl
    _send(msg)
    _recv()


def cache_del(key):
    msg = {"id": _gen_id("cache"), "type": "cache_del", "key": key}
    if _current_call_id:
        msg["parent_id"] = _current_call_id
    _send(msg)
    _recv()


def cache_flush():
    msg = {"id": _gen_id("cache"), "type": "cache_flush"}
    if _current_call_id:
        msg["parent_id"] = _current_call_id
    _send(msg)
    _recv()


def main():
    from tools_read import TOOLS
    from tools_write import TOOLS as WRITE_TOOLS

    all_tools = {}
    all_tools.update(TOOLS)
    all_tools.update(WRITE_TOOLS)

    log("handler starting")

    global _current_call_id

    while True:
        msg = _recv()
        msg_id = msg.get("id")
        msg_type = msg.get("type")

        if msg_type == "init":
            _current_call_id = msg_id
            try:
                config.update(msg.get("config", {}))
                log("initialized")
                _send({"id": msg_id, "type": "init_ok"})
            finally:
                _current_call_id = None
            continue

        if msg_type == "shutdown":
            log("shutting down")
            _send({"id": msg_id, "type": "shutdown_ok"})
            break

        if msg_type == "tool_call":
            tool = msg.get("tool")
            handler_fn = all_tools.get(tool)
            if not handler_fn:
                _send(
                    {
                        "id": msg_id,
                        "type": "tool_result",
                        "error": {"code": "unknown_tool", "message": f"Unknown tool: {tool}"},
                    }
                )
                continue

            _current_call_id = msg_id
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
            finally:
                _current_call_id = None
            continue


if __name__ == "__main__":
    sys.modules["handler"] = sys.modules[__name__]
    main()
