// Package resume holds the persistence contract and default
// implementation for resumable step transcripts. The zenflow root
// package re-exports these names via type aliases for backward
// compatibility.
package resume

import (
	"errors"
	"time"

	"github.com/zendev-sh/goai/provider"
)

// TranscriptStore is the persistence contract for resumable step
// transcripts. Implementations MUST be safe for concurrent Append from
// a single AgentRunner; Load/Delete are called from the executor's
// Resume path and may race with Appends on a LIVE step (see serial
// queue - resumes of a step that is still running are not permitted;
// the step must have reached terminal state before Router.Send triggers
// resume).
type TranscriptStore interface {
	// Append adds messages to stepID's transcript under runID. Returns
	// ErrTranscriptTooLarge when the transcript would exceed the
	// configured size cap. On cap-exceeded the returned
	// error wraps ErrTranscriptTooLarge and the messages ARE NOT
	// appended - the caller should surface the cap to the router so it
	// can emit DropReasonTranscriptTooLarge.
	Append(runID, stepID string, msgs []provider.Message) error

	// Load returns the full transcript for (runID, stepID). Returns
	// ErrNoTranscript when no transcript exists. Returns
	// ErrTranscriptTooLarge when a prior Append hit the cap and sealed
	// the slot - callers (Router via Executor.ResumeStep) MUST surface
	// DropReasonTranscriptTooLarge rather than resuming from a
	// truncated history. The returned *StepTranscript is a defensive
	// copy - callers may mutate the Messages slice without affecting
	// the store.
	Load(runID, stepID string) (*StepTranscript, error)

	// Delete removes a transcript. Idempotent: returns nil if no
	// transcript exists.
	Delete(runID, stepID string) error
}

// StepTranscript is the persisted conversation state for a single step.
// SystemPrompt + Model are captured at Run start so a resume can
// reconstruct the exact invocation.
type StepTranscript struct {
	StepID       string
	RunID        string
	Messages     []provider.Message
	SystemPrompt string
	Model        string // "provider:model-id" - consumer reconstructs
	SavedAt      time.Time
}

// TranscriptTruncatedLoader is an optional extension implemented by
// transcript stores that can surface a truncated view of a sealed
// transcript. When a store implements this interface AND the
// Executor's TruncateOnCapReached option is enabled, ResumeStep falls
// back to LoadTruncated after a sealed-slot Load returns
// ErrTranscriptTooLarge - preserving operability at the cost of
// potentially-incomplete history. VA-3b.
type TranscriptTruncatedLoader interface {
	// LoadTruncated returns up to maxMessages tail messages from the
	// sealed transcript. Returns ErrNoTranscript when no transcript
	// exists. Never returns ErrTranscriptTooLarge - the whole point
	// is to bypass the seal.
	LoadTruncated(runID, stepID string, maxMessages int) (*StepTranscript, error)
}

// MetadataSetter is an optional extension implemented by transcript
// stores that persist the system prompt + model identifier.
// AgentRunner calls SetMetadata on first Append when the store
// implements this interface. Implementations that don't need metadata
// persistence (e.g., transient test stubs) can omit it.
// Stable.
type MetadataSetter interface {
	SetMetadata(runID, stepID, systemPrompt, model string)
}

// DefaultTruncatedResumeMessages bounds LoadTruncated when the caller
// doesn't supply an explicit cap.
const DefaultTruncatedResumeMessages = 1000

// Sentinel errors returned by TranscriptStore implementations.
var (
	// ErrNoTranscript indicates Load found no matching transcript.
	// Routed to DropReasonNoTranscript by Router.Send when a resume
	// attempt on a sealed step has no history to restore.
	ErrNoTranscript = errors.New("zenflow: transcript not found")

	// ErrTranscriptTooLarge indicates an Append would exceed the
	// configured cap (WithMaxTranscriptMessages /
	// WithMaxTranscriptBytes). Routed to DropReasonTranscriptTooLarge.
	ErrTranscriptTooLarge = errors.New("zenflow: transcript exceeds size cap")
)
