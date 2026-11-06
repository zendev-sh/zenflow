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
| `model` | string | No | Model identifier. Bare names (`claude-sonnet-4-6`, auto-routed) and provider-prefixed forms (`google/...`, `bedrock/...`, `azure/...`, `anthropic/...`, `openai/...`, `fptcloud/...`) work. The CLI accepts every prefix in the [provider matrix](#provider-matrix). |
| `tools` | array of strings | No | Tool allowlist. Omit to allow every registered tool. List explicit names to restrict. Wildcards are not supported. |
| `disallowedTools` | array of strings | No | Tool denylist applied after the allowlist. |
| `maxTurns` | integer | no | Maximum tool-call turns the agent may take in one step. Defaults to 50 when omitted. Hitting the cap returns AgentStatusTruncated and the step fails with ErrAgentTurnLimitExceeded. |
| `temperature` | number | No | Sampling temperature in `[0, 2]`. |
| `topP` | number | No | Nucleus sampling parameter in `[0, 1]`. |
| `resultSchema` | object | No | JSON Schema for the agent's structured `result` channel. See [Structured output](/concepts/structured-output). |

The full schema and validation rules live in [YAML reference: Agent](/yaml/agent).

### System prompt

`prompt` is the agent's identity and is injected into the LLM's system slot (`goai.WithSystem`), not the user message. Two operator-level layers can extend it without editing the workflow:

- `WithSystemPromptSuffix(text)` (CLI `--system-prompt`) appends project policy to every agent's system prompt.
- `WithSkills(skills)` (CLI `--skills DIR`) appends the skill index and registers the `read_skill` tool for on-demand loading.

The agent's own `prompt` remains the base; suffixes are appended after it.

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

| Provider | CLI `--model` prefix | Example model id | API-key env var | [goai](https://goai.sh) entry point |
|----------|----------------------|------------------|-----------------|------------------|
| Google | `google/` | `gemini-3-pro-preview`, `gemini-2.5-pro` | `GEMINI_API_KEY` / `GOOGLE_GENERATIVE_AI_API_KEY` | `goai/provider/google` |
| AWS Bedrock | `bedrock/` | `anthropic.claude-sonnet-4-6`, `minimax.minimax-m2.5` | `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY` (or profile) | `goai/provider/bedrock` |
| Azure (AI Services) | `azure/` | `DeepSeek-V3.2`, `grok-2`, `Llama-3.1-405B` | `AZURE_OPENAI_API_KEY` + `AZURE_RESOURCE_NAME` | `goai/provider/azure` |
| Azure (OpenAI) | `azure/` | `gpt-5`, `gpt-5.3-codex`, `o3-mini` | `AZURE_OPENAI_API_KEY` + `AZURE_RESOURCE_NAME` | `goai/provider/azure` |
| Azure (deployment URL) | `azure-deployment/` | `gpt-5`, `claude-sonnet-4-6` | `AZURE_OPENAI_API_KEY` + `AZURE_RESOURCE_NAME` | `goai/provider/azure` |
| Vertex AI | `vertex/`, `vertex-anthropic/` | `gemini-2.5-pro`, `claude-3-5-sonnet@vertex` | Application Default Credentials | `goai/provider/vertex` |
| Anthropic direct | `anthropic/` | `claude-sonnet-4-6` | `ANTHROPIC_API_KEY` (+ `ANTHROPIC_BASE_URL`) | `goai/provider/anthropic` |
| OpenAI direct | `openai/` | `gpt-4o`, `o3` | `OPENAI_API_KEY` (+ `OPENAI_BASE_URL`) | `goai/provider/openai` |
| OpenRouter | `openrouter/` | `anthropic/claude-3.5-sonnet` | `OPENROUTER_API_KEY` | `goai/provider/openrouter` |
| Cerebras | `cerebras/` | `llama3.1-70b` | `CEREBRAS_API_KEY` | `goai/provider/cerebras` |
| Cheaper Inference | `cheaperinference/` | `gpt-5.4-mini` | `CHEAPER_INFERENCE_API_KEY` (+ `CHEAPER_INFERENCE_BASE_URL`) | `goai/provider/cheaperinference` |
| Cloudflare Workers AI | `cloudflare/` | `@cf/meta/llama-3.1-8b-instruct` | `CLOUDFLARE_API_TOKEN` + `CLOUDFLARE_ACCOUNT_ID` | `goai/provider/cloudflare` |
| Cohere | `cohere/` | `command-r-plus` | `COHERE_API_KEY` | `goai/provider/cohere` |
| DeepInfra | `deepinfra/` | `meta-llama/Llama-3.1-70B-Instruct` | `DEEPINFRA_API_KEY` | `goai/provider/deepinfra` |
| DeepSeek | `deepseek/` | `deepseek-chat` | `DEEPSEEK_API_KEY` | `goai/provider/deepseek` |
| Fireworks | `fireworks/` | `accounts/fireworks/models/llama-v3p1-70b-instruct` | `FIREWORKS_API_KEY` | `goai/provider/fireworks` |
| FPT Cloud | `fptcloud/` | `DeepSeek-V4.1-Flash` | `FPT_API_KEY` (+ `FPT_REGION`, `FPT_BASE_URL`) | `goai/provider/fptcloud` |
| Groq | `groq/` | `llama-3.3-70b-versatile` | `GROQ_API_KEY` | `goai/provider/groq` |
| MiniMax | `minimax/` | `MiniMax-M2` | `MINIMAX_API_KEY` | `goai/provider/minimax` |
| Mistral | `mistral/` | `mistral-large-latest` | `MISTRAL_API_KEY` | `goai/provider/mistral` |
| NVIDIA NIM | `nvidia/` | `meta/llama-3.1-70b-instruct` | `NVIDIA_API_KEY` | `goai/provider/nvidia` |
| Ollama | `ollama/` | `llama3.2` | `OLLAMA_BASE_URL` / `OLLAMA_HOST` | `goai/provider/ollama` |
| Perplexity | `perplexity/` | `sonar-pro` | `PERPLEXITY_API_KEY` | `goai/provider/perplexity` |
| RunPod | `runpod/` | model id | `RUNPOD_API_KEY` + `RUNPOD_ENDPOINT_ID` | `goai/provider/runpod` |
| Together AI | `together/` | `meta-llama/Llama-3.3-70B-Instruct-Turbo` | `TOGETHER_AI_API_KEY` / `TOGETHER_API_KEY` | `goai/provider/together` |
| vLLM | `vllm/` | served model id | `VLLM_BASE_URL` (+ `VLLM_API_KEY`) | `goai/provider/vllm` |
| xAI | `xai/` | `grok-2` | `XAI_API_KEY` | `goai/provider/xai` |
| OpenAI-compatible (generic) | `compat/` | server model id | `COMPAT_BASE_URL` (+ `COMPAT_API_KEY`) | `goai/provider/compat` |

Every prefix in the table is accepted by **both** the CLI (`--model <prefix>/<model>`) and the library (`zenflow.WithModel(<pkg>.Chat(...))`). Bare model names (no prefix) are auto-detected from the model name plus the API-key env vars that are set; use an explicit prefix when you need to force a provider (for example `anthropic/claude-sonnet-4-6` rather than letting a bare Claude name auto-route to Azure or Bedrock).

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
