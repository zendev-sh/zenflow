package exec

import (
	"cmp"
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/resume"
	"github.com/zendev-sh/zenflow/internal/router"
	"github.com/zendev-sh/zenflow/internal/spec"
	"github.com/zendev-sh/zenflow/internal/types"
)

// Concurrency defaults for the Executor scheduler and forEach loops.
const (
	// defaultMaxConcurrency is the fallback goroutine cap when neither the
	// workflow YAML Options.MaxConcurrency nor Executor.MaxConcurrency is set.
	// Precedence (highest wins, see executor.go runDAG ~line 511):
	// 1. Workflow.Options.MaxConcurrency (per-workflow YAML)
	// 2. Executor.MaxConcurrency (per-Orchestrator option,
	// set via WithMaxConcurrency)
	// 3. defaultMaxConcurrency (this constant, =5)
	// Cross-references: WithMaxConcurrency in options.go and
	// Orchestrator.maxConcurrency in zenflow.go both feed into level 2.
	defaultMaxConcurrency = 5
	// forEachMaxConcurrency is the hard upper bound on parallel forEach
	// iterations to prevent unbounded goroutine spawning.
	forEachMaxConcurrency = 100
)

// Executor runs a workflow with parallel step execution.
// It handles DAG scheduling, concurrency limiting, step timeouts, retries,
// and failure strategies (cascade, skip-dependents, abort).
type Executor struct {
	Runner       *AgentRunner
	Storage      Storage
	Progress     ProgressSink
	Workflow     *Workflow
	DefaultModel string
	// ForceModel, when non-empty, overrides Step.Model and AgentConfig.Model
	// during effective-model resolution. Set by Orchestrator from the
	// WithForceModel option; nested executors propagate the same value via
	// their constructors in executor_loop.go and executor_include.go.
	ForceModel     string
	MaxConcurrency int
	// SharedMem is the shared memory instance for inter-agent collaboration.
	// If set, shared memory tools are available and writes are persisted via Storage.
	SharedMem *SharedMemory
	// Tracer creates workflow/step trace spans when set.
	Tracer Tracer
	// Coordinator is the caller-provided AgentRunner that receives
	// workflow lifecycle events as RouterMessages on its Mailbox
	// Nil means no coordinator wiring - the executor skips the
	// mailbox + delivery-engine allocation and never pushes events.
	// The runner's lifecycle (Run loop) is owned by the caller.
	Coordinator *AgentRunner
	// Router enables inter-agent message passing. Created by executor when
	// Coordinator is set.
	Router *MessageRouter
	// RootRouter is the OUTERMOST executor's Router. Nested
	// executors (created by runRepeatUntilInnerDAG / runForEachInnerDAG /
	// runIncludeStep) propagate this so their inner-DAG steps register
	// delegations on the root router, allowing coord (which always uses
	// root.Router for forward_to_agent) to address inner steps by their
	// effective namespaced StepID. nil for the outermost executor (it
	// uses Router itself as root).
	RootRouter *MessageRouter
	// namespacePrefix is the parent loop/include namespace for
	// all step IDs run by this executor. Empty for the outermost
	// executor. For nested in repeat-until iteration N of parent loop
	// "L": "L.N". For nested in forEach item N of parent loop "L":
	// "L[N]". Effective StepID for inner runStep = namespacePrefix +
	// "." + step.ID. Drives mailbox keys, Router.RegisterInbox keys,
	// pushStepEventToCoord.From, and send_message.From - symmetric so
	// coord sees the same namespaced ID on receive AND uses it on
	// send via forward_to_agent.
	namespacePrefix string
	// per-run mailbox + wake registry created in Run when
	// the router is allocated. Persisted on the Executor (same lifecycle
	// scope as Router) so runStep can wire them into per-step
	// AgentRunners and the workflow-abort path can flush them. Both are
	// nil when the workflow has no coordinator runner installed.
	mailbox      MailboxStore
	wakeRegistry *router.MapWakeRegistry
	// Isolation provides per-step environment isolation (e.g., worktree-per-step).
	// If nil, steps run in the same working directory (no isolation).
	Isolation StepIsolation
	// RunID is the pre-generated run identifier. If empty, Executor generates one.
	// Set by Orchestrator so the run ID is available for tracing before execution starts.
	RunID string
	// ResumeSteps holds previously completed step results to skip re-execution.
	// Set by ResumeFlow. Nil for fresh runs.
	ResumeSteps map[string]*StepResult
	// ResumeRunID is the run ID to reuse when resuming. Empty for fresh runs.
	ResumeRunID string
	// IncludeDepth tracks recursive include nesting. Incremented per nested executor.
	// runIncludeStep rejects execution when IncludeDepth >= MaxIncludeDepth.
	IncludeDepth int
	// ParentDepResults carries dependency results from the parent include step
	// into the nested executor. Inner steps with no dependsOn receive these
	// results in their depResults map (spec §7 dependsOn rewriting).
	ParentDepResults map[string]*StepResult
	// OutputTransform transforms step output before injection into dependent
	// step prompts. If nil, the default truncation (maxDepContentBytes) is used.
	// P7.7.7: allows consumers to implement smart truncation or LLM compaction
	// for models with smaller context windows.
	OutputTransform OutputTransformer

	// agentStates holds per-stepID *goai.AgentState handles registered by
	// runStep before each AgentRunner.Run. The delivery-engine
	// poller reads these via Executor.AgentState(stepID) to decide when a
	// running step is idle and safe to inject mailbox messages into.
	// activeSteps tracks the set of currently-running step IDs. Entries
	// are added by runStep just before AgentRunner.Run and removed via a
	// deferred unregisterAgentState call once Run returns. The
	// DeliveryEngine queries ActiveSteps each tick to drive its poller.
	agentStatesMu sync.Mutex
	agentStates   map[string]*goai.AgentState
	activeSteps   map[string]struct{}

	// F3 / F5 / F6 / F7 / F8 - config knobs threaded through
	// from Orchestrator Options. Zero values fall back to compile-time
	// defaults so direct Executor users (tests, embedded callers) get
	// the historical behavior unchanged.
	MaxWakeCycles          int                 // per-step AgentRunner cap; 0 = default
	HoldTimeout            time.Duration       // F8; 0 = defaultHoldTimeout
	DropCallback           func(DropEvent)     // F3 user observer
	DropCallbackBufferSize int                 // F3
	MaxMailboxSize         int                 // F3 / F8 cap
	MailboxStoreFactory    func() MailboxStore // F3
	MailboxDeliveryEnabled *bool               // F3 (nil = on)
	EngineClock            EngineClock         // F3 lift
	ProgressBufferSize     int                 // F6 pump cap
	// transcript store (factory invoked per Run)
	// and caps. The store is owned by the Executor for the Run
	// lifetime; runStep passes the instance into every AgentRunner so
	// per-step conversations converge into a single store. The
	// ResumeStep path consults the same instance.
	TranscriptStoreFactory func() resume.TranscriptStore
	MaxTranscriptMessages  int
	MaxTranscriptBytes     int64
	// transcriptStore is the per-Run instance created in Run from
	// TranscriptStoreFactory (or the default InMemoryTranscriptStore
	// factory). nil when the whole Run has no transcript persistence.
	transcriptStore resume.TranscriptStore

	// resume coordination. resumes tracks per-stepID
	// serial locks + active-resume mailboxes. The run-lifetime context
	// is threaded explicitly via ResumeStep's ctx parameter (H3 refactor:
	// removed runCtx struct field and resumeContext method - callers
	// pass the correct cancellation scope directly).
	resumes   *ResumeStates
	resumesMu sync.Mutex

	// F8 - tracks in-flight resume goroutines so Run exit
	// waits for their orderly shutdown. Increment inside ResumeStep
	// before spawning runResume; decrement in runResume's defer. Run's
	// teardown path does a bounded Wait (see resumeShutdownTimeout).
	resumeWG sync.WaitGroup

	// resumeActiveCount is an atomic counter of currently-running
	// runResume goroutines. It is incremented on runResume entry and
	// decremented in its defer so Run's teardown timeout path can
	// report the exact leaked count (observable leaks).
	resumeActiveCount atomic.Int64

	// stepModelStrings tracks the USER-SUPPLIED workflow model string
	// (as written in the workflow YAML / default) per stepID when that
	// step ran. Consulted by ResumeStep to distinguish a transcript
	// that was recorded by THIS step (no resolver needed - same
	// workflow intent, possibly different wrapper provider.ModelID) vs
	// a transcript recorded under a genuinely different model
	// (resolver required). Populated by runStep before AgentRunner.Run
	// (VA-6 fix).
	stepModelStringsMu sync.Mutex
	stepModelStrings   map[string]string

	// resumePreStartDrainGate, when non-nil, is forwarded to every
	// AgentRunner spawned by runResume via its PreStartDrainGate field.
	// Test-only hook (unexported; set via setResumePreStartDrainGateForTest)
	// to deterministically order the resume goroutine's pre-start drain
	// relative to test Append calls.
	resumePreStartDrainGate <-chan struct{}

	// TruncateOnCapReached controls whether ResumeStep falls back to
	// LoadTruncated when the saved transcript is sealed past its
	// configured cap (VA-3b). Default false - sealed transcripts
	// surface DropReasonTranscriptTooLarge for safety. Set true via
	// WithTruncationOnCapReached to preserve operability at the cost
	// of a potentially partial conversation history.
	TruncateOnCapReached bool

	// F6 - resolver consulted by runResume when a saved
	// transcript references a model distinct from the default runner
	// model. nil resolver + non-matching transcript model ⇒ resume
	// fails with ErrModelResolverMissing.
	ModelResolver ModelResolver

	// ExternalInboxes are stepID-like identifiers for non-step senders
	// (e.g. "coordinator") whose mailboxes must be pre-registered with
	// the Router so reverse-routed RouterMessages - most notably the
	// resume response targeting OriginalSender="coordinator"
	// - land in the mailbox instead of dropping as DropReasonUnknownStep.
	// Populated via WithExternalInbox. Nil or empty = no extra inboxes.
	ExternalInboxes []string

	// SenderMatrixDAGAware (F7) - when true, runStep does NOT open
	// sender slots for sibling workflow steps; only the coordinator
	// dispatches inter-step messages, so the sibling-pessimistic NxN
	// matrix collapses to a single coordinator slot per running step.
	// Defaults to true (the safer + cheaper rule); set to false to
	// retain the pre-F7 conservative behavior.
	SenderMatrixDAGAware bool

	// FlowContext is the per-call use-case input supplied by the caller
	// via RunFlow's WithFlowContext option. When non-empty:
	// - if Coordinator != nil: the run-start path pushes a
	// workflow_start RouterMessage carrying this string as Content
	// into the coord runner's mailbox so the coord LLM sees the
	// context as its first event; per-step distribution is
	// then the coord's job (forward_to_agent calls).
	// - if Coordinator == nil: runStep prepends this string to every
	// step's effective user prompt as a static fallback.
	// Empty string = no-op (preserves previous behavior).
	FlowContext string
}

// MaxIncludeDepth moved to limits.go (centralized with other Max* caps).

// CoordRouterInboxID is the well-known step-ID used for the workflow
// Router's coordinator-targeted inbox. Steps reverse-route messages to
// the coordinator by Append-ing to this ID; drainCoordReverseReplies
// consumes them. This is DISTINCT from coordStepID(e.Coordinator) - that
// helper resolves the coord runner's *Mailbox* key (which may be the
// caller's own primary AgentRunner StepID, e.g. "primary" in an embedded SDK consumer).
// dual-ID convention:
// - coordStepID(runner) - caller-owned coord runner Mailbox key (push
// destination for lifecycle events).
// - CoordRouterInboxID - workflow Router's coord inbox key (drain
// source for reverse-routed step replies).
// They are independent strings: a caller may pass a custom coord runner
// with StepID="primary" and reverse replies still flow through the
// Router's "coordinator" inbox unchanged.
// CoordRouterInboxID lives in aliases.go.

// runIDRandRead is the entropy source used by GenerateRunID. Package-level
// var so a coverage test can inject a failing reader to exercise the
// rand.Read error branch (otherwise unreachable: crypto/rand.Read on a
// healthy system never fails).
var runIDRandRead = rand.Read

// GenerateRunID creates a random run identifier. Package-level var for test injection.
var GenerateRunID = func() (string, error) {
	b := make([]byte, 8)
	if _, err := runIDRandRead(b); err != nil {
		return "", fmt.Errorf("generate run ID: %w", err)
	}
	return fmt.Sprintf("run_%x", b), nil
}

// waitOrAbort races wg.Wait against ctx.Done. Returns true if the WaitGroup
// drained normally, false if ctx expired first. When it returns false, some
// step goroutines are still in-flight and orphaned - typically because a
// provider's DoGenerate is blocked on a network call that ignores ctx. The
// caller must NOT share mutable state (e.g., the results map) with those
// goroutines after this returns false, or they will mutate return values
// after the caller has already used them.
// This exists because naked wg.Wait blocks indefinitely when a step is
// stuck in a ctx-ignoring call site, causing `zenflow --timeout` to hang
// far past its deadline.
func waitOrAbort(ctx context.Context, wg *sync.WaitGroup) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// Run executes all workflow steps in dependency order with parallel scheduling.
func (e *Executor) Run(ctx context.Context) (*WorkflowResult, error) {
	// Defensive guards for direct construction (without going through
	// Orchestrator). Orchestrator.runFlowWithID always populates these,
	// but Executor is exported and a library consumer may construct
	// it manually. Without these guards a zero-value Executor would
	// nil-panic inside ValidateWorkflow / runStep.
	if e.Workflow == nil {
		return nil, ErrWorkflowNil
	}
	if e.Runner == nil {
		return nil, ErrRunnerNil
	}
	// derive a child ctx with a dedicated cancel so the
	// teardown defer can force-cancel in-flight resume goroutines even
	// if the caller's ctx remains live. Without this, `resumeShutdownTimeout`
	// was a pure timeout: a wedged goroutine observing only the caller's
	// ctx (still open) would leak for the full LLM wall-clock. The child
	// ctx is cancelled BEFORE waiting on resumeWG so every runResume
	// picks up ctx.Done promptly and emits
	// EventResumeFailed{reason:"workflow-shutdown"}.
	// H3: derive a child ctx with a dedicated cancel so the teardown defer
	// can force-cancel in-flight resume goroutines even if the caller's ctx
	// remains live. The run-lifetime ctx is threaded to resume goroutines via
	// runCtxProvider (installed on the Router below) - Router.Send calls
	// runCtxProvider and passes the result as the ctx argument to
	// ResumeStep, eliminating the need for a ctx struct field on Executor.
	runCtx, cancelRun := context.WithCancel(ctx)
	ctx = runCtx
	defer func() {
		// G3 (R3): force-cancel resume goroutines BEFORE waiting so
		// they exit via ctx.Done rather than relying on the parent
		// ctx propagating. cancelRun is idempotent - safe to call
		// even if the parent ctx already cancelled.
		cancelRun()
		done := make(chan struct{})
		go func() {
			e.resumeWG.Wait()
			close(done)
		}()
		shutdownTimer := time.NewTimer(resumeShutdownTimeout)
		defer shutdownTimer.Stop()
		select {
		case <-done:
		case <-shutdownTimer.C:
			// Safety net: a goroutine ignored ctx entirely (e.g. a
			// provider driver with no ctx plumbing). Report the leak
			// count so operators can trace the offender. The active
			// counter is decremented in runResume's defer so
			// whatever remains here is the wedged cohort.
			leaked := e.resumeActiveCount.Load()
			if e.Progress != nil {
				e.Progress.OnEvent(context.Background(), Event{
					Type:      types.EventMessage,
					Timestamp: time.Now(),
					RunID:     e.runID(),
					Message: fmt.Sprintf(
						"zenflow: timed out waiting for in-flight resume goroutines (%s, leaked=%d)",
						resumeShutdownTimeout, leaked),
					Data: map[string]any{
						"reason": "resume-timeout",
						"count":  leaked,
					},
				})
			}
		}
	}()

	// Validate workflow and get topological order.
	order, err := ValidateWorkflow(e.Workflow)
	if err != nil {
		return nil, err
	}

	start := time.Now()

	var runID string
	switch {
	case e.ResumeRunID != "":
		runID = e.ResumeRunID
	case e.RunID != "":
		runID = e.RunID
	default:
		var err error
		runID, err = GenerateRunID()
		if err != nil {
			return nil, err
		}
	}

	// Warn if options.isolation is set but no StepIsolation implementation is configured.
	if e.Workflow.Options.Isolation != "" && e.Isolation == nil {
		if e.Progress != nil {
			e.Progress.OnEvent(ctx, Event{
				Type:      types.EventMessage,
				Timestamp: time.Now(),
				RunID:     runID,
				Message:   fmt.Sprintf("workflow options.isolation=%q but no StepIsolation implementation configured; isolation will be skipped", e.Workflow.Options.Isolation),
			})
		} else {
			slog.WarnContext(ctx, "workflow options.isolation set but no StepIsolation configured",
				"isolation", e.Workflow.Options.Isolation,
				"run_id", runID,
			)
		}
	}

	// Apply workflow-level maxRetries to all agent runners.
	if e.Workflow.Options.MaxRetries != nil {
		e.Runner.goAIOptions = append(e.Runner.goAIOptions, goai.WithMaxRetries(*e.Workflow.Options.MaxRetries))
	}

	// Apply workflow-level timeout.
	if e.Workflow.Options.Timeout.D() > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.Workflow.Options.Timeout.D())
		defer cancel()
	}

	// Index steps and build dependency graph.
	stepMap := make(map[string]Step, len(e.Workflow.Steps))
	dependents := make(map[string][]string, len(e.Workflow.Steps)) // stepID -> list of steps that depend on it
	for _, s := range e.Workflow.Steps {
		stepMap[s.ID] = s
		for _, dep := range s.DependsOn {
			dependents[dep] = append(dependents[dep], s.ID)
		}
	}

	// MaxConcurrency: workflow YAML > orchestrator option > default 5.
	maxConc := e.Workflow.Options.MaxConcurrency
	if maxConc <= 0 {
		maxConc = e.MaxConcurrency
	}
	if maxConc <= 0 {
		maxConc = defaultMaxConcurrency
	}
	sem := make(chan struct{}, maxConc)

	strategy := cmp.Or(e.Workflow.Options.OnStepFailure, spec.FailureCascade)

	// Result tracking.
	var mu sync.Mutex
	results := make(map[string]*StepResult, len(order))
	var totalTokens provider.Usage
	skipped := make(map[string]bool, len(order))  // steps to skip due to failed deps
	cascaded := make(map[string]bool, len(order)) // steps to mark as cascade-failed (not skipped)

	// Track in-degree for parallel scheduling.
	inDegree := make(map[string]int, len(e.Workflow.Steps))
	for _, s := range e.Workflow.Steps {
		inDegree[s.ID] = len(s.DependsOn)
	}

	// Resume: pre-populate completed steps from checkpoint.
	if e.ResumeSteps != nil {
		for stepID, sr := range e.ResumeSteps {
			results[stepID] = sr
			addUsage(&totalTokens, sr.Tokens)

			// Decrement in-degree for dependents of completed steps.
			for _, dep := range dependents[stepID] {
				inDegree[dep]--
			}
		}
	}

	// Save initial run with a snapshot (not sharing mutable map).
	if e.Storage != nil {
		run := &Run{ID: runID, Workflow: e.Workflow, Status: spec.StatusRunning, Steps: make(map[string]*StepResult, len(order))}
		if sErr := e.Storage.SaveRun(ctx, run); sErr != nil {
			slog.Warn("initial SaveRun failed (continuing without persistence)", "err", sErr, "run_id", run.ID)
			// continue - Storage is observability, not correctness; final SaveRun will retry
		}
	}

	// Initialize a fresh MessageRouter when a coordinator runner is set
	// The mailbox + delivery engine stack is the sole delivery
	// path for messaging-enabled workflows.
	var (
		mailbox      MailboxStore
		wakeRegistry *router.MapWakeRegistry
		engineDone   <-chan struct{}
		engineCancel context.CancelFunc
	)
	mailboxEnabled := e.MailboxDeliveryEnabled == nil || *e.MailboxDeliveryEnabled
	if e.Coordinator != nil && mailboxEnabled {
		{
			// - the orchestrator pre-allocates Router in New
			// and passes it via the Executor.Router field (zenflow.go
			// runFlowWithID). When that path supplies a Router we reuse
			// it so the coord runner's pre-wired Router pointer matches
			// the one the executor actually uses. Standalone Executor
			// usage (no Orchestrator) still gets a fresh Router via the
			// nil-check fallback below.
			if e.Router == nil {
				e.Router = NewMessageRouter()
			}
			// F3 - WithMailboxStore lets consumers swap the default
			// in-memory store. WithMaxMailboxSize wraps the default
			// store with a bounded variant that emits "mailbox-full"
			// drops on overflow.
			switch {
			case e.MailboxStoreFactory != nil:
				mailbox = e.MailboxStoreFactory()
			case e.MaxMailboxSize > 0:
				mailbox = router.NewBoundedInMemoryStore(e.MaxMailboxSize)
			default:
				mailbox = NewInMemoryMailboxStore()
			}
			e.Router.SetMailbox(mailbox)

			// G7 : pre-register every workflow step ID so that
			// any future sibling-direct Send path can auto-open a sender
			// slot just-in-time instead of silently dropping with
			// DropReasonUnknownStep. Truly unknown step IDs (typo,
			// removed step) continue to drop.
			// also mark loop / include containers as wrapper
			// steps so coord's forward_to_agent rejects messages
			// targeting them (wrappers have no agent - silent misroute
			// otherwise). Detection: step has Loop or Include defined.
			for _, s := range e.Workflow.Steps {
				e.Router.RegisterStep(s.ID)
				if s.Loop != nil || s.Include != "" {
					e.Router.RegisterWrapperStep(s.ID)
				}
			}
			// Register external-sender inboxes (non-step identities such
			// as "coordinator") so reverse messages from
			// resumed steps land in the mailbox instead of dropping as
			// DropReasonUnknownStep. See WithExternalInbox.
			// auto-registration: when a coordinator
			// runner is installed, implicitly add the coord step ID
			// to the effective inbox set. Without this, CLI runs that
			// never call WithExternalInbox see the reverse RouterMessage
			// from a resumed step drop with DropReasonUnknownStep. We
			// compute effective inboxes locally and do NOT mutate
			// e.ExternalInboxes.
			effectiveInboxes := make([]string, len(e.ExternalInboxes), len(e.ExternalInboxes)+2)
			copy(effectiveInboxes, e.ExternalInboxes)
			coordID := coordStepID(e.Coordinator)
			// - dual-inbox registration. Two distinct
			// string keys both default to "coordinator" but serve
			// different roles (see WithCoordinator docstring):
			// 1. coordStepID(runner) - caller-owned coord runner
			// Mailbox key (lifecycle-event push destination).
			// 2. CoordRouterInboxID - workflow Router's coord inbox
			// key (resumed-step reverse-reply destination).
			// Both must be RegisterStep/RegisterInbox'd. When the
			// caller passes a custom StepID (e.g. "primary"), the two
			// IDs differ and 's "register coordID only" logic
			// silently drops reverse replies addressed to
			// CoordRouterInboxID with DropReasonUnknownStep. Always
			// register both; the de-dup loop below collapses repeats
			// for the default case where they coincide.
			// - replace O(n*m) nested-scan dedup with
			// a single-pass map. Bounded at small N today (extras=2,
			// effectiveInboxes typically <5), but the map form keeps
			// growth linear if either side ever expands.
			seen := make(map[string]struct{}, len(effectiveInboxes)+2)
			for _, id := range effectiveInboxes {
				seen[id] = struct{}{}
			}
			for _, extra := range []string{coordID, CoordRouterInboxID} {
				if _, ok := seen[extra]; ok {
					continue
				}
				seen[extra] = struct{}{}
				effectiveInboxes = append(effectiveInboxes, extra)
			}
			for _, id := range effectiveInboxes {
				if id == "" {
					continue
				}
				e.Router.RegisterStep(id)
				e.Router.RegisterInbox(id)
			}
			// - wiring moved to Orchestrator.New
			// (zenflow.go) so the coord runner's Router/Progress are
			// set synchronously before the consumer's coord goroutine
			// can read them. Doing the
			// wiring here in Run was racy: cmdFlow's startCoordRunner
			// spawns the goroutine BEFORE RunFlow is called, so the
			// goroutine's first iteration could read runner.Router /
			// runner.Progress concurrently with the assignment below.
			// Confirmed by `go test -race -count=2 -run
			// TestCmdFlow_ModelOverride` reproducing the race.
			// Standalone-Executor usage (no Orchestrator wrapper) that
			// installs a Coordinator directly via the struct literal
			// is responsible for pre-wiring Coordinator.Router and
			// Coordinator.Progress before calling Run. The fallback
			// path below covers callers that pre-wire - the executor
			// honors caller-supplied values without overwriting.
			if e.Coordinator.router == nil {
				e.Coordinator.router = e.Router
			}
			if e.Coordinator.progress == nil {
				e.Coordinator.progress = e.Progress
			}

			// F6 wrap the user-supplied progress sink in the
			// non-blocking pump so emit calls from critical paths
			// (router stepLock, poller invariant-check) never block on
			// a slow downstream sink.
			progressSink := wrapProgressNonBlocking(e.Progress, e.ProgressBufferSize)
			if progressSink != nil {
				// Replace e.Progress so the rest of the run goes through
				// the pump as well (events/output emitted from runStep,
				// pushStepEventToCoord, etc.). Restore via defer at Stop.
				origProgress := e.Progress
				e.Progress = progressSink
				defer func() {
					progressSink.Stop()
					e.Progress = origProgress
				}()
			}

			// Wire router-side drops into EventMessageDropped so the
			// "zero silent drops" contract holds for target-terminal,
			// unknown-step, and mailbox-closed-by-finalize paths
			// F3 also fans out to the user-supplied
			// DropCallback when configured.
			runIDForDrop := runID
			userDrop := newDropFanout(e.DropCallback, e.DropCallbackBufferSize)
			defer userDrop.Stop()
			emitProgress := e.Progress
			e.Router.SetOnDrop(func(de DropEvent) {
				if emitProgress != nil {
					emitProgress.OnEvent(ctx, Event{
						Type:      types.EventMessageDropped,
						Timestamp: time.Now(),
						RunID:     runIDForDrop,
						StepID:    de.StepID,
						Message:   fmt.Sprintf("[%s -> %s]: %s", de.Msg.From, de.StepID, de.Msg.Content),
						Data: map[string]any{
							"reason":   de.Reason.String(),
							"from":     de.Msg.From,
							"to":       de.StepID,
							"msg_type": int(de.Msg.Type),
						},
					})
				}
				userDrop.dispatch(de)
			})
			wakeRegistry = router.NewWakeRegistry()
			e.mailbox = mailbox
			e.wakeRegistry = wakeRegistry

			// Allocate the transcript store for this Run. Custom factory
			// > default InMemoryTranscriptStore with configured caps.
			// The store's lifetime is the Run; it is NOT reused across
			// Runs (GC on Run exit).
			switch {
			case e.TranscriptStoreFactory != nil:
				e.transcriptStore = e.TranscriptStoreFactory()
			default:
				e.transcriptStore = resume.NewInMemoryTranscriptStoreWithCaps(
					e.MaxTranscriptMessages, e.MaxTranscriptBytes,
				)
			}
			// register the Executor as the Router's
			// resume hook. Must happen AFTER transcriptStore is
			// allocated so CanResume reports true.
			e.Router.SetResumer(e)
			// H3: wire the run-lifetime ctx provider so Router.Send passes
			// the correct cancellation scope to ResumeStep. The closure
			// captures the local runCtx (derived above - a child of the
			// caller's ctx with its own cancel) so Run cancellation
			// propagates into every resume goroutine.
			e.Router.SetRunCtxProvider(func() context.Context { return runCtx })

			// register coord runner's Wake under BOTH coord
			// keys (coordID + CoordRouterInboxID; differ when caller
			// uses a custom StepID like "primary"). Then install the
			// Router.afterSend hook so every successful Append fires
			// SignalWake on the matching registered target. This closes
			// the gap where send_message → Router.Send → mailbox.Append
			// landed in coord's mailbox but coord never woke until the
			// next lifecycle event (often after the sender step had
			// already exited, surfacing the message as a (resumed)
			// reverse-drain at end-of-workflow).
			if e.Coordinator != nil && e.Coordinator.wake != nil {
				coordWakeTarget := router.NewChanWakeTarget(e.Coordinator.wake)
				wakeRegistry.Register(coordStepID(e.Coordinator), coordWakeTarget)
				if coordStepID(e.Coordinator) != CoordRouterInboxID {
					wakeRegistry.Register(CoordRouterInboxID, coordWakeTarget)
				}
			}
			// bridge Router messages addressed to coord into
			// coord.Mailbox so coord's drain loop sees them in real
			// time. Without the bridge, send_message → Router.Send
			// lands in Router-mailbox; coord runner reads from
			// coord.Mailbox (separate instance from
			// NewDefaultCoordRunner); coord wakes (above SignalWake)
			// but finds an empty mailbox and exits. Messages then sit
			// unread in Router-mailbox until end-of-step
			// drainCoordReverseReplies surfaces them as `(resumed)`.
			// Two mailboxes can't be unified because
			// drainCoordReverseReplies must drain Router-routed
			// messages (resume replies + send_message) at workflow
			// end, while lifecycle pushes via pushCoordEvent must NOT
			// be re-emitted as `(resumed)`. A bridge keeps the two
			// concerns separate while ensuring coord's loop sees
			// router-routed messages.
			coordIDForBridge := coordStepID(e.Coordinator)
			coordMailboxBridge := e.Coordinator.mailbox
			routerMailboxBridge := mailbox
			e.Router.SetAfterSend(func(stepID string, msg RouterMessage) {
				// Wrap the bridge body in recover so a panicking
				// caller-supplied Mailbox / ProgressSink cannot crash
				// Router.Send. Symmetrical with the workflow-start /
				// workflow-end coord-bridge recovers below; without it,
				// a defective bridge target would propagate up through
				// Router.Send into arbitrary sender goroutines.
				defer func() {
					if r := recover(); r != nil {
						var bridgeErr error
						if e2, ok := r.(error); ok {
							bridgeErr = fmt.Errorf("coord bridge panic: %w", e2)
						} else {
							bridgeErr = fmt.Errorf("coord bridge panic: %v", r)
						}
						if e.Progress != nil {
							e.Progress.OnEvent(ctx, Event{
								Type: types.EventError, Timestamp: time.Now(),
								RunID: runID, StepID: stepID, Error: bridgeErr,
							})
						} else {
							slog.WarnContext(ctx, "coord bridge panic recovered", "err", bridgeErr, "run_id", runID, "step_id", stepID)
						}
					}
				}()
				if t := wakeRegistry.WakeTarget(stepID); t != nil {
					t.SignalWake()
				}
				if coordMailboxBridge == nil {
					return
				}
				if stepID != CoordRouterInboxID && stepID != coordIDForBridge {
					return
				}
				// bridge as MOVE not COPY. Append to coord.Mailbox
				// (where coord runner's drain loop reads) AND MarkRead the
				// original in Router-mailbox. Without the MarkRead, the
				// per-step drainCoordReverseReplies sees
				// the same message still unread in Router-mailbox and
				// re-emits it as a duplicate `(resumed)` event AFTER the
				// sender step exited - matching the user's debate-until.yaml
				// observation where pro/con send_message surfaced as
				// `(resumed)` from coord even though the bridge had already
				// delivered it. msg.MessageID is populated by Router.Send
				// after mailbox.Append assigns the canonical ID.
				if _, err := coordMailboxBridge.Append(coordIDForBridge, msg); err != nil {
					slog.WarnContext(ctx, "mailbox append failed", "err", err, "site", "coord-bridge", "run_id", runID, "step_id", stepID, "msg_id", msg.MessageID)
				}
				// Emit EventCoordinatorInboxMessage for observability -
				// otherwise external observers (CLI sinks, JSON consumers,
				// E2E tests) lose visibility into bridged messages.
				// drainCoordReverseReplies emits the same event when it
				// drains from the Router mailbox; here in the bridge path
				// the MarkRead below removes the message before drain runs,
				// so emit it ourselves to keep the observability contract
				// invariant across both paths. Without this, resumed-step
				// reverse replies (and every send_message routed to
				// "coordinator") would silently bypass the JSON sink even
				// though they reach the coord runner correctly.
				if e.Progress != nil {
					e.Progress.OnEvent(ctx, Event{
						Type:        types.EventCoordinatorInboxMessage,
						Timestamp:   time.Now(),
						RunID:       runID,
						Message:     msg.Content,
						MessageKind: types.MessageKindContent,
						Data: map[string]any{
							"from": msg.From,
							"type": msg.Type.String(),
						},
					})
				}
				if msg.MessageID != "" && routerMailboxBridge != nil {
					routerMailboxBridge.MarkRead(stepID, []string{msg.MessageID})
				}
			})
			engineCtx, cancel := context.WithCancel(ctx)
			engineCancel = cancel
			var engineOpts []router.EngineOption
			if e.EngineClock != nil {
				engineOpts = append(engineOpts, router.WithEngineClock(e.EngineClock))
			}
			// Wire the router as the stepLock acquirer so the engine's
			// poll Observe+SignalWake sequence runs under stepLock.RLock,
			// eliminating the read-then-wake TOCTOU against the runner's
			// terminal-state defer (which takes stepLock.Lock).
			if e.Router != nil {
				engineOpts = append(engineOpts, router.WithStepLocker(e.Router))
			}
			engine := router.NewDeliveryEngine(e, mailbox, wakeRegistry, engineOpts...)
			engineDone = engine.Start(engineCtx)
		}
	}
	// Always defer engine teardown so the goroutine exits even on panic.
	defer func() {
		if engineCancel != nil {
			engineCancel()
		}
		if engineDone != nil {
			<-engineDone
		}
	}()

	if e.Progress != nil {
		e.Progress.OnEvent(ctx, Event{
			Type:      types.EventWorkflowStart,
			Timestamp: time.Now(),
			RunID:     runID,
			Message:   e.Workflow.Name,
			Data:      map[string]any{"total": len(order)},
		})
	}

	// when a coordinator runner is installed, push a
	// workflow_start RouterMessage carrying the per-call FlowContext as
	// its Content into the coord runner's mailbox. The push lands BEFORE
	// any per-step lifecycle event (StepStart) so the coord LLM sees the
	// curated use-case input as its first inbox message and can
	// formulate per-step forwards (forward_to_agent calls). When
	// FlowContext is empty the push still runs (Content empty) so the
	// coord observes the workflow boundary; when no coord runner is
	// installed pushCoordEvent short-circuits. Wrapped in recover to
	// match Fix 8 - a defective caller-supplied Mailbox
	// must not crash RunFlow at the boundary.
	func() {
		defer func() {
			if r := recover(); r != nil {
				var appendErr error
				if e2, ok := r.(error); ok {
					appendErr = fmt.Errorf("workflow-start coord append panic: %w", e2)
				} else {
					appendErr = fmt.Errorf("workflow-start coord append panic: %v", r)
				}
				if e.Progress != nil {
					e.Progress.OnEvent(ctx, Event{
						Type: types.EventError, Timestamp: time.Now(),
						RunID: runID, Error: appendErr,
					})
				} else {
					slog.WarnContext(ctx, "workflow-start coord append panic recovered",
						"err", appendErr,
						"run_id", runID,
					)
				}
			}
		}()
		// suppress workflow-start push when this executor is
		// nested (running an inner-DAG mini-workflow for a loop /
		// forEach / include). The OUTER executor already pushed its
		// workflow-start; nested miniWF lifecycle events are internal
		// plumbing and would surface to coord LLM as confusing
		// "workflow started" events for synthetic names like
		// "debate-rounds-repeat-0".
		if e.namespacePrefix == "" {
			e.pushCoordEvent(Event{
				Type:      types.EventWorkflowStart,
				Timestamp: time.Now(),
				RunID:     runID,
				Message:   e.Workflow.Name,
				Context:   e.FlowContext,
			})
		}
	}()

	// Abort context for "abort" strategy.
	abortCtx, abortCancel := context.WithCancel(ctx)
	defer abortCancel()

	var failed bool
	var wg sync.WaitGroup

	stepIndex := make(map[string]int, len(order))
	for i, id := range order {
		stepIndex[id] = i
	}

	// done channel: goroutines send their step ID when complete.
	done := make(chan string, len(order))

	// running tracks step IDs currently in-flight (results[id] == nil).
	// Used by scheduleOrder for least-busy/round-robin strategies.
	running := make(map[string]bool, maxConc)

	// dispatch sends all steps with zero in-degree to execution.
	// Loops until stable - handles cascading skip/cancel propagation regardless
	// of step declaration order. Must be called with mu held.
	dispatch := func() {
		if abortCtx.Err() != nil {
			return
		}
		// : propagate skip/cancel for zero-inDegree steps.
		for {
			progress := false
			for _, s := range e.Workflow.Steps {
				if inDegree[s.ID] != 0 {
					continue
				}
				if _, handled := results[s.ID]; handled {
					continue
				}
				if !skipped[s.ID] && !cascaded[s.ID] {
					continue
				}
				status := spec.StepSkipped
				if cascaded[s.ID] {
					status = spec.StepCancelled
				}
				results[s.ID] = &StepResult{ID: s.ID, Status: status}
				for _, dep := range dependents[s.ID] {
					inDegree[dep]--
					if skipped[s.ID] {
						skipped[dep] = true
					} else {
						cascaded[dep] = true
					}
				}
				if e.Progress != nil {
					e.Progress.OnEvent(ctx, Event{
						Type:      types.EventStepSkipped,
						Timestamp: time.Now(),
						RunID:     runID,
						StepID:    s.ID,
					})
				}
				progress = true
			}
			if !progress {
				break
			}
		}

		// -4 loop: collect ready steps, apply scheduler, evaluate conditions,
		// dispatch. Re-loop if any condition skips freed new dependents.
		for {
			conditionSkipped := false

			// : collect ready steps (inDegree==0, not handled, not skipped/cascaded).
			readySteps := make([]Step, 0, len(e.Workflow.Steps))
			for _, s := range e.Workflow.Steps {
				if inDegree[s.ID] != 0 {
					continue
				}
				if _, handled := results[s.ID]; handled {
					continue
				}
				readySteps = append(readySteps, s)
			}

			if len(readySteps) == 0 {
				break
			}

			// : apply scheduler strategy to reorder ready steps.
			readySteps = e.scheduleOrder(readySteps, running)

			// : dispatch ready steps (with CEL condition evaluation).
			for _, s := range readySteps {
				// Evaluate CEL condition before dispatching.
				if s.Condition != nil {
					evalCtx := BuildEvalContext(results)
					condResult, condErr := EvaluateCEL(*s.Condition, evalCtx)
					if condErr != nil {
						// Distinguish runtime data errors (missing field, no such key) from
						// type/compile errors. Runtime data errors → skip (treat as false);
						// compile/type errors → fail (workflow author bug).
						errStr := condErr.Error()
						isRuntimeDataErr := strings.Contains(errStr, "no such key") ||
							strings.Contains(errStr, "no such attribute") ||
							strings.Contains(errStr, "undefined field")
						if isRuntimeDataErr {
							if e.Progress != nil {
								e.Progress.OnEvent(ctx, Event{
									Type:      types.EventMessage,
									Timestamp: time.Now(),
									RunID:     runID,
									StepID:    s.ID,
									Message:   fmt.Sprintf("condition eval: missing data, skipping step: %v", condErr),
								})
							}
							results[s.ID] = &StepResult{ID: s.ID, Status: spec.StepSkipped}
							for _, dep := range dependents[s.ID] {
								inDegree[dep]--
							}
							conditionSkipped = true
							continue
						}
						// Treat other errors (type mismatch, compile) as step failure.
						results[s.ID] = &StepResult{
							ID:     s.ID,
							Status: spec.StepFailed,
							Error:  fmt.Errorf("run %q step %q: condition eval: %w", runID, s.ID, condErr),
						}
						failed = true
						switch strategy {
						case spec.FailureAbort:
							abortCancel()
						case spec.FailureCascade:
							e.markDependents(s.ID, dependents, cascaded)
						case spec.FailureSkipDependents:
							e.markDependents(s.ID, dependents, skipped)
						}
						for _, dep := range dependents[s.ID] {
							inDegree[dep]--
						}
						conditionSkipped = true
						continue
					}
					if !condResult {
						// Condition is false - skip step. Dependents still run
						// (condition-skip does NOT cascade like failure-skip).
						results[s.ID] = &StepResult{ID: s.ID, Status: spec.StepSkipped}
						for _, dep := range dependents[s.ID] {
							inDegree[dep]--
						}
						if e.Progress != nil {
							e.Progress.OnEvent(ctx, Event{
								Type:      types.EventStepSkipped,
								Timestamp: time.Now(),
								RunID:     runID,
								StepID:    s.ID,
								Message:   "condition false",
							})
						}
						conditionSkipped = true
						continue
					}
				}
				// Mark as in-flight.
				results[s.ID] = nil
				running[s.ID] = true
				stepID := s.ID
				// Snapshot dependency results under the lock for prompt injection.
				depResults := make(map[string]*StepResult, len(s.DependsOn))
				for _, dep := range s.DependsOn {
					if sr, ok := results[dep]; ok && sr != nil {
						depResults[dep] = sr
					}
				}
				// G3: For inner steps with no dependsOn, inject parent dep results
				// from include step (spec §7 dependsOn rewriting).
				if len(s.DependsOn) == 0 && len(e.ParentDepResults) > 0 {
					for k, v := range e.ParentDepResults {
						depResults[k] = v
					}
				}
				wg.Go(func() {
					// Acquire semaphore with abort awareness. If abortCtx is
					// cancelled while waiting for a slot, mark step as cancelled
					// and return instead of blocking indefinitely.
					select {
					case sem <- struct{}{}:
					case <-abortCtx.Done():
						mu.Lock()
						results[stepID] = &StepResult{ID: stepID, Status: spec.StepCancelled, Error: abortCtx.Err()}
						delete(running, stepID)
						mu.Unlock()
						// Non-blocking send: main loop may have exited after abort.
						select {
						case done <- stepID:
						default:
						}
						return
					}
					defer func() { <-sem }()

					step := stepMap[stepID]

					// Recover from panics in step execution (tool/progress/storage panics)
					// to prevent a single step from crashing the entire process.
					var sr *StepResult
					func() {
						defer func() {
							if r := recover(); r != nil {
								var panicErr error
								if e2, ok := r.(error); ok {
									panicErr = fmt.Errorf("panic in step %q: %w", stepID, e2)
								} else {
									panicErr = fmt.Errorf("panic in step %q: %v", stepID, r)
								}
								sr = &StepResult{ID: stepID, Status: spec.StepFailed, Error: panicErr}
							}
						}()
						switch {
						case step.Include != "":
							sr = e.runIncludeStep(abortCtx, runID, stepID, step, stepIndex[stepID], len(order), depResults)
						case step.Loop != nil:
							// Retry loop for loop steps (spec: retries apply to entire loop block).
							// runStep handles its own retries internally, so only wrap runLoopStep.
							// Validation guarantees Retries >= 0, so maxAttempts >= 1.
							maxAttempts := step.Retries + 1
							for attempt := range maxAttempts {
								sr = e.runLoopStep(abortCtx, runID, stepID, step, stepIndex[stepID], len(order), depResults)
								if sr.Status != spec.StepFailed || abortCtx.Err() != nil {
									break
								}
								_ = attempt // reset for retry - loop restarts from iteration 0
							}
						default:
							sr = e.runStep(abortCtx, runID, stepID, step, stepIndex[stepID], len(order), depResults)
						}
					}()

					mu.Lock()
					results[stepID] = sr
					delete(running, stepID)
					addUsage(&totalTokens, sr.Tokens)

					if sr.Status == spec.StepFailed {
						failed = true
						switch strategy {
						case spec.FailureAbort:
							abortCancel()
						case spec.FailureCascade:
							e.markDependents(stepID, dependents, cascaded)
						case spec.FailureSkipDependents:
							e.markDependents(stepID, dependents, skipped)
						}
					}

					// Decrement in-degree for dependents.
					for _, dep := range dependents[stepID] {
						inDegree[dep]--
					}

					// Snapshot results for coordinator (avoid data race - results is
					// written concurrently by other step goroutines under mu).
					// Used by pushStepEventToCoord to compute progress counters
					// embedded in the pushed RouterMessage Content.
					var resultsSnapshot map[string]*StepResult
					if e.Coordinator != nil {
						resultsSnapshot = make(map[string]*StepResult, len(results))
						for k, v := range results {
							resultsSnapshot[k] = v
						}
					}
					mu.Unlock()

					// Persist step result.
					if e.Storage != nil {
						if sErr := e.Storage.SaveStepResult(ctx, runID, stepID, sr); sErr != nil {
							storageErr := fmt.Errorf("run %q step %q: save step result: %w", runID, stepID, sErr)
							if e.Progress != nil {
								e.Progress.OnEvent(ctx, Event{
									Type:      types.EventError,
									Timestamp: time.Now(),
									RunID:     runID,
									StepID:    stepID,
									Error:     storageErr,
								})
							} else {
								slog.WarnContext(ctx, "save step result failed",
									"err", storageErr,
									"run_id", runID,
									"step_id", stepID,
								)
							}
						}
					}

					// Signal step completion first so dependent steps can be
					// dispatched immediately without waiting for coordinator LLM call.
					// Non-blocking send: main loop may have exited after abort.
					select {
					case done <- stepID:
					default:
					}

					// Coordinator OnStepEvent - runs after done signal so it
					// does not block DAG dispatch. Wrapped in recover to prevent
					// coordinator panics from crashing the step goroutine.
					// Delivery note: coordinator-targeted messages are routed
					// through the mailbox + poller stack. Wake signals re-enter
					// the running agent's tool loop so late arrivals are drained
					// before termination.
					func() {
						defer func() {
							if r := recover(); r != nil {
								var coordErr error
								if e2, ok := r.(error); ok {
									coordErr = fmt.Errorf("coordinator panic: %w", e2)
								} else {
									coordErr = fmt.Errorf("coordinator panic: %v", r)
								}
								if e.Progress != nil {
									e.Progress.OnEvent(ctx, Event{
										Type: types.EventError, Timestamp: time.Now(),
										RunID: runID, StepID: stepID, Error: coordErr,
									})
								} else {
									slog.WarnContext(ctx, "coordinator notify panic recovered",
										"err", coordErr,
										"run_id", runID,
										"step_id", stepID,
									)
								}
							}
						}()
						// : split into two steps.
						// 1) push the rich step-end event into the coord
						// runner's Mailbox (was inline in notifyCoordinator).
						// 2) drain reverse-routed replies sitting in the
						// workflow Router's "coordinator" inbox (was the
						// tail of notifyCoordinator).
						e.pushStepEventToCoord(ctx, runID, stepID, step.Agent, sr, resultsSnapshot)
						e.drainCoordReverseReplies(ctx, runID)
					}()
				})
			}
			if !conditionSkipped {
				break
			}
		}
	}

	// countRemaining returns the number of steps without a final result.
	// Must be called with mu held.
	countRemaining := func() int {
		n := len(order)
		for _, s := range e.Workflow.Steps {
			if sr, ok := results[s.ID]; ok && sr != nil {
				n--
			}
		}
		return n
	}

	// Initial dispatch.
	mu.Lock()
	dispatch()
	remaining := countRemaining()
	mu.Unlock()

	// aborted is true if we bailed out of a wait because ctx expired while
	// step goroutines were still in-flight. When true, orphaned goroutines
	// may continue mutating `results`/`totalTokens`; we snapshot those into
	// finalResults/finalTokens below so the caller sees a stable view.
	aborted := false

	// abortDrainGrace is the time granted to step goroutines after an abort
	// signal fires. Steps that honor ctx.Done (the vast majority) finish
	// within milliseconds. If any step is still running after this window,
	// we assume it is stuck on a ctx-ignoring provider call and
	// bail without waiting further. This is the short, aggressive cap used
	// when we KNOW the workflow is being stopped.
	const abortDrainGrace = 2 * time.Second

	// abortDrain uses a short fresh grace context (not abortCtx, which may
	// already be done) to give goroutines a brief cooperative window before
	// we bail. Used on the abort path only.
	abortDrain := func() bool {
		graceCtx, graceCancel := context.WithTimeout(context.Background(), abortDrainGrace)
		defer graceCancel()
		return waitOrAbort(graceCtx, &wg)
	}

	// successDrain waits for goroutines on the HAPPY path using the caller's
	// ctx. Post-signal work (storage save, `e.pushStepEventToCoord` push
	// for per-step narration, coordinator synthesis prep) can legitimately
	// take many seconds on real providers. Using the caller's ctx - rather
	// than a fresh short grace - means we wait patiently unless the user's
	// own deadline has expired. This avoids the regression where workflows
	// with a single fast step still got marked StatusPartial because the
	// coordinator's per-step narration LLM call didn't finish in 2 seconds.
	successDrain := func() bool {
		return waitOrAbort(ctx, &wg)
	}

	// activeAtAbort captures the snapshot of in-flight step IDs at the
	// moment abort is detected, BEFORE abortDrain waits for the step
	// goroutines to finish their cleanup. Without this snapshot
	// flushMailboxOnAbort below would call e.ActiveSteps AFTER each
	// step's deferred unregisterAgentState had already run, returning an
	// empty slice - and the abort-flush path would silently
	// no-op despite messages still sitting in the mailbox. We snapshot
	// at abort-detect time so the flush sees exactly the set of steps
	// that were running when cancellation fired.
	var activeAtAbort []string

	// Main loop: wait for goroutines to complete and dispatch newly ready steps.
	for remaining > 0 {
		select {
		case <-abortCtx.Done():
			activeAtAbort = e.ActiveSteps()
			// S4 (B2 fix): mark router cancelled BEFORE abortDrain so any
			// runStep defers that fire during the drain attribute pending
			// mailbox drops to DropReasonWorkflowCancelled (instead of the
			// generic DropReasonTargetTerminal). Otherwise the per-step
			// Router.Close inside runStep's defer chain wins the race and
			// emits "target-terminal" while the workflow is being torn down.
			if e.Router != nil {
				e.Router.MarkWorkflowCancelled()
			}
			if !abortDrain() {
				aborted = true
			}
			goto done_label
		case <-done:
			mu.Lock()
			dispatch()
			remaining = countRemaining()
			mu.Unlock()
		}
	}

	// On the success path (remaining == 0), goroutines have already signalled
	// completion via the done channel. Wait patiently for their post-signal
	// work (storage, coordinator narration) to finish unless the user's
	// ctx has expired.
	if !successDrain() {
		aborted = true
		// Capture active set before goroutines unregister so the
		// abort-flush path below can find pending mailbox messages.
		if activeAtAbort == nil {
			activeAtAbort = e.ActiveSteps()
		}
		// S4 (B2 fix): mark router cancelled so any in-flight runStep
		// defers that drain the mailbox attribute drops to
		// DropReasonWorkflowCancelled.
		if e.Router != nil {
			e.Router.MarkWorkflowCancelled()
		}
	}

done_label:
	// on the abort path the per-step 3-invariant wait
	// (deferred inside runStep) cannot complete naturally - the workflow
	// ctx is already done, and ctx-respecting waiters return ctx.Err
	// without inspecting the mailbox. Flush every active step's mailbox
	// here, emitting EventMessageDropped per pending message so the
	// "zero silent drops" guarantee holds even on cancellation.
	if mailbox != nil && (aborted || abortCtx.Err() != nil) {
		// activeAtAbort is captured at abort-detect time (line 867 on
		// abortCtx.Done, line 897 on successDrain failure). e.ActiveSteps
		// returns a non-nil (possibly empty) slice, so activeAtAbort is
		// always non-nil whenever we enter this block (we only get here
		// when aborted=true OR abortCtx.Err != nil - both paths set
		// activeAtAbort first).
		toFlush := activeAtAbort
		// S4: mark router as cancelled so any in-flight Send returns
		// with DropReasonWorkflowCancelled (instead of racing to land in
		// a mailbox that's about to be flushed).
		if e.Router != nil {
			e.Router.MarkWorkflowCancelled()
		}
		flushMailboxOnAbort(ctx, runID, toFlush, mailbox, e.Progress, router.DropReasonWorkflowCancelled)
	}

	// Snapshot results and totals under lock so orphaned step goroutines
	// (in the aborted case) cannot mutate the caller-visible WorkflowResult
	// after we return. See waitOrAbort for background.
	mu.Lock()
	finalResults := make(map[string]*StepResult, len(order))
	for k, v := range results {
		finalResults[k] = v
	}
	if aborted {
		// Mark any in-flight steps as cancelled in the snapshot. The stuck
		// goroutine may later write its own StepResult to the original map,
		// but the caller sees this cancelled entry in finalResults.
		for stepID := range running {
			if existing, ok := finalResults[stepID]; !ok || existing == nil {
				finalResults[stepID] = &StepResult{ID: stepID, Status: spec.StepCancelled, Error: abortCtx.Err()}
			}
		}
	}
	// Mark any steps that never ran (e.g., after abort) as cancelled.
	// Without this, unstarted steps would be absent from WorkflowResult.Steps.
	for _, s := range e.Workflow.Steps {
		if _, ok := finalResults[s.ID]; !ok {
			finalResults[s.ID] = &StepResult{ID: s.ID, Status: spec.StepCancelled}
		}
	}
	finalTokens := totalTokens
	_ = failed // status is now derived from step results (P7.7.12)
	mu.Unlock()

	result := &WorkflowResult{
		RunID:    runID,
		Steps:    finalResults,
		Tokens:   finalTokens,
		Duration: time.Since(start),
	}

	// P7.7.12: Derive status from actual step results, not from the aborted flag.
	// Previously, `aborted` (drain timeout from coordinator narration) could
	// force StatusPartial even when all steps completed successfully. This
	// caused G8/gemini and G8/azure-gpt5 to exit=1 despite all steps passing.
	hasCompleted := false
	hasFailed := false
	for _, sr := range result.Steps {
		// sr is guaranteed non-nil: lines 681-687 fill all missing steps with StepCancelled.
		switch sr.Status {
		case spec.StepCompleted:
			hasCompleted = true
		case spec.StepFailed, spec.StepCancelled:
			hasFailed = true
		}
	}
	switch {
	case hasFailed && hasCompleted:
		result.Status = spec.StatusPartial
	case hasFailed:
		result.Status = spec.StatusFailed
	default:
		result.Status = spec.StatusCompleted
	}

	// Persist final run status.
	if e.Storage != nil {
		finalRun := &Run{ID: runID, Workflow: e.Workflow, Status: result.Status, Steps: finalResults}
		if sErr := e.Storage.SaveRun(ctx, finalRun); sErr != nil {
			storageErr := fmt.Errorf("run %q: save final run: %w", runID, sErr)
			if e.Progress != nil {
				e.Progress.OnEvent(ctx, Event{Type: types.EventError, Timestamp: time.Now(), RunID: runID, Error: storageErr})
			} else {
				slog.WarnContext(ctx, "save final run failed",
					"err", storageErr,
					"run_id", runID,
				)
			}
		}
	}

	// Persist shared memory state.
	if e.Storage != nil && e.SharedMem != nil {
		entries := e.SharedMem.Entries()
		if len(entries) > 0 {
			if sErr := e.Storage.SaveSharedMemory(ctx, runID, entries); sErr != nil {
				storageErr := fmt.Errorf("run %q: save shared memory: %w", runID, sErr)
				if e.Progress != nil {
					e.Progress.OnEvent(ctx, Event{Type: types.EventError, Timestamp: time.Now(), RunID: runID, Error: storageErr})
				} else {
					slog.WarnContext(ctx, "save shared memory failed",
						"err", storageErr,
						"run_id", runID,
					)
				}
			}
		}
	}

	// the legacy synchronous coord-synthesis LLM call is removed
	// in this refactor. The new model is
	// caller-driven: the coord runner observes its mailbox via Run loop
	// and emits its own synthesis via the finalize tool. The
	// executor only pushes a terminal WorkflowEnd-equivalent event into
	// the mailbox so the coord can react. Token aggregation also moves
	// to the caller (the runner reports its own goai usage on
	// Run return).
	// same suppression for workflow-end (paired with the
	// workflow-start suppression above). Nested miniWF end events
	// are internal plumbing.
	if e.Coordinator != nil && e.Coordinator.mailbox != nil && e.namespacePrefix == "" {
		// (Fix 8): wrap the workflow-end Append in
		// recover so a panicking Mailbox.Append (e.g. user-supplied
		// store with a defect) cannot crash RunFlow after the DAG has
		// already completed. Symmetrical with the per-step coord-notify
		// recover at the Run-loop callsite.
		// Evidence: TestWorkflowEndAppend_RecoverPanics.
		func() {
			defer func() {
				if r := recover(); r != nil {
					var appendErr error
					if e2, ok := r.(error); ok {
						appendErr = fmt.Errorf("workflow-end coord append panic: %w", e2)
					} else {
						appendErr = fmt.Errorf("workflow-end coord append panic: %v", r)
					}
					if e.Progress != nil {
						e.Progress.OnEvent(ctx, Event{
							Type: types.EventError, Timestamp: time.Now(),
							RunID: runID, Error: appendErr,
						})
					} else {
						slog.WarnContext(ctx, "workflow-end coord append panic recovered",
							"err", appendErr,
							"run_id", runID,
						)
					}
				}
			}()
			coordID := coordStepID(e.Coordinator)
			if _, err := e.Coordinator.mailbox.Append(coordID, RouterMessage{
				From:      "executor",
				Type:      router.MessageInfo,
				Content:   fmt.Sprintf("workflow %q finished status=%s steps=%d", e.Workflow.Name, result.Status, len(result.Steps)),
				Timestamp: time.Now(),
				Metadata: map[string]string{
					"event_type": string(types.EventWorkflowEnd),
					"run_id":     runID,
					"status":     string(result.Status),
				},
			}); err != nil {
				slog.WarnContext(ctx, "mailbox append failed", "err", err, "site", "workflow-end", "run_id", runID)
			}
			// signal Wake so the coord's goai loop re-enters
			// and observes the terminal workflow_end event (and any
			// preceding events still buffered in the mailbox).
			signalCoordWake(e.Coordinator)
		}()
	}

	// ZF8.0a - Event ordering contract. WorkflowEnd is terminal per
	// run: all ResumeCompleted / ResumeFailed / TranscriptSealed /
	// CoordinatorInboxMessage events MUST drain before it. The outer
	// defer (line ~333) also waits on resumeWG with resumeShutdownTimeout,
	// but waiting AFTER WorkflowEnd would let resume events fire
	// post-terminal. Wait here first (bounded by resumeShutdownTimeout)
	// and drain the coordinator inbox once more. Cancelling runCtx is
	// deferred until after the normal return so resume goroutines
	// observe shutdown in the same sequence they did before.
	e.drainBeforeWorkflowEnd(ctx, runID)

	if e.Progress != nil {
		e.Progress.OnEvent(ctx, Event{
			Type:      types.EventWorkflowEnd,
			Timestamp: time.Now(),
			RunID:     runID,
			Duration:  result.Duration,
			Tokens:    &result.Tokens,
		})
	}

	return result, nil
}

// EvalContext provides variables available to CEL expressions.
type EvalContext struct {
	Content   string                      `json:"content"`
	Result    map[string]any              `json:"result"`
	Status    string                      `json:"status"`
	Steps     map[string]*EvalStepContext `json:"steps"`
	Iteration int                         `json:"iteration"`
	Item      any                         `json:"item"`
	Index     int                         `json:"index"`
}

// EvalStepContext holds per-step data for CEL expression evaluation.
type EvalStepContext struct {
	Content string         `json:"content"`
	Result  map[string]any `json:"result"`
	Status  string         `json:"status"`
}
