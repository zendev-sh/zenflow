---
title: Agents
description: An agent in zenflow is a named LLM configuration. You declare agents once at the top of a workflow and reference them from steps. Each step picks...
---

# Agents

An **agent** in zenflow is a named LLM configuration. You declare agents once at the top of a workflow and reference them from steps. Each step picks an agent; the executor runs the step's instructions through that agent's model, prompt, and tool set.

Agents are workflow-scoped. They are not standalone services; they exist for the duration of a `RunFlow` call.

## Declaring agents

```yaml
agents:
  planner:
    description: "Technical lead who creates implementation plans."
    prompt: "@prompts/planner.md"
    model: "bedrock/anthropic.claude-sonnet-4-6"
    tools: [bash, read, write]
    disallowedTools: [bash]
    maxTurns: 10
    temperature: 0.3
```

The `agents:` block is a map keyed by agent name. The keys are the strings steps put in `agent:`.

### Field reference

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `description` | string | Yes | Human-readable role summary. Coordinator and the goal-decomposition LLM read this. |
| `prompt` | string | No | System prompt. Use `@path/to/file.md` to load from disk (relative to the workflow file). |
| `model` | string | No | Model identifier. Bare names (`claude-sonnet-4-6`, auto-routed) and provider-prefixed forms (`bedrock/...`, `azure/...`, `google/...`, `vertex/...`) work. The CLI does not accept the `anthropic/` prefix; use `bedrock/anthropic.claude-...` or `azure/claude-...` instead. |
| `tools` | array of strings | No | Tool allowlist. Omit to allow every registered tool. List explicit names to restrict. Wildcards are not supported. |
| `disallowedTools` | array of strings | No | Tool denylist applied after the allowlist. |
| `maxTurns` | integer | no | Maximum tool-call turns the agent may take in one step. Defaults to 50 when omitted. Hitting the cap returns AgentStatusTruncated and the step fails with ErrAgentTurnLimitExceeded. |
| `temperature` | number | No | Sampling temperature in `[0, 2]`. |
| `topP` | number | No | Nucleus sampling parameter in `[0, 1]`. |
| `resultSchema` | object | No | JSON Schema for the agent's structured `result` channel. See [Structured output](/concepts/structured-output). |

The full schema and validation rules live in [YAML reference: Agent](/yaml/agent).

## Step-level overrides

A step may override the agent's model:

```yaml
steps:
  - id: think-hard
    agent: planner
    model: "claude-opus-4-6-thinking"
    instructions: "Reason step by step."
```

`step.model` takes precedence over `agent.model` for that step only. Other agent fields cannot be overridden per step; if you need a different prompt or tool set, declare another agent.

## Tool filtering

The effective tool set for an agent is `(allowlist) - (denylist)`:

- `tools` omitted: every tool the orchestrator was constructed with via `WithTools` is available (zenflow's executor exposes the orchestrator-registered tools when no `tools` field is set).
- `tools: [a, b, c]`: only those names.
- `disallowedTools: [bash]`: removed from the resolved allowlist.

When the agent's `resultSchema` is set, the executor auto-injects a `submit_result` tool on top of the resolved set. `send_message` is auto-injected on every step runner that has a MessageRouter AND is not the coordinator itself (detection: presence of `forward_to_agent` in the runner's tool list marks the coordinator). Step runners that already have a `send_message` tool keep their own - no overwrite.

## Implicit (default) agents

A step can omit `agent:`. Zenflow uses a default agent (the orchestrator-level `WithModel` LLM, with the orchestrator's `WithTools` catalogue). The minimal valid workflow is one step with no agents block at all:

```yaml
name: minimal
steps:
  - id: greet
    instructions: "Say hello."
```

Use this for quick scripts. Once you need different roles, prompts, or tool subsets, declare named agents.

## Provider matrix

zenflow runs on top of [goai](https://goai.sh). Any provider [goai](https://goai.sh) supports works - zenflow does not bind to a specific vendor. The model identifier you put in `model:` is passed through to [goai](https://goai.sh)'s resolver:

| Provider | Identifier examples | [goai](https://goai.sh) entry point |
|----------|---------------------|------------------|
| Google | `gemini-3-pro-preview`, `gemini-2.5-pro` | `goai/provider/google` |
| AWS Bedrock | `anthropic.claude-sonnet-4-6`, `minimax.minimax-m2.5` | `goai/provider/bedrock` |
| Azure (AI Services) | `DeepSeek-V3.2`, `grok-2`, `Llama-3.1-405B` | `goai/provider/azure` |
| Azure (OpenAI) | `gpt-5`, `gpt-5.3-codex`, `o3-mini` | `goai/provider/azure` |
| Azure (Anthropic) | `claude-sonnet-4-6` | `goai/provider/azure` |
| Vertex AI | `claude-3-5-sonnet@vertex` | `goai/provider/vertex` |
| Anthropic direct | `claude-sonnet-4-6` (bare names dispatched via library code) | `goai/provider/anthropic` (library only) -- use `zenflow.WithModel(anthropic.Chat("claude-sonnet-4-6"))`. The CLI binary does not auto-route to direct Anthropic; it routes Claude models to Azure/Bedrock. |
| OpenAI direct | `gpt-4o`, `o3` | `goai/provider/openai` (library only) |
| OpenRouter | use `zenflow.WithModel(openrouter.Chat(...))` | `goai/provider/openrouter` (library only) |
| Cerebras | use `zenflow.WithModel(cerebras.Chat(...))` | `goai/provider/cerebras` (library only) |
| Cloudflare Workers AI | use `zenflow.WithModel(cloudflare.Chat(...))` | `goai/provider/cloudflare` (library only) |
| Cohere | use `zenflow.WithModel(cohere.Chat(...))` | `goai/provider/cohere` (library only) |
| DeepInfra | use `zenflow.WithModel(deepinfra.Chat(...))` | `goai/provider/deepinfra` (library only) |
| DeepSeek | use `zenflow.WithModel(deepseek.Chat(...))` | `goai/provider/deepseek` (library only) |
| Fireworks | use `zenflow.WithModel(fireworks.Chat(...))` | `goai/provider/fireworks` (library only) |
| FPT Cloud | use `zenflow.WithModel(fptcloud.Chat(...))` | `goai/provider/fptcloud` (library only) |
| Groq | use `zenflow.WithModel(groq.Chat(...))` | `goai/provider/groq` (library only) |
| MiniMax | use `zenflow.WithModel(minimax.Chat(...))` | `goai/provider/minimax` (library only) |
| Mistral | use `zenflow.WithModel(mistral.Chat(...))` | `goai/provider/mistral` (library only) |
| NVIDIA NIM | use `zenflow.WithModel(nvidia.Chat(...))` | `goai/provider/nvidia` (library only) |
| Ollama | use `zenflow.WithModel(ollama.Chat(...))` | `goai/provider/ollama` (library only) |
| Perplexity | use `zenflow.WithModel(perplexity.Chat(...))` | `goai/provider/perplexity` (library only) |
| RunPod | use `zenflow.WithModel(runpod.Chat(...))` | `goai/provider/runpod` (library only) |
| Together AI | use `zenflow.WithModel(together.Chat(...))` | `goai/provider/together` (library only) |
| vLLM | use `zenflow.WithModel(vllm.Chat(...))` | `goai/provider/vllm` (library only) |
| xAI | use `zenflow.WithModel(xai.Chat(...))` | `goai/provider/xai` (library only) |
| OpenAI-compatible (generic) | use `zenflow.WithModel(compat.Chat(...))` | `goai/provider/compat` (library only) |

When you build the orchestrator, you pass one `provider.LanguageModel` via `WithModel`. The model your YAML asks for (per-agent `model:` or per-step `model:`) is resolved through the [goai](https://goai.sh) stack to a provider call. Goai handles auth, request shape, streaming, structured tool calls, and retries.

The CLI binary picks a provider from `--model` and the environment automatically. The library expects you to wire a `provider.LanguageModel` of your choice. See [goai docs](https://goai.sh) for provider setup.

## maxTurns and the conversation loop

`maxTurns` bounds how many round trips the agent can make against the LLM in one step. One "turn" = one LLM response (which may include tool calls). The loop terminates when:

1. The LLM returns text with no tool calls (natural exit).
2. The agent's `resultSchema` triggers a successful `submit_result` call (terminates immediately on the validating call).
3. `maxTurns` is reached.
4. The orchestrator's context is cancelled (timeout, abort).

If `maxTurns` is reached without a successful `submit_result` call (and `resultSchema` was set), the step fails with `"resultSchema defined but submit_result never called"`. Without `resultSchema`, hitting `maxTurns` is allowed and the step's content is whatever was accumulated.

## Temperature and topP

Both flow through [goai](https://goai.sh) to the underlying provider. They are advisory - providers that do not support a knob silently ignore it. For deterministic-ish agents (judges, validators, structured extractors), set `temperature: 0`. For creative-ish agents (planners, debaters), keep the default.

## Cross-links

- [YAML reference: Agent](/yaml/agent) - full field-by-field spec
- [Structured output](/concepts/structured-output) - how `resultSchema` and `submit_result` work
- [Tools](/concepts/tools) - tool catalogue, MCP, permissions
- [Coordinator](/concepts/coordinator) - the LLM-backed coordinator that supervises a workflow
- [goai providers](https://goai.sh) - provider-specific setup and supported features
