package exec

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/coord"
	"github.com/zendev-sh/zenflow/internal/resume"
	"github.com/zendev-sh/zenflow/internal/router"
	"github.com/zendev-sh/zenflow/internal/types"
)

const defaultMaxTurns = 50

// defaultMaxWakeCycles is the default number of wake-driven re-entries
// into goai.GenerateText permitted per Run before the wake loop is
// declared exhausted. Operators can override via
// WithRunnerMaxWakeCycles.
// On cap-hit (B3 fix): all remaining mailbox messages are drained and
// emitted as DropReasonMaxWakeCycles via the runner's Progress sink so
// the "zero silent drops" contract holds even when a pathological
// producer keeps the agent in a wake hot-loop.
const defaultMaxWakeCycles = 10

// defaultHoldTimeout bounds how long the executor holds a step in
// StepIdle while late messages keep arriving, before force-terminating
// it and emitting hold-timeout drops for any remaining mailbox
// entries.
const defaultHoldTimeout = 30 * time.Second

// maxWakeCyclesWarnFraction is the fraction of MaxWakeCycles at which
// EventMaxWakeCyclesWarning is emitted (once per Run). Recommended 80%.
const maxWakeCyclesWarnFraction = 0.8

// Tool name constants for special tool dispatch.
const (
	toolNameAgent        = "agent"
	toolNameSubmitResult = "submit_result"
	toolNameSendMessage  = "send_message"
)

// AgentStatus describes how an agent run terminated.
type AgentStatus string

const (
	// AgentStatusCompleted means the agent finished normally (LLM returned no tool calls).
	AgentStatusCompleted AgentStatus = "completed"
	// AgentStatusTruncated means the agent hit its maxTurns limit.
	AgentStatusTruncated AgentStatus = "truncated"
)

// AgentResult holds the output of a single agent conversation loop. Stable.
// For synchronous RunAgent calls, only Content/Result/Tokens/Turns/
// Status/Duration are set and errors are returned as the second value.
// For RunAgentAsync handles, the same struct is delivered over
// AgentHandle.Done and the Error field carries any terminal error
// (including AgentError-wrapped sentinels for TTL timeout, cancel, and
// panic-recover). Consumers MUST check Error before trusting the other
// fields.
type AgentResult struct {
	Content  string
	Result   map[string]any
	Tokens   provider.Usage
	Turns    int
	Status   AgentStatus   // "completed" or "truncated"
	Duration time.Duration // wall-clock duration of the agent run
	Error    error         // non-nil iff the async handle terminated with an error
}

// childSpawner is an abstraction over agentSpawner.SpawnChild, allowing tests
// to inject a mock that returns errors. In production, *agentSpawner satisfies this.
type childSpawner interface {
	SpawnChild(ctx context.Context, call provider.ToolCall) (string, error)
}

// AgentRunner executes a single-agent conversation loop with tool calling. Stable.
// It delegates the tool loop to goai.GenerateText(WithMaxSteps) and uses
// goai hooks for: permission checks, agent spawning, submit_result handling,
// and progress events. Inter-agent message delivery is handled by the
// mailbox+wake path - the sole delivery mechanism.
type AgentRunner struct {
	model       provider.LanguageModel
	tools       []goai.Tool
	permissions PermissionHandler
	progress    ProgressSink
	goAIOptions []goai.Option // Extra goai options (e.g., tracing).
	streaming   bool          // When true, use goai.StreamText and emit OnOutput per chunk.
	verbose     bool          // When true, emit agent text + reasoning content via OnOutput.
	runID       string        // Workflow run ID (for streaming output context).
	stepID      string        // Step ID (for streaming output context).
	// stateRef, when non-nil, is forwarded to goai.WithStateRef so the
	// poller can observe the runner's tool-loop lifecycle
	// (StepStarting / StepLLMInFlight / StepStepFinished /
	// StepToolExecuting / StepIdle) without holding a lock.
	stateRef *goai.AgentState

	// mailbox + Wake form the mailbox-driven delivery path.
	// When BOTH are non-nil, the AgentRunner consumes inter-agent messages
	// via the mailbox poll/wake model:
	// - DeliveryEngine watches mailbox.Unread(StepID) each tick.
	// - When the agent reaches StepIdle with unread messages, the
	// engine sends on Wake (non-blocking; cap-1 buffer).
	// - Inside the goai tool loop a WithStopWhen predicate consumes
	// Wake and exits the current loop iteration with
	// StopCausePredicate.
	// - AgentRunner then drains mailbox.Unread, MarkReads them, and
	// re-enters goai.GenerateText with the appended messages.
	// When mailbox+Wake are nil, the runner skips messaging entirely (the
	// agent has no inter-agent inbox, e.g. the standalone RunAgent path).
	mailbox MailboxStore
	wake    chan struct{}

	// router is the optional MessageRouter shared with sibling/child
	// AgentRunners. zenflow.RunAgent plumbs the per-call router into
	// the runner so child spawns inherit a live router for inter-agent
	// messaging. Workflow execution sets router via the Executor.
	// nil = no shared router (legacy single-call path with no messaging).
	router *MessageRouter

	// spawnDepth is the recursion depth of this runner relative to the
	// top-level RunAgent invocation. Used to enrich EventToolCall
	// payloads with a `depth` field so TUI consumers can collapse
	// nested-spawn cards under the parent. Zero means "top level"
	// (the runner created directly by RunAgent / RunFlow).
	spawnDepth int

	// spawnParentCallID is the agent-tool ToolCallID that produced this
	// runner via agentSpawner.SpawnChild. Emitted on every EventToolCall
	// in Data["parentCallID"] so consumers can route nested events into
	// the parent's children list. Empty for the top-level runner.
	spawnParentCallID string

	// maxWakeCycles caps the number of wake-driven re-entries into
	// goai.GenerateText per Run. Zero or negative means "use
	// defaultMaxWakeCycles". When the cap is reached with messages still
	// pending in the mailbox, the runner drains them and emits one
	// EventMessageDropped{reason: max-wake-cycles} per remaining
	// message (B3 fix - never silent).
	maxWakeCycles int

	// Resume Mechanism R2. When transcript is non-nil the
	// runner persists the step's conversation into the store on every
	// goai step-finish hook AND on Run exit. The stored transcript is
	// what Executor.ResumeStep consults when a router message arrives
	// for a terminated step.
	// ModelID / SystemPrompt are captured at Run start for transcript
	// metadata so a later Resume can reconstruct the invocation without
	// a side-channel. - SystemPrompt is also injected into goai
	// via goai.WithSystem (see Run baseOpts construction); callers that
	// want a pure-metadata behaviour with no system message should leave
	// the field empty and pass goai.WithSystem(...) via GoAIOptions
	// instead.
	transcript   resume.TranscriptStore
	modelID      string // "provider:model" stored in transcript.Model
	systemPrompt string // injected via goai.WithSystem AND stored in transcript.SystemPrompt

	// F1 - pre-loaded conversation history prepended to the
	// fresh user turn Run accepts via the userMessage parameter. Used by
	// Executor.runResume to replay the saved transcript through the
	// standard AgentRunner.Run machinery (hooks, Mailbox+Wake drain,
	// StateRef terminal CAS, Progress events) rather than bypassing it
	// with a direct goai.GenerateText call. Empty for normal (non-resume)
	// runs.
	initialMessages []provider.Message

	// preStartDrainGate, when non-nil, causes Run to block on receive
	// from this channel BEFORE the first drainMailboxIntoMessages call.
	// Test-only hook: lets callers hold the pre-start drain while they
	// set up mailbox preconditions (e.g. "seed N messages, then release
	// the drain"). Close or send on the channel to proceed. A nil gate
	// preserves production behavior - Run drains immediately.
	// Rationale: the resume path's pre-start drain races test
	// Append calls. Without this gate, -race -count=N flakes because the
	// drain's MarkRead can consume a message before subsequent Appends
	// check the cap, bypassing the cap rejection the test expects.
	preStartDrainGate <-chan struct{}

	spawner childSpawner // Optional: handles "agent" tool calls for child spawning.

	// Coord-finalize signal. Set by the FinalizeToolDef tool when the
	// coord LLM explicitly calls `finalize`. Finalization is explicit:
	// the runner's outer Run loop is the caller's responsibility, the
	// tool only SIGNALS exit. Caller observes via Finalized (atomic
	// snapshot) or Done (channel for select-driven loops).
	// FinalSummary carries the optional synthesis text the coord
	// passed.
	// Mechanism: atomic flag + cap-1 close-once channel + atomic.Value
	// summary. A special-EventType finalize would mix signal-of-intent
	// with observable narration (already covered by
	// EventCoordinatorSynthesis); a bare runner.Finalize method
	// without atomic backing could not provide both non-blocking poll
	// (Finalized) and select-friendly channel (Done).
	finalized         atomic.Bool
	finalizeChMu      sync.Mutex // guards finalizeCh allocation only
	finalizeCh        chan struct{}
	finalizeCloseOnce sync.Once              // guards close(finalizeCh) - exactly one close
	finalSummary      atomic.Pointer[string] // lazily populated by FinalizeToolDef

	// fwdSeq is the per-runner counter for tool-side correlation IDs
	// emitted by forward_to_agent and send_message (`msg-fwd-N` /
	// `msg-send-N`). Per-runner (not per-process) so concurrent
	// workflows do not interleave IDs in a way that breaks per-run
	// observability. Single shared counter across both tools so an
	// observer can correlate ordering across both tool families
	// within a single workflow.
	fwdSeq atomic.Uint64

	// wakeContextProvider, when non-nil, is invoked once before the
	// first GenerateText call AND once on every wake-driven re-entry
	// after the mailbox drain. The returned string is appended as a
	// fresh user-role message wrapped in <dynamic-context> tags so the
	// LLM can distinguish ambient context from in-band conversation.
	// Empty / whitespace-only returns are skipped (no empty message
	// injected). Set via WithRunnerWakeContextProvider.
	// Use case: long-running coordinators that need ambient context
	// refreshed every wake (e.g. chat: currently-open files,
	// cursor position, repo metadata). Coord consumers should configure
	// via WithCoordContextProvider; the option threads through here.
	wakeContextProvider func() string
}

// RunnerOption configures a NewAgentRunner. Stable.
// Renamed from AgentRunnerOption (C14: package-name stutter); the old
// name is kept as a type alias for backwards compatibility so existing
// callers (zenflow.go, coord_factory.go) keep compiling.
type RunnerOption func(*AgentRunner)

// AgentRunnerOption is the historical name for RunnerOption. Kept as
// an alias so internal callers and the public facade
// (zenflow.AgentRunnerOption) continue to compile during the rename.
// Deprecated: use RunnerOption.
type AgentRunnerOption = RunnerOption

// WithRunnerModel sets the language model on the AgentRunner.
// Stable.
func WithRunnerModel(m provider.LanguageModel) RunnerOption {
	return func(r *AgentRunner) { r.model = m }
}

// WithRunnerTools sets the tools available to the agent.
// Stable.
func WithRunnerTools(tools ...goai.Tool) RunnerOption {
	return func(r *AgentRunner) { r.tools = tools }
}

// WithRunnerPermissions sets the permission handler on the AgentRunner.
// Stable.
func WithRunnerPermissions(h PermissionHandler) RunnerOption {
	return func(r *AgentRunner) { r.permissions = h }
}

// WithRunnerProgress sets the progress event sink on the AgentRunner.
// Stable.
func WithRunnerProgress(s ProgressSink) RunnerOption {
	return func(r *AgentRunner) { r.progress = s }
}

// WithRunnerGoAIOptions sets extra goai options on the AgentRunner.
// Stable.
func WithRunnerGoAIOptions(opts ...goai.Option) RunnerOption {
	return func(r *AgentRunner) { r.goAIOptions = opts }
}

// WithRunnerStreaming enables streaming mode on the AgentRunner.
// Stable.
func WithRunnerStreaming() RunnerOption {
	return func(r *AgentRunner) { r.streaming = true }
}

// WithRunnerVerbose enables verbose output on the AgentRunner.
// Stable.
func WithRunnerVerbose() RunnerOption {
	return func(r *AgentRunner) { r.verbose = true }
}

// WithRunnerRunID sets the workflow run ID on the AgentRunner.
// Stable.
func WithRunnerRunID(id string) RunnerOption {
	return func(r *AgentRunner) { r.runID = id }
}

// WithRunnerStepID sets the step ID on the AgentRunner.
// Stable.
func WithRunnerStepID(id string) RunnerOption {
	return func(r *AgentRunner) { r.stepID = id }
}

// WithRunnerSystemPrompt sets the system prompt on the AgentRunner.
// Stable.
func WithRunnerSystemPrompt(prompt string) RunnerOption {
	return func(r *AgentRunner) { r.systemPrompt = prompt }
}

// WithRunnerModelID sets the model ID string (for transcript metadata) on the AgentRunner.
// Stable.
func WithRunnerModelID(id string) RunnerOption {
	return func(r *AgentRunner) { r.modelID = id }
}

// WithRunnerStateRef wires a goai.AgentState into the AgentRunner so the
// poller can observe the runner's tool-loop lifecycle without holding a
// lock. See the StateRef field for the full lifecycle contract.
// Stable.
func WithRunnerStateRef(s *goai.AgentState) RunnerOption {
	return func(r *AgentRunner) { r.stateRef = s }
}

// WithRunnerMailbox sets the MailboxStore the runner reads inter-agent
// messages from. Pair with WithRunnerWake to enable mailbox-mode delivery.
// Stable.
func WithRunnerMailbox(m MailboxStore) RunnerOption {
	return func(r *AgentRunner) { r.mailbox = m }
}

// WithRunnerWake sets the wake-signal channel that the DeliveryEngine
// pings when the agent's mailbox has unread messages. Pair with
// WithRunnerMailbox to enable mailbox-mode delivery.
// Stable.
func WithRunnerWake(ch chan struct{}) RunnerOption {
	return func(r *AgentRunner) { r.wake = ch }
}

// WithRunnerRouter wires a shared MessageRouter into the AgentRunner so
// child spawns inherit a live router for inter-agent messaging. nil is
// valid (legacy single-call path with no messaging).
// Stable.
func WithRunnerRouter(rt *MessageRouter) RunnerOption {
	return func(r *AgentRunner) { r.router = rt }
}

// WithRunnerSpawnDepth records the recursion depth of this runner
// relative to the top-level RunAgent invocation. Used to enrich
// EventToolCall payloads so TUI consumers can collapse nested-spawn
// cards under the parent.
// Stable.
func WithRunnerSpawnDepth(depth int) RunnerOption {
	return func(r *AgentRunner) { r.spawnDepth = depth }
}

// WithRunnerSpawnParentCallID records the agent-tool ToolCallID that
// produced this runner via SpawnChild. Emitted on every EventToolCall
// in Data["parentCallID"] so consumers can route nested events into the
// parent's children list.
// Stable.
func WithRunnerSpawnParentCallID(id string) RunnerOption {
	return func(r *AgentRunner) { r.spawnParentCallID = id }
}

// WithRunnerMaxWakeCycles caps the number of wake-driven re-entries
// into goai.GenerateText per Run. Zero or negative means "use the
// package default" (defaultMaxWakeCycles).
// Stable.
func WithRunnerMaxWakeCycles(n int) RunnerOption {
	return func(r *AgentRunner) { r.maxWakeCycles = n }
}

// WithRunnerTranscript wires a TranscriptStore so the runner persists
// the step's conversation on every goai step-finish hook AND on Run
// exit. Required for the Resume Mechanism (R2).
// Stable.
func WithRunnerTranscript(ts resume.TranscriptStore) RunnerOption {
	return func(r *AgentRunner) { r.transcript = ts }
}

// WithRunnerInitialMessages pre-loads conversation history that the
// runner prepends to the fresh user turn passed to Run. Used by
// Executor.runResume to replay a saved transcript through the standard
// AgentRunner.Run machinery. Empty for normal (non-resume) runs.
// Stable.
func WithRunnerInitialMessages(msgs []provider.Message) RunnerOption {
	return func(r *AgentRunner) { r.initialMessages = msgs }
}

// WithRunnerPreStartDrainGate is a test-only hook: when non-nil, Run
// blocks on receive from this channel BEFORE the first
// drainMailboxIntoMessages call. Lets callers hold the pre-start drain
// while they set up mailbox preconditions. A nil gate preserves
// production behavior.
// Stable.
func WithRunnerPreStartDrainGate(gate <-chan struct{}) RunnerOption {
	return func(r *AgentRunner) { r.preStartDrainGate = gate }
}

// WithRunnerWakeContextProvider attaches a callback that supplies
// ambient context the runner injects as a fresh user-role message
// once before the first LLM call and once after every wake-driven
// mailbox drain. The returned string is wrapped in <dynamic-context>
// tags. Empty / whitespace-only returns are skipped. Pass nil to
// disable (default).
// Designed for long-running coordinators that need per-wake context
// refresh (e.g. open-file list, repo metadata, session topic) without
// re-engineering the system prompt. Coord callers should normally use
// WithCoordContextProvider instead, which threads through here.
// Stable.
func WithRunnerWakeContextProvider(fn func() string) RunnerOption {
	return func(r *AgentRunner) { r.wakeContextProvider = fn }
}

// NewAgentRunner constructs an AgentRunner with the given options.
// Callers that prefer struct-literal initialization may continue to use
// &AgentRunner{...} directly - both forms are supported.
// Stable.
func NewAgentRunner(opts ...RunnerOption) *AgentRunner {
	r := &AgentRunner{}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Model returns the runner's configured LLM provider.
// External SDK consumers use this to inspect the bound model after
// construction (e.g. to verify CLI wiring or assert in tests). Use
// WithRunnerModel(...) to set the value at construction time.
// Stable.
func (r *AgentRunner) Model() provider.LanguageModel { return r.model }

// Tools returns the runner's configured tools slice. The
// returned slice is the same underlying array used by the runner; treat
// it as read-only.
// Stable.
func (r *AgentRunner) Tools() []goai.Tool { return r.tools }

// Wake returns the runner's wake channel. Send on this
// channel (non-blocking; cap-1 buffer) to interrupt the goai tool loop
// at the next idle predicate check, prompting a mailbox drain. nil
// when no wake channel was configured (the runner has no inter-agent
// inbox).
// Stable.
func (r *AgentRunner) Wake() chan struct{} { return r.wake }

// Run executes the agent loop using goai.GenerateText with WithMaxSteps.
// goai owns the tool loop. Zenflow hooks handle: permissions, agent spawning,
// submit_result, and progress events. Inter-agent message delivery (when
// Mailbox+Wake are configured) is interleaved via WithStopWhen + post-stop
// drain.
func (r *AgentRunner) Run(ctx context.Context, cfg AgentConfig, userMessage string, model string, tools []goai.Tool, attachments ...provider.Part) (retResult *AgentResult, retErr error) {
	start := time.Now()

	// Terminal state CAS: on every Run exit path, map the outcome to
	// one of {StepDone, StepCancelled, StepError} via the
	// goai.AgentState.SetTerminal CAS contract. The CAS guarantees a
	// single terminal write per Run regardless of how many panic/recover
	// or early-return branches execute. The poller (waitForStepTermination)
	// observes terminal kinds via state.Observe and unblocks the
	// executor's per-step lifecycle wait.
	// Precedence:
	// 1. panic → StepError
	// 2. retErr != nil → StepError (or StepCancelled if errors.Is ctx)
	// 3. ctx.Err → StepCancelled
	// 4. otherwise → StepDone
	// Seed transcript metadata (system prompt + model id) at Run start
	// so a later Resume can reconstruct the invocation even if the Run
	// errors out before any Append lands.
	if r.transcript != nil {
		if setter, ok := r.transcript.(resume.MetadataSetter); ok {
			setter.SetMetadata(r.runID, r.stepID, r.systemPrompt, r.modelID)
		}
	}

	defer func() {
		if r.stateRef == nil {
			return
		}
		if rec := recover(); rec != nil {
			r.stateRef.SetTerminal(goai.StepError)
			panic(rec) // re-panic so existing recovery in caller still runs
		}
		switch {
		case retErr != nil:
			if ctxErr := ctx.Err(); ctxErr != nil {
				r.stateRef.SetTerminal(goai.StepCancelled)
			} else {
				r.stateRef.SetTerminal(goai.StepError)
			}
		case ctx.Err() != nil:
			r.stateRef.SetTerminal(goai.StepCancelled)
		default:
			r.stateRef.SetTerminal(goai.StepDone)
		}
	}()

	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}

	agentID := cmp.Or(cfg.Description, model)

	// auto-inject `send_message` when this runner has a Router.
	// Step agents need a way to message the workflow coordinator (per
	// D-Z1 hub-only routing); rather than require every caller to
	// remember to add the tool, we inject it transparently when
	// messaging is wired. Skipped when:
	// - r.Router is nil (no coord wired - Q4 nil-coord path is
	// observable via the tool's own "dropped: no-coordinator"
	// return, but injecting the tool when no coord exists would
	// surface a tool the agent has no reason to ever call);
	// - the caller already supplied a tool named "send_message" (so
	// a downstream caller can override the default - e.g. with a
	// custom Description, schema, or Execute - without being
	// silently shadowed by our injection);
	// - the runner IS the coordinator (detected by presence
	// of `forward_to_agent`, the coord-specific tool that NO step
	// agent gets). Without this guard, coord auto-gets send_message
	// and weak prompt-followers (observed on MiniMax) call
	// send_message(target="coordinator") - sending to themselves -
	// causing an infinite self-loop. send_message is hub-only
	// (D-Z1): steps message coord, coord NEVER messages itself.
	if r.router != nil {
		hasSendMessage := false
		isCoordinator := false
		for _, t := range tools {
			switch t.Name {
			case "send_message":
				hasSendMessage = true
			case "forward_to_agent":
				isCoordinator = true
			}
		}
		if !hasSendMessage && !isCoordinator {
			tools = append(tools[:len(tools):len(tools)], coord.SendMessageToolDef(r))
		}
	}

	// Auto-inject submit_result tool if agent has ResultSchema.
	var submitDone atomic.Bool
	var submitResult atomic.Pointer[map[string]any]
	var submitHandler *SubmitResultHandler
	if cfg.ResultSchema != nil {
		submitHandler = NewSubmitResultHandler(cfg.ResultSchema)
		tools = append(tools[:len(tools):len(tools)], SubmitResultToolDef(cfg.ResultSchema))
	}

	// Build initial user message (with attachments).
	parts := []provider.Part{{Type: provider.PartText, Text: userMessage}}
	parts = append(parts, attachments...)
	userMsg := provider.Message{Role: provider.RoleUser, Content: parts}
	// F1 - if InitialMessages is set (resume path), prepend
	// the saved transcript BEFORE the new user turn so the resumed agent
	// sees the full prior context + the coordinator's new prompt.
	var messages []provider.Message
	if len(r.initialMessages) > 0 {
		messages = make([]provider.Message, 0, len(r.initialMessages)+1)
		messages = append(messages, r.initialMessages...)
		messages = append(messages, userMsg)
	} else {
		messages = []provider.Message{userMsg}
	}

	mailboxMode := r.mailbox != nil && r.wake != nil

	// Pre-start mailbox drain. Any messages buffered before this Run
	// started should land in the first LLM call so single-turn agents
	// see them. Cancel messages short-circuit immediately.
	if mailboxMode {
		// Test-only: block until the gate is closed/sent. Lets a test
		// finish setting up mailbox preconditions (e.g. admitting up to
		// cap, then confirming the cap rejects subsequent Appends)
		// before the drain consumes anything. Also observes ctx cancel.
		if r.preStartDrainGate != nil {
			select {
			case <-r.preStartDrainGate:
			case <-ctx.Done():
				r.emitResidualDrops(ctx, mailboxMode, router.DropReasonWorkflowCancelled)
				return &AgentResult{
					Content:  "cancelled",
					Status:   AgentStatusCompleted,
					Duration: time.Since(start),
				}, nil
			}
		}
		cancelled, _ := r.drainMailboxIntoMessages(ctx, &messages)
		if cancelled {
			r.emitResidualDrops(ctx, mailboxMode, router.DropReasonWorkflowCancelled)
			return &AgentResult{
				Content:  "cancelled",
				Status:   AgentStatusCompleted,
				Duration: time.Since(start),
			}, nil
		}
	}

	// Inject the first dynamic-context snapshot before the initial
	// GenerateText call so the LLM sees ambient state on every Run, not
	// only after a wake-driven re-entry.
	r.appendWakeContext(&messages)

	// stoppedBy captures goai's terminal cause via OnFinish - the
	// TextResult itself does not expose StopCause directly.
	var stoppedBy provider.StopCause
	captureFinish := func(info goai.FinishInfo) { stoppedBy = info.StoppedBy }

	// transcript persistence. persistedCount tracks how
	// many messages of the in-progress conversation have already been
	// appended to the store so the final flush (defer below) can avoid
	// double-writing. The per-step OnStepFinish hook persists the
	// initial user prompt + assistant step-text as they become
	// available; the final flush covers any tool-result messages that
	// accrue between the last OnStepFinish and Run exit.
	// Design note: OnStepFinish fires BEFORE tool execution so the
	// StepResult it receives does NOT include ToolResults. We therefore
	// cannot drive Append from StepResult alone - we snapshot the
	// runner's running messages slice instead. The authoritative copy
	// of the full conversation is `messages + result.ResponseMessages`
	// at Run exit, which the deferred flush persists.
	var (
		persistedCount int
		// transcriptSealed records whether a prior Append hit
		// ErrTranscriptTooLarge (cap seal) so subsequent attempts are
		// skipped instead of looping over the rejected cap-exceeded
		// path.: only set on IRREVERSIBLE errors (cap). Transient
		// IO errors go through transcriptErrored instead so subsequent
		// Append can retry.
		transcriptSealed bool
		// transcriptErrored is a one-shot flag that suppresses
		// duplicate EventTranscriptSealed emissions for non-cap store
		// errors. Next Append still runs - transient failures (flaky
		// disk, network blip) must not permanently lose transcript
		// persistence for the rest of the Run.
		transcriptErrored bool
	)
	flushTranscript := func(allMsgs []provider.Message) {
		if r.transcript == nil || transcriptSealed {
			return
		}
		if len(allMsgs) <= persistedCount {
			return
		}
		tail := allMsgs[persistedCount:]
		// Defensive copy so the store can't later see mutations from
		// the goai tool loop reusing the backing array.
		cp := make([]provider.Message, len(tail))
		copy(cp, tail)
		if err := r.transcript.Append(r.runID, r.stepID, cp); err != nil {
			isCap := errors.Is(err, resume.ErrTranscriptTooLarge)
			// only cap trips seal permanently. Non-cap errors
			// (transient IO) leave transcriptSealed=false so the next
			// Append can retry. The event is still emitted (once) so
			// operators see the failure signal.
			emitEvent := false
			if isCap {
				transcriptSealed = true
				emitEvent = true
			} else if !transcriptErrored {
				transcriptErrored = true
				emitEvent = true
			}
			if emitEvent && r.progress != nil {
				reason := "store-error"
				if isCap {
					reason = "transcript-too-large"
				}
				r.progress.OnEvent(ctx, Event{
					Type:      types.EventTranscriptSealed,
					Timestamp: time.Now(),
					RunID:     r.runID,
					StepID:    r.stepID,
					Data: map[string]any{
						"reason": reason,
						"error":  err.Error(),
					},
				})
			}
			return
		}
		// Success - reset the transient-error latch so a future
		// failure can re-emit, and advance the persisted cursor.
		transcriptErrored = false
		persistedCount = len(allMsgs)
	}

	// Final-flush defer: persists whatever messages + response
	// messages exist at Run exit, regardless of which terminal branch
	// executed. Runs BEFORE the SetTerminal defer because defers pop
	// LIFO: SetTerminal was registered first so it runs last, ensuring
	// the transcript is committed even if the terminal state is Error.
	var finalResultRef **goai.TextResult
	{
		var rp *goai.TextResult
		finalResultRef = &rp
		defer func() {
			if r.transcript == nil {
				return
			}
			res := rp
			full := make([]provider.Message, 0, len(messages)+8)
			full = append(full, messages...)
			if res != nil {
				full = append(full, res.ResponseMessages...)
			}
			flushTranscript(full)
		}()
	}

	// Build base options. When in mailbox mode, WithStopWhen drains
	// r.Wake non-blockingly so a pending wake exits the next iteration
	// with StopCausePredicate; the wake-loop below re-enters
	// goai.GenerateText after draining.
	baseOpts := []goai.Option{
		goai.WithTools(tools...),
		goai.WithMaxSteps(maxTurns),
		goai.WithOnFinish(captureFinish),
	}
	// actually inject the runner's SystemPrompt into goai. Prior
	// to this fix, r.SystemPrompt was set on the runner (e.g. by
	// NewDefaultCoordRunner installing DefaultCoordSystemPrompt) but
	// never reached the LLM call: the field was only read by the
	// transcript metadata setter for resume bookkeeping. As a result the
	// coord ran with an empty system prompt and inferred behaviour from
	// tool descriptions + short user-message hints - exactly the
	// kind of fragility that prior rounds spent time papering over. Inject when
	// non-empty so coord sees its addressing/cadence/recovery rules and
	// resume parity holds (executor_resume.go already mirrors this).
	if r.systemPrompt != "" {
		baseOpts = append(baseOpts, goai.WithSystem(r.systemPrompt))
	}
	if mailboxMode {
		wakePredicate := func(_ []goai.StepResult) bool {
			select {
			case <-r.wake:
				return true
			default:
				return false
			}
		}
		baseOpts = append(baseOpts, goai.WithStopWhen(wakePredicate))
	}
	if r.stateRef != nil {
		baseOpts = append(baseOpts, goai.WithStateRef(r.stateRef))
	}
	if cfg.Temperature != nil {
		baseOpts = append(baseOpts, goai.WithTemperature(*cfg.Temperature))
	}
	if cfg.TopP != nil {
		baseOpts = append(baseOpts, goai.WithTopP(*cfg.TopP))
	}

	// OnBeforeToolExecute: agent spawn, submit_result, permission check.
	baseOpts = append(baseOpts, goai.WithOnBeforeToolExecute(func(info goai.BeforeToolExecuteInfo) goai.BeforeToolExecuteResult {
		if info.ToolName == toolNameAgent && r.spawner != nil {
			content, err := r.spawner.SpawnChild(info.Ctx, provider.ToolCall{
				ID: info.ToolCallID, Name: info.ToolName, Input: info.Input,
			})
			if err != nil {
				return goai.BeforeToolExecuteResult{Skip: true, Error: err}
			}
			return goai.BeforeToolExecuteResult{Skip: true, Result: content}
		}
		if info.ToolName == toolNameSubmitResult && submitHandler != nil {
			resultMap, terminated, sErr := submitHandler.Handle(info.Input)
			if sErr != nil {
				return goai.BeforeToolExecuteResult{Skip: true, Error: sErr}
			}
			if terminated {
				submitResult.Store(&resultMap)
				submitDone.Store(true)
				return goai.BeforeToolExecuteResult{Skip: true, Result: "result submitted successfully"}
			}
		}
		// Built-in coord-routing tool: bypass permission gate. send_message
		// is internal hub-only routing (agent → coord), not user-facing IO,
		// so it must not trigger an interactive permission prompt. The
		// tool's own Execute (in internal/coord) handles router.Send.
		if info.ToolName == toolNameSendMessage {
			return goai.BeforeToolExecuteResult{}
		}
		if r.permissions != nil {
			allowed, err := r.permissions.RequestPermission(info.Ctx, PermissionRequest{
				StepID: agentID, ToolName: info.ToolName, ToolArgs: info.Input,
			})
			if err != nil {
				return goai.BeforeToolExecuteResult{Skip: true, Error: err}
			}
			if !allowed {
				return goai.BeforeToolExecuteResult{Skip: true, Result: "permission denied"}
			}
		}
		return goai.BeforeToolExecuteResult{}
	}))

	// OnBeforeStep: handle submit_result early termination.
	baseOpts = append(baseOpts, goai.WithOnBeforeStep(func(_ goai.BeforeStepInfo) goai.BeforeStepResult {
		if submitDone.Load() {
			return goai.BeforeStepResult{Stop: true}
		}
		return goai.BeforeStepResult{}
	}))

	// goai.WithOnStepFinish is registered as an
	// observability probe (no-op for default callers). The
	// authoritative transcript write happens inside the wake loop
	// after each goai iteration returns a *goai.TextResult -
	// flushTranscript is called with the current
	// `messages + result.ResponseMessages`. We use OnStepFinish only
	// to record a heartbeat flag so incremental progress is visible
	// to tests that want to observe per-step persistence even within
	// a single wake cycle.
	// Rationale: OnStepFinish fires BEFORE tool execution so its
	// StepResult carries no ToolResults; any Append driven from that
	// hook would then be immediately superseded by the post-iteration
	// flush. The simpler + correct design is to have a single
	// authoritative persistence path driven by the caller-visible
	// TextResult.ResponseMessages.
	var stepFinishObserved int
	if r.transcript != nil {
		baseOpts = append(baseOpts, goai.WithOnStepFinish(func(_ goai.StepResult) {
			stepFinishObserved++
		}))
	}
	_ = stepFinishObserved

	// Progress hooks (request/response/tool calls).
	if r.progress != nil {
		baseOpts = append(baseOpts, goai.WithOnRequest(func(info goai.RequestInfo) {
			r.progress.OnEvent(info.Ctx, Event{
				Type:      types.EventAgentTurn,
				Timestamp: info.Timestamp,
				RunID:     r.runID,
				StepID:    r.stepID,
				AgentName: agentID,
				Data:      map[string]any{"phase": "request", "turn": info.MessageCount, "model": model},
			})
			// Note: don't emit a bare Reasoning Output here. The
			// "◎ Thinking..." header should only appear when there's
			// actual reasoning content - driven by provider.ChunkReasoning
			// in streaming mode or by genResult.Reasoning in non-streaming.
			// Emitting on every request lit up the header every turn even
			// when thinking was disabled.
		}))
		baseOpts = append(baseOpts, goai.WithOnResponse(func(info goai.ResponseInfo) {
			usage := info.Usage
			r.progress.OnEvent(ctx, Event{
				Type:      types.EventAgentTurn,
				Timestamp: time.Now(),
				RunID:     r.runID,
				StepID:    r.stepID,
				AgentName: agentID,
				Data:      map[string]any{"phase": "response", "model": model},
				Tokens:    &usage,
			})
		}))
		baseOpts = append(baseOpts, goai.WithOnToolCallStart(func(info goai.ToolCallStartInfo) {
			data := map[string]any{
				"phase":        "start",
				"tool_name":    info.ToolName,
				"tool_call_id": info.ToolCallID,
				// Pass the redacted JSON arguments through so consumers
				// (TUI integrations) can populate the tool card's input
				// section with the path/command being invoked.
				// redact secrets before emitting to sinks.
				"input": redactSecrets(string(info.Input)),
			}
			// When this runner is a nested child (SpawnDepth > 0),
			// attach the spawn metadata so TUI consumers can collapse
			// the resulting tool_call event under the parent's
			// children list.
			if r.spawnDepth > 0 {
				data["depth"] = r.spawnDepth
			}
			if r.spawnParentCallID != "" {
				data["parentCallID"] = r.spawnParentCallID
			}
			r.progress.OnEvent(ctx, Event{
				Type:      types.EventToolCall,
				Timestamp: time.Now(),
				RunID:     r.runID,
				StepID:    r.stepID,
				AgentName: agentID,
				Data:      data,
			})
		}))
		baseOpts = append(baseOpts, goai.WithOnToolCall(func(info goai.ToolCallInfo) {
			data := map[string]any{
				"phase":        "end",
				"tool_name":    info.ToolName,
				"tool_call_id": info.ToolCallID,
				// Mirror start: include input on end too so reconnecting
				// consumers that miss the start frame can still
				// reconstruct the card.
				// redact secrets before emitting to sinks.
				"input":  redactSecrets(string(info.Input)),
				"output": info.Output,
			}
			if r.spawnDepth > 0 {
				data["depth"] = r.spawnDepth
			}
			if r.spawnParentCallID != "" {
				data["parentCallID"] = r.spawnParentCallID
			}
			r.progress.OnEvent(ctx, Event{
				Type:      types.EventToolCall,
				Timestamp: time.Now(),
				RunID:     r.runID,
				StepID:    r.stepID,
				AgentName: agentID,
				Data:      data,
				Duration:  info.Duration,
				Error:     info.Error,
			})
		}))
	}

	baseOpts = append(baseOpts, r.goAIOptions...)

	// Wake loop: re-enter goai whenever it stops with StopCausePredicate
	// (mailbox mode only). When mailbox mode is off, the loop runs once.
	// Default cap: 10 wake cycles. Surfaces observability for
	// cap-approach (warning at 80%) and cap-hit (drain+drop pending
	// messages instead of silent exit).
	maxWakeCycles := r.maxWakeCycles
	if maxWakeCycles <= 0 {
		maxWakeCycles = defaultMaxWakeCycles
	}
	iterCap := maxWakeCycles
	if !mailboxMode {
		iterCap = 1
	}
	warnAt := int(float64(maxWakeCycles) * maxWakeCyclesWarnFraction)
	if warnAt < 1 {
		warnAt = 1
	}
	warnEmitted := false
	var result *goai.TextResult
	wakeCycles := 0 // 1-indexed cycles AFTER the primary iteration
	// track whether we exited the loop because we hit the
	// wake-cycle cap (line 810 break) vs other reasons (ctx cancel,
	// natural stop, submit_done). Only when iter actually reaches
	// iterCap should the post-loop drain emit
	// `EventMessageDropped{reason:max-wake-cycles}`. For ctx-cancel
	// or other early exits, the messages are NOT cap-victims -
	// they're residue of the runner being torn down. Fix: emit drops
	// with reason=workflow-cancelled when ctx was canceled.
	hitWakeCap := false
	for iter := 0; iter < iterCap; iter++ {
		if ctx.Err() != nil {
			break
		}
		// B3: emit max-wake-cycles warning once at the configured
		// fraction of the cap (mailbox mode only - non-mailbox path
		// always runs exactly one iteration).
		if mailboxMode && !warnEmitted && iter+1 >= warnAt && r.progress != nil {
			warnEmitted = true
			unread := 0
			if r.mailbox != nil {
				unread = len(r.mailbox.Unread(r.stepID))
			}
			r.progress.OnEvent(ctx, Event{
				Type:      types.EventMaxWakeCyclesWarning,
				Timestamp: time.Now(),
				RunID:     r.runID,
				StepID:    r.stepID,
				Data: map[string]any{
					"current_cycle":    iter + 1,
					"max_cycles":       maxWakeCycles,
					"unread_remaining": unread,
				},
			})
		}
		stoppedBy = ""
		opts := append([]goai.Option{goai.WithMessages(messages...)}, baseOpts...)

		if r.streaming {
			stream, streamErr := goai.StreamText(ctx, r.model, opts...)
			if streamErr != nil {
				if result == nil {
					// A5#9 + B5#4: emit drops for any residual mailbox
					// messages already delivered before the first call
					// failed (e.g. ctx-cancel during first stream open).
					// Otherwise the post-loop drain at the bottom of Run
					// is skipped via this early-return and messages are
					// silently dropped without an EventMessageDropped.
					r.emitResidualDrops(ctx, mailboxMode, dropReasonForErr(ctx, streamErr))
					return nil, fmt.Errorf("agent stream (model %q): %w", model, streamErr)
				}
				break
			}
			// drop `&& r.Verbose` gate on emitText in streaming
			// path. Previous behaviour: --stream alone was a no-op (text
			// chunks read from goai stream but discarded before reaching
			// the sink), so users saw zero difference between `--stream`
			// and default mode. Now `--stream` actually streams agent
			// text token-by-token. The sink (sink/stdout.go:384) does NOT
			// gate text deltas on verbose, mirroring the runner here.
			// Reasoning text remains conditionally rendered: header
			// always shown via sink:365, body only when --verbose
			// (sink:368). --verbose without --stream still emits agent
			// text (line 785 below) but as one batched chunk at turn end.
			emitText := r.progress != nil
			emitReasoning := r.progress != nil
			for chunk := range stream.Stream() {
				switch chunk.Type {
				case provider.ChunkText:
					if emitText {
						r.progress.OnOutput(ctx, Output{
							RunID: r.runID, StepID: r.stepID, AgentID: agentID, Delta: chunk.Text,
						})
					}
				case provider.ChunkReasoning:
					if emitReasoning && chunk.Text != "" {
						r.progress.OnOutput(ctx, Output{
							RunID: r.runID, StepID: r.stepID, AgentID: agentID, Delta: chunk.Text, Reasoning: true,
						})
					}
				}
			}
			if emitText {
				r.progress.OnOutput(ctx, Output{
					RunID: r.runID, StepID: r.stepID, AgentID: agentID, Done: true,
				})
			}
			genResult := stream.Result()
			if stream.Err() != nil {
				if result == nil {
					// A5#9 + B5#4: see comment on streamErr branch above.
					r.emitResidualDrops(ctx, mailboxMode, dropReasonForErr(ctx, stream.Err()))
					return nil, fmt.Errorf("agent stream (model %q): %w", model, stream.Err())
				}
				break
			}
			result = mergeTextResult(result, genResult)
		} else {
			genResult, genErr := goai.GenerateText(ctx, r.model, opts...)
			if genErr != nil {
				if result == nil {
					// A5#9 + B5#4: emit residual mailbox drops before
					// returning. The post-loop drain at line ~1214 is
					// skipped via this early-return path, so without this
					// call any messages already delivered to the mailbox
					// (e.g. ctx-cancel mid-call) would be silently lost.
					r.emitResidualDrops(ctx, mailboxMode, dropReasonForErr(ctx, genErr))
					return nil, fmt.Errorf("agent generate (model %q): %w", model, genErr)
				}
				break
			}
			// Emit reasoning + text on the first iteration only -
			// subsequent wake-driven iterations are continuations and
			// duplicating the buffered output would double-print.
			if r.progress != nil && result == nil {
				// Reasoning is surfaced regardless of r.Verbose so the
				// "Thinking..." header (driven by Reasoning=true) appears
				// in non-streaming mode too. Sink controls whether the
				// delta text is rendered (verbose-only).
				if genResult.Reasoning != "" {
					r.progress.OnOutput(ctx, Output{
						RunID: r.runID, StepID: r.stepID, AgentID: agentID,
						Delta: genResult.Reasoning, Reasoning: true,
					})
				}
				if r.verbose && genResult.Text != "" {
					r.progress.OnOutput(ctx, Output{
						RunID: r.runID, StepID: r.stepID, AgentID: agentID, Delta: genResult.Text,
					})
					r.progress.OnOutput(ctx, Output{
						RunID: r.runID, StepID: r.stepID, AgentID: agentID, Done: true,
					})
				}
			}
			result = mergeTextResult(result, genResult)
		}

		// authoritative incremental flush after each
		// goai iteration. The full conversation snapshot for this
		// iteration is `messages + result.ResponseMessages`.
		if r.transcript != nil && result != nil {
			full := make([]provider.Message, 0, len(messages)+len(result.ResponseMessages))
			full = append(full, messages...)
			full = append(full, result.ResponseMessages...)
			flushTranscript(full)
		}
		// Keep the deferred final-flush in sync with the latest result.
		*finalResultRef = result

		if submitDone.Load() {
			break
		}
		if !mailboxMode {
			break
		}
		// Continuation trigger: a wake-predicate stop OR a natural exit
		// while the mailbox still has unread messages. The latter
		// covers agents that finish their tool loop before the engine
		// observes StepIdle (single-shot text completions, fast tool
		// loops); without this branch, late mailbox writes would be
		// dropped silently.
		pendingNow := len(r.mailbox.Unread(r.stepID))
		hasPending := pendingNow > 0
		// B3: if this was the LAST permitted iteration, do not drain -
		// leave any unread messages in the mailbox so the post-loop
		// cap-hit handler can emit them as max-wake-cycles drops.
		// Without this guard, the per-iteration drain at the bottom of
		// the loop body would silently MarkRead the messages even
		// though the next LLM call will never run.
		if iter+1 >= iterCap && hasPending {
			hitWakeCap = true // distinguish cap exhaustion from ctx-cancel exit
			break
		}
		if stoppedBy != provider.StopCausePredicate && !hasPending {
			// B6: agent reached natural completion with no unread
			// messages - emit EventAgentIdle so observers know the
			// agent is parked. We exit immediately rather than
			// blocking on Wake (the engine will re-spawn this Run
			// flow on the next workflow turn if needed).
			if r.progress != nil {
				r.progress.OnEvent(ctx, Event{
					Type:      types.EventAgentIdle,
					Timestamp: time.Now(),
					RunID:     r.runID,
					StepID:    r.stepID,
					Data: map[string]any{
						"unread_count": 0,
					},
				})
			}
			break
		}
		// Drain any pending wake signal so it doesn't immediately
		// re-fire the predicate on the next iteration before any new
		// messages arrive.
		select {
		case <-r.wake:
		default:
		}

		// Wake fired - drain the mailbox into the next call's messages.
		nextMessages := make([]provider.Message, 0, len(messages)+len(result.ResponseMessages)+4)
		nextMessages = append(nextMessages, messages...)
		nextMessages = append(nextMessages, result.ResponseMessages...)
		drainCancel, drained := r.drainMailboxIntoMessages(ctx, &nextMessages)
		if drainCancel {
			// A7#1 + B7#1: drainMailboxIntoMessages MarkReads only up to
			// the cancel marker (consumed = cancelIdx+1). Any messages
			// after the marker - or appended between the Unread snapshot
			// and cancel detection - remain unread and would otherwise be
			// silently abandoned by this early-return path. Emit residual
			// drops here so they surface as EventMessageDropped, matching
			// the other 5 cancel-exit sites.
			r.emitResidualDrops(ctx, mailboxMode, router.DropReasonWorkflowCancelled)
			return &AgentResult{
				Content:  "cancelled",
				Tokens:   result.TotalUsage,
				Turns:    len(result.Steps),
				Status:   AgentStatusCompleted,
				Duration: time.Since(start),
			}, nil
		}
		if !drained {
			break
		}
		// Inject a fresh dynamic-context snapshot per wake cycle, so
		// each re-entry sees up-to-date ambient state alongside the
		// drained mailbox messages.
		r.appendWakeContext(&nextMessages)
		// B6: emit EventAgentWake noting how many messages were
		// drained on this cycle.
		wakeCycles++
		if r.progress != nil {
			r.progress.OnEvent(ctx, Event{
				Type:      types.EventAgentWake,
				Timestamp: time.Now(),
				RunID:     r.runID,
				StepID:    r.stepID,
				Data: map[string]any{
					"message_count": pendingNow,
					"cycle":         wakeCycles,
				},
			})
		}
		messages = nextMessages
	}

	// B3 +: post-loop drain of remaining unread messages.
	// Distinguishes drop reason based on WHY the loop exited:
	// - hitWakeCap=true → cap exhaustion → DropReasonMaxWakeCycles
	// - ctx canceled → workflow teardown → DropReasonWorkflowCancelled
	// - other (natural stop, submit_done) → DropReasonTargetTerminal
	// (semantically: runner exited cleanly; messages arrived too late
	// for THIS Run; would normally be picked up by the next
	// re-spawn cycle, but if we're past finalize/Done the runner
	// is terminal).
	// Without this distinction, ctx-cancel residue at workflow end
	// got mislabeled as "max-wake-cycles" - confusing operators who
	// see the warning and think they need to bump the cap. Likewise,
	// the submit_result-break path (submitDone.Load at line ~1129)
	// is a clean terminal exit, NOT a cancellation - mislabeling it
	// as workflow-cancelled would lie about the run's outcome.
	if mailboxMode {
		// Default: terminal-clean exit (submit_done, natural stop with
		// no pending msgs but a race delivered one between the check
		// and this point, etc.). Override only for the two known
		// non-terminal exits: cap exhaustion and ctx cancellation.
		dropReason := router.DropReasonTargetTerminal
		switch {
		case hitWakeCap:
			dropReason = router.DropReasonMaxWakeCycles
		case ctx.Err() != nil:
			dropReason = router.DropReasonWorkflowCancelled
		}
		r.emitResidualDrops(ctx, mailboxMode, dropReason)
	}

	if result == nil {
		// Reachable only when ctx was cancelled before the first goai
		// iteration ran (the loop's `if ctx.Err != nil { break }` guard
		// at the top fires before any GenerateText call assigns result).
		// Every other nil-result path returns earlier inside the loop:
		// genErr with result==nil returns immediately (line 416),
		// streamErr with result==nil returns immediately (line 377).
		// Surface ctx.Err so callers can errors.Is against
		// context.Canceled / context.DeadlineExceeded.
		return nil, fmt.Errorf("agent generate (model %q): %w", model, ctx.Err())
	}

	// if the agent has a resultSchema but didn't call submit_result on
	// its own, retry once with ToolChoice=required and only the submit_result
	// tool available.
	if submitHandler != nil && !submitDone.Load() && ctx.Err() == nil {
		retryResult, retryErr := r.retrySubmitResult(ctx, cfg, messages, result.ResponseMessages, baseOpts)
		if retryErr == nil && retryResult != nil {
			addUsage(&result.TotalUsage, retryResult.TotalUsage)
		}
		// retryErr is best-effort: the primary outcome is encoded in
		// submitDone.Load (set by the submit_result handler on successful
		// retry); the executor surfaces "submit not called" via missing
		// structured Result, not via this error.
		if retryErr != nil {
			slog.Warn("submit_result retry failed (best-effort)", "err", retryErr, "run_id", r.runID, "step_id", r.stepID)
		}
	}

	status := AgentStatusCompleted
	if result.StepsExhausted {
		status = AgentStatusTruncated
	}
	if submitDone.Load() {
		var resultMap map[string]any
		if p := submitResult.Load(); p != nil {
			resultMap = *p
		}
		return &AgentResult{
			Content:  result.Text,
			Result:   resultMap,
			Tokens:   result.TotalUsage,
			Turns:    len(result.Steps),
			Status:   AgentStatusCompleted,
			Duration: time.Since(start),
		}, nil
	}
	if status == AgentStatusTruncated && submitHandler != nil {
		return nil, fmt.Errorf("agent exhausted %d turns: %w%s", maxTurns, ErrAgentTurnLimitExceeded, lastAssistantText(result))
	}
	if submitHandler != nil && !submitDone.Load() {
		return nil, fmt.Errorf("%w%s", ErrAgentNoSubmitResult, lastAssistantText(result))
	}
	return &AgentResult{
		Content:  result.Text,
		Tokens:   result.TotalUsage,
		Turns:    len(result.Steps),
		Status:   status,
		Duration: time.Since(start),
	}, nil
}

// emitResidualDrops drains any unread mailbox messages for this runner's
// step and emits an EventMessageDropped per message via the progress sink,
// then MarkReads them. No-op when mailbox mode is off, the mailbox is nil,
// the progress sink is nil, or there are no unread messages.
// Called from BOTH the normal post-loop drain at the end of Run AND the
// early-return paths inside the wake loop (genErr/streamErr with
// result==nil) to ensure residual drops are observable on every exit -
// without this, ctx-cancel during the FIRST goai call would skip the
// post-loop drain via early return, silently dropping any pre-delivered
// mailbox messages.
// The caller chooses the drop reason:
// - early-return error path: dropReasonForErr(ctx, err) - workflow
// cancelled if ctx.Err != nil, otherwise target-terminal.
// - post-loop normal path: workflow-cancelled (ctx mid-loop) or
// max-wake-cycles (cap hit), per existing logic.
func (r *AgentRunner) emitResidualDrops(ctx context.Context, mailboxMode bool, reason router.DropReason) {
	if !mailboxMode || r.mailbox == nil || r.progress == nil {
		return
	}
	remaining := r.mailbox.Unread(r.stepID)
	if len(remaining) == 0 {
		return
	}
	for _, msg := range remaining {
		r.progress.OnEvent(ctx, Event{
			Type:      types.EventMessageDropped,
			Timestamp: time.Now(),
			RunID:     r.runID,
			StepID:    r.stepID,
			Message:   fmt.Sprintf("[%s -> %s]: %s", msg.From, r.stepID, msg.Content),
			Data: map[string]any{
				"reason":   reason.String(),
				"from":     msg.From,
				"to":       r.stepID,
				"msg_type": int(msg.Type),
			},
		})
	}
	r.mailbox.MarkRead(r.stepID, MessageIDs(remaining))
}

// dropReasonForErr classifies an early-return error from
// goai.GenerateText/StreamText into a DropReason for residual mailbox
// messages. Workflow-cancelled when ctx was cancelled (most common case:
// the workflow shut down mid-call), otherwise target-terminal (the runner
// errored out so this Run cannot consume the messages).
func dropReasonForErr(ctx context.Context, err error) router.DropReason {
	if ctx.Err() != nil {
		return router.DropReasonWorkflowCancelled
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return router.DropReasonWorkflowCancelled
	}
	return router.DropReasonTargetTerminal
}

// mergeTextResult folds a fresh goai TextResult into an accumulating one. On
// the first iteration (acc==nil) it returns next directly; on subsequent
// iterations it accumulates token usage and appends response/step
// history while overwriting the rolling Text and StepsExhausted flags.
// Concat semantics: Steps and ResponseMessages from next are appended to
// acc; TotalUsage is summed via addUsage; StepsExhausted takes the later
// value (next wins). Text is overwritten by next's value to surface the
// most recent assistant text - this is zenflow's "later-call wins"
// contract for callers reading result.Text after multi-call merges.
func mergeTextResult(acc, next *goai.TextResult) *goai.TextResult {
	if acc == nil {
		return next
	}
	acc.Steps = append(acc.Steps, next.Steps...)
	acc.ResponseMessages = append(acc.ResponseMessages, next.ResponseMessages...)
	addUsage(&acc.TotalUsage, next.TotalUsage)
	acc.StepsExhausted = next.StepsExhausted
	acc.Text = next.Text
	return acc
}

// emitInboxDrain emits EventAgentInboxDrain when an inbox message has been
// appended to the agent's LLM conversation. This is the delivery-level ack:
// proves the message reached the LLM context, not that the LLM acted on it.
// Silently returns when no Progress sink is configured.
func (r *AgentRunner) emitInboxDrain(ctx context.Context, msg RouterMessage) {
	if r.progress == nil {
		return
	}
	r.progress.OnEvent(ctx, Event{
		Type:      types.EventAgentInboxDrain,
		Timestamp: time.Now(),
		RunID:     r.runID,
		StepID:    r.stepID,
		Message:   fmt.Sprintf("[%s]: %s", msg.From, msg.Content),
		Data: map[string]any{
			"from":     msg.From,
			"msg_type": int(msg.Type),
		},
	})
}

// retrySubmitResult performs's fallback retry: when the primary
// generation finishes without calling submit_result, it re-invokes the model
// with ONLY the submit_result tool available and ToolChoice=required.
func (r *AgentRunner) retrySubmitResult(
	ctx context.Context,
	cfg AgentConfig,
	originalMessages []provider.Message,
	responseMessages []provider.Message,
	primaryOpts []goai.Option,
) (*goai.TextResult, error) {
	retryMsgs := slices.Concat(originalMessages, responseMessages, []provider.Message{
		{
			Role: provider.RoleUser,
			Content: []provider.Part{{
				Type: provider.PartText,
				Text: "You did not call submit_result. You MUST call the submit_result tool NOW with your final structured answer matching the required schema. Do not return plain text - call the tool.",
			}},
		},
	})

	retryTools := []goai.Tool{SubmitResultToolDef(cfg.ResultSchema)}

	retryOpts := append([]goai.Option{}, primaryOpts...)
	retryOpts = append(retryOpts,
		goai.WithMessages(retryMsgs...),
		goai.WithTools(retryTools...),
		goai.WithMaxSteps(2),
		goai.WithToolChoice(goai.ToolChoiceRequired),
	)

	return goai.GenerateText(ctx, r.model, retryOpts...)
}

// lastAssistantText extracts the last assistant text content from the result
// for inclusion in error messages. Returns an empty string if no
// assistant content is present.
func lastAssistantText(result *goai.TextResult) string {
	if result == nil {
		return ""
	}
	text := result.Text
	if text == "" {
		for i := len(result.ResponseMessages) - 1; i >= 0; i-- {
			m := result.ResponseMessages[i]
			if m.Role != provider.RoleAssistant {
				continue
			}
			for _, p := range m.Content {
				if p.Type == provider.PartText && p.Text != "" {
					text = p.Text
					break
				}
			}
			if text != "" {
				break
			}
		}
	}
	if text == "" {
		return " (no assistant text captured)"
	}
	const maxLen = 500
	if len(text) > maxLen {
		text = text[:maxLen] + "...[truncated]"
	}
	return fmt.Sprintf(" - last assistant message: %q", text)
}

// drainMailboxIntoMessages pulls every Unread message from r.Mailbox for
// r.StepID, appends it as a user turn to msgs, MarkReads the consumed
// appendWakeContext invokes the wake-context provider (if configured)
// and, when it returns non-empty content, appends one fresh user-role
// message to the conversation wrapped in <dynamic-context> tags. The
// wrapping tag lets the LLM distinguish ambient context from in-band
// turn content. Whitespace-only returns are skipped so an idle
// provider does not pollute the message stream with empty turns.
// Called once before the first GenerateText call and once on every
// wake-driven re-entry after the mailbox drain, giving long-running
// coordinators a per-wake refresh hook.
func (r *AgentRunner) appendWakeContext(msgs *[]provider.Message) {
	if r.wakeContextProvider == nil {
		return
	}
	text := strings.TrimSpace(r.wakeContextProvider())
	if text == "" {
		return
	}
	*msgs = append(*msgs, provider.Message{Role: provider.RoleUser, Content: []provider.Part{{Type: provider.PartText, Text: "<dynamic-context>\n" + text + "\n</dynamic-context>"}}})
}

// batch, and emits an EventAgentInboxDrain per message. Returns
// (cancelled, drainedAny). cancelled is true if a RouterMessageCancel
// was encountered (caller should short-circuit Run); drainedAny is
// true if at least one non-cancel message was appended.
func (r *AgentRunner) drainMailboxIntoMessages(ctx context.Context, msgs *[]provider.Message) (cancelled bool, drainedAny bool) {
	if r.mailbox == nil || r.stepID == "" {
		return false, false
	}
	pending := r.mailbox.Unread(r.stepID)
	if len(pending) == 0 {
		return false, false
	}
	consumed := 0
	for i, msg := range pending {
		if msg.Type == router.MessageCancel {
			cancelled = true
			consumed = i + 1
			break
		}
		// RouterMessageInfo, RouterMessageContextUpdate, and
		// RouterMessageResumeReply all flow to the
		// agent's conversation as a user turn. The Type is preserved
		// on the mailbox record for observers; drain is uniform.
		*msgs = append(*msgs, provider.Message{Role: provider.RoleUser, Content: []provider.Part{{Type: provider.PartText, Text: fmt.Sprintf("[%s]: %s", msg.From, msg.Content)}}})
		r.emitInboxDrain(ctx, msg)
		drainedAny = true
		consumed = i + 1
	}
	if consumed > 0 {
		r.mailbox.MarkRead(r.stepID, MessageIDs(pending[:consumed]))
	}
	return cancelled, drainedAny
}

// MessageIDs is re-exported from internal/router via router_facade.go.

// --- Finalize-related methods (moved here from coord_tools.go so they
// stay in the same package as AgentRunner's unexported fields) ---

// Finalized reports whether the FinalizeToolDef tool has been invoked
// at least once. Safe to call from any goroutine; non-blocking.
// Caller's outer Run loop polls this between mailbox-drain
// iterations to decide whether to exit cleanly.
func (r *AgentRunner) Finalized() bool {
	return r.finalized.Load()
}

// Done returns a channel that closes when the FinalizeToolDef tool
// is invoked for the first time. Safe to call concurrently with
// FinalizeToolDef; the channel is lazily allocated and closed
// exactly once. Subsequent Done calls return the same channel.
// select-driven callers (e.g. CLI Run loops that block on
// {ctx.Done, runner.Done, mailbox-wake}) prefer this over polling
// Finalized.
func (r *AgentRunner) Done() <-chan struct{} {
	return r.ensureFinalizeCh()
}

// ensureFinalizeCh lazily allocates runner.finalizeCh under the
// dedicated mutex so concurrent Done/FinalizeToolDef calls observe
// the SAME channel - a prerequisite for the close-once invariant.
// Returns the (possibly newly created) channel for the caller to
// close (Finalize) or to receive on (Done).
func (r *AgentRunner) ensureFinalizeCh() chan struct{} {
	r.finalizeChMu.Lock()
	defer r.finalizeChMu.Unlock()
	if r.finalizeCh == nil {
		r.finalizeCh = make(chan struct{})
	}
	return r.finalizeCh
}

// FinalSummary returns the synthesis text the coord passed to the most
// recent FinalizeToolDef invocation, or empty string if finalize has
// not been called or was called with no summary.
// CLI Run loops surface this as EventCoordinatorSynthesis
// after the Done channel closes.
func (r *AgentRunner) FinalSummary() string {
	if p := r.finalSummary.Load(); p != nil {
		return *p
	}
	return ""
}

// SetFinalSummary stores the optional synthesis text the
// FinalizeToolDef tool received. Last-writer-wins on repeated calls
// (matches the prior `runner.finalSummary.Store(&summary)` semantics).
// Part of the coord.RunnerHandle behavioural contract; the coord
// FinalizeToolDef calls this to persist the synthesis text BEFORE
// invoking MarkFinalized so the close-once channel signal lands after
// the summary is durably visible to FinalSummary readers.
func (r *AgentRunner) SetFinalSummary(summary string) {
	r.finalSummary.Store(&summary)
}

// MarkFinalized flips the finalized flag AND closes the finalize
// channel exactly once. Idempotent: subsequent calls are no-ops because
// the underlying sync.Once guards the close. Part of the
// coord.RunnerHandle contract - the FinalizeToolDef tool calls
// MarkFinalized to terminate the caller's outer Run loop.
func (r *AgentRunner) MarkFinalized() {
	r.finalized.Store(true)
	ch := r.ensureFinalizeCh()
	r.finalizeCloseOnce.Do(func() { close(ch) })
}

// EnsureFinalizeCh returns a read-only view of the finalize channel.
// Callers select on this channel to detect FinalizeToolDef invocation;
// the channel is lazily allocated and closed exactly once by
// MarkFinalized. Equivalent to Done but spelled to satisfy the
// coord.RunnerHandle interface signature.
func (r *AgentRunner) EnsureFinalizeCh() <-chan struct{} {
	return r.ensureFinalizeCh()
}

// Router returns the runner's optional MessageRouter. nil when the
// runner was constructed without WithRunnerRouter (legacy single-call
// path with no inter-agent messaging). Part of the coord.RunnerHandle
// contract.
func (r *AgentRunner) Router() *MessageRouter { return r.router }

// Progress returns the runner's optional ProgressSink. nil when the
// runner was constructed without WithRunnerProgress. Part of the
// coord.RunnerHandle contract - coord tools emit narration / message
// events through this sink.
func (r *AgentRunner) Progress() ProgressSink { return r.progress }

// StepID returns the runner's configured StepID. Useful for CLI/TUI
// consumers that need to log or display the inbox key the runner
// registers under. Also satisfies the coord.RunnerHandle interface.
// Stable.
func (r *AgentRunner) StepID() string { return r.stepID }

// RunID returns the runner's configured RunID. Used by coord tools to
// stamp emitted events so JSON sink consumers can correlate events
// back to the workflow run that produced them.
func (r *AgentRunner) RunID() string { return r.runID }

// NextForwardSeq atomically allocates the next per-runner forward
// sequence number. The four coord tool factories use this to mint
// `msg-fwd-N` / `msg-send-N` correlation IDs returned in their tool
// result strings. Single shared counter so a single monotonic sequence
// spans every coord-side routing tool call within one workflow.
func (r *AgentRunner) NextForwardSeq() uint64 { return r.fwdSeq.Add(1) }
