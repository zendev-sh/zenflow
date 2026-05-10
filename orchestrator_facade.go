package zenflow

// orchestrator_facade.go re-exports the Orchestrator surface (Run* entry
// points, 44+ functional Options, FactoryCache, AgentHandle TTL config,
// Executor, error sentinels, error types, parsers, validators, schedulers,
// CEL evaluator, portability lints, isolation default, shared-memory tools).

import (
	"os"
	"path/filepath"

	"github.com/zendev-sh/zenflow/internal/exec"
	"github.com/zendev-sh/zenflow/internal/spec"
)

// DefaultStorageDir returns the default directory for FileStorage:
// $HOME/.zenflow/runs. When os.UserHomeDir fails (no HOME, etc.) it
// falls back to <os.TempDir>/zenflow/runs so the path is always usable.
// CLI consumers and embedders that want the standard zenflow storage
// location should call this and pass the result to NewFileStorage:
//
//	storage := zenflow.NewFileStorage(zenflow.DefaultStorageDir)
//	orch := zenflow.New(zenflow.WithStorage(storage))
//
// Stable.
func DefaultStorageDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "zenflow", "runs")
	}
	return filepath.Join(home, ".zenflow", "runs")
}

// ----- Orchestrator + Run* + Options -----

// Orchestrator is re-exported from internal/exec.
type Orchestrator = exec.Orchestrator

// Option is re-exported from internal/exec.
type Option = exec.Option

// RunFlowOption is re-exported from internal/exec.
type RunFlowOption = exec.RunFlowOption

// RunGoalOption is re-exported from internal/exec.
type RunGoalOption = exec.RunGoalOption

// New is re-exported from internal/exec.
var New = exec.New

// All Orchestrator With* options re-exported from internal/exec.
var (
	WithModel                     = exec.WithModel
	WithTools                     = exec.WithTools
	WithGoAIOptions               = exec.WithGoAIOptions
	WithStorage                   = exec.WithStorage
	WithPermissions               = exec.WithPermissions
	WithProgress                  = exec.WithProgress
	WithDefaultModel              = exec.WithDefaultModel
	WithForceModel                = exec.WithForceModel
	WithMaxConcurrency            = exec.WithMaxConcurrency
	WithMaxTurns                  = exec.WithMaxTurns
	WithMaxDepth                  = exec.WithMaxDepth
	WithApproval                  = exec.WithApproval
	WithApprovalTimeout           = exec.WithApprovalTimeout
	WithSharedMemory              = exec.WithSharedMemory
	WithTracer                    = exec.WithTracer
	WithCoordinator               = exec.WithCoordinator
	WithIsolation                 = exec.WithIsolation
	WithOutputTransform           = exec.WithOutputTransform
	WithStreaming                 = exec.WithStreaming
	WithoutStreaming              = exec.WithoutStreaming
	WithVerbose                   = exec.WithVerbose
	WithoutVerbose                = exec.WithoutVerbose
	WithMaxWakeCycles             = exec.WithMaxWakeCycles
	WithHoldTimeout               = exec.WithHoldTimeout
	WithAgentHandleTTL            = exec.WithAgentHandleTTL
	WithDropCallback              = exec.WithDropCallback
	WithDropCallbackBufferSize    = exec.WithDropCallbackBufferSize
	WithMaxMailboxSize            = exec.WithMaxMailboxSize
	WithMailboxStore              = exec.WithMailboxStore
	WithMailboxDelivery           = exec.WithMailboxDelivery
	WithoutMailboxDelivery        = exec.WithoutMailboxDelivery
	WithProgressBufferSize        = exec.WithProgressBufferSize
	WithTranscriptStore           = exec.WithTranscriptStore
	WithMaxTranscriptMessages     = exec.WithMaxTranscriptMessages
	WithMaxTranscriptBytes        = exec.WithMaxTranscriptBytes
	WithExternalInbox             = exec.WithExternalInbox
	WithModelResolver             = exec.WithModelResolver
	WithTruncationOnCapReached    = exec.WithTruncationOnCapReached
	WithoutTruncationOnCapReached = exec.WithoutTruncationOnCapReached
	WithRouterObserver            = exec.WithRouterObserver
	WithRunID                     = exec.WithRunID
	WithFlowContext               = exec.WithFlowContext
	WithGoalContext               = exec.WithGoalContext
)

// ErrOrchestratorClosed is re-exported from internal/exec.
var ErrOrchestratorClosed = exec.ErrOrchestratorClosed

// ----- ModelResolver -----

// ModelResolver is re-exported from internal/spec (the canonical
// definition); internal/exec.ModelResolver is itself a type alias
// to spec.ModelResolver, so callers can mix the two interchangeably.
type ModelResolver = spec.ModelResolver

// ----- Executor -----

// Executor is re-exported from internal/exec.
type Executor = exec.Executor

// EvalContext is re-exported from internal/exec.
type EvalContext = exec.EvalContext

// EvalStepContext is re-exported from internal/exec.
type EvalStepContext = exec.EvalStepContext

// EvaluateCEL is re-exported from internal/exec.
var EvaluateCEL = exec.EvaluateCEL

// EvaluateCELToArray is re-exported from internal/exec.
var EvaluateCELToArray = exec.EvaluateCELToArray

// BuildEvalContext is re-exported from internal/exec.
var BuildEvalContext = exec.BuildEvalContext

// AssemblePrompt is re-exported from internal/exec. Builds the user
// prompt from agent + step + baseDir + prior step results. Useful for
// SDK consumers that want to dry-run or inspect the assembled prompt
// without invoking the executor.
var AssemblePrompt = exec.AssemblePrompt

// AssemblePromptWithForEach is re-exported from internal/exec. Same as
// AssemblePrompt but accepts an optional *ForEachContext for forEach
// loop iterations.
var AssemblePromptWithForEach = exec.AssemblePromptWithForEach

// ----- Coord factory + lib -----

// DefaultCoordSystemPrompt is re-exported from internal/exec.
const DefaultCoordSystemPrompt = exec.DefaultCoordSystemPrompt

// DefaultCoordColdStartPrompt is re-exported from internal/exec.
const DefaultCoordColdStartPrompt = exec.DefaultCoordColdStartPrompt

// DefaultCoordContinuationPrompt is re-exported from internal/exec.
const DefaultCoordContinuationPrompt = exec.DefaultCoordContinuationPrompt

// CoordOption is re-exported from internal/exec.
type CoordOption = exec.CoordOption

// NewDefaultCoordRunner is re-exported from internal/exec.
var NewDefaultCoordRunner = exec.NewDefaultCoordRunner

// SynthesizeOnly is re-exported from internal/exec.
var SynthesizeOnly = exec.SynthesizeOnly

// WithCoordTools is re-exported from internal/exec.
var WithCoordTools = exec.WithCoordTools

// WithCoordMaxWakeCycles is re-exported from internal/exec.
var WithCoordMaxWakeCycles = exec.WithCoordMaxWakeCycles

// WithCoordSystemPrompt is re-exported from internal/exec.
var WithCoordSystemPrompt = exec.WithCoordSystemPrompt

// WithCoordSystemPromptSuffix is re-exported from internal/exec.
var WithCoordSystemPromptSuffix = exec.WithCoordSystemPromptSuffix

// WithCoordContextProvider is re-exported from internal/exec.
var WithCoordContextProvider = exec.WithCoordContextProvider

// BuildCoordStepMenu is re-exported from internal/exec.
var BuildCoordStepMenu = exec.BuildCoordStepMenu

// WaitForCoordWake is re-exported from internal/exec.
var WaitForCoordWake = exec.WaitForCoordWake

// ----- Coordinator (JSON parser) -----

// CoordinatorChat is re-exported from internal/exec.
var CoordinatorChat = exec.CoordinatorChat

// CoordinatorStreamChat is re-exported from internal/exec.
var CoordinatorStreamChat = exec.CoordinatorStreamChat

// CoordinatorPrompt is re-exported from internal/exec.
var CoordinatorPrompt = exec.CoordinatorPrompt

// BuildToolCatalog is re-exported from internal/exec.
var BuildToolCatalog = exec.BuildToolCatalog

// ParseCoordinatorResponse is re-exported from internal/exec.
var ParseCoordinatorResponse = exec.ParseCoordinatorResponse

// ValidateToolNames is re-exported from internal/exec.
var ValidateToolNames = exec.ValidateToolNames

// JSONParseError is re-exported from internal/exec.
type JSONParseError = exec.JSONParseError

// CoordinatorValidationError is re-exported from internal/exec.
type CoordinatorValidationError = exec.CoordinatorValidationError

// ToolNotFoundError is re-exported from internal/exec.
type ToolNotFoundError = exec.ToolNotFoundError

// ----- Parsers + Validators -----

// LoadWorkflow is re-exported from internal/exec.
var LoadWorkflow = exec.LoadWorkflow

// ParseWorkflow is re-exported from internal/exec.
var ParseWorkflow = exec.ParseWorkflow

// ParseWorkflowJSON is re-exported from internal/exec.
var ParseWorkflowJSON = exec.ParseWorkflowJSON

// SanitizeWorkflowUnicode is re-exported from internal/exec.
var SanitizeWorkflowUnicode = exec.SanitizeWorkflowUnicode

// ApplyDefaults is re-exported from internal/exec.
var ApplyDefaults = exec.ApplyDefaults

// ValidateWorkflow is re-exported from internal/exec.
var ValidateWorkflow = exec.ValidateWorkflow

// ----- Tools -----

// FilterTools is re-exported from internal/exec.
var FilterTools = exec.FilterTools

// ----- Storage backends -----

// MemoryStorage is re-exported from internal/exec.
type MemoryStorage = exec.MemoryStorage

// FileStorage is re-exported from internal/exec.
type FileStorage = exec.FileStorage

// NewMemoryStorage is re-exported from internal/exec.
var NewMemoryStorage = exec.NewMemoryStorage

// NewFileStorage is re-exported from internal/exec.
var NewFileStorage = exec.NewFileStorage

// ----- Shared memory -----

// SharedMemory is re-exported from internal/exec.
type SharedMemory = exec.SharedMemory

// NewSharedMemory is re-exported from internal/exec.
var NewSharedMemory = exec.NewSharedMemory

// NewSharedMemoryTools is re-exported from internal/exec.
var NewSharedMemoryTools = exec.NewSharedMemoryTools

// ----- Output transform -----

// TokenBudgetTransformer is re-exported from internal/exec.
type TokenBudgetTransformer = exec.TokenBudgetTransformer

// DefaultMaxBytesPerDep is re-exported from internal/exec. It is the
// per-dependency byte cap applied by the orchestrator's default
// OutputTransform when WithOutputTransform is not provided.
const DefaultMaxBytesPerDep = exec.DefaultMaxBytesPerDep

// DefaultMaxMailboxSize is re-exported from internal/exec. It is the
// per-step mailbox cap installed by New when WithMaxMailboxSize is
// not provided. Pass WithMaxMailboxSize(0) to opt out of the cap.
const DefaultMaxMailboxSize = exec.DefaultMaxMailboxSize

// DefaultCoordCleanupTimeout is re-exported from internal/exec. It
// bounds the cleanup phase of RunCoordinatorLoop's returned func.
const DefaultCoordCleanupTimeout = exec.DefaultCoordCleanupTimeout

// CoordLoopOption is re-exported from internal/exec.
type CoordLoopOption = exec.CoordLoopOption

// RunCoordinatorLoop is re-exported from internal/exec.
var RunCoordinatorLoop = exec.RunCoordinatorLoop

// WithCleanupTimeout is re-exported from internal/exec.
var WithCleanupTimeout = exec.WithCleanupTimeout

// ----- Isolation -----

// NopIsolation is re-exported from internal/exec.
type NopIsolation = exec.NopIsolation

// ----- Portability lints -----

// PortabilityWarning is re-exported from internal/exec.
type PortabilityWarning = exec.PortabilityWarning

// HostSpecificEnvError is re-exported from internal/exec.
type HostSpecificEnvError = exec.HostSpecificEnvError

// UnicodeUnsafeError is re-exported from internal/exec.
type UnicodeUnsafeError = exec.UnicodeUnsafeError

// LintPortability is re-exported from internal/exec.
var LintPortability = exec.LintPortability

// SanitizeUnicode is re-exported from internal/exec.
var SanitizeUnicode = exec.SanitizeUnicode

// DetectMixedScript is re-exported from internal/exec.
var DetectMixedScript = exec.DetectMixedScript

// ----- DAG render -----
// DAG rendering is a CLI-only concern and lives in
// `cmd/zenflow/dag` (package `dag`). Library consumers should not
// depend on the renderer; CLI binaries import it directly.

// TopoSort is re-exported from internal/exec.
var TopoSort = exec.TopoSort

// ----- FactoryCache -----

// FactoryCache is re-exported from internal/exec.
type FactoryCache = exec.FactoryCache

// NewFactoryCache is re-exported from internal/exec.
var NewFactoryCache = exec.NewFactoryCache

// ErrNilFactoryInner is re-exported from internal/exec.
var ErrNilFactoryInner = exec.ErrNilFactoryInner

// ----- Validation errors -----

// ValidationError is re-exported from internal/exec.
type ValidationError = exec.ValidationError

// CycleError is re-exported from internal/exec.
type CycleError = exec.CycleError

// MissingAgentError is re-exported from internal/exec.
type MissingAgentError = exec.MissingAgentError

// DuplicateStepError is re-exported from internal/exec.
type DuplicateStepError = exec.DuplicateStepError

// MissingDepError is re-exported from internal/exec.
type MissingDepError = exec.MissingDepError

// NoStepsError is re-exported from internal/exec.
type NoStepsError = exec.NoStepsError

// MissingNameError is re-exported from internal/exec.
type MissingNameError = exec.MissingNameError

// IncludeConflictError is re-exported from internal/exec.
type IncludeConflictError = exec.IncludeConflictError

// LoopValidationError is re-exported from internal/exec.
type LoopValidationError = exec.LoopValidationError

// ----- Error sentinels -----

var (
	ErrApprovalTimeout      = exec.ErrApprovalTimeout
	ErrModelRequired        = exec.ErrModelRequired
	ErrStorageRequired      = exec.ErrStorageRequired
	ErrNilAgentHandle       = exec.ErrNilAgentHandle
	ErrNilOrchestrator      = exec.ErrNilOrchestrator
	ErrResumeNoModel        = exec.ErrResumeNoModel
	ErrWorkflowNil          = exec.ErrWorkflowNil
	ErrPlanDenied           = exec.ErrPlanDenied
	ErrRunnerNil            = exec.ErrRunnerNil
	ErrEmptyGoal            = exec.ErrEmptyGoal
	ErrRunNotFound          = exec.ErrRunNotFound
	ErrStepNotFound         = exec.ErrStepNotFound
	ErrIncludePathEscape    = exec.ErrIncludePathEscape
	ErrIncludeDepthExceeded = exec.ErrIncludeDepthExceeded
	ErrRefPathEscape        = exec.ErrRefPathEscape
)

// ForEachContext is re-exported from internal/exec.
type ForEachContext = exec.ForEachContext
