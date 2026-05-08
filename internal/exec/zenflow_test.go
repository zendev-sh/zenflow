package exec

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"go.uber.org/goleak"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/spec"
	"github.com/zendev-sh/zenflow/internal/types"
)

// TestMain installs the goleak guard for the zenflow package's test
// suite. G5 : every test process exits via VerifyTestMain, so
// any goroutine still alive at SHUTDOWN is reported as a leak. Tests
// that intentionally background work (e.g. delivery_engine, pump,
// runStep) must therefore wire deterministic Stop/cancel before
// returning. Pre-existing leaks surface here as failures.
func TestMain(m *testing.M) {
	// Base ignores apply to every build (unit + e2e). These are all
	// fixture artifacts (hungModel/blockingModel test stubs) or generic
	// runtime frames that also appear in unit-only runs.
	opts := []goleak.Option{
 // time.Tick (used by tickerClock + production engine) can park
 // goroutines even after stop on macOS; ignore the runtime
 // timer goroutine specifically.
		goleak.IgnoreTopFunction("time.Sleep"),
 // timeout tests use a deliberately-hung model (`select {}`)
 // to reproduce a provider that ignores ctx cancellation. The
 // goroutines blocked in DoGenerate/DoStream cannot be reaped
 // because the test contract is "the higher layer (executor /
 // runStep / waitOrAbort) MUST surface ctx.Err despite the
 // blocked downstream call". Ignoring these specific blocked
 // stacks lets the rest of the suite still benefit from goleak.
		goleak.IgnoreTopFunction("github.com/zendev-sh/zenflow.(*hungModel).DoGenerate"),
		goleak.IgnoreTopFunction("github.com/zendev-sh/zenflow.(*hungModel).DoStream"),
 // waitOrAbort spawns a WaitGroup-Wait goroutine that only exits
 // when every step goroutine returns; with hungModel above this
 // also leaks by design. Top-of-stack here is the runtime
 // semaphore acquire; match by AnyFunction so we cover both the
 // runtime frame and the waitOrAbort.func1 caller frame.
		goleak.IgnoreAnyFunction("github.com/zendev-sh/zenflow/internal/exec.waitOrAbort.func1"),
 // blockingModel and similar test stubs may sit in chan receive
 // for the same reason.
		goleak.IgnoreAnyFunction("github.com/zendev-sh/zenflow/internal/exec.(*blockingModel).DoGenerate"),
		goleak.IgnoreAnyFunction("github.com/zendev-sh/zenflow/internal/exec.(*blockingModel).DoStream"),
		goleak.IgnoreAnyFunction("github.com/zendev-sh/zenflow/internal/exec.(*hungModel).DoGenerate"),
		goleak.IgnoreAnyFunction("github.com/zendev-sh/zenflow/internal/exec.(*hungModel).DoStream"),
	}
	// I2 : scope HTTP/2 / persistConn ignores to e2e-tagged
	// builds only. In unit-only runs (`go test ./...`) these ignores
	// stay OFF so any new leak whose root cause sits in HTTP/2 readLoop
	// surfaces as a failure instead of being silently masked. Toggled
	// via the `e2eEnabled` const, which is set true in
	// e2e_enabled_e2e.go (//go:build e2e) and false in
	// e2e_enabled_default.go (//go:build !e2e).
	if e2eEnabled {
		opts = append(opts,
 // H1 : real-provider HTTP/2 client connections (used
 // by E2E tests against Bedrock / Azure / Gemini) keep their
 // readLoop / writeLoop goroutines parked on idle keep-alive
 // connections. These are owned by net/http's connection pool
 // and only exit when the pool's MaxIdleConnsPerHost timer
 // fires (default 90s). Calling http.Client.CloseIdleConnections
 // during shutdown is not reliable across all transports we
 // embed (vendored providers reuse a global pool). Ignoring
 // these specific top-of-stack frames keeps goleak useful for
 // detecting REAL leaks while letting E2E tests exit 0.
			goleak.IgnoreAnyFunction("net/http.(*http2ClientConn).readLoop"),
			goleak.IgnoreAnyFunction("net/http.(*http2clientConnReadLoop).run"),
			goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
			goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"),
		)
	}
	goleak.VerifyTestMain(m, opts...)
}

func TestNew_Defaults(t *testing.T) {
	zf := New()
	if zf == nil {
		t.Fatal("New() returned nil")
	}

	// Default storage should be MemoryStorage.
	if zf.storage == nil {
		t.Error("default storage is nil")
	}

	// Default max concurrency should be 5.
	if zf.maxConcurrency != 5 {
		t.Errorf("maxConcurrency = %d, want 5", zf.maxConcurrency)
	}
}

func TestNew_WithOptions(t *testing.T) {
	llm := &mockModel{}
	tools := []goai.Tool{makeTool("test", "test tool", "ok")}
	store := NewMemoryStorage()

	zf := New(
		WithModel(llm),
		WithTools(tools...),
		WithStorage(store),
		WithMaxConcurrency(10),
		WithDefaultModel("claude-4-sonnet"),
	)
	if zf == nil {
		t.Fatal("New() returned nil")
	}
	if zf.model != llm {
		t.Error("LLM not set")
	}
	if len(zf.tools) == 0 {
		t.Error("Tools not set")
	}
	if zf.storage != store {
		t.Error("Storage not set")
	}
	if zf.maxConcurrency != 10 {
		t.Errorf("maxConcurrency = %d, want 10", zf.maxConcurrency)
	}
	if zf.defaultModel != "claude-4-sonnet" {
		t.Errorf("defaultModel = %q, want %q", zf.defaultModel, "claude-4-sonnet")
	}
}

func TestRunFlow_Integration(t *testing.T) {
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "design done", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
			{Text: "impl done", Usage: provider.Usage{InputTokens: 20, OutputTokens: 10}},
			{Text: "review done", Usage: provider.Usage{InputTokens: 15, OutputTokens: 8}},
		},
	}
	var tools []goai.Tool

	zf := New(
		WithModel(llm),
		WithTools(tools...),
		WithDefaultModel("gpt-4o"),
	)

	wf, err := ParseWorkflow([]byte(testWorkflowYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	result, err := zf.RunFlow(t.Context(), wf)
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
	if len(result.Steps) != 3 {
		t.Errorf("len(steps) = %d, want 3", len(result.Steps))
	}

	// Verify total tokens.
	if result.Tokens.InputTokens != 45 {
		t.Errorf("input tokens = %d, want 45", result.Tokens.InputTokens)
	}
}

func TestWithPermissions(t *testing.T) {
	o := New(WithPermissions(nil))
	if o == nil {
		t.Fatal("New returned nil")
	}
	if o.permissions != nil {
		t.Error("permissions should be nil when passed nil")
	}
}

func TestWithProgress(t *testing.T) {
	o := New(WithProgress(nil))
	if o == nil {
		t.Fatal("New returned nil")
	}
	if o.progress != nil {
		t.Error("progress should be nil when passed nil")
	}
}

func TestWithMaxTurns(t *testing.T) {
	o := New(WithMaxTurns(25))
	if o.maxTurns != 25 {
		t.Errorf("maxTurns = %d, want 25", o.maxTurns)
	}
}

func TestWithMaxDepth(t *testing.T) {
	o := New(WithMaxDepth(3))
	if o.maxDepth != 3 {
		t.Errorf("maxDepth = %d, want 3", o.maxDepth)
	}
}

func TestOrchestrator_HasLLM(t *testing.T) {
	o := New()
	if o.HasLLM() {
		t.Error("HasLLM() = true, want false (no LLM configured)")
	}

	o2 := New(WithModel(&mockModel{}))
	if !o2.HasLLM() {
		t.Error("HasLLM() = false, want true (LLM configured)")
	}
}

// --- Coverage gap tests ---

func TestRunFlow_NilWorkflow(t *testing.T) {
	o := New(WithModel(&mockModel{}))
	_, err := o.RunFlow(t.Context(), nil)
	if err == nil {
		t.Fatal("expected error for nil workflow")
	}
	if !errors.Is(err, ErrWorkflowNil) {
		t.Errorf("error = %v, want errors.Is(err, ErrWorkflowNil)", err)
	}
}

func TestRunAgent_NoLLM(t *testing.T) {
	o := New()
	_, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "hello"})
	if err == nil {
		t.Fatal("expected error for missing LLM")
	}
	if !errors.Is(err, ErrModelRequired) {
		t.Errorf("error = %v, want ErrModelRequired", err)
	}
}

func TestRunAgent_WithToolDefs(t *testing.T) {
	// Covers L65-67: parentToolNames collection when tools have defs.
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "done", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	tools := []goai.Tool{
		{Name: "read_file", Description: "reads files"},
		{Name: "write_file", Description: "writes files"},
	}

	o := New(
		WithModel(llm),
		WithTools(tools...),
		WithDefaultModel("gpt-4o"),
	)

	result, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "hello"})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if result.Content != "done" {
		t.Errorf("content = %q, want %q", result.Content, "done")
	}
}

// --- mockTracer for tracing coverage ---

type mockTracer struct {
	mu    sync.Mutex
	spans []mockSpan
}

type mockSpan struct {
	name  string
	attrs map[string]string
	err   error
	ended bool
}

type tracerCtxKey struct{}

func (m *mockTracer) StartSpan(ctx context.Context, name string, attrs map[string]string) context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := len(m.spans)
	m.spans = append(m.spans, mockSpan{name: name, attrs: attrs})
	return context.WithValue(ctx, tracerCtxKey{}, idx)
}

func (m *mockTracer) EndSpan(ctx context.Context, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx, ok := ctx.Value(tracerCtxKey{}).(int)
	if !ok || idx >= len(m.spans) {
		return
	}
	m.spans[idx].ended = true
	m.spans[idx].err = err
}

func (m *mockTracer) spansByName(name string) []mockSpan {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []mockSpan
	for _, s := range m.spans {
		if s.name == name {
			result = append(result, s)
		}
	}
	return result
}

// --- truncate tests ---

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"zero max", "hello", 0, ""},
		{"negative max", "hello", -1, ""},
		{"short enough", "hi", 10, "hi"},
		{"exact length", "hello", 5, "hello"},
		{"maxLen 1", "hello", 1, "h"},
		{"maxLen 2", "hello", 2, "he"},
		{"maxLen 3", "hello", 3, "hel"},
		{"truncated with ellipsis", "hello world", 8, "hello..."},
		{"unicode", "こんにちは世界", 5, "こん..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

// --- WithTracer option test ---

func TestWithTracer(t *testing.T) {
	tr := &mockTracer{}
	o := New(WithTracer(tr))
	if o.tracer != tr {
		t.Error("tracer not set by WithTracer")
	}
}

// --- RunAgent with tracer ---

func TestRunAgent_WithTracer_Success(t *testing.T) {
	tr := &mockTracer{}
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "traced", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	o := New(WithModel(llm), WithTracer(tr), WithDefaultModel("test-model"))
	result, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "trace me"})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if result.Content != "traced" {
		t.Errorf("content = %q, want %q", result.Content, "traced")
	}
	spans := tr.spansByName("zenflow.agent")
	if len(spans) != 1 {
		t.Fatalf("expected 1 agent span, got %d", len(spans))
	}
	if !spans[0].ended {
		t.Error("agent span not ended")
	}
	if spans[0].err != nil {
		t.Errorf("agent span error = %v, want nil", spans[0].err)
	}
	if spans[0].attrs["zenflow.agent.prompt"] != "trace me" {
		t.Errorf("prompt attr = %q", spans[0].attrs["zenflow.agent.prompt"])
	}
}

func TestRunAgent_WithTracer_Error(t *testing.T) {
	tr := &mockTracer{}
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "", Usage: provider.Usage{}, ToolCalls: []provider.ToolCall{{ID: "1", Name: "no_such_tool"}}},
		},
	}
	// Use a tools executor that returns an error.
	var tools []goai.Tool
	o := New(WithModel(llm), WithTools(tools...), WithTracer(tr), WithDefaultModel("test-model"))
	// The agent will try to call "no_such_tool" - the nil returns isError=true.
	// But the agent runner still returns success with the tool error embedded.
	// We need an actual error from RunAgent. Let's inject GenerateRunID failure.
	orig := GenerateRunID
	t.Cleanup(func() { GenerateRunID = orig })
	GenerateRunID = func() (string, error) { return "", fmt.Errorf("id fail") }

	_, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "fail"})
	if err == nil {
		t.Fatal("expected error")
	}
	// tracer should NOT have been called since error happened before tracer.StartSpan
	// Actually looking at code: GenerateRunID is called before tracer.StartSpan, so no spans.
	spans := tr.spansByName("zenflow.agent")
	if len(spans) != 0 {
		t.Errorf("expected 0 agent spans (error before StartSpan), got %d", len(spans))
	}
}

func TestRunAgent_WithTracer_LLMError(t *testing.T) {
	// Test tracer EndSpan with error when LLM call fails.
	tr := &mockTracer{}
	failLLM := &failingLLM{
		failOnCall: 1,
		okResponse: &provider.GenerateResult{Text: "ok"},
	}
	o := New(WithModel(failLLM), WithTracer(tr), WithDefaultModel("test-model"))
	_, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "fail please"})
	if err == nil {
		t.Fatal("expected error from failing LLM")
	}
	spans := tr.spansByName("zenflow.agent")
	if len(spans) != 1 {
		t.Fatalf("expected 1 agent span, got %d", len(spans))
	}
	if !spans[0].ended {
		t.Error("agent span not ended")
	}
	if spans[0].err == nil {
		t.Error("agent span error should be set on LLM failure")
	}
}

// --- RunFlow with tracer ---

func TestRunFlow_WithTracer_Success(t *testing.T) {
	tr := &mockTracer{}
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "step done", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}
	o := New(WithModel(llm), WithTracer(tr), WithDefaultModel("test-model"))
	wf := &Workflow{
		Name:  "traced-flow",
		Steps: []Step{{ID: "s1", Instructions: "do it"}},
	}
	result, err := o.RunFlow(t.Context(), wf)
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
	flowSpans := tr.spansByName("zenflow.flow")
	if len(flowSpans) != 1 {
		t.Fatalf("expected 1 flow span, got %d", len(flowSpans))
	}
	if !flowSpans[0].ended {
		t.Error("flow span not ended")
	}
	if flowSpans[0].err != nil {
		t.Errorf("flow span error = %v, want nil", flowSpans[0].err)
	}
}

func TestRunFlow_WithTracer_Failed(t *testing.T) {
	tr := &mockTracer{}
	failLLM := &failingLLM{
		failOnCall: 1,
		okResponse: &provider.GenerateResult{Text: "ok"},
	}
	o := New(WithModel(failLLM), WithTracer(tr), WithDefaultModel("test-model"))
	wf := &Workflow{
		Name:  "fail-flow",
		Steps: []Step{{ID: "s1", Instructions: "do it"}},
	}
	result, err := o.RunFlow(t.Context(), wf)
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	// Step fails -> workflow StatusFailed -> tracer EndSpan with error
	if result.Status != spec.StatusFailed {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusFailed)
	}
	flowSpans := tr.spansByName("zenflow.flow")
	if len(flowSpans) != 1 {
		t.Fatalf("expected 1 flow span, got %d", len(flowSpans))
	}
	if !flowSpans[0].ended {
		t.Error("flow span not ended")
	}
	if flowSpans[0].err == nil {
		t.Error("flow span error should be set on workflow failure")
	}
}

func TestRunFlow_WithTracer_GenerateRunIDError(t *testing.T) {
	tr := &mockTracer{}
	o := New(WithModel(&mockModel{}), WithTracer(tr))
	orig := GenerateRunID
	t.Cleanup(func() { GenerateRunID = orig })
	GenerateRunID = func() (string, error) { return "", errors.New("id fail") }

	_, err := o.RunFlow(t.Context(), &Workflow{Name: "test", Steps: []Step{{ID: "s1"}}})
	if err == nil {
		t.Fatal("expected error")
	}
	// Error happens before tracer.StartSpan is called (in runFlowWithID).
	// Actually RunFlow calls GenerateRunID first, then runFlowWithID.
	// So tracer is never invoked.
}

// --- runFlowWithID with tracer (via RunFlow/RunGoal) ---

func TestRunFlowWithID_TracerPartialStatus(t *testing.T) {
	// When some steps succeed and some fail, status is StatusPartial.
	// The tracer should receive an error for partial status.
	tr := &mockTracer{}
	callCount := 0
	llm := &mockModel{}
	// We need first step to succeed and second to fail.
	// Use failingLLM that fails on call 2.
	failLLM := &failingLLM{
		failOnCall: 2,
		okResponse: &provider.GenerateResult{Text: "ok", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
	}
	_ = llm
	_ = callCount

	o := New(WithModel(failLLM), WithTracer(tr), WithDefaultModel("test-model"))
	wf := &Workflow{
		Name: "partial-flow",
		Steps: []Step{
			{ID: "s1", Instructions: "step 1"},
			{ID: "s2", Instructions: "step 2"}, // this will fail
		},
	}
	result, err := o.RunFlow(t.Context(), wf)
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if result.Status != spec.StatusPartial {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusPartial)
	}
	flowSpans := tr.spansByName("zenflow.flow")
	if len(flowSpans) != 1 {
		t.Fatalf("expected 1 flow span, got %d", len(flowSpans))
	}
	if flowSpans[0].err == nil {
		t.Error("flow span error should be set on partial status")
	}
}

// --- ResumeFlow with tracer ---

func TestResumeFlow_WithTracer_Success(t *testing.T) {
	tr := &mockTracer{}
	store := NewMemoryStorage()
	ctx := t.Context()

	// Save a run so LoadRun succeeds.
	run := &Run{ID: "run-tr-1", Workflow: &Workflow{Name: "test"}, Status: spec.StatusRunning, Steps: map[string]*StepResult{}}
	if err := store.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "resumed", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	o := New(WithModel(llm), WithStorage(store), WithTracer(tr), WithDefaultModel("test-model"))
	wf := &Workflow{Name: "test", Steps: []Step{{ID: "s1", Instructions: "do it"}}}

	result, err := o.ResumeFlow(ctx, "run-tr-1", wf)
	if err != nil {
		t.Fatalf("ResumeFlow: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
	flowSpans := tr.spansByName("zenflow.flow")
	if len(flowSpans) != 1 {
		t.Fatalf("expected 1 flow span, got %d", len(flowSpans))
	}
	if !flowSpans[0].ended {
		t.Error("flow span not ended")
	}
	if flowSpans[0].attrs["zenflow.resume"] != "true" {
		t.Error("missing resume attribute")
	}
}

func TestResumeFlow_WithTracer_LoadRunError(t *testing.T) {
	tr := &mockTracer{}
	store := NewMemoryStorage()
	// Don't save any run -> LoadRun will fail.
	o := New(WithModel(&mockModel{}), WithStorage(store), WithTracer(tr))
	wf := &Workflow{Name: "test", Steps: []Step{{ID: "s1"}}}

	_, err := o.ResumeFlow(t.Context(), "nonexistent", wf)
	if err == nil {
		t.Fatal("expected error")
	}
	flowSpans := tr.spansByName("zenflow.flow")
	if len(flowSpans) != 1 {
		t.Fatalf("expected 1 flow span, got %d", len(flowSpans))
	}
	if !flowSpans[0].ended {
		t.Error("span not ended on LoadRun error")
	}
	if flowSpans[0].err == nil {
		t.Error("span should have error on LoadRun failure")
	}
}

func TestResumeFlow_WithTracer_LoadSharedMemError(t *testing.T) {
	tr := &mockTracer{}
	// Use a storage that fails LoadSharedMemory.
	store := &failSharedMemStorage{
		Storage: NewMemoryStorage(),
	}
	ctx := t.Context()

	// Save a run so LoadRun succeeds.
	run := &Run{ID: "run-sm-fail", Workflow: &Workflow{Name: "test"}, Status: spec.StatusRunning, Steps: map[string]*StepResult{}}
	if err := store.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	o := New(WithModel(&mockModel{}), WithStorage(store), WithTracer(tr))
	wf := &Workflow{Name: "test", Steps: []Step{{ID: "s1"}}}

	_, err := o.ResumeFlow(ctx, "run-sm-fail", wf)
	if err == nil {
		t.Fatal("expected error from LoadSharedMemory")
	}
	flowSpans := tr.spansByName("zenflow.flow")
	if len(flowSpans) != 1 {
		t.Fatalf("expected 1 flow span, got %d", len(flowSpans))
	}
	if !flowSpans[0].ended {
		t.Error("span not ended on shared mem error")
	}
	if flowSpans[0].err == nil {
		t.Error("span should have error on shared memory failure")
	}
}

// failSharedMemStorage wraps Storage but fails LoadSharedMemory.
type failSharedMemStorage struct {
	Storage
}

func (f *failSharedMemStorage) LoadSharedMemory(_ context.Context, _ string) (map[string]string, error) {
	return nil, errors.New("shared memory load failed")
}

func TestResumeFlow_WithTracer_FailedResult(t *testing.T) {
	tr := &mockTracer{}
	store := NewMemoryStorage()
	ctx := t.Context()

	run := &Run{ID: "run-fail-2", Workflow: &Workflow{Name: "test"}, Status: spec.StatusRunning, Steps: map[string]*StepResult{}}
	if err := store.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	failLLM := &failingLLM{failOnCall: 1, okResponse: &provider.GenerateResult{Text: "ok"}}
	o := New(WithModel(failLLM), WithStorage(store), WithTracer(tr), WithDefaultModel("m"))
	wf := &Workflow{Name: "test", Steps: []Step{{ID: "s1", Instructions: "do it"}}}

	result, err := o.ResumeFlow(ctx, "run-fail-2", wf)
	if err != nil {
		t.Fatalf("ResumeFlow: %v", err)
	}
	if result.Status != spec.StatusFailed {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusFailed)
	}
	flowSpans := tr.spansByName("zenflow.flow")
	if len(flowSpans) != 1 {
		t.Fatalf("expected 1 flow span, got %d", len(flowSpans))
	}
	if flowSpans[0].err == nil {
		t.Error("span error should be set on failed status")
	}
}

// --- RunGoal with tracer ---

func TestRunGoal_WithTracer_CoordinatorFails(t *testing.T) {
	tr := &mockTracer{}
	// LLM returns non-JSON → coordinator parse error → RunGoal returns error.
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "not json"},
			{Text: "still not json"},
			{Text: "nope"},
			{Text: "give up"},
		},
	}
	o := New(WithModel(llm), WithTracer(tr), WithDefaultModel("m"))
	_, err := o.RunGoal(t.Context(), "do something")
	if err == nil {
		t.Fatal("expected error from coordinator")
	}
	goalSpans := tr.spansByName("zenflow.goal")
	if len(goalSpans) != 1 {
		t.Fatalf("expected 1 goal span, got %d", len(goalSpans))
	}
	if !goalSpans[0].ended {
		t.Error("goal span not ended")
	}
	if goalSpans[0].err == nil {
		t.Error("goal span error should be set on coordinator failure")
	}
}

// --- Executor.runStep with tracer ---

func TestRunStep_WithTracer(t *testing.T) {
	tr := &mockTracer{}
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "step done", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}
	var tools []goai.Tool

	wf := &Workflow{
		Name:  "test",
		Steps: []Step{{ID: "s1", Instructions: "do it", Agent: "worker"}},
		Agents: map[string]AgentConfig{
			"worker": {Model: "gpt-4o"},
		},
	}

	exec := &Executor{
		Runner:       &AgentRunner{model: llm, tools: tools},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Tracer:       tr,
	}

	sr := exec.runStep(t.Context(), "run-1", "s1", wf.Steps[0], 0, 1, nil)
	if sr.Status != spec.StepCompleted {
		t.Errorf("status = %q, want %q", sr.Status, spec.StepCompleted)
	}
	stepSpans := tr.spansByName("zenflow.step")
	if len(stepSpans) != 1 {
		t.Fatalf("expected 1 step span, got %d", len(stepSpans))
	}
	if !stepSpans[0].ended {
		t.Error("step span not ended")
	}
	if stepSpans[0].err != nil {
		t.Errorf("step span error = %v, want nil", stepSpans[0].err)
	}
	if stepSpans[0].attrs["zenflow.step.agent"] != "worker" {
		t.Errorf("step agent attr = %q, want %q", stepSpans[0].attrs["zenflow.step.agent"], "worker")
	}
}

func TestRunFlowWithID_TracerExecRunError(t *testing.T) {
	// Cover L220-222: exec.Run returns a hard error with tracer set.
	// Trigger by passing a workflow with a cycle so ValidateWorkflow returns an error.
	// (initial SaveRun now degrades gracefully instead of returning an
	// error, so we use a validation error as the trigger instead.)
	tr := &mockTracer{}
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "ok", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}
	o := New(WithModel(llm), WithTracer(tr), WithDefaultModel("m"))
	wf := &Workflow{
		Name: "cycle-flow",
		Steps: []Step{
			{ID: "s1", Instructions: "do it", DependsOn: []string{"s2"}},
			{ID: "s2", Instructions: "do it too", DependsOn: []string{"s1"}},
		},
	}

	_, err := o.runFlowWithID(t.Context(), wf, "run-err-1", runFlowConfig{})
	if err == nil {
		t.Fatal("expected error from cyclic workflow")
	}
	flowSpans := tr.spansByName("zenflow.flow")
	if len(flowSpans) != 1 {
		t.Fatalf("expected 1 flow span, got %d", len(flowSpans))
	}
	if !flowSpans[0].ended {
		t.Error("flow span not ended")
	}
	if flowSpans[0].err == nil {
		t.Error("flow span error should be set when exec.Run returns error")
	}
}

// TestRunFlow_InitialSaveRunGracefulDegrade verifies: when the
// initial SaveRun fails, RunFlow continues running the workflow instead
// of aborting. Storage is observability, not DAG correctness.
func TestRunFlow_InitialSaveRunGracefulDegrade(t *testing.T) {
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}
	// failInitSaveRunStorage uses a properly-initialized MemoryStorage for
	// step results and shared memory but rejects every SaveRun call.
	store := &failInitSaveRunStorage{MemoryStorage: *NewMemoryStorage()}
	o := New(WithModel(llm), WithDefaultModel("m"), WithStorage(store))
	wf := &Workflow{
		Name:  "degrade-flow",
		Steps: []Step{{ID: "s1", Instructions: "do it"}},
	}

	result, err := o.RunFlow(t.Context(), wf)
	if err != nil {
		t.Fatalf("RunFlow should not abort on storage error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// Workflow must complete even though storage was unavailable.
	if result.Status != spec.StatusCompleted {
		t.Errorf("Status = %q, want %q", result.Status, spec.StatusCompleted)
	}
}

// failInitSaveRunStorage wraps a properly-initialized MemoryStorage and
// rejects ALL SaveRun calls - both initial and final. Used to verify
// that graceful-degrade on storage error doesn't prevent the
// workflow from running to completion.
type failInitSaveRunStorage struct{ MemoryStorage }

func (f *failInitSaveRunStorage) SaveRun(_ context.Context, _ *Run) error {
	return errors.New("storage save failed")
}

func TestResumeFlow_WithTracer_ExecRunError(t *testing.T) {
	// Cover L310-312: exec.Run returns error with tracer set, via ResumeFlow.
	// SaveRun failures are now graceful; use a storage whose LoadRun
	// fails (ResumeFlow line 634 - "resume: load run") so the span is still
	// ended with an error and the tracer path is exercised.
	tr := &mockTracer{}
	store := &failLoadRunStorage{MemoryStorage: *NewMemoryStorage()}
	ctx := t.Context()

	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "ok", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}
	o := New(WithModel(llm), WithStorage(store), WithTracer(tr), WithDefaultModel("m"))
	wf := &Workflow{
		Name:  "test",
		Steps: []Step{{ID: "s1", Instructions: "do it"}},
	}

	_, err := o.ResumeFlow(ctx, "run-re-err", wf)
	if err == nil {
		t.Fatal("expected error from failing LoadRun")
	}
	flowSpans := tr.spansByName("zenflow.flow")
	if len(flowSpans) != 1 {
		t.Fatalf("expected 1 flow span, got %d", len(flowSpans))
	}
	if !flowSpans[0].ended {
		t.Error("flow span not ended")
	}
	if flowSpans[0].err == nil {
		t.Error("flow span error should be set when LoadRun returns error")
	}
}

// failLoadRunStorage always fails LoadRun, simulating a storage that
// can't retrieve a saved run. Used to trigger ResumeFlow error path.
type failLoadRunStorage struct{ MemoryStorage }

func (f *failLoadRunStorage) LoadRun(_ context.Context, _ string) (*Run, error) {
	return nil, errors.New("load run failed")
}

func TestRunGoal_GenerateRunIDError(t *testing.T) {
	// Cover L333-335: GenerateRunID fails in RunGoal.
	orig := GenerateRunID
	t.Cleanup(func() { GenerateRunID = orig })
	GenerateRunID = func() (string, error) { return "", fmt.Errorf("id gen fail") }

	o := New(WithModel(&mockModel{}), WithDefaultModel("m"))
	_, err := o.RunGoal(t.Context(), "do something")
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "id gen fail" {
		t.Errorf("error = %q, want 'id gen fail'", err)
	}
}

// TestRunGoal_EmptyGoal verifies: RunGoal returns ErrEmptyGoal
// for empty and whitespace-only goal strings.
func TestRunGoal_EmptyGoal(t *testing.T) {
	o := New(WithModel(&mockModel{}), WithDefaultModel("m"))
	cases := []string{"", "   ", "\t", "\n", "  \t  \n  "}
	for _, goal := range cases {
		_, err := o.RunGoal(t.Context(), goal)
		if err == nil {
			t.Fatalf("RunGoal(%q): expected ErrEmptyGoal, got nil", goal)
		}
		if !errors.Is(err, ErrEmptyGoal) {
			t.Errorf("RunGoal(%q): err = %v, want errors.Is(ErrEmptyGoal)", goal, err)
		}
	}
}

func TestRunStep_WithTracer_Error(t *testing.T) {
	tr := &mockTracer{}
	failLLM := &failingLLM{failOnCall: 1, okResponse: &provider.GenerateResult{Text: "ok"}}

	wf := &Workflow{
		Name:  "test",
		Steps: []Step{{ID: "s1", Instructions: "do it"}},
	}

	exec := &Executor{
		Runner:       &AgentRunner{model: failLLM},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Tracer:       tr,
	}

	sr := exec.runStep(t.Context(), "run-1", "s1", wf.Steps[0], 0, 1, nil)
	if sr.Status != spec.StepFailed {
		t.Errorf("status = %q, want %q", sr.Status, spec.StepFailed)
	}
	stepSpans := tr.spansByName("zenflow.step")
	if len(stepSpans) != 1 {
		t.Fatalf("expected 1 step span, got %d", len(stepSpans))
	}
	if !stepSpans[0].ended {
		t.Error("step span not ended")
	}
	if stepSpans[0].err == nil {
		t.Error("step span error should be set on LLM failure")
	}
}

// TestResumeFlow_WithTracer_ExecRunError_Cyclic covers zenflow.go:701-703:
// tracer is set AND exec.Run returns a hard error (cyclic workflow fails
// ValidateWorkflow inside exec.Run after LoadRun and LoadSharedMemory succeed).
func TestResumeFlow_WithTracer_ExecRunError_Cyclic(t *testing.T) {
	tr := &mockTracer{}
	store := NewMemoryStorage()
	ctx := t.Context()

	// Save a run so LoadRun succeeds.
	run := &Run{ID: "run-cyclic", Workflow: &Workflow{Name: "cycle"}, Status: spec.StatusRunning, Steps: map[string]*StepResult{}}
	if err := store.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	o := New(WithModel(&mockModel{}), WithStorage(store), WithTracer(tr), WithDefaultModel("m"))
	// Cyclic workflow: s1 depends on s2, s2 depends on s1 → TopoSort fails.
	wf := &Workflow{
		Name: "cycle",
		Steps: []Step{
			{ID: "s1", Instructions: "first", DependsOn: []string{"s2"}},
			{ID: "s2", Instructions: "second", DependsOn: []string{"s1"}},
		},
	}

	_, err := o.ResumeFlow(ctx, "run-cyclic", wf)
	if err == nil {
		t.Fatal("expected error from cyclic workflow validation")
	}
	flowSpans := tr.spansByName("zenflow.flow")
	if len(flowSpans) != 1 {
		t.Fatalf("expected 1 flow span, got %d", len(flowSpans))
	}
	if !flowSpans[0].ended {
		t.Error("flow span not ended")
	}
	if flowSpans[0].err == nil {
		t.Error("flow span error should be set when exec.Run returns error")
	}
}

// TestRunGoal_RunFlowWithIDError covers zenflow.go:887-889: runFlowWithID
// returns an error after the coordinator successfully parses a workflow.
func TestRunGoal_RunFlowWithIDError(t *testing.T) {
	orig := runFlowFn
	t.Cleanup(func() { runFlowFn = orig })
	runFlowFn = func(_ *Orchestrator, _ context.Context, _ *Workflow, _ string, _ runFlowConfig) (*WorkflowResult, error) {
		return nil, errors.New("simulated runFlowWithID failure")
	}

	// Coordinator must succeed: return a minimal valid workflow JSON.
	validWF := `{"name":"goal-flow","steps":[{"id":"s1","instructions":"do it"}]}`
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: validWF, FinishReason: provider.FinishStop},
		},
	}
	o := New(WithModel(llm), WithDefaultModel("m"))
	_, err := o.RunGoal(t.Context(), "do something")
	if err == nil {
		t.Fatal("expected error from runFlowWithID")
	}
	if err.Error() != "simulated runFlowWithID failure" {
		t.Errorf("err = %q, want 'simulated runFlowWithID failure'", err.Error())
	}
}

// planReadyRecorder is a minimal ProgressSink that captures every event
// it sees so tests can assert specific event types were emitted.
type planReadyRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *planReadyRecorder) OnEvent(_ context.Context, e Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *planReadyRecorder) OnOutput(_ context.Context, _ Output) {}

// TestRunFlow_EmitsPlanReadyBeforeExecute verifies that Orchestrator.RunFlow
// emits an EventPlanReady event with the workflow attached in Data["workflow"]
// before delegating to runFlowWithID. This is required by ux.md §9.B so the
// CLI --plan flag and TUI DAG card render for every direct /flow run, not
// just /goal-driven runs. / A.8.
func TestRunFlow_EmitsPlanReadyBeforeExecute(t *testing.T) {
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "design done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
			{Text: "impl done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
			{Text: "review done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}
	rec := &planReadyRecorder{}
	zf := New(
		WithModel(llm),
		WithProgress(rec),
		WithDefaultModel("gpt-4o"),
	)
	wf, err := ParseWorkflow([]byte(testWorkflowYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := zf.RunFlow(t.Context(), wf); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	var found *Event
	for i := range rec.events {
		if rec.events[i].Type == types.EventPlanReady {
			found = &rec.events[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected at least one EventPlanReady; got %d events (no plan_ready)", len(rec.events))
	}
	got, ok := found.Data["workflow"]
	if !ok {
		t.Fatalf("EventPlanReady missing Data[\"workflow\"]; data=%+v", found.Data)
	}
	gotWf, ok := got.(*Workflow)
	if !ok {
		t.Fatalf("EventPlanReady Data[\"workflow\"] type=%T, want *Workflow", got)
	}
	if gotWf != wf {
		t.Fatalf("EventPlanReady workflow does not match input: got=%p want=%p", gotWf, wf)
	}
}

// TestRunAgent_StreamingVerbose exercises the WithRunnerStreaming and
// WithRunnerVerbose option paths inside Orchestrator.RunAgent (covers
// the if-branches added during the WithRunner* migration).
func TestRunAgent_StreamingVerbose(t *testing.T) {
	llm := &streamingCoordinatorLLM{
		responses: []string{"ok"},
	}
	o := New(
		WithModel(llm),
		WithDefaultModel("test"),
		WithStreaming(),
		WithVerbose(),
	)
	if _, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "hi"}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
}

// TestRunFlow_StreamingVerbose exercises the WithRunnerStreaming and
// WithRunnerVerbose option paths inside Orchestrator.runFlowWithID.
func TestRunFlow_StreamingVerbose(t *testing.T) {
	llm := &streamingCoordinatorLLM{
		responses: []string{"step-1 done"},
	}
	o := New(
		WithModel(llm),
		WithDefaultModel("test"),
		WithStreaming(),
		WithVerbose(),
	)
	wf := &Workflow{
		Name:  "streaming-flow",
		Steps: []Step{{ID: "s1", Instructions: "do it"}},
	}
	if _, err := o.RunFlow(t.Context(), wf); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
}

// TestResumeFlow_StreamingVerbose exercises the WithRunnerStreaming and
// WithRunnerVerbose option paths inside Orchestrator.ResumeFlow.
func TestResumeFlow_StreamingVerbose(t *testing.T) {
	store := NewMemoryStorage()
	ctx := t.Context()
	wf := &Workflow{
		Name:  "resume-streaming",
		Steps: []Step{{ID: "s1", Instructions: "do it"}},
	}
	if err := store.SaveRun(ctx, &Run{
		ID:       "run-sv",
		Workflow: wf,
		Status:   spec.StatusRunning,
		Steps:    map[string]*StepResult{},
	}); err != nil {
		t.Fatal(err)
	}
	llm := &streamingCoordinatorLLM{
		responses: []string{"s1 done"},
	}
	o := New(
		WithModel(llm),
		WithStorage(store),
		WithDefaultModel("test"),
		WithStreaming(),
		WithVerbose(),
	)
	if _, err := o.ResumeFlow(ctx, "run-sv", wf); err != nil {
		t.Fatalf("ResumeFlow: %v", err)
	}
}
