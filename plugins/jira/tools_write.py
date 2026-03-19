"""Jira write tool implementations.

All write tools default to dry_run=true for safety.
"""

import handler
from helpers import http_error, normalize_components, resolve_field_value, text_to_adf, validate_issue_key


def create_issue(params):
    """Create a new Jira issue."""
    project = params.get("project", "")
    issue_type = params.get("issue_type", "")
    summary = params.get("summary", "")
    description = params.get("description", "")
    assignee = params.get("assignee")
    priority = params.get("priority")
    labels = params.get("labels")
    components = params.get("components")
    dry_run = params.get("dry_run", True)

    fields = {
        "project": {"key": project},
        "issuetype": {"name": issue_type},
        "summary": summary,
    }

    if description:
        fields["description"] = text_to_adf(description) if handler.is_cloud else description

    if assignee:
        if handler.is_cloud:
            fields["assignee"] = {"accountId": assignee}
        else:
            fields["assignee"] = {"name": assignee}

    if priority:
        fields["priority"] = {"name": priority} if isinstance(priority, str) else priority

    if labels:
        fields["labels"] = labels if isinstance(labels, list) else [labels]

    if components:
        comp_list = components if isinstance(components, list) else [components]
        fields["components"] = normalize_components(comp_list)

    for i, cf in enumerate(params.get("custom_fields", [])):
        if not isinstance(cf, dict):
            raise ValueError(f"custom_fields[{i}]: expected an object, got {type(cf).__name__}")
        if "field_id" not in cf:
            raise ValueError(
                f"custom_fields[{i}]: missing required key 'field_id'. "
                "Each custom field must have 'field_id' and 'value'."
            )
        if "value" not in cf:
            raise ValueError(
                f"custom_fields[{i}]: missing required key 'value'. Each custom field must have 'field_id' and 'value'."
            )
        cf_id = cf["field_id"]
        if not cf_id.startswith("customfield_"):
            raise ValueError(
                f"Invalid custom field ID: '{cf_id}' (must start with 'customfield_'). "
                "Use the standard parameters for built-in fields."
            )
        fields[cf_id], _ = resolve_field_value(cf["value"], cf.get("field_type", "auto"), is_cloud=handler.is_cloud)

    if dry_run:
        return {"dry_run": True, "action": "jira_create_issue", "fields": fields}

    status, body, _ = handler.http("POST", "/rest/api/2/issue", body={"fields": fields})
    if status < 200 or status >= 300:
        if isinstance(body, dict):
            return {"error": body.get("errors", body.get("errorMessages", body)), "status": status}
        return {"error": str(body) if body else f"HTTP {status} with no response body", "status": status}
    if not isinstance(body, dict) or "key" not in body:
        return {"error": "Unexpected response from Jira", "raw": str(body)[:200], "status": status}
    return {"key": body.get("key"), "id": body.get("id"), "self": body.get("self")}


def add_comment(params):
    """Add a comment to a Jira issue."""
    issue_key = validate_issue_key(params.get("issue_key", ""))
    comment = params.get("comment", "")
    dry_run = params.get("dry_run", True)

    if dry_run:
        return {"dry_run": True, "action": "jira_add_comment", "issue_key": issue_key, "comment_preview": comment[:200]}

    body_data = {"body": text_to_adf(comment)} if handler.is_cloud else {"body": comment}
    status, body, _ = handler.http("POST", f"/rest/api/2/issue/{issue_key}/comment", body=body_data)
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "id": body.get("id"), "issue_key": issue_key}


def edit_comment(params):
    """Edit an existing comment on a Jira issue."""
    issue_key = validate_issue_key(params.get("issue_key", ""))
    comment_id = params.get("comment_id", "")
    comment = params.get("comment", "")
    dry_run = params.get("dry_run", True)

    if not str(comment_id).isdigit():
        raise ValueError(f"Invalid comment_id: {comment_id!r} (must be numeric)")

    if dry_run:
        return {
            "dry_run": True,
            "action": "jira_edit_comment",
            "issue_key": issue_key,
            "comment_id": comment_id,
            "comment_preview": comment[:200],
        }

    body_data = {"body": text_to_adf(comment)} if handler.is_cloud else {"body": comment}
    status, body, _ = handler.http("PUT", f"/rest/api/2/issue/{issue_key}/comment/{comment_id}", body=body_data)
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "issue_key": issue_key, "comment_id": comment_id}


def transition_issue(params):
    """Transition a Jira issue to a new workflow status."""
    issue_key = validate_issue_key(params.get("issue_key", ""))
    transition_id = params.get("transition_id")
    resolution = params.get("resolution")
    dry_run = params.get("dry_run", True)

    if dry_run:
        result = {
            "dry_run": True,
            "action": "jira_transition_issue",
            "issue_key": issue_key,
            "transition_id": transition_id,
        }
        if resolution:
            result["resolution"] = resolution
        return result

    data: dict = {"transition": {"id": str(transition_id)}}
    if resolution:
        data["fields"] = {"resolution": {"name": resolution}}

    status, body, _ = handler.http("POST", f"/rest/api/2/issue/{issue_key}/transitions", body=data)
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "issue_key": issue_key, "transition_id": transition_id}


def assign_issue(params):
    """Assign a Jira issue to a user."""
    issue_key = validate_issue_key(params.get("issue_key", ""))
    assignee = params.get("assignee", "")
    dry_run = params.get("dry_run", True)

    if dry_run:
        return {"dry_run": True, "action": "jira_assign_issue", "issue_key": issue_key, "assignee": assignee}

    if handler.is_cloud:
        body_data = {"accountId": assignee}
    else:
        body_data = {"name": assignee}

    status, body, _ = handler.http("PUT", f"/rest/api/2/issue/{issue_key}/assignee", body=body_data)
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "issue_key": issue_key, "assignee": assignee}


def set_priority(params):
    """Set the priority of a Jira issue."""
    issue_key = validate_issue_key(params.get("issue_key", ""))
    priority = params.get("priority", "")
    dry_run = params.get("dry_run", True)

    if dry_run:
        return {"dry_run": True, "action": "jira_set_priority", "issue_key": issue_key, "priority": priority}

    fields = {"priority": {"name": priority} if isinstance(priority, str) else priority}
    status, body, _ = handler.http("PUT", f"/rest/api/2/issue/{issue_key}", body={"fields": fields})
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "issue_key": issue_key, "priority": priority}


def set_labels(params):
    """Set labels on a Jira issue (replaces existing)."""
    issue_key = validate_issue_key(params.get("issue_key", ""))
    labels = params.get("labels", [])
    dry_run = params.get("dry_run", True)

    if dry_run:
        return {"dry_run": True, "action": "jira_set_labels", "issue_key": issue_key, "labels": labels}

    status, body, _ = handler.http("PUT", f"/rest/api/2/issue/{issue_key}", body={"fields": {"labels": labels}})
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "issue_key": issue_key, "labels": labels}


def add_labels(params):
    """Add labels to a Jira issue (preserves existing)."""
    issue_key = validate_issue_key(params.get("issue_key", ""))
    labels = params.get("labels", [])
    dry_run = params.get("dry_run", True)

    if dry_run:
        return {"dry_run": True, "action": "jira_add_labels", "issue_key": issue_key, "labels_to_add": labels}

    data = {"update": {"labels": [{"add": lbl} for lbl in labels]}}
    status, body, _ = handler.http("PUT", f"/rest/api/2/issue/{issue_key}", body=data)
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "issue_key": issue_key, "labels_added": labels}


def remove_labels(params):
    """Remove labels from a Jira issue."""
    issue_key = validate_issue_key(params.get("issue_key", ""))
    labels = params.get("labels", [])
    dry_run = params.get("dry_run", True)

    if dry_run:
        return {"dry_run": True, "action": "jira_remove_labels", "issue_key": issue_key, "labels_to_remove": labels}

    data = {"update": {"labels": [{"remove": lbl} for lbl in labels]}}
    status, body, _ = handler.http("PUT", f"/rest/api/2/issue/{issue_key}", body=data)
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "issue_key": issue_key, "labels_removed": labels}


def set_text_field(params):
    """Set a text field on a Jira issue."""
    issue_key = validate_issue_key(params.get("issue_key", ""))
    field_name = params.get("field_name", "")
    value = params.get("value", "")
    dry_run = params.get("dry_run", True)

    if dry_run:
        return {
            "dry_run": True,
            "action": "jira_set_text_field",
            "issue_key": issue_key,
            "field_name": field_name,
            "value_preview": str(value)[:100],
        }

    # Cloud v3 API requires ADF for description
    if isinstance(value, str) and handler.is_cloud and field_name == "description":
        value = text_to_adf(value)

    status, body, _ = handler.http("PUT", f"/rest/api/2/issue/{issue_key}", body={"fields": {field_name: value}})
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "issue_key": issue_key, "field_name": field_name}


def set_custom_field(params):
    """Set a custom field on a Jira issue."""
    issue_key = validate_issue_key(params.get("issue_key", ""))
    field_id = params.get("field_id", "")
    if not field_id.startswith("customfield_"):
        raise ValueError(
            f"field_id must start with 'customfield_', got '{field_id}'. Use jira_set_field for built-in fields."
        )
    value = params.get("value")
    field_type = params.get("field_type", "auto")
    dry_run = params.get("dry_run", True)

    field_value, field_type = resolve_field_value(value, field_type, is_cloud=handler.is_cloud)

    if dry_run:
        return {
            "dry_run": True,
            "action": "jira_set_custom_field",
            "issue_key": issue_key,
            "field_id": field_id,
            "field_type": field_type,
            "value": str(field_value)[:200],
        }

    status, body, _ = handler.http("PUT", f"/rest/api/2/issue/{issue_key}", body={"fields": {field_id: field_value}})
    if status < 200 or status >= 300:
        if isinstance(body, dict):
            return {"error": body.get("errors", body.get("errorMessages", body)), "status": status}
        return {"error": str(body) if body else f"HTTP {status}", "status": status}

    # Verify the field was actually updated (Jira silently ignores some invalid values)
    verify_status, verify_body, _ = handler.http("GET", f"/rest/api/2/issue/{issue_key}", query={"fields": field_id})
    if 200 <= verify_status < 300 and isinstance(verify_body, dict):
        actual = verify_body.get("fields", {}).get(field_id)
        if actual is None or actual == [] or actual == "":
            return {
                "warning": "Jira accepted the request but the field appears unchanged. "
                "Check the field_id and field_type are correct.",
                "issue_key": issue_key,
                "field_id": field_id,
                "field_type": field_type,
                "sent_value": str(field_value)[:200],
            }

    return {"success": True, "issue_key": issue_key, "field_id": field_id, "field_type": field_type}


def set_story_points(params):
    """Set story points on a Jira issue."""
    issue_key = validate_issue_key(params.get("issue_key", ""))
    points = params.get("points")
    field_id = params.get("field_id", "customfield_10028")
    dry_run = params.get("dry_run", True)

    if dry_run:
        return {
            "dry_run": True,
            "action": "jira_set_story_points",
            "issue_key": issue_key,
            "story_points": points,
            "field_id": field_id,
        }

    status, body, _ = handler.http("PUT", f"/rest/api/2/issue/{issue_key}", body={"fields": {field_id: float(points)}})
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "issue_key": issue_key, "story_points": points, "field_id": field_id}


def set_components(params):
    """Set components on a Jira issue (replaces existing)."""
    issue_key = validate_issue_key(params.get("issue_key", ""))
    components = params.get("components", [])
    dry_run = params.get("dry_run", True)

    comp_list = normalize_components(components if isinstance(components, list) else [components])

    if dry_run:
        return {"dry_run": True, "action": "jira_set_components", "issue_key": issue_key, "components": comp_list}

    status, body, _ = handler.http("PUT", f"/rest/api/2/issue/{issue_key}", body={"fields": {"components": comp_list}})
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "issue_key": issue_key, "components": comp_list}


def add_issue_link(params):
    """Add a link between two Jira issues."""
    link_type = params.get("link_type", "")
    inward_key = params.get("inward_issue_key", "")
    outward_key = params.get("outward_issue_key", "")
    comment = params.get("comment", "")
    dry_run = params.get("dry_run", True)

    payload = {
        "type": {"name": link_type},
        "inwardIssue": {"key": inward_key},
        "outwardIssue": {"key": outward_key},
    }
    if comment:
        payload["comment"] = {"body": text_to_adf(comment)} if handler.is_cloud else {"body": comment}

    if dry_run:
        return {"dry_run": True, "action": "jira_add_issue_link", "payload": payload}

    status, body, _ = handler.http("POST", "/rest/api/2/issueLink", body=payload)
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "link_type": link_type, "inward": inward_key, "outward": outward_key}


def delete_issue_link(params):
    """Delete an issue link."""
    link_id = params.get("link_id", "")

    status, body, _ = handler.http("DELETE", f"/rest/api/2/issueLink/{link_id}")
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "link_id": link_id}


def issue_worklog(params):
    """Add a worklog entry to a Jira issue."""
    issue_key = validate_issue_key(params.get("issue_key", ""))
    time_spent = params.get("time_spent", "")
    comment = params.get("comment")
    dry_run = params.get("dry_run", True)

    if dry_run:
        return {
            "dry_run": True,
            "action": "jira_issue_worklog",
            "issue_key": issue_key,
            "time_spent": time_spent,
            "comment": comment,
        }

    body_data = {"timeSpent": time_spent}
    if comment:
        body_data["comment"] = text_to_adf(comment) if handler.is_cloud else comment

    status, body, _ = handler.http("POST", f"/rest/api/2/issue/{issue_key}/worklog", body=body_data)
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "issue_key": issue_key, "time_spent": time_spent}


def add_issues_to_sprint(params):
    """Add issues to a sprint."""
    sprint_id = params.get("sprint_id")
    issue_keys = params.get("issue_keys", [])
    dry_run = params.get("dry_run", True)

    if isinstance(issue_keys, str):
        issue_keys = [k.strip() for k in issue_keys.split(",") if k.strip()]

    if dry_run:
        return {
            "dry_run": True,
            "action": "jira_add_issues_to_sprint",
            "sprint_id": sprint_id,
            "issue_keys": issue_keys,
        }

    status, body, _ = handler.http("POST", f"/rest/agile/1.0/sprint/{sprint_id}/issue", body={"issues": issue_keys})
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "sprint_id": sprint_id, "issue_keys": issue_keys}


def add_issues_to_backlog(params):
    """Move issues to the backlog (remove from sprints)."""
    issue_keys = params.get("issue_keys", [])
    dry_run = params.get("dry_run", True)

    if isinstance(issue_keys, str):
        issue_keys = [k.strip() for k in issue_keys.split(",") if k.strip()]

    if dry_run:
        return {"dry_run": True, "action": "jira_add_issues_to_backlog", "issue_keys": issue_keys}

    status, body, _ = handler.http("POST", "/rest/agile/1.0/backlog/issue", body={"issues": issue_keys})
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "issue_keys": issue_keys}


def set_parent(params):
    """Set the parent of one or more issues.

    Works for any hierarchy level: Outcome → Feature → Epic → Story/Task.
    Tries the standard REST API first; if that fails (screen restrictions),
    falls back to the Agile API (epic-level parents only).
    """
    parent_key = validate_issue_key(params.get("parent_key", ""))
    issue_keys = params.get("issue_keys", [])
    dry_run = params.get("dry_run", True)

    if isinstance(issue_keys, str):
        issue_keys = [k.strip() for k in issue_keys.split(",") if k.strip()]

    if not issue_keys:
        raise ValueError("issue_keys must contain at least one issue key")

    if dry_run:
        return {
            "dry_run": True,
            "action": "jira_set_parent",
            "parent_key": parent_key,
            "issue_keys": issue_keys,
        }

    # Try standard REST API (works for all hierarchy levels)
    rest_failed = []
    for key in issue_keys:
        status, body, _ = handler.http(
            "PUT", f"/rest/api/2/issue/{key}",
            body={"fields": {"parent": {"key": parent_key}}},
        )
        if status < 200 or status >= 300:
            rest_failed.append(key)

    if not rest_failed:
        return {"success": True, "parent_key": parent_key, "issue_keys": issue_keys}

    # Fallback: Agile API for issues that failed (handles epic screen restrictions)
    status, body, _ = handler.http(
        "POST", f"/rest/agile/1.0/epic/{parent_key}/issue",
        body={"issues": rest_failed},
    )
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "parent_key": parent_key, "issue_keys": issue_keys}


def clear_parent(params):
    """Remove the parent from one or more issues.

    Tries the standard REST API first; if that fails, falls back to the
    Agile API "none" epic endpoint.
    """
    issue_keys = params.get("issue_keys", [])
    dry_run = params.get("dry_run", True)

    if isinstance(issue_keys, str):
        issue_keys = [k.strip() for k in issue_keys.split(",") if k.strip()]

    if not issue_keys:
        raise ValueError("issue_keys must contain at least one issue key")

    if dry_run:
        return {
            "dry_run": True,
            "action": "jira_clear_parent",
            "issue_keys": issue_keys,
        }

    # Try standard REST API ({"key": null} clears the parent on Cloud)
    rest_failed = []
    for key in issue_keys:
        status, body, _ = handler.http(
            "PUT", f"/rest/api/2/issue/{key}",
            body={"fields": {"parent": {"key": None}}},
        )
        if status < 200 or status >= 300:
            rest_failed.append(key)

    if not rest_failed:
        return {"success": True, "issue_keys": issue_keys}

    # Fallback: Agile API moves issues out of any epic
    status, body, _ = handler.http(
        "POST", "/rest/agile/1.0/epic/none/issue",
        body={"issues": rest_failed},
    )
    if status < 200 or status >= 300:
        return http_error(status, body)
    return {"success": True, "issue_keys": issue_keys}


TOOLS = {
    "jira_create_issue": create_issue,
    "jira_add_comment": add_comment,
    "jira_edit_comment": edit_comment,
    "jira_transition_issue": transition_issue,
    "jira_assign_issue": assign_issue,
    "jira_set_priority": set_priority,
    "jira_set_labels": set_labels,
    "jira_add_labels": add_labels,
    "jira_remove_labels": remove_labels,
    "jira_set_text_field": set_text_field,
    "jira_set_custom_field": set_custom_field,
    "jira_set_story_points": set_story_points,
    "jira_set_components": set_components,
    "jira_add_issue_link": add_issue_link,
    "jira_delete_issue_link": delete_issue_link,
    "jira_issue_worklog": issue_worklog,
    "jira_add_issues_to_sprint": add_issues_to_sprint,
    "jira_add_issues_to_backlog": add_issues_to_backlog,
    "jira_set_parent": set_parent,
    "jira_clear_parent": clear_parent,
}
