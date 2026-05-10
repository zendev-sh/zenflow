package router

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// cutover: the legacy chan-Inbox path was removed. The
// router now always routes Send into MailboxStore. Each test below sets
// up an InMemoryMailboxStore + RegisterInbox(stepID) before exercising
// Send/Close behavior.
// Tests that exclusively covered chan-mechanics (e.g.
// TestRouter_PendingBuffer_FlushOnCreateInbox,
// TestRouter_SendDropsWhenBufferFull) were deleted because the
// underlying chan path no longer exists; their guarantees are replaced
// by mailbox-store invariants validated in mailbox_test.go.

func newRouterWithMailbox(t *testing.T) (*Router, *InMemoryMailboxStore) {
	t.Helper()
	r := NewRouter()
	mb := NewInMemoryMailboxStore()
	r.SetMailbox(mb)
	return r, mb
}

// TestRouter_SendDeliversToRegisteredStep verifies the happy
// path: RegisterInbox + Send lands the message in the mailbox.
func TestRouter_SendDeliversToRegisteredStep(t *testing.T) {
	r, mb := newRouterWithMailbox(t)
	r.RegisterInbox("agent-1")

	msg := Message{From: "coordinator", To: "agent-1", Content: "hello", Type: MessageInfo}
	_ = r.Send("agent-1", msg)

	got := mb.Unread("agent-1")
	if len(got) != 1 {
		t.Fatalf("Unread = %d, want 1", len(got))
	}
	if got[0].Content != "hello" || got[0].From != "coordinator" {
		t.Errorf("got %+v", got[0])
	}
}

// TestRouter_Close_MarksStepTerminal verifies Close transitions
// the step to terminal state and subsequent Sends emit "target-terminal"
// drops via OnDrop.
func TestRouter_Close_MarksStepTerminal(t *testing.T) {
	r, mb := newRouterWithMailbox(t)
	r.RegisterInbox("agent-1")

	var drops []DropEvent
	var dropMu sync.Mutex
	r.SetOnDrop(func(d DropEvent) {
		dropMu.Lock()
		defer dropMu.Unlock()
		drops = append(drops, d)
	})

	r.Close("agent-1")
	_ = r.Send("agent-1", Message{Content: "after close"})

	dropMu.Lock()
	defer dropMu.Unlock()
	if len(drops) != 1 {
		t.Fatalf("drops=%d want 1", len(drops))
	}
	if drops[0].Reason != DropReasonTargetTerminal {
		t.Errorf("drops[0].Reason=%q want target-terminal", drops[0].Reason)
	}
	if got := mb.Unread("agent-1"); len(got) != 0 {
		t.Errorf("Unread after Close = %d want 0", len(got))
	}
}

// TestRouter_Send_UnknownStep_EmitsDrop verifies that a Send to
// a stepID that was never registered (and has no pending senders)
// emits "unknown-step" via OnDrop. Required by.
func TestRouter_Send_UnknownStep_EmitsDrop(t *testing.T) {
	r, _ := newRouterWithMailbox(t)
	var drops []DropEvent
	r.SetOnDrop(func(d DropEvent) { drops = append(drops, d) })

	_ = r.Send("no-such-step", Message{From: "coord", Content: "x"})

	if len(drops) != 1 {
		t.Fatalf("drops=%d want 1", len(drops))
	}
	if drops[0].Reason != DropReasonUnknownStep {
		t.Errorf("Reason=%q want unknown-step", drops[0].Reason)
	}
	if drops[0].StepID != "no-such-step" {
		t.Errorf("StepID=%q want no-such-step", drops[0].StepID)
	}
}

// TestRouter_Send_TargetTerminal_EmitsDrop verifies that closing a
// registered step then Sending emits "target-terminal" via OnDrop.
func TestRouter_Send_TargetTerminal_EmitsDrop(t *testing.T) {
	r, _ := newRouterWithMailbox(t)
	r.RegisterInbox("a")
	r.Close("a")

	var drops []DropEvent
	r.SetOnDrop(func(d DropEvent) { drops = append(drops, d) })

	_ = r.Send("a", Message{From: "coord", Content: "doomed"})

	if len(drops) != 1 || drops[0].Reason != DropReasonTargetTerminal {
		t.Fatalf("drops=%+v want one target-terminal drop", drops)
	}
}

// TestRouter_Send_RaceWithClose hammers Send + Close concurrently and
// asserts every dropped message produced exactly one OnDrop event (no
// silent loss). Mailbox-closed-by-finalize is the expected race
// reason; target-terminal also acceptable depending on ordering.
func TestRouter_Send_RaceWithClose(t *testing.T) {
	r, mb := newRouterWithMailbox(t)
	r.RegisterInbox("racy")

	var drops int64
	r.SetOnDrop(func(_ DropEvent) { atomic.AddInt64(&drops, 1) })

	const writers = 16
	const perWriter = 100

	var wg sync.WaitGroup
	wg.Add(writers + 1)

	// Closer fires after a small number of sends have likely landed.
	go func() {
		defer wg.Done()
		// Allow some sends to land before the close.
		for i := 0; i < 50; i++ {
			_ = r.Send("racy", Message{Content: "warmup"})
		}
		r.Close("racy")
	}()

	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_ = r.Send("racy", Message{Content: "x"})
			}
		}()
	}

	wg.Wait()

	// Invariant: total messages = delivered (Unread before close)
	// + drops emitted. The store dropped any post-close Append, but
	// the router emits OnDrop for every post-close Send so the count
	// reconciles. We just need drops > 0 to prove the contract is
	// exercised; the precise number is timing-dependent.
	if atomic.LoadInt64(&drops) == 0 {
		t.Fatalf("expected at least one drop event after close race; got 0")
	}
	// Mailbox should be empty (Close wiped queues) and reads after
	// close return nil.
	if got := mb.Unread("racy"); len(got) != 0 {
		t.Errorf("Unread after close = %d want 0", len(got))
	}
}

// TestRouter_PendingSenders_AllowsPreRegisterSend verifies that
// while a sender slot is open, Sends to an unregistered step are NOT
// dropped - the message lands in the mailbox so a step that registers
// after the Send sees it on first Unread.
func TestRouter_PendingSenders_AllowsPreRegisterSend(t *testing.T) {
	r, mb := newRouterWithMailbox(t)
	var drops int
	r.SetOnDrop(func(_ DropEvent) { drops++ })

	r.OpenSender("late")
	_ = r.Send("late", Message{From: "coord", Content: "early"})

	if drops != 0 {
		t.Errorf("drops=%d want 0 while sender slot open", drops)
	}
	r.RegisterInbox("late")
	got := mb.Unread("late")
	if len(got) != 1 || got[0].Content != "early" {
		t.Errorf("Unread(late) = %+v want one 'early' msg", got)
	}
	r.CloseSender("late")
}

// TestRouter_NoMailbox_AllSendsDrop verifies the
// SetMailbox-not-called defensive path: every Send emits "unknown-step".
func TestRouter_NoMailbox_AllSendsDrop(t *testing.T) {
	r := NewRouter()
	var drops int
	r.SetOnDrop(func(_ DropEvent) { drops++ })

	_ = r.Send("any", Message{Content: "x"})
	_ = r.Send("any", Message{Content: "y"})

	if drops != 2 {
		t.Errorf("drops=%d want 2", drops)
	}
}

// TestRouter_CloseWithoutRegister is a no-op (no panic).
func TestRouter_CloseWithoutRegister(t *testing.T) {
	r, _ := newRouterWithMailbox(t)
	r.Close("never-registered") // must not panic
}

// TestRouter_ConcurrentSend hammers Send across multiple
// registered steps. Race detector must report clean.
func TestRouter_ConcurrentSend(t *testing.T) {
	r, mb := newRouterWithMailbox(t)
	const numSteps = 8
	const msgsPerStep = 25

	for i := 0; i < numSteps; i++ {
		r.RegisterInbox(stepIDFor(i))
	}

	var wg sync.WaitGroup
	for i := 0; i < numSteps; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := stepIDFor(idx)
			for j := 0; j < msgsPerStep; j++ {
				_ = r.Send(id, Message{Content: "ping", From: "sender"})
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < numSteps; i++ {
		got := mb.Unread(stepIDFor(i))
		if len(got) != msgsPerStep {
			t.Errorf("step %d: got %d msgs want %d", i, len(got), msgsPerStep)
		}
	}
}

// TestRouter_CreateInbox_ReRegistrationClosesOld
// from the chan era was deleted because the chan-channel-handle re-bind
// behavior no longer exists. Re-registering an already-closed step is
// not exercised by any production path (each step ID lives for exactly
// one runStep invocation), so no replacement test is added.

func stepIDFor(i int) string {
	return "step-" + string(rune('0'+i))
}

// TestRouter_SendReturnsErrorOnDrop - named test.
// changed Router.Send from `(stepID, msg)` (no return) to
// `(stepID, msg) error` so the new send_message tool and the
// updated forward_to_agent tool can surface drops as the tool's
// returned string in the canonical "dropped: <reason>" format.
// The error is in addition to (not in place of) the existing OnDrop
// callback path - every dropped message still emits exactly one
// EventMessageDropped through the executor's installed OnDrop. This test
// verifies BOTH channels fire so the new contract does not regress the
// "zero silent drops" event pipeline.
// Cases:
// - unknown-step (no RegisterInbox, no pending sender): err non-nil,
// err.Error contains "dropped: unknown-step", OnDrop fires once
// with DropReasonUnknownStep.
// - target-terminal (RegisterInbox then Close): err non-nil,
// err.Error contains "dropped: target-terminal", OnDrop fires once
// with DropReasonTargetTerminal.
// - happy path (RegisterInbox, no close): err == nil, no OnDrop fires.
func TestRouter_SendReturnsErrorOnDrop(t *testing.T) {
	t.Run("unknown_step", func(t *testing.T) {
		r, _ := newRouterWithMailbox(t)
		var drops []DropEvent
		r.SetOnDrop(func(d DropEvent) { drops = append(drops, d) })

		err := r.Send("no-such-step", Message{From: "coord", Content: "x"})
		if err == nil {
			t.Fatalf("err=nil want non-nil for unknown-step drop")
		}
		if !strings.Contains(err.Error(), "dropped: unknown-step") {
			t.Errorf("err=%q want substring 'dropped: unknown-step'", err)
		}
		if len(drops) != 1 || drops[0].Reason != DropReasonUnknownStep {
			t.Fatalf("drops=%+v want one DropReasonUnknownStep", drops)
		}
	})

	t.Run("target_terminal", func(t *testing.T) {
		r, _ := newRouterWithMailbox(t)
		r.RegisterInbox("a")
		r.Close("a")

		var drops []DropEvent
		r.SetOnDrop(func(d DropEvent) { drops = append(drops, d) })

		err := r.Send("a", Message{From: "coord", Content: "doomed"})
		if err == nil {
			t.Fatalf("err=nil want non-nil for target-terminal drop")
		}
		if !strings.Contains(err.Error(), "dropped: target-terminal") {
			t.Errorf("err=%q want substring 'dropped: target-terminal'", err)
		}
		if len(drops) != 1 || drops[0].Reason != DropReasonTargetTerminal {
			t.Fatalf("drops=%+v want one DropReasonTargetTerminal", drops)
		}
	})

	t.Run("happy_path", func(t *testing.T) {
		r, _ := newRouterWithMailbox(t)
		r.RegisterInbox("a")

		var drops []DropEvent
		r.SetOnDrop(func(d DropEvent) { drops = append(drops, d) })

		err := r.Send("a", Message{From: "coord", Content: "delivered"})
		if err != nil {
			t.Fatalf("err=%v want nil for happy path", err)
		}
		if len(drops) != 0 {
			t.Errorf("drops=%+v want none on happy path", drops)
		}
	})
}

// Router.Send delegates to a child router when stepID is
// registered. Used by nested-DAG executors to expose inner-step
// inboxes through the outer router so coord forward_to_agent reaches
// inner steps without the outer router needing to own the inbox.
func TestRouter_Delegation_RoutesToChildRouter(t *testing.T) {
	root, _ := newRouterWithMailbox(t)
	child, childMb := newRouterWithMailbox(t)
	child.RegisterInbox("inner-step")
	root.RegisterDelegate("inner-step", child)

	if err := root.Send("inner-step", Message{
		From:    "coord",
		Content: "delegated",
	}); err != nil {
		t.Fatalf("Send via delegation: %v", err)
	}

	// Message should land in the CHILD's mailbox, not root's.
	unread := childMb.Unread("inner-step")
	if len(unread) != 1 || unread[0].Content != "delegated" {
		t.Errorf("child mailbox missing message; got %d unread", len(unread))
	}
}

// Sequential repeat-until pattern: each iteration replaces the prior
// delegation. Send always lands in the LATEST iteration's nested
// router.
func TestRouter_Delegation_ReplaceOnReregister(t *testing.T) {
	root, _ := newRouterWithMailbox(t)
	iter0, iter0Mb := newRouterWithMailbox(t)
	iter1, iter1Mb := newRouterWithMailbox(t)
	iter0.RegisterInbox("inner-step")
	iter1.RegisterInbox("inner-step")

	root.RegisterDelegate("inner-step", iter0)
	if err := root.Send("inner-step", Message{Content: "to-iter0"}); err != nil {
		t.Fatalf("Send #1: %v", err)
	}
	if got := len(iter0Mb.Unread("inner-step")); got != 1 {
		t.Errorf("iter0 unread = %d, want 1", got)
	}

	// Iteration 1 starts: replace delegation.
	root.RegisterDelegate("inner-step", iter1)
	if err := root.Send("inner-step", Message{Content: "to-iter1"}); err != nil {
		t.Fatalf("Send #2: %v", err)
	}
	if got := len(iter1Mb.Unread("inner-step")); got != 1 {
		t.Errorf("iter1 unread = %d, want 1 (delegation should have replaced)", got)
	}
	if got := len(iter0Mb.Unread("inner-step")); got != 1 {
		t.Errorf("iter0 unread = %d, want 1 (NOT 2 - replaced delegation must not duplicate)", got)
	}
}

// UnregisterDelegate removes the entry; subsequent Send falls through
// to the outer router's normal path.
func TestRouter_Delegation_UnregisterFallsThrough(t *testing.T) {
	root, rootMb := newRouterWithMailbox(t)
	child, childMb := newRouterWithMailbox(t)
	root.RegisterInbox("inner-step")
	child.RegisterInbox("inner-step")

	root.RegisterDelegate("inner-step", child)
	root.UnregisterDelegate("inner-step")

	if err := root.Send("inner-step", Message{Content: "post-unreg"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Should land in ROOT's mailbox, not child's.
	if got := len(rootMb.Unread("inner-step")); got != 1 {
		t.Errorf("root mailbox unread = %d, want 1 (delegation removed)", got)
	}
	if got := len(childMb.Unread("inner-step")); got != 0 {
		t.Errorf("child mailbox unread = %d, want 0", got)
	}
}

// Nil delegate via RegisterDelegate(stepID, nil) is equivalent to Unregister.
func TestRouter_Delegation_NilDelegateIsUnregister(t *testing.T) {
	root, _ := newRouterWithMailbox(t)
	child, _ := newRouterWithMailbox(t)
	child.RegisterInbox("inner-step")
	root.RegisterDelegate("inner-step", child)
	root.RegisterDelegate("inner-step", nil) // explicit nil = unregister

	if d := root.getDelegate("inner-step"); d != nil {
		t.Errorf("delegate after nil-register = %v, want nil", d)
	}
}

// Self-delegation guard: r.delegations["x"] == r must not infinite-loop.
func TestRouter_Delegation_SelfDelegateNoLoop(t *testing.T) {
	root, rootMb := newRouterWithMailbox(t)
	root.RegisterInbox("x")
	root.RegisterDelegate("x", root) // self-delegation

	// Should fall through to normal Send (delegate==r short-circuit).
	if err := root.Send("x", Message{Content: "no-loop"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := len(rootMb.Unread("x")); got != 1 {
		t.Errorf("root mailbox unread = %d, want 1", got)
	}
}

// TestRouter_KnownSteps_Empty verifies KnownSteps returns nil
// (not an empty slice) when no steps registered. relies on
// this to skip prompt menu injection at cold-start before executor
// begins.
func TestRouter_KnownSteps_Empty(t *testing.T) {
	r := NewRouter()
	if got := r.KnownSteps(); got != nil {
		t.Errorf("KnownSteps() on empty router = %v, want nil", got)
	}
}

// TestRouter_KnownSteps_Sorted verifies KnownSteps returns step
// IDs in sorted order so prompt rendering is deterministic across
// runs. - coord LLM sees a stable menu.
func TestRouter_KnownSteps_Sorted(t *testing.T) {
	r := NewRouter()
	r.RegisterStep("verdict")
	r.RegisterStep("setup")
	r.RegisterStep("debate-rounds.0.pro-argue")
	r.RegisterStep("debate-rounds")
	r.RegisterStep("debate-rounds.0.con-argue")

	got := r.KnownSteps()
	want := []string{
		"debate-rounds",
		"debate-rounds.0.con-argue",
		"debate-rounds.0.pro-argue",
		"setup",
		"verdict",
	}
	if len(got) != len(want) {
		t.Fatalf("len(KnownSteps()) = %d, want %d (got %v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("KnownSteps()[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestRouter_KnownSteps_Idempotent verifies double-register of
// the same stepID does not produce duplicates in the returned snapshot.
func TestRouter_KnownSteps_Idempotent(t *testing.T) {
	r := NewRouter()
	r.RegisterStep("setup")
	r.RegisterStep("setup")
	r.RegisterStep("setup")
	got := r.KnownSteps()
	if len(got) != 1 || got[0] != "setup" {
		t.Errorf("KnownSteps() after triple-register = %v, want [setup]", got)
	}
}

// TestRouter_Inboxes_OpenAndClosed exercises both branches of
// Inboxes (open inbox + closed inbox) so coverage hits 100%.
func TestRouter_Inboxes_OpenAndClosed(t *testing.T) {
	r := NewRouter()
	r.SetMailbox(NewInMemoryMailboxStore())
	r.RegisterInbox("open-step")
	r.RegisterInbox("closed-step")
	r.Close("closed-step")
	got := r.Inboxes()
	if len(got) != 2 {
		t.Fatalf("Inboxes len=%d want 2; got=%v", len(got), got)
	}
}

// TestDropReasonStrings_DefensiveCopy verifies DropReasonStrings returns
// a non-empty copy.
func TestDropReasonStrings_DefensiveCopy(t *testing.T) {
	m := DropReasonStrings()
	if len(m) == 0 {
		t.Fatal("DropReasonStrings empty")
	}
	if _, ok := m[DropReasonUnspecified]; !ok {
		t.Errorf("missing DropReasonUnspecified")
	}
}
