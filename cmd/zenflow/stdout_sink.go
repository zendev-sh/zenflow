package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/zendev-sh/zenflow"
	"github.com/zendev-sh/zenflow/cmd/zenflow/dag"
)

// Compile-time assertion. Catches signature drift on
// zenflow.ProgressSink at the type definition.
var _ zenflow.ProgressSink = (*StdoutSink)(nil)

// dataGet pulls a typed value from an event.Data map; returns the zero T on miss or wrong type.
func dataGet[T any](m map[string]any, key string) T {
	v, _ := m[key].(T)
	return v
}

// StdoutSink writes workflow events to an io.Writer.
// It is safe for concurrent use.
type StdoutSink struct {
	mu               sync.Mutex
	w                io.Writer
	lastWorkflowName string // tracks workflow name for summary/completion headers
	showPlan         bool   // render DAG on plan_ready event
	verbose          bool   // show reasoning content (not just header)
	inReasoning      bool   // true while inside an open reasoning section (for header dedup)
	partialLine      bool   // true iff last write left the cursor mid-line (no trailing \n)
	streamStepID     string // stepID of the currently open agent-response stream ("" = none)
}

// StdoutSinkOption configures a StdoutSink during construction.
// Stable.
type StdoutSinkOption func(*StdoutSink)

// WithStdoutShowPlan enables DAG rendering on plan_ready events.
// Stable.
func WithStdoutShowPlan(v bool) StdoutSinkOption { return func(s *StdoutSink) { s.showPlan = v } }

// WithStdoutVerbose enables showing reasoning content (not just the header).
// Stable.
func WithStdoutVerbose(v bool) StdoutSinkOption { return func(s *StdoutSink) { s.verbose = v } }

// NewStdoutSink creates a sink that writes to os.Stdout.
func NewStdoutSink(opts ...StdoutSinkOption) *StdoutSink {
	return NewStdoutSinkTo(os.Stdout, opts...)
}

// NewStdoutSinkTo creates a sink that writes to the given writer.
func NewStdoutSinkTo(w io.Writer, opts ...StdoutSinkOption) *StdoutSink {
	s := &StdoutSink{w: w}
	for _, o := range opts {
		o(s)
	}
	return s
}

// WithShowPlan enables DAG rendering on plan_ready events.
// Deprecated: use the WithStdoutShowPlan option in NewStdoutSink. Will be removed before v1.0.
func (s *StdoutSink) WithShowPlan(enabled bool) *StdoutSink {
	s.showPlan = enabled
	return s
}

// WithVerbose enables showing reasoning content (not just "Thinking..." header).
// Deprecated: use the WithStdoutVerbose option in NewStdoutSink. Will be removed before v1.0.
func (s *StdoutSink) WithVerbose(enabled bool) *StdoutSink {
	s.verbose = enabled
	return s
}

// OnEvent handles a workflow event by printing it.
func (s *StdoutSink) OnEvent(_ context.Context, event zenflow.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Close any pending text/reasoning stream so the event renders on a
	// fresh line. Without this, an EventAgentTurn or EventToolCall fired
	// while text deltas are still being flushed glues the event onto the
	// last line of streamed text (B-N1).
	s.flushPendingLine()

	switch event.Type {
	case zenflow.EventWorkflowStart:
		s.inReasoning = false
		s.lastWorkflowName = event.Message
		fmt.Fprintf(s.w, "%s %s\n", C(Cyan, "▸ Starting workflow:"), C(Cyan+Bold, event.Message))

	case zenflow.EventStepStart:
		s.inReasoning = false // reset for new step
		index := dataGet[int](event.Data, "index")
		total := dataGet[int](event.Data, "total")
		agent := event.AgentName
		if agent == "" {
			agent = "default"
		}
		fmt.Fprintf(s.w, "%s %s %s\n", C(Cyan, fmt.Sprintf("▸ Step %d/%d:", index+1, total)), C(Cyan+Bold, event.StepID), C(Dim, "("+agent+")"))

	case zenflow.EventStepEnd:
		fmt.Fprintf(s.w, "%s %s\n", C(Success, "✓ ["+event.StepID+"] completed"), C(Dim, "("+event.Duration.String()+")"))

	case zenflow.EventStepSkipped:
		// §9.A normalisation: skipped means "not run" not "warning" -
		// glyph `○` (U+25CB) with Muted color (grey) across both CLI
		// and TUI. Was `⊘` Warning yellow.
		fmt.Fprintf(s.w, "%s\n", C(Muted, "○ ["+event.StepID+"] skipped"))

	case zenflow.EventError:
		fmt.Fprintf(s.w, "%s %v\n", C(ErrorFG, fmt.Sprintf("✗ [%s]", event.StepID)), event.Error)

	case zenflow.EventCoordinatorNarration:
		fmt.Fprintf(s.w, "%s %s\n", C(Info, fmt.Sprintf("≋ [%s]", event.StepID)), event.Message)

	case zenflow.EventCoordinatorMessage:
		fmt.Fprintf(s.w, "%s %s\n", C(Magenta, fmt.Sprintf("⇢ [%s]", event.StepID)), event.Message)

	case zenflow.EventMessageSent:
		// Outbound side of message visibility - fires when send_message
		// (agent → coord) or forward_to_agent (coord → agent) successfully
		// queues. ⇠ points OUT of the sender's bracket per convention
		// (⇠ = sent, ⇢ = received). Truncate long content for readability.
		to := dataGet[string](event.Data, "to")
		if to == "" {
			to = "?"
		}
		text := dataGet[string](event.Data, "text")
		if text == "" {
			text = event.Message
		}
		const maxSent = 240
		if len(text) > maxSent {
			text = text[:maxSent] + "..."
		}
		// Color matches EventAgentInboxDrain (received) so the sent/received
		// pair reads as one symmetric flow.
		fmt.Fprintf(s.w, "%s %s %s\n",
			C(Info, fmt.Sprintf("⇠ [%s]", event.StepID)),
			C(Dim, "sent to "+to+":"),
			text)

	case zenflow.EventCoordinatorInboxMessage:
		// reverse reply drained from the coordinator
		// inbox (typically from a resumed step). Uses Info (#3b82f6)
		// to match the "message received" semantic across the TUI/CLI.
		// Truncate long content so the terminal line stays readable.
		from := dataGet[string](event.Data, "from")
		if from == "" {
			from = "?"
		}
		content := event.Message
		const maxContent = 240
		if len(content) > maxContent {
			content = content[:maxContent] + "..."
		}
		fmt.Fprintf(s.w, "%s %s\n",
			C(Info, fmt.Sprintf("≋ [coordinator] from=%s (resumed):", from)),
			content)

	case zenflow.EventAgentInboxDrain:
		// Agent received coordinator-routed message. Info (#3b82f6)
		// matches the "incoming message from user/coordinator"
		// semantic used in chat rendering.
		from := dataGet[string](event.Data, "from")
		if from == "" {
			from = "?"
		}
		fmt.Fprintf(s.w, "%s %s\n", C(Info, fmt.Sprintf("⇢ [%s]", event.StepID)), C(Dim, "received from "+from))

	case zenflow.EventAgentIdle:
		// B6 /: agent finished a goai iteration with no unread
		// messages and is parked. Render as a low-key idle marker.
		fmt.Fprintf(s.w, "%s\n", C(Dim, fmt.Sprintf("· [%s] idle", event.StepID)))

	case zenflow.EventAgentWake:
		// B6 /: agent re-entered goai after draining N messages.
		count := dataGet[int](event.Data, "message_count")
		cycle := dataGet[int](event.Data, "cycle")
		fmt.Fprintf(s.w, "%s %s\n",
			C(Cyan, fmt.Sprintf("↻ [%s] wake", event.StepID)),
			C(Dim, fmt.Sprintf("msgs=%d cycle=%d", count, cycle)))

	case zenflow.EventMaxWakeCyclesWarning:
		// B6 / B3: emitted at 80% of MaxWakeCycles cap - operators get an
		// early heads-up before max-wake-cycles drops fire.
		cur := dataGet[int](event.Data, "current_cycle")
		maxC := dataGet[int](event.Data, "max_cycles")
		unread := dataGet[int](event.Data, "unread_remaining")
		fmt.Fprintf(s.w, "%s %s\n",
			C(Warning, fmt.Sprintf("⚠ [%s] wake-cycles approaching cap", event.StepID)),
			C(Dim, fmt.Sprintf("cycle=%d/%d unread=%d", cur, maxC, unread)))

	case zenflow.EventResumeStarted:
		// resumed step lifecycle rendering.
		from := dataGet[string](event.Data, "from")
		if from == "" {
			from = "?"
		}
		fmt.Fprintf(s.w, "%s %s\n",
			C(Cyan, fmt.Sprintf("↺ [%s] resumed by %s", event.StepID, from)),
			C(Dim, "(resume started)"))

	case zenflow.EventResumeQueued:
		// F4 - a resume message was appended to an
		// in-flight resume's mailbox instead of spawning a new
		// goroutine. Render dim gray so operators see the event
		// without confusing it with a fresh resume start. VA-4b:
		// include activeResumeID so operators can correlate with the
		// running EventResumeStarted.
		from := dataGet[string](event.Data, "from")
		if from == "" {
			from = "?"
		}
		active := dataGet[string](event.Data, "activeResumeID")
		tail := "(queued into in-flight resume)"
		if active != "" {
			tail = fmt.Sprintf("(active=%s)", active)
		}
		fmt.Fprintf(s.w, "%s %s\n",
			C(Dim, fmt.Sprintf("⋯ [%s] resume queued by %s", event.StepID, from)),
			C(Dim, tail))

	case zenflow.EventTranscriptSealed:
		// G4 - transcript store hit its cap (or IO error) and
		// further appends will be silently suppressed. Render yellow
		// so operators notice mid-Run.
		reason := dataGet[string](event.Data, "reason")
		if reason == "" {
			reason = "unknown"
		}
		fmt.Fprintf(s.w, "%s %s\n",
			C(Warning, fmt.Sprintf("✂ [%s] transcript sealed", event.StepID)),
			C(Dim, "reason="+reason))

	case zenflow.EventResumeCompleted:
		ms := dataGet[int64](event.Data, "durationMs")
		fmt.Fprintf(s.w, "%s %s\n",
			C(Success, fmt.Sprintf("↻ [%s] resume done", event.StepID)),
			C(Dim, fmt.Sprintf("(%dms)", ms)))

	case zenflow.EventResumeFailed:
		reason := dataGet[string](event.Data, "reason")
		if reason == "" {
			reason = "unknown"
		}
		fmt.Fprintf(s.w, "%s %s\n",
			C(Warning, fmt.Sprintf("⚠ [%s] resume failed", event.StepID)),
			C(Dim, "reason="+reason))

	case zenflow.EventMessageDropped:
		// surface every router-side or workflow-abort
		// drop so the "zero silent drops" contract is observable in the
		// CLI. Reason is the load-bearing field; from/step provide
		// localization context.
		from := dataGet[string](event.Data, "from")
		if from == "" {
			from = "?"
		}
		reason := dataGet[string](event.Data, "reason")
		if reason == "" {
			reason = "unknown"
		}
		// soften "workflow-cancelled" drops to INFO (○ Dim)
		// instead of WARN (⚠). These drops happen at workflow
		// shutdown when coord still has unprocessed mailbox messages
		// (more frequent on verbose models like MiniMax with longer
		// LLM cycles). Expected timing artifact, not an error. Other
		// drop reasons (coord-down, unknown-step, cap-exhaustion,
		// etc.) remain WARN since they indicate real routing/capacity
		// issues mid-flight.
		if reason == "workflow-cancelled" {
			fmt.Fprintf(s.w, "%s %s\n",
				C(Dim, fmt.Sprintf("○ msg-dropped [%s]", event.StepID)),
				C(Dim, fmt.Sprintf("from=%s reason=%s (expected at shutdown)", from, reason)))
		} else {
			fmt.Fprintf(s.w, "%s %s\n",
				C(Warning, fmt.Sprintf("⚠ msg-dropped [%s]", event.StepID)),
				C(Dim, fmt.Sprintf("from=%s reason=%s", from, reason)))
		}

	case zenflow.EventMessage:
		if event.StepID != "" {
			fmt.Fprintf(s.w, "%s %s\n", C(Warning, fmt.Sprintf("⚠ [%s]", event.StepID)), event.Message)
		} else {
			fmt.Fprintf(s.w, "%s %s\n", C(Warning, "⚠"), event.Message)
		}

	case zenflow.EventToolCall:
		phase := dataGet[string](event.Data, "phase")
		if phase != "end" {
			return
		}
		name := dataGet[string](event.Data, "tool_name")
		icon := toolIcon(name)
		input := dataGet[string](event.Data, "input")
		argPreview := toolArgsPreview(name, input)
		header := fmt.Sprintf("%s [%s] %s", icon, event.StepID, name)
		if argPreview != "" {
			header = fmt.Sprintf("%s [%s] %s %s", icon, event.StepID, name, C(Dim, argPreview))
		}
		// Tool header format: the status glyph (✓/×) carries the
		// Success/Error color; the tool icon + stepID + name render in
		// default FG so only the status stands out. Elapsed + error
		// detail use Dim.
		if event.Error != nil {
			fmt.Fprintf(s.w, "%s %s %s\n",
				C(ErrorFG, "×"),
				header,
				C(Dim, fmt.Sprintf("failed: %v", event.Error)))
		} else {
			fmt.Fprintf(s.w, "%s %s %s\n",
				C(Success, "✓"),
				header,
				C(Dim, "("+event.Duration.String()+")"))
		}
		if s.verbose {
			if out, ok := event.Data["output"].(string); ok && out != "" {
				renderToolOutput(s.w, out)
			}
		}

	case zenflow.EventAgentTurn:
		if !s.verbose {
			return
		}
		phase := dataGet[string](event.Data, "phase")
		if phase != "response" || event.Tokens == nil {
			return
		}
		// Per-turn token summary. Uses Σ glyph + Dim color across the
		// whole line so it reads as a quiet trailing stat - visually
		// distinct from the colored ◎ Thinking… header (B-N3).
		fmt.Fprintf(s.w, "%s\n",
			C(Dim, fmt.Sprintf("Σ [%s] turn (in=%d, out=%d)",
				event.StepID, event.Tokens.InputTokens, event.Tokens.OutputTokens)))

	case zenflow.EventCoordinatorSynthesis:
		name := s.lastWorkflowName
		if name != "" {
			fmt.Fprintf(s.w, "%s %s\n", C(Info, fmt.Sprintf("≋ [%s] Summary:", name)), event.Message)
		} else {
			fmt.Fprintf(s.w, "%s %s\n", C(Info, "≋ Summary:"), event.Message)
		}

	case zenflow.EventPlanReady:
		if s.showPlan {
			if wf, ok := event.Data["workflow"].(*zenflow.Workflow); ok {
				fmt.Fprint(s.w, dag.Render(wf))
				fmt.Fprintln(s.w)
			}
		}

	case zenflow.EventWorkflowEnd:
		name := s.lastWorkflowName
		if name != "" {
			fmt.Fprintf(s.w, "%s %s\n", C(Success, "✓ ["+name+"] completed"), C(Dim, "("+event.Duration.String()+")"))
		} else {
			fmt.Fprintf(s.w, "%s %s\n", C(Success, "✓ Workflow completed"), C(Dim, "("+event.Duration.String()+")"))
		}
	}
}

// OnOutput handles streaming agent output by printing deltas.
// Only used for agent output (--verbose --stream). Coordinator output
// is buffered and emitted via OnEvent (narration/synthesis/message events).
func (s *StdoutSink) OnOutput(_ context.Context, output zenflow.Output) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if output.Reasoning {
		// If we left the cursor mid-line on a non-reasoning write, close
		// it before opening the reasoning header. (Don't add a newline if
		// we're already inside an open reasoning section - its header
		// terminated the previous line.)
		if s.partialLine && !s.inReasoning {
			fmt.Fprintln(s.w)
			s.partialLine = false
		}
		if !s.inReasoning {
			// Thinking header uses the Thinking truecolor (#7c3aed).
			// Reasoning deltas below stay Dim so they don't visually
			// overpower the primary output stream. The header itself is
			// line-terminated, so partialLine stays false until a delta
			// arrives without \n.
			fmt.Fprintf(s.w, "%s\n", C(Thinking, fmt.Sprintf("◎ [%s] Thinking...", output.StepID)))
			s.inReasoning = true
		}
		if s.verbose && output.Delta != "" {
			fmt.Fprint(s.w, C(Dim, output.Delta))
			s.partialLine = !strings.HasSuffix(output.Delta, "\n")
		}
		return
	}
	// Transitioning out of a reasoning section: only emit a closing \n
	// if reasoning deltas left the line un-terminated. A bare header (no
	// deltas) is already line-terminated and needs nothing.
	if s.inReasoning {
		if s.partialLine {
			fmt.Fprintln(s.w)
			s.partialLine = false
		}
		s.inReasoning = false
	}
	if output.Delta != "" {
		// Open or switch the agent-response block. Format mirrors
		// EventCoordinatorNarration so any LLM-authored prose addressed
		// to the user (narration, summary, agent response) renders with
		// the same `≋ [stepID]` prefix. On parallel runs, a stepID
		// switch closes the previous line and re-emits the prefix so
		// every chunk stays attributable.
		if s.streamStepID != output.StepID {
			if s.partialLine {
				fmt.Fprintln(s.w)
				s.partialLine = false
			}
			fmt.Fprintf(s.w, "%s ", C(Info, fmt.Sprintf("≋ [%s]", output.StepID)))
			s.streamStepID = output.StepID
			s.partialLine = true
		}
		fmt.Fprint(s.w, output.Delta)
		s.partialLine = !strings.HasSuffix(output.Delta, "\n")
	}
	if output.Done {
		if s.partialLine {
			fmt.Fprintln(s.w)
			s.partialLine = false
		}
		s.streamStepID = ""
	}
}

// flushPendingLine writes a trailing newline if the cursor is mid-line,
// so a subsequent event renders on a fresh line. Caller must hold s.mu.
func (s *StdoutSink) flushPendingLine() {
	if s.partialLine {
		fmt.Fprintln(s.w)
		s.partialLine = false
	}
	// An unconsumed reasoning section without partial output also closes
	// here (header is already terminated; just clear the flag so the next
	// reasoning Output re-emits its header for clarity).
	s.inReasoning = false
	// Close any open agent-response stream so the next stream re-emits
	// its `≋ [stepID]` prefix. Otherwise a delta arriving after an
	// unrelated event would silently glue onto the previous block.
	s.streamStepID = ""
}

// toolArgsPreview renders a short, human-readable preview of the most
// salient tool argument (e.g. file_path for read/write, command for bash)
// so operators can see what's being acted on without enabling --verbose.
// Returns empty when input is unparseable or carries no useful field.
func toolArgsPreview(toolName, raw string) string {
	if raw == "" || raw == "{}" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ""
	}
	var key string
	switch toolName {
	case "bash":
		key = "command"
	case "read", "write", "edit":
		key = "file_path"
	case "grep", "glob":
		key = "pattern"
	case "fetch":
		key = "url"
	case "ls":
		key = "path"
	}
	pick := func(k string) string {
		if v, ok := m[k].(string); ok {
			return v
		}
		return ""
	}
	val := ""
	if key != "" {
		val = pick(key)
	}
	if val == "" {
		// Fallback: try common alternate keys before giving up.
		for _, k := range []string{"file_path", "path", "command", "pattern", "url", "query"} {
			if v := pick(k); v != "" {
				val = v
				break
			}
		}
	}
	if val == "" {
		return ""
	}
	return truncatePreview(val, 80)
}

func truncatePreview(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// renderToolOutput prints the tool's stdout/result in dim, indented form
// when verbose is on. Caps at maxToolOutputBytes to keep terminals usable
// for grep/read against large files.
const maxToolOutputBytes = 4096

func renderToolOutput(w io.Writer, out string) {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return
	}
	truncated := false
	if len(out) > maxToolOutputBytes {
		out = out[:maxToolOutputBytes]
		truncated = true
	}
	for line := range strings.SplitSeq(out, "\n") {
		fmt.Fprintf(w, "  %s\n", C(Dim, line))
	}
	if truncated {
		fmt.Fprintf(w, "  %s\n", C(Dim, "… (output truncated)"))
	}
}

// toolIcon returns the symbol for a tool name.
// Falls back to ◆ for unknown tools.
func toolIcon(name string) string {
	switch name {
	case "read":
		return "◇"
	case "edit":
		return "✎"
	case "write":
		return "✐"
	case "bash":
		return "⚙"
	case "grep":
		return "⊙"
	case "glob":
		return "⛶"
	case "fetch":
		return "⇄"
	case "task":
		return "✦"
	case "ls":
		return "⊞"
	default:
		return "◆"
	}
}
