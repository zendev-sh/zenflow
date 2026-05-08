---
title: Concepts
description: 'Mental model: orchestrator, coordinator, executor, agents, mailboxes, and how zenflow runs a workflow.'
---

# Concepts

This section explains how zenflow is put together. Start here if you want a mental model of the runtime: the Orchestrator that owns a process, the LLM coordinator that narrates and routes, the DAG scheduler that picks ready steps, and the agents and mailboxes that do the work. Each page is short and focused on one concept; follow the links from the [page map](#page-map) below in any order.

For the YAML surface, see [YAML Reference](/yaml/). For the Go API, see [Go API](/api/core-functions).

## Page map

### Architecture

- [Orchestrator](/concepts/orchestrator) - the process-wide Go type that owns model, sinks, storage, and lifecycle.
- [Execution Modes](/concepts/execution-modes) - the three entry points: `RunFlow`, `RunGoal`, `RunAgent`.
- [DAG Scheduling](/concepts/dag-scheduling) - how the executor walks `dependsOn`, picks ready steps, and bounds concurrency.
- [Coordinator](/concepts/coordinator) - the LLM that narrates progress, routes messages, and synthesizes results.
- [Messaging](/concepts/messaging) - hub-and-spoke routing through the coordinator's `forward_to_agent` tool.

### Lifecycle and failure

- [Failure Handling](/concepts/failure-handling) - retries, `onStepFailure` policies, and how errors propagate through the DAG.
- [Resume](/concepts/resume) - re-entering a workflow run from a checkpoint.
- [Observability](/concepts/observability) - the typed event stream, sinks, and OpenTelemetry tracing.

### Agents and tools

- [Agents](/concepts/agents) - named LLM configurations referenced from steps.
- [Tools](/concepts/tools) - functions the LLM can call to do real work.
- [Shared Memory](/concepts/shared-memory) - namespaced key-value store that lives for the run.
- [Step Isolation](/concepts/step-isolation) - what steps share by default and how to sandbox them.
- [Structured Output](/concepts/structured-output) - schema-bound results vs free-form text channels.

### Composition

- [Loops](/concepts/loops) - `forEach` and `repeat-until` sub-DAGs.
- [Conditions](/concepts/conditions) - CEL expressions that gate step execution.
- [Composition](/concepts/composition) - including sub-workflows via the `includes` registry.
