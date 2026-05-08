---
title: Execution Modes
description: zenflow exposes three ways to run an LLM-driven task. Each maps to one CLI verb and one library entry point on the Orchestrator.
---

# Execution Modes

zenflow exposes three ways to run an LLM-driven task. Each maps to one CLI verb and one library entry point on the `Orchestrator`.

| Mode | CLI verb | Library method | Input | Output |
|------|----------|----------------|-------|--------|
| Flow | `zenflow flow` | `Orchestrator.RunFlow` | A YAML / JSON workflow file (a fixed DAG) | `*WorkflowResult` with per-step results |
| Goal | `zenflow goal` | `Orchestrator.RunGoal` | A natural-language goal string | `*WorkflowResult` (LLM decomposes the goal into a workflow first) |
| Agent | `zenflow agent` | `Orchestrator.RunAgent` | A single prompt | `*AgentResult` with content, structured result, and token usage |

These three modes share one thing: they all run on top of the same agent runner, the same tool catalogue, the same [goai](https://goai.sh) provider stack. They differ in what the caller hands in and how zenflow turns it into a graph of LLM calls.

## When to use which

<figure class="zf-diagram">
<svg viewBox="0 0 760 360" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Decision tree: do you know the workflow steps? if yes, agent or flow? one prompt = agent; many steps = flow. If no, do you have a goal? if yes, use goal mode.">
  <!-- Question 1 -->
  <g>
    <rect class="zf-mode-q" x="280" y="20" width="200" height="44"/>
    <text class="zf-mode-q-text" x="380" y="46" text-anchor="middle">know the steps?</text>
  </g>
  <!-- yes / no edges to row 2 -->
  <line class="zf-mode-edge" x1="340" y1="64" x2="200" y2="106"/>
  <line class="zf-mode-edge" x1="420" y1="64" x2="560" y2="106"/>
  <text class="zf-mode-edge-label zf-mode-edge-yes" x="240" y="92" text-anchor="middle">yes</text>
  <text class="zf-mode-edge-label zf-mode-edge-no"  x="520" y="92" text-anchor="middle">no</text>

  <!-- Q2 left: agent | flow? -->
  <g>
    <rect class="zf-mode-q" x="80"  y="106" width="240" height="44"/>
    <text class="zf-mode-q-text" x="200" y="132" text-anchor="middle">one prompt or many steps?</text>
  </g>
  <!-- Q2 right: have a goal? -->
  <g>
    <rect class="zf-mode-q" x="440" y="106" width="240" height="44"/>
    <text class="zf-mode-q-text" x="560" y="132" text-anchor="middle">have a goal?</text>
  </g>

  <!-- Edges from Q2 left to outcomes -->
  <line class="zf-mode-edge" x1="160" y1="150" x2="120" y2="220"/>
  <line class="zf-mode-edge" x1="240" y1="150" x2="280" y2="220"/>
  <text class="zf-mode-edge-label" x="125" y="186" text-anchor="middle">one</text>
  <text class="zf-mode-edge-label" x="275" y="186" text-anchor="middle">many</text>

  <!-- Edge from Q2 right to outcome -->
  <line class="zf-mode-edge" x1="560" y1="150" x2="560" y2="220"/>
  <text class="zf-mode-edge-label" x="576" y="186" text-anchor="start">yes</text>

  <!-- Outcomes row -->
  <g>
    <rect class="zf-mode-out zf-mode-out-agent" x="40" y="220" width="160" height="56"/>
    <text class="zf-mode-out-name zf-mode-out-name-agent" x="120" y="244" text-anchor="middle">agent</text>
    <text class="zf-mode-out-detail" x="120" y="262" text-anchor="middle">one chat turn</text>
  </g>
  <g>
    <rect class="zf-mode-out zf-mode-out-flow" x="200" y="220" width="160" height="56"/>
    <text class="zf-mode-out-name zf-mode-out-name-flow" x="280" y="244" text-anchor="middle">flow</text>
    <text class="zf-mode-out-detail" x="280" y="262" text-anchor="middle">declared YAML DAG</text>
  </g>
  <g>
    <rect class="zf-mode-out zf-mode-out-goal" x="480" y="220" width="160" height="56"/>
    <text class="zf-mode-out-name zf-mode-out-name-goal" x="560" y="244" text-anchor="middle">goal</text>
    <text class="zf-mode-out-detail" x="560" y="262" text-anchor="middle">coordinator plans</text>
  </g>

  <!-- One-line caption -->
  <text class="zf-mode-caption" x="380" y="320" text-anchor="middle">three modes · same agent runner · same tool catalogue · same goai stack</text>
</svg>
<figcaption>Pick by what you can hand the runtime: a YAML DAG (<code>flow</code>), a goal string (<code>goal</code>), or a single prompt (<code>agent</code>).</figcaption>
</figure>

- **flow**: you already wrote (or generated, or templated) a workflow YAML. You want zenflow to schedule the DAG, route messages, and return per-step output. Use this for CI pipelines, code reviews, multi-stage refactors, anything where the structure is known.
- **goal**: you want the LLM to decide the structure. The coordinator decomposes the goal into agents + steps + dependencies, then runs the resulting flow. Use this for ad-hoc tasks where the user describes intent rather than structure.
- **agent**: you want one chat turn (with tool calls and optional child agent spawning) and a single result back. No DAG, no coordinator, no per-step persistence. Use this for shell scripts, slash commands, anywhere a one-shot LLM invocation makes sense.

## CLI

All three verbs share the same flag surface for model selection (`--model`), timeout (`--timeout`), output format (`--json`, `--quiet`, `--summary-only`, `--stream`, `--verbose`), and working directory (`--workdir`). See [CLI overview](/cli/) for the full list.

### `zenflow flow`

```bash
zenflow flow path/to/workflow.yaml
zenflow flow path/to/workflow.yaml "extra context for this run"
zenflow flow path/to/workflow.yaml --model claude-sonnet-4-6 --json
zenflow flow path/to/workflow.yaml --resume <run-id>
```

The optional second positional argument is per-call flow context. It is pushed into the coordinator's mailbox as the workflow_start event and (when no coordinator is installed) prepended to every step's prompt.

### `zenflow goal`

```bash
zenflow goal "Refactor the auth module to use JWT, then write tests."
zenflow goal "Audit the repo for outdated deps" "Be conservative - flag, don't upgrade."
zenflow flow workflow.yaml --plan   # print the DAG diagram before executing (flow only)
zenflow goal "..." --json
```

The coordinator LLM emits a JSON workflow that zenflow parses, validates, and executes via the same `RunFlow` path. `--plan` is a `zenflow flow` flag (not `zenflow goal`); it prints the DAG diagram from `EventPlanReady` while the flow runs, it does not skip execution. With an `ApprovalHandler` configured (library use), the plan is gated on approval before the flow starts.

Retry policy: up to 2 retries on JSON parse errors, 1 retry on validation errors, both budgets shared across the whole `RunGoal` call.

### `zenflow agent`

```bash
zenflow agent "Write a unit test for cmd/foo/handler.go."
zenflow agent "..." --max-turns 20 --stream
echo "extra context from stdin" | zenflow agent "Summarize this." --stream
```

A single agent loop. No DAG, no per-step checkpoints. The agent has access to the configured tool catalogue and may spawn child agents through the built-in `agent` tool (bounded by `--max-depth`).

## Library

The `Orchestrator` is the entry point for all three modes:

```go
import (
    "context"
    "github.com/zendev-sh/zenflow"
    "github.com/zendev-sh/goai/provider/azure"
)

llm := azure.Chat("gpt-5") // returns provider.LanguageModel directly
orch := zenflow.New(
    zenflow.WithModel(llm),
    zenflow.WithTools(myTools...),
    zenflow.WithProgress(progressSink),
)
```

### `RunFlow`

```go
wf, err := zenflow.LoadWorkflow("workflow.yaml")
if err != nil {
    return err
}
result, err := orch.RunFlow(ctx, wf)
if err != nil {
    return err
}
for stepID, sr := range result.Steps {
    fmt.Printf("%s [%s]: %s\n", stepID, sr.Status, sr.Content)
}
```

For per-call flow context, pass the variadic `RunFlowOption`:

```go
result, err := orch.RunFlow(ctx, wf, zenflow.WithFlowContext("audit only, no fixes"))
```

### `RunGoal`

```go
result, err := orch.RunGoal(ctx, "Refactor auth to JWT and add tests.")
```

Add per-call goal context:

```go
result, err := orch.RunGoal(ctx, goal, zenflow.WithGoalContext("Stay within stdlib."))
```

To gate plan execution on user approval, install `WithApproval`. Without one, the plan executes immediately after parsing.

### `RunAgent`

```go
result, err := orch.RunAgent(ctx, zenflow.AgentConfig{
    Prompt:   "Summarize the README.",
    Model:    "claude-sonnet-4-6",
    MaxTurns: 10,
})
fmt.Println(result.Content)
```

`AgentConfig` carries every per-call override. Empty fields fall back to orchestrator defaults: `cfg.Model` falls back to `WithDefaultModel`; `cfg.CallTools` falls back to `WithTools`; `cfg.ProgressSink` falls back to `WithProgress`; `cfg.MaxTurns` falls back to `WithMaxTurns`.

`RunAgent` runs a single conversation. Child agents spawned via the built-in `agent` tool are bounded by `WithMaxDepth` (default 3).

### `ResumeFlow`

`ResumeFlow` re-enters a workflow from its checkpoint. Completed steps are loaded from `Storage`; failed, cancelled, and skipped steps are re-executed.

```go
result, err := orch.ResumeFlow(ctx, runID, wf)
```

Resume requires `WithStorage(...)` to have been set when the original run started. The default `MemoryStorage` is per-process, so meaningful resume across processes requires a persistent storage backend.

## Mode comparison cheat-sheet

| Question | flow | goal | agent |
|----------|------|------|-------|
| Who decides the steps? | You (YAML) | The LLM | N/A (one prompt) |
| DAG scheduler runs? | Yes | Yes (after decomposition) | No |
| Coordinator runner active? | Yes if `WithCoordinator` is set | Yes during decomposition; flow uses `WithCoordinator` if set | No |
| Per-step persistence? | Yes (via `WithStorage`) | Yes | No |
| Resumable? | Yes (`ResumeFlow`) | Indirectly (resume the decomposed flow) | No |
| Suitable for CI / cron | Yes | Yes (review via narration stream; `--plan` is flow-only and rejected by goal) | Yes for one-shot tasks |
| Plan approval gate? | N/A | Optional (`WithApproval`) | N/A |
| Child agent spawning | No | No | Yes (bounded by `WithMaxDepth`) |

## Common patterns

### CI pipeline that fails the job on workflow failure

```bash
zenflow flow .github/workflows/zenflow-review.yaml --json --quiet > out.json
zenflow flow .github/workflows/zenflow-review.yaml || exit 1
```

`zenflow flow` exits non-zero on `StatusFailed` and `StatusPartial`, which is what most CI runners want.

### Goal with plan approval

There is no CLI dry-run for `zenflow goal`; the coordinator decomposes the goal and immediately executes the resulting workflow. To gate execution on user approval, use the library API and install `WithApproval(handler)`; the handler receives the parsed `*Workflow` and can return false to abort before any step runs.

To inspect the DAG diagram of a `zenflow flow` run as it starts, pass `--plan`:

```bash
zenflow flow workflow.yaml --plan
```

This prints the DAG from the `EventPlanReady` event while the flow proceeds; it does not block on confirmation.

### Library-level orchestrator reuse

A single `Orchestrator` instance can serve any number of `RunFlow` / `RunGoal` / `RunAgent` calls concurrently. Each call gets its own router and mailbox. Close the orchestrator with `orch.Close()` when shutting down to drain background handles.

## See also

- [DAG scheduling](/concepts/dag-scheduling) - how `flow` builds and walks the graph
- [Coordinator](/concepts/coordinator) - the LLM that supervises a flow run
- [Resume](/concepts/resume) - resume mechanics and current verification status
- [API: Core Functions](/api/core-functions) - the full method signatures
