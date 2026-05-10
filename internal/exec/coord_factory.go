package exec

import (
	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/coord"
)

// NewDefaultCoordRunner factory: CLI convenience helper that returns a
// pre-configured *AgentRunner suitable for use as a workflow coordinator.
// The returned runner is wired with the three default coord tools
// (forward_to_agent, narrate, finalize) and a default coord system prompt;
// the caller supplies the LanguageModel and may extend the configuration
// via CoordOption.
// Lifecycle ownership: the factory ONLY constructs the runner - it does
// NOT start the runner's Run loop. The caller is responsible for calling
// runner.Run(...) on its own goroutine and for disposing the runner when
// the workflow finishes.

// coordCoordinatorStepID is the canonical StepID assigned to a
// factory-built coord runner. The Executor uses this same constant for
// reverse-reply inbox routing (`CoordRouterInboxID`); keeping the
// factory in lockstep ensures forward_to_agent / narrate / send_message
// addressing all line up by default.
const coordCoordinatorStepID = coord.CoordRouterInboxID

// coordDefaultMaxWakeCycles is the wake-cycle cap the factory installs
// on the coord runner. The package-level defaultMaxWakeCycles (10)
// is calibrated for STEP runners, which have a short lifecycle (a few
// LLM iterations + idle exit). Coord is long-lived across the whole
// workflow and absorbs every step lifecycle event plus every bridged
// send_message - easily exceeding 10 wake cycles for non-trivial
// workflows. Hitting the cap causes the runner to MarkRead remaining
// unread messages and emit EventMessageDropped{reason:max-wake-cycles},
// meaning lifecycle events get lost permanently. Bumping to 100
// gives ample headroom; debate-until.yaml live runs use ~12 cycles.
// Override per-instance via WithCoordMaxWakeCycles(n).
const coordDefaultMaxWakeCycles = 100

// DefaultCoordSystemPrompt is the system prompt installed by
// NewDefaultCoordRunner when the caller does not override it.
// Exported so consumers can append integration-specific guidance
// without losing the tested baseline:
//
//	zenflow.NewDefaultCoordRunner(llm,
//
// zenflow.WithCoordSystemPrompt(zenflow.DefaultCoordSystemPrompt + extras))
// or via the convenience option:
//
//	zenflow.NewDefaultCoordRunner(llm,
//
// zenflow.WithCoordSystemPromptSuffix(extras))
// The prompt names every default tool (forward_to_agent, narrate,
// finalize) so a future rename will trip
// TestDefaultCoordSystemPrompt_MentionsTools and force the prompt to
// stay in lockstep with the tool surface.
// The prompt body is deliberately compact. See SPEC.md for the
// coordinator contract (event semantics, recommended decision flow,
// budget guidance, narration cadence).
const DefaultCoordSystemPrompt = `You are the workflow coordinator.

OPERATING MODE - autonomous coordinator, no human in loop:

Workflows run as automation (CI/CD, scheduled jobs, embedded
services). There is NO interactive user to ask for clarification. You
are an autonomous agent making decisions from the events arriving in
your mailbox. Mailbox events are SELF-EXPLANATORY - they carry step
IDs, status, content, and progress counters sufficient for you to
decide your next action.

You MUST NOT:
- Ask the user for clarification ("Could you clarify what action you'd
  like me to take?", "Please provide workflow definitions", etc.).
- Wait for human input before acting on events.
- Treat large message content (e.g. agent argument text) as a
  "document to analyze for the user" - it is event content from a
  step agent; narrate / forward it as appropriate, do not request
  clarification about it.
- Output meta-conversational stance ("I need more context to
  continue", "I'm not detecting an active workflow", "Please provide
  the workflow definitions"). Even if the mailbox state seems
  ambiguous, ACT on what you have: narrate the event you received,
  or stay silent until the next event arrives.

When uncertain, prefer SILENCE (no tool call, exit naturally) over
asking for clarification - the next mailbox event will resolve
ambiguity. The CLI re-invokes you on the next wake.

Your job: monitor step lifecycle events arriving in your mailbox
(EventStepStart / EventStepEnd / EventStepSkipped / EventError /
EventToolCall / EventCoordinatorInboxMessage) and decide, after each
event, whether the workflow needs orchestration help. Reverse replies
from resumed steps arrive as EventCoordinatorInboxMessage. You have
three tools:

- forward_to_agent(target_step_id, text, kind?) - route a message into
  a running step's mailbox. Use kind="context_update" to inject new
  context, kind="cancel" to ask a step to stop, or omit/use "info" for
  a general note. Hub-to-spoke only - agents reply by sending a
  message back to you (you are the hub), never directly to siblings.

  ADDRESSING RULES (these prevent unknown-step drops):
  • target_step_id is the STEP ID (the YAML "id:" field), NOT the
    agent name. Example: a step with id=list_services agent=discovery
    is addressed as forward_to_agent("list_services", ...), NOT
    forward_to_agent("discovery", ...). The events you receive carry
    the correct step ID in their from= / step= fields - mirror
    whatever you see there.
  • Inner-DAG steps (inside a loop / forEach / include) use NAMESPACED
    IDs:
      - repeat-until iter N: parentLoopID.N.innerStepID
        (e.g. debate-rounds.0.pro-argue)
      - forEach item N:      parentLoopID[N].innerStepID
        (e.g. deploy[0].deploy_step)
      - include sub-workflow: includeStepID.subStepID
    Use the SAME namespaced form when forwarding back. Events you
    receive ALREADY carry the namespaced form - just mirror it.
  • ONLY forward to step IDs that have appeared in mailbox events
    you have RECEIVED. Do NOT infer or invent step IDs from workflow
    domain semantics. For example: do NOT forward to "pro-rebuttal"
    just because debates typically have rebuttals; do NOT forward to
    "round-2-summary" or "next-iteration-X" anticipating future
    iterations that have not started yet. If a step ID has not
    appeared in any event, it does not exist - do not address it.
  • If you have ANALYSIS or CONTEXT the workflow's existing steps
    don't need (e.g. synthesis of arguments after both sides
    finished), NARRATE it for the user instead of forwarding. The
    narrate tool is the right channel for "user-facing analysis";
    forward_to_agent is ONLY for messages that a specific running
    step actually needs.

  RECOVERY RULE - when forward_to_agent returns "dropped: ...":
    The tool result tells you what went wrong AND lists currently
    available step IDs. In your CURRENT response (do NOT exit the
    turn), take ONE of these recovery actions:
      (a) Retry forward_to_agent with a CORRECT target ID from the
          available list.
      (b) Call narrate(text=...) with the same content to surface
          it for the user.
    The system DOES preserve dropped content as fallback narration
    automatically, but acting in the same turn (option a or b above)
    produces cleaner UX and avoids leaving the LLM-generated content
    only available via the fallback path.
- narrate(text) - emit a user-facing narration message explaining your
  reasoning, summarising a step result, or surfacing context. Does not
  route to step agents.
- finalize(summary?) - signal that coordination is complete. The
  caller's run loop will exit after this returns and you will NOT
  process any more events. See Termination rules below - finalize is
  one-way and irreversible.

Narration cadence:

- Narrate ONCE per significant event:
  - step START: one sentence "<agent> started <step-id>"
  - agent message via send_message: 1-2 sentences acknowledging it
  - step COMPLETE: one sentence summarising the outcome
- AT MOST ONE narrate per wake. If multiple events arrive together
  (e.g. step A complete + step B start in the same wake), synthesize
  them into ONE narration that addresses the most important event
  (priority: agent message > step start > step complete).
- NEVER repeat a narration you already emitted in a prior turn.
  Check your conversation history before narrating - if the text
  would be substantially identical to a recent narration, skip it.
- Skip narration entirely when the event carries no new information
  (duplicate wakes, system events, your own tool replies).
- Do NOT generate filler text when the mailbox is empty - exit
  naturally and wait for the next wake.

Forwarding:

- Use forward_to_agent when a downstream step would benefit from a
  sibling step's output mid-flight. Example: pro-argue's argument
  could be forwarded to a research-helper step running in parallel.
- Do not duplicate context that the step's own dependsOn chain
  already supplies.
- If asked a question via send_message that requires another step's
  data, forward your reply via forward_to_agent - DO NOT keep the
  question to yourself; the asking step is waiting.

Termination - prevent premature finalize:

- Call finalize EXACTLY ONCE when the workflow has reached its
  terminal state. The terminal state is reached when ALL declared
  steps have emitted EventStepEnd (status=completed or failed).
- Do NOT finalize after the first narration. Do NOT finalize while
  any step is still running. Do NOT finalize when uncertain - when
  uncertain, exit naturally (no tool calls) and wait for more events.
- A safe heuristic: only finalize after you receive an EventStepEnd
  for what you believe is the LAST step in the workflow AND no other
  steps are still pending in the mailbox.
- Without finalize the run loop will eventually exit on its own when
  the executor finishes - finalize is a hint, not a requirement.
`

// CoordOption configures a runner built by NewDefaultCoordRunner. Stable.
// Options follow the standard functional-options pattern. Apply via
// the variadic argument: NewDefaultCoordRunner(llm, SynthesizeOnly,
// WithCoordTools(myTool)).
type CoordOption func(*coordConfig)

// coordConfig is the internal accumulator for CoordOption calls. It
// holds the decisions a NewDefaultCoordRunner caller can express
// before the factory materialises them onto an *AgentRunner. Fields
// are deliberately private - callers configure via CoordOption
// constructors only, not by mutating this struct directly.
type coordConfig struct {
	// synthesizeOnly, when true, instructs the factory to omit the
	// `narrate` tool from the coord runner. The coord LLM still
	// receives forward_to_agent (for routing) and finalize (mandatory
	// exit signal); the synthesised final answer is surfaced via the
	// finalize summary argument instead of incremental narrations.
	synthesizeOnly bool

	// extraTools holds caller-supplied tools accumulated across
	// WithCoordTools option calls. The factory APPENDS them to the
	// default coord tool set (in registration order); pure replacement
	// is not exposed via options because additive flexibility for SDK
	// consumers is preferred. Callers who want full replacement can
	// mutate the returned runner.Tools directly.
	extraTools []goai.Tool

	// systemPrompt overrides the default coord system prompt. Empty
	// means use DefaultCoordSystemPrompt. Exposed so consumers
	// (SDK consumers, custom debate hosts, narration-heavy workflows)
	// can tune narration cadence without forking the package or
	// mutating the runner post-construction.
	systemPrompt string

	// maxWakeCycles overrides the wake-cycle cap set on the coord
	// runner. Zero (default) means use coordDefaultMaxWakeCycles.
	// Long-running aggregator workflows can bump this to avoid
	// DropReasonMaxWakeCycles when many lifecycle events +
	// bridged send_message arrivals accumulate.
	maxWakeCycles int

	// contextProvider, when non-nil, is invoked before every
	// GenerateText call inside the coord runner (initial call + each
	// wake-driven re-entry). Its return string is injected as a fresh
	// user-role message wrapped in <dynamic-context> tags. Empty /
	// whitespace-only returns are skipped. Set via
	// WithCoordContextProvider; the factory threads it through to the
	// runner via WithRunnerWakeContextProvider.
	contextProvider func() string
}

// SynthesizeOnly returns a CoordOption that drops the `narrate` tool
// from the default coord tool set. The coord LLM still has
// forward_to_agent (so it can curate context for in-flight steps) and
// finalize (mandatory exit signal with optional synthesis text), but
// can no longer interject mid-workflow narrations. Use this for the
// CLI `--summary-only` mode where the user wants a single final
// synthesis rather than per-step commentary. Stable.
func SynthesizeOnly() CoordOption {
	return func(c *coordConfig) { c.synthesizeOnly = true }
}

// WithCoordTools returns a CoordOption that APPENDS the supplied
// tools to the default coord tool set. Multiple WithCoordTools calls
// chain; each call's tools are appended after any previously
// accumulated tools in registration order. Stable.
// This is the SDK-flexibility hook: callers (CLI plugins, embedded
// SDK consumers, downstream embedders) can extend the coord
// with bespoke tools (e.g. a "request_user_input" tool that surfaces
// a prompt to the human, or a "consult_runbook" tool that looks up an
// SOP) without giving up the standard forward / narrate / finalize
// defaults.
// To replace the default tools entirely instead of appending, mutate
// the runner returned by NewDefaultCoordRunner directly:
//
//	runner := NewDefaultCoordRunner(llm)
//	runner.Tools = []goai.Tool{myReplacementSet...}
//
// - but doing so removes finalize, so the Run loop will not exit
// without a manual replacement signal.
func WithCoordTools(tools ...goai.Tool) CoordOption {
	return func(c *coordConfig) {
		c.extraTools = append(c.extraTools, tools...)
	}
}

// WithCoordMaxWakeCycles returns a CoordOption that overrides the
// coord runner's wake-cycle cap. Zero AND negative values both keep
// coordDefaultMaxWakeCycles (100) - the factory only honours strictly-
// positive overrides (see NewDefaultCoordRunner: `if cfg.maxWakeCycles
// > 0`). The coord runner is long-lived; the per-Run cap limits wake-
// driven re-entries inside one Run invocation. Set higher (e.g. 250)
// for workflows with many long-running parallel steps + frequent
// send_message bridging that could exceed the 100 default in one Run
// window. Stable.
// There is intentionally no path through this option to the package-
// level `defaultMaxWakeCycles` (10) - the coord cap is set inside
// NewDefaultCoordRunner with `coordDefaultMaxWakeCycles` (100). Callers
// who want the smaller per-step cap should set the runner's MaxWakeCycles
// field directly after factory construction.
func WithCoordMaxWakeCycles(n int) CoordOption {
	return func(c *coordConfig) { c.maxWakeCycles = n }
}

// WithCoordContextProvider returns a CoordOption that installs a
// callback the coord runner invokes before every GenerateText call
// (initial + each wake-driven re-entry). The callback's return string
// is appended as a fresh user-role message wrapped in
// <dynamic-context> tags so the LLM can distinguish ambient state
// from in-band conversation. Empty / whitespace-only returns are
// skipped so an idle provider does not pollute the message stream.
// Use this hook when a chat-driven UX needs ambient context
// refreshed every wake without re-engineering the system prompt:
// currently-open files, repo metadata, session topic, recent user
// actions. Pass nil to disable. The callback runs synchronously on
// the runner goroutine; keep it cheap (microseconds) and
// goroutine-safe; it must not call back into the runner.
// Stable.
func WithCoordContextProvider(fn func() string) CoordOption {
	return func(c *coordConfig) { c.contextProvider = fn }
}

// WithCoordSystemPrompt returns a CoordOption that overrides the
// default coord system prompt. Empty string is a no-op (default prompt
// is used). Exposed for consumers that want stricter or looser
// narration cadence than the defaults provide. The override fully
// replaces the default; callers who want to extend rather than replace
// should concatenate DefaultCoordSystemPrompt with extra text or use
// the convenience option WithCoordSystemPromptSuffix. Stable.
func WithCoordSystemPrompt(prompt string) CoordOption {
	return func(c *coordConfig) { c.systemPrompt = prompt }
}

// WithCoordSystemPromptSuffix returns a CoordOption that appends extra
// guidance to the default coord system prompt. Convenience for the
// common "keep tested baseline + add integration-specific guidance"
// pattern (e.g. a chat consumer appending session-addressing hints, a
// reviewer agent appending policy reminders). Empty string is a no-op.
// Last-write-wins with WithCoordSystemPrompt: if both are supplied,
// whichever appears later in the option list takes effect. Stable.
func WithCoordSystemPromptSuffix(extra string) CoordOption {
	if extra == "" {
		return func(*coordConfig) {}
	}
	return func(c *coordConfig) { c.systemPrompt = DefaultCoordSystemPrompt + extra }
}

// NewDefaultCoordRunner returns a pre-configured *AgentRunner suitable
// for use as a workflow coordinator. The returned runner has:
// - StepID = "coordinator" (matches Executor's reverse-reply inbox key)
// - Mailbox = a fresh in-memory MailboxStore (events from the
// executor land here; the runner's Run loop drains them on each wake)
// - Wake = a freshly allocated cap-1 buffered channel so the executor
// can signal mailbox-driven re-entry of the coord's goai tool loop
// after each lifecycle event push. AgentRunner.Run only enters
// mailbox-mode when BOTH Mailbox AND Wake are non-nil; without Wake
// the coord drains its mailbox once at Run-start and never re-enters
// to react to subsequent EventStepStart / EventStepEnd / EventError
// pushes. Stable.
// - Model = the supplied LanguageModel (may be nil if the caller is
// constructing a routing-only runner for tests)
// - Tools = the three defaults (forward_to_agent, narrate, finalize),
// plus any extras from WithCoordTools, minus narrate when
// SynthesizeOnly is set
// - SystemPrompt = DefaultCoordSystemPrompt
// - WakeContextProvider = supplied via WithCoordContextProvider, if
// any; nil disables per-wake context injection.
// The factory does NOT start runner.Run - caller owns the lifecycle.
func NewDefaultCoordRunner(llm provider.LanguageModel, opts ...CoordOption) *AgentRunner {
	cfg := coordConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}

	systemPrompt := DefaultCoordSystemPrompt
	if cfg.systemPrompt != "" {
		systemPrompt = cfg.systemPrompt
	}
	maxWakeCycles := coordDefaultMaxWakeCycles
	if cfg.maxWakeCycles > 0 {
		maxWakeCycles = cfg.maxWakeCycles
	}
	runnerOpts := []AgentRunnerOption{
		WithRunnerStepID(coordCoordinatorStepID),
		WithRunnerMailbox(NewInMemoryMailboxStore()),
		WithRunnerWake(make(chan struct{}, 1)),
		WithRunnerModel(llm),
		WithRunnerSystemPrompt(systemPrompt),
		WithRunnerMaxWakeCycles(maxWakeCycles),
	}
	if cfg.contextProvider != nil {
		runnerOpts = append(runnerOpts, WithRunnerWakeContextProvider(cfg.contextProvider))
	}
	runner := NewAgentRunner(runnerOpts...)

	// Build the default tool set. Tools close over `runner` so the
	// finalize tool's flag-flip / channel-close lands on the SAME
	// AgentRunner the caller will run.
	tools := make([]goai.Tool, 0, 3+len(cfg.extraTools))
	tools = append(tools, coord.ForwardToAgentToolDef(runner))
	if !cfg.synthesizeOnly {
		tools = append(tools, coord.NarrateToolDef(runner))
	}
	tools = append(tools, coord.FinalizeToolDef(runner))
	tools = append(tools, cfg.extraTools...)
	runner.tools = tools

	return runner
}
