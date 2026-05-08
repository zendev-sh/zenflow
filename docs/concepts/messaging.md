---
title: Messaging
description: 'Step agents in zenflow can talk to each other, but never directly. Every message goes through the coordinator. This is hub-and-spoke routing:...'
---

# Messaging

Step agents in zenflow can talk to each other, but never directly. Every message goes through the coordinator. This is hub-and-spoke routing: agents are spokes, the coordinator is the hub.

This page describes the routing model. For the coordinator's role, see [Coordinator](/concepts/coordinator).

## Why hub-and-spoke

Three reasons:

1. **Auditability.** Every cross-agent message touches the coordinator, who can narrate, log, or veto it. There is one place to look for "what did agents tell each other".
2. **Schema-free coordination.** Agents do not need to know each other's interfaces. They send free-form text to the coordinator, who routes it to whoever needs it.
3. **No N-by-N permission matrix.** With direct peer-to-peer, every pair of agents would need a permission rule. Hub-and-spoke needs one rule: "can this step send to the coordinator? (yes)" plus "can the coordinator forward to this step? (yes if registered)".

There is no peer-to-peer messaging in zenflow. Step A cannot directly call `send_message(to=stepB, ...)`. The `send_message` tool sends to the coordinator, full stop. The coordinator decides whether to forward to step B via `forward_to_agent`.

## The two tools

| Tool | Caller | Effect |
|------|--------|--------|
| `send_message(text)` | Step agents | Pushes a `RouterMessage` into the coordinator's inbox. The coordinator wakes and processes it. |
| `forward_to_agent(target_step_id, text, kind?)` | Coordinator | Pushes a `RouterMessage` into a step's inbox. The step's next agent turn drains the inbox into its conversation context. |

`send_message` is auto-injected on every step runner that has a MessageRouter AND is not the coordinator itself (detection: presence of `forward_to_agent` in the runner's tool list marks the coordinator). `MessageRouter` is the public alias for the internal `router.Router`. Use `MessageRouter` in user code. Step runners that already have a `send_message` tool keep their own - no overwrite. `forward_to_agent` is one of the three default coordinator tools (alongside `narrate` and `finalize`).

There is no direct way for the coordinator to address a step that has not been registered with the MessageRouter. The MessageRouter rejects sends to unknown step IDs with `DropReasonUnknownStep` and emits an `EventMessageDropped` event.

## Message flow

A typical round trip:

<figure class="zf-diagram">
<svg viewBox="0 0 720 700" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Message round trip: step A's agent calls send_message; the MessageRouter pushes to coord inbox; coord mailbox appends a RouterMessage; wake signal drains inbox; coord LLM decides target is step B; coord calls forward_to_agent; the MessageRouter pushes to step B inbox; step B's mailbox appends; step B's next agent turn drains and injects into LLM context.">
  <!-- Connector line (single vertical spine) -->
  <line class="zf-seq-spine" x1="360" y1="55" x2="360" y2="660"/>

  <!-- Step 1: send_message -->
  <g>
    <rect class="zf-seq-node zf-seq-node-step" x="100" y="20" width="520" height="44"/>
    <text class="zf-seq-num" x="118" y="46">1</text>
    <text class="zf-seq-name" x="138" y="46">step A's agent calls <tspan class="zf-seq-mono">send_message("for B")</tspan></text>
  </g>

  <!-- arrow 1 -->
  <polygon points="0,-5 9,0 0,5" class="zf-seq-arrow-head" transform="translate(360,90) rotate(90)"/>
  <text class="zf-seq-edge" x="376" y="80">enqueue</text>

  <!-- Step 2: MessageRouter.Send -->
  <g>
    <rect class="zf-seq-node zf-seq-node-router" x="100" y="92" width="520" height="44"/>
    <text class="zf-seq-num" x="118" y="118">2</text>
    <text class="zf-seq-name" x="138" y="118">MessageRouter.Send → coord inbox</text>
  </g>

  <polygon points="0,-5 9,0 0,5" class="zf-seq-arrow-head" transform="translate(360,162) rotate(90)"/>

  <!-- Step 3: coord mailbox append -->
  <g>
    <rect class="zf-seq-node zf-seq-node-mailbox" x="100" y="164" width="520" height="62"/>
    <text class="zf-seq-num" x="118" y="190">3</text>
    <text class="zf-seq-name" x="138" y="190">coord mailbox appends</text>
    <text class="zf-seq-mono-line" x="138" y="210">RouterMessage{From: stepA, To: coord}</text>
  </g>

  <polygon points="0,-5 9,0 0,5" class="zf-seq-arrow-head" transform="translate(360,252) rotate(90)"/>
  <text class="zf-seq-edge" x="376" y="242">wake</text>

  <!-- Step 4: wake signal -->
  <g>
    <rect class="zf-seq-node zf-seq-node-coord" x="100" y="254" width="520" height="44"/>
    <text class="zf-seq-num" x="118" y="280">4</text>
    <text class="zf-seq-name" x="138" y="280">wake signal → runner loop drains inbox</text>
  </g>

  <polygon points="0,-5 9,0 0,5" class="zf-seq-arrow-head" transform="translate(360,324) rotate(90)"/>

  <!-- Step 5: LLM decides -->
  <g>
    <rect class="zf-seq-node zf-seq-node-coord" x="100" y="326" width="520" height="44"/>
    <text class="zf-seq-num" x="118" y="352">5</text>
    <text class="zf-seq-name" x="138" y="352">coord LLM decides: this is for B</text>
  </g>

  <polygon points="0,-5 9,0 0,5" class="zf-seq-arrow-head" transform="translate(360,396) rotate(90)"/>

  <!-- Step 6: forward_to_agent -->
  <g>
    <rect class="zf-seq-node zf-seq-node-coord" x="100" y="398" width="520" height="44"/>
    <text class="zf-seq-num" x="118" y="424">6</text>
    <text class="zf-seq-name" x="138" y="424"><tspan class="zf-seq-mono">forward_to_agent("stepB", "for B")</tspan></text>
  </g>

  <polygon points="0,-5 9,0 0,5" class="zf-seq-arrow-head" transform="translate(360,468) rotate(90)"/>

  <!-- Step 7: MessageRouter.Send to stepB -->
  <g>
    <rect class="zf-seq-node zf-seq-node-router" x="100" y="470" width="520" height="44"/>
    <text class="zf-seq-num" x="118" y="496">7</text>
    <text class="zf-seq-name" x="138" y="496">MessageRouter.Send → stepB inbox</text>
  </g>

  <polygon points="0,-5 9,0 0,5" class="zf-seq-arrow-head" transform="translate(360,540) rotate(90)"/>

  <!-- Step 8: stepB mailbox -->
  <g>
    <rect class="zf-seq-node zf-seq-node-mailbox" x="100" y="542" width="520" height="62"/>
    <text class="zf-seq-num" x="118" y="568">8</text>
    <text class="zf-seq-name" x="138" y="568">stepB mailbox appends</text>
    <text class="zf-seq-mono-line" x="138" y="588">RouterMessage{From: coord, To: stepB}</text>
  </g>

  <polygon points="0,-5 9,0 0,5" class="zf-seq-arrow-head" transform="translate(360,630) rotate(90)"/>
  <text class="zf-seq-edge" x="376" y="620">drain</text>

  <!-- Step 9: stepB next turn -->
  <g>
    <rect class="zf-seq-node zf-seq-node-step" x="100" y="632" width="520" height="44"/>
    <text class="zf-seq-num" x="118" y="658">9</text>
    <text class="zf-seq-name" x="138" y="658">stepB's next turn drains, injects into LLM context</text>
  </g>
</svg>
<figcaption>Reverse path (B answers): step B calls <code>send_message("answer")</code> -&gt; coord inbox -&gt; coord LLM decides to forward to A -&gt; <code>forward_to_agent("stepA", "answer")</code>.</figcaption>
</figure>

Reverse path (B answers): Step B calls `send_message("answer")` -> coordinator inbox -> coordinator decides to forward to A -> `forward_to_agent("stepA", "answer")`.

## Addressing rules

The `target_step_id` argument to `forward_to_agent` is the step's `id:` from the YAML, not the agent name. A step with `id: list_services` and `agent: discovery` is addressed as `forward_to_agent("list_services", ...)`, not `forward_to_agent("discovery", ...)`.

### Namespaced IDs in loops, forEach, and includes

When a step lives inside a loop, forEach, or included sub-workflow, its runtime ID is namespaced. The MessageRouter accepts either the bare step name (`worker`) or the namespaced runtime ID (`loop-stages.0.worker`); root router delegation routes both to the correct mailbox. Use whichever form the coordinator's prompt naturally produces.

| Container | Namespace pattern | Example |
|-----------|-------------------|---------|
| repeat-until iteration N | `parentLoopID.N.innerStepID` | `loop-stages.0.worker` |
| forEach iteration N | `parentLoopID[N].innerStepID` | `deploy[0].deploy_step` |
| include sub-workflow | `includeStepID.subStepID` | `deploy-staging.run-tests` |

The events the coordinator receives carry these namespaced IDs in their `from=` and `step=` fields. The coordinator's system prompt instructs it to mirror whatever it sees. Don't construct namespaced IDs by hand. Use the step IDs that arrive in events; the MessageRouter resolves both bare and namespaced forms.

### Why this is enforced

If the coordinator addresses a step that does not exist (`forward_to_agent("future-step")`), the MessageRouter cannot deliver. The drop is surfaced as `EventMessageDropped{reason: "unknown-step"}` and the tool returns `"dropped: unknown step. Available: [list of registered IDs]"`. The default coord prompt instructs it to recover in the same turn (retry with a correct ID, or call `narrate(...)` with the same content).

## Event types

The progress sink sees the routing as a sequence of events:

| Event | Fires on |
|-------|----------|
| `EventStepStart` | A step's agent began execution. The runner is now registered with the MessageRouter. |
| `EventStepEnd` | A step terminated successfully (status=completed). Its mailbox closes after this. Failed steps fire `EventError`; skipped steps fire `EventStepSkipped`. |
| `EventMessageSent` | A `send_message` or `forward_to_agent` call succeeded (queued). |
| `EventAgentInboxDrain` | A step agent drained one `RouterMessage` into its LLM context. |
| `EventCoordinatorInboxMessage` | The coordinator drained one `RouterMessage` from its mailbox. |
| `EventCoordinatorNarration` | The coordinator called `narrate`. |
| `EventCoordinatorMessage` | The coordinator pushed a targeted message via `forward_to_agent`. |
| `EventCoordinatorSynthesis` | The coordinator called `finalize`. |
| `EventMessageDropped` | A send was rejected. `Data["reason"]` is one of the `DropReason` strings. |

`EventMessageSent` is the outbound side; `EventAgentInboxDrain` and `EventCoordinatorInboxMessage` are the inbound side. They are emitted independently because the gap between send and drain can be large (e.g. a step is busy with an LLM turn when a forward arrives - the forward sits in the mailbox until the next turn).

## RouterMessage shape

```go
type RouterMessage struct {
    MessageID string
    From      string
    To        string
    Type      RouterMessageType
    Content   string
    // ... timestamps and other metadata
}
```

`From` is the sender's step ID (or `"coordinator"`). `To` is the recipient's step ID. `Type` is one of:

- `RouterMessageInfo` - general informational message (the default). Used by the coordinator's `forward_to_agent` to deliver informational text to a target step's mailbox.
- `RouterMessageCancel` - requests the receiving agent to stop.
- `RouterMessageContextUpdate` - injects new context into the agent's conversation. Used by the coordinator's `forward_to_agent` to push context updates to a running step.
- `RouterMessageResumeReply` - the reverse-routed reply produced after a resumed step finishes. Tagged distinctly so observers can distinguish resume responses from regular coordinator pushes; the drain logic treats it the same as `RouterMessageInfo` (appended as a user turn).

## Drop reasons

Every drop emits exactly one `EventMessageDropped`. The `DropReason` enum names every possible reason; see [Failure handling](/concepts/failure-handling) for the full table.

The two most common drops in messaging:

- `unknown-step` - the target step ID was never registered. Usually a coordinator addressing mistake (typo, namespace mismatch, future step).
- `target-terminal` - the target step's mailbox is closed because the step finished. Forwarded messages to a step that already terminated drop here. The resume mechanism can rescue some of these - see [Resume](/concepts/resume).

## Inbox draining

Step agents drain their inbox at the start of every LLM turn. The drain prepends the queued messages to the conversation as user-role content with a header naming the sender. The agent's next response can refer to the messages as if they were normal user input. The orchestrator caps each mailbox at `DefaultMaxMailboxSize` (10000) messages by default; pass `WithMaxMailboxSize(0)` to opt out of bounding.

The coordinator drains its inbox under wake-driven control: every push triggers a wake signal, the runner loop drains all pending messages and asks the LLM for a response. See [Coordinator](/concepts/coordinator) for the wake cycle details.

## Reverse-routing for resumed steps

When a step terminates and a later message arrives addressed to it, the MessageRouter can ask the executor to resume the step (via the resume mechanism). The resumed step's response routes back to the original sender via the coordinator's inbox as `EventCoordinatorInboxMessage`. The coordinator can surface the reply via `narrate`, forward it elsewhere, or ignore it.

The mechanism is described as API surface only - see [Resume](/concepts/resume).

## What messaging is not for

- **Bulk data passing.** Use step outputs (`content`, `result`) and `dependsOn`. The output injection path is automatic and respects truncation caps. Sending a 50-page document via `forward_to_agent` works but is wasteful.
- **Inter-process IPC.** Messaging is in-process only. The default `InMemoryMailboxStore` does not survive a process restart. For multi-process flows, plug a persistent `MailboxStore` via `WithMailboxStore`, but you still need an external coordinator to bridge processes.
- **Authentication or capabilities.** Anyone running in the workflow can send to the coordinator and the coordinator can forward to any registered step. Permission gates live at the tool level (`WithPermissions`), not the messaging layer.

## Cross-links

- [Coordinator](/concepts/coordinator) - the hub
- [Failure handling](/concepts/failure-handling) - drop reasons
- [Resume](/concepts/resume) - reverse-routing to terminated steps
- [Observability](/concepts/observability) - inspecting the message stream
