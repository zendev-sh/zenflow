package exec

import (
	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/zendev-sh/zenflow/internal/spec"
)

// Uses mockTracer defined in zenflow_test.go.

// --- Test 1: Loop step produces "zenflow.loop" parent span ---

func TestTracing_LoopStep_HasLoopSpan(t *testing.T) {
	maxIter := 2
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "iter1", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			{Text: "iter2", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	var tools []goai.Tool
	tracer := &mockTracer{}

	wf := &Workflow{
		Name:   "loop-trace-test",
		Agents: map[string]AgentConfig{"w": {Description: "worker"}},
		Steps: []Step{
			{
				ID: "s1", Agent: "w", Instructions: "work",
				Loop: &Loop{MaxIterations: &maxIter},
			},
		},
	}

	exec := &Executor{
		Runner:       &AgentRunner{model: llm, tools: tools},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Tracer:       tracer,
	}

	step := wf.Steps[0]
	sr := exec.runLoopStep(t.Context(), "run-1", "s1", step, 0, 1, nil)
	if sr.Status != spec.StepCompleted {
		t.Fatalf("status = %q, want %q", sr.Status, spec.StepCompleted)
	}

	// Must have exactly 1 "zenflow.loop" span.
	loopSpans := tracer.spansByName("zenflow.loop")
	if len(loopSpans) != 1 {
		t.Fatalf("zenflow.loop spans = %d, want 1; all spans: %v", len(loopSpans), spanNames(tracer))
	}

	// Loop span should carry step ID attribute.
	if loopSpans[0].attrs["zenflow.step.id"] != "s1" {
		t.Errorf("zenflow.loop attr zenflow.step.id = %q, want %q", loopSpans[0].attrs["zenflow.step.id"], "s1")
	}
}

// --- Test 2: Loop with N iterations produces N "zenflow.loop.iteration" spans ---

func TestTracing_LoopStep_HasIterationSpans(t *testing.T) {
	maxIter := 3
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "iter1", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			{Text: "iter2", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			{Text: "iter3", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	var tools []goai.Tool
	tracer := &mockTracer{}

	wf := &Workflow{
		Name:   "loop-iter-trace-test",
		Agents: map[string]AgentConfig{"w": {Description: "worker"}},
		Steps: []Step{
			{
				ID: "s1", Agent: "w", Instructions: "work",
				Loop: &Loop{MaxIterations: &maxIter},
			},
		},
	}

	exec := &Executor{
		Runner:       &AgentRunner{model: llm, tools: tools},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Tracer:       tracer,
	}

	step := wf.Steps[0]
	sr := exec.runLoopStep(t.Context(), "run-1", "s1", step, 0, 1, nil)
	if sr.Status != spec.StepCompleted {
		t.Fatalf("status = %q, want %q", sr.Status, spec.StepCompleted)
	}

	// Must have exactly 3 "zenflow.loop.iteration" spans.
	iterSpans := tracer.spansByName("zenflow.loop.iteration")
	if len(iterSpans) != 3 {
		t.Fatalf("zenflow.loop.iteration spans = %d, want 3; all spans: %v", len(iterSpans), spanNames(tracer))
	}

	// Each iteration span should have the iteration number attribute.
	for i, span := range iterSpans {
		expected := strconv.Itoa(i)
		if span.attrs["zenflow.loop.iteration"] != expected {
			t.Errorf("iteration span %d attr zenflow.loop.iteration = %q, want %q", i, span.attrs["zenflow.loop.iteration"], expected)
		}
	}
}

// --- Test 3: Inside loop, iterations use zenflow.loop.iteration NOT zenflow.step ---

func TestTracing_LoopStep_NoStepSpanInsideLoop(t *testing.T) {
	maxIter := 2
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "iter1", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			{Text: "iter2", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	var tools []goai.Tool
	tracer := &mockTracer{}

	wf := &Workflow{
		Name:   "loop-no-step-span-test",
		Agents: map[string]AgentConfig{"w": {Description: "worker"}},
		Steps: []Step{
			{
				ID: "s1", Agent: "w", Instructions: "work",
				Loop: &Loop{MaxIterations: &maxIter},
			},
		},
	}

	exec := &Executor{
		Runner:       &AgentRunner{model: llm, tools: tools},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Tracer:       tracer,
	}

	step := wf.Steps[0]
	_ = exec.runLoopStep(t.Context(), "run-1", "s1", step, 0, 1, nil)

	// There should be NO "zenflow.step" spans - iterations use "zenflow.loop.iteration" instead.
	stepSpans := tracer.spansByName("zenflow.step")
	if len(stepSpans) != 0 {
		t.Errorf("zenflow.step spans = %d, want 0 (loop iterations should not produce step spans); all spans: %v",
			len(stepSpans), spanNames(tracer))
	}
}

// --- Test 4: Include step produces "zenflow.include" span ---

func TestTracing_IncludeStep_HasIncludeSpan(t *testing.T) {
	// Include tracing is in runIncludeStep, which is called by the dispatch switch.
	// Test through the actual dispatch path using exec.Run.
	dir := t.TempDir()
	subYAML := `name: sub
version: 1
agents:
  w:
    description: "worker"
steps:
  - id: inner
    agent: w
    instructions: "do inner work"
`
	if err := os.WriteFile(filepath.Join(dir, "sub.yaml"), []byte(subYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "include result", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	var tools []goai.Tool
	tracer := &mockTracer{}

	wf := &Workflow{
		Name:     "include-trace-test",
		Includes: map[string]string{"sub-wf": "sub.yaml"},
		Steps: []Step{
			{ID: "s1", Include: "sub-wf"},
		},
		BaseDir: dir,
	}

	exec := &Executor{
		Runner:       &AgentRunner{model: llm, tools: tools},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Tracer:       tracer,
	}

	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr := result.Steps["s1"]
	if sr == nil {
		t.Fatal("step s1 missing from results")
	}
	if sr.Status != spec.StepCompleted {
		t.Errorf("step status = %q, want %q (error: %v)", sr.Status, spec.StepCompleted, sr.Error)
	}

	// Must have a "zenflow.include" span with the include ref attribute.
	includeSpans := tracer.spansByName("zenflow.include")
	if len(includeSpans) != 1 {
		t.Fatalf("zenflow.include spans = %d, want 1; all spans: %v", len(includeSpans), spanNames(tracer))
	}
	if includeSpans[0].attrs["zenflow.include.ref"] != "sub-wf" {
		t.Errorf("zenflow.include attr zenflow.include.ref = %q, want %q",
			includeSpans[0].attrs["zenflow.include.ref"], "sub-wf")
	}

	// The include step itself should NOT produce a "zenflow.step" span.
	// However, the nested sub-workflow's inner steps will produce "zenflow.step" spans
	// because the tracer is propagated to the nested executor.
	// Verify that no "zenflow.step" span has step.id = "s1" (the include step).
	stepSpans := tracer.spansByName("zenflow.step")
	for _, s := range stepSpans {
		if s.attrs["zenflow.step.id"] == "s1" {
			t.Errorf("found zenflow.step span for include step s1 - include steps should use zenflow.include, not zenflow.step; all spans: %v",
				spanNames(tracer))
		}
	}
}

// --- Test 5: Regular step still produces "zenflow.step" span (regression) ---

func TestTracing_RegularStep_Unchanged(t *testing.T) {
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "output", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	var tools []goai.Tool
	tracer := &mockTracer{}

	wf := &Workflow{
		Name:   "regular-trace-test",
		Agents: map[string]AgentConfig{"w": {Description: "worker"}},
		Steps: []Step{
			{ID: "s1", Agent: "w", Instructions: "work"},
		},
	}

	exec := &Executor{
		Runner:       &AgentRunner{model: llm, tools: tools},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Tracer:       tracer,
	}

	step := wf.Steps[0]
	sr := exec.runStep(t.Context(), "run-1", "s1", step, 0, 1, nil)
	if sr.Status != spec.StepCompleted {
		t.Fatalf("status = %q, want %q", sr.Status, spec.StepCompleted)
	}

	// Must have exactly 1 "zenflow.step" span.
	stepSpans := tracer.spansByName("zenflow.step")
	if len(stepSpans) != 1 {
		t.Fatalf("zenflow.step spans = %d, want 1; all spans: %v", len(stepSpans), spanNames(tracer))
	}
	if stepSpans[0].attrs["zenflow.step.id"] != "s1" {
		t.Errorf("zenflow.step attr zenflow.step.id = %q, want %q", stepSpans[0].attrs["zenflow.step.id"], "s1")
	}

	// No loop or include spans.
	if len(tracer.spansByName("zenflow.loop")) != 0 {
		t.Errorf("unexpected zenflow.loop spans for regular step")
	}
	if len(tracer.spansByName("zenflow.include")) != 0 {
		t.Errorf("unexpected zenflow.include spans for regular step")
	}
}

// --- Test 6: Include step propagates tracer to nested executor ---

func TestTracing_IncludeStep_PropagatesTracer(t *testing.T) {
	// Verify that nested executor (include) inherits the tracer,
	// producing both "zenflow.include" and "zenflow.workflow" + inner "zenflow.step" spans.
	dir := t.TempDir()
	subYAML := `name: inner-wf
version: 1
agents:
  w:
    description: "worker"
steps:
  - id: inner_step
    agent: w
    instructions: "do inner work"
`
	if err := os.WriteFile(filepath.Join(dir, "inner.yaml"), []byte(subYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "inner result", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	var tools []goai.Tool
	tracer := &mockTracer{}

	wf := &Workflow{
		Name:     "propagation-test",
		Includes: map[string]string{"inner": "inner.yaml"},
		Steps: []Step{
			{ID: "s1", Include: "inner"},
		},
		BaseDir: dir,
	}

	exec := &Executor{
		Runner:       &AgentRunner{model: llm, tools: tools},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Tracer:       tracer,
	}

	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}

	// The nested executor should also produce spans because tracer was propagated.
	// We expect: zenflow.workflow (outer), zenflow.include, zenflow.workflow (inner), zenflow.step (inner_step).
	allNames := spanNames(tracer)
	// Must have at least a zenflow.include span (from runIncludeStep) and
	// a zenflow.step span from the inner workflow's step execution.
	includeSpans := tracer.spansByName("zenflow.include")
	if len(includeSpans) == 0 {
		t.Errorf("expected zenflow.include span, got none; all spans: %v", allNames)
	}
	stepSpans := tracer.spansByName("zenflow.step")
	if len(stepSpans) == 0 {
		t.Errorf("expected zenflow.step span from inner workflow, got none; all spans: %v", allNames)
	}
}

// --- Test 7: forEach produces "zenflow.loop" + "zenflow.loop.iteration" spans ---

func TestTracing_ForEach_HasLoopAndIterationSpans(t *testing.T) {
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "r1", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			{Text: "r2", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			{Text: "r3", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	var tools []goai.Tool
	tracer := &mockTracer{}

	wf := &Workflow{
		Name:   "foreach-trace-test",
		Agents: map[string]AgentConfig{"w": {Description: "worker"}},
		Steps: []Step{
			{
				ID: "s1", Agent: "w", Instructions: "process",
				Loop: &Loop{ForEach: []any{"a", "b", "c"}, MaxConcurrency: 1},
			},
		},
	}

	exec := &Executor{
		Runner:       &AgentRunner{model: llm, tools: tools},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Tracer:       tracer,
	}

	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Fatalf("status = %q, want %q (error: %v)", result.Status, spec.StatusCompleted, result.Steps["s1"].Error)
	}

	// Must have exactly 1 "zenflow.loop" span with type=forEach.
	loopSpans := tracer.spansByName("zenflow.loop")
	if len(loopSpans) != 1 {
		t.Fatalf("zenflow.loop spans = %d, want 1; all spans: %v", len(loopSpans), spanNames(tracer))
	}
	if loopSpans[0].attrs["zenflow.loop.type"] != "forEach" {
		t.Errorf("zenflow.loop.type = %q, want %q", loopSpans[0].attrs["zenflow.loop.type"], "forEach")
	}

	// Must have 3 "zenflow.loop.iteration" spans.
	iterSpans := tracer.spansByName("zenflow.loop.iteration")
	if len(iterSpans) != 3 {
		t.Fatalf("zenflow.loop.iteration spans = %d, want 3; all spans: %v", len(iterSpans), spanNames(tracer))
	}

	// Each iteration span should have the step ID attribute.
	for _, span := range iterSpans {
		if span.attrs["zenflow.step.id"] != "s1" {
			t.Errorf("iteration span attr zenflow.step.id = %q, want %q", span.attrs["zenflow.step.id"], "s1")
		}
	}
}

// spanNames extracts span names from the mockTracer for diagnostic output.
func spanNames(m *mockTracer) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, len(m.spans))
	for i, s := range m.spans {
		names[i] = s.name
	}
	return names
}
