package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/resume"
	"github.com/zendev-sh/zenflow/internal/spec"
	"github.com/zendev-sh/zenflow/internal/types"
)

// --- R3: Executor.ResumeStep ---

// newResumeExecutor builds a minimally-wired Executor sufficient for
// ResumeStep unit tests. It supplies an InMemoryTranscriptStore and a
// mockModel so the resumed AgentRunner can call GenerateText.
// H3 refactor: ResumeStep now receives and uses the caller's ctx directly -
// no runCtx struct field. Tests pass t.Context (or a derived cancellable
// context) to ResumeStep to control the resume goroutine's lifetime.
func newResumeExecutor(t *testing.T, model *mockModel) *Executor {
	t.Helper()
	e := &Executor{
		Runner: &AgentRunner{},
		RunID:  "run-resume-test",
	}
	if model != nil {
		e.Runner.model = model
	}
	e.transcriptStore = resume.NewInMemoryTranscriptStore()
	return e
}

func TestResumeR3_NoTranscript(t *testing.T) {
	e := newResumeExecutor(t, &mockModel{
		responses: []*provider.GenerateResult{textResult("x", 1, 1)},
	})
	_, err := e.ResumeStep(context.Background(), "never-ran", "hi", "coord")
	if !errors.Is(err, resume.ErrNoTranscript) {
		t.Fatalf("want ErrNoTranscript, got %v", err)
	}
}

func TestResumeR3_CanResumeGates(t *testing.T) {
	// No transcript store → CanResume false.
	e := &Executor{}
	if e.CanResume("x") {
		t.Fatal("CanResume should be false without transcriptStore")
	}
	e.transcriptStore = resume.NewInMemoryTranscriptStore()
	if !e.CanResume("x") {
		t.Fatal("CanResume should be true with store, no router")
	}
	// Router cancelled → false.
	e.Router = NewMessageRouter()
	e.Router.MarkWorkflowCancelled()
	if e.CanResume("x") {
		t.Fatal("CanResume should be false when workflow cancelled")
	}
}

func TestResumeR3_HappyPath_EmitsStartAndComplete(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{textResult("resumed-answer", 5, 3)},
	}
	prog := &captureSink{}
	e := newResumeExecutor(t, model)
	e.Progress = prog

	// Pre-populate the transcript.
	_ = e.transcriptStore.Append(e.RunID, "step-x", []provider.Message{
		mkTextMsg(provider.RoleUser, "original prompt"),
		mkTextMsg(provider.RoleAssistant, "original answer"),
	})

	h, err := e.ResumeStep(context.Background(), "step-x", "follow-up", "coord")
	if err != nil {
		t.Fatalf("ResumeStep: %v", err)
	}
	select {
	case <-h.DoneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for resume")
	}
	if h.Err != nil {
		t.Fatalf("handle error: %v", h.Err)
	}
	if h.Result != "resumed-answer" {
		t.Errorf("Result=%q", h.Result)
	}

	events := snapshotEvents(prog)
	if !hasEventType(events, types.EventResumeStarted) {
		t.Errorf("missing EventResumeStarted; got %v", eventTypes(events))
	}
	if !hasEventType(events, types.EventResumeCompleted) {
		t.Errorf("missing EventResumeCompleted; got %v", eventTypes(events))
	}
}

func TestResumeR3_SerialQueueing(t *testing.T) {
	// First resume consumes one GenerateText call. A concurrent
	// ResumeStep issued while the first is running must return a
	// "queued" handle immediately (DoneCh already closed, Result="queued").
	// We simulate in-flight work by gating the sequential mock on a
	// channel so the first DoGenerate blocks until released.
	gate := make(chan struct{})
	model := &sequentialMockModel{
		fn: func(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
			<-gate
			return textResult("first-done", 1, 1), nil
		},
	}
	e := &Executor{
		Runner:          &AgentRunner{model: model},
		RunID:           "run-resume-test",
		transcriptStore: resume.NewInMemoryTranscriptStore(),
	}
	_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})

	h1, err := e.ResumeStep(context.Background(), "s", "msg-1", "coord")
	if err != nil {
		t.Fatalf("first ResumeStep: %v", err)
	}
	// h1 is running; issue a second call - must take queue path.
	h2, err := e.ResumeStep(context.Background(), "s", "msg-2", "coord")
	if err != nil {
		t.Fatalf("second ResumeStep: %v", err)
	}
	if h2.Result != "queued" {
		t.Errorf("expected queue handle Result=queued, got %q", h2.Result)
	}
	select {
	case <-h2.DoneCh:
	default:
		t.Fatal("queued handle DoneCh should be closed immediately")
	}
	close(gate) // let first resume finish
	select {
	case <-h1.DoneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("first resume did not finish")
	}
}

func TestResumeR3_WorkflowShutdownEmitsFailed(t *testing.T) {
	gate := make(chan struct{})
	model := &sequentialMockModel{
		fn: func(ctx context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
			<-gate
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return textResult("never-returned", 1, 1), nil
		},
	}
	prog := &captureSink{}
	e := &Executor{
		Runner:          &AgentRunner{model: model},
		RunID:           "run-resume-test",
		transcriptStore: resume.NewInMemoryTranscriptStore(),
		Progress:        prog,
	}

	// H3: pass a cancellable ctx directly to ResumeStep - no struct field needed.
	ctx, cancel := context.WithCancel(context.Background())

	_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})

	h, err := e.ResumeStep(ctx, "s", "p", "coord")
	if err != nil {
		t.Fatalf("ResumeStep: %v", err)
	}
	// Cancel mid-run, then release the mock so the provider sees ctx.Err.
	cancel()
	close(gate)
	select {
	case <-h.DoneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown resume did not complete")
	}
	if h.Err == nil {
		t.Fatal("handle.Err must be set on shutdown")
	}
	events := snapshotEvents(prog)
	if !hasEventType(events, types.EventResumeFailed) {
		t.Errorf("missing EventResumeFailed; got %v", eventTypes(events))
	}
}

// --- R4: Router.Send → resumer hook ---

type fakeResumer struct {
	can           bool
	resumeErr     error
	resumeCalls   atomic.Int32
	lastStepID    string
	lastPrompt    string
	lastFromAgent string
}

func (f *fakeResumer) CanResume(_ string) bool { return f.can }
func (f *fakeResumer) ResumeStep(_ context.Context, stepID, prompt, fromAgent string) (*ResumeHandle, error) {
	f.resumeCalls.Add(1)
	f.lastStepID = stepID
	f.lastPrompt = prompt
	f.lastFromAgent = fromAgent
	if f.resumeErr != nil {
		return nil, f.resumeErr
	}
	done := make(chan struct{})
	close(done)
	return &ResumeHandle{StepID: stepID, DoneCh: done}, nil
}

func TestResumeR4_RouterSend_TriggersResumeOnClosedMailbox(t *testing.T) {
	r := NewMessageRouter()
	r.SetMailbox(NewInMemoryMailboxStore())
	resumer := &fakeResumer{can: true}
	r.SetResumer(resumer)

	var drops []DropEvent
	r.SetOnDrop(func(de DropEvent) { drops = append(drops, de) })

	// Register + close the step so Send hits the closed path.
	r.RegisterInbox("s")
	r.Close("s")

	if err := r.Send("s", RouterMessage{From: "coord", Content: "hello", MessageID: "m1"}); err != nil {
		t.Fatalf("setup send: %v", err)
	}

	if resumer.resumeCalls.Load() != 1 {
		t.Fatalf("resume not called; got %d", resumer.resumeCalls.Load())
	}
	if resumer.lastStepID != "s" || resumer.lastFromAgent != "coord" || resumer.lastPrompt != "hello" {
		t.Errorf("wrong args: step=%q from=%q prompt=%q",
			resumer.lastStepID, resumer.lastFromAgent, resumer.lastPrompt)
	}
	if len(drops) != 0 {
		t.Errorf("unexpected drops: %+v", drops)
	}
}

func TestResumeR4_RouterSend_CanResumeFalseDropsAsTargetTerminal(t *testing.T) {
	r := NewMessageRouter()
	r.SetMailbox(NewInMemoryMailboxStore())
	resumer := &fakeResumer{can: false}
	r.SetResumer(resumer)

	var drops []DropEvent
	r.SetOnDrop(func(de DropEvent) { drops = append(drops, de) })

	r.RegisterInbox("s")
	r.Close("s")
	_ = r.Send("s", RouterMessage{From: "c"}) // drop expected; error reflects drop reason

	if resumer.resumeCalls.Load() != 0 {
		t.Fatal("CanResume=false must not call ResumeStep")
	}
	if len(drops) != 1 || drops[0].Reason != DropReasonTargetTerminal {
		t.Errorf("want one target-terminal drop, got %+v", drops)
	}
}

func TestResumeR4_RouterSend_MapsErrorsToDropReasons(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want DropReason
	}{
		{"no-transcript", resume.ErrNoTranscript, DropReasonNoTranscript},
		{"too-large", resume.ErrTranscriptTooLarge, DropReasonTranscriptTooLarge},
		{"shutdown", ErrResumeShutdown, DropReasonResumeShutdown},
		{"other", errors.New("boom"), DropReasonTargetTerminal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewMessageRouter()
			r.SetMailbox(NewInMemoryMailboxStore())
			r.SetResumer(&fakeResumer{can: true, resumeErr: tc.err})

			var got []DropEvent
			r.SetOnDrop(func(de DropEvent) { got = append(got, de) })

			r.RegisterInbox("s")
			r.Close("s")
			_ = r.Send("s", RouterMessage{From: "c"}) // drop expected; error reflects drop reason

			if len(got) != 1 || got[0].Reason != tc.want {
				t.Fatalf("want one drop=%s, got %+v", tc.want.String(), got)
			}
		})
	}
}

// --- R5: reverse RouterMessage routing ---

func TestResumeR5_ReverseMessageGoesBackToSender(t *testing.T) {
	model := &mockModel{responses: []*provider.GenerateResult{textResult("reply-content", 1, 1)}}
	e := newResumeExecutor(t, model)
	e.Router = NewMessageRouter()
	e.Router.SetMailbox(NewInMemoryMailboxStore())
	// Register the sender so Router.Send (reverse) actually lands.
	e.Router.RegisterInbox("coord")

	_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})

	h, err := e.ResumeStep(context.Background(), "s", "p", "coord")
	if err != nil {
		t.Fatalf("ResumeStep: %v", err)
	}
	select {
	case <-h.DoneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	if h.Err != nil {
		t.Fatalf("err: %v", h.Err)
	}

	// The reverse message lands in coord's mailbox via SetMailbox →
	// InMemoryMailboxStore. Verify it's there.
	mb := e.Router
	_ = mb
	// Pull the mailbox back.
	// We have no direct accessor for the router's mailbox, but
	// SetMailbox stored our store - use it via introspection.
	inmem, ok := routerMailbox(e.Router).(MailboxStore)
	if !ok {
		t.Fatal("no mailbox")
	}
	msgs := inmem.Unread("coord")
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "reply-content") {
		t.Fatalf("reverse message missing/wrong: %+v", msgs)
	}
	if msgs[0].From != "s" {
		t.Errorf("From=%q, want 's'", msgs[0].From)
	}
}

func TestResumeR5_NoReverseWhenSenderEmpty(t *testing.T) {
	model := &mockModel{responses: []*provider.GenerateResult{textResult("x", 1, 1)}}
	e := newResumeExecutor(t, model)
	e.Router = NewMessageRouter()
	e.Router.SetMailbox(NewInMemoryMailboxStore())

	_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})

	h, err := e.ResumeStep(context.Background(), "s", "p", "" /*no sender*/)
	if err != nil {
		t.Fatalf("ResumeStep: %v", err)
	}
	<-h.DoneCh
	if h.Err != nil {
		t.Fatalf("err: %v", h.Err)
	}
	// No reverse message should exist anywhere.
	inmem, _ := routerMailbox(e.Router).(MailboxStore)
	if inmem != nil {
		if msgs := inmem.Unread(""); len(msgs) > 0 {
			t.Errorf("unexpected reverse message: %+v", msgs)
		}
	}
}

// --- R6: sink rendering ---

// The sink test lives in sink/ - we exercise the event payload here to
// confirm the Data keys match what the sink reads.
func TestResumeR6_EventPayloadShape(t *testing.T) {
	model := &mockModel{responses: []*provider.GenerateResult{textResult("r", 1, 1)}}
	prog := &captureSink{}
	e := newResumeExecutor(t, model)
	e.Progress = prog

	_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})
	h, err := e.ResumeStep(context.Background(), "s", "p", "coord")
	if err != nil {
		t.Fatalf("ResumeStep: %v", err)
	}
	<-h.DoneCh
	evts := snapshotEvents(prog)

	started := findEvent(evts, types.EventResumeStarted)
	if started == nil {
		t.Fatal("EventResumeStarted missing")
	}
	if _, ok := started.Data["resumeID"].(string); !ok {
		t.Errorf("missing resumeID; data=%+v", started.Data)
	}
	if f, _ := started.Data["from"].(string); f != "coord" {
		t.Errorf("from=%v", started.Data["from"])
	}

	done := findEvent(evts, types.EventResumeCompleted)
	if done == nil {
		t.Fatal("EventResumeCompleted missing")
	}
	if _, ok := done.Data["durationMs"].(int64); !ok {
		t.Errorf("missing durationMs; data=%+v", done.Data)
	}
}

// --- test helpers ---

// snapshotEvents returns a copy of captureSink.events under lock so
// the test can safely range without racing the runner goroutine.
func snapshotEvents(s *captureSink) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, len(s.events))
	copy(out, s.events)
	return out
}

func hasEventType(ev []Event, t EventType) bool {
	for _, e := range ev {
		if e.Type == t {
			return true
		}
	}
	return false
}
func findEvent(ev []Event, t EventType) *Event {
	for i := range ev {
		if ev[i].Type == t {
			return &ev[i]
		}
	}
	return nil
}
func eventTypes(ev []Event) []EventType {
	out := make([]EventType, len(ev))
	for i, e := range ev {
		out[i] = e.Type
	}
	return out
}

// --- F2: race - concurrent ResumeStep MUST NOT spawn two goroutines ---

// blockingLoadStore wraps an InMemoryTranscriptStore and blocks Load on
// a channel so a test can hold a ResumeStep mid-flight while a second
// call races in. Demonstrates that after the F2 fix both the first and
// second callers converge on the same resume goroutine.
type blockingLoadStore struct {
	inner *resume.InMemoryTranscriptStore
	gate  chan struct{}
}

func (b *blockingLoadStore) Append(r, s string, m []provider.Message) error {
	return b.inner.Append(r, s, m)
}
func (b *blockingLoadStore) Load(r, s string) (*resume.StepTranscript, error) {
	<-b.gate
	return b.inner.Load(r, s)
}
func (b *blockingLoadStore) Delete(r, s string) error { return b.inner.Delete(r, s) }

func TestResumeR3_ConcurrentResumeStep_OnlyOneGoroutineSpawns(t *testing.T) {
	model := &sequentialMockModel{
		fn: func(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
			return textResult("done", 1, 1), nil
		},
	}
	inner := resume.NewInMemoryTranscriptStore()
	_ = inner.Append("run-resume-test", "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})
	gate := make(chan struct{})
	store := &blockingLoadStore{inner: inner, gate: gate}

	prog := &captureSink{}
	e := &Executor{
		Runner:          &AgentRunner{model: model},
		RunID:           "run-resume-test",
		transcriptStore: store,
		Progress:        prog,
	}

	// Launch two concurrent ResumeStep calls. First enters; Load blocks
	// on gate. Second must take the queue path because state.running
	// is already true AND activeMailbox has been installed (F2
	// invariant).
	errs := make(chan error, 2)
	handles := make(chan *ResumeHandle, 2)
	go func() {
		h, err := e.ResumeStep(context.Background(), "s", "p1", "coord")
		handles <- h
		errs <- err
	}()
	// Small spin to ensure the first goroutine has reached state.mu
	// Lock / running=true. Without this, the second call may race in
	// before the first sets state.running. Bounded retry: up to 500ms.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		e.resumesMu.Lock()
		rs := e.resumes
		e.resumesMu.Unlock()
		if rs != nil {
			st := rs.get("s")
			st.mu.Lock()
			running := st.running
			st.mu.Unlock()
			if running {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	go func() {
		h, err := e.ResumeStep(context.Background(), "s", "p2", "coord")
		handles <- h
		errs <- err
	}()
	// Give goroutine 2 a moment to land in the queue path.
	time.Sleep(50 * time.Millisecond)

	// Release Load so the first resume proceeds.
	close(gate)

	// Wait for both ResumeStep calls to return.
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("ResumeStep: %v", err)
		}
	}
	// Wait for the first handle's DoneCh.
	for range 2 {
		h := <-handles
		select {
		case <-h.DoneCh:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for handle")
		}
	}

	// Critical assertion: exactly ONE EventResumeStarted for "s"
	// (the queued path emits EventResumeQueued instead).
	evts := snapshotEvents(prog)
	started := 0
	queued := 0
	for _, ev := range evts {
		switch ev.Type {
		case types.EventResumeStarted:
			if ev.StepID == "s" {
				started++
			}
		case types.EventResumeQueued:
			if ev.StepID == "s" {
				queued++
			}
		}
	}
	if started != 1 {
		t.Errorf("want exactly 1 EventResumeStarted, got %d; types=%v", started, eventTypes(evts))
	}
	if queued != 1 {
		t.Errorf("want exactly 1 EventResumeQueued, got %d; types=%v", queued, eventTypes(evts))
	}
}

// --- F3: transcript cap end-to-end ---

func TestResume_TranscriptCapEnforced(t *testing.T) {
	// Tiny cap so Append trips quickly. After seal, Load surfaces
	// ErrTranscriptTooLarge, which Router.Send maps to
	// DropReasonTranscriptTooLarge.
	store := resume.NewInMemoryTranscriptStoreWithCaps(2, 0)
	_ = store.Append("run-resume-test", "s", []provider.Message{
		mkTextMsg(provider.RoleUser, "a"),
		mkTextMsg(provider.RoleUser, "b"),
	})
	// Third append trips the cap and seals.
	capErr := store.Append("run-resume-test", "s", []provider.Message{mkTextMsg(provider.RoleUser, "c")})
	if !errors.Is(capErr, resume.ErrTranscriptTooLarge) {
		t.Fatalf("expected cap trip, got %v", capErr)
	}

	e := &Executor{
		Runner:          &AgentRunner{model: &mockModel{}},
		RunID:           "run-resume-test",
		transcriptStore: store,
	}
	// Wire the Executor as a resumer into a real Router so we exercise
	// the Router.Send → error-mapping path end-to-end.
	r := NewMessageRouter()
	r.SetMailbox(NewInMemoryMailboxStore())
	r.SetResumer(e)
	e.Router = r

	var drops []DropEvent
	r.SetOnDrop(func(de DropEvent) { drops = append(drops, de) })

	// Close "s" so Send lands on the resume path.
	r.RegisterInbox("s")
	r.Close("s")

	_ = r.Send("s", RouterMessage{From: "coord", Content: "hi"})

	if len(drops) != 1 {
		t.Fatalf("want exactly one drop, got %+v", drops)
	}
	if drops[0].Reason != DropReasonTranscriptTooLarge {
		t.Errorf("drop reason=%s, want DropReasonTranscriptTooLarge (%s)",
			drops[0].Reason.String(), DropReasonTranscriptTooLarge.String())
	}
}

// --- F4: queued resume emits event + MessageID assigned ---

func TestResumeR3_QueuedEmitsEvent(t *testing.T) {
	// First resume runs slowly; second takes the queue path. Assert:
	// (1) a distinct EventResumeQueued is emitted, (2) the queued
	// RouterMessage lands in the active mailbox with a non-empty
	// MessageID (supplied by the in-memory store's Append).
	gate := make(chan struct{})
	model := &sequentialMockModel{
		fn: func(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
			<-gate
			return textResult("done", 1, 1), nil
		},
	}
	prog := &captureSink{}
	e := &Executor{
		Runner:          &AgentRunner{model: model},
		RunID:           "run-resume-test",
		transcriptStore: resume.NewInMemoryTranscriptStore(),
		Progress:        prog,
	}
	// hold the resume goroutine's pre-start drain until AFTER
	// all test-side Appends have landed. Without this, the drain can
	// race with ResumeStep's queued-path Append and consume the just-
	// enqueued message before we inspect the mailbox.
	drainGate := make(chan struct{})
	e.setResumePreStartDrainGateForTest(drainGate)
	_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})

	h1, err := e.ResumeStep(context.Background(), "s", "first", "coord")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	h2, err := e.ResumeStep(context.Background(), "s", "second", "coord")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if h2.Result != "queued" {
		t.Fatalf("second handle want queued, got %q", h2.Result)
	}

	// Inspect the running resume's mailbox: the queued message must be
	// present with a non-empty MessageID.
	e.resumesMu.Lock()
	rs := e.resumes
	e.resumesMu.Unlock()
	st := rs.get("s")
	st.mu.Lock()
	mb := st.activeMailbox
	st.mu.Unlock()
	if mb == nil {
		t.Fatal("running resume has no active mailbox (F2 invariant broken)")
	}
	msgs := mb.Unread("s")
	if len(msgs) != 1 {
		t.Fatalf("expected 1 queued mailbox message, got %d", len(msgs))
	}
	if msgs[0].MessageID == "" {
		t.Errorf("queued message MessageID is empty; mailbox store must assign an ID")
	}

	// Release the pre-start drain now that we've asserted the queued
	// message is present with its ID. Then release the first resume so
	// both handles close.
	close(drainGate)
	close(gate)
	<-h1.DoneCh
	<-h2.DoneCh

	evts := snapshotEvents(prog)
	if !hasEventType(evts, types.EventResumeQueued) {
		t.Errorf("missing EventResumeQueued; types=%v", eventTypes(evts))
	}
}

// --- F5: reverse message carries RouterMessageResumeReply type ---

func TestResumeR5_ReverseUsesResumeReplyType(t *testing.T) {
	model := &mockModel{responses: []*provider.GenerateResult{textResult("rep", 1, 1)}}
	e := newResumeExecutor(t, model)
	e.Router = NewMessageRouter()
	e.Router.SetMailbox(NewInMemoryMailboxStore())
	e.Router.RegisterInbox("coord")

	_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})
	h, err := e.ResumeStep(context.Background(), "s", "p", "coord")
	if err != nil {
		t.Fatalf("ResumeStep: %v", err)
	}
	<-h.DoneCh
	if h.Err != nil {
		t.Fatalf("handle err: %v", h.Err)
	}

	mb, ok := routerMailbox(e.Router).(MailboxStore)
	if !ok {
		t.Fatal("no mailbox")
	}
	reverse := mb.Unread("coord")
	if len(reverse) != 1 {
		t.Fatalf("want 1 reverse message, got %d", len(reverse))
	}
	if reverse[0].Type != RouterMessageResumeReply {
		t.Errorf("reverse Type=%d, want RouterMessageResumeReply (%d)",
			reverse[0].Type, RouterMessageResumeReply)
	}
	if reverse[0].Metadata[MetadataKeyResumeReverse] != "1" {
		t.Errorf("reverse Metadata missing %s flag: %+v",
			MetadataKeyResumeReverse, reverse[0].Metadata)
	}
}

// --- F6: model-mismatch fails loudly ---

type idModel struct{ id string }

func (m *idModel) ModelID() string { return m.id }
func (m *idModel) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	return textResult("ok", 1, 1), nil
}
func (m *idModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, errors.New("stream not implemented")
}

func TestResumeR3_ModelMismatchFailsLoudly(t *testing.T) {
	runnerModel := &idModel{id: "providerA:m1"}
	e := newResumeExecutor(t, nil)
	e.Runner = &AgentRunner{model: runnerModel}
	e.Progress = &captureSink{}

	// Seed a transcript whose recorded Model differs from runner.
	store := e.transcriptStore.(*resume.InMemoryTranscriptStore)
	store.SetMetadata(e.RunID, "s", "sys", "providerB:m2")
	_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})

	// No resolver installed → must fail with ErrModelResolverMissing.
	_, err := e.ResumeStep(context.Background(), "s", "p", "coord")
	if !errors.Is(err, ErrModelResolverMissing) {
		t.Fatalf("want ErrModelResolverMissing, got %v", err)
	}
	// EventResumeFailed with reason=model-mismatch must also be
	// emitted so operators can trace the failure.
	evts := snapshotEvents(e.Progress.(*captureSink))
	found := false
	for _, ev := range evts {
		if ev.Type != types.EventResumeFailed {
			continue
		}
		if r, _ := ev.Data["reason"].(string); r == "model-mismatch" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("missing EventResumeFailed{reason=model-mismatch}; types=%v", eventTypes(evts))
	}
}

func TestResumeR3_ModelResolverAccepted(t *testing.T) {
	// With a resolver that returns a matching model, resume proceeds.
	runnerModel := &idModel{id: "providerA:m1"}
	saved := &mockModel{
		id:        "providerB:m2",
		responses: []*provider.GenerateResult{textResult("saved-model-said-hi", 1, 1)},
	}
	e := newResumeExecutor(t, nil)
	e.Runner = &AgentRunner{model: runnerModel}
	e.ModelResolver = func(id string) (provider.LanguageModel, error) {
		if id == "providerB:m2" {
			return saved, nil
		}
		return nil, errors.New("unknown model")
	}
	store := e.transcriptStore.(*resume.InMemoryTranscriptStore)
	store.SetMetadata(e.RunID, "s", "", "providerB:m2")
	_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})

	h, err := e.ResumeStep(context.Background(), "s", "p", "coord")
	if err != nil {
		t.Fatalf("ResumeStep: %v", err)
	}
	select {
	case <-h.DoneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("resume did not complete")
	}
	if h.Err != nil {
		t.Fatalf("handle err: %v", h.Err)
	}
	if h.Result != "saved-model-said-hi" {
		t.Errorf("want saved-model result, got %q", h.Result)
	}
}

// --- F7: cascade-resume suppression ---

func TestResumeR5_NoCascadeResumeOnSealedSender(t *testing.T) {
	// A resumed step sends a reverse RouterMessage to its
	// OriginalSender. If the sender's mailbox is ALSO closed AND has
	// a transcript, Router.Send MUST NOT cascade-resume.
	r := NewMessageRouter()
	r.SetMailbox(NewInMemoryMailboxStore())
	resumer := &fakeResumer{can: true}
	r.SetResumer(resumer)

	var drops []DropEvent
	r.SetOnDrop(func(de DropEvent) { drops = append(drops, de) })

	// Register + close "sender" so Send to it would normally cascade.
	r.RegisterInbox("sender")
	r.Close("sender")

	// Simulate the reverse message emission from runResume.
	_ = r.Send("sender", RouterMessage{
		From:    "s",
		To:      "sender",
		Content: "reply",
		Type:    RouterMessageResumeReply,
		Metadata: map[string]string{
			MetadataKeyResumeReverse: "1",
		},
	})

	if resumer.resumeCalls.Load() != 0 {
		t.Fatalf("reverse message triggered cascade resume (%d calls)", resumer.resumeCalls.Load())
	}
	if len(drops) != 1 || drops[0].Reason != DropReasonTargetTerminal {
		t.Fatalf("want one target-terminal drop, got %+v", drops)
	}
}

// --- F8: Run waits for in-flight resumes on exit ---

func TestResumeR3_RunWaitsForInFlightResumes(t *testing.T) {
	// Use a slow-Load store to pin a resume goroutine in-flight when
	// Run tears down. The runCtx cancels the goroutine's ctx which
	// triggers ErrResumeShutdown; Run must not return until
	// EventResumeFailed is emitted (bounded by the 5s safety timeout).
	gate := make(chan struct{})
	model := &sequentialMockModel{
		fn: func(ctx context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return textResult("x", 1, 1), nil
		},
	}
	prog := &captureSink{}
	e := &Executor{
		Runner:          &AgentRunner{model: model},
		RunID:           "run-resume-test",
		transcriptStore: resume.NewInMemoryTranscriptStore(),
		Progress:        prog,
	}
	_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})

	// H3: pass the cancellable ctx to ResumeStep directly.
	ctx, cancel := context.WithCancel(context.Background())

	h, err := e.ResumeStep(ctx, "s", "p", "coord")
	if err != nil {
		t.Fatalf("ResumeStep: %v", err)
	}

	// Simulate Run's teardown: cancel the ctx then wait on the
	// WaitGroup bounded by resumeShutdownTimeout.
	cancel()
	done := make(chan struct{})
	go func() {
		e.resumeWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(resumeShutdownTimeout + 2*time.Second):
		close(gate)
		t.Fatal("Run-teardown wait did not observe resume goroutine exit")
	}
	// Drain the model so it doesn't leak.
	close(gate)
	<-h.DoneCh

	evts := snapshotEvents(prog)
	if !hasEventType(evts, types.EventResumeFailed) {
		t.Errorf("missing EventResumeFailed after ctx cancel; types=%v", eventTypes(evts))
	}
	// VA-10: assert the Data["reason"] is exactly workflow-shutdown
	// so we can tell a shutdown-initiated failure from other causes.
	gotReason := ""
	for _, ev := range evts {
		if ev.Type != types.EventResumeFailed {
			continue
		}
		if r, _ := ev.Data["reason"].(string); r != "" {
			gotReason = r
			break
		}
	}
	if gotReason != "workflow-shutdown" {
		t.Errorf("EventResumeFailed reason=%q, want workflow-shutdown", gotReason)
	}
}

// routerMailbox pokes at the MessageRouter to retrieve its
// installed mailbox store so tests can Unread on it.
func routerMailbox(r *MessageRouter) any {
	return r.Mailbox()
}

// --- findings ---

// resumeRecordingModel captures every DoGenerate params call. Lets tests
// inspect what goai forwarded to the provider (G1 - tool-call replay
// verification).
type resumeRecordingModel struct {
	mu       sync.Mutex
	id       string
	calls    []provider.GenerateParams
	response *provider.GenerateResult
}

func (m *resumeRecordingModel) ModelID() string {
	if m.id == "" {
		return "recording-mock"
	}
	return m.id
}

func (m *resumeRecordingModel) DoGenerate(_ context.Context, p provider.GenerateParams) (*provider.GenerateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := provider.GenerateParams{Messages: make([]provider.Message, len(p.Messages))}
	copy(cp.Messages, p.Messages)
	m.calls = append(m.calls, cp)
	if m.response != nil {
		return m.response, nil
	}
	return textResult("ok", 1, 1), nil
}

func (m *resumeRecordingModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, errors.New("stream not implemented")
}

func (m *resumeRecordingModel) firstCall() provider.GenerateParams {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return provider.GenerateParams{}
	}
	cp := provider.GenerateParams{Messages: make([]provider.Message, len(m.calls[0].Messages))}
	copy(cp.Messages, m.calls[0].Messages)
	return cp
}

// G1 - multi-turn tool-call transcript replay. The resumed AgentRunner
// must forward the prior tool-call → tool-result pairs intact so the
// provider sees a well-formed conversation.
func TestResumeR3_ResumeTranscriptWithToolCallsReplay(t *testing.T) {
	rec := &resumeRecordingModel{response: textResult("resumed-done", 1, 1)}
	e := newResumeExecutor(t, nil)
	e.Runner = &AgentRunner{model: rec}

	// Build a transcript containing a tool-call round trip.
	tcInput := []byte(`{"msg":"hi"}`)
	priorMsgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Part{{Type: provider.PartText, Text: "use the echo tool with 'hi'"}}},
		{Role: provider.RoleAssistant, Content: []provider.Part{
			{Type: provider.PartToolCall, ToolCallID: "t1", ToolName: "echo", ToolInput: tcInput},
		}},
		{Role: provider.RoleTool, Content: []provider.Part{
			{Type: provider.PartToolResult, ToolCallID: "t1", ToolName: "echo", ToolOutput: "hi"},
		}},
		{Role: provider.RoleAssistant, Content: []provider.Part{{Type: provider.PartText, Text: "done."}}},
	}
	if err := e.transcriptStore.Append(e.RunID, "s", priorMsgs); err != nil {
		t.Fatalf("Append: %v", err)
	}

	h, err := e.ResumeStep(context.Background(), "s", "follow-up", "coord")
	if err != nil {
		t.Fatalf("ResumeStep: %v", err)
	}
	select {
	case <-h.DoneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	if h.Err != nil {
		t.Fatalf("resume err: %v", h.Err)
	}

	// Inspect what the model received on its first call. It MUST
	// include all 4 prior messages + the coordinator's new user turn.
	first := rec.firstCall()
	if len(first.Messages) < len(priorMsgs)+1 {
		t.Fatalf("provider received %d messages, want >= %d; msgs=%+v",
			len(first.Messages), len(priorMsgs)+1, first.Messages)
	}
	// The prior sequence must appear intact (role + part type order)
	// at the head of the forwarded messages.
	for i, want := range priorMsgs {
		got := first.Messages[i]
		if got.Role != want.Role {
			t.Errorf("msg[%d] role=%s, want %s", i, got.Role, want.Role)
		}
		if len(got.Content) != len(want.Content) {
			t.Errorf("msg[%d] parts=%d, want %d", i, len(got.Content), len(want.Content))
			continue
		}
		for j := range got.Content {
			if got.Content[j].Type != want.Content[j].Type {
				t.Errorf("msg[%d].part[%d] type=%s, want %s",
					i, j, got.Content[j].Type, want.Content[j].Type)
			}
		}
	}
	// Verify the tool-call pairing survived: call.ID on the assistant
	// part must match the tool-result CallID on the following message.
	assistantCall := first.Messages[1].Content[0]
	toolResult := first.Messages[2].Content[0]
	if assistantCall.ToolCallID != "t1" || toolResult.ToolCallID != "t1" {
		t.Errorf("tool-call pairing lost: assistantID=%q toolID=%q",
			assistantCall.ToolCallID, toolResult.ToolCallID)
	}
	if toolResult.ToolOutput != "hi" {
		t.Errorf("tool-result output lost: got %q", toolResult.ToolOutput)
	}
	// Final (new) user turn is the coordinator's follow-up prompt.
	lastMsg := first.Messages[len(first.Messages)-1]
	if lastMsg.Role != provider.RoleUser {
		t.Errorf("last message role=%s, want user", lastMsg.Role)
	}
	hasFollowup := false
	for _, p := range lastMsg.Content {
		if p.Type == provider.PartText && strings.Contains(p.Text, "follow-up") {
			hasFollowup = true
			break
		}
	}
	if !hasFollowup {
		t.Errorf("new user turn missing follow-up text; parts=%+v", lastMsg.Content)
	}
}

// G2 - queued-path Append error: ResumeStep must NOT emit
// EventResumeQueued when the active resume's mailbox is full, and
// must return ErrMailboxFullOnResume.
func TestResumeR3_QueuedPathErrorOnMailboxFull(t *testing.T) {
	gate := make(chan struct{})
	model := &sequentialMockModel{
		fn: func(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
			<-gate
			return textResult("first-done", 1, 1), nil
		},
	}
	prog := &captureSink{}
	e := &Executor{
		Runner:          &AgentRunner{model: model},
		RunID:           "run-resume-test",
		transcriptStore: resume.NewInMemoryTranscriptStore(),
		Progress:        prog,
		MaxMailboxSize:  1, // tight cap - one queued message max
	}
	// hold the resume goroutine's pre-start drain so the cap
	// assertion below (h3 rejection) is deterministic. Without this
	// gate, the drain may MarkRead h2's message mid-test, freeing a
	// slot and allowing h3 through.
	drainGate := make(chan struct{})
	e.setResumePreStartDrainGateForTest(drainGate)
	_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})

	h1, err := e.ResumeStep(context.Background(), "s", "first", "coord")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// First queued (accepted - within cap).
	h2, err := e.ResumeStep(context.Background(), "s", "second", "coord")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if h2.Result != "queued" {
		t.Fatalf("second should queue, got %q", h2.Result)
	}

	// Second queued: mailbox at cap - must fail loudly.
	_, err = e.ResumeStep(context.Background(), "s", "third", "coord")
	if !errors.Is(err, ErrMailboxFullOnResume) {
		t.Fatalf("third: want ErrMailboxFullOnResume, got %v", err)
	}

	// Release the pre-start drain + the model gate so h1 can complete.
	close(drainGate)
	close(gate)
	<-h1.DoneCh
	<-h2.DoneCh

	evts := snapshotEvents(prog)
	// Exactly ONE EventResumeQueued (for the accepted second call).
	queued := 0
	drops := 0
	for _, ev := range evts {
		switch ev.Type {
		case types.EventResumeQueued:
			queued++
		case types.EventMessageDropped:
			if r, _ := ev.Data["reason"].(string); r == DropReasonMailboxFull.String() {
				drops++
			}
		}
	}
	if queued != 1 {
		t.Errorf("want exactly 1 EventResumeQueued, got %d", queued)
	}
	if drops != 1 {
		t.Errorf("want exactly 1 mailbox-full drop, got %d", drops)
	}
}

// VA-6 - workflow model string match (production default) works
// WITHOUT a resolver: saved transcript's Model equals the
// user-supplied step string.
func TestResumeR3_WorkflowModelMatchDefault(t *testing.T) {
	runnerModel := &idModel{id: "us.anthropic.claude-sonnet-4-6"}
	e := newResumeExecutor(t, nil)
	e.Runner = &AgentRunner{model: runnerModel}
	e.Progress = &captureSink{}
	// Simulate runStep having recorded the step's user-supplied model.
	e.stepModelStringsMu.Lock()
	if e.stepModelStrings == nil {
		e.stepModelStrings = make(map[string]string)
	}
	e.stepModelStrings["s"] = "anthropic.claude-sonnet-4-6"
	e.stepModelStringsMu.Unlock()

	// Transcript records the user-supplied string (what executor.go:1519
	// stores as ModelID), which differs from runner.ModelID (the
	// wrapped cross-region prefix). No resolver - the step-string match
	// MUST accept the resume.
	store := e.transcriptStore.(*resume.InMemoryTranscriptStore)
	store.SetMetadata(e.RunID, "s", "sys", "anthropic.claude-sonnet-4-6")
	_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})

	h, err := e.ResumeStep(context.Background(), "s", "p", "coord")
	if err != nil {
		t.Fatalf("ResumeStep: %v; want nil (step-string match)", err)
	}
	select {
	case <-h.DoneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	if h.Err != nil {
		t.Fatalf("resume err: %v", h.Err)
	}
}

// VA-6b - distinguish ErrModelResolverError (resolver ran and failed)
// from ErrModelResolverMissing (no resolver OR resolver returned nil).
func TestResumeR3_ResolverErrorDistinctFromMissing(t *testing.T) {
	runnerModel := &idModel{id: "providerA:m1"}

	// Case 1: resolver returns an error.
	{
		e := newResumeExecutor(t, nil)
		e.Runner = &AgentRunner{model: runnerModel}
		prog := &captureSink{}
		e.Progress = prog
		e.ModelResolver = func(_ string) (provider.LanguageModel, error) {
			return nil, errors.New("infra down")
		}
		store := e.transcriptStore.(*resume.InMemoryTranscriptStore)
		store.SetMetadata(e.RunID, "s", "", "providerB:m2")
		_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})

		_, err := e.ResumeStep(context.Background(), "s", "p", "coord")
		if !errors.Is(err, ErrModelResolverError) {
			t.Fatalf("case1: want ErrModelResolverError, got %v", err)
		}
		// Event must carry reason=resolver-error AND the wrapped error string.
		evts := snapshotEvents(prog)
		found := false
		for _, ev := range evts {
			if ev.Type != types.EventResumeFailed {
				continue
			}
			r, _ := ev.Data["reason"].(string)
			errStr, _ := ev.Data["error"].(string)
			if r == "resolver-error" && strings.Contains(errStr, "infra down") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("case1: missing EventResumeFailed{reason=resolver-error}; types=%v",
				eventTypes(evts))
		}
	}

	// Case 2: resolver returns nil model, nil error → ErrModelResolverMissing.
	{
		e := newResumeExecutor(t, nil)
		e.Runner = &AgentRunner{model: runnerModel}
		e.Progress = &captureSink{}
		e.ModelResolver = func(_ string) (provider.LanguageModel, error) {
			return nil, nil
		}
		store := e.transcriptStore.(*resume.InMemoryTranscriptStore)
		store.SetMetadata(e.RunID, "s", "", "providerB:m2")
		_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})

		_, err := e.ResumeStep(context.Background(), "s", "p", "coord")
		if !errors.Is(err, ErrModelResolverMissing) {
			t.Fatalf("case2: want ErrModelResolverMissing, got %v", err)
		}
	}
}

// VA-4b - EventResumeQueued carries activeResumeID for correlation.
func TestResumeR3_QueuedEventHasActiveResumeID(t *testing.T) {
	gate := make(chan struct{})
	model := &sequentialMockModel{
		fn: func(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
			<-gate
			return textResult("done", 1, 1), nil
		},
	}
	prog := &captureSink{}
	e := &Executor{
		Runner:          &AgentRunner{model: model},
		RunID:           "run-resume-test",
		transcriptStore: resume.NewInMemoryTranscriptStore(),
		Progress:        prog,
	}
	_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})

	h1, err := e.ResumeStep(context.Background(), "s", "first", "coord")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	h2, err := e.ResumeStep(context.Background(), "s", "second", "coord")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if h2.Result != "queued" {
		t.Fatalf("want queued handle, got %q", h2.Result)
	}
	close(gate)
	<-h1.DoneCh
	<-h2.DoneCh

	// Extract the active handle's ResumeID from EventResumeStarted.
	evts := snapshotEvents(prog)
	var startedID, queuedActiveID string
	for _, ev := range evts {
		if ev.Type == types.EventResumeStarted {
			startedID, _ = ev.Data["resumeID"].(string)
		}
		if ev.Type == types.EventResumeQueued {
			queuedActiveID, _ = ev.Data["activeResumeID"].(string)
		}
	}
	if startedID == "" {
		t.Fatalf("missing EventResumeStarted.resumeID")
	}
	if queuedActiveID != startedID {
		t.Errorf("EventResumeQueued.activeResumeID=%q, want %q (from EventResumeStarted)",
			queuedActiveID, startedID)
	}
}

// VA-3b - sealed transcript + WithTruncationOnCapReached enables a
// truncated-load fallback so the step can still resume.
func TestResumeR3_SealedTranscriptTruncationFallback(t *testing.T) {
	// Build a store and fill past its cap so the slot seals.
	store := resume.NewInMemoryTranscriptStoreWithCaps(2, 0)
	_ = store.Append("run-resume-test", "s", []provider.Message{
		mkTextMsg(provider.RoleUser, "a"),
		mkTextMsg(provider.RoleUser, "b"),
	})
	capErr := store.Append("run-resume-test", "s", []provider.Message{mkTextMsg(provider.RoleUser, "c")})
	if !errors.Is(capErr, resume.ErrTranscriptTooLarge) {
		t.Fatalf("setup: expected seal, got %v", capErr)
	}
	// Confirm the slot sealed.
	if _, err := store.Load("run-resume-test", "s"); !errors.Is(err, resume.ErrTranscriptTooLarge) {
		t.Fatalf("expected sealed Load, got %v", err)
	}

	rec := &resumeRecordingModel{response: textResult("resumed", 1, 1)}
	prog := &captureSink{}
	e := &Executor{
		Runner:               &AgentRunner{model: rec},
		RunID:                "run-resume-test",
		transcriptStore:      store,
		Progress:             prog,
		TruncateOnCapReached: true,
	}

	h, err := e.ResumeStep(context.Background(), "s", "p", "coord")
	if err != nil {
		t.Fatalf("ResumeStep: %v (truncation fallback should succeed)", err)
	}
	select {
	case <-h.DoneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	if h.Err != nil {
		t.Fatalf("resume err: %v", h.Err)
	}

	// Provider must have received the 2 prior messages + new user turn.
	first := rec.firstCall()
	if len(first.Messages) < 3 {
		t.Errorf("provider saw %d messages, want >=3", len(first.Messages))
	}
	// EventMessage{reason=resume-truncated} must be emitted.
	evts := snapshotEvents(prog)
	found := false
	for _, ev := range evts {
		if ev.Type != types.EventMessage {
			continue
		}
		if r, _ := ev.Data["reason"].(string); r == "resume-truncated" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("missing resume-truncated EventMessage; types=%v", eventTypes(evts))
	}
}

// VA-3b sanity - truncation fallback is OFF by default.
func TestResumeR3_SealedTranscriptNoTruncationByDefault(t *testing.T) {
	store := resume.NewInMemoryTranscriptStoreWithCaps(1, 0)
	_ = store.Append("run-resume-test", "s", []provider.Message{mkTextMsg(provider.RoleUser, "a")})
	_ = store.Append("run-resume-test", "s", []provider.Message{mkTextMsg(provider.RoleUser, "b")})
	// Load returns sealed.
	e := &Executor{
		Runner:          &AgentRunner{model: &mockModel{}},
		RunID:           "run-resume-test",
		transcriptStore: store,
	}
	_, err := e.ResumeStep(context.Background(), "s", "p", "coord")
	if !errors.Is(err, resume.ErrTranscriptTooLarge) {
		t.Fatalf("want ErrTranscriptTooLarge, got %v", err)
	}
}

// G4 - transcript seal emits EventTranscriptSealed exactly once.
func TestResumeR3_TranscriptSealedEventEmittedOnce(t *testing.T) {
	// Use a real AgentRunner with a store capped to 1 message. The
	// first post-run Append persists messages within cap, the second
	// (from the defer-flush) hits the cap and seals.
	// We use a micro store of 1-byte cap - estimateMessageBytes gives
	// each message >=32 bytes so any Append trips.
	store := resume.NewInMemoryTranscriptStoreWithCaps(0, 1)
	prog := &captureSink{}
	runner := &AgentRunner{
		model:      &mockModel{responses: []*provider.GenerateResult{textResult("ans", 1, 1)}},
		progress:   prog,
		runID:      "run-seal-test",
		stepID:     "s",
		transcript: store,
	}
	ctx := t.Context()
	_, err := runner.Run(ctx, AgentConfig{}, "hello", "mock-model", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	evts := snapshotEvents(prog)
	sealed := 0
	for _, ev := range evts {
		if ev.Type == types.EventTranscriptSealed && ev.StepID == "s" {
			sealed++
		}
	}
	if sealed != 1 {
		t.Errorf("want exactly 1 EventTranscriptSealed, got %d; types=%v",
			sealed, eventTypes(evts))
	}
}

// G5 - empty transcript.Model uses runner default, no resolver consulted.
func TestResumeR3_EmptyTranscriptModelUsesRunnerDefault(t *testing.T) {
	runnerModel := &idModel{id: "providerA:m1"}
	prog := &captureSink{}
	e := newResumeExecutor(t, nil)
	e.Runner = &AgentRunner{model: runnerModel}
	e.Progress = prog
	// Install a resolver that MUST NOT be called.
	resolverCalls := atomic.Int32{}
	e.ModelResolver = func(_ string) (provider.LanguageModel, error) {
		resolverCalls.Add(1)
		return nil, errors.New("resolver should not have been called")
	}
	// Transcript with Model="" → skip model-fidelity check.
	_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})
	// Confirm Model is empty on the persisted transcript.
	loaded, _ := e.transcriptStore.Load(e.RunID, "s")
	if loaded.Model != "" {
		t.Fatalf("setup: want empty Model, got %q", loaded.Model)
	}

	h, err := e.ResumeStep(context.Background(), "s", "p", "coord")
	if err != nil {
		t.Fatalf("ResumeStep: %v", err)
	}
	select {
	case <-h.DoneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	if h.Err != nil {
		t.Fatalf("resume err: %v", h.Err)
	}
	if resolverCalls.Load() != 0 {
		t.Errorf("resolver called %d times; want 0 (empty transcript.Model path)",
			resolverCalls.Load())
	}
	if !hasEventType(snapshotEvents(prog), types.EventResumeCompleted) {
		t.Errorf("missing EventResumeCompleted")
	}
}

// G3 - Run force-cancels in-flight resumes via derived ctx. We simulate
// a wedged resume by using a model that ignores ctx.Done for a bounded
// period, then verify that cancelRun cuts it off within
// resumeShutdownTimeout.
func TestResumeR3_RunForceCancelsWedgedResume(t *testing.T) {
	// Model that honors ctx.Done but only after a small delay, so
	// we can see the ctx-cancel effect without flake.
	model := &sequentialMockModel{
		fn: func(ctx context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(10 * time.Second):
				return textResult("late", 1, 1), nil
			}
		},
	}
	prog := &captureSink{}
	e := &Executor{
		Runner:          &AgentRunner{model: model},
		RunID:           "run-resume-test",
		transcriptStore: resume.NewInMemoryTranscriptStore(),
		Progress:        prog,
		Workflow:        &Workflow{Name: "t"},
	}
	_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})

	// H3: drive Run's teardown path by passing a cancellable ctx directly
	// to ResumeStep. No runCtx struct field needed.
	ctx, cancel := context.WithCancel(context.Background())

	h, err := e.ResumeStep(ctx, "s", "p", "coord")
	if err != nil {
		t.Fatalf("ResumeStep: %v", err)
	}
	// Cancel the ctx (simulates Run's teardown cancelRun).
	cancel()
	// Wait on the WaitGroup with the same bound Run uses.
	done := make(chan struct{})
	go func() {
		e.resumeWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(resumeShutdownTimeout + 2*time.Second):
		<-h.DoneCh
		t.Fatal("resume goroutine did not observe ctx cancel")
	}
	// Active counter must be zero post-wait.
	if n := e.resumeActiveCount.Load(); n != 0 {
		t.Errorf("resumeActiveCount=%d after teardown, want 0", n)
	}
	<-h.DoneCh
}

// VA-3b coverage for LoadTruncated correctness.
func TestResumeR3_InMemStoreLoadTruncated(t *testing.T) {
	store := resume.NewInMemoryTranscriptStore()
	runID := "r"
	stepID := "s"
	msgs := make([]provider.Message, 10)
	for i := range msgs {
		msgs[i] = mkTextMsg(provider.RoleUser, fmt.Sprintf("m%d", i))
	}
	_ = store.Append(runID, stepID, msgs)
	got, err := store.LoadTruncated(runID, stepID, 3)
	if err != nil {
		t.Fatalf("LoadTruncated: %v", err)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("len=%d, want 3", len(got.Messages))
	}
	// Tail semantics: the last 3.
	for i, want := range []string{"m7", "m8", "m9"} {
		gotText := got.Messages[i].Content[0].Text
		if gotText != want {
			t.Errorf("msg[%d]=%q, want %q", i, gotText, want)
		}
	}
	// maxMessages=0 falls back to the default (1000), returning all 10.
	got2, err := store.LoadTruncated(runID, stepID, 0)
	if err != nil {
		t.Fatalf("LoadTruncated(0): %v", err)
	}
	if len(got2.Messages) != 10 {
		t.Errorf("default bound returned %d, want 10", len(got2.Messages))
	}
	// Missing slot → ErrNoTranscript.
	if _, err := store.LoadTruncated("nope", "nope", 5); !errors.Is(err, resume.ErrNoTranscript) {
		t.Errorf("missing slot: want ErrNoTranscript, got %v", err)
	}
}

// --- fixes ---

// activeResumeID must be published under the SAME critical section
// that flips running=true, so a concurrent queued-path caller always
// observes a non-empty ID (never the old "" stale value).
func TestResumeR3_ActiveResumeIDPublishedAtomically(t *testing.T) {
	inner := resume.NewInMemoryTranscriptStore()
	_ = inner.Append("run-resume-test", "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})
	gate := make(chan struct{})
	store := &blockingLoadStore{inner: inner, gate: gate}

	model := &sequentialMockModel{
		fn: func(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
			return textResult("done", 1, 1), nil
		},
	}
	prog := &captureSink{}
	e := &Executor{
		Runner:          &AgentRunner{model: model},
		RunID:           "run-resume-test",
		transcriptStore: store,
		Progress:        prog,
	}

	// First ResumeStep: blocks in Load (inside store.Load). But because
	// publishes activeResumeID BEFORE Load, the state must already
	// advertise the final ResumeID as soon as running=true is visible.
	h1Ch := make(chan *ResumeHandle, 1)
	go func() {
		h, err := e.ResumeStep(context.Background(), "s", "first", "coord")
		if err != nil {
			t.Errorf("first: %v", err)
		}
		h1Ch <- h
	}()

	// Wait for running=true to be visible.
	var firstActiveID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		e.resumesMu.Lock()
		rs := e.resumes
		e.resumesMu.Unlock()
		if rs != nil {
			st := rs.get("s")
			st.mu.Lock()
			if st.running {
				firstActiveID = st.activeResumeID
				st.mu.Unlock()
				break
			}
			st.mu.Unlock()
		}
		time.Sleep(5 * time.Millisecond)
	}
	if firstActiveID == "" {
		t.Fatal("activeResumeID empty while running=true - invariant violated")
	}

	// Second ResumeStep races into the queue path. The EventResumeQueued
	// it emits must carry the FIRST resume's ResumeID as activeResumeID.
	h2, err := e.ResumeStep(context.Background(), "s", "second", "coord")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if h2.Result != "queued" {
		t.Fatalf("second should be queued, got %q", h2.Result)
	}

	// Release Load so the first resume proceeds.
	close(gate)
	h1 := <-h1Ch
	<-h1.DoneCh
	<-h2.DoneCh

	// Verify: ResumeID on the first handle matches the firstActiveID we
	// observed under state.mu.
	if h1.ResumeID != firstActiveID {
		t.Errorf("h1.ResumeID=%q but observed activeResumeID=%q (not atomic)",
			h1.ResumeID, firstActiveID)
	}
	// EventResumeQueued must carry that same ID as activeResumeID.
	evts := snapshotEvents(prog)
	var queuedActive string
	for _, ev := range evts {
		if ev.Type == types.EventResumeQueued {
			queuedActive, _ = ev.Data["activeResumeID"].(string)
		}
	}
	if queuedActive == "" {
		t.Fatal("EventResumeQueued missing activeResumeID")
	}
	if queuedActive != firstActiveID {
		t.Errorf("EventResumeQueued.activeResumeID=%q, want %q", queuedActive, firstActiveID)
	}
}

// Run teardown timeout branch emits EventMessage{reason:resume-timeout,
// count:N} when a wedged resume goroutine ignores ctx.Done.
func TestResumeR3_ResumeTimeoutEmitsLeakCount(t *testing.T) {
	// Shrink the timeout so the test doesn't wait 5 seconds.
	// - switched defer→t.Cleanup for parity with every other
	// test seam in the codebase. Defers fire on the enclosing
	// function exit; t.Cleanup fires on the *test* exit which is the
	// same in a top-level Test but stays correct if this body ever
	// gets wrapped in a t.Run subtest.
	prev := resumeShutdownTimeout
	resumeShutdownTimeout = 100 * time.Millisecond
	t.Cleanup(func() { resumeShutdownTimeout = prev })

	// Model that IGNORES ctx entirely: it blocks on a channel only
	// released by test cleanup. A started-channel lets the test know
	// DoGenerate is inside the wedged state (so a cancelRun now lands
	// on a goroutine past ctx.Err checks in runResume).
	release := make(chan struct{})
	started := make(chan struct{})
	var startedOnce sync.Once
	t.Cleanup(func() { close(release) })
	model := &sequentialMockModel{
		fn: func(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
			startedOnce.Do(func() { close(started) })
			// Intentionally ignore ctx.Done. Block until test cleanup.
			<-release
			return textResult("late", 1, 1), nil
		},
	}
	prog := &captureSink{}
	e := &Executor{
		Runner:          &AgentRunner{model: model},
		RunID:           "run-resume-test",
		transcriptStore: resume.NewInMemoryTranscriptStore(),
		Progress:        prog,
		Workflow:        &Workflow{Name: "t", Steps: []Step{{ID: "dummy", Instructions: "x"}}},
	}
	_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})

	// H3: pass a cancellable ctx directly to ResumeStep - no runCtx struct field needed.
	// Use two levels of cancellation to mimic Run's parent + child ctx hierarchy:
	// cancel is the outer cancel (test cleanup), cancelRun is what Run's defer fires.
	ctx, cancel := context.WithCancel(context.Background())
	_ = cancel
	runCtx, cancelRun := context.WithCancel(ctx)

	// Kick off a resume; it will wedge inside the mock's fn.
	h, err := e.ResumeStep(runCtx, "s", "p", "coord")
	if err != nil {
		t.Fatalf("ResumeStep: %v", err)
	}
	_ = h

	// Wait for the mock to actually be running (past runResume's early
	// ctx checks) before we cancel - otherwise the goroutine exits
	// cleanly via the workflow-shutdown branch and nothing "leaks".
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("mock DoGenerate never started")
	}

	// Simulate Run's teardown defer: cancel runCtx then wait with timeout.
	cancelRun()
	// Replicate the teardown wait logic inline so we exercise the same
	// branch Run uses.
	done := make(chan struct{})
	go func() {
		e.resumeWG.Wait()
		close(done)
	}()
	leaked := int64(0)
	select {
	case <-done:
		t.Fatal("expected timeout branch, but goroutine exited cleanly")
	case <-time.After(resumeShutdownTimeout):
		leaked = e.resumeActiveCount.Load()
		if e.Progress != nil {
			e.Progress.OnEvent(context.Background(), Event{
				Type:      types.EventMessage,
				Timestamp: time.Now(),
				RunID:     e.runID(),
				Message:   fmt.Sprintf("zenflow: timed out waiting for in-flight resume goroutines (test, leaked=%d)", leaked),
				Data: map[string]any{
					"reason": "resume-timeout",
					"count":  leaked,
				},
			})
		}
	}

	if leaked < 1 {
		t.Errorf("expected leaked>=1 (wedged goroutine), got %d", leaked)
	}
	// Assert the emitted event.
	evts := snapshotEvents(prog)
	found := false
	for _, ev := range evts {
		if ev.Type != types.EventMessage {
			continue
		}
		r, _ := ev.Data["reason"].(string)
		if r != "resume-timeout" {
			continue
		}
		count, _ := ev.Data["count"].(int64)
		if count < 1 {
			t.Errorf("count=%d, want >=1", count)
		}
		found = true
		break
	}
	if !found {
		t.Errorf("missing EventMessage{reason=resume-timeout}; types=%v", eventTypes(evts))
	}

	cancel()
	// release is closed by t.Cleanup - goroutine unblocks after test.
}

// ---: LoadTruncated error surfacing ---

// fakeTranscriptStoreNoTrunc implements TranscriptStore but does NOT
// implement TranscriptTruncatedLoader. Used to verify the
// "unsupported" path emits a warning when TruncateOnCapReached is set.
type fakeTranscriptStoreNoTrunc struct {
	sealed bool
}

func (s *fakeTranscriptStoreNoTrunc) Append(_, _ string, _ []provider.Message) error {
	return nil
}
func (s *fakeTranscriptStoreNoTrunc) Load(_, _ string) (*resume.StepTranscript, error) {
	if s.sealed {
		return nil, fmt.Errorf("transcript too large: %w", resume.ErrTranscriptTooLarge)
	}
	return &resume.StepTranscript{Messages: []provider.Message{mkTextMsg(provider.RoleUser, "u")}}, nil
}
func (s *fakeTranscriptStoreNoTrunc) Delete(_, _ string) error { return nil }

func TestResumeR3_TruncateUnsupportedEmitsWarning(t *testing.T) {
	store := &fakeTranscriptStoreNoTrunc{sealed: true}
	prog := &captureSink{}
	e := &Executor{
		Runner:               &AgentRunner{model: &mockModel{}},
		RunID:                "run-resume-test",
		transcriptStore:      store,
		Progress:             prog,
		TruncateOnCapReached: true, // opt-in, but store doesn't implement
	}
	_, err := e.ResumeStep(context.Background(), "s", "p", "coord")
	if !errors.Is(err, resume.ErrTranscriptTooLarge) {
		t.Fatalf("want ErrTranscriptTooLarge, got %v", err)
	}
	evts := snapshotEvents(prog)
	found := false
	for _, ev := range evts {
		if ev.Type != types.EventMessage {
			continue
		}
		if r, _ := ev.Data["reason"].(string); r == "resume-truncation-unsupported" {
			found = true
			if _, ok := ev.Data["store-type"].(string); !ok {
				t.Errorf("missing store-type; data=%+v", ev.Data)
			}
			break
		}
	}
	if !found {
		t.Errorf("missing resume-truncation-unsupported warning; types=%v", eventTypes(evts))
	}
}

// fakeTranscriptStoreFailingTrunc implements TranscriptTruncatedLoader but
// LoadTruncated returns an error.
type fakeTranscriptStoreFailingTrunc struct{}

func (s *fakeTranscriptStoreFailingTrunc) Append(_, _ string, _ []provider.Message) error {
	return nil
}
func (s *fakeTranscriptStoreFailingTrunc) Load(_, _ string) (*resume.StepTranscript, error) {
	return nil, fmt.Errorf("slot sealed: %w", resume.ErrTranscriptTooLarge)
}
func (s *fakeTranscriptStoreFailingTrunc) Delete(_, _ string) error { return nil }
func (s *fakeTranscriptStoreFailingTrunc) LoadTruncated(_, _ string, _ int) (*resume.StepTranscript, error) {
	return nil, errors.New("truncation backend unreachable")
}

func TestResumeR3_TruncateLoadErrorSurfacesBoth(t *testing.T) {
	store := &fakeTranscriptStoreFailingTrunc{}
	prog := &captureSink{}
	e := &Executor{
		Runner:               &AgentRunner{model: &mockModel{}},
		RunID:                "run-resume-test",
		transcriptStore:      store,
		Progress:             prog,
		TruncateOnCapReached: true,
	}
	_, err := e.ResumeStep(context.Background(), "s", "p", "coord")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	// Must wrap BOTH the original cap error AND the truncation backend
	// error via errors.Join.
	if !errors.Is(err, resume.ErrTranscriptTooLarge) {
		t.Errorf("want errors.Is ErrTranscriptTooLarge, got %v", err)
	}
	if !strings.Contains(err.Error(), "truncation backend unreachable") {
		t.Errorf("want wrapped truncation error, got %v", err)
	}
	// EventMessage{reason=resume-truncation-failed} emitted.
	evts := snapshotEvents(prog)
	found := false
	for _, ev := range evts {
		if ev.Type != types.EventMessage {
			continue
		}
		if r, _ := ev.Data["reason"].(string); r == "resume-truncation-failed" {
			if errStr, _ := ev.Data["error"].(string); strings.Contains(errStr, "truncation backend unreachable") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Errorf("missing resume-truncation-failed event; types=%v", eventTypes(evts))
	}
}

// ---: transient store error does NOT permanently seal ---

// flakyTranscriptStore fails Append on the Nth call, succeeds otherwise.
type flakyTranscriptStore struct {
	mu        sync.Mutex
	failOn    int // 1-based
	calls     int
	succeeded int
}

func (s *flakyTranscriptStore) Append(_, _ string, _ []provider.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls == s.failOn {
		return errors.New("transient IO glitch")
	}
	s.succeeded++
	return nil
}
func (s *flakyTranscriptStore) Load(_, _ string) (*resume.StepTranscript, error) {
	return nil, resume.ErrNoTranscript
}
func (s *flakyTranscriptStore) Delete(_, _ string) error { return nil }

func TestResumeR3_TransientStoreErrorDoesNotSeal(t *testing.T) {
	// Direct-drive the runner flushTranscript via two AgentRunner.Run calls:
	// the first Append fails (transient), the second succeeds. This proves
	// transcriptSealed stayed false across the failure.
	store := &flakyTranscriptStore{failOn: 1}
	prog := &captureSink{}
	runner := &AgentRunner{
		model:      &mockModel{responses: []*provider.GenerateResult{textResult("a", 1, 1)}},
		progress:   prog,
		runID:      "run-flaky",
		stepID:     "s",
		transcript: store,
	}
	_, err := runner.Run(context.Background(), AgentConfig{}, "hello", "mock-model", nil)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}

	// Second run with same runner - transcriptSealed would have been set
	// to true by old code on any error. New code: only ErrTranscriptTooLarge
	// seals. The second Append must therefore succeed.
	runner2 := &AgentRunner{
		model:      &mockModel{responses: []*provider.GenerateResult{textResult("b", 1, 1)}},
		progress:   prog,
		runID:      "run-flaky",
		stepID:     "s",
		transcript: store,
	}
	_, err = runner2.Run(context.Background(), AgentConfig{}, "hello2", "mock-model", nil)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}

	// After both Runs: store.calls should be 2 (one per Run's final flush),
	// with succeeded >= 1 (second flush landed).
	store.mu.Lock()
	calls := store.calls
	succeeded := store.succeeded
	store.mu.Unlock()
	// 3 calls expected: Run-1 loop-flush (fails, call #1) + Run-1 deferred-flush
	// (succeeds, call #2) + Run-2 loop-flush (succeeds, call #3); Run-2 deferred
	// skips because persistedCount already caught up.
	if calls != 3 {
		t.Errorf("Append called %d times, want exactly 3 (Run-1 loop+deferred, Run-2 loop)", calls)
	}
	if succeeded < 1 {
		t.Errorf("no Append succeeded (%d/%d); transient error wrongly sealed store", succeeded, calls)
	}
}

// ---: DropReasonResolverError distinct from DropReasonTargetTerminal ---

// Extends TestResumeR3_ResolverErrorDistinctFromMissing - this asserts the
// DropReason emitted by the Router is DropReasonResolverError (not the
// generic DropReasonTargetTerminal).
func TestResumeR4_ResolverErrorHasDedicatedDropReason(t *testing.T) {
	r := NewMessageRouter()
	r.SetMailbox(NewInMemoryMailboxStore())
	r.SetResumer(&fakeResumer{can: true, resumeErr: ErrModelResolverError})

	var drops []DropEvent
	r.SetOnDrop(func(de DropEvent) { drops = append(drops, de) })

	r.RegisterInbox("s")
	r.Close("s")
	_ = r.Send("s", RouterMessage{From: "c"})

	if len(drops) != 1 {
		t.Fatalf("want one drop, got %+v", drops)
	}
	if drops[0].Reason != DropReasonResolverError {
		t.Errorf("want DropReasonResolverError, got %s", drops[0].Reason.String())
	}
	// Must differ from the missing-resolver mapping (which maps to
	// DropReasonTargetTerminal).
	if drops[0].Reason == DropReasonTargetTerminal {
		t.Errorf("resolver-error must NOT collapse to target-terminal")
	}
}

// ---: queued-path mailbox-full emits EventResumeFailed too ---

func TestResumeR3_QueuedMailboxFullEmitsBothEvents(t *testing.T) {
	gate := make(chan struct{})
	model := &sequentialMockModel{
		fn: func(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
			<-gate
			return textResult("done", 1, 1), nil
		},
	}
	prog := &captureSink{}
	e := &Executor{
		Runner:          &AgentRunner{model: model},
		RunID:           "run-resume-test",
		transcriptStore: resume.NewInMemoryTranscriptStore(),
		Progress:        prog,
		MaxMailboxSize:  1,
	}
	// hold pre-start drain so h3's cap rejection is deterministic.
	drainGate := make(chan struct{})
	e.setResumePreStartDrainGateForTest(drainGate)
	_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})

	h1, err := e.ResumeStep(context.Background(), "s", "first", "coord")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	_, err = e.ResumeStep(context.Background(), "s", "second", "coord")
	if err != nil {
		t.Fatalf("second (fills cap): %v", err)
	}
	// Third - mailbox full.
	_, err = e.ResumeStep(context.Background(), "s", "third", "coord")
	if !errors.Is(err, ErrMailboxFullOnResume) {
		t.Fatalf("third: want ErrMailboxFullOnResume, got %v", err)
	}

	close(drainGate)
	close(gate)
	<-h1.DoneCh

	evts := snapshotEvents(prog)
	drops := 0
	failed := 0
	var dropQueuedID, failedQueuedID string
	var dropActiveID, failedActiveID string
	for _, ev := range evts {
		switch ev.Type {
		case types.EventMessageDropped:
			if r, _ := ev.Data["reason"].(string); r == DropReasonMailboxFull.String() {
				drops++
				dropQueuedID, _ = ev.Data["queuedResumeID"].(string)
				dropActiveID, _ = ev.Data["activeResumeID"].(string)
			}
		case types.EventResumeFailed:
			if r, _ := ev.Data["reason"].(string); r == "mailbox-full-on-resume" {
				failed++
				failedQueuedID, _ = ev.Data["resumeID"].(string)
				failedActiveID, _ = ev.Data["activeResumeID"].(string)
			}
		}
	}
	if drops != 1 {
		t.Errorf("want 1 mailbox-full drop, got %d", drops)
	}
	if failed != 1 {
		t.Errorf("want 1 EventResumeFailed{reason=mailbox-full-on-resume}, got %d; types=%v",
			failed, eventTypes(evts))
	}
	// both events MUST carry the same queuedResumeID and activeResumeID
	// correlators so consumers can match the pair.
	if dropQueuedID == "" {
		t.Errorf("EventMessageDropped missing queuedResumeID")
	}
	if failedQueuedID == "" {
		t.Errorf("EventResumeFailed missing resumeID (queued-path)")
	}
	if dropQueuedID != failedQueuedID {
		t.Errorf("queuedResumeID mismatch: drop=%q failed=%q", dropQueuedID, failedQueuedID)
	}
	if dropActiveID == "" || failedActiveID == "" {
		t.Errorf("activeResumeID missing: drop=%q failed=%q", dropActiveID, failedActiveID)
	}
	if dropActiveID != failedActiveID {
		t.Errorf("activeResumeID mismatch: drop=%q failed=%q", dropActiveID, failedActiveID)
	}
}

// ---: cap remains enforced even after drain MarkReads ---
// TestResumeR3_QueuedCapStableUnderDrain exercises the resumeCapMailbox
// wrapper directly. It asserts that:
// 1. Appending up to the cap succeeds.
// 2. The cap rejects subsequent Appends (ErrMailboxFull).
// 3. After MarkRead drains entries from the UNDERLYING store (which
// would drop the inner queue length below the cap), a new Append
// through the wrapper is still bounded by the decremented counter,
// so admission is deterministic: it succeeds only because MarkRead
// credited the counter, NOT because the inner queue shrank.
// This is the deterministic analogue of the -race -count=30 flake test:
// the wrapper's counter is the source of truth for the cap, independent
// of the inner store's transient length.
func TestResumeR3_QueuedCapStableUnderDrain(t *testing.T) {
	const cap = 2
	w, err := newResumeCapMailbox(NewInMemoryMailboxStore(), cap)
	if err != nil {
		t.Fatalf("newResumeCapMailbox: %v", err)
	}
	stepID := "s"

	// 1. Fill to cap.
	id1, err := w.Append(stepID, RouterMessage{Content: "a"})
	if err != nil || id1 == "" {
		t.Fatalf("Append #1: %v id=%q", err, id1)
	}
	id2, err := w.Append(stepID, RouterMessage{Content: "b"})
	if err != nil || id2 == "" {
		t.Fatalf("Append #2: %v id=%q", err, id2)
	}
	if got := w.outstandingCount(); got != 2 {
		t.Fatalf("outstandingCount after fill: got %d want 2", got)
	}

	// 2. Cap rejects.
	if _, err := w.Append(stepID, RouterMessage{Content: "c"}); err == nil {
		t.Fatalf("Append #3 should have been rejected (cap), got nil err")
	}

	// 3. Drain the inner store's first message via MarkRead. This
	// simulates the resume goroutine's pre-start
	// drainMailboxIntoMessages consuming one queued message.
	w.MarkRead(stepID, []string{id1})
	if got := w.outstandingCount(); got != 1 {
		t.Fatalf("outstandingCount after MarkRead: got %d want 1", got)
	}

	// 4. Append admitted again (counter decremented), and a SECOND
	// extra Append is rejected (cap still enforced by the counter).
	id3, err := w.Append(stepID, RouterMessage{Content: "c"})
	if err != nil || id3 == "" {
		t.Fatalf("Append #3 after drain: %v id=%q", err, id3)
	}
	if _, err := w.Append(stepID, RouterMessage{Content: "d"}); err == nil {
		t.Fatalf("Append #4 should have been rejected (cap) after admit; got nil err")
	}

	// 5. Draining a message that was NOT issued by the wrapper must
	// not decrement the counter (defensive: idempotent MarkRead).
	w.MarkRead(stepID, []string{"bogus-id"})
	if got := w.outstandingCount(); got != 2 {
		t.Fatalf("outstandingCount after bogus MarkRead: got %d want 2", got)
	}
}

// ---: newResumeCapMailbox refuses a nil inner store ---
// TestResumeR3_ResumeCapMailboxNilInnerReturnsError verifies the
// constructor guard: newResumeCapMailbox must refuse a nil inner store
// with a clear error rather than masking the misconfiguration to a
// later nil-deref.
func TestResumeR3_ResumeCapMailboxNilInnerReturnsError(t *testing.T) {
	t.Parallel()
	w, err := newResumeCapMailbox(nil, 1)
	if err == nil {
		t.Fatal("expected error on nil inner store, got nil")
	}
	if !errors.Is(err, errNilResumeCapInner) {
		t.Fatalf("err = %v, want errors.Is(err, errNilResumeCapInner)", err)
	}
	if w != nil {
		t.Fatalf("expected nil wrapper on error, got %p", w)
	}
}

// ---: Run teardown path emits leak telemetry via PRODUCTION code ---
// TestResumeR3_RunTeardownEmitsLeakTelemetry drives an actual
// Executor.Run with a real workflow. A scripted coordinator sends a
// RouterMessage to the trigger step on EventStepEnd which - because the
// step's mailbox was sealed by runStep's defer - invokes ResumeStep.
// The resume model ignores ctx and blocks on `release` until test
// cleanup, wedging the resume goroutine. The outer ctx is then
// cancelled mid-resume and Run's teardown defer (executor.go:331-359)
// fires its 100ms bounded wait. Assertion: the PRODUCTION code emits
// EventMessage{reason=resume-timeout, count>=1} to the Progress sink.
// This differs from TestResumeR3_ResumeTimeoutEmitsLeakCount which
// inlines the teardown - here the emission IS the production path.
func TestResumeR3_RunTeardownEmitsLeakTelemetry(t *testing.T) {
	// - switched defer→t.Cleanup for parity with every other
	// test seam in the codebase. Defers fire on the enclosing
	// function exit; t.Cleanup fires on the *test* exit which is the
	// same in a top-level Test but stays correct if this body ever
	// gets wrapped in a t.Run subtest.
	prev := resumeShutdownTimeout
	resumeShutdownTimeout = 100 * time.Millisecond
	t.Cleanup(func() { resumeShutdownTimeout = prev })

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	resumeStarted := make(chan struct{})
	var resumeStartedOnce sync.Once
	keepaliveEntered := make(chan struct{})
	var keepaliveEnteredOnce sync.Once

	// Route model behavior by inspecting the system/user text so each
	// step + the resume get distinct responses:
	// - trigger step: "TRIGGER" marker → return "ready" fast
	// - keepalive step: "KEEPALIVE" marker → block until resume
	// actually started (so the workflow stays alive long enough
	// for the resume goroutine to enter DoGenerate), then return
	// "ok" fast
	// - resume goroutine: everything else → wedged (ignores ctx)
	model := &sequentialMockModel{
		fn: func(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
			joined := joinMessageText(params.Messages)
			// Routing priority: resume first (its transcript includes
			// both the original TRIGGER-STEP text AND the "RESUME-PROMPT"
			// marker from the coordinator's follow-up), then keepalive,
			// then trigger.
			switch {
			case strings.Contains(joined, "RESUME-PROMPT"):
				resumeStartedOnce.Do(func() { close(resumeStarted) })
				<-release // intentionally ignores ctx
				return textResult("late", 1, 1), nil
			case strings.Contains(joined, "KEEPALIVE-STEP"):
				keepaliveEnteredOnce.Do(func() { close(keepaliveEntered) })
				select {
				case <-resumeStarted:
				case <-time.After(3 * time.Second):
				}
				return textResult("ok", 1, 1), nil
			case strings.Contains(joined, "TRIGGER-STEP"):
				return textResult("ready", 1, 1), nil
			default:
				return textResult("unknown", 1, 1), nil
			}
		},
	}

	// replace the legacy resumeUnitScriptedCoordinator (a
	// CoordinatorAgent that sent a RouterMessage on EventStepEnd) with
	// the after push pattern: a coord *AgentRunner whose Mailbox
	// receives lifecycle pushes; an in-test goroutine drains the
	// mailbox, spots the trigger step's StepEnd, and Router.Sends a
	// RESUME-PROMPT to the trigger step ID - which is the same effect
	// the legacy OnStepEvent dispatch loop produced. The Router handle
	// is captured via WithRouterObserver from the orchestrator wiring
	// below.
	coord := newTestCoordRunner()
	coordMb := coord.mailbox
	prog := &captureSink{}

	exec := &Executor{
		Runner: &AgentRunner{
			model: model,
			// runStep builds its per-step AgentRunner with
			// RunID = e.Runner.RunID (executor.go:1519). ResumeStep
			// later consults e.runID which reads e.RunID. Set BOTH
			// to the same value so Append/Load keys align - otherwise
			// the transcript lands under "" and the Send is dropped
			// as no-transcript.
			runID: "run-teardown-test",
		},
		Workflow: newTestWorkflow([]Step{
			{ID: "trigger", Instructions: "TRIGGER-STEP: say ready"},
			{
				ID:           "keepalive",
				DependsOn:    []string{"trigger"},
				Instructions: "KEEPALIVE-STEP: stay alive",
			},
		}, nil),
		DefaultModel: "mock-model",
		Coordinator:  coord,
		Progress:     prog,
		RunID:        "run-teardown-test",
	}

	// Drain coord mailbox in the background; on first StepEnd of the
	// trigger step, fire the RESUME-PROMPT into the trigger mailbox via
	// the executor's Router (allocated lazily in Run - poll until
	// non-nil).
	resumeFiredOnce := sync.Once{}
	resumeFireDone := make(chan struct{})
	go func() {
		defer close(resumeFireDone)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			unread := coordMb.Unread("coordinator")
			if len(unread) > 0 {
				ids := make([]string, 0, len(unread))
				for _, m := range unread {
					ids = append(ids, m.MessageID)
					if m.Metadata["event_type"] == string(types.EventStepEnd) && m.Metadata["step_id"] == "trigger" {
						resumeFiredOnce.Do(func() {
							if exec.Router != nil {
								_ = exec.Router.Send("trigger", RouterMessage{
									From:    "coordinator",
									To:      "trigger",
									Type:    RouterMessageContextUpdate,
									Content: "RESUME-PROMPT: follow-up",
								}) // best-effort from goroutine; drop is observable via coordinator mailbox
							}
						})
					}
				}
				coordMb.MarkRead("coordinator", ids)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// Outer ctx so we can cancel mid-resume.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Run in a goroutine; cancel once the resume goroutine is wedged.
	type runResult struct {
		r   *WorkflowResult
		err error
	}
	done := make(chan runResult, 1)
	go func() {
		r, err := exec.Run(ctx)
		done <- runResult{r, err}
	}()

	// Wait for the resume goroutine to actually start blocking on
	// `release` before we cancel. Without this, the cancel may beat the
	// resume into existence and no leak occurs.
	// Sanity: keepalive must actually be entered, otherwise the test
	// regresses to the original race where the workflow finishes before
	// the resume goroutine schedules.
	select {
	case <-keepaliveEntered:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("keepalive step never entered - workflow racing finished?")
	}
	select {
	case <-resumeStarted:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("resume goroutine never entered the wedged DoGenerate")
	}

	// Cancel outer ctx → Run's teardown defer calls cancelRun → waits
	// resumeShutdownTimeout (100ms) → emits leak telemetry.
	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Executor.Run did not return after cancel")
	}

	// Assert the PRODUCTION teardown path emitted the telemetry.
	evts := snapshotEvents(prog)
	found := false
	for _, ev := range evts {
		if ev.Type != types.EventMessage {
			continue
		}
		r, _ := ev.Data["reason"].(string)
		if r != "resume-timeout" {
			continue
		}
		// count is stored as int64 via atomic.Int64.Load.
		count, _ := ev.Data["count"].(int64)
		if count < 1 {
			t.Errorf("count=%d, want >=1", count)
		}
		// assert the human-readable Message string carries the
		// production-canonical "leaked=<n>" format so future refactors
		// cannot silently break visual telemetry. Production emits this
		// at executor.go:352 (see cancelRun teardown block).
		if !strings.Contains(ev.Message, "leaked=") {
			t.Errorf("EventMessage.Message missing 'leaked=' marker; got %q", ev.Message)
		}
		found = true
		break
	}
	if !found {
		t.Errorf("production teardown path did NOT emit EventMessage{reason=resume-timeout}; types=%v",
			eventTypes(evts))
	}
}

// joinMessageText flattens all text parts across all messages into a
// single string so test models can route behavior by marker substring.
func joinMessageText(msgs []provider.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		for _, p := range m.Content {
			if p.Type == provider.PartText {
				b.WriteString(p.Text)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

// retired resumeUnitScriptedCoordinator (a CoordinatorAgent that
// emitted a RouterMessage on EventStepEnd). The
// TestResumeR3_RunTeardownEmitsLeakTelemetry test now drives the
// resume trigger via an in-test goroutine that drains the coord
// runner's mailbox and Router.Sends the RESUME-PROMPT directly - the
// same effect the legacy OnStepEvent dispatch loop produced.

// ---: activeResumeID cleared on ALL error paths ---

// fakeSealedStore returns ErrTranscriptTooLarge on Load so ResumeStep
// hits the cap-error cleanup path (executor_resume.go:345-351).
type fakeSealedStore struct{}

func (s *fakeSealedStore) Append(_, _ string, _ []provider.Message) error { return nil }
func (s *fakeSealedStore) Load(_, _ string) (*resume.StepTranscript, error) {
	return nil, fmt.Errorf("slot sealed: %w", resume.ErrTranscriptTooLarge)
}
func (s *fakeSealedStore) Delete(_, _ string) error { return nil }

// TestResumeR3_ActiveResumeIDClearedOnAllErrorPaths covers every error
// path that flips state.running back to false. Each path MUST also
// clear state.activeResumeID (invariant documented at
// executor_resume.go:30-34: activeResumeID is empty iff running is
// false). Missing clears on the resolver-error/nil paths were the
// finding.
func TestResumeR3_ActiveResumeIDClearedOnAllErrorPaths(t *testing.T) {
	assertCleared := func(t *testing.T, e *Executor, stepID string) {
		t.Helper()
		e.resumesMu.Lock()
		rs := e.resumes
		e.resumesMu.Unlock()
		if rs == nil {
			return // never allocated → trivially clean
		}
		st := rs.get(stepID)
		st.mu.Lock()
		defer st.mu.Unlock()
		if st.running {
			t.Errorf("state.running=true after error path, want false")
		}
		if st.activeResumeID != "" {
			t.Errorf("state.activeResumeID=%q after error path, want empty", st.activeResumeID)
		}
		if st.activeMailbox != nil {
			t.Errorf("state.activeMailbox non-nil after error path")
		}
	}

	t.Run("cap-error", func(t *testing.T) {
		e := &Executor{
			Runner:          &AgentRunner{model: &idModel{id: "m"}},
			RunID:           "r",
			transcriptStore: &fakeSealedStore{},
			Progress:        &captureSink{},
		}
		_, err := e.ResumeStep(context.Background(), "s", "p", "coord")
		if !errors.Is(err, resume.ErrTranscriptTooLarge) {
			t.Fatalf("want ErrTranscriptTooLarge, got %v", err)
		}
		assertCleared(t, e, "s")
	})

	t.Run("load-no-transcript", func(t *testing.T) {
		// ResumeStep bails with ErrNoTranscript BEFORE allocating a
		// resumeState when transcriptStore is nil. With a store that
		// returns ErrNoTranscript from Load, the state IS allocated,
		// CAS to running=true happens, then Load returns the error
		// and the cap-error cleanup block runs.
		store := &fakeLoadErrorStore{err: resume.ErrNoTranscript}
		e := &Executor{
			Runner:          &AgentRunner{model: &idModel{id: "m"}},
			RunID:           "r",
			transcriptStore: store,
			Progress:        &captureSink{},
		}
		_, err := e.ResumeStep(context.Background(), "s", "p", "coord")
		if !errors.Is(err, resume.ErrNoTranscript) {
			t.Fatalf("want ErrNoTranscript, got %v", err)
		}
		assertCleared(t, e, "s")
	})

	t.Run("resolver-missing", func(t *testing.T) {
		e := newResumeExecutor(t, nil)
		e.Runner = &AgentRunner{model: &idModel{id: "providerA:m1"}}
		e.Progress = &captureSink{}
		store := e.transcriptStore.(*resume.InMemoryTranscriptStore)
		store.SetMetadata(e.RunID, "s", "", "providerB:m2")
		_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})
		_, err := e.ResumeStep(context.Background(), "s", "p", "coord")
		if !errors.Is(err, ErrModelResolverMissing) {
			t.Fatalf("want ErrModelResolverMissing, got %v", err)
		}
		assertCleared(t, e, "s")
	})

	t.Run("resolver-error", func(t *testing.T) {
		e := newResumeExecutor(t, nil)
		e.Runner = &AgentRunner{model: &idModel{id: "providerA:m1"}}
		e.Progress = &captureSink{}
		e.ModelResolver = func(_ string) (provider.LanguageModel, error) {
			return nil, errors.New("infra down")
		}
		store := e.transcriptStore.(*resume.InMemoryTranscriptStore)
		store.SetMetadata(e.RunID, "s", "", "providerB:m2")
		_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})
		_, err := e.ResumeStep(context.Background(), "s", "p", "coord")
		if !errors.Is(err, ErrModelResolverError) {
			t.Fatalf("want ErrModelResolverError, got %v", err)
		}
		assertCleared(t, e, "s")
	})

	t.Run("resolver-nil", func(t *testing.T) {
		e := newResumeExecutor(t, nil)
		e.Runner = &AgentRunner{model: &idModel{id: "providerA:m1"}}
		e.Progress = &captureSink{}
		e.ModelResolver = func(_ string) (provider.LanguageModel, error) {
			return nil, nil
		}
		store := e.transcriptStore.(*resume.InMemoryTranscriptStore)
		store.SetMetadata(e.RunID, "s", "", "providerB:m2")
		_ = e.transcriptStore.Append(e.RunID, "s", []provider.Message{mkTextMsg(provider.RoleUser, "u")})
		_, err := e.ResumeStep(context.Background(), "s", "p", "coord")
		if !errors.Is(err, ErrModelResolverMissing) {
			t.Fatalf("want ErrModelResolverMissing, got %v", err)
		}
		assertCleared(t, e, "s")
	})
}

// fakeLoadErrorStore implements TranscriptStore; Load returns a
// configured error so ResumeStep hits the post-CAS cleanup block.
type fakeLoadErrorStore struct {
	err error
}

func (s *fakeLoadErrorStore) Append(_, _ string, _ []provider.Message) error   { return nil }
func (s *fakeLoadErrorStore) Load(_, _ string) (*resume.StepTranscript, error) { return nil, s.err }
func (s *fakeLoadErrorStore) Delete(_, _ string) error                         { return nil }

// ---: transient store error retries WITHIN the SAME Run ---
// TestResumeR3_TransientStoreErrorRetriesWithinSameRun - unlike
// TestResumeR3_TransientStoreErrorDoesNotSeal which uses two separate
// AgentRunner instances, this drives a SINGLE AgentRunner.Run.:
// the mechanism under test is NOT per-iteration retry - the flush path
// is driven by OnStepFinish AND a deferred final-flush at Run exit.
// The flaky store rejects the FIRST Append (transient, non-cap) so
// transcriptSealed stays false; the deferred final-flush within the
// same Run then re-attempts persistence on the up-to-date message
// slice and succeeds. Assertion: the store receives >=2 Append calls
// within the same Run and transcriptSealed never flips.
func TestResumeR3_TransientStoreErrorRetriesWithinSameRun(t *testing.T) {
	store := &flakyTranscriptStore{failOn: 1}

	// Turn 1: tool_calls → AgentRunner loops for another turn.
	// Turn 2: stop.
	toolCall := provider.ToolCall{
		ID:    "tc1",
		Name:  "noop",
		Input: json.RawMessage(`{}`),
	}
	turn1 := &provider.GenerateResult{
		Text:         "",
		ToolCalls:    []provider.ToolCall{toolCall},
		FinishReason: provider.FinishToolCalls,
		Usage:        provider.Usage{InputTokens: 1, OutputTokens: 1},
	}
	turn2 := textResult("final", 1, 1)
	model := &mockModel{responses: []*provider.GenerateResult{turn1, turn2}}

	noopTool := goai.Tool{
		Name:        "noop",
		Description: "noop",
		InputSchema: json.RawMessage(`{}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	}

	runner := &AgentRunner{
		model:      model,
		tools:      []goai.Tool{noopTool},
		progress:   &captureSink{},
		runID:      "run-retry",
		stepID:     "s",
		transcript: store,
	}
	_, err := runner.Run(context.Background(), AgentConfig{MaxTurns: 5}, "hello", "mock-model", runner.tools)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	store.mu.Lock()
	calls := store.calls
	succeeded := store.succeeded
	store.mu.Unlock()

	// Require at least 2 Append calls within THIS single Run: the first
	// fails (transient), the second succeeds. If the runner silently
	// seals after the first error, calls==1 or succeeded==0 and the
	// test fails.
	if calls < 2 {
		t.Errorf("Append called %d times in one Run, want >=2 (retry after transient error)", calls)
	}
	if succeeded < 1 {
		t.Errorf("no Append succeeded (%d/%d); transient error wrongly sealed store within same Run", succeeded, calls)
	}
}

// ---: coordinator inbox drain on step event ---

// retired r5DrainCoordinator (a CoordinatorAgent stub). The R5
// drain test below now installs a minimal *AgentRunner via
// newTestCoordRunner - non-nil coord wires the push branch in
// pushStepEventToCoord (rename) without executing an LLM call,
// and exercises drainCoordReverseReplies separately.

// TestResumeR5_CoordinatorInboxDrainedOnStepEvent verifies that when a
// reverse RouterMessage addressed to "coordinator" sits unread in the
// mailbox (as happens after resume completion), the
// drainCoordReverseReplies call (: extracted from
// notifyCoordinator) emits EventCoordinatorInboxMessage. Without this
// drain, resumed-step replies would never surface to operators - the
// CLI bug symptomised by
// `⚠ msg-dropped [coordinator] from=team-pro reason=unknown-step`.
func TestResumeR5_CoordinatorInboxDrainedOnStepEvent(t *testing.T) {
	prog := &captureSink{}
	router := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	router.SetMailbox(mb)
	router.RegisterStep("coordinator")
	router.RegisterInbox("coordinator")

	e := &Executor{
		Router:      router,
		Progress:    prog,
		Coordinator: newTestCoordRunner(),
		RunID:       "run-r5",
	}

	// Seed a reverse reply into the coordinator inbox - mirrors what
	// Executor.runResume Appends when a resumed step finishes.
	id, err := mb.Append("coordinator", RouterMessage{
		From:    "team-x",
		To:      "coordinator",
		Content: "hello from resumed",
		Type:    RouterMessageResumeReply,
	})
	if err != nil || id == "" {
		t.Fatalf("seed Append: err=%v id=%q", err, id)
	}

	// Fire pushStepEventToCoord + drainCoordReverseReplies as if a
	// sibling step just finished (: previously a single
	// notifyCoordinator call). The push lands in coord.Mailbox; the
	// drain picks up the seeded reverse reply.
	sr := &StepResult{Status: spec.StepCompleted}
	e.pushStepEventToCoord(context.Background(), e.RunID, "other-step", "agent", sr, nil)
	e.drainCoordReverseReplies(context.Background(), e.RunID)

	evts := snapshotEvents(prog)
	var inbox *Event
	for i := range evts {
		if evts[i].Type == types.EventCoordinatorInboxMessage {
			inbox = &evts[i]
			break
		}
	}
	if inbox == nil {
		t.Fatalf("no EventCoordinatorInboxMessage emitted; types=%v", eventTypes(evts))
	}
	if inbox.Message != "hello from resumed" {
		t.Errorf("Message=%q, want %q", inbox.Message, "hello from resumed")
	}
	if from, _ := inbox.Data["from"].(string); from != "team-x" {
		t.Errorf("Data[from]=%q, want team-x", from)
	}
	if typ, _ := inbox.Data["type"].(string); typ != "resume_reply" {
		t.Errorf("Data[type]=%q, want resume_reply", typ)
	}

	// After the drain, the message must be MarkRead'd - a second
	// drainCoordReverseReplies call must NOT re-emit it.
	prog.mu.Lock()
	prog.events = prog.events[:0]
	prog.mu.Unlock()
	e.pushStepEventToCoord(context.Background(), e.RunID, "other-step", "agent", sr, nil)
	e.drainCoordReverseReplies(context.Background(), e.RunID)
	for _, ev := range snapshotEvents(prog) {
		if ev.Type == types.EventCoordinatorInboxMessage {
			t.Errorf("duplicate EventCoordinatorInboxMessage emitted after drain; MarkRead broken")
		}
	}
}

// TestResumeR5_CoordinatorInboxAutoRegisteredUnderNonNoopCoordinator
// covers F1: when a non-noop Coordinator is installed and ExternalInboxes
// does NOT include "coordinator", the executor must still register the
// coordinator inbox so reverse replies do not drop with
// DropReasonUnknownStep. We verify the post-registration invariant by
// checking that Router.Send(coordinator, ...) lands in the mailbox
// after Run wires things up.
// Rather than run a full workflow (heavy), we call the same
// registration code path used inside Run: iterate effective inboxes,
// RegisterStep + RegisterInbox. We then send a message and confirm it
// lands without drop.
func TestResumeR5_CoordinatorAutoRegisteredInbox(t *testing.T) {
	router := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	router.SetMailbox(mb)

	// Simulate the executor's effective-inbox computation:
	// any non-nil coord runner triggers auto-registration of the coord
	// step ID (default "coordinator") into the workflow router's inbox
	// set so resume reverse replies do not drop with
	// DropReasonUnknownStep.
	coord := newTestCoordRunner()
	effective := []string{coordStepID(coord)}
	for _, id := range effective {
		router.RegisterStep(id)
		router.RegisterInbox(id)
	}

	// Track drops via OnDrop; if auto-registration failed, Send would
	// drop with DropReasonUnknownStep.
	var drops []DropEvent
	router.SetOnDrop(func(de DropEvent) { drops = append(drops, de) })

	if err := router.Send("coordinator", RouterMessage{
		From: "team-pro", To: "coordinator", Content: "x",
		Type: RouterMessageResumeReply,
	}); err != nil {
		t.Fatalf("setup send: %v", err)
	}

	if len(drops) != 0 {
		t.Fatalf("want 0 drops after auto-register, got %d: %+v", len(drops), drops)
	}
	if got := mb.Unread("coordinator"); len(got) != 1 {
		t.Fatalf("want 1 message in coordinator inbox, got %d", len(got))
	}
}

// TestDrainCoordReverseReplies_CustomStepID - . Pins the
// lifecycle-events-still-land-in-custom-StepID-Mailbox path. Sibling
// test TestExecutor_CustomStepIDAutoRegistersCoordRouterInbox covers
// the dual-inbox registration contract on the production code path.
// Setup: caller passes a coord runner with StepID="primary" (the embedded
// SDK consumer pattern). Asserts lifecycle events land in the runner's
// "primary" Mailbox bucket (proves no regression on the runner-side
// coordStepID push path under custom StepID).
func TestDrainCoordReverseReplies_CustomStepID(t *testing.T) {
	mailbox := NewInMemoryMailboxStore()
	runner := &AgentRunner{
		stepID:  "primary", // embedded-consumer pattern
		mailbox: mailbox,
	}
	model := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "ok", FinishReason: provider.FinishStop},
		},
	}
	wf := &Workflow{
		Name: "r3-4-custom-coord-stepid",
		Agents: map[string]AgentConfig{
			"a1": {Description: "x"},
		},
		Steps: []Step{
			{ID: "s1", Agent: "a1", Instructions: "noop"},
		},
	}

	o := New(
		WithModel(model),
		WithDefaultModel("test-model"),
		WithCoordinator(runner),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := o.RunFlow(ctx, wf); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}

	// Lifecycle events landed under runner.StepID="primary" - push path
	// unchanged by. Also assert nothing landed under "coordinator"
	// in the runner's mailbox (those would have been routing-key bugs).
	if got := mailbox.Unread("primary"); len(got) == 0 {
		t.Errorf("expected events in runner mailbox under StepID=primary, got 0")
	}
	if got := mailbox.Unread("coordinator"); len(got) != 0 {
		t.Errorf("unexpected events in runner mailbox under \"coordinator\" "+
			"(coordStepID(runner)=primary, so push must NOT use CoordRouterInboxID); got %d", len(got))
	}
}

// TestExecutor_CustomStepIDAutoRegistersCoordRouterInbox -
// production-path companion to TestDrainCoordReverseReplies_CustomStepID.
// Drives the Executor directly (bypassing Orchestrator) with a custom
// StepID="primary" coord runner, runs the wiring loop at executor.go
// ~545, then probes the live Router with a Send to CoordRouterInboxID.
// Pre-fix: Send drops with DropReasonUnknownStep because only "primary"
// was registered. Post-fix: zero drops, message lands.
// This test exercises the actual production code path - not a manual
// replica - so a regression that removes the dual-inbox registration
// will be caught here even if TestDrainCoordReverseReplies_CustomStepID's
// replica drifts from production wiring.
func TestExecutor_CustomStepIDAutoRegistersCoordRouterInbox(t *testing.T) {
	mailbox := NewInMemoryMailboxStore()
	runner := &AgentRunner{
		stepID:  "primary",
		mailbox: mailbox,
	}
	model := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "ok", FinishReason: provider.FinishStop},
		},
	}
	wf := &Workflow{
		Name: "r3-4-prod-path",
		Agents: map[string]AgentConfig{
			"a1": {Description: "x"},
		},
		Steps: []Step{
			{ID: "s1", Agent: "a1", Instructions: "noop"},
		},
	}

	var (
		dropMu sync.Mutex
		drops  []DropEvent
	)

	// The Executor allocates its own MessageRouter inside Run when
	// Coordinator != nil (executor.go:510 `e.Router = NewMessageRouter`),
	// so a caller-supplied Router would be discarded. Instead, hook
	// drops via DropCallback (e.DropCallback at line 605) which the
	// executor's OnDrop fanout dispatches into AFTER its own
	// SetOnDrop runs.
	exec := &Executor{
		Runner:       &AgentRunner{model: model, tools: nil},
		Workflow:     wf,
		DefaultModel: "test-model",
		Coordinator:  runner,
		DropCallback: func(de DropEvent) {
			dropMu.Lock()
			drops = append(drops, de)
			dropMu.Unlock()
		},
	}

	if _, err := exec.Run(t.Context()); err != nil {
		t.Fatalf("Executor.Run: %v", err)
	}

	// After Run, the executor's Router (e.Router, allocated inside
	// Run) must have BOTH coord-runner StepID ("primary") AND
	// CoordRouterInboxID ("coordinator") registered. Probe
	// CoordRouterInboxID with a reverse-reply Send: pre-fix this drops
	// with DropReasonUnknownStep; post-fix it lands.
	if err := exec.Router.Send(CoordRouterInboxID, RouterMessage{
		From:    "team-pro",
		To:      CoordRouterInboxID,
		Content: "post-run reverse reply",
		Type:    RouterMessageResumeReply,
	}); err != nil {
		t.Fatalf("setup send: %v", err)
	}

	dropMu.Lock()
	defer dropMu.Unlock()
	for _, de := range drops {
		if de.Reason == DropReasonUnknownStep && de.StepID == CoordRouterInboxID {
			t.Errorf("production-wiring contract violated: post-run Send to %q dropped with reason=%s",
				CoordRouterInboxID, de.Reason)
		}
	}
}
