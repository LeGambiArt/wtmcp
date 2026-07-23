"""GitHub tool implementations."""

import base64
import hashlib
import re

import handler

_VALID_ORG = re.compile(r"^[a-zA-Z0-9._-]+$")
_VALID_REPO = re.compile(r"^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+$")
_VALID_FILTER = {"assigned", "created", "mentioned", "subscribed", "repos", "all"}
_VALID_PR_STATE = {"open", "closed", "all"}
_RATE_LIMIT_WARN = 100
_MAX_CONTENT_BYTES = 50000
_MAX_DIR_ENTRIES = 200


def _http_error(status, body):
    """Format an HTTP error response."""
    msg = f"HTTP {status}"
    if isinstance(body, dict):
        detail = body.get("message", "")
        if detail:
            msg += f": {detail}"
    return {"error": msg}


def _check_rate_limit(headers, result):
    """Add rate limit warning if remaining calls are low."""
    remaining = headers.get("x-ratelimit-remaining") or headers.get("X-RateLimit-Remaining")
    if remaining is not None:
        try:
            remaining_int = int(remaining)
            if remaining_int < _RATE_LIMIT_WARN:
                result["_rate_limit_remaining"] = remaining_int
        except (ValueError, TypeError):
            pass


def _validate_org(org):
    if org and not _VALID_ORG.match(org):
        raise ValueError(f"invalid org name: {org!r}")
    if org == "..":
        raise ValueError(f"invalid org name: {org!r}")


def _validate_repo(repo):
    if not _VALID_REPO.match(repo):
        raise ValueError(f"invalid repo format: {repo!r} (expected owner/repo)")
    if ".." in repo.split("/"):
        raise ValueError(f"invalid repo format: {repo!r} (path traversal)")


def _validate_path(path):
    if not path or not path.strip():
        raise ValueError("path is required")
    if ".." in path.split("/"):
        raise ValueError(f"path traversal in path: {path!r}")
    if path.startswith("/"):
        raise ValueError(f"absolute path not allowed: {path!r}")


def _validate_filter(f):
    if f not in _VALID_FILTER:
        raise ValueError(f"invalid filter: {f!r} (valid: {', '.join(sorted(_VALID_FILTER))})")


def _split_repo(repo):
    """Split owner/repo into (owner, repo)."""
    parts = repo.split("/", 1)
    return parts[0], parts[1]


def _extract_search_item(item):
    """Extract brief fields from a search result item."""
    is_pr = "pull_request" in item
    repo_url = item.get("repository_url", "")
    repo = "/".join(repo_url.rstrip("/").split("/")[-2:]) if repo_url else ""

    result = {
        "type": "pr" if is_pr else "issue",
        "repo": repo,
        "number": item.get("number"),
        "title": item.get("title"),
        "state": item.get("state"),
        "author": (item.get("user") or {}).get("login", ""),
        "updated": item.get("updated_at"),
        "labels": [lb.get("name", "") for lb in (item.get("labels") or [])],
    }

    # Best-effort involvement detection
    if handler.username:
        assignees = [a.get("login", "") for a in (item.get("assignees") or [])]
        if item.get("user", {}).get("login") == handler.username:
            result["involvement"] = "author"
        elif handler.username in assignees:
            result["involvement"] = "assignee"
        else:
            result["involvement"] = "mentioned"

    return result


def _search_issues(query, sort, max_results, start_at):
    """Shared search implementation for GitHub search/issues endpoint."""
    per_page = min(max_results, 100)
    page = (start_at // per_page) + 1 if per_page > 0 else 1

    cache_input = f"{query}|{sort}|{max_results}|{start_at}"
    cache_key = f"search:{hashlib.sha256(cache_input.encode()).hexdigest()[:12]}"
    cached = handler.cache_get(cache_key)
    if cached:
        return cached

    status, body, headers = handler.http(
        "GET",
        "/search/issues",
        query={
            "q": query,
            "sort": sort,
            "per_page": str(per_page),
            "page": str(page),
        },
    )
    if status < 200 or status >= 300 or not isinstance(body, dict):
        return _http_error(status, body)

    items = body.get("items", [])
    total = body.get("total_count", len(items))

    result = {
        "total": total,
        "count": len(items),
        "start_at": start_at,
        "items": [_extract_search_item(i) for i in items],
    }

    if total > start_at + len(items):
        result["truncated"] = True

    _check_rate_limit(headers, result)
    handler.cache_set(cache_key, result, ttl=300)
    return result


# --- Primary tools ---


def my_work(params):
    """Unified view of all open items involving the authenticated user."""
    org = params.get("org", "")
    repo = params.get("repo", "")
    sort = params.get("sort", "updated")
    max_results = min(int(params.get("max_results", 20)), 100)
    start_at = max(int(params.get("start_at", 0)), 0)

    if org:
        _validate_org(org)
    if repo:
        _validate_repo(repo)

    if not handler.username:
        return {"error": "username not available — check GITHUB_TOKEN"}

    q = f"involves:{handler.username} is:open"
    if org:
        q += f" org:{org}"
    if repo:
        q += f" repo:{repo}"

    return _search_issues(q, sort, max_results, start_at)


def my_prs_to_review(params):
    """List open PRs where the user is a requested reviewer."""
    org = params.get("org", "")
    repo = params.get("repo", "")
    max_results = min(int(params.get("max_results", 30)), 100)

    if org:
        _validate_org(org)
    if repo:
        _validate_repo(repo)

    if not handler.username:
        return {"error": "username not available — check GITHUB_TOKEN"}

    q = f"is:pr is:open review-requested:{handler.username}"
    if org:
        q += f" org:{org}"
    if repo:
        q += f" repo:{repo}"

    return _search_issues(q, "updated", max_results, 0)


def my_issues(params):
    """List open issues assigned to the authenticated user."""
    filter_val = params.get("filter", "assigned")
    labels = params.get("labels", "")
    sort = params.get("sort", "updated")
    max_results = min(int(params.get("max_results", 30)), 100)

    _validate_filter(filter_val)

    cache_input = f"issues|{filter_val}|{labels}|{sort}|{max_results}"
    cache_key = f"myissues:{hashlib.sha256(cache_input.encode()).hexdigest()[:12]}"
    cached = handler.cache_get(cache_key)
    if cached:
        return cached

    query = {
        "filter": filter_val,
        "state": "open",
        "sort": sort,
        "per_page": str(max_results),
    }
    if labels:
        query["labels"] = labels

    status, body, headers = handler.http("GET", "/issues", query=query)
    if status < 200 or status >= 300 or not isinstance(body, list):
        return _http_error(status, body)

    issues = [
        {
            "repo": "/".join((i.get("repository", {}).get("full_name", "")).split("/")[-2:]),
            "number": i.get("number"),
            "title": i.get("title"),
            "state": i.get("state"),
            "assignee": (i.get("assignee") or {}).get("login", ""),
            "labels": [lb.get("name", "") for lb in (i.get("labels") or [])],
            "updated": i.get("updated_at"),
            "url": i.get("html_url", ""),
            "type": "pr" if i.get("pull_request") else "issue",
        }
        for i in body[:max_results]
    ]

    result = {"total": len(issues), "count": len(issues), "issues": issues}
    _check_rate_limit(headers, result)
    handler.cache_set(cache_key, result, ttl=300)
    return result


def my_notifications(params):
    """List GitHub notifications."""
    participating = params.get("participating", True)
    show_all = params.get("all", False)
    max_results = min(int(params.get("max_results", 30)), 50)

    cache_input = f"notif|{participating}|{show_all}|{max_results}"
    cache_key = f"notif:{hashlib.sha256(cache_input.encode()).hexdigest()[:12]}"
    cached = handler.cache_get(cache_key)
    if cached:
        return cached

    query = {
        "participating": str(participating).lower(),
        "all": str(show_all).lower(),
        "per_page": str(max_results),
    }

    status, body, headers = handler.http("GET", "/notifications", query=query)
    if status < 200 or status >= 300 or not isinstance(body, list):
        return _http_error(status, body)

    notifications = [
        {
            "id": n.get("id"),
            "reason": n.get("reason"),
            "unread": n.get("unread"),
            "updated": n.get("updated_at"),
            "subject_type": (n.get("subject") or {}).get("type", ""),
            "subject_title": (n.get("subject") or {}).get("title", ""),
            "repo": (n.get("repository") or {}).get("full_name", ""),
        }
        for n in body[:max_results]
    ]

    result = {"count": len(notifications), "notifications": notifications}
    _check_rate_limit(headers, result)
    handler.cache_set(cache_key, result, ttl=120)
    return result


# --- Deferred tools ---


def get_pr(params):
    """Get detailed info about a specific PR."""
    repo = params["repo"]
    pr_number = int(params["pr_number"])
    _validate_repo(repo)
    owner, name = _split_repo(repo)

    cache_key = f"pr:{owner}/{name}/{pr_number}"
    cached = handler.cache_get(cache_key)
    if cached:
        return cached

    status, body, headers = handler.http("GET", f"/repos/{owner}/{name}/pulls/{pr_number}")
    if status < 200 or status >= 300 or not isinstance(body, dict):
        return _http_error(status, body)

    result = {
        "number": body.get("number"),
        "title": body.get("title"),
        "state": body.get("state"),
        "draft": body.get("draft"),
        "body": body.get("body", ""),
        "author": (body.get("user") or {}).get("login", ""),
        "base": (body.get("base") or {}).get("ref", ""),
        "head": (body.get("head") or {}).get("ref", ""),
        "head_sha": (body.get("head") or {}).get("sha", ""),
        "merged": body.get("merged"),
        "mergeable": body.get("mergeable"),
        "mergeable_state": body.get("mergeable_state"),
        "additions": body.get("additions"),
        "deletions": body.get("deletions"),
        "changed_files": body.get("changed_files"),
        "commits": body.get("commits"),
        "comments": body.get("comments"),
        "review_comments": body.get("review_comments"),
        "labels": [lb.get("name", "") for lb in (body.get("labels") or [])],
        "assignees": [a.get("login", "") for a in (body.get("assignees") or [])],
        "requested_reviewers": [r.get("login", "") for r in (body.get("requested_reviewers") or [])],
        "created_at": body.get("created_at"),
        "updated_at": body.get("updated_at"),
        "merged_at": body.get("merged_at"),
        "url": body.get("html_url", ""),
    }
    _check_rate_limit(headers, result)
    handler.cache_set(cache_key, result, ttl=300)
    return result


def get_issue(params):
    """Get detailed info about a specific issue."""
    repo = params["repo"]
    issue_number = int(params["issue_number"])
    _validate_repo(repo)
    owner, name = _split_repo(repo)

    cache_key = f"issue:{owner}/{name}/{issue_number}"
    cached = handler.cache_get(cache_key)
    if cached:
        return cached

    status, body, headers = handler.http("GET", f"/repos/{owner}/{name}/issues/{issue_number}")
    if status < 200 or status >= 300 or not isinstance(body, dict):
        return _http_error(status, body)

    result = {
        "number": body.get("number"),
        "title": body.get("title"),
        "state": body.get("state"),
        "body": body.get("body", ""),
        "author": (body.get("user") or {}).get("login", ""),
        "assignees": [a.get("login", "") for a in (body.get("assignees") or [])],
        "labels": [lb.get("name", "") for lb in (body.get("labels") or [])],
        "milestone": (body.get("milestone") or {}).get("title", ""),
        "comments": body.get("comments"),
        "created_at": body.get("created_at"),
        "updated_at": body.get("updated_at"),
        "closed_at": body.get("closed_at"),
        "url": body.get("html_url", ""),
    }
    _check_rate_limit(headers, result)
    handler.cache_set(cache_key, result, ttl=300)
    return result


def get_pr_files(params):
    """List files changed in a PR."""
    repo = params["repo"]
    pr_number = int(params["pr_number"])
    max_results = min(int(params.get("max_results", 30)), 100)
    _validate_repo(repo)
    owner, name = _split_repo(repo)

    status, body, headers = handler.http(
        "GET",
        f"/repos/{owner}/{name}/pulls/{pr_number}/files",
        query={"per_page": str(max_results)},
    )
    if status < 200 or status >= 300 or not isinstance(body, list):
        return _http_error(status, body)

    files = [
        {
            "filename": f.get("filename"),
            "status": f.get("status"),
            "additions": f.get("additions"),
            "deletions": f.get("deletions"),
            "changes": f.get("changes"),
            "patch": f.get("patch", ""),
        }
        for f in body[:max_results]
    ]

    result = {"count": len(files), "files": files}
    _check_rate_limit(headers, result)
    return result


def get_pr_reviews(params):
    """List reviews on a PR."""
    repo = params["repo"]
    pr_number = int(params["pr_number"])
    _validate_repo(repo)
    owner, name = _split_repo(repo)

    status, body, headers = handler.http("GET", f"/repos/{owner}/{name}/pulls/{pr_number}/reviews")
    if status < 200 or status >= 300 or not isinstance(body, list):
        return _http_error(status, body)

    reviews = [
        {
            "id": r.get("id"),
            "user": (r.get("user") or {}).get("login", ""),
            "state": r.get("state"),
            "body": r.get("body", ""),
            "submitted_at": r.get("submitted_at"),
        }
        for r in body
    ]

    result = {"count": len(reviews), "reviews": reviews}
    _check_rate_limit(headers, result)
    return result


def get_pr_review_comments(params):
    """List inline review comments on a PR."""
    repo = params["repo"]
    pr_number = int(params["pr_number"])
    max_results = min(int(params.get("max_results", 50)), 100)
    _validate_repo(repo)
    owner, name = _split_repo(repo)

    status, body, headers = handler.http(
        "GET",
        f"/repos/{owner}/{name}/pulls/{pr_number}/comments",
        query={"per_page": str(max_results)},
    )
    if status < 200 or status >= 300 or not isinstance(body, list):
        return _http_error(status, body)

    comments = [
        {
            "id": c.get("id"),
            "user": (c.get("user") or {}).get("login", ""),
            "path": c.get("path"),
            "line": c.get("line"),
            "side": c.get("side"),
            "diff_hunk": c.get("diff_hunk", ""),
            "body": c.get("body", ""),
            "created_at": c.get("created_at"),
            "in_reply_to_id": c.get("in_reply_to_id"),
        }
        for c in body[:max_results]
    ]

    result = {"count": len(comments), "comments": comments}
    _check_rate_limit(headers, result)
    return result


def get_comments(params):
    """List conversation comments on an issue or PR."""
    repo = params["repo"]
    issue_number = int(params["issue_number"])
    max_results = min(int(params.get("max_results", 50)), 100)
    _validate_repo(repo)
    owner, name = _split_repo(repo)

    status, body, headers = handler.http(
        "GET",
        f"/repos/{owner}/{name}/issues/{issue_number}/comments",
        query={"per_page": str(max_results)},
    )
    if status < 200 or status >= 300 or not isinstance(body, list):
        return _http_error(status, body)

    comments = [
        {
            "id": c.get("id"),
            "user": (c.get("user") or {}).get("login", ""),
            "body": c.get("body", ""),
            "created_at": c.get("created_at"),
            "updated_at": c.get("updated_at"),
            "association": c.get("author_association", ""),
        }
        for c in body[:max_results]
    ]

    result = {"count": len(comments), "comments": comments}
    _check_rate_limit(headers, result)
    return result


def get_pr_commits(params):
    """List commits in a PR."""
    repo = params["repo"]
    pr_number = int(params["pr_number"])
    _validate_repo(repo)
    owner, name = _split_repo(repo)

    status, body, headers = handler.http("GET", f"/repos/{owner}/{name}/pulls/{pr_number}/commits")
    if status < 200 or status >= 300 or not isinstance(body, list):
        return _http_error(status, body)

    commits = [
        {
            "sha": c.get("sha", "")[:12],
            "message": (c.get("commit") or {}).get("message", ""),
            "author": (c.get("author") or {}).get("login", "")
            or (c.get("commit", {}).get("author") or {}).get("name", ""),
            "date": (c.get("commit", {}).get("author") or {}).get("date", ""),
        }
        for c in body
    ]

    result = {"count": len(commits), "commits": commits}
    _check_rate_limit(headers, result)
    return result


def search(params):
    """General-purpose issue/PR search."""
    query = params.get("query", "")
    sort = params.get("sort", "updated")
    max_results = min(int(params.get("max_results", 30)), 100)
    start_at = max(int(params.get("start_at", 0)), 0)

    if not query:
        return {"error": "query parameter is required"}

    return _search_issues(query, sort, max_results, start_at)


def get_file_contents(params):
    """Get file or directory contents from a repository."""
    repo = params.get("repo", "")
    path = params.get("path", "")
    ref = params.get("ref", "")
    _validate_repo(repo)
    _validate_path(path)
    owner, name = _split_repo(repo)

    query = {}
    if ref:
        query["ref"] = ref

    cache_input = f"file:{owner}/{name}/{path}:{ref}"
    cache_key = f"file:{hashlib.sha256(cache_input.encode()).hexdigest()[:12]}"
    cached = handler.cache_get(cache_key)
    if cached:
        return cached

    status, body, headers = handler.http("GET", f"/repos/{owner}/{name}/contents/{path}", query=query or None)
    if status < 200 or status >= 300:
        return _http_error(status, body)

    if isinstance(body, list):
        entries = [
            {"name": e.get("name", ""), "type": e.get("type", ""), "path": e.get("path", ""), "size": e.get("size", 0)}
            for e in body[:_MAX_DIR_ENTRIES]
        ]
        result = {"type": "directory", "path": path, "count": len(entries), "entries": entries}
        if len(body) > _MAX_DIR_ENTRIES:
            result["truncated"] = True
        _check_rate_limit(headers, result)
        handler.cache_set(cache_key, result, ttl=300)
        return result

    content_b64 = body.get("content")
    size = body.get("size", 0)
    result = {
        "name": body.get("name", ""),
        "path": body.get("path", ""),
        "size": size,
        "type": body.get("type", "file"),
        "sha": body.get("sha", ""),
        "html_url": body.get("html_url", ""),
    }

    if size > 1_000_000:
        result["content_available"] = False
        result["download_url"] = body.get("download_url", "")
        _check_rate_limit(headers, result)
        handler.cache_set(cache_key, result, ttl=300)
        return result

    if content_b64 is None:
        result["content_available"] = False
        result["download_url"] = body.get("download_url", "")
        result["git_url"] = body.get("git_url", "")
    else:
        try:
            decoded = base64.b64decode(content_b64)
            if b"\x00" in decoded[:1024]:
                result["content_available"] = False
                result["binary"] = True
            elif len(decoded) > _MAX_CONTENT_BYTES:
                result["content"] = decoded[:_MAX_CONTENT_BYTES].decode("utf-8", errors="replace")
                result["content"] += f"\n[TRUNCATED at {_MAX_CONTENT_BYTES // 1000}KB, full file is {size} bytes]"
            else:
                result["content"] = decoded.decode("utf-8", errors="replace")
        except (ValueError, UnicodeDecodeError):
            result["content_available"] = False
            result["encoding"] = body.get("encoding", "")

    _check_rate_limit(headers, result)
    handler.cache_set(cache_key, result, ttl=300)
    return result


def search_code(params):
    """Search for code across repositories."""
    query = params.get("query", "")
    max_results = min(int(params.get("max_results", 30)), 100)
    if not query:
        raise ValueError("query is required")

    cache_input = f"code_search|{query}|{max_results}"
    cache_key = f"csearch:{hashlib.sha256(cache_input.encode()).hexdigest()[:12]}"
    cached = handler.cache_get(cache_key)
    if cached:
        return cached

    status, body, headers = handler.http("GET", "/search/code", query={"q": query, "per_page": str(max_results)})
    if status < 200 or status >= 300 or not isinstance(body, dict):
        return _http_error(status, body)

    items = [
        {
            "name": i.get("name", ""),
            "path": i.get("path", ""),
            "sha": i.get("sha", "")[:12],
            "html_url": i.get("html_url", ""),
            "repo": (i.get("repository") or {}).get("full_name", ""),
        }
        for i in body.get("items", [])[:max_results]
    ]

    result = {"total": body.get("total_count", 0), "count": len(items), "items": items}
    _check_rate_limit(headers, result)
    handler.cache_set(cache_key, result, ttl=300)
    return result


def search_repositories(params):
    """Search for repositories."""
    query = params.get("query", "")
    max_results = min(int(params.get("max_results", 30)), 100)
    if not query:
        raise ValueError("query is required")

    cache_input = f"repo_search|{query}|{max_results}"
    cache_key = f"rsearch:{hashlib.sha256(cache_input.encode()).hexdigest()[:12]}"
    cached = handler.cache_get(cache_key)
    if cached:
        return cached

    status, body, headers = handler.http(
        "GET", "/search/repositories", query={"q": query, "per_page": str(max_results)}
    )
    if status < 200 or status >= 300 or not isinstance(body, dict):
        return _http_error(status, body)

    items = [
        {
            "full_name": r.get("full_name", ""),
            "description": r.get("description", ""),
            "html_url": r.get("html_url", ""),
            "language": r.get("language", ""),
            "stars": r.get("stargazers_count", 0),
            "forks": r.get("forks_count", 0),
            "updated_at": r.get("updated_at", ""),
            "default_branch": r.get("default_branch", ""),
            "archived": r.get("archived", False),
        }
        for r in body.get("items", [])[:max_results]
    ]

    result = {"total": body.get("total_count", 0), "count": len(items), "items": items}
    _check_rate_limit(headers, result)
    handler.cache_set(cache_key, result, ttl=300)
    return result


def list_pull_requests(params):
    """List pull requests for a repository."""
    repo = params.get("repo", "")
    state = params.get("state", "open")
    max_results = min(int(params.get("max_results", 30)), 100)
    _validate_repo(repo)
    if state not in _VALID_PR_STATE:
        raise ValueError(f"invalid state: {state!r} (valid: {', '.join(sorted(_VALID_PR_STATE))})")
    owner, name = _split_repo(repo)

    cache_input = f"prs:{owner}/{name}:{state}:{max_results}"
    cache_key = f"prs:{hashlib.sha256(cache_input.encode()).hexdigest()[:12]}"
    cached = handler.cache_get(cache_key)
    if cached:
        return cached

    status, body, headers = handler.http(
        "GET",
        f"/repos/{owner}/{name}/pulls",
        query={"state": state, "per_page": str(max_results)},
    )
    if status < 200 or status >= 300 or not isinstance(body, list):
        return _http_error(status, body)

    prs = [
        {
            "number": pr.get("number"),
            "title": pr.get("title", ""),
            "state": pr.get("state", ""),
            "draft": pr.get("draft", False),
            "author": (pr.get("user") or {}).get("login", ""),
            "base": (pr.get("base") or {}).get("ref", ""),
            "head": (pr.get("head") or {}).get("ref", ""),
            "labels": [lb.get("name", "") for lb in (pr.get("labels") or [])],
            "created_at": pr.get("created_at", ""),
            "updated_at": pr.get("updated_at", ""),
            "html_url": pr.get("html_url", ""),
        }
        for pr in body[:max_results]
    ]

    result = {"count": len(prs), "pull_requests": prs}
    _check_rate_limit(headers, result)
    handler.cache_set(cache_key, result, ttl=300)
    return result


def list_commits(params):
    """List commits for a repository."""
    repo = params.get("repo", "")
    sha = params.get("sha", "")
    max_results = min(int(params.get("max_results", 20)), 100)
    _validate_repo(repo)
    owner, name = _split_repo(repo)

    query = {"per_page": str(max_results)}
    if sha:
        query["sha"] = sha

    cache_input = f"commits:{owner}/{name}:{sha}:{max_results}"
    cache_key = f"commits:{hashlib.sha256(cache_input.encode()).hexdigest()[:12]}"
    cached = handler.cache_get(cache_key)
    if cached:
        return cached

    status, body, headers = handler.http("GET", f"/repos/{owner}/{name}/commits", query=query)
    if status < 200 or status >= 300 or not isinstance(body, list):
        return _http_error(status, body)

    commits = [
        {
            "sha": c.get("sha", "")[:12],
            "message": (c.get("commit") or {}).get("message", ""),
            "author": (c.get("author") or {}).get("login", "")
            or (c.get("commit", {}).get("author") or {}).get("name", ""),
            "date": (c.get("commit", {}).get("author") or {}).get("date", ""),
            "html_url": c.get("html_url", ""),
        }
        for c in body[:max_results]
    ]

    result = {"count": len(commits), "commits": commits}
    _check_rate_limit(headers, result)
    handler.cache_set(cache_key, result, ttl=300)
    return result


TOOLS = {
    "github_my_work": my_work,
    "github_my_prs_to_review": my_prs_to_review,
    "github_my_issues": my_issues,
    "github_my_notifications": my_notifications,
    "github_get_pr": get_pr,
    "github_get_issue": get_issue,
    "github_get_pr_files": get_pr_files,
    "github_get_pr_reviews": get_pr_reviews,
    "github_get_pr_review_comments": get_pr_review_comments,
    "github_get_comments": get_comments,
    "github_get_pr_commits": get_pr_commits,
    "github_search": search,
    "github_get_file_contents": get_file_contents,
    "github_search_code": search_code,
    "github_search_repositories": search_repositories,
    "github_list_pull_requests": list_pull_requests,
    "github_list_commits": list_commits,
}
