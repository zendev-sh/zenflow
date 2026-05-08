package exec

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zendev-sh/zenflow/internal/router"
	"github.com/zendev-sh/zenflow/internal/types"
)

// TestDropReason_StringRoundTrip locks in the wire-format strings so
// downstream subscribers reading Event.Data["reason"] keep working
// after S1's typed-enum migration. The cases come from the canonical
// `dropReasonStrings` map in router.go - never redefine the strings
// here (drift risk: a rename in router.go would be silently masked by a
// stale copy in this test).
func TestDropReason_StringRoundTrip(t *testing.T) {
	wantSubset := []DropReason{
		router.DropReasonWorkflowCancelled,
		router.DropReasonTargetTerminal,
		router.DropReasonUnknownStep,
		router.DropReasonMailboxClosedByFinalize,
		router.DropReasonMaxWakeCycles,
	}
	canonical := DropReasonStrings()
	for _, reason := range wantSubset {
		want, ok := canonical[reason]
		if !ok {
			t.Fatalf("DropReasonStrings() missing %v - canonical map drifted from enum", reason)
		}
		if got := reason.String(); got != want {
			t.Errorf("DropReason(%d).String()=%q want %q", reason, got, want)
		}
	}
	if router.DropReasonUnspecified.String() != "unspecified" {
		t.Errorf("Unspecified.String()=%q", router.DropReasonUnspecified.String())
	}
}

// TestRouter_WorkflowCancelled_RejectsSend: once
// MarkWorkflowCancelled is called, every Send returns immediately
// with a DropReasonWorkflowCancelled drop event regardless of the
// target step's mailbox state.
func TestRouter_WorkflowCancelled_RejectsSend(t *testing.T) {
	r := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	r.SetMailbox(mb)
	r.RegisterInbox("a")

	var drops []DropEvent
	var dropMu sync.Mutex
	r.SetOnDrop(func(d DropEvent) {
		dropMu.Lock()
		defer dropMu.Unlock()
		drops = append(drops, d)
	})

	if r.WorkflowCancelled() {
		t.Fatal("WorkflowCancelled() should be false initially")
	}
	r.MarkWorkflowCancelled()
	if !r.WorkflowCancelled() {
		t.Fatal("WorkflowCancelled() should be true after MarkWorkflowCancelled")
	}

	_ = r.Send("a", RouterMessage{From: "coord", Content: "after-cancel"})

	dropMu.Lock()
	defer dropMu.Unlock()
	if len(drops) != 1 {
		t.Fatalf("drops=%d want 1", len(drops))
	}
	if drops[0].Reason != router.DropReasonWorkflowCancelled {
		t.Errorf("drops[0].Reason=%v want WorkflowCancelled", drops[0].Reason)
	}
	if got := mb.Unread("a"); len(got) != 0 {
		t.Errorf("mailbox should not have received Send after cancel, got %d", len(got))
	}
}

// TestRouter_WorkflowCancelled_AttributesCloseDrops covers the S4
// Close-path attribution: pending messages drained at Close time after
// MarkWorkflowCancelled emit DropReasonWorkflowCancelled (not the
// generic terminal reason) so operators can distinguish abort drops.
func TestRouter_WorkflowCancelled_AttributesCloseDrops(t *testing.T) {
	r := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	r.SetMailbox(mb)
	r.RegisterInbox("b")
	if _, err := mb.Append("b", RouterMessage{From: "x", Content: "stuck"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	var drops []DropEvent
	r.SetOnDrop(func(d DropEvent) { drops = append(drops, d) })

	r.MarkWorkflowCancelled()
	r.Close("b")

	if len(drops) != 1 {
		t.Fatalf("drops=%d want 1", len(drops))
	}
	if drops[0].Reason != router.DropReasonWorkflowCancelled {
		t.Errorf("Close-time drop after cancel: Reason=%v want WorkflowCancelled", drops[0].Reason)
	}
}

// TestRouter_StepLock_AcquireRelease verifies that the
// per-step RWMutex registry hands out the same lock for repeated
// AcquireStepLock calls and lets multiple goroutines hold the read
// lock concurrently.
func TestRouter_StepLock_AcquireRelease(t *testing.T) {
	r := NewMessageRouter()

	lk1 := r.AcquireStepLock("step-a")
	lk2 := r.AcquireStepLock("step-a")
	if lk1 != lk2 {
		t.Fatal("AcquireStepLock should return the same lock for the same stepID")
	}

	// Concurrent read-lock holders.
	const readers = 8
	var wg sync.WaitGroup
	wg.Add(readers)
	var inFlight atomic.Int64
	var maxConcurrent atomic.Int64
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			lk1.RLock()
			defer lk1.RUnlock()
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for {
				cur := maxConcurrent.Load()
				if n <= cur || maxConcurrent.CompareAndSwap(cur, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
		}()
	}
	wg.Wait()
	if maxConcurrent.Load() < 2 {
		t.Errorf("expected concurrent read-lock holders >= 2, got %d", maxConcurrent.Load())
	}

	// Different stepID gets a different lock.
	lkOther := r.AcquireStepLock("step-b")
	if lkOther == lk1 {
		t.Fatal("AcquireStepLock should return distinct locks for distinct stepIDs")
	}

	r.ReleaseStepLock("step-a")
	// After release the next Acquire returns a fresh instance.
	lk3 := r.AcquireStepLock("step-a")
	if lk3 == lk1 {
		t.Error("after ReleaseStepLock, Acquire should return a new lock instance")
	}
}

// TestProgressSink_NonBlocking verifies that
// wrapProgressNonBlocking guarantees OnEvent never blocks the caller
// even when the downstream sink is slow.
func TestProgressSink_NonBlocking(t *testing.T) {
	slow := &slowSink{delay: 50 * time.Millisecond}
	pump := wrapProgressNonBlocking(slow, 0)
	defer pump.Stop()

	const N = 100
	start := time.Now()
	for i := 0; i < N; i++ {
		pump.OnEvent(context.Background(), Event{Type: types.EventMessage, Message: "x"})
	}
	elapsed := time.Since(start)
	// All N pushes must complete in well under (N * slow.delay) - the
	// pump's buffered channel absorbs the bursts. We allow up to 200ms
	// (well under the 5s "synchronous" baseline).
	if elapsed > 200*time.Millisecond {
		t.Fatalf("OnEvent push loop took %v - expected non-blocking sub-200ms; pump may be synchronous", elapsed)
	}

	// Drain.
	pump.Stop()
	if got := slow.count.Load(); got != N {
		t.Errorf("slow sink received %d events, want %d (some may have overflowed; see Stats)", got, N)
	}
	pushed, dropped := pump.Stats()
	if dropped != 0 {
		t.Errorf("dropped=%d (overflow shouldn't happen at N=%d, buf=%d)", dropped, N, defaultEventBusBuffer)
	}
	if pushed != int64(N) {
		t.Errorf("pushed=%d want %d", pushed, N)
	}
}

// TestProgressSink_PanicIsolation: a panicking
// downstream sink must NOT kill the pump.
func TestProgressSink_PanicIsolation(t *testing.T) {
	panickyAfter := atomic.Int64{}
	sink := &funcSink{
		onEvent: func(_ context.Context, _ Event) {
			if panickyAfter.Add(1) == 1 {
				panic("simulated user-sink panic")
			}
		},
	}
	pump := wrapProgressNonBlocking(sink, 0)
	defer pump.Stop()

	pump.OnEvent(context.Background(), Event{Type: types.EventMessage, Message: "boom"})
	pump.OnEvent(context.Background(), Event{Type: types.EventMessage, Message: "after"})
	pump.Stop()

	if panickyAfter.Load() != 2 {
		t.Errorf("inner sink should have been called 2 times despite panic, got %d", panickyAfter.Load())
	}
}

// slowSink delays OnEvent to test non-blocking pump behavior.
type slowSink struct {
	delay time.Duration
	count atomic.Int64
}

func (s *slowSink) OnEvent(_ context.Context, _ Event) {
	time.Sleep(s.delay)
	s.count.Add(1)
}

func (s *slowSink) OnOutput(_ context.Context, _ Output) {
	time.Sleep(s.delay)
	s.count.Add(1)
}

// funcSink wires arbitrary OnEvent / OnOutput handlers. Used for
// panic-isolation testing.
type funcSink struct {
	onEvent  func(context.Context, Event)
	onOutput func(context.Context, Output)
}

func (f *funcSink) OnEvent(ctx context.Context, ev Event) {
	if f.onEvent != nil {
		f.onEvent(ctx, ev)
	}
}

func (f *funcSink) OnOutput(ctx context.Context, out Output) {
	if f.onOutput != nil {
		f.onOutput(ctx, out)
	}
}

// TestMailbox_LenReportsUnreadAndTotal covers S1's Len API (plan
// §4.2 #1).
func TestMailbox_LenReportsUnreadAndTotal(t *testing.T) {
	mb := NewInMemoryMailboxStore()
	stepID := "len-step"

	if u, total := mb.Len(stepID); u != 0 || total != 0 {
		t.Fatalf("empty Len=(%d,%d) want (0,0)", u, total)
	}

	for i := 0; i < 3; i++ {
		if _, err := mb.Append(stepID, RouterMessage{Content: "x"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	u, total := mb.Len(stepID)
	if u != 3 || total != 3 {
		t.Errorf("after 3 appends Len=(%d,%d) want (3,3)", u, total)
	}

	all := mb.Unread(stepID)
	mb.MarkRead(stepID, MessageIDs(all[:2]))
	u, total = mb.Len(stepID)
	if u != 1 || total != 3 {
		t.Errorf("after MarkRead 2 of 3, Len=(%d,%d) want (1,3)", u, total)
	}
}

// TestMailbox_SealKeepsUnread verifies that Seal
// blocks new Appends but preserves existing unread for the poller to
// drain.
func TestMailbox_SealKeepsUnread(t *testing.T) {
	mb := NewInMemoryMailboxStore()
	stepID := "seal-step"
	if _, err := mb.Append(stepID, RouterMessage{Content: "before-seal"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	mb.Seal(stepID)

	// Existing unread message remains readable.
	got := mb.Unread(stepID)
	if len(got) != 1 || got[0].Content != "before-seal" {
		t.Errorf("Seal should preserve unread; got %+v", got)
	}

	// New Appends are dropped (matching Close).
	if _, err := mb.Append(stepID, RouterMessage{Content: "after-seal"}); err != nil {
		t.Fatalf("append after Seal: %v", err)
	}
	got = mb.Unread(stepID)
	if len(got) != 1 {
		t.Errorf("Append after Seal should be no-op; got %d entries", len(got))
	}

	// Hard delete clears it.
	mb.Close(stepID)
	if got := mb.Unread(stepID); len(got) != 0 {
		t.Errorf("after Close, Unread should be empty, got %d", len(got))
	}
}
