package router

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zendev-sh/zenflow/internal/resume"
)

// Resumer is the Executor-side hook consulted by the Router when a
// message lands on a closed mailbox (target-terminal). When the hook
// reports CanResume=true, the Router calls ResumeStep instead of
// emitting the drop; the resume goroutine runs in the background and
// eventually routes a reverse Message back to the sender.
// Wiring: Executor implements Resumer (see executor.go) and installs
// itself via Router.SetResumer at the start of Run. When the Executor
// has no TranscriptStore (e.g. mailbox-delivery disabled), CanResume
// always returns false and the Router falls back to the pre-Phase-7.12
// target-terminal drop path.
// R4.
type Resumer interface {
	// CanResume returns true if stepID has a saved transcript and the
	// Executor is currently willing to spawn a resume (Run not cancelled).
	CanResume(stepID string) bool
	// ResumeStep loads the step's transcript, appends prompt as a
	// user turn, and spawns a fresh AgentRunner. Returns a
	// ResumeHandle the caller can block on via h.DoneCh. Errors on
	// transcript-missing / cap-exceeded / shutdown - the Router maps
	// those to DropReason* values.
	ResumeStep(ctx context.Context, stepID, prompt, fromAgent string) (*ResumeHandle, error)
}

// ResumeHandle is the per-resume control block returned by
// Executor.ResumeStep.
type ResumeHandle struct {
	// StepID is the terminated step that was resumed.
	StepID string
	// ResumeID is a per-invocation identifier used in event payloads
	// so operators can correlate EventResumeStarted /
	// EventResumeCompleted / EventResumeFailed for the same resume.
	ResumeID string
	// OriginalSender is the From field of the router message that
	// triggered the resume; the resumed AgentRunner's final assistant
	// response is routed back to this agent via reverse Message.
	// May be empty (e.g. external caller directly invoking ResumeStep);
	// the executor suppresses the reverse Send in that case.
	OriginalSender string
	// DoneCh is closed once the resume goroutine has finished - either
	// after a successful final assistant response or on Err.
	DoneCh chan struct{}
	// Result is the resumed agent's final assistant text. Populated
	// before DoneCh closes when Err == nil.
	Result string
	// Err is non-nil on failure (ctx-cancel, transcript cap,
	// AgentRunner error). Populated before DoneCh closes.
	Err error
}

// ErrResumeShutdown and the other sentinel errors below are returned by
// Resumer.ResumeStep for the Router hook to map into DropReason values.
// Kept here (rather than in transcript.go) so the Router can
// import/route them without pulling executor internals.
var (
	// ErrResumeShutdown signals the workflow ctx was cancelled mid-resume.
	// Router maps to DropReasonResumeShutdown.
	ErrResumeShutdown = errors.New("zenflow: resume cancelled by workflow shutdown")

	// ErrModelResolverMissing indicates the saved transcript references a
	// model identifier that does not match the Executor's default runner
	// model and no ModelResolver was configured to resolve it. Router
	// maps to DropReasonTargetTerminal (no dedicated reason - this is an
	// operator configuration error).
	ErrModelResolverMissing = errors.New("zenflow: resume: saved transcript model differs from executor model and no ModelResolver configured (use WithModelResolver)")

	// ErrModelResolverError indicates a ModelResolver WAS configured but
	// returned an error (or nil model with no error indicator) when
	// asked to resolve the saved transcript's model identifier. Distinct
	// from ErrModelResolverMissing so operators can tell "no resolver
	// installed" from "resolver ran and failed" (VA-6b). Router maps to
	// DropReasonTargetTerminal.
	ErrModelResolverError = errors.New("zenflow: resume: ModelResolver returned error")

	// ErrMailboxFullOnResume indicates a queued resume attempt was
	// rejected because the active resume's mailbox was already at its
	// configured cap. G2 - prevents false EventResumeQueued emission.
	// Router maps to DropReasonMailboxFull.
	ErrMailboxFullOnResume = errors.New("zenflow: resume: active resume mailbox full")
)

// Message is a message between agents or coordinator. Stable.
// MessageID is assigned by the MailboxStore on Append (callers leave
// it empty). It is the stable identity used by MarkRead's CAS
// dedup contract: MarkRead(stepID, ids) returns the subset of ids
// that were already marked read on a prior call. This lets concurrent
// drainers detect double-consume without holding a lock across LLM
// calls.
type Message struct {
	MessageID string
	From      string
	To        string
	Content   string
	Type      MessageType
	Metadata  map[string]string
	Timestamp time.Time
}

// MessageType classifies router messages. Stable.
//go:generate stringer -type=MessageType -linecomment -output=messagetype_string.go
type MessageType int

const (
	// MessageInfo is a general informational message.
	// Available for consumers to send informational messages between agents.
	MessageInfo MessageType = iota // info
	// MessageCancel requests the receiving agent to stop.
	MessageCancel // cancel
	// MessageContextUpdate injects new context into the agent's conversation.
	// Available for consumers to push context updates to running agents.
	MessageContextUpdate // context_update
	// MessageResumeReply is the reverse-routed reply produced by
	// Executor.runResume after a resumed step finishes. Tagged
	// distinctly from MessageInfo so observers can distinguish
	// resume responses from regular coordinator pushes. Drain logic
	// treats it the same as MessageInfo (appended as a user turn).
	MessageResumeReply // resume_reply
)

// String for MessageType is generated by stringer (messagetype_string.go).
// Used by Event.Data["type"] on EventCoordinatorInboxMessage so sinks can
// render reverse replies distinctly from regular info messages.

// MetadataKeyResumeReverse is a sentinel Metadata key set on reverse
// Messages produced by Executor.runResume. When Router.Send
// encounters a closed target AND observes this key, it does NOT cascade
// into a second ResumeStep invocation - it emits
// DropReasonTargetTerminal instead.
const MetadataKeyResumeReverse = "zenflow-resume-reverse"

// DropReason is the typed enumeration of reasons a router message can be dropped
// without ever reaching the target agent. Stable.
// Per the "no silent drops" invariant: every drop emits exactly one
// EventMessageDropped carrying one of these reasons.
// String returns the canonical wire-format value used in
// Event.Data["reason"] for backward-compat with subscribers reading the
// pre-typed reasons.
type DropReason int

const (
	// DropReasonUnspecified is the zero value; never emitted in practice.
	DropReasonUnspecified DropReason = iota
	// DropReasonWorkflowCancelled - workflow ctx cancelled or abort fired
	// before the message could be delivered to the target's LLM context.
	DropReasonWorkflowCancelled
	// DropReasonTargetTerminal - Send to a step whose mailbox was closed
	// (target reached a terminal lifecycle state).
	DropReasonTargetTerminal
	// DropReasonUnknownStep - Send to a stepID that was never registered
	// and has no pending senders.
	DropReasonUnknownStep
	// DropReasonMailboxClosedByFinalize - mailbox raced with a concurrent
	// close; the closed flag won.
	DropReasonMailboxClosedByFinalize
	// DropReasonMaxWakeCycles - wake-loop hit the maxWakeCycles cap with
	// messages still pending; remainder drained as drops.
	DropReasonMaxWakeCycles
	// DropReasonHoldTimeout - the executor's hold-timeout
	// fired before the 3-invariant termination rule could converge.
	// Any messages still buffered in the mailbox are emitted with this
	// reason and the step is force-terminated.
	DropReasonHoldTimeout
	// DropReasonMailboxFull - the bounded in-memory mailbox is at the
	// MaxMailboxSize cap configured via WithMaxMailboxSize. The newest
	// message is rejected (oldest-wins fairness).
	DropReasonMailboxFull
	// resume-mechanism drop reasons. Emitted by
	// Router.Send when a resume attempt on a terminated step cannot
	// proceed.
	// DropReasonNoTranscript - target mailbox was closed AND the
	// executor's TranscriptStore has no saved transcript for the step.
	// Typically observed for steps that ran before, or for
	// steps whose transcript was explicitly deleted.
	DropReasonNoTranscript
	// DropReasonTranscriptTooLarge - the saved transcript exceeds the
	// configured cap (WithMaxTranscriptMessages / WithMaxTranscriptBytes),
	// so a resume would exceed the size bound. Natural cycle-detection
	// bound.
	DropReasonTranscriptTooLarge
	// DropReasonResumeShutdown - workflow ctx was cancelled mid-resume;
	// the in-flight resume goroutine exited early. Surfaced by
	// Executor.ResumeStep when ctx.Done fires between Load and the
	// AgentRunner's final assistant response.
	DropReasonResumeShutdown
	// DropReasonResolverError - a configured ModelResolver was consulted
	// for a saved-transcript model identifier and returned an error.
	// Distinct from DropReasonTargetTerminal so operators can tell
	// "resolver infrastructure failure" from generic terminal drops.
	DropReasonResolverError
)

// DropReasonStrings returns a defensive copy of the canonical map of
// DropReason values to their wire-format strings. Production code
// should call DropReason.String instead; this accessor exists for
// tests that need to enumerate every reason without reaching into
// unexported state.
// Stable.
func DropReasonStrings() map[DropReason]string {
	out := make(map[DropReason]string, len(dropReasonStrings))
	for k, v := range dropReasonStrings {
		out[k] = v
	}
	return out
}

// dropReasonStrings is the single source of truth for DropReason wire-
// format strings. Keep this map in sync with the DropReason iota values
// above. Tests that assert the wire format (router_string_test.go,
// spec_parity_test.go, router_mailbox_coverage_test.go) MUST import
// this map rather than redefine the strings, so a rename in one place
// does not silently drift across the suite.
var dropReasonStrings = map[DropReason]string{
	DropReasonUnspecified:             "unspecified",
	DropReasonWorkflowCancelled:       "workflow-cancelled",
	DropReasonTargetTerminal:          "target-terminal",
	DropReasonUnknownStep:             "unknown-step",
	DropReasonMailboxClosedByFinalize: "mailbox-closed-by-finalize",
	DropReasonMaxWakeCycles:           "max-wake-cycles",
	DropReasonHoldTimeout:             "hold-timeout",
	DropReasonMailboxFull:             "mailbox-full",
	DropReasonNoTranscript:            "no-transcript",
	DropReasonTranscriptTooLarge:      "transcript-too-large",
	DropReasonResumeShutdown:          "resume-shutdown",
	DropReasonResolverError:           "resolver-error",
}

// String returns the canonical wire-format string for the reason. Stable.
// These values are stable and used as Event.Data["reason"] payloads.
// Unknown values (out-of-range integers) fall back to "unspecified" so
// a DropEvent emitted by an old binary against a new enum still has a
// useful payload.
func (r DropReason) String() string {
	if s, ok := dropReasonStrings[r]; ok {
		return s
	}
	return "unspecified"
}

// DropEvent describes a single message that was discarded by the router
// without ever being appended to the target step's mailbox. Stable.
// It is the payload the router hands to its OnDrop callback so the executor can
// emit one EventMessageDropped per drop.
// Reason is the typed DropReason. The legacy string field is preserved
// via Reason.String for subscribers that read Event.Data["reason"].
// The router does NOT emit EventMessageDropped itself (zenflow events
// require RunID + ProgressSink wiring); it instead hands a typed
// payload to the executor via OnDrop, which translates to the event.
type DropEvent struct {
	StepID string
	Msg    Message
	Reason DropReason
}

// Router routes Messages from coordinator/agents into the
// per-step MailboxStore. Stable.
// The cutover removed the legacy buffered-chan delivery path;
// mailbox is now the only path.
// Lifecycle:
// - SetMailbox(store) MUST be called before any Send. Without a mailbox,
// Send becomes a no-op + emits a "unknown-step" drop event for every
// message (no silent loss).
// - RegisterInbox(stepID) marks a step as live so Send routes deliveries
// into the mailbox. The router rejects Sends to unregistered steps
// unless a sender slot is open (PendingSenders>0), which captures the
// pre-start race window where coordinator narration may target a step
// whose runStep goroutine has not yet executed RegisterInbox.
// - Close(stepID) marks the step terminal: subsequent Sends emit
// "target-terminal" drops. The mailbox is also closed via Append's
// no-op-on-closed contract.
// Trust model: the Router is internal to the library. All senders
// (coordinator, parent agents) are trusted - there is no sender
// authentication. The From field is informational only.
type Router struct {
	mu      sync.RWMutex
	mailbox MailboxStore
	open    map[string]bool // stepIDs with active mailbox (RegisterInbox called, Close not)
	closed  map[string]bool // stepIDs whose mailbox was closed (terminal)
	onDrop  func(DropEvent) // executor-installed drop callback

	// per-target pending-senders counter. A "sender slot"
	// represents an entity (coordinator goroutine, sibling step, etc.)
	// that COULD still call Send(target, ...). When the counter reaches
	// zero AND the mailbox is empty AND the agent reached a stable idle
	// state, the executor is permitted to terminate the target step
	// (mailbox invariants). Open/Close/Pending live on Router
	// (rather than in a side struct) because every sender already holds
	// a Router reference; co-locating keeps the API single-noun.
	sendersMu sync.Mutex
	senders   map[string]int

	// Single per-run flag flipped when the workflow
	// is cancelled (ctx.Done OR abort strategy fires). Read by Send (to
	// short-circuit with DropReasonWorkflowCancelled), Close (to attribute
	// pending-msg drops to the cancel reason), and waitForStepTermination
	// (to break out of the wait early).
	workflowCancelled atomic.Bool

	// Per-step RWMutex registry. Each active step
	// owns a stepLock; State transitions, Mailbox Seal/Delete, and the
	// invariant-check snapshot all acquire this lock. The map itself is
	// guarded by stepLocksMu. Locks are created on demand by
	// AcquireStepLock and survive until ReleaseStepLock.
	stepLocksMu sync.Mutex
	stepLocks   map[string]*sync.RWMutex

	// G7 : registry of stepIDs known to the workflow. Populated
	// once per Run via RegisterStep before any goroutines spawn.
	// Distinct from `open` (which tracks RUNTIME mailbox availability -
	// flipped on RegisterInbox / Close): a step may be known to the
	// workflow even when its runStep goroutine has not yet started, or
	// has already finished. Used by Send to distinguish "valid sibling
	// target with no slot opened yet" from "typo / unknown step ID".
	knownMu  sync.Mutex
	known    map[string]bool
	wrappers map[string]bool // explicit wrapper-step set; protected by knownMu

	// resumer hook. When non-nil AND Send hits a
	// closed-mailbox path AND resumer.CanResume(stepID) returns true,
	// Router.Send dispatches to resumer.ResumeStep instead of emitting
	// DropReasonTargetTerminal. Protected by the main mu so
	// installation/teardown is atomic w.r.t. Send.
	resumer Resumer
	// runCtxProvider, when non-nil, returns the Executor's run-lifetime
	// context so that ResumeStep receives the correct cancellation scope
	// instead of context.Background. Set by SetResumer alongside the
	// resumer hook. Protected by r.mu.
	runCtxProvider func() context.Context

	// delegations route Send calls to a child router that
	// owns the actual inbox for the stepID. Used by nested-DAG
	// executors (loop iterations, forEach items, includes) to expose
	// their inner-step inboxes to the OUTER (root) router so coord's
	// forward_to_agent (which always sends via root) reaches inner
	// steps. Each entry maps a (namespaced) stepID to the router
	// that owns its mailbox. Lifecycle: Register on iteration start;
	// Unregister on iteration end. Send checks delegations FIRST and
	// recurses into the delegate's Send - short-circuiting the
	// outer-router's mailbox path entirely (no duplicate delivery).
	// Protected by delegationsMu.
	delegationsMu sync.RWMutex
	delegations   map[string]*Router

	// afterSend hook fired after a successful mailbox.Append
	// (NOT after dropped sends). Executor installs this to bridge
	// Router.Send → wakeRegistry.WakeTarget(stepID).SignalWake, giving
	// every recipient (steps AND coord) push-immediate wake instead of
	// waiting for the DeliveryEngine's next poll tick. Critical for
	// step→coord messaging via send_message: the coord runner is NOT in
	// ActiveSteps so the engine never polls its mailbox; without this
	// hook, send_message messages sit unread until the next lifecycle
	// event fires signalCoordWake (push/end), which may be after the
	// sender step has already exited. - receives the just-Append'd
	// message so the executor can bridge it into coord.Mailbox without
	// re-reading from the store. Protected by mu.
	afterSend func(stepID string, msg Message)
}

// RegisterStep records stepID as a member of the current workflow's DAG.
// Idempotent. Called once per workflow step at Run start (before any
// runStep goroutine spawns) so that Send(siblingTarget, ...) issued
// from a sibling step can auto-open a sender slot just-in-time when
// the F7 DAG-aware matrix has not pre-opened one. Steps not registered
// here continue to drop with DropReasonUnknownStep.
// G7  - preserves "zero silent drops" under future
// sibling-direct Send paths added after F7.
func (r *Router) RegisterStep(stepID string) {
	r.knownMu.Lock()
	defer r.knownMu.Unlock()
	if r.known == nil {
		r.known = make(map[string]bool)
	}
	r.known[stepID] = true
}

// RegisterWrapperStep marks stepID as a wrapper (loop / include
// container) that orchestrates sub-steps but does NOT host an agent
// of its own. Coord's `forward_to_agent` rejects messages targeting
// wrapper steps via IsWrapperStep - without this marker, Router.Send
// would silently accept the message into the wrapper's inbox where
// no agent ever reads it.
// explicit-marker approach. Earlier heuristic ("step X is
// wrapper if any other registered step starts with X.") didn't work
// because namespacing delegates inner-DAG registration to a
// child router; outer router never sees children, can't detect
// wrapper from prefix scan. Executor must call this explicitly when
// processing a step with Loop or Include definition.
// Idempotent. Concurrent-safe.
func (r *Router) RegisterWrapperStep(stepID string) {
	r.knownMu.Lock()
	defer r.knownMu.Unlock()
	if r.wrappers == nil {
		r.wrappers = make(map[string]bool)
	}
	r.wrappers[stepID] = true
}

// IsWrapperStep returns true if stepID was registered via
// RegisterWrapperStep. - used by `forward_to_agent` to reject
// wrapper targets with a helpful error before Router.Send (so the
// silent misroute never happens).
// Concurrent-safe.
func (r *Router) IsWrapperStep(stepID string) bool {
	r.knownMu.Lock()
	defer r.knownMu.Unlock()
	if r.wrappers == nil {
		return false
	}
	return r.wrappers[stepID]
}

// Inboxes returns a snapshot of every inbox the router is currently
// tracking (both open and already-closed). Used by tests that need to
// assert which step IDs were registered as inboxes; production code
// should use KnownSteps which also includes wrapper / pending-sender
// stepIDs.
// Stable.
func (r *Router) Inboxes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.open)+len(r.closed))
	for id := range r.open {
		out = append(out, id)
	}
	for id := range r.closed {
		out = append(out, id)
	}
	return out
}

// KnownSteps returns a sorted snapshot of step IDs registered via
// RegisterStep. Used by coord prompt construction (inject "AVAILABLE
// STEPS" menu) and by forward_to_agent's unknown-step error feedback
// (return helpful message listing valid IDs). Concurrent-safe; returns
// a fresh slice copy so callers may iterate without holding the
// router's internal lock.
// Includes inner-DAG steps registered by nested executors (loop
// iterations, forEach items, include sub-workflows) - the namespaced
// IDs (e.g. "loopID.0.innerStep") materialise into the known set as
// each iteration starts. Coord prompt should refresh this list per
// wake to reflect the current snapshot.
// Stable.
func (r *Router) KnownSteps() []string {
	r.knownMu.Lock()
	defer r.knownMu.Unlock()
	if len(r.known) == 0 {
		return nil
	}
	return slices.Sorted(maps.Keys(r.known))
}

// HasStepLock reports whether stepID currently has an entry in the
// per-step lock registry. Intended for tests that need to assert
// release-on-Close behaviour without reaching into unexported state.
// Stable.
func (r *Router) HasStepLock(stepID string) bool {
	r.stepLocksMu.Lock()
	defer r.stepLocksMu.Unlock()
	_, ok := r.stepLocks[stepID]
	return ok
}

// AcquireStepLock returns the per-step RWMutex for stepID, creating it
// if necessary. Lifecycle code uses Lock for state
// transitions / Seal / Delete, the invariant-check uses RLock to take
// a coherent snapshot of (state, mailbox-len, pending-senders).
// Locks are reference-counted only by presence in the map: the executor
// is responsible for calling ReleaseStepLock when the step is fully
// retired.
func (r *Router) AcquireStepLock(stepID string) *sync.RWMutex {
	r.stepLocksMu.Lock()
	defer r.stepLocksMu.Unlock()
	if r.stepLocks == nil {
		r.stepLocks = make(map[string]*sync.RWMutex)
	}
	lk, ok := r.stepLocks[stepID]
	if !ok {
		lk = &sync.RWMutex{}
		r.stepLocks[stepID] = lk
	}
	return lk
}

// ReleaseStepLock removes the per-step RWMutex from the registry. Safe
// to call even when no lock was acquired (no-op).
func (r *Router) ReleaseStepLock(stepID string) {
	r.stepLocksMu.Lock()
	defer r.stepLocksMu.Unlock()
	delete(r.stepLocks, stepID)
}

// - compile-time assertion that *Router satisfies
// EngineStepLocker (the per-step lock contract delivery_engine
// consumes via WithStepLocker(e.Router)).
var _ EngineStepLocker = (*Router)(nil)

// NewRouter creates a new Router. Stable.
func NewRouter() *Router {
	return &Router{
		open:      make(map[string]bool),
		closed:    make(map[string]bool),
		senders:   make(map[string]int),
		stepLocks: make(map[string]*sync.RWMutex),
		known:     make(map[string]bool),
	}
}

// SetMailbox installs a MailboxStore as the delivery backend.
// Send observes nil mailbox until SetMailbox completes - call
// SetMailbox during setup before Send starts arriving.
func (r *Router) SetMailbox(store MailboxStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mailbox = store
}

// Mailbox returns the installed MailboxStore (or nil when unset).
// Callers use it to drain non-step inboxes such as "coordinator"
func (r *Router) Mailbox() MailboxStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mailbox
}

// SetResumer installs the Executor-side resume hook. May be called
// multiple times; the last setter wins. Passing nil disables resume
// (Send reverts to emitting DropReasonTargetTerminal for closed
// mailboxes). Safe to call before the Executor begins running - Send
// reads resumer under r.mu.RLock.
// R4.
func (r *Router) SetResumer(res Resumer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resumer = res
}

// SetRunCtxProvider installs a function that returns the Executor's
// run-lifetime context. When set, Send passes the result of ctxProvider
// to ResumeStep instead of context.Background, so cancellation of the
// workflow propagates correctly into resume goroutines. Called alongside
// SetResumer at the start of Run.
func (r *Router) SetRunCtxProvider(ctxProvider func() context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runCtxProvider = ctxProvider
}

// SetOnDrop installs a callback invoked once per dropped message.
// The executor wires this so router-side drops (target-terminal,
// unknown-step, mailbox-closed-by-finalize) translate to
// EventMessageDropped - preserving the "zero silent drops" contract
func (r *Router) SetOnDrop(fn func(DropEvent)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onDrop = fn
}

// RegisterDelegate adds a delegation entry: any future Send to stepID
// will be forwarded to delegate.Send instead of going through this
// router's own mailbox path. - used by nested-DAG executors
// (loop iterations, forEach items, includes) to expose their
// inner-step inboxes to the OUTER router so coord's forward_to_agent
// can reach inner steps. Replaces any prior delegation for the same
// stepID (sequential iterations of repeat-until rely on this - each
// iteration replaces the prior's delegation). Pass nil delegate to
// remove (equivalent to UnregisterDelegate).
func (r *Router) RegisterDelegate(stepID string, delegate *Router) {
	r.delegationsMu.Lock()
	defer r.delegationsMu.Unlock()
	if r.delegations == nil {
		r.delegations = make(map[string]*Router)
	}
	if delegate == nil {
		delete(r.delegations, stepID)
		return
	}
	r.delegations[stepID] = delegate
}

// UnregisterDelegate removes the delegation entry for stepID. No-op
// if no entry exists. Called by nested-DAG executors when an
// iteration ends so the next iteration (or workflow termination)
// doesn't see stale routing.
func (r *Router) UnregisterDelegate(stepID string) {
	r.delegationsMu.Lock()
	defer r.delegationsMu.Unlock()
	delete(r.delegations, stepID)
}

// getDelegate returns the registered delegate for stepID or nil.
// Internal lookup used by Send. Held under RLock for the duration of
// the lookup - caller may call delegate.Send afterward without
// holding this lock (delegations map is only mutated by
// Register/Unregister which take the write lock).
func (r *Router) getDelegate(stepID string) *Router {
	r.delegationsMu.RLock()
	defer r.delegationsMu.RUnlock()
	return r.delegations[stepID]
}

// SetAfterSend installs the post-Append hook fired after a successful
// Router.Send.: executor uses this to bridge Router.Send →
// wakeRegistry, giving every recipient (steps + coord) push-immediate
// wake instead of waiting for the DeliveryEngine's poll tick.:
// also passed the Message so the executor can bridge it into the
// coord runner's separate Mailbox. Drops (closed mailbox, unknown
// step, etc.) do NOT fire this hook - only successful Appends. Pass
// nil to disable.
func (r *Router) SetAfterSend(fn func(stepID string, msg Message)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.afterSend = fn
}

// RegisterInbox marks stepID as having a live mailbox. Call this
// before the step's AgentRunner.Run begins consuming the mailbox.
// Idempotent: re-registering an already-open step is a no-op; if the
// step was previously closed, RegisterInbox clears the closed flag and
// reopens it.
func (r *Router) RegisterInbox(stepID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.open == nil {
		r.open = make(map[string]bool)
	}
	if r.closed == nil {
		r.closed = make(map[string]bool)
	}
	r.open[stepID] = true
	delete(r.closed, stepID)
}

// OpenSender increments the pending-senders counter for targetStepID.
// every entity that may call Send(targetStepID, ...) MUST
// take a sender slot via OpenSender + defer CloseSender so the executor's
// 3-invariant termination check (no senders + empty mailbox + stable idle)
// can determine when it is safe to close the target's mailbox.
// The counter is per-target (not per-sender-identity) because the
// invariant only cares whether any sender is still in flight, not who.
func (r *Router) OpenSender(targetStepID string) {
	r.sendersMu.Lock()
	defer r.sendersMu.Unlock()
	if r.senders == nil {
		r.senders = make(map[string]int)
	}
	r.senders[targetStepID]++
}

// CloseSender decrements the pending-senders counter for targetStepID.
// Defensive: clamps at zero - a Close without a matching Open is a
// no-op rather than an underflow into negative numbers, which would
// mask later sender leaks behind a phantom positive count. Tests must
// continue to pair Open/Close, but production paths get a safety net.
func (r *Router) CloseSender(targetStepID string) {
	r.sendersMu.Lock()
	defer r.sendersMu.Unlock()
	if r.senders == nil {
		return
	}
	n := r.senders[targetStepID]
	if n <= 0 {
		return
	}
	if n == 1 {
		delete(r.senders, targetStepID)
		return
	}
	r.senders[targetStepID] = n - 1
}

// PendingSenders returns the current pending-senders count for
// targetStepID. Used by the executor's 3-invariant termination check.
func (r *Router) PendingSenders(targetStepID string) int {
	r.sendersMu.Lock()
	defer r.sendersMu.Unlock()
	return r.senders[targetStepID]
}

// Send delivers a message to stepID's mailbox. Non-blocking. Stable.
// Drops emit one DropEvent via OnDrop (when configured) so the
// executor can surface EventMessageDropped - never silent.
// Send now ALSO returns an error so per-call drops can surface
// to in-process callers (notably the send_message and forward_to_agent
// tools, which render the drop reason into their LLM-visible result
// string). The error is in addition to the OnDrop callback (which the
// executor uses to emit EventMessageDropped) - both fire on every drop.
// Returns nil on accept (mailbox.Append succeeded with no concurrent
// Close race). Returned errors are formatted as
// `"dropped: <reason>"` where `<reason>` is the canonical
// DropReason.String value, so callers can pass the error message
// directly through to LLM-visible tool results without reformatting.
// Drop matrix:
// - mailbox not configured → "unknown-step"
// - stepID never registered AND no pending senders → "unknown-step"
// - stepID was closed → "target-terminal"
// - mailbox.Append succeeds → no drop
// Drops while a sender slot is open (PendingSenders>0) for an
// unregistered step are NOT emitted as "unknown-step": the sender
// slot is the executor's promise that RegisterInbox is imminent. The
// message is still appended to the mailbox so the late-arriving step
// will see it on first Unread. (MailboxStore.Append never fails; a
// concurrent Close racing with Append is handled by the store's own
// closed-flag check and emits "mailbox-closed-by-finalize" via the
// Closed-after-Append fallback below.)
func (r *Router) Send(stepID string, msg Message) error {
	// delegation short-circuit. If a child router was
	// registered for this stepID via RegisterDelegate, forward Send
	// to it directly. The delegate has the actual mailbox + wake
	// target for the inbox; this router's own mailbox path is
	// skipped. Used to expose nested-DAG inner steps to the root
	// router (coord always uses root via its
	// `runner.Router = e.Router` wiring). Recursion terminates
	// because nested executors don't re-delegate the same stepID
	// inside themselves (delegate is the actual owner).
	if delegate := r.getDelegate(stepID); delegate != nil && delegate != r {
		return delegate.Send(stepID, msg)
	}
	// F5 - hold the per-step RLock for the duration of Send so a
	// concurrent Close (which takes the write lock) is serialised
	// against in-flight Sends. Without this, a Close happening between
	// our snapshot below and mailbox.Append could land the message in
	// a mailbox that's already been Close'd, surfacing as a silent
	// race instead of a deterministic drop.
	stepLock := r.AcquireStepLock(stepID)
	stepLock.RLock()
	defer stepLock.RUnlock()

	r.mu.RLock()
	mailbox := r.mailbox
	closed := r.closed[stepID]
	open := r.open[stepID]
	onDrop := r.onDrop
	resumer := r.resumer
	runCtxProvider := r.runCtxProvider
	afterSend := r.afterSend
	r.mu.RUnlock()

	// emit fires the OnDrop callback (when configured) AND returns a
	// formatted error so the caller can surface the drop reason as a
	// per-call result. The two paths are intentionally redundant -
	// OnDrop drives the executor's EventMessageDropped pipeline; the
	// returned error drives in-process consumers (send_message /
	// forward_to_agent tools) that need the reason in the LLM-visible
	// tool result. Format `dropped: <reason>` mirrors the canonical
	// tool-result string so tools can pass err.Error through verbatim.
	emit := func(reason DropReason) error {
		if onDrop != nil {
			onDrop(DropEvent{StepID: stepID, Msg: msg, Reason: reason})
		}
 // Typed *DropError so callers can extract the reason via
 // errors.As instead of parsing the message string. Error
 // returns the same canonical "dropped: <reason>" text used
 // by tools that pass err.Error through verbatim - no
 // observable behaviour change for substring-matching callers.
		return &DropError{Reason: reason}
	}

	// workflowCancelled short-circuit.
	if r.workflowCancelled.Load() {
		return emit(DropReasonWorkflowCancelled)
	}

	if mailbox == nil {
		return emit(DropReasonUnknownStep)
	}
	if closed {
 // resume hook. If a resumer is installed and
 // reports it can resume this stepID, hand the message off to
 // ResumeStep instead of dropping. Error mapping:
 // - ErrNoTranscript → DropReasonNoTranscript
 // - ErrTranscriptTooLarge → DropReasonTranscriptTooLarge
 // - ErrResumeShutdown → DropReasonResumeShutdown
 // - other → DropReasonTargetTerminal
 // (so the contract never regresses into silent loss when
 // the resume path returns a novel error)
 // F7: if this Send is a resume-reply bouncing off a sealed
 // sender, do NOT cascade-resume. Attribute as target-terminal
 // drop and stop.
		isResumeReverse := false
		if msg.Metadata != nil {
			if _, ok := msg.Metadata[MetadataKeyResumeReverse]; ok {
				isResumeReverse = true
			}
		}
		if isResumeReverse {
			return emit(DropReasonTargetTerminal)
		}
		if resumer != nil && resumer.CanResume(stepID) {
 // Use the Executor's run-lifetime context so that if the
 // workflow is cancelled, ResumeStep observes ctx.Done.
 // Fall back to context.Background only when the provider
 // is not set (e.g. in unit tests that use a bare router).
			resumeCtx := context.Background()
			if runCtxProvider != nil {
				resumeCtx = runCtxProvider()
			}
			_, err := resumer.ResumeStep(resumeCtx, stepID, msg.Content, msg.From)
			if err == nil {
 // Successful handoff - no drop emitted. Events
 // (EventResumeStarted / Completed / Failed) are
 // emitted by the Executor inside ResumeStep.
				return nil
			}
			switch {
			case errors.Is(err, resume.ErrNoTranscript):
				return emit(DropReasonNoTranscript)
			case errors.Is(err, resume.ErrTranscriptTooLarge):
				return emit(DropReasonTranscriptTooLarge)
			case errors.Is(err, ErrResumeShutdown):
				return emit(DropReasonResumeShutdown)
			case errors.Is(err, ErrMailboxFullOnResume):
				return emit(DropReasonMailboxFull)
			case errors.Is(err, ErrModelResolverError):
 // distinguish infrastructure resolver failure
 // from generic terminal/unknown drops.
				return emit(DropReasonResolverError)
			default:
				return emit(DropReasonTargetTerminal)
			}
		}
		return emit(DropReasonTargetTerminal)
	}
	if !open && r.PendingSenders(stepID) == 0 {
 // G7 : with the F7 DAG-aware sender matrix,
 // runStep no longer pre-opens N×N sibling slots - it opens
 // only the per-step coordinator slot. A *future* sender
 // path (sibling-direct Send) could target a workflow step
 // whose slot was never opened. Without intervention the
 // message would silently drop as DropReasonUnknownStep -
 // regressing the F7 perf win into a correctness loss.
 // Mitigation: if the target stepID is known to the workflow
 // (RegisterStep was called at Run start), auto-open a
 // sender slot just for the duration of this Append. The
 // message lands in the mailbox like any other Send, and
 // the per-step termination invariant is preserved because
 // CloseSender fires immediately on return. Truly unknown
 // step IDs (typo, removed step, send-to-coordinator from
 // outside the run) still emit DropReasonUnknownStep.
		r.knownMu.Lock()
		isKnown := r.known[stepID]
		r.knownMu.Unlock()
		if !isKnown {
			return emit(DropReasonUnknownStep)
		}
		r.OpenSender(stepID)
		defer r.CloseSender(stepID)
	}

	// Append. If a concurrent Close raced and the store's closed-flag
	// won, Append silently dropped the message - surface that as a
	// dedicated reason so the contract holds. F3 - bounded stores
	// return ErrMailboxFull on overflow which we surface as
	// DropReasonMailboxFull.
	assignedID, appendErr := mailbox.Append(stepID, msg)
	if errors.Is(appendErr, ErrMailboxFull) {
		return emit(DropReasonMailboxFull)
	}
	if appendErr != nil {
 // Custom MailboxStore implementations (file/sqlite/redis backends)
 // may return errors for serialization failures, backend timeouts,
 // or quota exceeded. We don't have a typed reason for each, so
 // surface them through the generic ResolverError reason which
 // already documents "store-side problem, not a routing decision."
 // Without this branch the message would be silently treated as
 // delivered, breaking the zero-silent-drops contract for
 // non-reference stores.
		return emit(DropReasonResolverError)
	}
	// populate the just-assigned MessageID on the local copy so
	// afterSend hooks (especially the coord bridge) can MarkRead in this
	// mailbox by ID - converting the bridge from copy semantics to move
	// semantics. Without the populated ID, drainCoordReverseReplies would
	// re-emit the bridged message as a duplicate (resumed) event.
	msg.MessageID = assignedID
	if mc, ok := mailbox.(interface{ Closed(string) bool }); ok && mc.Closed(stepID) {
 // Best-effort: re-check closed after Append. If the close beat
 // our append we cannot tell whether the message landed or was
 // dropped, so emit the dedicated race reason. False positives
 // (Close happened after Append landed) are acceptable - they
 // over-report rather than under-report, satisfying "zero silent
 // drops" without risking missed events.
		return emit(DropReasonMailboxClosedByFinalize)
	}
	// signal recipient wake immediately. Critical for step→coord
	// messaging via send_message - coord is NOT in ActiveSteps so the
	// DeliveryEngine never polls its mailbox; without this hook, the
	// message sits unread until the next lifecycle event fires
	// signalCoordWake (which may be after the sender step has already
	// exited). Steps already get wake from the engine's poll, so this is
	// just a faster path for them; double-wake is harmless (chan cap 1).
	if afterSend != nil {
		afterSend(stepID, msg)
	}
	return nil
}

// Close marks stepID's mailbox as terminal. Subsequent Sends emit
// "target-terminal" drops via OnDrop. The underlying mailbox.Close
// is invoked so any future Append is a no-op at the store layer too.
// any messages that were Appended to the mailbox but
// never drained by the agent (e.g. coordinator messages that arrived
// during the agent's last LLM call, or that arrived after the agent
// reached idle but before the executor's flush ran) MUST emit a
// DropEvent so the "zero silent drops" contract holds. Without this,
// the abort path (`flushMailboxOnAbort`) is the only safety net - and
// it only fires when a step goroutine fails to send `done` before the
// workflow ctx cancels. Steps that respond quickly to ctx-cancel and
// signal done would otherwise lose pending messages silently.
// The drop reason for these messages is "target-terminal" - the same
// reason emitted for post-Close Sends - because semantically the
// target is no longer available to receive them.
// Idempotent: a second Close on the same stepID re-takes the lock,
// observes an empty mailbox (already drained), re-marks the step
// terminal (already true), and re-releases the lock. No drops fire
// twice. Production code paths only Close each stepID once; this
// guarantee is documented for embedders that wire their own retry
// loops.
func (r *Router) Close(stepID string) {
	// F5 - write-lock the per-step lock so any in-flight Send completes
	// before we mark the step terminal. The two-stage discipline
	// (Send: RLock; Close: Lock) prevents the close-during-append race
	// where a message lands in a mailbox that we are about to flush.
	stepLock := r.AcquireStepLock(stepID)
	stepLock.Lock()
	// (2026-05-04) - release the per-step RWMutex from the
	// stepLocks registry once we're done with it. The original
	// design had the executor own ReleaseStepLock as part of its
	// per-step teardown, but no production code path ever called it,
	// so stepLocks grew unbounded over the life of a long-running
	// Router (one *sync.RWMutex per distinct step ever
	// touched). Calling it here in Close makes release symmetric with
	// the natural lifecycle endpoint for the step. Late Sends that
	// arrive after Close just take a fresh mutex via AcquireStepLock,
	// see r.closed[stepID]==true, and emit a DropReasonTargetTerminal
	// - no semantic difference from before.
	defer r.ReleaseStepLock(stepID)
	defer stepLock.Unlock()

	r.mu.Lock()
	mailbox := r.mailbox
	onDrop := r.onDrop
	if r.closed == nil {
		r.closed = make(map[string]bool)
	}
	r.closed[stepID] = true
	delete(r.open, stepID)
	r.mu.Unlock()
	if mailbox == nil {
		return
	}
	// Drain any still-pending messages and emit drops BEFORE closing
	// the mailbox. After Close the store discards the queue silently,
	// so the order matters.
	if onDrop != nil {
		reason := DropReasonTargetTerminal
 // S4: if workflow was cancelled, attribute drops to that cause
 // instead of the generic terminal reason - operators need to
 // distinguish abort drops from natural end-of-step drops.
		if r.workflowCancelled.Load() {
			reason = DropReasonWorkflowCancelled
		}
		for _, msg := range mailbox.Unread(stepID) {
			onDrop(DropEvent{StepID: stepID, Msg: msg, Reason: reason})
		}
	}
	mailbox.Close(stepID)
}

// MarkWorkflowCancelled flips the router into a cancelled state. After
// this call:
// - all subsequent Send calls return immediately with a
// DropReasonWorkflowCancelled drop event
// - subsequent Close calls attribute pending drops to
// DropReasonWorkflowCancelled (instead of the generic terminal
// reason) so operators can distinguish abort drops from natural
// end-of-step drops
// Idempotent: safe to call multiple times.
func (r *Router) MarkWorkflowCancelled() {
	r.workflowCancelled.Store(true)
}

// WorkflowCancelled reports whether MarkWorkflowCancelled has been
// called on this router. The poller and waitForStepTermination consult
// this flag to short-circuit waits when the workflow is being torn down.
func (r *Router) WorkflowCancelled() bool {
	return r.workflowCancelled.Load()
}
