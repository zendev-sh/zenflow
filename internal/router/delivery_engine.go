package router

import (
	"context"
	"sync"
	"time"

	"github.com/zendev-sh/goai"
)

// EngineActiveStepsSource is the minimal subset of *Executor that the
// DeliveryEngine reads. Defined as an interface so tests can drop in a
// fake without standing up an Executor + Workflow + Storage.
type EngineActiveStepsSource interface {
	// ActiveSteps returns step IDs currently executing under this run.
	// The slice is a snapshot; subsequent ticks call again.
	ActiveSteps() []string
	// AgentState returns the *goai.AgentState for stepID, or nil if the
	// step has not been registered (or was unregistered after
	// completion).
	AgentState(stepID string) *goai.AgentState
}

// EngineWakeTarget is the per-step wake handle the engine signals. In
// production each *AgentRunner exposes its Wake channel via a small
// adapter. The interface keeps tests independent of AgentRunner's
// surface.
type EngineWakeTarget interface {
	// SignalWake is non-blocking: if the wake channel already has a
	// pending signal, the call is a no-op (cap-1 buffer semantics).
	SignalWake()
}

// EngineWakeRegistry is the lookup the engine uses to find the wake
// target for a given stepID. Returns nil when no target is registered
// (e.g. the step has not yet been admitted into the executor's mailbox
// path or was already unregistered at end-of-step).
type EngineWakeRegistry interface {
	WakeTarget(stepID string) EngineWakeTarget
}

// EngineStepLocker is the optional interface the engine uses to acquire
// a per-step RWMutex for the C5a "read-then-wake" atomicity invariant
// to enforce read-then-wake atomicity. The poller's Observe+SignalWake sequence MUST run
// under stepLock.RLock so that a concurrent Run-return defer (which
// takes the write-lock and calls SetTerminal) cannot transition the
// state between the read and the wake send. When the engine's source
// implements this interface, the poll loop wraps each step's
// Observe+wake sequence in RLock/RUnlock; when it does not, the engine
// falls back to lock-free polling (acceptable for tests and pre-stepLock
// callers - a spurious wake against a freshly-terminated step is
// harmless because the wake channel has cap 1 and the runner's defer
// already unregistered the wake target).
type EngineStepLocker interface {
	AcquireStepLock(stepID string) *sync.RWMutex
}

// ChanWakeTarget is the production EngineWakeTarget: it wraps a
// buffered chan struct{} of capacity 1 (matching the AgentRunner wake
// contract). Sends are non-blocking; if the channel already holds a
// pending wake, the duplicate is dropped - a single wake suffices to
// flush the entire mailbox.
type ChanWakeTarget struct {
	ch chan struct{}
}

// NewChanWakeTarget wraps an existing wake channel. The channel MUST be
// buffered with cap >= 1; a cap-0 channel would cause SignalWake to
// drop signals deterministically when the agent is mid-LLM-call (no
// reader yet) and break the "wake at idle" guarantee.
func NewChanWakeTarget(ch chan struct{}) EngineWakeTarget {
	return &ChanWakeTarget{ch: ch}
}

// SignalWake implements EngineWakeTarget.
func (t *ChanWakeTarget) SignalWake() {
	if t == nil || t.ch == nil {
		return
	}
	select {
	case t.ch <- struct{}{}:
	default:
		// Already has a pending wake - coalesce.
	}
}

// MapWakeRegistry is a minimal in-memory EngineWakeRegistry. The
// Executor populates it as steps start and clears entries as they end.
type MapWakeRegistry struct {
	mu      sync.Mutex
	targets map[string]EngineWakeTarget
}

// NewWakeRegistry returns an empty registry safe for concurrent
// Register / Unregister / WakeTarget calls.
func NewWakeRegistry() *MapWakeRegistry {
	return &MapWakeRegistry{targets: make(map[string]EngineWakeTarget)}
}

// Register stores the wake target for stepID. Overwrites any prior
// entry - useful for retried steps that reallocate their Wake channel.
func (r *MapWakeRegistry) Register(stepID string, t EngineWakeTarget) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.targets[stepID] = t
}

// Unregister removes stepID from the registry. Called from the
// runStep deferred cleanup so the engine stops trying to wake a
// completed step.
func (r *MapWakeRegistry) Unregister(stepID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.targets, stepID)
}

// WakeTarget implements EngineWakeRegistry.
func (r *MapWakeRegistry) WakeTarget(stepID string) EngineWakeTarget {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.targets[stepID]
}

// EngineClock abstracts time.Tick so tests can drive ticks
// deterministically. Production uses RealClock; tests use fakeClock.
// Tick returns a receive-only chan that yields once per tick boundary.
// The chan must remain valid for the engine's lifetime; the engine
// drains it once per loop iteration.
type EngineClock interface {
	Tick(d time.Duration) <-chan time.Time
	Stop()
}

// RealClock is the production EngineClock. It wraps time.NewTicker.
type RealClock struct {
	t *time.Ticker
}

// - compile-time assertions catching signature drift on the
// internal engine* interfaces at the implementation definitions.
var (
	_ EngineClock        = (*RealClock)(nil)
	_ EngineWakeTarget   = (*ChanWakeTarget)(nil)
	_ EngineWakeRegistry = (*MapWakeRegistry)(nil)
)

// Tick starts a new ticker firing every d and returns its channel.
// If a prior ticker is still running on this RealClock (Tick called
// multiple times without an intervening Stop), the prior ticker is
// stopped so its goroutine and channel can be GC'd; otherwise repeated
// Tick calls would leak tickers. Not safe for concurrent calls on the
// same RealClock.
func (c *RealClock) Tick(d time.Duration) <-chan time.Time {
	if c.t != nil {
		c.t.Stop()
	}
	c.t = time.NewTicker(d)
	return c.t.C
}

// Stop stops the underlying ticker started by Tick. Safe to call when
// Tick has not been called (no-op) or after a previous Stop. After
// Stop the channel returned by Tick will not receive further ticks.
func (c *RealClock) Stop() {
	if c.t != nil {
		c.t.Stop()
	}
}

// DeliveryEngine is the per-run goroutine that drives mailbox-based
// message delivery. It periodically polls each active step's mailbox
// and signals the step's wake channel when the agent is StepIdle and
// has unread messages waiting. Engine never modifies the mailbox or
// the agent state - it is purely an observer + wake signaller.
// Lifecycle: one engine per workflow run. Started after the executor
// has wired up its router/mailbox/registry; stopped via context
// cancellation when Run completes or aborts.
// Tick cadence: 500ms by default, overridable via WithTickInterval for
// tests. The tradeoff is delivery latency vs. CPU overhead: 500ms is
// well below human-perceptible response time and far above any
// reasonable tick cost.
type DeliveryEngine struct {
	source       EngineActiveStepsSource
	mailbox      MailboxStore
	registry     EngineWakeRegistry
	stepLocker   EngineStepLocker // optional - see EngineStepLocker godoc
	tickInterval time.Duration
	clock        EngineClock
}

// TickInterval reports the engine's configured tick cadence. Tests use
// this to verify WithEngineTickInterval / default-fallback wiring.
// Stable.
func (e *DeliveryEngine) TickInterval() time.Duration { return e.tickInterval }

// PollOne triggers a single poll cycle for stepID, wiring through the
// usual mailbox / wake / step-locker pipeline. Exposed for tests that
// need to drive a specific stepID without spinning up Start.
// Stable.
func (e *DeliveryEngine) PollOne(stepID string) { e.pollOne(stepID) }

// WithStepLocker wires the per-step RWMutex acquirer into the engine so
// the poll loop can satisfy the C5a "read-then-wake" atomicity
// invariant. When unset, the engine polls without stepLock - see
// EngineStepLocker godoc for why this is safe in test contexts.
func WithStepLocker(l EngineStepLocker) EngineOption {
	return func(e *DeliveryEngine) {
		if l != nil {
			e.stepLocker = l
		}
	}
}

// EngineOption configures a DeliveryEngine.
type EngineOption func(*DeliveryEngine)

// WithEngineTickInterval overrides the default 500ms tick cadence. A
// non-positive value reverts to the default. Provided primarily for
// tests that want a fast tick - production callers should rely on the
// default.
func WithEngineTickInterval(d time.Duration) EngineOption {
	return func(e *DeliveryEngine) {
		if d > 0 {
			e.tickInterval = d
		}
	}
}

// WithEngineClock substitutes the engine's tick source. Tests pass a
// fakeClock to drive ticks deterministically; production code does not
// need this option.
func WithEngineClock(c EngineClock) EngineOption {
	return func(e *DeliveryEngine) {
		if c != nil {
			e.clock = c
		}
	}
}

// NewDeliveryEngine constructs a DeliveryEngine with the supplied
// source, mailbox, and wake registry. None may be nil - a nil
// dependency would silently no-op an entire role and mask wiring
// bugs.
func NewDeliveryEngine(source EngineActiveStepsSource, mailbox MailboxStore, registry EngineWakeRegistry, opts ...EngineOption) *DeliveryEngine {
	e := &DeliveryEngine{
		source:       source,
		mailbox:      mailbox,
		registry:     registry,
		tickInterval: 500 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(e)
	}
	if e.clock == nil {
		e.clock = &RealClock{}
	}
	return e
}

// Start spawns the engine goroutine. Returns immediately; the goroutine
// runs until ctx is cancelled. Callers may rely on a separate
// synchronization signal (channel close, WaitGroup) to confirm shutdown
// when needed; for unit tests, the goroutine exits on the next tick or
// ctx-done select fire after cancellation.
// done returns a channel that closes once the engine goroutine has
// exited. This is the supported way to wait for shutdown - pollers,
// fakes, and tests should select on done rather than racing on
// internal state.
func (e *DeliveryEngine) Start(ctx context.Context) (done <-chan struct{}) {
	doneCh := make(chan struct{})
	tickCh := e.clock.Tick(e.tickInterval)
	go func() {
		defer close(doneCh)
		defer e.clock.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tickCh:
				e.poll()
			}
		}
	}()
	return doneCh
}

// poll runs one tick of the engine. For each active step it reads the
// agent state and the mailbox; when the agent is idle AND has unread
// messages, it signals the step's wake target. This is the heart of
// +.
// Race-safety: every read is either a single atomic load
// (AgentState.Observe), a mutex-protected map read (Mailbox.Unread,
// registry lookup), or a non-blocking channel send (wake signal).
// Writes to the mailbox happen via Router.Send on other goroutines; we
// take a snapshot via Unread (which copies) and never hold a reference
// to the mailbox's internal slice.
func (e *DeliveryEngine) poll() {
	steps := e.source.ActiveSteps()
	for _, stepID := range steps {
		e.pollOne(stepID)
	}
}

// pollOne evaluates the wake-eligibility invariants for a single step
// and signals its wake target if the agent is StepIdle with unread
// messages. When stepLocker is configured, the Observe + SignalWake
// sequence runs under stepLock.RLock to satisfy the
// "read-then-wake TOCTOU" invariant - a concurrent Run-return defer
// that wants to transition state to a terminal kind must take
// stepLock.Lock and therefore waits until our RUnlock. Without the
// lock, the engine could observe StepIdle, then a racing terminal
// transition fires, and we'd send a wake to a runner that is about to
// exit. The wake channel has cap 1 so the spurious signal is dropped
// at unregister time, but the lock prevents the race entirely.
func (e *DeliveryEngine) pollOne(stepID string) {
	state := e.source.AgentState(stepID)
	if state == nil {
		return
	}
	if e.stepLocker != nil {
		lock := e.stepLocker.AcquireStepLock(stepID)
		lock.RLock()
		defer lock.RUnlock()
	}
	kind, _ := state.Observe()
	if kind != goai.StepIdle {
		// Agent is busy (StepLLMInFlight / StepToolExecuting / etc.)
		// - do not interrupt. Wake fires only between iterations.
		return
	}
	if len(e.mailbox.Unread(stepID)) == 0 {
		return
	}
	target := e.registry.WakeTarget(stepID)
	if target == nil {
		// Step was unregistered between ActiveSteps and now (its
		// runStep deferred Unregister fired). Nothing to wake; the
		// engine will simply skip on subsequent ticks once the step
		// also drops from ActiveSteps.
		return
	}
	target.SignalWake()
}
