package exec

import (
	"context"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/router"
	"github.com/zendev-sh/zenflow/internal/spec"
	"github.com/zendev-sh/zenflow/internal/types"
)

// ----: pending-senders counter on MessageRouter ----------------------

// TestRouter_PendingSenders_OpenClose verifies the basic Open/Close/Pending
// surface and that the counter is per-target (independent across step IDs).
func TestRouter_PendingSenders_OpenClose(t *testing.T) {
	r := NewMessageRouter()

	if got := r.PendingSenders("a"); got != 0 {
		t.Fatalf("initial PendingSenders(a)=%d want 0", got)
	}

	r.OpenSender("a")
	r.OpenSender("a")
	r.OpenSender("b")
	if got := r.PendingSenders("a"); got != 2 {
		t.Errorf("PendingSenders(a)=%d want 2", got)
	}
	if got := r.PendingSenders("b"); got != 1 {
		t.Errorf("PendingSenders(b)=%d want 1", got)
	}

	r.CloseSender("a")
	if got := r.PendingSenders("a"); got != 1 {
		t.Errorf("PendingSenders(a)=%d want 1 after one Close", got)
	}
	r.CloseSender("a")
	r.CloseSender("b")
	if got := r.PendingSenders("a"); got != 0 {
		t.Errorf("PendingSenders(a)=%d want 0", got)
	}
	if got := r.PendingSenders("b"); got != 0 {
		t.Errorf("PendingSenders(b)=%d want 0", got)
	}
}

// TestRouter_PendingSenders_NoNegative ensures CloseSender on a zero counter
// is a defensive no-op (never goes negative). A negative value would mask
// real sender leaks behind a "phantom positive" later.
func TestRouter_PendingSenders_NoNegative(t *testing.T) {
	r := NewMessageRouter()
	r.CloseSender("a")
	r.CloseSender("a")
	if got := r.PendingSenders("a"); got != 0 {
		t.Errorf("PendingSenders(a)=%d want 0 (clamped, never negative)", got)
	}
}

// TestRouter_PendingSenders_ConcurrentOpenClose hammers Open/Close from many
// goroutines and asserts the counter is balanced at the end. Race-detector
// must report clean.
func TestRouter_PendingSenders_ConcurrentOpenClose(t *testing.T) {
	r := NewMessageRouter()
	const writers = 32
	const opsPer = 200

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < opsPer; i++ {
				r.OpenSender("step")
				r.CloseSender("step")
			}
		}()
	}
	wg.Wait()
	if got := r.PendingSenders("step"); got != 0 {
		t.Fatalf("PendingSenders(step)=%d want 0 after balanced ops", got)
	}
}

// ----: 3-invariant termination ---------------------------------------

// TestWaitForStepTermination_AllSatisfied returns immediately when the three
// invariants (no senders + empty mailbox + stable idle) all hold.
func TestWaitForStepTermination_AllSatisfied(t *testing.T) {
	r := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	st := &goai.AgentState{}
	runToIdle(t, st)

	clock := newTickerClock()
	defer clock.stop()

	deadline := time.After(2 * time.Second)
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- waitForStepTermination(t.Context(), "s1", r, mb, st, clock.tickFn, 5*time.Millisecond)
	}()

	clock.fire()
	clock.fire() // need 2 stable ticks
	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("waitForStepTermination: %v", err)
		}
	case <-deadline:
		t.Fatalf("waitForStepTermination did not return within 2s")
	}
}

// TestWaitForStepTermination_BlocksOnPendingSender keeps the wait loop blocked
// while a sender slot is open, then releases it once the slot closes.
func TestWaitForStepTermination_BlocksOnPendingSender(t *testing.T) {
	r := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	st := &goai.AgentState{}
	runToIdle(t, st)

	r.OpenSender("s1")

	clock := newTickerClock()
	defer clock.stop()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- waitForStepTermination(t.Context(), "s1", r, mb, st, clock.tickFn, 5*time.Millisecond)
	}()

	for i := 0; i < 5; i++ {
		clock.fire()
	}
	select {
	case err := <-doneCh:
		t.Fatalf("waitForStepTermination returned early (err=%v) while sender slot held", err)
	case <-time.After(50 * time.Millisecond):
	}

	r.CloseSender("s1")
	for i := 0; i < 5; i++ {
		clock.fire()
	}
	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("waitForStepTermination: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("waitForStepTermination did not unblock after CloseSender")
	}
}

// TestWaitForStepTermination_BlocksOnUnreadMailbox keeps the wait loop blocked
// while the mailbox has unread entries.
func TestWaitForStepTermination_BlocksOnUnreadMailbox(t *testing.T) {
	r := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	st := &goai.AgentState{}
	runToIdle(t, st)

	if _, err := mb.Append("s1", RouterMessage{From: "x", Content: "y"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	clock := newTickerClock()
	defer clock.stop()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- waitForStepTermination(t.Context(), "s1", r, mb, st, clock.tickFn, 5*time.Millisecond)
	}()
	for i := 0; i < 5; i++ {
		clock.fire()
	}
	select {
	case err := <-doneCh:
		t.Fatalf("waitForStepTermination returned early (err=%v) with unread mailbox", err)
	case <-time.After(50 * time.Millisecond):
	}

	mb.MarkRead("s1", MessageIDs(mb.Unread("s1")))
	for i := 0; i < 5; i++ {
		clock.fire()
	}
	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("waitForStepTermination: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("waitForStepTermination did not unblock after MarkRead")
	}
}

// TestWaitForStepTermination_CtxCancel ensures ctx cancellation aborts the
// wait. The function returns ctx.Err so the caller can route to the
// workflow-abort flush path.
func TestWaitForStepTermination_CtxCancel(t *testing.T) {
	r := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	st := &goai.AgentState{}
	runToIdle(t, st)
	r.OpenSender("s1") // never closes

	clock := newTickerClock()
	defer clock.stop()

	ctx, cancel := context.WithCancel(t.Context())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- waitForStepTermination(ctx, "s1", r, mb, st, clock.tickFn, 5*time.Millisecond)
	}()

	cancel()
	select {
	case err := <-doneCh:
		if err == nil {
			t.Fatalf("waitForStepTermination: nil err on ctx cancel; want non-nil")
		}
	case <-time.After(time.Second):
		t.Fatalf("waitForStepTermination did not exit on ctx cancel")
	}
}

// ----: workflow-abort flush ------------------------------------------

// TestFlushMailboxOnAbort_EmitsDropEvents stages two pending mailbox entries
// for two different steps then calls flushMailboxOnAbort. The function
// must emit one EventMessageDropped per pending message with reason
// "workflow-cancelled" and close every mailbox.
func TestFlushMailboxOnAbort_EmitsDropEvents(t *testing.T) {
	mb := NewInMemoryMailboxStore()
	stepIDs := []string{"a", "b"}
	if _, err := mb.Append("a", RouterMessage{From: "coord", Content: "to-a"}); err != nil {
		t.Fatalf("Append a: %v", err)
	}
	if _, err := mb.Append("a", RouterMessage{From: "coord", Content: "to-a-2"}); err != nil {
		t.Fatalf("Append a: %v", err)
	}
	if _, err := mb.Append("b", RouterMessage{From: "coord", Content: "to-b"}); err != nil {
		t.Fatalf("Append b: %v", err)
	}

	sink := &captureSink{}
	flushMailboxOnAbort(context.Background(), "run-1", stepIDs, mb, sink, router.DropReasonWorkflowCancelled)

	got := sink.eventsByType(types.EventMessageDropped)
	if len(got) != 3 {
		t.Fatalf("EventMessageDropped count=%d want 3", len(got))
	}
	for _, ev := range got {
		if ev.RunID != "run-1" {
			t.Errorf("RunID=%q want run-1", ev.RunID)
		}
		if reason, _ := ev.Data["reason"].(string); reason != "workflow-cancelled" {
			t.Errorf("Data[reason]=%v want workflow-cancelled", ev.Data["reason"])
		}
		if ev.StepID != "a" && ev.StepID != "b" {
			t.Errorf("StepID=%q want a or b", ev.StepID)
		}
	}

	if got := mb.Unread("a"); len(got) != 0 {
		t.Errorf("Unread(a) after flush = %d, want 0 (closed)", len(got))
	}
	if got := mb.Unread("b"); len(got) != 0 {
		t.Errorf("Unread(b) after flush = %d, want 0 (closed)", len(got))
	}
}

// TestFlushMailboxOnAbort_NoSink does not panic when the progress sink is nil.
func TestFlushMailboxOnAbort_NoSink(t *testing.T) {
	mb := NewInMemoryMailboxStore()
	if _, err := mb.Append("a", RouterMessage{From: "x", Content: "y"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	flushMailboxOnAbort(context.Background(), "run", []string{"a"}, mb, nil, router.DropReasonWorkflowCancelled)
	if got := mb.Unread("a"); len(got) != 0 {
		t.Errorf("Unread(a)=%d want 0", len(got))
	}
}

// ---- end-to-end engine wire-up -------------------------------------------

// retired the legacy stubMessagingCoordinator stand-in (a
// CoordinatorAgent interface impl). Tests now construct a minimal
// *AgentRunner via newTestCoordRunner (see coord_test_helpers_test.go)
// to enable the executor's mailbox + router + delivery-engine stack
// without a real LLM coord. The runner's Mailbox is read directly when
// tests need to assert events were pushed.

// TestExecutorRun_WiresMailboxStack runs a tiny 1-step workflow with the
// stub coordinator and asserts that, during execution, the executor
// allocated a Router + Mailbox + WakeRegistry. The defer-run engine
// goroutine must exit cleanly (no leak).
func TestExecutorRun_WiresMailboxStack(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "ok", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}
	wf := newTestWorkflow(
		[]Step{{ID: "only", Instructions: "do work"}},
		nil,
	)
	exec := newTestExecutor(model, nil, wf)
	exec.Coordinator = newTestCoordRunner()

	res, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != spec.StatusCompleted {
		t.Fatalf("status=%v want StatusCompleted", res.Status)
	}
	if exec.Router == nil {
		t.Errorf("Router not allocated for non-noop coordinator")
	}
	if exec.mailbox == nil {
		t.Errorf("mailbox not wired for non-noop coordinator")
	}
	if exec.wakeRegistry == nil {
		t.Errorf("wakeRegistry not wired for non-noop coordinator")
	}
	// MailboxEnabled flag was removed (mailbox is the
	// only path). The wire-up assertion above covers what the flag
	// previously gated.
}

// ---- helpers --------------------------------------------------------------

// tickerClock is a minimal manual-fire clock used by tests. tickFn
// returns a buffered channel; tests call fire to push a tick onto it.
// The same channel is returned for every call so the wait-loop reads
// from a single shared queue.
type tickerClock struct {
	mu sync.Mutex
	ch chan time.Time
}

func newTickerClock() *tickerClock {
	return &tickerClock{ch: make(chan time.Time, 64)}
}
func (c *tickerClock) stop() {}
func (c *tickerClock) fire() {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case c.ch <- time.Now():
	default:
	}
}

func (c *tickerClock) tickFn(d time.Duration) <-chan time.Time {
	_ = d
	return c.ch
}

// silence "imported and not used" if atomic gets dropped.
var _ = atomic.Int64{}

// captureSink records events for assertions.
type captureSink struct {
	mu     sync.Mutex
	events []Event
}

func (s *captureSink) OnEvent(_ context.Context, e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}
func (s *captureSink) OnOutput(_ context.Context, _ Output) {}

func (s *captureSink) eventsByType(t EventType) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for _, e := range s.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// ---- Terminal-state gating ------------------------------------------------

// runToTerminal drives an AgentState into the requested terminal kind via
// the same set→SetTerminal handshake that AgentRunner.Run performs on exit.
func runToTerminal(t *testing.T, st *goai.AgentState, kind goai.StepKind) {
	t.Helper()
	// Match goai's tool-loop publish pattern (StepStarting → StepLLMInFlight
	// → StepIdle) before the consumer SetTerminal call so step counter is
	// preserved.
	runToIdle(t, st)
	if !st.SetTerminal(kind) {
		t.Fatalf("SetTerminal(%v) returned false; want true", kind)
	}
}

// TestWaitForStepTermination_OnDone exits when the runner publishes StepDone.
func TestWaitForStepTermination_OnDone(t *testing.T) {
	r := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	st := &goai.AgentState{}
	runToTerminal(t, st, goai.StepDone)

	clock := newTickerClock()
	defer clock.stop()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- waitForStepTermination(t.Context(), "s1", r, mb, st, clock.tickFn, 5*time.Millisecond)
	}()
	clock.fire()

	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("waitForStepTermination on StepDone: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForStepTermination did not return on StepDone within 2s")
	}
}

// TestWaitForStepTermination_OnCancelled exits on StepCancelled.
func TestWaitForStepTermination_OnCancelled(t *testing.T) {
	r := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	st := &goai.AgentState{}
	runToTerminal(t, st, goai.StepCancelled)

	clock := newTickerClock()
	defer clock.stop()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- waitForStepTermination(t.Context(), "s1", r, mb, st, clock.tickFn, 5*time.Millisecond)
	}()
	clock.fire()

	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("waitForStepTermination on StepCancelled: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForStepTermination did not return on StepCancelled within 2s")
	}
}

// TestWaitForStepTermination_OnError exits on StepError.
func TestWaitForStepTermination_OnError(t *testing.T) {
	r := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	st := &goai.AgentState{}
	runToTerminal(t, st, goai.StepError)

	clock := newTickerClock()
	defer clock.stop()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- waitForStepTermination(t.Context(), "s1", r, mb, st, clock.tickFn, 5*time.Millisecond)
	}()
	clock.fire()

	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("waitForStepTermination on StepError: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForStepTermination did not return on StepError within 2s")
	}
}

// TestStepIdleFallback_EmitsWarning verifies the H4 soft-gate
// observability hook (Verifier I1,). When a caller drives
// AgentState to StepIdle WITHOUT invoking SetTerminal (the path
// production callers MUST take via AgentRunner.Run's defer),
// waitForStepTermination still terminates (preserving compatibility
// with manual-state tests) but bumps the stepIdleFallbackHitsCount counter
// AND logs a one-shot warning the first time it fires per process.
func TestStepIdleFallback_EmitsWarning(t *testing.T) {
	resetStepIdleFallbackForTest()

	// Capture log output so we can assert the one-shot warning.
	var logBuf logCapture
	prevWriter := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(prevWriter) })

	r := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	st := &goai.AgentState{}
	// Drive to StepIdle but DELIBERATELY skip SetTerminal - this is
	// the wiring bug the soft-gate fallback is designed to detect.
	runToIdle(t, st)

	clock := newTickerClock()
	defer clock.stop()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- waitForStepTermination(t.Context(), "soft-gate-step", r, mb, st, clock.tickFn, 5*time.Millisecond)
	}()
	clock.fire()

	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("waitForStepTermination on bare StepIdle: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForStepTermination did not return on bare StepIdle within 2s")
	}

	if got := stepIdleFallbackHitsCount(); got < 1 {
		t.Errorf("stepIdleFallbackHitsCount=%d want >=1 after bare-StepIdle observation", got)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "soft-gate fallback") || !strings.Contains(logged, "soft-gate-step") {
		t.Errorf("expected one-shot soft-gate warning containing step ID; got: %q", logged)
	}

	// Second invocation must NOT re-log (sync.Once gate) but must bump
	// the counter again so operators can detect repeated misuse.
	hitsBefore := stepIdleFallbackHitsCount()
	logBuf.Reset()
	st2 := &goai.AgentState{}
	runToIdle(t, st2)
	doneCh2 := make(chan error, 1)
	go func() {
		doneCh2 <- waitForStepTermination(t.Context(), "soft-gate-step-2", r, mb, st2, clock.tickFn, 5*time.Millisecond)
	}()
	clock.fire()
	select {
	case err := <-doneCh2:
		if err != nil {
			t.Fatalf("second waitForStepTermination: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second waitForStepTermination timed out")
	}
	if got := stepIdleFallbackHitsCount(); got <= hitsBefore {
		t.Errorf("stepIdleFallbackHitsCount=%d want >%d after second bare-StepIdle observation", got, hitsBefore)
	}
	if logged := logBuf.String(); strings.Contains(logged, "soft-gate fallback") {
		t.Errorf("second invocation must not re-log (sync.Once); got: %q", logged)
	}
}

// TestStepIdleFallback_NoHitOnTerminal verifies the soft-gate
// observability hook does NOT fire when the production path is wired
// correctly (terminal CAS via SetTerminal).
func TestStepIdleFallback_NoHitOnTerminal(t *testing.T) {
	resetStepIdleFallbackForTest()

	r := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	st := &goai.AgentState{}
	runToTerminal(t, st, goai.StepDone)

	clock := newTickerClock()
	defer clock.stop()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- waitForStepTermination(t.Context(), "terminal-step", r, mb, st, clock.tickFn, 5*time.Millisecond)
	}()
	clock.fire()

	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("waitForStepTermination on StepDone: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForStepTermination did not return on StepDone within 2s")
	}

	if got := stepIdleFallbackHitsCount(); got != 0 {
		t.Errorf("stepIdleFallbackHitsCount=%d want 0 (terminal path must not trip soft-gate)", got)
	}
}

// logCapture is a thread-safe bytes buffer for capturing log output in
// tests. log.SetOutput requires an io.Writer - bytes.Buffer is not
// safe for concurrent reads/writes.
type logCapture struct {
	mu  sync.Mutex
	buf []byte
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	return len(p), nil
}

func (l *logCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.buf)
}

func (l *logCapture) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = l.buf[:0]
}
