---
title: Commands
description: 'zenflow dispatches on its first positional argument. Five verbs are wired:'
---

# Commands

`zenflow` dispatches on its first positional argument. Five verbs are wired:

- [`zenflow validate`](#zenflow-validate) - parse and validate a YAML workflow.
- [`zenflow plan`](#zenflow-plan) - print the execution plan (DAG order).
- [`zenflow flow`](#zenflow-flow) - load and execute a YAML workflow.
- [`zenflow goal`](#zenflow-goal) - have the LLM coordinator decompose a goal into a workflow, then run it.
- [`zenflow agent`](#zenflow-agent) - run a single-agent conversation.

Source of truth for flags and exit codes: [`cmd/zenflow/main.go`](https://github.com/zendev-sh/zenflow/blob/main/cmd/zenflow/main.go).

## Exit codes

Every command uses the same exit code table:

| Code | Meaning |
| --- | --- |
| `0` | Success. |
| `1` | Step or workflow failure. |
| `2` | Validation or coordinator-side error. |
| `3` | Configuration or usage error (missing argument, unknown flag, no LLM provider). |
| `124` | Hard-watchdog timeout. Fires when `--timeout` was set and the process did not unwind within `timeout + 30s`; goroutine stacks are dumped to stderr. |

## `zenflow validate`

### Synopsis

```text
zenflow validate <file>
```

### Description

Loads the workflow, runs JSON Schema validation, then runs the validator-only rules (cycle detection, referential integrity for `agent`/`dependsOn`/`untilAgent`, `include` mutual exclusion, and the `untilAgent.resultSchema` shape check). Prints `✓ Valid` on success.

No LLM calls. No file system writes. Safe to run in CI.

### Flags

None.

### Examples

```bash
zenflow validate workflow.yaml
# ✓ Valid

zenflow validate broken.yaml
# ✗ step "deploy" depends on unknown step "build"
echo $?
# 2
```

## `zenflow plan`

### Synopsis

```text
zenflow plan <file>
```

### Description

Loads and validates the workflow, then prints a DAG diagram in topological order. The diagram surfaces step dependencies, agents, and loop blocks at a glance. No LLM calls.

### Flags

None.

### Examples

```bash
zenflow plan workflow.yaml
```

Sample output:

```text
[design]    architect   →  api-server  database
[api-server] backend     →  integrate
[database]  backend      →  integrate
[integrate] integrator
```

## `zenflow flow`

### Synopsis

```text
zenflow flow <file> [<context>] [flags]
```

### Description

Loads the workflow and executes the DAG. Auto-resolves the LLM provider from `--model` + env vars; wires the default tool registry (`bash`, `read`, `write`, `glob`, `grep`) sandboxed to `--workdir` (or the current directory if `--workdir` is unset).

Optional `<context>` is a single positional after the file path that gets distributed to every step as flow-level context (delivered via the orchestrator's `WithFlowContext` option). It is convenient when one short string applies to the whole run.

When `--model` is set, the CLI overrides every `agent.model` and `step.model` in the loaded YAML with the flag's value. This makes cross-provider testing a one-flag change.

If `--resume RUN_ID` is supplied, the executor reattaches to the file-backed run state in `~/.zenflow/runs/<RUN_ID>/`, replays committed steps, and resumes from the first incomplete step.

When the run finishes, `zenflow flow` prints a "Final answer" block with either the coordinator's synthesis (if it called `finalize`) or the last topological step's content. The block is suppressed under `--json`, `--quiet`, or `--summary-only`.

### Flags

| Flag | Type | Default | Notes |
| --- | --- | --- | --- |
| `--model MODEL` | string | `$ZENFLOW_MODEL` | LLM model. `provider/model` or bare name. |
| `--timeout DURATION` | Go duration | unset | Hard timeout for the run. |
| `--max-concurrency N` | int | YAML default | Cap on parallel steps. |
| `--max-depth N` | int | `3` | Cap on nested agent-spawn depth. |
| `--max-retries N` | int | provider default | Override [goai](https://goai.sh) retry budget. |
| `--workdir DIR` | path | cwd | Sandbox directory. Refuses to run inside the zenflow source tree. |
| `--resume RUN_ID` | string | unset | Resume from `~/.zenflow/runs/<RUN_ID>/`. |
| `--json` | bool | off | NDJSON event stream on stdout. |
| `--stream` | bool | off | Stream agent text deltas. |
| `--plan` | bool | off | Print DAG before execution. |
| `--quiet` | bool | off | No narration. Events only. |
| `--summary-only` | bool | off | Skip per-step narration; show one final synthesis. |
| `--verbose` | bool | off | Show thinking, tool outputs, per-turn token counts. |
| `--trace` | bool | off | Enable OTel tracing. |
| `--thinking LEVEL` | enum | `off` | `off`, `low`, `medium`, `high`. |
| `--sandbox` | bool | off | Restrict tools to a safe set (`read`, `write`, `grep`, `glob`); blocks `bash` even when `--allow bash` is also set. Implies `--strict`. Mutually exclusive with `--yolo`. |
| `--yolo` | bool | off | Auto-approve all permission requests; no interactive prompt. Mutually exclusive with `--sandbox`. |
| `--allow LIST` | string | unset | Comma-separated list of tool names to allow without prompting (e.g., `bash,read`). |
| `--deny LIST` | string | unset | Comma-separated list of tool names to always block. |
| `--strict` | bool | off | Reject any tool not explicitly in the allow list. |

### Examples

Run a workflow against Gemini:

```bash
zenflow flow workflow.yaml --model google/gemini-2.5-flash
```

Run with a one-shot context string:

```bash
zenflow flow workflow.yaml "topic: distributed systems" --model bedrock/anthropic.claude-sonnet-4-6
```

Stream output and view the DAG up front:

```bash
zenflow flow workflow.yaml --model gemini-2.5-flash --plan --stream
```

Pipe NDJSON events to `jq` for ad-hoc filtering:

```bash
zenflow flow workflow.yaml --json --model gemini-2.5-flash \
  | jq -c 'select(.type == "step_end") | {stepId, duration}'
```

Resume a previously interrupted run:

```bash
zenflow flow workflow.yaml --resume 2026-05-03T14-30-00-abc123 --model gemini-2.5-flash
```

Sandbox tool execution to a scratch dir:

```bash
zenflow flow workflow.yaml --model gemini-2.5-flash --workdir "$(mktemp -d)"
```

## `zenflow goal`

### Synopsis

```text
zenflow goal <goal-text> [<extra-context>] [flags]
```

### Description

Asks the LLM coordinator to decompose `<goal-text>` into a workflow on the fly, then runs it. This is "no YAML required" mode: useful for experimentation, quick triage, and shell-driven automation.

The optional `<extra-context>` positional appends to the decomposition prompt (delivered via `WithGoalContext`). Piped stdin is also appended to the goal text - convenient for handing in a file, a curl response, or a previous command's output.

The coordinator emits a JSON workflow document (matching the v1 schema), validates it, then executes it the same way `zenflow flow` does. If the coordinator's JSON fails to parse or fails validation, the command exits `2` with the underlying error. Step failures still exit `1`.

### Flags

Same as [`zenflow flow`](#zenflow-flow), with two exceptions:

- No `--resume`. (`--resume` is `flow`-only.)
- No `--plan`. The decomposed workflow surfaces via the normal narration / event stream.

### Examples

Decompose a one-line goal:

```bash
zenflow goal "audit my Go module for breaking API changes" --model gemini-2.5-flash
```

Append piped stdin as additional context:

```bash
go list -m all | zenflow goal "summarize my dependency tree" --model gemini-2.5-flash
```

Pass extra context inline:

```bash
zenflow goal "write a release-notes draft" "version: 1.4.0, repo: zenflow" \
  --model bedrock/anthropic.claude-sonnet-4-6
```

Quiet mode for shell-driven automation:

```bash
zenflow goal "produce a JSON list of files changed since v1.0" \
  --json --quiet --model gemini-2.5-flash
```

## `zenflow agent`

### Synopsis

```text
zenflow agent <prompt> [flags]
```

### Description

Runs a single-agent conversation. No DAG, no coordinator, no narration - just one agent loop with the default tool registry. Useful as a Codex/Aider-style ad-hoc agent invocation.

Piped stdin is appended to the prompt, so this works as a shell filter:

```bash
cat README.md | zenflow agent "summarize this in 3 bullets" --model gemini-2.5-flash
```

Several `flow` / `goal` flags do not apply and are rejected: `--quiet`, `--summary-only`, `--resume`, `--plan`. The coordinator is never installed (single-agent mode has no coordinator semantics).

### Flags

| Flag | Type | Default | Notes |
| --- | --- | --- | --- |
| `--model MODEL` | string | `$ZENFLOW_MODEL` | LLM model. |
| `--max-turns N` | int | unset | Conversation turn cap. |
| `--max-depth N` | int | `3` | Cap on nested agent-spawn depth. |
| `--workdir DIR` | path | cwd | Sandbox directory. |
| `--timeout DURATION` | Go duration | unset | Hard timeout. |
| `--json` | bool | off | NDJSON event stream. |
| `--stream` | bool | off | Stream agent text deltas. |
| `--verbose` | bool | off | Show thinking, tool output, per-turn token counts. Without `--stream`, also prints the full agent content at end. |
| `--trace` | bool | off | Enable OTel tracing. |
| `--thinking LEVEL` | enum | `off` | `off`, `low`, `medium`, `high`. |
| `--max-retries N` | int | provider default | Override [goai](https://goai.sh) retry budget. |
| `--max-concurrency N` | int | unset | Accepted for shape; not meaningful in single-agent mode. |
| `--sandbox` | bool | off | Restrict tools to a safe set (`read`, `write`, `grep`, `glob`); blocks `bash` even when `--allow bash` is also set. Implies `--strict`. Mutually exclusive with `--yolo`. |
| `--yolo` | bool | off | Auto-approve all permission requests without prompting. Mutually exclusive with `--sandbox`. |
| `--allow LIST` | string | unset | Comma-separated allow-list of tool names. |
| `--deny LIST` | string | unset | Comma-separated deny-list of tool names. |
| `--strict` | bool | off | Reject any tool not explicitly in the allow list. |

### Examples

Quick one-off prompt:

```bash
zenflow agent "what does runtime.GOMAXPROCS do?" --model gemini-2.5-flash
```

Stream the answer:

```bash
zenflow agent "write a Go function that reverses a string" \
  --stream --verbose --model bedrock/anthropic.claude-sonnet-4-6
```

Cap to a few turns and print verbose output:

```bash
zenflow agent "find todos in this repo" \
  --max-turns 8 --verbose --model gemini-2.5-flash
```

NDJSON for pipelines:

```bash
zenflow agent "list all .go files" --json --model gemini-2.5-flash \
  | jq -c 'select(.type == "tool_call")'
```

## Banner and tty detection

When stdout is a terminal and `--json` is not present, the binary prints a one-line banner:

```text
≋≋≋ zenflow - let agents flow ≋≋≋
```

Piped or `--json` runs skip the banner so it does not pollute output.

## Signal handling

`zenflow flow` and `zenflow goal` install a platform-aware signal handler. On POSIX systems, `SIGINT`, `SIGTERM`, and `SIGHUP` cancel the orchestrator context, letting steps unwind gracefully. On Windows, only `os.Interrupt` is wired.

If `--timeout` is set, the watchdog enforces a hard deadline of `timeout + 30s`. Past that, the process dumps all goroutine stacks to stderr and exits `124`.

## See also

- [Flags](./flags) - flat table of every flag with type, default, and applies-to.
- [Output formats](./output-formats) - stdout shape and JSON event catalog.
- [Resume](/concepts/resume) - run state, checkpoints, and replay semantics.
