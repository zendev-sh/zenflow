---
title: Compare
description: zenflow takes a narrower position than the most popular multi-agent frameworks. This page is an honest side-by-side - not a "zenflow wins...
---

# Compare

zenflow takes a narrower position than the most popular multi-agent frameworks. This page is an honest side-by-side - not a "zenflow wins everything" pitch. Use it to figure out which tool fits your problem.

## At a glance

| | zenflow | CrewAI | AutoGen | LangGraph | open-multi-agent | langchaingo |
| --- | --- | --- | --- | --- | --- | --- |
| **Programming model** | Declarative YAML DAG | Python `Crew` / `Task` classes | Python conversation classes | Python state-graph DSL | TypeScript `runTeam(goal)` / `runTasks(dag)` | Go imperative chains |
| **Messaging shape** | Hub-and-spoke (coordinator-mediated) | Sequential / hierarchical task delegation | Free-form group chat (peer-to-peer) | Edges + state mutations | Coordinator decomposes goal into task DAG; `delegate_to_agent` for sync handoff | Caller-managed |
| **Delivery semantics** | Mailbox + Wake with typed drop reasons | Task-output hand-off (sequential pipeline) | Group-chat message append | Graph-step transition | Task retry with backoff, loop detection, context compaction | Caller-wired |
| **Provider abstraction** | Any provider [goai](https://goai.sh) supports | Mostly OpenAI + a few wrappers | Mostly OpenAI + Anthropic | Mostly OpenAI; LangChain ecosystem | 10 native (Anthropic / OpenAI / Azure / Bedrock / Gemini / Grok / DeepSeek / MiniMax / Qiniu / Copilot) + OpenAI-compat for the rest | LangChain Go subset |
| **Concurrency model** | DAG-scheduled goroutines, race detector clean | Sequential tasks, no native parallelism | Sequential conversation turns | Graph step at a time | Auto-parallelize independent tasks in the DAG | Caller-managed |
| **Test surface** | Mailbox + Wake assertions, typed drops, no LLM needed for unit tests | Crew run integration tests (LLM required) | Group chat integration tests (LLM required) | Graph-step unit tests possible | `onTrace` span assertions, structured-output Zod schemas | Caller-managed |
| **Languages** | Go (CLI + library) | Python | Python | Python (TS port) | TypeScript / Node.js >= 18 | Go |
| **Deployment** | Single static binary | Python interpreter + deps | Python interpreter + deps | Python interpreter + deps | Node.js + 3 runtime deps | Go binary + deps |
| **Observability** | Built-in OTel tracer, NDJSON event stream, ProgressSink | Custom callbacks | Custom callbacks | LangSmith integration | `onProgress` events + `onTrace` spans + post-run HTML dashboard | Caller-managed |

## Where each tool shines

**CrewAI.** Best for a "team of role-played agents" pattern where you want a clean Python class for each role and you do not need fine-grained control over message routing. The mental model is approachable: `Crew(agents=[...], tasks=[...]).kickoff()`. The surface is intentionally compact: bounded delivery semantics, persistent transcripts, and explicit parallel fan-out are layers you would add yourself or pick up from a different tool.

**AutoGen.** Strong fit for free-form research conversations where the value is in agents talking to each other organically. The group-chat shape is genuinely novel and works well for ideation. Determinism is a different shape: if you need reproducible step ordering, you typically pair it with an external scheduler or reach for a graph-based runtime.

**LangGraph.** The closest peer to zenflow in that it is a graph-shaped runtime, not a conversation-shaped one. LangGraph is more flexible (state mutations, conditional edges, retries are first-class), with a correspondingly larger API surface. If you are already inside the LangChain ecosystem and want graph semantics, LangGraph is the obvious move. If you want a workflow you can review in a PR without learning a Python DSL, zenflow is closer.

**open-multi-agent (OMA).** TypeScript-native, goal-driven. You hand it a goal string and a team; a coordinator agent decomposes the goal into a task DAG and parallelizes the independent branches automatically. Sweet spot is "describe the goal, not the graph" - useful when the workflow shape is not known up front and you want the runtime to figure it out. zenflow is the inverse: the workflow is fixed at edit time and reviewable in a PR. Pick OMA if you want a TypeScript backend that turns vague goals into orchestrated runs; pick zenflow if you want the graph pinned in source.

**langchaingo.** Direct LangChain port to Go, focused on chains and retrieval. Not really a multi-agent framework - it stops one layer below where zenflow starts. If you need a single Go agent with retrieval, langchaingo plus your own loop is reasonable. If you need multiple agents coordinating, zenflow handles the coordination and uses [goai](https://goai.sh) for the per-agent LLM calls.

## When zenflow is the right tool

Pick zenflow when:

- The workflow is **fixed at edit time** - reviewable in a PR, versionable, runnable from a CI pipeline. The YAML is the source of truth, not a script that builds objects at runtime.
- You need **bounded delivery semantics**. Every inter-agent message either lands in a mailbox or drops with a typed reason. There is no "did the message arrive?" ambiguity.
- You are **embedding in a Go service** - a queue worker, an HTTP server, a long-running job runner. zenflow is one Go module, no Python interpreter, no virtualenv.
- You want **provider neutrality**. Workflows do not know about Gemini vs Bedrock vs Azure. The provider is a `WithModel(...)` choice.
- You care about **test ergonomics**. You can unit-test a workflow's coordination logic against a mocked `provider.LanguageModel` without paying for real LLM calls until the integration tier.

## When zenflow is the wrong tool

Be honest with yourself. Pick something else when:

- You want **agents to negotiate freely**. zenflow's hub-and-spoke topology is deliberately constrained. If you want six agents in a free-form group chat with emergent dynamics, AutoGen is built for that and zenflow is not.
- You want a **Python-first stack**. zenflow has no Python bindings; the CLI works from any language but the embedding API is Go only. CrewAI and LangGraph are first-class Python.
- You want **runtime-mutable graphs** where new edges and nodes appear during execution. zenflow validates the graph at load time and runs it. LangGraph and AutoGen are more flexible here.
- Your workflow is a **single agent with tools**. zenflow runs that case (`zenflow agent`), but if you do not need coordination there is no benefit over [goai](https://goai.sh) directly or any other Go agent library.

## Side-by-side on a concrete scenario

To make the trade-offs concrete, here is the same workflow expressed in each tool's natural shape: a researcher and a writer running in parallel, exchanging mid-flight context, with a final editor.

### zenflow

The whole workflow is a YAML file plus one Go binary. The graph is reviewable, the messaging substrate is built in, and the editor's prompt automatically receives both upstream outputs.

```yaml
name: research-team
agents:
  researcher: { description: "..." }
  writer:     { description: "..." }
  editor:     { description: "..." }

steps:
  - id: research
    agent: researcher
    instructions: "..."
  - id: draft
    agent: writer
    instructions: "..."
  - id: polish
    agent: editor
    dependsOn: [research, draft]
```

A coordinator handles inter-step messaging via `WithCoordinator(NewDefaultCoordRunner(llm))`. Drops are typed; persistence plugs in via `WithMailboxStore` and `WithTranscriptStore`.

### CrewAI

A Python `Crew` with a list of `Agent` and a list of `Task`. Sequential by default; hierarchical mode delegates through a manager agent. Inter-task context is passed through task outputs, not a messaging substrate. Parallelism requires manual `asyncio` orchestration outside the Crew.

### AutoGen

A `GroupChat` with an `Agent` per role. Messages flow peer-to-peer with an optional speaker selector. Excellent for free-form research; deterministic behaviour requires custom selector logic.

### LangGraph

A `StateGraph` with nodes and conditional edges. State mutations carry context between nodes. Strong fit for research workflows with branching control flow; the trade is a Python DSL you have to learn.

### langchaingo

A Go `chains.Chain` per agent. Coordination is whatever your code does. You own the goroutines, the channels, the cancellation, the retry logic. Effective for one-off integrations; not a multi-agent framework on its own.

## A note on "production-readiness"

Every framework on this list claims production-readiness, and each is honest within its design. The shapes are different, so the operational characteristics under load are different too. zenflow's narrower scope is a deliberate trade: we chose to do fewer things and give them stronger guarantees (typed drops, race-safe delivery, an audit point in the coordinator) so the failure modes you hit at 3am are the failure modes documented in `DropReason`.

If your workflow fits the YAML-DAG-with-coordinator shape, zenflow will be predictable. If your workflow does not fit that shape, force-fitting it would be worse than picking a tool with a different default. That is a feature, not an apology.

## See also

- [Architecture](./architecture.md) - the executor, coordinator, and messaging internals.
- [Examples](./examples.md) - 19 worked examples covering every primitive.
- [YAML reference](/yaml/) - the full spec.
