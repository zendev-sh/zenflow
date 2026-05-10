// Package exec - executor_coord.go contains the executor-side coord
// wiring: AgentState/activeSteps registry helpers, push helpers
// (pushCoordEvent / pushStepEventToCoord), wake signalling
// (signalCoordWake), reverse-reply drain
// (drainCoordReverseReplies), the coordStepID constant, and the
// nestedSuppressLifecycleSink wrapper used to keep nested executor
// runs from spamming the parent's lifecycle sink. The runtime coord
// runner itself lives in coord_factory.go / coord_lib.go.
package exec

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/zendev-sh/goai"

	"github.com/zendev-sh/zenflow/internal/router"
	"github.com/zendev-sh/zenflow/internal/spec"
	"github.com/zendev-sh/zenflow/internal/types"
)

// AgentState returns the *goai.AgentState registered for stepID, or nil if
// the step has not been started yet (or never existed). Safe to call
// concurrently with Run / runStep - the underlying *goai.AgentState is
// itself goroutine-safe (atomic read).
func (e *Executor) AgentState(stepID string) *goai.AgentState {
	e.agentStatesMu.Lock()
	defer e.agentStatesMu.Unlock()
	return e.agentStates[stepID]
}

// registerAgentState records state under stepID and marks the step as
// active. Internal helper used by runStep when constructing the per-step
// AgentRunner. Each call should be paired with a deferred
// unregisterAgentState(stepID) so the active set stays consistent - leaks
// would cause the DeliveryEngine to keep polling completed steps.
func (e *Executor) registerAgentState(stepID string, state *goai.AgentState) {
	e.agentStatesMu.Lock()
	defer e.agentStatesMu.Unlock()
	hint := 0
	if e.Workflow != nil {
		hint = len(e.Workflow.Steps)
	}
	if e.agentStates == nil {
		e.agentStates = make(map[string]*goai.AgentState, hint)
	}
	if e.activeSteps == nil {
		e.activeSteps = make(map[string]struct{}, hint)
	}
	e.agentStates[stepID] = state
	e.activeSteps[stepID] = struct{}{}
}

// unregisterAgentState removes stepID from the active set. The
// agentStates handle is intentionally retained so a late observer (e.g.
// in-flight Engine tick) can still call AgentState(stepID) and read
// StepIdle rather than a confusing nil.
func (e *Executor) unregisterAgentState(stepID string) {
	e.agentStatesMu.Lock()
	defer e.agentStatesMu.Unlock()
	delete(e.activeSteps, stepID)
}

// ActiveSteps returns a snapshot of currently-running step IDs. Used by
// the DeliveryEngine to decide which mailboxes to poll each
// tick. Returns an empty slice (never nil) so callers can range without
// nil-checks.
func (e *Executor) ActiveSteps() []string {
	e.agentStatesMu.Lock()
	defer e.agentStatesMu.Unlock()
	if len(e.activeSteps) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(e.activeSteps))
	for id := range e.activeSteps {
		out = append(out, id)
	}
	return out
}

// StepModelString returns the workflow model string recorded for stepID
// when that step ran, or empty if the step never ran under this Executor.
// VA-6 - used by ResumeStep to decide whether a resolver is required.
func (e *Executor) StepModelString(stepID string) string {
	e.stepModelStringsMu.Lock()
	defer e.stepModelStringsMu.Unlock()
	if e.stepModelStrings == nil {
		return ""
	}
	return e.stepModelStrings[stepID]
}

// drainBeforeWorkflowEnd enforces the ZF8.0a ordering contract by
// (1) waiting up to resumeShutdownTimeout for any in-flight resume
// goroutines to finish so their ResumeCompleted / ResumeFailed /
// TranscriptSealed events land first, and (2) draining the coordinator
// inbox so late reverse-reply messages surface as
// EventCoordinatorInboxMessage before EventWorkflowEnd.
func (e *Executor) drainBeforeWorkflowEnd(ctx context.Context, runID string) {
	// (1) Bounded wait for resume goroutines.
	done := make(chan struct{})
	go func() {
		e.resumeWG.Wait()
		close(done)
	}()
	drainTimer := time.NewTimer(resumeShutdownTimeout)
	defer drainTimer.Stop()
	select {
	case <-done:
	case <-drainTimer.C:
	// The outer defer reports leaks via EventMessage; don't
	// duplicate here. Continue so we still emit WorkflowEnd.
	case <-ctx.Done():
		// - on cancel, don't sit on the 5s grace timer.
		// The outer cleanup will still observe orphan resume
		// goroutines via the shutdown wait.
	}

	// (2) Final coordinator inbox drain. - delegate to
	// drainCoordReverseReplies instead of inlining the same Unread /
	// EventCoordinatorInboxMessage / MarkRead loop. The two blocks were
	// byte-for-byte equivalent; keeping a single source of truth means
	// future changes to event shape (e.g. new Data fields, MessageKind
	// taxonomy) only need to land in one place.
	e.drainCoordReverseReplies(ctx, runID)
}

// coordStepID returns the mailbox key used for the coordinator runner
// Convention: caller-provided runner.StepID when non-empty,
// otherwise the constant "coordinator". Documented on WithCoordinator.
func coordStepID(runner *AgentRunner) string {
	if runner == nil {
		return CoordRouterInboxID
	}
	if runner.stepID != "" {
		return runner.stepID
	}
	return CoordRouterInboxID
}

// signalCoordWake non-blockingly signals the coord runner's Wake
// channel after a lifecycle event has been Append'd into its Mailbox
// The coord's goai tool loop is parked on a WithStopWhen
// predicate that selects on Wake; without this signal the loop never
// re-enters drainMailboxIntoMessages and the coord effectively reacts
// only to the events present at Run-start (the workflow_start push).
// Safety contract:
// - nil-coord OR nil-Wake → no-op (defensive: callers may construct a
// partially-wired runner, e.g. routing-only test scenarios).
// - Wake is cap-1 buffered: send is wrapped in select+default so a
// full Wake (a previous signal not yet consumed) silently coalesces
// into the pending wake. Coalescing is correct - the coord will
// drain the entire mailbox on the next loop entry regardless of
// how many signals fired.
// - never blocks the caller; safe to call from goroutines holding
// executor-internal locks.
func signalCoordWake(coord *AgentRunner) {
	if coord == nil || coord.wake == nil {
		return
	}
	select {
	case coord.wake <- struct{}{}:
	default:
	}
}

// pushCoordEvent appends a lifecycle event into the coordinator runner's
// Mailbox as a RouterMessage. No-op when no coordinator runner
// is installed or its Mailbox is nil - the caller is not required to
// supply a Mailbox, and the executor never panics on a partially-wired
// runner. The Metadata["event_type"] field carries the EventType string
// so the coord runner can route events without parsing Content.
// after the Append the function fires signalCoordWake so the
// coord's goai loop re-enters and observes the new event.
func (e *Executor) pushCoordEvent(ev Event) {
	if e.Coordinator == nil || e.Coordinator.mailbox == nil {
		return
	}
	meta := map[string]string{
		"event_type": string(ev.Type),
		"run_id":     ev.RunID,
		"step_id":    ev.StepID,
	}
	if ev.AgentName != "" {
		meta["agent"] = ev.AgentName
	}
	content := fmt.Sprintf("%s step=%q agent=%q", ev.Type, ev.StepID, ev.AgentName)
	if ev.Message != "" {
		content = ev.Message
	}
	// workflow_start carries the per-call FlowContext as its
	// Content so the coord LLM can read the curated use-case input
	// directly from its first inbox message. Other event types do not
	// populate Context.
	if ev.Type == types.EventWorkflowStart && ev.Context != "" {
		content = ev.Context
	}
	if ev.Error != nil {
		content += " err=" + ev.Error.Error()
	}
	// Fix C: lifecycle events (EventWorkflowStart) carry no StepID -
	// falling through with From="" makes the stdout sink render
	// coord's drain as "received from ?". Use "executor" to match the
	// workflow-end push precedent.
	from := cmp.Or(ev.StepID, "executor")
	if _, err := e.Coordinator.mailbox.Append(coordStepID(e.Coordinator), RouterMessage{
		From:      from,
		Type:      router.MessageInfo,
		Content:   content,
		Timestamp: time.Now(),
		Metadata:  meta,
	}); err != nil {
		slog.Warn("mailbox append failed", "err", err, "site", "push-helper", "run_id", ev.RunID, "step_id", ev.StepID)
	}
	signalCoordWake(e.Coordinator)
}

// pushStepEventToCoord pushes a step lifecycle event into the coordinator
// runner's Mailbox as a RouterMessage (renamed from notifyCoordinator
// in). The legacy synchronous LLM call (the legacy per-step
// OnStep callback) and the targeted-message dispatch loop are gone in
// this refactor - narration/forwarding is now performed by the coord
// runner itself via the wired-in tools (narrate, forward_to_agent,
// finalize).
// resultsSnapshot is preserved for embedding aggregate progress
// counters in the pushed RouterMessage Content so the coord LLM can
// reason about completed/failed/pending counts without a separate
// query.
// decision: the bare-lifecycle pushCoordEvent calls for
// EventStepEnd / EventError were removed from runStep - this richer
// post-done push obsoletes them. StepStart still goes through
// pushCoordEvent (no equivalent post-done callsite for start events).
// Exactly-one StepEnd per step is asserted by
// TestExecutor_ExactlyOneStepEndPerStep (coord_dedup_test.go).
func (e *Executor) pushStepEventToCoord(ctx context.Context, runID, stepID, agentName string, sr *StepResult, resultsSnapshot map[string]*StepResult) {
	if e.Coordinator == nil || e.Coordinator.mailbox == nil {
		return
	}

	// apply namespace symmetric with runStep. The Run-loop
	// caller passes BARE stepID; in nested executors
	// (loop iteration, forEach item, include sub-workflow), prepend
	// namespacePrefix so coord sees fully-qualified IDs (e.g.
	// "debate-rounds.0.pro-argue") instead of bare "pro-argue". Without
	// this, lifecycle events from inner steps render as bare IDs in coord
	// stdout, mismatching the namespaced IDs used elsewhere (send_message
	// From, EventStepStart). Bug surfaced in debate-until.yaml run on
	// MiniMax (cycle 4 + 6) where same wake had mixed bare/namespaced IDs.
	if e.namespacePrefix != "" {
		stepID = e.namespacePrefix + "." + stepID
	}

	// Build event from step result. Mirrors the lifecycle Progress event
	// the executor will emit for step-end so subscribers (TUIs) and the
	// coord runner observe identical typing.
	var evType EventType
	if sr.Status == spec.StepFailed {
		evType = types.EventError
	} else {
		evType = types.EventStepEnd
	}

	// Aggregate progress counters from the snapshot so the coord LLM
	// can reason about overall workflow state from a single message.
	var completed, failed, pending int
	for _, r := range resultsSnapshot {
		if r == nil {
			pending++
			continue
		}
		switch r.Status {
		case spec.StepCompleted:
			completed++
		case spec.StepFailed:
			failed++
		default:
			pending++
		}
	}

	// Tracing: the legacy zenflow.coordinator span scoped the LLM call
	// duration. In the LLM call is gone, so the span shrinks to the
	// mailbox-append latency. Keep the span for continuity - operators
	// already chart on it.
	var coordErr error
	if e.Tracer != nil {
		spanCtx := e.Tracer.StartSpan(ctx, "zenflow.coordinator", map[string]string{
			"zenflow.coordinator.phase": "on_step_event",
			"zenflow.step.id":           stepID,
			"zenflow.run_id":            runID,
		})
		defer func() {
			if r := recover(); r != nil {
				if e2, ok := r.(error); ok {
					coordErr = fmt.Errorf("coordinator panic: %w", e2)
				} else {
					coordErr = fmt.Errorf("coordinator panic: %v", r)
				}
			}
			e.Tracer.EndSpan(spanCtx, coordErr)
		}()
	}

	coordID := coordStepID(e.Coordinator)
	content := fmt.Sprintf("step %q (%q) finished status=%s in %s - completed=%d failed=%d pending=%d",
		stepID, agentName, sr.Status, sr.Duration, completed, failed, pending)
	if sr.Error != nil {
		content += " err=" + sr.Error.Error()
	}
	meta := map[string]string{
		"event_type": string(evType),
		"run_id":     runID,
		"step_id":    stepID,
		"agent":      agentName,
		"status":     string(sr.Status),
	}
	if _, err := e.Coordinator.mailbox.Append(coordID, RouterMessage{
		From:      stepID,
		Type:      router.MessageInfo,
		Content:   content,
		Timestamp: time.Now(),
		Metadata:  meta,
	}); err != nil {
		coordErr = err
	}
	// signal Wake so the coord's goai loop re-enters and reacts
	// to this step lifecycle event. Without the wake the message sits
	// unread in the mailbox until process exit.
	signalCoordWake(e.Coordinator)
}

// drainCoordReverseReplies surfaces reverse-routed resume replies sitting
// in the workflow Router's "coordinator" inbox as
// EventCoordinatorInboxMessage events. Resumed steps Append into this
// inbox via Executor.runResume; without this drain the CLI/TUI never
// sees the resumed agent's response.
func (e *Executor) drainCoordReverseReplies(ctx context.Context, runID string) {
	if e.Router == nil {
		return
	}
	mb := e.Router.Mailbox()
	if mb == nil {
		return
	}
	unread := mb.Unread(CoordRouterInboxID)
	if len(unread) == 0 {
		return
	}
	ids := make([]string, 0, len(unread))
	for _, m := range unread {
		if e.Progress != nil {
			e.Progress.OnEvent(ctx, Event{
				Type:        types.EventCoordinatorInboxMessage,
				Timestamp:   time.Now(),
				RunID:       runID,
				Message:     m.Content,
				MessageKind: types.MessageKindContent,
				Data: map[string]any{
					"from": m.From,
					"type": m.Type.String(),
				},
			})
		}
		ids = append(ids, m.MessageID)
	}
	mb.MarkRead(CoordRouterInboxID, ids)
}

// nestedSuppressLifecycleSink is the ProgressSink wrapper installed by
// nested executors (loop iterations, forEach items, includes). It
// suppresses the nested mini-workflow's EventWorkflowStart and
// EventWorkflowEnd events - they are internal plumbing of the loop /
// include step's per-iteration nested executor and must NOT surface
// to the outer sink. All other events pass through unchanged.
type nestedSuppressLifecycleSink struct {
	inner ProgressSink
}

func (n *nestedSuppressLifecycleSink) OnEvent(ctx context.Context, event Event) {
	if event.Type == types.EventWorkflowStart || event.Type == types.EventWorkflowEnd {
		return
	}
	n.inner.OnEvent(ctx, event)
}

func (n *nestedSuppressLifecycleSink) OnOutput(ctx context.Context, output Output) {
	n.inner.OnOutput(ctx, output)
}

// - compile-time assertions catching signature drift on
// ProgressSink, the resumer hook, and EngineActiveStepsSource at the
// type that satisfies them.
var (
	_ ProgressSink            = (*nestedSuppressLifecycleSink)(nil)
	_ Resumer                 = (*Executor)(nil)
	_ EngineActiveStepsSource = (*Executor)(nil)
)
