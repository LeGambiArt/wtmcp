#!/usr/bin/env python3
"""Snyk plugin handler.

Persistent handler with init/shutdown lifecycle. All HTTP calls go
through the core proxy. Auth is handled by the core via the bearer
provider with prefix "token".

Zero dependencies beyond Python stdlib.
"""

import json
import sys

_next_id = 0
config = {}

REST_VERSION = "2024-10-15"


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
    print(f"snyk: {message}", file=sys.stderr)


def http(method, path, query=None, body=None, headers=None, url=None):
    """Make an HTTP request via the core proxy."""
    msg = {
        "id": _gen_id("http"),
        "type": "http_request",
        "method": method,
        "path": path,
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
    body = resp.get("body", {})
    if resp.get("body_encoding") == "base64" and isinstance(body, str):
        import base64
        body = json.loads(base64.b64decode(body))
    elif isinstance(body, str):
        try:
            body = json.loads(body)
        except (json.JSONDecodeError, ValueError):
            pass
    return status, body, resp.get("headers", {})


# ---------- REST API helpers ----------

def _rest_get(path, params=None):
    """GET from the Snyk REST API with versioning."""
    query = {"version": REST_VERSION}
    if params:
        query.update(params)
    full_path = f"/rest/{path}" if not path.startswith("/rest/") else path
    status, body, _ = http("GET", full_path, query=query)
    if status < 200 or status >= 300:
        err = body if isinstance(body, dict) else {"status": status}
        raise RuntimeError(f"REST API error ({status}): {json.dumps(err)}")
    return body


def _rest_get_paginated(path, params=None):
    """GET all pages from a Snyk REST API endpoint."""
    items = []
    query = dict(params) if params else {}
    query.setdefault("limit", "100")

    data = _rest_get(path, query)
    items.extend(data.get("data", []))

    while True:
        next_link = (data.get("links") or {}).get("next")
        if not next_link:
            break
        next_url = next_link if next_link.startswith("http") else None
        if next_url:
            status, data, _ = http("GET", "", url=next_url)
            if status < 200 or status >= 300:
                break
        else:
            data = _rest_get(next_link)
        items.extend(data.get("data", []))

    return items


def _v1_request(method, path, body=None):
    """Make a request to the Snyk V1 API."""
    full_path = f"/v1/{path}" if not path.startswith("/v1/") else path
    headers = {"Content-Type": "application/json"}
    status, resp_body, _ = http(method, full_path, body=body, headers=headers)
    if status < 200 or status >= 300:
        err = resp_body if isinstance(resp_body, dict) else {"status": status}
        raise RuntimeError(f"V1 API error ({status}): {json.dumps(err)}")
    return resp_body


# ---------- Tool implementations ----------

def snyk_list_orgs(params):
    items = _rest_get_paginated("orgs")
    orgs = [
        {
            "id": o.get("id"),
            "name": (o.get("attributes") or {}).get("name"),
            "slug": (o.get("attributes") or {}).get("slug"),
        }
        for o in items
    ]
    return {"count": len(orgs), "orgs": orgs}


def snyk_list_projects(params):
    org_id = params["org_id"]
    query = {}
    name_filter = params.get("name_filter")
    if name_filter:
        query["names"] = name_filter

    items = _rest_get_paginated(f"orgs/{org_id}/projects", query)
    projects = [
        {
            "id": p.get("id"),
            "name": (p.get("attributes") or {}).get("name"),
            "type": (p.get("attributes") or {}).get("type"),
            "origin": (p.get("attributes") or {}).get("origin"),
            "status": (p.get("attributes") or {}).get("status"),
        }
        for p in items
    ]

    if name_filter:
        lower_filter = name_filter.lower()
        projects = [p for p in projects if lower_filter in (p.get("name") or "").lower()]

    return {"count": len(projects), "projects": projects}


def snyk_list_issues(params):
    org_id = params["org_id"]
    query = {}
    if params.get("project_id"):
        query["scan_item.id"] = params["project_id"]
        query["scan_item.type"] = "project"
    if params.get("severity"):
        query["severity"] = params["severity"]
    if params.get("issue_type"):
        query["type"] = params["issue_type"]

    items = _rest_get_paginated(f"orgs/{org_id}/issues", query)
    issues = []
    for i in items:
        attrs = i.get("attributes") or {}
        issues.append({
            "id": i.get("id"),
            "title": attrs.get("title"),
            "severity": attrs.get("effective_severity_level") or attrs.get("severity"),
            "status": attrs.get("status"),
            "type": attrs.get("type"),
            "ignored": attrs.get("ignored"),
            "key": attrs.get("key"),
        })

    return {"count": len(issues), "issues": issues}


def snyk_get_issue(params):
    org_id = params["org_id"]
    issue_id = params["issue_id"]
    data = _rest_get(f"orgs/{org_id}/issues/{issue_id}")
    return data.get("data", data)


def snyk_list_ignores(params):
    org_id = params["org_id"]
    project_id = params["project_id"]
    return _v1_request("GET", f"org/{org_id}/project/{project_id}/ignores")


def snyk_ignore_issue(params):
    org_id = params["org_id"]
    project_id = params["project_id"]
    issue_id = params["issue_id"]
    reason = params["reason"]
    reason_type = params.get("reason_type", "not-vulnerable")
    dry_run = params.get("dry_run", True)

    valid_types = ("not-vulnerable", "wont-fix", "temporary-ignore")
    if reason_type not in valid_types:
        raise ValueError(f"Invalid reason_type '{reason_type}'. Must be one of: {', '.join(valid_types)}")

    body = {
        "ignorePath": "*",
        "reason": reason,
        "reasonType": reason_type,
        "disregardIfFixable": False,
    }

    if dry_run:
        return {
            "dry_run": True,
            "action": "snyk_ignore_issue",
            "org_id": org_id,
            "project_id": project_id,
            "issue_id": issue_id,
            "body": body,
        }

    data = _v1_request("POST", f"org/{org_id}/project/{project_id}/ignore/{issue_id}", body)
    return {"success": True, "issue_id": issue_id, "response": data}


def snyk_delete_ignore(params):
    org_id = params["org_id"]
    project_id = params["project_id"]
    issue_id = params["issue_id"]
    dry_run = params.get("dry_run", True)

    if dry_run:
        return {
            "dry_run": True,
            "action": "snyk_delete_ignore",
            "org_id": org_id,
            "project_id": project_id,
            "issue_id": issue_id,
        }

    _v1_request("DELETE", f"org/{org_id}/project/{project_id}/ignore/{issue_id}")
    return {"success": True, "issue_id": issue_id}


# ---------- Tool registry ----------

TOOLS = {
    "snyk_list_orgs": snyk_list_orgs,
    "snyk_list_projects": snyk_list_projects,
    "snyk_list_issues": snyk_list_issues,
    "snyk_get_issue": snyk_get_issue,
    "snyk_list_ignores": snyk_list_ignores,
    "snyk_ignore_issue": snyk_ignore_issue,
    "snyk_delete_ignore": snyk_delete_ignore,
}


# ---------- Main loop ----------

def _init(msg):
    global config
    config = msg.get("config", {})
    log(f"init: api_version={config.get('api_version', REST_VERSION)}")


def main():
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
                _send({
                    "id": msg_id,
                    "type": "tool_result",
                    "error": {"code": "unknown_tool", "message": f"Unknown: {tool}"},
                })
                continue

            try:
                result = handler_fn(msg.get("params", {}))
                _send({"id": msg_id, "type": "tool_result", "result": result})
            except Exception as e:
                log(f"error in {tool}: {e}")
                _send({
                    "id": msg_id,
                    "type": "tool_result",
                    "error": {"code": "handler_error", "message": str(e)},
                })
            continue

        log(f"unknown message type: {msg_type}")


if __name__ == "__main__":
    main()
