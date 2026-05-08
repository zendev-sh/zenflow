package exec

import (
	"testing"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/resume"
)

// fakeTranscriptStore records every Append call. Implements
// MetadataSetter so the AgentRunner's metadata seeding path
// is exercised.
type fakeTranscriptStore struct {
	appends  [][]provider.Message
	metadata struct {
		runID, stepID, systemPrompt, model string
		setCount                           int
	}
}

func (f *fakeTranscriptStore) Append(_ string, _ string, msgs []provider.Message) error {
	cp := make([]provider.Message, len(msgs))
	copy(cp, msgs)
	f.appends = append(f.appends, cp)
	return nil
}

func (f *fakeTranscriptStore) Load(_ string, _ string) (*resume.StepTranscript, error) {
	return nil, resume.ErrNoTranscript
}

func (f *fakeTranscriptStore) Delete(_ string, _ string) error { return nil }

func (f *fakeTranscriptStore) SetMetadata(runID, stepID, systemPrompt, model string) {
	f.metadata.runID = runID
	f.metadata.stepID = stepID
	f.metadata.systemPrompt = systemPrompt
	f.metadata.model = model
	f.metadata.setCount++
}

func TestResumeR2_AgentRunnerPersistsTranscript(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("assistant reply", 10, 5),
		},
	}
	store := &fakeTranscriptStore{}

	runner := &AgentRunner{
		model:        model,
		runID:        "run-1",
		stepID:       "step-1",
		transcript:   store,
		modelID:      "goai:mock",
		systemPrompt: "you are helpful",
	}
	_, err := runner.Run(t.Context(), AgentConfig{}, "hello", "mock", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Metadata captured at Run start.
	if store.metadata.setCount == 0 {
		t.Fatalf("SetMetadata never called")
	}
	if store.metadata.runID != "run-1" || store.metadata.stepID != "step-1" {
		t.Fatalf("metadata ids wrong: %+v", store.metadata)
	}
	if store.metadata.systemPrompt != "you are helpful" || store.metadata.model != "goai:mock" {
		t.Fatalf("metadata payload wrong: %+v", store.metadata)
	}

	// At least one Append should have happened (incremental + final).
	if len(store.appends) == 0 {
		t.Fatalf("no Append calls recorded")
	}

	// Reconstruct the full persisted conversation by concatenating all
	// append batches. The first append contains the initial user turn;
	// the last append should contain the assistant response tail.
	var all []provider.Message
	for _, batch := range store.appends {
		all = append(all, batch...)
	}
	// Expect: user("hello") + assistant("assistant reply") at minimum.
	foundUser := false
	foundAssistant := false
	for _, m := range all {
		for _, p := range m.Content {
			if p.Type == provider.PartText && p.Text == "hello" && m.Role == provider.RoleUser {
				foundUser = true
			}
			if p.Type == provider.PartText && p.Text == "assistant reply" && m.Role == provider.RoleAssistant {
				foundAssistant = true
			}
		}
	}
	if !foundUser {
		t.Errorf("user message not persisted; all=%+v", all)
	}
	if !foundAssistant {
		t.Errorf("assistant response not persisted; all=%+v", all)
	}
}

func TestResumeR2_AgentRunnerNoStoreIsNoop(t *testing.T) {
	// When Transcript is nil, Run must not panic and must not take
	// any transcript-related code path.
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("ok", 1, 1),
		},
	}
	runner := &AgentRunner{model: model}
	if _, err := runner.Run(t.Context(), AgentConfig{}, "hi", "mock", nil); err != nil {
		t.Fatalf("Run w/o transcript: %v", err)
	}
}

func TestResumeR2_AgentRunnerMultiTurnPersistsAll(t *testing.T) {
	// Tool-loop scenario: first response is a tool call, second is final text.
	model := &mockModel{
		responses: []*provider.GenerateResult{
			toolCallResult("", 5, 2, provider.ToolCall{
				ID: "tc-1", Name: "read_file", Input: []byte(`{}`),
			}),
			textResult("final answer", 3, 3),
		},
	}
	tools := []goai.Tool{makeTool("read_file", "read a file", "file-content")}
	store := &fakeTranscriptStore{}

	runner := &AgentRunner{
		model:      model,
		runID:      "r",
		stepID:     "s",
		transcript: store,
	}
	result, err := runner.Run(t.Context(), AgentConfig{}, "go", "mock", tools)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil {
		t.Fatalf("nil result")
	}
	if len(store.appends) == 0 {
		t.Fatalf("no appends")
	}

	// Verify the final persisted conversation contains the tool result
	// (role=tool) - proving multi-turn + tool rounds are captured.
	var all []provider.Message
	for _, batch := range store.appends {
		all = append(all, batch...)
	}
	foundTool := false
	for _, m := range all {
		if m.Role == provider.RoleTool {
			foundTool = true
			break
		}
	}
	if !foundTool {
		t.Errorf("tool result not persisted; all=%+v", all)
	}
}
