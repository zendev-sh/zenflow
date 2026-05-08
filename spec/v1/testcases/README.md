# Conformance Test Suite

Language-agnostic test cases for validating zenflow workflow parsers.

## Test Format

Each test case is a YAML file with three fields:

```yaml
description: "What this test validates"
input: |
  name: test
  steps:
    - id: a
expected:
  valid: true
  step_count: 3
  topo_order: [a, b, c]
```

## Validation Layers

Test cases target two validation layers:

| Layer | What it catches | Tool |
|-------|----------------|------|
| **Schema** | Type errors, missing required fields, pattern violations, enum violations, negative values | JSON Schema validator (e.g., `ajv`) |
| **Validator** | Cycle detection, duplicate IDs, agent/dep references, mutual exclusion, nested loops, unknown version | Custom parser logic |

### Which layer catches which error?

| Error Code | Layer |
|------------|-------|
| `missing_name` | Schema (`required: ["name"]`) |
| `missing_steps` | Schema (`required: ["steps"]`) |
| `empty_steps` | Schema (`minItems: 1`) |
| `invalid_step_id` | Schema (`pattern`) |
| `agent_missing_description` | Schema (`required: ["description"]`) |
| `negative_value` | Schema (`minimum: 1`). Note: covers both negative values and zero -- any value below the minimum. |
| `duplicate_step` | Validator |
| `missing_agent` | Validator |
| `missing_dep` | Validator |
| `cycle` | Validator |
| `include_has_*` | Validator |
| `loop_missing_max_iterations` | Validator |
| `foreach_with_*` | Validator |
| `foreach_empty_array` | Validator |
| `untilagent_bad_ref` | Validator |
| `nested_loop_prohibited` | Validator |
| `unknown_version` | Validator |

A complete conformance test runner must implement both layers. Running only the JSON Schema will catch 6 of the 17 error categories. The remaining require custom validation logic.

## Running Tests

1. Parse `input` as a zenflow workflow document
2. Validate against `schema.json` (schema layer)
3. Run custom validation (validator layer)
4. Compare result against `expected`

For valid tests:
- `expected.valid: true`
- `expected.step_count`: verify step count after parsing
- `expected.topo_order`: exact topological order (linear workflows)
- `expected.topo_constraints`: partial ordering (parallel workflows)

For invalid tests:
- `expected.valid: false`
- `expected.error`: the specific error code that should be raised
