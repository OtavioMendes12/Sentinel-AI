package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func noop(context.Context, json.RawMessage) (string, error) { return "", nil }

func readFileSchema() *Schema {
	return &Schema{
		Type: TypeObject,
		Properties: map[string]*Schema{
			"path":       {Type: TypeString, MaxLength: 20},
			"start_line": {Type: TypeInteger, Minimum: Bound(1)},
			"mode":       {Type: TypeString, Enum: []string{"full", "excerpt"}},
			"ratio":      {Type: TypeNumber, Minimum: Bound(0), Maximum: Bound(1)},
			"recursive":  {Type: TypeBoolean},
			"tags": {Type: TypeArray, MaxItems: 2, Items: &Schema{
				Type:       TypeObject,
				Properties: map[string]*Schema{"name": {Type: TypeString}},
				Required:   []string{"name"},
			}},
		},
		Required: []string{"path"},
	}
}

func TestSchemaValidateAccepts(t *testing.T) {
	t.Parallel()

	args := `{"path":"main.go","start_line":10,"mode":"full","ratio":0.5,"recursive":true,"tags":[{"name":"a"}]}`
	if err := readFileSchema().Validate([]byte(args)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSchemaValidateRejects(t *testing.T) {
	t.Parallel()

	tests := map[string]struct{ args, want string }{
		"invalid json":      {`{"path":`, "not valid JSON"},
		"trailing value":    {`{"path":"a"} {}`, "single JSON value"},
		"not an object":     {`["main.go"]`, "arguments: must be an object"},
		"missing required":  {`{}`, `missing required property "path"`},
		"unknown property":  {`{"path":"a","command":"rm -rf /"}`, `unknown property "command"`},
		"wrong type":        {`{"path":42}`, "path: must be a string"},
		"too long":          {`{"path":"` + strings.Repeat("a", 21) + `"}`, "path: must be at most 20"},
		"enum":              {`{"path":"a","mode":"raw"}`, "mode: must be one of full, excerpt"},
		"float as integer":  {`{"path":"a","start_line":1.5}`, "start_line: must be an integer"},
		"below minimum":     {`{"path":"a","start_line":0}`, "start_line: must be >= 1"},
		"above maximum":     {`{"path":"a","ratio":1.5}`, "ratio: must be <= 1"},
		"string as bool":    {`{"path":"a","recursive":"yes"}`, "recursive: must be a boolean"},
		"null value":        {`{"path":null}`, "path: must be a string"},
		"too many items":    {`{"path":"a","tags":[{"name":"a"},{"name":"b"},{"name":"c"}]}`, "tags: must have at most 2"},
		"nested path":       {`{"path":"a","tags":[{"name":"a"},{}]}`, `tags[1]: missing required property "name"`},
		"nested wrong type": {`{"path":"a","tags":[{"name":1}]}`, "tags[0].name: must be a string"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := readFileSchema().Validate([]byte(tt.args))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestSchemaValidateCapsProblems(t *testing.T) {
	t.Parallel()

	var props []string
	for i := range 50 {
		props = append(props, `"p`+strings.Repeat("x", i)+`":1`)
	}
	err := readFileSchema().Validate([]byte(`{"path":"a",` + strings.Join(props, ",") + `}`))
	if err == nil || strings.Count(err.Error(), "unknown property") != maxReportedProblems {
		t.Fatalf("expected problems capped at %d, got %v", maxReportedProblems, err)
	}
}

func TestSchemaMarshalClosesObjects(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(readFileSchema())
	if err != nil {
		t.Fatal(err)
	}
	// Once for the root object and once for the nested tag object.
	if n := strings.Count(string(raw), `"additionalProperties":false`); n != 2 {
		t.Fatalf("expected 2 closed objects, got %d: %s", n, raw)
	}
	if strings.Contains(string(raw), `"maxLength":0`) {
		t.Fatalf("zero-valued limits should be omitted: %s", raw)
	}
}

func TestRegistryRegister(t *testing.T) {
	t.Parallel()

	valid := Tool{Name: "read_file", Description: "Reads a file.", InputSchema: readFileSchema(), Risk: RiskRead, Handler: noop}

	r := NewRegistry()
	if err := r.Register(valid); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := r.Register(valid); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected duplicate error, got %v", err)
	}

	tests := map[string]func(*Tool){
		"bad name":            func(t *Tool) { t.Name = "Read File" },
		"empty name":          func(t *Tool) { t.Name = "" },
		"no description":      func(t *Tool) { t.Description = "" },
		"no handler":          func(t *Tool) { t.Handler = nil },
		"zero risk":           func(t *Tool) { t.Risk = 0 },
		"unknown risk":        func(t *Tool) { t.Risk = 9 },
		"nil schema":          func(t *Tool) { t.InputSchema = nil },
		"non-object schema":   func(t *Tool) { t.InputSchema = &Schema{Type: TypeString} },
		"undeclared required": func(t *Tool) { t.InputSchema = &Schema{Type: TypeObject, Required: []string{"x"}} },
		"array without items": func(t *Tool) {
			t.InputSchema = &Schema{Type: TypeObject, Properties: map[string]*Schema{"xs": {Type: TypeArray}}}
		},
		"unsupported type": func(t *Tool) {
			t.InputSchema = &Schema{Type: TypeObject, Properties: map[string]*Schema{"x": {Type: "null"}}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tool := valid
			tool.Name = "other_tool"
			mutate(&tool)
			if err := NewRegistry().Register(tool); err == nil {
				t.Fatal("expected registration to fail")
			}
		})
	}
}

func TestRegistryLookupAndOrder(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	for _, name := range []string{"search_code", "get_diff", "read_file"} {
		if err := r.Register(Tool{Name: name, Description: "d", InputSchema: &Schema{Type: TypeObject}, Risk: RiskRead, Handler: noop}); err != nil {
			t.Fatal(err)
		}
	}

	if _, ok := r.Lookup("read_file"); !ok {
		t.Error("read_file not found")
	}
	if _, ok := r.Lookup("run_shell"); ok {
		t.Error("unregistered tool found")
	}

	var names []string
	for _, tool := range r.Tools() {
		names = append(names, tool.Name)
	}
	if got := strings.Join(names, ","); got != "get_diff,read_file,search_code" {
		t.Errorf("Tools() order = %s", got)
	}
}
