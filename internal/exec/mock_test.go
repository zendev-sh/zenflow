package exec

// mock_test.go provides shared test mocks for all unit tests in the zenflow package.
// Key types:
// - mockModel: implements provider.LanguageModel (DoGenerate, DoStream, ModelID)
// - mockTool: helper to create goai.Tool instances for tests
// - sequentialMockModel: function-based model for complex scenarios

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
)

// --- mockModel: thread-safe mock implementing provider.LanguageModel ---

// mockModel is a thread-safe mock LLM that implements provider.LanguageModel.
// It pops responses from a pre-configured list on each DoGenerate call.
type mockModel struct {
	mu        sync.Mutex
	id        string
	responses []*provider.GenerateResult
	calls     []provider.GenerateParams // recorded calls for assertions
	idx       int
	err       error // if set, DoGenerate always returns this error
}

func (m *mockModel) ModelID() string {
	if m.id != "" {
		return m.id
	}
	return "mock-model"
}

func (m *mockModel) DoGenerate(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, params)
	if m.err != nil {
		return nil, m.err
	}
	if m.idx >= len(m.responses) {
		return &provider.GenerateResult{
			Text:         "done",
			FinishReason: provider.FinishStop,
		}, nil
	}
	resp := m.responses[m.idx]
	m.idx++
	return resp, nil
}

func (m *mockModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, errors.New("streaming not implemented in mock")
}

// getCalls returns a copy of recorded calls (thread-safe).
func (m *mockModel) getCalls() []provider.GenerateParams {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]provider.GenerateParams, len(m.calls))
	copy(cp, m.calls)
	return cp
}

// --- sequentialMockModel: function-based mock for complex multi-turn tests ---

type sequentialMockModel struct {
	fn func(ctx context.Context, params provider.GenerateParams) (*provider.GenerateResult, error)
}

func (m *sequentialMockModel) ModelID() string { return "sequential-mock" }

func (m *sequentialMockModel) DoGenerate(ctx context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
	return m.fn(ctx, params)
}

func (m *sequentialMockModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, errors.New("streaming not implemented in mock")
}

// --- blockingModel: blocks until context cancelled ---

type blockingModel struct {
	mu    sync.Mutex
	calls int
}

func (b *blockingModel) ModelID() string { return "blocking-mock" }

func (b *blockingModel) DoGenerate(ctx context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b *blockingModel) DoStream(ctx context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// --- streamingMockModel: supports DoStream for streaming tests ---

type streamingMockModel struct {
	mu        sync.Mutex
	responses []*provider.GenerateResult
	calls     []provider.GenerateParams
	idx       int
}

func (m *streamingMockModel) ModelID() string { return "streaming-mock" }

func (m *streamingMockModel) DoGenerate(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, params)
	if m.idx >= len(m.responses) {
		return &provider.GenerateResult{Text: "done", FinishReason: provider.FinishStop}, nil
	}
	resp := m.responses[m.idx]
	m.idx++
	return resp, nil
}

func (m *streamingMockModel) DoStream(_ context.Context, params provider.GenerateParams) (*provider.StreamResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, params)
	var resp *provider.GenerateResult
	if m.idx >= len(m.responses) {
		resp = &provider.GenerateResult{Text: "done", FinishReason: provider.FinishStop}
	} else {
		resp = m.responses[m.idx]
		m.idx++
	}

	ch := make(chan provider.StreamChunk, 10)
	// Emit text as a single chunk then finish.
	if resp.Text != "" {
		ch <- provider.StreamChunk{Type: provider.ChunkText, Text: resp.Text}
	}
	// Emit tool calls if any.
	for _, tc := range resp.ToolCalls {
		ch <- provider.StreamChunk{
			Type:       provider.ChunkToolCallStreamStart,
			ToolCallID: tc.ID,
			ToolName:   tc.Name,
		}
		ch <- provider.StreamChunk{
			Type:       provider.ChunkToolCall,
			ToolCallID: tc.ID,
			ToolName:   tc.Name,
			ToolInput:  string(tc.Input),
		}
	}
	fr := resp.FinishReason
	if fr == "" {
		fr = provider.FinishStop
	}
	ch <- provider.StreamChunk{
		Type:         provider.ChunkFinish,
		FinishReason: fr,
		Usage:        resp.Usage,
	}
	close(ch)
	return &provider.StreamResult{Stream: ch}, nil
}

// reasoningStreamModel emits ChunkReasoning then ChunkText for testing reasoning paths.
type reasoningStreamModel struct {
	text      string
	reasoning string
	usage     provider.Usage
}

func (m *reasoningStreamModel) ModelID() string { return "reasoning-stream" }
func (m *reasoningStreamModel) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	return &provider.GenerateResult{Text: m.text, Usage: m.usage, FinishReason: provider.FinishStop}, nil
}
func (m *reasoningStreamModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	ch := make(chan provider.StreamChunk, 10)
	if m.reasoning != "" {
		ch <- provider.StreamChunk{Type: provider.ChunkReasoning, Text: m.reasoning}
	}
	if m.text != "" {
		ch <- provider.StreamChunk{Type: provider.ChunkText, Text: m.text}
	}
	ch <- provider.StreamChunk{
		Type:         provider.ChunkFinish,
		FinishReason: provider.FinishStop,
		Usage:        m.usage,
	}
	close(ch)
	return &provider.StreamResult{Stream: ch}, nil
}

// failingStreamModel returns an error from DoStream.
type failingStreamModel struct {
	err error
}

func (m *failingStreamModel) ModelID() string { return "failing-stream" }
func (m *failingStreamModel) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	return nil, m.err
}
func (m *failingStreamModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, m.err
}

// errorStreamModel emits some text then a ChunkError, so stream.Err is non-nil.
type errorStreamModel struct {
	text string
	err  error
}

func (m *errorStreamModel) ModelID() string { return "error-stream" }
func (m *errorStreamModel) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	return nil, m.err
}
func (m *errorStreamModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	ch := make(chan provider.StreamChunk, 5)
	if m.text != "" {
		ch <- provider.StreamChunk{Type: provider.ChunkText, Text: m.text}
	}
	ch <- provider.StreamChunk{Type: provider.ChunkError, Error: m.err}
	ch <- provider.StreamChunk{Type: provider.ChunkFinish, FinishReason: provider.FinishError}
	close(ch)
	return &provider.StreamResult{Stream: ch}, nil
}

// --- Helper: make a simple text response ---

func textResult(text string, inputTokens, outputTokens int) *provider.GenerateResult {
	return &provider.GenerateResult{
		Text:         text,
		FinishReason: provider.FinishStop,
		Usage:        provider.Usage{InputTokens: inputTokens, OutputTokens: outputTokens},
	}
}

// toolCallResult creates a response with tool calls.
func toolCallResult(text string, inputTokens, outputTokens int, toolCalls ...provider.ToolCall) *provider.GenerateResult {
	return &provider.GenerateResult{
		Text:         text,
		ToolCalls:    toolCalls,
		FinishReason: provider.FinishToolCalls,
		Usage:        provider.Usage{InputTokens: inputTokens, OutputTokens: outputTokens},
	}
}

// tc creates a provider.ToolCall.
func tc(id, name string, args json.RawMessage) provider.ToolCall {
	return provider.ToolCall{ID: id, Name: name, Input: args}
}

// --- Helper: create goai.Tool instances for tests ---

// makeTool creates a goai.Tool with a simple string-returning execute function.
func makeTool(name, desc string, result string) goai.Tool {
	return goai.Tool{
		Name:        name,
		Description: desc,
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			return result, nil
		},
	}
}

// makeErrorTool creates a goai.Tool that always returns an error.
func makeErrorTool(name, desc string) goai.Tool {
	return goai.Tool{
		Name:        name,
		Description: desc,
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "", errors.New("tool executor exploded")
		},
	}
}

// --- Helper: mockProgressSink ---

type mockProgressSink struct {
	onEvent  func(ctx context.Context, e Event)
	onOutput func(ctx context.Context, o Output)
}

func (m *mockProgressSink) OnEvent(ctx context.Context, e Event) {
	if m.onEvent != nil {
		m.onEvent(ctx, e)
	}
}

func (m *mockProgressSink) OnOutput(ctx context.Context, o Output) {
	if m.onOutput != nil {
		m.onOutput(ctx, o)
	}
}
