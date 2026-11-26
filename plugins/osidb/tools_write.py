"""OSIDB plugin write tools.

All write tools default to dry_run=True. Set dry_run=false to execute.
"""

from __future__ import annotations

import handler
from helpers import (
    AFFECT_API,
    FLAW_API,
    TRACKER_API,
    TRACKER_FILE_API,
    dry_run_response,
    http_error,
    is_error,
    validate_flaw_id,
    validate_path_segment,
)


def _api_post(path, body):
    status, resp_body, _headers = handler.http("POST", path, body=body)
    if is_error(status):
        return http_error(status, resp_body)
    return resp_body


def _api_put(path, body):
    status, resp_body, _headers = handler.http("PUT", path, body=body)
    if is_error(status):
        return http_error(status, resp_body)
    return resp_body


def _api_delete(path):
    status, body, _headers = handler.http("DELETE", path)
    if is_error(status):
        return http_error(status, body)
    return {"deleted": True}


def _fetch_current(path):
    status, body, _headers = handler.http("GET", path)
    if is_error(status):
        return None, http_error(status, body)
    return body, None


def _get_flaw_embargoed(flaw_id):
    status, body, _headers = handler.http("GET", f"{FLAW_API}/{flaw_id}", query={"include_fields": "embargoed"})
    if is_error(status):
        return None, http_error(status, body)
    return body.get("embargoed", False), None


def _get_affect_embargoed(affect_id):
    status, body, _headers = handler.http("GET", f"{AFFECT_API}/{affect_id}", query={"include_fields": "embargoed"})
    if is_error(status):
        return None, http_error(status, body)
    return body.get("embargoed", False), None


def _require_embargoed(params, fetch_fn, parent_id):
    embargoed = params.get("embargoed")
    if embargoed is not None:
        return embargoed, None
    val, err = fetch_fn(parent_id)
    if err:
        return None, err
    return val, None


# -- Flaws -------------------------------------------------------------------


def flaw_create(params):
    dry_run = params.get("dry_run", True)
    form_data = {}
    for key in (
        "title",
        "comment_zero",
        "cve_id",
        "impact",
        "components",
        "source",
        "cwe_id",
        "embargoed",
        "major_incident_state",
        "mitigation",
        "statement",
        "cve_description",
        "unembargo_dt",
        "reported_dt",
        "owner",
    ):
        val = params.get(key)
        if val is not None:
            form_data[key] = val
    if "embargoed" not in form_data:
        form_data["embargoed"] = False
    if dry_run:
        return dry_run_response("osidb_flaw_create", data=form_data)
    return _api_post(FLAW_API, form_data)


def flaw_update(params):
    flaw_id = params["flaw_id"]
    err = validate_flaw_id(flaw_id)
    if err:
        return err
    fields = params.get("fields", {})
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response("osidb_flaw_update", flaw_id=flaw_id, fields=fields)

    current, err = _fetch_current(f"{FLAW_API}/{flaw_id}")
    if err:
        return err
    current.update(fields)
    return _api_put(f"{FLAW_API}/{flaw_id}", current)


# -- Flaw Comments -----------------------------------------------------------


def flaw_comment_add(params):
    flaw_id = params["flaw_id"]
    err = validate_flaw_id(flaw_id)
    if err:
        return err
    text = params["text"]
    dry_run = params.get("dry_run", True)
    form_data = {"text": text}
    is_private = params.get("is_private")
    if is_private is not None:
        form_data["is_private"] = is_private
    embargoed, err = _require_embargoed(params, _get_flaw_embargoed, flaw_id)
    if err:
        return err
    form_data["embargoed"] = embargoed
    if dry_run:
        return dry_run_response("osidb_flaw_comment_add", flaw_id=flaw_id, data=form_data)
    return _api_post(f"{FLAW_API}/{flaw_id}/comments", form_data)


# -- Flaw References --------------------------------------------------------


def flaw_reference_add(params):
    flaw_id = params["flaw_id"]
    err = validate_flaw_id(flaw_id)
    if err:
        return err
    url = params["url"]
    dry_run = params.get("dry_run", True)
    form_data = {"url": url, "type": params.get("reference_type", "EXTERNAL")}
    description = params.get("description")
    if description:
        form_data["description"] = description
    embargoed, err = _require_embargoed(params, _get_flaw_embargoed, flaw_id)
    if err:
        return err
    form_data["embargoed"] = embargoed
    if dry_run:
        return dry_run_response("osidb_flaw_reference_add", flaw_id=flaw_id, data=form_data)
    return _api_post(f"{FLAW_API}/{flaw_id}/references", form_data)


def flaw_reference_update(params):
    flaw_id = params["flaw_id"]
    reference_id = params["reference_id"]
    err = validate_flaw_id(flaw_id) or validate_path_segment(reference_id, "reference_id")
    if err:
        return err
    fields = params.get("fields", {})
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response(
            "osidb_flaw_reference_update",
            flaw_id=flaw_id,
            reference_id=reference_id,
            fields=fields,
        )
    current, err = _fetch_current(f"{FLAW_API}/{flaw_id}/references/{reference_id}")
    if err:
        return err
    current.update(fields)
    return _api_put(f"{FLAW_API}/{flaw_id}/references/{reference_id}", current)


def flaw_reference_delete(params):
    flaw_id = params["flaw_id"]
    reference_id = params["reference_id"]
    err = validate_flaw_id(flaw_id) or validate_path_segment(reference_id, "reference_id")
    if err:
        return err
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response("osidb_flaw_reference_delete", flaw_id=flaw_id, reference_id=reference_id)
    return _api_delete(f"{FLAW_API}/{flaw_id}/references/{reference_id}")


# -- Flaw Acknowledgments ---------------------------------------------------


def flaw_acknowledgment_add(params):
    flaw_id = params["flaw_id"]
    err = validate_flaw_id(flaw_id)
    if err:
        return err
    name = params["name"]
    dry_run = params.get("dry_run", True)
    form_data = {
        "name": name,
        "affiliation": params.get("affiliation", ""),
        "from_upstream": params.get("from_upstream", False),
    }
    embargoed, err = _require_embargoed(params, _get_flaw_embargoed, flaw_id)
    if err:
        return err
    form_data["embargoed"] = embargoed
    if dry_run:
        return dry_run_response("osidb_flaw_acknowledgment_add", flaw_id=flaw_id, data=form_data)
    return _api_post(f"{FLAW_API}/{flaw_id}/acknowledgments", form_data)


def flaw_acknowledgment_update(params):
    flaw_id = params["flaw_id"]
    ack_id = params["ack_id"]
    err = validate_flaw_id(flaw_id) or validate_path_segment(ack_id, "ack_id")
    if err:
        return err
    fields = params.get("fields", {})
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response("osidb_flaw_acknowledgment_update", flaw_id=flaw_id, ack_id=ack_id, fields=fields)
    current, err = _fetch_current(f"{FLAW_API}/{flaw_id}/acknowledgments/{ack_id}")
    if err:
        return err
    current.update(fields)
    return _api_put(f"{FLAW_API}/{flaw_id}/acknowledgments/{ack_id}", current)


def flaw_acknowledgment_delete(params):
    flaw_id = params["flaw_id"]
    ack_id = params["ack_id"]
    err = validate_flaw_id(flaw_id) or validate_path_segment(ack_id, "ack_id")
    if err:
        return err
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response("osidb_flaw_acknowledgment_delete", flaw_id=flaw_id, ack_id=ack_id)
    return _api_delete(f"{FLAW_API}/{flaw_id}/acknowledgments/{ack_id}")


# -- Flaw CVSS Scores -------------------------------------------------------


def flaw_cvss_score_add(params):
    flaw_id = params["flaw_id"]
    err = validate_flaw_id(flaw_id)
    if err:
        return err
    vector = params["vector"]
    dry_run = params.get("dry_run", True)
    form_data = {
        "vector": vector,
        "issuer": params.get("issuer", "RH"),
        "cvss_version": params.get("cvss_version", "V3"),
    }
    comment = params.get("comment")
    if comment:
        form_data["comment"] = comment
    embargoed, err = _require_embargoed(params, _get_flaw_embargoed, flaw_id)
    if err:
        return err
    form_data["embargoed"] = embargoed
    if dry_run:
        return dry_run_response("osidb_flaw_cvss_score_add", flaw_id=flaw_id, data=form_data)
    return _api_post(f"{FLAW_API}/{flaw_id}/cvss_scores", form_data)


def flaw_cvss_score_update(params):
    flaw_id = params["flaw_id"]
    score_id = params["score_id"]
    err = validate_flaw_id(flaw_id) or validate_path_segment(score_id, "score_id")
    if err:
        return err
    fields = params.get("fields", {})
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response("osidb_flaw_cvss_score_update", flaw_id=flaw_id, score_id=score_id, fields=fields)
    current, err = _fetch_current(f"{FLAW_API}/{flaw_id}/cvss_scores/{score_id}")
    if err:
        return err
    current.update(fields)
    return _api_put(f"{FLAW_API}/{flaw_id}/cvss_scores/{score_id}", current)


def flaw_cvss_score_delete(params):
    flaw_id = params["flaw_id"]
    score_id = params["score_id"]
    err = validate_flaw_id(flaw_id) or validate_path_segment(score_id, "score_id")
    if err:
        return err
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response("osidb_flaw_cvss_score_delete", flaw_id=flaw_id, score_id=score_id)
    return _api_delete(f"{FLAW_API}/{flaw_id}/cvss_scores/{score_id}")


# -- Flaw Labels -------------------------------------------------------------


def flaw_label_add(params):
    flaw_id = params["flaw_id"]
    err = validate_flaw_id(flaw_id)
    if err:
        return err
    label_name = params["label_name"]
    dry_run = params.get("dry_run", True)
    form_data = {"name": label_name}
    state = params.get("state")
    if state:
        form_data["state"] = state
    contributor = params.get("contributor")
    if contributor:
        form_data["contributor"] = contributor
    embargoed, err = _require_embargoed(params, _get_flaw_embargoed, flaw_id)
    if err:
        return err
    form_data["embargoed"] = embargoed
    if dry_run:
        return dry_run_response("osidb_flaw_label_add", flaw_id=flaw_id, data=form_data)
    return _api_post(f"{FLAW_API}/{flaw_id}/labels", form_data)


def flaw_label_update(params):
    flaw_id = params["flaw_id"]
    label_id = params["label_id"]
    err = validate_flaw_id(flaw_id) or validate_path_segment(label_id, "label_id")
    if err:
        return err
    fields = params.get("fields", {})
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response("osidb_flaw_label_update", flaw_id=flaw_id, label_id=label_id, fields=fields)
    current, err = _fetch_current(f"{FLAW_API}/{flaw_id}/labels/{label_id}")
    if err:
        return err
    current.update(fields)
    return _api_put(f"{FLAW_API}/{flaw_id}/labels/{label_id}", current)


def flaw_label_delete(params):
    flaw_id = params["flaw_id"]
    label_id = params["label_id"]
    err = validate_flaw_id(flaw_id) or validate_path_segment(label_id, "label_id")
    if err:
        return err
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response("osidb_flaw_label_delete", flaw_id=flaw_id, label_id=label_id)
    return _api_delete(f"{FLAW_API}/{flaw_id}/labels/{label_id}")


# -- Flaw Package Versions --------------------------------------------------


def flaw_package_version_add(params):
    flaw_id = params["flaw_id"]
    err = validate_flaw_id(flaw_id)
    if err:
        return err
    dry_run = params.get("dry_run", True)
    form_data = {
        "package": params["package"],
        "versions": params["versions"],
    }
    embargoed, err = _require_embargoed(params, _get_flaw_embargoed, flaw_id)
    if err:
        return err
    form_data["embargoed"] = embargoed
    if dry_run:
        return dry_run_response("osidb_flaw_package_version_add", flaw_id=flaw_id, data=form_data)
    return _api_post(f"{FLAW_API}/{flaw_id}/package_versions", form_data)


def flaw_package_version_update(params):
    flaw_id = params["flaw_id"]
    version_id = params["version_id"]
    err = validate_flaw_id(flaw_id) or validate_path_segment(version_id, "version_id")
    if err:
        return err
    fields = params.get("fields", {})
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response(
            "osidb_flaw_package_version_update",
            flaw_id=flaw_id,
            version_id=version_id,
            fields=fields,
        )
    current, err = _fetch_current(f"{FLAW_API}/{flaw_id}/package_versions/{version_id}")
    if err:
        return err
    current.update(fields)
    return _api_put(f"{FLAW_API}/{flaw_id}/package_versions/{version_id}", current)


def flaw_package_version_delete(params):
    flaw_id = params["flaw_id"]
    version_id = params["version_id"]
    err = validate_flaw_id(flaw_id) or validate_path_segment(version_id, "version_id")
    if err:
        return err
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response("osidb_flaw_package_version_delete", flaw_id=flaw_id, version_id=version_id)
    return _api_delete(f"{FLAW_API}/{flaw_id}/package_versions/{version_id}")


# -- Affects -----------------------------------------------------------------


def affect_create(params):
    dry_run = params.get("dry_run", True)
    form_data = {}
    for key in (
        "flaw",
        "affectedness",
        "resolution",
        "impact",
        "ps_module",
        "ps_component",
        "ps_update_stream",
        "embargoed",
    ):
        val = params.get(key)
        if val is not None:
            form_data[key] = val
    if "embargoed" not in form_data:
        flaw_id = form_data.get("flaw")
        if flaw_id:
            embargoed, err = _require_embargoed(params, _get_flaw_embargoed, flaw_id)
            if err:
                return err
            form_data["embargoed"] = embargoed
        else:
            form_data["embargoed"] = False
    if dry_run:
        return dry_run_response("osidb_affect_create", data=form_data)
    return _api_post(AFFECT_API, form_data)


def affect_update(params):
    affect_id = params["affect_id"]
    err = validate_path_segment(affect_id, "affect_id")
    if err:
        return err
    fields = params.get("fields", {})
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response("osidb_affect_update", affect_id=affect_id, fields=fields)
    current, err = _fetch_current(f"{AFFECT_API}/{affect_id}")
    if err:
        return err
    current.update(fields)
    return _api_put(f"{AFFECT_API}/{affect_id}", current)


def affect_delete(params):
    affect_id = params["affect_id"]
    err = validate_path_segment(affect_id, "affect_id")
    if err:
        return err
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response("osidb_affect_delete", affect_id=affect_id)
    return _api_delete(f"{AFFECT_API}/{affect_id}")


def affect_bulk_create(params):
    affects = params["affects"]
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response("osidb_affect_bulk_create", count=len(affects), affects=affects)
    return _api_post(f"{AFFECT_API}/bulk", affects)


def affect_bulk_update(params):
    affects = params["affects"]
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response("osidb_affect_bulk_update", count=len(affects), affects=affects)
    return _api_put(f"{AFFECT_API}/bulk", affects)


def affect_bulk_delete(params):
    affect_ids = params["affect_ids"]
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response("osidb_affect_bulk_delete", count=len(affect_ids), affect_ids=affect_ids)
    status, body, _headers = handler.http("DELETE", f"{AFFECT_API}/bulk", body=affect_ids)
    if is_error(status):
        return http_error(status, body)
    return {"deleted": True, "count": len(affect_ids)}


def affect_cvss_score_add(params):
    affect_id = params["affect_id"]
    err = validate_path_segment(affect_id, "affect_id")
    if err:
        return err
    vector = params["vector"]
    dry_run = params.get("dry_run", True)
    form_data = {
        "vector": vector,
        "issuer": params.get("issuer", "RH"),
        "cvss_version": params.get("cvss_version", "V3"),
    }
    comment = params.get("comment")
    if comment:
        form_data["comment"] = comment
    embargoed, err = _require_embargoed(params, _get_affect_embargoed, affect_id)
    if err:
        return err
    form_data["embargoed"] = embargoed
    if dry_run:
        return dry_run_response("osidb_affect_cvss_score_add", affect_id=affect_id, data=form_data)
    return _api_post(f"{AFFECT_API}/{affect_id}/cvss_scores", form_data)


def affect_cvss_score_delete(params):
    affect_id = params["affect_id"]
    score_id = params["score_id"]
    err = validate_path_segment(affect_id, "affect_id") or validate_path_segment(score_id, "score_id")
    if err:
        return err
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response("osidb_affect_cvss_score_delete", affect_id=affect_id, score_id=score_id)
    return _api_delete(f"{AFFECT_API}/{affect_id}/cvss_scores/{score_id}")


# -- Trackers ----------------------------------------------------------------


def tracker_suggest(params):
    flaw_uuids = params["flaw_uuids"]
    dry_run = params.get("dry_run", True)
    if isinstance(flaw_uuids, str):
        flaw_uuids = [flaw_uuids]
    if dry_run:
        return dry_run_response(
            "osidb_tracker_suggest",
            flaw_uuids=flaw_uuids,
            exclude_existing=params.get("exclude_existing", False),
        )
    query = {}
    if params.get("exclude_existing", False):
        query["exclude_existing_trackers"] = "true"
    status, body, _headers = handler.http(
        "POST", TRACKER_FILE_API, query=query or None, body={"flaw_uuids": flaw_uuids}
    )
    if is_error(status):
        return http_error(status, body)
    return body


def tracker_create(params):
    flaw_uuids = params["flaw_uuids"]
    dry_run = params.get("dry_run", True)
    if isinstance(flaw_uuids, str):
        flaw_uuids = [flaw_uuids]
    if dry_run:
        return dry_run_response("osidb_tracker_create", flaw_uuids=flaw_uuids)
    return _api_post(TRACKER_FILE_API, {"flaw_uuids": flaw_uuids})


def tracker_update(params):
    tracker_id = params["tracker_id"]
    err = validate_path_segment(tracker_id, "tracker_id")
    if err:
        return err
    fields = params.get("fields", {})
    dry_run = params.get("dry_run", True)
    if dry_run:
        return dry_run_response("osidb_tracker_update", tracker_id=tracker_id, fields=fields)
    current, err = _fetch_current(f"{TRACKER_API}/{tracker_id}")
    if err:
        return err
    current.update(fields)
    return _api_put(f"{TRACKER_API}/{tracker_id}", current)


# -- Tool registry -----------------------------------------------------------

TOOLS = {
    "osidb_flaw_create": flaw_create,
    "osidb_flaw_update": flaw_update,
    "osidb_flaw_comment_add": flaw_comment_add,
    "osidb_flaw_reference_add": flaw_reference_add,
    "osidb_flaw_reference_update": flaw_reference_update,
    "osidb_flaw_reference_delete": flaw_reference_delete,
    "osidb_flaw_acknowledgment_add": flaw_acknowledgment_add,
    "osidb_flaw_acknowledgment_update": flaw_acknowledgment_update,
    "osidb_flaw_acknowledgment_delete": flaw_acknowledgment_delete,
    "osidb_flaw_cvss_score_add": flaw_cvss_score_add,
    "osidb_flaw_cvss_score_update": flaw_cvss_score_update,
    "osidb_flaw_cvss_score_delete": flaw_cvss_score_delete,
    "osidb_flaw_label_add": flaw_label_add,
    "osidb_flaw_label_update": flaw_label_update,
    "osidb_flaw_label_delete": flaw_label_delete,
    "osidb_flaw_package_version_add": flaw_package_version_add,
    "osidb_flaw_package_version_update": flaw_package_version_update,
    "osidb_flaw_package_version_delete": flaw_package_version_delete,
    "osidb_affect_create": affect_create,
    "osidb_affect_update": affect_update,
    "osidb_affect_delete": affect_delete,
    "osidb_affect_bulk_create": affect_bulk_create,
    "osidb_affect_bulk_update": affect_bulk_update,
    "osidb_affect_bulk_delete": affect_bulk_delete,
    "osidb_affect_cvss_score_add": affect_cvss_score_add,
    "osidb_affect_cvss_score_delete": affect_cvss_score_delete,
    "osidb_tracker_suggest": tracker_suggest,
    "osidb_tracker_create": tracker_create,
    "osidb_tracker_update": tracker_update,
}
