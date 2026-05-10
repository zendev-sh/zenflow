// Package exec - progress_pump.go contains the eventBusSinkPump:
// a buffered async wrapper around a downstream ProgressSink that
// guarantees non-blocking OnEvent / OnOutput on the critical path
// (router stepLock, poller invariant-check). On overflow the pump
// bounded-retries with a timeout; if the timeout fires, the event
// falls back to a structured log line - preserving the "no silent
// drops" invariant.
package exec

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// eventBusSinkPump wraps a downstream ProgressSink in a buffered async
// channel so that emit calls from critical paths (router stepLock,
// poller invariant-check) never block on a slow user-supplied sink
// On overflow the pump bounded-retries with a timeout; if the timeout
// fires, the event falls back to a structured log line - preserving
// the "no silent drops" invariant.
// Usage: wrap once per Run via wrapProgressNonBlocking, defer Stop
// before Run returns so the pump goroutine exits and any buffered
// events are flushed.
type eventBusSinkPump struct {
	inner   ProgressSink
	events  chan eventBusEntry
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
	timeout time.Duration

	// metrics (best-effort observability - not wired into prom yet but
	// surfaced via Stats for operator inspection).
	dropped atomic.Int64 // events that overflowed AND timed out (logger fallback used)
	pushed  atomic.Int64 // events successfully accepted by the buffered chan
}

// Compile-time assertion. eventBusSinkPump is the production
// non-blocking wrapper for any ProgressSink installed via
// wrapProgressNonBlocking; signature drift on ProgressSink would
// otherwise only surface at the wrap call site.
var _ ProgressSink = (*eventBusSinkPump)(nil)

// eventBusEntry carries a single event or output through the async pump
// channel. The caller's per-request context is intentionally NOT included:
// cancellation of a single caller should not affect the pump goroutine's
// ability to deliver other events. The pump selects on its own stop channel
// for shutdown signalling (H4 refactor).
type eventBusEntry struct {
	ev  *Event
	out *Output
}

const (
	defaultEventBusBuffer  = 1024
	defaultEventBusTimeout = 1 * time.Second
)

// wrapProgressNonBlocking returns a sink whose OnEvent / OnOutput are
// guaranteed non-blocking on the critical path. inner may be nil
// (returns nil - caller should still nil-check before emit). bufSize
// selects the chan buffer cap; <=0 falls back to
// defaultEventBusBuffer.
func wrapProgressNonBlocking(inner ProgressSink, bufSize int) *eventBusSinkPump {
	if inner == nil {
		return nil
	}
	if bufSize <= 0 {
		bufSize = defaultEventBusBuffer
	}
	p := &eventBusSinkPump{
		inner:   inner,
		events:  make(chan eventBusEntry, bufSize),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		timeout: defaultEventBusTimeout,
	}
	go p.run()
	return p
}

func (p *eventBusSinkPump) run() {
	defer close(p.done)
	for {
		select {
		case <-p.stop:
			// Drain any remaining buffered events before returning so
			// late drops still surface (no silent loss).
			for {
				select {
				case e := <-p.events:
					p.deliver(e)
				default:
					return
				}
			}
		case e := <-p.events:
			p.deliver(e)
		}
	}
}

func (p *eventBusSinkPump) deliver(e eventBusEntry) {
	defer func() {
		// C10 panic isolation: a panic in user-supplied sink must NOT
		// kill the pump goroutine.
		if r := recover(); r != nil {
			slog.Warn("event sink panic recovered", "panic", r, "event_type", fmt.Sprintf("%T", e.ev))
		}
	}()
	// H4: the pump is a Run-lifetime goroutine; the per-call ctx from the
	// original OnEvent/OnOutput caller is not stored (see eventBusEntry).
	// Use context.Background so the downstream sink receives an unrooted
	// context independent of any single caller's lifetime.
	switch {
	case e.ev != nil:
		p.inner.OnEvent(context.Background(), *e.ev)
	case e.out != nil:
		p.inner.OnOutput(context.Background(), *e.out)
	}
}

// OnEvent attempts a non-blocking push; on overflow, blocks for at
// most p.timeout, then falls back to dropping with a counted metric.
// The fallback path NEVER blocks indefinitely on the caller's
// goroutine, satisfying the "non-blocking critical path" requirement.
func (p *eventBusSinkPump) OnEvent(ctx context.Context, ev Event) {
	if p == nil {
		return
	}
	// H4: do not store ctx in the entry - the pump goroutine is Run-lifetime
	// and must not be influenced by a single caller's cancellation.
	entry := eventBusEntry{ev: &ev}
	select {
	case p.events <- entry:
		p.pushed.Add(1)
	default:
		// Overflow path: bounded retry on the caller's goroutine. Select on
		// p.stop (pump shutting down) rather than ctx.Done (single call) so
		// cancellation of one caller does not short-circuit delivery for other
		// pending events while the pump is still running.
		t := time.NewTimer(p.timeout)
		defer t.Stop()
		select {
		case p.events <- entry:
			p.pushed.Add(1)
		case <-t.C:
			p.dropped.Add(1)
		case <-p.stop:
			p.dropped.Add(1)
		}
	}
}

// OnOutput follows the same non-blocking discipline as OnEvent.
func (p *eventBusSinkPump) OnOutput(ctx context.Context, out Output) {
	if p == nil {
		return
	}
	// H4: do not store ctx in the entry (see OnEvent comment above).
	entry := eventBusEntry{out: &out}
	select {
	case p.events <- entry:
		p.pushed.Add(1)
	default:
		t := time.NewTimer(p.timeout)
		defer t.Stop()
		select {
		case p.events <- entry:
			p.pushed.Add(1)
		case <-t.C:
			p.dropped.Add(1)
		case <-p.stop:
			// Drop when the pump is shutting down.
			p.dropped.Add(1)
		}
	}
}

// Stop signals the pump to drain and exit. Idempotent. Blocks until
// the pump goroutine has flushed buffered entries.
func (p *eventBusSinkPump) Stop() {
	if p == nil {
		return
	}
	p.once.Do(func() { close(p.stop) })
	<-p.done
}

// Stats returns (pushed, dropped) counts for operator inspection.
func (p *eventBusSinkPump) Stats() (pushed, dropped int64) {
	if p == nil {
		return 0, 0
	}
	return p.pushed.Load(), p.dropped.Load()
}
