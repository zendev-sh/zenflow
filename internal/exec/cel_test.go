//go:build !e2e

package exec

import (
	"context"
	"github.com/zendev-sh/goai/provider"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zendev-sh/zenflow/internal/spec"
	"github.com/zendev-sh/zenflow/internal/types"
)

func TestCELEvaluate_SimpleTrue(t *testing.T) {
	ctx := &EvalContext{Steps: map[string]*EvalStepContext{}}
	got, err := EvaluateCEL("true", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("EvaluateCEL(\"true\") = false, want true")
	}
}

func TestCELEvaluate_SimpleFalse(t *testing.T) {
	ctx := &EvalContext{Steps: map[string]*EvalStepContext{}}
	got, err := EvaluateCEL("false", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("EvaluateCEL(\"false\") = true, want false")
	}
}

func TestCELEvaluate_StepStatus(t *testing.T) {
	ctx := &EvalContext{
		Steps: map[string]*EvalStepContext{
			"design": {Status: "completed", Content: "design output", Result: map[string]any{}},
		},
	}
	got, err := EvaluateCEL(`steps.design.status == "completed"`, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected true for completed design status")
	}
}

func TestCELEvaluate_StepContent(t *testing.T) {
	ctx := &EvalContext{
		Steps: map[string]*EvalStepContext{
			"design": {Content: "auth module design", Status: "completed", Result: map[string]any{}},
		},
	}

	got, err := EvaluateCEL(`steps.design.content.contains("auth")`, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected true for content containing 'auth'")
	}

	got, err = EvaluateCEL(`steps.design.content.contains("xyz")`, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected false for content not containing 'xyz'")
	}
}

func TestCELEvaluate_StepResult(t *testing.T) {
	ctx := &EvalContext{
		Steps: map[string]*EvalStepContext{
			"test": {Status: "completed", Content: "test output", Result: map[string]any{"passed": true}},
		},
	}
	got, err := EvaluateCEL(`steps.test.result.passed == true`, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected true for result.passed == true")
	}
}

func TestCELEvaluate_Iteration(t *testing.T) {
	ctx := &EvalContext{
		Steps:     map[string]*EvalStepContext{},
		Iteration: 3,
	}
	got, err := EvaluateCEL("iteration > 2", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected true for iteration(3) > 2")
	}

	got, err = EvaluateCEL("iteration > 5", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected false for iteration(3) > 5")
	}
}

func TestCELEvaluate_InvalidExpr(t *testing.T) {
	ctx := &EvalContext{Steps: map[string]*EvalStepContext{}}
	_, err := EvaluateCEL("!!!invalid", ctx)
	if err == nil {
		t.Error("expected error for invalid expression, got nil")
	}
}

func TestCELEvaluate_NonBoolResult(t *testing.T) {
	ctx := &EvalContext{Steps: map[string]*EvalStepContext{}}
	_, err := EvaluateCEL(`"hello"`, ctx)
	if err == nil {
		t.Error("expected error for non-bool result, got nil")
	}
}

// --- EvaluateCELToArray tests (Issue 1) ---

func TestCELEvaluateToArray_StepResult(t *testing.T) {
	ctx := &EvalContext{
		Steps: map[string]*EvalStepContext{
			"list": {
				Status:  "completed",
				Content: "listed",
				Result:  map[string]any{"items": []any{"a", "b"}},
			},
		},
	}
	arr, err := EvaluateCELToArray(`steps.list.result.items`, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(arr) != 2 {
		t.Fatalf("len = %d, want 2", len(arr))
	}
	if arr[0] != "a" || arr[1] != "b" {
		t.Errorf("arr = %v, want [a b]", arr)
	}
}

func TestCELEvaluateToArray_NonListError(t *testing.T) {
	ctx := &EvalContext{
		Steps: map[string]*EvalStepContext{
			"s": {Status: "completed", Content: "x", Result: map[string]any{"val": "not-a-list"}},
		},
	}
	_, err := EvaluateCELToArray(`steps.s.result.val`, ctx)
	if err == nil {
		t.Error("expected error for non-list result, got nil")
	}
}

// --- content/result/status variable tests (Issue 4) ---

func TestCELEvaluate_Content(t *testing.T) {
	ctx := &EvalContext{
		Steps:   map[string]*EvalStepContext{},
		Content: "task done",
	}
	got, err := EvaluateCEL(`content.contains("done")`, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected true for content.contains('done')")
	}
}

func TestCELEvaluate_Result(t *testing.T) {
	ctx := &EvalContext{
		Steps:  map[string]*EvalStepContext{},
		Result: map[string]any{"passed": true},
	}
	got, err := EvaluateCEL(`result.passed == true`, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected true for result.passed == true")
	}
}

func TestCELEvaluate_Status(t *testing.T) {
	ctx := &EvalContext{
		Steps:  map[string]*EvalStepContext{},
		Status: "completed",
	}
	got, err := EvaluateCEL(`status == "completed"`, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected true for status == 'completed'")
	}
}

func TestCELEvaluate_NilResult_DefaultsToEmptyMap(t *testing.T) {
	// When Result is nil, CEL should get an empty map (no nil panic).
	ctx := &EvalContext{
		Steps: map[string]*EvalStepContext{},
	}
	// result.size == 0 reliably checks empty map without CEL dyn == {} issues.
	got, err := EvaluateCEL(`result.size() == 0`, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected true for nil result defaulting to empty map")
	}
}

// Integration tests: CEL with executor.

func TestExecutor_StepCondition_True(t *testing.T) {
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "output", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}
	cond := "true"
	wf := newTestWorkflow(
		[]Step{{ID: "s1", Instructions: "do it", Condition: &cond}},
		nil,
	)
	exec := newTestExecutor(llm, nil, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr := result.Steps["s1"]
	if sr == nil {
		t.Fatal("step s1 missing from results")
	}
	if sr.Status != spec.StepCompleted {
		t.Errorf("step s1 status = %q, want %q", sr.Status, spec.StepCompleted)
	}
}

func TestExecutor_StepCondition_False(t *testing.T) {
	llm := &mockModel{
		responses: []*provider.GenerateResult{},
	}
	cond := "false"
	wf := newTestWorkflow(
		[]Step{{ID: "s1", Instructions: "do it", Condition: &cond}},
		nil,
	)
	exec := newTestExecutor(llm, nil, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr := result.Steps["s1"]
	if sr == nil {
		t.Fatal("step s1 missing from results")
	}
	if sr.Status != spec.StepSkipped {
		t.Errorf("step s1 status = %q, want %q", sr.Status, spec.StepSkipped)
	}
}

func TestExecutor_StepCondition_DependencyStatus(t *testing.T) {
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "design done", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
			{Text: "impl done", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}
	cond := `steps.design.status == "completed"`
	wf := newTestWorkflow(
		[]Step{
			{ID: "design", Instructions: "design it"},
			{ID: "impl", DependsOn: []string{"design"}, Instructions: "implement", Condition: &cond},
		},
		nil,
	)
	exec := newTestExecutor(llm, nil, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr := result.Steps["impl"]
	if sr == nil {
		t.Fatal("step impl missing from results")
	}
	if sr.Status != spec.StepCompleted {
		t.Errorf("step impl status = %q, want %q", sr.Status, spec.StepCompleted)
	}
}

func TestExecutor_StepCondition_FalseDependency_DependentsStillRun(t *testing.T) {
	// When a step is skipped by condition, its dependents should still run.
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "final output", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}
	cond := "false"
	wf := newTestWorkflow(
		[]Step{
			{ID: "optional", Instructions: "maybe", Condition: &cond},
			{ID: "final", DependsOn: []string{"optional"}, Instructions: "finish"},
		},
		nil,
	)
	exec := newTestExecutor(llm, nil, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The optional step should be skipped.
	if result.Steps["optional"].Status != spec.StepSkipped {
		t.Errorf("optional status = %q, want %q", result.Steps["optional"].Status, spec.StepSkipped)
	}
	// The dependent should still run (not cascade-cancelled).
	if result.Steps["final"].Status != spec.StepCompleted {
		t.Errorf("final status = %q, want %q", result.Steps["final"].Status, spec.StepCompleted)
	}
}

func TestExecutor_LoopUntil_CEL(t *testing.T) {
	// Loop with until="iteration >= 1" should run exactly 2 iterations (0-based: 0 after 1st, 1 after 2nd).
	var callCount atomic.Int32
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "iter 1", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
			{Text: "iter 2", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
			{Text: "iter 3 (should not happen)", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}
	// Use a custom LLM to count calls.
	countingLLM := &atomicCountingLLM{inner: llm, count: &callCount}

	until := "iteration >= 1"
	maxIter := 10
	wf := newTestWorkflow(
		[]Step{
			{
				ID:           "loop-step",
				Instructions: "iterate",
				Loop: &Loop{
					Until:         &until,
					MaxIterations: &maxIter,
				},
			},
		},
		nil,
	)
	exec := newTestExecutor(countingLLM, nil, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr := result.Steps["loop-step"]
	if sr == nil {
		t.Fatal("step loop-step missing")
	}
	if sr.Status != spec.StepCompleted {
		t.Errorf("status = %q, want %q (error: %v)", sr.Status, spec.StepCompleted, sr.Error)
	}
	if callCount.Load() != 2 {
		t.Errorf("LLM called %d times, want 2", callCount.Load())
	}
}

// atomicCountingLLM wraps a LanguageModel and counts DoGenerate calls with atomic access (race-safe).
type atomicCountingLLM struct {
	inner provider.LanguageModel
	count *atomic.Int32
}

func (c *atomicCountingLLM) ModelID() string { return c.inner.ModelID() }

func (c *atomicCountingLLM) DoGenerate(ctx context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
	c.count.Add(1)
	return c.inner.DoGenerate(ctx, params)
}

func (c *atomicCountingLLM) DoStream(ctx context.Context, params provider.GenerateParams) (*provider.StreamResult, error) {
	return c.inner.DoStream(ctx, params)
}

// mockProgressSink to capture events.
type celTestProgressSink struct {
	events []Event
}

func (s *celTestProgressSink) OnEvent(_ context.Context, e Event) {
	s.events = append(s.events, e)
}
func (s *celTestProgressSink) OnOutput(_ context.Context, _ Output) {}

func TestExecutor_StepCondition_False_EmitsSkipEvent(t *testing.T) {
	sink := &celTestProgressSink{}
	cond := "false"
	wf := newTestWorkflow(
		[]Step{{ID: "s1", Instructions: "do it", Condition: &cond}},
		nil,
	)
	exec := newTestExecutor(&mockModel{}, nil, wf)
	exec.Progress = sink

	_, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var found bool
	for _, e := range sink.events {
		if e.Type == types.EventStepSkipped && e.StepID == "s1" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected EventStepSkipped for s1, not found in events")
	}
}

// --- EvaluateCEL error path tests ---

func TestCELEvaluate_ProgramError(t *testing.T) {
	// Trigger the env.Program error path (line 41-43 in cel.go).
	// Use a CEL expression that compiles but fails at program creation with CostLimit.
	// Actually, the program error path is nearly impossible to trigger with a valid AST.
	// Instead, test the eval error path (line 61-63).
	ctx := &EvalContext{Steps: map[string]*EvalStepContext{}}
	// Access undefined variable will cause eval error.
	_, err := EvaluateCEL(`undefined_var > 0`, ctx)
	if err == nil {
		t.Error("expected error for undefined variable, got nil")
	}
}

// --- EvaluateCELToArray error path tests ---

func TestCELEvaluateToArray_CompileError(t *testing.T) {
	// Trigger compile error path (line 81-83 in cel.go).
	ctx := &EvalContext{Steps: map[string]*EvalStepContext{}}
	_, err := EvaluateCELToArray("!!!invalid", ctx)
	if err == nil {
		t.Error("expected compile error, got nil")
	}
	if !strings.Contains(err.Error(), "cel compile") {
		t.Errorf("expected 'cel compile' in error, got: %v", err)
	}
}

func TestCELEvaluateToArray_EvalError(t *testing.T) {
	// Trigger eval error path (line 106-108 in cel.go).
	ctx := &EvalContext{Steps: map[string]*EvalStepContext{}}
	_, err := EvaluateCELToArray(`undefined_var`, ctx)
	if err == nil {
		t.Error("expected eval error, got nil")
	}
}

// --- buildStepsMap nil value test ---

func TestBuildStepsMap_NilValue(t *testing.T) {
	// Trigger the nil sc continue path (line 125-126 in cel.go).
	steps := map[string]*EvalStepContext{
		"valid": {Content: "ok", Status: "completed", Result: map[string]any{}},
		"nilsc": nil,
	}
	m := buildStepsMap(steps)
	if len(m) != 1 {
		t.Errorf("expected 1 entry (nil skipped), got %d", len(m))
	}
	if _, ok := m["valid"]; !ok {
		t.Error("expected 'valid' entry in map")
	}
}

// --- BuildEvalContext nil value test ---

func TestBuildEvalContext_NilStepResult(t *testing.T) {
	// Trigger the nil sr continue path (line 143-144 in cel.go).
	results := map[string]*StepResult{
		"done":      {ID: "done", Status: spec.StepCompleted, Content: "output"},
		"in-flight": nil, // in-flight step
	}
	ec := BuildEvalContext(results)
	if len(ec.Steps) != 1 {
		t.Errorf("expected 1 step (nil skipped), got %d", len(ec.Steps))
	}
	if _, ok := ec.Steps["done"]; !ok {
		t.Error("expected 'done' step in context")
	}
}

func TestCEL_CachedEnv_Reuse(t *testing.T) {
	// Issue 8: cachedCELEnv should return the same env on multiple calls.
	env1, err1 := cachedCELEnv()
	if err1 != nil {
		t.Fatalf("first call: %v", err1)
	}
	env2, err2 := cachedCELEnv()
	if err2 != nil {
		t.Fatalf("second call: %v", err2)
	}
	// sync.OnceValues guarantees same return values.
	if env1 != env2 {
		t.Error("cachedCELEnv returned different env instances; expected same (cached)")
	}
}
