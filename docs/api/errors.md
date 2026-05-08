---
title: Errors
description: 'zenflow surfaces errors at three layers: typed sentinel error values, DropReason codes for messaging failures, and CLI exit codes for the zenflow...'
---

# Errors

zenflow surfaces errors at three layers: typed sentinel `error` values, `DropReason` codes for messaging failures, and CLI exit codes for the `zenflow` binary.

## Sentinel errors

Use `errors.Is(err, X)` to classify; `errors.As(err, &X)` to extract a typed wrapper.

### Orchestrator lifecycle

- `zenflow.ErrOrchestratorClosed` - `RunAgent` / `RunAgentAsync` was called on an orchestrator after `Close()` ran. The orchestrator's background goroutines are gone and its handle registry is drained; new calls would leak resources with no lifecycle to attach to. **Fix:** construct a fresh `Orchestrator`, or remove the stale entry from your factory cache so the next call rebuilds it.
- `zenflow.ErrModelRequired` - `RunFlow` / `RunGoal` / `RunAgent` was called on an orchestrator that has no `provider.LanguageModel` configured. **Fix:** pass `WithModel(...)` at orchestrator construction, or set `cfg.Model` per call.
- `zenflow.ErrStorageRequired` - `ResumeFlow` was called on an orchestrator that has no `Storage` configured. **Fix:** pass `WithStorage(...)`.
- `zenflow.ErrWorkflowNil` - `RunFlow` / `ResumeFlow` was passed a nil `*Workflow`. **Fix:** load the workflow via `LoadWorkflow` or construct one explicitly before calling.
- `zenflow.ErrPlanDenied` - `RunGoal`'s LLM-decomposed plan was rejected by the configured `ApprovalHandler` (the handler returned `false`). Distinct from `ErrApprovalTimeout` (the handler ran but exceeded its window). The flow is aborted cleanly with no executor side effects.
- `zenflow.ErrApprovalTimeout` - the configured `ApprovalHandler.ApprovePlan` did not return within `WithApprovalTimeout`'s window. The flow is aborted; the handler's late return, if any, is ignored.
- `zenflow.ErrNilAgentHandle` / `zenflow.ErrNilOrchestrator` - returned by methods called on a nil receiver (defensive guards for callers that race a Close with concurrent use).
- `zenflow.ErrResumeNoModel` - `Executor.ResumeStep` could not construct an agent runner because neither the saved transcript nor the executor had a model resolver. **Fix:** install `WithModelResolver` or ensure the executor's default runner model matches the transcript.

### Async agent handles

`AgentError` wraps a sentinel with optional human-readable text. `errors.Is(AgentError{Sentinel: X}, X)` returns true.

- `zenflow.ErrAgentHandleTimeout` - the async handle exceeded its TTL (default 30 minutes; `DefaultAgentHandleTTL`). The agent goroutine is cancelled via its context; its later arrival, if any, is discarded. **Override the TTL.** SDK consumers: call `zenflow.WithAgentHandleTTL(d)`. CLI users: set `ZENFLOW_AGENT_HANDLE_TTL` env var (the CLI maps it to the option; the library never reads env vars directly).
- `zenflow.ErrAgentCancelled` - the handle was cancelled via `AgentHandle.Cancel()` before the agent completed. Subsequent `Done()` reads see this sentinel.
- `zenflow.ErrAgentPanicked` - the agent goroutine recovered a panic. The recovered value is in `AgentError.Msg`. Inspect it before retrying - panics usually indicate a real bug, not a transient failure.

### Storage cleanup

In zenflow, "storage cleanup" means evicting per-run state from the configured `Storage` backend after the caller has finished consuming the `WorkflowResult`. The in-memory `*MemoryStorage` accumulates metadata, per-step results, and shared-memory entries for every run it has seen; without explicit eviction, long-lived embedders (HTTP servers, queue workers) leak that state across runs. `*MemoryStorage` exposes `DeleteRun(runID string)` (no ctx, no error return) to evict a completed run's metadata, step results, and shared memory from the in-process map. Call it after consuming `WorkflowResult` in long-lived embedders to bound memory growth. Idempotent: no-op when `runID` is unknown.

### Transcript store

- `zenflow.ErrNoTranscript` - `Load` found no matching transcript. Returned as `DropReasonNoTranscript` when a Send to a closed step has no history to resume from. Typically observed for steps that ran before the transcript store was wired up, or whose transcript was explicitly deleted.
- `zenflow.ErrTranscriptTooLarge` - an Append would exceed the configured cap (`WithMaxTranscriptMessages` / `WithMaxTranscriptBytes`). The messages are NOT appended; the store's slot is sealed. Routed to `DropReasonTranscriptTooLarge` on subsequent resume attempts. **To preserve operability** (at the cost of potentially-incomplete history), call `WithTruncationOnCapReached()` (paired with `WithoutTruncationOnCapReached()` to disable) and use a store that implements `TranscriptTruncatedLoader`.

### Resume path

- `zenflow.ErrResumeShutdown` - the workflow's context was cancelled mid-resume; the in-flight resume goroutine exited early. Routed to `DropReasonResumeShutdown`. Typically observed when a long-running resume races against a workflow timeout or explicit cancel.
- `zenflow.ErrModelResolverMissing` - a saved transcript references a model identifier that does not match the executor's default runner model and no `ModelResolver` was configured to resolve it. Without a resolver, the resume fails loudly rather than silently falling back to the wrong model. **Fix:** install `WithModelResolver` that maps the saved model string to the right `provider.LanguageModel`.
- `zenflow.ErrModelResolverError` - a `ModelResolver` was configured but returned an error (or returned `nil` model with no error). Routed to `DropReasonResolverError`. Distinct from `ErrModelResolverMissing` so operators can tell "no resolver installed" from "resolver ran and failed".
- `zenflow.ErrMailboxFullOnResume` - a queued resume attempt was rejected because the active resume's mailbox was already at its configured cap. Routed to `DropReasonMailboxFull`.

### Coordinator tool arguments

The four built-in coordinator tools (`forward_to_agent`, `send_message`, `narrate`, `finalize`) validate their inputs before doing anything. Argument failures bubble back to the LLM as a tool error, so the model can self-correct on the next step.

- `zenflow.ErrForwardTargetRequired` - the coordinator called `forward_to_agent` without a `target_step_id` (or with an empty string). **When you'll see it:** a coord prompt regression where the model emits `{"text": "..."}` and forgets the target. Surfaces as a tool error in the next coord turn; the model usually retries with the right arg. Repeat offenses suggest the system prompt needs a stronger reminder.
- `zenflow.ErrSendMessageEmpty` - the coordinator called `send_message` (hub broadcast) with empty `text`. **When you'll see it:** prompt drift where the model invokes the tool with no payload. The tool refuses; the next coord turn includes the error and the model can retry.
- `zenflow.ErrNarrateEmpty` - the coordinator called `narrate` with empty `text`. **When you'll see it:** same shape as `ErrSendMessageEmpty` - the model invoked the narration tool without supplying narration text. Refused; next turn carries the error.

### Agent runner

`AgentRunner` enforces the agent contract (turn cap, terminal `submit_result`, handle identity). All sentinels here are wrapped by `AgentError` when surfaced through `AgentHandle.Done()`.

- `zenflow.ErrAgentToolDirectInvocation` - an agent-as-tool definition was invoked directly through the goai tool loop instead of being intercepted by the spawner hook. **When you'll see it:** a misconfigured orchestrator where `OnBeforeToolExecute` did not register the agent spawner, so the call fell through to the default `Execute` body. **Fix:** ensure the orchestrator's spawner hook is wired before `RunFlow` / `RunAgent`; never call `agentTool.Execute` from custom code.
- `zenflow.ErrAgentTurnLimitExceeded` - the agent ran the configured `MaxTurns` LLM calls without ever calling `submit_result`. **When you'll see it:** a runaway agent that keeps thinking aloud or invoking tools instead of terminating. **Fix:** raise the turn cap via `WithMaxTurns(n)` if the task genuinely needs more steps, otherwise tighten the agent prompt to push toward `submit_result` sooner.
- `zenflow.ErrAgentNoSubmitResult` - the agent finished its turn budget (or returned `finishReason=stop`) but never called `submit_result`, despite a `resultSchema` being configured. **When you'll see it:** the agent emitted a final assistant message in plain text and stopped. The last assistant text is appended to the error message for diagnostic context. **Fix:** strengthen the system prompt's "you MUST call submit_result" clause, or supply a fallback handler.
- `zenflow.ErrInvalidAgentHandleID` - `NewAgentHandle` was called with an empty ID. **When you'll see it:** a custom embedder constructing handles by hand and forgetting to populate the ID; never seen via the standard `RunAgentAsync` path (which generates IDs). **Fix:** pass a non-empty stable ID (UUID or step-derived string) to `NewAgentHandle`.

### Orchestrator wiring

These guards fire when a consumer of the orchestrator API supplied a missing or malformed argument. They are returned synchronously from the calling method; no goroutines are spawned and no events are emitted.

- `zenflow.ErrRunnerNil` - `Executor.Run` was invoked on an `Executor` whose `Runner` field is `nil`. **When you'll see it:** custom embedders that build an `Executor` directly (instead of going through `Orchestrator`) and forgot to set the runner. **Fix:** prefer `Orchestrator`, which wires the runner internally; if you must use `Executor` directly, set `exec.Runner = ...` before calling `Run`.
- `zenflow.ErrEmptyGoal` - `RunGoal` was called with an empty (or whitespace-only) goal string. **When you'll see it:** a CLI consumer that forwards user input without trimming, or a programmatic caller that builds the goal from a template that resolved to an empty string. **Fix:** validate the goal is non-empty before calling `RunGoal`.
- `zenflow.ErrRunNotFound` - `Storage.LoadRun` was called with a run ID the configured backend has never seen. Wrapped with the run ID for context. **When you'll see it:** a `ResumeFlow` against a run that was never persisted, or a stale run ID from a previous storage backend. **Fix:** verify the run ID exists via `Storage.ListRuns` before resuming.
- `zenflow.ErrStepNotFound` - `Storage.LoadStepResult` was asked for a step that has no persisted result yet (the step never completed, or never ran). Wrapped with `runID/stepID`. **When you'll see it:** custom resume logic that probes step results before checking the run's step graph. **Fix:** consult `WorkflowResult.Steps` for the canonical list of completed steps before loading by ID.
- `zenflow.ErrIncludePathEscape` - a step's `include:` reference resolved to a path outside the workflow's base directory (via `..` traversal or a leading `/`). Wrapped with the offending step ID and ref. **When you'll see it:** a workflow attempting to pull a system file (e.g. `/etc/passwd`) or escape into a sibling directory. **Fix:** keep `include:` paths relative and inside the workflow root; if cross-directory inclusion is genuinely needed, set a different `BaseDir`.
- `zenflow.ErrIncludeDepthExceeded` - an `include:` chain exceeded `MaxIncludeDepth` (5). Wrapped with step ID and ref. **When you'll see it:** an accidental cycle (A includes B includes A) or a deeply nested fragment library. **Fix:** flatten the include graph; the cap protects against infinite recursion and is not configurable on purpose.
- `zenflow.ErrRefPathEscape` - an `@`-prefixed file ref (in step input or agent prompt) resolved to a path outside the workflow's base directory. Wrapped with the offending path. **When you'll see it:** same shape as `ErrIncludePathEscape`, but for inline `@./file.txt` refs rather than `include:` blocks. **Fix:** keep ref paths relative and inside the workflow root.
- `zenflow.ErrNilFactoryInner` - `NewFactoryCache(nil)` was called. **When you'll see it:** custom plumbing that wraps an inner factory but forgot to construct it first. **Fix:** pass a real factory; the cache is a memoizer, it has no useful behavior wrapping `nil`.

### Permission policy

`DecidePermission` and the orchestrator's permission gate return these sentinels (wrapped) when a tool call is blocked. The CLI's interactive prompt does not fire these - they're for declarative `--allow` / `--deny` / `--strict` decisions.

- `zenflow.ErrToolDenied` - a tool name matched the policy's `Deny` list (typically from `--deny`). **When you'll see it:** an agent attempting to run a tool the operator explicitly forbade. The tool call fails immediately and the LLM sees the error in the next turn. **Fix:** if the deny is intentional, no action - the agent will route around it. If the deny was a misconfiguration, remove the offending entry from `--deny`.
- `zenflow.ErrToolNotAllowed` - strict mode is on (`--strict`) and the requested tool is not on the `Allow` list. **When you'll see it:** an agent calling a tool the operator never explicitly allowed under strict mode. Distinct from `ErrToolDenied` so operators can route alerts separately ("explicit deny" vs "not in allowlist"). **Fix:** add the tool to `--allow` if it should be permitted, otherwise let the agent fail and re-plan.

### MessageRouter

The router exposes one bare sentinel for back-pressure conditions; most router failures are surfaced through `DropEvent` / `DropError` instead (see `## DropReason`).

- `zenflow.ErrMailboxFull` - a `Send` was rejected because the target's bounded mailbox is at its `MaxMailboxSize` cap (oldest-wins fairness). **When you'll see it:** production deployments that set a positive cap and have a producer outpacing the consumer. Surfaced both as this sentinel (for direct callers) and as `DropReasonMailboxFull` (for observers via `WithDropCallback` / `EventMessageDropped`). **Fix:** raise `WithMaxMailboxSize`, slow the producer, or speed up the consumer.

## DropReason

Every router message that fails to reach its target's mailbox produces exactly one `DropEvent` with a typed `DropReason`. **There are no silent drops.** Subscribers receive both `EventMessageDropped` (via `ProgressSink`) and the optional `WithDropCallback` invocation.

`DropReason.String()` returns the canonical wire-format value used in `Event.Data["reason"]`. The values are stable and safe to match on in dashboards / alert rules.

| Constant | Wire string | Cause | Mitigation |
| --- | --- | --- | --- |
| `DropReasonUnspecified` | `"unspecified"` | Zero value; never emitted in practice | None - if you see this, file a bug. |
| `DropReasonWorkflowCancelled` | `"workflow-cancelled"` | Workflow context cancelled (or the abort strategy fired) before the message could be delivered | Expected during clean cancellation. Investigate only if seen on a successful run. |
| `DropReasonTargetTerminal` | `"target-terminal"` | Send to a step whose mailbox was closed (the step reached terminal lifecycle state) | Common in flows where coord narrates after step completion. Avoid by routing late messages to coord instead, or by enabling resume so terminated steps can re-engage. |
| `DropReasonUnknownStep` | `"unknown-step"` | Send to a stepID that was never registered AND has no pending senders | Typo in step ID, message routed to a step that doesn't exist in the DAG, or send to an external identity (e.g., `"coordinator"`) that wasn't pre-registered via `WithExternalInbox`. |
| `DropReasonMailboxClosedByFinalize` | `"mailbox-closed-by-finalize"` | Mailbox raced with a concurrent close; the closed flag won | Race during step termination. Usually benign - if observed frequently, check for senders that don't open a sender slot via `MessageRouter.OpenSender`. |
| `DropReasonMaxWakeCycles` | `"max-wake-cycles"` | The wake-loop hit the `MaxWakeCycles` cap with messages still pending; remaining messages drained as drops | Raise `WithMaxWakeCycles` (default 10; coord default 100). If raising doesn't help, investigate the producer for a hot loop. |
| `DropReasonHoldTimeout` | `"hold-timeout"` | The hold-timeout fired before the agent's three-invariant termination rule could converge; remaining mailbox messages drained as drops | Raise `WithHoldTimeout` (default 30 seconds) for chat-style workflows; lower it for batch pipelines where idle gaps signal stuck steps. |
| `DropReasonMailboxFull` | `"mailbox-full"` | The bounded mailbox is at the `MaxMailboxSize` cap; the newest message is rejected (oldest-wins fairness) | Raise `WithMaxMailboxSize`, slow the producer, or speed up the consumer. Default is unbounded; production deployments should set a positive cap. |
| `DropReasonNoTranscript` | `"no-transcript"` | Target mailbox was closed AND the executor's `TranscriptStore` has no saved transcript for the step | Step ran before transcript wiring, or the transcript was explicitly deleted. Resume cannot proceed. |
| `DropReasonTranscriptTooLarge` | `"transcript-too-large"` | The saved transcript exceeds the configured cap (`WithMaxTranscriptMessages` / `WithMaxTranscriptBytes`); resume would exceed the size bound | Raise the caps, prune transcript history, or enable `WithTruncationOnCapReached()` (paired with `WithoutTruncationOnCapReached()` to disable) to fall back to a truncated tail. |
| `DropReasonResumeShutdown` | `"resume-shutdown"` | The workflow context was cancelled mid-resume; the in-flight resume goroutine exited early | Expected during clean shutdown. The original sender sees the drop and can retry on the next workflow run. |
| `DropReasonResolverError` | `"resolver-error"` | A configured `ModelResolver` was consulted for a saved-transcript model identifier and returned an error | Inspect resolver logs - likely a misconfigured provider, missing API key, or transient infra failure. Distinct from the catch-all `target-terminal` so operators can route resolver alerts separately. |

### `DropError`

`MessageRouter.Send` returns `*zenflow.DropError` on every drop. The error's `Error()` method returns the canonical `"dropped: <reason>"` string (matching `DropReason.String()`) so existing substring-matching consumers - like LLM tool results that pass `err.Error()` through verbatim - continue to work without modification.

For routing-decision callers that need to act on a specific reason, extract the typed value via `errors.As`:

```go
if err := router.Send(stepID, msg); err != nil {
    var de *zenflow.DropError
    if errors.As(err, &de) {
        switch de.Reason {
        case zenflow.DropReasonUnknownStep:
            // append "valid step IDs: …" hint, retry with corrected target
        case zenflow.DropReasonMailboxFull:
            // back off + retry; the consumer is behind
        case zenflow.DropReasonTargetTerminal:
            // step has finished; route the message somewhere else
        default:
            // log and surface the canonical text via err.Error()
        }
    }
    return err
}
```

The struct definition is intentionally minimal:

```go
type DropError struct {
    Reason DropReason
}
```

`DropError.Reason` is the typed enum, not a string - immune to format-string drift if `DropReason.String()` is ever revised. The same `DropReason` is also surfaced through the `WithDropCallback(fn)` observer (typed `DropEvent.Reason`) and through `EventMessageDropped` events (canonical wire string in `Event.Data["reason"]`).

### Detecting cancelled vs failed

When iterating `WorkflowResult.Steps`:

- `StepStatus == StepCompleted` - the step succeeded.
- `StepStatus == StepFailed` - the step errored. `StepResult.Error` is non-nil; check `errors.Is(sr.Error, ctx.Err())` to distinguish workflow-level cancellation from step-level errors.
- `StepStatus == StepCancelled` - the step was cancelled because the workflow was aborted (under the `cascade` strategy) or its context was cancelled before completion.
- `StepStatus == StepSkipped` - the step was skipped because a dependency failed (under the `skip-dependents` strategy) or its `Condition` evaluated false.

For workflow-level state:

```go
result, err := orch.RunFlow(ctx, wf)
switch {
case errors.Is(err, context.Canceled):
    // Workflow was cancelled by the caller.
case errors.Is(err, context.DeadlineExceeded):
    // Workflow ran past its context deadline.
case err != nil:
    // Configuration or storage error; result may be nil.
case result.Status == zenflow.StatusCompleted:
    // All steps succeeded.
case result.Status == zenflow.StatusFailed:
    // No steps completed.
case result.Status == zenflow.StatusPartial:
    // Mixed: walk result.Steps for the per-step breakdown.
}
```

### Error wrapping conventions

zenflow follows standard Go error-wrapping conventions:

- Public methods wrap underlying errors via `fmt.Errorf("...: %w", err)` so `errors.Is` and `errors.As` traverse the chain.
- Sentinel values (e.g., `ErrOrchestratorClosed`) are returned directly without wrapping when no further context is meaningful.
- Typed wrappers (`AgentError`, `JSONParseError`, `CoordinatorValidationError`) embed a sentinel via `Unwrap()` so `errors.Is(err, sentinel)` works on a `*CoordinatorValidationError`.

## Workflow validation errors

`LoadWorkflow` and `ParseWorkflow` return typed error structs when YAML parses but fails schema/topology validation. Callers should `errors.As` to extract structured fields (which step ID, which dependency, etc.) for tooling.

| Type | Fields | Meaning |
| --- | --- | --- |
| `*ValidationError` | `Message string` | Generic validation error (e.g., "step \"x\": retries must be non-negative"). Catch-all for rules that don't have their own type. |
| `*CycleError` | `Message string` | Topological sort detected a cycle in `dependsOn`. Message names the offending edge. |
| `*MissingAgentError` | `Message string`, `Agent string`, `StepID string` | Step references an agent name that's not declared in `agents:`. |
| `*DuplicateStepError` | `Message string`, `StepID string` | Two steps share the same `id`. |
| `*MissingDepError` | `Message string`, `Dep string`, `StepID string` | Step's `dependsOn` lists a step ID that doesn't exist. |
| `*NoStepsError` | `Message string` | Workflow has zero steps. |
| `*MissingNameError` | `Message string` | Workflow `name:` field is empty. |
| `*IncludeConflictError` | `Message string`, `StepID string`, `Field string` | An `include:` reference collides with an inline step ID or another include. |
| `*LoopValidationError` | `Message string`, `StepID string` | A `loop:` block violates one of the loop-specific rules (e.g., `forEach` mutually exclusive with `until`, invalid `outputMode`). |

## Coordinator validation errors

`RunGoal` calls into the coordinator LLM to decompose a goal into a workflow. Failures during that decomposition return typed wrappers around the underlying cause:

| Type | Fields | Meaning |
| --- | --- | --- |
| `*JSONParseError` | `Err error` (Unwrap) | The coordinator's response was not valid JSON. The wrapped error is the JSON decoder's message. |
| `*CoordinatorValidationError` | `Err error` (Unwrap) | The coordinator returned valid JSON but the workflow it produced failed `validate()`. The wrapped error is one of the workflow validation errors above. |
| `*ToolNotFoundError` | `Tool string`, `Agent string` | The coordinator referenced a tool name in an `agents.<name>.tools:` list that isn't registered with the orchestrator. |

`RunGoal` retries up to 2 times on `JSONParseError` and 1 time on `CoordinatorValidationError` before giving up.

## Portability errors

`LintPortability` and `SanitizeUnicode` return typed errors so callers can decide whether to surface them as warnings or fatal:

| Type | Fields | Meaning |
| --- | --- | --- |
| `*HostSpecificEnvError` | `Field string`, `Var string` | Workflow string interpolates a host-specific env var (`$USER`, `$HOSTNAME`, `$HOME`, `$PWD`). The workflow won't load on another host. |
| `*UnicodeUnsafeError` | `Reason string`, `Rune rune` | Workflow text contains a Unicode bidi-override codepoint (U+202A-E, U+2066-9). These can hide malicious content from human review. |

## CLI exit codes

The `zenflow` CLI translates internal failures to stable exit codes (defined in `cmd/zenflow/main.go`):

| Code | Meaning | When it fires |
| --- | --- | --- |
| `0` | Success | Workflow / goal / agent completed without errors; all steps reached `StepCompleted`. |
| `1` | Workflow failed | `WorkflowResult.Status` is `StatusFailed` or `StatusPartial`; one or more steps did not complete successfully. |
| `2` | Validation/coordinator error | Invalid YAML, schema rejection, `JSONParseError`, `CoordinatorValidationError`, `ToolNotFoundError`. |
| `3` | Usage error | Unknown flag, missing positional argument, `--resume` on goal/agent, `--plan` on goal/agent, mutually exclusive flags supplied together. The `usage()` line is printed to stderr. |
| `124` | Watchdog timeout | The `--timeout` value was exceeded. The watchdog grace period (default 30 seconds) gave clean shutdown a chance; on grace expiry the process exits via `os.Exit(124)`. Orphan goroutines, if any, are killed with the process. |

`continue-on-error` style wrappers in CI should treat `0` as pass and any other code as fail. If you want to distinguish workflow failure (`1`) from setup failure (`2`/`3`), check the exit code explicitly.

### `124` and partial output

When the watchdog fires, any progress events emitted before timeout have already been written to stdout (and to file when `--json` redirected to one). The watchdog kills the process; it does not flush stdio buffers. For NDJSON consumers, this means the last line of output may be truncated mid-event. Defensive parsers should skip non-parseable trailing lines.

### Retries

zenflow does not retry the CLI invocation itself - one CLI run = one execution. Retries within a workflow happen at the step level, configured via `Step.Retries` / `Step.MaxRetries` / `WorkflowOptions.MaxRetries`. The CLI exit code reflects the final status after retries are exhausted.

If your CI orchestrator retries failed jobs automatically, prefer narrow retries (transient API errors, timeouts) over broad ones (configuration errors). Exit code `3` rarely benefits from a retry; exit code `124` sometimes does (transient slowness); exit code `1` depends on the failed step.

## Diagnostic patterns

A few patterns that come up often when debugging zenflow runs.

### "I'm seeing message_dropped events but my workflow finished"

That's expected in two scenarios:

1. **Coordinator narrating after step completion.** The coord LLM tries to push a narration to a step that already terminated. Reason: `target-terminal`. Benign - the message had no business reaching a finished step, and the coord can keep narrating the next step.
2. **Workflow cancellation flush.** When a workflow context is cancelled, every still-pending message in every mailbox is flushed as a drop with reason `workflow-cancelled`. This satisfies the "no silent drops" contract and lets observers see exactly what was lost.

If you see drops with reason `unknown-step`, `mailbox-full`, or `max-wake-cycles`, that's worth investigating - they signal misrouting, back-pressure, or a hot loop respectively.

### "RunFlow returned successfully but `result.Steps` says some steps failed"

`RunFlow` only returns an error for orchestrator-level failures (nil workflow, missing model). Storage write failures are NOT propagated through this return value - they surface as `EventError` events and `slog.Warn` log entries. Subscribe to events to detect them. Per-step failures show up in the `Steps` map - the workflow completed, but with a `StatusPartial` or `StatusFailed` overall status.

Always inspect both the `error` return and `result.Status` before declaring success:

```go
result, err := orch.RunFlow(ctx, wf)
if err != nil {
    return fmt.Errorf("run flow: %w", err)
}
if result.Status != zenflow.StatusCompleted {
    return fmt.Errorf("workflow finished with status %s", result.Status)
}
```

### "RunAgentAsync handle never delivers a result"

Three things to check:

1. **TTL expired.** The handle was force-completed with `AgentError{Sentinel: ErrAgentHandleTimeout}`. Read it from `Done()` to confirm.
2. **Orchestrator was Closed.** New `RunAgentAsync` calls return `ErrOrchestratorClosed` synchronously; existing handles are force-cancelled. If you're seeing handles that never resolve, you may be reading from a stale handle whose buffered result was already consumed.
3. **Internal goroutine reading from `Done()`.** Only the external caller should read from `AgentHandle.Done()`. Internal lifecycle code that needs to know "the handle is terminal" should use the unexported `finished` channel pattern (see `agent_handle.go`); using `Done()` would steal the buffered result.

### "Resume keeps failing with no-transcript"

Resume requires both a `TranscriptStore` (default `InMemoryTranscriptStore` for intra-run, persistent store for cross-run) AND for the original step to have actually appended messages. Steps that error out before any LLM call still get metadata seeded (so the resume reconstructs the system prompt and model), but a step that crashes immediately may have no messages to load.

If you need cross-process resume (different CLI runs, different containers), wire a persistent `TranscriptStore` via `WithTranscriptStore`. The default in-memory store is per-orchestrator and lost on exit.
