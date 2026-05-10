package exec

// Tests for the consumer SDK surface added to zenflow for
// embedded-consumer integration.
// These tests were written RED before the implementation landed, and
// drive the AgentConfig fields (Name, CallTools, ProgressSink,
// SubagentToolSet, SessionID), the RunAgent(ctx, cfg) signature, and
// Orchestrator.ListAgentHandles.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
)

// NOTE: TestAgentConfig_CarriesToolsAndProgressSink was removed. The
// previous test was a pure struct-echo (set fields, read them back)
// which would pass for any plain Go struct and proves nothing about
// behavior. The real wiring - that AgentConfig fields reach the
// AgentRunner - is covered by TestRunAgent_UsesAgentConfigModelAndTools
// (CallTools/Model) and TestRunAgentAsync_HonorsAllAgentConfigFields
// (every field). See audit, finding #3.

// TestRunAgent_UsesAgentConfigModelAndTools asserts that the new
// RunAgent(ctx, cfg) signature honors cfg.Model and cfg.CallTools
// per-call, overriding the Orchestrator-level defaults supplied via
// WithModel / WithTools / WithDefaultModel.
func TestRunAgent_UsesAgentConfigModelAndTools(t *testing.T) {
	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "ok", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}

	// Orchestrator-level default tool is "orch_tool".
	o := New(
		WithModel(llm),
		WithTools(goai.Tool{Name: "orch_tool"}),
		WithDefaultModel("orch-model"),
	)

	// Per-call: override with "call_tool" and model "call-model".
	cfg := AgentConfig{
		Prompt:    "hello",
		Model:     "call-model",
		CallTools: []goai.Tool{{Name: "call_tool"}},
	}

	result, err := o.RunAgent(t.Context(), cfg)
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if result.Content != "ok" {
		t.Errorf("content = %q", result.Content)
	}

	calls := llm.getCalls()
	if len(calls) == 0 {
		t.Fatal("DoGenerate never called")
	}
	names := toolNames(calls[0].Tools)
	if !containsName(names, "call_tool") {
		t.Errorf("expected call_tool in request, got %v", names)
	}
	if containsName(names, "orch_tool") {
		t.Errorf("orch_tool leaked into request even though cfg.CallTools was set: %v", names)
	}
}

// TestRunAgentAsync_HonorsAllAgentConfigFields asserts that
// RunAgentAsync forwards every configurable AgentConfig field into
// the underlying runner invocation. Without this, per-call Model /
// CallTools / MaxTurns / ProgressSink are silently ignored.
func TestRunAgentAsync_HonorsAllAgentConfigFields(t *testing.T) {
	var (
		seen     AgentConfig
		seenOnce sync.Once
	)
	savedRunner := runAgentAsyncRunner
	t.Cleanup(func() { runAgentAsyncRunner = savedRunner })
	runAgentAsyncRunner = func(o *Orchestrator, ctx context.Context, cfg AgentConfig) (*AgentResult, error) {
		seenOnce.Do(func() { seen = cfg })
		return &AgentResult{Content: "stub", Status: AgentStatusCompleted}, nil
	}

	sink := &mockProgressSink{}
	tools := []goai.Tool{{Name: "restricted"}}
	cfg := AgentConfig{
		Name:            "helper",
		Prompt:          "do X",
		Model:           "gpt-5",
		MaxTurns:        9,
		CallTools:       tools,
		ProgressSink:    sink,
		SubagentToolSet: "readonly",
		SessionID:       "sess-hello",
	}

	o := New(WithModel(&mockModel{}))
	h, err := o.RunAgentAsync(t.Context(), cfg)
	if err != nil {
		t.Fatalf("RunAgentAsync: %v", err)
	}
	select {
	case <-h.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("handle never completed")
	}

	if seen.Name != cfg.Name {
		t.Errorf("Name = %q, want %q", seen.Name, cfg.Name)
	}
	if seen.Prompt != cfg.Prompt {
		t.Errorf("Prompt = %q, want %q", seen.Prompt, cfg.Prompt)
	}
	if seen.Model != cfg.Model {
		t.Errorf("Model = %q, want %q", seen.Model, cfg.Model)
	}
	if seen.MaxTurns != cfg.MaxTurns {
		t.Errorf("MaxTurns = %d, want %d", seen.MaxTurns, cfg.MaxTurns)
	}
	if len(seen.CallTools) != 1 || seen.CallTools[0].Name != "restricted" {
		t.Errorf("CallTools = %#v", seen.CallTools)
	}
	if seen.ProgressSink != sink {
		t.Errorf("ProgressSink not forwarded")
	}
	if seen.SubagentToolSet != cfg.SubagentToolSet {
		t.Errorf("SubagentToolSet = %q", seen.SubagentToolSet)
	}
	if seen.SessionID != cfg.SessionID {
		t.Errorf("SessionID = %q", seen.SessionID)
	}
}

// TestRunAgentAsync_ForwardsProgressSinkEvents asserts that when
// cfg.ProgressSink is set, the AgentRunner spawned by RunAgent sees
// that sink and EMITS events to it (positive assertion), while the
// Orchestrator-level sink stays untouched (negative assertion).
// Tightened from: the previous version only checked that
// orchSink was not used - a vacuous green when the runner emitted
// nothing at all.
// AgentRunner installs WithOnRequest/WithOnResponse hooks on the
// goai stack (agent_runner.go ~line 493) which fire EventAgentTurn
// once per LLM call. So a single-step mockModel response produces at
// least two events (request phase + response phase) on the per-call
// sink.
func TestRunAgentAsync_ForwardsProgressSinkEvents(t *testing.T) {
	var (
		mu              sync.Mutex
		perCallEvents   int
		orchEvents      int
		gotTypesPerCall []EventType
	)
	perCall := &mockProgressSink{
		onEvent: func(_ context.Context, e Event) {
			mu.Lock()
			perCallEvents++
			gotTypesPerCall = append(gotTypesPerCall, e.Type)
			mu.Unlock()
		},
	}

	llm := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "hello", Usage: provider.Usage{InputTokens: 2, OutputTokens: 3}},
		},
	}

	// Orchestrator-level progress is a DIFFERENT sink so a fallback
	// (regression) is observable: any event delivered here proves the
	// per-call override was lost.
	orchSink := &mockProgressSink{
		onEvent: func(_ context.Context, _ Event) {
			mu.Lock()
			orchEvents++
			mu.Unlock()
		},
	}

	o := New(
		WithModel(llm),
		WithProgress(orchSink),
		WithDefaultModel("m"),
	)

	cfg := AgentConfig{
		Prompt:       "hi",
		ProgressSink: perCall,
	}
	res, err := o.RunAgent(t.Context(), cfg)
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if res.Content != "hello" {
		t.Errorf("content = %q", res.Content)
	}

	mu.Lock()
	defer mu.Unlock()

	// POSITIVE: per-call sink saw real runner events. AgentRunner's
	// WithOnRequest + WithOnResponse hooks fire on each LLM call, so a
	// successful single-step run delivers >=1 event.
	if perCallEvents == 0 {
		t.Errorf("per-call sink received 0 events - runner did not honor cfg.ProgressSink (got types: %v)", gotTypesPerCall)
	}

	// NEGATIVE: orchestrator sink was NOT used as a fallback.
	if orchEvents != 0 {
		t.Errorf("orchestrator-level sink received %d events; want 0 - cfg.ProgressSink override leaked", orchEvents)
	}
}

// TestOrchestrator_ListAgentHandles_ReturnsActiveHandles asserts that
// ListAgentHandles returns every active handle registered under the
// given sessionID. The TUI pill query migrates from
// `HasBackgroundTasks` to this method.
func TestOrchestrator_ListAgentHandles_ReturnsActiveHandles(t *testing.T) {
	// Block the stub runner so handles stay active through the
	// assertion. Release via the unblock channel after the check.
	unblock := make(chan struct{})
	savedRunner := runAgentAsyncRunner
	t.Cleanup(func() { runAgentAsyncRunner = savedRunner })
	runAgentAsyncRunner = func(o *Orchestrator, ctx context.Context, cfg AgentConfig) (*AgentResult, error) {
		select {
		case <-unblock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &AgentResult{Content: "done"}, nil
	}

	o := New(WithModel(&mockModel{}))
	ctx := t.Context()

	h1, err := o.RunAgentAsync(ctx, AgentConfig{Prompt: "p1", SessionID: "s-A"})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := o.RunAgentAsync(ctx, AgentConfig{Prompt: "p2", SessionID: "s-A"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := o.RunAgentAsync(ctx, AgentConfig{Prompt: "p3", SessionID: "s-B"})
	if err != nil {
		t.Fatal(err)
	}

	handlesA := o.ListAgentHandles("s-A")
	if len(handlesA) != 2 {
		t.Fatalf("ListAgentHandles(s-A) = %d, want 2", len(handlesA))
	}
	gotIDs := map[string]bool{}
	for _, h := range handlesA {
		gotIDs[h.ID] = true
	}
	if !gotIDs[h1.ID] || !gotIDs[h2.ID] {
		t.Errorf("missing handle IDs: got %v, want {%s,%s}", gotIDs, h1.ID, h2.ID)
	}

	handlesB := o.ListAgentHandles("s-B")
	if len(handlesB) != 1 || handlesB[0].ID != other.ID {
		t.Errorf("ListAgentHandles(s-B) = %+v, want [%s]", handlesB, other.ID)
	}

	if got := o.ListAgentHandles("nonexistent"); len(got) != 0 {
		t.Errorf("unknown session should return empty slice, got %v", got)
	}

	close(unblock)
	<-h1.Done()
	<-h2.Done()
	<-other.Done()
}

// TestOrchestrator_ListAgentHandles_ExcludesCompletedHandles asserts
// that after a handle's Done closes, ListAgentHandles no longer
// returns it.
// DECISION (documented in godoc + here): ListAgentHandles returns
// only ACTIVE (not yet completed) handles. This method is scoped to
// the TUI pill query replacing HasBackgroundTasks - pills represent
// live work, not history. Completed handles are not a historical
// event log.
func TestOrchestrator_ListAgentHandles_ExcludesCompletedHandles(t *testing.T) {
	savedRunner := runAgentAsyncRunner
	t.Cleanup(func() { runAgentAsyncRunner = savedRunner })
	runAgentAsyncRunner = func(o *Orchestrator, ctx context.Context, cfg AgentConfig) (*AgentResult, error) {
		return &AgentResult{Content: "instant"}, nil
	}

	o := New(WithModel(&mockModel{}))
	h, err := o.RunAgentAsync(t.Context(), AgentConfig{Prompt: "p", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("handle never completed")
	}

	// Poll for registry cleanup; registry unregisters on finish.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(o.ListAgentHandles("s")) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("completed handle still listed: %v", o.ListAgentHandles("s"))
}

// TestOrchestrator_ListAgentHandles_ConcurrentRegistration is a
// race-detector guard. Many goroutines each create handles; the
// registry must not lose any before completion and must not panic.
func TestOrchestrator_ListAgentHandles_ConcurrentRegistration(t *testing.T) {
	unblock := make(chan struct{})
	savedRunner := runAgentAsyncRunner
	t.Cleanup(func() { runAgentAsyncRunner = savedRunner })
	runAgentAsyncRunner = func(o *Orchestrator, ctx context.Context, cfg AgentConfig) (*AgentResult, error) {
		select {
		case <-unblock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &AgentResult{Content: "ok"}, nil
	}

	o := New(WithModel(&mockModel{}))
	const workers = 32
	handles := make([]*AgentHandle, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range workers {
		go func(idx int) {
			defer wg.Done()
			h, err := o.RunAgentAsync(t.Context(), AgentConfig{Prompt: "p", SessionID: "s-X"})
			if err != nil {
				t.Errorf("RunAgentAsync[%d]: %v", idx, err)
				return
			}
			handles[idx] = h
			_ = o.ListAgentHandles("s-X")
		}(i)
	}
	wg.Wait()

	if got := len(o.ListAgentHandles("s-X")); got != workers {
		t.Errorf("active handles = %d, want %d", got, workers)
	}

	close(unblock)
	for _, h := range handles {
		if h != nil {
			select {
			case <-h.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("handle did not finish")
			}
		}
	}
}

// --- helpers ---

func toolNames(tools []provider.ToolDefinition) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

func containsName(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
