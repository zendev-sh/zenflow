---
title: CLI
description: zenflow is a single Go binary. It loads YAML workflows, runs them through an LLM coordinator, and streams progress to your terminal as either...
---

# CLI

`zenflow` is a single Go binary. It loads YAML workflows, runs them through an LLM coordinator, and streams progress to your terminal as either human-readable text or NDJSON events.

This page is the entry point for the CLI cluster. For per-command detail, see [Commands](./commands). For the full flag table, see [Flags](./flags).

## Verbs

The binary dispatches on the first positional:

| Verb | What it does |
| --- | --- |
| `zenflow validate <file>` | Parse and validate a YAML workflow without running it. |
| `zenflow plan <file>` | Show the execution plan (DAG topological order). |
| `zenflow flow <file>` | Load and execute a YAML workflow. |
| `zenflow goal <text>` | Ask the LLM coordinator to decompose a goal into a workflow, then run it. |
| `zenflow agent <prompt>` | Run a single-agent conversation. |

`validate` and `plan` are inert (no LLM calls). `flow`, `goal`, and `agent` require an LLM provider configured via `--model` and the matching env vars.

```bash
zenflow validate workflow.yaml
zenflow plan workflow.yaml
zenflow flow workflow.yaml --model bedrock/anthropic.claude-sonnet-4-6
zenflow goal "audit my Go module for breaking API changes" --model gemini-2.5-flash
zenflow agent "summarize go.sum" --model bedrock/anthropic.claude-sonnet-4-6
```

## Running via Docker

The same verbs work against the official multi-arch image at `ghcr.io/zendev-sh/zenflow`:

```bash
docker run --rm -e GEMINI_API_KEY \
    -v "$(pwd)":/wd -w /wd \
    ghcr.io/zendev-sh/zenflow:v0.1.0-pre \
    flow workflow.yaml --model google/gemini-2.5-flash
```

The image's `ENTRYPOINT` is `zenflow`, so the first positional after the image tag is the verb (`flow`, `goal`, `agent`, `validate`, `plan`). See [Docker](../integrations/docker) for tag conventions, env-var contract, and the Kubernetes Job manifest.

## Common flags

These flags work across `flow`, `goal`, and `agent` (with a few command-specific exceptions called out in [Commands](./commands)).

| Flag | Type | Default | Purpose |
| --- | --- | --- | --- |
| `--model MODEL` | string | `$ZENFLOW_MODEL` | LLM model identifier. Supports `provider/model` and bare names. |
| `--timeout DURATION` | Go duration | unset | Hard timeout for the run. |
| `--max-concurrency N` | integer | YAML default | Cap on parallel steps (`flow` / `goal`). |
| `--max-depth N` | integer | `3` | Cap on nested agent-spawn depth. |
| `--max-retries N` | integer | provider default | Override [goai](https://goai.sh) retry budget. |
| `--workdir DIR` | path | cwd | Working directory for tool execution. (See `--sandbox` for tool-allowlist gating.) |
| `--json` | bool | off | Emit NDJSON events on stdout instead of human-readable output. |
| `--stream` | bool | off | Stream agent text token by token. |
| `--verbose` | bool | off | Show agent thinking, tool output bodies, and per-turn token counts. |
| `--quiet` | bool | off | Suppress narration. Show events only. |
| `--summary-only` | bool | off | Skip per-step narration; show one final synthesis. |
| `--plan` | bool | off | Print the DAG before execution (`flow` only). |
| `--resume RUN_ID` | string | unset | Resume from a checkpoint (`flow` only). |
| `--trace` | bool | off | Enable OpenTelemetry tracing. |
| `--thinking LEVEL` | enum | `off` | Extended reasoning: `off`, `low`, `medium`, `high`. |

`zenflow agent` adds `--max-turns N` for the conversation turn cap.

For the full table including types and defaults, see [Flags](./flags).

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success. |
| `1` | Step or workflow failure. |
| `2` | Validation or coordinator-side error. |
| `3` | Configuration or usage error. |
| `124` | Hard-watchdog timeout (when `--timeout` fires and the process did not unwind cleanly). |

Exit code `124` means the workflow exceeded `--timeout + 30s` grace; the CLI then prints a goroutine stack dump and force-exits via `os.Exit(124)` (see `cmd/zenflow/main.go` lines 51-72 and [Failure handling](../concepts/failure-handling)). In-process callers using `RunFlow` directly must implement their own watchdog if they need similar hard cancellation.

## Provider configuration

The CLI auto-detects the provider from the `--model` value plus environment variables. The cleanest path is to use the `provider/model` form:

```bash
zenflow flow workflow.yaml --model google/gemini-2.5-flash
zenflow flow workflow.yaml --model bedrock/anthropic.claude-sonnet-4-6
zenflow flow workflow.yaml --model azure/DeepSeek-V3.2
zenflow flow workflow.yaml --model azure-deployment/gpt-5
```

Bare names work too if the matching env var is set:

| Bare pattern | Picked when |
| --- | --- |
| `gemini-...` | `GEMINI_API_KEY` or `GOOGLE_GENERATIVE_AI_API_KEY` set |
| `anthropic.<model>` (or other Bedrock vendor prefixes) | `AWS_ACCESS_KEY_ID` set |
| `claude-...` | `AZURE_OPENAI_API_KEY` set (Azure Anthropic endpoint) |
| `gpt-...` | `AZURE_OPENAI_API_KEY` set (Azure OpenAI deployment-based path) |
| anything else | falls back to whichever provider env var is present |

`ZENFLOW_MODEL` works as a default when `--model` is not passed.

If no provider can be resolved, the CLI exits with code `3` and a message instructing you to configure a provider.

## Output

By default, `zenflow` writes a colorized human-readable progress stream to stdout. Each event renders as one line with a glyph, a step ID in brackets, and a description. When stdout is not a tty (piped, redirected), colors are disabled but the format is unchanged.

`--json` switches to NDJSON: one JSON object per line. Every event includes `type` and `timestamp`; specific events add `stepId`, `agent`, `data`, `tokens`, etc.

`--stream` streams agent text deltas. In human mode the deltas appear under a `≋ [stepID]` prefix; in JSON mode each delta is an event with `"type":"output"` and a `delta` field.

For the full output catalog (event types, JSON shape, an event-by-event capture), see [Output formats](./output-formats).

## Page map

| Topic | Page |
| --- | --- |
| Per-command synopsis, all flags, exit codes, examples | [Commands](./commands) |
| Flag table grouped by purpose | [Flags](./flags) |
| Stdout format, JSON event shape, streaming | [Output formats](./output-formats) |
