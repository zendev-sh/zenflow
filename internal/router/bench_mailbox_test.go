package router

// bench_mailbox_test.go - BenchmarkMailbox
// Measures InMemoryMailboxStore throughput for three workload shapes:
// - serial-append: single goroutine appending N messages, then
// unread+markread.
// - parallel-append: b.RunParallel with each goroutine appending to
// its own step (no cross-goroutine sharing by design - tests lock
// contention on idSeq and mu).
// - unread-markread-refill: prefill N messages, then timed loop of
// Unread + MarkRead + re-fill so the store stays non-empty per iteration.
// Each sub-benchmark uses a fresh InMemoryMailboxStore allocated in setup
// (before ResetTimer) so no state bleeds between sub-benchmarks.
// Run:
//	go test -bench=BenchmarkMailbox -benchtime=1x -run=^$ ./zenflow/

import (
	"fmt"
	"testing"
	"time"
)

const (
	benchMailboxMsgsPerIter = 8    // messages per b.Loop iteration in serial bench
	benchMailboxStepID      = "s1" // constant step ID for serial sub-benchmarks
)

// benchMessage returns a Message suitable for mailbox benchmarks.
func benchMessage(i int) Message {
	return Message{
		From:      "coordinator",
		To:        "s1",
		Content:   fmt.Sprintf("message body %d for benchmarking", i),
		Type:      MessageInfo,
		Timestamp: time.Time{},
	}
}

// BenchmarkMailbox - three delivery-path shapes.
func BenchmarkMailbox(b *testing.B) {
	// --- serial-append: append, unread, markread in one goroutine ---
	b.Run("serial-append", func(b *testing.B) {
		store := NewInMemoryMailboxStore()
		b.ResetTimer()
		b.ReportAllocs()
		for b.Loop() {
			for i := range benchMailboxMsgsPerIter {
				_, _ = store.Append(benchMailboxStepID, benchMessage(i))
			}
			msgs := store.Unread(benchMailboxStepID)
			ids := make([]string, len(msgs))
			for i, m := range msgs {
				ids[i] = m.MessageID
			}
			_ = store.MarkRead(benchMailboxStepID, ids)
		}
	})

	// --- parallel-append: goroutines each own a distinct step ---
	// Each goroutine appends to its own step ID (stepNNN) so there is no
	// intentional contention on the per-step queue. The shared lock mu +
	// atomic idSeq are the contention points the benchmark targets.
	b.Run("parallel-append", func(b *testing.B) {
		store := NewInMemoryMailboxStore()
		b.ResetTimer()
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			// Each goroutine gets its own step ID derived from its
			// goroutine-local counter to avoid cross-goroutine queue sharing.
			stepID := fmt.Sprintf("step-%p", pb) // unique per goroutine
			for i := 0; pb.Next(); i++ {
				_, _ = store.Append(stepID, benchMessage(i))
			}
		})
	})

	// --- unread-markread-refill: drain + re-fill path, mailbox pre-filled ---
	// Setup: write benchMailboxMsgsPerIter messages before ResetTimer so
	// the benchmark starts with a non-empty store. Each iteration drains
	// (Unread + MarkRead) then re-fills (Append) - the re-fill is inside
	// the loop intentionally so the store stays non-empty across iterations.
	b.Run("unread-markread-refill", func(b *testing.B) {
		store := NewInMemoryMailboxStore()
		// Pre-fill.
		for i := range benchMailboxMsgsPerIter {
			_, _ = store.Append(benchMailboxStepID, benchMessage(i))
		}
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; b.Loop(); i++ {
			msgs := store.Unread(benchMailboxStepID)
			ids := make([]string, len(msgs))
			for j, m := range msgs {
				ids[j] = m.MessageID
			}
			_ = store.MarkRead(benchMailboxStepID, ids)
			// Re-fill for the next iteration (outside the measured path? No
			// - b.Loop measures wall-clock between Next calls). Refill is
			// intentionally inside the loop so the store stays non-empty.
			_, _ = store.Append(benchMailboxStepID, benchMessage(i))
		}
	})
}
