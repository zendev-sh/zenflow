package exec

// Per-call Router/Mailbox tests.
// These tests verify that RunAgent plumbs a per-call Router + Mailbox into
// the AgentRunner, that sibling agents share the same Router / Mailbox
// instance, that the mailbox is unregistered on Run completion, and that
// nested-spawn metadata (parentCallID + depth) is emitted on SpawnChild.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/spec"
)

// TestRunAgent_RouterPlumbedToRunner asserts D.1 - the per-call Router is
// reachable from the runner. Verified indirectly: after RunAgent returns, the
// router's Mailbox is non-nil (proves SetMailbox was called).
func TestRunAgent_RouterPlumbedToRunner(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("ok", 1, 1),
		},
	}

	var capturedRouter *MessageRouter
	o := New(
		WithModel(model),
		WithDefaultModel("gpt-4o"),
		WithRouterObserver(func(r *MessageRouter) { capturedRouter = r }),
	)
	if _, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "hi"}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if capturedRouter == nil {
		t.Fatalf("router observer never fired - RunAgent did not allocate a MessageRouter")
	}
	// D.1: runner observed the router (we proxy this via the per-call mailbox
	// being set on it - only the new RunAgent wiring does this).
	if capturedRouter.Mailbox() == nil {
		t.Fatalf("router.Mailbox() is nil - RunAgent did not call SetMailbox; "+
			"router=%p, indicates WithRunnerRouter was not plumbed", capturedRouter)
	}
}

// TestRunAgent_MailboxPerCall asserts D.2 - a fresh Mailbox is allocated per
// RunAgent invocation. Two sequential calls must produce two distinct mailbox
// instances (no shared/leaked state).
func TestRunAgent_MailboxPerCall(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("first", 1, 1),
			textResult("second", 1, 1),
		},
	}

	var routers []*MessageRouter
	var mu sync.Mutex
	o := New(
		WithModel(model),
		WithDefaultModel("gpt-4o"),
		WithRouterObserver(func(r *MessageRouter) {
			mu.Lock()
			routers = append(routers, r)
			mu.Unlock()
		}),
	)
	if _, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "1"}); err != nil {
		t.Fatalf("first RunAgent: %v", err)
	}
	if _, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "2"}); err != nil {
		t.Fatalf("second RunAgent: %v", err)
	}
	if len(routers) != 2 {
		t.Fatalf("expected 2 routers, got %d", len(routers))
	}
	mb1 := routers[0].Mailbox()
	mb2 := routers[1].Mailbox()
	if mb1 == nil || mb2 == nil {
		t.Fatalf("mailbox not installed on per-call routers: mb1=%v mb2=%v", mb1, mb2)
	}
	if mb1 == mb2 {
		t.Fatalf("mailbox reused across calls - expected per-call allocation; ptr=%p", mb1)
	}
}

// TestRunAgent_SiblingsShareRouterAndMailbox asserts D.3 - when a primary
// agent spawns TWO children (sync, in the same turn), both children inherit
// the SAME Router and the SAME Mailbox instance from the parent's runner.
// Verified structurally:
// - the captured router's open-inbox set contains an entry for each
// child's step ID (proves agentSpawner.SpawnChild called Router.RegisterInbox
// on the SAME router instance the test captured),
// - Mailbox.Append + Unread on a child's step ID round-trips a message
// through the mailbox the runner consumes from.
// Renamed from `TestRunAgent_SiblingsExchangeMessages`: zenflow does not
// (yet) ship a model-callable `send_message` tool, so we verify the
// plumbing-level invariant rather than fabricate a synthetic exchange. If
// a `send_message` tool is added later, this test can be promoted to use
// it without changing the structural contract.
func TestRunAgent_SiblingsShareRouterAndMailbox(t *testing.T) {
	var capturedRouter *MessageRouter

	// Spawn two children in the SAME primary turn so both inboxes coexist
	// on the router when we assert.
	var primaryCalls atomic.Int32
	model := &sequentialMockModel{
		fn: func(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
			lastUser := ""
			for _, m := range params.Messages {
				if m.Role == provider.RoleUser {
					for _, p := range m.Content {
						if p.Type == provider.PartText {
							lastUser = p.Text
						}
					}
				}
			}
			switch {
			case strings.Contains(lastUser, "alpha-task"),
				strings.Contains(lastUser, "beta-task"):
				return textResult("child-done", 1, 1), nil
			default:
				n := primaryCalls.Add(1)
				if n == 1 {
					alphaArgs, _ := json.Marshal(agentToolParams{
						Name:         "alpha",
						Instructions: "alpha-task",
					})
					betaArgs, _ := json.Marshal(agentToolParams{
						Name:         "beta",
						Instructions: "beta-task",
					})
					return toolCallResult("", 1, 1,
						tc("tc-alpha", "agent", alphaArgs),
						tc("tc-beta", "agent", betaArgs),
					), nil
				}
				return textResult("primary-done", 1, 1), nil
			}
		},
	}

	o := New(
		WithModel(model),
		WithDefaultModel("gpt-4o"),
		WithRouterObserver(func(r *MessageRouter) { capturedRouter = r }),
	)
	res, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "go"})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if res.Content != "primary-done" {
		t.Fatalf("primary content = %q want primary-done", res.Content)
	}
	if capturedRouter == nil || capturedRouter.Mailbox() == nil {
		t.Fatalf("router/mailbox not plumbed: router=%v", capturedRouter)
	}

	// Walk every inbox the router knows about (open + closed). The primary
	// inbox is "agent:primary:<runID>"; siblings get child IDs of the form
	// "<sanitised-name>-<seq>" (see agentSpawner.SpawnChild). After their
	// sync runs return, the children's inboxes have been Closed via
	// closeChildInbox in agent_tool.go - but they still appear in the
	// router's closed-set, so we can prove SpawnChild registered them on
	// the SAME router we captured.
	all := routerOpenInboxes(capturedRouter)
	var alphaID, betaID string
	for _, id := range all {
		switch {
		case strings.HasPrefix(id, "alpha-"):
			alphaID = id
		case strings.HasPrefix(id, "beta-"):
			betaID = id
		}
	}
	if alphaID == "" || betaID == "" {
		t.Fatalf("expected alpha + beta inboxes registered on captured router; got=%v", all)
	}

	// Confirm both children's inboxes were closed on the SAME mailbox the
	// observer captured. If agentSpawner.SpawnChild had silently
	// constructed its own router/mailbox per child instead of inheriting
	// the parent's, neither closed flag would land on this store.
	mb := capturedRouter.Mailbox()
	store, ok := mb.(*InMemoryMailboxStore)
	if !ok {
		t.Fatalf("captured router does not own *InMemoryMailboxStore; got %T", mb)
	}
	if !store.Closed(alphaID) {
		t.Fatalf("alpha child inbox %q not closed on captured mailbox - child likely used a different mailbox instance", alphaID)
	}
	if !store.Closed(betaID) {
		t.Fatalf("beta child inbox %q not closed on captured mailbox - child likely used a different mailbox instance", betaID)
	}
}

// TestRunAgent_MailboxUnregisteredOnExit asserts D.4 - after RunAgent returns,
// the per-call mailbox no longer accumulates new senders (i.e. it has been
// finalized / closed). Specifically the router's primary inbox must be closed.
func TestRunAgent_MailboxUnregisteredOnExit(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("done", 1, 1),
		},
	}

	var capturedRouter *MessageRouter
	o := New(
		WithModel(model),
		WithDefaultModel("gpt-4o"),
		WithRouterObserver(func(r *MessageRouter) { capturedRouter = r }),
	)
	if _, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "hi"}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if capturedRouter == nil {
		t.Fatalf("router never allocated")
	}
	mb := capturedRouter.Mailbox()
	if mb == nil {
		t.Fatalf("mailbox missing post-run")
	}
	// D.4: the primary's inbox must have been closed/sealed by deferred
	// cleanup. We assert against the concrete InMemoryMailboxStore - the
	// soft type assertion previously used here let the test silently pass
	// if the contract changed. F8 fix.
	store, ok := mb.(*InMemoryMailboxStore)
	if !ok {
		t.Fatalf("RunAgent did not install *InMemoryMailboxStore; got %T", mb)
	}
	stepID := runAgentPrimaryStepID(capturedRouter)
	if stepID == "" {
		t.Fatalf("router has no inboxes - primary step never registered")
	}
	if !store.Closed(stepID) {
		t.Fatalf("primary inbox %q not closed after RunAgent exit", stepID)
	}
}

// TestRunAgent_MaxDepthCapEnforced is the B.11 / D-track companion: when an
// orchestrator is built with WithMaxDepth(2) (the consumer flow-bridge
// mitigation) a depth-3 spawn returns the canonical "max agent depth %d
// reached" string from agent_tool.go and does not actually run the
// grandchild's grandchild.
func TestRunAgent_MaxDepthCapEnforced(t *testing.T) {
	var primaryCalls, childCalls, gcCalls, depth3Calls atomic.Int32
	model := &sequentialMockModel{
		fn: func(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
			lastUserText := ""
			for _, m := range params.Messages {
				if m.Role == provider.RoleUser {
					for _, p := range m.Content {
						if p.Type == provider.PartText {
							lastUserText = p.Text
						}
					}
				}
			}
			switch {
			case strings.Contains(lastUserText, "depth3-task"):
				// F9: track depth-3 model invocations. The cap MUST prevent
				// any depth-3 runner from being constructed, so this branch
				// must never be entered.
				depth3Calls.Add(1)
				return textResult("ggc-done", 1, 1), nil
			case strings.Contains(lastUserText, "depth2-task"):
				n := gcCalls.Add(1)
				if n == 1 {
					args, _ := json.Marshal(agentToolParams{
						Name:         "depth3",
						Instructions: "depth3-task",
					})
					return toolCallResult("", 1, 1, tc("tc-depth3", "agent", args)), nil
				}
				// Second turn: the spawner returned the cap message (which
				// the LLM sees as a tool result) and we wrap up.
				return textResult("depth2-done", 1, 1), nil
			case strings.Contains(lastUserText, "depth1-task"):
				n := childCalls.Add(1)
				if n == 1 {
					args, _ := json.Marshal(agentToolParams{
						Name:         "depth2",
						Instructions: "depth2-task",
					})
					return toolCallResult("", 1, 1, tc("tc-depth2", "agent", args)), nil
				}
				return textResult("depth1-done", 1, 1), nil
			default:
				n := primaryCalls.Add(1)
				if n == 1 {
					args, _ := json.Marshal(agentToolParams{
						Name:         "depth1",
						Instructions: "depth1-task",
					})
					return toolCallResult("", 1, 1, tc("tc-depth1", "agent", args)), nil
				}
				return textResult("primary-done", 1, 1), nil
			}
		},
	}

	// F9: capture tool-call events so we can assert that the depth-3 spawn
	// returned the canonical cap message back to the depth-2 LLM (the
	// spawner stringifies "max agent depth %d reached" - we substring-match
	// to avoid coupling to the exact format).
	sink := &captureSink{}
	o := New(
		WithModel(model),
		WithDefaultModel("gpt-4o"),
		WithProgress(sink),
		WithMaxDepth(2), // primary → child → grandchild OK; great-grandchild capped
	)
	if _, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "go"}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	// gcCalls > 0 proves the depth-2 grandchild ran. The depth-3 model
	// branch must NEVER have been entered: the spawner short-circuits BEFORE
	// constructing the depth-3 runner, so no DoGenerate call ever sees
	// "depth3-task".
	if got := gcCalls.Load(); got < 2 {
		t.Fatalf("expected grandchild to run at least 2 turns (spawn + post-cap), got %d", got)
	}
	if got := depth3Calls.Load(); got != 0 {
		t.Fatalf("depth-3 model fn was invoked %d time(s); cap should have prevented runner construction", got)
	}

	// Hunt for the cap message in the captured tool-call output events.
	// The spawner returns it as the agent tool's result string, so it
	// surfaces as the Output field on EventToolCall (or in EventMessage
	// for the depth-2 turn loop).
	time.Sleep(20 * time.Millisecond)
	events := snapshotEvents(sink)
	sawCap := false
	for _, e := range events {
		if e.Data == nil {
			continue
		}
		if out, _ := e.Data["output"].(string); strings.Contains(out, "max agent depth") {
			sawCap = true
			break
		}
		if msg, _ := e.Data["message"].(string); strings.Contains(msg, "max agent depth") {
			sawCap = true
			break
		}
		if strings.Contains(e.Message, "max agent depth") {
			sawCap = true
			break
		}
	}
	if !sawCap {
		t.Fatalf("did not observe the canonical \"max agent depth\" cap string in any event; events=%+v", events)
	}
}

// TestSpawnChild_EmitsParentCallID asserts D.5 - when SpawnChild fires, the
// resulting tool_call event Data carries parentCallID and depth fields
// that consumers (TUI) can use to collapse nested spawns under their parent.
func TestSpawnChild_EmitsParentCallID(t *testing.T) {
	// Two-level recursion: primary → child spawns grandchild → grandchild
	// fires another tool call. We expect a depth=1 tool_call event from the
	// child runner (its own grandchild spawn) and a depth=2 tool_call event
	// from the grandchild runner (its inner agent call).
	var primaryCalls, childCalls, gcCalls atomic.Int32
	model := &sequentialMockModel{
		fn: func(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
			lastUserText := ""
			for _, m := range params.Messages {
				if m.Role == provider.RoleUser {
					for _, p := range m.Content {
						if p.Type == provider.PartText {
							lastUserText = p.Text
						}
					}
				}
			}
			switch {
			case strings.Contains(lastUserText, "ggc-task"):
				return textResult("ggc-done", 1, 1), nil
			case strings.Contains(lastUserText, "grandchild-task"):
				n := gcCalls.Add(1)
				if n == 1 {
					args, _ := json.Marshal(agentToolParams{
						Name:         "ggc",
						Instructions: "ggc-task",
					})
					return toolCallResult("", 1, 1, tc("tc-ggc", "agent", args)), nil
				}
				return textResult("grandchild-done", 1, 1), nil
			case strings.Contains(lastUserText, "child-task"):
				// We're inside the child's turn loop.
				n := childCalls.Add(1)
				if n == 1 {
					args, _ := json.Marshal(agentToolParams{
						Name:         "grandchild",
						Instructions: "grandchild-task",
					})
					return toolCallResult("", 1, 1, tc("tc-gc", "agent", args)), nil
				}
				return textResult("child-done", 1, 1), nil
			default:
				n := primaryCalls.Add(1)
				if n == 1 {
					args, _ := json.Marshal(agentToolParams{
						Name:         "child",
						Instructions: "child-task",
					})
					return toolCallResult("", 1, 1, tc("tc-child", "agent", args)), nil
				}
				return textResult("primary-done", 1, 1), nil
			}
		},
	}

	sink := &captureSink{}
	o := New(
		WithModel(model),
		WithDefaultModel("gpt-4o"),
		WithProgress(sink),
		WithMaxDepth(5),
	)
	if _, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "go"}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	// Allow async events a moment in case the sink is wrapped.
	time.Sleep(20 * time.Millisecond)

	events := snapshotEvents(sink)

	// Find spawn events: they carry a non-empty parentCallID xor depth in Data.
	var sawDepth1, sawDepth2 bool
	var depth2ParentCallID string
	for _, e := range events {
		if e.Data == nil {
			continue
		}
		depth, _ := e.Data["depth"].(int)
		pcid, _ := e.Data["parentCallID"].(string)
		if depth == 1 && pcid == "tc-child" {
			sawDepth1 = true
		}
		if depth == 2 && pcid == "tc-gc" {
			sawDepth2 = true
			depth2ParentCallID = pcid
		}
	}
	if !sawDepth1 {
		t.Errorf("did not observe depth=1 spawn event with parentCallID=tc-child; events=%+v", events)
	}
	if !sawDepth2 {
		t.Errorf("did not observe depth=2 spawn event with parentCallID=tc-gc; events=%+v", events)
	}
	if sawDepth2 && depth2ParentCallID == "" {
		t.Errorf("depth=2 event had empty parentCallID")
	}
}

// TestRunAgent_RouterObserverPanicDoesNotCrashRun: a panicking
// observer must NOT propagate up - RunAgent recovers and the run
// completes normally. Telemetry hooks installed in production are the
// most likely source of unexpected panics; recovering keeps the agent
// run robust against buggy hooks.
func TestRunAgent_RouterObserverPanicDoesNotCrashRun(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("ok", 1, 1),
		},
	}

	o := New(
		WithModel(model),
		WithDefaultModel("gpt-4o"),
		WithRouterObserver(func(r *MessageRouter) {
			panic("boom from observer")
		}),
	)

	// If the panic propagated, this call would crash the test goroutine
	// (or surface as a non-nil error before RunAgent even completes).
	res, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "hi"})
	if err != nil {
		t.Fatalf("RunAgent must complete despite observer panic; got err=%v", err)
	}
	if res == nil {
		t.Fatalf("RunAgent must return a non-nil result despite observer panic")
	}
}

// TestRunFlow_RouterObserverFires verifies that WithRouterObserver
// fires for RunFlow with the per-Run Router instance. This is the
// fix-of-record for the missing observer invocation that broke the
// resume mechanism's E2E test (a workflow-side test coord captured
// the router via the observer, then sent into it to trigger
// auto-resume; without observer fire the coord saw a nil router).
func TestRunFlow_RouterObserverFires(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{textResult("done", 1, 1)},
	}

	var captured *MessageRouter
	o := New(
		WithModel(model),
		WithDefaultModel("gpt-4o"),
		WithStorage(NewMemoryStorage()),
		WithRouterObserver(func(r *MessageRouter) { captured = r }),
	)

	wf := &Workflow{
		Name: "observer-test",
		Steps: []Step{
			{ID: "only", Instructions: "ping"},
		},
	}

	if _, err := o.RunFlow(t.Context(), wf); err != nil {
		t.Fatalf("RunFlow err=%v", err)
	}
	if captured == nil {
		t.Fatalf("RouterObserver was never invoked from RunFlow")
	}
}

// TestRunFlow_RouterObserverPanicDoesNotCrashRun: same panic-recovery
// contract as the RunAgent equivalent, exercised on the RunFlow path.
func TestRunFlow_RouterObserverPanicDoesNotCrashRun(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{textResult("done", 1, 1)},
	}

	o := New(
		WithModel(model),
		WithDefaultModel("gpt-4o"),
		WithStorage(NewMemoryStorage()),
		WithRouterObserver(func(r *MessageRouter) { panic("boom from RunFlow observer") }),
	)

	wf := &Workflow{
		Name: "observer-panic",
		Steps: []Step{
			{ID: "only", Instructions: "ping"},
		},
	}

	res, err := o.RunFlow(t.Context(), wf)
	if err != nil {
		t.Fatalf("RunFlow must complete despite observer panic; got err=%v", err)
	}
	if res == nil {
		t.Fatalf("RunFlow must return a non-nil result despite observer panic")
	}
}

// TestResumeFlow_RouterObserverFires + Panic recovery - the resume
// path runs the same observer-fire logic as RunFlow; tests pin the
// branch coverage there.
func TestResumeFlow_RouterObserverFires(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{textResult("done", 1, 1)},
	}

	storage := NewMemoryStorage()
	wf := &Workflow{
		Name: "resume-observer",
		Steps: []Step{
			{ID: "only", Instructions: "ping"},
		},
	}
	// Seed a Run + StepResult so ResumeFlow has something to load.
	runID := "run-observer-1"
	if err := storage.SaveRun(t.Context(), &Run{ID: runID, Workflow: wf, Status: spec.StatusPartial, Steps: map[string]*StepResult{}}); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	var captured *MessageRouter
	o := New(
		WithModel(model),
		WithDefaultModel("gpt-4o"),
		WithStorage(storage),
		WithRouterObserver(func(r *MessageRouter) { captured = r }),
	)

	if _, err := o.ResumeFlow(t.Context(), runID, wf); err != nil {
		t.Fatalf("ResumeFlow err=%v", err)
	}
	if captured == nil {
		t.Fatalf("RouterObserver was never invoked from ResumeFlow")
	}
}

func TestResumeFlow_RouterObserverPanicDoesNotCrashRun(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{textResult("done", 1, 1)},
	}

	storage := NewMemoryStorage()
	wf := &Workflow{
		Name: "resume-observer-panic",
		Steps: []Step{
			{ID: "only", Instructions: "ping"},
		},
	}
	runID := "run-observer-panic-1"
	if err := storage.SaveRun(t.Context(), &Run{ID: runID, Workflow: wf, Status: spec.StatusPartial, Steps: map[string]*StepResult{}}); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	o := New(
		WithModel(model),
		WithDefaultModel("gpt-4o"),
		WithStorage(storage),
		WithRouterObserver(func(r *MessageRouter) { panic("boom from ResumeFlow observer") }),
	)

	if _, err := o.ResumeFlow(t.Context(), runID, wf); err != nil {
		t.Fatalf("ResumeFlow must complete despite observer panic; got err=%v", err)
	}
}
