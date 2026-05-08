---
title: Workflow
description: The workflow object is the document root. This page covers every top-level field, what is required, and the shape of the options block. For...
---

# Workflow

The workflow object is the document root. This page covers every top-level field, what is required, and the shape of the `options` block. For sub-objects (`agents`, `steps`, `loop`), follow the cross-references.

The authoritative source is [`spec/v1/spec.md` §2 and §8](https://github.com/zendev-sh/zenflow/blob/main/spec/v1/spec.md). The structural contract is [`spec/v1/schema.json`](https://github.com/zendev-sh/zenflow/blob/main/spec/v1/schema.json).

## Required fields

A document must have `name` and `steps`:

```yaml
name: my-workflow
steps:
  - id: hello
    instructions: "Say hello."
```

| Field | Constraint |
| --- | --- |
| `name` | Non-empty string (`minLength: 1`). |
| `steps` | Array with at least one element (`minItems: 1`). |

Everything else is optional.

Workflow YAML/JSON file size cap: 1 MiB (`MaxFileSizeBytes`).

## name

Free-form string identifying the workflow. Used in CLI output, JSON events (`event.message` on `workflow_start` / `workflow_end`), and the storage path when `--resume` is in play.

```yaml
name: code-review
```

There are no character restrictions beyond non-empty. Conventions in the reference examples: kebab-case, lowercase, descriptive (`research-team`, `loop-foreach`, `parallel-fan-out`).

## description

Human-readable summary. Optional. Surfaces in tooling that lists workflows; the engine itself does not interpret it. Maximum length: 2000 chars (`MaxDescriptionChars`). Note: `agent.description` is currently not subject to this cap.

```yaml
name: deploy
description: |
  Build, push, and deploy services to staging. Requires AWS_ACCESS_KEY_ID
  and bash tool.
steps:
  - id: build
    instructions: "..."
```

## version

Integer. Defaults to `1` if omitted. Must be `>= 1`. Currently the only defined version is `1`; v1 validators reject unknown versions.

```yaml
name: pinned
version: 1
steps:
  - id: noop
    instructions: "."
```

Pin `version: 1` if you want a parser to fail loudly on a future v2 document instead of silently accepting unknown fields.

## agents

Map of agent name to `AgentConfig`. Each key is the name a step references via its `agent` field. See [Agent](./agent) for the full field list.

```yaml
agents:
  planner:
    description: "Technical lead who creates implementation plans."
    model: "bedrock/anthropic.claude-sonnet-4-6"
    temperature: 0.3
  coder:
    description: "Developer who writes Go code."
    tools: ["read", "write", "bash"]
```

Agents are optional. A step without an `agent` field uses a default agent provided by the executor (a generic LLM call with the orchestrator's default model). Defining named agents is the way to specialize behavior, pin models, or scope tools.

## includes

Named sub-workflow registry. Maps a name to a YAML file path relative to the current workflow file. Steps reference entries via the `include` field.

```yaml
includes:
  deploy: "workflows/deploy.yaml"
  auth: "workflows/auth-flow.yaml"

steps:
  - id: deploy-staging
    include: deploy
    dependsOn: [build]
```

Sub-workflow step IDs are namespaced as `{parent-step-id}.{inner-step-id}` (e.g., `deploy-staging.run-tests`). Recursive includes are hard-capped at depth 5 (`MaxIncludeDepth` in `limits.go`). The separate `MaxNestingDepth = 20` constant applies to `@`-reference chain depth, NOT include nesting. See [Step](./step#include) for the rules on what fields an `include` step may carry.

## steps

Array of `Step` objects. The DAG. Step order in the array has no semantic meaning for execution; it is a convenience for human readers. Execution order follows `dependsOn` edges. See [Step](./step) for every field.

```yaml
steps:
  - id: design
    agent: architect
    instructions: "Design the API."

  - id: implement
    agent: coder
    instructions: "Build it."
    dependsOn: [design]
```

`minItems: 1` is enforced - an empty workflow is rejected. The top-level `steps` array is capped at 100 entries (`MaxStepsPerWorkflow`). Inner `loop.steps` and sub-workflow `steps` arrays are NOT counted against this cap.

## options

Execution configuration. Every field is optional; sensible defaults apply if the block is omitted.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `maxConcurrency` | integer | `5` | Max steps executing in parallel. `minimum: 0`. `0` (or omitting the field) is treated as "unset" and falls through to the next precedence level. Precedence: workflow YAML `options.maxConcurrency` > `WithMaxConcurrency(n)` orchestrator option > library default `5`. The default is applied at execution time, not by the parser. |
| `onStepFailure` | enum | `cascade` | Failure strategy: `cascade`, `skip-dependents`, or `abort`. |
| `timeout` | Duration | unset | Timeout for the whole workflow. |
| `stepTimeout` | Duration | unset | Default per-step timeout, used when a step does not define its own `timeout`. |
| `isolation` | string | unset | Isolation strategy. Free-form; consumer-defined. Examples: `none`, `worktree-per-step`. |
| `scheduler` | enum | `dependency-first` | Scheduling algorithm: `dependency-first`, `round-robin`, or `least-busy`. |
| `maxRetries` | integer | unset | Workflow-level default for each step's `maxRetries` cap (the agent runner's tool-call retry budget, passed via `goai.WithMaxRetries`). Steps inherit this unless they set their own `maxRetries`. When unset at both step and options level, [goai](https://goai.sh)'s built-in retry policy applies (configured via `WithGoAIOptions(goai.WithMaxRetries(n))` if you want to override it). `minimum: 0`. |

Unknown fields are rejected.

### maxConcurrency

Caps how many steps can execute simultaneously. `1` forces sequential execution; the engine still respects DAG order, just one step at a time.

```yaml
options:
  maxConcurrency: 4
```

**Precedence:** workflow YAML `options.maxConcurrency` > `WithMaxConcurrency(n)` orchestrator option > library default `5`. Setting `0` (or omitting the field) is treated as "unset" and falls through to the next level. The default is applied at execution time, so `WithMaxConcurrency(n)` is honored when YAML leaves the field unset.

### onStepFailure

Controls what happens to dependents when a step fails.

| Value | Behavior |
| --- | --- |
| `cascade` (default) | Steps that depend on the failed step (transitively) fail with the same error. |
| `skip-dependents` | Dependent steps are marked `skipped`. Independent branches continue. |
| `abort` | The whole workflow stops. Running steps may be cancelled. |

```yaml
options:
  onStepFailure: skip-dependents
```

### timeout / stepTimeout

Both follow the [Duration format](#duration-format). `timeout` bounds the workflow; `stepTimeout` provides a default for any step that does not set its own `timeout`.

```yaml
options:
  timeout: "2h"
  stepTimeout: "30m"
```

A zero duration (`"0s"`) is valid syntactically; the executor's interpretation of zero is implementation-defined.

### isolation

Free-form string. The schema validates the type (`string`) only; the runtime decides what values mean. The reference engine recognizes `none` (the default) and `worktree-per-step` (each step runs in its own git worktree, see [Concepts / Step Isolation](/concepts/step-isolation)). Custom embedders may register additional values.

```yaml
options:
  isolation: "worktree-per-step"
```

### scheduler

Picks the scheduling algorithm.

| Value | Behavior |
| --- | --- |
| `dependency-first` (default) | Dispatch steps in topological order, respecting `dependsOn`. |
| `round-robin` | Round-robin across ready steps. |
| `least-busy` | Prefer steps whose agents have the smallest active workload. |

```yaml
options:
  scheduler: dependency-first
```

## Duration format

Duration values are strings using a subset of Go's `time.Duration` format. Hours, minutes, and seconds are supported; milliseconds and below are not.

**Pattern**: `^(\d+h)?(\d+m)?(\d+s)?$`, `minLength: 2`.

| Valid | Invalid |
| --- | --- |
| `"30m"` | `"30"` (no unit) |
| `"1h30m"` | `"1.5h"` (no fractions) |
| `"45s"` | `"500ms"` (no ms) |
| `"1h0m30s"` | `""` (`minLength: 2`) |
| `"0s"` | `"1us"` (no microseconds) |

Parse left to right: each `<digits><unit>` group multiplies digits by the unit and sums. The spec does not constrain a maximum; implementations should reject durations exceeding their platform limits.

## Full example

A complete workflow exercising every top-level field:

```yaml
name: full-featured
version: 1
description: |
  Demonstrates every top-level workflow field: agents, steps, includes,
  and options.

agents:
  architect:
    description: "Technical lead who designs APIs."
    model: "bedrock/anthropic.claude-sonnet-4-6"
    temperature: 0.3
    maxTurns: 8
  coder:
    description: "Developer who implements designs."
    tools: ["read", "write", "bash"]

includes:
  deploy: "workflows/deploy.yaml"

steps:
  - id: design
    agent: architect
    instructions: "@instructions/design-rest-api.md"

  - id: implement
    agent: coder
    instructions: "Build the API server."
    dependsOn: [design]

  - id: deploy-staging
    include: deploy
    dependsOn: [implement]
    timeout: "20m"

options:
  maxConcurrency: 3
  onStepFailure: skip-dependents
  timeout: "1h"
  stepTimeout: "20m"
  scheduler: dependency-first
  isolation: "worktree-per-step"
```

## Validation

The CLI runs structural validation (JSON Schema) plus the validator-only rules (cycle detection, referential integrity, mutual exclusion):

```bash
zenflow validate workflow.yaml
# ✓ Valid
```

Exit codes from `zenflow validate`:

- `0` valid
- `2` validation error (schema or validator rule)
- `3` usage error (missing argument)

For a pure schema check (no validator rules) without the Go binary, see the [ajv-cli recipe in the YAML overview](./#with-ajv-cli).
