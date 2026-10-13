"""Jira export, cache query, and diagnostic tools.

Export tools write JSON files to disk. Cache query tools read local
JSON files. These are pure local I/O — no Jira API calls (except
debug_fields which queries the Jira field list).
"""

import json
from datetime import datetime, timezone
from pathlib import Path

import handler
from helpers import (
    adf_to_text,
    calculate_sprint_metrics,
    extract_brief_issue,
    extract_nested_field,
    http_error,
    validate_issue_key,
)


def _get_output_dir():
    return handler.config.get("_output_dir", "")


def _get_session_dir():
    return handler.config.get("_session_dir", "")


def _normalize_export_path(file_path):
    """Normalize an export file path to a relative path for file_write.

    Accepts both relative paths and absolute paths (strips the outputDir
    prefix for backward compatibility). The core handles confinement.
    """
    if not file_path:
        raise ValueError("output_file is required")
    path = Path(file_path)
    if path.is_absolute():
        output_dir = _get_output_dir()
        if output_dir:
            try:
                return str(path.relative_to(Path(output_dir).resolve()))
            except ValueError:
                pass
        raise ValueError(f"absolute path '{file_path}' is outside the output directory; use a relative path instead")
    return str(path)


def _reject_sensitive_path(path, base_dir):
    """Reject paths with dot-prefixed components or credential directories.

    .wtmcp-data/ is exempted since exported artifacts are intentional.
    Also blocks .github/, .editorconfig etc. — users can copy such files
    to .wtmcp-data/<plugin>/ as a workaround.
    """
    try:
        rel = Path(path).relative_to(Path(base_dir).resolve())
    except ValueError:
        raise ValueError("path is not under session directory")
    for part in rel.parts:
        if part == ".wtmcp-data":
            return
        if part.startswith(".") and part != ".":
            raise ValueError(f"path contains sensitive component '{part}'")
        if part == "env.d":
            raise ValueError(f"path contains credential directory '{part}'")


def _validate_read_path(file_path):
    """Validate and resolve a read path within session or output directory."""
    if not file_path:
        raise ValueError("file_path is required")
    path = Path(file_path)

    for base_dir in [_get_output_dir(), _get_session_dir()]:
        if not base_dir:
            continue
        candidate = path if path.is_absolute() else Path(base_dir) / path
        resolved = candidate.resolve()
        base = Path(base_dir).resolve()
        try:
            resolved.relative_to(base)
        except ValueError:
            continue
        _reject_sensitive_path(resolved, base)
        if resolved.is_file():
            return resolved

    if not _get_session_dir() and not _get_output_dir():
        raise ValueError("file_path requires a configured session directory")
    raise FileNotFoundError(f"file not found or outside allowed directories: {file_path}")


def export_sprint_data(params):
    """Export all issues for a sprint to a local JSON file."""
    board_id = params.get("board_id", "")
    sprint_id = params.get("sprint_id", "")
    output_file = params.get("output_file", "")

    rel_path = _normalize_export_path(output_file)

    # Fetch sprint info
    status, sprint_info, _ = handler.http("GET", f"/rest/agile/1.0/sprint/{sprint_id}")
    if status < 200 or status >= 300:
        return http_error(status, sprint_info)

    # Fetch issues
    status, issues_resp, _ = handler.http(
        "GET",
        f"/rest/agile/1.0/board/{board_id}/sprint/{sprint_id}/issue",
        query={"maxResults": "1000", "fields": "*all"},
    )
    if status < 200 or status >= 300:
        return http_error(status, issues_resp)

    issues = issues_resp.get("issues", [])
    export_data = {
        "export_metadata": {
            "export_timestamp": datetime.now(timezone.utc).isoformat(),
            "board_id": board_id,
            "sprint_id": sprint_id,
            "tool": "jira_export_sprint_data",
        },
        "sprint_info": sprint_info,
        "issues": issues,
    }

    content = json.dumps(export_data, indent=2, ensure_ascii=False)
    saved_path = handler.file_write(rel_path, content)

    return {
        "success": True,
        "file_path": saved_path,
        "issue_count": len(issues),
        "sprint_name": sprint_info.get("name") if isinstance(sprint_info, dict) else None,
    }


def export_board_sprints(params):
    """Export all sprints for a board to a local JSON file."""
    board_id = params.get("board_id", "")
    state = params.get("state")
    output_file = params.get("output_file", "")

    rel_path = _normalize_export_path(output_file)

    # Paginate sprints
    all_sprints: list = []
    start = 0
    while True:
        query: dict = {"startAt": str(start), "maxResults": "50"}
        if state:
            query["state"] = state
        status, body, _ = handler.http("GET", f"/rest/agile/1.0/board/{board_id}/sprint", query=query)
        if status < 200 or status >= 300:
            return http_error(status, body)
        values = body.get("values", [])
        all_sprints.extend(values)
        if body.get("isLast", True) or len(values) < 50:
            break
        start += 50

    export_data = {
        "export_metadata": {
            "export_timestamp": datetime.now(timezone.utc).isoformat(),
            "board_id": board_id,
            "state": state,
            "tool": "jira_export_board_sprints",
        },
        "sprints": {"values": all_sprints, "isLast": True},
    }

    content = json.dumps(export_data, indent=2, ensure_ascii=False)
    saved_path = handler.file_write(rel_path, content)

    return {"success": True, "file_path": saved_path, "sprint_count": len(all_sprints)}


def export_sprint_report(params):
    """Export sprint report to a local JSON file (Server/DC only)."""
    board_id = params.get("board_id", "")
    sprint_id = params.get("sprint_id", "")
    output_file = params.get("output_file", "")

    if handler.is_cloud:
        return {"error": "Sprint reports are not available on Jira Cloud."}

    rel_path = _normalize_export_path(output_file)

    status, report, _ = handler.http(
        "GET",
        "/rest/greenhopper/1.0/rapid/charts/sprintreport",
        query={"rapidViewId": str(board_id), "sprintId": str(sprint_id)},
    )
    if status < 200 or status >= 300:
        return http_error(status, report)

    export_data = {
        "export_metadata": {
            "export_timestamp": datetime.now(timezone.utc).isoformat(),
            "board_id": board_id,
            "sprint_id": sprint_id,
            "tool": "jira_export_sprint_report",
        },
        "report": report,
    }

    content = json.dumps(export_data, indent=2, ensure_ascii=False)
    saved_path = handler.file_write(rel_path, content)

    return {"success": True, "file_path": saved_path}


def query_local_sprint_data(params):
    """Query and filter locally exported sprint data."""
    file_path = params.get("file_path", "")
    assignee = params.get("assignee")
    issue_status = params.get("status")
    issue_type = params.get("issue_type")
    labels = params.get("labels")
    priority = params.get("priority")
    brief = params.get("brief", True)

    path = _validate_read_path(file_path)

    with path.open("r", encoding="utf-8") as f:
        data = json.load(f)

    issues = data.get("issues", [])
    filtered = issues

    if assignee:
        a = assignee.lower()
        filtered = [
            i for i in filtered if a in (i.get("fields", {}).get("assignee") or {}).get("displayName", "").lower()
        ]

    if issue_status:
        s = issue_status.lower()
        filtered = [i for i in filtered if s in (i.get("fields", {}).get("status") or {}).get("name", "").lower()]

    if issue_type:
        t = issue_type.lower()
        filtered = [i for i in filtered if t in (i.get("fields", {}).get("issuetype") or {}).get("name", "").lower()]

    if labels:
        filtered = [i for i in filtered if all(lbl in i.get("fields", {}).get("labels", []) for lbl in labels)]

    if priority:
        p = priority.lower()
        filtered = [i for i in filtered if p in (i.get("fields", {}).get("priority") or {}).get("name", "").lower()]

    result: dict = {
        "total_issues": len(issues),
        "filtered_count": len(filtered),
        "sprint_info": data.get("sprint_info"),
    }

    if brief:
        result["issues"] = [extract_brief_issue(i) for i in filtered]
    else:
        result["issues"] = filtered
    return result


def compare_sprints(params):
    """Compare metrics across multiple exported sprint files."""
    file_paths = params.get("file_paths", [])
    if isinstance(file_paths, str):
        file_paths = [p.strip() for p in file_paths.split(",") if p.strip()]

    sprint_data = []
    for fp in file_paths:
        path = _validate_read_path(fp)

        with path.open("r", encoding="utf-8") as f:
            data = json.load(f)

        issues = data.get("issues", [])
        sprint_info = data.get("sprint_info", {})
        metrics = calculate_sprint_metrics(issues)
        metrics["sprint_name"] = sprint_info.get("name", "Unknown")
        metrics["sprint_id"] = sprint_info.get("id")
        metrics["file_path"] = str(path)
        sprint_data.append(metrics)

    return {"sprints": sprint_data, "comparison_count": len(sprint_data)}


def sprint_metrics_summary(params):
    """Generate metrics summary from exported sprint data."""
    file_path = params.get("file_path", "")

    path = _validate_read_path(file_path)

    with path.open("r", encoding="utf-8") as f:
        data = json.load(f)

    issues = data.get("issues", [])
    sprint_info = data.get("sprint_info", {})
    metrics = calculate_sprint_metrics(issues)
    metrics["sprint_name"] = sprint_info.get("name", "Unknown")
    metrics["sprint_id"] = sprint_info.get("id")
    metrics["sprint_state"] = sprint_info.get("state")
    metrics["source_file"] = str(path)
    return metrics


def read_cache_summary(params):
    """Read and summarize a cached/exported Jira response file."""
    file_path = params.get("file_path", "")
    fields = params.get("fields")
    issue_keys = params.get("issue_keys")
    max_issues = int(params.get("max_issues", 20))

    path = _validate_read_path(file_path)

    with path.open("r", encoding="utf-8") as f:
        cache_data = json.load(f)

    data = cache_data.get("data", cache_data)
    issues: list = []
    if isinstance(data, dict) and "issues" in data:
        issues = data["issues"]
    elif isinstance(data, list):
        issues = data

    if not issues:
        return {"file_path": file_path, "issue_count": 0, "message": "No issues found"}

    if issue_keys:
        keys = issue_keys if isinstance(issue_keys, list) else [k.strip() for k in issue_keys.split(",")]
        issues = [i for i in issues if i.get("key") in keys]

    total = len(issues)
    issues = issues[:max_issues]

    default_fields = ["key", "summary", "status.name", "priority.name", "assignee.displayName"]
    extract = fields if fields else default_fields

    summaries = []
    for issue in issues:
        fields_data = issue.get("fields", {})
        row: dict = {}
        for field in extract:
            if field == "key":
                row["key"] = issue.get("key", "")
            else:
                row[field] = extract_nested_field(fields_data, field)
        summaries.append(row)

    result: dict = {"file_path": file_path, "total_issues": total, "returned": len(summaries), "issues": summaries}
    if total > max_issues:
        result["note"] = f"Showing {max_issues} of {total}. Use issue_keys to filter."
    return result


def get_issue_from_cache(params):
    """Get a specific issue from a cached/exported file."""
    file_path = params.get("file_path", "")
    issue_key = validate_issue_key(params.get("issue_key", ""))

    path = _validate_read_path(file_path)

    with path.open("r", encoding="utf-8") as f:
        cache_data = json.load(f)

    data = cache_data.get("data", cache_data)
    issues: list = []
    if isinstance(data, dict) and "issues" in data:
        issues = data["issues"]
    elif isinstance(data, list):
        issues = data

    for issue in issues:
        if issue.get("key") == issue_key:
            fields_data = issue.get("fields", {})
            result: dict = {"key": issue.get("key"), "self": issue.get("self")}
            # Extract core fields into readable format
            for field, path_str in [
                ("summary", "summary"),
                ("description", "description"),
                ("status", "status.name"),
                ("priority", "priority.name"),
                ("issuetype", "issuetype.name"),
                ("resolution", "resolution.name"),
                ("assignee", "assignee.displayName"),
                ("reporter", "reporter.displayName"),
                ("created", "created"),
                ("updated", "updated"),
                ("labels", "labels"),
            ]:
                value = extract_nested_field(fields_data, path_str)
                # Convert ADF to plain text for description field
                if field == "description":
                    value = adf_to_text(value)
                result[field] = value
            return result

    return {"error": f"Issue {issue_key} not found in {file_path}"}


def debug_fields(params):
    """List Jira custom fields, optionally filtered by name."""
    search = params.get("search", "")

    status, body, _ = handler.http("GET", "/rest/api/2/field")
    if status < 200 or status >= 300:
        return http_error(status, body)

    fields_list = body if isinstance(body, list) else []
    custom_fields = []
    matching = []

    search_lower = search.lower() if search else None

    for field in fields_list:
        fid = field.get("id", "")
        fname = field.get("name", "")
        ftype = (field.get("schema") or {}).get("type", "")
        info = {"id": fid, "name": fname, "type": ftype}

        if fid.startswith("customfield_"):
            custom_fields.append(info)

        if search_lower and search_lower in fname.lower():
            matching.append(info)

    result: dict = {"total_fields": len(fields_list), "custom_fields_count": len(custom_fields)}

    if search_lower:
        result["search"] = search
        result["matching_fields"] = matching
        result["match_count"] = len(matching)
    else:
        result["sample_custom_fields"] = custom_fields[:10]

    return result


def download_attachment(params):
    """Download a Jira attachment by ID."""
    attachment_id = params.get("attachment_id", "")
    if not str(attachment_id).isdigit():
        raise ValueError(f"Invalid attachment_id: {attachment_id!r} (must be numeric)")

    # Get attachment metadata first
    status, meta, _ = handler.http("GET", f"/rest/api/2/attachment/{attachment_id}")
    if status < 200 or status >= 300:
        return meta

    filename = meta.get("filename", "unknown") if isinstance(meta, dict) else "unknown"
    default_mime = "application/octet-stream"
    mime_type = meta.get("mimeType", default_mime) if isinstance(meta, dict) else default_mime
    size = meta.get("size", 0) if isinstance(meta, dict) else 0

    # Get the content URL from metadata — on Server/DC this is a full URL
    # like https://jira.example.com/secure/attachment/123/file.txt,
    # on Cloud it's the REST content endpoint.
    content_url = meta.get("content", "") if isinstance(meta, dict) else ""
    if not content_url:
        return {"error": f"No content URL in attachment metadata for {attachment_id}"}

    # Download using the full URL from metadata
    status, content, headers = handler.http("GET", "", url=content_url)
    if status < 200 or status >= 300:
        return content if not isinstance(content, bytes) else {"error": "Download failed"}

    # If content came back as bytes (binary, auto-decoded from base64), re-encode for transport
    if isinstance(content, bytes):
        import base64

        return {
            "filename": filename,
            "mimeType": mime_type,
            "size": size,
            "encoding": "base64",
            "content": base64.b64encode(content).decode("ascii"),
        }

    # Text content
    return {"filename": filename, "mimeType": mime_type, "size": size, "encoding": "utf-8", "content": content}


def add_attachment(params):
    """Add a file attachment to a Jira issue."""
    issue_key = validate_issue_key(params.get("issue_key", ""))
    filename = params.get("filename", "")
    file_path = params.get("file_path", "")
    content_b64 = params.get("content", "")
    content_type = params.get("content_type", "application/octet-stream")
    dry_run = params.get("dry_run", True)

    import base64
    import mimetypes

    # Read from file_path or decode from base64 content
    if file_path:
        path = _validate_read_path(file_path)
        content = path.read_bytes()
        if not filename:
            filename = path.name
        if content_type == "application/octet-stream":
            guessed = mimetypes.guess_type(path.name)[0]
            if guessed:
                content_type = guessed
    elif content_b64:
        if not filename:
            raise ValueError("filename is required when using content parameter")
        try:
            content = base64.b64decode(content_b64)
        except Exception as e:
            raise ValueError(f"content is not valid base64: {e}") from e
    else:
        raise ValueError("either file_path or content is required")

    if dry_run:
        return {
            "dry_run": True,
            "action": "jira_add_attachment",
            "issue_key": issue_key,
            "filename": filename,
            "content_type": content_type,
            "size_bytes": len(content),
        }

    status, body, _ = handler.http_upload(
        "POST",
        f"/rest/api/2/issue/{issue_key}/attachments",
        field="file",
        filename=filename,
        content=content,
        content_type=content_type,
        headers={"X-Atlassian-Token": "no-check"},
    )
    if status < 200 or status >= 300:
        return http_error(status, body)

    handler.invalidate_cache(issue_key)

    if isinstance(body, list) and body:
        att = body[0]
        return {
            "success": True,
            "issue_key": issue_key,
            "id": att.get("id"),
            "filename": att.get("filename"),
            "size": att.get("size"),
            "mimeType": att.get("mimeType"),
        }
    return {"success": True, "issue_key": issue_key, "raw": body}


def delete_attachment(params):
    """Delete a Jira attachment by ID."""
    attachment_id = params.get("attachment_id", "")
    dry_run = params.get("dry_run", True)

    if not str(attachment_id).isdigit():
        raise ValueError(f"Invalid attachment_id: {attachment_id!r} (must be numeric)")

    if dry_run:
        return {"dry_run": True, "action": "jira_delete_attachment", "attachment_id": attachment_id}

    status, body, _ = handler.http("DELETE", f"/rest/api/2/attachment/{attachment_id}")
    if status < 200 or status >= 300:
        return http_error(status, body)
    handler.invalidate_cache()
    return {"success": True, "attachment_id": attachment_id}


TOOLS = {
    "jira_export_sprint_data": export_sprint_data,
    "jira_export_board_sprints": export_board_sprints,
    "jira_export_sprint_report": export_sprint_report,
    "jira_query_local_sprint_data": query_local_sprint_data,
    "jira_compare_sprints": compare_sprints,
    "jira_sprint_metrics_summary": sprint_metrics_summary,
    "jira_read_cache_summary": read_cache_summary,
    "jira_get_issue_from_cache": get_issue_from_cache,
    "jira_debug_fields": debug_fields,
    "jira_download_attachment": download_attachment,
    "jira_add_attachment": add_attachment,
    "jira_delete_attachment": delete_attachment,
}
