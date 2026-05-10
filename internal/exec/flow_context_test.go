package exec

// flow_context_test.go - named tests.
// Tests cover:
// - TestRunFlow_FlowContext - RunFlowOption + WithFlowContext threads through to Executor.
// - TestCoord_ReceivesWorkflowStart - coord mailbox receives workflow_start with Context populated.
// - TestRunFlow_BlanketContextWhenNoCoordinator - coord==nil + WithFlowContext prepends to every step prompt.
// - TestRunGoal_GoalContext - RunGoalOption + WithGoalContext appears in coordinator prompt.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/spec"
	"github.com/zendev-sh/zenflow/internal/types"
)

// promptCapturingModel records every prompt's user-message text in order so
// tests can assert that flow / goal context strings reach the LLM.
type promptCapturingModel struct {
	mu     sync.Mutex
	id     string
	texts  []string
	canned []string // optional canned responses; empty = always returns "done"
	idx    int
}

func (m *promptCapturingModel) ModelID() string {
	if m.id != "" {
		return m.id
	}
	return "prompt-capture"
}

func (m *promptCapturingModel) DoGenerate(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Concatenate user-message text from all parts so callers can do simple
	// substring assertions regardless of prompt assembly details.
	var b strings.Builder
	for _, msg := range params.Messages {
		if msg.Role != provider.RoleUser {
			continue
		}
		for _, part := range msg.Content {
			if part.Type == provider.PartText {
				b.WriteString(part.Text)
				b.WriteString("\n")
			}
		}
	}
	m.texts = append(m.texts, b.String())

	text := "done"
	if m.idx < len(m.canned) {
		text = m.canned[m.idx]
		m.idx++
	}
	return &provider.GenerateResult{
		Text:         text,
		FinishReason: provider.FinishStop,
		Usage:        provider.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

func (m *promptCapturingModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, errPromptCaptureNoStream
}

// captured returns a snapshot of the recorded prompts.
func (m *promptCapturingModel) captured() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.texts))
	copy(out, m.texts)
	return out
}

var errPromptCaptureNoStream = stringError("promptCapturingModel: streaming not implemented")

type stringError string

func (e stringError) Error() string { return string(e) }

// TestRunFlow_FlowContext - WithFlowContext is a RunFlowOption applied
// alongside RunFlow's variadic args, and the supplied string reaches the
// Executor (observed via the FlowContext field set on the Executor - the
// blanket-injection path is exercised separately, this test only
// asserts the option threading).
func TestRunFlow_FlowContext(t *testing.T) {
	llm := &promptCapturingModel{}
	o := New(WithModel(llm), WithDefaultModel("test-model"))
	wf := &Workflow{Name: "ctx-flow", Steps: []Step{{ID: "s1", Instructions: "x"}}}

	got, err := o.RunFlow(t.Context(), wf, WithFlowContext("topic: AI replaces juniors"))
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if got == nil || got.Status != spec.StatusCompleted {
		t.Fatalf("RunFlow result = %+v, want status=%q", got, spec.StatusCompleted)
	}

	// Assertion: the captured user-prompt for the (only) step must contain
	// the context string. Without WithFlowContext threading through to
	// runStep, the prompt would only contain the step's Instructions ("x").
	prompts := llm.captured()
	if len(prompts) == 0 {
		t.Fatalf("no prompts captured")
	}
	if !strings.Contains(prompts[0], "topic: AI replaces juniors") {
		t.Errorf("prompt missing flow context\nprompt=%q", prompts[0])
	}
}

// TestRunFlow_FlowContext_NoOpWhenAbsent - RunFlow without any options
// behaves identically to before (variadic backward-compat).
func TestRunFlow_FlowContext_NoOpWhenAbsent(t *testing.T) {
	llm := &promptCapturingModel{}
	o := New(WithModel(llm), WithDefaultModel("test-model"))
	wf := &Workflow{Name: "noctx-flow", Steps: []Step{{ID: "s1", Instructions: "do-it"}}}

	if _, err := o.RunFlow(t.Context(), wf); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	prompts := llm.captured()
	if len(prompts) == 0 {
		t.Fatalf("no prompts captured")
	}
	if !strings.Contains(prompts[0], "do-it") {
		t.Errorf("prompt missing instructions: %q", prompts[0])
	}
	// Sentinel - must NOT contain a stray context marker when no option was supplied.
	if strings.Contains(prompts[0], "[Flow Context]") {
		t.Errorf("prompt contains [Flow Context] marker but no WithFlowContext supplied: %q", prompts[0])
	}
}

// TestCoord_ReceivesWorkflowStart - when WithCoordinator is set and
// WithFlowContext is supplied, the coord runner's Mailbox receives a
// RouterMessage tagged with event_type=workflow_start whose Content is
// the supplied context.
func TestCoord_ReceivesWorkflowStart(t *testing.T) {
	llm := &promptCapturingModel{}
	coord := NewDefaultCoordRunner(stubCoordLanguageModel{})
	o := New(
		WithModel(llm),
		WithDefaultModel("test-model"),
		WithCoordinator(coord),
	)
	wf := &Workflow{Name: "z52", Steps: []Step{{ID: "s1", Instructions: "x"}}}

	if _, err := o.RunFlow(t.Context(), wf, WithFlowContext("topic: X")); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}

	msgs := coord.mailbox.Unread(coordStepID(coord))
	if len(msgs) == 0 {
		t.Fatalf("coord mailbox is empty - no workflow_start push observed")
	}

	// The workflow_start push MUST be the first event in the mailbox so the
	// coordinator LLM sees its context input before any per-step lifecycle
	// events (StepStart/StepEnd).
	first := msgs[0]
	if first.Metadata["event_type"] != string(types.EventWorkflowStart) {
		t.Errorf("first mailbox message event_type=%q want %q", first.Metadata["event_type"], types.EventWorkflowStart)
	}
	if !strings.Contains(first.Content, "topic: X") {
		t.Errorf("workflow_start Content missing context: %q", first.Content)
	}
}

// TestRunFlow_BlanketContextWhenNoCoordinator - coord==nil + WithFlowContext
// must prepend the context to every step's effective user prompt.
func TestRunFlow_BlanketContextWhenNoCoordinator(t *testing.T) {
	llm := &promptCapturingModel{}
	o := New(WithModel(llm), WithDefaultModel("test-model"), WithCoordinator(nil))
	wf := &Workflow{
		Name: "blanket",
		Steps: []Step{
			{ID: "s1", Instructions: "first-step"},
			{ID: "s2", Instructions: "second-step"},
		},
	}
	if _, err := o.RunFlow(t.Context(), wf, WithFlowContext("topic: X")); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	prompts := llm.captured()
	if len(prompts) < 2 {
		t.Fatalf("expected >=2 prompts, got %d", len(prompts))
	}
	for i, p := range prompts {
		if !strings.Contains(p, "topic: X") {
			t.Errorf("prompt[%d] missing flow context: %q", i, p)
		}
	}
}

// TestRunGoal_GoalContext - WithGoalContext is a RunGoalOption; its value
// must appear in the prompt sent to the coordinator LLM.
func TestRunGoal_GoalContext(t *testing.T) {
	// Goal LLM must return a valid coordinator JSON envelope so RunGoal
	// proceeds past parsing. We capture the prompt regardless of whether
	// the workflow then runs to completion - only the first call (to the
	// coordinator) is asserted.
	const goalJSON = `{
  "name": "g",
  "agents": {"a": {"model": "test-model", "tools": []}},
  "steps": [{"id": "s1", "agent": "a", "instructions": "do it"}]
}`
	llm := &promptCapturingModel{canned: []string{goalJSON}}
	o := New(WithModel(llm), WithDefaultModel("test-model"))
	if _, err := o.RunGoal(t.Context(), "ship a debate workflow", WithGoalContext("topic: AI replaces juniors")); err != nil {
		// RunGoal may still succeed; if it errors after the first call we
		// don't care here - we only assert the captured prompt.
		t.Logf("RunGoal returned: %v (acceptable for this test)", err)
	}

	prompts := llm.captured()
	if len(prompts) == 0 {
		t.Fatalf("no prompts captured")
	}
	if !strings.Contains(prompts[0], "topic: AI replaces juniors") {
		t.Errorf("coordinator prompt missing goal context: %q", prompts[0])
	}
	// And the goal text itself must still be present.
	if !strings.Contains(prompts[0], "ship a debate workflow") {
		t.Errorf("coordinator prompt missing goal text: %q", prompts[0])
	}
}

// TestRunGoal_GoalContext_NoOpWhenAbsent - RunGoal without options retains
// its previous behavior (variadic backward-compat).
func TestRunGoal_GoalContext_NoOpWhenAbsent(t *testing.T) {
	const goalJSON = `{
  "name": "g",
  "agents": {"a": {"model": "test-model", "tools": []}},
  "steps": [{"id": "s1", "agent": "a", "instructions": "do it"}]
}`
	llm := &promptCapturingModel{canned: []string{goalJSON}}
	o := New(WithModel(llm), WithDefaultModel("test-model"))
	if _, err := o.RunGoal(t.Context(), "plain goal text"); err != nil {
		t.Logf("RunGoal returned: %v (acceptable for this test)", err)
	}
	prompts := llm.captured()
	if len(prompts) == 0 {
		t.Fatalf("no prompts captured")
	}
	if strings.Contains(prompts[0], "[Goal Context]") {
		t.Errorf("prompt contains [Goal Context] marker but no WithGoalContext supplied: %q", prompts[0])
	}
}
