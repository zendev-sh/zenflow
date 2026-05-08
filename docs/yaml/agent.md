---
title: Agent
description: 'The agents block of a workflow holds named LLM configurations. Steps reference an agent by name (step.agent: planner) to pick up a specific model,...'
---

# Agent

The `agents` block of a workflow holds named LLM configurations. Steps reference an agent by name (`step.agent: planner`) to pick up a specific model, prompt, tool scope, and sampling profile.

Authoritative source: [`spec/v1/spec.md` §3](https://github.com/zendev-sh/zenflow/blob/main/spec/v1/spec.md#3-agents).

## Shape

```yaml
agents:
  planner:
    description: "Technical lead who creates implementation plans."
    prompt: "@prompts/planner.md"
    model: "bedrock/anthropic.claude-sonnet-4-6"
    disallowedTools: ["bash"]
    maxTurns: 10
    temperature: 0.3
    topP: 0.9
    resultSchema:
      type: object
      required: [plan]
      properties:
        plan: { type: string }
```

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `description` | string | yes | Human-readable role. Surfaces in tooling and logs. |
| `prompt` | string | no | System prompt. Supports `@file` references. |
| `model` | string | no | Model identifier. Free-form. |
| `tools` | array[string] | no | Tool allowlist. Omit to allow every registered tool. List explicit names to restrict. Wildcard tool names like `"*"` are not currently expanded - they are parsed as literal tool identifiers that match nothing. To allow every registered tool, omit the `tools` field entirely. |
| `disallowedTools` | array[string] | no | Denylist applied after the allowlist. |
| `maxTurns` | integer | no | Conversation turn cap. `minimum: 0` (the schema treats `0` and "omitted" identically; both fall back to the default of 50). |
| `temperature` | number | no | Sampling temperature. Range `[0, 2]`. |
| `topP` | number | no | Nucleus sampling parameter. Range `[0, 1]`. |
| `resultSchema` | object | no | JSON Schema for structured output via the `submit_result` tool. |

`additionalProperties: false` - unknown fields are rejected.

## description

The only required field. Free-form text. The engine displays it in narration and verbose logs so operators can tell which agent is acting on a step.

```yaml
agents:
  reviewer:
    description: "Senior engineer who audits code quality and security."
```

Keep it short and role-shaped. Long-form behavior belongs in `prompt`.

## prompt

System prompt for the agent. Optional. If you set both a prompt here and a step-level `instructions`, the engine routes them to separate message slots: the agent prompt is sent as the system message, and the step instructions are sent as the user message for that step.

Supports the `@file` convention. A value starting with `@` is a path relative to the workflow file; the engine reads the file at load time.

```yaml
agents:
  planner:
    description: "Technical lead."
    prompt: "@prompts/planner.md"
  coder:
    description: "Implementer."
    prompt: |
      You are a careful Go programmer.
      Always write tests before code.
```

A literal string starting with `@` (e.g., a Twitter handle) is not currently escapable - prefer file references for any prompt content.

## model

Model identifier. Free-form string. The schema validates only that this is a string; resolution is the runtime's job.

Both bare names and provider-prefixed names are accepted:

```yaml
agents:
  fast:
    description: "Quick check agent."
    model: "gemini-2.5-flash"           # bare name
  careful:
    description: "Deep reasoning agent."
    model: "bedrock/anthropic.claude-sonnet-4-6"  # provider-prefixed via Bedrock
```

The CLI resolves a bare name by checking which provider env vars are set (`GEMINI_API_KEY`, `AWS_ACCESS_KEY_ID`, `AZURE_OPENAI_API_KEY`). Common patterns:

| Pattern | Provider routing |
| --- | --- |
| `google/...` | Gemini direct API |
| `bedrock/...` | AWS Bedrock |
| `azure/...` | Azure OpenAI / AI Services |
| `azure-deployment/...` | Azure OpenAI deployment-based URL pattern |
| `vertex/...` | Google Vertex AI |
| `vertex-anthropic/...` | Anthropic models on Google Vertex |
| `gemini-...` (bare) | Gemini, if `GEMINI_API_KEY` set |
| `anthropic.<model>` (bare) | Bedrock cross-region pattern |
| `claude-...` (bare) | Azure Anthropic, if `AZURE_OPENAI_API_KEY` set |
| `gpt-...` (bare) | Azure OpenAI deployment-based path |

The bare `anthropic/` prefix is NOT auto-routed - use `azure/claude-...` or `vertex-anthropic/claude-...` for Anthropic-on-Azure or Anthropic-on-Vertex respectively.

A step can override the agent model with its own `step.model` field. See [Step / model](./step#model).

### Inheriting the orchestrator default model

If `agent.model` is unset and `step.model` is unset, the agent uses the orchestrator's default model. The CLI sets that from `--model` (or `ZENFLOW_MODEL` env var); library callers set it via `zenflow.WithModel(...)`.

```yaml
agents:
  planner:
    description: "Planner."
    # model omitted - inherits orchestrator default
```

```bash
zenflow flow workflow.yaml --model bedrock/anthropic.claude-sonnet-4-6
```

## tools and disallowedTools

Both are arrays of tool names. Together they define the agent's effective tool set:

```
effective = (allowlist) - (denylist)
```

- If `tools` is omitted, every tool registered with the orchestrator (via `WithTools`) is available to the agent.
- `tools: [a, b, c]` means exactly those three.
- `disallowedTools` removes entries from whatever the allowlist resolved to.

```yaml
agents:
  reader:
    description: "Read-only researcher."
    tools: ["read", "grep", "glob"]

  builder:
    description: "Builder with most tools but no destructive bash."
    disallowedTools: ["bash"]
```

The CLI's default tool registry includes `bash`, `read`, `write`, `glob`, and `grep`. Library callers register their own via `zenflow.WithTools(...)`.

## maxTurns

Caps how many LLM turns an agent may take in a single step. A turn is one assistant message (text + tool calls) plus the tool results that follow. Omit to use the default of 50.

```yaml
agents:
  bounded:
    description: "Agent that must converge in 5 turns or fewer."
    maxTurns: 5
```

If `maxTurns` is exhausted without the agent reaching `end_turn` (or calling `submit_result` when a `resultSchema` is defined), the step fails. For agents with a `resultSchema`, the failure message is `"resultSchema defined but submit_result never called"`.

## temperature

Sampling temperature. Range `[0, 2]`. Lower means more deterministic; higher means more creative. Many providers cap at `1.0`; consult the provider docs for what values are meaningful.

```yaml
agents:
  deterministic:
    description: "Always picks the obvious answer."
    temperature: 0
  creative:
    description: "Brainstorms freely."
    temperature: 0.9
```

## topP

Nucleus sampling parameter. Range `[0, 1]`. Most providers expose either `temperature` or `topP`; check provider compatibility before setting both.

```yaml
agents:
  focused:
    description: "Picks from a tight token distribution."
    topP: 0.5
```

## resultSchema

JSON Schema describing the structured output the agent must produce. When set, the executor auto-injects a `submit_result` tool whose input parameters equal this schema. The agent terminates the step by calling `submit_result(...)` with arguments that pass schema validation.

```yaml
agents:
  classifier:
    description: "Classifies issue severity."
    resultSchema:
      type: object
      required: [severity, summary]
      properties:
        severity:
          type: string
          enum: [low, medium, high, critical]
        summary: { type: string }
```

Behavior at runtime:

1. The agent calls `submit_result({...})`.
2. The executor validates arguments against `resultSchema`.
3. **Valid**: arguments become `step.result`. The tool returns `{"status":"ok"}`. The conversation loop terminates.
4. **Invalid**: the tool returns `{"status":"error","message":"..."}`. The conversation continues; the agent can retry.

If the agent never produces a valid `submit_result` (turn cap or `end_turn`), the step fails. Downstream steps and CEL expressions can read the structured result via `steps.<id>.result`.

For a worked example with `untilAgent`, see [Loop / `untilAgent`](./loop#until-and-untilagent).

## Bare-name resolution and references

Step `agent` fields reference an agent by the key in `agents`:

```yaml
agents:
  planner:
    description: "..."

steps:
  - id: plan
    agent: planner       # references agents.planner
```

The validator rejects a `step.agent` whose name does not exist in the `agents` map. There is no shorthand: every name used by a step must appear in the registry. A step without an `agent` field uses the executor's default agent and inherits the orchestrator's default model.

## Tool inheritance and the implicit `submit_result`

When `resultSchema` is set, the executor adds `submit_result` to the agent's tool list automatically. You do not list it in `tools`; it is always present alongside whatever else you allowed.

```yaml
agents:
  judge:
    description: "Decides whether to stop the loop."
    tools: []                     # no other tools
    resultSchema:
      type: object
      required: [done]
      properties:
        done: { type: boolean }
        reason: { type: string }
    # submit_result is auto-injected; effective tools = [submit_result]
```

This is also how `untilAgent` works: the named judge agent must define a `resultSchema` whose top-level object has a required boolean `done` field. See [Loop / `untilAgent`](./loop#untilagent-contract) for the full validator rules.

## Complete example

```yaml
name: review-pipeline
agents:
  reader:
    description: "Researcher who reads source files."
    tools: ["read", "glob", "grep"]
    maxTurns: 10
    temperature: 0.2

  reviewer:
    description: "Senior engineer who audits the code."
    model: "bedrock/anthropic.claude-sonnet-4-6"
    prompt: "@prompts/reviewer.md"
    tools: ["read"]
    maxTurns: 8
    resultSchema:
      type: object
      required: [verdict, issues]
      properties:
        verdict:
          type: string
          enum: [pass, fail]
        issues:
          type: array
          items: { type: string }

steps:
  - id: scan
    agent: reader
    instructions: "List all Go files in the project."

  - id: audit
    agent: reviewer
    dependsOn: [scan]
    instructions: "Review the code surfaced in the scan step."
```
