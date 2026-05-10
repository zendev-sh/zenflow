package exec

import (
	"errors"
	"fmt"
	"testing"

	"github.com/zendev-sh/zenflow/internal/router"
)

func TestErrorMessages(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{&ValidationError{Message: "bad yaml"}, "validation: bad yaml"},
		{&CycleError{Message: "a->b->a"}, "cycle detected: a->b->a"},
		{&MissingAgentError{Message: "x", StepID: "s1", Agent: "alice"}, `missing agent: x (step "s1", agent "alice")`},
		{&DuplicateStepError{Message: "x", StepID: "s1"}, `duplicate step: x (step "s1")`},
		{&MissingDepError{Message: "x", StepID: "s1", Dep: "s0"}, `missing dependency: x (step "s1", dep "s0")`},
		{&NoStepsError{Message: "x"}, "no steps: x"},
		{&MissingNameError{Message: "x"}, "missing name: x"},
		{&IncludeConflictError{Message: "x", StepID: "s1", Field: "agent"}, `include conflict: x (step "s1", field "agent")`},
		{&LoopValidationError{Message: "x", StepID: "s1"}, `loop validation: x (step "s1")`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(fmt.Sprintf("%T", tt.err), func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("%T.Error() = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestErrorInterfaces(t *testing.T) {
	// Verify all error types implement the error interface.
	var _ error = &ValidationError{}
	var _ error = &CycleError{}
	var _ error = &MissingAgentError{}
	var _ error = &DuplicateStepError{}
	var _ error = &MissingDepError{}
	var _ error = &NoStepsError{}
	var _ error = &MissingNameError{}
	var _ error = &IncludeConflictError{}
	var _ error = &LoopValidationError{}
}

func TestCoordinatorErrors(t *testing.T) {
	t.Run("JSONParseError", func(t *testing.T) {
		inner := errors.New("unexpected EOF")
		err := &JSONParseError{Err: inner}
		if got := err.Error(); got != "zenflow: json parse: unexpected EOF" {
			t.Errorf("Error() = %q, want %q", got, "zenflow: json parse: unexpected EOF")
		}
		if !errors.Is(err.Unwrap(), inner) {
			t.Error("Unwrap() should return the inner error")
		}
	})

	t.Run("CoordinatorValidationError", func(t *testing.T) {
		inner := errors.New("bad plan")
		err := &CoordinatorValidationError{Err: inner}
		if got := err.Error(); got != "zenflow: coordinator validation: bad plan" {
			t.Errorf("Error() = %q, want %q", got, "zenflow: coordinator validation: bad plan")
		}
		if !errors.Is(err.Unwrap(), inner) {
			t.Error("Unwrap() should return the inner error")
		}
	})

	t.Run("ToolNotFoundError", func(t *testing.T) {
		err := &ToolNotFoundError{Tool: "deploy", Agent: "coder"}
		want := `zenflow: validation: agent "coder" references unknown tool "deploy"`
		if got := err.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})
}

// TestDropError_Error verifies the canonical "dropped: <reason>" text
// for every DropReason value. This is the contract substring-matching
// consumers (LLM tool-result strings, log greppers) depend on; a
// future change to DropReason.String must keep DropError.Error in
// lock-step.
func TestDropError_Error(t *testing.T) {
	cases := []struct {
		reason DropReason
		want   string
	}{
		{router.DropReasonWorkflowCancelled, "dropped: workflow-cancelled"},
		{router.DropReasonTargetTerminal, "dropped: target-terminal"},
		{router.DropReasonUnknownStep, "dropped: unknown-step"},
		{router.DropReasonMailboxClosedByFinalize, "dropped: mailbox-closed-by-finalize"},
		{router.DropReasonMaxWakeCycles, "dropped: max-wake-cycles"},
		{router.DropReasonHoldTimeout, "dropped: hold-timeout"},
		{router.DropReasonMailboxFull, "dropped: mailbox-full"},
		{router.DropReasonNoTranscript, "dropped: no-transcript"},
		{router.DropReasonTranscriptTooLarge, "dropped: transcript-too-large"},
		{router.DropReasonResumeShutdown, "dropped: resume-shutdown"},
		{router.DropReasonResolverError, "dropped: resolver-error"},
		{router.DropReasonUnspecified, "dropped: unspecified"},
	}
	for _, tc := range cases {
		t.Run(tc.reason.String(), func(t *testing.T) {
			got := (&DropError{Reason: tc.reason}).Error()
			if got != tc.want {
				t.Errorf("(&DropError{Reason: %v}).Error() = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
}

// TestDropError_ErrorsAs verifies the typed-error contract: callers
// can extract the typed *DropError from a wrapped error chain via
// errors.As, then read the Reason field directly. The whole point of
// the typed error (vs the previous opaque errors.New) is to avoid
// substring-matching err.Error for routing decisions.
func TestDropError_ErrorsAs(t *testing.T) {
	original := &DropError{Reason: router.DropReasonUnknownStep}
	wrapped := errors.Join(errors.New("router send failed"), original)

	var de *DropError
	if !errors.As(wrapped, &de) {
		t.Fatalf("errors.As failed to extract *DropError from wrapped chain: %v", wrapped)
	}
	if de.Reason != router.DropReasonUnknownStep {
		t.Errorf("extracted Reason = %v, want %v", de.Reason, router.DropReasonUnknownStep)
	}
	// Also verify direct (unwrapped) extraction.
	var de2 *DropError
	if !errors.As(error(original), &de2) {
		t.Fatal("errors.As failed on direct *DropError")
	}
	if de2 != original {
		t.Errorf("errors.As returned %p, want pointer-equal to original %p", de2, original)
	}
}

// TestSentinelErrors_Identity verifies the new sentinel errors round-trip
// through errors.Is. Documents the contract that callers use to match
// these errors.
func TestSentinelErrors_Identity(t *testing.T) {
	cases := []error{
		ErrModelRequired,
		ErrStorageRequired,
		ErrNilAgentHandle,
		ErrNilOrchestrator,
		ErrResumeNoModel,
		ErrWorkflowNil,
		ErrPlanDenied,
		ErrApprovalTimeout,
		ErrEmptyGoal,
	}
	for _, sentinel := range cases {
		t.Run(sentinel.Error(), func(t *testing.T) {
			if !errors.Is(sentinel, sentinel) {
				t.Errorf("errors.Is(%v, %v) = false, want true", sentinel, sentinel)
			}
			// Wrapping must not lose identity.
			wrapped := errors.Join(errors.New("outer"), sentinel)
			if !errors.Is(wrapped, sentinel) {
				t.Errorf("errors.Is(wrapped, sentinel) = false; sentinel %v escaped errors.Is", sentinel)
			}
		})
	}
}

// TestValidationErrors_Unwrap covers the Unwrap methods on every
// typed validation error so errors.Is / errors.As see through the
// wrapper to the inner cause. Each Unwrap is a one-liner returning
// e.Err - the test exercises both the "wrapped" path (Err set, Unwrap
// returns it) and the "no inner" path (Err nil, Unwrap returns nil).
func TestValidationErrors_Unwrap(t *testing.T) {
	inner := errors.New("inner cause")

	cases := []struct {
		name    string
		wrapped error
		bare    error
	}{
		{"ValidationError", &ValidationError{Message: "x", Err: inner}, &ValidationError{Message: "x"}},
		{"CycleError", &CycleError{Message: "x", Err: inner}, &CycleError{Message: "x"}},
		{"MissingAgentError", &MissingAgentError{Message: "x", Err: inner}, &MissingAgentError{Message: "x"}},
		{"DuplicateStepError", &DuplicateStepError{Message: "x", Err: inner}, &DuplicateStepError{Message: "x"}},
		{"MissingDepError", &MissingDepError{Message: "x", Err: inner}, &MissingDepError{Message: "x"}},
		{"NoStepsError", &NoStepsError{Message: "x", Err: inner}, &NoStepsError{Message: "x"}},
		{"MissingNameError", &MissingNameError{Message: "x", Err: inner}, &MissingNameError{Message: "x"}},
		{"IncludeConflictError", &IncludeConflictError{Message: "x", Err: inner}, &IncludeConflictError{Message: "x"}},
		{"LoopValidationError", &LoopValidationError{Message: "x", Err: inner}, &LoopValidationError{Message: "x"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name+"/wrapped", func(t *testing.T) {
			if !errors.Is(tc.wrapped, inner) {
				t.Errorf("errors.Is(%T{Err: inner}, inner) = false; Unwrap chain broken", tc.wrapped)
			}
		})
		t.Run(tc.name+"/bare", func(t *testing.T) {
			if got := errors.Unwrap(tc.bare); got != nil {
				t.Errorf("errors.Unwrap(%T{}) = %v, want nil", tc.bare, got)
			}
		})
	}
}

// TestNewSentinelErrors_WrapSite verifies that ErrRunNotFound, ErrStepNotFound,
// ErrAgentTurnLimitExceeded, and ErrAgentNoSubmitResult are visible via errors.Is
// through fmt.Errorf("%w") wrapping - confirming the wrap-site contract.
func TestNewSentinelErrors_WrapSite(t *testing.T) {
	t.Run("ErrRunNotFound via fmt.Errorf wrap", func(t *testing.T) {
		wrapped := fmt.Errorf("zenflow: run %q: %w", "r1", ErrRunNotFound)
		if !errors.Is(wrapped, ErrRunNotFound) {
			t.Errorf("errors.Is = false; wrapped = %v", wrapped)
		}
	})
	t.Run("ErrStepNotFound via fmt.Errorf wrap", func(t *testing.T) {
		wrapped := fmt.Errorf("zenflow: step %q/%q: %w", "r1", "s1", ErrStepNotFound)
		if !errors.Is(wrapped, ErrStepNotFound) {
			t.Errorf("errors.Is = false; wrapped = %v", wrapped)
		}
	})
	t.Run("ErrAgentTurnLimitExceeded via fmt.Errorf wrap", func(t *testing.T) {
		wrapped := fmt.Errorf("agent exhausted %d turns: %w", 5, ErrAgentTurnLimitExceeded)
		if !errors.Is(wrapped, ErrAgentTurnLimitExceeded) {
			t.Errorf("errors.Is = false; wrapped = %v", wrapped)
		}
	})
	t.Run("ErrAgentNoSubmitResult via fmt.Errorf wrap", func(t *testing.T) {
		wrapped := fmt.Errorf("%w some trailing text", ErrAgentNoSubmitResult)
		if !errors.Is(wrapped, ErrAgentNoSubmitResult) {
			t.Errorf("errors.Is = false; wrapped = %v", wrapped)
		}
	})
}
