# Zenflow Workflow Specification v1

## 1. Introduction

Zenflow is a declarative format for defining multi-agent workflow DAGs. A zenflow workflow document describes agents (LLM configurations), steps (tasks forming a directed acyclic graph), and execution options. The executor schedules steps based on dependency edges, runs them through LLM agents, and passes outputs between steps automatically.

This specification defines the document schema. It does not define runtime behavior (scheduling algorithms, prompt assembly, storage, permission handling). Those are implementation concerns.

**Audience**: developers implementing a zenflow parser, validator, or executor in any language.

**Machine-readable schema**: `schema.json` in this directory is the JSON Schema (Draft 2020-12) source of truth. This document provides prose explanation, examples, and validation rules that go beyond what JSON Schema can express.

---

## 2. Document Structure

A zenflow document is a YAML or JSON file with these top-level fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Workflow name. Must be non-empty (`minLength: 1`). |
| `description` | string | No | Human-readable purpose of the workflow. |
| `version` | integer | No | Schema version. Must be >= 1. Currently only `1` is defined. Defaults to `1` if omitted. |
| `agents` | map[string, AgentConfig] | No | Named agent definitions. Keys are agent names referenced by steps. |
| `includes` | map[string, string] | No | Named sub-workflow registry. See [Section 7](#7-includes). |
| `steps` | array[Step] | Yes | Ordered list of steps forming a DAG. Must contain at least one step. |
| `options` | Options | No | Execution configuration. See [Section 8](#8-options). |

Unknown fields are rejected (`additionalProperties: false`).

**Minimal example** (YAML):

```yaml
name: minimal
steps:
  - id: greet
    instructions: "Say hello."
```

**Same document in JSON**:

```json
{
  "name": "minimal",
  "steps": [
    {
      "id": "greet",
      "instructions": "Say hello."
    }
  ]
}
```

---

## 3. Agents

The `agents` section defines named LLM configurations that steps reference. Agents are optional -- steps without an `agent` field use a default agent provided by the executor.

### AgentConfig Fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `description` | string | Yes | -- | Human-readable description of the agent's role. |
| `prompt` | string | No | -- | System prompt. Supports `@` file references (see [Section 9](#9-file-references)). |
| `model` | string | No | -- | LLM model identifier. Free-form string. Bare (`claude-sonnet-4-6`) and provider-prefixed (`bedrock/anthropic.claude-sonnet-4-6`, `google/gemini-3-pro-preview`, `azure/gpt-5`, `vertex/...`) forms are valid. The CLI's auto-router does not accept the bare `anthropic/` prefix. Resolution is implementation-defined. |
| `tools` | array[string] | No | -- | Tool allowlist. Omit to allow every registered tool. List explicit names to restrict. Wildcard tool names like `"*"` are not currently expanded -- they are parsed as literal tool identifiers that match nothing. Use the explicit tool list. |
| `disallowedTools` | array[string] | No | -- | Tool denylist. Applied after the allowlist. |
| `maxTurns` | integer | No | 50 (orchestrator default) | Maximum LLM conversation turns. Minimum: 0. Setting `0` is equivalent to omitting (use the default of 50). Negative values are rejected. |
| `temperature` | number | No | -- | Sampling temperature. Range: 0 to 2. |
| `topP` | number | No | -- | Nucleus sampling parameter. Range: 0 to 1. |
| `resultSchema` | object | No | -- | JSON Schema defining the shape of the agent's structured `result` output. Compiled into a `submit_result` tool at runtime -- agent calls this tool to produce structured output. The tool's input parameters = this schema. Free-form JSON Schema object. The `schema.json` validates that `resultSchema` is a JSON object, but does not verify it is a valid JSON Schema. Implementations must validate `resultSchema` against JSON Schema meta-schema at parse time or document time. |

### Tool Filtering

The `tools` and `disallowedTools` fields control which tools an agent can use:

- If `tools` is absent, tool availability is implementation-defined.
- Wildcard tool names like `"*"` are not currently expanded -- they are parsed as literal tool identifiers that match nothing. To allow every registered tool, omit the `tools` field entirely.
- If `tools` is a list of names, only those tools are allowed.
- `disallowedTools` removes tools from the resolved allowlist.

The effective tool set is: `(allowlist) - (denylist)`.

```yaml
agents:
  planner:
    description: "Technical lead who creates implementation plans."
    prompt: "@prompts/planner.md"
    model: "bedrock/anthropic.claude-sonnet-4-6"
    # tools omitted -> every registered tool is available
    disallowedTools:
      - bash
    maxTurns: 10
    temperature: 0.3
```

---

## 4. Steps

Steps are the nodes of the workflow DAG. Each step represents a task assigned to an agent.

### Step Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | Yes | Unique identifier. Schema pattern: `^[a-zA-Z][a-zA-Z0-9_-]*$` (loose). Parser additionally enforces the strict pattern `^[a-z][a-z0-9_-]{0,63}$` (lowercase letters/digits/underscore/hyphen, max 64 chars) post-schema. Dots and brackets are reserved for namespacing (see Sections 6 and 7). Coordinator-generated workflows (e.g., `RunGoal` output) are stricter still and reject hyphens for CEL-identifier safety. |
| `agent` | string | No | Reference to an agent name defined in the `agents` section. |
| `instructions` | string | No | Task instructions. Supports `@` file references. |
| `dependsOn` | array[string] | No | Step IDs that must complete before this step starts. |
| `contextFiles` | array[string] | No | File paths whose contents are injected into the agent context. Paths are relative to the workflow file directory. |
| `model` | string | No | Override model for this step. Takes precedence over the agent's model. |
| `timeout` | Duration | No | Timeout for the step. If the step has a `loop`, applies to all iterations combined. |
| `retries` | integer | No | Retry attempts on failure. Minimum: 0. |
| `maxRetries` | integer | No | Per-step cap on the agent runner's tool-call retry budget (passed via `goai.WithMaxRetries`). Distinct from `retries` (which retries the whole step). Minimum: 0. Falls back to the workflow-level `options.maxRetries` and finally the orchestrator default. |
| `condition` | string | No | CEL expression. Step is skipped when false. See [Section 5](#5-conditions). |
| `include` | string | No | Sub-workflow reference. See [Section 7](#7-includes). |
| `loop` | Loop | No | Loop configuration. See [Section 6](#6-loops). |

### Dependency Model

The `dependsOn` array defines edges in the DAG. A step starts only after all its dependencies have completed (status `completed`, `skipped`, or `failed` depending on the failure strategy).

Steps with no dependencies and no unmet prerequisites start immediately, subject to concurrency limits.

```yaml
steps:
  - id: design
    agent: architect
    instructions: "Design the REST API."

  - id: api-server
    agent: backend
    instructions: "Implement the API server."
    dependsOn: [design]

  - id: database
    agent: backend
    instructions: "Implement the database layer."
    dependsOn: [design]

  - id: integrate
    agent: integrator
    instructions: "Wire components together."
    dependsOn: [api-server, database]
```

In this example, `api-server` and `database` run in parallel after `design` completes. `integrate` waits for both.

### Execution Order Guarantees

1. A step never starts before all its `dependsOn` steps have finished.
2. Steps with no dependency relationship may execute in any order, including in parallel.
3. The array order of `steps` has no semantic meaning for execution. It is a convenience for human readers.

### Data Passing

Dependency step outputs are automatically injected into the dependent step's agent context. No explicit template syntax is needed. The injection format is implementation-defined.

### Output Model

Every step produces output with two channels:

| Channel | Type | Source | Purpose |
|---------|------|--------|---------|
| **content** | string | Concatenated text from all agent turns | Free-form text (markdown). Human-readable. Injected into dependent steps' context. Displayed to users. |
| **result** | `map[string]any` | `submit_result` tool call arguments | Structured JSON object. Machine-readable. Used by CEL expressions, executor protocols (e.g., `untilAgent`), and consumers. Validated against `resultSchema` if defined on the agent. |

**How result is produced**: When an agent's `resultSchema` is defined, the executor auto-injects a `submit_result` tool into the agent's tool list. The tool's input schema is the agent's `resultSchema`. When the agent calls `submit_result({...})`:

1. The executor validates the arguments against `resultSchema`, including nested objects (recursive `properties`/`required`) and array elements (per-element `items` schema). Validation errors include the JSON path of the offending field (e.g., `tags[2].name`).
2. **If valid**: arguments become the step's `result`, tool returns `{"status": "ok"}`, conversation loop **terminates immediately**.
3. **If invalid**: tool returns `{"status": "error", "message": "<validation details>"}`, conversation **continues** -- the agent sees the error and can retry with corrected arguments.

`content` = concatenated text from all turns up to the successful `submit_result` call.

This is the only mechanism for producing structured result -- no text parsing, no GenerateObject, no dual LLM calls.

**Edge cases**:
- **Multiple valid calls**: Impossible -- the loop terminates on first valid call.
- **Invalid then valid**: Expected flow. Agent calls `submit_result` with bad args, gets error, retries with fixed args. Second call succeeds and terminates.
- **Parallel tool calls**: If the LLM emits multiple `submit_result` calls in one turn, the executor takes the first valid one and ignores the rest.
- **Never called**: If the agent exhausts `maxTurns` or reaches `end_turn` (LLM voluntarily stops without calling any tool) without a valid `submit_result` call, the step **fails** with error `"resultSchema defined but submit_result never called"`.
- **Parallel side-effecting tools**: If the LLM emits `submit_result` alongside other tool calls (e.g., `write_file`) in the same turn, all tools in the batch execute. The `submit_result` terminates the loop after the batch completes; side effects from other tools in the batch are committed, not rolled back.
- **Agents without `resultSchema`**: No `submit_result` tool injected. Output is content-only (`result` is nil).

Result is not defined in the workflow YAML schema (it is runtime output, not document input). Its structure is free-form unless constrained by the agent's `resultSchema` field. Specific protocols (like `untilAgent`) document which result keys they read.

All CEL evaluation contexts (`condition`, `until`, `forEach`) have access to both `steps.<id>.content` (text) and `steps.<id>.result` (structured data) of dependency/inner steps.

---

## 5. Conditions

A step's `condition` field holds a CEL (Common Expression Language) expression. The expression is evaluated before the step runs. If it evaluates to `false`, the step is skipped.

### Evaluation Semantics

- The condition is evaluated after all `dependsOn` steps have finished but before the step starts.
- An empty string is not valid (`minLength: 1` in the schema).
- The expression must evaluate to a boolean.

### Available Variables

| Variable | Type | Description |
|----------|------|-------------|
| `steps.<id>.content` | string | Text content of a completed dependency step. |
| `steps.<id>.status` | string | Status of a dependency step: `"completed"`, `"failed"`, `"skipped"`, or `"cancelled"`. |
| `steps.<id>.result` | object | Structured result (JSON, `map[string]any`) of a completed dependency step. |

In `condition` scope (top-level step) the bare `content` variable is in scope but always evaluates to an empty string -- only `steps.<id>.content` is meaningful. Avoid bare `content` in conditions.

Result enables structured decisions without parsing free-form text. For example:
```yaml
condition: "steps.analyze.result.severity == 'critical'"
```

### Skip Behavior

When a step is skipped:

1. Its status is set to `skipped`.
2. Steps that depend on it see `steps.<id>.status == "skipped"`.
3. Whether dependents still run depends on the `onStepFailure` option and the dependent's own conditions.

```yaml
steps:
  - id: design
    agent: designer
    instructions: "Design the user management API."

  - id: implement
    agent: developer
    instructions: "Implement the API."
    dependsOn: [design]

  - id: security-audit
    agent: security
    instructions: "Audit the authentication implementation."
    dependsOn: [implement]
    condition: "steps.design.result.features.contains('authentication')"

  - id: test
    agent: developer
    instructions: "Write and run unit tests."
    dependsOn: [implement]

  - id: optimize
    agent: optimizer
    instructions: "Optimize hot paths."
    dependsOn: [test]
    condition: "steps.test.status == 'completed' && steps.test.content.contains('PASS')"
```

### Condition with Loop

A step may have both `condition` and `loop`. Evaluation order: condition first. If false, the entire loop is skipped. If true, the loop begins.

---

## 6. Loops

The `loop` field on a step defines iteration behavior. There are two mutually exclusive modes: **repeat-until** (sequential iterations until a condition is met) and **forEach** (parallel map over an array).

### Loop Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `maxIterations` | integer | Yes (repeat-until, validator-enforced) | Hard iteration cap. Minimum: 1. Guarantees termination. Not used with `forEach`. |
| `until` | string | No | CEL expression evaluated after each iteration. Loop stops when true. |
| `untilAgent` | string | No | Agent name acting as judge. Loop stops when agent's `result.done` is `true`. |
| `forEach` | string or array | No | Array to iterate over. CEL expression returning an array, or a static array literal. |
| `maxConcurrency` | integer | No | Max parallel forEach iterations. Default: all parallel. Minimum: 0. Setting `0` is equivalent to omitting the field (treated as unset). |
| `delay` | Duration | No | Wait between iterations. Repeat-until only. When the loop exits early (via `until` evaluating true or `untilAgent` returning `done: true`), the next iteration's `delay` is not applied. |
| `steps` | array[Step] | No | Inner steps for multi-step loop (`minItems: 1`). If absent, the parent step itself is looped. |
| `outputMode` | string | No | How a multi-iteration loop builds its final `Content`. `last` (default, also matches the empty string) keeps only the final iteration's output. `cumulative` concatenates all iterations' outputs separated by `\n\n---\n\n`. Used by debate / rolling-context patterns where every round's text matters. |

### Repeat-Until Mode

Runs iterations sequentially. Each iteration sees the previous iteration's content and result.

Requirements:
- `maxIterations` is required (prevents infinite loops).
- `until` and `untilAgent` are optional. Both may be present. Evaluation order: `until` (CEL) is evaluated first. If true, loop stops. Otherwise, `untilAgent` is invoked. If `result.done` is true, loop stops. If neither is satisfied, the next iteration begins.
- If neither `until` nor `untilAgent` is specified, the loop runs exactly `maxIterations` times.

**Single-step loop** (no inner `steps`):

```yaml
- id: health-check
  instructions: "Check service health."
  loop:
    maxIterations: 10
    until: "content.contains('healthy')"
    delay: "30s"
```

**Multi-step loop** (inner `steps` form a sub-DAG):

```yaml
- id: dev-cycle
  dependsOn: [design]
  loop:
    maxIterations: 5
    untilAgent: judge
    # untilAgent automatically invokes the judge agent after each iteration.
    # No explicit evaluate step needed; the judge receives iteration outputs as context.
    steps:
      - id: implement
        agent: coder
        instructions: "Implement or fix the code."
      - id: review
        agent: reviewer
        instructions: "Review the implementation."
        dependsOn: [implement]
```

### `untilAgent` Semantics

The named agent acts as a judge after each iteration:

1. **Parse-time validation**: The validator checks that the agent referenced by `untilAgent` exists in the `agents` section and has a `resultSchema` where: (a) `properties.done` exists with `type: "boolean"`, and (b) `"done"` is in the top-level `required` array. If not, the workflow is rejected at parse time.
2. The judge receives iteration outputs as context.
3. The executor auto-injects a `submit_result` tool (parameters = judge's `resultSchema`).
4. The judge produces output with two parts: **content** (free-form text) and **result** (structured JSON object from `submit_result` tool call). Calling `submit_result` terminates the judge's conversation.
5. The executor reads the decision from `result.done`:
   - If `result.done` is `true`, the loop terminates.
   - If `result.done` is `false`, the loop continues.
6. The `result.reason` field is optional but recommended for observability.
7. If the judge fails to call `submit_result` (exhausts maxTurns, reaches `end_turn`, or validation never succeeds), the iteration is treated as a judge failure: `result` is nil, and the loop continues to the next iteration (fail-open, bounded by `maxIterations`). The next iteration's agents receive the judge's `content` (text output) as context but no structured `result`. This means work agents in the next iteration can still see the judge's textual feedback even if the structured decision was missing.

**Why fail-open for judges, fail-hard for regular agents**: A regular step with `resultSchema` has an explicit contract -- the workflow author expects structured output. Failing silently would hide a broken agent. A judge, however, is a secondary evaluator within a loop that already has `maxIterations` as a hard bound. Fail-hard would abort the entire loop on a single judge glitch. Fail-open lets the loop continue and converge via `maxIterations` or the CEL `until` condition. The judge failure is logged via `ProgressSink` for observability.

**Output model**: Agent output in zenflow has two channels:
- **Content**: Free-form text (markdown). Human-readable. Used for step output passing and display.
- **Result**: Structured JSON object (`map[string]any`). Machine-readable. Validated against the agent's `resultSchema` if defined. Used by the executor for decisions (e.g., `untilAgent` protocol) and by consumers for custom logic.

The `untilAgent` protocol uses result exclusively -- it never parses the content text. This keeps the content channel clean for human consumption while result carries structured signals.

Example judge output:
```
Content: "The implementation looks solid. All edge cases are handled
         and test coverage is comprehensive. Ready to ship."
Result: {"done": true, "reason": "all tests pass, edge cases covered"}
```

### ForEach Mode

Maps over an array in parallel. Each element produces one iteration.

Requirements:
- `forEach` is required (provides the array).
- Array length determines iteration count. No `maxIterations` needed.
- `maxConcurrency` throttles parallelism (default: all parallel).

`forEach` is mutually exclusive with `until`, `untilAgent`, `maxIterations`, and `delay`.

**Static array**:

```yaml
- id: review-repos
  agent: reviewer
  instructions: "Review this repo."
  loop:
    forEach: ["frontend", "backend", "infra"]
    maxConcurrency: 3
```

**Dynamic array from step output** (CEL expression):

```yaml
- id: list-services
  agent: discovery
  instructions: "List services as JSON array."

- id: deploy-each
  dependsOn: [list-services]
  loop:
    forEach: "steps.list-services.result.services"
    maxConcurrency: 3
    steps:
      - id: deploy
        agent: deployer
        instructions: "Deploy this service."
      - id: verify
        agent: verifier
        instructions: "Run health checks."
        dependsOn: [deploy]
```

### Item Injection

For each iteration, the current item is injected into the agent context:

| Variable | Type | Scope | Description |
|----------|------|-------|-------------|
| `item` | any JSON type | forEach | Current element from the array. Items can be any JSON type, though string and object are the most common. |
| `index` | int | forEach | Zero-based iteration index. |
| `iteration` | int | repeat-until | Zero-based iteration number. |
| `steps.<id>.content` | string | multi-step loop | Text content of an inner step from the just-completed iteration. |
| `steps.<id>.status` | string | multi-step loop | Status of an inner step from the just-completed iteration. |
| `steps.<id>.result` | object | multi-step loop | Structured result of an inner step from the just-completed iteration. |
| `content` | string | single-step loop | Text content of the step from the just-completed iteration. |
| `result` | object | single-step loop | Structured result of the step from the just-completed iteration. |
| `status` | string | single-step loop | Status of the step from the just-completed iteration. |

Both `item`/`index` and `iteration` are available in CEL expressions within their respective loop modes. No template syntax (e.g., `{{item}}`) is used -- items are context-injected. `content` and `result` (without `steps.` prefix) are available in single-step repeat-until loops only.

Injection format in the agent message:

```
## forEach Item (index: 0)
{"name": "auth", "region": "us-east-1"}
```

### Interaction with Other Step Fields

- **`timeout` + `loop`**: Timeout applies to all iterations combined, not per iteration. For per-iteration timeouts, set `timeout` on inner steps or use `options.stepTimeout`.
- **`retries` + `loop`**: Retries apply to the entire loop block. If the loop fails (e.g., `maxIterations` exhausted without `until` met), the whole loop retries from iteration 0.
- **`condition` + `loop`**: Condition is evaluated first. If false, the entire loop is skipped.

### Nested Loop Prohibition

Inner steps inside a `loop.steps` array must not have their own `loop` field. Nested loops are not supported in v1. This rule is enforced by validation, not by the JSON Schema structure (since the schema uses `$ref` which is inherently recursive).

### ForEach Step ID Namespacing

ForEach iterations produce namespaced step IDs: `{step-id}[{index}]`. For multi-step forEach loops: `{step-id}[{index}].{inner-id}`. Examples: `deploy-each[0]`, `deploy-each[1].verify`.

---

## 7. Includes

Includes enable workflow composition. A step can delegate to a sub-workflow defined in a separate file.

### Two-Level Design

**Top-level `includes`**: a named registry mapping names to file paths.

```yaml
includes:
  deploy: "workflows/deploy.yaml"
  auth: "workflows/auth-flow.yaml"
```

**Step-level `include`**: references a name from the registry, or a file path directly.

```yaml
steps:
  - id: deploy-staging
    include: deploy           # named reference
    dependsOn: [build]

  - id: setup-auth
    include: "workflows/auth-flow.yaml"  # direct path (no includes section needed)
    dependsOn: [design]
```

### Include Step Rules

A step with `include` must not have these fields (mutually exclusive):
- `agent`
- `instructions`
- `loop`
- `condition`
- `contextFiles`
- `model`

The include step delegates entirely to the sub-workflow. Allowing condition on an include step would create ambiguity: should the condition evaluate before loading the sub-workflow, or after? To avoid this, conditions should be placed on a wrapper step that depends on the include step, or within the sub-workflow itself.

A step with `include` may have:
- `dependsOn` -- applies to the sub-workflow as a whole.
- `timeout` -- applies to the entire sub-workflow execution.
- `retries` -- retries the sub-workflow from the beginning.

### Sub-Workflow Behavior

- The sub-workflow file is parsed as a full zenflow document.
- Sub-workflow agents merge into the parent scope. Name collisions are an error.
- Sub-workflow step IDs are namespaced: `{parent-step-id}.{inner-step-id}` (e.g., `deploy-staging.run-tests`).
- Recursive includes are allowed but depth-limited. Implementations should set a max depth (recommended: 5).
- File paths are relative to the including workflow file's directory.

### Reference Resolution

When a step has `include: foo`:

1. If `foo` matches a key in the top-level `includes` map, resolve to that file path.
2. Otherwise, treat `foo` as a file path directly.

This is a two-phase process: JSON Schema validates that `include` is a string; the parser resolves the reference.

```yaml
includes:
  deploy: "workflows/deploy.yaml"

steps:
  - id: design
    agent: architect
    instructions: "Design the deployment strategy."

  - id: build
    agent: builder
    instructions: "Build and package the application."
    dependsOn: [design]

  - id: deploy-staging
    include: deploy
    dependsOn: [build]
    timeout: "30m"
    retries: 1

  - id: deploy-production
    include: deploy
    dependsOn: [deploy-staging]
    timeout: "45m"
    retries: 2
```

---

## 8. Options

The `options` object configures execution behavior for the entire workflow.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `maxConcurrency` | integer | `5` | Max steps executing in parallel. Minimum: 0. Setting `0` (or omitting the field) is treated as "unset" and falls through to the next level. Precedence: workflow YAML `options.maxConcurrency` > `WithMaxConcurrency(n)` orchestrator option > library default `5`. The default is applied at execution time, not by the parser. |
| `onStepFailure` | enum | `cascade` | Failure strategy: `"cascade"`, `"skip-dependents"`, or `"abort"`. |
| `timeout` | Duration | -- | Timeout for the entire workflow. |
| `stepTimeout` | Duration | -- | Default timeout for each step, unless overridden by a step-level `timeout`. |
| `isolation` | string | -- | Isolation strategy. Free-form string. The schema validates type only; values are consumer-defined. Examples: `"none"`, `"worktree-per-step"`. |
| `scheduler` | enum | `dependency-first` | Scheduling algorithm: `"dependency-first"`, `"round-robin"`, or `"least-busy"`. |
| `maxRetries` | integer | -- | Workflow-level default for the per-step `maxRetries` cap (passed to the agent runner via `goai.WithMaxRetries`). Falls through to per-step `maxRetries` when set; otherwise the orchestrator default applies. Minimum: 0. |

### Failure Strategies

- **`cascade`**: When a step fails, all steps that depend on it (transitively) also fail.
- **`skip-dependents`**: When a step fails, dependent steps are skipped. Other independent branches continue.
- **`abort`**: When any step fails, the entire workflow stops. Running steps may be cancelled.

```yaml
options:
  maxConcurrency: 4
  onStepFailure: skip-dependents
  timeout: "2h"
  stepTimeout: "30m"
  isolation: "worktree-per-step"
  scheduler: dependency-first
```

---

## 9. File References

Fields that accept file references: `agents.*.prompt` and `steps.*.instructions`.

A value starting with `@` is a file reference. The `@` prefix is stripped, and the remainder is treated as a file path relative to the workflow file's directory.

| Value | Interpretation |
|-------|---------------|
| `"Write a plan."` | Literal string. |
| `"@prompts/planner.md"` | Read `prompts/planner.md` relative to the workflow file. |

The `@` convention is a runtime behavior. JSON Schema does not enforce it -- parsers detect and resolve it during loading.

The `contextFiles` field does not use the `@` prefix. Its values are always file paths relative to the workflow file directory.

```yaml
agents:
  planner:
    description: "Technical lead."
    prompt: "@prompts/planner.md"

steps:
  - id: plan
    agent: planner
    instructions: "@instructions/plan-feature.md"
    contextFiles:
      - "docs/architecture.md"
      - "docs/api-spec.yaml"
```

---

## 10. Duration Format

Duration values are strings following a subset of Go's `time.Duration` format. Only hours (`h`), minutes (`m`), and seconds (`s`) are supported.

**Pattern**: `^(\d+h)?(\d+m)?(\d+s)?$` with `minLength: 2`.

| Valid | Invalid | Reason |
|-------|---------|--------|
| `"30m"` | `"30"` | Missing unit. |
| `"1h30m"` | `"1.5h"` | No fractions. |
| `"45s"` | `"500ms"` | Milliseconds not supported. |
| `"1h0m30s"` | `""` | Empty string rejected (`minLength: 2`). |
| `"0s"` | `"1us"` | Microseconds not supported. |

`"0s"` is valid and means zero duration. The interpretation of a zero timeout (no timeout vs. immediate timeout) is implementation-defined.

For implementations in languages without Go's duration parser, the format is: one or more groups of `<digits><unit>` in order `h`, `m`, `s`. Parse left to right, multiply digits by unit, sum.

The spec does not constrain maximum duration values. Implementations should handle overflow gracefully (e.g., reject durations exceeding platform-specific limits).

---

## 11. Validation Rules

These rules must be enforced by validators. Some are expressed in JSON Schema; others require custom logic.

### Schema-Enforced Rules

| Rule | Enforcement |
|------|-------------|
| `name` is required and non-empty | `required` + `minLength: 1` |
| `steps` is required and non-empty | `required` + `minItems: 1` |
| `AgentConfig.description` is required | `required` |
| Step `id` matches `^[a-zA-Z][a-zA-Z0-9_-]*$` (loose, schema-level) | `pattern` |
| Step `id` additionally matches `^[a-z][a-z0-9_-]{0,63}$` (strict, parser-enforced post-schema; lowercase only, max 64 chars). Coordinator-generated workflows additionally reject hyphens. | parser |
| `version` >= 1 | `minimum: 1` |
| `maxTurns` >= 0 | `minimum: 0` (parser/validator treat `0` and "omitted" identically: both fall back to the default of 50) |
| `temperature` in [0, 2] | `minimum` + `maximum` |
| `topP` in [0, 1] | `minimum` + `maximum` |
| `retries` >= 0 | `minimum: 0` |
| `maxRetries` >= 0 (step and options) | `minimum: 0` |
| `Loop` block has at least one field | `minProperties: 1` |
| `loop.outputMode` is `""`, `"last"`, or `"cumulative"` | `enum` |
| `onStepFailure` is one of 3 values | `enum` |
| `scheduler` is one of 3 values | `enum` |
| Duration format | `pattern` + `minLength` |
| No unknown fields | `additionalProperties: false` |

### Validator-Enforced Rules

These rules cannot be expressed in JSON Schema and require custom validation logic.

**Uniqueness**:
- Step IDs within the same scope (top-level `steps`, or `loop.steps`) must be unique.

**Referential Integrity**:
- Every `step.agent` value must match a key in the `agents` map.
- Every entry in `step.dependsOn` must match a step ID in the same scope.
- Every `loop.untilAgent` value must match a key in the `agents` map. The referenced agent must have `resultSchema` defined (not nil/absent), and that schema must have `properties.done.type` equal to `"boolean"` and `"done"` in the top-level `required` array.
- Every `step.include` value must match a key in the `includes` map or be a valid file path.

**Cycle Detection**:
- The dependency graph formed by `dependsOn` edges must be acyclic. This applies separately to each scope: the top-level DAG, each `loop.steps` sub-DAG, and each included sub-workflow.

**Cross-Namespace `dependsOn` Prohibition**:
- `dependsOn` references are resolved within the same scope. Outer steps must not reference inner step IDs (from loops or includes), and inner steps must not reference outer step IDs.

**Include Mutual Exclusion**:
- A step with `include` must not have `agent`, `instructions`, `loop`, `condition`, `contextFiles`, or `model`.

**ForEach Mutual Exclusion**:
- If `loop.forEach` is present, `loop.maxIterations`, `loop.until`, `loop.untilAgent`, and `loop.delay` must be absent.

**Repeat-Until Requirements**:
- If `loop` is present without `forEach`, `loop.maxIterations` is required.

**Nested Loop Prohibition**:
- Steps inside `loop.steps` must not have a `loop` field.

**ForEach Array Constraints**:
- If `forEach` is a static array literal, it must not be empty.

**Negative Value Rejection**:
- `maxConcurrency` (both `options` and `loop` level) must be >= 0. The schema permits `0` because both the parser and validator treat `0` and "field omitted" identically; `0` does not mean "no concurrency", it means "unset - fall through to the next precedence level". For workflow-level `options.maxConcurrency`, the precedence chain is YAML > `WithMaxConcurrency(n)` orchestrator option > library default `5`, applied at execution time. For loop-level `forEach`, unset means all-parallel.
- `retries` and `maxRetries` (both step and `options` level) must be >= 0.

**Version Validation**:
- If `version` is present, only known versions (currently `1`) are accepted.

### Hard Caps

The following caps apply to BOTH parsed YAML/JSON workflows AND coordinator-generated workflows (e.g., `RunGoal` output). They are enforced after schema validation; documents that exceed any cap are rejected.

| Cap | Value | Applies To |
|-----|-------|-----------|
| `MaxStepsPerWorkflow` | `100` | Length of the top-level `steps` array. Inner `loop.steps` and sub-workflow `steps` arrays are NOT counted against this cap (they are validated only by their own structural rules). |
| `MaxNestingDepth` | `20` | Maximum `@`-reference chain depth in step output dereferencing. [^cel-cost] [^include-depth] |
| `MaxDescriptionChars` | `2000` | Per-field character cap on `workflow.description` and `step.instructions`. (Note: `agent.description` is currently not subject to this cap -- see issue tracker.) |
| `MaxFileSizeBytes` | `1 MiB` | Maximum size of the YAML or JSON workflow file passed to `ParseWorkflow` / `ParseWorkflowJSON` (1 MiB). |
| `MaxAttachmentSizeBytes` | `10 MiB` | Per-entry cap for `contextFiles` and `@`-referenced attachments. |
| `MaxIncludeDepth` | `5` | Maximum depth of sub-workflow nesting via `include`. |

[^cel-cost]: CEL expression cost is bounded by the CEL `CostLimit(10000)` in `internal/exec/cel.go`, not by `MaxNestingDepth`.

[^include-depth]: Include nesting cap is `MaxIncludeDepth = 5` (separate constant), not `MaxNestingDepth`.

These caps are deliberate denial-of-service and complexity bounds; they are not configurable via the workflow document.

---

## 12. Versioning

The `version` field indicates which schema version the document conforms to.

| Value | Meaning |
|-------|---------|
| Absent | Defaults to `1`. |
| `1` | Conforms to this specification. |
| `>= 2` | Reserved for future versions. Validators for v1 should reject unknown versions. |

### Backwards Compatibility

Within a major version, changes are additive only:
- New optional fields may be added.
- New enum values may be added to `onStepFailure`, `scheduler`, etc.
- No existing required fields will be removed or renamed.
- No existing field semantics will change.

Breaking changes (new required fields, removed fields, changed semantics) require a new major version (`v2`). Old schemas remain available at their version paths (`spec/v1/`, `spec/v2/`).

---

## 13. YAML vs JSON

Both YAML and JSON are valid serializations. The schema defines the data model; the serialization format is interchangeable.

Rules:
- YAML and JSON documents with the same data must produce identical parsed results.
- No YAML-specific features are used (no anchors, no tags, no merge keys).
- YAML comments are allowed but have no semantic meaning.
- JSON is the expected output format from LLM coordinators (RunGoal). YAML is the expected format for human-authored workflows.

**YAML**:

```yaml
name: my-workflow
version: 1
steps:
  - id: first
    instructions: "Do the thing."
    timeout: "10m"
```

**Equivalent JSON**:

```json
{
  "name": "my-workflow",
  "version": 1,
  "steps": [
    {
      "id": "first",
      "instructions": "Do the thing.",
      "timeout": "10m"
    }
  ]
}
```

Both documents validate against `schema.json` and produce the same parsed workflow.

## 14. NDJSON Event Schema

`zenflow flow`, `zenflow goal`, and `zenflow agent` all accept `--json` to emit a line-delimited JSON event stream on stdout. Each line is one JSON object describing a single workflow event. The format is the **machine surface** of the CLI; shell consumers parse it with `jq`, `grep`, or any line-oriented tool.

### Envelope

Every event line is a flat JSON object with these fields. Fields are omitted when their Go zero value would be unhelpful (empty strings, nil pointers, zero durations); consumers must tolerate missing fields.

| Field | Type | Description |
| --- | --- | --- |
| `type` | string | Discriminator. Drives interpretation of the rest of the object. See § 14.2 for the full enumeration. |
| `timestamp` | RFC3339 string | Event clock. Always present. |
| `runId` | string | Workflow run ID. Correlates events from the same run. Present when known. |
| `stepId` | string | Step ID. Present on step-scoped events; absent for run-scoped events. |
| `agent` | string | Agent name (the YAML `agents:` map key). Present on agent-emitted events. |
| `agentId` | string | Per-call agent identifier. Set on streaming `output` events when the runner is namespaced (subagent, loop iteration). |
| `message` | string | Human-readable detail; semantics vary per `type`. |
| `duration` | Go duration string (`"1.234s"`) | Set on completion events (`step_end`, `workflow_end`, `tool_call` end-phase, `resume_completed`). |
| `tokens` | object `{prompt, completion, total}` | Token counts. Set on completion events that carry usage data. |
| `error` | string | Error string. Set on failure events (`error`, `resume_failed`, etc.). |
| `data` | object | Event-type-specific payload. The required keys per `type` are documented in § 14.2. |
| `delta` | string | Streaming token delta. Only on `output`. |
| `done` | boolean | Streaming-end marker. Only on `output`. |
| `reasoning` | boolean | Set to `true` when the streaming `delta` is reasoning/thinking content (vs final agent text). Omitted otherwise. |

### 14.1 Stability and forwards-compatibility

The schema is **additive**:

- New `type` values may be introduced in any minor release. Consumers must skip unknown `type` values gracefully.
- Existing `type` values never reshape: their required envelope fields and `data` keys remain stable across the v0.x line.
- New optional fields may be added to the envelope or to `data` payloads at any time. Consumers must not fail on unrecognised keys.
- Removing or renaming a `type` value, an envelope field, or a `data` key would be a breaking change and is not permitted within v0.x.

This contract mirrors the Stable-tier surface: code that ignores unknown fields keeps working; code that pattern-matches on a known `type` keeps working as long as the canonical event is emitted.

### 14.2 Event types

| `type` | Emitted when | Required `data` keys |
| --- | --- | --- |
| `workflow_start` | Workflow run begins. | `total` (int - declared step count). |
| `workflow_end` | Workflow run ends (success or failure). Carries `duration`, `tokens`. | - |
| `step_start` | A DAG step begins. | `index` (int), `total` (int). Loop steps add `loop_type`, optionally `items`. Include steps add `include`. |
| `step_end` | A DAG step ends successfully. Carries `duration`, `tokens`. | - |
| `step_skipped` | A step was skipped (failed-dep cascade with `skip-dependents`, or false `condition`). | - |
| `error` | A step failed. Carries `error`, optionally `duration`. | - |
| `agent_turn` | The agent loop took an LLM call. | `phase` (`request` or `response`), `model` (string). `request` adds `turn` (int); `response` adds `tokens`. |
| `tool_call` | A tool was invoked. | `phase` (`start` or `end`), `tool_name`, `tool_call_id`, `input` (redacted JSON string). `end` adds `output`, `duration`, `error` (nil on success). Subagent callers add `depth` and `parentCallID`. |
| `message` | An informational message (CEL-skip reason, forEach item cap, resume-truncation note). | Optionally `reason`, `messageCount`. |
| `coordinator_narration` | Coordinator's `narrate(...)` tool call. Auto-emitted. | - |
| `coordinator_message` | Coordinator's `forward_to_agent(...)` tool call. Reserved type: the SDK does NOT auto-emit this; consumers that want a per-routed-message progress event surface it themselves from a `WithRunnerProgress` sink wired up around the coordinator runner. The type is part of the stable schema so consumers can rely on the name. | - |
| `coordinator_synthesis` | Coordinator's `finalize(summary=...)` tool call. Reserved type: the SDK does NOT auto-emit this; the `finalize` tool sets the runner's `FinalSummary()` and the embedder is expected to surface the summary as a `coordinator_synthesis` event after the coord runner exits. The type is part of the stable schema. | - |
| `coordinator_inbox_message` | Reverse reply from a resumed step landed in the coordinator's inbox. | `from` (string - originating step ID), `type` (RouterMessageType string). |
| `message_sent` | An agent or coord-side outbound message was queued. | `to` (string - recipient step ID), `text` (string), `msg_type` (int - RouterMessageType). |
| `message_dropped` | A router message was dropped before delivery. | `reason` (string), `from`, `to`, `msg_type` (int). |
| `agent_inbox_drain` | An agent drained one router message into its LLM conversation. | `from`, `msg_type` (int). |
| `agent_idle` | The agent finished a goai iteration with no unread mailbox messages. | `unread_count` (int - always 0). |
| `agent_wake` | The agent woke from idle to drain new messages. | `message_count` (int), `cycle` (int - 1-indexed wake cycle). |
| `max_wake_cycles_warning` | Wake-cycle cap is at 80% of configured limit. | `current_cycle` (int), `max_cycles` (int), `unread_remaining` (int). |
| `resume_started` | Auto-resume goroutine spawned for a terminated step. | `resumeID` (string), `from` (string - original sender). |
| `resume_completed` | Resume run finished with a final response. Carries `duration`. | `resumeID`, `from`, `durationMs` (int64). |
| `resume_failed` | Resume could not complete. | `resumeID`, `from`, `reason`, `durationMs`. |
| `resume_queued` | A resume attempt arrived while another for the same step was in flight. | `resumeID`, `from`, optionally `activeResumeID`. |
| `transcript_sealed` | Transcript Append hit the size cap; subsequent appends for the step are suppressed. | `reason`, `error`. |
| `plan_ready` | `RunGoal` decomposition produced a workflow. | `workflow` (parsed `*Workflow`). |
| `output` | Streaming agent output token. Sets `delta`, `done`. May set `reasoning: true`. | - |

#### `msg_type` integer values

`message_sent`, `message_dropped`, and `agent_inbox_drain` carry a `msg_type` integer drawn from the `RouterMessageType` enum. Integer values are stable across the v0.x line; reordering is a breaking change. Consumers parsing NDJSON should map:

| `msg_type` | Name | Meaning |
| --- | --- | --- |
| `0` | `RouterMessageInfo` | General informational message routed between agents (the default). |
| `1` | `RouterMessageCancel` | Coordinator-initiated cancel; the receiving agent stops its run cleanly. |
| `2` | `RouterMessageContextUpdate` | New context injected into the receiving agent's conversation. |
| `3` | `RouterMessageResumeReply` | Reverse-routed reply produced by the resume mechanism after a terminated step's transcript was replayed. |

### 14.3 Sample stream

The shape of a complete `zenflow flow simple.yaml --json` run is roughly:

```json
{"type":"workflow_start","timestamp":"2026-05-05T10:00:00Z","runId":"r1","message":"simple","data":{"total":1}}
{"type":"step_start","timestamp":"2026-05-05T10:00:00Z","runId":"r1","stepId":"hello","agent":"writer","data":{"index":0,"total":1}}
{"type":"agent_turn","timestamp":"2026-05-05T10:00:00Z","runId":"r1","stepId":"hello","agent":"writer","data":{"phase":"request","turn":1,"model":"gemini-3-pro-preview"}}
{"type":"output","runId":"r1","stepId":"hello","delta":"Hello, world.","done":false}
{"type":"output","runId":"r1","stepId":"hello","delta":"","done":true}
{"type":"agent_turn","timestamp":"2026-05-05T10:00:01Z","runId":"r1","stepId":"hello","agent":"writer","data":{"phase":"response","model":"gemini-3-pro-preview"},"tokens":{"prompt":12,"completion":3,"total":15}}
{"type":"step_end","timestamp":"2026-05-05T10:00:01Z","runId":"r1","stepId":"hello","agent":"writer","duration":"1.0s","tokens":{"prompt":12,"completion":3,"total":15}}
{"type":"workflow_end","timestamp":"2026-05-05T10:00:01Z","runId":"r1","duration":"1.0s","tokens":{"prompt":12,"completion":3,"total":15}}
```

### 14.4 Round-trip stability

A spec-conformant test (`TestSpecSampleNDJSON_RoundTrip` in `sink/json_test.go`) decodes the sample stream above and asserts the documented envelope shape on every line. The test fails when:

- A line's `type` value is not in the § 14.2 enumeration.
- A required envelope field is missing (`type`, `timestamp` always required; `runId` required on every event in the sample).
- An unknown `data` key appears on a known `type` without being documented in § 14.2.

The test is the schema's *enforcement* mechanism: if a future change to `sink/json.go` reshapes an existing event, the round-trip fails until either the producer is reverted or the spec section is updated in the same PR.

---

## 15. Router & Mailbox

### Per-step mailbox bounds

Every step has an inbox (mailbox) that holds messages routed to it from the coordinator or other agents. The mailbox is bounded:

- **Default capacity**: `DefaultMaxMailboxSize = 10000` messages.
- **Overflow behavior**: When the mailbox is full, additional messages are dropped (not blocked). Each drop emits a `message_dropped` event (see §14.2) with `reason="queue-full"`.
- **Opt-out**: `WithMaxMailboxSize(0)` (programmatic only) disables the bound. There is no workflow-document-level field to override the default; the cap exists to bound memory usage during long runs.

Consumers parsing NDJSON should treat `message_dropped` with `reason="queue-full"` as a backpressure signal -- the receiving step is not draining its inbox fast enough relative to inbound traffic.
