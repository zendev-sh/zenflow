---
title: Coordinator
description: The coordinator is an LLM that watches a workflow run, narrates progress, routes messages between steps, and produces a final synthesis. It is the...
---

# Coordinator

The coordinator is an LLM that watches a workflow run, narrates progress, routes messages between steps, and produces a final synthesis. It is the difference between a DAG executor and an agentic system.

The coordinator is optional. Without one, zenflow runs the DAG quietly: steps execute in dependency order, message-routing tools that target the coordinator drop, and the workflow ends with whatever step outputs accumulated. Most non-trivial flows benefit from a coordinator.

<Asciinema id="T6ghM70jlJEth4Ez" aria-label="zenflow flow full-featured.yaml --plan demo" />

The cast above is the coordinator at work on the full-featured workflow (6 top-level steps + a `deploy_staging` sub-workflow). Watch the `≋ [coordinator] ...` lines: every step start, every step completion, every sub-workflow boundary fires `narrate`, and the final summary lands once the deployer verifies the build.

## What the coordinator does

A coordinator runner has a mailbox. The executor pushes lifecycle events into that mailbox as `RouterMessage` envelopes:

- `EventStepStart` - a step's agent began.
- `EventStepEnd` - a step terminated (any status).
- `EventStepSkipped` - a step was skipped (failed dep or condition).
- `EventError` - a step or executor error fired.
- `EventCoordinatorInboxMessage` - a resumed step's reply (or other router-delivered message) landed in the coordinator inbox.
- Messages from `send_message` calls in step agents (forwarded to the coordinator inbox).

After each push, the executor signals the coordinator's `Wake` channel. The runner's tool loop drains the new mailbox messages, asks the LLM what to do, and executes any tool calls the LLM emitted.

The coordinator's three default tools are:

- `forward_to_agent(target_step_id, text, kind?)` - inject a message into a running step's mailbox. The step agent sees the message in its conversation context on its next turn.
- `narrate(text)` - emit a user-facing narration. Surfaces in the CLI output and JSON sink as an `EventCoordinatorNarration`.
- `finalize(summary?)` - signal coordination is complete. The workflow's `WorkflowResult.Summary` is set to the summary string. `finalize` flips the runner's `finalized` flag and closes the `Done` channel; the CLI's coord loop continues until the workflow's DAG completion cancels its context (the CLI does NOT consult `runner.Finalized()` to exit). Embedders building custom coord loops can choose to honor `runner.Finalized()` as an exit signal - see [coord-tools.md](/api/coord-tools) for the canonical loop shape.

A coordinator that never calls `finalize` is fine: when the executor finishes the DAG, it cancels the runner's context and the loop exits naturally. `finalize` is a hint for "I have nothing more to say"; it is not required.

## Wake cycles

The coordinator runner's `Run` method is a wake-driven loop:

1. Drain the mailbox into the conversation history.
2. Ask the LLM for a response.
3. Execute any tool calls (`forward_to_agent`, `narrate`, `finalize`, plus any extras you wired in).
4. If the loop is in mailbox-mode and `Wake` has not been signalled, exit. Wait for the next signal.
5. If `Wake` was signalled, loop back to step 1.

The cap on wake re-entries per `Run` invocation is 100 wake cycles by default; raise via `WithCoordMaxWakeCycles(n)`. When hit, remaining mailbox messages are drained as drops with `DropReasonMaxWakeCycles`.

This loop is what lets the coordinator be both reactive (it wakes per event) and stateless across waits (no goroutine pinned to it when the mailbox is empty).

## Cold start vs continuation

The coordinator's system prompt is the same on every invocation. What changes is the mailbox content and the conversation history.

- **Cold start**: the first wake of a fresh run. The mailbox starts empty (or holds an initial `workflow_start` event when `WithFlowContext` was supplied). The coordinator narrates whatever the first event tells it about, or stays silent.
- **Continuation**: any subsequent wake. The conversation history carries every prior turn. The coordinator's job is to answer the new event without repeating itself.

The `DefaultCoordSystemPrompt` is tuned for the continuation case: "narrate ONCE per significant event", "do NOT repeat narration you already emitted", "exit naturally when nothing new is happening". The cold-start case usually lands one short narration like `"<agent> started <step-id>"` and exits.

## Installing a coordinator

Two paths.

### CLI default

The CLI binary installs a default coordinator automatically for `zenflow flow` and `zenflow goal`. You see narration in the output, a final summary after the run, and (in `--json` mode) `coordinator_narration` and `coordinator_synthesis` events.

`--quiet` suppresses narration but keeps the runner installed. `--summary-only` switches the runner to `SynthesizeOnly()` mode (no `narrate` tool, just `finalize` with a summary). See [CLI flags](/cli/flags).

### Library: NewDefaultCoordRunner

```go
import (
    "context"
    "github.com/zendev-sh/zenflow"
)

coord := zenflow.NewDefaultCoordRunner(llm)
orch := zenflow.New(
    zenflow.WithModel(llm),
    zenflow.WithCoordinator(coord),
    zenflow.WithProgress(progressSink),
)

// Caller owns the runner's lifecycle.
ctx, cancel := context.WithCancel(parentCtx)
go func() {
    _, _ = coord.Run(ctx, zenflow.AgentConfig{}, "", "", coord.Tools)
}()
defer cancel()

result, err := orch.RunFlow(ctx, wf)
```

`NewDefaultCoordRunner` returns a pre-configured `*AgentRunner`:

- `StepID = "coordinator"` (matches the executor's reverse-reply inbox key).
- A fresh `InMemoryMailboxStore`.
- A buffered `Wake` channel for executor-driven re-entry.
- The default coord system prompt (`DefaultCoordSystemPrompt`).
- The three default tools wired to the runner instance.
- `MaxWakeCycles = 100`.

The factory does **not** start the runner's `Run` loop. Lifecycle is the caller's job: CLI consumers start the loop on a goroutine before `RunFlow` and let context cancellation tear it down; embedded consumers reuse their existing primary `AgentRunner` and pass it directly to `WithCoordinator(primary)`. The orchestrator wires the runner's `MessageRouter` and `Progress` synchronously when `New()` returns, so the consumer can spawn the coord goroutine immediately afterward without racing the wiring.

### Coord options

```go
coord := zenflow.NewDefaultCoordRunner(
    llm,
    zenflow.SynthesizeOnly(),                        // drops `narrate`, only finalize fires
    zenflow.WithCoordTools(myCustomTool),            // append extra tools (cumulative across calls)
    zenflow.WithCoordMaxWakeCycles(250),             // raise the wake cap
    zenflow.WithCoordSystemPrompt(myCustomPrompt),   // replace the prompt entirely
    zenflow.WithCoordSystemPromptSuffix("\nExtra:..."),  // append to the default
    zenflow.WithCoordContextProvider(func() string { // ambient context refreshed every wake
        return ambientSnapshotForCoord()
    }),
)
```

`WithCoordSystemPromptSuffix` is the right tool for "keep the tested baseline, add a few project-specific lines". Replace the prompt entirely only if you have a strong reason - the default contains addressing rules that prevent unknown-step drops.

`WithCoordContextProvider` is the per-wake context hook. The callback fires once before the first LLM call and once on every wake-driven re-entry; its returned string is appended as a fresh user-role message wrapped in `<dynamic-context>...</dynamic-context>` so the coord LLM can distinguish ambient state from in-band conversation. Empty / whitespace-only returns are skipped. Use it for chat-driven UX that needs ambient context refreshed each wake (currently-open files, repo metadata, session topic) without re-engineering the system prompt.

## Worked example: messaging-demo

```yaml
name: messaging-rounds
agents:
  asker:
    description: "Curious user. Sends questions via send_message; reads answers from inbox."
  expert:
    description: "Knowledgeable expert. Reads questions; sends answers via send_message."
  summarizer:
    description: "Summarizes the Q/A history."

steps:
  - id: asker-1
    agent: asker
    instructions: |
      Round 1. Call send_message with "QUESTION_1: What is the capital of France?"
      Then reply with EXACTLY: "SENT_1".

  - id: expert-1
    agent: expert
    dependsOn: [asker-1]
    instructions: |
      Read the forwarded question from your inbox.
      Call send_message with "ANSWER_1: <answer>"
      Then reply with EXACTLY: "REPLIED_1".

  # ... rounds 2 and 3 follow the same pattern, each depending on the previous expert.

  - id: summary
    agent: summarizer
    dependsOn: [expert-3]
    instructions: |
      Read the full conversation. Write a 3-sentence summary.
```

What the coordinator does, round by round:

1. **`asker-1` starts.** Coordinator wakes on `EventStepStart`. It narrates `"asker started round 1"` (or stays silent - either is fine).
2. **`asker-1` calls `send_message`.** The send goes to the coordinator's inbox. The coordinator wakes, sees `from=asker-1: QUESTION_1: ...`, decides this needs to reach `expert-1`. It calls `forward_to_agent(target_step_id="expert-1", text="QUESTION_1: ...")`. The MessageRouter pushes the message into `expert-1`'s mailbox.
3. **`asker-1` finishes** with content `"SENT_1"`. Coordinator wakes on `EventStepEnd`, may narrate one sentence.
4. **`expert-1` starts.** Its agent's first turn drains the inbox, sees the question, calls `send_message` with the answer.
5. **Coordinator forwards the answer to `asker-2`** (the next round's step) once it starts. And so on.
6. **`summary` finishes**, coordinator narrates a final line and may call `finalize(summary="3 rounds of Q&A on Paris, population, landmarks")`. The workflow ends.

Two key facts in this workflow:

- `asker` and `expert` never see each other directly. They only see the coordinator, who curates what reaches whose mailbox.
- The `dependsOn` chain (`asker-1 -> expert-1 -> asker-2 -> ...`) gives the coordinator time to forward each message before the next pair starts.

This is hub-and-spoke routing, enforced by the system. See [Messaging](/concepts/messaging) for the full model.

## Addressing rules

The coordinator's system prompt enforces step-ID-based addressing:

- `target_step_id` is the step's `id:` from the YAML, not the agent name.
- Inner-DAG steps (loops, forEach, includes) use namespaced IDs:
  - repeat-until iter N: `parentLoopID.N.innerStepID`
  - forEach item N: `parentLoopID[N].innerStepID`
  - include sub-workflow: `includeStepID.subStepID`
- Only address step IDs that have appeared in mailbox events. Inferring future step IDs from domain semantics is a frequent source of unknown-step drops.

The coordinator sees the right ID in every event payload (`from=` / `step=` fields). Mirror what you see.

## Failure recovery

When `forward_to_agent` returns `"dropped: ..."`, the tool result lists currently available step IDs. The coord's prompt instructs it to take one recovery action in the same turn:

1. Retry `forward_to_agent` with a correct target ID.
2. Call `narrate(...)` with the same content to surface it.

The system also preserves dropped content as fallback narration automatically, so user-facing output is not lost. Acting in the same turn produces cleaner output.

## Termination

`finalize` is one-way. Once called, the coordinator runner stops processing events for the rest of the run. The default prompt cautions against premature `finalize`: the rule is "all declared steps have emitted `EventStepEnd` and no step is still pending in the mailbox".

Without `finalize`, the runner exits when the executor cancels its context (workflow done). The summary in `WorkflowResult.Summary` will be empty in that case; if you need a synthesis, install a coordinator and rely on `finalize`.

## Cross-links

- [Messaging](/concepts/messaging) - the routing model the coordinator participates in
- [Observability](/concepts/observability) - sinks that surface narration and synthesis events
- [API: Options](/api/options) - `WithCoordinator`, `NewDefaultCoordRunner`, `CoordOption`
- [Failure handling](/concepts/failure-handling) - how the coord sees step failures
