package sink

import (
	"context"
	"sync"
	"time"

	"github.com/zendev-sh/zenflow"
)

// defaultBufSinkBuffer is the channel capacity for the BufferedSink
// dispatcher. 1024 absorbs typical burst sizes (narration + tool_call
// deltas) without blocking callers. Drops are intentionally absent at
// this layer - explicit drop accounting belongs to the router (see
// DropReasonMailboxFull). Kept package-private; callers that need a
// different cap should wrap or implement their own ProgressSink.
const defaultBufSinkBuffer = 1024

// BufferedSink is a ProgressSink that batches high-volume delta events
// (narration, agent_turn, tool_call, output, etc.) within a time window
// and flushes immediately on lifecycle events (see IsLifecycleEvent).
// The returned sink implements zenflow.ProgressSink and an additional
// Close error method that flushes any pending batch - call it when
// the run ends to avoid losing the final window of deltas.
// Backpressure: OnEvent/OnOutput block when the internal buffer is
// full (defaults to 1024 entries); consumers wrapping a slow sink
// should choose a larger buffer to avoid coupling LLM throughput to
// UI responsiveness.
// Goroutine model: a single dispatcher goroutine owns the batch and
// the timer. Callers submit work via a buffered channel; lifecycle
// events cause the dispatcher to emit the current batch (in order)
// followed immediately by the lifecycle event itself.
type BufferedSink struct {
	wrapped zenflow.ProgressSink
	window  time.Duration

	in   chan bufItem
	done chan struct{}

	closeOnce sync.Once
}

// Compile-time assertions. Catch signature drift on both the base
// ProgressSink contract and the ClosableProgressSink shape that
// Buffered returns.
var (
	_ zenflow.ProgressSink = (*BufferedSink)(nil)
	_ ClosableProgressSink = (*BufferedSink)(nil)
)

type bufItem struct {
	ctx     context.Context
	ev      zenflow.Event
	out     zenflow.Output
	isOut   bool
	isFlush bool // Close signal
	// Result channel for synchronous Close flush.
	ack chan struct{}
}

// ClosableProgressSink is a ProgressSink that owns shutdown semantics.
// Exposed so callers of Buffered can hold the concrete shutdown
// contract via an exported named interface instead of the anonymous
// interface{ Close error } type assertion ceremony. defer Close
// is the canonical shutdown pattern; the interface lets callers store
// the result without knowing the unexported *BufferedSink type.
type ClosableProgressSink interface {
	zenflow.ProgressSink
	Close() error
}

// Buffered returns a ProgressSink that coalesces delta events within
// the given window. When window <= 0, defaults to 100ms.
// The return type is ClosableProgressSink so callers can defer b.Close
// directly without an anonymous type assertion. The underlying
// *BufferedSink already exposes Close error; this just surfaces the
// contract through the static type.
func Buffered(wrapped zenflow.ProgressSink, window time.Duration) ClosableProgressSink {
	if window <= 0 {
		window = 100 * time.Millisecond
	}
	b := &BufferedSink{
		wrapped: wrapped,
		window:  window,
		// Large enough to absorb bursts without blocking callers. If
		// overflowed, OnEvent/OnOutput block - safer than drop (drops
		// are explicit at the router layer, not here).
		in:   make(chan bufItem, defaultBufSinkBuffer),
		done: make(chan struct{}),
	}
	go b.loop()
	return b
}

// OnEvent queues an event. Lifecycle events are flushed immediately
// (the dispatcher emits any pending batch first to preserve order).
func (b *BufferedSink) OnEvent(ctx context.Context, e zenflow.Event) {
	select {
	case <-b.done:
		// Post-close: forward synchronously so nothing is silently dropped.
		b.wrapped.OnEvent(ctx, e)
	case b.in <- bufItem{ctx: ctx, ev: e}:
	}
}

// OnOutput queues an output delta for coalescing.
func (b *BufferedSink) OnOutput(ctx context.Context, o zenflow.Output) {
	select {
	case <-b.done:
		b.wrapped.OnOutput(ctx, o)
	case b.in <- bufItem{ctx: ctx, out: o, isOut: true}:
	}
}

// Close drains any pending batch and stops the dispatcher. Safe to
// call multiple times.
// Order matters: close(b.done) runs BEFORE the flush dance so concurrent
// OnEvent/OnOutput callers prefer the wrapped (synchronous) path instead
// of racing into b.in. The loop also drains any items that snuck into
// b.in after isFlush was queued (FIFO would otherwise leave them stuck
// behind the flush sentinel and silently dropped when the loop returns).
func (b *BufferedSink) Close() error {
	b.closeOnce.Do(func() {
		// Signal closed first: new OnEvent/OnOutput calls bias toward the
		// b.done branch and forward synchronously to the wrapped sink.
		close(b.done)
		ack := make(chan struct{})
		b.in <- bufItem{isFlush: true, ack: ack}
		<-ack
	})
	return nil
}

func (b *BufferedSink) loop() {
	var (
		batchEv  []bufItem
		batchOut []bufItem
		timer    *time.Timer
		timerC   <-chan time.Time
	)

	startTimer := func() {
		if timer == nil {
			timer = time.NewTimer(b.window)
			timerC = timer.C
		}
	}
	stopTimer := func() {
		if timer != nil {
			// Go 1.23+ no longer requires draining timer.C after Stop.
			timer.Stop()
			timer = nil
			timerC = nil
		}
	}

	flush := func() {
		for _, it := range batchEv {
			b.wrapped.OnEvent(it.ctx, it.ev)
		}
		for _, it := range batchOut {
			b.wrapped.OnOutput(it.ctx, it.out)
		}
		batchEv = batchEv[:0]
		batchOut = batchOut[:0]
		stopTimer()
	}

	for {
		select {
		case it := <-b.in:
			if it.isFlush {
				flush()
				// Drain any items that snuck into b.in behind the flush
				// sentinel (Close closed b.done first, but OnEvent's select
				// is non-deterministic and may still pick the b.in branch).
				// Forward them synchronously to avoid silent drops.
			drain:
				for {
					select {
					case extra := <-b.in:
						if extra.isFlush {
							// Defensive: a second Close cannot happen
							// (closeOnce), but tolerate stray sentinels.
							if extra.ack != nil {
								close(extra.ack)
							}
							continue
						}
						if extra.isOut {
							b.wrapped.OnOutput(extra.ctx, extra.out)
						} else {
							b.wrapped.OnEvent(extra.ctx, extra.ev)
						}
					default:
						break drain
					}
				}
				close(it.ack)
				return
			}
			if it.isOut {
				batchOut = append(batchOut, it)
				startTimer()
				continue
			}
			// Event.
			if IsLifecycleEvent(it.ev) {
				// Flush pending batch, then emit lifecycle event.
				flush()
				b.wrapped.OnEvent(it.ctx, it.ev)
				continue
			}
			batchEv = append(batchEv, it)
			startTimer()

		case <-timerC:
			flush()
		}
	}
}
