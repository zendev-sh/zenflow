// Package exec - goal_decomposer.go contains the goal-decomposition LLM
// helpers used by RunGoal: prompt assembly, single-turn / streaming chat
// invocation, and JSON response parsing/validation. This file is the
// "decomposer" coordinator (one-shot LLM that turns a high-level goal
// into a workflow YAML). It is distinct from the runtime coord runner
// (see coord_factory.go / coord_lib.go) which streams events during
// flow execution.
package exec

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
)

// CoordinatorChat sends a single-turn prompt to the goal-decomposition
// coordinator LLM and returns the raw response content. Used by
// RunGoal's decomposition + retry loop. Returns the response text and
// token usage.
func CoordinatorChat(ctx context.Context, model provider.LanguageModel, prompt string) (string, provider.Usage, error) {
	result, err := goai.GenerateText(ctx, model,
		goai.WithMessages(provider.Message{Role: provider.RoleUser, Content: []provider.Part{{Type: provider.PartText, Text: prompt}}}),
	)
	if err != nil {
		return "", provider.Usage{}, fmt.Errorf("coordinator llm: %w", err)
	}
	return result.Text, result.TotalUsage, nil
}

// CoordinatorStreamChat is the streaming variant of CoordinatorChat
// used by RunGoal when streaming is enabled. onText/onReasoning fire
// per chunk; the accumulated text-only content (reasoning excluded) is
// returned so JSON downstream parsing is not polluted by reasoning
// tokens.
func CoordinatorStreamChat(ctx context.Context, model provider.LanguageModel, prompt string, onText func(string), onReasoning func(string)) (string, provider.Usage, error) {
	stream, err := goai.StreamText(ctx, model,
		goai.WithMessages(provider.Message{Role: provider.RoleUser, Content: []provider.Part{{Type: provider.PartText, Text: prompt}}}),
	)
	if err != nil {
		return "", provider.Usage{}, fmt.Errorf("coordinator llm stream: %w", err)
	}
	var textOnly strings.Builder
	for chunk := range stream.Stream() {
		switch chunk.Type {
		case provider.ChunkText:
			textOnly.WriteString(chunk.Text)
			if onText != nil {
				onText(chunk.Text)
			}
		case provider.ChunkReasoning:
			if onReasoning != nil && chunk.Text != "" {
				onReasoning(chunk.Text)
			}
		}
	}
	result := stream.Result()
	if stream.Err() != nil {
		return "", provider.Usage{}, fmt.Errorf("coordinator llm stream: %w", stream.Err())
	}
	return textOnly.String(), result.TotalUsage, nil
}

//go:embed spec/v1/schema.json
var specSchema string

//go:embed spec/v1/examples/coordinator-output.json
var coordinatorExample string

// maxResponseSize is the maximum allowed coordinator response size (1 MB).
const maxResponseSize = 1 << 20

// JSONParseError indicates the coordinator response was not valid JSON.
// Stable.
type JSONParseError struct{ Err error }

func (e *JSONParseError) Error() string {
	var se *json.SyntaxError
	if errors.As(e.Err, &se) {
		return fmt.Sprintf("zenflow: json parse at offset %d: %s", se.Offset, e.Err.Error())
	}
	var ute *json.UnmarshalTypeError
	if errors.As(e.Err, &ute) {
		return fmt.Sprintf("zenflow: json parse at offset %d: %s", ute.Offset, e.Err.Error())
	}
	return "zenflow: json parse: " + e.Err.Error()
}
func (e *JSONParseError) Unwrap() error { return e.Err }

// CoordinatorValidationError indicates the coordinator response failed workflow validation.
// Named differently from ValidationError (YAML/schema) to avoid ambiguity.
type CoordinatorValidationError struct{ Err error }

func (e *CoordinatorValidationError) Error() string {
	return "zenflow: coordinator validation: " + e.Err.Error()
}
func (e *CoordinatorValidationError) Unwrap() error { return e.Err }

// ToolNotFoundError indicates the coordinator referenced a tool not in the catalog.
type ToolNotFoundError struct {
	Tool  string
	Agent string
}

func (e *ToolNotFoundError) Error() string {
	if e.Tool == "*" {
		return fmt.Sprintf("zenflow: validation: agent %q uses wildcard tool \"*\"; wildcards are not supported; list tools explicitly by name (e.g., [\"read\",\"bash\"]) or omit the field entirely", e.Agent)
	}
	return fmt.Sprintf("zenflow: validation: agent %q references unknown tool %q", e.Agent, e.Tool)
}

// CoordinatorPrompt builds the system prompt for the coordinator LLM call.
// The coordinator decomposes a goal into a workflow (agents + steps) given
// the available tool catalog. The prompt includes the full Zenflow JSON Schema
// (embedded from spec/v1/schema.json) so the LLM can use all spec features
// (loop, condition, resultSchema, options, etc.) without hardcoded subsets.
func CoordinatorPrompt(goal, toolCatalog string) string {
	var sb strings.Builder
	sb.WriteString(`You are a workflow coordinator. Your job is to break down a goal into a set of agents and steps that can be executed as a DAG.

## Goal
`)
	sb.WriteString(goal)
	sb.WriteString("\n\n## Available Tools\n")
	sb.WriteString(toolCatalog)
	sb.WriteString(`

## Zenflow Schema

Your output MUST conform to this JSON Schema:

`)
	sb.WriteString(specSchema)
	sb.WriteString(`

## Supported Features

The following features are fully implemented and you SHOULD use them:
- agents: description (required), prompt, tools, disallowedTools, maxTurns, resultSchema
- steps: id, agent, instructions, dependsOn, contextFiles, model, timeout, retries
- step.condition (CEL expression) - step only runs when condition evaluates to true
- step.include / top-level includes - include sub-workflows from external YAML files. step.include is mutually exclusive with agent, instructions, loop, condition, contextFiles, and model. An include step only has: id, include, dependsOn, timeout, retries.
- top-level: name (required), description (optional), version (optional, default 1)
- loop: maxIterations + untilAgent + delay between iterations (judge agent with resultSchema containing done boolean)
- loop.forEach - iterate over a list, running the step for each item
- loop.until (CEL expression) - loop terminates when expression evaluates to true
- loop.maxConcurrency - limit parallel iterations in forEach loops
- loop.steps - multi-step inner DAG within a loop iteration
- options: maxConcurrency, onStepFailure (cascade/skip-dependents/abort), timeout, stepTimeout
- options.scheduler - scheduling strategy: dependency-first (default), round-robin (alternate agents), least-busy (prefer idle agents)
- options.isolation - per-step environment isolation (e.g., worktree-per-step)
- CEL variables available: steps.<id>.content/result/status, iteration (0-based), item, index, content, result, status

## Instructions

1. Study the schema and supported features above. Use all relevant fields.
2. Define agents with description (required), prompt, tools (from available tools above), and other fields as needed. Do NOT set 'model', 'temperature', or 'topP' on agents - the orchestrator provides the default model and sampling parameters. Setting them causes failures on providers like Azure gpt-5 that reject non-default values.
3. Tools field: list tool names EXPLICITLY by name (e.g., ["read","bash","write"]). Do NOT use wildcards like "*" - wildcards are NOT supported and will be rejected by validation. Omit the field entirely if the agent needs no tools.
4. Define steps forming a DAG - no cycles. Steps without dependencies run in parallel. Step IDs MUST use snake_case (lowercase letters, digits, underscores only - NO hyphens, NO uppercase). Hyphenated IDs like "style-review" break CEL expressions in step.condition and step.loop.until because CEL does not allow hyphens in identifier chains - use "style_review" instead.
5. For iterative tasks, use loop with maxIterations + untilAgent or until (CEL). The untilAgent judge agent MUST have a resultSchema with a required done boolean field AND the judge's prompt MUST explicitly instruct it to call submit_result with {"done": true} when satisfied or {"done": false} with feedback when not. Without clear instructions, judges frequently loop indefinitely. Set maxIterations to a reasonable cap (3-5 for most tasks, 10 for complex ones). Design worker agents with resultSchema so they produce structured output the judge can evaluate.
   For forEach loops: the producer step MUST have a resultSchema defining the array field, and its instructions MUST tell the agent to call submit_result. The forEach CEL expression references the producer's structured result: "steps.<producer_id>.result.<array_field>". Example pattern:
   - Producer agent: resultSchema = {"type":"object","required":["items"],"properties":{"items":{"type":"array","items":{"type":"string"}}}}
   - Producer instructions: "... You MUST call submit_result with the items array."
   - forEach step: forEach = "steps.producer_step.result.items"
   CRITICAL: Without resultSchema + submit_result on the producer, steps.<id>.result is null and the forEach CEL expression returns an empty array or error. The content field (free text) CANNOT be iterated - only result (structured JSON) works with CEL.
6. For structured agent output, define resultSchema on the agent - the executor auto-injects a submit_result tool.
7. Use step.condition (CEL) to conditionally skip steps based on prior step results. CEL examples:
   - "steps.review.result.approved == true" - run only when a prior step approved something
   - "steps.check.result.issues_found && size(steps.check.result.issues_found) > 0" - run when a list is non-empty
   - "steps.plan.status == 'completed'" - compare against step status (string literal)
   - "has(steps.analyze.result.severity) && steps.analyze.result.severity == 'high'" - guard access with has()
   CRITICAL: Always use the FULL path steps.<id>.result.<field> or steps.<id>.content - NEVER use bare variable names like "poem" or "result". Only the steps.* namespace is available in CEL. Example: if step "write_poem" produced output, reference it as steps.write_poem.content (for text) or steps.write_poem.result.field_name (for structured result). Using bare names causes "undeclared reference" errors at runtime.
8. Set options (maxConcurrency, onStepFailure, scheduler, isolation, timeout) appropriate for the task.
9. Output ONLY valid JSON - no markdown, no explanation.

## Example

Here is a complete coordinator output using multiple schema features:

`)
	sb.WriteString(coordinatorExample)
	sb.WriteString("\n")
	return sb.String()
}

// BuildToolCatalog creates a human-readable listing of available tools.
func BuildToolCatalog(tools []goai.Tool) string {
	if len(tools) == 0 {
		return "(no tools available)"
	}
	var sb strings.Builder
	sb.Grow(len(tools) * 80)
	for i, t := range tools {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("- **")
		sb.WriteString(t.Name)
		sb.WriteString("**: ")
		sb.WriteString(t.Description)
	}
	return sb.String()
}

// ParseCoordinatorResponse parses the coordinator LLM output into a Workflow.
// Returns an error if JSON is invalid or the workflow fails validation.
// Returns *JSONParseError for malformed JSON, *CoordinatorValidationError for schema issues.
func ParseCoordinatorResponse(response string) (*Workflow, error) {
	// Size guard: reject responses larger than 1 MB before parsing.
	if len(response) > maxResponseSize {
		return nil, &JSONParseError{Err: fmt.Errorf("response too large: %d bytes (max %d)", len(response), maxResponseSize)}
	}

	// Strip markdown code fences if present.
	response = strings.TrimSpace(response)
	if strings.HasPrefix(response, "```") {
 // Remove opening fence (possibly with language tag).
		idx := strings.Index(response, "\n")
		if idx >= 0 {
			response = response[idx+1:]
		}
 // Remove closing fence.
		response = strings.TrimSuffix(response, "```")
		response = strings.TrimSpace(response)
	}

	var wf Workflow
	if err := json.Unmarshal([]byte(response), &wf); err != nil {
		return nil, &JSONParseError{Err: err}
	}

	// Sanitise Unicode bidi-override codepoints (U+202A-E, U+2066-9)
	// from coordinator-generated workflows BEFORE ValidateWorkflow. Static
	// YAML/JSON workflows already get this via ParseWorkflow /
	// ParseWorkflowJSON, but the coordinator path
	// previously skipped it - letting an LLM-injected payload smuggle
	// hidden visual reordering through `name` / `description` /
	// `instructions` fields straight into the executor and the agents
	// running them. We surface the violation as a coordinator
	// validation error so the retry loop can re-prompt.
	if err := SanitizeWorkflowUnicode(&wf); err != nil {
		return nil, &CoordinatorValidationError{Err: err}
	}

	if _, err := ValidateWorkflow(&wf); err != nil {
		return nil, &CoordinatorValidationError{Err: err}
	}

	// R7A-1: enforce size/depth caps so coordinator-emitted workflows respect
	// the same limits as static YAML/JSON workflows (parity with
	// ParseWorkflow / ParseWorkflowJSON). Without this, an LLM-emitted
	// workflow can bypass step-count, dependency-fan-out and nesting
	// caps that file-based workflows hit at parse time.
	if err := enforceLimits(&wf); err != nil {
		return nil, &CoordinatorValidationError{Err: err}
	}

	//-B: reject step IDs containing hyphens. Static YAML workflows
	// may use hyphens freely (schema allows them), but coordinator-generated
	// workflows often reference step results via CEL expressions in
	// step.condition and step.loop.until - and CEL does not allow hyphens
	// in identifier chains. A step ID like "style-review" combined with a
	// CEL expression "steps.style-review.result.approved" fails to compile
	// at runtime with "invalid argument to has macro" (observed on
	// G2_chain/azure-gpt5 during E2E). Rejecting here triggers the
	// coordinator retry loop which re-prompts with the error message.
	if badID := findHyphenatedStepID(wf.Steps); badID != "" {
		return nil, &CoordinatorValidationError{Err: fmt.Errorf(
			"step id %q contains a hyphen; step IDs must use snake_case or camelCase (letters, digits, underscores only) so they can be referenced in CEL condition/until expressions", badID)}
	}

	ApplyDefaults(&wf)

	// Strip model fields from coordinator-generated workflows.
	// The orchestrator provides the default model - coordinator-specified models
	// often don't match the runtime provider (e.g., LLM picks "gpt-4o" but
	// the test provider is bedrock-minimax which can't resolve it).
	// Strip Temperature and TopP for the same reason: the coordinator has no
	// idea which provider will execute the workflow, and some providers reject
	// non-default sampling parameters outright. Notably, Azure gpt-5 rejects
	// any temperature other than 1, so `zenflow goal --model azure-deployment/gpt-5`
	// failed 100% before this strip. Stripping is safe because the
	// executor falls back to provider defaults when these fields are nil, and
	// production users who need specific sampling should set them on the
	// agents directly in YAML rather than relying on coordinator output.
	for name, agent := range wf.Agents {
		agent.Model = ""
		agent.Temperature = nil
		agent.TopP = nil
		wf.Agents[name] = agent
	}
	for i := range wf.Steps {
		wf.Steps[i].Model = ""
	}

	// P7.7.10: Strip timeout and stepTimeout from coordinator-generated workflows.
	// Coordinator LLMs set aggressive timeouts in generated JSON (e.g., "5m" for
	// complex multi-step goals), causing steps to hit `context deadline exceeded`
	// on slower providers. Stripping is safe because users who need specific
	// timeouts should pass --timeout via CLI or set them in static YAML.
	// Same rationale as temperature/topP stripping above: the coordinator has
	// no knowledge of which provider will execute the workflow.
	wf.Options.Timeout = 0
	wf.Options.StepTimeout = 0
	stripStepTimeouts(wf.Steps)

	return &wf, nil
}

// stripStepTimeouts recursively clears timeout from all steps (including loop inner steps).
func stripStepTimeouts(steps []Step) {
	for i := range steps {
		steps[i].Timeout = 0
		if steps[i].Loop != nil {
			stripStepTimeouts(steps[i].Loop.Steps)
		}
	}
}

// findHyphenatedStepID walks every step in the workflow (including nested
// loop.steps) and returns the first step ID that contains a hyphen, or ""
// if all step IDs are CEL-safe. Used by ParseCoordinatorResponse.
func findHyphenatedStepID(steps []Step) string {
	for _, s := range steps {
		if strings.Contains(s.ID, "-") {
			return s.ID
		}
		if s.Loop != nil {
			if bad := findHyphenatedStepID(s.Loop.Steps); bad != "" {
				return bad
			}
		}
	}
	return ""
}

// ValidateToolNames checks that all tool names referenced in agent configs exist
// in the provided tool catalog. Returns errToolNotFound for the first unknown tool.
// also rejects wildcard entries like "*" with a specific error message.
// Coordinator LLMs sometimes emit `"tools": ["*"]` as shorthand for "all
// tools", but zenflow requires explicit names. A generic "unknown tool" error
// would leave the user wondering whether their catalog is misspelled; the
// specific message tells them the real fix.
func ValidateToolNames(wf *Workflow, tools []goai.Tool) error {
	catalog := make(map[string]bool, len(tools))
	for _, t := range tools {
		catalog[t.Name] = true
	}
	for name, agent := range wf.Agents {
		for _, tool := range agent.Tools {
			if tool == "*" {
				return &ToolNotFoundError{
					Tool:  tool,
					Agent: name,
				}
			}
			if !catalog[tool] {
				return &ToolNotFoundError{Tool: tool, Agent: name}
			}
		}
	}
	return nil
}
