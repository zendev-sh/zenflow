package exec

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zendev-sh/zenflow/internal/types"
)

// TestWithCoordinator_NilOptOut - passing nil disables coordinator
// wiring entirely. After running a minimal workflow, no coordinator-side
// machinery should have been allocated and no events should have been
// pushed to any caller-provided mailbox (because there isn't one).
// The check is structural: orchestrator.coordinator must be nil after
// WithCoordinator(nil), and runFlowWithID must complete without
// requiring any runner.
func TestWithCoordinator_NilOptOut(t *testing.T) {
	o := New(WithCoordinator(nil))
	if o.coordinator != nil {
		t.Fatalf("WithCoordinator(nil) expected o.coordinator == nil, got %v", o.coordinator)
	}
}

// TestWithCoordinator_RunnerSet - passing a non-nil *AgentRunner
// installs it as the coordinator. The runner's Mailbox is the inbox the
// executor will push lifecycle events into. The orchestrator's
// coordinator field must reference exactly the runner the caller passed.
func TestWithCoordinator_RunnerSet(t *testing.T) {
	mailbox := NewInMemoryMailboxStore()
	runner := &AgentRunner{
		stepID:  "coordinator",
		mailbox: mailbox,
	}
	o := New(WithCoordinator(runner))
	if o.coordinator != runner {
		t.Fatalf("WithCoordinator(runner) expected o.coordinator == runner, got %v", o.coordinator)
	}
}

// TestOrchestratorCoordinator_PublicAccessor - the public
// Coordinator accessor mirrors the private coordinator field.
// Standalone CLI in cmd/zenflow uses this seam to start the coord
// runner's Run loop after constructing the orchestrator (per
// caller-owned lifecycle). Asserts every branch:
// - nil receiver returns nil (defensive)
// - WithCoordinator(nil) → returns nil
// - WithCoordinator(runner) → returns the same runner
func TestOrchestratorCoordinator_PublicAccessor(t *testing.T) {
	var nilOrch *Orchestrator
	if got := nilOrch.Coordinator(); got != nil {
		t.Errorf("nil-receiver Coordinator() = %v, want nil", got)
	}

	o1 := New(WithCoordinator(nil))
	if got := o1.Coordinator(); got != nil {
		t.Errorf("WithCoordinator(nil): Coordinator() = %v, want nil", got)
	}

	runner := &AgentRunner{stepID: "coordinator", mailbox: NewInMemoryMailboxStore()}
	o2 := New(WithCoordinator(runner))
	if got := o2.Coordinator(); got != runner {
		t.Errorf("WithCoordinator(runner): Coordinator() = %p, want %p", got, runner)
	}
}

// TestExecutor_PushesEventsToCoordMailbox - when WithCoordinator(runner)
// is supplied with a runner whose Mailbox is non-nil, the executor must
// append a RouterMessage per lifecycle event (StepStart, StepEnd) to the
// runner's mailbox under the coord step ID. The runner is NOT started
// (no Run loop); the executor simply pushes and the test inspects the
// mailbox directly.
// Coord step ID convention: runner.StepID when non-empty, else the
// constant "coordinator".
func TestExecutor_PushesEventsToCoordMailbox(t *testing.T) {
	mailbox := NewInMemoryMailboxStore()
	runner := &AgentRunner{
		stepID:  "coordinator",
		mailbox: mailbox,
	}
	model := &mockModel{}
	wf := &Workflow{
		Name: "z13",
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

	unread := mailbox.Unread("coordinator")
	if len(unread) == 0 {
		t.Fatalf("expected events pushed to coord mailbox, got 0")
	}
	// Verify we have at least one StepStart-tagged and one StepEnd-tagged
	// message in order. Encoding contract: msg.Metadata["event_type"]
	// carries the EventType string so consumers can route without
	// parsing Content.
	var sawStart, sawEnd bool
	for _, m := range unread {
		switch m.Metadata["event_type"] {
		case string(types.EventStepStart):
			sawStart = true
		case string(types.EventStepEnd):
			if !sawStart {
				t.Errorf("StepEnd appeared before StepStart in mailbox")
			}
			sawEnd = true
		}
	}
	if !sawStart {
		t.Errorf("expected a StepStart event in coord mailbox; got %d messages with subjects %v",
			len(unread), eventTypesOf(unread))
	}
	if !sawEnd {
		t.Errorf("expected a StepEnd event in coord mailbox; got %d messages with subjects %v",
			len(unread), eventTypesOf(unread))
	}
}

func eventTypesOf(msgs []RouterMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Metadata["event_type"]
	}
	return out
}

// allowedGoRunCallsites is the explicit allowlist for production
// `go *.Run(...)` callsites that survive the source-level gate
// (widening). The widened regex
// `go[[:space:]]+[a-zA-Z0-9_.]+\.Run\(` catches ANY `go X.Run(` -
// including obfuscated coord spawns like `go agentRunnerForCoord.Run(`
// that the original `[Cc]oord` substring filter would miss. Any new
// production callsite NOT in this list FAILs the test as a likely
// regression on the caller-owned-lifecycle invariant.
// Update this list ONLY when you are intentionally introducing a new
// `go runner.Run(...)` callsite that does NOT spawn a coord runner on
// the caller's behalf. Document the reason inline. As of the
// list is EMPTY: the synchronous Run callsites in zenflow.go and
// executor_resume.go don't match the regex (no `go` prefix), and no
// production goroutine spawns runner.Run.
var allowedGoRunCallsites = map[string]string{
	// Empty by design . Add entries as
	// "<file:line>": "<reason>" pairs when introducing a legitimate
	// `go XYZ.Run(...)` spawn in production code.
}

// TestOrchestrator_RequiresCallerStartedRunner - orchestrator
// pushes events to the coord runner's Mailbox but does NOT call
// runner.Run on the caller's behalf. The contract is: the caller owns
// runner lifecycle. If the caller never starts the runner, the workflow
// still completes (events accumulate unread in the mailbox).
// strengthening (Fix 2): in addition to the runtime assertion
// (workflow completes with un-started runner), this test asserts at the
// SOURCE level that the orchestrator has zero `go runner.Run(...)`
// callsites - the previous version of the test could pass even if the
// orchestrator quietly spawned the runner via `go coord.Run(...)`. The
// shell-out grep mirrors the pattern in coord_removal_test.go.
// : the original regex
// `go[[:space:]]+[a-zA-Z_]*[Cc]oord(inator)?[a-zA-Z_]*\.Run\(` only
// matched callsites whose receiver name CONTAINED "coord" - so a
// regression like `go agentRun.Run(...)` or `go r.Run(...)` would slip
// through. The widened regex catches ALL `go X.Run(` callsites and
// uses an explicit allowlist (allowedGoRunCallsites above) to permit
// legitimate exceptions. Today the allowlist is empty.
func TestOrchestrator_RequiresCallerStartedRunner(t *testing.T) {
	// Source-level assertion FIRST: production code must not contain
	// any `go <anything>.Run(...)` callsite outside the explicit
	// allowlist. widened the regex so renames or wrapper
	// receivers cannot evade the gate.
	// The source-level grep is platform-agnostic at the invariant level
	// (a callsite in production code is the same regardless of host OS),
	// but the assertion shells out to `grep` which doesn't ship on
	// Windows. macOS and Linux CI both run this gate, so any source-level
	// regression is caught there - on Windows we skip the grep portion
	// and still execute the runtime assertion below.
	if runtime.GOOS != "windows" {
		cmd := exec.Command("grep", "-rEn",
			"go[[:space:]]+[a-zA-Z0-9_.]+\\.Run\\(",
			"--include=*.go",
			".",
		)
		out, err := cmd.Output()
		// grep exit codes: 0 = match found (FAIL for us), 1 = no match
		// (PASS), 2+ = real error.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() > 1 {
			t.Fatalf("grep failed with exit code %d: %v", exitErr.ExitCode(), err)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		var hits []string
		for _, line := range lines {
			if line == "" {
				continue
			}
			// Parse "file:line:content" - extract path and "file:line".
			path := line
			fileLine := line
			if i := strings.Index(line, ":"); i >= 0 {
				path = line[:i]
				if j := strings.Index(line[i+1:], ":"); j >= 0 {
					fileLine = line[:i+1+j]
				}
			}
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			if strings.HasPrefix(path, "./vendor/") || strings.HasPrefix(path, "vendor/") {
				continue
			}
			// Allowlist check: legitimate `go X.Run(...)` callsites carry
			// an explicit entry in allowedGoRunCallsites. Strip a leading
			// "./" so allowlist keys don't depend on grep's path prefix.
			key := strings.TrimPrefix(fileLine, "./")
			if _, ok := allowedGoRunCallsites[key]; ok {
				continue
			}
			hits = append(hits, line)
		}
		if len(hits) > 0 {
			t.Fatalf("caller-owned-lifecycle contract violated: unexpected `go *.Run(...)` callsite "+
				"NOT in allowedGoRunCallsites; this may be a regression on the caller-owned-lifecycle "+
				"invariant. If intentional, add an explicit entry to allowedGoRunCallsites with a reason. "+
				"Got %d hit(s):\n%s",
				len(hits), strings.Join(hits, "\n"))
		}
	}

	// Runtime assertion: workflow completes when the caller never
	// starts the runner; events accumulate unread.
	mailbox := NewInMemoryMailboxStore()
	runner := &AgentRunner{
		stepID:  "coordinator",
		mailbox: mailbox,
		// No Run goroutine. Run was never called.
	}
	model := &mockModel{}
	wf := &Workflow{
		Name: "z14",
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
	res, err := o.RunFlow(ctx, wf)
	if err != nil {
		t.Fatalf("RunFlow with un-started runner: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil WorkflowResult")
	}
	// Mailbox should contain unread events - the caller never drained.
	if got := len(mailbox.Unread("coordinator")); got == 0 {
		t.Errorf("expected unread events in coord mailbox (no caller drain), got 0")
	}
}
