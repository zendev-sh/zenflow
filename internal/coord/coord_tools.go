package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/zenflow/internal/router"
	"github.com/zendev-sh/zenflow/internal/types"
)

// Tool name constants. Bare strings are used in goai.Tool definitions
// across this file; centralizing them here prevents drift and makes
// cross-package alignment with internal/exec/agent_runner.go obvious
// (same string value, not a shared symbol - keeps package boundaries).
const (
	toolNameForwardToAgent = "forward_to_agent"
	toolNameSendMessage    = "send_message"
	toolNameNarrate        = "narrate"
	toolNameFinalize       = "finalize"
)

// CoordRouterInboxID is the canonical mailbox key reverse replies and
// agent send_message calls route to. Mirrors the root-package constant
// of the same name; kept duplicated here so internal/coord has no
// import edge to root (which would create a cycle once root facades
// re-export coord symbols).
const CoordRouterInboxID = "coordinator"

// Sentinel errors for required-argument validation in coord tools.
// Promoted from inline errors.New calls so callers (and tests) can
// match against stable values via errors.Is rather than substring
// matching on Error output.
var (
	// ErrForwardTargetRequired is returned by forward_to_agent when the
	// target_step_id argument is empty.
	ErrForwardTargetRequired = errors.New("zenflow: forward_to_agent: target_step_id is required")
	// ErrSendMessageEmpty is returned by send_message when text is empty.
	ErrSendMessageEmpty = errors.New("zenflow: send_message: text is required and must be non-empty")
	// ErrNarrateEmpty is returned by narrate when text is empty.
	ErrNarrateEmpty = errors.New("zenflow: narrate: text is required and must be non-empty")
)

// Coord tool definitions.
// Four goai.Tool factories that close over a coord-side RunnerHandle
// to drive workflow execution: forward_to_agent (route a RouterMessage
// to a sibling step's mailbox), send_message (agent → coord upstream
// channel), narrate (emit a user-facing EventCoordinatorNarration),
// and finalize (signal the caller's Run loop to exit).
// Each factory is a constructor that captures the handle pointer; the
// handle supplies the wiring (Router for forward, Progress for
// narrate, finalize plumbing for finalize). The tools are safe to
// register on any handle - missing wiring (e.g. nil Router, nil
// Progress) surfaces as an explicit tool-result error rather than a
// panic, so a misconfigured caller sees the LLM receive a clear
// failure reason instead of silent loss.
// Tool-side correlation IDs (`msg-fwd-N` / `msg-send-N`) come from
// the per-runner counter exposed via RunnerHandle.NextForwardSeq.
// Per-runner scoping keeps IDs from interleaving across concurrent
// workflows in the same process - an observer correlating "msg-fwd-5"
// can trust the 5 means "the fifth tool call in THIS runner's
// lifecycle", not "the fifth across the entire process since boot."
// Both forward_to_agent and send_message share the same counter on
// the runner so a single monotonic sequence spans every coord-side
// routing tool call within a workflow.

// forwardArgs is the JSON payload accepted by forward_to_agent.
type forwardArgs struct {
	TargetStepID string `json:"target_step_id"`
	Text         string `json:"text"`
	Kind         string `json:"kind"` // optional: "info" (default), "context_update", "cancel"
}

// narrateArgs is the JSON payload accepted by narrate.
type narrateArgs struct {
	Text string `json:"text"`
}

// finalizeArgs is the JSON payload accepted by finalize.
type finalizeArgs struct {
	Summary string `json:"summary"` // optional: synthesis text the caller may surface as EventCoordinatorSynthesis
}

// sendMessageArgs is the JSON payload accepted by send_message. Per
// D-Z1 there is NO `to` field - agents cannot select the destination;
// the tool always routes to the canonical coord inbox key.
type sendMessageArgs struct {
	Text string `json:"text"`
}

// kindToRouterMessageType maps the LLM-visible "kind" string to the
// internal RouterMessageType enum. Unknown kinds fall back to
// RouterMessageInfo so a hallucinated kind ("follow-up", "ack") still
// produces a deliverable message rather than a tool error - the
// content is preserved either way and the coord can re-route on the
// next turn if needed.
func kindToRouterMessageType(kind string) router.MessageType {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "info":
		return router.MessageInfo
	case "context_update", "context-update":
		return router.MessageContextUpdate
	case "cancel":
		return router.MessageCancel
	default:
		return router.MessageInfo
	}
}

// ForwardToAgentToolDef returns the `forward_to_agent` tool. The coord
// LLM calls it to route a message into a running step agent's mailbox
// for context injection, follow-up questions, or instructions. Stable.
// Routes via runner.Router.Send (NOT direct mailbox.Append) so the
// MessageRouter's lifecycle checks (closed-mailbox detection,
// pending-senders accounting, drop emission via the executor's
// installed OnDrop callback) all fire normally. Drops surface BOTH
// as EventMessageDropped (via OnDrop) AND as the tool's returned
// result string (`"dropped: <reason>"`) so the LLM observes the
// failure on the same turn the call was made.
// Result format:
// - success: `"queued: msg-fwd-<n>"` (n = monotonic per-runner seq)
// - drop: `"dropped: <reason>"` (reason = canonical
// DropReason.String, e.g. "unknown-step", "target-terminal",
// "mailbox-full")
// Safety: when runner.Router is nil (coord wired into an executor
// that has not yet installed a Router, OR a unit-test runner with no
// messaging stack) the tool returns a clear error so the LLM observes
// the misconfiguration instead of a silent no-op. The nil-Router case
// is distinct from a router-side drop: nil-Router is an operator
// configuration bug (the coord was misconfigured at construction) and
// surfaces as an Execute error; a router drop is a runtime routing
// outcome and surfaces as a non-error tool-result string.
func ForwardToAgentToolDef(runner RunnerHandle) goai.Tool {
	return goai.Tool{
		Name:        toolNameForwardToAgent,
		Description: "Forward a message to a running step agent. Coordinator uses this to inject context, follow-up questions, or instructions into a sibling step's mailbox. Hub-to-spoke only - agents cannot reply directly via this tool. Use kind='context_update' for context injection, 'cancel' to request the recipient stop, or omit/use 'info' for general notes.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"target_step_id":{"type":"string","description":"The stepID of the agent to receive the message (must be a registered workflow step)."},"text":{"type":"string","description":"Message text to deliver."},"kind":{"type":"string","enum":["info","context_update","cancel"],"description":"Optional message kind. Defaults to 'info'. Unknown values fall back to 'info' rather than erroring."}},"required":["target_step_id","text"]}`),
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args forwardArgs
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("forward_to_agent: parse args: %w", err)
			}
			if args.TargetStepID == "" {
				return "", ErrForwardTargetRequired
			}
			rt := runner.Router()
			if rt == nil {
				return "dropped: no-router", nil
			}
 // reject wrapper-step targets BEFORE Router.Send.
 // Wrappers (loop / include containers) ARE registered steps
 // so Router.Send would silently accept the message into the
 // wrapper's inbox where NO agent ever reads it. This is
 // worse than unknown-step drops (no fallback fires).
 // Coord LLM sometimes mirrors wrapper IDs from lifecycle
 // events (prompt rule "mirror From" applied to wrapper
 // start/end events). Hard reject at the tool
 // boundary turns the silent misroute into a visible error
 // + fallback narration so content is preserved.
			if rt.IsWrapperStep(args.TargetStepID) {
				if p := runner.Progress(); p != nil {
					p.OnEvent(ctx, types.Event{
						Type:      types.EventCoordinatorNarration,
						Timestamp: time.Now(),
						RunID:     runner.RunID(),
						StepID:    runner.StepID(),
						Message:   "[forward to wrapper \"" + args.TargetStepID + "\" rejected - content preserved as narration]: " + args.Text,
					})
				}
				return "rejected: \"" + args.TargetStepID + "\" is a loop / include WRAPPER step with no agent of its own - messages forwarded to it would sit unread (silent misroute). Wrapper IDs appear in lifecycle events (start/complete) but are NOT valid forward targets.\n\nACTION REQUIRED in your CURRENT response: EITHER (a) forward to a specific sub-step of \"" + args.TargetStepID + "\" (e.g. \"" + args.TargetStepID + ".0.<inner-step-id>\" or \"" + args.TargetStepID + "[0].<inner-step-id>\"), OR (b) call narrate(text=...) with the same content. The system already preserved your content as fallback narration; you can also act in this turn for cleaner UX.", nil
			}
			id := fmt.Sprintf("msg-fwd-%d", runner.NextForwardSeq())
			msgType := kindToRouterMessageType(args.Kind)
 // Fix B: emit EventMessageSent BEFORE Router.Send so users see
 // the outbound message immediately. Without this, live runs had
 // a silent gap between forward_to_agent's tool_call and the
 // recipient's eventual EventAgentInboxDrain (often turns later
 // after the recipient woke from its mailbox).
			emitMessageSent(ctx, runner, args.TargetStepID, args.Text, msgType)
			if err := rt.Send(args.TargetStepID, router.Message{
				From:      runner.StepID(),
				To:        args.TargetStepID,
				Content:   args.Text,
				Type:      msgType,
				Timestamp: time.Now(),
			}); err != nil {
 // Per: drops surface as the tool result string
 // (NOT as an Execute error). Router.Send already
 // formatted err as "dropped: <reason>" so we pass it
 // through verbatim.
 // preserve dropped forward content as a
 // coordinator narration so the user log retains the
 // information. Without this, coord-generated analysis
 // is "lost to agents" (no step receives it) AND
 // effectively lost to the user once the brief
 // `⇠ sent ...` line scrolls past. Emitting as
 // EventCoordinatorNarration also makes the content
 // addressable in JSON sink consumers (downstream
 // pipelines can capture the would-have-been-forwarded
 // content for diagnostics or fallback workflows).
				if p := runner.Progress(); p != nil {
					p.OnEvent(ctx, types.Event{
						Type:      types.EventCoordinatorNarration,
						Timestamp: time.Now(),
						RunID:     runner.RunID(),
						StepID:    runner.StepID(),
						Message:   "[forward to \"" + args.TargetStepID + "\" dropped - content preserved as narration]: " + args.Text,
					})
				}
 // for unknown-step drops specifically,
 // append a snapshot of currently registered step IDs
 // so the LLM can self-correct in the next turn.
 // Defends against hallucinated targets like
 // "negative" / "narrator" / "<workflow>[setup]"
 // observed across multiple weak-compliance models.
 // Other drop reasons (target-terminal, coord-down,
 // cap-exhaustion) keep the original concise message
 // since the LLM cannot correct those by re-targeting.
				var de *router.DropError
				if errors.As(err, &de) && de.Reason == router.DropReasonUnknownStep {
					return err.Error() + BuildUnknownStepHint(rt, args.TargetStepID), nil
				}
				return err.Error(), nil
			}
			return "queued: " + id, nil
		},
	}
}

// BuildUnknownStepHint formats a helpful suffix listing currently
// registered step IDs after a forward_to_agent unknown-step drop.
// + - error feedback for the coord LLM to self-correct.
// Returns an empty string when no steps are registered (defensive;
// caller wraps it harmlessly).
// message is action-oriented: tells LLM the EXACT next steps
// to take in the SAME response (retry with valid ID OR narrate as
// fallback) rather than just listing available IDs and hoping.
// Without action guidance, weak models (Gemini-Flash observed) may
// retry with wrapper or exit the turn entirely, leaving content
// dropped. The system DOES auto-preserve dropped content as narration
// (server-side safety net), but coaching the LLM to act in the same
// turn produces cleaner UX.
func BuildUnknownStepHint(rt *router.Router, attempted string) string {
	if rt == nil {
		return ""
	}
	steps := rt.KnownSteps()
	if len(steps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(". Attempted target \"")
	b.WriteString(attempted)
	b.WriteString("\" is not a registered step. Available step IDs (current snapshot): ")
	for i, s := range steps {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(s)
	}
	b.WriteString(".\n\nACTION REQUIRED in your CURRENT response (do not exit the turn): EITHER (a) retry forward_to_agent with one of the available IDs above, OR (b) call narrate(text=...) with the same content to surface it for the user. Do NOT exit without one of these - the system will preserve content as fallback narration, but acting in this turn produces cleaner UX.\n\nNotes: workflow name is NEVER a step prefix; agent role names are NOT step IDs. If you intended to forward to a step that has not started yet (e.g. a future loop iteration), the step does NOT exist - wait for its lifecycle event before forwarding.")
	return b.String()
}

// emitMessageSent emits one EventMessageSent for outbound messages from
// send_message and forward_to_agent. Silently no-op when the runner has
// no Progress sink wired (mirrors emitInboxDrain's contract). Field
// shape per EventMessageSent docstring: StepID = sender;
// Data["to"] = recipient; Data["text"] = full text;
// Data["msg_type"] = RouterMessageType int.
func emitMessageSent(ctx context.Context, runner RunnerHandle, to, text string, msgType router.MessageType) {
	p := runner.Progress()
	if p == nil {
		return
	}
	p.OnEvent(ctx, types.Event{
		Type:      types.EventMessageSent,
		Timestamp: time.Now(),
		RunID:     runner.RunID(),
		StepID:    runner.StepID(),
		Message:   text,
		Data: map[string]any{
			"to":       to,
			"text":     text,
			"msg_type": int(msgType),
		},
	})
}

// SendMessageToolDef returns the `send_message` tool. Step agents call
// it to send a message back to the workflow coordinator. The target is
// hardcoded to the canonical coord inbox key (CoordRouterInboxID =
// "coordinator") - there is no `to` parameter on the input schema. Stable.
// Hub-only routing:
// - Agents can only message the hub (coord); siblings are unreachable.
// - The coord is the sole router - it decides forwarding via
// `forward_to_agent`. This eliminates peer discovery and mesh
// complexity (DL-6 anti-pattern).
// Result format:
// - success: `"queued: msg-send-<n>"` (n = monotonic per-runner seq)
// - drop: `"dropped: <reason>"` where <reason> is the canonical
// DropReason.String value, e.g. "unknown-step", "target-terminal",
// "mailbox-full". Drops surface in BOTH the tool result string AND
// EventMessageDropped (via Router's OnDrop), preserving the
// "zero silent drops" contract while making the failure visible to
// the LLM on the same turn.
// No-coordinator drop:
// - When runner.Router is nil (coord not wired into a workflow,
// OR a unit-test runner with no messaging stack), the tool returns
// `"dropped: no-coordinator"` as the result string with NIL error.
// This is distinct from `forward_to_agent`'s nil-Router path which
// surfaces as an Execute error: send_message is callable from steps
// in any zenflow context (including non-workflow RunAgent), so a
// missing coord is a runtime routing outcome (the user opted out of
// a coord), not a coord-side configuration bug.
func SendMessageToolDef(runner RunnerHandle) goai.Tool {
	return goai.Tool{
		Name:        toolNameSendMessage,
		Description: "Send a message to the workflow coordinator. Hub-only routing - agents cannot directly message siblings; the coordinator decides forwarding. Use this for status updates, questions, or requests for context the coordinator can route to other steps.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","description":"Message text to send to the coordinator."}},"required":["text"]}`),
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args sendMessageArgs
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("send_message: parse args: %w", err)
			}
			if strings.TrimSpace(args.Text) == "" {
				return "", ErrSendMessageEmpty
			}
			rt := runner.Router()
			if rt == nil {
				return "dropped: no-coordinator", nil
			}
			id := fmt.Sprintf("msg-send-%d", runner.NextForwardSeq())
			emitMessageSent(ctx, runner, CoordRouterInboxID, args.Text, router.MessageInfo)
			if err := rt.Send(CoordRouterInboxID, router.Message{
				From:      runner.StepID(),
				To:        CoordRouterInboxID,
				Content:   args.Text,
				Type:      router.MessageInfo,
				Timestamp: time.Now(),
			}); err != nil {
				return err.Error(), nil
			}
			return "queued: " + id, nil
		},
	}
}

// NarrateToolDef returns the `narrate` tool. The coord LLM calls it to
// emit a user-facing narration message - e.g. explaining its routing
// decision, summarising a recently-finished step, or surfacing
// reasoning context for downstream observers. Stable.
// Emits one EventCoordinatorNarration per call via runner.Progress.
// Does NOT route messages to step agents (use forward_to_agent for
// that). Empty text returns an error rather than emitting an empty
// event - a no-op narration always indicates a coord-side bug worth
// surfacing to the model.
// Safety: when runner.Progress is nil the tool returns a clear
// error. The coord must be wired with a sink before narrate can fire.
func NarrateToolDef(runner RunnerHandle) goai.Tool {
	return goai.Tool{
		Name:        toolNameNarrate,
		Description: "Emit a narration message for the user/observer. Coordinator uses this to explain reasoning, summarize step results, or surface user-facing context. Does NOT route to step agents - use forward_to_agent for that.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","description":"The narration text to surface to the user."}},"required":["text"]}`),
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args narrateArgs
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("narrate: parse args: %w", err)
			}
			if strings.TrimSpace(args.Text) == "" {
				return "", ErrNarrateEmpty
			}
			p := runner.Progress()
			if p == nil {
				return "dropped: no-progress-sink", nil
			}
			p.OnEvent(ctx, types.Event{
				Type:      types.EventCoordinatorNarration,
				Timestamp: time.Now(),
				RunID:     runner.RunID(),
				StepID:    runner.StepID(),
				AgentName: runner.StepID(),
				Message:   args.Text,
			})
			return "narrated", nil
		},
	}
}

// FinalizeToolDef returns the `finalize` tool. The coord LLM calls it
// to signal that coordination is complete - the caller's outer Run
// loop should exit. Stable.
// IMPORTANT - the tool does NOT itself stop AgentRunner.Run;
// finalization is explicit and the loop lifecycle is the caller's
// responsibility. The tool ONLY signals exit by:
// 1. Storing the optional summary via runner.SetFinalSummary so
// FinalSummary observers see it.
// 2. Calling runner.MarkFinalized which atomically flips the
// finalized flag AND closes the finalize channel exactly once
// (idempotent on repeated calls).
// The caller's Run loop selects on runner.EnsureFinalizeCh (or
// polls runner.Finalized) and exits its outer-loop iteration. Per
// CLI usage the loop also surfaces runner.FinalSummary as
// EventCoordinatorSynthesis before disposing the runner.
func FinalizeToolDef(runner RunnerHandle) goai.Tool {
	return goai.Tool{
		Name:        toolNameFinalize,
		Description: "Signal that coordination is complete. The caller's Run loop should exit after this tool returns. Coordinator MUST call this explicitly when it is done routing/narrating - there is no implicit finalization. Optional 'summary' carries the final synthesis text for the caller to surface as EventCoordinatorSynthesis.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string","description":"Optional final synthesis text the caller may surface as EventCoordinatorSynthesis."}}}`),
		Execute: func(_ context.Context, input json.RawMessage) (string, error) {
			var args finalizeArgs
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("finalize: parse args: %w", err)
			}
			runner.SetFinalSummary(args.Summary)
			runner.MarkFinalized()
			return "finalized", nil
		},
	}
}
