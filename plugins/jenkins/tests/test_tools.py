"""Tests for tools_read.py - tool logic with mocked http/cache."""

from unittest.mock import MagicMock, patch

import handler
import tools_read

# Default Jenkins URL used across all tests
_TEST_URL = "https://jenkins.example.com"


def _mock_http(status, body, headers=None):
    """Mock handler.http to return fixed response."""
    return patch.object(handler, "http", return_value=(status, body, headers or {}))


def _mock_cache_get(value=None):
    """Mock handler.cache_get to return fixed value."""
    return patch.object(handler, "cache_get", return_value=value)


def _mock_cache_set():
    """Mock handler.cache_set to track calls."""
    return patch.object(handler, "cache_set", MagicMock())


class TestSetupUrl:
    """Test jenkins_url validation."""

    def test_valid_https_url(self):
        err = handler.set_jenkins_url("https://jenkins.example.com")
        assert err is None
        assert handler._jenkins_url == "https://jenkins.example.com"

    def test_valid_http_url(self):
        err = handler.set_jenkins_url("http://jenkins.example.com")
        assert err is None

    def test_strips_trailing_slash(self):
        err = handler.set_jenkins_url("https://jenkins.example.com/")
        assert err is None
        assert handler._jenkins_url == "https://jenkins.example.com"

    def test_invalid_scheme(self):
        err = handler.set_jenkins_url("ftp://jenkins.example.com")
        assert err is not None
        assert "invalid" in err["error"]

    def test_empty_url(self):
        err = handler.set_jenkins_url("")
        assert err is not None
        assert "invalid" in err["error"]

    def test_none_url(self):
        err = handler.set_jenkins_url(None)
        assert err is not None
        assert "invalid" in err["error"]

    def test_missing_jenkins_url_param(self):
        result = tools_read.jenkins_whoami({})
        assert "error" in result
        assert "invalid" in result["error"]


class TestJenkinsWhoami:
    """Test jenkins_whoami tool."""

    def test_cache_hit_returns_immediately(self):
        cached_data = {
            "id": "admin",
            "fullName": "Administrator",
            "email": "admin@example.com",
        }

        with _mock_cache_get(cached_data):
            result = tools_read.jenkins_whoami({"jenkins_url": _TEST_URL})

        assert result == cached_data

    def test_cache_miss_fetches_and_caches(self):
        api_response = {
            "id": "admin",
            "fullName": "Administrator",
            "absoluteUrl": "https://jenkins.example.com/user/admin",
            "description": "Admin user",
            "property": [
                {"_class": "jenkins.security.ApiTokenProperty"},
                {"_class": "hudson.tasks.Mailer$UserProperty", "address": "admin@example.com"},
                {"_class": "jenkins.security.seed.UserSeedProperty"},
            ],
        }

        with (
            _mock_http(200, api_response),
            _mock_cache_get(None),
            _mock_cache_set() as mock_set,
        ):
            result = tools_read.jenkins_whoami({"jenkins_url": _TEST_URL})

        assert result["id"] == "admin"
        assert result["fullName"] == "Administrator"
        assert result["email"] == "admin@example.com"
        # Verify cached with 1 hour TTL
        assert mock_set.called
        call_args = mock_set.call_args[0]
        assert call_args[2] == 3600  # ttl=3600

    def test_http_error_returns_error_dict(self):
        with _mock_http(404, {"error": "not found"}), _mock_cache_get(None):
            result = tools_read.jenkins_whoami({"jenkins_url": _TEST_URL})

        assert "error" in result
        assert "404" in str(result)

    def test_auth_failure_detected(self):
        with _mock_http(401, "Unauthorized"), _mock_cache_get(None):
            result = tools_read.jenkins_whoami({"jenkins_url": _TEST_URL})

        assert "error" in result


class TestJenkinsListJobs:
    """Test jenkins_list_jobs tool."""

    def test_list_root_jobs(self):
        api_response = {
            "jobs": [
                {
                    "name": "job1",
                    "_class": "hudson.model.FreeStyleProject",
                    "color": "blue",
                    "healthReport": [{"score": 100}],
                    "lastBuild": {"number": 10, "result": "SUCCESS"},
                },
                {
                    "name": "job2",
                    "_class": "org.jenkinsci.plugins.workflow.job.WorkflowJob",
                    "color": "red",
                    "healthReport": [{"score": 20}],
                    "lastBuild": {"number": 5, "result": "FAILURE"},
                },
            ]
        }

        with (
            _mock_http(200, api_response),
            _mock_cache_get(None),
            _mock_cache_set(),
        ):
            result = tools_read.jenkins_list_jobs({"jenkins_url": _TEST_URL})

        assert result["count"] == 2
        assert len(result["jobs"]) == 2
        assert result["jobs"][0]["name"] == "job1"
        assert result["jobs"][0]["type"] == "freestyle"
        assert result["jobs"][0]["health"] == 100
        assert result["jobs"][1]["name"] == "job2"
        assert result["jobs"][1]["type"] == "pipeline"

    def test_list_jobs_in_folder(self):
        api_response = {
            "jobs": [
                {
                    "name": "my-job",
                    "_class": "hudson.model.FreeStyleProject",
                    "healthReport": [],
                    "lastBuild": None,
                }
            ]
        }

        params = {"jenkins_url": _TEST_URL, "folder": "team/project"}

        with (
            _mock_http(200, api_response),
            _mock_cache_get(None),
            _mock_cache_set(),
        ):
            result = tools_read.jenkins_list_jobs(params)

        assert result["count"] == 1
        assert result["jobs"][0]["folder"] == "team/project"

    def test_filter_by_name(self):
        api_response = {
            "jobs": [
                {"name": "frontend-build", "_class": "hudson.model.FreeStyleProject"},
                {"name": "backend-build", "_class": "hudson.model.FreeStyleProject"},
                {"name": "frontend-test", "_class": "hudson.model.FreeStyleProject"},
            ]
        }

        params = {"jenkins_url": _TEST_URL, "name_filter": "frontend"}

        with (
            _mock_http(200, api_response),
            _mock_cache_get(None),
            _mock_cache_set(),
        ):
            result = tools_read.jenkins_list_jobs(params)

        assert result["count"] == 2
        assert all("frontend" in job["name"].lower() for job in result["jobs"])

    def test_max_results_limit(self):
        jobs = [{"name": f"job{i}", "_class": "hudson.model.FreeStyleProject"} for i in range(100)]
        api_response = {"jobs": jobs}

        params = {"jenkins_url": _TEST_URL, "max_results": 10}

        with (
            _mock_http(200, api_response),
            _mock_cache_get(None),
            _mock_cache_set(),
        ):
            result = tools_read.jenkins_list_jobs(params)

        assert result["count"] == 10
        assert len(result["jobs"]) == 10

    def test_invalid_folder_name_returns_error(self):
        params = {"jenkins_url": _TEST_URL, "folder": "../etc/passwd"}
        result = tools_read.jenkins_list_jobs(params)
        assert "error" in result
        assert "invalid_input" in result["error"]

    def test_cache_hit_returns_immediately(self):
        cached_data = {"count": 1, "jobs": [{"name": "job1"}]}
        params = {"jenkins_url": _TEST_URL, "folder": "team"}

        with _mock_cache_get(cached_data):
            result = tools_read.jenkins_list_jobs(params)

        assert result == cached_data

    def test_cache_hit_with_name_filter(self):
        cached_data = {
            "count": 3,
            "jobs": [
                {"name": "frontend-build", "type": "freestyle"},
                {"name": "backend-build", "type": "freestyle"},
                {"name": "frontend-test", "type": "pipeline"},
            ],
        }
        params = {"jenkins_url": _TEST_URL, "folder": "team", "name_filter": "frontend"}

        with _mock_cache_get(cached_data):
            result = tools_read.jenkins_list_jobs(params)

        assert result["count"] == 2
        assert all("frontend" in job["name"].lower() for job in result["jobs"])

    def test_cache_hit_with_max_results(self):
        jobs_list = [{"name": f"job{i}", "type": "freestyle"} for i in range(20)]
        cached_data = {"count": 20, "jobs": jobs_list}
        params = {"jenkins_url": _TEST_URL, "folder": "root", "max_results": 5}

        with _mock_cache_get(cached_data):
            result = tools_read.jenkins_list_jobs(params)

        assert result["count"] == 5
        assert len(result["jobs"]) == 5


class TestJenkinsGetBuild:
    """Test jenkins_get_build tool."""

    def test_get_completed_build(self):
        api_response = {
            "number": 42,
            "result": "SUCCESS",
            "duration": 125000,
            "timestamp": 1715068800000,
            "building": False,
            "actions": [
                {"causes": [{"shortDescription": "Started by user admin"}]},
                {"parameters": [{"name": "BRANCH", "value": "main"}]},
            ],
            "changeSets": [{"items": [{"commitId": "abc123", "author": {"fullName": "Jane Dev"}, "msg": "Fix bug"}]}],
            "artifacts": [{"fileName": "build.jar"}],
        }

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42"}

        with _mock_http(200, api_response), _mock_cache_get(None), _mock_cache_set() as mock_set:
            result = tools_read.jenkins_get_build(params)

        assert result["number"] == 42
        assert result["result"] == "SUCCESS"
        assert result["building"] is False
        assert result["causes"] == ["Started by user admin"]
        assert result["parameters"] == {"BRANCH": "main"}

        # Verify permanent cache (ttl=0) for completed build
        assert mock_set.called
        call_args = mock_set.call_args[0]
        assert call_args[2] == 0  # ttl=0 for permanent

    def test_get_running_build_not_cached(self):
        api_response = {
            "number": 43,
            "result": None,
            "duration": 0,
            "timestamp": 1715068900000,
            "building": True,
            "actions": [],
            "changeSets": [],
            "artifacts": [],
        }

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "43"}

        with _mock_http(200, api_response), _mock_cache_get(None), _mock_cache_set() as mock_set:
            result = tools_read.jenkins_get_build(params)

        assert result["building"] is True
        # Verify NOT cached
        mock_set.assert_not_called()

    def test_get_build_with_alias(self):
        api_response = {
            "number": 100,
            "result": "FAILURE",
            "building": False,
        }

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "lastFailedBuild"}

        with _mock_http(200, api_response), _mock_cache_get(None), _mock_cache_set() as mock_set:
            result = tools_read.jenkins_get_build(params)

        assert result["number"] == 100
        assert result["result"] == "FAILURE"

        # CRITICAL: Verify alias is NOT cached under alias key
        # Should be cached under resolved number (100) instead
        assert mock_set.called
        call_args = mock_set.call_args[0]
        cached_key = call_args[0]
        # Key should end with ":100" not ":lastFailedBuild"
        assert cached_key.endswith(":100")
        assert not cached_key.endswith(":lastFailedBuild")

    def test_invalid_job_name_returns_error(self):
        params = {"jenkins_url": _TEST_URL, "job_name": "../etc/passwd", "build_number": "42"}
        result = tools_read.jenkins_get_build(params)
        assert "error" in result
        assert "invalid_input" in result["error"]

    def test_invalid_build_number_returns_error(self):
        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "not-a-number"}
        result = tools_read.jenkins_get_build(params)
        assert "error" in result
        assert "invalid_input" in result["error"]

    def test_cache_hit_returns_immediately(self):
        cached_data = {"number": 42, "result": "SUCCESS"}
        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42"}

        with _mock_cache_get(cached_data):
            result = tools_read.jenkins_get_build(params)

        assert result == cached_data

    def test_http_error_returns_error_dict(self):
        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "999"}

        with _mock_http(404, {"message": "Build not found"}), _mock_cache_get(None):
            result = tools_read.jenkins_get_build(params)

        assert "error" in result
        assert "404" in str(result)


class TestJenkinsGetBuildLog:
    """Test jenkins_get_build_log tool."""

    def test_get_log_with_tail_mode(self):
        # Build is completed
        build_response = {"building": False}

        # Log with 100 lines
        log_lines = [f"Line {i}" for i in range(100)]
        log_text = "\n".join(log_lines)

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42", "tail_lines": 10}

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, build_response, {}),  # Build status check
                    (200, log_text, {}),  # Console log
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set(),
        ):
            result = tools_read.jenkins_get_build_log(params)

        assert result["line_count"] == 10
        assert "Line 99" in result["log"]
        assert "Line 0" not in result["log"]
        assert result["building"] is False

    def test_log_scrubbing_applied(self):
        build_response = {"building": False}
        log_with_secrets = """
        export AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE
        DB_PASSWORD=secretpass123
        Build completed successfully
        """

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42"}

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, build_response, {}),  # Build status check
                    (200, log_with_secrets, {}),  # Console log
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set(),
        ):
            result = tools_read.jenkins_get_build_log(params)

        # CRITICAL: Secrets must be redacted
        assert "[REDACTED]" in result["log"]
        assert "AKIAIOSFODNN7EXAMPLE" not in result["log"]
        assert "secretpass123" not in result["log"]
        # Normal content preserved
        assert "Build completed successfully" in result["log"]

    def test_grep_filter_applied(self):
        build_response = {"building": False}
        log_text = """
        INFO: Starting build
        ERROR: Connection failed
        INFO: Retrying
        ERROR: Still failing
        INFO: Done
        """

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42", "grep": "ERROR"}

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, build_response, {}),  # Build status check
                    (200, log_text, {}),  # Console log
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set(),
        ):
            result = tools_read.jenkins_get_build_log(params)

        assert "ERROR: Connection failed" in result["log"]
        assert "ERROR: Still failing" in result["log"]
        assert "INFO:" not in result["log"]

    def test_completed_build_log_cached_permanently(self):
        build_response = {"building": False}
        log_text = "Build completed"

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42"}

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, build_response, {}),  # Build status check
                    (200, log_text, {}),  # Console log
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set() as mock_set,
        ):
            tools_read.jenkins_get_build_log(params)

        # Verify permanent cache for completed build log
        assert mock_set.called
        # Find the cache_set call for the log (not the build status check)
        log_cache_calls = [c for c in mock_set.call_args_list if "log" in str(c[0][0])]
        assert len(log_cache_calls) == 1
        # ttl=0 for permanent (passed as keyword arg)
        assert log_cache_calls[0][1]["ttl"] == 0

    def test_running_build_log_not_cached(self):
        build_response = {"building": True}
        log_text = "Build in progress..."

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42"}

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, build_response, {}),  # Build status check
                    (200, log_text, {}),  # Console log
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set() as mock_set,
        ):
            result = tools_read.jenkins_get_build_log(params)

        assert result["building"] is True
        # Verify log NOT cached (only build status check might be cached)
        log_cache_calls = [c for c in mock_set.call_args_list if "log" in str(c[0][0])]
        assert len(log_cache_calls) == 0

    def test_invalid_job_name_returns_error(self):
        params = {"jenkins_url": _TEST_URL, "job_name": "../etc/passwd", "build_number": "42"}
        result = tools_read.jenkins_get_build_log(params)
        assert "error" in result
        assert "invalid_input" in result["error"]

    def test_tail_lines_capped_at_max(self):
        # Requesting 10000 lines should be capped at 5000
        build_response = {"building": False}
        log_lines = [f"Line {i}" for i in range(6000)]
        log_text = "\n".join(log_lines)

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42", "tail_lines": 10000}

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, build_response, {}),  # Build status check
                    (200, log_text, {}),  # Console log
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set(),
        ):
            result = tools_read.jenkins_get_build_log(params)

        # Should be capped at 5000 lines
        assert result["line_count"] <= 5000


class TestJenkinsGetJob:
    """Test jenkins_get_job tool."""

    def test_get_job_basic_metadata(self):
        api_response = {
            "name": "my-pipeline",
            "_class": "org.jenkinsci.plugins.workflow.job.WorkflowJob",
            "description": "Production deployment pipeline",
            "url": "https://jenkins.example.com/job/my-pipeline/",
            "color": "blue",
            "healthReport": [{"score": 80}],
            "property": [
                {
                    "_class": "hudson.model.ParametersDefinitionProperty",
                    "parameterDefinitions": [
                        {"name": "BRANCH", "type": "StringParameterDefinition", "defaultValue": "main"},
                        {"name": "ENVIRONMENT", "type": "ChoiceParameterDefinition", "choices": ["dev", "prod"]},
                    ],
                }
            ],
            "builds": [{"number": 100}, {"number": 99}, {"number": 98}],
        }

        params = {"jenkins_url": _TEST_URL, "job_name": "my-pipeline"}

        with _mock_http(200, api_response), _mock_cache_get(None), _mock_cache_set():
            result = tools_read.jenkins_get_job(params)

        assert result["name"] == "my-pipeline"
        assert result["type"] == "pipeline"
        assert result["description"] == "Production deployment pipeline"
        assert result["health"] == 80
        assert len(result["parameters"]) == 2
        assert result["parameters"][0]["name"] == "BRANCH"
        assert result["last_builds"] == [100, 99, 98]

    def test_get_job_no_parameters(self):
        api_response = {"name": "simple-job", "_class": "hudson.model.FreeStyleProject", "property": []}

        params = {"jenkins_url": _TEST_URL, "job_name": "simple-job"}

        with _mock_http(200, api_response), _mock_cache_get(None), _mock_cache_set():
            result = tools_read.jenkins_get_job(params)

        assert result["parameters"] == []

    def test_get_job_with_max_builds(self):
        builds = [{"number": i} for i in range(100, 0, -1)]
        api_response = {"name": "test-job", "_class": "hudson.model.FreeStyleProject", "builds": builds}

        params = {"jenkins_url": _TEST_URL, "job_name": "test-job", "max_builds": 5}

        with _mock_http(200, api_response), _mock_cache_get(None), _mock_cache_set():
            result = tools_read.jenkins_get_job(params)

        assert len(result["last_builds"]) == 5
        assert result["last_builds"] == [100, 99, 98, 97, 96]

    def test_invalid_job_name_returns_error(self):
        params = {"jenkins_url": _TEST_URL, "job_name": "../etc/passwd"}
        result = tools_read.jenkins_get_job(params)
        assert "error" in result
        assert "invalid_input" in result["error"]

    def test_cache_hit_returns_immediately(self):
        cached_data = {"name": "my-job", "type": "freestyle", "last_builds": [10, 9, 8]}
        params = {"jenkins_url": _TEST_URL, "job_name": "my-job"}

        with _mock_cache_get(cached_data):
            result = tools_read.jenkins_get_job(params)

        assert result["name"] == "my-job"
        assert result["type"] == "freestyle"
        assert result["last_builds"] == [10, 9, 8]

    def test_cache_hit_truncates_last_builds(self):
        cached_data = {
            "name": "my-job",
            "type": "freestyle",
            "last_builds": [10, 9, 8, 7, 6, 5, 4, 3, 2, 1],
        }
        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "max_builds": 3}

        with _mock_cache_get(cached_data):
            result = tools_read.jenkins_get_job(params)

        assert result["last_builds"] == [10, 9, 8]

    def test_max_builds_bool_rejected(self):
        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "max_builds": True}
        result = tools_read.jenkins_get_job(params)
        assert "error" in result
        assert "max_builds must be an integer" in result["detail"]

    def test_http_error_returns_error_dict(self):
        params = {"jenkins_url": _TEST_URL, "job_name": "non-existent"}

        with _mock_http(404, {"message": "Job not found"}), _mock_cache_get(None):
            result = tools_read.jenkins_get_job(params)

        assert "error" in result
        assert "404" in str(result)


class TestJenkinsListBuilds:
    """Test jenkins_list_builds tool."""

    def test_list_builds_basic(self):
        api_response = {
            "builds": [
                {"number": 100, "result": "SUCCESS", "timestamp": 1715068800000, "duration": 120000},
                {"number": 99, "result": "FAILURE", "timestamp": 1715068700000, "duration": 115000},
                {"number": 98, "result": "SUCCESS", "timestamp": 1715068600000, "duration": 118000},
            ]
        }

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job"}

        with _mock_http(200, api_response), _mock_cache_get(None), _mock_cache_set():
            result = tools_read.jenkins_list_builds(params)

        assert result["count"] == 3
        assert len(result["builds"]) == 3
        assert result["builds"][0]["number"] == 100
        assert result["builds"][0]["result"] == "SUCCESS"

    def test_list_builds_with_result_filter(self):
        api_response = {
            "builds": [
                {"number": 100, "result": "SUCCESS"},
                {"number": 99, "result": "FAILURE"},
                {"number": 98, "result": "SUCCESS"},
                {"number": 97, "result": "FAILURE"},
            ]
        }

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "result_filter": "FAILURE"}

        with _mock_http(200, api_response), _mock_cache_get(None), _mock_cache_set():
            result = tools_read.jenkins_list_builds(params)

        assert result["count"] == 2
        assert all(b["result"] == "FAILURE" for b in result["builds"])
        assert result["builds"][0]["number"] == 99
        assert result["builds"][1]["number"] == 97

    def test_list_builds_with_max_results(self):
        builds = [{"number": i, "result": "SUCCESS"} for i in range(100, 0, -1)]
        api_response = {"builds": builds}

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "max_results": 10}

        with _mock_http(200, api_response), _mock_cache_get(None), _mock_cache_set():
            result = tools_read.jenkins_list_builds(params)

        assert result["count"] == 10
        assert len(result["builds"]) == 10

    def test_list_builds_with_offset(self):
        builds = [{"number": i, "result": "SUCCESS"} for i in range(100, 90, -1)]
        api_response = {"builds": builds}

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "offset": 5, "max_results": 3}

        with _mock_http(200, api_response), _mock_cache_get(None), _mock_cache_set():
            result = tools_read.jenkins_list_builds(params)

        # Should skip first 5, return next 3
        assert result["count"] == 3
        assert result["builds"][0]["number"] == 95

    def test_invalid_job_name_returns_error(self):
        params = {"jenkins_url": _TEST_URL, "job_name": "../etc/passwd"}
        result = tools_read.jenkins_list_builds(params)
        assert "error" in result
        assert "invalid_input" in result["error"]

    def test_max_results_bool_rejected(self):
        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "max_results": True}
        result = tools_read.jenkins_list_builds(params)
        assert "error" in result
        assert "max_results must be an integer" in result["detail"]

    def test_offset_bool_rejected(self):
        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "offset": False}
        result = tools_read.jenkins_list_builds(params)
        assert "error" in result
        assert "offset must be an integer" in result["detail"]

    def test_invalid_result_filter_returns_error(self):
        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "result_filter": "INVALID"}
        result = tools_read.jenkins_list_builds(params)
        assert "error" in result
        assert "result_filter must be one of" in result["detail"]

    def test_http_error_returns_error_dict(self):
        params = {"jenkins_url": _TEST_URL, "job_name": "non-existent"}

        with _mock_http(404, {"message": "Job not found"}), _mock_cache_get(None):
            result = tools_read.jenkins_list_builds(params)

        assert "error" in result
        assert "404" in str(result)

    def test_cache_hit_returns_immediately(self):
        cached_data = {"count": 5, "builds": []}
        params = {"jenkins_url": _TEST_URL, "job_name": "my-job"}

        with _mock_cache_get(cached_data):
            result = tools_read.jenkins_list_builds(params)

        assert result == cached_data


class TestJenkinsListViews:
    """Test jenkins_list_views tool."""

    def test_list_views_basic(self):
        api_response = {
            "views": [
                {"name": "All", "url": "https://jenkins.example.com/", "_class": "hudson.model.AllView"},
                {
                    "name": "Production",
                    "url": "https://jenkins.example.com/view/Production/",
                    "_class": "hudson.model.ListView",
                },
                {
                    "name": "Development",
                    "url": "https://jenkins.example.com/view/Development/",
                    "_class": "hudson.model.ListView",
                },
            ]
        }

        params = {"jenkins_url": _TEST_URL}

        with _mock_http(200, api_response), _mock_cache_get(None), _mock_cache_set():
            result = tools_read.jenkins_list_views(params)

        assert result["count"] == 3
        assert len(result["views"]) == 3
        assert result["views"][0]["name"] == "All"
        assert result["views"][1]["name"] == "Production"

    def test_list_views_with_name_filter(self):
        api_response = {
            "views": [
                {"name": "All", "_class": "hudson.model.AllView"},
                {"name": "Production", "_class": "hudson.model.ListView"},
                {"name": "Production-Backup", "_class": "hudson.model.ListView"},
                {"name": "Development", "_class": "hudson.model.ListView"},
            ]
        }

        params = {"jenkins_url": _TEST_URL, "name_filter": "production"}

        with _mock_http(200, api_response), _mock_cache_get(None), _mock_cache_set():
            result = tools_read.jenkins_list_views(params)

        assert result["count"] == 2
        assert all("production" in v["name"].lower() for v in result["views"])

    def test_cache_hit_returns_immediately(self):
        cached_data = {"count": 2, "views": []}
        params = {"jenkins_url": _TEST_URL}

        with _mock_cache_get(cached_data):
            result = tools_read.jenkins_list_views(params)

        assert result == cached_data

    def test_cache_hit_with_name_filter(self):
        cached_data = {
            "count": 3,
            "views": [
                {"name": "All", "_class": "hudson.model.AllView"},
                {"name": "Production", "_class": "hudson.model.ListView"},
                {"name": "Development", "_class": "hudson.model.ListView"},
            ],
        }
        params = {"jenkins_url": _TEST_URL, "name_filter": "prod"}

        with _mock_cache_get(cached_data):
            result = tools_read.jenkins_list_views(params)

        assert result["count"] == 1
        assert result["views"][0]["name"] == "Production"


class TestJenkinsGetView:
    """Test jenkins_get_view tool."""

    def test_get_view_basic(self):
        api_response = {
            "name": "Production",
            "description": "Production jobs",
            "jobs": [
                {"name": "deploy-prod", "_class": "org.jenkinsci.plugins.workflow.job.WorkflowJob", "color": "blue"},
                {"name": "rollback-prod", "_class": "hudson.model.FreeStyleProject", "color": "red"},
            ],
        }

        params = {"jenkins_url": _TEST_URL, "view_name": "Production"}

        with _mock_http(200, api_response), _mock_cache_get(None), _mock_cache_set():
            result = tools_read.jenkins_get_view(params)

        assert result["name"] == "Production"
        assert result["description"] == "Production jobs"
        assert result["count"] == 2
        assert result["jobs"][0]["name"] == "deploy-prod"

    def test_get_view_with_name_filter(self):
        api_response = {
            "name": "All",
            "jobs": [
                {"name": "deploy-prod", "_class": "hudson.model.FreeStyleProject"},
                {"name": "deploy-dev", "_class": "hudson.model.FreeStyleProject"},
                {"name": "test-prod", "_class": "hudson.model.FreeStyleProject"},
            ],
        }

        params = {"jenkins_url": _TEST_URL, "view_name": "All", "name_filter": "deploy"}

        with _mock_http(200, api_response), _mock_cache_get(None), _mock_cache_set():
            result = tools_read.jenkins_get_view(params)

        assert result["count"] == 2
        assert all("deploy" in j["name"] for j in result["jobs"])

    def test_invalid_view_name_returns_error(self):
        params = {"jenkins_url": _TEST_URL, "view_name": "../etc/passwd"}
        result = tools_read.jenkins_get_view(params)
        assert "error" in result
        assert "invalid_input" in result["error"]

    def test_cache_hit_returns_immediately(self):
        cached_data = {"name": "Production", "description": None, "count": 0, "jobs": []}
        params = {"jenkins_url": _TEST_URL, "view_name": "Production"}

        with _mock_cache_get(cached_data):
            result = tools_read.jenkins_get_view(params)

        assert result == cached_data


class TestJenkinsGetQueue:
    """Test jenkins_get_queue tool."""

    def test_get_queue_with_items(self):
        api_response = {
            "items": [
                {
                    "id": 123,
                    "task": {"name": "deploy-prod"},
                    "why": "Waiting for next available executor",
                    "inQueueSince": 1715068800000,
                    "stuck": False,
                },
                {
                    "id": 124,
                    "task": {"name": "test-suite"},
                    "why": "Build #42 is already in progress",
                    "inQueueSince": 1715068900000,
                    "stuck": False,
                },
            ]
        }

        params = {"jenkins_url": _TEST_URL}

        with _mock_http(200, api_response), _mock_cache_get(None), _mock_cache_set():
            result = tools_read.jenkins_get_queue(params)

        assert result["count"] == 2
        assert len(result["queue"]) == 2
        assert result["queue"][0]["id"] == 123
        assert result["queue"][0]["job_name"] == "deploy-prod"
        assert result["queue"][0]["reason"] == "Waiting for next available executor"

    def test_get_queue_empty(self):
        api_response = {"items": []}

        params = {"jenkins_url": _TEST_URL}

        with _mock_http(200, api_response), _mock_cache_get(None), _mock_cache_set():
            result = tools_read.jenkins_get_queue(params)

        assert result["count"] == 0
        assert result["queue"] == []

    def test_get_queue_stuck_only_filter(self):
        api_response = {
            "items": [
                {"id": 123, "task": {"name": "job1"}, "why": "reason", "stuck": False},
                {"id": 124, "task": {"name": "job2"}, "why": "reason", "stuck": True},
                {"id": 125, "task": {"name": "job3"}, "why": "reason", "stuck": False},
            ]
        }

        params = {"jenkins_url": _TEST_URL, "stuck_only": True}

        with _mock_http(200, api_response), _mock_cache_get(None), _mock_cache_set():
            result = tools_read.jenkins_get_queue(params)

        assert result["count"] == 1
        assert result["queue"][0]["id"] == 124
        assert result["queue"][0]["stuck"] is True

    def test_cache_not_used(self):
        # Queue status should always be fresh, never cached
        api_response = {"items": []}

        params = {"jenkins_url": _TEST_URL}

        with _mock_http(200, api_response), _mock_cache_set() as mock_set:
            tools_read.jenkins_get_queue(params)

        # Verify NOT cached
        mock_set.assert_not_called()


class TestJenkinsFlushCache:
    """Test jenkins_flush_cache tool."""

    def test_flush_cache_all(self):
        params = {}

        with patch.object(handler, "cache_flush", MagicMock()) as mock_flush:
            result = tools_read.jenkins_flush_cache(params)

        assert result["status"] == "flushed"
        assert result["scope"] == "all"
        # Verify cache_flush called with namespace
        mock_flush.assert_called_once_with("jenkins")

    def test_flush_cache_with_confirmation(self):
        params = {"confirm": True}

        with patch.object(handler, "cache_flush", MagicMock()) as mock_flush:
            result = tools_read.jenkins_flush_cache(params)

        assert result["status"] == "flushed"
        mock_flush.assert_called_once()

    def test_flush_cache_no_confirmation_returns_warning(self):
        params = {"confirm": False}

        result = tools_read.jenkins_flush_cache(params)

        assert result["status"] == "cancelled"
        assert "confirmation required" in result["message"].lower()


class TestJenkinsGetTestResults:
    """Test jenkins_get_test_results tool."""

    def test_get_test_results_with_failures(self):
        # Build info response
        build_response = {
            "building": False,
            "actions": [
                {
                    "_class": "hudson.tasks.junit.TestResultAction",
                    "totalCount": 10,
                    "failCount": 2,
                    "skipCount": 1,
                }
            ],
        }

        # JUnit XML response
        junit_xml = """<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="Tests" tests="10" failures="2" skipped="1">
    <testcase name="test_success" classname="MyTests"/>
    <testcase name="test_failure" classname="MyTests">
      <failure message="AssertionError">Expected 5, got 3</failure>
    </testcase>
    <testcase name="test_skipped" classname="MyTests">
      <skipped/>
    </testcase>
  </testsuite>
</testsuites>"""

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42"}

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, build_response, {}),  # Build info
                    (200, junit_xml, {}),  # JUnit XML
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set(),
        ):
            result = tools_read.jenkins_get_test_results(params)

        assert result["job_name"] == "my-job"
        assert result["build_number"] == "42"
        assert result["total"] == 10
        assert result["passed"] == 7
        assert result["failed"] == 2
        assert result["skipped"] == 1
        assert len(result["failures"]) == 1
        assert result["total_failures"] == 1
        assert result["failures"][0]["name"] == "test_failure"

    def test_get_test_results_all_passed(self):
        build_response = {
            "building": False,
            "actions": [
                {
                    "_class": "hudson.tasks.junit.TestResultAction",
                    "totalCount": 5,
                    "failCount": 0,
                    "skipCount": 0,
                }
            ],
        }

        junit_xml = """<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="Tests" tests="5" failures="0" skipped="0"/>
</testsuites>"""

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42"}

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, build_response, {}),
                    (200, junit_xml, {}),
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set(),
        ):
            result = tools_read.jenkins_get_test_results(params)

        assert result["total"] == 5
        assert result["passed"] == 5
        assert result["failed"] == 0
        assert result["failures"] == []
        assert result["total_failures"] == 0

    def test_no_test_results_returns_error(self):
        build_response = {
            "building": False,
            "actions": [],  # No TestResultAction
        }

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42"}

        with (
            patch.object(
                handler,
                "http",
                return_value=(200, build_response, {}),
            ),
            _mock_cache_get(None),
        ):
            result = tools_read.jenkins_get_test_results(params)

        assert "error" in result
        assert "no_test_results" in result["error"]

    def test_completed_build_cached_permanently(self):
        build_response = {
            "building": False,
            "actions": [{"_class": "hudson.tasks.junit.TestResultAction", "totalCount": 1}],
        }
        junit_xml = """<testsuites><testsuite tests="1"/></testsuites>"""

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42"}

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, build_response, {}),
                    (200, junit_xml, {}),
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set() as mock_set,
        ):
            tools_read.jenkins_get_test_results(params)

        # Verify permanent cache (ttl=0)
        assert mock_set.call_count == 1
        assert mock_set.call_args[1]["ttl"] == 0

    def test_running_build_not_cached(self):
        build_response = {
            "building": True,
            "actions": [{"_class": "hudson.tasks.junit.TestResultAction", "totalCount": 1}],
        }
        junit_xml = """<testsuites><testsuite tests="1"/></testsuites>"""

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42"}

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, build_response, {}),
                    (200, junit_xml, {}),
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set() as mock_set,
        ):
            tools_read.jenkins_get_test_results(params)

        # Verify NOT cached
        mock_set.assert_not_called()

    def test_invalid_job_name_returns_error(self):
        params = {"jenkins_url": _TEST_URL, "job_name": "../etc/passwd", "build_number": "42"}
        result = tools_read.jenkins_get_test_results(params)
        assert "error" in result
        assert "invalid_input" in result["error"]

    def test_cache_hit_returns_immediately(self):
        cached_data = {"total": 5, "passed": 5, "failed": 0}
        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42"}

        with _mock_cache_get(cached_data):
            result = tools_read.jenkins_get_test_results(params)

        assert result == cached_data

    def test_junit_xml_fetch_error(self):
        build_response = {
            "building": False,
            "actions": [
                {
                    "_class": "hudson.tasks.junit.TestResultAction",
                    "totalCount": 10,
                }
            ],
        }

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42"}

        # Build API succeeds, but JUnit XML fetch fails
        with (
            patch.object(
                handler,
                "http",
                side_effect=[(200, build_response, {}), (500, "Internal Server Error", {})],
            ),
            _mock_cache_get(None),
        ):
            result = tools_read.jenkins_get_test_results(params)

        assert "error" in result
        assert "HTTP 500" in result["error"]


class TestJenkinsGetPipelineStages:
    """Test jenkins_get_pipeline_stages tool."""

    def test_get_pipeline_stages_success(self):
        wfapi_response = {
            "stages": [
                {
                    "id": "3",
                    "name": "Build",
                    "status": "SUCCESS",
                    "startTimeMillis": 1715068800000,
                    "durationMillis": 45000,
                },
                {
                    "id": "5",
                    "name": "Test",
                    "status": "SUCCESS",
                    "startTimeMillis": 1715068845000,
                    "durationMillis": 120000,
                },
                {
                    "id": "7",
                    "name": "Deploy",
                    "status": "FAILED",
                    "startTimeMillis": 1715068965000,
                    "durationMillis": 15000,
                },
            ]
        }
        build_response = {"building": False}

        params = {"jenkins_url": _TEST_URL, "job_name": "my-pipeline", "build_number": "42"}

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, wfapi_response, {}),  # wfapi/describe
                    (200, build_response, {}),  # build status check
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set(),
        ):
            result = tools_read.jenkins_get_pipeline_stages(params)

        assert result["job_name"] == "my-pipeline"
        assert result["build_number"] == "42"
        assert result["count"] == 3
        assert len(result["stages"]) == 3

        assert result["stages"][0]["name"] == "Build"
        assert result["stages"][0]["status"] == "SUCCESS"
        assert result["stages"][0]["id"] == "3"
        assert result["stages"][0]["duration_ms"] == 45000
        assert result["stages"][0]["start_time_ms"] == 1715068800000

    def test_pipeline_plugin_not_installed(self):
        # 404 from wfapi endpoint indicates Pipeline plugin missing
        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42"}

        with _mock_http(404, {"message": "Not found"}), _mock_cache_get(None):
            result = tools_read.jenkins_get_pipeline_stages(params)

        assert "error" in result
        assert "pipeline_not_supported" in result["error"]
        assert "Pipeline plugin" in result["detail"]

    def test_non_pipeline_job(self):
        # wfapi returns empty stages for freestyle jobs
        wfapi_response = {"stages": []}
        build_response = {"building": False}

        params = {"jenkins_url": _TEST_URL, "job_name": "freestyle-job", "build_number": "42"}

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, wfapi_response, {}),  # wfapi/describe
                    (200, build_response, {}),  # build status check
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set(),
        ):
            result = tools_read.jenkins_get_pipeline_stages(params)

        assert result["count"] == 0
        assert result["stages"] == []

    def test_completed_build_cached_permanently(self):
        wfapi_response = {"stages": [{"id": "1", "name": "Build", "status": "SUCCESS"}]}
        build_response = {"building": False}

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42"}

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, wfapi_response, {}),  # wfapi/describe
                    (200, build_response, {}),  # build status check
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set() as mock_set,
        ):
            tools_read.jenkins_get_pipeline_stages(params)

        # Verify permanent cache (ttl=0)
        assert mock_set.call_count == 1
        assert mock_set.call_args[1]["ttl"] == 0

    def test_running_build_not_cached(self):
        wfapi_response = {"stages": [{"id": "1", "name": "Build", "status": "IN_PROGRESS"}]}
        build_response = {"building": True}

        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42"}

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, wfapi_response, {}),  # wfapi/describe
                    (200, build_response, {}),  # build status check
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set() as mock_set,
        ):
            tools_read.jenkins_get_pipeline_stages(params)

        # Verify NOT cached
        mock_set.assert_not_called()

    def test_invalid_job_name_returns_error(self):
        params = {"jenkins_url": _TEST_URL, "job_name": "../etc/passwd", "build_number": "42"}
        result = tools_read.jenkins_get_pipeline_stages(params)
        assert "error" in result
        assert "invalid_input" in result["error"]

    def test_cache_hit_returns_immediately(self):
        cached_data = {"count": 2, "stages": []}
        params = {"jenkins_url": _TEST_URL, "job_name": "my-job", "build_number": "42"}

        with _mock_cache_get(cached_data):
            result = tools_read.jenkins_get_pipeline_stages(params)

        assert result == cached_data


class TestJenkinsGetStageLog:
    """Test jenkins_get_stage_log tool."""

    def test_get_stage_log_basic(self):
        log = """[Pipeline] stage
[Pipeline] { (Build)
[Pipeline] sh
+ npm install
added 50 packages
[Pipeline] }
[Pipeline] // Build"""

        build_response = {"building": False}

        params = {
            "jenkins_url": _TEST_URL,
            "job_name": "my-pipeline",
            "build_number": "42",
            "stage_id": "3",
        }

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, log, {}),  # Stage log
                    (200, build_response, {}),  # Build status check
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set(),
        ):
            result = tools_read.jenkins_get_stage_log(params)

        assert result["job_name"] == "my-pipeline"
        assert result["build_number"] == "42"
        assert result["stage_id"] == "3"
        assert "npm install" in result["log"]
        assert "added 50 packages" in result["log"]

    def test_log_scrubbing_applied(self):
        log = """export AWS_SECRET_ACCESS_KEY=abc123456
Build starting"""

        build_response = {"building": False}

        params = {
            "jenkins_url": _TEST_URL,
            "job_name": "my-job",
            "build_number": "42",
            "stage_id": "5",
        }

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, log, {}),  # Stage log
                    (200, build_response, {}),  # Build status check
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set(),
        ):
            result = tools_read.jenkins_get_stage_log(params)

        assert "[REDACTED]" in result["log"]
        assert "abc123456" not in result["log"]
        assert "Build starting" in result["log"]

    def test_tail_mode_limits_lines(self):
        log = "\n".join(f"Line {i}" for i in range(1000))
        build_response = {"building": False}

        params = {
            "jenkins_url": _TEST_URL,
            "job_name": "my-job",
            "build_number": "42",
            "stage_id": "3",
            "tail_lines": 10,
        }

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, log, {}),  # Stage log
                    (200, build_response, {}),  # Build status check
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set(),
        ):
            result = tools_read.jenkins_get_stage_log(params)

        assert result["line_count"] == 10
        assert "Line 999" in result["log"]
        assert "Line 0" not in result["log"]

    def test_completed_build_cached_permanently(self):
        log = "Stage completed"
        build_response = {"building": False}

        params = {
            "jenkins_url": _TEST_URL,
            "job_name": "my-job",
            "build_number": "42",
            "stage_id": "3",
        }

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, log, {}),  # Stage log
                    (200, build_response, {}),  # Build status check
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set() as mock_set,
        ):
            tools_read.jenkins_get_stage_log(params)

        # Verify permanent cache (ttl=0)
        assert mock_set.call_count == 1
        assert mock_set.call_args[1]["ttl"] == 0

    def test_running_build_not_cached(self):
        log = "Stage in progress..."
        build_response = {"building": True}

        params = {
            "jenkins_url": _TEST_URL,
            "job_name": "my-job",
            "build_number": "42",
            "stage_id": "3",
        }

        with (
            patch.object(
                handler,
                "http",
                side_effect=[
                    (200, log, {}),  # Stage log
                    (200, build_response, {}),  # Build status check
                ],
            ),
            _mock_cache_get(None),
            _mock_cache_set() as mock_set,
        ):
            tools_read.jenkins_get_stage_log(params)

        # Verify NOT cached
        mock_set.assert_not_called()

    def test_invalid_stage_id_returns_error(self):
        params = {
            "jenkins_url": _TEST_URL,
            "job_name": "my-job",
            "build_number": "42",
            "stage_id": "../etc",
        }

        result = tools_read.jenkins_get_stage_log(params)

        assert "error" in result
        assert "invalid_input" in result["error"]

    def test_cache_hit_returns_immediately(self):
        cached_data = {"log": "cached log", "line_count": 5}
        params = {
            "jenkins_url": _TEST_URL,
            "job_name": "my-job",
            "build_number": "42",
            "stage_id": "3",
        }

        with _mock_cache_get(cached_data):
            result = tools_read.jenkins_get_stage_log(params)

        assert result == cached_data

    def test_tail_lines_zero_returns_full_log(self):
        log = "\n".join(f"Line {i}" for i in range(100))
        build_response = {"building": False}

        params = {
            "jenkins_url": _TEST_URL,
            "job_name": "my-job",
            "build_number": "42",
            "stage_id": "3",
            "tail_lines": 0,
        }

        with (
            patch.object(handler, "http", side_effect=[(200, log, {}), (200, build_response, {})]),
            _mock_cache_get(None),
            _mock_cache_set(),
        ):
            result = tools_read.jenkins_get_stage_log(params)

        # Full log returned (no truncation)
        assert "Line 0" in result["log"]
        assert "Line 99" in result["log"]
        assert result["line_count"] == 100
