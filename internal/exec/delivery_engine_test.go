package exec

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
)

// fakeClock is the deterministic EngineClock used by tests. Tests
// drive ticks by calling Fire; the engine receives one synthetic
// time.Time per Fire call. Stop is a no-op (the channel close is
// driven by Fire+test cleanup).
type fakeClock struct {
	ch chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{ch: make(chan time.Time, 8)} }

func (c *fakeClock) Tick(_ time.Duration) <-chan time.Time { return c.ch }
func (c *fakeClock) Stop()                                 {}
func (c *fakeClock) Fire() {
	c.ch <- time.Now()
}

// fakeSource is the deterministic EngineActiveStepsSource used by R3
// tests. It exposes a fixed list of step IDs and per-step AgentState
// handles; tests mutate the AgentState atomically (via setKind) to
// stage the engine's input for the next tick.
type fakeSource struct {
	mu     sync.Mutex
	active []string
	states map[string]*goai.AgentState
}

func newFakeSource() *fakeSource {
	return &fakeSource{states: make(map[string]*goai.AgentState)}
}

func (s *fakeSource) ActiveSteps() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.active))
	copy(out, s.active)
	return out
}

func (s *fakeSource) AgentState(stepID string) *goai.AgentState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states[stepID]
}

// AgentState staging strategy.
// goai.AgentState's setter is package-private - tests cannot mutate
// (kind, step) directly. Two test-side options exist:
// - For StepIdle: run a tiny noop goai.GenerateText with WithStateRef
// (see runToIdle). After return, Observe reports StepIdle.
// - For "busy" (anything non-Idle): use a fresh &goai.AgentState{}
// whose zero value reports StepStarting. The engine treats every
// non-Idle kind identically, so a zero-value state is a faithful
// stand-in for StepLLMInFlight / StepToolExecuting / etc.
// runToIdle dispatches a tiny goai.GenerateText call through a stub
// model that completes immediately, leaving the supplied AgentState in
// the StepIdle terminal state. This is the only legitimate way to
// stage StepIdle from outside the goai package.
func runToIdle(t *testing.T, st *goai.AgentState) {
	t.Helper()
	model := &noopModel{}
	if _, err := goai.GenerateText(t.Context(), model, goai.WithStateRef(st), goai.WithPrompt("noop")); err != nil {
		t.Fatalf("runToIdle: %v", err)
	}
	// Confirm the post-condition.
	kind, _ := st.Observe()
	if kind != goai.StepIdle {
		t.Fatalf("runToIdle: AgentState kind=%v want StepIdle", kind)
	}
}

// noopModel returns an empty FinishStop response on first call. This
// drives goai's tool loop to a single-step natural exit and leaves
// any WithStateRef target at StepIdle.
type noopModel struct{}

func (m *noopModel) ModelID() string                          { return "noop" }
func (m *noopModel) Capabilities() provider.ModelCapabilities { return provider.ModelCapabilities{} }
func (m *noopModel) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	return &provider.GenerateResult{
		Text:         "ok",
		FinishReason: provider.FinishStop,
		Usage:        provider.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}, nil
}
func (m *noopModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, fmt.Errorf("not implemented")
}

// fakeWakeRegistry implements EngineWakeRegistry for tests, exposing
// WakeCount(stepID) so tests can assert wake delivery.
type fakeWakeRegistry struct {
	mu      sync.Mutex
	targets map[string]*countingWakeTarget
}

func newFakeWakeRegistry() *fakeWakeRegistry {
	return &fakeWakeRegistry{targets: make(map[string]*countingWakeTarget)}
}

func (r *fakeWakeRegistry) Register(stepID string) *countingWakeTarget {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := &countingWakeTarget{}
	r.targets[stepID] = t
	return t
}

func (r *fakeWakeRegistry) WakeTarget(stepID string) EngineWakeTarget {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.targets[stepID]
	if !ok {
		return nil
	}
	return t
}

type countingWakeTarget struct {
	count atomic.Int64
}

func (t *countingWakeTarget) SignalWake() { t.count.Add(1) }

// TestDeliveryEngine_WakesOnIdleWithUnread verifies +: when an
// active step is in StepIdle and its mailbox has at least one unread
// message, the engine signals that step's wake target on the next tick.
func TestDeliveryEngine_WakesOnIdleWithUnread(t *testing.T) {
	source := newFakeSource()
	mailbox := NewInMemoryMailboxStore()
	registry := newFakeWakeRegistry()

	st := &goai.AgentState{}
	source.mu.Lock()
	source.active = append(source.active, "step-1")
	source.states["step-1"] = st
	source.mu.Unlock()

	// Drive the AgentState to StepIdle via a real (no-op) goai call.
	runToIdle(t, st)

	// Stage one mailbox message and register a wake target.
	if _, err := mailbox.Append("step-1", RouterMessage{From: "coord", Content: "ping"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	target := registry.Register("step-1")

	clock := newFakeClock()
	engine := NewDeliveryEngine(source, mailbox, registry, WithEngineClock(clock))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := engine.Start(ctx)

	clock.Fire()
	// Wait for the wake by polling the counter - bounded to keep the
	// test deterministic. We yield via runtime.Gosched until the
	// engine goroutine has processed the tick.
	if !waitFor(t, func() bool { return target.count.Load() == 1 }, 2*time.Second) {
		t.Fatalf("wake not signalled within 2s; count=%d", target.count.Load())
	}

	cancel()
	<-done
}

// TestDeliveryEngine_NoWakeWhenLLMInFlight verifies scheduling: the
// engine MUST NOT wake an agent that is still mid-LLM call. A wake
// would force the agent to drop its in-flight result. From the
// engine's perspective, anything other than StepIdle is "busy".
func TestDeliveryEngine_NoWakeWhenLLMInFlight(t *testing.T) {
	source := newFakeSource()
	mailbox := NewInMemoryMailboxStore()
	registry := newFakeWakeRegistry()

	// Leave AgentState at its zero value: (StepStarting, 0). The
	// engine treats every non-StepIdle kind identically - including
	// StepLLMInFlight, StepToolExecuting, StepStepFinished, and
	// StepStarting - so this exercises the same code path as a real
	// in-flight call.
	st := &goai.AgentState{}
	source.mu.Lock()
	source.active = append(source.active, "step-busy")
	source.states["step-busy"] = st
	source.mu.Unlock()
	if kind, _ := st.Observe(); kind == goai.StepIdle {
		t.Fatalf("precondition: zero-value AgentState reports %v, want non-Idle", kind)
	}

	if _, err := mailbox.Append("step-busy", RouterMessage{From: "coord", Content: "ping"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	target := registry.Register("step-busy")

	clock := newFakeClock()
	engine := NewDeliveryEngine(source, mailbox, registry, WithEngineClock(clock))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := engine.Start(ctx)

	clock.Fire()
	clock.Fire() // two ticks - confirm no wake even with repeated polls.

	// Give the engine a chance to (incorrectly) signal.
	time.Sleep(20 * time.Millisecond)
	if got := target.count.Load(); got != 0 {
		t.Fatalf("wake signalled while busy; count=%d want 0", got)
	}

	cancel()
	<-done
}

// TestDeliveryEngine_NoWakeWhenEmpty verifies: an idle agent with
// an empty mailbox must NOT receive a wake. Spurious wakes would
// trigger empty drain iterations and waste an LLM call.
func TestDeliveryEngine_NoWakeWhenEmpty(t *testing.T) {
	source := newFakeSource()
	mailbox := NewInMemoryMailboxStore()
	registry := newFakeWakeRegistry()

	st := &goai.AgentState{}
	source.mu.Lock()
	source.active = append(source.active, "step-empty")
	source.states["step-empty"] = st
	source.mu.Unlock()
	runToIdle(t, st)

	target := registry.Register("step-empty")

	clock := newFakeClock()
	engine := NewDeliveryEngine(source, mailbox, registry, WithEngineClock(clock))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := engine.Start(ctx)

	clock.Fire()
	clock.Fire()
	time.Sleep(20 * time.Millisecond)

	if got := target.count.Load(); got != 0 {
		t.Fatalf("wake signalled with empty mailbox; count=%d want 0", got)
	}

	cancel()
	<-done
}

// TestDeliveryEngine_PollHonorsStepLock_NoSpuriousWakeAfterTerminal
// verifies that when the engine is configured with a
// stepLocker, the poll's Observe + SignalWake sequence runs under
// stepLock.RLock so a concurrent terminal-state transition (which
// takes stepLock.Lock) cannot race the wake. This test simulates the
// race by holding the step's write-lock during a tick and asserting
// the poll BLOCKS until the lock is released, then signals exactly
// once. Without the C5a fix the wake fires immediately regardless of
// the lock - exposing the TOCTOU.
func TestDeliveryEngine_PollHonorsStepLock_NoSpuriousWakeAfterTerminal(t *testing.T) {
	source := newFakeSource()
	mailbox := NewInMemoryMailboxStore()
	registry := newFakeWakeRegistry()
	router := NewMessageRouter()

	st := &goai.AgentState{}
	source.mu.Lock()
	source.active = append(source.active, "step-lock")
	source.states["step-lock"] = st
	source.mu.Unlock()
	runToIdle(t, st)

	if _, err := mailbox.Append("step-lock", RouterMessage{From: "coord", Content: "ping"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	target := registry.Register("step-lock")

	clock := newFakeClock()
	engine := NewDeliveryEngine(source, mailbox, registry,
		WithEngineClock(clock),
		WithStepLocker(router),
	)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := engine.Start(ctx)

	// Acquire the write-lock - simulates the runner's terminal-state
	// defer holding stepLock.Lock. The engine's poll MUST block on
	// AcquireStepLock(...).RLock and not signal the wake.
	lock := router.AcquireStepLock("step-lock")
	lock.Lock()
	clock.Fire()
	// Give the engine a chance to (incorrectly) signal.
	time.Sleep(50 * time.Millisecond)
	if got := target.count.Load(); got != 0 {
		lock.Unlock()
		t.Fatalf("wake fired while write-lock held - C5a TOCTOU regressed; count=%d want 0", got)
	}
	// Release the write-lock - now the poll's RLock can proceed and
	// the wake should signal exactly once.
	lock.Unlock()
	if !waitFor(t, func() bool { return target.count.Load() == 1 }, 2*time.Second) {
		t.Fatalf("wake not signalled after lock release; count=%d", target.count.Load())
	}

	cancel()
	<-done
}

// TestDeliveryEngine_StopsOnCtxDone verifies lifecycle: the
// engine's goroutine exits cleanly when the parent context is
// cancelled. We assert via the done channel + a goroutine-count delta
// to catch leaks (a stuck engine would keep its goroutine alive past
// ctx cancel).
func TestDeliveryEngine_StopsOnCtxDone(t *testing.T) {
	source := newFakeSource()
	mailbox := NewInMemoryMailboxStore()
	registry := newFakeWakeRegistry()

	clock := newFakeClock()
	engine := NewDeliveryEngine(source, mailbox, registry, WithEngineClock(clock))

	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(t.Context())
	done := engine.Start(ctx)
	// Immediately cancel; the engine's select{} should fall through
	// the ctx.Done case on its next iteration.
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("engine did not exit within 1s after ctx cancel")
	}

	// Allow the runtime to reclaim the exited goroutine.
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+1 { // +1 for testing harness slack
		t.Fatalf("goroutine leak: before=%d after=%d", before, after)
	}
}

// TestAgentRunner_WakeDrainsAndResumesLoop verifies: when the
// goai loop exits via the wake-stop predicate (StopCausePredicate),
// AgentRunner drains the mailbox into the message thread and re-enters
// goai.GenerateText to consume the injected context. This is the
// integration test that proves the wake → drain → resume contract.
func TestAgentRunner_WakeDrainsAndResumesLoop(t *testing.T) {
	mailbox := NewInMemoryMailboxStore()
	wake := make(chan struct{}, 1)
	stepID := "step-int"

	model := &recordingModel{
		responses: []*provider.GenerateResult{
			textResult("primary text", 5, 5),
			textResult("post-drain text", 5, 5),
		},
		// afterCall(0) simulates a coordinator delivering a message
		// AFTER the agent's primary turn completes - the post-call
		// hasPending check should observe it and trigger a
		// continuation call.
		afterCall: func(idx int) {
			if idx == 0 {
				_, _ = mailbox.Append(stepID, RouterMessage{From: "coord", Content: "injected ctx"})
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		},
	}

	runner := &AgentRunner{
		model:   model,
		mailbox: mailbox,
		wake:    wake,
		stepID:  stepID,
	}

	result, err := runner.Run(t.Context(), AgentConfig{MaxTurns: 5}, "do work", "m", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Content != "post-drain text" {
		t.Errorf("Content=%q want 'post-drain text' (resumed call)", result.Content)
	}
	if model.callCount() < 2 {
		t.Errorf("LLM call count = %d, want >=2 (primary + post-drain)", model.callCount())
	}
	// Confirm the injected message reached the second LLM call's
	// message slice.
	msgs := model.messagesFor(1)
	found := false
	for _, m := range msgs {
		for _, p := range m.Content {
			if p.Type == provider.PartText && strings.Contains(p.Text, "injected ctx") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("injected ctx not present in second-call messages; msgs=%+v", msgs)
	}
	// Mailbox MarkRead should have cleared the queue.
	if got := len(mailbox.Unread(stepID)); got != 0 {
		t.Errorf("mailbox not drained; unread=%d", got)
	}
}

// recordingModel records each call's messages so tests can assert on
// the post-drain message thread. It also drives the wake-stop path:
// the FIRST DoGenerate sleeps just long enough that goai records a
// completed step before the WithStopWhen predicate is evaluated, so
// the predicate firing on the predrained Wake exits the loop with
// StopCausePredicate.
type recordingModel struct {
	mu        sync.Mutex
	responses []*provider.GenerateResult
	calls     []provider.GenerateParams
	afterCall func(idx int)
}

func (m *recordingModel) ModelID() string { return "recording" }
func (m *recordingModel) Capabilities() provider.ModelCapabilities {
	return provider.ModelCapabilities{}
}
func (m *recordingModel) DoGenerate(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := len(m.calls)
	m.calls = append(m.calls, params)
	var resp *provider.GenerateResult
	if idx >= len(m.responses) {
		resp = textResult("fallback", 1, 1)
	} else {
		resp = m.responses[idx]
	}
	if m.afterCall != nil {
		m.afterCall(idx)
	}
	return resp, nil
}
func (m *recordingModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *recordingModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}
func (m *recordingModel) messagesFor(callIdx int) []provider.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if callIdx < 0 || callIdx >= len(m.calls) {
		return nil
	}
	out := make([]provider.Message, len(m.calls[callIdx].Messages))
	copy(out, m.calls[callIdx].Messages)
	return out
}

// waitFor polls cond every 1ms until it returns true or the timeout
// elapses. Returns the final value of cond. Bounded so a buggy engine
// cannot hang the test indefinitely.
func waitFor(t *testing.T, cond func() bool, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
	return cond()
}

// TestExecutor_ActiveSteps_AddRemove verifies the active-step
// accessor: registerAgentState adds the step, unregisterAgentState
// removes it, and ActiveSteps returns the live set.
func TestExecutor_ActiveSteps_AddRemove(t *testing.T) {
	e := &Executor{}
	if got := e.ActiveSteps(); len(got) != 0 {
		t.Fatalf("initial ActiveSteps=%v want empty", got)
	}
	e.registerAgentState("a", &goai.AgentState{})
	e.registerAgentState("b", &goai.AgentState{})
	got := e.ActiveSteps()
	if len(got) != 2 {
		t.Fatalf("after register, ActiveSteps=%v want 2 entries", got)
	}
	e.unregisterAgentState("a")
	got = e.ActiveSteps()
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("after unregister(a), ActiveSteps=%v want [b]", got)
	}
	// The agentState handle for an unregistered step is intentionally
	// retained so late observers don't crash.
	if e.AgentState("a") == nil {
		t.Errorf("AgentState(a) returned nil; expected retained handle")
	}
	e.unregisterAgentState("b")
	if got := e.ActiveSteps(); len(got) != 0 {
		t.Fatalf("after unregister(b), ActiveSteps=%v want empty", got)
	}
}
