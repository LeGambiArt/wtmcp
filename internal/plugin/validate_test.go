package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCompileParamsSchema(t *testing.T) {
	tool := ToolDef{
		Name: "test_tool",
		Params: map[string]ParamDef{
			"name":  {Type: "string", Required: true},
			"count": {Type: "integer"},
		},
	}

	cs, err := CompileParamsSchema("test_tool", tool)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if cs == nil {
		t.Fatal("expected compiled schema")
	}
}

func TestCompileEmptySchema(t *testing.T) {
	tool := ToolDef{Name: "empty"}
	cs, err := CompileParamsSchema("empty", tool)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if cs != nil {
		t.Error("expected nil for empty schema")
	}
}

func TestValidateValid(t *testing.T) {
	tool := ToolDef{
		Name: "test",
		Params: map[string]ParamDef{
			"name":  {Type: "string", Required: true},
			"count": {Type: "integer"},
		},
	}

	cs, _ := CompileParamsSchema("test", tool)

	tests := []struct {
		name   string
		params string
	}{
		{"all fields", `{"name":"alice","count":5}`},
		{"required only", `{"name":"bob"}`},
		{"extra fields", `{"name":"charlie","extra":"ignored"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := cs.Validate(json.RawMessage(tt.params)); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateInvalid(t *testing.T) {
	tool := ToolDef{
		Name: "test",
		Params: map[string]ParamDef{
			"name":  {Type: "string", Required: true},
			"count": {Type: "integer"},
		},
	}

	cs, _ := CompileParamsSchema("test", tool)

	tests := []struct {
		name   string
		params string
	}{
		{"missing required", `{"count":5}`},
		{"wrong type", `{"name":123}`},
		{"wrong type for optional", `{"name":"alice","count":"not_a_number"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := cs.Validate(json.RawMessage(tt.params))
			if err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

func TestCompileOversizedSchema(t *testing.T) {
	params := make(map[string]ParamDef)
	for i := 0; i < 2000; i++ {
		params[fmt.Sprintf("field_%04d_with_a_long_name_for_padding", i)] = ParamDef{
			Type:        "string",
			Description: "a]description that adds to the schema size significantly",
		}
	}
	tool := ToolDef{Name: "huge", Params: params}
	cs, err := CompileParamsSchema("huge", tool)
	if err == nil {
		t.Fatal("expected error for oversized schema")
	}
	if cs != nil {
		t.Error("expected nil compiled schema")
	}
}

func TestValidateErrorMessages(t *testing.T) {
	tool := ToolDef{
		Name: "test",
		Params: map[string]ParamDef{
			"name":  {Type: "string", Required: true},
			"count": {Type: "integer"},
		},
	}

	cs, _ := CompileParamsSchema("test", tool)

	tests := []struct {
		name     string
		params   string
		contains string
	}{
		{
			name:     "missing required shows field name",
			params:   `{"count":5}`,
			contains: "name",
		},
		{
			name:     "wrong type shows field path",
			params:   `{"name":123}`,
			contains: "/name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := cs.Validate(json.RawMessage(tt.params))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.contains) {
				t.Errorf("error %q should contain %q", err.Error(), tt.contains)
			}
			if strings.Contains(err.Error(), "&{") {
				t.Errorf("error contains raw Go struct: %q", err.Error())
			}
		})
	}
}

func TestCompileBadSchema_ReturnsError(t *testing.T) {
	t.Run("no properties still returns nil", func(t *testing.T) {
		tool := ToolDef{Name: "noparams"}
		cs, err := CompileParamsSchema("noparams", tool)
		if err != nil {
			t.Fatalf("no-property schema should not error: %v", err)
		}
		if cs != nil {
			t.Error("expected nil schema for no properties")
		}
	})

	t.Run("invalid schema type returns error", func(t *testing.T) {
		tool := ToolDef{
			Name: "bad_schema",
			Params: map[string]ParamDef{
				"field": {Type: "not_a_real_type", Required: true},
			},
		}
		cs, err := CompileParamsSchema("bad_schema", tool)
		if err == nil {
			t.Fatal("expected error for invalid schema type")
		}
		if cs != nil {
			t.Error("expected nil compiled schema on error")
		}
		if !strings.Contains(err.Error(), "schema compilation failed") {
			t.Errorf("expected 'schema compilation failed' in error, got: %v", err)
		}
	})
}

func TestCompileSchemaWithUntypedProperty(t *testing.T) {
	tool := ToolDef{
		Name: "test",
		Params: map[string]ParamDef{
			"custom_fields": {
				Type: "array",
				Items: &ItemsDef{
					Type: "object",
					Properties: map[string]ParamDef{
						"field_id": {Type: "string", Description: "field ID"},
						"value":    {Description: "depends on field_type"},
					},
					Required: []string{"field_id", "value"},
				},
			},
		},
	}

	cs, err := CompileParamsSchema("test", tool)
	if err != nil {
		t.Fatalf("compile should succeed for untyped property: %v", err)
	}
	if cs == nil {
		t.Fatal("expected compiled schema")
	}

	valid := []struct {
		name   string
		params string
	}{
		{"string value", `{"custom_fields":[{"field_id":"cf_1","value":"text"}]}`},
		{"numeric value", `{"custom_fields":[{"field_id":"cf_2","value":42}]}`},
		{"object value", `{"custom_fields":[{"field_id":"cf_3","value":{"name":"x"}}]}`},
		{"null value", `{"custom_fields":[{"field_id":"cf_4","value":null}]}`},
	}
	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			if err := cs.Validate(json.RawMessage(tt.params)); err != nil {
				t.Errorf("should accept %s: %v", tt.name, err)
			}
		})
	}
}

func TestValidateNilSchema(t *testing.T) {
	var cs *CompiledSchema
	if err := cs.Validate(json.RawMessage(`{"anything":"goes"}`)); err != nil {
		t.Errorf("nil schema should pass: %v", err)
	}
}

func TestValidateInvalidJSON(t *testing.T) {
	tool := ToolDef{
		Name: "test",
		Params: map[string]ParamDef{
			"name": {Type: "string", Required: true},
		},
	}

	cs, _ := CompileParamsSchema("test", tool)
	err := cs.Validate(json.RawMessage(`not json`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}
