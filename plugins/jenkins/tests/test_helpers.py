"""Tests for helpers.py - pure function tests (no mocks needed)."""

import helpers
import pytest


class TestValidateJobName:
    """Test job name validation and sanitization."""

    def test_valid_simple_name(self):
        assert helpers.validate_job_name("my-job") == "my-job"

    def test_valid_name_with_underscores(self):
        assert helpers.validate_job_name("my_job_123") == "my_job_123"

    def test_valid_name_with_spaces(self):
        assert helpers.validate_job_name("My Job Name") == "My Job Name"

    def test_valid_folder_path(self):
        result = helpers.validate_job_name("team/project/my-job")
        assert result == "team/project/my-job"

    def test_valid_nested_folders(self):
        result = helpers.validate_job_name("org/team/subteam/project/job")
        assert result == "org/team/subteam/project/job"

    def test_reject_path_traversal_parent(self):
        with pytest.raises(ValueError, match="path traversal"):
            helpers.validate_job_name("../etc/passwd")

    def test_reject_double_dot_in_middle(self):
        with pytest.raises(ValueError, match="path traversal"):
            helpers.validate_job_name("folder/../job")

    def test_reject_single_dot_segment(self):
        with pytest.raises(ValueError, match="path traversal"):
            helpers.validate_job_name("folder/./job")

    def test_reject_empty_segment(self):
        with pytest.raises(ValueError, match="empty segment"):
            helpers.validate_job_name("folder//job")

    def test_reject_leading_slash(self):
        with pytest.raises(ValueError, match="start or end"):
            helpers.validate_job_name("/absolute/path")

    def test_reject_trailing_slash(self):
        with pytest.raises(ValueError, match="start or end"):
            helpers.validate_job_name("path/to/job/")

    def test_reject_empty_string(self):
        with pytest.raises(ValueError, match="required"):
            helpers.validate_job_name("")

    def test_reject_whitespace_only(self):
        with pytest.raises(ValueError, match="required"):
            helpers.validate_job_name("   ")

    def test_reject_too_long(self):
        long_name = "a" * 513
        with pytest.raises(ValueError, match="too long"):
            helpers.validate_job_name(long_name)

    def test_reject_invalid_characters(self):
        with pytest.raises(ValueError, match="invalid characters"):
            helpers.validate_job_name("job@name")

    def test_reject_segment_starting_with_hyphen(self):
        with pytest.raises(ValueError, match="invalid characters"):
            helpers.validate_job_name("-job")

    def test_whitespace_stripped(self):
        assert helpers.validate_job_name("  my-job  ") == "my-job"

    def test_reject_segment_whitespace(self):
        with pytest.raises(ValueError, match="leading/trailing whitespace"):
            helpers.validate_job_name(" team / job ")


class TestValidateBuildNumber:
    """Test build number validation."""

    def test_valid_integer(self):
        assert helpers.validate_build_number(42) == "42"

    def test_valid_string_integer(self):
        assert helpers.validate_build_number("123") == "123"

    def test_valid_alias_lastBuild(self):
        assert helpers.validate_build_number("lastBuild") == "lastBuild"

    def test_valid_alias_lastSuccessfulBuild(self):
        result = helpers.validate_build_number("lastSuccessfulBuild")
        assert result == "lastSuccessfulBuild"

    def test_valid_alias_lastFailedBuild(self):
        result = helpers.validate_build_number("lastFailedBuild")
        assert result == "lastFailedBuild"

    def test_valid_alias_lastStableBuild(self):
        result = helpers.validate_build_number("lastStableBuild")
        assert result == "lastStableBuild"

    def test_valid_alias_lastUnstableBuild(self):
        result = helpers.validate_build_number("lastUnstableBuild")
        assert result == "lastUnstableBuild"

    def test_valid_alias_lastUnsuccessfulBuild(self):
        result = helpers.validate_build_number("lastUnsuccessfulBuild")
        assert result == "lastUnsuccessfulBuild"

    def test_valid_alias_lastCompletedBuild(self):
        result = helpers.validate_build_number("lastCompletedBuild")
        assert result == "lastCompletedBuild"

    def test_whitespace_stripped(self):
        assert helpers.validate_build_number("  42  ") == "42"

    def test_alias_whitespace_stripped(self):
        result = helpers.validate_build_number("  lastBuild  ")
        assert result == "lastBuild"

    def test_reject_zero(self):
        with pytest.raises(ValueError, match="must be positive"):
            helpers.validate_build_number(0)

    def test_reject_negative(self):
        with pytest.raises(ValueError, match="must be positive"):
            helpers.validate_build_number(-1)

    def test_reject_float(self):
        with pytest.raises(ValueError, match="invalid build number"):
            helpers.validate_build_number(42.5)

    def test_reject_invalid_string(self):
        with pytest.raises(ValueError, match="invalid build number"):
            helpers.validate_build_number("notanumber")

    def test_reject_none(self):
        with pytest.raises(ValueError, match="invalid build number"):
            helpers.validate_build_number(None)

    def test_reject_empty_string(self):
        with pytest.raises(ValueError, match="invalid build number"):
            helpers.validate_build_number("")

    def test_reject_zero_string(self):
        with pytest.raises(ValueError, match="must be positive"):
            helpers.validate_build_number("0")

    def test_reject_negative_string(self):
        with pytest.raises(ValueError, match="must be positive"):
            helpers.validate_build_number("-1")

    def test_reject_true(self):
        with pytest.raises(ValueError, match="invalid build number"):
            helpers.validate_build_number(True)

    def test_reject_false(self):
        with pytest.raises(ValueError, match="invalid build number"):
            helpers.validate_build_number(False)


class TestJobPath:
    """Test Jenkins job path URL construction."""

    def test_simple_job(self):
        result = helpers.job_path("my-job")
        assert result == "job/my-job"

    def test_job_with_folder(self):
        result = helpers.job_path("team/my-job")
        assert result == "job/team/job/my-job"

    def test_nested_folders(self):
        result = helpers.job_path("org/team/project/job")
        assert result == "job/org/job/team/job/project/job/job"

    def test_url_encoding_spaces(self):
        result = helpers.job_path("My Job")
        assert result == "job/My%20Job"

    def test_url_encoding_special_chars(self):
        result = helpers.job_path("job.name-v2")
        assert result == "job/job.name-v2"

    def test_url_encoding_folder_with_spaces(self):
        result = helpers.job_path("My Team/My Job")
        assert result == "job/My%20Team/job/My%20Job"

    def test_reject_path_traversal(self):
        with pytest.raises(ValueError, match="path traversal"):
            helpers.job_path("../etc/passwd")


class TestScrubLogContent:
    """Test log scrubbing for secret redaction."""

    def test_scrub_aws_access_key(self):
        log = "export AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE"
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        assert "AKIAIOSFODNN7EXAMPLE" not in result

    def test_scrub_aws_secret_key(self):
        log = "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        assert "wJalrXUtnFEMI" not in result

    def test_scrub_password_equals(self):
        log = "DB_PASSWORD=secretpass123"
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        assert "secretpass123" not in result

    def test_scrub_token_colon(self):
        log = "api_token: abc123def456"
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        assert "abc123def456" not in result

    def test_scrub_jwt(self):
        log = "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abc123"
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        assert "eyJhbGci" not in result

    def test_scrub_connection_string_jdbc(self):
        log = "jdbc:postgresql://db.example.com:5432/mydb?user=admin&password=secret"
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        assert "admin" not in result

    def test_scrub_connection_string_mongodb(self):
        log = "mongodb://user:pass@localhost:27017/mydb"
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        assert "pass@localhost" not in result

    def test_scrub_private_key_block(self):
        log = """-----BEGIN PRIVATE KEY-----
MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj
MzEfYyjiWA4R4/M2bS1+fWIcPm15j7A1+3OwV5K9oGc1m7q9VJTUt9Us8cKjMzEf
-----END PRIVATE KEY-----"""
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        # Both header and key body should be redacted
        assert "MIIEvQIBADANBg" not in result
        assert "BEGIN PRIVATE KEY" not in result

    def test_scrub_rsa_private_key(self):
        log = """-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEAu1SU1LfVLPHCozMxH2Mo4lgOEePzNm0tfn1iHD5teY+wNftz
-----END RSA PRIVATE KEY-----"""
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        assert "MIIEpAIBAAKCAQEA" not in result

    def test_scrub_github_pat(self):
        log = "GITHUB_TOKEN=ghp_1234567890abcdefghijklmnopqrstuvwxyz"
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        assert "ghp_" not in result

    def test_scrub_gitlab_pat(self):
        log = "export GITLAB_TOKEN=glpat-1234567890abcdefghij"
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        assert "glpat-" not in result

    def test_scrub_aws_session_token(self):
        log = "AWS_SESSION_TOKEN=FwoGZXIvYXdzEBQaDD..."
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        assert "FwoGZXIv" not in result

    def test_scrub_three_segment_jwt(self):
        log = (
            "token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"
            ".eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ"
            ".SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
        )
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        # Entire JWT should be redacted, including signature segment
        assert "SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c" not in result

    def test_scrub_export_statement(self):
        log = "export SECRET_KEY=mysecret123"
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        assert "mysecret123" not in result

    def test_preserve_normal_log_content(self):
        log = "Build succeeded in 2m 15s"
        result = helpers.scrub_log_content(log)
        assert result == log  # No redaction

    def test_preserve_non_secret_variable(self):
        log = "BUILD_NUMBER=42"
        result = helpers.scrub_log_content(log)
        assert result == log  # No redaction

    def test_multiple_secrets_in_log(self):
        log = """
        export AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE
        DB_PASSWORD=secretpass123
        api_token: abc123def456
        Build complete
        """
        result = helpers.scrub_log_content(log)
        assert result.count("[REDACTED]") >= 3
        assert "AKIAIOSFODNN7EXAMPLE" not in result
        assert "secretpass123" not in result
        assert "abc123def456" not in result
        assert "Build complete" in result

    def test_scrub_bearer_token_header(self):
        log = 'curl -H "Authorization: Bearer sk-1234567890abcdef"'
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        assert "sk-1234567890abcdef" not in result

    def test_scrub_github_fine_grained_pat(self):
        log = "GITHUB_TOKEN=github_pat_11ABCDE0123456789abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrst"
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        assert "github_pat_" not in result

    def test_scrub_ec_private_key(self):
        log = """-----BEGIN EC PRIVATE KEY-----
MHcCAQEEIIGlRQiTrVI/l8BFZhHJUAmFqG1x1J5fhT4wFKZcgEY1oAoGCCqGSM49
-----END EC PRIVATE KEY-----"""
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        assert "MHcCAQEEIIGlRQ" not in result

    def test_scrub_openssh_private_key(self):
        log = """-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
-----END OPENSSH PRIVATE KEY-----"""
        result = helpers.scrub_log_content(log)
        assert "[REDACTED]" in result
        assert "b3BlbnNzaC1rZXktdjE" not in result

    def test_scrub_performance_on_large_input(self):
        import time

        # 50KB of safe content (no secrets)
        log = "BUILD_NUMBER=42\n" * 2500  # 50KB
        start = time.time()
        result = helpers.scrub_log_content(log)
        elapsed = time.time() - start
        # Should complete in under 1 second
        assert elapsed < 1.0
        # Should not redact safe variable
        assert "BUILD_NUMBER" in result

    def test_scrub_none_input(self):
        result = helpers.scrub_log_content(None)
        assert result == ""

    def test_scrub_empty_string(self):
        result = helpers.scrub_log_content("")
        assert result == ""


class TestTruncateLog:
    """Test log truncation for tail mode."""

    def test_fewer_lines_than_requested(self):
        log = "Line 1\nLine 2\nLine 3"
        result, count = helpers.truncate_log(log, tail_lines=10)
        assert result == log
        assert count == 3

    def test_exact_lines_requested(self):
        log = "Line 1\nLine 2\nLine 3"
        result, count = helpers.truncate_log(log, tail_lines=3)
        assert result == log
        assert count == 3

    def test_tail_mode_returns_last_n_lines(self):
        lines = [f"Line {i}" for i in range(100)]
        log = "\n".join(lines)
        result, count = helpers.truncate_log(log, tail_lines=10)
        assert "Line 99" in result
        assert "Line 90" in result
        assert "Line 0" not in result
        assert count == 100

    def test_tail_zero_returns_full_log(self):
        log = "Line 1\nLine 2\nLine 3"
        result, count = helpers.truncate_log(log, tail_lines=0)
        assert result == log
        assert count == 3

    def test_empty_log(self):
        result, count = helpers.truncate_log("", tail_lines=10)
        assert result == ""
        assert count == 0

    def test_single_line_no_newline(self):
        result, count = helpers.truncate_log("Single line", tail_lines=10)
        assert result == "Single line"
        assert count == 1

    def test_preserves_trailing_newline(self):
        log = "Line 1\nLine 2\n"
        result, count = helpers.truncate_log(log, tail_lines=10)
        assert result == log
        assert count == 2


class TestFilterLogLines:
    """Test log line filtering (grep functionality)."""

    def test_filter_case_insensitive(self):
        log = "INFO: Starting\nERROR: Failed\nINFO: Retrying"
        result = helpers.filter_log_lines(log, "ERROR")
        assert result == "ERROR: Failed"

    def test_filter_lowercase_pattern(self):
        log = "INFO: Starting\nERROR: Failed\nINFO: Retrying"
        result = helpers.filter_log_lines(log, "error")
        assert result == "ERROR: Failed"

    def test_filter_multiple_matches(self):
        log = "INFO: Starting\nERROR: Failed\nERROR: Crashed\nINFO: Done"
        result = helpers.filter_log_lines(log, "ERROR")
        assert "ERROR: Failed" in result
        assert "ERROR: Crashed" in result
        assert "INFO:" not in result

    def test_filter_no_matches(self):
        log = "INFO: Starting\nINFO: Done"
        result = helpers.filter_log_lines(log, "ERROR")
        assert result == ""

    def test_filter_partial_match(self):
        log = "Build started\nBuild failed\nTest passed"
        result = helpers.filter_log_lines(log, "fail")
        assert result == "Build failed"

    def test_filter_preserves_line_structure(self):
        log = "Line 1: ERROR\nLine 2: INFO\nLine 3: ERROR"
        result = helpers.filter_log_lines(log, "ERROR")
        lines = result.split("\n")
        assert len(lines) == 2
        assert lines[0] == "Line 1: ERROR"
        assert lines[1] == "Line 3: ERROR"

    def test_empty_log(self):
        result = helpers.filter_log_lines("", "ERROR")
        assert result == ""

    def test_empty_pattern_returns_full_log(self):
        log = "Line 1\nLine 2"
        result = helpers.filter_log_lines(log, "")
        assert result == log


class TestCheckAuthRedirect:
    """Test HTML login page detection."""

    def test_detect_html_login_page(self):
        body = "<html><body><form>...j_security_check...</form></body></html>"
        headers = {"Content-Type": "text/html"}
        result = helpers.check_auth_redirect(200, body, headers)
        assert result is not None
        assert result["error"] == "authentication_failed"
        assert "login page" in result["detail"]

    def test_detect_html_with_login_form(self):
        body = "<html><form action='/login'>...</form></html>"
        headers = {"Content-Type": "text/html; charset=utf-8"}
        result = helpers.check_auth_redirect(200, body, headers)
        assert result is not None
        assert "authentication_failed" in result["error"]

    def test_detect_case_insensitive_j_security_check(self):
        body = "<html><form>...J_SECURITY_CHECK...</form></html>"
        headers = {"Content-Type": "text/html"}
        result = helpers.check_auth_redirect(200, body, headers)
        assert result is not None
        assert result["error"] == "authentication_failed"

    def test_detect_302_redirect_to_login(self):
        body = ""
        headers = {"Location": "/login"}
        result = helpers.check_auth_redirect(302, body, headers)
        assert result is not None
        assert result["error"] == "authentication_failed"
        assert "redirect" in result["detail"].lower()

    def test_detect_303_redirect_to_login(self):
        body = ""
        headers = {"Location": "https://jenkins.example.com/securityRealm/commenceLogin"}
        result = helpers.check_auth_redirect(303, body, headers)
        assert result is not None
        assert result["error"] == "authentication_failed"

    def test_ignore_json_response(self):
        body = {"mode": "NORMAL", "jobs": []}
        headers = {"Content-Type": "application/json"}
        result = helpers.check_auth_redirect(200, body, headers)
        assert result is None

    def test_ignore_html_without_form(self):
        body = "<html><body>Welcome to Jenkins</body></html>"
        headers = {"Content-Type": "text/html"}
        result = helpers.check_auth_redirect(200, body, headers)
        assert result is None

    def test_ignore_non_html_content_type(self):
        body = "<html><form>login</form></html>"
        headers = {"Content-Type": "text/plain"}
        result = helpers.check_auth_redirect(200, body, headers)
        assert result is None

    def test_ignore_302_redirect_to_non_login_url(self):
        body = ""
        headers = {"Location": "/job/my-job/build"}
        result = helpers.check_auth_redirect(302, body, headers)
        assert result is None


class TestTruncateResult:
    """Test result size limiting."""

    def test_short_text_unchanged(self):
        text = "Short result"
        result = helpers.truncate_result(text, max_size=100)
        assert result == text

    def test_truncate_long_text(self):
        text = "A" * 1000
        result = helpers.truncate_result(text, max_size=100)
        assert len(result) < len(text)
        assert "truncated" in result
        assert "900 bytes omitted" in result

    def test_truncate_at_exact_limit(self):
        text = "A" * 100
        result = helpers.truncate_result(text, max_size=100)
        assert result == text  # No truncation

    def test_default_max_size(self):
        text = "A" * 100000
        result = helpers.truncate_result(text)
        assert len(result) < len(text)
        assert "truncated" in result


class TestExtractBriefBuild:
    """Test build data extraction from Jenkins API."""

    def test_extract_completed_build(self):
        jenkins_data = {
            "number": 42,
            "result": "SUCCESS",
            "duration": 125000,
            "timestamp": 1715068800000,
            "building": False,
            "actions": [
                {"causes": [{"shortDescription": "Started by user admin"}]},
                {"parameters": [{"name": "BRANCH", "value": "main"}, {"name": "DEPLOY_ENV", "value": "staging"}]},
            ],
            "changeSets": [
                {"items": [{"commitId": "abc123", "author": {"fullName": "Jane Dev"}, "msg": "Fix login bug"}]}
            ],
            "artifacts": [{"fileName": "build.jar"}, {"fileName": "test-results.xml"}],
        }

        result = helpers.extract_brief_build(jenkins_data)

        assert result["number"] == 42
        assert result["result"] == "SUCCESS"
        assert result["duration_ms"] == 125000
        assert result["building"] is False
        assert result["causes"] == ["Started by user admin"]
        assert result["parameters"] == {"BRANCH": "main", "DEPLOY_ENV": "staging"}
        assert len(result["changes"]) == 1
        assert result["changes"][0]["commit"] == "abc123"
        assert result["changes"][0]["author"] == "Jane Dev"
        assert result["changes"][0]["message"] == "Fix login bug"
        assert result["artifacts"] == ["build.jar", "test-results.xml"]

    def test_extract_running_build(self):
        jenkins_data = {
            "number": 43,
            "result": None,
            "duration": 0,
            "timestamp": 1715068900000,
            "building": True,
            "actions": [],
            "changeSets": [],
            "artifacts": [],
        }

        result = helpers.extract_brief_build(jenkins_data)

        assert result["number"] == 43
        assert result["result"] is None
        assert result["building"] is True
        assert result["causes"] == []
        assert result["parameters"] == {}
        assert result["changes"] == []
        assert result["artifacts"] == []

    def test_extract_build_no_actions(self):
        jenkins_data = {"number": 44, "result": "FAILURE", "building": False, "actions": []}

        result = helpers.extract_brief_build(jenkins_data)
        assert result["causes"] == []
        assert result["parameters"] == {}

    def test_extract_build_scrubs_sensitive_parameters(self):
        jenkins_data = {
            "number": 45,
            "result": "SUCCESS",
            "building": False,
            "actions": [
                {
                    "parameters": [
                        {"name": "BRANCH", "value": "main"},
                        {"name": "API_KEY", "value": "secret123"},
                        {"name": "DEPLOY_PASSWORD", "value": "pass456"},
                        {"name": "GITHUB_TOKEN", "value": "ghp_abcdef"},
                        {"name": "BUILD_NUMBER", "value": "45"},
                    ]
                }
            ],
            "changeSets": [],
            "artifacts": [],
        }

        result = helpers.extract_brief_build(jenkins_data)

        # Normal parameters preserved
        assert result["parameters"]["BRANCH"] == "main"
        assert result["parameters"]["BUILD_NUMBER"] == "45"
        # Sensitive parameters redacted
        assert result["parameters"]["API_KEY"] == "[REDACTED]"
        assert result["parameters"]["DEPLOY_PASSWORD"] == "[REDACTED]"
        assert result["parameters"]["GITHUB_TOKEN"] == "[REDACTED]"


class TestDetectJobType:
    """Test job type detection from _class field."""

    def test_detect_freestyle(self):
        job = {"_class": "hudson.model.FreeStyleProject"}
        assert helpers.detect_job_type(job) == "freestyle"

    def test_detect_pipeline(self):
        job = {"_class": "org.jenkinsci.plugins.workflow.job.WorkflowJob"}
        assert helpers.detect_job_type(job) == "pipeline"

    def test_detect_folder(self):
        job = {"_class": "com.cloudbees.hudson.plugins.folder.Folder"}
        assert helpers.detect_job_type(job) == "folder"

    def test_detect_multibranch(self):
        job = {"_class": "org.jenkinsci.plugins.workflow.multibranch.WorkflowMultiBranchProject"}
        assert helpers.detect_job_type(job) == "multibranch"

    def test_detect_unknown(self):
        job = {"_class": "com.example.UnknownProject"}
        assert helpers.detect_job_type(job) == "unknown"

    def test_missing_class_field(self):
        job = {}
        assert helpers.detect_job_type(job) == "unknown"


class TestExtractBriefJob:
    """Test job data extraction from Jenkins API."""

    def test_extract_simple_job(self):
        jenkins_data = {
            "name": "my-pipeline",
            "_class": "org.jenkinsci.plugins.workflow.job.WorkflowJob",
            "url": "https://jenkins.example.com/job/my-pipeline/",
            "color": "blue",
            "healthReport": [{"score": 80, "description": "Build stability: 4 out of 5 recent builds succeeded."}],
            "lastBuild": {
                "number": 42,
                "result": "SUCCESS",
                "timestamp": 1715068800000,
            },
        }

        result = helpers.extract_brief_job(jenkins_data, "")

        assert result["name"] == "my-pipeline"
        assert result["type"] == "pipeline"
        assert result["folder"] == ""
        assert result["last_build"] == 42
        assert result["last_result"] == "SUCCESS"
        assert result["health"] == 80

    def test_extract_job_in_folder(self):
        jenkins_data = {
            "name": "my-job",
            "_class": "hudson.model.FreeStyleProject",
            "healthReport": [],
            "lastBuild": None,
        }

        result = helpers.extract_brief_job(jenkins_data, "team/project")

        assert result["name"] == "my-job"
        assert result["type"] == "freestyle"
        assert result["folder"] == "team/project"
        assert result["last_build"] is None
        assert result["last_result"] is None
        assert result["health"] is None

    def test_extract_job_no_health_report(self):
        jenkins_data = {
            "name": "new-job",
            "_class": "hudson.model.FreeStyleProject",
            "healthReport": [],
        }

        result = helpers.extract_brief_job(jenkins_data, "")
        assert result["health"] is None


class TestParseJunitXml:
    """Test parse_junit_xml helper."""

    def test_parse_basic_junit_xml(self):
        xml = """<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="TestSuite1" tests="3" failures="1" skipped="1" time="1.5">
    <testcase name="test_success" classname="MyTests" time="0.5"/>
    <testcase name="test_failure" classname="MyTests" time="0.8">
      <failure message="AssertionError: expected 5 but got 3">
Traceback (most recent call last):
  File "test.py", line 10, in test_failure
    assert result == 5
AssertionError: expected 5 but got 3
      </failure>
    </testcase>
    <testcase name="test_skipped" classname="MyTests" time="0.2">
      <skipped message="Not implemented yet"/>
    </testcase>
  </testsuite>
</testsuites>"""

        result = helpers.parse_junit_xml(xml)

        assert result["total"] == 3
        assert result["passed"] == 1
        assert result["failed"] == 1
        assert result["skipped"] == 1
        assert result["duration_seconds"] == 1.5
        assert len(result["failures"]) == 1

        failure = result["failures"][0]
        assert failure["name"] == "test_failure"
        assert failure["classname"] == "MyTests"
        assert "expected 5 but got 3" in failure["message"]
        assert "Traceback" in failure["trace"]

    def test_truncate_long_stack_trace(self):
        long_trace = "Line\n" * 120  # 600 chars
        xml = f"""<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="Test" tests="1" failures="1">
    <testcase name="test_fail" classname="Test">
      <failure message="Error">{long_trace}</failure>
    </testcase>
  </testsuite>
</testsuites>"""

        result = helpers.parse_junit_xml(xml)

        assert len(result["failures"]) == 1
        # Trace should be truncated to 500 chars
        assert len(result["failures"][0]["trace"]) <= 500
        assert result["failures"][0]["trace"].endswith("...")

    def test_parse_all_passed(self):
        xml = """<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="Test" tests="2" failures="0" skipped="0">
    <testcase name="test_1" classname="Test"/>
    <testcase name="test_2" classname="Test"/>
  </testsuite>
</testsuites>"""

        result = helpers.parse_junit_xml(xml)

        assert result["total"] == 2
        assert result["passed"] == 2
        assert result["failed"] == 0
        assert result["failures"] == []

    def test_parse_invalid_xml(self):
        xml = "not valid XML"

        result = helpers.parse_junit_xml(xml)

        assert "error" in result
        assert "parse_error" in result["error"]

    def test_parse_empty_xml(self):
        xml = """<?xml version="1.0" encoding="UTF-8"?>
<testsuites></testsuites>"""

        result = helpers.parse_junit_xml(xml)

        assert result["total"] == 0
        assert result["passed"] == 0
        assert result["failed"] == 0
        assert result["skipped"] == 0
        assert result["failures"] == []

    def test_multiple_testsuites(self):
        xml = """<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="Suite1" tests="2" failures="1">
    <testcase name="test_1" classname="Suite1"/>
    <testcase name="test_2" classname="Suite1">
      <failure message="Failed"/>
    </testcase>
  </testsuite>
  <testsuite name="Suite2" tests="1" skipped="1">
    <testcase name="test_3" classname="Suite2">
      <skipped/>
    </testcase>
  </testsuite>
</testsuites>"""

        result = helpers.parse_junit_xml(xml)

        assert result["total"] == 3
        assert result["passed"] == 1
        assert result["failed"] == 1
        assert result["skipped"] == 1

    def test_standalone_testsuite_as_root(self):
        xml = """<?xml version="1.0" encoding="UTF-8"?>
<testsuite name="Test" tests="2" failures="1" skipped="0" time="1.0">
  <testcase name="test_1" classname="Test"/>
  <testcase name="test_2" classname="Test">
    <failure message="Failed"/>
  </testcase>
</testsuite>"""

        result = helpers.parse_junit_xml(xml)

        assert result["total"] == 2
        assert result["passed"] == 1
        assert result["failed"] == 1
        assert result["skipped"] == 0
        assert len(result["failures"]) == 1
