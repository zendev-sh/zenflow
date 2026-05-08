package exec

import (
	"errors"
	"fmt"
	"strings"
)

// ErrApprovalTimeout indicates the ApprovalHandler.ApprovePlan call
// exceeded the duration configured via WithApprovalTimeout. When
// returned, the workflow aborts cleanly.
var ErrApprovalTimeout = errors.New("zenflow: approval handler timed out (use WithApprovalTimeout to adjust the window)")

// ErrModelRequired is returned when an entrypoint that needs an LLM
// is called without one configured. Caller fix: pass WithModel(...) to
// New or set Model on the per-call AgentConfig.
var ErrModelRequired = errors.New("zenflow: LLM provider is required (use WithModel)")

// ErrStorageRequired is returned by ResumeFlow when no Storage backend
// is configured. Caller fix: pass WithStorage(...) to New.
var ErrStorageRequired = errors.New("zenflow: storage is required for resume (use WithStorage)")

// ErrNilAgentHandle is returned by methods called on a nil *AgentHandle
// receiver (defensive guard for callers that race a Close with concurrent
// use of the handle).
var ErrNilAgentHandle = errors.New("zenflow: nil AgentHandle")

// ErrNilOrchestrator is returned by methods called on a nil *Orchestrator
// receiver. Same defensive intent as ErrNilAgentHandle.
var ErrNilOrchestrator = errors.New("zenflow: nil Orchestrator")

// ErrResumeNoModel is returned by ResumeStep when neither the transcript
// nor the executor has a model resolver - resume cannot construct a
// runner without one.
var ErrResumeNoModel = errors.New("zenflow: resume: executor has no model (use WithModel)")

// ErrWorkflowNil is returned by RunFlow and ResumeFlow when the
// caller passes a nil *Workflow. Defensive guard for callers that
// race a workflow load with a per-call run.
var ErrWorkflowNil = errors.New("zenflow: workflow must not be nil")

// ErrPlanDenied is returned by RunGoal when the configured
// ApprovalHandler returns false for the LLM-decomposed plan. Distinct
// from ErrApprovalTimeout (handler ran but exceeded its window) so
// callers can tell "I said no" from "the handler took too long."
var ErrPlanDenied = errors.New("zenflow: plan denied by approval handler")

// ErrRunnerNil is returned by Executor.Run when called on a struct
// constructed without a Runner. Extracted from a bare fmt.Errorf at the
// call site so callers can errors.Is on it. Most production callers go
// through Orchestrator (which fills Runner internally) so this is most
// often hit by direct-Executor unit tests that forgot the Runner field.
var ErrRunnerNil = errors.New("zenflow: Executor.Runner is nil (use Orchestrator or set Runner before calling Run)")

// ErrEmptyGoal is returned by RunGoal when the goal string is empty or
// whitespace-only. Callers may errors.Is on this sentinel to distinguish
// "caller sent empty goal" from LLM or executor failures.
// Stable.
var ErrEmptyGoal = errors.New("zenflow: goal must not be empty")

// Note: DropError is re-exported from internal/router via type alias in
// router_facade.go (this file kept the original definition before the
// internal/router extraction).

// ValidationError represents a YAML/schema validation error.
// Err is an optional wrapped cause; when set, errors.Is and errors.As
// see through to the underlying error. Existing call sites that
// construct ValidationError without an Err field continue to work
// unchanged - Unwrap returns nil and the wrapper still satisfies
// the error interface.
// Stable.
type ValidationError struct {
	Message string
	Err     error
}

func (e *ValidationError) Error() string {
	// - drop the redundant "error" word; matches the
	// `validation: <detail>` form used by *CoordinatorValidationError
	// so a consumer logging both at the same layer sees consistent
	// strings.
	return fmt.Sprintf("validation: %s", e.Message)
}

// Unwrap exposes the wrapped cause (if any) so errors.Is / errors.As
// see through ValidationError. Returns nil when no inner error was set.
func (e *ValidationError) Unwrap() error { return e.Err }

// CycleError indicates a cycle was detected in the workflow DAG.
// Err is an optional wrapped cause; when set, errors.Is and errors.As
// see through to the underlying error. Existing call sites that
// construct CycleError without an Err field continue to work unchanged.
// Stable.
type CycleError struct {
	Message string
	// Nodes lists the step IDs involved in the cycle (when available).
	Nodes []string
	Err   error
}

func (e *CycleError) Error() string {
	// (2026-05-04) - render Nodes when populated. ValidateWorkflow
	// fills the slice with the offending step IDs (`a → b → a`); a
	// developer that only Print-s the error never saw them before
	// (had to errors.As to *CycleError and inspect the field manually).
	if len(e.Nodes) > 0 {
		return fmt.Sprintf("cycle detected: %s (nodes: %s)", e.Message, strings.Join(e.Nodes, " → "))
	}
	return fmt.Sprintf("cycle detected: %s", e.Message)
}

// Unwrap exposes the wrapped cause (if any) so errors.Is / errors.As
// see through CycleError. Returns nil when no inner error was set.
func (e *CycleError) Unwrap() error { return e.Err }

// MissingAgentError indicates a step references an undefined agent.
// Err is an optional wrapped cause; existing call sites that omit
// Err continue to compile (Unwrap returns nil).
// Stable.
type MissingAgentError struct {
	Message string
	Agent   string
	StepID  string
	Err     error
}

func (e *MissingAgentError) Error() string {
	return fmt.Sprintf("missing agent: %s (step %q, agent %q)", e.Message, e.StepID, e.Agent)
}

// Unwrap exposes the wrapped cause (if any).
func (e *MissingAgentError) Unwrap() error { return e.Err }

// DuplicateStepError indicates duplicate step IDs in the workflow.
// Err is an optional wrapped cause; existing call sites that omit
// Err continue to compile (Unwrap returns nil).
// Stable.
type DuplicateStepError struct {
	Message string
	StepID  string
	Err     error
}

func (e *DuplicateStepError) Error() string {
	return fmt.Sprintf("duplicate step: %s (step %q)", e.Message, e.StepID)
}

// Unwrap exposes the wrapped cause (if any).
func (e *DuplicateStepError) Unwrap() error { return e.Err }

// MissingDepError indicates a step depends on a non-existent step.
// Err is an optional wrapped cause; existing call sites that omit
// Err continue to compile (Unwrap returns nil).
// Stable.
type MissingDepError struct {
	Message string
	Dep     string
	StepID  string
	Err     error
}

func (e *MissingDepError) Error() string {
	return fmt.Sprintf("missing dependency: %s (step %q, dep %q)", e.Message, e.StepID, e.Dep)
}

// Unwrap exposes the wrapped cause (if any).
func (e *MissingDepError) Unwrap() error { return e.Err }

// NoStepsError indicates the workflow has no steps defined.
// Err is an optional wrapped cause; existing call sites that omit
// Err continue to compile (Unwrap returns nil).
// Stable.
type NoStepsError struct {
	Message string
	Err     error
}

func (e *NoStepsError) Error() string {
	return fmt.Sprintf("no steps: %s", e.Message)
}

// Unwrap exposes the wrapped cause (if any).
func (e *NoStepsError) Unwrap() error { return e.Err }

// MissingNameError indicates the workflow has no name.
// Err is an optional wrapped cause; existing call sites that omit
// Err continue to compile (Unwrap returns nil).
// Stable.
type MissingNameError struct {
	Message string
	Err     error
}

func (e *MissingNameError) Error() string {
	return fmt.Sprintf("missing name: %s", e.Message)
}

// Unwrap exposes the wrapped cause (if any).
func (e *MissingNameError) Unwrap() error { return e.Err }

// IncludeConflictError indicates a step with include has conflicting fields.
// Err is an optional wrapped cause; existing call sites that omit
// Err continue to compile (Unwrap returns nil).
// Stable.
type IncludeConflictError struct {
	Message string
	StepID  string
	Field   string
	Err     error
}

func (e *IncludeConflictError) Error() string {
	return fmt.Sprintf("include conflict: %s (step %q, field %q)", e.Message, e.StepID, e.Field)
}

// Unwrap exposes the wrapped cause (if any).
func (e *IncludeConflictError) Unwrap() error { return e.Err }

// LoopValidationError indicates an invalid loop configuration.
// Err is an optional wrapped cause; existing call sites that omit
// Err continue to compile (Unwrap returns nil).
// Stable.
type LoopValidationError struct {
	Message string
	StepID  string
	Err     error
}

func (e *LoopValidationError) Error() string {
	return fmt.Sprintf("loop validation: %s (step %q)", e.Message, e.StepID)
}

// Unwrap exposes the wrapped cause (if any).
func (e *LoopValidationError) Unwrap() error { return e.Err }

// ErrRunNotFound is returned by Storage.LoadRun when the run does not exist.
// Stable.
var ErrRunNotFound = errors.New("zenflow: run not found")

// ErrStepNotFound is returned by Storage.LoadStepResult when the step has no persisted result.
// Stable.
var ErrStepNotFound = errors.New("zenflow: step result not found")

// ErrAgentTurnLimitExceeded is returned by AgentRunner.Run when an agent exhausts its turn limit without calling submit_result.
// Stable.
var ErrAgentTurnLimitExceeded = errors.New("zenflow: agent exhausted turn limit without submit_result")

// ErrAgentNoSubmitResult is returned by AgentRunner.Run when an agent finishes without calling submit_result despite having a resultSchema.
// Stable.
var ErrAgentNoSubmitResult = errors.New("zenflow: agent finished without calling submit_result")
