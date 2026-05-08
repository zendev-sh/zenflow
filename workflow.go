package zenflow

// workflow.go is now a facade - every workflow specification type is
// defined in internal/spec; this file re-exports them via type alias so
// the public SDK surface (`Workflow`, `Step`, `Run`, etc.) keeps
// compiling for external consumers. Implementation lives in
// internal/spec/. Edit there.

import (
	"github.com/zendev-sh/zenflow/internal/spec"
)

// Workflow is re-exported from internal/spec.
type Workflow = spec.Workflow

// AgentConfig is re-exported from internal/spec.
type AgentConfig = spec.AgentConfig

// Step is re-exported from internal/spec.
type Step = spec.Step

// Loop is re-exported from internal/spec.
type Loop = spec.Loop

// WorkflowOptions is re-exported from internal/spec.
type WorkflowOptions = spec.WorkflowOptions

// OutputTransformer is re-exported from internal/spec.
type OutputTransformer = spec.OutputTransformer

// StepResult is re-exported from internal/spec.
type StepResult = spec.StepResult

// WorkflowResult is re-exported from internal/spec.
type WorkflowResult = spec.WorkflowResult

// Run is re-exported from internal/spec.
type Run = spec.Run

// WorkflowStatus is re-exported from internal/spec.
type WorkflowStatus = spec.WorkflowStatus

// StepStatus is re-exported from internal/spec.
type StepStatus = spec.StepStatus

// LoopOutputMode constants re-exported from internal/spec.
const (
	LoopOutputModeLast       = spec.LoopOutputModeLast
	LoopOutputModeCumulative = spec.LoopOutputModeCumulative
)

// FailureStrategy values re-exported from internal/spec.
const (
	FailureCascade        = spec.FailureCascade
	FailureSkipDependents = spec.FailureSkipDependents
	FailureAbort          = spec.FailureAbort
)

// SchedulerStrategy values re-exported from internal/spec.
const (
	SchedulerDependencyFirst = spec.SchedulerDependencyFirst
	SchedulerRoundRobin      = spec.SchedulerRoundRobin
	SchedulerLeastBusy       = spec.SchedulerLeastBusy
)

// WorkflowStatus values re-exported from internal/spec.
const (
	StatusRunning   = spec.StatusRunning
	StatusCompleted = spec.StatusCompleted
	StatusFailed    = spec.StatusFailed
	StatusPartial   = spec.StatusPartial
)

// StepStatus values re-exported from internal/spec.
const (
	StepCompleted = spec.StepCompleted
	StepFailed    = spec.StepFailed
	StepSkipped   = spec.StepSkipped
	StepCancelled = spec.StepCancelled
)
