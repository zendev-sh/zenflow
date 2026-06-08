//go:build !e2e

package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/zenflow/internal/spec"
	"github.com/zendev-sh/zenflow/internal/types"
)

// --- cel.go uncovered branches ---

// TestCompileCEL_EnvError covers the getCELEnv() failure path in compileCEL (line 53-55).
func TestCompileCEL_EnvError(t *testing.T) {
	orig := getCELEnv
	t.Cleanup(func() { getCELEnv = orig })
	wantErr := errors.New("injected env failure")
	getCELEnv = func() (*cel.Env, error) { return nil, wantErr }

	_, err := EvaluateCEL("true", &EvalContext{Steps: map[string]*EvalStepContext{}})
	if err == nil {
		t.Fatal("expected error from getCELEnv failure")
	}
	if !strings.Contains(err.Error(), "injected env failure") {
		t.Errorf("error should mention injected failure, got: %v", err)
	}
}

// TestEvaluateCEL_NonBoolResult covers the non-bool result branch.
// The expression "1 + 1" evaluates to an int, not a bool.
func TestEvaluateCEL_NonBoolResult(t *testing.T) {
	ctx := &EvalContext{Steps: map[string]*EvalStepContext{}}
	_, err := EvaluateCEL("1 + 1", ctx)
	if err == nil {
		t.Fatal("expected error for non-bool CEL result")
	}
	if !strings.Contains(err.Error(), "want bool") {
		t.Errorf("error should mention 'want bool', got: %v", err)
	}
}

// TestEvaluateCEL_EvalError covers the eval error branch (line 98-100).
// Division by zero compiles fine but fails at eval time.
func TestEvaluateCEL_EvalError(t *testing.T) {
	ctx := &EvalContext{Steps: map[string]*EvalStepContext{}}
	_, err := EvaluateCEL("1/0 == 1", ctx)
	if err == nil {
		t.Fatal("expected eval error for 1/0")
	}
	if !strings.Contains(err.Error(), "cel eval") {
		t.Errorf("error should mention 'cel eval', got: %v", err)
	}
}

// TestEvaluateCELToString_NonStringResult covers the non-string result branch.
// The expression "1 + 1" evaluates to an int, not a string.
func TestEvaluateCELToString_NonStringResult(t *testing.T) {
	ctx := &EvalContext{Steps: map[string]*EvalStepContext{}}
	_, err := EvaluateCELToString("1 + 1", ctx)
	if err == nil {
		t.Fatal("expected error for non-string CEL result")
	}
	if !strings.Contains(err.Error(), "want string") {
		t.Errorf("error should mention 'want string', got: %v", err)
	}
}

// TestEvaluateCELToString_DivisionByZeroEvalError covers the eval error branch (line 132-134).
func TestEvaluateCELToString_DivisionByZeroEvalError(t *testing.T) {
	ctx := &EvalContext{Steps: map[string]*EvalStepContext{}}
	_, err := EvaluateCELToString("string(1/0)", ctx)
	if err == nil {
		t.Fatal("expected eval error for division by zero in string expr")
	}
	if !strings.Contains(err.Error(), "cel eval") {
		t.Errorf("error should mention 'cel eval', got: %v", err)
	}
}

// TestEvaluateCELToArray_NonListResult covers the non-list result branch.
// The expression "true" evaluates to a bool, not a list.
func TestEvaluateCELToArray_NonListResult(t *testing.T) {
	ctx := &EvalContext{Steps: map[string]*EvalStepContext{}}
	_, err := EvaluateCELToArray("true", ctx)
	if err == nil {
		t.Fatal("expected error for non-list CEL result")
	}
	if !strings.Contains(err.Error(), "want list") {
		t.Errorf("error should mention 'want list', got: %v", err)
	}
}

// TestEvaluateCELToArray_EvalError covers the eval error branch (line 167-169).
func TestEvaluateCELToArray_EvalError(t *testing.T) {
	ctx := &EvalContext{Steps: map[string]*EvalStepContext{}}
	// Use an expression that compiles as a list but fails at eval.
	// [1/0] is a list literal whose element causes division by zero during eval.
	_, err := EvaluateCELToArray("[1/0]", ctx)
	if err == nil {
		t.Fatal("expected eval error for division by zero in list expr")
	}
	if !strings.Contains(err.Error(), "cel eval") {
		t.Errorf("error should mention 'cel eval', got: %v", err)
	}
}

// --- executor_tool.go uncovered branches ---

// TestRunToolStep_JSONMarshalError covers the json.Marshal failure path (lines 89-102).
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

// --- parse.go uncovered branches ---

// TestLoadWorkflow_FileTooLarge covers parse.go:42-44 (stat size check).
func TestLoadWorkflow_FileTooLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.yaml")
	// Create a sparse file larger than MaxFileSizeBytes without writing content.
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxFileSizeBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	_, err = LoadWorkflow(path)
	if err == nil {
		t.Fatal("expected error for oversized workflow file")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should mention 'exceeds', got: %v", err)
	}
}

// TestParseWorkflowJSON_TooLarge covers parse.go:79-81.
func TestParseWorkflowJSON_TooLarge(t *testing.T) {
	big := make([]byte, MaxFileSizeBytes+1)
	for i := range big {
		big[i] = ' '
	}
	_, err := ParseWorkflowJSON(big)
	if err == nil {
		t.Fatal("expected error for oversized JSON workflow")
	}
	if !strings.Contains(err.Error(), "max file size") {
		t.Errorf("error should mention 'max file size', got: %v", err)
	}
}

// TestParseWorkflowJSON_EnforceLimits covers parse.go:95-97.
// A workflow with too many steps triggers enforceLimits.
func TestParseWorkflowJSON_EnforceLimits(t *testing.T) {
	// Build a JSON workflow with MaxStepsPerWorkflow+1 steps.
	type jsonStep struct {
		ID           string `json:"id"`
		Instructions string `json:"instructions"`
	}
	type jsonWorkflow struct {
		Name  string     `json:"name"`
		Steps []jsonStep `json:"steps"`
	}
	steps := make([]jsonStep, MaxStepsPerWorkflow+1)
	for i := range steps {
		steps[i] = jsonStep{ID: fmt.Sprintf("s%d", i+1), Instructions: "do something"}
	}
	wf := jsonWorkflow{Name: "too-many-steps", Steps: steps}
	data, err := json.Marshal(wf)
	if err != nil {
		t.Fatal(err)
	}

	_, err = ParseWorkflowJSON(data)
	if err == nil {
		t.Fatal("expected error for too-many-steps workflow")
	}
	if !strings.Contains(err.Error(), "too many steps") {
		t.Errorf("error should mention 'too many steps', got: %v", err)
	}
}

// TestValidateWorkflow_ToolInputWithoutTool_JSON covers parse.go:326-328 via ValidateWorkflow directly.
// The YAML test was using the wrong field name (toolInput vs tool_input), so the
// field was rejected before validation reached line 326.
func TestValidateWorkflow_ToolInputWithoutTool_Direct(t *testing.T) {
	wf := &Workflow{
		Name: "tool-input-no-tool",
		Steps: []Step{
			{ID: "s1", Instructions: "do it", ToolInput: map[string]any{"key": "value"}},
		},
	}
	_, err := ValidateWorkflow(wf)
	if err == nil {
		t.Fatal("expected error for tool_input without tool")
	}
	if !strings.Contains(err.Error(), "tool_input requires tool") {
		t.Errorf("error should mention 'tool_input requires tool', got: %v", err)
	}
}

// TestReadRef_FilepathAbsError_Resolved covers parse.go:576-578.
// We inject a filepathAbs that fails on the first call (resolving the ref path).
func TestReadRef_FilepathAbsError_Resolved(t *testing.T) {
	orig := filepathAbs
	t.Cleanup(func() { filepathAbs = orig })

	callCount := 0
	wantErr := errors.New("injected abs failure")
	filepathAbs = func(path string) (string, error) {
		callCount++
		if callCount == 1 {
			return "", wantErr
		}
		return filepath.Abs(path)
	}

	dir := t.TempDir()
	_, err := readRef(dir, "some.txt")
	if err == nil {
		t.Fatal("expected error from filepathAbs injection")
	}
	if !strings.Contains(err.Error(), "resolve path") {
		t.Errorf("error should mention 'resolve path', got: %v", err)
	}
}

// TestReadRef_FilepathAbsError_Base covers parse.go:580-582.
// We inject a filepathAbs that succeeds on the first call but fails on the second (base).
func TestReadRef_FilepathAbsError_Base(t *testing.T) {
	orig := filepathAbs
	t.Cleanup(func() { filepathAbs = orig })

	callCount := 0
	wantErr := errors.New("injected abs base failure")
	filepathAbs = func(path string) (string, error) {
		callCount++
		if callCount == 2 {
			return "", wantErr
		}
		return filepath.Abs(path)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "some.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readRef(dir, "some.txt")
	if err == nil {
		t.Fatal("expected error from filepathAbs base injection")
	}
	if !strings.Contains(err.Error(), "resolve base") {
		t.Errorf("error should mention 'resolve base', got: %v", err)
	}
}

// TestReadRef_RefFileTooLarge covers parse.go:603-605.
// Create a sparse file > MaxFileSizeBytes inside the temp dir so path check passes.
func TestReadRef_RefFileTooLarge(t *testing.T) {
	dir := t.TempDir()
	refPath := filepath.Join(dir, "big.md")
	f, err := os.Create(refPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxFileSizeBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	_, err = readRef(dir, "big.md")
	if err == nil {
		t.Fatal("expected error for oversized ref file")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should mention 'exceeds', got: %v", err)
	}
}

// TestResolveChainedRef_FilepathAbsError covers parse.go:546-548.
// Inject filepathAbs failure so resolveChainedRef fails at the abs key computation.
func TestResolveChainedRef_FilepathAbsError(t *testing.T) {
	orig := filepathAbs
	t.Cleanup(func() { filepathAbs = orig })

	wantErr := errors.New("injected chained abs failure")
	filepathAbs = func(path string) (string, error) {
		return "", wantErr
	}

	_, err := resolveChainedRef(t.TempDir(), "some.md", 1, map[string]bool{})
	if err == nil {
		t.Fatal("expected error from filepathAbs injection")
	}
	if !strings.Contains(err.Error(), "resolve ref path") {
		t.Errorf("error should mention 'resolve ref path', got: %v", err)
	}
}
