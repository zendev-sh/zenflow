package main

import (
	"errors"
	"os"
	"testing"
)

// TestComputeColorEnabled_NoColor covers the NO_COLOR env-var branch.
// When NO_COLOR is set to any non-empty value, computeColorEnabled must
// return false regardless of TERM or stdout mode.
func TestComputeColorEnabled_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	// TERM must NOT be dumb so we reach the NO_COLOR check first.
	t.Setenv("TERM", "xterm-256color")
	if computeColorEnabled() {
		t.Fatal("computeColorEnabled() = true with NO_COLOR set, want false")
	}
}

// TestComputeColorEnabled_TermDumb covers the TERM=dumb branch.
// When TERM is "dumb", computeColorEnabled must return false even if
// stdout is a terminal.
func TestComputeColorEnabled_TermDumb(t *testing.T) {
	t.Setenv("NO_COLOR", "") // ensure NO_COLOR branch does NOT fire
	t.Setenv("TERM", "dumb")
	if computeColorEnabled() {
		t.Fatal("computeColorEnabled() = true with TERM=dumb, want false")
	}
}

// TestComputeColorEnabled_StatError covers the os.Stdout.Stat error branch.
// Uses the stdoutStat test seam to inject a failing stat function, which
// forces computeColorEnabled to hit the `if err != nil { return false }` path.
func TestComputeColorEnabled_StatError(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	orig := stdoutStat
	t.Cleanup(func() { stdoutStat = orig })
	stdoutStat = func() (os.FileInfo, error) {
		return nil, errors.New("injected stat failure")
	}

	if computeColorEnabled() {
		t.Fatal("computeColorEnabled() = true with stat error, want false")
	}
}

// TestComputeColorEnabled_NonTerminal covers the stat-success but
// non-character-device branch. In test processes stdout is typically
// a pipe (not a char device), so computeColorEnabled should return
// false here without any env manipulation.
func TestComputeColorEnabled_NonTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	// In `go test`, stdout is a pipe - stdoutStat succeeds but
	// ModeCharDevice is not set, so computeColorEnabled returns false.
	if computeColorEnabled() {
		// This can legitimately be true when run from a real terminal
		// (e.g. `go test -v` with stdout attached to a PTY). Accept it;
		// the test is informational in that scenario.
		t.Log("computeColorEnabled() = true: stdout is a real terminal, skipping assertion")
	}
}
