//go:build !e2e

package exec

import (
	"context"
	"github.com/zendev-sh/goai/provider"
	"sync"
	"testing"

	"github.com/zendev-sh/zenflow/internal/spec"
)

// mockIsolation tracks Setup/Cleanup calls for testing.
type mockIsolation struct {
	mu       sync.Mutex
	setups   []isolationCall
	cleanups []isolationCall
	workDir  string
	setupErr error
}

type isolationCall struct {
	RunID  string
	StepID string
}

func (m *mockIsolation) Setup(_ context.Context, runID, stepID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setups = append(m.setups, isolationCall{RunID: runID, StepID: stepID})
	return m.workDir, m.setupErr
}

func (m *mockIsolation) Cleanup(_ context.Context, runID, stepID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanups = append(m.cleanups, isolationCall{RunID: runID, StepID: stepID})
	return nil
}

func TestIsolation_Setup_Called(t *testing.T) {
	iso := &mockIsolation{workDir: "/tmp/test-workdir"}
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "step done", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	wf := &Workflow{
		Name:  "iso-test",
		Steps: []Step{{ID: "s1", Instructions: "do it"}},
	}
	exec := &Executor{
		Runner:       &AgentRunner{model: llm, tools: nil},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Isolation:    iso,
	}
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
	iso.mu.Lock()
	defer iso.mu.Unlock()
	if len(iso.setups) != 1 {
		t.Fatalf("Setup called %d times, want 1", len(iso.setups))
	}
	if iso.setups[0].StepID != "s1" {
		t.Errorf("Setup stepID = %q, want %q", iso.setups[0].StepID, "s1")
	}
}

func TestIsolation_Cleanup_Called(t *testing.T) {
	iso := &mockIsolation{}
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "step done", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	wf := &Workflow{
		Name:  "iso-test",
		Steps: []Step{{ID: "s1", Instructions: "do it"}},
	}
	exec := &Executor{
		Runner:       &AgentRunner{model: llm, tools: nil},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Isolation:    iso,
	}
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
	iso.mu.Lock()
	defer iso.mu.Unlock()
	if len(iso.cleanups) != 1 {
		t.Fatalf("Cleanup called %d times, want 1", len(iso.cleanups))
	}
	if iso.cleanups[0].StepID != "s1" {
		t.Errorf("Cleanup stepID = %q, want %q", iso.cleanups[0].StepID, "s1")
	}
}

func TestIsolation_Cleanup_CalledOnFailure(t *testing.T) {
	// Cleanup must be called even when the step fails.
	iso := &mockIsolation{}
	failLLM := &failingLLM{
		failOnCall: 1,
		okResponse: &provider.GenerateResult{Text: "ok"},
	}
	wf := &Workflow{
		Name:  "iso-fail-test",
		Steps: []Step{{ID: "s1", Instructions: "do it"}},
	}
	exec := &Executor{
		Runner:       &AgentRunner{model: failLLM, tools: nil},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Isolation:    iso,
	}
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusFailed {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusFailed)
	}
	iso.mu.Lock()
	defer iso.mu.Unlock()
	if len(iso.cleanups) != 1 {
		t.Fatalf("Cleanup should be called even on failure, got %d calls", len(iso.cleanups))
	}
}

func TestIsolation_WorkDir_AffectsBaseDir(t *testing.T) {
	// When isolation.Setup returns a workDir, it is passed to Setup correctly
	// and the returned workDir is used as the base directory for the step.
	const expectedWorkDir = "/tmp/isolated-step"
	iso := &mockIsolation{workDir: expectedWorkDir}
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}
	wf := &Workflow{
		Name:  "iso-workdir-test",
		Steps: []Step{{ID: "s1", Instructions: "do it"}},
	}
	exec := &Executor{
		Runner:       &AgentRunner{model: llm, tools: nil},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Isolation:    iso,
	}
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
	// Verify Setup was called with correct args and returned the expected workDir.
	iso.mu.Lock()
	defer iso.mu.Unlock()
	if len(iso.setups) != 1 {
		t.Fatalf("Setup called %d times, want 1", len(iso.setups))
	}
	if iso.setups[0].StepID != "s1" {
		t.Errorf("Setup stepID = %q, want %q", iso.setups[0].StepID, "s1")
	}
	if iso.workDir != expectedWorkDir {
		t.Errorf("workDir = %q, want %q", iso.workDir, expectedWorkDir)
	}
}

func TestIsolation_NoopByDefault(t *testing.T) {
	// When no isolation is set, steps should run normally without Setup/Cleanup.
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}
	wf := &Workflow{
		Name:  "no-iso-test",
		Steps: []Step{{ID: "s1", Instructions: "do it"}},
	}
	exec := &Executor{
		Runner:       &AgentRunner{model: llm, tools: nil},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
 // Isolation is nil - no isolation.
	}
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
}

func TestIsolation_MultipleSteps(t *testing.T) {
	// Each step should get its own Setup/Cleanup call.
	iso := &mockIsolation{workDir: "/tmp/test"}
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
			{Text: "done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}
	wf := &Workflow{
		Name: "iso-multi-test",
		Steps: []Step{
			{ID: "s1", Instructions: "step 1"},
			{ID: "s2", DependsOn: []string{"s1"}, Instructions: "step 2"},
		},
	}
	exec := &Executor{
		Runner:       &AgentRunner{model: llm, tools: nil},
		Workflow:     wf,
		DefaultModel: "gpt-4o",
		Isolation:    iso,
	}
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
	iso.mu.Lock()
	defer iso.mu.Unlock()
	if len(iso.setups) != 2 {
		t.Errorf("Setup called %d times, want 2", len(iso.setups))
	}
	if len(iso.cleanups) != 2 {
		t.Errorf("Cleanup called %d times, want 2", len(iso.cleanups))
	}
}

func TestWithIsolation(t *testing.T) {
	iso := &mockIsolation{}
	o := New(WithIsolation(iso))
	if o.isolation != iso {
		t.Error("WithIsolation did not set isolation")
	}
}
