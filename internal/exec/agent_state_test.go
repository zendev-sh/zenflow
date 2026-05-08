package exec

import (
	"testing"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
)

// TestAgentRunner_StateRef verifies that when an AgentState is attached to
// the runner, goai mutates it during execution and the final observable
// state is StepIdle (loop terminated).
func TestAgentRunner_StateRef(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("done", 1, 1),
		},
	}

	state := &goai.AgentState{}
	runner := &AgentRunner{
		model:    model,
		stateRef: state,
	}
	if _, err := runner.Run(t.Context(), AgentConfig{}, "hi", "m", nil); err != nil {
		t.Fatalf("run: %v", err)
	}

	kind, step := state.Observe()
	// G3 : Run sets StepDone via SetTerminal on natural completion.
	if kind != goai.StepDone {
		t.Fatalf("expected StepDone after Run returns, got %s (step=%d)", kind, step)
	}
	if step < 1 {
		t.Fatalf("expected step >= 1, got %d", step)
	}
}

// TestAgentRunner_StateRefNilOk verifies that omitting StateRef (nil) does
// not break execution - the runner must remain backwards compatible.
func TestAgentRunner_StateRefNilOk(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("ok", 1, 1),
		},
	}
	runner := &AgentRunner{model: model} // StateRef intentionally nil
	if _, err := runner.Run(t.Context(), AgentConfig{}, "hi", "m", nil); err != nil {
		t.Fatalf("run: %v", err)
	}
}
