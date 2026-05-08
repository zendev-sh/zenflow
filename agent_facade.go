package zenflow

// agent_facade.go re-exports the AgentRunner ecosystem moved into
// internal/exec.

import (
	"github.com/zendev-sh/zenflow/internal/exec"
)

// AgentRunner is re-exported from internal/exec.
type AgentRunner = exec.AgentRunner

// AgentRunnerOption is re-exported from internal/exec.
// The canonical internal name is exec.RunnerOption (C14 stutter
// rename). The public facade keeps the AgentRunnerOption name so
// external SDK consumers don't see a breaking rename. exec keeps
// AgentRunnerOption as an alias of RunnerOption, so either form
// resolves to the same underlying type here.
type AgentRunnerOption = exec.RunnerOption

// AgentResult is re-exported from internal/exec.
type AgentResult = exec.AgentResult

// AgentStatus is re-exported from internal/exec.
type AgentStatus = exec.AgentStatus

// AgentStatus enum values re-exported from internal/exec.
const (
	AgentStatusCompleted = exec.AgentStatusCompleted
	AgentStatusTruncated = exec.AgentStatusTruncated
)

// SubmitResultHandler is re-exported from internal/exec.
type SubmitResultHandler = exec.SubmitResultHandler

// NewAgentRunner is re-exported from internal/exec.
var NewAgentRunner = exec.NewAgentRunner

// NewSubmitResultHandler is re-exported from internal/exec.
var NewSubmitResultHandler = exec.NewSubmitResultHandler

// SubmitResultToolDef is re-exported from internal/exec.
var SubmitResultToolDef = exec.SubmitResultToolDef

// AgentToolDef is re-exported from internal/exec.
var AgentToolDef = exec.AgentToolDef

// WithRunner* options re-exported from internal/exec.
var (
	WithRunnerModel               = exec.WithRunnerModel
	WithRunnerTools               = exec.WithRunnerTools
	WithRunnerPermissions         = exec.WithRunnerPermissions
	WithRunnerProgress            = exec.WithRunnerProgress
	WithRunnerGoAIOptions         = exec.WithRunnerGoAIOptions
	WithRunnerStreaming           = exec.WithRunnerStreaming
	WithRunnerVerbose             = exec.WithRunnerVerbose
	WithRunnerRunID               = exec.WithRunnerRunID
	WithRunnerStepID              = exec.WithRunnerStepID
	WithRunnerSystemPrompt        = exec.WithRunnerSystemPrompt
	WithRunnerModelID             = exec.WithRunnerModelID
	WithRunnerStateRef            = exec.WithRunnerStateRef
	WithRunnerMailbox             = exec.WithRunnerMailbox
	WithRunnerWake                = exec.WithRunnerWake
	WithRunnerRouter              = exec.WithRunnerRouter
	WithRunnerSpawnDepth          = exec.WithRunnerSpawnDepth
	WithRunnerSpawnParentCallID   = exec.WithRunnerSpawnParentCallID
	WithRunnerMaxWakeCycles       = exec.WithRunnerMaxWakeCycles
	WithRunnerTranscript          = exec.WithRunnerTranscript
	WithRunnerInitialMessages     = exec.WithRunnerInitialMessages
	WithRunnerPreStartDrainGate   = exec.WithRunnerPreStartDrainGate
	WithRunnerWakeContextProvider = exec.WithRunnerWakeContextProvider
)

// AgentHandle + lifecycle re-exports from internal/exec.
type AgentHandle = exec.AgentHandle

// NewAgentHandle is re-exported from internal/exec.
var NewAgentHandle = exec.NewAgentHandle

// SetRunAgentAsyncRunnerForTest is re-exported from internal/exec.
var SetRunAgentAsyncRunnerForTest = exec.SetRunAgentAsyncRunnerForTest

// AgentError is re-exported from internal/exec.
type AgentError = exec.AgentError

// AgentError sentinels re-exported from internal/exec.
var (
	ErrAgentHandleTimeout     = exec.ErrAgentHandleTimeout
	ErrAgentCancelled         = exec.ErrAgentCancelled
	ErrAgentPanicked          = exec.ErrAgentPanicked
	ErrAgentTurnLimitExceeded    = exec.ErrAgentTurnLimitExceeded
	ErrAgentNoSubmitResult       = exec.ErrAgentNoSubmitResult
	ErrInvalidAgentHandleID      = exec.ErrInvalidAgentHandleID
	ErrAgentToolDirectInvocation = exec.ErrAgentToolDirectInvocation
)

// DefaultAgentHandleTTL is re-exported from internal/exec.
const DefaultAgentHandleTTL = exec.DefaultAgentHandleTTL

// CoordRouterInboxID is re-exported from internal/exec.
const CoordRouterInboxID = exec.CoordRouterInboxID
