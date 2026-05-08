---
title: Flags
description: 'Every flag accepted by the zenflow CLI, grouped by purpose. Source of truth: cmd/zenflow/main.go.'
---

# Flags

Every flag accepted by the `zenflow` CLI, grouped by purpose. Source of truth: [`cmd/zenflow/main.go`](https://github.com/zendev-sh/zenflow/blob/main/cmd/zenflow/main.go).

The `Applies to` column lists the verbs that recognize the flag. Flags not listed against a verb are rejected by that verb's parser with exit code `3`.

## Provider and model

| Flag | Type | Default | Applies to | Description |
| --- | --- | --- | --- | --- |
| `--model MODEL` | string | `$ZENFLOW_MODEL` | flow, goal, agent | LLM identifier. Accepts `provider/model` (`google/gemini-2.5-flash`, `bedrock/anthropic.claude-sonnet-4-6`, `azure/DeepSeek-V3.2`, `azure-deployment/gpt-5`) or a bare name (auto-detected from env). When set on `flow`, this overrides every `agent.model` and `step.model` in the loaded YAML. |
| `--max-retries N` | integer | provider default | flow, goal, agent | Per-step cap on the agent runner's tool-call retry budget (passed via `goai.WithMaxRetries`). Distinct from `step.retries`, which retries the whole step on failure. When unset, [goai](https://goai.sh)'s built-in retry policy applies. `N >= 0`. `N = 0` disables retries. |
| `--thinking LEVEL` | enum | `off` | flow, goal, agent | Extended reasoning. Valid values: `off`, `low`, `medium`, `high`. Routed to provider-native keys (Bedrock `reasoningConfig`, Anthropic `thinking`, Google `thinkingConfig`, OpenAI/Azure `reasoning_effort`). Each provider reads only what it understands. |

## Lifecycle and execution

| Flag | Type | Default | Applies to | Description |
| --- | --- | --- | --- | --- |
| `--timeout DURATION` | Go duration | unset | flow, goal, agent | Hard timeout for the run. Format follows `time.ParseDuration` (`30s`, `5m`, `1h30m`). On expiry, the orchestrator context is cancelled; if the process does not unwind within `timeout + 30s`, the watchdog dumps goroutine stacks and exits `124`. |
| `--max-concurrency N` | integer | YAML `options.maxConcurrency` (`5`) | flow, goal | Cap on parallel steps. Used as a fallback when the workflow YAML omits `options.maxConcurrency`; the YAML value wins if set. `N >= 1`. |
| `--max-depth N` | integer | `3` | agent | Cap on nested agent-spawn depth. Hard ceiling on how deep an agent's `task`-style nested calls can recurse. Applies to: agent (controls turn-cap depth on spawned children). Flow/goal accept and forward the option, but currently no DAG step path consumes it. |
| `--max-turns N` | integer | unset | agent | Conversation turn cap for the single-agent loop. Without it, the agent runs until `end_turn` or `--timeout`. |
| `--workdir DIR` | path | cwd | flow, goal, agent | Sandbox directory. The CLI `chdir`s to this path before running so the default tool set (`bash`, `read`, `write`, `glob`, `grep`) is contained. Refuses to run if the path resolves into a zenflow source checkout. Must exist and be a directory. |
| `--resume RUN_ID` | string | unset | flow | Resume from a previous run's checkpoint at `~/.zenflow/runs/<RUN_ID>/`. The executor replays committed steps and restarts from the first incomplete step. Rejected by `goal` and `agent`. |

## Output and rendering

| Flag | Type | Default | Applies to | Description |
| --- | --- | --- | --- | --- |
| `--json` | bool | off | flow, goal, agent | Emit NDJSON events on stdout (one JSON object per line). Stable contract for shell consumers. Disables the banner. Coord narration / forward / finalize events are still included; combine with `--quiet` to drop them. |
| `--stream` | bool | off | flow, goal, agent | Stream agent text deltas as they arrive. In human mode, deltas appear under the `≋ [stepID]` prefix. In JSON mode, each delta is an `output` event with a `delta` field. Combine with `--verbose` to also stream reasoning deltas. |
| `--verbose` | bool | off | flow, goal, agent | Show agent reasoning headers (and bodies, when `--stream`), tool output bodies, and per-turn token counts. On `agent` without `--stream`, also prints the agent's full content at end. |
| `--quiet` | bool | off | flow, goal | Suppress coordinator narration. Show events only. The CLI installs `WithCoordinator(nil)`, which also saves the coordinator LLM cost. Rejected by `agent`. |
| `--summary-only` | bool | off | flow, goal | Skip per-step narration; show one final synthesis at the end. The coordinator is installed in `SynthesizeOnly` mode (no `narrate`, only `forward_to_agent` + `finalize`). Rejected by `agent`. |
| `--plan` | bool | off | flow | Print the DAG diagram before execution. Renders the topological order with dependency edges. JSON consumers receive the same data as a `plan_ready` event; the human-mode rendering happens in the stdout sink. |

## Debugging and observability

| Flag | Type | Default | Applies to | Description |
| --- | --- | --- | --- | --- |
| `--trace` | bool | off | flow, goal, agent | Enable OpenTelemetry tracing. By default, spans are exported as human-readable text to stderr. If `OTEL_EXPORTER_OTLP_ENDPOINT` is set, spans are exported via OTLP/HTTP to that endpoint instead (useful for Jaeger, Grafana Tempo, OTEL Collector, etc.). |

## Permissions and safety

These flags control which tools the agent is allowed to call. By default, zenflow gates unknown or potentially unsafe tools with an interactive confirmation prompt. The flags below override that gate for scripted and CI use.

| Flag | Type | Default | Applies to | Description |
| --- | --- | --- | --- | --- |
| `--sandbox` | bool | off | flow, goal, agent | Restrict tools to a curated safe set: `read`, `write`, `grep`, `glob`. `bash` is always blocked in sandbox mode, even when `--allow bash` is also passed (sandbox wins). Implies `--strict` (any tool not in the safe set is rejected without prompting). Mutually exclusive with `--yolo`; combining them exits with code `3`. Use this flag for automated CI runs where shell escape must be ruled out. |
| `--yolo` | bool | off | flow, goal, agent | Auto-approve all permission requests. No interactive prompt; every tool call is allowed. Mutually exclusive with `--sandbox`. |
| `--allow LIST` | string | unset | flow, goal, agent | Comma-separated list of tool names to allow without prompting (e.g., `bash,read`). Tools not in the list are still gated by the interactive prompt unless `--strict` or `--yolo` is also set. |
| `--deny LIST` | string | unset | flow, goal, agent | Comma-separated list of tool names that are always blocked, regardless of other flags. Cannot overlap with the sandbox safe set when `--sandbox` is active (exits `3`). |
| `--strict` | bool | off | flow, goal, agent | Reject any tool call whose tool name is not explicitly in the allow list. The interactive prompt is never shown; unlisted tools fail immediately. |

> **Tool names are case-sensitive.** Use lowercase: `--allow bash,read` (not `Bash` or `READ`). Built-in tools all use lowercase names (`bash`, `read`, `write`, `grep`, `glob`).

### Permission flag interactions

- **`--sandbox` and `--yolo` are mutually exclusive.** Combining them exits `3`.
- **`--sandbox` beats `--allow bash`.** Even if you pass `--allow bash`, sandbox mode removes `bash` from the merged allow list before the run starts.
- **`--sandbox` implies `--strict`.** You do not need to pass both; `--sandbox` enables strict mode automatically.
- **`--deny` cannot list sandbox defaults.** When `--sandbox` is active, listing any of `read`, `write`, `grep`, or `glob` in `--deny` is rejected with exit `3`, because the deny check fires before the allow check and would silently block tools the sandbox is designed to permit.
- **`--strict` without `--allow` blocks everything.** Use `--allow` together with `--strict` to produce a precise fixed allow list.
- **`--yolo` is mutually exclusive with `--allow`/`--deny`/`--strict`.** Combining any of these with `--yolo` exits `3` with the error `--yolo auto-approves every tool; remove --allow/--deny/--strict (or drop --yolo)`. The runtime would otherwise let `--yolo` silently win, masking the configured allow/deny/strict intent.

## Flag interactions

Some flags affect each other; the CLI's logic in `coordinatorOption` and `buildOrchestratorOpts` decides what the resulting orchestrator looks like.

- **`--quiet` beats `--summary-only`.** When both are set, the coordinator is disabled outright and no synthesis is emitted.
- **`--json` keeps the coordinator on.** JSON consumers get the full event stream (narration, forwards, finalize) so they can build dashboards or pipelines without re-implementing routing in Go. To get JSON without coordinator events, combine with `--quiet`.
- **`--plan` is honored on `flow` only.** `goal` accepts the flag (the parser does not reject it) but does not render a plan; treat it as a no-op on `goal`. The JSON surface emits a `plan_ready` event the moment the coordinator's decomposition is parsed.
- **`--workdir` and tool containment.** When `--workdir` is set, the default tool set is anchored to that path. When `--workdir` is unset, the tools are anchored to the current working directory at startup. Library users who want a permissive setup must configure tools explicitly via `zenflow.WithTools(...)`.
- **`--model` and YAML.** When `--model` is set on `flow`, every `agent.model` and `step.model` field in the loaded YAML is cleared, so the workflow runs entirely under the flag's model. This is intentional: it makes cross-provider testing a one-flag change.

## Environment variables

These complement (but never override) explicit flags:

| Variable | Purpose |
| --- | --- |
| `ZENFLOW_MODEL` | Default for `--model` when the flag is unset. |
| `GEMINI_API_KEY` / `GOOGLE_GENERATIVE_AI_API_KEY` | Credentials for Google direct API. Either name is accepted. |
| `AWS_ACCESS_KEY_ID` (and the rest of the AWS standard set) | Credentials for AWS Bedrock. |
| `AZURE_OPENAI_API_KEY` / `AZURE_RESOURCE_NAME` | Credentials and resource name for Azure OpenAI / Azure AI Services. |

If no provider env var is set and `--model` cannot be resolved, the CLI exits `3` with `"LLM provider not configured (use as library with WithModel)"`.

## See also

- [Commands](./commands) - per-verb synopsis and examples.
- [Output formats](./output-formats) - stdout shape and JSON event catalog.
