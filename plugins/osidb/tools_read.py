"""OSIDB plugin read-only tools."""

from __future__ import annotations

import handler
from helpers import (
    AFFECT_API,
    FLAW_API,
    LABEL_API,
    TRACKER_API,
    build_affect_query,
    build_flaw_query,
    build_tracker_query,
    http_error,
    is_cve_id,
    is_error,
    subresource_query,
    validate_flaw_id,
    validate_path_segment,
)


def _api_get(path, query=None):
    status, body, _headers = handler.http("GET", path, query=query)
    if is_error(status):
        return http_error(status, body)
    return body


# -- Status ------------------------------------------------------------------


def status(params):
    return _api_get("/osidb/api/v1/status")


# -- Flaws -------------------------------------------------------------------


def flaw_get(params):
    flaw_id = params["flaw_id"]
    err = validate_flaw_id(flaw_id)
    if err:
        return err
    query = {}
    if params.get("include_history"):
        query["include_history"] = True
    include_fields = params.get("include_fields")
    if include_fields:
        query["include_fields"] = ",".join(include_fields) if isinstance(include_fields, list) else include_fields
    exclude_fields = params.get("exclude_fields")
    if exclude_fields:
        query["exclude_fields"] = ",".join(exclude_fields) if isinstance(exclude_fields, list) else exclude_fields
    return _api_get(f"{FLAW_API}/{flaw_id}", query=query or None)


def flaw_list(params):
    query = build_flaw_query(params)
    return _api_get(FLAW_API, query=query)


def flaw_search(params):
    query_str = params["query"]
    limit = params.get("limit", 50)
    query = {"search": query_str, "limit": limit}
    offset = params.get("offset")
    if offset is not None:
        query["offset"] = offset
    include_fields = params.get("include_fields")
    if include_fields:
        query["include_fields"] = ",".join(include_fields) if isinstance(include_fields, list) else include_fields
    exclude_fields = params.get("exclude_fields")
    if exclude_fields:
        query["exclude_fields"] = ",".join(exclude_fields) if isinstance(exclude_fields, list) else exclude_fields
    return _api_get(FLAW_API, query=query)


def my_flaws(params):
    email = params["email"]
    name = params.get("name")
    label = params.get("label", "hybrid_bu")
    limit = params.get("limit", 50)
    workflow_state = params.get("workflow_state")
    impact = params.get("impact")

    include_fields_param = params.get("include_fields") if not params.get("analyse") else None
    fields = "uuid,cve_id,title,impact,classification,owner,labels,created_dt,updated_dt"
    if include_fields_param:
        if isinstance(include_fields_param, list):
            field_set = set(include_fields_param)
        else:
            field_set = {f.strip() for f in include_fields_param.split(",")}
        for needed in ("uuid", "labels"):
            field_set.add(needed)
        fields = ",".join(sorted(field_set))

    base_query = {"limit": 100, "include_fields": fields}
    if workflow_state:
        base_query["workflow_state"] = workflow_state
    if impact:
        base_query["impact"] = impact

    seen_uuids = set()
    matched = []

    for extra in [{"owner": email}, {"flaw_labels": label}]:
        offset = 0
        while True:
            query = {**base_query, **extra, "offset": offset}
            result = _api_get(FLAW_API, query=query)
            if isinstance(result, dict) and "error" in result:
                return result

            for flaw in result.get("results", []):
                flaw_uuid = flaw.get("uuid")
                if flaw_uuid in seen_uuids:
                    continue

                if "flaw_labels" in extra:
                    flaw_labels = flaw.get("labels", [])
                    lbl = next((la for la in flaw_labels if la.get("name") == label), None)
                    if lbl is None:
                        continue
                    contributor = (lbl.get("contributor") or "").lower().strip()
                    if not _contributor_matches(contributor, email, name):
                        continue

                seen_uuids.add(flaw_uuid)
                matched.append(flaw)

            total = result.get("count", 0)
            offset += 100
            if offset >= total:
                break

    if params.get("analyse"):
        return _analyse_flaws(matched, email, label, name)

    return {"count": len(matched), "results": matched[:limit]}


def _contributor_matches(contributor, email, name=None):
    if not contributor:
        return False
    c = contributor.lower().strip()
    if c == email.lower():
        return True
    if name and c == name.lower().strip():
        return True
    prefix = email.split("@")[0].lower() if "@" in email else email.lower()
    if c == prefix:
        return True
    return False


_IMPACT_ORDER = {"CRITICAL": 0, "IMPORTANT": 1, "MODERATE": 2, "LOW": 3}
_WF_STATE_ORDER = {
    "NEW": 0,
    "TRIAGE": 1,
    "PRE_SECONDARY_ASSESSMENT": 2,
    "SECONDARY_ASSESSMENT": 3,
    "DONE": 4,
    "REJECTED": 5,
}
_LABEL_STATE_ORDER = {"NEW": 0, "REQ": 1, "SKIP": 2, "DONE": 3}


def _classify_state(flaw):
    clf = flaw.get("classification")
    if clf and isinstance(clf, dict):
        return clf.get("state", "") or ""
    return flaw.get("workflow_state") or ""


def _analyse_flaws(flaws, email, label, name=None):
    needs_attention = []
    completed = []

    for flaw in flaws:
        cve_id = flaw.get("cve_id", "")
        uuid = flaw.get("uuid", "")
        title = flaw.get("title", "")
        impact = flaw.get("impact") or "UNRATED"
        wf_state = _classify_state(flaw) or "UNKNOWN"
        owner = flaw.get("owner") or ""
        created_dt = str(flaw.get("created_dt", ""))

        lbl = next((la for la in flaw.get("labels", []) if la.get("name") == label), None)
        label_state = lbl.get("state", "") if lbl else ""
        label_contributor = lbl.get("contributor", "") if lbl else ""

        is_owner = owner.lower() == email.lower()
        is_contributor = _contributor_matches((label_contributor or "").lower(), email, name)
        role = "owner" if is_owner else "contributor"

        flaw_done = wf_state in ("DONE", "REJECTED")
        label_done = label_state in ("DONE", "SKIP")

        if flaw_done or (is_contributor and not is_owner and label_done):
            status = "done"
        else:
            status = "active"

        entry = {
            "cve_id": cve_id,
            "uuid": uuid,
            "title": title,
            "impact": impact,
            "workflow_state": wf_state,
            "label_state": label_state,
            "role": role,
            "owner": owner,
            "created_dt": created_dt,
        }

        if status == "active":
            needs_attention.append(entry)
        else:
            completed.append(entry)

    def sort_key(e):
        return (
            _IMPACT_ORDER.get(e["impact"], 99),
            _WF_STATE_ORDER.get(e["workflow_state"], 99),
            _LABEL_STATE_ORDER.get(e["label_state"], 99),
            e["created_dt"],
        )

    needs_attention.sort(key=sort_key)
    completed.sort(key=sort_key)

    return {
        "summary": {
            "total": len(needs_attention) + len(completed),
            "needs_attention": len(needs_attention),
            "completed": len(completed),
            "by_impact": {
                imp: sum(1 for e in needs_attention if e["impact"] == imp)
                for imp in ("CRITICAL", "IMPORTANT", "MODERATE", "LOW", "UNRATED")
                if any(e["impact"] == imp for e in needs_attention)
            },
        },
        "needs_attention": needs_attention,
        "completed": completed,
    }


# -- Flaw sub-resources ------------------------------------------------------


def flaw_comments(params):
    flaw_id = params["flaw_id"]
    err = validate_flaw_id(flaw_id)
    if err:
        return err
    return _api_get(f"{FLAW_API}/{flaw_id}/comments", query=subresource_query(params))


def flaw_references(params):
    flaw_id = params["flaw_id"]
    err = validate_flaw_id(flaw_id)
    if err:
        return err
    return _api_get(f"{FLAW_API}/{flaw_id}/references", query=subresource_query(params))


def flaw_acknowledgments(params):
    flaw_id = params["flaw_id"]
    err = validate_flaw_id(flaw_id)
    if err:
        return err
    return _api_get(f"{FLAW_API}/{flaw_id}/acknowledgments", query=subresource_query(params))


def flaw_cvss_scores(params):
    flaw_id = params["flaw_id"]
    err = validate_flaw_id(flaw_id)
    if err:
        return err
    return _api_get(f"{FLAW_API}/{flaw_id}/cvss_scores", query=subresource_query(params))


def flaw_labels(params):
    flaw_id = params["flaw_id"]
    err = validate_flaw_id(flaw_id)
    if err:
        return err
    return _api_get(f"{FLAW_API}/{flaw_id}/labels", query=subresource_query(params))


def flaw_package_versions(params):
    flaw_id = params["flaw_id"]
    err = validate_flaw_id(flaw_id)
    if err:
        return err
    return _api_get(f"{FLAW_API}/{flaw_id}/package_versions", query=subresource_query(params))


def flaw_promotion_status(params):
    flaw_id = params["flaw_id"]
    err = validate_flaw_id(flaw_id)
    if err:
        return err

    if is_cve_id(flaw_id):
        flaw = _api_get(f"{FLAW_API}/{flaw_id}", query={"include_fields": "uuid"})
        if isinstance(flaw, dict) and "error" in flaw:
            return flaw
        flaw_uuid = flaw.get("uuid")
    else:
        flaw_uuid = flaw_id

    path = f"/workflows/api/v1/workflows/{flaw_uuid}"
    query = {"next": "true", "verbose": "true"}
    status_code, body, _headers = handler.http("GET", path, query=query)
    if status_code == 404:
        return {"error": f"Workflows service returned 404 for flaw {flaw_id}. The flaw may not yet be indexed."}
    if is_error(status_code):
        return http_error(status_code, body)

    result = _extract_promotion_blockers(body)
    result["flaw_id"] = flaw_id
    result["flaw_uuid"] = flaw_uuid
    return result


def _extract_promotion_blockers(workflow_data):
    classification = workflow_data.get("classification", {})
    current_state = classification.get("state", "UNKNOWN")
    current_workflow = classification.get("workflow", "UNKNOWN")

    workflows = workflow_data.get("workflows", [])
    active_workflow = None
    for wf in workflows:
        if wf.get("name") == current_workflow:
            active_workflow = wf
            break

    if not active_workflow:
        return {
            "current_workflow": current_workflow,
            "current_state": current_state,
            "next_state": None,
            "promotion_blockers": [],
            "all_requirements": [],
            "ready_to_promote": False,
            "error": "Could not find active workflow in response",
        }

    states = active_workflow.get("states", [])
    current_idx = None
    for i, s in enumerate(states):
        if s["name"] == current_state:
            current_idx = i
            break

    next_state_info = None
    if current_idx is not None and current_idx + 1 < len(states):
        next_state_info = states[current_idx + 1]

    if not next_state_info:
        return {
            "current_workflow": current_workflow,
            "current_state": current_state,
            "next_state": None,
            "promotion_blockers": [],
            "all_requirements": [],
            "ready_to_promote": current_state == "DONE",
            "note": "Flaw is in terminal state" if current_state == "DONE" else "No next state found",
        }

    requirements = next_state_info.get("requirements", [])
    blockers = []
    all_reqs = []

    for req in requirements:
        req_info = {
            "name": req.get("name", ""),
            "accepts": req.get("accepts", False),
            "description": req.get("description", ""),
        }
        if "requirements" in req:
            req_info["sub_requirements"] = [
                {"name": r.get("name", ""), "accepts": r.get("accepts", False)} for r in req.get("requirements", [])
            ]
            req_info["condition"] = req.get("condition", "AND")
        all_reqs.append(req_info)
        if not req.get("accepts", False):
            blockers.append(req_info)

    return {
        "current_workflow": current_workflow,
        "current_state": current_state,
        "next_state": next_state_info["name"],
        "ready_to_promote": len(blockers) == 0,
        "promotion_blockers": blockers,
        "all_requirements": all_reqs,
    }


# -- Affects -----------------------------------------------------------------


def affect_get(params):
    affect_id = params["affect_id"]
    err = validate_path_segment(affect_id, "affect_id")
    if err:
        return err
    query = {}
    include_fields = params.get("include_fields")
    if include_fields:
        query["include_fields"] = ",".join(include_fields) if isinstance(include_fields, list) else include_fields
    exclude_fields = params.get("exclude_fields")
    if exclude_fields:
        query["exclude_fields"] = ",".join(exclude_fields) if isinstance(exclude_fields, list) else exclude_fields
    return _api_get(f"{AFFECT_API}/{affect_id}", query=query or None)


def affect_list(params):
    query = build_affect_query(params)
    return _api_get(AFFECT_API, query=query)


def affect_cvss_scores(params):
    affect_id = params["affect_id"]
    err = validate_path_segment(affect_id, "affect_id")
    if err:
        return err
    return _api_get(f"{AFFECT_API}/{affect_id}/cvss_scores", query=subresource_query(params))


# -- Trackers ----------------------------------------------------------------


def tracker_get(params):
    tracker_id = params["tracker_id"]
    err = validate_path_segment(tracker_id, "tracker_id")
    if err:
        return err
    query = {}
    include_fields = params.get("include_fields")
    if include_fields:
        query["include_fields"] = ",".join(include_fields) if isinstance(include_fields, list) else include_fields
    exclude_fields = params.get("exclude_fields")
    if exclude_fields:
        query["exclude_fields"] = ",".join(exclude_fields) if isinstance(exclude_fields, list) else exclude_fields
    return _api_get(f"{TRACKER_API}/{tracker_id}", query=query or None)


def tracker_list(params):
    query = build_tracker_query(params)
    return _api_get(TRACKER_API, query=query)


# -- Global Labels -----------------------------------------------------------


def label_list(params):
    query = {}
    limit = params.get("limit")
    if limit is not None:
        query["limit"] = limit
    offset = params.get("offset")
    if offset is not None:
        query["offset"] = offset
    return _api_get(LABEL_API, query=query or None)


def label_get(params):
    label_id = params["label_id"]
    err = validate_path_segment(label_id, "label_id")
    if err:
        return err
    return _api_get(f"{LABEL_API}/{label_id}")


# -- Tool registry -----------------------------------------------------------

TOOLS = {
    "osidb_status": status,
    "osidb_flaw_get": flaw_get,
    "osidb_flaw_list": flaw_list,
    "osidb_flaw_search": flaw_search,
    "osidb_my_flaws": my_flaws,
    "osidb_flaw_comments": flaw_comments,
    "osidb_flaw_references": flaw_references,
    "osidb_flaw_acknowledgments": flaw_acknowledgments,
    "osidb_flaw_cvss_scores": flaw_cvss_scores,
    "osidb_flaw_labels": flaw_labels,
    "osidb_flaw_package_versions": flaw_package_versions,
    "osidb_flaw_promotion_status": flaw_promotion_status,
    "osidb_affect_get": affect_get,
    "osidb_affect_list": affect_list,
    "osidb_affect_cvss_scores": affect_cvss_scores,
    "osidb_tracker_get": tracker_get,
    "osidb_tracker_list": tracker_list,
    "osidb_label_list": label_list,
    "osidb_label_get": label_get,
}
