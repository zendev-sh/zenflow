package exec

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/resume"
	"github.com/zendev-sh/zenflow/internal/spec"
	"github.com/zendev-sh/zenflow/internal/types"
)

// ErrOrchestratorClosed is returned by RunAgentAsync (and related
// lifecycle methods) when the caller has already invoked Close on the
// Orchestrator. A closed orchestrator does not accept new work - the
// caller must construct a fresh one (or, when using the factory cache,
// remove the sessionID entry so the next call re-creates it).
var ErrOrchestratorClosed = errors.New("zenflow: orchestrator closed")

// Orchestrator coordinates workflow execution. Stable.
type Orchestrator struct {
	model       provider.LanguageModel
	tools       []goai.Tool
	goaiOpts    []goai.Option
	storage     Storage
	permissions PermissionHandler
	approval    ApprovalHandler
	progress    ProgressSink
	// coordinator, when non-nil, is the caller-provided AgentRunner that
	// receives workflow lifecycle events as RouterMessages on its
	// Mailbox. The orchestrator never starts or stops the runner
	// lifecycle is the caller's responsibility.
	coordinator *AgentRunner
	// router is the orchestrator-owned MessageRouter, allocated in New
	// after options apply when a coordinator is installed. Holding the
	// Router on the orchestrator (rather than allocating per-RunFlow
	// inside Executor.Run) lets us wire `coordinator.Router` and
	// `coordinator.Progress` synchronously at construction time, BEFORE
	// the consumer's coord goroutine can
	// observe them. Without this synchronous wiring, the executor's
	// in-Run wiring raced with BuildCoordStepMenu / Run reads from
	// the consumer's coord goroutine (finding, 2026-05-04).
	router          *MessageRouter
	sharedMem       *SharedMemory
	tracer          Tracer
	isolation       StepIsolation
	outputTransform OutputTransformer
	defaultModel    string
	// forceModel, when non-empty, takes precedence over both Step.Model
	// and AgentConfig.Model when resolving the effective model identifier
	// for any step or agent the orchestrator runs. Set via WithForceModel
	// to support cross-provider CLI overrides (e.g. `zenflow flow --model
	// X` running every agent through provider X regardless of YAML).
	forceModel      string
	maxConcurrency  int // see defaultMaxConcurrency in executor.go for precedence
	maxTurns        int
	maxDepth        int
	streaming       bool
	verbose         bool

	// Day-1 Options API surface. All zero values = "use compile-time
	// defaults" so unconfigured callers retain default behavior.
	maxWakeCycles          int             // AgentRunner per-step cap; 0 = defaultMaxWakeCycles
	holdTimeout            time.Duration   // 0 = defaultHoldTimeout (30s)
	agentHandleTTL         time.Duration   // RunAgentAsync handle TTL; 0 = DefaultAgentHandleTTL
	dropCallback           func(DropEvent) // user observer; called once per drop
	dropCallbackBufferSize int             // queue size for dropCallback dispatch; 0 = synchronous
	maxMailboxSize         int             // bounded mailbox; 0 = unbounded (only when explicitly set via WithMaxMailboxSize(0))
	maxMailboxSizeSet      bool            // tracks whether WithMaxMailboxSize was applied so New() can install DefaultMaxMailboxSize without overriding an explicit zero opt-out
	mailboxStoreFactory    func() MailboxStore
	mailboxDeliveryEnabled *bool // nil = enabled (default true)
	engineClock            EngineClock
	progressBufferSize     int // pump buffer; 0 = defaultEventBusBuffer

	// Transcript store factory + caps. Factory is invoked per-Run
	// (see applyExecutorOptions) so each Run gets a fresh store.
	// Caps apply only to the default InMemoryTranscriptStore;
	// custom stores manage their own limits.
	transcriptStoreFactory func() resume.TranscriptStore
	maxTranscriptMessages  int
	maxTranscriptBytes     int64

	// externalInboxes: non-step sender identities (e.g. "coordinator")
	// to pre-register with the Router so reverse-routed messages from
	// resumed steps land instead of dropping.
	externalInboxes []string

	// modelResolver is consulted by Executor.runResume when the saved
	// transcript carries a non-empty Model identifier that differs
	// from the Executor's default runner model. Without a resolver AND
	// in the face of a mismatch, resume fails loudly with
	// ErrModelResolverMissing.
	modelResolver ModelResolver

	// truncateOnCapReached: when true, ResumeStep falls back to
	// LoadTruncated after a sealed-slot Load to keep the step
	// resumable. Default false - sealed transcripts surface
	// DropReasonTranscriptTooLarge for safety. VA-3b.
	truncateOnCapReached bool

	// runID overrides the auto-generated run identifier for RunFlow /
	// RunAgent when set. Callers that allocate a runID
	// up-front for DB rows and HTTP responses supply it here so the
	// orchestrator emits events carrying the same ID. Empty = generate
	// internally (legacy behavior). Applied by RunFlow and RunAgent;
	// ResumeFlow already takes a runID argument and ignores this field.
	// RunGoal still allocates its own goalRunID when this is empty.
	runID string

	// handleRegistry indexes active RunAgentAsync handles by
	// SessionID. ListAgentHandles consults it to report live work
	// for a given session (the session-status query for the
	// consumer's session model). Populated when RunAgentAsync spawns
	// a handle; pruned when the handle's Done channel closes.
	// Protected by handleMu.
	handleMu       sync.Mutex
	handleRegistry map[string][]*AgentHandle

	// closeOnce guards Close so it is idempotent - concurrent or
	// repeated calls only run cleanup once. Once set, new
	// RunAgentAsync invocations are rejected with ErrOrchestratorClosed
	// so a stale factory cache entry cannot leak new goroutines after
	// the owning session has been torn down.
	closeOnce sync.Once
	closed    atomic.Bool

	// routerObserver is the optional hook installed via
	// WithRouterObserver. RunAgent invokes it once with the freshly
	// allocated per-call MessageRouter so observers (telemetry, tests)
	// can hold a handle without polling internal state. nil = no-op.
	routerObserver func(*MessageRouter)
}

// DefaultMaxBytesPerDep is the per-dependency content cap applied by the
// default OutputTransform (TokenBudgetTransformer). 8 KiB ≈ 2 K tokens - 
// safe for tight context windows while still allowing larger windows to
// work (truncation is a ceiling, not a floor). Callers that want a
// different cap install WithOutputTransform with their own transformer.
const DefaultMaxBytesPerDep = 8 * 1024

// DefaultMaxMailboxSize is the per-step mailbox cap installed by New
// when the caller does not supply WithMaxMailboxSize. The default keeps
// pathological producers from exhausting memory in long-running
// workflows; callers that need an unbounded queue must opt out
// explicitly via WithMaxMailboxSize(0).
const DefaultMaxMailboxSize = 10000

// New creates an Orchestrator with the given options. Stable.
// Defaults: MemoryStorage, maxConcurrency=5,
// OutputTransform=&TokenBudgetTransformer{MaxBytesPerDep: DefaultMaxBytesPerDep}.
// Effective defaults at call time: maxTurns=50, maxDepth=3.
// Use WithModel, WithTools, WithMaxTurns, etc. to configure.
func New(opts ...Option) *Orchestrator {
	o := &Orchestrator{
		storage:        NewMemoryStorage(),
		maxConcurrency: 5,
	}
	for _, opt := range opts {
		opt(o)
	}
	// Library default: install a TokenBudgetTransformer with the standard
	// per-dep cap unless the caller already installed an explicit transform
	// via WithOutputTransform. Day-1 callers (and the OSS CLI) get safe
	// truncation out of the box; advanced callers retain full control.
	if o.outputTransform == nil {
		o.outputTransform = &TokenBudgetTransformer{MaxBytesPerDep: DefaultMaxBytesPerDep}
	}
	// Library default: install DefaultMaxMailboxSize when the caller did
	// not invoke WithMaxMailboxSize. The maxMailboxSizeSet flag preserves
	// the documented "explicit zero = unbounded opt-out" semantics:
	// WithMaxMailboxSize(0) flips the flag without changing the field
	// value, and this branch then leaves the zero in place.
	if !o.maxMailboxSizeSet {
		o.maxMailboxSize = DefaultMaxMailboxSize
	}
	// Synchronously wire the coord runner's Router + Progress
	// dependencies BEFORE returning. The consumer spawns the coord Run
	// goroutine immediately after newOrch and before RunFlow; that
	// goroutine reads runner.Router (in BuildCoordStepMenu) and
	// runner.Progress (in AgentRunner.Run). Doing the wiring here
	// (synchronously, before the consumer can spawn) puts a
	// happens-before edge between the writes and the reads.
	if o.coordinator != nil {
		if o.router == nil {
			o.router = NewMessageRouter()
		}
		if o.coordinator.router == nil {
			o.coordinator.router = o.router
		}
		if o.coordinator.progress == nil {
			o.coordinator.progress = o.progress
		}
	}
	return o
}

// Progress returns the orchestrator's configured ProgressSink, or nil
// if none was installed via WithProgress. Exposed so consumer code that
// owns the coord goroutine lifecycle can wire its own runners
// to the same sink without going through WithCoordinator.
func (o *Orchestrator) Progress() ProgressSink {
	if o == nil {
		return nil
	}
	return o.progress
}

// truncate returns s truncated to at most maxLen runes, including "..." suffix.
func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

// addUsage accumulates all fields of provider.Usage from src into dst.
func addUsage(dst *provider.Usage, src provider.Usage) {
	dst.InputTokens += src.InputTokens
	dst.OutputTokens += src.OutputTokens
	dst.TotalTokens += src.TotalTokens
	dst.ReasoningTokens += src.ReasoningTokens
	dst.CacheReadTokens += src.CacheReadTokens
	dst.CacheWriteTokens += src.CacheWriteTokens
}

// DefaultModel returns the model identifier this orchestrator was
// configured with via WithDefaultModel. Used by FactoryCache callers
// to detect when a cached orchestrator's model is stale relative to
// the session's current selection (user switched via /models in TUI),
// which is otherwise undetectable until subagents fire with the wrong
// default. Returns the empty string when WithDefaultModel was never
// applied (or when the receiver is nil).
func (o *Orchestrator) DefaultModel() string {
	if o == nil {
		return ""
	}
	return o.defaultModel
}

// HasLLM reports whether an LLM provider has been configured.
func (o *Orchestrator) HasLLM() bool {
	return o.model != nil
}

// RunAgent executes a single agent conversation with optional child
// agent spawning. The cfg argument carries every per-call override:
// cfg.Prompt is the user message; cfg.Model overrides the Orchestrator
// default; cfg.CallTools overrides the Orchestrator tool set;
// cfg.ProgressSink overrides the Orchestrator sink; cfg.MaxTurns bounds
// the per-call tool loop. All fields except Prompt are optional and
// fall back to the Orchestrator defaults (registered via WithModel /
// WithTools / WithProgress / WithMaxTurns) when unset. Stable.
func (o *Orchestrator) RunAgent(ctx context.Context, cfg AgentConfig) (*AgentResult, error) {
	// Symmetric with RunAgentAsync: once Close has been called, the
	// orchestrator's background goroutines are cancelled and its
	// handle registry has been drained; a new RunAgent would be
	// unobservable by ListAgentHandles and would leak resources with
	// no lifecycle to attach to. Reject early.
	if o.closed.Load() {
		return nil, ErrOrchestratorClosed
	}
	if o.model == nil {
		return nil, ErrModelRequired
	}

	// Generate run ID for tracing, or use caller-supplied ID when set.
	runID := o.runID
	if runID == "" {
		var err error
		runID, err = GenerateRunID()
		if err != nil {
			return nil, err
		}
	}

	// Start agent trace span. Use defer to ensure EndSpan fires even if
	// childWg.Wait blocks or panics.
	if o.tracer != nil {
		ctx = o.tracer.StartSpan(ctx, "zenflow.agent", map[string]string{
			"zenflow.run_id":       runID,
			"zenflow.agent.prompt": truncate(cfg.Prompt, 200),
		})
	}
	var agentErr error
	defer func() {
		if o.tracer != nil {
			o.tracer.EndSpan(ctx, agentErr)
		}
	}()

	start := time.Now()
	// Standalone RunAgent plumbs a per-call MessageRouter and a fresh
	// in-memory MailboxStore into both the AgentRunner and any
	// agent-tool-spawned children. This unlocks inter-agent messaging
	// on the RunAgent path. The mailbox is per-call - successive
	// RunAgent invocations get distinct mailbox instances so inbox
	// state cannot leak between calls.
	router := NewMessageRouter()
	if o.routerObserver != nil {
 // Observer panics MUST NOT crash the agent run. Telemetry/debug
 // hooks installed in production are the most likely source of
 // unexpected panics; recover here so a buggy hook degrades to a
 // logged warning instead of taking down the whole RunAgent
 // invocation.
		func() {
			defer func() {
				if r := recover(); r != nil {
 // Warn, not Error: graceful degradation means the
 // agent run continues normally; only the observer's
 // side effects are lost.
					slog.Warn("panic in WithRouterObserver callback",
						"hook", "routerObserver",
						"panic", r,
					)
				}
			}()
			o.routerObserver(router)
		}()
	}
	mailbox := NewInMemoryMailboxStore()
	router.SetMailbox(mailbox)
	primaryStepID := agentPrimaryStepID(runID)
	router.RegisterStep(primaryStepID)
	router.RegisterInbox(primaryStepID)
	defer func() {
 // Close the primary inbox so the per-call mailbox does not
 // retain dangling open-step state across calls.
		router.Close(primaryStepID)
	}()

	maxDepth := o.maxDepth
	if maxDepth <= 0 {
		maxDepth = 3
	}

	// Per-call MaxTurns overrides the Orchestrator default; both
	// fall back to defaultMaxTurns if unset.
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = o.maxTurns
	}
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}

	// Resolve effective tool set: per-call CallTools override the
	// Orchestrator-level tools entirely when non-empty, matching the
	// spec example in §5.2.1.a (env.SubagentToolSet).
	effectiveTools := o.tools
	if len(cfg.CallTools) > 0 {
		effectiveTools = cfg.CallTools
	}

	// Resolve effective model. Precedence (high → low): forceModel
	// (WithForceModel CLI override), per-call cfg.Model, orchestrator
	// defaultModel. cmp.Or returns the first non-zero argument.
	effectiveModel := cmp.Or(o.forceModel, cfg.Model, o.defaultModel)

	// Resolve effective progress sink: per-call ProgressSink
	// overrides the Orchestrator sink when non-nil (the consumer's
	// progress bridge landing here).
	effectiveProgress := o.progress
	if cfg.ProgressSink != nil {
		effectiveProgress = cfg.ProgressSink
	}

	// Collect parent tool names for subset-checking (#3).
	parentToolNames := make([]string, 0, len(effectiveTools))
	for _, t := range effectiveTools {
		parentToolNames = append(parentToolNames, t.Name)
	}

	sp := &agentSpawner{
		Model:        o.model,
		Tools:        effectiveTools,
		GoAIOptions:  o.goaiOpts,
		Permissions:  o.permissions,
		Progress:     effectiveProgress,
		Router:       router,
		DefaultModel: effectiveModel,
		MaxDepth:     maxDepth,
		MaxTurns:     maxTurns,
		ParentTools:  parentToolNames,
	}

	// Per-call router/mailbox are populated on the runner so the
	// runner observes inter-agent messages addressed to primaryStepID.
	// Wake gives the runner a wake channel symmetric to the executor's
	// per-step wiring; for the standalone RunAgent path no
	// DeliveryEngine watches the mailbox, but the channel is allocated
	// so the runner's mailbox-mode predicates (Mailbox+Wake non-nil)
	// can engage if a future engine is introduced.
	wake := make(chan struct{}, 1)
	runnerOpts := []AgentRunnerOption{
		WithRunnerModel(o.model),
		WithRunnerTools(effectiveTools...),
		WithRunnerPermissions(o.permissions),
		WithRunnerProgress(effectiveProgress),
		WithRunnerGoAIOptions(o.goaiOpts...),
		WithRunnerRunID(runID),
		WithRunnerStepID(primaryStepID),
		WithRunnerMailbox(mailbox),
		WithRunnerWake(wake),
		WithRunnerRouter(router),
	}
	if o.streaming {
		runnerOpts = append(runnerOpts, WithRunnerStreaming())
	}
	if o.verbose {
		runnerOpts = append(runnerOpts, WithRunnerVerbose())
	}
	runner := NewAgentRunner(runnerOpts...)
	runner.spawner = sp

	// runCfg is the AgentConfig threaded into AgentRunner.Run. Note: the
	// YAML-declared `Tools []string` field (catalog-lookup names) is
	// NOT forwarded here - it is only consumed by the workflow executor
	// (`executor.go::FilterTools(e.Runner.Tools, agent.Tools, …)`) when
	// resolving a workflow-declared agent's tool set. On the RunAgent
	// path the effective tool slice is already resolved above
	// (`effectiveTools`, from `cfg.CallTools || o.tools`) and passed
	// directly to `runner.Run`; forwarding `cfg.Tools` here would be
	// silent dead code. `DisallowedTools` is likewise a YAML workflow
	// knob applied by the executor, not the runner.
	runCfg := AgentConfig{
		Name:            cfg.Name,
		Description:     cfg.Description,
		Prompt:          cfg.Prompt,
		Model:           effectiveModel,
		MaxTurns:        maxTurns,
		Temperature:     cfg.Temperature,
		TopP:            cfg.TopP,
		ResultSchema:    cfg.ResultSchema,
		CallTools:       cfg.CallTools,
		ProgressSink:    cfg.ProgressSink,
		SubagentToolSet: cfg.SubagentToolSet,
		SessionID:       cfg.SessionID,
	}

	// Get all available tools plus the agent tool for child spawning.
	tools := make([]goai.Tool, len(effectiveTools))
	copy(tools, effectiveTools)
	tools = append(tools, AgentToolDef())

	result, err := runner.Run(ctx, runCfg, cfg.Prompt, effectiveModel, tools, cfg.Attachments...)
	if err != nil {
		agentErr = err
 // Drain async children even on the parent-error path. They share
 // the same ctx that caused the parent to fail, so they exit
 // promptly via ctx.Err; waiting here makes sure their final
 // writes to sp.children / sp.childErrors land before RunAgent
 // returns to the caller.
		sp.childWg.Wait()
		return nil, err
	}

	// Wait for async children.
	sp.childWg.Wait()

	// Collect children and aggregate tokens.
	sp.mu.Lock()
	children := sp.children
	sp.mu.Unlock()

	totalTokens := result.Tokens
	for _, child := range children {
		addUsage(&totalTokens, child.Tokens)
	}

	return &AgentResult{
		Content:  result.Content,
		Result:   result.Result,
		Tokens:   totalTokens,
		Turns:    result.Turns,
		Status:   result.Status,
		Duration: time.Since(start),
	}, nil
}

// RunFlow executes a workflow and returns the result. Stable.
// When WithRunID was supplied at construction time, that ID is reused; otherwise
// a fresh one is generated.
// Variadic RunFlowOption args configure per-call behavior such as
// WithFlowContext. Backward-compatible: zero opts = previous behavior.
func (o *Orchestrator) RunFlow(ctx context.Context, wf *Workflow, opts ...RunFlowOption) (*WorkflowResult, error) {
	cfg := runFlowConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	runID := o.runID
	if runID == "" {
		var err error
		runID, err = GenerateRunID()
		if err != nil {
			return nil, err
		}
	}
	// Mirror RunGoal: emit plan_ready so sinks (CLI --plan, TUI DAG card)
	// can render the DAG before execution. RunGoal emits its own
	// plan_ready before calling runFlowWithID, so direct `/flow` runs
	// would otherwise never produce the event. ux.md §9.B requires the
	// DAG card to appear for every /flow run, not only on first cold start.
	if wf != nil && o.progress != nil {
 // Recover panics from a buggy user-supplied ProgressSink. This
 // OnEvent fires BEFORE wrapProgressNonBlocking takes effect
 // (executor.go), so a panic here would crash the RunFlow goroutine
 // entirely. The pump's recover only protects events emitted from
 // inside Executor.Run.
		emitPlanReady(ctx, o.progress, Event{
			Type:    types.EventPlanReady,
			RunID:   runID,
			Message: wf.Name,
			Data:    map[string]any{"workflow": wf},
		})
	}
	return o.runFlowWithID(ctx, wf, runID, cfg)
}

// emitPlanReady wraps a single ProgressSink.OnEvent call in a
// recover guard. Used by RunFlow + RunGoal for the pre-executor
// plan_ready emit, which fires before wrapProgressNonBlocking
// installs its pump-level recover.
func emitPlanReady(ctx context.Context, sink ProgressSink, ev Event) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("panic in ProgressSink.OnEvent (plan_ready)",
				"hook", "progressSink",
				"panic", r,
			)
		}
	}()
	sink.OnEvent(ctx, ev)
}

// runFlowFn is a test seam for the runFlowWithID call inside RunGoal.
// In production it always points to (*Orchestrator).runFlowWithID; tests may
// override it to inject an error at exactly that call-site.
var runFlowFn = (*Orchestrator).runFlowWithID

// runFlowWithID is the internal implementation shared by RunFlow and RunGoal.
// RunGoal passes its own run ID so both goal and flow spans share the same ID.
func (o *Orchestrator) runFlowWithID(ctx context.Context, wf *Workflow, runID string, cfg runFlowConfig) (*WorkflowResult, error) {
	if wf == nil {
		return nil, ErrWorkflowNil
	}
	if o.model == nil {
		return nil, ErrModelRequired
	}

	// Start flow trace span at Orchestrator level.
	if o.tracer != nil {
		ctx = o.tracer.StartSpan(ctx, "zenflow.flow", map[string]string{
			"zenflow.run_id":        runID,
			"zenflow.workflow.name": wf.Name,
		})
	}

	flowRunnerOpts := []AgentRunnerOption{
		WithRunnerModel(o.model),
		WithRunnerTools(o.tools...),
		WithRunnerPermissions(o.permissions),
		WithRunnerProgress(o.progress),
		WithRunnerGoAIOptions(o.goaiOpts...),
		WithRunnerRunID(runID),
	}
	if o.streaming {
		flowRunnerOpts = append(flowRunnerOpts, WithRunnerStreaming())
	}
	if o.verbose {
		flowRunnerOpts = append(flowRunnerOpts, WithRunnerVerbose())
	}
	runner := NewAgentRunner(flowRunnerOpts...)

	// Resolve the per-Run Router up front. Two cases:
	// - WithCoordinator was used: o.router was allocated in New so
	// the coord runner could be wired synchronously. Reuse that
	// instance here so coord and executor see the same Router.
	// - No coordinator: allocate a fresh Router for this Run. The
	// pre-allocation (vs leaving Executor.Run to do it lazily) is
	// needed so WithRouterObserver fires before any Run-side
	// goroutine consumes the router, matching the option's docstring
	// contract ("invoked once per RunAgent / RunFlow").
	router := o.router
	if router == nil {
		router = NewMessageRouter()
	}
	if o.routerObserver != nil {
 // Observer panics MUST NOT crash the run. Telemetry / debug
 // hooks installed in production are the most likely panic
 // source; recover so a buggy hook degrades to a logged warning
 // instead of taking down the whole RunFlow invocation.
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("panic in WithRouterObserver callback",
						"hook", "routerObserver",
						"entry", "RunFlow",
						"panic", r,
					)
				}
			}()
			o.routerObserver(router)
		}()
	}

	exec := &Executor{
		Runner:          runner,
		Storage:         o.storage,
		Progress:        o.progress,
		Workflow:        wf,
		DefaultModel:    o.defaultModel,
		ForceModel:      o.forceModel,
		MaxConcurrency:  o.maxConcurrency,
		SharedMem:       o.sharedMem,
		Tracer:          o.tracer,
		Isolation:       o.isolation,
		Coordinator:     o.coordinatorRunner(),
		OutputTransform: o.outputTransform,
		RunID:           runID,
		FlowContext:     cfg.flowContext,
		Router:          router,
	}
	o.applyExecutorOptions(exec)

	result, err := exec.Run(ctx)
	if o.tracer != nil {
		var traceErr error
		if err != nil {
			traceErr = err
		} else if result != nil && (result.Status == spec.StatusFailed || result.Status == spec.StatusPartial) {
			traceErr = fmt.Errorf("workflow %q: %s", wf.Name, result.Status)
		}
		o.tracer.EndSpan(ctx, traceErr)
	}
	return result, err
}

// coordinatorRunner returns the caller-provided coordinator AgentRunner,
// or nil when none was installed via WithCoordinator.
func (o *Orchestrator) coordinatorRunner() *AgentRunner {
	return o.coordinator
}

// Coordinator returns the caller-provided coordinator AgentRunner, or
// nil when none was installed via WithCoordinator. Public test seam
// the standalone CLI in cmd/zenflow needs to start the coord's
// Run loop after constructing the orchestrator (per caller-owned
// lifecycle), and assertions need to observe whether
// `--quiet`/`--json`/`--summary-only`/default routed to nil-coord vs a
// real default coord vs a SynthesizeOnly coord. Mirrors the
// MaxDepth public accessor pattern.
// Returns nil when called on a nil receiver.
func (o *Orchestrator) Coordinator() *AgentRunner {
	if o == nil {
		return nil
	}
	return o.coordinator
}

// ResumeFlow resumes a previously started workflow from its checkpoint. Stable.
// Completed steps are loaded from storage and not re-executed.
// Failed, cancelled, and skipped steps are re-executed.
func (o *Orchestrator) ResumeFlow(ctx context.Context, runID string, wf *Workflow) (*WorkflowResult, error) {
	if wf == nil {
		return nil, ErrWorkflowNil
	}
	if o.storage == nil {
		return nil, ErrStorageRequired
	}

	// Start flow trace span for resume (reuses existing run ID).
	if o.tracer != nil {
		ctx = o.tracer.StartSpan(ctx, "zenflow.flow", map[string]string{
			"zenflow.run_id":        runID,
			"zenflow.workflow.name": wf.Name,
			"zenflow.resume":        "true",
		})
	}

	// Load previous run to validate it exists.
	if _, err := o.storage.LoadRun(ctx, runID); err != nil {
		if o.tracer != nil {
			o.tracer.EndSpan(ctx, err)
		}
		return nil, fmt.Errorf("resume: %w", err)
	}

	// Load completed step results from storage.
	completedSteps := make(map[string]*StepResult, len(wf.Steps))
	for _, step := range wf.Steps {
		sr, err := o.storage.LoadStepResult(ctx, runID, step.ID)
		if err != nil {
			continue // Step not found = needs to run.
		}
		if sr.Status == spec.StepCompleted {
			completedSteps[step.ID] = sr
		}
 // Failed/cancelled/skipped steps are re-executed.
	}

	// Start with orchestrator's shared memory (if configured), then overlay persisted entries.
	sm := o.sharedMem
	if sm == nil {
		sm = NewSharedMemory()
	}
	entries, err := o.storage.LoadSharedMemory(ctx, runID)
	if err != nil {
		if o.tracer != nil {
			o.tracer.EndSpan(ctx, err)
		}
		return nil, fmt.Errorf("resume: load shared memory: %w", err)
	}
	if len(entries) > 0 {
		sm.LoadEntries(entries)
	}

	flowRunnerOpts := []AgentRunnerOption{
		WithRunnerModel(o.model),
		WithRunnerTools(o.tools...),
		WithRunnerPermissions(o.permissions),
		WithRunnerProgress(o.progress),
		WithRunnerGoAIOptions(o.goaiOpts...),
		WithRunnerRunID(runID),
	}
	if o.streaming {
		flowRunnerOpts = append(flowRunnerOpts, WithRunnerStreaming())
	}
	if o.verbose {
		flowRunnerOpts = append(flowRunnerOpts, WithRunnerVerbose())
	}
	runner := NewAgentRunner(flowRunnerOpts...)

	// Same pre-allocation pattern as runFlowWithID - fire
	// WithRouterObserver before any Run-side goroutine consumes the
	// Router so observers see the same instance the executor uses.
	router := o.router
	if router == nil {
		router = NewMessageRouter()
	}
	if o.routerObserver != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("panic in WithRouterObserver callback",
						"hook", "routerObserver",
						"entry", "ResumeFlow",
						"panic", r,
					)
				}
			}()
			o.routerObserver(router)
		}()
	}

	exec := &Executor{
		Runner:          runner,
		Storage:         o.storage,
		Progress:        o.progress,
		Workflow:        wf,
		DefaultModel:    o.defaultModel,
		ForceModel:      o.forceModel,
		MaxConcurrency:  o.maxConcurrency,
		SharedMem:       sm,
		Tracer:          o.tracer,
		Isolation:       o.isolation,
		Coordinator:     o.coordinatorRunner(),
		OutputTransform: o.outputTransform,
		Router:          router,
		ResumeSteps:     completedSteps,
		ResumeRunID:     runID,
	}
	o.applyExecutorOptions(exec)

	result, err := exec.Run(ctx)
	if o.tracer != nil {
		var traceErr error
		if err != nil {
			traceErr = err
		} else if result != nil && (result.Status == spec.StatusFailed || result.Status == spec.StatusPartial) {
			traceErr = fmt.Errorf("workflow %q: %s", wf.Name, result.Status)
		}
		o.tracer.EndSpan(ctx, traceErr)
	}
	return result, err
}

// RunGoal decomposes a goal into steps via LLM coordinator, then executes as DAG. Stable.
// The coordinator receives the goal and a catalog of available tools, and outputs
// a JSON workflow (agents + steps). The workflow is parsed, validated, optionally
// approved via ApprovalHandler, then executed via RunFlow.
// Retry policy: up to 2 retries on JSON parse errors, 1 retry on validation errors.
// Retry budgets are global across the entire RunGoal call - not per-error-type.
// For example, after 2 JSON retries, a subsequent validation error will only get
// 1 retry regardless of prior JSON failures. The total number of coordinator LLM
// calls is bounded by 1 + maxJSONRetries + maxValidationRetries = 4.
func (o *Orchestrator) RunGoal(ctx context.Context, goal string, opts ...RunGoalOption) (result *WorkflowResult, err error) {
	// reject empty / whitespace-only goals early.
	if strings.TrimSpace(goal) == "" {
		return nil, ErrEmptyGoal
	}

	cfg := runGoalConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	// Generate run ID upfront - shared by both goal and flow spans.
	// Prefer caller-supplied WithRunID when set so server-visible and
	// emitted IDs match.
	goalRunID := o.runID
	if goalRunID == "" {
		var idErr error
		goalRunID, idErr = GenerateRunID()
		if idErr != nil {
			return nil, idErr
		}
	}

	// Start goal trace span at Orchestrator level.
	if o.tracer != nil {
		ctx = o.tracer.StartSpan(ctx, "zenflow.goal", map[string]string{
			"zenflow.run_id":    goalRunID,
			"zenflow.goal.text": truncate(goal, 200),
		})
		defer func() { o.tracer.EndSpan(ctx, err) }()
	}

	if o.model == nil {
		return nil, ErrModelRequired
	}

	// Build tool catalog from available tools.
	catalog := BuildToolCatalog(o.tools)

	// Build the coordinator prompt.: when WithGoalContext was
	// supplied, append the additional context as a clearly-labelled
	// section so the decomposition LLM can use it without parsing the
	// goal text for context cues.
	prompt := CoordinatorPrompt(goal, catalog)
	if cfg.goalContext != "" {
		prompt = prompt + "\n\n## Goal Context\n" + cfg.goalContext
	}

	// Retry loop: up to 3 total attempts (initial + 2 retries for JSON, 1 for validation).
	const maxJSONRetries = 2
	const maxValidationRetries = 1

	var (
		wf            *Workflow
		lastErr       error
		jsonRetries   int
		valRetries    int
		totalTokens   provider.Usage
		currentPrompt = prompt
	)

	for {
 // Race CoordinatorChat against ctx.Done. If the provider's DoGenerate
 // does not honor ctx (e.g., stuck in a network read), the goroutine is
 // orphaned but RunGoal still returns promptly so `zenflow goal --timeout`
 // exits on time. See.
		type chatOut struct {
			content string
			tokens  provider.Usage
			err     error
		}
		chatCh := make(chan chatOut, 1)
		go func(prompt string) {
 // Defensive recover: surface a panic inside CoordinatorChat /
 // CoordinatorStreamChat as a normal chatErr instead of crashing
 // RunGoal's caller.
			defer func() {
				if r := recover(); r != nil {
					var perr error
					if e, ok := r.(error); ok {
						perr = fmt.Errorf("coordinator panic: %w", e)
					} else {
						perr = fmt.Errorf("coordinator panic: %v", r)
					}
					chatCh <- chatOut{err: perr}
				}
			}()
			var c string
			var tk provider.Usage
			var e error
			if o.streaming && o.progress != nil {
				c, tk, e = CoordinatorStreamChat(ctx, o.model, prompt,
					nil, // text: goal output is JSON, not useful to stream
					func(delta string) {
						o.progress.OnOutput(ctx, Output{
							RunID:     goalRunID,
							Delta:     delta,
							Reasoning: true,
						})
					},
				)
			} else {
				c, tk, e = CoordinatorChat(ctx, o.model, prompt)
			}
			chatCh <- chatOut{content: c, tokens: tk, err: e}
		}(currentPrompt)

		var content string
		var tokens provider.Usage
		var chatErr error
		select {
		case out := <-chatCh:
			content, tokens, chatErr = out.content, out.tokens, out.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		addUsage(&totalTokens, tokens)
		if chatErr != nil {
			return nil, chatErr
		}

		wf, lastErr = ParseCoordinatorResponse(content)
		if lastErr == nil {
			break // Success
		}

 // Classify error using typed errors instead of string prefix matching.
		var jsonErr *JSONParseError
		var valErr *CoordinatorValidationError
		if errors.As(lastErr, &jsonErr) && jsonRetries < maxJSONRetries {
			jsonRetries++
			currentPrompt = prompt + fmt.Sprintf("\n\n## Previous Attempt Failed\nError: %s\nPlease output ONLY valid JSON matching the schema above.", lastErr)
			continue
		}
		if errors.As(lastErr, &valErr) && valRetries < maxValidationRetries {
			valRetries++
			currentPrompt = prompt + fmt.Sprintf("\n\n## Previous Attempt Failed\nError: %s\nPlease fix the issues and output valid JSON.", lastErr)
			continue
		}

		return nil, lastErr
	}

	// Validate that all agent tool names exist in the available tool catalog.
	if err := ValidateToolNames(wf, o.tools); err != nil {
		return nil, err
	}

	// Approval gate.
	if o.approval != nil {
		approved, err := o.approval.ApprovePlan(ctx, wf)
		if err != nil {
			return nil, fmt.Errorf("approval: %w", err)
		}
		if !approved {
			return nil, ErrPlanDenied
		}
	}

	// Emit plan_ready so sinks (e.g., CLI --plan) can render the DAG
	// before execution. Same recover wrap as RunFlow's plan_ready emit:
	// this fires before wrapProgressNonBlocking installs its pump-level
	// recover, so a buggy user sink that panics would otherwise crash
	// the RunGoal goroutine entirely.
	if o.progress != nil {
		emitPlanReady(ctx, o.progress, Event{
			Type:    types.EventPlanReady,
			RunID:   goalRunID,
			Message: wf.Name,
			Data:    map[string]any{"workflow": wf},
		})
	}

	// Execute the workflow via runFlowWithID; reuses goalRunID so both
	// spans share the same ID. Forward the goal's context through to
	// the decomposed flow so the executor's coord/coord-nil distribution
	// paths see it too.
	result, err = runFlowFn(o, ctx, wf, goalRunID, runFlowConfig{flowContext: cfg.goalContext})
	if err != nil {
		return nil, err
	}

	// Add coordinator token usage to the result.
	addUsage(&result.Tokens, totalTokens)

	return result, nil
}

// applyExecutorOptions threads the F3 Day-1 Options API knobs from the
// Orchestrator into a freshly constructed Executor. Called once per
// RunFlow / ResumeFlow invocation so each Run gets the configured
// behavior. SenderMatrixDAGAware defaults to true (the F7 cheaper +
// safer rule); other knobs use zero-as-default semantics that the
// Executor itself resolves at Run time.
func (o *Orchestrator) applyExecutorOptions(exec *Executor) {
	if exec == nil {
		return
	}
	exec.MaxWakeCycles = o.maxWakeCycles
	exec.HoldTimeout = o.holdTimeout
	exec.DropCallback = o.dropCallback
	exec.DropCallbackBufferSize = o.dropCallbackBufferSize
	exec.MaxMailboxSize = o.maxMailboxSize
	exec.MailboxStoreFactory = o.mailboxStoreFactory
	exec.MailboxDeliveryEnabled = o.mailboxDeliveryEnabled
	exec.EngineClock = o.engineClock
	exec.ProgressBufferSize = o.progressBufferSize
	exec.SenderMatrixDAGAware = true
	exec.TranscriptStoreFactory = o.transcriptStoreFactory
	exec.MaxTranscriptMessages = o.maxTranscriptMessages
	exec.MaxTranscriptBytes = o.maxTranscriptBytes
	exec.ExternalInboxes = o.externalInboxes
	exec.ModelResolver = o.modelResolver
	exec.TruncateOnCapReached = o.truncateOnCapReached
}
