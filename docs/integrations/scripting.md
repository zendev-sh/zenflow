---
title: Scripting
description: 'zenflow''s CLI is designed to drop into shell pipelines and child-process invocations. The two surfaces that matter for scripting are:'
---

# Scripting

zenflow's CLI is designed to drop into shell pipelines and child-process invocations. The two surfaces that matter for scripting are:

- **Exit code.** `0` on success, non-zero on failure (see [errors reference](../api/errors)).
- **`--json` flag.** Switches stdout from human-readable progress to NDJSON: one JSON event per line.

This page covers bash, Node.js, and Python patterns for invoking zenflow as a subprocess.

## Bash

The simplest case is "run the workflow, do something with the events":

```bash
#!/usr/bin/env bash
set -euo pipefail

# Run the workflow and pipe NDJSON through jq, line by line.
zenflow flow workflow.yaml --json | \
    jq -c '. | select(.type == "step_end" or .type == "error")' | \
    while IFS= read -r line; do
        type=$(echo "$line" | jq -r '.type')
        step=$(echo "$line" | jq -r '.stepId')
        if [[ "$type" == "error" ]]; then
            err=$(echo "$line" | jq -r '.error')
            echo "[FAILED] $step - $err"
        else
            dur=$(echo "$line" | jq -r '.duration')
            echo "[done] $step - $dur"
        fi
    done

# Capture the exit code of zenflow (not jq).
echo "Exit code: ${PIPESTATUS[0]}"
```

Two things to call out:

- **`PIPESTATUS[0]`** captures zenflow's exit code, not the last command in the pipeline. Without this, `set -e` plus a piped jq will hide a workflow failure because jq itself exited 0.
- **`set -euo pipefail`** makes the script fail loudly on the first error. `pipefail` in particular ensures a zenflow failure propagates through the pipe instead of being masked by jq's success.

If you need both stdout (events) and stderr (warnings/diagnostics), capture them separately:

```bash
zenflow flow workflow.yaml --json \
    > >(tee events.ndjson) \
    2> >(tee errors.log >&2)

zf_exit=$?
echo "zenflow exited $zf_exit"

if [[ $zf_exit -ne 0 ]]; then
    echo "Last 20 errors:"
    tail -n 20 errors.log
    exit "$zf_exit"
fi
```

Process substitution (`> >(...)`) is bash-specific - if you need POSIX-portable, redirect to files instead:

```bash
zenflow flow workflow.yaml --json > events.ndjson 2> errors.log
zf_exit=$?
```

### Filtering for specific events

The full event catalog is documented in [Output Formats](../cli/output-formats). The most useful selectors:

```bash
# Just step completions
jq 'select(.type == "step_end")' events.ndjson

# Failed steps only (failure is reported as a separate "error" event,
# not as a status field on step_end).
jq 'select(.type == "error")' events.ndjson

# Tokens used per step
jq -r 'select(.type == "step_end") |
    "\(.stepId)\t\(.tokens.TotalTokens // 0)"' events.ndjson

# The final summary (when a coordinator is running)
jq -r 'select(.type == "coordinator_synthesis") | .message' events.ndjson

# Any dropped messages (diagnostic - usually a bug or misconfig)
jq 'select(.type == "message_dropped")' events.ndjson
```

## Node.js

Use `child_process.spawn` for streaming, `child_process.spawnSync` for one-shot:

```js
import { spawn } from 'node:child_process';
import readline from 'node:readline';

function runWorkflow(yamlPath) {
    return new Promise((resolve, reject) => {
        const child = spawn('zenflow', ['flow', yamlPath, '--json'], {
            stdio: ['ignore', 'pipe', 'pipe'],
            env: process.env, // forward GEMINI_API_KEY etc.
        });

        const events = [];
        const errLines = [];

        // Parse stdout line-by-line as NDJSON.
        const rl = readline.createInterface({ input: child.stdout });
        rl.on('line', (line) => {
            if (!line.trim()) return;
            try {
                const evt = JSON.parse(line);
                events.push(evt);
                if (evt.type === 'step_end') {
                    console.log(`${evt.stepId} - ${evt.duration}`);
                } else if (evt.type === 'error') {
                    console.log(`FAILED: ${evt.stepId} - ${evt.error}`);
                }
            } catch (e) {
                console.warn('non-JSON line on stdout:', line);
            }
        });

        // Capture stderr separately - zenflow uses it for warnings,
        // not for the structured event stream.
        child.stderr.on('data', (chunk) => {
            errLines.push(chunk.toString());
        });

        child.on('close', (code, signal) => {
            const result = {
                exitCode: code,
                signal,
                events,
                stderr: errLines.join(''),
            };
            if (code === 0) resolve(result);
            else reject(Object.assign(new Error(`zenflow exit ${code}`), result));
        });

        child.on('error', reject); // spawn-level failure (binary not found)
    });
}

try {
    const { events } = await runWorkflow('./workflow.yaml');
    const final = events.find((e) => e.type === 'workflow_end');
    console.log('done:', final);
} catch (err) {
    console.error('zenflow failed:', err.exitCode, err.stderr);
    process.exit(err.exitCode ?? 1);
}
```

Key choices:

- **`spawn` not `exec`.** `exec` buffers all output in memory before resolving, which kills you on long-running workflows that produce thousands of events.
- **`readline` for NDJSON.** Each line is a complete JSON object. Don't try to `JSON.parse` the entire stdout buffer - it's not valid JSON, it's newline-delimited.
- **`stdio: ['ignore', 'pipe', 'pipe']`.** Closes stdin (zenflow doesn't read from it), pipes stdout and stderr separately so you can interleave them in your own logs.
- **Forward `process.env`.** Without `env: process.env`, the child inherits an empty environment and provider keys go missing.

If you don't need streaming (small workflows, batch jobs):

```js
import { spawnSync } from 'node:child_process';

const result = spawnSync('zenflow', ['flow', 'workflow.yaml', '--json'], {
    encoding: 'utf-8',
});

if (result.status !== 0) {
    throw new Error(`zenflow exit ${result.status}: ${result.stderr}`);
}

const events = result.stdout
    .split('\n')
    .filter(Boolean)
    .map((line) => JSON.parse(line));

console.log(`${events.length} events`);
```

`spawnSync` blocks until the binary exits and gives you everything at once. Fine for small workflows, do not use for anything multi-minute.

## Python

```python
#!/usr/bin/env python3
"""Run a zenflow workflow and process its event stream."""

import json
import subprocess
import sys
from pathlib import Path


def run_workflow(yaml_path: Path) -> tuple[int, list[dict], str]:
    """Run zenflow on yaml_path. Returns (exit_code, events, stderr)."""
    proc = subprocess.Popen(
        ["zenflow", "flow", str(yaml_path), "--json"],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        bufsize=1,  # line-buffered
    )

    events: list[dict] = []
    assert proc.stdout is not None
    try:
        for line in proc.stdout:
            line = line.strip()
            if not line:
                continue
            try:
                evt = json.loads(line)
            except json.JSONDecodeError:
                print(f"warn: non-JSON stdout line: {line!r}", file=sys.stderr)
                continue
            events.append(evt)
            if evt.get("type") == "step_end":
                print(f"{evt.get('stepId')} - {evt.get('duration')}")
            elif evt.get("type") == "error":
                print(f"FAILED: {evt.get('stepId')} - {evt.get('error')}")
    finally:
        proc.stdout.close()
        # stderr is captured all at once after the child exits.
        stderr = proc.stderr.read() if proc.stderr else ""
        if proc.stderr:
            proc.stderr.close()

    exit_code = proc.wait()
    return exit_code, events, stderr


def main() -> int:
    yaml = Path("workflow.yaml")
    if not yaml.exists():
        print(f"missing {yaml}", file=sys.stderr)
        return 2

    code, events, stderr = run_workflow(yaml)
    if code != 0:
        print(f"zenflow exited {code}", file=sys.stderr)
        if stderr:
            print(stderr, file=sys.stderr)
        return code

    end = next((e for e in events if e.get("type") == "workflow_end"), None)
    if end:
        print(f"done: duration={end.get('duration')}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
```

If you don't need streaming, `subprocess.run` is the simpler call:

```python
import json
import subprocess

result = subprocess.run(
    ["zenflow", "flow", "workflow.yaml", "--json"],
    capture_output=True,
    text=True,
    check=False,
)

if result.returncode != 0:
    raise RuntimeError(
        f"zenflow exit {result.returncode}: {result.stderr}"
    )

events = [json.loads(line) for line in result.stdout.splitlines() if line.strip()]
```

`check=False` lets you read `result.stderr` on failure. With `check=True`, the raised `CalledProcessError` does carry stderr but the flow is uglier.

## Reliability patterns

A handful of things worth doing in any of the three languages:

- **Set a timeout at the language level too.** zenflow's `--timeout` is one safety net; an OS-level timeout on the subprocess is another (`spawnSync` `timeout` option in Node, `subprocess.run(..., timeout=N)` in Python). If zenflow itself wedges (rare), the parent script can still recover.
- **Don't trust stdout to be NDJSON only.** A misconfigured environment can occasionally produce a non-JSON line (e.g., a Go runtime panic). Skip lines that don't parse instead of crashing the whole script.
- **Log stderr separately.** zenflow uses stderr for warnings and diagnostics. Mixing it into stdout breaks NDJSON parsing.
- **Buffer line by line, not chunk by chunk.** All three examples above use line-oriented parsing. Reading a 64K chunk and splitting on `\n` is fine for batch use, but breaks for interactive cases where you want each event delivered as it arrives.
- **Handle the `124` watchdog exit specifically.** If your script orchestrates retries, treat `124` (timeout) different from `1` (workflow failure) - retrying a timeout often makes sense, retrying a deterministic step failure usually doesn't.
- **Pin the binary path.** `which zenflow` at script start, then use the absolute path in `spawn`/`Popen`. Saves you from `$PATH` surprises across CI environments.

## When to use the Go API instead

If your script is itself a Go program, skip the subprocess dance entirely - import zenflow directly:

```go
import "github.com/zendev-sh/zenflow"

orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithProgress(myCustomSink), // structured access to the same events
)
defer orch.Close()

result, err := orch.RunFlow(ctx, wf)
```

You get the same events without NDJSON serialize/parse overhead, and you can build custom `ProgressSink` implementations that route events into your own structures. Full reference: [Go API](../api/core-functions).
