"""Unit tests for helpers.py — pure functions, no mocking needed."""

import json

import pytest
from helpers import (
    _MAX_WIKI_PARSE_DEPTH,
    _MAX_WIKI_PARSE_LEN,
    _is_safe_url,
    _looks_like_wiki_markup,
    _parse_inline_markup,
    adf_to_text,
    calculate_sprint_metrics,
    escape_jql,
    extract_brief_issue,
    extract_issue_fields,
    extract_nested_field,
    extract_sprint_summary,
    extract_user_fields,
    is_user_alias,
    natural_sort_key,
    normalize_components,
    parse_sprint_field,
    resolve_field_value,
    text_to_adf,
    validate_issue_key,
    wiki_to_adf,
)

# --- validate_issue_key ---


class TestValidateIssueKey:
    def test_valid_simple(self):
        assert validate_issue_key("PROJ-123") == "PROJ-123"

    def test_valid_uppercase(self):
        assert validate_issue_key("proj-1") == "PROJ-1"

    def test_valid_with_whitespace(self):
        assert validate_issue_key("  ABC-42  ") == "ABC-42"

    def test_valid_with_numbers_in_project(self):
        assert validate_issue_key("RHEL9-100") == "RHEL9-100"

    def test_valid_with_underscore(self):
        assert validate_issue_key("MY_PROJ-7") == "MY_PROJ-7"

    def test_invalid_no_dash(self):
        with pytest.raises(ValueError, match="Invalid issue key"):
            validate_issue_key("PROJ123")

    def test_invalid_no_number(self):
        with pytest.raises(ValueError, match="Invalid issue key"):
            validate_issue_key("PROJ-")

    def test_invalid_starts_with_number(self):
        with pytest.raises(ValueError, match="Invalid issue key"):
            validate_issue_key("123-ABC")

    def test_invalid_empty(self):
        with pytest.raises(ValueError, match="Invalid issue key"):
            validate_issue_key("")

    def test_invalid_just_whitespace(self):
        with pytest.raises(ValueError, match="Invalid issue key"):
            validate_issue_key("   ")

    def test_invalid_lowercase_dash(self):
        # Single lowercase letter doesn't match [A-Z]
        with pytest.raises(ValueError, match="Invalid issue key"):
            validate_issue_key("a-1")


# --- is_user_alias ---


class TestIsUserAlias:
    def test_me(self):
        assert is_user_alias("me") is True

    def test_myself(self):
        assert is_user_alias("myself") is True

    def test_currentuser(self):
        assert is_user_alias("currentUser") is True

    def test_case_insensitive(self):
        assert is_user_alias("ME") is True
        assert is_user_alias("Myself") is True
        assert is_user_alias("CURRENTUSER") is True

    def test_with_whitespace(self):
        assert is_user_alias("  me  ") is True

    def test_regular_username(self):
        assert is_user_alias("jdoe") is False

    def test_empty(self):
        assert is_user_alias("") is False


# --- extract_brief_issue ---


def _make_issue(key="TEST-1", summary="Fix bug", status="Open", assignee="Jane", priority="High"):
    """Build a Jira issue dict for testing."""
    fields = {"summary": summary}
    if status is not None:
        fields["status"] = {"name": status}
    if assignee is not None:
        fields["assignee"] = {"displayName": assignee}
    if priority is not None:
        fields["priority"] = {"name": priority}
    return {"key": key, "fields": fields}


class TestExtractBriefIssue:
    def test_full_fields(self):
        result = extract_brief_issue(_make_issue())
        assert result == {
            "key": "TEST-1",
            "summary": "Fix bug",
            "status": "Open",
            "assignee": "Jane",
            "priority": "High",
        }

    def test_missing_status(self):
        result = extract_brief_issue(_make_issue(status=None))
        assert result["status"] == ""

    def test_missing_assignee(self):
        result = extract_brief_issue(_make_issue(assignee=None))
        assert result["assignee"] == ""

    def test_missing_priority(self):
        result = extract_brief_issue(_make_issue(priority=None))
        assert result["priority"] == ""

    def test_empty_fields(self):
        result = extract_brief_issue({"key": "X-1", "fields": {}})
        assert result["summary"] == ""
        assert result["status"] == ""
        assert result["assignee"] == ""
        assert result["priority"] == ""

    def test_no_fields_key(self):
        result = extract_brief_issue({"key": "X-1"})
        assert result["key"] == "X-1"
        assert result["summary"] == ""

    def test_non_dict_status(self):
        # Edge case: status is a string (shouldn't happen, but be safe)
        issue = {"key": "X-1", "fields": {"status": "Open"}}
        result = extract_brief_issue(issue)
        assert result["status"] == ""


# --- extract_issue_fields ---


class TestExtractIssueFields:
    def test_default_fields(self):
        issue = _make_issue()
        result = extract_issue_fields(issue, "summary,status,assignee,priority")
        assert result == {
            "key": "TEST-1",
            "summary": "Fix bug",
            "status": "Open",
            "assignee": "Jane",
            "priority": "High",
        }

    def test_key_always_included(self):
        result = extract_issue_fields({"key": "X-1", "fields": {}}, "summary")
        assert "key" in result
        assert result["key"] == "X-1"

    def test_custom_fields(self):
        issue = {
            "key": "X-1",
            "fields": {
                "summary": "Test",
                "labels": ["urgent", "p1"],
                "created": "2026-01-01T10:00:00.000+0000",
            },
        }
        result = extract_issue_fields(issue, "summary,labels,created")
        assert result["summary"] == "Test"
        assert result["labels"] == ["urgent", "p1"]
        assert result["created"] == "2026-01-01T10:00:00.000+0000"

    def test_dotted_path(self):
        issue = {"key": "X-1", "fields": {"status": {"name": "Open", "id": "1"}}}
        result = extract_issue_fields(issue, "status.name")
        assert result["status.name"] == "Open"

    def test_missing_fields(self):
        result = extract_issue_fields({"key": "X-1", "fields": {}}, "summary,nonexistent")
        assert result["summary"] is None
        assert result["nonexistent"] is None

    def test_empty_fields_str(self):
        result = extract_issue_fields({"key": "X-1", "fields": {"summary": "Hi"}}, "")
        assert result == {"key": "X-1"}

    def test_none_fields_str(self):
        result = extract_issue_fields({"key": "X-1", "fields": {"summary": "Hi"}}, None)
        assert result == {"key": "X-1"}

    def test_whitespace_in_field_names(self):
        issue = _make_issue()
        result = extract_issue_fields(issue, "summary, status , priority")
        assert result["summary"] == "Fix bug"
        assert result["status"] == "Open"
        assert result["priority"] == "High"

    def test_adf_description(self):
        adf_doc = {
            "type": "doc",
            "version": 1,
            "content": [
                {
                    "type": "paragraph",
                    "content": [{"type": "text", "text": "Hello world"}],
                }
            ],
        }
        issue = {"key": "X-1", "fields": {"description": adf_doc}}
        result = extract_issue_fields(issue, "description")
        assert result["description"] == "Hello world"

    def test_array_fields_passthrough(self):
        issue = {
            "key": "X-1",
            "fields": {
                "components": [{"name": "Backend"}, {"name": "API"}],
                "labels": ["bug", "critical"],
            },
        }
        result = extract_issue_fields(issue, "components,labels")
        # components is a list of dicts — extract_nested_field returns as-is
        assert isinstance(result["labels"], list)
        assert result["labels"] == ["bug", "critical"]

    def test_dict_flattening(self):
        issue = {
            "key": "X-1",
            "fields": {
                "status": {
                    "self": "https://...",
                    "name": "In Progress",
                    "id": "3",
                    "statusCategory": {"name": "In Progress"},
                },
            },
        }
        result = extract_issue_fields(issue, "status")
        # Dict should be flattened to .name
        assert result["status"] == "In Progress"


# --- extract_user_fields ---


class TestExtractUserFields:
    def test_cloud_user(self):
        user = {
            "accountId": "abc123",
            "name": "jdoe",
            "displayName": "Jane Doe",
            "emailAddress": "jane@example.com",
            "active": True,
            "timeZone": "America/New_York",
        }
        result = extract_user_fields(user)
        assert result["accountId"] == "abc123"
        assert result["displayName"] == "Jane Doe"
        assert result["emailAddress"] == "jane@example.com"
        assert result["active"] is True
        assert result["timeZone"] == "America/New_York"

    def test_server_user_key_fallback(self):
        user = {
            "key": "jdoe",
            "name": "jdoe",
            "displayName": "Jane Doe",
        }
        result = extract_user_fields(user)
        assert result["accountId"] == "jdoe"

    def test_missing_fields(self):
        result = extract_user_fields({})
        assert result["accountId"] is None
        assert result["displayName"] is None
        assert result["active"] is None


# --- text_to_adf ---


class TestTextToAdf:
    def test_simple_text(self):
        result = text_to_adf("Hello world")
        assert result["version"] == 1
        assert result["type"] == "doc"
        assert len(result["content"]) == 1
        assert result["content"][0]["content"][0]["text"] == "Hello world"

    def test_multiline(self):
        result = text_to_adf("Line 1\nLine 2\nLine 3")
        assert len(result["content"]) == 3
        assert result["content"][0]["content"][0]["text"] == "Line 1"
        assert result["content"][2]["content"][0]["text"] == "Line 3"

    def test_empty_lines_become_empty_paragraphs(self):
        result = text_to_adf("Before\n\nAfter")
        assert len(result["content"]) == 3
        assert result["content"][1]["content"] == []

    def test_empty_string(self):
        result = text_to_adf("")
        assert result["content"] == [{"type": "paragraph", "content": []}]

    def test_none(self):
        result = text_to_adf(None)
        assert result["content"] == [{"type": "paragraph", "content": []}]

    def test_adf_dict_passthrough(self):
        adf = {
            "version": 1,
            "type": "doc",
            "content": [{"type": "paragraph", "content": [{"type": "text", "text": "Pre-built"}]}],
        }
        result = text_to_adf(adf)
        assert result is adf

    def test_adf_json_string_passthrough(self):
        adf = {
            "version": 1,
            "type": "doc",
            "content": [{"type": "paragraph", "content": [{"type": "text", "text": "From JSON"}]}],
        }
        result = text_to_adf(json.dumps(adf))
        assert result == adf

    def test_invalid_adf_dict_raises(self):
        with pytest.raises(ValueError, match="Invalid ADF dict"):
            text_to_adf({"type": "not_doc", "version": 1})

    def test_empty_dict_treated_as_empty(self):
        result = text_to_adf({})
        assert result["content"] == [{"type": "paragraph", "content": []}]

    def test_invalid_adf_dict_wrong_version(self):
        with pytest.raises(ValueError, match="Invalid ADF dict"):
            text_to_adf({"type": "doc", "version": 2})

    def test_json_array_string_treated_as_text(self):
        result = text_to_adf("[1, 2, 3]")
        assert result["type"] == "doc"
        assert result["content"][0]["content"][0]["text"] == "[1, 2, 3]"

    def test_json_string_invalid_adf_treated_as_text(self):
        result = text_to_adf('{"type": "table", "version": 1}')
        assert result["type"] == "doc"
        assert result["content"][0]["content"][0]["text"] == '{"type": "table", "version": 1}'

    def test_non_adf_json_string_treated_as_text(self):
        result = text_to_adf('{"key": "value"}')
        assert result["type"] == "doc"
        assert result["content"][0]["content"][0]["text"] == '{"key": "value"}'

    def test_plain_text_with_url_becomes_link(self):
        result = text_to_adf("See https://example.com for details")
        nodes = result["content"][0]["content"]
        assert nodes[0] == {"type": "text", "text": "See "}
        assert nodes[1]["text"] == "https://example.com"
        assert nodes[1]["marks"] == [{"type": "link", "attrs": {"href": "https://example.com"}}]
        assert nodes[2] == {"type": "text", "text": " for details"}

    def test_plain_text_url_only(self):
        result = text_to_adf("https://gitlab.com/redhat/centos-stream/rpms/python-jwcrypto/-/merge_requests/20")
        nodes = result["content"][0]["content"]
        assert len(nodes) == 1
        assert nodes[0]["text"] == "https://gitlab.com/redhat/centos-stream/rpms/python-jwcrypto/-/merge_requests/20"
        assert nodes[0]["marks"] == [
            {
                "type": "link",
                "attrs": {"href": "https://gitlab.com/redhat/centos-stream/rpms/python-jwcrypto/-/merge_requests/20"},
            }
        ]

    def test_plain_text_url_trailing_period_stripped(self):
        result = text_to_adf("See https://example.com.")
        nodes = result["content"][0]["content"]
        link_node = next(n for n in nodes if n.get("marks"))
        assert link_node["text"] == "https://example.com"
        assert link_node["marks"] == [{"type": "link", "attrs": {"href": "https://example.com"}}]
        assert any(n.get("text") == "." for n in nodes)

    def test_plain_text_url_with_tilde_query_fragment(self):
        url = "https://example.com/~testuser/content.html?some=value#partial=field"
        result = text_to_adf(url)
        nodes = result["content"][0]["content"]
        assert len(nodes) == 1
        assert nodes[0]["text"] == url
        assert nodes[0]["marks"] == [{"type": "link", "attrs": {"href": url}}]

    def test_plain_text_mr_comment_url(self):
        """Regression: MR comment with bare URL should render URL as clickable link."""
        text = "Merge request for rebase to 1.5.8: https://gitlab.com/redhat/centos-stream/rpms/python-jwcrypto/-/merge_requests/20"
        result = text_to_adf(text)
        nodes = result["content"][0]["content"]
        assert nodes[0]["text"] == "Merge request for rebase to 1.5.8: "
        assert nodes[1]["marks"] == [
            {
                "type": "link",
                "attrs": {"href": "https://gitlab.com/redhat/centos-stream/rpms/python-jwcrypto/-/merge_requests/20"},
            }
        ]

    def test_url_with_jql_in_parentheses(self):
        """JQL filter URLs with balanced parentheses must not be truncated."""
        url = "https://jira.example.com/issues/?jql=project+in+(FOO,BAR)"
        result = text_to_adf(f"See {url}")
        nodes = result["content"][0]["content"]
        link_node = next(n for n in nodes if n.get("marks"))
        assert link_node["text"] == url
        assert link_node["marks"] == [{"type": "link", "attrs": {"href": url}}]

    def test_url_with_wiki_page_parentheses(self):
        """Wikipedia-style URLs with balanced parentheses must not be truncated."""
        url = "https://en.wikipedia.org/wiki/Foo_(bar)"
        result = text_to_adf(f"See {url}")
        nodes = result["content"][0]["content"]
        link_node = next(n for n in nodes if n.get("marks"))
        assert link_node["text"] == url
        assert link_node["marks"] == [{"type": "link", "attrs": {"href": url}}]

    def test_url_with_multiple_parentheses_balanced(self):
        """URLs with multiple balanced parentheses groups must not be truncated."""
        url = "https://example.com/api(v1)/search?q=(a+b)+(c+d)"
        result = text_to_adf(f"API: {url}")
        nodes = result["content"][0]["content"]
        link_node = next(n for n in nodes if n.get("marks"))
        assert link_node["text"] == url
        assert link_node["marks"] == [{"type": "link", "attrs": {"href": url}}]

    def test_url_with_unbalanced_parenthesis_truncated(self):
        """URLs with unbalanced closing parentheses should still be truncated."""
        result = text_to_adf("See https://example.com.")
        nodes = result["content"][0]["content"]
        link_node = next(n for n in nodes if n.get("marks"))
        assert link_node["text"] == "https://example.com"
        assert any(n.get("text") == "." for n in nodes)


# --- normalize_components ---


class TestNormalizeComponents:
    def test_string_list(self):
        result = normalize_components(["Web", "API"])
        assert result == [{"name": "Web"}, {"name": "API"}]

    def test_dict_passthrough(self):
        result = normalize_components([{"name": "Web", "id": "123"}])
        assert result == [{"name": "Web", "id": "123"}]

    def test_mixed(self):
        result = normalize_components(["Web", {"name": "API"}])
        assert result == [{"name": "Web"}, {"name": "API"}]

    def test_empty(self):
        assert normalize_components([]) == []


# --- parse_sprint_field ---


class TestParseSprintField:
    def test_cloud_dict(self):
        data = {"id": 123, "name": "Sprint 1", "state": "active"}
        assert parse_sprint_field(data) == data

    def test_server_string(self):
        data = "com.atlassian.greenhopper.service.sprint.Sprint@abc[id=123,name=Sprint 1,state=active]"
        result = parse_sprint_field(data)
        assert result["id"] == "123"
        assert result["name"] == "Sprint 1"
        assert result["state"] == "active"

    def test_empty(self):
        assert parse_sprint_field(None) == {}
        assert parse_sprint_field(42) == {}


# --- natural_sort_key ---


class TestNaturalSortKey:
    def test_numeric_sorting(self):
        names = ["Sprint 10", "Sprint 2", "Sprint 1", "Sprint 9"]
        sorted_names = sorted(names, key=natural_sort_key)
        assert sorted_names == ["Sprint 1", "Sprint 2", "Sprint 9", "Sprint 10"]

    def test_alpha_sorting(self):
        names = ["Beta", "Alpha", "Gamma"]
        assert sorted(names, key=natural_sort_key) == ["Alpha", "Beta", "Gamma"]


# --- escape_jql ---


class TestEscapeJql:
    def test_quotes(self):
        assert escape_jql('Sprint "1"') == 'Sprint \\"1\\"'

    def test_backslash(self):
        assert escape_jql("a\\b") == "a\\\\b"

    def test_newlines(self):
        assert escape_jql("a\nb") == "a\\nb"

    def test_null_bytes(self):
        assert escape_jql("a\0b") == "ab"

    def test_clean_string(self):
        assert escape_jql("Sprint 35") == "Sprint 35"


# --- extract_sprint_summary ---


class TestExtractSprintSummary:
    def test_full(self):
        sprint = {
            "id": 1,
            "name": "S1",
            "state": "active",
            "startDate": "2026-01-01",
            "endDate": "2026-01-14",
            "extra": "x",
        }
        result = extract_sprint_summary(sprint)
        assert result == {"id": 1, "name": "S1", "state": "active", "startDate": "2026-01-01", "endDate": "2026-01-14"}
        assert "extra" not in result

    def test_partial(self):
        result = extract_sprint_summary({"id": 1, "name": "S1"})
        assert result["state"] is None


# --- extract_nested_field ---


class TestExtractNestedField:
    def test_simple_string(self):
        assert extract_nested_field({"summary": "Hello"}, "summary") == "Hello"

    def test_dotted_path(self):
        assert extract_nested_field({"status": {"name": "Open"}}, "status.name") == "Open"

    def test_dotted_missing(self):
        assert extract_nested_field({"status": {"id": 1}}, "status.name") is None

    def test_dict_auto_extract_name(self):
        assert extract_nested_field({"priority": {"name": "High"}}, "priority") == "High"

    def test_dict_auto_extract_value(self):
        assert extract_nested_field({"team": {"value": "Alpha"}}, "team") == "Alpha"

    def test_missing_field(self):
        assert extract_nested_field({}, "nope") is None

    def test_non_dict_parent(self):
        assert extract_nested_field({"status": "Open"}, "status.name") is None


# --- calculate_sprint_metrics ---


class TestCalculateSprintMetrics:
    def test_basic_metrics(self):
        issues = [
            {"fields": {"status": {"statusCategory": {"key": "done"}}}},
            {"fields": {"status": {"statusCategory": {"key": "done"}}}},
            {"fields": {"status": {"statusCategory": {"key": "indeterminate"}}}},
        ]
        result = calculate_sprint_metrics(issues)
        assert result["total_issues"] == 3
        assert result["completed_issues"] == 2
        assert result["completion_rate"] == 66.7

    def test_empty(self):
        result = calculate_sprint_metrics([])
        assert result["total_issues"] == 0
        assert result["completion_rate"] == 0

    def test_all_done(self):
        issues = [{"fields": {"status": {"statusCategory": {"key": "done"}}}}]
        result = calculate_sprint_metrics(issues)
        assert result["completion_rate"] == 100.0

    def test_missing_status(self):
        issues = [{"fields": {}}]
        result = calculate_sprint_metrics(issues)
        assert result["completed_issues"] == 0


# --- resolve_field_value ---


class TestResolveFieldValue:
    # --- auto detection ---
    def test_auto_int(self):
        val, ft = resolve_field_value(42, "auto")
        assert val == 42.0
        assert ft == "number"

    def test_auto_float(self):
        val, ft = resolve_field_value(3.14, "auto")
        assert val == 3.14
        assert ft == "number"

    def test_auto_bool_is_number(self):
        # bool is subclass of int in Python — auto-detects as number
        val, ft = resolve_field_value(True, "auto")
        assert val == 1.0
        assert ft == "number"

    def test_auto_list(self):
        val, ft = resolve_field_value(["a", "b"], "auto")
        assert val == [{"value": "a"}, {"value": "b"}]
        assert ft == "multi-select"

    def test_auto_string(self):
        val, ft = resolve_field_value("hello", "auto")
        assert val == "hello"
        assert ft == "text"

    def test_auto_dict(self):
        val, ft = resolve_field_value({"key": "val"}, "auto")
        assert val == {"key": "val"}
        assert ft == "text"

    # --- explicit types ---
    def test_number_from_string(self):
        val, ft = resolve_field_value("3.14", "number")
        assert val == 3.14

    def test_number_invalid(self):
        with pytest.raises(ValueError, match="Cannot convert"):
            resolve_field_value("abc", "number")

    def test_number_inf(self):
        with pytest.raises(ValueError, match="finite"):
            resolve_field_value(float("inf"), "number")

    def test_number_nan(self):
        with pytest.raises(ValueError, match="finite"):
            resolve_field_value(float("nan"), "number")

    def test_select(self):
        val, ft = resolve_field_value("Option A", "select")
        assert val == {"value": "Option A"}
        assert ft == "select"

    def test_multi_select_single(self):
        val, ft = resolve_field_value("one", "multi-select")
        assert val == [{"value": "one"}]

    def test_multi_select_list(self):
        val, ft = resolve_field_value(["a", "b"], "multi-select")
        assert val == [{"value": "a"}, {"value": "b"}]

    def test_version_single(self):
        val, ft = resolve_field_value("1.0", "version")
        assert val == [{"name": "1.0"}]

    def test_version_list(self):
        val, ft = resolve_field_value(["1.0", "2.0"], "version")
        assert val == [{"name": "1.0"}, {"name": "2.0"}]

    def test_user_cloud(self):
        val, ft = resolve_field_value("abc123", "user", is_cloud=True)
        assert val == {"accountId": "abc123"}

    def test_user_server(self):
        val, ft = resolve_field_value("jdoe", "user", is_cloud=False)
        assert val == {"name": "jdoe"}

    def test_user_invalid_type(self):
        with pytest.raises(ValueError, match="string value"):
            resolve_field_value(123, "user")

    def test_user_empty_string(self):
        val, ft = resolve_field_value("", "user", is_cloud=True)
        assert val == {"accountId": ""}

    def test_text_passthrough(self):
        val, ft = resolve_field_value("hello", "text")
        assert val == "hello"
        assert ft == "text"

    def test_unknown_type_passthrough(self):
        val, ft = resolve_field_value({"custom": True}, "unknown")
        assert val == {"custom": True}
        assert ft == "unknown"


class TestAdfToText:
    """Test adf_to_text() ADF-to-plain-text conversion."""

    def test_plain_string_passthrough(self):
        """Non-ADF strings pass through unchanged."""
        assert adf_to_text("Hello world") == "Hello world"
        assert adf_to_text("") == ""

    def test_none_passthrough(self):
        """None and non-dict values pass through."""
        assert adf_to_text(None) is None
        assert adf_to_text(123) == 123
        assert adf_to_text([1, 2, 3]) == [1, 2, 3]

    def test_single_paragraph(self):
        """Simple ADF with one paragraph."""
        adf = {
            "type": "doc",
            "version": 1,
            "content": [
                {
                    "type": "paragraph",
                    "content": [{"type": "text", "text": "Hello world"}],
                }
            ],
        }
        assert adf_to_text(adf) == "Hello world"

    def test_multiple_paragraphs(self):
        """ADF with multiple paragraphs separated by newlines."""
        adf = {
            "type": "doc",
            "version": 1,
            "content": [
                {"type": "paragraph", "content": [{"type": "text", "text": "First paragraph"}]},
                {"type": "paragraph", "content": [{"type": "text", "text": "Second paragraph"}]},
            ],
        }
        assert adf_to_text(adf) == "First paragraph\nSecond paragraph"

    def test_empty_paragraphs(self):
        """Empty paragraphs are handled correctly."""
        adf = {
            "type": "doc",
            "version": 1,
            "content": [
                {"type": "paragraph", "content": [{"type": "text", "text": "Before"}]},
                {"type": "paragraph", "content": []},  # Empty paragraph
                {"type": "paragraph", "content": [{"type": "text", "text": "After"}]},
            ],
        }
        result = adf_to_text(adf)
        assert "Before" in result
        assert "After" in result

    def test_nested_content_bold_and_links(self):
        """Nested content (bold, links) - extract text without markup."""
        adf = {
            "type": "doc",
            "version": 1,
            "content": [
                {
                    "type": "paragraph",
                    "content": [
                        {"type": "text", "text": "Normal "},
                        {"type": "text", "text": "bold", "marks": [{"type": "strong"}]},
                        {"type": "text", "text": " and "},
                        {
                            "type": "text",
                            "text": "link",
                            "marks": [{"type": "link", "attrs": {"href": "http://example.com"}}],
                        },
                    ],
                }
            ],
        }
        assert adf_to_text(adf) == "Normal bold and link"

    def test_complex_nested_structure(self):
        """Complex ADF with lists, headings, etc."""
        adf = {
            "type": "doc",
            "version": 1,
            "content": [
                {"type": "heading", "content": [{"type": "text", "text": "Title"}]},
                {"type": "paragraph", "content": [{"type": "text", "text": "Paragraph"}]},
                {
                    "type": "bulletList",
                    "content": [
                        {
                            "type": "listItem",
                            "content": [{"type": "paragraph", "content": [{"type": "text", "text": "Item 1"}]}],
                        },
                        {
                            "type": "listItem",
                            "content": [{"type": "paragraph", "content": [{"type": "text", "text": "Item 2"}]}],
                        },
                    ],
                },
            ],
        }
        result = adf_to_text(adf)
        assert "Title" in result
        assert "Paragraph" in result
        assert "Item 1" in result
        assert "Item 2" in result

    def test_non_doc_dict_passthrough(self):
        """Dict without type=doc passes through unchanged."""
        not_adf = {"key": "value", "foo": "bar"}
        assert adf_to_text(not_adf) == not_adf


# --- Wiki markup detection ---


class TestLooksLikeWikiMarkup:
    def test_heading(self):
        assert _looks_like_wiki_markup("h2. Title")

    def test_heading_h1(self):
        assert _looks_like_wiki_markup("h1. Main Title")

    def test_heading_h6(self):
        assert _looks_like_wiki_markup("h6. Deep heading")

    def test_unordered_list(self):
        assert _looks_like_wiki_markup("* item one")

    def test_ordered_list(self):
        assert _looks_like_wiki_markup("# first item")

    def test_code_block(self):
        assert _looks_like_wiki_markup("{code}\nprint('hi')\n{code}")

    def test_code_block_with_lang(self):
        assert _looks_like_wiki_markup("{code:python}\nx = 1\n{code}")

    def test_quote_block(self):
        assert _looks_like_wiki_markup("{quote}\nsome quote\n{quote}")

    def test_bq_shorthand(self):
        assert _looks_like_wiki_markup("bq. Some quoted text")

    def test_panel(self):
        assert _looks_like_wiki_markup("{panel}\ncontent\n{panel}")

    def test_horizontal_rule(self):
        assert _looks_like_wiki_markup("----")

    def test_table_header(self):
        assert _looks_like_wiki_markup("||H1||H2||")

    def test_plain_text(self):
        assert not _looks_like_wiki_markup("Just some regular text")

    def test_plain_with_asterisk_mid_sentence(self):
        assert not _looks_like_wiki_markup("This is a * note about things")

    def test_json_not_wiki(self):
        assert not _looks_like_wiki_markup('{"key": "value"}')

    def test_multiline_with_heading(self):
        assert _looks_like_wiki_markup("Some intro\nh2. Title\nMore text")


# --- Inline markup parsing ---


class TestParseInlineMarkup:
    def test_plain_text(self):
        nodes = _parse_inline_markup("just plain text")
        assert len(nodes) == 1
        assert nodes[0] == {"type": "text", "text": "just plain text"}

    def test_bold(self):
        nodes = _parse_inline_markup("*bold*")
        assert len(nodes) == 1
        assert nodes[0]["text"] == "bold"
        assert nodes[0]["marks"] == [{"type": "strong"}]

    def test_italic(self):
        nodes = _parse_inline_markup("_italic_")
        assert nodes[0]["text"] == "italic"
        assert nodes[0]["marks"] == [{"type": "em"}]

    def test_strikethrough(self):
        nodes = _parse_inline_markup("-struck-")
        assert nodes[0]["text"] == "struck"
        assert nodes[0]["marks"] == [{"type": "strike"}]

    def test_underline(self):
        nodes = _parse_inline_markup("+under+")
        assert nodes[0]["text"] == "under"
        assert nodes[0]["marks"] == [{"type": "underline"}]

    def test_monospace(self):
        nodes = _parse_inline_markup("{{code}}")
        assert nodes[0]["text"] == "code"
        assert nodes[0]["marks"] == [{"type": "code"}]

    def test_superscript(self):
        nodes = _parse_inline_markup("^sup^")
        assert nodes[0]["text"] == "sup"
        assert nodes[0]["marks"] == [{"type": "subsup", "attrs": {"type": "sup"}}]

    def test_subscript(self):
        nodes = _parse_inline_markup("~sub~")
        assert nodes[0]["text"] == "sub"
        assert nodes[0]["marks"] == [{"type": "subsup", "attrs": {"type": "sub"}}]

    def test_sup_boundary_guard(self):
        """Superscript with spaces around content should not match."""
        nodes = _parse_inline_markup("^ not sup ^")
        assert len(nodes) == 1
        assert nodes[0] == {"type": "text", "text": "^ not sup ^"}

    def test_sub_boundary_guard(self):
        """Subscript with spaces around content should not match."""
        nodes = _parse_inline_markup("~ not sub ~")
        assert len(nodes) == 1
        assert nodes[0] == {"type": "text", "text": "~ not sub ~"}

    def test_mention_and_subscript_tilde(self):
        """Tilde used in mentions and subscript should not conflict."""
        nodes = _parse_inline_markup("[~user] and ~sub~")
        assert nodes[0] == {"type": "mention", "attrs": {"id": "user"}}
        assert nodes[1] == {"type": "text", "text": " and "}
        assert nodes[2]["text"] == "sub"
        assert nodes[2]["marks"] == [{"type": "subsup", "attrs": {"type": "sub"}}]

    def test_link_with_text(self):
        nodes = _parse_inline_markup("[Example|https://example.com]")
        assert nodes[0]["text"] == "Example"
        assert nodes[0]["marks"] == [{"type": "link", "attrs": {"href": "https://example.com"}}]

    def test_bare_link(self):
        nodes = _parse_inline_markup("[https://example.com]")
        assert nodes[0]["text"] == "https://example.com"
        assert nodes[0]["marks"] == [{"type": "link", "attrs": {"href": "https://example.com"}}]

    def test_bare_url_no_brackets(self):
        nodes = _parse_inline_markup("See https://example.com here")
        assert nodes[0] == {"type": "text", "text": "See "}
        assert nodes[1]["text"] == "https://example.com"
        assert nodes[1]["marks"] == [{"type": "link", "attrs": {"href": "https://example.com"}}]
        assert nodes[2] == {"type": "text", "text": " here"}

    def test_bare_url_trailing_period_stripped(self):
        nodes = _parse_inline_markup("See https://example.com.")
        link_node = next(n for n in nodes if n.get("marks"))
        assert link_node["text"] == "https://example.com"
        assert link_node["marks"] == [{"type": "link", "attrs": {"href": "https://example.com"}}]
        period_nodes = [n for n in nodes if n.get("text") == "."]
        assert period_nodes

    def test_bare_url_jql_parentheses_balanced(self):
        """JQL filter URLs with balanced parentheses must not be truncated."""
        url = "https://jira.example.com/issues/?jql=project+in+(FOO,BAR)"
        nodes = _parse_inline_markup(f"See {url}")
        link_node = next(n for n in nodes if n.get("marks"))
        assert link_node["text"] == url
        assert link_node["marks"] == [{"type": "link", "attrs": {"href": url}}]

    def test_bare_url_wiki_page_parentheses_balanced(self):
        """Wikipedia-style URLs with balanced parentheses must not be truncated."""
        url = "https://en.wikipedia.org/wiki/Foo_(bar)"
        nodes = _parse_inline_markup(f"See {url}")
        link_node = next(n for n in nodes if n.get("marks"))
        assert link_node["text"] == url
        assert link_node["marks"] == [{"type": "link", "attrs": {"href": url}}]

    def test_bare_url_multiple_parentheses_balanced(self):
        """URLs with multiple balanced parentheses groups must not be truncated."""
        url = "https://example.com/api(v1)/search?q=(a+b)+(c+d)"
        nodes = _parse_inline_markup(f"API: {url}")
        link_node = next(n for n in nodes if n.get("marks"))
        assert link_node["text"] == url
        assert link_node["marks"] == [{"type": "link", "attrs": {"href": url}}]

    def test_bare_url_with_query_params(self):
        nodes = _parse_inline_markup("https://example.com/search?q=test&page=1")
        assert nodes[0]["text"] == "https://example.com/search?q=test&page=1"
        assert nodes[0]["marks"] == [{"type": "link", "attrs": {"href": "https://example.com/search?q=test&page=1"}}]

    def test_mention(self):
        nodes = _parse_inline_markup("[~abc123]")
        assert nodes[0] == {"type": "mention", "attrs": {"id": "abc123"}}

    def test_image_as_link(self):
        nodes = _parse_inline_markup("!screenshot.png!")
        # Relative image URLs without / prefix are rejected by allowlist
        assert nodes[0]["text"] == "!screenshot.png!"
        assert "marks" not in nodes[0]

    def test_linebreak(self):
        nodes = _parse_inline_markup("before\\\\after")
        assert nodes[0] == {"type": "text", "text": "before"}
        assert nodes[1] == {"type": "hardBreak"}
        assert nodes[2] == {"type": "text", "text": "after"}

    def test_mixed_inline(self):
        nodes = _parse_inline_markup("Hello *bold* and _italic_")
        texts = [(n.get("text", ""), n.get("marks", [])) for n in nodes]
        assert texts[0] == ("Hello ", [])
        assert texts[1] == ("bold", [{"type": "strong"}])
        assert texts[2] == (" and ", [])
        assert texts[3] == ("italic", [{"type": "em"}])

    def test_empty_string(self):
        assert _parse_inline_markup("") == []

    def test_hyphen_mid_word_not_strike(self):
        """Hyphens inside words should not be treated as strikethrough."""
        nodes = _parse_inline_markup("foo-bar-baz")
        assert len(nodes) == 1
        assert nodes[0] == {"type": "text", "text": "foo-bar-baz"}

    def test_underscore_mid_word_not_italic(self):
        """Underscores inside identifiers should not be treated as italic."""
        nodes = _parse_inline_markup("test_configuration_docs")
        assert len(nodes) == 1
        assert nodes[0] == {"type": "text", "text": "test_configuration_docs"}

    def test_iso_date_not_strike(self):
        """ISO dates should not be treated as strikethrough."""
        nodes = _parse_inline_markup("2026-03-31")
        assert len(nodes) == 1
        assert nodes[0] == {"type": "text", "text": "2026-03-31"}

    def test_strike_with_word_boundaries(self):
        """Strikethrough with proper word boundaries should still work."""
        nodes = _parse_inline_markup("some -struck- here")
        assert nodes[1]["text"] == "struck"
        assert nodes[1]["marks"] == [{"type": "strike"}]

    def test_italic_with_word_boundaries(self):
        """Italic with proper word boundaries should still work."""
        nodes = _parse_inline_markup("hello _italic_ world")
        assert nodes[1]["text"] == "italic"
        assert nodes[1]["marks"] == [{"type": "em"}]

    def test_bold_mid_word_not_bold(self):
        """Asterisks inside words should not be treated as bold."""
        nodes = _parse_inline_markup("foo*bar*baz")
        assert len(nodes) == 1
        assert nodes[0] == {"type": "text", "text": "foo*bar*baz"}

    def test_strike_after_punctuation(self):
        """Strikethrough after punctuation (not word char) should work."""
        nodes = _parse_inline_markup("(-struck-)")
        assert nodes[1]["text"] == "struck"
        assert nodes[1]["marks"] == [{"type": "strike"}]

    def test_image_requires_dot(self):
        """!important! should not be treated as an image."""
        nodes = _parse_inline_markup("This is !important! news")
        assert len(nodes) == 1
        assert nodes[0]["text"] == "This is !important! news"

    def test_image_with_relative_path(self):
        """Relative image paths without / prefix are rejected by allowlist."""
        nodes = _parse_inline_markup("!images/photo.jpg!")
        assert nodes[0]["text"] == "!images/photo.jpg!"
        assert "marks" not in nodes[0]

    def test_image_with_absolute_path(self):
        """Image paths starting with / are allowed."""
        nodes = _parse_inline_markup("!/uploads/photo.jpg!")
        assert nodes[0]["text"] == "/uploads/photo.jpg"
        assert nodes[0]["marks"][0]["type"] == "link"

    def test_image_with_url(self):
        """Image URLs with https:// are allowed."""
        nodes = _parse_inline_markup("!https://example.com/photo.jpg!")
        assert nodes[0]["text"] == "https://example.com/photo.jpg"
        assert nodes[0]["marks"][0]["type"] == "link"

    def test_link_unsafe_scheme_javascript(self):
        """javascript: URLs should be emitted as plain text."""
        nodes = _parse_inline_markup("[click|javascript:alert(1)]")
        assert len(nodes) == 1
        assert nodes[0] == {"type": "text", "text": "[click|javascript:alert(1)]"}

    def test_link_unsafe_scheme_data(self):
        """data: URLs should be emitted as plain text."""
        nodes = _parse_inline_markup("[click|data:text/html,<script>]")
        assert len(nodes) == 1
        assert "marks" not in nodes[0]

    def test_link_safe_scheme_https(self):
        nodes = _parse_inline_markup("[Example|https://example.com]")
        assert nodes[0]["marks"] == [{"type": "link", "attrs": {"href": "https://example.com"}}]

    def test_link_safe_scheme_mailto(self):
        nodes = _parse_inline_markup("[Email|mailto:user@example.com]")
        assert nodes[0]["marks"] == [{"type": "link", "attrs": {"href": "mailto:user@example.com"}}]

    def test_link_safe_scheme_relative(self):
        nodes = _parse_inline_markup("[Page|/wiki/page]")
        assert nodes[0]["marks"] == [{"type": "link", "attrs": {"href": "/wiki/page"}}]

    def test_image_unsafe_scheme(self):
        """javascript: image URL should be emitted as plain text."""
        nodes = _parse_inline_markup("!javascript:alert.x!")
        assert len(nodes) == 1
        assert "marks" not in nodes[0]

    def test_link_unsafe_scheme_vbscript(self):
        """vbscript: URLs should be emitted as plain text."""
        nodes = _parse_inline_markup("[click|vbscript:MsgBox]")
        assert len(nodes) == 1
        assert "marks" not in nodes[0]

    def test_link_unsafe_scheme_file(self):
        """file: URLs should be rejected by allowlist."""
        nodes = _parse_inline_markup("[click|file:///etc/passwd]")
        assert len(nodes) == 1
        assert "marks" not in nodes[0]

    def test_link_unsafe_scheme_blob(self):
        """blob: URLs should be rejected by allowlist."""
        nodes = _parse_inline_markup("[click|blob:http://example.com/uuid]")
        assert len(nodes) == 1
        assert "marks" not in nodes[0]

    def test_link_whitespace_injected_scheme(self):
        """Whitespace-injected schemes should be rejected."""
        nodes = _parse_inline_markup("[click|java\tscript:alert(1)]")
        assert len(nodes) == 1
        assert "marks" not in nodes[0]

    def test_link_anchor(self):
        """Anchor links (#heading) should be allowed."""
        nodes = _parse_inline_markup("[Section|#heading]")
        assert nodes[0]["marks"] == [{"type": "link", "attrs": {"href": "#heading"}}]

    def test_link_empty_url(self):
        """Empty URL should be handled gracefully."""
        nodes = _parse_inline_markup("[click|]")
        assert len(nodes) >= 1
        # Empty URL doesn't match allowlist, so emitted as plain text
        for n in nodes:
            assert "marks" not in n or n["marks"][0]["type"] != "link"

    def test_link_scheme_only(self):
        """URL with only scheme should be allowed."""
        nodes = _parse_inline_markup("[click|https://]")
        assert nodes[0]["marks"] == [{"type": "link", "attrs": {"href": "https://"}}]

    def test_bare_link_routes_through_safe_url(self):
        """Bare links should also route through _is_safe_url."""
        nodes = _parse_inline_markup("[https://example.com]")
        assert nodes[0]["marks"] == [{"type": "link", "attrs": {"href": "https://example.com"}}]


# --- URL safety function ---


class TestIsSafeUrl:
    def test_https(self):
        assert _is_safe_url("https://example.com") is True

    def test_http(self):
        assert _is_safe_url("http://example.com") is True

    def test_mailto(self):
        assert _is_safe_url("mailto:user@example.com") is True

    def test_relative(self):
        assert _is_safe_url("/wiki/page") is True

    def test_anchor(self):
        assert _is_safe_url("#section") is True

    def test_javascript(self):
        assert _is_safe_url("javascript:alert(1)") is False

    def test_data(self):
        assert _is_safe_url("data:text/html,<script>") is False

    def test_vbscript(self):
        assert _is_safe_url("vbscript:MsgBox") is False

    def test_file(self):
        assert _is_safe_url("file:///etc/passwd") is False

    def test_blob(self):
        assert _is_safe_url("blob:http://example.com") is False

    def test_bare_path(self):
        assert _is_safe_url("page.html") is False

    def test_empty(self):
        assert _is_safe_url("") is False

    def test_protocol_relative(self):
        assert _is_safe_url("//evil.com/payload") is False

    def test_https_case_insensitive(self):
        assert _is_safe_url("HTTPS://example.com") is True

    def test_mailto_case_insensitive(self):
        assert _is_safe_url("MAILTO:user@example.com") is True

    def test_javascript_case_insensitive(self):
        assert _is_safe_url("JavaScript:alert(1)") is False

    def test_localhost(self):
        assert _is_safe_url("http://localhost:8080/api") is True

    def test_ip_address(self):
        assert _is_safe_url("https://192.168.1.1/api") is True

    def test_ip_address_with_port(self):
        assert _is_safe_url("http://127.0.0.1:3000") is True

    def test_localhost_with_path(self):
        assert _is_safe_url("http://localhost/api/v1") is True


# --- Input length cap ---


class TestWikiParserLengthCap:
    def test_oversized_input_wiki_to_adf(self):
        """wiki_to_adf falls back to single paragraph for oversized input."""
        large = "h1. Title\n" * (_MAX_WIKI_PARSE_LEN // 10 + 1)
        result = wiki_to_adf(large)
        assert result["version"] == 1
        assert result["type"] == "doc"
        assert len(result["content"]) == 1
        assert result["content"][0]["type"] == "paragraph"
        assert result["content"][0]["content"][0]["text"] == large

    def test_oversized_input_text_to_adf(self):
        """text_to_adf skips wiki parsing for oversized input."""
        large = "h1. Title\n" * (_MAX_WIKI_PARSE_LEN // 10 + 1)
        result = text_to_adf(large)
        assert result["type"] == "doc"
        # Falls through to plain-text splitter, not wiki parser
        for block in result["content"]:
            assert block["type"] == "paragraph"

    def test_normal_input_still_parsed(self):
        """Input under the cap is still parsed as wiki markup."""
        result = text_to_adf("h1. Title\n\nSome text")
        assert result["content"][0]["type"] == "heading"


# --- Recursion depth cap ---


class TestWikiParserDepthCap:
    def test_deeply_nested_quotes(self):
        """Deeply nested {quote} blocks hit depth cap gracefully."""
        # Build nesting deeper than _MAX_WIKI_PARSE_DEPTH
        depth = _MAX_WIKI_PARSE_DEPTH + 5
        text = "{quote}\n" * depth + "inner text" + "\n{quote}" * depth
        result = wiki_to_adf(text)
        assert result["version"] == 1
        assert result["type"] == "doc"
        # Should not raise RecursionError — verify content is present
        adf_text = str(result)
        assert "inner text" in adf_text

    def test_deeply_nested_unclosed_quotes(self):
        """Deeply nested unclosed {quote} blocks are handled."""
        depth = _MAX_WIKI_PARSE_DEPTH + 5
        text = "{quote}\n" * depth + "inner text"
        result = wiki_to_adf(text)
        assert result["version"] == 1
        adf_text = str(result)
        assert "inner text" in adf_text

    def test_deeply_nested_panels(self):
        """Deeply nested {panel} blocks hit depth cap gracefully."""
        depth = _MAX_WIKI_PARSE_DEPTH + 5
        text = "{panel}\n" * depth + "panel text" + "\n{panel}" * depth
        result = wiki_to_adf(text)
        assert result["version"] == 1
        adf_text = str(result)
        assert "panel text" in adf_text

    def test_deeply_nested_lists(self):
        """Deeply nested lists are capped and content preserved."""
        # Build a list with depth > _MAX_LIST_DEPTH
        lines = []
        for i in range(25):
            lines.append("*" * (i + 1) + f" item {i}")
        text = "\n".join(lines)
        result = wiki_to_adf(text)
        assert result["version"] == 1
        # All items should be present in the output
        adf_text = str(result)
        for i in range(25):
            assert f"item {i}" in adf_text


# --- Wiki to ADF block parsing ---


class TestWikiToAdf:
    # Headings
    def test_heading_h1(self):
        result = wiki_to_adf("h1. Title")
        block = result["content"][0]
        assert block["type"] == "heading"
        assert block["attrs"]["level"] == 1
        assert block["content"][0]["text"] == "Title"

    def test_heading_h6(self):
        result = wiki_to_adf("h6. Deep")
        assert result["content"][0]["attrs"]["level"] == 6

    def test_heading_with_inline(self):
        result = wiki_to_adf("h2. *Bold* title")
        content = result["content"][0]["content"]
        assert content[0]["marks"] == [{"type": "strong"}]
        assert content[0]["text"] == "Bold"
        assert content[1]["text"] == " title"

    # Lists
    def test_unordered_list(self):
        result = wiki_to_adf("* item1\n* item2")
        block = result["content"][0]
        assert block["type"] == "bulletList"
        assert len(block["content"]) == 2
        assert block["content"][0]["type"] == "listItem"

    def test_ordered_list(self):
        result = wiki_to_adf("# first\n# second")
        block = result["content"][0]
        assert block["type"] == "orderedList"
        assert len(block["content"]) == 2

    def test_nested_unordered_list(self):
        result = wiki_to_adf("* top\n** nested\n* bottom")
        block = result["content"][0]
        assert block["type"] == "bulletList"
        # First item should have nested list
        first_item = block["content"][0]
        assert len(first_item["content"]) == 2  # paragraph + nested list
        assert first_item["content"][1]["type"] == "bulletList"

    def test_nested_ordered_list(self):
        result = wiki_to_adf("# top\n## nested")
        block = result["content"][0]
        first_item = block["content"][0]
        assert first_item["content"][1]["type"] == "orderedList"

    # Code blocks
    def test_code_block_no_lang(self):
        result = wiki_to_adf("{code}\nprint('hi')\n{code}")
        block = result["content"][0]
        assert block["type"] == "codeBlock"
        assert block["content"][0]["text"] == "print('hi')"
        assert "attrs" not in block

    def test_code_block_with_lang(self):
        result = wiki_to_adf("{code:python}\nx = 1\n{code}")
        block = result["content"][0]
        assert block["type"] == "codeBlock"
        assert block["attrs"]["language"] == "python"
        assert block["content"][0]["text"] == "x = 1"

    def test_code_block_multiline(self):
        result = wiki_to_adf("{code}\nline1\nline2\nline3\n{code}")
        assert result["content"][0]["content"][0]["text"] == "line1\nline2\nline3"

    def test_code_block_unclosed(self):
        result = wiki_to_adf("{code}\nsome code")
        block = result["content"][0]
        assert block["type"] == "codeBlock"
        assert block["content"][0]["text"] == "some code"

    # Blockquotes
    def test_bq_shorthand(self):
        result = wiki_to_adf("bq. Some quote")
        block = result["content"][0]
        assert block["type"] == "blockquote"
        assert block["content"][0]["type"] == "paragraph"

    def test_quote_block(self):
        result = wiki_to_adf("{quote}\nquoted text\n{quote}")
        block = result["content"][0]
        assert block["type"] == "blockquote"
        assert block["content"][0]["content"][0]["text"] == "quoted text"

    def test_quote_block_unclosed(self):
        result = wiki_to_adf("{quote}\nunclosed quote")
        assert result["content"][0]["type"] == "blockquote"

    # Horizontal rule
    def test_horizontal_rule(self):
        result = wiki_to_adf("----")
        assert result["content"][0]["type"] == "rule"

    # Tables
    def test_simple_table(self):
        result = wiki_to_adf("||H1||H2||\n|c1|c2|")
        table = result["content"][0]
        assert table["type"] == "table"
        assert len(table["content"]) == 2
        # Header row
        header_row = table["content"][0]
        assert header_row["content"][0]["type"] == "tableHeader"
        # Data row
        data_row = table["content"][1]
        assert data_row["content"][0]["type"] == "tableCell"

    def test_table_header_content(self):
        result = wiki_to_adf("||Name||Age||")
        row = result["content"][0]["content"][0]
        cells = row["content"]
        assert cells[0]["content"][0]["content"][0]["text"] == "Name"
        assert cells[1]["content"][0]["content"][0]["text"] == "Age"

    def test_table_with_inline_markup(self):
        result = wiki_to_adf("||*Bold Header*||Plain||")
        cell = result["content"][0]["content"][0]["content"][0]
        para_content = cell["content"][0]["content"]
        assert para_content[0]["marks"] == [{"type": "strong"}]

    # Panel
    def test_panel(self):
        result = wiki_to_adf("{panel}\ncontent here\n{panel}")
        block = result["content"][0]
        assert block["type"] == "panel"
        assert block["attrs"]["panelType"] == "info"
        assert block["content"][0]["content"][0]["text"] == "content here"

    def test_panel_unclosed(self):
        result = wiki_to_adf("{panel}\nunclosed panel")
        assert result["content"][0]["type"] == "panel"

    def test_panel_with_parameters(self):
        """Panel with parameters should not error."""
        result = wiki_to_adf("{panel:title=Warning}\ncontent\n{panel}")
        block = result["content"][0]
        assert block["type"] == "panel"
        assert block["attrs"]["panelType"] == "info"
        assert block["content"][0]["content"][0]["text"] == "content"

    def test_code_block_no_inline_parsing(self):
        """Inline markup inside {code} blocks should NOT be parsed."""
        result = wiki_to_adf("{code}\n*bold* and _italic_\n{code}")
        block = result["content"][0]
        assert block["type"] == "codeBlock"
        assert block["content"][0]["text"] == "*bold* and _italic_"

    def test_mixed_list_types(self):
        """Different list types should produce separate list blocks."""
        result = wiki_to_adf("* bullet\n# ordered")
        assert len(result["content"]) == 2
        assert result["content"][0]["type"] == "bulletList"
        assert result["content"][1]["type"] == "orderedList"

    # Mixed content
    def test_heading_then_paragraph(self):
        result = wiki_to_adf("h2. Title\n\nSome text")
        assert result["content"][0]["type"] == "heading"
        assert result["content"][1]["type"] == "paragraph"
        assert result["content"][1]["content"][0]["text"] == "Some text"

    def test_complex_document(self):
        doc = """h1. Project Overview

This is the *introduction*.

h2. Tasks

* Task one
* Task two
** Sub-task

h2. Code

{code:python}
def hello():
    print("world")
{code}

----

bq. Important note here"""
        result = wiki_to_adf(doc)
        types = [b["type"] for b in result["content"]]
        assert "heading" in types
        assert "paragraph" in types
        assert "bulletList" in types
        assert "codeBlock" in types
        assert "rule" in types
        assert "blockquote" in types

    def test_empty_input(self):
        result = wiki_to_adf("")
        assert result["content"] == [{"type": "paragraph", "content": []}]

    def test_version_and_type(self):
        result = wiki_to_adf("h1. Test")
        assert result["version"] == 1
        assert result["type"] == "doc"


# --- Integration: text_to_adf with wiki markup ---


class TestTextToAdfWikiIntegration:
    def test_wiki_heading_converted(self):
        result = text_to_adf("h2. Title\n\nSome paragraph")
        assert result["type"] == "doc"
        assert result["content"][0]["type"] == "heading"

    def test_wiki_list_converted(self):
        result = text_to_adf("* item 1\n* item 2")
        assert result["content"][0]["type"] == "bulletList"

    def test_plain_text_still_works(self):
        result = text_to_adf("Just plain text here")
        assert result["content"][0]["type"] == "paragraph"
        assert result["content"][0]["content"][0]["text"] == "Just plain text here"

    def test_adf_passthrough_still_works(self):
        adf = {"version": 1, "type": "doc", "content": []}
        assert text_to_adf(adf) is adf

    def test_json_adf_passthrough_still_works(self):
        adf = {"version": 1, "type": "doc", "content": []}
        assert text_to_adf(json.dumps(adf)) == adf

    def test_wiki_code_block_converted(self):
        result = text_to_adf("{code:java}\nSystem.out.println();\n{code}")
        assert result["content"][0]["type"] == "codeBlock"
        assert result["content"][0]["attrs"]["language"] == "java"

    def test_wiki_table_converted(self):
        result = text_to_adf("||Col1||Col2||\n|a|b|")
        assert result["content"][0]["type"] == "table"

    def test_wiki_list_with_underscore_identifier(self):
        """Underscores in identifiers should not become italic inside wiki lists."""
        result = text_to_adf("* Update test_configuration_docs\n* Add tests")
        item = result["content"][0]["content"][0]["content"][0]
        assert len(item["content"]) == 1
        assert item["content"][0]["text"] == "Update test_configuration_docs"
        assert "marks" not in item["content"][0]

    def test_wiki_list_with_iso_date(self):
        """ISO dates should not become strikethrough inside wiki lists."""
        result = text_to_adf("* Deadline: 2026-03-31\n* Status: Done")
        item = result["content"][0]["content"][0]["content"][0]
        assert len(item["content"]) == 1
        assert item["content"][0]["text"] == "Deadline: 2026-03-31"
        assert "marks" not in item["content"][0]

    def test_wiki_list_with_hyphenated_words(self):
        """Hyphenated words should not become strikethrough inside wiki lists."""
        result = text_to_adf("* Use foo-bar-baz option\n* Done")
        item = result["content"][0]["content"][0]["content"][0]
        assert len(item["content"]) == 1
        assert item["content"][0]["text"] == "Use foo-bar-baz option"
        assert "marks" not in item["content"][0]
