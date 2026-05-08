package spec

import (
	"testing"
	"time"
)

// Coverage backfill for branches that root-package tests exercise via the
// public facade aliases. Go's per-package coverage only credits in-package
// callers, so even though *Workflow.Result, FormatDuration's negative
// branch, and ParseDurationStrict's overflow branch are exhaustively
// tested in zenflow_test.go and executor_nilguards_coverage_test.go, the
// internal/spec coverage gate ignores those calls. The tests below
// re-execute the same paths inside package spec.

func TestWorkflowResult_Result(t *testing.T) {
	if _, ok := (*WorkflowResult)(nil).Result("x"); ok {
		t.Errorf("nil receiver Result should return ok=false")
	}
	wr := &WorkflowResult{Steps: map[string]*StepResult{
		"alive":      {ID: "alive", Status: StepCompleted, Content: "out"},
		"nil-stored": nil,
	}}
	got, ok := wr.Result("alive")
	if !ok || got.ID != "alive" || got.Content != "out" {
		t.Errorf("Result(alive) = %+v ok=%v, want non-zero with ok=true", got, ok)
	}
	if _, ok := wr.Result("nil-stored"); ok {
		t.Errorf("Result(nil-stored) ok=true, want false (nil pointer)")
	}
	if _, ok := wr.Result("missing"); ok {
		t.Errorf("Result(missing) ok=true, want false (absent key)")
	}
}

func TestWorkflowResult_FinalAnswer(t *testing.T) {
	// nil receiver returns "".
	if got := (*WorkflowResult)(nil).FinalAnswer(nil); got != "" {
		t.Errorf("nil receiver FinalAnswer = %q, want \"\"", got)
	}
	// (a) Summary takes priority over step inspection.
	wf := &Workflow{
		Name: "wf",
		Steps: []Step{
			{ID: "a"},
			{ID: "b", DependsOn: []string{"a"}},
		},
	}
	withSummary := &WorkflowResult{
		Summary: "coord-summary",
		Steps: map[string]*StepResult{
			"a": {ID: "a", Status: StepCompleted, Content: "step-a"},
			"b": {ID: "b", Status: StepCompleted, Content: "step-b"},
		},
	}
	if got := withSummary.FinalAnswer(wf); got != "coord-summary" {
		t.Errorf("FinalAnswer with Summary = %q, want %q", got, "coord-summary")
	}
	// (b) Summary empty -> last terminal step's Content. With deps a->b, only b is terminal.
	noSummary := &WorkflowResult{
		Steps: map[string]*StepResult{
			"a": {ID: "a", Status: StepCompleted, Content: "step-a"},
			"b": {ID: "b", Status: StepCompleted, Content: "step-b"},
		},
	}
	if got := noSummary.FinalAnswer(wf); got != "step-b" {
		t.Errorf("FinalAnswer terminal-only = %q, want %q", got, "step-b")
	}
	// (b2) Multiple terminal steps -> LAST in declaration order wins.
	wfMulti := &Workflow{
		Name: "wf",
		Steps: []Step{
			{ID: "x"},
			{ID: "y"},
		},
	}
	multi := &WorkflowResult{
		Steps: map[string]*StepResult{
			"x": {ID: "x", Status: StepCompleted, Content: "x-out"},
			"y": {ID: "y", Status: StepCompleted, Content: "y-out"},
		},
	}
	if got := multi.FinalAnswer(wfMulti); got != "y-out" {
		t.Errorf("FinalAnswer multi-terminal = %q, want %q", got, "y-out")
	}
	// (c) No terminal completed step -> "".
	wfFailed := &Workflow{
		Name: "wf",
		Steps: []Step{
			{ID: "a"},
		},
	}
	failed := &WorkflowResult{
		Steps: map[string]*StepResult{
			"a": {ID: "a", Status: StepFailed, Content: "boom"},
		},
	}
	if got := failed.FinalAnswer(wfFailed); got != "" {
		t.Errorf("FinalAnswer no-completed = %q, want \"\"", got)
	}
	// (c2) wf nil with empty Summary.
	if got := noSummary.FinalAnswer(nil); got != "" {
		t.Errorf("FinalAnswer nil-wf = %q, want \"\"", got)
	}
	// (c3) Steps map nil with empty Summary.
	emptySteps := &WorkflowResult{}
	if got := emptySteps.FinalAnswer(wf); got != "" {
		t.Errorf("FinalAnswer nil-Steps = %q, want \"\"", got)
	}
	// (c4) Empty wf.Steps with non-nil result Steps.
	emptyWF := &Workflow{Name: "wf"}
	if got := noSummary.FinalAnswer(emptyWF); got != "" {
		t.Errorf("FinalAnswer empty-wf-steps = %q, want \"\"", got)
	}
}

func TestFormatDuration_NegativeBranches(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{-30 * time.Second, "-30s"},
		{-5 * time.Minute, "-5m"},
		{-time.Hour - 30*time.Minute, "-1h30m"},
		{-500 * time.Millisecond, "-0s"},
	}
	for _, tc := range cases {
		if got := FormatDuration(tc.in); got != tc.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseDurationStrict_Overflow(t *testing.T) {
	overflowing := "99999999999999999999h"
	if _, err := ParseDurationStrict(overflowing); err == nil {
		t.Errorf("expected overflow rejection for %q", overflowing)
	}
}

func TestParseDurationStrict_RejectionBranches(t *testing.T) {
	rejects := []string{
		"",        // empty path
		"-5m",     // negative path
		"500ms",   // sub-second / non-matching pattern
		"30ms30s", // mixed precision
	}
	for _, s := range rejects {
		if _, err := ParseDurationStrict(s); err == nil {
			t.Errorf("ParseDurationStrict(%q) returned nil error; want validation error", s)
		}
	}
	if _, err := ParseDurationStrict("30s"); err != nil {
		t.Errorf("ParseDurationStrict(30s) err=%v, want nil", err)
	}
}
