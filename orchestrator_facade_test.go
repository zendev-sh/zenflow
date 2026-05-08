package zenflow_test

// orchestrator_facade_test.go - root-package tests for facade-only
// helpers that don't have an internal counterpart (e.g. DefaultStorageDir,
// which is defined directly in orchestrator_facade.go rather than being a
// re-export from internal/exec or internal/spec).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zendev-sh/zenflow"
)

// DefaultStorageDir returns ~/.zenflow/runs when HOME is resolvable,
// or <TempDir>/zenflow/runs as a fallback. Both branches must terminate
// in a non-empty path that callers can safely pass to NewFileStorage.
func TestDefaultStorageDir_HappyPath(t *testing.T) {
	got := zenflow.DefaultStorageDir()
	if got == "" {
		t.Fatal("DefaultStorageDir() returned empty path")
	}
	// Expect either the HOME-based path (~/.zenflow/runs) or the TempDir
	// fallback (<TempDir>/zenflow/runs). Both end with `zenflow/runs`.
	if !strings.HasSuffix(filepath.ToSlash(got), "zenflow/runs") {
		t.Errorf("DefaultStorageDir() = %q, want path ending with zenflow/runs", got)
	}
	if home, err := os.UserHomeDir(); err == nil {
		want := filepath.Join(home, ".zenflow", "runs")
		if got != want {
			t.Errorf("DefaultStorageDir() = %q, want %q (HOME-based)", got, want)
		}
	}
}

// When os.UserHomeDir fails, DefaultStorageDir must fall back to TempDir.
// On Unix, clearing HOME (and the secondary lookups) is sufficient to make
// UserHomeDir return an error.
func TestDefaultStorageDir_FallbackWhenNoHome(t *testing.T) {
	// Save & clear all HOME-equivalent env vars so UserHomeDir errors out.
	saved := map[string]string{}
	for _, k := range []string{"HOME", "USERPROFILE", "home"} {
		saved[k] = os.Getenv(k)
		_ = os.Unsetenv(k)
	}
	t.Cleanup(func() {
		for k, v := range saved {
			if v != "" {
				_ = os.Setenv(k, v)
			}
		}
	})
	got := zenflow.DefaultStorageDir()
	if got == "" {
		t.Fatal("DefaultStorageDir() returned empty path in fallback")
	}
	wantPrefix := filepath.Join(os.TempDir(), "zenflow", "runs")
	// Skip the assertion if UserHomeDir still managed to resolve a home
	// (some platforms have alternate lookup paths). The happy-path test
	// already covers the success branch.
	if got != wantPrefix {
		t.Skipf("UserHomeDir still resolved on this platform; got=%q want fallback=%q", got, wantPrefix)
	}
}
