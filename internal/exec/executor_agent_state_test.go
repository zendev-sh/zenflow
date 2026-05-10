package exec

import (
	"encoding/json"
	"testing"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/spec"
)

// TestExecutor_AgentStateAccessor verifies that after Run, every executed
// step has a registered *goai.AgentState observable via Executor.AgentState
// and that each state reports a terminal kind (StepDone for natural
// completion).
func TestExecutor_AgentStateAccessor(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "out-1", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
			{Text: "out-2", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}
	tools := []goai.Tool{
		{Name: "noop", Description: "noop", InputSchema: json.RawMessage(`{}`)},
	}
	wf := newTestWorkflow(
		[]Step{
			{ID: "first", Instructions: "first step"},
			{ID: "second", DependsOn: []string{"first"}, Instructions: "second step"},
		},
		nil,
	)
	exec := newTestExecutor(model, tools, wf)

	// Pre-Run: accessor returns nil for unknown step.
	if exec.AgentState("first") != nil {
		t.Fatalf("AgentState should be nil before Run")
	}

	res, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != spec.StatusCompleted {
		t.Fatalf("status = %v", res.Status)
	}

	// Post-Run: each step has a registered AgentState in StepIdle.
	for _, id := range []string{"first", "second"} {
		st := exec.AgentState(id)
		if st == nil {
			t.Fatalf("AgentState(%q) is nil", id)
		}
		kind, step := st.Observe()
		// G3 : AgentRunner.Run sets StepDone via SetTerminal on
		// natural completion. StepIdle would only persist if SetTerminal
		// was never invoked (e.g. consumer that drives AgentState
		// without the runner).
		if kind != goai.StepDone {
			t.Fatalf("step %q kind = %v (want StepDone), step=%d", id, kind, step)
		}
	}

	// Unknown ID still returns nil.
	if exec.AgentState("ghost") != nil {
		t.Fatalf("unknown step should return nil")
	}
}
