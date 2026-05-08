---
title: Loops
description: 'A loop turns a single step into a sub-DAG that runs many times. Two modes:'
---

# Loops

A loop turns a single step into a sub-DAG that runs many times. Two modes:

- **`forEach`** - parallel iteration over an array. Each element produces one iteration.
- **repeat-until** - sequential iterations until a CEL expression or a judge agent says stop.

Both modes are configured under the `loop:` key on a step. They are mutually exclusive: a `loop` either has `forEach` (forEach mode) or `maxIterations` (repeat-until mode), never both.

## Repeat-until

Runs iterations sequentially. Each iteration sees the previous iteration's content and result. Right for refine-style flows: code, review, fix, review, fix, until reviewer approves.

```yaml
- id: dev-cycle
  dependsOn: [design]
  loop:
    maxIterations: 5
    untilAgent: judge
    until: "steps.review.content.contains('LGTM')"
    delay: "10s"
    steps:
      - id: implement
        agent: coder
        instructions: "Implement or fix the code based on review feedback."
      - id: review
        agent: reviewer
        instructions: "Review the implementation. Output LGTM if ready, or list issues to fix."
        dependsOn: [implement]
```

### Required and optional fields

| Field | Required | Description |
|-------|----------|-------------|
| `maxIterations` | Yes | Hard cap. Prevents infinite loops. |
| `until` | No | CEL expression evaluated after each iteration. Loop stops when true. |
| `untilAgent` | No | Named agent acts as judge after each iteration. Loop stops when judge's `result.done` is true. |
| `delay` | No | Wait between iterations. |
| `steps` | No | Inner steps for multi-step loops. If absent, the parent step itself is looped. |

`until` and `untilAgent` may both be present. Evaluation order: `until` first; if true, loop stops. Otherwise the judge runs; if `result.done` is true, loop stops. If neither is satisfied (or neither is set), the next iteration begins. If both are absent, the loop runs exactly `maxIterations` times. `maxIterations` is required for repeat-until (the schema rejects the loop without it). If you somehow construct a repeat-until loop without `maxIterations` (the schema would reject this normally, but library callers building a `Workflow` struct directly could bypass), the executor's safety cap of 100 iterations is the last line of defense - it returns the last result without a step error. In practice, the schema rejection fires first.

### Single-step vs multi-step

A repeat-until loop without inner `steps` repeats the parent step itself:

```yaml
- id: health-check
  agent: monitor
  instructions: "Check service health."
  loop:
    maxIterations: 10
    until: "content.contains('healthy')"
    delay: "30s"
```

A multi-step loop runs an inner sub-DAG each iteration:

```yaml
- id: dev-cycle
  loop:
    maxIterations: 5
    untilAgent: judge
    steps:
      - id: implement
        agent: coder
      - id: review
        agent: reviewer
        dependsOn: [implement]
```

The inner DAG follows the same scheduling rules as the top-level DAG. `dependsOn` references inside `loop.steps` resolve within the loop's scope only.

### `untilAgent` semantics

The named agent acts as a judge after each iteration:

1. Parser checks the agent has a `resultSchema` with `properties.done` of type boolean and `done` in `required`.
2. The judge sees the iteration outputs as context.
3. The executor injects a `submit_result` tool whose schema is the judge's `resultSchema`.
4. The judge calls `submit_result({done: ..., reason: ...})`. The call terminates the judge's conversation.
5. The executor reads `result.done`. True means stop; false means continue.

If the judge fails to call `submit_result` (exhausts maxTurns, model error, schema mismatch), the loop continues to the next iteration. This is "fail-open" behaviour: a single judge glitch does not abort the loop. The judge's content is still visible to the next iteration's agents.

Why fail-open for judges, fail-hard for regular agents: a regular step with `resultSchema` has an explicit contract - failing silently would hide a broken agent. A judge is a secondary evaluator within a loop already bounded by `maxIterations`. Fail-hard would abort the whole loop on a single glitch.

## ForEach

Maps over an array in parallel. Each element produces one iteration.

```yaml
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

### Required and optional fields

| Field | Required | Description |
|-------|----------|-------------|
| `forEach` | Yes | The array. CEL expression evaluating to an array, or a static array literal. |
| `maxConcurrency` | No | Max parallel iterations. Default: all parallel. |
| `steps` | No | Inner sub-DAG. Without it, the parent step itself is looped. |

`forEach` is mutually exclusive with `maxIterations`, `until`, `untilAgent`, and `delay`. Array length determines iteration count.

### Static vs dynamic arrays

Static array literal:

```yaml
loop:
  forEach: ["service-a", "service-b", "service-c"]
  maxConcurrency: 3
```

Dynamic array from a previous step's structured `result`:

```yaml
loop:
  forEach: "steps.list-services.result.services"
  maxConcurrency: 3
```

The CEL expression is evaluated once at scheduling time. The result must be an array.

## Item injection

Each iteration receives context variables in CEL and in the agent prompt:

| Variable | Scope | Description |
|----------|-------|-------------|
| `item` | forEach | Current element from the array. Any JSON type (string, object, number, etc.). |
| `index` | forEach | Zero-based iteration index. |
| `iteration` | repeat-until | Zero-based iteration number. |
| `content` | single-step repeat-until | Content of the previous iteration. |
| `result` | single-step repeat-until | Structured result of the previous iteration. |
| `steps.<id>.content` / `.result` / `.status` | multi-step | Inner step results from the just-completed iteration. |

The agent prompt for forEach iterations gets the item injected automatically:

```
## forEach Item (index: 0)
{"name": "auth", "region": "us-east-1"}
```

No template syntax (`{{item}}`) is needed.

## Output modes

A loop produces one consolidated `StepResult` for the parent step. `loop.outputMode` controls how that result's `content` is constructed:

- `last` (default) - `content` is the final iteration's content. Right for refine-style loops where downstream consumers want the polished output.
- `cumulative` - `content` is every iteration's output concatenated, plus judge feedback for `untilAgent` loops. Right for aggregator-style loops where downstream consumers need the full history.

```yaml
- id: rounds
  loop:
    maxIterations: 3
    outputMode: cumulative
    steps:
      - id: round
        agent: speaker
        instructions: "Add to the running discussion."

- id: synthesise
  agent: synthesiser
  dependsOn: [rounds]
  instructions: "Synthesise the discussion. You will see all rounds in context."
```

When `outputMode: cumulative`, the parent step's content bypasses the per-dependency 16 KB truncation cap that normal step outputs go through. The overall 120 KB prompt cap still applies.

## Namespaced step IDs

Inner steps inside a loop are namespaced at runtime:

| Container | Namespace pattern | Example |
|-----------|-------------------|---------|
| Repeat-until iteration N | `parentLoopID.N.innerStepID` | `dev-cycle.0.implement` |
| forEach iteration N | `parentLoopID[N].innerStepID` | `deploy-each[0].deploy` |

These namespaced IDs surface in messages, events, and `forward_to_agent` addressing. The coordinator sees them in event payloads (`step=`, `from=`) and must mirror the same form when forwarding back. See [Messaging](/concepts/messaging) for addressing rules.

## Parallel iterations and `maxConcurrency`

ForEach iterations run in parallel up to `loop.maxConcurrency`. The orchestrator-level `WithMaxConcurrency(n)` cap also applies; the effective parallelism is the minimum.

Inner DAGs within an iteration follow normal `dependsOn` scheduling: in the example above, `deploy-each[0].verify` waits for `deploy-each[0].deploy`, and similarly for every iteration. The two iterations run concurrently with each other.

## Worked example: bidirectional messaging in a loop

```yaml
name: loop-bidirectional
agents:
  worker:
    description: "Worker that performs a task and sends progress updates."
  judge:
    description: "Judge: stop after 3 stages."
    resultSchema:
      type: object
      required: [done]
      properties:
        done:
          type: boolean

steps:
  - id: setup
    agent: worker
    instructions: 'Reply EXACTLY with "READY".'

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
            Iteration N. Read your inbox for any coordinator context.
            Call send_message with "STAGE_<N>: <task description>"
            Reply with EXACTLY "STAGE_<N>_DONE".
```

What happens:

1. **`setup`** runs, replies `READY`, completes.
2. **`loop-stages`** begins its first iteration. The inner step gets the runtime ID `loop-stages.0.worker`. The coordinator sees `EventStepStart{step="loop-stages.0.worker"}`.
3. The worker drains its inbox (empty on first iteration), calls `send_message("STAGE_1: ...")`. The send goes to the coordinator's inbox.
4. Coordinator wakes, sees the message from `loop-stages.0.worker`, optionally narrates and / or forwards context back via `forward_to_agent("loop-stages.0.worker", "...")`.
5. Worker finishes iteration 0. Judge runs, decides `done: false`, loop continues.
6. **Iteration 1** starts as `loop-stages.1.worker`. The judge's previous content and result are visible. The worker reads its inbox (which may now hold a forward from the coordinator), proceeds.
7. After iteration 2, judge returns `done: true`, loop stops.

The coordinator's `forward_to_agent` accepts either the bare name (`worker`) or the namespaced runtime ID (`loop-stages.0.worker`); both are routed via root router delegation.

## Cross-links

- [DAG scheduling](/concepts/dag-scheduling) - inner sub-DAG scheduling rules
- [Conditions](/concepts/conditions) - skipping the whole loop based on a condition
- [Structured output](/concepts/structured-output) - the `submit_result` mechanism the judge uses
- [Messaging](/concepts/messaging) - namespaced addressing for inner steps
- [YAML: Loop](/yaml/loop) - field-by-field reference
