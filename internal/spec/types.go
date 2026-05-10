// Package spec holds the workflow specification types - the schema-shaped
// data the YAML parser, validator, and executor share. This is a leaf
// package: it depends only on goai/provider for token-usage values and
// internal/types for ProgressSink, with no edge back to package zenflow
// root. internal/coord, internal/agent, internal/exec can all import it
// without forming cycles. Every public type is re-exported via type
// alias from package zenflow's workflow_facade.go so the SDK surface
// is unchanged.
package spec

import (
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/types"
)

// Workflow defines a DAG of steps executed by named agents. Stable.
type Workflow struct {
	// Name is the workflow identifier - REQUIRED. ValidateWorkflow returns
	// *MissingNameError ("workflow name is required") when empty. Used
	// by trace spans, log entries, and the human-readable banner.
	Name string `json:"name" yaml:"name"`
	// Description is optional human prose for the workflow.
	Description string                 `json:"description,omitempty" yaml:"description,omitempty"`
	Version     int                    `json:"version,omitempty" yaml:"version,omitempty"`
	Agents      map[string]AgentConfig `json:"agents,omitempty" yaml:"agents,omitempty"`
	Includes    map[string]string      `json:"includes,omitempty" yaml:"includes,omitempty"`
	// Steps is the ordered list of work units - REQUIRED, must be
	// non-empty. ValidateWorkflow returns *NoStepsError when zero-length.
	Steps   []Step          `json:"steps" yaml:"steps"`
	Options WorkflowOptions `json:"options,omitempty" yaml:"options,omitempty"`
	// BaseDir is the directory containing the workflow file, used for resolving
	// relative paths in contextFiles. Set by LoadWorkflow; empty for programmatic use.
	BaseDir string `json:"-" yaml:"-"`
}

// AgentConfig defines a named agent's role, model, and tool access. Stable.
// The YAML/JSON-serialized fields (Description, Prompt, Model, Tools,
// DisallowedTools, MaxTurns, Temperature, TopP, ResultSchema) describe
// a workflow-declared agent - parsed from workflow YAML by
// LoadWorkflow / ParseWorkflow. Here `Tools []string` lists tool
// NAMES (catalog lookups) because workflow YAML can only carry
// strings; the Orchestrator resolves names against its registered
// tool catalog via FilterTools.
// The non-serialized fields (Name, CallTools, ProgressSink,
// SubagentToolSet, SessionID) are per-call fields used by invocations
// through Orchestrator.RunAgent / Orchestrator.RunAgentAsync. Consumers
// populate these at call time to override the Orchestrator-level
// defaults (supplied via WithTools / WithProgress) for a single
// invocation. They are never read from workflow YAML and carry zero
// values after parsing.
type AgentConfig struct {
	// --- YAML/JSON-declared fields (workflow-file agent) ---

	// Description is the agent's role summary - REQUIRED for
	// workflow-declared agents (parsed from `agents:` block in YAML).
	// ValidateWorkflow returns *ValidationError ("agent X missing required
	// field 'description'") when empty. Programmatic per-call
	// invocations through Orchestrator.RunAgent / RunAgentAsync may
	// leave it empty since they go through the AgentRunner.Run path
	// which doesn't consult Description.
	Description     string         `json:"description,omitempty" yaml:"description,omitempty"`
	Prompt          string         `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	Model           string         `json:"model,omitempty" yaml:"model,omitempty"`
	Tools           []string       `json:"tools,omitempty" yaml:"tools,omitempty"`
	DisallowedTools []string       `json:"disallowedTools,omitempty" yaml:"disallowedTools,omitempty"`
	MaxTurns        int            `json:"maxTurns,omitempty" yaml:"maxTurns,omitempty"`
	Temperature     *float64       `json:"temperature,omitempty" yaml:"temperature,omitempty"`
	TopP            *float64       `json:"topP,omitempty" yaml:"topP,omitempty"`
	ResultSchema    map[string]any `json:"resultSchema,omitempty" yaml:"resultSchema,omitempty"`

	// --- Per-call fields, never serialized ---

	// Name is an optional human-readable identifier for the agent
	// (e.g. "research", "reviewer"). Advisory only - used by
	// observability paths, not by the tool loop.
	Name string `json:"-" yaml:"-"`

	// CallTools, when non-empty, overrides the Orchestrator-level
	// tool set for this single call. Carries the resolved
	// []goai.Tool values (with Execute closures), not string names -
	// consumers build it from a restricted subset of their tool
	// registry. A typical consumer's task tool sets this to a
	// restricted subagent tool set so a subagent receives a read-only
	// tool slice while the primary agent retains its full toolbox.
	// When nil, RunAgent / RunAgentAsync falls back to the tools
	// registered via WithTools.
	CallTools []goai.Tool `json:"-" yaml:"-"`

	// ProgressSink, when non-nil, overrides the Orchestrator-level
	// sink for this single call. Consumers wire a per-call progress
	// bridge here so subagent events land in the primary agent's
	// inbox instead of the session's global sink.
	ProgressSink types.ProgressSink `json:"-" yaml:"-"`

	// SubagentToolSet is a logical label for the restricted tool
	// set associated with this call (e.g. "read-only", "full").
	// Purely advisory - used for logging and policy reporting. Does
	// not itself modify the tool slice; CallTools carries the
	// actual tool values.
	SubagentToolSet string `json:"-" yaml:"-"`

	// Attachments carries multimodal `provider.Part` values (text
	// snippets, image references, PDF byte payloads, etc.) the agent
	// loop concatenates onto the user message at the start of the
	// run. Forwarded verbatim to `AgentRunner.Run`'s variadic
	// attachments slot. nil / empty → text-only conversation. Used
	// by chat consumers passing files alongside the prompt and by
	// e2e tests verifying the multimodal pathway end-to-end.
	Attachments []provider.Part `json:"-" yaml:"-"`

	// SessionID identifies the consumer session that owns this call
	// (e.g. the consumer's chat session). The Orchestrator's in-memory
	// handle registry partitions active RunAgentAsync handles by
	// this key so ListAgentHandles can return only the handles
	// belonging to one session. Empty string is a valid
	// single-session key ("" bucket); consumers must populate it
	// for multi-session deployments.
	SessionID string `json:"-" yaml:"-"`
}

// Step is a single node in the workflow DAG. Stable.
type Step struct {
	ID           string   `json:"id" yaml:"id"`
	Agent        string   `json:"agent,omitempty" yaml:"agent,omitempty"`
	Instructions string   `json:"instructions,omitempty" yaml:"instructions,omitempty"`
	DependsOn    []string `json:"dependsOn,omitempty" yaml:"dependsOn,omitempty"`
	ContextFiles []string `json:"contextFiles,omitempty" yaml:"contextFiles,omitempty"`
	Model        string   `json:"model,omitempty" yaml:"model,omitempty"`
	Timeout      Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Retries      int      `json:"retries,omitempty" yaml:"retries,omitempty"`
	MaxRetries   *int     `json:"maxRetries,omitempty" yaml:"maxRetries,omitempty"`
	Condition    *string  `json:"condition,omitempty" yaml:"condition,omitempty"`
	Include      string   `json:"include,omitempty" yaml:"include,omitempty"`
	Loop         *Loop    `json:"loop,omitempty" yaml:"loop,omitempty"`
}

// Loop defines iteration behavior for a step. Stable.
type Loop struct {
	MaxIterations  *int     `json:"maxIterations,omitempty" yaml:"maxIterations,omitempty"`
	Until          *string  `json:"until,omitempty" yaml:"until,omitempty"`
	UntilAgent     string   `json:"untilAgent,omitempty" yaml:"untilAgent,omitempty"`
	ForEach        any      `json:"forEach,omitempty" yaml:"forEach,omitempty"`
	MaxConcurrency int      `json:"maxConcurrency,omitempty" yaml:"maxConcurrency,omitempty"`
	Delay          Duration `json:"delay,omitempty" yaml:"delay,omitempty"`
	Steps          []Step   `json:"steps,omitempty" yaml:"steps,omitempty"`
	// OutputMode controls how the loop step's returned StepResult.Content is
	// constructed for dependent steps to consume.
	// "" or "last" - (default, backward compat) Content = the LAST inner
	// step of the FINAL iteration only. Right for refine-
	// style loops where each iteration supersedes the prior
	// and downstream consumers want the polished output.
	// "cumulative" - Content = full iteration history (every iteration's
	// inner-step output concatenated, plus judge feedback
	// for untilAgent loops). Right for aggregator-style
	// loops where downstream consumers (e.g. a verdict
	// summarizer) need to see ALL rounds, not just the
	// final one.
	OutputMode LoopOutputMode `json:"outputMode,omitempty" yaml:"outputMode,omitempty"`
}

// LoopOutputMode controls how a loop step aggregates per-iteration content.
type LoopOutputMode string

// LoopOutputMode constants for Loop.OutputMode validation.
const (
	LoopOutputModeLast       LoopOutputMode = "last"
	LoopOutputModeCumulative LoopOutputMode = "cumulative"
)

// WorkflowOptions controls workflow execution behavior. Stable.
type WorkflowOptions struct {
	MaxConcurrency int               `json:"maxConcurrency,omitempty" yaml:"maxConcurrency,omitempty"`
	MaxRetries     *int              `json:"maxRetries,omitempty" yaml:"maxRetries,omitempty"`
	OnStepFailure  FailureStrategy   `json:"onStepFailure,omitempty" yaml:"onStepFailure,omitempty"`
	Timeout        Duration          `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	StepTimeout    Duration          `json:"stepTimeout,omitempty" yaml:"stepTimeout,omitempty"`
	Isolation      string            `json:"isolation,omitempty" yaml:"isolation,omitempty"`
	Scheduler      SchedulerStrategy `json:"scheduler,omitempty" yaml:"scheduler,omitempty"`
}

// FailureStrategy controls executor behavior when a step fails.
type FailureStrategy string

// FailureStrategy values for WorkflowOptions.OnStepFailure.
const (
	FailureCascade        FailureStrategy = "cascade"
	FailureSkipDependents FailureStrategy = "skip-dependents"
	FailureAbort          FailureStrategy = "abort"
)

// SchedulerStrategy controls how ready steps are dispatched.
type SchedulerStrategy string

// SchedulerStrategy values for WorkflowOptions.Scheduler.
const (
	SchedulerDependencyFirst SchedulerStrategy = "dependency-first"
	SchedulerRoundRobin      SchedulerStrategy = "round-robin"
	SchedulerLeastBusy       SchedulerStrategy = "least-busy"
)

// OutputTransformer transforms step output before injection into dependent step prompts.
// Consumers implement this to control truncation/compaction based on the target model's
// context window. The default behavior (no transformer) uses fixed-size byte truncation.
type OutputTransformer interface {
	// TransformStepOutput transforms a completed step's content and result before
	// they are injected into a dependent step's prompt. stepID identifies the source
	// step, targetModel is the model that will consume the output.
	// Return the (possibly shortened) content and result.
	TransformStepOutput(stepID string, content string, result map[string]any, targetModel string) (string, map[string]any)
}

// Duration wraps time.Duration with YAML/JSON string parsing. Stable.
type Duration time.Duration

// StepResult holds the outcome of a single step execution.
// # Field-population contract (v0.1.0)
// StepResult is documented Stable. Fields will not be removed or have
// their types changed within the v0.x line.
// Per-Status field population:
//
//	StepCompleted : ID, Status, Content (agent final text), Result
//
// (structured output from submit_result, may be nil),
// Tokens (accumulated across retries), Duration.
// Error is nil. PreserveContent may be true for loop
// steps in outputMode=cumulative.
//
//	StepFailed : ID, Status, Error. Tokens reflects attempts made
//
// before failure. Duration is set when the step ran
// at least one agent iteration before failing.
// Content and Result are zero.
//
//	StepSkipped : ID, Status. All other fields are zero.
//
// Populated by the DAG planner for steps whose
// prerequisites failed with onStepFailure=skip-dependents.
//
//	StepCancelled : ID, Status. All other fields are zero.
//
// Populated by the DAG planner for steps cancelled by
// the cascade or abort failure strategies.
// Stable.
type StepResult struct {
	ID       string
	Status   StepStatus
	Content  string
	Result   map[string]any
	Tokens   provider.Usage
	Duration time.Duration
	Error    error
	// PreserveContent, when true, signals to AssemblePrompt that this
	// result's Content was intentionally aggregated (e.g. by a loop step
	// in outputMode=cumulative) and must NOT be subject to the per-dep
	// 16KB truncation cap (maxDepContentBytes). The overall maxPromptBytes
	// cap (120KB) still applies as the final safety net. Default false
	// preserves the standard truncation behavior for ordinary step outputs.
	PreserveContent bool
}

// WorkflowResult holds the outcome of an entire workflow execution.
// Returned by RunFlow, ResumeFlow, and RunGoal.
// # Field-population contract (v0.1.0)
// WorkflowResult is documented Stable. Fields will not be removed or
// have their types changed within the v0.x line.
// All fields are always populated on a non-nil return:
//
//	RunID : unique identifier for the execution; matches RunID in all
//
// Event values emitted during the run.
//
//	Status : terminal workflow status (StatusCompleted, StatusFailed,
//
// StatusPartial). StatusRunning is never returned.
//
//	Steps : map of stepID to *StepResult for every step that was
//
// dispatched. Steps that were never dispatched (e.g.
// skipped because a prerequisite failed before scheduling)
// are absent from the map. Do not mutate the pointed-to
// StepResult values; the behavior is undefined.
//
//	Duration : wall-clock time from Run entry to Run exit.
//	Tokens : aggregate token usage summed across all step agents.
//
// Individual step token counts are in Steps[id].Tokens.
//
//	Summary : human-readable synthesis from the coordinator's finalize
//
// tool. Empty when no coordinator is configured or the
// coordinator did not call finalize.
// Steps is exposed for backward compatibility; consumers should prefer
// the Result(stepID) accessor for forward-compatible lookups. Mutating
// returned StepResult pointers from Steps is undefined behavior.
// Stable.
type WorkflowResult struct {
	RunID  string
	Status WorkflowStatus
	// Steps maps step ID to its result pointer. Held for backward
	// compatibility - new code should use Result(stepID) to obtain a
	// value copy and avoid pointer escape / representation coupling.
	Steps    map[string]*StepResult
	Duration time.Duration
	Tokens   provider.Usage
	// Summary is a human-readable summary produced by the coordinator
	// runner's finalize tool. Empty when no coord is installed or
	// the coord did not call finalize.
	Summary string
}

// Result returns a value copy of the StepResult for stepID.
// The second return value is false when stepID is not present or the
// stored pointer is nil. Using this accessor insulates callers from
// future representation changes to WorkflowResult.Steps and prevents
// pointer escape (callers cannot mutate the internal StepResult).
// Stable.
func (r *WorkflowResult) Result(stepID string) (StepResult, bool) {
	if r == nil {
		return StepResult{}, false
	}
	sr, ok := r.Steps[stepID]
	if !ok || sr == nil {
		return StepResult{}, false
	}
	return *sr, true
}

// FinalAnswer returns the workflow's final output text in priority order:
// (1) the coord-finalized Summary, (2) the LAST topological step's Content
// (terminal node - typically the summarizer/verdict). Returns "" when
// neither is available.
// A "terminal" step is one not pointed to by any other step's DependsOn.
// When multiple terminal steps exist, the LAST one in declaration order
// wins. Steps without StepCompleted status or with empty Content are
// skipped.
// Stable.
func (r *WorkflowResult) FinalAnswer(wf *Workflow) string {
	if r == nil {
		return ""
	}
	if r.Summary != "" {
		return r.Summary
	}
	if wf == nil || len(wf.Steps) == 0 || r.Steps == nil {
		return ""
	}
	depended := make(map[string]struct{}, len(wf.Steps))
	for _, s := range wf.Steps {
		for _, d := range s.DependsOn {
			depended[d] = struct{}{}
		}
	}
	var lastTerminal string
	for _, s := range wf.Steps {
		if _, isDep := depended[s.ID]; isDep {
			continue
		}
		if sr, ok := r.Steps[s.ID]; ok && sr != nil && sr.Status == StepCompleted && sr.Content != "" {
			lastTerminal = sr.Content
		}
	}
	return lastTerminal
}

// Run represents a single workflow execution instance.
type Run struct {
	ID       string
	Workflow *Workflow
	Status   WorkflowStatus
	Steps    map[string]*StepResult
}

// WorkflowStatus describes the terminal state of an entire workflow execution.
type WorkflowStatus string

const (
	// StatusRunning indicates the workflow is currently executing.
	StatusRunning WorkflowStatus = "running"
	// StatusCompleted indicates all steps finished successfully.
	StatusCompleted WorkflowStatus = "completed"
	// StatusFailed indicates the workflow failed with no completed steps.
	StatusFailed WorkflowStatus = "failed"
	// StatusPartial indicates some steps completed but at least one failed.
	StatusPartial WorkflowStatus = "partial"
)

// StepStatus describes the terminal state of a single step execution.
type StepStatus string

const (
	// StepCompleted indicates the step finished successfully.
	StepCompleted StepStatus = "completed"
	// StepFailed indicates the step encountered an error.
	StepFailed StepStatus = "failed"
	// StepSkipped indicates the step was skipped due to a failed dependency
	// (when onStepFailure is "skip-dependents").
	StepSkipped StepStatus = "skipped"
	// StepCancelled indicates the step was cancelled due to a failed dependency
	// (when onStepFailure is "cascade") or workflow abort.
	StepCancelled StepStatus = "cancelled"
)
