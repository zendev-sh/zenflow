package exec

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/types"
)

func TestAgentToolDef(t *testing.T) {
	def := AgentToolDef()
	if def.Name != "agent" {
		t.Errorf("name = %q, want %q", def.Name, "agent")
	}
	if def.Description == "" {
		t.Error("description is empty")
	}
	// InputSchema should be valid JSON.
	var schema map[string]any
	if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
		t.Fatalf("invalid InputSchema JSON: %v", err)
	}
	// Should have required fields.
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatal("missing required field in schema")
	}
	hasName := false
	hasInstructions := false
	for _, r := range required {
		switch r.(string) {
		case "name":
			hasName = true
		case "instructions":
			hasInstructions = true
		}
	}
	if !hasName || !hasInstructions {
		t.Errorf("required = %v, want [name, instructions]", required)
	}
}

func TestAgentSpawner_SyncChild(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("child result", 10, 5),
		},
	}
	router := NewMessageRouter()

	spawner := &agentSpawner{
		Model:        model,
		Tools:        []goai.Tool{makeTool("read_file", "read a file", "content")},
		Router:       router,
		DefaultModel: "gpt-4o",
		MaxDepth:     3,
		CurrentDepth: 0,
	}

	args, _ := json.Marshal(agentToolParams{
		Name:         "child-1",
		Instructions: "Do something",
		Description:  "A helper agent",
	})

	result, err := spawner.SpawnChild(t.Context(), provider.ToolCall{
		ID:    "call-1",
		Name:  "agent",
		Input: args,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "child result" {
		t.Errorf("content = %q, want %q", result, "child result")
	}

	// Verify child was recorded.
	spawner.mu.Lock()
	if len(spawner.children) != 1 {
		t.Errorf("children count = %d, want 1", len(spawner.children))
	}
	spawner.mu.Unlock()
}

func TestAgentSpawner_AsyncChild(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("async result", 5, 3),
		},
	}
	router := NewMessageRouter()

	spawner := &agentSpawner{
		Model:        model,
		Router:       router,
		DefaultModel: "gpt-4o",
		MaxDepth:     3,
		CurrentDepth: 0,
	}

	args, _ := json.Marshal(agentToolParams{
		Name:            "async-child",
		Instructions:    "Do something async",
		RunInBackground: true,
	})

	result, err := spawner.SpawnChild(t.Context(), provider.ToolCall{
		ID:    "call-2",
		Name:  "agent",
		Input: args,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Async should return immediately with "launched" message.
	if !strings.Contains(result, "launched") {
		t.Errorf("content = %q, want to contain 'launched'", result)
	}

	// Wait for the async child to complete.
	spawner.childWg.Wait()

	// Child result should be recorded.
	spawner.mu.Lock()
	if len(spawner.children) != 1 {
		t.Errorf("children count = %d, want 1", len(spawner.children))
	}
	if spawner.children[0].Content != "async result" {
		t.Errorf("child output = %q, want %q", spawner.children[0].Content, "async result")
	}
	spawner.mu.Unlock()
}

func TestAgentSpawner_MaxDepth(t *testing.T) {
	router := NewMessageRouter()

	spawner := &agentSpawner{
		Model:        &mockModel{},
		Router:       router,
		DefaultModel: "gpt-4o",
		MaxDepth:     2,
		CurrentDepth: 2, // Already at max depth.
	}

	args, _ := json.Marshal(agentToolParams{
		Name:         "too-deep",
		Instructions: "Should fail",
	})

	result, err := spawner.SpawnChild(t.Context(), provider.ToolCall{
		ID:    "call-3",
		Name:  "agent",
		Input: args,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "max agent depth") {
		t.Errorf("content = %q, want to contain 'max agent depth'", result)
	}
}

func TestAgentSpawner_InvalidParams(t *testing.T) {
	router := NewMessageRouter()

	spawner := &agentSpawner{
		Model:        &mockModel{},
		Router:       router,
		DefaultModel: "gpt-4o",
		MaxDepth:     3,
	}

	result, err := spawner.SpawnChild(t.Context(), provider.ToolCall{
		ID:    "call-4",
		Name:  "agent",
		Input: json.RawMessage(`{invalid json`),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "invalid agent params") {
		t.Errorf("content = %q, want to contain 'invalid agent params'", result)
	}
}

func TestAgentSpawner_RecursiveSpawn(t *testing.T) {
	var callMu sync.Mutex
	callCount := 0

	model := &sequentialMockModel{
		fn: func(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
			callMu.Lock()
			n := callCount
			callCount++
			callMu.Unlock()

			if n == 0 {
				args, _ := json.Marshal(agentToolParams{
					Name:         "child",
					Instructions: "spawn grandchild",
				})
				return toolCallResult("", 10, 5,
					tc("tc-1", "agent", args)), nil
			}
			if n == 1 {
				args, _ := json.Marshal(agentToolParams{
					Name:         "grandchild",
					Instructions: "just answer",
				})
				return toolCallResult("", 8, 4,
					tc("tc-2", "agent", args)), nil
			}
			if n == 2 {
				return textResult("grandchild done", 5, 3), nil
			}
			if n == 3 {
				return textResult("child done with grandchild", 6, 3), nil
			}
			return textResult("primary done", 7, 4), nil
		},
	}

	router := NewMessageRouter()

	spawner := &agentSpawner{
		Model:        model,
		Router:       router,
		DefaultModel: "gpt-4o",
		MaxDepth:     3,
		CurrentDepth: 0,
	}

	// chan-Inbox path removed - child spawn paths in
	// the standalone agentSpawner have no inter-agent inbox today.
	runner := &AgentRunner{
		model:   model,
		spawner: spawner,
	}

	tools := []goai.Tool{AgentToolDef()}
	result, err := runner.Run(t.Context(), AgentConfig{MaxTurns: 10}, "start", "gpt-4o", tools)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = router // retained for spawner construction; no per-agent inbox

	if result.Content != "primary done" {
		t.Errorf("content = %q, want %q", result.Content, "primary done")
	}

	callMu.Lock()
	totalCalls := callCount
	callMu.Unlock()
	if totalCalls < 3 {
		t.Errorf("total LLM calls = %d, want >= 3 (primary + child + grandchild)", totalCalls)
	}
}

func TestAgentSpawner_ChildUsesCustomModel(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("child with custom model", 5, 3),
		},
	}
	router := NewMessageRouter()

	spawner := &agentSpawner{
		Model:        model,
		Router:       router,
		DefaultModel: "gpt-4o",
		MaxDepth:     3,
		CurrentDepth: 0,
	}

	args, _ := json.Marshal(agentToolParams{
		Name:         "custom-model-child",
		Instructions: "Do something",
		Model:        "claude-4-sonnet",
	})

	_, err := spawner.SpawnChild(t.Context(), provider.ToolCall{
		ID:    "call-5",
		Name:  "agent",
		Input: args,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The child still uses the same model object (model field routing is at the spawner level).
	calls := model.getCalls()
	if len(calls) < 1 {
		t.Fatalf("model calls = %d, want >= 1", len(calls))
	}
}

func TestAgentSpawner_ChildWithPrompt(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("answered", 5, 3),
		},
	}
	router := NewMessageRouter()

	spawner := &agentSpawner{
		Model:        model,
		Router:       router,
		DefaultModel: "gpt-4o",
		MaxDepth:     3,
		CurrentDepth: 0,
	}

	args, _ := json.Marshal(agentToolParams{
		Name:         "prompted-child",
		Instructions: "Write tests",
		Prompt:       "You are a test engineer",
	})

	_, err := spawner.SpawnChild(t.Context(), provider.ToolCall{
		ID:    "call-6",
		Name:  "agent",
		Input: args,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// : subagent role (cfg.Prompt) now flows to system slot via
	// goai.WithSystem. The user message carries ONLY the task
	// instructions; role text must NOT appear there.
	calls := model.getCalls()
	if len(calls) < 1 {
		t.Fatalf("model calls = %d, want >= 1", len(calls))
	}
	if calls[0].System != "You are a test engineer" {
		t.Errorf("system prompt: got %q, want %q", calls[0].System, "You are a test engineer")
	}
	msgs := calls[0].Messages
	if len(msgs) == 0 {
		t.Fatal("no messages in call")
	}
	userText := ""
	for _, p := range msgs[0].Content {
		if p.Type == provider.PartText {
			userText = p.Text
		}
	}
	if strings.Contains(userText, "You are a test engineer") {
		t.Errorf("user message leaked system role: %q", userText)
	}
	if strings.Contains(userText, "## Agent Role") {
		t.Errorf("'## Agent Role' header should not appear after Z.7.3 migration")
	}
	if !strings.Contains(userText, "Write tests") {
		t.Errorf("user message missing instructions: %q", userText)
	}
}

// --- RunAgent tests ---

func TestRunAgent_Simple(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("hello world", 10, 5),
		},
	}

	o := New(
		WithModel(model),
		WithDefaultModel("gpt-4o"),
	)

	result, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "say hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "hello world" {
		t.Errorf("content = %q, want %q", result.Content, "hello world")
	}
	if result.Tokens.InputTokens != 10 {
		t.Errorf("input tokens = %d, want 10", result.Tokens.InputTokens)
	}
	if result.Turns < 1 {
		t.Errorf("turns = %d, want >= 1", result.Turns)
	}
}

func TestRunAgent_SpawnSyncChild(t *testing.T) {
	var callMu sync.Mutex
	callCount := 0

	model := &sequentialMockModel{
		fn: func(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
			callMu.Lock()
			n := callCount
			callCount++
			callMu.Unlock()

			if n == 0 {
				args, _ := json.Marshal(agentToolParams{
					Name:         "helper",
					Instructions: "help me",
				})
				return toolCallResult("", 10, 5,
					tc("tc-1", "agent", args)), nil
			}
			if n == 1 {
				return textResult("child helped", 8, 4), nil
			}
			return textResult("all done with child help", 12, 6), nil
		},
	}

	o := New(
		WithModel(model),
		WithDefaultModel("gpt-4o"),
	)

	result, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "do task with helper"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "all done with child help" {
		t.Errorf("content = %q, want %q", result.Content, "all done with child help")
	}

	// Tokens should include child's tokens.
	if result.Tokens.InputTokens < 28 {
		t.Errorf("total input tokens = %d, want >= 28", result.Tokens.InputTokens)
	}
}

func TestRunAgent_SpawnAsyncChild(t *testing.T) {
	// Per-agent counters: the primary and the async-child share the
	// same sequentialMockModel.fn but execute on different goroutines.
	// The pre-fix version used a single shared callCount which raced
	// - a child's first call could win position 1 and pre-empt the
	// primary's second call (the "primary done" reply), causing the
	// primary to silently be assigned the "async child done" text.
	// Route by inspecting the user prompt: distinct for primary vs
	// child, eliminating the cross-goroutine call ordering race.
	var primaryCalls atomic.Int32

	model := &sequentialMockModel{
		fn: func(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
			isChild := false
			for _, m := range params.Messages {
				if m.Role != provider.RoleUser {
					continue
				}
				for _, p := range m.Content {
					if p.Type == provider.PartText && strings.Contains(p.Text, "do background work") {
						isChild = true
					}
				}
			}
			if isChild {
				return textResult("async child done", 5, 3), nil
			}
			n := primaryCalls.Add(1) - 1
			if n == 0 {
				args, _ := json.Marshal(agentToolParams{
					Name:            "background-worker",
					Instructions:    "do background work",
					RunInBackground: true,
				})
				return toolCallResult("", 10, 5,
					tc("tc-1", "agent", args)), nil
			}
			return textResult("primary done, async child running", 8, 4), nil
		},
	}

	o := New(
		WithModel(model),
		WithDefaultModel("gpt-4o"),
	)

	result, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "do with async help"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "primary done, async child running" {
		t.Errorf("content = %q", result.Content)
	}
}

func TestRunAgent_MaxDepth(t *testing.T) {
	var callMu sync.Mutex
	callCount := 0

	model := &sequentialMockModel{
		fn: func(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
			callMu.Lock()
			n := callCount
			callCount++
			callMu.Unlock()

			if n < 5 {
				args, _ := json.Marshal(agentToolParams{
					Name:         "deep-child",
					Instructions: "go deeper",
				})
				return toolCallResult("", 5, 3,
					tc("tc", "agent", args)), nil
			}
			return textResult("bottom reached", 5, 3), nil
		},
	}

	o := New(
		WithModel(model),
		WithDefaultModel("gpt-4o"),
		WithMaxDepth(2),
	)

	result, err := o.RunAgent(t.Context(), AgentConfig{Prompt: "go deep"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should complete without error - depth limit returns a message.
	if result.Content == "" {
		t.Error("expected non-empty content")
	}
}

func TestAgentSpawner_MaxChildren(t *testing.T) {
	router := NewMessageRouter()

	spawner := &agentSpawner{
		Model:        &mockModel{},
		Router:       router,
		DefaultModel: "gpt-4o",
		MaxDepth:     3,
		MaxChildren:  2,
	}

	// Spawn 2 children successfully.
	for i := 0; i < 2; i++ {
		args, _ := json.Marshal(agentToolParams{
			Name:         "child",
			Instructions: "do",
		})
		_, err := spawner.SpawnChild(t.Context(), provider.ToolCall{
			ID: "tc", Name: "agent", Input: args,
		})
		if err != nil {
			t.Fatalf("spawn %d: %v", i, err)
		}
	}

	// Third should be rejected.
	args, _ := json.Marshal(agentToolParams{
		Name:         "extra",
		Instructions: "do",
	})
	result, err := spawner.SpawnChild(t.Context(), provider.ToolCall{
		ID: "tc", Name: "agent", Input: args,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "max children") {
		t.Errorf("result = %q, want max children message", result)
	}
}

func TestAgentSpawner_AsyncChildError(t *testing.T) {
	model := &mockModel{
		err: errors.New("child LLM failed"),
	}
	router := NewMessageRouter()
	var errorCount atomic.Int32

	spawner := &agentSpawner{
		Model:        model,
		Router:       router,
		DefaultModel: "gpt-4o",
		MaxDepth:     3,
		Progress: &mockProgressSink{
			onEvent: func(_ context.Context, e Event) {
				if e.Type == types.EventError {
					errorCount.Add(1)
				}
			},
		},
	}

	args, _ := json.Marshal(agentToolParams{
		Name:            "failing-async",
		Instructions:    "do",
		RunInBackground: true,
	})
	_, err := spawner.SpawnChild(t.Context(), provider.ToolCall{
		ID: "tc", Name: "agent", Input: args,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	spawner.childWg.Wait()

	// Error should be stored in childErrors.
	spawner.mu.Lock()
	errCount := len(spawner.childErrors)
	spawner.mu.Unlock()
	if errCount != 1 {
		t.Errorf("childErrors = %d, want 1", errCount)
	}
}

func TestAgentSpawner_ChildModelWarning(t *testing.T) {
	var warned atomic.Bool
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("done", 5, 3),
		},
	}
	router := NewMessageRouter()

	spawner := &agentSpawner{
		Model:        model,
		Router:       router,
		DefaultModel: "gpt-4o",
		MaxDepth:     3,
		Progress: &mockProgressSink{
			onEvent: func(_ context.Context, e Event) {
				if e.Type == types.EventMessage {
					warned.Store(true)
				}
			},
		},
	}

	args, _ := json.Marshal(agentToolParams{
		Name:         "child",
		Instructions: "do",
		Model:        "different-model",
	})
	_, err := spawner.SpawnChild(t.Context(), provider.ToolCall{
		ID: "tc", Name: "agent", Input: args,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !warned.Load() {
		t.Error("expected model warning event")
	}
}

func TestFilterTools(t *testing.T) {
	tools := []goai.Tool{
		makeTool("read", "read", ""),
		makeTool("write", "write", ""),
		makeTool("bash", "bash", ""),
	}

	// Allow only read and bash.
	filtered := FilterTools(tools, []string{"read", "bash"}, nil)
	if len(filtered) != 2 {
		t.Fatalf("filtered = %d, want 2", len(filtered))
	}

	// Disallow bash.
	filtered = FilterTools(tools, nil, []string{"bash"})
	if len(filtered) != 2 {
		t.Fatalf("filtered = %d, want 2", len(filtered))
	}
	for _, tool := range filtered {
		if tool.Name == "bash" {
			t.Error("bash should be filtered out")
		}
	}

	// Allow + disallow.
	filtered = FilterTools(tools, []string{"read", "bash"}, []string{"bash"})
	if len(filtered) != 1 || filtered[0].Name != "read" {
		t.Errorf("filtered = %v, want [read]", filtered)
	}
}

func TestIntersectTools(t *testing.T) {
	// Empty allowed = return requested.
	result := intersectTools([]string{"a", "b"}, nil)
	if len(result) != 2 {
		t.Errorf("intersect empty allowed: got %v", result)
	}

	// Empty requested = nil.
	result = intersectTools(nil, []string{"a"})
	if result != nil {
		t.Errorf("intersect empty requested: got %v", result)
	}

	// Intersection.
	result = intersectTools([]string{"a", "b", "c"}, []string{"b", "c", "d"})
	if len(result) != 2 {
		t.Errorf("intersect: got %v, want [b, c]", result)
	}
}
