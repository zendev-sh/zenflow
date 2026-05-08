package exec

// Cold-start resume overhead benchmark.
// Acceptance: resume path excluding LLM RTT < 200ms per invocation.
// We use a near-zero-cost mock provider that returns a
// tiny result synchronously so measured wall-clock is dominated by
// zenflow internals (transcript Load, handle allocation, goroutine
// spawn, EventResumeStarted emission, reverse-route send, Done
// signaling).
// Run:
//	go test -bench=BenchmarkResumeOverhead -benchtime=10x -run=^$ ./zenflow/

import (
	"context"
	"testing"

	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/resume"
)

// BenchmarkResumeOverhead measures the wall-clock cost of a single
// Executor.ResumeStep call from invocation through DoneCh close, with
// a fake provider that returns instantly. The benchmark reports
// ns/op; divide by 1e6 for ms/op. Target: ms/op < 200.
func BenchmarkResumeOverhead(b *testing.B) {
	// Fake provider returns a tiny deterministic result with zero
	// artificial delay - DoGenerate returns immediately, so the
	// measured time is pure zenflow overhead.
	model := &sequentialMockModel{
		fn: func(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
			return textResult("ok", 1, 1), nil
		},
	}

	e := &Executor{
		Runner:          &AgentRunner{model: model},
		RunID:           "run-bench",
		transcriptStore: resume.NewInMemoryTranscriptStore(),
	}

	// Seed a small transcript once - the cost of Append is not part
	// of the resume cold-start path we're measuring.
	if err := e.transcriptStore.Append(e.RunID, "s", []provider.Message{
		mkTextMsg(provider.RoleUser, "seed-user"),
		mkTextMsg(provider.RoleAssistant, "seed-assistant"),
	}); err != nil {
		b.Fatalf("transcript seed: %v", err)
	}

	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		h, err := e.ResumeStep(ctx, "s", "poke", "coord")
		if err != nil {
			b.Fatalf("ResumeStep: %v", err)
		}
		<-h.DoneCh
		if h.Err != nil {
			b.Fatalf("handle.Err: %v", h.Err)
		}
	}
	b.StopTimer()

	// Derived ms/op for the report (go test prints ns/op natively;
	// also log the ms/op so the acceptance gate is obvious in the raw
	// bench output).
	nsPerOp := float64(b.Elapsed().Nanoseconds()) / float64(b.N)
	b.ReportMetric(nsPerOp/1e6, "ms/op")
}
