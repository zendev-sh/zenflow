---
title: Failure Handling
description: 'A workflow has many ways to go wrong: an agent hits its turn cap, a tool call returns an error, the LLM provider rate-limits, the user cancels...'
---

# Failure Handling

A workflow has many ways to go wrong: an agent hits its turn cap, a tool call returns an error, the LLM provider rate-limits, the user cancels with Ctrl-C. Zenflow surfaces every failure mode through typed statuses, drop reasons, and a small number of failure strategies.

## Step status

Every step ends in one of four terminal statuses (`StepStatus`):

| Status | Meaning |
|--------|---------|
| `completed` | The step finished successfully. |
| `failed` | The step encountered an error after retries were exhausted. |
| `skipped` | The step's `condition` evaluated to false, or a dependency failed under `skip-dependents` strategy. |
| `cancelled` | The workflow aborted (cascading from a failed step under `cascade` strategy, or workflow-level abort). |

`StepResult.Error` is non-nil for `failed` only. For `skipped` and `cancelled`, the step never started; there is no error.

## Workflow status

`WorkflowResult.Status` is derived from the step statuses (`WorkflowStatus`):

| Status | When |
|--------|------|
| `completed` | All steps `completed`. |
| `partial` | At least one step `completed` and at least one step `failed`. |
| `failed` | No step `completed`. |
| `running` | Transient - only seen on intermediate snapshots, never on a returned `WorkflowResult`. |

The CLI exits non-zero on `failed` and `partial`, zero on `completed`. Library callers should check `result.Status` explicitly.

## Failure strategies

`options.onStepFailure` controls how a failed step affects its dependents:

- **`cascade`** (default): when a step fails, every dependent (transitively) is marked `cancelled`. Independent branches of the DAG continue to run.
- **`skip-dependents`**: when a step fails, every direct dependent is marked `skipped`. Skips can themselves cascade to their dependents according to each step's `condition`.
- **`abort`**: when any step fails, the entire workflow stops. Running steps are cancelled.

```yaml
options:
  onStepFailure: skip-dependents
```

`cascade` is right when downstream steps are tightly coupled (you cannot "deploy" without "build"). `skip-dependents` is right when steps are loosely coupled and you want unrelated branches to keep running. `abort` is right for high-stakes flows where a partial completion is worse than no completion.

## Retries

`step.retries` is a per-step retry budget for LLM-side failures (transient provider errors, schema validation failures on `submit_result` after `maxTurns`).

```yaml
steps:
  - id: deploy
    agent: deployer
    instructions: "Deploy the service."
    retries: 2
    timeout: "5m"
```

Retries re-run the entire step from the start (fresh agent loop, fresh tool calls). For loops, `retries` re-runs the whole loop from iteration 0 - there is no per-iteration retry.

::: warning Retry mechanics under verification
The retry path (around the executor's per-step loop) is being verified for correctness across the provider matrix. Treat `retries: N` as best-effort: the API surface is stable and works on the happy path, but edge cases (provider-side rate limits with non-retryable errors, retry-after header handling) are not yet production-grade. Production deployments should rely on the provider's own retry layer ([goai](https://goai.sh)'s per-provider retry policies) for transient transport errors and use `step.retries` only for LLM-content failures (validation, structured output mishaps).
:::

## Per-step timeout

`step.timeout` is the wall-clock cap for the step's agent loop. After timeout, the agent's context is cancelled and the step transitions to `failed`.

```yaml
steps:
  - id: long-task
    timeout: "10m"
```

For loop steps, `timeout` applies to all iterations combined. To bound an individual iteration, set `timeout` on inner steps or use `options.stepTimeout` (workflow-default per-step cap).

::: warning Per-step timeout under verification
Behaviour is correct on the happy path: agent responds before the deadline → step succeeds; agent runs over → context cancellation propagates and the step fails. Edge cases (timeout firing mid-tool-call with side effects, timeout interaction with retries) are still being verified. Use it for soft caps; do not rely on it for hard cleanup of mid-flight tool side effects.
:::

## Drop reasons

Beyond step-level failure, the routing layer can drop messages without ever delivering them. Every drop emits one `EventMessageDropped` with `Data["reason"]` set to one of the `DropReason` constants.

| `DropReason` | Wire string | Meaning |
|---|---|---|
| `DropReasonWorkflowCancelled` | `workflow-cancelled` | Workflow context cancelled (or `abort` fired) before delivery. |
| `DropReasonTargetTerminal` | `target-terminal` | Send to a step whose mailbox is closed (step finished). |
| `DropReasonUnknownStep` | `unknown-step` | Send to a step ID that was never registered. |
| `DropReasonMailboxClosedByFinalize` | `mailbox-closed-by-finalize` | Send raced a `Close`. The router cannot determine whether the message landed before `Close` took effect, so it emits this reason for ANY observed race. Treat as advisory: the message may have been delivered. Over-reports rather than under-reports. |
| `DropReasonMaxWakeCycles` | `max-wake-cycles` | Wake-loop hit the `maxWakeCycles` cap with messages still pending. |
| `DropReasonHoldTimeout` | `hold-timeout` | Executor's hold-timeout fired before the step's termination invariants converged. |
| `DropReasonMailboxFull` | `mailbox-full` | Bounded mailbox is at the `WithMaxMailboxSize` cap. Newest message rejected. |
| `DropReasonNoTranscript` | `no-transcript` | Resume attempt on terminated step, but no saved transcript exists. |
| `DropReasonTranscriptTooLarge` | `transcript-too-large` | Saved transcript exceeds the configured size cap. |
| `DropReasonResumeShutdown` | `resume-shutdown` | Workflow context cancelled mid-resume. |
| `DropReasonResolverError` | `resolver-error` | Configured `ModelResolver` returned an error during resume. |

Drops never silently lose work: every drop produces exactly one `EventMessageDropped`. To capture them programmatically, install `WithDropCallback(fn func(DropEvent))`. The callback runs synchronously by default; set `WithDropCallbackBufferSize(n)` to dispatch through a buffered channel.

## Error propagation

`StepResult.Error` carries the failure cause:

```go
result, err := orch.RunFlow(ctx, wf)
for stepID, sr := range result.Steps {
    if sr.Status == zenflow.StepFailed {
        fmt.Printf("step %s failed: %v\n", stepID, sr.Error)
    }
}
```

`RunFlow`'s top-level `error` is non-nil only for orchestrator-level setup failures (no LLM provider, malformed workflow that bypassed parse-time checks). Step-level failures go into `result.Steps[id].Error` and are reflected in `result.Status`. Always inspect both.

## Coordinator awareness

The coordinator sees failures via `EventError` events in its mailbox; successful completions arrive as `EventStepEnd`. The error event payload includes the failing step ID, the agent name, and an error message. The coordinator can:

- Narrate the failure for the user.
- Forward context to a recovery step that depends on the failed one.
- Decide nothing more needs to happen and exit naturally.

The coordinator does not have authority to change a step's status, retry it, or veto a downstream cancellation. The DAG executor owns that.

## Partial completion

`StatusPartial` is common in DAGs with `skip-dependents`. Some branches succeed, others skip, and the workflow returns a mix. Partial is a meaningful end state: it tells the caller "I did what I could, here's the breakdown".

For CI use cases that should treat partial as failure, check `result.Status` explicitly:

```go
if result.Status != zenflow.StatusCompleted {
    os.Exit(1)
}
```

The CLI does this by default - partial flows exit non-zero.

## Cancellation

Two ways a workflow can be cancelled:

1. **External**: the caller cancels `ctx`. `RunFlow` returns promptly; in-flight steps see context cancellation and transition to `failed` (with `context.Canceled` as the error). Pending steps never start.
2. **Self-abort**: `options.onStepFailure: abort` fires after a step fails. The executor cancels the workflow's internal context.

Cancellation is cooperative. A tool that ignores `ctx` can run past a cancellation; the executor will not kill the goroutine forcibly. Tools that perform long blocking work should honour `ctx.Done()`.

### Coordinator exit conditions

Distinct from workflow cancellation:

- **Coordinator wake-cap exhaustion**: the coordinator's wake loop hits its `WithCoordMaxWakeCycles(n)` integer cap (default 100). Remaining mailbox messages drop; the executor continues running the DAG without further coordinator narration. This is NOT a workflow cancellation - the workflow runs to completion.

## Cross-links

- [DAG scheduling](/concepts/dag-scheduling) - how the scheduler walks the graph and what `cascade` / `skip-dependents` do at the graph level
- [Messaging](/concepts/messaging) - drop semantics in routing
- [Resume](/concepts/resume) - resuming failed / cancelled / skipped steps
- [Observability](/concepts/observability) - capturing drops and step failures via sinks
- [API: Options](/api/options) - `WithDropCallback`, `WithDropCallbackBufferSize`
