//go:build !e2e

package exec

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/zenflow/internal/spec"
	"github.com/zendev-sh/zenflow/internal/types"
)

func newToolExecutor(tools []goai.Tool, wf *Workflow) *Executor {
	return &Executor{
		Runner:       &AgentRunner{model: &mockModel{}, tools: tools},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Tools:        tools,
	}
}

func TestRunToolStep_Success(t *testing.T) {
	tool := makeTool("mytool", "does stuff", "result")
	wf := newTestWorkflow([]Step{{ID: "s1", Tool: "mytool", ToolInput: map[string]any{"key": "value"}}}, nil)
	exec := newToolExecutor([]goai.Tool{tool}, wf)

	sr := exec.runToolStep(t.Context(), "run1", "s1", wf.Steps[0], map[string]*StepResult{}, 0, 1)
	if sr.Status != spec.StepCompleted {
		t.Errorf("status = %q, want %q (error: %v)", sr.Status, spec.StepCompleted, sr.Error)
	}
	if sr.Content != "result" {
		t.Errorf("content = %q, want %q", sr.Content, "result")
	}
}

func TestRunToolStep_ToolNotFound(t *testing.T) {
	wf := newTestWorkflow([]Step{{ID: "s1", Tool: "missing"}}, nil)
	exec := newToolExecutor(nil, wf)

	sr := exec.runToolStep(t.Context(), "run1", "s1", wf.Steps[0], map[string]*StepResult{}, 0, 1)
	if sr.Status != spec.StepFailed {
		t.Errorf("status = %q, want %q", sr.Status, spec.StepFailed)
	}
	if sr.Error == nil {
		t.Fatal("expected non-nil error")
	}
}

func TestRunToolStep_ToolExecuteError(t *testing.T) {
	tool := makeErrorTool("badtool", "always fails")
	wf := newTestWorkflow([]Step{{ID: "s1", Tool: "badtool"}}, nil)
	exec := newToolExecutor([]goai.Tool{tool}, wf)

	sr := exec.runToolStep(t.Context(), "run1", "s1", wf.Steps[0], map[string]*StepResult{}, 0, 1)
	if sr.Status != spec.StepFailed {
		t.Errorf("status = %q, want %q", sr.Status, spec.StepFailed)
	}
	if sr.Error == nil {
		t.Fatal("expected non-nil error")
	}
}

func TestRunToolStep_CELInputExpansion(t *testing.T) {
	var receivedInput json.RawMessage
	tool := goai.Tool{
		Name:        "mytool",
		Description: "captures input",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(_ context.Context, input json.RawMessage) (string, error) {
			receivedInput = input
			return "ok", nil
		},
	}
	wf := newTestWorkflow([]Step{
		{ID: "s1", Tool: "mytool", ToolInput: map[string]any{"msg": "$steps.prev.content"}},
	}, nil)
	exec := newToolExecutor([]goai.Tool{tool}, wf)

	depResults := map[string]*StepResult{
		"prev": {ID: "prev", Status: spec.StepCompleted, Content: "expanded"},
	}
	sr := exec.runToolStep(t.Context(), "run1", "s1", wf.Steps[0], depResults, 0, 1)
	if sr.Status != spec.StepCompleted {
		t.Fatalf("status = %q, want %q (error: %v)", sr.Status, spec.StepCompleted, sr.Error)
	}

	var got map[string]any
	if err := json.Unmarshal(receivedInput, &got); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	if got["msg"] != "expanded" {
		t.Errorf("msg = %q, want %q", got["msg"], "expanded")
	}
}

func TestRunToolStep_CELInputLiteralPassthrough(t *testing.T) {
	var receivedInput json.RawMessage
	tool := goai.Tool{
		Name:        "mytool",
		Description: "captures input",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(_ context.Context, input json.RawMessage) (string, error) {
			receivedInput = input
			return "ok", nil
		},
	}
	wf := newTestWorkflow([]Step{
		{ID: "s1", Tool: "mytool", ToolInput: map[string]any{"msg": "plain value"}},
	}, nil)
	exec := newToolExecutor([]goai.Tool{tool}, wf)

	sr := exec.runToolStep(t.Context(), "run1", "s1", wf.Steps[0], map[string]*StepResult{}, 0, 1)
	if sr.Status != spec.StepCompleted {
		t.Fatalf("status = %q, want %q (error: %v)", sr.Status, spec.StepCompleted, sr.Error)
	}

	var got map[string]any
	if err := json.Unmarshal(receivedInput, &got); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	if got["msg"] != "plain value" {
		t.Errorf("msg = %q, want %q", got["msg"], "plain value")
	}
}

// TestRunToolStep_DollarEscape verifies that a value starting with "$$" is
// treated as a literal: the leading "$$" collapses to "$" and the remainder
// is passed through verbatim with no CEL evaluation.
func TestRunToolStep_DollarEscape(t *testing.T) {
	var receivedInput json.RawMessage
	tool := goai.Tool{
		Name:        "mytool",
		Description: "captures input",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(_ context.Context, input json.RawMessage) (string, error) {
			receivedInput = input
			return "ok", nil
		},
	}
	// "$$HOME" must NOT be evaluated as CEL (which would fail); it must
	// collapse to the literal "$HOME".
	wf := newTestWorkflow([]Step{
		{ID: "s1", Tool: "mytool", ToolInput: map[string]any{"path": "$$HOME"}},
	}, nil)
	exec := newToolExecutor([]goai.Tool{tool}, wf)

	sr := exec.runToolStep(t.Context(), "run1", "s1", wf.Steps[0], map[string]*StepResult{}, 0, 1)
	if sr.Status != spec.StepCompleted {
		t.Fatalf("status = %q, want %q (error: %v)", sr.Status, spec.StepCompleted, sr.Error)
	}

	var got map[string]any
	if err := json.Unmarshal(receivedInput, &got); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	if got["path"] != "$HOME" {
		t.Errorf("path = %q, want %q", got["path"], "$HOME")
	}
}

func TestRunToolStep_WithProgress_Success(t *testing.T) {
	var events []Event
	sink := &mockProgressSink{
		onEvent: func(_ context.Context, e Event) {
			events = append(events, e)
		},
	}
	tool := makeTool("mytool", "does stuff", "result")
	wf := newTestWorkflow([]Step{{ID: "s1", Tool: "mytool", ToolInput: map[string]any{"key": "value"}}}, nil)
	ex := newToolExecutor([]goai.Tool{tool}, wf)
	ex.Progress = sink

	sr := ex.runToolStep(t.Context(), "run1", "s1", wf.Steps[0], map[string]*StepResult{}, 0, 1)
	if sr.Status != spec.StepCompleted {
		t.Errorf("status = %q, want %q (error: %v)", sr.Status, spec.StepCompleted, sr.Error)
	}

	var hasStart, hasEnd bool
	for _, e := range events {
		if e.Type == types.EventStepStart && e.StepID == "s1" {
			hasStart = true
		}
		if e.Type == types.EventStepEnd && e.StepID == "s1" {
			hasEnd = true
		}
	}
	if !hasStart {
		t.Error("expected EventStepStart for s1")
	}
	if !hasEnd {
		t.Error("expected EventStepEnd for s1")
	}
}

func TestRunToolStep_WithProgress_ToolNotFound(t *testing.T) {
	var events []Event
	sink := &mockProgressSink{
		onEvent: func(_ context.Context, e Event) {
			events = append(events, e)
		},
	}
	wf := newTestWorkflow([]Step{{ID: "s1", Tool: "missing"}}, nil)
	ex := newToolExecutor(nil, wf)
	ex.Progress = sink

	sr := ex.runToolStep(t.Context(), "run1", "s1", wf.Steps[0], map[string]*StepResult{}, 0, 1)
	if sr.Status != spec.StepFailed {
		t.Errorf("status = %q, want %q", sr.Status, spec.StepFailed)
	}

	var hasErrorEvent bool
	for _, e := range events {
		if e.Type == types.EventError && e.StepID == "s1" {
			hasErrorEvent = true
		}
	}
	if !hasErrorEvent {
		t.Error("expected EventError for tool-not-found")
	}
}

func TestRunToolStep_WithProgress_CELError(t *testing.T) {
	var events []Event
	sink := &mockProgressSink{
		onEvent: func(_ context.Context, e Event) {
			events = append(events, e)
		},
	}
	// Use a bad CEL expression that refers to undefined variable in a way that fails.
	tool := makeTool("mytool", "does stuff", "result")
	wf := newTestWorkflow([]Step{
		{ID: "s1", Tool: "mytool", ToolInput: map[string]any{"msg": "$!!!invalid_cel"}},
	}, nil)
	ex := newToolExecutor([]goai.Tool{tool}, wf)
	ex.Progress = sink

	sr := ex.runToolStep(t.Context(), "run1", "s1", wf.Steps[0], map[string]*StepResult{}, 0, 1)
	if sr.Status != spec.StepFailed {
		t.Errorf("status = %q, want %q", sr.Status, spec.StepFailed)
	}

	var hasErrorEvent bool
	for _, e := range events {
		if e.Type == types.EventError && e.StepID == "s1" {
			hasErrorEvent = true
		}
	}
	if !hasErrorEvent {
		t.Error("expected EventError for CEL error")
	}
}

func TestRunToolStep_WithProgress_ExecuteError(t *testing.T) {
	var events []Event
	sink := &mockProgressSink{
		onEvent: func(_ context.Context, e Event) {
			events = append(events, e)
		},
	}
	tool := makeErrorTool("badtool", "always fails")
	wf := newTestWorkflow([]Step{{ID: "s1", Tool: "badtool"}}, nil)
	ex := newToolExecutor([]goai.Tool{tool}, wf)
	ex.Progress = sink

	sr := ex.runToolStep(t.Context(), "run1", "s1", wf.Steps[0], map[string]*StepResult{}, 0, 1)
	if sr.Status != spec.StepFailed {
		t.Errorf("status = %q, want %q", sr.Status, spec.StepFailed)
	}

	var hasErrorEvent bool
	for _, e := range events {
		if e.Type == types.EventError && e.StepID == "s1" {
			hasErrorEvent = true
		}
	}
	if !hasErrorEvent {
		t.Error("expected EventError for execute error")
	}
}

func TestRunToolStep_WorkflowDefaultTimeout(t *testing.T) {
	const shortTimeout = 50 * time.Millisecond
	tool := goai.Tool{
		Name:        "slowtool",
		Description: "blocks until ctx done",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, _ json.RawMessage) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}
	wf := &Workflow{
		Name: "test",
		Steps: []Step{
			{ID: "s1", Tool: "slowtool"},
		},
		Options: WorkflowOptions{StepTimeout: Duration(shortTimeout)},
	}
	ex := newToolExecutor([]goai.Tool{tool}, wf)

	sr := ex.runToolStep(t.Context(), "run1", "s1", wf.Steps[0], map[string]*StepResult{}, 0, 1)
	if sr.Status != spec.StepFailed {
		t.Errorf("status = %q, want %q", sr.Status, spec.StepFailed)
	}
	if !errors.Is(sr.Error, context.DeadlineExceeded) {
		t.Errorf("error = %v, want DeadlineExceeded", sr.Error)
	}
}

// TestExecutor_Run_ToolStep is an integration test that exercises the
// case step.Tool != "" dispatch branch in executor.go via Executor.Run().
func TestExecutor_Run_ToolStep(t *testing.T) {
	tool := makeTool("greet", "greets", "hello from tool")
	wf := newTestWorkflow([]Step{{ID: "s1", Tool: "greet", ToolInput: map[string]any{"name": "world"}}}, nil)
	ex := &Executor{
		Runner:       &AgentRunner{model: &mockModel{}, tools: []goai.Tool{tool}},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Tools:        []goai.Tool{tool},
	}

	result, err := ex.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
	sr, ok := result.Steps["s1"]
	if !ok {
		t.Fatal("step s1 missing from results")
	}
	if sr.Status != spec.StepCompleted {
		t.Errorf("s1.Status = %q, want %q", sr.Status, spec.StepCompleted)
	}
	if sr.Content != "hello from tool" {
		t.Errorf("s1.Content = %q, want %q", sr.Content, "hello from tool")
	}
}

// TestExecutor_Run_ToolStep_WithProgress exercises EventStepStart/EventStepEnd
// emission through the full Executor.Run path for a tool step.
func TestExecutor_Run_ToolStep_WithProgress(t *testing.T) {
	var events []Event
	sink := &mockProgressSink{
		onEvent: func(_ context.Context, e Event) {
			events = append(events, e)
		},
	}
	tool := makeTool("greet", "greets", "hello")
	wf := newTestWorkflow([]Step{{ID: "s1", Tool: "greet"}}, nil)
	ex := &Executor{
		Runner:       &AgentRunner{model: &mockModel{}, tools: []goai.Tool{tool}},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Tools:        []goai.Tool{tool},
		Progress:     sink,
	}

	result, err := ex.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}

	var hasStart, hasEnd bool
	for _, e := range events {
		if e.Type == types.EventStepStart && e.StepID == "s1" {
			hasStart = true
		}
		if e.Type == types.EventStepEnd && e.StepID == "s1" {
			hasEnd = true
		}
	}
	if !hasStart {
		t.Error("expected EventStepStart for tool step s1")
	}
	if !hasEnd {
		t.Error("expected EventStepEnd for tool step s1")
	}
}

// TestExecutor_Run_ToolStep_Retry exercises the retry loop in executor.go
// for a tool step that fails then succeeds.
func TestExecutor_Run_ToolStep_Retry(t *testing.T) {
	callCount := 0
	tool := goai.Tool{
		Name:        "flaky",
		Description: "fails first call then succeeds",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			callCount++
			if callCount == 1 {
				return "", errors.New("transient failure")
			}
			return "success on retry", nil
		},
	}
	wf := newTestWorkflow([]Step{{ID: "s1", Tool: "flaky", Retries: 1}}, nil)
	ex := &Executor{
		Runner:       &AgentRunner{model: &mockModel{}, tools: []goai.Tool{tool}},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Tools:        []goai.Tool{tool},
	}

	result, err := ex.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
	sr := result.Steps["s1"]
	if sr.Content != "success on retry" {
		t.Errorf("content = %q, want %q", sr.Content, "success on retry")
	}
	if callCount != 2 {
		t.Errorf("tool called %d times, want 2", callCount)
	}
}

// TestExecutor_Run_ToolStep_FailedStatus exercises the tool step failure path
// through Executor.Run (strategy tracking, result aggregation).
func TestExecutor_Run_ToolStep_Failed(t *testing.T) {
	tool := makeErrorTool("badtool", "always fails")
	wf := newTestWorkflow([]Step{{ID: "s1", Tool: "badtool"}}, nil)
	ex := &Executor{
		Runner:       &AgentRunner{model: &mockModel{}, tools: []goai.Tool{tool}},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Tools:        []goai.Tool{tool},
	}

	result, err := ex.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusFailed {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusFailed)
	}
	sr := result.Steps["s1"]
	if sr.Status != spec.StepFailed {
		t.Errorf("s1.Status = %q, want %q", sr.Status, spec.StepFailed)
	}
}

func TestRunToolStep_Timeout(t *testing.T) {
	const shortTimeout = 50 * time.Millisecond
	tool := goai.Tool{
		Name:        "slowtool",
		Description: "blocks until ctx done",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, _ json.RawMessage) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}
	wf := newTestWorkflow([]Step{
		{ID: "s1", Tool: "slowtool", Timeout: spec.Duration(shortTimeout)},
	}, nil)
	exec := newToolExecutor([]goai.Tool{tool}, wf)

	sr := exec.runToolStep(t.Context(), "run1", "s1", wf.Steps[0], map[string]*StepResult{}, 0, 1)
	if sr.Status != spec.StepFailed {
		t.Errorf("status = %q, want %q", sr.Status, spec.StepFailed)
	}
	if !errors.Is(sr.Error, context.DeadlineExceeded) {
		t.Errorf("error = %v, want DeadlineExceeded", sr.Error)
	}
}

// TestRunToolStep_JSONMarshalError covers the json.Marshal failure path.
// A channel value is not JSON-serialisable, which forces the marshal error branch.
func TestRunToolStep_JSONMarshalError(t *testing.T) {
	tool := makeTool("mytool", "does stuff", "result")
	wf := newTestWorkflow([]Step{
		{ID: "s1", Tool: "mytool", ToolInput: map[string]any{"ch": make(chan int)}},
	}, nil)
	exec := newToolExecutor([]goai.Tool{tool}, wf)

	sr := exec.runToolStep(t.Context(), "run1", "s1", wf.Steps[0], map[string]*StepResult{}, 0, 1)
	if sr.Status != spec.StepFailed {
		t.Errorf("status = %q, want %q", sr.Status, spec.StepFailed)
	}
	if sr.Error == nil {
		t.Fatal("expected non-nil error from marshal failure")
	}
	if !strings.Contains(sr.Error.Error(), "marshal tool input") {
		t.Errorf("error should mention 'marshal tool input', got: %v", sr.Error)
	}
}

// TestRunToolStep_JSONMarshalError_WithProgress covers the same marshal path with Progress set.
func TestRunToolStep_JSONMarshalError_WithProgress(t *testing.T) {
	var events []Event
	sink := &mockProgressSink{
		onEvent: func(_ context.Context, e Event) { events = append(events, e) },
	}
	tool := makeTool("mytool", "does stuff", "result")
	wf := newTestWorkflow([]Step{
		{ID: "s1", Tool: "mytool", ToolInput: map[string]any{"ch": make(chan int)}},
	}, nil)
	ex := newToolExecutor([]goai.Tool{tool}, wf)
	ex.Progress = sink

	sr := ex.runToolStep(t.Context(), "run1", "s1", wf.Steps[0], map[string]*StepResult{}, 0, 1)
	if sr.Status != spec.StepFailed {
		t.Errorf("status = %q, want %q", sr.Status, spec.StepFailed)
	}

	var hasErrorEvent bool
	for _, e := range events {
		if e.Type == types.EventError && e.StepID == "s1" {
			hasErrorEvent = true
		}
	}
	if !hasErrorEvent {
		t.Error("expected EventError for marshal failure")
	}
}
