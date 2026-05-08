package zenflow

// router_facade.go re-exports the symbols moved into
// internal/router so that external SDK consumers (,
// zenflow CLI, third parties) keep their existing
// `import "github.com/zendev-sh/zenflow"` working unchanged.
// Implementation lives in internal/router/. Edit there.

import (
	"github.com/zendev-sh/zenflow/internal/router"
)

// ----- router.go re-exports -----

// Resumer is re-exported from internal/router.
type Resumer = router.Resumer

// ResumeHandle is re-exported from internal/router.
type ResumeHandle = router.ResumeHandle

// RouterMessage is re-exported from internal/router.
type RouterMessage = router.Message

// RouterMessageType is re-exported from internal/router.
type RouterMessageType = router.MessageType

// MetadataKeyResumeReverse is re-exported from internal/router.
const MetadataKeyResumeReverse = router.MetadataKeyResumeReverse

// DropReason is re-exported from internal/router.
type DropReason = router.DropReason

// DropEvent is re-exported from internal/router.
type DropEvent = router.DropEvent

// MessageRouter is re-exported from internal/router.
type MessageRouter = router.Router

// DropError is re-exported from internal/router.
type DropError = router.DropError

// RouterMessageType enum re-exports.
const (
	RouterMessageInfo          = router.MessageInfo
	RouterMessageCancel        = router.MessageCancel
	RouterMessageContextUpdate = router.MessageContextUpdate
	RouterMessageResumeReply   = router.MessageResumeReply
)

// DropReason enum re-exports.
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

// Router error sentinels re-exported.
var (
	ErrResumeShutdown       = router.ErrResumeShutdown
	ErrModelResolverMissing = router.ErrModelResolverMissing
	ErrModelResolverError   = router.ErrModelResolverError
	ErrMailboxFullOnResume  = router.ErrMailboxFullOnResume
)

// NewMessageRouter is re-exported from internal/router.
var NewMessageRouter = router.NewRouter

// DropReasonStrings is re-exported from internal/router.
var DropReasonStrings = router.DropReasonStrings

// ----- mailbox.go re-exports -----

// MailboxStore is re-exported from internal/router.
type MailboxStore = router.MailboxStore

// LenAware is re-exported from internal/router.
type LenAware = router.LenAware

// ClosedAware is re-exported from internal/router.
type ClosedAware = router.ClosedAware

// InMemoryMailboxStore is re-exported from internal/router.
type InMemoryMailboxStore = router.InMemoryMailboxStore

// BoundedInMemoryStore is re-exported from internal/router.
type BoundedInMemoryStore = router.BoundedInMemoryStore

// ErrMailboxFull is re-exported from internal/router.
var ErrMailboxFull = router.ErrMailboxFull

// MailboxLen is re-exported from internal/router.
var MailboxLen = router.MailboxLen

// NewInMemoryMailboxStore is re-exported from internal/router.
var NewInMemoryMailboxStore = router.NewInMemoryMailboxStore

// NewBoundedInMemoryStore is re-exported from internal/router.
var NewBoundedInMemoryStore = router.NewBoundedInMemoryStore

// MessageIDs is re-exported from internal/router.
var MessageIDs = router.MessageIDs

// ----- delivery_engine.go re-exports -----

// EngineActiveStepsSource is re-exported from internal/router.
type EngineActiveStepsSource = router.EngineActiveStepsSource

// EngineWakeTarget is re-exported from internal/router.
type EngineWakeTarget = router.EngineWakeTarget

// EngineWakeRegistry is re-exported from internal/router.
type EngineWakeRegistry = router.EngineWakeRegistry

// ChanWakeTarget is re-exported from internal/router.
type ChanWakeTarget = router.ChanWakeTarget

// MapWakeRegistry is re-exported from internal/router.
type MapWakeRegistry = router.MapWakeRegistry

// EngineClock is re-exported from internal/router.
type EngineClock = router.EngineClock

// RealClock is re-exported from internal/router.
type RealClock = router.RealClock

// DeliveryEngine is re-exported from internal/router.
type DeliveryEngine = router.DeliveryEngine

// EngineOption is re-exported from internal/router.
type EngineOption = router.EngineOption

// EngineStepLocker is re-exported from internal/router.
// Renamed from the previously unexported `engineStepLocker` in zenflow
// root when the messaging substrate was extracted to internal/router
// (so out-of-package callers can satisfy it).
type EngineStepLocker = router.EngineStepLocker

// NewChanWakeTarget is re-exported from internal/router.
var NewChanWakeTarget = router.NewChanWakeTarget

// NewWakeRegistry is re-exported from internal/router.
var NewWakeRegistry = router.NewWakeRegistry

// WithStepLocker is re-exported from internal/router.
var WithStepLocker = router.WithStepLocker

// WithEngineTickInterval is re-exported from internal/router.
var WithEngineTickInterval = router.WithEngineTickInterval

// WithEngineClock is re-exported from internal/router.
var WithEngineClock = router.WithEngineClock

// NewDeliveryEngine is re-exported from internal/router.
var NewDeliveryEngine = router.NewDeliveryEngine
