package spec

import (
	"context"

	"github.com/zendev-sh/goai/provider"
)

// interfaces.go holds the workflow-shaped interface contracts that the
// executor consumes (Storage / Tracer / StepIsolation / ApprovalHandler)
// plus the per-step ModelResolver type. They live alongside the
// Workflow / Step / Run definitions because their method signatures
// reference those types - moving them anywhere else (e.g. internal/types)
// would create a types → spec import cycle. Re-exported via type alias
// from package zenflow's interfaces.go so the public SDK surface is
// unchanged.

// RunStore persists workflow Run records (lifecycle metadata, status,
// step roll-ups). Narrow role interface; consumers that only need to
// load or save runs should depend on RunStore rather than the wider
// Storage. Stable.
type RunStore interface {
	SaveRun(ctx context.Context, run *Run) error
	LoadRun(ctx context.Context, id string) (*Run, error)
}

// StepResultStore persists per-step outputs (StepResult: status, output
// payload, error, timing). Narrow role interface; useful for components
// that read step outputs without needing run-level state. Stable.
type StepResultStore interface {
	SaveStepResult(ctx context.Context, runID, stepID string, result *StepResult) error
	LoadStepResult(ctx context.Context, runID, stepID string) (*StepResult, error)
}

// SharedMemoryStore persists the per-run shared key/value scratchpad
// that steps and the coordinator use to pass facts between agents.
// Narrow role interface kept separate from run/step persistence so
// alternate memory backends (e.g. Redis, vector store) can satisfy
// just this slice. Stable.
type SharedMemoryStore interface {
	SaveSharedMemory(ctx context.Context, runID string, entries map[string]string) error
	LoadSharedMemory(ctx context.Context, runID string) (map[string]string, error)
}

// Storage persists workflow run state. It is the composition of the
// three role interfaces (RunStore + StepResultStore + SharedMemoryStore)
// and remains the canonical contract every Executor backend implements.
// Existing implementations and external SDK consumers see no behavioural
// change - splitting is purely a structural refinement so callers can
// depend on the narrowest slice they need. Stable.
type Storage interface {
	RunStore
	StepResultStore
	SharedMemoryStore
}

// Tracer provides tracing hooks for workflow execution. Stable.
type Tracer interface {
	StartSpan(ctx context.Context, name string, attrs map[string]string) context.Context
	EndSpan(ctx context.Context, err error)
}

// StepIsolation provides per-step environment isolation. Stable.
type StepIsolation interface {
	Setup(ctx context.Context, runID, stepID string) (workDir string, err error)
	Cleanup(ctx context.Context, runID, stepID string) error
}

// ApprovalHandler gates workflow plan execution in RunGoal (optional). Stable.
type ApprovalHandler interface {
	ApprovePlan(ctx context.Context, plan *Workflow) (bool, error)
}

// ModelResolver resolves a saved transcript model identifier (as stored
// in StepTranscript.Model, e.g. "openai:gpt-4o-mini") to a concrete
// provider.LanguageModel. Executor.ResumeStep invokes it when the
// transcript's model differs from the runner's default. Returning nil
// model + nil error is treated as "not resolvable" - Executor fails the
// resume with ErrModelResolverMissing. Stable.
type ModelResolver func(modelID string) (provider.LanguageModel, error)
