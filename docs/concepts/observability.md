---
title: Observability
description: 'Zenflow emits a typed event stream for every workflow run: step lifecycle events, agent inbox drains, coordinator narration, message drops, token...'
---

# Observability

Zenflow emits a typed event stream for every workflow run: step lifecycle events, agent inbox drains, coordinator narration, message drops, token counts, output deltas. The stream is routed through a `ProgressSink` interface that you wire into the orchestrator. Everything visible - CLI output, JSON logs, OTel spans - is built on this one stream.

## ProgressSink

```go
type ProgressSink interface {
    OnEvent(ctx context.Context, event Event)
    OnOutput(ctx context.Context, output Output)
}
```

`OnEvent` fires on lifecycle and routing events. `OnOutput` fires on streaming text deltas (only when `WithStreaming()` is enabled). Both should be cheap - the orchestrator calls them on the hot path. Heavy work belongs in a goroutine that the sink owns.

Install via `WithProgress`:

```go
orch := zenflow.New(
    zenflow.WithModel(llm),
    zenflow.WithProgress(mySink),
    // ...
)
```

A nil sink is fine - events are dropped silently.

## Built-in sinks

The `github.com/zendev-sh/zenflow/sink` package ships three:

### StdoutSink

Renders events as terminal output. The CLI uses this for the default human-readable mode (`zenflow flow workflow.yaml`).

```go
import "github.com/zendev-sh/zenflow/sink"

s := sink.NewStdoutSink()
s.WithVerbose(true)   // also show agent text output
s.WithShowPlan(true)  // render the DAG before execution

orch := zenflow.New(
    zenflow.WithModel(llm),
    zenflow.WithProgress(s),
)
```

The output uses a small set of glyphs (`▸`, `✓`, `≋`, `⇠`, `⇢`) to distinguish step starts, completions, narration, sends, and drains. No prose explanation of the glyphs is needed - the sink is meant for live tailing, not log analysis.

### JSONSink

Renders one JSON line per event. Right for structured logging, CI capture, or piping into another tool.

```go
s := sink.NewJSONSink()                 // writes to os.Stdout
s := sink.NewJSONSinkTo(myWriter)       // custom writer (file, buffer, network)

orch := zenflow.New(
    zenflow.WithModel(llm),
    zenflow.WithProgress(s),
)
```

Each event line carries `type`, `runId`, `stepId`, `timestamp`, plus event-specific fields. Outputs (when streaming is on) get their own `type: "output"` lines.

The CLI uses this for `--json` mode. Capturing the stream programmatically:

```go
var buf bytes.Buffer
s := sink.NewJSONSinkTo(&buf)

orch := zenflow.New(
    zenflow.WithModel(llm),
    zenflow.WithProgress(s),
)
result, err := orch.RunFlow(ctx, wf)
if err != nil {
    return err
}

// buf now holds one JSON line per event.
scanner := bufio.NewScanner(&buf)
for scanner.Scan() {
    var event map[string]any
    json.Unmarshal(scanner.Bytes(), &event)
    if event["type"] == "error" {
        // step failure (or other error); see event["error"] for details
    }
}
```

### Buffered

`sink.Buffered(wrapped, window)` wraps another sink and coalesces output deltas within a time window. Useful when the wrapped sink is expensive (e.g. writes to a network log) and you do not need every individual delta.

```go
s := sink.Buffered(sink.NewJSONSink(), 100*time.Millisecond)
```

Lifecycle events flush immediately; output deltas are batched up to the window.

## Event types

The full set lives in `interfaces.go`. Highlights:

| Event | Fires on |
|-------|----------|
| `EventWorkflowStart` | Workflow execution began. |
| `EventWorkflowEnd` | Workflow execution completed (any status). |
| `EventStepStart` | A step's agent loop began. |
| `EventStepEnd` | A step finished successfully (status = completed). Failed steps fire `EventError`; skipped steps fire `EventStepSkipped` (see rows below); cancelled steps emit no dedicated event. |
| `EventStepSkipped` | A step was skipped (failed dependency or false condition). |
| `EventToolCall` | A tool call was emitted. |
| `EventMessage` | Informational message (e.g. child agent model warning). |
| `EventError` | An error occurred (step failure, storage error, judge failure). |
| `EventCoordinatorNarration` | The coordinator called `narrate`. |
| `EventCoordinatorMessage` | The coordinator pushed a targeted message via `forward_to_agent`. |
| `EventCoordinatorSynthesis` | The coordinator called `finalize`. |
| `EventCoordinatorInboxMessage` | The coordinator drained one message from its mailbox. |
| `EventMessageSent` | A `send_message` or `forward_to_agent` call queued successfully. |
| `EventAgentInboxDrain` | A step agent drained one message into its conversation. |
| `EventMessageDropped` | A send was rejected (`Data["reason"]` is the drop reason). |
| `EventPlanReady` | `RunGoal` decomposition produced a workflow (`Data["workflow"]`). |
| `EventResumeStarted` / `EventResumeCompleted` / `EventResumeFailed` / `EventResumeQueued` | Resume mechanics. |

Every event carries `Type`, `Timestamp`, `RunID`, `StepID`, `AgentName`, `Data`, `Duration`, `Tokens`, `Error`, `Message`. Most fields are optional - populate or read defensively.

## Output (streaming)

`OnOutput` fires once per token (or token batch) when streaming is enabled. The `Output` struct:

```go
type Output struct {
    RunID     string
    StepID    string
    AgentID   string
    Delta     string
    Done      bool
    Reasoning bool // true for thinking/reasoning tokens
}
```

`Delta` is the next chunk of text. `Done` flips true on the final delta. `Reasoning` distinguishes thinking-model output (Claude reasoning, OpenAI o-series) from the final answer.

Wire streaming with `WithStreaming()` (or `WithoutStreaming()` to disable explicitly). Without it, agents accumulate full responses and `OnOutput` never fires.

## Verbosity

`WithVerbose()` enables agent output display (use `WithoutVerbose()` to disable). By default, only workflow events (`▸ step started`, `✓ step done`) and coordinator narration (`≋ ...`) are shown - agent reasoning, tool input/output bodies, and per-turn token counts stay suppressed. With verbose, the agent's own LLM responses, tool call payloads, and tool result bodies are surfaced too. The toggle affects the `StdoutSink` rendering path only; `JSONSink` always emits the full event stream. Useful for debugging an opaque coordinator decision or tool failure; noisy in production where it can multiply event volume by 5-10x on tool-heavy workflows.

## Secret redaction

zenflow ships a conservative best-effort redaction pass (`redactSecrets` in `redact.go`) that runs on tool-call inputs only. It is not an end-to-end protection - what it covers and what it does not is summarised below.

**Patterns covered.** Four prefix patterns (case-insensitive, the prefix is preserved):

- `Authorization: Bearer <token>`
- `X-Api-Key: <value>`
- `--password <value>` / `--password=<value>`
- `api_key` / `api-key` / `apikey` `=` / `:` `<value>` (with optional quoting)

Plus four bare-token patterns matched anywhere in the string:

- OpenAI `sk-...` (20+ alphanumerics)
- GitHub `ghp_...` (20+ alphanumerics)
- Slack `xox[bp]-...`
- AWS `AKIA[A-Z0-9]{16}`

**Where it runs - and where it does NOT.** Redaction is applied **only** to tool-call **inputs** that surface in `EventToolCall` start/end events (see `agent_runner.go` lines 728 and 758). It is **not** applied to:

- Tool **outputs** (`info.Output` is emitted verbatim through `EventToolCall` end events).
- Transcripts (`TranscriptStore` receives full message bodies, including tool results).
- Storage payloads (`FileStorage` and `MemoryStorage` persist the full `StepResult.Content` and message history).
- Coordinator narration, agent text deltas, or any other event body.

**Implication.** If a tool returns a secret in its output (for example, a `read` of a `.env` file or a shell command that prints credentials), that secret will appear in event payloads, transcript snapshots, and resume checkpoints. Strip secrets at the tool boundary before returning them - do not rely on redaction downstream. The pattern set is intentionally conservative (it will miss novel formats) and is meant for human log review, not for compliance-grade scrubbing.

## OTel via goai

Distributed tracing rides on [goai](https://goai.sh)'s tracing layer. The zenflow sub-module `zenflow/observability/otel` implements the `Tracer` interface in terms of OpenTelemetry spans.

```go
import (
    "context"
    "github.com/zendev-sh/zenflow"
    zfotel "github.com/zendev-sh/zenflow/observability/otel"
)

func main() {
    ctx := context.Background()
    orch := zenflow.New(
        zenflow.WithModel(llm),
        zfotel.WithTracing(),
    )
    defer orch.Close()
    // ... use orch
    _ = ctx
}
```

`zfotel.WithTracing()` returns a `zenflow.Option` that wires zenflow's `Tracer` interface to the global OpenTelemetry `TracerProvider`. There is no separate `Shutdown` to call - the underlying `TracerProvider` is owned by the OTel SDK setup in your `main` (e.g. via `zfotel.WithDefaultExporter`), and `orch.Close()` releases zenflow's own resources.

The tracer creates spans at three levels:

- `zenflow.flow` - the whole `RunFlow` invocation (with `zenflow.run_id`, `zenflow.workflow.name`).
- `zenflow.goal` - the `RunGoal` decomposition phase (with `zenflow.goal.text` truncated to 200 chars).
- `zenflow.agent` - each `RunAgent` invocation (with `zenflow.agent.prompt` truncated to 200 chars).
- Per-step spans inside the executor.

LLM-call spans come from [goai](https://goai.sh) automatically when its OTel hook is configured. See [goai observability docs](https://goai.sh) for the LLM-side spans (request, tokens, cost).

## Debugging coordinator narration

When the coordinator misbehaves (forwards to the wrong step, narrates twice, never finalises), the JSON sink is the right tool:

```bash
zenflow flow my-workflow.yaml --json | jq 'select(.type | startswith("coordinator"))'
```

This filters to only `coordinator_narration`, `coordinator_message`, `coordinator_synthesis`, `coordinator_inbox_message`. Cross-reference with `message_dropped` events:

```bash
zenflow flow my-workflow.yaml --json | jq 'select(.type == "message_dropped")'
```

Each drop carries `data.reason`, `data.from`, `data.to` so you can pinpoint the address that failed.

## Drop callbacks

For metrics or alerting paths that need drop visibility without subscribing to the full event firehose:

```go
orch := zenflow.New(
    zenflow.WithModel(llm),
    zenflow.WithDropCallback(func(d zenflow.DropEvent) {
        metrics.Increment("zenflow.drops", "reason", d.Reason.String())
    }),
)
```

The callback runs synchronously by default. For high-throughput flows, set `WithDropCallbackBufferSize(n)` to dispatch through a buffered channel; overflow falls back to synchronous (no drop event is itself silently lost).

## Composing sinks

`ProgressSink` is just an interface. You can compose sinks with a fan-out wrapper:

```go
type fanout struct {
    sinks []zenflow.ProgressSink
}

func (f *fanout) OnEvent(ctx context.Context, e zenflow.Event) {
    for _, s := range f.sinks {
        s.OnEvent(ctx, e)
    }
}

func (f *fanout) OnOutput(ctx context.Context, o zenflow.Output) {
    for _, s := range f.sinks {
        s.OnOutput(ctx, o)
    }
}
```

Use this to send events to both a JSON log and the terminal renderer simultaneously.

## Buffer sizing

`WithProgressBufferSize(n)` configures the non-blocking event-bus wrapper. Events emit non-blocking on the critical path while the buffered channel has capacity. On overflow, the wrapper applies a bounded back-pressure (up to 1 second) before dropping. Larger buffers tolerate slower sinks at the cost of memory.

The default (1024) is right for terminal output. For high-throughput pipelines feeding remote logging, raise it to 8192+ and run a separate goroutine to consume.

## Run IDs

Every event carries a `RunID`. The orchestrator generates one per `RunFlow` / `RunGoal` / `RunAgent` invocation. Pin a specific ID with `WithRunID("...")` when the caller already allocated one (for cross-system correlation):

```go
orch := zenflow.New(
    zenflow.WithModel(llm),
    zenflow.WithRunID("req-abc-123"),
)
result, err := orch.RunFlow(ctx, wf)
// All emitted events carry runID="req-abc-123".
```

`ResumeFlow` already takes `runID` as an argument and ignores `WithRunID`.

## Cross-links

- [Coordinator](/concepts/coordinator) - what the coord-related events mean
- [Messaging](/concepts/messaging) - send / drain / drop event semantics
- [Failure handling](/concepts/failure-handling) - `EventError` and `EventMessageDropped` reasons
- [API: Options](/api/options) - `WithProgress`, `WithStreaming`, `WithVerbose`, `WithTracer`
- [Integrations: Observability](/integrations/observability) - OTel collector, log aggregator examples
