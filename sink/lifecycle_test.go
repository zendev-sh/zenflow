package sink

import (
	"testing"

	"github.com/zendev-sh/zenflow"
)

// TestIsLifecycleEvent verifies the flush-immediate classification.
// Lifecycle events (step start/end, drops, resume, sealed, error,
// workflow-start/end) must return true so Buffered flushes
// immediately on them.
func TestIsLifecycleEvent(t *testing.T) {
	lifecycle := []zenflow.EventType{
		zenflow.EventWorkflowStart,
		zenflow.EventWorkflowEnd,
		zenflow.EventStepStart,
		zenflow.EventStepEnd,
		zenflow.EventStepSkipped,
		zenflow.EventError,
		zenflow.EventMessageDropped,
		zenflow.EventResumeStarted,
		zenflow.EventResumeCompleted,
		zenflow.EventResumeFailed,
		zenflow.EventResumeQueued,
		zenflow.EventTranscriptSealed,
		zenflow.EventPlanReady,
		zenflow.EventCoordinatorInboxMessage,
	}
	for _, et := range lifecycle {
		if !IsLifecycleEvent(zenflow.Event{Type: et}) {
			t.Errorf("IsLifecycleEvent(%q) = false; want true", et)
		}
	}

	// High-frequency narration/delta types must NOT be lifecycle.
	deltas := []zenflow.EventType{
		zenflow.EventCoordinatorNarration,
		zenflow.EventCoordinatorMessage,
		zenflow.EventCoordinatorSynthesis,
		zenflow.EventAgentTurn,
		zenflow.EventMessage,
		zenflow.EventToolCall,
		zenflow.EventAgentInboxDrain,
		zenflow.EventAgentIdle,
		zenflow.EventAgentWake,
		zenflow.EventMaxWakeCyclesWarning,
	}
	for _, et := range deltas {
		if IsLifecycleEvent(zenflow.Event{Type: et}) {
			t.Errorf("IsLifecycleEvent(%q) = true; want false (delta event)", et)
		}
	}
}
