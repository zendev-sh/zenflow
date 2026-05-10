package exec

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// DefaultAgentHandleTTL is the start-to-finish wall-clock cap on a
// RunAgentAsync handle. When exceeded, the handle is force-completed
// with AgentError{Sentinel: ErrAgentHandleTimeout} and the inner
// context is cancelled. The TTL is NOT reset by progress events.
// Override at orchestrator-construction time via WithAgentHandleTTL.
// CLI consumers may map the ZENFLOW_AGENT_HANDLE_TTL environment
// variable to that option (see cmd/zenflow's orchestrator_opts.go);
// the library itself does not read environment variables.
const DefaultAgentHandleTTL = 30 * time.Minute

// closeDrainDeadline caps the total wall-clock time Orchestrator.Close
// waits for in-flight agent goroutines to drain (observable lifecycle
// drain). Variable (not const) so tests can shorten it via
// setCloseDrainDeadlineForTest.
var closeDrainDeadline = 5 * time.Second

// setCloseDrainDeadlineForTest swaps closeDrainDeadline for a single
// test and returns the previous value so the caller can restore it.
// Test-only - production code must not call this.
func setCloseDrainDeadlineForTest(d time.Duration) time.Duration {
	prev := closeDrainDeadline
	closeDrainDeadline = d
	return prev
}

// Sentinel errors used by AgentError.Sentinel. Consumers should use
// errors.Is(err, ErrAgent*) to classify a failed handle.
var (
	// ErrAgentHandleTimeout signals the handle exceeded its TTL before
	// the agent returned. The handle is force-completed; the agent
	// goroutine may still be running and is cancelled via its ctx, but
	// its later arrival (if any) is discarded.
	ErrAgentHandleTimeout = errors.New("zenflow: agent handle TTL exceeded")

	// ErrAgentCancelled signals the handle was cancelled via
	// AgentHandle.Cancel before the agent completed normally.
	ErrAgentCancelled = errors.New("zenflow: agent cancelled")

	// ErrAgentPanicked signals the agent goroutine recovered a panic.
	// The recovered value is in AgentError.Msg.
	ErrAgentPanicked = errors.New("zenflow: agent panicked")

	// ErrInvalidAgentHandleID signals that NewAgentHandle was called
	// with an empty id. Returned instead of panicking so callers can
	// surface the misuse via the error path.
	ErrInvalidAgentHandleID = errors.New("zenflow: agent handle ID must be non-empty")
)

// AgentError wraps a sentinel error class with optional human-readable
// detail. errors.Is(AgentError{Sentinel: X}, X) returns true.
// Stable.
type AgentError struct {
	Sentinel error
	Msg      string
}

// Error implements the error interface. Format: "<sentinel>: <msg>" or
// just "<sentinel>" when Msg is empty.
func (e AgentError) Error() string {
	if e.Sentinel == nil {
		if e.Msg == "" {
			return "zenflow: agent error"
		}
		return e.Msg
	}
	if e.Msg == "" {
		return e.Sentinel.Error()
	}
	return e.Sentinel.Error() + ": " + e.Msg
}

// Unwrap exposes the sentinel so errors.Is and errors.As see through.
func (e AgentError) Unwrap() error { return e.Sentinel }

// AgentHandle is returned by Orchestrator.RunAgentAsync. The caller
// drives completion via Done and may force-terminate via Cancel.
// ID is stable for the lifetime of the handle and flows through any
// ProgressSink events emitted by the underlying AgentRunner. Format:
// "agent-<UUID v4>" (e.g. "agent-6ba7b810-9dad-11d1-80b4-00c04fd430c8").
// Stable.
type AgentHandle struct {
	ID string

	// done is buffered size 1 and carries the single terminal
	// AgentResult to an external `<-h.Done` reader. finish is the
	// only producer; it is guarded by `once` so at most one value is
	// ever sent. After the send, done is closed - subsequent receives
	// return the zero value per Go channel semantics.
	// INTERNAL READERS MUST NOT READ FROM `done` - doing so steals the
	// buffered value from the external reader and causes the external
	// Done read to observe a zero AgentResult. Internal consumers
	// (registry-cleanup goroutine, TTL watchdog) wait on `finished`
	// below instead.
	done chan AgentResult

	// finished is a close-only signal channel. finish closes it after
	// storing the result on done. Any number of internal goroutines
	// may `<-h.finished` to learn "the handle is terminal" without
	// consuming the buffered AgentResult on done.
	finished chan struct{}

	cancel context.CancelFunc

	// once guards the single send + close of done AND the close of
	// finished. All three transitions happen atomically inside
	// once.Do.
	once sync.Once

	// sessionID is the key under which the Orchestrator's handle
	// registry tracks this handle - copied from AgentConfig.SessionID
	// at RunAgentAsync time so the registry cleanup goroutine knows
	// which bucket to purge after Done closes. Empty string is a
	// valid bucket (single-session deployments).
	sessionID string
}

// NewAgentHandle constructs an AgentHandle with both internal
// channels (`done` for the external AgentResult reader + `finished`
// close-only signal for internal lifecycle goroutines) already
// initialized. Use this in tests that construct standalone
// AgentHandles outside of RunAgentAsync - direct struct literals
// risk skipping the `finished` field, which would nil-channel-hang
// any future internal reader that waits on it.
// Returns ErrInvalidAgentHandleID when id is empty so callers see the
// misuse via the error path rather than a runtime panic.
// Production code should go through Orchestrator.RunAgentAsync,
// not NewAgentHandle directly.
func NewAgentHandle(id string) (*AgentHandle, error) {
	if id == "" {
		return nil, ErrInvalidAgentHandleID
	}
	return &AgentHandle{
		ID:       id,
		done:     make(chan AgentResult, 1),
		finished: make(chan struct{}),
	}, nil
}

// Done returns the read-only channel that delivers the terminal
// AgentResult exactly once and is then closed. Multiple reads after
// close yield the zero value.
func (h *AgentHandle) Done() <-chan AgentResult { return h.done }

// Cancel force-terminates the agent run. The handle's Done will
// yield an AgentResult whose Error wraps ErrAgentCancelled. Calling
// Cancel multiple times is safe: only the first call wins.
func (h *AgentHandle) Cancel() error {
	if h == nil {
		slog.Debug("Cancel called on nil AgentHandle")
		return ErrNilAgentHandle
	}
	// finish FIRST so the Cancelled sentinel wins against the agent
	// goroutine, which would otherwise observe ctx.Done and race to
	// publish "context canceled" before we claim the done channel.
	h.finish(AgentResult{Error: AgentError{Sentinel: ErrAgentCancelled}})
	if h.cancel != nil {
		h.cancel()
	}
	return nil
}

// finish delivers res on h.done exactly once, then closes the channel.
// Safe to call from multiple goroutines (TTL watchdog, agent goroutine,
// Cancel). Only the first call wins.
func (h *AgentHandle) finish(res AgentResult) {
	h.once.Do(func() {
		// Non-blocking send because the channel is buffered size 1,
		// but guard with select to be explicit and avoid any future
		// regression if buffering changes.
		select {
		case h.done <- res:
		default:
		}
		close(h.done)
		// Signal internal consumers (registry-cleanup, TTL watchdog)
		// that the handle is terminal - they MUST read from `finished`,
		// not `done`, or they would steal the buffered AgentResult.
		close(h.finished)
	})
}

// resolveAgentHandleTTL returns configured (positive) or
// DefaultAgentHandleTTL. Zero/negative configured values fall back so
// the library never disables the TTL silently - callers that want a
// finite override must pass a positive duration via WithAgentHandleTTL.
func resolveAgentHandleTTL(configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	return DefaultAgentHandleTTL
}

// panicInRegistryCleanupHook and panicInTTLWatchdogHook are nil in
// production. Tests set them to a function that panics so the recover
// branches inside the registry-cleanup and TTL-watchdog goroutines are
// exercised without resorting to brittle race-window manipulation. The
// hooks are package-level vars rather than per-handle fields so they
// remain invisible to public API surface - only test code in this
// package can reach them.
var (
	panicInRegistryCleanupHook func(*AgentHandle)
	panicInTTLWatchdogHook     func(*AgentHandle)
)

// randRead is a test seam for newAgentHandleID's crypto/rand call.
// crypto/rand.Read documents that it never returns an error in normal
// operation and panics on truly catastrophic failure - but
// newAgentHandleID still defends with an explicit error wrap so the
// handle path stays string-error-only (no panic propagation up to the
// agent runner). Tests override randRead to exercise the wrap branch.
var randRead = rand.Read

// newAgentHandleID generates a stable "agent-<UUID v4>" identifier.
// Uses crypto/rand with the RFC 4122 version/variant bit patches so the
// output matches the UUID v4 format.
func newAgentHandleID() (string, error) {
	var b [16]byte
	if _, err := randRead(b[:]); err != nil {
		return "", fmt.Errorf("generate agent handle ID: %w", err)
	}
	// RFC 4122 §4.4: set version (4) and variant (10xx) bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"agent-%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16],
	), nil
}

// runAgentAsyncRunner is the package-level hook that RunAgentAsync
// invokes to actually run the agent. Production code points it at
// Orchestrator.RunAgent; tests override it to inject stub behavior
// (synthetic results, hangs, panics) without needing a real LLM.
// Signature matches Orchestrator.RunAgent: takes the full AgentConfig
// so every per-call field (Name, Model, MaxTurns, CallTools,
// ProgressSink, SubagentToolSet, SessionID, etc.) flows through to
// the runner - no silent field dropping.
var runAgentAsyncRunner = func(o *Orchestrator, ctx context.Context, cfg AgentConfig) (*AgentResult, error) {
	return o.RunAgent(ctx, cfg)
}

// SetRunAgentAsyncRunnerForTest swaps runAgentAsyncRunner so tests
// can capture the AgentConfig passed to RunAgentAsync without
// depending on a real LLM. Returns the previous runner so the caller
// can restore it.
// **WARNING: Test-only. NOT for production callers.** This function
// mutates a package-level variable visible to every concurrent
// goroutine in the process; calling it from production code would
// silently break every active and future RunAgentAsync invocation.
// Exported for cross-module test use: cross-module consumer tests
// (and other consumer packages that wire zenflow into their own task
// systems) need this to verify
// AgentConfig propagation without standing up a real provider. The
// "ForTest" suffix is the canonical Go marker for an exported
// test-only seam.
func SetRunAgentAsyncRunnerForTest(fn func(*Orchestrator, context.Context, AgentConfig) (*AgentResult, error)) func(*Orchestrator, context.Context, AgentConfig) (*AgentResult, error) {
	prev := runAgentAsyncRunner
	if fn != nil {
		runAgentAsyncRunner = fn
	}
	return prev
}

// RunAgentAsync spawns the primary agent in a goroutine and returns a
// handle immediately. The caller drives completion via handle.Done;
// ctx cancellation or handle.Cancel terminate the run early. A
// 30-minute default TTL (overridable via ZENFLOW_AGENT_HANDLE_TTL)
// force-completes the handle if the agent does not finish in time.
// Progress events flow through the ProgressSink registered on the
// orchestrator.
// Stable.
func (o *Orchestrator) RunAgentAsync(ctx context.Context, cfg AgentConfig) (*AgentHandle, error) {
	if o == nil {
		return nil, ErrNilOrchestrator
	}
	if o.closed.Load() {
		return nil, ErrOrchestratorClosed
	}
	id, err := newAgentHandleID()
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	h := &AgentHandle{
		ID:        id,
		done:      make(chan AgentResult, 1),
		finished:  make(chan struct{}),
		cancel:    cancel,
		sessionID: cfg.SessionID,
	}

	// Register the handle under its sessionID so ListAgentHandles
	// can surface it to the consumer's session-status query.
	// Registration happens BEFORE the goroutines start so a
	// ListAgentHandles call immediately after RunAgentAsync always
	// sees the handle.
	o.registerHandle(h)

	ttl := resolveAgentHandleTTL(o.agentHandleTTL)

	// Capture the runner hook once at launch so the spawned goroutine
	// does not race against test cleanup (saveRunnerHook) on the
	// package-level var. Production code sets the hook at init time
	// and never mutates it; tests swap it per-test and restore via
	// t.Cleanup, which may fire while the stub is still running.
	runner := runAgentAsyncRunner

	// Registry-cleanup watcher - fires once the handle finishes
	// (either terminal, TTL, cancel, or panic-recover) and removes
	// the handle from the per-session slice. Runs in its own
	// goroutine so it does not contend with the TTL watchdog or the
	// agent goroutine.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic in agent handle goroutine",
					"goroutine", "registry-cleanup",
					"handle_id", h.ID,
					"session_id", h.sessionID,
					"panic", r,
				)
			}
		}()
		// Wait on `finished` (close-only signal), NOT `done`, so we
		// never steal the buffered AgentResult that an external
		// `<-h.Done` reader must receive.
		<-h.finished
		// Panic-injection seam (test-only). Production: nil → no-op.
		// Tests assign panicInRegistryCleanupHook to exercise the
		// recover branch above without modifying production behaviour.
		if hook := panicInRegistryCleanupHook; hook != nil {
			hook(h)
		}
		o.unregisterHandle(h)
	}()

	// TTL watchdog. Fires once, then exits.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic in agent handle goroutine",
					"goroutine", "ttl-watchdog",
					"handle_id", h.ID,
					"session_id", h.sessionID,
					"panic", r,
				)
			}
		}()
		// Panic-injection seam (test-only). Production: nil → no-op.
		if hook := panicInTTLWatchdogHook; hook != nil {
			hook(h)
		}
		t := time.NewTimer(ttl)
		defer t.Stop()
		select {
		case <-t.C:
			// Finish FIRST so the TTL sentinel wins against the agent
			// goroutine, which would otherwise observe the ctx cancel
			// below and race to call finish with "context canceled".
			// finish is once-guarded; cancel then unwinds the
			// agent goroutine, whose later finish call is a no-op.
			h.finish(AgentResult{Error: AgentError{Sentinel: ErrAgentHandleTimeout}})
			cancel()
		case <-h.finished:
			// Normal finish path closed the signal; nothing to do. We
			// wait on `finished` rather than `done` so we never steal
			// the buffered AgentResult.
		}
	}()

	// Agent goroutine.
	go func() {
		// Release the runCtx resources on every exit path. Without this,
		// the WithCancel child + its timer (if any) leak until the parent
		// ctx is cancelled - which for long-lived embedders that reuse a
		// single parent ctx across many RunAgentAsync calls means
		// per-call accumulation. h.cancel may also be invoked by the TTL
		// watchdog or an explicit Cancel; calling cancel multiple times
		// is documented as safe (no-op on subsequent calls).
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic in agent run goroutine",
					"goroutine", "agent",
					"handle_id", h.ID,
					"session_id", h.sessionID,
					"panic", r,
				)
				h.finish(AgentResult{Error: AgentError{
					Sentinel: ErrAgentPanicked,
					Msg:      fmt.Sprintf("%v", r),
				}})
			}
		}()

		res, err := runner(o, runCtx, cfg)
		var out AgentResult
		if res != nil {
			out = *res
		}
		if err != nil {
			// Preserve the underlying error as-is (callers using
			// errors.Is on goai / ctx errors still work). Wrap into
			// AgentResult.Error.
			out.Error = err
		}
		h.finish(out)
	}()

	return h, nil
}

// ListAgentHandles returns the currently ACTIVE AgentHandle values
// registered under sessionID via RunAgentAsync. Completed handles
// (Done channel closed) are excluded - this method is the source of
// truth for the consumer's session-status query. The returned slice is
// a snapshot; it is safe for concurrent iteration and will not be
// mutated by subsequent handle lifecycle events.
// DECISION (documented in TestOrchestrator_ListAgentHandles_ExcludesCompletedHandles):
// ListAgentHandles is ACTIVE-ONLY. It does not retain completed
// handles as a history log. Pills show live work; a completed
// handle's result has already been delivered via Done and inboxed
// via the consumer's progress bridge.
// Order is unspecified and may vary between calls. Consumers that
// require a stable order must sort by AgentHandle.ID.
func (o *Orchestrator) ListAgentHandles(sessionID string) []*AgentHandle {
	if o == nil {
		return nil
	}
	o.handleMu.Lock()
	defer o.handleMu.Unlock()
	src := o.handleRegistry[sessionID]
	if len(src) == 0 {
		return nil
	}
	out := make([]*AgentHandle, len(src))
	copy(out, src)
	return out
}

// registerHandle adds h to the per-session registry. Safe under
// concurrent registration; returns once the handle is visible to
// ListAgentHandles.
func (o *Orchestrator) registerHandle(h *AgentHandle) {
	if o == nil || h == nil {
		return
	}
	o.handleMu.Lock()
	defer o.handleMu.Unlock()
	if o.handleRegistry == nil {
		o.handleRegistry = make(map[string][]*AgentHandle)
	}
	o.handleRegistry[h.sessionID] = append(o.handleRegistry[h.sessionID], h)
}

// Close releases long-lived goroutines and cached state owned by the
// Orchestrator. It is intended for call sites (e.g. per-session factory
// caches) that want to tear an orchestrator down when its owning
// session ends. Close is idempotent: subsequent calls are no-ops and
// return nil.
// Behavior:
// - New RunAgentAsync calls made after Close return
// ErrOrchestratorClosed. Existing AgentHandles are still observed
// via the normal Done channel - Close does NOT forcibly drop
// results in flight.
// - All currently-registered AgentHandles are Canceled so their
// agent goroutines observe ctx.Done, finish publishes
// ErrAgentCancelled, and the TTL watchdog goroutines exit through
// their <-h.done branch. This ends the in-memory goroutine
// population without requiring the 30-minute TTL to expire.
// - The handle registry map is emptied; the cleanup-watcher
// goroutines spawned in RunAgentAsync continue to race with
// unregisterHandle (no-op on an empty slice), then exit.
// Close always returns nil today - the error return is reserved for
// future cleanup stages (persistent resource flushes, subscriber
// unwind) so callers can add logging without breaking the signature.
func (o *Orchestrator) Close() error {
	if o == nil {
		return nil
	}
	o.closeOnce.Do(func() {
		o.closed.Store(true)

		// Snapshot and drop all handles under the lock, then Cancel
		// outside the lock - Cancel calls finish which wakes the
		// cleanup-watcher goroutine that itself takes handleMu.
		// Calling Cancel while holding handleMu would deadlock.
		o.handleMu.Lock()
		total := 0
		for _, slice := range o.handleRegistry {
			total += len(slice)
		}
		snapshot := make([]*AgentHandle, 0, total)
		for _, slice := range o.handleRegistry {
			snapshot = append(snapshot, slice...)
		}
		o.handleRegistry = nil
		o.handleMu.Unlock()

		for _, h := range snapshot {
			_ = h.Cancel()
		}

		// Bounded await: give in-flight agent goroutines up to a total
		// of closeDrainDeadline (across all handles) to drain after
		// Cancel. Cancel finishes the handle synchronously via
		// finish, which closes h.finished - but the underlying agent
		// goroutine (running the user-supplied runner) may still be
		// unwinding. We wait per-handle on h.finished with a deadline
		// so Close returns deterministically rather than racing with
		// goroutine teardown - observable lifecycle drain.
		totalDeadline := closeDrainDeadline
		deadline := time.Now().Add(totalDeadline)
		var notDrained int
		for _, h := range snapshot {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				notDrained++
				continue
			}
			timer := time.NewTimer(remaining)
			select {
			case <-h.finished:
				timer.Stop()
			case <-timer.C:
				notDrained++
			}
		}
		if notDrained > 0 {
			slog.Warn("Orchestrator.Close: partial drain - some handles did not finish within deadline",
				"notDrained", notDrained,
				"totalHandles", len(snapshot),
				"deadline", totalDeadline.String(),
			)
		}
	})
	return nil
}

// IsClosed reports whether Close has been called on this Orchestrator.
// Exposed primarily for tests and for factory caches that want to skip
// returning an already-closed orchestrator from a sync.Map.
func (o *Orchestrator) IsClosed() bool {
	if o == nil {
		return true
	}
	return o.closed.Load()
}

// unregisterHandle removes h from the per-session registry. Called
// exactly once per handle after h.Done closes. Missing handle is a
// no-op (defensive - tolerates Cancel + TTL races).
func (o *Orchestrator) unregisterHandle(h *AgentHandle) {
	if o == nil || h == nil {
		return
	}
	o.handleMu.Lock()
	defer o.handleMu.Unlock()
	slice := o.handleRegistry[h.sessionID]
	for i, cand := range slice {
		if cand == h {
			// Preserve order-free compaction - swap-remove.
			slice[i] = slice[len(slice)-1]
			slice[len(slice)-1] = nil
			slice = slice[:len(slice)-1]
			break
		}
	}
	if len(slice) == 0 {
		delete(o.handleRegistry, h.sessionID)
	} else {
		o.handleRegistry[h.sessionID] = slice
	}
}
