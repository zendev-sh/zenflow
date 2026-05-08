---
name: zenflow
description: Multi-agent workflow engine for Go. Declarative YAML workflows; an LLM coordinator routes events through hub-and-spoke mailboxes. Three CLI verbs (flow / goal / agent) cover deterministic execution, dynamic planning, and one-shot agent calls. Use this when you need to run a multi-step LLM pipeline with branching, loops, conditions, retries, or persistence.
allowed-tools: Bash
---

# zenflow

zenflow is a multi-agent workflow engine for Go. Workflows are declarative YAML; the engine is a DAG executor with an LLM coordinator that narrates progress and routes events between running steps through race-safe mailboxes. The Go SDK layer is [goai](https://goai.sh) - any provider goai supports works as a zenflow agent backend (Google, AWS Bedrock, Azure, OpenAI-compatible).

## Install

One-line installer (Linux + macOS):

```bash
curl -fsSL https://zenflow.sh/install.sh | sh
```

Or via `go install`:

```bash
go install github.com/zendev-sh/zenflow/cmd/zenflow@latest
```

Or pull the Docker image:

```bash
docker pull ghcr.io/zendev-sh/zenflow:latest
```

The single Go binary has no runtime dependencies.

## Three CLI verbs

| Command | When |
| --- | --- |
| `zenflow flow workflow.yaml` | The plan is fixed up-front; you want a deterministic DAG execution. |
| `zenflow goal "build a thing"` | The plan must adapt to user input; the coordinator decomposes the goal into a workflow on the fly and runs it. |
| `zenflow agent "<prompt>"` | One-shot agent call with optional tool loop. Reuses zenflow's lifecycle hooks and provider routing. |

### `zenflow flow` - deterministic DAG

```bash
zenflow flow review.yaml --model gemini/gemini-3-pro-preview
```

Loads the YAML, validates, runs every step in dependency order. Failures cascade per `options.onStepFailure`.

### `zenflow goal` - dynamic planning

```bash
zenflow goal "Refactor the auth module to use OAuth2" --model bedrock/anthropic.claude-sonnet-4-6
```

The coordinator LLM decomposes the goal into a workflow (agents + steps), validates it against the spec schema, and executes. Pass `--plan` to print the plan without running it.

### `zenflow agent` - single-agent chat

```bash
zenflow agent "Summarise this repo's README" --model azure-deployment/gpt-5
```

A single agent loop with the configured tool catalog. Most useful for one-shot tasks that don't need multi-step coordination.

## Provider env vars

Set the credentials for whichever provider you target:

| Provider | Env var | Example model |
| --- | --- | --- |
| Google Gemini | `GEMINI_API_KEY` | `gemini/gemini-3-pro-preview` |
| AWS Bedrock | `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY` (or AWS profile) | `bedrock/anthropic.claude-sonnet-4-6`, `bedrock/minimax.minimax-m2.5` |
| Azure (AI Services / OpenAI / Anthropic) | `AZURE_OPENAI_API_KEY` + `AZURE_RESOURCE_NAME` | `azure-deployment/gpt-5`, `azure-deployment/claude-sonnet-4-6` |

The `--model` flag accepts the `provider/model-id` form. For Azure, deployment-based URLs are auto-resolved when the model id matches a known deployment family.

## YAML workflow shape

Minimum:

```yaml
name: my-workflow

agents:
  writer:
    description: A concise technical writer.
    prompt: "Always reply in numbered bullet points."

steps:
  - id: draft
    agent: writer
    instructions: "Draft a 5-point summary of {{.context.input}}."
```

Add features as you need them:

- `dependsOn: [otherStep]` - DAG edges.
- `condition: steps.draft.result.length > 100` - CEL expression; step is `skipped` when false.
- `loop:` - three flavours: `forEach` (iterate an array from a previous step's output), `repeat-until` (run a sub-DAG until an `untilAgent` returns `done: true` or a `until` CEL expression is true), `outputMode: cumulative | last`.
- `include: subworkflow.yaml` - compose sub-workflows; their step IDs are namespaced under the parent step.
- `resultSchema: { ... }` - auto-injects a `submit_result` tool so the agent emits structured output.
- `options:` - `maxConcurrency`, `onStepFailure: cascade | skip-dependents | abort`, `timeout`, `stepTimeout`, `isolation: worktree-per-step`, `scheduler: dependency-first | round-robin | least-busy`.

Full reference: <https://zenflow.sh/yaml>.

## NDJSON `--json` event schema

Every CLI verb accepts `--json` for line-delimited JSON output. Each event line carries:

| Field | Type | Notes |
| --- | --- | --- |
| `type` | string | Event type (see table below). |
| `timestamp` | RFC3339 string | Event clock. |
| `runId` | string | Workflow run ID; correlates events from the same run. |
| `stepId` | string | Step ID; absent for run-scoped events. |
| `agent` | string | Agent name; absent for non-agent events. |
| `agentId` | string | Per-call agent ID (set on streaming `output` events when the runner is namespaced). |
| `message` | string | Human-readable detail; semantics differ per event type. |
| `duration` | string | Go duration string (`"1.234s"`); set on completion events. |
| `tokens` | object | `{prompt, completion, total}` token counts; set on completion events. |
| `error` | string | Error string; set on failure events. |
| `data` | object | Event-type-specific payload (input args, output bytes, drop reasons, etc.). |
| `delta` | string | Streaming token delta. Only on `output`. |
| `done` | boolean | Streaming-end marker. Only on `output`. |
| `reasoning` | boolean | Set to `true` when the streaming delta is reasoning/thinking content (vs final agent text). Omitted otherwise. |

### Event types

| `type` | Emitted when |
| --- | --- |
| `workflow_start` | Workflow run begins. |
| `workflow_end` | Workflow run ends (success or failure). |
| `step_start` | A DAG step begins. |
| `step_end` | A DAG step ends successfully. |
| `step_skipped` | A step skipped (failed dep with `skip-dependents` strategy, or false `condition`). |
| `error` | A step failed; subsequent dependents may cascade per `options.onStepFailure`. |
| `agent_turn` | The agent loop took a turn (LLM call). `data.phase` is `request` or `response`. |
| `tool_call` | A tool was invoked. `data.phase` is `start` or `end`. |
| `message` | An informational message (e.g. CEL skip reason, forEach item cap). |
| `coordinator_narration` | Coordinator's `narrate(...)` tool call. |
| `coordinator_message` | Coordinator's `forward_to_agent(...)` tool call. |
| `coordinator_synthesis` | Coordinator's `finalize(summary=...)` tool call. |
| `coordinator_inbox_message` | Reverse reply from a resumed step landed in the coordinator's inbox. |
| `message_sent` | An agent or coord-side outbound message was queued. |
| `message_dropped` | A router message was dropped before delivery. `data.reason` carries the typed reason. |
| `agent_inbox_drain` | An agent drained one router message into its LLM conversation. |
| `agent_idle` | The agent finished a goai iteration with no unread mailbox messages. |
| `agent_wake` | The agent woke from idle to drain new messages. |
| `max_wake_cycles_warning` | Wake-cycle cap is at 80% of configured limit. |
| `resume_started` | Auto-resume goroutine spawned for a terminated step that received a new router message. |
| `resume_completed` | Resume run finished with a final response. |
| `resume_failed` | Resume could not complete (ctx cancel, transcript cap, agent error). |
| `resume_queued` | A resume attempt arrived while another for the same step was already in flight. |
| `transcript_sealed` | Transcript Append hit the size cap; subsequent appends for the step are suppressed. |
| `plan_ready` | `RunGoal` decomposition produced a workflow. `data.workflow` carries the parsed `*Workflow`. |
| `output` | Streaming agent output token. Set `delta`, `done`, optionally `reasoning`. |

The schema is **additive**: new event types may appear, existing types never reshape. Unknown `type` values are safe to skip.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success. |
| `1` | Step or workflow failure. |
| `2` | Validation or coordinator-side error. |
| `3` | Configuration or usage error (missing argument, unknown flag, no LLM provider). |
| `124` | Hard-watchdog timeout. Fires when `--timeout` was set and the process did not unwind within `timeout + 30s`; goroutine stacks are dumped to stderr. |

## When to use which mode

```
You have a YAML workflow file already.       → zenflow flow workflow.yaml
You have a free-form goal in a string.       → zenflow goal "<goal>"
You have a one-shot agent prompt.            → zenflow agent "<prompt>"

You want deterministic, repeatable runs.     → zenflow flow
You want the coordinator to plan on the fly. → zenflow goal
You want to chain steps, conditions, loops.  → flow or goal (both support full YAML)
You only need a single LLM call.             → zenflow agent

You need to embed in Go code.                → import github.com/zendev-sh/zenflow
You need to script around the binary.        → use --json output (see schema above)
```

## Embedding in Go

The same engine is exposed as a library:

```go
import (
  "context"

  "github.com/zendev-sh/zenflow"
  "github.com/zendev-sh/goai/google"
)

func run(ctx context.Context) error {
  llm, _ := google.New(ctx, "gemini-3-pro-preview")

  orch := zenflow.New(
    zenflow.WithModel(llm),
    zenflow.WithStorage(zenflow.NewMemoryStorage()),
  )
  defer orch.Close()

  wf, err := zenflow.LoadWorkflow("review.yaml")
  if err != nil { return err }

  result, err := orch.RunFlow(ctx, wf)
  _ = result
  return err
}
```

49 `With*` orchestrator options + 22 `WithRunner*` agent options cover provider config, concurrency, timeouts, mailbox tuning, transcript caps, isolation, observability, and resume policies. Full reference: <https://zenflow.sh/api>.

## Reference

- Full docs: <https://zenflow.sh>
- GitHub: <https://github.com/zendev-sh/zenflow>
- Spec: <https://zenflow.sh/yaml> and <https://github.com/zendev-sh/zenflow/blob/main/spec/v1/spec.md>
