package exec

import (
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/resume"
)

// Option configures an Orchestrator. Stable.
type Option func(*Orchestrator)

// RunFlowOption configures a single RunFlow invocation. Unlike Option (which
// configures the long-lived Orchestrator), RunFlowOption is per-call;
// callers can supply a different context per workflow run without
// reconstructing the Orchestrator. Stable.
type RunFlowOption func(*runFlowConfig)

// runFlowConfig collects per-call settings for RunFlow. Zero values
// preserve legacy behavior (no flow context, no blanket injection).
type runFlowConfig struct {
	// flowContext is the use-case input curated by the caller (CLI 2nd
	// positional arg, an SDK consumer's run_flow tool, etc.). When
	// non-empty:
	// - the executor pushes a workflow_start RouterMessage carrying
	// this string as Content into the coordinator runner's Mailbox
	// (only when WithCoordinator is set);
	// - in the coord==nil fallback, the executor prepends this string
	// to every step's effective user prompt.
	flowContext string
}

// WithFlowContext supplies use-case input to be distributed to the
// workflow steps. When a coordinator runner is installed via
// WithCoordinator, the context is pushed into the coord's mailbox as the
// first event (workflow_start) so the coord LLM can curate per-step
// forwards. When no coordinator is installed (WithCoordinator(nil)), the
// context is blanket-prepended to every step's effective user prompt as
// a static fallback. Empty string is a no-op.
// Stable.
func WithFlowContext(ctx string) RunFlowOption {
	return func(c *runFlowConfig) { c.flowContext = ctx }
}

// RunGoalOption configures a single RunGoal invocation. See RunFlowOption.
// Stable.
type RunGoalOption func(*runGoalConfig)

// runGoalConfig collects per-call settings for RunGoal.
type runGoalConfig struct {
	// goalContext is appended to the coordinator-decomposition prompt so
	// the goal-decomposition LLM sees both the user's goal text and any
	// additional contextual framing the caller wants to supply.
	goalContext string
}

// WithGoalContext supplies additional context (beyond the goal text
// itself) to the RunGoal coordinator-decomposition prompt. Empty string
// is a no-op.
// Stable.
func WithGoalContext(ctx string) RunGoalOption {
	return func(c *runGoalConfig) { c.goalContext = ctx }
}

// WithModel sets the language model.
// Stable.
func WithModel(m provider.LanguageModel) Option {
	return func(o *Orchestrator) { o.model = m }
}

// WithTools sets the tools available to agents.
// Stable.
func WithTools(tools ...goai.Tool) Option {
	return func(o *Orchestrator) { o.tools = tools }
}

// WithGoAIOptions passes extra goai options to GenerateText calls.
// Stable.
func WithGoAIOptions(opts ...goai.Option) Option {
	return func(o *Orchestrator) { o.goaiOpts = opts }
}

// WithStorage sets the persistence backend.
// Applies to RunFlow only - RunAgent does not persist state.
// Stable.
func WithStorage(s Storage) Option {
	return func(o *Orchestrator) { o.storage = s }
}

// WithPermissions sets the permission handler.
// Stable.
func WithPermissions(h PermissionHandler) Option {
	return func(o *Orchestrator) { o.permissions = h }
}

// WithProgress sets the progress event sink.
// Stable.
func WithProgress(s ProgressSink) Option {
	return func(o *Orchestrator) { o.progress = s }
}

// WithDefaultModel sets the fallback model name.
// Stable.
func WithDefaultModel(model string) Option {
	return func(o *Orchestrator) { o.defaultModel = model }
}

// WithForceModel overrides every per-agent and per-step Model identifier
// with the given model name during execution. Empty string disables the
// override (equivalent to leaving the option off).
// Precedence (high → low) for effective model resolution:
//	WithForceModel > Step.Model > AgentConfig.Model > WithDefaultModel
// Use WithForceModel for cross-provider CLI overrides (e.g. running every
// step of a workflow through a single test provider regardless of YAML).
// For ordinary defaults, prefer WithDefaultModel - it lets per-agent and
// per-step Model declarations win.
// Stable.
func WithForceModel(model string) Option {
	return func(o *Orchestrator) { o.forceModel = model }
}

// WithMaxConcurrency sets the maximum parallel step count.
// Precedence: see `defaultMaxConcurrency` in executor.go - this option
// is level 2 (Workflow.Options.MaxConcurrency overrides it; if both
// this option and the YAML default are zero, the executor falls back
// to defaultMaxConcurrency=5).
// Stable.
func WithMaxConcurrency(n int) Option {
	return func(o *Orchestrator) { o.maxConcurrency = n }
}

// WithMaxTurns sets the maximum conversation turns.
// Applies to RunAgent only - RunFlow uses per-agent MaxTurns from workflow YAML.
// Stable.
func WithMaxTurns(n int) Option {
	return func(o *Orchestrator) { o.maxTurns = n }
}

// WithMaxDepth sets the maximum agent nesting depth.
// Applies to RunAgent only - RunFlow does not support child agent spawning.
// Stable.
func WithMaxDepth(n int) Option {
	return func(o *Orchestrator) { o.maxDepth = n }
}

// MaxDepth returns the configured agent nesting depth cap. Returns the
// raw configured value; 0 means "use the runtime default" (currently
// 3, applied lazily inside RunAgent). Callers use this to assert
// WithMaxDepth(...) actually reached the constructed Orchestrator.
func (o *Orchestrator) MaxDepth() int {
	if o == nil {
		return 0
	}
	return o.maxDepth
}

// WithApproval sets the plan approval handler for RunGoal.
// If set, the coordinator-generated workflow must be approved before execution.
// Stable.
func WithApproval(h ApprovalHandler) Option {
	return func(o *Orchestrator) { o.approval = h }
}

// WithSharedMemory sets the shared memory instance for inter-agent collaboration.
// If set, shared_memory_read and shared_memory_write tools are auto-injected
// into agent tool chains during RunFlow and ResumeFlow.
// Stable.
func WithSharedMemory(sm *SharedMemory) Option {
	return func(o *Orchestrator) { o.sharedMem = sm }
}

// WithTracer sets the tracer for distributed tracing.
// The OTel sub-module (zenflow/observability/otel) provides a Tracer implementation.
// When set, workflow and step spans are created automatically.
// Stable.
func WithTracer(t Tracer) Option {
	return func(o *Orchestrator) { o.tracer = t }
}

// WithCoordinator installs a caller-provided AgentRunner as the workflow
// coordinator. When non-nil, the executor pushes lifecycle events
// (StepStart, StepEnd, error, etc.) as RouterMessages into runner.Mailbox
// under the coord step ID: runner.StepID when non-empty, otherwise the
// constant "coordinator". The runner's lifecycle is the caller's
// responsibility: the orchestrator does not call runner.Run, does not
// block on it, and does not check whether anyone drains the mailbox.
// CLI consumers typically construct an ephemeral coord runner via
// NewDefaultCoordRunner before RunFlow and dispose it after; embedded
// consumers reuse their existing primary AgentRunner so
// workflow events flow into the same chat history as user turns.
// Dual-ID convention: the "coord" identity is split across two independent
// string keys, both defaulting to "coordinator" but serving different roles:
// 1. The coord-runner Mailbox key - resolved by coordStepID(runner):
// runner.StepID if non-empty, else "coordinator". This is the key
// under which lifecycle events are Append-ed into runner.Mailbox.
// Embedded consumers typically pass runner.StepID="primary"
// so workflow events land in the same chat-history bucket as user turns.
// 2. The workflow-Router coordinator inbox key - the constant
// CoordRouterInboxID ("coordinator", in executor.go). Resumed steps
// reverse-route their replies into this inbox via Router.Send;
// drainCoordReverseReplies surfaces them as EventCoordinatorInboxMessage.
// This key is independent of the runner's StepID; even when the caller
// supplies a custom runner.StepID, reverse replies still flow through the
// Router's "coordinator" inbox unchanged.
// The executor's inbox auto-registration loop ALWAYS RegisterStep +
// RegisterInbox both keys when a Coordinator runner is installed, even
// when they coincide (de-duped), so a custom-StepID runner never causes
// resumed-step reverse replies to drop with DropReasonUnknownStep. Tested by
// TestExecutor_CustomStepIDAutoRegistersCoordRouterInbox.
// Pass nil to disable the coordinator entirely (workflow runs with no
// LLM-driven monitoring; messaging tools that target the coord still
// drop with an explicit reason). Not used by RunAgent - the primary
// agent IS the coordinator on that path.
// Stable.
func WithCoordinator(runner *AgentRunner) Option {
	return func(o *Orchestrator) { o.coordinator = runner }
}

// WithIsolation sets the step isolation strategy.
// When set, Setup is called before each step and Cleanup is deferred after.
// Stable.
func WithIsolation(iso StepIsolation) Option {
	return func(o *Orchestrator) { o.isolation = iso }
}

// WithOutputTransform sets the output transformer for step output injection.
// When set, step outputs are transformed before injection into dependent step prompts.
// Use this to implement smart truncation based on target model context window.
// Stable.
func WithOutputTransform(t OutputTransformer) Option {
	return func(o *Orchestrator) { o.outputTransform = t }
}

// WithStreaming enables streaming mode.
// When enabled, content that is being displayed (controlled by WithVerbose)
// is delivered token-by-token via ProgressSink.OnOutput instead of as full text.
// Stable.
func WithStreaming() Option {
	return func(o *Orchestrator) { o.streaming = true }
}

// WithoutStreaming disables streaming mode (restores the default batch delivery).
// Stable.
func WithoutStreaming() Option {
	return func(o *Orchestrator) { o.streaming = false }
}

// WithStreamingBool toggles streaming based on a boolean.
// Deprecated: use WithStreaming/WithoutStreaming. Will be removed before v1.0.
func WithStreamingBool(enabled bool) Option {
	if enabled {
		return WithStreaming()
	}
	return WithoutStreaming()
}

// WithVerbose enables agent output display.
// When enabled, agent LLM responses are shown in addition to events and narration.
// Without verbose, only workflow events (▸, ✓) and coordinator narration (≋) are shown.
// Stable.
func WithVerbose() Option {
	return func(o *Orchestrator) { o.verbose = true }
}

// WithoutVerbose disables agent output display (default: only events and narration).
// Stable.
func WithoutVerbose() Option {
	return func(o *Orchestrator) { o.verbose = false }
}

// WithVerboseBool toggles verbose output based on a boolean.
// Deprecated: use WithVerbose/WithoutVerbose. Will be removed before v1.0.
func WithVerboseBool(enabled bool) Option {
	if enabled {
		return WithVerbose()
	}
	return WithoutVerbose()
}

// WithMaxWakeCycles caps the number of wake-driven re-entries into goai
// per AgentRunner.Run. Zero means "use defaultMaxWakeCycles" (10). When
// the cap is reached with messages still pending, the runner emits one
// EventMessageDropped{reason: max-wake-cycles} per remaining message.
// Stable.
func WithMaxWakeCycles(n int) Option {
	return func(o *Orchestrator) { o.maxWakeCycles = n }
}

// WithAgentHandleTTL bounds the start-to-finish wall-clock cap on a
// RunAgentAsync handle. When the TTL is exceeded the handle is
// force-completed with AgentError{Sentinel: ErrAgentHandleTimeout} and
// the inner context is cancelled. Zero or negative falls back to
// DefaultAgentHandleTTL (30m). The library does not consult any
// environment variables; CLI consumers wire ZENFLOW_AGENT_HANDLE_TTL
// (or any other source) to this option themselves.
// Stable.
func WithAgentHandleTTL(d time.Duration) Option {
	return func(o *Orchestrator) { o.agentHandleTTL = d }
}

// WithHoldTimeout bounds how long the executor retains a step in
// StepIdle while messages keep arriving. After the timeout, the executor
// force-terminates the step and emits one EventMessageDropped{reason:
// hold-timeout} per remaining mailbox message. Zero falls back to
// defaultHoldTimeout (30s).
// Stable.
func WithHoldTimeout(d time.Duration) Option {
	return func(o *Orchestrator) { o.holdTimeout = d }
}

// WithDropCallback installs a user-supplied observer invoked once per
// dropped router message (in addition to the EventMessageDropped event
// already emitted via ProgressSink). Useful for metrics/alerting paths
// that don't want to subscribe to the full event firehose. The
// callback runs synchronously by default; set
// WithDropCallbackBufferSize to a positive value to dispatch
// asynchronously through a buffered channel (overflow falls back to
// synchronous so no drop event is itself silently lost).
// Stable.
func WithDropCallback(fn func(DropEvent)) Option {
	return func(o *Orchestrator) { o.dropCallback = fn }
}

// WithDropCallbackBufferSize selects the buffer size for asynchronous
// dispatch of WithDropCallback events. Zero or negative = synchronous
// dispatch (the default).
// Stable.
func WithDropCallbackBufferSize(n int) Option {
	return func(o *Orchestrator) { o.dropCallbackBufferSize = n }
}

// WithMaxMailboxSize bounds the per-step in-memory mailbox queue. When
// the cap is exceeded, Append rejects the newest message and the
// router emits EventMessageDropped{reason: mailbox-full} via OnDrop.
// When this option is NOT applied, New installs DefaultMaxMailboxSize
// (10000) - finite by default so pathological producers cannot exhaust
// memory in long-running workflows. To opt out and run with an
// unbounded mailbox, pass WithMaxMailboxSize(0) explicitly: the zero is
// preserved (it sets an internal flag separate from the size value, so
// the default install does not overwrite it).
// Only takes effect when the default InMemoryMailboxStore is used (i.e.
// no WithMailboxStore override). Custom stores enforce their own caps.
// Stable.
func WithMaxMailboxSize(n int) Option {
	return func(o *Orchestrator) {
		o.maxMailboxSize = n
		o.maxMailboxSizeSet = true
	}
}

// WithMailboxStore replaces the default InMemoryMailboxStore with a
// caller-supplied implementation (e.g. a sqlite/redis-backed store for
// multi-process workflows). The supplied factory is invoked once per
// workflow run so each Run gets a fresh store instance.
// Stable.
func WithMailboxStore(factory func() MailboxStore) Option {
	return func(o *Orchestrator) { o.mailboxStoreFactory = factory }
}

// WithMailboxDelivery enables the mailbox + delivery engine stack (the default).
// Use WithoutMailboxDelivery to suppress Router/Mailbox allocation in Executor.Run
// for tests that exercise the scheduler path without mailbox machinery.
// Stable.
func WithMailboxDelivery() Option {
	return func(o *Orchestrator) {
		v := true
		o.mailboxDeliveryEnabled = &v
	}
}

// WithoutMailboxDelivery suppresses Router/Mailbox allocation in Executor.Run -
// useful for tests that exercise the scheduler path without mailbox machinery.
// Stable.
func WithoutMailboxDelivery() Option {
	return func(o *Orchestrator) {
		v := false
		o.mailboxDeliveryEnabled = &v
	}
}

// WithMailboxDeliveryBool toggles mailbox delivery based on a boolean.
// Deprecated: use WithMailboxDelivery/WithoutMailboxDelivery. Will be removed before v1.0.
func WithMailboxDeliveryBool(enabled bool) Option {
	if enabled {
		return WithMailboxDelivery()
	}
	return WithoutMailboxDelivery()
}

// withClock substitutes the engine + lifecycle tick source. Test-only
// - production callers leave it unset and the orchestrator uses the
// real time.Ticker via the package's RealClock. Unexported because
// EngineClock itself is unexported (external callers could not satisfy
// the parameter type anyway). Same-package tests use this option to
// inject a fakeClock for deterministic tick-driven behaviour.
func withClock(c EngineClock) Option {
	return func(o *Orchestrator) { o.engineClock = c }
}

// WithProgressBufferSize controls the buffer of the non-blocking
// progress sink wrapper.
// Behavior: emits are non-blocking on the critical path while the
// buffered channel has capacity. On overflow the wrapper applies a
// bounded back-pressure of up to defaultEventBusTimeout (1s); if the
// channel is still full at the deadline the event is dropped and the
// internal dropped counter is incremented (visible via Stats).
// This bounds worst-case caller latency at 1s rather than blocking
// indefinitely.
// Larger buffers tolerate slower downstream sinks at the cost of more
// buffered memory. Zero or negative falls back to
// defaultEventBusBuffer (1024).
func WithProgressBufferSize(n int) Option {
	return func(o *Orchestrator) { o.progressBufferSize = n }
}

// WithTranscriptStore installs a TranscriptStore factory. The factory
// is invoked once per Executor.Run so each Run gets a fresh store
// instance (matching WithMailboxStore lifecycle). The default, when
// this option is not supplied, is a per-Run InMemoryTranscriptStore,
// which supports intra-run resume only. Supply a persistent-backed
// store (file / SQLite) to enable cross-run or cross-process resume.
func WithTranscriptStore(factory func() resume.TranscriptStore) Option {
	return func(o *Orchestrator) { o.transcriptStoreFactory = factory }
}

// WithMaxTranscriptMessages overrides the default per-step message
// count cap for the Day-1 InMemoryTranscriptStore. When exceeded,
// Append returns ErrTranscriptTooLarge and a subsequent resume on
// that step emits DropReasonTranscriptTooLarge. Zero or negative
// leaves the default `DefaultMaxTranscriptMessages`. Ignored when a
// custom store is supplied via WithTranscriptStore (custom stores
// enforce their own caps).
func WithMaxTranscriptMessages(n int) Option {
	return func(o *Orchestrator) { o.maxTranscriptMessages = n }
}

// WithMaxTranscriptBytes overrides the default per-step byte cap for
// the Day-1 InMemoryTranscriptStore. Zero or negative leaves the
// default `DefaultMaxTranscriptBytes`. Ignored when a custom store is
// supplied via WithTranscriptStore.
func WithMaxTranscriptBytes(b int64) Option {
	return func(o *Orchestrator) { o.maxTranscriptBytes = b }
}

// WithExternalInbox pre-registers one or more non-step sender inboxes
// on the Router at Run start. Use this for identities that send
// RouterMessages but are not workflow steps (e.g. "coordinator") and
// need to receive reverse-routed responses; notably the reverse message
// a resumed step sends back to its OriginalSender. Without
// pre-registration, Router.Send to an unknown non-step target drops as
// DropReasonUnknownStep.
// Idempotent and safe to pass the same ID multiple times.
// Stable.
func WithExternalInbox(ids ...string) Option {
	return func(o *Orchestrator) {
 // Deduplicate: build a seen-set from existing entries so repeated
 // calls (or duplicate IDs within a single call) do not register
 // the same inbox more than once.
		seen := make(map[string]bool, len(o.externalInboxes)+len(ids))
		for _, id := range o.externalInboxes {
			seen[id] = true
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				o.externalInboxes = append(o.externalInboxes, id)
			}
		}
	}
}

// WithModelResolver installs a ModelResolver consulted by the resume
// path. When a saved transcript's Model identifier differs from the
// Executor's default runner model, the resolver is called with the
// transcript's model ID; a nil/empty return fails the resume loudly with
// ErrModelResolverMissing (DropReasonTargetTerminal). Without a resolver,
// mismatch also fails loudly; the resume path never silently falls back
// to the wrong model.
// Scope:
// - REQUIRED for cross-run resume with a non-default model (after
// process restart with a persistent transcript store). The
// Executor's per-step model-string map is intra-run memory only.
// - OPTIONAL for intra-run resume: the workflow step's model string
// is tracked automatically and the default-path match succeeds
// without a resolver.
// Stable.
func WithModelResolver(r ModelResolver) Option {
	return func(o *Orchestrator) { o.modelResolver = r }
}

// WithTruncationOnCapReached configures the resume path to fall back to
// TranscriptTruncatedLoader.LoadTruncated when a sealed transcript's
// Load returns ErrTranscriptTooLarge. The bound defaults to 1000 messages;
// stores implementing LoadTruncated may choose their own tail size.
func WithTruncationOnCapReached() Option {
	return func(o *Orchestrator) { o.truncateOnCapReached = true }
}

// WithoutTruncationOnCapReached restores the default behavior: a sealed
// transcript fails the resume with DropReasonTranscriptTooLarge so
// operators can detect and investigate the cap.
func WithoutTruncationOnCapReached() Option {
	return func(o *Orchestrator) { o.truncateOnCapReached = false }
}

// WithTruncationOnCapReachedBool toggles cap-reached truncation based on a boolean.
// Deprecated: use WithTruncationOnCapReached/WithoutTruncationOnCapReached. Will be removed before v1.0.
func WithTruncationOnCapReachedBool(enabled bool) Option {
	if enabled {
		return WithTruncationOnCapReached()
	}
	return WithoutTruncationOnCapReached()
}

// WithRouterObserver registers a callback invoked once per RunAgent /
// RunFlow invocation with the freshly-allocated MessageRouter for that
// run. Intended for observability hooks (telemetry, debug inspectors,
// integration tests) that need a handle on the per-call router without
// polling internal state. The callback fires synchronously before any
// Run-side goroutine consumes the router; it must not block.
// Production callers typically leave this unset; nil callbacks are
// ignored.
// Panic semantics: if the callback panics, the panic IS recovered (the
// run continues) and logged via slog.Error with key "panic" and
// hook="routerObserver". The panic is NOT propagated as an error;
// RunAgent / RunFlow behave as if the observer had returned cleanly.
// Callers that need to surface observer failures must do so out-of-band
// (e.g. via their own channel or atomic flag captured in the closure).
func WithRouterObserver(fn func(*MessageRouter)) Option {
	return func(o *Orchestrator) { o.routerObserver = fn }
}

// WithRunID pins the orchestrator's run identifier for RunFlow / RunAgent
// / RunGoal. When set, all internally-emitted Event.RunID values use this
// ID instead of a freshly-generated one. ResumeFlow already takes runID
// as an argument and ignores this option.
// Callers that allocate a runID up-front (e.g. HTTP servers that store it
// in a database row and return it to the client before the workflow
// starts) use this to guarantee the server-visible ID and the zenflow-
// emitted Event.RunID match. Without this option, zenflow generates its
// own ID internally and any pre-allocated ID diverges - persisted events
// carry the internal ID while DB rows and HTTP responses carry the
// pre-allocated one, breaking cross-referencing (e.g. /flow-debug).
// Empty = fall back to internal generation (legacy behavior).
// Stable.
func WithRunID(runID string) Option {
	return func(o *Orchestrator) { o.runID = runID }
}
