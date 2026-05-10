// Package exec - drop_fanout.go contains the dropFanout dispatcher
// for DropEvent callbacks (Plan §12.1 / F3 -
// WithDropCallback / WithDropCallbackBufferSize). It supports both
// synchronous dispatch (bufSize <= 0) and an async buffered worker
// goroutine path. The "zero silent drops" invariant is enforced via
// a synchronous fallback when the buffered channel is full.
package exec

import (
	"log/slog"
	"sync"
)

// dropFanout dispatches DropEvents to a user-supplied callback. When
// bufSize > 0, dispatch goes through a buffered channel + worker
// goroutine so the user's callback latency does not stall router
// critical paths. When bufSize <= 0, dispatch is synchronous. Plan
// §12.1 / F3 (WithDropCallback / WithDropCallbackBufferSize).
// Overflow contract: if the buffered channel is full, dispatch falls
// back to synchronous invocation rather than dropping the event - the
// "zero silent drops" invariant must hold for the user observer too.
// R8#13 - No SDK-side coalescing. The integration plan (§9.2, §26.3
// "Drop aggregation window") places drop aggregation on the CONSUMER
// side (the consumer's drop aggregator with a 100ms wall-clock window).
// zenflow's dropFanout is a pure fan-out with at-least-once semantics:
// it forwards every DropEvent exactly once to the registered callback
// and lets the consumer decide whether to render each event or batch
// them into a single WarningItem. Moving the coalescer into zenflow
// would (a) force a time-window policy on stdout/JSON sink consumers
// who want every event, and (b) break the §9.2 contract that "1 drop
// = 1 callback invocation" regardless of consumer rendering choices.
// Callers that want batching compose a coalescer on top of their own
// callback; the SDK stays policy-free.
type dropFanout struct {
	cb     func(DropEvent)
	events chan DropEvent
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
}

func newDropFanout(cb func(DropEvent), bufSize int) *dropFanout {
	if cb == nil {
		return nil
	}
	d := &dropFanout{cb: cb}
	if bufSize > 0 {
		d.events = make(chan DropEvent, bufSize)
		d.stop = make(chan struct{})
		d.done = make(chan struct{})
		go d.run()
	}
	return d
}

func (d *dropFanout) run() {
	defer close(d.done)
	for {
		select {
		case <-d.stop:
			// Drain remaining buffered events before exit.
			for {
				select {
				case e := <-d.events:
					d.invoke(e)
				default:
					return
				}
			}
		case e := <-d.events:
			d.invoke(e)
		}
	}
}

func (d *dropFanout) invoke(e DropEvent) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("drop callback panic recovered", "panic", r) // F3 isolation
		}
	}()
	d.cb(e)
}

func (d *dropFanout) dispatch(e DropEvent) {
	if d == nil {
		return
	}
	if d.events == nil {
		// Synchronous path.
		d.invoke(e)
		return
	}
	// Single-select dispatch with three arms covering all cases:
	// - buffered send: room in queue, worker alive → enqueue and return
	// - <-d.stop: worker has exited (or is exiting); honor "zero
	// silent drops" by invoking synchronously
	// - default: queue full but worker still alive; load-shed by
	// invoking synchronously rather than blocking the
	// caller. A misbehaving callback can stall a Send,
	// but we'd rather surface latency than lose events
	// or block the dispatcher's caller indefinitely.
	select {
	case d.events <- e:
	case <-d.stop:
		d.invoke(e)
	default:
		d.invoke(e)
	}
}

func (d *dropFanout) Stop() {
	if d == nil || d.events == nil {
		return
	}
	d.once.Do(func() { close(d.stop) })
	<-d.done
}
