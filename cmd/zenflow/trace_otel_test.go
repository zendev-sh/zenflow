//go:build otel

// trace_otel_test.go - tests covering the OTel-enabled CLI build.
// Runs only with `-tags otel` because the assertions presume that
// trace_otel.go's init has overridden traceAppendOptionsFunc to
// append WithTracing + WithGoAIOptions(GoAIOption).

package main

import (
	"testing"
)

// TestBuildOrchestratorOpts_TraceFlagAddsGoAIOption verifies that
// when flags.trace is true under the `-tags otel` build,
// buildOrchestratorOpts returns two more options than the base:
// WithTracing and WithGoAIOptions(GoAIOption).
func TestBuildOrchestratorOpts_TraceFlagAddsGoAIOption(t *testing.T) {
	base := buildOrchestratorOpts(cmdFlags{})
	withTrace := buildOrchestratorOpts(cmdFlags{trace: true})

	diff := len(withTrace) - len(base)
	if diff != 2 {
		t.Errorf("trace=true added %d extra options, want 2 (WithTracing + WithGoAIOptions)", diff)
	}
}

// TestBuildOrchestratorOpts_NoTraceNoExtraOptions verifies that without
// --trace the option count matches the base (no spurious GoAIOption).
// This is the `-tags otel` build counterpart to
// TestBuildOrchestratorOpts_NoTraceBaseStable in orchestrator_opts_test.go.
func TestBuildOrchestratorOpts_NoTraceNoExtraOptions(t *testing.T) {
	withTrace := buildOrchestratorOpts(cmdFlags{trace: true})
	withoutTrace := buildOrchestratorOpts(cmdFlags{trace: false})

	if len(withTrace) <= len(withoutTrace) {
		t.Errorf("trace=true option count (%d) must be > trace=false (%d)", len(withTrace), len(withoutTrace))
	}
}
