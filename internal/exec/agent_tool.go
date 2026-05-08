package exec

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/types"
)

// ErrAgentToolDirectInvocation is returned by AgentToolDef's Execute
// stub when the agent tool is invoked directly through the goai tool
// loop. The agent tool is intercepted by the AgentRunner spawner hook
// (OnBeforeToolExecute); reaching the Execute body indicates a wiring
// bug in the consumer (spawner not registered or hook bypassed). The
// sentinel allows callers to detect this via errors.Is instead of
// substring matching, while preserving the (string, error) return
// contract so the goai loop surfaces a clean tool-result error rather
// than panicking mid-conversation.
var ErrAgentToolDirectInvocation = errors.New("zenflow: agent tool must be handled by spawner, not executed directly")

// agentNameInvalidCharRE matches any character that Bedrock and OpenAI
// reject in tool-call names (regex [a-zA-Z0-9_-]+). Used by
// sanitizeAgentName to strip LLM-hallucinated chars that would otherwise
// fail provider validation on the NEXT request.
var agentNameInvalidCharRE = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// sanitizeAgentName produces a Bedrock-/OpenAI-safe child agent name by
// stripping every char outside [a-zA-Z0-9_-]. Empty / all-invalid input
// falls back to "agent" so the child still runs.
func sanitizeAgentName(name string) string {
	clean := agentNameInvalidCharRE.ReplaceAllString(name, "")
	return cmp.Or(clean, "agent")
}

type agentToolParams struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Prompt          string   `json:"prompt"`
	Tools           []string `json:"tools,omitempty"`
	Model           string   `json:"model,omitempty"`
	Instructions    string   `json:"instructions"`
	RunInBackground bool     `json:"run_in_background"`
}

// agentSpawner handles child agent creation and execution.
type agentSpawner struct {
	Model        provider.LanguageModel
	Tools        []goai.Tool
	GoAIOptions  []goai.Option
	Permissions  PermissionHandler
	Progress     ProgressSink
	Router       *MessageRouter
	DefaultModel string
	MaxDepth     int
	MaxTurns     int // inherited from parent; children use this if > 0
	MaxChildren  int // max concurrent+completed children (default 10)
	CurrentDepth int
	ParentTools  []string // parent's available tool names; children are subset-checked

	mu          sync.Mutex
	children    []*AgentResult
	childErrors []error // errors from async children (stored even if Progress is nil)
	childWg     sync.WaitGroup
	childCount  int // for unique child IDs
}

// Compile-time assertion that *agentSpawner satisfies the childSpawner
// contract. Catches signature drift on either side at the type definition
// rather than at the assignment in AgentRunner.spawner.
var _ childSpawner = (*agentSpawner)(nil)

// AgentToolDef returns the tool definition for the agent spawning tool.
// Schema notes:
// - `model`: omit field entirely to inherit parent's model. Do NOT pass
// literal strings like "default" or "auto" - those are interpreted as
// model identifiers and fail provider lookup.
// - `tools`: must be a SUBSET of the parent's available tool names.
// Pass the names the child genuinely needs; an empty array inherits
// parent's full tool set.
// - `run_in_background`: keep `false` unless you explicitly need fire-
// and-forget. Backgrounded children deliver results to the inbox on
// the parent's next turn.
func AgentToolDef() goai.Tool {
	return goai.Tool{
		Name:        toolNameAgent,
		Description: "Spawn a child agent to handle a focused subtask. The child runs with its own LLM context and returns its result string.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {"type": "string", "description": "Short identifier for this child (alphanumeric, underscore, dash; matches Bedrock/OpenAI tool-name regex [a-zA-Z0-9_-]+)"},
				"description": {"type": "string", "description": "Short description of the child's role (one sentence)"},
				"prompt": {"type": "string", "description": "Role/persona prompt for the child"},
				"tools": {"type": "array", "items": {"type": "string"}, "description": "Subset of parent's tool names the child may call. Omit or pass empty to inherit parent's full tool set."},
				"model": {"type": "string", "description": "OPTIONAL model identifier. OMIT this field entirely to inherit the parent's model. Do NOT pass strings like 'default', 'auto', or 'gpt-4' unless that exact provider/model is configured - invalid identifiers fall back to the parent default but emit a warning."},
				"instructions": {"type": "string", "description": "Specific task instructions for this invocation"},
				"run_in_background": {"type": "boolean", "description": "If true, launch async and return immediately. Default false."}
			},
			"required": ["name", "instructions"]
		}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
 // No-op placeholder: actual dispatch handled by OnBeforeToolExecute hook in AgentRunner.
			return "", ErrAgentToolDirectInvocation
		},
	}
}

// invalidModelHallucinations enumerates strings that LLMs commonly emit
// in the `model` field due to schema-description ambiguity or training-
// data leak. When `params.Model` matches one of these, the spawner
// silently treats it as empty and falls back to `s.DefaultModel`,
// avoiding both the "model 'X' not found" hard error and the warning
// noise that pollutes the chat surface.
// Add to this list as new patterns surface; do NOT add legitimate
// model identifiers that some providers happen to expose under the
// same string (the cost of a missed entry is one warning, not data
// loss).
var invalidModelHallucinations = map[string]bool{
	// Schema-description leaks (LLM interprets "default" as the value).
	"default": true,
	"auto":    true,
	"parent":  true,
	"inherit": true,
	// Training-data leaks of OpenAI model names that are not in any
	// configured Bedrock/Azure/Google registry. These appear when LLMs
	// reflexively fill `model` with a memorised string instead of
	// omitting the field. Sonnet, GPT-5, etc. all do this occasionally.
	"gpt-4":         true,
	"gpt-4o":        true,
	"gpt-4o-mini":   true,
	"gpt-3.5-turbo": true,
}

// isLikelyHallucinatedModel reports whether `model` looks like a
// schema-misinterpretation or training-data leak rather than a real
// model identifier. Used by SpawnChild to silently fall back to the
// parent's DefaultModel without emitting noisy warnings.
func isLikelyHallucinatedModel(model string) bool {
	return invalidModelHallucinations[model]
}

// intersectTools returns only the tool names present in both requested and allowed.
// If requested is empty, returns allowed. If allowed is empty, returns requested as-is.
func intersectTools(requested, allowed []string) []string {
	if len(allowed) == 0 {
		return requested
	}
	if len(requested) == 0 {
		return nil // no specific tools requested - use Available() default
	}
	set := make(map[string]bool, len(allowed))
	for _, t := range allowed {
		set[t] = true
	}
	result := make([]string, 0, len(requested))
	for _, t := range requested {
		if set[t] {
			result = append(result, t)
		}
	}
	return result
}

// SpawnChild creates and runs a child agent. Returns the result text for sync, or "task launched" for async.
func (s *agentSpawner) SpawnChild(ctx context.Context, call provider.ToolCall) (string, error) {
	var params agentToolParams
	if err := json.Unmarshal(call.Input, &params); err != nil {
		return "invalid agent params: " + err.Error(), nil
	}

	// Sanitize the child name so downstream Bedrock / OpenAI tool-call
	// validation does not reject the entire conversation when the LLM
	// emits names like "[TOOL_CALLS]agent" or "Multi_tool_use.parallel".
	// Both providers enforce the regex [a-zA-Z0-9_-]+ on tool names; a
	// single invalid char anywhere in the conversation history fails
	// the next request, so we strip them at spawn time. Empty after
	// sanitisation falls back to a generic "agent" so the call still
	// proceeds.
	params.Name = sanitizeAgentName(params.Name)

	if s.CurrentDepth >= s.MaxDepth {
		return fmt.Sprintf("max agent depth %d reached", s.MaxDepth), nil
	}

	// #1: Check MaxChildren limit and reserve a slot atomically.
	maxChildren := s.MaxChildren
	if maxChildren <= 0 {
		maxChildren = 10
	}
	s.mu.Lock()
	if s.childCount >= maxChildren {
		s.mu.Unlock()
		return fmt.Sprintf("max children %d reached", maxChildren), nil
	}
	s.childCount++
	childID := fmt.Sprintf("%s-%d", params.Name, s.childCount)
	s.mu.Unlock()

	// #4: Resolve effective model. LLMs frequently emit schema-description
	// leaks ("default", "auto") or training-data leaks ("gpt-4") in the
	// model field. Treat those as empty (silent inherit) so the child
	// runs with the parent's model instead of failing provider lookup.
	model := params.Model
	switch {
	case model == "":
		model = s.DefaultModel
	case isLikelyHallucinatedModel(model):
 // Silent fallback - no warning. The fallback IS the contract for
 // schema-misinterpretation; warning noise was the real bug.
		model = s.DefaultModel
	case model != s.DefaultModel && s.Progress != nil:
 // Genuine override (LLM explicitly chose a different real model).
 // Keep the warning so operators can spot deliberate divergence.
		s.Progress.OnEvent(ctx, Event{
			Type:    types.EventMessage,
			Message: fmt.Sprintf("child agent %q requested model %q (default: %q)", params.Name, model, s.DefaultModel),
		})
	}

	// #5: Use parent's MaxTurns instead of hardcoded 50.
	maxTurns := s.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}

	cfg := AgentConfig{
		Description: params.Description,
		Prompt:      params.Prompt,
		Model:       model,
		Tools:       params.Tools,
		MaxTurns:    maxTurns,
	}

	// #3: Subset-check requested tools against parent's available tools.
	childToolNames := intersectTools(params.Tools, s.ParentTools)

	// Get available tools for child.
	tools := FilterTools(s.Tools, childToolNames, nil)

	// Build child spawner for recursive spawning.
	childSpawner := &agentSpawner{
		Model:        s.Model,
		Tools:        s.Tools,
		GoAIOptions:  s.GoAIOptions,
		Permissions:  s.Permissions,
		Progress:     s.Progress,
		Router:       s.Router,
		DefaultModel: s.DefaultModel,
		MaxDepth:     s.MaxDepth,
		MaxTurns:     maxTurns,
		MaxChildren:  maxChildren,
		CurrentDepth: s.CurrentDepth + 1,
		ParentTools:  childToolNames,
	}

	// Add agent tool to child's tools (for recursive spawning).
	tools = append(tools, AgentToolDef())

	// When the parent has a Router (RunAgent now plumbs one), the child
	// runner inherits the same router + mailbox so siblings can exchange
	// messages. SpawnDepth + SpawnParentCallID are populated so
	// EventToolCall emissions carry the metadata the TUI needs to collapse
	// nested spawns.
	childRunner := NewAgentRunner(
		WithRunnerModel(s.Model),
		WithRunnerTools(s.Tools...),
		WithRunnerGoAIOptions(s.GoAIOptions...),
		WithRunnerPermissions(s.Permissions),
		WithRunnerProgress(s.Progress),
		WithRunnerRouter(s.Router),
		WithRunnerStepID(childID),
		WithRunnerSpawnDepth(childSpawner.CurrentDepth),
		WithRunnerSpawnParentCallID(call.ID),
 // : subagent role (cfg.Prompt) flows to the system slot
 // via goai.WithSystem, mirroring the workflow-agent migration
 // in . The LLM-supplied `prompt` task-tool param is the
 // per-subagent template (option (b) in the original 
 // design): the parent agent picks the subagent's identity,
 // the system slot routes it to the LLM with proper
 // instruction-following weight + safety filtering.
		WithRunnerSystemPrompt(cfg.Prompt),
	)
	childRunner.spawner = childSpawner
	if s.Router != nil {
		if mb := s.Router.Mailbox(); mb != nil {
			childRunner.mailbox = mb
			childRunner.wake = make(chan struct{}, 1)
			s.Router.RegisterStep(childID)
			s.Router.RegisterInbox(childID)
		}
	}

	// : cfg.Prompt now flows to system via WithRunnerSystemPrompt
	// above. The user message carries only the task instructions.
	// Pre-fix, role + task were concatenated under "## Agent Role" /
	// "## Task" headers; that mixed identity into the user slot and
	// matched the same anti-pattern retired for workflow agents.
	prompt := params.Instructions

	// closeChildInbox tears down the child's inbox slot on the shared
	// router. Symmetric to the per-call mailbox close done by RunAgent
	// for the primary; ensures sibling children do not leak open-step
	// entries on the router map.
	closeChildInbox := func() {
		if s.Router != nil && childRunner.mailbox != nil {
			s.Router.Close(childID)
		}
	}

	if params.RunInBackground {
 // Async: launch goroutine, return immediately.
		s.childWg.Go(func() {
			defer closeChildInbox()
			result, err := childRunner.Run(ctx, cfg, prompt, model, tools)
			s.mu.Lock()
			if err != nil {
 // #9: Always store error in childErrors.
				s.childErrors = append(s.childErrors, fmt.Errorf("async child %q: %w", params.Name, err))
			}
			if result != nil {
				s.children = append(s.children, result)
			}
			s.mu.Unlock()
			if err != nil && s.Progress != nil {
				s.Progress.OnEvent(ctx, Event{
					Type:      types.EventError,
					AgentName: childID,
					Error:     fmt.Errorf("async child %q: %w", params.Name, err),
				})
			}
		})
		return fmt.Sprintf("Agent %q launched in background.", params.Name), nil
	}

	// Sync: block until child completes.
	defer closeChildInbox()
	result, err := childRunner.Run(ctx, cfg, prompt, model, tools)
	if err != nil {
 // Return BOTH the descriptive text (for the LLM consumer) and a
 // non-nil error. Previously returned (string, nil) which made the
 // tool-call wrapper render a green ✓ glyph despite the content
 // being an error message. With a non-nil error the wrapper
 // correctly flips to × and the chat surface no longer lies about
 // success.
		return "agent error: " + err.Error(), fmt.Errorf("agent %q: %w", params.Name, err)
	}

	s.mu.Lock()
	s.children = append(s.children, result)
	s.mu.Unlock()

	return result.Content, nil
}

// FilterTools returns a subset of tools based on allow/disallow lists.
// If allow is non-empty, only tools whose names appear in allow are returned.
// Tools whose names appear in disallow are always excluded.
func FilterTools(tools []goai.Tool, allow, disallow []string) []goai.Tool {
	dis := make(map[string]bool, len(disallow))
	for _, name := range disallow {
		dis[name] = true
	}

	var allowSet map[string]bool
	if len(allow) > 0 {
		allowSet = make(map[string]bool, len(allow))
		for _, name := range allow {
			allowSet[name] = true
		}
	}

	result := make([]goai.Tool, 0, len(tools))
	for _, t := range tools {
		if dis[t.Name] {
			continue
		}
		if allowSet != nil && !allowSet[t.Name] {
			continue
		}
		result = append(result, t)
	}
	return result
}
