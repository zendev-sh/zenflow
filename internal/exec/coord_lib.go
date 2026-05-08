package exec

import (
	"context"
	"strings"
)

// primitives for consumers that build their own coord
// lifecycle (CLI, TUI integrations, custom integrations). Exposed because
// the patterns below either (a) require Router internals (step-menu
// generation) or (b) are race-safe wake patterns that consumers MUST
// implement correctly to avoid deadlock / dropped events.
// Wrapper helpers (e.g. RunCoordLoop) are deliberately NOT provided:
// integrators have different lifecycle constraints (chat-driven wake
// sources, hot-reload, pause/resume) and a single wrapper would be
// rigid. Compose primitives instead.

// DefaultCoordColdStartPrompt is the user message zenflow's CLI sends
// on the very first coord Run cycle. It tells the LLM to wait silently
// until events arrive (avoids cosmetic "no events" narration on an
// empty mailbox) and explicitly defers finalize until the workflow
// has actually completed.
// Use directly or as a starting point for a customised prompt:
//	userMsg := zenflow.DefaultCoordColdStartPrompt + zenflow.BuildCoordStepMenu(runner)
const DefaultCoordColdStartPrompt = "Coordinate the workflow. Your mailbox starts empty; wait silently for step lifecycle events to arrive (do NOT narrate before any event is in your mailbox). When events arrive, follow your narration cadence rules. CRITICAL: do NOT call finalize yet - the workflow is just starting; many step events will arrive later. Only finalize AFTER you have seen EventStepEnd for the LAST workflow step."

// DefaultCoordContinuationPrompt is the user message sent on every
// subsequent coord Run re-entry (after Wake fires or pending mailbox
// content forces a re-entry). Kept short by design: the wake-blocking
// pattern (see WaitForCoordWake) guarantees the LLM is only invoked
// when there ARE events, so the prompt does not need to ask the LLM
// to "check" anything. hardening - verbose "check your
// mailbox" phrasing here baited weak prompt-followers (MiniMax) into
// echoing it as filler narration.
const DefaultCoordContinuationPrompt = "Process the new mailbox events. Follow your narration cadence rules. Do NOT call finalize unless workflow is complete (all declared steps emitted EventStepEnd)."

// BuildCoordStepMenu returns a human-readable list of FORWARDABLE
// step IDs for the coord LLM, filtering out wrapper containers
// (Loop / Include) registered via Router.RegisterWrapperStep. Designed
// to be appended to coord prompts each wake so the LLM sees an
// up-to-date snapshot as new loop iterations register sub-steps:
//	userMsg := zenflow.DefaultCoordContinuationPrompt + zenflow.BuildCoordStepMenu(runner)
// Returns the empty string when:
// - runner / runner.Router is nil
// - no steps registered (cold-start before executor begins)
// - every registered step is a wrapper (rare; degenerate workflow)
// The menu includes a "do NOT invent step IDs" rule because
// confirmed that listing valid IDs alone tempts weak LLMs to fabricate
// neighbouring IDs (e.g. "X.1.<step>" before iteration 1 starts).
// Single source of truth: the wrapper filter consults Router's
// explicit marker (set by executor when registering steps). The same
// marker drives ForwardToAgentToolDef's reject path, so consumer-side
// filtering and tool-side validation cannot drift.
func BuildCoordStepMenu(runner *AgentRunner) string {
	if runner == nil || runner.router == nil {
		return ""
	}
	steps := runner.router.KnownSteps()
	if len(steps) == 0 {
		return ""
	}
	leaves := make([]string, 0, len(steps))
	for _, candidate := range steps {
		if !runner.router.IsWrapperStep(candidate) {
			leaves = append(leaves, candidate)
		}
	}
	if len(leaves) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nFORWARDABLE STEPS (current snapshot - refreshed each wake):\n")
	for _, s := range leaves {
		b.WriteString("  - ")
		b.WriteString(s)
		b.WriteString("\n")
	}
	b.WriteString("\nCRITICAL - do NOT invent step IDs not in this list:\n")
	b.WriteString("  - The list above is the COMPLETE set of forwardable targets right now.\n")
	b.WriteString("  - Forwarding to any other ID (including imagined future iterations like\n")
	b.WriteString("    \"X.1.<step>\" before iteration 1 starts, or domain-inferred steps like\n")
	b.WriteString("    \"pro-rebuttal\" / \"round-2-summary\") WILL fail with unknown-step drop.\n")
	b.WriteString("  - The system preserves dropped content as fallback narration, but you\n")
	b.WriteString("    waste an LLM call generating it. PREFER narrate(text=...) directly\n")
	b.WriteString("    when no current step needs the content.\n")
	return b.String()
}

// WaitForCoordWake blocks until the coord should re-enter Run or
// ctx is done. Encapsulates the race-safe wake pattern:
// 1. Honour ctx cancellation immediately.
// 2. If the mailbox has pending events (delivered between Run's exit
// and this call), return true without blocking - those events
// would otherwise wait until the next Wake even though they are
// already actionable.
// 3. Otherwise block on Wake | ctx.Done.
// Returns true to proceed to the next runner.Run, false to terminate
// the loop. Typical usage:
//	for {
// runner.Run(ctx, cfg, userMsg, modelID, runner.Tools)
// if !zenflow.WaitForCoordWake(ctx, runner) {
// return
// }
// userMsg = zenflow.DefaultCoordContinuationPrompt + zenflow.BuildCoordStepMenu(runner)
//	}
// Consumers with additional wake sources (e.g. a TUI's chat-input
// channel) should compose their own select rather than calling this
// helper - the runner.Wake / runner.Mailbox primitives are public.
func WaitForCoordWake(ctx context.Context, runner *AgentRunner) bool {
	if ctx.Err() != nil {
		return false
	}
	if runner != nil && runner.mailbox != nil && len(runner.mailbox.Unread(runner.stepID)) > 0 {
		return true
	}
	if runner == nil || runner.wake == nil {
 // No wake channel: degenerate case (no mailbox mode). Fall
 // through to ctx.Done so the loop exits cleanly on cancel
 // instead of busy-spinning on Run.
		<-ctx.Done()
		return false
	}
	select {
	case <-runner.wake:
		return true
	case <-ctx.Done():
		return false
	}
}
