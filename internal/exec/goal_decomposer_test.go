package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/spec"
)

var _ goai.Tool // ensure import used

func TestCoordinatorPrompt(t *testing.T) {
	prompt := CoordinatorPrompt("Add OAuth2", "- **read**: read files\n- **bash**: run commands")
	if !strings.Contains(prompt, "Add OAuth2") {
		t.Error("prompt should contain the goal")
	}
	if !strings.Contains(prompt, "read") {
		t.Error("prompt should contain tool catalog")
	}
	if !strings.Contains(prompt, "coordinator") {
		t.Error("prompt should mention coordinator role")
	}
	if !strings.Contains(prompt, "JSON") {
		t.Error("prompt should mention JSON output format")
	}
	// Verify embedded spec schema is included.
	if !strings.Contains(prompt, "$schema") {
		t.Error("prompt should contain the embedded JSON Schema")
	}
	if !strings.Contains(prompt, "AgentConfig") {
		t.Error("prompt should contain AgentConfig definition from schema")
	}
	// Verify embedded example is included.
	if !strings.Contains(prompt, "refactor_auth_module") {
		t.Error("prompt should contain the coordinator-output.json example")
	}
	// Verify spec features are mentioned in instructions.
	if !strings.Contains(prompt, "resultSchema") {
		t.Error("prompt should mention resultSchema")
	}
	if !strings.Contains(prompt, "loop") {
		t.Error("prompt should mention loop")
	}
}

func TestBuildToolCatalog_Empty(t *testing.T) {
	result := BuildToolCatalog(nil)
	if result != "(no tools available)" {
		t.Errorf("got %q, want %q", result, "(no tools available)")
	}
}

func TestBuildToolCatalog(t *testing.T) {
	tools := []goai.Tool{
		{Name: "read", Description: "read files"},
		{Name: "bash", Description: "run commands"},
	}
	result := BuildToolCatalog(tools)
	if !strings.Contains(result, "**read**") {
		t.Error("should contain tool name in bold")
	}
	if !strings.Contains(result, "read files") {
		t.Error("should contain tool description")
	}
}

func TestParseCoordinatorResponse_Valid(t *testing.T) {
	resp := `{
		"name": "test-plan",
		"agents": {
			"coder": {
				"description": "writes code",
				"prompt": "You are a coder",
				"tools": ["read"]
			}
		},
		"steps": [
			{"id": "step1", "agent": "coder", "instructions": "do something"}
		]
	}`
	wf, err := ParseCoordinatorResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wf.Name != "test-plan" {
		t.Errorf("name = %q, want %q", wf.Name, "test-plan")
	}
	if len(wf.Steps) != 1 {
		t.Errorf("steps = %d, want 1", len(wf.Steps))
	}
}

func TestParseCoordinatorResponse_WithCodeFence(t *testing.T) {
	resp := "```json\n" + `{
		"name": "test-plan",
		"steps": [{"id": "step1", "instructions": "do it"}]
	}` + "\n```"
	wf, err := ParseCoordinatorResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wf.Name != "test-plan" {
		t.Errorf("name = %q, want %q", wf.Name, "test-plan")
	}
}

func TestParseCoordinatorResponse_InvalidJSON(t *testing.T) {
	_, err := ParseCoordinatorResponse("not json at all")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	var jsonErr *JSONParseError
	if !errors.As(err, &jsonErr) {
		t.Errorf("error type = %T, want *JSONParseError", err)
	}
}

func TestParseCoordinatorResponse_ValidationError(t *testing.T) {
	// Valid JSON but missing required name field.
	resp := `{"steps": [{"id": "step1"}]}`
	_, err := ParseCoordinatorResponse(resp)
	if err == nil {
		t.Fatal("expected validation error")
	}
	var valErr *CoordinatorValidationError
	if !errors.As(err, &valErr) {
		t.Errorf("error type = %T, want *CoordinatorValidationError", err)
	}
}

// TestJSONParseError_Error_UnmarshalTypeError covers the
// json.UnmarshalTypeError branch of (*JSONParseError).Error()
// (goal_decomposer.go:88-91). A type-mismatch JSON (e.g. number where
// string expected) produces *json.UnmarshalTypeError; the Error()
// formatter must emit "json parse at offset N".
func TestJSONParseError_Error_UnmarshalTypeError(t *testing.T) {
	// Workflow.Name is a string; supply a JSON number to trigger
	// json.UnmarshalTypeError.
	var wf Workflow
	innerErr := json.Unmarshal([]byte(`{"name": 42}`), &wf)
	if innerErr == nil {
		t.Fatal("expected json.UnmarshalTypeError from {\"name\":42}")
	}
	var ute *json.UnmarshalTypeError
	if !errors.As(innerErr, &ute) {
		t.Fatalf("inner error type = %T, want *json.UnmarshalTypeError", innerErr)
	}
	parseErr := &JSONParseError{Err: innerErr}
	msg := parseErr.Error()
	if !strings.Contains(msg, "at offset") {
		t.Errorf("Error() = %q, want 'at offset' (UnmarshalTypeError branch)", msg)
	}
}

// TestParseCoordinatorResponse_EnforceLimits covers the enforceLimits
// failure branch in ParseCoordinatorResponse (goal_decomposer.go:260-262).
// A workflow with more steps than MaxStepsPerWorkflow is rejected as a
// CoordinatorValidationError.
func TestParseCoordinatorResponse_EnforceLimits(t *testing.T) {
	// Build a workflow with too many steps.
	var sb strings.Builder
	sb.WriteString(`{"name":"too-many","steps":[`)
	for i := 0; i <= MaxStepsPerWorkflow; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"id":"s%d","instructions":"x"}`, i)
	}
	sb.WriteString(`]}`)
	_, err := ParseCoordinatorResponse(sb.String())
	if err == nil {
		t.Fatal("expected enforceLimits error for over-budget step count")
	}
	var ve *CoordinatorValidationError
	if !errors.As(err, &ve) {
		t.Errorf("error type = %T, want *CoordinatorValidationError", err)
	}
}

func TestParseCoordinatorResponse_TooLarge(t *testing.T) {
	// Response exceeding 1 MB should be rejected before parsing.
	huge := strings.Repeat("x", maxResponseSize+1)
	_, err := ParseCoordinatorResponse(huge)
	if err == nil {
		t.Fatal("expected error for oversized response")
	}
	var jsonErr *JSONParseError
	if !errors.As(err, &jsonErr) {
		t.Errorf("error type = %T, want *JSONParseError", err)
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %q, want 'too large' mention", err)
	}
}

// TestParseCoordinatorResponse_RejectsHyphenatedStepID is the-B regression
// test. Coordinator LLMs occasionally emit kebab-case step IDs (e.g.,
// "style-review") together with CEL expressions like
// "steps.style-review.result.approved" - CEL does not allow hyphens in
// identifier chains, so the workflow fails to compile at runtime with
// "invalid argument to has() macro". Observed on G2_chain/azure-gpt5 during
// E2E. ParseCoordinatorResponse must reject hyphenated step IDs
// through the coordinator validation retry path so the LLM re-emits with
// CEL-safe IDs.
func TestParseCoordinatorResponse_RejectsHyphenatedStepID(t *testing.T) {
	resp := `{
		"name": "zfb8b-hyphen",
		"agents": {
			"reviewer": {"description": "reviews"}
		},
		"steps": [
			{"id": "draft", "agent": "reviewer", "instructions": "write"},
			{"id": "style-review", "agent": "reviewer", "dependsOn": ["draft"], "instructions": "review"}
		]
	}`
	_, err := ParseCoordinatorResponse(resp)
	if err == nil {
		t.Fatal("expected error for hyphenated step id, got nil")
	}
	var valErr *CoordinatorValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("error type = %T, want *CoordinatorValidationError", err)
	}
	if !strings.Contains(err.Error(), "style-review") {
		t.Errorf("error = %q, want to mention 'style-review'", err)
	}
	if !strings.Contains(err.Error(), "snake_case") && !strings.Contains(err.Error(), "camelCase") {
		t.Errorf("error = %q, want to suggest snake_case/camelCase", err)
	}
}

// TestParseCoordinatorResponse_RejectsHyphenatedNestedLoopStepID verifies
// the walker descends into loop.steps so hyphens nested inside a loop are
// also caught.
func TestParseCoordinatorResponse_RejectsHyphenatedNestedLoopStepID(t *testing.T) {
	resp := `{
		"name": "zfb8b-nested",
		"agents": {
			"coder": {"description": "codes"},
			"judge": {
				"description": "judges",
				"resultSchema": {
					"type": "object",
					"required": ["done"],
					"properties": {"done": {"type": "boolean"}}
				}
			}
		},
		"steps": [
			{
				"id": "dev_cycle",
				"loop": {
					"maxIterations": 3,
					"untilAgent": "judge",
					"steps": [
						{"id": "implement", "agent": "coder", "instructions": "code"},
						{"id": "run-tests", "agent": "coder", "dependsOn": ["implement"], "instructions": "test"}
					]
				}
			}
		]
	}`
	_, err := ParseCoordinatorResponse(resp)
	if err == nil {
		t.Fatal("expected error for nested hyphenated loop step id, got nil")
	}
	if !strings.Contains(err.Error(), "run-tests") {
		t.Errorf("error = %q, want to mention 'run-tests'", err)
	}
}

// TestParseCoordinatorResponse_AcceptsSnakeCase verifies the happy
// path for snake_case step IDs. R7A-1: ParseCoordinatorResponse now
// runs enforceLimits, which uses strictStepIDPattern (lowercase only) -
// matching the YAML/JSON parse paths. camelCase IDs are no longer
// accepted via the coordinator path; the coordinator prompt now
// instructs the LLM to emit snake_case only.
func TestParseCoordinatorResponse_AcceptsSnakeCase(t *testing.T) {
	resp := `{
		"name": "zfb8b-happy",
		"agents": {"worker": {"description": "works"}},
		"steps": [
			{"id": "draft_plan", "agent": "worker", "instructions": "plan"},
			{"id": "review_code", "agent": "worker", "dependsOn": ["draft_plan"], "instructions": "review"},
			{"id": "step3", "agent": "worker", "dependsOn": ["review_code"], "instructions": "finalize"}
		]
	}`
	if _, err := ParseCoordinatorResponse(resp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestValidateToolNames_WildcardRejected is the regression test.
// Coordinator LLMs sometimes emit `"tools": ["*"]` as a shorthand - that
// must be rejected with a helpful error message, not a generic
// "unknown tool" error.
func TestValidateToolNames_WildcardRejected(t *testing.T) {
	wf := &Workflow{
		Name: "zfb8-wildcard",
		Agents: map[string]AgentConfig{
			"greedy": {
				Description: "wants all tools",
				Tools:       []string{"*"},
			},
		},
		Steps: []Step{{ID: "s1", Agent: "greedy", Instructions: "do it"}},
	}
	// Catalog has real tools; validator must still reject the wildcard.
	catalog := []goai.Tool{
		{Name: "read"},
		{Name: "bash"},
	}
	err := ValidateToolNames(wf, catalog)
	if err == nil {
		t.Fatal("expected error for wildcard tool, got nil")
	}
	var toolErr *ToolNotFoundError
	if !errors.As(err, &toolErr) {
		t.Fatalf("error type = %T, want *ToolNotFoundError", err)
	}
	if toolErr.Tool != "*" {
		t.Errorf("Tool = %q, want %q", toolErr.Tool, "*")
	}
	if !strings.Contains(err.Error(), "wildcard") {
		t.Errorf("error message = %q, want to mention 'wildcard'", err)
	}
	if !strings.Contains(err.Error(), "list tools explicitly") {
		t.Errorf("error message = %q, want to advise listing tools explicitly", err)
	}
}

// TestParseCoordinatorResponse_StripsModelAndSamplingParams is the
// regression test. Coordinator LLMs frequently emit temperature, topP, and
// model on their generated agents even when the prompt tells them not to.
// Those fields can break execution on providers that reject non-default
// sampling parameters (notably Azure gpt-5, which rejects any temperature
// other than 1, causing `zenflow goal --model azure-deployment/gpt-5` to
// fail 100% before the strip landed).
// ParseCoordinatorResponse must strip Model/Temperature/TopP from every
// agent so the orchestrator's defaults take effect.
func TestParseCoordinatorResponse_StripsModelAndSamplingParams(t *testing.T) {
	resp := `{
		"name": "zfb2-test",
		"agents": {
			"coder": {
				"description": "writes code",
				"prompt": "You are a coder",
				"model": "gpt-4o",
				"temperature": 0.2,
				"topP": 0.9,
				"tools": ["read"]
			},
			"reviewer": {
				"description": "reviews code",
				"model": "claude-3-5-sonnet",
				"temperature": 0.7
			}
		},
		"steps": [
			{"id": "write", "agent": "coder", "instructions": "write something", "model": "gpt-4o-mini"},
			{"id": "review", "agent": "reviewer", "dependsOn": ["write"], "instructions": "review it"}
		]
	}`
	wf, err := ParseCoordinatorResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for name, agent := range wf.Agents {
		if agent.Model != "" {
			t.Errorf("agent %q: Model = %q, want empty (stripped)", name, agent.Model)
		}
		if agent.Temperature != nil {
			t.Errorf("agent %q: Temperature = %v, want nil (stripped)", name, *agent.Temperature)
		}
		if agent.TopP != nil {
			t.Errorf("agent %q: TopP = %v, want nil (stripped)", name, *agent.TopP)
		}
	}
	for i, s := range wf.Steps {
		if s.Model != "" {
			t.Errorf("step %d (%s): Model = %q, want empty (stripped)", i, s.ID, s.Model)
		}
	}
}

func TestValidateToolNames(t *testing.T) {
	wf := &Workflow{
		Name: "test",
		Agents: map[string]AgentConfig{
			"coder": {Tools: []string{"read", "bash"}},
		},
		Steps: []Step{{ID: "s1", Agent: "coder", Instructions: "do"}},
	}
	catalog := []goai.Tool{
		{Name: "read", Description: "read files"},
		{Name: "bash", Description: "run commands"},
	}
	if err := ValidateToolNames(wf, catalog); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Unknown tool should fail.
	wf.Agents["coder"] = AgentConfig{Tools: []string{"read", "nonexistent"}}
	err := ValidateToolNames(wf, catalog)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	var toolErr *ToolNotFoundError
	if !errors.As(err, &toolErr) {
		t.Errorf("error type = %T, want *ToolNotFoundError", err)
	}
	if toolErr.Tool != "nonexistent" {
		t.Errorf("tool = %q, want %q", toolErr.Tool, "nonexistent")
	}
}

func TestRunGoal_ToolValidation(t *testing.T) {
	// Coordinator returns workflow with agent referencing a tool not in catalog.
	coordResp := `{
		"name": "test-plan",
		"agents": {
			"coder": {
				"description": "codes",
				"prompt": "code",
				"tools": ["read", "deploy"]
			}
		},
		"steps": [{"id": "s1", "agent": "coder", "instructions": "do"}]
	}`
	llm := &coordinatorLLM{responses: []string{coordResp}}
	tools := []goai.Tool{{Name: "read", Description: "read files"}}
	orch := New(WithModel(llm), WithTools(tools...), WithDefaultModel("gpt-4o"))
	_, err := orch.RunGoal(t.Context(), "Build something")
	if err == nil {
		t.Fatal("expected error for unknown tool in agent config")
	}
	var toolErr *ToolNotFoundError
	if !errors.As(err, &toolErr) {
		t.Errorf("error type = %T, want *ToolNotFoundError; err = %v", err, err)
	}
}

func TestCoordinatorChat(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "coordinator response", Usage: provider.Usage{InputTokens: 50, OutputTokens: 100}, FinishReason: provider.FinishStop},
		},
	}
	content, tokens, err := CoordinatorChat(t.Context(), model, "test prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "coordinator response" {
		t.Errorf("content = %q, want %q", content, "coordinator response")
	}
	if tokens.InputTokens != 50 || tokens.OutputTokens != 100 {
		t.Errorf("tokens = %+v, want {50, 100}", tokens)
	}
	// Verify the model was called.
	calls := model.getCalls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
}

func TestCoordinatorChat_DoGenerateError(t *testing.T) {
	innerErr := errors.New("upstream LLM unavailable")
	llm := &failingGenerateModel{err: innerErr}
	_, _, err := CoordinatorChat(t.Context(), llm, "prompt")
	if err == nil {
		t.Fatalf("expected error from DoGenerate failure, got nil")
	}
	if !strings.Contains(err.Error(), "coordinator llm") {
		t.Errorf("err=%v want wrap prefix 'coordinator llm'", err)
	}
	if !errors.Is(err, innerErr) {
		t.Errorf("err does not wrap inner DoGenerate error via fmt.Errorf %%w; errors.Is returned false. err=%v", err)
	}
}

// failingGenerateModel returns its err from DoGenerate; DoStream is unused.
type failingGenerateModel struct {
	err error
}

func (m *failingGenerateModel) ModelID() string { return "failing-generate" }
func (m *failingGenerateModel) Capabilities() provider.ModelCapabilities {
	return provider.ModelCapabilities{}
}
func (m *failingGenerateModel) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	return nil, m.err
}
func (m *failingGenerateModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, m.err
}

// --- RunGoal tests ---

// coordinatorLLM returns a valid workflow JSON on the first call, then
// simple responses for agent step calls. Implements provider.LanguageModel.
type coordinatorLLM struct {
	mu         sync.Mutex
	calls      []provider.GenerateParams
	responses  []string // raw JSON strings to return in order
	idx        int
	agentCalls int // count of non-coordinator (agent step) calls
}

func (c *coordinatorLLM) ModelID() string { return "coordinator-mock" }

func (c *coordinatorLLM) DoGenerate(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, params)

	if c.idx < len(c.responses) {
		resp := c.responses[c.idx]
		c.idx++
		return &provider.GenerateResult{
			Text:         resp,
			Usage:        provider.Usage{InputTokens: 10, OutputTokens: 20},
			FinishReason: provider.FinishStop,
		}, nil
	}
	c.agentCalls++
	return &provider.GenerateResult{
		Text:         "step done",
		Usage:        provider.Usage{InputTokens: 5, OutputTokens: 5},
		FinishReason: provider.FinishStop,
	}, nil
}

func (c *coordinatorLLM) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, errors.New("not implemented")
}

// streamingCoordinatorLLM streams the coordinator JSON response and
// non-streaming responses for agent steps. Used for RunGoal streaming path.
type streamingCoordinatorLLM struct {
	mu         sync.Mutex
	calls      []provider.GenerateParams
	responses  []string
	idx        int
	agentCalls int
}

func (c *streamingCoordinatorLLM) ModelID() string { return "streaming-coordinator-mock" }

func (c *streamingCoordinatorLLM) DoGenerate(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, params)
	if c.idx < len(c.responses) {
		resp := c.responses[c.idx]
		c.idx++
		return &provider.GenerateResult{Text: resp, Usage: provider.Usage{InputTokens: 10, OutputTokens: 20}, FinishReason: provider.FinishStop}, nil
	}
	c.agentCalls++
	return &provider.GenerateResult{Text: "step done", Usage: provider.Usage{InputTokens: 5, OutputTokens: 5}, FinishReason: provider.FinishStop}, nil
}

func (c *streamingCoordinatorLLM) DoStream(_ context.Context, params provider.GenerateParams) (*provider.StreamResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, params)
	var text string
	if c.idx < len(c.responses) {
		text = c.responses[c.idx]
		c.idx++
	} else {
		c.agentCalls++
		text = "step done"
	}
	ch := make(chan provider.StreamChunk, 10)
	// Emit reasoning chunk first, then text.
	ch <- provider.StreamChunk{Type: provider.ChunkReasoning, Text: "thinking about plan"}
	ch <- provider.StreamChunk{Type: provider.ChunkText, Text: text}
	ch <- provider.StreamChunk{Type: provider.ChunkFinish, FinishReason: provider.FinishStop, Usage: provider.Usage{InputTokens: 10, OutputTokens: 20}}
	close(ch)
	return &provider.StreamResult{Stream: ch}, nil
}

const validCoordinatorJSON = `{
	"name": "auto-plan",
	"agents": {
		"worker": {
			"description": "does work",
			"prompt": "You are a worker"
		}
	},
	"steps": [
		{"id": "task1", "agent": "worker", "instructions": "Do task 1"},
		{"id": "task2", "agent": "worker", "dependsOn": ["task1"], "instructions": "Do task 2"}
	]
}`

func TestRunGoal_Success(t *testing.T) {
	llm := &coordinatorLLM{
		responses: []string{validCoordinatorJSON},
	}
	tools := []goai.Tool{
		{Name: "read", Description: "read files"},
	}

	orch := New(
		WithModel(llm),
		WithTools(tools...),
		WithDefaultModel("gpt-4o"),
	)

	result, err := orch.RunGoal(t.Context(), "Build a feature")
	if err != nil {
		t.Fatalf("RunGoal error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
	if len(result.Steps) != 2 {
		t.Errorf("steps = %d, want 2", len(result.Steps))
	}
	// First call should be coordinator, then 2 step calls.
	if len(llm.calls) != 3 {
		t.Errorf("total LLM calls = %d, want 3 (1 coordinator + 2 steps)", len(llm.calls))
	}
}

func TestRunGoal_Streaming(t *testing.T) {
	// Tests the streaming CoordinatorStreamChat path in RunGoal.
	llm := &streamingCoordinatorLLM{
		responses: []string{validCoordinatorJSON},
	}
	tools := []goai.Tool{
		{Name: "read", Description: "read files"},
	}

	var outputs []Output
	progress := &mockProgressSink{
		onEvent:  func(_ context.Context, _ Event) {},
		onOutput: func(_ context.Context, o Output) { outputs = append(outputs, o) },
	}

	orch := New(
		WithModel(llm),
		WithTools(tools...),
		WithDefaultModel("gpt-4o"),
		WithStreaming(),
		WithProgress(progress),
	)

	result, err := orch.RunGoal(t.Context(), "Build a feature")
	if err != nil {
		t.Fatalf("RunGoal error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}

	// Should have reasoning outputs from the streaming coordinator path.
	var hasReasoning bool
	for _, o := range outputs {
		if o.Reasoning && o.Delta != "" {
			hasReasoning = true
		}
	}
	if !hasReasoning {
		t.Error("expected reasoning outputs from streaming coordinator path")
	}
}

func TestRunGoal_NoLLM(t *testing.T) {
	orch := New()
	_, err := orch.RunGoal(t.Context(), "Build something")
	if err == nil {
		t.Fatal("expected error when no LLM configured")
	}
}

func TestRunGoal_NoTools(t *testing.T) {
	// RunGoal should work even without tools - coordinator gets "(no tools available)".
	llm := &coordinatorLLM{
		responses: []string{validCoordinatorJSON},
	}
	orch := New(
		WithModel(llm),
		WithDefaultModel("gpt-4o"),
	)

	result, err := orch.RunGoal(t.Context(), "Build something")
	if err != nil {
		t.Fatalf("RunGoal error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
}

func TestRunGoal_RetryOnInvalidJSON(t *testing.T) {
	llm := &coordinatorLLM{
		responses: []string{
			"this is not json",    // 1st attempt: invalid JSON
			"still not valid {{{", // 2nd attempt: invalid JSON
			validCoordinatorJSON,  // 3rd attempt: valid
		},
	}
	tools := []goai.Tool{}

	orch := New(
		WithModel(llm),
		WithTools(tools...),
		WithDefaultModel("gpt-4o"),
	)

	result, err := orch.RunGoal(t.Context(), "Build a feature")
	if err != nil {
		t.Fatalf("RunGoal error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
	// 3 coordinator calls + 2 step execution calls.
	if len(llm.calls) != 5 {
		t.Errorf("total LLM calls = %d, want 5", len(llm.calls))
	}
}

func TestRunGoal_ExhaustedRetries(t *testing.T) {
	llm := &coordinatorLLM{
		responses: []string{
			"bad json 1",
			"bad json 2",
			"bad json 3", // all 3 attempts fail
		},
	}
	orch := New(
		WithModel(llm),
		WithDefaultModel("gpt-4o"),
	)

	_, err := orch.RunGoal(t.Context(), "Build something")
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	var jsonErr *JSONParseError
	if !errors.As(err, &jsonErr) {
		t.Errorf("error type = %T, want *JSONParseError; err = %v", err, err)
	}
}

func TestRunGoal_RetryOnValidationError(t *testing.T) {
	// First response is valid JSON but fails validation (no name).
	// Second response is valid.
	llm := &coordinatorLLM{
		responses: []string{
			`{"steps": [{"id": "s1"}]}`, // valid JSON, fails validation (no name)
			validCoordinatorJSON,
		},
	}
	orch := New(
		WithModel(llm),
		WithDefaultModel("gpt-4o"),
	)

	result, err := orch.RunGoal(t.Context(), "Build a feature")
	if err != nil {
		t.Fatalf("RunGoal error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
}

// --- ApprovalHandler tests ---

type mockApproval struct {
	approved bool
	err      error
	called   bool
	plan     *Workflow
}

func (m *mockApproval) ApprovePlan(_ context.Context, plan *Workflow) (bool, error) {
	m.called = true
	m.plan = plan
	return m.approved, m.err
}

func TestRunGoal_ApprovalApproved(t *testing.T) {
	llm := &coordinatorLLM{
		responses: []string{validCoordinatorJSON},
	}
	approval := &mockApproval{approved: true}

	orch := New(
		WithModel(llm),
		WithDefaultModel("gpt-4o"),
		WithApproval(approval),
	)

	result, err := orch.RunGoal(t.Context(), "Build something")
	if err != nil {
		t.Fatalf("RunGoal error: %v", err)
	}
	if !approval.called {
		t.Error("ApprovalHandler was not called")
	}
	if approval.plan == nil {
		t.Error("ApprovalHandler received nil plan")
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
}

func TestRunGoal_ApprovalDenied(t *testing.T) {
	llm := &coordinatorLLM{
		responses: []string{validCoordinatorJSON},
	}
	approval := &mockApproval{approved: false}

	orch := New(
		WithModel(llm),
		WithDefaultModel("gpt-4o"),
		WithApproval(approval),
	)

	_, err := orch.RunGoal(t.Context(), "Build something")
	if err == nil {
		t.Fatal("expected error when plan denied")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("error = %q, want denial error", err)
	}
}

func TestRunGoal_ApprovalError(t *testing.T) {
	llm := &coordinatorLLM{
		responses: []string{validCoordinatorJSON},
	}
	approval := &mockApproval{err: errors.New("approval service down")}

	orch := New(
		WithModel(llm),
		WithDefaultModel("gpt-4o"),
		WithApproval(approval),
	)

	_, err := orch.RunGoal(t.Context(), "Build something")
	if err == nil {
		t.Fatal("expected error when approval handler fails")
	}
	if !strings.Contains(err.Error(), "approval") {
		t.Errorf("error = %q, want approval error", err)
	}
}

func TestWithApproval(t *testing.T) {
	approval := &mockApproval{}
	orch := New(WithApproval(approval))
	if orch.approval != approval {
		t.Error("WithApproval did not set approval handler")
	}
}

// --- Scheduling strategy validation ---

func TestSchedulingStrategy_Validation(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"empty (default)", "", false},
		{"dependency-first", "dependency-first", false},
		{"round-robin", "round-robin", false},
		{"least-busy", "least-busy", false},
		{"invalid", "random", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf := &Workflow{
				Name: "test",
				Steps: []Step{
					{ID: "s1", Instructions: "do"},
				},
				Options: WorkflowOptions{
					Scheduler: spec.SchedulerStrategy(tt.value),
				},
			}
			_, err := ValidateWorkflow(wf)
			if tt.wantErr && err == nil {
				t.Error("expected validation error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// --- Acceptance test: RunGoal end-to-end ---

func TestRunGoal_Acceptance_EndToEnd(t *testing.T) {
	// Coordinator returns a 3-step DAG with 2 agents.
	coordinatorResp := `{
		"name": "oauth2-feature",
		"agents": {
			"architect": {
				"description": "designs systems",
				"prompt": "You are a solution architect"
			},
			"developer": {
				"description": "writes code",
				"prompt": "You are a developer"
			}
		},
		"steps": [
			{"id": "design", "agent": "architect", "instructions": "Design OAuth2 module"},
			{"id": "implement", "agent": "developer", "dependsOn": ["design"], "instructions": "Implement OAuth2"},
			{"id": "test", "agent": "developer", "dependsOn": ["implement"], "instructions": "Write tests"}
		]
	}`

	llm := &coordinatorLLM{
		responses: []string{coordinatorResp},
	}
	tools := []goai.Tool{
		{Name: "read", Description: "read files", InputSchema: json.RawMessage(`{}`)},
		{Name: "bash", Description: "run commands", InputSchema: json.RawMessage(`{}`)},
		{Name: "edit", Description: "edit files", InputSchema: json.RawMessage(`{}`)},
	}

	approval := &mockApproval{approved: true}

	orch := New(
		WithModel(llm),
		WithTools(tools...),
		WithDefaultModel("claude-4-sonnet"),
		WithApproval(approval),
		WithMaxConcurrency(2),
	)

	result, err := orch.RunGoal(t.Context(), "Add user authentication with OAuth2")
	if err != nil {
		t.Fatalf("RunGoal error: %v", err)
	}

	// Verify workflow completed.
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}

	// Verify all 3 steps completed.
	for _, id := range []string{"design", "implement", "test"} {
		sr, ok := result.Steps[id]
		if !ok {
			t.Errorf("step %q missing from results", id)
			continue
		}
		if sr.Status != spec.StepCompleted {
			t.Errorf("step %q status = %q, want %q", id, sr.Status, spec.StepCompleted)
		}
	}

	// Verify approval handler was called with the plan.
	if !approval.called {
		t.Error("ApprovalHandler was not called")
	}
	if approval.plan == nil {
		t.Fatal("ApprovalHandler received nil plan")
	}
	if approval.plan.Name != "oauth2-feature" {
		t.Errorf("plan name = %q, want %q", approval.plan.Name, "oauth2-feature")
	}
	if len(approval.plan.Steps) != 3 {
		t.Errorf("plan steps = %d, want 3", len(approval.plan.Steps))
	}

	// Verify token aggregation includes coordinator + step tokens.
	if result.Tokens.InputTokens == 0 {
		t.Error("total input tokens should be > 0")
	}
	if result.Tokens.OutputTokens == 0 {
		t.Error("total output tokens should be > 0")
	}

	// Verify LLM calls: 1 coordinator + 3 steps = 4 total.
	if len(llm.calls) != 4 {
		t.Errorf("total LLM calls = %d, want 4", len(llm.calls))
	}

	// First call should be the coordinator (contains the goal).
	firstCall := llm.calls[0]
	if len(firstCall.Messages) < 1 {
		t.Fatalf("coordinator call messages = %d, want >= 1", len(firstCall.Messages))
	}
	firstMsgText := ""
	for _, p := range firstCall.Messages[0].Content {
		if p.Type == provider.PartText {
			firstMsgText += p.Text
		}
	}
	if !strings.Contains(firstMsgText, "OAuth2") {
		t.Error("coordinator call should contain the goal")
	}
	if !strings.Contains(firstMsgText, "read") {
		t.Error("coordinator call should contain tool catalog")
	}
}

// --- Context cancellation test ---

// blockingModel is defined in mock_test.go

func TestRunGoal_ContextCancellation(t *testing.T) {
	llm := &blockingModel{}
	orch := New(WithModel(llm), WithDefaultModel("gpt-4o"))

	ctx, cancel := context.WithCancel(t.Context())
	// Cancel immediately before coordinator can return.
	cancel()

	_, err := orch.RunGoal(ctx, "Build something")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// failStorage always fails on SaveRun to trigger RunFlow error.
type failStorage struct{}

func (f *failStorage) SaveRun(_ context.Context, _ *Run) error {
	return errors.New("storage: simulated save error")
}
func (f *failStorage) LoadRun(_ context.Context, _ string) (*Run, error) {
	return nil, errors.New("not found")
}
func (f *failStorage) SaveStepResult(_ context.Context, _, _ string, _ *StepResult) error {
	return errors.New("storage: simulated save error")
}
func (f *failStorage) LoadStepResult(_ context.Context, _, _ string) (*StepResult, error) {
	return nil, errors.New("not found")
}
func (f *failStorage) SaveSharedMemory(_ context.Context, _ string, _ map[string]string) error {
	return errors.New("storage: simulated save error")
}
func (f *failStorage) LoadSharedMemory(_ context.Context, _ string) (map[string]string, error) {
	return nil, errors.New("not found")
}

func TestRunGoal_StorageFailGracefulDegrade(t *testing.T) {
	// coordinator succeeds, storage fails on every SaveRun/SaveStepResult
	// call - RunGoal must complete gracefully instead of returning an error.
	// Storage is observability, not DAG correctness.
	llm := &coordinatorLLM{
		responses: []string{validCoordinatorJSON},
	}
	orch := New(
		WithModel(llm),
		WithDefaultModel("gpt-4o"),
		WithStorage(&failStorage{}),
	)

	result, err := orch.RunGoal(t.Context(), "Build a feature")
	if err != nil {
		t.Fatalf("RunGoal should not abort on storage error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// Workflow must complete even with storage fully unavailable.
	if result.Status != spec.StatusCompleted {
		t.Errorf("Status = %q, want %q", result.Status, spec.StatusCompleted)
	}
}

// TestParseCoordinatorResponse_StripsTimeouts is the P7.7.10 regression test.
// Coordinator LLMs set aggressive timeouts in generated JSON (e.g., "5m"),
// causing steps to hit context deadline exceeded on slower providers.
func TestParseCoordinatorResponse_StripsTimeouts(t *testing.T) {
	resp := `{
		"name": "timeout-test",
		"agents": {"worker": {"description": "works"}},
		"steps": [
			{"id": "step1", "agent": "worker", "instructions": "do", "timeout": "5m"},
			{"id": "step2", "agent": "worker", "dependsOn": ["step1"], "instructions": "more", "timeout": "10m"}
		],
		"options": {
			"timeout": "30m",
			"stepTimeout": "15m"
		}
	}`
	wf, err := ParseCoordinatorResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wf.Options.Timeout != 0 {
		t.Errorf("options.Timeout = %v, want 0 (stripped)", wf.Options.Timeout)
	}
	if wf.Options.StepTimeout != 0 {
		t.Errorf("options.StepTimeout = %v, want 0 (stripped)", wf.Options.StepTimeout)
	}
	for _, s := range wf.Steps {
		if s.Timeout != 0 {
			t.Errorf("step %q timeout = %v, want 0 (stripped)", s.ID, s.Timeout)
		}
	}
}

// TestStripStepTimeouts_Nested verifies nested loop step timeouts are stripped.
func TestStripStepTimeouts_Nested(t *testing.T) {
	steps := []Step{
		{ID: "outer", Timeout: Duration(5 * 60_000_000_000), Loop: &Loop{
			Steps: []Step{
				{ID: "inner", Timeout: Duration(3 * 60_000_000_000)},
			},
		}},
	}
	stripStepTimeouts(steps)
	if steps[0].Timeout != 0 {
		t.Errorf("outer timeout = %v, want 0", steps[0].Timeout)
	}
	if steps[0].Loop.Steps[0].Timeout != 0 {
		t.Errorf("inner timeout = %v, want 0", steps[0].Loop.Steps[0].Timeout)
	}
}

func TestCoordinatorPrompt_NoUnsupportedSection(t *testing.T) {
	prompt := CoordinatorPrompt("Build something", "- **read**: read files")
	// After update, the "NOT YET Supported" section should be removed.
	if strings.Contains(prompt, "NOT YET Supported") {
		t.Error("coordinator prompt should not contain 'NOT YET Supported'")
	}
	// All previously unsupported features should now appear in supported section.
	for _, feature := range []string{"forEach", "condition", "include", "scheduler", "isolation"} {
		if !strings.Contains(prompt, feature) {
			t.Errorf("coordinator prompt should mention %q as supported", feature)
		}
	}
}
