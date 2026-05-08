---
title: Structured Output
description: 'Every step in zenflow produces two output channels:'
---

# Structured Output

Every step in zenflow produces two output channels:

- **`content`** - free-form text. The concatenation of every assistant turn the agent emitted. Right for human display, summary, anything markdown-shaped.
- **`result`** - structured JSON. A `map[string]any` populated when the agent calls `submit_result`. Right for downstream conditional logic, machine-readable signals, judge decisions.

`result` is opt-in. An agent gets it only when its `resultSchema` is set. Without a `resultSchema`, the step produces content only and `result` is nil.

## Declaring a result schema

```yaml
agents:
  tester:
    description: "Test runner."
    tools: [bash]
    resultSchema:
      type: object
      required: [passed]
      properties:
        passed:
          type: boolean
          description: "true if all tests passed."
        failed_count:
          type: integer
        summary:
          type: string
```

The `resultSchema` field accepts any JSON Schema (Draft 2020-12) object. Zenflow validates it as a JSON object at parse time but does not deeply check the schema's own validity until first use. For complex schemas, run a JSON Schema linter at author time.

## How `submit_result` works

When an agent has a `resultSchema`, the executor injects a tool called `submit_result` into the agent's tool list. The tool's input schema is the agent's `resultSchema`. That is the entire mechanism - no GenerateObject calls, no separate structured-mode LLM round trip, no text parsing.

Flow:

1. The agent receives its prompt plus the auto-injected `submit_result` tool.
2. At some turn, the agent calls `submit_result({...})` with arguments matching the schema.
3. The executor validates the arguments:
   - **Valid**: the arguments become the step's `result`. The tool returns `{"status": "ok"}`. The agent's conversation loop **terminates immediately** - no further turns happen, even if the agent included other content.
   - **Invalid**: the tool returns `{"status": "error", "message": "<validation details>"}`. The conversation **continues**. The agent sees the error message in its tool result and may retry.
4. `content` is the concatenation of text from all turns up to (and including) the successful `submit_result` call.

This is the only path to structured output. There is no parallel "GenerateObject" mode; everything goes through tool-calling.

## Edge cases

- **Multiple valid calls.** Cannot happen - the loop terminates on the first valid call.
- **Invalid then valid.** Expected flow. The agent calls `submit_result` with bad args, sees the error, retries with corrected args. The corrected call terminates.
- **Parallel tool calls in one turn.** If the LLM emits multiple `submit_result` calls in the same turn (rare), the executor takes the first valid one and ignores the rest.
- **Never called.** If the agent exhausts `maxTurns` or reaches a natural end-of-turn (no tool calls at all) without a successful `submit_result`, the step **fails** with `"resultSchema defined but submit_result never called"`.
- **Side-effecting tools alongside `submit_result`.** If the LLM emits `submit_result` with another tool (e.g. `write_file`) in the same turn, all tools in the batch execute. `submit_result` then terminates the loop after the batch completes - side effects are kept, not rolled back.
- **Agents without `resultSchema`.** No `submit_result` tool is injected. `result` is nil; only `content` is populated.

## Reading results downstream

Downstream steps see structured results via CEL:

```yaml
- id: optimize
  agent: optimizer
  dependsOn: [test]
  condition: "steps.test.status == 'completed' && steps.test.result.passed == true"
  instructions: "Optimize hot paths."
```

The `result` field in CEL is the same `map[string]any` that the step produced. CEL allows arbitrary path access (`result.summary`, `result.errors[0].file`, `result.tags.exists(t, t == 'critical')`).

In Go code, after `RunFlow`:

```go
result, err := orch.RunFlow(ctx, wf)
testResult := result.Steps["test"]
if testResult.Status == zenflow.StepCompleted {
    passed, _ := testResult.Result["passed"].(bool)
    summary, _ := testResult.Result["summary"].(string)
    fmt.Printf("tests passed=%v: %s\n", passed, summary)
}
```

`StepResult.Result` is the typed `map[string]any`. Access fields with type assertions or use a helper library (e.g. `github.com/tidwall/gjson` for JSON-path access).

## Per-step result schemas

`resultSchema` is an agent-level field. Steps do not have their own `resultSchema` override. To get different schemas across steps, declare multiple agents (each with its own `resultSchema`) and reference the right agent from each step:

```yaml
agents:
  classifier:
    model: gemini-3-pro-preview
    resultSchema: { ... }
  summarizer:
    model: gemini-3-pro-preview
    resultSchema: { ... }
steps:
  - id: classify
    agent: classifier
  - id: summarize
    agent: summarizer
```

This keeps the agent-to-schema binding explicit and lets the executor inject the right `submit_result` schema per step without any step-level override mechanism.

## `untilAgent` and result schemas

The `untilAgent` loop control protocol relies on structured results. The judge agent must have a `resultSchema` with:

- `properties.done` of type boolean.
- `done` in the top-level `required` array.

The validator enforces this at parse time. The judge calls `submit_result({done: true|false, reason: "..."})`. The executor reads `result.done` to decide whether the loop terminates.

If the judge fails to call `submit_result`, the loop continues to the next iteration (fail-open behaviour for judges; see [Loops](/concepts/loops)).

## Validation errors

The validation message returned to the agent on invalid `submit_result` includes the JSON Schema validation error path and message:

```json
{
  "status": "error",
  "message": "validation failed: /passed: expected boolean, got string"
}
```

The agent can use this to retry with corrected arguments. Most modern LLMs handle the retry correctly when the schema is small and the error message is clear. For complex schemas, smaller required-field sets and inline `description` annotations help the model produce valid output on the first try.

## Cross-link to goai

zenflow's `submit_result` mechanism rides on [goai](https://goai.sh)'s tool-calling abstraction. Internally, the tool is registered with [goai](https://goai.sh) as a normal `goai.Tool` with an `InputSchema` set to the `resultSchema`. The provider ([goai](https://goai.sh)'s `Chat` / `GenerateText`) handles the actual structured-output formatting per provider quirks (OpenAI strict mode, Anthropic tool-use, Google function calling). Zenflow does not bypass any of this - it composes on top.

For deeper structured-output mechanics (provider-specific behaviour, retry policies, streaming with structured output), see [goai's structured output docs](https://goai.sh).

## Why `submit_result` instead of GenerateObject

Three reasons:

1. **Single LLM call per turn.** A separate "structured" call would double the round trips. Tool calling fits the existing loop.
2. **Self-correction via tool results.** Validation errors come back as tool results, the model sees them, and retries naturally. A separate GenerateObject path would need its own retry layer.
3. **Mixed text + structured output.** The agent can emit narrative content in earlier turns and finalise with `submit_result`. The `content` channel captures the narrative, the `result` channel captures the structured decision.

The cost is one extra schema-aware tool per agent. Given that most flows already inject `send_message`, `shared_memory_*`, and tool-catalogue tools, one more is unobtrusive.

## Cross-links

- [Agents](/concepts/agents) - the `resultSchema` field on agents
- [Loops](/concepts/loops) - `untilAgent` judges and structured `done` decisions
- [Conditions](/concepts/conditions) - reading `steps.<id>.result` in CEL
- [Tools](/concepts/tools) - the broader tool-calling model `submit_result` rides on
- [goai structured output](https://goai.sh) - provider-side mechanics
