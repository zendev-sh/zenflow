package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/zenflow"
	
)

func zflProviderUsage(in, out int) provider.Usage {
	return provider.Usage{InputTokens: in, OutputTokens: out}
}

func TestNewStdoutSink(t *testing.T) {
	s := NewStdoutSink()
	if s == nil {
		t.Fatal("NewStdoutSink() returned nil")
	}
}

func TestStdoutSink_WorkflowStart(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:      zenflow.EventWorkflowStart,
		Timestamp: time.Now(),
		Message:   "my-workflow",
	})

	got := buf.String()
	if !strings.Contains(got, "my-workflow") {
		t.Errorf("output = %q, expected to contain workflow name", got)
	}
}

func TestStdoutSink_StepStart(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:      zenflow.EventStepStart,
		Timestamp: time.Now(),
		StepID:    "design",
		AgentName: "coder",
		Data:      map[string]any{"index": 0, "total": 3},
	})

	got := buf.String()
	if !strings.Contains(got, "design") {
		t.Errorf("output = %q, expected to contain step ID", got)
	}
	if !strings.Contains(got, "coder") {
		t.Errorf("output = %q, expected to contain agent name", got)
	}
	if !strings.Contains(got, "1/3") {
		t.Errorf("output = %q, expected to contain '1/3'", got)
	}
}

func TestStdoutSink_StepStartDefaultAgent(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:      zenflow.EventStepStart,
		Timestamp: time.Now(),
		StepID:    "review",
		AgentName: "", // No agent → should show "default".
		Data:      map[string]any{"index": 1, "total": 2},
	})

	got := buf.String()
	if !strings.Contains(got, "default") {
		t.Errorf("output = %q, expected to contain 'default' for empty agent", got)
	}
}

func TestStdoutSink_StepEnd(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:      zenflow.EventStepEnd,
		Timestamp: time.Now(),
		StepID:    "implement",
		Duration:  5 * time.Second,
	})

	got := buf.String()
	if !strings.Contains(got, "implement") {
		t.Errorf("output = %q, expected to contain step ID", got)
	}
	if !strings.Contains(got, "completed") {
		t.Errorf("output = %q, expected to contain 'completed'", got)
	}
}

func TestStdoutSink_StepSkipped(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:      zenflow.EventStepSkipped,
		Timestamp: time.Now(),
		StepID:    "optional-step",
	})

	got := buf.String()
	if !strings.Contains(got, "optional-step") {
		t.Errorf("output = %q, expected to contain step ID", got)
	}
	if !strings.Contains(got, "skipped") {
		t.Errorf("output = %q, expected to contain 'skipped'", got)
	}
}

// TestCLIStepSkipped_GlyphIsMutedO covers ZF8F.S4.7 - the CLI glyph for
// EventStepSkipped was normalised from `⊘` Warning yellow to `○` Muted
// grey per §9.A.1. Asserts glyph codepoint + Muted escape + absence of
// the old `⊘` + Warning escape.
func TestCLIStepSkipped_GlyphIsMutedO(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:      zenflow.EventStepSkipped,
		Timestamp: time.Now(),
		StepID:    "optional-step",
	})

	got := buf.String()
	if !strings.Contains(got, "○") {
		t.Errorf("expected muted `○` glyph, got %q", got)
	}
	if strings.Contains(got, "⊘") {
		t.Errorf("expected `⊘` removed, got %q", got)
	}
	if !strings.Contains(got, "optional-step") {
		t.Errorf("expected step ID in output, got %q", got)
	}
	if !strings.Contains(got, "skipped") {
		t.Errorf("expected `skipped` in output, got %q", got)
	}
}

func TestStdoutSink_Error(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:      zenflow.EventError,
		Timestamp: time.Now(),
		StepID:    "broken-step",
		Error:     errTest,
	})

	got := buf.String()
	if !strings.Contains(got, "broken-step") {
		t.Errorf("output = %q, expected to contain step ID", got)
	}
	if !strings.Contains(got, "test error") {
		t.Errorf("output = %q, expected to contain error message", got)
	}
}

func TestStdoutSink_WorkflowEnd(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:      zenflow.EventWorkflowEnd,
		Timestamp: time.Now(),
		Duration:  30 * time.Second,
	})

	got := buf.String()
	if !strings.Contains(got, "completed") {
		t.Errorf("output = %q, expected to contain 'completed'", got)
	}
}

func TestStdoutSink_UnhandledEventNoOutput(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	// Events like EventAgentTurn, EventToolCall, EventMessage are not handled.
	s.OnEvent(t.Context(), zenflow.Event{
		Type:      zenflow.EventToolCall,
		Timestamp: time.Now(),
	})

	if buf.Len() != 0 {
		t.Errorf("expected no output for unhandled event, got %q", buf.String())
	}
}

// errTest is a simple test error.
type testError struct{}

func (e testError) Error() string { return "test error" }

var errTest error = testError{}

func TestStdoutSink_CoordinatorNarration(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:    zenflow.EventCoordinatorNarration,
		StepID:  "s1",
		Message: "Step completed nicely.",
	})

	got := buf.String()
	if !strings.Contains(got, "[s1] Step completed nicely.") {
		t.Errorf("output = %q, expected [stepID] prefix + narration text", got)
	}
	if !strings.Contains(got, "≋") {
		t.Errorf("output = %q, expected narration prefix '≋'", got)
	}
}

func TestStdoutSink_CoordinatorSynthesis(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:    zenflow.EventCoordinatorSynthesis,
		Message: "All steps completed successfully.",
	})

	got := buf.String()
	if !strings.Contains(got, "All steps completed successfully.") {
		t.Errorf("output = %q, expected synthesis text", got)
	}
	if !strings.Contains(got, "Summary") {
		t.Errorf("output = %q, expected 'Summary' header", got)
	}
}

func TestStdoutSink_OnOutput(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnOutput(t.Context(), zenflow.Output{Delta: "hello "})
	s.OnOutput(t.Context(), zenflow.Output{Delta: "world", Done: true})

	got := buf.String()
	want := "hello world\n"
	if got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// Coordinator no longer streams via OnOutput - it buffers response and emits
// via OnEvent (EventCoordinatorNarration, EventCoordinatorMessage, EventCoordinatorSynthesis).
// OnOutput is only used for agent output (--verbose --stream).

func TestStdoutSink_CoordinatorMessage(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:    zenflow.EventCoordinatorMessage,
		StepID:  "api-server",
		Message: "Database schema is ready.",
	})

	got := buf.String()
	if !strings.Contains(got, "⇢ [api-server]") {
		t.Errorf("expected ⇢ [api-server], got %q", got)
	}
	if !strings.Contains(got, "Database schema is ready.") {
		t.Errorf("expected message content, got %q", got)
	}
}

func TestStdoutSink_CoordinatorInboxMessage(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:    zenflow.EventCoordinatorInboxMessage,
		Message: "reply from resumed step",
		Data: map[string]any{
			"from": "team-pro",
			"type": "resume_reply",
		},
	})

	got := buf.String()
	if !strings.Contains(got, "≋ [coordinator] from=team-pro") {
		t.Errorf("expected '≋ [coordinator] from=team-pro' prefix, got %q", got)
	}
	if !strings.Contains(got, "(resumed):") {
		t.Errorf("expected '(resumed):' marker, got %q", got)
	}
	if !strings.Contains(got, "reply from resumed step") {
		t.Errorf("expected content, got %q", got)
	}
}

func TestStdoutSink_CoordinatorInboxMessage_TruncatesLongContent(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	long := strings.Repeat("x", 400)
	s.OnEvent(t.Context(), zenflow.Event{
		Type:    zenflow.EventCoordinatorInboxMessage,
		Message: long,
		Data:    map[string]any{"from": "team-con", "type": "resume_reply"},
	})

	got := buf.String()
	if !strings.Contains(got, "...") {
		t.Errorf("expected truncation ellipsis, got %q", got)
	}
	if strings.Contains(got, long) {
		t.Errorf("expected content to be truncated, but full 400-char string present")
	}
}

func TestStdoutSink_CoordinatorInboxMessage_MissingFrom(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:    zenflow.EventCoordinatorInboxMessage,
		Message: "m",
	})
	if !strings.Contains(buf.String(), "from=?") {
		t.Errorf("expected 'from=?' fallback, got %q", buf.String())
	}
}

func TestStdoutSink_OnOutput_ReasoningIncludesStepID(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnOutput(t.Context(), zenflow.Output{StepID: "team-pro", Reasoning: true, Delta: "thinking"})

	got := buf.String()
	if !strings.Contains(got, "[team-pro] Thinking...") {
		t.Errorf("reasoning header missing step id; got %q", got)
	}
}

func TestStdoutSink_OnOutput_DoneSingleNewline(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	// Summary is now single-line - should end with one newline, not two.
	s.OnOutput(t.Context(), zenflow.Output{AgentID: "coordinator", Delta: "Sum."})
	s.OnOutput(t.Context(), zenflow.Output{AgentID: "coordinator", Done: true})

	got := buf.String()
	if !strings.HasSuffix(got, "Sum.\n") {
		t.Errorf("output = %q, expected single newline after Done", got)
	}
}

func TestStdoutSink_OnOutput_NonCoordinatorNoDoneExtraNewline(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	// Non-coordinator done should only emit one newline.
	s.OnOutput(t.Context(), zenflow.Output{AgentID: "agent-1", Delta: "hi"})
	s.OnOutput(t.Context(), zenflow.Output{AgentID: "agent-1", Done: true})

	got := buf.String()
	if strings.HasSuffix(got, "\n\n") {
		t.Errorf("non-coordinator Done should not emit extra newline, got %q", got)
	}
}

// --- OnEvent new branches ---

func TestStdoutSink_WorkflowEnd_WithName(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	// Set workflow name first.
	s.OnEvent(t.Context(), zenflow.Event{
		Type:    zenflow.EventWorkflowStart,
		Message: "my-wf",
	})

	buf.Reset()

	s.OnEvent(t.Context(), zenflow.Event{
		Type:     zenflow.EventWorkflowEnd,
		Duration: 10 * time.Second,
	})

	got := buf.String()
	if !strings.Contains(got, "[my-wf] completed") {
		t.Errorf("output = %q, expected workflow name in WorkflowEnd", got)
	}
}

func TestStdoutSink_WorkflowEnd_NoName(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	// No WorkflowStart → no name tracked.
	s.OnEvent(t.Context(), zenflow.Event{
		Type:     zenflow.EventWorkflowEnd,
		Duration: 5 * time.Second,
	})

	got := buf.String()
	if !strings.Contains(got, "Workflow completed") {
		t.Errorf("output = %q, expected generic 'Workflow completed'", got)
	}
}

func TestStdoutSink_CoordinatorSynthesis_WithName(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	// Set workflow name.
	s.OnEvent(t.Context(), zenflow.Event{
		Type:    zenflow.EventWorkflowStart,
		Message: "named-wf",
	})

	buf.Reset()

	s.OnEvent(t.Context(), zenflow.Event{
		Type:    zenflow.EventCoordinatorSynthesis,
		Message: "All done.",
	})

	got := buf.String()
	if !strings.Contains(got, "≋ [named-wf] Summary: All done.") {
		t.Errorf("output = %q, expected '≋ [named-wf] Summary:' format", got)
	}
}

func TestStdoutSink_WorkflowStartTracksName(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:    zenflow.EventWorkflowStart,
		Message: "tracked-name",
	})

	if !strings.Contains(buf.String(), "tracked-name") {
		t.Errorf("output = %q, expected workflow name", buf.String())
	}
}

// --- Coverage gap tests ---

func TestStdoutSink_WithShowPlan(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf).WithShowPlan(true)
	if s == nil {
		t.Fatal("WithShowPlan returned nil")
	}
}

func TestStdoutSink_WithVerbose(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf).WithVerbose(true)
	if s == nil {
		t.Fatal("WithVerbose returned nil")
	}
}

func TestStdoutSink_EventPlanReady_ShowPlan(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf).WithShowPlan(true)

	wf := &zenflow.Workflow{
		Name:  "plan-test",
		Steps: []zenflow.Step{{ID: "s1"}},
	}
	s.OnEvent(t.Context(), zenflow.Event{
		Type: zenflow.EventPlanReady,
		Data: map[string]any{"workflow": wf},
	})

	got := buf.String()
	if !strings.Contains(got, "s1") {
		t.Errorf("output = %q, expected DAG with step s1", got)
	}
}

func TestStdoutSink_EventPlanReady_NoShowPlan(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf) // showPlan=false by default

	wf := &zenflow.Workflow{
		Name:  "plan-test",
		Steps: []zenflow.Step{{ID: "s1"}},
	}
	s.OnEvent(t.Context(), zenflow.Event{
		Type: zenflow.EventPlanReady,
		Data: map[string]any{"workflow": wf},
	})

	if buf.Len() != 0 {
		t.Errorf("expected no output when showPlan=false, got %q", buf.String())
	}
}

func TestStdoutSink_OnOutput_ReasoningHeader(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	// First reasoning output should emit "Thinking..." header.
	s.OnOutput(t.Context(), zenflow.Output{Reasoning: true})

	got := buf.String()
	if !strings.Contains(got, "Thinking") {
		t.Errorf("output = %q, expected 'Thinking...' header", got)
	}
}

func TestStdoutSink_OnOutput_VerboseReasoningContent(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf).WithVerbose(true)

	// Reasoning with verbose should emit delta content.
	s.OnOutput(t.Context(), zenflow.Output{Reasoning: true, Delta: "thinking step 1"})

	got := buf.String()
	if !strings.Contains(got, "thinking step 1") {
		t.Errorf("output = %q, expected reasoning content", got)
	}
}

func TestStdoutSink_OnOutput_ReasoningToTextTransition(t *testing.T) {
	// Header-only reasoning → text: header is line-terminated already,
	// no extra blank line should be emitted before the text.
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)
	s.OnOutput(t.Context(), zenflow.Output{Reasoning: true})
	buf.Reset()
	s.OnOutput(t.Context(), zenflow.Output{Delta: "text output"})
	got := buf.String()
	if strings.HasPrefix(got, "\n") {
		t.Errorf("output = %q, expected no leading newline (header already terminated)", got)
	}
	if !strings.Contains(got, "text output") {
		t.Errorf("output = %q, expected text content", got)
	}

	// Reasoning with un-terminated delta → text: must emit a closing
	// newline so the text renders on a fresh line.
	var buf2 bytes.Buffer
	s2 := NewStdoutSinkTo(&buf2).WithVerbose(true)
	s2.OnOutput(t.Context(), zenflow.Output{Reasoning: true, Delta: "thought"})
	buf2.Reset()
	s2.OnOutput(t.Context(), zenflow.Output{Delta: "text"})
	if !strings.HasPrefix(buf2.String(), "\n") {
		t.Errorf("output = %q, expected leading newline (delta was un-terminated)", buf2.String())
	}
}

func TestSink_C_ColorEnabled(t *testing.T) {
	orig := ColorEnabled()
	defer func() { SetColorEnabled(orig) }()

	SetColorEnabled(true)
	got := C(Cyan, "hello")
	if got == "hello" {
		t.Error("expected ANSI-wrapped output when color enabled")
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("output = %q, expected to contain 'hello'", got)
	}
	if !strings.HasSuffix(got, Reset) {
		t.Errorf("output = %q, expected to end with Reset code", got)
	}
}

// Fix B: EventMessageSent renders as `⇠ [sender] sent to <to>: <preview>`
// - outbound side of message visibility, matching the ⇠ = sent convention.
func TestStdoutSink_MessageSent(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:    zenflow.EventMessageSent,
		StepID:  "asker-1",
		Message: "QUESTION_1: capital of France?",
		Data: map[string]any{
			"to":   "coordinator",
			"text": "QUESTION_1: capital of France?",
		},
	})

	got := buf.String()
	if !strings.Contains(got, "⇠ [asker-1]") {
		t.Errorf("expected ⇠ [asker-1], got %q", got)
	}
	if !strings.Contains(got, "sent to coordinator:") {
		t.Errorf("expected 'sent to coordinator:', got %q", got)
	}
	if !strings.Contains(got, "QUESTION_1: capital of France?") {
		t.Errorf("expected text in output, got %q", got)
	}
}

// Truncates long text to keep the line readable.
func TestStdoutSink_MessageSent_LongTextTruncates(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	long := strings.Repeat("a", 300)
	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventMessageSent,
		StepID: "step",
		Data:   map[string]any{"to": "coordinator", "text": long},
	})

	got := buf.String()
	if !strings.Contains(got, "...") {
		t.Errorf("expected '...' truncation marker, got %q", got)
	}
	if strings.Count(got, "a") > 250 {
		t.Errorf("expected truncated to ~240 chars, got %d 'a' chars", strings.Count(got, "a"))
	}
}

// Empty Data["text"] falls back to event.Message.
func TestStdoutSink_MessageSent_EmptyTextFallsBackToMessage(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:    zenflow.EventMessageSent,
		StepID:  "step",
		Message: "fallback-text",
		Data:    map[string]any{"to": "coordinator"},
	})
	if !strings.Contains(buf.String(), "fallback-text") {
		t.Errorf("expected text fallback to event.Message ('fallback-text'), got %q", buf.String())
	}
}

// Empty Data["to"] falls back to "?".
func TestStdoutSink_MessageSent_EmptyToFallsBack(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventMessageSent,
		StepID: "step",
		Data:   map[string]any{"text": "hi"},
	})
	if !strings.Contains(buf.String(), "sent to ?:") {
		t.Errorf("expected 'sent to ?:' fallback, got %q", buf.String())
	}
}

func TestStdoutSink_AgentInboxDrain(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:    zenflow.EventAgentInboxDrain,
		StepID:  "worker",
		Message: "[coordinator]: context update",
		Data:    map[string]any{"from": "coordinator"},
	})

	got := buf.String()
	if !strings.Contains(got, "⇢ [worker]") {
		t.Errorf("expected ⇢ [worker], got %q", got)
	}
	if !strings.Contains(got, "received from coordinator") {
		t.Errorf("expected 'received from coordinator', got %q", got)
	}
}

// EventMessageDropped rendering - every router-side
// or workflow-abort drop must surface so the "zero silent drops"
// contract is observable on the CLI.
func TestStdoutSink_MessageDropped(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:    zenflow.EventMessageDropped,
		StepID:  "downstream",
		Message: "[coord -> downstream]: hi",
		Data:    map[string]any{"from": "coord", "reason": "target-terminal"},
	})

	got := buf.String()
	if !strings.Contains(got, "msg-dropped") {
		t.Errorf("expected 'msg-dropped' label, got %q", got)
	}
	if !strings.Contains(got, "[downstream]") {
		t.Errorf("expected '[downstream]', got %q", got)
	}
	if !strings.Contains(got, "from=coord") {
		t.Errorf("expected 'from=coord', got %q", got)
	}
	if !strings.Contains(got, "reason=target-terminal") {
		t.Errorf("expected 'reason=target-terminal', got %q", got)
	}
}

func TestStdoutSink_MessageDropped_DefaultsForMissingFields(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventMessageDropped,
		StepID: "x",
		Data:   map[string]any{}, // no from, no reason
	})

	got := buf.String()
	if !strings.Contains(got, "from=?") {
		t.Errorf("expected fallback 'from=?', got %q", got)
	}
	if !strings.Contains(got, "reason=unknown") {
		t.Errorf("expected fallback 'reason=unknown', got %q", got)
	}
}

// workflow-cancelled drops are softened to INFO format
// (○ Dim) instead of WARN (⚠) since they're an expected timing
// artifact at workflow shutdown (coord still has unprocessed
// mailbox messages when executor cancels coordCtx). All other drop
// reasons remain WARN since they indicate real routing/capacity
// issues mid-flight.
func TestStdoutSink_MessageDropped_WorkflowCancelledIsInfo(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventMessageDropped,
		StepID: "coordinator",
		Data:   map[string]any{"from": "verdict", "reason": "workflow-cancelled"},
	})

	got := buf.String()
	if !strings.Contains(got, "○ msg-dropped") {
		t.Errorf("expected INFO marker '○' for workflow-cancelled drop, got %q", got)
	}
	if strings.Contains(got, "⚠ msg-dropped") {
		t.Errorf("workflow-cancelled drop should NOT use WARN marker '⚠', got %q", got)
	}
	if !strings.Contains(got, "expected at shutdown") {
		t.Errorf("expected explanatory suffix 'expected at shutdown', got %q", got)
	}
}

// non-cancellation drops remain WARN (real bugs).
func TestStdoutSink_MessageDropped_OtherReasonsRemainWarn(t *testing.T) {
	for _, reason := range []string{"unknown-step", "coord-down", "cap-exhaustion", "target-terminal"} {
		var buf bytes.Buffer
		s := NewStdoutSinkTo(&buf)

		s.OnEvent(t.Context(), zenflow.Event{
			Type:   zenflow.EventMessageDropped,
			StepID: "coordinator",
			Data:   map[string]any{"from": "x", "reason": reason},
		})

		got := buf.String()
		if !strings.Contains(got, "⚠ msg-dropped") {
			t.Errorf("reason=%q expected WARN marker '⚠', got %q", reason, got)
		}
		if strings.Contains(got, "○ msg-dropped") {
			t.Errorf("reason=%q should NOT be softened to INFO, got %q", reason, got)
		}
	}
}

func TestStdoutSink_AgentInboxDrain_UnknownFrom(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventAgentInboxDrain,
		StepID: "w",
		Data:   map[string]any{},
	})

	if !strings.Contains(buf.String(), "received from ?") {
		t.Errorf("expected fallback '?' for missing from, got %q", buf.String())
	}
}

func TestStdoutSink_Message_WithStep(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:    zenflow.EventMessage,
		StepID:  "s1",
		Message: "isolation not configured",
	})

	got := buf.String()
	if !strings.Contains(got, "⚠ [s1]") || !strings.Contains(got, "isolation not configured") {
		t.Errorf("unexpected output %q", got)
	}
}

func TestStdoutSink_Message_NoStep(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:    zenflow.EventMessage,
		Message: "global warning",
	})

	got := buf.String()
	if !strings.Contains(got, "⚠") || !strings.Contains(got, "global warning") {
		t.Errorf("unexpected output %q", got)
	}
	if strings.Contains(got, "⚠ [") {
		t.Errorf("should not show step bracket when no StepID: %q", got)
	}
}

func TestStdoutSink_ToolCall_StartPhase_Ignored(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventToolCall,
		StepID: "s",
		Data:   map[string]any{"phase": "start", "tool_name": "read"},
	})

	if buf.Len() != 0 {
		t.Errorf("start phase should not render, got %q", buf.String())
	}
}

func TestStdoutSink_ToolCall_EndOK(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:     zenflow.EventToolCall,
		StepID:   "s",
		Duration: 12 * time.Millisecond,
		Data:     map[string]any{"phase": "end", "tool_name": "bash"},
	})

	got := buf.String()
	if !strings.Contains(got, "⚙ [s] bash") {
		t.Errorf("expected '⚙ [s] bash', got %q", got)
	}
	if !strings.Contains(got, "12ms") {
		t.Errorf("expected duration, got %q", got)
	}
}

func TestStdoutSink_ToolCall_EndError(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventToolCall,
		StepID: "s",
		Data:   map[string]any{"phase": "end", "tool_name": "read"},
		Error:  errBoom,
	})

	got := buf.String()
	// Tool header shape: status glyph (×) separate from
	// tool icon (◇ for read) + stepID + name.
	if !strings.Contains(got, "× ◇ [s] read") {
		t.Errorf("expected '× ◇ [s] read', got %q", got)
	}
	if !strings.Contains(got, "failed") {
		t.Errorf("expected 'failed', got %q", got)
	}
}

func TestStdoutSink_AgentTurn_NonVerbose_Skipped(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventAgentTurn,
		StepID: "s",
		Data:   map[string]any{"phase": "response"},
	})

	if buf.Len() != 0 {
		t.Errorf("non-verbose should skip, got %q", buf.String())
	}
}

func TestStdoutSink_AgentTurn_VerboseRequestPhase_Skipped(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf).WithVerbose(true)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventAgentTurn,
		StepID: "s",
		Data:   map[string]any{"phase": "request"},
	})

	if buf.Len() != 0 {
		t.Errorf("request phase should skip even verbose, got %q", buf.String())
	}
}

func TestStdoutSink_AgentTurn_VerboseResponse_WithTokens(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf).WithVerbose(true)

	usage := zflProviderUsage(123, 45)
	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventAgentTurn,
		StepID: "s",
		Data:   map[string]any{"phase": "response"},
		Tokens: &usage,
	})

	got := buf.String()
	if !strings.Contains(got, "Σ [s] turn") {
		t.Errorf("expected 'Σ [s] turn', got %q", got)
	}
	if !strings.Contains(got, "in=123") || !strings.Contains(got, "out=45") {
		t.Errorf("expected token counts, got %q", got)
	}
}

// errBoom + zflProviderUsage are shared helpers so tests can isolate the
// provider.Usage type import.
type errBoomType struct{}

func (errBoomType) Error() string { return "boom" }

var errBoom = errBoomType{}

func TestStdoutSink_ToolIcons(t *testing.T) {
	cases := []struct {
		tool string
		icon string
	}{
		{"read", "◇"},
		{"edit", "✎"},
		{"write", "✐"},
		{"bash", "⚙"},
		{"grep", "⊙"},
		{"glob", "⛶"},
		{"fetch", "⇄"},
		{"task", "✦"},
		{"ls", "⊞"},
		{"unknown-tool", "◆"}, // fallback
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			var buf bytes.Buffer
			s := NewStdoutSinkTo(&buf)
			s.OnEvent(t.Context(), zenflow.Event{
				Type:   zenflow.EventToolCall,
				StepID: "s",
				Data:   map[string]any{"phase": "end", "tool_name": tc.tool},
			})
			got := buf.String()
			if !strings.Contains(got, tc.icon) {
				t.Errorf("tool=%s: expected icon %q in %q", tc.tool, tc.icon, got)
			}
		})
	}
}

func TestStdoutSink_AgentIdleAndWake(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)

	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventAgentIdle,
		StepID: "stp1",
		Data:   map[string]any{"unread_count": 0},
	})
	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventAgentWake,
		StepID: "stp1",
		Data:   map[string]any{"message_count": 3, "cycle": 2},
	})
	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventMaxWakeCyclesWarning,
		StepID: "stp1",
		Data:   map[string]any{"current_cycle": 8, "max_cycles": 10, "unread_remaining": 2},
	})

	got := buf.String()
	for _, want := range []string{
		"[stp1] idle",
		"[stp1] wake",
		"msgs=3 cycle=2",
		"wake-cycles approaching cap",
		"cycle=8/10 unread=2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output: %s", want, got)
		}
	}
}

// resume-mechanism event rendering - every resume
// lifecycle transition must be visible in the CLI so operators can
// trace auto-resumed terminated steps end-to-end.
func TestStdoutSink_ResumeStarted(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)
	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventResumeStarted,
		StepID: "team-con",
		Data:   map[string]any{"resumeID": "resume_abc", "from": "coord"},
	})
	got := buf.String()
	for _, want := range []string{"↺", "[team-con]", "resumed by coord"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output: %s", want, got)
		}
	}
}

func TestStdoutSink_ResumeQueued(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)
	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventResumeQueued,
		StepID: "team-con",
		Data:   map[string]any{"resumeID": "r", "from": "coord"},
	})
	got := buf.String()
	for _, want := range []string{"⋯", "[team-con]", "resume queued by coord"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output: %s", want, got)
		}
	}
}

func TestStdoutSink_ResumeCompleted(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)
	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventResumeCompleted,
		StepID: "team-con",
		Data:   map[string]any{"resumeID": "resume_abc", "durationMs": int64(1200)},
	})
	got := buf.String()
	for _, want := range []string{"↻", "[team-con]", "resume done", "1200ms"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output: %s", want, got)
		}
	}
}

func TestStdoutSink_ResumeFailed(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)
	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventResumeFailed,
		StepID: "team-con",
		Data:   map[string]any{"resumeID": "r", "reason": "workflow-shutdown"},
	})
	got := buf.String()
	for _, want := range []string{"⚠", "[team-con]", "resume failed", "reason=workflow-shutdown"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output: %s", want, got)
		}
	}
}

// VA-4b - EventResumeQueued carries activeResumeID in the tail.
func TestStdoutSink_ResumeQueuedCarriesActiveID(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)
	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventResumeQueued,
		StepID: "team-con",
		Data: map[string]any{
			"resumeID":       "r-queued",
			"from":           "coord",
			"activeResumeID": "r-active-123",
		},
	})
	got := buf.String()
	for _, want := range []string{"⋯", "[team-con]", "resume queued by coord", "active=r-active-123"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output: %s", want, got)
		}
	}
}

// G4 - EventTranscriptSealed renders with reason + scissors marker.
func TestStdoutSink_TranscriptSealed(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)
	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventTranscriptSealed,
		StepID: "team-con",
		Data:   map[string]any{"reason": "transcript-too-large"},
	})
	got := buf.String()
	for _, want := range []string{"✂", "[team-con]", "transcript sealed", "reason=transcript-too-large"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output: %s", want, got)
		}
	}
}

// Coverage: each of these events has an `if from == ""` / `if reason == ""`
// guard that substitutes a default sentinel. Exercise those branches.

func TestStdoutSink_ResumeStarted_EmptyFromFallsBackToQuestionMark(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)
	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventResumeStarted,
		StepID: "s",
		Data:   map[string]any{"resumeID": "r"},
	})
	if !strings.Contains(buf.String(), "resumed by ?") {
		t.Errorf("missing '?' fallback: %s", buf.String())
	}
}

func TestStdoutSink_ResumeQueued_EmptyFromFallsBackToQuestionMark(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)
	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventResumeQueued,
		StepID: "s",
		Data:   map[string]any{"resumeID": "r"},
	})
	if !strings.Contains(buf.String(), "resume queued by ?") {
		t.Errorf("missing '?' fallback: %s", buf.String())
	}
}

func TestStdoutSink_TranscriptSealed_EmptyReasonFallsBackToUnknown(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)
	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventTranscriptSealed,
		StepID: "s",
		Data:   map[string]any{},
	})
	if !strings.Contains(buf.String(), "reason=unknown") {
		t.Errorf("missing 'unknown' fallback: %s", buf.String())
	}
}

func TestStdoutSink_ResumeFailed_EmptyReasonFallsBackToUnknown(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf)
	s.OnEvent(t.Context(), zenflow.Event{
		Type:   zenflow.EventResumeFailed,
		StepID: "s",
		Data:   map[string]any{},
	})
	if !strings.Contains(buf.String(), "reason=unknown") {
		t.Errorf("missing 'unknown' fallback: %s", buf.String())
	}
}

// =============================================================================
// WithStdoutShowPlan / WithStdoutVerbose option funcs - 0% coverage fixed
// =============================================================================

// TestWithStdoutShowPlan_OptionSetsField verifies that the
// WithStdoutShowPlan option func sets showPlan=true on the sink when
// passed to NewStdoutSink.
func TestWithStdoutShowPlan_OptionSetsField(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf, WithStdoutShowPlan(true))
	// A plan_ready event should render the DAG when showPlan=true.
	wf := &zenflow.Workflow{
		Name:  "opt-test",
		Steps: []zenflow.Step{{ID: "opt-step"}},
	}
	s.OnEvent(t.Context(), zenflow.Event{
		Type: zenflow.EventPlanReady,
		Data: map[string]any{"workflow": wf},
	})
	got := buf.String()
	if !strings.Contains(got, "opt-step") {
		t.Errorf("WithStdoutShowPlan(true) did not enable DAG rendering; got %q", got)
	}
}

// TestWithStdoutShowPlan_FalseDoesNotRender verifies that passing
// WithStdoutShowPlan(false) suppresses DAG rendering (matches the
// no-options default, but exercises the option code path explicitly).
func TestWithStdoutShowPlan_FalseDoesNotRender(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf, WithStdoutShowPlan(false))
	wf := &zenflow.Workflow{
		Name:  "no-plan",
		Steps: []zenflow.Step{{ID: "hidden-step"}},
	}
	s.OnEvent(t.Context(), zenflow.Event{
		Type: zenflow.EventPlanReady,
		Data: map[string]any{"workflow": wf},
	})
	if buf.Len() != 0 {
		t.Errorf("WithStdoutShowPlan(false) should suppress DAG; got %q", buf.String())
	}
}

// TestWithStdoutVerbose_OptionSetsField verifies that the
// WithStdoutVerbose option func sets verbose=true on the sink when
// passed to NewStdoutSink.
func TestWithStdoutVerbose_OptionSetsField(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf, WithStdoutVerbose(true))
	// Verbose mode must render reasoning delta content.
	s.OnOutput(t.Context(), zenflow.Output{
		StepID:    "v-step",
		Reasoning: true,
		Delta:     "verbose-reasoning-delta",
	})
	got := buf.String()
	if !strings.Contains(got, "verbose-reasoning-delta") {
		t.Errorf("WithStdoutVerbose(true) did not enable reasoning content; got %q", got)
	}
}

// TestWithStdoutVerbose_FalseHidesReasoning verifies that passing
// WithStdoutVerbose(false) suppresses reasoning deltas (exercises the
// option code path even though false is the default).
func TestWithStdoutVerbose_FalseHidesReasoning(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf, WithStdoutVerbose(false))
	s.OnOutput(t.Context(), zenflow.Output{
		StepID:    "v-step",
		Reasoning: true,
		Delta:     "hidden-delta",
	})
	got := buf.String()
	if strings.Contains(got, "hidden-delta") {
		t.Errorf("WithStdoutVerbose(false) should hide reasoning delta; got %q", got)
	}
}

// TestNewStdoutSinkTo_WithMultipleOptions verifies that NewStdoutSinkTo
// applies all supplied options (exercises the for-range loop body that
// was 0% because no test passed more than zero options via the
// NewStdoutSinkTo function-option path).
func TestNewStdoutSinkTo_WithMultipleOptions(t *testing.T) {
	var buf bytes.Buffer
	s := NewStdoutSinkTo(&buf,
		WithStdoutShowPlan(true),
		WithStdoutVerbose(true),
	)
	// showPlan=true: plan_ready renders DAG.
	wf := &zenflow.Workflow{
		Name:  "multi-opt",
		Steps: []zenflow.Step{{ID: "multi-step"}},
	}
	s.OnEvent(t.Context(), zenflow.Event{
		Type: zenflow.EventPlanReady,
		Data: map[string]any{"workflow": wf},
	})
	if !strings.Contains(buf.String(), "multi-step") {
		t.Errorf("showPlan option not applied; got %q", buf.String())
	}
	buf.Reset()
	// verbose=true: reasoning delta is rendered.
	s.OnOutput(t.Context(), zenflow.Output{
		StepID:    "multi-step",
		Reasoning: true,
		Delta:     "multi-delta",
	})
	if !strings.Contains(buf.String(), "multi-delta") {
		t.Errorf("verbose option not applied; got %q", buf.String())
	}
}
