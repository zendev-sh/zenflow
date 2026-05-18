package exec

// agent_messaging_test.go - AUV2 PR4 zenflow surface: AgentConfig
// InitialMessages plumbing, standalone-agent transcript persistence,
// and AgentHandle.SendMessage / RunID / PrimaryStepID.

import (
	"context"
	"strings"
	"testing"

	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/resume"
)

func userMsg(text string) provider.Message {
	return provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Part{{Type: provider.PartText, Text: text}},
	}
}

func msgText(m provider.Message) string {
	var b strings.Builder
	for _, p := range m.Content {
		if p.Type == provider.PartText {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// TestNewAgentMessaging verifies the per-call substrate is fully wired:
// router with an installed mailbox, a cap-1 wake channel, and the
// primary step ID derived from the run ID and registered as an inbox.
func TestNewAgentMessaging(t *testing.T) {
	ms := newAgentMessaging("run-xyz")
	if ms.runID != "run-xyz" {
		t.Fatalf("runID = %q, want run-xyz", ms.runID)
	}
	if ms.primaryStepID != agentPrimaryStepID("run-xyz") {
		t.Fatalf("primaryStepID = %q, want %q", ms.primaryStepID, agentPrimaryStepID("run-xyz"))
	}
	if ms.router == nil || ms.router.Mailbox() == nil {
		t.Fatal("router/mailbox not wired")
	}
	if ms.mailbox == nil {
		t.Fatal("mailbox nil")
	}
	if cap(ms.wake) != 1 {
		t.Fatalf("wake cap = %d, want 1", cap(ms.wake))
	}
	// The inbox must accept a Send addressed to the primary step.
	if err := ms.router.Send(ms.primaryStepID, RouterMessage{
		To: ms.primaryStepID, Content: "ping", Type: RouterMessageInfo,
	}); err != nil {
		t.Fatalf("Send to registered inbox: %v", err)
	}
}

// TestRunAgent_InitialMessages_Prepended asserts AgentConfig.InitialMessages
// flows through RunAgent into the runner: the saved transcript lands
// before the new user prompt in the very first LLM call.
func TestRunAgent_InitialMessages_Prepended(t *testing.T) {
	model := &mockModel{responses: []*provider.GenerateResult{textResult("ok", 1, 1)}}
	o := New(WithModel(model), WithDefaultModel("gpt-4o"))

	_, err := o.RunAgent(t.Context(), AgentConfig{
		Prompt:          "NEWPROMPT",
		InitialMessages: []provider.Message{userMsg("PRIORONE"), userMsg("PRIORTWO")},
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	calls := model.getCalls()
	if len(calls) == 0 {
		t.Fatal("model never called")
	}
	msgs := calls[0].Messages
	if len(msgs) < 3 {
		t.Fatalf("first call has %d messages, want >=3 (2 prior + new)", len(msgs))
	}
	if got := msgText(msgs[0]); got != "PRIORONE" {
		t.Errorf("msg[0] = %q, want PRIORONE", got)
	}
	if got := msgText(msgs[1]); got != "PRIORTWO" {
		t.Errorf("msg[1] = %q, want PRIORTWO", got)
	}
	if got := msgText(msgs[2]); !strings.Contains(got, "NEWPROMPT") {
		t.Errorf("msg[2] = %q, want it to contain NEWPROMPT", got)
	}
}

// TestRunAgent_NoInitialMessages_ColdStart asserts the cold-start path
// is unchanged: with no InitialMessages the first call sees only the
// new user prompt.
func TestRunAgent_NoInitialMessages_ColdStart(t *testing.T) {
	model := &mockModel{responses: []*provider.GenerateResult{textResult("ok", 1, 1)}}
	o := New(WithModel(model), WithDefaultModel("gpt-4o"))

	if _, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "HELLO"}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	msgs := model.getCalls()[0].Messages
	if len(msgs) != 1 {
		t.Fatalf("cold start first call has %d messages, want 1", len(msgs))
	}
	if got := msgText(msgs[0]); !strings.Contains(got, "HELLO") {
		t.Errorf("msg[0] = %q, want it to contain HELLO", got)
	}
}

// TestRunAgent_TranscriptPersisted asserts a standalone RunAgent
// persists its conversation to the orchestrator's TranscriptStore,
// keyed by (runID, primaryStepID), so the agent can later be
// resurrected from the saved transcript.
func TestRunAgent_TranscriptPersisted(t *testing.T) {
	model := &mockModel{responses: []*provider.GenerateResult{textResult("the-answer", 1, 1)}}
	store := resume.NewInMemoryTranscriptStore()
	o := New(
		WithModel(model),
		WithDefaultModel("gpt-4o"),
		WithRunID("run-fixed-1"),
		WithTranscriptStore(func() resume.TranscriptStore { return store }),
	)
	if _, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "the-question"}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	tr, err := store.Load("run-fixed-1", agentPrimaryStepID("run-fixed-1"))
	if err != nil {
		t.Fatalf("transcript not persisted: Load: %v", err)
	}
	if len(tr.Messages) == 0 {
		t.Fatal("persisted transcript has no messages")
	}
	if got := msgText(tr.Messages[0]); !strings.Contains(got, "the-question") {
		t.Errorf("transcript msg[0] = %q, want it to contain the-question", got)
	}
}

// TestRunAgentAsync_HandleExposesRunIDAndStepID asserts the AgentHandle
// surfaces the run ID and primary step ID so a consumer can load the
// agent's transcript for resurrection.
func TestRunAgentAsync_HandleExposesRunIDAndStepID(t *testing.T) {
	model := &mockModel{responses: []*provider.GenerateResult{textResult("ok", 1, 1)}}
	o := New(WithModel(model), WithDefaultModel("gpt-4o"))
	h, err := o.RunAgentAsync(t.Context(), AgentConfig{Prompt: "go"})
	if err != nil {
		t.Fatalf("RunAgentAsync: %v", err)
	}
	if h.RunID() == "" {
		t.Error("RunID() empty")
	}
	if h.PrimaryStepID() != agentPrimaryStepID(h.RunID()) {
		t.Errorf("PrimaryStepID() = %q, want %q", h.PrimaryStepID(), agentPrimaryStepID(h.RunID()))
	}
	<-h.Done()
}

// TestRunAgentAsync_DistinctRunIDsPerAgent asserts each async agent
// gets its own run ID even on a shared orchestrator (and even when
// that orchestrator was built with WithRunID), so concurrent agents'
// transcripts and mailboxes never collide.
func TestRunAgentAsync_DistinctRunIDsPerAgent(t *testing.T) {
	model := &mockModel{responses: []*provider.GenerateResult{
		textResult("a", 1, 1), textResult("b", 1, 1),
	}}
	o := New(WithModel(model), WithDefaultModel("gpt-4o"), WithRunID("pinned-run"))
	h1, err := o.RunAgentAsync(t.Context(), AgentConfig{Prompt: "1"})
	if err != nil {
		t.Fatalf("RunAgentAsync 1: %v", err)
	}
	h2, err := o.RunAgentAsync(t.Context(), AgentConfig{Prompt: "2"})
	if err != nil {
		t.Fatalf("RunAgentAsync 2: %v", err)
	}
	if h1.RunID() == h2.RunID() {
		t.Fatalf("two async agents share run ID %q - transcripts/mailboxes would collide", h1.RunID())
	}
	if h1.RunID() == "pinned-run" || h2.RunID() == "pinned-run" {
		t.Error("RunAgentAsync honoured o.runID - must always generate a fresh one")
	}
	<-h1.Done()
	<-h2.Done()
}

// TestAgentHandle_SendMessage_QueuesIntoMailbox asserts SendMessage on
// a running agent appends the message to the agent's mailbox and
// signals its wake channel.
func TestAgentHandle_SendMessage_QueuesIntoMailbox(t *testing.T) {
	// blockingModel parks in DoGenerate until ctx cancel, keeping the
	// agent "running" for the duration of the test.
	model := &blockingModel{}
	o := New(WithModel(model), WithDefaultModel("gpt-4o"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h, err := o.RunAgentAsync(ctx, AgentConfig{Prompt: "work"})
	if err != nil {
		t.Fatalf("RunAgentAsync: %v", err)
	}
	res, err := h.SendMessage("user-followup")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if res != "queued: "+h.PrimaryStepID() {
		t.Errorf("SendMessage result = %q, want queued: %s", res, h.PrimaryStepID())
	}
	unread := h.msg.mailbox.Unread(h.msg.primaryStepID)
	if len(unread) != 1 {
		t.Fatalf("mailbox has %d unread, want 1", len(unread))
	}
	if unread[0].Content != "user-followup" {
		t.Errorf("queued message = %q, want user-followup", unread[0].Content)
	}
	select {
	case <-h.msg.wake:
		// wake signalled - good
	default:
		t.Error("wake channel was not signalled")
	}
}

// TestAgentHandle_SendMessage_AfterFinish_Drops asserts SendMessage to
// an already-finished agent returns a target-terminal drop so the
// consumer routes to the resurrection path instead.
func TestAgentHandle_SendMessage_AfterFinish_Drops(t *testing.T) {
	model := &mockModel{responses: []*provider.GenerateResult{textResult("done", 1, 1)}}
	o := New(WithModel(model), WithDefaultModel("gpt-4o"))
	h, err := o.RunAgentAsync(t.Context(), AgentConfig{Prompt: "quick"})
	if err != nil {
		t.Fatalf("RunAgentAsync: %v", err)
	}
	<-h.Done()
	res, err := h.SendMessage("too-late")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !strings.HasPrefix(res, "dropped:") {
		t.Errorf("SendMessage to finished agent = %q, want a dropped: result", res)
	}
}

// TestAgentHandle_SendMessage_NoSubstrate asserts a handle with no
// messaging substrate (built via NewAgentHandle in test fixtures)
// returns an error rather than panicking.
func TestAgentHandle_SendMessage_NoSubstrate(t *testing.T) {
	h, err := NewAgentHandle("agent-fixture")
	if err != nil {
		t.Fatalf("NewAgentHandle: %v", err)
	}
	if _, err := h.SendMessage("x"); err == nil {
		t.Error("SendMessage on substrate-less handle returned nil error")
	}
	if h.RunID() != "" || h.PrimaryStepID() != "" {
		t.Error("substrate-less handle should report empty RunID/PrimaryStepID")
	}
}

// TestAgentHandle_SendMessage_NilReceiver guards the nil-handle path.
func TestAgentHandle_SendMessage_NilReceiver(t *testing.T) {
	var h *AgentHandle
	if _, err := h.SendMessage("x"); err == nil {
		t.Error("SendMessage on nil handle returned nil error")
	}
	if h.RunID() != "" || h.PrimaryStepID() != "" {
		t.Error("nil handle should report empty RunID/PrimaryStepID")
	}
}
