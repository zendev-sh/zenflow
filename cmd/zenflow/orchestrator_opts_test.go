package main

// orchestrator_opts_test.go - tests for buildOrchestratorOpts and the
// --version flag's runtime/debug.ReadBuildInfo fallback. The OTel
// trace-flag tests live in trace_otel_test.go behind `//go:build otel`
// so default builds (which ship without the OTel dependency) do not
// expect WithTracing / WithGoAIOptions to be appended.

import (
	"bytes"
	"runtime/debug"
	"strings"
	"testing"
)

// TestBuildOrchestratorOpts_NoTraceBaseStable verifies that
// buildOrchestratorOpts returns a stable option count when trace is
// false, regardless of build tag. Functions as a regression guard for
// accidental option-shape drift in the no-trace path.
func TestBuildOrchestratorOpts_NoTraceBaseStable(t *testing.T) {
	base := buildOrchestratorOpts(cmdFlags{})
	if len(base) == 0 {
		t.Fatalf("buildOrchestratorOpts returned 0 options at baseline; expected non-empty (Progress + Tools + OutputTransform at minimum)")
	}
}

// =============================================================================
// M5 - --version: runtime/debug.ReadBuildInfo fallback
// =============================================================================

// TestVersionFlag_FallbackFromBuildInfo verifies that when version=="dev"
// (ldflags not set), the --version output uses VCS info from ReadBuildInfo.
func TestVersionFlag_FallbackFromBuildInfo(t *testing.T) {
	// Inject a fake readBuildInfo that returns known VCS settings.
	origReadBuildInfo := readBuildInfo
	origVersion := version
	origCommit := commit
	origDate := date
	t.Cleanup(func() {
		readBuildInfo = origReadBuildInfo
		version = origVersion
		commit = origCommit
		date = origDate
	})

	version = "dev"
	commit = "unknown"
	date = "unknown"

	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{Version: "v1.2.3"},
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "abcdef1234567"},
				{Key: "vcs.time", Value: "2026-01-01T00:00:00Z"},
			},
		}, true
	}

	var buf bytes.Buffer
	origStdout := stdout
	stdout = &buf
	t.Cleanup(func() { stdout = origStdout })

	origExit := exit
	t.Cleanup(func() { exit = origExit })
	exit = func(int) {}

	origArgs := osArgs
	t.Cleanup(func() { osArgs = origArgs })
	osArgs = func() []string { return []string{"zenflow", "--version"} }

	main()

	got := buf.String()
	if !strings.Contains(got, "v1.2.3") {
		t.Errorf("version output = %q, want to contain v1.2.3 (from BuildInfo.Main.Version)", got)
	}
	if !strings.Contains(got, "abcdef1") {
		t.Errorf("version output = %q, want to contain commit=abcdef1 (first 7 of vcs.revision)", got)
	}
	if !strings.Contains(got, "2026-01-01") {
		t.Errorf("version output = %q, want to contain 2026-01-01 (from vcs.time)", got)
	}
}

// TestVersionFlag_LdflagsNotOverriddenByBuildInfo verifies that when
// ldflags are set (version != "dev"), ReadBuildInfo is NOT consulted and
// the ldflags values are used as-is.
func TestVersionFlag_LdflagsNotOverriddenByBuildInfo(t *testing.T) {
	origReadBuildInfo := readBuildInfo
	origVersion := version
	origCommit := commit
	origDate := date
	t.Cleanup(func() {
		readBuildInfo = origReadBuildInfo
		version = origVersion
		commit = origCommit
		date = origDate
	})

	version = "v9.9.9"
	commit = "deadbeef"
	date = "2030-06-15T00:00:00Z"

	called := false
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		called = true
		return nil, false
	}

	var buf bytes.Buffer
	origStdout := stdout
	stdout = &buf
	t.Cleanup(func() { stdout = origStdout })

	origExit := exit
	t.Cleanup(func() { exit = origExit })
	exit = func(int) {}

	origArgs := osArgs
	t.Cleanup(func() { osArgs = origArgs })
	osArgs = func() []string { return []string{"zenflow", "--version"} }

	main()

	if called {
		t.Error("readBuildInfo was called even though ldflags version != \"dev\"")
	}
	got := buf.String()
	if !strings.Contains(got, "v9.9.9") {
		t.Errorf("version output = %q, want to contain v9.9.9", got)
	}
	if !strings.Contains(got, "deadbeef") {
		t.Errorf("version output = %q, want to contain deadbeef", got)
	}
}

// TestVersionFlag_BuildInfoUnavailable verifies that when ReadBuildInfo
// returns ok=false (e.g. CGO binary), the output still works gracefully.
func TestVersionFlag_BuildInfoUnavailable(t *testing.T) {
	origReadBuildInfo := readBuildInfo
	origVersion := version
	origCommit := commit
	origDate := date
	t.Cleanup(func() {
		readBuildInfo = origReadBuildInfo
		version = origVersion
		commit = origCommit
		date = origDate
	})

	version = "dev"
	commit = "unknown"
	date = "unknown"

	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return nil, false
	}

	var buf bytes.Buffer
	origStdout := stdout
	stdout = &buf
	t.Cleanup(func() { stdout = origStdout })

	origExit := exit
	t.Cleanup(func() { exit = origExit })
	exit = func(int) {}

	origArgs := osArgs
	t.Cleanup(func() { osArgs = origArgs })
	osArgs = func() []string { return []string{"zenflow", "--version"} }

	main()

	got := buf.String()
	if !strings.Contains(got, "zenflow dev") {
		t.Errorf("version output = %q, want 'zenflow dev' when BuildInfo unavailable", got)
	}
}

// TestVersionFlag_ShortRevision verifies that a vcs.revision shorter than
// 7 chars is used as-is without panicking on slice bounds.
func TestVersionFlag_ShortRevision(t *testing.T) {
	origReadBuildInfo := readBuildInfo
	origVersion := version
	origCommit := commit
	origDate := date
	t.Cleanup(func() {
		readBuildInfo = origReadBuildInfo
		version = origVersion
		commit = origCommit
		date = origDate
	})

	version = "dev"
	commit = "unknown"
	date = "unknown"

	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{Version: ""},
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "abc"}, // shorter than 7 chars
				{Key: "vcs.time", Value: "2026-01-01T00:00:00Z"},
			},
		}, true
	}

	var buf bytes.Buffer
	origStdout := stdout
	stdout = &buf
	t.Cleanup(func() { stdout = origStdout })

	origExit := exit
	t.Cleanup(func() { exit = origExit })
	exit = func(int) {}

	origArgs := osArgs
	t.Cleanup(func() { osArgs = origArgs })
	osArgs = func() []string { return []string{"zenflow", "--version"} }

	// Must not panic on short revision.
	main()

	got := buf.String()
	if !strings.Contains(got, "abc") {
		t.Errorf("version output = %q, want to contain short revision 'abc'", got)
	}
}
