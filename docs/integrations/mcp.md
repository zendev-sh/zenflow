---
title: MCP servers
description: zenflow speaks the Model Context Protocol natively. Point it at a Claude-compatible settings.json and every MCP server's tools become available to your agents - no Go code, no recompile.
---

# MCP servers

zenflow is an MCP-native agent harness. [Model Context Protocol](https://modelcontextprotocol.io) servers expose tools (and prompts and resources) over a standard wire format; zenflow connects to them, discovers their tools, and hands those tools to your agents exactly like the built-in `read` / `write` / `bash` set. The wire protocol is owned by [goai](https://goai.sh)'s MCP client - zenflow is the glue that turns a config file into a running, tool-equipped workflow.

You configure MCP servers in a Claude-compatible `settings.json`. No Go code, no recompile.

## Quick start (CLI)

Drop a `.zenflow/settings.json` next to where you run the CLI:

```json
{
  "mcpServers": {
    "firecrawl": {
      "command": "npx",
      "args": ["-y", "firecrawl-mcp"],
      "env": {
        "FIRECRAWL_API_KEY": "${FIRECRAWL_API_KEY}"
      }
    }
  }
}
```

Reference the server in an agent's `tools:` list - the bare server name grants **all** of that server's tools:

```yaml
name: summarize
agents:
  crawler:
    description: "Crawls pages with the firecrawl MCP server."
    tools: ["read", "write", "firecrawl"]   # 'firecrawl' = every firecrawl tool
  writer:
    description: "Reads and summarizes."
    tools: ["read", "write"]

steps:
  - id: crawl
    agent: crawler
    instructions: "Crawl https://www.wikipedia.org/ as markdown using the firecrawl tools, then write it to download.md"
  - id: write
    agent: writer
    instructions: "Read download.md and write a summary to summary.md"
    dependsOn: ["crawl"]
```

```bash
export FIRECRAWL_API_KEY=fc-...
zenflow flow summarize.yaml --model google/gemini-2.5-flash --verbose
```

With `--verbose`, zenflow prints what it loaded:

```
zenflow: MCP loaded 8 tool(s) from server(s) [firecrawl]
```

The same config is read by all three CLI verbs - `zenflow flow`, `zenflow goal`, and `zenflow agent`.

## Config file

By default zenflow looks for `.zenflow/settings.json` relative to the working directory. Override or disable it:

| Flag | Effect |
| --- | --- |
| `--mcp-config PATH` | Read MCP servers from `PATH` instead of the default. |
| `--no-mcp` | Skip MCP loading entirely. |

A missing default file is a silent no-op - MCP is opt-in by the presence of the file. An explicit `--mcp-config PATH` that does not exist is reported.

### Server entry shapes

Each entry under `mcpServers` is one server. The transport is inferred from the fields, or set explicitly with `"type"`.

**Stdio (local subprocess)** - the common case:

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/data"],
      "env": { "LOG_LEVEL": "info" }
    }
  }
}
```

**Remote (Streamable HTTP or SSE)**:

```json
{
  "mcpServers": {
    "remote-http": {
      "url": "https://mcp.example.com/v1",
      "headers": { "Authorization": "Bearer ${MCP_TOKEN}" }
    },
    "legacy-sse": {
      "type": "sse",
      "url": "https://sse.example.com/mcp"
    }
  }
}
```

| Field | Applies to | Meaning |
| --- | --- | --- |
| `command`, `args` | stdio | Executable and arguments for the subprocess. |
| `env` | stdio | Extra environment variables (merged onto the parent env). |
| `url` | http / sse | Remote server endpoint. |
| `headers` | http / sse | Headers sent on every request (auth, etc.). |
| `type` | all | `"stdio"`, `"http"`, or `"sse"`. Empty infers: `command` → stdio, `url` → http. |
| `disabled` | all | `true` skips this server. |

**Environment expansion.** Every string in `command`, `args`, `env`, `url`, and `headers` is expanded with `${VAR}` / `$VAR` from the process environment, so secrets stay out of the file:

```json
{ "env": { "FIRECRAWL_API_KEY": "${FIRECRAWL_API_KEY}" } }
```

## Remote servers (HTTP / SSE)

A remote server is one you reach over the network instead of spawning locally. Set `url` (the transport defaults to Streamable HTTP) and pass auth via `headers`. zenflow connects exactly the same way - the tools come back namespaced and grantable by server name.

### Example: GitHub's remote MCP server

GitHub hosts an official remote MCP server at `https://api.githubcopilot.com/mcp/`. Authenticate with a token in an `Authorization` header (keep the token in an env var, not the file):

```json
// .zenflow/settings.json
{
  "mcpServers": {
    "github": {
      "type": "http",
      "url": "https://api.githubcopilot.com/mcp/",
      "headers": { "Authorization": "Bearer ${GITHUB_MCP_TOKEN}" }
    }
  }
}
```

```yaml
# review.yaml
name: triage
agents:
  dev:
    description: "GitHub assistant."
    tools: ["github"]   # bare server name = every github__* tool
steps:
  - id: triage
    agent: dev
    instructions: "Use the github tools to find the 5 oldest open issues in zendev-sh/zenflow and summarize each."
```

```bash
export GITHUB_MCP_TOKEN=$(gh auth token)   # or a fine-grained PAT
zenflow flow review.yaml --model google/gemini-2.5-flash --verbose --yolo
# zenflow: MCP loaded 44 tool(s) from server(s) [github]
```

The server exposes ~44 tools (`github__search_repositories`, `github__get_file_contents`, `github__list_issues`, `github__create_pull_request`, ...). The `tools: ["github"]` group above grants **all** of them, including write-capable ones.

For a run that can only read, prefer an explicit allowlist over a partial `disallowedTools` - the group includes more write tools than you might remember (`issue_write`, `merge_pull_request`, `push_files`, ...), so listing just the read tools is safer:

```yaml
agents:
  reader:
    description: "Read-only GitHub researcher."
    tools:
      - github__search_repositories
      - github__get_file_contents
      - github__list_issues
      - github__issue_read
      - github__list_releases
      - github__get_latest_release
```

### SSE transport

For older servers that speak the legacy SSE protocol, set `"type": "sse"`:

```json
{ "mcpServers": { "legacy": { "type": "sse", "url": "https://sse.example.com/mcp" } } }
```

### Troubleshooting: `HTTP 400` on connect

```
zenflow: MCP: mcp server "github": mcp: transport start: mcp: SSE connection failed: HTTP 400
✗ zenflow: validation: agent "reader" references unknown tool "github__search_repositories"
```

The first line is the real cause; the second is a knock-on (no tools loaded, so the allowlist references nothing). The usual culprits:

- **Empty or invalid auth token (most common).** If `${GITHUB_MCP_TOKEN}` resolves to an empty string - the env var was never exported - the header becomes `Authorization: Bearer ` and GitHub rejects it with `400`. Export the token first: `export GITHUB_MCP_TOKEN=$(gh auth token)`, and double-check it is non-empty.
- **Server doesn't support the SSE-GET probe.** zenflow's Streamable HTTP transport opens an optional server-initiated SSE stream on connect, tolerating `405`/`404` (POST-only servers). A server that answers that probe with `400` instead - some non-GitHub implementations - is not spec-compliant for that handshake; use `"type": "sse"` against its SSE endpoint, or run it locally over stdio.

## Tool naming and grouping

Discovered tools are namespaced `<server>__<tool>` so two servers can never collide. A firecrawl server that exposes `scrape` and `crawl` contributes `firecrawl__scrape` and `firecrawl__crawl`.

In an agent's `tools:` allowlist (or `disallowedTools:`) you can reference either:

- the **bare server name** - `firecrawl` selects every `firecrawl__*` tool, or
- a **specific tool** - `firecrawl__scrape` selects just that one.

Built-in tool names (`read`, `write`, `bash`, `glob`, `grep`) contain no `__` separator, so grouping never affects them.

If an agent references a tool that no server provided (a typo, or a server that failed to load), zenflow rejects the workflow before the first LLM call with a clear `references unknown tool` error - it does not silently drop the tool.

## Embedding in Go

The CLI path is a thin consumer of a small library surface. Load the config, connect, and layer the tools onto the orchestrator with `WithAdditionalTools`:

```go
package main

import (
    "context"
    "errors"
    "log"
    "os"

    "github.com/zendev-sh/goai/provider/google"
    "github.com/zendev-sh/zenflow"
)

func main() {
    ctx := context.Background()

    cfg, err := zenflow.LoadMCPConfig(".zenflow/settings.json")
    if errors.Is(err, os.ErrNotExist) {
        cfg = &zenflow.MCPConfig{} // no MCP configured; carry on
    } else if err != nil {
        log.Fatal(err)
    }

    // Connect to every server. ctx governs stdio subprocess lifetime, so it
    // must outlive tool use; orderly shutdown is ts.Close().
    ts, err := zenflow.ConnectMCPConfig(ctx, cfg)
    if err != nil {
        log.Printf("some MCP servers failed: %v", err) // partial success is OK
    }
    defer ts.Close()

    llm := google.Chat("gemini-2.5-flash", google.WithAPIKey(os.Getenv("GEMINI_API_KEY")))
    orch := zenflow.New(
        zenflow.WithModel(llm),
        zenflow.WithTools(/* your built-in tools */),
        zenflow.WithAdditionalTools(ts.Tools()...), // appends; does not replace
        zenflow.WithCoordinator(zenflow.NewDefaultCoordRunner(llm)),
    )
    defer orch.Close()

    // orch.RunFlow(ctx, wf) ...
}
```

Key points:

- **`WithAdditionalTools` appends.** `WithTools` *replaces* the catalog; `WithAdditionalTools` layers onto it. Use the latter for MCP so you keep your built-ins.
- **`ConnectMCPConfig` is resilient.** One unreachable server does not abort the rest - the returned `*MCPToolset` carries every server that connected, and the error is the join of the failures. Use the toolset even on a non-nil error, and always `Close` it.
- **`ctx` governs stdio lifetime.** goai starts stdio servers with the context you pass; hand it one that lives as long as the tools may be called, and rely on `MCPToolset.Close()` for shutdown rather than cancelling that context.

### Connection options

| Option | Effect |
| --- | --- |
| `WithMCPClientInfo(name, version)` | Identity advertised to servers in the initialize handshake (default `zenflow`/`1.0.0`). |
| `WithMCPRequestTimeout(d)` | Bounds each MCP request, including the connect handshake and every tool call (default 60s). |
| `WithMCPStderr(w)` | Routes stdio subprocess stderr to `w` (default discarded). |

## Lifecycle

Each stdio server is a child process. `MCPToolset.Close()` terminates them; the CLI defers it for you when a run finishes. Long-lived embedders should `defer ts.Close()` alongside `defer orch.Close()`.

## Security

The settings file is a trust boundary. A stdio server entry runs its `command` as a local subprocess the moment the config loads - so an MCP config is as trusted as a `Makefile` or a `.vscode/tasks.json`. The spawn happens at load time, ahead of the tool-call permission gate (`--yolo` / `--allow` / `--sandbox`), which governs *tool invocations*, not the server process itself.

- **Only run zenflow in directories you trust.** The CLI auto-loads `.zenflow/settings.json` from the working directory and launches its stdio servers. Treat a checkout with an unfamiliar `.zenflow/settings.json` the way you would treat one with an unfamiliar build script. Use `--no-mcp` to skip loading, or `--mcp-config` to point at a file you control.
- **Use `https` for remote servers carrying secrets.** A `Bearer` token on an `http://` URL is sent in cleartext; zenflow prints a warning when it sees this.
- Keep tokens in environment variables and reference them with `${VAR}` - never commit a literal secret in the file.

zenflow runs stdio servers via an argument vector (no shell), so values in `args` are not subject to shell-injection; a server name containing the `__` separator is rejected (it would make tool namespacing ambiguous); and a server that fails to start never blocks the others.

## Cross-links

- [Tools](/concepts/tools) - how agents resolve tools; built-in, library-supplied, auto-injected, and MCP.
- [Agents](/concepts/agents) - `tools` / `disallowedTools` allowlists.
- [CLI flags](/cli/flags) - `--mcp-config`, `--no-mcp`.
- [Go API: Options](/api/options) - `WithAdditionalTools` and the MCP functions.
- [goai MCP docs](https://goai.sh) - the underlying client, transports, and auth.
