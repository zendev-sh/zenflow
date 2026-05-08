package exec

import (
	"context"
	"errors"
	"fmt"
	"github.com/zendev-sh/goai/provider"
	"strings"
	"testing"
	"time"

	"github.com/zendev-sh/zenflow/internal/spec"
)

// mockLLMForResume responds with predictable content for each step.
type mockLLMForResume struct {
	calls int
}

func (m *mockLLMForResume) DoGenerate(_ context.Context, req provider.GenerateParams) (*provider.GenerateResult, error) {
	m.calls++
	return &provider.GenerateResult{
		Text:         fmt.Sprintf("response-%d", m.calls),
		Usage:        provider.Usage{InputTokens: 10, OutputTokens: 5},
		FinishReason: provider.FinishStop, // was "stop",
	}, nil
}

func TestResumeFlow_SkipsCompletedSteps(t *testing.T) {
	store := NewMemoryStorage()
	ctx := t.Context()

	// Pre-populate run with step-1 completed.
	run := &Run{
		ID:       "run-resume-1",
		Workflow: &Workflow{Name: "test"},
		Status:   spec.StatusRunning,
		Steps:    map[string]*StepResult{},
	}
	if err := store.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	sr := &StepResult{
		ID:       "step-1",
		Status:   spec.StepCompleted,
		Content:  "already done",
		Tokens:   provider.Usage{InputTokens: 50, OutputTokens: 25},
		Duration: 2 * time.Second,
	}
	if err := store.SaveStepResult(ctx, "run-resume-1", "step-1", sr); err != nil {
		t.Fatal(err)
	}

	wf := &Workflow{
		Name: "resume-test",
		Steps: []Step{
			{ID: "step-1", Instructions: "Already completed"},
			{ID: "step-2", DependsOn: []string{"step-1"}, Instructions: "Needs to run"},
		},
	}

	llm := &mockLLMForResume{}
	orch := New(
		WithModel(llm),
		WithStorage(store),
		WithDefaultModel("test-model"),
	)

	result, err := orch.ResumeFlow(ctx, "run-resume-1", wf)
	if err != nil {
		t.Fatalf("ResumeFlow: %v", err)
	}

	// step-1 should be loaded (not re-executed), so LLM should only be called for step-2.
	if llm.calls != 1 {
		t.Errorf("LLM calls = %d, want 1 (only step-2)", llm.calls)
	}

	// RunID should be reused from the checkpoint.
	if result.RunID != "run-resume-1" {
		t.Errorf("RunID = %q, want run-resume-1 (should reuse checkpoint RunID)", result.RunID)
	}

	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want completed", result.Status)
	}

	// step-1 result should be the pre-populated one.
	sr1 := result.Steps["step-1"]
	if sr1 == nil {
		t.Fatal("step-1 missing from result")
	}
	if sr1.Content != "already done" {
		t.Errorf("step-1 content = %q, want 'already done'", sr1.Content)
	}

	// step-2 should have been executed.
	sr2 := result.Steps["step-2"]
	if sr2 == nil {
		t.Fatal("step-2 missing from result")
	}
	if sr2.Status != spec.StepCompleted {
		t.Errorf("step-2 status = %q, want completed", sr2.Status)
	}
}

func TestResumeFlow_ReExecutesFailedSteps(t *testing.T) {
	store := NewMemoryStorage()
	ctx := t.Context()

	run := &Run{
		ID:       "run-resume-2",
		Workflow: &Workflow{Name: "test"},
		Status:   spec.StatusPartial,
		Steps:    map[string]*StepResult{},
	}
	if err := store.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// step-1 completed, step-2 failed.
	if err := store.SaveStepResult(ctx, "run-resume-2", "step-1", &StepResult{
		ID: "step-1", Status: spec.StepCompleted, Content: "done",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStepResult(ctx, "run-resume-2", "step-2", &StepResult{
		ID: "step-2", Status: spec.StepFailed, Error: fmt.Errorf("timeout"),
	}); err != nil {
		t.Fatal(err)
	}

	wf := &Workflow{
		Name: "resume-test",
		Steps: []Step{
			{ID: "step-1", Instructions: "Done"},
			{ID: "step-2", DependsOn: []string{"step-1"}, Instructions: "Retry me"},
		},
	}

	llm := &mockLLMForResume{}
	orch := New(
		WithModel(llm),
		WithStorage(store),
		WithDefaultModel("test-model"),
	)

	result, err := orch.ResumeFlow(ctx, "run-resume-2", wf)
	if err != nil {
		t.Fatalf("ResumeFlow: %v", err)
	}

	// step-2 should be re-executed.
	if llm.calls != 1 {
		t.Errorf("LLM calls = %d, want 1 (only step-2 re-executed)", llm.calls)
	}

	sr2 := result.Steps["step-2"]
	if sr2 == nil {
		t.Fatal("step-2 missing")
	}
	if sr2.Status != spec.StepCompleted {
		t.Errorf("step-2 status = %q, want completed", sr2.Status)
	}
}

// mockLLMReadSharedMem calls shared_memory_read on first turn, then completes.
type mockLLMReadSharedMem struct {
	turn int
}

func (m *mockLLMReadSharedMem) DoGenerate(_ context.Context, req provider.GenerateParams) (*provider.GenerateResult, error) {
	m.turn++
	if m.turn == 1 {
		return &provider.GenerateResult{
			Text: "reading shared memory",
			ToolCalls: []provider.ToolCall{
				{ID: "tc-sm-1", Name: "shared_memory_read", Input: []byte(`{"key":"agent-a/key"}`)},
			},
			Usage:        provider.Usage{InputTokens: 10, OutputTokens: 5},
			FinishReason: provider.FinishStop, // was "tool_calls",
		}, nil
	}
	// Find the tool result in messages to verify the value was read.
	for _, msg := range req.Messages {
		if msg.Role == provider.RoleTool {
			for _, p := range msg.Content {
				if p.Type == provider.PartToolResult {
					return &provider.GenerateResult{
						Text:         "got: " + p.ToolOutput,
						Usage:        provider.Usage{InputTokens: 10, OutputTokens: 5},
						FinishReason: provider.FinishStop,
					}, nil
				}
			}
		}
	}
	return &provider.GenerateResult{Text: "done", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}, FinishReason: provider.FinishStop}, nil
}

func TestResumeFlow_RestoresSharedMemory(t *testing.T) {
	store := NewMemoryStorage()
	ctx := t.Context()

	run := &Run{
		ID:       "run-resume-3",
		Workflow: &Workflow{Name: "test"},
		Status:   spec.StatusRunning,
		Steps:    map[string]*StepResult{},
	}
	if err := store.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// Pre-save shared memory with a key the step will read.
	if err := store.SaveSharedMemory(ctx, "run-resume-3", map[string]string{
		"agent-a/key": "persisted-value",
	}); err != nil {
		t.Fatal(err)
	}

	wf := &Workflow{
		Name: "resume-test",
		Steps: []Step{
			{ID: "step-1", Instructions: "read shared memory"},
		},
	}

	llm := &mockLLMReadSharedMem{}
	orch := New(
		WithModel(llm),
		WithStorage(store),
		WithDefaultModel("test-model"),
	)

	result, err := orch.ResumeFlow(ctx, "run-resume-3", wf)
	if err != nil {
		t.Fatalf("ResumeFlow: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want completed", result.Status)
	}

	// Verify the step output contains the shared memory value,
	// proving the SharedMemory was restored and accessible during execution.
	sr := result.Steps["step-1"]
	if sr == nil {
		t.Fatal("step-1 missing")
	}
	if sr.Content == "" {
		t.Error("step-1 content is empty")
	}
	// The mock LLM reads "agent-a/key" from shared memory and includes
	// the value in its response. If SharedMemory was not restored,
	// the read would return "key not found" instead of "persisted-value".
	if !strings.Contains(sr.Content, "persisted-value") {
		t.Errorf("step-1 content = %q, want to contain 'persisted-value' (shared memory not restored)", sr.Content)
	}
}

func TestResumeFlow_RunNotFound(t *testing.T) {
	store := NewMemoryStorage()
	orch := New(
		WithModel(&mockLLMForResume{}),
		WithStorage(store),
		WithDefaultModel("test-model"),
	)

	_, err := orch.ResumeFlow(t.Context(), "nonexistent", &Workflow{
		Name:  "test",
		Steps: []Step{{ID: "s1", Instructions: "go"}},
	})
	if err == nil {
		t.Fatal("expected error for missing run")
	}
}

func TestWithSharedMemory(t *testing.T) {
	sm := NewSharedMemory()
	sm.Write("agent", "key", "val")

	orch := New(WithSharedMemory(sm))
	// Verify the orchestrator has sharedMem set by using it in a flow.
	// Simple check: the option doesn't panic and sets the field.
	if orch.sharedMem == nil {
		t.Fatal("expected sharedMem to be set")
	}
	if orch.sharedMem != sm {
		t.Error("expected sharedMem to be the same instance passed to WithSharedMemory")
	}
}

func TestResumeFlow_NilWorkflow(t *testing.T) {
	orch := New(WithModel(&mockLLMForResume{}), WithDefaultModel("test"))
	_, err := orch.ResumeFlow(t.Context(), "run-1", nil)
	if err == nil {
		t.Fatal("expected error for nil workflow")
	}
}

func TestResumeFlow_NilStorage(t *testing.T) {
	orch := New(WithModel(&mockLLMForResume{}), WithStorage(nil), WithDefaultModel("test"))
	wf := &Workflow{Name: "test", Steps: []Step{{ID: "s1", Instructions: "go"}}}
	_, err := orch.ResumeFlow(t.Context(), "run-1", wf)
	if err == nil {
		t.Fatal("expected error for nil storage")
	}
	if !errors.Is(err, ErrStorageRequired) {
		t.Errorf("got %v, want errors.Is(err, ErrStorageRequired)", err)
	}
}

// partialFailStorage fails only on LoadSharedMemory.
type partialFailStorage struct {
	*MemoryStorage
}

func (p *partialFailStorage) LoadSharedMemory(_ context.Context, _ string) (map[string]string, error) {
	return nil, errors.New("shared memory load failed")
}

func TestResumeFlow_LoadSharedMemoryError(t *testing.T) {
	ms := NewMemoryStorage()
	ctx := t.Context()

	run := &Run{
		ID:       "run-smerr",
		Workflow: &Workflow{Name: "test"},
		Status:   spec.StatusRunning,
		Steps:    map[string]*StepResult{},
	}
	if err := ms.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	store := &partialFailStorage{MemoryStorage: ms}
	orch := New(
		WithModel(&mockLLMForResume{}),
		WithStorage(store),
		WithDefaultModel("test-model"),
	)

	wf := &Workflow{
		Name:  "test",
		Steps: []Step{{ID: "s1", Instructions: "go"}},
	}

	_, err := orch.ResumeFlow(ctx, "run-smerr", wf)
	if err == nil {
		t.Fatal("expected error when LoadSharedMemory fails")
	}
}

func TestResumeFlow_AllStepsAlreadyCompleted(t *testing.T) {
	store := NewMemoryStorage()
	ctx := t.Context()

	run := &Run{
		ID:       "run-resume-4",
		Workflow: &Workflow{Name: "test"},
		Status:   spec.StatusCompleted,
		Steps:    map[string]*StepResult{},
	}
	if err := store.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStepResult(ctx, "run-resume-4", "step-1", &StepResult{
		ID: "step-1", Status: spec.StepCompleted, Content: "done",
	}); err != nil {
		t.Fatal(err)
	}

	wf := &Workflow{
		Name:  "resume-test",
		Steps: []Step{{ID: "step-1", Instructions: "Done"}},
	}

	llm := &mockLLMForResume{}
	orch := New(
		WithModel(llm),
		WithStorage(store),
		WithDefaultModel("test-model"),
	)

	result, err := orch.ResumeFlow(ctx, "run-resume-4", wf)
	if err != nil {
		t.Fatalf("ResumeFlow: %v", err)
	}

	// No LLM calls should happen - all steps already done.
	if llm.calls != 0 {
		t.Errorf("LLM calls = %d, want 0", llm.calls)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want completed", result.Status)
	}
}

func (m *mockLLMForResume) ModelID() string { return "resume-mock" }
func (m *mockLLMForResume) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLLMReadSharedMem) ModelID() string { return "shared-mem-mock" }
func (m *mockLLMReadSharedMem) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, errors.New("not implemented")
}
