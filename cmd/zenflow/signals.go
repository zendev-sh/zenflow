package main

// cross-platform signal handling for the zenflow CLI.
// POSIX systems get the full set: SIGINT (Ctrl+C), SIGTERM (orchestrator
// shutdown), SIGHUP (terminal closed). Windows only delivers
// os.Interrupt (Ctrl+C) reliably to a console process - SIGTERM exists
// in the Go signal abstraction on Windows but the OS never sends it,
// and SIGHUP / SIGUSR1 / SIGUSR2 are POSIX-only and would not even
// compile under `_ = syscall.SIGHUP` on Windows.
// The split is by build tag so the Windows binary doesn't pull in
// syscall.SIGTERM / SIGHUP. installSignalHandler returns a cancel func
// the caller defers; when a signal arrives, the cancel func is invoked
// to propagate shutdown through context. The handler also writes a
// short message to stderr so the user knows the signal was received.
// Tests live in signals_test.go and exercise both POSIX and Windows
// wiring via the same `goos` indirection used in the tool package.

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
)

// signalGOOS shadows runtime.GOOS for tests. Production reads it as
// runtime.GOOS at startup. Never write outside test code.
var signalGOOS = runtime.GOOS

// platformShutdownSignalsFn is the test seam wrapping
// platformShutdownSignals. installSignalHandler reads through it so a
// test can substitute an empty slice and exercise the defensive
// "len==0" branch without faking out signal.Notify itself.
var platformShutdownSignalsFn = platformShutdownSignals

// platformShutdownSignals returns the set of OS signals the CLI listens
// to on the current platform. The slice is the input to
// signal.Notify. Kept as a func (not a const) so tests can flip
// signalGOOS and re-evaluate.
func platformShutdownSignals() []os.Signal {
	if signalGOOS == "windows" {
 // Windows: only os.Interrupt is reliably delivered. SIGTERM
 // exists as a Go constant but the OS doesn't dispatch it for
 // real (Win32 has no SIGTERM equivalent - services receive
 // SERVICE_CONTROL_STOP instead, which Go's signal package
 // does not surface).
		return []os.Signal{os.Interrupt}
	}
	// POSIX: Ctrl+C, kill(1), terminal hang-up. We deliberately omit
	// SIGUSR1/SIGUSR2 - zenflow has no use for them yet, and adding
	// them would imply a contract we'd then have to keep.
	return platformPosixSignals()
}

// installSignalHandler wires platformShutdownSignals to a goroutine
// that cancels ctx (via the returned cancel) on first signal and
// force-exits on second. Returns a cleanup func the caller must defer.
// Behaviour:
// - First signal: log to stderr, call cancel so context-aware
// code paths (provider HTTP, executor) can wind down gracefully.
// - Second signal: log "force exit" and call os.Exit(130) (POSIX
// SIGINT exit-code convention; Windows users see the same code,
// consistent with `cmd /c` behaviour).
// - Cleanup: stop signal.Notify and drain the goroutine.
// The returned context is the input ctx wrapped in a CancelFunc that
// the handler triggers. Callers should pass this context to RunFlow /
// RunGoal / RunAgent so cancellation propagates to the executor.
func installSignalHandler(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	sigs := platformShutdownSignalsFn()
	if len(sigs) == 0 {
 // Defensive: shouldn't happen, but if a future build-tag
 // branch returns nothing we still return a usable cancel
 // rather than registering an empty signal.Notify (which
 // blocks forever on Go ≥ 1.18).
		return ctx, cancel
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, sigs...)
	done := make(chan struct{})
	go func() {
		defer close(done)
 // First phase: wait for either a signal or external shutdown.
 // Cleanup signals shutdown by closing ch (after signal.Stop
 // guarantees no further sends), which makes the receive return
 // ok=false. We use this rather than racing on ctx.Done so the
 // shutdown path is deterministic regardless of select ordering.
		sig, ok := <-ch
		if !ok {
			return
		}
		fmt.Fprintf(stderr, "\nzenflow: received %s, shutting down (press again to force exit)\n", sig)
		cancel()
 // Second phase: keep listening for a second signal so the user
 // can force-exit a hung shutdown. A closed ch (cleanup) lets
 // the goroutine exit cleanly here too.
		sig, ok = <-ch
		if !ok {
			return
		}
		fmt.Fprintf(stderr, "zenflow: received %s twice, force exit\n", sig)
		exit(130)
	}()
	return ctx, func() {
 // signal.Stop guarantees no further sends to ch; close then
 // unblocks any pending receive in the goroutine. cancel runs
 // after the goroutine has exited so the returned context's
 // deadline propagates without racing the goroutine itself.
		signal.Stop(ch)
		close(ch)
		<-done
		cancel()
	}
}
