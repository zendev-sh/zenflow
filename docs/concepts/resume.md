---
title: Resume
description: 'Zenflow can re-enter a step that has already terminated. Two distinct paths exist:'
---

# Resume

Zenflow can re-enter a step that has already terminated. Two distinct paths exist:

1. **Workflow resume** via `Orchestrator.ResumeFlow` - restart a whole flow from its last checkpoint, re-running failed / cancelled / skipped steps and reusing completed step results.
2. **Step resume** via the router's transcript mechanism - when a message arrives addressed to a step whose mailbox is closed, the router can ask the executor to spawn a fresh agent loop with the saved transcript and the new message appended.

::: warning Active verification - not production-grade
The transcript-based step resume mechanism is in active verification. The API surface is stable and the happy path works in tests, but operator-grade properties (cross-process resume with persistent transcript stores, large-transcript truncation behaviour, model-resolver edge cases under provider rotation, behaviour under race conditions between resume and concurrent finalize) are still being validated. Do not rely on transcript-based resume for production workflows yet. The `ResumeFlow` (workflow-level) path is older and more mature, but the persistent-storage backends behind it carry the same caveat as any state-restoring system: validate end-to-end before depending on it.
:::

## Workflow resume

```go
result, err := orch.ResumeFlow(ctx, runID, wf)
```

Behaviour:

1. The orchestrator loads the previous run from `Storage` to validate it exists.
2. For each step in the workflow, it asks `Storage` for the saved `StepResult`. Steps with status `completed` are loaded into the resume map and never re-executed. Steps with status `failed`, `cancelled`, or `skipped` are absent from the resume map and will be re-run.
3. Shared memory is restored: the orchestrator's `SharedMemory` (or a fresh one) is overlaid with the persisted entries.
4. The executor runs the DAG, but skips any step whose ID appears in the resume map.

The original run's `runID` is reused. Events emitted during resume carry that ID, so a downstream observer sees the resume as a continuation of the same logical run.

Requirements:

- `WithStorage(s)` must have been set when the original run started. The default `MemoryStorage` is in-process - meaningful resume across processes requires a persistent backend (file system, SQLite, etc).
- The workflow definition passed to `ResumeFlow` should match the one used in the original run. Adding new steps is allowed; removing or renaming steps that were completed is not (the resume map will contain dangling IDs).

::: warning Concurrent ResumeFlow on the same `runID` is unsafe
zenflow does not guard against two simultaneous `ResumeFlow` calls for the same run - each constructs its own `Executor` against the shared `Storage` and the two will race on `SaveStepResult` / `SaveRun`. With `MemoryStorage` the latest write wins under the storage mutex; with `FileStorage` each write is atomic via rename so the file never half-writes, but the visible state may flip between the two parallel runs. Embedders that may resume from multiple processes must serialise externally (file lock, queue, advisory lock).
:::

## Transcript-based step resume

The router consults an internal resumer hook (the `Executor` implements it) when a `Send` lands on a closed mailbox. The hook decides whether the step can be resumed; if yes, a resume goroutine spawns and the original `Send` returns immediately. The hook itself is not part of the public API; consumers trigger resume implicitly by `Send`-ing to a closed step.

When triggered, the resume goroutine:

1. Loads the step's transcript from `TranscriptStore`.
2. Appends `prompt` as a new user turn.
3. Spawns a fresh `AgentRunner` with the same model and tools.
4. Lets the agent run a new turn (or several) until it produces a final assistant response.
5. Sends a reverse `RouterMessage` back to `fromAgent` with the response.

The result is asynchronous: the original `Send` returns immediately and the resumer eventually emits `EventResumeStarted` / `EventResumeCompleted` (or `EventResumeFailed`) events.

### TranscriptStore

```go
type TranscriptStore interface {
    // Append adds messages to stepID's transcript under runID.
    // Returns ErrTranscriptTooLarge when the transcript would exceed
    // the configured size cap; on cap-exceeded the messages are NOT
    // appended.
    Append(runID, stepID string, msgs []provider.Message) error

    // Load returns the full transcript for (runID, stepID). Returns
    // ErrNoTranscript when no transcript exists. Returns
    // ErrTranscriptTooLarge when a prior Append hit the cap and sealed
    // the slot.
    Load(runID, stepID string) (*StepTranscript, error)

    // Delete removes a transcript. Idempotent: returns nil if no
    // transcript exists.
    Delete(runID, stepID string) error
}
```

An optional extension `TranscriptTruncatedLoader` exposes `LoadTruncated(runID, stepID string, maxMessages int) (*StepTranscript, error)` so a sealed transcript can still be resumed in truncated form when `WithTruncationOnCapReached()` is set.

Each step's transcript is the full message history for that step (system prompt, user prompts, assistant responses, tool calls and results) plus the system prompt and model identifier captured at run start (`StepTranscript.SystemPrompt`, `StepTranscript.Model`). The default in-process implementation is `InMemoryTranscriptStore`.

Configuration via orchestrator options:

- `WithTranscriptStore(factory)` - install a custom store (factory pattern; called once per `Run`).
- `WithMaxTranscriptMessages(n)` - per-step message cap for the default in-memory store.
- `WithMaxTranscriptBytes(b)` - per-step byte cap for the default in-memory store.

Hitting either cap turns subsequent `Append` calls into `ErrTranscriptTooLarge`, and a future resume on that step emits `DropReasonTranscriptTooLarge`.

### ModelResolver

When the saved transcript carries a model identifier different from the executor's default runner model, the resumer needs to resolve the saved identifier back to a `provider.LanguageModel`. Install `WithModelResolver`:

```go
import (
    "github.com/zendev-sh/zenflow"
    "github.com/zendev-sh/goai/provider"
    "github.com/zendev-sh/goai/provider/google"
    // "github.com/zendev-sh/goai/provider/azure"
    // "github.com/zendev-sh/goai/provider/bedrock"
)

orch := zenflow.New(
    zenflow.WithModel(defaultLLM),
    zenflow.WithModelResolver(func(modelID string) (provider.LanguageModel, error) {
        // Construct the model directly via the appropriate goai provider
        // package. There is no central goai.ResolveModel; dispatch on
        // the saved modelID yourself (e.g. parse a "provider:model" prefix
        // and call google.Chat / azure.Chat / bedrock.Chat / vertex.Chat).
        return google.Chat(modelID), nil
    }),
)
```

The CLI binary's `resolveModel(providerName, modelID string) provider.LanguageModel` in `cmd/zenflow/main.go` is the reference implementation; it dispatches across every provider zenflow ships with and is a good starting point to copy into a library consumer.

Without a resolver, mismatch fails loudly with `ErrModelResolverMissing`. Resolution failures inside the resolver surface as `DropReasonResolverError`.

For intra-run resume, the executor remembers each step's model identifier in memory, so the resolver is only required for cross-run (process-restart) resume with non-default models.

### TruncateOnCapReached

When a sealed transcript's `Load` returns `ErrTranscriptTooLarge`, the default behaviour is to fail the resume with `DropReasonTranscriptTooLarge`. Set `WithTruncationOnCapReached()` to fall back to `LoadTruncated` (when the store implements `TranscriptTruncatedLoader`), preserving operability at the cost of a potentially-incomplete history. Pair with `WithoutTruncationOnCapReached()` to restore the default.

### Resume goroutine outcome

Each resume invocation gets an internal handle the executor uses to track its lifetime. The handle is not part of the public API; it carries the resumed `StepID`, a per-resume `ResumeID`, the `OriginalSender`, a `DoneCh` channel, and the eventual `Result` / `Err`. Consumers observe resumes through the progress sink (see "Resume events" below); they don't interact with the handle directly.

### Drop reasons specific to resume

| `DropReason` | Meaning |
|---|---|
| `DropReasonNoTranscript` | Mailbox closed and no saved transcript exists for the step. |
| `DropReasonTranscriptTooLarge` | Saved transcript exceeds caps. |
| `DropReasonResumeShutdown` | Workflow context cancelled mid-resume. |
| `DropReasonResolverError` | The configured `ModelResolver` returned an error. |

## Resume events

The progress sink sees resume mechanics as four event types:

- `EventResumeStarted` - resume goroutine spawned.
- `EventResumeCompleted` - resumed agent returned a final response.
- `EventResumeFailed` - resume could not complete (cancellation, transcript cap, agent error).
- `EventResumeQueued` - a resume attempt arrived while a resume for the same step was already in flight; the new attempt was queued (or rejected if the active resume's mailbox is full).

## What resume is not

- **A retry mechanism.** Use `step.retries` for retry-on-failure. Resume is for picking up where a terminated step left off; retry is for re-running a step that failed.
- **A pause/resume primitive.** You cannot voluntarily pause a step and resume it later from outside. Resume fires when a router message arrives addressed to a closed mailbox. To checkpoint a long-running run, use `ResumeFlow` with a persistent storage backend.
- **A cross-process IPC channel.** Resume is in-process. The default `InMemoryTranscriptStore` does not survive a process restart. Cross-process resume requires a persistent `TranscriptStore` backend.

## Cross-links

- [Failure handling](/concepts/failure-handling) - statuses and drop reasons
- [Messaging](/concepts/messaging) - how router-driven resume integrates with the inbox model
- [Observability](/concepts/observability) - resume events on the progress sink
- [API: Options](/api/options) - `WithStorage`, `WithTranscriptStore`, `WithModelResolver`
- [API: Core Functions](/api/core-functions) - `ResumeFlow` signature
