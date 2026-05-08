// Package types holds the small, dependency-free contract surface of
// zenflow that can be shared across all internal packages without
// triggering an import cycle. Specifically: ProgressSink + Event +
// EventType + MessageKind + Output (the observability contract) and
// PermissionHandler + PermissionRequest (the tool-gate contract).
// These types were originally defined in package zenflow's
// interfaces.go but were extracted to break circular imports as the
// internal/agent, internal/coord, and internal/exec packages were
// carved out of root. The root package re-exports every symbol via
// type aliases in interfaces_facade.go so the public SDK surface is
// unchanged.
package types

import (
	"context"
	"encoding/json"
	"time"

	"github.com/zendev-sh/goai/provider"
)

// PermissionHandler gates tool execution. Stable.
type PermissionHandler interface {
	RequestPermission(ctx context.Context, req PermissionRequest) (bool, error)
}

// PermissionRequest describes a tool call awaiting approval. Stable.
type PermissionRequest struct {
	RunID    string
	StepID   string
	ToolName string
	ToolArgs json.RawMessage
}

// ProgressSink receives execution events. Stable.
// # ctx semantics for OnEvent
// ctx is request-scoped to the Run that emitted the event. Sinks MUST
// NOT store ctx in long-lived state: the context is cancelled when the
// enclosing Run returns, and a stored ctx will become permanently
// cancelled with no replacement.
// If a sink needs to perform work asynchronously after the Run ends
// (e.g. flushing to a remote log store), it must derive a fresh context
// via context.Background and apply an explicit timeout to that
// child context. Storing the incoming ctx and scheduling async work
// against it will silently lose events after Run exit.
type ProgressSink interface {
	OnEvent(ctx context.Context, event Event)
	OnOutput(ctx context.Context, output Output)
}

// Output represents a streaming output delta from an agent. Stable.
type Output struct {
	RunID     string
	StepID    string
	AgentID   string
	Delta     string
	Done      bool
	Reasoning bool // true when delta is a reasoning/thinking token
}

// Event is a single observable lifecycle event emitted via ProgressSink.OnEvent.
// # Field-population contract (v0.1.0)
// Event is documented Stable. The set of exported fields is frozen for
// v0.1.0 compatibility: fields will not be removed or have their
// documented types changed within the v0.x line. Consumers may rely on
// Event{Type: ...} literals continuing to compile.
// Per-EventType field population (fields not listed carry the zero value):
//	EventWorkflowStart : Type, RunID, Timestamp, Message (workflow name),
// Data["total"]=int (step count).
// Context (FlowContext string, may be empty) is set
// only on the coordinator-inbox push; the public
// ProgressSink emission does not set Context.
//	EventWorkflowEnd : Type, RunID, Timestamp, Duration, Tokens.
//	EventStepStart : Type, RunID, StepID, Timestamp, AgentName,
// Data["index"]=int, Data["total"]=int.
// Loop steps additionally set Data["loop_type"]=string
// ("repeat", "repeat-until", "forEach") and
// Data["items"]=int for forEach steps.
// Include steps additionally set Data["include"]=string.
//	EventStepEnd : Type, RunID, StepID, Timestamp, AgentName,
// Duration, Tokens. Error is zero on success.
// Also emitted by the loop and forEach container
// steps on successful completion.
//	EventStepSkipped : Type, RunID, StepID, Timestamp.
//	EventError : Type, RunID, StepID, Timestamp, Error.
// Duration is set when a step execution duration
// is available (i.e. when a step fails mid-run).
// Storage errors set RunID+StepID; step errors also
// set Duration.
//	EventAgentTurn : Type, RunID, StepID, Timestamp, AgentName,
// Data["phase"]="request"|"response",
// Data["model"]=string.
// "request" phase: Data["turn"]=int (message count).
// "response" phase: Tokens.
//	EventToolCall : Type, RunID, StepID, Timestamp, AgentName,
// Data["phase"]="start"|"end",
// Data["tool_name"]=string,
// Data["tool_call_id"]=string,
// Data["input"]=string (redacted JSON).
// "start" phase: above only.
// "end" phase: additionally Data["output"]=string,
// Duration, Error (nil on success).
// Subagent callers additionally set
// Data["depth"]=int and Data["parentCallID"]=string
// when SpawnDepth > 0.
//	EventMessage : Type, RunID, Timestamp, Message.
// StepID is set when the message is step-scoped
// (e.g. CEL condition skip, forEach cap warning).
// Data is set for some sub-cases (e.g.
// Data["reason"], Data["messageCount"] for resume
// truncation events).
//	EventPlanReady : Type, RunID, Message (workflow name),
// Data["workflow"]=*Workflow.
// Timestamp may be zero (emitted before executor).
//	EventCoordinatorNarration : Type, RunID, StepID, Timestamp, AgentName,
// Message (narration text).
//	EventCoordinatorMessage : Type, RunID, Timestamp, MessageKind, Message,
// Subject (advisory tag).
//	EventCoordinatorSynthesis : Type, RunID, Timestamp, Message.
//	EventCoordinatorInboxMessage : Type, RunID, Timestamp, Message (content),
// MessageKind (default MessageKindContent),
// Data["from"]=string (originating step ID),
// Data["type"]=string (RouterMessageType).
//	EventMessageSent : Type, RunID, StepID (sender), Timestamp,
// Message (text, may be truncated by sinks),
// Data["to"]=string, Data["text"]=string,
// Data["msg_type"]=int (RouterMessageType).
//	EventMessageDropped : Type, RunID, StepID (target), Timestamp,
// Message ("[from -> to]: content"),
// Data["reason"]=string, Data["from"]=string,
// Data["to"]=string, Data["msg_type"]=int.
//	EventAgentInboxDrain : Type, RunID, StepID, Timestamp,
// Message ("[from]: content"),
// Data["from"]=string, Data["msg_type"]=int.
//	EventAgentIdle : Type, RunID, StepID, Timestamp,
// Data["unread_count"]=int (always 0).
//	EventAgentWake : Type, RunID, StepID, Timestamp,
// Data["message_count"]=int, Data["cycle"]=int.
//	EventMaxWakeCyclesWarning : Type, RunID, StepID, Timestamp,
// Data["current_cycle"]=int,
// Data["max_cycles"]=int,
// Data["unread_remaining"]=int.
//	EventResumeStarted : Type, RunID, StepID, Timestamp,
// Data["resumeID"]=string, Data["from"]=string.
//	EventResumeCompleted : Type, RunID, StepID, Timestamp, Duration,
// Data["resumeID"]=string, Data["from"]=string,
// Data["durationMs"]=int64.
//	EventResumeFailed : Type, RunID, StepID, Timestamp,
// Data["resumeID"]=string, Data["from"]=string,
// Data["reason"]=string, Data["durationMs"]=int64.
// Some paths additionally set Data["error"]=string.
//	EventResumeQueued : Type, RunID, StepID, Timestamp,
// Data["resumeID"]=string, Data["from"]=string,
// Data["activeResumeID"]=string.
//	EventTranscriptSealed : Type, RunID, StepID, Timestamp,
// Data["reason"]=string
// ("transcript-too-large" or "store-error"),
// Data["error"]=string.
// For EventTypes not listed, or for fields not listed per EventType, the
// field carries its Go zero value. Consumers must not rely on zero-value
// fields being populated for unlisted EventType/field combinations.
// Stable.
type Event struct {
	Type      EventType
	Timestamp time.Time
	RunID     string
	StepID    string
	AgentName string
	Data      map[string]any
	Duration  time.Duration
	Tokens    *provider.Usage
	Error     error
	Message   string

	// MessageKind classifies coordinator messages. Used only by
	// EventCoordinatorMessage and EventCoordinatorInboxMessage.
	// Values:
	// - MessageKindNotification: status ping, short, no injection
	// into the recipient's LLM context (rendered as a badge).
	// - MessageKindContent: payload to surface to the user or
	// inject into the recipient's LLM context (default).
	// Empty for non-message events.
	MessageKind MessageKind
	// Subject is an optional short tag for message events (e.g.
	// "resume-reply", "follow-up"). Purely advisory; renderers may
	// show it as a header prefix. Empty for non-message events.
	Subject string

	// Context carries the per-call use-case input supplied via
	// RunFlow's WithFlowContext option. Currently populated only on
	// EventWorkflowStart events that the executor pushes into the
	// coordinator runner's mailbox so the coord LLM can curate
	// per-step distribution. Empty for all other events.
	Context string
}

// MessageKind is the discriminator type for coordinator message events.
// Stable.
type MessageKind string

// MessageKind values for Event.MessageKind. Carried by
// EventCoordinatorMessage and EventCoordinatorInboxMessage.
const (
	MessageKindNotification MessageKind = "notification"
	MessageKindContent      MessageKind = "content"
)

// EventType classifies workflow events. Stable.
// The constants below (EventWorkflowStart, EventStepStart, etc.) are part of the Stable surface.
type EventType string

const (
	EventWorkflowStart        EventType = "workflow_start"        // Workflow execution began.
	EventWorkflowEnd          EventType = "workflow_end"          // Workflow execution completed.
	EventStepStart            EventType = "step_start"            // Step execution began.
	EventStepEnd              EventType = "step_end"              // Step execution completed.
	EventStepSkipped          EventType = "step_skipped"          // Step was skipped (failed dep or condition).
	EventAgentTurn            EventType = "agent_turn"            // Emitted by AgentRunner on each LLM request/response.
	EventToolCall             EventType = "tool_call"             // Emitted by AgentRunner around each tool execution (phase=start/end).
	EventMessage              EventType = "message"               // Informational message (e.g., child agent model warning).
	EventError                EventType = "error"                 // Error occurred (step failure, storage error, judge failure).
	EventCoordinatorNarration EventType = "coordinator_narration" // Coordinator narration after a step event.
	EventCoordinatorMessage   EventType = "coordinator_message"   // Coordinator targeted message to a running agent.
	EventCoordinatorSynthesis EventType = "coordinator_synthesis" // Coordinator final synthesis.
	// EventCoordinatorInboxMessage is emitted when the executor drains a
	// RouterMessage addressed to the coordinator inbox (notably reverse
	// replies from resumed steps). Data["from"]=originating step id
	// (e.g. "team-pro"); Data["type"]=RouterMessageType string. Message
	// holds the content. Without this event, a reverse reply would sit
	// unread in the coordinator mailbox forever and operators would never
	// see the resumed agent's response.
	EventCoordinatorInboxMessage EventType = "coordinator_inbox_message"
	// EventMessageSent fires whenever an agent or coord successfully queues
	// a message via send_message (agent to coord) or forward_to_agent
	// (coord to agent). The send is the OUTBOUND side of message visibility;
	// the matching INBOUND event is EventAgentInboxDrain (when the recipient
	// drains the message into its LLM conversation) or
	// EventCoordinatorInboxMessage (for reverse replies into coord).
	// Fields: StepID = sender's StepID. Data["to"] = recipient StepID.
	// Data["text"] = full text sent. Data["msg_type"] = RouterMessageType
	// int. Message = text (sinks may truncate).
	EventMessageSent     EventType = "message_sent"
	EventPlanReady       EventType = "plan_ready"        // Goal decomposition produced a workflow (Data["workflow"] = *Workflow).
	EventAgentInboxDrain EventType = "agent_inbox_drain" // Agent drained ONE RouterMessage into its LLM conversation. Message="[from]: content"; Data["from"]=string, Data["msg_type"]=int (same shape as EventMessageSent / EventMessageDropped). Fires once per message.
	// EventMessageDropped is emitted whenever a router message is
	// discarded without ever reaching the target agent's LLM conversation.
	// Reasons are open-ended strings (Data["reason"]); current values:
	// - "workflow-cancelled": workflow ctx cancelled before delivery.
	// - "target-terminal": Send to a step whose mailbox was closed.
	// - "unknown-step": Send to a stepID that was never registered
	// and has no pending senders.
	// - "mailbox-closed-by-finalize": Send raced a Close; the closed
	// flag won.
	// The contract is: zero silent drops - every drop produces exactly
	// one EventMessageDropped.
	EventMessageDropped EventType = "message_dropped"

	// Agent lifecycle observability events. Emitted by AgentRunner when
	// in mailbox-driven mode (Mailbox+Wake non-nil) so the CLI/TUI can
	// surface idle-waiting and wake-driven resume.
	// - EventAgentIdle: agent finished a goai iteration with no unread
	// messages and is parked waiting for Wake. Data["unread_count"]=0.
	// - EventAgentWake: agent re-entered goai after draining N unread
	// messages. Data["message_count"]=int, Data["cycle"]=int
	// (1-indexed wake cycle).
	// - EventMaxWakeCyclesWarning: wake-loop reached 80% of the
	// configured maxWakeCycles cap. Data: "current_cycle"=int,
	// "max_cycles"=int, "unread_remaining"=int. Operators can use
	// this as an early signal a hot-loop / pathological producer is
	// about to trigger DropReasonMaxWakeCycles.
	EventAgentIdle            EventType = "agent_idle"
	EventAgentWake            EventType = "agent_wake"
	EventMaxWakeCyclesWarning EventType = "max_wake_cycles_warning"

	// Resume Mechanism events. Emitted by Executor.ResumeStep when a
	// router message arrives for a terminated step and the auto-resume
	// path fires.
	// - EventResumeStarted: resume goroutine spawned for the step.
	// Data: "resumeID"=string, "from"=string (original sender).
	// - EventResumeCompleted: resumed AgentRunner returned a final
	// response. Data: "resumeID", "durationMs"=int64, "from",
	// optionally "result".
	// - EventResumeFailed: resume could not complete (ctx cancel,
	// transcript cap, agent error). Data: "resumeID", "from",
	// "reason"=string (e.g. "workflow-shutdown",
	// "transcript-too-large", "agent-error").
	EventResumeStarted   EventType = "resume_started"
	EventResumeCompleted EventType = "resume_completed"
	EventResumeFailed    EventType = "resume_failed"
	// EventResumeQueued: a resume attempt arrived while a resume for the
	// same stepID was already in flight. The incoming message was
	// appended to the running resume's mailbox instead of spawning a
	// second goroutine. Data: "resumeID"=string (queued handle),
	// "from"=string, "activeResumeID"=string (optional; the running
	// handle). Prevents silent accept of queued messages.
	EventResumeQueued EventType = "resume_queued"

	// EventTranscriptSealed: emitted once per stepID when a transcript
	// Append first hits ErrTranscriptTooLarge (or any other store error).
	// Subsequent Appends for the same step are silently suppressed (see
	// AgentRunner.Run flushTranscript). The event carries Data: "stepID",
	// "runID", "reason" (e.g. "transcript-too-large", "store-error").
	// Makes sealing observable mid-Run instead of only surfacing at the
	// next Resume attempt.
	EventTranscriptSealed EventType = "transcript_sealed"
)
