// Package coord houses the workflow coordinator surface: the small
// behavioural contract a coord-side AgentRunner must implement
// (RunnerHandle), the four goai.Tool factories the coord LLM uses to
// drive a workflow (forward_to_agent / send_message / narrate /
// finalize), and the factory + default system prompt that wire those
// tools onto a pre-configured *AgentRunner.
// All public coord symbols (tool factories, prompts, factory) are
// re-exported by package zenflow's coord_facade.go so external SDK
// consumers keep their `import "github.com/zendev-sh/zenflow"` working
// unchanged. Edit here, not in root.
package coord

import (
	"github.com/zendev-sh/zenflow/internal/router"
	"github.com/zendev-sh/zenflow/internal/types"
)

// RunnerHandle is the behaviour contract the four coord tool factories
// (ForwardToAgentToolDef, SendMessageToolDef, NarrateToolDef,
// FinalizeToolDef) demand of the *AgentRunner they close over. It is
// the seam that lets the tools live in internal/coord while the
// concrete *AgentRunner stays in internal/exec - both packages can be
// imported without forming a cycle.
// Methods come in three groups:
// - Wiring accessors (Router, Progress, StepID, RunID): supply the
// plumbing the tools need to deliver messages and emit events.
// - Forward-correlation (NextForwardSeq): atomically allocates the
// monotonic counter that backs `msg-fwd-N` / `msg-send-N`.
// - Finalize plumbing (EnsureFinalizeCh, MarkFinalized,
// SetFinalSummary, FinalSummary, Finalized): the close-once /
// atomic-flag dance FinalizeToolDef performs to signal the caller's
// Run loop to exit. MarkFinalized handles BOTH the flag flip AND
// the close-once on the channel so callers do not need direct chan
// access.
// Any *AgentRunner-shaped type that satisfies these methods is a
// valid RunnerHandle. The concrete *exec.AgentRunner provides the
// canonical implementation.
type RunnerHandle interface {
	Router() *router.Router
	Progress() types.ProgressSink
	StepID() string
	RunID() string

	// NextForwardSeq returns the next monotonic per-runner forward
	// sequence number. Used to mint `msg-fwd-N` / `msg-send-N`
	// correlation IDs on the tool result string. Atomic + monotonic.
	NextForwardSeq() uint64

	// EnsureFinalizeCh returns the lazily-allocated finalize channel.
	// Read-only; the channel is closed exactly once by MarkFinalized.
	// Safe to call concurrently with MarkFinalized - both observe the
	// same channel via internal synchronisation on the runner.
	EnsureFinalizeCh() <-chan struct{}

	// MarkFinalized sets the finalized flag AND closes the finalize
	// channel exactly once. Idempotent: subsequent calls are no-ops.
	MarkFinalized()

	// SetFinalSummary stores the optional synthesis text passed to
	// FinalizeToolDef. Last-writer-wins on repeated calls.
	SetFinalSummary(summary string)

	// FinalSummary returns the most recently stored synthesis text,
	// or empty string if SetFinalSummary was never called.
	FinalSummary() string

	// Finalized snapshots whether MarkFinalized has been invoked.
	Finalized() bool
}
