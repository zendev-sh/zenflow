//go:build !e2e

package exec

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zendev-sh/zenflow/internal/spec"
	"github.com/zendev-sh/zenflow/internal/types"
)

// sequentialCallLLM calls a function for each DoGenerate call. Implements provider.LanguageModel.
type sequentialCallLLM struct {
	fn func(req provider.GenerateParams) *provider.GenerateResult
}

func (m *sequentialCallLLM) ModelID() string { return "sequential-call-mock" }

func (m *sequentialCallLLM) DoGenerate(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
	return m.fn(params), nil
}

func (m *sequentialCallLLM) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func TestExecutor_ForEach_StaticArray(t *testing.T) {
	// forEach: ["a", "b", "c"] → runs 3 iterations, each gets item injected.
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "result-a", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
			{Text: "result-b", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
			{Text: "result-c", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}
	var tools []goai.Tool

	wf := newTestWorkflow(
		[]Step{
			{
				ID:           "process",
				Instructions: "Process this item",
				Loop: &Loop{
					ForEach: []any{"a", "b", "c"},
				},
			},
		},
		nil,
	)

	exec := newTestExecutor(llm, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}

	sr := result.Steps["process"]
	if sr == nil {
		t.Fatal("step 'process' missing from results")
	}
	if sr.Status != spec.StepCompleted {
		t.Errorf("step status = %q, want %q", sr.Status, spec.StepCompleted)
	}

	// Should have called LLM 3 times (once per item).
	if len(llm.calls) != 3 {
		t.Errorf("llm calls = %d, want 3", len(llm.calls))
	}

	// Token aggregation: 3 × (10+5).
	if sr.Tokens.InputTokens != 30 {
		t.Errorf("input tokens = %d, want 30", sr.Tokens.InputTokens)
	}
	if sr.Tokens.OutputTokens != 15 {
		t.Errorf("output tokens = %d, want 15", sr.Tokens.OutputTokens)
	}
}

func TestExecutor_ForEach_StaticArray_InnerDAG(t *testing.T) {
	// forEach with loop.steps: multi-step inner DAG per item.
	// Inner DAG: step-a → step-b (chain). 2 items = 4 LLM calls.
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "item1-step-a", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			{Text: "item1-step-b", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			{Text: "item2-step-a", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			{Text: "item2-step-b", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	var tools []goai.Tool

	wf := newTestWorkflow(
		[]Step{
			{
				ID:           "process",
				Agent:        "worker",
				Instructions: "Process items",
				Loop: &Loop{
					ForEach: []any{"item1", "item2"},
					Steps: []Step{
						{ID: "step-a", Agent: "worker", Instructions: "first step"},
						{ID: "step-b", Agent: "worker", DependsOn: []string{"step-a"}, Instructions: "second step"},
					},
				},
			},
		},
		map[string]AgentConfig{
			"worker": {Description: "Worker agent"},
		},
	)

	exec := newTestExecutor(llm, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}

	sr := result.Steps["process"]
	if sr == nil {
		t.Fatal("step 'process' missing from results")
	}
	if sr.Status != spec.StepCompleted {
		t.Errorf("step status = %q, want %q", sr.Status, spec.StepCompleted)
	}

	// 2 items × 2 inner steps = 4 LLM calls.
	if len(llm.calls) != 4 {
		t.Errorf("llm calls = %d, want 4", len(llm.calls))
	}
}

func TestExecutor_ForEach_MaxConcurrency(t *testing.T) {
	// forEach with maxConcurrency=2 → at most 2 parallel iterations.
	var mu sync.Mutex
	var concurrent, maxConcurrent int

	cLLM := &concurrencyTrackingLLM{
		mu:            &mu,
		concurrent:    &concurrent,
		maxConcurrent: &maxConcurrent,
		response:      &provider.GenerateResult{Text: "done", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
	}
	var tools []goai.Tool

	wf := newTestWorkflow(
		[]Step{
			{
				ID:           "process",
				Instructions: "Process item",
				Loop: &Loop{
					ForEach:        []any{"a", "b", "c", "d"},
					MaxConcurrency: 2,
				},
			},
		},
		nil,
	)

	exec := newTestExecutor(cLLM, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}

	// maxConcurrent should never exceed 2.
	mu.Lock()
	mc := maxConcurrent
	mu.Unlock()
	if mc > 2 {
		t.Errorf("max concurrent = %d, want <= 2", mc)
	}
}

func TestExecutor_ForEach_ItemInjection(t *testing.T) {
	// Verify each iteration's prompt includes "## forEach Item" section.
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "r1", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			{Text: "r2", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	var tools []goai.Tool

	wf := newTestWorkflow(
		[]Step{
			{
				ID:           "process",
				Instructions: "Handle this",
				Loop: &Loop{
					ForEach: []any{"alpha", "beta"},
				},
			},
		},
		nil,
	)

	exec := newTestExecutor(llm, tools, wf)
	_, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check that LLM calls include forEach item injection.
	if len(llm.calls) != 2 {
		t.Fatalf("llm calls = %d, want 2", len(llm.calls))
	}

	// Check that each item ("alpha", "beta") appears in exactly one LLM call.
	foundItems := make(map[string]bool)
	for i := range 2 {
		promptText := ""
		for _, p := range llm.calls[i].Messages[0].Content {
			if p.Type == provider.PartText {
				promptText += p.Text
			}
		}
		if !strings.Contains(promptText, "## forEach Item") {
			t.Errorf("call %d: missing '## forEach Item' section in prompt", i)
		}
		if !strings.Contains(promptText, "index:") {
			t.Errorf("call %d: missing index in prompt", i)
		}
		for _, item := range []string{"alpha", "beta"} {
			if strings.Contains(promptText, item) {
				foundItems[item] = true
			}
		}
	}
	for _, item := range []string{"alpha", "beta"} {
		if !foundItems[item] {
			t.Errorf("item %q not found in any LLM call prompt", item)
		}
	}
}

func TestExecutor_ForEach_ResultAggregation(t *testing.T) {
	// Final StepResult aggregates all iteration results.
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "result-1", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
			{Text: "result-2", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
			{Text: "result-3", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}
	var tools []goai.Tool

	wf := newTestWorkflow(
		[]Step{
			{
				ID:           "process",
				Instructions: "Process",
				Loop: &Loop{
					ForEach: []any{"x", "y", "z"},
				},
			},
		},
		nil,
	)

	exec := newTestExecutor(llm, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sr := result.Steps["process"]
	if sr == nil {
		t.Fatal("step 'process' missing")
	}

	// Result should contain iterations array.
	if sr.Result == nil {
		t.Fatal("expected Result map with iterations, got nil")
	}
	iterations, ok := sr.Result["iterations"]
	if !ok {
		t.Fatal("expected 'iterations' key in Result")
	}
	iterSlice, ok := iterations.([]any)
	if !ok {
		t.Fatalf("iterations type = %T, want []any", iterations)
	}
	if len(iterSlice) != 3 {
		t.Errorf("iterations count = %d, want 3", len(iterSlice))
	}

	// Content should equal the last iteration's (index 2) content.
	// The aggregation loop sets lastContent from the highest index in results.
	lastIter, ok := iterSlice[2].(map[string]any)
	if !ok {
		t.Fatalf("iterations[2] type = %T, want map[string]any", iterSlice[2])
	}
	lastIterContent, _ := lastIter["content"].(string)
	if sr.Content != lastIterContent {
		t.Errorf("sr.Content = %q, want last iteration content %q", sr.Content, lastIterContent)
	}
}

func TestExecutor_ForEach_IterationFailure(t *testing.T) {
	// One iteration fails → step fails (cascade).
	llm := &failingLLM{
		failOnCall: 2, // Second iteration fails.
		okResponse: &provider.GenerateResult{Text: "ok", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
	}
	var tools []goai.Tool

	wf := newTestWorkflow(
		[]Step{
			{
				ID:           "process",
				Instructions: "Process",
				Loop: &Loop{
					ForEach: []any{"a", "b", "c"},
				},
			},
		},
		nil,
	)

	exec := newTestExecutor(llm, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Step should fail when any iteration fails.
	sr := result.Steps["process"]
	if sr == nil {
		t.Fatal("step 'process' missing")
	}
	if sr.Status != spec.StepFailed {
		t.Errorf("status = %q, want %q", sr.Status, spec.StepFailed)
	}
}

func TestExecutor_ForEach_CELExpression(t *testing.T) {
	// forEach: "steps.list.result.items" - CEL expression that evaluates to array.
	// The "list" step uses submit_result to produce a structured result with items.
	// The "process" step iterates over the items via CEL evaluation.
	var callCount atomic.Int32
	llm := &sequentialCallLLM{fn: func(req provider.GenerateParams) *provider.GenerateResult {
		n := callCount.Add(1)
		switch n {
		case 1:
			// "list" step: return submit_result with items.
			return &provider.GenerateResult{
				Text: "listed",
				ToolCalls: []provider.ToolCall{
					{ID: "sr1", Name: "submit_result", Input: json.RawMessage(`{"items":["x","y"]}`)},
				},
				Usage: provider.Usage{InputTokens: 10, OutputTokens: 5},
			}
		default:
			// forEach iterations.
			return &provider.GenerateResult{
				Text:  "processed",
				Usage: provider.Usage{InputTokens: 5, OutputTokens: 3},
			}
		}
	}}
	var tools []goai.Tool

	wf := &Workflow{
		Name: "cel-foreach-test",
		Agents: map[string]AgentConfig{
			"lister": {
				Description: "Lists items",
				ResultSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"items": map[string]any{"type": "array"},
					},
					"required": []any{"items"},
				},
			},
		},
		Steps: []Step{
			{ID: "list", Agent: "lister", Instructions: "List items"},
			{
				ID:           "process",
				DependsOn:    []string{"list"},
				Instructions: "Process item",
				Loop: &Loop{
					ForEach: "steps.list.result.items", // CEL expression (string type).
				},
			},
		},
	}

	exec := newTestExecutor(llm, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The process step must succeed with real CEL evaluation.
	sr := result.Steps["process"]
	if sr == nil {
		t.Fatal("step 'process' missing from results")
	}
	if sr.Status != spec.StepCompleted {
		t.Errorf("step status = %q, want %q (error: %v)", sr.Status, spec.StepCompleted, sr.Error)
	}

	// Should have called LLM 3 times: 1 for list + 2 for forEach iterations.
	if callCount.Load() != 3 {
		t.Errorf("llm calls = %d, want 3", callCount.Load())
	}
}

func TestExecutor_ForEach_InnerDAG_ItemInjection(t *testing.T) {
	// Verify that inner DAG steps receive forEach item context in their prompts.
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			// item "alpha": step-a, step-b
			{Text: "alpha-a", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			{Text: "alpha-b", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			// item "beta": step-a, step-b
			{Text: "beta-a", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			{Text: "beta-b", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	var tools []goai.Tool

	wf := newTestWorkflow(
		[]Step{
			{
				ID:           "process",
				Instructions: "Process items",
				Loop: &Loop{
					ForEach: []any{"alpha", "beta"},
					Steps: []Step{
						{ID: "step-a", Instructions: "first step"},
						{ID: "step-b", DependsOn: []string{"step-a"}, Instructions: "second step"},
					},
				},
			},
		},
		nil,
	)

	exec := newTestExecutor(llm, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}

	// Verify that the first inner step (step-a) of each item has forEach item context.
	// The LLM should have received prompts containing "## forEach Item" for inner DAG steps.
	if len(llm.calls) != 4 {
		t.Fatalf("llm calls = %d, want 4", len(llm.calls))
	}

	// At least the first step of each item should have "## forEach Item" in its prompt.
	foundAlpha := false
	foundBeta := false
	for _, call := range llm.calls {
		promptText := ""
		for _, p := range call.Messages[0].Content {
			if p.Type == provider.PartText {
				promptText += p.Text
			}
		}
		if strings.Contains(promptText, "## forEach Item") {
			if strings.Contains(promptText, `"alpha"`) {
				foundAlpha = true
			}
			if strings.Contains(promptText, `"beta"`) {
				foundBeta = true
			}
		}
	}
	if !foundAlpha {
		t.Error("no inner DAG prompt contained forEach item 'alpha'")
	}
	if !foundBeta {
		t.Error("no inner DAG prompt contained forEach item 'beta'")
	}
}

func TestExecutor_ForEach_DefaultParallel(t *testing.T) {
	// Issue 6: Default should be all-parallel (maxConcurrency=len(items)).
	// With 4 items and no maxConcurrency set, all should run in parallel.
	var mu sync.Mutex
	var concurrent, maxConcurrent int

	cLLM := &concurrencyTrackingLLM{
		mu:            &mu,
		concurrent:    &concurrent,
		maxConcurrent: &maxConcurrent,
		response:      &provider.GenerateResult{Text: "done", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
	}
	var tools []goai.Tool

	wf := newTestWorkflow(
		[]Step{
			{
				ID:           "process",
				Instructions: "Process item",
				Loop: &Loop{
					ForEach: []any{"a", "b", "c", "d"},
					// No MaxConcurrency set - default should be all-parallel.
				},
			},
		},
		nil,
	)

	exec := newTestExecutor(cLLM, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}

	// With default all-parallel and 4 items, maxConcurrent should be > 1
	// (at least 2 in practice, likely 4 on multi-core machines).
	mu.Lock()
	mc := maxConcurrent
	mu.Unlock()
	if mc <= 1 {
		t.Errorf("max concurrent = %d, want > 1 (default should be all-parallel)", mc)
	}
}

func TestExecutor_ForEach_NamespacedIDs(t *testing.T) {
	// Issue 4: forEach iteration results should have namespaced IDs like "stepID[index]".
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "r0", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			{Text: "r1", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	var tools []goai.Tool

	wf := newTestWorkflow(
		[]Step{
			{
				ID:           "proc",
				Instructions: "Process",
				Loop:         &Loop{ForEach: []any{"x", "y"}},
			},
		},
		nil,
	)

	exec := newTestExecutor(llm, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sr := result.Steps["proc"]
	if sr == nil {
		t.Fatal("step 'proc' missing")
	}

	iterations, ok := sr.Result["iterations"].([]any)
	if !ok {
		t.Fatalf("iterations type = %T, want []any", sr.Result["iterations"])
	}
	for i, iter := range iterations {
		m, ok := iter.(map[string]any)
		if !ok {
			t.Fatalf("iteration %d type = %T, want map[string]any", i, iter)
		}
		expectedID := "proc[" + strconv.Itoa(i) + "]"
		if id, ok := m["id"].(string); !ok || id != expectedID {
			t.Errorf("iteration %d id = %q, want %q", i, m["id"], expectedID)
		}
	}
}

// --- Coverage: forEach with Progress events (executor.go lines 1302-1311, 1499-1513) ---

func TestExecutor_ForEach_WithProgress(t *testing.T) {
	var mu sync.Mutex
	var events []Event
	sink := &forEachProgressSink{mu: &mu, events: &events}

	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "r1", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			{Text: "r2", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	wf := newTestWorkflow(
		[]Step{
			{
				ID:           "proc",
				Instructions: "process",
				Loop:         &Loop{ForEach: []any{"a", "b"}},
			},
		},
		nil,
	)
	exec := newTestExecutor(llm, nil, wf)
	exec.Progress = sink
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want completed", result.Status)
	}
	// Should have emitted EventStepStart and EventStepEnd for the forEach.
	mu.Lock()
	defer mu.Unlock()
	var hasStart, hasEnd bool
	for _, e := range events {
		if e.StepID == "proc" && e.Type == types.EventStepStart {
			hasStart = true
		}
		if e.StepID == "proc" && e.Type == types.EventStepEnd {
			hasEnd = true
		}
	}
	if !hasStart {
		t.Error("expected EventStepStart for proc")
	}
	if !hasEnd {
		t.Error("expected EventStepEnd for proc")
	}
}

type forEachProgressSink struct {
	mu     *sync.Mutex
	events *[]Event
}

func (s *forEachProgressSink) OnEvent(_ context.Context, e Event) {
	s.mu.Lock()
	*s.events = append(*s.events, e)
	s.mu.Unlock()
}
func (s *forEachProgressSink) OnOutput(_ context.Context, _ Output) {}

// --- Coverage: forEach with failed iteration + Progress (executor.go lines 1470-1473, 1499-1503) ---

func TestExecutor_ForEach_FailedIteration(t *testing.T) {
	// One iteration fails → entire step should fail.
	callCount := atomic.Int32{}
	failLLM := &forEachFailLLM{failOnCall: 2, count: &callCount}
	wf := newTestWorkflow(
		[]Step{
			{
				ID:           "proc",
				Instructions: "process",
				Loop:         &Loop{ForEach: []any{"a", "b", "c"}, MaxConcurrency: 1},
			},
		},
		nil,
	)
	var mu sync.Mutex
	var events []Event
	sink := &forEachProgressSink{mu: &mu, events: &events}

	exec := newTestExecutor(failLLM, nil, wf)
	exec.Progress = sink
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr := result.Steps["proc"]
	if sr == nil {
		t.Fatal("proc missing")
	}
	if sr.Status != spec.StepFailed {
		t.Errorf("status = %q, want failed", sr.Status)
	}
	// Should have EventError for the failed forEach.
	mu.Lock()
	defer mu.Unlock()
	var hasError bool
	for _, e := range events {
		if e.StepID == "proc" && e.Type == types.EventError {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Error("expected EventError for proc")
	}
}

type forEachFailLLM struct {
	failOnCall int32
	count      *atomic.Int32
}

func (f *forEachFailLLM) ModelID() string { return "foreach-fail-mock" }

func (f *forEachFailLLM) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	n := f.count.Add(1)
	if n == f.failOnCall {
		return nil, errors.New("llm failure")
	}
	return &provider.GenerateResult{Text: "ok", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}, FinishReason: provider.FinishStop}, nil
}

func (f *forEachFailLLM) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, errors.New("not implemented")
}

// --- Coverage: forEach cancellation path (executor.go lines 1378-1380, 1486-1493) ---

func TestExecutor_ForEach_Cancellation(t *testing.T) {
	// Use a slow LLM with short timeout and low maxConcurrency.
	// Only 1 item can run at a time, remaining items wait on semaphore.
	// When timeout fires, the waiting items get StepCancelled.
	slowLLM := &forEachSlowLLM{}
	wf := &Workflow{
		Name: "foreach-cancel",
		Steps: []Step{
			{
				ID:           "proc",
				Instructions: "process",
				Loop:         &Loop{ForEach: []any{"a", "b", "c", "d"}, MaxConcurrency: 1},
			},
		},
		Options: WorkflowOptions{Timeout: Duration(5 * time.Millisecond)},
	}
	exec := newTestExecutor(slowLLM, nil, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr := result.Steps["proc"]
	if sr == nil {
		t.Fatal("proc missing")
	}
	// Step should fail or be cancelled due to timeout.
	if sr.Status == spec.StepCompleted {
		t.Error("expected non-completed status (cancelled or failed)")
	}
}

type forEachSlowLLM struct{}

func (s *forEachSlowLLM) ModelID() string { return "foreach-slow-mock" }

func (s *forEachSlowLLM) DoGenerate(ctx context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(100 * time.Millisecond):
		return &provider.GenerateResult{Text: "ok", FinishReason: provider.FinishStop}, nil
	}
}

func (s *forEachSlowLLM) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, errors.New("not implemented")
}

// --- Coverage: forEach inner DAG with failed status (executor.go lines 1705-1709) ---

func TestExecutor_ForEach_InnerDAG_FailedStatus(t *testing.T) {
	// forEach with inner steps where one inner step fails.
	failLLM := &forEachFailLLM{failOnCall: 2, count: &atomic.Int32{}}
	wf := newTestWorkflow(
		[]Step{
			{
				ID:           "proc",
				Instructions: "process",
				Loop: &Loop{
					ForEach: []any{"a"},
					Steps: []Step{
						{ID: "inner1", Instructions: "first inner"},
						{ID: "inner2", DependsOn: []string{"inner1"}, Instructions: "second inner"},
					},
				},
			},
		},
		nil,
	)
	exec := newTestExecutor(failLLM, nil, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr := result.Steps["proc"]
	if sr == nil {
		t.Fatal("proc missing")
	}
	if sr.Status != spec.StepFailed {
		t.Errorf("status = %q, want failed (error: %v)", sr.Status, sr.Error)
	}
}

// --- Coverage: forEach inner DAG with timeout (executor.go line 1661-1663) ---

func TestExecutor_ForEach_InnerDAG_Timeout(t *testing.T) {
	slowLLM := &forEachSlowLLM{}
	timeout := Duration(10 * time.Millisecond)
	wf := newTestWorkflow(
		[]Step{
			{
				ID:           "proc",
				Instructions: "process",
				Timeout:      timeout,
				Loop: &Loop{
					ForEach: []any{"a"},
					Steps: []Step{
						{ID: "inner1", Instructions: "slow inner"},
					},
				},
			},
		},
		nil,
	)
	exec := newTestExecutor(slowLLM, nil, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr := result.Steps["proc"]
	if sr == nil {
		t.Fatal("proc missing")
	}
	// Should fail due to timeout.
	if sr.Status == spec.StepCompleted {
		t.Error("expected non-completed status (timeout)")
	}
}

// --- Coverage: forEach CEL eval failure (executor.go line 1285-1287) ---

func TestExecutor_ForEach_CELEvalError(t *testing.T) {
	llm := &mockModel{}
	wf := newTestWorkflow(
		[]Step{
			{
				ID:           "proc",
				Instructions: "process",
				Loop:         &Loop{ForEach: `!!!invalid_cel`},
			},
		},
		nil,
	)
	exec := newTestExecutor(llm, nil, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr := result.Steps["proc"]
	if sr == nil || sr.Status != spec.StepFailed {
		t.Fatalf("expected proc to fail, got %v", sr)
	}
}

// Compile-time checks to keep imports used.
var _ json.Marshaler = json.RawMessage{} // keep encoding/json import

func TestExecutor_ForEach_AllCancelledNoFirstErr(t *testing.T) {
	// Context cancelled before any iteration starts - all StepCancelled, firstErr nil.
	llm := &mockModel{responses: []*provider.GenerateResult{{Text: "x", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}}}}
	wf := &Workflow{
		Name: "foreach-all-cancel",
		Steps: []Step{
			{ID: "proc", Instructions: "process", Loop: &Loop{ForEach: []any{"a", "b"}}},
		},
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // cancel immediately
	exec := newTestExecutor(llm, nil, wf)
	// Override context by running with cancelled context.
	result, err := exec.Run(ctx)
	if err != nil {
		// Cancelled context may cause run to error - that's fine.
		t.Logf("Run error (expected): %v", err)
		return
	}
	sr := result.Steps["proc"]
	if sr == nil {
		t.Fatal("proc missing")
	}
	t.Logf("proc status=%s error=%v", sr.Status, sr.Error)
	if sr.Status == spec.StepCompleted {
		t.Error("expected non-completed status when context cancelled")
	}
}
