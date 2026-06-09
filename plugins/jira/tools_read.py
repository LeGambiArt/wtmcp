"""Jira read-only tool implementations."""

import hashlib

import handler
from helpers import (
    extract_brief_issue,
    extract_issue_fields,
    extract_user_fields,
    http_error,
    is_user_alias,
    validate_issue_key,
    validate_project_key,
)


def get_myself(_params):
    """Get authenticated user profile, cached for 1 hour."""
    cached = handler.cache_get("myself")
    if cached:
        handler.log("returning cached user profile")
        return cached

    status, body, _ = handler.http("GET", "/rest/api/2/myself")
    if status < 200 or status >= 300:
        return http_error(status, body)
    if not isinstance(body, dict):
        return http_error(status, body)

    result = extract_user_fields(body)
    handler.cache_set("myself", result, ttl=3600)
    return result


_MAX_SEARCH_PAGES = 20


def search(params):
    """Search issues using JQL with optional brief mode."""
    jql = params.get("jql", "")
    max_cap = 2000 if handler.is_cloud else 200
    max_results = min(int(params.get("max_results", 50)), max_cap)
    start_at = max(int(params.get("start_at", 0)), 0)
    fields = params.get("fields", "summary,status,assignee,priority")
    brief = params.get("brief", True)

    key_input = f"{jql}|{max_results}|{start_at}|{fields}|{brief}|{handler.is_cloud}"
    cache_key = f"search:{hashlib.sha256(key_input.encode()).hexdigest()[:12]}"
    cached = handler.cache_get(cache_key)
    if cached:
        handler.log(f"returning cached search: {jql[:60]}")
        return cached

    if handler.is_cloud:
        result = _search_cloud(jql, max_results, start_at, fields, brief)
    else:
        result = _search_server(jql, max_results, start_at, fields, brief)

    if "error" not in result:
        handler.cache_set(cache_key, result, ttl=300)
    return result


def _search_cloud(jql, max_results, start_at, fields, brief):
    """Cloud v3: cursor-based pagination via nextPageToken.

    Cloud v3 does not support startAt — pagination uses an opaque
    nextPageToken cursor. The start_at parameter is emulated by
    fetching enough results and slicing. High start_at values
    cause proportionally more API calls.
    """
    all_issues = []
    next_page_token = ""
    page_size = min(max_results + start_at, 100)

    for page in range(_MAX_SEARCH_PAGES):
        query = {
            "jql": jql,
            "maxResults": str(page_size),
            "fields": fields,
        }
        if next_page_token:
            query["nextPageToken"] = next_page_token

        status, body, _ = handler.http("GET", "/rest/api/2/search", query=query)
        if status < 200 or status >= 300 or not isinstance(body, dict):
            handler.log(f"cloud search failed on page {page}: HTTP {status}")
            return http_error(status, body)

        page_issues = body.get("issues", [])
        all_issues.extend(page_issues)

        if len(all_issues) >= start_at + max_results:
            break

        if len(page_issues) == 0 or body.get("isLast", True) or not body.get("nextPageToken"):
            break

        next_page_token = body["nextPageToken"]

    total_fetched = len(all_issues)

    if start_at > 0:
        all_issues = all_issues[start_at:]
    all_issues = all_issues[:max_results]

    result = {"total": total_fetched, "count": len(all_issues), "start_at": start_at}

    if total_fetched > start_at + len(all_issues):
        result["truncated"] = True
        result["warning"] = (
            f"Showing {len(all_issues)} of {total_fetched} fetched results. "
            f"Use start_at={start_at + len(all_issues)} to fetch the next page."
        )

    if brief:
        result["issues"] = [extract_brief_issue(i) for i in all_issues]
    else:
        result["issues"] = [extract_issue_fields(i, fields) for i in all_issues]

    return result


def _search_server(jql, max_results, start_at, fields, brief):
    """Server v2: offset-based pagination via startAt."""
    status, body, _ = handler.http(
        "GET",
        "/rest/api/2/search",
        query={
            "jql": jql,
            "maxResults": str(max_results),
            "startAt": str(start_at),
            "fields": fields,
        },
    )
    if status < 200 or status >= 300 or not isinstance(body, dict):
        return http_error(status, body)

    issues = body.get("issues", [])
    total = body.get("total", len(issues))

    result = {"total": total, "count": len(issues), "start_at": start_at}

    if total > len(issues):
        result["truncated"] = True
        result["warning"] = (
            f"Showing {len(issues)} of {total} results. Use start_at={start_at + len(issues)} to fetch the next page."
        )

    if brief:
        result["issues"] = [extract_brief_issue(i) for i in issues]
    else:
        result["issues"] = [extract_issue_fields(i, fields) for i in issues]

    return result


def get_issues(params):
    """Get issues by key with optional brief mode, cached for 5 min."""
    raw_keys = params.get("issue_keys", "")
    keys = [validate_issue_key(k) for k in raw_keys.split(",") if k.strip()]
    if not keys:
        return {"issues": [], "count": 0}

    fields = params.get("fields", "summary,status,assignee,priority")
    brief = params.get("brief", True)

    sorted_keys = ",".join(sorted(keys))
    key_input = f"{sorted_keys}|{fields}|{brief}"
    cache_key = f"issues:{hashlib.sha256(key_input.encode()).hexdigest()[:12]}"
    cached = handler.cache_get(cache_key)
    if cached:
        handler.log(f"returning cached issues: {sorted_keys[:60]}")
        return cached

    # Batch fetch via JQL: key in (K1, K2, ...)
    key_list = ",".join(keys)
    jql = f"key in ({key_list})"

    status, body, _ = handler.http(
        "GET",
        "/rest/api/2/search",
        query={
            "jql": jql,
            "maxResults": str(len(keys)),
            "fields": fields,
        },
    )
    if status < 200 or status >= 300 or not isinstance(body, dict):
        return http_error(status, body)

    # Build lookup for ordering and missing-key detection.
    fetched = {}
    for issue in body.get("issues", []):
        fetched[issue.get("key", "")] = issue

    results = []
    for k in keys:
        issue = fetched.get(k)
        if issue:
            results.append(extract_brief_issue(issue) if brief else extract_issue_fields(issue, fields))
        else:
            results.append({"key": k, "error": "Issue not found or not accessible"})

    result = {"issues": results, "count": len(results)}
    handler.cache_set(cache_key, result, ttl=300)
    return result


def get_user(params):
    """Look up a Jira user."""
    username = params.get("username", "")
    if not username.strip():
        return {"error": "username is required"}

    # Handle self-referencing aliases.
    if is_user_alias(username):
        status, body, _ = handler.http("GET", "/rest/api/2/myself")
        if 200 <= status < 300 and isinstance(body, dict):
            return extract_user_fields(body)
        return http_error(status, body)

    # Cloud uses query=, Server uses username=.
    if handler.is_cloud:
        query = {"query": username}
    else:
        query = {"username": username}

    status, body, _ = handler.http("GET", "/rest/api/2/user/search", query=query)
    if status < 200 or status >= 300:
        return http_error(status, body)

    users = body if isinstance(body, list) else []
    if not users:
        return {"error": f"User '{username}' not found"}

    return extract_user_fields(users[0])


def get_transitions(params):
    """Get available workflow transitions for an issue."""
    issue_key = validate_issue_key(params.get("issue_key", ""))
    status, body, _ = handler.http("GET", f"/rest/api/2/issue/{issue_key}/transitions")
    if status < 200 or status >= 300:
        return http_error(status, body)
    return body


def get_resolutions(_params):
    """Get all available resolution values, cached for 1 hour."""
    cached = handler.cache_get("resolutions")
    if cached:
        return cached

    status, body, _ = handler.http("GET", "/rest/api/2/resolution")
    if status < 200 or status >= 300:
        return http_error(status, body)
    if isinstance(body, list):
        result = {"resolutions": [{"id": r.get("id"), "name": r.get("name")} for r in body]}
    else:
        result = body
    handler.cache_set("resolutions", result, ttl=3600)
    return result


def get_link_types(_params):
    """List available issue link types, cached for 1 hour."""
    cached = handler.cache_get("link_types")
    if cached:
        return cached

    status, body, _ = handler.http("GET", "/rest/api/2/issueLinkType")
    if status < 200 or status >= 300:
        return http_error(status, body)
    handler.cache_set("link_types", body, ttl=3600)
    return body


def get_issue_types(params):
    """Get available issue types for a project, cached for 1 hour."""
    raw_key = params.get("project_key", "")
    project_key = validate_project_key(raw_key) if raw_key else ""

    cache_key = f"issue_types:project:{project_key}" if project_key else "issue_types:global"
    cached = handler.cache_get(cache_key)
    if cached:
        return cached

    if project_key:
        status, body, _ = handler.http("GET", f"/rest/api/2/project/{project_key}")
        if status < 200 or status >= 300:
            return http_error(status, body)
        raw_types = body.get("issueTypes", []) if isinstance(body, dict) else []
    else:
        status, body, _ = handler.http("GET", "/rest/api/2/issuetype")
        if status < 200 or status >= 300:
            return http_error(status, body)
        raw_types = body if isinstance(body, list) else []

    result = {
        "issue_types": [
            {"id": t.get("id"), "name": t.get("name"), "subtask": t.get("subtask", False)} for t in raw_types
        ],
    }
    if project_key:
        result["project_key"] = project_key
    handler.cache_set(cache_key, result, ttl=3600)
    return result


def flush_cache(_params):
    """Flush all Jira plugin cache entries."""
    handler.cache_flush()
    return {"success": True, "message": "Jira cache flushed"}


TOOLS = {
    "jira_get_myself": get_myself,
    "jira_search": search,
    "jira_get_issues": get_issues,
    "jira_get_user": get_user,
    "jira_get_transitions": get_transitions,
    "jira_get_resolutions": get_resolutions,
    "jira_get_link_types": get_link_types,
    "jira_get_issue_types": get_issue_types,
    "jira_flush_cache": flush_cache,
}
