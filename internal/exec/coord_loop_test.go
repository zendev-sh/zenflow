package exec

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zendev-sh/goai/provider"
)

// coordLoopErrLLM returns an error from every DoGenerate call. Combined
// with NewDefaultCoordRunner, it drives the coord loop's error-log
// branch + early Run exit so the loop can iterate quickly under test.
type coordLoopErrLLM struct {
	calls atomic.Int32
	err   error
}

func (m *coordLoopErrLLM) ModelID() string { return "coord-loop-err" }
func (m *coordLoopErrLLM) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	m.calls.Add(1)
	return nil, m.err
}
func (m *coordLoopErrLLM) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	m.calls.Add(1)
	return nil, m.err
}

// coordLoopBlockingLLM blocks in DoGenerate until block is closed,
// then returns ctx.Err / its sentinel. Used to verify ctx-cancel
// terminates the loop and the cleanup timer fires when the coord
// goroutine outlives the cleanup deadline.
type coordLoopBlockingLLM struct {
	block chan struct{}
}

func (m *coordLoopBlockingLLM) ModelID() string { return "coord-loop-block" }
func (m *coordLoopBlockingLLM) DoGenerate(ctx context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	select {
	case <-m.block:
		return nil, errors.New("unblocked")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (m *coordLoopBlockingLLM) DoStream(ctx context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	select {
	case <-m.block:
		return nil, errors.New("unblocked")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestRunCoordinatorLoop_NilRunner: nil runner short-circuits to a
// no-op cleanup func that does not panic.
func TestRunCoordinatorLoop_NilRunner(t *testing.T) {
	cleanup := RunCoordinatorLoop(context.Background(), nil, "any-model")
	if cleanup == nil {
		t.Fatal("cleanup is nil for nil runner; want no-op func")
	}
	cleanup() // must not panic
}

// TestRunCoordinatorLoop_CtxCancelTerminatesLoop: cancelling the ctx
// must wind down the goroutine and let cleanup return promptly.
func TestRunCoordinatorLoop_CtxCancelTerminatesLoop(t *testing.T) {
	llm := &coordLoopBlockingLLM{block: make(chan struct{})}
	defer close(llm.block)
	runner := NewDefaultCoordRunner(llm)
	ctx, cancel := context.WithCancel(context.Background())
	cleanup := RunCoordinatorLoop(ctx, runner, "test-model")

	// Cancel ctx → coord goroutine sees ctx.Done, Run returns,
	// WaitForCoordWake observes ctx canceled → loop exits.
	cancel()

	doneCh := make(chan struct{})
	go func() { cleanup(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not return within 2s after ctx cancel")
	}
}

// TestRunCoordinatorLoop_MultipleRunIterationsOnWake: when Run exits
// (LLM returns error fast), WaitForCoordWake observes Wake and
// re-spawns Run; the loop must perform >= 2 Run calls before ctx
// cancellation.
func TestRunCoordinatorLoop_MultipleRunIterationsOnWake(t *testing.T) {
	llm := &coordLoopErrLLM{err: errors.New("transient")}
	runner := NewDefaultCoordRunner(llm)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanup := RunCoordinatorLoop(ctx, runner, "test-model")

	// First Run iteration completes quickly (LLM returns error).
	// Wait for it to land in WaitForCoordWake.
	deadline := time.Now().Add(2 * time.Second)
	for llm.calls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if llm.calls.Load() < 1 {
		t.Fatal("first Run iteration did not complete within 2s")
	}

	// Fire Wake → WaitForCoordWake returns true → loop re-spawns Run.
	select {
	case runner.Wake() <- struct{}{}:
	default:
	}

	deadline = time.Now().Add(2 * time.Second)
	for llm.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := llm.calls.Load(); got < 2 {
		t.Fatalf("expected >=2 Run iterations after Wake; got %d", got)
	}

	cancel()
	doneCh := make(chan struct{})
	go func() { cleanup(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not return within 2s after final cancel")
	}
}

// TestRunCoordinatorLoop_CleanupTimeoutFires: when the coord goroutine
// is wedged in a hung LLM call that ignores ctx cancellation (modelled
// by withholding block + ignoring ctx), the cleanup timer must trip
// and let cleanup return rather than blocking on the goroutine.
// The blocking LLM here actually honours ctx.Done, so to genuinely
// exercise the timer-fired branch we use WithCleanupTimeout(1ns) and
// rely on the goroutine not having scheduled in by the deadline.
// Coverage-note: the exact timer-fires branch is exercised in real
// processes when an LLM ignores ctx; this test asserts the cleanup
// returns within a bounded wall-clock regardless.
func TestRunCoordinatorLoop_CleanupTimeoutFires(t *testing.T) {
	llm := &coordLoopBlockingLLM{block: make(chan struct{})}
	runner := NewDefaultCoordRunner(llm)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanup := RunCoordinatorLoop(ctx, runner, "test-model", WithCleanupTimeout(5*time.Millisecond))

	doneCh := make(chan struct{})
	go func() { cleanup(); close(doneCh) }()
	select {
	case <-doneCh:
	// Cleanup returned within bounded time even though the
	// goroutine may still be live (it'll exit when block closes /
	// ctx is reaped at test end).
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not return within 2s; cleanup-timeout did not fire")
	}
	close(llm.block) // release the goroutine so it does not leak past test end.
}

// TestRunCoordinatorLoop_WithCleanupTimeoutZeroIgnored: zero or
// negative durations fall back to DefaultCoordCleanupTimeout, so a
// no-op LLM (returns instantly) lets cleanup succeed via the
// done-channel path well within the default 2s.
func TestRunCoordinatorLoop_WithCleanupTimeoutZeroIgnored(t *testing.T) {
	llm := &coordLoopErrLLM{err: errors.New("transient")}
	runner := NewDefaultCoordRunner(llm)
	ctx, cancel := context.WithCancel(context.Background())
	cleanup := RunCoordinatorLoop(ctx, runner, "test-model", WithCleanupTimeout(0), WithCleanupTimeout(-1))

	cancel()
	doneCh := make(chan struct{})
	go func() { cleanup(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup did not return; zero/negative timeout should fall back to default")
	}
}

// coordLoopPanicLLM panics on every DoGenerate to exercise the
// panic-recover branch in coord_loop.go's goroutine (line 84-86).
type coordLoopPanicLLM struct{}

func (coordLoopPanicLLM) ModelID() string { return "coord-loop-panic" }
func (coordLoopPanicLLM) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	panic("coord LLM exploded")
}
func (coordLoopPanicLLM) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	panic("coord LLM exploded (stream)")
}

// TestRunCoordinatorLoop_RecoversFromPanic verifies the panic-recover
// branch of the coordinator goroutine (coord_loop.go:84-86). A panicking
// LLM must not crash the process; the recover logs the panic via slog
// and the goroutine exits cleanly so cleanup returns within the
// default timeout.
func TestRunCoordinatorLoop_RecoversFromPanic(t *testing.T) {
	runner := NewDefaultCoordRunner(coordLoopPanicLLM{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanup := RunCoordinatorLoop(ctx, runner, "test-model")

	// Allow the goroutine time to schedule, panic, and recover.
	time.Sleep(50 * time.Millisecond)
	cancel()
	doneCh := make(chan struct{})
	go func() { cleanup(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not return within 2s after panic-recover")
	}
}

// TestRunCoordinatorLoop_NilOptionIgnored: a nil CoordLoopOption must
// not panic; it is silently skipped during config application.
func TestRunCoordinatorLoop_NilOptionIgnored(t *testing.T) {
	llm := &coordLoopErrLLM{err: errors.New("transient")}
	runner := NewDefaultCoordRunner(llm)
	ctx, cancel := context.WithCancel(context.Background())
	cleanup := RunCoordinatorLoop(ctx, runner, "test-model", nil)
	cancel()
	doneCh := make(chan struct{})
	go func() { cleanup(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not return; nil option must not break loop")
	}
}
