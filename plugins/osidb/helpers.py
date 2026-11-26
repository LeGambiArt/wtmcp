"""OSIDB plugin helper functions.

Pure utility functions with no protocol or I/O dependencies.
"""

from __future__ import annotations

import re

_CVE_RE = re.compile(r"^CVE-\d{4}-\d{4,}$")
_UUID_RE = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$", re.IGNORECASE)

FLAW_API = "/osidb/api/v1/flaws"
AFFECT_API = "/osidb/api/v1/affects"
TRACKER_API = "/osidb/api/v1/trackers"
TRACKER_FILE_API = "/trackers/api/v1/file"
LABEL_API = "/osidb/api/v1/labels"


def http_error(status, body):
    result = {"error": f"HTTP {status}"}
    if isinstance(body, dict):
        detail = body.get("detail") or body.get("message") or body.get("error")
        if detail:
            result["detail"] = detail
        else:
            non_meta = {k: v for k, v in body.items() if k not in ("dt", "env", "revision", "version")}
            if non_meta:
                result["detail"] = non_meta
    elif isinstance(body, str):
        if len(body) > 200:
            result["detail"] = body[:200] + "... (truncated)"
        elif body:
            result["detail"] = body
    return result


def is_error(status):
    return status < 200 or status >= 300


def is_cve_id(value):
    return bool(_CVE_RE.match(value))


def is_uuid(value):
    return bool(_UUID_RE.match(value))


def validate_flaw_id(value):
    if not (is_cve_id(value) or is_uuid(value)):
        return {"error": "Invalid flaw_id: expected CVE ID (CVE-YYYY-NNNN+) or UUID"}
    return None


def validate_path_segment(value, name="id"):
    s = str(value)
    if "/" in s or ".." in s:
        return {"error": f"Invalid {name}: must not contain '/' or '..'"}
    return None


def build_flaw_query(params, extra=None):
    """Build query dict for the flaws list endpoint from tool params."""
    DIRECT = [
        "cve_id",
        "impact",
        "source",
        "embargoed",
        "owner",
        "workflow_state",
        "workflow_name",
        "cwe_id",
        "title",
        "components",
        "major_incident_state",
        "bz_id",
        "search",
        "limit",
        "offset",
        "order",
    ]
    REMAP = {
        "owner_isempty": "owner__isempty",
        "created_dt_gte": "created_dt__gte",
        "created_dt_lte": "created_dt__lte",
        "updated_dt_gte": "updated_dt__gte",
        "updated_dt_lte": "updated_dt__lte",
        "changed_after": "changed_after",
        "changed_before": "changed_before",
        "affects_ps_module": "affects__ps_module",
        "affects_ps_component": "affects__ps_component",
        "affects_ps_update_stream": "affects__ps_update_stream",
        "affects_affectedness": "affects__affectedness",
        "affects_resolution": "affects__resolution",
        "label_contributor": "labels__contributor",
        "label_contributor_isempty": "labels__contributor__isempty",
        "label_state": "labels__state",
        "include_history": "include_history",
    }
    query = {}
    for key in DIRECT:
        val = params.get(key)
        if val is not None:
            query[key] = val
    for param_key, api_key in REMAP.items():
        val = params.get(param_key)
        if val is not None:
            query[api_key] = val

    flaw_labels = params.get("flaw_labels")
    if flaw_labels:
        if isinstance(flaw_labels, list):
            query["flaw_labels"] = ",".join(flaw_labels)
        else:
            query["flaw_labels"] = flaw_labels

    djangoql = params.get("query")
    if djangoql:
        query["query"] = normalize_djangoql(djangoql)

    include_fields = params.get("include_fields")
    if include_fields:
        if isinstance(include_fields, list):
            query["include_fields"] = ",".join(include_fields)
        else:
            query["include_fields"] = include_fields

    exclude_fields = params.get("exclude_fields")
    if exclude_fields:
        if isinstance(exclude_fields, list):
            query["exclude_fields"] = ",".join(exclude_fields)
        else:
            query["exclude_fields"] = exclude_fields

    if extra:
        query.update(extra)

    if "limit" not in query:
        query["limit"] = 50

    return query


def build_affect_query(params):
    """Build query dict for the affects list endpoint."""
    DIRECT = [
        "flaw",
        "affectedness",
        "resolution",
        "impact",
        "ps_module",
        "ps_component",
        "ps_update_stream",
        "embargoed",
        "limit",
        "offset",
        "order",
    ]
    REMAP = {
        "flaw_cve_id": "flaw__cve_id",
        "flaw_uuid": "flaw__uuid",
        "created_dt_gte": "created_dt__gte",
        "created_dt_lte": "created_dt__lte",
        "updated_dt_gte": "updated_dt__gte",
        "updated_dt_lte": "updated_dt__lte",
    }
    query = {}
    for key in DIRECT:
        val = params.get(key)
        if val is not None:
            query[key] = val
    for param_key, api_key in REMAP.items():
        val = params.get(param_key)
        if val is not None:
            query[api_key] = val

    include_fields = params.get("include_fields")
    if include_fields:
        if isinstance(include_fields, list):
            query["include_fields"] = ",".join(include_fields)
        else:
            query["include_fields"] = include_fields

    exclude_fields = params.get("exclude_fields")
    if exclude_fields:
        if isinstance(exclude_fields, list):
            query["exclude_fields"] = ",".join(exclude_fields)
        else:
            query["exclude_fields"] = exclude_fields

    if "limit" not in query:
        query["limit"] = 50

    return query


def build_tracker_query(params):
    """Build query dict for the trackers list endpoint."""
    DIRECT = [
        "type",
        "external_system_id",
        "status",
        "resolution",
        "ps_update_stream",
        "limit",
        "offset",
        "order",
    ]
    REMAP = {
        "affect_uuid": "affects",
        "flaw_cve_id": "affects__flaw__cve_id",
        "flaw_uuid": "affects__flaw__uuid",
        "created_dt_gte": "created_dt__gte",
        "created_dt_lte": "created_dt__lte",
        "updated_dt_gte": "updated_dt__gte",
        "updated_dt_lte": "updated_dt__lte",
    }
    query = {}
    for key in DIRECT:
        val = params.get(key)
        if val is not None:
            query[key] = val
    for param_key, api_key in REMAP.items():
        val = params.get(param_key)
        if val is not None:
            query[api_key] = val

    include_fields = params.get("include_fields")
    if include_fields:
        if isinstance(include_fields, list):
            query["include_fields"] = ",".join(include_fields)
        else:
            query["include_fields"] = include_fields

    exclude_fields = params.get("exclude_fields")
    if exclude_fields:
        if isinstance(exclude_fields, list):
            query["exclude_fields"] = ",".join(exclude_fields)
        else:
            query["exclude_fields"] = exclude_fields

    if "limit" not in query:
        query["limit"] = 50

    return query


_DJANGOQL_KEYWORDS = re.compile(
    r"\b(AND|OR|NOT|TRUE|FALSE|NONE|IN|NOT\s+IN|STARTSWITH|NOT\s+STARTSWITH|ENDSWITH|NOT\s+ENDSWITH)\b"
)


def normalize_djangoql(query):
    """Lowercase DjangoQL keywords outside quoted strings."""
    parts = re.split(r"""("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')""", query)
    for i, part in enumerate(parts):
        if i % 2 == 0:
            parts[i] = _DJANGOQL_KEYWORDS.sub(lambda m: m.group(0).lower(), part)
    return "".join(parts)


def subresource_query(params):
    """Build query dict for sub-resource list endpoints (comments, refs, etc.)."""
    query = {"limit": params.get("limit", 50)}
    offset = params.get("offset")
    if offset is not None:
        query["offset"] = offset
    return query


def dry_run_response(action, **details):
    result = {"dry_run": True, "action": action}
    result.update(details)
    return result
