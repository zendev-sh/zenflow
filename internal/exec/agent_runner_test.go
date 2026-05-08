package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/resume"
	"github.com/zendev-sh/zenflow/internal/router"
	"github.com/zendev-sh/zenflow/internal/types"
)

// Ensure goai import is used.
var _ goai.Tool

// --- Tests ---

func TestAgentRunner_SimpleResponse(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("Hello, world!", 10, 5),
		},
	}

	runner := &AgentRunner{model: model}
	result, err := runner.Run(t.Context(), AgentConfig{}, "Say hello", "gpt-4o", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "Hello, world!" {
		t.Errorf("content = %q, want %q", result.Content, "Hello, world!")
	}
	if result.Tokens.InputTokens != 10 {
		t.Errorf("input tokens = %d, want 10", result.Tokens.InputTokens)
	}
	if result.Tokens.OutputTokens != 5 {
		t.Errorf("output tokens = %d, want 5", result.Tokens.OutputTokens)
	}
	if result.Turns < 1 {
		t.Errorf("turns = %d, want >= 1", result.Turns)
	}

	// Verify the model was called.
	calls := model.getCalls()
	if len(calls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(calls))
	}
}

// runner.SystemPrompt must reach the LLM via goai.WithSystem.
// Previously the field was set on the runner but only consumed by
// transcript metadata, leaving the LLM with an empty system message and
// forcing all behavioural rules into the user prompt or tool descriptions.
// This test pins the contract: a non-empty SystemPrompt MUST surface as
// GenerateParams.System on every DoGenerate call.
func TestAgentRunner_SystemPromptInjectedToGoAI(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{textResult("ok", 1, 1)},
	}
	const sys = "You are the workflow coordinator. Follow addressing rules strictly."
	runner := &AgentRunner{model: model, systemPrompt: sys}
	if _, err := runner.Run(t.Context(), AgentConfig{}, "go", "gpt-4o", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls := model.getCalls()
	if len(calls) == 0 {
		t.Fatalf("no DoGenerate calls recorded")
	}
	for i, c := range calls {
		if c.System != sys {
			t.Errorf("call %d: GenerateParams.System=%q, want %q", i, c.System, sys)
		}
	}
}

// empty SystemPrompt must NOT inject a stray WithSystem option.
// Workflow agents bake their role into the user message via
// prompt.go::AssemblePrompt and intentionally leave runner.SystemPrompt
// empty (executor.go); injecting an empty system would shadow the
// caller's GoAIOptions(WithSystem(...)) supplied via the Orchestrator.
func TestAgentRunner_EmptySystemPromptNotInjected(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{textResult("ok", 1, 1)},
	}
	runner := &AgentRunner{model: model} // SystemPrompt unset
	if _, err := runner.Run(t.Context(), AgentConfig{}, "go", "gpt-4o", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls := model.getCalls()
	if len(calls) == 0 {
		t.Fatalf("no DoGenerate calls recorded")
	}
	for i, c := range calls {
		if c.System != "" {
			t.Errorf("call %d: GenerateParams.System=%q, want empty", i, c.System)
		}
	}
}

func TestAgentRunner_ToolLoop(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			toolCallResult("", 20, 10,
				tc("call-1", "read_file", json.RawMessage(`{"path":"main.go"}`))),
			textResult("I read the file and it contains Go code.", 30, 15),
		},
	}
	tools := []goai.Tool{
		makeTool("read_file", "read a file", "package main\nfunc main() {}"),
	}

	runner := &AgentRunner{model: model, tools: tools}
	result, err := runner.Run(t.Context(), AgentConfig{}, "Read main.go", "gpt-4o", tools)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "I read the file and it contains Go code." {
		t.Errorf("content = %q", result.Content)
	}
	if result.Turns < 2 {
		t.Errorf("turns = %d, want >= 2", result.Turns)
	}
	if result.Tokens.InputTokens < 50 {
		t.Errorf("input tokens = %d, want >= 50", result.Tokens.InputTokens)
	}

	// Second DoGenerate call should include tool result messages.
	calls := model.getCalls()
	if len(calls) < 2 {
		t.Fatalf("model calls = %d, want >= 2", len(calls))
	}
	foundTool := false
	for _, m := range calls[1].Messages {
		if m.Role == provider.RoleTool {
			foundTool = true
		}
	}
	if !foundTool {
		t.Error("expected tool message in second model call")
	}
}

func TestAgentRunner_MultipleToolCalls(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			toolCallResult("", 20, 10,
				tc("c1", "read_file", json.RawMessage(`{"path":"a.go"}`)),
				tc("c2", "read_file", json.RawMessage(`{"path":"b.go"}`))),
			textResult("Both files read.", 40, 20),
		},
	}
	tools := []goai.Tool{
		makeTool("read_file", "read a file", "file content"),
	}

	runner := &AgentRunner{model: model, tools: tools}
	result, err := runner.Run(t.Context(), AgentConfig{}, "Read both files", "gpt-4o", tools)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "Both files read." {
		t.Errorf("content = %q", result.Content)
	}

	// Second call should have tool result messages.
	calls := model.getCalls()
	if len(calls) < 2 {
		t.Fatalf("model calls = %d, want >= 2", len(calls))
	}
	toolCount := 0
	for _, m := range calls[1].Messages {
		if m.Role == provider.RoleTool {
			toolCount++
		}
	}
	if toolCount < 2 {
		t.Errorf("tool messages = %d, want >= 2", toolCount)
	}
}

func TestAgentRunner_MaxTurns(t *testing.T) {
	// Model always returns tool calls - should stop at maxTurns.
	model := &mockModel{
		responses: make([]*provider.GenerateResult, 100),
	}
	for i := range model.responses {
		model.responses[i] = toolCallResult("", 5, 5,
			tc("c1", "noop", json.RawMessage(`{}`)))
	}
	tools := []goai.Tool{
		makeTool("noop", "no-op", "ok"),
	}

	runner := &AgentRunner{model: model, tools: tools}
	result, err := runner.Run(t.Context(), AgentConfig{MaxTurns: 3}, "loop forever", "gpt-4o", tools)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Turns != 3 {
		t.Errorf("turns = %d, want exactly 3 (MaxTurns cap)", result.Turns)
	}
}

func TestAgentRunner_ToolError(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			toolCallResult("", 10, 5,
				tc("c1", "failing_tool", json.RawMessage(`{}`))),
			textResult("Tool failed, but I handled it.", 20, 10),
		},
	}

	tools := []goai.Tool{
		makeErrorTool("failing_tool", "a tool that fails"),
	}

	runner := &AgentRunner{model: model, tools: tools}
	result, err := runner.Run(t.Context(), AgentConfig{}, "call failing tool", "gpt-4o", tools)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "Tool failed, but I handled it." {
		t.Errorf("content = %q", result.Content)
	}
}

func TestAgentRunner_ContextCancelled(t *testing.T) {
	// goai.GenerateText propagates context cancellation as an error.
	model := &blockingModel{}

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // Cancel immediately.

	runner := &AgentRunner{model: model}
	_, err := runner.Run(ctx, AgentConfig{}, "hello", "gpt-4o", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// The error should contain context.Canceled somewhere in the chain.
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// errorPermissions returns an error from RequestPermission.
type errorPermissions struct{}

func (e *errorPermissions) RequestPermission(_ context.Context, _ PermissionRequest) (bool, error) {
	return false, errors.New("permission system broken")
}

func TestAgentRunner_PermissionError(t *testing.T) {
	// When permission handler returns an error, the OnBeforeToolExecute hook
	// sets Skip=true + Error. goai sends this as a tool result error to the model.
	// The model then responds without tool calls. The overall Run doesn't fail.
	model := &mockModel{
		responses: []*provider.GenerateResult{
			toolCallResult("", 10, 5,
				tc("c1", "bash", json.RawMessage(`{}`))),
			textResult("handled permission error", 20, 10),
		},
	}
	tools := []goai.Tool{
		makeTool("bash", "run commands", "ok"),
	}

	runner := &AgentRunner{
		model:       model,
		tools:       tools,
		permissions: &errorPermissions{},
	}
	result, err := runner.Run(t.Context(), AgentConfig{}, "do", "gpt-4o", tools)
	if err != nil {
		// If goai propagates permission errors as fatal, that's also acceptable.
		if !strings.Contains(err.Error(), "permission system broken") {
			t.Errorf("error = %v, want 'permission system broken'", err)
		}
		return
	}
	// If no error, the model should have handled the permission denial.
	if result.Content != "handled permission error" {
		t.Logf("content = %q (permission error was handled by model)", result.Content)
	}
}

// TestAgentRunner_NoMailbox_RunsWithoutMessaging verifies that an
// AgentRunner with no Mailbox/Wake configured (mailbox mode off)
// completes its tool loop normally - the equivalent of the legacy
// "closed chan inbox" path that asserted Run did not depend on inbox
// being open.
func TestAgentRunner_NoMailbox_RunsWithoutMessaging(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			toolCallResult("", 10, 5,
				tc("c1", "read", json.RawMessage(`{}`))),
			textResult("done with no mailbox", 10, 5),
		},
	}
	tools := []goai.Tool{
		makeTool("read", "read file", "file"),
	}

	runner := &AgentRunner{
		model: model,
		tools: tools,
		// Mailbox + Wake intentionally nil → mailbox mode disabled.
	}

	result, err := runner.Run(t.Context(), AgentConfig{}, "do", "gpt-4o", tools)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "done with no mailbox" {
		t.Errorf("content = %q, want %q", result.Content, "done with no mailbox")
	}
}

// denyAllPermissions denies every tool call.
type denyAllPermissions struct{}

func (d *denyAllPermissions) RequestPermission(_ context.Context, _ PermissionRequest) (bool, error) {
	return false, nil
}

func TestAgentRunner_PermissionDenied(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			toolCallResult("", 10, 5,
				tc("c1", "bash", json.RawMessage(`{"cmd":"rm -rf /"}`))),
			textResult("Permission was denied, so I can't do that.", 20, 10),
		},
	}
	tools := []goai.Tool{
		makeTool("bash", "run commands", "ok"),
	}

	runner := &AgentRunner{
		model:       model,
		tools:       tools,
		permissions: &denyAllPermissions{},
	}
	result, err := runner.Run(t.Context(), AgentConfig{}, "delete everything", "gpt-4o", tools)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "Permission was denied, so I can't do that." {
		t.Errorf("content = %q", result.Content)
	}

	// The tool should NOT have been executed - goai sends permission denied via OnBeforeToolExecute hook.
	// We just verify the model was called more than once (tool call + response).
	calls := model.getCalls()
	if len(calls) < 2 {
		t.Logf("model calls = %d (may vary based on goai tool loop implementation)", len(calls))
	}
}

// TestAgentRunner_SendMessageBypassesPermission covers
// agent_runner.go:818-820 - the send_message tool short-circuits the
// OnBeforeToolExecute permission check. We wire a deny-all permission
// handler then issue a send_message tool call; the call must succeed
// (not be skipped with "permission denied"), proving the bypass branch
// fires before the permissions.RequestPermission gate.
func TestAgentRunner_SendMessageBypassesPermission(t *testing.T) {
	// Tool that records its calls so we can assert it WAS executed.
	var executed atomic.Bool
	sendMessageTool := goai.Tool{
		Name:        "send_message",
		Description: "internal coord routing",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			executed.Store(true)
			return "delivered", nil
		},
	}
	model := &mockModel{
		responses: []*provider.GenerateResult{
			toolCallResult("", 10, 5,
				tc("c1", "send_message", json.RawMessage(`{"target":"coord","content":"hi"}`))),
			textResult("done", 20, 10),
		},
	}
	tools := []goai.Tool{sendMessageTool}
	runner := &AgentRunner{
		model:       model,
		tools:       tools,
		permissions: &denyAllPermissions{},
	}
	_, err := runner.Run(t.Context(), AgentConfig{}, "send", "gpt-4o", tools)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !executed.Load() {
		t.Error("send_message Execute was not called; permission bypass branch did not fire")
	}
}

func TestAgentRunner_MaxTurnsReturnsAssistantContent(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			toolCallResult("Let me try something", 10, 5,
				tc("c1", "read", json.RawMessage(`{}`))),
			toolCallResult("Still working on it", 10, 5,
				tc("c2", "read", json.RawMessage(`{}`))),
		},
	}
	tools := []goai.Tool{
		makeTool("read", "read file", "file content"),
	}

	runner := &AgentRunner{model: model, tools: tools}
	result, err := runner.Run(t.Context(), AgentConfig{MaxTurns: 2}, "read files", "gpt-4o", tools)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// When truncated, result.Content comes from the last goai step's text.
	// The exact value depends on goai's handling of truncation.
	if result.Content == "" && result.Status != AgentStatusTruncated {
		t.Errorf("expected either content or truncated status")
	}
}

func TestAgentRunner_AssistantMessageIncludesToolCalls(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			toolCallResult("thinking...", 10, 5,
				tc("c1", "read", json.RawMessage(`{"path":"a.go"}`))),
			textResult("done", 20, 10),
		},
	}
	tools := []goai.Tool{
		makeTool("read", "read file", "content"),
	}

	runner := &AgentRunner{model: model, tools: tools}
	_, err := runner.Run(t.Context(), AgentConfig{}, "read file", "gpt-4o", tools)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Second DoGenerate call's messages should have assistant message with ToolCalls populated.
	calls := model.getCalls()
	if len(calls) < 2 {
		t.Fatalf("model calls = %d, want >= 2", len(calls))
	}
	foundAssistantWithTools := false
	for _, m := range calls[1].Messages {
		if m.Role == provider.RoleAssistant {
			// Check if the message has tool-call parts
			for _, p := range m.Content {
				if p.Type == provider.PartToolCall {
					foundAssistantWithTools = true
				}
			}
		}
	}
	if !foundAssistantWithTools {
		t.Error("expected assistant message with tool call parts in second model call")
	}
}

// --- Streaming + Verbose AgentRunner tests ---

func TestAgentRunner_Streaming_VerboseEmitsOutput(t *testing.T) {
	model := &streamingMockModel{
		responses: []*provider.GenerateResult{
			textResult("Streamed hello!", 10, 5),
		},
	}

	var outputs []Output
	progress := &mockProgressSink{
		onOutput: func(_ context.Context, o Output) {
			outputs = append(outputs, o)
		},
	}

	runner := &AgentRunner{
		model:     model,
		streaming: true,
		verbose:   true,
		progress:  progress,
		runID:     "run-1",
		stepID:    "step-1",
	}
	result, err := runner.Run(t.Context(), AgentConfig{}, "Say hello", "test-model", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "Streamed hello!" {
		t.Errorf("content = %q, want %q", result.Content, "Streamed hello!")
	}

	// Should have at least one delta and one Done output.
	if len(outputs) < 2 {
		t.Fatalf("expected at least 2 outputs (delta + done), got %d", len(outputs))
	}
	var hasDelta, hasDone bool
	for _, o := range outputs {
		if o.Delta != "" {
			hasDelta = true
			if o.RunID != "run-1" || o.StepID != "step-1" {
				t.Errorf("delta output RunID=%q StepID=%q, want run-1/step-1", o.RunID, o.StepID)
			}
		}
		if o.Done {
			hasDone = true
		}
	}
	if !hasDelta {
		t.Error("expected at least one delta output")
	}
	if !hasDone {
		t.Error("expected a Done output")
	}
}

// streaming mode now emits text deltas regardless of Verbose.
// Previous behaviour: --stream alone was a no-op (text chunks read
// from goai stream but discarded), so users saw zero difference between
// `--stream` and default mode. The sink (sink/stdout.go:384) does NOT
// gate text deltas on verbose, so dropping the runner gate makes
// `--stream` actually stream agent text token-by-token even without
// --verbose. This test locks the new behaviour.
func TestAgentRunner_Streaming_NotVerbose_EmitsTextOutput(t *testing.T) {
	model := &streamingMockModel{
		responses: []*provider.GenerateResult{
			textResult("hello", 10, 5),
		},
	}

	var textOutputCount int
	progress := &mockProgressSink{
		onOutput: func(_ context.Context, o Output) {
			if !o.Reasoning && o.Delta != "" {
				textOutputCount++
			}
		},
	}

	runner := &AgentRunner{
		model:     model,
		streaming: true,
		verbose:   false, // --stream alone now streams text without --verbose.
		progress:  progress,
	}
	_, err := runner.Run(t.Context(), AgentConfig{}, "hello", "test-model", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if textOutputCount == 0 {
		t.Errorf("regression: expected >0 text OnOutput calls when --stream alone, got 0 (was the verbose gate accidentally re-introduced?)")
	}
}

// non-streaming mode still gates text on Verbose. Agent text
// is delivered as one full chunk at end of LLM turn - would dump
// arbitrary-length agent responses to stdout in default mode if not
// gated. Streaming gets its own gate because the user explicitly
// opted into streaming output.
func TestAgentRunner_NonStreaming_NotVerbose_NoTextOutput(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("hello", 10, 5),
		},
	}

	var textOutputCount int
	progress := &mockProgressSink{
		onOutput: func(_ context.Context, o Output) {
			if !o.Reasoning && o.Delta != "" {
				textOutputCount++
			}
		},
	}

	runner := &AgentRunner{
		model:     model,
		streaming: false, // non-streaming path - verbose gate at agent_runner.go:785 still applies
		verbose:   false,
		progress:  progress,
	}
	_, err := runner.Run(t.Context(), AgentConfig{}, "hello", "test-model", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if textOutputCount > 0 {
		t.Errorf("non-streaming + non-verbose should not emit batched agent text (would noise stdout), got %d", textOutputCount)
	}
}

func TestAgentRunner_NonStreaming_VerboseEmitsOutput(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("Non-stream output.", 10, 5),
		},
	}

	var outputs []Output
	progress := &mockProgressSink{
		onOutput: func(_ context.Context, o Output) {
			outputs = append(outputs, o)
		},
	}

	runner := &AgentRunner{
		model:    model,
		verbose:  true,
		progress: progress,
		runID:    "run-2",
		stepID:   "step-2",
	}
	result, err := runner.Run(t.Context(), AgentConfig{}, "hello", "test-model", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "Non-stream output." {
		t.Errorf("content = %q", result.Content)
	}

	// Non-streaming verbose emits: thinking signal + text delta + Done.
	// Filter out reasoning signals to check text outputs.
	var textOutputs []Output
	for _, o := range outputs {
		if !o.Reasoning {
			textOutputs = append(textOutputs, o)
		}
	}
	if len(textOutputs) != 2 {
		t.Fatalf("expected 2 text outputs (text + done), got %d", len(textOutputs))
	}
	if textOutputs[0].Delta != "Non-stream output." {
		t.Errorf("first output delta = %q", textOutputs[0].Delta)
	}
	if !textOutputs[1].Done {
		t.Error("second output should be Done")
	}
}

func TestAgentRunner_NonStreaming_VerboseSkipsEmptyText(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "", FinishReason: provider.FinishStop, Usage: provider.Usage{InputTokens: 5, OutputTokens: 0}},
		},
	}

	var textOutputCount int
	progress := &mockProgressSink{
		onOutput: func(_ context.Context, o Output) {
			if !o.Reasoning {
				textOutputCount++
			}
		},
	}

	runner := &AgentRunner{
		model:    model,
		verbose:  true,
		progress: progress,
	}
	_, err := runner.Run(t.Context(), AgentConfig{}, "hello", "test-model", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty text should not emit text OnOutput (thinking signal is OK).
	if textOutputCount != 0 {
		t.Errorf("expected 0 text OnOutput for empty text, got %d", textOutputCount)
	}
}

func TestAgentRunner_Streaming_ReasoningChunks(t *testing.T) {
	model := &reasoningStreamModel{
		text:      "Final answer",
		reasoning: "Let me think...",
		usage:     provider.Usage{InputTokens: 10, OutputTokens: 5},
	}

	var outputs []Output
	progress := &mockProgressSink{
		onOutput: func(_ context.Context, o Output) {
			outputs = append(outputs, o)
		},
		onEvent: func(_ context.Context, _ Event) {},
	}

	runner := &AgentRunner{
		model:     model,
		streaming: true,
		verbose:   true,
		progress:  progress,
		runID:     "run-r",
		stepID:    "step-r",
	}
	result, err := runner.Run(t.Context(), AgentConfig{}, "Think about this", "test-model", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// goai's result.Text may include reasoning tokens - just check it contains the text.
	if !strings.Contains(result.Content, "Final answer") {
		t.Errorf("content = %q, expected to contain %q", result.Content, "Final answer")
	}

	// Should have reasoning outputs with Reasoning=true.
	if len(outputs) == 0 {
		t.Fatal("no outputs received - reasoning streaming wiring is broken")
	}
	var hasReasoning bool
	for _, o := range outputs {
		if o.Reasoning && o.Delta == "Let me think..." {
			hasReasoning = true
		}
	}
	if !hasReasoning {
		t.Error("expected reasoning output with delta 'Let me think...'")
	}
}

func TestAgentRunner_NonStreaming_OnRequestEmitsTurnEventOnly(t *testing.T) {
	// Tests the OnRequest hook path: Progress is set, not streaming.
	// Contract: emit EventAgentTurn{phase:request}; do NOT emit a bare
	// reasoning Output. The "◎ Thinking..." header should only appear
	// when the provider actually returns reasoning content (avoids
	// per-turn noise when thinking is disabled).
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("response", 10, 5),
		},
	}

	var events []Event
	var outputs []Output
	progress := &mockProgressSink{
		onEvent: func(_ context.Context, e Event) {
			events = append(events, e)
		},
		onOutput: func(_ context.Context, o Output) {
			outputs = append(outputs, o)
		},
	}

	runner := &AgentRunner{
		model:    model,
		progress: progress,
	}
	_, err := runner.Run(t.Context(), AgentConfig{}, "hello", "test-model", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var hasRequestEvent bool
	for _, e := range events {
		if e.Type == types.EventAgentTurn {
			if phase, _ := e.Data["phase"].(string); phase == "request" {
				hasRequestEvent = true
			}
		}
	}
	if !hasRequestEvent {
		t.Error("expected EventAgentTurn with phase=request from OnRequest hook")
	}

	for _, o := range outputs {
		if o.Reasoning {
			t.Errorf("unexpected reasoning Output emitted (response had no reasoning content): %+v", o)
		}
	}
}

func TestAgentRunner_Streaming_InitError(t *testing.T) {
	// DoStream returns error → Run should return error.
	model := &failingStreamModel{err: fmt.Errorf("stream init failed")}
	runner := &AgentRunner{
		model:     model,
		streaming: true,
		verbose:   true,
	}
	_, err := runner.Run(t.Context(), AgentConfig{}, "hello", "test-model", nil)
	if err == nil {
		t.Fatal("expected error from streaming")
	}
	if !strings.Contains(err.Error(), "stream init failed") {
		t.Errorf("error = %v, expected to contain 'stream init failed'", err)
	}
}

func TestAgentRunner_Streaming_StreamError(t *testing.T) {
	// Stream produces ChunkError → stream.Err() non-nil after consumption.
	model := &errorStreamModel{text: "partial", err: fmt.Errorf("mid-stream failure")}
	runner := &AgentRunner{
		model:     model,
		streaming: true,
		verbose:   true,
		progress:  &mockProgressSink{onOutput: func(_ context.Context, _ Output) {}},
	}
	_, err := runner.Run(t.Context(), AgentConfig{}, "hello", "test-model", nil)
	if err == nil {
		t.Fatal("expected error from stream.Err()")
	}
	if !strings.Contains(err.Error(), "mid-stream failure") {
		t.Errorf("error = %v, expected to contain 'mid-stream failure'", err)
	}
}

// the legacy chan-Inbox tests below were rewritten
// onto the mailbox path (Mailbox+Wake fields). Each test now stages
// messages by Append-ing into a MailboxStore before/after Run starts,
// triggering the wake-driven continuation via the per-step Wake chan.
//
// Behaviour preserved:
//   - pre-start drain (was "initial drain")
//   - pre-start cancel short-circuit
//   - mid-execution wake + drain (was "OnBeforeStep drain")
//   - post-primary continuation drain (was "late drain")
//   - cancel during continuation
//   - continuation error fallback to primary result

// newMailboxRunnerEnv builds a runner wired into a fresh
// InMemoryMailboxStore + cap-1 Wake chan, returning the runner, the
// store (so the test can Append directly), and the wake chan (so the
// test can simulate engine wakes).
func newMailboxRunnerEnv(model provider.LanguageModel, stepID string) (*AgentRunner, *InMemoryMailboxStore, chan struct{}) {
	mb := NewInMemoryMailboxStore()
	wake := make(chan struct{}, 1)
	r := &AgentRunner{
		model:   model,
		stepID:  stepID,
		mailbox: mb,
		wake:    wake,
	}
	return r, mb, wake
}

func TestAgentRunner_PreStartMailboxDrain_DeliveredToLLM(t *testing.T) {
	captured := &capturingModel{result: textResult("ok", 10, 5)}
	runner, mb, _ := newMailboxRunnerEnv(captured, "step-a")

	if _, err := mb.Append("step-a", RouterMessage{From: "coordinator", Content: "context from upstream"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := mb.Append("step-a", RouterMessage{From: "coordinator", Content: "second context"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	var drains int
	runner.progress = &mockProgressSink{onEvent: func(_ context.Context, e Event) {
		if e.Type == types.EventAgentInboxDrain {
			drains++
		}
	}}

	_, err := runner.Run(t.Context(), AgentConfig{}, "start", "m", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if drains != 2 {
		t.Errorf("EventAgentInboxDrain count = %d, want 2", drains)
	}
	if len(captured.lastMessages) < 3 {
		t.Fatalf("expected >=3 messages (1 user + 2 drained), got %d", len(captured.lastMessages))
	}
	seen := 0
	for _, m := range captured.lastMessages {
		for _, p := range m.Content {
			if p.Type == provider.PartText && strings.Contains(p.Text, "context from upstream") {
				seen++
			}
			if p.Type == provider.PartText && strings.Contains(p.Text, "second context") {
				seen++
			}
		}
	}
	if seen != 2 {
		t.Errorf("drained messages not both found in LLM prompt (seen %d/2)", seen)
	}
}

func TestAgentRunner_PreStartMailboxDrain_Cancel(t *testing.T) {
	captured := &capturingModel{result: textResult("should not reach", 10, 5)}
	runner, mb, _ := newMailboxRunnerEnv(captured, "step-a")

	if _, err := mb.Append("step-a", RouterMessage{Type: router.MessageCancel, From: "parent"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	result, err := runner.Run(t.Context(), AgentConfig{}, "start", "m", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Content != "cancelled" {
		t.Errorf("content = %q, want 'cancelled'", result.Content)
	}
	if captured.calls != 0 {
		t.Errorf("LLM should not have been called, got %d calls", captured.calls)
	}
}

// TestAgentRunner_PreStartMailboxDrain_GateCtxCancel_EmitsResidualDrops
// covers the pre-loop early-return at the preStartDrainGate ctx.Done()
// branch. Messages buffered before the wake loop starts MUST surface as
// EventMessageDropped (reason=WorkflowCancelled) instead of being silently
// dropped when the gate observes ctx cancel.
func TestAgentRunner_PreStartMailboxDrain_GateCtxCancel_EmitsResidualDrops(t *testing.T) {
	captured := &capturingModel{result: textResult("should not reach", 10, 5)}
	runner, mb, _ := newMailboxRunnerEnv(captured, "step-a")

	// Hold the gate so Run blocks on the select; ctx cancel will then
	// trigger the line-617 early-return path under test.
	gate := make(chan struct{})
	runner.preStartDrainGate = gate

	if _, err := mb.Append("step-a", RouterMessage{From: "coordinator", Content: "msg-1"}); err != nil {
		t.Fatalf("Append-1: %v", err)
	}
	if _, err := mb.Append("step-a", RouterMessage{From: "coordinator", Content: "msg-2"}); err != nil {
		t.Fatalf("Append-2: %v", err)
	}

	var drops int
	var dropReasons []string
	runner.progress = &mockProgressSink{onEvent: func(_ context.Context, e Event) {
		if e.Type == types.EventMessageDropped {
			drops++
			if reason, ok := e.Data["reason"].(string); ok {
				dropReasons = append(dropReasons, reason)
			}
		}
	}}

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // pre-cancel so the gate's select hits ctx.Done immediately.

	result, err := runner.Run(ctx, AgentConfig{}, "start", "m", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Content != "cancelled" {
		t.Errorf("content = %q, want 'cancelled'", result.Content)
	}
	if captured.calls != 0 {
		t.Errorf("LLM should not have been called, got %d calls", captured.calls)
	}
	if drops < 2 {
		t.Errorf("EventMessageDropped count = %d, want >=2 (residual mailbox drains)", drops)
	}
	wantReason := router.DropReasonWorkflowCancelled.String()
	for _, r := range dropReasons {
		if r != wantReason {
			t.Errorf("drop reason = %q, want %q", r, wantReason)
		}
	}
}

// TestAgentRunner_PreStartMailboxDrain_CancelMessage_EmitsResidualDrops
// covers the pre-loop early-return at line 626 (cancel-message path
// after drainMailboxIntoMessages). Messages buffered AFTER a cancel
// marker remain Unread and MUST surface as EventMessageDropped
// instead of being silently dropped on cancelled exit.
func TestAgentRunner_PreStartMailboxDrain_CancelMessage_EmitsResidualDrops(t *testing.T) {
	captured := &capturingModel{result: textResult("should not reach", 10, 5)}
	runner, mb, _ := newMailboxRunnerEnv(captured, "step-a")

	// Order matters: cancel first (consumed and triggers cancelled=true),
	// then more messages remain Unread in the mailbox after drainMailboxIntoMessages
	// returns. Those residual messages must be observed as drops.
	if _, err := mb.Append("step-a", RouterMessage{Type: router.MessageCancel, From: "parent"}); err != nil {
		t.Fatalf("Append cancel: %v", err)
	}
	if _, err := mb.Append("step-a", RouterMessage{From: "coordinator", Content: "after-cancel-1"}); err != nil {
		t.Fatalf("Append-1: %v", err)
	}
	if _, err := mb.Append("step-a", RouterMessage{From: "coordinator", Content: "after-cancel-2"}); err != nil {
		t.Fatalf("Append-2: %v", err)
	}

	var drops int
	var dropReasons []string
	runner.progress = &mockProgressSink{onEvent: func(_ context.Context, e Event) {
		if e.Type == types.EventMessageDropped {
			drops++
			if reason, ok := e.Data["reason"].(string); ok {
				dropReasons = append(dropReasons, reason)
			}
		}
	}}

	result, err := runner.Run(t.Context(), AgentConfig{}, "start", "m", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Content != "cancelled" {
		t.Errorf("content = %q, want 'cancelled'", result.Content)
	}
	if captured.calls != 0 {
		t.Errorf("LLM should not have been called, got %d calls", captured.calls)
	}
	if drops < 2 {
		t.Errorf("EventMessageDropped count = %d, want >=2 (residual mailbox messages after cancel)", drops)
	}
	wantReason := router.DropReasonWorkflowCancelled.String()
	for _, r := range dropReasons {
		if r != wantReason {
			t.Errorf("drop reason = %q, want %q", r, wantReason)
		}
	}
}

func TestAgentRunner_WakeDrain_ContinuesOnNewMessages(t *testing.T) {
	stepID := "step-late"
	mb := NewInMemoryMailboxStore()
	wake := make(chan struct{}, 1)

	model := &mailboxModel{
		responses: []*provider.GenerateResult{
			textResult("primary done", 10, 5),
			textResult("processed late msg", 10, 5),
		},
		afterFirst: func() {
			// Simulate the delivery engine: append + wake.
			_, _ = mb.Append(stepID, RouterMessage{From: "coordinator", Content: "late context"})
			select {
			case wake <- struct{}{}:
			default:
			}
		},
	}
	var drains int
	runner := &AgentRunner{
		model:   model,
		stepID:  stepID,
		mailbox: mb,
		wake:    wake,
		progress: &mockProgressSink{onEvent: func(_ context.Context, e Event) {
			if e.Type == types.EventAgentInboxDrain {
				drains++
			}
		}},
	}

	result, err := runner.Run(t.Context(), AgentConfig{}, "start", "m", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Content != "processed late msg" {
		t.Errorf("content = %q, want 'processed late msg' (continuation)", result.Content)
	}
	if drains != 1 {
		t.Errorf("EventAgentInboxDrain count = %d, want 1", drains)
	}
	if model.calls < 2 {
		t.Errorf("expected >=2 LLM calls (primary + continuation), got %d", model.calls)
	}
}

func TestAgentRunner_WakeDrain_Cancel(t *testing.T) {
	stepID := "step-cancel-cont"
	mb := NewInMemoryMailboxStore()
	wake := make(chan struct{}, 1)

	model := &mailboxModel{
		responses: []*provider.GenerateResult{textResult("primary", 10, 5)},
		afterFirst: func() {
			_, _ = mb.Append(stepID, RouterMessage{Type: router.MessageCancel, From: "parent"})
			select {
			case wake <- struct{}{}:
			default:
			}
		},
	}
	runner := &AgentRunner{
		model:   model,
		stepID:  stepID,
		mailbox: mb,
		wake:    wake,
	}
	result, err := runner.Run(t.Context(), AgentConfig{}, "start", "m", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Content != "cancelled" {
		t.Errorf("content = %q, want 'cancelled'", result.Content)
	}
	if model.calls != 1 {
		t.Errorf("expected exactly 1 LLM call (primary only), got %d", model.calls)
	}
}

// TestAgentRunner_WakeLoopDrainCancel_EmitsResidualDrops covers the
// wake-loop drain early-return at the cancel-message path
// (after drainMailboxIntoMessages returns cancelled=true on a wake
// re-entry). drainMailboxIntoMessages MarkReads only up to the cancel
// marker (consumed = cancelIdx+1); any messages that were Unread AFTER
// the marker - or appended between the Unread() snapshot and cancel
// detection - remain unread. They MUST surface as EventMessageDropped
// with reason=WorkflowCancelled instead of being silently abandoned by
// the cancelled-exit return path.
//
// Mirrors the pre-loop sibling test
// TestAgentRunner_PreStartMailboxDrain_CancelMessage_EmitsResidualDrops
// but exercises the wake-loop continuation drain instead of the
// pre-start drain.
func TestAgentRunner_WakeLoopDrainCancel_EmitsResidualDrops(t *testing.T) {
	stepID := "step-wake-cancel-residue"
	mb := NewInMemoryMailboxStore()
	wake := make(chan struct{}, 1)

	model := &mailboxModel{
		responses: []*provider.GenerateResult{
			textResult("primary done", 10, 5),
			textResult("should not be reached", 10, 5),
		},
		afterFirst: func() {
			// Order matters: msg1 (consumed), then cancel (triggers
			// cancelled=true, MarkReads pending[:cancelIdx+1]), then
			// msg2 + msg3 which remain Unread and must surface as drops.
			_, _ = mb.Append(stepID, RouterMessage{From: "coordinator", Content: "msg-1"})
			_, _ = mb.Append(stepID, RouterMessage{Type: router.MessageCancel, From: "parent"})
			_, _ = mb.Append(stepID, RouterMessage{From: "coordinator", Content: "after-cancel-1"})
			_, _ = mb.Append(stepID, RouterMessage{From: "coordinator", Content: "after-cancel-2"})
			select {
			case wake <- struct{}{}:
			default:
			}
		},
	}

	var drops int
	var dropReasons []string
	runner := &AgentRunner{
		model:   model,
		stepID:  stepID,
		mailbox: mb,
		wake:    wake,
		progress: &mockProgressSink{onEvent: func(_ context.Context, e Event) {
			if e.Type == types.EventMessageDropped {
				drops++
				if reason, ok := e.Data["reason"].(string); ok {
					dropReasons = append(dropReasons, reason)
				}
			}
		}},
	}

	result, err := runner.Run(t.Context(), AgentConfig{}, "start", "m", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Content != "cancelled" {
		t.Errorf("content = %q, want 'cancelled'", result.Content)
	}
	// Exactly 1 LLM call - primary only. The wake-loop drain must short
	// circuit on the cancel marker before a second GenerateText fires.
	if model.calls != 1 {
		t.Errorf("expected exactly 1 LLM call (primary only), got %d", model.calls)
	}
	if drops < 2 {
		t.Errorf("EventMessageDropped count = %d, want >=2 (msg2 + msg3 left unread after cancel marker)", drops)
	}
	wantReason := router.DropReasonWorkflowCancelled.String()
	for _, r := range dropReasons {
		if r != wantReason {
			t.Errorf("drop reason = %q, want %q", r, wantReason)
		}
	}
}

// capturingModel records messages passed to DoGenerate so tests can assert
// that drained messages made it into the LLM prompt.
type capturingModel struct {
	result       *provider.GenerateResult
	lastMessages []provider.Message
	calls        int
}

func (m *capturingModel) ModelID() string { return "capturing" }
func (m *capturingModel) Capabilities() provider.ModelCapabilities {
	return provider.ModelCapabilities{}
}
func (m *capturingModel) DoGenerate(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
	m.calls++
	m.lastMessages = append([]provider.Message(nil), params.Messages...)
	return m.result, nil
}
func (m *capturingModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, fmt.Errorf("not implemented")
}

// TestAgentRunner_WakeContextProvider_InjectsOnInitialAndPerWake
// verifies WithRunnerWakeContextProvider fires once before the first
// LLM call and once on every wake-driven re-entry, with the returned
// text wrapped in <dynamic-context> tags as a fresh user message.
// Empty / whitespace-only returns must be skipped.
func TestAgentRunner_WakeContextProvider_InjectsOnInitialAndPerWake(t *testing.T) {
	stepID := "step-ctx"
	mb := NewInMemoryMailboxStore()
	wake := make(chan struct{}, 1)

	calls := 0
	provideCtx := func() string {
		calls++
		switch calls {
		case 1:
			return "snapshot-A"
		case 2:
			return ""
		default:
			return "  \n\t " // whitespace-only - skipped
		}
	}

	captures := [][]provider.Message{}
	model := &capturingMailboxModel{
		responses: []*provider.GenerateResult{
			textResult("primary done", 10, 5),
			textResult("processed late msg", 10, 5),
		},
		afterFirst: func() {
			_, _ = mb.Append(stepID, RouterMessage{From: "coordinator", Content: "late msg"})
			select {
			case wake <- struct{}{}:
			default:
			}
		},
		onCall: func(msgs []provider.Message) {
			snap := append([]provider.Message(nil), msgs...)
			captures = append(captures, snap)
		},
	}

	runner := &AgentRunner{
		model:               model,
		stepID:              stepID,
		mailbox:             mb,
		wake:                wake,
		wakeContextProvider: provideCtx,
	}

	_, err := runner.Run(t.Context(), AgentConfig{}, "start", "m", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if calls != 2 {
		t.Fatalf("provider call count = %d, want 2 (initial + 1 wake)", calls)
	}
	if len(captures) < 2 {
		t.Fatalf("captured %d LLM calls, want >= 2", len(captures))
	}

	// First LLM call: must contain <dynamic-context>snapshot-A</dynamic-context>.
	if !messagesContainText(captures[0], "<dynamic-context>\nsnapshot-A\n</dynamic-context>") {
		t.Errorf("initial call missing snapshot-A injection; messages=%+v", captures[0])
	}
	// Second LLM call (post-wake): provider returned "" so NO new
	// <dynamic-context> message must have been appended on this cycle.
	// We assert by counting <dynamic-context> occurrences across the
	// second call's messages: still exactly 1 (from the initial inject,
	// retained in the conversation), not 2.
	if got := countContextMarkers(captures[1]); got != 1 {
		t.Errorf("dynamic-context markers in second call = %d, want 1 (empty return must be skipped)", got)
	}
}

// TestAgentRunner_WakeContextProvider_NilDisables verifies that
// runners constructed without a provider see no injected
// <dynamic-context> messages on any LLM call.
func TestAgentRunner_WakeContextProvider_NilDisables(t *testing.T) {
	model := &capturingModel{
		result: textResult("done", 5, 5),
	}
	runner := &AgentRunner{model: model, stepID: "s"}
	if _, err := runner.Run(t.Context(), AgentConfig{}, "go", "m", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := countContextMarkers(model.lastMessages); got != 0 {
		t.Errorf("nil provider must inject 0 <dynamic-context> messages, got %d", got)
	}
}

func messagesContainText(msgs []provider.Message, want string) bool {
	for _, m := range msgs {
		for _, p := range m.Content {
			if p.Type == provider.PartText && strings.Contains(p.Text, want) {
				return true
			}
		}
	}
	return false
}

func countContextMarkers(msgs []provider.Message) int {
	n := 0
	for _, m := range msgs {
		for _, p := range m.Content {
			if p.Type == provider.PartText && strings.Contains(p.Text, "<dynamic-context>") {
				n++
			}
		}
	}
	return n
}

// capturingMailboxModel layers message capture on top of mailboxModel
// behaviour (queued responses + afterFirst hook). Used by
// wake-context-provider tests that need to assert what messages each
// LLM call saw across the wake re-entry boundary.
type capturingMailboxModel struct {
	responses  []*provider.GenerateResult
	afterFirst func()
	onCall     func([]provider.Message)
	calls      int
}

func (m *capturingMailboxModel) ModelID() string { return "capturing-mailbox" }
func (m *capturingMailboxModel) Capabilities() provider.ModelCapabilities {
	return provider.ModelCapabilities{}
}
func (m *capturingMailboxModel) DoGenerate(_ context.Context, p provider.GenerateParams) (*provider.GenerateResult, error) {
	idx := m.calls
	m.calls++
	if m.onCall != nil {
		m.onCall(p.Messages)
	}
	if idx >= len(m.responses) {
		return textResult("fallback", 1, 1), nil
	}
	res := m.responses[idx]
	if idx == 0 && m.afterFirst != nil {
		m.afterFirst()
	}
	return res, nil
}
func (m *capturingMailboxModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, fmt.Errorf("not implemented")
}

// mailboxModel returns queued responses in order and invokes afterFirst once,
// immediately after the first DoGenerate call - letting tests inject
// post-primary mailbox messages that the wake-loop continuation observes.
type mailboxModel struct {
	responses  []*provider.GenerateResult
	afterFirst func()
	calls      int
}

func (m *mailboxModel) ModelID() string { return "mailbox-model" }
func (m *mailboxModel) Capabilities() provider.ModelCapabilities {
	return provider.ModelCapabilities{}
}
func (m *mailboxModel) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	idx := m.calls
	m.calls++
	if idx >= len(m.responses) {
		return textResult("fallback", 1, 1), nil
	}
	res := m.responses[idx]
	if idx == 0 && m.afterFirst != nil {
		m.afterFirst()
	}
	return res, nil
}
func (m *mailboxModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, fmt.Errorf("not implemented")
}

// TestAgentRunner_MidLoopDrain_ViaToolExecution exercises the
// mid-execution delivery scenario: a tool injects a mailbox message
// while the LLM tool loop is running, the wake fires, and the next
// goai iteration drains the mailbox into the LLM thread. Replaces
// the old chan-based OnBeforeStepDrain test.
func TestAgentRunner_MidLoopDrain_ViaToolExecution(t *testing.T) {
	stepID := "step-injector"
	mb := NewInMemoryMailboxStore()
	wake := make(chan struct{}, 1)

	model := &mockModel{
		responses: []*provider.GenerateResult{
			toolCallResult("", 10, 5, tc("c1", "injector", json.RawMessage(`{}`))),
			textResult("done", 10, 5),
		},
	}
	injectorTool := goai.Tool{
		Name:        "injector",
		Description: "appends a mailbox message + wake on execute",
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			_, _ = mb.Append(stepID, RouterMessage{From: "coordinator", Content: "injected mid-turn"})
			select {
			case wake <- struct{}{}:
			default:
			}
			return "done", nil
		},
	}
	var drains int
	runner := &AgentRunner{
		model:   model,
		tools:   []goai.Tool{injectorTool},
		stepID:  stepID,
		mailbox: mb,
		wake:    wake,
		progress: &mockProgressSink{onEvent: func(_ context.Context, e Event) {
			if e.Type == types.EventAgentInboxDrain {
				drains++
			}
		}},
	}
	_, err := runner.Run(t.Context(), AgentConfig{MaxTurns: 5}, "do", "m", []goai.Tool{injectorTool})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if drains != 1 {
		t.Errorf("EventAgentInboxDrain count = %d, want 1", drains)
	}
}

// TestAgentRunner_WakeDrain_ContinuationError verifies that if the
// wake-driven continuation LLM call fails, the runner returns the
// primary result rather than propagating the error (best-effort
// semantics; matches old late-drain fallback).
func TestAgentRunner_WakeDrain_ContinuationError(t *testing.T) {
	stepID := "step-cont-err"
	mb := NewInMemoryMailboxStore()
	wake := make(chan struct{}, 1)

	model := &errorOnSecondCallMailboxModel{
		primary: textResult("primary result", 10, 5),
		afterFirst: func() {
			_, _ = mb.Append(stepID, RouterMessage{From: "coordinator", Content: "late trigger"})
			select {
			case wake <- struct{}{}:
			default:
			}
		},
	}
	runner := &AgentRunner{
		model:   model,
		stepID:  stepID,
		mailbox: mb,
		wake:    wake,
	}
	result, err := runner.Run(t.Context(), AgentConfig{}, "start", "m", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Content != "primary result" {
		t.Errorf("content = %q, want 'primary result' (continuation failed, fall back to primary)", result.Content)
	}
}

// errorOnSecondCallMailboxModel returns primary on first call, fires
// afterFirst, then returns an error on the continuation call.
type errorOnSecondCallMailboxModel struct {
	primary    *provider.GenerateResult
	afterFirst func()
	calls      int
}

func (m *errorOnSecondCallMailboxModel) ModelID() string { return "err-on-2nd-mb" }
func (m *errorOnSecondCallMailboxModel) Capabilities() provider.ModelCapabilities {
	return provider.ModelCapabilities{}
}
func (m *errorOnSecondCallMailboxModel) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	m.calls++
	if m.calls == 1 {
		if m.afterFirst != nil {
			m.afterFirst()
		}
		return m.primary, nil
	}
	return nil, fmt.Errorf("simulated continuation failure")
}
func (m *errorOnSecondCallMailboxModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, fmt.Errorf("not implemented")
}

// TestAgentRunner_MidLoopCancel_ViaToolExecution verifies a cancel
// message injected during tool execution (via mailbox + wake) stops
// the agent on the next continuation drain.
func TestAgentRunner_MidLoopCancel_ViaToolExecution(t *testing.T) {
	stepID := "step-cancel-inject"
	mb := NewInMemoryMailboxStore()
	wake := make(chan struct{}, 1)

	model := &mockModel{
		responses: []*provider.GenerateResult{
			toolCallResult("", 10, 5, tc("c1", "cancel-injector", json.RawMessage(`{}`))),
			textResult("should not reach here", 10, 5),
		},
	}
	cancelTool := goai.Tool{
		Name:        "cancel-injector",
		Description: "appends a cancel message + wake",
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			_, _ = mb.Append(stepID, RouterMessage{Type: router.MessageCancel, From: "parent"})
			select {
			case wake <- struct{}{}:
			default:
			}
			return "done", nil
		},
	}
	runner := &AgentRunner{
		model:   model,
		tools:   []goai.Tool{cancelTool},
		stepID:  stepID,
		mailbox: mb,
		wake:    wake,
	}
	result, err := runner.Run(t.Context(), AgentConfig{MaxTurns: 5}, "do", "m", []goai.Tool{cancelTool})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Content != "cancelled" {
		t.Errorf("content = %q, want 'cancelled'", result.Content)
	}
}

// TestAgentRunner_MaxWakeCycles_EmitsWarningAndDrops verifies B3:
// once the wake loop reaches the configured MaxWakeCycles cap with
// pending mailbox messages, the runner MUST:
//  1. emit EventMaxWakeCyclesWarning at >=80% of the cap (once)
//  2. emit one EventMessageDropped{reason:"max-wake-cycles"} per
//     message remaining in the mailbox after the cap fires
//
// (i.e. zero silent drops even on pathological wake-loop hot-loops).
func TestAgentRunner_MaxWakeCycles_EmitsWarningAndDrops(t *testing.T) {
	stepID := "step-cap"
	mb := NewInMemoryMailboxStore()
	wake := make(chan struct{}, 1)

	// Tight cap so the test runs fast: cap=2 → warnAt = int(2*0.8) = 1.
	// To exercise the cap-hit drop path we need messages still pending
	// AFTER the loop exits. Strategy: a "noisy producer" model that
	// appends a fresh mailbox message on EVERY DoGenerate call. With
	// cap=2 the loop runs iter0 (primary) → drain msg from iter0 →
	// iter1 (one more msg appended during iter1's DoGenerate) → loop
	// exits (cap reached) with msg-iter1 still in the mailbox →
	// drain-on-cap path drops it as max-wake-cycles.
	model := &noisyProducerModel{mailbox: mb, stepID: stepID, wake: wake}

	var (
		warnings   []Event
		drops      []Event
		idleCount  int
		wakeCount  int
		drainCount int
	)
	sink := &mockProgressSink{onEvent: func(_ context.Context, e Event) {
		switch e.Type {
		case types.EventMaxWakeCyclesWarning:
			warnings = append(warnings, e)
		case types.EventMessageDropped:
			drops = append(drops, e)
		case types.EventAgentIdle:
			idleCount++
		case types.EventAgentWake:
			wakeCount++
		case types.EventAgentInboxDrain:
			drainCount++
		}
	}}

	runner := &AgentRunner{
		model:         model,
		stepID:        stepID,
		mailbox:       mb,
		wake:          wake,
		progress:      sink,
		maxWakeCycles: 1,
	}

	_, err := runner.Run(t.Context(), AgentConfig{}, "start", "m", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(warnings) != 1 {
		t.Errorf("EventMaxWakeCyclesWarning count = %d, want 1; warnings=%+v", len(warnings), warnings)
	} else {
		if cur, _ := warnings[0].Data["current_cycle"].(int); cur < 1 {
			t.Errorf("warning current_cycle = %v, want >=1", warnings[0].Data["current_cycle"])
		}
		if maxC, _ := warnings[0].Data["max_cycles"].(int); maxC != 1 {
			t.Errorf("warning max_cycles = %v, want 1", warnings[0].Data["max_cycles"])
		}
	}

	// At least one drop expected (msg-pre-cap was queued and the cap
	// fired before the runner could drain it via a wake cycle).
	if len(drops) < 1 {
		t.Errorf("EventMessageDropped count = %d, want >=1; drops=%+v drains=%d wakes=%d", len(drops), drops, drainCount, wakeCount)
	}
	for _, d := range drops {
		if reason, _ := d.Data["reason"].(string); reason != "max-wake-cycles" {
			t.Errorf("drop reason = %q, want max-wake-cycles", reason)
		}
	}
	_ = idleCount // observability - not asserted (race-prone)
}

// when the runner's loop exits because ctx was canceled
// (workflow teardown), remaining unread messages must be dropped with
// reason="workflow-cancelled" - NOT "max-wake-cycles". User saw
// `⚠ msg-dropped from=verdict reason=max-wake-cycles` AT END of a
// successful workflow because the unconditional cap-handler mislabeled
// ctx-cancel residue as cap exhaustion.
func TestAgentRunner_CtxCancelDrop_UsesWorkflowCancelledReason(t *testing.T) {
	stepID := "step-cancel"
	mb := NewInMemoryMailboxStore()
	wake := make(chan struct{}, 1)

	// Deterministic ctx-cancel-with-residue: the model appends a fresh
	// mailbox message at the START of the second DoGenerate call, then
	// signals "ready" + blocks until ctx is cancelled. The test cancels
	// only after receiving the ready signal, so:
	//   - iter=0: call 1 returns success quickly. result!=nil. Loop
	//     drains the pre-existing message and re-enters.
	//   - iter=1: call 2 appends a NEW message, signals ready, blocks
	//     on ctx.Done. Test cancels ctx. DoGenerate returns ctx.Err
	//     but result!=nil so the runner breaks out of the loop (NOT
	//     the early-return path at line ~1070).
	//   - Post-loop drain sees the call-2 message and MUST emit a drop
	//     with reason=workflow-cancelled (NOT max-wake-cycles).
	// Without the synchronization, ctx could cancel before call 2
	// reaches Append, leaving the mailbox empty and skipping the drop
	// emit path entirely - which is what produced the original SKIP.
	ready := make(chan struct{})
	model := &cancellableProducerModel{mailbox: mb, stepID: stepID, wake: wake, ready: ready}

	var drops []Event
	sink := &mockProgressSink{onEvent: func(_ context.Context, e Event) {
		if e.Type == types.EventMessageDropped {
			drops = append(drops, e)
		}
	}}

	runner := &AgentRunner{
		model:         model,
		stepID:        stepID,
		mailbox:       mb,
		wake:          wake,
		progress:      sink,
		maxWakeCycles: 100, // high cap so we DON'T hit it
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel() // belt-and-suspenders: ensures Run unblocks if the
	// ready signal somehow doesn't arrive (e.g. future refactor breaks
	// the call-2 path) so the test fails fast instead of hanging.
	go func() {
		// Wait for the model's call-2 to finish appending its message
		// AND to be parked on ctx.Done. Then cancel - this guarantees
		// the post-loop drain will see the call-2 message and emit a
		// workflow-cancelled drop.
		select {
		case <-ready:
			cancel()
		case <-ctx.Done():
			return
		}
	}()

	_, _ = runner.Run(ctx, AgentConfig{}, "start", "m", nil)

	if len(drops) == 0 {
		t.Fatalf("no EventMessageDropped emitted; expected exactly 1 with reason=workflow-cancelled. mailbox unread=%d, model calls=%d",
			len(mb.Unread(stepID)), model.calls)
	}
	for _, d := range drops {
		reason, _ := d.Data["reason"].(string)
		if reason == router.DropReasonMaxWakeCycles.String() {
			t.Errorf("drop reason = %q, want %q (cap NOT hit; ctx was canceled)",
				reason, router.DropReasonWorkflowCancelled.String())
		}
		if reason != router.DropReasonWorkflowCancelled.String() {
			t.Errorf("drop reason = %q, want %q", reason, router.DropReasonWorkflowCancelled.String())
		}
	}
}

// TestAgentRunner_CtxCancelDrop_FirstCallEmitsResidualDrops covers the
// A5#9 + B5#4 observability gap: when the FIRST goai.GenerateText call
// errors with result==nil (e.g. ctx-cancel during the very first call),
// the runner takes the early-return path inside the wake loop and
// previously skipped the post-loop drain entirely - silently dropping any
// mailbox messages already delivered before cancel.
//
// Deterministic synchronization (mirrors RTL5-G pattern):
//   - The model's FIRST DoGenerate appends a residue message, signals
//     `ready` (blocking), then blocks on ctx.Done.
//   - The test waits for `ready`, then cancels ctx. Because we only
//     proceed past `ready` after the residue is in the mailbox AND the
//     model is parked on ctx.Done, the runner is GUARANTEED to:
//       1) take the early-return path (result==nil + genErr==ctx.Err)
//       2) observe the residue in r.mailbox.Unread(stepID)
//   - Without the fix, the early-return at agent_runner.go ~1070
//     skips the post-loop drain - test observes 0 drops.
//   - With the fix, emitResidualDrops runs before the early-return -
//     test observes >=1 drop with reason=workflow-cancelled.
func TestAgentRunner_CtxCancelDrop_FirstCallEmitsResidualDrops(t *testing.T) {
	stepID := "step-first-cancel"
	mb := NewInMemoryMailboxStore()
	wake := make(chan struct{}, 1)

	ready := make(chan struct{})
	model := &firstCallCancelModel{mailbox: mb, stepID: stepID, wake: wake, ready: ready}

	var drops []Event
	sink := &mockProgressSink{onEvent: func(_ context.Context, e Event) {
		if e.Type == types.EventMessageDropped {
			drops = append(drops, e)
		}
	}}

	runner := &AgentRunner{
		model:         model,
		stepID:        stepID,
		mailbox:       mb,
		wake:          wake,
		progress:      sink,
		maxWakeCycles: 100, // high cap so we DON'T hit it
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		select {
		case <-ready:
			cancel()
		case <-ctx.Done():
			return
		}
	}()

	_, err := runner.Run(ctx, AgentConfig{}, "start", "m", nil)
	if err == nil {
		t.Fatalf("expected error from cancelled first call, got nil")
	}

	if len(drops) == 0 {
		t.Fatalf("no EventMessageDropped emitted; expected >=1 with reason=workflow-cancelled. mailbox unread=%d, model calls=%d (regression: A5#9 + B5#4 - first-call cancel skipped post-loop drain)",
			len(mb.Unread(stepID)), model.calls)
	}
	for _, d := range drops {
		reason, _ := d.Data["reason"].(string)
		if reason != router.DropReasonWorkflowCancelled.String() {
			t.Errorf("drop reason = %q, want %q", reason, router.DropReasonWorkflowCancelled.String())
		}
	}
	// Mailbox must be drained (MarkRead) - residue should not linger.
	if rem := len(mb.Unread(stepID)); rem != 0 {
		t.Errorf("mailbox unread after drop emit = %d, want 0 (MarkRead missed)", rem)
	}
}

// firstCallCancelModel forces the FIRST DoGenerate call to:
//  1. Append a residue mailbox message
//  2. Signal `ready` so the test can cancel deterministically
//  3. Block on ctx.Done so DoGenerate returns ctx.Err with result==nil
//
// This exercises the early-return path at agent_runner.go ~1070
// (genErr != nil && result == nil) where the post-loop drain was
// previously skipped.
type firstCallCancelModel struct {
	mailbox MailboxStore
	stepID  string
	wake    chan struct{}
	ready   chan struct{}
	calls   int
}

func (m *firstCallCancelModel) ModelID() string { return "first-call-cancel" }
func (m *firstCallCancelModel) DoGenerate(ctx context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	m.calls++
	// Seed the mailbox BEFORE signalling ready - guarantees the residue
	// is observable when the test cancels and the post-loop drain runs.
	_, _ = m.mailbox.Append(m.stepID, RouterMessage{
		From:      "executor",
		Content:   fmt.Sprintf("first-call-residue-%d", m.calls),
		Type:      router.MessageInfo,
		Timestamp: time.Now(),
	})
	select {
	case m.wake <- struct{}{}:
	default:
	}
	// Signal ready (blocking) - test MUST observe before cancelling so
	// the residue is guaranteed in the mailbox when ctx flips.
	select {
	case m.ready <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// Block until ctx cancels. result is still nil at this point (this
	// is call 1), so the runner takes the
	// `if result == nil { return ... }` early-return - which previously
	// skipped the post-loop drain.
	<-ctx.Done()
	return nil, ctx.Err()
}
func (m *firstCallCancelModel) DoStream(ctx context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, fmt.Errorf("not implemented")
}

// cancellableProducerModel deterministically exercises the
// "ctx-cancel mid-loop with mailbox residue" exit path of AgentRunner.Run.
// Call 1: returns a successful result quickly so the runner advances to
// iter=1. Call 2: appends a fresh mailbox message, signals `ready`, then
// blocks on ctx.Done so the caller can cancel deterministically AFTER
// the message is in the mailbox.
type cancellableProducerModel struct {
	mailbox MailboxStore
	stepID  string
	wake    chan struct{}
	ready   chan struct{}
	calls   int
}

func (m *cancellableProducerModel) ModelID() string { return "cancellable-producer" }
func (m *cancellableProducerModel) DoGenerate(ctx context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	m.calls++
	// Append a fresh mailbox residue + fire wake on every call. On call 1
	// this drives the runner into iter=1 (post-iter pendingNow>0 path).
	// On call 2 this seeds the residue that the post-loop drain will
	// observe after we cancel ctx.
	_, _ = m.mailbox.Append(m.stepID, RouterMessage{
		From:      "executor",
		Content:   fmt.Sprintf("residue-call-%d", m.calls),
		Type:      router.MessageInfo,
		Timestamp: time.Now(),
	})
	select {
	case m.wake <- struct{}{}:
	default:
	}
	if m.calls == 1 {
		return &provider.GenerateResult{Text: "first", FinishReason: provider.FinishStop}, nil
	}
	// Call 2+: signal ready (blocking - the test MUST observe it before
	// ctx cancel, so the residue is guaranteed in the mailbox), then
	// block on ctx.Done. result is non-nil at this point (from call 1),
	// so the runner's `if result == nil { return ... }` early-return at
	// agent_runner.go ~1070 is NOT taken; instead it `break`s and reaches
	// the post-loop drain that emits
	// EventMessageDropped{reason:workflow-cancelled}.
	select {
	case m.ready <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func (m *cancellableProducerModel) DoStream(ctx context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Type: provider.ChunkFinish, FinishReason: provider.FinishStop}
	close(ch)
	return &provider.StreamResult{Stream: ch}, nil
}

// noisyProducerModel appends a fresh mailbox message + signals wake on
// every DoGenerate call so the wake loop always has work to do -
// guaranteed to hit the MaxWakeCycles cap. Used by
// TestAgentRunner_MaxWakeCycles_EmitsWarningAndDrops.
type noisyProducerModel struct {
	mailbox *InMemoryMailboxStore
	stepID  string
	wake    chan struct{}
	calls   int
}

func (m *noisyProducerModel) ModelID() string { return "noisy-producer" }
func (m *noisyProducerModel) Capabilities() provider.ModelCapabilities {
	return provider.ModelCapabilities{}
}
func (m *noisyProducerModel) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	m.calls++
	// Append a new message + wake so the next iteration always observes
	// pending - drives the wake loop to its cap.
	_, _ = m.mailbox.Append(m.stepID, RouterMessage{
		From:    "coordinator",
		Content: fmt.Sprintf("msg-call-%d", m.calls),
	})
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return textResult(fmt.Sprintf("r%d", m.calls), 1, 1), nil
}
func (m *noisyProducerModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, fmt.Errorf("not implemented")
}

// TestRunAgent_SendMessageToolInjected - named test.
//
// When AgentRunner.Run is invoked on a runner with a non-nil Router,
// the `send_message` tool MUST be auto-injected into the tool list
// passed to goai.GenerateText so step agents can message the coord
// without the caller having to remember to add the tool. Conversely,
// when Router is nil, send_message MUST NOT be injected (Q4 - the
// no-coord drop is observable but the tool itself should not appear
// when no coord is configured).
//
// Auto-injection is skipped when send_message is already present in
// the caller-supplied tools list (so a caller who wants to override
// the default - e.g. with a custom Description or Execute - wins).
//
// Verification mechanism: the mockModel records every GenerateParams
// it was called with. We inspect params.Tools to confirm a tool named
// "send_message" appears (or doesn't) per the expected matrix.
func TestRunAgent_SendMessageToolInjected(t *testing.T) {
	hasSendMessage := func(params provider.GenerateParams) bool {
		for _, td := range params.Tools {
			if td.Name == "send_message" {
				return true
			}
		}
		return false
	}

	t.Run("router_set_injects", func(t *testing.T) {
		model := &mockModel{
			responses: []*provider.GenerateResult{
				textResult("ok", 1, 1),
			},
		}
		router := NewMessageRouter()
		router.SetMailbox(NewInMemoryMailboxStore())
		runner := &AgentRunner{
			model:  model,
			stepID: "agent-A",
			router: router,
		}
		_, err := runner.Run(t.Context(), AgentConfig{}, "hi", "mock", nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		calls := model.getCalls()
		if len(calls) == 0 {
			t.Fatalf("model not called")
		}
		if !hasSendMessage(calls[0]) {
			t.Errorf("send_message not auto-injected when Router != nil; tools=%v", toolNames(calls[0].Tools))
		}
	})

	t.Run("router_nil_no_inject", func(t *testing.T) {
		model := &mockModel{
			responses: []*provider.GenerateResult{
				textResult("ok", 1, 1),
			},
		}
		runner := &AgentRunner{
			model:  model,
			stepID: "agent-A",
			// Router intentionally nil.
		}
		_, err := runner.Run(t.Context(), AgentConfig{}, "hi", "mock", nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		calls := model.getCalls()
		if len(calls) == 0 {
			t.Fatalf("model not called")
		}
		if hasSendMessage(calls[0]) {
			t.Errorf("send_message auto-injected when Router == nil; tools=%v", toolNames(calls[0].Tools))
		}
	})

	t.Run("caller_provided_send_message_wins", func(t *testing.T) {
		// A caller-supplied send_message must NOT be replaced. We
		// detect this by giving the caller's send_message a unique
		// Description and asserting the tool the model sees still
		// carries that description (not the auto-injected default).
		const sentinel = "CALLER-OVERRIDE-MARKER"
		callerTool := goai.Tool{
			Name:        "send_message",
			Description: sentinel,
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
				return "ok", nil
			},
		}
		model := &mockModel{
			responses: []*provider.GenerateResult{
				textResult("ok", 1, 1),
			},
		}
		router := NewMessageRouter()
		router.SetMailbox(NewInMemoryMailboxStore())
		runner := &AgentRunner{
			model:  model,
			stepID: "agent-A",
			router: router,
		}
		_, err := runner.Run(t.Context(), AgentConfig{}, "hi", "mock", []goai.Tool{callerTool})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		calls := model.getCalls()
		if len(calls) == 0 {
			t.Fatalf("model not called")
		}
		// Count how many send_message tools appear; assert exactly one
		// AND that it's the caller's (sentinel description).
		var sendMessageCount int
		var sawSentinel bool
		for _, td := range calls[0].Tools {
			if td.Name == "send_message" {
				sendMessageCount++
				if td.Description == sentinel {
					sawSentinel = true
				}
			}
		}
		if sendMessageCount != 1 {
			t.Errorf("send_message appears %d times, want exactly 1", sendMessageCount)
		}
		if !sawSentinel {
			t.Errorf("caller-provided send_message was replaced; want sentinel description preserved")
		}
	})

	// coord runner detection. When the caller-supplied tools
	// list contains `forward_to_agent` (a coord-only marker tool),
	// auto-injection of send_message is skipped - coord MUST NOT have
	// send_message because it would self-loop (target="coordinator").
	// Hub-only routing (D-Z1) means steps message coord; coord NEVER
	// messages itself.
	t.Run("coord_runner_no_inject", func(t *testing.T) {
		coordMarkerTool := goai.Tool{
			Name:        "forward_to_agent",
			Description: "coord-only tool",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
				return "ok", nil
			},
		}
		model := &mockModel{
			responses: []*provider.GenerateResult{
				textResult("ok", 1, 1),
			},
		}
		router := NewMessageRouter()
		router.SetMailbox(NewInMemoryMailboxStore())
		runner := &AgentRunner{
			model:  model,
			stepID: "coordinator",
			router: router,
		}
		_, err := runner.Run(t.Context(), AgentConfig{}, "hi", "mock", []goai.Tool{coordMarkerTool})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		calls := model.getCalls()
		if len(calls) == 0 {
			t.Fatalf("model not called")
		}
		if hasSendMessage(calls[0]) {
			t.Errorf("send_message auto-injected into coord runner (forward_to_agent present); coord cannot legally message itself (D-Z1 hub-only). tools=%v", toolNames(calls[0].Tools))
		}
	})
}

// TestNewAgentRunner_Options verifies the NewAgentRunner constructor and
// its AgentRunnerOption helpers. Both the constructor form and the
// struct-literal form must produce equivalent results.
func TestNewAgentRunner_Options(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("hi", 5, 3),
		},
	}

	var events []Event
	progress := &mockProgressSink{
		onEvent: func(_ context.Context, ev Event) { events = append(events, ev) },
	}

	extraOpt := goai.WithMaxSteps(7) // dummy option to verify GoAIOptions round-trips

	r := NewAgentRunner(
		WithRunnerModel(model),
		WithRunnerProgress(progress),
		WithRunnerGoAIOptions(extraOpt),
		WithRunnerRunID("run-42"),
		WithRunnerStepID("step-1"),
		WithRunnerSystemPrompt("you are helpful"),
		WithRunnerModelID("test:model"),
		WithRunnerStreaming(),
		WithRunnerVerbose(),
	)

	// Verify all fields set by options.
	if r.model != model {
		t.Error("Model not set by WithRunnerModel")
	}
	if r.progress != progress {
		t.Error("Progress not set by WithRunnerProgress")
	}
	if len(r.goAIOptions) != 1 {
		t.Errorf("GoAIOptions len=%d want 1", len(r.goAIOptions))
	}
	if r.runID != "run-42" {
		t.Errorf("RunID=%q want run-42", r.runID)
	}
	if r.stepID != "step-1" {
		t.Errorf("StepID=%q want step-1", r.stepID)
	}
	if r.systemPrompt != "you are helpful" {
		t.Errorf("SystemPrompt=%q want 'you are helpful'", r.systemPrompt)
	}
	if r.modelID != "test:model" {
		t.Errorf("ModelID=%q want 'test:model'", r.modelID)
	}
	if !r.streaming {
		t.Error("Streaming not set by WithRunnerStreaming")
	}
	if !r.verbose {
		t.Error("Verbose not set by WithRunnerVerbose")
	}

	// Both forms (constructor vs struct literal) must produce equivalent
	// behavior: run a simple text exchange and verify it returns a result.
	rLiteral := &AgentRunner{
		model:    model,
		progress: progress,
	}
	model2 := &mockModel{responses: []*provider.GenerateResult{textResult("hi", 5, 3)}}
	rLiteral.model = model2

	result, err := rLiteral.Run(t.Context(), AgentConfig{}, "say hi", "test:model", nil)
	if err != nil {
		t.Fatalf("struct-literal runner Run error: %v", err)
	}
	if result.Content != "hi" {
		t.Errorf("struct-literal result.Content=%q want 'hi'", result.Content)
	}
}

// TestNewAgentRunner_WithRunnerTools verifies that WithRunnerTools sets the
// Tools slice on the constructed AgentRunner and that the result is
// equivalent to direct struct-literal initialisation.
func TestNewAgentRunner_WithRunnerTools(t *testing.T) {
	tool := goai.Tool{
		Name:        "fake-tool",
		Description: "fake tool for testing",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	}

	r := NewAgentRunner(WithRunnerTools(tool))

	if len(r.tools) != 1 {
		t.Fatalf("Tools len=%d, want 1", len(r.tools))
	}
	if r.tools[0].Name != "fake-tool" {
		t.Errorf("Tools[0].Name=%q, want %q", r.tools[0].Name, "fake-tool")
	}

	// Equivalence with struct-literal: same tool name and description.
	r2 := &AgentRunner{tools: []goai.Tool{tool}}
	if len(r.tools) != len(r2.tools) {
		t.Errorf("Tools len mismatch: constructor=%d, literal=%d", len(r.tools), len(r2.tools))
	}
	if r.tools[0].Name != r2.tools[0].Name {
		t.Errorf("Tools[0].Name: constructor=%q, literal=%q", r.tools[0].Name, r2.tools[0].Name)
	}
}

// TestNewAgentRunner_WithRunnerPermissions verifies that WithRunnerPermissions
// sets the Permissions field on the constructed AgentRunner.
func TestNewAgentRunner_WithRunnerPermissions(t *testing.T) {
	h := &errorPermissions{} // pre-existing PermissionHandler test stub

	r := NewAgentRunner(WithRunnerPermissions(h))

	if r.permissions != h {
		t.Errorf("Permissions = %v, want %v", r.permissions, h)
	}
}

// TestNewAgentRunner_WithRunnerStateRef verifies that WithRunnerStateRef
// sets the StateRef field on the constructed AgentRunner.
func TestNewAgentRunner_WithRunnerStateRef(t *testing.T) {
	st := &goai.AgentState{}

	r := NewAgentRunner(WithRunnerStateRef(st))

	if r.stateRef != st {
		t.Errorf("StateRef = %p, want %p", r.stateRef, st)
	}
}

// TestNewAgentRunner_WithRunnerTranscript verifies that WithRunnerTranscript
// sets the Transcript field on the constructed AgentRunner.
func TestNewAgentRunner_WithRunnerTranscript(t *testing.T) {
	ts := resume.NewInMemoryTranscriptStore()

	r := NewAgentRunner(WithRunnerTranscript(ts))

	if r.transcript != ts {
		t.Errorf("Transcript = %v, want %v", r.transcript, ts)
	}
}

// TestNewAgentRunner_WithRunnerInitialMessages verifies that
// WithRunnerInitialMessages sets the InitialMessages slice on the
// constructed AgentRunner.
func TestNewAgentRunner_WithRunnerInitialMessages(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Part{{Type: provider.PartText, Text: "hi"}}},
	}

	r := NewAgentRunner(WithRunnerInitialMessages(msgs))

	if len(r.initialMessages) != 1 {
		t.Fatalf("InitialMessages len=%d, want 1", len(r.initialMessages))
	}
	if r.initialMessages[0].Role != provider.RoleUser {
		t.Errorf("InitialMessages[0].Role = %v, want %v", r.initialMessages[0].Role, provider.RoleUser)
	}
}

// TestNewAgentRunner_WithRunnerPreStartDrainGate verifies that
// WithRunnerPreStartDrainGate sets the PreStartDrainGate channel on the
// constructed AgentRunner.
func TestNewAgentRunner_WithRunnerPreStartDrainGate(t *testing.T) {
	gate := make(chan struct{})

	r := NewAgentRunner(WithRunnerPreStartDrainGate(gate))

	if r.preStartDrainGate == nil {
		t.Fatal("PreStartDrainGate is nil")
	}
}

// TestAgentRunner_Getters covers the 4 exported field accessors that
// external SDK consumers use after the field unexported.
func TestAgentRunner_Getters(t *testing.T) {
	model := &mockModel{responses: []*provider.GenerateResult{textResult("ok", 1, 1)}}
	tools := []goai.Tool{{Name: "t1"}, {Name: "t2"}}
	wakeCh := make(chan struct{}, 1)

	r := NewAgentRunner(
		WithRunnerModel(model),
		WithRunnerTools(tools...),
		WithRunnerStepID("step-getter"),
		WithRunnerWake(wakeCh),
	)

	if got := r.Model(); got != model {
		t.Errorf("Model(): got %v want %v", got, model)
	}
	if got := r.Tools(); len(got) != 2 || got[0].Name != "t1" || got[1].Name != "t2" {
		t.Errorf("Tools(): got %v want [t1 t2]", got)
	}
	if got := r.StepID(); got != "step-getter" {
		t.Errorf("StepID(): got %q want 'step-getter'", got)
	}
	if got := r.Wake(); got != wakeCh {
		t.Errorf("Wake(): got %v want %v", got, wakeCh)
	}
}

// TestAgentRunner_SubmitDoneDrop_UsesTargetTerminalReason verifies B8#2:
// when the wake loop exits because submit_result was called (submitDone
// flipped inside OnBeforeToolExecute, then the post-iteration check at
// agent_runner.go ~1129 breaks), residual mailbox messages MUST surface
// as EventMessageDropped{reason:target-terminal} - NOT
// reason:workflow-cancelled. The run terminated cleanly via submit_result;
// labelling the residue as "workflow-cancelled" lies about the run's
// outcome and confuses operators who interpret that reason as "the
// workflow was torn down mid-execution".
//
// Deterministic flow:
//  1. The model's DoGenerate appends a residual mailbox message BEFORE
//     returning the submit_result tool call. By the time AgentRunner
//     finishes processing the response and reaches the
//     `if submitDone.Load() { break }` check, the mailbox holds one
//     unread residue message that was NOT drained into this iteration.
//  2. The submit_result tool call fires OnBeforeToolExecute -> the
//     handler flips submitDone to true.
//  3. Loop hits `if submitDone.Load() { break }` and exits.
//  4. Post-loop drain MUST emit one EventMessageDropped with
//     reason=target-terminal (the run is done, not cancelled, not
//     wake-cap exhausted).
func TestAgentRunner_SubmitDoneDrop_UsesTargetTerminalReason(t *testing.T) {
	stepID := "step-submit-done"
	mb := NewInMemoryMailboxStore()
	wake := make(chan struct{}, 1)

	// Pre-seed one message so the pre-start drain consumes it (unrelated
	// to the residue we want to observe). The residue is appended INSIDE
	// DoGenerate below, after the pre-start drain has already run.
	model := &submitDoneResidueModel{mailbox: mb, stepID: stepID, wake: wake}

	var drops []Event
	sink := &mockProgressSink{onEvent: func(_ context.Context, e Event) {
		if e.Type == types.EventMessageDropped {
			drops = append(drops, e)
		}
	}}

	runner := &AgentRunner{
		model:         model,
		stepID:        stepID,
		mailbox:       mb,
		wake:          wake,
		progress:      sink,
		maxWakeCycles: 100, // high cap so we DON'T hit it
	}

	cfg := AgentConfig{
		MaxTurns: 5,
		ResultSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"answer": map[string]any{"type": "string"}},
			"required":   []any{"answer"},
		},
	}

	_, err := runner.Run(t.Context(), cfg, "do task", "m", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(drops) == 0 {
		t.Fatalf("no EventMessageDropped emitted; expected >=1 with reason=target-terminal. mailbox unread=%d, model calls=%d (submitDone-break path skipped post-loop drain or never seeded residue)",
			len(mb.Unread(stepID)), model.calls)
	}
	for _, d := range drops {
		reason, _ := d.Data["reason"].(string)
		if reason == router.DropReasonWorkflowCancelled.String() {
			t.Errorf("drop reason = %q, want %q (run terminated via submit_result, NOT cancelled)",
				reason, router.DropReasonTargetTerminal.String())
		}
		if reason == router.DropReasonMaxWakeCycles.String() {
			t.Errorf("drop reason = %q, want %q (cap NOT hit; submit_result ended the run)",
				reason, router.DropReasonTargetTerminal.String())
		}
		if reason != router.DropReasonTargetTerminal.String() {
			t.Errorf("drop reason = %q, want %q", reason, router.DropReasonTargetTerminal.String())
		}
	}
	// Mailbox must be drained (MarkRead) - residue should not linger.
	if rem := len(mb.Unread(stepID)); rem != 0 {
		t.Errorf("mailbox unread after drop emit = %d, want 0 (MarkRead missed)", rem)
	}
}

// submitDoneResidueModel deterministically exercises the
// "submitDone-break with mailbox residue" exit path of AgentRunner.Run.
//
// First (and only) DoGenerate call:
//  1. Append a fresh mailbox message - residue that the runner CANNOT
//     have drained yet (we are mid-call; the next drain point would be
//     line ~1184 which is unreachable once submitDone breaks at ~1129).
//  2. Return a submit_result tool call. AgentRunner's OnBeforeToolExecute
//     handler will recognise it, validate against the schema, and flip
//     submitDone -> true. The post-iteration check breaks the loop.
type submitDoneResidueModel struct {
	mailbox MailboxStore
	stepID  string
	wake    chan struct{}
	calls   int
}

func (m *submitDoneResidueModel) ModelID() string { return "submit-done-residue" }
func (m *submitDoneResidueModel) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	m.calls++
	// Append residue BEFORE returning the tool call. Once we return, the
	// runner processes OnBeforeToolExecute (submitDone <- true) and breaks
	// at line ~1129 BEFORE reaching the per-iteration drain at ~1184.
	_, _ = m.mailbox.Append(m.stepID, RouterMessage{
		From:      "executor",
		Content:   fmt.Sprintf("submit-done-residue-%d", m.calls),
		Type:      router.MessageInfo,
		Timestamp: time.Now(),
	})
	// Non-blocking wake - keeps mailboxMode consistent. The runner won't
	// actually consume this signal because submitDone breaks first.
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return toolCallResult("",
		1, 1,
		tc("call-submit", toolNameSubmitResult, json.RawMessage(`{"answer":"done"}`)),
	), nil
}
func (m *submitDoneResidueModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, fmt.Errorf("not implemented")
}
