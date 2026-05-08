---
title: Options
description: zenflow's Orchestrator is configured through functional options - one constructor (zenflow.New) plus a long list of With* helpers. This page...
---

# Options

zenflow's `Orchestrator` is configured through functional options - one constructor (`zenflow.New`) plus a long list of `With*` helpers. This page groups them by concern, lists the signature, default behavior, and a short example for each.

There are two flavors of option:

- **`Option`** - applied at construction time; long-lived (`zenflow.New(opts...)`).
- **`RunFlowOption`** / **`RunGoalOption`** - applied per call (`orch.RunFlow(ctx, wf, opts...)`).

## LLM and tools

### `WithModel`

```go
func WithModel(m provider.LanguageModel) Option
```

Sets the language model used by the orchestrator. Required for `RunFlow`, `RunGoal`, and `RunAgent` - the run methods error explicitly when no model is set.

**Default:** none. Without `WithModel`, every run method returns `zenflow.ErrModelRequired` (canonical text: `"zenflow: LLM provider is required (use WithModel)"`; match via `errors.Is(err, zenflow.ErrModelRequired)`).

```go
import "github.com/zendev-sh/goai/provider/anthropic"
model := anthropic.Chat("claude-sonnet-4-6")
orch := zenflow.New(zenflow.WithModel(model))
```

### `WithDefaultModel`

```go
func WithDefaultModel(model string) Option
```

Sets the fallback model identifier (string form, e.g., `"bedrock:anthropic.claude-sonnet-4-6"`) used when a workflow step or `AgentConfig` does not specify one explicitly.

**Default:** `""` (no fallback - per-step / per-call must specify).

**When to use:** workflows where most steps share one model and only a few override (e.g., a "fast" reasoner step). The orchestrator passes the resolved string through to [goai](https://goai.sh) so the right provider is selected.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithDefaultModel("gemini:gemini-2.5-flash"),
)
```

`Orchestrator.DefaultModel()` returns the configured value.

### `WithForceModel`

```go
func WithForceModel(modelID string) Option
```

Overrides every per-agent and per-step `Model` identifier with the given model name during execution. Empty string disables the override (equivalent to leaving the option off). Precedence (high → low) for effective model resolution is: `WithForceModel` > `Step.Model` > `AgentConfig.Model` > `WithDefaultModel`.

**Default:** `""` (no override; per-step / per-agent / default-model resolution applies normally).

**When to use:** cross-provider CLI overrides (e.g., running every step of a workflow through one test provider regardless of what the YAML specifies). For ordinary defaults, prefer `WithDefaultModel` - it lets per-agent and per-step `Model` declarations win.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithForceModel("bedrock:anthropic.claude-sonnet-4-6"),
)
```

### `WithGoAIOptions`

```go
func WithGoAIOptions(opts ...goai.Option) Option
```

Forwards extra `goai.Option` values into every `GenerateText` / `StreamText` call zenflow makes. Use this for tracing, custom retry policies, or any goai-level knob zenflow does not expose directly.

**Default:** no extra options.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithGoAIOptions(
        goai.WithTemperature(0.5),
        goai.WithMaxRetries(3),
    ),
)
```

### `WithTools`

```go
func WithTools(tools ...goai.Tool) Option
```

Sets the tool catalog available to agents. Each tool's `Execute` closure is invoked when the LLM calls the tool by name; zenflow handles the [goai](https://goai.sh) loop, permission checks, and progress events.

**Default:** empty slice (agents can only produce text, no tool calls succeed).

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithTools(
        readFileTool,
        writeFileTool,
        bashTool,
    ),
)
```

For per-call tool restriction (e.g., a sub-agent gets only read-only tools), set `AgentConfig.CallTools` instead of using a separate orchestrator.

### `WithMaxTurns`

```go
func WithMaxTurns(n int) Option
```

Caps the number of turns (LLM call + tool round trips) per `RunAgent` invocation. Applies to `RunAgent` only - `RunFlow` uses per-agent `MaxTurns` from workflow YAML.

**Default:** `defaultMaxTurns` = 50.

**When to hit it:** agents stuck in a tool-call loop. The runner returns `Status: AgentStatusTruncated` rather than erroring.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithMaxTurns(20), // tighter cap for per-call agents
)
```

### `WithMaxDepth`

```go
func WithMaxDepth(n int) Option
```

Caps agent nesting depth - how many levels of child agents can be spawned via the `agent` tool. Applies to `RunAgent` only.

**Default:** 3.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithMaxDepth(5),
)
```

`Orchestrator.MaxDepth()` returns the configured value (without the runtime fallback).

## Coordinator

### `WithCoordinator`

```go
func WithCoordinator(runner *AgentRunner) Option
```

Installs a caller-provided `AgentRunner` as the workflow coordinator. The executor pushes lifecycle events (step start/end, errors, etc.) into the runner's mailbox so a coord LLM can narrate progress, route messages between agents, and produce a final summary.

**Default:** nil (no coordinator; workflow runs without LLM-driven monitoring).

**Lifecycle is the caller's responsibility.** The orchestrator never calls `runner.Run`, never blocks on it, and never checks whether anyone drains the mailbox. Pass `nil` to disable.

The simplest path is `NewDefaultCoordRunner` plus a goroutine to host its `Run` loop. See [Core Functions → NewDefaultCoordRunner](./core-functions#newdefaultcoordrunner) for the canonical pattern.

```go
coord := zenflow.NewDefaultCoordRunner(model)
go coord.Run(ctx, zenflow.AgentConfig{}, "", "", coord.Tools)

orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithCoordinator(coord),
)
```

### `WithMaxWakeCycles`

```go
func WithMaxWakeCycles(n int) Option
```

Caps wake-driven re-entries into [goai](https://goai.sh) per `AgentRunner.Run`. Each wake cycle = one round trip of `mailbox drained → goai called → return to wait`.

**Default:** `defaultMaxWakeCycles` = 10.

**When to raise:** workflows where many messages stream into one agent during its run (e.g., a long-running reviewer that receives updates from parallel research steps). The default is conservative; production aggregator workflows often run at 50-250.

When the cap is reached with messages still pending, the runner emits one `EventMessageDropped{reason: max-wake-cycles}` per remaining message rather than dropping silently.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithMaxWakeCycles(50),
)
```

For coord runners specifically, use `zenflow.WithCoordMaxWakeCycles(n)` when constructing via `NewDefaultCoordRunner` - that one defaults to 100.

### `WithCoordContextProvider`

```go
func WithCoordContextProvider(fn func() string) CoordOption
```

`CoordOption` (passed to `NewDefaultCoordRunner`, not to `New()`). Installs a callback the coord runner invokes once before the first `goai.GenerateText` call AND once on every wake-driven re-entry after the mailbox drain. The returned string is appended as a fresh user-role message wrapped in `<dynamic-context>...</dynamic-context>` so the LLM can distinguish ambient state from in-band conversation. Empty / whitespace-only returns are skipped; a nil callback is a no-op.

**When to use:** chat-driven UX consumers (e.g. an editor integration) that need ambient context refreshed every wake without re-engineering the system prompt - currently-open files, repo metadata, session topic, recent user actions. Keep the callback cheap (microseconds) and goroutine-safe; it runs synchronously on the runner goroutine.

```go
coord := zenflow.NewDefaultCoordRunner(
    llm,
    zenflow.WithCoordContextProvider(func() string {
        return ambientSnapshotForCoord()
    }),
)
```

For symmetry, the runner-level option `zenflow.WithRunnerWakeContextProvider(fn)` exposes the same hook on any `AgentRunner` constructed via `zenflow.NewAgentRunner(...)`. Coord callers should normally prefer `WithCoordContextProvider`; the runner-level option is for bespoke runner construction outside the coord factory.

## Concurrency and lifecycle

### `WithMaxConcurrency`

```go
func WithMaxConcurrency(n int) Option
```

Sets the maximum number of workflow steps that can run in parallel.

**Default:** 5.

**Precedence:** workflow YAML `options.maxConcurrency` > `WithMaxConcurrency(n)` orchestrator option > library default `5`. Setting YAML `maxConcurrency: 0` (or omitting the field) is treated as "unset" and falls through to the next level, so `WithMaxConcurrency(n)` is honored when the workflow does not pin the value. The default is applied at execution time, not by the parser.

**When to tune:** raise on machines with many cores or workflows where most steps are LLM-bound (waiting on the network, not CPU). Drop to 1-2 on small CI runners or workflows where steps share rate-limited APIs.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithMaxConcurrency(10),
)
```

### `WithIsolation`

```go
func WithIsolation(iso StepIsolation) Option
```

Installs a `StepIsolation` strategy - `Setup` is called before each step, `Cleanup` is deferred after. Use for per-step working directories, container sandboxes, or any other resource scope tied to step lifetime.

**Default:** nil (no setup/cleanup).

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithIsolation(myWorkdirIsolation),
)
```

The interface is documented in [Types](./types).

### `WithMaxMailboxSize`

```go
func WithMaxMailboxSize(n int) Option
```

Bounds the per-step in-memory mailbox queue. When the cap is exceeded, Append rejects the newest message and the router emits `EventMessageDropped{reason: mailbox-full}` via OnDrop.

**Default:** 0 (unbounded).

**When to set:** workflows where a producer can flood a slow consumer. Without a cap, runaway producers can OOM. The default is unbounded for backward compatibility; setting any positive value is a good practice.

Only takes effect with the default `InMemoryMailboxStore` - custom stores enforce their own caps.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithMaxMailboxSize(1000),
)
```

### `WithHoldTimeout`

```go
func WithHoldTimeout(d time.Duration) Option
```

Bounds how long the executor retains a step in `StepIdle` while messages keep arriving. After the timeout, the step force-terminates and one `EventMessageDropped{reason: hold-timeout}` is emitted per remaining mailbox message.

**Default:** `defaultHoldTimeout` = 30 seconds.

**When to tune:** raise for chat-style workflows where idle gaps between user messages are normal; lower for automated pipelines where any idle gap signals a stuck step.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithHoldTimeout(2 * time.Minute),
)
```

### `WithAgentHandleTTL`

```go
func WithAgentHandleTTL(d time.Duration) Option
```

Bounds the start-to-finish wall-clock cap on a `RunAgentAsync` handle. When the TTL is exceeded the handle is force-completed with `AgentError{Sentinel: ErrAgentHandleTimeout}` and the inner context is cancelled.

**Default:** `DefaultAgentHandleTTL` = 30 minutes. Zero or negative values fall back to the default.

**When to tune:** raise for long-running async agents (multi-hour research / batch jobs) where 30 minutes is too tight; lower for interactive UIs that should reclaim a stuck handle quickly. The library does not consult any environment variables; CLI consumers wiring `ZENFLOW_AGENT_HANDLE_TTL` (or any other source) pass the parsed `time.Duration` to this option themselves.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithAgentHandleTTL(2 * time.Hour),
)
```

## AgentRunner-specific options

These options configure a hand-rolled `AgentRunner` (`zenflow.NewAgentRunner`). Most consumers use `RunFlow` / `RunGoal` / `RunAgent` and never need them. Use these only when building a custom runner outside the orchestrator (e.g., a bespoke coordinator, an embedding harness, or a test fixture). Each option returns a `RunnerOption` and is applied by `NewAgentRunner(opts...)`.

### Inputs

#### `WithRunnerModel`

```go
func WithRunnerModel(m provider.LanguageModel) RunnerOption
```

Sets the language model the runner calls via `goai.GenerateText` / `goai.StreamText`.

**Default:** none. Required - `Run` errors without a model.

#### `WithRunnerModelID`

```go
func WithRunnerModelID(id string) RunnerOption
```

Sets the model identifier string recorded in transcript metadata (e.g., `"bedrock:anthropic.claude-sonnet-4-6"`). Distinct from `WithRunnerModel`, which sets the live provider handle.

**Default:** `""`.

**When to use:** resume / replay paths where the saved transcript must record exactly which model produced each step.

#### `WithRunnerSystemPrompt`

```go
func WithRunnerSystemPrompt(prompt string) RunnerOption
```

Sets the system prompt forwarded to goai as `WithSystem(prompt)`.

**Default:** `""` (no system prompt).

#### `WithRunnerTools`

```go
func WithRunnerTools(tools ...goai.Tool) RunnerOption
```

Sets the tool catalog the runner exposes to goai. Each tool's `Execute` closure is invoked when the LLM calls the tool by name.

**Default:** empty slice.

#### `WithRunnerInitialMessages`

```go
func WithRunnerInitialMessages(msgs []provider.Message) RunnerOption
```

Pre-loads conversation history that the runner prepends to the fresh user turn passed to `Run`. Used by `Executor.runResume` to replay a saved transcript through the standard `AgentRunner.Run` machinery.

**Default:** empty (normal, non-resume runs).

#### `WithRunnerGoAIOptions`

```go
func WithRunnerGoAIOptions(opts ...goai.Option) RunnerOption
```

Forwards extra `goai.Option` values into every `GenerateText` / `StreamText` call the runner makes (tracing, retry policy, temperature, etc.).

**Default:** none.

### Wiring

#### `WithRunnerRunID`

```go
func WithRunnerRunID(id string) RunnerOption
```

Sets the workflow run ID stamped on every event the runner emits. Pair with `WithRunnerStepID` so consumers can correlate runner output with the parent workflow.

**Default:** `""`.

#### `WithRunnerStepID`

```go
func WithRunnerStepID(id string) RunnerOption
```

Sets the step ID stamped on every event the runner emits.

**Default:** `""`.

#### `WithRunnerStateRef`

```go
func WithRunnerStateRef(s *goai.AgentState) RunnerOption
```

Wires a `goai.AgentState` into the runner so an external poller can observe the tool-loop lifecycle without holding a lock. See `AgentState` for the lifecycle contract.

**Default:** nil.

**When to use:** UIs that need lock-free state polling (e.g., a TUI status bar refreshing at 60fps).

#### `WithRunnerMailbox`

```go
func WithRunnerMailbox(m MailboxStore) RunnerOption
```

Sets the `MailboxStore` the runner reads inter-agent messages from. Pair with `WithRunnerWake` to enable mailbox-mode delivery.

**Default:** nil (legacy single-call mode, no inter-agent messaging).

#### `WithRunnerWake`

```go
func WithRunnerWake(ch chan struct{}) RunnerOption
```

Sets the wake-signal channel the `DeliveryEngine` pings when the runner's mailbox has unread messages.

**Default:** nil. Pair with `WithRunnerMailbox`.

#### `WithRunnerRouter`

```go
func WithRunnerRouter(rt *MessageRouter) RunnerOption
```

Wires a shared `MessageRouter` into the runner so child spawns inherit a live router for inter-agent messaging.

**Default:** nil (legacy single-call path with no messaging).

#### `WithRunnerTranscript`

```go
func WithRunnerTranscript(ts resume.TranscriptStore) RunnerOption
```

Wires a `TranscriptStore` so the runner persists the step's conversation on every goai step-finish hook AND on `Run` exit.

**Default:** nil. Required for the Resume Mechanism.

#### `WithRunnerWakeContextProvider`

```go
func WithRunnerWakeContextProvider(fn func() string) RunnerOption
```

Installs a callback the runner invokes once before the first LLM call AND once after every wake-driven mailbox drain. The returned string is appended as a fresh user-role message wrapped in `<dynamic-context>...</dynamic-context>`. Empty / whitespace returns are skipped; nil callback is a no-op.

**Default:** nil.

**When to use:** bespoke runners that need per-wake ambient context refresh (open files, repo metadata, session topic). Coord callers should normally use `WithCoordContextProvider` - that option threads into this hook.

### Behavior

#### `WithRunnerStreaming`

```go
func WithRunnerStreaming() RunnerOption
```

Enables streaming mode - tokens are surfaced via `ProgressSink.OnOutput` as they arrive instead of as a single block after the LLM call returns.

**Default:** off.

#### `WithRunnerVerbose`

```go
func WithRunnerVerbose() RunnerOption
```

Enables verbose output - the runner's LLM responses are surfaced via `ProgressSink.OnOutput` in addition to lifecycle events.

**Default:** off.

#### `WithRunnerMaxWakeCycles`

```go
func WithRunnerMaxWakeCycles(n int) RunnerOption
```

Caps wake-driven re-entries into `goai.GenerateText` per `Run`. Zero or negative falls back to the package default.

**Default:** `defaultMaxWakeCycles` = 10. (Coord runners constructed via `NewDefaultCoordRunner` default to 100.)

#### `WithRunnerSpawnDepth`

```go
func WithRunnerSpawnDepth(depth int) RunnerOption
```

Records the recursion depth of this runner relative to the top-level `RunAgent` invocation. Stamped onto `EventToolCall` payloads so TUI consumers can collapse nested-spawn cards under the parent.

**Default:** 0 (top-level).

#### `WithRunnerSpawnParentCallID`

```go
func WithRunnerSpawnParentCallID(id string) RunnerOption
```

Records the `agent`-tool `ToolCallID` that produced this runner via `SpawnChild`. Emitted on every `EventToolCall` in `Data["parentCallID"]` so consumers can route nested events into the parent's children list.

**Default:** `""`.

#### `WithRunnerPermissions`

```go
func WithRunnerPermissions(h PermissionHandler) RunnerOption
```

Sets the permission handler consulted before every tool call. Returning `false` rejects the call.

**Default:** nil (every tool call permitted).

#### `WithRunnerProgress`

```go
func WithRunnerProgress(s ProgressSink) RunnerOption
```

Installs the `ProgressSink` the runner emits lifecycle events and streaming output through.

**Default:** nil (events discarded).

#### `WithRunnerPreStartDrainGate`

```go
func WithRunnerPreStartDrainGate(gate <-chan struct{}) RunnerOption
```

Test-only hook: when non-nil, `Run` blocks on receive from this channel BEFORE the first mailbox drain. Lets tests hold the pre-start drain while setting up mailbox preconditions.

**Default:** nil (production behavior).

## Storage and memory

### `WithStorage`

```go
func WithStorage(s Storage) Option
```

Sets the persistence backend. Used by `RunFlow` to checkpoint step results (enabling `ResumeFlow`) and by `WithSharedMemory` to persist cross-step memory.

**Default:** `NewMemoryStorage()` (in-process map; lost when the orchestrator exits). The `zenflow` CLI binary installs `NewFileStorage(~/.zenflow/runs/)` instead of this default to enable `--resume` across processes; library embedders inherit the in-memory default.

**For resume:** use `NewFileStorage(dir)` or implement the `Storage` interface against your database.

`RunAgent` does not persist state - this option only affects workflow runs.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithStorage(zenflow.NewFileStorage("/var/lib/zenflow")),
)
```

### `WithSharedMemory`

```go
func WithSharedMemory(sm *SharedMemory) Option
```

Installs a `SharedMemory` instance so `shared_memory_read` and `shared_memory_write` tools auto-inject into agent tool chains during `RunFlow` / `ResumeFlow`. Lets agents collaborate via a key-value store namespaced by writer.

**Default:** nil (no shared memory tools available).

```go
sm := zenflow.NewSharedMemory()
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithSharedMemory(sm),
)
```

See [Concepts → Shared Memory](../concepts/shared-memory) for the read/write contract.

### `WithTranscriptStore`

```go
func WithTranscriptStore(factory func() TranscriptStore) Option
```

Installs a `TranscriptStore` factory used by the resume mechanism. The factory is invoked once per `Executor.Run` so each run gets a fresh store.

**Default:** per-run `InMemoryTranscriptStore` (intra-run resume only).

**When to use:** cross-run or cross-process resume. Supply a persistent (file / SQLite / etc.) store.

### `WithMaxTranscriptMessages`, `WithMaxTranscriptBytes`

```go
func WithMaxTranscriptMessages(n int) Option
func WithMaxTranscriptBytes(b int64) Option
```

Override the per-step caps for the default `InMemoryTranscriptStore`. Ignored when a custom store is supplied via `WithTranscriptStore`.

**Defaults:** 10000 messages, 50 MiB.

## Permissions and approval

### `WithPermissions`

```go
func WithPermissions(h PermissionHandler) Option
```

Sets the permission handler. Each tool call invokes `RequestPermission` before execution; returning `false` rejects the call and surfaces the rejection through the [goai](https://goai.sh) tool loop.

**Default:** nil (every tool call permitted).

**When to use:** interactive CLIs (prompt the user), security policies (deny destructive tools), or audit logging.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithPermissions(myInteractivePermissionPrompt),
)
```

### `WithApproval`

```go
func WithApproval(h ApprovalHandler) Option
```

Sets the plan approval handler used by `RunGoal`. After the LLM produces a workflow plan, the handler is asked to approve it before execution. Returning `false` aborts the run with the sentinel `zenflow.ErrPlanDenied` (canonical text: `"zenflow: plan denied by approval handler"`; match via `errors.Is(err, zenflow.ErrPlanDenied)`). See [Errors → Sentinel errors](./errors#sentinel-errors).

**Default:** nil (plans execute immediately, no confirmation).

**When to use:** CLI consumers that want a "type yes to run this DAG" prompt; programmatic gates that reject plans touching forbidden tools or steps.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithApproval(myCLIPromptApprover),
)
```

### `WithApprovalTimeout`

```go
func WithApprovalTimeout(d time.Duration) Option
```

Bounds how long `ApprovalHandler.ApprovePlan` may block. Must be applied **after** `WithApproval` - the option wraps the previously installed handler, so applying it before `WithApproval` is a no-op. On timeout, `ApprovePlan` returns `(false, zenflow.ErrApprovalTimeout)` and `RunGoal` aborts cleanly. Applying the option twice is safe; the second call is a no-op (no double-wrapping).

**Default:** zero (no timeout - the handler may block indefinitely). Zero or negative values are also a no-op.

**When to use:** interactive CLI / TUI approval prompts where a human may walk away from the keyboard, or programmatic gates that must not stall a long-running orchestrator. Distinguish "timeout" from "user cancel" by inspecting the error: `errors.Is(err, zenflow.ErrApprovalTimeout)`.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithApproval(myCLIPromptApprover),
    zenflow.WithApprovalTimeout(2 * time.Minute),
)
```

## Output

### `WithProgress`

```go
func WithProgress(s ProgressSink) Option
```

Installs a `ProgressSink`. zenflow emits one event per lifecycle transition (workflow start/end, step start/end, agent turn, tool call, error, etc.) and one `Output` per streaming token (when streaming is on).

**Default:** nil (events discarded; no observable progress).

The two built-in sinks live in `zenflow/sink`:

- `sink.NewStdoutSink()` - human-readable progress with glyphs and colors.
- `sink.NewJSONSink()` - NDJSON to stdout, the CLI's `--json` mode.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithProgress(sink.NewStdoutSink()),
)
```

For composition (e.g., write to both file and stdout), implement `ProgressSink` and fan out manually.

### `WithStreaming`

```go
func WithStreaming() Option
func WithoutStreaming() Option
```

Enables streaming mode. Content visible to the user is delivered token-by-token via `ProgressSink.OnOutput` instead of as full text after the LLM call returns.

**Default:** false.

**When to enable:** TUIs, chat interfaces, anywhere a user is watching output flow in real time. CI / batch / scripted use cases want it off (full lines per event are easier to parse).

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithProgress(sink),
    zenflow.WithStreaming(),
)
```


### `WithVerbose`

```go
func WithVerbose() Option
func WithoutVerbose() Option
```

Enables agent output display. When on, agent LLM responses are surfaced via `ProgressSink.OnOutput` in addition to the workflow events and coordinator narration. When off, only workflow events and narration are shown.

**Default:** false.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithProgress(sink),
    zenflow.WithVerbose(),
)
```


### `WithOutputTransform`

```go
func WithOutputTransform(t OutputTransformer) Option
```

Installs a transformer applied to step outputs before they are injected into dependent steps' prompts. Use this to implement smart truncation based on the target model's context window.

**Default:** nil (uses fixed-size byte truncation, currently 16 KB per dep, 120 KB total prompt cap).

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithOutputTransform(myContextAwareCompactor),
)
```

### `WithProgressBufferSize`

```go
func WithProgressBufferSize(n int) Option
```

Controls the buffer size of the non-blocking progress sink wrapper. Larger buffers tolerate slower downstream sinks at the cost of more buffered memory.

**Default:** `defaultEventBusBuffer` = 1024.

**Behavior:** emits are non-blocking while the buffered channel has capacity. On overflow, the wrapper applies up to 1 second of bounded back-pressure; if the channel is still full at the deadline, the event is dropped and an internal counter is incremented.

## Observability

### `WithTracer`

```go
func WithTracer(t Tracer) Option
```

Installs a `Tracer` for distributed tracing. The OTel sub-module (`zenflow/observability/otel`) provides an implementation that creates OTel spans for every workflow, goal, agent, and step run.

**Default:** nil (no spans produced).

**Span names:** `zenflow.flow`, `zenflow.goal`, `zenflow.agent`, `zenflow.step`, `zenflow.coordinator`, `zenflow.loop` / `zenflow.loop.iteration`, `zenflow.include`. Attributes vary by span - see [Integrations → Observability](../integrations/observability).

```go
import zenotel "github.com/zendev-sh/zenflow/observability/otel"

orch := zenflow.New(
    zenflow.WithModel(model),
    zenotel.WithTracing(),
)
```

### `WithDropCallback`

```go
func WithDropCallback(fn func(DropEvent)) Option
```

Installs a user-supplied observer invoked once per dropped router message, in addition to the `EventMessageDropped` event already emitted via `ProgressSink`. Use this for metrics / alerting paths that don't want to subscribe to the full event firehose.

**Default:** nil.

**Synchronous by default.** Set `WithDropCallbackBufferSize` to a positive value to dispatch asynchronously through a buffered channel.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithDropCallback(func(d zenflow.DropEvent) {
        prometheusCounter.WithLabelValues(d.Reason.String()).Inc()
    }),
)
```

### `WithDropCallbackBufferSize`

```go
func WithDropCallbackBufferSize(n int) Option
```

Selects the buffer size for asynchronous dispatch of drop-callback events.

**Default:** 0 (synchronous dispatch).

**Overflow behavior:** if the buffered channel fills up, the callback falls back to synchronous dispatch so no drop event is itself silently lost.

```go
orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithDropCallback(myCallback),
    zenflow.WithDropCallbackBufferSize(64),
)
```

### `WithRouterObserver`

```go
func WithRouterObserver(fn func(*MessageRouter)) Option
```

Registers a callback invoked once per `RunAgent` / `RunFlow` invocation with the freshly-allocated `MessageRouter` for that run. Intended for observability hooks (telemetry, debug inspectors, integration tests) that need a handle on the per-call router without polling internal state.

**Default:** nil.

**Panic semantics:** if the callback panics, the panic IS recovered (the run continues) and logged via `slog.Error`. Production callers typically leave this unset.

## Per-call options

### `WithFlowContext`

```go
func WithFlowContext(ctx string) RunFlowOption
```

Supplies use-case input to the workflow steps for one specific `RunFlow` call. Behavior depends on whether a coordinator is installed:

- **With `WithCoordinator`:** the context is pushed into the coord's mailbox as the first event (`workflow_start` with `Context` field set) so the coord LLM can curate per-step forwards.
- **Without a coordinator (or `WithCoordinator(nil)`):** the context is blanket-prepended to every step's effective user prompt as a static fallback.

**Default:** `""` (no flow context, no blanket injection).

```go
result, err := orch.RunFlow(ctx, wf,
    zenflow.WithFlowContext("PR #123: optimize message routing"),
)
```

### `WithGoalContext`

```go
func WithGoalContext(ctx string) RunGoalOption
```

Supplies additional context (beyond the goal text itself) to the `RunGoal` decomposition prompt. Appended as a clearly-labelled `## Goal Context` section so the decomposition LLM uses it without parsing the goal text for context cues.

**Default:** `""` (no extra context).

```go
result, err := orch.RunGoal(ctx,
    "Audit the message routing path for race conditions",
    zenflow.WithGoalContext("Repo uses Mailbox+Wake delivery; pay attention to that contract."),
)
```

## Other options

A few options exist for advanced use cases or testing infrastructure:

- **`WithMailboxStore(factory)`** - replace the default `InMemoryMailboxStore` with a custom backend (e.g., SQLite for multi-process workflows). Factory is invoked once per run.
- **`WithMailboxDelivery()` / `WithoutMailboxDelivery()`** - toggle the entire mailbox + delivery engine stack. Defaults to enabled. Mostly used by tests that exercise the scheduler path without messaging machinery.
- **`WithExternalInbox(ids...)`** - pre-register non-step sender inboxes (e.g., `"coordinator"`) on the MessageRouter so reverse-routed responses don't drop with `DropReasonUnknownStep`.
- **`WithModelResolver(r)`** - install a `ModelResolver` consulted by the resume path when a saved transcript references a model identifier different from the executor's default.
- **`WithTruncationOnCapReached()` / `WithoutTruncationOnCapReached()`** - configure the resume path to fall back to a truncated load when a sealed transcript hits its cap. Default disabled (sealed transcripts fail the resume loudly).
- **`WithRunID(runID string)`** - pin the orchestrator's run identifier. Without this, zenflow generates a fresh ID internally; useful when an HTTP server has already returned a run ID to the caller and needs the emitted events to carry the same ID.

## Coordinator-loop options

These options configure `RunCoordinatorLoop` (see [Core Functions → RunCoordinatorLoop](./core-functions#runcoordinatorloop)). They are a separate option family (`CoordLoopOption`) - not interchangeable with `Option` / `RunFlowOption` / `RunGoalOption`.

### `WithCleanupTimeout`

```go
func WithCleanupTimeout(d time.Duration) CoordLoopOption
```

Bounds the cleanup phase of the func returned by `RunCoordinatorLoop`. After the inner context is cancelled, the cleanup waits up to `d` for the loop goroutine to exit, then returns. Caps shutdown latency: a stuck coord LLM call cannot block the caller indefinitely.

**Default:** `DefaultCoordCleanupTimeout` = 2 seconds. Zero or negative values fall back to the default.

```go
cleanup := zenflow.RunCoordinatorLoop(ctx, runner, modelID,
    zenflow.WithCleanupTimeout(5 * time.Second),
)
defer cleanup()
```

## Public defaults

A handful of `Default*` constants are exported so embedders can compute relative caps (e.g., "2x the default mailbox size") without hardcoding magic numbers that may shift in future releases.

| Constant | Value | What it caps |
| --- | --- | --- |
| `DefaultMaxBytesPerDep` | `8 * 1024` (8 KiB) | Per-dependency byte cap applied by the orchestrator's default `OutputTransform` when `WithOutputTransform` is not provided. Truncates upstream step content before injection into a downstream step's prompt. |
| `DefaultMaxMailboxSize` | `10000` | Per-step mailbox cap installed by `New()` when `WithMaxMailboxSize` is not provided. Pass `WithMaxMailboxSize(0)` to opt out of the cap; pass any positive value to override. |
| `DefaultCoordCleanupTimeout` | `2 * time.Second` | Default for `WithCleanupTimeout` on `RunCoordinatorLoop`. The cap on the cleanup func's wait-for-exit phase. |
| `DefaultAgentHandleTTL` | `30 * time.Minute` | Wall-clock TTL on `RunAgentAsync` handles. Override per-orchestrator via `WithAgentHandleTTL`. Documented under [Types → `AgentError`](./types#agenterror). |
