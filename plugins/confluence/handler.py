#!/usr/bin/env python3
"""Confluence plugin handler.

Persistent handler with init/shutdown lifecycle. All HTTP calls go
through the core proxy (no HTTP libraries needed). Auth is handled
by the core. Cache is provided by the core cache service.

Zero dependencies beyond Python stdlib.
"""

import hashlib
import json
import sys

_next_id = 0
_current_call_id = None
config = {}


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
    """
    msg = {
        "id": _gen_id("http"),
        "type": "http_request",
        "method": method,
        "path": path,
    }
    if _current_call_id:
        msg["parent_id"] = _current_call_id
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
    """Get a value from the core cache. Returns value or None."""
    msg = {"id": _gen_id("cache"), "type": "cache_get", "key": key}
    if _current_call_id:
        msg["parent_id"] = _current_call_id
    _send(msg)
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
    if _current_call_id:
        msg["parent_id"] = _current_call_id
    if ttl is not None:
        msg["ttl"] = ttl
    _send(msg)
    _recv()  # consume ack


def log(message):
    """Write a log message to stderr (captured by core)."""
    print(f"confluence: {message}", file=sys.stderr)


# --- Response helpers ---


def _strip(obj):
    """Recursively remove _links, _expandable, and _extensions from responses."""
    if isinstance(obj, dict):
        return {k: _strip(v) for k, v in obj.items() if k not in ("_links", "_expandable", "_extensions")}
    if isinstance(obj, list):
        return [_strip(item) for item in obj]
    return obj


def _extract_page(page, include_body=True):
    """Extract essential fields from a raw Confluence page response."""
    result = {
        "id": page.get("id"),
        "title": page.get("title"),
        "status": page.get("status"),
    }

    space = page.get("space")
    if space:
        result["space_key"] = space.get("key")
        result["space_name"] = space.get("name")

    version = page.get("version")
    if version:
        result["version"] = version.get("number")
        result["last_modified"] = version.get("when")
        by = version.get("by")
        if by:
            result["modified_by"] = by.get("displayName", by.get("username", ""))

    links = page.get("_links", {})
    webui = links.get("webui", "")
    base = links.get("base", "")
    if webui:
        result["url"] = base + webui if base else webui

    if include_body:
        body = page.get("body", {})
        storage = body.get("storage", {})
        if storage.get("value"):
            result["body"] = storage["value"]

    return result


def _extract_space(space, full_description=False):
    """Extract essential fields from a raw Confluence space response."""
    result = {
        "key": space.get("key"),
        "name": space.get("name"),
        "type": space.get("type"),
    }

    desc = space.get("description", {})
    plain = desc.get("plain", {}).get("value", "")
    if plain:
        if full_description:
            result["description"] = plain
        elif len(plain) > 100:
            result["description"] = plain[:100] + "..."
        else:
            result["description"] = plain

    homepage = space.get("homepage")
    if homepage:
        result["homepage_id"] = homepage.get("id")
        result["homepage_title"] = homepage.get("title")

    return result


def _extract_search_result(item):
    """Extract brief info from a search result."""
    content = item.get("content", item)
    result = {
        "id": content.get("id"),
        "title": content.get("title"),
        "type": content.get("type"),
    }
    space = content.get("space")
    if space:
        result["space_key"] = space.get("key")

    links = content.get("_links", {})
    webui = links.get("webui", "")
    base = item.get("_links", {}).get("base", links.get("base", ""))
    if webui:
        result["url"] = base + webui if base else webui

    last_modified = content.get("lastModified") or item.get("lastModified")
    if last_modified:
        result["last_modified"] = last_modified

    excerpt = item.get("excerpt", "")
    if excerpt:
        result["excerpt"] = excerpt[:200]

    return result


def _extract_write_result(body):
    """Extract confirmation from a write operation response."""
    result = {
        "id": body.get("id"),
        "title": body.get("title"),
    }
    version = body.get("version")
    if version:
        result["version"] = version.get("number")

    links = body.get("_links", {})
    webui = links.get("webui", "")
    base = links.get("base", "")
    if webui:
        result["url"] = base + webui if base else webui

    return result


# --- Tools ---


def confluence_get_page(params):
    page_id = params.get("page_id")
    if not page_id:
        raise ValueError("page_id is required")

    include_body = params.get("include_body", True)
    expand = "version,space"
    if include_body:
        expand = "body.storage,version,space"

    status, body, _ = http(
        "GET",
        f"/rest/api/content/{page_id}",
        query={"expand": expand},
    )
    if status >= 400:
        return {"error": f"HTTP {status}", "details": _strip(body)}
    return _extract_page(body, include_body=include_body)


def confluence_get_page_by_title(params):
    title = params.get("title")
    space_key = params.get("space_key")
    if not title or not space_key:
        raise ValueError("title and space_key are required")

    include_body = params.get("include_body", True)
    expand = "version,space"
    if include_body:
        expand = "body.storage,version,space"

    status, body, _ = http(
        "GET",
        "/rest/api/content",
        query={
            "title": title,
            "spaceKey": space_key,
            "expand": expand,
            "limit": 1,
        },
    )
    if status >= 400:
        return {"error": f"HTTP {status}", "details": _strip(body)}

    results = body.get("results", [])
    if not results:
        return {"error": f"No page found with title '{title}' in space '{space_key}'"}
    return _extract_page(results[0], include_body=include_body)


def confluence_search(params):
    cql = params.get("cql")
    if not cql:
        raise ValueError("cql is required")
    limit = params.get("limit", 10)

    # Check cache
    cache_key = f"search:{hashlib.sha256(cql.encode()).hexdigest()[:12]}"
    cached = cache_get(cache_key)
    if cached:
        return cached

    status, body, _ = http(
        "GET",
        "/rest/api/content/search",
        query={"cql": cql, "limit": limit},
    )
    if status >= 400:
        return {"error": f"HTTP {status}", "details": _strip(body)}

    results = body.get("results", [])
    result = {
        "results": [_extract_search_result(r) for r in results],
        "total": body.get("totalSize", len(results)),
        "count": len(results),
    }
    cache_set(cache_key, result, ttl=300)
    return result


def confluence_get_pages_by_title(params):
    title = params.get("title")
    space_key = params.get("space_key")
    if not title or not space_key:
        raise ValueError("title and space_key are required")

    status, body, _ = http(
        "GET",
        "/rest/api/content",
        query={
            "title": title,
            "spaceKey": space_key,
            "expand": "version",
        },
    )
    if status >= 400:
        return {"error": f"HTTP {status}", "details": _strip(body)}

    results = body.get("results", [])
    return {
        "results": [_extract_page(r, include_body=False) for r in results],
        "count": len(results),
    }


def confluence_get_spaces(params):
    limit = params.get("limit", 20)

    # Check cache
    cached = cache_get("spaces")
    if cached:
        return cached

    status, body, _ = http(
        "GET",
        "/rest/api/space",
        query={"limit": limit, "expand": "description"},
    )
    if status >= 400:
        return {"error": f"HTTP {status}", "details": _strip(body)}

    results = body.get("results", [])
    result = {
        "spaces": [_extract_space(s) for s in results],
        "count": len(results),
    }
    cache_set("spaces", result, ttl=1800)
    return result


def confluence_get_space(params):
    space_key = params.get("space_key")
    if not space_key:
        raise ValueError("space_key is required")

    # Check cache
    cache_key = f"space:{space_key}"
    cached = cache_get(cache_key)
    if cached:
        return cached

    status, body, _ = http(
        "GET",
        f"/rest/api/space/{space_key}",
        query={"expand": "description,homepage"},
    )
    if status >= 400:
        return {"error": f"HTTP {status}", "details": _strip(body)}

    result = _extract_space(body, full_description=True)
    cache_set(cache_key, result, ttl=1800)
    return result


def confluence_create_page(params):
    space_key = params.get("space_key")
    title = params.get("title")
    body_html = params.get("body")
    parent_id = params.get("parent_id")
    dry_run = params.get("dry_run", True)

    if not space_key or not title or not body_html:
        raise ValueError("space_key, title, and body are required")

    if dry_run:
        preview = body_html[:200] + "..." if len(body_html) > 200 else body_html
        return {
            "dry_run": True,
            "action": "confluence_create_page",
            "space_key": space_key,
            "title": title,
            "parent_id": parent_id,
            "body_preview": preview,
        }

    payload = {
        "type": "page",
        "title": title,
        "space": {"key": space_key},
        "body": {
            "storage": {
                "value": body_html,
                "representation": "storage",
            }
        },
    }
    if parent_id:
        payload["ancestors"] = [{"id": parent_id}]

    status, body, _ = http("POST", "/rest/api/content", body=payload)
    if status >= 400:
        return {"error": f"HTTP {status}", "details": _strip(body)}
    return _extract_write_result(body)


def confluence_update_page(params):
    page_id = params.get("page_id")
    title = params.get("title")
    body_html = params.get("body")
    dry_run = params.get("dry_run", True)

    if not page_id or not title or not body_html:
        raise ValueError("page_id, title, and body are required")

    if dry_run:
        preview = body_html[:200] + "..." if len(body_html) > 200 else body_html
        return {
            "dry_run": True,
            "action": "confluence_update_page",
            "page_id": page_id,
            "title": title,
            "body_preview": preview,
        }

    # Fetch current version number
    status, current, _ = http(
        "GET",
        f"/rest/api/content/{page_id}",
        query={"expand": "version"},
    )
    if status >= 400:
        return {"error": f"HTTP {status} fetching current version", "details": _strip(current)}

    current_version = current.get("version", {}).get("number", 0)

    payload = {
        "version": {"number": current_version + 1},
        "title": title,
        "type": "page",
        "body": {
            "storage": {
                "value": body_html,
                "representation": "storage",
            }
        },
    }

    status, body, _ = http("PUT", f"/rest/api/content/{page_id}", body=payload)
    if status >= 400:
        return {"error": f"HTTP {status}", "details": _strip(body)}
    return _extract_write_result(body)


def confluence_add_comment(params):
    page_id = params.get("page_id")
    comment = params.get("comment")
    dry_run = params.get("dry_run", True)

    if not page_id or not comment:
        raise ValueError("page_id and comment are required")

    if dry_run:
        return {
            "dry_run": True,
            "action": "confluence_add_comment",
            "page_id": page_id,
            "comment_preview": comment[:200],
        }

    payload = {
        "type": "comment",
        "container": {"id": page_id, "type": "page"},
        "body": {
            "storage": {
                "value": comment,
                "representation": "storage",
            }
        },
    }

    status, body, _ = http("POST", "/rest/api/content", body=payload)
    if status >= 400:
        return {"error": f"HTTP {status}", "details": _strip(body)}
    return _extract_write_result(body)


def confluence_get_page_children(params):
    page_id = params.get("page_id")
    if not page_id:
        raise ValueError("page_id is required")

    # Check cache
    cache_key = f"children:{page_id}"
    cached = cache_get(cache_key)
    if cached:
        return cached

    status, body, _ = http("GET", f"/rest/api/content/{page_id}/child/page")
    if status >= 400:
        return {"error": f"HTTP {status}", "details": _strip(body)}

    if isinstance(body, dict):
        raw = body.get("results", [])
    elif isinstance(body, list):
        raw = body
    else:
        raw = []

    results = [{"id": c.get("id"), "title": c.get("title"), "status": c.get("status")} for c in raw]
    result = {"results": results, "count": len(results)}
    cache_set(cache_key, result, ttl=600)
    return result


def confluence_get_page_history(params):
    page_id = params.get("page_id")
    if not page_id:
        raise ValueError("page_id is required")

    status, body, _ = http("GET", f"/rest/api/content/{page_id}/history")
    if status >= 400:
        return {"error": f"HTTP {status}", "details": _strip(body)}
    return _strip(body)


# --- Tool registry ---

TOOLS = {
    "confluence_get_page": confluence_get_page,
    "confluence_get_page_by_title": confluence_get_page_by_title,
    "confluence_search": confluence_search,
    "confluence_get_pages_by_title": confluence_get_pages_by_title,
    "confluence_get_spaces": confluence_get_spaces,
    "confluence_get_space": confluence_get_space,
    "confluence_create_page": confluence_create_page,
    "confluence_update_page": confluence_update_page,
    "confluence_add_comment": confluence_add_comment,
    "confluence_get_page_children": confluence_get_page_children,
    "confluence_get_page_history": confluence_get_page_history,
}


def main():
    log("handler starting")
    global _current_call_id, config

    while True:
        msg = _recv()
        msg_id = msg.get("id")
        msg_type = msg.get("type")

        if msg_type == "init":
            _current_call_id = msg_id
            try:
                config = msg.get("config", {})
                log(f"init: url={config.get('confluence_url', '?')}")
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

        log(f"unknown message type: {msg_type}")


if __name__ == "__main__":
    sys.modules["handler"] = sys.modules[__name__]
    main()
