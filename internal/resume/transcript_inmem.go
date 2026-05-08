package resume

import (
	"sync"
	"time"

	"github.com/zendev-sh/goai/provider"
)

// DefaultMaxTranscriptMessages is the default per-step message-count cap
// applied to InMemoryTranscriptStore when the caller does not override
// it via WithTranscriptCaps or Orchestrator.WithMaxTranscriptMessages.
// Exported so external consumers and the WithMaxTranscriptMessages
// docstring stay in sync if the value changes.
// Stable.
const DefaultMaxTranscriptMessages = 10000

// DefaultMaxTranscriptBytes is the default per-step byte cap applied
// to InMemoryTranscriptStore when the caller does not override it via
// WithTranscriptCaps or Orchestrator.WithMaxTranscriptBytes. 50 MiB.
// Exported so external consumers and the WithMaxTranscriptBytes
// docstring stay in sync if the value changes.
// Stable.
const DefaultMaxTranscriptBytes = 50 * 1024 * 1024 // 50 MiB

// InMemoryTranscriptStore is the Day-1 default TranscriptStore. It
// provides intra-run resume only - transcripts live in memory and
// disappear when the Executor.Run exits. Cross-run / cross-process
// resume requires a persistent backend (file or SQLite) wired via
// WithTranscriptStore.
// Thread-safety: a single sync.Mutex guards the map and size counters.
// Append/Load/Delete all take the full lock; this is adequate for the
// intra-run workload where contention is bounded by the number of
// concurrent steps.
type InMemoryTranscriptStore struct {
	mu sync.Mutex
	// keyed by runID -> stepID -> transcript
	data map[string]map[string]*StepTranscript

	maxMessages int
	maxBytes    int64

	// bytes tracks cumulative transcript bytes for each (runID,stepID)
	// so cap checks stay O(1). The counter is a sum of per-message
	// estimates (see estimateMessageBytes).
	bytes map[string]map[string]int64

	// sealed[runID][stepID] is set to true after an Append hit the
	// configured cap (message-count OR byte-total). Once sealed the
	// transcript rejects further Appends with ErrTranscriptTooLarge AND
	// Load surfaces the same sentinel so Executor.ResumeStep can route
	// DropReasonTranscriptTooLarge through Router.Send instead of
	// silently resuming from a truncated transcript.
	sealed map[string]map[string]bool
}

// InMemoryTranscriptStoreOption is a functional option for
// NewInMemoryTranscriptStoreWithOptions.
// Stable.
type InMemoryTranscriptStoreOption func(*InMemoryTranscriptStore)

// WithTranscriptCaps sets the per-store message-count and byte caps.
// Zero or negative values are ignored (defaults apply). Use as an
// argument to NewInMemoryTranscriptStoreWithOptions.
// Stable.
func WithTranscriptCaps(maxMessages int, maxBytes int64) InMemoryTranscriptStoreOption {
	return func(s *InMemoryTranscriptStore) {
		if maxMessages > 0 {
			s.maxMessages = maxMessages
		}
		if maxBytes > 0 {
			s.maxBytes = maxBytes
		}
	}
}

// newInMemoryTranscriptStore is the shared internal default-builder.
func newInMemoryTranscriptStore() *InMemoryTranscriptStore {
	return &InMemoryTranscriptStore{
		data:        make(map[string]map[string]*StepTranscript),
		bytes:       make(map[string]map[string]int64),
		sealed:      make(map[string]map[string]bool),
		maxMessages: DefaultMaxTranscriptMessages,
		maxBytes:    DefaultMaxTranscriptBytes,
	}
}

// NewInMemoryTranscriptStoreWithOptions constructs a store with the
// default caps and then applies each option in order. Use
// WithTranscriptCaps to override caps.
// Stable.
func NewInMemoryTranscriptStoreWithOptions(opts ...InMemoryTranscriptStoreOption) *InMemoryTranscriptStore {
	s := newInMemoryTranscriptStore()
	for _, o := range opts {
		o(s)
	}
	return s
}

// NewInMemoryTranscriptStore constructs an empty store using the
// default caps. Use WithMaxTranscriptMessages / WithMaxTranscriptBytes
// on the Orchestrator to override.
func NewInMemoryTranscriptStore() *InMemoryTranscriptStore {
	return newInMemoryTranscriptStore()
}

// NewInMemoryTranscriptStoreWithCaps constructs a store with explicit
// caps. Zero or negative values fall back to the defaults. Primarily
// used internally by the Orchestrator factory path; external callers
// typically use NewInMemoryTranscriptStoreWithOptions + WithTranscriptCaps.
func NewInMemoryTranscriptStoreWithCaps(maxMessages int, maxBytes int64) *InMemoryTranscriptStore {
	return NewInMemoryTranscriptStoreWithOptions(WithTranscriptCaps(maxMessages, maxBytes))
}

// Append implements TranscriptStore. Cap enforcement is atomic: the
// new byte/message totals are computed up front, and if they would
// exceed the cap the append is rejected with ErrTranscriptTooLarge -
// no partial mutation.
func (s *InMemoryTranscriptStore) Append(runID, stepID string, msgs []provider.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	runMap, ok := s.data[runID]
	if !ok {
		runMap = make(map[string]*StepTranscript)
		s.data[runID] = runMap
	}
	t, ok := runMap[stepID]
	if !ok {
		t = &StepTranscript{
			StepID: stepID,
			RunID:  runID,
		}
		runMap[stepID] = t
	}

	runBytes, ok := s.bytes[runID]
	if !ok {
		runBytes = make(map[string]int64)
		s.bytes[runID] = runBytes
	}

	// Compute projected size.
	projectedMessages := len(t.Messages) + len(msgs)
	var addBytes int64
	for _, m := range msgs {
		addBytes += estimateMessageBytes(m)
	}
	projectedBytes := runBytes[stepID] + addBytes

	if projectedMessages > s.maxMessages || projectedBytes > s.maxBytes {
 // F3: seal this (runID,stepID) slot so subsequent Load calls
 // also surface ErrTranscriptTooLarge - matching the contract
 // documented in the TranscriptStore interface.
		sealRun, ok := s.sealed[runID]
		if !ok {
			sealRun = make(map[string]bool)
			s.sealed[runID] = sealRun
		}
		sealRun[stepID] = true
		return ErrTranscriptTooLarge
	}

	t.Messages = append(t.Messages, msgs...)
	t.SavedAt = time.Now()
	runBytes[stepID] = projectedBytes
	return nil
}

// Load implements TranscriptStore. The returned transcript is a
// defensive copy (the Messages slice is duplicated) so the caller can
// mutate it freely.
func (s *InMemoryTranscriptStore) Load(runID, stepID string) (*StepTranscript, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// F3: sealed slots surface ErrTranscriptTooLarge on Load so the
	// router emits DropReasonTranscriptTooLarge rather than silently
	// resuming from a truncated transcript.
	if sealRun, ok := s.sealed[runID]; ok && sealRun[stepID] {
		return nil, ErrTranscriptTooLarge
	}

	runMap, ok := s.data[runID]
	if !ok {
		return nil, ErrNoTranscript
	}
	t, ok := runMap[stepID]
	if !ok {
		return nil, ErrNoTranscript
	}

	// Defensive copy.
	msgs := make([]provider.Message, len(t.Messages))
	copy(msgs, t.Messages)
	return &StepTranscript{
		StepID:       t.StepID,
		RunID:        t.RunID,
		Messages:     msgs,
		SystemPrompt: t.SystemPrompt,
		Model:        t.Model,
		SavedAt:      t.SavedAt,
	}, nil
}

// LoadTruncated implements TranscriptTruncatedLoader. Returns up to
// maxMessages tail messages regardless of whether the slot was sealed.
// Used by ResumeStep when WithTruncationOnCapReached is enabled so a
// sealed transcript can still be resumed from a truncated prefix.
// VA-3b.
func (s *InMemoryTranscriptStore) LoadTruncated(runID, stepID string, maxMessages int) (*StepTranscript, error) {
	if maxMessages <= 0 {
		maxMessages = DefaultTruncatedResumeMessages
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	runMap, ok := s.data[runID]
	if !ok {
		return nil, ErrNoTranscript
	}
	t, ok := runMap[stepID]
	if !ok {
		return nil, ErrNoTranscript
	}

	start := 0
	if len(t.Messages) > maxMessages {
		start = len(t.Messages) - maxMessages
	}
	tail := t.Messages[start:]
	msgs := make([]provider.Message, len(tail))
	copy(msgs, tail)
	return &StepTranscript{
		StepID:       t.StepID,
		RunID:        t.RunID,
		Messages:     msgs,
		SystemPrompt: t.SystemPrompt,
		Model:        t.Model,
		SavedAt:      t.SavedAt,
	}, nil
}

// Delete implements TranscriptStore. Idempotent.
func (s *InMemoryTranscriptStore) Delete(runID, stepID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if runMap, ok := s.data[runID]; ok {
		delete(runMap, stepID)
		if len(runMap) == 0 {
			delete(s.data, runID)
		}
	}
	if runBytes, ok := s.bytes[runID]; ok {
		delete(runBytes, stepID)
		if len(runBytes) == 0 {
			delete(s.bytes, runID)
		}
	}
	if sealRun, ok := s.sealed[runID]; ok {
		delete(sealRun, stepID)
		if len(sealRun) == 0 {
			delete(s.sealed, runID)
		}
	}
	return nil
}

// SetMetadata records the system prompt + model identifier for a
// transcript. Called by AgentRunner on the first Append so a later
// Resume can reconstruct the invocation. Idempotent: the values are
// overwritten each call (the last Run wins), which matches the
// expectation that a single AgentRunner instance serves a single step
// per Run.
func (s *InMemoryTranscriptStore) SetMetadata(runID, stepID, systemPrompt, model string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	runMap, ok := s.data[runID]
	if !ok {
		runMap = make(map[string]*StepTranscript)
		s.data[runID] = runMap
	}
	t, ok := runMap[stepID]
	if !ok {
		t = &StepTranscript{
			StepID: stepID,
			RunID:  runID,
		}
		runMap[stepID] = t
	}
	t.SystemPrompt = systemPrompt
	t.Model = model
}

// estimateMessageBytes returns a byte-size estimate for a provider.Message
// used by the cap check. Rough heuristic: sum of every text part's
// byte length. Non-text parts (images, tool calls, tool results) add a
// flat 256 B placeholder - exact serialization would require marshalling
// which is too expensive for the hot path.
func estimateMessageBytes(m provider.Message) int64 {
	var n int64
	for _, p := range m.Content {
		switch p.Type {
		case provider.PartText:
			n += int64(len(p.Text))
		case provider.PartReasoning:
			n += int64(len(p.Text))
		default:
			n += 256
		}
	}
	// Add a small overhead for role + envelope.
	n += 32
	return n
}

// Compile-time contract checks.
var (
	_ TranscriptStore           = (*InMemoryTranscriptStore)(nil)
	_ MetadataSetter            = (*InMemoryTranscriptStore)(nil)
	_ TranscriptTruncatedLoader = (*InMemoryTranscriptStore)(nil)
)
