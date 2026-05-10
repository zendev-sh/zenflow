package router

import (
	"sync"
	"testing"
	"time"
)

// TestMailbox_AppendUnread verifies FIFO ordering of append→unread.
func TestMailbox_AppendUnread(t *testing.T) {
	store := NewInMemoryMailboxStore()
	stepID := "step-a"

	for i, body := range []string{"one", "two", "three"} {
		msg := Message{From: "coord", To: stepID, Content: body, Timestamp: time.Now()}
		if _, err := store.Append(stepID, msg); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	got := store.Unread(stepID)
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}
	if got[0].Content != "one" || got[1].Content != "two" || got[2].Content != "three" {
		t.Fatalf("FIFO violated: %+v", got)
	}

	// After unread+markRead, second call returns empty.
	store.MarkRead(stepID, MessageIDs(got))
	if leftover := store.Unread(stepID); len(leftover) != 0 {
		t.Fatalf("expected empty after MarkRead, got %d", len(leftover))
	}
}

// TestMailbox_MarkReadIdempotent verifies MarkRead can be called multiple times.
func TestMailbox_MarkReadIdempotent(t *testing.T) {
	store := NewInMemoryMailboxStore()
	stepID := "step-x"
	_, _ = store.Append(stepID, Message{Content: "a"})
	got := store.Unread(stepID)
	ids := MessageIDs(got)
	if alreadyRead := store.MarkRead(stepID, ids); len(alreadyRead) != 0 {
		t.Fatalf("first MarkRead alreadyRead=%v want empty", alreadyRead)
	}
	if alreadyRead := store.MarkRead(stepID, ids); len(alreadyRead) != len(ids) {
		t.Fatalf("second MarkRead alreadyRead=%v want %v (CAS dedup)", alreadyRead, ids)
	}
	if leftover := store.Unread(stepID); len(leftover) != 0 {
		t.Fatalf("idempotency broken: leftover=%d", len(leftover))
	}
}

// TestMailbox_Close drops messages for the closed step.
func TestMailbox_Close(t *testing.T) {
	store := NewInMemoryMailboxStore()
	stepID := "step-close"
	_, _ = store.Append(stepID, Message{Content: "doomed-1"})
	_, _ = store.Append(stepID, Message{Content: "doomed-2"})
	store.Close(stepID)
	if got := store.Unread(stepID); len(got) != 0 {
		t.Fatalf("Close should drop pending: got %d", len(got))
	}
	// Append after Close: behavior is "drop silently" - Append must not panic
	// and returns ("", nil).
	id, err := store.Append(stepID, Message{Content: "after"})
	if err != nil {
		t.Fatalf("Append after Close: %v", err)
	}
	if id != "" {
		t.Fatalf("Append after Close: id=%q want empty", id)
	}
}

// TestMailbox_Concurrent verifies thread-safety under heavy concurrent Append+Unread.
// Run with -race to catch data races.
func TestMailbox_Concurrent(t *testing.T) {
	store := NewInMemoryMailboxStore()
	const writers = 20
	const perWriter = 50
	stepID := "step-conc"

	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_, _ = store.Append(stepID, Message{From: "w", Content: "x"})
			}
		}(w)
	}

	// Concurrent reader draining periodically.
	done := make(chan struct{})
	totalRead := 0
	var rmu sync.Mutex
	go func() {
		for {
			select {
			case <-done:
				// final drain
				m := store.Unread(stepID)
				rmu.Lock()
				totalRead += len(m)
				rmu.Unlock()
				store.MarkRead(stepID, MessageIDs(m))
				return
			default:
				m := store.Unread(stepID)
				rmu.Lock()
				totalRead += len(m)
				rmu.Unlock()
				store.MarkRead(stepID, MessageIDs(m))
				time.Sleep(time.Microsecond * 100)
			}
		}
	}()

	wg.Wait()
	time.Sleep(10 * time.Millisecond)
	close(done)
	time.Sleep(20 * time.Millisecond)

	rmu.Lock()
	got := totalRead
	rmu.Unlock()
	if got != writers*perWriter {
		t.Fatalf("expected %d total messages read, got %d", writers*perWriter, got)
	}
}

// TestRouter_MailboxDelegation ensures Send routes through the mailbox
// after the cutover (mailbox is the only path).
func TestRouter_MailboxDelegation(t *testing.T) {
	router := NewRouter()
	store := NewInMemoryMailboxStore()
	router.SetMailbox(store)
	router.RegisterInbox("agent-1")

	stepID := "agent-1"
	if err := router.Send(stepID, Message{From: "coord", To: stepID, Content: "hello"}); err != nil {
		t.Fatalf("setup send: %v", err)
	}

	got := store.Unread(stepID)
	if len(got) != 1 || got[0].Content != "hello" {
		t.Fatalf("expected mailbox-delivered message, got %+v", got)
	}
}

// TestRouter_MailboxDisabledFallsBackToChan was deleted
// because the chan path no longer exists. Behavior previously asserted
// (chan delivery when flag off) is gone; the new contract is "mailbox
// always" verified by TestRouter_MailboxDelegation above.

// TestMailbox_AppendReturnsID verifies that Append returns a non-empty
// MessageID, that the ID is unique across appends, and that the same
// ID is observable via Unread (so callers can correlate per-message
// MarkRead calls).
func TestMailbox_AppendReturnsID(t *testing.T) {
	store := NewInMemoryMailboxStore()
	stepID := "id-test"

	id1, err := store.Append(stepID, Message{Content: "first"})
	if err != nil {
		t.Fatalf("Append 1: %v", err)
	}
	if id1 == "" {
		t.Fatal("Append 1 returned empty ID")
	}
	id2, err := store.Append(stepID, Message{Content: "second"})
	if err != nil {
		t.Fatalf("Append 2: %v", err)
	}
	if id2 == "" || id1 == id2 {
		t.Fatalf("Append 2 ID=%q, id1=%q want unique non-empty", id2, id1)
	}
	got := store.Unread(stepID)
	if len(got) != 2 {
		t.Fatalf("Unread len=%d want 2", len(got))
	}
	if got[0].MessageID != id1 || got[1].MessageID != id2 {
		t.Fatalf("Unread MessageIDs=[%q,%q] want [%q,%q]",
			got[0].MessageID, got[1].MessageID, id1, id2)
	}
}

// TestMailbox_MarkReadCAS_DetectsAlreadyRead verifies the CAS dedup
// contract: a second MarkRead with the same IDs returns those IDs in
// the alreadyRead slice (so a duplicate-drain race surfaces here
// rather than silently consuming twice).
func TestMailbox_MarkReadCAS_DetectsAlreadyRead(t *testing.T) {
	store := NewInMemoryMailboxStore()
	stepID := "cas-test"

	id1, _ := store.Append(stepID, Message{Content: "a"})
	id2, _ := store.Append(stepID, Message{Content: "b"})

	if alreadyRead := store.MarkRead(stepID, []string{id1, id2}); len(alreadyRead) != 0 {
		t.Fatalf("first MarkRead alreadyRead=%v want empty", alreadyRead)
	}
	// Second MarkRead must surface both IDs as alreadyRead.
	dup := store.MarkRead(stepID, []string{id1, id2})
	if len(dup) != 2 {
		t.Fatalf("second MarkRead alreadyRead=%v want [%s %s] (CAS dedup)", dup, id1, id2)
	}
	hits := map[string]bool{}
	for _, id := range dup {
		hits[id] = true
	}
	if !hits[id1] || !hits[id2] {
		t.Fatalf("alreadyRead=%v missing one of [%s %s]", dup, id1, id2)
	}
}
