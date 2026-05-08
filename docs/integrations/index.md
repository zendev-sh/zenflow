---
title: Integrations
description: 'zenflow ships as a single Go binary and a Go library. Both surfaces drop cleanly into the places you already run code: CI/CD pipelines, container...'
---

# Integrations

zenflow ships as a single Go binary and a Go library. Both surfaces drop cleanly into the places you already run code: CI/CD pipelines, container images, shell pipelines, and observability stacks.

This section walks each integration shape with copy-pasteable configs.

## Decision matrix

Pick the surface that matches what you already do:

| You want to... | Reach for | Notes |
| --- | --- | --- |
| Run a workflow on every push or PR | [CI/CD](./ci-cd) | GitHub Actions, GitLab CI, CircleCI examples. The binary is small, no daemon. |
| Bake zenflow into an image you deploy | [Docker](./docker) | Multi-stage `golang:1.25` build, distroless runtime, Kubernetes Job manifest. |
| Drive zenflow from a shell, Node, or Python script | [Scripting](./scripting) | NDJSON event stream via `--json`, exit-code semantics, parsing patterns. |
| Send spans to OTel / Langfuse / Jaeger / Datadog | [Observability](./observability) | Wired through [goai](https://goai.sh)'s tracing options. Per-step, per-LLM-call, per-tool spans. |

## What is shared across every integration

Three things hold no matter where you run zenflow:

1. **API keys come through environment variables.** zenflow reads provider env vars (`GEMINI_API_KEY`, `AWS_ACCESS_KEY_ID`, `AZURE_OPENAI_API_KEY`, etc.) the same way [goai](https://goai.sh) does. Keep them in your CI secrets store, your container's `--env-file`, your scheduler's secret mount, etc. Never bake them into the image.
2. **Exit codes are stable.** `0` = success, `1` = workflow failed (one or more steps in failed/partial state), `2` = invalid CLI usage / parse errors, `3` = configuration errors (missing model, invalid YAML, can't find file), `124` = watchdog timeout. See [the errors reference](../api/errors) for the full mapping.
3. **`--json` is the machine surface.** Every CI / scripting integration in this section uses NDJSON output: one JSON event per line on stdout. The shape is documented in [Output Formats](../cli/output-formats); it is additive and stable, so a parser written today keeps working when new event types ship.

## Embedding via the Go API instead

If your integration needs more than CLI invocation - for example, an HTTP service that runs workflows on demand, a long-lived agent worker, or a Bun-style streaming TUI - skip the CLI and import zenflow as a library:

```go
import "github.com/zendev-sh/zenflow"

orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithTools(tools...),
    zenflow.WithProgress(sink),
)
defer orch.Close()

result, err := orch.RunFlow(ctx, wf)
```

Full reference: [Go API](../api/core-functions).

The CLI itself is built on the same library; nothing the CLI does is unreachable from Go code.
