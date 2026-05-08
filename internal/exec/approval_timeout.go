package exec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// approvalTimeoutHandler wraps an ApprovalHandler with a deadline.
// On timeout, ApprovePlan returns (false, ErrApprovalTimeout) and the
// wrapped handler's ctx is cancelled so any blocking UI prompt can
// unwind cleanly.
type approvalTimeoutHandler struct {
	inner   ApprovalHandler
	timeout time.Duration
}

// Compile-time assertion catching signature drift on ApprovalHandler
// at the wrapper definition.
var _ ApprovalHandler = (*approvalTimeoutHandler)(nil)

func (h *approvalTimeoutHandler) ApprovePlan(ctx context.Context, wf *Workflow) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	type result struct {
		ok  bool
		err error
	}
	ch := make(chan result, 1)
	go func() {
 // Recover panics in the user-supplied ApprovalHandler.
 // Consistent with every other user-callback goroutine in the
 // codebase (RunAgentAsync registry-cleanup, TTL watchdog, agent
 // goroutine, routerObserver). A panic in approval logic would
 // otherwise crash the whole process.
		defer func() {
			if r := recover(); r != nil {
				slog.Warn("panic in ApprovalHandler.ApprovePlan",
					"hook", "approvalHandler",
					"panic", r,
				)
				ch <- result{false, fmt.Errorf("approval handler panicked: %v", r)}
			}
		}()
		ok, err := h.inner.ApprovePlan(ctx, wf)
		ch <- result{ok, err}
	}()

	select {
	case r := <-ch:
 // Even if inner returned ctx.Err, surface ErrApprovalTimeout
 // so callers can distinguish "timeout" from "user cancel".
		if errors.Is(r.err, context.DeadlineExceeded) {
			return false, ErrApprovalTimeout
		}
		return r.ok, r.err
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return false, ErrApprovalTimeout
		}
		return false, ctx.Err()
	}
}

// WithApprovalTimeout bounds how long ApprovalHandler.ApprovePlan may
// block. Must be applied AFTER WithApproval. Zero or negative is a
// no-op (no timeout, the default). On timeout, ApprovePlan returns
// (false, ErrApprovalTimeout) and RunGoal aborts cleanly.
// Stable.
func WithApprovalTimeout(d time.Duration) Option {
	return func(o *Orchestrator) {
		if d <= 0 || o.approval == nil {
			return
		}
 // Avoid double-wrapping if the option is applied twice.
		if _, ok := o.approval.(*approvalTimeoutHandler); ok {
			return
		}
		o.approval = &approvalTimeoutHandler{inner: o.approval, timeout: d}
	}
}
