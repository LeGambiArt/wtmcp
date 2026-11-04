package main

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
)

// A statement retains its original text. Decode whole statements rather than
// interpreting individual lines: a table-looking line can be string content,
// and an assignment can span several lines.
type tomlStatement struct {
	raw    string
	path   toml.Key
	header bool
}

func parseTOMLStatements(data []byte) ([]tomlStatement, error) {
	var statements []tomlStatement
	var context toml.Key
	lines := strings.SplitAfter(string(data), "\n")
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		var metadata toml.MetaData
		for {
			var value map[string]any
			var err error
			metadata, err = toml.Decode(raw, &value)
			if err == nil {
				break
			}
			i++
			if i >= len(lines) {
				return nil, fmt.Errorf("cannot parse TOML statement: %w", err)
			}
			raw += lines[i]
		}
		statement := tomlStatement{raw: raw}
		if keys := metadata.Keys(); len(keys) > 0 {
			statement.header = strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff")), "[")
			if statement.header {
				context = keys[0]
				statement.path = context
			} else {
				statement.path = append(append(toml.Key{}, context...), keys[0]...)
			}
		}
		statements = append(statements, statement)
	}
	return statements, nil
}

func tomlPathHasPrefix(path, prefix toml.Key) bool {
	return len(path) >= len(prefix) && reflect.DeepEqual(path[:len(prefix)], prefix)
}

// replaceTOMLValue changes a complete assignment, retaining its spelling and
// trailing comment. Let the parser locate '=' so quoted keys containing '='
// are not confused with the assignment operator.
func replaceTOMLValue(raw string, value any) (string, error) {
	encoded, err := toml.Marshal(map[string]any{"value": value})
	if err != nil {
		return "", err
	}
	rhs := strings.TrimSpace(strings.TrimPrefix(string(encoded), "value ="))
	for i := 0; i < len(raw); i++ {
		if raw[i] != '=' {
			continue
		}
		if _, err := toml.Decode(raw[:i]+"= 0", new(map[string]any)); err != nil {
			continue
		}
		suffix := "\n"
		if strings.HasSuffix(raw, "\r\n") {
			suffix = "\r\n"
		}
		for j := i + 1; j < len(raw); j++ {
			if raw[j] == '#' {
				if _, err := toml.Decode(raw[:j], new(map[string]any)); err == nil {
					suffix = " " + raw[j:]
					break
				}
			}
		}
		return raw[:i+1] + " " + rhs + suffix, nil
	}
	return "", fmt.Errorf("cannot locate TOML assignment")
}

// setTOMLFields supports explicit tables and dotted assignments. Inline tables
// enclosing the target table are rejected rather than risking other settings.
func setTOMLFields(data []byte, table toml.Key, fields map[string]any, fieldOrder []string) ([]byte, error) {
	statements, err := parseTOMLStatements(data)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	mainTable, insertAt := -1, len(statements)
	anchor := -1
	var context, relative toml.Key
	for i, statement := range statements {
		if statement.header {
			context = statement.path
			if mainTable >= 0 && insertAt == len(statements) {
				insertAt = i
			}
			if reflect.DeepEqual(statement.path, table) {
				mainTable = i
			}
			continue
		}
		if len(statement.path) > 0 && len(statement.path) <= len(table) && tomlPathHasPrefix(table, statement.path) {
			return nil, fmt.Errorf("%s uses an unsupported inline TOML table", table.String())
		}
		if len(statement.path) != len(table)+1 || !tomlPathHasPrefix(statement.path, table) {
			continue
		}
		if anchor < 0 && !tomlPathHasPrefix(context, table) {
			anchor = i
			relative = table[len(context):]
		}
		key := statement.path[len(table)]
		if value, ok := fields[key]; ok {
			statements[i].raw, err = replaceTOMLValue(statement.raw, value)
			if err != nil {
				return nil, err
			}
			seen[key] = true
		}
	}
	var added bytes.Buffer
	if mainTable < 0 && anchor >= 0 {
		insertAt = anchor
	} else if mainTable < 0 {
		insertAt = len(statements)
		fmt.Fprintf(&added, "\n[%s]\n", table.String())
	}
	for _, key := range fieldOrder {
		if value, ok := fields[key]; ok && !seen[key] {
			encoded, err := toml.Marshal(map[string]any{key: value})
			if err != nil {
				return nil, err
			}
			if mainTable < 0 && anchor >= 0 {
				path := append(append(toml.Key{}, relative...), key)
				encoded = []byte(path.String() + strings.TrimPrefix(string(encoded), key))
			}
			added.Write(encoded)
		}
	}
	var result strings.Builder
	for i := 0; i <= len(statements); i++ {
		if i == insertAt && added.Len() > 0 {
			if result.Len() > 0 && !strings.HasSuffix(result.String(), "\n") {
				result.WriteByte('\n')
			}
			result.Write(added.Bytes())
		}
		if i < len(statements) {
			result.WriteString(statements[i].raw)
		}
	}
	return []byte(result.String()), nil
}

func renameTOMLTable(data []byte, from, to toml.Key) ([]byte, error) {
	statements, err := parseTOMLStatements(data)
	if err != nil {
		return nil, err
	}
	var result strings.Builder
	var context toml.Key
	for _, statement := range statements {
		raw := statement.raw
		if statement.header {
			context = statement.path
			if tomlPathHasPrefix(context, from) {
				path := append(append(toml.Key{}, to...), context[len(from):]...)
				// Preserve header comments; the first successful prefix is the
				// actual closing bracket, even when a quoted key contains ']'.
				for i := range raw {
					if raw[i] == ']' {
						if _, err := toml.Decode(raw[:i+1], new(map[string]any)); err == nil {
							opening, closing := "[", "]"
							if strings.HasPrefix(strings.TrimSpace(raw), "[[") {
								opening, closing = "[[", "]]"
							}
							prefix := ""
							if strings.HasPrefix(raw, "\ufeff") {
								prefix = "\ufeff"
							}
							raw = prefix + opening + path.String() + closing + raw[i+1:]
							break
						}
					}
				}
			}
		} else if tomlPathHasPrefix(statement.path, from) && !tomlPathHasPrefix(context, from) {
			return nil, fmt.Errorf("%s rename requires an explicit table", from.String())
		}
		result.WriteString(raw)
	}
	return []byte(result.String()), nil
}
