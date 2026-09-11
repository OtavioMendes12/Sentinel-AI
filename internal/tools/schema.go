package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// JSON types supported by Schema.
const (
	TypeObject  = "object"
	TypeArray   = "array"
	TypeString  = "string"
	TypeInteger = "integer"
	TypeNumber  = "number"
	TypeBoolean = "boolean"
)

// maxReportedProblems caps validation output so a wildly wrong payload does
// not flood the model's context.
const maxReportedProblems = 20

// Schema is the subset of JSON Schema used for tool arguments. The same value
// is sent to the model as the tool definition and used to validate what the
// model sends back, so the two cannot drift apart. Objects never accept
// undeclared properties.
type Schema struct {
	Type        string             `json:"type"`
	Description string             `json:"description,omitempty"`
	Properties  map[string]*Schema `json:"properties,omitempty"`
	Required    []string           `json:"required,omitempty"`
	Items       *Schema            `json:"items,omitempty"`
	Enum        []string           `json:"enum,omitempty"`
	Minimum     *float64           `json:"minimum,omitempty"`
	Maximum     *float64           `json:"maximum,omitempty"`
	MaxLength   int                `json:"maxLength,omitempty"`
	MaxItems    int                `json:"maxItems,omitempty"`
}

// Bound returns a pointer to v, for Schema.Minimum and Schema.Maximum.
func Bound(v float64) *float64 { return &v }

// MarshalJSON adds "additionalProperties": false to every object schema.
func (s Schema) MarshalJSON() ([]byte, error) {
	type plain Schema // drops the method set to avoid recursion
	if s.Type != TypeObject {
		return json.Marshal(plain(s))
	}
	return json.Marshal(struct {
		plain
		AdditionalProperties bool `json:"additionalProperties"`
	}{plain: plain(s)})
}

// Validate checks that args is a single JSON value matching the schema. The
// error lists the problems found, using paths such as "findings[0].line".
func (s *Schema) Validate(args []byte) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("arguments are not valid JSON: %w", err)
	}
	if dec.More() {
		return errors.New("arguments must be a single JSON value")
	}

	var problems []string
	s.validate("", v, &problems)
	if len(problems) == 0 {
		return nil
	}
	if len(problems) > maxReportedProblems {
		problems = append(problems[:maxReportedProblems], "(more problems omitted)")
	}
	return errors.New(strings.Join(problems, "; "))
}

func (s *Schema) validate(at string, v any, problems *[]string) {
	if s == nil { // unreachable for registered tools: check() rejects nil schemas
		return
	}
	fail := func(format string, args ...any) {
		name := at
		if name == "" {
			name = "arguments"
		}
		*problems = append(*problems, name+": "+fmt.Sprintf(format, args...))
	}

	switch s.Type {
	case TypeObject:
		obj, ok := v.(map[string]any)
		if !ok {
			fail("must be an object")
			return
		}
		for _, name := range s.Required {
			if _, present := obj[name]; !present {
				fail("missing required property %q", name)
			}
		}
		for _, name := range sortedKeys(obj) {
			prop, known := s.Properties[name]
			if !known {
				fail("unknown property %q", name)
				continue
			}
			prop.validate(join(at, name), obj[name], problems)
		}

	case TypeArray:
		arr, ok := v.([]any)
		if !ok {
			fail("must be an array")
			return
		}
		if s.MaxItems > 0 && len(arr) > s.MaxItems {
			fail("must have at most %d items", s.MaxItems)
		}
		for i, item := range arr {
			s.Items.validate(fmt.Sprintf("%s[%d]", at, i), item, problems)
		}

	case TypeString:
		str, ok := v.(string)
		switch {
		case !ok:
			fail("must be a string")
		case len(s.Enum) > 0 && !slices.Contains(s.Enum, str):
			fail("must be one of %s", strings.Join(s.Enum, ", "))
		case s.MaxLength > 0 && utf8.RuneCountInString(str) > s.MaxLength:
			fail("must be at most %d characters", s.MaxLength)
		}

	case TypeInteger, TypeNumber:
		kind := "a number"
		if s.Type == TypeInteger {
			kind = "an integer"
		}
		num, ok := v.(json.Number)
		if !ok {
			fail("must be %s", kind)
			return
		}
		// Integers must be written as integers (10, not 10.0) so they decode
		// into Go int fields later.
		var f float64
		var err error
		if s.Type == TypeInteger {
			var n int64
			n, err = num.Int64()
			f = float64(n)
		} else {
			f, err = num.Float64()
		}
		if err != nil {
			fail("must be %s", kind)
			return
		}
		if s.Minimum != nil && f < *s.Minimum {
			fail("must be >= %v", *s.Minimum)
		}
		if s.Maximum != nil && f > *s.Maximum {
			fail("must be <= %v", *s.Maximum)
		}

	case TypeBoolean:
		if _, ok := v.(bool); !ok {
			fail("must be a boolean")
		}
	}
}

// check verifies that the schema itself is well formed. It runs when a tool
// is registered, so definition mistakes fail at startup, not mid-review.
func (s *Schema) check(at string) error {
	if s == nil {
		return fmt.Errorf("%s: schema is nil", at)
	}
	switch s.Type {
	case TypeObject:
		for _, name := range s.Required {
			if _, ok := s.Properties[name]; !ok {
				return fmt.Errorf("%s: required property %q is not declared", at, name)
			}
		}
		for _, name := range sortedKeys(s.Properties) {
			if err := s.Properties[name].check(join(at, name)); err != nil {
				return err
			}
		}
	case TypeArray:
		return s.Items.check(at + "[]")
	case TypeString, TypeInteger, TypeNumber, TypeBoolean:
	default:
		return fmt.Errorf("%s: unsupported type %q", at, s.Type)
	}
	return nil
}

func join(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
