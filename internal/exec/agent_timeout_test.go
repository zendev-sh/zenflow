package exec

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/spec"
)

// hungModel blocks forever, completely ignoring context cancellation.
// Simulates a provider whose HTTP call (or retry loop) never returns even
// when the caller cancels ctx - the exact failure mode observed in
// (bedrock/azure processes running 20-40min past --timeout).
// Unlike blockingModel (which returns ctx.Err on cancel), this model
// NEVER returns. Any layer above it that only waits on DoGenerate's return
// will also never return unless it races against ctx.Done itself.
type hungModel struct{}

func (h *hungModel) ModelID() string { return "hung-mock" }

func (h *hungModel) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	select {}
}

func (h *hungModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	select {}
}

// dumpGoroutines returns a full goroutine dump as a string. Used to diagnose
// exactly where the test is stuck when the hang is reproduced.
func dumpGoroutines() string {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return string(buf[:n])
}

// TestZFB1_RunFlow_HungProvider_Reproduction reproduces.
// Expected (correct) behavior: when ctx expires, orch.RunFlow should return
// within a short grace window with context.DeadlineExceeded.
// Actual (buggy) behavior: RunFlow never returns because a hung provider
// blocks goai.GenerateText, which blocks AgentRunner.Run, which blocks
// Executor.Run, which blocks RunFlow. ctx cancellation is ignored by the
// blocked call site.
// This test confirms whether reproduces at the zenflow layer with a
// deterministic hung provider. If it hangs, the stack dump identifies the
// exact blocking goroutine so we can fix root cause.
func TestZFB1_RunFlow_HungProvider_Reproduction(t *testing.T) {
	orch := New(
		WithModel(&hungModel{}),
		WithDefaultModel("hung-mock"),
	)

	wf := &Workflow{
		Name: "zfb1-repro",
		Agents: map[string]AgentConfig{
			"writer": {Description: "writes something"},
		},
		Steps: []Step{
			{ID: "s1", Agent: "writer", Instructions: "write something"},
		},
	}

	// CLI-like timeout: 1 second.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	type runOut struct {
		result *WorkflowResult
		err    error
	}
	outCh := make(chan runOut, 1)
	start := time.Now()
	go func() {
		r, e := orch.RunFlow(ctx, wf)
		outCh <- runOut{result: r, err: e}
	}()

	// Grace window: must exceed executor's 2s abort drain grace. 5s keeps
	// a comfortable margin so minor scheduling jitter does not flake the test.
	const grace = 5 * time.Second

	select {
	case out := <-outCh:
		elapsed := time.Since(start)
		t.Logf("RunFlow returned after %v with err=%v", elapsed, out.err)
		if elapsed > grace {
			t.Fatalf("BUG: RunFlow took %v after 1s timeout (want ≤%v)", elapsed, grace)
		}
 // Executor convention: return (result, nil) even on cancellation; status
 // signals abort. Accept either a ctx-wrapped error OR a non-completed
 // status as evidence of a clean cancellation.
		if out.err != nil && !errors.Is(out.err, context.DeadlineExceeded) && !errors.Is(out.err, context.Canceled) {
			t.Fatalf("RunFlow error = %v, want DeadlineExceeded or Canceled", out.err)
		}
		if out.err == nil {
			if out.result == nil {
				t.Fatalf("RunFlow returned nil result and nil error")
			}
			if out.result.Status == spec.StatusCompleted {
				t.Fatalf("RunFlow completed despite ctx cancellation (status=%s)", out.result.Status)
			}
			sr := out.result.Steps["s1"]
			if sr == nil {
				t.Fatalf("step s1 missing from results after cancel")
			}
			if sr.Status == spec.StepCompleted {
				t.Fatalf("step s1 marked Completed despite hung provider (status=%s)", sr.Status)
			}
			t.Logf("OK: workflow status=%s, step s1 status=%s", out.result.Status, sr.Status)
		}
	case <-time.After(10 * time.Second):
		stacks := dumpGoroutines()
		t.Fatalf("BUG REPRODUCED: RunFlow did not return within 10s despite 1s ctx timeout.\n\n=== GOROUTINE DUMP ===\n%s", stacks)
	}
}

// TestZFB1_RunGoal_HungProvider_Reproduction mirrors the flow test for the
// RunGoal path (coordinator → validate → runFlowWithID). Coordinator's first
// call will hang because the hung model also ignores ctx in DoGenerate.
func TestZFB1_RunGoal_HungProvider_Reproduction(t *testing.T) {
	orch := New(
		WithModel(&hungModel{}),
		WithDefaultModel("hung-mock"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := orch.RunGoal(ctx, "write a haiku about timeouts")
		done <- err
	}()

	const grace = 3 * time.Second

	select {
	case err := <-done:
		elapsed := time.Since(start)
		t.Logf("RunGoal returned after %v with err=%v", elapsed, err)
		if elapsed > grace {
			t.Fatalf("BUG: RunGoal took %v after 1s timeout (want ≤%v)", elapsed, grace)
		}
		if err == nil {
			t.Fatalf("RunGoal returned nil error; want context.DeadlineExceeded")
		}
	case <-time.After(10 * time.Second):
		stacks := dumpGoroutines()
		t.Fatalf("BUG REPRODUCED: RunGoal did not return within 10s despite 1s ctx timeout.\n\n=== GOROUTINE DUMP ===\n%s", stacks)
	}
}

// TestZFB1_RunFlow_WellBehavedBlockingProvider is a control test. It uses
// blockingModel, which blocks but DOES honor ctx.Done. If this test also
// hangs, then zenflow has a cleanup path (childWg, progress sink, coordinator
// narration) that blocks without checking ctx - a second, distinct bug.
// Expected: RunFlow returns promptly after ctx expires, with the step marked
// failed/cancelled.
func TestZFB1_RunFlow_WellBehavedBlockingProvider(t *testing.T) {
	orch := New(
		WithModel(&blockingModel{}),
		WithDefaultModel("blocking-mock"),
	)

	wf := &Workflow{
		Name: "zfb1-control",
		Agents: map[string]AgentConfig{
			"writer": {Description: "writes something"},
		},
		Steps: []Step{
			{ID: "s1", Agent: "writer", Instructions: "write something"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := orch.RunFlow(ctx, wf)
		done <- err
	}()

	const grace = 3 * time.Second

	select {
	case err := <-done:
		elapsed := time.Since(start)
		t.Logf("RunFlow returned after %v with err=%v", elapsed, err)
		if elapsed > grace {
			t.Fatalf("BUG: RunFlow took %v after 1s timeout with well-behaved provider (want ≤%v)", elapsed, grace)
		}
	case <-time.After(10 * time.Second):
		stacks := dumpGoroutines()
		t.Fatalf("BUG: RunFlow did not return within 10s even with a well-behaved blocking provider.\n\n=== GOROUTINE DUMP ===\n%s", stacks)
	}
}
