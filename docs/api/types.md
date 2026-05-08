---
title: Types
description: Reference for every public value type in the zenflow package. Types are grouped by concern; for option helpers see Options; for *Orchestrator...
---

# Types

Reference for every public value type in the zenflow package. Types are grouped by concern; for option helpers see [Options](./options); for `*Orchestrator` methods see [Core Functions](./core-functions).

All types live in package `github.com/zendev-sh/zenflow` unless noted otherwise (sink helpers live in `github.com/zendev-sh/zenflow/sink`).

## Workflow definition

### `Workflow`

```go
type Workflow struct {
    Name        string
    Description string
    Version     int
    Agents      map[string]AgentConfig
    Includes    map[string]string
    Steps       []Step
    Options     WorkflowOptions
    BaseDir     string // set by LoadWorkflow; empty for programmatic use
}
```

Top-level structure for a workflow. Fields that map directly to YAML/JSON keys carry the obvious tags; `BaseDir` is set by `LoadWorkflow` to the directory of the source file so relative `contextFiles` resolve against it.

A workflow without `Steps` is valid only as a sub-workflow target for `Includes`.

### `Step`

```go
type Step struct {
    ID           string
    Agent        string
    Instructions string
    DependsOn    []string
    ContextFiles []string
    Model        string   // overrides the agent's default model
    Timeout      Duration
    Retries      int
    MaxRetries   *int
    Condition    *string  // CEL expression
    Include      string   // sub-workflow reference
    Loop         *Loop
}
```

A single node in the workflow DAG.

- `ID` must match `^[a-zA-Z][a-zA-Z0-9_-]*$`. Validated by `LoadWorkflow`.
- `Agent` references a key in `Workflow.Agents`. Empty string falls back to the default agent.
- `DependsOn` lists step IDs that must complete before this step runs. Cycles are rejected at parse time.
- `ContextFiles` paths are resolved against `Workflow.BaseDir` and read into the step's prompt as additional context.
- `Timeout`, `Retries`, `MaxRetries` are per-step overrides; `WorkflowOptions` carries workflow-wide defaults.
- `Condition` is a CEL expression evaluated against the workflow's variables and previous step outputs; false means skip.
- `Include` references a sub-workflow by name (looked up in `Workflow.Includes`).
- `Loop` makes this step iterative (see below).

### `Loop`

```go
type Loop struct {
    MaxIterations  *int
    Until          *string  // CEL stop condition
    UntilAgent     string   // judge agent for stop decision
    ForEach        any      // iterable: list, range, or CEL expression
    MaxConcurrency int
    Delay          Duration
    Steps          []Step
    OutputMode     LoopOutputMode   // "last" (default) or "cumulative"
}
```

`OutputMode` is a typed string for compile-time safety; use the named constants below.

Iteration config for a step. The most common shapes:

- `ForEach: [...]` + `Steps: [...]` - run the inner steps once per item.
- `MaxIterations: ptr(N)` + `Until: "...condition..."` - run up to N times or until the condition is true.
- `UntilAgent: "judge"` - delegate the stop decision to a judge agent.

`OutputMode` controls how the loop step's `StepResult.Content` is constructed for downstream consumers:

- `"last"` (or empty, the default) - content is the last inner step of the final iteration only. Right for refine-style loops.
- `"cumulative"` - content concatenates every iteration's output (plus judge feedback when `UntilAgent` is set). Right for aggregator loops where downstream needs the full history.

```go
const (
    LoopOutputModeLast       LoopOutputMode = "last"
    LoopOutputModeCumulative LoopOutputMode = "cumulative"
)
```

### `AgentConfig`

```go
type AgentConfig struct {
    // YAML/JSON-declared:
    Description     string
    Prompt          string
    Model           string
    Tools           []string  // names; resolved from orchestrator catalog
    DisallowedTools []string
    MaxTurns        int
    Temperature     *float64
    TopP            *float64
    ResultSchema    map[string]any

    // Per-call (never serialized):
    Name            string
    CallTools       []goai.Tool   // resolved tool values; overrides Tools
    ProgressSink    ProgressSink
    SubagentToolSet string        // advisory label
    Attachments     []provider.Part // multimodal: text fragments, image refs, PDF bytes
    SessionID       string        // for ListAgentHandles partitioning
}
```

Defines a named agent's role, model, and tool access. Two halves to the struct:

- **YAML-declared** fields (above the `// Per-call` divider) describe a workflow-level agent. `Tools` is a list of tool NAMES because workflow YAML can only carry strings; the orchestrator resolves them against its registered tool catalog.
- **Per-call** fields are populated at `RunAgent` / `RunAgentAsync` time to override orchestrator defaults for one invocation. `CallTools` carries resolved `[]goai.Tool` values; `ProgressSink` overrides `WithProgress`; `Attachments` carries multimodal `provider.Part` values (text snippets, image refs, PDF byte payloads) the agent loop concatenates onto the user message at the start of the run; `SessionID` keys the live-handle registry for `ListAgentHandles`.

`Tools` and `DisallowedTools` are workflow-only fields - they are read by the executor when filtering tools for a workflow-declared agent, and ignored on the `RunAgent` path (use `CallTools` there).

### `WorkflowOptions`

```go
type WorkflowOptions struct {
    MaxConcurrency int
    MaxRetries     *int
    OnStepFailure  FailureStrategy   // "cascade" | "skip-dependents" | "abort"
    Timeout        Duration
    StepTimeout    Duration
    Isolation      string
    Scheduler      SchedulerStrategy   // "dependency-first" | "round-robin" | "least-busy"
}
```

Workflow-wide execution knobs. Per-step values override these. `OnStepFailure` and `Scheduler` are typed strings for compile-time safety; use the named constants below.

Failure strategy constants:

```go
const (
    FailureCascade        FailureStrategy = "cascade"
    FailureSkipDependents FailureStrategy = "skip-dependents"
    FailureAbort          FailureStrategy = "abort"
)
```

Scheduler strategy constants:

```go
const (
    SchedulerDependencyFirst SchedulerStrategy = "dependency-first"
    SchedulerRoundRobin      SchedulerStrategy = "round-robin"
    SchedulerLeastBusy       SchedulerStrategy = "least-busy"
)
```

### `Duration`

```go
type Duration time.Duration
```

Wraps `time.Duration` with YAML/JSON string parsing (`"30s"`, `"5m"`, etc.). Used in `Step.Timeout`, `Loop.Delay`, `WorkflowOptions.Timeout`.

## Run results

### `WorkflowResult`

```go
type WorkflowResult struct {
    RunID    string
    Status   WorkflowStatus
    Steps    map[string]*StepResult
    Duration time.Duration
    Tokens   provider.Usage
    Summary  string
}
```

Outcome of a `RunFlow` / `RunGoal` / `ResumeFlow` call.

- `RunID` - the run identifier; same value flows through every event's `RunID` field.
- `Status` - terminal workflow state (see `WorkflowStatus` below).
- `Steps` - map keyed by step ID. Note this is a `map`, so iteration order is non-deterministic; if you need ordered output, walk `wf.Steps` and look up by ID.
- `Duration` - wall clock from start to terminal status.
- `Tokens` - aggregated `provider.Usage` across every LLM call (steps + coordinator).
- `Summary` - human-readable summary produced by the coordinator's `finalize` tool. Empty when no coordinator was installed or the coord did not call `finalize`.

### `StepResult`

```go
type StepResult struct {
    ID              string
    Status          StepStatus
    Content         string
    Result          map[string]any
    Tokens          provider.Usage
    Duration        time.Duration
    Error           error
    PreserveContent bool
}
```

Outcome of a single step.

- `Content` - the agent's final assistant text (after the last LLM turn).
- `Result` - structured output if `AgentConfig.ResultSchema` was set on the agent; nil otherwise.
- `Tokens` - per-step LLM usage.
- `Error` - non-nil if the step entered `StepFailed`. Error text describes whether the failure was a tool error, a model error, or a downstream cascade.
- `PreserveContent` - signals to the prompt assembler that this step's content was intentionally aggregated (e.g., by a `cumulative`-mode loop) and must not be subject to per-dep truncation. The overall prompt cap still applies as a final safety net.

### `WorkflowStatus`

```go
type WorkflowStatus string

const (
    StatusRunning   WorkflowStatus = "running"
    StatusCompleted WorkflowStatus = "completed"
    StatusFailed    WorkflowStatus = "failed"
    StatusPartial   WorkflowStatus = "partial"
)
```

- `StatusRunning` - workflow currently executing (visible only in mid-run progress events; `RunFlow` never returns this).
- `StatusCompleted` - all steps reached `StepCompleted`.
- `StatusFailed` - workflow failed with no completed steps (e.g., the first step errored under the `abort` strategy).
- `StatusPartial` - some steps completed but at least one failed or was skipped.

### `StepStatus`

```go
type StepStatus string

const (
    StepCompleted StepStatus = "completed"
    StepFailed    StepStatus = "failed"
    StepSkipped   StepStatus = "skipped"
    StepCancelled StepStatus = "cancelled"
)
```

- `StepCompleted` - the step finished successfully.
- `StepFailed` - the step encountered an error (model error, tool error, panic, timeout).
- `StepSkipped` - skipped due to a failed dependency under the `skip-dependents` strategy, or because a `Condition` evaluated false.
- `StepCancelled` - cancelled due to a failed dependency under the `cascade` strategy, or the step was scheduled but the workflow's context was cancelled BEFORE the step could be dispatched. In-flight steps interrupted by cancellation surface as `StepFailed` with `context.Canceled`.

### `Run`

```go
type Run struct {
    ID       string
    Workflow *Workflow
    Status   WorkflowStatus
    Steps    map[string]*StepResult
}
```

A persisted workflow execution instance. Used by `Storage.SaveRun` / `Storage.LoadRun`. Most application code interacts with `WorkflowResult` instead.

## Agent runtime

### `AgentRunner`

```go
type AgentRunner struct { /* all fields unexported */ }

func NewAgentRunner(opts ...RunnerOption) *AgentRunner
```

Executes a single-agent conversation loop with tool calling. Most callers do not construct `AgentRunner` directly - the orchestrator builds them per `RunAgent` / per workflow step. The notable exception is the coordinator, where `NewDefaultCoordRunner(llm, opts...)` returns a runner the caller hosts in their own goroutine.

All fields are unexported. Configure via the `WithRunner*` functional options documented in [Options](./options).

Capabilities the runner holds (set via the corresponding `WithRunner*` options):

- Model, tools, permissions, progress sink, goai option pass-through - same semantics as the orchestrator-level options.
- Streaming and verbose toggles - per-runner overrides of the orchestrator-level streaming knobs.
- Mailbox + wake channel - inter-agent message delivery. When both are configured the runner enters mailbox-mode and consumes inter-agent messages via the Mailbox+Wake protocol; otherwise the runner skips messaging entirely.
- Message router - the `MessageRouter` shared with sibling/child runners.
- Max wake cycles - per-runner cap; falls back to package default.
- Transcript store, model ID, system prompt - resume support; the store persists conversation history per step; the metadata is what `Executor.ResumeStep` consults to reconstruct the invocation.
- Initial messages - pre-loaded conversation history, used by the resume path to replay a saved transcript through standard `AgentRunner.Run` machinery.

Minimal usage example:

```go
runner := zenflow.NewAgentRunner(
    zenflow.WithRunnerModel(model),
    zenflow.WithRunnerTools(tools),
    zenflow.WithRunnerPermissions(permHandler),
    zenflow.WithRunnerProgress(progressSink),
)
result, err := runner.Run(ctx, cfg, userMessage, modelID, tools)
```

The runner's `Run` method is the entry point - it owns the [goai](https://goai.sh) tool loop:

```go
func (r *AgentRunner) Run(
    ctx context.Context,
    cfg AgentConfig,
    userMessage string,
    model string,
    tools []goai.Tool,
    attachments ...provider.Part,
) (*AgentResult, error)
```

### `AgentRunnerOption`

```go
type AgentRunnerOption = RunnerOption
```

**Deprecated:** prefer [`RunnerOption`](./options) for new code. `AgentRunnerOption` is a back-compat alias kept on the public facade so external SDK consumers do not see a breaking rename - the canonical internal name is `RunnerOption` (the older `AgentRunner` prefix was dropped to avoid stutter with the package name). Both spellings resolve to the same underlying type, so `WithRunner*` options accept either form interchangeably. New code should use `RunnerOption`; existing call sites compile unchanged.

### `AgentResult`

```go
type AgentResult struct {
    Content  string
    Result   map[string]any
    Tokens   provider.Usage
    Turns    int
    Status   AgentStatus
    Duration time.Duration
    Error    error
}
```

Outcome of a single agent run. For synchronous `RunAgent`, only `Content`, `Result`, `Tokens`, `Turns`, `Status`, `Duration` are set; errors return as the second return value.

For `RunAgentAsync` handles, the same struct is delivered over `AgentHandle.Done()` and `Error` carries any terminal error - including `AgentError`-wrapped sentinels for TTL timeout, cancel, and panic-recover. **Async consumers MUST check `Error` before trusting the other fields.**

### `AgentStatus`

```go
type AgentStatus string

const (
    AgentStatusCompleted AgentStatus = "completed"
    AgentStatusTruncated AgentStatus = "truncated"
)
```

- `AgentStatusCompleted` - agent finished normally (LLM returned no further tool calls).
- `AgentStatusTruncated` - agent hit its `MaxTurns` cap mid-conversation.

### `AgentHandle`

```go
type AgentHandle struct {
    ID string
    // ... internal fields ...
}

func NewAgentHandle(id string) *AgentHandle
func (h *AgentHandle) Done() <-chan AgentResult
func (h *AgentHandle) Cancel() error
```

Returned by `RunAgentAsync`. The caller drives completion via `Done()` and may force-terminate via `Cancel()`.

- `ID` - stable for the lifetime of the handle. Format: `agent-<UUID v4>`. Flows through every `ProgressSink` event the underlying runner emits.
- `Done()` - read-only channel, buffered size 1, delivers exactly one terminal `AgentResult` then closes. Multiple reads after close yield the zero value.
- `Cancel()` - force-terminates. Subsequent `Done()` reads see `AgentResult{Error: AgentError{Sentinel: ErrAgentCancelled}}`. Idempotent.

`NewAgentHandle` is for tests that construct standalone handles outside `RunAgentAsync`. Production code should always go through `RunAgentAsync`. Panics if `id` is empty.

### `AgentError`

```go
type AgentError struct {
    Sentinel error
    Msg      string
}

func (e AgentError) Error() string
func (e AgentError) Unwrap() error
```

Wraps a sentinel error class with optional human-readable detail. `errors.Is(AgentError{Sentinel: X}, X)` returns true.

Sentinels exposed alongside:

- `ErrAgentHandleTimeout` - the handle exceeded its TTL (default 30 minutes; `DefaultAgentHandleTTL`).
- `ErrAgentCancelled` - cancelled via `AgentHandle.Cancel()`.
- `ErrAgentPanicked` - the agent goroutine recovered a panic; the recovered value is in `Msg`.

`DefaultAgentHandleTTL = 30 * time.Minute`. SDK consumers: call `zenflow.WithAgentHandleTTL(d)`. CLI users may set the `ZENFLOW_AGENT_HANDLE_TTL` env var; the CLI maps it to the option, but the library never reads env vars directly.

## Messaging

### `MessageRouter`

```go
type MessageRouter struct { /* internal fields */ }

func NewMessageRouter() *MessageRouter
func (r *MessageRouter) SetMailbox(store MailboxStore)
func (r *MessageRouter) Mailbox() MailboxStore
func (r *MessageRouter) RegisterStep(stepID string)
func (r *MessageRouter) RegisterInbox(stepID string)
func (r *MessageRouter) RegisterWrapperStep(stepID string)
func (r *MessageRouter) IsWrapperStep(stepID string) bool
func (r *MessageRouter) KnownSteps() []string
func (r *MessageRouter) Send(stepID string, msg RouterMessage) error
func (r *MessageRouter) Close(stepID string)
func (r *MessageRouter) MarkWorkflowCancelled()
func (r *MessageRouter) WorkflowCancelled() bool
// ...
```

Routes `RouterMessage` values from coordinator/agents into the per-step `MailboxStore`. The orchestrator allocates one router per `RunFlow` / `RunAgent` call; integration tests can install an observer via `WithRouterObserver` to grab a handle on the per-call router without polling.

Lifecycle invariants:

- `SetMailbox(store)` must be called before any `Send`. Without a mailbox, `Send` returns an `unknown-step` drop.
- `RegisterInbox(stepID)` marks a step as live so `Send` routes deliveries into the mailbox.
- `Close(stepID)` marks the step terminal: subsequent `Send`s emit `target-terminal` drops.

`Send` returns `nil` on accept and a typed `*DropError` on drop. The error's `Error()` method returns the canonical `"dropped: <reason>"` text, so substring-matching consumers (LLM tool results, log greppers) keep working unchanged. Routing-decision callers should use `errors.As` to extract the typed reason instead of parsing the string:

```go
if err := router.Send(stepID, msg); err != nil {
    var de *zenflow.DropError
    if errors.As(err, &de) {
        switch de.Reason {
        case zenflow.DropReasonUnknownStep:
            // append "valid step IDs: …" hint
        case zenflow.DropReasonMailboxFull:
            // back off + retry
        }
    }
}
```

### `MailboxStore`

```go
type MailboxStore interface {
    Append(stepID string, msg RouterMessage) (id string, err error)
    Unread(stepID string) []RouterMessage
    MarkRead(stepID string, ids []string) []string
    Close(stepID string)
}
```

Per-step inbox storage. `Append` assigns a stable `MessageID` and returns it; `Unread` returns all undrained messages; `MarkRead` is the CAS dedup contract - it returns the subset of IDs that were already marked read on a prior call so concurrent drainers can detect double-consume without holding a lock across LLM calls.

`Close` is the interface's terminal signal: it drops any pending messages and marks the step closed so subsequent `Append` calls are silently dropped and subsequent `Unread` calls return nil.

The default implementation is `*InMemoryMailboxStore`:

```go
func NewInMemoryMailboxStore() *InMemoryMailboxStore

func (s *InMemoryMailboxStore) Seal(stepID string)
```

Custom implementations (SQLite, Redis) are wired via `WithMailboxStore(factory)`.

`Seal` is an `*InMemoryMailboxStore`-only extension (not part of the `MailboxStore` interface). It is the soft-close variant of `Close`: it marks the step terminal so subsequent `Append`s are silently dropped, but unlike `Close` it does NOT delete the existing queue. `Unread` continues to return any messages that were enqueued before the seal so the poller can finish draining. The executor uses this to flush in-flight messages before invoking the hard `Close`. Idempotent: re-sealing an already-sealed (or closed) step is a no-op.

`MailboxLen(store, stepID)` is a free function that returns `(unread, total)` counts when the store implements the optional `Len` method.

### `RouterMessage`

```go
type RouterMessage struct {
    MessageID string
    From      string
    To        string
    Content   string
    Type      RouterMessageType
    Metadata  map[string]string
    Timestamp time.Time
}
```

A message between agents or coordinator.

- `MessageID` - assigned by `MailboxStore.Append` (callers leave it empty). Stable identity for the `MarkRead` CAS contract.
- `From`, `To` - sender / target step IDs. The router is internal to the library, all senders are trusted - `From` is informational only, never authenticated.
- `Type` - classifies the message; see `RouterMessageType` below.
- `Metadata` - arbitrary string map. Reserved key `MetadataKeyResumeReverse` (`"zenflow-resume-reverse"`) marks messages produced by `Executor.runResume` so a Send to a closed target does not cascade into a second resume.

### `RouterMessageType`

```go
type RouterMessageType int

const (
    RouterMessageInfo RouterMessageType = iota
    RouterMessageCancel
    RouterMessageContextUpdate
    RouterMessageResumeReply
)

func (t RouterMessageType) String() string
```

- `RouterMessageInfo` - general informational message.
- `RouterMessageCancel` - request the receiving agent to stop.
- `RouterMessageContextUpdate` - inject new context into the agent's conversation.
- `RouterMessageResumeReply` - reverse-routed reply produced by `Executor.runResume` after a resumed step finishes. Drain logic treats it the same as `RouterMessageInfo` (appended as a user turn) but observers can distinguish it via the type tag.

`String()` returns canonical lowercase forms (`"info"`, `"cancel"`, `"context_update"`, `"resume_reply"`).

### `DropEvent`

```go
type DropEvent struct {
    StepID string
    Msg    RouterMessage
    Reason DropReason
}
```

Describes a single message discarded by the router without ever being appended to the target's mailbox. Surfaced through `WithDropCallback` and through `EventMessageDropped` events on the progress sink. See [Errors](./errors) for the full `DropReason` enum.

### Delivery engine

The delivery engine watches the mailbox for unread messages on active steps and signals each step's wake channel so the runner re-enters its tool loop and drains the inbox. `Orchestrator` wires it inside `Executor.Run`; most consumers never touch the public types listed below.

The pieces are exported so tests and bespoke runtimes (e.g., a multi-process router that owns its own poll loop) can substitute their own active-step source, wake target, registry, or clock without forking the engine.

#### `DeliveryEngine`

```go
type DeliveryEngine struct { /* internal fields */ }

func NewDeliveryEngine(
    source EngineActiveStepsSource,
    mailbox MailboxStore,
    registry EngineWakeRegistry,
    opts ...EngineOption,
) *DeliveryEngine

func (e *DeliveryEngine) TickInterval() time.Duration
func (e *DeliveryEngine) PollOne(stepID string)
```

Per-run goroutine that polls each active step's mailbox on a tick and signals the step's wake channel when there are unread messages. None of `source`, `mailbox`, or `registry` may be nil - a nil dependency would silently no-op the corresponding role and mask wiring bugs. Default tick cadence is 500ms; override via `WithEngineTickInterval`.

`PollOne(stepID)` triggers a single poll cycle without spinning up the loop; tests use it to drive a specific step deterministically.

#### `EngineOption`

```go
type EngineOption func(*DeliveryEngine)

func WithEngineTickInterval(d time.Duration) EngineOption
func WithEngineClock(c EngineClock) EngineOption
func WithStepLocker(l EngineStepLocker) EngineOption
```

Functional-option type for `NewDeliveryEngine`. `WithEngineTickInterval` overrides the 500ms default; non-positive values revert to the default. `WithEngineClock` substitutes the tick source (production uses `RealClock`; tests pass a fake). `WithStepLocker` wires the per-step RWMutex acquirer required for the read-then-wake atomicity invariant - omit it in tests where the invariant does not matter.

#### `EngineActiveStepsSource`

```go
type EngineActiveStepsSource interface {
    ActiveSteps() []string
    AgentState(stepID string) *goai.AgentState
}
```

The minimal subset of `*Executor` the engine reads. Defined as an interface so tests can drop in a fake without standing up an Executor + Workflow + Storage. `ActiveSteps` returns a snapshot of step IDs currently executing; subsequent ticks call again. `AgentState` returns the runner's `*goai.AgentState`, or nil if the step has not been registered (or was unregistered after completion).

#### `EngineWakeTarget`

```go
type EngineWakeTarget interface {
    SignalWake()
}
```

The per-step wake handle the engine signals when a mailbox has unread messages. `SignalWake` MUST be non-blocking: if the wake channel already has a pending signal, the call is a no-op (cap-1 buffer semantics).

#### `EngineWakeRegistry`

```go
type EngineWakeRegistry interface {
    WakeTarget(stepID string) EngineWakeTarget
}
```

Lookup the engine uses to find the wake target for a given stepID. Returns nil when no target is registered (e.g., the step has not yet been admitted into the executor's mailbox path or was already unregistered at end-of-step).

#### `ChanWakeTarget`

```go
type ChanWakeTarget struct { /* internal fields */ }

func NewChanWakeTarget(ch chan struct{}) EngineWakeTarget
```

The production `EngineWakeTarget`: it wraps a buffered `chan struct{}` of capacity 1 (matching the `AgentRunner.Wake` contract). `SignalWake` is non-blocking; a duplicate wake against an already-pending channel is dropped (a single wake suffices to flush the entire mailbox). The channel passed to `NewChanWakeTarget` MUST be buffered with cap >= 1; cap-0 would drop signals deterministically when the agent is mid-LLM-call.

#### `MapWakeRegistry`

```go
type MapWakeRegistry struct { /* internal fields */ }

func NewWakeRegistry() *MapWakeRegistry
func (r *MapWakeRegistry) Register(stepID string, t EngineWakeTarget)
func (r *MapWakeRegistry) Unregister(stepID string)
func (r *MapWakeRegistry) WakeTarget(stepID string) EngineWakeTarget
```

Minimal in-memory `EngineWakeRegistry`. The Executor populates it as steps start (`Register`) and clears entries as they end (`Unregister`); `Register` overwrites any prior entry, useful for retried steps that reallocate their wake channel. Safe for concurrent calls.

#### `EngineClock` and `RealClock`

```go
type EngineClock interface {
    Tick(d time.Duration) <-chan time.Time
    Stop()
}

type RealClock struct { /* internal fields */ }
```

Abstracts `time.Tick` so tests can drive ticks deterministically. `RealClock` wraps `time.NewTicker` and is the production implementation; tests pass a fake clock via `WithEngineClock`. Calling `Tick` twice on the same `RealClock` without an intervening `Stop` cancels the prior ticker so its goroutine and channel are GC'd; not safe for concurrent calls on the same `RealClock`.

#### `EngineStepLocker`

```go
type EngineStepLocker interface {
    AcquireStepLock(stepID string) *sync.RWMutex
}
```

Optional interface the engine uses to acquire a per-step `RWMutex` for the "read-then-wake" atomicity invariant. The poll loop wraps each step's Observe + SignalWake sequence in `RLock`/`RUnlock` so a concurrent `Run`-return defer (which takes the write-lock and transitions to `SetTerminal`) cannot flip state between the read and the wake. Omit (or pass via `WithStepLocker(nil)`) in test contexts where a spurious wake against a freshly-terminated step is harmless.

### `LenAware`

```go
type LenAware interface {
    Len(stepID string) (unread, total int)
}
```

Optional `MailboxStore` extension that exposes the queue length. `unread` is the number of messages currently visible to a subsequent `Unread` call; `total` is `unread` + already-`MarkRead`'d count. Stores that do not retain `MarkRead`'d messages report `total == unread`. The free function `MailboxLen(store, stepID)` is the canonical entry point; it falls back to `len(store.Unread(stepID))` when the store does not implement the interface.

### `ClosedAware`

```go
type ClosedAware interface {
    Closed(stepID string) bool
}
```

Optional `MailboxStore` extension implemented by stores that expose their per-step closed flag. The router uses it to surface "mailbox-closed-by-finalize" drops when a `Close` races a concurrent `Send`.

### `BoundedInMemoryStore`

```go
type BoundedInMemoryStore struct { /* internal fields */ }

func NewBoundedInMemoryStore(maxSize int) *BoundedInMemoryStore
func (b *BoundedInMemoryStore) MaxSize() int
```

A `MailboxStore` that wraps `InMemoryMailboxStore` with a hard per-step cap on queued unread messages. When `Append` would exceed the cap, the new message is rejected and `Append` returns `("", ErrMailboxFull)`; the router maps that to `DropReasonMailboxFull` when emitting `OnDrop`.

::: warning Footgun: non-positive `maxSize` disables the cap
A `maxSize` of `0` or negative makes this store effectively unbounded - equivalent to `NewInMemoryMailboxStore` but with a wrapper that adds no protection. Callers that want a true cap MUST pass a positive value. If you want unbounded behaviour, prefer `NewInMemoryMailboxStore` directly so the intent is explicit at the call site.
:::

`MaxSize()` reports the configured cap; tests use it to verify constructor wiring without reaching into unexported state.

## Resume helpers

The resume mechanism (`Executor.ResumeStep` driven by the router when a closed step receives a new message) is built on three small types. Most embedders never construct them directly - the executor does. They are exported for integration tests and for bespoke runtimes that wire their own resume policy.

### `Resumer`

```go
type Resumer interface {
    CanResume(stepID string) bool
    ResumeStep(ctx context.Context, stepID, prompt, fromAgent string) (*ResumeHandle, error)
}
```

The contract the `MessageRouter` uses to decide whether a `Send` to a closed step should kick off a resume rather than drop. `Executor` implements this and installs itself via `MessageRouter.SetResumer` at the start of `Run`. `CanResume` returns true only when the step has a saved transcript AND the executor is still willing to spawn (run not cancelled). `ResumeStep` loads the transcript, appends `prompt` as a fresh user turn, spawns a fresh `AgentRunner`, and returns a `ResumeHandle`. When the executor has no `TranscriptStore` (mailbox-delivery disabled), `CanResume` always returns false and the router falls back to the pre-resume target-terminal drop path.

### `ResumeHandle`

```go
type ResumeHandle struct {
    StepID         string
    ResumeID       string
    OriginalSender string
    DoneCh         chan struct{}
    Result         string
    Err            error
}
```

Per-resume control block returned by `Resumer.ResumeStep`. Block on `DoneCh` for completion - it is closed once the resume goroutine has finished, either after a successful final assistant response (`Result` populated, `Err == nil`) or on failure (`Err` non-nil). `ResumeID` is the per-invocation identifier emitted on `EventResumeStarted` / `EventResumeCompleted` / `EventResumeFailed` so operators can correlate the three events for the same resume. `OriginalSender` is the `From` field of the router message that triggered the resume; the resumed runner's final assistant response is routed back to this agent via a reverse `RouterMessage` (suppressed when empty).

### `MetadataKeyResumeReverse`

```go
const MetadataKeyResumeReverse = "zenflow-resume-reverse"
```

Sentinel `Metadata` key the executor stamps on the reverse `RouterMessage` it sends after a resume completes. The router checks for this key when handling drops on the reverse path so a closed sender does NOT cascade into a second resume (an infinite-resume guard). Embedders subscribing to dropped messages can use this key to distinguish reverse-path drops from ordinary forward-path drops.

## Persistence

### `Storage`

```go
type Storage interface {
    SaveRun(ctx context.Context, run *Run) error
    LoadRun(ctx context.Context, id string) (*Run, error)
    SaveStepResult(ctx context.Context, runID, stepID string, result *StepResult) error
    LoadStepResult(ctx context.Context, runID, stepID string) (*StepResult, error)
    SaveSharedMemory(ctx context.Context, runID string, entries map[string]string) error
    LoadSharedMemory(ctx context.Context, runID string) (map[string]string, error)
}
```

Persists workflow state for `ResumeFlow` and `WithSharedMemory`. `Save*` are called during execution; `Load*` are used on resume.

`Storage` is the canonical contract every `Executor` backend implements; it is the composition of the three narrower role interfaces below. The split is purely structural - existing implementations satisfy `Storage` unchanged - so callers can depend on the narrowest slice they need.

### `RunStore`

```go
type RunStore interface {
    SaveRun(ctx context.Context, run *Run) error
    LoadRun(ctx context.Context, id string) (*Run, error)
}
```

Narrow role interface: persists workflow `Run` records (lifecycle metadata, status, step roll-ups). Consumers that only need to load or save runs - e.g., a UI that lists historical runs, a test fake that does not care about per-step output - should depend on `RunStore` rather than the wider [`Storage`](#storage).

### `StepResultStore`

```go
type StepResultStore interface {
    SaveStepResult(ctx context.Context, runID, stepID string, result *StepResult) error
    LoadStepResult(ctx context.Context, runID, stepID string) (*StepResult, error)
}
```

Narrow role interface: persists per-step outputs (`StepResult`: status, output payload, error, timing). Useful for components that read step outputs without needing run-level state - e.g., a result-only consumer that streams completed step content into another system. Depend on `StepResultStore` rather than the wider [`Storage`](#storage) when run-level operations are not required.

### `SharedMemoryStore`

```go
type SharedMemoryStore interface {
    SaveSharedMemory(ctx context.Context, runID string, entries map[string]string) error
    LoadSharedMemory(ctx context.Context, runID string) (map[string]string, error)
}
```

Narrow role interface: persists the per-run shared key/value scratchpad that steps and the coordinator use to pass facts between agents. Kept separate from run/step persistence so alternate memory backends (Redis, vector store) can satisfy just this slice without implementing run lifecycle or step-result storage. Pair with [`Storage`](#storage) - or a custom composition - when wiring `WithSharedMemory`.

#### Why three interfaces instead of one

The original `Storage` interface bundled all six methods, forcing test fakes and bespoke backends to stub out unrelated operations even when only one role was exercised. Splitting into `RunStore` + `StepResultStore` + `SharedMemoryStore` lets callers implement only the slice they need:

- A run-listing UI fakes `RunStore` only.
- A SQLite step-result archive implements `StepResultStore` and embeds an in-memory `RunStore` + `SharedMemoryStore`.
- A vector-store memory backend implements `SharedMemoryStore` and reuses the default `MemoryStorage` for the other two roles.

`Storage` itself remains the canonical executor contract, and `*MemoryStorage` / `*FileStorage` continue to satisfy it unchanged - so existing call sites compile and behave identically.

Built-in implementations:

- `NewMemoryStorage() *MemoryStorage` - in-process map. Default; lost when the orchestrator exits.
- `NewFileStorage(baseDir string) *FileStorage` - one JSON file per run/step under `baseDir`. Survives process restarts; safe for cross-process resume when readers and writers agree on the directory. Empty baseDir does NOT panic at construction; the first write fails with an OS error from `os.MkdirAll`. Validate the path before calling.

`*MemoryStorage` exposes one extension method beyond the interface:

```go
func (m *MemoryStorage) DeleteRun(runID string)
```

Removes the run record, every persisted step result for that run, and any shared-memory entries scoped to it. Idempotent: calling with an unknown `runID` is a no-op. Long-lived embedders (web servers, daemons) should call this once a workflow result has been consumed so the in-process map does not grow unbounded. The method is concrete-type only (not on the `Storage` interface) and takes no `context.Context` and returns no error.

### `TranscriptStore`

```go
type TranscriptStore interface {
    Append(runID, stepID string, msgs []provider.Message) error
    Load(runID, stepID string) (*StepTranscript, error)
    Delete(runID, stepID string) error
}
```

Persists per-step conversation history so a terminated step can be resumed with full prior context. The default implementation (`InMemoryTranscriptStore`) covers intra-run resume only; cross-run / cross-process resume requires a persistent backend wired via `WithTranscriptStore`.

`Append` returns `ErrTranscriptTooLarge` when the configured cap is exceeded - the messages are not appended and the caller surfaces `DropReasonTranscriptTooLarge` to the router.

`Load` returns `ErrNoTranscript` when no transcript exists, or `ErrTranscriptTooLarge` when a prior Append sealed the slot.

### `StepTranscript`

```go
type StepTranscript struct {
    StepID       string
    RunID        string
    Messages     []provider.Message
    SystemPrompt string
    Model        string  // "provider:model-id"
    SavedAt      time.Time
}
```

The persisted conversation state. `SystemPrompt` and `Model` are captured at run start so a resume can reconstruct the exact invocation.

### `TranscriptTruncatedLoader`

```go
type TranscriptTruncatedLoader interface {
    LoadTruncated(runID, stepID string, maxMessages int) (*StepTranscript, error)
}
```

Optional extension. Stores that implement it can return a truncated tail of a sealed transcript; combined with `WithTruncationOnCapReached()`, this preserves operability at the cost of potentially-incomplete history.

### `MetadataSetter`

```go
type MetadataSetter interface {
    SetMetadata(runID, stepID, systemPrompt, model string)
}
```

Optional extension implemented by `TranscriptStore` backends that can persist per-step invocation metadata (system prompt and model ID). When the `AgentRunner` starts, it calls `SetMetadata` on the store if implemented so the resume path can reconstruct the exact invocation without a separate metadata table. `InMemoryTranscriptStore` implements this interface.

### `InMemoryTranscriptStore`

```go
type InMemoryTranscriptStore struct { /* internal fields */ }

func NewInMemoryTranscriptStore() *InMemoryTranscriptStore
func NewInMemoryTranscriptStoreWithCaps(maxMessages int, maxBytes int64) *InMemoryTranscriptStore
func NewInMemoryTranscriptStoreWithOptions(opts ...InMemoryTranscriptStoreOption) *InMemoryTranscriptStore

func WithTranscriptCaps(maxMessages int, maxBytes int64) InMemoryTranscriptStoreOption

func (s *InMemoryTranscriptStore) Append(...) error
func (s *InMemoryTranscriptStore) Load(...) (*StepTranscript, error)
func (s *InMemoryTranscriptStore) LoadTruncated(...) (*StepTranscript, error)
func (s *InMemoryTranscriptStore) Delete(...) error
func (s *InMemoryTranscriptStore) SetMetadata(runID, stepID, systemPrompt, model string)
```

Default implementation. Caps default to 10000 messages, 50 MiB; override via the constructor or `WithMaxTranscriptMessages` / `WithMaxTranscriptBytes`. Implements both `TranscriptStore` and `TranscriptTruncatedLoader`.

- `NewInMemoryTranscriptStoreWithOptions(opts...)` - constructs with functional options. Preferred over `NewInMemoryTranscriptStoreWithCaps` for new code.
- `WithTranscriptCaps(maxMessages, maxBytes)` - sets entry/byte caps for the cap-based eviction policy. Pass to `NewInMemoryTranscriptStoreWithOptions`.

### `SharedMemory`

```go
type SharedMemory struct { /* internal fields */ }

func NewSharedMemory() *SharedMemory
func (sm *SharedMemory) Write(agent, key, value string)
func (sm *SharedMemory) Read(qualifiedKey string) (string, bool)
func (sm *SharedMemory) ListByAgent(agent string) map[string]string
func (sm *SharedMemory) Summary() string
func (sm *SharedMemory) Entries() map[string]string
func (sm *SharedMemory) LoadEntries(entries map[string]string)
```

Cross-step key-value store. Each entry is namespaced by writer (e.g., `"researcher.findings"`). The `shared_memory_read` and `shared_memory_write` tools auto-inject into agent tool chains when `WithSharedMemory` is set.

`Summary()` returns a human-readable list of all entries; agents can read it via the tool to see what's available.

`LoadEntries` is used by `ResumeFlow` to restore persisted shared memory entries after a process restart.

### `FactoryCache`

```go
type FactoryCache struct { /* internal fields */ }

func NewFactoryCache(inner func(sessionID string) *Orchestrator) *FactoryCache
func (c *FactoryCache) For(sessionID string) *Orchestrator
func (c *FactoryCache) Close(sessionID string) error
func (c *FactoryCache) CloseAll()
```

Session-scoped `*Orchestrator` cache with TTL-based eviction. Memoizes the orchestrator per `sessionID` so repeated calls for the same session reuse a single instance rather than allocating fresh goroutine trees (handle registry, TTL watchdogs) on every invocation.

- `NewFactoryCache(inner)` - wraps the supplied constructor. Panics if `inner == nil`.
- `For(sessionID)` - returns the cached orchestrator for `sessionID`, building it via `inner` on the first call. Returns `nil` if `inner` exceeds the construction timeout.
- `Close(sessionID)` - calls `Orchestrator.Close()` on the cached instance and removes it from the cache. Idempotent.
- `CloseAll()` - closes every cached instance; call on server shutdown.

## Hooks and policies

### `ProgressSink`

```go
type ProgressSink interface {
    OnEvent(ctx context.Context, event Event)
    OnOutput(ctx context.Context, output Output)
}
```

Receives execution events. `OnEvent` is fired for every lifecycle transition (workflow start/end, step start/end, agent turn, tool call, error, etc.); `OnOutput` is fired per streaming token when streaming is on.

Built-in implementations live under `github.com/zendev-sh/zenflow/sink`:

- `sink.NewStdoutSink()` / `sink.NewStdoutSinkTo(w)` - human-readable progress with glyphs and colors.
- `sink.NewJSONSink()` / `sink.NewJSONSinkTo(w)` - NDJSON, one event per line. The CLI's `--json` mode.
- `sink.Buffered(wrapped, window)` - debounces high-frequency output deltas into one event per window.

### `Event`

```go
type Event struct {
    Type        EventType
    Timestamp   time.Time
    RunID       string
    StepID      string
    AgentName   string
    Data        map[string]any
    Duration    time.Duration
    Tokens      *provider.Usage
    Error       error
    Message     string
    MessageKind string  // "notification" | "content"
    Subject     string  // optional short tag
    Context     string  // populated only on workflow_start when WithFlowContext was set
}
```

### `EventType`

The full event type enum (every constant):

```go
const (
    EventWorkflowStart           EventType = "workflow_start"
    EventWorkflowEnd             EventType = "workflow_end"
    EventStepStart               EventType = "step_start"
    EventStepEnd                 EventType = "step_end"
    EventStepSkipped             EventType = "step_skipped"
    EventAgentTurn               EventType = "agent_turn"
    EventToolCall                EventType = "tool_call"
    EventMessage                 EventType = "message"
    EventError                   EventType = "error"
    EventCoordinatorNarration    EventType = "coordinator_narration"
    EventCoordinatorMessage      EventType = "coordinator_message"
    EventCoordinatorSynthesis    EventType = "coordinator_synthesis"
    EventCoordinatorInboxMessage EventType = "coordinator_inbox_message"
    EventMessageSent             EventType = "message_sent"
    EventPlanReady               EventType = "plan_ready"
    EventAgentInboxDrain         EventType = "agent_inbox_drain"
    EventMessageDropped          EventType = "message_dropped"
    EventAgentIdle               EventType = "agent_idle"
    EventAgentWake               EventType = "agent_wake"
    EventMaxWakeCyclesWarning    EventType = "max_wake_cycles_warning"
    EventResumeStarted           EventType = "resume_started"
    EventResumeCompleted         EventType = "resume_completed"
    EventResumeFailed            EventType = "resume_failed"
    EventResumeQueued            EventType = "resume_queued"
    EventTranscriptSealed        EventType = "transcript_sealed"
)
```

`MessageKind` constants:

```go
const (
    MessageKindNotification = "notification"
    MessageKindContent      = "content"
)
```

### `Output`

```go
type Output struct {
    RunID     string
    StepID    string
    AgentID   string
    Delta     string
    Done      bool
    Reasoning bool  // true when delta is a reasoning/thinking token
}
```

Streaming output delta. Sinks coalesce these into displayable text.

### `PermissionHandler`, `PermissionRequest`

```go
type PermissionHandler interface {
    RequestPermission(ctx context.Context, req PermissionRequest) (bool, error)
}

type PermissionRequest struct {
    RunID    string
    StepID   string
    ToolName string
    ToolArgs json.RawMessage
}
```

Gates tool execution. Return `false` to deny (the [goai](https://goai.sh) loop surfaces the rejection); return an error to abort the entire step. CLI consumers typically prompt the user; library consumers can implement static policies (deny destructive tools) or audit-only logging (always permit).

### `ApprovalHandler`

```go
type ApprovalHandler interface {
    ApprovePlan(ctx context.Context, plan *Workflow) (bool, error)
}
```

Gates `RunGoal` plan execution. After the LLM produces a workflow plan, the handler is asked to approve it. Returning `false` aborts the run; returning an error wraps it as `fmt.Errorf("approval: %w", err)`.

### `Tracer`

```go
type Tracer interface {
    StartSpan(ctx context.Context, name string, attrs map[string]string) context.Context
    EndSpan(ctx context.Context, err error)
}
```

Tracing bridge. zenflow has no OTel dependency in core; the `zenflow/observability/otel` sub-module implements `Tracer` against the OTel SDK.

Span name conventions: `zenflow.flow`, `zenflow.goal`, `zenflow.agent`, `zenflow.step`, `zenflow.coordinator`, `zenflow.loop`, `zenflow.loop.iteration`, `zenflow.include`.

### `StepIsolation`

```go
type StepIsolation interface {
    Setup(ctx context.Context, runID, stepID string) (workDir string, err error)
    Cleanup(ctx context.Context, runID, stepID string) error
}
```

Per-step environment isolation. `Setup` is called before each step; the returned `workDir` is the step's working directory. `Cleanup` is deferred after the step completes.

`NopIsolation{}` is the no-op implementation; pass it via `WithIsolation(zenflow.NopIsolation{})` if you want explicit "no isolation" wiring. The orchestrator default is `nil`, which the executor treats as "skip setup/cleanup."

### `OutputTransformer`

```go
type OutputTransformer interface {
    TransformStepOutput(
        stepID string,
        content string,
        result map[string]any,
        targetModel string,
    ) (string, map[string]any)
}
```

Transforms a completed step's content and result before injection into a dependent step's prompt. Use this to implement smart truncation based on the target model's context window.

### `ModelResolver`

```go
type ModelResolver func(modelID string) (provider.LanguageModel, error)
```

Resolves a saved-transcript model identifier (as stored in `StepTranscript.Model`, e.g. `"openai:gpt-4o-mini"`) to a concrete `provider.LanguageModel`. Consulted by the resume path when a transcript's model differs from the executor's default.

Returning `(nil, nil)` is treated as "not resolvable" - the resume fails with `ErrModelResolverMissing`.

## Sink helpers

In `github.com/zendev-sh/zenflow/sink`:

### `StdoutSink`

```go
type StdoutSink struct { /* internal */ }
type StdoutSinkOption func(*StdoutSink)

func WithStdoutShowPlan(v bool) StdoutSinkOption  // enable DAG rendering on EventPlanReady
func WithStdoutVerbose(v bool) StdoutSinkOption   // enable agent text and reasoning content

func NewStdoutSink(opts ...StdoutSinkOption) *StdoutSink
func NewStdoutSinkTo(w io.Writer, opts ...StdoutSinkOption) *StdoutSink

// Deprecated: use WithStdoutShowPlan option in NewStdoutSink. Will be removed before v1.0.
func (s *StdoutSink) WithShowPlan(enabled bool) *StdoutSink
// Deprecated: use WithStdoutVerbose option in NewStdoutSink. Will be removed before v1.0.
func (s *StdoutSink) WithVerbose(enabled bool) *StdoutSink
```

Human-readable progress: triangle/check glyphs for step start/end, dashed lines for coordinator narration, colored status badges. Pass `WithStdoutShowPlan(true)` to render the DAG on `EventPlanReady`; pass `WithStdoutVerbose(true)` to include agent text and reasoning content.

`SetColorEnabled(v bool)` in the `sink` package overrides the auto-detected color setting. Intended for tests; production code should let the sink detect terminal capabilities automatically.

### `JSONSink`

```go
type JSONSink struct { /* internal */ }

func NewJSONSink() *JSONSink              // -> os.Stdout
func NewJSONSinkTo(w io.Writer) *JSONSink
func (s *JSONSink) Err() error            // first encode error
```

NDJSON output. One JSON object per line, additive contract (new events / fields don't break old parsers). Backs the CLI's `--json` mode.

### `BufferedSink`

```go
type BufferedSink struct { /* internal */ }

func Buffered(wrapped ProgressSink, window time.Duration) ProgressSink
func (b *BufferedSink) Close() error
```

Wraps a sink with a coalescing window: high-frequency `OnOutput` deltas within `window` are merged into one event. Lifecycle events flush immediately. Use this when wrapping a slow downstream sink (database writer, network logger) to avoid back-pressure on the agent loop.

`IsLifecycleEvent(e Event) bool` is a free helper that classifies an event as lifecycle (flush-immediate) or stream (coalescable).

## Sentinel errors

Surfaced through error returns from various methods; documented in detail in [Errors](./errors):

- `ErrOrchestratorClosed` - `RunAgent` / `RunAgentAsync` called on a closed orchestrator.
- `ErrAgentHandleTimeout`, `ErrAgentCancelled`, `ErrAgentPanicked` - async handle terminal states.
- `ErrNoTranscript`, `ErrTranscriptTooLarge` - transcript store errors.
- `ErrResumeShutdown`, `ErrModelResolverMissing`, `ErrModelResolverError`, `ErrMailboxFullOnResume` - resume path errors.
