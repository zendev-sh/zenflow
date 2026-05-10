package exec

import (
	"context"
	"strings"
	"testing"
	"time"
)

// --- BuildCoordStepMenu ---

func TestBuildCoordStepMenu_NilRunner(t *testing.T) {
	if got := BuildCoordStepMenu(nil); got != "" {
		t.Errorf("BuildCoordStepMenu(nil) = %q, want empty", got)
	}
}

func TestBuildCoordStepMenu_NoRouter(t *testing.T) {
	runner := &AgentRunner{stepID: "coordinator"}
	if got := BuildCoordStepMenu(runner); got != "" {
		t.Errorf("BuildCoordStepMenu(no router) = %q, want empty", got)
	}
}

func TestBuildCoordStepMenu_EmptyRouter(t *testing.T) {
	router := NewMessageRouter()
	runner := &AgentRunner{stepID: "coordinator", router: router}
	if got := BuildCoordStepMenu(runner); got != "" {
		t.Errorf("BuildCoordStepMenu(empty router) = %q, want empty", got)
	}
}

func TestBuildCoordStepMenu_FiltersWrappers(t *testing.T) {
	router := NewMessageRouter()
	router.RegisterStep("setup")
	router.RegisterStep("verdict")
	router.RegisterStep("debate-rounds")
	router.RegisterWrapperStep("debate-rounds")
	router.RegisterStep("debate-rounds.0.pro-argue")
	router.RegisterStep("debate-rounds.0.con-argue")
	router.RegisterStep("debate-rounds.judge")
	runner := &AgentRunner{stepID: "coordinator", router: router}
	got := BuildCoordStepMenu(runner)

	if strings.Contains(got, "  - debate-rounds\n") {
		t.Errorf("wrapper 'debate-rounds' must NOT appear in leaf menu; got:\n%s", got)
	}
	for _, leaf := range []string{"setup", "verdict", "debate-rounds.0.pro-argue", "debate-rounds.0.con-argue", "debate-rounds.judge"} {
		if !strings.Contains(got, "  - "+leaf+"\n") {
			t.Errorf("leaf %q missing; got:\n%s", leaf, got)
		}
	}
	for _, want := range []string{"FORWARDABLE STEPS", "do NOT invent", "WILL fail with unknown-step drop", "PREFER narrate"} {
		if !strings.Contains(got, want) {
			t.Errorf("menu missing required guidance %q; got:\n%s", want, got)
		}
	}
}

func TestBuildCoordStepMenu_ForEachWrapper(t *testing.T) {
	router := NewMessageRouter()
	router.RegisterStep("deploy")
	router.RegisterWrapperStep("deploy")
	router.RegisterStep("deploy[0].provision")
	router.RegisterStep("deploy[1].provision")
	runner := &AgentRunner{stepID: "coordinator", router: router}
	got := BuildCoordStepMenu(runner)
	if strings.Contains(got, "  - deploy\n") {
		t.Errorf("forEach wrapper 'deploy' must NOT appear in leaf menu; got:\n%s", got)
	}
	for _, leaf := range []string{"deploy[0].provision", "deploy[1].provision"} {
		if !strings.Contains(got, "  - "+leaf+"\n") {
			t.Errorf("forEach leaf %q missing; got:\n%s", leaf, got)
		}
	}
}

func TestBuildCoordStepMenu_AllSteps_AreLeaves(t *testing.T) {
	router := NewMessageRouter()
	router.RegisterStep("a")
	router.RegisterStep("b")
	router.RegisterStep("c")
	runner := &AgentRunner{stepID: "coordinator", router: router}
	got := BuildCoordStepMenu(runner)
	for _, leaf := range []string{"a", "b", "c"} {
		if !strings.Contains(got, "  - "+leaf+"\n") {
			t.Errorf("leaf %q missing; got:\n%s", leaf, got)
		}
	}
}

func TestBuildCoordStepMenu_AllWrappers_ReturnsEmpty(t *testing.T) {
	router := NewMessageRouter()
	router.RegisterStep("loop1")
	router.RegisterWrapperStep("loop1")
	router.RegisterStep("loop2")
	router.RegisterWrapperStep("loop2")
	runner := &AgentRunner{stepID: "coordinator", router: router}
	if got := BuildCoordStepMenu(runner); got != "" {
		t.Errorf("BuildCoordStepMenu with all wrappers should return empty; got:\n%s", got)
	}
}

// --- WaitForCoordWake ---

func TestWaitForCoordWake_CtxAlreadyDone_ReturnsFalse(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	runner := &AgentRunner{stepID: "coordinator", mailbox: NewInMemoryMailboxStore(), wake: make(chan struct{}, 1)}
	if WaitForCoordWake(ctx, runner) {
		t.Error("WaitForCoordWake should return false when ctx already cancelled")
	}
}

func TestWaitForCoordWake_PendingMailbox_ReturnsTrueWithoutBlocking(t *testing.T) {
	mb := NewInMemoryMailboxStore()
	if _, err := mb.Append("coordinator", RouterMessage{From: "test", Content: "ping"}); err != nil {
		t.Fatalf("setup append: %v", err)
	}
	runner := &AgentRunner{stepID: "coordinator", mailbox: mb, wake: make(chan struct{}, 1)}

	done := make(chan bool, 1)
	go func() { done <- WaitForCoordWake(t.Context(), runner) }()
	select {
	case got := <-done:
		if !got {
			t.Error("WaitForCoordWake should return true when mailbox has pending")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("WaitForCoordWake blocked despite pending mailbox content")
	}
}

func TestWaitForCoordWake_WakeFires_ReturnsTrue(t *testing.T) {
	wake := make(chan struct{}, 1)
	runner := &AgentRunner{stepID: "coordinator", mailbox: NewInMemoryMailboxStore(), wake: wake}

	done := make(chan bool, 1)
	go func() { done <- WaitForCoordWake(t.Context(), runner) }()
	wake <- struct{}{}
	select {
	case got := <-done:
		if !got {
			t.Error("WaitForCoordWake should return true when Wake fires")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("WaitForCoordWake did not unblock on Wake")
	}
}

func TestWaitForCoordWake_CtxCancelledWhileWaiting_ReturnsFalse(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	runner := &AgentRunner{stepID: "coordinator", mailbox: NewInMemoryMailboxStore(), wake: make(chan struct{}, 1)}

	done := make(chan bool, 1)
	go func() { done <- WaitForCoordWake(ctx, runner) }()
	// Yield long enough for the goroutine to enter the select and
	// block on Wake | ctx.Done. Without this delay the test races
	// the goroutine's first ctx.Err check and may return via the
	// fast path instead of the select case we want to exercise.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case got := <-done:
		if got {
			t.Error("WaitForCoordWake should return false when ctx cancels mid-wait")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("WaitForCoordWake did not unblock on ctx cancel")
	}
}

func TestWaitForCoordWake_NilWake_BlocksOnCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	runner := &AgentRunner{stepID: "coordinator"} // no Mailbox, no Wake
	done := make(chan bool, 1)
	go func() { done <- WaitForCoordWake(ctx, runner) }()
	// Yield long enough for the goroutine to reach the nil-Wake
	// branch and block on ctx.Done. Without this delay the test
	// races the goroutine's first ctx.Err check.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case got := <-done:
		if got {
			t.Error("WaitForCoordWake should return false when ctx cancels and no Wake channel")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("WaitForCoordWake did not unblock when no Wake channel and ctx cancelled")
	}
}

// --- DefaultCoord*Prompt ---

func TestDefaultCoordColdStartPrompt_NotEmpty(t *testing.T) {
	if DefaultCoordColdStartPrompt == "" {
		t.Fatal("DefaultCoordColdStartPrompt is empty")
	}
	for _, want := range []string{"Coordinate the workflow", "wait silently", "do NOT call finalize", "EventStepEnd"} {
		if !strings.Contains(DefaultCoordColdStartPrompt, want) {
			t.Errorf("DefaultCoordColdStartPrompt missing %q", want)
		}
	}
}

func TestDefaultCoordContinuationPrompt_NotEmpty(t *testing.T) {
	if DefaultCoordContinuationPrompt == "" {
		t.Fatal("DefaultCoordContinuationPrompt is empty")
	}
	for _, want := range []string{"Process the new mailbox events", "narration cadence", "Do NOT call finalize"} {
		if !strings.Contains(DefaultCoordContinuationPrompt, want) {
			t.Errorf("DefaultCoordContinuationPrompt missing %q", want)
		}
	}
}

// --- WithCoordSystemPromptSuffix ---

func TestWithCoordSystemPromptSuffix_AppendsToDefault(t *testing.T) {
	const extra = "\n\nADDITIONAL: chat-aware addressing rules apply."
	runner := NewDefaultCoordRunner(stubCoordLanguageModel{}, WithCoordSystemPromptSuffix(extra))
	if !strings.HasPrefix(runner.systemPrompt, DefaultCoordSystemPrompt) {
		t.Error("suffix option must keep DefaultCoordSystemPrompt as prefix")
	}
	if !strings.HasSuffix(runner.systemPrompt, extra) {
		t.Errorf("suffix option must append extra; got tail=%q", runner.systemPrompt[len(runner.systemPrompt)-len(extra):])
	}
}

func TestWithCoordSystemPromptSuffix_EmptyIsNoop(t *testing.T) {
	runner := NewDefaultCoordRunner(stubCoordLanguageModel{}, WithCoordSystemPromptSuffix(""))
	if runner.systemPrompt != DefaultCoordSystemPrompt {
		t.Error("empty suffix must leave default prompt unchanged")
	}
}

func TestWithCoordSystemPromptSuffix_LastWriteWins(t *testing.T) {
	const extra = "\nEXTRA"
	const replace = "REPLACE"

	// Suffix then replace - replace wins.
	r1 := NewDefaultCoordRunner(stubCoordLanguageModel{},
		WithCoordSystemPromptSuffix(extra),
		WithCoordSystemPrompt(replace),
	)
	if r1.systemPrompt != replace {
		t.Errorf("replace after suffix should win; got %q", r1.systemPrompt)
	}

	// Replace then suffix - suffix wins (re-bases on default).
	r2 := NewDefaultCoordRunner(stubCoordLanguageModel{},
		WithCoordSystemPrompt(replace),
		WithCoordSystemPromptSuffix(extra),
	)
	if r2.systemPrompt != DefaultCoordSystemPrompt+extra {
		t.Errorf("suffix after replace should win and re-base on default; got %q", r2.systemPrompt)
	}
}
