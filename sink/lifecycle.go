package sink

import "github.com/zendev-sh/zenflow"

// IsLifecycleEvent reports whether an event must be flushed immediately
// by a coalescing sink (see Buffered). Lifecycle events are step/workflow
// transitions, resume lifecycle markers, drops, errors, plan-ready,
// transcript-sealed, and coordinator-inbox (reverse-reply) messages -
// any signal where user-visible latency matters more than batching.
// High-frequency delta events (narration, agent_turn, tool_call,
// agent_inbox_drain, idle/wake, coordinator_message/synthesis,
// generic message) are NOT lifecycle: they are safe to coalesce
// within the Buffered window.
// Exported so consumers (TUI adapter, JSONSink) share one
// canonical list rather than re-encoding it.
func IsLifecycleEvent(e zenflow.Event) bool {
	switch e.Type {
	case zenflow.EventWorkflowStart,
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
		zenflow.EventCoordinatorInboxMessage:
		return true
	}
	return false
}
