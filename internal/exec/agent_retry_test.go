package exec

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
)

// TestZFB3_AgentRunner_RetriesSubmitResultOnMiss is the regression test.
// It simulates an LLM that finishes text-only on the first call (the exact
// failure mode seen in 13/168 E2E cases) and verifies the retry
// path forces a submit_result call via ToolChoice=required + isolated tools.
func TestZFB3_AgentRunner_RetriesSubmitResultOnMiss(t *testing.T) {
	var callCount int32
	model := &sequentialMockModel{
		fn: func(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
			n := atomic.AddInt32(&callCount, 1)
			if n == 1 {
 // Primary call: LLM finishes text-only (the bug we're fixing).
 // Verify tools include the distractor "read" so we know this
 // is the primary (pre-retry) call.
				if !hasToolDefinition(params.Tools, "read") {
					t.Errorf("primary call missing distractor 'read' tool")
				}
				if !hasToolDefinition(params.Tools, "submit_result") {
					t.Errorf("primary call missing 'submit_result' tool")
				}
				if params.ToolChoice != "" && params.ToolChoice != "auto" {
					t.Errorf("primary call ToolChoice = %q, want auto/empty", params.ToolChoice)
				}
				return &provider.GenerateResult{
					Text:         "here is my final answer, but I forgot to call the tool",
					FinishReason: provider.FinishStop,
				}, nil
			}
 // Retry call: must have ONLY submit_result tool and ToolChoice=required.
			if hasToolDefinition(params.Tools, "read") {
				t.Errorf("retry call must NOT include distractor 'read' tool")
			}
			if !hasToolDefinition(params.Tools, "submit_result") {
				t.Errorf("retry call missing 'submit_result' tool")
			}
			if params.ToolChoice != "required" {
				t.Errorf("retry call ToolChoice = %q, want 'required'", params.ToolChoice)
			}
 // The retry reminder should appear as the last user message.
			if len(params.Messages) == 0 {
				t.Fatal("retry call has no messages")
			}
			last := params.Messages[len(params.Messages)-1]
			if last.Role != provider.RoleUser {
				t.Errorf("retry last message role = %q, want user", last.Role)
			}
			var lastText string
			for _, p := range last.Content {
				if p.Type == provider.PartText {
					lastText += p.Text
				}
			}
			if !strings.Contains(lastText, "submit_result") {
				t.Errorf("retry reminder missing 'submit_result' mention: %q", lastText)
			}
 // On the retry, the model calls submit_result correctly.
			return &provider.GenerateResult{
				ToolCalls: []provider.ToolCall{
					{ID: "c1", Name: "submit_result", Input: json.RawMessage(`{"done":true,"answer":"42"}`)},
				},
				FinishReason: provider.FinishToolCalls,
			}, nil
		},
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"done":   map[string]any{"type": "boolean"},
			"answer": map[string]any{"type": "string"},
		},
		"required": []string{"done", "answer"},
	}

	runner := &AgentRunner{model: model}
	cfg := AgentConfig{ResultSchema: schema}
	// Include a distractor tool so the primary call has something besides submit_result.
	tools := []goai.Tool{
		makeTool("read", "read a file", "file contents"),
	}

	res, err := runner.Run(t.Context(), cfg, "please answer", "mock-model", tools)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if res == nil {
		t.Fatal("Run returned nil result")
	}
	if res.Result == nil {
		t.Fatal("Result map is nil - submit_result was never captured")
	}
	if done, _ := res.Result["done"].(bool); !done {
		t.Errorf("Result.done = %v, want true", done)
	}
	if ans, _ := res.Result["answer"].(string); ans != "42" {
		t.Errorf("Result.answer = %q, want %q", ans, "42")
	}
	if atomic.LoadInt32(&callCount) != 2 {
		t.Errorf("model.DoGenerate called %d times, want 2 (primary + retry)", callCount)
	}
}

// TestZFB3_AgentRunner_PrimaryCallsSubmitResult_NoRetry verifies the happy
// path: when the LLM calls submit_result on its own, the retry is NOT
// triggered (avoiding unnecessary API cost).
func TestZFB3_AgentRunner_PrimaryCallsSubmitResult_NoRetry(t *testing.T) {
	var callCount int32
	model := &sequentialMockModel{
		fn: func(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
			atomic.AddInt32(&callCount, 1)
			return &provider.GenerateResult{
				ToolCalls: []provider.ToolCall{
					{ID: "c1", Name: "submit_result", Input: json.RawMessage(`{"done":true}`)},
				},
				FinishReason: provider.FinishToolCalls,
			}, nil
		},
	}

	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"done": map[string]any{"type": "boolean"}},
		"required":   []string{"done"},
	}

	runner := &AgentRunner{model: model}
	cfg := AgentConfig{ResultSchema: schema}

	res, err := runner.Run(t.Context(), cfg, "do it", "mock-model", nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if res.Result == nil || res.Result["done"] != true {
		t.Errorf("Result = %+v, want {done:true}", res.Result)
	}
	// Only the primary call. No retry.
	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("model.DoGenerate called %d times, want exactly 1", got)
	}
}

// TestZFB3_AgentRunner_RetryAlsoFails_IncludesLastAssistantText verifies
// when both the primary and retry fail to produce submit_result, the
// returned error includes the last assistant text for debugging.
func TestZFB3_AgentRunner_RetryAlsoFails_IncludesLastAssistantText(t *testing.T) {
	var callCount int32
	const lastMsg = "I really do not want to call this tool"
	model := &sequentialMockModel{
		fn: func(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
			atomic.AddInt32(&callCount, 1)
 // Both primary and retry: return text, no tool calls.
			return &provider.GenerateResult{
				Text:         lastMsg,
				FinishReason: provider.FinishStop,
			}, nil
		},
	}

	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"done": map[string]any{"type": "boolean"}},
		"required":   []string{"done"},
	}

	runner := &AgentRunner{model: model}
	cfg := AgentConfig{ResultSchema: schema}

	_, err := runner.Run(t.Context(), cfg, "do it", "mock-model", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "submit_result") {
		t.Errorf("error = %q, want to mention submit_result", err)
	}
	if !strings.Contains(err.Error(), lastMsg) {
		t.Errorf("error = %q, want to include last assistant text %q", err, lastMsg)
	}
	// Primary + retry = 2 calls.
	if got := atomic.LoadInt32(&callCount); got < 2 {
		t.Errorf("model.DoGenerate called %d times, want ≥2 (primary + retry)", got)
	}
}

// hasToolDefinition reports whether a []provider.ToolDefinition contains a tool by name.
func hasToolDefinition(tools []provider.ToolDefinition, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}
