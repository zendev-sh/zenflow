package exec

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestSubmitResultToolDef_NilSchema(t *testing.T) {
	def := SubmitResultToolDef(nil)
	if def.Name != "" {
		t.Errorf("name = %q, want empty for nil schema", def.Name)
	}
}

func TestSubmitResultToolDef_ValidSchema(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"done": map[string]any{"type": "boolean"}},
		"required":   []string{"done"},
	}
	def := SubmitResultToolDef(schema)
	if def.Name != "submit_result" {
		t.Errorf("name = %q, want %q", def.Name, "submit_result")
	}
	if def.InputSchema == nil {
		t.Fatal("parameters is nil")
	}
	// Should be valid JSON.
	var parsed map[string]any
	if err := json.Unmarshal(def.InputSchema, &parsed); err != nil {
		t.Fatalf("invalid parameters JSON: %v", err)
	}
}

func TestSubmitResultHandler_Handle_InvalidJSON(t *testing.T) {
	h := NewSubmitResultHandler(nil)
	_, _, err := h.Handle(json.RawMessage(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error = %v, want 'invalid JSON'", err)
	}
}

func TestSubmitResultHandler_Handle_MissingRequired(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"done": map[string]any{"type": "boolean"}},
		"required":   []any{"done"},
	}
	h := NewSubmitResultHandler(schema)
	_, _, err := h.Handle(json.RawMessage(`{"other": true}`))
	if err == nil {
		t.Fatal("expected error for missing required field")
	}
	if !strings.Contains(err.Error(), "missing required field") {
		t.Errorf("error = %v, want 'missing required field'", err)
	}
}

func TestSubmitResultHandler_Handle_Valid(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"done": map[string]any{"type": "boolean"}},
		"required":   []any{"done"},
	}
	h := NewSubmitResultHandler(schema)
	result, terminated, err := h.Handle(json.RawMessage(`{"done": true}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !terminated {
		t.Error("expected terminated=true")
	}
	if done, ok := result["done"].(bool); !ok || !done {
		t.Errorf("result[done] = %v, want true", result["done"])
	}
}

func TestSubmitResultHandler_Handle_NilSchema(t *testing.T) {
	h := NewSubmitResultHandler(nil)
	result, terminated, err := h.Handle(json.RawMessage(`{"anything": "goes"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !terminated {
		t.Error("expected terminated=true")
	}
	if result["anything"] != "goes" {
		t.Errorf("result = %v", result)
	}
}

// --- checkType coverage for all types ---

func TestCheckType(t *testing.T) {
	cases := []struct {
		name     string
		field    string
		value    any
		typeName string
		wantErr  bool
	}{
		{"boolean_valid", "b", true, "boolean", false},
		{"boolean_invalid", "b", "not-bool", "boolean", true},
		{"string_valid", "s", "hello", "string", false},
		{"string_invalid", "s", 42.0, "string", true},
		{"number_valid", "n", 3.14, "number", false},
		{"number_invalid", "n", "not-number", "number", true},
		{"integer_valid", "i", 42.0, "integer", false},
		{"integer_non_float", "i", "not-int", "integer", true},
		{"integer_fractional", "i", 3.5, "integer", true},
		{"object_valid", "o", map[string]any{}, "object", false},
		{"object_invalid", "o", "not-object", "object", true},
		{"array_valid", "a", []any{1, 2}, "array", false},
		{"array_invalid", "a", "not-array", "array", true},
		{"null_valid", "n", nil, "null", false},
		{"null_invalid", "n", "not-null", "null", true},
		// Unknown type names should be accepted without validation,
		// not crash. Permissive-by-default lets schemas evolve.
		{"unknown_no_error", "x", "anything", "unknown-type", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkType(tc.field, tc.value, tc.typeName)
			if tc.wantErr && err == nil {
				t.Errorf("checkType(%q, %v, %q): want error, got nil", tc.field, tc.value, tc.typeName)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("checkType(%q, %v, %q): unexpected error: %v", tc.field, tc.value, tc.typeName, err)
			}
		})
	}
}

// --- checkType via Handle with all types ---

func TestSubmitResultHandler_AllTypes(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"b": map[string]any{"type": "boolean"},
			"s": map[string]any{"type": "string"},
			"n": map[string]any{"type": "number"},
			"i": map[string]any{"type": "integer"},
			"o": map[string]any{"type": "object"},
			"a": map[string]any{"type": "array"},
		},
	}
	h := NewSubmitResultHandler(schema)

	// Valid input with all types.
	args := json.RawMessage(`{"b":true,"s":"hi","n":3.14,"i":42,"o":{},"a":[]}`)
	result, terminated, err := h.Handle(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !terminated {
		t.Error("expected terminated=true")
	}
	if result["b"] != true {
		t.Errorf("b = %v, want true", result["b"])
	}

	// Invalid: wrong type for boolean field.
	_, _, err = h.Handle(json.RawMessage(`{"b":"not-bool"}`))
	if err == nil {
		t.Fatal("expected error for wrong type")
	}
	if !strings.Contains(err.Error(), "must be boolean") {
		t.Errorf("error = %v, want 'must be boolean'", err)
	}
}

// --- toStringSlice coverage ---

func TestToStringSlice_StringSlice(t *testing.T) {
	input := []string{"a", "b"}
	result, ok := toStringSlice(input)
	if !ok {
		t.Fatal("expected ok=true for []string")
	}
	if len(result) != 2 || result[0] != "a" || result[1] != "b" {
		t.Errorf("result = %v, want [a b]", result)
	}
}

func TestToStringSlice_AnySlice(t *testing.T) {
	input := []any{"x", "y"}
	result, ok := toStringSlice(input)
	if !ok {
		t.Fatal("expected ok=true for []any of strings")
	}
	if len(result) != 2 || result[0] != "x" || result[1] != "y" {
		t.Errorf("result = %v, want [x y]", result)
	}
}

func TestToStringSlice_AnySliceNonString(t *testing.T) {
	input := []any{"ok", 42}
	_, ok := toStringSlice(input)
	if ok {
		t.Fatal("expected ok=false for []any with non-string")
	}
}

func TestToStringSlice_InvalidType(t *testing.T) {
	_, ok := toStringSlice(42)
	if ok {
		t.Fatal("expected ok=false for int input")
	}
}

// --- validateResultSchema: required not string array ---

func TestValidateResultSchema_RequiredNotStringArray(t *testing.T) {
	result := map[string]any{"done": true}
	schema := map[string]any{
		"required": 42, // not a string array
	}
	err := validateResultSchema(result, schema)
	if err == nil {
		t.Fatal("expected error for invalid required")
	}
	if !strings.Contains(err.Error(), "not a string array") {
		t.Errorf("error = %v, want 'not a string array'", err)
	}
}

func TestSubmitResultToolDef_MarshalError(t *testing.T) {
	// Create a schema with a channel - json.Marshal can't handle channels.
	schema := map[string]any{
		"type": make(chan int),
	}
	td := SubmitResultToolDef(schema)
	if td.Name != toolNameSubmitResult {
		t.Errorf("name = %q, want %q", td.Name, toolNameSubmitResult)
	}
	// Should fall back to generic {"type":"object"}.
	if string(td.InputSchema) != `{"type":"object"}` {
		t.Errorf("parameters = %s, want fallback", td.InputSchema)
	}
}

func TestValidateResultSchema_PropertyFieldNotInResult(t *testing.T) {
	// Property defined in schema but not present in result → continue (skip).
	schema := map[string]any{
		"properties": map[string]any{
			"missing": map[string]any{"type": "string"},
		},
	}
	err := validateResultSchema(map[string]any{"other": "val"}, schema)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateResultSchema_PropDefNotMap(t *testing.T) {
	// propDef is not a map[string]any → continue (skip type check).
	schema := map[string]any{
		"properties": map[string]any{
			"field": "not-a-map", // should be map[string]any
		},
	}
	err := validateResultSchema(map[string]any{"field": "val"}, schema)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateResultSchema_TypeFieldNotString(t *testing.T) {
	// propSchema["type"] is not a string → continue (skip type check).
	schema := map[string]any{
		"properties": map[string]any{
			"field": map[string]any{"type": 42}, // type should be string
		},
	}
	err := validateResultSchema(map[string]any{"field": "val"}, schema)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckType_IntegerNaN(t *testing.T) {
	err := checkType("field", math.NaN(), "integer")
	if err == nil {
		t.Fatal("expected error for NaN")
	}
}

func TestCheckType_IntegerInf(t *testing.T) {
	err := checkType("field", math.Inf(1), "integer")
	if err == nil {
		t.Fatal("expected error for Inf")
	}
}

func TestCheckType_IntegerBeyondSafeRange(t *testing.T) {
	// 1<<54 is well beyond 1<<53 safe range.
	err := checkType("field", float64(1<<54), "integer")
	if err == nil {
		t.Fatal("expected error for beyond safe range")
	}
	if !strings.Contains(err.Error(), "exceeds safe") {
		t.Errorf("error = %v, want 'exceeds safe'", err)
	}
}

func TestCheckType_IntegerNegBeyondSafeRange(t *testing.T) {
	err := checkType("field", -float64(1<<54), "integer")
	if err == nil {
		t.Fatal("expected error for negative beyond safe range")
	}
	if !strings.Contains(err.Error(), "exceeds safe") {
		t.Errorf("error = %v, want 'exceeds safe'", err)
	}
}

// --- Nested-schema recursion (C17) ---

// TestValidateResultSchema_NestedRequired_Missing verifies the recursion
// into a nested object: schema declares foo.required=[bar], input has
// foo={} - validation must error on the missing nested-required field.
func TestValidateResultSchema_NestedRequired_Missing(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"foo": map[string]any{
				"type":     "object",
				"required": []any{"bar"},
				"properties": map[string]any{
					"bar": map[string]any{"type": "string"},
				},
			},
		},
	}
	input := map[string]any{"foo": map[string]any{}}
	err := validateResultSchema(input, schema)
	if err == nil {
		t.Fatal("expected error for missing nested required field")
	}
	if !strings.Contains(err.Error(), "missing required field") {
		t.Errorf("error = %v, want 'missing required field'", err)
	}
	if !strings.Contains(err.Error(), "bar") {
		t.Errorf("error = %v, want mention of 'bar'", err)
	}
}

// TestValidateResultSchema_NestedTypeWrong verifies recursion catches a
// nested type mismatch: foo.bar declared string, input has foo.bar=123.
func TestValidateResultSchema_NestedTypeWrong(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"foo": map[string]any{
				"type":     "object",
				"required": []any{"bar"},
				"properties": map[string]any{
					"bar": map[string]any{"type": "string"},
				},
			},
		},
	}
	// 123 unmarshals from JSON as float64.
	input := map[string]any{"foo": map[string]any{"bar": float64(123)}}
	err := validateResultSchema(input, schema)
	if err == nil {
		t.Fatal("expected error for nested type mismatch")
	}
	if !strings.Contains(err.Error(), "must be string") {
		t.Errorf("error = %v, want 'must be string'", err)
	}
}

// TestValidateResultSchema_ArrayOfObjects_SecondElementMissingRequired
// verifies recursion into array items: schema declares items as an
// object with required=[x], input has [{x:1},{}] - the second element
// must trigger an error.
func TestValidateResultSchema_ArrayOfObjects_SecondElementMissingRequired(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"items": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":     "object",
					"required": []any{"x"},
					"properties": map[string]any{
						"x": map[string]any{"type": "number"},
					},
				},
			},
		},
	}
	input := map[string]any{
		"items": []any{
			map[string]any{"x": float64(1)},
			map[string]any{},
		},
	}
	err := validateResultSchema(input, schema)
	if err == nil {
		t.Fatal("expected error on second array element missing required field")
	}
	// Error should mention the index path and the missing field.
	if !strings.Contains(err.Error(), "items[1]") {
		t.Errorf("error = %v, want mention of 'items[1]'", err)
	}
	if !strings.Contains(err.Error(), "x") {
		t.Errorf("error = %v, want mention of 'x'", err)
	}
}

// TestValidateResultSchema_NestedHappyPath verifies nested-required +
// types correct does NOT error.
func TestValidateResultSchema_NestedHappyPath(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"foo": map[string]any{
				"type":     "object",
				"required": []any{"bar"},
				"properties": map[string]any{
					"bar": map[string]any{"type": "string"},
				},
			},
			"items": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":     "object",
					"required": []any{"x"},
					"properties": map[string]any{
						"x": map[string]any{"type": "number"},
					},
				},
			},
		},
	}
	input := map[string]any{
		"foo": map[string]any{"bar": "ok"},
		"items": []any{
			map[string]any{"x": float64(1)},
			map[string]any{"x": float64(2)},
		},
	}
	if err := validateResultSchema(input, schema); err != nil {
		t.Fatalf("unexpected error on nested happy path: %v", err)
	}
}

// TestValidateResultSchema_ArrayOfPrimitives_TypeMismatch verifies
// items-type checking on primitive-element arrays: items=[{type:string}]
// with input ["a", 2] must error on the second element.
func TestValidateResultSchema_ArrayOfPrimitives_TypeMismatch(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tags": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
		},
	}
	input := map[string]any{
		"tags": []any{"a", float64(2)},
	}
	err := validateResultSchema(input, schema)
	if err == nil {
		t.Fatal("expected error on tags[1] type mismatch")
	}
	if !strings.Contains(err.Error(), "tags[1]") {
		t.Errorf("error = %v, want mention of 'tags[1]'", err)
	}
	if !strings.Contains(err.Error(), "must be string") {
		t.Errorf("error = %v, want 'must be string'", err)
	}
}

// TestHasObjectShape_PropertiesMap covers the `_, ok := schema["properties"].(map[string]any); if ok { return true }`
// branch (submit_result.go:156-158).
func TestHasObjectShape_PropertiesMap(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{"foo": map[string]any{}},
	}
	if !hasObjectShape(schema) {
		t.Error("hasObjectShape({properties:{...}}) = false, want true")
	}
}

// TestHasObjectShape_RequiredOnly covers the second branch
// (submit_result.go:159-161): `properties` absent but `required` present
// still flags the schema as object-shaped.
func TestHasObjectShape_RequiredOnly(t *testing.T) {
	schema := map[string]any{
		"required": []any{"foo"},
	}
	if !hasObjectShape(schema) {
		t.Error("hasObjectShape({required:[...]}) = false, want true")
	}
}

// TestHasObjectShape_NeitherPropertiesNorRequired covers the false return
// path so all three exits of hasObjectShape are exercised.
func TestHasObjectShape_NeitherPropertiesNorRequired(t *testing.T) {
	schema := map[string]any{"type": "string"}
	if hasObjectShape(schema) {
		t.Error("hasObjectShape({type:string}) = true, want false")
	}
}

// TestValidateResultSchema_ObjectImplicit_ValNotMap covers the
// "implicit object shape, val is not a map → continue" branch
// (submit_result.go:116-121). Schema declares `properties` (object shape)
// without `type:"object"`; input value is a string. Validation must NOT
// recurse and must NOT error (the implicit shape is best-effort).
func TestValidateResultSchema_ObjectImplicit_ValNotMap(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"foo": map[string]any{
				// implicit object: properties present, type missing
				"properties": map[string]any{
					"bar": map[string]any{"type": "string"},
				},
			},
		},
	}
	// foo is a string, not a map. Validator must skip recursion silently.
	input := map[string]any{"foo": "not-a-map"}
	if err := validateResultSchema(input, schema); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestValidateArrayItems_ObjectItemNotMap covers the
// `nested, ok := el.(map[string]any); if !ok { continue }` branch
// (submit_result.go:181-182). Items declared object, element is a string -
// element is silently skipped (checkType already errored for explicit
// type:"object" mismatch).
func TestValidateArrayItems_ObjectItemNotMap(t *testing.T) {
	// items has implicit object shape (properties without type) so
	// checkType is skipped; element is a string → ok==false branch fires.
	items := map[string]any{
		"properties": map[string]any{
			"x": map[string]any{"type": "number"},
		},
	}
	arr := []any{"not-a-map"}
	if err := validateArrayItems("tags", arr, items); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestValidateArrayItems_NestedArrayMissingItems covers the
// `subItems, ok := items["items"].(map[string]any); if !ok { continue }`
// branch (submit_result.go:195-196). Outer items declared array, but the
// inner `items` schema is missing → recursion is skipped silently.
func TestValidateArrayItems_NestedArrayMissingItems(t *testing.T) {
	items := map[string]any{
		"type": "array",
		// note: no inner "items" key
	}
	arr := []any{[]any{float64(1), float64(2)}}
	if err := validateArrayItems("matrix", arr, items); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestStripSubmitPrefix_NoPrefix covers the "prefix absent → return err"
// branch (submit_result.go:234).
func TestStripSubmitPrefix_NoPrefix(t *testing.T) {
	original := errors.New("plain error without the magic prefix")
	got := stripSubmitPrefix(original)
	if got != original {
		t.Errorf("stripSubmitPrefix(%q) = %q, want pointer-equal to original", original, got)
	}
}

// TestValidateResultSchema_NestedArrayOfArrays verifies double-nested
// array recursion: outer items=array, inner items=number. Input
// [[1, "bad"]] must error on the inner element.
func TestValidateResultSchema_NestedArrayOfArrays(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"matrix": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "number"},
				},
			},
		},
	}
	input := map[string]any{
		"matrix": []any{
			[]any{float64(1), "bad"},
		},
	}
	err := validateResultSchema(input, schema)
	if err == nil {
		t.Fatal("expected error on nested array element type mismatch")
	}
	if !strings.Contains(err.Error(), "matrix[0][1]") {
		t.Errorf("error = %v, want mention of 'matrix[0][1]'", err)
	}
}
