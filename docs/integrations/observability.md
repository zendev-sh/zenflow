---
title: Observability
description: zenflow emits OpenTelemetry spans through its Tracer interface. The default in-tree implementation lives in the zenflow/observability/otel...
---

# Observability

zenflow emits OpenTelemetry spans through its `Tracer` interface. The default in-tree implementation lives in the `zenflow/observability/otel` sub-module; once wired, spans flow through [goai](https://goai.sh)'s tracing options for LLM call instrumentation.

## Where the spans live

zenflow produces spans at several levels. The `Tracer` interface lives in `interfaces.go`; span names are emitted by `internal/exec/`:

| Span name | When | Notable attributes |
| --- | --- | --- |
| `zenflow.flow` | Top of `RunFlow` / `ResumeFlow` | `zenflow.run_id`, `zenflow.workflow.name`, `zenflow.resume` (on resume) |
| `zenflow.goal` | Top of `RunGoal` | `zenflow.run_id`, `zenflow.goal.text` (truncated to 200 chars) |
| `zenflow.agent` | Top of `RunAgent` | `zenflow.run_id`, `zenflow.agent.prompt` (truncated) |
| `zenflow.step` | Per step inside a workflow DAG | `zenflow.run_id`, `zenflow.step.id`, `zenflow.step.agent` |
| `zenflow.coordinator` | Each coordinator activation (per step event, plus the final synthesis) | `zenflow.run_id`, `zenflow.coordinator.phase` |
| `zenflow.loop` | Per `loop` block in a workflow | `zenflow.run_id`, `zenflow.step.id`, `zenflow.loop.type` |
| `zenflow.loop.iteration` | Per iteration inside a `loop` block | `zenflow.run_id`, `zenflow.step.id`, `zenflow.loop.iteration` |
| `zenflow.include` | When a workflow `include`s another workflow | `zenflow.run_id`, `zenflow.step.id`, `zenflow.include.ref` |

LLM call spans nest underneath the relevant zenflow span and come from goai. Names are `goai.generate`, `goai.stream`, etc., with provider, model, and token attributes attached. Tool call spans (`goai.tool`) nest under those.

The result for one workflow run is a tree shaped like:

<figure class="zf-diagram">
<svg viewBox="0 0 760 360" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="OTel trace tree: zenflow.flow span at the root contains two zenflow.step spans (review-pr and summarize); review-pr contains a goai.generate that itself contains two goai.tool spans (read_file and grep) plus a second goai.generate after tool results; summarize contains one goai.generate.">
<!-- Vertical guide rails (one per level) -->
  <line class="zf-trace-line" x1="40"  y1="50"  x2="40"  y2="320"/>
  <line class="zf-trace-line" x1="100" y1="100" x2="100" y2="220"/>
  <line class="zf-trace-line" x1="160" y1="150" x2="160" y2="200"/>

  <!-- Horizontal connectors -->
  <line class="zf-trace-line" x1="40"  y1="100" x2="100" y2="100"/>
  <line class="zf-trace-line" x1="100" y1="150" x2="160" y2="150"/>
  <line class="zf-trace-line" x1="160" y1="180" x2="220" y2="180"/>
  <line class="zf-trace-line" x1="160" y1="200" x2="220" y2="200"/>
  <line class="zf-trace-line" x1="100" y1="220" x2="160" y2="220"/>
  <line class="zf-trace-line" x1="40"  y1="270" x2="100" y2="270"/>
  <line class="zf-trace-line" x1="100" y1="270" x2="100" y2="310"/>
  <line class="zf-trace-line" x1="100" y1="310" x2="160" y2="310"/>

  <!-- Level 0: zenflow.flow -->
  <g class="zf-trace-node-flow" transform="translate(20, 40)">
    <rect width="280" height="28" rx="4" stroke-width="1.4"/>
    <text class="zf-trace-name zf-trace-name-flow" x="14" y="18">zenflow.flow</text>
    <text class="zf-trace-meta" x="140" y="18">(run-XYZ)</text>
  </g>

  <!-- Level 1a: zenflow.step (review-pr) -->
  <g class="zf-trace-node-step" transform="translate(80, 88)">
    <rect width="280" height="26" rx="4" stroke-width="1.2"/>
    <text class="zf-trace-name zf-trace-name-step" x="14" y="17">zenflow.step</text>
    <text class="zf-trace-meta" x="135" y="17">(review-pr)</text>
  </g>

  <!-- Level 2: goai.generate (under review-pr, before tools) -->
  <g class="zf-trace-node-llm" transform="translate(140, 138)">
    <rect width="240" height="24" rx="4" stroke-width="1.2"/>
    <text class="zf-trace-name zf-trace-name-llm" x="14" y="16">goai.generate</text>
  </g>

  <!-- Level 3a: goai.tool (read_file) -->
  <g class="zf-trace-node-tool" transform="translate(200, 168)">
    <rect width="240" height="24" rx="4" stroke-width="1.2"/>
    <text class="zf-trace-name zf-trace-name-tool" x="14" y="16">goai.tool</text>
    <text class="zf-trace-meta" x="100" y="16">(read_file)</text>
  </g>

  <!-- Level 3b: goai.tool (grep) -->
  <g class="zf-trace-node-tool" transform="translate(200, 192)">
    <rect width="240" height="24" rx="4" stroke-width="1.2"/>
    <text class="zf-trace-name zf-trace-name-tool" x="14" y="16">goai.tool</text>
    <text class="zf-trace-meta" x="100" y="16">(grep)</text>
  </g>

  <!-- Level 2: second goai.generate (after tool results) -->
  <g class="zf-trace-node-llm" transform="translate(140, 208)">
    <rect width="280" height="24" rx="4" stroke-width="1.2"/>
    <text class="zf-trace-name zf-trace-name-llm" x="14" y="16">goai.generate</text>
    <text class="zf-trace-meta" x="135" y="16">(after tool results)</text>
  </g>

  <!-- Level 1b: zenflow.step (summarize) -->
  <g class="zf-trace-node-step" transform="translate(80, 258)">
    <rect width="280" height="26" rx="4" stroke-width="1.2"/>
    <text class="zf-trace-name zf-trace-name-step" x="14" y="17">zenflow.step</text>
    <text class="zf-trace-meta" x="135" y="17">(summarize)</text>
  </g>

  <!-- Level 2: goai.generate under summarize -->
  <g class="zf-trace-node-llm" transform="translate(140, 298)">
    <rect width="240" height="24" rx="4" stroke-width="1.2"/>
    <text class="zf-trace-name zf-trace-name-llm" x="14" y="16">goai.generate</text>
  </g>
</svg>
<figcaption>One <code>zenflow.flow</code> root span containing two <code>zenflow.step</code> children. Each step span wraps the <code>goai.generate</code> calls it issues; tool calls (<code>goai.tool</code>) nest under the generate that invoked them.</figcaption>
</figure>

## Wiring OTel in Go

The bridge is two lines: install zenflow's tracer via `zenotel.WithTracing()`, and enable [goai](https://goai.sh) LLM-call spans via `WithGoAIOptions(zenotel.GoAIOption())`. Both come from the `zenflow/observability/otel` sub-module so the core library has no OTel dependency.

```go
import (
    "github.com/zendev-sh/zenflow"
    zenotel "github.com/zendev-sh/zenflow/observability/otel"

    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func setupTracing(ctx context.Context) (*sdktrace.TracerProvider, error) {
    exp, err := otlptracehttp.New(ctx) // reads OTEL_EXPORTER_OTLP_ENDPOINT etc.
    if err != nil {
        return nil, err
    }
    tp := sdktrace.NewTracerProvider(
        sdktrace.WithBatcher(exp),
        sdktrace.WithResource(/* your resource attrs */),
    )
    otel.SetTracerProvider(tp)
    return tp, nil
}

func main() {
    ctx := context.Background()
    tp, err := setupTracing(ctx)
    if err != nil {
        log.Fatal(err)
    }
    defer tp.Shutdown(ctx)

    orch := zenflow.New(
        zenflow.WithModel(model),
        zenotel.WithTracing(),
        zenflow.WithGoAIOptions(zenotel.GoAIOption()),
    )
    defer orch.Close()

    result, err := orch.RunFlow(ctx, wf)
    _ = result
    _ = err
}
```

Two pieces matter:

- **`zenotel.WithTracing()`** returns a `zenflow.Option` that installs zenflow's span-producing layer. Without it, the `zenflow.flow` / `zenflow.step` / `zenflow.agent` / `zenflow.coordinator` / `zenflow.loop` / `zenflow.include` spans are not produced. The implementation lives in the `zenflow/observability/otel` sub-module so the core library has no OTel dependency.
- **`WithGoAIOptions(zenotel.GoAIOption())`** enables the LLM-call spans. zenflow forwards the [goai](https://goai.sh) options into the runner, where they wire up `goai.generate` and `goai.tool` spans that nest under whatever parent context the runner received from zenflow.

The OTel SDK setup itself (exporter, resource, propagators) is the same as for any Go service - zenflow just produces spans into whatever provider you've globally registered.

## Routing to specific backends

OTel's `OTEL_EXPORTER_OTLP_ENDPOINT` env var controls where the spans end up. Common destinations:

### Langfuse

Langfuse Cloud and self-hosted both expose an OTLP-compatible endpoint:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=https://cloud.langfuse.com/api/public/otel
export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Basic $(echo -n "${LANGFUSE_PUBLIC_KEY}:${LANGFUSE_SECRET_KEY}" | base64)"
```

Langfuse renders the LLM calls (prompts, completions, tokens, costs) inside the wrapping `zenflow.step` span, so each step shows up as a "trace" in Langfuse with the LLM rounds expanded underneath.

### Jaeger

Run Jaeger's all-in-one container locally:

```bash
docker run --rm -d \
    -p 16686:16686 \
    -p 4317:4317 \
    -p 4318:4318 \
    jaegertracing/all-in-one:latest

export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
```

Then open `http://localhost:16686` and pick "zenflow" from the service dropdown. The flow tree renders as nested spans with timing on the right.

### Datadog

Datadog accepts OTLP via the Datadog Agent's OTLP receiver:

```yaml
# datadog-agent.yaml (Helm values or daemonset config)
otlp_config:
  receiver:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
```

Then point your app at the agent:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
```

Datadog's APM UI groups the spans into a service map and highlights long-running steps, error rates, and token usage when the [goai](https://goai.sh) spans set those attributes.

## Span attributes worth filtering on

The attributes zenflow attaches at each level make for useful searches:

- `zenflow.run_id` - tie every span in one workflow run together. The same ID flows into the NDJSON event stream's `runId` field, so you can cross-reference a CI artifact with a trace.
- `zenflow.workflow.name` - filter by workflow YAML.
- `zenflow.step.id` - drill into one step across many runs.
- `zenflow.step.agent` - group by the agent persona that ran the step (useful when one agent shows up in many workflows).
- `zenflow.resume = "true"` - filter for runs that came from `ResumeFlow` rather than a fresh start.

The [goai](https://goai.sh) layer adds provider/model attributes (`gen_ai.system`, `gen_ai.request.model`, `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`) following the OTel GenAI semantic conventions. Cost-attribution dashboards typically aggregate over `gen_ai.system` and `gen_ai.request.model`.

## Sampling

For production deployments where every workflow run is interesting (which is most zenflow use cases - the workflows are explicit, not continuous request handling), use `AlwaysSample`:

```go
sdktrace.NewTracerProvider(
    sdktrace.WithSampler(sdktrace.AlwaysSample()),
    sdktrace.WithBatcher(exp),
)
```

If you have high workflow throughput and need to drop some, `ParentBased(TraceIDRatioBased(0.1))` keeps 10 percent of the trees - and importantly, it samples at the root so you never lose a single step from a sampled trace.

## Without OTel

If you don't want to wire OTel at all, the NDJSON event stream from `--json` carries equivalent information for most diagnostic needs: per-step start and end events with status, duration, and token counts. It is plain text, easy to grep, and works in CI without any extra infrastructure.

OTel becomes worth the setup once you have multiple long-running services, want flame-graph timing across step parallelism, or need to tie zenflow runs into an existing observability stack.

## CLI `--trace` and the `otel` build tag

The Go API patterns above work on every distribution path - the small tracer interface lives in the default build. The CLI `--trace` flag, however, is gated behind the `otel` build tag because wiring up the full OTel SDK + OTLP exporter would otherwise pull a large dependency graph into every `go install` consumer.

Pre-built binaries (`install.sh`, Homebrew, Docker) all ship with `-tags otel` baked in, so `--trace` works on those paths out of the box.

Source builds via `go install` default to no build tag and `--trace` is a runtime no-op there. To get a binary with `--trace` wired up, install or build with the tag:

```bash
go install -tags otel github.com/zendev-sh/zenflow/cmd/zenflow@latest
```

The Go API path (`WithTracer(...)`) is unaffected by the build tag - if you embed zenflow as a library, you can wire any OTel-compatible tracer without rebuilding.
