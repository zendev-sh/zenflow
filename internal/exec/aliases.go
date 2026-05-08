// Package exec is the zenflow execution core. It contains the entire
// historical zenflow root implementation - Orchestrator, Executor,
// AgentRunner, RunFlow / RunGoal / RunAgent, Storage backends, the
// agent / coord factory plumbing, parsers, validators, schedulers,
// and prompt assembly. Carved out of package zenflow root so the public
// SDK surface (the facade files in zenflow/) sits cleanly above the
// implementation.
// Public types and constructors are re-exported via type alias from
// package zenflow's *_facade.go files; external SDK consumers'
// `import "github.com/zendev-sh/zenflow"` keeps working unchanged.
package exec

import (
	"time"

	"github.com/zendev-sh/zenflow/internal/coord"
	"github.com/zendev-sh/zenflow/internal/router"
	"github.com/zendev-sh/zenflow/internal/spec"
	"github.com/zendev-sh/zenflow/internal/types"
)

// aliases.go binds a residual set of moved-package symbols (router,
// spec, types, coord) back into the package exec namespace. These are
// pure type aliases - same underlying types, no boxing, no extra
// method-set indirection.
// De-aliasing status (Z.16+, C20): the bulk of the convenience aliases
// have been removed and call sites inside package exec now reference
// the canonical sub-package paths directly (router.X, spec.X,
// resume.X, types.X, coord.X). The aliases that REMAIN here are kept
// because at least one of the following holds:
// 1. Facade-required: CoordRouterInboxID is re-exported through
// zenflow.CoordRouterInboxID = exec.CoordRouterInboxID; removing
// the alias would force a corresponding facade rewrite.
// 2. Cluster-blocking: a handful of router/resume/spec aliases are
// still referenced by tests or production files inside this
// package whose mechanical de-aliasing collides with local
// identifiers - most notably a `router :=` parameter in
// step_termination.go and several `router *MessageRouter`
// fields/locals - so qualifying them to `router.X` would break
// compilation. The remaining aliases stay as compat shims until
// each cluster is revisited.
// Expect this file to keep shrinking, not grow.

// ----- internal/router type aliases -----

type (
	MessageRouter           = router.Router
	MailboxStore            = router.MailboxStore
	LenAware                = router.LenAware
	ClosedAware             = router.ClosedAware
	InMemoryMailboxStore    = router.InMemoryMailboxStore
	RouterMessage           = router.Message
	DropReason              = router.DropReason
	DropError               = router.DropError
	DropEvent               = router.DropEvent
	EngineActiveStepsSource = router.EngineActiveStepsSource
	EngineWakeRegistry      = router.EngineWakeRegistry
	EngineWakeTarget        = router.EngineWakeTarget
	EngineClock             = router.EngineClock
	RealClock               = router.RealClock
	ChanWakeTarget          = router.ChanWakeTarget
	DeliveryEngine          = router.DeliveryEngine
	Resumer                 = router.Resumer
	ResumeHandle            = router.ResumeHandle
)

// ----- internal/router constants -----

const MetadataKeyResumeReverse = router.MetadataKeyResumeReverse

const (
	RouterMessageInfo          = router.MessageInfo
	RouterMessageCancel        = router.MessageCancel
	RouterMessageContextUpdate = router.MessageContextUpdate
	RouterMessageResumeReply   = router.MessageResumeReply
)

const (
	DropReasonUnspecified             = router.DropReasonUnspecified
	DropReasonWorkflowCancelled       = router.DropReasonWorkflowCancelled
	DropReasonTargetTerminal          = router.DropReasonTargetTerminal
	DropReasonUnknownStep             = router.DropReasonUnknownStep
	DropReasonMailboxClosedByFinalize = router.DropReasonMailboxClosedByFinalize
	DropReasonMaxWakeCycles           = router.DropReasonMaxWakeCycles
	DropReasonHoldTimeout             = router.DropReasonHoldTimeout
	DropReasonMailboxFull             = router.DropReasonMailboxFull
	DropReasonNoTranscript            = router.DropReasonNoTranscript
	DropReasonTranscriptTooLarge      = router.DropReasonTranscriptTooLarge
	DropReasonResumeShutdown          = router.DropReasonResumeShutdown
	DropReasonResolverError           = router.DropReasonResolverError
)

// ----- internal/router error sentinels -----

var (
	ErrResumeShutdown       = router.ErrResumeShutdown
	ErrModelResolverMissing = router.ErrModelResolverMissing
	ErrModelResolverError   = router.ErrModelResolverError
	ErrMailboxFullOnResume  = router.ErrMailboxFullOnResume
	ErrMailboxFull          = router.ErrMailboxFull
)

// ----- internal/router function re-exports -----

func NewMessageRouter() *MessageRouter               { return router.NewRouter() }
func MessageIDs(msgs []RouterMessage) []string       { return router.MessageIDs(msgs) }
func NewInMemoryMailboxStore() *InMemoryMailboxStore { return router.NewInMemoryMailboxStore() }
func NewBoundedInMemoryStore(n int) *router.BoundedInMemoryStore {
	return router.NewBoundedInMemoryStore(n)
}
func MailboxLen(s MailboxStore, id string) (unread, total int)     { return router.MailboxLen(s, id) }
func NewChanWakeTarget(ch chan struct{}) EngineWakeTarget          { return router.NewChanWakeTarget(ch) }
func NewWakeRegistry() *router.MapWakeRegistry                     { return router.NewWakeRegistry() }
func WithStepLocker(l router.EngineStepLocker) router.EngineOption { return router.WithStepLocker(l) }
func WithEngineTickInterval(d time.Duration) router.EngineOption {
	return router.WithEngineTickInterval(d)
}
func WithEngineClock(c EngineClock) router.EngineOption { return router.WithEngineClock(c) }
func DropReasonStrings() map[router.DropReason]string   { return router.DropReasonStrings() }

// NewDeliveryEngine is re-exported from internal/router.
func NewDeliveryEngine(source EngineActiveStepsSource, mailbox MailboxStore, registry EngineWakeRegistry, opts ...router.EngineOption) *DeliveryEngine {
	return router.NewDeliveryEngine(source, mailbox, registry, opts...)
}

// ----- internal/types re-exports -----

type (
	PermissionHandler = types.PermissionHandler
	PermissionRequest = types.PermissionRequest
	ProgressSink      = types.ProgressSink
	Output            = types.Output
	Event             = types.Event
	EventType         = types.EventType
	MessageKind       = types.MessageKind
)

// ----- internal/spec re-exports -----

type (
	Storage           = spec.Storage
	Tracer            = spec.Tracer
	StepIsolation     = spec.StepIsolation
	ApprovalHandler   = spec.ApprovalHandler
	ModelResolver     = spec.ModelResolver
	Workflow          = spec.Workflow
	AgentConfig       = spec.AgentConfig
	Step              = spec.Step
	Loop              = spec.Loop
	WorkflowOptions   = spec.WorkflowOptions
	OutputTransformer = spec.OutputTransformer
	StepResult        = spec.StepResult
	WorkflowResult    = spec.WorkflowResult
	Run               = spec.Run
	WorkflowStatus    = spec.WorkflowStatus
	StepStatus        = spec.StepStatus
	Duration          = spec.Duration
)

// ----- internal/coord re-exports -----

// CoordRouterInboxID mirrors the canonical coord inbox key. Kept as a
// package-level constant because the public facade (zenflow root's
// agent_facade.go) re-exports `zenflow.CoordRouterInboxID = exec.CoordRouterInboxID`;
// removing this alias would force a facade rewrite. Same value as
// coord.CoordRouterInboxID.
const CoordRouterInboxID = coord.CoordRouterInboxID
