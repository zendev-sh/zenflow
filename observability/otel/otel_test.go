package otel

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/zendev-sh/zenflow"

	"github.com/zendev-sh/goai/provider"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func newTestTP(t *testing.T) (*sdktrace.TracerProvider, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { tp.Shutdown(t.Context()) })
	return tp, exporter
}

// --- Tracer unit tests ---

func TestOtelTracer_StartEndSpan(t *testing.T) {
	tp, exporter := newTestTP(t)
	tracer := &otelTracer{tracer: tp.Tracer(tracerName)}

	ctx := tracer.StartSpan(t.Context(), "zenflow.flow", map[string]string{
		"zenflow.run_id":        "run-123",
		"zenflow.workflow.name": "test-wf",
	})
	tracer.EndSpan(ctx, nil)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name != "zenflow.flow" {
		t.Errorf("span name = %q, want zenflow.flow", spans[0].Name)
	}

	found := map[string]string{}
	for _, attr := range spans[0].Attributes {
		found[string(attr.Key)] = attr.Value.AsString()
	}
	if found["zenflow.run_id"] != "run-123" {
		t.Errorf("run_id = %q", found["zenflow.run_id"])
	}
	if found["zenflow.workflow.name"] != "test-wf" {
		t.Errorf("workflow.name = %q", found["zenflow.workflow.name"])
	}
}

func TestOtelTracer_SpanSuccess_SetsOk(t *testing.T) {
	tp, exporter := newTestTP(t)
	tracer := &otelTracer{tracer: tp.Tracer(tracerName)}

	ctx := tracer.StartSpan(t.Context(), "zenflow.flow", nil)
	tracer.EndSpan(ctx, nil)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Status.Code != codes.Ok {
		t.Errorf("success span status = %v, want Ok", spans[0].Status.Code)
	}
}

func TestOtelTracer_SpanWithError(t *testing.T) {
	tp, exporter := newTestTP(t)
	tracer := &otelTracer{tracer: tp.Tracer(tracerName)}

	ctx := tracer.StartSpan(t.Context(), "zenflow.step", map[string]string{"zenflow.step.id": "fail"})
	tracer.EndSpan(ctx, errors.New("step failed"))

	spans := exporter.GetSpans()
	if spans[0].Status.Code != codes.Error {
		t.Errorf("status = %v, want Error", spans[0].Status.Code)
	}
	if spans[0].Status.Description != "step failed" {
		t.Errorf("status desc = %q", spans[0].Status.Description)
	}
}

func TestOtelTracer_NestedSpans(t *testing.T) {
	tp, exporter := newTestTP(t)
	tracer := &otelTracer{tracer: tp.Tracer(tracerName)}

	flowCtx := tracer.StartSpan(t.Context(), "zenflow.flow", map[string]string{"zenflow.workflow.name": "wf"})
	stepCtx := tracer.StartSpan(flowCtx, "zenflow.step", map[string]string{"zenflow.step.id": "s1"})
	tracer.EndSpan(stepCtx, nil)
	tracer.EndSpan(flowCtx, nil)

	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}

	stepSpan := spans[0] // ended first
	flowSpan := spans[1]

	if stepSpan.Parent.SpanID() != flowSpan.SpanContext.SpanID() {
		t.Error("step span is not a child of flow span")
	}
}

func TestOtelTracer_EmptyAttrs(t *testing.T) {
	tp, exporter := newTestTP(t)
	tracer := &otelTracer{tracer: tp.Tracer(tracerName)}

	ctx := tracer.StartSpan(t.Context(), "zenflow.agent", nil)
	tracer.EndSpan(ctx, nil)

	if len(exporter.GetSpans()) != 1 {
		t.Fatal("expected 1 span")
	}
}

// --- Integration tests with Orchestrator ---

func TestRunFlow_WithTracing(t *testing.T) {
	tp, exporter := newTestTP(t)

	orch := zenflow.New(
		zenflow.WithModel(&mockLLM{}),
		zenflow.WithDefaultModel("test"),
		WithTracing(WithTracerProvider(tp)),
	)

	wf := &zenflow.Workflow{
		Name: "traced-wf",
		Steps: []zenflow.Step{
			{ID: "s1", Instructions: "hello"},
			{ID: "s2", DependsOn: []string{"s1"}, Instructions: "world"},
		},
	}

	result, err := orch.RunFlow(t.Context(), wf)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != zenflow.StatusCompleted {
		t.Errorf("status = %q", result.Status)
	}

	names := map[string]int{}
	for _, s := range exporter.GetSpans() {
		names[s.Name]++
	}
	if names["zenflow.flow"] != 1 {
		t.Errorf("flow spans = %d, want 1", names["zenflow.flow"])
	}
	if names["zenflow.step"] != 2 {
		t.Errorf("step spans = %d, want 2", names["zenflow.step"])
	}
}

func TestRunFlow_FailingStep_ErrorSpan(t *testing.T) {
	tp, exporter := newTestTP(t)

	orch := zenflow.New(
		zenflow.WithModel(&failingLLM{}),
		zenflow.WithDefaultModel("test"),
		WithTracing(WithTracerProvider(tp)),
	)

	wf := &zenflow.Workflow{
		Name:  "fail-wf",
		Steps: []zenflow.Step{{ID: "s1", Instructions: "fail"}},
	}

	result, err := orch.RunFlow(t.Context(), wf)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != zenflow.StatusFailed {
		t.Errorf("status = %q, want failed", result.Status)
	}

	for _, s := range exporter.GetSpans() {
		if s.Name == "zenflow.flow" && s.Status.Code != codes.Error {
			t.Errorf("flow span status = %v, want Error", s.Status.Code)
		}
		if s.Name == "zenflow.step" && s.Status.Code != codes.Error {
			t.Errorf("step span status = %v, want Error", s.Status.Code)
		}
	}
}

func TestRunAgent_WithTracing(t *testing.T) {
	tp, exporter := newTestTP(t)

	orch := zenflow.New(
		zenflow.WithModel(&mockLLM{}),
		zenflow.WithDefaultModel("test"),
		WithTracing(WithTracerProvider(tp)),
	)

	result, err := orch.RunAgent(t.Context(), zenflow.AgentConfig{Prompt: "say hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != zenflow.AgentStatusCompleted {
		t.Errorf("status = %q", result.Status)
	}

	names := map[string]int{}
	for _, s := range exporter.GetSpans() {
		names[s.Name]++
	}
	if names["zenflow.agent"] != 1 {
		t.Errorf("agent spans = %d, want 1", names["zenflow.agent"])
	}
}

func TestRunAgent_Failing_ErrorSpan(t *testing.T) {
	tp, exporter := newTestTP(t)

	orch := zenflow.New(
		zenflow.WithModel(&failingLLM{}),
		zenflow.WithDefaultModel("test"),
		WithTracing(WithTracerProvider(tp)),
	)

	_, err := orch.RunAgent(t.Context(), zenflow.AgentConfig{Prompt: "fail"})
	if err == nil {
		t.Fatal("expected error")
	}

	for _, s := range exporter.GetSpans() {
		if s.Name == "zenflow.agent" && s.Status.Code != codes.Error {
			t.Errorf("agent span status = %v, want Error", s.Status.Code)
		}
	}
}

func TestRunGoal_WithTracing(t *testing.T) {
	tp, exporter := newTestTP(t)

	// coordinatorLLM returns valid JSON workflow on first call, then "ok" for step execution.
	llm := &coordinatorLLM{}
	orch := zenflow.New(
		zenflow.WithModel(llm),
		zenflow.WithDefaultModel("test"),
		WithTracing(WithTracerProvider(tp)),
	)

	result, err := orch.RunGoal(t.Context(), "create a 1-step workflow")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != zenflow.StatusCompleted {
		t.Errorf("status = %q", result.Status)
	}

	names := map[string]int{}
	for _, s := range exporter.GetSpans() {
		names[s.Name]++
	}
	if names["zenflow.goal"] != 1 {
		t.Errorf("goal spans = %d, want 1", names["zenflow.goal"])
	}
	if names["zenflow.flow"] != 1 {
		t.Errorf("flow spans = %d, want 1", names["zenflow.flow"])
	}
	if names["zenflow.step"] < 1 {
		t.Errorf("step spans = %d, want >= 1", names["zenflow.step"])
	}

	// Verify goal and flow spans share the same run_id.
	runIDs := map[string]string{} // span name -> run_id
	for _, s := range exporter.GetSpans() {
		for _, attr := range s.Attributes {
			if string(attr.Key) == "zenflow.run_id" {
				runIDs[s.Name] = attr.Value.AsString()
			}
		}
	}
	if runIDs["zenflow.goal"] != runIDs["zenflow.flow"] {
		t.Errorf("goal run_id=%q != flow run_id=%q", runIDs["zenflow.goal"], runIDs["zenflow.flow"])
	}
}

func TestResumeFlow_WithTracing(t *testing.T) {
	tp, exporter := newTestTP(t)

	store := zenflow.NewMemoryStorage()
	runID := "run-trace-resume"
	run := &zenflow.Run{ID: runID, Workflow: &zenflow.Workflow{Name: "wf"}, Status: zenflow.StatusRunning, Steps: map[string]*zenflow.StepResult{}}
	if err := store.SaveRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}

	orch := zenflow.New(
		zenflow.WithModel(&mockLLM{}),
		zenflow.WithStorage(store),
		zenflow.WithDefaultModel("test"),
		WithTracing(WithTracerProvider(tp)),
	)

	wf := &zenflow.Workflow{
		Name:  "resume-wf",
		Steps: []zenflow.Step{{ID: "s1", Instructions: "hello"}},
	}

	result, err := orch.ResumeFlow(t.Context(), runID, wf)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != zenflow.StatusCompleted {
		t.Errorf("status = %q", result.Status)
	}

	names := map[string]int{}
	var resumeAttr string
	for _, s := range exporter.GetSpans() {
		names[s.Name]++
		if s.Name == "zenflow.flow" {
			for _, attr := range s.Attributes {
				if string(attr.Key) == "zenflow.resume" {
					resumeAttr = attr.Value.AsString()
				}
			}
		}
	}
	if names["zenflow.flow"] != 1 {
		t.Errorf("flow spans = %d, want 1", names["zenflow.flow"])
	}
	if resumeAttr != "true" {
		t.Errorf("resume attr = %q, want 'true'", resumeAttr)
	}
}

func TestGoAIOption_ReturnsNonNil(t *testing.T) {
	if GoAIOption() == nil {
		t.Fatal("nil")
	}
}

func TestGoAIOption_WithProvider(t *testing.T) {
	tp, _ := newTestTP(t)
	if GoAIOption(WithTracerProvider(tp)) == nil {
		t.Fatal("nil")
	}
}

// --- mocks ---

type mockLLM struct{}

func (m *mockLLM) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	return &provider.GenerateResult{Text: "ok", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}, FinishReason: provider.FinishStop}, nil
}

type failingLLM struct{}

func (m *failingLLM) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	return nil, errors.New("llm error")
}

// coordinatorLLM returns valid JSON workflow on first call, then "ok" for steps.
type coordinatorLLM struct {
	mu    sync.Mutex
	calls int
}

func (m *coordinatorLLM) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 1 {
 // Coordinator response: valid JSON workflow.
		return &provider.GenerateResult{
			Text: `{
				"name": "goal-workflow",
				"version": 1,
				"agents": {"worker": {"description": "worker agent"}},
				"steps": [{"id": "s1", "agent": "worker", "instructions": "do it"}]
			}`,
			Usage:        provider.Usage{InputTokens: 50, OutputTokens: 100},
			FinishReason: provider.FinishStop,
		}, nil
	}
	// Step execution response.
	return &provider.GenerateResult{Text: "done", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}, FinishReason: provider.FinishStop}, nil
}

func (m *mockLLM) ModelID() string { return "mock" }
func (m *mockLLM) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, nil
}
func (m *failingLLM) ModelID() string { return "failing-mock" }
func (m *failingLLM) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, nil
}
func (m *coordinatorLLM) ModelID() string { return "coordinator-mock" }
func (m *coordinatorLLM) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, nil
}
