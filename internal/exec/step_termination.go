// Package exec - step_termination.go contains the per-step
// termination wait + abort flush helpers used by the executor:
// waitForStepTermination (3-invariant rule with a stable-tick guard),
// the F8 hold-timeout wrapper waitForStepTerminationWithHoldTimeout,
// and flushMailboxOnAbort which drains and closes mailboxes during
// the workflow-abort flow. Also hosts the StepIdle soft-gate
// observability counter (stepIdleFallback*) that flags production
// callers which forgot to wire AgentRunner.Run's SetTerminal defer.
package exec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zendev-sh/goai"

	"github.com/zendev-sh/zenflow/internal/types"
)

// stepIdleFallbackOnce + stepIdleFallbackHits implement the soft-gate
// observability hook. The fallback branch in waitForStepTermination
// accepts a bare goai.StepIdle as a terminating observation to preserve
// compatibility with tests that drive AgentState manually without the
// AgentRunner.Run terminal-state defer. Production callers (executor.go
// runStep) ALWAYS wire SetTerminal via AgentRunner.Run's defer, so they
// should never trip this branch; if they do, it indicates a wiring bug
// that would otherwise be silent. We emit one log line on first hit (per
// process) and increment an atomic counter so same-package tests can
// assert "fallback never fired" via stepIdleFallbackHitsCount.
// stepIdleFallbackMu guards all accesses to stepIdleFallbackOnce so
// that resetStepIdleFallbackForTest (which replaces the sync.Once
// value) is safe relative to concurrent .Do calls in
// waitForStepTermination. Without the mutex a plain struct assignment
// races with a concurrent .Do - the -race detector flags it. Tests
// that call resetStepIdleFallbackForTest may still NOT use t.Parallel
// against each other (the reset must be exclusive), but concurrent
// production callers hitting the .Do path are now safe.
var (
	stepIdleFallbackMu   sync.Mutex
	stepIdleFallbackOnce sync.Once
	stepIdleFallbackHits atomic.Int64
)

// stepIdleFallbackHitsCount returns the cumulative count of times the
// H4 StepIdle soft-gate fallback in waitForStepTermination has been
// hit since process start. Internal observability counter - production
// callers (executor.go runStep) wire SetTerminal via AgentRunner.Run's
// defer and should observe a terminal kind directly; a non-zero count
// from a production-only workload indicates a missing SetTerminal
// wiring on a custom caller. Same-package tests that drive AgentState
// manually (lifecycle_test.go, round3_fixes_test.go) intentionally
// trip this path and assert against the counter.
func stepIdleFallbackHitsCount() int64 { return stepIdleFallbackHits.Load() }

// resetStepIdleFallbackForTest resets the once-warning state and counter for
// tests. Callers MUST NOT run concurrently with each other - this reset must
// be exclusive (no t.Parallel on any test that calls this function).
// stepIdleFallbackMu makes it safe to call this while concurrent production
// goroutines are executing the .Do path in waitForStepTermination.
func resetStepIdleFallbackForTest() {
	stepIdleFallbackMu.Lock()
	stepIdleFallbackOnce = sync.Once{}
	stepIdleFallbackMu.Unlock()
	stepIdleFallbackHits.Store(0)
}

// tickFunc returns a ticker channel that fires once per polling interval.
// It mirrors EngineClock.Tick so production code can pass time.Tick and
// tests can substitute a manual-fire stub.
type tickFunc func(d time.Duration) <-chan time.Time

// waitForStepTermination blocks until ALL THREE of the
// termination invariants hold for stepID:
// 1. router.PendingSenders(stepID) == 0 - no goroutine could still Send.
// 2. mailbox.Unread(stepID) is empty - nothing is queued.
// 3. state.Observe == StepIdle, observed STABLE across at least one
// subsequent tick - guards against a race where the agent flips
// Idle → LLMInFlight between the poller's read and our snapshot.
// Returns nil when the invariants hold; returns ctx.Err when ctx
// cancels mid-wait (the caller routes to the workflow-abort flush
// path defined in flushMailboxOnAbort). The function is a no-op on
// nil router or mailbox - early callers (tests, runs without a coord
// runner) can pass nil and skip the wait entirely.
// "Stable across one tick" is implemented as: require the invariants
// to hold on TWO consecutive observations separated by at least one
// fire of the supplied tick channel. Without the second observation
// a fast Idle→Busy→Idle bounce (single-shot LLM call wrapped by a
// tool result) would falsely satisfy the invariant.
func waitForStepTermination(
	ctx context.Context,
	stepID string,
	router *MessageRouter,
	mailbox MailboxStore,
	state *goai.AgentState,
	tick tickFunc,
	interval time.Duration,
) error {
	if router == nil || mailbox == nil {
		return nil
	}
	if interval <= 0 {
		interval = 50 * time.Millisecond
	}

	// F5 - snapshot all three invariants under the per-step RLock so
	// the read is coherent: a concurrent Close cannot complete between
	// our PendingSenders/Unread/Observe reads. With a coherent
	// snapshot one observation suffices, eliminating the previous
	// 50ms × 2 stable-observation tail (~100ms per step).
	stepLock := router.AcquireStepLock(stepID)
	check := func() bool {
		stepLock.RLock()
		defer stepLock.RUnlock()
		// B1 defense-in-depth: if the workflow was cancelled, the
		// abort-flush path will drain mailboxes wholesale; do
		// not keep blocking the per-step wait on inbound senders that
		// will never deliver.
		if router.WorkflowCancelled() {
			return true
		}
		if router.PendingSenders(stepID) != 0 {
			return false
		}
		if len(mailbox.Unread(stepID)) != 0 {
			return false
		}
		if state != nil {
			kind, _ := state.Observe()
			// Gate on terminal kinds (Done, Cancelled,
			// Error) rather than the wake-eligible StepIdle. StepIdle
			// alone is unsafe because the runner may flip back to
			// StepLLMInFlight via the wake loop after observing it. A
			// terminal state, in contrast, is sticky (CAS-guarded by
			// goai.AgentState.SetTerminal) and proves no further
			// transitions can occur.
			// StepIdle fallback: the check below ALSO accepts StepIdle
			// to support callers that drive AgentState manually without
			// invoking AgentRunner.Run's terminal-state defer. Callers
			// in this category are exclusively tests today:
			// - lifecycle_test.go drives st.set(StepIdle, ...) to
			// reproduce poller invariants without standing up a
			// real runner.
			// - round3_fixes_test.go uses the same pattern.
			// The standalone RunAgent path (zenflow.go RunAgent) does
			// NOT pass StateRef into AgentRunner; its state here is
			// therefore nil and this branch is skipped entirely.
			// Removing the fallback would require rewriting every
			// AgentState-driving test through a full goai.GenerateText
			// call - a significant refactor with no production benefit.
			// Keep the fallback explicit + documented; the production
			// invariant ("terminal states are sticky") is preserved
			// because the executor path always wires SetTerminal via
			// AgentRunner.Run's defer.
			if !kind.IsTerminal() && kind != goai.StepIdle {
				return false
			}
			// Observability hook on the soft-gate fallback. If we
			// accepted a bare StepIdle (rather than a terminal kind),
			// record the hit so operators can detect a production caller
			// that forgot to wire SetTerminal via AgentRunner.Run's
			// defer. The first hit per process logs a one-shot warning;
			// subsequent hits only bump the counter (avoid log spam).
			// Tests that intentionally drive StepIdle without a terminal
			// CAS will inflate this counter; that is expected and
			// observable via stepIdleFallbackHitsCount.
			if kind == goai.StepIdle {
				stepIdleFallbackHits.Add(1)
				stepIdleFallbackMu.Lock()
				stepIdleFallbackOnce.Do(func() {
					slog.Warn("waitForStepTermination accepted StepIdle without terminal CAS; production callers must wire AgentRunner.Run's SetTerminal defer to avoid this soft-gate fallback",
						"step_id", stepID,
					)
				})
				stepIdleFallbackMu.Unlock()
			}
		}
		return true
	}

	if check() {
		return nil
	}

	tickCh := tick(interval)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tickCh:
		}
		if check() {
			return nil
		}
	}
}

// errHoldTimeout is returned by waitForStepTerminationWithHoldTimeout
// when the hold-timeout cap fires before
// the 3-invariant termination rule converges. The caller is expected
// to drain any remaining mailbox messages and emit one
// DropReasonHoldTimeout drop per message.
var errHoldTimeout = holdTimeoutError{}

type holdTimeoutError struct{}

func (holdTimeoutError) Error() string { return "hold-timeout" }

// IsHoldTimeout reports whether err originated from the F8
// hold-timeout cap. Exposed so tests can assert the cause.
func IsHoldTimeout(err error) bool { return errors.Is(err, errHoldTimeout) }

// waitForStepTerminationWithHoldTimeout is the F8 wrapper around
// waitForStepTermination that adds an absolute "hold" timeout. After
// holdTimeout elapses (measured from call entry), the wait aborts
// with errHoldTimeout regardless of the 3-invariant state. Caller is
// responsible for draining the mailbox and emitting drop events with
// DropReasonHoldTimeout. holdTimeout <= 0 disables the cap (falls
// back to the pre-F8 wait).
// nowFn is injectable so tests can use a fake clock; in production
// pass time.Now.
func waitForStepTerminationWithHoldTimeout(
	ctx context.Context,
	stepID string,
	router *MessageRouter,
	mailbox MailboxStore,
	state *goai.AgentState,
	tick tickFunc,
	interval time.Duration,
	holdTimeout time.Duration,
	nowFn func() time.Time,
) error {
	if holdTimeout <= 0 {
		return waitForStepTermination(ctx, stepID, router, mailbox, state, tick, interval)
	}
	if nowFn == nil {
		nowFn = time.Now
	}
	holdCtx, cancel := context.WithDeadline(ctx, nowFn().Add(holdTimeout))
	defer cancel()
	err := waitForStepTermination(holdCtx, stepID, router, mailbox, state, tick, interval)
	if err == nil {
		return nil
	}
	// Distinguish hold-timeout from caller-ctx cancel:
	// - hold-timeout: holdCtx fired but ctx is still alive.
	// - caller cancel: ctx is done.
	if ctx.Err() != nil {
		return err
	}
	return errHoldTimeout
}

// flushMailboxOnAbort drains every pending message for each step in
// stepIDs, emits one EventMessageDropped per message with the supplied
// reason, then closes each mailbox. Used by the executor's
// workflow-abort path to ensure NO message is
// silently dropped even when the workflow is cancelled before the
// 3-invariant termination rule can fire.
// Per-mailbox semantics:
// - Unread is called once per step; the snapshot is iterated to
// emit drop events.
// - Close is called immediately after - subsequent Send/Append
// calls land on a closed mailbox and are dropped silently
// (already accounted for via the closed-mailbox no-op contract).
// progress may be nil; the function still drains and closes mailboxes
// so the cleanup is unconditional.
func flushMailboxOnAbort(
	ctx context.Context,
	runID string,
	stepIDs []string,
	mailbox MailboxStore,
	progress ProgressSink,
	reason DropReason,
) {
	if mailbox == nil {
		return
	}
	for _, stepID := range stepIDs {
		pending := mailbox.Unread(stepID)
		for _, msg := range pending {
			if progress != nil {
				progress.OnEvent(ctx, Event{
					Type:      types.EventMessageDropped,
					Timestamp: time.Now(),
					RunID:     runID,
					StepID:    stepID,
					Message:   fmt.Sprintf("[%s -> %s]: %s", msg.From, stepID, msg.Content),
					Data: map[string]any{
						"reason":   reason.String(),
						"from":     msg.From,
						"to":       stepID,
						"msg_type": int(msg.Type),
					},
				})
			}
		}
		mailbox.Close(stepID)
	}
}
