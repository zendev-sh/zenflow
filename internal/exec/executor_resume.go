package exec

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/resume"
	"github.com/zendev-sh/zenflow/internal/router"
	"github.com/zendev-sh/zenflow/internal/types"
)

// resumeState tracks in-flight and queued resume attempts for a single
// stepID so that concurrent ResumeStep calls serialize correctly
// (at most one in-flight resume per stepID; further sends queue into
// the running agent's mailbox via the Wake mechanism).
type resumeState struct {
	mu      sync.Mutex
	running bool
	// activeMailbox is the mailbox of the currently-running resume, if
	// any. Router.Send to the same stepID while running=true may
	// append to this mailbox so the running resume picks up the
	// follow-on turn via Wake.
	activeMailbox MailboxStore
	activeWake    chan struct{}
	// activeResumeID is the ResumeID of the currently-running resume.
	// Observers (queued-path event emission, operator telemetry) read
	// this under state.mu to correlate EventResumeQueued with the
	// in-flight EventResumeStarted. Empty when no resume is running.
	// VA-4b.
	activeResumeID string
}

// resumeCapMailbox wraps a MailboxStore and makes cap enforcement
// authoritative by counting Appended-but-not-MarkRead'd MessageIDs
// tracked in a wrapper-local set, independent of the inner queue's
// length semantics.
// What the wrapper DOES:
// - Keeps an explicit `outstanding` set of MessageIDs that have been
// Appended through this wrapper but not yet MarkRead'd. That set's
// size IS the cap-relevant queue depth - a single source of truth
// regardless of how the inner store counts things.
// - Append rejects with ErrMailboxFull when len(outstanding) >= cap,
// under a single mutex hold, so cap checks are linearizable with
// respect to concurrent Append/MarkRead calls on the same wrapper.
// What the wrapper does NOT do:
// - It does NOT synthesize back-pressure after a consumer legitimately
// drains a queued message. When the resume goroutine's pre-start
// drain MarkReads a message, the slot is freed and a subsequent
// Append is admitted - that is standard mailbox semantics, not a
// race. Tests that need a stable "N queued, N+1 rejected" invariant
// must gate the pre-start drain (see WithRunnerPreStartDrainGate).
// The wrapper is installed only on the resume queue path (ResumeStep
// appends; the resume goroutine's AgentRunner drains). Other mailbox
// callers continue to see the underlying store directly.
type resumeCapMailbox struct {
	inner   MailboxStore
	maxSize int
	mu      sync.Mutex
	// outstanding tracks MessageIDs Appended via this wrapper that have
	// not yet been MarkRead'd. Its size IS the cap-relevant queue depth.
	outstanding map[string]struct{}
}

// errNilResumeCapInner signals that newResumeCapMailbox was called
// with a nil inner store. Returned instead of panicking so callers
// (Executor.ResumeStep + tests) can surface the misuse via the error
// path. Internal so it does not leak into the public surface; the
// exported translation is exec.ErrInvalidResumeMailboxInner if needed.
var errNilResumeCapInner = errors.New("zenflow: resume cap mailbox inner store must be non-nil")

// newResumeCapMailbox is an internal (lowercase) constructor with a
// single production caller (Executor.ResumeStep) that always passes a
// freshly-constructed NewInMemoryMailboxStore. A nil inner store at
// this call site is a programming error in the executor itself; the
// constructor returns errNilResumeCapInner so the caller surfaces the
// misuse via the error path rather than crashing the process. Tests
// that pass nil deliberately exercise this guard.
func newResumeCapMailbox(inner MailboxStore, maxSize int) (*resumeCapMailbox, error) {
	if inner == nil {
		return nil, errNilResumeCapInner
	}
	return &resumeCapMailbox{
		inner:       inner,
		maxSize:     maxSize,
		outstanding: make(map[string]struct{}),
	}, nil
}

// Append implements MailboxStore. Pre-checks the cap against our own
// outstanding count (NOT against inner's queue length) so a concurrent
// drain+MarkRead cannot create a window in which a new Append sees the
// queue below cap. On successful inner Append, records the returned id
// as outstanding.
func (c *resumeCapMailbox) Append(stepID string, msg RouterMessage) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maxSize > 0 && len(c.outstanding) >= c.maxSize {
		return "", ErrMailboxFull
	}
	id, err := c.inner.Append(stepID, msg)
	if err != nil || id == "" {
		return id, err
	}
	c.outstanding[id] = struct{}{}
	return id, nil
}

// Unread implements MailboxStore.
func (c *resumeCapMailbox) Unread(stepID string) []RouterMessage {
	return c.inner.Unread(stepID)
}

// MarkRead implements MailboxStore. Decrements the outstanding set by
// the ids being consumed BEFORE delegating so a concurrent Append that
// observes the decremented counter is deterministically admitted (never
// exceeding the cap, because Append re-takes c.mu).
func (c *resumeCapMailbox) MarkRead(stepID string, ids []string) []string {
	if len(ids) == 0 {
		return c.inner.MarkRead(stepID, ids)
	}
	c.mu.Lock()
	for _, id := range ids {
		delete(c.outstanding, id)
	}
	c.mu.Unlock()
	return c.inner.MarkRead(stepID, ids)
}

// Close implements MailboxStore.
func (c *resumeCapMailbox) Close(stepID string) {
	c.mu.Lock()
	c.outstanding = make(map[string]struct{})
	c.mu.Unlock()
	c.inner.Close(stepID)
}

// Len forwards to inner. The inner store is constructed via
// NewInMemoryMailboxStore (see ResumeStep) which implements LenAware;
// we keep the type-assertion for defensive decoupling but require
// LenAware at runtime to avoid silently reporting a len(Unread)
// estimate that does not match the inner's authoritative counts.
func (c *resumeCapMailbox) Len(stepID string) (unread, total int) {
	return c.inner.(LenAware).Len(stepID)
}

// Closed forwards to inner if it implements ClosedAware.
func (c *resumeCapMailbox) Closed(stepID string) bool {
	if ca, ok := c.inner.(ClosedAware); ok {
		return ca.Closed(stepID)
	}
	return false
}

// outstandingCount returns the current number of Appended-but-unread
// messages tracked by this wrapper. Test-only helper.
func (c *resumeCapMailbox) outstandingCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.outstanding)
}

// Seal forwards to inner if it implements Seal (InMemoryMailboxStore
// method). Declared only to satisfy any future Seal contract; currently
// a best-effort forwarder.
func (c *resumeCapMailbox) Seal(stepID string) {
	if s, ok := c.inner.(interface{ Seal(string) }); ok {
		s.Seal(stepID)
	}
}

// Compile-time assertions.
var (
	_ MailboxStore = (*resumeCapMailbox)(nil)
	_ LenAware     = (*resumeCapMailbox)(nil)
	_ ClosedAware  = (*resumeCapMailbox)(nil)
)

// ResumeStates is the Executor's per-stepID resume coordination
// registry. A sync.Map backs a map of stepID → *resumeState so lookups
// never block producers.
type ResumeStates struct {
	mu sync.Mutex
	m  map[string]*resumeState
}

func NewResumeStates() *ResumeStates {
	return &ResumeStates{m: make(map[string]*resumeState)}
}

func (r *ResumeStates) get(stepID string) *resumeState {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.m[stepID]
	if !ok {
		s = &resumeState{}
		r.m[stepID] = s
	}
	return s
}

// resumeShutdownTimeout bounds Run's wait for in-flight resume
// goroutines after ctx cancel (F8). Set high enough that a well-behaved
// LLM provider can observe ctx.Done and return, low enough that a
// wedged provider cannot hold Run hostage.
// Exported as a var (not const) so tests can shrink it to exercise the
// leak-telemetry branch deterministically.
var resumeShutdownTimeout = 5 * time.Second

// resumeIDRandRead is the entropy source used by the default resumeIDGen
// closure. Package-level var so a coverage test can inject a failing
// reader to exercise the slog.Warn error branch (otherwise unreachable:
// crypto/rand.Read on a healthy system never fails).
var resumeIDRandRead = rand.Read

// resumeIDGen returns an opaque per-resume identifier.
var resumeIDGen = func() string {
	b := make([]byte, 6)
	if _, err := resumeIDRandRead(b); err != nil {
		slog.Warn("crypto/rand.Read failed", "err", err)
	}
	return fmt.Sprintf("resume_%x", b)
}

// CanResume implements resumer. Reports true when the Executor has a
// transcript store AND the Run ctx has not been cancelled. The
// transcript lookup itself happens inside ResumeStep - CanResume is a
// cheap gate that lets the Router skip the resumer path entirely when
// the feature is not active.
func (e *Executor) CanResume(_ string) bool {
	if e == nil {
		return false
	}
	if e.transcriptStore == nil {
		return false
	}
	// Workflow-cancelled short-circuit. If the router
	// knows the run is cancelled, skip resume; the pending drop will
	// be attributed to the cancel cause by the normal drop path.
	if e.Router != nil && e.Router.WorkflowCancelled() {
		return false
	}
	return true
}

// ResumeStep implements resumer. Spawns (or queues into) a fresh
// AgentRunner for stepID seeded with the step's saved transcript plus
// the caller-supplied prompt as a new user turn. Returns a
// *ResumeHandle the caller can block on via h.DoneCh.
// Concurrency model:
// - At most one resume goroutine runs per stepID at a time.
// - If a resume is already in flight, the new message is appended
// to the running resume's mailbox and the pre-existing handle is
// NOT returned - a fresh handle is created and its DoneCh is
// closed immediately with Err=nil and the signal that the
// message was queued (Result="queued"). Further ResumeStep calls
// never block on in-flight resumes, they just enqueue.
// - On Run shutdown (ctx cancelled by Run's cancelRun defer),
// ResumeStep returns ErrResumeShutdown which the Router maps to
// DropReasonResumeShutdown.
// NOTE (cross-run scope,): the default model-fidelity path matches
// a saved transcript's Model against the user-supplied workflow step
// string tracked in Executor.stepModelStrings. That map is per-Executor
// memory and is NOT persisted. After a process restart - even with a
// persistent transcript store - the map is empty, so cross-run resume
// for a step whose model string differs from the default runner model
// REQUIRES a registered ModelResolver (see WithModelResolver).
// Intra-run resume needs no resolver: the step's model string is
// recorded at runStep and available for the lifetime of the Executor.
// Reverse routing on success.
func (e *Executor) ResumeStep(ctx context.Context, stepID, prompt, fromAgent string) (*ResumeHandle, error) {
	if e == nil || e.transcriptStore == nil {
		return nil, resume.ErrNoTranscript
	}
	if e.Router != nil && e.Router.WorkflowCancelled() {
		return nil, ErrResumeShutdown
	}

	// Lazily allocate the per-step coordination registry under the
	// executor's resumesMu. The prior double-checked-lock pattern was
	// racy under -race because the initial nil read was unsynchronized.
	// Holding the mutex for the full check-and-allocate is cheap (N^-1
	// ResumeStep calls see a hit) and correct.
	e.resumesMu.Lock()
	if e.resumes == nil {
		e.resumes = NewResumeStates()
	}
	resumes := e.resumes
	e.resumesMu.Unlock()
	state := resumes.get(stepID)

	// F2: allocate mailbox + wake up front so that when we flip
	// running=true we can publish them atomically under state.mu.
	// This closes the race window where a second concurrent caller
	// previously saw running=true && activeMailbox==nil and fell
	// through to spawn a second resume goroutine.
	// wrap the bounded in-memory store with resumeCapMailbox so
	// the cap is enforced by a wrapper-local counter of outstanding
	// (appended-but-not-MarkRead'd) MessageIDs rather than by the inner
	// store's queue length. This makes concurrent Append/MarkRead
	// cap-checks linearizable. Note this does NOT synthesize cap
	// pressure when a consumer legitimately drains - see
	// resumeCapMailbox's doc comment above.
	// newResumeCapMailbox returns errNilResumeCapInner only when the
	// inner store is nil; we always pass a freshly-constructed
	// NewInMemoryMailboxStore here so that branch is unreachable.
	// The constructor's nil-guard is exercised directly in
	// resume_coverage_test.go to keep the public-ish API contract honest.
	resumeMailbox, _ := newResumeCapMailbox(NewInMemoryMailboxStore(), e.MaxMailboxSize)
	resumeWake := make(chan struct{}, 1)

	// pre-allocate the ResumeID so it can be published under the
	// SAME critical section that flips running=true. Prior code set
	// activeResumeID in a second lock acquisition AFTER transcript Load,
	// leaving a window where a queued-path caller could observe
	// running=true with activeResumeID="" (stale). Atomic publication
	// closes that correlation race.
	preallocatedResumeID := resumeIDGen()

	state.mu.Lock()
	if state.running {
		// Queue path - running resume's mailbox is guaranteed
		// non-nil under F2 invariant (we only ever flip running=true
		// after installing activeMailbox+activeWake atomically).
		activeMailbox := state.activeMailbox
		activeWake := state.activeWake
		activeResumeID := state.activeResumeID
		state.mu.Unlock()

		queued := RouterMessage{
			From:      fromAgent,
			To:        stepID,
			Content:   prompt,
			Type:      router.MessageInfo,
			Timestamp: time.Now(),
		}
		// G2: Append can fail when the mailbox is at its bounded cap
		// (ErrMailboxFull). Capture the error: on failure, do NOT
		// emit EventResumeQueued (would mislead operators into
		// thinking the message was accepted) - emit a drop event
		// instead and return ErrMailboxFullOnResume so the Router
		// can map it to DropReasonMailboxFull.
		if activeMailbox != nil {
			if _, err := activeMailbox.Append(stepID, queued); err != nil {
				// emit BOTH a drop event (delivery-loss signal)
				// AND an EventResumeFailed (resume-lifecycle signal)
				// so observers subscribed to either channel learn
				// about the rejected enqueue. Keeps parity with the
				// resolver-error path which also emits Failed.
				queuedResumeID := resumeIDGen()
				if e.Progress != nil {
					e.Progress.OnEvent(ctx, Event{
						Type:      types.EventMessageDropped,
						Timestamp: time.Now(),
						RunID:     e.runID(),
						StepID:    stepID,
						Message:   fmt.Sprintf("[%s -> %s]: %s (resume mailbox full)", fromAgent, stepID, prompt),
						Data: map[string]any{
							"reason":         router.DropReasonMailboxFull.String(),
							"from":           fromAgent,
							"to":             stepID,
							"activeResumeID": activeResumeID,
							"queuedResumeID": queuedResumeID,
							"error":          err.Error(),
						},
					})
					e.Progress.OnEvent(ctx, Event{
						Type:      types.EventResumeFailed,
						Timestamp: time.Now(),
						RunID:     e.runID(),
						StepID:    stepID,
						Data: map[string]any{
							"resumeID":       queuedResumeID,
							"from":           fromAgent,
							"reason":         "mailbox-full-on-resume",
							"activeResumeID": activeResumeID,
							"error":          err.Error(),
							"durationMs":     int64(0),
						},
					})
				}
				return nil, ErrMailboxFullOnResume
			}
		}
		if activeWake != nil {
			select {
			case activeWake <- struct{}{}:
			default:
			}
		}
		// Synthesize a "queued" handle: DoneCh closed immediately.
		done := make(chan struct{})
		close(done)
		queuedHandle := &ResumeHandle{
			StepID:         stepID,
			ResumeID:       resumeIDGen(),
			OriginalSender: fromAgent,
			DoneCh:         done,
			Result:         "queued",
		}
		// F4: emit a dedicated EventResumeQueued so observers see the
		// accept-into-mailbox path (instead of silently returning a
		// queued handle with no trace in the event log).
		// VA-4b: include activeResumeID so operators can correlate the
		// queued event with the currently-running resume.
		if e.Progress != nil {
			e.Progress.OnEvent(ctx, Event{
				Type:      types.EventResumeQueued,
				Timestamp: time.Now(),
				RunID:     e.runID(),
				StepID:    stepID,
				Data: map[string]any{
					"resumeID":       queuedHandle.ResumeID,
					"from":           fromAgent,
					"activeResumeID": activeResumeID,
				},
			})
		}
		return queuedHandle, nil
	}
	// F2 +: install mailbox+wake + ResumeID + mark running under
	// the SAME lock acquisition so the race window closes. Any queued
	// caller observing running=true now also observes a non-empty
	// activeResumeID.
	state.running = true
	state.activeMailbox = resumeMailbox
	state.activeWake = resumeWake
	state.activeResumeID = preallocatedResumeID
	state.mu.Unlock()

	// Load transcript AFTER the CAS - any subsequent caller in this
	// window will observe running=true with a non-nil mailbox.
	transcript, err := e.transcriptStore.Load(e.runID(), stepID)
	if err != nil {
		// VA-3b: if the store sealed the slot past its cap AND the
		// caller enabled WithTruncationOnCapReached AND the store
		// implements TranscriptTruncatedLoader, fall back to
		// LoadTruncated so the step stays resumable.
		if errors.Is(err, resume.ErrTranscriptTooLarge) && e.TruncateOnCapReached {
			if loader, ok := e.transcriptStore.(resume.TranscriptTruncatedLoader); ok {
				trunc, terr := loader.LoadTruncated(e.runID(), stepID, resume.DefaultTruncatedResumeMessages)
				if terr == nil && trunc != nil {
					transcript = trunc
					err = nil
					if e.Progress != nil {
						e.Progress.OnEvent(ctx, Event{
							Type:      types.EventMessage,
							Timestamp: time.Now(),
							RunID:     e.runID(),
							StepID:    stepID,
							Message:   "resume: loaded truncated transcript after cap seal",
							Data: map[string]any{
								"reason":       "resume-truncated",
								"messageCount": len(trunc.Messages),
							},
						})
					}
				} else if terr != nil {
					// Case B: LoadTruncated itself errored. Surface
					// both errors so observers see the fallback attempt
					// AND the original seal cause. Emit a dedicated
					// event so operators can distinguish a legitimate
					// seal from a truncation-path failure.
					if e.Progress != nil {
						e.Progress.OnEvent(ctx, Event{
							Type:      types.EventMessage,
							Timestamp: time.Now(),
							RunID:     e.runID(),
							StepID:    stepID,
							Message:   "resume: truncated load failed",
							Data: map[string]any{
								"reason": "resume-truncation-failed",
								"error":  terr.Error(),
							},
						})
					}
					err = errors.Join(err, terr)
				}
			} else {
				// Case A: operator opted in to truncation but the
				// configured transcript store does not implement
				// TranscriptTruncatedLoader. Emit a warning so the
				// opt-in is not silently ignored. Original err still
				// surfaces downstream.
				if e.Progress != nil {
					e.Progress.OnEvent(ctx, Event{
						Type:      types.EventMessage,
						Timestamp: time.Now(),
						RunID:     e.runID(),
						StepID:    stepID,
						Message:   "resume: TruncateOnCapReached set but store does not implement TranscriptTruncatedLoader",
						Data: map[string]any{
							"reason":     "resume-truncation-unsupported",
							"store-type": fmt.Sprintf("%T", e.transcriptStore),
						},
					})
				}
			}
		}
		if err != nil {
			state.mu.Lock()
			state.running = false
			state.activeMailbox = nil
			state.activeWake = nil
			state.activeResumeID = ""
			state.mu.Unlock()
			return nil, err
		}
	}

	// F6 + VA-6: model-fidelity check.
	// transcript.Model carries the USER-SUPPLIED workflow model string
	// (recorded by AgentRunner as ModelID). We compare it against the
	// same user-supplied string the stepID ran with - NOT against the
	// wrapped provider.LanguageModel's ModelID (which in production
	// often differs via cross-region prefixes, deployment aliases,
	// etc.). This way the default production path never requires a
	// resolver: same workflow step → same model string → match.
	// A resolver is only needed when the transcript's Model differs
	// from the step's last-seen string AND from Runner.Model.ModelID
	// - e.g., a stale transcript from an earlier workflow revision.
	resumeModel := e.Runner.model
	if transcript.Model != "" && e.Runner != nil && e.Runner.model != nil {
		stepString := e.StepModelString(stepID)
		runnerID := e.Runner.model.ModelID()
		// Match path: transcript.Model equals EITHER the
		// workflow-step string OR the wrapped provider.ModelID.
		if transcript.Model == stepString || transcript.Model == runnerID {
			// Match - no resolver needed.
		} else if e.ModelResolver == nil {
			state.mu.Lock()
			state.running = false
			state.activeMailbox = nil
			state.activeWake = nil
			state.activeResumeID = ""
			state.mu.Unlock()
			handle := &ResumeHandle{
				StepID:         stepID,
				ResumeID:       resumeIDGen(),
				OriginalSender: fromAgent,
				DoneCh:         closedChan(),
				Err:            ErrModelResolverMissing,
			}
			e.emitResumeFailed(ctx, handle, "model-mismatch", 0)
			return nil, ErrModelResolverMissing
		} else {
			// VA-6b: distinguish "resolver returned error" from
			// "resolver returned nil" so operators can tell
			// infrastructure failure from config gap.
			resolved, rerr := e.ModelResolver(transcript.Model)
			if rerr != nil {
				state.mu.Lock()
				state.running = false
				state.activeMailbox = nil
				state.activeWake = nil
				state.activeResumeID = ""
				state.mu.Unlock()
				handle := &ResumeHandle{
					StepID:         stepID,
					ResumeID:       resumeIDGen(),
					OriginalSender: fromAgent,
					DoneCh:         closedChan(),
					Err:            ErrModelResolverError,
				}
				if e.Progress != nil {
					e.Progress.OnEvent(ctx, Event{
						Type:      types.EventResumeFailed,
						Timestamp: time.Now(),
						RunID:     e.runID(),
						StepID:    handle.StepID,
						Data: map[string]any{
							"resumeID":   handle.ResumeID,
							"from":       handle.OriginalSender,
							"reason":     "resolver-error",
							"error":      rerr.Error(),
							"durationMs": int64(0),
						},
					})
				}
				return nil, ErrModelResolverError
			}
			if resolved == nil {
				state.mu.Lock()
				state.running = false
				state.activeMailbox = nil
				state.activeWake = nil
				state.activeResumeID = ""
				state.mu.Unlock()
				handle := &ResumeHandle{
					StepID:         stepID,
					ResumeID:       resumeIDGen(),
					OriginalSender: fromAgent,
					DoneCh:         closedChan(),
					Err:            ErrModelResolverMissing,
				}
				e.emitResumeFailed(ctx, handle, "model-mismatch", 0)
				return nil, ErrModelResolverMissing
			}
			resumeModel = resolved
		}
	}

	// reuse the preallocated ResumeID so handle.ResumeID matches
	// state.activeResumeID (published under the running=true CAS above).
	// This guarantees any queued-path caller observes the correlator.
	handle := &ResumeHandle{
		StepID:         stepID,
		ResumeID:       preallocatedResumeID,
		OriginalSender: fromAgent,
		DoneCh:         make(chan struct{}),
	}

	// Emit EventResumeStarted synchronously so observers see the
	// resume kick-off even if the goroutine schedules later.
	if e.Progress != nil {
		e.Progress.OnEvent(ctx, Event{
			Type:      types.EventResumeStarted,
			Timestamp: time.Now(),
			RunID:     e.runID(),
			StepID:    stepID,
			Data: map[string]any{
				"resumeID": handle.ResumeID,
				"from":     fromAgent,
			},
		})
	}

	// increment the live-goroutine counter BEFORE spawning so Run's
	// teardown-timeout path cannot observe leaked=0 in the window between
	// the goroutine being scheduled and actually running its first line.
	// Decrement stays in runResume's defer. WaitGroup.Go below pairs Add+go
	// atomically so Run's teardown-wait cannot miss the goroutine.
	e.resumeActiveCount.Add(1)
	e.resumeWG.Go(func() {
		e.runResumeWithRecover(ctx, state, handle, transcript, prompt, resumeModel, resumeMailbox, resumeWake)
	})
	return handle, nil
}

// runResumeWithRecover wraps e.runResume in a defensive recover so a
// panic inside runResume does not take down the whole process.
// runResume's own defers (which close handle.DoneCh and release
// resumeState) run before this recover, so callers blocked on
// handle.DoneCh are unblocked naturally; we just log the panic for
// operator visibility. Extracted from the inline goroutine body in
// ResumeStep so the recover branch is unit-testable.
func (e *Executor) runResumeWithRecover(
	ctx context.Context,
	state *resumeState,
	handle *ResumeHandle,
	transcript *resume.StepTranscript,
	prompt string,
	resumeModel provider.LanguageModel,
	resumeMailbox MailboxStore,
	resumeWake chan struct{},
) {
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "runResume panic recovered",
				"panic", r,
				"step_id", handle.StepID,
				"resume_id", handle.ResumeID,
			)
		}
	}()
	e.runResume(ctx, state, handle, transcript, prompt, resumeModel, resumeMailbox, resumeWake)
}

// closedChan returns a channel whose close has already completed -
// useful for synthesizing immediately-done handles on fail-fast
// validation paths.
func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// runResume is the body of the resume goroutine. It constructs a
// fresh AgentRunner seeded with the transcript + new user turn, runs
// it to completion, and routes the final assistant response back to
// OriginalSender via a reverse RouterMessage. On any failure path it
// emits EventResumeFailed; on success EventResumeCompleted.
func (e *Executor) runResume(
	ctx context.Context,
	state *resumeState,
	handle *ResumeHandle,
	transcript *resume.StepTranscript,
	prompt string,
	resumeModel provider.LanguageModel,
	resumeMailbox MailboxStore,
	resumeWake chan struct{},
) {
	start := time.Now()
	// increment now happens in ResumeStep BEFORE the goroutine
	// spawns. Only the decrement lives here to ensure the counter
	// reflects pending-start goroutines as well as running ones.
	// (The WaitGroup itself is decremented by sync.WaitGroup.Go on
	// goroutine return, so no manual Done is needed here.)
	defer func() {
		e.resumeActiveCount.Add(-1)
		// Release the serial lock so a queued or future
		// ResumeStep call for the same stepID can run.
		state.mu.Lock()
		state.running = false
		state.activeMailbox = nil
		state.activeWake = nil
		state.activeResumeID = ""
		state.mu.Unlock()
		close(handle.DoneCh)
	}()

	if resumeModel == nil {
		handle.Err = ErrResumeNoModel
		e.emitResumeFailed(ctx, handle, "no-model", 0)
		return
	}
	if ctx.Err() != nil {
		handle.Err = ErrResumeShutdown
		e.emitResumeFailed(ctx, handle, "workflow-shutdown", time.Since(start).Milliseconds())
		return
	}

	// F1: use AgentRunner.Run with InitialMessages so the resumed turn
	// benefits from machinery: permission hooks, StateRef
	// terminal CAS, Progress events, mailbox+wake drain of queued
	// messages that arrive while the resume is in flight.
	stateRef := &goai.AgentState{}
	goaiOpts := append([]goai.Option{}, e.Runner.goAIOptions...)
	if transcript.SystemPrompt != "" {
		goaiOpts = append(goaiOpts, goai.WithSystem(transcript.SystemPrompt))
	}

	runner := &AgentRunner{
		model:           resumeModel,
		tools:           e.Runner.tools,
		permissions:     e.Runner.permissions,
		progress:        e.Runner.progress,
		goAIOptions:     goaiOpts,
		streaming:       e.Runner.streaming,
		verbose:         e.Runner.verbose,
		runID:           e.runID(),
		stepID:          handle.StepID,
		stateRef:        stateRef,
		mailbox:         resumeMailbox,
		wake:            resumeWake,
		maxWakeCycles:   e.MaxWakeCycles,
		initialMessages: transcript.Messages,
		// Test-only: forward the executor's pre-start drain gate so
		// -race -count=N tests can deterministically hold the drain
		// until all setup Appends have landed. nil in production.
		preStartDrainGate: e.resumePreStartDrainGate,
		// Do NOT re-persist into the same transcript slot: resume
		// responses are side-channel, not mutations of the saved
		// step conversation.
		transcript: nil,
	}

	cfg := AgentConfig{MaxTurns: defaultMaxTurns}
	userMsg := fmt.Sprintf("[%s]: %s", handle.OriginalSender, prompt)
	modelLabel := cmp.Or(transcript.Model, "resume")

	result, err := runner.Run(ctx, cfg, userMsg, modelLabel, runner.tools)
	if err != nil {
		if ctx.Err() != nil {
			handle.Err = ErrResumeShutdown
			e.emitResumeFailed(ctx, handle, "workflow-shutdown", time.Since(start).Milliseconds())
			return
		}
		handle.Err = err
		e.emitResumeFailed(ctx, handle, "agent-error", time.Since(start).Milliseconds())
		return
	}

	handle.Result = result.Content

	// Reverse RouterMessage to the original sender. F5:
	// tag as RouterMessageResumeReply so observers can distinguish
	// resume responses from ambient coordinator pushes. F7: set the
	// zenflow-resume-reverse metadata flag so Router.Send does NOT
	// cascade-resume if the sender's mailbox was also sealed.
	if handle.OriginalSender != "" && e.Router != nil {
		// Router.Send returns error. Reverse-reply drops are
		// already observable via the executor's installed OnDrop
		// callback (DropReasonTargetTerminal when the OriginalSender
		// finished before the resume completed; F7 metadata short-
		// circuits cascade-resume into the same drop reason). Discard
		// the per-call error so the resume goroutine's exit path is
		// unaffected - the EventMessageDropped pipeline carries the
		// signal operators consume.
		_ = e.Router.Send(handle.OriginalSender, RouterMessage{
			From:    handle.StepID,
			To:      handle.OriginalSender,
			Content: handle.Result,
			Type:    router.MessageResumeReply,
			Metadata: map[string]string{
				router.MetadataKeyResumeReverse: "1",
			},
			Timestamp: time.Now(),
		})
	}

	if e.Progress != nil {
		e.Progress.OnEvent(ctx, Event{
			Type:      types.EventResumeCompleted,
			Timestamp: time.Now(),
			RunID:     e.runID(),
			StepID:    handle.StepID,
			Duration:  time.Since(start),
			Data: map[string]any{
				"resumeID":   handle.ResumeID,
				"from":       handle.OriginalSender,
				"durationMs": time.Since(start).Milliseconds(),
			},
		})
	}
}

// emitResumeFailed publishes an EventResumeFailed with the given reason.
func (e *Executor) emitResumeFailed(ctx context.Context, h *ResumeHandle, reason string, durationMs int64) {
	if e.Progress == nil {
		return
	}
	e.Progress.OnEvent(ctx, Event{
		Type:      types.EventResumeFailed,
		Timestamp: time.Now(),
		RunID:     e.runID(),
		StepID:    h.StepID,
		Data: map[string]any{
			"resumeID":   h.ResumeID,
			"from":       h.OriginalSender,
			"reason":     reason,
			"durationMs": durationMs,
		},
	})
}

// runID returns the Executor's run identifier. Small helper so tests
// that construct an Executor directly don't need to worry about the
// RunID field being empty.
func (e *Executor) runID() string {
	if e.RunID != "" {
		return e.RunID
	}
	return "run-unknown"
}

// setResumePreStartDrainGateForTest installs a test-only gate that the
// resume goroutine's pre-start drain blocks on before consuming any
// mailbox messages. Setting to nil restores production behavior. Not
// safe for concurrent use with an active Run - intended for setup
// before the first ResumeStep call.
func (e *Executor) setResumePreStartDrainGateForTest(gate <-chan struct{}) {
	e.resumePreStartDrainGate = gate
}
