"""Jira plugin helper functions.

Pure utility functions with no protocol or I/O dependencies.
"""

import json
import math
import re

_ISSUE_KEY_RE = re.compile(r"^[A-Z][A-Z0-9_]+-\d+$")
_PROJECT_KEY_RE = re.compile(r"^[A-Z][A-Z0-9_]+$")

_USER_ALIASES = frozenset({"me", "myself", "currentuser"})

# --- Wiki markup regex patterns (compiled at module level) ---

_WIKI_HEADING_RE = re.compile(r"^h([1-6])\.\s+(.*)")
_WIKI_LIST_UL_RE = re.compile(r"^(\*+)\s+(.*)")
_WIKI_LIST_OL_RE = re.compile(r"^(#+)\s+(.*)")
# {code} and {quote} use identical start/end delimiters in Jira wiki
# syntax. The state machine disambiguates: in NORMAL state a match
# opens the block; in CODE/QUOTE state it closes it.
_WIKI_CODE_START_RE = re.compile(r"^\{code(?::(\w+))?\}\s*$")
_WIKI_CODE_END_RE = re.compile(r"^\{code\}\s*$")
_WIKI_QUOTE_START_RE = re.compile(r"^\{quote\}\s*$")
_WIKI_QUOTE_END_RE = re.compile(r"^\{quote\}\s*$")
_WIKI_BQ_RE = re.compile(r"^bq\.\s+(.*)")
_WIKI_RULE_RE = re.compile(r"^----\s*$")
_WIKI_TABLE_HEADER_RE = re.compile(r"^\|\|")
_WIKI_TABLE_ROW_RE = re.compile(r"^\|[^|]")
# Panel parameters (title, borderStyle, bgColor) are intentionally
# ignored — no clean mapping to ADF panel types (info, note, etc.).
_WIKI_PANEL_START_RE = re.compile(r"^\{panel(?::[^}]*)?\}\s*$")
_WIKI_PANEL_END_RE = re.compile(r"^\{panel\}\s*$")

_WIKI_BLOCK_DETECT_RE = re.compile(
    r"(?m)"
    r"(?:^h[1-6]\.\s)"
    r"|(?:^\*+\s)"
    r"|(?:^#+\s)"
    r"|(?:^\{code(?::|\}))"
    r"|(?:^\{quote\})"
    r"|(?:^\{panel(?::|\}))"
    r"|(?:^bq\.\s)"
    r"|(?:^----\s*$)"
    r"|(?:^\|\|)"
)

_WIKI_INLINE_RE = re.compile(
    r"(?P<monospace>\{\{(.+?)\}\})"
    r"|(?P<link>\[(?P<link_text>[^]|~]*)\|(?P<link_url>[^]]+)\])"
    r"|(?P<bare_link>\[(?P<bare_url>https?://[^]]+)\])"
    r"|(?P<mention>\[~(?P<mention_id>[^]]+)\])"
    r"|(?P<image>!(?P<image_url>\S+?\.\S+?)!)"
    r"|(?P<bold>(?<!\w)\*(?=\S)(?P<bold_text>.+?)(?<=\S)\*(?!\w))"
    r"|(?P<italic>(?<!\w)_(?=\S)(?P<italic_text>.+?)(?<=\S)_(?!\w))"
    r"|(?P<strike>(?<!\w)-(?=\S)(?P<strike_text>.+?)(?<=\S)-(?!\w))"
    r"|(?P<underline>(?<!\w)\+(?=\S)(?P<underline_text>.+?)(?<=\S)\+(?!\w))"
    r"|(?P<sup>(?<!\w)\^(?=\S)(?P<sup_text>.+?)(?<=\S)\^(?!\w))"
    r"|(?P<sub>(?<!\w)~(?=\S)(?P<sub_text>.+?)(?<=\S)~(?!\w))"
    r"|(?P<linebreak>\\\\)"
)


def http_error(status, body):
    """Build a compact error dict from an HTTP error response.

    Prevents raw HTML error pages (auth redirects, 500 pages) from
    flooding the LLM context. Extracts the message from JSON errors
    or truncates non-JSON responses.
    """
    result = {"error": f"HTTP {status}"}

    if isinstance(body, dict):
        # Jira JSON error: {"errorMessages": [...], "errors": {...}}
        msgs = body.get("errorMessages") or body.get("errors")
        if msgs:
            result["detail"] = msgs
        else:
            msg = body.get("message") or body.get("error")
            if msg:
                result["detail"] = msg
    elif isinstance(body, str):
        # HTML or plain text — truncate to prevent token waste
        if len(body) > 200:
            result["detail"] = body[:200] + "... (truncated)"
        elif body:
            result["detail"] = body
    return result


def validate_issue_key(key):
    """Validate and return a cleaned issue key.

    Raises ValueError if key doesn't match PROJECT-123 format.
    """
    cleaned = key.strip().upper()
    if not _ISSUE_KEY_RE.match(cleaned):
        raise ValueError(f"Invalid issue key: '{key}' (expected format: PROJECT-123)")
    return cleaned


def validate_project_key(key):
    """Validate and return a cleaned project key.

    Raises ValueError if key doesn't match Jira's project key format.
    """
    cleaned = key.strip().upper()
    if not _PROJECT_KEY_RE.match(cleaned):
        raise ValueError(f"Invalid project key: '{key}' (expected format: PROJ)")
    return cleaned


def is_user_alias(username):
    """Check if username is a self-referencing alias (me, myself, currentUser)."""
    return username.lower().strip() in _USER_ALIASES


def extract_brief_issue(issue):
    """Extract compact summary from a Jira issue response.

    Returns dict with key, summary, status, assignee, priority.
    """
    fields = issue.get("fields", {})
    status = fields.get("status")
    assignee = fields.get("assignee")
    priority = fields.get("priority")
    return {
        "key": issue.get("key", ""),
        "summary": fields.get("summary", ""),
        "status": status.get("name", "") if isinstance(status, dict) else "",
        "assignee": assignee.get("displayName", "") if isinstance(assignee, dict) else "",
        "priority": priority.get("name", "") if isinstance(priority, dict) else "",
    }


def extract_issue_fields(issue, fields_str):
    """Extract clean field values from a Jira issue response.

    Returns a dict with 'key' plus the extracted value for each field
    in the comma-separated fields_str. Dict-valued fields (status,
    priority, assignee, etc.) are flattened to their .displayName,
    .name, or .value. ADF documents (description on Cloud) are
    converted to plain text. Array fields (labels, components) pass
    through as-is.

    Args:
        issue: Raw Jira issue dict from the API
        fields_str: Comma-separated field names (e.g., "summary,status")

    Returns:
        Flat dict: {"key": "PROJ-1", "summary": "...", "status": "..."}
    """
    result = {"key": issue.get("key", "")}
    if not fields_str:
        return result

    fields_data = issue.get("fields", {})
    for field_name in fields_str.split(","):
        field_name = field_name.strip()
        if not field_name:
            continue

        if "." in field_name:
            # Dotted path — delegate to extract_nested_field.
            result[field_name] = extract_nested_field(fields_data, field_name)
            continue

        value = fields_data.get(field_name)
        if isinstance(value, dict):
            if value.get("type") == "doc":
                # ADF document (Cloud description/comment) → plain text.
                value = adf_to_text(value)
            elif "displayName" in value:
                # User fields (assignee, reporter, creator).
                value = value["displayName"]
            elif "name" in value:
                # Named objects (status, priority, issuetype).
                value = value["name"]
            elif "value" in value:
                # Custom select fields.
                value = value["value"]
        result[field_name] = value
    return result


_SAFE_URL_RE = re.compile(r"^(?:https?://|mailto:|/(?!/)|#)", re.IGNORECASE)

_MAX_WIKI_PARSE_LEN = 100_000
_MAX_WIKI_PARSE_DEPTH = 20
_MAX_LIST_DEPTH = 20


def _is_safe_url(url):
    """Check that a URL uses a known-safe scheme."""
    return bool(_SAFE_URL_RE.match(url))


def _looks_like_wiki_markup(text):
    """Check if text contains Jira wiki markup patterns.

    Uses block-level patterns only to avoid false positives from
    inline characters like * or _ in plain text.
    """
    return bool(_WIKI_BLOCK_DETECT_RE.search(text))


def _parse_inline_markup(text):
    """Parse inline wiki markup into ADF inline nodes.

    Returns a list of ADF inline nodes (text with marks, mentions,
    hard breaks, etc.).
    """
    if not text:
        return []

    nodes = []
    last_end = 0

    for m in _WIKI_INLINE_RE.finditer(text):
        # Emit plain text before this match
        if m.start() > last_end:
            nodes.append({"type": "text", "text": text[last_end : m.start()]})

        if m.group("monospace"):
            nodes.append({"type": "text", "text": m.group(2), "marks": [{"type": "code"}]})
        elif m.group("link"):
            link_text = m.group("link_text")
            link_url = m.group("link_url")
            if _is_safe_url(link_url):
                nodes.append(
                    {"type": "text", "text": link_text, "marks": [{"type": "link", "attrs": {"href": link_url}}]}
                )
            else:
                nodes.append({"type": "text", "text": m.group("link")})
        elif m.group("bare_link"):
            url = m.group("bare_url")
            if _is_safe_url(url):
                nodes.append({"type": "text", "text": url, "marks": [{"type": "link", "attrs": {"href": url}}]})
            else:
                nodes.append({"type": "text", "text": m.group("bare_link")})
        elif m.group("mention"):
            nodes.append({"type": "mention", "attrs": {"id": m.group("mention_id")}})
        elif m.group("image"):
            url = m.group("image_url")
            if _is_safe_url(url):
                nodes.append({"type": "text", "text": url, "marks": [{"type": "link", "attrs": {"href": url}}]})
            else:
                nodes.append({"type": "text", "text": m.group("image")})
        elif m.group("bold"):
            nodes.append({"type": "text", "text": m.group("bold_text"), "marks": [{"type": "strong"}]})
        elif m.group("italic"):
            nodes.append({"type": "text", "text": m.group("italic_text"), "marks": [{"type": "em"}]})
        elif m.group("strike"):
            nodes.append({"type": "text", "text": m.group("strike_text"), "marks": [{"type": "strike"}]})
        elif m.group("underline"):
            nodes.append({"type": "text", "text": m.group("underline_text"), "marks": [{"type": "underline"}]})
        elif m.group("sup"):
            nodes.append(
                {"type": "text", "text": m.group("sup_text"), "marks": [{"type": "subsup", "attrs": {"type": "sup"}}]}
            )
        elif m.group("sub"):
            nodes.append(
                {"type": "text", "text": m.group("sub_text"), "marks": [{"type": "subsup", "attrs": {"type": "sub"}}]}
            )
        elif m.group("linebreak"):
            nodes.append({"type": "hardBreak"})

        last_end = m.end()

    # Emit trailing plain text
    if last_end < len(text):
        nodes.append({"type": "text", "text": text[last_end:]})

    return nodes


def _build_list_tree(items, list_type):
    """Build a nested ADF list from (depth, text) tuples.

    list_type is "bulletList" or "orderedList".
    """

    def _build(items, current_depth):
        if current_depth > _MAX_LIST_DEPTH:
            flat_items = []
            for _depth, text in items:
                flat_items.append(
                    {"type": "listItem", "content": [{"type": "paragraph", "content": _parse_inline_markup(text)}]}
                )
            return {"type": list_type, "content": flat_items}

        list_items = []
        i = 0
        while i < len(items):
            depth, text = items[i]
            if depth == current_depth:
                content = [{"type": "paragraph", "content": _parse_inline_markup(text)}]
                i += 1
                # Collect deeper items as children
                children = []
                while i < len(items) and items[i][0] > current_depth:
                    children.append(items[i])
                    i += 1
                if children:
                    content.append(_build(children, current_depth + 1))
                list_items.append({"type": "listItem", "content": content})
            elif depth > current_depth:
                # Orphaned deeper items — wrap in a list item
                children = []
                while i < len(items) and items[i][0] > current_depth:
                    children.append(items[i])
                    i += 1
                if children:
                    nested = _build(children, children[0][0])
                    list_items.append({"type": "listItem", "content": [nested]})
            else:
                break
        return {"type": list_type, "content": list_items}

    return _build(items, items[0][0] if items else 1)


def _parse_table_rows(lines):
    """Parse consecutive table lines into an ADF table node."""
    rows = []
    for line in lines:
        is_header = line.startswith("||")
        if is_header:
            # Split on || but skip empty first/last from leading/trailing ||
            raw = line.strip()
            if raw.startswith("||"):
                raw = raw[2:]
            if raw.endswith("||"):
                raw = raw[:-2]
            cells = [c.strip() for c in raw.split("||")]
            cell_type = "tableHeader"
        else:
            raw = line.strip()
            if raw.startswith("|"):
                raw = raw[1:]
            if raw.endswith("|"):
                raw = raw[:-1]
            cells = [c.strip() for c in raw.split("|")]
            cell_type = "tableCell"

        row_content = []
        for cell_text in cells:
            row_content.append(
                {"type": cell_type, "content": [{"type": "paragraph", "content": _parse_inline_markup(cell_text)}]}
            )
        rows.append({"type": "tableRow", "content": row_content})

    return {"type": "table", "content": rows}


def _parse_wiki_blocks(text, _depth=0):
    """Parse wiki markup text into a list of ADF block nodes.

    Uses a line-by-line state machine for block-level constructs,
    then applies inline parsing to text content.
    """
    lines = text.split("\n")
    blocks = []
    para_buffer = []
    state = "NORMAL"  # NORMAL, CODE, QUOTE, PANEL
    block_buffer = []
    code_lang = None

    def _flush_para():
        if para_buffer:
            combined = "\n".join(para_buffer).strip()
            if combined:
                blocks.append({"type": "paragraph", "content": _parse_inline_markup(combined)})
            para_buffer.clear()

    i = 0
    while i < len(lines):
        line = lines[i]

        if state == "CODE":
            if _WIKI_CODE_END_RE.match(line):
                node: dict = {"type": "codeBlock", "content": [{"type": "text", "text": "\n".join(block_buffer)}]}
                if code_lang:
                    node["attrs"] = {"language": code_lang}
                blocks.append(node)
                block_buffer.clear()
                state = "NORMAL"
            else:
                block_buffer.append(line)
            i += 1
            continue

        if state == "QUOTE":
            if _WIKI_QUOTE_END_RE.match(line):
                inner = "\n".join(block_buffer)
                if inner.strip() and _depth < _MAX_WIKI_PARSE_DEPTH:
                    inner_blocks = _parse_wiki_blocks(inner, _depth + 1)
                elif inner.strip():
                    inner_blocks = [{"type": "paragraph", "content": _parse_inline_markup(inner)}]
                else:
                    inner_blocks = []
                if not inner_blocks:
                    inner_blocks = [{"type": "paragraph", "content": []}]
                blocks.append({"type": "blockquote", "content": inner_blocks})
                block_buffer.clear()
                state = "NORMAL"
            else:
                block_buffer.append(line)
            i += 1
            continue

        if state == "PANEL":
            if _WIKI_PANEL_END_RE.match(line):
                inner = "\n".join(block_buffer)
                if inner.strip() and _depth < _MAX_WIKI_PARSE_DEPTH:
                    inner_blocks = _parse_wiki_blocks(inner, _depth + 1)
                elif inner.strip():
                    inner_blocks = [{"type": "paragraph", "content": _parse_inline_markup(inner)}]
                else:
                    inner_blocks = []
                if not inner_blocks:
                    inner_blocks = [{"type": "paragraph", "content": []}]
                node = {"type": "panel", "attrs": {"panelType": "info"}, "content": inner_blocks}
                blocks.append(node)
                block_buffer.clear()
                state = "NORMAL"
            else:
                block_buffer.append(line)
            i += 1
            continue

        # --- NORMAL state ---

        # Code block start
        m = _WIKI_CODE_START_RE.match(line)
        if m:
            _flush_para()
            code_lang = m.group(1)
            state = "CODE"
            i += 1
            continue

        # Quote block start
        if _WIKI_QUOTE_START_RE.match(line):
            _flush_para()
            state = "QUOTE"
            i += 1
            continue

        # Panel start
        m = _WIKI_PANEL_START_RE.match(line)
        if m:
            _flush_para()
            state = "PANEL"
            i += 1
            continue

        # Heading
        m = _WIKI_HEADING_RE.match(line)
        if m:
            _flush_para()
            level = int(m.group(1))
            heading_text = m.group(2).strip()
            blocks.append({"type": "heading", "attrs": {"level": level}, "content": _parse_inline_markup(heading_text)})
            i += 1
            continue

        # Horizontal rule
        if _WIKI_RULE_RE.match(line):
            _flush_para()
            blocks.append({"type": "rule"})
            i += 1
            continue

        # Blockquote shorthand
        m = _WIKI_BQ_RE.match(line)
        if m:
            _flush_para()
            blocks.append(
                {
                    "type": "blockquote",
                    "content": [{"type": "paragraph", "content": _parse_inline_markup(m.group(1))}],
                }
            )
            i += 1
            continue

        # Unordered list
        m = _WIKI_LIST_UL_RE.match(line)
        if m:
            _flush_para()
            items = []
            while i < len(lines):
                lm = _WIKI_LIST_UL_RE.match(lines[i])
                if not lm:
                    break
                items.append((len(lm.group(1)), lm.group(2)))
                i += 1
            blocks.append(_build_list_tree(items, "bulletList"))
            continue

        # Ordered list
        m = _WIKI_LIST_OL_RE.match(line)
        if m:
            _flush_para()
            items = []
            while i < len(lines):
                lm = _WIKI_LIST_OL_RE.match(lines[i])
                if not lm:
                    break
                items.append((len(lm.group(1)), lm.group(2)))
                i += 1
            blocks.append(_build_list_tree(items, "orderedList"))
            continue

        # Table
        if _WIKI_TABLE_HEADER_RE.match(line) or _WIKI_TABLE_ROW_RE.match(line):
            _flush_para()
            table_lines = []
            while i < len(lines) and (_WIKI_TABLE_HEADER_RE.match(lines[i]) or _WIKI_TABLE_ROW_RE.match(lines[i])):
                table_lines.append(lines[i])
                i += 1
            blocks.append(_parse_table_rows(table_lines))
            continue

        # Regular text — accumulate into paragraph
        if line.strip():
            para_buffer.append(line)
        else:
            _flush_para()
        i += 1

    # Handle unclosed blocks
    if state == "CODE":
        node: dict = {"type": "codeBlock", "content": [{"type": "text", "text": "\n".join(block_buffer)}]}
        if code_lang:
            node["attrs"] = {"language": code_lang}
        blocks.append(node)
    elif state == "QUOTE":
        inner = "\n".join(block_buffer)
        if inner.strip() and _depth < _MAX_WIKI_PARSE_DEPTH:
            inner_blocks = _parse_wiki_blocks(inner, _depth + 1)
        elif inner.strip():
            inner_blocks = [{"type": "paragraph", "content": _parse_inline_markup(inner)}]
        else:
            inner_blocks = [{"type": "paragraph", "content": []}]
        blocks.append({"type": "blockquote", "content": inner_blocks})
    elif state == "PANEL":
        inner = "\n".join(block_buffer)
        if inner.strip() and _depth < _MAX_WIKI_PARSE_DEPTH:
            inner_blocks = _parse_wiki_blocks(inner, _depth + 1)
        elif inner.strip():
            inner_blocks = [{"type": "paragraph", "content": _parse_inline_markup(inner)}]
        else:
            inner_blocks = [{"type": "paragraph", "content": []}]
        blocks.append({"type": "panel", "attrs": {"panelType": "info"}, "content": inner_blocks})

    _flush_para()
    return blocks


def wiki_to_adf(text):
    """Convert Jira wiki markup text to an ADF document."""
    if len(text) > _MAX_WIKI_PARSE_LEN:
        return {
            "version": 1,
            "type": "doc",
            "content": [{"type": "paragraph", "content": [{"type": "text", "text": text}]}],
        }
    blocks = _parse_wiki_blocks(text)
    if not blocks:
        blocks = [{"type": "paragraph", "content": []}]
    return {"version": 1, "type": "doc", "content": blocks}


def text_to_adf(text: str | dict | None) -> dict:
    """Convert plain text to Atlassian Document Format (ADF).

    Jira Cloud API v3 requires comment/description bodies in ADF.
    Accepts plain text (split into paragraphs), pre-built ADF dicts
    (passed through after validation), or JSON-encoded ADF strings
    (parsed and validated).

    Raises ValueError if a dict is passed that isn't valid ADF.
    JSON strings encoding non-ADF dicts are intentionally treated as
    plain text, since a string is ambiguous (could be user text that
    happens to be valid JSON).
    """
    _EMPTY_ADF: dict = {"version": 1, "type": "doc", "content": [{"type": "paragraph", "content": []}]}

    if isinstance(text, dict):
        if not text:
            return _EMPTY_ADF
        if text.get("type") == "doc" and text.get("version") == 1:
            return text
        raise ValueError("Invalid ADF dict: must have 'type': 'doc' and 'version': 1")

    if not text:
        return _EMPTY_ADF

    # Check if the string is a JSON-encoded ADF document.
    # Only valid ADF (type=doc, version=1) is accepted; other JSON
    # strings fall through to plain-text handling intentionally.
    if isinstance(text, str):
        try:
            parsed = json.loads(text)
            if isinstance(parsed, dict) and parsed.get("type") == "doc" and parsed.get("version") == 1:
                return parsed
        except (json.JSONDecodeError, ValueError):
            pass

    # Wiki markup detection — convert before plain text fallback.
    # Length guard here so oversized text falls through to the
    # plain-text paragraph splitter (splits on \n) instead of
    # wiki_to_adf()'s single-paragraph fallback.
    if isinstance(text, str) and len(text) <= _MAX_WIKI_PARSE_LEN and _looks_like_wiki_markup(text):
        return wiki_to_adf(text)

    # Plain text — split into paragraphs
    content = []
    for para in str(text).split("\n"):
        if para.strip():
            content.append({"type": "paragraph", "content": [{"type": "text", "text": para}]})
        else:
            content.append({"type": "paragraph", "content": []})

    return {"version": 1, "type": "doc", "content": content}


def adf_to_text(value):
    """Extract plain text from an ADF document, or return value unchanged.

    Jira Cloud v3 returns description and comment bodies as ADF objects.
    This extracts the text content for display. Handles nested content
    nodes recursively.

    Args:
        value: ADF document dict or plain text string

    Returns:
        Plain text string
    """
    if not isinstance(value, dict) or value.get("type") != "doc":
        return value  # Not ADF, return as-is

    parts = []

    def _walk(nodes):
        for node in nodes:
            if node.get("type") == "text":
                parts.append(node.get("text", ""))
            elif "content" in node:
                _walk(node["content"])
            if node.get("type") == "paragraph":
                parts.append("\n")

    _walk(value.get("content", []))
    return "".join(parts).strip()


def normalize_components(components):
    """Normalize a list of component names or dicts to [{name: ...}]."""
    return [c if isinstance(c, dict) else {"name": str(c)} for c in components]


def parse_sprint_field(sprint_data):
    """Parse sprint field from Cloud (dict) or Server (string) format.

    Server format: "com.atlassian...@abc[id=123,name=Sprint 1,state=active,...]"
    Cloud format: {"id": 123, "name": "Sprint 1", "state": "active", ...}
    """
    if isinstance(sprint_data, dict):
        return sprint_data
    if isinstance(sprint_data, str):
        content = sprint_data
        bracket_start = content.find("[")
        bracket_end = content.rfind("]")
        if bracket_start != -1 and bracket_end != -1:
            content = content[bracket_start + 1 : bracket_end]
        info = {}
        for pair in content.split(","):
            if "=" in pair:
                key, value = pair.split("=", 1)
                info[key.strip()] = value.strip()
        return info
    return {}


def natural_sort_key(name):
    """Sort key for numeric-aware ordering of sprint names.

    "Sprint 9" sorts before "Sprint 10" instead of after.
    """
    return [(0, int(c), "") if c.isdigit() else (1, 0, c.lower()) for c in re.split(r"(\d+)", name)]


def escape_jql(value):
    """Escape a value for safe use in double-quoted JQL strings."""
    value = value.replace("\\", "\\\\")
    value = value.replace('"', '\\"')
    value = value.replace("\n", "\\n")
    value = value.replace("\r", "\\r")
    value = value.replace("\0", "")
    return value


def extract_sprint_summary(sprint):
    """Extract compact sprint info: id, name, state, dates."""
    return {
        "id": sprint.get("id"),
        "name": sprint.get("name"),
        "state": sprint.get("state"),
        "startDate": sprint.get("startDate"),
        "endDate": sprint.get("endDate"),
    }


def extract_nested_field(fields_data, field_name):
    """Extract a possibly nested field value from Jira issue fields.

    Handles dotted paths like "status.name", "assignee.displayName".
    For simple names, returns the raw value. For dict values without
    a dotted path, extracts .name or .value if present.
    """
    if "." in field_name:
        parts = field_name.split(".", 1)
        obj = fields_data.get(parts[0])
        if isinstance(obj, dict):
            return obj.get(parts[1])
        return None

    value = fields_data.get(field_name)
    if isinstance(value, dict):
        return value.get("name", value.get("value", str(value)))
    return value


def calculate_sprint_metrics(issues):
    """Calculate basic sprint metrics from an issues list.

    Returns total_issues, completed_issues, completion_rate.
    """
    total = len(issues)
    completed = sum(
        1
        for i in issues
        if (i.get("fields", {}).get("status", {}) or {}).get("statusCategory", {}).get("key") == "done"
    )
    return {
        "total_issues": total,
        "completed_issues": completed,
        "completion_rate": round(completed / total * 100, 1) if total > 0 else 0,
    }


def extract_user_fields(user):
    """Extract standard fields from a Jira user dict."""
    return {
        "accountId": user.get("accountId") or user.get("key"),
        "name": user.get("name"),
        "displayName": user.get("displayName"),
        "emailAddress": user.get("emailAddress"),
        "active": user.get("active"),
        "timeZone": user.get("timeZone"),
    }


def resolve_field_value(value, field_type, is_cloud=False):
    """Convert a value to the appropriate Jira field format.

    Handles type conversion for custom fields based on field_type:
    text, number, select, multi-select, version, user, or auto.

    Returns (converted_value, resolved_field_type).
    """
    if field_type == "auto":
        if isinstance(value, (int, float)):
            field_type = "number"
        elif isinstance(value, list):
            field_type = "multi-select"
        else:
            field_type = "text"

    if field_type == "number":
        try:
            result = float(value)
        except (ValueError, TypeError) as exc:
            raise ValueError(f"Cannot convert {str(value)[:50]!r} to number: {exc}") from exc
        if not math.isfinite(result):
            raise ValueError(f"Number value must be finite, got {result}")
        return result, field_type
    elif field_type == "select":
        return {"value": value}, field_type
    elif field_type == "multi-select":
        values = value if isinstance(value, list) else [value]
        try:
            return [{"value": str(v)} for v in values], field_type
        except (ValueError, TypeError) as exc:
            raise ValueError(f"Invalid value in multi-select list: {exc}") from exc
    elif field_type == "version":
        values = value if isinstance(value, list) else [value]
        try:
            return [{"name": str(v)} for v in values], field_type
        except (ValueError, TypeError) as exc:
            raise ValueError(f"Invalid value in version list: {exc}") from exc
    elif field_type == "user":
        if not isinstance(value, str):
            raise ValueError(f"User field requires a string value (accountId or username), got {type(value).__name__}")
        return ({"accountId": value} if is_cloud else {"name": value}), field_type
    else:
        return value, field_type
