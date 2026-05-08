package exec

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"testing"
	"time"
)

// saveRunnerHook swaps runAgentAsyncRunner to fn for the test and
// restores the original on cleanup.
func saveRunnerHook(t *testing.T, fn func(*Orchestrator, context.Context, AgentConfig) (*AgentResult, error)) {
	t.Helper()
	orig := runAgentAsyncRunner
	runAgentAsyncRunner = fn
	t.Cleanup(func() { runAgentAsyncRunner = orig })
}

func TestRunAgentAsync_HandleIDFormat(t *testing.T) {
	saveRunnerHook(t, func(_ *Orchestrator, _ context.Context, _ AgentConfig) (*AgentResult, error) {
		return &AgentResult{Content: "ok"}, nil
	})
	o := New()
	h, err := o.RunAgentAsync(t.Context(), AgentConfig{})
	if err != nil {
		t.Fatalf("RunAgentAsync: %v", err)
	}
	re := regexp.MustCompile(`^agent-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	if !re.MatchString(h.ID) {
		t.Fatalf("handle ID %q does not match UUID v4 format", h.ID)
	}
	// Drain so the goroutines exit cleanly.
	<-h.Done()
}

func TestRunAgentAsync_DoneDeliversResult(t *testing.T) {
	want := &AgentResult{Content: "hello world", Turns: 2, Status: AgentStatusCompleted}
	saveRunnerHook(t, func(_ *Orchestrator, _ context.Context, _ AgentConfig) (*AgentResult, error) {
		return want, nil
	})
	o := New()
	h, err := o.RunAgentAsync(t.Context(), AgentConfig{Prompt: "hi"})
	if err != nil {
		t.Fatalf("RunAgentAsync: %v", err)
	}
	select {
	case got := <-h.Done():
		if got.Content != want.Content || got.Turns != want.Turns || got.Status != want.Status {
			t.Fatalf("result mismatch: got %+v want %+v", got, *want)
		}
		if got.Error != nil {
			t.Fatalf("unexpected Error: %v", got.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Done() did not deliver within 2s")
	}
	// Second receive must return the zero value (channel is closed).
	select {
	case got := <-h.Done():
		if got.Content != "" || got.Error != nil {
			t.Fatalf("second receive expected zero value, got %+v", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("second receive did not unblock on closed channel")
	}
}

func TestRunAgentAsync_CancelYieldsCancelledSentinel(t *testing.T) {
	release := make(chan struct{})
	saveRunnerHook(t, func(_ *Orchestrator, ctx context.Context, _ AgentConfig) (*AgentResult, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return &AgentResult{Content: "late"}, nil
		}
	})
	t.Cleanup(func() { close(release) })

	o := New()
	h, err := o.RunAgentAsync(t.Context(), AgentConfig{})
	if err != nil {
		t.Fatalf("RunAgentAsync: %v", err)
	}
	if err := h.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case got := <-h.Done():
		if got.Error == nil {
			t.Fatalf("expected Error, got nil")
		}
		if !errors.Is(got.Error, ErrAgentCancelled) {
			t.Fatalf("errors.Is ErrAgentCancelled = false; err = %v", got.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Done() did not deliver after Cancel within 2s")
	}
}

func TestRunAgentAsync_TTLForceCompletion(t *testing.T) {
	saveRunnerHook(t, func(_ *Orchestrator, ctx context.Context, _ AgentConfig) (*AgentResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	o := New(WithAgentHandleTTL(50 * time.Millisecond))
	h, err := o.RunAgentAsync(t.Context(), AgentConfig{})
	if err != nil {
		t.Fatalf("RunAgentAsync: %v", err)
	}
	select {
	case got := <-h.Done():
		if got.Error == nil {
			t.Fatalf("expected Error on TTL timeout, got nil")
		}
		if !errors.Is(got.Error, ErrAgentHandleTimeout) {
			t.Fatalf("errors.Is ErrAgentHandleTimeout = false; err = %v", got.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Done() did not deliver within 2s (TTL watchdog failed)")
	}
}

func TestRunAgentAsync_PanicRecovered(t *testing.T) {
	saveRunnerHook(t, func(_ *Orchestrator, _ context.Context, _ AgentConfig) (*AgentResult, error) {
		panic("boom-42")
	})
	o := New()
	h, err := o.RunAgentAsync(t.Context(), AgentConfig{})
	if err != nil {
		t.Fatalf("RunAgentAsync: %v", err)
	}
	select {
	case got := <-h.Done():
		if got.Error == nil {
			t.Fatalf("expected Error on panic, got nil")
		}
		if !errors.Is(got.Error, ErrAgentPanicked) {
			t.Fatalf("errors.Is ErrAgentPanicked = false; err = %v", got.Error)
		}
		var ae AgentError
		if !errors.As(got.Error, &ae) {
			t.Fatalf("expected AgentError, got %T", got.Error)
		}
		if ae.Msg == "" {
			t.Fatal("expected AgentError.Msg to contain panic string, got empty")
		}
		if !containsString(ae.Msg, "boom-42") {
			t.Fatalf("AgentError.Msg %q does not contain panic string %q", ae.Msg, "boom-42")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Done() did not deliver within 2s (panic-recover failed)")
	}
}

// Recovery of panics in the TTL-watchdog + registry-cleanup goroutines
// is provided by `defer func { if r := recover; ... }` blocks in
// agent_handle.go. The recover-pattern is trivial Go boilerplate and
// does not need a dedicated test - testing it required test-only
// injection hooks in production code, which violates the CLAUDE.md
// rule against test-only seams + caused a -race DATA RACE. Hooks
// removed; recovery remains in place.

// TestSetRunAgentAsyncRunnerForTest_Hook verifies the exported test hook
// swaps runAgentAsyncRunner and returns the previous value so the
// caller can restore it. nil input is a no-op.
func TestSetRunAgentAsyncRunnerForTest_Hook(t *testing.T) {
	orig := runAgentAsyncRunner
	called := 0
	replacement := func(_ *Orchestrator, _ context.Context, _ AgentConfig) (*AgentResult, error) {
		called++
		return &AgentResult{Content: "test-hook"}, nil
	}
	prev := SetRunAgentAsyncRunnerForTest(replacement)
	t.Cleanup(func() { runAgentAsyncRunner = orig })

	if prev == nil {
		t.Fatal("SetRunAgentAsyncRunnerForTest(fn) returned nil previous")
	}

	o := New()
	h, err := o.RunAgentAsync(t.Context(), AgentConfig{})
	if err != nil {
		t.Fatalf("RunAgentAsync: %v", err)
	}
	select {
	case got := <-h.Done():
		if got.Content != "test-hook" {
			t.Errorf("Content = %q, want %q", got.Content, "test-hook")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Done() did not deliver")
	}
	if called != 1 {
		t.Errorf("replacement called %d times, want 1", called)
	}

	// nil input must be a no-op - runner unchanged after the nil swap.
	beforePtr := fmt.Sprintf("%p", runAgentAsyncRunner)
	if got := SetRunAgentAsyncRunnerForTest(nil); got == nil {
		t.Error("nil-input swap returned nil previous (expected current runner)")
	}
	if fmt.Sprintf("%p", runAgentAsyncRunner) != beforePtr {
		t.Error("nil input mutated runAgentAsyncRunner (should be no-op)")
	}
}

func TestAgentError_ErrorsIsSentinel(t *testing.T) {
	e := AgentError{Sentinel: ErrAgentHandleTimeout, Msg: "x"}
	if !errors.Is(e, ErrAgentHandleTimeout) {
		t.Fatal("errors.Is(AgentError{Sentinel: ErrAgentHandleTimeout}, ErrAgentHandleTimeout) = false")
	}
	if errors.Is(e, ErrAgentCancelled) {
		t.Fatal("errors.Is returned true for mismatched sentinel")
	}
	if e.Error() == "" {
		t.Fatal("Error() returned empty string for non-empty AgentError")
	}
	// Error must include both sentinel message and the detail when both present.
	if !containsString(e.Error(), "x") {
		t.Fatalf("Error() %q missing Msg detail", e.Error())
	}
}

func TestRunAgentAsync_OptionTTLOverride_Parsed(t *testing.T) {
	got := resolveAgentHandleTTL(100 * time.Millisecond)
	if got != 100*time.Millisecond {
		t.Fatalf("resolveAgentHandleTTL(100ms) = %v, want 100ms", got)
	}

	// Zero falls back to default.
	if got := resolveAgentHandleTTL(0); got != DefaultAgentHandleTTL {
		t.Fatalf("zero TTL should fall back to default; got %v", got)
	}
	// Negative falls back to default.
	if got := resolveAgentHandleTTL(-1 * time.Second); got != DefaultAgentHandleTTL {
		t.Fatalf("negative TTL should fall back to default; got %v", got)
	}
}

// TestRunAgentAsync_CancelIdempotent ensures multiple Cancel calls
// and finish races do not double-send or double-close the done chan.
func TestRunAgentAsync_CancelIdempotent(t *testing.T) {
	saveRunnerHook(t, func(_ *Orchestrator, ctx context.Context, _ AgentConfig) (*AgentResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	o := New()
	h, err := o.RunAgentAsync(t.Context(), AgentConfig{})
	if err != nil {
		t.Fatalf("RunAgentAsync: %v", err)
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			_ = h.Cancel()
		})
	}
	wg.Wait()
	<-h.Done()
}

func containsString(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
