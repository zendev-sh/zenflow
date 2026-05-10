package router

// bench_router_test.go - BenchmarkRouterSend
// Measures Router.Send throughput under contention.
// Setup: a router backed by an InMemoryMailboxStore with N registered
// steps (all open). N goroutines each call Send in a tight loop
// (b.RunParallel). The benchmark reports the per-message cost
// including lock acquisition, mailbox append, and afterSend hook.
// Sub-benchmarks vary the step count to expose any O(N) lookup path
// that might exist inside Send (there shouldn't be - Send is O(1) on
// the happy path).
// Run:
//	go test -bench=BenchmarkRouterSend -benchtime=1x -run=^$ ./zenflow/

import (
	"fmt"
	"testing"
	"time"
)

// setupRouter creates a router with n registered + open steps backed by
// an InMemoryMailboxStore. Returns the router and a slice of step IDs.
func setupRouter(n int) (*Router, []string) {
	router := NewRouter()
	store := NewInMemoryMailboxStore()
	router.SetMailbox(store)

	stepIDs := make([]string, n)
	for i := range n {
		id := fmt.Sprintf("bench-step-%04d", i)
		stepIDs[i] = id
		router.RegisterStep(id)
		router.RegisterInbox(id)
	}
	return router, stepIDs
}

// benchMsg is a reusable Message for router benchmarks.
// Timestamp set to zero to avoid time.Now allocation inside the loop.
var benchMsg = Message{
	From:      "coordinator",
	Content:   "benchmark payload",
	Type:      MessageInfo,
	Timestamp: time.Time{},
}

// BenchmarkRouterSend - per-message Send latency under parallel contention.
func BenchmarkRouterSend(b *testing.B) {
	stepCounts := []int{1, 10, 100}

	for _, n := range stepCounts {
		b.Run(fmt.Sprintf("steps%d", n), func(b *testing.B) {
			router, stepIDs := setupRouter(n)
			b.ResetTimer()
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				// Each goroutine cycles through all step IDs so Send is
				// spread across the full step set, maximising mailbox lock
				// contention proportional to n.
				i := 0
				for pb.Next() {
					msg := benchMsg
					msg.To = stepIDs[i%n]
					// Ignore drop errors: benchmarks run without a coord
					// and the mailbox is open, so Send should always
					// succeed. A failure here indicates a test-setup bug.
					_ = router.Send(stepIDs[i%n], msg)
					i++
				}
			})
		})
	}
}
