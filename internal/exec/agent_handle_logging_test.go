package exec

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// loggingSyncWriter serialises concurrent slog writes so the test can
// inspect the buffer without tripping -race.
type loggingSyncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *loggingSyncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func (s *loggingSyncWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.String()
}

// captureSlog redirects slog.Default to a synchronised buffer for the
// lifetime of the test. The returned writer yields the captured bytes.
func captureSlog(t *testing.T, level slog.Level) *loggingSyncWriter {
	t.Helper()
	prev := slog.Default()
	w := &loggingSyncWriter{w: &bytes.Buffer{}}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return w
}

// TestAgentHandle_AgentGoroutinePanic_LogsContext verifies that a panic
// inside the agent runner is recovered by the agent goroutine and the
// recovery logs the goroutine label, handleID, sessionID, and panic
// value.
func TestAgentHandle_AgentGoroutinePanic_LogsContext(t *testing.T) {
	logs := captureSlog(t, slog.LevelDebug)

	saveRunnerHook(t, func(_ *Orchestrator, _ context.Context, _ AgentConfig) (*AgentResult, error) {
		panic("synthetic agent panic")
	})

	o := New()
	t.Cleanup(func() { _ = o.Close() })
	const sessID = "sess-panic-test"
	h, err := o.RunAgentAsync(t.Context(), AgentConfig{SessionID: sessID})
	if err != nil {
		t.Fatalf("RunAgentAsync: %v", err)
	}

	select {
	case <-h.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done() did not fire within 2s after panic")
	}

	got := logs.String()
	if got == "" {
		t.Fatal("logs are empty - panic recovery did not log anything (slog capture broken or recovery missing)")
	}
	for _, want := range []string{
		"panic in agent run goroutine",
		`goroutine=agent`,
		`handle_id=` + h.ID,
		`session_id=` + sessID,
		"synthetic agent panic",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected log to contain %q; got:\n%s", want, got)
		}
	}
}

// TestOrchestrator_Close_AwaitsGoroutineDrain_With5sCap verifies Close
// waits for in-flight handles via h.finished and logs a partial-drain
// warning when the deadline expires.
// Test strategy: register a synthetic handle directly into the
// orchestrator's registry without spawning the cleanup-watcher / TTL
// goroutines. Close's bounded-await hits the per-handle deadline
// because no one ever closes h.finished. The test helper shortens the
// deadline to 200ms so the test stays fast.
func TestOrchestrator_Close_AwaitsGoroutineDrain_With5sCap(t *testing.T) {
	logs := captureSlog(t, slog.LevelDebug)

	prev := setCloseDrainDeadlineForTest(200 * time.Millisecond)
	t.Cleanup(func() { setCloseDrainDeadlineForTest(prev) })

	o := New()
	// Inject a synthetic handle whose `finished` channel will never be
	// closed before the deadline. We DO NOT route through RunAgentAsync
	// because that spawns watcher goroutines that would leak. We DO
	// initialise `done` (size 1, buffered) so finish inside Cancel
	// does not block.
	stuck := make(chan struct{})
	h := &AgentHandle{
		ID:        "agent-test-stuck",
		done:      make(chan AgentResult, 1),
		finished:  stuck,
		sessionID: "sess-drain",
	}
	// finish will close BOTH done and stuck - to prevent that here
	// we manually pre-arm `once` so finish becomes a no-op for this
	// test handle, leaving stuck open for the deadline to expire.
	h.once.Do(func() {})

	// Register directly - bypass RunAgentAsync so no extra goroutines
	// spawn that we would have to babysit.
	o.handleMu.Lock()
	if o.handleRegistry == nil {
		o.handleRegistry = make(map[string][]*AgentHandle)
	}
	o.handleRegistry[h.sessionID] = append(o.handleRegistry[h.sessionID], h)
	o.handleMu.Unlock()

	start := time.Now()
	if err := o.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed < 150*time.Millisecond {
		t.Errorf("Close returned in %v; want >= 150ms (bounded await skipped?)", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Close returned in %v; want < 2s (deadline cap exceeded)", elapsed)
	}

	got := logs.String()
	for _, want := range []string{
		"Orchestrator.Close: partial drain",
		"notDrained=1",
		"totalHandles=1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected log to contain %q; got:\n%s", want, got)
		}
	}

	// Cleanup: close stuck so any potential listeners can exit.
	select {
	case <-stuck:
	default:
		close(stuck)
	}
}
