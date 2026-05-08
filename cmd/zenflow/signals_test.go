package main

// signals_test.go -  evidence.
// Cross-platform tests for the CLI signal handler. Uses signalGOOS
// indirection so both POSIX and Windows wiring can be exercised on
// macOS. The subset of behaviour that needs real OS signal delivery
// (e.g. asserting SIGTERM cancels ctx) is exercised by the POSIX path
// only; the Windows-only os.Interrupt-only assertion is a static check
// of the returned signal slice.

import (
	"context"
	"errors"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func withSignalGOOS(t *testing.T, fake string) {
	t.Helper()
	orig := signalGOOS
	signalGOOS = fake
	t.Cleanup(func() { signalGOOS = orig })
}

// TestSignalHandler_PlatformAlternatives asserts platformShutdownSignals
// returns the right set per OS.
// - POSIX: at least os.Interrupt + SIGTERM + SIGHUP (this is the
// full set today; if zenflow grows new signals, update this list).
// - Windows: exactly os.Interrupt - see signals.go for why SIGTERM
// is unreliable on Win32.
func TestSignalHandler_PlatformAlternatives(t *testing.T) {
	t.Run("posix", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX-only branch - requires syscall.SIGTERM/SIGHUP defined")
		}
		withSignalGOOS(t, "linux")
		got := platformShutdownSignals()
		want := map[os.Signal]bool{
			os.Interrupt:    false,
			syscall.SIGTERM: false,
			syscall.SIGHUP:  false,
		}
		for _, s := range got {
			if _, ok := want[s]; ok {
				want[s] = true
			} else {
				t.Errorf("unexpected signal: %v", s)
			}
		}
		for s, seen := range want {
			if !seen {
				t.Errorf("missing signal: %v", s)
			}
		}
	})

	t.Run("windows", func(t *testing.T) {
		withSignalGOOS(t, "windows")
		got := platformShutdownSignals()
		if len(got) != 1 {
			t.Fatalf("len(got)=%d, want 1 (os.Interrupt only)", len(got))
		}
		if got[0] != os.Interrupt {
			t.Errorf("got[0]=%v, want os.Interrupt", got[0])
		}
	})

	t.Run("default_unknown_falls_back_to_posix_helper", func(t *testing.T) {
 // Any goos that isn't "windows" routes to platformPosixSignals.
 // We don't assert the exact length (build-tag-dependent) but
 // confirm it returns at least one signal so installSignalHandler
 // won't degrade to a no-op.
		withSignalGOOS(t, "plan9") // arbitrary non-windows
		got := platformShutdownSignals()
		if len(got) == 0 {
			t.Fatal("non-windows should return at least one signal")
		}
	})
}

// TestInstallSignalHandler_CancelOnSignal asserts that delivering the
// real os.Interrupt to the process cancels the returned context. POSIX
// only - Windows requires CTRL_BREAK / CTRL_C events that need a console
// session, which isn't practical from `go test`.
func TestInstallSignalHandler_CancelOnSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires console signal delivery; covered by Windows CI integration suite")
	}
	// Swap stderr and exit so the test doesn't print to the real stderr
	// or kill the test process.
	origStderr := stderr
	origExit := exit
	stderr = &discard{}
	exit = func(int) {}
	t.Cleanup(func() {
		stderr = origStderr
		exit = origExit
	})

	ctx, stop := installSignalHandler(context.Background())
	defer stop()

	proc, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if err := proc.Signal(os.Interrupt); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	select {
	case <-ctx.Done():
 // expected
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Errorf("ctx.Err = %v, want Canceled", ctx.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ctx not cancelled within 2s of os.Interrupt")
	}
}

// TestInstallSignalHandler_DoubleSignalForceExits asserts the second
// signal triggers the exit(130) branch. POSIX only for the same console
// reason as above.
func TestInstallSignalHandler_DoubleSignalForceExits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires console signal delivery")
	}
	origStderr := stderr
	origExit := exit
	stderr = &discard{}
	exitCode := make(chan int, 1)
	exit = func(c int) { exitCode <- c }
	t.Cleanup(func() {
		stderr = origStderr
		exit = origExit
	})

	ctx, stop := installSignalHandler(context.Background())
	defer stop()

	proc, _ := os.FindProcess(os.Getpid())
	_ = proc.Signal(os.Interrupt)
	// Wait for first signal to register before sending the second.
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("first signal didn't cancel ctx")
	}
	_ = proc.Signal(os.Interrupt)
	select {
	case code := <-exitCode:
		if code != 130 {
			t.Errorf("exit code = %d, want 130", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second signal didn't trigger exit()")
	}
}

// TestInstallSignalHandler_NoSignals_ReturnsCancelable hits the
// defensive "empty signal slice" branch by stubbing
// platformShutdownSignalsFn to return nothing. The returned cancel
// must still cancel the context - a future platform with no signals at
// all shouldn't lose context plumbing.
func TestInstallSignalHandler_NoSignals_ReturnsCancelable(t *testing.T) {
	orig := platformShutdownSignalsFn
	platformShutdownSignalsFn = func() []os.Signal { return nil }
	t.Cleanup(func() { platformShutdownSignalsFn = orig })

	ctx, cancel := installSignalHandler(context.Background())
	if ctx == nil {
		t.Fatal("nil ctx")
	}
	cancel()
	select {
	case <-ctx.Done():
 // expected - cancel cancels
	case <-time.After(time.Second):
		t.Fatal("cancel did not cancel ctx")
	}
}

// discard is an io.Writer that drops everything - used to silence
// stderr during signal tests.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
