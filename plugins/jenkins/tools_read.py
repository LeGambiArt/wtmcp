"""Jenkins plugin read-only tools.

All tools import http and cache functions from handler module.
"""

import hashlib
import urllib.parse

import handler
import helpers


def _cache_key_hash(text):
    """Generate 12-char SHA256 hash for cache keys."""
    return hashlib.sha256(text.encode()).hexdigest()[:12]


def _make_cache_key(*parts):
    """Build a cache key scoped to the current Jenkins URL.

    Includes a hash of handler._jenkins_url so keys from different
    Jenkins instances never collide.
    """
    url_hash = _cache_key_hash(handler._jenkins_url) if handler._jenkins_url else "nourl"
    return ":".join(["jenkins", url_hash, *parts])


def _cache_if_numeric_build(cache_key, result, build_number):
    """Cache permanently only when build_number is numeric, not an alias like 'lastBuild'."""
    try:
        int(build_number)
        handler.cache_set(cache_key, result, ttl=0)
    except (ValueError, TypeError):
        pass


def _setup_url(params):
    """Extract and set jenkins_url from params or fallback to config.

    Returns error dict or None.

    Priority:
    1. jenkins_url from tool call params (highest)
    2. JENKINS_URL from environment/config (fallback)
    """
    jenkins_url = params.get("jenkins_url", "").strip()

    # If no URL provided in params, try config/env fallback
    if not jenkins_url:
        jenkins_url = handler.config.get("jenkins_url", "").strip()

    return handler.set_jenkins_url(jenkins_url)


def jenkins_whoami(params):
    """Get authenticated user identity.

    Args:
        params: dict with jenkins_url

    Returns:
        dict: User info with id, fullName, email
    """
    err = _setup_url(params)
    if err:
        return err

    cache_key = _make_cache_key("whoami")

    # Check cache
    cached = handler.cache_get(cache_key)
    if cached is not None:
        return cached

    # Fetch from Jenkins
    status, body, headers = handler.http("GET", "/me/api/json")

    if status not in (200, 201):
        return {"error": f"HTTP {status}", "detail": str(body)}

    # Extract email from property array (Mailer UserProperty)
    email = ""
    for prop in body.get("property", []):
        if isinstance(prop, dict) and "address" in prop:
            email = prop["address"]
            break

    # Extract compact user info
    result = {
        "id": body.get("id", ""),
        "fullName": body.get("fullName", ""),
        "email": email,
    }

    # Cache for 1 hour
    handler.cache_set(cache_key, result, 3600)

    return result


def jenkins_list_jobs(params):
    """List Jenkins jobs and pipelines.

    Args:
        params: dict with jenkins_url, optional folder, name_filter, max_results

    Returns:
        dict: Jobs list with count
    """
    err = _setup_url(params)
    if err:
        return err

    folder = params.get("folder", "")
    name_filter = params.get("name_filter", "")
    max_results = params.get("max_results", 50)

    # Validate max_results
    if not isinstance(max_results, int) or isinstance(max_results, bool):
        return {"error": "invalid_input", "detail": "max_results must be an integer"}
    if max_results < 1:
        return {"error": "invalid_input", "detail": "max_results must be positive"}
    if max_results > 500:
        max_results = 500  # Clamp to reasonable upper bound

    # Coerce name_filter to string
    if name_filter:
        name_filter = str(name_filter)

    # Validate folder name if provided
    if folder:
        try:
            folder = helpers.validate_job_name(folder)
        except ValueError as e:
            return {"error": "invalid_input", "detail": str(e)}

    # Build cache key
    folder_hash = _cache_key_hash(folder) if folder else "root"
    cache_key = _make_cache_key("jobs", folder_hash)

    # Check cache
    cached = handler.cache_get(cache_key)
    if cached is not None:
        # Apply filters to cached data
        jobs = cached["jobs"]
        if name_filter:
            name_filter_lower = name_filter.lower()
            jobs = [j for j in jobs if name_filter_lower in j["name"].lower()]
        jobs = jobs[:max_results]
        return {"count": len(jobs), "jobs": jobs}

    # Build API path
    if folder:
        path = f"/{helpers.job_path(folder)}/api/json"
    else:
        path = "/api/json"

    # Add tree parameter to limit response size
    tree_param = "jobs[name,_class,url,color,healthReport[score],lastBuild[number,result,timestamp]]"

    # Fetch from Jenkins
    status, body, headers = handler.http("GET", path, query={"tree": tree_param})

    if status not in (200, 201):
        return {"error": f"HTTP {status}", "detail": str(body)}

    # Check for auth redirect (HTML login page)
    auth_error = helpers.check_auth_redirect(status, body, headers)
    if auth_error:
        return auth_error

    # Extract job list
    jenkins_jobs = body.get("jobs", [])
    jobs = [helpers.extract_brief_job(job, folder) for job in jenkins_jobs]

    # Cache for 5 minutes (job list changes moderately)
    handler.cache_set(cache_key, {"count": len(jobs), "jobs": jobs}, ttl=300)

    # Apply filters
    if name_filter:
        name_filter_lower = name_filter.lower()
        jobs = [j for j in jobs if name_filter_lower in j["name"].lower()]

    jobs = jobs[:max_results]

    return {"count": len(jobs), "jobs": jobs}


def jenkins_get_build(params):
    """Get detailed build information.

    Args:
        params: dict with jenkins_url, job_name, build_number

    Returns:
        dict: Build details with result, duration, causes, etc.
    """
    err = _setup_url(params)
    if err:
        return err

    job_name = params.get("job_name", "")
    build_number = params.get("build_number", "")

    # Validate inputs
    try:
        job_name = helpers.validate_job_name(job_name)
        build_number = helpers.validate_build_number(build_number)
    except ValueError as e:
        return {"error": "invalid_input", "detail": str(e)}

    # Build cache key
    job_hash = _cache_key_hash(job_name)
    cache_key = _make_cache_key("build", job_hash, build_number)

    # Check cache
    cached = handler.cache_get(cache_key)
    if cached is not None:
        return cached

    # Build API path with tree parameter
    path = f"/{helpers.job_path(job_name)}/{build_number}/api/json"
    tree_param = (
        "number,result,duration,timestamp,building,"
        "actions[causes[shortDescription],parameters[name,value]],"
        "changeSets[items[msg,author[fullName],commitId]],"
        "artifacts[fileName]"
    )

    # Fetch from Jenkins
    status, body, headers = handler.http("GET", path, query={"tree": tree_param})

    if status not in (200, 201):
        return {"error": f"HTTP {status}", "detail": str(body)}

    # Check for HTML redirect
    auth_error = helpers.check_auth_redirect(status, body, headers)
    if auth_error:
        return auth_error

    # Extract compact build data
    result = helpers.extract_brief_build(body)
    result["job_name"] = job_name

    # Cache completed builds permanently, don't cache running builds
    if not result.get("building", False):
        # If build_number was an alias, cache under the resolved numeric number
        # This prevents stale alias lookups while still caching the actual build
        resolved_number = result.get("number")
        if resolved_number is not None:
            # Check if build_number is numeric (not an alias)
            try:
                int(build_number)
                # Numeric lookup - cache as-is
                handler.cache_set(cache_key, result, 0)
            except (ValueError, TypeError):
                # Alias lookup - cache under resolved number instead
                resolved_cache_key = _make_cache_key("build", job_hash, str(resolved_number))
                handler.cache_set(resolved_cache_key, result, 0)
        # else: no number in response, don't cache

    return result


# Constants for log processing
_MAX_LOG_LINES = 5000
_DEFAULT_TAIL_LINES = 200
_MAX_LOG_BYTES = 512 * 1024  # 512 KB


def jenkins_get_build_log(params):
    """Retrieve console output from a build.

    CRITICAL: All log content is scrubbed for secrets before returning.

    Args:
        params: dict with jenkins_url, job_name, build_number, tail_lines (optional),
                grep (optional), max_bytes (optional)

    Returns:
        dict: Log content with metadata
    """
    err = _setup_url(params)
    if err:
        return err

    job_name = params.get("job_name", "")
    build_number = params.get("build_number", "")
    tail_lines = params.get("tail_lines", _DEFAULT_TAIL_LINES)
    grep_pattern = params.get("grep", "")
    max_bytes = params.get("max_bytes", _MAX_LOG_BYTES)

    # Validate inputs
    try:
        job_name = helpers.validate_job_name(job_name)
        build_number = helpers.validate_build_number(build_number)
        tail_lines = max(0, min(int(tail_lines), _MAX_LOG_LINES))
        max_bytes = max(0, min(int(max_bytes), _MAX_LOG_BYTES))
    except ValueError as e:
        return {"error": "invalid_input", "detail": str(e)}

    # Build cache key including tail_lines and grep
    job_hash = _cache_key_hash(job_name)
    grep_hash = _cache_key_hash(grep_pattern) if grep_pattern else "none"
    cache_key = _make_cache_key("log", job_hash, build_number, str(tail_lines), grep_hash)

    # Check cache
    cached = handler.cache_get(cache_key)
    if cached is not None:
        return cached

    # First, check if build is complete (to determine caching strategy)
    build_path = f"/{helpers.job_path(job_name)}/{build_number}/api/json"
    build_status, build_body, _ = handler.http("GET", build_path, query={"tree": "building"})

    building = None  # Unknown unless confirmed
    if build_status in (200, 201) and isinstance(build_body, dict):
        building = build_body.get("building", False)

    # Fetch console log
    log_path = f"/{helpers.job_path(job_name)}/{build_number}/consoleText"
    status, body, headers = handler.http("GET", log_path)

    if status not in (200, 201):
        return {"error": f"HTTP {status}", "detail": str(body)}

    # Check for HTML redirect (login page)
    auth_error = helpers.check_auth_redirect(status, body, headers)
    if auth_error:
        return auth_error

    # Convert body to string if needed
    if isinstance(body, bytes):
        body = body.decode("utf-8", errors="replace")
    if not isinstance(body, str):
        body = str(body)

    # Apply log scrubbing (CRITICAL security control)
    log_text = helpers.scrub_log_content(body)

    # Apply grep filter if specified
    if grep_pattern:
        log_text = helpers.filter_log_lines(log_text, grep_pattern)

    # Apply tail truncation
    truncated_log, total_lines = helpers.truncate_log(log_text, tail_lines)

    # Truncate by bytes if needed
    if len(truncated_log.encode("utf-8")) > max_bytes:
        truncated_log = truncated_log.encode("utf-8")[:max_bytes].decode("utf-8", errors="ignore")

    # Apply truncate_result for final size control (on the log string)
    truncated_log = helpers.truncate_result(truncated_log)

    # Build result
    result = {
        "job_name": job_name,
        "build_number": build_number,
        "log": truncated_log,
        "line_count": len(truncated_log.splitlines()) if truncated_log else 0,
        "total_lines": total_lines,
        "truncated": total_lines > tail_lines if tail_lines > 0 else False,
        "building": building,
    }

    # Add hint if truncated
    if result["truncated"]:
        result["hint"] = f"Log truncated to last {tail_lines} lines. Increase tail_lines for more context."

    if building is False:
        _cache_if_numeric_build(cache_key, result, build_number)

    return result


def jenkins_get_job(params):
    """Get detailed job metadata.

    Args:
        params: dict with jenkins_url, job_name, max_builds (optional, default 10)

    Returns:
        dict: Job metadata with description, parameters, health, last builds
    """
    err = _setup_url(params)
    if err:
        return err

    job_name = params.get("job_name", "")
    max_builds = params.get("max_builds", 10)

    # Validate inputs
    try:
        job_name = helpers.validate_job_name(job_name)
        if not isinstance(max_builds, int) or isinstance(max_builds, bool):
            return {"error": "invalid_input", "detail": "max_builds must be an integer"}
        max_builds = max(1, min(int(max_builds), 50))  # Clamp to [1, 50]
    except ValueError as e:
        return {"error": "invalid_input", "detail": str(e)}

    # Build cache key
    job_hash = _cache_key_hash(job_name)
    cache_key = _make_cache_key("job", job_hash)

    # Check cache
    cached = handler.cache_get(cache_key)
    if cached is not None:
        # Slice last_builds to max_builds (cached data has full list)
        result = cached.copy()
        result["last_builds"] = cached.get("last_builds", [])[:max_builds]
        return result

    # Build API path with tree parameter
    path = f"/{helpers.job_path(job_name)}/api/json"
    tree_param = (
        "name,_class,description,url,color,"
        "healthReport[score],"
        "property[_class,parameterDefinitions[name,type,defaultValue,choices]],"
        "builds[number]"
    )

    # Fetch from Jenkins
    status, body, headers = handler.http("GET", path, query={"tree": tree_param})

    if status not in (200, 201):
        return {"error": f"HTTP {status}", "detail": str(body)}

    # Check for HTML redirect
    auth_error = helpers.check_auth_redirect(status, body, headers)
    if auth_error:
        return auth_error

    # Extract health score
    health = None
    health_reports = body.get("healthReport", [])
    if health_reports and len(health_reports) > 0:
        health = health_reports[0].get("score")

    # Extract parameters
    parameters = []
    for prop in body.get("property", []):
        if "ParametersDefinitionProperty" in prop.get("_class", ""):
            for param_def in prop.get("parameterDefinitions", []):
                param_info = {
                    "name": param_def.get("name", ""),
                    "type": param_def.get("type", "").replace("ParameterDefinition", ""),
                }
                if "defaultValue" in param_def:
                    param_info["default"] = param_def["defaultValue"]
                if "choices" in param_def:
                    param_info["choices"] = param_def["choices"]
                parameters.append(param_info)

    # Extract builds (cache full list up to 50, truncate on return)
    builds = body.get("builds", [])
    all_builds = [b["number"] for b in builds[:50] if "number" in b]

    # Build result with full builds list for caching
    result = {
        "name": body.get("name", ""),
        "type": helpers.detect_job_type(body),
        "description": body.get("description", ""),
        "url": body.get("url", ""),
        "health": health,
        "parameters": parameters,
        "last_builds": all_builds,
    }

    # Cache for 5 minutes (job metadata changes infrequently)
    handler.cache_set(cache_key, result, ttl=300)

    # Truncate last_builds to requested max_builds before returning
    result["last_builds"] = all_builds[:max_builds]
    return result


def jenkins_list_builds(params):
    """List build history for a job with filtering and pagination.

    Args:
        params: dict with jenkins_url, job_name, result_filter (optional),
                max_results (optional, default 20), offset (optional, default 0)

    Returns:
        dict: Build list with metadata
    """
    err = _setup_url(params)
    if err:
        return err

    job_name = params.get("job_name", "")
    result_filter = params.get("result_filter", "")
    max_results = params.get("max_results", 20)
    offset = params.get("offset", 0)

    # Validate inputs
    try:
        job_name = helpers.validate_job_name(job_name)
        if not isinstance(max_results, int) or isinstance(max_results, bool):
            return {"error": "invalid_input", "detail": "max_results must be an integer"}
        if not isinstance(offset, int) or isinstance(offset, bool):
            return {"error": "invalid_input", "detail": "offset must be an integer"}
        max_results = max(1, min(int(max_results), 100))  # Clamp to [1, 100]
        offset = max(0, int(offset))
    except ValueError as e:
        return {"error": "invalid_input", "detail": str(e)}

    # Validate result_filter if provided
    if result_filter:
        _VALID_RESULTS = {"SUCCESS", "FAILURE", "UNSTABLE", "ABORTED", "NOT_BUILT"}
        if result_filter not in _VALID_RESULTS:
            return {
                "error": "invalid_input",
                "detail": f"result_filter must be one of: {', '.join(sorted(_VALID_RESULTS))}",
            }

    # Build cache key (include filter and pagination params)
    job_hash = _cache_key_hash(job_name)
    filter_hash = _cache_key_hash(result_filter) if result_filter else "all"
    cache_key = _make_cache_key("builds", job_hash, filter_hash, str(offset), str(max_results))

    # Check cache
    cached = handler.cache_get(cache_key)
    if cached is not None:
        return cached

    # Build API path with tree parameter
    path = f"/{helpers.job_path(job_name)}/api/json"
    tree_param = "builds[number,result,timestamp,duration,building]"

    # Fetch from Jenkins
    status, body, headers = handler.http("GET", path, query={"tree": tree_param})

    if status not in (200, 201):
        return {"error": f"HTTP {status}", "detail": str(body)}

    # Check for HTML redirect
    auth_error = helpers.check_auth_redirect(status, body, headers)
    if auth_error:
        return auth_error

    # Extract builds
    builds = body.get("builds", [])

    # Apply result filter if specified
    if result_filter:
        builds = [b for b in builds if b.get("result") == result_filter]

    # Apply pagination
    builds = builds[offset : offset + max_results]

    # Build result
    result = {
        "job_name": job_name,
        "count": len(builds),
        "builds": builds,
        "offset": offset,
        "result_filter": result_filter if result_filter else None,
    }

    # Cache for 5 minutes (build list changes moderately)
    handler.cache_set(cache_key, result, ttl=300)

    return result


def jenkins_list_views(params):
    """List all Jenkins views.

    Args:
        params: dict with jenkins_url, name_filter (optional)

    Returns:
        dict: Views list
    """
    err = _setup_url(params)
    if err:
        return err

    name_filter = params.get("name_filter", "")

    # Coerce name_filter to string
    if name_filter:
        name_filter = str(name_filter)

    # Build cache key
    cache_key = _make_cache_key("views", "all")

    # Check cache
    cached = handler.cache_get(cache_key)
    if cached is not None:
        if not name_filter:
            return {"count": cached["count"], "views": cached["views"]}
        # Apply filter to cached data
        views = cached["views"]
        name_filter_lower = name_filter.lower()
        views = [v for v in views if name_filter_lower in v["name"].lower()]
        return {"count": len(views), "views": views}

    # Fetch from Jenkins
    path = "/api/json"
    tree_param = "views[name,url,_class]"

    status, body, headers = handler.http("GET", path, query={"tree": tree_param})

    if status not in (200, 201):
        return {"error": f"HTTP {status}", "detail": str(body)}

    # Check for HTML redirect
    auth_error = helpers.check_auth_redirect(status, body, headers)
    if auth_error:
        return auth_error

    # Extract views
    views = body.get("views", [])

    # Cache for 10 minutes (views change infrequently)
    handler.cache_set(cache_key, {"count": len(views), "views": views}, ttl=600)

    # Apply filter
    if name_filter:
        name_filter_lower = name_filter.lower()
        views = [v for v in views if name_filter_lower in v["name"].lower()]

    return {"count": len(views), "views": views}


def jenkins_get_view(params):
    """Get jobs in a specific view.

    Args:
        params: dict with jenkins_url, view_name, name_filter (optional)

    Returns:
        dict: View info with job list
    """
    err = _setup_url(params)
    if err:
        return err

    view_name = params.get("view_name", "")
    name_filter = params.get("name_filter", "")

    # Validate view name
    if not view_name or not isinstance(view_name, str):
        return {"error": "invalid_input", "detail": "view_name is required"}
    view_name = view_name.strip()
    if not view_name:
        return {"error": "invalid_input", "detail": "view_name is required"}
    # Basic validation (no path traversal)
    if ".." in view_name or view_name.startswith("/"):
        return {"error": "invalid_input", "detail": "invalid view name"}

    # Coerce name_filter to string
    if name_filter:
        name_filter = str(name_filter)

    # Build cache key
    view_hash = _cache_key_hash(view_name)
    cache_key = _make_cache_key("view", view_hash)

    # Check cache
    cached = handler.cache_get(cache_key)
    if cached is not None:
        # Apply filter to cached data
        jobs = list(cached["jobs"])
        if name_filter:
            name_filter_lower = name_filter.lower()
            jobs = [j for j in jobs if name_filter_lower in j["name"].lower()]
        return {
            "name": cached["name"],
            "description": cached.get("description"),
            "count": len(jobs),
            "jobs": jobs,
        }

    # Build API path
    path = f"/view/{urllib.parse.quote(view_name, safe='')}/api/json"
    tree_param = "name,description,jobs[name,_class,url,color]"

    # Fetch from Jenkins
    status, body, headers = handler.http("GET", path, query={"tree": tree_param})

    if status not in (200, 201):
        return {"error": f"HTTP {status}", "detail": str(body)}

    # Check for HTML redirect
    auth_error = helpers.check_auth_redirect(status, body, headers)
    if auth_error:
        return auth_error

    # Extract jobs
    jobs = body.get("jobs", [])

    # Build result
    result = {
        "name": body.get("name", view_name),
        "description": body.get("description"),
        "count": len(jobs),
        "jobs": jobs,
    }

    # Cache for 10 minutes
    handler.cache_set(cache_key, result, ttl=600)

    # Apply filter
    if name_filter:
        name_filter_lower = name_filter.lower()
        jobs = [j for j in jobs if name_filter_lower in j["name"].lower()]
        result = {
            "name": result["name"],
            "description": result["description"],
            "count": len(jobs),
            "jobs": jobs,
        }

    return result


def jenkins_get_queue(params):
    """Get current build queue status.

    Queue is never cached (always fresh status).

    Args:
        params: dict with jenkins_url, stuck_only (optional, default False)

    Returns:
        dict: Queue items
    """
    err = _setup_url(params)
    if err:
        return err

    stuck_only = params.get("stuck_only", False)

    # Fetch from Jenkins (no caching - queue changes constantly)
    path = "/queue/api/json"
    tree_param = "items[id,task[name],why,inQueueSince,stuck]"

    status, body, headers = handler.http("GET", path, query={"tree": tree_param})

    if status not in (200, 201):
        return {"error": f"HTTP {status}", "detail": str(body)}

    # Check for HTML redirect
    auth_error = helpers.check_auth_redirect(status, body, headers)
    if auth_error:
        return auth_error

    # Extract queue items
    items = body.get("items", [])

    # Filter for stuck items if requested
    if stuck_only:
        items = [item for item in items if item.get("stuck", False)]

    # Build compact queue entries
    queue = []
    for item in items:
        task = item.get("task", {})
        queue.append(
            {
                "id": item.get("id"),
                "job_name": task.get("name", ""),
                "reason": item.get("why", ""),
                "queued_since": item.get("inQueueSince"),
                "stuck": item.get("stuck", False),
            }
        )

    return {
        "count": len(queue),
        "queue": queue,
    }


def jenkins_flush_cache(params):
    """Flush plugin cache (admin/debug tool).

    Requires confirmation to prevent accidental flushes.

    Args:
        params: dict with confirm (optional, default True for backward compat)

    Returns:
        dict: Flush status
    """
    confirm = params.get("confirm", True)

    # Require confirmation (safety check)
    if not confirm:
        return {
            "status": "cancelled",
            "message": "Cache flush cancelled. Set confirm=true to proceed. Confirmation required.",
        }

    # Flush cache for jenkins namespace
    success = handler.cache_flush("jenkins")

    if success:
        return {
            "status": "flushed",
            "scope": "all",
            "message": "All Jenkins plugin cache entries have been cleared.",
        }
    else:
        return {
            "error": "cache_flush_failed",
            "detail": "Failed to flush cache. Check handler logs.",
        }


def jenkins_get_test_results(params):
    """Get JUnit test results for a build.

    Args:
        params: dict with jenkins_url, job_name, build_number

    Returns:
        dict: Test results with pass/fail/skip counts and failure details
    """
    err = _setup_url(params)
    if err:
        return err

    job_name = params.get("job_name", "")
    build_number = params.get("build_number", "")

    # Validate inputs
    try:
        job_name = helpers.validate_job_name(job_name)
        build_number = helpers.validate_build_number(build_number)
    except ValueError as e:
        return {"error": "invalid_input", "detail": str(e)}

    # Build cache key
    job_hash = _cache_key_hash(job_name)
    cache_key = _make_cache_key("test_results", job_hash, build_number)

    # Check cache
    cached = handler.cache_get(cache_key)
    if cached is not None:
        return cached

    # First, get build info to check if tests exist and if build complete
    build_path = f"/{helpers.job_path(job_name)}/{build_number}/api/json"
    build_tree = "building,actions[_class,totalCount,failCount,skipCount]"

    status, body, headers = handler.http("GET", build_path, query={"tree": build_tree})

    if status not in (200, 201):
        return {"error": f"HTTP {status}", "detail": str(body)}

    auth_error = helpers.check_auth_redirect(status, body, headers)
    if auth_error:
        return auth_error

    building = body.get("building", False)

    # Find TestResultAction
    test_action = None
    for action in body.get("actions", []):
        if "TestResultAction" in action.get("_class", ""):
            test_action = action
            break

    if not test_action:
        return {
            "error": "no_test_results",
            "detail": "Build has no test results. Ensure tests ran and published JUnit XML.",
        }

    # Fetch JUnit XML report
    junit_path = f"/{helpers.job_path(job_name)}/{build_number}/testReport/api/xml"

    status, xml_body, headers = handler.http("GET", junit_path)

    if status not in (200, 201):
        return {"error": f"HTTP {status}", "detail": str(xml_body)}

    auth_error = helpers.check_auth_redirect(status, xml_body, headers)
    if auth_error:
        return auth_error

    # Parse JUnit XML
    parsed = helpers.parse_junit_xml(xml_body)

    if "error" in parsed:
        return parsed

    # Cap failures list to prevent huge responses
    failures = parsed["failures"]
    total_failures = len(failures)
    max_failures = 50
    if len(failures) > max_failures:
        failures = failures[:max_failures]

    # Build result
    result = {
        "job_name": job_name,
        "build_number": build_number,
        "total": parsed["total"],
        "passed": parsed["passed"],
        "failed": parsed["failed"],
        "errors": parsed["errors"],
        "skipped": parsed["skipped"],
        "duration_seconds": parsed["duration_seconds"],
        "failures": failures,
        "total_failures": total_failures,
        "building": building,
    }

    if not building:
        _cache_if_numeric_build(cache_key, result, build_number)

    return result


def jenkins_get_pipeline_stages(params):
    """Get pipeline stage breakdown (requires Pipeline plugin).

    Args:
        params: dict with jenkins_url, job_name, build_number

    Returns:
        dict: Stage list with name, status, duration, start time
    """
    err = _setup_url(params)
    if err:
        return err

    job_name = params.get("job_name", "")
    build_number = params.get("build_number", "")

    # Validate inputs
    try:
        job_name = helpers.validate_job_name(job_name)
        build_number = helpers.validate_build_number(build_number)
    except ValueError as e:
        return {"error": "invalid_input", "detail": str(e)}

    # Build cache key
    job_hash = _cache_key_hash(job_name)
    cache_key = _make_cache_key("pipeline_stages", job_hash, build_number)

    # Check cache
    cached = handler.cache_get(cache_key)
    if cached is not None:
        return cached

    # Fetch pipeline stages via wfapi
    wfapi_path = f"/{helpers.job_path(job_name)}/{build_number}/wfapi/describe"

    status, body, headers = handler.http("GET", wfapi_path)

    # Graceful degradation if Pipeline plugin not installed
    if status == 404:
        return {
            "error": "pipeline_not_supported",
            "detail": (
                "Pipeline plugin not installed or job is not a Pipeline."
                " Install the Pipeline plugin or use a different tool."
            ),
        }

    if status not in (200, 201):
        return {"error": f"HTTP {status}", "detail": str(body)}

    auth_error = helpers.check_auth_redirect(status, body, headers)
    if auth_error:
        return auth_error

    # Extract stages
    stages = body.get("stages", [])

    # Build compact stage list
    stage_list = []
    for stage in stages:
        stage_list.append(
            {
                "id": stage.get("id", ""),
                "name": stage.get("name", ""),
                "status": stage.get("status", "UNKNOWN"),
                "start_time_ms": stage.get("startTimeMillis"),
                "duration_ms": stage.get("durationMillis"),
            }
        )

    # Check if build is complete (fetch build info)
    build_path = f"/{helpers.job_path(job_name)}/{build_number}/api/json"
    build_status, build_body, _ = handler.http("GET", build_path, query={"tree": "building"})

    building = False
    if build_status in (200, 201):
        building = build_body.get("building", False)

    # Build result
    result = {
        "job_name": job_name,
        "build_number": build_number,
        "count": len(stage_list),
        "stages": stage_list,
        "building": building,
    }

    if not building:
        _cache_if_numeric_build(cache_key, result, build_number)

    return result


def jenkins_get_stage_log(params):
    """Get console log for a specific pipeline stage.

    Uses same security controls as jenkins_get_build_log (scrubbing).

    Args:
        params: dict with jenkins_url, job_name, build_number, stage_id, tail_lines (optional)

    Returns:
        dict: Scrubbed stage log
    """
    err = _setup_url(params)
    if err:
        return err

    job_name = params.get("job_name", "")
    build_number = params.get("build_number", "")
    stage_id = params.get("stage_id", "")
    tail_lines = params.get("tail_lines", 200)

    # Validate inputs
    try:
        job_name = helpers.validate_job_name(job_name)
        build_number = helpers.validate_build_number(build_number)

        # Validate stage_id (should be numeric or safe string)
        if not stage_id or not isinstance(stage_id, str):
            return {"error": "invalid_input", "detail": "stage_id is required"}
        stage_id = str(stage_id).strip()
        if ".." in stage_id or "/" in stage_id:
            return {"error": "invalid_input", "detail": "invalid stage_id"}

        if not isinstance(tail_lines, int) or isinstance(tail_lines, bool):
            return {"error": "invalid_input", "detail": "tail_lines must be an integer"}
        tail_lines = max(0, min(int(tail_lines), 5000))
    except ValueError as e:
        return {"error": "invalid_input", "detail": str(e)}

    # Build cache key (include tail_lines)
    job_hash = _cache_key_hash(job_name)
    cache_key = _make_cache_key("stage_log", job_hash, build_number, stage_id, str(tail_lines))

    # Check cache
    cached = handler.cache_get(cache_key)
    if cached is not None:
        return cached

    # Fetch stage log via wfapi
    stage_log_path = f"/{helpers.job_path(job_name)}/{build_number}/execution/node/{stage_id}/wfapi/log"

    status, body, headers = handler.http("GET", stage_log_path)

    if status not in (200, 201):
        return {"error": f"HTTP {status}", "detail": str(body)}

    auth_error = helpers.check_auth_redirect(status, body, headers)
    if auth_error:
        return auth_error

    # wfapi/log returns JSON {"text": "..."} — extract the text field
    if isinstance(body, dict):
        body = body.get("text", "")

    # Scrub secrets
    scrubbed_log = helpers.scrub_log_content(body)

    # Apply tail mode
    if tail_lines > 0:
        scrubbed_log, _ = helpers.truncate_log(scrubbed_log, tail_lines)

    # Apply final size guard
    scrubbed_log = helpers.truncate_result(scrubbed_log)

    # Count lines in the (possibly truncated) log
    line_count = len(scrubbed_log.splitlines()) if scrubbed_log else 0

    # Check if build is complete (for caching decision)
    build_path = f"/{helpers.job_path(job_name)}/{build_number}/api/json"
    build_status, build_body, _ = handler.http("GET", build_path, query={"tree": "building"})

    building = False
    if build_status in (200, 201):
        building = build_body.get("building", False)

    # Build result
    result = {
        "job_name": job_name,
        "build_number": build_number,
        "stage_id": stage_id,
        "log": scrubbed_log,
        "line_count": line_count,
        "building": building,
    }

    if not building:
        _cache_if_numeric_build(cache_key, result, build_number)

    return result


def jenkins_list_artifacts(params):
    """List artifacts for a build.

    Args:
        params: dict with jenkins_url, job_name, build_number

    Returns:
        dict: Artifact list with file names and relative paths
    """
    err = _setup_url(params)
    if err:
        return err

    job_name = params.get("job_name", "")
    build_number = params.get("build_number", "")

    try:
        job_name = helpers.validate_job_name(job_name)
        build_number = helpers.validate_build_number(build_number)
    except ValueError as e:
        return {"error": "invalid_input", "detail": str(e)}

    job_hash = _cache_key_hash(job_name)
    cache_key = _make_cache_key("artifacts", job_hash, build_number)

    cached = handler.cache_get(cache_key)
    if cached is not None:
        return cached

    path = f"/{helpers.job_path(job_name)}/{build_number}/api/json"
    tree_param = "building,artifacts[fileName,relativePath]"

    status, body, headers = handler.http("GET", path, query={"tree": tree_param})

    if status not in (200, 201):
        return {"error": f"HTTP {status}", "detail": str(body)}

    auth_error = helpers.check_auth_redirect(status, body, headers)
    if auth_error:
        return auth_error

    building = body.get("building", False)
    artifacts = body.get("artifacts", [])

    result = {
        "job_name": job_name,
        "build_number": build_number,
        "count": len(artifacts),
        "artifacts": [
            {"fileName": a.get("fileName", ""), "relativePath": a.get("relativePath", "")} for a in artifacts
        ],
    }

    if not building:
        _cache_if_numeric_build(cache_key, result, build_number)

    return result


def jenkins_get_artifact(params):
    """Download a specific artifact file from a build.

    Args:
        params: dict with jenkins_url, job_name, build_number, artifact_path, tail_lines (optional)

    Returns:
        dict: Artifact content (text or error)
    """
    err = _setup_url(params)
    if err:
        return err

    job_name = params.get("job_name", "")
    build_number = params.get("build_number", "")
    artifact_path = params.get("artifact_path", "")
    tail_lines = params.get("tail_lines", 0)

    try:
        job_name = helpers.validate_job_name(job_name)
        build_number = helpers.validate_build_number(build_number)
        if not isinstance(tail_lines, int) or isinstance(tail_lines, bool):
            tail_lines = 0
        tail_lines = max(0, min(int(tail_lines), _MAX_LOG_LINES))
    except ValueError as e:
        return {"error": "invalid_input", "detail": str(e)}

    if not artifact_path or not isinstance(artifact_path, str):
        return {"error": "invalid_input", "detail": "artifact_path is required"}
    artifact_path = artifact_path.strip()
    if ".." in artifact_path:
        return {"error": "invalid_input", "detail": "path traversal not allowed"}

    job_hash = _cache_key_hash(job_name)
    path_hash = _cache_key_hash(artifact_path)
    cache_key = _make_cache_key("artifact", job_hash, build_number, path_hash, str(tail_lines))

    cached = handler.cache_get(cache_key)
    if cached is not None:
        return cached

    safe_path = "/".join(urllib.parse.quote(seg, safe="") for seg in artifact_path.split("/"))
    url_path = f"/{helpers.job_path(job_name)}/{build_number}/artifact/{safe_path}"

    status, body, headers = handler.http("GET", url_path)

    if status not in (200, 201):
        return {"error": f"HTTP {status}", "detail": str(body)}

    auth_error = helpers.check_auth_redirect(status, body, headers)
    if auth_error:
        return auth_error

    if isinstance(body, bytes):
        body = body.decode("utf-8", errors="replace")
    if not isinstance(body, str):
        body = str(body)

    content = helpers.scrub_log_content(body)

    if tail_lines > 0:
        content, _ = helpers.truncate_log(content, tail_lines)

    content = helpers.truncate_result(content)

    result = {
        "job_name": job_name,
        "build_number": build_number,
        "artifact_path": artifact_path,
        "content": content,
        "size_bytes": len(body),
    }

    # Only cache permanently if build is complete and build_number is numeric
    build_path = f"/{helpers.job_path(job_name)}/{build_number}/api/json"
    build_status, build_body, _ = handler.http("GET", build_path, query={"tree": "building"})

    building = True
    if build_status in (200, 201) and isinstance(build_body, dict):
        building = build_body.get("building", True)

    if not building:
        _cache_if_numeric_build(cache_key, result, build_number)

    return result


# Tool registry for handler dispatch (security: whitelist pattern)
TOOLS = {
    "jenkins_whoami": jenkins_whoami,
    "jenkins_list_jobs": jenkins_list_jobs,
    "jenkins_get_build": jenkins_get_build,
    "jenkins_get_build_log": jenkins_get_build_log,
    "jenkins_get_job": jenkins_get_job,
    "jenkins_list_builds": jenkins_list_builds,
    "jenkins_list_views": jenkins_list_views,
    "jenkins_get_view": jenkins_get_view,
    "jenkins_get_queue": jenkins_get_queue,
    "jenkins_flush_cache": jenkins_flush_cache,
    "jenkins_get_test_results": jenkins_get_test_results,
    "jenkins_get_pipeline_stages": jenkins_get_pipeline_stages,
    "jenkins_get_stage_log": jenkins_get_stage_log,
    "jenkins_list_artifacts": jenkins_list_artifacts,
    "jenkins_get_artifact": jenkins_get_artifact,
}
