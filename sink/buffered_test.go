package sink

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/zendev-sh/zenflow"
)

// recordingSink captures OnEvent/OnOutput calls for assertions.
type recordingSink struct {
	mu      sync.Mutex
	events  []zenflow.Event
	outputs []zenflow.Output
}

func (r *recordingSink) OnEvent(_ context.Context, e zenflow.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recordingSink) OnOutput(_ context.Context, o zenflow.Output) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outputs = append(r.outputs, o)
}

func (r *recordingSink) snap() ([]zenflow.Event, []zenflow.Output) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := append([]zenflow.Event(nil), r.events...)
	o := append([]zenflow.Output(nil), r.outputs...)
	return e, o
}

// TestBuffered_LifecycleFlushImmediate - lifecycle events bypass the
// buffer and land immediately.
func TestBuffered_LifecycleFlushImmediate(t *testing.T) {
	rs := &recordingSink{}
	// Large window so only lifecycle-flush can surface events.
	b := Buffered(rs, time.Hour)
	defer b.(interface{ Close() error }).Close()

	ctx := context.Background()
	b.OnEvent(ctx, zenflow.Event{Type: zenflow.EventStepStart, StepID: "a"})

	// Give goroutines a chance to schedule.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		ev, _ := rs.snap()
		if len(ev) == 1 && ev[0].Type == zenflow.EventStepStart {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	ev, _ := rs.snap()
	t.Fatalf("lifecycle event not flushed immediately: got %d events", len(ev))
}

// TestBuffered_DeltaCoalesced - non-lifecycle events are held until the
// window elapses (or Close).
func TestBuffered_DeltaCoalesced(t *testing.T) {
	rs := &recordingSink{}
	b := Buffered(rs, 50*time.Millisecond)
	defer b.(interface{ Close() error }).Close()

	ctx := context.Background()
	for i := 0; i < 10; i++ {
		b.OnEvent(ctx, zenflow.Event{Type: zenflow.EventCoordinatorNarration, Message: "x"})
	}

	// Immediately after send, buffer should still hold them.
	ev, _ := rs.snap()
	if len(ev) != 0 {
		t.Fatalf("delta events not coalesced: got %d before window", len(ev))
	}

	// After window elapses, events flush.
	time.Sleep(120 * time.Millisecond)
	ev, _ = rs.snap()
	if len(ev) != 10 {
		t.Fatalf("want 10 events after window, got %d", len(ev))
	}
}

// TestBuffered_LifecycleFlushesPendingDeltas - a lifecycle event must
// flush any queued delta events BEFORE itself so downstream ordering
// matches source ordering.
func TestBuffered_LifecycleFlushesPendingDeltas(t *testing.T) {
	rs := &recordingSink{}
	b := Buffered(rs, time.Hour)
	defer b.(interface{ Close() error }).Close()

	ctx := context.Background()
	b.OnEvent(ctx, zenflow.Event{Type: zenflow.EventCoordinatorNarration, Message: "1"})
	b.OnEvent(ctx, zenflow.Event{Type: zenflow.EventCoordinatorNarration, Message: "2"})
	b.OnEvent(ctx, zenflow.Event{Type: zenflow.EventStepEnd, StepID: "a"})

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		ev, _ := rs.snap()
		if len(ev) >= 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	ev, _ := rs.snap()
	if len(ev) != 3 {
		t.Fatalf("want 3 events, got %d", len(ev))
	}
	if ev[0].Message != "1" || ev[1].Message != "2" || ev[2].Type != zenflow.EventStepEnd {
		t.Fatalf("ordering broken: %+v", ev)
	}
}

// TestBuffered_CloseFlushes - Close drains any pending batched events.
func TestBuffered_CloseFlushes(t *testing.T) {
	rs := &recordingSink{}
	b := Buffered(rs, time.Hour)

	ctx := context.Background()
	b.OnEvent(ctx, zenflow.Event{Type: zenflow.EventCoordinatorNarration, Message: "q"})
	b.OnOutput(ctx, zenflow.Output{Delta: "hi"})

	if err := b.(interface{ Close() error }).Close(); err != nil {
		t.Fatal(err)
	}
	ev, out := rs.snap()
	if len(ev) != 1 || len(out) != 1 {
		t.Fatalf("Close did not flush: events=%d outputs=%d", len(ev), len(out))
	}
}

// TestBuffered_DefaultWindow - passing window <= 0 applies the 100ms default.
func TestBuffered_DefaultWindow(t *testing.T) {
	rs := &recordingSink{}
	b := Buffered(rs, 0) // window <= 0 branch
	defer b.(interface{ Close() error }).Close()

	ctx := context.Background()
	b.OnEvent(ctx, zenflow.Event{Type: zenflow.EventCoordinatorNarration, Message: "d"})

	// At 100ms default, the event should flush within ~250ms.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		ev, _ := rs.snap()
		if len(ev) == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	ev, _ := rs.snap()
	t.Fatalf("default-window flush failed: got %d events", len(ev))
}

// TestBuffered_LoopDrainsItemsBehindFlushSentinel deterministically
// exercises the drain inner-loop in BufferedSink.loop (buffered.go:184-203).
// Setup: stop the auto-spawned loop by sending an isFlush sentinel
// pre-loaded with extra items behind it in the buffered channel. The
// loop picks up the flush, then drains the buffered extras (event,
// output, and a stray secondary flush) synchronously to the wrapped
// sink. Race-free because we never invoke OnEvent/OnOutput from a
// concurrent goroutine - the buffered chan's FIFO is observed
// directly.
func TestBuffered_LoopDrainsItemsBehindFlushSentinel(t *testing.T) {
	rs := &recordingSink{}
	// Construct the BufferedSink manually to bypass the goroutine spawn
	// and drive the loop on the test's goroutine.
	b := &BufferedSink{
		wrapped: rs,
		window:  time.Hour,
		in:      make(chan bufItem, 16),
		done:    make(chan struct{}),
	}
	ctx := context.Background()
	// Push the primary flush sentinel FIRST so the loop picks it as
	// `it` in the outer select. Then enqueue the drain items behind it
	// - the inner drain loop reads them via a non-blocking select.
	primaryAck := make(chan struct{})
	b.in <- bufItem{isFlush: true, ack: primaryAck}
	b.in <- bufItem{ctx: ctx, ev: zenflow.Event{Message: "drained-event"}}
	b.in <- bufItem{ctx: ctx, out: zenflow.Output{Delta: "drained-out"}, isOut: true}
	// Stray secondary flush sentinel (with ack channel) - covers the
	// `if extra.isFlush { close(extra.ack); continue }` defensive branch.
	strayAck := make(chan struct{})
	b.in <- bufItem{isFlush: true, ack: strayAck}
	// One more event after the stray sentinel to prove drain continues.
	b.in <- bufItem{ctx: ctx, ev: zenflow.Event{Message: "post-stray"}}

	// Run the loop on this goroutine. It returns after seeing the
	// primary flush + drain.
	b.loop()

	// Both ack channels must have been closed.
	select {
	case <-primaryAck:
	default:
		t.Fatal("primary flush ack was not closed")
	}
	select {
	case <-strayAck:
	default:
		t.Fatal("stray flush ack was not closed (defensive branch)")
	}
	ev, out := rs.snap()
	if len(ev) != 2 {
		t.Errorf("events drained = %d, want 2 (drained-event + post-stray)", len(ev))
	}
	if len(out) != 1 {
		t.Errorf("outputs drained = %d, want 1 (drained-out)", len(out))
	}
}

// TestBuffered_PostCloseForwards - OnEvent/OnOutput after Close must
// forward synchronously to the wrapped sink (the <-b.done case).
// The select between b.done (closed) and b.in (buffered, capacity 1024)
// is non-deterministic: both cases may be ready. We call OnEvent/OnOutput
// many times so the <-b.done branch is selected at least once for each.
func TestBuffered_PostCloseForwards(t *testing.T) {
	rs := &recordingSink{}
	b := Buffered(rs, 50*time.Millisecond)
	if err := b.(interface{ Close() error }).Close(); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	const n = 256
	for i := 0; i < n; i++ {
		b.OnEvent(ctx, zenflow.Event{Type: zenflow.EventCoordinatorNarration, Message: "post"})
		b.OnOutput(ctx, zenflow.Output{Delta: "post-out"})
	}

	ev, out := rs.snap()
	if len(ev) == 0 {
		t.Fatal("post-close OnEvent never took b.done branch")
	}
	if len(out) == 0 {
		t.Fatal("post-close OnOutput never took b.done branch")
	}
	for _, e := range ev {
		if e.Message != "post" {
			t.Fatalf("bad event: %+v", e)
		}
	}
	for _, o := range out {
		if o.Delta != "post-out" {
			t.Fatalf("bad output: %+v", o)
		}
	}
}
