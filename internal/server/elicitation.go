// Elicitation confirmation-message formatting.
//
// This file renders the parameter summary shown to a human approver
// before a write-tool call executes. The message must be bounded by
// absolute caps regardless of input shape, and the approver must see
// a faithful rendering of what will execute.

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxElicitParamLen is the byte budget for content fields in
// elicitation confirmation messages. Routing scalars and scalar
// arrays are never truncated mid-value; absolute caps bound the
// whole message regardless of input size.
const maxElicitParamLen = 1000

// maxElicitMetaParamsBytes caps the size of the structured parameters
// attachment on elicitation requests. Beyond it the attachment is
// omitted and the bounded message summary remains the only payload.
const maxElicitMetaParamsBytes = 1 << 20

// Caps that bound the confirmation message regardless of input size.
// The routing byte cap sits far above any legitimate routing value;
// the field, item, and key caps bound output height and width; the
// content and message caps bound the sections in aggregate so no
// input shape — item count, key count, or key size — can inflate the
// message. maxElicitMessageBytes is the absolute ceiling enforced at
// the formatElicitParams exit; the rest keep legitimate messages far
// below it.
const (
	maxElicitRoutingBytes  = 6 << 10  // routing section absolute byte cap
	maxElicitFields        = 50       // routing fields rendered before eliding
	maxElicitRoutingItems  = 50       // scalar-array items rendered before summarizing
	maxElicitContentItems  = 16       // content-array items rendered before summarizing
	maxElicitContentFields = 50       // content fields rendered before eliding
	maxElicitContentBytes  = 6 << 10  // content section aggregate byte cap
	maxElicitMessageBytes  = 16 << 10 // absolute ceiling on the whole message
	elicitGistFloor        = 60       // minimum gist budget for one rendered item
	elicitGistString       = 80       // strings longer than this are gistened, not shown
	elicitItemScalars      = 256      // scalar subfield bytes rendered per item
	elicitItemKeys         = 16       // keys rendered per array item before eliding
	elicitKeyLen           = 64       // max escaped bytes per rendered key name
)

// contentFields are parameter names treated as free-text content.
// Composite values (objects, or arrays carrying objects) are content
// by shape regardless of name; everything else is routing data.
var contentFields = map[string]bool{
	"body":        true,
	"content":     true,
	"message":     true,
	"description": true,
}

// formatElicitParams formats tool parameters for the confirmation
// prompt. The approver must see who/where in full — routing scalars
// and scalar arrays (address lists) are never truncated mid-value —
// and what in bounded gist form, with item counts and byte totals
// visible in every degradation path. Composite values (objects,
// arrays carrying objects) and free-text names share the content
// budget, bounded in aggregate and in field count; absolute caps
// bound the message for any input shape.
func formatElicitParams(data json.RawMessage, maxLen int) string {
	var params map[string]json.RawMessage
	if err := json.Unmarshal(data, &params); err != nil {
		return finalizeElicitMessage(truncateJSON(data, maxLen))
	}
	if len(params) == 0 {
		return "{}"
	}

	var routingKeys, cKeys []string
	hasDryRun := false
	for k := range params {
		switch {
		case k == "dry_run":
			hasDryRun = true
		case isContentField(k, params[k]):
			cKeys = append(cKeys, k)
		default:
			routingKeys = append(routingKeys, k)
		}
	}
	sort.Strings(routingKeys)
	sort.Strings(cKeys)

	ordered := make([]string, 0, len(routingKeys)+1)
	if hasDryRun {
		ordered = append(ordered, "dry_run")
	}
	ordered = append(ordered, routingKeys...)

	var lines []string
	routingBytes := 0
	renderedFields := 0
	elidedFields := 0
	elidedBytes := 0
	var elidedNames []string
	var lastElided [8]string
	elideRouting := func(k string, raw json.RawMessage) {
		elidedFields++
		elidedBytes += len(raw)
		if len(elidedNames) < 8 {
			elidedNames = append(elidedNames, renderKey(k))
		}
		lastElided[(elidedFields-1)%8] = k
	}
	for _, k := range ordered {
		if renderedFields >= maxElicitFields {
			elideRouting(k, params[k])
			continue
		}
		// The key prefix is framing: budget it up front so a byte-heavy
		// list can still render partially inside the remaining cap.
		prefix := len(fmt.Sprintf("  %s: ", renderKey(k)))
		value := formatRoutingValue(params[k], maxElicitRoutingBytes-routingBytes-prefix)
		line := fmt.Sprintf("  %s: %s", renderKey(k), value)
		if routingBytes+len(line) > maxElicitRoutingBytes {
			elideRouting(k, params[k])
			continue
		}
		lines = append(lines, line)
		routingBytes += len(line)
		renderedFields++
	}
	if elidedFields > 0 {
		note := fmt.Sprintf("  … +%d more parameters", elidedFields)
		// Name the first and last elided keys: write-target fields
		// (to, repo, path) sort unpredictably among junk fields, and
		// the approver must be able to see who/where was hidden.
		switch {
		case elidedFields <= 8:
			// elidedNames already holds them all
		case elidedFields <= 16:
			for j := 9; j <= elidedFields; j++ {
				elidedNames = append(elidedNames, renderKey(lastElided[(j-1)%8]))
			}
		default:
			elidedNames = append(elidedNames, "…")
			for j := elidedFields - 7; j <= elidedFields; j++ {
				elidedNames = append(elidedNames, renderKey(lastElided[(j-1)%8]))
			}
		}
		if len(elidedNames) > 0 {
			note += fmt.Sprintf(" (%s)", strings.Join(elidedNames, ", "))
		}
		note += fmt.Sprintf(", +%d bytes of routing data not shown", elidedBytes)
		lines = append(lines, note)
	}

	// The content budget comes from maxLen alone: a padded routing
	// field must not be able to hide composite content behind markers.
	// The section is bounded in aggregate (maxElicitContentBytes) and
	// in field count (maxElicitContentFields), so neither key count nor
	// key size can inflate it. Each field renders within the remaining
	// budget and actual rendered bytes are charged, so only the last
	// field can overshoot the cap.
	perField := 0
	if len(cKeys) > 0 {
		perField = maxLen / len(cKeys)
	}
	contentBytes := 0
	renderedContent := 0
	var elidedContent, elidedContentBytes int
	for _, k := range cKeys {
		raw := params[k]
		if renderedContent >= maxElicitContentFields || contentBytes >= maxElicitContentBytes {
			elidedContent++
			elidedContentBytes += len(raw)
			continue
		}
		budget := perField
		if rem := maxElicitContentBytes - contentBytes; rem < budget {
			budget = rem
		}
		var value string
		if jsonValueKind(raw) == "array" {
			value = formatContentArray(raw, budget)
		} else {
			value = renderContentValue(raw, budget)
		}
		line := fmt.Sprintf("  %s: %s", renderKey(k), value)
		lines = append(lines, line)
		contentBytes += len(line)
		renderedContent++
	}
	if elidedContent > 0 {
		lines = append(lines, fmt.Sprintf(
			"  … +%d more content fields [%d bytes total]",
			elidedContent, elidedContentBytes))
	}

	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, line := range lines {
		if i > 0 {
			buf.WriteString(",\n")
		}
		buf.WriteString(line)
	}
	buf.WriteString("\n}")
	return finalizeElicitMessage(buf.String())
}

// finalizeElicitMessage is the single exit point for the
// confirmation message: it replaces invalid UTF-8 (raw bytes can
// survive inside JSON string literals), strips invisible control
// characters — marking the removal so the approver knows a shown
// value is not the executed value byte-for-byte — and enforces the
// absolute output ceiling.
func finalizeElicitMessage(msg string) string {
	msg = strings.ToValidUTF8(msg, string(utf8.RuneError))
	msg, removed := stripInvisibleControls(msg)
	if removed > 0 {
		msg += fmt.Sprintf("\n[%d invisible characters removed]", removed)
	}
	return boundMessage(msg)
}

// formatElicitToolName renders the tool name for the confirmation
// header: invisible controls stripped under the same policy as the
// message body, then truncated, so the first line the approver reads
// can be neither visually reordered nor blown past the message caps.
func formatElicitToolName(name string) string {
	name = strings.ToValidUTF8(name, string(utf8.RuneError))
	name, _ = stripInvisibleControls(name)
	return truncateUTF8(name, elicitKeyLen)
}

// boundMessage enforces the absolute output ceiling. The per-section
// caps keep legitimate messages far below it; this is the backstop
// that bounds the message even if a section cap regresses. The cut
// lands on the last complete line so values are not split mid-token,
// and the marker length is reserved so the result never exceeds the
// ceiling.
func boundMessage(msg string) string {
	if len(msg) <= maxElicitMessageBytes {
		return msg
	}
	marker := fmt.Sprintf("[message truncated, %d bytes total]", len(msg))
	room := maxElicitMessageBytes - len(marker)
	cut := truncateUTF8(msg, room)
	if idx := strings.LastIndexByte(cut, '\n'); idx >= 0 {
		cut = cut[:idx+1]
	}
	return cut + marker
}

// renderKey renders a key name for the confirmation message: escaped
// and truncated to elicitKeyLen bytes of the escaped form. Escaped
// output contains no newline, so a key can never forge parameter
// lines or markers, and truncation bounds the rendered size of
// arbitrary key names.
func renderKey(k string) string {
	escaped := marshalString(k)
	if len(escaped) <= elicitKeyLen {
		return escaped
	}
	return truncateUTF8(escaped, elicitKeyLen) + "…"
}

// jsonValueKind classifies a raw JSON value by its first
// non-whitespace byte.
func jsonValueKind(raw json.RawMessage) string {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 {
		return "scalar"
	}
	switch trimmed[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	}
	return "scalar"
}

// isScalarArray reports whether raw is a JSON array whose items are
// all scalars. Such arrays carry routing data (address lists) and are
// rendered in full, never truncated mid-element.
func isScalarArray(raw json.RawMessage) bool {
	if jsonValueKind(raw) != "array" {
		return false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return false
	}
	for _, item := range items {
		switch jsonValueKind(item) {
		case "object", "array":
			return false
		}
	}
	return true
}

// isContentField reports whether a parameter renders in the budgeted
// content section: free-text names, and composite values (objects, or
// arrays carrying objects).
func isContentField(name string, raw json.RawMessage) bool {
	if contentFields[name] {
		return true
	}
	kind := jsonValueKind(raw)
	if kind == "object" {
		return true
	}
	return kind == "array" && !isScalarArray(raw)
}

// formatRoutingValue renders a routing value in full. Scalar arrays
// render one item per line, capped by item count and the remaining
// routing budget — which the renderer budgets against its own
// framing (brackets, header, more-items marker) so a byte-heavy list
// degrades to first-N-items instead of failing the caller's cap
// check and rendering nothing at all.
func formatRoutingValue(raw json.RawMessage, budget int) string {
	if !isScalarArray(raw) {
		return indentValue(raw, "  ")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		// An unparseable array must not display as an empty one: the
		// approver would see "[]" while the contents are unknown.
		return fmt.Sprintf("[unparseable array, %d bytes total]", len(raw))
	}
	if len(items) == 0 {
		return "[]"
	}
	header := fmt.Sprintf(" [%d items, %d bytes total]", len(items), len(raw))
	// Framing reserved out of the item budget: closing bracket and
	// header are always rendered; the more-items marker appears when
	// items are left over. The slack is generous, never tight.
	reserved := len(header) + 4 + 64
	rendered := 0
	acc := 0
	for i, item := range items {
		itemLen := len(compactJSON(item)) + 8
		if i >= maxElicitRoutingItems || acc+itemLen+reserved > budget {
			break
		}
		acc += itemLen
		rendered++
	}
	rest := len(items) - rendered
	var b strings.Builder
	b.WriteString("[\n")
	for i := 0; i < rendered; i++ {
		b.WriteString("    ")
		b.WriteString(compactJSON(items[i]))
		if i < rendered-1 || rest > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	if rest > 0 {
		fmt.Fprintf(&b, "    … +%d more items [%d bytes total]\n", rest, len(raw))
	}
	b.WriteString("  ]")
	b.WriteString(header)
	return b.String()
}

// formatContentArray renders a composite array for the content
// section, bounded by budget. Items that fit render in full;
// otherwise each rendered item is a single line with scalar
// subfields in full and long strings as gists. Rendering stops at
// the item-count cap or once the budget is spent — at least one item
// always renders, so a zero budget still shows the first item's
// one-liner rather than a bare marker. Item count and byte totals
// appear in every degradation path.
func formatContentArray(raw json.RawMessage, budget int) string {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return renderContentValue(raw, budget)
	}
	header := fmt.Sprintf("[%d items, %d bytes total]", len(items), len(raw))
	if len(items) == 0 {
		return "[]"
	}

	if budget > 0 {
		compactTotal := 0
		for _, item := range items {
			compactTotal += len(compactJSON(item)) + 8
			if compactTotal > budget {
				break
			}
		}
		if compactTotal <= budget {
			var b strings.Builder
			b.WriteString("[\n")
			for i, item := range items {
				b.WriteString("    ")
				b.WriteString(compactJSON(item))
				if i < len(items)-1 {
					b.WriteString(",")
				}
				b.WriteString("\n")
			}
			b.WriteString("  ] ")
			b.WriteString(header)
			return b.String()
		}
	}

	// Line budget per item: the field budget split across the
	// item-count cap, floored at the gist floor. Items render
	// incrementally and stop once the budget is spent, so the field
	// overshoots its budget by at most one item line.
	lineBudget := elicitGistFloor
	if budget > 0 {
		split := len(items)
		if split > maxElicitContentItems {
			split = maxElicitContentItems
		}
		if b := budget / split; b > lineBudget {
			lineBudget = b
		}
	}

	var b strings.Builder
	b.WriteString("[\n")
	rendered := 0
	total := 0
	for i, item := range items {
		if i >= maxElicitContentItems || (total >= budget && i > 0) {
			break
		}
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n    ")
		line := renderItemLine(item, lineBudget)
		b.WriteString(line)
		total += len(line)
		rendered++
	}
	rest := len(items) - rendered
	if rest > 0 {
		fmt.Fprintf(&b, "\n    … +%d more items [%d bytes total]", rest, len(raw))
	}
	b.WriteString("\n  ] ")
	b.WriteString(header)
	return b.String()
}

// renderContentValue renders a non-array content value: pretty when
// it fits the budget, truncated otherwise. Very large values skip
// pretty-printing and truncate the compact form directly.
func renderContentValue(raw json.RawMessage, perField int) string {
	if perField <= 0 {
		return fmt.Sprintf("[truncated, %d bytes total]", len(raw))
	}
	if len(raw) > 10*perField {
		return fmt.Sprintf("%s [truncated, %d bytes total]",
			truncateUTF8(compactJSON(raw), perField), len(raw))
	}
	pretty := indentValue(raw, "  ")
	if len(pretty) <= perField {
		return pretty
	}
	return fmt.Sprintf("%s [truncated, %d bytes total]",
		truncateUTF8(pretty, perField), len(raw))
}

// renderItemLine renders one array item on a single line, bounded by
// lineBudget. Scalar subfields render in full up to elicitItemScalars
// (they carry the write target); long strings render as normalized
// gists drawing from the line budget; keys beyond the per-item key cap
// or the budget collapse into a count-and-bytes marker that names the
// first dropped keys, so a dropped write-target subfield stays
// identifiable. Output is escaped JSON only — an item can never forge
// parameter lines with embedded newlines.
func renderItemLine(item json.RawMessage, lineBudget int) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(item, &obj); err != nil || obj == nil {
		return gistCompactValue(item, lineBudget)
	}
	var gistKeys, scalarKeys []string
	for k := range obj {
		if isLongStringValue(obj[k]) {
			gistKeys = append(gistKeys, k)
		} else {
			scalarKeys = append(scalarKeys, k)
		}
	}
	sort.Strings(scalarKeys)
	sort.Strings(gistKeys)

	var parts []string
	used := 0
	scalarBytes := 0
	elidedKeys := 0
	elidedBytes := 0
	var elidedNames []string
	elide := func(k string, v json.RawMessage) {
		elidedKeys++
		elidedBytes += len(v)
		if len(elidedNames) < 8 {
			elidedNames = append(elidedNames, renderKey(k))
		}
	}
	for _, k := range append(scalarKeys, gistKeys...) {
		v := obj[k]
		if len(parts) >= elicitItemKeys {
			elide(k, v)
			continue
		}
		if isLongStringValue(v) {
			// Gists draw from the line budget; once it is spent the
			// remaining keys collapse into the more-keys marker.
			if used >= lineBudget && len(parts) > 0 {
				elide(k, v)
				continue
			}
			share := lineBudget - used
			if share < 0 {
				share = 0
			}
			part := fmt.Sprintf("%s: %s", renderKey(k), gistStringValue(v, share))
			parts = append(parts, part)
			used += len(part)
			continue
		}
		// Scalar subfields keep their own allowance: they carry the
		// write target and render in full up to elicitItemScalars.
		part := fmt.Sprintf("%s: %s", renderKey(k), compactJSON(v))
		if scalarBytes+len(part) > elicitItemScalars {
			parts = append(parts, fmt.Sprintf("%s: …", renderKey(k)))
			continue
		}
		parts = append(parts, part)
		used += len(part)
		scalarBytes += len(part)
	}
	out := "{" + strings.Join(parts, ", ")
	if elidedKeys > 0 {
		note := fmt.Sprintf(" … +%d more keys", elidedKeys)
		if len(elidedNames) > 0 {
			note += fmt.Sprintf(" (%s)", strings.Join(elidedNames, ", "))
		}
		note += fmt.Sprintf(" [%d bytes total]", elidedBytes)
		// renderKey output carries no newline, so this truncation
		// guard only bounds size, never line integrity.
		out += truncateUTF8(note, 384)
	}
	return out + "}"
}

// isLongStringValue reports whether raw is a string value longer
// than the gist threshold.
func isLongStringValue(raw json.RawMessage) bool {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	return len(s) > elicitGistString
}

// gistStringValue renders a long string value as a normalized,
// escaped gist. A vacuous gist (whitespace, near-empty) renders as an
// anomaly marker instead of a blank-looking line.
func gistStringValue(raw json.RawMessage, budget int) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return gistCompactValue(raw, budget)
	}
	// Bound the normalization work: a gist renders at most budget
	// bytes, so normalizing beyond a small multiple of it is wasted
	// allocation (a 10 MB string otherwise transiently allocates
	// ~10x its size on the synchronous confirmation path).
	limit := 4*budget + 64
	if limit < 256 {
		limit = 256
	}
	if len(s) > limit {
		s = truncateUTF8(s, limit)
	}
	normalized := normalizeGist(s)
	if len(normalized) < 8 {
		return fmt.Sprintf("[no preview, %d bytes]", len(raw))
	}
	if len(normalized) <= budget {
		return marshalString(normalized)
	}
	return marshalString(truncateUTF8(normalized, budget)) + " … [truncated]"
}

// gistCompactValue renders a non-object item or fallback value,
// truncated to the budget with a per-item marker.
func gistCompactValue(raw json.RawMessage, budget int) string {
	compact := compactJSON(raw)
	if len(compact) <= budget {
		return compact
	}
	return truncateUTF8(compact, budget) + " … [truncated]"
}

// normalizeGist trims and collapses whitespace runs so a gist is a
// single readable line.
func normalizeGist(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// marshalString renders s as an escaped single-line JSON string.
func marshalString(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		return fmt.Sprintf("%q", s)
	}
	return string(encoded)
}

// compactJSON compacts a raw JSON value onto one line. An
// unparseable value renders as an anomaly marker rather than raw
// passthrough: raw bytes can contain newlines, which would let an
// item forge parameter lines in the confirmation message.
func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return fmt.Sprintf("[unparseable value, %d bytes total]", len(raw))
	}
	return buf.String()
}

// stripInvisibleControls removes invisible control characters from
// the confirmation message and reports how many were removed. See
// isInvisibleControl for the character policy.
func stripInvisibleControls(s string) (string, int) {
	removed := 0
	out := strings.Map(func(r rune) rune {
		if isInvisibleControl(r) {
			removed++
			return -1
		}
		return r
	}, s)
	return out, removed
}

// isInvisibleControl reports whether r is invisible in most
// renderings and can therefore pad or reorder the values the
// approver relies on: all Unicode format characters (category Cf)
// plus the default-ignorable variation selectors and the combining
// grapheme joiner. Unlike the tool-output sanitizer
// (stripFormatChars in sanitize.go), U+200D (zero-width joiner) is
// stripped here: in the approval gate o\u200Drg must not display as
// org, and emoji readability is not worth invisible padding in a
// security decision. Legitimate combining marks (e.g. U+0301) are
// visible-enough text and stay.
func isInvisibleControl(r rune) bool {
	if unicode.Is(unicode.Cf, r) {
		return true
	}
	switch {
	case r >= 0xFE00 && r <= 0xFE0F, // variation selectors
		r >= 0xE0100 && r <= 0xE01EF, // variation selectors supplement
		r == 0x034F:                  // combining grapheme joiner
		return true
	}
	return false
}

// indentValue pretty-prints a JSON value with the given prefix for
// continuation lines. On error it degrades to the single-line
// compact form rather than passing raw bytes through.
func indentValue(raw json.RawMessage, prefix string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, prefix, "  "); err != nil {
		return compactJSON(raw)
	}
	return buf.String()
}

// truncateJSON pretty-prints JSON and truncates to maxLen bytes,
// backing off to the last valid UTF-8 rune boundary. Values that do
// not parse degrade to the single-line compact form, never to raw
// multi-line passthrough.
func truncateJSON(data json.RawMessage, maxLen int) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		s := compactJSON(data)
		if len(s) > maxLen {
			return truncateUTF8(s, maxLen) + "..."
		}
		return s
	}
	s := buf.String()
	if len(s) > maxLen {
		return truncateUTF8(s, maxLen) + "..."
	}
	return s
}
