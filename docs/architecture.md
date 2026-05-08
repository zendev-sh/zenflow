---
title: Architecture
description: zenflow is a declarative multi-agent workflow engine for Go. A workflow is a YAML file, the engine is a DAG executor, and an LLM-driven...
---

# Architecture

zenflow is a declarative multi-agent workflow engine for Go. A workflow is a YAML file, the engine is a DAG executor, and an LLM-driven coordinator routes events between running steps through hub-and-spoke mailboxes. This page walks through the layers from top to bottom: the three-layer stack, the executor, the coordinator, the messaging substrate, and the lifecycle that ties them together.

## The three-layer stack

<figure class="zf-diagram">
<svg viewBox="0 0 880 320" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Three-layer stack: zenflow engine on top contains DAG executor, coordinator runner, MessageRouter + Mailbox, and the internal delivery engine. The middle layer is the goai SDK with GenerateText/StreamText and the provider.LanguageModel interface. The bottom layer is the LLM provider HTTP API.">
<!-- Layer 1: zenflow engine -->
  <g>
    <rect class="zf-stack-rect zf-stack-rect-engine" x="40" y="20" width="800" height="120" rx="8"/>
    <text class="zf-stack-label zf-stack-label-engine" x="60" y="44">zenflow engine</text>
    <text class="zf-stack-name" x="60" y="70">Orchestrator + DAG executor + coordinator + messaging</text>
    <text class="zf-stack-detail" x="74" y="92">▸ DAG executor          internal/exec/</text>
    <text class="zf-stack-detail" x="74" y="108">▸ Coordinator runner    internal/exec/coord_factory.go</text>
    <text class="zf-stack-detail" x="74" y="124">▸ MessageRouter + Mailbox  internal/router/</text>
    <text class="zf-stack-detail" x="430" y="92">▸ delivery engine        internal/router/</text>
    <text class="zf-stack-detail" x="430" y="108">▸ TranscriptStore       internal/resume/</text>
    <text class="zf-stack-detail" x="430" y="124">▸ Coord tool factories  internal/coord/</text>
  </g>

  <!-- Arrow 1 -> 2 -->
  <line class="zf-stack-arrow" x1="440" y1="140" x2="440" y2="166"/>
  <polygon points="0,-5 9,0 0,5" fill="var(--zf-muted-fg)" opacity="0.7" transform="translate(440,166) rotate(90)"/>

  <!-- Layer 2: goai SDK -->
  <g>
    <rect class="zf-stack-rect zf-stack-rect-sdk" x="40" y="166" width="800" height="80" rx="8"/>
    <text class="zf-stack-label zf-stack-label-sdk" x="60" y="190">goai sdk</text>
    <text class="zf-stack-name" x="60" y="216">GenerateText · StreamText · tool loop · 9 lifecycle hooks</text>
    <text class="zf-stack-detail" x="60" y="234">provider.LanguageModel interface (Google · Bedrock · Azure · ...)</text>
  </g>

  <!-- Arrow 2 -> 3 -->
  <line class="zf-stack-arrow" x1="440" y1="246" x2="440" y2="272"/>
  <polygon points="0,-5 9,0 0,5" fill="var(--zf-muted-fg)" opacity="0.7" transform="translate(440,272) rotate(90)"/>

  <!-- Layer 3: LLM provider -->
  <g>
    <rect class="zf-stack-rect zf-stack-rect-provider" x="40" y="272" width="800" height="36" rx="8"/>
    <text class="zf-stack-label zf-stack-label-provider" x="60" y="294">LLM provider · HTTP</text>
    <text class="zf-stack-detail" x="280" y="294">Gemini · Bedrock · Azure · OpenAI-compatible · ...</text>
  </g>
</svg>
<figcaption>zenflow does not own the LLM tool loop - that's <a href="https://goai.sh">goai</a>'s job. zenflow contributes the workflow shape, coordination, and delivery guarantees on top of any provider <a href="https://goai.sh">goai</a> supports.</figcaption>
</figure>

zenflow does not implement the LLM tool loop. That is [goai](https://goai.sh)'s job. zenflow contributes:

- **Workflow shape** - what to run, in what order, with what dependencies.
- **Coordination** - which agent gets which message at which moment.
- **Delivery guarantees** - every message is either delivered to a mailbox or dropped with a typed reason.

If [goai](https://goai.sh) supports a provider, zenflow runs on it. Verified providers include Google `gemini-3-pro-preview`, AWS Bedrock (`anthropic.claude-sonnet-4-6`, `minimax.minimax-m2.5`), and Azure (`DeepSeek-V3.2`, `claude-sonnet-4-6`, `gpt-5`, `gpt-5.3-codex`).

## Workflow as a DAG

A workflow is `Workflow{ Name, Agents, Includes, Steps, Options }`. Each `Step` has an ID, an agent reference, instructions, an optional `dependsOn` edge list, optional CEL `condition`, an optional `loop` block, and per-step model/tool overrides.

The executor builds a DAG in two passes:

1. **Validation.** Every `dependsOn` ID exists. Every agent reference resolves. Loops, conditions, and includes parse against the schema. CEL expressions in `condition` blocks compile against the available context.
2. **Topological scheduling.** Steps with all dependencies satisfied are ready. The scheduler picks ready steps up to `MaxConcurrency` (default 5) and runs each in its own goroutine. As a step finishes, its dependents become ready and are scheduled in turn.

The graph is the workflow author's contract with the runtime. The executor never invents a dependency edge, never re-orders steps, and never silently drops a step. A step that cannot run because of a `condition` evaluating false is marked `skipped` with the reason recorded; downstream `dependsOn` is satisfied as if the step had succeeded.

### Parallel fan-out

When two or more steps share the same set of completed dependencies, they are started in parallel:

```yaml
steps:
  - id: design
    agent: architect
    instructions: "..."

  - id: api-server
    dependsOn: [design]
  - id: database
    dependsOn: [design]
  - id: ui-components
    dependsOn: [design]

  - id: integrate
    dependsOn: [api-server, database, ui-components]
```

`api-server`, `database`, and `ui-components` start at the same moment. `integrate` waits for all three. There is no fan-out keyword - parallel execution is implied by the graph shape.

### Loops

Loops come in three flavours. All three are inner DAGs the executor expands at run time.

- `forEach` - iterate an array drawn from a previous step's structured output. Each iteration is its own sub-DAG. Iterations can run in parallel.
- **repeat-until** - run a sub-DAG until an `untilAgent` returns `done: true` or `until` CEL evaluates true (max bounded by `maxIterations`).
- `outputMode: cumulative` vs `last` controls whether downstream steps see every iteration's output or only the final one.

Each iteration's steps get namespaced IDs (`loop-stages.0.worker`, `loop-stages[1].worker`, `deploy[0].deploy_step`) so the coordinator can address them unambiguously. The same applies to `includes`: a sub-workflow's step IDs are prefixed by the include's parent step ID.

### Conditions

A step's `condition` is a CEL expression evaluated against the context produced by upstream steps. When false, the step is skipped (not failed). The CEL surface is intentionally small: previous step outputs, shared memory entries, and a few built-in helpers.

## The coordinator

The coordinator is itself an `AgentRunner` - the same primitive that drives every workflow step. What makes it special is the toolset and the wake loop.

### Default tools

`NewDefaultCoordRunner(llm)` returns a runner pre-wired with three tools:

- **`forward_to_agent(target_step_id, text, kind?)`** - route a message into a running step's mailbox. `kind="context_update"` injects context, `kind="cancel"` asks a step to stop, `kind="info"` (or omitted) is a general note.
- **`narrate(text)`** - emit a user-facing narration event. Does not route to any step.
- **`finalize(summary?)`** - signal that coordination is complete. The Run loop exits after this returns; the coordinator will not process more events.

`SynthesizeOnly()` drops `narrate` for the `--summary-only` CLI mode. `WithCoordTools(...)` appends caller-supplied tools (an SOP lookup, a human approval gate, a custom logger). `WithCoordSystemPromptSuffix(extra)` appends extra guidance to the tested baseline `DefaultCoordSystemPrompt` without forking it.

### Wake cycles

Step lifecycle events (start, end, error) and inter-step `send_message` traffic land in the coordinator's mailbox. After each push, the executor pings the coordinator's `Wake` channel; the coordinator's Run loop wakes, reads everything in its mailbox, calls the LLM with the accumulated events as context, executes any tool calls (forward, narrate, or finalize), then exits the inner LLM loop and waits for the next wake.

The default wake-cycle cap is 100 (vs 10 for step runners). Coord runners are long-lived across the whole workflow and absorb every step lifecycle event plus every bridged `send_message`, so a higher cap is necessary. Override via `WithCoordMaxWakeCycles(n)`.

When a `forward_to_agent` call drops (unknown step ID, mailbox full), the tool result tells the coordinator what went wrong and lists currently available step IDs. The system also preserves the dropped content as fallback narration so the user never loses LLM-generated text.

## MessageRouter, Mailbox, and the delivery engine

zenflow's messaging substrate is three layers:

- **MessageRouter** (public alias of `router.Router` in `internal/router/router.go`, re-exported via `router_facade.go`) - hub-and-spoke addressing. Maps step IDs (and namespaced loop/include IDs) to mailbox slots. Handles `RegisterStep`, `RegisterInbox`, `Send`, `Close`. Inner-DAG namespacing (`loop-stages.0.worker`) is resolved by MessageRouter delegation - the root MessageRouter holds a delegation entry pointing at the active iteration's nested router.
- **Mailbox** (`internal/router/mailbox.go`) - per-agent inbox queue. The default `InMemoryMailboxStore` is bounded by `WithMaxMailboxSize(n)` (zero = unbounded). Custom backends (sqlite, redis) plug in via `WithMailboxStore(factory)`.
- **Delivery engine (internal)** (`internal/router/delivery_engine.go`) - race-safe coupling of `Send` and `Wake`. When a message arrives, the engine appends it to the recipient's mailbox AND signals the recipient's wake channel in a single atomic step. There is no possible interleaving where a message lands but the recipient never wakes, or the recipient wakes but the mailbox is empty.

### Drop reasons

Every drop is typed. The complete list lives in `internal/router/router.go` as `DropReason` constants; the user-facing summary is:

| `DropReason` | When | Where to look |
| --- | --- | --- |
| `unknown-step` | Target step ID was never registered and has no pending senders. | Coordinator mistakenly addressed a non-existent step. |
| `mailbox-full` | Bounded in-memory mailbox at the `WithMaxMailboxSize` cap; the newest message is rejected (oldest-wins fairness). | Lower send rate, or raise `WithMaxMailboxSize`. |
| `mailbox-closed-by-finalize` | Mailbox raced with a concurrent close; the closed flag won. | Sender lost the race against `finalize`; treat as terminal. |
| `target-terminal` | Send to a step whose mailbox was closed because the target reached a terminal lifecycle state. | Coordinator addressed a step that already returned. |
| `workflow-cancelled` | Workflow context was cancelled or `abort` fired before the message reached the target's LLM context. | Inspect the cancel cause; the run is shutting down. |
| `max-wake-cycles` | Wake loop hit the `maxWakeCycles` cap with messages still pending; remainder drained as drops. | Bump `WithMaxWakeCycles` or `WithCoordMaxWakeCycles`. |
| `hold-timeout` | Executor's hold-timeout fired before the 3-invariant termination rule could converge; buffered messages are flushed and the step is force-terminated. | Sender is stuck; raise `WithHoldTimeout` or fix the sender. |
| `no-transcript` | Resume target's mailbox is closed AND the `TranscriptStore` has no saved transcript for the step. | Step ran before transcripts were persisted, or the transcript was deleted. |
| `transcript-too-large` | Saved transcript exceeds `WithMaxTranscriptMessages` / `WithMaxTranscriptBytes`; resume would exceed the size bound. | Inspect the persisted transcript. |
| `resume-shutdown` | Workflow context was cancelled mid-resume; the in-flight resume goroutine exited early. | Resume aborted by shutdown; retry on the next run. |
| `resolver-error` | Configured `ModelResolver` returned an error when resolving a saved-transcript model identifier. | Resolver infrastructure failure; check provider/model wiring. |

Every drop also fires `EventMessageDropped` on the progress sink, and (when set) `WithDropCallback` for metrics-only consumers.

### Hub-and-spoke, no peer-to-peer

A step agent has exactly one channel out: `send_message(text)`. This sends to the coordinator. The coordinator decides what to do with the message - usually `forward_to_agent` to another step, sometimes `narrate` for the user. Step agents never address each other directly. This is enforced by tool surface: there is no peer-to-peer send tool, and `forward_to_agent` is only registered on the coordinator runner.

The reason for the hub topology is auditability. Every inter-step message passes through the coordinator's LLM, which means you get a single place to log, reason about, intercept, or replay the conversation. The cost is an extra LLM call per forward; the benefit is that no agent can quietly poison another agent's context.

## Lifecycle

A typical run looks like this:

<figure class="zf-diagram">
<svg viewBox="0 0 880 580" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Lifecycle of a single workflow run: Orchestrator.New constructs the engine; RunFlow starts a topological schedule; the executor spawns parallel step goroutines and one long-lived coordinator goroutine; steps drive send_message and EventStepEnd back to the coord mailbox; the coord LLM drains the mailbox and forks into forward_to_agent / narrate / finalize; RunFlow returns a WorkflowResult; Close drains the handle registry.">
<!-- Vertical spine -->
  <!-- Stage 1: Orchestrator.New -->
  <g>
    <rect class="zf-life-rect-stage" x="60" y="20" width="320" height="48"/>
    <text class="zf-life-name zf-life-name-stage" x="76" y="42">Orchestrator.New(opts...)</text>
    <text class="zf-life-detail" x="76" y="60">WithModel · WithCoordinator · WithStorage · ...</text>
  </g>

  <line class="zf-life-arrow" x1="220" y1="68" x2="220" y2="92"/>
  <polygon points="0,-5 9,0 0,5" fill="var(--zf-muted-fg)" opacity="0.55" transform="translate(220,92) rotate(90)"/>

  <!-- Stage 2: RunFlow -->
  <g>
    <rect class="zf-life-rect-stage" x="60" y="92" width="320" height="48"/>
    <text class="zf-life-name zf-life-name-stage" x="76" y="114">RunFlow(ctx, wf)</text>
    <text class="zf-life-detail" x="76" y="132">one workflow run, returns WorkflowResult</text>
  </g>

  <line class="zf-life-arrow" x1="220" y1="140" x2="220" y2="164"/>
  <polygon points="0,-5 9,0 0,5" fill="var(--zf-muted-fg)" opacity="0.55" transform="translate(220,164) rotate(90)"/>

  <!-- Stage 3: Executor.Run + branches -->
  <g>
    <rect class="zf-life-rect-stage" x="60" y="164" width="320" height="48"/>
    <text class="zf-life-name zf-life-name-stage" x="76" y="186">Executor.Run</text>
    <text class="zf-life-detail" x="76" y="204">topological schedule, fans out into goroutines</text>
  </g>

  <!-- Two-way fork from Executor.Run -->
  <line class="zf-life-arrow" x1="160" y1="212" x2="160" y2="244"/>
  <polygon points="0,-5 9,0 0,5" fill="var(--zf-muted-fg)" opacity="0.55" transform="translate(160,244) rotate(90)"/>
  <line class="zf-life-arrow" x1="320" y1="212" x2="320" y2="244"/>
  <polygon points="0,-5 9,0 0,5" fill="var(--zf-muted-fg)" opacity="0.55" transform="translate(320,244) rotate(90)"/>

  <!-- Branch A: step goroutines -->
  <g>
    <rect class="zf-life-rect-step" x="40" y="244" width="240" height="170"/>
    <text class="zf-life-name zf-life-name-step" x="58" y="266">step goroutines</text>
    <text class="zf-life-detail" x="58" y="284">parallel up to MaxConcurrency</text>
    <text class="zf-life-detail" x="58" y="305">each step:</text>
    <text class="zf-life-detail" x="74" y="321">AgentRunner.Run</text>
    <text class="zf-life-detail" x="74" y="335">  → goai.GenerateText</text>
    <text class="zf-life-detail" x="74" y="349">  → tool loop → result</text>
    <text class="zf-life-detail" x="58" y="370">side effects:</text>
    <text class="zf-life-detail" x="74" y="386" fill="var(--zf-t-ok)">send_message → coord mbox</text>
    <text class="zf-life-detail" x="74" y="400" fill="var(--zf-t-ok)">EventStepEnd → coord mbox</text>
  </g>

  <!-- Branch B: coordinator goroutine -->
  <g>
    <rect class="zf-life-rect-coord" x="320" y="244" width="240" height="170"/>
    <text class="zf-life-name zf-life-name-coord" x="338" y="266">coordinator goroutine</text>
    <text class="zf-life-detail" x="338" y="284">one, long-lived</text>
    <text class="zf-life-detail" x="338" y="307">wake → drain mailbox → LLM</text>
    <text class="zf-life-detail" x="338" y="324">picks one tool per turn:</text>
    <text class="zf-life-detail" x="354" y="343" fill="var(--zf-t-flag)">▸ forward_to_agent → step mbox</text>
    <text class="zf-life-detail" x="354" y="362" fill="var(--zf-t-flag)">▸ narrate → ProgressSink</text>
    <text class="zf-life-detail" x="354" y="381" fill="var(--zf-t-flag)">▸ finalize → set terminal flag</text>
    <text class="zf-life-detail" x="338" y="402">drains remaining events, exits</text>
  </g>

  <!-- Cross-talk: step goroutines feed coord goroutine via dashed lines -->
  <path class="zf-life-side-arrow" d="M 280 386 C 310 386, 320 386, 340 386"/>
  <polygon points="0,-4 7,0 0,4" fill="var(--zf-t-ok)" opacity="0.6" transform="translate(340,386)"/>
  <path class="zf-life-side-arrow-coord" d="M 320 343 C 290 343, 280 343, 260 343"/>
  <polygon points="0,-4 7,0 0,4" fill="var(--zf-t-string)" opacity="0.6" transform="translate(260,343) rotate(180)"/>
  <text class="zf-life-edge-label" x="298" y="372" text-anchor="middle">events</text>
  <text class="zf-life-edge-label" x="298" y="332" text-anchor="middle">forward</text>

  <line class="zf-life-arrow" x1="220" y1="414" x2="220" y2="436"/>
  <polygon points="0,-5 9,0 0,5" fill="var(--zf-muted-fg)" opacity="0.55" transform="translate(220,436) rotate(90)"/>

  <!-- Stage 4: WorkflowResult returned -->
  <g>
    <rect class="zf-life-rect-stage" x="60" y="436" width="320" height="48"/>
    <text class="zf-life-name zf-life-name-stage" x="76" y="458">return WorkflowResult</text>
    <text class="zf-life-detail" x="76" y="476">{ Status, Summary, Steps[], Tokens, ... }</text>
  </g>

  <line class="zf-life-arrow" x1="220" y1="484" x2="220" y2="508"/>
  <polygon points="0,-5 9,0 0,5" fill="var(--zf-muted-fg)" opacity="0.55" transform="translate(220,508) rotate(90)"/>

  <!-- Stage 5: Close -->
  <g>
    <rect class="zf-life-rect-end" x="60" y="508" width="320" height="48"/>
    <text class="zf-life-name" x="76" y="530">Close()</text>
    <text class="zf-life-detail" x="76" y="548">cancels in-flight RunAgentAsync handles</text>
  </g>

  <!-- Right-side legend -->
  <g transform="translate(600, 30)">
    <text class="zf-life-detail" x="0" y="0" font-weight="700" fill="var(--zf-fg)">colour key</text>
    <rect width="14" height="14" x="0" y="14" rx="2" fill="var(--zf-bg)" stroke="var(--zf-t-prompt)" stroke-width="1.3"/>
    <text class="zf-life-detail" x="22" y="25">orchestrator stage</text>
    <rect width="14" height="14" x="0" y="36" rx="2" fill="var(--zf-bg)" stroke="var(--zf-t-ok)" stroke-width="1.3"/>
    <text class="zf-life-detail" x="22" y="47">step goroutine</text>
    <rect width="14" height="14" x="0" y="58" rx="2" fill="var(--zf-bg)" stroke="var(--zf-t-string)" stroke-width="1.3"/>
    <text class="zf-life-detail" x="22" y="69">coordinator goroutine</text>
    <rect width="14" height="14" x="0" y="80" rx="2" fill="var(--zf-bg)" stroke="var(--zf-t-flag)" stroke-width="1.3"/>
    <text class="zf-life-detail" x="22" y="91">tool dispatch</text>
  </g>

  <!-- Subtitle of right-side block -->
  <g transform="translate(600, 160)">
    <text class="zf-life-detail" x="0" y="0" font-weight="700" fill="var(--zf-fg)">two goroutine families</text>
    <text class="zf-life-detail" x="0" y="20">step goroutines run in parallel,</text>
    <text class="zf-life-detail" x="0" y="36">each driving one agent through</text>
    <text class="zf-life-detail" x="0" y="52">goai's tool loop.</text>
    <text class="zf-life-detail" x="0" y="80">the coordinator goroutine is</text>
    <text class="zf-life-detail" x="0" y="96">long-lived and single. it wakes</text>
    <text class="zf-life-detail" x="0" y="112">on every mailbox push and decides</text>
    <text class="zf-life-detail" x="0" y="128">whether to narrate, forward, or</text>
    <text class="zf-life-detail" x="0" y="144">finalize on the next LLM turn.</text>
  </g>
</svg>
<figcaption><code>Orchestrator.Close()</code> is idempotent and required for long-lived embedders. It cancels in-flight <code>RunAgentAsync</code> handles and rejects new <code>RunAgent</code> invocations with <code>ErrOrchestratorClosed</code>. Per-Run mailbox cleanup happens during <code>Executor.Run</code> unwind, not at <code>Orchestrator.Close</code>.</figcaption>
</figure>

`Orchestrator.Close()` is idempotent and required for long-lived embedders. It cancels in-flight `RunAgentAsync` handles and rejects new `RunAgent` invocations with `ErrOrchestratorClosed`. Per-Run mailbox cleanup happens during `Executor.Run` unwind, not at `Orchestrator.Close`. Examples in `zenflow/examples/` always `defer orch.Close()`.

### Event flow, end-to-end

<figure class="zf-diagram">
<svg viewBox="0 0 980 540" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Event flow end to end. Step A calls send_message; the MessageRouter pushes to the coord mailbox with Send+Wake. The coord LLM drains the mailbox and forks into one of three tools per turn: forward_to_agent which Send+Wake's a step B mailbox via the MessageRouter; narrate which writes EventNarration to the ProgressSink; or finalize which exits the Run loop. Step B's LLM picks up the forwarded message on its next wake cycle.">
<!-- ROW 1: step A -> MessageRouter -> coord mailbox -->
  <g>
    <rect class="zf-flow-rect-step" x="20" y="40" width="140" height="50"/>
    <text class="zf-flow-name zf-flow-name-step" x="42" y="62">step A</text>
    <text class="zf-flow-detail" x="34" y="80">user-defined</text>
  </g>

  <line class="zf-flow-arrow" x1="160" y1="65" x2="304" y2="65"/>
  <polygon points="0,-5 9,0 0,5" fill="var(--zf-muted-fg)" opacity="0.7" transform="translate(304,65)"/>
  <text class="zf-flow-edge-label" x="232" y="56" text-anchor="middle">send_message</text>

  <g>
    <rect class="zf-flow-rect-router" x="304" y="40" width="120" height="50"/>
    <text class="zf-flow-name" x="328" y="62">MessageRouter</text>
    <text class="zf-flow-detail" x="318" y="80">hub addressing</text>
  </g>

  <line class="zf-flow-arrow" x1="424" y1="65" x2="568" y2="65"/>
  <polygon points="0,-5 9,0 0,5" fill="var(--zf-muted-fg)" opacity="0.7" transform="translate(568,65)"/>
  <text class="zf-flow-edge-label" x="496" y="56" text-anchor="middle">Send + Wake</text>

  <g>
    <rect class="zf-flow-rect-mailbox" x="568" y="40" width="160" height="50"/>
    <text class="zf-flow-name zf-flow-name-mailbox" x="588" y="62">coord mailbox</text>
    <text class="zf-flow-detail" x="582" y="80">FIFO · typed drops</text>
  </g>

  <!-- coord mailbox down to coord LLM -->
  <line class="zf-flow-arrow" x1="648" y1="90" x2="648" y2="138"/>
  <polygon points="0,-5 9,0 0,5" fill="var(--zf-muted-fg)" opacity="0.7" transform="translate(648,138) rotate(90)"/>
  <text class="zf-flow-edge-label" x="664" y="118" text-anchor="start">wake</text>

  <!-- ROW 2: coord LLM -->
  <g>
    <rect class="zf-flow-rect-llm" x="548" y="138" width="200" height="56"/>
    <text class="zf-flow-name zf-flow-name-mailbox" x="572" y="160">coord LLM</text>
    <text class="zf-flow-detail" x="566" y="180">tool loop · 1 tool / turn</text>
  </g>

  <!-- 3-way fork from coord LLM -->
  <line class="zf-flow-arrow-tool" x1="648" y1="194" x2="220" y2="234"/>
  <line class="zf-flow-arrow-tool" x1="648" y1="194" x2="490" y2="234"/>
  <line class="zf-flow-arrow-tool" x1="648" y1="194" x2="800" y2="234"/>

  <!-- Tool labels -->
  <g>
    <rect class="zf-flow-rect-tool" x="120" y="232" width="200" height="36"/>
    <text class="zf-flow-name zf-flow-name-tool" x="156" y="256" text-anchor="start">▸ forward_to_agent</text>
  </g>
  <g>
    <rect class="zf-flow-rect-tool" x="390" y="232" width="200" height="36"/>
    <text class="zf-flow-name zf-flow-name-tool" x="450" y="256" text-anchor="start">▸ narrate</text>
  </g>
  <g>
    <rect class="zf-flow-rect-tool" x="700" y="232" width="200" height="36"/>
    <text class="zf-flow-name zf-flow-name-tool" x="772" y="256" text-anchor="start">▸ finalize</text>
  </g>

  <!-- Forward path: forward_to_agent -> MessageRouter -> step B mailbox -->
  <line class="zf-flow-arrow-tool" x1="220" y1="268" x2="220" y2="320"/>
  <polygon points="0,-5 9,0 0,5" fill="var(--zf-t-flag)" opacity="0.7" transform="translate(220,320) rotate(90)"/>

  <g>
    <rect class="zf-flow-rect-router" x="160" y="320" width="120" height="46"/>
    <text class="zf-flow-name" x="184" y="343">MessageRouter</text>
    <text class="zf-flow-detail" x="172" y="358">delegation map</text>
  </g>

  <line class="zf-flow-arrow-tool" x1="220" y1="366" x2="220" y2="416"/>
  <polygon points="0,-5 9,0 0,5" fill="var(--zf-t-flag)" opacity="0.7" transform="translate(220,416) rotate(90)"/>
  <text class="zf-flow-edge-label zf-flow-edge-label-tool" x="234" y="394" text-anchor="start">Send + Wake</text>

  <g>
    <rect class="zf-flow-rect-mailbox" x="120" y="416" width="200" height="48"/>
    <text class="zf-flow-name zf-flow-name-mailbox" x="146" y="438">step B mailbox</text>
    <text class="zf-flow-detail" x="142" y="456">per-step inbox</text>
  </g>

  <line class="zf-flow-arrow" x1="220" y1="464" x2="220" y2="490"/>
  <polygon points="0,-5 9,0 0,5" fill="var(--zf-muted-fg)" opacity="0.7" transform="translate(220,490) rotate(90)"/>

  <g>
    <rect class="zf-flow-rect-step" x="120" y="490" width="200" height="40"/>
    <text class="zf-flow-name zf-flow-name-step" x="148" y="510" text-anchor="start">step B LLM</text>
    <text class="zf-flow-detail" x="148" y="525" text-anchor="start">picks up on next wake</text>
  </g>

  <!-- Narrate path: narrate -> ProgressSink -->
  <line class="zf-flow-arrow-tool" x1="490" y1="268" x2="490" y2="320"/>
  <polygon points="0,-5 9,0 0,5" fill="var(--zf-t-flag)" opacity="0.7" transform="translate(490,320) rotate(90)"/>

  <g>
    <rect class="zf-flow-rect-sink" x="380" y="320" width="220" height="46"/>
    <text class="zf-flow-name" x="416" y="342" text-anchor="start">ProgressSink</text>
    <text class="zf-flow-detail" x="404" y="358" text-anchor="start">EventNarration · stdout / json</text>
  </g>

  <!-- Finalize path: finalize -> exit Run loop -->
  <line class="zf-flow-arrow-tool" x1="800" y1="268" x2="800" y2="320"/>
  <polygon points="0,-5 9,0 0,5" fill="var(--zf-t-flag)" opacity="0.7" transform="translate(800,320) rotate(90)"/>

  <g>
    <rect class="zf-flow-rect-sink" x="690" y="320" width="220" height="46"/>
    <text class="zf-flow-name" x="724" y="342" text-anchor="start">exit Run loop</text>
    <text class="zf-flow-detail" x="710" y="358" text-anchor="start">return WorkflowResult</text>
  </g>
</svg>
<figcaption>The MessageRouter is the only piece that touches both step mailboxes and the coord mailbox. Steps never see each other directly; every inter-step message must transit the coord, by design.</figcaption>
</figure>

The MessageRouter is the only piece that touches both step mailboxes and the coordinator mailbox. Steps never see each other.

## Why this design

A handful of design choices distinguish zenflow from peer multi-agent frameworks. They are deliberate tradeoffs.

**Hub-and-spoke topology.** Peer-to-peer messaging is the natural shape for free-form group chats; zenflow chose hub-and-spoke. The hub pays one extra LLM call per forward and in return you get a single audit point, a single place to enforce policy, and a single source of conversational truth. The tradeoff is less directness in the message graph.

**Mailbox + Wake delivery.** Inter-agent delivery uses an atomic `Send + Wake` pair: the message is appended to the recipient's mailbox AND the wake fires, or neither happens. Drops are typed via `DropReason`. The pair handles the corner cases that show up in concurrent message routing (racing senders, recipients mid-shutdown, panicking handlers) because the drop reason is part of the contract instead of an inferred state.

**Declarative YAML.** A YAML workflow is reviewable in a PR, diffable across versions, and runnable from any language that can shell out to a binary. Programmatic control flow gives you flexibility; the tradeoff is that every topology is bespoke and every change is a code change. Pick the shape that matches your team's review and deployment workflow.

**Provider abstraction via [goai](https://goai.sh) instead of bespoke clients.** zenflow does not know about Bedrock cross-region fallback, Azure deployment routing, Gemini multimodal parts, or OpenAI Responses-API streaming. [goai](https://goai.sh) does. zenflow's job is workflow orchestration; the LLM details belong one layer down.

**Single Orchestrator lifecycle.** Long-running embedders hold one `*Orchestrator` for the lifetime of the process and call `Close()` in shutdown. The orchestrator owns the handle registry, the factory cache, and any persistent stores. There is no global state.

## Capabilities present in code, not yet promoted

A few capabilities exist in the public API but are not part of the documented happy path. They are mentioned here so a code reader does not mistake them for missing pieces.

- **Resume from transcript.** `Orchestrator.ResumeFlow(ctx, runID, wf)` and `WithTranscriptStore(factory)` exist for cross-run resume. The default `InMemoryTranscriptStore` supports intra-run resume only; persistent stores plug in via the factory.
- **Per-step `timeout` and `retry`.** Fields exist in the `Step` struct. The execution semantics are stable enough to use but not yet covered by the canonical tutorials.
- **Multimodal input via `contextFiles`.** Image and PDF attachments work for multimodal models. See `spec/v1/examples/context-files.yaml` for the wire shape.

These will pick up dedicated documentation as they stabilise. The shipped surface in this release is the engine, the coordinator, the messaging substrate, and the YAML spec.

## Where the code lives

```
zenflow/
  doc.go                  Package zenflow doc
  interfaces.go           Storage/Tracer/StepIsolation/ApprovalHandler aliases (root facade)
  workflow.go             Workflow / Step / Run / StepResult / AgentConfig aliases (root facade)
  duration.go             Duration alias + FormatDuration / ParseDurationStrict re-exports
  router_facade.go        Re-exports for internal/router/ public API
  transcript_facade.go    Re-exports for internal/resume/ public API
  coord_facade.go         Re-exports for internal/coord/ tool factories
  agent_facade.go         Re-exports for internal/exec/ AgentRunner ecosystem
  orchestrator_facade.go  Re-exports for internal/exec/ Orchestrator + 49 With* + Executor + Storage backends + parsers + coord factory + JSON coordinator + ~60 utility symbols
  e2e_enabled_default.go  build !e2e
  e2e_enabled_e2e.go      build e2e
  internal/
    types/                Event, EventType, MessageKind, Output, ProgressSink, PermissionHandler/Request
    spec/                 Workflow / Step / Run / StepResult / AgentConfig / Duration types + Storage / Tracer / StepIsolation / ApprovalHandler / ModelResolver interface contracts
    router/               MessageRouter, MailboxStore, deliveryEngine
    resume/               TranscriptStore, InMemoryTranscriptStore
    coord/                RunnerHandle interface + 4 coord goai.Tool factories
    exec/                 AgentRunner + Executor + Orchestrator + JSON coordinator + RunFlow/RunGoal/RunAgent + ResumeFlow + 49 With* options + Storage backends + SharedMemory + parsers + validators + scheduler + CEL evaluator + portability lints + isolation default + lifecycle + prompt assembly
  observability/otel/     OpenTelemetry tracing exporters
  cmd/zenflow/            CLI entrypoint
  sink/                   Progress sinks (stdout, NDJSON)
  examples/               18 //go:build example mains
  spec/v1/
    schema.json           Workflow JSON Schema
    spec.md               Authoritative YAML specification
    examples/             19 reference workflows
```

There is no `adapter/goai/` package - the [goai](https://goai.sh) SDK is consumed directly by `internal/exec/executor.go` and `internal/exec/agent_runner.go` via `github.com/zendev-sh/goai` imports. The Orchestrator API is stable. The internal package layout under `internal/exec/` and the sibling internal packages is implementation detail and can change without notice.
