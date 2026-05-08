---
title: DAG Scheduling
description: Zenflow workflows are directed acyclic graphs. Nodes are steps, edges come from dependsOn. The scheduler walks the graph, runs ready steps in...
---

# DAG Scheduling

Zenflow workflows are directed acyclic graphs. Nodes are steps, edges come from `dependsOn`. The Executor (the per-run scheduler) walks the graph, runs ready steps in parallel up to `maxConcurrency`, and stops when every step has reached a terminal status.

This page covers the scheduling rules. For the full step grammar, see [YAML: Step](/yaml/step).

## Building the graph

A step's `dependsOn` array names other steps it requires before it can start. The graph is built from these edges at parse time. `ParseWorkflow` rejects any workflow with more than `MaxStepsPerWorkflow` (100) top-level steps, and rejects `Workflow.Description` or `Step.Instructions` longer than `MaxDescriptionChars` (2000) characters.

```yaml
steps:
  - id: design
    instructions: "Design the API."

  - id: api-server
    instructions: "Implement the server."
    dependsOn: [design]

  - id: database
    instructions: "Implement the database."
    dependsOn: [design]

  - id: integrate
    instructions: "Wire everything together."
    dependsOn: [api-server, database]
```

The graph this produces:

<figure class="zf-diagram">
<svg viewBox="0 0 720 200" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="DAG: design feeds two parallel steps (api-server and database); both then feed integrate.">
  <!-- Edges first (under nodes) -->
  <line class="zf-dag-edge" x1="180" y1="100" x2="320" y2="60"/>
  <polygon points="0,-5 9,0 0,5" class="zf-dag-arrow-head" transform="translate(320,60) rotate(-15)"/>
  <line class="zf-dag-edge" x1="180" y1="100" x2="320" y2="140"/>
  <polygon points="0,-5 9,0 0,5" class="zf-dag-arrow-head" transform="translate(320,140) rotate(15)"/>
  <line class="zf-dag-edge" x1="460" y1="60" x2="540" y2="100"/>
  <polygon points="0,-5 9,0 0,5" class="zf-dag-arrow-head" transform="translate(540,100) rotate(25)"/>
  <line class="zf-dag-edge" x1="460" y1="140" x2="540" y2="100"/>
  <polygon points="0,-5 9,0 0,5" class="zf-dag-arrow-head" transform="translate(540,100) rotate(-25)"/>

  <!-- Nodes -->
  <g>
    <rect class="zf-dag-node zf-dag-node-design" x="40" y="76" width="140" height="48"/>
    <text class="zf-dag-name" x="110" y="106" text-anchor="middle">design</text>
  </g>
  <g>
    <rect class="zf-dag-node zf-dag-node-step" x="320" y="36" width="140" height="48"/>
    <text class="zf-dag-name" x="390" y="66" text-anchor="middle">api-server</text>
  </g>
  <g>
    <rect class="zf-dag-node zf-dag-node-step" x="320" y="116" width="140" height="48"/>
    <text class="zf-dag-name" x="390" y="146" text-anchor="middle">database</text>
  </g>
  <g>
    <rect class="zf-dag-node zf-dag-node-merge" x="540" y="76" width="140" height="48"/>
    <text class="zf-dag-name" x="610" y="106" text-anchor="middle">integrate</text>
  </g>
</svg>
<figcaption>Both <code>api-server</code> and <code>database</code> declare <code>dependsOn: [design]</code>; they run in parallel. <code>integrate</code> declares both as dependencies and waits for the slower one.</figcaption>
</figure>

`api-server` and `database` share `design` as a dependency, so they fan out and run in parallel once `design` finishes. `integrate` waits for both.

## Topological scheduling

The default scheduler is `dependency-first` (set via `options.scheduler`). It works as follows:

1. Compute in-degrees from `dependsOn` edges.
2. Push every step with in-degree 0 onto a ready queue.
3. While the ready queue is non-empty and the running set is below `maxConcurrency`, pop the next step and run it.
4. When a step finishes (any terminal status), decrement the in-degree of every dependent. Dependents that reach 0 join the ready queue.
5. Stop when no step is running and the ready queue is empty.

Two corollaries:

- **Step array order has no semantic meaning.** Listing `database` before `api-server` does not change execution order; only `dependsOn` matters.
- **Parallel by default.** Steps without an edge between them may execute concurrently. The cap is `options.maxConcurrency` (workflow-level) and `WithMaxConcurrency` (orchestrator-level, default 5). Precedence: workflow YAML `options.maxConcurrency` wins if set (> 0); otherwise the orchestrator's `WithMaxConcurrency()` value is used; otherwise the default 5.

### Other schedulers

`options.scheduler` accepts:

- `dependency-first` (default) - depth-first preference for steps unblocking the most dependents.
- `round-robin` - rotate through ready steps to spread load across agents.
- `least-busy` - preferred when one agent is hot and another idle (mostly useful with shared mutable resources).

The choice rarely matters when `maxConcurrency` is high enough that everything ready runs at once; it matters when concurrency is constrained.

## Worked example: parallel-fan-out

```yaml
name: parallel-fan-out
agents:
  architect:
    description: "System architect."
    model: "claude-opus-4-6"
  backend:
    description: "Backend developer."
    model: "claude-sonnet-4-6"
    tools: [write, bash]
  frontend:
    description: "Frontend developer."
    model: "claude-sonnet-4-6"
    tools: [write]
  integrator:
    description: "Integration engineer."
    model: "claude-sonnet-4-6"
    tools: [write, bash]

steps:
  - id: design
    agent: architect
    instructions: "Design the REST API and data models."

  - id: api-server
    agent: backend
    instructions: "Implement the server based on the design."
    dependsOn: [design]

  - id: database
    agent: backend
    instructions: "Implement the database layer."
    dependsOn: [design]

  - id: ui-components
    agent: frontend
    instructions: "Build the React components."
    dependsOn: [design]

  - id: integrate
    agent: integrator
    instructions: "Wire everything and write integration tests."
    dependsOn: [api-server, database, ui-components]
```

Timeline (assuming `maxConcurrency: 5` and similar step durations):

```
t=0:   [design]
t=1:   design done -> [api-server, database, ui-components] start in parallel
t=2:   all three done -> [integrate] starts
t=3:   integrate done -> workflow ends
```

The fan-out from `design` to three sibling steps and the fan-in to `integrate` are both expressed by `dependsOn` only. There is no fan-out / fan-in primitive.

## Cycle detection

Cycles are caught at parse time, not at run time. Loading a workflow with a cycle returns an error before any LLM call:

```yaml
# This fails to load.
steps:
  - id: a
    dependsOn: [b]
  - id: b
    dependsOn: [a]
```

Each scope is checked independently: the top-level DAG, every `loop.steps` sub-DAG, and every included sub-workflow. A cycle in one scope does not contaminate the others (but you cannot load any workflow that contains a cycle).

Cross-scope cycles are also rejected: `dependsOn` cannot reference a step ID outside its own scope. An outer step cannot depend on an inner loop step, and vice versa.

## Completion criteria

A workflow finishes when no step is running and no step is ready. The terminal statuses are:

- `completed` - the step succeeded.
- `failed` - the step encountered an error (after retries exhausted).
- `skipped` - the step's `condition` evaluated to false, or a dependency failed under `skip-dependents` strategy.
- `cancelled` - the workflow aborted (cascading from a failed step under `cascade` or `abort` strategy).

Workflow status is derived from step statuses:

| All steps | Workflow status |
|-----------|-----------------|
| All `completed` | `StatusCompleted` |
| At least one `completed` and at least one `failed` | `StatusPartial` |
| No step `completed`, all `failed` / `skipped` / `cancelled` | `StatusFailed` |

See [Failure handling](/concepts/failure-handling) for how failures cascade.

## Concurrency caps

Two knobs throttle parallelism:

- `options.maxConcurrency` in the YAML - workflow-level cap.
- `WithMaxConcurrency(n)` on the orchestrator - process-level cap.

The effective cap follows a first-non-zero precedence: if the workflow YAML sets `options.maxConcurrency` (> 0), that value wins. Otherwise the orchestrator's `WithMaxConcurrency()` option is used. If neither is set, the default is 5. Setting the effective cap to `1` serialises the whole workflow regardless of structure.

For loops with `forEach`, `loop.maxConcurrency` is a third cap that applies only to that loop's iterations. See [Loops](/concepts/loops).

## Data passing

Each step's output is automatically injected into dependents' agent context. A step has two output channels:

| Channel | Type | Source |
|---------|------|--------|
| `content` | string | Concatenated text from all agent turns |
| `result` | `map[string]any` | Arguments from a successful `submit_result` call (only when the agent has `resultSchema`) |

Both are visible to dependents under `steps.<id>.content` and `steps.<id>.result` in CEL expressions. The text content is also injected into the dependent agent's prompt automatically - no template syntax is needed.

The injection format is implementation-defined; the executor truncates per-dependency content to a 16 KB cap and the overall prompt to 120 KB to keep within model limits. Steps that produce intentionally aggregated content (loops with `outputMode: cumulative`) bypass the per-dep cap; the overall cap still applies.

## Independence guarantees

Two steps with no `dependsOn` relationship may execute in any order, including in parallel. The runtime makes no ordering guarantee beyond what `dependsOn` declares. Any pair of independent steps that share state (file system, env, shared memory) needs explicit coordination - see [Step isolation](/concepts/step-isolation) and [Shared memory](/concepts/shared-memory).

## Cross-links

- [YAML: Step](/yaml/step) - field-level reference for steps and dependsOn
- [Failure handling](/concepts/failure-handling) - what happens when a step fails
- [Loops](/concepts/loops) - sub-DAG inside a single step
- [Composition](/concepts/composition) - sub-workflows via `include`
- [Conditions](/concepts/conditions) - skipping steps based on prior outputs
