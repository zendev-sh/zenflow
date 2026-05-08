---
title: Conditions
description: A step's condition field carries a CEL expression. The expression is evaluated before the step runs. If it evaluates to false, the step is skipped...
---

# Conditions

A step's `condition` field carries a CEL expression. The expression is evaluated before the step runs. If it evaluates to false, the step is skipped - its status becomes `skipped` and its content / result are empty.

Conditions are how zenflow expresses "do this only if a previous step said so". They keep the DAG static (the structure is known at parse time) while letting individual nodes opt out at run time.

## Syntax

```yaml
- id: security_audit
  agent: security
  instructions: "Audit the authentication implementation for vulnerabilities."
  dependsOn: [implement]
  condition: "steps.design.result.features.exists(f, f == 'authentication')"
```

The `condition` value is a string holding a CEL (Common Expression Language) expression. The expression must evaluate to a boolean. Empty strings are rejected at parse time.

CEL is documented at [the CEL spec](https://github.com/google/cel-spec). Zenflow uses the standard CEL feature set plus a small set of builtin variables.

## Available variables

| Variable | Type | Description |
|----------|------|-------------|
| `steps.<id>.content` | string | Text content of a completed dependency step. |
| `steps.<id>.status` | string | One of `"completed"`, `"failed"`, `"skipped"`, `"cancelled"`. |
| `steps.<id>.result` | object | Structured `result` (a `map[string]any`) from a completed dependency step. |

Only steps in the `dependsOn` chain (transitively) are visible. A step cannot reference steps it does not depend on; the validator catches it.

The bare `content` variable (without `steps.` prefix) is not available in condition scope because the condition evaluates before the step runs. Inside loops, `content` and `result` are available - see [Loops](/concepts/loops).

## Evaluation semantics

1. The condition is evaluated after every `dependsOn` step has finished.
2. The expression is executed exactly once per step.
3. If the result is `false`, the step transitions to `skipped` and never starts.
4. If the result is `true`, the step starts.
5. If the expression references a missing field or non-existent key (a "no such key" / "no such attribute" / "undefined field" error at evaluation time), the step is skipped (treated as if the condition were false). If the expression has a type mismatch or compile error, the step transitions to `failed` with a CEL error (treated as a workflow author bug).

A skipped step's status `"skipped"` propagates to dependents through `steps.<id>.status`. Dependents see it and decide whether to run themselves, possibly with their own condition.

## Conditions and loops

A step may have both `condition` and `loop`. The condition is evaluated first. If false, the entire loop is skipped (no iterations run). If true, the loop begins normally.

```yaml
- id: deploy-each
  dependsOn: [list-services]
  condition: "size(steps.list-services.result.services) > 0"
  loop:
    forEach: "steps.list-services.result.services"
    steps:
      - id: deploy
        agent: deployer
        instructions: "Deploy this service."
```

Without the condition, an empty `services` array would still trigger the forEach (with zero iterations) - fine, but noisy. The condition makes the skip explicit.

## Worked example

```yaml
name: condition-example
agents:
  designer:
    description: "Software architect."
    resultSchema:
      type: object
      required: [features]
      properties:
        features:
          type: array
          items:
            type: string
  developer:
    description: "Developer."
    tools: [write, bash]
  security:
    description: "Security engineer."
    tools: [read]
  tester:
    description: "Tester."
    tools: [write, bash]
    resultSchema:
      type: object
      required: [passed]
      properties:
        passed:
          type: boolean
        summary:
          type: string
  optimizer:
    description: "Performance engineer."
    tools: [read, bash]

steps:
  - id: design
    agent: designer
    instructions: "Design the user management API. Include authentication if user data is involved."

  - id: implement
    agent: developer
    dependsOn: [design]
    instructions: "Implement the API based on the design."

  - id: security_audit
    agent: security
    dependsOn: [implement]
    condition: "steps.design.result.features.exists(f, f == 'authentication')"
    instructions: "Audit the authentication for vulnerabilities."

  - id: test
    agent: tester
    dependsOn: [implement]
    instructions: "Write and run unit tests. Submit passed=true if all green."

  - id: optimize
    agent: optimizer
    dependsOn: [test]
    condition: "steps.test.status == 'completed' && steps.test.result.passed == true"
    instructions: "Profile and optimize the hot paths."

  - id: finalize
    agent: designer
    dependsOn: [security_audit, optimize]
    instructions: "Generate release notes."
```

What happens at runtime:

1. **`design`** runs. Designer calls `submit_result({features: ["user-profiles", "authentication"]})`. Step completes.
2. **`implement`** runs (only dependency `design` is done).
3. **`security_audit`**'s condition is evaluated:
   - `steps.design.result.features.exists(f, f == 'authentication')` → `true` (the array contains `"authentication"`).
   - Step starts.
4. **`test`** runs in parallel with `security_audit` (both depend only on `implement`).
5. **`optimize`**'s condition is evaluated after `test` finishes:
   - `steps.test.status == 'completed' && steps.test.result.passed == true` → depends on what tester reported.
   - If tests passed, `optimize` runs. If tests failed (`result.passed == false` or `status == 'failed'`), `optimize` is skipped.
6. **`finalize`** runs after both `security_audit` and `optimize` reach a terminal status. If either was skipped, that is fine - `finalize` does not condition on their success.

## Common patterns

### Skip when a feature is absent

```yaml
condition: "steps.design.result.features.exists(f, f == 'authentication')"
```

Use `exists` for "any element matches". Use `all` for "every element matches". Both are CEL builtins.

### Skip when a flag is false

```yaml
condition: "steps.precheck.result.should_proceed"
```

CEL truthiness: a missing field, null, false, zero, or empty value evaluates to false. A present truthy value evaluates to true.

### Skip when an upstream failed

```yaml
condition: "steps.test.status == 'completed'"
```

Without this, the step would still try to run if `test` is `failed` (depending on the workflow's `onStepFailure` strategy). With it, downstream steps gracefully skip on upstream failure.

### Skip based on text content

```yaml
condition: "steps.review.content.contains('LGTM')"
```

CEL strings have `contains`, `startsWith`, `endsWith`, `matches` (regex). Useful when an upstream agent does not produce a `result` and you have to read the free-form text.

### Combining conditions

```yaml
condition: "steps.test.status == 'completed' && (steps.test.result.passed == true || steps.config.result.allow_skip == true)"
```

CEL supports `&&`, `||`, `!`, and parentheses. Keep conditions readable - if the expression is more than two lines, split into a separate gating step that does the logic and returns a boolean in its `result`.

## Why CEL

CEL is fast, sandboxed, and has no side effects. The same expression cannot read the file system, make a network call, or mutate state. That keeps conditions fully predictable and parseable: zenflow can analyse them at parse time to verify only known step IDs are referenced.

The validator catches many common errors at parse time:

- Referenced step IDs that do not exist.
- References to step IDs that are not in the dependency chain.
- Empty condition strings.

Type mismatches (treating a string as a number, indexing a map with a wrong key) only surface at run time.

## What conditions cannot do

- **Restructure the DAG.** A condition can skip a node, not add or remove edges. The graph is fixed at parse time.
- **Loop or branch arbitrarily.** Use loops for repetition and conditions for one-time skips. Multi-way branching needs separate steps with mutually exclusive conditions.
- **Read external state.** No file I/O, no environment variables, no time. Use a precondition step whose agent reads the state and emits a structured `result` for the next step's condition to read.

## Cross-links

- [DAG scheduling](/concepts/dag-scheduling) - how skip status propagates to dependents
- [Failure handling](/concepts/failure-handling) - `skip-dependents` strategy interaction
- [Loops](/concepts/loops) - conditions on loop steps
- [YAML: CEL Reference](/yaml/cel-reference) - full list of CEL operators and functions zenflow supports
