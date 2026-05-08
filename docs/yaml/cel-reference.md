---
title: CEL reference
description: 'zenflow uses CEL (Common Expression Language) for two fields:'
---

# CEL reference

zenflow uses [CEL (Common Expression Language)](https://github.com/google/cel-spec) for two fields:

- `step.condition` - a boolean expression. False means skip the step.
- `loop.until` - a boolean expression. True means stop the loop.
- `loop.forEach` - an expression returning an array (when not given as a static array literal).

CEL is implemented via [`cel-go`](https://github.com/google/cel-go). zenflow registers the variables listed below; no extra functions are exposed beyond CEL's standard library. CPU cost is bounded at evaluation time (`CostLimit(10000)`), so expressions complete in microseconds to a few milliseconds.

This page is a quick reference. For the full CEL language, see [the spec](https://github.com/google/cel-spec/blob/master/doc/langdef.md).

## Available variables

The variable set depends on where the expression appears.

### In `step.condition`

Evaluated after all `dependsOn` finish, before this step starts.

| Variable | Type | Description |
| --- | --- | --- |
| `steps.<id>.content` | string | Concatenated text from a completed dependency step. |
| `steps.<id>.status` | string | One of `completed`, `failed`, `skipped`, `cancelled`. |
| `steps.<id>.result` | object | Structured result of a completed dependency step. |

`content` and `result` (without the `steps.` prefix) are **not** in scope here - the step has not run yet.

### In `loop.until` and `loop.forEach`

Evaluated after each iteration (for `until`) or once at loop entry (for `forEach`). All variables below capture the just-completed iteration.

| Variable | Scope | Type | Description |
| --- | --- | --- | --- |
| `iteration` | repeat-until | int | Zero-based iteration number. |
| `index` | forEach | int | Zero-based iteration index. |
| `item` | forEach | dyn | Current element from the array. |
| `content` | single-step repeat-until | string | Text content of the step. |
| `result` | single-step repeat-until | object | Structured result of the step. |
| `status` | single-step repeat-until | string | Status of the step. |
| `steps.<id>.content` | multi-step loop | string | Inner step content. |
| `steps.<id>.status` | multi-step loop | string | Inner step status. |
| `steps.<id>.result` | multi-step loop | object | Inner step structured result. |

In **multi-step** loops (`loop.steps` present), address inner steps via `steps.<inner-id>` because the bare `content`/`result` are reserved for single-step bodies.

## Evaluation rules

- `condition` and `until` must evaluate to a **boolean**. A non-boolean result is an error and aborts the step or loop.
- `forEach` (when a string) must evaluate to an **array**. A non-array result fails the loop.
- An empty string is rejected for `condition` (`minLength: 1` in the schema). The same applies to `until`.
- CEL evaluation has no `context.Context`; it cannot be cancelled mid-expression. The `CostLimit(10000)` ceiling prevents pathological inputs.

## Common patterns

### Status checks

```yaml
condition: "steps.tests.status == 'completed'"
condition: "steps.scan.status != 'skipped'"
```

### Content substring match

```yaml
condition: "steps.review.content.contains('LGTM')"
condition: "steps.build.content.contains('error')"
```

### Structured result fields

```yaml
condition: "steps.scan.result.severity == 'critical'"
condition: "steps.classify.result.priority >= 3"
```

### Boolean composition

```yaml
condition: "steps.tests.status == 'completed' && steps.tests.content.contains('PASS')"
condition: "steps.audit.result.passed || steps.audit.result.warnings_only"
```

### List membership and existence quantifiers

```yaml
condition: "steps.scan.result.findings.exists(f, f.severity == 'critical')"
condition: "'auth' in steps.design.result.modules"
```

### Numeric comparison and arithmetic

```yaml
until: "result.score >= 95"
until: "iteration >= 3 && result.improvements < 2"
```

### forEach over a structured array

```yaml
loop:
  forEach: "steps.list-services.result.services"
```

### forEach over a filtered subset

```yaml
loop:
  forEach: "steps.scan.result.findings.filter(f, f.severity == 'critical')"
```

`filter` and `exists` are part of the CEL standard library; they take a lambda `(varname, predicate)` form.

## Worked example

A condition that combines status, structured result, and free-form content:

```yaml
agents:
  scanner:
    description: "Scans for issues."
    resultSchema:
      type: object
      required: [findings]
      properties:
        findings:
          type: array
          items:
            type: object
            properties:
              severity: { type: string }
              file: { type: string }

steps:
  - id: scan
    agent: scanner
    instructions: "Scan the repo."

  - id: deep-audit
    dependsOn: [scan]
    condition: |
      steps.scan.status == 'completed' &&
      steps.scan.result.findings.exists(f, f.severity == 'critical')
    instructions: "Deeply audit the critical findings."
```

## Limitations

- **No custom functions.** zenflow does not register additional CEL functions beyond the standard library that `cel-go` ships with. If you need string parsing, regular expressions, or HTTP, do that work in an agent step and surface the result via `submit_result`.
- **No mutation.** CEL is a pure expression language. You cannot assign, mutate, or call side-effectful operations.
- **No `context` propagation.** Cancellation is enforced by the cost limit, not by Go's `context.Context`.
- **Cost limit is fixed.** The evaluator caps cost at `10000` units. Pathological inputs (deeply nested comprehensions over large arrays) are rejected at runtime; keep expressions shallow.
- **String comparison is byte-wise.** Use `contains` for substring matching; CEL does not do regex.
- **Numeric types follow CEL rules.** Integer / double promotion is automatic; explicit casts (`int(...)`, `double(...)`) are available when you need to be precise.
- **Result shape is implementation-determined.** `steps.<id>.result` is the structured output produced by the step's `submit_result` call. If the agent has no `resultSchema`, `result` is empty - prefer `content` checks in that case.

For the full CEL language, including macros, comprehensions, and standard-library functions, see [`cel-spec/langdef.md`](https://github.com/google/cel-spec/blob/master/doc/langdef.md).
