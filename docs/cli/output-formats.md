---
title: Output formats
description: 'zenflow produces two output shapes:'
---

# Output formats

`zenflow` produces two output shapes:

- **Stdout (human-readable).** The default. Colored, glyphed, one event per line. Source: [`sink/stdout.go`](https://github.com/zendev-sh/zenflow/blob/main/sink/stdout.go).
- **NDJSON event stream.** Selected with `--json`. One JSON object per line. Source: [`sink/json.go`](https://github.com/zendev-sh/zenflow/blob/main/sink/json.go).

A third mode, `--stream`, layers token-by-token agent text onto either of the above.

## Human-readable stdout

When stdout is a terminal and `--json` is not set, events render with a glyph + bracketed step ID + message format. The renderer maintains stream state (open agent text blocks, reasoning sections) so events line up cleanly even when text is streaming.

A typical successful run produces something like this:

```text
≋≋≋ zenflow - let agents flow ≋≋≋

▸ Starting workflow: code-review
▸ Step 1/4: design (architect)
≋ [coordinator] Starting design phase.
✓ ◇ [design] read (docs/spec.md) (12.4ms)
✓ [design] completed (8.31s)
▸ Step 2/4: implement (coder)
≋ [coordinator] Implementing the design.
✓ ⚙ [implement] bash (go build ./...) (1.92s)
✓ [implement] completed (45.10s)
▸ Step 3/4: review (reviewer)
✓ [review] completed (12.45s)
▸ Step 4/4: finalize (architect)
✓ [finalize] completed (4.12s)
≋ [code-review] Summary: All checks passed. Ready to merge.
✓ [code-review] completed (1m10.88s)

── Final answer ─────────────────────
All checks passed. Ready to merge.
─────────────────────────────────────
Run ID: 2026-05-03T14-30-00-abc123
```

### Glyph legend

| Glyph | Meaning |
| --- | --- |
| `▸` | Workflow / step start |
| `✓` | Step success / tool success |
| `×` | Tool failure |
| `✗` | Step error |
| `○` | Step skipped (or expected drop at shutdown) |
| `≋` | Coordinator narration / synthesis / agent response |
| `⇠` | Outbound message (sender's bracket) |
| `⇢` | Inbound message (receiver's bracket) |
| `↻` | Agent wake (resumed processing) |
| `↺` | Step resumed |
| `·` | Agent idle |
| `◎` | Reasoning / thinking header |
| `Σ` | Per-turn token summary (`--verbose`) |
| `⚠` | Warning |
| `⋯` | Resume queued |
| `⊞ ⊙ ⛶ ⇄ ✎ ✐ ◇ ⚙ ✦ ◆` | Tool icons (`ls`, `grep`, `glob`, `fetch`, `edit`, `write`, `read`, `bash`, `task`, fallback) |

### Streaming under `--stream`

Agent text deltas open under the same `≋ [stepID]` prefix used for narration. Reasoning deltas are gated behind `--verbose`. The stream is closed (a newline emitted) when the model finishes the response or the next event arrives, so events never glue onto a half-printed line.

```text
≋ [implement] func ReverseString(s string) string {
    runes := []rune(s)
    ...
}
✓ [implement] completed (12.4s)
```

When stdout is not a tty (piped, redirected), the renderer still produces the same lines but drops ANSI color escapes. The format is otherwise identical, which keeps grep-friendly pipelines simple.

## NDJSON event stream

`--json` switches the sink to `JSONSink`. Every event becomes one JSON object on its own line.

### Common envelope

Every event has these fields:

```json
{
  "type": "<event-type>",
  "timestamp": "2026-05-03T14:30:00.123456789Z"
}
```

Optional fields, included when populated:

| Field | Type | When |
| --- | --- | --- |
| `runId` | string | All events from a `flow` / `goal` run. |
| `stepId` | string | Step-scoped events. |
| `agent` | string | Step events that resolve to a named agent. |
| `message` | string | Narration, summary, errors. |
| `duration` | string (Go duration) | Step / workflow / tool end events. |
| `tokens` | object | Per-turn token usage (`--verbose`). |
| `error` | string | Error events. |
| `data` | object | Event-specific payload (see below). |

### Event-by-event capture

A small workflow run looks like this on the wire (one event per line, pretty-printed for readability):

```jsonl
{"type":"workflow_start","runId":"2026-05-03T14-30-00-abc","timestamp":"2026-05-03T14:30:00Z","message":"code-review"}
{"type":"plan_ready","runId":"2026-05-03T14-30-00-abc","timestamp":"2026-05-03T14:30:00Z","data":{"workflow":{"name":"code-review","steps":["design","implement","review","finalize"]}}}
{"type":"step_start","runId":"2026-05-03T14-30-00-abc","stepId":"design","agent":"architect","timestamp":"2026-05-03T14:30:01Z","data":{"index":0,"total":4}}
{"type":"coordinator_narration","runId":"2026-05-03T14-30-00-abc","stepId":"coordinator","timestamp":"2026-05-03T14:30:01Z","message":"Starting design phase."}
{"type":"tool_call","runId":"2026-05-03T14-30-00-abc","stepId":"design","timestamp":"2026-05-03T14:30:03Z","data":{"phase":"end","tool_name":"read","input":"{\"file_path\":\"docs/spec.md\"}","output":"# Spec...","duration":"12.4ms"}}
{"type":"step_end","runId":"2026-05-03T14-30-00-abc","stepId":"design","timestamp":"2026-05-03T14:30:09Z","duration":"8.31s"}
{"type":"step_start","runId":"2026-05-03T14-30-00-abc","stepId":"implement","agent":"coder","timestamp":"2026-05-03T14:30:09Z","data":{"index":1,"total":4}}
{"type":"output","runId":"2026-05-03T14-30-00-abc","stepId":"implement","delta":"func Reverse","done":false}
{"type":"output","runId":"2026-05-03T14-30-00-abc","stepId":"implement","delta":"String(s string) string {","done":false}
{"type":"output","runId":"2026-05-03T14-30-00-abc","stepId":"implement","delta":"","done":true}
{"type":"step_end","runId":"2026-05-03T14-30-00-abc","stepId":"implement","timestamp":"2026-05-03T14:30:54Z","duration":"45.10s"}
{"type":"coordinator_synthesis","runId":"2026-05-03T14-30-00-abc","timestamp":"2026-05-03T14:31:10Z","message":"All checks passed. Ready to merge."}
{"type":"workflow_end","runId":"2026-05-03T14-30-00-abc","timestamp":"2026-05-03T14:31:11Z","duration":"1m10.88s"}
```

### Event type catalog

| Type | Description |
| --- | --- |
| `workflow_start` | Workflow begun. `message` is the workflow name. |
| `workflow_end` | Workflow finished. `duration` set. |
| `plan_ready` | Loaded workflow ready to execute. `data.workflow` is the parsed workflow. |
| `step_start` | Step begun. `data.index` and `data.total` are the topological position. |
| `step_end` | Step completed successfully. |
| `step_skipped` | Step skipped because its `condition` evaluated false (or a dependency upstream skipped under `skip-dependents`). |
| `message` | MessageRouter-message delivery event; informational message attached to a step or run (e.g., child agent model warning). |
| `error` | Error scoped to a step or the workflow. |
| `tool_call` | Tool invocation. `data.phase` is `start` or `end`; the human sink prints only `end`. JSON consumers see both. |
| `agent_turn` | Per-turn token summary. Only when `--verbose` is on. |
| `coordinator_narration` | Coordinator-emitted progress note. |
| `coordinator_message` | Coordinator-issued forward. |
| `coordinator_inbox_message` | Reverse reply drained from the coordinator's inbox. |
| `coordinator_synthesis` | Final summary from the coordinator's `finalize` call. |
| `agent_inbox_drain` | Step agent received a coordinator-routed message. |
| `agent_idle` | Agent finished a [goai](https://goai.sh) iteration with no unread messages. |
| `agent_wake` | Agent re-entered [goai](https://goai.sh) after draining N messages. `data.message_count` and `data.cycle` set. |
| `max_wake_cycles_warning` | Emitted at 80% of the wake-cycles cap. |
| `message_sent` | Outbound messaging event (paired with the recipient's `agent_inbox_drain`). |
| `message_dropped` | MessageRouter-side or workflow-abort drop. `data.reason` is load-bearing. |
| `resume_started` | A step was resumed by an inbound message. |
| `resume_queued` | A resume message was appended to an in-flight resume's mailbox. |
| `resume_completed` | Resume run finished. |
| `resume_failed` | Resume run failed. `data.reason` set. |
| `transcript_sealed` | Transcript store hit its cap; further appends are silently suppressed. |
| `output` | Streaming agent text delta. `delta` and `done` fields. `reasoning: true` distinguishes thinking deltas. |

The contract for `--json` is **additive**: new fields and new event types may appear in future versions; existing fields and event shapes do not change. Consumers should ignore unknown event types and unknown fields.

## Streaming events under `--stream`

When `--stream` is on, the JSON sink emits an `output` event per token batch. Every `output` event carries `runId` + `stepId` (the snippet below abbreviates `runId` as `<runId>`; see the canonical example on line 121 above):

```jsonl
{"type":"output","runId":"<runId>","stepId":"implement","delta":"func Reverse","done":false}
{"type":"output","runId":"<runId>","stepId":"implement","delta":"String(s string) string {","done":false}
{"type":"output","runId":"<runId>","stepId":"implement","reasoning":true,"delta":"I should think about edge cases first.","done":false}
{"type":"output","runId":"<runId>","stepId":"implement","delta":"","done":true}
```

`reasoning: true` is set for thinking deltas; absent (or `false`) for primary agent text. The final delta of a stream has `done: true` (and may have an empty `delta`).

## Filtering with `jq`

A few useful one-liners:

```bash
# List every step end with duration
zenflow flow workflow.yaml --json --model gemini-2.5-flash \
  | jq -c 'select(.type == "step_end") | {stepId, duration}'

# Watch only tool failures
zenflow flow workflow.yaml --json --model gemini-2.5-flash \
  | jq 'select(.type == "tool_call" and .error != null)'

# Tally tokens per step
zenflow flow workflow.yaml --json --verbose --model gemini-2.5-flash \
  | jq -s 'map(select(.type == "agent_turn")) | group_by(.stepId)
           | map({stepId: .[0].stepId, in: (map(.tokens.InputTokens) | add),
                  out: (map(.tokens.OutputTokens) | add)})'

# Capture only the final synthesis
zenflow flow workflow.yaml --json --model gemini-2.5-flash \
  | jq -r 'select(.type == "coordinator_synthesis") | .message'
```

## Banner

When stdout is a terminal and `--json` is not in the args, the binary emits a single banner line before any events:

```text
≋≋≋ zenflow - let agents flow ≋≋≋
```

Pipes, redirects, and `--json` runs skip the banner so it does not corrupt machine-readable output.
