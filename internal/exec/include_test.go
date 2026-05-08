//go:build !e2e

package exec

import (
	"context"
	"errors"
	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zendev-sh/zenflow/internal/spec"
)

func TestExecutor_Include_LoadsSubWorkflow(t *testing.T) {
	// step with include="deploy" loads from includes registry.
	// Create a temp dir with a sub-workflow YAML.
	dir := t.TempDir()
	subYAML := `name: deploy-sub
version: 1
agents:
  deployer:
    description: "Deploys stuff"
steps:
  - id: deploy_step
    agent: deployer
    instructions: "Deploy the thing"
`
	if err := os.WriteFile(filepath.Join(dir, "deploy.yaml"), []byte(subYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "deployed!", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}
	var tools []goai.Tool

	wf := &Workflow{
		Name:     "include-test",
		Includes: map[string]string{"deploy": "deploy.yaml"},
		Steps: []Step{
			{ID: "do-deploy", Include: "deploy"},
		},
		BaseDir: dir,
	}

	exec := newTestExecutor(llm, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sr := result.Steps["do-deploy"]
	if sr == nil {
		t.Fatal("step 'do-deploy' missing from results")
	}
	if sr.Status != spec.StepCompleted {
		t.Errorf("step status = %q, want %q (error: %v)", sr.Status, spec.StepCompleted, sr.Error)
	}
}

func TestExecutor_Include_SubWorkflowExecutes(t *testing.T) {
	// Sub-workflow steps run inline, results merged back.
	dir := t.TempDir()
	subYAML := `name: build-sub
version: 1
agents:
  builder:
    description: "Builds things"
steps:
  - id: compile
    agent: builder
    instructions: "Compile code"
  - id: test
    agent: builder
    dependsOn: [compile]
    instructions: "Run tests"
`
	if err := os.WriteFile(filepath.Join(dir, "build.yaml"), []byte(subYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "compiled", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
			{Text: "tests passed", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}
	var tools []goai.Tool

	wf := &Workflow{
		Name:     "include-exec-test",
		Includes: map[string]string{"build": "build.yaml"},
		Steps: []Step{
			{ID: "do_build", Include: "build"},
		},
		BaseDir: dir,
	}

	exec := newTestExecutor(llm, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sr := result.Steps["do_build"]
	if sr == nil {
		t.Fatal("step 'do_build' missing from results")
	}
	if sr.Status != spec.StepCompleted {
		t.Errorf("step status = %q, want %q (error: %v)", sr.Status, spec.StepCompleted, sr.Error)
	}

	// Sub-workflow had 2 steps - should have called LLM 2 times.
	if len(llm.calls) != 2 {
		t.Errorf("llm calls = %d, want 2", len(llm.calls))
	}
}

func TestExecutor_Include_MissingInclude(t *testing.T) {
	// include ref not in registry → error.
	llm := &mockModel{}
	var tools []goai.Tool

	wf := &Workflow{
		Name: "missing-include-test",
		Steps: []Step{
			{ID: "bad-ref", Include: "nonexistent"},
		},
	}

	exec := newTestExecutor(llm, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sr := result.Steps["bad-ref"]
	if sr == nil {
		t.Fatal("step 'bad-ref' missing from results")
	}
	if sr.Status != spec.StepFailed {
		t.Errorf("step status = %q, want %q", sr.Status, spec.StepFailed)
	}
	if sr.Error == nil {
		t.Fatal("expected error for missing include ref")
	}
	if !strings.Contains(sr.Error.Error(), "nonexistent") {
		t.Errorf("error should mention 'nonexistent', got: %v", sr.Error)
	}
}

func TestExecutor_Include_InheritsTimeout(t *testing.T) {
	// Sub-workflow inherits parent step timeout.
	dir := t.TempDir()
	subYAML := `name: slow-sub
version: 1
agents:
  worker:
    description: "Slow worker"
steps:
  - id: slow_step
    agent: worker
    instructions: "Do slow thing"
`
	if err := os.WriteFile(filepath.Join(dir, "slow.yaml"), []byte(subYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Use slowMockLLM that takes 10ms per call. Timeout is 1ms.
	slowLLM := &slowMockLLM{}
	var tools []goai.Tool

	wf := &Workflow{
		Name:     "include-timeout-test",
		Includes: map[string]string{"slow": "slow.yaml"},
		Steps: []Step{
			{
				ID:      "do-slow",
				Include: "slow",
				Timeout: Duration(time.Millisecond), // 1ms - LLM takes 10ms
			},
		},
		BaseDir: dir,
	}

	exec := newTestExecutor(slowLLM, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sr := result.Steps["do-slow"]
	if sr == nil {
		t.Fatal("step 'do-slow' missing from results")
	}
	// Should fail due to timeout.
	if sr.Status != spec.StepFailed {
		t.Errorf("step status = %q, want %q (should timeout)", sr.Status, spec.StepFailed)
	}
}

func TestExecutor_Include_FallbackToDirectFilePath(t *testing.T) {
	// Issue 7: When include value is not found in includes registry,
	// fall back to treating it as a direct file path.
	dir := t.TempDir()
	subYAML := `name: direct-sub
version: 1
agents:
  worker:
    description: "Worker"
steps:
  - id: inner_step
    agent: worker
    instructions: "Do work"
`
	if err := os.WriteFile(filepath.Join(dir, "sub-workflow.yaml"), []byte(subYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "direct include result", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}
	var tools []goai.Tool

	wf := &Workflow{
		Name: "direct-include-test",
 // No Includes map - the include value should be treated as a direct file path.
		Steps: []Step{
			{ID: "do-it", Include: "sub-workflow.yaml"},
		},
		BaseDir: dir,
	}

	exec := newTestExecutor(llm, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sr := result.Steps["do-it"]
	if sr == nil {
		t.Fatal("step 'do-it' missing from results")
	}
	if sr.Status != spec.StepCompleted {
		t.Errorf("step status = %q, want %q (error: %v)", sr.Status, spec.StepCompleted, sr.Error)
	}
	if sr.Content != "direct include result" {
		t.Errorf("content = %q, want %q", sr.Content, "direct include result")
	}
}

func TestExecutor_Include_FallbackNotFound(t *testing.T) {
	// Direct file path fallback - file doesn't exist → error.
	llm := &mockModel{}
	var tools []goai.Tool

	wf := &Workflow{
		Name: "fallback-notfound-test",
		Steps: []Step{
			{ID: "bad", Include: "nonexistent-workflow.yaml"},
		},
		BaseDir: t.TempDir(),
	}

	exec := newTestExecutor(llm, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sr := result.Steps["bad"]
	if sr == nil {
		t.Fatal("step 'bad' missing from results")
	}
	if sr.Status != spec.StepFailed {
		t.Errorf("step status = %q, want %q", sr.Status, spec.StepFailed)
	}
}

func TestExecutor_Include_NamespacedInnerStepIDs(t *testing.T) {
	// Issue 4: Include step results should have namespaced inner step IDs.
	dir := t.TempDir()
	subYAML := `name: inner-ns
version: 1
agents:
  w:
    description: "Worker"
steps:
  - id: compile
    agent: w
    instructions: "Compile"
  - id: test_step
    agent: w
    dependsOn: [compile]
    instructions: "Test"
`
	if err := os.WriteFile(filepath.Join(dir, "inner.yaml"), []byte(subYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "compiled", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			{Text: "tested", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	var tools []goai.Tool

	wf := &Workflow{
		Name:     "include-ns-test",
		Includes: map[string]string{"build": "inner.yaml"},
		Steps: []Step{
			{ID: "do_build", Include: "build"},
		},
		BaseDir: dir,
	}

	exec := newTestExecutor(llm, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sr := result.Steps["do_build"]
	if sr == nil {
		t.Fatal("step 'do_build' missing")
	}
	if sr.Status != spec.StepCompleted {
		t.Errorf("status = %q, want %q (error: %v)", sr.Status, spec.StepCompleted, sr.Error)
	}

	// Check for namespaced inner step IDs in Result.
	innerSteps, ok := sr.Result["innerSteps"].(map[string]any)
	if !ok {
		t.Fatalf("expected innerSteps in result, got %T", sr.Result["innerSteps"])
	}
	// Expect keys: "do_build.compile" and "do_build.test_step"
	for _, expectedKey := range []string{"do_build.compile", "do_build.test_step"} {
		if _, ok := innerSteps[expectedKey]; !ok {
			t.Errorf("missing namespaced inner step key %q in result; got keys: %v", expectedKey, mapKeys(innerSteps))
		}
	}
}

func TestExecutor_Include_PathTraversal(t *testing.T) {
	// include: "../../etc/foo.yaml" should be rejected as path traversal.
	llm := &mockModel{}
	var tools []goai.Tool

	wf := &Workflow{
		Name: "path-traversal-test",
		Steps: []Step{
			{ID: "evil", Include: "../../etc/foo.yaml"},
		},
		BaseDir: t.TempDir(),
	}

	exec := newTestExecutor(llm, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sr := result.Steps["evil"]
	if sr == nil {
		t.Fatal("step 'evil' missing from results")
	}
	if sr.Status != spec.StepFailed {
		t.Errorf("step status = %q, want %q", sr.Status, spec.StepFailed)
	}
	if sr.Error == nil || !strings.Contains(sr.Error.Error(), "escapes workflow directory") {
		t.Errorf("error = %v, want 'escapes workflow directory'", sr.Error)
	}
}

func TestExecutor_Include_PathTraversal_ViaRegistry(t *testing.T) {
	// Even includes registry entries should be checked for traversal.
	llm := &mockModel{}
	var tools []goai.Tool

	wf := &Workflow{
		Name:     "registry-traversal-test",
		Includes: map[string]string{"evil": "../../etc/passwd.yaml"},
		Steps: []Step{
			{ID: "evil", Include: "evil"},
		},
		BaseDir: t.TempDir(),
	}

	exec := newTestExecutor(llm, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sr := result.Steps["evil"]
	if sr == nil {
		t.Fatal("step 'evil' missing from results")
	}
	if sr.Status != spec.StepFailed {
		t.Errorf("step status = %q, want %q", sr.Status, spec.StepFailed)
	}
	if sr.Error == nil || !strings.Contains(sr.Error.Error(), "escapes workflow directory") {
		t.Errorf("error = %v, want 'escapes workflow directory'", sr.Error)
	}
}

func TestExecutor_Include_PathTraversal_EmptyBaseDir(t *testing.T) {
	// When BaseDir is empty, absolute paths and ../ should be rejected.
	wf := &Workflow{
		Name:    "test-empty-basedir-traversal",
		BaseDir: "", // empty - programmatic workflow
		Steps: []Step{
			{ID: "inc", Include: "../../etc/foo.yaml"},
		},
	}
	exec := newTestExecutor(&mockModel{}, nil, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr := result.Steps["inc"]
	if sr == nil {
		t.Fatal("step inc missing")
	}
	if sr.Status != spec.StepFailed {
		t.Errorf("status = %q, want failed", sr.Status)
	}
	if sr.Error == nil || !strings.Contains(sr.Error.Error(), "escapes workflow directory") {
		t.Errorf("error = %v, want 'escapes workflow directory'", sr.Error)
	}
}

func TestExecutor_Include_PathTraversal_AbsolutePathNoBaseDir(t *testing.T) {
	// Absolute paths should be rejected when no BaseDir is set.
	wf := &Workflow{
		Name:    "test-abs-path-no-basedir",
		BaseDir: "",
		Steps: []Step{
			{ID: "inc", Include: "/etc/sensitive.yaml"},
		},
	}
	exec := newTestExecutor(&mockModel{}, nil, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr := result.Steps["inc"]
	if sr == nil {
		t.Fatal("step inc missing")
	}
	if sr.Status != spec.StepFailed {
		t.Errorf("status = %q, want failed", sr.Status)
	}
	if sr.Error == nil || !strings.Contains(sr.Error.Error(), "escapes workflow directory") {
		t.Errorf("error = %v, want 'escapes workflow directory'", sr.Error)
	}
}

// TestExecutor_Include_PathTraversal_ErrorsIs verifies that path-traversal
// rejections expose ErrIncludePathEscape via errors.Is - confirms the
// sentinel-error contract for callers that want to distinguish escape
// rejections from generic load errors.
func TestExecutor_Include_PathTraversal_ErrorsIs(t *testing.T) {
	cases := []struct {
		name string
		wf   *Workflow
	}{
		{
			name: "with BaseDir, ../ traversal",
			wf: &Workflow{
				Name:    "errorsis-with-basedir",
				BaseDir: t.TempDir(),
				Steps: []Step{
					{ID: "evil", Include: "../../etc/passwd.yaml"},
				},
			},
		},
		{
			name: "empty BaseDir, ../ traversal",
			wf: &Workflow{
				Name:    "errorsis-empty-basedir",
				BaseDir: "",
				Steps: []Step{
					{ID: "evil", Include: "../foo.yaml"},
				},
			},
		},
		{
			name: "empty BaseDir, absolute path",
			wf: &Workflow{
				Name:    "errorsis-empty-basedir-abs",
				BaseDir: "",
				Steps: []Step{
					{ID: "evil", Include: "/etc/sensitive.yaml"},
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := newTestExecutor(&mockModel{}, nil, tc.wf)
			result, err := exec.Run(t.Context())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			sr := result.Steps["evil"]
			if sr == nil {
				t.Fatal("step 'evil' missing from results")
			}
			if sr.Status != spec.StepFailed {
				t.Errorf("status = %q, want %q", sr.Status, spec.StepFailed)
			}
			if sr.Error == nil {
				t.Fatal("expected non-nil error")
			}
			if !errors.Is(sr.Error, ErrIncludePathEscape) {
				t.Errorf("errors.Is(%v, ErrIncludePathEscape) = false; want true", sr.Error)
			}
		})
	}
}

// --- G1: Agent name collision detection ---

func TestExecutor_Include_AgentNameCollision(t *testing.T) {
	dir := t.TempDir()
	subYAML := `name: sub-with-agent
version: 1
agents:
  shared-agent:
    description: "Sub agent"
steps:
  - id: inner
    agent: shared-agent
    instructions: "Do inner work"
`
	if err := os.WriteFile(filepath.Join(dir, "sub.yaml"), []byte(subYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	wf := &Workflow{
		Name:     "collision-test",
		Agents:   map[string]AgentConfig{"shared-agent": {Description: "Parent agent"}},
		Includes: map[string]string{"sub": "sub.yaml"},
		Steps:    []Step{{ID: "inc", Include: "sub"}},
		BaseDir:  dir,
	}

	exec := newTestExecutor(&mockModel{}, nil, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sr := result.Steps["inc"]
	if sr == nil {
		t.Fatal("step 'inc' missing")
	}
	if sr.Status != spec.StepFailed {
		t.Errorf("status = %q, want %q", sr.Status, spec.StepFailed)
	}
	if sr.Error == nil || !strings.Contains(sr.Error.Error(), "agent name collision") {
		t.Errorf("error = %v, want 'agent name collision'", sr.Error)
	}
	if sr.Error == nil || !strings.Contains(sr.Error.Error(), "shared-agent") {
		t.Errorf("error should mention colliding agent name, got: %v", sr.Error)
	}
}

func TestExecutor_Include_NoCollision_DifferentAgents(t *testing.T) {
	dir := t.TempDir()
	subYAML := `name: sub
version: 1
agents:
  sub-agent:
    description: "Sub agent"
steps:
  - id: inner
    agent: sub-agent
    instructions: "Do work"
`
	if err := os.WriteFile(filepath.Join(dir, "sub.yaml"), []byte(subYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	llm := &mockModel{responses: []*provider.GenerateResult{{Text: "ok", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}}}}
	wf := &Workflow{
		Name:     "no-collision-test",
		Agents:   map[string]AgentConfig{"parent-agent": {Description: "Parent"}},
		Includes: map[string]string{"sub": "sub.yaml"},
		Steps:    []Step{{ID: "inc", Include: "sub"}},
		BaseDir:  dir,
	}

	exec := newTestExecutor(llm, nil, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sr := result.Steps["inc"]
	if sr == nil {
		t.Fatal("step 'inc' missing")
	}
	if sr.Status != spec.StepCompleted {
		t.Errorf("status = %q, want %q (error: %v)", sr.Status, spec.StepCompleted, sr.Error)
	}
}

// --- G2: Recursive include depth limit ---

func TestExecutor_Include_DepthLimitExceeded(t *testing.T) {
	dir := t.TempDir()
	subYAML := `name: sub
version: 1
steps:
  - id: inner
    instructions: "Do work"
`
	if err := os.WriteFile(filepath.Join(dir, "sub.yaml"), []byte(subYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	wf := &Workflow{
		Name:     "depth-test",
		Includes: map[string]string{"sub": "sub.yaml"},
		Steps:    []Step{{ID: "inc", Include: "sub"}},
		BaseDir:  dir,
	}

	exec := newTestExecutor(&mockModel{}, nil, wf)
	exec.IncludeDepth = MaxIncludeDepth // Already at max depth

	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sr := result.Steps["inc"]
	if sr == nil {
		t.Fatal("step 'inc' missing")
	}
	if sr.Status != spec.StepFailed {
		t.Errorf("status = %q, want %q", sr.Status, spec.StepFailed)
	}
	if sr.Error == nil || !strings.Contains(sr.Error.Error(), "max include depth") {
		t.Errorf("error = %v, want 'max include depth'", sr.Error)
	}
}

func TestExecutor_Include_DepthUnderLimit(t *testing.T) {
	dir := t.TempDir()
	subYAML := `name: sub
version: 1
steps:
  - id: inner
    instructions: "Do work"
`
	if err := os.WriteFile(filepath.Join(dir, "sub.yaml"), []byte(subYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	llm := &mockModel{responses: []*provider.GenerateResult{{Text: "ok", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}}}}
	wf := &Workflow{
		Name:     "depth-ok-test",
		Includes: map[string]string{"sub": "sub.yaml"},
		Steps:    []Step{{ID: "inc", Include: "sub"}},
		BaseDir:  dir,
	}

	exec := newTestExecutor(llm, nil, wf)
	exec.IncludeDepth = MaxIncludeDepth - 1 // One under max - should succeed

	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sr := result.Steps["inc"]
	if sr == nil {
		t.Fatal("step 'inc' missing")
	}
	if sr.Status != spec.StepCompleted {
		t.Errorf("status = %q, want %q (error: %v)", sr.Status, spec.StepCompleted, sr.Error)
	}
}

// --- G3: Include dependsOn rewriting (parent dep injection) ---

func TestExecutor_Include_ParentDepResultsInjected(t *testing.T) {
	// Parent step "setup" produces content. Include step depends on "setup".
	// Inner steps with no dependsOn should see "setup" results in their prompt.
	dir := t.TempDir()
	subYAML := `name: sub
version: 1
agents:
  w:
    description: "Worker"
steps:
  - id: inner
    agent: w
    instructions: "Process parent context"
`
	if err := os.WriteFile(filepath.Join(dir, "sub.yaml"), []byte(subYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "setup done", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			{Text: "inner done", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	var tools []goai.Tool

	wf := &Workflow{
		Name:     "dep-inject-test",
		Includes: map[string]string{"sub": "sub.yaml"},
		Steps: []Step{
			{ID: "setup", Instructions: "Setup the environment"},
			{ID: "inc", Include: "sub", DependsOn: []string{"setup"}},
		},
		BaseDir: dir,
	}

	exec := newTestExecutor(llm, tools, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sr := result.Steps["inc"]
	if sr == nil {
		t.Fatal("step 'inc' missing")
	}
	if sr.Status != spec.StepCompleted {
		t.Errorf("status = %q, want %q (error: %v)", sr.Status, spec.StepCompleted, sr.Error)
	}

	// The inner step should have parent dep results in its prompt.
	// AssemblePrompt includes prior results text like "--- Prior step: setup ---".
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.calls) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(llm.calls))
	}
	// Second call is the inner step. Its first message (system/user prompt) should
	// contain the parent dep result from "setup".
	innerCall := llm.calls[1]
	var innerPrompt string
	for _, msg := range innerCall.Messages {
		for _, p := range msg.Content {
			if p.Type == provider.PartText {
				innerPrompt += p.Text + "\n"
			}
		}
	}
	if !strings.Contains(innerPrompt, "setup") {
		t.Errorf("inner step prompt should contain parent dep 'setup', got: %s", innerPrompt)
	}
}

// --- G4: Namespaced progress events ---

func TestExecutor_Include_NamespacedProgressEvents(t *testing.T) {
	dir := t.TempDir()
	subYAML := `name: sub
version: 1
agents:
  w:
    description: "Worker"
steps:
  - id: inner_step
    agent: w
    instructions: "Do work"
`
	if err := os.WriteFile(filepath.Join(dir, "sub.yaml"), []byte(subYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	llm := &mockModel{responses: []*provider.GenerateResult{{Text: "ok", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}}}}
	var tools []goai.Tool

	var mu sync.Mutex
	var events []Event
	sink := &includeTestProgressSink{
		mu:     &mu,
		events: &events,
	}

	wf := &Workflow{
		Name:     "ns-progress-test",
		Includes: map[string]string{"sub": "sub.yaml"},
		Steps:    []Step{{ID: "parent_inc", Include: "sub"}},
		BaseDir:  dir,
	}

	exec := newTestExecutor(llm, tools, wf)
	exec.Progress = sink

	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Fatalf("workflow status = %q, want %q", result.Status, spec.StatusCompleted)
	}

	// Check that inner step events have namespaced step IDs.
	mu.Lock()
	defer mu.Unlock()

	var namespacedStepIDs []string
	for _, ev := range events {
		if strings.HasPrefix(ev.StepID, "parent_inc.") {
			namespacedStepIDs = append(namespacedStepIDs, ev.StepID)
		}
	}
	if len(namespacedStepIDs) == 0 {
		var allStepIDs []string
		for _, ev := range events {
			if ev.StepID != "" {
				allStepIDs = append(allStepIDs, ev.StepID)
			}
		}
		t.Errorf("no namespaced step IDs found; all step IDs: %v", allStepIDs)
	}

	// Specifically check for "parent_inc.inner_step" prefix.
	found := false
	for _, id := range namespacedStepIDs {
		if id == "parent_inc.inner_step" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected namespaced ID 'parent_inc.inner_step' in events, got: %v", namespacedStepIDs)
	}
}

type includeTestProgressSink struct {
	mu     *sync.Mutex
	events *[]Event
}

func (s *includeTestProgressSink) OnEvent(_ context.Context, e Event) {
	s.mu.Lock()
	*s.events = append(*s.events, e)
	s.mu.Unlock()
}
func (s *includeTestProgressSink) OnOutput(_ context.Context, _ Output) {}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
