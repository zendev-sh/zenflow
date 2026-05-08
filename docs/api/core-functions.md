---
title: Core Functions
description: 'The Go API surface is small: one constructor (zenflow.New), three "run something" methods on *Orchestrator, two lifecycle methods, and a couple of...'
---

# Core Functions

The Go API surface is small: one constructor (`zenflow.New`), three "run something" methods on `*Orchestrator`, two lifecycle methods, and a couple of helpers for loading workflows and building default coordinators.

This page documents each one in detail. For the option constructors that customise the orchestrator, see [Options](./options). For the value types (`Workflow`, `StepResult`, etc.), see [Types](./types).

## `zenflow.New`

```go
func New(opts ...Option) *Orchestrator
```

Constructs an `Orchestrator` and applies the supplied options.

**Defaults applied before options run:**

- `Storage` = `NewMemoryStorage()` (in-process map; lost on exit; supports `DeleteRun(ctx, runID)` for explicit eviction)
- `MaxConcurrency` = 5

Effective defaults at call time (resolved inside `RunFlow` / `RunAgent` if options didn't set them):

- `MaxTurns` = 50 (`RunAgent` per-call cap)
- `MaxDepth` = 3 (child agent nesting cap)

**Returns:** a non-nil `*Orchestrator`. There is no error path - validation happens at run time when required wiring is missing (e.g., no model).

**Example:**

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithTools(tools...),
    zenflow.WithProgress(sink),
    zenflow.WithMaxConcurrency(8),
)
defer orch.Close()
```

`orch.Close()` is mandatory for long-lived orchestrators - see the `Close` section below.

## `RunFlow`

```go
func (o *Orchestrator) RunFlow(
    ctx context.Context,
    wf *Workflow,
    opts ...RunFlowOption,
) (*WorkflowResult, error)
```

Executes a YAML-defined workflow as a DAG. Steps run in topological order, parallelism bounded by `WithMaxConcurrency`.

**Parameters:**

- `ctx` - cancellation context. Pending steps not yet dispatched surface as `StepCancelled`. In-flight steps surface as `StepFailed` with `context.Canceled` as the error.
- `wf` - parsed workflow. Either built programmatically or loaded via the `LoadWorkflow` helper documented below. Must be non-nil.
- `opts` - per-call options:
  - `WithFlowContext(ctx string)` - feeds use-case input to the coordinator (when `WithCoordinator` is configured) or blanket-prepends to every step's prompt (when no coordinator is installed).

**Returns:**

- `*WorkflowResult` - non-nil unless the call failed before any step ran. Carries `RunID`, `Status`, per-step `Steps` map, total `Duration`, accumulated `Tokens`, and an optional `Summary` (when a coordinator finalised with text).
- `error` - non-nil when the orchestrator is misconfigured (`wf == nil`, no `LLM provider`) or `ctx` was cancelled before any step started. Storage write failures are NOT propagated through this return value - they surface as `EventError` events and `slog.Warn` log entries. Subscribe to events to detect them.

**Behavior:**

1. Allocates a run ID (or reuses `WithRunID` value if set at construction).
2. Emits an `EventPlanReady` so sinks can render the DAG.
3. Builds an `Executor`, threads in all `With*` options, runs the DAG.
4. If `WithTracer` is set, wraps execution in a `zenflow.flow` span.

**Error conditions:**

- `wf == nil` → `zenflow.ErrWorkflowNil` (canonical text: `"zenflow: workflow must not be nil"`; match via `errors.Is(err, zenflow.ErrWorkflowNil)`)
- `o.model == nil` → `zenflow.ErrModelRequired` (canonical text: `"zenflow: LLM provider is required (use WithModel)"`; match via `errors.Is(err, zenflow.ErrModelRequired)`)
- `ctx.Err() != nil` after the call returns → result will reflect cancellation; the error is whatever the executor surfaced.

**Example:**

```go
wf, err := zenflow.LoadWorkflow("workflows/review.yaml")
if err != nil {
    return err
}

result, err := orch.RunFlow(ctx, wf,
    zenflow.WithFlowContext("PR #123: refactor message routing"),
)
if err != nil {
    return fmt.Errorf("run flow: %w", err)
}

for stepID, sr := range result.Steps {
    fmt.Printf("[%s] %s (%s)\n", sr.Status, stepID, sr.Duration)
}
fmt.Println("summary:", result.Summary)
```

## `RunGoal`

```go
func (o *Orchestrator) RunGoal(
    ctx context.Context,
    goal string,
    opts ...RunGoalOption,
) (*WorkflowResult, error)
```

Decomposes a free-form goal into a workflow via an LLM coordinator, optionally gates the plan through `WithApproval`, then executes the workflow as if it were passed to `RunFlow`.

**Parameters:**

- `ctx` - cancellation context.
- `goal` - free-form English description of what to accomplish. The coordinator turns it into a JSON workflow.
- `opts`:
  - `WithGoalContext(ctx string)` - additional framing appended to the decomposition prompt.

**Retry policy:** Up to 2 retries on JSON parse errors, 1 retry on schema validation errors. Budgets are global across the call: after 2 JSON retries, a subsequent validation error gets only the 1 validation retry. Total LLM calls bounded at `1 + 2 + 1 = 4`.

**Returns:**

- `*WorkflowResult` - same shape as `RunFlow`. Token counts include the decomposition LLM call.
- `error` - returned if every retry fails, the approval handler denies the plan, or any tool name in the generated workflow is not in the orchestrator's tool catalog.

**Error conditions:**

- `o.model == nil` → `zenflow.ErrModelRequired` (same sentinel as RunFlow/RunAgent)
- decomposition retries exhausted → returns the last `*JSONParseError` or `*CoordinatorValidationError`
- `WithApproval` returns `false` → `zenflow.ErrPlanDenied` (canonical text: `"zenflow: plan denied by approval handler"`; match via `errors.Is(err, zenflow.ErrPlanDenied)`)
- `WithApproval` returns an error → wrapped as `fmt.Errorf("approval: %w", err)`

**Example:**

```go
result, err := orch.RunGoal(ctx,
    "Review the open PR for race conditions and produce a summary",
    zenflow.WithGoalContext("Repo uses Mailbox+Wake messaging; check that contract carefully."),
)
```

## `RunAgent`

```go
func (o *Orchestrator) RunAgent(
    ctx context.Context,
    cfg AgentConfig,
) (*AgentResult, error)
```

Runs a single-agent conversation loop with optional child-agent spawning. Unlike `RunFlow`, there is no DAG - just one agent calling tools and possibly spawning sub-agents.

**Parameters:**

- `ctx` - cancellation context.
- `cfg` - per-call configuration. Required field is `Prompt`; other fields override the orchestrator-level defaults:
  - `Prompt` - the user message. Required.
  - `Model` - per-call override of `WithDefaultModel`.
  - `MaxTurns` - per-call override of `WithMaxTurns`. Falls back to orchestrator default, then to `defaultMaxTurns` (50).
  - `CallTools` - per-call override of `WithTools` when non-empty. Carries resolved `[]goai.Tool` values.
  - `ProgressSink` - per-call override of `WithProgress`.
  - `SubagentToolSet` - advisory label (e.g. "read-only") for the tool set; used by logging.
  - `Attachments` - multimodal `[]provider.Part` (text fragments, image refs, PDF byte payloads) appended to the initial user message; lets a single `RunAgent` call mix prose with images or documents.
  - `SessionID` - identifies the consumer session that owns this call (for `ListAgentHandles` partitioning in async mode).
  - `Temperature`, `TopP`, `ResultSchema` - LLM sampling and structured-output knobs.

**Returns:**

- `*AgentResult` - `Content` (final assistant text), `Result` (structured output if `ResultSchema` was set), `Tokens` (parent + children aggregated), `Turns`, `Status` (`completed` or `truncated`), `Duration`.
- `error` - non-nil on failure. Note: panics in the LLM goroutine are recovered and surfaced via `error` in the synchronous path; the async path delivers them as `AgentResult.Error` wrapping `ErrAgentPanicked`.

**Error conditions:**

- `o.IsClosed()` (orchestrator was Close'd) → returns `ErrOrchestratorClosed`. Construct a fresh orchestrator.
- `o.model == nil` → `zenflow.ErrModelRequired` (canonical text: `"zenflow: LLM provider is required (use WithModel)"`; match via `errors.Is(err, zenflow.ErrModelRequired)`)

**Behavior:**

1. Allocates per-call `MessageRouter` and in-memory `MailboxStore` so child spawns can inter-communicate.
2. Spawns a primary agent with the resolved tools, model, and progress sink.
3. Waits for the agent and any child agents to complete.
4. Aggregates token usage across the parent and all children.

**Example:**

```go
result, err := orch.RunAgent(ctx, zenflow.AgentConfig{
    Prompt:    "Summarize the changes in CHANGES.md",
    Model:     "bedrock:anthropic.claude-sonnet-4-6",
    MaxTurns:  20,
    SessionID: "user-42",
})
if err != nil {
    return err
}
fmt.Println(result.Content)
fmt.Printf("tokens: %d in / %d out\n", result.Tokens.InputTokens, result.Tokens.OutputTokens)
```

## `ResumeFlow`

```go
func (o *Orchestrator) ResumeFlow(
    ctx context.Context,
    runID string,
    wf *Workflow,
) (*WorkflowResult, error)
```

Resumes a previously-started workflow from its checkpoint. Steps with `StepCompleted` status are loaded from storage and not re-executed; failed, cancelled, and skipped steps run again.

**Requires:** a non-default `Storage` backend (the in-memory default loses state when the orchestrator exits). For cross-process resume, use a `FileStorage` or other persistent implementation.

::: warning Concurrent ResumeFlow on the same `runID` is unsafe
zenflow does not guard against two simultaneous `ResumeFlow` calls for the same run - each constructs its own `Executor` against the shared `Storage` and the two will race on `SaveStepResult` / `SaveRun`. With `MemoryStorage` the latest write wins under the storage mutex; with `FileStorage` each write is atomic via rename so the file never half-writes, but the visible state may flip between the two parallel runs. Embedders that may resume from multiple processes must serialise externally (file lock, queue, advisory lock).
:::

**Parameters:**

- `ctx` - cancellation context.
- `runID` - the run identifier returned from the original `RunFlow` (`WorkflowResult.RunID`).
- `wf` - the workflow definition. Must match the structure used in the original run; only step results are loaded from storage, the workflow shape itself comes from the YAML you pass here.

**Returns:** Same shape as `RunFlow`.

**Error conditions:**

- `wf == nil` → `zenflow.ErrWorkflowNil` (canonical text: `"zenflow: workflow must not be nil"`; match via `errors.Is(err, zenflow.ErrWorkflowNil)`)
- `o.storage == nil` → `zenflow.ErrStorageRequired` (canonical text: `"zenflow: storage is required for resume (use WithStorage)"`; match via `errors.Is(err, zenflow.ErrStorageRequired)`)
- run not found → `fmt.Errorf("resume: %w", err)` from the storage layer

**Example:**

```go
fileStore := zenflow.NewFileStorage("/var/lib/zenflow")
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithStorage(fileStore),
)

// First run - persists step results.
first, err := orch.RunFlow(ctx, wf)
if err != nil {
    log.Println("first run failed:", err)
}

// Later (possibly different process) - resume.
second, err := orch.ResumeFlow(ctx, first.RunID, wf)
```

The resume mechanism is documented end-to-end in [Concepts → Resume](../concepts/resume).

## `Coordinator`, `HasLLM`, `DefaultModel`

Three accessor methods used by integrators that need to introspect the orchestrator they're handed:

```go
func (o *Orchestrator) Coordinator() *AgentRunner
func (o *Orchestrator) HasLLM() bool
func (o *Orchestrator) DefaultModel() string
```

- `Coordinator()` returns the `AgentRunner` installed via `WithCoordinator`, or nil. Useful for callers that own the coord lifecycle (per `WithCoordinator`'s contract: orchestrator never starts or stops the coord runner).
- `HasLLM()` reports whether `WithModel` was applied. Diagnostic; the run methods themselves error explicitly when no model is set.
- `DefaultModel()` returns the value passed to `WithDefaultModel`, or `""` if never set. Used by orchestrator-cache callers to detect when a cached orchestrator's default model has gone stale relative to the current session selection.

All three are nil-receiver-safe (return zero values).

## `Close`

```go
func (o *Orchestrator) Close() error
```

Tears down the orchestrator: cancels in-flight async agent handles, waits for them to drain (up to 5 seconds), and marks the orchestrator as closed so subsequent `RunAgent` / `RunAgentAsync` calls return `ErrOrchestratorClosed`.

**Idempotent** - safe to call multiple times; only the first call performs cleanup.

**Required for long-lived orchestrators.** Without `Close()`, async handles can leak goroutines past the parent's lifetime. The right pattern:

```go
orch := zenflow.New(...)
defer orch.Close()
```

`RunFlow` and `RunAgent` (synchronous) do not strictly require `Close` if the program exits immediately afterward (process termination cleans up everything), but the call is cheap and sets the invariant that orchestrator lifecycle is explicit.

**Returns:** always `nil` in the current implementation. The signature includes an `error` for forward compatibility; future versions may surface drain-deadline failures.

## `LoadWorkflow`

```go
func LoadWorkflow(path string) (*Workflow, error)
```

Reads a YAML or JSON workflow file from `path`, validates it against the schema, and returns the parsed `*Workflow`.

**Behavior:**

1. Reads the file from disk.
2. Detects format from extension (`.yaml`, `.yml`, `.json`).
3. Parses to `*Workflow`.
4. Sets `wf.BaseDir` to `filepath.Dir(absPath)` so relative `contextFiles` resolve from the workflow's directory.
5. Validates step IDs (regex `^[a-zA-Z][a-zA-Z0-9_-]*$`), agent references, dependency cycles, etc.
6. Runs schema validation against the embedded JSON Schema.

**Returns:**

- `*Workflow` - parsed and validated.
- `error` - file read errors, parse errors, validation errors. Validation errors are typed as `*CoordinatorValidationError` carrying the failed assertions list.

**Example:**

```go
wf, err := zenflow.LoadWorkflow("workflows/review.yaml")
if err != nil {
    return fmt.Errorf("load: %w", err)
}
fmt.Println("loaded:", wf.Name, "with", len(wf.Steps), "steps")
```

For programmatic construction (no file), build the `Workflow` struct directly and skip `LoadWorkflow`. Validation is then your responsibility, though the executor still rejects obviously-malformed inputs at run time.

## `NewDefaultCoordRunner`

```go
func NewDefaultCoordRunner(
    llm provider.LanguageModel,
    opts ...CoordOption,
) *AgentRunner
```

Constructs a pre-configured `*AgentRunner` for use as a workflow coordinator. The returned runner has:

- `StepID = "coordinator"` (matches the executor's reverse-reply inbox key)
- `Mailbox` = fresh `InMemoryMailboxStore`
- `Wake` = cap-1 buffered channel (required for mailbox-mode delivery)
- `Tools` = the three default coord tools (`forward_to_agent`, `narrate`, `finalize`), plus any from `WithCoordTools(...)`, minus `narrate` if `SynthesizeOnly()` is set
- `SystemPrompt` = `DefaultCoordSystemPrompt` (overridable via `WithCoordSystemPrompt` / `WithCoordSystemPromptSuffix`)
- `MaxWakeCycles` = 100 (overridable via `WithCoordMaxWakeCycles`)

**The factory does NOT start `runner.Run`.** Caller owns the lifecycle - typical usage starts the runner in a goroutine before calling `RunFlow` and waits for it to exit afterward.

**Available `CoordOption`s:**

- `SynthesizeOnly()` - drop `narrate`; coord only emits a final summary via `finalize`.
- `WithCoordTools(tools ...goai.Tool)` - append extra tools to the default set (additive, not replacing).
- `WithCoordSystemPrompt(prompt string)` - replace the default system prompt entirely.
- `WithCoordSystemPromptSuffix(extra string)` - append guidance to the default prompt.
- `WithCoordMaxWakeCycles(n int)` - raise / lower the wake-cycle cap.

**Example:**

```go
coord := zenflow.NewDefaultCoordRunner(model,
    zenflow.WithCoordSystemPromptSuffix("\nKeep narration under 80 chars per line."),
)

// Start the coord in the background.
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
go func() {
    coord.Run(ctx, zenflow.AgentConfig{}, "", "", coord.Tools)
}()

orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithCoordinator(coord),
)
defer orch.Close()

result, err := orch.RunFlow(ctx, wf)
_ = result
_ = err
cancel()
```

The lifecycle convention is documented under [Concepts → Coordinator](../concepts/coordinator).

## `RunAgentAsync` and `AgentHandle`

For background work where the caller wants a handle instead of blocking on completion:

```go
func (o *Orchestrator) RunAgentAsync(
    ctx context.Context,
    cfg AgentConfig,
) (*AgentHandle, error)

func (o *Orchestrator) ListAgentHandles(sessionID string) []*AgentHandle
```

`*AgentHandle` exposes:

- `ID` - stable identifier, format `agent-<UUID v4>`. Flows through every `ProgressSink` event the agent emits.
- `Done() <-chan AgentResult` - delivers exactly one result, then closes. Multiple reads after close yield the zero value.
- `Cancel() error` - force-terminates the agent. Subsequent `Done()` reads see `AgentResult{Error: AgentError{Sentinel: ErrAgentCancelled}}`.

**TTL:** Each handle has a wall-clock cap (default 30 minutes; `DefaultAgentHandleTTL`). SDK consumers: call `zenflow.WithAgentHandleTTL(d)`. CLI users may set the `ZENFLOW_AGENT_HANDLE_TTL` env var; the CLI maps it to the option, but the library never reads env vars directly. When exceeded, the handle is force-completed with `AgentError{Sentinel: ErrAgentHandleTimeout}`.

**ListAgentHandles** returns all live async handles tagged with the supplied `SessionID` (from `AgentConfig.SessionID`). Used by hosting consumers to surface "this session has N background tasks running" without polling.

`RunAgentAsync` returns `ErrOrchestratorClosed` if `Close` has been called.

## Workflow loading helpers

`LoadWorkflow` is a one-shot file → validated `*Workflow` helper. The four functions in this section are the lower-level building blocks it composes - useful when the workflow source is not a path on disk (e.g., bytes from an HTTP body, a JSON blob from a goal-decomposition LLM, or a programmatically-built `*Workflow` that still needs defaults applied and validation run).

### `ParseWorkflow`

```go
func ParseWorkflow(data []byte) (*Workflow, error)
```

Parses a YAML or JSON byte slice into `*Workflow`. Detects the format from the leading bytes; no file I/O. Used internally by `LoadWorkflow` after the file read, and by tests / fixtures that ship workflow bytes inline.

**When to use:** loading a workflow from any non-file source (network response, embedded fixture, multipart upload). Pair with `ApplyDefaults` + `ValidateWorkflow` if you also want the rest of the loader pipeline.

### `ParseWorkflowJSON`

```go
func ParseWorkflowJSON(data []byte) (*Workflow, error)
```

Strictly-JSON variant of `ParseWorkflow`. Skips format detection - useful when the source contract guarantees JSON (e.g., the coordinator's plan output during `RunGoal`) so a YAML-shaped payload fails fast rather than silently parsing as YAML.

### `SanitizeWorkflowUnicode`

```go
func SanitizeWorkflowUnicode(wf *Workflow) error
```

Strips invisible / bidi control codepoints from every string field on the workflow (step IDs, agent prompts, instructions, etc.). Returns `*UnicodeUnsafeError` listing the field paths that contained unsafe runes, with the workflow already sanitized in place. CLI consumers run this after parse to defend against prompt-injection via zero-width characters.

### `ApplyDefaults`

```go
func ApplyDefaults(wf *Workflow)
```

Populates default values on a parsed workflow: workflow-wide `MaxConcurrency`, `OnStepFailure`, `Scheduler`, etc., plus per-step defaults inherited from `Workflow.Options`. Idempotent. `LoadWorkflow` calls it; programmatic callers building `*Workflow` literals should call it before `ValidateWorkflow` so validation sees the post-default shape.

### `ValidateWorkflow`

```go
func ValidateWorkflow(wf *Workflow) ([]string, error)
```

Runs the full validator suite: schema, ID regex, agent reference resolution, dependency cycle detection, include conflict checks, loop validation, and so on. Returns the list of `[]string` portability warnings (advisory; non-fatal) and a typed `error` (e.g., `*CycleError`, `*MissingAgentError`, `*ValidationError`) on the first hard failure. Inspect the returned error with `errors.As` to surface a structured diagnostic.

## Coordinator loop helpers

These helpers expose the coord re-spawn loop that the `zenflow` CLI binary uses internally. Embedders that want a CLI-style "respawn coord on every wake" lifecycle can drop them in instead of writing the for-loop themselves.

### `RunCoordinatorLoop`

```go
func RunCoordinatorLoop(
    ctx context.Context,
    runner *AgentRunner,
    modelID string,
    opts ...CoordLoopOption,
) func()
```

Starts a goroutine that runs `runner.Run` until the runner returns, then re-spawns it on every `WaitForCoordWake` signal. Exits cleanly on `ctx` cancellation. Returns a cleanup func that the caller MUST call (typically via `defer`) to drain the loop goroutine before exiting.

The cleanup func cancels the inner context, waits up to `WithCleanupTimeout` (default `DefaultCoordCleanupTimeout` = 2s) for the loop to exit, then returns. This bounds shutdown latency: a stuck coord LLM call cannot block the caller forever.

**When to use:** embedders that want a CLI-style coord lifecycle (the `cmd/zenflow` binary uses this verbatim). Custom UX (e.g., chat consoles that want one Run per user turn instead of one Run per wake) should hand-roll the loop using `WaitForCoordWake` directly.

### `DefaultStorageDir`

```go
func DefaultStorageDir() string
```

Returns the canonical zenflow storage directory: `$HOME/.zenflow/runs` (or `<os.TempDir>/zenflow/runs` when `os.UserHomeDir` fails). Pass to `NewFileStorage` for cross-process resume on the standard path:

```go
storage := zenflow.NewFileStorage(zenflow.DefaultStorageDir())
orch := zenflow.New(zenflow.WithStorage(storage))
```

CLI consumers and embedders that want the default zenflow storage location should call this rather than hard-coding `~/.zenflow/runs`.

### `TopoSort`

```go
func TopoSort(steps []Step) ([]string, error)
```

Returns the steps' IDs in topological dependency order. Returns `*CycleError` (carrying the cycle's step IDs) if `DependsOn` forms a cycle. The orchestrator runs `TopoSort` internally before dispatching the DAG; embedders rarely need to call it, but it is exposed for tools that render or analyze the workflow shape (linters, plan visualizers, schedulers that pre-compute fan-out).

## Duration helpers

The `Duration` type carries its own YAML/JSON parsing (see [Types](./types)). When you need to format or parse `time.Duration` values directly - e.g., when rendering a custom progress sink or accepting a duration on a CLI flag - use the two helpers below. Both share the same surface form as the YAML schema: optional leading `-`, plus at least one whole `h`/`m`/`s` component. Sub-second precision (`ms`, `us`, `ns`) is rejected so library output stays round-trippable.

### `FormatDuration`

```go
func FormatDuration(d time.Duration) string
```

Emits a `time.Duration` using only whole `h`/`m`/`s` components, conforming to the schema pattern `^-?(\d+h)?(\d+m)?(\d+s)?$`. Sub-second precision is truncated; a value like `750ms` formats as `0s`, and `90s` formats as `1m30s`. Negative durations get a leading `-` prefix with absolute-value components. The zero value formats as `"0s"`.

**When to use:** rendering durations to text in a way that matches what the CLI and `sink.JSONSink` emit (e.g., custom progress sinks, log lines, CLI tools built on top of zenflow). Pairs with `ParseDurationStrict` for symmetric round-trip.

### `ParseDurationStrict`

```go
func ParseDurationStrict(s string) (time.Duration, error)
```

Strict parser that combines `time.ParseDuration` with the schema pattern check. Rejects sub-second and mixed-precision Go forms (`100ms`, `1h30.5m`) at the parser boundary so callers cannot smuggle in values the YAML loader would refuse. Empty string is rejected (catches the YAML-null vs JSON-null asymmetry uniformly); the trivial form `"0s"` still parses to zero. Negative durations are rejected outright - unlike `time.ParseDuration`, which silently accepts `-30s`.

**When to use:** parsing duration strings from CLI flags, environment variables, or external config when you want exactly the same validation as the YAML loader. Returns a wrapped `error` with the offending input quoted on failure.

## `MessageIDs`

```go
func MessageIDs(msgs []RouterMessage) []string
```

Returns the `MessageID` of every message in `msgs`. Used to translate the `[]RouterMessage` shape returned by `MailboxStore.Unread` into the `[]string` ids accepted by `MailboxStore.MarkRead` (the F4 CAS dedup contract).

**When to use:** custom drainers that pull from a `MailboxStore` directly. The default `AgentRunner` drain path calls this internally; embedders only need it when wiring a bespoke runtime (e.g., a multi-process router that owns its own poll loop).
