---
title: Orchestrator
description: The Orchestrator is the public Go type you construct once per process and use everywhere else. It owns your model, your sinks, your storage, and...
---

# Orchestrator

The `Orchestrator` is the public Go type you construct once per process and use everywhere else. It owns your model, your sinks, your storage, and the optional coordinator runner, and it exposes the three entry points (`RunFlow`, `RunGoal`, `RunAgent`) that drive every other concern in zenflow.

It is, deliberately, the only top-level type to learn.

```go
orch := zenflow.New(
    zenflow.WithModel(llm),
    zenflow.WithProgress(sink),
    zenflow.WithStorage(zenflow.NewFileStorage(".zenflow")),
    zenflow.WithCoordinator(zenflow.NewDefaultCoordRunner(llm)),
)
defer orch.Close()

result, err := orch.RunFlow(ctx, wf)
```

Construction is plain functional options. Every `With*` option mutates one orchestrator-level field; nothing happens at construction beyond wiring. The first goroutine spawn is on the first `RunFlow`/`RunGoal`/`RunAgent` call.

## What it owns

| Field group | What it is | How it's set |
| --- | --- | --- |
| **Model & tools** | The default `provider.LanguageModel`, the registered tool catalog, and per-call goai options | `WithModel`, `WithTools`, `WithGoAIOptions` |
| **Storage & memory** | The `Storage` backend (memory or file) and the optional shared-memory store for cross-step KV | `WithStorage`, `WithSharedMemory`, `WithModelResolver` |
| **Sinks & approval** | Progress sink, drop callback, approval handler, permission handler, tracer | `WithProgress`, `WithDropCallback`, `WithApproval`, `WithPermissions`, `WithTracer` |
| **Coordinator** | The hosted `*AgentRunner` that handles cross-agent messaging when one is installed | `WithCoordinator(runner)` (optional) |
| **Workflow defaults** | Default model, max-concurrency, max-turns, max-depth, isolation, output transform | `WithDefaultModel`, `WithMaxConcurrency`, `WithMaxTurns`, `WithIsolation`, `WithOutputTransform` |

The orchestrator is **stateless across runs** for the workflow itself: every `RunFlow` call constructs a fresh `Executor`, a fresh `MessageRouter`, and a fresh in-memory `MailboxStore`. What persists across runs is the configuration you set on the orchestrator and, if you wired one, the coordinator runner instance (so the coord LLM keeps its conversation continuity if you opt to share it).

## Three entry points

| Entry point | What you give it | What it does |
| --- | --- | --- |
| `RunFlow(ctx, wf)` | A parsed `*Workflow` (from `LoadWorkflow` or built in code) | Runs a fully-declared YAML DAG end to end |
| `RunGoal(ctx, goal)` | A single-sentence goal string | Asks the coordinator to decompose the goal into a `Workflow`, then runs it |
| `RunAgent(ctx, cfg)` | An `AgentConfig` for one agent | Runs a single agent's tool loop with no DAG and no coordinator |

`ResumeFlow(ctx, runID, wf)` is the fourth entry point for picking up a previously-checkpointed run from `Storage`.

All four return either a `*WorkflowResult` (or `*AgentResult` for `RunAgent`) plus an error. The result carries per-step status, token totals, and content.

## Why it's the only type you import

Almost every other zenflow type is reachable through the orchestrator:

<figure class="zf-diagram">
  <p class="zf-diagram-title">orchestrator ownership graph</p>
  <svg viewBox="0 0 960 580" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Orchestrator ownership tree: Executor with MessageRouter and MailboxStore beneath it, plus AgentRunner (coordinator), ProgressSink, Storage, and Workflow as siblings.">
    <!-- ============ ROOT ============ -->
    <g>
      <rect class="zf-life-rect-stage" x="20" y="20" width="920" height="64"/>
      <text x="40" y="46" class="zf-life-name zf-life-name-stage">Orchestrator</text>
      <text x="40" y="68" class="zf-life-detail">zenflow.New(opts...) · long-lived · holds config + entry points · concurrency-safe</text>
    </g>
    <!-- ============ TREE TRUNKS + STUBS (zf-life-arrow style) ============ -->
    <!-- Main trunk from Orch bottom down to last sibling (Workflow @ y=518) -->
    <line class="zf-life-arrow" x1="60" y1="84" x2="60" y2="518"/>
    <!-- Stub to Executor (y=120 = mid of executor box) -->
    <line class="zf-life-arrow" x1="60" y1="120" x2="100" y2="120"/>
    <!-- Sub-trunk under Executor: Executor's only direct child is
         MessageRouter, so this trunk stops at MessageRouter stub level (y=200).
         (Mailbox is MessageRouter's child, not Executor's; it gets its own
         sub-sub-trunk below.) -->
    <line class="zf-life-arrow" x1="140" y1="142" x2="140" y2="200"/>
    <!-- Sub-stub to MessageRouter (y=200) -->
    <line class="zf-life-arrow" x1="140" y1="200" x2="180" y2="200"/>
    <!-- Sub-sub-trunk under MessageRouter (at y=178-222, Mailbox at y=234-278) -->
    <line class="zf-life-arrow" x1="220" y1="222" x2="220" y2="256"/>
    <!-- Sub-sub-stub to Mailbox (y=256) -->
    <line class="zf-life-arrow" x1="220" y1="256" x2="260" y2="256"/>
    <!-- Stub to AgentRunner (y=312) -->
    <line class="zf-life-arrow" x1="60" y1="312" x2="100" y2="312"/>
    <!-- Stub to ProgressSink (y=380) -->
    <line class="zf-life-arrow" x1="60" y1="380" x2="100" y2="380"/>
    <!-- Stub to Storage (y=448) -->
    <line class="zf-life-arrow" x1="60" y1="448" x2="100" y2="448"/>
    <!-- Stub to Workflow (y=518) -->
    <line class="zf-life-arrow" x1="60" y1="518" x2="100" y2="518"/>
    <!-- ============ L1: EXECUTOR ============ -->
    <g>
      <rect class="zf-life-rect-step" x="100" y="98" width="840" height="44"/>
      <text x="120" y="124" class="zf-life-name zf-life-name-step">Executor</text>
      <text x="120" y="138" class="zf-life-detail">per-run · transient · walks the DAG, spawns AgentRunners, returns WorkflowResult</text>
      <text x="920" y="124" text-anchor="end" class="zf-life-detail">executor.go</text>
    </g>
    <!-- ============ L2: ROUTER ============ -->
    <g>
      <rect class="zf-life-rect-coord" x="180" y="178" width="760" height="44"/>
      <text x="200" y="204" class="zf-life-name zf-life-name-coord">MessageRouter</text>
      <text x="200" y="218" class="zf-life-detail">per-run mailbox bus · hub-and-spoke routing</text>
      <text x="920" y="204" text-anchor="end" class="zf-life-detail">internal/router/</text>
    </g>
    <!-- ============ L3: MAILBOX STORE ============ -->
    <g>
      <rect class="zf-life-rect-coord" x="260" y="234" width="680" height="44"/>
      <text x="280" y="260" class="zf-life-name zf-life-name-coord">MailboxStore</text>
      <text x="280" y="274" class="zf-life-detail">in-memory by default · interface, swap with file or sqlite backend</text>
      <text x="920" y="260" text-anchor="end" class="zf-life-detail">internal/router/</text>
    </g>
    <!-- ============ L1: AGENT RUNNER (the coordinator) ============ -->
    <g>
      <rect class="zf-life-rect-tool" x="100" y="290" width="840" height="44"/>
      <text x="120" y="316" class="zf-life-name zf-life-name-tool">AgentRunner</text>
      <text x="120" y="330" class="zf-life-detail">the coordinator (optional) · LLM hub · forward_to_agent · narrate · finalize</text>
      <text x="920" y="316" text-anchor="end" class="zf-life-detail">agent_runner.go · coord_factory.go</text>
    </g>
    <!-- ============ L1: PROGRESS SINK ============ -->
    <g>
      <rect class="zf-life-rect-end" x="100" y="358" width="840" height="44"/>
      <text x="120" y="384" class="zf-life-name">ProgressSink</text>
      <text x="120" y="398" class="zf-life-detail">your hook into lifecycle events · stdout · json · custom</text>
      <text x="920" y="384" text-anchor="end" class="zf-life-detail">interfaces.go</text>
    </g>
    <!-- ============ L1: STORAGE ============ -->
    <g>
      <rect class="zf-life-rect-end" x="100" y="426" width="840" height="44"/>
      <text x="120" y="452" class="zf-life-name">Storage</text>
      <text x="120" y="466" class="zf-life-detail">your checkpoint backend · MemoryStorage · FileStorage · custom</text>
      <text x="920" y="452" text-anchor="end" class="zf-life-detail">storage_file.go · storage_mem.go</text>
    </g>
    <!-- ============ L1: WORKFLOW (passed-in, dashed border to mark "not owned") ============ -->
    <g>
      <rect class="zf-life-rect-end" x="100" y="496" width="840" height="44" stroke-dasharray="6 4"/>
      <text x="120" y="522" class="zf-life-name">Workflow</text>
      <text x="120" y="536" class="zf-life-detail">passed in, not owned · parsed from YAML or built in code</text>
      <text x="920" y="522" text-anchor="end" class="zf-life-detail">workflow.go</text>
    </g>
  </svg>
  <figcaption>
    Solid borders mark types the orchestrator <em>owns</em> (constructs or holds a reference to). The dashed border on <code>Workflow</code> marks the one type you pass <em>in</em> on each Run* call; it travels through the orchestrator but isn't part of its state.
  </figcaption>
</figure>

Public consumers rarely instantiate any of these directly. Tests construct an `Executor` or a bare `AgentRunner` for unit-level tests; production code stays inside the orchestrator surface.

## Lifecycle

```go
orch := zenflow.New(opts...)   // 1. construct (sync, no goroutines)
defer orch.Close()             // 4. drain (handle registry, cancel coord goroutines)

result, err := orch.RunFlow(ctx, wf)  // 2. run (spawns Executor + step runners)
// ... inspect result ...
result2, err := orch.RunFlow(ctx, wf2) // 3. run again, same orchestrator
```

`Close()` is idempotent. It drains every active `RunAgentAsync` handle (best-effort, bounded) and signals long-lived goroutines (the coordinator's wake loop, the factory cache cleanup) to exit. Once closed, new `Run*` calls return `ErrOrchestratorClosed`.

For one-shot CLI invocations, `defer orch.Close()` is enough. For long-lived embedders (HTTP servers, queue workers), call `Close()` during shutdown, and remember the orchestrator is concurrency-safe: many goroutines can call `RunFlow` on the same instance.

## Orchestrator vs Coordinator vs Executor

These three terms come up in every doc. They are **not** synonymous:

- **Orchestrator** is the public Go type you construct (`zenflow.Orchestrator`). It's the owner.
- **Coordinator** is an LLM-backed `AgentRunner` the orchestrator hosts when you pass `WithCoordinator(...)`. It's the messaging hub.
- **Executor** is a per-run internal struct that walks the DAG and spawns one `AgentRunner` per step. It's the scheduler.

See the [agent orchestration architecture diagram](/agent-orchestration.html) (external SVG) for the full picture, including the message flow between the three.

## Where to next

- [Coordinator](/concepts/coordinator) - what the LLM hub does, the three tools it carries, when to install one.
- [Execution Modes](/concepts/execution-modes) - when to choose `RunFlow` vs `RunGoal` vs `RunAgent`.
- [DAG Scheduling](/concepts/dag-scheduling) - what the Executor does inside a `RunFlow` call.
- [API: Core Functions](/api/core-functions) - the full signature reference for every orchestrator method.
