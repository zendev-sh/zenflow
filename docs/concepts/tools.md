---
title: Tools
description: Agents in zenflow do work through tools. A tool is a function the LLM can call - bash command, file read, HTTP request, anything. Tools turn an...
---

# Tools

Agents in zenflow do work through tools. A tool is a function the LLM can call - bash command, file read, HTTP request, anything. Tools turn an LLM from a chat surface into something that touches the world.

Zenflow distinguishes three kinds of tools:

1. **Built-in CLI tools** (`bash`, `read`, `write`, `glob`, `grep`) - shipped with the `zenflow` binary, available to YAML-declared agents in CLI runs.
2. **Library-supplied tools** - any `goai.Tool` you register via `WithTools`. The CLI is a consumer of this surface; embedded users supply their own.
3. **Auto-injected tools** - the executor adds `send_message`, `shared_memory_read`, `shared_memory_write`, `submit_result` automatically when the workflow uses messaging, shared memory, or structured output.

## Built-in CLI tools

The `zenflow` binary ships with a small CLI-only tool set in `cmd/zenflow/tool/`. They are not part of the zenflow library - only the CLI binary registers them. This split exists because the library has zero dependency on file-system or shell IO; everything that touches the host belongs in the CLI layer.

| Tool | What it does |
|------|--------------|
| `bash` | Run a shell command. Honours per-step timeout, captures stdout / stderr / exit code. |
| `read` | Read a file. Returns content as text. Respects the working directory configured by `--workdir` and per-step isolation. |
| `write` | Write content to a file. Creates parents as needed. |
| `glob` | List files matching a glob pattern. |
| `grep` | Search files matching a pattern; returns matches with file / line metadata. |

In a YAML workflow, refer to them by name in `tools:`:

```yaml
agents:
  developer:
    description: "Backend developer."
    tools: [bash, read, write, glob, grep]
```

Omit the `tools` field to include all of them.

These tools follow conservative defaults. Bash respects per-step timeouts and process-group cleanup. Read / write are bounded by the work directory the isolation layer hands out. Grep is a regex search, not a shell-out to `grep(1)` - it is portable across platforms.

For full flag references (`bash` working directory, `grep` flags, `read` byte limits), see [CLI tools reference](/cli/) and the source under `cmd/zenflow/tool/`.

## Library-supplied tools

When using zenflow as a Go library, you register tools via `WithTools`. A tool is just a `goai.Tool`:

```go
import (
    "context"
    "encoding/json"

    "github.com/zendev-sh/goai"
    "github.com/zendev-sh/zenflow"
)

httpGet := goai.Tool{
    Name:        "http_get",
    Description: "Fetch a URL and return the response body.",
    InputSchema: json.RawMessage(`{
        "type": "object",
        "required": ["url"],
        "properties": {
            "url": {"type": "string"}
        }
    }`),
    Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
        var args struct {
            URL string `json:"url"`
        }
        if err := json.Unmarshal(raw, &args); err != nil {
            return "", err
        }
        // ... do the HTTP call, return the body or an error string.
        return body, nil
    },
}

orch := zenflow.New(
    zenflow.WithModel(llm),
    zenflow.WithTools(httpGet, otherTool, ...),
)
```

The same tool surface [goai](https://goai.sh) uses everywhere. Zenflow does not wrap or extend `goai.Tool` - what works in [goai](https://goai.sh) works in zenflow. See [goai tools docs](https://goai.sh) for the complete API.

## Tool filtering

Each agent's effective tool set is `(allowlist) - (denylist)`:

- Omit `tools` - every registered tool is available to the agent.
- `tools: [a, b]` - only `a` and `b`.
- `disallowedTools: [bash]` - removed from the resolved allowlist.

The executor resolves the names against the orchestrator's tool catalogue (the slice you passed to `WithTools`). Names that do not match any registered tool surface as a load-time error.

## Auto-injected tools

Three tool families the executor adds automatically based on what the workflow uses:

### `submit_result`

Added when an agent has a `resultSchema`. The tool's input schema is the agent's `resultSchema`. The agent calls it to produce structured `result`. See [Structured output](/concepts/structured-output).

### `send_message`

`send_message` is auto-injected on every step runner that has a MessageRouter AND is not the coordinator itself (detection: presence of `forward_to_agent` in the runner's tool list marks the coordinator). Step runners that already have a `send_message` tool keep their own - no overwrite. Lets the agent push a message to the coordinator's mailbox. See [Messaging](/concepts/messaging).

The coordinator is auto-installed on the CLI path; library users opt in via `WithCoordinator`.

### `shared_memory_read` / `shared_memory_write`

Added when `WithSharedMemory(sm)` is set on the orchestrator. Lets agents read and write the namespaced key/value store. See [Shared memory](/concepts/shared-memory).

## Permission gate

`WithPermissions(handler)` installs a hook that runs before every tool call:

```go
type PermissionHandler interface {
    RequestPermission(ctx context.Context, req PermissionRequest) (bool, error)
}

type PermissionRequest struct {
    RunID    string
    StepID   string
    ToolName string
    ToolArgs json.RawMessage
}
```

The handler returns `true` to allow, `false` to deny, or an error. Denied tool calls return a tool result indicating denial, and the agent sees that in its conversation. Errors fail the step.

Use cases:

- **CLI confirmation.** The default CLI implementation prompts the user before EVERY tool call (no automatic allowlist). Use `--yolo` to skip prompts entirely, `--sandbox` to allow only the safe read-only set (`read`, `write`, `grep`, `glob`) without prompting and block `bash`, or `--allow tool1,tool2` to whitelist specific tools.
- **Allow-list policies.** A handler that blocks `bash` calls containing `sudo`, or `write` to certain paths.
- **Audit logging.** A handler that records every tool call to a log before allowing it.

The handler runs synchronously in the agent's loop. Keep it fast - a slow handler stalls the LLM.

## MCP tools via goai

[Model Context Protocol](https://modelcontextprotocol.io) servers expose tool catalogues over a standard wire format. Goai includes an MCP client that converts MCP tools into `goai.Tool` values. They register with `WithTools` like any other tool:

```go
import "github.com/zendev-sh/goai/mcp"

client := mcp.NewClient("zenflow", "1.0.0")
if err := client.Connect(ctx); err != nil {
    return err
}
defer client.Close()

result, err := client.ListTools(ctx, nil)
if err != nil {
    return err
}
mcpTools := mcp.ConvertTools(client, result.Tools)

orch := zenflow.New(
    zenflow.WithModel(llm),
    zenflow.WithTools(mcpTools...),
)
```

The agent calls them by their MCP-declared names. Zenflow does not need to know they came from MCP - they look like any other `goai.Tool`. See [goai MCP docs](https://goai.sh) for full setup and authentication options.

## Tool execution and side effects

Tools execute in the same process as the agent, on a goroutine the executor spawned for the step. Tool side effects (file writes, HTTP requests, mutations) are not transactional or rolled back if the step later fails or retries. If your tool has side effects you care about under retry, design the tool to be idempotent (write to a unique path per call, use ETags / If-Match for HTTP, etc.).

Step isolation can give you a fresh working directory per step, which limits some classes of cross-step interference. See [Step isolation](/concepts/step-isolation).

## Tool budget and `maxTurns`

Each tool call counts as part of a turn. The agent's `maxTurns` cap bounds total LLM round trips, not tool calls per round trip. An agent can call ten tools in one turn. Per turn, the executor runs tools in parallel where the LLM emitted them in parallel.

If a tool call exceeds the step's `timeout`, the surrounding step transitions to `failed` (the tool's context is cancelled). Tools that do not honour `ctx.Done()` will run past cancellation and the step waits - design tools to honour context.

## Tool result format

A tool's `Execute` returns a `string`. That string is what the LLM sees as the tool's "result" in its conversation. For structured tool output (e.g. JSON), serialise to JSON in `Execute` and have the agent parse it. Goai does not enforce structure on tool outputs - the LLM treats them as opaque text.

Errors from `Execute` (returning a non-nil `error`) surface to the LLM as a synthesised error result like `"error: ..."`. Most LLMs handle this naturally - they see the failure, retry with different arguments, or give up.

## Cross-links

- [Agents](/concepts/agents) - tool allowlists and denylists per agent
- [Structured output](/concepts/structured-output) - the auto-injected `submit_result`
- [Messaging](/concepts/messaging) - the auto-injected `send_message`
- [Shared memory](/concepts/shared-memory) - the auto-injected `shared_memory_*`
- [API: Options](/api/options) - `WithTools`, `WithPermissions`
- [goai tool docs](https://goai.sh) - the underlying `goai.Tool` API
