package exec

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// slowApprover blocks ApprovePlan until ctx is cancelled, simulating
// a user who never answered the approval prompt.
type slowApprover struct{}

func (slowApprover) ApprovePlan(ctx context.Context, _ *Workflow) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

// TestApprovalTimeout_Wraps - WithApprovalTimeout wraps the handler
// so ApprovePlan returns (false, ErrApprovalTimeout) when the
// configured deadline elapses.
func TestApprovalTimeout_Wraps(t *testing.T) {
	o := &Orchestrator{}
	WithApproval(slowApprover{})(o)
	WithApprovalTimeout(30 * time.Millisecond)(o)

	h := o.approval
	if h == nil {
		t.Fatal("approval handler nil after WithApprovalTimeout")
	}

	start := time.Now()
	ok, err := h.ApprovePlan(t.Context(), &Workflow{Name: "x", Steps: []Step{{ID: "s"}}})
	elapsed := time.Since(start)

	if ok {
		t.Fatal("ApprovePlan returned ok=true; want false on timeout")
	}
	if !errors.Is(err, ErrApprovalTimeout) {
		t.Fatalf("err = %v; want ErrApprovalTimeout", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("timeout too slow: %v", elapsed)
	}
}

// panicApprover simulates a buggy ApprovalHandler that panics during
// ApprovePlan. Used to exercise the recover branch in
// approvalTimeoutHandler.
type panicApprover struct{}

func (panicApprover) ApprovePlan(_ context.Context, _ *Workflow) (bool, error) {
	panic("approval logic exploded")
}

// TestApprovalTimeout_RecoversPanic covers the panic-recovery
// branch in approvalTimeoutHandler. A panic in the user-supplied
// handler must NOT crash the process; the wrapped handler converts it
// to a typed error so the workflow aborts cleanly.
func TestApprovalTimeout_RecoversPanic(t *testing.T) {
	o := &Orchestrator{}
	WithApproval(panicApprover{})(o)
	WithApprovalTimeout(500 * time.Millisecond)(o)

	h := o.approval
	ok, err := h.ApprovePlan(t.Context(), &Workflow{Name: "x", Steps: []Step{{ID: "s"}}})
	if ok {
		t.Fatal("expected deny when handler panics")
	}
	if err == nil {
		t.Fatal("expected error when handler panics")
	}
	if !strings.Contains(err.Error(), "approval handler panicked") {
		t.Errorf("err = %v, want 'approval handler panicked' wrap", err)
	}
}

// TestApprovalTimeout_ZeroDisabled - zero duration leaves the handler
// untouched.
func TestApprovalTimeout_ZeroDisabled(t *testing.T) {
	inner := slowApprover{}
	o := &Orchestrator{}
	WithApproval(inner)(o)
	WithApprovalTimeout(0)(o)

	if o.approval == nil {
		t.Fatal("approval nil")
	}
	// Ensure the wrapper was NOT applied (approval is the original handler).
	if _, wrapped := o.approval.(*approvalTimeoutHandler); wrapped {
		t.Fatal("zero timeout should not wrap handler")
	}
}
