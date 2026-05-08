---
title: Shared Memory
description: SharedMemory is a namespaced key-value store that lives for the duration of a workflow run. Steps write to their own namespace, read from any...
---

# Shared Memory

`SharedMemory` is a namespaced key-value store that lives for the duration of a workflow run. Steps write to their own namespace, read from any namespace. It is the easiest way to share state across steps that do not have a `dependsOn` edge between them.

## When to use it

The main data-passing channel between steps is `dependsOn` plus per-step `content` and `result`. That covers most cases: step A produces output, step B reads `steps.A.content` or `steps.A.result` because B depends on A.

Use shared memory when none of those apply:

- **Cross-branch state.** Two parallel branches of the DAG (no `dependsOn` between them) need to coordinate. For example, a security audit branch wants to flag findings that an unrelated optimization branch should avoid.
- **Cumulative facts.** A step that runs many times (forEach, repeat-until) wants to accumulate findings into a single store, and a downstream step reads the whole store.
- **Coordinator-curated context.** The coordinator wants to record a fact that any future step can look up, regardless of `dependsOn`.

If two steps have a clean producer / consumer relationship, prefer `dependsOn` and step outputs. They are typed (`result` is `map[string]any`), validated (via `resultSchema`), and visible to CEL. Shared memory is unstructured - string-only values, agent-namespaced keys, no schema.

## API

```go
sm := zenflow.NewSharedMemory()

orch := zenflow.New(
    zenflow.WithModel(llm),
    zenflow.WithSharedMemory(sm),
    // ...
)

result, err := orch.RunFlow(ctx, wf)

// After the run, inspect the entries:
for k, v := range sm.Entries() {
    fmt.Printf("%s = %s\n", k, v)
}
```

`SharedMemory` exposes:

- `Write(agent, key, value string)` - store `value` at `"agent/key"`. Replaces any prior value.
- `Read(qualifiedKey string) (string, bool)` - read the fully-qualified key (`"agentName/key"`).
- `ListByAgent(agent string) map[string]string` - return entries for one agent, with the namespace prefix stripped.
- `Entries() map[string]string` - shallow copy of the full map (for persistence).
- `LoadEntries(map[string]string)` - bulk load (used during resume).
- `Summary() string` - markdown digest of all entries (`- agent/key: value`, value-truncated to 100 chars). Used as context-injection text.

The agent name is sanitised on `Write`: any `/` characters are replaced with `_` to prevent namespace collisions. Two agents named `team/a` and `team_a` would collide; the sanitiser ensures only `team_a` exists.

## Tool exposure

When `WithSharedMemory` is set, the executor auto-injects two tools into every step agent's tool catalogue:

- `shared_memory_write({key, value})` - writes under the calling agent's namespace.
- `shared_memory_read({key})` - reads from any namespace; the tool accepts a fully-qualified key.

Step agents call these like any other tool. The agent name is determined automatically from the step's agent assignment, so `shared_memory_write({key: "found", value: "x"})` from agent `auditor` writes to `auditor/found`.

The two tools are not exposed to the coordinator by default - coordinator's role is routing, not state. If you want the coordinator to mutate shared memory, append the tools manually via `WithCoordTools`.

## Lifecycle

A `SharedMemory` instance is per-run by default: pass a fresh `NewSharedMemory()` to the orchestrator, and entries from one `RunFlow` call do not leak into the next.

To share state across runs (e.g. a long-lived advisor that accumulates findings over many workflow invocations), keep the `SharedMemory` instance and re-use the orchestrator. Just remember the instance is in-process - restarting the binary loses the state.

## Persistence

For resumable runs, the storage backend persists the shared memory entries on each step completion. On `ResumeFlow`, the executor calls `LoadEntries` to restore them before re-executing failed / cancelled / skipped steps.

This works automatically with the default `MemoryStorage` (intra-process) and any custom `Storage` implementation. The default is process-local; for cross-process resume, plug a persistent storage backend.

## Worked example: cross-branch coordination

```yaml
agents:
  auditor:
    description: "Security auditor."
  optimizer:
    description: "Performance optimizer."
  reporter:
    description: "Final report writer."

steps:
  - id: scan
    agent: auditor
    instructions: |
      Audit the auth module. For each issue, call:
        shared_memory_write({key: "issue-<n>", value: "<description>"})

  - id: profile
    agent: optimizer
    instructions: |
      Profile the auth module. Before suggesting optimizations, call
        shared_memory_read({key: "auditor/issue-1"})
      to check whether the auditor found anything that overlaps.

  - id: report
    agent: reporter
    dependsOn: [scan, profile]
    instructions: |
      Read every entry under auditor/ and optimizer/ and write a
      combined report.
```

`scan` and `profile` run in parallel (no `dependsOn`). They coordinate via shared memory: the auditor writes findings, the optimizer reads them. `report` depends on both and synthesises.

## What it is not

- **A scratch buffer for the same step.** Use Go-level state (or skip it entirely) within a single step.
- **A typed store.** Values are strings. If you need structured data, JSON-encode it before writing and decode after reading. Or use `step.result` (which is structured) and `dependsOn`.
- **An IPC mechanism.** Like the rest of zenflow, shared memory is in-process only.

## Cross-links

- [DAG scheduling](/concepts/dag-scheduling) - dependsOn vs shared memory tradeoffs
- [Tools](/concepts/tools) - the auto-injected `shared_memory_*` tools
- [Resume](/concepts/resume) - how shared memory is restored on resume
- [API: Options](/api/options) - `WithSharedMemory`
