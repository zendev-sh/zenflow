package exec

// bench_coord_test.go - BenchmarkCoordOverhead.
// Compares wall time of running an identical workflow with two coord
// configurations:
// - nil-coord baseline: WithCoordinator is not set (executor-only mode).
// - default-coord: WithCoordinator(NewDefaultCoordRunner(stubLM)).
// The workflow has one step whose agent invokes a stub tool that sleeps
// for `bench_simulatedStepDuration`. The step's stub LM returns one
// tool call followed by a Stop response - no real LLM latency.
// The default-coord LLM stub returns a `finalize` tool call on every
// invocation, so the coord drains its mailbox and exits cleanly when
// the workflow completes. This isolates coord-machinery overhead
// (mailbox plumbing, event emission, runner.Run goroutine, finalize
// tool dispatch) from any LLM cost.
// Performance gate: (default - nil) / nil < 10%. Raw numbers come from
// `go test -bench BenchmarkCoordOverhead -benchmem ./zenflow/...`.

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
)

// bench_simulatedStepDuration is the per-iteration "agent work" time
// the bench targets (2000ms simulated step). The step's stub tool sleeps
// this long; everything else (LLM call, coord wake, event emission) is
// measured against this baseline.
const bench_simulatedStepDuration = 2 * time.Second

// benchStepLM is a minimal provider.LanguageModel for the step agent.
// Call 1 → tool call to bench_sleep
// Call 2+ → Stop with text "done" (terminates the agent's tool loop)
type benchStepLM struct {
	calls atomic.Int64
}

func (m *benchStepLM) ModelID() string { return "bench-step-lm" }

func (m *benchStepLM) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	n := m.calls.Add(1)
	if n == 1 {
		return &provider.GenerateResult{
			ToolCalls: []provider.ToolCall{
				{
					ID:    "tc-bench-sleep",
					Name:  "bench_sleep",
					Input: json.RawMessage(`{}`),
				},
			},
			FinishReason: provider.FinishToolCalls,
		}, nil
	}
	return &provider.GenerateResult{
		Text:         "done",
		FinishReason: provider.FinishStop,
	}, nil
}

func (m *benchStepLM) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, nil
}

// benchCoordLM is a minimal provider.LanguageModel for the coord runner.
// It always returns a `finalize` tool call so the coord runner exits as
// soon as it gets a chance to run a turn.
type benchCoordLM struct{}

func (m *benchCoordLM) ModelID() string { return "bench-coord-lm" }

func (m *benchCoordLM) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	return &provider.GenerateResult{
		ToolCalls: []provider.ToolCall{
			{
				ID:    "tc-bench-finalize",
				Name:  "finalize",
				Input: json.RawMessage(`{"summary":"done"}`),
			},
		},
		FinishReason: provider.FinishToolCalls,
	}, nil
}

func (m *benchCoordLM) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, nil
}

// benchSleepTool is the per-step "agent work" simulation. Sleep duration
// = bench_simulatedStepDuration. Returns "ok" immediately on ctx done.
func benchSleepTool() goai.Tool {
	return goai.Tool{
		Name:        "bench_sleep",
		Description: "Sleeps for the bench_simulatedStepDuration to simulate per-step LLM latency.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Execute: func(ctx context.Context, _ json.RawMessage) (string, error) {
			select {
			case <-time.After(bench_simulatedStepDuration):
				return "ok", nil
			case <-ctx.Done():
				return "ctx-done", nil
			}
		},
	}
}

// benchWorkflow returns a fresh single-step workflow used by both
// arms of the benchmark. Per-call so b.Loop iterations don't share
// state.
func benchWorkflow() *Workflow {
	return &Workflow{
		Name: "bench-coord-overhead",
		Agents: map[string]AgentConfig{
			"worker": {
				Description: "Bench worker - calls bench_sleep once.",
				Prompt:      "You are a bench worker. Call bench_sleep once, then reply 'done'.",
				Tools:       []string{"bench_sleep"},
				MaxTurns:    3,
			},
		},
		Steps: []Step{
			{
				ID:           "do-work",
				Agent:        "worker",
				Instructions: "Call bench_sleep then reply done.",
			},
		},
	}
}

// runBenchOnce executes one workflow iteration with the given coord
// runner (or nil for the baseline arm). Wall-time is measured by the
// caller (b.Loop scoping). Returns once RunFlow returns.
func runBenchOnce(b *testing.B, coord *AgentRunner) {
	b.Helper()
	stepLM := &benchStepLM{}

	opts := []Option{
		WithModel(stepLM),
		WithDefaultModel(stepLM.ModelID()),
		WithTools(benchSleepTool()),
	}
	if coord != nil {
		opts = append(opts, WithCoordinator(coord))
	}
	orch := New(opts...)

	// When a coord is configured, start its Run loop on a goroutine
	// (caller-owned lifecycle per). Cancel via ctx after RunFlow
	// returns; finalize-on-every-turn ensures the coord exits promptly.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	coordDone := make(chan struct{})
	if coord != nil {
		coordCtx, coordCancel := context.WithCancel(ctx)
		defer coordCancel()
		go func() {
			defer close(coordDone)
			_, _ = coord.Run(coordCtx, AgentConfig{}, "Coordinate.", coord.model.ModelID(), coord.tools)
		}()
	} else {
		close(coordDone)
	}

	if _, err := orch.RunFlow(ctx, benchWorkflow()); err != nil {
		b.Fatalf("RunFlow: %v", err)
	}

	if coord != nil {
		// Drain coord goroutine before the next iteration so we don't
		// leak goroutines across b.N. The coord's stub LM returns
		// finalize on every call → Run loop exits as soon as the LLM
		// is invoked.
		select {
		case <-coordDone:
		case <-time.After(2 * time.Second):
			// Force exit - bench iteration shouldn't hang on coord.
		}
	}
}

// BenchmarkCoordOverhead.
// Two sub-benchmarks; the perf-report computes overhead =
// (Default - Nil) / Nil. Hard gate: < 10%.
func BenchmarkCoordOverhead(b *testing.B) {
	b.Run("NilCoord", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			runBenchOnce(b, nil)
		}
	})

	b.Run("DefaultCoord", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			coord := NewDefaultCoordRunner(&benchCoordLM{})
			runBenchOnce(b, coord)
		}
	})
}

// benchMultiStepWorkflow returns a fresh 4-step DAG workflow shaped
// like spec/v1/examples/debate.yaml - one setup step, two parallel
// review steps that depend on setup, then one fan-in step that depends
// on both reviewers. Each step has the same agent contract as the
// single-step bench: call bench_sleep once, return done.
// Topology:
//
//	setup → review-a ┐
//
// └ review-b ┴ → fan-in
// Per-call so b.Loop iterations don't share state.
func benchMultiStepWorkflow() *Workflow {
	return &Workflow{
		Name: "bench-coord-overhead-multistep",
		Agents: map[string]AgentConfig{
			"worker": {
				Description: "Bench worker - calls bench_sleep once.",
				Prompt:      "You are a bench worker. Call bench_sleep once, then reply 'done'.",
				Tools:       []string{"bench_sleep"},
				MaxTurns:    3,
			},
		},
		Steps: []Step{
			{
				ID:           "setup",
				Agent:        "worker",
				Instructions: "Call bench_sleep then reply done.",
			},
			{
				ID:           "review-a",
				Agent:        "worker",
				Instructions: "Call bench_sleep then reply done.",
				DependsOn:    []string{"setup"},
			},
			{
				ID:           "review-b",
				Agent:        "worker",
				Instructions: "Call bench_sleep then reply done.",
				DependsOn:    []string{"setup"},
			},
			{
				ID:           "fan-in",
				Agent:        "worker",
				Instructions: "Call bench_sleep then reply done.",
				DependsOn:    []string{"review-a", "review-b"},
			},
		},
	}
}

// benchMultiStepLM is a stateless step LM for the multi-step bench.
// Unlike benchStepLM (which counts globally), this LM inspects the
// conversation history: if any prior message is a tool result, return
// Stop ("done"); otherwise return one bench_sleep tool call. This keeps
// the per-step contract (call tool once, then stop) regardless of how
// many steps share the LM instance, which the orchestrator does because
// WithModel takes a single LM passed to every step.
type benchMultiStepLM struct{}

func (m *benchMultiStepLM) ModelID() string { return "bench-multistep-lm" }

func (m *benchMultiStepLM) DoGenerate(_ context.Context, p provider.GenerateParams) (*provider.GenerateResult, error) {
	for _, msg := range p.Messages {
		for _, part := range msg.Content {
			if part.Type == provider.PartToolResult && part.ToolName == "bench_sleep" {
				return &provider.GenerateResult{
					Text:         "done",
					FinishReason: provider.FinishStop,
				}, nil
			}
		}
	}
	return &provider.GenerateResult{
		ToolCalls: []provider.ToolCall{
			{
				ID:    "tc-bench-sleep",
				Name:  "bench_sleep",
				Input: json.RawMessage(`{}`),
			},
		},
		FinishReason: provider.FinishToolCalls,
	}, nil
}

func (m *benchMultiStepLM) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, nil
}

// runMultiStepBenchOnce mirrors runBenchOnce but executes the 4-step
// DAG. Uses benchMultiStepLM (stateless / message-history-driven) so
// the same LM instance can serve all 4 steps without the global
// counter problem benchStepLM has.
func runMultiStepBenchOnce(b *testing.B, coord *AgentRunner) {
	b.Helper()
	stepLM := &benchMultiStepLM{}

	opts := []Option{
		WithModel(stepLM),
		WithDefaultModel(stepLM.ModelID()),
		WithTools(benchSleepTool()),
		// Allow the two review steps to run in parallel so the bench
		// reflects the realistic fan-out cost of coord routing.
		WithMaxConcurrency(4),
	}
	if coord != nil {
		opts = append(opts, WithCoordinator(coord))
	}
	orch := New(opts...)

	// 4 steps × 2 s simulated work, but two steps run in parallel →
	// critical path is 3 × 2 s = 6 s. Add 30 s headroom.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	coordDone := make(chan struct{})
	if coord != nil {
		coordCtx, coordCancel := context.WithCancel(ctx)
		defer coordCancel()
		go func() {
			defer close(coordDone)
			_, _ = coord.Run(coordCtx, AgentConfig{}, "Coordinate.", coord.model.ModelID(), coord.tools)
		}()
	} else {
		close(coordDone)
	}

	if _, err := orch.RunFlow(ctx, benchMultiStepWorkflow()); err != nil {
		b.Fatalf("RunFlow: %v", err)
	}

	if coord != nil {
		select {
		case <-coordDone:
		case <-time.After(2 * time.Second):
			// Force exit - bench iteration shouldn't hang on coord.
		}
	}
}

// BenchmarkCoordOverhead_MultiStep - follow-up.
// Wider DAG variant of BenchmarkCoordOverhead: 4-step workflow shaped
// like the debate example (1 setup → 2 parallel reviews → 1 fan-in).
// Push more events through the coord mailbox per workflow run so the
// 10% overhead gate is exercised on a realistic topology, not just on
// the single-step degenerate case.
// Same gate (< 10%) applies.
func BenchmarkCoordOverhead_MultiStep(b *testing.B) {
	b.Run("NilCoord", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			runMultiStepBenchOnce(b, nil)
		}
	})

	b.Run("DefaultCoord", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			coord := NewDefaultCoordRunner(&benchCoordLM{})
			runMultiStepBenchOnce(b, coord)
		}
	})
}
