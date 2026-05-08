package exec

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/types"
)

// eventCollector collects events from ProgressSink for testing.
type eventCollector struct {
	mu     sync.Mutex
	events []Event
}

func (c *eventCollector) OnEvent(_ context.Context, event Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

func (c *eventCollector) OnOutput(_ context.Context, _ Output) {}

func (c *eventCollector) Events() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]Event, len(c.events))
	copy(cp, c.events)
	return cp
}

func (c *eventCollector) ByType(t EventType) []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Event
	for _, e := range c.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// toolCallModel returns tool calls on first DoGenerate, then completes.
type toolCallModel struct {
	mu   sync.Mutex
	turn int
}

func (m *toolCallModel) ModelID() string { return "toolcall-mock" }

func (m *toolCallModel) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	m.mu.Lock()
	m.turn++
	n := m.turn
	m.mu.Unlock()

	if n == 1 {
		return &provider.GenerateResult{
			Text: "calling tool",
			ToolCalls: []provider.ToolCall{
				{ID: "tc-1", Name: "echo", Input: json.RawMessage(`{"text":"hello"}`)},
			},
			Usage:        provider.Usage{InputTokens: 20, OutputTokens: 10},
			FinishReason: provider.FinishToolCalls,
		}, nil
	}
	return &provider.GenerateResult{
		Text:         "done after tool call",
		Usage:        provider.Usage{InputTokens: 30, OutputTokens: 15},
		FinishReason: provider.FinishStop,
	}, nil
}

func (m *toolCallModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, nil
}

func TestAgentRunner_EmitsAgentTurnEvents(t *testing.T) {
	collector := &eventCollector{}
	model := &toolCallModel{}
	echoTool := makeTool("echo", "Echo tool", "echoed: hello")

	runner := &AgentRunner{
		model:    model,
		tools:    []goai.Tool{echoTool},
		progress: collector,
	}

	cfg := AgentConfig{MaxTurns: 10}
	tools := []goai.Tool{echoTool}

	_, err := runner.Run(t.Context(), cfg, "test prompt", "model-1", tools)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should emit EventAgentTurn for each LLM call (request + response per turn).
	turnEvents := collector.ByType(types.EventAgentTurn)
	// 2 LLM calls × 2 events (request+response) = at least 4 events.
	if len(turnEvents) < 4 {
		t.Errorf("expected at least 4 agent_turn events (request+response per turn), got %d", len(turnEvents))
	}

	// Response events should have token usage.
	for _, e := range turnEvents {
		if e.Data != nil && e.Data["phase"] == "response" && e.Tokens == nil {
			t.Errorf("response event missing Tokens: %v", e.Data)
		}
	}
}

func TestAgentRunner_EmitsToolCallEvents(t *testing.T) {
	collector := &eventCollector{}
	model := &toolCallModel{}
	echoTool := makeTool("echo", "Echo tool", "echoed: hello")

	runner := &AgentRunner{
		model:    model,
		tools:    []goai.Tool{echoTool},
		progress: collector,
	}

	cfg := AgentConfig{MaxTurns: 10}
	tools := []goai.Tool{echoTool}

	_, err := runner.Run(t.Context(), cfg, "test", "model-1", tools)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should emit EventToolCall start + end for the echo tool.
	toolEvents := collector.ByType(types.EventToolCall)
	if len(toolEvents) < 2 {
		t.Errorf("expected at least 2 tool_call events (start+end), got %d", len(toolEvents))
	}

	hasStart := false
	hasEnd := false
	for _, e := range toolEvents {
		if e.Data != nil && e.Data["tool_name"] == "echo" {
			if e.Data["phase"] == "start" {
				hasStart = true
			}
			if e.Data["phase"] == "end" {
				hasEnd = true
			}
		}
	}
	if !hasStart {
		t.Error("missing tool_call start event")
	}
	if !hasEnd {
		t.Error("missing tool_call end event")
	}
}

func TestAgentRunner_NoEventsWithoutProgress(t *testing.T) {
	model := &toolCallModel{}
	echoTool := makeTool("echo", "Echo tool", "echoed: hello")

	runner := &AgentRunner{
		model: model,
		tools: []goai.Tool{echoTool},
 // No Progress set - should not panic.
	}

	cfg := AgentConfig{MaxTurns: 10}
	tools := []goai.Tool{echoTool}

	_, err := runner.Run(t.Context(), cfg, "test", "model-1", tools)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestAgentRunner_ToolErrorWithProgress(t *testing.T) {
	collector := &eventCollector{}
	model := &mockModel{
		responses: []*provider.GenerateResult{
			toolCallResult("", 10, 5,
				tc("c1", "fail", json.RawMessage(`{}`))),
			textResult("handled", 10, 5),
		},
	}
	failTool := makeErrorTool("fail", "a failing tool")

	runner := &AgentRunner{
		model:    model,
		tools:    []goai.Tool{failTool},
		progress: collector,
	}

	_, err := runner.Run(t.Context(), AgentConfig{}, "do", "model-1", []goai.Tool{failTool})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should have a tool_call end event with an error.
	toolEvents := collector.ByType(types.EventToolCall)
	hasErrorEvent := false
	for _, e := range toolEvents {
		if e.Error != nil {
			hasErrorEvent = true
		}
	}
	if !hasErrorEvent {
		t.Error("expected tool_call event with error")
	}
}

func TestAgentRunner_SpawnerToolCallEvents(t *testing.T) {
	collector := &eventCollector{}

	parentModel := &mockModel{
		responses: []*provider.GenerateResult{
			toolCallResult("spawning agent", 20, 10,
				tc("tc-agent", "agent", json.RawMessage(`{"name":"child","instructions":"do stuff"}`))),
			textResult("parent done", 30, 15),
		},
	}

	router := NewMessageRouter()
	sp := &agentSpawner{
		Model:        parentModel,
		Progress:     collector,
		Router:       router,
		DefaultModel: "model-1",
		MaxDepth:     3,
		MaxTurns:     10,
	}

	// chan-Inbox path removed.
	_ = router

	runner := &AgentRunner{
		model:    parentModel,
		progress: collector,
		spawner:  sp,
	}

	cfg := AgentConfig{MaxTurns: 10}
	tools := []goai.Tool{AgentToolDef()}

	_, err := runner.Run(t.Context(), cfg, "test spawn", "model-1", tools)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Check for tool call events for the "agent" tool.
	toolEvents := collector.ByType(types.EventToolCall)
	hasAgentStart := false
	hasAgentEnd := false
	for _, e := range toolEvents {
		if e.Data != nil && e.Data["tool_name"] == "agent" {
			if e.Data["phase"] == "start" {
				hasAgentStart = true
			}
			if e.Data["phase"] == "end" {
				hasAgentEnd = true
			}
		}
	}
	if !hasAgentStart {
		t.Error("missing agent tool_call start event")
	}
	if !hasAgentEnd {
		t.Error("missing agent tool_call end event")
	}
}

func TestAgentRunner_SubmitResultToolCallEvents(t *testing.T) {
	collector := &eventCollector{}

	model := &mockModel{
		responses: []*provider.GenerateResult{
			toolCallResult("submitting", 20, 10,
				tc("tc-submit", "submit_result", json.RawMessage(`{"done": true}`))),
		},
	}

	runner := &AgentRunner{
		model:    model,
		progress: collector,
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"done": map[string]any{"type": "boolean"},
		},
	}

	cfg := AgentConfig{
		MaxTurns:     10,
		ResultSchema: schema,
	}

	result, err := runner.Run(t.Context(), cfg, "submit", "model-1", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Result == nil {
		t.Fatal("expected non-nil result")
	}

	// Check for submit_result tool call events.
	toolEvents := collector.ByType(types.EventToolCall)
	hasSubmitStart := false
	hasSubmitEnd := false
	for _, e := range toolEvents {
		if e.Data != nil && e.Data["tool_name"] == "submit_result" {
			if e.Data["phase"] == "start" {
				hasSubmitStart = true
			}
			if e.Data["phase"] == "end" {
				hasSubmitEnd = true
			}
		}
	}
	if !hasSubmitStart {
		t.Error("missing submit_result tool_call start event")
	}
	if !hasSubmitEnd {
		t.Error("missing submit_result tool_call end event")
	}
}

func TestAgentRunner_TurnEventHasCorrectTokens(t *testing.T) {
	collector := &eventCollector{}
	model := &toolCallModel{}
	echoTool := makeTool("echo", "Echo tool", "echoed: hello")

	runner := &AgentRunner{
		model:    model,
		tools:    []goai.Tool{echoTool},
		progress: collector,
	}

	cfg := AgentConfig{MaxTurns: 10}
	tools := []goai.Tool{echoTool}

	_, err := runner.Run(t.Context(), cfg, "test", "model-1", tools)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Find the first response event (has tokens).
	var responseEvent *Event
	for _, e := range collector.Events() {
		if e.Type == types.EventAgentTurn && e.Data != nil && e.Data["phase"] == "response" && e.Tokens != nil {
			responseEvent = &e
			break
		}
	}
	if responseEvent == nil {
		t.Fatal("no response turn event with tokens found")
	}

	// First response should have InputTokens=20, OutputTokens=10.
	if responseEvent.Tokens.InputTokens != 20 {
		t.Errorf("turn 1 InputTokens = %d, want 20", responseEvent.Tokens.InputTokens)
	}
	if responseEvent.Tokens.OutputTokens != 10 {
		t.Errorf("turn 1 OutputTokens = %d, want 10", responseEvent.Tokens.OutputTokens)
	}
}
