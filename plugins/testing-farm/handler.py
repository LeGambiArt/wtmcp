#!/usr/bin/env python3
"""Testing Farm plugin handler.

Persistent handler with init/shutdown lifecycle. All HTTP calls go
through the core proxy (no HTTP libraries needed). Auth is handled
by the core. Cache is provided by the core cache service.

Zero dependencies beyond Python stdlib.
"""

import base64
import glob as globmod
import hashlib
import json
import os
import re
import sys
import xml.etree.ElementTree as ET

_next_id = 0

# Plugin state set during init.
config = {}
ssh_keys = []

API_VERSION = "v0.1"


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
    resp_body = resp.get("body", {})
    resp_headers = resp.get("headers", {})
    if resp.get("body_encoding") == "base64" and isinstance(resp_body, str):
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
    """Delete a value from the core cache."""
    _send({"id": _gen_id("cache"), "type": "cache_del", "key": key})
    _recv()  # consume ack


def log(message):
    """Write a log message to stderr (captured by core)."""
    print(f"testing-farm: {message}", file=sys.stderr)


# ---------------------------------------------------------------------------
# SSH key discovery
# ---------------------------------------------------------------------------


def _discover_ssh_keys():
    """Discover SSH public keys from ~/.ssh/ or config override."""
    keys = []
    key_path = config.get("ssh_key_path", "")

    if key_path:
        try:
            with open(key_path, "r") as f:
                content = f.read().strip()
                if content:
                    keys.append(content)
        except OSError as e:
            log(f"failed to read SSH key from {key_path}: {e}")
    else:
        home = os.path.expanduser("~")
        for path in sorted(globmod.glob(os.path.join(home, ".ssh", "id_*.pub"))):
            try:
                with open(path, "r") as f:
                    content = f.read().strip()
                    if content:
                        keys.append(content)
            except OSError:
                continue

    return keys


# ---------------------------------------------------------------------------
# Read tools
# ---------------------------------------------------------------------------


def testing_farm_about(_params):
    """Get Testing Farm API version and metadata."""
    cached = cache_get("tf:about")
    if cached:
        return cached

    status, body, _ = http("GET", f"/{API_VERSION}/about")
    if status != 200:
        raise Exception(f"API error (HTTP {status}): {body}")

    cache_set("tf:about", body, ttl=3600)
    return body


def testing_farm_whoami(_params):
    """Get info about the authenticated token/user."""
    cached = cache_get("tf:whoami")
    if cached:
        return cached

    status, body, _ = http("GET", f"/{API_VERSION}/me")
    if status != 200:
        raise Exception(f"API error (HTTP {status}): {body}")

    cache_set("tf:whoami", body, ttl=3600)
    return body


def testing_farm_list_requests(params):
    """List test requests with optional filters."""
    query = {}
    limit = params.get("limit", 10)
    if limit:
        query["limit"] = min(int(limit), 100)

    state = params.get("state")
    if state:
        query["state"] = state

    created_after = params.get("created_after")
    if created_after:
        query["created_after"] = created_after

    key_parts = f"{state or ''}/{limit}/{created_after or ''}"
    cache_key = f"tf:list:{hashlib.sha256(key_parts.encode()).hexdigest()[:12]}"
    cached = cache_get(cache_key)
    if cached:
        return cached

    status, body, _ = http("GET", f"/{API_VERSION}/requests", query=query)
    if status != 200:
        raise Exception(f"API error (HTTP {status}): {body}")

    if not isinstance(body, list):
        return body

    results = []
    for req in body:
        envs = req.get("environments_requested", [])
        env0 = envs[0] if envs else {}
        compose = env0.get("os", {}).get("compose", "")
        arch = env0.get("arch", "")
        test_name = req.get("test", {}).get("fmf", {}).get("name", "")

        results.append(
            {
                "id": req.get("id", ""),
                "state": req.get("state", ""),
                "result": _extract_result(req),
                "compose": compose,
                "arch": arch,
                "test_name": test_name,
                "created": req.get("created", ""),
            }
        )

    result = {"requests": results, "count": len(results)}
    cache_set(cache_key, result, ttl=60)
    return result


def _extract_result(req):
    """Extract overall result from a request."""
    result = req.get("result", {})
    if isinstance(result, dict):
        return result.get("overall", "unknown")
    return str(result) if result else "unknown"


def testing_farm_get_request(params):
    """Get detailed status of a test request."""
    request_id = params["request_id"]

    cached = cache_get(f"tf:request:{request_id}")
    if cached:
        return cached

    status, body, _ = http("GET", f"/{API_VERSION}/requests/{request_id}")
    if status != 200:
        raise Exception(f"API error (HTTP {status}): {body}")

    envs = body.get("environments_requested", [])
    env0 = envs[0] if envs else {}

    run = body.get("run", {}) or {}
    artifacts_url = run.get("artifacts", "")

    result = {
        "id": body.get("id", ""),
        "state": body.get("state", ""),
        "result": _extract_result(body),
        "compose": env0.get("os", {}).get("compose", ""),
        "arch": env0.get("arch", ""),
        "test_name": body.get("test", {}).get("fmf", {}).get("name", ""),
        "test_url": body.get("test", {}).get("fmf", {}).get("url", ""),
        "created": body.get("created", ""),
        "updated": body.get("updated", ""),
        "artifacts_url": artifacts_url,
        "run_log": run.get("log", ""),
        "run_stages": [{"name": s.get("name", ""), "status": s.get("status", "")} for s in (run.get("stages") or [])],
    }

    if result["state"] in ("complete", "error"):
        cache_set(f"tf:request:{request_id}", result, ttl=3600)

    return result


def testing_farm_list_composes(_params):
    """List available OS composes."""
    cached = cache_get("tf:composes")
    if cached:
        return cached

    status, body, _ = http("GET", f"/{API_VERSION}/composes")
    if status != 200:
        raise Exception(f"API error (HTTP {status}): {body}")

    # Trim to compose names only — the raw response includes metadata
    # (allowed_arches, tags, etc.) that wastes LLM context tokens.
    if isinstance(body, dict):
        trimmed = {}
        for group, composes in body.items():
            if isinstance(composes, list):
                trimmed[group] = [c.get("name", c) if isinstance(c, dict) else c for c in composes]
            else:
                trimmed[group] = composes
        body = trimmed

    cache_set("tf:composes", body, ttl=14400)
    return body


def testing_farm_list_reservations(params):
    """List running reservations."""
    limit = params.get("limit", 20)

    cache_key = f"tf:reservations:{limit}"
    cached = cache_get(cache_key)
    if cached:
        return cached

    query = {"limit": min(int(limit), 100)}

    status, body, _ = http("GET", f"/{API_VERSION}/requests", query=query)
    if status != 200:
        raise Exception(f"API error (HTTP {status}): {body}")

    if not isinstance(body, list):
        return body

    results = []
    for req in body:
        test_name = req.get("test", {}).get("fmf", {}).get("name", "")
        if "/testing-farm/reserve" not in test_name:
            continue

        envs = req.get("environments_requested", [])
        env0 = envs[0] if envs else {}
        compose = env0.get("os", {}).get("compose", "")
        arch = env0.get("arch", "")
        duration = env0.get("variables", {}).get("TF_RESERVATION_DURATION", "")

        results.append(
            {
                "id": req.get("id", ""),
                "state": req.get("state", ""),
                "result": _extract_result(req),
                "compose": compose,
                "arch": arch,
                "duration_min": duration,
                "created": req.get("created", ""),
            }
        )

    result = {"reservations": results, "count": len(results)}
    cache_set(cache_key, result, ttl=60)
    return result


def testing_farm_get_ssh(params):
    """Extract SSH connection info for a reservation."""
    request_id = params["request_id"]

    cached = cache_get(f"tf:ssh:{request_id}")
    if cached:
        return cached

    # Get request details.
    status, body, _ = http("GET", f"/{API_VERSION}/requests/{request_id}")
    if status != 200:
        raise Exception(f"API error (HTTP {status}): {body}")

    run = body.get("run", {}) or {}
    artifacts_url = run.get("artifacts", "")
    if not artifacts_url:
        raise Exception("No artifacts URL found — request may still be queued")

    state = body.get("state", "")
    if state not in ("running", "complete"):
        return {
            "error": f"Request is in state '{state}' — SSH info is only available for running or complete reservations",
            "state": state,
            "request_id": request_id,
        }

    envs = body.get("environments_requested", [])
    env0 = envs[0] if envs else {}

    # Fetch results.xml from artifacts.
    results_url = f"{artifacts_url}/results.xml"
    rstatus, rbody, _ = http("GET", "/", url=results_url)
    if rstatus != 200:
        raise Exception(f"Failed to fetch results.xml (HTTP {rstatus}): artifacts may not be available yet")

    # Parse results.xml to find console log.
    xml_text = rbody if isinstance(rbody, str) else rbody.decode("utf-8") if isinstance(rbody, bytes) else str(rbody)
    ip_address = _parse_ssh_from_results_xml(xml_text, artifacts_url)

    if not ip_address:
        return {
            "error": "Could not extract IP address from console logs",
            "request_id": request_id,
            "artifacts_url": artifacts_url,
            "hint": "The system may still be provisioning. Try again shortly.",
        }

    result = {
        "ip": ip_address,
        "ssh_command": f"ssh root@{ip_address}",
        "request_id": request_id,
        "state": state,
        "compose": env0.get("os", {}).get("compose", ""),
        "arch": env0.get("arch", ""),
        "artifacts_url": artifacts_url,
    }
    cache_set(f"tf:ssh:{request_id}", result, ttl=1800)
    return result


def _parse_ssh_from_results_xml(xml_text, artifacts_url):
    """Parse results.xml to find the IP address from console logs."""
    try:
        root = ET.fromstring(xml_text)
    except ET.ParseError:
        log("failed to parse results.xml as XML")
        return None

    # Find workdir log entries.
    workdir_href = None
    for log_elem in root.iter("log"):
        name = log_elem.get("name", "")
        if name == "workdir":
            workdir_href = log_elem.get("href", "")
            break

    if not workdir_href:
        # Fallback: try to find any console log reference.
        for log_elem in root.iter("log"):
            href = log_elem.get("href", "")
            if "console" in href.lower():
                return _fetch_and_parse_console_log(href)
        return None

    # List workdir to find console-*.log files.
    dir_url = workdir_href if workdir_href.startswith("http") else f"{artifacts_url}/{workdir_href}"
    dstatus, dbody, _ = http("GET", "/", url=dir_url)
    if dstatus != 200:
        return None

    # Parse directory listing for console log files.
    dir_text = dbody if isinstance(dbody, str) else dbody.decode("utf-8") if isinstance(dbody, bytes) else str(dbody)
    console_logs = re.findall(r'href="(console-[^"]+\.log)"', dir_text)

    if not console_logs:
        # Try data/ subdirectory pattern.
        data_entries = re.findall(r'href="(data/[^"]*)"', dir_text)
        for entry in data_entries:
            entry_url = f"{dir_url.rstrip('/')}/{entry}"
            estatus, ebody, _ = http("GET", "/", url=entry_url)
            if estatus == 200:
                entry_text = (
                    ebody
                    if isinstance(ebody, str)
                    else ebody.decode("utf-8")
                    if isinstance(ebody, bytes)
                    else str(ebody)
                )
                console_logs = re.findall(r'href="(console-[^"]+\.log)"', entry_text)
                if console_logs:
                    dir_url = entry_url
                    break

    for console_log in console_logs:
        log_url = f"{dir_url.rstrip('/')}/{console_log}"
        ip = _fetch_and_parse_console_log(log_url)
        if ip:
            return ip

    return None


def _fetch_and_parse_console_log(log_url):
    """Fetch a console log and parse the IP address."""
    lstatus, lbody, _ = http("GET", "/", url=log_url)
    if lstatus != 200:
        return None

    log_text = lbody if isinstance(lbody, str) else lbody.decode("utf-8") if isinstance(lbody, bytes) else str(lbody)
    return _extract_ip_from_console(log_text)


def _extract_ip_from_console(text):
    """Extract IP address from console log text.

    Tries cloud-init table format first, then falls back to other
    patterns used on different architectures.
    """
    # Pattern 1: cloud-init network table.
    # ci-info: | eth0 | True | 10.0.0.1 | ... | global |
    for match in re.finditer(
        r"ci-info:\s*\|\s*\S+\s*\|\s*True\s*\|\s*"
        r"(\d+\.\d+\.\d+\.\d+)\s*\|.*?\|\s*global\s*\|",
        text,
    ):
        return match.group(1)

    # Pattern 2: "Using IPv4 address: IP" (s390x and other arches).
    match = re.search(r"Using IPv4 address:\s*(\d+\.\d+\.\d+\.\d+)", text)
    if match:
        return match.group(1)

    # Pattern 3: reservation SSH info line.
    match = re.search(r"ssh\s+root@(\d+\.\d+\.\d+\.\d+)", text)
    if match:
        return match.group(1)

    return None


def testing_farm_get_results(params):
    """Get test results for a completed request."""
    request_id = params["request_id"]

    cached = cache_get(f"tf:results:{request_id}")
    if cached:
        return cached

    status, body, _ = http("GET", f"/{API_VERSION}/requests/{request_id}")
    if status != 200:
        raise Exception(f"API error (HTTP {status}): {body}")

    result = body.get("result", {})
    xunit = result.get("xunit", "")

    summary = {
        "request_id": request_id,
        "state": body.get("state", ""),
        "overall": result.get("overall", "unknown") if isinstance(result, dict) else str(result),
    }

    if xunit:
        tests = _parse_xunit(xunit)
        summary["tests"] = tests
        summary["test_count"] = len(tests)

    if body.get("state") == "complete":
        cache_set(f"tf:results:{request_id}", summary, ttl=3600)

    return summary


def _parse_xunit(xunit_xml):
    """Parse xunit XML into a list of test results."""
    tests = []
    try:
        root = ET.fromstring(xunit_xml)
        for tc in root.iter("testcase"):
            test = {
                "name": tc.get("name", ""),
            }
            failure = tc.find("failure")
            error_elem = tc.find("error")
            if failure is not None:
                test["result"] = "failure"
                test["message"] = failure.get("message", "")
            elif error_elem is not None:
                test["result"] = "error"
                test["message"] = error_elem.get("message", "")
            elif tc.find("skipped") is not None:
                test["result"] = "skipped"
            else:
                test["result"] = "passed"
            tests.append(test)
    except ET.ParseError:
        log("failed to parse xunit XML")
    return tests


def testing_farm_get_logs(params):
    """Get log URLs for a request."""
    request_id = params["request_id"]

    cached = cache_get(f"tf:logs:{request_id}")
    if cached:
        return cached

    status, body, _ = http("GET", f"/{API_VERSION}/requests/{request_id}")
    if status != 200:
        raise Exception(f"API error (HTTP {status}): {body}")

    run = body.get("run", {}) or {}
    artifacts_url = run.get("artifacts", "")

    result = {
        "request_id": request_id,
        "state": body.get("state", ""),
        "pipeline_log": run.get("log", ""),
        "artifacts_url": artifacts_url,
    }

    if result["state"] in ("complete", "error"):
        cache_set(f"tf:logs:{request_id}", result, ttl=3600)

    return result


# ---------------------------------------------------------------------------
# Write tools
# ---------------------------------------------------------------------------


def testing_farm_reserve(params):
    """Reserve a system on Testing Farm."""
    compose = params["compose"]
    arch = params["arch"]
    duration = params.get("duration", 60)
    hardware = params.get("hardware")
    extra_keys = params.get("ssh_keys", "")
    dry_run = params.get("dry_run", True)

    # Combine auto-discovered keys with any extra keys.
    all_keys = list(ssh_keys)
    if extra_keys:
        for line in extra_keys.strip().splitlines():
            line = line.strip()
            if line and line not in all_keys:
                all_keys.append(line)

    if not all_keys:
        raise Exception("No SSH public keys found. Provide ssh_keys parameter or ensure ~/.ssh/id_*.pub files exist.")

    keys_blob = "\n".join(all_keys)
    keys_b64 = base64.b64encode(keys_blob.encode("utf-8")).decode("ascii")

    environment = {
        "os": {"compose": compose},
        "arch": arch,
        "variables": {
            "TF_RESERVATION_DURATION": str(duration),
        },
        "secrets": {
            "TF_RESERVATION_AUTHORIZED_KEYS_BASE64": keys_b64,
        },
        "settings": {
            "provisioning": {
                "tags": {
                    "ArtemisUseSpot": "false",
                },
            },
        },
    }

    if hardware:
        environment["hardware"] = hardware

    payload = {
        "test": {
            "fmf": {
                "url": "https://gitlab.com/testing-farm/tests",
                "ref": "main",
                "name": "/testing-farm/reserve",
            },
        },
        "environments": [environment],
    }

    if dry_run:
        return {
            "dry_run": True,
            "message": "Preview — set dry_run=false to submit",
            "payload": payload,
            "ssh_keys_count": len(all_keys),
        }

    status, body, _ = http("POST", f"/{API_VERSION}/requests", body=payload)
    if status not in (200, 201):
        raise Exception(f"API error (HTTP {status}): {body}")

    return {
        "id": body.get("id", ""),
        "state": body.get("state", ""),
        "message": (
            "Reservation submitted. Use testing_farm_list_reservations to monitor,"
            " then testing_farm_get_ssh to get connection details."
        ),
    }


def testing_farm_submit_test(params):
    """Submit a test request to Testing Farm."""
    git_url = params["git_url"]
    git_ref = params.get("git_ref", "main")
    plan_name = params.get("plan_name")
    compose = params["compose"]
    arch = params["arch"]
    artifacts = params.get("artifacts")
    env_vars = params.get("env_vars")
    timeout = params.get("timeout")
    dry_run = params.get("dry_run", True)

    fmf = {
        "url": git_url,
        "ref": git_ref,
    }
    if plan_name:
        fmf["name"] = plan_name

    environment = {
        "os": {"compose": compose},
        "arch": arch,
    }

    if artifacts:
        environment["artifacts"] = artifacts

    if env_vars:
        environment["variables"] = env_vars

    payload = {
        "test": {"fmf": fmf},
        "environments": [environment],
    }

    if timeout:
        payload["settings"] = {"pipeline": {"timeout": int(timeout) * 60}}

    if dry_run:
        return {
            "dry_run": True,
            "message": "Preview — set dry_run=false to submit",
            "payload": payload,
        }

    status, body, _ = http("POST", f"/{API_VERSION}/requests", body=payload)
    if status not in (200, 201):
        raise Exception(f"API error (HTTP {status}): {body}")

    return {
        "id": body.get("id", ""),
        "state": body.get("state", ""),
        "message": "Test submitted. Use testing_farm_get_request to monitor progress.",
    }


def testing_farm_cancel(params):
    """Cancel a test request or release a reservation."""
    request_id = params["request_id"]

    status, body, _ = http("DELETE", f"/{API_VERSION}/requests/{request_id}")
    if status not in (200, 204):
        raise Exception(f"API error (HTTP {status}): {body}")

    # Invalidate cached data for this request.
    for prefix in ("tf:request:", "tf:ssh:", "tf:results:", "tf:logs:"):
        cache_del(f"{prefix}{request_id}")

    return {
        "request_id": request_id,
        "message": "Request cancelled/deleted successfully",
    }


# ---------------------------------------------------------------------------
# Main loop
# ---------------------------------------------------------------------------

TOOLS = {
    # Read tools
    "testing_farm_about": testing_farm_about,
    "testing_farm_whoami": testing_farm_whoami,
    "testing_farm_list_requests": testing_farm_list_requests,
    "testing_farm_get_request": testing_farm_get_request,
    "testing_farm_list_composes": testing_farm_list_composes,
    "testing_farm_list_reservations": testing_farm_list_reservations,
    "testing_farm_get_ssh": testing_farm_get_ssh,
    "testing_farm_get_results": testing_farm_get_results,
    "testing_farm_get_logs": testing_farm_get_logs,
    # Write tools
    "testing_farm_reserve": testing_farm_reserve,
    "testing_farm_submit_test": testing_farm_submit_test,
    "testing_farm_cancel": testing_farm_cancel,
}


def _init(msg):
    """Handle init message: store config, discover SSH keys."""
    global config, ssh_keys
    config = msg.get("config", {})
    ssh_keys = _discover_ssh_keys()
    log(f"init: api_url={config.get('api_url', '?')}, ssh_keys={len(ssh_keys)}")


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
