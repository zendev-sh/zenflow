//go:build !e2e

package exec

import (
	"testing"

	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/spec"
)

func TestScheduler_DependencyFirst_IsDefault(t *testing.T) {
	// When no scheduler is set (empty string), dispatch should work as before:
	// all ready steps are dispatched in topological order.
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "a done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
			{Text: "b done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
			{Text: "c done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}
	wf := &Workflow{
		Name: "default-scheduler",
		Steps: []Step{
			{ID: "a", Instructions: "do a"},
			{ID: "b", DependsOn: []string{"a"}, Instructions: "do b"},
			{ID: "c", DependsOn: []string{"a"}, Instructions: "do c"},
		},
		// Options.Scheduler is empty → dependency-first default.
	}
	exec := newTestExecutor(llm, nil, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
	for _, id := range []string{"a", "b", "c"} {
		sr := result.Steps[id]
		if sr == nil || sr.Status != spec.StepCompleted {
			t.Errorf("step %q: got %v, want completed", id, sr)
		}
	}
}

func TestScheduler_RoundRobin_Distribution(t *testing.T) {
	// 4 parallel steps using 2 agents with round-robin scheduler.
	// Verify all steps complete and the scheduleOrder function produces
	// correct interleaving (tested directly in TestScheduleOrder_RoundRobin_AlternatesAgents).
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
			{Text: "done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
			{Text: "done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
			{Text: "done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}
	wf := &Workflow{
		Name: "round-robin-test",
		Agents: map[string]AgentConfig{
			"alpha": {Description: "agent alpha"},
			"beta":  {Description: "agent beta"},
		},
		Steps: []Step{
			{ID: "s1", Agent: "alpha", Instructions: "do s1"},
			{ID: "s2", Agent: "beta", Instructions: "do s2"},
			{ID: "s3", Agent: "alpha", Instructions: "do s3"},
			{ID: "s4", Agent: "beta", Instructions: "do s4"},
		},
		Options: WorkflowOptions{
			Scheduler: spec.SchedulerRoundRobin,
		},
	}
	exec := newTestExecutor(llm, nil, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
	// All 4 steps should complete regardless of dispatch order.
	for _, id := range []string{"s1", "s2", "s3", "s4"} {
		sr := result.Steps[id]
		if sr == nil || sr.Status != spec.StepCompleted {
			t.Errorf("step %q: got %v, want completed", id, sr)
		}
	}
}

func TestScheduler_LeastBusy_Distribution(t *testing.T) {
	// 4 parallel steps with 2 agents. Least-busy should prefer the agent
	// with fewer currently running steps.
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
			{Text: "done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
			{Text: "done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
			{Text: "done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}
	wf := &Workflow{
		Name: "least-busy-test",
		Agents: map[string]AgentConfig{
			"alpha": {Description: "agent alpha"},
			"beta":  {Description: "agent beta"},
		},
		Steps: []Step{
			{ID: "s1", Agent: "alpha", Instructions: "do s1"},
			{ID: "s2", Agent: "alpha", Instructions: "do s2"},
			{ID: "s3", Agent: "beta", Instructions: "do s3"},
			{ID: "s4", Agent: "beta", Instructions: "do s4"},
		},
		Options: WorkflowOptions{
			Scheduler: spec.SchedulerLeastBusy,
		},
	}
	exec := newTestExecutor(llm, nil, wf)
	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != spec.StatusCompleted {
		t.Errorf("status = %q, want %q", result.Status, spec.StatusCompleted)
	}
	// All 4 steps should complete regardless of dispatch order.
	for _, id := range []string{"s1", "s2", "s3", "s4"} {
		sr := result.Steps[id]
		if sr == nil || sr.Status != spec.StepCompleted {
			t.Errorf("step %q: got %v, want completed", id, sr)
		}
	}
}

func TestScheduler_Validation(t *testing.T) {
	// Already tested in coordinator_test.go but verify it here too.
	wf := &Workflow{
		Name: "test",
		Steps: []Step{
			{ID: "s1", Instructions: "do"},
		},
		Options: WorkflowOptions{
			Scheduler: "invalid-strategy",
		},
	}
	_, err := ValidateWorkflow(wf)
	if err == nil {
		t.Error("expected validation error for invalid scheduler")
	}
}

func TestScheduleOrder_DependencyFirst_ReturnsSameOrder(t *testing.T) {
	exec := &Executor{
		Workflow: &Workflow{Options: WorkflowOptions{Scheduler: ""}},
	}
	ready := []Step{
		{ID: "a", Agent: "x"},
		{ID: "b", Agent: "y"},
	}
	ordered := exec.scheduleOrder(ready, nil)
	if len(ordered) != 2 || ordered[0].ID != "a" || ordered[1].ID != "b" {
		t.Errorf("dependency-first should return same order, got %v", stepIDs(ordered))
	}
}

func TestScheduleOrder_RoundRobin_AlternatesAgents(t *testing.T) {
	exec := &Executor{
		Workflow: &Workflow{Options: WorkflowOptions{Scheduler: spec.SchedulerRoundRobin}},
	}
	ready := []Step{
		{ID: "a1", Agent: "alpha"},
		{ID: "a2", Agent: "alpha"},
		{ID: "b1", Agent: "beta"},
		{ID: "b2", Agent: "beta"},
	}
	running := map[string]bool{"running1": true} // some step running
	ordered := exec.scheduleOrder(ready, running)
	if len(ordered) != 4 {
		t.Fatalf("expected 4 steps, got %d", len(ordered))
	}
	// Verify full interleaving: no two consecutive steps should have the same agent.
	for i := 1; i < len(ordered); i++ {
		if ordered[i].Agent == ordered[i-1].Agent {
			t.Errorf("round-robin: consecutive steps %d and %d have same agent %q (order: %v)",
				i-1, i, ordered[i].Agent, stepIDs(ordered))
		}
	}
}

func TestScheduleOrder_LeastBusy_PrefersIdleAgent(t *testing.T) {
	exec := &Executor{
		Workflow: &Workflow{
			Options: WorkflowOptions{Scheduler: spec.SchedulerLeastBusy},
			Steps: []Step{
				{ID: "running1", Agent: "alpha"},
				{ID: "ready1", Agent: "alpha"},
				{ID: "ready2", Agent: "beta"},
			},
		},
	}
	ready := []Step{
		{ID: "ready1", Agent: "alpha"},
		{ID: "ready2", Agent: "beta"},
	}
	// alpha has 1 running step, beta has 0
	running := map[string]bool{"running1": true}
	ordered := exec.scheduleOrder(ready, running)
	if len(ordered) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(ordered))
	}
	// beta (0 running) should come before alpha (1 running)
	if ordered[0].Agent != "beta" {
		t.Errorf("least-busy should prefer beta (0 running), got %s first (order: %v)", ordered[0].Agent, stepIDs(ordered))
	}
}

// stepIDs extracts step IDs from a slice for test output.
func stepIDs(steps []Step) []string {
	ids := make([]string, len(steps))
	for i, s := range steps {
		ids[i] = s.ID
	}
	return ids
}
