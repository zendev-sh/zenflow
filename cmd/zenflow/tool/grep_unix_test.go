//go:build !windows

package tool

import (
	"errors"
	"sync"
	"testing"
)

// TestResolvedGrepBinary_ReturnsNonEmpty verifies the grep binary resolver
// returns a non-empty path on Unix.
func TestResolvedGrepBinary_ReturnsNonEmpty(t *testing.T) {
	p := resolvedGrepBinary()
	if p == "" {
		t.Fatal("resolvedGrepBinary() returned empty string")
	}
}

// TestResolvedGrepBinary_LookPathFails covers the LookPath fallback branch:
// when LookPath returns an error the resolver falls back to the bare "grep" string.
func TestResolvedGrepBinary_LookPathFails(t *testing.T) {
	// Override the LookPath seam and reset the once so the fallback runs.
	orig := lookPathFunc
	t.Cleanup(func() {
		lookPathFunc = orig
		grepBinaryOnce = sync.Once{}
		grepBinaryPath = ""
	})
	lookPathFunc = func(string) (string, error) { return "", errors.New("not found") }
	grepBinaryOnce = sync.Once{}
	grepBinaryPath = ""

	got := resolvedGrepBinary()
	if got != "grep" {
		t.Errorf("resolvedGrepBinary() = %q, want %q (fallback)", got, "grep")
	}
}
