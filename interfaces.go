package zenflow

import (
	"github.com/zendev-sh/zenflow/internal/spec"
	"github.com/zendev-sh/zenflow/internal/types"
)

// interfaces.go re-exports the four workflow-contract interfaces (Storage /
// Tracer / StepIsolation / ApprovalHandler) defined in internal/spec, plus
// the seven observability + permission types defined in internal/types.
// External SDK consumers' import paths and Storage / Tracer / etc.
// implementations satisfy the aliased interfaces unchanged.

// Storage is re-exported from internal/spec.
type Storage = spec.Storage

// RunStore is re-exported from internal/spec. Narrow role: persist/load
// workflow Run records only.
type RunStore = spec.RunStore

// StepResultStore is re-exported from internal/spec. Narrow role:
// persist/load per-step results only.
type StepResultStore = spec.StepResultStore

// SharedMemoryStore is re-exported from internal/spec. Narrow role:
// persist/load the per-run shared key/value memory only.
type SharedMemoryStore = spec.SharedMemoryStore

// Tracer is re-exported from internal/spec.
type Tracer = spec.Tracer

// StepIsolation is re-exported from internal/spec.
type StepIsolation = spec.StepIsolation

// ApprovalHandler is re-exported from internal/spec.
type ApprovalHandler = spec.ApprovalHandler

// PermissionHandler is re-exported from internal/types.
type PermissionHandler = types.PermissionHandler

// PermissionRequest is re-exported from internal/types.
type PermissionRequest = types.PermissionRequest

// ProgressSink is re-exported from internal/types.
type ProgressSink = types.ProgressSink

// Output is re-exported from internal/types.
type Output = types.Output

// Event is re-exported from internal/types.
type Event = types.Event

// MessageKind is re-exported from internal/types.
type MessageKind = types.MessageKind

// EventType is re-exported from internal/types.
type EventType = types.EventType

// MessageKind constants re-exported from internal/types.
const (
	MessageKindNotification = types.MessageKindNotification
	MessageKindContent      = types.MessageKindContent
)

// EventType constants re-exported from internal/types.
const (
	EventWorkflowStart           = types.EventWorkflowStart
	EventWorkflowEnd             = types.EventWorkflowEnd
	EventStepStart               = types.EventStepStart
	EventStepEnd                 = types.EventStepEnd
	EventStepSkipped             = types.EventStepSkipped
	EventAgentTurn               = types.EventAgentTurn
	EventToolCall                = types.EventToolCall
	EventMessage                 = types.EventMessage
	EventError                   = types.EventError
	EventCoordinatorNarration    = types.EventCoordinatorNarration
	EventCoordinatorMessage      = types.EventCoordinatorMessage
	EventCoordinatorSynthesis    = types.EventCoordinatorSynthesis
	EventCoordinatorInboxMessage = types.EventCoordinatorInboxMessage
	EventMessageSent             = types.EventMessageSent
	EventPlanReady               = types.EventPlanReady
	EventAgentInboxDrain         = types.EventAgentInboxDrain
	EventMessageDropped          = types.EventMessageDropped
	EventAgentIdle               = types.EventAgentIdle
	EventAgentWake               = types.EventAgentWake
	EventMaxWakeCyclesWarning    = types.EventMaxWakeCyclesWarning
	EventResumeStarted           = types.EventResumeStarted
	EventResumeCompleted         = types.EventResumeCompleted
	EventResumeFailed            = types.EventResumeFailed
	EventResumeQueued            = types.EventResumeQueued
	EventTranscriptSealed        = types.EventTranscriptSealed
)
