package exec

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestOrchestrator_Close_CancelsGoroutines asserts that Close on an
// Orchestrator with an in-flight RunAgentAsync handle unblocks the
// agent goroutine (via ctx cancel), unwinds the TTL watchdog, drains
// the handle registry, and rejects future RunAgentAsync calls. F5
// fix: was previously leaking goroutines on session end.
// NOTE: we do NOT assert the specific error value observed on
// Done because AgentHandle.done is size-1 buffered + closed-after-
// single-send, which races between the internal registry-cleanup
// watcher and the user's <-Done receive. The user may observe
// either the Cancel sentinel OR the post-close zero value depending
// on scheduling. Fixing that race is out of scope for F5 (see
// agent_handle.go finish). What matters here is that the
// orchestrator's goroutine population winds down deterministically.
func TestOrchestrator_Close_CancelsGoroutines(t *testing.T) {
	release := make(chan struct{})
	agentDone := make(chan struct{})
	saveRunnerHook(t, func(_ *Orchestrator, ctx context.Context, _ AgentConfig) (*AgentResult, error) {
		defer close(agentDone)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return &AgentResult{Content: "late"}, nil
		}
	})
	t.Cleanup(func() {
 // Ensure the runner closure exits even if the test returns
 // before it did.
		select {
		case <-release:
		default:
			close(release)
		}
	})

	o := New()
	_, err := o.RunAgentAsync(t.Context(), AgentConfig{SessionID: "s1"})
	if err != nil {
		t.Fatalf("RunAgentAsync: %v", err)
	}

	if err := o.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Agent goroutine must observe ctx cancellation and exit. Prior
	// to Close, it was parked on ctx.Done inside the runner hook.
	select {
	case <-agentDone:
	case <-time.After(2 * time.Second):
		t.Fatal("agent goroutine did not exit within 2s after Close - ctx cancel did not propagate")
	}

	// Registry must be drained.
	if got := o.ListAgentHandles("s1"); len(got) != 0 {
		t.Fatalf("ListAgentHandles after Close = %d handles; want 0", len(got))
	}

	// IsClosed flip must be visible to callers.
	if !o.IsClosed() {
		t.Fatalf("IsClosed after Close = false; want true")
	}

	// New RunAgentAsync calls must be rejected.
	if _, err := o.RunAgentAsync(t.Context(), AgentConfig{}); !errors.Is(err, ErrOrchestratorClosed) {
		t.Fatalf("RunAgentAsync after Close: want ErrOrchestratorClosed, got %v", err)
	}
}

// TestOrchestrator_Close_Idempotent asserts Close can be called
// multiple times and from multiple goroutines without panicking or
// double-Cancelling handles.
func TestOrchestrator_Close_Idempotent(t *testing.T) {
	o := New()
	// First call on an orchestrator with no handles must succeed.
	if err := o.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}
	if !o.IsClosed() {
		t.Fatalf("IsClosed after Close = false; want true")
	}
	// Second call is a no-op.
	if err := o.Close(); err != nil {
		t.Fatalf("Close #2: %v", err)
	}

	// Concurrent Close calls on a fresh orchestrator must all succeed.
	o2 := New()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := o2.Close(); err != nil {
				t.Errorf("concurrent Close: %v", err)
			}
		})
	}
	wg.Wait()
}

// TestFactoryCache_SameSessionIDReturnsSameOrchestrator asserts that
// calling For(sessionID) twice for the same sessionID returns the
// exact same *Orchestrator pointer - the core invariant that proves
// we are not leaking a fresh orchestrator per Prompt call for the
// same session.
func TestFactoryCache_SameSessionIDReturnsSameOrchestrator(t *testing.T) {
	var constructed atomic.Int32
	inner := func(sessionID string) *Orchestrator {
		constructed.Add(1)
		return New()
	}
	cache, err := NewFactoryCache(inner)
	if err != nil {
		t.Fatalf("NewFactoryCache: %v", err)
	}

	first := cache.For("sess-A")
	if first == nil {
		t.Fatal("cache.For #1 returned nil")
	}
	second := cache.For("sess-A")
	if second == nil {
		t.Fatal("cache.For #2 returned nil")
	}
	if first != second {
		t.Fatalf("pointer mismatch: first=%p second=%p", first, second)
	}
	if got := constructed.Load(); got != 1 {
		t.Fatalf("inner called %d times; want 1", got)
	}

	// Different sessionID -> different orchestrator.
	other := cache.For("sess-B")
	if other == first {
		t.Fatalf("cache.For('sess-B') returned same orchestrator as 'sess-A'")
	}
	if got := constructed.Load(); got != 2 {
		t.Fatalf("inner called %d times; want 2", got)
	}
}

// TestFactoryCache_ClosesOrchestratorOnClose asserts that
// FactoryCache.Close(sessionID) invokes Orchestrator.Close and
// removes the entry so the next For call re-constructs. This is
// the wire point the F5 fix relies on for session-deleted cleanup.
func TestFactoryCache_ClosesOrchestratorOnClose(t *testing.T) {
	var constructed atomic.Int32
	inner := func(sessionID string) *Orchestrator {
		constructed.Add(1)
		return New()
	}
	cache, err := NewFactoryCache(inner)
	if err != nil {
		t.Fatalf("NewFactoryCache: %v", err)
	}

	orch := cache.For("sess-D")
	if orch == nil {
		t.Fatal("cache.For returned nil")
	}
	if orch.IsClosed() {
		t.Fatalf("fresh orchestrator IsClosed = true; want false")
	}

	if err := cache.Close("sess-D"); err != nil {
		t.Fatalf("cache.Close: %v", err)
	}
	if !orch.IsClosed() {
		t.Fatalf("orchestrator IsClosed after cache.Close = false; want true")
	}

	// Next For call must re-construct, not return the closed one.
	orch2 := cache.For("sess-D")
	if orch2 == nil {
		t.Fatal("cache.For after Close returned nil")
	}
	if orch2 == orch {
		t.Fatalf("cache.For after Close returned the closed orchestrator")
	}
	if got := constructed.Load(); got != 2 {
		t.Fatalf("inner called %d times; want 2", got)
	}
}

// TestFactoryCache_NilResultNotCached asserts a nil result from the
// inner factory (e.g. model resolution failed) is NOT cached - the
// next call must retry construction.
func TestFactoryCache_NilResultNotCached(t *testing.T) {
	var constructed atomic.Int32
	var shouldReturnNil atomic.Bool
	shouldReturnNil.Store(true)
	inner := func(sessionID string) *Orchestrator {
		constructed.Add(1)
		if shouldReturnNil.Load() {
			return nil
		}
		return New()
	}
	cache, err := NewFactoryCache(inner)
	if err != nil {
		t.Fatalf("NewFactoryCache: %v", err)
	}

	if got := cache.For("sess-nil"); got != nil {
		t.Fatalf("cache.For #1 = %p; want nil", got)
	}

	shouldReturnNil.Store(false)
	if got := cache.For("sess-nil"); got == nil {
		t.Fatal("cache.For #2 returned nil; want a real orchestrator after transient failure")
	}
	if got := constructed.Load(); got != 2 {
		t.Fatalf("inner called %d times; want 2 (nil must not poison cache)", got)
	}
}

// TestFactoryCache_CloseAll asserts CloseAll drops every entry and
// Closees each orchestrator - the process-shutdown wire point.
func TestFactoryCache_CloseAll(t *testing.T) {
	inner := func(sessionID string) *Orchestrator { return New() }
	cache, err := NewFactoryCache(inner)
	if err != nil {
		t.Fatalf("NewFactoryCache: %v", err)
	}

	a := cache.For("sess-1")
	b := cache.For("sess-2")
	c := cache.For("sess-3")

	cache.CloseAll()

	for name, o := range map[string]*Orchestrator{"a": a, "b": b, "c": c} {
		if !o.IsClosed() {
			t.Errorf("%s: IsClosed after CloseAll = false; want true", name)
		}
	}

	// After CloseAll, For must re-create.
	a2 := cache.For("sess-1")
	if a2 == a {
		t.Fatal("cache.For after CloseAll returned the closed orchestrator")
	}
}

// TestFactoryCache_EmptySessionIDBypassesCache asserts an empty
// sessionID is NOT cached - each call invokes inner fresh. Matches
// the pre-cache behavior of both production factories.
func TestFactoryCache_EmptySessionIDBypassesCache(t *testing.T) {
	var constructed atomic.Int32
	inner := func(sessionID string) *Orchestrator {
		constructed.Add(1)
		return New()
	}
	cache, err := NewFactoryCache(inner)
	if err != nil {
		t.Fatalf("NewFactoryCache: %v", err)
	}

	_ = cache.For("")
	_ = cache.For("")
	if got := constructed.Load(); got != 2 {
		t.Fatalf("inner called %d times; want 2 (empty sessionID bypasses cache)", got)
	}
}

// TestFactoryCache_InnerTimeoutReturnsNil - asserts that when
// the inner constructor blocks past factoryCacheBuildTimeout, For
// returns nil instead of pinning the caller forever. The inner
// goroutine continues running (cannot be cancelled) but its result
// is no longer cached.
func TestFactoryCache_InnerTimeoutReturnsNil(t *testing.T) {
	prev := factoryCacheBuildTimeout
	factoryCacheBuildTimeout = 25 * time.Millisecond
	t.Cleanup(func() { factoryCacheBuildTimeout = prev })

	innerStart := make(chan struct{})
	innerRelease := make(chan struct{})
	t.Cleanup(func() { close(innerRelease) })

	inner := func(sessionID string) *Orchestrator {
		close(innerStart)
		<-innerRelease
		return New()
	}
	cache, err := NewFactoryCache(inner)
	if err != nil {
		t.Fatalf("NewFactoryCache: %v", err)
	}

	start := time.Now()
	got := cache.For("session-blocked")
	elapsed := time.Since(start)

	if got != nil {
		t.Fatalf("For() returned %p; want nil on timeout", got)
	}
	if elapsed >= 5*factoryCacheBuildTimeout {
		t.Fatalf("For() blocked %v; want ~%v", elapsed, factoryCacheBuildTimeout)
	}
	// Confirm inner actually started (so we exercised the timeout
	// path, not some short-circuit).
	select {
	case <-innerStart:
	default:
		t.Fatal("inner never started; timeout test did not exercise the bounded-wait path")
	}
}
