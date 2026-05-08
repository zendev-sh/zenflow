---
title: Coordinator and agent tools
description: Two tool families - coord-side (forward_to_agent, narrate, finalize, send_message) wired by NewDefaultCoordRunner / step-runner auto-injection, and agent-side (submit_result, agent) auto-installed on every step runner that has a ResultSchema or a child spawner.
---

# Coordinator and agent tools

This page documents two distinct tool families:

- **Coord-side tools** - run on the coordinator (or step runners that share the MessageRouter). Cover routing (`forward_to_agent`, `send_message`), narration (`narrate`), and termination (`finalize`).
- **Agent-side tools** - run on every step / child agent. Cover structured-result delivery (`submit_result`) and child spawning (`agent`).

Both families are exposed as public constructors so embedders who build a custom coordinator or a hand-rolled step runner can compose the same toolchain without re-implementing it.

## Coord-side tools

The default coordinator (built by `NewDefaultCoordRunner`) wires three tools (`forward_to_agent`, `narrate`, `finalize`); a fourth helper, `send_message`, is auto-injected on every step runner that has a MessageRouter AND is not the coordinator itself (detection: presence of `forward_to_agent` in the runner's tool list marks the coordinator). Step runners that already have a `send_message` tool keep their own - no overwrite. This page documents all four for reference.

All four take a `*AgentRunner` argument and return a `goai.Tool`. The runner supplies the wiring: `MessageRouter` for routing, `Progress` for narration, the `finalized` atomic + `finalizeCh` channel for finalize. Missing wiring (e.g. nil `MessageRouter`) surfaces as an explicit tool-result error so the LLM observes a clear failure reason instead of silent loss.

## `ForwardToAgentToolDef(runner *AgentRunner) goai.Tool`

Returns the `forward_to_agent` tool. Coord LLM calls it to route a message into a running step's mailbox for context injection, follow-up questions, or instructions.

- Routes via `runner.Router().Send` (NOT direct `mailbox.Append`) so the `MessageRouter`'s lifecycle checks (closed-mailbox detection, pending-senders accounting, drop emission via the executor's installed `OnDrop` callback) all fire normally.
- Drops surface BOTH as `EventMessageDropped` (via `OnDrop`) AND as the tool's returned result string `"dropped: <reason>"` so the LLM observes the failure on the same turn the call was made.
- Supports `kind="info"` (default), `kind="context_update"`, `kind="cancel"`. Unknown kinds fall back to `info` rather than erroring; content is preserved either way.
- Wrapper-step targets (loop / include containers) surface as the tool's result string prefixed `"rejected: "` (NOT a Go error - the LLM sees it as a normal tool result). A fallback narration also fires (when a Progress sink is wired) so the dropped content is preserved on the user log; messages routed to a wrapper would sit unread (silent misroute) without this guard.

### Parameters

| Parameter | Type | Required | Description |
|---|---|---|---|
| `target_step_id` | string | yes | Step ID to deliver to. Either bare (`"worker"`) or namespaced (`"loop-stages.0.worker"`) accepted. |
| `text` | string | yes | Message body. |
| `kind` | string | no | Message type. Enum: `info`, `context_update`, `cancel`. Unknown values fall back to `"info"`. |

### Result format (shared with `send_message`)

- success → `"queued: msg-fwd-<n>"` where `n` is a monotonic counter shared between `forward_to_agent` and `send_message` on the same runner (both tools increment the same `runner.fwdSeq`).
- drop → `"dropped: <reason>"` where `<reason>` is the canonical [DropReason](./errors#dropreason) string.
- unknown-step drop only → the result string additionally appends a `buildUnknownStepHint` block: `". Attempted target \"<id>\" is not a registered step. Available step IDs (current snapshot): <list>."` followed by an `ACTION REQUIRED` directive instructing the LLM to either retry with a valid ID or call `narrate` with the same content. Other drop reasons (target-terminal, coord-down, cap-exhaustion) keep the original concise message since the LLM cannot correct those by re-targeting.
- nil-MessageRouter → Execute error `"forward_to_agent: no router available (coord not wired into a workflow)"` (distinct from `send_message`'s nil-MessageRouter behaviour).

## `SendMessageToolDef(runner *AgentRunner) goai.Tool`

Returns the `send_message` tool. Auto-injected on every step runner where (a) `MessageRouter != nil` and (b) the runner's tool list does NOT already contain `forward_to_agent` (which marks the coord). If the runner already has a `send_message` tool, the existing one is preserved (no overwrite). The coord's outbound channel is `forward_to_agent` (preventing the coord from messaging itself in a recursion loop).

Step agents use `send_message` to push a message to the workflow coordinator's inbox. The destination is **hardcoded** to the canonical coord inbox key (`CoordRouterInboxID = "coordinator"`) - there is no `target_step_id` parameter on the input schema. Hub-and-spoke is preserved by construction: agents can only address the hub; siblings are unreachable. The coord decides forwarding via `forward_to_agent`.

### Parameters

| Parameter | Type | Required | Description |
|---|---|---|---|
| `text` | string | yes | Message body delivered to the coordinator's inbox. No other parameters; the destination is hardcoded. |

### Result format

- success → `"queued: msg-send-<n>"` where `n` is the same monotonic counter shared with `forward_to_agent` on the same runner.
- router drop → `"dropped: <reason>"` with the canonical [DropReason](./errors#dropreason) string, returned as the result string with a nil Go error (drops also surface as `EventMessageDropped` via the MessageRouter's `OnDrop`).
- no-MessageRouter (e.g. `RunAgent` without a coordinator) → result string `"dropped: no-coordinator"` with a nil Go error - the tool does not fail loudly. This is intentionally different from `forward_to_agent`'s nil-MessageRouter path (which returns an Execute error): `send_message` is callable from steps in any zenflow context, so a missing coord is treated as a runtime routing outcome rather than a coord-side configuration bug.
- empty / whitespace-only `text` → Execute error `"send_message: text is required and must be non-empty"`.

## `NarrateToolDef(runner *AgentRunner) goai.Tool`

Returns the `narrate` tool. Coord LLM calls it to push a progress event onto `runner.Progress` as `EventCoordinatorNarration`. Independent of the routing path: narrate emissions never enter a mailbox.

### Parameters

| Parameter | Type | Required | Description |
|---|---|---|---|
| `text` | string | yes | Narration text emitted to the Progress sink as one `EventCoordinatorNarration`. |

### Result format

- success → result string `"narrated"`.
- empty / whitespace-only `text` → Execute error `"narrate: text is required and must be non-empty"`.
- nil Progress sink → Execute error `"narrate: no progress sink available"` (the tool does not fall back to the mailbox; it is sink-only).

Notes:

- One-line, free-form text. The CLI's stdout sink renders it with the `≋` prefix; JSON sink emits it as a structured event.
- Repeated rapid-fire calls are not rate-limited at the library layer; operators who need rate-limiting wrap the sink.

## `FinalizeToolDef(runner *AgentRunner) goai.Tool`

Returns the `finalize` tool. Coord LLM calls it to signal "the workflow is done, here is my synthesis." Sets `runner.finalized` to `true`, stores the optional summary on `runner.finalSummary`, and closes the runner's `Done()` channel so callers blocked on it unblock cleanly.

### Parameters

| Parameter | Type | Required | Description |
|---|---|---|---|
| `summary` | string | no | Final synthesis text. Empty string still finalizes the runner. On repeated calls the per-call summary overwrites the prior value (last-writer-wins). |

### Result format

- success → result string `"finalized"`.

### Idempotency

The `Done()` channel close is `sync.Once`-guarded, so repeated `finalize` calls do not panic and do not re-close. The `finalized` atomic is set on every call (already-true → still-true). The `finalSummary` is overwritten on each call (last-writer-wins).

### Loop-exit semantics

- The CLI does NOT honour `finalize` as a workflow-exit signal; the workflow's own DAG completion is authoritative. The CLI's coord loop (`cmd/zenflow/main.go`) is `for { runner.Run; WaitForCoordWake }` and exits only on context cancellation - it does not consult `runner.Finalized()`. `finalize` is advisory; it lets the coord deliver a synthesis and stop without blocking workflow termination.
- Embedders who DO want the signal can build their own coord loop that selects on `runner.Done()` and exits on close. `runner.FinalSummary()` exposes the same state for poll-driven loops.

## Composition example

```go
import (
    "context"
    "github.com/zendev-sh/goai"
    "github.com/zendev-sh/goai/provider"
    "github.com/zendev-sh/zenflow"
)

func customCoord(llm provider.LanguageModel) *zenflow.AgentRunner {
    runner := &zenflow.AgentRunner{
        Model:        llm,
        SystemPrompt: zenflow.DefaultCoordSystemPrompt,
        StepID:       "coordinator",
        Mailbox:      zenflow.NewInMemoryMailboxStore(),
        Wake:         make(chan struct{}, 1),
        // MessageRouter and Progress are wired by the executor when the runner
        // is registered as the coord. If you build a runner by hand
        // outside an executor, the coord tools that depend on those
        // fields will surface clear errors / drops (see each tool's
        // "Result format" above) rather than panic.
    }
    runner.Tools = []goai.Tool{
        zenflow.ForwardToAgentToolDef(runner),
        zenflow.NarrateToolDef(runner),
        zenflow.FinalizeToolDef(runner),
        // Add custom coord tools here.
    }
    return runner
}
```

`NewDefaultCoordRunner` does the same wiring (`SendMessageToolDef` is intentionally NOT added to the coord, preventing the coord from sending to itself in a recursion loop).

## Auxiliary helpers

| Symbol | Purpose |
| --- | --- |
| `BuildCoordStepMenu(runner *AgentRunner) string` | Returns a human-readable list of the registered step IDs the coord can target with `forward_to_agent`. Used by the CLI's coord loop to append a freshly-snapshot list to every continuation prompt, so the LLM sees the live target set without re-discovering it across wake cycles. |
| `WaitForCoordWake(ctx context.Context, runner *AgentRunner) bool` | Blocks the caller until either `runner.Wake` fires or `ctx` cancels. Returns `true` on Wake, `false` on cancel. The CLI's coord loop uses this between `runner.Run` invocations to avoid tight-poll re-entry: without Wake-blocking the loop would burn one LLM call per iteration on filler narrations. |
| `DefaultCoordColdStartPrompt`, `DefaultCoordContinuationPrompt`, `DefaultCoordSystemPrompt` (constants) | Stable prompt strings used by `NewDefaultCoordRunner` and the CLI coord loop. Override the system prompt suffix via `WithCoordSystemPromptSuffix(extra)` instead of replacing the constants. |

## Agent-side tools (auto-installed on every step runner)

The agent-side family runs on the leaf agent (every step runner, every spawned child) - not on the coordinator. Two tools cover the two contracts a child agent has with the system: returning a structured result, and spawning sub-children. Both are auto-installed when the corresponding feature is configured on the runner; pure free-text agents see neither.

The agent-side tools are surfaced separately because (a) the coord never calls them (the coord finalizes via `finalize`, not `submit_result`) and (b) embedders who build a runner by hand outside `RunAgent` / `RunFlow` need the same factories the orchestrator uses internally.

### `zenflow.SubmitResultToolDef(schema map[string]any) goai.Tool`

Returns the `submit_result` tool. The agent LLM calls it exactly once as its terminal action to deliver a structured JSON result that conforms to the agent's `ResultSchema`. The Execute body is a no-op (`"ok"`); actual handling is intercepted by the runner's `OnAfterToolExecute` hook, which feeds the call into the matching `SubmitResultHandler` for validation and termination.

When-installed: **auto-installed by the runner whenever `cfg.ResultSchema != nil`** (see `agent_runner.go` Run path). Free-text agents (no schema) do not see this tool. If `schema` is `nil` the constructor returns a zero-value `goai.Tool` (Name=`""`); use `NewSubmitResultHandler` and this tool together, never independently.

The runner enforces the call: if the LLM finishes without calling `submit_result`, the runner appends a retry turn that re-injects ONLY the `submit_result` tool and re-prompts. After retry exhaustion, the runner returns [`ErrAgentNoSubmitResult`](./errors#async-agent-handles) wrapped in `AgentError`.

### `zenflow.SubmitResultHandler` and `zenflow.NewSubmitResultHandler(schema map[string]any) *SubmitResultHandler`

`SubmitResultHandler` is the validator the runner uses to parse, type-check, and accept `submit_result` calls. `NewSubmitResultHandler` constructs the default implementation; the returned handler validates the LLM's JSON args against `schema` (required fields, basic JSON-Schema type checking, recursive descent into nested objects and array items) before the runner stores the result and terminates.

Signature on the handler: `Handle(args json.RawMessage) (result map[string]any, terminated bool, err error)`. `terminated` is always `true` on success - `submit_result` is by contract the agent's last action.

When-installed: **paired with `SubmitResultToolDef` and constructed automatically inside the runner whenever `cfg.ResultSchema != nil`.** External callers normally do not instantiate this directly; the constructor is exported for embedders who build a custom runner-equivalent loop and need to mirror the validation contract.

### `zenflow.AgentToolDef() goai.Tool`

Returns the `agent` tool - the spawning primitive every step runner offers to its LLM so it can delegate a focused subtask to a child agent with its own context window, tool subset, and (optional) model override. The Execute body is a stub that returns [`ErrAgentToolDirectInvocation`](./errors#async-agent-handles); actual dispatch is intercepted by the runner's `OnBeforeToolExecute` hook, which routes the call into the registered `agentSpawner`.

Schema fields: `name` (alphanumeric, validates against the Bedrock/OpenAI tool-name regex `[a-zA-Z0-9_-]+`), `description`, `prompt` (role/persona), `tools` (subset of parent's tool names; empty inherits full set), `model` (omit to inherit parent; do NOT pass literal strings like `"default"` or `"auto"`), `instructions` (per-call task), `run_in_background` (default `false`; `true` returns immediately and delivers via inbox on the parent's next turn).

When-installed: **auto-appended by `RunAgent` and child-spawn paths** (`zenflow.go` line 459, `agent_tool.go` line 266). The tool is present on the runner's tool list whenever a `childSpawner` is wired; the spawner is wired by `RunAgent`/orchestrator and inherited by every spawned child up to `MaxDepth`. Hand-rolled runners that do not register a spawner can still include `AgentToolDef()` in their tool list, but invocations will fail with `ErrAgentToolDirectInvocation` until a spawner is set.

Reaching the Execute body indicates a wiring bug (spawner not registered or hook bypassed); the sentinel lets embedders detect this via `errors.Is` instead of substring matching, while preserving the `(string, error)` return contract so the goai loop surfaces a clean tool-result error rather than panicking mid-conversation.

### Composition example (hand-rolled step runner with both agent-side tools)

```go
import (
    "github.com/zendev-sh/goai"
    "github.com/zendev-sh/zenflow"
)

func customStepRunner(schema map[string]any) []goai.Tool {
    tools := []goai.Tool{
        // ... your domain tools ...
        zenflow.AgentToolDef(),               // requires a spawner wired into the runner
        zenflow.SubmitResultToolDef(schema),  // pair with NewSubmitResultHandler in your loop
    }
    return tools
}
```

`RunAgent` does the equivalent wiring internally (including the spawner registration and the retry-on-missing-submit_result loop); prefer it over hand-rolling.

## CEL evaluation helpers

The orchestrator evaluates CEL expressions on `Step.Condition` (skip-if-false), `Loop.Until` (stop condition), and `Loop.ForEach` (when given as a CEL expression instead of a literal list). The three helpers below are the public wrappers around the underlying `cel-go` machinery.

### `EvaluateCEL`

```go
func EvaluateCEL(expr string, ctx *EvalContext) (bool, error)
```

Compiles and evaluates `expr` against `ctx`, returning a `bool`. Compilation results are cached per-expression so repeated evaluations (typical for loop conditions) skip the parse + type-check cost. CPU is bounded via `cel.CostLimit(10000)`; expressions complete in microseconds-milliseconds. Non-bool results return an error so a typo like `steps.review.status == "completed"` (returns string) surfaces loudly rather than silently coercing.

CEL evaluation does NOT accept a `context.Context` for cancellation - the cost limit is the only bound.

### `EvaluateCELToArray`

```go
func EvaluateCELToArray(expr string, ctx *EvalContext) ([]any, error)
```

Variant that requires the expression to evaluate to a list. Used by `Loop.ForEach` to derive the iteration array from a CEL expression (e.g., `steps.list_files.result.files`). Returns an error if the result is not a list.

### `BuildEvalContext`

```go
func BuildEvalContext(results map[string]*StepResult) *EvalContext
```

Constructs an `*EvalContext` from the executor's results map. The resulting context exposes `steps.<id>.{content,result,status}` to CEL expressions. The executor calls this internally with its mutex held; embedders that want to dry-run a CEL expression against a snapshot of step results (e.g., a workflow linter) can call it directly.

## Prompt assembly helpers

`AssemblePrompt` and `AssemblePromptWithForEach` build the user-prompt string the executor passes to `goai.GenerateText` for each step. They are exported so embedders can dry-run the prompt construction (linters, prompt-debugging UIs) without invoking the executor.

### `AssemblePrompt`

```go
func AssemblePrompt(
    agent AgentConfig,
    step Step,
    baseDir string,
    priorResults map[string]*StepResult,
) (string, []provider.Part)
```

Returns `(promptText, attachments)`. Composes the agent's `Prompt`, the step's `Instructions`, the `ContextFiles` resolved from `baseDir`, and the upstream `priorResults` mapped onto `DependsOn` into a single user message. Attachments carry image/PDF refs from the agent config.

### `AssemblePromptWithForEach`

```go
func AssemblePromptWithForEach(
    agent AgentConfig,
    step Step,
    baseDir string,
    priorResults map[string]*StepResult,
    fe *ForEachContext,
) (string, []provider.Part)
```

Same as `AssemblePrompt` but accepts an optional `*ForEachContext` for loop iterations. The context exposes `iteration`, `item`, and `index` to the prompt assembler so the rendered prompt sees per-iteration state. Pass `nil` for non-loop steps (equivalent to `AssemblePrompt`).

## Goal-decomposition coordinator

The functions in this section drive `RunGoal`'s LLM-based workflow synthesis. Most embedders never call them directly - `RunGoal` composes them in the right order with retries. They are exported so embedders building a custom goal-decomposition flow (e.g., a multi-pass refiner that runs `CoordinatorChat` twice) can reuse the pieces.

### `CoordinatorChat`

```go
func CoordinatorChat(
    ctx context.Context,
    model provider.LanguageModel,
    prompt string,
) (string, provider.Usage, error)
```

Runs a single non-streaming LLM call against `model` with `prompt` as the user message and returns the assistant's text plus token usage. No tools, no system prompt, no retry. The decomposition path uses this when streaming is off.

### `CoordinatorStreamChat`

```go
func CoordinatorStreamChat(
    ctx context.Context,
    model provider.LanguageModel,
    prompt string,
    onText func(string),
    onReasoning func(string),
) (string, provider.Usage, error)
```

Streaming variant. `onText` is invoked per assistant text delta; `onReasoning` per reasoning/thinking delta (nil disables reasoning callbacks). Returns the full concatenated assistant text once the stream closes.

**When to use:** TUI consumers that want to render the coord's plan as it streams; `RunGoal` uses this when streaming is enabled on the orchestrator.

### `CoordinatorPrompt`

```go
func CoordinatorPrompt(goal, toolCatalog string) string
```

Builds the user prompt the coordinator LLM sees during goal decomposition. Embeds the goal text, the tool catalog (built via `BuildToolCatalog`), and the JSON schema instruction template. Exported so embedders can pre-compute the prompt for token-counting or caching layers.

### `BuildToolCatalog`

```go
func BuildToolCatalog(tools []goai.Tool) string
```

Renders the orchestrator's tool catalog as the human-readable list embedded in the coord prompt. Each entry includes the tool name, description, and a one-line summary of its input schema. Pair with `CoordinatorPrompt`.

### `ParseCoordinatorResponse`

```go
func ParseCoordinatorResponse(response string) (*Workflow, error)
```

Parses the coordinator's raw text response into a `*Workflow`. Strips markdown fences, extracts the JSON object, runs `ParseWorkflowJSON`, then `ApplyDefaults`. On JSON syntax failure returns `*JSONParseError` (the retry budget in `RunGoal` consumes one of its 2 JSON retries on this); on schema failure returns `*CoordinatorValidationError` (consumes the 1 validation retry).

### `ValidateToolNames`

```go
func ValidateToolNames(wf *Workflow, tools []goai.Tool) error
```

Cross-checks every `agent.Tools` and `agent.DisallowedTools` entry in `wf.Agents` against the names in `tools`. Returns `*ToolNotFoundError` listing the unknown names. `RunGoal` runs this immediately after `ParseCoordinatorResponse` so a hallucinated tool name fails the run rather than producing a workflow that would error mid-step.

## Tool filtering

### `FilterTools`

```go
func FilterTools(tools []goai.Tool, allow, disallow []string) []goai.Tool
```

Returns the subset of `tools` whose names are in `allow` (or all of them when `allow` is empty / nil) AND not in `disallow`. The executor uses it per-step to materialize the tool list a step's agent sees, given the agent's declared `Tools` / `DisallowedTools` against the orchestrator's full catalog.

**When to use:** building a custom step runner outside the orchestrator that needs the same allow/disallow semantics, or pre-computing per-agent tool lists for diagnostics.

## Shared-memory tool factory

### `NewSharedMemoryTools`

```go
func NewSharedMemoryTools(sm *SharedMemory, agentName string) []goai.Tool
```

Returns the two-tool slice (`shared_memory_read`, `shared_memory_write`) bound to `sm` and writing under the namespace `agentName`. The orchestrator auto-injects this onto every agent's tool list when `WithSharedMemory` is set; the factory is exported so embedders building a hand-rolled runner with shared-memory access can mirror the wiring.

`agentName` becomes the prefix on every key the agent writes (e.g., a writer named `"researcher"` storing `"findings"` produces the qualified key `"researcher.findings"`). Reading is unscoped - any agent can read any qualified key.
