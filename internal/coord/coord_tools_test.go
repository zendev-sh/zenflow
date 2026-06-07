package coord

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/zendev-sh/zenflow/internal/router"
	"github.com/zendev-sh/zenflow/internal/types"
)

// stubRunner is a minimal RunnerHandle for exercising the coord tool
// factories without a full AgentRunner. Router/Progress are nil so the
// tools take their graceful no-wiring paths.
type stubRunner struct {
	finalized bool
	summary   string
}

func (s *stubRunner) Router() *router.Router            { return nil }
func (s *stubRunner) Progress() types.ProgressSink      { return nil }
func (s *stubRunner) StepID() string                    { return "step" }
func (s *stubRunner) RunID() string                     { return "run" }
func (s *stubRunner) NextForwardSeq() uint64            { return 1 }
func (s *stubRunner) EnsureFinalizeCh() <-chan struct{} { return nil }
func (s *stubRunner) MarkFinalized()                    { s.finalized = true }
func (s *stubRunner) SetFinalSummary(x string)          { s.summary = x }
func (s *stubRunner) FinalSummary() string              { return s.summary }
func (s *stubRunner) Finalized() bool                   { return s.finalized }

func TestForwardToAgentToolDef_Execute(t *testing.T) {
	r := &stubRunner{}
	tool := ForwardToAgentToolDef(r)
	if tool.Name != toolNameForwardToAgent {
		t.Fatalf("name = %q", tool.Name)
	}
	// Missing target -> required error.
	if _, err := tool.Execute(t.Context(), json.RawMessage(`{"target_step_id":""}`)); !errors.Is(err, ErrForwardTargetRequired) {
		t.Errorf("empty target: want ErrForwardTargetRequired, got %v", err)
	}
	// Valid target with nil router -> dropped, no error.
	got, err := tool.Execute(t.Context(), json.RawMessage(`{"target_step_id":"worker","text":"hi"}`))
	if err != nil || got != "dropped: no-router" {
		t.Errorf("nil router: got %q err=%v, want 'dropped: no-router'", got, err)
	}
}

func TestSendMessageToolDef_Execute(t *testing.T) {
	tool := SendMessageToolDef(&stubRunner{})
	if _, err := tool.Execute(t.Context(), json.RawMessage(`{"text":"  "}`)); !errors.Is(err, ErrSendMessageEmpty) {
		t.Errorf("empty text: want ErrSendMessageEmpty, got %v", err)
	}
	got, err := tool.Execute(t.Context(), json.RawMessage(`{"text":"status"}`))
	if err != nil || got != "dropped: no-coordinator" {
		t.Errorf("nil router: got %q err=%v, want 'dropped: no-coordinator'", got, err)
	}
}

func TestNarrateToolDef_Execute(t *testing.T) {
	tool := NarrateToolDef(&stubRunner{})
	if _, err := tool.Execute(t.Context(), json.RawMessage(`{"text":""}`)); !errors.Is(err, ErrNarrateEmpty) {
		t.Errorf("empty text: want ErrNarrateEmpty, got %v", err)
	}
	got, err := tool.Execute(t.Context(), json.RawMessage(`{"text":"note"}`))
	if err != nil || got != "dropped: no-progress-sink" {
		t.Errorf("nil progress: got %q err=%v, want 'dropped: no-progress-sink'", got, err)
	}
}

func TestFinalizeToolDef_Execute(t *testing.T) {
	r := &stubRunner{}
	tool := FinalizeToolDef(r)
	got, err := tool.Execute(t.Context(), json.RawMessage(`{"summary":"all done"}`))
	if err != nil || got != "finalized" {
		t.Fatalf("got %q err=%v, want 'finalized'", got, err)
	}
	if !r.finalized || r.summary != "all done" {
		t.Errorf("finalize side effects: finalized=%v summary=%q", r.finalized, r.summary)
	}
}
