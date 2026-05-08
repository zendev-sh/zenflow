package exec

import (
	"errors"
	"log/slog"
	"sync"
	"time"
)

// ErrNilFactoryInner signals that NewFactoryCache was called with a nil
// inner factory. Returned instead of panicking so callers can surface
// the misuse via the error path.
var ErrNilFactoryInner = errors.New("zenflow: factory cache inner factory must be non-nil")

// factoryCacheBuildTimeout bounds how long FactoryCache.For will wait
// for the inner constructor to return before logging a warning and
// returning nil. The inner func may block on synchronous I/O (model
// resolve, DB lookup) - without a bound, a hung dependency would pin
// every FactoryCache.For caller for that session..
// 30s gives slow-but-legitimate model-registry lookups (network round
// trips to Bedrock, Vertex ADC refresh) plenty of headroom while
// guarding against a never-returning dependency.
// Exposed as a var (not const) so tests can shorten the timeout
// without altering production code paths.
var factoryCacheBuildTimeout = 30 * time.Second

// FactoryCache memoizes a zenflow.Orchestrator per sessionID so repeat
// calls for the same session return the same instance instead of
// leaking fresh goroutines (handle registry, TTL watchdogs) on every
// Prompt invocation. The cache is the production plumbing behind
// SDK consumers' per-session orchestrator factory: consumers wrap
// their raw orchestrator constructors with a FactoryCache so the same
// session always sees the same Orchestrator.
// Concurrency:
// - For(sessionID) is safe for concurrent callers. Concurrent first
// calls for the same session may invoke the inner constructor
// more than once; the loser of the construct-then-store race is
// Closed and discarded so only one orchestrator per sessionID
// is returned to callers across the process.
// - Close(sessionID) and CloseAll are idempotent; an already-closed
// orchestrator is removed from the cache and Closed again as a
// no-op.
// The zero value is NOT ready to use - use NewFactoryCache.
type FactoryCache struct {
	inner func(sessionID string) *Orchestrator

	mu    sync.Mutex
	items map[string]*Orchestrator
}

// NewFactoryCache wraps inner so repeat calls for the same sessionID
// return the same Orchestrator. inner is invoked at most once per
// sessionID under normal conditions; concurrent first-callers may
// race, but only one winner is retained and the losers are Closed
// before being discarded.
// inner may return nil (e.g. when model resolution fails). A nil
// result is NOT cached - the next For call with the same sessionID
// retries construction. This matches the pre-cache "graceful
// degradation" behavior of both zenflowFactory closures.
// Returns ErrNilFactoryInner when inner is nil so callers see the
// misuse via the error path rather than a runtime panic.
func NewFactoryCache(inner func(sessionID string) *Orchestrator) (*FactoryCache, error) {
	if inner == nil {
		return nil, ErrNilFactoryInner
	}
	return &FactoryCache{
		inner: inner,
		items: make(map[string]*Orchestrator),
	}, nil
}

// For returns the cached orchestrator for sessionID, or constructs a
// new one via the inner factory and caches it. Nil results from inner
// are NOT cached so transient failures (e.g. model resolution hiccups)
// do not permanently poison the cache.
// An empty sessionID bypasses the cache entirely - each call invokes
// inner and the result is returned uncached. This preserves the
// pre-cache behavior for callers that deliberately pass "" to signal
// single-session deployments.
func (c *FactoryCache) For(sessionID string) *Orchestrator {
	if c == nil || c.inner == nil {
		return nil
	}
	if sessionID == "" {
		return c.inner(sessionID)
	}

	c.mu.Lock()
	if existing, ok := c.items[sessionID]; ok {
		c.mu.Unlock()
		if existing != nil && !existing.IsClosed() {
			return existing
		}
 // Cached entry was closed externally (e.g. via session-deleted
 // wiring) - drop it and re-create below.
		c.mu.Lock()
 // Re-check under the lock to avoid clobbering a concurrent
 // fresh entry.
		if cur, ok2 := c.items[sessionID]; ok2 && cur == existing {
			delete(c.items, sessionID)
		}
	}
	c.mu.Unlock()

	// Construct outside the lock - inner may be slow (model resolve,
	// DB lookup) and we do not want to serialize unrelated sessions.
	// bound inner with a watchdog. The inner signature does
	// not carry a ctx (would force every caller to plumb one through),
	// so we run it in a goroutine and select against a timeout. If
	// inner hangs forever the goroutine leaks - unavoidable without
	// inner cancellation support - but the FactoryCache.For caller
	// returns nil and the upstream code path can fall back gracefully
	// instead of pinning the session forever.
	orch := c.callInnerWithTimeout(sessionID)
	if orch == nil {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.items[sessionID]; ok && existing != nil && !existing.IsClosed() {
 // Lost the race - discard the freshly-built orchestrator so
 // only the winning instance is visible to callers. Close the
 // loser so its goroutines do not linger.
		_ = orch.Close()
		slog.Debug("FactoryCache: race-loser orchestrator closed", "session_id", sessionID)
		return existing
	}
	c.items[sessionID] = orch
	return orch
}

// callInnerWithTimeout runs c.inner(sessionID) in a goroutine and
// returns the result, or nil if factoryCacheBuildTimeout elapses
// first. On timeout the goroutine continues running so its eventual
// result is not lost - but it is no longer attributable to the
// caller. The caller treats nil as "construction failed; do not
// cache" and the next For call will re-trigger inner.
// Cap is 1: the first caller takes the result; if a previous
// attempt's goroutine arrives later, the non-blocking send below
// drops it and the goroutine exits cleanly. Cap-2 was unnecessary
// because the select/default already prevents the sender from
// blocking - cap-1 is sufficient and removes the latent bug where
// 3+ consecutive timeouts for the same sessionID could leave a
// goroutine blocked on a full cap-2 channel with no reader.
// (Deeper fix - making inner context-aware so timed-out goroutines
// can be cancelled - is tracked separately as H3.)
func (c *FactoryCache) callInnerWithTimeout(sessionID string) *Orchestrator {
	resCh := make(chan *Orchestrator, 1)
	go func() {
 // Best-effort send: if the caller already timed out and moved
 // on, the channel slot is taken or no reader is waiting - drop
 // silently and let the goroutine exit. The dropped result is
 // the caller's responsibility to recover via the next For
 // call.
		select {
		case resCh <- c.inner(sessionID):
		default:
		}
	}()
	timer := time.NewTimer(factoryCacheBuildTimeout)
	defer timer.Stop()
	select {
	case orch := <-resCh:
		return orch
	case <-timer.C:
		slog.Warn("FactoryCache: inner constructor exceeded timeout",
			"session_id", sessionID,
			"timeout", factoryCacheBuildTimeout)
		return nil
	}
}

// Peek returns the cached orchestrator for sessionID WITHOUT
// constructing one when absent. Returns nil when no entry exists or
// the cached entry has been Closed externally. Used by callers (e.g.
// cachedZenflowFactory) that need to inspect cached orchestrator
// state - most importantly its DefaultModel - to decide whether the
// cache entry is stale and should be invalidated before the next
// For call.
func (c *FactoryCache) Peek(sessionID string) *Orchestrator {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	orch, ok := c.items[sessionID]
	if !ok || orch == nil || orch.IsClosed() {
		return nil
	}
	return orch
}

// Close drops the cache entry for sessionID and invokes Close on the
// orchestrator. Returns nil if the session was never cached. Safe to
// call concurrently with For - For will re-create an orchestrator on
// the next call.
func (c *FactoryCache) Close(sessionID string) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	orch, ok := c.items[sessionID]
	if ok {
		delete(c.items, sessionID)
	}
	c.mu.Unlock()
	if !ok || orch == nil {
		return nil
	}
	// Close observability: bracket the per-session fan-out cancel so
	// operators can see entry/exit when the orchestrator's downstream
	// goroutines (registry-cleanup, TTL watchdog, agent loops) take
	// longer than expected to drain.
	slog.Debug("FactoryCache.Close: orchestrator close start", "session_id", sessionID)
	err := orch.Close()
	slog.Debug("FactoryCache.Close: orchestrator close complete", "session_id", sessionID, "err", err)
	return err
}

// CloseAll drops every cached entry and Closees each orchestrator.
// Intended for process-level shutdown paths (tests, graceful server
// teardown). Individual Close errors are discarded - use CloseAllErr
// if callers need to observe them.
func (c *FactoryCache) CloseAll() {
	_ = c.CloseAllErr()
}

// CloseAllErr is like CloseAll but returns a joined error containing
// all Close failures (nil if all succeed). Suitable for callers
// that need to log or propagate shutdown errors.
func (c *FactoryCache) CloseAllErr() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	snapshot := c.items
	c.items = make(map[string]*Orchestrator)
	c.mu.Unlock()

	// Bracket fan-out cancel so operators can see "started closing N
	// orchestrators" / "finished" in shutdown logs.
	slog.Debug("FactoryCache.CloseAll: fan-out start", "sessions", len(snapshot))
	// The bracket start/complete pair gives operators the count and the
	// timing; one log per closed orchestrator would flood stdout when
	// Debug is enabled in production with hundreds of live sessions.
	closeErrs := make([]error, 0, len(snapshot))
	for _, orch := range snapshot {
		if orch != nil {
			closeErrs = append(closeErrs, orch.Close())
		}
	}
	slog.Debug("FactoryCache.CloseAll: fan-out complete", "sessions", len(snapshot))
	return errors.Join(closeErrs...)
}
