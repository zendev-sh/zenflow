---
title: Step
description: Steps are the nodes of the workflow DAG. Each step represents one task assigned to an agent (or delegated to a sub-workflow via include). This...
---

# Step

Steps are the nodes of the workflow DAG. Each step represents one task assigned to an agent (or delegated to a sub-workflow via `include`). This page documents every `Step` field exhaustively.

Authoritative source: [`spec/v1/spec.md` §4 and §7](https://github.com/zendev-sh/zenflow/blob/main/spec/v1/spec.md). Structural contract: [`spec/v1/schema.json` `$defs.Step`](https://github.com/zendev-sh/zenflow/blob/main/spec/v1/schema.json).

## Field summary

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `id` | string | yes | Unique within scope. Pattern `^[a-zA-Z][a-zA-Z0-9_-]*$`. |
| `agent` | string | no | Reference to an entry in the workflow's `agents` map. |
| `instructions` | string | no | Task instructions. Supports `@file` references. |
| `dependsOn` | array[string] | no | Step IDs that must finish before this step starts. |
| `contextFiles` | array[string] | no | File paths whose contents are injected into the agent context. Paths are relative to the workflow file. |
| `model` | string | no | Override model for this step. Beats `agent.model`. |
| `timeout` | Duration | no | Step (or whole-loop) timeout. |
| `retries` | integer | no | Retry attempts on failure. `minimum: 0`. |
| `maxRetries` | integer | no | Per-step cap on the agent runner's tool-call retry budget (passed via `goai.WithMaxRetries`). Distinct from `retries`, which retries the whole step. `minimum: 0`. Falls back to `options.maxRetries`, then the orchestrator default. |
| `condition` | string | no | CEL expression. Step is skipped when false. |
| `include` | string | no | Sub-workflow reference. Mutually exclusive with several other fields. |
| `loop` | Loop | no | Loop configuration. See [Loop](./loop). |

`additionalProperties: false`. Unknown step-level fields are rejected.

## id

Required. Unique within the same scope - top-level steps share one namespace, each `loop.steps` array forms its own, and each included sub-workflow gets a namespaced one.

The pattern is `^[a-zA-Z][a-zA-Z0-9_-]*$`: letter first, then letters/digits/underscores/hyphens. Dots and brackets are reserved for namespacing (`forEach` produces `step[0]` indices; includes produce `parent.inner`).

> The parser additionally enforces a strict pattern `^[a-z][a-z0-9_-]{0,63}$` post-schema: lowercase only, max 64 chars.

```yaml
steps:
  - id: design
  - id: implement-api
  - id: run_tests
```

The validator rejects:

- IDs starting with a digit.
- IDs containing `.` or `[` (reserved).
- Duplicate IDs in the same scope.
- `dependsOn` references that do not match a sibling ID.

## agent

Reference to an entry in the workflow's `agents` map. Optional - a step without `agent` runs through the executor's default agent (uses the orchestrator's default model).

```yaml
agents:
  planner:
    description: "Technical lead."

steps:
  - id: plan
    agent: planner
    instructions: "..."

  - id: greet
    instructions: "Say hi."   # uses default agent
```

The validator rejects an `agent` value that does not appear as a key in `agents`.

## instructions

Task instructions delivered to the agent as the user message for this step. Supports the `@file` convention - a value starting with `@` is read from disk relative to the workflow file.

```yaml
steps:
  - id: literal
    instructions: "Summarize the design doc in three bullets."

  - id: from-file
    agent: planner
    instructions: "@instructions/plan-feature.md"
```

When a step has dependencies, the engine prepends the dependency outputs to the agent context automatically; `instructions` does not need template syntax to reference upstream content. See [Concepts / DAG Scheduling](/concepts/dag-scheduling) for the data-passing rules.

Maximum length: 2000 chars (`MaxDescriptionChars`).

## dependsOn

Array of step IDs that must complete before this step starts. Defines the DAG edges.

```yaml
steps:
  - id: design
    instructions: "Design the REST API."

  - id: api-server
    dependsOn: [design]
    instructions: "Implement the API server."

  - id: database
    dependsOn: [design]
    instructions: "Implement the database layer."

  - id: integrate
    dependsOn: [api-server, database]
    instructions: "Wire them together."
```

`api-server` and `database` execute in parallel after `design`. `integrate` waits for both.

Rules:

- All entries must be sibling IDs in the same scope. Cross-namespace references (a top-level step reaching into a `loop.steps` ID, or vice versa) are rejected.
- Cycles are rejected per scope (top-level DAG, each `loop.steps` sub-DAG, each included sub-workflow).
- Dependencies on `skipped` or `failed` steps still count as "finished" for ordering; whether dependents run depends on `options.onStepFailure` and any `condition` they carry.

## condition

CEL expression evaluated before the step runs. If it evaluates to `false`, the step is skipped (status `skipped`, no agent invocation). The expression must evaluate to a boolean; a non-boolean result is an error.

```yaml
steps:
  - id: scan
    instructions: "Scan the codebase for risky patterns."

  - id: deep-audit
    dependsOn: [scan]
    instructions: "Deep audit the auth module."
    condition: "steps.scan.result.severity == 'critical'"
```

Variables available in `condition`:

| Variable | Type | Source |
| --- | --- | --- |
| `steps.<id>.content` | string | Concatenated text from the dependency step. |
| `steps.<id>.status` | string | `completed`, `failed`, `skipped`, `cancelled`. |
| `steps.<id>.result` | object | Structured result (`map[string]any`) of the dependency step. |

`condition` evaluates after all `dependsOn` finish but before this step starts. The variables `content` and `result` (without the `steps.` prefix) are not available in this scope - they only appear inside `loop.until` for single-step repeat-until loops. See [CEL reference](./cel-reference) for the full surface.

If a step has both `condition` and `loop`, condition runs first. A false condition skips the entire loop.

## timeout

Step timeout. Format follows the [Duration grammar](./workflow#duration-format) - `30m`, `1h30m`, `45s`, etc. When the step has a `loop`, the timeout applies to all iterations combined, not per iteration.

```yaml
steps:
  - id: slow-step
    instructions: "Long-running task."
    timeout: "10m"
```

If `options.stepTimeout` is set on the workflow, it provides the default; a step-level `timeout` overrides it for that step.

> Per-step `timeout` is part of the v1 schema and the executor honors it. The values you pick should reflect your model's typical response time and your tolerance for cancellation; do not blindly copy a value from another workflow.

## retries

Number of retry attempts on failure. `minimum: 0`. The executor re-runs the step from the beginning each time (and the entire loop block, if there is one).

Retry budget interaction with `timeout` depends on whether the step carries a `loop`:

- **Non-loop steps**: all retry attempts share one `timeout` budget. A single `context.WithTimeout` is created once and reused across every attempt - if the first attempt burns most of the budget, subsequent retries get whatever remains and may be cancelled mid-flight.
- **Loop steps**: each retry of the loop block gets a fresh `timeout` budget. The loop's iterations are bounded but the per-retry deadline restarts on every retry, so a loop that timed out can re-enter with a full timeout window on the next attempt.

```yaml
steps:
  - id: flaky
    instructions: "Call the unreliable API."
    retries: 2
    timeout: "30s"
```

> `retries` is part of the v1 schema. Whether a retry is the right call depends on the failure mode - retries help with transient provider errors but mask deterministic bugs. Prefer fixing the underlying flake.

## contextFiles

Array of file paths whose contents are injected into the agent context for this step. Paths are relative to the workflow file's directory. Unlike `instructions` and `prompt`, `contextFiles` does **not** use the `@` prefix - the values are always paths.

```yaml
steps:
  - id: review-design
    agent: reviewer
    instructions: "Review the design doc."
    contextFiles:
      - "docs/architecture.md"
      - "docs/api-spec.yaml"
```

> `contextFiles` is part of the v1 schema. The injection format is implementation-defined. Each entry is capped at 10 MiB (`MaxAttachmentSizeBytes`).

## model

Override the model for just this step. Takes precedence over `agent.model` and the orchestrator default. Useful when one step in a workflow needs a different model than the rest.

```yaml
agents:
  worker:
    description: "Default worker."
    model: "gemini-2.5-flash"

steps:
  - id: quick
    agent: worker
    instructions: "Fast classification task."

  - id: deep
    agent: worker
    model: "bedrock/anthropic.claude-sonnet-4-6"   # override for this step
    instructions: "Hard reasoning task."
```

The CLI's `--model` flag overrides every `agent.model` and `step.model` (it nukes those fields after loading the workflow), letting you test cross-provider compatibility from one binary.

## include

Reference to a sub-workflow. The value is either a name from the top-level `includes` map, or a YAML file path relative to the current workflow file. The reference is resolved by the parser.

```yaml
includes:
  deploy: "workflows/deploy.yaml"

steps:
  - id: build
    instructions: "Build the binary."

  - id: deploy-staging
    include: deploy                        # named reference
    dependsOn: [build]

  - id: deploy-prod
    include: "workflows/deploy.yaml"       # direct path
    dependsOn: [deploy-staging]
```

A step with `include` may **not** carry these fields:

- `agent`
- `instructions`
- `loop`
- `condition`
- `contextFiles`
- `model`

This is a validator-enforced mutual exclusion (the JSON Schema cannot express it). The include step delegates the work entirely to the sub-workflow. To gate an `include` step, place a `condition` on a wrapper step that depends on it - or push the gate into the sub-workflow itself.

A step with `include` may carry:

- `dependsOn` - the whole sub-workflow waits for the named upstream steps.
- `timeout` - bounds the entire sub-workflow execution.
- `retries` - retries the sub-workflow from the beginning.

Sub-workflow agents merge into the parent scope (name collisions are rejected). Sub-workflow step IDs are namespaced as `{parent-step-id}.{inner-step-id}` (e.g., `deploy-staging.run-tests`). Recursive includes are hard-capped at depth 5 (`MaxIncludeDepth` in `limits.go`). The separate `MaxNestingDepth = 20` constant applies to `@`-reference chain depth, NOT include nesting.

## loop

Loop configuration. Two mutually exclusive modes: **repeat-until** (sequential iterations until a stop condition) and **forEach** (parallel map over an array). See [Loop](./loop) for the full field set.

When `loop` is present without `forEach`, `loop.maxIterations` is required. When `forEach` is present, `maxIterations` / `until` / `untilAgent` / `delay` must all be absent. Inner steps inside `loop.steps` must not carry their own `loop` - nested loops are not supported in v1.

```yaml
steps:
  - id: deploy-each
    dependsOn: [list-services]
    loop:
      forEach: "steps.list-services.result.services"
      maxConcurrency: 3
      steps:
        - id: deploy
          agent: deployer
          instructions: "Deploy this service."
```

## Output model

Every step produces output on two channels:

| Channel | Type | Source |
| --- | --- | --- |
| `content` | string | Concatenated text from all agent turns. Free-form markdown. Human-readable. |
| `result` | `map[string]any` or nil | Arguments of the successful `submit_result` call. Structured. Validated against the agent's `resultSchema` when defined. |

Dependent steps see both channels via CEL: `steps.<id>.content` and `steps.<id>.result`. Steps without an agent `resultSchema` produce `content` only; their `result` is nil.

When the agent has a `resultSchema`, `submit_result` is the **only** way to produce structured output. There is no JSON parsing of free-form text. Calling `submit_result` with valid arguments terminates the conversation immediately. Invalid arguments produce a tool error and the conversation continues, letting the agent retry.

For the full edge-case table (`submit_result` never called, parallel calls, side effects), see [`spec.md` §4 Output Model](https://github.com/zendev-sh/zenflow/blob/main/spec/v1/spec.md#output-model).

## Worked examples

### Linear chain

```yaml
name: simple-chain
steps:
  - id: design
    agent: architect
    instructions: "Design the API."

  - id: implement
    agent: coder
    dependsOn: [design]
    instructions: "Build it."

  - id: review
    agent: reviewer
    dependsOn: [implement]
    instructions: "Review the implementation."
```

### Parallel fan-out

```yaml
name: parallel-fan-out
steps:
  - id: design
    agent: architect
    instructions: "Design the system."

  - id: api
    agent: builder
    dependsOn: [design]
    instructions: "Build the API."

  - id: db
    agent: builder
    dependsOn: [design]
    instructions: "Build the database."

  - id: ui
    agent: builder
    dependsOn: [design]
    instructions: "Build the UI."

  - id: integrate
    agent: integrator
    dependsOn: [api, db, ui]
    instructions: "Wire them together."

options:
  maxConcurrency: 3
```

### Conditional branch

```yaml
name: conditional-audit
agents:
  scanner:
    description: "Scanner agent."
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

steps:
  - id: scan
    agent: scanner
    instructions: "Scan for issues."

  - id: deep-audit
    dependsOn: [scan]
    condition: "steps.scan.result.findings.exists(f, f.severity == 'critical')"
    instructions: "Deeply audit the critical findings."
```
