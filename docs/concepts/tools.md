---
title: Tools
description: Agents in zenflow do work through tools. A tool is a function the LLM can call - bash command, file read, HTTP request, anything. Tools turn an...
---

# Tools

Agents in zenflow do work through tools. A tool is a function the LLM can call - bash command, file read, HTTP request, anything. Tools turn an LLM from a chat surface into something that touches the world.

Zenflow distinguishes four kinds of tools:

1. **Built-in CLI tools** (`bash`, `read`, `write`, `glob`, `grep`) - shipped with the `zenflow` binary, available to YAML-declared agents in CLI runs.
2. **Library-supplied tools** - any `goai.Tool` you register via `WithTools`. The CLI is a consumer of this surface; embedded users supply their own.
3. **MCP tools** - tools discovered from [Model Context Protocol](https://modelcontextprotocol.io) servers declared in `settings.json`. zenflow connects and exposes them natively - no Go code. See [MCP servers](/integrations/mcp).
4. **Auto-injected tools** - the executor adds `send_message`, `shared_memory_read`, `shared_memory_write`, `submit_result` automatically when the workflow uses messaging, shared memory, or structured output.

## Tool steps

A **tool step** invokes a registered goai tool directly from the workflow YAML, without an LLM call. This is useful when you need a precise, deterministic action - reading a file whose path was discovered by a prior agent, running a shell command with a computed argument - without the overhead or non-determinism of an LLM turn.

Set `tool` to the tool name and optionally supply `toolInput` with input fields:

```yaml
- id: find-config
  agent: analyst
  instructions: Find the path to the main config file and return it.

- id: read-config
  dependsOn: [find-config]
  tool: read
  toolInput:
    path: $steps["find-config"].content

- id: review
  dependsOn: [read-config]
  agent: analyst
  instructions: Review the config and suggest improvements.
```

**How it differs from agent steps**: agent steps run an LLM conversation loop that may call multiple tools across multiple turns. A tool step calls exactly one tool once and stores the return string as the step's `content`. No model is involved; no `agent` reference is needed; `result` is always nil.

**CEL in `toolInput`**: any string value in `toolInput` that starts with `$` is evaluated as a CEL expression returning a string. The `$` prefix marks the boundary. CEL variables available: `steps["step-id"].content`, `steps["step-id"].result`, `steps["step-id"].status`. Note the bracket notation - step IDs can contain hyphens, which CEL dot-access cannot handle. To pass a literal value that starts with `$` (e.g. `$HOME`), double it: `$$HOME` is not evaluated and collapses to `$HOME`.

**Mutual exclusion**: `tool` is mutually exclusive with `agent`, `instructions`, `loop`, `include`, `contextFiles`, and `model`. `toolInput` requires `tool` to be set. Retry, timeout, condition, and depends_on work identically to agent steps.

For the full field reference see [Step / tool and toolInput](/yaml/step#tool).

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

When using zenflow as a Go library, you register tools via `WithTools`. A tool is just a `goai.Tool`. The simplest way to build one is `goai.NewTool`, which generates the JSON Schema from your input struct and unmarshals arguments for you:

```go
import (
    "context"

    "github.com/zendev-sh/goai"
    "github.com/zendev-sh/zenflow"
)

httpGet := goai.NewTool("http_get",
    "Fetch a URL and return the response body.",
    func(ctx context.Context, args struct {
        URL string `json:"url" jsonschema:"description=The URL to fetch"`
    }) (string, error) {
        // ... do the HTTP call, return the body or an error string.
        return body, nil
    })

orch := zenflow.New(
    zenflow.WithModel(llm),
    zenflow.WithTools(httpGet, otherTool, ...),
)
```

`NewTool` derives a strict-mode schema: every field of the input struct is `required` and `additionalProperties` is `false`. Use the `jsonschema` struct tag for per-field `description=...` and `enum=a|b|c` (descriptions must not contain commas - the tag parser uses commas to separate `description` from `enum`). For a schema shape `NewTool` cannot express, fall back to the raw `goai.Tool{Name, Description, InputSchema, Execute}` struct literal with a hand-written `InputSchema`.

The same tool surface [goai](https://goai.sh) uses everywhere. Zenflow does not wrap or extend `goai.Tool` - what works in [goai](https://goai.sh) works in zenflow. See [goai tools docs](https://goai.sh) for the complete API.

## Tool filtering

Each agent's effective tool set is `(allowlist) - (denylist)`:

- Omit `tools` - every registered tool is available to the agent.
- `tools: [a, b]` - only `a` and `b`.
- `disallowedTools: [bash]` - removed from the resolved allowlist.
- `tools: [firecrawl]` - an MCP **server name** grants every tool that server contributed (`firecrawl__scrape`, `firecrawl__crawl`, ...). Works in both lists. See [MCP servers](/integrations/mcp).

The executor resolves the names against the orchestrator's tool catalogue (the slice you passed to `WithTools` / `WithAdditionalTools`). Names that do not match any registered tool - or MCP server group - surface as a load-time error before the first LLM call.

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

## MCP tools (native)

[Model Context Protocol](https://modelcontextprotocol.io) servers expose tool catalogues over a standard wire format. zenflow supports MCP natively: point it at a Claude-compatible `settings.json` and every server's tools become available to your agents - no Go code, no recompile.

```json
// .zenflow/settings.json
{
  "mcpServers": {
    "firecrawl": {
      "command": "npx",
      "args": ["-y", "firecrawl-mcp"],
      "env": { "FIRECRAWL_API_KEY": "${FIRECRAWL_API_KEY}" }
    }
  }
}
```

```yaml
agents:
  crawler:
    description: "Crawls pages with firecrawl."
    tools: ["read", "write", "firecrawl"]   # bare server name = all its tools
```

The CLI reads `.zenflow/settings.json` automatically (override with `--mcp-config`, disable with `--no-mcp`). Discovered tools are namespaced `<server>__<tool>`; an agent can grant a whole server by its bare name or a single tool by its full name.

Embedders get the same surface as a small library API - `LoadMCPConfig`, `ConnectMCPConfig`, and `WithAdditionalTools` (which appends to the catalog rather than replacing it):

```go
cfg, _ := zenflow.LoadMCPConfig(".zenflow/settings.json")
ts, _ := zenflow.ConnectMCPConfig(ctx, cfg)
defer ts.Close()

orch := zenflow.New(
    zenflow.WithModel(llm),
    zenflow.WithTools(builtins...),
    zenflow.WithAdditionalTools(ts.Tools()...),
)
```

zenflow does not need to know a tool came from MCP - it looks like any other `goai.Tool`. See the [MCP servers guide](/integrations/mcp) for the full config format (stdio / HTTP / SSE), env expansion, grouping, and lifecycle.

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
