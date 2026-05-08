package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/zendev-sh/goai"
)

// SubmitResultToolDef returns a goai.Tool for the submit_result tool.
// If schema is nil, returns a zero-value goai.Tool (Name="").
func SubmitResultToolDef(schema map[string]any) goai.Tool {
	if schema == nil {
		return goai.Tool{}
	}
	params, err := json.Marshal(schema)
	if err != nil {
 // Fallback to generic object schema.
		params = json.RawMessage(`{"type":"object"}`)
	}
	return goai.Tool{
		Name: toolNameSubmitResult,
 // description emphasizes MANDATORY + FINAL + "only way to succeed"
 // because LLMs frequently finish text-only and skip this tool even with
 // strong prompt-level instructions. The retry path in AgentRunner.Run
 // is the reliable enforcement, but a clear description reduces the
 // retry rate.
		Description: "REQUIRED FINAL ACTION. You MUST call this tool exactly once as your last action to complete the task - it is the ONLY way to return a successful result. Do NOT return plain text; you must call submit_result with a JSON object matching the schema below. Calling this tool ends the conversation.",
		InputSchema: params,
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
 // No-op: submit_result is handled by SubmitResultHandler in AgentRunner.
			return "ok", nil
		},
	}
}

// SubmitResultHandler validates and extracts submit_result tool call arguments.
// It checks required fields and basic type conformance against the agent's ResultSchema.
type SubmitResultHandler struct {
	schema map[string]any
}

// NewSubmitResultHandler creates a handler that validates against the given schema.
func NewSubmitResultHandler(schema map[string]any) *SubmitResultHandler {
	return &SubmitResultHandler{schema: schema}
}

// Handle parses the submit_result arguments, validates against schema, and returns
// (result map, terminated bool, error).
// terminated is always true on success (submit_result terminates the conversation).
func (h *SubmitResultHandler) Handle(args json.RawMessage) (map[string]any, bool, error) {
	var result map[string]any
	if err := json.Unmarshal(args, &result); err != nil {
		return nil, false, fmt.Errorf("submit_result: invalid JSON: %w", err)
	}

	// Validate required fields from schema.
	if h.schema != nil {
		if err := validateResultSchema(result, h.schema); err != nil {
			return nil, false, err
		}
	}

	return result, true, nil
}

// validateResultSchema performs validation of result against schema,
// recursively descending into nested object/array property definitions.
// Checks: required fields present at every level, basic type checking,
// nested-object required+type validation, and array item validation.
// The recursion is bounded by the schema depth (caller-supplied) and
// follows JSON Schema draft conventions: object properties recurse via
// "properties"/"required"; arrays recurse via "items".
func validateResultSchema(result map[string]any, schema map[string]any) error {
	// Check required fields at this level.
	if req, ok := schema["required"]; ok {
		required, ok := toStringSlice(req)
		if !ok {
			return fmt.Errorf("submit_result: schema 'required' is not a string array (got %T)", req)
		}
		for _, field := range required {
			if _, exists := result[field]; !exists {
				return fmt.Errorf("submit_result: missing required field %q", field)
			}
		}
	}

	// Check property types and recurse into nested object/array schemas.
	if props, ok := schema["properties"].(map[string]any); ok {
		for field, propDef := range props {
			val, exists := result[field]
			if !exists {
				continue // not required, skip
			}
			propSchema, ok := propDef.(map[string]any)
			if !ok {
				continue
			}
			expectedType, _ := propSchema["type"].(string)
			if expectedType != "" {
				if err := checkType(field, val, expectedType); err != nil {
					return err
				}
			}
 // Recurse into nested object: validate its required + properties
 // against the nested map. checkType above already proved val is
 // map[string]any when expectedType=="object"; the type-assertion
 // here is defensive (e.g. when expectedType is missing but the
 // schema still nests "properties").
			if expectedType == "object" || hasObjectShape(propSchema) {
				nested, ok := val.(map[string]any)
				if !ok {
 // If the schema declared an object shape but val is not
 // an object, fall through - checkType already errored
 // above for explicit type=="object" mismatches; for
 // implicit shapes we do not synthesize a type error.
					continue
				}
				if err := validateResultSchema(nested, propSchema); err != nil {
					return err
				}
			}
 // Recurse into array items: each element must satisfy the
 // "items" schema. JSON Schema allows "items" to be either an
 // object (single schema applied to every element) or an array
 // (positional). We only support the single-schema form here -
 // matches the producer side (LLM tool calls almost always emit
 // homogeneous arrays).
 // Skip the val.([]any) double-check that earlier rounds had
 // here: when expectedType=="array" we have already passed
 // checkType above (which returns the same []any assertion),
 // so val IS []any by construction.
			if expectedType == "array" {
				arr := val.([]any)
				items, ok := propSchema["items"].(map[string]any)
				if !ok {
					continue
				}
				if err := validateArrayItems(field, arr, items); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// hasObjectShape reports true when schema describes an object via the
// presence of "properties" or "required" even if "type" is missing.
// Permits schemas that omit redundant type tags.
func hasObjectShape(schema map[string]any) bool {
	if _, ok := schema["properties"].(map[string]any); ok {
		return true
	}
	if _, ok := schema["required"]; ok {
		return true
	}
	return false
}

// validateArrayItems validates each element of arr against the items
// schema. Element index is included in the error path so callers can
// pinpoint which element failed (e.g. `field "tags[2]"`).
func validateArrayItems(field string, arr []any, items map[string]any) error {
	itemType, _ := items["type"].(string)
	for i, el := range arr {
		elField := fmt.Sprintf("%s[%d]", field, i)
 // Per-element type check first.
		if itemType != "" {
			if err := checkType(elField, el, itemType); err != nil {
				return err
			}
		}
 // Recurse into nested object items.
		if itemType == "object" || hasObjectShape(items) {
			nested, ok := el.(map[string]any)
			if !ok {
				continue
			}
			if err := validateResultSchemaWithPrefix(nested, items, elField); err != nil {
				return err
			}
		}
 // Recurse into nested arrays. When itemType=="array" the
 // per-element checkType above has already proven el is []any,
 // so the assertion is guaranteed to succeed.
		if itemType == "array" {
			subArr := el.([]any)
			subItems, ok := items["items"].(map[string]any)
			if !ok {
				continue
			}
			if err := validateArrayItems(elField, subArr, subItems); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateResultSchemaWithPrefix is validateResultSchema with a path
// prefix prepended to required-field and type-mismatch errors so array
// items report which element failed (e.g. `tags[2].name`). The prefix
// is only used to rewrite error messages emitted by validateResultSchema
// itself; deeper recursion still uses the unprefixed path because
// validateArrayItems handles its own prefixing.
func validateResultSchemaWithPrefix(result map[string]any, schema map[string]any, prefix string) error {
	err := validateResultSchema(result, schema)
	if err == nil {
		return nil
	}
	// Lightweight rewrite: prepend the prefix to the field name in the
	// error message. Callers rely on errors.Is for sentinel detection
	// (none here) and string formatting for human display, so a literal
	// prefix is acceptable.
	return fmt.Errorf("submit_result: in %q: %w", prefix, stripSubmitPrefix(err))
}

// stripSubmitPrefix removes the leading "submit_result: " from an error
// message so wrapping does not produce "submit_result: in foo[0]:
// submit_result: missing ...". Best-effort: returns the original error
// when the prefix is absent.
func stripSubmitPrefix(err error) error {
	const p = "submit_result: "
	msg := err.Error()
	if len(msg) > len(p) && msg[:len(p)] == p {
		return errors.New(msg[len(p):])
	}
	return err
}

func checkType(field string, val any, expectedType string) error {
	switch expectedType {
	case "boolean":
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("submit_result: field %q must be boolean, got %T", field, val)
		}
	case "string":
		if _, ok := val.(string); !ok {
			return fmt.Errorf("submit_result: field %q must be string, got %T", field, val)
		}
	case "number":
		if _, ok := val.(float64); !ok {
			return fmt.Errorf("submit_result: field %q must be number, got %T", field, val)
		}
	case "integer":
		v, ok := val.(float64)
		if !ok {
			return fmt.Errorf("submit_result: field %q must be integer, got %T", field, val)
		}
		if v != math.Trunc(v) || math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("submit_result: field %q must be integer, got %v", field, v)
		}
 // Reject values beyond float64 safe integer range (2^53).
		const safeInt = 1 << 53
		if v > safeInt || v < -safeInt {
			return fmt.Errorf("submit_result: field %q integer value %v exceeds safe float64 precision range", field, v)
		}
	case "object":
		if _, ok := val.(map[string]any); !ok {
			return fmt.Errorf("submit_result: field %q must be object, got %T", field, val)
		}
	case "array":
		if _, ok := val.([]any); !ok {
			return fmt.Errorf("submit_result: field %q must be array, got %T", field, val)
		}
	case "null":
		if val != nil {
			return fmt.Errorf("submit_result: field %q must be null, got %T", field, val)
		}
	}
	return nil
}

func toStringSlice(v any) ([]string, bool) {
	switch arr := v.(type) {
	case []string:
		return arr, true
	case []any:
		result := make([]string, 0, len(arr))
		for _, item := range arr {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			result = append(result, s)
		}
		return result, true
	}
	return nil, false
}
