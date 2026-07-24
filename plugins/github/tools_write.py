"""GitHub write tool implementations.

All write tools default to dry_run=true for safety.
"""

import base64
import re

import handler
from tools import _check_rate_limit, _http_error, _split_repo, _validate_path, _validate_repo

_VALID_EVENTS = {"COMMENT", "REQUEST_CHANGES", "APPROVE"}
_MAX_BODY_LEN = 65000
_MAX_COMMENTS = 50
_MAX_FILE_CONTENT = 1_000_000
_VALID_BRANCH = re.compile(r"^[a-zA-Z0-9]([a-zA-Z0-9._/-]*[a-zA-Z0-9])?$")


def _validate_branch(name):
    """Validate a branch name against git check-ref-format rules."""
    if not name or not name.strip():
        raise ValueError("branch name is required")
    if not _VALID_BRANCH.match(name):
        raise ValueError(f"invalid branch name: {name!r}")
    if ".." in name or "//" in name:
        raise ValueError(f"invalid branch name: {name!r} (contains .. or //)")
    if name.endswith(".lock"):
        raise ValueError(f"invalid branch name: {name!r} (ends with .lock)")
    if "/." in name:
        raise ValueError(f"invalid branch name: {name!r} (component starts with '.')")


def _fetch_head_sha(owner, name, pr_number):
    """Fetch the HEAD SHA of a PR via a fresh (uncached) GET."""
    status, body, _ = handler.http("GET", f"/repos/{owner}/{name}/pulls/{pr_number}")
    if status < 200 or status >= 300 or not isinstance(body, dict):
        return None
    return (body.get("head") or {}).get("sha") or None


def _validate_comment(comment, index):
    """Validate a single inline comment object."""
    if not isinstance(comment, dict):
        raise ValueError(f"comments[{index}]: expected an object, got {type(comment).__name__}")
    path = comment.get("path")
    if not path or not isinstance(path, str):
        raise ValueError(f"comments[{index}]: 'path' is required and must be a non-empty string")
    line = comment.get("line")
    if isinstance(line, float) and line != int(line):
        raise ValueError(f"comments[{index}]: 'line' must be an integer, got {line}")
    if not isinstance(line, (int, float)) or int(line) <= 0:
        raise ValueError(f"comments[{index}]: 'line' is required and must be a positive integer")
    body = comment.get("body")
    if not body or not isinstance(body, str) or not body.strip():
        raise ValueError(f"comments[{index}]: 'body' is required and must be a non-empty string")
    result = {"path": path, "line": int(line), "body": body}
    start_line = comment.get("start_line")
    if start_line is not None:
        start_line = int(start_line)
        if start_line <= 0:
            raise ValueError(f"comments[{index}]: 'start_line' must be a positive integer")
        if start_line >= int(line):
            raise ValueError(f"comments[{index}]: 'start_line' ({start_line}) must be less than 'line' ({int(line)})")
        result["start_line"] = start_line
    return result


def create_review(params):
    """Submit a PR review with optional inline comments."""
    repo = params.get("repo", "")
    pr_number = int(params.get("pr_number", 0))
    body = params.get("body", "")
    event = (params.get("event") or "COMMENT").upper()
    comments_raw = params.get("comments") or []
    commit_id = params.get("commit_id", "")
    confirm_approve = params.get("confirm_approve", False)
    dry_run = params.get("dry_run", True)

    _validate_repo(repo)
    owner, name = _split_repo(repo)

    if pr_number <= 0:
        raise ValueError("pr_number must be a positive integer")

    if event not in _VALID_EVENTS:
        raise ValueError(f"invalid event: {event!r} (valid: {', '.join(sorted(_VALID_EVENTS))})")

    if event == "APPROVE" and not confirm_approve:
        return {
            "warning": "APPROVE will post an approval review under your identity. "
            "This can satisfy branch protection rules and enable merge. "
            "Re-call with confirm_approve=true to proceed.",
            "dry_run": True,
            "action": "github_create_review",
            "repo": repo,
            "pr_number": pr_number,
            "event": event,
        }

    if event == "REQUEST_CHANGES" and not body and not comments_raw:
        raise ValueError("REQUEST_CHANGES requires a body or at least one comment")

    if body and len(body) > _MAX_BODY_LEN:
        raise ValueError(f"body exceeds {_MAX_BODY_LEN} character limit ({len(body)} chars)")

    if not isinstance(comments_raw, list):
        raise ValueError(f"comments must be a list, got {type(comments_raw).__name__}")
    if len(comments_raw) > _MAX_COMMENTS:
        raise ValueError(f"too many comments ({len(comments_raw)}): maximum is {_MAX_COMMENTS}")

    comments = [_validate_comment(c, i) for i, c in enumerate(comments_raw)]

    if dry_run is not False:
        preview = {
            "dry_run": True,
            "action": "github_create_review",
            "repo": repo,
            "pr_number": pr_number,
            "event": event,
            "comment_count": len(comments),
        }
        if body:
            preview["body_preview"] = body[:200]
        if not commit_id:
            sha = _fetch_head_sha(owner, name, pr_number)
            if sha:
                preview["commit_id"] = sha
        else:
            preview["commit_id"] = commit_id
        return preview

    if not commit_id:
        commit_id = _fetch_head_sha(owner, name, pr_number)
        if not commit_id:
            raise ValueError(
                f"could not resolve HEAD SHA for PR #{pr_number} — provide commit_id explicitly or verify the PR exists"
            )

    api_body = {"event": event, "commit_id": commit_id}
    if body:
        api_body["body"] = body
    if comments:
        api_body["comments"] = comments

    status, resp, headers = handler.http(
        "POST",
        f"/repos/{owner}/{name}/pulls/{pr_number}/reviews",
        body=api_body,
    )
    if status < 200 or status >= 300:
        return _http_error(status, resp)

    handler.invalidate_cache()
    result = {
        "id": resp.get("id") if isinstance(resp, dict) else None,
        "state": resp.get("state") if isinstance(resp, dict) else None,
        "html_url": resp.get("html_url") if isinstance(resp, dict) else None,
    }
    _check_rate_limit(headers, result)
    return result


def add_pr_comment(params):
    """Post a single inline review comment on a PR diff."""
    repo = params.get("repo", "")
    pr_number = int(params.get("pr_number", 0))
    path = params.get("path", "")
    line = int(params.get("line", 0))
    body = params.get("body", "")
    commit_id = params.get("commit_id", "")
    side = (params.get("side") or "RIGHT").upper()
    start_line = params.get("start_line")
    dry_run = params.get("dry_run", True)

    _validate_repo(repo)
    owner, name = _split_repo(repo)

    if pr_number <= 0:
        raise ValueError("pr_number must be a positive integer")
    if not path:
        raise ValueError("path is required")
    if line <= 0:
        raise ValueError("line must be a positive integer")
    if not body or not body.strip():
        raise ValueError("body is required and must be non-empty")
    if len(body) > _MAX_BODY_LEN:
        raise ValueError(f"body exceeds {_MAX_BODY_LEN} character limit")
    if side not in ("LEFT", "RIGHT"):
        raise ValueError(f"invalid side: {side!r} (valid: LEFT, RIGHT)")

    validated_start_line = None
    if start_line is not None:
        validated_start_line = int(start_line)
        if validated_start_line <= 0:
            raise ValueError("start_line must be a positive integer")
        if validated_start_line >= line:
            raise ValueError(f"start_line ({validated_start_line}) must be less than line ({line})")

    if dry_run is not False:
        preview = {
            "dry_run": True,
            "action": "github_add_pr_comment",
            "repo": repo,
            "pr_number": pr_number,
            "path": path,
            "line": line,
            "side": side,
            "body_preview": body[:200],
        }
        if validated_start_line is not None:
            preview["start_line"] = validated_start_line
        if not commit_id:
            sha = _fetch_head_sha(owner, name, pr_number)
            if sha:
                preview["commit_id"] = sha
        else:
            preview["commit_id"] = commit_id
        return preview

    if not commit_id:
        commit_id = _fetch_head_sha(owner, name, pr_number)
        if not commit_id:
            raise ValueError(
                f"could not resolve HEAD SHA for PR #{pr_number} — provide commit_id explicitly or verify the PR exists"
            )

    api_body = {
        "body": body,
        "commit_id": commit_id,
        "path": path,
        "line": line,
        "side": side,
        "subject_type": "line",
    }
    if validated_start_line is not None:
        api_body["start_line"] = validated_start_line
        api_body["start_side"] = side

    status, resp, headers = handler.http(
        "POST",
        f"/repos/{owner}/{name}/pulls/{pr_number}/comments",
        body=api_body,
    )
    if status < 200 or status >= 300:
        return _http_error(status, resp)

    handler.invalidate_cache()
    result = {
        "id": resp.get("id") if isinstance(resp, dict) else None,
        "html_url": resp.get("html_url") if isinstance(resp, dict) else None,
    }
    _check_rate_limit(headers, result)
    return result


def add_comment(params):
    """Post a conversation comment on an issue or PR."""
    repo = params.get("repo", "")
    issue_number = int(params.get("issue_number", 0))
    body = params.get("body", "")
    dry_run = params.get("dry_run", True)

    _validate_repo(repo)
    owner, name = _split_repo(repo)

    if issue_number <= 0:
        raise ValueError("issue_number must be a positive integer")
    if not body or not body.strip():
        raise ValueError("body is required and must be non-empty")
    if len(body) > _MAX_BODY_LEN:
        raise ValueError(f"body exceeds {_MAX_BODY_LEN} character limit")

    if dry_run is not False:
        return {
            "dry_run": True,
            "action": "github_add_comment",
            "repo": repo,
            "issue_number": issue_number,
            "body_preview": body[:200],
        }

    status, resp, headers = handler.http(
        "POST",
        f"/repos/{owner}/{name}/issues/{issue_number}/comments",
        body={"body": body},
    )
    if status < 200 or status >= 300:
        return _http_error(status, resp)

    handler.invalidate_cache()
    result = {
        "id": resp.get("id") if isinstance(resp, dict) else None,
        "html_url": resp.get("html_url") if isinstance(resp, dict) else None,
    }
    _check_rate_limit(headers, result)
    return result


def create_pull_request(params):
    """Create a new pull request."""
    repo = params.get("repo", "")
    title = params.get("title", "")
    head = params.get("head", "")
    base = params.get("base", "")
    body = params.get("body", "")
    draft = params.get("draft", False)
    maintainer_can_modify = params.get("maintainer_can_modify", True)
    dry_run = params.get("dry_run", True)

    _validate_repo(repo)
    owner, name = _split_repo(repo)

    if not title or not title.strip():
        raise ValueError("title is required and must be non-empty")
    if not head or not head.strip():
        raise ValueError("head is required and must be non-empty")
    if not base or not base.strip():
        raise ValueError("base is required and must be non-empty")
    if body and len(body) > _MAX_BODY_LEN:
        raise ValueError(f"body exceeds {_MAX_BODY_LEN} character limit ({len(body)} chars)")

    if dry_run is not False:
        preview = {
            "dry_run": True,
            "action": "github_create_pull_request",
            "repo": repo,
            "head": head,
            "base": base,
            "title": title,
            "draft": draft,
            "maintainer_can_modify": maintainer_can_modify,
        }
        if body:
            preview["body_preview"] = body[:200]
        return preview

    api_body = {
        "title": title,
        "head": head,
        "base": base,
        "draft": draft,
        "maintainer_can_modify": maintainer_can_modify,
    }
    if body:
        api_body["body"] = body

    status, resp, headers = handler.http(
        "POST",
        f"/repos/{owner}/{name}/pulls",
        body=api_body,
    )
    if status < 200 or status >= 300:
        return _http_error(status, resp)

    handler.invalidate_cache()
    result = {
        "id": resp.get("id") if isinstance(resp, dict) else None,
        "number": resp.get("number") if isinstance(resp, dict) else None,
        "html_url": resp.get("html_url") if isinstance(resp, dict) else None,
        "state": resp.get("state") if isinstance(resp, dict) else None,
        "draft": resp.get("draft") if isinstance(resp, dict) else None,
    }
    _check_rate_limit(headers, result)
    return result


def create_branch(params):
    """Create a new branch from a source branch."""
    repo = params.get("repo", "")
    branch_name = params.get("branch_name", "")
    source_branch = params.get("source_branch", "main")
    dry_run = params.get("dry_run", True)

    _validate_repo(repo)
    _validate_branch(branch_name)
    _validate_branch(source_branch)
    owner, name = _split_repo(repo)

    status, body, headers = handler.http("GET", f"/repos/{owner}/{name}/git/ref/heads/{source_branch}")
    if status < 200 or status >= 300 or not isinstance(body, dict):
        return _http_error(status, body)
    source_sha = (body.get("object") or {}).get("sha", "")
    if not source_sha:
        return {"error": f"could not resolve SHA for branch {source_branch!r}"}

    if dry_run is not False:
        result = {
            "dry_run": True,
            "action": "github_create_branch",
            "repo": repo,
            "branch_name": branch_name,
            "source_branch": source_branch,
            "source_sha": source_sha[:12],
        }
        _check_rate_limit(headers, result)
        return result

    status, resp, headers = handler.http(
        "POST",
        f"/repos/{owner}/{name}/git/refs",
        body={"ref": f"refs/heads/{branch_name}", "sha": source_sha},
    )
    if status < 200 or status >= 300:
        return _http_error(status, resp)

    handler.invalidate_cache()
    result = {
        "ref": (resp.get("ref", "") if isinstance(resp, dict) else ""),
        "sha": (resp.get("object", {}).get("sha", "")[:12] if isinstance(resp, dict) else ""),
        "created": True,
    }
    _check_rate_limit(headers, result)
    return result


def create_or_update_file(params):
    """Create or update a file in a repository."""
    repo = params.get("repo", "")
    path = params.get("path", "")
    content = params.get("content", "")
    message = params.get("message", "")
    branch = params.get("branch", "")
    dry_run = params.get("dry_run", True)

    _validate_repo(repo)
    owner, name = _split_repo(repo)
    _validate_path(path)
    if branch:
        _validate_branch(branch)

    if not content:
        raise ValueError("content is required")
    if not message or not message.strip():
        raise ValueError("message is required")
    if len(content) > _MAX_FILE_CONTENT:
        raise ValueError(f"content exceeds {_MAX_FILE_CONTENT} character limit ({len(content)} characters)")

    query = {}
    if branch:
        query["ref"] = branch

    existing_sha = None
    status, body, _ = handler.http("GET", f"/repos/{owner}/{name}/contents/{path}", query=query or None)
    if 200 <= status < 300 and isinstance(body, dict):
        existing_sha = body.get("sha", "")
    elif status != 404:
        return _http_error(status, body)
    is_update = existing_sha is not None

    if dry_run is not False:
        preview = {
            "dry_run": True,
            "action": "github_create_or_update_file",
            "repo": repo,
            "path": path,
            "is_update": is_update,
            "message_preview": message[:200],
        }
        if branch:
            preview["branch"] = branch
        return preview

    encoded = base64.b64encode(content.encode("utf-8")).decode("ascii")
    api_body = {"message": message, "content": encoded}
    if existing_sha:
        api_body["sha"] = existing_sha
    if branch:
        api_body["branch"] = branch

    status, resp, headers = handler.http("PUT", f"/repos/{owner}/{name}/contents/{path}", body=api_body)
    if status < 200 or status >= 300:
        return _http_error(status, resp)

    handler.invalidate_cache()
    file_info = (resp.get("content") or {}) if isinstance(resp, dict) else {}
    result = {
        "path": file_info.get("path", path),
        "sha": file_info.get("sha", "")[:12],
        "html_url": file_info.get("html_url", ""),
        "is_update": is_update,
        "created": True,
    }
    _check_rate_limit(headers, result)
    return result


WRITE_TOOLS = {
    "github_create_review": create_review,
    "github_add_pr_comment": add_pr_comment,
    "github_add_comment": add_comment,
    "github_create_pull_request": create_pull_request,
    "github_create_branch": create_branch,
    "github_create_or_update_file": create_or_update_file,
}
