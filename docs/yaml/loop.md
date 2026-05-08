---
title: Loop
description: 'The loop field on a step turns it into an iterating block. There are two mutually exclusive modes:'
---

# Loop

The `loop` field on a step turns it into an iterating block. There are two mutually exclusive modes:

- **forEach** - parallel map over an array. Each element produces one iteration.
- **repeat-until** - sequential iterations until a stop condition fires (or `maxIterations` exhausts).

Authoritative source: [`spec/v1/spec.md` §6](https://github.com/zendev-sh/zenflow/blob/main/spec/v1/spec.md#6-loops). Structural contract: [`spec/v1/schema.json` `$defs.Loop`](https://github.com/zendev-sh/zenflow/blob/main/spec/v1/schema.json).

## Field summary

| Field | Type | Used by | Notes |
| --- | --- | --- | --- |
| `forEach` | string \| array | forEach | CEL expression returning an array, or a static array literal. |
| `maxConcurrency` | integer | forEach | Throttles parallel iterations. `minimum: 0` (`0` is equivalent to omitting the field; treated as unset = all parallel). Default: all parallel. |
| `maxIterations` | integer | repeat-until | Required for repeat-until. `minimum: 1`. |
| `until` | string | repeat-until | CEL expression. Loop stops when true. |
| `untilAgent` | string | repeat-until | Agent name acting as judge. Loop stops when the judge sets `result.done = true`. |
| `delay` | Duration | repeat-until | Wait between iterations. |
| `outputMode` | enum | both | `last` (default) or `cumulative`. Controls the loop step's `content`. The empty string `""` is also accepted (back-compat alias for `last`). |
| `steps` | array[Step] | both | Inner steps for multi-step loops. `minItems: 1`. If absent, the parent step itself is looped. |

`additionalProperties: false`. `minProperties: 1` - an empty `loop:` block is rejected.

## Mutual exclusion

- `forEach` rules out `until`, `untilAgent`, `maxIterations`, and `delay`.
- A loop without `forEach` is a repeat-until and must declare `maxIterations`.
- Inner steps inside `loop.steps` must not carry their own `loop` field. Nested loops are not supported in v1; the validator enforces this even though the JSON Schema (which uses `$ref`) is structurally recursive.

## forEach mode

Maps over an array in parallel. Array length determines the iteration count.

### Static array

```yaml
- id: review-repos
  agent: reviewer
  instructions: "Review this repo."
  loop:
    forEach: ["repo-a", "repo-b", "repo-c"]
    maxConcurrency: 3
```

`item` is bound to the current element. `index` is the zero-based iteration number. Iterations run in parallel up to `maxConcurrency` (default: all parallel).

### Dynamic array from a step output

`forEach` can also be a CEL expression returning an array. The expression sees `steps.<id>.result` for any dependency step, so a discovery step can hand the loop a list to fan out over.

```yaml
agents:
  discovery:
    description: "Lists deployable services."
    resultSchema:
      type: object
      required: [services]
      properties:
        services:
          type: array
          items:
            type: object
            properties:
              name: { type: string }
              region: { type: string }

steps:
  - id: list-services
    agent: discovery
    instructions: "List all microservices that need deployment."

  - id: deploy-each
    dependsOn: [list-services]
    loop:
      forEach: "steps.list-services.result.services"
      maxConcurrency: 3
      steps:
        - id: deploy
          agent: deployer
          instructions: "Deploy this service to its target region."
        - id: verify
          agent: verifier
          dependsOn: [deploy]
          instructions: "Run health checks."
```

The discovery step uses a `resultSchema` so its output is structured; `deploy-each.loop.forEach` reads `steps.list-services.result.services` directly. No JSON parsing of free-form text is needed.

### Item injection

For each iteration, the executor binds `item` and `index` into the agent context. The injection format is:

```
## forEach Item (index: 0)
{"name": "auth", "region": "us-east-1"}
```

`item` can be any JSON type; string and object are the most common. There is no template syntax (no `{{item}}` substitution); the value is delivered via context injection.

### Step ID namespacing

forEach iterations produce namespaced step IDs:

- Single-step loop: `{step-id}[{index}]` (e.g., `review-repos[0]`).
- Multi-step loop: `{step-id}[{index}].{inner-id}` (e.g., `deploy-each[0].verify`).

Outside the loop, you reference the loop step itself (`deploy-each`); the namespaced IDs are visible in events and logs.

### Constraints

- An empty static array literal is rejected.
- A CEL `forEach` that evaluates to a non-array value fails the loop.
- `maxConcurrency: 1` forces sequential forEach execution, which is occasionally useful when iterations share a non-isolated resource.

## Repeat-until mode

Runs iterations sequentially. Each iteration sees the previous iteration's content and (if applicable) result.

```yaml
- id: health-check
  instructions: "Check service health."
  loop:
    maxIterations: 10
    until: "content.contains('healthy')"
    delay: "30s"
```

`maxIterations` is required; without it the executor cannot guarantee termination. `delay` waits between iterations. Both are repeat-until-only; using either with `forEach` is rejected.

### until and untilAgent

A repeat-until loop has two stop signals:

- `until` - CEL expression evaluated after each iteration. Loop stops when true.
- `untilAgent` - named agent that judges whether to stop. Loop stops when the judge sets `result.done = true`.

Either, both, or neither may be present. Evaluation order: `until` first; if true, the loop stops. Otherwise the judge runs; if `result.done = true`, the loop stops. If neither fires, the next iteration begins (subject to `maxIterations`).

If neither `until` nor `untilAgent` is set, the loop runs exactly `maxIterations` times.

```yaml
agents:
  judge:
    description: "Decides if the review cycle is complete."
    resultSchema:
      type: object
      required: [done]
      properties:
        done: { type: boolean }
        reason: { type: string }

steps:
  - id: dev-cycle
    loop:
      maxIterations: 5
      untilAgent: judge
      until: "steps.review.content.contains('LGTM')"
      delay: "10s"
      steps:
        - id: implement
          agent: coder
          instructions: "Implement or fix the rate limiter."
        - id: review
          agent: reviewer
          dependsOn: [implement]
          instructions: "Review the implementation. Output LGTM if ready."
```

### untilAgent contract

When you reference an agent via `untilAgent`, the validator enforces:

1. The agent exists in the `agents` map.
2. The agent has a `resultSchema`.
3. That schema has `properties.done` with `type: "boolean"`.
4. `"done"` is in the schema's top-level `required` array.

At runtime, the executor injects a `submit_result` tool whose parameters equal the judge's `resultSchema`. The judge's textual output becomes its `content`; the structured decision lives in `result`. The executor reads `result.done` to decide whether to stop. `result.reason` is optional but recommended for observability.

If the judge fails to call `submit_result` in an iteration (turn cap or `end_turn`), the loop continues to the next iteration with the judge's text but no structured result. This is intentional fail-open behavior - the regular fail-hard rule for `resultSchema` would abort an entire loop on a single judge glitch, while the loop already has `maxIterations` as a hard bound.

For the full rationale, see [`spec.md` §6 untilAgent Semantics](https://github.com/zendev-sh/zenflow/blob/main/spec/v1/spec.md#untilagent-semantics).

### Iteration variables

Inside a repeat-until iteration, CEL has access to the following variables. All variables below capture the just-completed iteration.

| Variable | Scope | Description |
| --- | --- | --- |
| `iteration` | always | Zero-based iteration number. |
| `content` | single-step loop | Text content of the step. |
| `result` | single-step loop | Structured result of the step. |
| `status` | single-step loop | Status of the step. |
| `steps.<id>.content` | multi-step loop | Text content of an inner step. |
| `steps.<id>.status` | multi-step loop | Status of an inner step. |
| `steps.<id>.result` | multi-step loop | Structured result of an inner step. |

The bare `content` and `result` are available only in **single-step** repeat-until loops (no inner `steps` array). In a multi-step loop, address inner steps via `steps.<inner-id>`.

## Multi-step loop bodies

Both modes can carry an inner `steps` array. Inner steps form their own DAG with their own `dependsOn` namespace.

```yaml
- id: dev-cycle
  loop:
    maxIterations: 5
    untilAgent: judge
    steps:
      - id: implement
        agent: coder
        instructions: "Implement or fix the code."

      - id: review
        agent: reviewer
        dependsOn: [implement]
        instructions: "Review the implementation."
```

Rules for inner DAGs:

- Inner step IDs must be unique within the loop scope.
- Inner `dependsOn` must reference inner siblings only.
- An inner step must not have `loop` (no nesting in v1).
- An outer step's `dependsOn` cannot reach into a loop's inner namespace, and inner steps cannot reach out.

To inject one iteration's outputs into the next, address the inner step from inside an inner CEL expression: `steps.review.content`, `steps.review.result.<field>`. Across iterations, the values reflect the just-completed iteration.

## outputMode

Controls what the loop step's outer `content` contains after the loop finishes.

| Value | Behavior |
| --- | --- |
| `""` / `"last"` / `"cumulative"` (default = `"last"`; empty string is back-compat alias for `"last"`) | `last`: the loop step's `content` is the last iteration's terminal output. `cumulative`: concatenated history of every iteration. |

```yaml
- id: dev-cycle
  loop:
    maxIterations: 3
    untilAgent: judge
    outputMode: cumulative
    steps:
      - id: worker
        agent: worker
        instructions: "Make progress on stage {{iteration}}."
```

`outputMode: last` matches the v1 default (and pre-`outputMode` behavior). Use `cumulative` when downstream steps need the full iteration log; the trade-off is a larger `content` payload.

## Interaction with other step fields

| Combo | Behavior |
| --- | --- |
| `condition` + `loop` | Condition runs first. False skips the entire loop. |
| `timeout` + `loop` | Timeout applies to all iterations combined, not per iteration. For per-iteration timeouts, set `timeout` on inner steps (or use `options.stepTimeout`). |
| `retries` + `loop` | Retries apply to the entire loop block; failure restarts from iteration 0. |

## Worked examples

### forEach (loop-foreach.yaml)

```yaml
name: loop-foreach
description: Discover services and deploy each one in parallel.

agents:
  discovery:
    description: "Lists deployable microservices."
    resultSchema:
      type: object
      required: [services]
      properties:
        services:
          type: array
          items:
            type: object
            properties:
              name: { type: string }
              region: { type: string }
  deployer:
    description: "Deploys a single service."
    tools: ["bash"]
  verifier:
    description: "Runs health checks."
    tools: ["bash"]

steps:
  - id: list_services
    agent: discovery
    instructions: "List all microservices that need deployment."

  - id: deploy_each
    dependsOn: [list_services]
    loop:
      forEach: "steps.list_services.result.services"
      maxConcurrency: 3
      steps:
        - id: deploy
          agent: deployer
          instructions: "Deploy this service to its target region."
        - id: verify
          agent: verifier
          dependsOn: [deploy]
          instructions: "Run health checks on the deployed service."

  - id: summary
    agent: discovery
    dependsOn: [deploy_each]
    instructions: "Summarize all deployment results."
```

### Repeat-until (loop-repeat-until.yaml)

```yaml
name: loop-repeat-until
description: Iterative code-review-fix cycle until the reviewer approves.

agents:
  coder:
    description: "Developer who writes and fixes Go code."
    tools: ["read", "write", "bash"]
  reviewer:
    description: "Code reviewer."
    tools: ["read"]
  judge:
    description: "Decides if the review cycle is done."
    resultSchema:
      type: object
      required: [done]
      properties:
        done: { type: boolean }
        reason: { type: string }

steps:
  - id: design
    agent: coder
    instructions: "Design the API for a rate-limiter package."

  - id: dev_cycle
    dependsOn: [design]
    loop:
      maxIterations: 5
      untilAgent: judge
      until: "steps.review.content.contains('LGTM')"
      delay: "10s"
      steps:
        - id: implement
          agent: coder
          instructions: "Implement or fix the rate-limiter."
        - id: review
          agent: reviewer
          dependsOn: [implement]
          instructions: "Review the implementation. Output LGTM if ready."

options:
  onStepFailure: abort
```

### Bidirectional messaging inside a repeat-until loop

The reference [`loop-bidirectional.yaml`](https://github.com/zendev-sh/zenflow/blob/main/spec/v1/examples/loop-bidirectional.yaml) example exercises hub-and-spoke messaging across iterations: each iteration's worker sends progress to the coordinator, and the coordinator forwards context-update messages back into the active iteration. Because the loop step namespaces inner step IDs as `loop-stages.<index>.<inner-id>`, the coordinator can address either bare `worker` or namespaced `loop-stages.0.worker` and the router delegates correctly.

```yaml
name: loop-bidirectional

agents:
  worker:
    description: "Worker that reports progress and reads coord updates."
  judge:
    description: "Judge: stop after worker reports on all 3 stages."
    resultSchema:
      type: object
      required: [done]
      properties:
        done: { type: boolean }

steps:
  - id: setup
    agent: worker
    instructions: |
      Workflow setup. Reply EXACTLY with "READY".

  - id: loop-stages
    dependsOn: [setup]
    loop:
      maxIterations: 3
      untilAgent: judge
      outputMode: cumulative
      steps:
        - id: worker
          agent: worker
          instructions: |
            Report on stage {{iteration}}. Use send_message to push progress
            to the coordinator. Read any context updates from your inbox.
```

For a deeper walkthrough of messaging and addressing, see [Concepts / Messaging](/concepts/messaging).
