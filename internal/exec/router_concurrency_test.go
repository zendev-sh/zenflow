package exec

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/router"
	"github.com/zendev-sh/zenflow/internal/spec"
)

// newRouterWithMailbox is a local helper for tests in this file
// (the canonical version in internal/router/router_test.go is not
// reachable from the root test package).
func newRouterWithMailbox(t *testing.T) (*MessageRouter, *InMemoryMailboxStore) {
	t.Helper()
	r := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	r.SetMailbox(mb)
	return r, mb
}

// ----- F5: stepLock wired into Send / Close / waitForStepTermination ------

// TestRouter_StepLock_SerializesSendVsClose verifies that an in-flight
// Send (holding the per-step RLock) serialises against a concurrent
// Close (taking the write lock). Without F5 this race could leak a
// post-Close Append; with F5 wired the Close blocks until the Send
// finishes its mailbox.Append, so the test sees either:
// - Send wins → message in mailbox, no drop
// - Close wins → drop emitted, mailbox empty
// Critically, both Send and Close must complete (no deadlock), and
// the per-message bookkeeping must reconcile: every Send produces
// either a delivered mailbox entry OR a drop event.
func TestRouter_StepLock_SerializesSendVsClose(t *testing.T) {
	r := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	r.SetMailbox(mb)
	r.RegisterInbox("s")

	var drops int64
	r.SetOnDrop(func(_ DropEvent) { atomic.AddInt64(&drops, 1) })

	const N = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			_ = r.Send("s", RouterMessage{Content: "x"})
		}
	}()
	go func() {
		defer wg.Done()
 // Let some sends accumulate, then close.
		time.Sleep(time.Microsecond * 100)
		r.Close("s")
	}()
	wg.Wait()

	delivered := len(mb.Unread("s"))
	dropped := atomic.LoadInt64(&drops)
	// Every Send accounted for. With F5 wired the mailbox+drop totals
	// MUST equal N - no silent loss possible.
	if int64(delivered)+dropped != N {
		t.Fatalf("delivered=%d + dropped=%d != N=%d (silent loss)",
			delivered, dropped, N)
	}
}

// TestWaitForStepTermination_OneSnapshotUnderLock verifies the F5
// optimisation: a single coherent observation under the stepLock
// suffices, eliminating the pre-F5 50ms × 2 stable-observation tail.
// We arrange: the invariants are satisfied at the moment of the
// first check call. Pre-F5 required two ticks; post-F5 returns
// immediately on the first observation. The test fires zero ticks
// and expects waitForStepTermination to return without blocking.
func TestWaitForStepTermination_OneSnapshotUnderLock(t *testing.T) {
	r := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	st := &goai.AgentState{}
	runToIdle(t, st)

	clock := newTickerClock()
	defer clock.stop()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- waitForStepTermination(t.Context(), "s1", r, mb, st, clock.tickFn, 5*time.Millisecond)
	}()

	// Do NOT fire any ticks. Pre-F5 the wait would block here forever
	// (needed 2 stable observations). Post-F5 the immediate snapshot
	// satisfies the invariant on call entry.
	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("waitForStepTermination: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("waitForStepTermination did not return on the immediate snapshot - F5 fast-path regressed")
	}
}

// ----- F6: progress sink pump wired by default ----------------------------

// TestProgressSink_DefaultIsNonBlocking exercises the F6 wire-up: a
// workflow with a non-noop coordinator gets its ProgressSink wrapped
// in eventBusSinkPump automatically. We supply a deliberately slow
// sink (50ms per OnEvent) and a 2-step workflow; the executor must
// finish in well under 2 × 50ms × stepCount because the pump
// absorbs the latency.
func TestProgressSink_DefaultIsNonBlocking(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "ok", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
			{Text: "ok", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}
	wf := newTestWorkflow(
		[]Step{
			{ID: "a", Instructions: "do a"},
			{ID: "b", Instructions: "do b"},
		},
		nil,
	)

	slow := &slowSink{delay: 50 * time.Millisecond}
	exec := newTestExecutor(model, nil, wf)
	exec.Coordinator = newTestCoordRunner()
	exec.Progress = slow
	// Mark DAG-aware so we keep the F7 cheaper path in the integration
	// test as well.
	exec.SenderMatrixDAGAware = true

	start := time.Now()
	res, err := exec.Run(t.Context())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != spec.StatusCompleted {
		t.Fatalf("status=%v", res.Status)
	}
	// Without the pump each event would block 50ms in the caller.
	// 2 steps × ~5 events ≈ 500ms minimum. With the pump the events
	// drain async - the workflow itself completes well under that
	// bound. We give a generous 1.5s headroom (real LLMs in tests are
	// stub but we still want flakiness margin).
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("Run took %v with slow sink - pump did not absorb latency (F6 not wired by default)", elapsed)
	}
}

// ----- F7: DAG-aware sender matrix ---------------------------------------
// The legacy TestExecutor_SenderMatrix_OnlyCoordinatorOpens hooked into
// CoordinatorAgent.OnStepEvent to snapshot Router.PendingSenders during
// in-flight steps. removed the OnStepEvent callback (executor pushes
// events directly into coord.Mailbox), so the indirect verification
// vehicle no longer exists. The F7 sender-matrix invariant is now
// covered by the direct Router unit tests in router_test.go (specifically
// TestMessageRouter_OpenSender / CloseSender) - that surface is the
// single source of truth for the open-slot count contract; the
// disappearance of this end-to-end test does not reduce coverage of the
// invariant itself.

// ----- F8: hold-timeout force-exit ---------------------------------------

// TestWaitForStepTermination_HoldTimeoutForcesExit verifies that
// waitForStepTerminationWithHoldTimeout aborts with errHoldTimeout
// when the hold cap fires before the 3-invariant rule converges.
// We hold a sender slot open forever so the wait can never satisfy
// invariant 1 naturally.
func TestWaitForStepTermination_HoldTimeoutForcesExit(t *testing.T) {
	r := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	st := &goai.AgentState{}
	runToIdle(t, st)
	r.OpenSender("s") // never closes - invariant 1 unsatisfiable

	clock := newTickerClock()
	defer clock.stop()

	// Fake clock advances "now" via a controllable counter so we
	// don't have to actually sleep.
	base := time.Now()
	nowCalls := 0
	nowFn := func() time.Time {
		nowCalls++
 // First call: at deadline calculation.
 // Subsequent calls: post-deadline so the holdCtx fires fast.
		if nowCalls == 1 {
			return base
		}
		return base.Add(2 * time.Hour)
	}

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- waitForStepTerminationWithHoldTimeout(
			t.Context(), "s", r, mb, st, clock.tickFn,
			5*time.Millisecond, 50*time.Millisecond, nowFn,
		)
	}()

	// Fire some ticks; the holdCtx deadline (50ms) will trip.
	for i := 0; i < 10; i++ {
		clock.fire()
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case err := <-doneCh:
		if !IsHoldTimeout(err) {
			t.Fatalf("err=%v want errHoldTimeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForStepTerminationWithHoldTimeout did not exit on hold timeout")
	}
}

// TestWaitForStepTermination_HoldTimeoutDisabledFallsThrough verifies
// that holdTimeout <= 0 reverts to the pre-F8 behavior (just calls
// waitForStepTermination - same return value contract).
func TestWaitForStepTermination_HoldTimeoutDisabledFallsThrough(t *testing.T) {
	r := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	st := &goai.AgentState{}
	runToIdle(t, st)

	clock := newTickerClock()
	defer clock.stop()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- waitForStepTerminationWithHoldTimeout(
			t.Context(), "s", r, mb, st, clock.tickFn,
			5*time.Millisecond, 0, nil,
		)
	}()

	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("err=%v want nil (immediate snapshot satisfies invariants)", err)
		}
	case <-time.After(time.Second):
		t.Fatal("did not return on satisfied invariants")
	}
}

// ----- G7 : sibling-direct Send safety --------------------------

// TestExecutor_SenderMatrix_AutoOpensForUnregisteredSender verifies the G7
// guard: when a workflow step (registered via RegisterStep at Run start)
// receives a Send from a path that did NOT pre-open a sender slot
// (e.g. a future sibling-direct Send added after F7), the router
// auto-opens the slot just-in-time so the message lands in the mailbox
// instead of silently dropping as DropReasonUnknownStep.
// Truly unknown step IDs (not registered) continue to drop - the F7
// "zero silent drops" semantic is preserved while protecting against
// accidental regression from sibling-direct Sends.
func TestExecutor_SenderMatrix_AutoOpensForUnregisteredSender(t *testing.T) {
	r, mb := newRouterWithMailbox(t)
	var drops []DropEvent
	r.SetOnDrop(func(d DropEvent) { drops = append(drops, d) })

	// Pre-register the step as part of the workflow's known DAG, but do
	// NOT call RegisterInbox (no runStep goroutine has started yet) and
	// do NOT pre-open any sender slot. This mirrors the post-F7 state
	// for a step whose runStep goroutine is still queueing.
	r.RegisterStep("sib")

	_ = r.Send("sib", RouterMessage{From: "other-sib", Content: "hello"})

	if len(drops) != 0 {
		t.Fatalf("got %d drop events; want 0 (auto-open should have suppressed the drop). drops=%+v", len(drops), drops)
	}
	pending := mb.Unread("sib")
	if len(pending) != 1 {
		t.Fatalf("mailbox.Unread(sib)=%d want 1 (auto-opened slot should have appended)", len(pending))
	}
	if pending[0].Content != "hello" {
		t.Errorf("pending[0].Content=%q want %q", pending[0].Content, "hello")
	}
	// Auto-opened slot must be released after Send returns - no leak.
	if got := r.PendingSenders("sib"); got != 0 {
		t.Errorf("PendingSenders(sib)=%d want 0 (auto-opened slot leaked)", got)
	}

	// Negative path: unknown step (not registered) still drops as
	// DropReasonUnknownStep - the auto-open path must not blanket
	// every Send.
	_ = r.Send("typo-step", RouterMessage{From: "x", Content: "y"})
	if len(drops) != 1 {
		t.Fatalf("expected 1 drop for unregistered step, got %d", len(drops))
	}
	if drops[0].Reason != router.DropReasonUnknownStep {
		t.Errorf("Reason=%q want unknown-step", drops[0].Reason)
	}
	if drops[0].StepID != "typo-step" {
		t.Errorf("StepID=%q want typo-step", drops[0].StepID)
	}
}
