"""Pure helper functions for Jenkins plugin.

All functions here have no I/O dependencies - they are pure input/output
transformations. This makes them easy to test and reason about.
"""

import re
import urllib.parse

# Constants
_MAX_JOB_NAME_LEN = 512

# Job name validation: each segment must start with alphanumeric,
# then contain alphanumeric, spaces, underscores, dots, or hyphens
_JOB_NAME_SEGMENT_RE = re.compile(r"^[a-zA-Z0-9][a-zA-Z0-9 _.\-]*$")


def validate_job_name(name):
    """Validate a Jenkins job name, preventing path traversal.

    Job names can contain folders with forward slashes: "team/project/job"

    Args:
        name: Job name to validate

    Returns:
        str: The validated job name (stripped of leading/trailing whitespace)

    Raises:
        ValueError: If the name is invalid
    """
    if not name or not isinstance(name, str):
        raise ValueError("job name is required")

    name = name.strip()

    if not name:
        raise ValueError("job name is required")

    if len(name) > _MAX_JOB_NAME_LEN:
        raise ValueError(f"job name too long: {len(name)} chars (max {_MAX_JOB_NAME_LEN})")

    if name.startswith("/") or name.endswith("/"):
        raise ValueError("job name must not start or end with '/'")

    segments = name.split("/")
    for segment in segments:
        if segment != segment.strip():
            raise ValueError(f"segment has leading/trailing whitespace in job name: '{name}'")
        if not segment:
            raise ValueError(f"empty segment in job name: '{name}'")
        if segment in (".", ".."):
            raise ValueError(f"path traversal in job name: '{name}'")
        if not _JOB_NAME_SEGMENT_RE.match(segment):
            raise ValueError(f"invalid characters in job name segment: '{segment}'")

    return name


# Build number aliases supported by Jenkins
_BUILD_ALIASES = frozenset(
    {
        "lastBuild",
        "lastStableBuild",
        "lastSuccessfulBuild",
        "lastFailedBuild",
        "lastUnstableBuild",
        "lastUnsuccessfulBuild",
        "lastCompletedBuild",
    }
)


def validate_build_number(value):
    """Validate a build number (positive integer or Jenkins alias).

    Args:
        value: Build number as int or string, or Jenkins alias

    Returns:
        str: The validated build number or alias

    Raises:
        ValueError: If the value is invalid
    """
    if isinstance(value, bool):
        raise ValueError(f"invalid build number: '{value}' (expected positive integer or alias)")
    if isinstance(value, int):
        if value < 1:
            raise ValueError(f"build number must be positive: {value}")
        return str(value)

    if isinstance(value, str):
        stripped = value.strip()
        if stripped in _BUILD_ALIASES:
            return stripped
        try:
            n = int(stripped)
        except ValueError:
            pass
        else:
            if n < 1:
                raise ValueError(f"build number must be positive: {stripped}")
            return str(n)

    raise ValueError(f"invalid build number: '{value}' (expected positive integer or alias)")


def job_path(job_name):
    """Convert 'folder/subfolder/job' to 'job/folder/job/subfolder/job/job'.

    Jenkins folder structure uses 'job/<name>' for each level.

    Args:
        job_name: Job name (will be validated)

    Returns:
        str: Jenkins API path with URL-encoded segments
    """
    # Validate input (defense-in-depth)
    job_name = validate_job_name(job_name)
    parts = job_name.split("/")
    # URL-encode each segment individually
    encoded_parts = [urllib.parse.quote(p, safe="") for p in parts]
    return "/".join(f"job/{p}" for p in encoded_parts)


# Secret patterns for log scrubbing
_SECRET_PATTERNS = [
    # Private key blocks (MUST be first - multiline, non-greedy)
    # Matches from BEGIN to END including all key material
    re.compile(
        r"-----BEGIN (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----"
        r".*?"
        r"-----END (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----",
        re.DOTALL,
    ),
    # Bearer tokens (opaque, non-JWT) in HTTP headers
    re.compile(r"(?i)Bearer\s+\S+"),
    # GitHub Personal Access Tokens
    re.compile(r"ghp_[a-zA-Z0-9]{36}"),
    re.compile(r"gho_[a-zA-Z0-9]{36}"),
    re.compile(r"ghs_[a-zA-Z0-9]{36}"),
    # GitHub fine-grained Personal Access Tokens
    re.compile(r"github_pat_[a-zA-Z0-9_]{20,}"),
    # GitLab Personal Access Tokens
    re.compile(r"glpat-[a-zA-Z0-9_\-]{20,}"),
    # AWS Access Key IDs
    re.compile(r"AKIA[0-9A-Z]{16}"),
    # AWS Secret Access Keys (with or without key name prefix)
    re.compile(r"(?i)aws[_-]?secret[_-]?access[_-]?key\s*[=:]\s*\S+"),
    # AWS Session Tokens (FwoGZXIv prefix is common)
    re.compile(r"(?i)aws[_-]?session[_-]?token\s*[=:]\s*\S+"),
    re.compile(r"FwoGZXIv[a-zA-Z0-9+/=]+"),
    # JWTs - all three segments (header.payload.signature)
    re.compile(r"eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+"),
    # Connection strings (before generic patterns to match full URL)
    re.compile(r"(?i)(?:jdbc:[a-z]+|mongodb|redis|amqp|mysql|postgres)://[^\s]+"),
    # Common secret patterns in CI output
    re.compile(
        r"(?i)(password|passwd|secret|token|api[_-]?key|"
        r"private[_-]?key|access[_-]?key|bearer)\s*[=:]\s*\S+"
    ),
    # Generic high-entropy strings in key=value context
    re.compile(
        r"(?i)(?:export\s+)?[A-Z_]{0,64}(?:SECRET|TOKEN|PASSWORD"
        r"|KEY|CREDENTIAL)[A-Z_]{0,64}\s*=\s*\S+"
    ),
]


def scrub_log_content(text):
    """Remove likely secrets from build log output.

    Applies regex patterns to detect and redact secrets before
    returning logs to the LLM. Patterns are applied in order, with
    multiline patterns (private keys) first.

    Args:
        text: Log content to scrub. If None or non-string, returns
              an empty string.

    Returns:
        str: Log content with secrets replaced by [REDACTED], or
             empty string if input is None/non-string.
    """
    if not isinstance(text, str):
        return ""
    for pattern in _SECRET_PATTERNS:
        text = pattern.sub("[REDACTED]", text)
    return text


def truncate_log(log_text, tail_lines):
    """Truncate log to last N lines (tail mode).

    Args:
        log_text: Full log content
        tail_lines: Number of lines from end (0 = full log)

    Returns:
        tuple: (truncated_text, total_line_count)
    """
    if not log_text:
        return "", 0

    lines = log_text.splitlines(keepends=True)
    total_count = len(lines)

    if tail_lines == 0 or tail_lines >= total_count:
        return log_text, total_count

    # Return last N lines
    return "".join(lines[-tail_lines:]), total_count


def filter_log_lines(log_text, pattern):
    """Filter log lines matching a case-insensitive pattern.

    Args:
        log_text: Full log content
        pattern: Pattern to match (case-insensitive substring)

    Returns:
        str: Lines matching the pattern, joined by newlines
    """
    if not pattern or not log_text:
        return log_text

    pattern_lower = pattern.lower()
    matching_lines = []

    for line in log_text.splitlines():
        if pattern_lower in line.lower():
            matching_lines.append(line)

    return "\n".join(matching_lines)


def check_auth_redirect(status, body, headers):
    """Detect Jenkins HTML login redirects and HTTP redirects to login pages.

    Jenkins behind a reverse proxy often returns an HTML login page
    with 200 status instead of 401/403, or redirects with 302/303.

    Args:
        status: HTTP status code
        body: Response body (dict or str)
        headers: Response headers dict

    Returns:
        dict with error details if login page detected, None otherwise
    """
    # Check for HTTP redirect to login page (302/303)
    if status in (302, 303):
        location = headers.get("Location", "")
        if location:
            location_lower = location.lower()
            # Common Jenkins login URLs
            if any(pattern in location_lower for pattern in ["login", "securityrealm", "auth", "signin"]):
                return {
                    "error": "authentication_failed",
                    "detail": f"Jenkins redirected to login page ({location}). Check credentials.",
                }

    # Check for HTML login page in response body
    content_type = headers.get("Content-Type", "")
    if "text/html" in content_type and isinstance(body, str):
        body_lower = body.lower()
        if "<form" in body_lower and ("j_security_check" in body_lower or "login" in body_lower):
            return {
                "error": "authentication_failed",
                "detail": "Jenkins returned a login page. Check JENKINS_USER and JENKINS_TOKEN.",
            }

    return None


# Response size limits
_MAX_RESULT_SIZE = 50_000  # 50KB for any tool result


def truncate_result(text, max_size=_MAX_RESULT_SIZE):
    """Truncate text with indication of omitted content.

    Args:
        text: Text to truncate
        max_size: Maximum size in bytes

    Returns:
        str: Truncated text with indicator if truncated
    """
    if len(text) <= max_size:
        return text
    omitted = len(text) - max_size
    return text[:max_size] + f"\n\n... (truncated, {omitted} bytes omitted)"


def extract_build_causes(actions):
    """Extract trigger causes from build actions array.

    Args:
        actions: Jenkins build actions array

    Returns:
        list[str]: Cause descriptions
    """
    causes = []
    for action in actions:
        if "causes" in action:
            for cause in action["causes"]:
                desc = cause.get("shortDescription", "")
                if desc:
                    causes.append(desc)
    return causes


# Parameter names that commonly contain secrets
_SENSITIVE_PARAM_PATTERN = re.compile(
    r"(?i)(password|passwd|secret|token|api[_-]?key|"
    r"private[_-]?key|access[_-]?key|credential|auth)",
)


def extract_build_parameters(actions):
    """Extract build parameters from actions array.

    Sensitive parameter values (based on name patterns) are redacted
    to prevent secret leakage.

    Args:
        actions: Jenkins build actions array

    Returns:
        dict: Parameter name -> value mapping (sensitive values redacted)
    """
    params = {}
    for action in actions:
        if "parameters" in action:
            for param in action["parameters"]:
                name = param.get("name")
                value = param.get("value")
                if name is not None:
                    # Redact values for parameters with secret-like names
                    if _SENSITIVE_PARAM_PATTERN.search(name):
                        params[name] = "[REDACTED]"
                    else:
                        params[name] = value
    return params


def extract_scm_changes(change_sets):
    """Extract commit messages and authors from changeSets.

    Args:
        change_sets: Jenkins changeSets array

    Returns:
        list[dict]: List of changes with commit, author, message
    """
    changes = []
    for change_set in change_sets:
        if "items" in change_set:
            for item in change_set["items"]:
                changes.append(
                    {
                        "commit": item.get("commitId", ""),
                        "author": item.get("author", {}).get("fullName", ""),
                        "message": item.get("msg", ""),
                    }
                )
    return changes


def extract_brief_build(build):
    """Extract compact build summary from Jenkins API response.

    Args:
        build: Jenkins build API response dict

    Returns:
        dict: Compact build summary
    """
    # Extract artifacts
    artifacts = []
    for artifact in build.get("artifacts", []):
        if "fileName" in artifact:
            artifacts.append(artifact["fileName"])

    return {
        "number": build.get("number"),
        "result": build.get("result"),
        "duration_ms": build.get("duration", 0),
        "timestamp": build.get("timestamp"),
        "building": build.get("building", False),
        "causes": extract_build_causes(build.get("actions", [])),
        "parameters": extract_build_parameters(build.get("actions", [])),
        "changes": extract_scm_changes(build.get("changeSets", [])),
        "artifacts": artifacts,
    }


def detect_job_type(job):
    """Detect job type from _class field.

    Args:
        job: Jenkins job dict with _class field

    Returns:
        str: Job type (freestyle, pipeline, folder, multibranch, unknown)
    """
    job_class = job.get("_class", "")

    if "FreeStyleProject" in job_class:
        return "freestyle"
    elif "WorkflowJob" in job_class:
        return "pipeline"
    elif "Folder" in job_class:
        return "folder"
    elif "MultiBranch" in job_class or "multibranch" in job_class.lower():
        return "multibranch"
    else:
        return "unknown"


def extract_brief_job(job, folder):
    """Extract compact job summary from Jenkins API response.

    Args:
        job: Jenkins job API response dict
        folder: Folder path (empty string for root)

    Returns:
        dict: Compact job summary
    """
    # Extract health score (first health report)
    health = None
    health_reports = job.get("healthReport", [])
    if health_reports and len(health_reports) > 0:
        health = health_reports[0].get("score")

    # Extract last build info
    last_build_num = None
    last_result = None
    last_build = job.get("lastBuild")
    if last_build:
        last_build_num = last_build.get("number")
        last_result = last_build.get("result")

    return {
        "name": job.get("name", ""),
        "type": detect_job_type(job),
        "folder": folder,
        "last_build": last_build_num,
        "last_result": last_result,
        "health": health,
    }


def parse_junit_xml(xml_content):
    """Parse JUnit XML test report.

    Args:
        xml_content: JUnit XML string

    Returns:
        dict with total, passed, failed, skipped counts and failure details
    """
    import xml.etree.ElementTree as ET

    try:
        root = ET.fromstring(xml_content)
    except ET.ParseError as e:
        return {"error": "parse_error", "detail": f"Invalid XML: {str(e)}"}

    total = 0
    failed = 0
    errors = 0
    skipped = 0
    failures = []

    # Handle both <testsuites> and <testsuite> as root
    testsuites = root.findall("testsuite")
    if not testsuites and root.tag == "testsuite":
        testsuites = [root]

    for testsuite in testsuites:
        suite_tests = int(testsuite.get("tests", 0))
        suite_failures = int(testsuite.get("failures", 0))
        suite_errors = int(testsuite.get("errors", 0))
        suite_skipped = int(testsuite.get("skipped", 0))

        total += suite_tests
        failed += suite_failures
        errors += suite_errors
        skipped += suite_skipped

        # Extract failure and error details
        for testcase in testsuite.findall("testcase"):
            for elem in (testcase.find("failure"), testcase.find("error")):
                if elem is not None:
                    msg = elem.get("message", "")
                    trace = elem.text or ""

                    if len(trace) > 500:
                        trace = trace[:497] + "..."

                    failures.append(
                        {
                            "name": testcase.get("name", ""),
                            "classname": testcase.get("classname", ""),
                            "message": msg,
                            "trace": trace,
                            "type": elem.tag,
                        }
                    )

    passed = total - failed - errors - skipped

    # Calculate total duration
    duration = 0.0
    for testsuite in testsuites:
        suite_time = testsuite.get("time", "0")
        try:
            duration += float(suite_time)
        except ValueError:
            pass

    return {
        "total": total,
        "passed": passed,
        "failed": failed,
        "errors": errors,
        "skipped": skipped,
        "duration_seconds": duration,
        "failures": failures,
    }
