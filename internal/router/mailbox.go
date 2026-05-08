package router

import (
	"errors"
	"maps"
	"strconv"
	"sync"
	"sync/atomic"
)

// MailboxStore is a per-step message store with at-least-once delivery
// semantics. Messages persist until explicitly marked read or until Close
// is called for the step. Stable.
// the MailboxStore is the only delivery path on the
// Router. Wire a store via Router.SetMailbox before any
// Send.
// Implementations MUST be safe for concurrent Append + Unread + MarkRead +
// Close. The reference implementation is InMemoryMailboxStore.
type MailboxStore interface {
	// Append enqueues msg into the per-step mailbox identified by stepID.
	// The store assigns a fresh MessageID and returns it; the input msg
	// is not mutated. Append on a closed step is a no-op (drop silently)
	// and returns ("", nil) - callers (Router.Send) are strictly
	// non-blocking and never expect failure.
	// Returning the ID lets producers correlate individual messages
	// with later MarkRead CAS results.
	Append(stepID string, msg Message) (id string, err error)

	// Unread returns a snapshot of all messages currently buffered for
	// stepID, each carrying the MessageID assigned by Append. The store
	// does NOT mark them read; callers invoke MarkRead with the IDs
	// after they have been delivered to the agent. The two-step
	// (Unread → MarkRead) shape allows the poller to confirm successful
	// dispatch before clearing the buffer, preserving at-least-once
	// semantics if delivery fails mid-flight.
	Unread(stepID string) []Message

	// MarkRead removes the messages identified by ids from stepID's
	// buffer. The returned slice contains the subset of ids that had
	// already been marked read on a previous call (CAS dedup): a
	// well-behaved drainer always sees an empty slice; a duplicate-drain
	// race surfaces here instead of silently consuming twice.
	// MarkRead is idempotent: re-marking IDs that were already read
	// does not panic; they simply appear in alreadyRead.
	MarkRead(stepID string, ids []string) (alreadyRead []string)

	// Close drops all pending messages for stepID and marks the step as
	// closed. Subsequent Appends are silently dropped. Subsequent Unreads
	// return nil.
	Close(stepID string)
}

// LenAware is an optional MailboxStore extension that exposes the
// queue length. Returns (unread, total) message counts for stepID.
// unread is the
// number of messages currently in the unread queue (i.e. visible to a
// subsequent Unread call); total is unread + already-MarkRead'd count.
// Stores that do not retain MarkRead'd messages report total == unread.
type LenAware interface {
	Len(stepID string) (unread, total int)
}

// MailboxLen returns (unread, total) for the given stepID, falling back
// to len(Unread) when the underlying store does not implement
// LenAware. This is the canonical entry point used by the executor's
// invariant-check snapshot (stepLock + Len + state + pending-senders
// are all read in the same critical section).
func MailboxLen(store MailboxStore, stepID string) (unread, total int) {
	if store == nil {
		return 0, 0
	}
	if la, ok := store.(LenAware); ok {
		return la.Len(stepID)
	}
	n := len(store.Unread(stepID))
	return n, n
}

// ClosedAware is an optional MailboxStore extension implemented by
// stores that expose their per-step closed flag. The router uses it to
// surface "mailbox-closed-by-finalize" drops when a Close races a
// concurrent Send.
type ClosedAware interface {
	Closed(stepID string) bool
}

// InMemoryMailboxStore is the default MailboxStore: an in-process,
// per-stepID FIFO queue protected by a single mutex. Suitable for the
// process-local zenflow runtime. For multi-process workflows a
// persistent backend (sqlite, redis) would replace this. Stable.
// MessageIDs are monotonic counters formatted "m<N>", scoped per
// store instance (not per stepID). The format is opaque to consumers
// - only equality is required for MarkRead's CAS dedup.
type InMemoryMailboxStore struct {
	mu       sync.Mutex
	queues   map[string][]Message
	closed   map[string]bool
	readCnt  map[string]int // total messages MarkRead'd per step
	totalCnt map[string]int // total messages Append'd per step (lifetime)
	// readIDs records IDs that have already been MarkRead'd, so a
	// duplicate-drain race shows up in MarkRead's alreadyRead return
	// instead of silently consuming twice. Per-step set keyed by ID.
	readIDs map[string]map[string]struct{}
	// idSeq is the monotonic ID generator (atomic for lock-free
	// allocation; the queues map mutation still serialises on mu).
	idSeq atomic.Uint64
}

// NewInMemoryMailboxStore returns a ready-to-use in-memory mailbox. Stable.
func NewInMemoryMailboxStore() *InMemoryMailboxStore {
	return &InMemoryMailboxStore{
		queues:   make(map[string][]Message),
		closed:   make(map[string]bool),
		readCnt:  make(map[string]int),
		totalCnt: make(map[string]int),
		readIDs:  make(map[string]map[string]struct{}),
	}
}

// Len reports (unread, total) for stepID. Implements LenAware (plan
// §4.2 #1). unread = len of the in-memory queue; total = lifetime
// Appends (whether or not later MarkRead'd) for stepID.
func (s *InMemoryMailboxStore) Len(stepID string) (unread, total int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queues[stepID]), s.totalCnt[stepID]
}

// Append implements MailboxStore. Generates a fresh MessageID,
// stamps it on the stored copy of msg, and returns it. On a closed
// step Append is a no-op and returns ("", nil).
func (s *InMemoryMailboxStore) Append(stepID string, msg Message) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed[stepID] {
		return "", nil
	}
	id := "m" + strconv.FormatUint(s.idSeq.Add(1), 10)
	msg.MessageID = id
	s.queues[stepID] = append(s.queues[stepID], msg)
	s.totalCnt[stepID]++
	return id, nil
}

// Unread implements MailboxStore. Returns a copy so callers may iterate
// without holding the lock.
func (s *InMemoryMailboxStore) Unread(stepID string) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.queues[stepID]
	if len(q) == 0 {
		return nil
	}
	out := make([]Message, len(q))
	copy(out, q)
	return out
}

// MarkRead implements MailboxStore. Removes any queue entries whose
// MessageID matches one of ids. Returns the subset of ids that were
// already marked read on a prior call (CAS dedup): a normal drainer
// sees an empty slice; a duplicate-drain race surfaces here instead
// of silently consuming twice. Idempotent.
func (s *InMemoryMailboxStore) MarkRead(stepID string, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	read := s.readIDs[stepID]
	if read == nil {
		read = make(map[string]struct{})
		s.readIDs[stepID] = read
	}
	// Determine which ids are duplicates vs. fresh.
	want := make(map[string]struct{}, len(ids))
	var alreadyRead []string
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := read[id]; dup {
			alreadyRead = append(alreadyRead, id)
			continue
		}
		want[id] = struct{}{}
	}
	if len(want) == 0 {
		return alreadyRead
	}
	q := s.queues[stepID]
	if len(q) == 0 {
 // Nothing in queue - record fresh ids as read so a second
 // MarkRead still surfaces them as already-read (CAS contract).
		maps.Copy(read, want)
		return alreadyRead
	}
	// Three-index slice forces a fresh backing array on first append so
	// out never aliases the original q. No defensive copy needed on
	// assignment - out already owns its own storage.
	out := q[:0:0]
	removed := 0
	for _, m := range q {
		if _, hit := want[m.MessageID]; hit {
			read[m.MessageID] = struct{}{}
			removed++
			continue
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		delete(s.queues, stepID)
	} else {
		s.queues[stepID] = out
	}
	// Any want-id that was not present in the queue is treated as fresh
	// "marked-read in absentia" so the CAS contract still holds on a
	// later double-mark.
	maps.Copy(read, want)
	s.readCnt[stepID] += removed
	return alreadyRead
}

// Close implements MailboxStore. Hard-deletes pending messages and
// marks the step as closed (subsequent Appends are silently dropped,
// subsequent Unreads return nil). Equivalent to a hard delete when
// invoked after Seal.
// readCnt / totalCnt are also dropped because they are stats counters
// scoped to a step's lifetime; once Close marks the step's terminal
// state, no further Append/MarkRead can happen for that stepID, and
// the closed flag (the only post-Close-readable state) remains set.
// Without this cleanup the maps grew unbounded in long-running
// processes that ran many short-lived steps.
func (s *InMemoryMailboxStore) Close(stepID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.queues, stepID)
	delete(s.readIDs, stepID)
	delete(s.readCnt, stepID)
	delete(s.totalCnt, stepID)
	s.closed[stepID] = true
}

// Seal is the soft-close variant: marks the step terminal so
// subsequent Appends are silently dropped, BUT existing
// unread messages remain readable until drained by the poller. Used
// when a step reaches terminal state and the poller is still flushing.
// After Seal, Append is a no-op (matching Close), but Unread continues
// to return any messages that were enqueued before Seal. The executor
// invokes Close (hard delete) only after the post-Seal flush completes.
// Idempotent: re-Sealing an already-Sealed (or Closed) step is a no-op.
func (s *InMemoryMailboxStore) Seal(stepID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed[stepID] = true
	// Note: queues[stepID] is NOT deleted; Unread can still drain it.
}

// Closed reports whether stepID's mailbox has been closed. Implements
// the optional ClosedAware interface so the router can detect
// close-during-send races.
func (s *InMemoryMailboxStore) Closed(stepID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed[stepID]
}

// Compile-time: InMemoryMailboxStore satisfies MailboxStore + the
// ClosedAware/LenAware extensions.
var (
	_ MailboxStore = (*InMemoryMailboxStore)(nil)
	_ ClosedAware  = (*InMemoryMailboxStore)(nil)
	_ LenAware     = (*InMemoryMailboxStore)(nil)
)

// BoundedInMemoryStore wraps InMemoryMailboxStore with a hard per-step
// cap on queued unread messages. When Append would exceed the cap, the
// new message is rejected and Append returns ("", ErrMailboxFull) so
// the caller (Router.Send) can attribute the drop to "mailbox-full".
// Named field (not pointer embedding) avoids promoting the full
// InMemoryMailboxStore surface (Seal/Closed/Len) onto this type - only
// the four MailboxStore methods are delegated explicitly below.
type BoundedInMemoryStore struct {
	inner   *InMemoryMailboxStore
	maxSize int
}

// ErrMailboxFull signals the bounded store rejected an Append because
// the per-step queue is at capacity. The Router maps this to
// DropReasonMailboxFull when emitting OnDrop.
var ErrMailboxFull = errors.New("zenflow: mailbox full")

// NewBoundedInMemoryStore returns a BoundedInMemoryStore wrapping a
// fresh InMemoryMailboxStore. The maxSize parameter is the per-step
// queue cap: when an Append would exceed it, the store returns
// ErrMailboxFull and the Router maps that to DropReasonMailboxFull.
// FOOTGUN: A non-positive maxSize (<=0) disables the cap entirely,
// making this store effectively unbounded - equivalent to
// NewInMemoryMailboxStore but with a wrapper that adds no protection.
// Callers that want a true cap MUST pass a positive value. If you
// want unbounded behavior, prefer NewInMemoryMailboxStore directly so
// the intent is explicit at the call site.
// Stable.
func NewBoundedInMemoryStore(maxSize int) *BoundedInMemoryStore {
	return &BoundedInMemoryStore{
		inner:   NewInMemoryMailboxStore(),
		maxSize: maxSize,
	}
}

// MaxSize returns the configured per-step queue cap. Tests use this to
// verify constructor wiring without reaching into unexported state.
// Stable.
func (b *BoundedInMemoryStore) MaxSize() int { return b.maxSize }

// Inner returns the wrapped InMemoryMailboxStore. Tests use this to
// assert that NewBoundedInMemoryStore initialised the inner store.
// Stable.
func (b *BoundedInMemoryStore) Inner() *InMemoryMailboxStore { return b.inner }

// Append enforces the max-size cap. On overflow returns
// ("", ErrMailboxFull) - the Router observes this and emits a
// DropReasonMailboxFull drop.
func (b *BoundedInMemoryStore) Append(stepID string, msg Message) (string, error) {
	b.inner.mu.Lock()
	defer b.inner.mu.Unlock()
	if b.inner.closed[stepID] {
		return "", nil
	}
	if b.maxSize > 0 && len(b.inner.queues[stepID]) >= b.maxSize {
		return "", ErrMailboxFull
	}
	id := "m" + strconv.FormatUint(b.inner.idSeq.Add(1), 10)
	msg.MessageID = id
	b.inner.queues[stepID] = append(b.inner.queues[stepID], msg)
	b.inner.totalCnt[stepID]++
	return id, nil
}

// Unread delegates to the inner store. Satisfies MailboxStore.
func (b *BoundedInMemoryStore) Unread(stepID string) []Message {
	return b.inner.Unread(stepID)
}

// MarkRead delegates to the inner store. Satisfies MailboxStore.
func (b *BoundedInMemoryStore) MarkRead(stepID string, ids []string) []string {
	return b.inner.MarkRead(stepID, ids)
}

// Close delegates to the inner store. Satisfies MailboxStore.
func (b *BoundedInMemoryStore) Close(stepID string) {
	b.inner.Close(stepID)
}

// Compile-time: BoundedInMemoryStore satisfies MailboxStore.
var _ MailboxStore = (*BoundedInMemoryStore)(nil)

// MessageIDs returns the MessageID of every message in msgs. Used to
// translate the []Message shape returned by MailboxStore.Unread
// into the []string ids accepted by MailboxStore.MarkRead (// F4 - CAS dedup contract).
func MessageIDs(msgs []Message) []string {
	if len(msgs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.MessageID != "" {
			ids = append(ids, m.MessageID)
		}
	}
	return ids
}
