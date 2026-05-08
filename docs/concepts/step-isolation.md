---
title: Step Isolation
description: Steps in a workflow share the same process by default. They share the working directory, the environment, the file system, and any shared memory....
---

# Step Isolation

Steps in a workflow share the same process by default. They share the working directory, the environment, the file system, and any shared memory. For most flows that is fine. For flows where steps mutate state - write files, run shell commands, modify a git repo - concurrent execution under shared state is a recipe for races.

The `StepIsolation` interface lets you give each step its own work directory, scratch space, or sandbox.

## The interface

```go
type StepIsolation interface {
    Setup(ctx context.Context, runID, stepID string) (workDir string, err error)
    Cleanup(ctx context.Context, runID, stepID string) error
}
```

`Setup` is called before each step starts. It returns the work directory the step's tools should use. `Cleanup` is called after the step ends (regardless of status - completed, failed, skipped, cancelled). The two calls bracket the step's lifetime.

Install via `WithIsolation`:

```go
orch := zenflow.New(
    zenflow.WithModel(llm),
    zenflow.WithIsolation(myIsolator),
    // ...
)
```

A nil isolation (the default) means no setup, no cleanup, and steps run in the orchestrator's working directory.

## When isolation matters

You probably want isolation if any of these are true:

- **Steps fan out in parallel and write files.** Two parallel steps writing to the same `./build/` directory will clobber each other. Each needs its own working directory.
- **Steps modify a git repo.** Concurrent git operations on the same checkout race. A worktree-per-step approach gives each step its own checkout.
- **Steps run shell commands with state.** `cd`, env vars, file system mutation - anything stateful that the shell tool relies on.
- **Steps need to fail without polluting the workspace.** Cleanup runs even on failure, so a half-broken state from a failed step does not leak into successful steps.

You probably do not need isolation if:

- **Your steps are read-only.** Steps that only read the file system and produce text output do not race.
- **Your steps are sequential.** A workflow with no parallelism (every step has a single `dependsOn`) cannot race because only one step runs at a time.
- **Your steps share intentional state.** Some flows want all steps to mutate the same directory in sequence (e.g. an in-place refactor where step N+1 sees step N's changes). Isolation breaks that.

## Goroutine sharing

Steps in a single zenflow process share goroutines. Even with isolation, there is no operating-system process boundary between them. Isolation gives each step a separate **work directory** and lets you set up / tear down per-step state. It does not give each step a separate Go runtime, separate memory, or separate signal handlers.

For full process isolation (OS-level sandboxing, container per step), build that into your `StepIsolation` implementation: have `Setup` start a container or chroot, return the in-container work directory, and have `Cleanup` tear it down. The interface is intentionally minimal so it can wrap any isolation backend.

## Built-in: `NopIsolation`

The package ships a no-op implementation:

```go
type NopIsolation struct{}

func (n *NopIsolation) Setup(ctx context.Context, runID, stepID string) (string, error) {
    return "", nil
}

func (n *NopIsolation) Cleanup(ctx context.Context, runID, stepID string) error {
    return nil
}
```

It is equivalent to passing nothing. Useful for tests that want to assert the executor calls Setup / Cleanup without doing real work.

## Patterns

### Worktree per step

For git-heavy flows, have `Setup` create a fresh git worktree from the parent repo and `Cleanup` remove it:

```go
type WorktreeIsolation struct {
    BaseRepo string
    Branch   string
}

func (w *WorktreeIsolation) Setup(ctx context.Context, runID, stepID string) (string, error) {
    dir := filepath.Join(os.TempDir(), "zenflow", runID, stepID)
    cmd := exec.CommandContext(ctx, "git", "worktree", "add", dir, w.Branch)
    cmd.Dir = w.BaseRepo
    if err := cmd.Run(); err != nil {
        return "", err
    }
    return dir, nil
}

func (w *WorktreeIsolation) Cleanup(ctx context.Context, runID, stepID string) error {
    dir := filepath.Join(os.TempDir(), "zenflow", runID, stepID)
    cmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", dir)
    cmd.Dir = w.BaseRepo
    return cmd.Run()
}
```

Each step gets a clean worktree. Parallel steps write to separate paths. Cleanup removes the worktree even if the step failed.

### Tempdir per step

For non-git flows, a plain `os.MkdirTemp` per step works:

```go
func (t *TempdirIsolation) Setup(ctx context.Context, runID, stepID string) (string, error) {
    dir, err := os.MkdirTemp("", "zenflow-"+runID+"-"+stepID+"-*")
    return dir, err
}

func (t *TempdirIsolation) Cleanup(ctx context.Context, runID, stepID string) error {
    // Look up the dir by runID+stepID and os.RemoveAll it.
}
```

### Shared memory considerations

`SharedMemory` lives in process memory and is not affected by isolation - every step still reads and writes the same map. That is the point: isolation keeps file system state separate but lets steps coordinate via the shared memory key/value store. See [Shared memory](/concepts/shared-memory).

## Cross-links

- [DAG scheduling](/concepts/dag-scheduling) - parallelism rules that motivate isolation
- [Shared memory](/concepts/shared-memory) - cross-step state that isolation does not partition
- [Tools](/concepts/tools) - how isolated work directories are passed to tool implementations
- [API: Options](/api/options) - `WithIsolation` and the `StepIsolation` interface
